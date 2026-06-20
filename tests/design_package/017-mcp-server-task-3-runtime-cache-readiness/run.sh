#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
WORKDIR="${TMPDIR:-/tmp}/gitcode-mcp-validation-017-$$"
cleanup() {
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

mkdir -p "$WORKDIR"
rsync -a --exclude '.git' --exclude 'ai/artifacts' "$ROOT/" "$WORKDIR/"
cd "$WORKDIR"

cat > "$WORKDIR/internal/mcp/design017_validation_test.go" <<'GOEOF'
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
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

type design017SSEClient struct {
	resp     *http.Response
	endpoint string
	events   <-chan response
}

type design017LockService struct {
	serviceInterface
	err error
}

func (s *design017LockService) ListSources(context.Context, service.ListSourcesRequest) (service.ListSourcesResult, error) {
	return service.ListSourcesResult{}, s.err
}

type design017SlowService struct {
	serviceInterface
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *design017SlowService) ListSources(ctx context.Context, req service.ListSourcesRequest) (service.ListSourcesResult, error) {
	if req.RepoID != "slow" {
		return s.serviceInterface.ListSources(ctx, req)
	}
	s.once.Do(func() { close(s.entered) })
	select {
	case <-ctx.Done():
		return service.ListSourcesResult{}, ctx.Err()
	case <-s.release:
		return service.ListSourcesResult{RepoID: req.RepoID, Results: []service.SourceSummary{}, Limit: req.Limit, Offset: req.Offset}, nil
	}
}

func design017StoreAt(t *testing.T, path string) *cache.SQLiteStore {
	t.Helper()
	ctx := context.Background()
	store, err := cache.NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "fixture", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	graph := cache.SourceGraph{Source: cache.Source{RepoID: "fixture-a", ID: "DOC-1", Kind: "doc", Path: "docs/doc-1.md", Title: "Doc 1", Body: "offline cached body", Status: "active", Labels: []string{"offline"}, ContentHash: "h1", CreatedAt: now, UpdatedAt: now}, SyncStatus: &cache.SyncStatus{RepoID: "fixture-a", SourceID: "DOC-1", RemoteType: "wiki", RemoteID: "Doc1", RemoteRevision: "rev1", Status: "fresh", LastFetchedAt: now}}
	if err := store.UpsertSourceGraph(ctx, graph); err != nil {
		t.Fatal(err)
	}
	return store
}

func design017OpenSSE(t *testing.T, serverURL string) design017SSEClient {
	t.Helper()
	resp, err := http.Get(serverURL + "/sse")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("sse status=%d content_type=%q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	scanner := bufio.NewScanner(resp.Body)
	var endpoint string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			endpoint = strings.TrimPrefix(line, "data: ")
		}
		if line == "" && endpoint != "" {
			break
		}
	}
	if !strings.HasPrefix(endpoint, "/message?session_id=") {
		t.Fatalf("bad endpoint %q", endpoint)
	}
	events := make(chan response, 8)
	go func() {
		defer close(events)
		var data string
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			}
			if line == "" && data != "" {
				var r response
				_ = json.Unmarshal([]byte(data), &r)
				events <- r
				data = ""
			}
		}
	}()
	return design017SSEClient{resp: resp, endpoint: endpoint, events: events}
}

