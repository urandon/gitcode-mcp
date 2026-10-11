//go:build darwin || linux

package observability

import (
	"context"
	"sync"
	"testing"
)

func TestJobRecoveryCatalogAndNonblockingRejection(t *testing.T) {
	for _, tc := range []struct{ outcome, failure string }{
		{"resumed", ""}, {"reconciled", ""}, {"rejected", "cancelled"}, {"rejected", "interrupted"}, {"rejected", "operation_failed"},
	} {
		e := executionFixtureEvent(JobRecovered, ExecutionDetails{Phase: "recovery", Outcome: tc.outcome, FailureClass: tc.failure})
		data, err := marshalEvent(e)
		var decoded Event
		if err != nil || decodeStrict(data, &decoded) != nil || decoded != e {
			t.Fatal("recovery catalog failed strict replay")
		}
		for _, mutate := range []func(*Event){
			func(e *Event) { e.JobRef = "" }, func(e *Event) { e.CorrelationRef = "" },
			func(e *Event) { e.Phase = "completion" }, func(e *Event) { e.Collection = "issues" },
			func(e *Event) { e.Attempt = 1 }, func(e *Event) { e.StageRef = KnownRef("stage", "fixture").value },
			func(e *Event) { e.WorkKind = "sync" }, func(e *Event) { e.Legacy = true },
			func(e *Event) { e.Outcome = "synthetic-private" }, func(e *Event) { e.FailureClass = "synthetic-private" },
		} {
			invalid := e
			mutate(&invalid)
			if invalid.valid() {
				t.Fatal("recovery catalog accepted invented execution or private dimensions")
			}
		}
	}
	c := New(Config{Directory: testDirectory(t), ManagedOutput: true})
	defer flush(t, c, true)
	ready(t, c)
	ctx := WithObservation(context.Background(), c, stageObservationRefs())
	before := c.admissionDrops.Load()
	c.mu.Lock()
	var workers sync.WaitGroup
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			EmitFromContext(ctx, JobRecovered, ExecutionDetails{Phase: "recovery", Outcome: "resumed"})
		}()
	}
	workers.Wait()
	c.mu.Unlock()
	if c.admissionDrops.Load()-before != 16 {
		t.Fatal("recovery observations waited, retried or hid rejection")
	}
	flush(t, c, true)
	page, err := c.Query(Filter{Code: JobRecovered}, "", 20)
	if err != nil || len(page.Events) != 0 || page.Coverage.Dropped != 16 || page.Coverage.State != "partial" {
		t.Fatal("recovery rejection fabricated a retained boundary")
	}
}

func TestRecoveryBulkMetricLossNeverWaitsOrInventsSeries(t *testing.T) {
	m := NewMetrics("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", metricsTime())
	m.mu.Lock()
	var workers sync.WaitGroup
	for i := 0; i < 16; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); m.LostObservations(0); m.LostObservations(7) }()
	}
	workers.Wait()
	m.mu.Unlock()
	m.LostObservation()
	m.SampleAt(metricsTime(), nil)
	got := m.Snapshot()
	if got.Dropped != 113 || got.State != "partial" {
		t.Fatal("bulk recovery metric loss was blocked or recounted")
	}
}

func TestJobRecoveryRestartPreservesOriginalCorrelation(t *testing.T) {
	dir := testDirectory(t)
	c := New(Config{Directory: dir, ManagedOutput: true})
	defer flush(t, c, true)
	ready(t, c)
	refs := stageObservationRefs()
	refs.Stage = Ref{}
	refs.Correlation = KnownRef("correlation", c.BootID()+"\x00fixture-job")
	refs.ExecutionDetails = ExecutionDetails{Phase: "recovery", Outcome: "reconciled"}
	admitFixture(t, c, JobRecovered, refs)
	flush(t, c, true)
	restarted := New(Config{Directory: dir, ManagedOutput: true})
	defer flush(t, restarted, true)
	ready(t, restarted)
	flush(t, restarted, true)
	page, err := restarted.Query(Filter{Code: JobRecovered}, "", 20)
	if err != nil || len(page.Events) != 1 || page.Events[0].BootID != c.BootID() || page.Events[0].CorrelationRef != refs.Correlation.value || page.Events[0].ExecutionDetails != refs.ExecutionDetails {
		t.Fatal("restart lost or manufactured recovery correlation")
	}
	for _, code := range []Code{JobAdmitted, JobTerminal, ProviderAttempt, SyncCommitted} {
		page, err := restarted.Query(Filter{Code: code}, "", 20)
		if err != nil || len(page.Events) != 0 {
			t.Fatal("recovery replay invented new execution evidence")
		}
	}
}
