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
	job, created, err := jobs.createCoalescedJob(SyncJobType, "owner/repo", "", 1, "sync-work", "cache-1", "reg-1", "", cancel)
	if err != nil {
		t.Fatal(err)
	}
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

func TestJobActionCanCancelRepositoryDocsIndex(t *testing.T) {
	jobs := NewJobManager("")
	jobContext, cancel := context.WithCancel(context.Background())
	job, created, err := jobs.createCoalescedJob(RepositoryDocsIndexJobType, "owner/repo", "profile", 0, "repo-doc-work", "cache-1", "reg-1", "", cancel)
	if err != nil || !created {
		t.Fatalf("job=%+v created=%t err=%v", job, created, err)
	}
	jobs.updateJob(job.ID, func(stored *Job, now time.Time) {
		stored.Status = JobStatusRunning
		stored.StartedAt = &now
	})
	go func() {
		<-jobContext.Done()
		jobs.finishJob(job.ID, JobStatusCancelled, "cancelled")
	}()

	receipt, err := NewJobActionManager("", jobs, nil).Cancel(context.Background(), adminhttp.JobActionRequest{JobID: job.ID, IdempotencyKey: "cancel-repo-docs"})
	if err != nil || receipt.Outcome != "cancelled" || receipt.JobStatus != JobStatusCancelled {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
}

func TestJobActionRetryCoalescesEquivalentWork(t *testing.T) {
	jobs := NewJobManager("")
	now := time.Now().UTC()
	finished := now.Add(-time.Minute)
	jobs.jobs["job-000001"] = &Job{ID: "job-000001", Type: RAGIndexJobType, RepoID: "owner/repo", CacheUUID: "cache-1", RegistrationID: "reg-1", Status: JobStatusFailed, CreatedAt: finished, UpdatedAt: finished, FinishedAt: &finished}
	jobs.nextID = 1
	active, _, err := jobs.createCoalescedJob(RAGIndexJobType, "owner/repo", "profile", 10, "rag-work", "cache-1", "reg-1", "ns-1", func() {})
	if err != nil {
		t.Fatal(err)
	}
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
	jobs.jobs["job-000001"] = &Job{ID: "job-000001", Type: SyncJobType, RepoID: "owner/repo", CacheUUID: "cache-1", RegistrationID: "reg-1", Status: JobStatusFailed, CreatedAt: finished, UpdatedAt: finished, FinishedAt: &finished, SyncCollections: []SyncCollectionView{{Collection: "wiki", Outcome: SyncCollectionPermanentFailure}}}
	jobs.nextID = 1
	actions := NewJobActionManager("", jobs, nil)
	actions.reconcile = func(_ context.Context, registrationID, collection string) (MaintenanceReconcileResult, error) {
		if registrationID != "reg-1" || collection != "wiki" {
			t.Fatalf("registration=%q collection=%q", registrationID, collection)
		}
		created, _, err := jobs.createCoalescedJob(SyncJobType, "owner/repo", "", 10, "new-sync-work", "cache-1", "reg-1", "", func() {})
		if err != nil {
			return MaintenanceReconcileResult{}, err
		}
		return MaintenanceReconcileResult{JobsStarted: []string{created.ID}}, nil
	}
	receipt, err := actions.Retry(context.Background(), adminhttp.JobActionRequest{JobID: "job-000001", Collection: "wiki", IdempotencyKey: "retry-new"})
	if err != nil || receipt.Outcome != "created" || receipt.ResultJob != "job-000002" || receipt.JobStatus != JobStatusQueued {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
}

func TestJobActionRetryPreparedReceiptPreventsRestartDuplicateAfterFinalSaveFailure(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "actions.json")
	jobs := NewJobManager(filepath.Join(dir, "jobs.json"))
	now := time.Now().UTC()
	jobs.jobs["job-000001"] = &Job{ID: "job-000001", Type: SyncJobType, RepoID: "owner/repo", CacheUUID: "cache-1", RegistrationID: "reg-1", Status: JobStatusSucceeded, SyncHealth: SyncHealthPartial, CreatedAt: now, UpdatedAt: now, FinishedAt: &now, SyncCollections: []SyncCollectionView{{Collection: "wiki", Outcome: SyncCollectionPermanentFailure}}}
	jobs.nextID = 1
	if err := jobs.saveLocked(); err != nil {
		t.Fatal(err)
	}
	actions := NewJobActionManager(path, jobs, nil)
	runs := 0
	actions.reconcile = func(context.Context, string, string) (MaintenanceReconcileResult, error) {
		runs++
		created, _, err := jobs.createCoalescedJob(SyncJobType, "owner/repo", "", 1, "retry-work", "cache-1", "reg-1", "", func() {})
		if err != nil {
			return MaintenanceReconcileResult{}, err
		}
		return MaintenanceReconcileResult{JobsStarted: []string{created.ID}}, nil
	}
	writes := 0
	actions.writeFile = func(target string, data []byte, mode os.FileMode) error {
		writes++
		if writes == 2 {
			return errors.New("final receipt unavailable")
		}
		return durableAtomicWriteFile(target, data, mode)
	}
	req := adminhttp.JobActionRequest{JobID: "job-000001", Collection: "wiki", IdempotencyKey: "retry-crash-window"}
	if _, err := actions.Retry(context.Background(), req); err == nil || runs != 1 {
		t.Fatalf("first retry err=%v runs=%d", err, runs)
	}

	restarted := NewJobActionManager(path, jobs, nil)
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	restarted.reconcile = func(context.Context, string, string) (MaintenanceReconcileResult, error) {
		runs++
		coalesced, created, err := jobs.createCoalescedJob(SyncJobType, "owner/repo", "", 1, "retry-work", "cache-1", "reg-1", "", func() {})
		if err != nil || created {
			t.Fatalf("replay coalescing job=%+v created=%t err=%v", coalesced, created, err)
		}
		return MaintenanceReconcileResult{JobsCoalesced: []string{coalesced.ID}}, nil
	}
	replay, err := restarted.Retry(context.Background(), req)
	if err != nil || !replay.Replayed || replay.Outcome != "coalesced" || replay.ResultJob != "job-000002" || runs != 2 {
		t.Fatalf("restart replay=%+v err=%v runs=%d", replay, err, runs)
	}
}

func TestJobActionRetryPreparedReceiptRedrivesCrashBeforeMutation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "actions.json")
	jobs := NewJobManager(filepath.Join(dir, "jobs.json"))
	now := time.Now().UTC()
	jobs.jobs["job-000001"] = &Job{ID: "job-000001", Type: SyncJobType, RepoID: "owner/repo", CacheUUID: "cache-1", RegistrationID: "reg-1", Status: JobStatusSucceeded, SyncHealth: SyncHealthPartial, CreatedAt: now, UpdatedAt: now, FinishedAt: &now, SyncCollections: []SyncCollectionView{{Collection: "wiki", Outcome: SyncCollectionPermanentFailure}}}
	jobs.nextID = 1
	if err := jobs.saveLocked(); err != nil {
		t.Fatal(err)
	}
	req := adminhttp.JobActionRequest{JobID: "job-000001", Collection: "wiki", IdempotencyKey: "retry-before-mutation"}
	keyHash := hashJobAction(req.IdempotencyKey)
	intentHash := hashJobAction("retry\x00" + req.JobID + "\x00" + req.Collection)
	before := NewJobActionManager(path, jobs, nil)
	before.receipts[keyHash] = jobActionReceiptDisk{KeyHash: keyHash, IntentHash: intentHash, Receipt: adminhttp.JobActionReceipt{
		ReceiptID: "receipt-" + keyHash[:16], Action: "retry", TargetJob: req.JobID, Outcome: "pending", JobStatus: JobStatusSucceeded, CreatedAt: now,
	}}
	if err := before.saveLocked(); err != nil {
		t.Fatal(err)
	}

	restarted := NewJobActionManager(path, jobs, nil)
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	runs := 0
	restarted.reconcile = func(context.Context, string, string) (MaintenanceReconcileResult, error) {
		runs++
		created, _, err := jobs.createCoalescedJob(SyncJobType, "owner/repo", "", 1, "retry-before-work", "cache-1", "reg-1", "", func() {})
		if err != nil {
			return MaintenanceReconcileResult{}, err
		}
		return MaintenanceReconcileResult{JobsStarted: []string{created.ID}}, nil
	}
	receipt, err := restarted.Retry(context.Background(), req)
	if err != nil || !receipt.Replayed || receipt.Outcome != "created" || receipt.ResultJob != "job-000002" || runs != 1 {
		t.Fatalf("redriven receipt=%+v err=%v runs=%d", receipt, err, runs)
	}
	settled := NewJobActionManager(path, jobs, nil)
	if err := settled.Load(); err != nil {
		t.Fatal(err)
	}
	replayed, err := settled.Retry(context.Background(), req)
	if err != nil || !replayed.Replayed || replayed.Outcome != "created" || runs != 1 {
		t.Fatalf("settled replay=%+v err=%v runs=%d", replayed, err, runs)
	}
}

