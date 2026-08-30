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

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
)

type maintenanceTestRAGRuntime struct {
	smokeErr error
}

func (maintenanceTestRAGRuntime) LookPath(string) (string, error) { return "/usr/bin/provider", nil }
func (maintenanceTestRAGRuntime) IsLive(context.Context, string, time.Duration) (bool, string) {
	return true, ""
}
func (maintenanceTestRAGRuntime) ListModels(context.Context, string, time.Duration) ([]string, error) {
	return []string{config.Default().RAG.Profiles[config.DefaultRAGProfile].Model}, nil
}
func (maintenanceTestRAGRuntime) PullModel(context.Context, string, string, time.Duration) error {
	return nil
}
func (r maintenanceTestRAGRuntime) EmbeddingSmoke(context.Context, string, string, time.Duration) error {
	return r.smokeErr
}
func (maintenanceTestRAGRuntime) Start(context.Context, config.RAGProviderConfig) (string, error) {
	return "", nil
}

func TestMaintenancePlanIsDeterministicAndPathSafe(t *testing.T) {
	ctx := context.Background()
	manager, client, cachePath, stop := runningMaintenanceSetupFixture(t)
	defer stop()
	setup := MaintenanceSetup{Manager: manager, Config: config.Default(), CachePath: cachePath, CachePathSource: "command", Client: func() (*RPCClient, error) { return client, nil }}
	req := MaintenanceSetupRequest{RepoID: "owner/repo", SyncMode: "off", RAGMode: "off"}
	first, err := setup.Plan(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := setup.Plan(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.PlanID == "" || first.PlanID != second.PlanID || first.Status != "ready" {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if first.Cache.LocationKind != "explicit" || first.Cache.UUID == "" || first.Cache.Ref == "" {
		t.Fatalf("cache plan=%+v", first.Cache)
	}
	data, _ := json.Marshal(first)
	if strings.Contains(string(data), cachePath) {
		t.Fatalf("plan leaked selected cache path: %s", data)
	}
}

func TestMaintenanceEnableRejectsStalePlanAndReplaysSafely(t *testing.T) {
	ctx := context.Background()
	manager, client, cachePath, stop := runningMaintenanceSetupFixture(t)
	defer stop()
	setup := MaintenanceSetup{Manager: manager, Config: config.Default(), CachePath: cachePath, CachePathSource: "repo-local:/workspace/.gitcode/config.yaml", Client: func() (*RPCClient, error) { return client, nil }}
	req := MaintenanceSetupRequest{RepoID: "owner/repo", SyncMode: "off", RAGMode: "off", IdempotencyKey: "enable-1", Confirmed: true}
	plan, err := setup.Plan(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	stale := req
	stale.PlanID = plan.PlanID
	stale.SyncMode = "head"
	if _, err := setup.Apply(ctx, stale); err == nil {
		t.Fatal("stale plan was accepted")
	} else if coded, ok := err.(interface{ DiagnosticCode() string }); !ok || coded.DiagnosticCode() != "stale_plan" {
		t.Fatalf("stale error=%T %v", err, err)
	}
	req.PlanID = plan.PlanID
	first, err := setup.Apply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := setup.Apply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "ready" || second.Status != "ready" || first.Registration == nil || second.Registration == nil || first.Registration.RegistrationID != second.Registration.RegistrationID {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if first.AuditReceipt == "" || first.AuditReceipt != second.AuditReceipt {
		t.Fatalf("audit receipts first=%q second=%q", first.AuditReceipt, second.AuditReceipt)
	}
	setup.Config.MaxRetries++
	changedPlan, err := setup.Plan(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if changedPlan.PlanID == plan.PlanID || changedPlan.ConfigurationHash == plan.ConfigurationHash {
		t.Fatalf("configuration change did not invalidate plan: old=%+v new=%+v", plan, changedPlan)
	}
	req.PlanID = changedPlan.PlanID
	if _, err := setup.Apply(ctx, req); err == nil {
		t.Fatal("changed configuration reused an existing idempotency key")
	} else if coded, ok := err.(interface{ DiagnosticCode() string }); !ok || coded.DiagnosticCode() != "idempotency_conflict" {
		t.Fatalf("changed-config apply error=%T %v", err, err)
	}
}

func TestMaintenanceWaitsForDaemonReadiness(t *testing.T) {
	manager := newTestManager(t, "darwin")
	src := manager.Source.(testSource)
	src.env = map[string]string{"GITCODE_MCP_SERVICE_NETWORK": "mem", "GITCODE_MCP_SERVICE_ADDRESS": "maintenance-delayed-ready-" + filepath.Base(t.TempDir())}
	manager.Source = src
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		<-time.After(150 * time.Millisecond)
		errCh <- manager.Run(ctx)
	}()
	setup := MaintenanceSetup{Manager: manager, StartupTimeout: time.Second}
	client, capabilities, err := setup.waitForMaintenanceDaemon(context.Background())
	if err != nil || client == nil || capabilities.RegistryProtocol != maintenanceRegistrySchema {
		cancel()
		t.Fatalf("readiness client=%v capabilities=%+v err=%v", client, capabilities, err)
	}
	cancel()
	if err := <-errCh; err != nil && err != context.Canceled {
		t.Fatal(err)
	}
}

func TestMaintenanceApplyMissingKeyIsTypedInvalidQuery(t *testing.T) {
	setup := MaintenanceSetup{}
	_, err := setup.Apply(context.Background(), MaintenanceSetupRequest{RepoID: "owner/repo", Confirmed: true})
	var typed MaintenanceSetupInputError
	if !errors.As(err, &typed) || typed.DiagnosticCode() != "invalid_query" || typed.Field != "idempotency_key" {
		t.Fatalf("error=%T %v", err, err)
	}
}

func TestMaintenanceReadinessFailureHasOnePublicSafeRecovery(t *testing.T) {
	setup := MaintenanceSetup{
		StartupTimeout: time.Millisecond,
		Client: func() (*RPCClient, error) {
			return nil, errors.New("dial unix /private/sentinel/control.sock: connection refused")
		},
	}
	_, _, err := setup.waitForMaintenanceDaemon(context.Background())
	var typed MaintenanceServiceReadinessError
	if !errors.As(err, &typed) || typed.DiagnosticCode() != "service_not_ready" {
		t.Fatalf("error=%T %v", err, err)
	}
	message := err.Error()
	if strings.Count(message, "gitcode-mcp service repair") != 1 || strings.Contains(message, "control.sock") || strings.Contains(message, "/private/sentinel") {
		t.Fatalf("unsafe or ambiguous readiness error: %q", message)
	}
}

func TestMaintenancePlanOldDaemonHasOnePublicSafeRecovery(t *testing.T) {
	ctx := context.Background()
	manager := newTestManager(t, "darwin")
	src := manager.Source.(testSource)
	src.env = map[string]string{"GITCODE_MCP_SERVICE_NETWORK": "mem", "GITCODE_MCP_SERVICE_ADDRESS": "maintenance-old-daemon-" + filepath.Base(t.TempDir())}
	manager.Source = src
	paths, err := manager.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := ensurePathDirs(paths); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := writeState(paths, State{PID: os.Getpid(), SocketPath: paths.SocketPath, StartedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	cachePath := filepath.Join(t.TempDir(), "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	setup := MaintenanceSetup{
		Manager:   manager,
		Config:    config.Default(),
		CachePath: cachePath,
		Client: func() (*RPCClient, error) {
			return nil, errors.New("dial unix /private/sentinel/control.sock: connection refused")
		},
	}
	plan, err := setup.Plan(ctx, MaintenanceSetupRequest{RepoID: "owner/repo", SyncMode: "off", RAGMode: "off"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, action := range plan.Actions {
		if action.ID != "upgrade-service" {
			continue
		}
		found = true
		if action.Handoff != "gitcode-mcp service repair" || action.Status != "blocked" {
			t.Fatalf("upgrade action=%+v", action)
		}
	}
	data, _ := json.Marshal(plan)
	message := string(data)
	if !found || strings.Count(message, "gitcode-mcp service repair") != 1 || strings.Contains(message, "control.sock") || strings.Contains(message, "/private/sentinel") {
		t.Fatalf("unsafe or ambiguous old-daemon plan: %s", data)
	}
}

func TestMaintenancePlanLeavesEmbeddingSmokePendingAndApplyReportsTypedFailure(t *testing.T) {
	ctx := context.Background()
	manager, client, cachePath, stop := runningMaintenanceSetupFixture(t)
	defer stop()
	cfg := config.Default()
	cfg.CachePath = cachePath
	setup := MaintenanceSetup{Manager: manager, Config: cfg, CachePath: cachePath, CachePathSource: "command", RAGRuntime: maintenanceTestRAGRuntime{smokeErr: errors.New("probe failed")}, Client: func() (*RPCClient, error) { return client, nil }}
	req := MaintenanceSetupRequest{RepoID: "owner/repo", SyncMode: "off", RAGMode: "maintain", IdempotencyKey: "smoke-failure", Confirmed: true}
	plan, err := setup.Plan(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	foundPending := false
	for _, action := range plan.Actions {
		if action.ID == "verify-provider-smoke" && action.Status == "required" {
			foundPending = true
		}
	}
	if !foundPending || plan.Provider.EmbeddingSmokeStatus != "skipped" {
		t.Fatalf("plan=%+v", plan)
	}
	req.PlanID = plan.PlanID
	result, err := setup.Apply(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "blocked" || result.FailureClass != "smoke_failed" || result.NextAction == "" || result.Registration != nil {
		t.Fatalf("apply result=%+v", result)
	}
}

func TestMaintenanceApplyStatusPreservesRefreshing(t *testing.T) {
	entry := MaintenanceEntry{RegistrationID: "reg-1"}
	result := MaintenanceReconcileResult{Entries: []MaintenanceEntry{{RegistrationID: "reg-1", State: "refreshing"}}}
	if got := maintenanceApplyStatus(entry, result); got != "refreshing" {
		t.Fatalf("status=%q", got)
	}
}

func TestMaintenanceLocationKindsCoverCommonCacheSelectionModes(t *testing.T) {
	cases := map[string]string{
		"default": "global",
		"yaml":    "global",
		"command": "explicit",
		"repo-local:/workspace/.gitcode/config.yaml": "repo-local",
		"env:GITCODE_MCP_CONFIG (codex)":             "codex",
		"configured mounted-volume":                  "configured",
	}
	for source, want := range cases {
		if got := maintenanceLocationKind(source); got != want {
			t.Errorf("source %q kind=%q want=%q", source, got, want)
		}
	}
}

func runningMaintenanceSetupFixture(t *testing.T) (Manager, *RPCClient, string, func()) {
	t.Helper()
	manager := newTestManager(t, "darwin")
	src := manager.Source.(testSource)
	src.env = map[string]string{"GITCODE_MCP_SERVICE_NETWORK": "mem", "GITCODE_MCP_SERVICE_ADDRESS": "maintenance-enable-" + filepath.Base(t.TempDir())}
	manager.Source = src
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	store, err := cache.NewSQLiteStore(context.Background(), cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- manager.Run(ctx) }()
	client := waitForTestClient(t, manager, errCh)
	return manager, client, cachePath, func() {
		cancel()
		if err := <-errCh; err != nil && err != context.Canceled {
			t.Errorf("service stop: %v", err)
		}
	}
}
