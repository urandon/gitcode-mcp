package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/auth"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/mcp"
	"gitcode-mcp/internal/observability"
	"gitcode-mcp/internal/servicectl"
	"gitcode-mcp/internal/testnet"
)

type observationCredentialSpy struct{ calls atomic.Int64 }

func (p *observationCredentialSpy) Resolve(context.Context, config.EffectiveConfig) (config.SecretString, config.CredentialStatus, error) {
	p.calls.Add(1)
	return config.SecretString{}, config.CredentialStatus{}, nil
}
func (p *observationCredentialSpy) Status(context.Context, config.EffectiveConfig) config.CredentialStatus {
	p.calls.Add(1)
	return config.CredentialStatus{}
}

func bootstrapObservationDaemon(t *testing.T) (string, *observability.Reader) {
	t.Helper()
	c := observability.New(observability.Config{Directory: t.TempDir(), ManagedOutput: true})
	if !c.Close(context.Background(), true) {
		t.Fatal("fixture collector did not close")
	}
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	r := &observability.Reader{Collector: c, Metrics: observability.NewMetrics(c.BootID(), at), Now: func() time.Time { return at }}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- (servicectl.RPCServer{Observability: r}).Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("daemon fixture did not unwind")
		}
	})
	return listener.Addr().String(), r
}

const observationDiscoveryInput = `{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n" +
	`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n" +
	`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"service_observability","arguments":{}}}` + "\n" +
	`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"service_metrics","arguments":{}}}` + "\n" +
	`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"service_events","arguments":{}}}` + "\n"

func TestMCPBootstrapObservationEntrypointFaultsNeverTouchCacheOrCredentials(t *testing.T) {
	address, _ := bootstrapObservationDaemon(t)
	for _, fault := range []string{"missing", "parent_failure", "unreadable", "future_schema", "migration_required"} {
		for _, credentialPresent := range []bool{false, true} {
			t.Run(fault+map[bool]string{false: "-missing-credential", true: "-credential-present"}[credentialPresent], func(t *testing.T) {
				root := t.TempDir()
				path := filepath.Join(root, "cache.db")
				switch fault {
				case "missing":
					path = filepath.Join(root, "uncreated", "cache.db")
				case "parent_failure":
					path = filepath.Join(root, "not-a-directory", "cache.db")
					if os.WriteFile(filepath.Dir(path), []byte("fixture"), 0600) != nil {
						t.Fatal("parent fixture failed")
					}
				case "unreadable":
					if os.WriteFile(path, []byte("not sqlite"), 0600) != nil {
						t.Fatal("unreadable fixture failed")
					}
				case "future_schema":
					writeSchemaVersion(t, path, 99)
				case "migration_required":
					writeSchemaVersion(t, path, 1)
				}
				before, readErr := os.ReadFile(path)
				src := newTestSource(t)
				src.env["GITCODE_MCP_SERVICE_NETWORK"] = "tcp"
				src.env["GITCODE_MCP_SERVICE_ADDRESS"] = address
				if credentialPresent {
					src.env["GITCODE_MCP_TEST_KEYCHAIN_TOKEN"] = "synthetic-private-credential"
				}
				spy := &observationCredentialSpy{}
				old := mcpRoute
				defer func() { mcpRoute = old }()
				mcpRoute = func(ctx context.Context, in io.Reader, out, errOut io.Writer, deps StartupDeps) int {
					deps.CredentialResolver = auth.NewCredentialResolverWithProvider(spy)
					return runMCPStdio(ctx, in, out, errOut, deps)
				}
				var out, errOut bytes.Buffer
				if run([]string{"--mcp", "--cache-path", path}, strings.NewReader(observationDiscoveryInput), &out, &errOut, src) != 0 || errOut.Len() != 0 {
					t.Fatal("observation entrypoint failed on unrelated bootstrap fault")
				}
				lines := nonEmptyLines(out.String())
				if len(lines) != 5 {
					t.Fatal("observation entrypoint lost responses")
				}
				for _, line := range lines {
					var response struct {
						Error json.RawMessage `json:"error"`
					}
					if json.Unmarshal([]byte(line), &response) != nil || len(response.Error) != 0 {
						t.Fatal("observation read failed due to cache/credential fault")
					}
				}
				for _, name := range []string{"service_observability", "service_metrics", "service_events"} {
					if !entrypointMCPToolNames(t, lines[1])[name] {
						t.Fatal("read observation discovery missing")
					}
				}
				cfg := config.Default()
				cfg.CachePath = path
				deps := buildStartupDeps(cfg, "", false)
				deps.Source = src
				deps.CredentialResolver = auth.NewCredentialResolverWithProvider(spy)
				assertBootstrapHTTPDiscoveryAndObservations(t, deps, nil)
				after, afterErr := os.ReadFile(path)
				if !bytes.Equal(before, after) || (readErr == nil) != (afterErr == nil) || spy.calls.Load() != 0 {
					t.Fatal("discovery/read touched cache or resolved credentials")
				}
				if fault == "missing" {
					if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
						t.Fatal("observation created cache parent")
					}
				}
				if strings.Contains(out.String()+errOut.String(), path) || strings.Contains(out.String()+errOut.String(), "synthetic-private") {
					t.Fatal("observation leaked private startup authority")
				}
			})
		}
	}
}

