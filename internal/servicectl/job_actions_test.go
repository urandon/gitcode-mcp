package servicectl

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/adminhttp"
)

func TestJobActionCancelPersistsAndReplaysReceipt(t *testing.T) {
	dir := t.TempDir()
	jobs := NewJobManager(filepath.Join(dir, "jobs.json"))
	jobContext, cancel := context.WithCancel(context.Background())
	job, created := jobs.createCoalescedJob(SyncJobType, "owner/repo", "", 1, "sync-work", "cache-1", "reg-1", "", cancel)
	if !created {
		t.Fatal("job not created")
	}
	jobs.updateJob(job.ID, func(stored *Job, now time.Time) {
		stored.Status = JobStatusRunning
		stored.StartedAt = &now
	})
	go func() {
		<-jobContext.Done()
		jobs.finishJob(job.ID, JobStatusCancelled, "cancelled")
	}()

	path := filepath.Join(dir, "actions.json")
	actions := NewJobActionManager(path, jobs, nil)
	first, err := actions.Cancel(context.Background(), adminhttp.JobActionRequest{JobID: job.ID, IdempotencyKey: "cancel-secret"})
	if err != nil || first.Outcome != "cancelled" || first.JobStatus != JobStatusCancelled || first.Replayed {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(disk), "cancel-secret") {
		t.Fatalf("receipt file contains raw idempotency key: %s", disk)
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("receipt mode=%v", info.Mode().Perm())
	}
	jobsDisk, err := os.ReadFile(filepath.Join(dir, "jobs.json"))
	if err != nil || !strings.Contains(string(jobsDisk), `"work_ref": "work-`) || strings.Contains(string(jobsDisk), "sync-work") {
		t.Fatalf("public work identity persistence=%s err=%v", jobsDisk, err)
	}

	restarted := NewJobActionManager(path, jobs, nil)
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	replay, err := restarted.Cancel(context.Background(), adminhttp.JobActionRequest{JobID: job.ID, IdempotencyKey: "cancel-secret"})
	if err != nil || !replay.Replayed || replay.ReceiptID != first.ReceiptID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	_, err = restarted.Retry(context.Background(), adminhttp.JobActionRequest{JobID: job.ID, IdempotencyKey: "cancel-secret"})
	var actionErr adminhttp.JobActionError
	if !errors.As(err, &actionErr) || actionErr.Status != http.StatusConflict || actionErr.Code != "idempotency_conflict" {
		t.Fatalf("conflict error=%T %v", err, err)
	}
}

func TestJobActionRetryCoalescesEquivalentWork(t *testing.T) {
	jobs := NewJobManager("")
	now := time.Now().UTC()
	finished := now.Add(-time.Minute)
	jobs.jobs["job-000001"] = &Job{ID: "job-000001", Type: RAGIndexJobType, RepoID: "owner/repo", CacheUUID: "cache-1", RegistrationID: "reg-1", Status: JobStatusFailed, CreatedAt: finished, UpdatedAt: finished, FinishedAt: &finished}
	jobs.nextID = 1
	active, _ := jobs.createCoalescedJob(RAGIndexJobType, "owner/repo", "profile", 10, "rag-work", "cache-1", "reg-1", "ns-1", func() {})
	actions := NewJobActionManager("", jobs, nil)
	receipt, err := actions.Retry(context.Background(), adminhttp.JobActionRequest{JobID: "job-000001", IdempotencyKey: "retry-1"})
	if err != nil || receipt.Outcome != "coalesced" || receipt.ResultJob != active.ID || receipt.JobStatus != JobStatusQueued {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
}

func TestJobActionRetryCreatesCurrentMaintenanceWork(t *testing.T) {
	jobs := NewJobManager("")
	now := time.Now().UTC()
	finished := now.Add(-time.Minute)
	jobs.jobs["job-000001"] = &Job{ID: "job-000001", Type: SyncJobType, RepoID: "owner/repo", CacheUUID: "cache-1", RegistrationID: "reg-1", Status: JobStatusFailed, CreatedAt: finished, UpdatedAt: finished, FinishedAt: &finished}
	jobs.nextID = 1
	actions := NewJobActionManager("", jobs, nil)
	actions.reconcile = func(_ context.Context, registrationID string) (MaintenanceReconcileResult, error) {
		if registrationID != "reg-1" {
			t.Fatalf("registration=%q", registrationID)
		}
		created, _ := jobs.createCoalescedJob(SyncJobType, "owner/repo", "", 10, "new-sync-work", "cache-1", "reg-1", "", func() {})
		return MaintenanceReconcileResult{JobsStarted: []string{created.ID}}, nil
	}
	receipt, err := actions.Retry(context.Background(), adminhttp.JobActionRequest{JobID: "job-000001", IdempotencyKey: "retry-new"})
	if err != nil || receipt.Outcome != "created" || receipt.ResultJob != "job-000002" || receipt.JobStatus != JobStatusQueued {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
}

func TestJobActionStateAndCapabilityBoundaries(t *testing.T) {
	jobs := NewJobManager("")
	now := time.Now().UTC()
	jobs.jobs["fake"] = &Job{ID: "fake", Type: "fake", Status: JobStatusRunning, CreatedAt: now, UpdatedAt: now}
	jobs.jobs["terminal"] = &Job{ID: "terminal", Type: SyncJobType, Status: JobStatusSucceeded, CreatedAt: now, UpdatedAt: now}
	jobs.jobs["active"] = &Job{ID: "active", Type: SyncJobType, RegistrationID: "reg", Status: JobStatusRunning, CreatedAt: now, UpdatedAt: now}
	actions := NewJobActionManager("", jobs, nil)

	assertCode := func(action string, req adminhttp.JobActionRequest, want string) {
		t.Helper()
		var err error
		if action == "cancel" {
			_, err = actions.Cancel(context.Background(), req)
		} else {
			_, err = actions.Retry(context.Background(), req)
		}
		var typed adminhttp.JobActionError
		if !errors.As(err, &typed) || typed.Code != want {
			t.Fatalf("%s %s error=%T %v", action, req.JobID, err, err)
		}
	}
	assertCode("cancel", adminhttp.JobActionRequest{JobID: "fake", IdempotencyKey: "a"}, "capability_unavailable")
	assertCode("cancel", adminhttp.JobActionRequest{JobID: "terminal", IdempotencyKey: "b"}, "job_not_active")
	assertCode("retry", adminhttp.JobActionRequest{JobID: "active", IdempotencyKey: "c"}, "job_not_terminal")
	assertCode("retry", adminhttp.JobActionRequest{JobID: "terminal", IdempotencyKey: "d"}, "retry_unavailable")
}

func TestJobActionReceiptsAreBounded(t *testing.T) {
	actions := NewJobActionManager("", NewJobManager(""), nil)
	base := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	for index := 0; index < maxJobActionReceipts+3; index++ {
		key := hashJobAction(time.Duration(index).String())
		actions.receipts[key] = jobActionReceiptDisk{KeyHash: key, Receipt: adminhttp.JobActionReceipt{CreatedAt: base.Add(time.Duration(index) * time.Second)}}
	}
	actions.pruneLocked()
	if len(actions.receipts) != maxJobActionReceipts {
		t.Fatalf("receipts=%d want=%d", len(actions.receipts), maxJobActionReceipts)
	}
}
