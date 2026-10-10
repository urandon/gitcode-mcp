package servicectl

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/observability"
)

func TestExecutionHealthProjectsDurableFailuresRecoveryAndWriterWait(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	jobs := NewJobManager("")
	m := NewMaintenanceManager(Manager{}, jobs, "")
	jobs.jobs["failed"] = &Job{ID: "failed", Type: SyncJobType, Status: JobStatusFailed, CacheUUID: "cache", RepoID: "fixture/repository", RegistrationID: "reg", UpdatedAt: at,
		Error: "synthetic-private-error", ErrorClass: "network_timeout", SyncCollections: []SyncCollectionView{{Collection: "wiki", Outcome: SyncCollectionPermanentFailure, ErrorClass: "network_timeout", UpdatedAt: at}}}
	before := m.ExecutionHealth(at)
	if before.State != "degraded" || before.Active != 1 || before.Incidents[0].Reason != "provider_timeout" {
		t.Fatal("durable failure lost")
	}
	success := at.Add(time.Second)
	m.entries["reg"] = &MaintenanceEntry{CacheUUID: "cache", RepoID: "fixture/repository", Enabled: true, Frontiers: []cache.MaintenanceFrontier{{RemoteType: "wiki", LastSuccessAt: success, UpdatedAt: success, Status: "complete"}}}
	recovered := m.ExecutionHealth(success)
	if recovered.State != "healthy" || recovered.Recovered != 1 || recovered.Incidents[0].LastSuccessAt == nil || !recovered.Incidents[0].LastSuccessAt.Equal(success) {
		t.Fatal("frontier success did not recover old incident")
	}
	waitAt := success.Add(time.Second)
	retryAt := waitAt.Add(time.Minute)
	jobs.jobs["waiting"] = &Job{ID: "waiting", Type: SyncJobType, Status: JobStatusRunning, CacheUUID: "cache", RepoID: "fixture/repository", RegistrationID: "reg", UpdatedAt: waitAt,
		SyncStage: &SyncStageView{Collection: "wiki", Phase: SyncStageWaitingCommit, UpdatedAt: waitAt, RetryAfter: retryAt, BlockingOp: "synthetic-private-operation"}}
	waiting := m.ExecutionHealth(waitAt)
	if waiting.State != "degraded" || waiting.Active != 1 || waiting.Incidents[0].State != "waiting_writer" || waiting.Incidents[0].RetryAfter == nil || !waiting.Incidents[0].FirstObservedAt.Equal(at) {
		t.Fatal("writer wait lost retained history")
	}
	jobs.jobs["waiting"].Status = JobStatusSucceeded
	jobs.jobs["waiting"].SyncStage.Phase = SyncStageCommitted
	jobs.jobs["waiting"].UpdatedAt = waitAt.Add(time.Second)
	jobs.jobs["waiting"].SyncCollections = []SyncCollectionView{{Collection: "wiki", Outcome: SyncCollectionSuccess, UpdatedAt: waitAt.Add(time.Second)}}
	if got := m.ExecutionHealth(waitAt.Add(time.Second)); got.State != "healthy" || got.Recovered != 1 {
		t.Fatal("successful commit did not recover writer wait")
	}
	data, _ := json.Marshal(waiting)
	for _, private := range []string{"fixture/repository", "synthetic-private-error", "synthetic-private-operation", "\"cache\"", "\"reg\"", "\"waiting\""} {
		if strings.Contains(string(data), private) {
			t.Fatal("public health contains private authority data")
		}
	}
}

