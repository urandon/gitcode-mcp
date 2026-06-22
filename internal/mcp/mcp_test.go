package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/service"
)

func TestMCPRepoScopedDuplicateAlias(t *testing.T) {
	store := populatedStore(t)
	defer store.Close()
	ctx := context.Background()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-b", Owner: "owner-b", Name: "repo-b", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	for _, repoID := range []string{"fixture-a", "fixture-b"} {
		if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{Source: cache.Source{RepoID: repoID, ID: "ISSUE-42", Kind: "issue", Path: repoID + "/issues/42.md", Title: repoID, Body: repoID + " body", Status: "open", ContentHash: repoID + "42"}, Identities: []cache.Identity{{RepoID: repoID, AliasType: "issue", Alias: "42", Remote: cache.RemoteAlias{Type: "issue", ID: "42"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	svc := service.New(store)
	srv, r, w, stderr := newPipeServer(svc)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = srv.Serve() }()
	call := func(repoID string) service.SourceRecord {
		req := map[string]any{"jsonrpc": "2.0", "id": repoID, "method": "tools/call", "params": map[string]any{"name": "get_source", "arguments": map[string]any{"repo_id": repoID, "id": "issue:42"}}}
		b, _ := json.Marshal(req)
		_, _ = r.Write(append(b, '\n'))
		line, err := readLine(w)
		if err != nil {
			t.Fatalf("read response: %v (stderr: %s)", err, stderr.String())
		}
		var resp response
		if err := json.Unmarshal(line, &resp); err != nil || resp.Error != nil {
			t.Fatalf("response=%s err=%v", string(line), err)
		}
		var tc toolCallResult
		if err := json.Unmarshal(resp.Result, &tc); err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(tc.StructuredContent)
		var record service.SourceRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			t.Fatal(err)
		}
		return record
	}
	a := call("fixture-a")
	b := call("fixture-b")
	if a.RepoID != "fixture-a" || b.RepoID != "fixture-b" || a.Body == b.Body {
		t.Fatalf("scoped MCP records crossed repos: a=%#v b=%#v", a, b)
	}
	r.Close()
	wg.Wait()
}

func TestIntegration(t *testing.T) {
	store := populatedStore(t)
	svc := service.New(store)
	defer store.Close()

	srv, r, w, stderr := newPipeServer(svc)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = srv.Serve()
	}()

	sendAndRecv := func(t *testing.T, req any) json.RawMessage {
		t.Helper()
		b, _ := json.Marshal(req)
		_, _ = r.Write(append(b, '\n'))
		line, err := readLine(w)
		if err != nil {
			t.Fatalf("read response: %v (stderr: %s)", err, stderr.String())
		}
		return line
	}

	initReq := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"}
	initResp := sendAndRecv(t, initReq)
	var initR response
	if err := json.Unmarshal(initResp, &initR); err != nil {
		t.Fatalf("decode initialize response: %v", err)
	}
	if initR.Error != nil {
		t.Fatalf("initialize error: %+v", initR.Error)
	}
	var ir initResult
	if err := json.Unmarshal(initR.Result, &ir); err != nil {
		t.Fatalf("decode init result: %v", err)
	}
	if ir.ProtocolVersion != "2024-11-05" {
		t.Fatalf("protocolVersion = %q, want %q", ir.ProtocolVersion, "2024-11-05")
	}
	if ir.Capabilities.Tools.ListChanged != false {
		t.Fatalf("tools.listChanged = %v, want false", ir.Capabilities.Tools.ListChanged)
	}
	if ir.ServerInfo.Name != "gitcode-mcp" || ir.ServerInfo.Version != "0.1.0" {
		t.Fatalf("serverInfo = %+v", ir.ServerInfo)
	}

	toolsReq := map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/list"}
	toolsResp := sendAndRecv(t, toolsReq)
	var toolsR response
	if err := json.Unmarshal(toolsResp, &toolsR); err != nil {
		t.Fatalf("decode tools/list response: %v", err)
	}
	if toolsR.Error != nil {
		t.Fatalf("tools/list error: %+v", toolsR.Error)
	}
	var tls toolsListResult
	if err := json.Unmarshal(toolsR.Result, &tls); err != nil {
		t.Fatalf("decode tools/list result: %v", err)
	}
	if len(tls.Tools) != 15 {
		t.Fatalf("tools count = %d, want 15: %+v", len(tls.Tools), tls.Tools)
	}
	expectedNames := []string{"search_sources", "get_source", "list_sources", "list_chunks", "search_chunks", "get_snippet", "stale_index_report", "recent_changes", "link_check", "cache_status", "source_backlinks", "resolve_id", "sync_status", "export_snapshot", "diff_snapshot"}
	registry := srv.toolRegistry()
	if len(registry) != len(tls.Tools) {
		t.Fatalf("registry count = %d, listed tools = %d", len(registry), len(tls.Tools))
	}
	seen := map[string]bool{}
	for i, want := range expectedNames {
		if tls.Tools[i].Name != want {
			t.Fatalf("tool[%d].Name = %q, want %q", i, tls.Tools[i].Name, want)
		}
		if seen[tls.Tools[i].Name] {
			t.Fatalf("duplicate tool listed: %s", tls.Tools[i].Name)
		}
		seen[tls.Tools[i].Name] = true
		if _, ok := registry[tls.Tools[i].Name]; !ok {
			t.Fatalf("listed tool %q is not callable", tls.Tools[i].Name)
		}
	}

	resolveReq := map[string]any{
		"jsonrpc": "2.0",
		"id":      3,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "resolve_id",
			"arguments": map[string]any{"repo_id": "fixture-a", "id": "DOC-123"},
		},
	}
	resolveResp := sendAndRecv(t, resolveReq)
	var resolveR response
	if err := json.Unmarshal(resolveResp, &resolveR); err != nil {
		t.Fatalf("decode resolve_id response: %v", err)
	}
	if resolveR.Error != nil {
		t.Fatalf("resolve_id error: %+v", resolveR.Error)
	}
	var tc toolCallResult
	if err := json.Unmarshal(resolveR.Result, &tc); err != nil {
		t.Fatalf("decode resolve_id result: %v", err)
	}
	if len(tc.Content) == 0 {
		t.Fatalf("resolve_id content is empty")
	}
	scRaw, _ := json.Marshal(tc.StructuredContent)
	var resolved service.ResolvedID
	if err := json.Unmarshal(scRaw, &resolved); err != nil {
		t.Fatalf("decode resolve_id structuredContent: %v", err)
	}
	if resolved.ID == "" || resolved.Path == "" {
		t.Fatalf("resolve_id missing fields: %+v", resolved)
	}

	r.Close()
	wg.Wait()
}