func design017Post(t *testing.T, url string, body any) int {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func design017ReadEvent(t *testing.T, events <-chan response) response {
	t.Helper()
	select {
	case r, ok := <-events:
		if !ok {
			t.Fatal("SSE event stream closed")
		}
		return r
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for SSE response")
	}
	return response{}
}

func design017AssertTypedLockCode(t *testing.T, data *errorData) {
	t.Helper()
	if data == nil {
		t.Fatal("missing error data")
	}
	switch data.Code {
	case "busy", "cache_owned", "migration_blocked":
	default:
		t.Fatalf("unexpected lock error code %q", data.Code)
	}
	if data.Operation == "" || data.StartedAt == "" || data.PID == 0 {
		t.Fatalf("missing safe lock metadata: %+v", data)
	}
}

func TestDesign017HTTPSSERuntimeReadsDuringWriterAndTypedContention(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	writer := design017StoreAt(t, path)
	defer writer.Close()
	reader, err := cache.NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	lease, err := writer.AcquireWriter(ctx, cache.WriterRequest{Operation: "sync-index", RepoID: "fixture-a"})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.ReleaseWriter(ctx, lease)

	readTransport := NewHTTPSSETransport(NewRPCHandler(service.New(reader)), ServerConfig{SessionID: func() string { return time.Now().Format("150405.000000000") }})
	readServer := httptest.NewServer(readTransport.Handler())
	defer readServer.Close()
	clientA := design017OpenSSE(t, readServer.URL)
	defer clientA.resp.Body.Close()
	clientB := design017OpenSSE(t, readServer.URL)
	defer clientB.resp.Body.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); if status := design017Post(t, readServer.URL+clientA.endpoint, map[string]any{"jsonrpc": "2.0", "id": "a", "method": "tools/call", "params": map[string]any{"name": "list_sources", "arguments": map[string]any{"repo_id": "fixture-a"}}}); status != http.StatusAccepted { t.Errorf("client A post status=%d", status) } }()
	go func() { defer wg.Done(); if status := design017Post(t, readServer.URL+clientB.endpoint, map[string]any{"jsonrpc": "2.0", "id": "b", "method": "tools/call", "params": map[string]any{"name": "list_sources", "arguments": map[string]any{"repo_id": "fixture-a"}}}); status != http.StatusAccepted { t.Errorf("client B post status=%d", status) } }()
	wg.Wait()
	respA := design017ReadEvent(t, clientA.events)
	respB := design017ReadEvent(t, clientB.events)
	if respA.Error != nil || respB.Error != nil || !strings.Contains(string(respA.Result), "DOC-1") || !strings.Contains(string(respB.Result), "DOC-1") {
		t.Fatalf("safe read responses A=%+v B=%+v", respA, respB)
	}

	_, contentionErr := reader.AcquireWriter(ctx, cache.WriterRequest{Operation: "sync", RepoID: "fixture-a"})
	var contention cache.ErrLockContention
	if !errors.As(contentionErr, &contention) {
		t.Fatalf("expected real cache lock contention, got %T %[1]v", contentionErr)
	}
	lockTransport := NewHTTPSSETransport(NewRPCHandler(&design017LockService{serviceInterface: service.New(reader), err: contentionErr}), ServerConfig{SessionID: func() string { return "lock-session" }})
	lockServer := httptest.NewServer(lockTransport.Handler())
	defer lockServer.Close()
	lockClient := design017OpenSSE(t, lockServer.URL)
	defer lockClient.resp.Body.Close()
	if status := design017Post(t, lockServer.URL+lockClient.endpoint, map[string]any{"jsonrpc": "2.0", "id": "locked", "method": "tools/call", "params": map[string]any{"name": "list_sources", "arguments": map[string]any{"repo_id": "fixture-a"}}}); status != http.StatusAccepted {
		t.Fatalf("lock post status=%d", status)
	}
	lockResp := design017ReadEvent(t, lockClient.events)
	if lockResp.Error == nil {
		t.Fatalf("lock-affected operation reported success: %+v", lockResp)
	}
	design017AssertTypedLockCode(t, lockResp.Error.Data)
}

