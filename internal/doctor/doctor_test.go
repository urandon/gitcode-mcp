package doctor

import (
	"context"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/service"
)

type fakeSource struct {
	env      map[string]string
	files    map[string][]byte
	home     string
	cfgDir   string
	cacheDir string
}

func (s fakeSource) Env(key string) string                { return s.env[key] }
func (s fakeSource) UserHomeDir() (string, error)         { return s.home, nil }
func (s fakeSource) UserConfigDir() (string, error)       { return s.cfgDir, nil }
func (s fakeSource) UserCacheDir() (string, error)        { return s.cacheDir, nil }
func (s fakeSource) ReadFile(path string) ([]byte, error) { return s.files[path], nil }

type fakeCredentialReporter struct{ status config.CredentialStatus }

func (r fakeCredentialReporter) Status(context.Context, config.EffectiveConfig) config.CredentialStatus {
	return r.status
}

type fakeStore struct {
	repos   []cache.RepositoryBinding
	counts  cache.RecordCounts
	version int
}

func (s *fakeStore) ListRepositories(context.Context) ([]cache.RepositoryBinding, error) {
	return s.repos, nil
}
func (s *fakeStore) RecordCounts(context.Context, string) (cache.RecordCounts, error) {
	return s.counts, nil
}
func (s *fakeStore) SchemaVersion(context.Context) (int, error) { return s.version, nil }
func (s *fakeStore) Close() error                               { return nil }

type fakeService struct {
	cacheStatus service.CacheStatusResult
	syncStatus  service.SyncStatusSummaryResult
	staleIndex  service.StaleIndexResult
}

func (s fakeService) CacheStatus(context.Context, service.CacheStatusRequest) (service.CacheStatusResult, error) {
	return s.cacheStatus, nil
}
func (s fakeService) SyncStatus(context.Context, service.ListSourcesRequest) (service.SyncStatusSummaryResult, error) {
	return s.syncStatus, nil
}
func (s fakeService) StaleIndex(context.Context, service.StaleIndexRequest) (service.StaleIndexResult, error) {
	return s.staleIndex, nil
}

func TestBuildFullReport(t *testing.T) {
	now := time.Date(2026, 6, 22, 10, 0, 0, 0, time.UTC)
	src := fakeSource{env: map[string]string{}, files: map[string][]byte{}, home: "/home/test", cfgDir: "/cfg", cacheDir: "/cache"}
	store := &fakeStore{
		repos:   []cache.RepositoryBinding{{RepoID: "fixture-a", Owner: "fixture-owner", Name: "fixture-repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}},
		version: 7,
	}
	report, err := Build(context.Background(), Request{
		Version:            "test-version",
		Source:             src,
		CredentialReporter: fakeCredentialReporter{status: config.CredentialStatus{Source: "env", Present: true, StoreMode: "env", AvailableSources: []string{"env", "keychain"}}},
		CachePath:          "/cache/db.sqlite",
		OpenStore:          func(context.Context, string) (Store, error) { return store, nil },
		NewService: func(Store) Service {
			return fakeService{
				cacheStatus: service.CacheStatusResult{Records: 2, Chunks: 3, SyncEvents: 1},
				syncStatus:  service.SyncStatusSummaryResult{FreshCount: 2, LastSyncAt: now, LastSyncStartedAt: now.Add(-time.Minute), LastSyncCompletedAt: now, ZeroDelta: true},
				staleIndex:  service.StaleIndexResult{StaleCount: 0, LastIndexedAt: now},
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != "test-version" || report.Cache.SchemaVersion != "7" || report.Repo.Status != "ready" || report.Credential.Status != "token_configured" || report.Sync.Status != "available" || report.Index.Status != "available" || report.MCP.TransportStdio != "supported" || report.LiveProvider.ProviderMode != "fixture" {
		t.Fatalf("unexpected report: %#v", report)
	}
	var b strings.Builder
	RenderText(&b, report)
	out := b.String()
	for _, want := range []string{"version:", "config:", "cache:", "credential:", "repo:", "sync:", "index:", "mcp:", "live_provider:", "auth_probe:", "last_sync_completed_at:", "zero_delta: true"} {
		if !strings.Contains(out, want) {
			t.Fatalf("text missing %q in %q", want, out)
		}
	}
}

func TestBuildNoBinding(t *testing.T) {
	report, err := Build(context.Background(), Request{
		Version:            "test-version",
		Source:             fakeSource{env: map[string]string{}, files: map[string][]byte{}, home: "/home/test", cfgDir: "/cfg", cacheDir: "/cache"},
		CredentialReporter: fakeCredentialReporter{status: config.CredentialStatus{Source: "missing", StoreMode: "auto", AvailableSources: []string{"env", "keychain"}}},
		CachePath:          "/cache/db.sqlite",
		OpenStore:          func(context.Context, string) (Store, error) { return &fakeStore{version: 7}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Repo.Status != "no_repo_bound" || !strings.Contains(report.Repo.BindHint, "repo add") {
		t.Fatalf("missing no-binding diagnostic: %#v", report.Repo)
	}
}

func TestBuildNoToken(t *testing.T) {
	report, err := Build(context.Background(), Request{
		Version:            "test-version",
		Source:             fakeSource{env: map[string]string{}, files: map[string][]byte{}, home: "/home/test", cfgDir: "/cfg", cacheDir: "/cache"},
		CredentialReporter: fakeCredentialReporter{status: config.CredentialStatus{Source: "missing", Present: false, StoreMode: "auto", AvailableSources: []string{"env", "keychain"}, Remediation: "Set GITCODE_TOKEN or configure a credential store."}},
		CachePath:          "/cache/db.sqlite",
		OpenStore:          func(context.Context, string) (Store, error) { return &fakeStore{version: 7}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Credential.Status != "no_token_configured" || len(report.Credential.AvailableSources) != 2 {
		t.Fatalf("missing no-token diagnostic: %#v", report.Credential)
	}
}

func TestBuildRedactsOutput(t *testing.T) {
	src := fakeSource{env: map[string]string{config.EnvToken: "secret-token-value", "GITCODE_E2E_OWNER": "private-owner", "GITCODE_E2E_REPO": "private-repo"}, files: map[string][]byte{}, home: "/home/test", cfgDir: "/cfg", cacheDir: "/cache"}
	report, err := Build(context.Background(), Request{
		Version:            "test-version",
		Source:             src,
		Live:               true,
		CredentialReporter: fakeCredentialReporter{status: config.CredentialStatus{Source: "env", Present: true, StoreMode: "env", Remediation: "Authorization: Bearer secret-token-value for private-owner/private-repo"}},
		CachePath:          "/cache/db.sqlite",
		OpenStore:          func(context.Context, string) (Store, error) { return &fakeStore{version: 7}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	RenderText(&b, report)
	for _, forbidden := range []string{"secret-token-value", "private-owner", "private-repo", "Bearer secret-token-value"} {
		if strings.Contains(b.String(), forbidden) || strings.Contains(report.Credential.Remediation, forbidden) {
			t.Fatalf("doctor leaked %q: %#v text=%q", forbidden, report, b.String())
		}
	}
	if !strings.Contains(b.String(), "[REDACTED]") {
		t.Fatalf("doctor output missing redaction marker: %q", b.String())
	}
}
