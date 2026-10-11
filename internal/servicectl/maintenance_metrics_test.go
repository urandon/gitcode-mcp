package servicectl

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/observability"
)

func requireRetryProbe(t *testing.T, m *MaintenanceManager, want float64) {
	t.Helper()
	value := m.ScheduledRetryProbe()
	if value.Value == nil || *value.Value != want {
		t.Fatal("scheduled retry authority projection differs")
	}
}

func TestScheduledRetryProbeDeduplicatesJobAndRegistryAuthority(t *testing.T) {
	jobs, metrics := testJobMetrics(t, "")
	m := NewMaintenanceManager(Manager{}, jobs, "")
	now := time.Now().UTC()
	due := now.Add(-time.Second) // Due, but not admitted, remains scheduled.
	m.entries["reg"] = &MaintenanceEntry{CacheUUID: "cache", RepoID: "fixture/repository", Enabled: true,
		Policy:    MaintenancePolicy{SyncEnabled: true, RAGEnabled: true},
		SyncStage: MaintenanceStageState{RetryAfter: due}, RAGStage: MaintenanceStageState{RetryAfter: due},
		RepositoryDocs: &RepositoryDocsMaintenanceState{SourceRegistrationID: "docs", Stage: MaintenanceStageState{RetryAfter: due}}}
	m.sources["reg"] = map[string]*repositoryDocsRegisteredSource{"docs": {State: *m.entries["reg"].RepositoryDocs}}
	requireRetryProbe(t, m, 3)
	jobs.jobs["sync"] = &Job{ID: "sync", Type: SyncJobType, CacheUUID: "cache", RepoID: "fixture/repository", Status: JobStatusRunning,
		SyncCollections: []SyncCollectionView{{Collection: "wiki", Outcome: SyncCollectionRetryScheduled, RetryAfter: &due}, {Collection: "issues", Outcome: SyncCollectionRetryScheduled, RetryAfter: &due}},
		SyncStage:       &SyncStageView{Phase: SyncStageWaitingCommit, Collection: "wiki", RetryAfter: due}}
	// Two actual collection waits, not the registry's old sync backoff and
	// not a duplicate stage/collection view; RAG and docs each still wait.
	requireRetryProbe(t, m, 4)
	jobs.jobs["rag"] = &Job{ID: "rag", Type: RAGIndexJobType, CacheUUID: "cache", RepoID: "fixture/repository", Status: JobStatusQueued}
	jobs.jobs["docs"] = &Job{ID: "docs", Type: RepositoryDocsIndexJobType, CacheUUID: "cache", RepoID: "fixture/repository", SourceRegistrationID: "docs", Status: JobStatusRunning}
	requireRetryProbe(t, m, 2)
	jobs.jobs["sync"].SyncCollections = nil
	jobs.jobs["sync"].SyncStage.Phase = SyncStageCommitting
	requireRetryProbe(t, m, 0)
	jobs.jobs["sync"].Status = JobStatusCancelling
	jobs.jobs["sync"].SyncCollections = []SyncCollectionView{{Collection: "wiki", Outcome: SyncCollectionRetryScheduled, RetryAfter: &due}}
	requireRetryProbe(t, m, 0)
	// Observation never changes durable authority or admits a job.
	if len(jobs.jobs) != 3 || jobs.jobs["rag"].Status != JobStatusQueued {
		t.Fatal("probe changed execution")
	}
	values := jobs.MetricProbes()
	values["retry_scheduled"] = m.ScheduledRetryProbe()
	metrics.SampleAt(now, values)
	series := jobMetric(t, metrics, "retry_scheduled", nil)
	if series.Value == nil || *series.Value != 0 {
		t.Fatal("sampler lost retry probe")
	}
	data, _ := json.Marshal(metrics.Snapshot())
	for _, private := range []string{"fixture/repository", "\"cache\"", "\"docs\""} {
		if strings.Contains(string(data), private) {
			t.Fatal("retry probe leaked cohort identity")
		}
	}
}

func TestScheduledRetryProbeDisabledAndPeriodicAreNotRetries(t *testing.T) {
	jobs := NewJobManager("")
	m := NewMaintenanceManager(Manager{}, jobs, "")
	due := time.Now().UTC()
	m.entries["disabled"] = &MaintenanceEntry{Enabled: false, Policy: MaintenancePolicy{SyncEnabled: true, RAGEnabled: true}, SyncStage: MaintenanceStageState{RetryAfter: due}, RAGStage: MaintenanceStageState{RetryAfter: due}}
	m.entries["poll"] = &MaintenanceEntry{Enabled: true, NextReconcileAt: due, Policy: MaintenancePolicy{SyncEnabled: true, RAGEnabled: true}, RepositoryDocs: &RepositoryDocsMaintenanceState{NextPollAt: due}}
	m.entries["off"] = &MaintenanceEntry{Enabled: true, SyncStage: MaintenanceStageState{RetryAfter: due}, RAGStage: MaintenanceStageState{RetryAfter: due}}
	jobs.jobs["terminal"] = &Job{Status: JobStatusFailed, Type: SyncJobType, SyncStage: &SyncStageView{Phase: SyncStageWaitingCommit, RetryAfter: due}}
	requireRetryProbe(t, m, 0)
	if got := (*MaintenanceManager)(nil).ScheduledRetryProbe(); got.Value != nil {
		t.Fatal("missing authority fabricated zero")
	}
}

