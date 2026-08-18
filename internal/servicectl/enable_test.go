package servicectl

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
)

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