func TestExecutionHealthConcurrentAuthorityMutation(t *testing.T) {
	at := time.Now().UTC()
	jobs := NewJobManager("")
	m := NewMaintenanceManager(Manager{}, jobs, "")
	m.entries["reg"] = &MaintenanceEntry{Enabled: true, CacheUUID: "cache", RepoID: "fixture/repository", Policy: MaintenancePolicy{RAGEnabled: true}}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			jobs.mu.Lock()
			jobs.jobs["job"] = &Job{ID: "job", Type: RAGIndexJobType, Status: JobStatusFailed, UpdatedAt: at.Add(time.Duration(i) * time.Second), CacheUUID: "cache", RepoID: "fixture/repository", RegistrationID: "reg"}
			jobs.mu.Unlock()
			m.mu.Lock()
			m.entries["reg"].RAGStage = MaintenanceStageState{Status: JobStatusFailed, UpdatedAt: at, RetryAfter: at.Add(time.Minute), ConsecutiveFailures: 1}
			m.mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			m.ExecutionHealth(at)
			m.ScheduledRetryProbe()
		}
	}()
	wg.Wait()
	if got := m.ExecutionHealth(at); got.State != "degraded" || got.Active != 1 {
		t.Fatal("concurrent projection lost durable failure")
	}
}

func TestExecutionHealthAndRetryProbeBoundAuthorityScan(t *testing.T) {
	at := time.Now().UTC()
	jobs := NewJobManager("")
	m := NewMaintenanceManager(Manager{}, jobs, "")
	for i := 0; i <= observability.MaxHealthEvidence; i++ {
		jobs.jobs[fmt.Sprint(i)] = &Job{Type: RAGIndexJobType, Status: JobStatusQueued}
	}
	if got := m.ExecutionHealth(at); got.State != "unknown" || got.Reason != "evidence_limit" {
		t.Fatal("unbounded authority inspection accepted")
	}
	if got := m.ScheduledRetryProbe(); got.Value != nil {
		t.Fatal("over-budget retry probe fabricated zero")
	}
	if !jobs.mu.TryLock() {
		t.Fatal("over-budget read retained job authority lock")
	}
	jobs.mu.Unlock()
	if !m.mu.TryLock() {
		t.Fatal("over-budget read retained registry authority lock")
	}
	m.mu.Unlock()
}

func TestExecutionHealthRegistryRetryAndCancellationStayDistinct(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	due := at.Add(time.Minute)
	jobs := NewJobManager("")
	m := NewMaintenanceManager(Manager{}, jobs, "")
	m.entries["reg"] = &MaintenanceEntry{CacheUUID: "cache", RepoID: "fixture/repository", Enabled: true, Policy: MaintenancePolicy{RAGEnabled: true}, RAGStage: MaintenanceStageState{Status: JobStatusFailed, ConsecutiveFailures: 1, UpdatedAt: at, RetryAfter: due, LastErrorClass: "synthetic-private-class"}}
	got := m.ExecutionHealth(at)
	if got.Active != 1 || got.Incidents[0].Origin != "rag" || got.Incidents[0].State != "retrying" || got.Incidents[0].Reason != "operation_failed" {
		t.Fatal("registry retry evidence lost")
	}
	// A resumed job can reuse and overwrite its terminal public identity. Its
	// registry retains the prior failure until the new result is confirmed.
	jobs.jobs["resumed"] = &Job{ID: "resumed", CacheUUID: "cache", RepoID: "fixture/repository", RegistrationID: "reg", Type: RAGIndexJobType, Status: JobStatusRunning, UpdatedAt: at.Add(time.Second)}
	if running := m.ExecutionHealth(at.Add(time.Second)); running.Active != 1 || running.Incidents[0].State != "active" || running.Incidents[0].RetryAfter != nil {
		t.Fatal("resumed execution erased prior failure or kept old timer")
	}
	m.entries["reg"].Enabled = false
	jobs.jobs["cancelled"] = &Job{ID: "cancelled", Type: RAGIndexJobType, Status: JobStatusCancelled, UpdatedAt: at, ErrorClass: "cancelled"}
	jobs.jobs["superseded"] = &Job{ID: "superseded", Type: RAGIndexJobType, Status: JobStatusSuperseded, UpdatedAt: at}
	if got := m.ExecutionHealth(at); got.State != "healthy" || len(got.Incidents) != 0 {
		t.Fatal("explicit cancellation or disabled backoff became a failure")
	}
}