func TestScheduledRetryProbeDoesNotWaitOnEitherAuthority(t *testing.T) {
	jobs := NewJobManager("")
	m := NewMaintenanceManager(Manager{}, jobs, "")
	for _, locked := range []string{"registry", "jobs"} {
		if locked == "registry" {
			m.mu.Lock()
		} else {
			jobs.mu.Lock()
		}
		done := make(chan observability.ProbeValue, 1)
		go func() { done <- m.ScheduledRetryProbe() }()
		var failed bool
		select {
		case got := <-done:
			failed = got.Value != nil || got.Reason != "probe_unavailable"
		case <-time.After(time.Second):
			failed = true
		}
		if locked == "registry" {
			m.mu.Unlock()
		} else {
			jobs.mu.Unlock()
		}
		if failed {
			t.Fatal("busy authority waited or fabricated a zero")
		}
	}
	// Reverse lock ownership cannot deadlock a try-only nested probe.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	jobs.mu.Lock()
	if !m.mu.TryLock() {
		jobs.mu.Unlock()
		t.Fatal("probe retained registry lock")
	}
	m.mu.Unlock()
	jobs.mu.Unlock()
	if ctx.Err() != nil {
		t.Fatal("probe stalled")
	}
}

func TestScheduledRetryProbeCancelledSourceTombstonesAndStageOutcomes(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	for _, canonical := range []bool{false, true} {
		jobs := NewJobManager("")
		m := NewMaintenanceManager(Manager{}, jobs, "")
		entry := &MaintenanceEntry{RegistrationID: "reg", CacheUUID: "cache", RepoID: "fixture/repository", PathFingerprint: "current-authority", Enabled: true}
		m.entries["reg"] = entry
		state := RepositoryDocsMaintenanceState{SourceRegistrationID: "source", SourceRegistrationGeneration: 1, State: "degraded",
			Stage: observeMaintenanceStage(MaintenanceStageState{}, Job{ID: "job", Type: RepositoryDocsIndexJobType, Status: JobStatusCancelled, UpdatedAt: at}, at)}
		setState := func() {
			if canonical {
				m.sources["reg"] = map[string]*repositoryDocsRegisteredSource{"source": {State: state}}
			} else {
				copy := state
				entry.RepositoryDocs = &copy
			}
		}
		setState()
		requireRetryProbe(t, m, 0)
		state.Stage.Status = JobStatusSuperseded
		setState()
		requireRetryProbe(t, m, 0)
		state.Stage.Status = JobStatusFailed
		setState()
		requireRetryProbe(t, m, 1)
		intent := repositoryDocsAdmissionIntent{RegistrationID: "reg", SourceRegistrationID: "source", SourceRegistrationGeneration: 1, RepoID: entry.RepoID, Disposition: repositoryDocsAdmissionCancelled, CreatedAt: at}
		m.admissions[repositoryDocsAdmissionKey("reg", "source")] = intent
		requireRetryProbe(t, m, 0)
		if health := m.ExecutionHealth(at); health.State != "healthy" || health.Active != 0 {
			t.Fatal("cancelled source stage became a current execution failure")
		}
		// Current authority overrides a legacy cancellation, just as actual
		// reconciliation does; old physical-authority tombstones do not match.
		currentKey := repositoryDocsAdmissionKey("reg", "source", entry.PathFingerprint)
		intent.Disposition = repositoryDocsAdmissionPending
		m.admissions[currentKey] = intent
		requireRetryProbe(t, m, 1)
		intent.Disposition = repositoryDocsAdmissionCancelled
		m.admissions[currentKey] = intent
		requireRetryProbe(t, m, 0)
		delete(m.admissions, currentKey)
		delete(m.admissions, repositoryDocsAdmissionKey("reg", "source"))
		m.admissions[repositoryDocsAdmissionKey("reg", "source", "other-authority")] = intent
		requireRetryProbe(t, m, 1)
		if canonical {
			delete(m.admissions, repositoryDocsAdmissionKey("reg", "source", "other-authority"))
			intent.Disposition = repositoryDocsAdmissionCancelled
			m.admissions[currentKey] = intent
			m.mu.Lock()
			selected, _, _ := m.selectRepositoryDocsSourceForReconcileLocked("reg")
			m.mu.Unlock()
			if selected != nil {
				t.Fatal("test cancellation did not prevent actual reconciliation")
			}
		}
	}
}

func TestScheduledRetryProbeCancelledAndSupersededRegistryStages(t *testing.T) {
	jobs := NewJobManager("")
	m := NewMaintenanceManager(Manager{}, jobs, "")
	due := time.Now().UTC()
	m.entries["reg"] = &MaintenanceEntry{Enabled: true, Policy: MaintenancePolicy{SyncEnabled: true, RAGEnabled: true},
		SyncStage: MaintenanceStageState{Status: JobStatusCancelled, RetryAfter: due}, RAGStage: MaintenanceStageState{Status: JobStatusSuperseded, RetryAfter: due}}
	requireRetryProbe(t, m, 0)
}
