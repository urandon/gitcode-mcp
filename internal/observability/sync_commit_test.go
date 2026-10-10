//go:build darwin || linux

package observability

import (
	"context"
	"sync"
	"testing"
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
	c := New(Config{Directory: testDirectory(t), ManagedOutput: true})
	ready(t, c)
	ctx, buffer := DeferSyncCommit(WithObservation(context.Background(), c, Context{Job: KnownRef("job", "fixture"), Correlation: KnownRef("correlation", "fixture")}))
	before := c.admissionDrops.Load()
	ConfirmSyncCommit(ctx, "issues")
	c.mu.Lock()
	var workers sync.WaitGroup
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); buffer.Flush(); ConfirmSyncCommit(ctx, "issues") }()
	}
	workers.Wait() // would deadlock if observation waited for a reader
	c.mu.Unlock()
	if c.admissionDrops.Load()-before != 1 {
		t.Fatal("one rejected confirmation was retried or hidden")
	}
	buffer.Flush()
	flush(t, c, true)
	page, err := c.Query(Filter{Code: SyncCommitted}, "", 20)
	if err != nil || len(page.Events) != 0 || page.Coverage.State != "partial" {
		t.Fatal("contention loss fabricated a retained commit")
	}
}