func TestExecutionHealthRestartReconstructsSameRetainedAuthority(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	path := filepath.Join(t.TempDir(), "jobs.json")
	jobs := NewJobManager(path)
	m := NewMaintenanceManager(Manager{}, jobs, "")
	jobs.jobs["job-000001"] = &Job{ID: "job-000001", Type: RAGIndexJobType, Status: JobStatusFailed, UpdatedAt: at, CreatedAt: at, CacheUUID: "cache", RepoID: "fixture/repository", RegistrationID: "reg", ErrorClass: "rag_failed"}
	jobs.nextID = 1
	if err := jobs.saveLocked(); err != nil {
		t.Fatal("fixture snapshot persistence failed")
	}
	before := m.ExecutionHealth(at)
	restarted := NewJobManager(path)
	if err := restarted.LoadAndMarkInterrupted(); err != nil {
		t.Fatal("fixture reload failed")
	}
	after := NewMaintenanceManager(Manager{}, restarted, "").ExecutionHealth(at)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("boot changed retained durable failure history")
	}
}

func TestExecutionHealthBusyMissingAndInvalidCannotBecomeHealthy(t *testing.T) {
	at := time.Now().UTC()
	jobs := NewJobManager("")
	m := NewMaintenanceManager(Manager{}, jobs, "")
	for _, authority := range []string{"registry", "jobs"} {
		if authority == "registry" {
			m.mu.Lock()
		} else {
			jobs.mu.Lock()
		}
		done := make(chan observability.ExecutionHealth, 1)
		go func() { done <- m.ExecutionHealth(at) }()
		var got observability.ExecutionHealth
		select {
		case got = <-done:
		case <-time.After(time.Second):
			if authority == "registry" {
				m.mu.Unlock()
			} else {
				jobs.mu.Unlock()
			}
			t.Fatal("health read waited on execution authority")
		}
		if authority == "registry" {
			m.mu.Unlock()
		} else {
			jobs.mu.Unlock()
		}
		if got.State != "unknown" || got.Coverage != "unavailable" || got.Reason != "authority_busy" {
			t.Fatal("busy authority fabricated health")
		}
	}
	if got := (*MaintenanceManager)(nil).ExecutionHealth(at); got.State != "unknown" || got.Reason != "authority_missing" {
		t.Fatal("missing authority fabricated health")
	}
	jobs.jobs["broken"] = &Job{ID: "broken", Type: RAGIndexJobType, Status: JobStatusFailed}
	if got := m.ExecutionHealth(at); got.State != "unknown" || got.Coverage != "partial" {
		t.Fatal("missing authority timestamps fabricated health")
	}
}

func TestExecutionHealthStageRejectionSurvivesEarlierCollectionSuccess(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	jobs := NewJobManager("")
	jobs.now = func() time.Time { return at.Add(time.Second) }
	m := NewMaintenanceManager(Manager{}, jobs, "")
	jobs.jobs["job"] = &Job{ID: "job", Type: SyncJobType, Status: JobStatusInterrupted, UpdatedAt: at, CacheUUID: "cache", RepoID: "fixture/repository", RegistrationID: "reg",
		SyncCollections: []SyncCollectionView{{Collection: "issues", Outcome: SyncCollectionSuccess, UpdatedAt: at}},
		SyncStage:       &SyncStageView{StageRef: "stage-ref", Collection: "wiki", Phase: SyncStageWaitingCommit, UpdatedAt: at}}
	jobs.rejectInterruptedSyncStageByRef("stage-ref", "stage_identity_mismatch")
	got := m.ExecutionHealth(at.Add(time.Second))
	if jobs.jobs["job"].Status != JobStatusFailed || got.State != "degraded" || got.Active != 1 || got.Incidents[0].Collection != "wiki" || !got.Incidents[0].LastObservedAt.Equal(at.Add(time.Second)) {
		t.Fatal("terminal stage rejection disappeared behind successful collection")
	}
	jobs.jobs["success"] = &Job{ID: "success", Type: SyncJobType, Status: JobStatusSucceeded, UpdatedAt: at.Add(2 * time.Second), CacheUUID: "cache", RepoID: "fixture/repository", RegistrationID: "reg",
		SyncCollections: []SyncCollectionView{{Collection: "wiki", Outcome: SyncCollectionSuccess, UpdatedAt: at.Add(2 * time.Second)}}}
	if recovered := m.ExecutionHealth(at.Add(2 * time.Second)); recovered.State != "healthy" || recovered.Recovered != 1 {
		t.Fatal("later confirmed collection success did not recover stage rejection")
	}
}

