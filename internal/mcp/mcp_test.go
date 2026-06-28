package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/auth"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/diagnostics"
	"gitcode-mcp/internal/gitcode"
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

func TestMCPErrorOutputCanonicalFailureClass(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "SCN-MCP-ERROR-OUTPUT-401", err: service.ErrSyncFailure{Mode: "live_auth_failure", Target: "issue:*", Cause: gitcode.ErrAuthExpired{Endpoint: "/api/v5/repos/owner/repo/issues", Status: http.StatusUnauthorized}}, want: string(diagnostics.CodeAPIFailure)},
		{name: "SCN-MCP-ERROR-OUTPUT-400", err: gitcode.ErrAPIValidation{Endpoint: "/api/v5/repos/owner/repo/issues", Status: http.StatusBadRequest}, want: string(diagnostics.CodeAPIFailure)},
		{name: "SCN-MCP-ERROR-OUTPUT-404", err: service.ErrSyncFailure{Mode: "remote_not_found", Target: "issue:404", Cause: gitcode.ErrRemoteNotFound{Endpoint: "/api/v5/repos/owner/repo/issues/404", Alias: "issue:404"}}, want: string(diagnostics.CodeAPIFailure)},
		{name: "SCN-MCP-ERROR-OUTPUT-409", err: service.ErrSyncFailure{Mode: "conflict", Target: "issue:7", Cause: gitcode.ErrConflict{Endpoint: "/api/v5/repos/owner/repo/issues/7", Status: http.StatusConflict}}, want: string(diagnostics.CodeAPIFailure)},
		{name: "SCN-MCP-ERROR-OUTPUT-413", err: service.ErrSyncFailure{Mode: "payload_too_large", Target: "issue:*", PayloadSource: "remote_status", Cause: gitcode.ErrPayloadTooLarge{Endpoint: "/api/v5/repos/owner/repo/issues", Limit: 5, Size: 6, Source: "remote_status"}}, want: string(diagnostics.CodeAPIFailure)},
		{name: "SCN-MCP-ERROR-OUTPUT-429", err: service.ErrSyncFailure{Mode: "rate_limited", Target: "issue:*", Cause: gitcode.ErrRateLimited{Endpoint: "/api/v5/repos/owner/repo/issues", Attempts: 1}}, want: string(diagnostics.CodeAPIFailure)},
		{name: "SCN-MCP-ERROR-OUTPUT-MALFORMED-JSON", err: gitcode.ErrPartialResponse{Endpoint: "/api/v5/repos/owner/repo/issues", Message: "malformed JSON"}, want: string(diagnostics.CodeSchemaDecode)},
		{name: "SCN-MCP-ERROR-OUTPUT-SCHEMA-MISMATCH", err: &gitcode.ErrSchemaDecode{Field: "number", Message: "number is required"}, want: string(diagnostics.CodeSchemaDecode)},
		{name: "SCN-MCP-ERROR-OUTPUT-PARTIAL-RESPONSE", err: service.ErrSyncFailure{Mode: "partial_response", Target: "issue:*", Cause: gitcode.ErrPartialResponse{Endpoint: "/api/v5/repos/owner/repo/issues", Expected: 10, Got: 5}}, want: string(diagnostics.CodeSchemaDecode)},
		{name: "SCN-MCP-ERROR-OUTPUT-LOCAL-BODY-LIMIT", err: service.ErrWriteFailure{Code: "write_provider_error", PayloadSource: "local_body_limit", Cause: gitcode.ErrPayloadTooLarge{Endpoint: "/api/v5/repos/owner/repo/issues", Limit: 5, Size: 6, Source: "local_body_limit"}}, want: string(diagnostics.CodeSchemaDecode)},
		{name: "SCN-MCP-ERROR-OUTPUT-TIMEOUT", err: service.ErrSyncFailure{Mode: "network_timeout", Target: "issue:*", Cause: gitcode.ErrNetworkUnavailable{Endpoint: "/api/v5/repos/owner/repo/issues", Attempts: 1}}, want: string(diagnostics.CodeLiveTransportFailure)},
		{name: "SCN-MCP-ERROR-OUTPUT-500", err: gitcode.ErrNetworkUnavailable{Endpoint: "/api/v5/repos/owner/repo/issues", Status: http.StatusInternalServerError, Attempts: 1}, want: string(diagnostics.CodeLiveTransportFailure)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			id := json.RawMessage(`"SCN-MCP-ERROR-OUTPUT-01"`)
			srv := &Server{writer: &out, stderr: io.Discard}
			srv.writeDomainError(&id, tt.err)
			var resp response
			if err := json.Unmarshal(bytesTrimSpace(out.Bytes()), &resp); err != nil {
				t.Fatalf("decode response: %v body=%q", err, out.String())
			}
			if resp.Error == nil || resp.Error.Data == nil {
				t.Fatalf("response missing error data: %#v", resp)
			}
			if resp.Error.Data.FailureClass != tt.want {
				t.Fatalf("failure_class=%q want %s body=%q", resp.Error.Data.FailureClass, tt.want, out.String())
			}
		})
	}
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
	expectedNames := expectedToolNamesForAccess(ToolAccessRead)
	if len(tls.Tools) != len(expectedNames) {
		t.Fatalf("tools count = %d, want %d: %+v", len(tls.Tools), len(expectedNames), tls.Tools)
	}
	registry := srv.toolRegistry()
	if len(registry) != len(toolListOrder) {
		t.Fatalf("registry count = %d, want %d", len(registry), len(toolListOrder))
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

func TestMCPBlockedWriteBoundary(t *testing.T) {
	t.Setenv("GITCODE_TOKEN", "")
	providerCalls := 0
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls++
		w.WriteHeader(http.StatusTeapot)
	}))
	defer provider.Close()

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

	send := func(req map[string]any) {
		t.Helper()
		b, _ := json.Marshal(req)
		_, _ = r.Write(append(b, '\n'))
	}

	assertError := func(name string, raw json.RawMessage) {
		t.Helper()
		var resp response
		if err := json.Unmarshal(raw, &resp); err != nil {
			t.Fatalf("%s: decode response: %v", name, err)
		}
		if resp.Error == nil {
			t.Fatalf("%s: expected error, got success", name)
		}
		if resp.Error.Code != -32601 {
			t.Fatalf("%s: error.code = %d, want -32601", name, resp.Error.Code)
		}
		if resp.Error.Data == nil {
			t.Fatalf("%s: error.data is nil", name)
		}
		if resp.Error.Data.Code != "unsupported_capability" {
			t.Fatalf("%s: errorData.Code = %q, want unsupported_capability", name, resp.Error.Data.Code)
		}
	}

	// tools/list remains read/cache-only (no write tools advertised)
	t.Run("tools/list unchanged", func(t *testing.T) {
		send(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
		line, err := readLine(w)
		if err != nil {
			t.Fatalf("read response: %v (stderr: %s)", err, stderr.String())
		}
		var resp response
		if err := json.Unmarshal(line, &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Error != nil {
			t.Fatalf("tools/list error: %+v", resp.Error)
		}
		var tls toolsListResult
		if err := json.Unmarshal(resp.Result, &tls); err != nil {
			t.Fatal(err)
		}
		blocked := map[string]bool{
			"sync_live": false, "index_repo": false,
			"add_issue_comment": false, "update_issue": false,
			"create_pr": false, "update_pr": false, "add_pr_comment": false, "link_pr_issue": false,
			"create_issue": false, "add_comment": false,
			"create_page": false, "update_page": false,
			"create-issue": false, "update-issue": false, "add-label": false,
			"create-page": false, "update-page": false,
		}
		for _, tool := range tls.Tools {
			if _, ok := blocked[tool.Name]; ok {
				t.Fatalf("blocked write tool %q appears in tools/list", tool.Name)
			}
		}
	})

	// scenario blocked-write-canonical-5
	t.Run("blocked write canonical 5", func(t *testing.T) {
		canonical := []string{"create_issue", "add_comment", "create_page", "update_page"}

		for i, name := range canonical {
			send(map[string]any{
				"jsonrpc": "2.0",
				"id":      100 + i,
				"method":  "tools/call",
				"params":  map[string]any{"name": name, "arguments": map[string]any{"repo_id": "fixture-a"}},
			})
			line, err := readLine(w)
			if err != nil {
				t.Fatalf("%s: read response: %v", name, err)
			}
			assertError(name, line)
		}
	})

	if providerCalls != 0 {
		t.Fatalf("unsupported write tools made %d provider calls", providerCalls)
	}

	// read tool parity — existing read tools still work
	t.Run("read tool parity", func(t *testing.T) {
		readCalls := []struct {
			name string
			args map[string]any
		}{
			{"search_sources", map[string]any{"repo_id": "fixture-a", "query": "parity"}},
			{"get_source", map[string]any{"repo_id": "fixture-a", "id": "DOC-123"}},
			{"list_sources", map[string]any{"repo_id": "fixture-a"}},
		}
		for _, rc := range readCalls {
			send(map[string]any{
				"jsonrpc": "2.0",
				"id":      rc.name,
				"method":  "tools/call",
				"params":  map[string]any{"name": rc.name, "arguments": rc.args},
			})
			line, err := readLine(w)
			if err != nil {
				t.Fatalf("%s: read response: %v (stderr: %s)", rc.name, err, stderr.String())
			}
			var resp response
			if err := json.Unmarshal(line, &resp); err != nil {
				t.Fatalf("%s: decode response: %v", rc.name, err)
			}
			if resp.Error != nil {
				t.Fatalf("%s: unexpected error: %+v", rc.name, resp.Error)
			}
		}
	})

	r.Close()
	wg.Wait()
}

func TestMCPToolAccessPolicy(t *testing.T) {
	t.Run("read mode filters and blocks write tools before validation", func(t *testing.T) {
		spy := &writeLifecycleSpyService{}
		srv, r, w, stderr := newPipeServer(spy)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); _ = srv.Serve() }()

		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "list", "method": "tools/list"})
		_, _ = r.Write(append(b, '\n'))
		line, err := readLine(w)
		if err != nil {
			t.Fatalf("read tools/list response: %v (stderr: %s)", err, stderr.String())
		}
		var listResp response
		if err := json.Unmarshal(line, &listResp); err != nil || listResp.Error != nil {
			t.Fatalf("tools/list response=%s err=%v", string(line), err)
		}
		var tls toolsListResult
		if err := json.Unmarshal(listResp.Result, &tls); err != nil {
			t.Fatal(err)
		}
		for _, tool := range tls.Tools {
			if writeToolNames[tool.Name] {
				t.Fatalf("read mode listed write tool %q", tool.Name)
			}
		}

		b, _ = json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "blocked", "method": "tools/call", "params": map[string]any{"name": "add_pr_comment", "arguments": map[string]any{}}})
		_, _ = r.Write(append(b, '\n'))
		line, err = readLine(w)
		if err != nil {
			t.Fatalf("read blocked call response: %v (stderr: %s)", err, stderr.String())
		}
		var callResp response
		if err := json.Unmarshal(line, &callResp); err != nil {
			t.Fatalf("decode blocked call response: %v", err)
		}
		if callResp.Error == nil || callResp.Error.Data == nil {
			t.Fatalf("blocked call missing error data: %#v", callResp)
		}
		if callResp.Error.Data.Code != "tool_disabled_by_policy" || callResp.Error.Data.AccessMode != "read" || callResp.Error.Code != -32000 {
			t.Fatalf("unexpected blocked call error: %#v", callResp.Error)
		}
		if len(spy.calls) != 0 {
			t.Fatalf("disabled write tool reached service: %#v", spy.calls)
		}

		_ = r.Close()
		wg.Wait()
	})

	t.Run("write mode lists write tools", func(t *testing.T) {
		srv, r, w, stderr := newPipeServerWithToolAccess(&writeLifecycleSpyService{}, ToolAccessWrite)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); _ = srv.Serve() }()

		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "list", "method": "tools/list"})
		_, _ = r.Write(append(b, '\n'))
		line, err := readLine(w)
		if err != nil {
			t.Fatalf("read tools/list response: %v (stderr: %s)", err, stderr.String())
		}
		var resp response
		if err := json.Unmarshal(line, &resp); err != nil || resp.Error != nil {
			t.Fatalf("tools/list response=%s err=%v", string(line), err)
		}
		var tls toolsListResult
		if err := json.Unmarshal(resp.Result, &tls); err != nil {
			t.Fatal(err)
		}
		listed := map[string]bool{}
		for _, tool := range tls.Tools {
			listed[tool.Name] = true
		}
		for name := range writeToolNames {
			if !listed[name] {
				t.Fatalf("write mode missing write tool %q", name)
			}
		}

		_ = r.Close()
		wg.Wait()
	})
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
		for i, name := range []string{"sync", "migrate"} {
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
	srv := New(io.Reader(strings.NewReader("")), io.Discard, io.Discard, service.New(store), nil)
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
		if !reflect.DeepEqual(prop.Enum, []string{"issue", "wiki", "pull_request", "pr_comment"}) {
			t.Fatalf("tool %s kind enum = %#v, want [issue wiki pull_request pr_comment]", name, prop.Enum)
		}
	}
}

