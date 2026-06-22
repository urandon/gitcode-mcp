package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/config"
)

type testSource struct {
	env       map[string]string
	files     map[string][]byte
	homeDir   string
	configDir string
	cacheDir  string
	readErr   map[string]error
}

func newTestSource(t *testing.T) *testSource {
	t.Helper()
	root := t.TempDir()
	return &testSource{
		env:       map[string]string{},
		files:     map[string][]byte{},
		homeDir:   filepath.Join(root, "home"),
		configDir: filepath.Join(root, "config"),
		cacheDir:  filepath.Join(root, "cache"),
		readErr:   map[string]error{},
	}
}

func (s *testSource) Env(key string) string          { return s.env[key] }
func (s *testSource) UserHomeDir() (string, error)   { return s.homeDir, nil }
func (s *testSource) UserConfigDir() (string, error) { return s.configDir, nil }
func (s *testSource) UserCacheDir() (string, error)  { return s.cacheDir, nil }
func (s *testSource) ReadFile(path string) ([]byte, error) {
	if err := s.readErr[path]; err != nil {
		return nil, err
	}
	data, ok := s.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}

func TestEntrypointHelpRouting(t *testing.T) {
	t.Run("SCN-ENTRYPOINT-HELP-CLI", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"--help"}, strings.NewReader(""), &stdout, &stderr, newTestSource(t))
		if code != 0 {
			t.Fatalf("exit = %d", code)
		}
		if !strings.Contains(stdout.String(), "--mcp") {
			t.Fatalf("help missing --mcp: %q", stdout.String())
		}
		if !strings.Contains(stdout.String(), "--live") {
			t.Fatalf("help missing --live: %q", stdout.String())
		}
		if stderr.Len() != 0 {
			t.Fatalf("stderr = %q", stderr.String())
		}
	})

	t.Run("SCN-ENTRYPOINT-HELP-MCP", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := run([]string{"--mcp", "--help"}, strings.NewReader(""), &stdout, &stderr, newTestSource(t))
		if code != 0 {
			t.Fatalf("exit = %d", code)
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout = %q", stdout.String())
		}
		if !strings.Contains(stderr.String(), "stdio MCP") {
			t.Fatalf("stderr missing MCP help: %q", stderr.String())
		}
	})
}

func TestEntrypointDefaultModeDependencyHandoff(t *testing.T) {
	src := newTestSource(t)
	configPath := filepath.Join(src.homeDir, "startup.json")
	src.env[config.EnvConfigPath] = configPath
	src.files[configPath] = []byte(`{"cache_path":"/tmp/config-cache.db","default_timeout":"30s","format":"json"}`)

	old := cliRoute
	defer func() { cliRoute = old }()
	var gotArgs []string
	var gotDeps StartupDeps
	cliRoute = func(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer, deps StartupDeps) int {
		gotArgs = append([]string(nil), args...)
		gotDeps = deps
		return 0
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"--cache-path", "/tmp/override-cache.db", "--timeout", "10s", "search", "test"}, strings.NewReader(""), &stdout, &stderr, src)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if strings.Join(gotArgs, " ") != "search test" {
		t.Fatalf("args = %#v", gotArgs)
	}
	if gotDeps.Config.CachePath != "/tmp/override-cache.db" || gotDeps.Cache.CachePath != "/tmp/override-cache.db" || gotDeps.Cache.LockPath != "/tmp/override-cache.db.lock" {
		t.Fatalf("cache handoff = %#v", gotDeps)
	}
	if gotDeps.Config.DefaultTimeout != 10*time.Second || gotDeps.GitCode.DefaultTimeout != 10*time.Second {
		t.Fatalf("timeout handoff = %#v", gotDeps)
	}
	if gotDeps.Config.Format != "json" {
		t.Fatalf("format = %q", gotDeps.Config.Format)
	}
}

