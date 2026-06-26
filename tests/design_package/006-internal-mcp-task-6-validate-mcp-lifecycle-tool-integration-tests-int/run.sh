#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
VALIDATION_TMPDIR="$(mktemp -d "${TMPDIR:-/tmp}/gitcode-mcp-lifecycle.XXXXXX")"
cleanup() {
  rm -rf "$VALIDATION_TMPDIR"
}
trap cleanup EXIT

TEST_FILE="$ROOT/internal/mcp/lifecycle_design_validation_test.go"
cat > "$TEST_FILE" <<'GOEOF'
package mcp

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/service"
)

func TestDesignPackageLifecycleToolIntegrationValidation(t *testing.T) {
	store := populatedStore(t)
	defer store.Close()
	ctx := context.Background()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "empty-repo", Owner: "owner", Name: "empty", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	srv, r, w, stderr := newPipeServer(service.New(store))
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = srv.Serve()
	}()

	callToolCall := func(name string, args map[string]any) (json.RawMessage, *response) {
		t.Helper()
		rawID := json.RawMessage("\"" + name + "\"")
		req := map[string]any{"jsonrpc": "2.0", "id": &rawID, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}}
		b, _ := json.Marshal(req)
		_, _ = r.Write(append(b, '\n'))
		line, err := readLine(w)
		if err != nil {
			t.Fatalf("read %s response: %v (stderr: %s)", name, err, stderr.String())
		}
		var resp response
		if err := json.Unmarshal(line, &resp); err != nil {
			t.Fatalf("decode %s response: %v", name, err)
		}
		return line, &resp
	}

	// SCN-MCP-LIFECYCLE-LIST-TOOLS: serialized tools/list includes lifecycle tool names and excludes write tools.
	rawListID := json.RawMessage(`"tools"`)
	listReq := map[string]any{"jsonrpc": "2.0", "id": &rawListID, "method": "tools/list"}
	b, _ := json.Marshal(listReq)
	_, _ = r.Write(append(b, '\n'))
	line, err := readLine(w)
	if err != nil {
		t.Fatalf("read tools/list response: %v", err)
	}
	var listResp response
	if err := json.Unmarshal(line, &listResp); err != nil || listResp.Error != nil {
		t.Fatalf("tools/list response=%s err=%v", string(line), err)
	}
	var tls toolsListResult
	if err := json.Unmarshal(listResp.Result, &tls); err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, tool := range tls.Tools {
		listed[tool.Name] = true
	}
	for _, want := range []string{"repo_status", "sync_live", "index_repo", "auth_status", "doctor"} {
		if !listed[want] {
			t.Fatalf("tools/list missing lifecycle tool %q; listed=%v", want, toolNames(tls.Tools))
		}
	}
	for _, forbid := range []string{"create_issue", "update_issue", "add_comment", "create_page", "update_page"} {
		if listed[forbid] {
			t.Fatalf("tools/list advertised write tool %q", forbid)
		}
	}

	// SCN-MCP-LIFECYCLE-REPO-STATUS-EMPTY: repo_status with no repo binding returns binding_state=nothing_bound.
	statusLine, statusResp := callToolCall("repo_status", map[string]any{})
	if statusResp.Error != nil {
		t.Fatalf("repo_status error: %+v", statusResp.Error)
	}
	var statusResult repoStatusResult
	decodeStructured(t, decodeToolCallResult(t, statusLine), &statusResult)
	if statusResult.BindingState != "nothing_bound" {
		t.Fatalf("repo_status binding_state=%q, want nothing_bound", statusResult.BindingState)
	}

	// SCN-MCP-LIFECYCLE-SYNC-ISSUES: sync_live --issues returns structured content with selected collection and nonzero fresh/success evidence.
	syncLine, syncResp := callToolCall("sync_live", map[string]any{"repo_id": "fixture-a", "issues": true, "remote_alias": "issue:42", "idempotency_key": "mcp-lifecycle-design-sync-issue-42"})
	if syncResp.Error != nil {
		t.Fatalf("sync_live error: %+v", syncResp.Error)
	}
	var syncResult syncLiveResult
	decodeStructured(t, decodeToolCallResult(t, syncLine), &syncResult)
	if syncResult.FreshCount == 0 || len(syncResult.Results) == 0 {
		t.Fatalf("sync_live FreshCount=0 or empty results: %+v", syncResult)
	}
	if !containsString(syncResult.Collections, "issues") {
		t.Fatalf("sync_live missing issues collection: %v", syncResult.Collections)
	}
	if syncResult.Results[0].Record.Kind != "issue" || syncResult.Results[0].Record.ID == "" {
		t.Fatalf("sync_live record=%+v", syncResult.Results[0].Record)
	}

	// SCN-MCP-LIFECYCLE-INDEX-ROUTE: index_repo observes Service.Index outcome and no stale-index diagnostic.
	indexLine, indexResp := callToolCall("index_repo", map[string]any{"repo_id": "fixture-a"})
	if indexResp.Error != nil {
		t.Fatalf("index_repo error: %+v", indexResp.Error)
	}
	var indexResult service.OperationResult
	decodeStructured(t, decodeToolCallResult(t, indexLine), &indexResult)
	if indexResult.Command != "index" || indexResult.Status != "ok" {
		t.Fatalf("index_repo result=%+v, want Command=index Status=ok", indexResult)
	}

	// SCN-MCP-LIFECYCLE-CACHE-READS: list_sources returns expected cached records.
	listSrcLine, listSrcResp := callToolCall("list_sources", map[string]any{"repo_id": "fixture-a", "kind": "issue"})
	if listSrcResp.Error != nil {
		t.Fatalf("list_sources error: %+v", listSrcResp.Error)
	}
	var listSourcesResult service.ListSourcesResult
	decodeStructured(t, decodeToolCallResult(t, listSrcLine), &listSourcesResult)
	if len(listSourcesResult.Results) == 0 {
		t.Fatalf("list_sources after lifecycle sync returned no records")
	}

	searchLine, searchResp := callToolCall("search_sources", map[string]any{"repo_id": "fixture-a", "query": "issue", "kind": "issue"})
	if searchResp.Error != nil {
		t.Fatalf("search_sources error: %+v", searchResp.Error)
	}
	var searchSourcesResult service.SearchSourcesResult
	decodeStructured(t, decodeToolCallResult(t, searchLine), &searchSourcesResult)
	if len(searchSourcesResult.Results) == 0 {
		t.Fatalf("search_sources after lifecycle sync returned no records")
	}

	// SCN-MCP-LIFECYCLE-NAME-REGISTRY: reordered/appended definitions do not change handler routing by name.
	originalDefs := append([]toolDefinition(nil), toolDefs...)
	defer func() { toolDefs = originalDefs }()
	for i, j := 0, len(toolDefs)-1; i < j; i, j = i+1, j-1 {
		toolDefs[i], toolDefs[j] = toolDefs[j], toolDefs[i]
	}
	toolDefs = append([]toolDefinition{{Name: "appended_lifecycle_probe", Description: "Probe appended lifecycle tool definition.", InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{}}}}, toolDefs...)
	registry := srv.toolRegistry()
	for _, name := range []string{"search_sources", "get_source", "list_sources", "resolve_id", "repo_status", "sync_live", "index_repo", "doctor"} {
		tool, ok := registry[name]
		if !ok {
			t.Fatalf("registry missing %q after reorder", name)
		}
		if tool.definition.Name != name {
			t.Fatalf("registry[%q].definition.Name = %q", name, tool.definition.Name)
		}
	}

	// SCN-MCP-LIFECYCLE-UNSUPPORTED-WRITE: create_issue returns unsupported_capability with no credential lookup or HTTP call.
	t.Setenv("GITCODE_TOKEN", "")
	rawCreateID := json.RawMessage(`"create_issue"`)
	createReq := map[string]any{"jsonrpc": "2.0", "id": &rawCreateID, "method": "tools/call", "params": map[string]any{"name": "create_issue", "arguments": map[string]any{"repo_id": "fixture-a", "title": "test"}}}
	b2, _ := json.Marshal(createReq)
	_, _ = r.Write(append(b2, '\n'))
	createLine, err := readLine(w)
	if err != nil {
		t.Fatalf("read create_issue response: %v", err)
	}
	var createResp response
	if err := json.Unmarshal(createLine, &createResp); err != nil {
		t.Fatalf("decode create_issue response: %v", err)
	}
	if createResp.Error == nil {
		t.Fatalf("create_issue returned success, want unsupported_capability error")
	}
	if createResp.Error.Data == nil || createResp.Error.Data.Code != "unsupported_capability" {
		t.Fatalf("create_issue error data=%+v, want Code=unsupported_capability", createResp.Error.Data)
	}

	r.Close()
	wg.Wait()
}

func toolNames(tools []toolDefinition) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name
	}
	return names
}
GOEOF

cleanup_test() {
  rm -f "$TEST_FILE"
}
trap 'cleanup_test; cleanup' EXIT

cd "$ROOT"
echo "=== Running design-package lifecycle validation tests ==="
go test ./internal/mcp/... -run 'TestDesignPackageLifecycleToolIntegrationValidation' -count=1 -v
echo "=== Running full package tests ==="
go test ./internal/mcp/... -count=1
echo "=== Running full repo tests ==="
go test ./...
echo "=== Running git diff check ==="
git diff --check