func TestMCPRegistryIsNameBased(t *testing.T) {
	originalDefs := append([]toolDefinition(nil), toolDefs...)
	defer func() { toolDefs = originalDefs }()
	for i, j := 0, len(toolDefs)-1; i < j; i, j = i+1, j-1 {
		toolDefs[i], toolDefs[j] = toolDefs[j], toolDefs[i]
	}
	toolDefs = append([]toolDefinition{{Name: "appended_lifecycle_probe", Description: "Probe appended lifecycle tool definition.", InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{}}}}, toolDefs...)

	store := populatedStore(t)
	defer store.Close()
	srv := New(io.Reader(strings.NewReader("")), io.Discard, io.Discard, service.New(store), nil)
	registry := srv.toolRegistry()
	for _, name := range []string{"search_sources", "get_source", "list_sources", "resolve_id", "repo_status", "sync_live", "index_repo", "doctor"} {
		tool, ok := registry[name]
		if !ok {
			t.Fatalf("tool %s is not registered", name)
		}
		if tool.definition.Name != name {
			t.Fatalf("registry[%q].definition.Name = %q", name, tool.definition.Name)
		}
	}

	var out bytes.Buffer
	srv.writer = &out
	srv.toolsList(request{JSONRPC: "2.0"})
	var resp response
	if err := json.Unmarshal(bytesTrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatal(err)
	}
	var tls toolsListResult
	if err := json.Unmarshal(resp.Result, &tls); err != nil {
		t.Fatal(err)
	}
	expectedNames := expectedToolNamesForAccess(ToolAccessRead)
	if len(tls.Tools) != len(expectedNames) {
		t.Fatalf("tools count = %d, want %d", len(tls.Tools), len(expectedNames))
	}
	for i, want := range expectedNames {
		if tls.Tools[i].Name != want {
			t.Fatalf("tool[%d].Name = %q, want %q", i, tls.Tools[i].Name, want)
		}
	}
}

