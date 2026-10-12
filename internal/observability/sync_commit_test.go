//go:build darwin || linux

package observability

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestSyncCommitBufferOneTurnScopeAndSeal(t *testing.T) {
	// Park only the private storage opener, not the product execution path, so
	// positive admission is deterministic without retrying a one-shot Flush.
	gate := make(chan struct{})
	c := newCollector(Config{Directory: testDirectory(t), ManagedOutput: true}, func(path string) (*storage, []Event, bool, error) {
		<-gate
		return openStorage(path)
	})
	refs := Context{Job: KnownRef("job", "fixture-job"), Correlation: KnownRef("correlation", "fixture-job")}
	ctx, buffer := DeferSyncCommit(WithObservation(context.Background(), c, refs))
	ConfirmSyncCommit(ctx, "synthetic-unsupported")
	rebound := refs
	rebound.Job = KnownRef("job", "different-job")
	ConfirmSyncCommit(WithObservation(ctx, c, rebound), "issues")
	if buffer.mark.Load() != nil {
		t.Fatal("unknown collection or rebound job marked the turn")
	}
	ConfirmSyncCommit(ctx, "wiki")
	ConfirmSyncCommit(ctx, "issues") // one turn may confirm at most once
	if c.queued != 0 {
		t.Fatal("confirmation published before caller release")
	}
	buffer.Flush()
	buffer.Flush()
	ConfirmSyncCommit(ctx, "pulls")
	buffer.Flush()
	if c.queued != 1 {
		t.Fatal("sealed turn emitted duplicate or late confirmation")
	}
	close(gate)
	flush(t, c, true)
	page, err := c.Query(Filter{Code: SyncCommitted}, "", 20)
	if err != nil || len(page.Events) != 1 || page.Events[0].Collection != "wiki" || page.Events[0].JobRef != refs.Job.value || page.Events[0].CorrelationRef != refs.Correlation.value {
		t.Fatal("confirmed turn lost its immutable scope")
	}
	unbound := context.Background()
	got, absent := DeferSyncCommit(unbound)
	if got != unbound || absent != nil {
		t.Fatal("unobserved commit changed context")
	}
	absent.Flush()
	ConfirmSyncCommit(unbound, "issues")
}

func TestSyncCommitBufferRaceAndContentionNeverRetry(t *testing.T) {
	path := testDirectory(t)
	c := New(Config{Directory: path, ManagedOutput: true})
	defer flush(t, c, true)
	ready(t, c)
	ctx, buffer := DeferSyncCommit(WithObservation(context.Background(), c, Context{Job: KnownRef("job", "fixture"), Correlation: KnownRef("correlation", "fixture")}))
	before := c.admissionDrops.Load()
	baseline := c.Coverage()
	ConfirmSyncCommit(ctx, "issues")
	c.mu.Lock()
	var workers sync.WaitGroup
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); buffer.Flush(); ConfirmSyncCommit(ctx, "issues") }()
	}
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	select {
	case <-done:
		c.mu.Unlock()
	case <-time.After(time.Second):
		c.mu.Unlock()
		<-done
		t.Fatal("confirmed commit observation waited for a reader")
	}
	if c.admissionDrops.Load()-before != 1 {
		t.Fatal("one rejected confirmation was retried or hidden")
	}
	buffer.Flush()
	flush(t, c, true)
	page, err := c.Query(Filter{Code: SyncCommitted}, "", 20)
	if err != nil || len(page.Events) != 0 || page.Coverage.State != "partial" || page.Coverage.Reason != "observation_contention" || page.Coverage.Dropped != baseline.Dropped+1 || page.Coverage.LossEpoch != baseline.LossEpoch+1 {
		t.Fatal("contention loss fabricated a retained commit")
	}
	restarted := New(Config{Directory: path, ManagedOutput: true})
	defer flush(t, restarted, true)
	ready(t, restarted)
	flush(t, restarted, true)
	retained, err := restarted.Query(Filter{Code: SyncCommitted}, "", 20)
	if err != nil || len(retained.Events) != 0 || retained.Coverage.State != "partial" || retained.Coverage.Dropped != page.Coverage.Dropped || retained.Coverage.LossEpoch != page.Coverage.LossEpoch {
		t.Fatal("restart erased, retried or recounted a rejected commit observation")
	}
}
