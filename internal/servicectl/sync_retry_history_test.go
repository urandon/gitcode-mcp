package servicectl

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSyncRetryHistorySurvivesSuccessAndDurableReload(t *testing.T) {
	// Ordinary retention remains enabled; only relative timestamps matter.
	at := time.Now().UTC().Add(-5 * time.Second)
	path := filepath.Join(t.TempDir(), "jobs.json")
	jobs := NewJobManager(path)
	job := &Job{ID: "job-000001", Type: SyncJobType, Status: JobStatusRunning, CreatedAt: at, CacheUUID: "cache", RepoID: "fixture/repository"}
	jobs.jobs[job.ID], jobs.nextID = job, 1
	first, last, success := at.Add(time.Second), at.Add(2*time.Second), at.Add(3*time.Second)
	for _, scheduled := range []time.Time{first, last} {
		due := scheduled.Add(time.Minute)
		applySyncCollectionObservation(job, SyncCollectionView{Collection: "issues", Outcome: SyncCollectionRetryScheduled, RetryAfter: &due, UpdatedAt: scheduled}, scheduled)
	}
	applySyncCollectionObservation(job, SyncCollectionView{Collection: "issues", Outcome: SyncCollectionSuccess, Attempt: 3, LastSuccessAt: &success, UpdatedAt: success}, success)
	job.Status, job.FinishedAt = JobStatusSucceeded, &success
	if err := jobs.saveLocked(); err != nil {
		t.Fatal("retry history persistence failed")
	}
	view := job.SyncCollections[0]
	if view.FirstRetryAt == nil || view.LastRetryAt == nil || !view.FirstRetryAt.Equal(first) || !view.LastRetryAt.Equal(last) || view.RetryAfter != nil || view.ErrorClass != "" || view.Attempt != 3 {
		t.Fatal("success erased history or retained current failure")
	}
	before := NewMaintenanceManager(Manager{}, jobs, "").ExecutionHealth(success)
	if before.State != "healthy" || before.Recovered != 1 || before.Active != 0 {
		t.Fatalf("same-job success lost recovered retry: state=%s coverage=%s reason=%s active=%d recovered=%d", before.State, before.Coverage, before.Reason, before.Active, before.Recovered)
	}
	incident := before.Incidents[0]
	if incident.Reason != "retry_wait" || incident.State != "recovered" || incident.RetryAfter != nil || !incident.FirstObservedAt.Equal(first) || !incident.LastObservedAt.Equal(success) || incident.LastSuccessAt == nil || !incident.LastSuccessAt.Equal(success) {
		t.Fatal("recovered retry lost finite causal timestamps")
	}
	reloaded := NewJobManager(path)
	if reloaded.LoadAndMarkInterrupted() != nil {
		t.Fatal("retry history reload failed")
	}
	after := NewMaintenanceManager(Manager{}, reloaded, "").ExecutionHealth(success)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("restart changed retained retry history")
	}
	copy := cloneJob(job)
	*copy.SyncCollections[0].FirstRetryAt = at
	*copy.SyncCollections[0].LastRetryAt = at
	if !job.SyncCollections[0].FirstRetryAt.Equal(first) || !job.SyncCollections[0].LastRetryAt.Equal(last) {
		t.Fatal("public clone mutated retry authority")
	}
}

func TestSyncRetryHistoryLegacyAndMalformedAuthority(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	stamp := func(delta time.Duration) *time.Time { value := at.Add(delta); return &value }
	for _, tc := range []struct {
		name        string
		first, last *time.Time
		legacy      bool
	}{
		{name: "legacy", legacy: true},
		{name: "first_only", first: stamp(time.Second)},
		{name: "last_only", last: stamp(time.Second)},
		{name: "zero", first: &time.Time{}, last: stamp(time.Second)},
		{name: "inverted", first: stamp(2 * time.Second), last: stamp(time.Second)},
		{name: "before_job", first: stamp(-time.Second), last: stamp(time.Second)},
		{name: "after_view", first: stamp(time.Second), last: stamp(4 * time.Second)},
		{name: "future", first: stamp(time.Second), last: stamp(2 * time.Second)},
		{name: "after_finished", first: stamp(time.Second), last: stamp(2 * time.Second)},
		{name: "zero_finished", first: stamp(time.Second), last: stamp(2 * time.Second)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			jobs := NewJobManager("")
			success := at.Add(3 * time.Second)
			jobs.jobs["job"] = &Job{ID: "job", Type: SyncJobType, Status: JobStatusSucceeded, CreatedAt: at, UpdatedAt: success, CacheUUID: "cache", RepoID: "fixture/repository", SyncCollections: []SyncCollectionView{{Collection: "issues", Outcome: SyncCollectionSuccess, UpdatedAt: success, LastSuccessAt: &success, FirstRetryAt: tc.first, LastRetryAt: tc.last}}}
			if tc.name == "after_finished" {
				jobs.jobs["job"].FinishedAt = stamp(time.Second)
			}
			if tc.name == "zero_finished" {
				jobs.jobs["job"].FinishedAt = &time.Time{}
			}
			observed := success
			if tc.name == "future" {
				observed = at.Add(time.Second)
			}
			got := NewMaintenanceManager(Manager{}, jobs, "").ExecutionHealth(observed)
			if tc.legacy {
				if got.State != "healthy" || len(got.Incidents) != 0 {
					t.Fatal("legacy success acquired invented retry history")
				}
			} else if got.State != "unknown" || got.Coverage != "unavailable" || len(got.Incidents) != 0 {
				t.Fatal("malformed retry history manufactured incidents or healthy authority")
			}
		})
	}
}