func TestMCPLifecycleTools(t *testing.T) {
	store := populatedStore(t)
	defer store.Close()
	ctx := context.Background()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "empty-repo", Owner: "owner", Name: "empty", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	srv, r, w, stderr := newPipeServerWithToolAccess(service.New(store), ToolAccessWrite)
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

	listed := map[string]bool{}
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "tools", "method": "tools/list"})
	_, _ = r.Write(append(b, '\n'))
	line, err := readLine(w)
	if err != nil {
		t.Fatalf("read tools/list response: %v", err)
	}
	var resp response
	if err := json.Unmarshal(line, &resp); err != nil || resp.Error != nil {
		t.Fatalf("tools/list response=%s err=%v", string(line), err)
	}
	var tls toolsListResult
	if err := json.Unmarshal(resp.Result, &tls); err != nil {
		t.Fatal(err)
	}
	for _, tool := range tls.Tools {
		listed[tool.Name] = true
	}
	for _, name := range []string{"repo_status", "sync_live", "add_issue_comment", "update_issue", "create_pr", "update_pr", "add_pr_comment", "link_pr_issue", "index_repo", "auth_status", "doctor"} {
		if !listed[name] {
			t.Fatalf("tools/list missing lifecycle tool %q", name)
		}
	}
	for _, name := range []string{"create_issue", "add_comment", "create_page", "update_page"} {
		if listed[name] {
			t.Fatalf("tools/list advertised write tool %q", name)
		}
	}

	statusCall := call("repo_status", map[string]any{})
	var status repoStatusResult
	decodeStructured(t, statusCall, &status)
	if status.BindingState != "nothing_bound" {
		t.Fatalf("repo_status binding_state=%q", status.BindingState)
	}

	syncCall := call("sync_live", map[string]any{"repo_id": "fixture-a", "issues": true, "remote_alias": "issue:42", "idempotency_key": "mcp-lifecycle-sync-issue-42"})
	var syncResult syncLiveResult
	decodeStructured(t, syncCall, &syncResult)
	if syncResult.FreshCount != 0 || len(syncResult.Results) != 0 || !containsString(syncResult.Collections, "issues") || !containsLifecycleDiagnostic(syncResult.Diagnostics, "mcp_live_not_configured") {
		t.Fatalf("sync_live result=%+v", syncResult)
	}

	bulkSyncCall := call("sync_live", map[string]any{"repo_id": "fixture-a", "issues": true, "idempotency_key": "mcp-lifecycle-bulk-issues"})
	var bulkSyncResult syncLiveResult
	decodeStructured(t, bulkSyncCall, &bulkSyncResult)
	if bulkSyncResult.SuccessCount != 0 || bulkSyncResult.FailureCount != 0 || !containsString(bulkSyncResult.Collections, "issues") || !containsLifecycleDiagnostic(bulkSyncResult.Diagnostics, "mcp_live_not_configured") {
		t.Fatalf("bulk sync_live result=%+v", bulkSyncResult)
	}

	indexCall := call("index_repo", map[string]any{"repo_id": "fixture-a"})
	var indexResult service.OperationResult
	decodeStructured(t, indexCall, &indexResult)
	if indexResult.Command != "index" || indexResult.Status != "ok" {
		t.Fatalf("index_repo result=%+v", indexResult)
	}

	authCall := call("auth_status", map[string]any{})
	var authResult authStatusResult
	decodeStructured(t, authCall, &authResult)
	if strings.Contains(fmt.Sprint(authResult), "test-token") {
		t.Fatalf("auth_status leaked token: %+v", authResult)
	}

	doctorCall := call("doctor", map[string]any{})
	var doctor doctorResult
	decodeStructured(t, doctorCall, &doctor)
	if doctor.Status != "ok" || doctor.Diagnostics == nil {
		t.Fatalf("doctor result=%+v", doctor)
	}
	if doctor.ToolAccess != "write" {
		t.Fatalf("doctor tool_access=%q, want write", doctor.ToolAccess)
	}
	doctorRepoCall := call("doctor", map[string]any{"repo_id": "fixture-a"})
	var doctorRepo doctorResult
	decodeStructured(t, doctorRepoCall, &doctorRepo)
	if doctorRepo.Repo == nil || doctorRepo.Cache == nil || doctorRepo.Sync == nil || doctorRepo.Index == nil || doctorRepo.Auth == nil {
		t.Fatalf("repo doctor missing sections: %+v", doctorRepo)
	}

	listedSources := call("list_sources", map[string]any{"repo_id": "fixture-a", "kind": "issue"})
	var sources service.ListSourcesResult
	decodeStructured(t, listedSources, &sources)
	if len(sources.Results) == 0 {
		t.Fatalf("list_sources after sync returned no records")
	}
	searchedSources := call("search_sources", map[string]any{"repo_id": "fixture-a", "query": "issue", "kind": "issue"})
	var searched service.SearchSourcesResult
	decodeStructured(t, searchedSources, &searched)
	if len(searched.Results) == 0 {
		t.Fatalf("search_sources after sync returned no records")
	}
	_ = r.Close()
	wg.Wait()
}

func TestMCPAuthStatusUsesCredentialResolverMockKeychain(t *testing.T) {
	store := populatedStore(t)
	defer store.Close()
	resolver := auth.NewCredentialResolverWithProvider(config.StaticCredentialProvider{Source: "mock-keychain", Token: "secret-token", StoreMode: "keychain"})
	var buf bytes.Buffer
	srv := New(strings.NewReader(""), &buf, io.Discard, service.New(store), resolver)
	id := json.RawMessage(`"auth"`)
	srv.callAuthStatus(context.Background(), &id, json.RawMessage(`{}`))

	var resp response
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &resp); err != nil {
		t.Fatalf("unmarshal response: %v body=%q", err, buf.String())
	}
	if resp.Error != nil {
		t.Fatalf("auth_status returned error: %+v", resp.Error)
	}
	var callResult toolCallResult
	if err := json.Unmarshal(resp.Result, &callResult); err != nil {
		t.Fatalf("unmarshal call result: %v", err)
	}
	var status authStatusResult
	decodeStructured(t, callResult, &status)
	if !status.Present || status.Source != "mock-keychain" || status.StoreMode != "keychain" {
		t.Fatalf("auth_status=%+v", status)
	}
	if strings.Contains(fmt.Sprint(callResult), "secret-token") {
		t.Fatalf("auth_status leaked token: %+v", callResult)
	}
}

