package servicectl

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/observability"
)

func TestRecoveryObservationActualConfirmedResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	before := NewJobManager(path)
	job, created, err := before.createCoalescedJob(SyncJobType, "fixture/repository", "", 0, "recovery-fixture-work", "fixture-cache", "fixture-registration", "", func() {})
	if err != nil || !created {
		t.Fatal("recovery admission fixture failed")
	}
	restarted := NewJobManager(path)
	if restarted.LoadAndMarkInterrupted() != nil {
		t.Fatal("recovery snapshot fixture failed")
	}
	retained, ok := restarted.Get(job.ID)
	if !ok || retained.Status != JobStatusInterrupted {
		t.Fatal("actual restarted job was not interrupted")
	}
	c := jobObservationFixture(t)
	restarted.SetObservation(c)
	if restarted.resumeInterruptedSyncRetry(job.ID, func() {}) != nil {
		t.Fatal("actual recovery resume failed")
	}
	if restarted.resumeInterruptedSyncRetry(job.ID, func() {}) != nil {
		t.Fatal("repeated recovery changed execution result")
	}
	retained, ok = restarted.Get(job.ID)
	if !ok || retained.Status != JobStatusRunning {
		t.Fatal("actual recovery did not retain running state")
	}
	sealJobObservation(t, c)
	page, err := c.Query(observability.Filter{}, "", 100)
	if err != nil || page.HasMore {
		t.Fatal("recovery observation query failed")
	}
	count := 0
	for _, e := range page.Events {
		if e.Code == observability.JobRecovered {
			count++
			if e.JobRef != expectedObservationRef("job", job.ID) || e.CorrelationRef != expectedObservationRef("correlation", c.BootID()+"\x00"+job.ID) || e.Phase != "recovery" || e.Outcome != "resumed" || e.Attempt != 0 || e.StageRef != "" {
				t.Fatal("actual recovery lost its finite job boundary")
			}
		} else if e.Code == observability.JobAdmitted || e.Code == observability.JobTerminal || e.Code == observability.SyncCommitted || e.Code == observability.ProviderAttempt {
			t.Fatal("recovery manufactured a fresh execution boundary")
		}
	}
	if uint64(count)+page.Coverage.Dropped != 1 {
		t.Fatal("confirmed recovery resume has no retained or explicitly lost evidence")
	}
}

func recoveryObservationFixture(t *testing.T) (*JobManager, Job, *observability.Collector) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jobs.json")
	before := NewJobManager(path)
	job, created, err := before.createCoalescedJob(SyncJobType, "fixture/repository", "", 0, "synthetic-private-recovery-work", "synthetic-private-recovery-cache", "synthetic-private-recovery-registration", "", func() {})
	if err != nil || !created {
		t.Fatal("recovery fixture admission failed")
	}
	m := NewJobManager(path)
	if m.LoadAndMarkInterrupted() != nil {
		t.Fatal("actual recovery fixture reload failed")
	}
	c := jobObservationFixture(t)
	m.SetObservation(c)
	return m, job, c
}

func TestRecoveryObservationRetainedFailedResumeDoesNotInventConfirmation(t *testing.T) {
	for _, stageOwner := range []bool{false, true} {
		t.Run(fmt.Sprint(stageOwner), func(t *testing.T) {
			m, job, c := recoveryObservationFixture(t)
			stage := testSyncStageEnvelope()
			stage.JobID, stage.CacheUUID, stage.RegistrationID, stage.RepoID = job.ID, job.CacheUUID, job.RegistrationID, job.RepoID
			journal := NewSyncStageJournal(t.TempDir(), SyncStageLimits{})
			stage, err := journal.Create(stage)
			if err != nil {
				t.Fatal("recovery stage fixture failed")
			}
			resume := func() error {
				if stageOwner {
					return m.resumeInterruptedSyncStage(stage, func() {})
				}
				return m.resumeInterruptedSyncRetry(job.ID, func() {})
			}
			m.writeFile = func(string, []byte, os.FileMode) error { return errors.New("synthetic-private-recovery-save-error") }
			if resume() == nil {
				t.Fatal("actual failed resume save was not reached")
			}
			if retained, ok := m.Get(job.ID); !ok || retained.Status != JobStatusRunning {
				t.Fatal("instrumentation changed retained failed-save semantics")
			}
			m.writeFile = durableAtomicWriteFile
			if resume() != nil {
				t.Fatal("repeated retained resume changed its result")
			}
			if m.updateJobPersisted(job.ID, func(j *Job, now time.Time) { j.UpdatedAt = now }) != nil {
				t.Fatal("unrelated snapshot save failed")
			}
			sealJobObservation(t, c)
			page, err := c.Query(observability.Filter{Code: observability.JobRecovered}, "", 20)
			if err != nil || len(page.Events) != 0 || page.Coverage.Dropped != 1 || page.Coverage.State != "partial" {
				t.Fatal("retained failed save manufactured confirmed or repeated recovery")
			}
		})
	}
}

