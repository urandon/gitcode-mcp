#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
WORKDIR="${TMPDIR:-/tmp}/gitcode-mcp-validation-016-$$"
cleanup() {
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

mkdir -p "$WORKDIR"
rsync -a --exclude '.git' --exclude 'ai/artifacts' "$ROOT/" "$WORKDIR/"
cd "$WORKDIR"

cat > "$WORKDIR/internal/mcp/design016_validation_test.go" <<'GOEOF'
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/service"
)

type design016SSEEvents struct{ lines *bufio.Reader }

func design016Store(t *testing.T) cache.Store {
	t.Helper()
	ctx := context.Background()
	store, err := cache.NewSQLiteStore(ctx, filepath.Join(t.TempDir(), "fixture-cache.db"))
	if err != nil { t.Fatal(err) }
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	for _, repo := range []string{"fixture-a", "fixture-b"} {
		if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: repo, Owner: repo + "-owner", Name: repo + "-repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}, CreatedAt: now, UpdatedAt: now}); err != nil { t.Fatal(err) }
		body := "# " + repo + " source\noffline cache-first body for " + repo + "\nreadable through mcp"
		graph := cache.SourceGraph{Source: cache.Source{RepoID: repo, ID: "DOC-1", Kind: "source", Path: repo + "/wiki/doc-1.md", Title: repo + " doc", Body: body, Status: "active", Labels: []string{"offline"}, ContentHash: repo + "-hash", CreatedAt: now, UpdatedAt: now}, SyncStatus: &cache.SyncStatus{RepoID: repo, SourceID: "DOC-1", RemoteType: "wiki", RemoteID: "Doc1", RemoteRevision: repo + "-rev", Status: "fresh", LastFetchedAt: now}}
		if err := store.UpsertSourceGraph(ctx, graph); err != nil { t.Fatal(err) }
	}
	return store
}

func design016OpenSSE(t *testing.T, url string, reqID string) (*http.Response, string, *design016SSEEvents) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil { t.Fatal(err) }
	if reqID != "" { req.Header.Set("X-Request-ID", reqID) }
	resp, err := http.DefaultClient.Do(req)
	if err != nil { t.Fatal(err) }
	if resp.StatusCode != http.StatusOK { t.Fatalf("SSE status=%d", resp.StatusCode) }
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") { t.Fatalf("SSE content-type=%q", ct) }
	reader := bufio.NewReader(resp.Body)
	event, data := design016ReadSSEEvent(t, reader)
	if event != "endpoint" { t.Fatalf("first SSE event=%q data=%q", event, data) }
	if !strings.HasPrefix(data, "/message?session_id=") { t.Fatalf("endpoint is not session-correlated: %q", data) }
	return resp, data, &design016SSEEvents{lines: reader}
}

func design016ReadSSEEvent(t *testing.T, r *bufio.Reader) (string, string) {
	t.Helper()
	var event, data string
	for {
		line, err := r.ReadString('\n')
		if err != nil { t.Fatalf("reading SSE event: %v", err) }
		line = strings.TrimRight(line, "\r\n")
		if line == "" { return event, data }
		if strings.HasPrefix(line, "event: ") { event = strings.TrimPrefix(line, "event: ") }
		if strings.HasPrefix(line, "data: ") { data = strings.TrimPrefix(line, "data: ") }
	}
}

func design016ReadSSEMessage(t *testing.T, events *design016SSEEvents, wantID string) response {
	t.Helper()
	event, data := design016ReadSSEEvent(t, events.lines)
	if event != "message" { t.Fatalf("SSE event=%q data=%q", event, data) }
	var resp response
	if err := json.Unmarshal([]byte(data), &resp); err != nil { t.Fatalf("bad JSON-RPC SSE data=%s err=%v", data, err) }
	if resp.ID == nil || string(*resp.ID) != wantID { t.Fatalf("response id=%v want=%s data=%s", resp.ID, wantID, data) }
	return resp
}

func design016ReadSSEMessageAny(t *testing.T, events *design016SSEEvents, wantIDs map[string]bool) response {
	t.Helper()
	event, data := design016ReadSSEEvent(t, events.lines)
	if event != "message" { t.Fatalf("SSE event=%q data=%q", event, data) }
	var resp response
	if err := json.Unmarshal([]byte(data), &resp); err != nil { t.Fatalf("bad JSON-RPC SSE data=%s err=%v", data, err) }
	if resp.ID == nil || !wantIDs[string(*resp.ID)] { t.Fatalf("response id=%v not in %v data=%s", resp.ID, wantIDs, data) }
	return resp
}

func design016Post(t *testing.T, url string, reqID string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil { t.Fatal(err) }
	req.Header.Set("Content-Type", "application/json")
	if reqID != "" { req.Header.Set("X-Request-ID", reqID) }
	resp, err := http.DefaultClient.Do(req)
	if err != nil { t.Fatal(err) }
	return resp
}