func TestSchemasAndResults(t *testing.T) {
	store := populatedStore(t)
	svc := service.New(store)
	defer store.Close()

	srv, r, w, stderr := newPipeServer(svc)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = srv.Serve()
	}()

	send := func(req any) {
		b, _ := json.Marshal(req)
		_, _ = r.Write(append(b, '\n'))
	}
	recv := func() json.RawMessage {
		line, err := readLine(w)
		if err != nil {
			t.Fatalf("read response: %v (stderr: %s)", err, stderr.String())
		}
		return line
	}
	callResponse := func(raw json.RawMessage) (toolCallResult, error) {
		var r response
		if err := json.Unmarshal(raw, &r); err != nil {
			return toolCallResult{}, err
		}
		if r.Error != nil {
			return toolCallResult{}, fmt.Errorf("rpc error: %+v", r.Error)
		}
		var tc toolCallResult
		if err := json.Unmarshal(r.Result, &tc); err != nil {
			return toolCallResult{}, err
		}
		return tc, nil
	}

	send(map[string]any{"jsonrpc": "2.0", "method": "initialize", "id": 1})
	_ = recv()

	t.Run("search_sources defaults", func(t *testing.T) {
		send(map[string]any{
			"jsonrpc": "2.0", "id": 10, "method": "tools/call",
			"params": map[string]any{"name": "search_sources", "arguments": map[string]any{"repo_id": "fixture-a", "query": "backlog"}},
		})
		tc, err := callResponse(recv())
		if err != nil {
			t.Fatal(err)
		}
		var sres service.SearchSourcesResult
		scRaw, _ := json.Marshal(tc.StructuredContent)
		json.Unmarshal(scRaw, &sres)
		if sres.RepoID != "fixture-a" {
			t.Fatalf("repo_id = %q, want fixture-a", sres.RepoID)
		}
		if sres.Limit != 20 {
			t.Fatalf("limit = %d, want 20", sres.Limit)
		}
		if sres.Offset != 0 {
			t.Fatalf("offset = %d, want 0", sres.Offset)
		}
		if len(sres.Results) == 0 || sres.Results[0].ID == "" || sres.Results[0].Path == "" {
			t.Fatalf("search results missing fields: %+v", sres)
		}
	})

	t.Run("list_sources defaults", func(t *testing.T) {
		send(map[string]any{
			"jsonrpc": "2.0", "id": 11, "method": "tools/call",
			"params": map[string]any{"name": "list_sources", "arguments": map[string]any{"repo_id": "fixture-a"}},
		})
		tc, err := callResponse(recv())
		if err != nil {
			t.Fatal(err)
		}
		var lres service.ListSourcesResult
		scRaw, _ := json.Marshal(tc.StructuredContent)
		json.Unmarshal(scRaw, &lres)
		if lres.RepoID != "fixture-a" {
			t.Fatalf("repo_id = %q, want fixture-a", lres.RepoID)
		}
		if lres.Limit != 20 {
			t.Fatalf("limit = %d, want 20", lres.Limit)
		}
		if lres.Offset != 0 {
			t.Fatalf("offset = %d, want 0", lres.Offset)
		}
		if len(lres.Results) == 0 || lres.Results[0].ID == "" {
			t.Fatalf("list results empty or missing id")
		}
	})

	t.Run("get_source", func(t *testing.T) {
		send(map[string]any{
			"jsonrpc": "2.0", "id": 12, "method": "tools/call",
			"params": map[string]any{"name": "get_source", "arguments": map[string]any{"repo_id": "fixture-a", "id": "DOC-123"}},
		})
		tc, err := callResponse(recv())
		if err != nil {
			t.Fatal(err)
		}
		var rec service.SourceRecord
		scRaw, _ := json.Marshal(tc.StructuredContent)
		json.Unmarshal(scRaw, &rec)
		if rec.ID != "DOC-123" || rec.Path == "" || rec.Title == "" {
			t.Fatalf("get_source missing fields: %+v", rec)
		}
	})

	t.Run("recent_changes", func(t *testing.T) {
		send(map[string]any{
			"jsonrpc": "2.0", "id": 13, "method": "tools/call",
			"params": map[string]any{"name": "recent_changes", "arguments": map[string]any{"repo_id": "fixture-a", "limit": 1}},
		})
		tc, err := callResponse(recv())
		if err != nil {
			t.Fatal(err)
		}
		var recent service.RecentChangesResult
		scRaw, _ := json.Marshal(tc.StructuredContent)
		json.Unmarshal(scRaw, &recent)
		if recent.RepoID != "fixture-a" || recent.Limit != 1 || len(recent.Results) != 1 || recent.Results[0].RepoID != "fixture-a" {
			t.Fatalf("recent_changes missing scoped payload: %+v", recent)
		}
	})

	t.Run("link_check", func(t *testing.T) {
		send(map[string]any{
			"jsonrpc": "2.0", "id": 22, "method": "tools/call",
			"params": map[string]any{"name": "link_check", "arguments": map[string]any{"repo_id": "fixture-a", "strict": true}},
		})
		tc, err := callResponse(recv())
		if err != nil {
			t.Fatal(err)
		}
		var link service.LinkCheckResult
		scRaw, _ := json.Marshal(tc.StructuredContent)
		json.Unmarshal(scRaw, &link)
		if link.RepoID != "fixture-a" || link.CheckedCount == 0 || link.BrokenCount != 0 || link.SuggestedAliases == nil {
			t.Fatalf("link_check missing scoped payload: %+v", link)
		}
	})

	t.Run("cache_status", func(t *testing.T) {
		send(map[string]any{
			"jsonrpc": "2.0", "id": 23, "method": "tools/call",
			"params": map[string]any{"name": "cache_status", "arguments": map[string]any{"repo_id": "fixture-a"}},
		})
		tc, err := callResponse(recv())
		if err != nil {
			t.Fatal(err)
		}
		var status service.CacheStatusResult
		scRaw, _ := json.Marshal(tc.StructuredContent)
		json.Unmarshal(scRaw, &status)
		if status.RepoID != "fixture-a" || !status.WALCapable || status.JournalMode == "" || status.IndexFreshnessWarnings == 0 {
			t.Fatalf("cache_status missing fields: %+v", status)
		}
	})

	t.Run("source_backlinks", func(t *testing.T) {
		send(map[string]any{
			"jsonrpc": "2.0", "id": 24, "method": "tools/call",
			"params": map[string]any{"name": "source_backlinks", "arguments": map[string]any{"repo_id": "fixture-a", "id": "DOC-123"}},
		})
		tc, err := callResponse(recv())
		if err != nil {
			t.Fatal(err)
		}
		var blres service.BacklinksResult
		scRaw, _ := json.Marshal(tc.StructuredContent)
		json.Unmarshal(scRaw, &blres)
		if blres.RepoID != "fixture-a" || blres.ID != "DOC-123" {
			t.Fatalf("backlinks scope/id mismatch: %+v", blres)
		}
	})

	t.Run("resolve_id", func(t *testing.T) {
		send(map[string]any{
			"jsonrpc": "2.0", "id": 14, "method": "tools/call",
			"params": map[string]any{"name": "resolve_id", "arguments": map[string]any{"repo_id": "fixture-a", "id": "DOC-123"}},
		})
		tc, err := callResponse(recv())
		if err != nil {
			t.Fatal(err)
		}
		var res service.ResolvedID
		scRaw, _ := json.Marshal(tc.StructuredContent)
		json.Unmarshal(scRaw, &res)
		if res.ID != "DOC-123" || res.Path == "" {
			t.Fatalf("resolve_id missing fields: %+v", res)
		}
	})

	t.Run("sync_status per-record", func(t *testing.T) {
		send(map[string]any{
			"jsonrpc": "2.0", "id": 15, "method": "tools/call",
			"params": map[string]any{"name": "sync_status", "arguments": map[string]any{"repo_id": "fixture-a", "id": "DOC-123"}},
		})
		tc, err := callResponse(recv())
		if err != nil {
			t.Fatal(err)
		}
		var status service.SyncStatusResult
		scRaw, _ := json.Marshal(tc.StructuredContent)
		json.Unmarshal(scRaw, &status)
		if status.SourceID != "DOC-123" {
			t.Fatalf("sync_status source_id = %v, want DOC-123", status.SourceID)
		}
	})

	t.Run("sync_status aggregate", func(t *testing.T) {
		send(map[string]any{
			"jsonrpc": "2.0", "id": 16, "method": "tools/call",
			"params": map[string]any{"name": "sync_status", "arguments": map[string]any{"repo_id": "fixture-a"}},
		})
		tc, err := callResponse(recv())
		if err != nil {
			t.Fatal(err)
		}
		var agg service.SyncStatusSummaryResult
		scRaw, _ := json.Marshal(tc.StructuredContent)
		json.Unmarshal(scRaw, &agg)
		if agg.RepoID != "fixture-a" || agg.CacheEmpty {
			t.Fatalf("aggregate sync_status scope/cache mismatch: %+v", agg)
		}
	})

	t.Run("export_snapshot defaults", func(t *testing.T) {
		send(map[string]any{
			"jsonrpc": "2.0", "id": 17, "method": "tools/call",
			"params": map[string]any{"name": "export_snapshot", "arguments": map[string]any{"repo_id": "fixture-a"}},
		})
		tc, err := callResponse(recv())
		if err != nil {
			t.Fatal(err)
		}
		var exp exportSnapshotSResult
		scRaw, _ := json.Marshal(tc.StructuredContent)
		json.Unmarshal(scRaw, &exp)
		if exp.RepoID != "fixture-a" || exp.Format != "json" {
			t.Fatalf("export scope/format mismatch: %+v", exp)
		}
		if exp.ContentHash == "" || exp.SnapshotID == "" {
			t.Fatalf("export identifiers missing: %+v", exp)
		}
	})

	t.Run("diff_snapshot", func(t *testing.T) {
		send(map[string]any{
			"jsonrpc": "2.0", "id": 18, "method": "tools/call",
			"params": map[string]any{"name": "diff_snapshot", "arguments": map[string]any{"repo_id": "fixture-a", "base_id": "abc", "head_id": "def"}},
		})
		tc, err := callResponse(recv())
		if err != nil {
			t.Fatal(err)
		}
		var diff diffSnapshotSResult
		scRaw, _ := json.Marshal(tc.StructuredContent)
		json.Unmarshal(scRaw, &diff)
		if diff.RepoID != "fixture-a" || diff.BaseID != "abc" || diff.HeadID != "def" {
			t.Fatalf("diff scope/base/head mismatch: %+v", diff)
		}
	})

	t.Run("invalid limit returns -32602", func(t *testing.T) {
		send(map[string]any{
			"jsonrpc": "2.0", "id": 19, "method": "tools/call",
			"params": map[string]any{"name": "search_sources", "arguments": map[string]any{"query": "q", "limit": "abc"}},
		})
		var r response
		json.Unmarshal(recv(), &r)
		if r.Error == nil || r.Error.Code != -32602 {
			t.Fatalf("expected -32602, got %+v", r.Error)
		}
	})

	t.Run("invalid kind returns -32602", func(t *testing.T) {
		send(map[string]any{
			"jsonrpc": "2.0", "id": 20, "method": "tools/call",
			"params": map[string]any{"name": "list_sources", "arguments": map[string]any{"kind": "invalid_kind"}},
		})
		var r response
		json.Unmarshal(recv(), &r)
		if r.Error == nil || r.Error.Code != -32602 {
			t.Fatalf("expected -32602, got %+v", r.Error)
		}
	})

	t.Run("missing query returns -32602", func(t *testing.T) {
		send(map[string]any{
			"jsonrpc": "2.0", "id": 21, "method": "tools/call",
			"params": map[string]any{"name": "search_sources", "arguments": map[string]any{"repo_id": "fixture-a"}},
		})
		var r response
		json.Unmarshal(recv(), &r)
		if r.Error == nil || r.Error.Code != -32602 {
			t.Fatalf("expected -32602, got %+v", r.Error)
		}
	})

	t.Run("missing repo_id returns -32602", func(t *testing.T) {
		send(map[string]any{
			"jsonrpc": "2.0", "id": 25, "method": "tools/call",
			"params": map[string]any{"name": "cache_status", "arguments": map[string]any{}},
		})
		var r response
		json.Unmarshal(recv(), &r)
		if r.Error == nil || r.Error.Code != -32602 || r.Error.Data == nil || r.Error.Data.Code != "invalid_arguments" {
			t.Fatalf("expected invalid_arguments, got %+v", r.Error)
		}
	})

	t.Run("invalid snippet range returns -32602", func(t *testing.T) {
		send(map[string]any{
			"jsonrpc": "2.0", "id": 26, "method": "tools/call",
			"params": map[string]any{"name": "get_snippet", "arguments": map[string]any{"repo_id": "fixture-a", "line_start": 5, "line_end": 2}},
		})
		var r response
		json.Unmarshal(recv(), &r)
		if r.Error == nil || r.Error.Code != -32602 {
			t.Fatalf("expected -32602, got %+v", r.Error)
		}
	})

	t.Run("mutation tools are not registered", func(t *testing.T) {
		for i, name := range []string{"create_issue", "update_issue", "sync", "migrate"} {
			send(map[string]any{
				"jsonrpc": "2.0", "id": 27 + i, "method": "tools/call",
				"params": map[string]any{"name": name, "arguments": map[string]any{"repo_id": "fixture-a"}},
			})
			var r response
			json.Unmarshal(recv(), &r)
			if r.Error == nil || r.Error.Code != -32601 || r.Error.Data == nil || r.Error.Data.Code != "unknown_tool" {
				t.Fatalf("%s: expected unknown_tool, got %+v", name, r.Error)
			}
		}
	})

	r.Close()
	wg.Wait()
}

