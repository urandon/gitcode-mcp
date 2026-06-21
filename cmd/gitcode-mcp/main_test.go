package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	var stdout, stderr bytes.Buffer
	code := run([]string{"--live", "--cache-path", cachePath, "search", "test"}, strings.NewReader(""), &stdout, &stderr, src)
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
