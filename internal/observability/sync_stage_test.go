//go:build darwin || linux

package observability

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func stageObservationRefs() Context {
	return Context{Job: KnownRef("job", "fixture-job"), Cache: KnownRef("cache", "fixture-cache"), Registration: KnownRef("registration", "fixture-registration"), Repo: KnownRef("repo", "fixture/repository"), Correlation: KnownRef("correlation", "fixture-correlation"), Stage: KnownRef("stage", "fixture-stage")}
}

func TestSyncStageCatalogFiniteReplay(t *testing.T) {
	for _, tc := range []struct{ outcome, failure string }{
		{"staged", ""}, {"committing", ""}, {"committed", ""}, {"waiting_commit", "writer_busy"}, {"superseded", "superseded"},
		{"rejected", "cancelled"}, {"rejected", "invalid"}, {"rejected", "writer_busy"}, {"rejected", "operation_failed"},
	} {
		refs := stageObservationRefs()
		e := executionFixtureEvent(SyncStageChanged, ExecutionDetails{Phase: "staging", Collection: "issues", Outcome: tc.outcome, FailureClass: tc.failure, Attempt: MaxExecutionAttempt})
		e.StageRef, e.CacheRef, e.RegistrationRef, e.RepoRef = refs.Stage.value, refs.Cache.value, refs.Registration.value, refs.Repo.value
		b, err := marshalEvent(e)
		var decoded Event
		if err != nil || decodeStrict(b, &decoded) != nil || decoded != e || !decoded.valid() {
			t.Fatal("confirmed stage state failed strict round trip")
		}
		for _, mutate := range []func(*Event){
			func(e *Event) { e.StageRef = "" }, func(e *Event) { e.StageRef = "synthetic-private" },
			func(e *Event) { e.JobRef = "" }, func(e *Event) { e.CacheRef = "" },
			func(e *Event) { e.RegistrationRef = "" }, func(e *Event) { e.RepoRef = "" }, func(e *Event) { e.CorrelationRef = "" },
			func(e *Event) { e.Phase = "commit" }, func(e *Event) { e.Collection = "synthetic-private" },
			func(e *Event) { e.Outcome = "synthetic-private" }, func(e *Event) { e.FailureClass = "synthetic-private" },
			func(e *Event) { e.Attempt++ }, func(e *Event) { e.WorkKind = "sync" }, func(e *Event) { e.Legacy = true },
		} {
			invalid := e
			mutate(&invalid)
			if invalid.valid() {
				t.Fatal("stage catalog accepted absent authority or unbounded content")
			}
		}
	}
	for code := range catalog {
		if code != SyncStageChanged && (Event{Code: code, StageRef: stageObservationRefs().Stage.value}).validExecution() {
			t.Fatal("stage identity escaped its code-specific contract")
		}
	}
}

func TestSyncStageBufferOneTurnScopeSealAndRestart(t *testing.T) {
	gate := make(chan struct{})
	dir := testDirectory(t)
	c := newCollector(Config{Directory: dir, ManagedOutput: true}, func(path string) (*storage, []Event, bool, error) {
		<-gate
		return openStorage(path)
	})
	refs := stageObservationRefs()
	ctx, buffer := DeferSyncStage(WithObservation(context.Background(), c, refs))
	details := ExecutionDetails{Phase: "staging", Collection: "wiki", Outcome: "committing"}
	for _, mutate := range []func(*Context){
		func(r *Context) { r.Job = KnownRef("job", "other") }, func(r *Context) { r.Cache = KnownRef("cache", "other") },
		func(r *Context) { r.Registration = KnownRef("registration", "other") }, func(r *Context) { r.Repo = KnownRef("repo", "other") },
		func(r *Context) { r.Stage = Ref{} },
	} {
		wrong := refs
		mutate(&wrong)
		ConfirmSyncStage(ctx, wrong, details)
	}
	ConfirmSyncStage(WithObservation(ctx, c, Context{}), refs, details)
	if buffer.mark.Load() != nil {
		t.Fatal("mismatched journal identity marked a commit turn")
	}
	ConfirmSyncStage(ctx, refs, details)
	ConfirmSyncStage(ctx, refs, ExecutionDetails{Phase: "staging", Collection: "issues", Outcome: "committed"})
	if c.queued != 0 {
		t.Fatal("stage evidence emitted before coordinator release")
	}
	buffer.Flush()
	buffer.Flush()
	ConfirmSyncStage(ctx, refs, details)
	buffer.Flush()
	if c.queued != 1 {
		t.Fatal("duplicate or late stage confirmation changed one-turn evidence")
	}
	close(gate)
	flush(t, c, true)
	restarted := New(Config{Directory: dir, ManagedOutput: true})
	ready(t, restarted)
	flush(t, restarted, true)
	page, err := restarted.Query(Filter{Code: SyncStageChanged}, "", 20)
	if err != nil || len(page.Events) != 1 || page.Events[0].ExecutionDetails != details || page.Events[0].StageRef != refs.Stage.value || page.Events[0].CorrelationRef != refs.Correlation.value {
		t.Fatal("stage replay lost confirmed opaque identity or manufactured another event")
	}
	data, _ := json.Marshal(page.Events)
	for _, private := range []string{"fixture-job", "fixture-cache", "fixture-registration", "fixture/repository", "fixture-stage", "fixture-correlation"} {
		if strings.Contains(string(data), private) {
			t.Fatal("stage event exposed authority input")
		}
	}
	unbound := context.Background()
	got, absent := DeferSyncStage(unbound)
	if got != unbound || absent != nil {
		t.Fatal("stage observation altered an unobserved execution")
	}
	absent.Flush()
	ConfirmSyncStage(unbound, refs, details)
}

func TestSyncStageBufferRaceAndContentionNeverRetry(t *testing.T) {
	c := New(Config{Directory: testDirectory(t), ManagedOutput: true})
	ready(t, c)
	refs := stageObservationRefs()
	ctx, buffer := DeferSyncStage(WithObservation(context.Background(), c, refs))
	details := ExecutionDetails{Phase: "staging", Collection: "issues", Outcome: "committing"}
	ConfirmSyncStage(ctx, refs, details)
	before := c.admissionDrops.Load()
	c.mu.Lock()
	var workers sync.WaitGroup
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); buffer.Flush(); ConfirmSyncStage(ctx, refs, details) }()
	}
	workers.Wait() // holding the reader lock must not block any producer
	c.mu.Unlock()
	buffer.Flush()
	if c.admissionDrops.Load()-before != 1 {
		t.Fatal("one rejected stage confirmation was retried or hidden")
	}
	flush(t, c, true)
	page, err := c.Query(Filter{Code: SyncStageChanged}, "", 20)
	if err != nil || len(page.Events) != 0 || page.Coverage.State != "partial" {
		t.Fatal("stage rejection fabricated retained evidence")
	}
}
