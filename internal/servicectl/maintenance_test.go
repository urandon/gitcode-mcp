package servicectl

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/service"
)

func TestMaintenanceRegistryEnrollReplayAndSanitize(t *testing.T) {
	ctx := context.Background()
	manager := newTestManager(t, "darwin")
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	jobs := NewJobManager(filepath.Join(dir, "jobs.json"))
	registryPath := filepath.Join(dir, "managed-caches.json")
	maintenance := NewMaintenanceManager(manager, jobs, registryPath)
	req := testMaintenanceEnrollRequest(cachePath, "enroll-1", MaintenancePolicy{RAGEnabled: false})
	first, err := maintenance.Enroll(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	changedConfig := testMaintenanceEnrollRequest(cachePath, "enroll-1", MaintenancePolicy{RAGEnabled: false})
	changedConfig.ConfigSnapshot.MaxRetries++
	changedConfig.ConfigHash = maintenanceHash(changedConfig.ConfigSnapshot)
	if _, err := maintenance.Enroll(ctx, changedConfig); err == nil {
		t.Fatal("idempotency key reuse with changed config succeeded")
	} else if coded, ok := err.(interface{ DiagnosticCode() string }); !ok || coded.DiagnosticCode() != "idempotency_conflict" {
		t.Fatalf("changed-config replay error=%T %v", err, err)
	}
	if _, err := maintenance.Enroll(ctx, testMaintenanceEnrollRequest(cachePath, "enroll-1", MaintenancePolicy{RAGEnabled: true})); err == nil {
		t.Fatal("idempotency key reuse with changed policy succeeded")
	} else if coded, ok := err.(interface{ DiagnosticCode() string }); !ok || coded.DiagnosticCode() != "idempotency_conflict" {
		t.Fatalf("changed-policy replay error=%T %v", err, err)
	}
	second, err := maintenance.Enroll(ctx, req)
	if err != nil || first.RegistrationID == "" || second.RegistrationID != first.RegistrationID || second.Generation != first.Generation {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
	updated, err := maintenance.Enroll(ctx, testMaintenanceEnrollRequest(cachePath, "enroll-2", MaintenancePolicy{RAGEnabled: true}))
	if err != nil || !updated.Policy.RAGEnabled || updated.Generation <= first.Generation {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	listed, err := maintenance.List(ctx)
	if err != nil || len(listed.Entries) != 1 {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	publicJSON, _ := json.Marshal(listed)
	if strings.Contains(string(publicJSON), cachePath) {
		t.Fatalf("public result leaked cache path: %s", publicJSON)
	}
	disk, err := os.ReadFile(registryPath)
	if err != nil || !strings.Contains(string(disk), cachePath) {
		t.Fatalf("registry must persist local path: %s err=%v", disk, err)
	}
	if strings.Contains(string(disk), "enroll-1") || strings.Contains(string(disk), "enroll-2") {
		t.Fatalf("registry leaked raw idempotency key: %s", disk)
	}
	info, err := os.Stat(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("registry mode=%v", info.Mode().Perm())
	}
	reloaded := NewMaintenanceManager(manager, jobs, registryPath)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	reloadedList, _ := reloaded.List(ctx)
	if len(reloadedList.Entries) != 1 || reloadedList.Entries[0].RegistrationID != first.RegistrationID {
		t.Fatalf("reloaded=%+v", reloadedList)
	}
}

func TestMaintenanceReconcileReadsGenerationWithoutStartingDisabledJobs(t *testing.T) {
	ctx := context.Background()
	manager := newTestManager(t, "darwin")
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSource(ctx, cache.Source{RepoID: "owner/repo", ID: "ISSUE-1", Kind: "issue", Path: "issues/1.md", Title: "one", ContentHash: "hash-1"}); err != nil {
		t.Fatal(err)
	}
	wantState, _ := store.GetRepoContentState(ctx, "owner/repo")
	store.Close()
	jobs := NewJobManager(filepath.Join(dir, "jobs.json"))
	maintenance := NewMaintenanceManager(manager, jobs, filepath.Join(dir, "managed-caches.json"))
	entry, err := maintenance.Enroll(ctx, testMaintenanceEnrollRequest(cachePath, "enroll-1", MaintenancePolicy{}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := maintenance.Reconcile(ctx)
	if err != nil || len(result.Entries) != 1 || len(result.JobsStarted) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	got := result.Entries[0]
	if got.RegistrationID != entry.RegistrationID || got.ContentGeneration != wantState.ContentGeneration || got.State != "ready" {
		t.Fatalf("got=%+v want generation=%d", got, wantState.ContentGeneration)
	}
}

func TestMaintenanceReconcileRegistrationDoesNotTouchOtherCaches(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	manager := newTestManager(t, "darwin")
	maintenance := NewMaintenanceManager(manager, NewJobManager(filepath.Join(dir, "jobs.json")), filepath.Join(dir, "managed-caches.json"))
	var entries []MaintenanceEntry
	for _, name := range []string{"left.db", "right.db"} {
		cachePath := filepath.Join(dir, name)
		store, err := cache.NewSQLiteStore(ctx, cachePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
			t.Fatal(err)
		}
		store.Close()
		entry, err := maintenance.Enroll(ctx, testMaintenanceEnrollRequest(cachePath, "enroll-"+name, MaintenancePolicy{}))
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	result, err := maintenance.ReconcileRegistration(ctx, entries[0].RegistrationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 || result.Entries[0].RegistrationID != entries[0].RegistrationID || len(result.JobsStarted) != 0 {
		t.Fatalf("scoped reconcile=%+v", result)
	}
	listed, _ := maintenance.List(ctx)
	for _, entry := range listed.Entries {
		if entry.RegistrationID == entries[1].RegistrationID && !entry.LastReconciledAt.IsZero() {
			t.Fatalf("unrelated registration was reconciled: %+v", entry)
		}
	}
}

func TestMaintenanceWorkKeysSeparateSameRepoAcrossCaches(t *testing.T) {
	left := ragIndexWorkKey(StartRAGIndexJobRequest{RepoID: "owner/repo", CacheUUID: "cache-left", NamespaceID: "ns", ChunkPolicy: "heading"})
	right := ragIndexWorkKey(StartRAGIndexJobRequest{RepoID: "owner/repo", CacheUUID: "cache-right", NamespaceID: "ns", ChunkPolicy: "heading"})
	if left == right {
		t.Fatalf("RAG work keys collapsed: %q", left)
	}
	if ragIndexWorkKey(StartRAGIndexJobRequest{RepoID: "owner/repo", CacheUUID: "cache-left", Profile: "old"}) == ragIndexWorkKey(StartRAGIndexJobRequest{RepoID: "owner/repo", CacheUUID: "cache-left", Profile: "current"}) {
		t.Fatal("RAG work keys collapsed distinct profiles")
	}
	if ragIndexWorkKey(StartRAGIndexJobRequest{RepoID: "owner/repo", CacheUUID: "cache-left", MaxChunks: 10}) == ragIndexWorkKey(StartRAGIndexJobRequest{RepoID: "owner/repo", CacheUUID: "cache-left", MaxChunks: 1000}) {
		t.Fatal("RAG work keys collapsed distinct repair bounds")
	}
	head := syncWorkKey(StartSyncJobRequest{RepoID: "owner/repo", CacheUUID: "cache-left", Lane: "head", Issues: true})
	tail := syncWorkKey(StartSyncJobRequest{RepoID: "owner/repo", CacheUUID: "cache-left", Lane: "tail", Issues: true})
	if head == tail {
		t.Fatalf("head and tail work keys collapsed: %q", head)
	}
}

func TestActiveCacheRepoDoesNotSerializeAnotherCache(t *testing.T) {
	jobs := NewJobManager(filepath.Join(t.TempDir(), "jobs.json"))
	_, cancel := context.WithCancel(context.Background())
	job := jobs.createJobWithMetadata(SyncJobType, "owner/repo", "", 0, cancel)
	jobs.SetWorkIdentity(job.ID, "sync:cache-left:owner/repo:head", "cache-left", "reg-left", "")
	if _, ok := jobs.ActiveCacheRepo(SyncJobType, "cache-left", "owner/repo"); !ok {
		t.Fatal("active work was not found for its cache")
	}
	if other, ok := jobs.ActiveCacheRepo(SyncJobType, "cache-right", "owner/repo"); ok {
		t.Fatalf("unrelated cache was serialized by %+v", other)
	}
}

func TestMaintenanceSyncAggregationNeverPromotesBoundedTail(t *testing.T) {
	aggregate := &service.SyncResourcesResult{}
	mergeSyncResources(aggregate, &service.SyncResourcesResult{TraversalStatus: "complete", StopReason: "end_of_collection"})
	mergeSyncResources(aggregate, &service.SyncResourcesResult{TraversalStatus: "bounded", StopReason: "max_pages"})
	if aggregate.TraversalStatus != "bounded" || aggregate.StopReason != "max_pages" {
		t.Fatalf("aggregate=%+v", aggregate)
	}
}

func TestMaintenanceTailUsesConstantWindowFromCheckpoint(t *testing.T) {
	now := time.Now().UTC()
	lane, page, maxPages := nextMaintenanceSyncLane(MaintenanceEntry{Policy: MaintenancePolicy{Issues: true, HeadIntervalSeconds: 900, TailSlicePages: 10}}, []cache.MaintenanceFrontier{
		{RemoteType: "issue", Lane: "head", Status: "fresh", UpdatedAt: now},
		{RemoteType: "issue", Lane: "tail", Status: "backfilling", PagesListed: 50, Checkpoint: "next_page:41", UpdatedAt: now},
	}, now)
	if lane != "tail" || page != 41 || maxPages != 10 {
		t.Fatalf("lane=%q page=%d max_pages=%d", lane, page, maxPages)
	}
}

func TestMaintenancePartialHeadContinuesBeforeCompletedTail(t *testing.T) {
	now := time.Now().UTC()
	lane, page, maxPages := nextMaintenanceSyncLane(MaintenanceEntry{Policy: MaintenancePolicy{Issues: true, HeadIntervalSeconds: 900, HeadMaxPages: 3, TailSlicePages: 10}}, []cache.MaintenanceFrontier{
		{RemoteType: "issue", Lane: "head", Status: "partial", Checkpoint: "next_page:3", UpdatedAt: now},
		{RemoteType: "issue", Lane: "tail", Status: "complete", UpdatedAt: now},
	}, now)
	if lane != "head" || page != 3 || maxPages != 3 {
		t.Fatalf("lane=%q page=%d max_pages=%d", lane, page, maxPages)
	}
}

func TestMaintenancePolicyDefaultsFromRepositoryScopes(t *testing.T) {
	issuesOnly := cache.RepositoryBinding{RepoID: "owner/repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}
	policy, err := normalizeMaintenancePolicy(MaintenancePolicy{SyncEnabled: true}, issuesOnly)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Issues || !policy.IssueComments || !policy.Pulls || !policy.PRComments || policy.Wiki {
		t.Fatalf("scope-derived policy=%+v", policy)
	}
	if _, err := normalizeMaintenancePolicy(MaintenancePolicy{SyncEnabled: true, Wiki: true}, issuesOnly); err == nil {
		t.Fatal("explicit wiki selection without wiki scope returned nil error")
	}
}

func TestMaintenanceFailureDoesNotExposeCachePath(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "private-cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(filepath.Join(dir, "jobs.json")), filepath.Join(dir, "managed-caches.json"))
	if _, err := maintenance.Enroll(ctx, testMaintenanceEnrollRequest(cachePath, "private-path-test", MaintenancePolicy{})); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cachePath); err != nil {
		t.Fatal(err)
	}
	result, err := maintenance.Reconcile(ctx)
	if err != nil || len(result.Entries) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), cachePath) || strings.Contains(result.Entries[0].LastError, dir) {
		t.Fatalf("maintenance result leaked private path: %s", data)
	}
	if result.Entries[0].LastError != "managed cache is unavailable" {
		t.Fatalf("last_error=%q", result.Entries[0].LastError)
	}
}

func TestCreateCoalescedJobIsAtomic(t *testing.T) {
	jobs := NewJobManager(filepath.Join(t.TempDir(), "jobs.json"))
	const callers = 32
	var wg sync.WaitGroup
	var mu sync.Mutex
	created := 0
	ids := map[string]int{}
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, cancel := context.WithCancel(context.Background())
			job, wasCreated := jobs.createCoalescedJob(SyncJobType, "owner/repo", "", 0, "same-work", "cache-1", "reg-1", "", cancel)
			if !wasCreated {
				cancel()
			}
			mu.Lock()
			defer mu.Unlock()
			ids[job.ID]++
			if wasCreated {
				created++
			}
		}()
	}
	wg.Wait()
	if created != 1 || len(ids) != 1 {
		t.Fatalf("created=%d ids=%v", created, ids)
	}
}

func TestMaintenanceFrontiersPreservePerCollectionResults(t *testing.T) {
	ctx := context.Background()
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertMaintenanceFrontier(ctx, cache.MaintenanceFrontier{RepoID: "owner/repo", RemoteType: "wiki", Ordering: "updated_at_desc", FilterKey: "all", Lane: "tail", Status: "backfilling", Checkpoint: "next_page:21", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	req := StartSyncJobRequest{RepoID: "owner/repo", CachePath: cachePath, Lane: "tail", Page: 1, MaxPages: 10}
	err = recordMaintenanceSyncFrontiers(ctx, req, []syncCollectionResult{
		{RemoteType: "issue", Result: &service.SyncResourcesResult{TraversalStatus: "complete", StopReason: "end_of_collection", PagesListed: 1, RecordsListed: 5}},
		{RemoteType: "wiki", Result: &service.SyncResourcesResult{TraversalStatus: "bounded", StopReason: "max_pages", PagesListed: 10, RecordsListed: 100}},
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err = cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	frontiers, err := store.ListMaintenanceFrontiers(ctx, "owner/repo")
	if err != nil || len(frontiers) != 2 {
		t.Fatalf("frontiers=%+v err=%v", frontiers, err)
	}
	byType := map[string]cache.MaintenanceFrontier{}
	for _, frontier := range frontiers {
		byType[frontier.RemoteType] = frontier
	}
	if byType["issue"].Status != "complete" || byType["issue"].PagesListed != 1 {
		t.Fatalf("issue frontier=%+v", byType["issue"])
	}
	if byType["wiki"].Status != "backfilling" || byType["wiki"].PagesListed != 10 {
		t.Fatalf("wiki frontier=%+v", byType["wiki"])
	}
	if byType["wiki"].Checkpoint != "next_page:21" {
		t.Fatalf("wiki checkpoint regressed: %+v", byType["wiki"])
	}
}

func TestMaintenanceStageInterleavesRAGBeforeTail(t *testing.T) {
	policy := MaintenancePolicy{SyncEnabled: true, RAGEnabled: true}
	if got := nextMaintenanceStage(policy, "head", true, true, true); got != SyncJobType {
		t.Fatalf("head stage=%q", got)
	}
	if got := nextMaintenanceStage(policy, "tail", true, true, true); got != RAGIndexJobType {
		t.Fatalf("stale RAG stage=%q", got)
	}
	if got := nextMaintenanceStage(policy, "tail", false, true, true); got != SyncJobType {
		t.Fatalf("tail stage=%q", got)
	}
	if got := nextMaintenanceStage(policy, "tail", true, true, false); got != SyncJobType {
		t.Fatalf("RAG backoff blocked healthy tail stage: %q", got)
	}
	if got := nextMaintenanceStage(policy, "head", true, false, true); got != RAGIndexJobType {
		t.Fatalf("sync backoff blocked healthy RAG stage: %q", got)
	}
}

func TestMaintenanceReconcileDoesNotOverlapActiveRAGAndSync(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	jobs := NewJobManager(filepath.Join(dir, "jobs.json"))
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), jobs, filepath.Join(dir, "managed-caches.json"))
	entry, err := maintenance.Enroll(ctx, testMaintenanceEnrollRequest(cachePath, "cross-type-arbitration", MaintenancePolicy{SyncEnabled: true}))
	if err != nil {
		t.Fatal(err)
	}
	_, cancel := context.WithCancel(ctx)
	defer cancel()
	active, created := jobs.createCoalescedJob(RAGIndexJobType, entry.RepoID, "fake-rag", 0, "rag-index:"+entry.CacheUUID+":"+entry.RepoID, entry.CacheUUID, entry.RegistrationID, "", cancel)
	if !created {
		t.Fatal("active RAG test job was not created")
	}
	result, err := maintenance.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.JobsStarted) != 0 || len(result.Entries) != 1 || len(result.Entries[0].ActiveJobs) != 1 || result.Entries[0].ActiveJobs[0] != active.ID {
		t.Fatalf("result=%+v active=%+v", result, active)
	}
}

func TestMaintenanceLaneTracksEveryCollectionFrontier(t *testing.T) {
	now := time.Now().UTC()
	entry := MaintenanceEntry{Policy: MaintenancePolicy{Issues: true, Wiki: true, HeadIntervalSeconds: 900, HeadMaxPages: 3, TailSlicePages: 10}}
	frontiers := []cache.MaintenanceFrontier{
		{RemoteType: "issue", Lane: "head", Status: "fresh", UpdatedAt: now},
		{RemoteType: "issue", Lane: "tail", Status: "complete", UpdatedAt: now},
		{RemoteType: "wiki", Lane: "tail", Status: "backfilling", Checkpoint: "next_page:21", UpdatedAt: now},
	}
	if lane, page, pages := nextMaintenanceSyncLane(entry, frontiers, now); lane != "head" || page != 1 || pages != 3 {
		t.Fatalf("missing wiki head lane=%q page=%d pages=%d", lane, page, pages)
	}
	frontiers = append(frontiers, cache.MaintenanceFrontier{RemoteType: "wiki", Lane: "head", Status: "fresh", UpdatedAt: now})
	if lane, page, pages := nextMaintenanceSyncLane(entry, frontiers, now); lane != "tail" || page != 21 || pages != 10 {
		t.Fatalf("wiki tail lane=%q page=%d pages=%d", lane, page, pages)
	}
}

func TestMaintenanceCheckpointAdvancesWithOnePageOverlap(t *testing.T) {
	req := StartSyncJobRequest{Lane: "tail", Page: 21, MaxPages: 10}
	collection := syncCollectionResult{Result: &service.SyncResourcesResult{TraversalStatus: "bounded", PagesListed: 10}}
	if got := nextMaintenanceCheckpoint(req, collection); got != "next_page:30" {
		t.Fatalf("checkpoint=%q", got)
	}
	collection.Result.TraversalStatus = "complete"
	if got := nextMaintenanceCheckpoint(req, collection); got != "" {
		t.Fatalf("complete checkpoint=%q", got)
	}
}

func TestMaintenanceHeadCheckpointAdvancesUntilTraversalCompletes(t *testing.T) {
	req := StartSyncJobRequest{Lane: "head", Page: 1, MaxPages: 3}
	collection := syncCollectionResult{Result: &service.SyncResourcesResult{TraversalStatus: "bounded", PagesListed: 3}}
	if got := nextMaintenanceCheckpoint(req, collection); got != "next_page:3" {
		t.Fatalf("checkpoint=%q", got)
	}
	req.Page = 3
	collection.Result.TraversalStatus = "complete"
	if got := nextMaintenanceCheckpoint(req, collection); got != "" {
		t.Fatalf("complete checkpoint=%q", got)
	}
}

func TestMaintenanceCompletedHeadPublishesAggregatedHighWatermark(t *testing.T) {
	ctx := context.Background()
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	if err := store.UpsertMaintenanceFrontier(ctx, cache.MaintenanceFrontier{RepoID: "owner/repo", RemoteType: "issue", Ordering: "updated_at_desc", FilterKey: "all", Lane: "head", Status: "partial", HighUpdatedAt: base.Add(time.Hour), HighRemoteID: "issue:2", Checkpoint: "next_page:3", UpdatedAt: base}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	result := &service.SyncResourcesResult{TraversalStatus: "complete", StopReason: "watermark", PagesListed: 1, Results: []service.SyncResult{{Record: service.SourceSummary{ID: "ISSUE-3", RemoteAlias: "issue:3", UpdatedAt: base.Add(2 * time.Hour)}}}}
	if err := recordMaintenanceSyncFrontiers(ctx, StartSyncJobRequest{RepoID: "owner/repo", CachePath: cachePath, Lane: "head", Page: 3, MaxPages: 3}, []syncCollectionResult{{RemoteType: "issue", Result: result}}); err != nil {
		t.Fatal(err)
	}
	store, err = cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	frontier, ok, err := store.GetSyncFrontier(ctx, "owner/repo", "issue", "updated_at_desc", "state=all")
	if err != nil || !ok || frontier.Status != "complete" || !frontier.HighUpdatedAt.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("frontier=%+v ok=%t err=%v", frontier, ok, err)
	}
}

func TestMaintenanceHeadBoundedTraversalIsNotFresh(t *testing.T) {
	ctx := context.Background()
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	err = recordMaintenanceSyncFrontiers(ctx, StartSyncJobRequest{RepoID: "owner/repo", CachePath: cachePath, Lane: "head", MaxPages: 3}, []syncCollectionResult{{RemoteType: "issue", Result: &service.SyncResourcesResult{TraversalStatus: "bounded", StopReason: "max_pages", PagesListed: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	store, err = cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	frontiers, err := store.ListMaintenanceFrontiers(ctx, "owner/repo")
	if err != nil || len(frontiers) != 1 || frontiers[0].Status != "partial" || frontiers[0].Checkpoint != "next_page:3" {
		t.Fatalf("frontiers=%+v err=%v", frontiers, err)
	}
}

func TestMaintenanceStageFailureBackoffPersistsUntilSuccess(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	failedAt := now.Add(-time.Second)
	failed := Job{ID: "job-000001", Type: RAGIndexJobType, Status: JobStatusFailed, ErrorClass: "provider_unavailable", UpdatedAt: failedAt, FinishedAt: &failedAt}
	state := observeMaintenanceStage(MaintenanceStageState{}, failed, now)
	if state.ConsecutiveFailures != 1 || !state.RetryAfter.Equal(now.Add(time.Minute)) || state.LastErrorClass != "provider_unavailable" {
		t.Fatalf("first failure state=%+v", state)
	}
	if repeated := observeMaintenanceStage(state, failed, now.Add(30*time.Second)); repeated.ConsecutiveFailures != 1 || !repeated.RetryAfter.Equal(state.RetryAfter) {
		t.Fatalf("same job counted twice: %+v", repeated)
	}
	secondAt := now.Add(time.Minute)
	second := Job{ID: "job-000002", Type: RAGIndexJobType, Status: JobStatusFailed, ErrorClass: "provider_unavailable", UpdatedAt: secondAt, FinishedAt: &secondAt}
	state = observeMaintenanceStage(state, second, secondAt)
	if state.ConsecutiveFailures != 2 || !state.RetryAfter.Equal(secondAt.Add(2*time.Minute)) {
		t.Fatalf("second failure state=%+v", state)
	}
	succeededAt := secondAt.Add(3 * time.Minute)
	succeeded := Job{ID: "job-000003", Type: RAGIndexJobType, Status: JobStatusSucceeded, NamespaceID: "namespace-new", UpdatedAt: succeededAt, FinishedAt: &succeededAt}
	state = observeMaintenanceStage(state, succeeded, succeededAt)
	if state.ConsecutiveFailures != 0 || !state.RetryAfter.IsZero() || state.LastErrorClass != "" || state.NamespaceID != "namespace-new" {
		t.Fatalf("success did not clear failure state: %+v", state)
	}
}

func TestMaintenanceJobDiagnosticsArePublicSafe(t *testing.T) {
	secret := "/Users/private/cache.db?token=secret"
	event := sanitizeMaintenanceProgress(RAGIndexJobType, service.ProgressEvent{Type: "records", Phase: "running", RecordsFailed: 1, Message: secret})
	if strings.Contains(event.Message, secret) || event.Message != "RAG maintenance failed" {
		t.Fatalf("progress message=%q", event.Message)
	}
	if got := sanitizeMaintenanceErrorClass("../../private", "rag_failed"); got != "rag_failed" {
		t.Fatalf("error class=%q", got)
	}
}

func TestSelectMaintenanceNamespaceRequiresRecordedEffectiveProfile(t *testing.T) {
	now := time.Now().UTC()
	namespaces := []cache.EmbeddingNamespace{
		{ID: "old", EmbeddingNamespaceIdentity: cache.EmbeddingNamespaceIdentity{ProfileID: "profile-old"}, UpdatedAt: now.Add(time.Minute)},
		{ID: "current", EmbeddingNamespaceIdentity: cache.EmbeddingNamespaceIdentity{ProfileID: "profile-current"}, UpdatedAt: now},
	}
	entry := MaintenanceEntry{NamespaceID: "old", Policy: MaintenancePolicy{Profile: "profile-current"}}
	if got := selectMaintenanceNamespace(entry, namespaces, "profile-current"); got != "" {
		t.Fatalf("unverified namespace=%q", got)
	}
	entry.NamespaceID = "current"
	if got := selectMaintenanceNamespace(entry, namespaces, "profile-current"); got != "current" {
		t.Fatalf("namespace=%q", got)
	}
}

func TestMaintenanceEffectiveDefaultProfileRejectsOtherNamespace(t *testing.T) {
	manager := newTestManager(t, "darwin")
	source := manager.Source.(testSource)
	source.env[config.EnvRAGProfile] = "profile-default"
	profile := maintenanceEffectiveProfile(Manager{Source: source}, filepath.Join(t.TempDir(), "cache.db"), "")
	if profile != "profile-default" {
		t.Fatalf("effective profile=%q", profile)
	}
	entry := MaintenanceEntry{NamespaceID: "manual"}
	namespaces := []cache.EmbeddingNamespace{
		{ID: "manual", EmbeddingNamespaceIdentity: cache.EmbeddingNamespaceIdentity{ProfileID: "profile-manual"}},
		{ID: "default", EmbeddingNamespaceIdentity: cache.EmbeddingNamespaceIdentity{ProfileID: "profile-default"}},
	}
	if got := selectMaintenanceNamespace(entry, namespaces, profile); got != "" {
		t.Fatalf("selected non-default namespace=%q", got)
	}
}

func TestMaintenancePeriodicallyVerifiesReadyRAGNamespace(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	if !maintenanceRAGVerificationDue("namespace", "ready", now.Add(-15*time.Minute), 15*time.Minute, now) {
		t.Fatal("ready namespace did not become due for provider/model verification")
	}
	if maintenanceRAGVerificationDue("namespace", "ready", now.Add(-14*time.Minute), 15*time.Minute, now) {
		t.Fatal("recently verified namespace became due early")
	}
}

func testMaintenanceEnrollRequest(cachePath, key string, policy MaintenancePolicy) MaintenanceEnrollRequest {
	cfg := config.Default()
	cfg.CachePath = cachePath
	return MaintenanceEnrollRequest{
		CachePath: cachePath, RepoID: "owner/repo", IdempotencyKey: key, Policy: policy,
		ConfigHash: maintenanceHash(cfg), ConfigSnapshot: cfg,
	}
}