func TestMCPAuthStatusJSONRPCHandlerPreservesCredentialResolver(t *testing.T) {
	store := populatedStore(t)
	defer store.Close()
	resolver := auth.NewCredentialResolverWithProvider(config.StaticCredentialProvider{Source: "mock-keychain", Token: "secret-token", StoreMode: "keychain"})
	handler := NewRPCHandlerWithCredentialResolver(service.New(store), resolver)
	params := json.RawMessage(`{"name":"auth_status","arguments":{}}`)
	id := json.RawMessage(`"auth"`)
	resp, ok := handler.Handle(context.Background(), request{JSONRPC: "2.0", ID: &id, Method: "tools/call", Params: &params})
	if !ok || resp == nil {
		t.Fatal("auth_status JSON-RPC returned no response")
	}
	if resp.Error != nil {
		t.Fatalf("auth_status returned error: %+v", resp.Error)
	}
	var callResult toolCallResult
	if err := json.Unmarshal(resp.Result, &callResult); err != nil {
		t.Fatalf("unmarshal call result: %v", err)
	}
	var status authStatusResult
	decodeStructured(t, callResult, &status)
	if !status.Present || status.Source != "mock-keychain" || status.StoreMode != "keychain" {
		t.Fatalf("auth_status=%+v", status)
	}
	if strings.Contains(fmt.Sprint(callResult), "secret-token") {
		t.Fatalf("auth_status leaked token: %+v", callResult)
	}
}

type indexRepoSpyService struct {
	serviceInterface
	indexCalls      []service.OperationRequest
	staleIndexCalls []service.StaleIndexRequest
}

func (s *indexRepoSpyService) Index(ctx context.Context, req service.OperationRequest) (service.OperationResult, error) {
	s.indexCalls = append(s.indexCalls, req)
	return service.OperationResult{Command: "index", Status: "ok", ProcessedCount: 3, Evidence: "snapshot_id=spy-snapshot", GeneratedAt: time.Date(2026, 6, 26, 10, 0, 0, 0, time.UTC)}, nil
}

func (s *indexRepoSpyService) StaleIndex(ctx context.Context, req service.StaleIndexRequest) (service.StaleIndexResult, error) {
	s.staleIndexCalls = append(s.staleIndexCalls, req)
	return service.StaleIndexResult{RepoID: req.RepoID}, nil
}

func TestMCPIndexRepoDelegatesServiceIndex(t *testing.T) {
	spy := &indexRepoSpyService{}
	srv, r, w, stderr := newPipeServerWithToolAccess(spy, ToolAccessWrite)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = srv.Serve() }()

	call := func(name string, args map[string]any) toolCallResult {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": name, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		_, _ = r.Write(append(b, '\n'))
		line, err := readLine(w)
		if err != nil {
			t.Fatalf("read %s response: %v (stderr: %s)", name, err, stderr.String())
		}
		return decodeToolCallResult(t, line)
	}

	indexResult := call("index_repo", map[string]any{"repo_id": "fixture-a"})
	var opResult service.OperationResult
	decodeStructured(t, indexResult, &opResult)

	if len(spy.indexCalls) != 1 {
		t.Fatalf("SCN-MCP-INDEX-REPO-DELEGATES-SERVICE-INDEX: Index call count = %d, want 1", len(spy.indexCalls))
	}
	if len(spy.staleIndexCalls) != 0 {
		t.Fatalf("SCN-MCP-INDEX-REPO-DELEGATES-SERVICE-INDEX: StaleIndex call count = %d, want 0", len(spy.staleIndexCalls))
	}
	if opResult.Command != "index" {
		t.Fatalf("SCN-MCP-INDEX-REPO-DELEGATES-SERVICE-INDEX: Command = %q, want index", opResult.Command)
	}
	if opResult.Status != "ok" {
		t.Fatalf("SCN-MCP-INDEX-REPO-DELEGATES-SERVICE-INDEX: Status = %q, want ok", opResult.Status)
	}
	if opResult.ProcessedCount != 3 {
		t.Fatalf("SCN-MCP-INDEX-REPO-DELEGATES-SERVICE-INDEX: ProcessedCount = %d, want 3", opResult.ProcessedCount)
	}

	_ = r.Close()
	wg.Wait()
}

type writeLifecycleSpyService struct {
	serviceInterface
	calls map[string]service.WriteCommandRequest
}

func (s *writeLifecycleSpyService) record(command string, req service.WriteCommandRequest) (service.WriteCommandResult, error) {
	if s.calls == nil {
		s.calls = map[string]service.WriteCommandRequest{}
	}
	s.calls[command] = req
	return service.WriteCommandResult{Command: command, Status: "succeeded", RepoID: req.RepoID, ID: "PR-7", RemoteID: "7", RemoteNumber: req.Number, IdempotencyKey: req.IdempotencyKey, GeneratedAt: time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)}, nil
}

func (s *writeLifecycleSpyService) AddComment(_ context.Context, req service.WriteCommandRequest) (service.WriteCommandResult, error) {
	return s.record("add-comment", req)
}

func (s *writeLifecycleSpyService) UpdateIssue(_ context.Context, req service.WriteCommandRequest) (service.WriteCommandResult, error) {
	return s.record("update-issue", req)
}

func (s *writeLifecycleSpyService) CreatePR(_ context.Context, req service.WriteCommandRequest) (service.WriteCommandResult, error) {
	return s.record("create-pr", req)
}

func (s *writeLifecycleSpyService) UpdatePR(_ context.Context, req service.WriteCommandRequest) (service.WriteCommandResult, error) {
	return s.record("update-pr", req)
}

func (s *writeLifecycleSpyService) AddPRComment(_ context.Context, req service.WriteCommandRequest) (service.WriteCommandResult, error) {
	return s.record("add-pr-comment", req)
}

func (s *writeLifecycleSpyService) LinkPRIssue(_ context.Context, req service.WriteCommandRequest) (service.WriteCommandResult, error) {
	return s.record("link-pr-issue", req)
}

func TestMCPWriteLifecycleToolsDelegateToService(t *testing.T) {
	spy := &writeLifecycleSpyService{}
	srv, r, w, stderr := newPipeServerWithToolAccess(spy, ToolAccessWrite)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = srv.Serve() }()

	call := func(name string, args map[string]any) service.WriteCommandResult {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": name, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		_, _ = r.Write(append(b, '\n'))
		line, err := readLine(w)
		if err != nil {
			t.Fatalf("read %s response: %v (stderr: %s)", name, err, stderr.String())
		}
		result := decodeToolCallResult(t, line)
		var write service.WriteCommandResult
		decodeStructured(t, result, &write)
		return write
	}

	call("add_issue_comment", map[string]any{"repo_id": "fixture-a", "write_mode": "live", "number": 16, "body": "proposal", "idempotency_key": "issue-comment-key"})
	call("update_issue", map[string]any{"repo_id": "fixture-a", "write_mode": "live", "number": 16, "title": "updated", "labels": []string{"enhancement"}, "idempotency_key": "issue-update-key"})
	call("create_pr", map[string]any{"repo_id": "fixture-a", "write_mode": "live", "title": "PR", "body": "body", "head": "topic", "base": "main", "idempotency_key": "create-pr-key"})
	call("update_pr", map[string]any{"repo_id": "fixture-a", "write_mode": "live", "number": 7, "body": "new body", "idempotency_key": "update-pr-key"})
	call("add_pr_comment", map[string]any{"repo_id": "fixture-a", "write_mode": "live", "number": 7, "body": "tested", "idempotency_key": "pr-comment-key"})
	call("link_pr_issue", map[string]any{"repo_id": "fixture-a", "write_mode": "live", "pr_number": 7, "issue_number": 16, "strategy": "auto", "idempotency_key": "link-key"})

	assertReq := func(command string) service.WriteCommandRequest {
		t.Helper()
		req, ok := spy.calls[command]
		if !ok {
			t.Fatalf("missing service call %s in %#v", command, spy.calls)
		}
		if req.RepoID != "fixture-a" || req.Mode != service.WriteModeLive || req.IdempotencyKey == "" {
			t.Fatalf("%s request=%#v", command, req)
		}
		return req
	}
	if req := assertReq("add-comment"); req.Number != 16 || req.Body != "proposal" {
		t.Fatalf("add-comment req=%#v", req)
	}
	if req := assertReq("update-issue"); req.Number != 16 || req.Title != "updated" || len(req.Labels) != 1 || req.Labels[0] != "enhancement" {
		t.Fatalf("update-issue req=%#v", req)
	}
	if req := assertReq("create-pr"); req.Title != "PR" || req.Head != "topic" || req.Base != "main" {
		t.Fatalf("create-pr req=%#v", req)
	}
	if req := assertReq("update-pr"); req.Number != 7 || req.Body != "new body" {
		t.Fatalf("update-pr req=%#v", req)
	}
	if req := assertReq("add-pr-comment"); req.Number != 7 || req.Body != "tested" {
		t.Fatalf("add-pr-comment req=%#v", req)
	}
	if req := assertReq("link-pr-issue"); req.Number != 7 || req.IssueNumber != 16 || req.Strategy != "auto" {
		t.Fatalf("link-pr-issue req=%#v", req)
	}

	_ = r.Close()
	wg.Wait()
}