func TestJobActionCollectionRetryDoesNotCoalesceDifferentActiveCollection(t *testing.T) {
	jobs := NewJobManager("")
	now := time.Now().UTC()
	finished := now.Add(-time.Minute)
	jobs.jobs["job-000001"] = &Job{ID: "job-000001", Type: SyncJobType, RepoID: "owner/repo", CacheUUID: "cache-1", RegistrationID: "reg-1", Status: JobStatusSucceeded, SyncHealth: SyncHealthPartial, CreatedAt: finished, UpdatedAt: finished, FinishedAt: &finished, SyncCollections: []SyncCollectionView{{Collection: "issues", Outcome: SyncCollectionPermanentFailure}}}
	jobs.nextID = 1
	active, created, err := jobs.createCoalescedJob(SyncJobType, "owner/repo", "", 0, "sync:cache-1:owner/repo:head:wiki", "cache-1", "reg-1", "", func() {})
	if err != nil || !created {
		t.Fatalf("active=%+v created=%t err=%v", active, created, err)
	}
	actions := NewJobActionManager("", jobs, nil)
	reconciled := false
	actions.reconcile = func(_ context.Context, registrationID, collection string) (MaintenanceReconcileResult, error) {
		reconciled = true
		if registrationID != "reg-1" || collection != "issues" {
			t.Fatalf("registration=%q collection=%q", registrationID, collection)
		}
		started, created, err := jobs.createCoalescedJob(SyncJobType, "owner/repo", "", 0, "sync:cache-1:owner/repo:head:issues", "cache-1", "reg-1", "", func() {})
		if err != nil || !created {
			return MaintenanceReconcileResult{}, err
		}
		return MaintenanceReconcileResult{JobsStarted: []string{started.ID}}, nil
	}
	receipt, err := actions.Retry(context.Background(), adminhttp.JobActionRequest{JobID: "job-000001", Collection: "issues", IdempotencyKey: "retry-issues"})
	if err != nil || !reconciled || receipt.Outcome != "created" || receipt.ResultJob == active.ID {
		t.Fatalf("receipt=%+v reconciled=%t active=%+v err=%v", receipt, reconciled, active, err)
	}
}

