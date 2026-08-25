package servicectl

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"gitcode-mcp/internal/adminhttp"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/service"
)

func TestAdminBindingPlanApplyDefaultsAPIAndReplays(t *testing.T) {
	ctx := context.Background()
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(t, "darwin")
	manager.AdminCachePath = cachePath
	cfg := config.Default()
	manager.EffectiveConfig = &cfg
	receiptPath := filepath.Join(t.TempDir(), "controls.json")
	receipts := NewAdminControlReceiptManager(receiptPath)
	controls := NewAdminControlManager(manager, NewMaintenanceManager(manager, NewJobManager(""), ""), NewJobManager(""), receipts)
	req := adminhttp.BindingControlRequest{CacheRef: publicCacheRef(identity.UUID, cachePath), RepoID: "owner/repo", Scopes: []string{"issues"}}

	planned, err := controls.PlanBinding(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	plan := planned.(AdminBindingPlan)
	if plan.Action != "add" || plan.Status != "ready" || plan.Binding.APIBaseURL != cfg.GitCodeBaseURL || plan.PlanID == "" {
		t.Fatalf("plan=%+v default=%q", plan, cfg.GitCodeBaseURL)
	}
	replanned, err := controls.PlanBinding(ctx, req)
	if err != nil || replanned.(AdminBindingPlan).PlanID != plan.PlanID {
		t.Fatalf("non-deterministic plan=%+v err=%v", replanned, err)
	}
	req.PlanID, req.IdempotencyKey = plan.PlanID, "binding-apply-1"
	first, err := controls.ApplyBinding(ctx, req)
	if err != nil || first.(map[string]any)["outcome"] != "added" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	restartedReceipts := NewAdminControlReceiptManager(receiptPath)
	if err := restartedReceipts.Load(); err != nil {
		t.Fatal(err)
	}
	restarted := NewAdminControlManager(manager, NewMaintenanceManager(manager, NewJobManager(""), ""), NewJobManager(""), restartedReceipts)
	replay, err := restarted.ApplyBinding(ctx, req)
	if err != nil || replay.(map[string]any)["replayed"] != true {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	read, err := cache.NewSQLiteReadOnlyStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	binding, err := read.GetRepository(ctx, "owner/repo")
	if err != nil || binding.APIBaseURL != cfg.GitCodeBaseURL {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
}

func TestAdminBindingRejectsStalePlanAliasConflictAndPrivatePath(t *testing.T) {
	ctx := context.Background()
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "other/repo", Owner: "other", Name: "repo", APIBaseURL: "https://api.gitcode.com/api/v5", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}, Aliases: []string{"legacy/repo"}}); err != nil {
		t.Fatal(err)
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(t, "darwin")
	manager.AdminCachePath = cachePath
	cfg := config.Default()
	manager.EffectiveConfig = &cfg
	controls := NewAdminControlManager(manager, NewMaintenanceManager(manager, NewJobManager(""), ""), NewJobManager(""), NewAdminControlReceiptManager(""))
	cacheRef := publicCacheRef(identity.UUID, cachePath)

	blockedAny, err := controls.PlanBinding(ctx, adminhttp.BindingControlRequest{CacheRef: cacheRef, RepoID: "owner/repo", Scopes: []string{"issues"}, Aliases: []string{"legacy/repo"}})
	if err != nil || blockedAny.(AdminBindingPlan).Status != "blocked" {
		t.Fatalf("alias conflict plan=%+v err=%v", blockedAny, err)
	}
	_, err = controls.PlanBinding(ctx, adminhttp.BindingControlRequest{CacheRef: cachePath, RepoID: "owner/repo", Scopes: []string{"issues"}})
	var privatePath adminhttp.ControlError
	if !errors.As(err, &privatePath) || privatePath.Status != http.StatusNotFound || privatePath.Code != "cache_not_found" {
		t.Fatalf("private path err=%T %[1]v", err)
	}

	req := adminhttp.BindingControlRequest{CacheRef: cacheRef, RepoID: "owner/repo", Scopes: []string{"issues"}}
	planned, err := controls.PlanBinding(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	plan := planned.(AdminBindingPlan)
	write, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	svc := service.New(write)
	if _, err := svc.AddRepository(ctx, service.AddRepositoryRequest{RepoID: "owner/repo", Owner: "owner", Name: "repo", APIBaseURL: cfg.GitCodeBaseURL, Scopes: []string{"issues"}, DisplayName: "external change"}); err != nil {
		t.Fatal(err)
	}
	write.Close()
	req.PlanID, req.IdempotencyKey = plan.PlanID, "stale-1"
	_, err = controls.ApplyBinding(ctx, req)
	var stale adminhttp.ControlError
	if !errors.As(err, &stale) || stale.Status != http.StatusConflict || stale.Code != "stale_plan" {
		t.Fatalf("stale err=%T %[1]v", err)
	}
}

func TestAdminControlErrorMapsBindingConflict(t *testing.T) {
	err := adminControlError(service.ErrConflict{Kind: "repository", ID: "owner/repo"})
	var typed adminhttp.ControlError
	if !errors.As(err, &typed) || typed.Status != http.StatusConflict || typed.Code != "binding_conflict" {
		t.Fatalf("err=%T %[1]v", err)
	}
}

func TestAdminMaintenanceSetupMapsBoundedPolicy(t *testing.T) {
	ctx := context.Background()
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", APIBaseURL: "https://api.gitcode.com/api/v5", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(t, "darwin")
	manager.AdminCachePath = cachePath
	cfg := config.Default()
	manager.EffectiveConfig = &cfg
	controls := NewAdminControlManager(manager, NewMaintenanceManager(manager, NewJobManager(""), ""), NewJobManager(""), NewAdminControlReceiptManager(""))
	req := adminhttp.MaintenanceControlRequest{
		CacheRef: publicCacheRef(identity.UUID, cachePath), RepoID: "owner/repo", SyncMode: "head-and-backfill",
		Collections: []string{"issues"}, RAGMode: "maintain", Profile: "local",
		HeadIntervalSeconds: 901, RAGIntervalSeconds: 1801, HeadMaxPages: 7, TailSlicePages: 11, PerPage: 83,
	}
	_, mapped, err := controls.maintenanceSetup(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if mapped.HeadIntervalSeconds != req.HeadIntervalSeconds || mapped.RAGIntervalSeconds != req.RAGIntervalSeconds || mapped.HeadMaxPages != req.HeadMaxPages || mapped.TailSlicePages != req.TailSlicePages || mapped.PerPage != req.PerPage {
		t.Fatalf("mapped bounds=%+v", mapped)
	}
	if !mapped.NoServiceInstall || !mapped.NoModelDownload || !mapped.Detach || mapped.AllowMachineChange {
		t.Fatalf("browser safety flags=%+v", mapped)
	}
}
