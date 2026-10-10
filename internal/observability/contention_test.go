//go:build darwin || linux

package observability

import (
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