func TestSyncRetryHistoryRetainsInterruptedCollectionAfterRealReload(t *testing.T) {
	at := time.Now().UTC().Add(-5 * time.Second)
	scheduled := at.Add(time.Second)
	due := scheduled.Add(time.Minute)
	path := filepath.Join(t.TempDir(), "jobs.json")
	jobs := NewJobManager(path)
	job := &Job{ID: "job-000001", Type: SyncJobType, Status: JobStatusRunning, CreatedAt: at, CacheUUID: "cache", RepoID: "fixture/repository"}
	jobs.jobs[job.ID], jobs.nextID = job, 1
	applySyncCollectionObservation(job, SyncCollectionView{Collection: "issues", Outcome: SyncCollectionRetryScheduled, RetryAfter: &due, UpdatedAt: scheduled}, scheduled)
	if jobs.saveLocked() != nil {
		t.Fatal("scheduled authority persistence failed")
	}
	reloaded := NewJobManager(path)
	if reloaded.LoadAndMarkInterrupted() != nil {
		t.Fatal("actual interrupted owner reload failed")
	}
	got := NewMaintenanceManager(Manager{}, reloaded, "").ExecutionHealth(time.Now().UTC())
	if got.State != "degraded" || got.Coverage != "retained" {
		t.Fatal("interrupted owner lost retained authority")
	}
	for _, incident := range got.Incidents {
		if incident.Collection == "issues" {
			if incident.State != "interrupted" || incident.Reason != "interrupted" || incident.RetryAfter != nil || !incident.FirstObservedAt.Equal(scheduled) {
				t.Fatal("retry history overrode actual interrupted state in same-time tie")
			}
			return
		}
	}
	t.Fatal("interrupted collection incident missing")
}

func TestSyncRetryHistoryCannotBeInjectedByAnAttempt(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	job := &Job{}
	applySyncCollectionObservation(job, SyncCollectionView{Collection: "issues", Outcome: SyncCollectionSuccess, UpdatedAt: at, FirstRetryAt: &at, LastRetryAt: &at}, at)
	if job.SyncCollections[0].FirstRetryAt != nil || job.SyncCollections[0].LastRetryAt != nil {
		t.Fatal("attempt payload injected historical authority")
	}
}

func TestSyncRetryHistoryPreservesLaterFailureAndCancellation(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	first, last := at.Add(time.Second), at.Add(2*time.Second)
	for _, status := range []string{JobStatusFailed, JobStatusCancelled, JobStatusSuperseded} {
		t.Run(status, func(t *testing.T) {
			jobs := NewJobManager("")
			jobs.jobs["job"] = &Job{ID: "job", Type: SyncJobType, Status: status, CreatedAt: at, UpdatedAt: last, CacheUUID: "cache", RepoID: "fixture/repository", ErrorClass: "network_timeout", SyncCollections: []SyncCollectionView{{Collection: "issues", Outcome: SyncCollectionPermanentFailure, ErrorClass: "network_timeout", UpdatedAt: last, FirstRetryAt: &first, LastRetryAt: &first}}}
			got := NewMaintenanceManager(Manager{}, jobs, "").ExecutionHealth(last)
			if status == JobStatusFailed {
				if got.State != "degraded" || len(got.Incidents) != 1 || got.Incidents[0].Reason != "provider_timeout" || !got.Incidents[0].FirstObservedAt.Equal(first) {
					t.Fatal("retained retry overrode later confirmed failure")
				}
			} else if got.State != "healthy" || len(got.Incidents) != 0 {
				t.Fatal("retained retry revived cancelled or superseded work")
			}
		})
	}
}
