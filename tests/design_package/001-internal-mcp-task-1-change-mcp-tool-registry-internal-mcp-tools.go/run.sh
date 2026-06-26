#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/gitcode-mcp-registry-validation.XXXXXX")"
cleanup() {
  rm -rf "$WORK"
}
trap cleanup EXIT

rsync -a --delete \
  --exclude '.git' \
  --exclude 'ai/artifacts' \
  --exclude 'tests/design_package/001-internal-mcp-task-1-change-mcp-tool-registry-internal-mcp-tools.go' \
  "$ROOT/" "$WORK/"

python3 - <<'PY' "$WORK/internal/mcp/mcp.go"
import pathlib
import re
import sys
path = pathlib.Path(sys.argv[1])
text = path.read_text()
if re.search(r'toolDefs\s*\[\s*\d+\s*\]', text):
    raise SystemExit('positional toolDefs[N] registry access remains in internal/mcp/mcp.go')
if 'map[string]registeredTool' not in text and 'type toolRegistry map[string]registeredTool' not in text:
    raise SystemExit('map-based tool registry type not found')
PY

cat > "$WORK/internal/mcp/registry_design_validation_test.go" <<'GOEOF'
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"reflect"
	"strings"
	"sync"
	"testing"

	"gitcode-mcp/internal/service"
)

func validationCallMCP(t *testing.T, r io.Writer, w io.Reader, stderr *bytes.Buffer, req map[string]any) response {
	t.Helper()
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	if _, err := r.Write(append(b, '\n')); err != nil {
		t.Fatalf("write request: %v", err)
	}
	line, err := readLine(w)
	if err != nil {
		t.Fatalf("read response: %v stderr=%s", err, stderr.String())
	}
	var resp response
	if err := json.Unmarshal(line, &resp); err != nil {
		t.Fatalf("decode response: %v body=%s", err, string(line))
	}
	return resp
}

func validationToolNames(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var result toolsListResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode tools/list: %v", err)
	}
	names := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func validationCallTool(t *testing.T, srv *Server, name string, args map[string]any) response {
	t.Helper()
	params := map[string]any{"name": name}
	if args != nil {
		params["arguments"] = args
	}
	pb, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	id := json.RawMessage(`"validation"`)
	req := request{JSONRPC: "2.0", ID: &id, Method: "tools/call", Params: (*json.RawMessage)(&pb)}
	var out bytes.Buffer
	origWriter := srv.writer
	origStderr := srv.stderr
	srv.writer = &out
	srv.stderr = io.Discard
	t.Cleanup(func() {
		srv.writer = origWriter
		srv.stderr = origStderr
	})
	srv.toolsCall(context.Background(), req)
	var resp response
	if err := json.Unmarshal(bytesTrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("decode tools/call response: %v body=%s", err, out.String())
	}
	return resp
}

func TestDesignPackageMCPRegistryNameLookup(t *testing.T) {
	store := populatedStore(t)
	defer store.Close()
	svc := service.New(store)
	srv, r, w, stderr := newPipeServer(svc)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = srv.Serve()
	}()
	defer func() {
		_ = r.Close()
		wg.Wait()
	}()

	resp := validationCallMCP(t, r, w, stderr, map[string]any{
		"jsonrpc": "2.0",
		"id":      "001-internal-mcp-task-1-change-mcp-tool-registry-internal-mcp-tools.go-scenario-1",
		"method":  "tools/call",
		"params": map[string]any{
			"name":      "resolve_id",
			"arguments": map[string]any{"repo_id": "fixture-a", "id": "DOC-123"},
		},
	})
	if resp.Error != nil {
		t.Fatalf("resolve_id returned error: %+v", resp.Error)
	}
	var result toolCallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var resolved service.ResolvedID
	if err := json.Unmarshal(raw, &resolved); err != nil {
		t.Fatalf("decode resolved id: %v raw=%s", err, string(raw))
	}
	if resolved.RepoID != "fixture-a" || resolved.ID != "DOC-123" || resolved.Path == "" {
		t.Fatalf("resolve_id did not dispatch by requested name: %+v", resolved)
	}
}