func TestMCPToolKindSchemaIncludesOnlyGitCodeKinds(t *testing.T) {
	store := populatedStore(t)
	defer store.Close()
	srv := New(io.Reader(strings.NewReader("")), io.Discard, io.Discard, service.New(store))
	registry := srv.toolRegistry()
	for _, name := range []string{"list_sources", "search_sources", "search_chunks"} {
		tool, ok := registry[name]
		if !ok {
			t.Fatalf("tool %s is not registered", name)
		}
		prop, ok := tool.definition.InputSchema.Properties["kind"]
		if !ok {
			t.Fatalf("tool %s missing kind schema", name)
		}
		if !reflect.DeepEqual(prop.Enum, []string{"issue", "wiki"}) {
			t.Fatalf("tool %s kind enum = %#v, want [issue wiki]", name, prop.Enum)
		}
	}
}

func TestMCPReadToolParityOverStdio(t *testing.T) {
	store := populatedStore(t)
	defer store.Close()
	srv, r, w, stderr := newPipeServer(service.New(store))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = srv.Serve()
	}()
	call := func(name string, args map[string]any) toolCallResult {
		t.Helper()
		req := map[string]any{"jsonrpc": "2.0", "id": name, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}}
		b, _ := json.Marshal(req)
		_, _ = r.Write(append(b, '\n'))
		line, err := readLine(w)
		if err != nil {
			t.Fatalf("read %s response: %v (stderr: %s)", name, err, stderr.String())
		}
		return decodeToolCallResult(t, line)
	}
	assertReadToolParity(t, call)
	_ = r.Close()
	wg.Wait()
}

