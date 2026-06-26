package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/diagnostics"

	_ "modernc.org/sqlite"
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
	t.Run("SCN-MOCKAPI-LIVE-SYNC-VALID", func(t *testing.T) {
		server := NewMockGitCodeAPI(t)
		defer server.Close()
		cachePath := filepath.Join(t.TempDir(), "cache.db")
		src := newTestSource(t)
		src.env[config.EnvToken] = "test-token"
		addRepoForStartupTest(t, cachePath, server.BaseURL())

		var stdout, stderr bytes.Buffer
		code := run([]string{"sync", "--live", "--cache-path", cachePath, "--repo", "fixture-a"}, strings.NewReader(""), &stdout, &stderr, src)
		if code != 0 {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		counts := server.Counts()
		if counts.ListIssues == 0 || counts.ListWikiPages == 0 || counts.ListComments == 0 || counts.UnexpectedRequests != 0 {
			t.Fatalf("mock counts = %#v", counts)
		}
		out := stdout.String() + stderr.String()
		if strings.Contains(out, "ISSUE-42") || strings.Contains(out, "WIKI-HOME") {
			t.Fatalf("fixture identifiers leaked: %q", out)
		}
		assertStartupCacheHasLiveMockRecords(t, cachePath)
	})

	t.Run("SCN-MOCKAPI-LIVE-SYNC-MISSING-CREDENTIAL", func(t *testing.T) {
		server := NewMockGitCodeAPI(t)
		defer server.Close()
		cachePath := filepath.Join(t.TempDir(), "cache.db")
		src := newTestSource(t)
		configPath := filepath.Join(t.TempDir(), "config.yaml")
		src.files[configPath] = []byte("credential:\n  store: env\n")
		src.env[config.EnvMCPConfigPath] = configPath
		addRepoForStartupTest(t, cachePath, server.BaseURL())

		var stdout, stderr bytes.Buffer
		code := run([]string{"sync", "--live", "--cache-path", cachePath, "--repo", "fixture-a"}, strings.NewReader(""), &stdout, &stderr, src)
		if code == 0 {
			t.Fatalf("code=0 stdout=%q", stdout.String())
		}
		if counts := server.Counts(); counts.TotalRequests != 0 {
			t.Fatalf("mock counts=%#v, want zero", counts)
		}
		if !strings.Contains(stderr.String(), "failure_class: config_credential") {
			t.Fatalf("stderr missing canonical failure class: %q", stderr.String())
		}
	})

	t.Run("SCN-MOCKAPI-LIVE-SYNC-INVALID-TOKEN-401 SCN-CLI-ERROR-OUTPUT-401", func(t *testing.T) {
		server := NewMockGitCodeAPI(t, MockGitCodeAPIAuthMode(mockGitCodeAPIAuthReject401))
		defer server.Close()
		cachePath := filepath.Join(t.TempDir(), "cache.db")
		src := newTestSource(t)
		src.env[config.EnvToken] = "invalid-test-token"
		addRepoForStartupTest(t, cachePath, server.BaseURL())

		var stdout, stderr bytes.Buffer
		code := run([]string{"sync", "--live", "--cache-path", cachePath, "--repo", "fixture-a"}, strings.NewReader(""), &stdout, &stderr, src)
		if code == 0 {
			t.Fatalf("code=0 stdout=%q", stdout.String())
		}
		counts := server.Counts()
		if counts.TotalRequests == 0 || counts.AuthFailures == 0 {
			t.Fatalf("mock counts=%#v, want auth failure after request", counts)
		}
		out := stdout.String() + stderr.String()
		if !strings.Contains(out, "failure_class: api_validation") || strings.Contains(out, "ISSUE-42") || strings.Contains(out, "WIKI-HOME") {
			t.Fatalf("invalid-token output = %q", out)
		}
		assertNoDecommissionedFailureClass(t, out)
	})

	for _, tt := range []struct {
		name string
		mode mockGitCodeAPIFailureMode
		want string
	}{
		{name: "SCN-CLI-ERROR-OUTPUT-400", mode: mockGitCodeAPIFailure400, want: string(diagnostics.CodeAPIFailure)},
		{name: "SCN-CLI-ERROR-OUTPUT-404", mode: mockGitCodeAPIFailure404, want: string(diagnostics.CodeAPIFailure)},
		{name: "SCN-CLI-ERROR-OUTPUT-409", mode: mockGitCodeAPIFailure409, want: string(diagnostics.CodeAPIFailure)},
		{name: "SCN-CLI-ERROR-OUTPUT-413", mode: mockGitCodeAPIFailure413, want: string(diagnostics.CodeAPIFailure)},
		{name: "SCN-CLI-ERROR-OUTPUT-429", mode: mockGitCodeAPIFailure429, want: string(diagnostics.CodeAPIFailure)},
		{name: "SCN-CLI-ERROR-OUTPUT-MALFORMED-JSON", mode: mockGitCodeAPIFailureMalformedJSON, want: string(diagnostics.CodeSchemaDecode)},
		{name: "SCN-CLI-ERROR-OUTPUT-SCHEMA-MISMATCH", mode: mockGitCodeAPIFailureSchemaMismatch, want: string(diagnostics.CodeSchemaDecode)},
		{name: "SCN-CLI-ERROR-OUTPUT-PARTIAL-RESPONSE", mode: mockGitCodeAPIFailurePartial, want: string(diagnostics.CodeSchemaDecode)},
		{name: "SCN-CLI-ERROR-OUTPUT-TIMEOUT", mode: mockGitCodeAPIFailureTimeout, want: string(diagnostics.CodeLiveTransportFailure)},
		{name: "SCN-CLI-ERROR-OUTPUT-500", mode: mockGitCodeAPIFailure500, want: string(diagnostics.CodeLiveTransportFailure)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := NewMockGitCodeAPI(t, MockGitCodeAPIFailureMode(tt.mode))
			defer server.Close()
			cachePath := filepath.Join(t.TempDir(), "cache.db")
			src := newTestSource(t)
			src.env[config.EnvToken] = "test-token"
			if tt.mode == mockGitCodeAPIFailureTimeout {
				configPath := filepath.Join(t.TempDir(), "config.json")
				src.env[config.EnvConfigPath] = configPath
				src.files[configPath] = []byte(`{"default_timeout":"1ms","max_retries":0}`)
			}
			addRepoForStartupTest(t, cachePath, server.BaseURL())

			var stdout, stderr bytes.Buffer
			code := run([]string{"sync", "--live", "--cache-path", cachePath, "--repo", "fixture-a", "--issues"}, strings.NewReader(""), &stdout, &stderr, src)
			if code == 0 {
				t.Fatalf("code=0 stdout=%q", stdout.String())
			}
			out := stdout.String() + stderr.String()
			if !strings.Contains(out, "failure_class: "+tt.want) {
				t.Fatalf("output missing failure class %s: %q", tt.want, out)
			}
			if tt.want == string(diagnostics.CodeAPIFailure) || tt.want == string(diagnostics.CodeSchemaDecode) {
				assertNoDecommissionedFailureClass(t, out)
			}
		})
	}

	t.Run("SCN-MOCKAPI-OFFLINE-SYNC-NO-HTTP", func(t *testing.T) {
		server := NewMockGitCodeAPI(t)

		defer server.Close()
		cachePath := filepath.Join(t.TempDir(), "cache.db")
		src := newTestSource(t)
		configPath := filepath.Join(t.TempDir(), "config.yaml")
		src.files[configPath] = []byte("credential:\n  store: env\n")
		src.env[config.EnvMCPConfigPath] = configPath
		addRepoForStartupTest(t, cachePath, server.BaseURL())

		var stdout, stderr bytes.Buffer
		code := run([]string{"sync", "--cache-path", cachePath, "--repo", "fixture-a"}, strings.NewReader(""), &stdout, &stderr, src)
		if code != 0 {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		if counts := server.Counts(); counts.TotalRequests != 0 {
			t.Fatalf("mock counts=%#v, want zero", counts)
		}
	})

	t.Run("SCN-MOCKAPI-API-BASE-AUTHORITY", func(t *testing.T) {
		pair := NewMockGitCodeAPIPair(t)
		defer pair.Selected.Close()
		defer pair.NonSelected.Close()
		cachePath := filepath.Join(t.TempDir(), "cache.db")
		src := newTestSource(t)
		src.env[config.EnvToken] = "test-token"
		src.env[config.EnvAPIURL] = pair.NonSelected.BaseURL()
		addRepoForStartupTest(t, cachePath, pair.Selected.BaseURL())

		var stdout, stderr bytes.Buffer
		code := run([]string{"sync", "--live", "--cache-path", cachePath, "--repo", "fixture-a"}, strings.NewReader(""), &stdout, &stderr, src)
		if code != 0 {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		if selected, nonSelected := pair.Selected.Counts(), pair.NonSelected.Counts(); selected.TotalRequests == 0 || nonSelected.TotalRequests != 0 {
			t.Fatalf("selected=%#v nonSelected=%#v", selected, nonSelected)
		}
	})

	t.Run("SCN-CLI-DOCTOR-LIVE-JSON-STARTUP-SNAPSHOT", func(t *testing.T) {
		server := NewMockGitCodeAPI(t)
		defer server.Close()
		cachePath := filepath.Join(t.TempDir(), "cache.db")
		src := newTestSource(t)
		src.env[config.EnvToken] = "test-token"
		addRepoForStartupTest(t, cachePath, server.BaseURL())

		var stdout, stderr bytes.Buffer
		code := run([]string{"doctor", "--live", "--format", "json", "--cache-path", cachePath, "--repo", "fixture-a"}, strings.NewReader(""), &stdout, &stderr, src)
		if code != 0 {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"\"provider_mode\": \"live-http\"", "\"source\": \"env:GITCODE_TOKEN\"", fmt.Sprintf("\"path\": \"%s\"", cachePath), fmt.Sprintf("\"api_base_url\": \"%s\"", server.BaseURL())} {
			if !strings.Contains(out, want) {
				t.Fatalf("doctor output missing %q in %q", want, out)
			}
		}
		for _, want := range []string{"\"api_base_url_source\": \"repository_binding.api_base_url\"", "\"readiness_status\": \"ready\""} {
			if !strings.Contains(out, want) {
				t.Fatalf("doctor output missing %q in %q", want, out)
			}
		}
		if counts := server.Counts(); strings.Contains(out, "test-token") || strings.Contains(out, "Authorization") || counts.TotalRequests != 0 {
			t.Fatalf("doctor leaked secret or contacted server; counts=%#v out=%q", counts, out)
		}
	})

	t.Run("SCN-CLI-DOCTOR-LIVE-JSON-SELECTED-VS-NON-SELECTED", func(t *testing.T) {
		pair := NewMockGitCodeAPIPair(t)
		defer pair.Selected.Close()
		defer pair.NonSelected.Close()
		cachePath := filepath.Join(t.TempDir(), "cache.db")
		src := newTestSource(t)
		src.env[config.EnvToken] = "test-token"
		addRepoForStartupTest(t, cachePath, pair.Selected.BaseURL())
		addNamedRepoForStartupTest(t, cachePath, "fixture-b", pair.NonSelected.BaseURL())

		var stdout, stderr bytes.Buffer
		code := run([]string{"doctor", "--live", "--format", "json", "--cache-path", cachePath, "--repo", "fixture-b"}, strings.NewReader(""), &stdout, &stderr, src)
		if code != 0 {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		out := stdout.String()
		if !strings.Contains(out, fmt.Sprintf("\"api_base_url\": \"%s\"", pair.NonSelected.BaseURL())) || strings.Contains(out, fmt.Sprintf("\"api_base_url\": \"%s\"", pair.Selected.BaseURL())) {
			t.Fatalf("doctor did not switch effective base URL: %q", out)
		}
		if selected, nonSelected := pair.Selected.Counts(), pair.NonSelected.Counts(); !strings.Contains(out, "\"readiness_status\": \"ready\"") || selected.TotalRequests != 0 || nonSelected.TotalRequests != 0 {
			t.Fatalf("doctor readiness/request mismatch selected=%#v nonSelected=%#v out=%q", selected, nonSelected, out)
		}
	})

	t.Run("SCN-CLI-DOCTOR-LIVE-JSON-MISSING-CREDENTIAL-NO-HTTP", func(t *testing.T) {
		server := NewMockGitCodeAPI(t)
		defer server.Close()
		cachePath := filepath.Join(t.TempDir(), "cache.db")
		src := newTestSource(t)
		configPath := filepath.Join(t.TempDir(), "config.yaml")
		src.files[configPath] = []byte("credential:\n  store: env\n")
		src.env[config.EnvMCPConfigPath] = configPath
		addRepoForStartupTest(t, cachePath, server.BaseURL())

		var stdout, stderr bytes.Buffer
		code := run([]string{"doctor", "--live", "--format", "json", "--cache-path", cachePath, "--repo", "fixture-a"}, strings.NewReader(""), &stdout, &stderr, src)
		if code != 0 {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{fmt.Sprintf("\"api_base_url\": \"%s\"", server.BaseURL()), "\"readiness_status\": \"missing_credential\"", "\"missing_credential\""} {
			if !strings.Contains(out, want) {
				t.Fatalf("doctor output missing %q in %q", want, out)
			}
		}
		if counts := server.Counts(); counts.TotalRequests != 0 {
			t.Fatalf("doctor contacted server; counts=%#v out=%q", counts, out)
		}
	})

	t.Run("SCN-MOCKAPI-LIVE-CREATE-ISSUE", func(t *testing.T) {
		server := NewMockGitCodeAPI(t)
		defer server.Close()
		cachePath := filepath.Join(t.TempDir(), "cache.db")
		src := newTestSource(t)
		configPath := filepath.Join(t.TempDir(), "config.yaml")
		src.files[configPath] = []byte("credential:\n  store: auto\n")
		src.env[config.EnvMCPConfigPath] = configPath
		src.env["GITCODE_MCP_TEST_KEYCHAIN_TOKEN"] = "test-token"
		addRepoForStartupTest(t, cachePath, server.BaseURL())

		var authStdout, authStderr bytes.Buffer
		authCode := run([]string{"auth", "status", "--live", "--cache-path", cachePath, "--repo", "fixture-a"}, strings.NewReader(""), &authStdout, &authStderr, src)
		if authCode != 0 {
			t.Fatalf("auth code=%d stdout=%q stderr=%q", authCode, authStdout.String(), authStderr.String())
		}
		authOut := authStdout.String() + authStderr.String()
		if !strings.Contains(authOut, "credential_source: mock-keychain") || strings.Contains(authOut, "test-token") {
			t.Fatalf("auth status output invalid: %q", authOut)
		}
		if counts := server.Counts(); counts.TotalRequests != 0 {
			t.Fatalf("auth status contacted server; counts=%#v", counts)
		}

		var stdout, stderr bytes.Buffer
		code := run([]string{"create-issue", "--live", "--cache-path", cachePath, "--repo", "fixture-a", "--title", "Mock Created", "--body", "created by mock keychain", "--idempotency-key", "cred-write-1"}, strings.NewReader(""), &stdout, &stderr, src)
		if code != 0 {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		creates := server.CapturedCreateRequests()
		if len(creates) != 1 || !creates[0].AuthorizationOK || creates[0].IdempotencyKey != "cred-write-1" {
			t.Fatalf("create requests = %#v", creates)
		}
		out := stdout.String() + stderr.String()
		if strings.Contains(out, "fixture client is read-only") || strings.Contains(out, "test-token") {
			t.Fatalf("write output invalid: %q", out)
		}
		assertStartupCreateConfirmation(t, cachePath, "cred-write-1")
	})

	t.Run("SCN-CRED-DOCTOR-LIVE-MOCK-KEYCHAIN", func(t *testing.T) {
		server := NewMockGitCodeAPI(t)
		defer server.Close()
		cachePath := filepath.Join(t.TempDir(), "cache.db")
		src := newTestSource(t)
		src.env["GITCODE_MCP_TEST_KEYCHAIN_TOKEN"] = "test-token"
		addRepoForStartupTest(t, cachePath, server.BaseURL())

		var stdout, stderr bytes.Buffer
		code := run([]string{"doctor", "--live", "--format", "json", "--cache-path", cachePath, "--repo", "fixture-a"}, strings.NewReader(""), &stdout, &stderr, src)
		if code != 0 {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		out := stdout.String()
		for _, want := range []string{"\"provider_mode\": \"live-http\"", "\"source\": \"mock-keychain\"", fmt.Sprintf("\"path\": \"%s\"", cachePath), fmt.Sprintf("\"api_base_url\": \"%s\"", server.BaseURL())} {
			if !strings.Contains(out, want) {
				t.Fatalf("doctor output missing %q in %q", want, out)
			}
		}
		if counts := server.Counts(); strings.Contains(out, "test-token") || counts.TotalRequests != 0 {
			t.Fatalf("doctor leaked token or contacted server; counts=%#v out=%q", counts, out)
		}
	})

	t.Run("SCN-CLI-LIVE-BINDING-INVALID-URL-NO-HTTP", func(t *testing.T) {
		server := NewMockGitCodeAPI(t)
		defer server.Close()
		cachePath := filepath.Join(t.TempDir(), "cache.db")
		src := newTestSource(t)
		src.env[config.EnvToken] = "test-token"
		addRepoForStartupTest(t, cachePath, "ftp://example.invalid/api")

		var stdout, stderr bytes.Buffer
		code := run([]string{"sync", "--live", "--cache-path", cachePath, "--repo", "fixture-a"}, strings.NewReader(""), &stdout, &stderr, src)
		if code == 0 {
			t.Fatalf("code=0 stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
		if counts := server.Counts(); !strings.Contains(stderr.String(), "api_base_url") || counts.TotalRequests != 0 {
			t.Fatalf("stderr=%q counts=%#v", stderr.String(), counts)
		}
	})

	t.Run("SCN-CLI-LIVE-BINDING-DISABLED-SCOPE-NO-HTTP", func(t *testing.T) {
		server := NewMockGitCodeAPI(t)
		defer server.Close()
		cachePath := filepath.Join(t.TempDir(), "cache.db")
		src := newTestSource(t)
		src.env[config.EnvToken] = "test-token"
		addRepoForStartupTestWithScopes(t, cachePath, server.BaseURL(), "issues")

		var stdout, stderr bytes.Buffer
		code := run([]string{"sync", "--live", "--wiki", "--cache-path", cachePath, "--repo", "fixture-a"}, strings.NewReader(""), &stdout, &stderr, src)
		if code == 0 {
			t.Fatalf("code=0 stdout=%q stderr=%q", stdout.String(), stderr.String())
		}
		if counts := server.Counts(); !strings.Contains(stderr.String(), "scope") || counts.TotalRequests != 0 {
			t.Fatalf("stderr=%q counts=%#v", stderr.String(), counts)
		}
	})
}

func addRepoForStartupTest(t *testing.T, cachePath, baseURL string) {
	t.Helper()
	addRepoForStartupTestWithScopes(t, cachePath, baseURL, "issues,wiki")
}

func addRepoForStartupTestWithScopes(t *testing.T, cachePath, baseURL, scopes string) {
	t.Helper()
	addNamedRepoForStartupTestWithScopes(t, cachePath, "fixture-a", baseURL, scopes)
}

func addNamedRepoForStartupTest(t *testing.T, cachePath, repoID, baseURL string) {
	t.Helper()
	addNamedRepoForStartupTestWithScopes(t, cachePath, repoID, baseURL, "issues,wiki")
}

func addNamedRepoForStartupTestWithScopes(t *testing.T, cachePath, repoID, baseURL, scopes string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	owner := "owner-" + repoID
	name := "repo-" + repoID
	if repoID == "fixture-a" {
		owner = "owner-a"
		name = "repo-a"
	}
	code := run([]string{"repo", "add", "--cache-path", cachePath, "--repo", repoID, "--owner", owner, "--name", name, "--api-base-url", baseURL, "--scopes", scopes}, strings.NewReader(""), &stdout, &stderr, newTestSource(t))
	if code != 0 {
		t.Fatalf("repo add code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func assertStartupCacheHasLiveMockRecords(t *testing.T, cachePath string) {
	t.Helper()
	store, err := cache.NewSQLiteStore(context.Background(), cachePath)
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	defer store.Close()
	if _, err := store.GetSourceScoped(context.Background(), "fixture-a", "ISSUE-MOCK-ISSUE-100"); err != nil {
		t.Fatalf("mock issue missing: %v", err)
	}
	if _, err := store.GetSourceScoped(context.Background(), "fixture-a", "WIKI-LIVEGUIDE.MD"); err != nil {
		t.Fatalf("mock wiki missing: %v", err)
	}
	if _, err := store.GetSourceScoped(context.Background(), "fixture-a", "ISSUE-42"); err == nil {
		t.Fatalf("fixture issue leaked into live cache")
	}
	if _, err := store.GetSourceScoped(context.Background(), "fixture-a", "WIKI-HOME"); err == nil {
		t.Fatalf("fixture wiki leaked into live cache")
	}
}

func assertStartupCreateConfirmation(t *testing.T, cachePath, idempotencyKey string) {
	t.Helper()
	store, err := cache.NewSQLiteStore(context.Background(), cachePath)
	if err != nil {
		t.Fatalf("open cache: %v", err)
	}
	defer store.Close()
	confirmation, err := store.GetCacheConfirmationByKey(context.Background(), "fixture-a", idempotencyKey)
	if err != nil || confirmation == nil || confirmation.RecordID != "ISSUE-MOCK-CREATED-ISSUE" || confirmation.RemoteID != "MOCK-CREATED-ISSUE" {
		t.Fatalf("cache confirmation = %#v err=%v", confirmation, err)
	}
	auditEvent, err := store.GetAuditEventByKey(context.Background(), "fixture-a", idempotencyKey)
	if err != nil || auditEvent == nil || auditEvent.RemoteID != "MOCK-CREATED-ISSUE" || auditEvent.RequestMetadata["method"] != "POST" {
		t.Fatalf("audit event = %#v err=%v", auditEvent, err)
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

func TestEntrypointMCPStartupFallback(t *testing.T) {
	t.Run("SCN-MCP-STARTUP-SCHEMA-INCOMPATIBLE", func(t *testing.T) {
		cachePath := filepath.Join(t.TempDir(), "cache.db")
		writeSchemaVersion(t, cachePath, 99)
		stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"doctor","arguments":{}}}` + "\n")
		var stdout, stderr bytes.Buffer
		code := run([]string{"--mcp", "--cache-path", cachePath}, stdin, &stdout, &stderr, newTestSource(t))
		if code != 0 || stderr.Len() != 0 {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
		lines := nonEmptyLines(stdout.String())
		if len(lines) != 2 {
			t.Fatalf("stdout lines=%d %q", len(lines), stdout.String())
		}
		assertStartupToolsList(t, lines[0], "schema_incompatible")
		assertStartupDoctor(t, lines[1], "schema_incompatible", "upgrade")
	})

	t.Run("SCN-MCP-STARTUP-INJECTED-CACHE-INIT-FAILURE", func(t *testing.T) {
		cachePath := filepath.Join(t.TempDir(), "missing", "cache.db")
		if err := os.WriteFile(filepath.Dir(cachePath), []byte("not a directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		stdin := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"doctor","arguments":{}}}` + "\n")
		var stdout, stderr bytes.Buffer
		code := run([]string{"--mcp", "--cache-path", cachePath}, stdin, &stdout, &stderr, newTestSource(t))
		if code != 0 || stderr.Len() != 0 {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
		lines := nonEmptyLines(stdout.String())
		if len(lines) != 2 {
			t.Fatalf("stdout lines=%d %q", len(lines), stdout.String())
		}
		assertStartupToolsList(t, lines[0], "startup-failure")
		assertStartupDoctor(t, lines[1], "startup-failure", "doctor")
	})
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

func nonEmptyLines(out string) []string {
	var lines []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func assertStartupToolsList(t *testing.T, line string, wantCode string) {
	t.Helper()
	var resp struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
			StartupDiagnostic struct {
				ErrorClass string `json:"error_class"`
			} `json:"startup_diagnostic"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("decode tools/list: %v line=%q", err, line)
	}
	if len(resp.Result.Tools) != 1 || resp.Result.Tools[0].Name != "doctor" {
		t.Fatalf("tools/list tools=%+v", resp.Result.Tools)
	}
	if resp.Result.StartupDiagnostic.ErrorClass != wantCode {
		t.Fatalf("diagnostic=%q want %q", resp.Result.StartupDiagnostic.ErrorClass, wantCode)
	}
}

func assertStartupDoctor(t *testing.T, line string, wantCode string, wantRemediation string) {
	t.Helper()
	var resp struct {
		Result struct {
			StructuredContent struct {
				Status      string `json:"status"`
				Diagnostics []struct {
					Code        string `json:"code"`
					Message     string `json:"message"`
					Remediation string `json:"remediation"`
				} `json:"diagnostics"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("decode doctor: %v line=%q", err, line)
	}
	if resp.Result.StructuredContent.Status != "degraded" || len(resp.Result.StructuredContent.Diagnostics) != 1 {
		t.Fatalf("doctor=%+v", resp.Result.StructuredContent)
	}
	diagnostic := resp.Result.StructuredContent.Diagnostics[0]
	if diagnostic.Code != wantCode || diagnostic.Message == "" || !strings.Contains(diagnostic.Remediation, wantRemediation) {
		t.Fatalf("diagnostic=%+v", diagnostic)
	}
}

func writeSchemaVersion(t *testing.T, path string, version int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_version (version INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_version (version) VALUES (?)`, version); err != nil {
		t.Fatal(err)
	}
}

func assertNoDecommissionedFailureClass(t *testing.T, out string) {
	t.Helper()
	for _, bad := range []string{"failure_class: live_transport_failure", "failure_class: configuration_error", "failure_class: live_api_failure", "failure_class: live_auth_failure", "failure_class: unsupported_mock_payload"} {
		if strings.Contains(out, bad) {
			t.Fatalf("decommissioned failure class %q in output %q", bad, out)
		}
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
