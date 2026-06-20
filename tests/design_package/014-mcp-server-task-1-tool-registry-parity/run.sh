#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
WORKDIR="${TMPDIR:-/tmp}/gitcode-mcp-validation-014-$$"
cleanup() {
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

mkdir -p "$WORKDIR"
rsync -a --exclude '.git' --exclude 'ai/artifacts' "$ROOT/" "$WORKDIR/"
cd "$WORKDIR"

cat > "$WORKDIR/internal/mcp/design014_validation_test.go" <<'GOEOF'
package mcp

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/service"
)

func design014Store(t *testing.T) cache.Store {
	t.Helper()
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = store.Close() })
	for _, repo := range []string{"fixture-a", "fixture-b"} {
		if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: repo, Owner: repo + "-owner", Name: repo + "-repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil { t.Fatal(err) }
	}
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	for _, repo := range []string{"fixture-a", "fixture-b"} {
		issueBody := "# Issue " + repo + "\nshared alias text for " + repo + "\nsee WIKI-1"
		wikiBody := "# Wiki " + repo + "\nwiki page body for " + repo + "\nsee ISSUE-1"
		graphs := []cache.SourceGraph{
			{Source: cache.Source{RepoID: repo, ID: "ISSUE-1", Kind: "issue", Path: repo + "/issues/1.md", Title: "Issue " + repo, Body: issueBody, Status: "open", ContentHash: repo + "-issue-hash", CreatedAt: now, UpdatedAt: now.Add(time.Minute)}, Identities: []cache.Identity{{RepoID: repo, SourceID: "ISSUE-1", AliasType: "issue", Alias: "1", Remote: cache.RemoteAlias{Type: "issue", ID: "1"}}}, SyncStatus: &cache.SyncStatus{RepoID: repo, SourceID: "ISSUE-1", RemoteType: "issue", RemoteID: "1", RemoteRevision: repo + "-rev-issue", Status: "fresh", LastFetchedAt: now}},
			{Source: cache.Source{RepoID: repo, ID: "WIKI-1", Kind: "wiki", Path: repo + "/wiki/home.md", Title: "Wiki " + repo, Body: wikiBody, Status: "active", ContentHash: repo + "-wiki-hash", CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(2 * time.Minute)}, Identities: []cache.Identity{{RepoID: repo, SourceID: "WIKI-1", AliasType: "wiki", Alias: "Home", Remote: cache.RemoteAlias{Type: "wiki", ID: "Home"}}}, SyncStatus: &cache.SyncStatus{RepoID: repo, SourceID: "WIKI-1", RemoteType: "wiki", RemoteID: "Home", RemoteRevision: repo + "-rev-wiki", Status: "fresh", LastFetchedAt: now}},
		}
		for _, graph := range graphs {
			if err := store.UpsertSourceGraph(ctx, graph); err != nil { t.Fatal(err) }
		}
		if err := store.UpsertLink(ctx, cache.Link{RepoID: repo, SourceID: "ISSUE-1", TargetID: "WIKI-1", Kind: "mentions", Text: "see WIKI-1"}); err != nil { t.Fatal(err) }
		if err := store.UpsertLink(ctx, cache.Link{RepoID: repo, SourceID: "WIKI-1", TargetID: "ISSUE-1", Kind: "mentions", Text: "see ISSUE-1"}); err != nil { t.Fatal(err) }
	}
	svc := service.New(store)
	if _, err := svc.Index(ctx, service.OperationRequest{RepoID: "fixture-a", Mode: "full"}); err != nil { t.Fatal(err) }
	if _, err := svc.Index(ctx, service.OperationRequest{RepoID: "fixture-b", Mode: "full"}); err != nil { t.Fatal(err) }
	return store
}

func design014Call(t *testing.T, r io.Writer, w io.Reader, id int, name string, args map[string]any) (map[string]any, *rpcError) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc":"2.0", "id":id, "method":"tools/call", "params":map[string]any{"name":name, "arguments":args}})
	_, _ = r.Write(append(b, '\n'))
	line, err := readLine(w)
	if err != nil { t.Fatal(err) }
	var resp response
	if err := json.Unmarshal(line, &resp); err != nil { t.Fatalf("bad json-rpc response %s: %v", string(line), err) }
	if resp.Error != nil { return nil, resp.Error }
	var call map[string]any
	if err := json.Unmarshal(resp.Result, &call); err != nil { t.Fatal(err) }
	structured, ok := call["structuredContent"].(map[string]any)
	if !ok { t.Fatalf("%s missing structuredContent: %#v", name, call) }
	return structured, nil
}

