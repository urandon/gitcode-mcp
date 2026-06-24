#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
cd "$ROOT"

unset GITCODE_TOKEN
unset GITCODE_API_BASE_URL
unset GITCODE_E2E_OWNER
unset GITCODE_E2E_REPO
unset GITCODE_E2E_TOKEN
unset GITCODE_MCP_CONFIG
unset GITCODE_MCP_TEST_KEYCHAIN_TOKEN

export GONOSUMDB='*'
export GOPRIVATE=''

VALIDATION_DIR="$ROOT/tests/design_package/002-cache-provenance-layer-task-1-add-cache-provenance-schema-read-filters-sync-wr/work-$$"
mkdir "$VALIDATION_DIR"
trap 'rm -rf "$VALIDATION_DIR"' EXIT

cat > "$VALIDATION_DIR/cache_provenance_runtime_test.go" <<'GOEOF'
package cacheprovenancevalidation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/service"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

type liveResetter interface {
	ResetLive(context.Context, string) error
}

func requireJSONField(t *testing.T, payload any, path string, want string) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	cur := decoded
	for _, key := range splitPath(path) {
		m, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("%s missing object in %s", path, string(body))
		}
		cur, ok = m[key]
		if !ok {
			t.Fatalf("%s missing in %s", path, string(body))
		}
	}
	if got, ok := cur.(string); !ok || got != want {
		t.Fatalf("%s = %#v, want %q in %s", path, cur, want, string(body))
	}
}

func splitPath(path string) []string {
	out := []string{}
	start := 0
	for i := 0; i <= len(path); i++ {
		if i == len(path) || path[i] == '.' {
			out = append(out, path[start:i])
			start = i + 1
		}
	}
	return out
}

func containsString(haystack []byte, needle []byte) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Part A – schema and provenance constants
// ---------------------------------------------------------------------------

func TestProvenanceConstants(t *testing.T) {
	if string(cache.ProvenanceFixture) != "fixture" || string(cache.ProvenanceLive) != "live" {
		t.Fatal("cache provenance constants for fixture/live are not available or have wrong values")
	}
}

