//go:build darwin || linux

package observability

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestUnconfirmedExecutionLossSealRestartAndCursor(t *testing.T) {
	path := testDirectory(t)
	c := New(Config{Directory: path, ManagedOutput: true})
	defer flush(t, c, true)
	ready(t, c)
	admitFixture(t, c, RecoveryFinished, Context{})
	await(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.events) > 0
	})
	before, err := c.Query(Filter{}, "", 100)
	if err != nil {
		t.Fatal("fixture cursor unavailable")
	}
	admissionDrops := c.admissionDrops.Load()
	c.mu.Lock()
	done := make(chan struct{})
	go func() {
		c.UnconfirmedExecutions(0)
		c.UnconfirmedExecutions(7)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		c.mu.Unlock()
		t.Fatal("boundary loss waited on observation lock")
	}
	c.mu.Unlock()
	if c.admissionDrops.Load() != admissionDrops {
		t.Fatal("unconfirmed boundary counted as rejected admission")
	}
	if _, err := c.Query(Filter{}, before.Cursor, 100); err == nil {
		t.Fatal("loss did not invalidate the previous cursor")
	}
	flush(t, c, true)
	baseline := c.Coverage()
	if baseline.State != "partial" || baseline.Reason != "execution_boundary_unconfirmed" || baseline.Dropped != before.Coverage.Dropped+7 || baseline.LossEpoch != before.Coverage.LossEpoch+7 {
		t.Fatal("unconfirmed boundary loss not folded exactly once")
	}
	c.UnconfirmedExecution()
	// A producer paused between the seal check and Add cannot publish a
	// post-seal loss, even when it resumes after shutdown persistence.
	c.pendingBoundaryLoss.Add(3)
	if c.Coverage() != baseline {
		t.Fatal("post-seal loss changed published coverage")
	}
	restarted := New(Config{Directory: path, ManagedOutput: true})
	defer flush(t, restarted, true)
	ready(t, restarted)
	flush(t, restarted, true)
	got := restarted.Coverage()
	if got.Dropped != baseline.Dropped || got.LossEpoch != baseline.LossEpoch {
		t.Fatal("restart lost or recounted sealed boundary loss")
	}
}

func TestCloseSealsBoundaryLossBeforeShutdownPersistence(t *testing.T) {
	path := testDirectory(t)
	var pause atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	var enteredOnce, releaseOnce sync.Once
	c := newCollector(Config{Directory: path, ManagedOutput: true}, func(path string) (*storage, []Event, bool, error) {
		s, events, unclean, err := openStorage(path)
		if err == nil {
			s.appendFault = func() error {
				if pause.Load() {
					enteredOnce.Do(func() { close(entered) })
					<-release
				}
				return nil
			}
		}
		return s, events, unclean, err
	})
	defer flush(t, c, true)
	defer releaseOnce.Do(func() { close(release) })
	ready(t, c)
	admitFixture(t, c, RecoveryFinished, Context{})
	await(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.events) > 0
	})
	prior := c.Coverage()
	c.UnconfirmedExecution()
	pause.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan bool, 1)
	go func() { done <- c.Close(ctx, true) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("shutdown persistence barrier unavailable")
	}
	// The append barrier may catch a queued event before Close acquires the
	// mutex. Observe the actual seal before injecting a post-seal increment.
	await(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.closing && c.pendingBoundaryLoss.Load()&admissionClosed != 0
	})
	baseline := c.Coverage()
	if baseline.Dropped != prior.Dropped+1 || baseline.LossEpoch != prior.LossEpoch+1 {
		t.Fatal("pre-seal boundary loss was not folded exactly once")
	}
	c.UnconfirmedExecution()
	c.pendingBoundaryLoss.Add(3)
	if c.Coverage() != baseline {
		t.Fatal("shutdown rejection changed sealed boundary loss")
	}
	releaseOnce.Do(func() { close(release) })
	if !<-done {
		t.Fatal("shutdown did not finish after releasing persistence")
	}
	restarted := New(Config{Directory: path, ManagedOutput: true})
	defer flush(t, restarted, true)
	ready(t, restarted)
	flush(t, restarted, true)
	if got := restarted.Coverage(); got.Dropped != baseline.Dropped || got.LossEpoch != baseline.LossEpoch {
		t.Fatal("checkpoint lost or recounted sealed boundary loss")
	}
}