func TestMCPReadToolParityOverHTTPSSE(t *testing.T) {
	store := populatedStore(t)
	defer store.Close()
	transport := NewHTTPSSETransport(NewRPCHandler(service.New(store)), ServerConfig{ReadinessProbe: func(context.Context) Readiness { return Readiness{Ready: true} }, SessionID: func() string { return "parity-session" }})
	server := httptest.NewServer(transport.Handler())
	defer server.Close()
	sseResp, endpoint, events := openSSE(t, server.URL+"/sse")
	defer sseResp.Body.Close()
	call := func(name string, args map[string]any) toolCallResult {
		t.Helper()
		postJSON(t, server.URL+endpoint, map[string]any{"jsonrpc": "2.0", "id": name, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		resp := readSSEMessage(t, events)
		if resp.Error != nil {
			t.Fatalf("%s returned error: %+v", name, resp.Error)
		}
		line, _ := json.Marshal(resp)
		return decodeToolCallResult(t, line)
	}
	assertReadToolParity(t, call)
}

func assertReadToolParity(t *testing.T, call func(string, map[string]any) toolCallResult) {
	t.Helper()
	cacheStatus := call("cache_status", map[string]any{"repo_id": "fixture-a"})
	var status service.CacheStatusResult
	decodeStructured(t, cacheStatus, &status)
	if status.RepoID != "fixture-a" || status.Chunks < 2 || status.SyncEvents < 2 {
		t.Fatalf("cache_status parity mismatch: %+v", status)
	}
	listed := call("list_sources", map[string]any{"repo_id": "fixture-a", "kind": "issue"})
	var sources service.ListSourcesResult
	decodeStructured(t, listed, &sources)
	if len(sources.Results) == 0 || sources.Results[0].Kind != "issue" {
		t.Fatalf("list_sources parity mismatch: %+v", sources)
	}
	got := call("get_source", map[string]any{"repo_id": "fixture-a", "id": "issue:42"})
	var record service.SourceRecord
	decodeStructured(t, got, &record)
	if record.ID != "ISSUE-42" || record.Kind != "issue" {
		t.Fatalf("get_source parity mismatch: %+v", record)
	}
	syncStatus := call("sync_status", map[string]any{"repo_id": "fixture-a"})
	var syncSummary service.SyncStatusSummaryResult
	decodeStructured(t, syncStatus, &syncSummary)
	if syncSummary.RepoID != "fixture-a" || syncSummary.FreshCount == 0 || syncSummary.CacheEmpty {
		t.Fatalf("sync_status parity mismatch: %+v", syncSummary)
	}
	chunks := call("list_chunks", map[string]any{"repo_id": "fixture-a", "source_id": "ISSUE-42"})
	var listedChunks service.ChunkQueryResult
	decodeStructured(t, chunks, &listedChunks)
	if len(listedChunks.Chunks) == 0 || listedChunks.Chunks[0].SourceID != "ISSUE-42" {
		t.Fatalf("list_chunks parity mismatch: %+v", listedChunks)
	}
	searchedChunks := call("search_chunks", map[string]any{"repo_id": "fixture-a", "query": "parity"})
	var chunkSearch service.ChunkQueryResult
	decodeStructured(t, searchedChunks, &chunkSearch)
	if len(chunkSearch.Chunks) == 0 {
		t.Fatalf("search_chunks parity mismatch: %+v", chunkSearch)
	}
	searchedSources := call("search_sources", map[string]any{"repo_id": "fixture-a", "query": "parity", "kind": "wiki"})
	var sourceSearch service.SearchSourcesResult
	decodeStructured(t, searchedSources, &sourceSearch)
	if len(sourceSearch.Results) == 0 || sourceSearch.Results[0].Kind != "wiki" {
		t.Fatalf("search_sources parity mismatch: %+v", sourceSearch)
	}
}

func decodeToolCallResult(t *testing.T, line json.RawMessage) toolCallResult {
	t.Helper()
	var resp response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("tool returned error: %+v", resp.Error)
	}
	var result toolCallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	if len(result.Content) == 0 {
		t.Fatalf("tool result missing content: %+v", result)
	}
	return result
}

func decodeStructured(t *testing.T, result toolCallResult, target any) {
	t.Helper()
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode structured content: %v", err)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestFramingAndErrors(t *testing.T) {
	store := populatedStore(t)
	svc := service.New(store)
	defer store.Close()

	t.Run("invalid JSON returns -32700", func(t *testing.T) {
		srv, r, w, stderr := newPipeServer(svc)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); _ = srv.Serve() }()

		_, _ = r.Write([]byte("not json at all\n"))
		line, _ := readLine(w)
		var resp response
		json.Unmarshal(line, &resp)
		if resp.Error == nil || resp.Error.Code != -32700 {
			t.Fatalf("expected -32700, got %+v (stderr=%s)", resp.Error, stderr.String())
		}

		r.Close()
		wg.Wait()
	})

	t.Run("EOF exits cleanly", func(t *testing.T) {
		srv, r, _, _ := newPipeServer(svc)
		r.Close()
		if err := srv.Serve(); err != nil {
			t.Fatalf("Serve on EOF returns: %v", err)
		}
	})

	t.Run("no-id notification writes no response", func(t *testing.T) {
		srv, r, w, stderr := newPipeServer(svc)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); _ = srv.Serve() }()

		notification := `{"jsonrpc":"2.0","method":"initialized"}`
		_, _ = r.Write([]byte(notification + "\n"))
		_, _ = r.Write([]byte(`{"jsonrpc":"2.0","id":99,"method":"initialize"}` + "\n"))

		line, err := readLine(w)
		if err != nil {
			t.Fatalf("expected response for id=99: %v (stderr=%s)", err, stderr.String())
		}
		var resp response
		json.Unmarshal(line, &resp)
		if resp.ID == nil {
			t.Fatalf("expected id=99 response, got nil-id: %+v", resp)
		}
		rawID, _ := json.Marshal(99)
		if !bytes.Equal([]byte(*resp.ID), rawID) {
			t.Fatalf("id mismatch, got %s want 99", string(*resp.ID))
		}

		r.Close()
		wg.Wait()
	})

	t.Run("request ids preserved on success", func(t *testing.T) {
		srv, r, w, _ := newPipeServer(svc)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); _ = srv.Serve() }()

		_, _ = r.Write([]byte(`{"jsonrpc":"2.0","id":"my-custom-id-123","method":"initialize"}` + "\n"))
		line, _ := readLine(w)
		var resp response
		json.Unmarshal(line, &resp)
		if resp.ID == nil || string(*resp.ID) != `"my-custom-id-123"` {
			t.Fatalf("id not preserved: %s", string(*resp.ID))
		}

		r.Close()
		wg.Wait()
	})

	t.Run("request ids preserved on error", func(t *testing.T) {
		srv, r, w, _ := newPipeServer(svc)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); _ = srv.Serve() }()

		_, _ = r.Write([]byte(`{"jsonrpc":"2.0","id":"err-456","method":"unknown_method"}` + "\n"))
		line, _ := readLine(w)
		var resp response
		json.Unmarshal(line, &resp)
		if resp.Error == nil || resp.Error.Code != -32601 {
			t.Fatalf("expected -32601, got %+v", resp.Error)
		}
		if resp.ID == nil || string(*resp.ID) != `"err-456"` {
			t.Fatalf("id not preserved on error: %s", string(*resp.ID))
		}

		r.Close()
		wg.Wait()
	})

	t.Run("batch requests return -32600", func(t *testing.T) {
		srv, r, w, _ := newPipeServer(svc)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); _ = srv.Serve() }()

		_, _ = r.Write([]byte(`[{"jsonrpc":"2.0","method":"initialize"}]` + "\n"))
		line, err := readLine(w)
		if err != nil {
			t.Fatalf("read response: %v", err)
		}
		var resp response
		json.Unmarshal(line, &resp)
		if resp.Error == nil || resp.Error.Code != -32600 {
			t.Fatalf("expected -32600 for batch, got %+v", resp.Error)
		}

		r.Close()
		wg.Wait()
	})

	t.Run("unknown tool returns -32601", func(t *testing.T) {
		srv, r, w, _ := newPipeServer(svc)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); _ = srv.Serve() }()

		send := func(req map[string]any) {
			b, _ := json.Marshal(req)
			_, _ = r.Write(append(b, '\n'))
		}
		send(map[string]any{
			"jsonrpc": "2.0", "id": 50, "method": "tools/call",
			"params": map[string]any{"name": "nonexistent_tool", "arguments": map[string]any{"repo_id": "fixture-a"}},
		})
		line, _ := readLine(w)
		var resp response
		json.Unmarshal(line, &resp)
		if resp.Error == nil || resp.Error.Code != -32601 {
			t.Fatalf("expected -32601, got %+v", resp.Error)
		}

		r.Close()
		wg.Wait()
	})

	t.Run("initialized with id returns -32601", func(t *testing.T) {
		srv, r, w, _ := newPipeServer(svc)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); _ = srv.Serve() }()

		_, _ = r.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialized"}` + "\n"))
		line, _ := readLine(w)
		var resp response
		json.Unmarshal(line, &resp)
		if resp.Error == nil || resp.Error.Code != -32601 {
			t.Fatalf("expected -32601, got %+v", resp.Error)
		}

		r.Close()
		wg.Wait()
	})

	t.Run("domain error not_found maps to -32000", func(t *testing.T) {
		srv, r, w, _ := newPipeServer(svc)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); _ = srv.Serve() }()

		send := func(req map[string]any) {
			b, _ := json.Marshal(req)
			_, _ = r.Write(append(b, '\n'))
		}
		send(map[string]any{
			"jsonrpc": "2.0", "id": 60, "method": "tools/call",
			"params": map[string]any{"name": "get_source", "arguments": map[string]any{"repo_id": "fixture-a", "id": "NONEXISTENT"}},
		})
		line, _ := readLine(w)
		var resp response
		json.Unmarshal(line, &resp)
		if resp.Error == nil || resp.Error.Code != -32000 {
			t.Fatalf("expected -32000, got %+v", resp.Error)
		}
		if resp.Error.Data == nil || resp.Error.Data.Code != "not_found" {
			t.Fatalf("expected data.code=not_found, got %+v", resp.Error)
		}

		r.Close()
		wg.Wait()
	})

	t.Run("domain error cache_empty maps to -32000", func(t *testing.T) {
		emptyStore, err := cache.NewInMemorySQLiteStore(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer emptyStore.Close()
		if err := emptyStore.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
			t.Fatal(err)
		}
		emptySvc := service.New(emptyStore)

		srv, r, w, _ := newPipeServer(emptySvc)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); _ = srv.Serve() }()

		send := func(req map[string]any) {
			b, _ := json.Marshal(req)
			_, _ = r.Write(append(b, '\n'))
		}
		send(map[string]any{
			"jsonrpc": "2.0", "id": 61, "method": "tools/call",
			"params": map[string]any{"name": "list_sources", "arguments": map[string]any{"repo_id": "fixture-a"}},
		})
		line, _ := readLine(w)
		var resp response
		json.Unmarshal(line, &resp)
		if resp.Error == nil || resp.Error.Code != -32000 {
			t.Fatalf("expected -32000, got %+v", resp.Error)
		}
		if resp.Error.Data == nil || resp.Error.Data.Code != "cache_empty" {
			t.Fatalf("expected data.code=cache_empty, got %+v", resp.Error)
		}

		r.Close()
		wg.Wait()
	})

	t.Run("diagnostics on stderr not stdout", func(t *testing.T) {
		srv, r, w, stderr := newPipeServer(svc)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); _ = srv.Serve() }()

		_, _ = r.Write([]byte(`{"jsonrpc":"2.0","id":99,"method":"tools/call","params":{"name":"get_source","arguments":{"repo_id":"fixture-a","id":"NONEXISTENT"}}}` + "\n"))
		line, _ := readLine(w)
		if !strings.Contains(string(line), "jsonrpc") {
			t.Fatalf("stdout response missing jsonrpc field: %s", string(line))
		}
		if bytes.Contains(line, []byte("mcp:")) {
			t.Fatalf("stdout unexpectedly contains diagnostics: %s", string(line))
		}
		_ = stderr.String()

		r.Close()
		wg.Wait()
	})
}