func assertBootstrapHTTPDiscoveryAndObservations(t *testing.T, deps StartupDeps, prepare func(*mcpBootstrap)) {
	t.Helper()
	h, bootstrap := newMCPHTTPHandler(deps)
	defer bootstrap.Close()
	if prepare != nil {
		prepare(bootstrap)
	}
	initialStore := bootstrap.store
	transport := mcp.NewHTTPSSETransport(h, mcp.ServerConfig{SessionID: func() string { return "bootstrap-observation" }, ReadinessProbe: bootstrap.Readiness})
	server := httptest.NewServer(transport.Handler())
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/sse", nil)
	stream, err := server.Client().Do(req)
	if err != nil {
		t.Fatal("HTTP bootstrap fixture stream unavailable")
	}
	defer stream.Body.Close()
	scanner := bufio.NewScanner(stream.Body)
	scanner.Buffer(make([]byte, 4096), observability.MaxMetricsBytes+8192)
	readData := func() string {
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "data: ") {
				return strings.TrimPrefix(scanner.Text(), "data: ")
			}
		}
		t.Fatal("bounded HTTP observation stream lost a response")
		return ""
	}
	endpoint := readData()
	if endpoint != "/message?session_id=bootstrap-observation" {
		t.Fatal("fixture endpoint malformed")
	}
	for _, raw := range nonEmptyLines(observationDiscoveryInput) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+endpoint, strings.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal("HTTP bootstrap fixture request failed")
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatal("HTTP bootstrap fixture transport rejected valid request")
		}
		data := readData()
		var result struct {
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(data), &result) != nil || len(result.Error) != 0 || strings.Contains(data, "synthetic-private") || strings.Contains(data, deps.Config.CachePath) {
			t.Fatal("HTTP discovery/observation failed on unrelated bootstrap fault or leaked authority")
		}
	}
	if bootstrap.store != initialStore {
		t.Fatal("HTTP discovery/observation changed SQLite bootstrap state")
	}
}

