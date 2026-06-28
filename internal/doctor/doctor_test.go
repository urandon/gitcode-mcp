package doctor

import (
	"context"
	"os"
	"path/filepath"
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
	dirs     map[string]bool
	home     string
	cfgDir   string
	cacheDir string
	cwd      string
}

func (s fakeSource) Env(key string) string          { return s.env[key] }
func (s fakeSource) UserHomeDir() (string, error)   { return s.home, nil }
func (s fakeSource) UserConfigDir() (string, error) { return s.cfgDir, nil }
func (s fakeSource) UserCacheDir() (string, error)  { return s.cacheDir, nil }
func (s fakeSource) ReadFile(path string) ([]byte, error) {
	data, ok := s.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}
func (s fakeSource) WorkingDir() (string, error) {
	if s.cwd == "" {
		return s.home, nil
	}
	return s.cwd, nil
}
func (s fakeSource) Stat(path string) (os.FileInfo, error) {
	if s.dirs[path] {
		return fakeDoctorFileInfo{name: filepath.Base(path), dir: true}, nil
	}
	if _, ok := s.files[path]; ok {
		return fakeDoctorFileInfo{name: filepath.Base(path)}, nil
	}
	return nil, os.ErrNotExist
}

type fakeDoctorFileInfo struct {
	name string
	dir  bool
}

func (f fakeDoctorFileInfo) Name() string { return f.name }
func (f fakeDoctorFileInfo) Size() int64  { return 0 }
func (f fakeDoctorFileInfo) Mode() os.FileMode {
	if f.dir {
		return os.ModeDir | 0o755
	}
	return 0o644
}
func (f fakeDoctorFileInfo) ModTime() time.Time { return time.Time{} }
func (f fakeDoctorFileInfo) IsDir() bool        { return f.dir }
func (f fakeDoctorFileInfo) Sys() any           { return nil }

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
		CredentialReporter: fakeCredentialReporter{status: config.CredentialStatus{Source: "env", Present: true, StoreMode: "env", AvailableSources: []string{"env", "keyring"}}},
		CachePath:          "/cache/db.sqlite",
		MCPToolAccess:      "write",
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
	if report.Version != "test-version" || report.Cache.SchemaVersion != "7" || report.Repo.Status != "ready" || report.Credential.Status != "token_configured" || report.Sync.Status != "available" || report.Index.Status != "available" || report.MCP.TransportStdio != "supported" || report.LiveProvider.ProviderMode != "offline-fixture" {
		t.Fatalf("unexpected report: %#v", report)
	}
	if report.MCP.ToolAccess != "write" {
		t.Fatalf("mcp tool_access=%q, want write", report.MCP.ToolAccess)
	}
	var b strings.Builder
	RenderText(&b, report)
	out := b.String()
	for _, want := range []string{"version:", "config:", "cache:", "credential:", "repo:", "sync:", "index:", "mcp:", "tool_access: write", "live_provider:", "auth_probe:", "last_sync_completed_at:", "zero_delta: true"} {
		if !strings.Contains(out, want) {
			t.Fatalf("text missing %q in %q", want, out)
		}
	}
}