type syncLiveBoundsSpyService struct {
	serviceInterface
	bulkIssuesCalls []service.BulkSyncRequest
}

func (s *syncLiveBoundsSpyService) ProviderMode() gitcode.ProviderMode {
	return gitcode.ProviderModeLive
}

func (s *syncLiveBoundsSpyService) BulkSyncIssues(ctx context.Context, req service.BulkSyncRequest) (*service.SyncResourcesResult, error) {
	s.bulkIssuesCalls = append(s.bulkIssuesCalls, req)
	result := &service.SyncResourcesResult{
		Results:      []service.SyncResult{},
		Failures:     []service.ResourceError{},
		SuccessCount: 0,
		FailureCount: 0,
	}
	return result, &service.PartialSyncError{Diagnostic: service.SyncDiagnosticTimeout, TotalRequested: 7}
}

func TestMCPSyncLivePropagatesBoundsAndDiagnostics(t *testing.T) {
	spy := &syncLiveBoundsSpyService{}
	srv, r, w, stderr := newPipeServerWithToolAccess(spy, ToolAccessWrite)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = srv.Serve() }()

	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      "sync-live-bounds",
		"method":  "tools/call",
		"params": map[string]any{
			"name": "sync_live",
			"arguments": map[string]any{
				"repo_id":     "fixture-a",
				"issues":      true,
				"max_pages":   2,
				"max_records": 7,
				"per_page":    3,
			},
		},
	})
	_, _ = r.Write(append(b, '\n'))
	line, err := readLine(w)
	if err != nil {
		t.Fatalf("read sync_live response: %v (stderr: %s)", err, stderr.String())
	}
	callResult := decodeToolCallResult(t, line)
	var result syncLiveResult
	decodeStructured(t, callResult, &result)

	if len(spy.bulkIssuesCalls) != 1 {
		t.Fatalf("BulkSyncIssues calls = %d, want 1", len(spy.bulkIssuesCalls))
	}
	req := spy.bulkIssuesCalls[0]
	if req.PerPage != 3 {
		t.Fatalf("PerPage = %d, want 3", req.PerPage)
	}
	if req.Bounds == nil || req.Bounds.MaxPages != 2 || req.Bounds.MaxRecords != 7 {
		t.Fatalf("Bounds = %+v, want max_pages=2 max_records=7", req.Bounds)
	}
	if !containsLifecycleDiagnostic(result.Diagnostics, string(service.SyncDiagnosticTimeout)) {
		t.Fatalf("diagnostics = %+v, want sync_timeout", result.Diagnostics)
	}

	_ = r.Close()
	wg.Wait()
}

func TestMCPIndexRepoNotStaleDiagnostic(t *testing.T) {
	store := populatedStore(t)
	defer store.Close()
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "index-repo-target", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	graphs := []cache.SourceGraph{
		{Source: cache.Source{RepoID: "index-repo-target", ID: "ISSUE-1", Kind: "issue", Path: "issues/1.md", Title: "Issue 1", Body: "indexable", Status: "open", ContentHash: "h1", CreatedAt: time.Date(2026, 6, 26, 10, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 6, 26, 10, 0, 0, 0, time.UTC)}},
		{Source: cache.Source{RepoID: "index-repo-target", ID: "ISSUE-2", Kind: "issue", Path: "issues/2.md", Title: "Issue 2", Body: "also indexable", Status: "open", ContentHash: "h2", CreatedAt: time.Date(2026, 6, 26, 10, 1, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 6, 26, 10, 1, 0, 0, time.UTC)}},
	}
	for _, graph := range graphs {
		if err := store.UpsertSourceGraph(context.Background(), graph); err != nil {
			t.Fatal(err)
		}
	}

	srv, r, w, stderr := newPipeServerWithToolAccess(service.New(store), ToolAccessWrite)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = srv.Serve() }()

	call := func(name string, args map[string]any) toolCallResult {
		t.Helper()
		b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": name, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		_, _ = r.Write(append(b, '\n'))
		line, err := readLine(w)
		if err != nil {
			t.Fatalf("read %s response: %v (stderr: %s)", name, err, stderr.String())
		}
		return decodeToolCallResult(t, line)
	}

	indexResult := call("index_repo", map[string]any{"repo_id": "index-repo-target"})
	var opResult service.OperationResult
	decodeStructured(t, indexResult, &opResult)

	if opResult.Command != "index" || opResult.Status != "ok" {
		t.Fatalf("SCN-MCP-INDEX-REPO-NOT-STALE-DIAGNOSTIC: expected index outcome, got command=%q status=%q", opResult.Command, opResult.Status)
	}
	if opResult.ProcessedCount <= 0 {
		t.Fatalf("SCN-MCP-INDEX-REPO-NOT-STALE-DIAGNOSTIC: expected processed_count > 0, got %d", opResult.ProcessedCount)
	}

	raw, err := json.Marshal(opResult)
	if err != nil {
		t.Fatal(err)
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatal(err)
	}
	for _, staleField := range []string{"stale_count", "affected_source_ids", "missing_target_ids"} {
		if _, exists := asMap[staleField]; exists {
			t.Fatalf("SCN-MCP-INDEX-REPO-NOT-STALE-DIAGNOSTIC: index_repo response contains stale-index field %q", staleField)
		}
	}

	_ = r.Close()
	wg.Wait()
}

