#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCENARIO_DIR="$ROOT/tests/design_package/009-index-chunking-task-1-chunkpolicy-and-query-api"
WORK="$SCENARIO_DIR/.tmp-run"

if [[ -d "$WORK" ]]; then
  chmod -R u+w "$WORK" 2>/dev/null || true
  rm -rf "$WORK"
fi
mkdir -p "$WORK/go-build-cache" "$WORK/go-tmp" "$WORK/tmp" "$WORK/home"

cleanup() {
  if [[ -d "$WORK" ]]; then
    chmod -R u+w "$WORK" 2>/dev/null || true
    rm -rf "$WORK"
  fi
}
trap cleanup EXIT

export GOCACHE="$WORK/go-build-cache"
export GOPATH="$WORK/go-path"
export GOMODCACHE="$WORK/go-mod-cache"
export GOTMPDIR="$WORK/go-tmp"
export TMPDIR="$WORK/tmp"
export HOME="$WORK/home"
export GITCODE_LIVE_TEST=""
export GITCODE_LIVE_TOKEN=""
export GITCODE_TEST_TOKEN=""
export GITCODE_TOKEN=""

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

run_capture() {
  local name="$1"
  shift
  set +e
  "$@" >"$WORK/$name.out" 2>"$WORK/$name.err"
  local code=$?
  set -e
  if [[ "$code" != "0" ]]; then
    printf '%s\n' "--- $name stdout ---" >&2
    cat "$WORK/$name.out" >&2
    printf '%s\n' "--- $name stderr ---" >&2
    cat "$WORK/$name.err" >&2
    fail "$name exited $code"
  fi
}

assert_log_contains() {
  local name="$1"
  local needle="$2"
  if ! grep -Fq -- "$needle" "$WORK/$name.out" && ! grep -Fq -- "$needle" "$WORK/$name.err"; then
    fail "$name did not emit expected evidence marker: $needle"
  fi
}

mkdir -p "$WORK/product_validation"
cat >"$WORK/product_validation/product_validation_test.go" <<'GOGO'
package productvalidation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/cli"
	"gitcode-mcp/internal/index"
	"gitcode-mcp/internal/mcp"
	"gitcode-mcp/internal/service"
)

type rpcResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      json.RawMessage  `json:"id,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *json.RawMessage `json:"error,omitempty"`
}

type toolResult struct {
	Content           []map[string]any `json:"content"`
	StructuredContent json.RawMessage  `json:"structuredContent"`
}

func TestChunkPolicyAndQueryProductPath(t *testing.T) {
	ctx := context.Background()
	cachePath := t.TempDir() + "/cache.db"
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	repoID := "fixture-a"
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: repoID, Owner: "fixture-owner", Name: "fixture-repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}

	sources := []cache.Source{
		{RepoID: repoID, ID: "ISSUE-1", Kind: "issue", Path: "issues/1.md", Title: "Fixture Issue", Body: "# Issue\n\nThe issue body mentions stable query behavior and shared chunk results.\n\n## Acceptance\n\n- deterministic ids\n- warnings metadata\n", Status: "open", ContentHash: "issue-hash", CreatedAt: fixtureTime(), UpdatedAt: fixtureTime()},
		{RepoID: repoID, ID: "WIKI-HOME", Kind: "wiki", Path: "wiki/Home.md", Title: "Fixture Wiki", Body: "# Home\n\nWiki content documents list chunks and get snippet parity.\n\n## Details\n\nThe wiki page has heading paths and normalized text.\n", Status: "active", ContentHash: "wiki-hash", CreatedAt: fixtureTime(), UpdatedAt: fixtureTime()},
		{RepoID: repoID, ID: "CHANGELOG-1", Kind: "changelog", Path: "CHANGELOG.md", Title: "Fixture Changelog", Body: "# Changelog\n\n2026-06-01 Added offline validation for chunk APIs.\n2026-06-02 Added sliding window policy coverage.\n2026-06-03 Preserved deterministic ranges.\n", Status: "active", ContentHash: "changelog-hash", CreatedAt: fixtureTime(), UpdatedAt: fixtureTime()},
	}

	var firstRun []index.Chunk
	var secondRun []index.Chunk
	for _, source := range sources {
		if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{Source: source}); err != nil {
			t.Fatal(err)
		}
		idxSource := index.SourceRecord{RepoID: source.RepoID, ID: source.ID, RecordID: source.ID, SnapshotID: "snap-fixture", Kind: source.Kind, Path: source.Path, Title: source.Title, Body: source.Body, Status: source.Status, UpdatedAt: source.UpdatedAt}
		parsed := index.ParseSource(idxSource)
		for _, opts := range []index.ChunkOptions{{}, {Policy: index.ChunkPolicySlidingWindow, WindowBytes: 64, OverlapBytes: 16}} {
			chunks := index.ChunkSourceWithOptions(idxSource, parsed, opts)
			repeated := index.ChunkSourceWithOptions(idxSource, parsed, opts)
			if !reflect.DeepEqual(chunks, repeated) {
				t.Fatalf("repeated indexing differed for %s policy %+v", source.ID, opts)
			}
			firstRun = append(firstRun, chunks...)
			secondRun = append(secondRun, repeated...)
			for _, chunk := range chunks {
				if _, err := store.UpsertChunk(ctx, toCacheChunk(chunk)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	if !reflect.DeepEqual(firstRun, secondRun) {
		t.Fatalf("fixture indexing is not deterministic")
	}
	assertNoPolicyCollision(t, firstRun)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	cliList := runCLIChunkResult(t, cachePath, "list-chunks", "--repo", repoID, "--source-id", "ISSUE-1", "--policy", "heading", "--limit", "1")
	assertChunkResultShape(t, cliList, true)
	if cliList.Limit != 1 || cliList.Offset != 0 || cliList.Total < 2 || len(cliList.Chunks) != 1 {
		t.Fatalf("unexpected list pagination: %+v", cliList)
	}
	cliPage := runCLIChunkResult(t, cachePath, "list-chunks", "--repo", repoID, "--source-id", "ISSUE-1", "--offset", "1", "--limit", "1")
	if len(cliPage.Chunks) != 1 || cliPage.Chunks[0].ID == cliList.Chunks[0].ID {
		t.Fatalf("limit/offset ordering is not stable: first=%+v second=%+v", cliList, cliPage)
	}
	cliSliding := runCLIChunkResult(t, cachePath, "list-chunks", "--repo", repoID, "--policy", "sliding_window", "--limit", "20")
	assertChunkResultShape(t, cliSliding, true)
	if cliSliding.Total == 0 {
		t.Fatalf("sliding_window policy is not queryable")
	}
	cliSearch := runCLIChunkResult(t, cachePath, "search-chunks", "--repo", repoID, "--policy", "sliding_window", "sliding")
	assertChunkResultShape(t, cliSearch, true)
	if cliSearch.Total == 0 {
		t.Fatalf("search_chunks did not find sliding_window chunks")
	}
	cliSnippet := runCLIChunkResult(t, cachePath, "get-snippet", "--repo", repoID, "--chunk-id", cliList.Chunks[0].ID)
	assertChunkResultShape(t, cliSnippet, false)
	if cliSnippet.Chunks[0].SnippetText == "" {
		t.Fatalf("snippet text missing: %+v", cliSnippet)
	}

	store2, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	svc := service.New(store2)
	mcpList := callMCPChunkResult(t, svc, "list_chunks", map[string]any{"repo_id": repoID, "source_id": "ISSUE-1", "policy": "heading", "limit": 1})
	assertChunkResultShape(t, mcpList, true)
	if !reflect.DeepEqual(cliList, mcpList) {
		t.Fatalf("CLI/MCP list ChunkQueryResult parity mismatch\ncli=%+v\nmcp=%+v", cliList, mcpList)
	}
	mcpSearch := callMCPChunkResult(t, svc, "search_chunks", map[string]any{"repo_id": repoID, "policy": "sliding_window", "query": "sliding", "limit": 50})
	assertChunkResultShape(t, mcpSearch, true)
	if mcpSearch.Total == 0 {
		t.Fatalf("MCP search_chunks did not find sliding_window chunks")
	}
	mcpSnippet := callMCPChunkResult(t, svc, "get_snippet", map[string]any{"repo_id": repoID, "chunk_id": cliList.Chunks[0].ID})
	assertChunkResultShape(t, mcpSnippet, false)
	if !reflect.DeepEqual(cliSnippet, mcpSnippet) {
		t.Fatalf("CLI/MCP snippet ChunkQueryResult parity mismatch\ncli=%+v\nmcp=%+v", cliSnippet, mcpSnippet)
	}
}

func fixtureTime() time.Time { return time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC) }

func toCacheChunk(chunk index.Chunk) cache.Chunk {
	return cache.Chunk{RepoID: chunk.RepoID, ID: chunk.ID, SourceID: chunk.SourceID, RecordID: chunk.RecordID, SnapshotID: chunk.SnapshotID, ContentHash: chunk.ContentHash, ByteStart: chunk.ByteStart, ByteEnd: chunk.ByteEnd, LineStart: chunk.LineStart, LineEnd: chunk.LineEnd, HeadingPath: append([]string(nil), chunk.HeadingPath...), Text: chunk.Text, NormalizedText: chunk.NormalizedText, InheritedMetadata: copyMap(chunk.InheritedMetadata), OutboundLinks: append([]string(nil), chunk.OutboundLinks...), ResolvedAliases: copyMap(chunk.ResolvedAliases), Policy: string(chunk.Policy)}
}

func copyMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func assertNoPolicyCollision(t *testing.T, chunks []index.Chunk) {
	t.Helper()
	seen := map[string]index.Chunk{}
	policies := map[index.ChunkPolicy]bool{}
	for _, chunk := range chunks {
		if prior, ok := seen[chunk.ID]; ok && prior.Policy != chunk.Policy {
			t.Fatalf("chunk id collision across policies: %s", chunk.ID)
		}
		seen[chunk.ID] = chunk
		policies[chunk.Policy] = true
	}
	if !policies[index.ChunkPolicyHeading] || !policies[index.ChunkPolicySlidingWindow] {
		t.Fatalf("both policies were not emitted: %+v", policies)
	}
}

func runCLIChunkResult(t *testing.T, cachePath string, args ...string) service.ChunkQueryResult {
	t.Helper()
	fullArgs := append([]string{args[0], "--format", "json", "--cache-path", cachePath}, args[1:]...)
	var stdout, stderr bytes.Buffer
	if code := cli.Execute(fullArgs, &stdout, &stderr); code != 0 {
		t.Fatalf("cli %v code=%d stderr=%s stdout=%s", fullArgs, code, stderr.String(), stdout.String())
	}
	assertWarningsFieldVisible(t, stdout.Bytes())
	var result service.ChunkQueryResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode cli result: %v\n%s", err, stdout.String())
	}
	return result
}

func callMCPChunkResult(t *testing.T, svc *service.Service, name string, args map[string]any) service.ChunkQueryResult {
	t.Helper()
	request := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}}
	b, _ := json.Marshal(request)
	var stdout, stderr bytes.Buffer
	srv := mcp.New(strings.NewReader(string(b)+"\n"), &stdout, &stderr, svc)
	if err := srv.Serve(); err != nil {
		t.Fatalf("mcp serve: %v stderr=%s", err, stderr.String())
	}
	var resp rpcResponse
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		t.Fatalf("decode mcp response: %v\n%s", err, stdout.String())
	}
	if resp.Error != nil {
		t.Fatalf("mcp error: %s", string(*resp.Error))
	}
	var tr toolResult
	if err := json.Unmarshal(resp.Result, &tr); err != nil {
		t.Fatalf("decode mcp tool result: %v\n%s", err, stdout.String())
	}
	assertWarningsFieldVisible(t, tr.StructuredContent)
	var result service.ChunkQueryResult
	if err := json.Unmarshal(tr.StructuredContent, &result); err != nil {
		t.Fatalf("decode mcp structuredContent: %v\n%s", err, string(tr.StructuredContent))
	}
	return result
}

func assertWarningsFieldVisible(t *testing.T, raw []byte) {
	t.Helper()
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("invalid json object: %v", err)
	}
	if _, ok := obj["warnings"]; !ok {
		t.Fatalf("warning metadata field omitted from visible response: %s", string(raw))
	}
}

func assertChunkResultShape(t *testing.T, result service.ChunkQueryResult, requireText bool) {
	t.Helper()
	if result.Limit < 0 || result.Offset < 0 || result.Total < len(result.Chunks) || result.Warnings == nil {
		t.Fatalf("pagination/warnings shape missing: %+v", result)
	}
	for _, chunk := range result.Chunks {
		if chunk.ID == "" || chunk.RepoID == "" || chunk.SourceID == "" || chunk.RecordID == "" || chunk.SnapshotID == "" || chunk.Policy == "" || chunk.ContentHash == "" {
			t.Fatalf("chunk missing identity metadata: %+v", chunk)
		}
		if chunk.ByteEnd <= chunk.ByteStart || chunk.LineStart <= 0 || chunk.LineEnd < chunk.LineStart {
			t.Fatalf("chunk missing valid ranges: %+v", chunk)
		}
		if len(chunk.HeadingPath) == 0 {
			t.Fatalf("chunk missing heading path: %+v", chunk)
		}
		if requireText && (chunk.Text == "" || chunk.NormalizedText == "") {
			t.Fatalf("chunk missing normalized text: %+v", chunk)
		}
		if !requireText && chunk.SnippetText == "" {
			t.Fatalf("chunk missing snippet text: %+v", chunk)
		}
		if chunk.ByteStart < 0 || chunk.ByteEnd < 0 {
			t.Fatalf("negative byte range: %+v", chunk)
		}
		_ = fmt.Sprintf("%s", chunk.Policy)
	}
}
GOGO

run_capture focused-index-tests go test ./internal/index -run 'TestChunkPolicyDeterminismAndMetadata|TestChunkPolicyBoundaries|TestChunkQueryContract' -count=1 -v
assert_log_contains focused-index-tests "TestChunkPolicyDeterminismAndMetadata"
assert_log_contains focused-index-tests "TestChunkPolicyBoundaries"
assert_log_contains focused-index-tests "TestChunkQueryContract"

run_capture product-path go test "$WORK/product_validation" -count=1 -v
assert_log_contains product-path "TestChunkPolicyAndQueryProductPath"

run_capture all-tests go test ./...
run_capture diff-check git diff --check

printf 'PASS: scenario-009 chunk policy and query API validation passed\n'