func TestMCPBootstrapProviderConstructionFailureKeepsObservationAvailable(t *testing.T) {
	address, _ := bootstrapObservationDaemon(t)
	src := newTestSource(t)
	src.env["GITCODE_MCP_SERVICE_NETWORK"] = "tcp"
	src.env["GITCODE_MCP_SERVICE_ADDRESS"] = address
	src.env[config.EnvToken] = "synthetic-private-credential"
	depsForPath := func(path string) StartupDeps {
		cfg := config.Default()
		cfg.CachePath = path
		deps := buildStartupDeps(cfg, "", false)
		deps.Source = src
		deps.CredentialResolver = auth.NewCredentialResolver(src)
		// Invalid adapter configuration injected after config validation fails
		// construction without provider, DNS, or OS credential-store access.
		deps.GitCode.BaseURL = "%synthetic-private-provider"
		return deps
	}
	call := `{"jsonrpc":"2.0","id":0,"method":"tools/call","params":{"name":"create_issue","arguments":{"repo_id":"fixture-a","title":"Public fixture","body":"fixture","write_mode":"live","idempotency_key":"bootstrap-provider-failure"}}}` + "\n"
	var out, errOut bytes.Buffer
	if runMCPStdio(context.Background(), strings.NewReader(call+observationDiscoveryInput), &out, &errOut, depsForPath(filepath.Join(t.TempDir(), "cache.db"))) != 0 || errOut.Len() != 0 {
		t.Fatal("provider fault disabled stdio transport")
	}
	lines := nonEmptyLines(out.String())
	if len(lines) != 6 {
		t.Fatal("provider fault lost stdio responses")
	}
	for i, line := range lines {
		var response struct {
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(line), &response) != nil || (len(response.Error) != 0) != (i == 0) || strings.Contains(line, "synthetic-private") {
			t.Fatal("provider fault leaked configuration or disabled observation")
		}
	}
	assertBootstrapHTTPDiscoveryAndObservations(t, depsForPath(filepath.Join(t.TempDir(), "cache.db")), func(b *mcpBootstrap) {
		if _, err := b.ForTool(context.Background(), "create_issue", nil); err == nil || b.Diagnostic().ErrorClass == "" {
			t.Fatal("provider construction fixture did not fail safely")
		}
	})
}

