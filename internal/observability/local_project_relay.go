package observability

import (
	"context"
	"sync"
	"sync/atomic"
)

const LocalProjectRelayCapacity = 64
const LocalProjectRelayMaxLoss = uint64(1<<32 - 1)

// LocalProjectTransition is a closed, content-free owner boundary. It is not a
// receipt, current health, an arbitrary Event, or a caller-supplied correlation.
type LocalProjectTransition struct {
	ProjectRef string `json:"project_ref"`
	Code       Code   `json:"code"`
	Phase      string `json:"phase"`
	Outcome    string `json:"outcome"`
}

func (t LocalProjectTransition) Valid() bool {
	return t.ProjectRef != "" && validRef(t.ProjectRef, "project") && localAuthorityPhase(t.Code, t.Phase, t.Outcome)
}

// LocalProjectRelay is memory-only. Only a trusted native launcher may create
// it from its already validated immutable cohort. It cannot open files, probe
// providers, install a collector, or execute caller callbacks.
type LocalProjectRelay struct {
	mu       sync.Mutex
	projects map[string]bool
	events   chan LocalProjectTransition
	sealed   chan struct{}
	closed   bool
	loss     atomic.Uint64 // closed bit and frozen lower bound share ONE word
}

func NewLocalProjectRelay(admittedProjectIDs []string) *LocalProjectRelay {
	if len(admittedProjectIDs) == 0 || len(admittedProjectIDs) > 16 {
		return nil
	}
	r := &LocalProjectRelay{projects: map[string]bool{}, events: make(chan LocalProjectTransition, LocalProjectRelayCapacity), sealed: make(chan struct{})}
	for _, id := range admittedProjectIDs {
		ref := KnownRef("project", id).value
		if id == "" || r.projects[ref] {
			return nil
		}
		r.projects[ref] = true
	}
	return r
}

type localRelayContextKey struct{}

// WithLocalProjectRelay carries one concrete bounded native sink. It does not
// provide the general observation/metrics carrier or change execution policy.
func WithLocalProjectRelay(ctx context.Context, relay *LocalProjectRelay) context.Context {
	if relay == nil {
		return ctx
	}
	return context.WithValue(ctx, localRelayContextKey{}, relay)
}

func (r *LocalProjectRelay) emit(t LocalProjectTransition) {
	if r == nil || !t.Valid() || !r.projects[t.ProjectRef] {
		return
	}
	if !r.mu.TryLock() {
		r.omit()
		return
	}
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	select {
	case r.events <- t:
	default:
		r.omit()
	}
}

func (r *LocalProjectRelay) omit() {
	if r == nil {
		return
	}
	n := r.loss.Load()
	if n&admissionClosed == 0 && n < LocalProjectRelayMaxLoss {
		// One bounded attempt, never a producer retry loop. Failure means a
		// concurrent omission already advanced this lower bound, or Seal froze
		// it. Coverage stays partial without waiting for either owner.
		r.loss.CompareAndSwap(n, n+1)
	}
}

func (r *LocalProjectRelay) Events() <-chan LocalProjectTransition { return r.events }
func (r *LocalProjectRelay) Sealed() <-chan struct{}               { return r.sealed }

// Lost is a saturating lower bound, not an untrusted loop count. The transport
// sends cumulative loss; the parent folds a validated delta once per frame.
func (r *LocalProjectRelay) Lost() uint64 {
	return min(r.loss.Load()&^admissionClosed, LocalProjectRelayMaxLoss)
}

// Seal follows the real owner join. Producers never wait for this lock; late
// producers cannot panic on the closed channel or alter the published seal.
func (r *LocalProjectRelay) Seal() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	// Producers use one CAS, so a preempted omission cannot increment a sealed
	// word. Only this joined sealer retries; in production owner work has ended.
	for {
		n := r.loss.Load()
		if r.loss.CompareAndSwap(n, admissionClosed|min(n, LocalProjectRelayMaxLoss)) {
			break
		}
	}
	close(r.events)
	close(r.sealed)
}
