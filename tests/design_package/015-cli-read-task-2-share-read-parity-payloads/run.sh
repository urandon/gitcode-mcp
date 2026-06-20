#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
WORKDIR="${TMPDIR:-/tmp}/gitcode-mcp-validation-015-$$"
cleanup() {
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

mkdir -p "$WORKDIR"
rsync -a --exclude '.git' --exclude 'ai/artifacts' "$ROOT/" "$WORKDIR/"
cd "$WORKDIR"

cat > "$WORKDIR/internal/cli/design015_validation_test.go" <<'GOEOF'
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/mcp"
	"gitcode-mcp/internal/service"
)

type design015PipeConn struct{ io.Reader; io.Writer }

func (c design015PipeConn) Close() error {
	if rc, ok := c.Reader.(io.Closer); ok {
		_ = rc.Close()
	}
	if wc, ok := c.Writer.(io.Closer); ok {
		_ = wc.Close()
	}
	return nil
}

func design015ReadLine(t *testing.T, r io.Reader) []byte {
	t.Helper()
	buf := make([]byte, 0, 8192)
	for {
		var b [1]byte
		_, err := r.Read(b[:])
		if err != nil {
			t.Fatal(err)
		}
		if b[0] == '\n' {
			return buf
		}
		buf = append(buf, b[0])
	}
}