func TestDesign014MCPStdioRegistryTwoRepoReadSurface(t *testing.T) {
	store := design014Store(t)
	srv, r, w, stderr := newPipeServer(service.New(store))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = srv.Serve() }()
	defer func() { _ = r.Close(); wg.Wait() }()

	_, _ = r.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n"))
	line, err := readLine(w)
	if err != nil { t.Fatalf("initialize failed: %v stderr=%s", err, stderr.String()) }
	var initResp response
	if err := json.Unmarshal(line, &initResp); err != nil || initResp.Error != nil { t.Fatalf("bad initialize: %s err=%v rpc=%+v", string(line), err, initResp.Error) }

	_, _ = r.Write([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}` + "\n"))
	line, err = readLine(w)
	if err != nil { t.Fatalf("tools/list failed: %v", err) }
	var toolsResp response
	if err := json.Unmarshal(line, &toolsResp); err != nil || toolsResp.Error != nil { t.Fatalf("bad tools/list: %s", string(line)) }
	var listed toolsListResult
	if err := json.Unmarshal(toolsResp.Result, &listed); err != nil { t.Fatal(err) }
	got := map[string]bool{}
	for _, tool := range listed.Tools { got[tool.Name] = true }
	for _, name := range []string{"search_sources","get_source","list_sources","get_snippet","recent_changes","link_check","stale_index_report","cache_status","search_chunks","list_chunks","source_backlinks","sync_status","export_snapshot","diff_snapshot"} {
		if !got[name] { t.Fatalf("tools/list missing approved read tool %q; got=%v", name, got) }
	}
	for _, name := range []string{"sync","create_issue","update_issue","create_page","update_page","add_comment","migration"} {
		if got[name] { t.Fatalf("mutation or sync tool %q must not be registered", name) }
	}

	calls := []struct{ name string; args map[string]any }{
		{"search_sources", map[string]any{"repo_id":"fixture-a", "query":"fixture-a", "limit":1, "offset":0}},
		{"get_source", map[string]any{"repo_id":"fixture-a", "id":"issue:1"}},
		{"list_sources", map[string]any{"repo_id":"fixture-a", "limit":2, "offset":0}},
		{"get_snippet", map[string]any{"repo_id":"fixture-a", "source_id":"ISSUE-1", "line_start":1, "line_end":2}},
		{"recent_changes", map[string]any{"repo_id":"fixture-a", "limit":2, "offset":0}},
		{"link_check", map[string]any{"repo_id":"fixture-a", "strict":true}},
		{"stale_index_report", map[string]any{"repo_id":"fixture-a"}},
		{"cache_status", map[string]any{"repo_id":"fixture-a"}},
		{"search_chunks", map[string]any{"repo_id":"fixture-a", "query":"Issue", "limit":2, "offset":0}},
		{"list_chunks", map[string]any{"repo_id":"fixture-a", "limit":2, "offset":0}},
		{"source_backlinks", map[string]any{"repo_id":"fixture-a", "id":"ISSUE-1", "limit":2, "offset":0}},
		{"sync_status", map[string]any{"repo_id":"fixture-a", "id":"ISSUE-1"}},
		{"export_snapshot", map[string]any{"repo_id":"fixture-a", "format":"json", "inline":true}},
		{"diff_snapshot", map[string]any{"repo_id":"fixture-a", "base_id":"missing-base", "head_id":"missing-head", "format":"json"}},
	}
	for i, tc := range calls {
		structured, rpcErr := design014Call(t, r, w, 10+i, tc.name, tc.args)
		if rpcErr != nil {
			if tc.name == "diff_snapshot" && rpcErr.Code == -32000 && rpcErr.Data != nil && rpcErr.Data.Code == "not_found" { continue }
			t.Fatalf("%s returned rpc error %+v", tc.name, rpcErr)
		}
		payload, _ := json.Marshal(structured)
		if !strings.Contains(string(payload), "fixture-a") { t.Fatalf("%s response does not carry repo scope: %s", tc.name, string(payload)) }
		if strings.Contains(string(payload), "fixture-b") { t.Fatalf("%s leaked fixture-b data into fixture-a response: %s", tc.name, string(payload)) }
		if (tc.name == "search_sources" || tc.name == "list_sources" || tc.name == "recent_changes" || tc.name == "search_chunks" || tc.name == "list_chunks" || tc.name == "source_backlinks") && (structured["limit"] == nil || structured["offset"] == nil) {
			t.Fatalf("%s missing pagination metadata: %#v", tc.name, structured)
		}
	}
}

func TestDesign014MCPArgumentAndTypedErrors(t *testing.T) {
	store := design014Store(t)
	srv, r, w, _ := newPipeServer(service.New(store))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = srv.Serve() }()
	defer func() { _ = r.Close(); wg.Wait() }()

	cases := []struct{ name string; args map[string]any; code int; data string }{
		{"cache_status", map[string]any{}, -32602, "invalid_arguments"},
		{"search_chunks", map[string]any{"repo_id":"fixture-a"}, -32602, "invalid_arguments"},
		{"get_snippet", map[string]any{"repo_id":"fixture-a", "line_start":3, "line_end":1}, -32602, "invalid_arguments"},
		{"create_issue", map[string]any{"repo_id":"fixture-a"}, -32601, "unknown_tool"},
		{"get_source", map[string]any{"repo_id":"fixture-a", "id":"DOES-NOT-EXIST"}, -32000, "not_found"},
	}
	for i, tc := range cases {
		_, rpcErr := design014Call(t, r, w, 100+i, tc.name, tc.args)
		if rpcErr == nil || rpcErr.Code != tc.code || rpcErr.Data == nil || rpcErr.Data.Code != tc.data { t.Fatalf("%s error=%+v want code=%d data=%s", tc.name, rpcErr, tc.code, tc.data) }
	}
}
GOEOF

cat > "$WORKDIR/internal/cli/design014_validation_test.go" <<'GOEOF'
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/mcp"
	"gitcode-mcp/internal/service"
)

type design014PipeConn struct { io.Reader; io.Writer }
func (c design014PipeConn) Close() error { if rc, ok := c.Reader.(io.Closer); ok { _ = rc.Close() }; if wc, ok := c.Writer.(io.Closer); ok { _ = wc.Close() }; return nil }

func design014ReadLine(t *testing.T, r io.Reader) []byte {
	t.Helper()
	buf := make([]byte, 0, 4096)
	for {
		var b [1]byte
		_, err := r.Read(b[:])
		if err != nil { t.Fatal(err) }
		if b[0] == '\n' { return buf }
		buf = append(buf, b[0])
	}
}

func design014Store(t *testing.T) cache.Store {
	t.Helper()
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = store.Close() })
	for _, repo := range []string{"fixture-a", "fixture-b"} {
		if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: repo, Owner: repo + "-owner", Name: repo + "-repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil { t.Fatal(err) }
	}
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	for _, repo := range []string{"fixture-a", "fixture-b"} {
		graphs := []cache.SourceGraph{
			{Source: cache.Source{RepoID: repo, ID: "ISSUE-1", Kind: "issue", Path: repo + "/issues/1.md", Title: "Issue " + repo, Body: "# Issue " + repo + "\nbody " + repo + "\nsee WIKI-1", Status: "open", ContentHash: repo + "-issue-hash", CreatedAt: now, UpdatedAt: now.Add(time.Minute)}, Identities: []cache.Identity{{RepoID: repo, SourceID: "ISSUE-1", AliasType: "issue", Alias: "1", Remote: cache.RemoteAlias{Type: "issue", ID: "1"}}}, SyncStatus: &cache.SyncStatus{RepoID: repo, SourceID: "ISSUE-1", RemoteType: "issue", RemoteID: "1", RemoteRevision: repo + "-rev-issue", Status: "fresh", LastFetchedAt: now}},
			{Source: cache.Source{RepoID: repo, ID: "WIKI-1", Kind: "wiki", Path: repo + "/wiki/home.md", Title: "Wiki " + repo, Body: "# Wiki " + repo + "\nbody " + repo + "\nsee ISSUE-1", Status: "active", ContentHash: repo + "-wiki-hash", CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(2 * time.Minute)}, Identities: []cache.Identity{{RepoID: repo, SourceID: "WIKI-1", AliasType: "wiki", Alias: "Home", Remote: cache.RemoteAlias{Type: "wiki", ID: "Home"}}}, SyncStatus: &cache.SyncStatus{RepoID: repo, SourceID: "WIKI-1", RemoteType: "wiki", RemoteID: "Home", RemoteRevision: repo + "-rev-wiki", Status: "fresh", LastFetchedAt: now}},
		}
		for _, graph := range graphs { if err := store.UpsertSourceGraph(ctx, graph); err != nil { t.Fatal(err) } }
		if err := store.UpsertLink(ctx, cache.Link{RepoID: repo, SourceID: "ISSUE-1", TargetID: "WIKI-1", Kind: "mentions", Text: "see WIKI-1"}); err != nil { t.Fatal(err) }
		if err := store.UpsertLink(ctx, cache.Link{RepoID: repo, SourceID: "WIKI-1", TargetID: "ISSUE-1", Kind: "mentions", Text: "see ISSUE-1"}); err != nil { t.Fatal(err) }
	}
	svc := service.New(store)
	if _, err := svc.Index(ctx, service.OperationRequest{RepoID:"fixture-a", Mode:"full"}); err != nil { t.Fatal(err) }
	if _, err := svc.Index(ctx, service.OperationRequest{RepoID:"fixture-b", Mode:"full"}); err != nil { t.Fatal(err) }
	return store
}

func canonicalJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil { t.Fatal(err) }
	return string(b)
}

func mcpStructured(t *testing.T, rw io.Writer, rr io.Reader, id int, name string, args map[string]any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc":"2.0", "id":id, "method":"tools/call", "params":map[string]any{"name":name, "arguments":args}})
	_, _ = rw.Write(append(b, '\n'))
	line := design014ReadLine(t, rr)
	var resp struct { Error *struct { Code int `json:"code"`; Data *struct { Code string `json:"code"` } `json:"data"` } `json:"error"`; Result struct { StructuredContent map[string]any `json:"structuredContent"` } `json:"result"` }
	if err := json.Unmarshal(line, &resp); err != nil { t.Fatalf("bad MCP response: %s", string(line)) }
	if resp.Error != nil { t.Fatalf("%s returned MCP error %+v", name, resp.Error) }
	return resp.Result.StructuredContent
}

func TestDesign014CLIJSONParityWithMCPStructuredContent(t *testing.T) {
	store := design014Store(t)
	factory := func(context.Context, string) (queryService, func() error, error) { return service.New(store), nil, nil }
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	conn := design014PipeConn{Reader: clientR, Writer: clientW}
	server := mcp.New(serverR, serverW, io.Discard, service.New(store))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = server.Serve() }()
	defer func() { _ = conn.Close(); wg.Wait() }()

	parity := []struct { tool string; mcpArgs map[string]any; cliArgs []string; mcpCanon func(map[string]any) any; cliCanon func(any) any }{
		{"recent_changes", map[string]any{"repo_id":"fixture-a", "limit":2, "offset":0}, []string{"recent", "--repo", "fixture-a", "--limit", "2", "--offset", "0", "--format", "json"}, func(m map[string]any) any { return m["results"] }, func(v any) any { return v }},
		{"link_check", map[string]any{"repo_id":"fixture-a"}, []string{"link-check", "--repo", "fixture-a", "--format", "json"}, func(m map[string]any) any { return m }, func(v any) any { return v }},
		{"cache_status", map[string]any{"repo_id":"fixture-a"}, []string{"cache-status", "--repo", "fixture-a", "--format", "json"}, func(m map[string]any) any { return m }, func(v any) any { return v }},
		{"list_chunks", map[string]any{"repo_id":"fixture-a", "limit":2, "offset":0}, []string{"list-chunks", "--repo", "fixture-a", "--limit", "2", "--offset", "0", "--format", "json"}, func(m map[string]any) any { return m }, func(v any) any { return v }},
		{"source_backlinks", map[string]any{"repo_id":"fixture-a", "id":"ISSUE-1", "limit":2, "offset":0}, []string{"backlinks", "--repo", "fixture-a", "ISSUE-1", "--format", "json"}, func(m map[string]any) any { return m["backlinks"] }, func(v any) any { return v }},
	}
	for i, tc := range parity {
		var stdout, stderr bytes.Buffer
		if code := executeWithFactory(tc.cliArgs, &stdout, &stderr, factory); code != 0 { t.Fatalf("CLI %v code=%d stderr=%s", tc.cliArgs, code, stderr.String()) }
		var cliPayload any
		if err := json.Unmarshal(stdout.Bytes(), &cliPayload); err != nil { t.Fatalf("CLI %v emitted invalid JSON: %s", tc.cliArgs, stdout.String()) }
		mcpPayload := mcpStructured(t, conn, conn, i+1, tc.tool, tc.mcpArgs)
		if canonicalJSON(t, tc.mcpCanon(mcpPayload)) != canonicalJSON(t, tc.cliCanon(cliPayload)) {
			t.Fatalf("%s canonical parity mismatch\nMCP: %s\nCLI: %s", tc.tool, canonicalJSON(t, tc.mcpCanon(mcpPayload)), canonicalJSON(t, tc.cliCanon(cliPayload)))
		}
		joined := canonicalJSON(t, mcpPayload)
		if !strings.Contains(joined, "fixture-a") || strings.Contains(joined, "fixture-b") { t.Fatalf("%s repo scope failure: %s", tc.tool, joined) }
	}
}
GOEOF

GITCODE_LIVE_TEST=0 go test ./internal/mcp ./internal/cli -run 'TestDesign014' -count=1
GITCODE_LIVE_TEST=0 go test ./...
git -C "$ROOT" diff --check