func TestStartupDiagnosticInjection(t *testing.T) {
	diagnostic := StartupDiagnosticFromError(os.ErrPermission)
	if diagnostic.ErrorClass != "cache_path_unwritable" || !strings.Contains(diagnostic.Remediation, "chmod") || !strings.Contains(diagnostic.Remediation, "--cache-path") {
		t.Fatalf("cache_path_unwritable diagnostic=%+v", diagnostic)
	}
	srv := NewMinimalRPCHandler(diagnostic)

	initReq := request{JSONRPC: "2.0", Method: "initialize"}
	initID := json.RawMessage(`"init"`)
	initReq.ID = &initID
	initResp, ok := srv.Handle(context.Background(), initReq)
	if !ok || initResp.Error != nil {
		t.Fatalf("initialize response=%+v ok=%t", initResp, ok)
	}
	var init initResult
	if err := json.Unmarshal(initResp.Result, &init); err != nil {
		t.Fatal(err)
	}
	if init.Capabilities.Tools.StartupDiagnostic == nil || init.Capabilities.Tools.StartupDiagnostic.ErrorClass != "cache_path_unwritable" {
		t.Fatalf("initialize startup diagnostic=%+v", init.Capabilities.Tools.StartupDiagnostic)
	}

	listReq := request{JSONRPC: "2.0", Method: "tools/list"}
	listID := json.RawMessage(`"list"`)
	listReq.ID = &listID
	listResp, ok := srv.Handle(context.Background(), listReq)
	if !ok || listResp.Error != nil {
		t.Fatalf("tools/list response=%+v ok=%t", listResp, ok)
	}
	var list toolsListResult
	if err := json.Unmarshal(listResp.Result, &list); err != nil {
		t.Fatal(err)
	}
	if list.StartupDiagnostic == nil || list.StartupDiagnostic.ErrorClass == "" || list.StartupDiagnostic.Message == "" || list.StartupDiagnostic.Remediation == "" {
		t.Fatalf("tools/list startup diagnostic=%+v", list.StartupDiagnostic)
	}
	if len(list.Tools) != 1 || list.Tools[0].Name != "doctor" {
		t.Fatalf("minimal tools/list tools=%+v", list.Tools)
	}

	doctorParams := json.RawMessage(`{"name":"doctor","arguments":{}}`)
	doctorID := json.RawMessage(`"doctor"`)
	doctorReq := request{JSONRPC: "2.0", ID: &doctorID, Method: "tools/call", Params: &doctorParams}
	doctorResp, ok := srv.Handle(context.Background(), doctorReq)
	if !ok || doctorResp.Error != nil {
		t.Fatalf("doctor response=%+v ok=%t", doctorResp, ok)
	}
	var callResult toolCallResult
	if err := json.Unmarshal(doctorResp.Result, &callResult); err != nil {
		t.Fatal(err)
	}
	var doctor doctorResult
	decodeStructured(t, callResult, &doctor)
	if doctor.Status != "degraded" || len(doctor.Diagnostics) != 1 {
		t.Fatalf("doctor result=%+v", doctor)
	}
	got := doctor.Diagnostics[0]
	if got.ErrorClass != "cache_path_unwritable" || got.Message == "" || got.Remediation == "" {
		t.Fatalf("doctor diagnostic=%+v", got)
	}
}

func TestStartupDiagnosticSchemaIncompatible(t *testing.T) {
	diagnostic := StartupDiagnosticFromError(&cache.SchemaVersionError{Compat: cache.VersionCompatibility{Message: "cache schema is newer than supported", Remediation: "upgrade the gitcode-mcp binary to a version that supports this schema"}})
	if diagnostic.ErrorClass != "schema_incompatible" || !strings.Contains(diagnostic.Remediation, "upgrade") {
		t.Fatalf("schema_incompatible diagnostic=%+v", diagnostic)
	}
	srv := NewMinimalRPCHandler(diagnostic)

	initReq := request{JSONRPC: "2.0", Method: "initialize"}
	initID := json.RawMessage(`"init"`)
	initReq.ID = &initID
	initResp, ok := srv.Handle(context.Background(), initReq)
	if !ok || initResp.Error != nil {
		t.Fatalf("initialize response=%+v ok=%t", initResp, ok)
	}
	var init initResult
	if err := json.Unmarshal(initResp.Result, &init); err != nil {
		t.Fatal(err)
	}
	if init.Capabilities.Tools.StartupDiagnostic == nil || init.Capabilities.Tools.StartupDiagnostic.ErrorClass != "schema_incompatible" {
		t.Fatalf("initialize startup diagnostic=%+v", init.Capabilities.Tools.StartupDiagnostic)
	}
	if !strings.Contains(init.Capabilities.Tools.StartupDiagnostic.Remediation, "upgrade") {
		t.Fatalf("initialize startup diagnostic missing upgrade remediation: %+v", init.Capabilities.Tools.StartupDiagnostic)
	}

	listReq := request{JSONRPC: "2.0", Method: "tools/list"}
	listID := json.RawMessage(`"list"`)
	listReq.ID = &listID
	listResp, ok := srv.Handle(context.Background(), listReq)
	if !ok || listResp.Error != nil {
		t.Fatalf("tools/list response=%+v ok=%t", listResp, ok)
	}
	var list toolsListResult
	if err := json.Unmarshal(listResp.Result, &list); err != nil {
		t.Fatal(err)
	}
	if list.StartupDiagnostic == nil || list.StartupDiagnostic.ErrorClass != "schema_incompatible" || list.StartupDiagnostic.Message == "" || list.StartupDiagnostic.Remediation == "" {
		t.Fatalf("tools/list startup diagnostic=%+v", list.StartupDiagnostic)
	}
	if len(list.Tools) != 1 || list.Tools[0].Name != "doctor" {
		t.Fatalf("minimal tools/list tools=%+v", list.Tools)
	}

	doctorParams := json.RawMessage(`{"name":"doctor","arguments":{}}`)
	doctorID := json.RawMessage(`"doctor"`)
	doctorReq := request{JSONRPC: "2.0", ID: &doctorID, Method: "tools/call", Params: &doctorParams}
	doctorResp, ok := srv.Handle(context.Background(), doctorReq)
	if !ok || doctorResp.Error != nil {
		t.Fatalf("doctor response=%+v ok=%t", doctorResp, ok)
	}
	var callResult toolCallResult
	if err := json.Unmarshal(doctorResp.Result, &callResult); err != nil {
		t.Fatal(err)
	}
	var doctor doctorResult
	decodeStructured(t, callResult, &doctor)
	if doctor.Status != "degraded" || len(doctor.Diagnostics) != 1 {
		t.Fatalf("doctor result=%+v", doctor)
	}
	got := doctor.Diagnostics[0]
	if got.ErrorClass != "schema_incompatible" || got.Message == "" || got.Remediation == "" {
		t.Fatalf("doctor diagnostic=%+v", got)
	}
	if !strings.Contains(got.Remediation, "upgrade") {
		t.Fatalf("doctor diagnostic missing upgrade remediation: %+v", got)
	}
}

