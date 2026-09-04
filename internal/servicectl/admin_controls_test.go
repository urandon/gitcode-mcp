package servicectl

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"gitcode-mcp/internal/adminhttp"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/service"
)

func TestMaintenanceReconcileOutcomeReportsCoalescedWork(t *testing.T) {
	result := MaintenanceReconcileResult{JobsCoalesced: []string{"job-000001"}}
	if got := maintenanceReconcileOutcome(result); got != "coalesced" {
		t.Fatalf("outcome=%q", got)
	}
}

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

func TestAdminBindingApplySharesCacheMutationFence(t *testing.T) {
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
	_ = store.Close()
	manager := newTestManager(t, "darwin")
	manager.AdminCachePath = cachePath
	cfg := config.Default()
	manager.EffectiveConfig = &cfg
	jobs := NewJobManager("")
	maintenance := NewMaintenanceManager(manager, jobs, "")
	controls := NewAdminControlManager(manager, maintenance, jobs, NewAdminControlReceiptManager(""))
	req := adminhttp.BindingControlRequest{CacheRef: publicCacheRef(identity.UUID, cachePath), RepoID: "owner/repo", Scopes: []string{"issues"}}
	planned, err := controls.PlanBinding(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.PlanID, req.IdempotencyKey = planned.(AdminBindingPlan).PlanID, "binding-fence-1"
	releaseFence, blocked := jobs.BeginCacheMutationFence(identity.UUID)
	if len(blocked) != 0 {
		t.Fatalf("unexpected blockers=%v", blocked)
	}
	_, err = controls.ApplyBinding(ctx, req)
	var fenced adminhttp.ControlError
	if !errors.As(err, &fenced) || fenced.Code != "cache_authority_fenced" {
		t.Fatalf("binding apply crossed conflict fence: %T %v", err, err)
	}
	releaseFence()
	result, err := controls.ApplyBinding(ctx, req)
	if err != nil || result.(map[string]any)["outcome"] != "added" {
		t.Fatalf("binding after fence=%+v err=%v", result, err)
	}
}

func TestAdminControlErrorMapsBindingConflict(t *testing.T) {
	err := adminControlError(service.ErrConflict{Kind: "repository", ID: "owner/repo"})
	var typed adminhttp.ControlError
	if !errors.As(err, &typed) || typed.Status != http.StatusConflict || typed.Code != "binding_conflict" {
		t.Fatalf("err=%T %[1]v", err)
	}
}

func TestAdminControlErrorExplainsRepositoryDocsProviderBoundary(t *testing.T) {
	err := adminControlError(RepositoryDocsProviderBoundaryError{ProviderID: "remote", Boundary: "remote"})
	var typed adminhttp.ControlError
	if !errors.As(err, &typed) || typed.Status != http.StatusConflict || typed.Code != "repository_docs_provider_boundary_blocked" || !strings.Contains(typed.Remediation, "local_process or local_network") {
		t.Fatalf("err=%T %[1]v typed=%+v", err, typed)
	}
}

func TestAdminControlErrorMapsMaintenanceScopeAndUnknownFailuresTruthfully(t *testing.T) {
	for _, scope := range []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki} {
		err := adminControlError(maintenancePolicyScopeError{Scope: scope})
		var typed adminhttp.ControlError
		if !errors.As(err, &typed) || typed.Status != http.StatusBadRequest || typed.Code != "invalid_policy" || typed.Field != "collections" || len(typed.Blockers) != 1 || !strings.Contains(typed.Blockers[0], string(scope)+" repository scope") || typed.CLIHandoff != "" {
			t.Fatalf("scope=%s err=%T %[2]v typed=%+v", scope, err, typed)
		}
	}

	err := adminControlError(errors.New("planner exploded"))
	var typed adminhttp.ControlError
	if !errors.As(err, &typed) || typed.Code != "invalid_request" || typed.CLIHandoff != "" || strings.Contains(strings.ToLower(typed.Remediation), "handoff") {
		t.Fatalf("unknown err=%T %[1]v typed=%+v", err, typed)
	}
}

func TestAdminControlErrorExplainsLossyAndStaleConflictRecovery(t *testing.T) {
	for _, test := range []struct {
		code string
		want string
	}{
		{code: "conflict_details_unavailable", want: "registry backup"},
		{code: "conflict_generation_stale", want: "render a new plan"},
		{code: "conflict_candidate_identity_changed", want: "Restore the candidate cache authority"},
		{code: "cache_clone_retired", want: "retained canonical cache authority"},
	} {
		err := adminControlError(MaintenanceConflictResolutionError{code: test.code})
		var typed adminhttp.ControlError
		if !errors.As(err, &typed) || typed.Status != http.StatusConflict || typed.Code != test.code || !strings.Contains(typed.Remediation, test.want) {
			t.Fatalf("code=%s err=%T %[2]v typed=%+v", test.code, err, typed)
		}
	}
}

