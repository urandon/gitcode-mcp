package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/service"
)

type admissionSyncService struct {
	calls []string
	err   error
}

type codedSyncTestError struct{ code string }

func (e codedSyncTestError) Error() string          { return e.code }
func (e codedSyncTestError) DiagnosticCode() string { return e.code }

func TestDurableSyncSelectorInvariantCoversDefaultAndComments(t *testing.T) {
	if !syncDurableCollections(StartSyncJobRequest{}) || !syncDurableCollections(StartSyncJobRequest{IssueComments: true, PRComments: true}) {
		t.Fatal("daemon selector combination escaped durable staging")
	}
	defaultReq := normalizeDurableSyncRequest(StartSyncJobRequest{})
	if !defaultReq.Issues || !defaultReq.Wiki || defaultReq.Pulls || defaultReq.IssueComments || defaultReq.PRComments {
		t.Fatalf("default durable selection=%+v", defaultReq)
	}
	commentsReq := normalizeDurableSyncRequest(StartSyncJobRequest{Comments: true})
	if !commentsReq.PRComments {
		t.Fatalf("legacy comments selector was not normalized: %+v", commentsReq)
	}
	works := durableCollectionWorks(nil, service.BulkSyncRequest{}, StartSyncJobRequest{Issues: true, IssueComments: true, Wiki: true, Pulls: true, PRComments: true})
	got := make([]string, 0, len(works))
	for _, work := range works {
		got = append(got, work.collection)
	}
	want := []string{"issues", "issue_comments", "wiki", "pulls", "pr_comments"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("durable collection order=%v want=%v", got, want)
	}
}

func TestDurableCollectionPersistsPartialFetchOutcomeBeforeCommit(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cachePath := filepath.Join(root, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	binding := cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}
	if err := store.AddRepository(ctx, binding); err != nil {
		t.Fatal(err)
	}
	identity, _ := store.CacheIdentity(ctx)
	schema, _ := store.SchemaVersion(ctx)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	jobID := "job-000001"
	jobs := NewJobManager(filepath.Join(root, "jobs.json"))
	now := time.Now().UTC()
	jobs.jobs[jobID] = &Job{ID: jobID, Type: SyncJobType, RepoID: binding.RepoID, CacheUUID: identity.UUID, Status: JobStatusRunning, CreatedAt: now, UpdatedAt: now}
	jobs.nextID = 1
	if err := jobs.saveLocked(); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(t, "darwin")
	manager.RuntimeDir = root
	workflow := &SyncStageWorkflow{Collections: []string{"issues", "wiki"}, Current: 0, ProviderMode: "fixture"}
	fetchErr := codedSyncTestError{code: "provider_failure"}
	result, _, err := jobs.runDurableCollection(ctx, manager, jobID, StartSyncJobRequest{
		RepoID: binding.RepoID, CachePath: cachePath, CacheUUID: identity.UUID,
		RegistrationID: maintenanceRegistrationID(identity.UUID, binding.RepoID),
	}, schema, syncRepositoryBindingFingerprint(binding), durableCollectionWork{
		collection: "issues", remoteType: "issue",
		fetch: func(context.Context) (durableCollectionBatch, error) {
			return durableCollectionBatch{payload: json.RawMessage(`{"items":[1,2]}`), recordCount: 2, recordsListed: 3, idempotencyKey: "partial-fetch", fetchedAt: now}, fetchErr
		},
		commit: func(context.Context, SyncStageEnvelope) (*service.SyncResourcesResult, error) {
			return &service.SyncResourcesResult{RecordsListed: 3, SuccessCount: 2}, nil
		},
	}, workflow)
	if !errors.Is(err, fetchErr) || result == nil || result.SuccessCount != 2 {
		t.Fatalf("durable partial result=%+v err=%v", result, err)
	}
	stages, err := NewSyncStageJournal(root, SyncStageLimits{}).List()
	if err != nil || len(stages) != 1 || stages[0].Workflow == nil || stages[0].Workflow.Outcome == nil {
		t.Fatalf("persisted stages=%+v err=%v", stages, err)
	}
	outcome := *stages[0].Workflow.Outcome
	if outcome.RecordsListed != 3 || outcome.SuccessCount != 2 || outcome.ErrorClass != "provider_failure" || outcome.ErrorCollection != "issue" {
		t.Fatalf("persisted partial outcome=%+v", outcome)
	}
}

func TestDirectCacheWritersNormalizeIdentityAndRespectAuthorityFence(t *testing.T) {
	ctx := context.Background()
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	identity, _ := store.CacheIdentity(ctx)
	_ = store.Close()
	manager := newTestManager(t, "darwin")
	cfg := config.Default()
	manager.EffectiveConfig = &cfg
	jobs := NewJobManager("")
	release, blockers := jobs.BeginCacheMutationFence(identity.UUID)
	defer release()
	if len(blockers) != 0 {
		t.Fatalf("unexpected blockers=%v", blockers)
	}
	_, err = jobs.StartSync(ctx, manager, StartSyncJobRequest{RepoID: "owner/repo", CachePath: cachePath, Issues: true})
	var fenced CacheMutationFenceError
	if !errors.As(err, &fenced) {
		t.Fatalf("missing-uuid writer error=%T %v", err, err)
	}
	_, err = jobs.StartRAGIndex(ctx, manager, StartRAGIndexJobRequest{RepoID: "owner/repo", CachePath: cachePath, CacheUUID: "wrong-uuid"})
	if err == nil || err.Error() != "service: cache uuid does not match the selected cache authority" {
		t.Fatalf("wrong-uuid writer error=%T %v", err, err)
	}
}