func TestEntrypointAuthStatusGlobalLiveRouting(t *testing.T) {
	src := newTestSource(t)
	src.env[config.EnvToken] = "sentinel-token"
	var stdout, stderr bytes.Buffer
	code := run([]string{"--live", "auth", "status"}, strings.NewReader(""), &stdout, &stderr, src)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	out := stdout.String() + stderr.String()
	for _, want := range []string{"credential_source: env:GITCODE_TOKEN", "auth_probe_status: skipped"} {
		if !strings.Contains(out, want) {
			t.Fatalf("auth status missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "sentinel-token") {
		t.Fatalf("token emitted stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestEntrypointLiveModeDependencyHandoff(t *testing.T) {
	src := newTestSource(t)
	src.env[config.EnvToken] = "sentinel-token"
	old := cliRoute
	defer func() { cliRoute = old }()
	var gotDeps StartupDeps
	cliRoute = func(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer, deps StartupDeps) int {
		gotDeps = deps
		return 0
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"--live", "--cache-path", "/tmp/live-cache.db", "sync"}, strings.NewReader(""), &stdout, &stderr, src)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if !gotDeps.GitCode.Live {
		t.Fatalf("live mode not handed off: %#v", gotDeps.GitCode)
	}
	if gotDeps.GitCode.Token != "sentinel-token" {
		t.Fatalf("token not handed off")
	}
	if strings.Contains(stdout.String()+stderr.String(), "sentinel-token") {
		t.Fatalf("token emitted stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestEntrypointLiveModeRequiresToken(t *testing.T) {
	src := newTestSource(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	src.files[configPath] = []byte("credential:\n  store: env\n")
	src.env[config.EnvMCPConfigPath] = configPath
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	addRepoForStartupTest(t, cachePath, "https://example.invalid/api")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--live", "--cache-path", cachePath, "sync", "--repo", "fixture-a"}, strings.NewReader(""), &stdout, &stderr, src)
	if code == 0 {
		t.Fatal("exit = 0")
	}
	if !strings.Contains(stderr.String(), "GITCODE_TOKEN") {
		t.Fatalf("stderr missing token diagnostic: %q", stderr.String())
	}
}

func TestEntrypointCLICompatibility(t *testing.T) {
	src := newTestSource(t)
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--cache-path", cachePath, "search", "test"}, strings.NewReader(""), &stdout, &stderr, src)
	if strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("did not reach CLI route: exit=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "repo_required") {
		t.Fatalf("unexpected CLI compatibility result: exit=%d stderr=%q", code, stderr.String())
	}
}

func TestCLIStartupPlanSelectsLiveProvider(t *testing.T) {
	t.Run("SCN-CLI-LIVE-SYNC-USES-LIVE-PROVIDER", func(t *testing.T) {
		var requests atomic.Int64
		server := newStartupLiveMockServer(t, &requests)
		defer server.Close()
		cachePath := filepath.Join(t.TempDir(), "cache.db")
		src := newTestSource(t)
		src.env[config.EnvToken] = "test-token"
		addRepoForStartupTest(t, cachePath, server.URL)

		var stdout, stderr bytes.Buffer
		code := run([]string{"sync", "--live", "--cache-path", cachePath, "--repo", "fixture-a"}, strings.NewReader(""), &stdout, &stderr, src)
		if code != 0 {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		if requests.Load() == 0 {
			t.Fatalf("mock server was not reached")
		}
		out := stdout.String() + stderr.String()
		if strings.Contains(out, "ISSUE-42") || strings.Contains(out, "WIKI-HOME") {
			t.Fatalf("fixture identifiers leaked: %q", out)
		}
	})

	t.Run("SCN-CLI-LIVE-SYNC-MISSING-CREDENTIAL", func(t *testing.T) {
		var requests atomic.Int64
		server := newStartupLiveMockServer(t, &requests)
		defer server.Close()
		cachePath := filepath.Join(t.TempDir(), "cache.db")
		src := newTestSource(t)
		configPath := filepath.Join(t.TempDir(), "config.yaml")
		src.files[configPath] = []byte("credential:\n  store: env\n")
		src.env[config.EnvMCPConfigPath] = configPath
		addRepoForStartupTest(t, cachePath, server.URL)

		var stdout, stderr bytes.Buffer
		code := run([]string{"sync", "--live", "--cache-path", cachePath, "--repo", "fixture-a"}, strings.NewReader(""), &stdout, &stderr, src)
		if code == 0 {
			t.Fatalf("code=0 stdout=%q", stdout.String())
		}
		if requests.Load() != 0 {
			t.Fatalf("mock server requests=%d, want 0", requests.Load())
		}
		if !strings.Contains(stderr.String(), "missing_credential") {
			t.Fatalf("stderr missing typed diagnostic: %q", stderr.String())
		}
	})

	t.Run("SCN-CLI-OFFLINE-SYNC-NO-HTTP", func(t *testing.T) {
		var requests atomic.Int64
		server := newStartupLiveMockServer(t, &requests)
		defer server.Close()
		cachePath := filepath.Join(t.TempDir(), "cache.db")
		src := newTestSource(t)
		configPath := filepath.Join(t.TempDir(), "config.yaml")
		src.files[configPath] = []byte("credential:\n  store: env\n")
		src.env[config.EnvMCPConfigPath] = configPath
		addRepoForStartupTest(t, cachePath, server.URL)

		var stdout, stderr bytes.Buffer
		code := run([]string{"sync", "--cache-path", cachePath, "--repo", "fixture-a"}, strings.NewReader(""), &stdout, &stderr, src)
		if code != 0 {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		if requests.Load() != 0 {
			t.Fatalf("mock server requests=%d, want 0", requests.Load())
		}
	})

	t.Run("SCN-CLI-LIVE-API-BASE-AUTHORITY", func(t *testing.T) {
		var selectedRequests, alternateRequests atomic.Int64
		selected := newStartupLiveMockServer(t, &selectedRequests)
		defer selected.Close()
		alternate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			alternateRequests.Add(1)
			http.Error(w, "wrong server", http.StatusTeapot)
		}))
		defer alternate.Close()
		cachePath := filepath.Join(t.TempDir(), "cache.db")
		src := newTestSource(t)
		src.env[config.EnvToken] = "test-token"
		src.env[config.EnvAPIURL] = alternate.URL
		addRepoForStartupTest(t, cachePath, selected.URL)

		var stdout, stderr bytes.Buffer
		code := run([]string{"sync", "--live", "--cache-path", cachePath, "--repo", "fixture-a"}, strings.NewReader(""), &stdout, &stderr, src)
		if code != 0 {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		if selectedRequests.Load() == 0 || alternateRequests.Load() != 0 {
			t.Fatalf("selected=%d alternate=%d", selectedRequests.Load(), alternateRequests.Load())
		}
	})

	t.Run("SCN-CLI-DOCTOR-LIVE-JSON-STARTUP-SNAPSHOT", func(t *testing.T) {
		var requests atomic.Int64
		server := newStartupLiveMockServer(t, &requests)
		defer server.Close()
		cachePath := filepath.Join(t.TempDir(), "cache.db")
		src := newTestSource(t)
		src.env[config.EnvToken] = "test-token"
		addRepoForStartupTest(t, cachePath, server.URL)

		var stdout, stderr bytes.Buffer
		code := run([]string{"doctor", "--live", "--format", "json", "--cache-path", cachePath, "--repo", "fixture-a"}, strings.NewReader(""), &stdout, &stderr, src)
		if code != 0 {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"\"provider_mode\": \"live-http\"", "\"source\": \"env:GITCODE_TOKEN\"", fmt.Sprintf("\"path\": \"%s\"", cachePath), fmt.Sprintf("\"api_base_url\": \"%s\"", server.URL)} {
			if !strings.Contains(out, want) {
				t.Fatalf("doctor output missing %q in %q", want, out)
			}
		}
		if strings.Contains(out, "test-token") || requests.Load() != 0 {
			t.Fatalf("doctor leaked token or contacted server; requests=%d out=%q", requests.Load(), out)
		}
	})
}

func addRepoForStartupTest(t *testing.T, cachePath, baseURL string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run([]string{"repo", "add", "--cache-path", cachePath, "--repo", "fixture-a", "--owner", "owner-a", "--name", "repo-a", "--api-base-url", baseURL, "--scopes", "issues,wiki"}, strings.NewReader(""), &stdout, &stderr, newTestSource(t))
	if code != 0 {
		t.Fatalf("repo add code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func newStartupLiveMockServer(t *testing.T, requests *atomic.Int64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			http.Error(w, "missing authorization", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/repos/owner-a/repo-a/issues":
			fmt.Fprint(w, `[{"id":"issue-7","number":7,"title":"Mock Issue","state":"open","body":"mock issue body","created_at":"2026-06-22T00:00:00Z","updated_at":"2026-06-22T00:00:00Z"}]`)
		case "/api/v5/repos/owner-a/repo-a/issues/7":
			fmt.Fprint(w, `{"id":"issue-7","number":7,"title":"Mock Issue","state":"open","body":"mock issue body","created_at":"2026-06-22T00:00:00Z","updated_at":"2026-06-22T00:00:00Z"}`)
		case "/api/v5/repos/owner-a/repo-a/issues/7/comments":
			fmt.Fprint(w, `[{"id":"comment-7","author":"mock-user","body":"mock comment","created_at":"2026-06-22T00:00:00Z","updated_at":"2026-06-22T00:00:00Z"}]`)
		case "/api/v5/repos/owner-a/repo-a/wiki":
			fmt.Fprint(w, `[{"id":"wiki-guide","slug":"Guide","title":"Mock Guide","body":"mock wiki body","revision":"rev-1","created_at":"2026-06-22T00:00:00Z","updated_at":"2026-06-22T00:00:00Z"}]`)
		case "/api/v5/repos/owner-a/repo-a/wiki/Guide":
			fmt.Fprint(w, `{"id":"wiki-guide","slug":"Guide","title":"Mock Guide","body":"mock wiki body","revision":"rev-1","created_at":"2026-06-22T00:00:00Z","updated_at":"2026-06-22T00:00:00Z"}`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestEntrypointMCPInitialize(t *testing.T) {
	src := newTestSource(t)
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--mcp", "--cache-path", cachePath, "--timeout", "10s"}, stdin, &stdout, &stderr, src)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q", stderr.String())
	}
	var resp struct {
		JSONRPC string `json:"jsonrpc"`
		Result  struct {
			ServerInfo struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		t.Fatalf("stdout is not JSON-RPC: %q err=%v", stdout.String(), err)
	}
	if resp.JSONRPC != "2.0" || resp.Result.ServerInfo.Name != "gitcode-mcp" {
		t.Fatalf("unexpected initialize response: %#v", resp)
	}
}

func TestEntrypointMCPServeRouting(t *testing.T) {
	src := newTestSource(t)
	old := mcpServeRoute
	defer func() { mcpServeRoute = old }()
	var gotTransport, gotBind string
	mcpServeRoute = func(ctx context.Context, stdin io.Reader, stdout io.Writer, stderr io.Writer, deps StartupDeps, transport string, bind string) int {
		gotTransport = transport
		gotBind = bind
		return 0
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"mcp", "serve", "--transport", "http-sse", "--bind", "127.0.0.1:9234", "--cache-path", filepath.Join(t.TempDir(), "cache.db")}, strings.NewReader(""), &stdout, &stderr, src)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if gotTransport != "http-sse" || gotBind != "127.0.0.1:9234" {
		t.Fatalf("route transport=%q bind=%q", gotTransport, gotBind)
	}
}

func TestEntrypointMCPServeLiveFlagRouting(t *testing.T) {
	src := newTestSource(t)
	src.env[config.EnvToken] = "sentinel-token"
	old := mcpServeRoute
	defer func() { mcpServeRoute = old }()
	var got StartupDeps
	mcpServeRoute = func(ctx context.Context, stdin io.Reader, stdout io.Writer, stderr io.Writer, deps StartupDeps, transport string, bind string) int {
		got = deps
		return 0
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"--live", "mcp", "serve", "--transport", "stdio", "--cache-path", filepath.Join(t.TempDir(), "cache.db")}, strings.NewReader(""), &stdout, &stderr, src)
	if code != 0 {
		t.Fatalf("exit = %d stderr=%q", code, stderr.String())
	}
	if !got.GitCode.Live {
		t.Fatalf("live mode not handed off: %#v", got.GitCode)
	}
}

func TestEntrypointMCPDependencyHandoffAndRedaction(t *testing.T) {
	t.Run("SCN-ENTRYPOINT-MCP-HANDOFF", func(t *testing.T) {
		src := newTestSource(t)
		src.env[config.EnvToken] = "sentinel-token"
		old := mcpRoute
		defer func() { mcpRoute = old }()
		var got StartupDeps
		mcpRoute = func(ctx context.Context, stdin io.Reader, stdout io.Writer, stderr io.Writer, deps StartupDeps) int {
			got = deps
			return 0
		}
		var stdout, stderr bytes.Buffer
		code := run([]string{"--mcp", "--cache-path", "/tmp/mcp-cache.db", "--timeout=10s"}, strings.NewReader(""), &stdout, &stderr, src)
		if code != 0 {
			t.Fatalf("exit = %d stderr=%q", code, stderr.String())
		}
		if got.Config.CachePath != "/tmp/mcp-cache.db" || got.Config.DefaultTimeout != 10*time.Second {
			t.Fatalf("handoff = %#v", got)
		}
		if strings.Contains(stdout.String()+stderr.String(), "sentinel-token") {
			t.Fatalf("token emitted stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
	})

	t.Run("SCN-ENTRYPOINT-MCP-REDACTION", func(t *testing.T) {
		src := newTestSource(t)
		src.env[config.EnvToken] = "sentinel-token"
		configPath := filepath.Join(src.homeDir, "bad.json")
		src.env[config.EnvConfigPath] = configPath
		src.readErr[configPath] = errors.New("permission denied for sentinel-token")
		var stdout, stderr bytes.Buffer
		code := run([]string{"--mcp"}, strings.NewReader(""), &stdout, &stderr, src)
		if code == 0 {
			t.Fatal("exit = 0")
		}
		if stdout.Len() != 0 {
			t.Fatalf("stdout = %q", stdout.String())
		}
		if strings.Contains(stderr.String(), "sentinel-token") || strings.Contains(stderr.String(), src.homeDir) || strings.Contains(stderr.String(), configPath) {
			t.Fatalf("stderr not redacted: %q", stderr.String())
		}
	})
}