func TestDesign017ReadinessHealthAndStdioTypedLockMapping(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache.db")
	writer := design017StoreAt(t, path)
	defer writer.Close()
	reader, err := cache.NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	lease, err := writer.AcquireWriter(ctx, cache.WriterRequest{Operation: "sync-index", RepoID: "fixture-a"})
	if err != nil {
		t.Fatal(err)
	}
	defer writer.ReleaseWriter(ctx, lease)

	migrationStore, openErr := cache.NewSQLiteStore(ctx, path)
	if openErr == nil {
		_ = migrationStore.Close()
		t.Fatal("cache open succeeded while writer ownership should block migration/open readiness")
	}
	var contention cache.ErrLockContention
	if !errors.As(openErr, &contention) {
		t.Fatalf("cache open error = %T %[1]v, want ErrLockContention", openErr)
	}
	transport := NewHTTPSSETransport(NewRPCHandler(service.New(reader)), ServerConfig{ReadinessProbe: func(context.Context) Readiness { return LockContentionReadiness(contention) }})
	server := httptest.NewServer(transport.Handler())
	defer server.Close()
	health, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	if health.StatusCode != http.StatusOK {
		t.Fatalf("health status=%d", health.StatusCode)
	}
	_ = health.Body.Close()
	readyResp, err := http.Get(server.URL + "/ready")
	if err != nil {
		t.Fatal(err)
	}
	defer readyResp.Body.Close()
	var ready Readiness
	if err := json.NewDecoder(readyResp.Body).Decode(&ready); err != nil {
		t.Fatal(err)
	}
	if readyResp.StatusCode != http.StatusServiceUnavailable || ready.Ready || ready.ErrorData == nil {
		t.Fatalf("ready status=%d body=%+v", readyResp.StatusCode, ready)
	}
	design017AssertTypedLockCode(t, ready.ErrorData)

	stdioSvc := &design017LockService{serviceInterface: service.New(reader), err: openErr}
	srv, r, w, _ := newPipeServer(stdioSvc)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = srv.Serve() }()
	req := map[string]any{"jsonrpc": "2.0", "id": 17, "method": "tools/call", "params": map[string]any{"name": "list_sources", "arguments": map[string]any{"repo_id": "fixture-a"}}}
	b, _ := json.Marshal(req)
	_, _ = r.Write(append(b, '\n'))
	line, err := readLine(w)
	if err != nil {
		t.Fatal(err)
	}
	var resp response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil {
		t.Fatalf("stdio lock failure reported success: %+v", resp)
	}
	design017AssertTypedLockCode(t, resp.Error.Data)
	_ = r.Close()
	wg.Wait()
}

func TestDesign017CancelledMessageDoesNotEnqueueOrBlockOtherClient(t *testing.T) {
	store := design017StoreAt(t, filepath.Join(t.TempDir(), "cache.db"))
	defer store.Close()
	slow := &design017SlowService{serviceInterface: service.New(store), entered: make(chan struct{}), release: make(chan struct{})}
	ids := []string{"cancel-session", "live-session"}
	transport := NewHTTPSSETransport(NewRPCHandler(slow), ServerConfig{SessionID: func() string { id := ids[0]; ids = ids[1:]; return id }, PerSessionQueue: 1})
	server := httptest.NewServer(transport.Handler())
	defer server.Close()
	cancelClient := design017OpenSSE(t, server.URL)
	defer cancelClient.resp.Body.Close()
	liveClient := design017OpenSSE(t, server.URL)
	defer liveClient.resp.Body.Close()

	ctx, cancel := context.WithCancel(context.Background())
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "cancelled", "method": "tools/call", "params": map[string]any{"name": "list_sources", "arguments": map[string]any{"repo_id": "slow"}}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+cancelClient.endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() {
		defer close(done)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-slow.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("slow read did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled /message did not return")
	}
	select {
	case resp := <-cancelClient.events:
		t.Fatalf("cancelled request enqueued a response: %+v", resp)
	case <-time.After(100 * time.Millisecond):
	}

	if status := design017Post(t, server.URL+liveClient.endpoint, map[string]any{"jsonrpc": "2.0", "id": "live", "method": "tools/call", "params": map[string]any{"name": "list_sources", "arguments": map[string]any{"repo_id": "fixture-a"}}}); status != http.StatusAccepted {
		t.Fatalf("live post status=%d", status)
	}
	liveResp := design017ReadEvent(t, liveClient.events)
	if liveResp.Error != nil || !strings.Contains(string(liveResp.Result), "DOC-1") {
		t.Fatalf("live client did not succeed after cancellation: %+v", liveResp)
	}
	close(slow.release)
}
GOEOF

go test ./internal/mcp -run 'TestDesign017' -count=1
go test ./...
git -C "$ROOT" diff --check