func TestStartupDiagnosticCacheLockContention(t *testing.T) {
	lockErr := cache.ErrLockContention{Path: "lock-test.path", Operation: "write", RepoID: "fixture-a", StartedAt: time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC), PID: 42, CachePath: "/tmp/test-cache"}
	diagnostic := StartupDiagnosticFromError(lockErr)
	if diagnostic.ErrorClass != "cache_lock_contention" || !strings.Contains(diagnostic.Remediation, "retry") {
		t.Fatalf("cache_lock_contention diagnostic=%+v", diagnostic)
	}
	srv := NewMinimalRPCHandler(diagnostic)

	initReq := request{JSONRPC: "2.0", Method: "initialize"}
	initID := json.RawMessage(`"init"`)
	initReq.ID = &initID
	initResp, ok := srv.Handle(context.Background(), initReq)
	if !ok || initResp.Error != nil {
		t.Fatalf("initialize response=%+v ok=%t", initResp, ok)
	}
	var init initResult
	if err := json.Unmarshal(initResp.Result, &init); err != nil {
		t.Fatal(err)
	}
	if init.Capabilities.Tools.StartupDiagnostic == nil || init.Capabilities.Tools.StartupDiagnostic.ErrorClass != "cache_lock_contention" {
		t.Fatalf("initialize startup diagnostic=%+v", init.Capabilities.Tools.StartupDiagnostic)
	}

	listReq := request{JSONRPC: "2.0", Method: "tools/list"}
	listID := json.RawMessage(`"list"`)
	listReq.ID = &listID
	listResp, ok := srv.Handle(context.Background(), listReq)
	if !ok || listResp.Error != nil {
		t.Fatalf("tools/list response=%+v ok=%t", listResp, ok)
	}
	var list toolsListResult
	if err := json.Unmarshal(listResp.Result, &list); err != nil {
		t.Fatal(err)
	}
	if list.StartupDiagnostic == nil || list.StartupDiagnostic.ErrorClass != "cache_lock_contention" || list.StartupDiagnostic.Message == "" || list.StartupDiagnostic.Remediation == "" {
		t.Fatalf("tools/list startup diagnostic=%+v", list.StartupDiagnostic)
	}
	if len(list.Tools) != 1 || list.Tools[0].Name != "doctor" {
		t.Fatalf("minimal tools/list tools=%+v", list.Tools)
	}

	doctorParams := json.RawMessage(`{"name":"doctor","arguments":{}}`)
	doctorID := json.RawMessage(`"doctor"`)
	doctorReq := request{JSONRPC: "2.0", ID: &doctorID, Method: "tools/call", Params: &doctorParams}
	doctorResp, ok := srv.Handle(context.Background(), doctorReq)
	if !ok || doctorResp.Error != nil {
		t.Fatalf("doctor response=%+v ok=%t", doctorResp, ok)
	}
	var callResult toolCallResult
	if err := json.Unmarshal(doctorResp.Result, &callResult); err != nil {
		t.Fatal(err)
	}
	var doctor doctorResult
	decodeStructured(t, callResult, &doctor)
	if doctor.Status != "degraded" || len(doctor.Diagnostics) != 1 {
		t.Fatalf("doctor result=%+v", doctor)
	}
	got := doctor.Diagnostics[0]
	if got.ErrorClass != "cache_lock_contention" || got.Message == "" || got.Remediation == "" {
		t.Fatalf("doctor diagnostic=%+v", got)
	}
	if !strings.Contains(got.Remediation, "retry") {
		t.Fatalf("doctor diagnostic missing retry remediation: %+v", got)
	}
}

func TestStartupDiagnosticStartupFailure(t *testing.T) {
	rawErr := errors.New("panic: secret stack trace\n/path/file.go:10\n\ncaused by: internal config error at /Users/test/.gitcode/config.yaml")
	diagnostic := StartupDiagnosticFromError(rawErr)
	if diagnostic.ErrorClass != "startup-failure" || diagnostic.Message == "" || diagnostic.Remediation == "" {
		t.Fatalf("startup-failure diagnostic=%+v", diagnostic)
	}
	if strings.Contains(diagnostic.Message, "panic") || strings.Contains(diagnostic.Message, "/path/file.go") || strings.Contains(diagnostic.Message, "stack trace") {
		t.Fatalf("startup-failure diagnostic leaked raw details=%+v", diagnostic)
	}
	srv := NewMinimalRPCHandler(diagnostic)

	initReq := request{JSONRPC: "2.0", Method: "initialize"}
	initID := json.RawMessage(`"init"`)
	initReq.ID = &initID
	initResp, ok := srv.Handle(context.Background(), initReq)
	if !ok || initResp.Error != nil {
		t.Fatalf("initialize response=%+v ok=%t", initResp, ok)
	}
	var init initResult
	if err := json.Unmarshal(initResp.Result, &init); err != nil {
		t.Fatal(err)
	}
	if init.Capabilities.Tools.StartupDiagnostic == nil || init.Capabilities.Tools.StartupDiagnostic.ErrorClass != "startup-failure" {
		t.Fatalf("initialize startup diagnostic=%+v", init.Capabilities.Tools.StartupDiagnostic)
	}
	if init.Capabilities.Tools.StartupDiagnostic.Message == "" || init.Capabilities.Tools.StartupDiagnostic.Remediation == "" {
		t.Fatalf("initialize startup diagnostic missing message/remediation: %+v", init.Capabilities.Tools.StartupDiagnostic)
	}

	listReq := request{JSONRPC: "2.0", Method: "tools/list"}
	listID := json.RawMessage(`"list"`)
	listReq.ID = &listID
	listResp, ok := srv.Handle(context.Background(), listReq)
	if !ok || listResp.Error != nil {
		t.Fatalf("tools/list response=%+v ok=%t", listResp, ok)
	}
	var list toolsListResult
	if err := json.Unmarshal(listResp.Result, &list); err != nil {
		t.Fatal(err)
	}
	if list.StartupDiagnostic == nil || list.StartupDiagnostic.ErrorClass != "startup-failure" || list.StartupDiagnostic.Message == "" || list.StartupDiagnostic.Remediation == "" {
		t.Fatalf("tools/list startup diagnostic=%+v", list.StartupDiagnostic)
	}
	if len(list.Tools) != 1 || list.Tools[0].Name != "doctor" {
		t.Fatalf("minimal tools/list tools=%+v", list.Tools)
	}

	doctorParams := json.RawMessage(`{"name":"doctor","arguments":{}}`)
	doctorID := json.RawMessage(`"doctor"`)
	doctorReq := request{JSONRPC: "2.0", ID: &doctorID, Method: "tools/call", Params: &doctorParams}
	doctorResp, ok := srv.Handle(context.Background(), doctorReq)
	if !ok || doctorResp.Error != nil {
		t.Fatalf("doctor response=%+v ok=%t", doctorResp, ok)
	}
	var callResult toolCallResult
	if err := json.Unmarshal(doctorResp.Result, &callResult); err != nil {
		t.Fatal(err)
	}
	var doctor doctorResult
	decodeStructured(t, callResult, &doctor)
	if doctor.Status != "degraded" || len(doctor.Diagnostics) != 1 {
		t.Fatalf("doctor result=%+v", doctor)
	}
	got := doctor.Diagnostics[0]
	if got.ErrorClass != "startup-failure" || got.Message == "" || got.Remediation == "" {
		t.Fatalf("doctor diagnostic=%+v", got)
	}
	if strings.Contains(got.Message, "panic") || strings.Contains(got.Message, "/path/file.go") || strings.Contains(got.Message, "stack trace") || strings.Contains(got.Message, "/Users/test") {
		t.Fatalf("doctor diagnostic leaked raw details: %+v", got)
	}
}