func TestRecordsProvenanceCheckConstraintAcceptsFixtureLive(t *testing.T) {
	ctx := context.Background()
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	repoID := "checktest"
	now := time.Now().UTC()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v5/repos/o/r/issues" {
			fmt.Fprint(w, `[{"id":100,"number":"2","title":"L","body":"b","state":"open","updated_at":"2026-06-24T00:00:00Z"}]`)
		} else if strings.Contains(r.URL.Path, "/comments") {
			fmt.Fprint(w, `[]`)
		} else if strings.Contains(r.URL.Path, ".wiki/contents") {
			fmt.Fprint(w, `[]`)
		}
	}))
	defer server.Close()

	if err := store.UpsertRepo(ctx, cache.RepositoryBinding{
		RepoID: repoID, Owner: "o", Name: "r", APIBaseURL: server.URL,
		Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki},
		DisplayName: "checktest", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	fixtureSvc := service.New(store)
	_, err = fixtureSvc.SyncToCache(ctx, service.SyncRequest{
		RepoID: repoID, StableID: "CHK-1", RemoteAlias: "issue:1", IdempotencyKey: "chk-fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.GetRecord(ctx, repoID, "CHK-1"); err != nil {
		t.Fatal(err)
	}

	liveSvc, err := service.NewWithMode(store, gitcode.ProviderModeLive, "test-token", service.ServiceConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = liveSvc.BulkSyncIssues(ctx, service.BulkSyncRequest{
		RepoID: repoID, IdempotencyKey: "chk-live", PerPage: 100,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify records table has provenance=live for the live-synced record
	record, err := store.GetRecord(ctx, repoID, "ISSUE-2")
	if err != nil {
		t.Fatalf("live-synced record not found in records table: %v", err)
	}
	if record.Provenance != cache.ProvenanceLive {
		t.Fatalf("records provenance for live-synced record = %q, want %q", record.Provenance, cache.ProvenanceLive)
	}

	// Verify records table has provenance=fixture for the fixture-synced record
	fixtureRecord, err := store.GetRecord(ctx, repoID, "CHK-1")
	if err != nil {
		t.Fatal(err)
	}
	if fixtureRecord.Provenance != cache.ProvenanceFixture {
		t.Fatalf("records provenance for fixture-synced record = %q, want %q", fixtureRecord.Provenance, cache.ProvenanceFixture)
	}
}

func TestSourcesProvenanceColumnExists(t *testing.T) {
	ctx := context.Background()
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	repoID := "sourcetest"
	now := time.Now().UTC()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{
		RepoID: repoID, Owner: "o", Name: "r", APIBaseURL: "http://localhost",
		Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues},
		DisplayName: "sourcetest", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	svc := service.New(store)
	_, err = svc.SyncToCache(ctx, service.SyncRequest{
		RepoID: repoID, StableID: "SRC-1", RemoteAlias: "issue:1", IdempotencyKey: "src-fixture",
	})
	if err != nil {
		t.Fatal(err)
	}

	src, err := store.GetSource(ctx, "SRC-1")
	if err != nil {
		t.Fatal(err)
	}
	if src.Provenance != cache.ProvenanceFixture {
		t.Fatalf("sources provenance = %q, want %q", src.Provenance, cache.ProvenanceFixture)
	}
}

// ---------------------------------------------------------------------------
// Part B – sync-provenance wiring and read filters
// ---------------------------------------------------------------------------

func TestScenario002CacheProvenanceRuntime(t *testing.T) {
	ctx := context.Background()
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// -- Setup httptest GitCode /api/v5 server for live sync
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-token" {
			t.Errorf("live sync did not use test token: got %q", auth)
		}
		switch r.URL.Path {
		case "/api/v5/repos/example-owner/example-repo/issues":
			fmt.Fprint(w, `[{"id":4109571,"number":"3","title":"Live issue","body":"live body","state":"open","updated_at":"2026-06-24T00:00:00Z"}]`)
		case "/api/v5/repos/example-owner/example-repo/issues/3/comments":
			fmt.Fprint(w, `[]`)
		case "/api/v5/repos/example-owner/example-repo.wiki/contents":
			fmt.Fprint(w, `[]`)
		default:
			t.Errorf("unexpected live route %s", r.URL.Path)
		}
	}))
	defer server.Close()

	now := time.Now().UTC()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{
		RepoID:      "fixture-a",
		Owner:       "example-owner",
		Name:        "example-repo",
		APIBaseURL:  server.URL,
		Scopes:      []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki},
		DisplayName: "fixture-a",
		CreatedAt:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatal(err)
	}

	// -- Step B4: Fixture-mode sync writes provenance=fixture
	fixtureSvc := service.New(store)
	fixtureResult, err := fixtureSvc.SyncToCache(ctx, service.SyncRequest{
		RepoID:         "fixture-a",
		StableID:       "FIXTURE-ONLY",
		RemoteAlias:    "issue:42",
		IdempotencyKey: "scenario-002-fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	requireJSONField(t, fixtureResult, "record.provenance", "fixture")

	fixtureGet, err := fixtureSvc.GetSource(ctx, service.GetSourceRequest{RepoID: "fixture-a", ID: "FIXTURE-ONLY"})
	if err != nil {
		t.Fatal(err)
	}
	requireJSONField(t, fixtureGet, "provenance", "fixture")

	// -- Step B5: Live-mode sync writes provenance=live
	liveSvc, err := service.NewWithMode(store, gitcode.ProviderModeLive, "test-token", service.ServiceConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	liveResult, err := liveSvc.BulkSyncIssues(ctx, service.BulkSyncRequest{
		RepoID:         "fixture-a",
		IdempotencyKey: "scenario-002-live",
		PerPage:        100,
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(liveResult)
	if !json.Valid(body) {
		t.Fatalf("live sync result is not valid JSON: %s", string(body))
	}
	if !containsString(body, []byte(`"provenance":"live"`)) {
		t.Fatalf("live sync result does not expose provenance=live: %s", string(body))
	}

	// -- Step B4/B5-cont: Verify records table provenance
	record, err := store.GetRecord(ctx, "fixture-a", "FIXTURE-ONLY")
	if err != nil {
		t.Fatalf("fixture record not found in records table: %v", err)
	}
	if record.Provenance != cache.ProvenanceFixture {
		t.Fatalf("records provenance for fixture sync = %q, want %q", record.Provenance, cache.ProvenanceFixture)
	}

	liveRecord, err := store.GetRecord(ctx, "fixture-a", "ISSUE-3")
	if err != nil {
		t.Fatalf("live record not found in records table: %v", err)
	}
	if liveRecord.Provenance != cache.ProvenanceLive {
		t.Fatalf("records provenance for live sync = %q, want %q", liveRecord.Provenance, cache.ProvenanceLive)
	}

	// -- Step B6: SourceFilter has Provenance field and SetProvenance setter
	filter := cache.SourceFilter{RepoID: "fixture-a"}
	filterValue := any(&filter)
	setter, ok := filterValue.(interface{ SetProvenance(cache.Provenance) })
	if !ok {
		t.Fatal("cache.SourceFilter has no executable provenance filter setter")
	}
	setter.SetProvenance(cache.ProvenanceLive)
	if !ok {
		t.Fatal("cache.SourceFilter does not support SetProvenance")
	}

	// -- Step B7: SearchQuery has Provenance field and SetProvenance setter
	searchQuery := cache.SearchQuery{RepoID: "fixture-a", Query: "Live"}
	queryValue := any(&searchQuery)
	searchSetter, ok := queryValue.(interface{ SetProvenance(cache.Provenance) })
	if !ok {
		t.Fatal("cache.SearchQuery has no executable provenance filter setter")
	}
	searchSetter.SetProvenance(cache.ProvenanceLive)

	// -- Step B8: RecordFilter has a Provenance field
	_ = cache.RecordFilter{RepoID: "fixture-a", Provenance: cache.ProvenanceFixture}

	// -- Step B9: ListRecords filters by provenance when RecordFilter.Provenance is set
	fixtureRecords, err := store.ListRecords(ctx, cache.RecordFilter{RepoID: "fixture-a", Provenance: cache.ProvenanceFixture})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range fixtureRecords {
		if r.Provenance != cache.ProvenanceFixture {
			t.Fatalf("fixture-filtered ListRecords returned record %s with provenance %s", r.ID, r.Provenance)
		}
	}

	liveRecordsOnly, err := store.ListRecords(ctx, cache.RecordFilter{RepoID: "fixture-a", Provenance: cache.ProvenanceLive})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range liveRecordsOnly {
		if r.Provenance != cache.ProvenanceLive {
			t.Fatalf("live-filtered ListRecords returned record %s with provenance %s", r.ID, r.Provenance)
		}
	}

	// -- Step B10: SearchRecords filters by provenance when SearchQuery.Provenance is set
	fixtureSearch, err := store.SearchRecords(ctx, cache.SearchQuery{RepoID: "fixture-a", Query: "FIXTURE", Provenance: cache.ProvenanceFixture})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range fixtureSearch {
		if r.Provenance != cache.ProvenanceFixture {
			t.Fatalf("fixture-filtered SearchRecords returned result %s with provenance %s", r.ID, r.Provenance)
		}
	}

	liveSearch, err := store.SearchRecords(ctx, cache.SearchQuery{RepoID: "fixture-a", Query: "Live", Provenance: cache.ProvenanceLive})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range liveSearch {
		if r.Provenance != cache.ProvenanceLive {
			t.Fatalf("live-filtered SearchRecords returned result %s with provenance %s", r.ID, r.Provenance)
		}
	}

	// -- Step B11: ListSources exposes both fixture and live provenance
	listAll, err := liveSvc.ListSources(ctx, service.ListSourcesRequest{RepoID: "fixture-a"})
	if err != nil {
		t.Fatal(err)
	}
	listBody, _ := json.Marshal(listAll)
	if !containsString(listBody, []byte(`"provenance":"fixture"`)) || !containsString(listBody, []byte(`"provenance":"live"`)) {
		t.Fatalf("list output does not expose both fixture/live provenance: %s", string(listBody))
	}

	// -- Step B12: SearchSources exposes provenance=live
	searchLive, err := liveSvc.SearchSources(ctx, service.SearchSourcesRequest{RepoID: "fixture-a", Query: "Live"})
	if err != nil {
		t.Fatal(err)
	}
	searchBody, _ := json.Marshal(searchLive)
	if !containsString(searchBody, []byte(`"provenance":"live"`)) {
		t.Fatalf("search output does not expose live provenance: %s", string(searchBody))
	}

	// -- Step B13: GetSource for live record exposes provenance=live
	liveGet, err := liveSvc.GetSource(ctx, service.GetSourceRequest{RepoID: "fixture-a", ID: "ISSUE-3"})
	if err != nil {
		t.Fatal(err)
	}
	requireJSONField(t, liveGet, "provenance", "live")

	// -- Step B14: GetSyncStatus for live record exposes provenance=live
	liveStatus, err := liveSvc.GetSyncStatus(ctx, service.SyncStatusRequest{RepoID: "fixture-a", ID: "ISSUE-3"})
	if err != nil {
		t.Fatal(err)
	}
	requireJSONField(t, liveStatus, "provenance", "live")

	// -- Step B15: Batch SyncStatus for all sources exposes provenance in each result
	batchStatus, err := liveSvc.SyncStatus(ctx, service.ListSourcesRequest{RepoID: "fixture-a"})
	if err != nil {
		t.Fatal(err)
	}
	batchBody, _ := json.Marshal(batchStatus)
	if !containsString(batchBody, []byte(`"provenance":"fixture"`)) || !containsString(batchBody, []byte(`"provenance":"live"`)) {
		t.Fatalf("batch sync_status output does not expose both fixture/live provenance: %s", string(batchBody))
	}
	for _, r := range batchStatus.Results {
		if r.Provenance == "" {
			t.Fatalf("batch sync_status result for %s has empty provenance", r.SourceID)
		}
	}

	// -- Step B16: SourceFilter provenance filtering works
	fixtureFilter := cache.SourceFilter{RepoID: "fixture-a"}
	fixtureFilter.SetProvenance(cache.ProvenanceFixture)
	fixtureOnly, err := store.ListSources(ctx, fixtureFilter)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range fixtureOnly {
		if s.Provenance != cache.ProvenanceFixture {
			t.Fatalf("fixture-filtered list contains non-fixture source %s with provenance %s", s.ID, s.Provenance)
		}
	}

	liveFilter := cache.SourceFilter{RepoID: "fixture-a"}
	liveFilter.SetProvenance(cache.ProvenanceLive)
	liveOnly, err := store.ListSources(ctx, liveFilter)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range liveOnly {
		if s.Provenance != cache.ProvenanceLive {
			t.Fatalf("live-filtered list contains non-live source %s with provenance %s", s.ID, s.Provenance)
		}
	}

	// -- Step D16: ResetLive is an executable product path on the Store interface
	resetStore, ok := any(store).(liveResetter)
	if !ok {
		t.Fatal("cache store does not expose executable ResetLive(ctx, repoID) product path")
	}

	// -- Step D17: ResetLive clears live-origin records from both sources and records
	if err := resetStore.ResetLive(ctx, "fixture-a"); err != nil {
		t.Fatal(err)
	}

	// -- Step D18: Live-origin sources are not readable after ResetLive
	if _, err := liveSvc.GetSource(ctx, service.GetSourceRequest{RepoID: "fixture-a", ID: "ISSUE-3"}); err == nil {
		t.Fatal("live-origin source remained readable after cache reset --live equivalent")
	}

	// -- Step D19: Fixture-origin sources survive live reset
	if _, err := liveSvc.GetSource(ctx, service.GetSourceRequest{RepoID: "fixture-a", ID: "FIXTURE-ONLY"}); err != nil {
		t.Fatalf("fixture-origin source was removed by live reset: %v", err)
	}

	// -- Step D20: After reset, list only shows fixture sources
	afterReset, err := liveSvc.ListSources(ctx, service.ListSourcesRequest{RepoID: "fixture-a"})
	if err != nil {
		t.Fatal(err)
	}
	afterBody, _ := json.Marshal(afterReset)
	if containsString(afterBody, []byte(`"provenance":"live"`)) {
		t.Fatalf("list output contains live provenance after reset: %s", string(afterBody))
	}
	if !containsString(afterBody, []byte(`"provenance":"fixture"`)) {
		t.Fatalf("list output missing fixture provenance after reset: %s", string(afterBody))
	}

	// -- Step D21: Search after reset shows no live-origin results
	searchAfterReset, err := liveSvc.SearchSources(ctx, service.SearchSourcesRequest{RepoID: "fixture-a", Query: "Live"})
	if err != nil && !strings.Contains(err.Error(), "not found") {
		t.Fatal(err)
	}
	if searchAfterReset.Results != nil {
		for _, r := range searchAfterReset.Results {
			if r.Provenance == "live" {
				t.Fatalf("search after reset contains live-provenance result %s", r.ID)
			}
		}
	}

	// -- Step D22: Fixture-origin records cannot masquerade as live through read paths
	fixtureGetAfterReset, err := liveSvc.GetSource(ctx, service.GetSourceRequest{RepoID: "fixture-a", ID: "FIXTURE-ONLY"})
	if err != nil {
		t.Fatal(err)
	}
	requireJSONField(t, fixtureGetAfterReset, "provenance", "fixture")

	fixtureStatusAfterReset, err := liveSvc.GetSyncStatus(ctx, service.SyncStatusRequest{RepoID: "fixture-a", ID: "FIXTURE-ONLY"})
	if err != nil {
		t.Fatal(err)
	}
	requireJSONField(t, fixtureStatusAfterReset, "provenance", "fixture")
}

// ---------------------------------------------------------------------------
// Part D - ResetLive clears records table live-provenance rows directly
// ---------------------------------------------------------------------------

func TestResetLiveClearsRecordsTableLiveRows(t *testing.T) {
	ctx := context.Background()
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	now := time.Now().UTC()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v5/repos/o/r/issues" {
			fmt.Fprint(w, `[{"id":200,"number":"5","title":"Live","body":"live","state":"open","updated_at":"2026-06-24T00:00:00Z"}]`)
		} else if strings.Contains(r.URL.Path, "/comments") {
			fmt.Fprint(w, `[]`)
		} else if strings.Contains(r.URL.Path, ".wiki/contents") {
			fmt.Fprint(w, `[]`)
		}
	}))
	defer server.Close()

	if err := store.UpsertRepo(ctx, cache.RepositoryBinding{
		RepoID: "reset-test", Owner: "o", Name: "r", APIBaseURL: server.URL,
		Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki},
		DisplayName: "reset-test", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	fixtureSvc := service.New(store)
	_, err = fixtureSvc.SyncToCache(ctx, service.SyncRequest{
		RepoID: "reset-test", StableID: "FIX-1", RemoteAlias: "issue:1", IdempotencyKey: "fix-1",
	})
	if err != nil {
		t.Fatal(err)
	}

	liveSvc, err := service.NewWithMode(store, gitcode.ProviderModeLive, "test-token", service.ServiceConfig{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	_, err = liveSvc.BulkSyncIssues(ctx, service.BulkSyncRequest{
		RepoID: "reset-test", IdempotencyKey: "live-1", PerPage: 100,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify both exist in records table
	if _, err := store.GetRecord(ctx, "reset-test", "FIX-1"); err != nil {
		t.Fatalf("fixture record not found before reset: %v", err)
	}
	if _, err := store.GetRecord(ctx, "reset-test", "ISSUE-5"); err != nil {
		t.Fatalf("live record not found before reset: %v", err)
	}

	// ResetLive
	resetter, ok := any(store).(liveResetter)
	if !ok {
		t.Fatal("store does not expose ResetLive")
	}
	if err := resetter.ResetLive(ctx, "reset-test"); err != nil {
		t.Fatal(err)
	}

	// Fixture record must survive
	if _, err := store.GetRecord(ctx, "reset-test", "FIX-1"); err != nil {
		t.Fatalf("fixture record was cleared by ResetLive: %v", err)
	}

	// Live record must be gone
	if _, err := store.GetRecord(ctx, "reset-test", "ISSUE-5"); err == nil {
		t.Fatal("live record survived ResetLive")
	}
}

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
GOEOF

cat > "$VALIDATION_DIR/go.mod" <<GOEOF
module gitcode-mcp/tests/design_package/002-cache-provenance-layer-task-1-add-cache-provenance-schema-read-filters-sync-wr/work

go 1.22

require gitcode-mcp v0.0.0

replace gitcode-mcp => $ROOT
GOEOF

(
  cd "$VALIDATION_DIR"
  GO111MODULE=on GOWORK=off go mod tidy
  GO111MODULE=on GOWORK=off go test ./... -count=1 -v
)

# Also run the production tests
go test ./... -count=1
git diff --check