func TestDesignPackageMCPRegistryLifecycleInsertionOrderIndependent(t *testing.T) {
	store := populatedStore(t)
	defer store.Close()
	srv := New(io.Reader(strings.NewReader("")), io.Discard, io.Discard, service.New(store))

	registry := toolRegistry{}
	registerTool(registry, "validation_lifecycle_tool", func(context.Context, *json.RawMessage, json.RawMessage) {
		t.Fatalf("test lifecycle tool handler was called for resolve_id")
	})
	for name, tool := range srv.toolRegistry() {
		registry[name] = tool
	}
	if _, ok := registry["validation_lifecycle_tool"]; !ok {
		t.Fatalf("test lifecycle tool was not inserted into registry")
	}
	tool, ok := registry["resolve_id"]
	if !ok {
		t.Fatalf("resolve_id missing from registry")
	}
	var out bytes.Buffer
	origWriter := srv.writer
	srv.writer = &out
	t.Cleanup(func() { srv.writer = origWriter })
	id := json.RawMessage(`"001-internal-mcp-task-1-change-mcp-tool-registry-internal-mcp-tools.go-scenario-2"`)
	args := json.RawMessage(`{"repo_id":"fixture-a","id":"DOC-123"}`)
	tool.handler(context.Background(), &id, args)
	var resp response
	if err := json.Unmarshal(bytesTrimSpace(out.Bytes()), &resp); err != nil {
		t.Fatalf("decode response: %v body=%s", err, out.String())
	}
	if resp.Error != nil {
		t.Fatalf("resolve_id via registry returned error: %+v", resp.Error)
	}
	var result toolCallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("decode tool result: %v", err)
	}
	raw, _ := json.Marshal(result.StructuredContent)
	var resolved service.ResolvedID
	if err := json.Unmarshal(raw, &resolved); err != nil {
		t.Fatalf("decode resolved id: %v", err)
	}
	if resolved.ID != "DOC-123" {
		t.Fatalf("handler shifted after lifecycle insertion: %+v", resolved)
	}
}

func TestDesignPackageMCPRegistryListAndUnsupportedWriteBoundary(t *testing.T) {
	store := populatedStore(t)
	defer store.Close()
	svc := service.New(store)
	srv, r, w, stderr := newPipeServer(svc)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = srv.Serve()
	}()
	defer func() {
		_ = r.Close()
		wg.Wait()
	}()

	resp := validationCallMCP(t, r, w, stderr, map[string]any{"jsonrpc": "2.0", "id": "mcp-tools-list-deterministic", "method": "tools/list"})
	if resp.Error != nil {
		t.Fatalf("tools/list error: %+v", resp.Error)
	}
	got := validationToolNames(t, resp.Result)
	want := []string{"search_sources", "get_source", "list_sources", "list_chunks", "search_chunks", "get_snippet", "stale_index_report", "recent_changes", "link_check", "cache_status", "source_backlinks", "resolve_id", "sync_status", "export_snapshot", "diff_snapshot"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tools/list names = %#v, want %#v", got, want)
	}
	blocked := map[string]bool{"create-issue": true, "update-issue": true, "add-label": true, "create-page": true, "update-page": true}
	for _, name := range got {
		if blocked[name] {
			t.Fatalf("blocked write tool %q advertised in tools/list", name)
		}
	}

	writeResp := validationCallMCP(t, r, w, stderr, map[string]any{
		"jsonrpc": "2.0",
		"id":      "mcp-unsupported-write-call",
		"method":  "tools/call",
		"params":  map[string]any{"name": "create-issue", "arguments": map[string]any{"repo_id": "fixture-a"}},
	})
	if writeResp.Error == nil || writeResp.Error.Data == nil || writeResp.Error.Data.Code != "unsupported_capability" {
		t.Fatalf("create-issue error = %+v, want unsupported_capability", writeResp.Error)
	}

	unknownResp := validationCallMCP(t, r, w, stderr, map[string]any{
		"jsonrpc": "2.0",
		"id":      "mcp-unknown-tool",
		"method":  "tools/call",
		"params":  map[string]any{"name": "definitely_unknown_tool", "arguments": map[string]any{}},
	})
	if unknownResp.Error == nil || unknownResp.Error.Data == nil || unknownResp.Error.Data.Code != "unknown_tool" {
		t.Fatalf("unknown tool error = %+v, want unknown_tool", unknownResp.Error)
	}
}
GOEOF

(
  cd "$WORK"
  go test ./internal/mcp/... 
  go test ./...
  git diff --check --no-index /dev/null /dev/null >/dev/null
)