func newPipeServer(svc serviceInterface) (*Server, io.ReadWriteCloser, io.ReadWriteCloser, *bytes.Buffer) {
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	stderr := &bytes.Buffer{}
	srv := New(serverR, serverW, stderr, svc)
	conn := &pipeConn{Reader: clientR, Writer: clientW}
	return srv, conn, conn, stderr
}

type pipeConn struct {
	io.Reader
	io.Writer
	closed bool
	mu     sync.Mutex
}

func (c *pipeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if rc, ok := c.Reader.(io.ReadCloser); ok {
		rc.Close()
	}
	if wc, ok := c.Writer.(io.WriteCloser); ok {
		wc.Close()
	}
	return nil
}

func openSSE(t *testing.T, url string) (*http.Response, string, <-chan response) {
	t.Helper()
	resp, err := http.Get(url)
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
	if endpoint == "" {
		t.Fatal("missing endpoint event")
	}
	events := make(chan response, 8)
	go func() {
		var data string
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data: ") {
				data = strings.TrimPrefix(line, "data: ")
			}
			if line == "" && data != "" {
				var resp response
				if err := json.Unmarshal([]byte(data), &resp); err == nil {
					events <- resp
				}
				data = ""
			}
		}
		close(events)
	}()
	return resp, endpoint, events
}

