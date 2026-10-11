//go:build darwin || linux

package observability

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestEmitterDoesNotWaitForReaderAndLossSurvivesRestart(t *testing.T) {
	path := testDirectory(t)
	c := New(Config{Directory: path, ManagedOutput: true})
	defer flush(t, c, true)
	ready(t, c)
	await(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.events) > 0
	})
	before, err := c.Query(Filter{}, "", 200)
	if err != nil {
		t.Fatal("baseline cursor unavailable")
	}
	c.mu.Lock()
	// Rejected untrusted input is not a validated execution event and must
	// neither enter storage nor manufacture an execution-loss boundary.
	if c.Emit(Code("synthetic-untrusted-code"), Context{}) || c.Emit(RecoveryFailed, Context{Repo: Ref{"synthetic-untrusted-ref"}}) {
		c.mu.Unlock()
		t.Fatal("untrusted evidence admitted")
	}
	if c.pendingLoss.Load() != 0 {
		c.mu.Unlock()
		t.Fatal("untrusted input changed execution observation")
	}
	done := make(chan struct{})
	var accepted atomic.Bool
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			if c.Emit(RecoveryFinished, Context{}) {
				accepted.Store(true)
			}
		}
	}()
	// The reader mutex remains held until every emission finishes. This
	// barrier, rather than a timing comparison, proves execution isolation.
	select {
	case <-done:
		c.mu.Unlock()
	case <-time.After(time.Second):
		c.mu.Unlock()
		t.Fatal("event admission waited for an observation reader")
	}
	if accepted.Load() {
		t.Fatal("contended admission bypassed queue synchronization")
	}
	coverage := c.Coverage()
	if coverage.State != "partial" || coverage.Dropped != 100 || coverage.LossEpoch != before.Coverage.LossEpoch+100 {
		t.Fatal("bounded contention loss was not accounted")
	}
	_, err = c.Query(Filter{}, before.Cursor, 200)
	var queryErr QueryError
	if !errors.As(err, &queryErr) || queryErr.Code != "snapshot_required" || queryErr.Reason != "generation_or_loss_changed" {
		t.Fatal("pre-loss cursor did not expose the missing evidence")
	}
	flush(t, c, true)
	restarted := New(Config{Directory: path, ManagedOutput: true})
	defer flush(t, restarted, true)
	ready(t, restarted)
	flush(t, restarted, true)
	got := restarted.Coverage()
	if got.State != "partial" || got.Dropped != coverage.Dropped || got.LossEpoch != coverage.LossEpoch {
		t.Fatal("restart erased or recounted known contention loss")
	}
	inventory(t, path)
}

func TestConcurrentEmissionReadersAndCheckpointAccountEveryAttempt(t *testing.T) {
	c := New(Config{Directory: testDirectory(t), ManagedOutput: true})
	defer flush(t, c, true)
	ready(t, c)
	var workers sync.WaitGroup
	var accepted atomic.Uint64
	for i := 0; i < 4; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 100; j++ {
				if c.Emit(RecoveryFinished, Context{}) {
					accepted.Add(1)
				}
			}
		}()
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 100; j++ {
				_ = c.Coverage()
				_, _ = c.Query(Filter{}, "", 1)
			}
		}()
	}
	workers.Wait()
	flush(t, c, true)
	coverage := c.Coverage()
	if accepted.Load()+coverage.Dropped != 400 {
		t.Fatal("contention lost or counted an attempt twice")
	}
	var count uint64
	cursor := ""
	for {
		page, err := c.Query(Filter{Code: RecoveryFinished}, cursor, 200)
		if err != nil {
			t.Fatal("retained events unavailable after checkpoint")
		}
		count += uint64(len(page.Events))
		if !page.HasMore {
			break
		}
		if page.Cursor == cursor {
			t.Fatal("bounded scan stopped advancing")
		}
		cursor = page.Cursor
	}
	if count != accepted.Load() {
		t.Fatal("admitted events were lost while readers contended")
	}
}

func TestClosedCollectorRejectsContentionWithoutChangingLossBoundary(t *testing.T) {
	path := testDirectory(t)
	c := New(Config{Directory: path, ManagedOutput: true})
	defer flush(t, c, true)
	ready(t, c)
	flush(t, c, true)
	baseline, err := c.Query(Filter{}, "", 200)
	if err != nil {
		t.Fatal("closed baseline unavailable")
	}
	c.mu.Lock()
	for i := 0; i < 100; i++ {
		if c.Emit(RecoveryFinished, Context{}) {
			c.mu.Unlock()
			t.Fatal("closed collector admitted execution evidence")
		}
	}
	c.mu.Unlock()
	// Model an already validated failed-TryLock producer preempted just
	// before its Add: a late Add after the seal is not published as loss.
	c.pendingLoss.Add(1)
	if c.Coverage() != baseline.Coverage {
		t.Fatal("closed rejection changed published loss coverage")
	}
	if _, err := c.Query(Filter{}, baseline.Cursor, 200); err != nil {
		t.Fatal("closed rejection invalidated a clean cursor")
	}
	restarted := New(Config{Directory: path, ManagedOutput: true})
	defer flush(t, restarted, true)
	ready(t, restarted)
	flush(t, restarted, true)
	if restarted.Coverage().Dropped != baseline.Coverage.Dropped || restarted.Coverage().LossEpoch != baseline.Coverage.LossEpoch {
		t.Fatal("restart changed the sealed loss boundary")
	}
}

func TestCloseSealsLossBeforeShutdownPersistence(t *testing.T) {
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
	await(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.events) > 0
	})
	// A validated loss before sealing must be preserved, unlike late Add.
	prior := c.Coverage()
	c.pendingLoss.Add(7)
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
	baseline := c.Coverage()
	if baseline.Dropped != prior.Dropped+7 || baseline.LossEpoch != prior.LossEpoch+7 {
		t.Fatal("pre-seal loss was not folded exactly once")
	}
	c.mu.Lock()
	accepted := c.Emit(RecoveryFinished, Context{})
	c.mu.Unlock()
	c.pendingLoss.Add(3)
	if accepted || c.Coverage() != baseline {
		t.Fatal("shutdown rejection changed the sealed loss boundary")
	}
	releaseOnce.Do(func() { close(release) })
	if !<-done {
		t.Fatal("shutdown did not finish after releasing persistence")
	}
	restarted := New(Config{Directory: path, ManagedOutput: true})
	defer flush(t, restarted, true)
	ready(t, restarted)
	flush(t, restarted, true)
	if restarted.Coverage().Dropped != baseline.Dropped || restarted.Coverage().LossEpoch != baseline.LossEpoch {
		t.Fatal("final checkpoint lost or recounted sealed losses")
	}
}
