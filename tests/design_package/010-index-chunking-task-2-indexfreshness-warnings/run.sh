#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
SCENARIO_DIR="$ROOT/tests/design_package/010-index-chunking-task-2-indexfreshness-warnings"
TEST_FILE="$SCENARIO_DIR/indexfreshness_product_test.go"
trap 'rm -f "$TEST_FILE"' EXIT

cat > "$TEST_FILE" <<'GOEOF'
package indexfreshness_product_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/cli"
	"gitcode-mcp/internal/index"
	"gitcode-mcp/internal/mcp"
	"gitcode-mcp/internal/service"
)

func TestIndexFreshnessProductSurfaces(t *testing.T) {
	ctx := context.Background()
	cachePath := filepath.Join(t.TempDir(), "fixture.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	seedFreshnessFixture(t, ctx, store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	insertBrokenLink(t, ctx, cachePath)

	firstStale := runCLI(t, "stale-index", "--repo", "fixture-a", "--cache-path", cachePath, "--format", "json")
	secondStale := runCLI(t, "stale-index", "--repo", "fixture-a", "--cache-path", cachePath, "--format", "json")
	if firstStale.stdout != secondStale.stdout {
		t.Fatalf("stale-index output is not deterministic\nfirst=%s\nsecond=%s", firstStale.stdout, secondStale.stdout)
	}
	var stale service.StaleIndexResult
	decodeJSON(t, firstStale.stdout, &stale)
	wantWarnings := []string{"stale_index", "link_stale_only", "missing_index", "stale_index_revision"}
	gotWarnings := warningCodes(stale.Warnings)
	if !reflect.DeepEqual(gotWarnings, wantWarnings) {
		t.Fatalf("stale-index warnings = %#v, want %#v; full=%s", gotWarnings, wantWarnings, firstStale.stdout)
	}
	wantRecordOrder := []string{"DOC-CONTENT", "DOC-FRESH", "DOC-LINK", "DOC-MISSING", "DOC-REVISION"}
	gotRecordOrder := make([]string, 0, len(stale.Records))
	states := map[string]string{}
	warningBySource := map[string]string{}
	for _, record := range stale.Records {
		gotRecordOrder = append(gotRecordOrder, record.SourceID)
		states[record.SourceID] = string(record.State)
		warningBySource[record.SourceID] = record.WarningCode
	}
	if !reflect.DeepEqual(gotRecordOrder, wantRecordOrder) {
		t.Fatalf("stale-index record order = %#v, want %#v", gotRecordOrder, wantRecordOrder)
	}
	if states["DOC-FRESH"] != "fresh" || warningBySource["DOC-FRESH"] != "" {
		t.Fatalf("fresh record should have no freshness warning: states=%#v warnings=%#v", states, warningBySource)
	}
	for source, want := range map[string]string{"DOC-MISSING": "missing_index", "DOC-CONTENT": "stale_index", "DOC-REVISION": "stale_index_revision", "DOC-LINK": "link_stale_only"} {
		if warningBySource[source] != want {
			t.Fatalf("%s warning = %q, want %q; warnings=%#v", source, warningBySource[source], want, warningBySource)
		}
	}

	statusOut := runCLI(t, "cache-status", "--repo", "fixture-a", "--cache-path", cachePath, "--format", "json")
	var status service.CacheStatusResult
	decodeJSON(t, statusOut.stdout, &status)
	if status.IndexFreshnessWarnings != 4 {
		t.Fatalf("cache-status freshness warnings = %d, want 4; output=%s", status.IndexFreshnessWarnings, statusOut.stdout)
	}
	for _, code := range wantWarnings {
		if status.IndexFreshnessByWarning[code] != 1 {
			t.Fatalf("cache-status warning count %s = %d, want 1; output=%s", code, status.IndexFreshnessByWarning[code], statusOut.stdout)
		}
	}

	snippetOut := runCLI(t, "get-snippet", "--repo", "fixture-a", "--cache-path", cachePath, "--format", "json", "--source-id", "DOC-MISSING")
	var snippet service.ChunkQueryResult
	decodeJSON(t, snippetOut.stdout, &snippet)
	if snippet.Total != 0 || len(snippet.Warnings) != 1 || snippet.Warnings[0].Code != "missing_index" {
		t.Fatalf("get-snippet missing-index result = %+v; output=%s", snippet, snippetOut.stdout)
	}

	legacyExport := runCLI(t, "export", "--repo", "fixture-a", "--cache-path", cachePath, "--format", "json")
	if !strings.Contains(legacyExport.stdout, "missing_index") || !strings.Contains(legacyExport.stdout, "stale_index_revision") || !strings.Contains(legacyExport.stdout, "link_stale_only") {
		t.Fatalf("export snapshot did not include visible freshness warnings: %s", legacyExport.stdout)
	}
	exportSnapshot := runCLIAllowFailure("export-snapshot", "--repo", "fixture-a", "--cache-path", cachePath, "--format", "json")
	if exportSnapshot.code != 0 {
		t.Errorf("CLI export-snapshot required by acceptance is unavailable or failing: code=%d stdout=%q stderr=%q", exportSnapshot.code, exportSnapshot.stdout, exportSnapshot.stderr)
	} else if !strings.Contains(exportSnapshot.stdout, "missing_index") {
		t.Errorf("export-snapshot omitted missing_index warning metadata: %s", exportSnapshot.stdout)
	}

	mcpSnippet := callMCPTool(t, cachePath, "get_snippet", map[string]any{"repo_id": "fixture-a", "source_id": "DOC-MISSING"})
	if !strings.Contains(mcpSnippet, "missing_index") {
		t.Fatalf("MCP get_snippet omitted missing_index warning metadata: %s", mcpSnippet)
	}
	mcpStale := callMCPToolAllowError(t, cachePath, "stale_index_report", map[string]any{"repo_id": "fixture-a"})
	if mcpStale.errText != "" {
		t.Errorf("MCP stale_index_report required by acceptance is unavailable or failing: %s", mcpStale.errText)
	} else if !strings.Contains(mcpStale.result, "missing_index") || !strings.Contains(mcpStale.result, "stale_index_revision") || !strings.Contains(mcpStale.result, "link_stale_only") {
		t.Errorf("MCP stale_index_report omitted required warnings: %s", mcpStale.result)
	}
}

func seedFreshnessFixture(t *testing.T, ctx context.Context, store *cache.SQLiteStore) {
	t.Helper()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	sources := []cache.Source{
		{RepoID: "fixture-a", ID: "DOC-CONTENT", Kind: "doc", Path: "docs/content.md", Title: "Content stale", Body: "new content body", Status: "ready", ContentHash: index.ContentHash("new content body"), CreatedAt: base, UpdatedAt: base},
		{RepoID: "fixture-a", ID: "DOC-FRESH", Kind: "doc", Path: "docs/fresh.md", Title: "Fresh", Body: "fresh body", Status: "ready", ContentHash: index.ContentHash("fresh body"), CreatedAt: base, UpdatedAt: base},
		{RepoID: "fixture-a", ID: "DOC-LINK", Kind: "doc", Path: "docs/link.md", Title: "Link stale", Body: "link body", Status: "ready", ContentHash: index.ContentHash("link body"), CreatedAt: base, UpdatedAt: base},
		{RepoID: "fixture-a", ID: "DOC-MISSING", Kind: "doc", Path: "docs/missing.md", Title: "Missing index", Body: "missing body", Status: "ready", ContentHash: index.ContentHash("missing body"), CreatedAt: base, UpdatedAt: base},
		{RepoID: "fixture-a", ID: "DOC-REVISION", Kind: "doc", Path: "docs/revision.md", Title: "Revision stale", Body: "revision body", Status: "ready", ContentHash: index.ContentHash("revision body"), CreatedAt: base, UpdatedAt: base.Add(time.Hour)},
	}
	for _, source := range sources {
		status := &cache.SyncStatus{RepoID: source.RepoID, SourceID: source.ID, RemoteType: "wiki", RemoteID: source.ID, RemoteRevision: "rev-1", Status: "fresh", LastFetchedAt: base}
		if source.ID == "DOC-REVISION" {
			status.RemoteRevision = "rev-2"
			status.LastFetchedAt = base.Add(time.Hour)
		}
		graph := cache.SourceGraph{Source: source, SyncStatus: status}
		if err := store.UpsertSourceGraph(ctx, graph); err != nil {
			t.Fatal(err)
		}
	}
	chunks := []cache.Chunk{
		fixtureChunk(sources[0], "old-content-hash", "rev-1", base),
		fixtureChunk(sources[1], sources[1].ContentHash, "rev-1", base),
		fixtureChunk(sources[2], sources[2].ContentHash, "rev-1", base),
		fixtureChunk(sources[4], sources[4].ContentHash, "rev-1", base),
	}
	for _, chunk := range chunks {
		if _, err := store.UpsertChunk(ctx, chunk); err != nil {
			t.Fatal(err)
		}
	}
}

func insertBrokenLink(t *testing.T, ctx context.Context, cachePath string) {
	t.Helper()
	db, err := sql.Open("sqlite", cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO links (repo_id, source_id, target_id, kind, text) VALUES (?, ?, ?, ?, ?)`, "fixture-a", "DOC-LINK", "DOC-ABSENT", "mentions", "DOC-ABSENT"); err != nil {
		t.Fatal(err)
	}
}

func fixtureChunk(source cache.Source, hash string, revision string, updated time.Time) cache.Chunk {
	return cache.Chunk{RepoID: source.RepoID, ID: "chunk-" + source.ID, SourceID: source.ID, RecordID: source.ID, ContentHash: hash, ByteStart: 0, ByteEnd: len(source.Body), LineStart: 1, LineEnd: 1, HeadingPath: []string{source.Title}, Text: source.Body, NormalizedText: strings.ToLower(source.Body), InheritedMetadata: map[string]string{"remote_revision": revision, "sync_revision": revision, "source_updated_at": updated.Format(time.RFC3339Nano)}, Policy: "heading"}
}

type cliResult struct{ code int; stdout, stderr string }

func runCLI(t *testing.T, args ...string) cliResult {
	t.Helper()
	result := runCLIAllowFailure(args...)
	if result.code != 0 {
		t.Fatalf("gitcode-mcp %v failed: code=%d stdout=%q stderr=%q", args, result.code, result.stdout, result.stderr)
	}
	return result
}

func runCLIAllowFailure(args ...string) cliResult {
	var stdout, stderr bytes.Buffer
	code := cli.Execute(args, &stdout, &stderr)
	return cliResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func callMCPTool(t *testing.T, cachePath string, name string, arguments map[string]any) string {
	t.Helper()
	result := callMCPToolAllowError(t, cachePath, name, arguments)
	if result.errText != "" {
		t.Fatalf("MCP tool %s returned error: %s", name, result.errText)
	}
	return result.result
}

type mcpToolResult struct{ result, errText string }

func callMCPToolAllowError(t *testing.T, cachePath string, name string, arguments map[string]any) mcpToolResult {
	t.Helper()
	ctx := context.Background()
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := service.New(store)
	clientR, serverW := io.Pipe()
	serverR, clientW := io.Pipe()
	conn := &pipeConn{Reader: clientR, Writer: clientW}
	srv := mcp.New(serverR, serverW, io.Discard, svc)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = srv.Serve() }()
	defer func() { _ = conn.Close(); wg.Wait() }()
	request := map[string]any{"jsonrpc": "2.0", "id": name, "method": "tools/call", "params": map[string]any{"name": name, "arguments": arguments}}
	payload, _ := json.Marshal(request)
	_, _ = conn.Write(append(payload, '\n'))
	line := readLine(t, conn)
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int             `json:"code"`
			Message string          `json:"message"`
			Data    json.RawMessage `json:"data"`
		} `json:"error"`
	}
	decodeJSON(t, string(line), &resp)
	if resp.Error != nil {
		return mcpToolResult{errText: string(line)}
	}
	return mcpToolResult{result: string(resp.Result)}
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
		_ = rc.Close()
	}
	if wc, ok := c.Writer.(io.WriteCloser); ok {
		_ = wc.Close()
	}
	return nil
}

func readLine(t *testing.T, r io.Reader) []byte {
	t.Helper()
	buf := make([]byte, 0, 4096)
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

func decodeJSON(t *testing.T, raw string, out any) {
	t.Helper()
	if err := json.Unmarshal([]byte(raw), out); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, raw)
	}
}

func warningCodes(warnings []index.IndexWarning) []string {
	codes := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		codes = append(codes, warning.Code)
	}
	return codes
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
GOEOF

cd "$ROOT"
go test ./internal/index -run 'TestFreshnessReportClassifications|TestChunkQueryFreshnessWarnings' -count=1
go test ./tests/design_package/010-index-chunking-task-2-indexfreshness-warnings -run TestIndexFreshnessProductSurfaces -count=1