func TestStartupDiagnosticAllScenarios(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		errorClass  string
		remediation string
	}{
		{name: "SCN-STARTUP-001", err: &cache.SchemaVersionError{Compat: cache.VersionCompatibility{Message: "cache schema is newer than supported", Remediation: "upgrade the gitcode-mcp binary to a version that supports this schema"}}, errorClass: "schema_incompatible", remediation: "upgrade"},
		{name: "SCN-STARTUP-002", err: cache.ErrLockContention{Path: "lock-test.path", Operation: "write", CachePath: "/tmp/test-cache", PID: 42, StartedAt: time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)}, errorClass: "cache_lock_contention", remediation: "retry"},
		{name: "SCN-STARTUP-003", err: os.ErrPermission, errorClass: "cache_path_unwritable", remediation: "chmod"},
		{name: "SCN-STARTUP-004", err: errors.New("internal config error"), errorClass: "startup-failure", remediation: "gitcode-mcp doctor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diagnostic := StartupDiagnosticFromError(tt.err)
			if diagnostic.ErrorClass != tt.errorClass {
				t.Fatalf("factory errorClass = %q, want %q", diagnostic.ErrorClass, tt.errorClass)
			}
			if diagnostic.Message == "" {
				t.Fatalf("factory message is empty")
			}
			if !strings.Contains(diagnostic.Remediation, tt.remediation) {
				t.Fatalf("factory remediation=%q does not contain %q", diagnostic.Remediation, tt.remediation)
			}
			srv := NewMinimalRPCHandler(diagnostic)

			listReq := request{JSONRPC: "2.0", Method: "tools/list"}
			listID := json.RawMessage(`"list"`)
			listReq.ID = &listID
			listResp, ok := srv.Handle(context.Background(), listReq)
			if !ok || listResp.Error != nil {
				t.Fatalf("tools/list response=%+v ok=%t", listResp, ok)
			}
			var list toolsListResult
			if err := json.Unmarshal(listResp.Result, &list); err != nil {
				t.Fatal(err)
			}
			if list.StartupDiagnostic == nil || list.StartupDiagnostic.ErrorClass != tt.errorClass {
				t.Fatalf("tools/list startup diagnostic errorClass=%q", list.StartupDiagnostic.ErrorClass)
			}
			if list.StartupDiagnostic.Message == "" {
				t.Fatalf("tools/list startup diagnostic message is empty")
			}
			if !strings.Contains(list.StartupDiagnostic.Remediation, tt.remediation) {
				t.Fatalf("tools/list startup diagnostic remediation=%q does not contain %q", list.StartupDiagnostic.Remediation, tt.remediation)
			}
			if len(list.Tools) != 1 || list.Tools[0].Name != "doctor" {
				t.Fatalf("minimal tools/list returned tools=%+v", list.Tools)
			}

			doctorParams := json.RawMessage(`{"name":"doctor","arguments":{}}`)
			doctorID := json.RawMessage(`"doctor"`)
			doctorReq := request{JSONRPC: "2.0", ID: &doctorID, Method: "tools/call", Params: &doctorParams}
			doctorResp, ok := srv.Handle(context.Background(), doctorReq)
			if !ok || doctorResp.Error != nil {
				t.Fatalf("doctor response=%+v ok=%t", doctorResp, ok)
			}
			var callResult toolCallResult
			if err := json.Unmarshal(doctorResp.Result, &callResult); err != nil {
				t.Fatal(err)
			}
			var doctor doctorResult
			decodeStructured(t, callResult, &doctor)
			if doctor.Status != "degraded" {
				t.Fatalf("doctor status=%q", doctor.Status)
			}
			if len(doctor.Diagnostics) != 1 {
				t.Fatalf("doctor diagnostics count=%d", len(doctor.Diagnostics))
			}
			got := doctor.Diagnostics[0]
			if got.ErrorClass != tt.errorClass {
				t.Fatalf("doctor diagnostic errorClass=%q, want %q", got.ErrorClass, tt.errorClass)
			}
			if got.Message == "" {
				t.Fatalf("doctor diagnostic message is empty")
			}
			if !strings.Contains(got.Remediation, tt.remediation) {
				t.Fatalf("doctor diagnostic remediation=%q does not contain %q", got.Remediation, tt.remediation)
			}
		})
	}
}

func TestStartupDiagnosticRemediationText(t *testing.T) {
	schemaDiag := StartupDiagnosticFromError(&cache.SchemaVersionError{Compat: cache.VersionCompatibility{Message: "cache schema is newer than supported", Remediation: "upgrade the gitcode-mcp binary to a version that supports this schema"}})
	if schemaDiag.ErrorClass != "schema_incompatible" || !strings.Contains(schemaDiag.Remediation, "upgrade") {
		t.Fatalf("schema diagnostic=%+v", schemaDiag)
	}

	genericDiag := StartupDiagnosticFromError(errors.New("panic: secret stack trace\n/path/file.go:10"))
	if genericDiag.ErrorClass != "startup-failure" || strings.Contains(genericDiag.Message, "panic") || strings.Contains(genericDiag.Message, "/path/file.go") || genericDiag.Remediation == "" {
		t.Fatalf("generic diagnostic leaked raw details=%+v", genericDiag)
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

func containsLifecycleDiagnostic(values []lifecycleDiagnostic, code string) bool {
	for _, value := range values {
		if value.Code == code {
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
	return newPipeServerWithToolAccess(svc, ToolAccessRead)
}

func newPipeServerWithToolAccess(svc serviceInterface, access ToolAccess) (*Server, io.ReadWriteCloser, io.ReadWriteCloser, *bytes.Buffer) {
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	stderr := &bytes.Buffer{}
	srv := NewWithToolAccess(serverR, serverW, stderr, svc, nil, access)
	conn := &pipeConn{Reader: clientR, Writer: clientW}
	return srv, conn, conn, stderr
}

func expectedToolNamesForAccess(access ToolAccess) []string {
	names := make([]string, 0, len(toolListOrder))
	for _, name := range toolListOrder {
		if normalizeToolAccess(access) == ToolAccessRead && writeToolNames[name] {
			continue
		}
		names = append(names, name)
	}
	return names
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

func TestHTTPSSETransportDeduplicatesSessionIDs(t *testing.T) {
	store := populatedStore(t)
	defer store.Close()
	transport := NewHTTPSSETransport(NewRPCHandler(service.New(store)), ServerConfig{SessionID: func() string { return "same-session" }})
	server := httptest.NewServer(transport.Handler())
	defer server.Close()

	sseA, endpointA, eventsA := openSSE(t, server.URL+"/sse")
	defer sseA.Body.Close()
	sseB, endpointB, eventsB := openSSE(t, server.URL+"/sse")
	defer sseB.Body.Close()
	if endpointA != "/message?session_id=same-session" || endpointB != "/message?session_id=same-session-2" {
		t.Fatalf("endpoints = %q %q", endpointA, endpointB)
	}
	postJSON(t, server.URL+endpointA, map[string]any{"jsonrpc": "2.0", "id": "a", "method": "initialize"})
	postJSON(t, server.URL+endpointB, map[string]any{"jsonrpc": "2.0", "id": "b", "method": "initialize"})
	respA := readSSEMessage(t, eventsA)
	respB := readSSEMessage(t, eventsB)
	if respA.ID == nil || string(*respA.ID) != `"a"` || respB.ID == nil || string(*respB.ID) != `"b"` {
		t.Fatalf("responses crossed: %+v %+v", respA, respB)
	}
}

func TestHTTPSSETransportSessionErrorsAndMultiClient(t *testing.T) {
	store := populatedStore(t)
	defer store.Close()
	ids := []string{"session-a", "session-b"}
	var idMu sync.Mutex
	var logs bytes.Buffer
	transport := NewHTTPSSETransport(NewRPCHandler(service.New(store)), ServerConfig{
		Logger: logWriter(&logs),
		SessionID: func() string {
			idMu.Lock()
			defer idMu.Unlock()
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
	if !strings.Contains(logs.String(), "code=missing_session") || !strings.Contains(logs.String(), "session_id=missing") || !strings.Contains(logs.String(), "code=unknown_session") {
		t.Fatalf("logs missing transport errors: %q", logs.String())
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