func TestMCPBootstrapLiveWriteMissingCredentialAndOfflineFailClosed(t *testing.T) {
	for _, offline := range []bool{false, true} {
		src := newTestSource(t)
		cfgPath := filepath.Join(src.configDir, "fixture.yaml")
		src.env[config.EnvMCPConfigPath] = cfgPath
		src.files[cfgPath] = []byte("credential:\n  store: env\n")
		path := filepath.Join(t.TempDir(), "uncreated", "cache.db")
		input := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"create_issue","arguments":{"repo_id":"fixture-a","title":"Public fixture","body":"fixture","write_mode":"live","idempotency_key":"negative-bootstrap-write"}}}` + "\n"
		args := []string{"--mcp", "--cache-path", path}
		if offline {
			args = append(args, "--offline")
		}
		var out, errOut bytes.Buffer
		if run(args, strings.NewReader(input), &out, &errOut, src) != 0 || errOut.Len() != 0 {
			t.Fatal("negative live fixture entrypoint failed")
		}
		var response struct {
			Error struct {
				Data struct {
					Code string `json:"code"`
				} `json:"data"`
			} `json:"error"`
		}
		if json.Unmarshal(out.Bytes(), &response) != nil {
			t.Fatal("negative live fixture response malformed")
		}
		want := "credential_unavailable"
		if offline {
			want = "live_disabled"
		}
		if response.Error.Data.Code != want {
			t.Fatal("live request fell back to fixture or lost fail-closed reason")
		}
		if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
			t.Fatal("rejected live request opened or created cache")
		}
	}
}

func TestMCPBootstrapHTTPReadinessAndDiscoveryDoNotInitialize(t *testing.T) {
	src := newTestSource(t)
	address, _ := bootstrapObservationDaemon(t)
	src.env["GITCODE_MCP_SERVICE_NETWORK"] = "tcp"
	src.env["GITCODE_MCP_SERVICE_ADDRESS"] = address
	cfg := config.Default()
	cfg.CachePath = filepath.Join(t.TempDir(), "uncreated", "cache.db")
	deps := buildStartupDeps(cfg, "", false)
	deps.Source = src
	spy := &observationCredentialSpy{}
	deps.CredentialResolver = auth.NewCredentialResolverWithProvider(spy)
	h, b := newMCPHTTPHandler(deps)
	defer b.Close()
	transport := mcp.NewHTTPSSETransport(h, mcp.ServerConfig{ReadinessProbe: b.Readiness})
	server := httptest.NewServer(transport.Handler())
	defer server.Close()
	for _, url := range []string{"/health", "/ready"} {
		resp, err := server.Client().Get(server.URL + url)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatal("transport metadata unavailable before cache bootstrap")
		}
	}
	if b.store != nil || spy.calls.Load() != 0 {
		t.Fatal("HTTP readiness opened cache or resolved credentials")
	}
	if _, err := os.Stat(filepath.Dir(cfg.CachePath)); !os.IsNotExist(err) {
		t.Fatal("HTTP startup created cache parent")
	}
	// The real initialized store remains unowned until an explicit tool call.
	b.mu.Lock()
	ready := b.Readiness(context.Background())
	b.mu.Unlock()
	if !ready.Ready || ready.Code != "bootstrap_busy" {
		t.Fatal("cache bootstrap contention blocked transport readiness")
	}
}

func TestMCPBootstrapLaterLiveWriteStillConfirmedAuditedAndIdempotent(t *testing.T) {
	api := testnet.NewGitCodeAPIServer(t)
	defer api.Close()
	path := filepath.Join(t.TempDir(), "cache.db")
	addRepoForStartupTest(t, path, api.BaseURL())
	src := newTestSource(t)
	src.env["GITCODE_MCP_TEST_KEYCHAIN_TOKEN"] = "test-token"
	src.env[config.EnvAPIURL] = api.BaseURL()
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n"
	call := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"create_issue","arguments":{"repo_id":"fixture-a","title":"Offline compatibility fixture","body":"public-safe fixture","write_mode":"live","idempotency_key":"observation-bootstrap-write"}}}` + "\n"
	var out, errOut bytes.Buffer
	if run([]string{"--mcp", "--cache-path", path}, strings.NewReader(input+call+call), &out, &errOut, src) != 0 || errOut.Len() != 0 {
		t.Fatal("later authorized live tool failed")
	}
	lines := nonEmptyLines(out.String())
	if len(lines) != 3 {
		t.Fatal("missing audited write responses")
	}
	for _, line := range lines {
		var resp struct {
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(line), &resp) != nil || len(resp.Error) != 0 {
			t.Fatal("live write lost confirmed provider behavior")
		}
	}
	if api.Counts().CreateIssue != 1 || api.Counts().UnexpectedRequests != 0 {
		t.Fatal("live operation fell back to fixture or repeated unsafe POST")
	}
	assertStartupCreateConfirmation(t, path, "observation-bootstrap-write")
	if strings.Contains(out.String()+errOut.String(), "test-token") {
		t.Fatal("live credential leaked")
	}
}

func TestMCPBootstrapClosedAndBusyAreBounded(t *testing.T) {
	src := newTestSource(t)
	cfg := config.Default()
	cfg.CachePath = filepath.Join(t.TempDir(), "uncreated", "cache.db")
	deps := buildStartupDeps(cfg, "", false)
	deps.Source = src
	b := newMCPBootstrap(deps)
	b.mu.Lock()
	start := time.Now()
	_, err := b.ForTool(context.Background(), "cache_status", nil)
	b.mu.Unlock()
	if err == nil || time.Since(start) > time.Second {
		t.Fatal("bootstrap waited on occupied state")
	}
	b.Close()
	if _, err := b.ForTool(context.Background(), "cache_status", nil); err == nil {
		t.Fatal("closed bootstrap reopened cache")
	}
	if !reflect.DeepEqual(b.Diagnostic(), mcp.StartupDiagnostic{}) {
		t.Fatal("busy/closed bootstrap fabricated cache diagnosis")
	}
}
