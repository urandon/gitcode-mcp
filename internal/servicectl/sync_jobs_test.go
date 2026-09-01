package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/service"
)

type admissionSyncService struct {
	calls []string
	err   error
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

func TestDirectCacheWriterBlocksConflictFenceAndDaemonAdmission(t *testing.T) {
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
	jobs := NewJobManager("")
	releaseWriter, err := jobs.BeginDirectCacheWriter(identity.UUID, "admin-binding-test")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseWriter()
	releaseFence, blocked := jobs.BeginCacheMutationFence(identity.UUID)
	if len(blocked) != 1 || blocked[0] != "admin-binding-test" {
		t.Fatalf("direct writer did not block conflict fence: %v", blocked)
	}
	releaseFence()
	manager := newTestManager(t, "darwin")
	cfg := config.Default()
	manager.EffectiveConfig = &cfg
	_, err = jobs.StartSync(ctx, manager, StartSyncJobRequest{RepoID: "owner/repo", CachePath: cachePath, Issues: true})
	var busy ErrCacheWriterBusy
	if !errors.As(err, &busy) || busy.ActiveType != "direct_cache_write" {
		t.Fatalf("daemon writer crossed direct reservation: %T %v", err, err)
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
	held, err := store.AcquireWriter(ctx, cache.WriterRequest{Operation: "rag-index", RepoID: "owner/repo", LockPath: cachePath + ".lock"})
	if err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(t, "darwin")
	manager.RuntimeDir = root
	cfg := config.Default()
	manager.EffectiveConfig = &cfg
	jobs := NewJobManager(filepath.Join(root, "jobs.json"))
	job, err := jobs.StartSync(ctx, manager, StartSyncJobRequest{RepoID: "owner/repo", CachePath: cachePath, CacheUUID: identity.UUID, Issues: true, ProviderMode: "fixture", IdempotencyKey: "durable-daemon-issues", MaxPages: 1, PerPage: 100})
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
	if err != nil || len(stages) != 1 || stages[0].State.Phase != SyncStageCommitted {
		t.Fatalf("stages=%+v err=%v", stages, err)
	}
	_ = store.Close()
}

func TestDaemonRestartRecoversStagedBatchWithoutProvider(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	cachePath := filepath.Join(root, "cache.db")
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
		RegistrationID: registrationID, RepoID: "owner/repo", Collection: "issues",
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
	held, err := store.AcquireWriter(ctx, cache.WriterRequest{Operation: "rag-index", RepoID: "owner/repo", LockPath: cachePath + ".lock"})
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
