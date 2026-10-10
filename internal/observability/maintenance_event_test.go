package observability

import (
	"sync"
	"testing"
)

func TestMaintenanceEventClosedCatalogAndReplay(t *testing.T) {
	for _, state := range []string{"enrolled", "disabled", "ready", "degraded", "backfilling", "indexing", "refreshing", "cache_schema_blocked"} {
		for _, failure := range []string{"", "cache", "identity", "content", "frontier", "embedding", "retry", "sync_schedule", "rag_schedule", "repository_docs", "cache_schema_blocked", "operation_failed"} {
			e := executionFixtureEvent(MaintenanceStateChanged, ExecutionDetails{Phase: "maintenance", MaintenanceScope: "registration", PreviousState: "absent", Outcome: state, FailureClass: failure})
			e.JobRef = ""
			e.CacheRef, e.RegistrationRef, e.RepoRef = KnownRef("cache", "fixture").value, KnownRef("registration", "fixture").value, KnownRef("repo", "fixture/repository").value
			data, err := marshalEvent(e)
			var replay Event
			if err != nil || decodeStrict(data, &replay) != nil || replay != e {
				t.Fatal("maintenance event failed strict replay")
			}
			for _, mutate := range []func(*Event){
				func(e *Event) { e.CorrelationRef = "" }, func(e *Event) { e.CacheRef = "" }, func(e *Event) { e.RegistrationRef = "" }, func(e *Event) { e.RepoRef = "" },
				func(e *Event) { e.JobRef = KnownRef("job", "fixture").value }, func(e *Event) { e.StageRef = KnownRef("stage", "fixture").value },
				func(e *Event) { e.Phase = "fetch" }, func(e *Event) { e.Collection = "issues" }, func(e *Event) { e.WorkKind = "sync" }, func(e *Event) { e.Attempt = 1 },
				func(e *Event) { e.AdminRoute = "read" }, func(e *Event) { e.Legacy = true }, func(e *Event) { e.MaintenanceScope = "synthetic-private" },
				func(e *Event) { e.PreviousState = "synthetic-private" }, func(e *Event) { e.Outcome = "synthetic-private" }, func(e *Event) { e.FailureClass = "synthetic-private" },
				func(e *Event) { e.PreviousFailureClass = "synthetic-private" }, func(e *Event) { e.PreviousFailureClass = "cache" },
				func(e *Event) { e.PreviousState, e.PreviousFailureClass = e.Outcome, e.FailureClass },
			} {
				invalid := e
				mutate(&invalid)
				if invalid.valid() {
					t.Fatal("maintenance catalog accepted raw or invented dimensions")
				}
			}
		}
	}
	e := executionFixtureEvent(JobAdmitted, ExecutionDetails{Phase: "admission", Outcome: "queued"})
	if !e.valid() {
		t.Fatal("negative fixture baseline invalid")
	}
	for _, mutate := range []func(*Event){func(e *Event) { e.MaintenanceScope = "registration" }, func(e *Event) { e.PreviousState = "ready" }, func(e *Event) { e.PreviousFailureClass = "cache" }} {
		invalid := e
		mutate(&invalid)
		if invalid.valid() {
			t.Fatal("another execution code admitted maintenance-only dimensions")
		}
	}
}

func TestMaintenanceEventCorrelationAtomicAndDistinct(t *testing.T) {
	c := New(Config{Directory: testDirectory(t), ManagedOutput: true})
	defer flush(t, c, true)
	ready(t, c)
	refs := make(chan Ref, 128)
	var workers sync.WaitGroup
	// Identity allocation must not wait for an observation reader, including
	// when multiple registration owners finish in the same boot.
	c.mu.Lock()
	for i := 0; i < cap(refs); i++ {
		workers.Add(1)
		go func() { defer workers.Done(); refs <- c.MaintenanceTransitionCorrelation() }()
	}
	workers.Wait()
	admin := c.AdminRequestCorrelation()
	c.mu.Unlock()
	close(refs)
	seen := map[string]bool{admin.value: true}
	for ref := range refs {
		if ref.value == "" || !validRef(ref.value, "correlation") || seen[ref.value] {
			t.Fatal("maintenance correlation missing, reused or shared with Admin identity")
		}
		seen[ref.value] = true
	}
}
