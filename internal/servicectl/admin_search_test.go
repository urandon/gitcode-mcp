package servicectl

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"gitcode-mcp/internal/adminhttp"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
)

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