func TestRecoveryObservationActualOwnersVersusGenericProjection(t *testing.T) {
	for _, owner := range []string{"reconciled", "rejected", "stage_resumed", "projection"} {
		t.Run(owner, func(t *testing.T) {
			m, job, c := recoveryObservationFixture(t)
			var err error
			switch owner {
			case "reconciled":
				err = m.completeInterruptedSyncRetry(job.ID)
				if err == nil {
					err = m.completeInterruptedSyncRetry(job.ID)
				}
			case "rejected":
				m.writeFile = func(string, []byte, os.FileMode) error { return errors.New("synthetic-private-save-error") }
				if m.failInterruptedSyncRetry(job.ID, "synthetic-private-rejection") == nil {
					t.Fatal("rolled-back recovery save fault not reached")
				}
				if retained, ok := m.Get(job.ID); !ok || retained.Status != JobStatusInterrupted {
					t.Fatal("recovery rejection did not preserve rollback")
				}
				m.writeFile = durableAtomicWriteFile
				err = m.failInterruptedSyncRetry(job.ID, "synthetic-private-rejection")
				if err == nil {
					err = m.failInterruptedSyncRetry(job.ID, "synthetic-private-rejection")
				}
			case "stage_resumed":
				stage := testSyncStageEnvelope()
				stage.JobID, stage.CacheUUID, stage.RegistrationID, stage.RepoID = job.ID, job.CacheUUID, job.RegistrationID, job.RepoID
				stage, err = NewSyncStageJournal(t.TempDir(), SyncStageLimits{}).Create(stage)
				if err == nil {
					err = m.resumeInterruptedSyncStage(stage, func() {})
				}
				if err == nil {
					err = m.resumeInterruptedSyncStage(stage, func() {})
				}
			case "projection":
				err = m.updateJobPersisted(job.ID, func(j *Job, now time.Time) { j.Status, j.FinishedAt = JobStatusSucceeded, &now })
			}
			if err != nil {
				t.Fatal("actual recovery owner fixture failed")
			}
			want := map[observability.Code]int{observability.JobRecovered: 1}
			if owner == "projection" {
				want = map[observability.Code]int{}
			}
			events := assertJobObservations(t, c, job, want)
			data, _ := json.Marshal(events)
			if strings.Contains(string(data), "synthetic-private") || strings.Contains(string(data), job.RepoID) {
				t.Fatal("private recovery authority or rejection bytes escaped")
			}
			for _, event := range events {
				if event.Code == observability.JobRecovered && (event.Phase != "recovery" || event.Attempt != 0 || event.StageRef != "" || (owner == "rejected" && event.FailureClass != "operation_failed")) {
					t.Fatal("recovery event lost its closed contract")
				}
			}
		})
	}
}

func TestRecoveryObservationActualBoundedGroupRejection(t *testing.T) {
	for _, fault := range []bool{false, true} {
		t.Run(fmt.Sprint(fault), func(t *testing.T) {
			const total = maxRecoveryObservationIdentities + 15
			policy := NewJobManager("").retention
			policy.MaxTerminalJobs, policy.MaxDiagnosticJobs = total+16, total+16
			m := NewJobManagerWithRetention(filepath.Join(t.TempDir(), "jobs.json"), policy)
			now := time.Now().UTC()
			for i := 1; i <= total; i++ {
				id := fmt.Sprintf("job-%06d", i)
				m.jobs[id] = &Job{ID: id, Type: SyncJobType, RepoID: "fixture/repository", CacheUUID: "synthetic-private-cache", RegistrationID: "synthetic-private-registration", Status: JobStatusInterrupted, CreatedAt: now, UpdatedAt: now, SyncStage: &SyncStageView{StageRef: "synthetic-private-stage-ref", Collection: "issues"}}
			}
			m.nextID = total
			if m.saveLocked() != nil {
				t.Fatal("group recovery fixture save failed")
			}
			c := jobObservationFixture(t)
			m.SetObservation(c)
			metrics := observability.NewMetrics(c.BootID(), time.Now().UTC())
			m.SetMetrics(metrics)
			saves := 0
			m.writeFile = func(path string, data []byte, mode os.FileMode) error {
				saves++
				if fault {
					return errors.New("synthetic-private-group-save-error")
				}
				return durableAtomicWriteFile(path, data, mode)
			}
			m.rejectInterruptedSyncStageByRef("synthetic-private-stage-ref", "synthetic-private-group-rejection")
			if saves != 1 {
				t.Fatal("diagnostic group rejection changed snapshot write count")
			}
			m.rejectInterruptedSyncStageByRef("synthetic-private-stage-ref", "synthetic-private-group-rejection")
			wantMetricLoss := uint64(total - maxRecoveryObservationIdentities)
			if fault {
				wantMetricLoss = total
			}
			if metrics.Snapshot().Dropped != wantMetricLoss {
				t.Fatal("group recovery metric loss did not match uncaptured actual boundaries")
			}
			requireJobCounter(t, metrics, "jobs_admitted", map[string]string{"kind": "sync"}, 0)
			requireJobCounter(t, metrics, "jobs_terminal", map[string]string{"kind": "sync", "outcome": "failed"}, 0)
			sealJobObservation(t, c)
			var events []observability.Event
			var loss uint64
			var cursor string
			finished := false
			for pageNumber := 0; pageNumber < 4; pageNumber++ {
				page, err := c.Query(observability.Filter{Code: observability.JobRecovered}, cursor, observability.MaxPageEvents)
				if err != nil || (pageNumber > 0 && page.Coverage.Dropped != loss) || page.Coverage.State != "partial" {
					t.Fatal("group recovery query lost its stable partial boundary")
				}
				loss = page.Coverage.Dropped
				events = append(events, page.Events...)
				if len(events) > maxRecoveryObservationIdentities {
					t.Fatal("group diagnostic capture exceeded its bound")
				}
				if !page.HasMore {
					finished = true
					break
				}
				cursor = page.Cursor
			}
			if !finished || uint64(len(events))+loss != total {
				t.Fatal("bounded group recovery lost or duplicated its actual boundaries")
			}
			if fault && len(events) != 0 {
				t.Fatal("failed group save invented confirmed rejection")
			}
			for _, e := range events {
				if e.Outcome != "rejected" || e.FailureClass != "operation_failed" {
					t.Fatal("group rejection escaped its finite catalog")
				}
			}
			data, _ := json.Marshal(events)
			if strings.Contains(string(data), "synthetic-private") {
				t.Fatal("group evidence captured private fields")
			}
		})
	}
}

