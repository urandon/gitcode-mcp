package servicectl

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/adminhttp"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/service"
)

func TestAdminObservationReadsCacheWithoutExposingItsPath(t *testing.T) {
	root, err := shortWorkspaceTemp(t, "admin-observation-")
	if err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(root, "private", "cache.db")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRepo(ctx, cache.RepositoryBinding{RepoID: "example/repo", Owner: "example", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	manager := newTestManager(t, "darwin")
	manager.AdminCachePath = cachePath
	jobs := NewJobManager("")
	maintenance := NewMaintenanceManager(manager, jobs, filepath.Join(root, "registry.json"))
	snapshot, err := manager.adminObservation(ctx, jobs, maintenance, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), root) || strings.Contains(string(data), "cache.db") {
		t.Fatalf("absolute cache path leaked: %s", data)
	}
	if len(snapshot.Caches) != 1 || len(snapshot.Caches[0].Repositories) != 1 || snapshot.Caches[0].Repositories[0].RepoID != "example/repo" {
		t.Fatalf("cache topology=%+v", snapshot.Caches)
	}
	documentation := snapshot.Caches[0].Repositories[0].Documentation
	if documentation == nil || documentation.State != "not_indexed" || documentation.IndexHandoff != "" || documentation.SearchAvailable {
		t.Fatalf("repository documentation observation=%+v", documentation)
	}
	if snapshot.JobRetention.SuccessTTLSeconds != int64(config.DefaultJobSuccessTTL.Seconds()) || snapshot.JobRetention.MaxTerminalJobs != config.DefaultJobMaxTerminal {
		t.Fatalf("job retention observation=%+v", snapshot.JobRetention)
	}
}

func TestAdminObservationKeepsCurrentRAGSeparateFromLastErrorAndRetry(t *testing.T) {
	now := time.Now().UTC()
	entry := MaintenanceEntry{
		CacheUUID: "11111111-2222-3333-4444-555555555555", RepoID: "example/repo",
		Policy: MaintenancePolicy{RAGEnabled: true}, ContentGeneration: 8, CoveredGeneration: 8, RAGStatus: "ready",
		RAGStage: MaintenanceStageState{
			Status: "retry_scheduled", LastErrorClass: "cache_busy", LastError: "/private/cache.db is locked",
			RetryAfter: now.Add(time.Minute), UpdatedAt: now,
		},
	}
	coverage := coverageFromEntry(entry, adminhttp.SecondaryCount{Complete: 3, Total: 3}, 20, 20)
	execution := executionFromEntry(entry, nil)
	if coverage.RAG.State != "current" || coverage.RAG.Status != "ready" {
		t.Fatalf("current RAG truth was hidden by stage history: %+v", coverage.RAG)
	}
	if execution.Contention == nil || execution.ScheduledRetry == nil || len(execution.LastErrors) != 1 {
		t.Fatalf("execution context lost contention/retry/error: %+v", execution)
	}
	if strings.Contains(execution.LastErrors[0].Message, "/private/") {
		t.Fatalf("private path leaked in stage message: %q", execution.LastErrors[0].Message)
	}
}

func TestAdminObservationUsesOpaqueCacheReferences(t *testing.T) {
	path := "/Users/example/private/cache.db"
	ref := publicCacheRef("", path)
	if !strings.HasPrefix(ref, "cache-") || strings.Contains(ref, "Users") || strings.Contains(ref, "cache.db") {
		t.Fatalf("cache ref is not opaque: %q", ref)
	}
	if got := publicCacheRef("11111111-2222-3333-4444-555555555555", path); got != "cache-111111112222" {
		t.Fatalf("uuid cache ref=%q", got)
	}
	if got := publicCacheRef("", ""); got != "" {
		t.Fatalf("empty cache identity=%q", got)
	}
}

func TestAdminMaintenanceObservationExposesCanonicalAliasesAndSanitizedConflict(t *testing.T) {
	entry := MaintenanceEntry{
		RegistrationID: "cache-reg-canonical", CacheUUID: "11111111-2222-3333-4444-555555555555", RepoID: "owner/repo",
		Aliases: []string{"legacy/repo"}, LegacyRegistrationIDs: []string{"cache-reg-legacy"}, State: "identity_conflict",
		IdentityConflict: &MaintenanceIdentityConflict{
			CandidateRegistrationIDs: []string{"cache-reg-canonical", "cache-reg-legacy"},
			PolicyHashes:             []string{"sha256:policy-a", "sha256:policy-b"}, ConfigHashes: []string{"sha256:config"},
		},
	}
	view := adminMaintenanceObservation(entry)
	data, _ := json.Marshal(view)
	if len(view.Aliases) != 1 || view.Aliases[0] != "legacy/repo" || len(view.LegacyRegistrationIDs) != 1 || view.IdentityConflict == nil || len(view.IdentityConflict.CandidateRegistrationIDs) != 2 {
		t.Fatalf("view=%+v", view)
	}
	if strings.Contains(string(data), "cache_path") || strings.Contains(string(data), "config_snapshot") {
		t.Fatalf("private migration state leaked: %s", data)
	}
}

func TestAdminJobObservationDropsRawProgressMessagesAndEndpoints(t *testing.T) {
	job := Job{ID: "job-000001", Type: "sync", RegistrationID: "reg-1", Status: JobStatusFailed, Error: "/private/cache.db failed", ErrorClass: "cache_busy"}
	job.Progress = append(job.Progress, service.ProgressEvent{Type: "page", Endpoint: "/private/api", Message: "raw log"}, service.ProgressEvent{Type: "failed", Collection: "wiki", RecordsFailed: 1, RetryAfter: "30s"})
	view := adminJobObservation(job)
	if strings.Contains(view.FailureMessage, "/private/") || len(view.Progress) != 2 || view.FailureCollection != "wiki" || view.RetryAfter != "30s" || view.InspectCommand != "gitcode-mcp service job job-000001 --format json" || view.RemediationCommand != "gitcode-mcp service maintenance --format json" {
		t.Fatalf("sanitized job=%+v", view)
	}
}

func TestAdminRepositoryDocsJobIsCancellableButNotGenericallyRetryable(t *testing.T) {
	now := time.Now().UTC()
	view := adminJobObservation(Job{ID: "job-repo-docs", Type: RepositoryDocsIndexJobType, RegistrationID: "reg-1", Status: JobStatusRunning, CreatedAt: now, UpdatedAt: now})
	if !view.Cancellable || view.Retryable {
		t.Fatalf("view=%+v", view)
	}
}

func TestAdminRepositoryDocsObservationExposesOpaqueAuthoritiesAndRetention(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entry := &MaintenanceEntry{
		RegistrationID: "reg-docs",
		RepositoryDocsSources: []RepositoryDocsMaintenanceState{
			{SourceRegistrationID: "source-b", SourceRegistrationGeneration: 2, State: "registered", GitStoreRef: "git-store-b", WorktreeRef: "worktree-b"},
			{SourceRegistrationID: "source-a", SourceRegistrationGeneration: 4, State: "ready", GitStoreRef: "git-store-a", WorktreeRef: "worktree-a"},
		},
	}
	view := repositoryDocumentationObservation(ctx, store, "owner/repo", entry, 123456)
	if len(view.Sources) != 2 || view.Sources[0].SourceID != "source-a" || view.Sources[1].SourceID != "source-b" {
		t.Fatalf("sources=%+v", view.Sources)
	}
	if view.Retention.CommittedSetsPerIdentity != 8 || view.Retention.OverlayMaxAgeHours != 24 || view.Retention.TerminalMaxAgeHours != 168 || view.Retention.VectorByteCeiling != 123456 {
		t.Fatalf("retention=%+v", view.Retention)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if bytes := string(encoded); strings.Contains(bytes, "/Users/") || strings.Contains(bytes, "repository_path") {
		t.Fatalf("private authority leaked: %s", bytes)
	}
}

func TestAdminRepositoryDocsObservationEnablesFullTextWithoutSemanticSet(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state := RepositoryDocsMaintenanceState{
		SourceRegistrationID: "source-local", SourceRegistrationGeneration: 3,
		State: "registered", GitStoreRef: "git-store-local", CommitOID: "0123456789abcdef0123456789abcdef01234567",
	}
	entry := &MaintenanceEntry{
		RegistrationID: "reg-docs", RepositoryDocs: &state,
		RepositoryDocsSources: []RepositoryDocsMaintenanceState{state},
	}

	view := repositoryDocumentationObservation(ctx, store, "owner/repo", entry, 123456)
	if !view.Registered || !view.SearchAvailable || view.SemanticAvailable || view.State != "not_indexed" {
		t.Fatalf("registered lexical-only observation=%+v", view)
	}
	if view.SearchHandoff == "" || view.IndexHandoff == "" {
		t.Fatalf("registered handoffs=%+v", view)
	}
}

func TestAdminCoverageNeverPromotesBoundedTailToComplete(t *testing.T) {
	now := time.Now().UTC()
	entry := MaintenanceEntry{
		Policy: MaintenancePolicy{Issues: true, Wiki: true},
		Frontiers: []cache.MaintenanceFrontier{
			{RemoteType: "issue", Lane: "tail", Status: "complete", UpdatedAt: now},
			{RemoteType: "wiki", Lane: "tail", Status: "backfilling", StopReason: "max_pages", PagesListed: 3, UpdatedAt: now},
		},
	}
	coverage := coverageFromEntry(entry, adminhttp.SecondaryCount{}, 0, 0)
	if coverage.Tail.State != "partial" || coverage.Tail.Status == "complete" || coverage.Tail.StopReason != "max_pages" {
		t.Fatalf("bounded tail was promoted: %+v", coverage.Tail)
	}
}

func TestAdminCoverageRequiresEveryConfiguredCollectionForCurrentHead(t *testing.T) {
	now := time.Now().UTC()
	entry := MaintenanceEntry{
		Policy:    MaintenancePolicy{Issues: true, Wiki: true},
		Frontiers: []cache.MaintenanceFrontier{{RemoteType: "issue", Lane: "head", Status: "fresh", UpdatedAt: now}},
	}
	coverage := coverageFromEntry(entry, adminhttp.SecondaryCount{}, 0, 0)
	if coverage.Head.State != "partial" || coverage.Head.Status != "not_observed_for_all_collections" {
		t.Fatalf("missing collection head evidence=%+v", coverage.Head)
	}
}