func design016RequireTransportError(t *testing.T, resp *http.Response, status int, reqID string, code string) {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != status { t.Fatalf("status=%d want=%d", resp.StatusCode, status) }
	if got := resp.Header.Get("X-Request-ID"); got != reqID { t.Fatalf("X-Request-ID=%q want=%q", got, reqID) }
	var payload transportError
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil { t.Fatalf("transport error body decode: %v", err) }
	if payload.Error.Code != code { t.Fatalf("transport error code=%q want=%q", payload.Error.Code, code) }
}

func TestDesign016HTTPSSEProductSessionTransport(t *testing.T) {
	store := design016Store(t)
	defer store.Close()
	var logs bytes.Buffer
	ids := []string{"session-a", "session-b"}
	transport := NewHTTPSSETransport(NewRPCHandler(service.New(store)), ServerConfig{
		ReadinessProbe: func(context.Context) Readiness { return Readiness{Ready: true} },
		Logger: log.New(&logs, "", 0),
		RequestID: func() string { return "generated-request" },
		SessionID: func() string { id := ids[0]; ids = ids[1:]; return id },
		PerSessionQueue: 4,
	})
	server := httptest.NewServer(transport.Handler())
	defer server.Close()

	healthReq, _ := http.NewRequest(http.MethodGet, server.URL+"/health", nil)
	healthReq.Header.Set("X-Request-ID", "health-016")
	healthResp, err := http.DefaultClient.Do(healthReq)
	if err != nil { t.Fatal(err) }
	if healthResp.StatusCode != http.StatusOK || healthResp.Header.Get("X-Request-ID") != "health-016" { t.Fatalf("health status=%d request_id=%q", healthResp.StatusCode, healthResp.Header.Get("X-Request-ID")) }
	_ = healthResp.Body.Close()

	readyReq, _ := http.NewRequest(http.MethodGet, server.URL+"/ready", nil)
	readyReq.Header.Set("X-Request-ID", "ready-016")
	readyResp, err := http.DefaultClient.Do(readyReq)
	if err != nil { t.Fatal(err) }
	if readyResp.StatusCode != http.StatusOK || readyResp.Header.Get("X-Request-ID") != "ready-016" { t.Fatalf("ready status=%d request_id=%q", readyResp.StatusCode, readyResp.Header.Get("X-Request-ID")) }
	_ = readyResp.Body.Close()

	sseA, endpointA, eventsA := design016OpenSSE(t, server.URL+"/sse", "sse-a-016")
	defer sseA.Body.Close()
	sseB, endpointB, eventsB := design016OpenSSE(t, server.URL+"/sse", "sse-b-016")
	defer sseB.Body.Close()
	if endpointA == endpointB { t.Fatalf("sessions reused endpoint %q", endpointA) }

	initResp := design016Post(t, server.URL+endpointA, "init-016", map[string]any{"jsonrpc":"2.0", "id":1, "method":"initialize"})
	if initResp.StatusCode != http.StatusAccepted || initResp.Header.Get("X-Request-ID") != "init-016" { t.Fatalf("initialize post status=%d request_id=%q", initResp.StatusCode, initResp.Header.Get("X-Request-ID")) }
	body, _ := io.ReadAll(initResp.Body)
	_ = initResp.Body.Close()
	if len(strings.TrimSpace(string(body))) != 0 { t.Fatalf("/message returned body instead of SSE delivery: %q", string(body)) }
	initSSE := design016ReadSSEMessage(t, eventsA, "1")
	if initSSE.Error != nil { t.Fatalf("initialize SSE error=%+v", initSSE.Error) }

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); resp := design016Post(t, server.URL+endpointA, "tool-a-016", map[string]any{"jsonrpc":"2.0", "id":"a", "method":"tools/call", "params":map[string]any{"name":"list_sources", "arguments":map[string]any{"repo_id":"fixture-a", "limit":1, "offset":0}}}); if resp.StatusCode != http.StatusAccepted { t.Errorf("tool A status=%d", resp.StatusCode) }; _ = resp.Body.Close() }()
	go func() { defer wg.Done(); resp := design016Post(t, server.URL+endpointB, "tool-b-016", map[string]any{"jsonrpc":"2.0", "id":"b", "method":"tools/call", "params":map[string]any{"name":"list_sources", "arguments":map[string]any{"repo_id":"fixture-b", "limit":1, "offset":0}}}); if resp.StatusCode != http.StatusAccepted { t.Errorf("tool B status=%d", resp.StatusCode) }; _ = resp.Body.Close() }()
	wg.Wait()

	respA := design016ReadSSEMessageAny(t, eventsA, map[string]bool{`"a"`: true})
	respB := design016ReadSSEMessageAny(t, eventsB, map[string]bool{`"b"`: true})
	if respA.Error != nil || respB.Error != nil { t.Fatalf("tool errors A=%+v B=%+v", respA.Error, respB.Error) }
	if !strings.Contains(string(respA.Result), "fixture-a") || strings.Contains(string(respA.Result), "fixture-b") { t.Fatalf("session A payload not isolated: %s", string(respA.Result)) }
	if !strings.Contains(string(respB.Result), "fixture-b") || strings.Contains(string(respB.Result), "fixture-a") { t.Fatalf("session B payload not isolated: %s", string(respB.Result)) }

	design016RequireTransportError(t, design016Post(t, server.URL+"/message", "missing-016", map[string]any{"jsonrpc":"2.0", "id":9, "method":"initialize"}), http.StatusBadRequest, "missing-016", "missing_session")
	design016RequireTransportError(t, design016Post(t, server.URL+"/message?session_id=unknown", "unknown-016", map[string]any{"jsonrpc":"2.0", "id":10, "method":"initialize"}), http.StatusNotFound, "unknown-016", "unknown_session")
	_ = sseA.Body.Close()
	time.Sleep(50 * time.Millisecond)
	design016RequireTransportError(t, design016Post(t, server.URL+endpointA, "closed-016", map[string]any{"jsonrpc":"2.0", "id":11, "method":"initialize"}), http.StatusNotFound, "closed-016", "unknown_session")

	for _, reqID := range []string{"health-016", "ready-016", "sse-a-016", "init-016", "tool-a-016", "tool-b-016"} {
		if !strings.Contains(logs.String(), "request_id="+reqID) { t.Fatalf("logs missing request id %s: %s", reqID, logs.String()) }
	}
}