func design015Store(t *testing.T, includeChunks bool) *cache.SQLiteStore {
	t.Helper()
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	for _, repo := range []string{"fixture-a", "fixture-b"} {
		if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: repo, Owner: repo + "-owner", Name: repo + "-repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		issueBody := "# Issue " + repo + "\ncanonical parity issue body for " + repo + "\nmentions WIKI-Home"
		wikiBody := "# Wiki " + repo + "\ncanonical parity wiki body for " + repo + "\nmentions ISSUE-42"
		graphs := []cache.SourceGraph{
			{Source: cache.Source{RepoID: repo, ID: "ISSUE-42", Kind: "issue", Path: repo + "/issues/42.md", Title: "Issue " + repo, Body: issueBody, Status: "open", Labels: []string{"offline"}, ContentHash: repo + "-issue-hash", CreatedAt: now, UpdatedAt: now.Add(time.Minute)}, Identities: []cache.Identity{{RepoID: repo, SourceID: "ISSUE-42", AliasType: "issue", Alias: "42", Remote: cache.RemoteAlias{Type: "issue", ID: "42"}}}, SyncStatus: &cache.SyncStatus{RepoID: repo, SourceID: "ISSUE-42", RemoteType: "issue", RemoteID: "42", RemoteRevision: repo + "-issue-rev", Status: "fresh", LastFetchedAt: now}},
			{Source: cache.Source{RepoID: repo, ID: "WIKI-Home", Kind: "wiki", Path: repo + "/wiki/home.md", Title: "Wiki " + repo, Body: wikiBody, Status: "active", Labels: []string{"offline"}, ContentHash: repo + "-wiki-hash", CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(2 * time.Minute)}, Identities: []cache.Identity{{RepoID: repo, SourceID: "WIKI-Home", AliasType: "wiki", Alias: "Home", Remote: cache.RemoteAlias{Type: "wiki", ID: "Home"}}}, SyncStatus: &cache.SyncStatus{RepoID: repo, SourceID: "WIKI-Home", RemoteType: "wiki", RemoteID: "Home", RemoteRevision: repo + "-wiki-rev", Status: "fresh", LastFetchedAt: now}},
		}
		for _, graph := range graphs {
			if err := store.UpsertSourceGraph(ctx, graph); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.UpsertLink(ctx, cache.Link{RepoID: repo, SourceID: "ISSUE-42", TargetID: "WIKI-Home", Kind: "mentions", Text: "mentions WIKI-Home"}); err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertLink(ctx, cache.Link{RepoID: repo, SourceID: "WIKI-Home", TargetID: "ISSUE-42", Kind: "mentions", Text: "mentions ISSUE-42"}); err != nil {
			t.Fatal(err)
		}
		if includeChunks {
			chunks := []cache.Chunk{
				{RepoID: repo, ID: repo + "-issue-chunk", SourceID: "ISSUE-42", RecordID: "ISSUE-42", ContentHash: repo + "-issue-hash", ByteStart: 0, ByteEnd: len(issueBody), LineStart: 1, LineEnd: 3, HeadingPath: []string{"Issue " + repo}, Text: issueBody, NormalizedText: strings.ToLower(issueBody), Policy: "heading"},
				{RepoID: repo, ID: repo + "-wiki-chunk", SourceID: "WIKI-Home", RecordID: "WIKI-Home", ContentHash: repo + "-wiki-hash", ByteStart: 0, ByteEnd: len(wikiBody), LineStart: 1, LineEnd: 3, HeadingPath: []string{"Wiki " + repo}, Text: wikiBody, NormalizedText: strings.ToLower(wikiBody), Policy: "heading"},
			}
			for _, chunk := range chunks {
				if _, err := store.UpsertChunk(ctx, chunk); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	return store
}

func design015Factory(store *cache.SQLiteStore) serviceFactory {
	return func(context.Context, string) (queryService, func() error, error) {
		return service.New(store), nil, nil
	}
}

func design015CLI(t *testing.T, store *cache.SQLiteStore, args ...string) map[string]any {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := executeWithFactory(args, &stdout, &stderr, design015Factory(store))
	if code != 0 {
		t.Fatalf("CLI %v code=%d stderr=%s stdout=%s", args, code, stderr.String(), stdout.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("CLI %v emitted invalid JSON: %s err=%v", args, stdout.String(), err)
	}
	return payload
}

func design015MCPClient(t *testing.T, store *cache.SQLiteStore) (design015PipeConn, *sync.WaitGroup) {
	t.Helper()
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	conn := design015PipeConn{Reader: clientR, Writer: clientW}
	server := mcp.New(serverR, serverW, io.Discard, service.New(store))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = server.Serve() }()
	return conn, &wg
}

func design015MCP(t *testing.T, conn design015PipeConn, id int, tool string, args map[string]any) map[string]any {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
	_, _ = conn.Write(append(b, '\n'))
	line := design015ReadLine(t, conn)
	var resp struct {
		Error *struct {
			Code int `json:"code"`
			Data *struct {
				Code string `json:"code"`
			} `json:"data"`
		} `json:"error"`
		Result struct {
			StructuredContent map[string]any `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("bad MCP JSON: %s err=%v", string(line), err)
	}
	if resp.Error != nil {
		t.Fatalf("MCP %s error=%+v", tool, resp.Error)
	}
	return resp.Result.StructuredContent
}

func design015Canonical(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

func design015Subset(v map[string]any, keys ...string) map[string]any {
	out := map[string]any{}
	for _, key := range keys {
		out[key] = v[key]
	}
	return out
}

func design015AssertSame(t *testing.T, name string, got, want any) {
	t.Helper()
	gb := design015Canonical(got)
	wb := design015Canonical(want)
	if gb != wb {
		t.Fatalf("%s canonical JSON mismatch\nMCP=%s\nCLI=%s", name, gb, wb)
	}
}

func TestDesign015MCPCLIReadParityPayloads(t *testing.T) {
	store := design015Store(t, true)
	conn, wg := design015MCPClient(t, store)
	defer func() { _ = conn.Close(); wg.Wait() }()

	cases := []struct {
		name    string
		tool    string
		mcpArgs map[string]any
		cliArgs []string
		canon   func(map[string]any) any
	}{
		{"get_snippet", "get_snippet", map[string]any{"repo_id": "fixture-a", "source_id": "ISSUE-42", "line_start": 1, "line_end": 2}, []string{"get-snippet", "--repo", "fixture-a", "ISSUE-42", "--chunk-id", "fixture-a-issue-chunk", "--format", "json"}, func(v map[string]any) any { return v }},
		{"recent_changes", "recent_changes", map[string]any{"repo_id": "fixture-a", "limit": 2, "offset": 0}, []string{"recent", "--repo", "fixture-a", "--limit", "2", "--offset", "0", "--format", "json"}, func(v map[string]any) any { return v }},
		{"link_check", "link_check", map[string]any{"repo_id": "fixture-a"}, []string{"link-check", "--repo", "fixture-a", "--format", "json"}, func(v map[string]any) any { return v }},
		{"stale_index_report", "stale_index_report", map[string]any{"repo_id": "fixture-a"}, []string{"stale-index", "--repo", "fixture-a", "--format", "json"}, func(v map[string]any) any { return v }},
		{"cache_status", "cache_status", map[string]any{"repo_id": "fixture-a"}, []string{"cache-status", "--repo", "fixture-a", "--format", "json"}, func(v map[string]any) any { return v }},
		{"list_chunks", "list_chunks", map[string]any{"repo_id": "fixture-a", "limit": 2, "offset": 0}, []string{"list-chunks", "--repo", "fixture-a", "--limit", "2", "--offset", "0", "--format", "json"}, func(v map[string]any) any { return v }},
		{"search_chunks", "search_chunks", map[string]any{"repo_id": "fixture-a", "query": "canonical", "limit": 2, "offset": 0}, []string{"list-chunks", "--repo", "fixture-a", "--limit", "2", "--offset", "0", "--format", "json"}, func(v map[string]any) any { return design015Subset(v, "limit", "offset", "total", "warnings") }},
		{"sync_status per-record", "sync_status", map[string]any{"repo_id": "fixture-a", "id": "ISSUE-42"}, []string{"sync-status", "--repo", "fixture-a", "ISSUE-42", "--format", "json"}, func(v map[string]any) any { return v }},
		{"sync_status aggregate", "sync_status", map[string]any{"repo_id": "fixture-a"}, []string{"sync_status", "--repo", "fixture-a", "--format", "json"}, func(v map[string]any) any { return v }},
		{"source_backlinks", "source_backlinks", map[string]any{"repo_id": "fixture-a", "id": "ISSUE-42", "limit": 50, "offset": 0}, []string{"backlinks", "--repo", "fixture-a", "ISSUE-42", "--limit", "50", "--offset", "0", "--format", "json"}, func(v map[string]any) any { return v }},
		{"list_sources", "list_sources", map[string]any{"repo_id": "fixture-a", "limit": 20, "offset": 0}, []string{"list", "--repo", "fixture-a", "--limit", "20", "--offset", "0", "--format", "json"}, func(v map[string]any) any { return v }},
		{"search_sources", "search_sources", map[string]any{"repo_id": "fixture-a", "query": "canonical", "limit": 20, "offset": 0}, []string{"search", "--repo", "fixture-a", "canonical", "--limit", "20", "--offset", "0", "--format", "json"}, func(v map[string]any) any { return v }},
		{"get_source", "get_source", map[string]any{"repo_id": "fixture-a", "id": "ISSUE-42"}, []string{"get", "--repo", "fixture-a", "ISSUE-42", "--format", "json"}, func(v map[string]any) any { return v }},
	}

	for i, tc := range cases {
		mcpPayload := design015MCP(t, conn, i+1, tc.tool, tc.mcpArgs)
		cliPayload := design015CLI(t, store, tc.cliArgs...)
		design015AssertSame(t, tc.name, tc.canon(mcpPayload), tc.canon(cliPayload))
		joined := design015Canonical(mcpPayload)
		if !strings.Contains(joined, "fixture-a") || strings.Contains(joined, "fixture-b") {
			t.Fatalf("%s repo scope failure: %s", tc.name, joined)
		}
	}
}

func TestDesign015MissingIndexWarningsAndTimestampUTC(t *testing.T) {
	store := design015Store(t, false)
	conn, wg := design015MCPClient(t, store)
	defer func() { _ = conn.Close(); wg.Wait() }()

	for i, tc := range []struct {
		name    string
		tool    string
		mcpArgs map[string]any
		cliArgs []string
	}{
		{"stale", "stale_index_report", map[string]any{"repo_id": "fixture-a"}, []string{"stale-index", "--repo", "fixture-a", "--format", "json"}},
		{"chunks", "list_chunks", map[string]any{"repo_id": "fixture-a", "limit": 50, "offset": 0}, []string{"list-chunks", "--repo", "fixture-a", "--limit", "50", "--offset", "0", "--format", "json"}},
	} {
		mcpPayload := design015MCP(t, conn, i+50, tc.tool, tc.mcpArgs)
		cliPayload := design015CLI(t, store, tc.cliArgs...)
		design015AssertSame(t, tc.name, mcpPayload, cliPayload)
		if !strings.Contains(design015Canonical(mcpPayload), "missing_index") {
			t.Fatalf("%s did not surface missing_index warning: %s", tc.name, design015Canonical(mcpPayload))
		}
	}

	snippet := design015CLI(t, store, "get-snippet", "--repo", "fixture-a", "ISSUE-42", "--line-start", "1", "--line-end", "2", "--format", "json")
	if !strings.Contains(design015Canonical(snippet), "missing_index") {
		t.Fatalf("CLI snippet did not surface missing_index warning: %s", design015Canonical(snippet))
	}

	recent := design015CLI(t, store, "recent", "--repo", "fixture-a", "--format", "json")
	results, ok := recent["results"].([]any)
	if !ok || len(results) == 0 {
		t.Fatalf("recent results missing: %#v", recent)
	}
	updatedAt, ok := results[0].(map[string]any)["updated_at"].(string)
	if !ok || !strings.HasSuffix(updatedAt, "Z") {
		t.Fatalf("updated_at is not UTC-normalized RFC3339: %#v", results[0])
	}
}

func TestDesign015TypedErrorParity(t *testing.T) {
	store := design015Store(t, true)
	conn, wg := design015MCPClient(t, store)
	defer func() { _ = conn.Close(); wg.Wait() }()

	type rpcErr struct {
		Code int `json:"code"`
		Data struct {
			Code string `json:"code"`
		} `json:"data"`
	}
	mcpError := func(id int, tool string, args map[string]any) rpcErr {
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
		_, _ = conn.Write(append(b, '\n'))
		line := design015ReadLine(t, conn)
		var resp struct{ Error rpcErr `json:"error"` }
		if err := json.Unmarshal(line, &resp); err != nil {
			t.Fatal(err)
		}
		return resp.Error
	}
	cliError := func(args ...string) map[string]any {
		var stdout, stderr bytes.Buffer
		code := executeWithFactory(args, &stdout, &stderr, design015Factory(store))
		if code == 0 {
			t.Fatalf("CLI %v unexpectedly succeeded", args)
		}
		var payload map[string]any
		if err := json.Unmarshal(stderr.Bytes(), &payload); err != nil {
			t.Fatalf("CLI %v stderr not JSON: %s", args, stderr.String())
		}
		return payload
	}

	checks := []struct {
		name      string
		mcpTool   string
		mcpArgs   map[string]any
		cliArgs   []string
		mcpCode   string
		cliClass  string
	}{
		{"missing repo", "cache_status", map[string]any{}, []string{"cache-status", "--format", "json"}, "invalid_arguments", "validation_failed"},
		{"unknown repo", "cache_status", map[string]any{"repo_id": "missing-repo"}, []string{"cache-status", "--repo", "missing-repo", "--format", "json"}, "not_found", "not_found"},
		{"missing query", "search_sources", map[string]any{"repo_id": "fixture-a"}, []string{"search", "--repo", "fixture-a", "--format", "json"}, "invalid_arguments", "validation_failed"},
		{"not found", "get_source", map[string]any{"repo_id": "fixture-a", "id": "DOES-NOT-EXIST"}, []string{"get", "--repo", "fixture-a", "DOES-NOT-EXIST", "--format", "json"}, "not_found", "not_found"},
	}
	for i, check := range checks {
		me := mcpError(100+i, check.mcpTool, check.mcpArgs)
		ce := cliError(check.cliArgs...)
		if me.Data.Code != check.mcpCode {
			t.Fatalf("%s MCP error code=%s want %s", check.name, me.Data.Code, check.mcpCode)
		}
		if design015CLIErrorClass(ce) != check.cliClass {
			t.Fatalf("%s CLI error class=%#v want %s payload=%#v", check.name, design015CLIErrorClass(ce), check.cliClass, ce)
		}
	}
}

func design015CLIErrorClass(payload map[string]any) string {
	if value, ok := payload["class"].(string); ok {
		return value
	}
	if value, ok := payload["failure_class"].(string); ok {
		if value == "repo_required" || value == "invalid_query" {
			return "validation_failed"
		}
		return value
	}
	return ""
}

func TestDesign015NonParityBoundaryExportDiff(t *testing.T) {
	store := design015Store(t, true)
	conn, wg := design015MCPClient(t, store)
	defer func() { _ = conn.Close(); wg.Wait() }()

	_, _ = conn.Write([]byte(`{"jsonrpc":"2.0","id":500,"method":"tools/list"}` + "\n"))
	line := design015ReadLine(t, conn)
	var toolsResp struct {
		Result struct {
			Tools []struct{ Name string `json:"name"` } `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(line, &toolsResp); err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tool := range toolsResp.Result.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	if !reflect.DeepEqual([]bool{contains015(names, "export_snapshot"), contains015(names, "diff_snapshot")}, []bool{true, true}) {
		t.Fatalf("snapshot MCP tools may exist but are not parity-required; got names=%v", names)
	}
	for _, args := range [][]string{
		{"export", "--repo", "fixture-a", "--format", "json"},
		{"diff", "--repo", "fixture-a", "--format", "json"},
	} {
		payload := design015CLI(t, store, args...)
		if payload["repo_id"] != "fixture-a" {
			t.Fatalf("CLI-only %v missing repo payload: %#v", args, payload)
		}
	}
}

func contains015(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
GOEOF

GITCODE_LIVE_TEST=0 go test ./internal/cli -run 'TestDesign015' -count=1
GITCODE_LIVE_TEST=0 go test ./...
git -C "$ROOT" diff --check
