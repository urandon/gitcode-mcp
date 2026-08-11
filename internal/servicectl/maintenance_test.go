package servicectl

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
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
	req := MaintenanceEnrollRequest{CachePath: cachePath, RepoID: "owner/repo", IdempotencyKey: "enroll-1", Policy: MaintenancePolicy{RAGEnabled: false}}
	first, err := maintenance.Enroll(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := maintenance.Enroll(ctx, MaintenanceEnrollRequest{CachePath: cachePath, RepoID: "owner/repo", IdempotencyKey: "enroll-1", Policy: MaintenancePolicy{RAGEnabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	if first.RegistrationID == "" || second.RegistrationID != first.RegistrationID || second.Generation != first.Generation {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if second.Policy.RAGEnabled {
		t.Fatal("idempotent replay changed policy")
	}
	updated, err := maintenance.Enroll(ctx, MaintenanceEnrollRequest{CachePath: cachePath, RepoID: "owner/repo", IdempotencyKey: "enroll-2", Policy: MaintenancePolicy{RAGEnabled: true}})
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
	entry, err := maintenance.Enroll(ctx, MaintenanceEnrollRequest{CachePath: cachePath, RepoID: "owner/repo", IdempotencyKey: "enroll-1", Policy: MaintenancePolicy{}})
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

func TestMaintenanceWorkKeysSeparateSameRepoAcrossCaches(t *testing.T) {
	left := ragIndexWorkKey(StartRAGIndexJobRequest{RepoID: "owner/repo", CacheUUID: "cache-left", NamespaceID: "ns", ChunkPolicy: "heading"})
	right := ragIndexWorkKey(StartRAGIndexJobRequest{RepoID: "owner/repo", CacheUUID: "cache-right", NamespaceID: "ns", ChunkPolicy: "heading"})
	if left == right {
		t.Fatalf("RAG work keys collapsed: %q", left)
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

func TestMaintenanceTailExtendsRequestedBoundNotAggregatePageCount(t *testing.T) {
	now := time.Now().UTC()
	lane, maxPages := nextMaintenanceSyncLane(MaintenanceEntry{Policy: MaintenancePolicy{HeadIntervalSeconds: 900, TailSlicePages: 10}}, []cache.MaintenanceFrontier{
		{Lane: "head", Status: "fresh", UpdatedAt: now},
		{Lane: "tail", Status: "backfilling", PagesListed: 50, Checkpoint: "max_pages:10", UpdatedAt: now},
	}, now)
	if lane != "tail" || maxPages != 20 {
		t.Fatalf("lane=%q max_pages=%d", lane, maxPages)
	}
}