func TestDesign016StdioCompatibilityOneLocalClient(t *testing.T) {
	store := design016Store(t)
	defer store.Close()
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	server := New(serverR, serverW, io.Discard, service.New(store))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = server.Serve() }()
	defer func() { _ = clientR.Close(); _ = clientW.Close(); wg.Wait() }()

	_, _ = clientW.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n"))
	line, err := readLine(clientR)
	if err != nil { t.Fatal(err) }
	var initResp response
	if err := json.Unmarshal(line, &initResp); err != nil || initResp.Error != nil { t.Fatalf("stdio initialize response=%s err=%v rpc=%+v", string(line), err, initResp.Error) }

	_, _ = clientW.Write([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"get_source","arguments":{"repo_id":"fixture-a","id":"DOC-1"}}}` + "\n"))
	line, err = readLine(clientR)
	if err != nil { t.Fatal(err) }
	var toolResp response
	if err := json.Unmarshal(line, &toolResp); err != nil || toolResp.Error != nil || !strings.Contains(string(toolResp.Result), "fixture-a") { t.Fatalf("stdio tool response=%s err=%v rpc=%+v", string(line), err, toolResp.Error) }
}
GOEOF

cat > "$WORKDIR/cmd/gitcode-mcp/design016_validation_test.go" <<'GOEOF'
package main

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestDesign016MCPServeHTTPSSERoutingAndStdioCompatibility(t *testing.T) {
	src := newTestSource(t)
	oldServe := mcpServeRoute
	oldStdio := mcpRoute
	defer func() { mcpServeRoute = oldServe; mcpRoute = oldStdio }()
	var gotTransport, gotBind string
	mcpServeRoute = func(ctx context.Context, stdin io.Reader, stdout io.Writer, stderr io.Writer, deps StartupDeps, transport string, bind string) int {
		gotTransport = transport
		gotBind = bind
		return 0
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"mcp", "serve", "--transport", "http-sse", "--bind", "127.0.0.1:0", "--cache-path", filepath.Join(t.TempDir(), "fixture.db")}, strings.NewReader(""), &stdout, &stderr, src)
	if code != 0 { t.Fatalf("http-sse serve exit=%d stderr=%q", code, stderr.String()) }
	if gotTransport != "http-sse" || gotBind != "127.0.0.1:0" { t.Fatalf("http-sse route transport=%q bind=%q", gotTransport, gotBind) }

	gotTransport, gotBind = "", ""
	stdout.Reset(); stderr.Reset()
	code = run([]string{"mcp", "serve", "--transport", "stdio", "--cache-path", filepath.Join(t.TempDir(), "fixture.db")}, strings.NewReader(""), &stdout, &stderr, src)
	if code != 0 { t.Fatalf("stdio serve exit=%d stderr=%q", code, stderr.String()) }
	if gotTransport != "stdio" { t.Fatalf("stdio route transport=%q", gotTransport) }
}
GOEOF

go test ./internal/mcp ./cmd/gitcode-mcp -run 'TestDesign016' -count=1
go test ./...
git -C "$ROOT" diff --check -- tests/design_package/016-mcp-server-task-2-httpsse-session-transport