func TestJobActionCollectionRetryReportsExactWorkCoalescing(t *testing.T) {
	jobs := NewJobManager("")
	now := time.Now().UTC()
	finished := now.Add(-time.Minute)
	jobs.jobs["job-000001"] = &Job{ID: "job-000001", Type: SyncJobType, RepoID: "owner/repo", CacheUUID: "cache-1", RegistrationID: "reg-1", Status: JobStatusSucceeded, SyncHealth: SyncHealthPartial, CreatedAt: finished, UpdatedAt: finished, FinishedAt: &finished, SyncCollections: []SyncCollectionView{{Collection: "issues", Outcome: SyncCollectionPermanentFailure}}}
	jobs.nextID = 1
	active, created, err := jobs.createCoalescedJob(SyncJobType, "owner/repo", "", 0, "sync:cache-1:owner/repo:head:truefalsefalsefalsefalse", "cache-1", "reg-1", "", func() {})
	if err != nil || !created {
		t.Fatalf("active=%+v created=%t err=%v", active, created, err)
	}
	actions := NewJobActionManager("", jobs, nil)
	actions.reconcile = func(context.Context, string, string) (MaintenanceReconcileResult, error) {
		return MaintenanceReconcileResult{JobsCoalesced: []string{active.ID}}, nil
	}
	receipt, err := actions.Retry(context.Background(), adminhttp.JobActionRequest{JobID: "job-000001", Collection: "issues", IdempotencyKey: "retry-issues-coalesced"})
	if err != nil || receipt.Outcome != "coalesced" || receipt.ResultJob != active.ID || receipt.JobStatus != JobStatusQueued {
		t.Fatalf("receipt=%+v active=%+v err=%v", receipt, active, err)
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
	jobs.jobs["terminal"].RegistrationID = "reg"
	jobs.jobs["terminal"].SyncCollections = []SyncCollectionView{{Collection: "wiki", Outcome: SyncCollectionSuccess}}
	assertCode("retry", adminhttp.JobActionRequest{JobID: "terminal", Collection: "wiki", IdempotencyKey: "e"}, "collection_retry_unavailable")
	assertCode("retry", adminhttp.JobActionRequest{JobID: "terminal", Collection: "unknown", IdempotencyKey: "f"}, "collection_retry_unavailable")
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