func TestAdminMaintenanceConflictResolutionUsesDomainAtomicReceipt(t *testing.T) {
	ctx := context.Background()
	maintenance, registryPath, canonicalID := newMaintenanceIdentityConflictFixture(t)
	listed, err := maintenance.List(ctx)
	if err != nil || len(listed.Entries) != 1 || listed.Entries[0].IdentityConflict == nil {
		t.Fatalf("conflict=%+v err=%v", listed.Entries, err)
	}
	conflict := listed.Entries[0]
	var selected MaintenanceIdentityCandidate
	for _, candidate := range conflict.IdentityConflict.Candidates {
		if candidate.RegistrationID == canonicalID {
			selected = candidate
		}
	}
	controls := NewAdminControlManager(newTestManager(t, "darwin"), maintenance, NewJobManager(""), NewAdminControlReceiptManager(filepath.Join(t.TempDir(), "generic-controls.json")))
	req := adminhttp.MaintenanceConflictResolutionRequest{RegistrationID: conflict.RegistrationID, CandidateRef: selected.CandidateRef, ExpectedGeneration: conflict.Generation}
	planned, err := controls.PlanMaintenanceConflictResolution(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	plan := planned.(MaintenanceConflictResolutionPlan)
	req.PlanID, req.IdempotencyKey = plan.PlanID, "admin-domain-atomic"
	applied, err := controls.ApplyMaintenanceConflictResolution(ctx, req)
	result, ok := applied.(MaintenanceConflictResolutionResult)
	if err != nil || !ok || result.RegistrationID != canonicalID || result.ReceiptID == "" || result.Replayed {
		t.Fatalf("applied=%T %+v err=%v", applied, applied, err)
	}
	restarted := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	replayedControls := NewAdminControlManager(newTestManager(t, "darwin"), restarted, NewJobManager(""), NewAdminControlReceiptManager(""))
	replayedAny, err := replayedControls.ApplyMaintenanceConflictResolution(ctx, req)
	replayed, ok := replayedAny.(MaintenanceConflictResolutionResult)
	if err != nil || !ok || !replayed.Replayed || replayed.ReceiptID != result.ReceiptID {
		t.Fatalf("replayed=%+v err=%v", replayed, err)
	}
}

func TestValidateAdminMaintenanceRequestReturnsFieldSpecificFailure(t *testing.T) {
	err := validateAdminMaintenanceRequest(adminhttp.MaintenanceControlRequest{RepoID: "owner/repo", SyncMode: "forever"})
	var typed adminhttp.ControlError
	if !errors.As(err, &typed) || typed.Code != "invalid_policy" || typed.Field != "sync_mode" || len(typed.Blockers) != 1 || typed.CLIHandoff != "" {
		t.Fatalf("err=%T %v typed=%+v", err, err, typed)
	}
}

func TestAdminMaintenanceObservationCollectionsRoundTripToPlan(t *testing.T) {
	view := adminMaintenancePolicyObservation(MaintenancePolicy{
		Issues: true, IssueComments: true, Wiki: true, Pulls: true, PRComments: true,
	})
	want := "issues,issue-comments,wiki,pulls,pr-comments"
	if got := strings.Join(view.Collections, ","); got != want {
		t.Fatalf("observation collections=%q want %q", got, want)
	}
	if err := validateAdminMaintenanceRequest(adminhttp.MaintenanceControlRequest{
		RepoID: "owner/repo", Collections: view.Collections,
	}); err != nil {
		t.Fatalf("observed collections do not round-trip into plan validation: %v", err)
	}

	err := validateAdminMaintenanceRequest(adminhttp.MaintenanceControlRequest{
		RepoID: "owner/repo", Collections: []string{"issue_comments"},
	})
	var typed adminhttp.ControlError
	if !errors.As(err, &typed) || typed.Code != "invalid_policy" || typed.Field != "collections" {
		t.Fatalf("underscore alias err=%T %v typed=%+v", err, err, typed)
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

func TestAdminMaintenancePlanReportsRepositoryScopeMismatch(t *testing.T) {
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
	_, err = controls.PlanMaintenance(ctx, adminhttp.MaintenanceControlRequest{
		CacheRef: publicCacheRef(identity.UUID, cachePath), RepoID: "owner/repo", SyncMode: "head", Collections: []string{"wiki"}, RAGMode: "off",
	})
	var typed adminhttp.ControlError
	if !errors.As(err, &typed) || typed.Status != http.StatusBadRequest || typed.Code != "invalid_policy" || typed.Field != "collections" || len(typed.Blockers) != 1 || !strings.Contains(typed.Blockers[0], "wiki repository scope") || typed.CLIHandoff != "" {
		t.Fatalf("err=%T %v typed=%+v", err, err, typed)
	}
}