func readSSEMessage(t *testing.T, events <-chan response) response {
	t.Helper()
	select {
	case resp, ok := <-events:
		if !ok {
			t.Fatal("sse stream closed")
		}
		return resp
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for sse message")
	}
	return response{}
}

func postJSON(t *testing.T, url string, body any) {
	t.Helper()
	if status := postJSONStatus(t, url, body); status != http.StatusAccepted {
		t.Fatalf("post status=%d", status)
	}
}

func postJSONStatus(t *testing.T, url string, body any) int {
	t.Helper()
	status, _ := postJSONTransportError(t, url, body)
	return status
}

func postJSONTransportError(t *testing.T, url string, body any) (int, transportError) {
	t.Helper()
	b, _ := json.Marshal(body)
	return postRaw(t, url, string(b))
}

func postRaw(t *testing.T, url string, body string) (int, transportError) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var terr transportError
	if resp.StatusCode != http.StatusAccepted {
		_ = json.NewDecoder(resp.Body).Decode(&terr)
	}
	return resp.StatusCode, terr
}

func logWriter(w io.Writer) *log.Logger {
	return log.New(w, "", 0)
}

func readLine(r io.Reader) (json.RawMessage, error) {
	buf := make([]byte, 0, 4096)
	for {
		var b [1]byte
		_, err := r.Read(b[:])
		if err != nil {
			return nil, err
		}
		if b[0] == '\n' {
			return json.RawMessage(buf), nil
		}
		buf = append(buf, b[0])
	}
}

func TestHTTPSSETransportSessionFlow(t *testing.T) {
	store := populatedStore(t)
	defer store.Close()
	var logs bytes.Buffer
	transport := NewHTTPSSETransport(NewRPCHandler(service.New(store)), ServerConfig{
		ReadinessProbe: func(context.Context) Readiness { return Readiness{Ready: true} },
		Logger:         logWriter(&logs),
		RequestID:      func() string { return "generated-request" },
		SessionID:      func() string { return "session-a" },
	})
	server := httptest.NewServer(transport.Handler())
	defer server.Close()

	healthReq, _ := http.NewRequest(http.MethodGet, server.URL+"/health", nil)
	healthReq.Header.Set("X-Request-ID", "health-id")
	healthResp, err := http.DefaultClient.Do(healthReq)
	if err != nil {
		t.Fatal(err)
	}
	if healthResp.StatusCode != http.StatusOK || healthResp.Header.Get("X-Request-ID") != "health-id" {
		t.Fatalf("health status=%d request_id=%q", healthResp.StatusCode, healthResp.Header.Get("X-Request-ID"))
	}
	_ = healthResp.Body.Close()

	readyResp, err := http.Get(server.URL + "/ready")
	if err != nil {
		t.Fatal(err)
	}
	if readyResp.StatusCode != http.StatusOK || readyResp.Header.Get("X-Request-ID") != "generated-request" {
		t.Fatalf("ready status=%d request_id=%q", readyResp.StatusCode, readyResp.Header.Get("X-Request-ID"))
	}
	_ = readyResp.Body.Close()

	badMethodResp, err := http.Post(server.URL+"/sse", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	if badMethodResp.StatusCode != http.StatusMethodNotAllowed || badMethodResp.Header.Get("X-Request-ID") != "generated-request" {
		t.Fatalf("sse bad method status=%d request_id=%q", badMethodResp.StatusCode, badMethodResp.Header.Get("X-Request-ID"))
	}
	_ = badMethodResp.Body.Close()

	sseResp, endpoint, events := openSSE(t, server.URL+"/sse")
	defer sseResp.Body.Close()
	if endpoint != "/message?session_id=session-a" {
		t.Fatalf("endpoint = %q", endpoint)
	}

	postJSON(t, server.URL+endpoint, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	resp := readSSEMessage(t, events)
	if resp.ID == nil || string(*resp.ID) != "1" || resp.Error != nil {
		t.Fatalf("initialize response = %+v", resp)
	}
	var init initResult
	if err := json.Unmarshal(resp.Result, &init); err != nil || init.ServerInfo.Name != "gitcode-mcp" {
		t.Fatalf("init result=%s err=%v", string(resp.Result), err)
	}

	postJSON(t, server.URL+endpoint, map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": "get_source", "arguments": map[string]any{"repo_id": "fixture-a", "id": "DOC-123"}}})
	toolResp := readSSEMessage(t, events)
	if toolResp.ID == nil || string(*toolResp.ID) != "2" || toolResp.Error != nil {
		t.Fatalf("tool response = %+v", toolResp)
	}
	if !strings.Contains(logs.String(), "request_id=health-id") {
		t.Fatalf("logs missing request id: %q", logs.String())
	}
}

func TestMCPRuntimeLockContentionErrorMapping(t *testing.T) {
	started := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	lockErr := cache.ErrLockContention{Path: "redacted.lock", Operation: "sync-index", RepoID: "fixture-a", StartedAt: started, PID: 42, CachePath: ":memory:"}
	store := populatedStore(t)
	defer store.Close()
	svc := &lockContentionService{serviceInterface: service.New(store), err: lockErr}

	srv, r, w, stderr := newPipeServer(svc)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = srv.Serve() }()
	req := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "list_sources", "arguments": map[string]any{"repo_id": "fixture-a"}}}
	b, _ := json.Marshal(req)
	_, _ = r.Write(append(b, '\n'))
	line, err := readLine(w)
	if err != nil {
		t.Fatalf("read response: %v (stderr: %s)", err, stderr.String())
	}
	var resp response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error == nil || resp.Error.Data == nil || resp.Error.Data.Code != "cache_owned" || resp.Error.Data.Operation != "sync-index" || resp.Error.Data.RepoID != "fixture-a" || resp.Error.Data.PID != 42 || resp.Error.Data.StartedAt == "" || resp.Error.Data.CachePath != ":memory:" {
		t.Fatalf("lock error response = %+v", resp.Error)
	}
	_ = r.Close()
	wg.Wait()
}