func TestNormalizeCacheWriterIdentityDoesNotExposePrivateCachePath(t *testing.T) {
	manager := newTestManager(t, "darwin")
	cfg := config.Default()
	manager.EffectiveConfig = &cfg
	privatePath := "/private/sentinel-user/cache-authority/missing.db"
	cachePath, cacheUUID, registrationID, repoID := privatePath, "", "", "owner/repo"
	err := normalizeCacheWriterIdentity(context.Background(), manager, &cachePath, &cacheUUID, &registrationID, &repoID)
	if err == nil || strings.Contains(err.Error(), privatePath) || strings.Contains(err.Error(), "sentinel-user") {
		t.Fatalf("public error leaked cache authority: %T %v", err, err)
	}
	coded, ok := err.(interface{ DiagnosticCode() string })
	if !ok || coded.DiagnosticCode() != "cache_authority_unavailable" {
		t.Fatalf("diagnostic=%T %v", err, err)
	}
}

func TestDirectCacheWriterBlocksConflictFenceButAllowsDurableFetchAdmission(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cachePath := filepath.Join(root, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	identity, _ := store.CacheIdentity(ctx)
	_ = store.Close()
	jobs := NewJobManager("")
	releaseWriter, err := jobs.BeginDirectCacheWriter(identity.UUID, "admin-binding-test")
	if err != nil {
		t.Fatal(err)
	}
	releasedWriter := false
	defer func() {
		if !releasedWriter {
			releaseWriter()
		}
	}()
	releaseFence, blocked := jobs.BeginCacheMutationFence(identity.UUID)
	if len(blocked) != 1 || blocked[0] != "admin-binding-test" {
		t.Fatalf("direct writer did not block conflict fence: %v", blocked)
	}
	releaseFence()
	manager := newTestManager(t, "darwin")
	manager.RuntimeDir = root
	cfg := config.Default()
	manager.EffectiveConfig = &cfg
	job, err := jobs.StartSync(ctx, manager, StartSyncJobRequest{RepoID: "owner/repo", CachePath: cachePath, Issues: true, ProviderMode: "fixture", MaxPages: 1})
	if err != nil {
		t.Fatalf("durable fetch admission failed: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var observed Job
	for time.Now().Before(deadline) {
		observed, _ = jobs.Get(job.ID)
		if observed.SyncStage != nil && observed.SyncStage.Phase == SyncStageWaitingCommit {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if observed.SyncStage == nil || observed.SyncStage.Phase != SyncStageWaitingCommit || observed.SyncStage.BlockingOp != "direct_cache_write" {
		t.Fatalf("durable sync did not stage behind direct writer: %+v", observed)
	}
	releaseWriter()
	releasedWriter = true
	for time.Now().Before(deadline) {
		observed, _ = jobs.Get(job.ID)
		if observed.Status == JobStatusSucceeded {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if observed.Status != JobStatusSucceeded {
		t.Fatalf("durable sync did not commit after direct writer release: %+v", observed)
	}
}

func TestDurableSyncFetchDoesNotReserveWriterAdmission(t *testing.T) {
	jobs := NewJobManager("")
	_, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	first, created, err := jobs.createCoalescedJob(SyncJobType, "owner/first", "", 0, "sync-first", "cache-shared", "reg-first", "", cancelA)
	if err != nil || !created {
		t.Fatalf("first sync admission job=%+v created=%t err=%v", first, created, err)
	}
	_, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	second, created, err := jobs.createCoalescedJob(SyncJobType, "owner/second", "", 0, "sync-second", "cache-shared", "reg-second", "", cancelB)
	if err != nil || !created || second.ID == first.ID {
		t.Fatalf("second sync admission job=%+v created=%t err=%v", second, created, err)
	}
	releaseDirect, err := jobs.BeginDirectCacheWriter("cache-shared", "admin-arrived-during-fetch")
	if err != nil {
		t.Fatalf("direct writer was blocked by provider-only sync admission: %v", err)
	}
	releaseDirect()
	jobs.updateJob(first.ID, func(job *Job, _ time.Time) {
		job.SyncStage = &SyncStageView{Phase: SyncStageCommitting}
	})
	if _, err := jobs.BeginDirectCacheWriter("cache-shared", "admin-during-commit"); err == nil {
		t.Fatal("direct writer overlapped an active durable commit")
	} else {
		var busy ErrCacheWriterBusy
		if !errors.As(err, &busy) || busy.ActiveJobID != first.ID {
			t.Fatalf("commit writer blocker=%T %v", err, err)
		}
	}
	jobs.updateJob(first.ID, func(job *Job, _ time.Time) {
		job.SyncStage = &SyncStageView{Phase: SyncStageFetching}
	})
	_, cancelRAG := context.WithCancel(context.Background())
	defer cancelRAG()
	third, created, err := jobs.createCoalescedJob(RAGIndexJobType, "owner/third", "", 0, "rag-third", "cache-shared", "reg-third", "", cancelRAG)
	if err != nil || !created || third.Type != RAGIndexJobType {
		t.Fatalf("RAG writer was blocked by provider-only sync admission: job=%+v created=%t err=%v", third, created, err)
	}
}

func (s *admissionSyncService) result(name string) (*service.SyncResourcesResult, error) {
	s.calls = append(s.calls, name)
	if len(s.calls) == 1 {
		return nil, s.err
	}
	return &service.SyncResourcesResult{SuccessCount: 1}, nil
}

func (s *admissionSyncService) BulkSyncIssues(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error) {
	return s.result("issues")
}
func (s *admissionSyncService) BulkSyncIssueComments(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error) {
	return s.result("issue_comments")
}
func (s *admissionSyncService) BulkSyncWiki(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error) {
	return s.result("wiki")
}
func (s *admissionSyncService) BulkSyncPullRequests(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error) {
	return s.result("pulls")
}
func (s *admissionSyncService) BulkSyncPRComments(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error) {
	return s.result("pr_comments")
}
func (s *admissionSyncService) BulkSyncAll(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error) {
	return s.result("all")
}

func TestRunSyncSelectionsStopsAfterWriterAdmissionContention(t *testing.T) {
	holder := cache.ErrLockContention{
		Path:      "cache.db.lock",
		Operation: "bulk-sync-issues",
		RepoID:    "owner/repo",
		PID:       42,
		StartedAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
	}
	svc := &admissionSyncService{err: holder}
	result, collections, err := runSyncSelections(context.Background(), svc, service.BulkSyncRequest{RepoID: "owner/repo"}, StartSyncJobRequest{Issues: true, IssueComments: true, Wiki: true, Pulls: true, PRComments: true})

	var contention cache.ErrLockContention
	if !errors.As(err, &contention) {
		t.Fatalf("runSyncSelections error = %T %[1]v, want ErrLockContention", err)
	}
	if contention.Operation != holder.Operation || contention.RepoID != holder.RepoID || contention.PID != holder.PID || !contention.StartedAt.Equal(holder.StartedAt) {
		t.Fatalf("contention = %#v, want preserved holder metadata %#v", contention, holder)
	}
	if len(svc.calls) != 1 || svc.calls[0] != "issues" {
		t.Fatalf("calls = %v, want only first selected collection", svc.calls)
	}
	if len(collections) != 1 || collections[0].RemoteType != "issue" {
		t.Fatalf("collections = %#v, want only issue attempt", collections)
	}
	if result == nil || result.SuccessCount != 0 || result.FailureCount != 0 {
		t.Fatalf("result = %#v, want empty aggregate without synthetic partial failure", result)
	}
	var partial *service.PartialSyncError
	if errors.As(err, &partial) {
		t.Fatalf("error = %T %[1]v, must preserve direct contention", err)
	}
}

func TestFailedSyncCollectionProgressNamesEachFailedCollection(t *testing.T) {
	events := failedSyncCollectionProgress([]syncCollectionResult{
		{RemoteType: "wiki", Err: errors.New("empty repository")},
		{RemoteType: "pr_comment", Result: &service.SyncResourcesResult{FailureCount: 2}},
		{RemoteType: "issue", Result: &service.SyncResourcesResult{SuccessCount: 1}},
	})
	if len(events) != 2 || events[0].Collection != "wiki" || events[0].RecordsFailed != 1 || events[1].Collection != "pr_comment" || events[1].RecordsFailed != 2 {
		t.Fatalf("events=%+v", events)
	}
}

func TestIssueOnlyDaemonSyncWaitsForWriterAndCommitsRetainedStage(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cachePath := filepath.Join(root, "cache.db")
	lockPath := cachePath + ".lock"
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	held, err := store.AcquireWriter(ctx, cache.WriterRequest{Operation: "rag-index", RepoID: "owner/repo", LockPath: lockPath})
	if err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(t, "darwin")
	manager.RuntimeDir = root
	cfg := config.Default()
	cfg.LockPath = lockPath
	manager.EffectiveConfig = &cfg
	jobs := NewJobManager(filepath.Join(root, "jobs.json"))
	job, err := jobs.StartSync(ctx, manager, StartSyncJobRequest{RepoID: "owner/repo", CachePath: cachePath, CacheUUID: identity.UUID, Issues: true, ProviderMode: "fixture", MaxPages: 1, PerPage: 100})
	if err != nil {
		t.Fatalf("StartSync: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var waiting Job
	for time.Now().Before(deadline) {
		waiting, _ = jobs.Get(job.ID)
		if waiting.SyncStage != nil && waiting.SyncStage.Phase == SyncStageWaitingCommit {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if waiting.SyncStage == nil || waiting.SyncStage.Phase != SyncStageWaitingCommit || waiting.Status != JobStatusRunning || waiting.SyncStage.BlockerClass != "cache_busy" || waiting.SyncStage.BlockingOp != "rag-index" {
		t.Fatalf("waiting job = %+v", waiting)
	}
	if err := store.ReleaseWriter(ctx, held); err != nil {
		t.Fatal(err)
	}
	for time.Now().Before(deadline) {
		waiting, _ = jobs.Get(job.ID)
		if waiting.Status == JobStatusSucceeded {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if waiting.Status != JobStatusSucceeded || waiting.SyncStage == nil || waiting.SyncStage.Phase != SyncStageCommitted || waiting.SyncStage.Committed != 1 {
		t.Fatalf("terminal job = %+v", waiting)
	}
	if _, err := store.GetSourceScoped(ctx, "owner/repo", "ISSUE-42"); err != nil {
		t.Fatalf("committed source missing: %v", err)
	}
	stages, err := NewSyncStageJournal(root, SyncStageLimits{}).List()
	if err != nil || len(stages) != 0 {
		t.Fatalf("stages=%+v err=%v", stages, err)
	}
	_ = store.Close()
}

func TestDaemonRestartRecoversStagedBatchWithoutProvider(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	cachePath := filepath.Join(root, "cache.db")
	lockPath := cachePath + ".lock"
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := store.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := service.New(store).FetchIssueSyncBatch(ctx, service.BulkSyncRequest{RepoID: "owner/repo", IdempotencyKey: "restart-stage", Bounds: &service.SyncBounds{MaxPages: 1}, PerPage: 100})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	registrationID := maintenanceRegistrationID(identity.UUID, "owner/repo")
	journal := NewSyncStageJournal(root, SyncStageLimits{})
	stage, err := journal.Create(SyncStageEnvelope{
		JobID: "job-000001", CacheUUID: identity.UUID, CacheSchema: schema, CachePath: cachePath,
		RegistrationID: registrationID, RepoID: "owner/repo", BindingFingerprint: syncRepositoryBindingFingerprint(cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}), Collection: "issues",
		IdempotencyKey: "restart-stage", RecordCount: batch.RecordCount(), Payload: payload,
		State: SyncStageState{Phase: SyncStageStaged, RetryBudget: defaultSyncCommitRetries, FetchedAt: batch.FetchedAt},
	})
	if err != nil {
		t.Fatal(err)
	}
	view := stage.PublicView()
	jobsPath := filepath.Join(root, "jobs.json")
	before := NewJobManager(jobsPath)
	createdAt := time.Now().UTC().Add(-time.Minute)
	before.jobs[stage.JobID] = &Job{ID: stage.JobID, Type: SyncJobType, RepoID: "owner/repo", CacheUUID: identity.UUID, RegistrationID: registrationID, Status: JobStatusRunning, CreatedAt: createdAt, UpdatedAt: createdAt, SyncStage: &view}
	before.nextID = 1
	if err := before.saveLocked(); err != nil {
		t.Fatal(err)
	}
	held, err := store.AcquireWriter(ctx, cache.WriterRequest{Operation: "rag-index", RepoID: "owner/repo", LockPath: lockPath})
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewJobManager(jobsPath)
	if err := restarted.LoadAndMarkInterrupted(); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(t, "darwin")
	manager.RuntimeDir = root
	cfg := config.Default()
	cfg.LockPath = lockPath
	manager.EffectiveConfig = &cfg
	if err := restarted.RecoverSyncStages(ctx, manager); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var job Job
	for time.Now().Before(deadline) {
		job, _ = restarted.Get(stage.JobID)
		if job.SyncStage != nil && job.SyncStage.Phase == SyncStageWaitingCommit {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job.Status != JobStatusRunning || job.SyncStage == nil || job.SyncStage.Phase != SyncStageWaitingCommit {
		t.Fatalf("recovered waiting job = %+v", job)
	}
	if err := store.ReleaseWriter(ctx, held); err != nil {
		t.Fatal(err)
	}
	for time.Now().Before(deadline) {
		job, _ = restarted.Get(stage.JobID)
		if job.Status == JobStatusSucceeded {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if job.Status != JobStatusSucceeded || job.SyncStage == nil || job.SyncStage.Phase != SyncStageCommitted {
		t.Fatalf("recovered terminal job = %+v", job)
	}
	if _, err := store.GetSourceScoped(ctx, "owner/repo", "ISSUE-42"); err != nil {
		t.Fatalf("recovered commit missing source: %v", err)
	}
}

func TestDaemonRestartUsesAtomicReceiptWhenJournalMissedCommitTerminal(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cachePath := filepath.Join(root, "cache.db")
	lockPath := cachePath + ".lock"
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	binding := cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}
	if err := store.AddRepository(ctx, binding); err != nil {
		t.Fatal(err)
	}
	identity, _ := store.CacheIdentity(ctx)
	schema, _ := store.SchemaVersion(ctx)
	svc, err := service.NewWithMode(store, gitcode.ProviderModeFixture, "", service.ServiceConfig{LockPath: lockPath})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := svc.FetchIssueSyncBatch(ctx, service.BulkSyncRequest{RepoID: binding.RepoID, IdempotencyKey: "receipt-recovery", Bounds: &service.SyncBounds{MaxPages: 1, MaxRecords: 100}, PerPage: 100})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	registrationID := maintenanceRegistrationID(identity.UUID, binding.RepoID)
	journal := NewSyncStageJournal(root, SyncStageLimits{})
	stage, err := journal.Create(SyncStageEnvelope{
		JobID: "job-000001", CacheUUID: identity.UUID, CacheSchema: schema, CachePath: cachePath,
		RegistrationID: registrationID, RepoID: binding.RepoID, BindingFingerprint: syncRepositoryBindingFingerprint(binding),
		Collection: "issues", IdempotencyKey: batch.IdempotencyKey, RecordCount: batch.RecordCount(), Payload: payload,
		State: SyncStageState{Phase: SyncStageCommitting, RetryBudget: defaultSyncCommitRetries, FetchedAt: batch.FetchedAt},
	})
	if err != nil {
		t.Fatal(err)
	}
	batch.CommitReceipt = syncStageCommitReceipt(stage)
	if _, err := svc.CommitIssueSyncBatch(ctx, batch, nil); err != nil {
		t.Fatal(err)
	}
	// Simulate ENOSPC/rename failure after SQLite commit: the private sidecar is
	// still nonterminal while the transaction receipt already proves success.
	view := stage.PublicView()
	jobsPath := filepath.Join(root, "jobs.json")
	before := NewJobManager(jobsPath)
	now := time.Now().UTC()
	before.jobs[stage.JobID] = &Job{ID: stage.JobID, Type: SyncJobType, RepoID: binding.RepoID, CacheUUID: identity.UUID, RegistrationID: registrationID, Status: JobStatusRunning, CreatedAt: now, UpdatedAt: now, SyncStage: &view}
	before.nextID = 1
	if err := before.saveLocked(); err != nil {
		t.Fatal(err)
	}
	restarted := NewJobManager(jobsPath)
	if err := restarted.LoadAndMarkInterrupted(); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(t, "darwin")
	manager.RuntimeDir = root
	cfg := config.Default()
	cfg.LockPath = lockPath
	manager.EffectiveConfig = &cfg
	if err := restarted.RecoverSyncStages(ctx, manager); err != nil {
		t.Fatal(err)
	}
	job, ok := restarted.Get(stage.JobID)
	if !ok || job.Status != JobStatusSucceeded || job.SyncStage == nil || job.SyncStage.Phase != SyncStageCommitted {
		t.Fatalf("receipt recovery job=%+v ok=%t", job, ok)
	}
	if stages, err := journal.List(); err != nil || len(stages) != 0 {
		t.Fatalf("receipt-confirmed terminal stage retained: %+v err=%v", stages, err)
	}
}

func TestDaemonRestartContinuesMixedWorkflowAfterCommittedCollection(t *testing.T) {
	tests := []struct {
		name            string
		terminalSidecar bool
		prefixError     bool
	}{
		{name: "receipt_only"},
		{name: "terminal_sidecar", terminalSidecar: true},
		{name: "receipt_only_partial_provider_failure", prefixError: true},
		{name: "terminal_sidecar_partial_provider_failure", terminalSidecar: true, prefixError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			cachePath := filepath.Join(root, "cache.db")
			store, err := cache.NewSQLiteStore(ctx, cachePath)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			binding := cache.RepositoryBinding{RepoID: "fixture-a", Owner: "fixture", Name: "a", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}
			if err := store.AddRepository(ctx, binding); err != nil {
				t.Fatal(err)
			}
			identity, _ := store.CacheIdentity(ctx)
			schema, _ := store.SchemaVersion(ctx)
			lockPath := cachePath + ".lock"
			svc, err := service.NewWithMode(store, gitcode.ProviderModeFixture, "", service.ServiceConfig{LockPath: lockPath})
			if err != nil {
				t.Fatal(err)
			}
			request := StartSyncJobRequest{RepoID: binding.RepoID, CachePath: cachePath, CacheUUID: identity.UUID, RegistrationID: maintenanceRegistrationID(identity.UUID, binding.RepoID), ProviderMode: "fixture", Issues: true, Wiki: true, IdempotencyKey: "mixed-recovery", MaxPages: 1, PerPage: 100}
			batch, err := svc.FetchIssueSyncBatch(ctx, service.BulkSyncRequest{RepoID: binding.RepoID, IdempotencyKey: request.IdempotencyKey, Bounds: &service.SyncBounds{MaxPages: 1}, PerPage: 100})
			if err != nil {
				t.Fatal(err)
			}
			payload, err := json.Marshal(batch)
			if err != nil {
				t.Fatal(err)
			}
			journal := NewSyncStageJournal(root, SyncStageLimits{})
			workflow := syncStageWorkflowFromRequest(request, []string{"issues", "wiki"}, 0)
			workflow.Outcome = &SyncStageWorkflowOutcome{RecordsListed: batch.RecordsListed, SuccessCount: batch.RecordCount()}
			if tc.prefixError {
				workflow.Outcome.ErrorClass, workflow.Outcome.ErrorCollection = "provider_failure", "issue"
			}
			stage, err := journal.Create(SyncStageEnvelope{
				JobID: "job-000001", CacheUUID: identity.UUID, CacheSchema: schema, CachePath: cachePath,
				RegistrationID: request.RegistrationID, RepoID: binding.RepoID, BindingFingerprint: syncRepositoryBindingFingerprint(binding),
				Collection: "issues", IdempotencyKey: batch.IdempotencyKey, RecordCount: batch.RecordCount(), Payload: payload,
				Workflow: workflow,
				State:    SyncStageState{Phase: SyncStageCommitting, RetryBudget: defaultSyncCommitRetries, FetchedAt: batch.FetchedAt},
			})
			if err != nil {
				t.Fatal(err)
			}
			batch.CommitReceipt = syncStageCommitReceipt(stage)
			if _, err := svc.CommitIssueSyncBatch(ctx, batch, nil); err != nil {
				t.Fatal(err)
			}
			if tc.terminalSidecar {
				state := stage.State
				state.Phase, state.CommittedAt = SyncStageCommitted, time.Now().UTC()
				stage, err = journal.UpdateState(stage.StageID, state)
				if err != nil {
					t.Fatal(err)
				}
			}
			view := stage.PublicView()
			jobsPath := filepath.Join(root, "jobs.json")
			before := NewJobManager(jobsPath)
			now := time.Now().UTC()
			before.jobs[stage.JobID] = &Job{ID: stage.JobID, Type: SyncJobType, RepoID: binding.RepoID, CacheUUID: identity.UUID, RegistrationID: request.RegistrationID, Status: JobStatusRunning, CreatedAt: now, UpdatedAt: now, SyncStage: &view}
			before.nextID = 1
			if err := before.saveLocked(); err != nil {
				t.Fatal(err)
			}

			restarted := NewJobManager(jobsPath)
			if err := restarted.LoadAndMarkInterrupted(); err != nil {
				t.Fatal(err)
			}
			manager := newTestManager(t, "darwin")
			manager.RuntimeDir = root
			cfg := config.Default()
			cfg.LockPath = lockPath
			manager.EffectiveConfig = &cfg
			if err := restarted.RecoverSyncStages(ctx, manager); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(5 * time.Second)
			var recovered Job
			for time.Now().Before(deadline) {
				recovered, _ = restarted.Get(stage.JobID)
				if jobTerminalStatus(recovered.Status) {
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			wantStatus, wantErrorClass := JobStatusSucceeded, ""
			if tc.prefixError {
				wantStatus, wantErrorClass = JobStatusFailed, "provider_failure"
			}
			if recovered.Status != wantStatus || recovered.ErrorClass != wantErrorClass || recovered.SyncStage == nil || recovered.SyncStage.Collection != "wiki" || recovered.SyncStage.Phase != SyncStageCommitted {
				t.Fatalf("mixed recovery stopped after first collection: %+v", recovered)
			}
			if _, err := store.GetSourceScoped(ctx, binding.RepoID, "WIKI-HOME"); err != nil {
				t.Fatalf("remaining wiki collection was not committed: %v", err)
			}
			cleanupDeadline := time.Now().Add(5 * time.Second)
			stages, err := journal.List()
			for err == nil && len(stages) != 0 && time.Now().Before(cleanupDeadline) {
				time.Sleep(20 * time.Millisecond)
				stages, err = journal.List()
			}
			if err != nil || len(stages) != 0 {
				t.Fatalf("completed workflow retained stages=%+v err=%v", stages, err)
			}
		})
	}
}

func TestRecoveredWorkflowRetainsCheckpointUntilTerminalSnapshotIsDurable(t *testing.T) {
	root := t.TempDir()
	journal := NewSyncStageJournal(root, SyncStageLimits{})
	envelope := testSyncStageEnvelope()
	envelope.CachePath = filepath.Join(root, "cache.db")
	envelope.Workflow = &SyncStageWorkflow{
		Collections: []string{"issues"}, Current: 0, ProviderMode: "fixture",
		Outcome: &SyncStageWorkflowOutcome{RecordsListed: 2, SuccessCount: 2},
	}
	stage, err := journal.Create(envelope)
	if err != nil {
		t.Fatal(err)
	}
	state := stage.State
	state.Phase, state.CommittedAt = SyncStageCommitted, time.Now().UTC()
	stage, err = journal.UpdateState(stage.StageID, state)
	if err != nil {
		t.Fatal(err)
	}

	jobsPath := filepath.Join(root, "jobs.json")
	jobs := NewJobManager(jobsPath)
	now := time.Now().UTC()
	view := stage.PublicView()
	jobs.jobs[stage.JobID] = &Job{ID: stage.JobID, Type: SyncJobType, Status: JobStatusInterrupted, CreatedAt: now, UpdatedAt: now, SyncStage: &view}
	jobs.nextID = 1
	if err := jobs.saveLocked(); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected terminal snapshot failure")
	jobs.writeFile = func(string, []byte, os.FileMode) error { return injected }
	manager := newTestManager(t, "darwin")
	manager.RuntimeDir = root
	if err := jobs.resumeCommittedSyncWorkflow(context.Background(), manager, journal, stage); !errors.Is(err, injected) {
		t.Fatalf("terminal snapshot error=%v", err)
	}
	if _, err := journal.Load(stage.StageID); err != nil {
		t.Fatalf("checkpoint removed before terminal snapshot became durable: %v", err)
	}
	if job, _ := jobs.Get(stage.JobID); job.Status != JobStatusInterrupted {
		t.Fatalf("failed terminal persistence left unrecoverable in-memory status: %+v", job)
	}

	jobs.writeFile = durableAtomicWriteFile
	if err := jobs.resumeCommittedSyncWorkflow(context.Background(), manager, journal, stage); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.Load(stage.StageID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("checkpoint retained after terminal snapshot became durable: %v", err)
	}
	if job, _ := jobs.Get(stage.JobID); job.Status != JobStatusSucceeded {
		t.Fatalf("terminal retry did not complete job: %+v", job)
	}
}

func TestRecoveredStageRejectsMissingCacheBeforeWritableOpen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	missing := filepath.Join(root, "removed-cache.db")
	journal := NewSyncStageJournal(root, SyncStageLimits{})
	stage, err := journal.Create(SyncStageEnvelope{
		JobID: "job-000001", CacheUUID: "removed-cache-uuid", CacheSchema: cache.CurrentSchemaVersion(), CachePath: missing,
		RegistrationID: maintenanceRegistrationID("removed-cache-uuid", "owner/repo"), RepoID: "owner/repo", BindingFingerprint: "sha256:removed-binding", Collection: "issues",
		IdempotencyKey: "removed-cache-stage", RecordCount: 0, Payload: json.RawMessage(`{"version":1}`),
		State: SyncStageState{Phase: SyncStageStaged, RetryBudget: defaultSyncCommitRetries, FetchedAt: time.Now().UTC()},
	})
	if err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(t, "darwin")
	cfg := config.Default()
	manager.EffectiveConfig = &cfg
	jobs := NewJobManager("")
	_, rejected, err := jobs.commitRecoveredSyncStage(ctx, manager, journal, stage)
	if err == nil || rejected.State.Phase != SyncStageRejected {
		t.Fatalf("recovery result stage=%+v err=%v", rejected, err)
	}
	if _, statErr := os.Stat(missing); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("recovery created or mutated removed cache: %v", statErr)
	}
}

func TestSyncStageTargetRejectsReplacementSchemaBindingAndRegistration(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cachePath := filepath.Join(root, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	identity, _ := store.CacheIdentity(ctx)
	schema, _ := store.SchemaVersion(ctx)
	binding := cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}
	stage := SyncStageEnvelope{CacheUUID: identity.UUID, CacheSchema: schema, CachePath: cachePath, RepoID: "owner/repo", RegistrationID: maintenanceRegistrationID(identity.UUID, "owner/repo"), BindingFingerprint: syncRepositoryBindingFingerprint(binding)}
	if err := validateSyncStageTargetReadOnly(ctx, stage); err != nil {
		t.Fatalf("valid target rejected: %v", err)
	}

	cases := []struct {
		name string
		edit func(*SyncStageEnvelope)
		code string
	}{
		{name: "schema", edit: func(stage *SyncStageEnvelope) { stage.CacheSchema-- }, code: "cache_schema_mismatch"},
		{name: "binding", edit: func(stage *SyncStageEnvelope) { stage.RepoID = "owner/missing" }, code: "repository_binding_unavailable"},
		{name: "binding route", edit: func(stage *SyncStageEnvelope) { stage.BindingFingerprint = "sha256:old-route" }, code: "repository_binding_changed"},
		{name: "registration", edit: func(stage *SyncStageEnvelope) { stage.RegistrationID = "wrong-registration" }, code: "registration_id_mismatch"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			candidate := stage
			tc.edit(&candidate)
			err := validateSyncStageTargetReadOnly(ctx, candidate)
			coded, ok := err.(interface{ DiagnosticCode() string })
			if !ok || coded.DiagnosticCode() != tc.code {
				t.Fatalf("error=%T %v code=%q", err, err, tc.code)
			}
		})
	}
	changed := binding
	changed.Owner = "different-owner"
	if err := store.UpdateRepository(ctx, changed); err != nil {
		t.Fatal(err)
	}
	if err := validateSyncStageTargetReadOnly(ctx, stage); err == nil {
		t.Fatal("same repo_id with a changed remote route accepted the stale stage")
	} else if coded, ok := err.(interface{ DiagnosticCode() string }); !ok || coded.DiagnosticCode() != "repository_binding_changed" {
		t.Fatalf("route change error=%T %v", err, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(cachePath, cachePath+".replaced"); err != nil {
		t.Fatal(err)
	}
	replacement, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	err = validateSyncStageTargetReadOnly(ctx, stage)
	coded, ok := err.(interface{ DiagnosticCode() string })
	if !ok || coded.DiagnosticCode() != "cache_uuid_mismatch" {
		t.Fatalf("replacement error=%T %v", err, err)
	}
}

func TestDurableSyncDefaultsBoundProviderBatchBeforeStaging(t *testing.T) {
	bounded := syncBulkRequest(StartSyncJobRequest{RepoID: "owner/repo"}, nil)
	if bounded.Bounds == nil || bounded.Bounds.MaxPages != 1 || bounded.Bounds.MaxRecords != defaultSyncStageMaxRecords || bounded.Bounds.MaxBytes != durableSyncPayloadMaxBytes || bounded.PerPage != 100 {
		t.Fatalf("implicit durable bounds=%+v per_page=%d", bounded.Bounds, bounded.PerPage)
	}
	explicit := syncBulkRequest(StartSyncJobRequest{RepoID: "owner/repo", MaxPages: 3, MaxRecords: 41, PerPage: 17}, nil)
	if explicit.Bounds.MaxPages != 1 || explicit.Bounds.MaxRecords != 41 || explicit.Bounds.MaxBytes != durableSyncPayloadMaxBytes || explicit.PerPage != 17 {
		t.Fatalf("explicit durable bounds escaped stage clamp: %+v per_page=%d", explicit.Bounds, explicit.PerPage)
	}
	oversized := syncBulkRequest(StartSyncJobRequest{RepoID: "owner/repo", MaxPages: 500, MaxRecords: 50_000, PerPage: 1_000}, nil)
	if oversized.Bounds.MaxPages != 1 || oversized.Bounds.MaxRecords != defaultSyncStageMaxRecords || oversized.PerPage != 100 {
		t.Fatalf("oversized durable bounds escaped hard ceiling: %+v per_page=%d", oversized.Bounds, oversized.PerPage)
	}
}

func TestDurableSyncResponseLimitIsAlwaysInsideStageEnvelope(t *testing.T) {
	for _, configured := range []int64{0, -1, durableSyncResponseMaxBytes + 1, defaultSyncStageMaxBytes} {
		if got := durableSyncResponseLimit(configured); got != durableSyncResponseMaxBytes {
			t.Fatalf("configured=%d limit=%d want=%d", configured, got, durableSyncResponseMaxBytes)
		}
	}
	const smaller = int64(1 << 20)
	if got := durableSyncResponseLimit(smaller); got != smaller {
		t.Fatalf("smaller configured limit=%d want=%d", got, smaller)
	}
	if durableSyncResponseMaxBytes >= durableSyncPayloadMaxBytes || durableSyncPayloadMaxBytes >= defaultSyncStageMaxBytes {
		t.Fatalf("invalid byte ceilings response=%d payload=%d stage=%d", durableSyncResponseMaxBytes, durableSyncPayloadMaxBytes, defaultSyncStageMaxBytes)
	}
}

func TestWikiByteChunkCarriesExactOffsetIntoMaintenanceCheckpoint(t *testing.T) {
	ctx := context.Background()
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	frontier, err := stagedMaintenanceFrontier(ctx, StartSyncJobRequest{
		RepoID: "owner/repo", CachePath: cachePath, Lane: "head", Page: 1,
	}, "wiki", durableCollectionBatch{
		checkpoint: "max_records", traversalStatus: "bounded", pagesListed: 1, recordsListed: 2, nextPage: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if frontier == nil || frontier.Checkpoint != "next_page:3" || frontier.Status != "partial" {
		t.Fatalf("frontier=%+v", frontier)
	}
}

func TestSyncCommitReservationIsAtomicWithDirectWriterAdmission(t *testing.T) {
	type commitResult struct {
		reservation syncCommitReservation
		blockedOp   string
		blockedRef  string
		reserved    bool
		err         error
	}
	type directResult struct {
		release func()
		err     error
	}
	for iteration := 0; iteration < 100; iteration++ {
		jobs := NewJobManager("")
		job, created, err := jobs.createCoalescedJob(SyncJobType, "owner/repo", "", 0, fmt.Sprintf("sync-%d", iteration), "cache-shared", "reg-1", "", func() {})
		if err != nil || !created {
			t.Fatalf("iteration %d: create job=%+v created=%t err=%v", iteration, job, created, err)
		}
		stage := testSyncStageEnvelope()
		stage.JobID = job.ID
		stage.CacheUUID = "cache-shared"
		stage.StageID = fmt.Sprintf("stage-%d", iteration)
		stage.State.Phase = SyncStageCommitting

		start := make(chan struct{})
		commitDone := make(chan commitResult, 1)
		directDone := make(chan directResult, 1)
		go func() {
			<-start
			reservation, op, ref, reserved, err := jobs.tryReserveSyncCommit(job.ID, stage, "test reservation")
			commitDone <- commitResult{reservation: reservation, blockedOp: op, blockedRef: ref, reserved: reserved, err: err}
		}()
		go func() {
			<-start
			release, err := jobs.BeginDirectCacheWriter("cache-shared", fmt.Sprintf("direct-%d", iteration))
			directDone <- directResult{release: release, err: err}
		}()
		close(start)
		commit, direct := <-commitDone, <-directDone
		if commit.err != nil {
			t.Fatalf("iteration %d: reserve error: %v", iteration, commit.err)
		}
		directReserved := direct.err == nil
		if commit.reserved == directReserved {
			t.Fatalf("iteration %d: exclusive admission violated: commit=%t direct=%t commit_blocker=%s/%s direct_err=%v", iteration, commit.reserved, directReserved, commit.blockedOp, commit.blockedRef, direct.err)
		}
		if commit.reserved {
			var busy ErrCacheWriterBusy
			if !errors.As(direct.err, &busy) || busy.ActiveJobID != job.ID {
				t.Fatalf("iteration %d: direct admission blocker=%T %v", iteration, direct.err, direct.err)
			}
			jobs.rollbackSyncCommitReservation(job.ID, commit.reservation)
		} else {
			if commit.blockedOp != "direct_cache_write" || commit.blockedRef == "" {
				t.Fatalf("iteration %d: commit blocker=%s/%s", iteration, commit.blockedOp, commit.blockedRef)
			}
			direct.release()
		}
	}
}

func TestDurableRecoveryCollectionAllowlistIncludesEveryDaemonSelector(t *testing.T) {
	for _, collection := range []string{"issues", "issue_comments", "wiki", "pulls", "pr_comments"} {
		if !supportedDurableSyncCollection(collection) {
			t.Fatalf("durable recovery rejects selected collection %q", collection)
		}
	}
	for _, collection := range []string{"", "all", "comments", "unknown"} {
		if supportedDurableSyncCollection(collection) {
			t.Fatalf("durable recovery accepts ambiguous collection %q", collection)
		}
	}
}

func TestSyncCommitAdmissionIsFIFOAcrossRepositoriesSharingCache(t *testing.T) {
	ctx := context.Background()
	jobs := NewJobManager("")
	releaseA, err := jobs.acquireSyncCommitTurn(ctx, "cache-shared", "repo-a-stage")
	if err != nil {
		t.Fatal(err)
	}
	type admission struct {
		stage   string
		release func()
		err     error
	}
	admitted := make(chan admission, 2)
	request := func(stage string) {
		release, err := jobs.acquireSyncCommitTurn(ctx, "cache-shared", stage)
		admitted <- admission{stage: stage, release: release, err: err}
	}
	go request("repo-b-stage")
	waitForSyncCommitQueueLength(t, jobs, "cache-shared", 2)
	go request("repo-c-stage")
	waitForSyncCommitQueueLength(t, jobs, "cache-shared", 3)

	releaseA()
	first := <-admitted
	if first.err != nil || first.stage != "repo-b-stage" {
		t.Fatalf("first admission=%+v, want repo-b-stage", first)
	}
	select {
	case next := <-admitted:
		t.Fatalf("repo-c admitted before repo-b released: %+v", next)
	case <-time.After(20 * time.Millisecond):
	}
	first.release()
	second := <-admitted
	if second.err != nil || second.stage != "repo-c-stage" {
		t.Fatalf("second admission=%+v, want repo-c-stage", second)
	}
	second.release()
	waitForSyncCommitQueueLength(t, jobs, "cache-shared", 0)
}

func TestBlockingCacheWriterRefIsPublicAndExcludesCurrentSync(t *testing.T) {
	jobs := NewJobManager("")
	jobs.jobs["sync-current"] = &Job{ID: "sync-current", Type: SyncJobType, CacheUUID: "cache-shared", Status: JobStatusRunning}
	jobs.jobs["rag-blocker"] = &Job{ID: "rag-blocker", Type: RAGIndexJobType, CacheUUID: "cache-shared", Status: JobStatusRunning}
	jobs.inflightWorkers["sync-current"] = true
	jobs.inflightWorkers["rag-blocker"] = true
	if got := jobs.blockingCacheWriterRef("sync-current", "cache-shared"); got != "rag-blocker" {
		t.Fatalf("blocking ref=%q, want rag-blocker", got)
	}
	jobs.directCacheWriters["cache-shared"] = "admin-binding-public"
	if got := jobs.blockingCacheWriterRef("sync-current", "cache-shared"); got != "admin-binding-public" {
		t.Fatalf("direct blocking ref=%q, want admin-binding-public", got)
	}
	if got := jobs.blockingCacheWriterRef("sync-current", ""); got != "" {
		t.Fatalf("empty cache blocking ref=%q", got)
	}
}

func waitForSyncCommitQueueLength(t *testing.T, jobs *JobManager, cacheUUID string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		jobs.mu.Lock()
		got := len(jobs.syncCommitQueues[cacheUUID])
		jobs.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("sync commit queue did not reach length %d", want)
}