func TestRecoveryObservationMissingResumeIsNotAnEvent(t *testing.T) {
	m, _, c := recoveryObservationFixture(t)
	if m.resumeInterruptedSyncRetry("synthetic-private-missing", func() {}) != nil {
		t.Fatal("missing resume changed result")
	}
	assertJobObservations(t, c, Job{}, map[observability.Code]int{})
}

func TestRecoveryObservationRepositoryDocsConfirmedResumeAndRollback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	before := NewJobManager(path)
	job, created, err := before.createCoalescedJobWithIntent(RepositoryDocsIndexJobType, "fixture/repository", "", 0, "synthetic-private-docs-work", "synthetic-private-docs-cache", "synthetic-private-docs-registration", "", JobRecoveryIntent{SourceRegistrationID: "synthetic-private-source", SourceRegistrationGeneration: 3, ExpectedRevisionSetID: "synthetic-private-set"}, func() {})
	if err != nil || !created {
		t.Fatal("documentation recovery fixture admission failed")
	}
	m := NewJobManager(path)
	if m.LoadAndMarkInterrupted() != nil {
		t.Fatal("documentation recovery reload failed")
	}
	c := jobObservationFixture(t)
	m.SetObservation(c)
	metrics := observability.NewMetrics(c.BootID(), time.Now().UTC())
	m.SetMetrics(metrics)
	resume := func(id string, generation int64) (bool, error) {
		_, resumed, err := m.ResumeRepositoryDocsAdmission(id, job.RegistrationID, "synthetic-private-source", generation, "synthetic-private-set", "synthetic-private-docs-work", "", func() {})
		return resumed, err
	}
	for _, id := range []string{job.ID, "synthetic-private-missing"} {
		if resumed, err := resume(id, 4); err != nil || resumed {
			t.Fatal("stale identity was revived")
		}
	}
	// Fenced work is not a new recovery boundary or writer denial.
	m.mu.Lock()
	m.syncRecoveryFences[job.CacheUUID] = true
	m.mu.Unlock()
	if resumed, err := resume(job.ID, 3); err == nil || resumed {
		t.Fatal("recovery fence failed")
	}
	m.mu.Lock()
	delete(m.syncRecoveryFences, job.CacheUUID)
	m.cacheMutationFences[job.CacheUUID] = true
	m.mu.Unlock()
	if resumed, err := resume(job.ID, 3); err == nil || resumed {
		t.Fatal("mutation fence failed")
	}
	m.mu.Lock()
	delete(m.cacheMutationFences, job.CacheUUID)
	m.mu.Unlock()
	m.writeFile = func(string, []byte, os.FileMode) error { return errors.New("synthetic-private-docs-save-error") }
	if resumed, err := resume(job.ID, 3); err == nil || resumed {
		t.Fatal("documentation resume save fault not reached")
	}
	if retained, ok := m.Get(job.ID); !ok || retained.Status != JobStatusInterrupted {
		t.Fatal("documentation failed resume did not roll back")
	}
	m.writeFile = durableAtomicWriteFile
	if resumed, err := resume(job.ID, 3); err != nil || !resumed {
		t.Fatal("actual documentation recovery did not resume")
	}
	if resumed, err := resume(job.ID, 3); err != nil || resumed {
		t.Fatal("repeated documentation recovery revived queued work")
	}
	requireJobCounter(t, metrics, "jobs_admitted", map[string]string{"kind": "repository_docs"}, 0)
	requireJobCounter(t, metrics, "jobs_terminal", map[string]string{"kind": "repository_docs", "outcome": "succeeded"}, 0)
	if metrics.Snapshot().Dropped != 0 {
		t.Fatal("rolled-back documentation save invented observation loss")
	}
	assertJobObservations(t, c, job, map[observability.Code]int{observability.JobRecovered: 1})
}