func TestBuildReportsRepoLocalCacheSelection(t *testing.T) {
	root := "/workspace/repo"
	repoCfg := filepath.Join(root, ".gitcode", "gitcode-mcp.yaml")
	cachePath := filepath.Join(root, ".gitcode", "mcp", "cache.db")
	src := fakeSource{
		env:      map[string]string{},
		files:    map[string][]byte{repoCfg: []byte("cache_mode: repo-local\n")},
		dirs:     map[string]bool{filepath.Join(root, ".git"): true},
		home:     "/home/test",
		cfgDir:   "/cfg",
		cacheDir: "/state",
		cwd:      filepath.Join(root, "nested"),
	}
	report, err := Build(context.Background(), Request{
		Version:            "test-version",
		Source:             src,
		CredentialReporter: fakeCredentialReporter{status: config.CredentialStatus{Source: "missing", StoreMode: "auto"}},
		OpenStore:          func(context.Context, string) (Store, error) { return &fakeStore{version: 12}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Config.CacheMode != config.CacheModeRepoLocal || report.Config.CachePath != cachePath || report.Config.CachePathSource != "repo-local:"+repoCfg || report.Config.RepoRoot != root {
		t.Fatalf("repo-local config section=%#v", report.Config)
	}
	var b strings.Builder
	RenderText(&b, report)
	out := b.String()
	for _, want := range []string{"cache_mode: repo-local", "cache_path_source: repo-local:" + repoCfg, "repo_root: " + root} {
		if !strings.Contains(out, want) {
			t.Fatalf("text missing %q in %q", want, out)
		}
	}
}

func TestBuildNoBinding(t *testing.T) {
	report, err := Build(context.Background(), Request{
		Version:            "test-version",
		Source:             fakeSource{env: map[string]string{}, files: map[string][]byte{}, home: "/home/test", cfgDir: "/cfg", cacheDir: "/cache"},
		CredentialReporter: fakeCredentialReporter{status: config.CredentialStatus{Source: "missing", StoreMode: "auto", AvailableSources: []string{"env", "keyring"}}},
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
		CredentialReporter: fakeCredentialReporter{status: config.CredentialStatus{Source: "missing", Present: false, StoreMode: "auto", AvailableSources: []string{"env", "keyring"}, Remediation: "Set GITCODE_TOKEN or configure a credential store."}},
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

func TestLiveReadinessSelectsEffectiveBinding(t *testing.T) {
	report, err := Build(context.Background(), Request{
		Version:            "test-version",
		Source:             fakeSource{env: map[string]string{}, files: map[string][]byte{}, home: "/home/test", cfgDir: "/cfg", cacheDir: "/cache"},
		Live:               true,
		ProviderMode:       "live-http",
		CredentialReporter: fakeCredentialReporter{status: config.CredentialStatus{Source: "mock-keyring", Present: true, StoreMode: "auto"}},
		CachePath:          "/cache/db.sqlite",
		LiveBinding:        service.LiveRepositoryBinding{RepoID: "selected", APIBaseURL: "https://selected.example.test", BaseURLSource: "repository_binding.api_base_url"},
		OpenStore: func(context.Context, string) (Store, error) {
			return &fakeStore{version: 7, repos: []cache.RepositoryBinding{
				{RepoID: "alternate", APIBaseURL: "https://alternate.example.test"},
				{RepoID: "selected", APIBaseURL: "https://selected.example.test"},
			}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.LiveReadiness.ProviderMode != "live-http" || report.LiveReadiness.APIBaseURL != "https://selected.example.test" || report.LiveReadiness.APIBaseURLSource != "repository_binding.api_base_url" || report.LiveReadiness.ReadinessStatus != "ready" {
		t.Fatalf("unexpected live readiness: %#v", report.LiveReadiness)
	}
}

func TestLiveReadinessRepoSelectorSwitchesBinding(t *testing.T) {
	report, err := Build(context.Background(), Request{
		Version:            "test-version",
		Source:             fakeSource{env: map[string]string{}, files: map[string][]byte{}, home: "/home/test", cfgDir: "/cfg", cacheDir: "/cache"},
		Live:               true,
		ProviderMode:       "live-http",
		RepoID:             "alternate",
		CredentialReporter: fakeCredentialReporter{status: config.CredentialStatus{Source: "mock-keyring", Present: true, StoreMode: "auto"}},
		CachePath:          "/cache/db.sqlite",
		LiveBinding:        service.LiveRepositoryBinding{RepoID: "selected", APIBaseURL: "https://selected.example.test"},
		OpenStore: func(context.Context, string) (Store, error) {
			return &fakeStore{version: 7, repos: []cache.RepositoryBinding{
				{RepoID: "alternate", APIBaseURL: "https://alternate.example.test"},
				{RepoID: "selected", APIBaseURL: "https://selected.example.test"},
			}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.LiveReadiness.APIBaseURL != "https://alternate.example.test" || report.Repo.RepoID != "alternate" {
		t.Fatalf("unexpected selected repo: repo=%#v readiness=%#v", report.Repo, report.LiveReadiness)
	}
}

func TestLiveReadinessMissingCredentialPreservesEffectiveValues(t *testing.T) {
	report, err := Build(context.Background(), Request{
		Version:            "test-version",
		Source:             fakeSource{env: map[string]string{}, files: map[string][]byte{}, home: "/home/test", cfgDir: "/cfg", cacheDir: "/cache"},
		Live:               true,
		ProviderMode:       "live-http",
		RepoID:             "selected",
		CredentialReporter: fakeCredentialReporter{status: config.CredentialStatus{Source: "missing", Present: false, StoreMode: "auto"}},
		CachePath:          "/cache/db.sqlite",
		OpenStore: func(context.Context, string) (Store, error) {
			return &fakeStore{version: 7, repos: []cache.RepositoryBinding{{RepoID: "selected", APIBaseURL: "https://selected.example.test"}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.LiveReadiness.APIBaseURL != "https://selected.example.test" || report.LiveReadiness.ReadinessStatus != "missing_credential" || len(report.LiveReadiness.Diagnostics) != 1 || report.LiveReadiness.Diagnostics[0] != "missing_credential" {
		t.Fatalf("unexpected missing credential readiness: %#v", report.LiveReadiness)
	}
}

func TestLiveReadinessInvalidAPIBaseURLPrecedesCredential(t *testing.T) {
	report, err := Build(context.Background(), Request{
		Version:            "test-version",
		Source:             fakeSource{env: map[string]string{}, files: map[string][]byte{}, home: "/home/test", cfgDir: "/cfg", cacheDir: "/cache"},
		Live:               true,
		ProviderMode:       "live-http",
		RepoID:             "selected",
		CredentialReporter: fakeCredentialReporter{status: config.CredentialStatus{Source: "missing", Present: false, StoreMode: "auto"}},
		CachePath:          "/cache/db.sqlite",
		OpenStore: func(context.Context, string) (Store, error) {
			return &fakeStore{version: 7, repos: []cache.RepositoryBinding{{RepoID: "selected", APIBaseURL: "ftp://example.invalid/api"}}}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.LiveReadiness.ReadinessStatus != "configuration_error" || len(report.LiveReadiness.Diagnostics) != 1 || report.LiveReadiness.Diagnostics[0] != "invalid_api_base_url" {
		t.Fatalf("unexpected invalid api readiness: %#v", report.LiveReadiness)
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