func TestHTTPSSEReadinessLockContention(t *testing.T) {
	lockErr := cache.ErrLockContention{Path: "redacted.lock", Operation: "migration", StartedAt: time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC), PID: 42, CachePath: ":memory:"}
	store := populatedStore(t)
	defer store.Close()
	transport := NewHTTPSSETransport(NewRPCHandler(service.New(store)), ServerConfig{ReadinessProbe: func(context.Context) Readiness { return LockContentionReadiness(lockErr) }})
	server := httptest.NewServer(transport.Handler())
	defer server.Close()

	healthResp, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	if healthResp.StatusCode != http.StatusOK {
		t.Fatalf("health status=%d", healthResp.StatusCode)
	}
	_ = healthResp.Body.Close()

	readyResp, err := http.Get(server.URL + "/ready")
	if err != nil {
		t.Fatal(err)
	}
	defer readyResp.Body.Close()
	var ready Readiness
	if err := json.NewDecoder(readyResp.Body).Decode(&ready); err != nil {
		t.Fatal(err)
	}
	if readyResp.StatusCode != http.StatusServiceUnavailable || ready.Code != "migration_blocked" || ready.ErrorData == nil || ready.ErrorData.Code != "migration_blocked" {
		t.Fatalf("ready status=%d body=%+v", readyResp.StatusCode, ready)
	}
}

func TestHTTPSSECancelledSessionDoesNotBlockOtherClient(t *testing.T) {
	store := populatedStore(t)
	defer store.Close()
	ids := []string{"session-cancel", "session-live"}
	transport := NewHTTPSSETransport(NewRPCHandler(service.New(store)), ServerConfig{SessionID: func() string {
		id := ids[0]
		ids = ids[1:]
		return id
	}})
	server := httptest.NewServer(transport.Handler())
	defer server.Close()

	sseCancel, endpointCancel, eventsCancel := openSSE(t, server.URL+"/sse")
	sseLive, endpointLive, eventsLive := openSSE(t, server.URL+"/sse")
	defer sseLive.Body.Close()
	_ = sseCancel.Body.Close()
	time.Sleep(20 * time.Millisecond)
	closed := postJSONStatus(t, server.URL+endpointCancel, map[string]any{"jsonrpc": "2.0", "id": "cancelled", "method": "tools/call", "params": map[string]any{"name": "list_sources", "arguments": map[string]any{"repo_id": "fixture-a"}}})
	if closed != http.StatusNotFound {
		t.Fatalf("closed post status=%d", closed)
	}
	select {
	case resp, ok := <-eventsCancel:
		if ok {
			t.Fatalf("cancelled session received response: %+v", resp)
		}
	default:
	}

	postJSON(t, server.URL+endpointLive, map[string]any{"jsonrpc": "2.0", "id": "live", "method": "tools/call", "params": map[string]any{"name": "list_sources", "arguments": map[string]any{"repo_id": "fixture-a"}}})
	liveResp := readSSEMessage(t, eventsLive)
	if liveResp.ID == nil || string(*liveResp.ID) != `"live"` || liveResp.Error != nil {
		t.Fatalf("live response = %+v", liveResp)
	}
}

