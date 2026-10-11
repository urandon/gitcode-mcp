//go:build darwin || linux

package observability

import (
	"sync"
	"testing"
)

func TestAdminRequestClosedCatalogReplayAndNonblockingLoss(t *testing.T) {
	for _, route := range []string{"session", "read", "control", "search", "events", "assets", "unknown"} {
		for _, outcome := range []string{"succeeded", "failed", "rejected", "cancelled"} {
			e := executionFixtureEvent(AdminRequestCompleted, ExecutionDetails{Phase: "admin", AdminRoute: route, Outcome: outcome})
			e.JobRef = ""
			data, err := marshalEvent(e)
			var replay Event
			if err != nil || decodeStrict(data, &replay) != nil || replay != e {
				t.Fatal("Admin catalog failed strict replay")
			}
			for _, mutate := range []func(*Event){
				func(e *Event) { e.CorrelationRef = "" }, func(e *Event) { e.JobRef = KnownRef("job", "fixture").value },
				func(e *Event) { e.CacheRef = KnownRef("cache", "fixture").value }, func(e *Event) { e.RepoRef = KnownRef("repo", "fixture").value },
				func(e *Event) { e.RegistrationRef = KnownRef("registration", "fixture").value }, func(e *Event) { e.StageRef = KnownRef("stage", "fixture").value },
				func(e *Event) { e.Phase = "fetch" }, func(e *Event) { e.Collection = "issues" }, func(e *Event) { e.Attempt = 1 },
				func(e *Event) { e.WorkKind = "other" }, func(e *Event) { e.FailureClass = "operation_failed" }, func(e *Event) { e.Legacy = true },
				func(e *Event) { e.AdminRoute = "synthetic-private" }, func(e *Event) { e.Outcome = "synthetic-private" },
			} {
				invalid := e
				mutate(&invalid)
				if invalid.valid() {
					t.Fatal("Admin catalog admitted invented execution/private fields")
				}
			}
		}
	}
	for _, code := range []Code{Boot, JobAdmitted, JobRecovered, ProviderAttempt, WriterContended, SyncCommitted, SyncStageChanged} {
		e := executionFixtureEvent(code, ExecutionDetails{AdminRoute: "read"})
		if e.valid() {
			t.Fatal("another code admitted Admin-only dimensions")
		}
	}
	c := New(Config{Directory: testDirectory(t), ManagedOutput: true})
	defer flush(t, c, true)
	ready(t, c)
	before := c.admissionDrops.Load()
	c.mu.Lock()
	var workers sync.WaitGroup
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			c.Emit(AdminRequestCompleted, Context{Correlation: c.AdminRequestCorrelation(), ExecutionDetails: ExecutionDetails{Phase: "admin", AdminRoute: "read", Outcome: "succeeded"}})
		}()
	}
	workers.Wait()
	c.mu.Unlock()
	if c.admissionDrops.Load()-before != 16 {
		t.Fatal("Admin events waited, retried or hid contention")
	}
	flush(t, c, true)
	page, err := c.Query(Filter{Code: AdminRequestCompleted}, "", 20)
	if err != nil || len(page.Events) != 0 || page.Coverage.Dropped != 16 || page.Coverage.State != "partial" {
		t.Fatal("Admin contention invented retained requests")
	}
}

func TestAdminRequestCorrelationAtomicAcrossControllersAndReaders(t *testing.T) {
	c := New(Config{Directory: testDirectory(t), ManagedOutput: true})
	defer flush(t, c, true)
	ready(t, c)
	var workers sync.WaitGroup
	refs := make(chan Ref, 128)
	c.mu.Lock()
	for i := 0; i < 128; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); refs <- c.AdminRequestCorrelation() }()
	}
	workers.Wait()
	c.mu.Unlock()
	close(refs)
	seen := map[string]bool{}
	for ref := range refs {
		if ref.value == "" || !validRef(ref.value, "correlation") || seen[ref.value] {
			t.Fatal("atomic request correlation missing/reused")
		}
		seen[ref.value] = true
	}
}
