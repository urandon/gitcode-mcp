package observability

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

const qualificationWindowLimit = 10 * time.Second

const (
	qualificationRequested uint32 = iota
	qualificationActive
	qualificationReleased
	qualificationAutomatic
)

type qualificationWindow struct {
	ctx    context.Context
	ready  chan struct{}
	resume chan struct{}
	once   sync.Once
	state  atomic.Uint32
}

// endAutomatically shares the same terminal-state CAS as controlled release.
// Clearing owner alone is not a release result: the caller can race that cleanup.
func (w *qualificationWindow) endAutomatically() {
	w.state.CompareAndSwap(qualificationRequested, qualificationAutomatic)
	w.state.CompareAndSwap(qualificationActive, qualificationAutomatic)
}

func (w *qualificationWindow) release(cancel context.CancelFunc, stop <-chan struct{}, beforeTransition func()) bool {
	if w.ctx.Err() != nil {
		w.endAutomatically()
	}
	select {
	case <-stop:
		w.endAutomatically()
	default:
	}
	// A private deterministic interleaving seam; the real closure passes nil.
	if beforeTransition != nil {
		beforeTransition()
	}
	controlled := w.state.CompareAndSwap(qualificationActive, qualificationReleased)
	w.once.Do(func() { close(w.resume); cancel() })
	return controlled
}

// NewQualificationCollector opts an internal integration fixture into a
// bounded writer admission window. No product constructor/config/transport
// enables this seam. Persistence, Emit and queue limits are the real ones.
func NewQualificationCollector(cfg Config) *Collector {
	return newCollectorWithQualification(cfg, openStorage, true)
}

// PauseWriterForQualification acknowledges a boundary between completed
// writer sections, without holding any collector mutex. Producers still make
// exactly one nonblocking Emit call. The fixture must not read observations
// during the window. Resume, cancellation, a hard deadline, or Close ends it.
// The idempotent release reports whether the window was still active, so a
// fixture cannot mistake automatic expiry for a controlled admission phase.
func (c *Collector) PauseWriterForQualification(ctx context.Context) (func() bool, error) {
	return c.pauseWriterForQualification(ctx, qualificationWindowLimit)
}

func (c *Collector) pauseWriterForQualification(ctx context.Context, limit time.Duration) (func() bool, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if c.qualificationRequests == nil {
		return nil, QueryError{Code: "observation_unavailable", Reason: "qualification_disabled"}
	}
	windowCtx, cancel := context.WithTimeout(ctx, limit)
	request := &qualificationWindow{ctx: windowCtx, ready: make(chan struct{}), resume: make(chan struct{})}
	if !c.qualificationOwner.CompareAndSwap(nil, request) {
		cancel()
		return nil, QueryError{Code: "observation_busy", Reason: "qualification_window_active"}
	}
	resume := func() bool {
		return request.release(cancel, c.stop, nil)
	}
	fail := func(err error) (func() bool, error) {
		request.endAutomatically()
		resume()
		c.qualificationOwner.CompareAndSwap(request, nil)
		return nil, err
	}
	select {
	case <-c.stop:
		return fail(QueryError{Code: "observation_unavailable", Reason: "collector_closed"})
	default:
	}
	select {
	case c.qualificationRequests <- request:
	case <-windowCtx.Done():
		return fail(windowCtx.Err())
	case <-c.stop:
		return fail(QueryError{Code: "observation_unavailable", Reason: "collector_closed"})
	default:
		return fail(QueryError{Code: "observation_busy", Reason: "qualification_queue_busy"})
	}
	select {
	case <-request.ready:
		if windowCtx.Err() != nil {
			return fail(windowCtx.Err())
		}
		select {
		case <-c.stop:
			return fail(QueryError{Code: "observation_unavailable", Reason: "collector_closed"})
		default:
			return resume, nil
		}
	case <-windowCtx.Done():
		return fail(windowCtx.Err())
	case <-c.stop:
		return fail(QueryError{Code: "observation_unavailable", Reason: "collector_closed"})
	}
}

func (c *Collector) runQualificationWindow(request *qualificationWindow) {
	defer func() {
		request.endAutomatically()
		c.qualificationOwner.CompareAndSwap(request, nil)
	}()
	if request.ctx.Err() != nil {
		return
	}
	// Called only by the serial writer loop after all earlier write/bookkeeping
	// sections have returned. No lock is held across acknowledgement or wait.
	if !request.state.CompareAndSwap(qualificationRequested, qualificationActive) {
		return
	}
	close(request.ready)
	select {
	case <-request.resume:
	case <-request.ctx.Done():
		request.endAutomatically()
	case <-c.stop:
		request.endAutomatically()
	}
}