func TestHTTPSSETransportSessionErrorsAndMultiClient(t *testing.T) {
	store := populatedStore(t)
	defer store.Close()
	ids := []string{"session-a", "session-b"}
	transport := NewHTTPSSETransport(NewRPCHandler(service.New(store)), ServerConfig{
		SessionID: func() string {
			id := ids[0]
			ids = ids[1:]
			return id
		},
	})
	server := httptest.NewServer(transport.Handler())
	defer server.Close()

	missing, missingErr := postJSONTransportError(t, server.URL+"/message", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	if missing != http.StatusBadRequest || missingErr.Error.Code != "missing_session" {
		t.Fatalf("missing session status=%d error=%+v", missing, missingErr)
	}
	unknown, unknownErr := postJSONTransportError(t, server.URL+"/message?session_id=missing", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"})
	if unknown != http.StatusNotFound || unknownErr.Error.Code != "unknown_session" {
		t.Fatalf("unknown session status=%d error=%+v", unknown, unknownErr)
	}

	sseA, endpointA, eventsA := openSSE(t, server.URL+"/sse")
	defer sseA.Body.Close()
	sseB, endpointB, eventsB := openSSE(t, server.URL+"/sse")
	defer sseB.Body.Close()
	if endpointA == endpointB {
		t.Fatalf("duplicate endpoints: %q", endpointA)
	}
	duplicateBodyStatus, duplicateBodyErr := postRaw(t, server.URL+endpointA, `{"jsonrpc":"2.0","id":7,"method":"initialize"} {"jsonrpc":"2.0","id":8,"method":"initialize"}`)
	if duplicateBodyStatus != http.StatusBadRequest || duplicateBodyErr.Error.Code != "invalid_json" {
		t.Fatalf("duplicate body status=%d error=%+v", duplicateBodyStatus, duplicateBodyErr)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		postJSON(t, server.URL+endpointA, map[string]any{"jsonrpc": "2.0", "id": "a", "method": "tools/call", "params": map[string]any{"name": "list_sources", "arguments": map[string]any{"repo_id": "fixture-a"}}})
	}()
	go func() {
		defer wg.Done()
		postJSON(t, server.URL+endpointB, map[string]any{"jsonrpc": "2.0", "id": "b", "method": "tools/call", "params": map[string]any{"name": "tools/list"}})
	}()
	wg.Wait()
	respA := readSSEMessage(t, eventsA)
	respB := readSSEMessage(t, eventsB)
	if respA.ID == nil || string(*respA.ID) != `"a"` {
		t.Fatalf("session a response crossed: %+v", respA)
	}
	if respB.ID == nil || string(*respB.ID) != `"b"` {
		t.Fatalf("session b response crossed: %+v", respB)
	}

	_ = sseA.Body.Close()
	time.Sleep(20 * time.Millisecond)
	closed := postJSONStatus(t, server.URL+endpointA, map[string]any{"jsonrpc": "2.0", "id": 3, "method": "initialize"})
	if closed != http.StatusNotFound {
		t.Fatalf("closed session status=%d", closed)
	}
}

type lockContentionService struct {
	serviceInterface
	err error
}

func (s *lockContentionService) ListSources(context.Context, service.ListSourcesRequest) (service.ListSourcesResult, error) {
	return service.ListSourcesResult{}, s.err
}

func populatedStore(t *testing.T) cache.Store {
	t.Helper()
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	graphs := []cache.SourceGraph{
		{
			Source:     cache.Source{RepoID: "fixture-a", ID: "DOC-123", Kind: "doc", Path: "docs/backlog.md", Title: "Backlog", Body: "backlog overview\nready task details\nmore context", Status: "active", Labels: []string{"knowledge"}, ContentHash: "h1", CreatedAt: now, UpdatedAt: now},
			SyncStatus: &cache.SyncStatus{RepoID: "fixture-a", SourceID: "DOC-123", RemoteType: "issue", RemoteID: "100", RemoteRevision: "r1", Status: "fresh", LastFetchedAt: now},
			Chunks:     []cache.Chunk{{RepoID: "fixture-a", ID: "chunk-doc-123", SourceID: "DOC-123", RecordID: "DOC-123", ContentHash: "h1", ByteStart: 0, ByteEnd: 16, LineStart: 1, LineEnd: 1, Text: "backlog overview", NormalizedText: "backlog overview"}},
		},
		{
			Source:     cache.Source{RepoID: "fixture-a", ID: "ISSUE-42", Kind: "issue", Path: "issues/42.md", Title: "Live-shaped issue", Body: "parity issue body for connected client reads", Status: "open", ContentHash: "h-issue-42", CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute)},
			SyncStatus: &cache.SyncStatus{RepoID: "fixture-a", SourceID: "ISSUE-42", RemoteType: "issue", RemoteID: "42", RemoteRevision: "issue-r42", Status: "fresh", LastFetchedAt: now.Add(time.Minute)},
			Identities: []cache.Identity{{RepoID: "fixture-a", AliasType: "issue", Alias: "42", Remote: cache.RemoteAlias{Type: "issue", ID: "42"}}},
			Chunks:     []cache.Chunk{{RepoID: "fixture-a", ID: "chunk-issue-42", SourceID: "ISSUE-42", RecordID: "ISSUE-42", ContentHash: "h-issue-42", ByteStart: 0, ByteEnd: 18, LineStart: 1, LineEnd: 1, Text: "parity issue body", NormalizedText: "parity issue body"}},
		},
		{
			Source:     cache.Source{RepoID: "fixture-a", ID: "WIKI-7", Kind: "wiki", Path: "wiki/live-readiness.md", Title: "Live Readiness Wiki", Body: "parity wiki body for connected client reads", Status: "published", ContentHash: "h-wiki-7", CreatedAt: now.Add(2 * time.Minute), UpdatedAt: now.Add(2 * time.Minute)},
			SyncStatus: &cache.SyncStatus{RepoID: "fixture-a", SourceID: "WIKI-7", RemoteType: "wiki", RemoteID: "7", RemoteRevision: "wiki-r7", Status: "fresh", LastFetchedAt: now.Add(2 * time.Minute)},
			Identities: []cache.Identity{{RepoID: "fixture-a", AliasType: "wiki", Alias: "7", Remote: cache.RemoteAlias{Type: "wiki", ID: "7"}}},
			Chunks:     []cache.Chunk{{RepoID: "fixture-a", ID: "chunk-wiki-7", SourceID: "WIKI-7", RecordID: "WIKI-7", ContentHash: "h-wiki-7", ByteStart: 0, ByteEnd: 17, LineStart: 1, LineEnd: 1, Text: "parity wiki body", NormalizedText: "parity wiki body"}},
		},
		{
			Source: cache.Source{RepoID: "fixture-a", ID: "TASK-1", Kind: "task", Path: "project/tasks/task-1.md", Title: "Ready Task", Body: "task references DOC-123", Status: "ready", ContentHash: "h2", CreatedAt: now.Add(3 * time.Minute), UpdatedAt: now.Add(3 * time.Minute)},
			Links:  []cache.Link{{RepoID: "fixture-a", SourceID: "TASK-1", TargetID: "DOC-123", Kind: "mentions", Text: "see DOC-123"}},
		},
	}
	for _, graph := range graphs {
		if err := store.UpsertSourceGraph(context.Background(), graph); err != nil {
			t.Fatal(err)
		}
	}
	for _, event := range []cache.SyncEvent{
		{RepoID: "fixture-a", ID: "sync-doc-123", SourceID: "DOC-123", RemoteType: "issue", RemoteID: "100", RemoteRevision: "r1", Status: "succeeded", IdempotencyKey: "sync-doc-123", CreatedAt: now, StartedAt: now, CompletedAt: now},
		{RepoID: "fixture-a", ID: "sync-issue-42", SourceID: "ISSUE-42", RemoteType: "issue", RemoteID: "42", RemoteRevision: "issue-r42", Status: "succeeded", IdempotencyKey: "sync-issue-42", CreatedAt: now.Add(time.Minute), StartedAt: now.Add(time.Minute), CompletedAt: now.Add(time.Minute)},
		{RepoID: "fixture-a", ID: "sync-wiki-7", SourceID: "WIKI-7", RemoteType: "wiki", RemoteID: "7", RemoteRevision: "wiki-r7", Status: "succeeded", IdempotencyKey: "sync-wiki-7", CreatedAt: now.Add(2 * time.Minute), StartedAt: now.Add(2 * time.Minute), CompletedAt: now.Add(2 * time.Minute)},
	} {
		if err := store.RecordSyncEvent(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	return store
}