func TestExecutionHealthWholeJobFailureAndRecoveryAlongsideCollections(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	jobs := NewJobManager("")
	m := NewMaintenanceManager(Manager{}, jobs, "")
	jobs.jobs["failed"] = &Job{ID: "failed", Type: SyncJobType, Status: JobStatusFailed, UpdatedAt: at, CacheUUID: "cache", RepoID: "fixture/repository", RegistrationID: "reg",
		SyncCollections: []SyncCollectionView{{Collection: "issues", Outcome: SyncCollectionSuccess, UpdatedAt: at.Add(-time.Second)}}}
	if got := m.ExecutionHealth(at); got.State != "degraded" || got.Active != 1 || got.Incidents[0].Collection != "other" {
		t.Fatal("unattributed whole-job failure disappeared behind collection snapshots")
	}
	jobs.jobs["succeeded"] = &Job{ID: "succeeded", Type: SyncJobType, Status: JobStatusSucceeded, UpdatedAt: at.Add(time.Second), CacheUUID: "cache", RepoID: "fixture/repository", RegistrationID: "reg",
		SyncCollections: []SyncCollectionView{{Collection: "issues", Outcome: SyncCollectionSuccess, UpdatedAt: at.Add(time.Second)}}}
	if got := m.ExecutionHealth(at.Add(time.Second)); got.State != "healthy" || got.Recovered != 1 {
		t.Fatal("whole-job success was not retained alongside collection success")
	}
}

func TestExecutionHealthRetainedFrontierFailureSurvivesJobPruning(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	for _, priorSuccess := range []time.Time{{}, at.Add(-time.Hour)} {
		jobs := NewJobManager("")
		m := NewMaintenanceManager(Manager{}, jobs, "")
		entry := &MaintenanceEntry{RegistrationID: "reg", CacheUUID: "cache", RepoID: "fixture/repository", Enabled: true, State: "ready", Policy: MaintenancePolicy{SyncEnabled: true, Wiki: true},
			Frontiers: []cache.MaintenanceFrontier{{RemoteType: "wiki", Lane: "head", Status: "degraded", LastErrorClass: "network_timeout", LastSuccessAt: priorSuccess, UpdatedAt: at}}}
		m.entries["reg"] = entry
		got := m.ExecutionHealth(at)
		if got.State != "degraded" || got.Active != 1 || got.Incidents[0].Reason != "provider_timeout" {
			t.Fatal("current retained frontier failure disappeared after job pruning")
		}
		if !priorSuccess.IsZero() && (got.Incidents[0].LastSuccessAt == nil || !got.Incidents[0].LastSuccessAt.Equal(priorSuccess)) {
			t.Fatal("preceding frontier success lost")
		}
	}
}

func TestExecutionHealthAutoDisabledFaultDiffersFromExplicitDisable(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	for _, state := range []string{"config_snapshot_invalid", "identity_conflict", "identity_unresolved", "cache_schema_blocked"} {
		jobs := NewJobManager("")
		m := NewMaintenanceManager(Manager{}, jobs, "")
		entry := &MaintenanceEntry{RegistrationID: "reg", CacheUUID: "cache", RepoID: "fixture/repository", Enabled: true, LastReconciledAt: at}
		m.entries["reg"] = entry
		blockMaintenanceIdentity(entry, state)
		if got := m.ExecutionHealth(at); entry.Enabled || got.State != "degraded" || got.Active != 1 || got.Incidents[0].Origin != "maintenance" {
			t.Fatal("automatic authority block became healthy because admission was disabled")
		}
		entry.LastReconciledAt = time.Time{}
		if got := m.ExecutionHealth(at); got.State != "unknown" || got.Coverage != "partial" {
			t.Fatal("missing blocked-authority time fabricated healthy state")
		}
		entry.State = "disabled"
		if got := m.ExecutionHealth(at); got.State != "healthy" || got.Active != 0 {
			t.Fatal("explicit disable became an authority failure")
		}
	}
}
