//go:build darwin || linux

package observability

import (
	"context"
	"sync"
	"testing"
)

func TestRetryOmissionQualifiedScopeNonblockingAndRestart(t *testing.T) {
	dir := testDirectory(t)
	c := New(Config{Directory: dir, ManagedOutput: true})
	ready(t, c)
	baseline := c.Coverage()
	refs := Context{Job: KnownRef("job", "fixture-job"), Cache: KnownRef("cache", "synthetic-private-cache"),
		Registration: KnownRef("registration", "fixture-registration"), Repo: KnownRef("repo", "fixture/repository"),
		Correlation: KnownRef("correlation", "fixture-correlation")}
	OmitRetryFromContext(context.Background())
	OmitRetryFromContext(WithObservation(context.Background(), nil, refs))
	for _, mutate := range []func(*Context){
		func(r *Context) { r.Job = Ref{} },
		func(r *Context) { r.Correlation = Ref{} },
		func(r *Context) { r.Job = Ref{value: "synthetic-private-job"} },
		func(r *Context) { r.Cache = KnownRef("repo", "wrong-kind") },
		func(r *Context) { r.Registration = Ref{value: "synthetic-private-registration"} },
		func(r *Context) { r.Repo = Ref{value: "synthetic-private-repo"} },
		func(r *Context) { r.Correlation = KnownRef("job", "wrong-kind") },
	} {
		wrong := refs
		mutate(&wrong)
		OmitRetryFromContext(WithObservation(context.Background(), c, wrong))
	}
	if c.pendingBoundaryLoss.Load() != 0 {
		t.Fatal("unbound or malformed retry scope poisoned coverage")
	}
	ctx := WithObservation(context.Background(), c, refs)
	c.mu.Lock()
	var workers sync.WaitGroup
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); OmitRetryFromContext(ctx) }()
	}
	workers.Wait() // omission never acquires a held collector lock
	if c.pendingBoundaryLoss.Load() != 16 || c.admissionDrops.Load() != 0 || c.queued != 0 {
		t.Fatal("retry omission was hidden, fabricated or counted as admission loss")
	}
	c.mu.Unlock()
	flush(t, c, true)
	OmitRetryFromContext(ctx) // a closed collector cannot manufacture later loss
	if c.pendingBoundaryLoss.Load() != admissionClosed {
		t.Fatal("closed retry observer changed the sealed loss boundary")
	}
	restarted := New(Config{Directory: dir, ManagedOutput: true})
	ready(t, restarted)
	flush(t, restarted, true)
	page, err := restarted.Query(Filter{Code: RetryAdmitted}, "", 20)
	if err != nil || len(page.Events) != 0 || page.Coverage.State != "partial" || page.Coverage.Dropped != baseline.Dropped+16 || page.Coverage.LossEpoch != baseline.LossEpoch+16 {
		t.Fatal("count-only retry omission lost durable partial coverage")
	}
}
