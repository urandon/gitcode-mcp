package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/adminhttp"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/repositorydocs"
)

func TestAdminRepositoryDocsSearchUsesPrivateRegistrationAuthority(t *testing.T) {
	ctx := context.Background()
	cachePath, _ := seedAdminSearchCache(t, ctx)
	repositoryPath := filepath.Join(t.TempDir(), "worktree")
	if err := os.MkdirAll(filepath.Join(repositoryPath, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repositoryPath, "docs", "architecture.md"), []byte("# Durable documents\n\nThe daemon resolves Git authority from a private registration.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitForRepositoryDocsJobTest(t, repositoryPath, "init")
	runGitForRepositoryDocsJobTest(t, repositoryPath, "config", "user.email", "test@example.com")
	runGitForRepositoryDocsJobTest(t, repositoryPath, "config", "user.name", "Test")
	runGitForRepositoryDocsJobTest(t, repositoryPath, "add", "docs/architecture.md")
	runGitForRepositoryDocsJobTest(t, repositoryPath, "commit", "-m", "docs")

	manager := newTestManager(t, "darwin")
	jobs := NewJobManager("")
	maintenance := NewMaintenanceManager(manager, jobs, filepath.Join(t.TempDir(), "managed-caches.json"))
	enroll := testMaintenanceEnrollRequest(cachePath, "admin-repo-docs", MaintenancePolicy{SyncMode: "off"})
	enroll.ConfigSnapshot = adminFakeRAGConfig()
	enroll.ConfigSnapshot.CachePath = cachePath
	enroll.ConfigHash = maintenanceHash(enroll.ConfigSnapshot)
	entry, err := maintenance.Enroll(ctx, enroll)
	if err != nil {
		t.Fatal(err)
	}
	registeredEntry, registered, err := maintenance.RegisterRepositoryDocsSource(ctx, entry.CacheUUID, entry.RepoID, repositoryPath, "")
	if err != nil || !registered {
		t.Fatalf("register=%v err=%v", registered, err)
	}
	entry = registeredEntry
	controls := NewAdminControlManager(manager, maintenance, jobs, NewAdminControlReceiptManager(""))
	planned, err := controls.PlanRepositoryDocs(ctx, adminhttp.RepositoryDocsPlanRequest{RegistrationID: entry.RegistrationID, Revision: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	plan := planned.(repositorydocs.PlanResult)
	if plan.RepoID != "owner/repo" || plan.EligibleFiles != 1 || plan.CommitOID == "" {
		t.Fatalf("plan=%+v", plan)
	}
	searched, err := controls.SearchRepositoryDocs(ctx, adminhttp.RepositoryDocsSearchRequest{
		RegistrationID: entry.RegistrationID, Query: "private registration", Revision: "HEAD", Mode: "fulltext", Limit: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := searched.(repositorydocs.SearchResult)
	if result.RepoID != "owner/repo" || result.EffectiveMode != "fulltext" || len(result.Hits) != 1 || result.Hits[0].Citation.Path != "docs/architecture.md" {
		t.Fatalf("result=%+v", result)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), repositoryPath) || strings.Contains(string(encoded), cachePath) {
		t.Fatalf("public result leaked private authority: %s", encoded)
	}
	indexRequest := adminhttp.RegistrationControlRequest{RegistrationID: entry.RegistrationID, IdempotencyKey: "admin-repo-docs-index-1"}
	indexed, err := controls.IndexRepositoryDocs(ctx, indexRequest)
	if err != nil {
		t.Fatal(err)
	}
	receipt := indexed.(map[string]any)
	jobID, _ := receipt["job_id"].(string)
	if jobID == "" || receipt["outcome"] != "accepted" {
		t.Fatalf("receipt=%+v", receipt)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		job, ok := jobs.Get(jobID)
		if ok && jobTerminalStatus(job.Status) {
			if job.Type != RepositoryDocsIndexJobType || job.Status != JobStatusSucceeded {
				t.Fatalf("job=%+v", job)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("repository docs job %s did not finish", jobID)
		}
		time.Sleep(5 * time.Millisecond)
	}
	for _, job := range jobs.List() {
		if job.Type == SyncJobType || job.Type == RAGIndexJobType {
			t.Fatalf("repository docs control started unrelated work: %+v", job)
		}
	}
	manager.AdminCachePath = cachePath
	snapshot, err := manager.adminObservation(ctx, jobs, maintenance, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	docs := snapshot.Caches[0].Repositories[0].Documentation
	if docs == nil || !docs.SearchAvailable || !docs.SemanticAvailable || docs.SourceID != entry.RepositoryDocs.SourceRegistrationID || docs.SourceGeneration != entry.RepositoryDocs.SourceRegistrationGeneration || !strings.Contains(docs.IndexHandoff, "--source-registration-generation") {
		t.Fatalf("documentation observation=%+v entries=%+v cache=%q", docs, maintenance.adminEntries(), cachePath)
	}
	public, err := json.Marshal(docs)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(public), repositoryPath) || strings.Contains(string(public), cachePath) {
		t.Fatalf("documentation observation leaked private authority: %s", public)
	}
	replayed, err := controls.IndexRepositoryDocs(ctx, indexRequest)
	if err != nil || replayed.(map[string]any)["replayed"] != true || replayed.(map[string]any)["job_id"] != jobID {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
}

func TestRPCRepositoryDocsRegistrationAllowsFullTextWithoutEmbeddingProvider(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Aliases: []string{"legacy/repo"}}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	repositoryPath := createRepositoryDocsRegistrationRepo(t, filepath.Join(dir, "repo"), "full text works without embeddings")
	cfg := config.Default()
	cfg.CachePath = cachePath
	cfg.LockPath = cachePath + ".lock"
	cfg.RAG.DefaultProfile = ""
	cfg.RAG.Indexing.Profile = ""
	cfg.RAG.Profiles = nil
	cfg.RAG.Providers = nil
	manager := Manager{EffectiveConfig: &cfg}
	jobs := NewJobManager("")
	maintenance := NewMaintenanceManager(manager, jobs, filepath.Join(dir, "managed-caches.json"))
	enroll := testMaintenanceEnrollRequest(cachePath, "fulltext-no-provider", MaintenancePolicy{SyncMode: "off"})
	enroll.ConfigSnapshot = cfg
	enroll.ConfigHash = maintenanceHash(cfg)
	entry, err := maintenance.Enroll(ctx, enroll)
	if err != nil {
		t.Fatal(err)
	}
	server := RPCServer{Manager: manager, Jobs: jobs, Maintenance: maintenance}
	registered, err := server.registerRepositoryDocsSource(ctx, RegisterRepositoryDocsSourceRequest{RepoID: "owner/repo", RepositoryPath: repositoryPath, CachePath: cachePath})
	if err != nil || registered.RepositoryDocs == nil {
		t.Fatalf("registered=%+v err=%v", registered, err)
	}
	result, err := server.repositoryDocsSearch(ctx, RepositoryDocsQueryRequest{RepoID: "legacy/repo", CachePath: cachePath, Query: "without embeddings", Mode: repositorydocs.SearchModeFullText})
	if err != nil {
		t.Fatal(err)
	}
	if result.RepoID != "owner/repo" || result.EffectiveMode != repositorydocs.SearchModeFullText || len(result.Hits) != 1 {
		t.Fatalf("fulltext result=%+v", result)
	}

	repositoryPathB := createRepositoryDocsRegistrationRepo(t, filepath.Join(dir, "repo-b"), "second authority")
	second, ok, err := maintenance.RegisterRepositoryDocsSource(ctx, entry.CacheUUID, entry.RepoID, repositoryPathB, "")
	if err != nil || !ok || second.RepositoryDocs == nil {
		t.Fatalf("second registration=%+v ok=%v err=%v", second, ok, err)
	}
	_, err = server.repositoryDocsSearch(ctx, RepositoryDocsQueryRequest{RepoID: "owner/repo", CachePath: cachePath, Query: "authority", Mode: repositorydocs.SearchModeFullText})
	var ambiguous RepositoryDocsSourceAmbiguousError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("ambiguous error=%T %v", err, err)
	}
	_, err = server.repositoryDocsSearch(ctx, RepositoryDocsQueryRequest{
		RepositoryDocsSourceSelector: RepositoryDocsSourceSelector{
			RegistrationID: entry.RegistrationID, SourceRegistrationID: registered.RepositoryDocs.SourceRegistrationID,
			SourceRegistrationGeneration: registered.RepositoryDocs.SourceRegistrationGeneration + 1,
		},
		RepoID: "owner/repo", CachePath: cachePath, Query: "authority", Mode: repositorydocs.SearchModeFullText,
	})
	var stale RepositoryDocsSourceGenerationConflictError
	if !errors.As(err, &stale) {
		t.Fatalf("stale selector error=%T %v", err, err)
	}
}

func TestRPCRepositoryDocsRepoOnlyResolutionReportsMissingRegistration(t *testing.T) {
	ctx := context.Background()
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
	cfg := config.Default()
	cfg.CachePath = cachePath
	server := RPCServer{Manager: Manager{EffectiveConfig: &cfg}, Maintenance: NewMaintenanceManager(Manager{EffectiveConfig: &cfg}, NewJobManager(""), "")}
	_, err = server.repositoryDocsSourceForRequest(ctx, "owner/repo", cachePath, RepositoryDocsSourceSelector{})
	var unavailable RepositoryDocsSourceUnavailableError
	if !errors.As(err, &unavailable) || unavailable.DiagnosticCode() != "repository_docs_registration_not_found" {
		t.Fatalf("missing registration error=%T %v", err, err)
	}
}

func TestRPCRepositoryDocsRepoOnlyResolutionStaysInsideSelectedCache(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.RAG.DefaultProfile = ""
	cfg.RAG.Indexing.Profile = ""
	cfg.RAG.Profiles = nil
	cfg.RAG.Providers = nil
	manager := Manager{EffectiveConfig: &cfg}
	maintenance := NewMaintenanceManager(manager, NewJobManager(""), filepath.Join(dir, "managed-caches.json"))
	server := RPCServer{Manager: manager, Maintenance: maintenance}

	register := func(name, content string) string {
		t.Helper()
		cachePath := filepath.Join(dir, name+".db")
		store, err := cache.NewSQLiteStore(ctx, cachePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
			t.Fatal(err)
		}
		store.Close()
		localCfg := cfg
		localCfg.CachePath = cachePath
		localCfg.LockPath = cachePath + ".lock"
		enroll := testMaintenanceEnrollRequest(cachePath, "selected-cache-"+name, MaintenancePolicy{SyncMode: "off"})
		enroll.ConfigSnapshot = localCfg
		enroll.ConfigHash = maintenanceHash(localCfg)
		entry, err := maintenance.Enroll(ctx, enroll)
		if err != nil {
			t.Fatal(err)
		}
		repositoryPath := createRepositoryDocsRegistrationRepo(t, filepath.Join(dir, "repo-"+name), content)
		registered, ok, err := maintenance.RegisterRepositoryDocsSource(ctx, entry.CacheUUID, entry.RepoID, repositoryPath, "")
		if err != nil || !ok || registered.RepositoryDocs == nil {
			t.Fatalf("register %s=%+v ok=%v err=%v", name, registered, ok, err)
		}
		return cachePath
	}

	firstCache := register("first", "first-cache-only-token")
	_ = register("second", "second-cache-only-token")
	result, err := server.repositoryDocsSearch(ctx, RepositoryDocsQueryRequest{RepoID: "owner/repo", CachePath: firstCache, Query: "first-cache-only-token", Mode: repositorydocs.SearchModeFullText})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 || !strings.Contains(result.Hits[0].Snippet, "first-cache-only-token") {
		t.Fatalf("selected-cache search=%+v", result)
	}
}

func TestAdminSearchCompareIsObservationOnlyAndExplainsFallback(t *testing.T) {
	ctx := context.Background()
	cachePath, cacheRef := seedAdminSearchCache(t, ctx)
	manager := newTestManager(t, "darwin")
	manager.AdminCachePath = cachePath
	cfg := adminFakeRAGConfig()
	manager.EffectiveConfig = &cfg
	jobs := NewJobManager("")
	controls := NewAdminControlManager(manager, NewMaintenanceManager(manager, jobs, ""), jobs, NewAdminControlReceiptManager(""))

	compared, err := controls.CompareSearch(ctx, adminhttp.SearchCompareRequest{CacheRef: cacheRef, RepoID: "owner/repo", Query: "semantic lifecycle", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	result := compared.(AdminSearchComparison)
	if result.FullText.EffectiveMode != "full_text" || result.Hybrid.EffectiveMode != "full_text" || result.Hybrid.FallbackReason != "rag_namespace_missing" || len(result.FullText.Results) == 0 {
		t.Fatalf("comparison=%+v", result)
	}
	if result.FullText.Results[0].Match.LexicalScore == 0 {
		t.Fatalf("missing lexical explanation: %+v", result.FullText.Results[0])
	}
	if len(jobs.List()) != 0 {
		t.Fatalf("search started jobs: %+v", jobs.List())
	}
	store, err := cache.NewSQLiteReadOnlyStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runs, err := store.ListRAGIndexRuns(ctx, cache.RAGIndexRunFilter{RepoID: "owner/repo"})
	if err != nil || len(runs) != 0 {
		t.Fatalf("search mutated RAG runs=%+v err=%v", runs, err)
	}
}

func TestAdminProviderSmokeAndBoundedRepairPlan(t *testing.T) {
	ctx := context.Background()
	cachePath, cacheRef := seedAdminSearchCache(t, ctx)
	manager := newTestManager(t, "darwin")
	manager.AdminCachePath = cachePath
	cfg := adminFakeRAGConfig()
	manager.EffectiveConfig = &cfg
	jobs := NewJobManager("")
	controls := NewAdminControlManager(manager, NewMaintenanceManager(manager, jobs, ""), jobs, NewAdminControlReceiptManager(""))

	smoked, err := controls.SmokeProvider(ctx, adminhttp.ProviderSmokeRequest{CacheRef: cacheRef, RepoID: "owner/repo", Profile: "fake"})
	if err != nil || smoked.(AdminProviderSmoke).Status != "ready" {
		t.Fatalf("smoke=%+v err=%v", smoked, err)
	}
	planned, err := controls.PlanRAGRepair(ctx, adminhttp.RAGRepairRequest{CacheRef: cacheRef, RepoID: "owner/repo", Profile: "fake", MaxChunks: 1})
	if err != nil {
		t.Fatal(err)
	}
	plan := planned.(AdminRAGRepairPlan)
	if plan.Status != "ready" || plan.MaxChunks != 1 || plan.Coverage.EligibleChunks != 1 || plan.Coverage.MissingChunks != 1 || plan.PlanID == "" {
		t.Fatalf("plan=%+v", plan)
	}
	if plan.Effects[2].DataBoundary != "configured_embedding_provider" || !plan.Effects[2].ConfirmationRequired {
		t.Fatalf("effects=%+v", plan.Effects)
	}
	repairReq := adminhttp.RAGRepairRequest{CacheRef: cacheRef, RepoID: "owner/repo", Profile: "fake", MaxChunks: 1, PlanID: plan.PlanID, IdempotencyKey: "repair-key-1"}
	applied, err := controls.ApplyRAGRepair(ctx, repairReq)
	if err != nil {
		t.Fatal(err)
	}
	receipt := applied.(map[string]any)
	jobID, _ := receipt["job_id"].(string)
	if receipt["outcome"] != "created" || jobID == "" || receipt["max_chunks"] != 1 {
		t.Fatalf("receipt=%+v", receipt)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		job, ok := jobs.Get(jobID)
		if ok && jobTerminalStatus(job.Status) {
			if job.Status != JobStatusSucceeded || job.Completed != 1 {
				t.Fatalf("job=%+v", job)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("repair job %s did not complete", jobID)
		}
		time.Sleep(5 * time.Millisecond)
	}
	replayed, err := controls.ApplyRAGRepair(ctx, repairReq)
	if err != nil || replayed.(map[string]any)["replayed"] != true || replayed.(map[string]any)["job_id"] != jobID {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	planned, err = controls.PlanRAGRepair(ctx, adminhttp.RAGRepairRequest{CacheRef: cacheRef, RepoID: "owner/repo", Profile: "fake", MaxChunks: 1})
	if err != nil || planned.(AdminRAGRepairPlan).Status != "no_work_needed" {
		t.Fatalf("post-repair plan=%+v err=%v", planned, err)
	}
}

func seedAdminSearchCache(t *testing.T, ctx context.Context) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cache.db")
	store, err := cache.NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", APIBaseURL: "https://api.gitcode.com/api/v5", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	source := cache.Source{RepoID: "owner/repo", ID: "ISSUE-1", Kind: "issue", Path: "issues/1.md", Title: "Semantic lifecycle", Body: "daemon lifecycle and semantic search evidence", Status: "open", Provenance: cache.ProvenanceLive, ContentHash: "source-hash", UpdatedAt: time.Unix(10, 0).UTC()}
	chunk := cache.Chunk{RepoID: "owner/repo", ID: "chunk-1", SourceID: source.ID, Policy: "heading", Text: source.Body, NormalizedText: source.Body, ContentHash: "chunk-hash", LineStart: 1, LineEnd: 1}
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{Source: source, Chunks: []cache.Chunk{chunk}, ReplaceChunks: true}); err != nil {
		t.Fatal(err)
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return path, publicCacheRef(identity.UUID, path)
}

func adminFakeRAGConfig() config.Config {
	cfg := config.Default()
	cfg.RAG.DefaultProfile = "fake"
	cfg.RAG.Providers["fake"] = config.RAGProviderConfig{Type: "fake", DataBoundary: "local_process", Timeout: time.Second}
	cfg.RAG.Profiles["fake"] = config.RAGProfileConfig{Provider: "fake", Model: "fake-embed", Dimensions: 8, BatchSize: 2, MaxInputTokens: 512}
	cfg.RAG.Indexing.Profile = "fake"
	cfg.RAG.Search.Profile = "fake"
	cfg.RAG.Search.Hybrid = true
	return cfg
}
