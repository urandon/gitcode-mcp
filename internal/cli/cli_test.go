package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/service"
)

func TestHelpReturnsSuccess(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Execute(nil, &stdout, &stderr)

	if code != 0 {
		t.Fatalf("Execute(nil) code = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "gitcode-mcp") {
		t.Fatalf("help output did not include program name: %q", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestMinimumReplacementBar(t *testing.T) {
	factory := cacheBackedFactory(t)
	cases := [][]string{
		{"search", "--repo", "fixture-a", "backlog"},
		{"list", "--repo", "fixture-a", "--kind", "task", "--status", "ready"},
		{"get", "--repo", "fixture-a", "DOC-123"},
		{"backlinks", "--repo", "fixture-a", "DOC-123"},
		{"get-snippet", "--repo", "fixture-a", "DOC-123", "--line-start", "1", "--line-end", "1"},
		{"list-chunks", "--repo", "fixture-a"},
		{"recent", "--repo", "fixture-a"},
		{"link-check", "--repo", "fixture-a"},
		{"stale-index", "--repo", "fixture-a"},
		{"cache-status", "--repo", "fixture-a"},
		{"export", "--repo", "fixture-a"},
		{"diff", "--repo", "fixture-a"},
	}
	for _, args := range cases {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := executeWithFactory(args, &stdout, &stderr, factory)
		if code != 0 {
			t.Fatalf("%v code = %d stderr=%q stdout=%q", args, code, stderr.String(), stdout.String())
		}
		if stdout.Len() == 0 && args[0] != "link-check" {
			t.Fatalf("%v produced no output", args)
		}
	}
}

func TestCLIRepoScopedDuplicateAlias(t *testing.T) {
	store := populatedStore(t)
	defer store.Close()
	ctx := context.Background()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-b", Owner: "owner-b", Name: "repo-b", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{Source: cache.Source{RepoID: "fixture-a", ID: "ISSUE-42", Kind: "issue", Path: "fixture-a/issues/42.md", Title: "Fixture A", Body: "fixture-a scoped body", Status: "open", ContentHash: "a42"}, Identities: []cache.Identity{{RepoID: "fixture-a", AliasType: "issue", Alias: "42", Remote: cache.RemoteAlias{Type: "issue", ID: "42"}}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{Source: cache.Source{RepoID: "fixture-b", ID: "ISSUE-42", Kind: "issue", Path: "fixture-b/issues/42.md", Title: "Fixture B", Body: "fixture-b scoped body", Status: "open", ContentHash: "b42"}, Identities: []cache.Identity{{RepoID: "fixture-b", AliasType: "issue", Alias: "42", Remote: cache.RemoteAlias{Type: "issue", ID: "42"}}}}); err != nil {
		t.Fatal(err)
	}
	factory := func(context.Context, string) (queryService, func() error, error) { return service.New(store), nil, nil }
	var outA, errA bytes.Buffer
	if code := executeWithFactory([]string{"get", "--repo", "fixture-a", "issue:42"}, &outA, &errA, factory); code != 0 {
		t.Fatalf("fixture-a code=%d err=%q", code, errA.String())
	}
	var outB, errB bytes.Buffer
	if code := executeWithFactory([]string{"get", "--repo", "fixture-b", "issue:42"}, &outB, &errB, factory); code != 0 {
		t.Fatalf("fixture-b code=%d err=%q", code, errB.String())
	}
	if !strings.Contains(outA.String(), "repo_id: fixture-a") || strings.Contains(outA.String(), "fixture-b scoped body") {
		t.Fatalf("fixture-a output crossed scope: %q", outA.String())
	}
	if !strings.Contains(outB.String(), "repo_id: fixture-b") || strings.Contains(outB.String(), "fixture-a scoped body") {
		t.Fatalf("fixture-b output crossed scope: %q", outB.String())
	}
	var unscopedOut, unscopedErr bytes.Buffer
	if code := executeWithFactory([]string{"get", "issue:42"}, &unscopedOut, &unscopedErr, factory); code != 4 || !strings.Contains(unscopedErr.String(), "repo_required") {
		t.Fatalf("unscoped code=%d err=%q", code, unscopedErr.String())
	}
}

func TestCacheStatusJSON(t *testing.T) {
	store := populatedStore(t)
	defer store.Close()
	now := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	if err := store.UpsertRecordGraph(context.Background(), cache.RecordGraph{
		Record:     cache.Record{RepoID: "fixture-a", ID: "ISSUE-1", Type: "issue", Path: "issues/1.md", Title: "Issue", Body: "body", Status: "open", ContentHash: "h", Provenance: cache.ProvenanceRemote, RemoteType: "issue", RemoteID: "1", CreatedAt: now, UpdatedAt: now},
		Comments:   []cache.RecordComment{{CommentID: "c1", Author: "fixture-user", Body: "comment", ContentHash: "hc", CreatedAt: now, UpdatedAt: now}},
		Identities: []cache.Identity{{AliasType: "issue", Alias: "1", Remote: cache.RemoteAlias{Type: "issue", ID: "1"}}},
		SyncEvents: []cache.SyncEvent{{ID: "sync-1", RemoteType: "issue", RemoteID: "1", RemoteRevision: "r1", Status: "fresh", IdempotencyKey: "sync-1", Message: "fixture", CreatedAt: now}},
		AuditTrail: []cache.AuditTrailEntry{{ID: "audit-1", Operation: "sync", Status: "success", CreatedAt: now}},
		Snapshots:  []cache.Snapshot{{ID: "snap-1", Format: "json", ContentHash: "sh", RecordCount: 1, CreatedAt: now, Chunks: []cache.SnapshotChunk{{ChunkID: "chunk-1", RecordID: "ISSUE-1", LineStart: 1, LineEnd: 1}}}},
	}); err != nil {
		t.Fatal(err)
	}
	factory := func(context.Context, string) (queryService, func() error, error) { return service.New(store), nil, nil }
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"cache-status", "--repo", "fixture-a", "--format", "json"}, &stdout, &stderr, factory)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var result service.CacheStatusResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if !result.WALCapable || result.Records != 1 || result.Comments != 1 || result.IdentityAliases != 1 || result.SyncEvents != 1 || result.AuditRows != 1 || result.Snapshots != 1 || result.SnapshotChunks != 1 {
		t.Fatalf("cache-status result = %#v", result)
	}
}

func TestSearchJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"search", "--repo", "fixture-a", "backlog", "--format", "json"}, &stdout, &stderr, cacheBackedFactory(t))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var results service.SearchSourcesResult
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatalf("invalid json: %v: %q", err, stdout.String())
	}
	if results.RepoID != "fixture-a" || results.Query != "backlog" || len(results.Results) == 0 || results.Results[0].ID == "" || results.Results[0].Path == "" || results.Results[0].Title == "" || results.Results[0].Snippet == "" {
		t.Fatalf("missing fields: %#v", results)
	}
}

func TestGetSource(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"get", "--repo", "fixture-a", "DOC-123"}, &stdout, &stderr, cacheBackedFactory(t))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, want := range []string{"id: DOC-123", "path: docs/backlog.md", "title: Backlog", "body:", "status: active"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("get output missing %q in %q", want, stdout.String())
		}
	}
}

func TestSnippetAliasesMatchCanonical(t *testing.T) {
	factory := cacheBackedFactory(t)
	var canonical bytes.Buffer
	var canonicalErr bytes.Buffer
	if code := executeWithFactory([]string{"get-snippet", "--repo", "fixture-a", "DOC-123", "--line-start", "1", "--line-end", "1", "--format", "json"}, &canonical, &canonicalErr, factory); code != 0 {
		t.Fatalf("canonical code=%d stderr=%q", code, canonicalErr.String())
	}
	for _, command := range []string{"snippet", "snippets"} {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := executeWithFactory([]string{command, "--repo", "fixture-a", "DOC-123", "--line-start", "1", "--line-end", "1", "--format", "json"}, &stdout, &stderr, factory); code != 0 {
			t.Fatalf("%s code=%d stderr=%q", command, code, stderr.String())
		}
		if stdout.String() != canonical.String() {
			t.Fatalf("%s output differs\n got: %q\nwant: %q", command, stdout.String(), canonical.String())
		}
	}
}

func TestSnippetRejectsChunkAndLineAddressing(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"get-snippet", "--repo", "fixture-a", "DOC-123", "--chunk-id", "chunk-1", "--line-start", "1", "--format", "json"}, &stdout, &stderr, spyFactory())
	if code != 4 || !strings.Contains(stderr.String(), "invalid_query") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestExportJSONDeterministic(t *testing.T) {
	factory := cacheBackedFactory(t)
	var firstOut bytes.Buffer
	var firstErr bytes.Buffer
	if code := executeWithFactory([]string{"export", "--repo", "fixture-a", "--format", "json"}, &firstOut, &firstErr, factory); code != 0 {
		t.Fatalf("first export code=%d stderr=%q", code, firstErr.String())
	}
	var secondOut bytes.Buffer
	var secondErr bytes.Buffer
	if code := executeWithFactory([]string{"export", "--repo", "fixture-a", "--format", "json"}, &secondOut, &secondErr, factory); code != 0 {
		t.Fatalf("second export code=%d stderr=%q", code, secondErr.String())
	}
	if firstOut.String() != secondOut.String() {
		t.Fatalf("export output not deterministic")
	}
	var snapshot service.Snapshot
	if err := json.Unmarshal(firstOut.Bytes(), &snapshot); err != nil {
		t.Fatalf("invalid snapshot json: %v", err)
	}
	if len(snapshot.Sources) == 0 || len(snapshot.Chunks) != 0 {
		t.Fatalf("unexpected snapshot content: %#v", snapshot)
	}
}

func TestDiffLoadsSnapshotPaths(t *testing.T) {
	factory := cacheBackedFactory(t)
	basePath := filepath.Join(t.TempDir(), "base.json")
	var exportOut bytes.Buffer
	var exportErr bytes.Buffer
	if code := executeWithFactory([]string{"export", "--repo", "fixture-a", "--format", "json", "--output", basePath}, &exportOut, &exportErr, factory); code != 0 {
		t.Fatalf("export code=%d stderr=%q", code, exportErr.String())
	}
	if _, err := os.Stat(basePath); err != nil {
		t.Fatalf("base snapshot not written: %v", err)
	}
	var diffOut bytes.Buffer
	var diffErr bytes.Buffer
	if code := executeWithFactory([]string{"diff", "--repo", "fixture-a", "--format", "json", "--base", basePath}, &diffOut, &diffErr, factory); code != 0 {
		t.Fatalf("diff code=%d stderr=%q", code, diffErr.String())
	}
	var result service.DiffSnapshotResult
	if err := json.Unmarshal(diffOut.Bytes(), &result); err != nil {
		t.Fatalf("invalid diff json: %v", err)
	}
	if result.BaseSnapshotID != basePath {
		t.Fatalf("base id=%q want %q", result.BaseSnapshotID, basePath)
	}
}

func TestAllCommandsRegistered(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute([]string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	for _, want := range []string{"ingest", "index", "search", "list", "get", "get-snippet", "snippet", "snippets", "backlinks", "list-chunks", "link-check", "stale-index", "recent", "cache-status", "sync-status", "sync_status", "sync", "export", "diff", "create-issue", "update-issue", "create-page", "update-page", "add-comment", "add-label"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("help missing command %q in %q", want, stdout.String())
		}
	}
}

func TestRecentJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"recent", "--repo", "fixture-a", "--format", "json"}, &stdout, &stderr, cacheBackedFactory(t))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var results service.RecentChangesResult
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if results.RepoID != "fixture-a" || len(results.Results) == 0 || results.Results[0].UpdatedAt.IsZero() {
		t.Fatalf("missing recent fields: %#v", results)
	}
}

func TestSyncStatusJSONAndAlias(t *testing.T) {
	factory := cacheBackedFactory(t)
	var perRecord bytes.Buffer
	var perRecordErr bytes.Buffer
	if code := executeWithFactory([]string{"sync-status", "--repo", "fixture-a", "DOC-123", "--format", "json"}, &perRecord, &perRecordErr, factory); code != 0 {
		t.Fatalf("sync-status per-record code=%d stderr=%q", code, perRecordErr.String())
	}
	var status service.SyncStatusResult
	if err := json.Unmarshal(perRecord.Bytes(), &status); err != nil {
		t.Fatalf("invalid per-record json: %v", err)
	}
	if status.RepoID != "fixture-a" || status.SourceID != "DOC-123" || status.Freshness != service.FreshnessFresh {
		t.Fatalf("sync-status per-record = %#v", status)
	}
	var aggregate bytes.Buffer
	var aggregateErr bytes.Buffer
	if code := executeWithFactory([]string{"sync_status", "--repo", "fixture-a", "--format", "json"}, &aggregate, &aggregateErr, factory); code != 0 {
		t.Fatalf("sync_status aggregate code=%d stderr=%q", code, aggregateErr.String())
	}
	var summary service.SyncStatusSummaryResult
	if err := json.Unmarshal(aggregate.Bytes(), &summary); err != nil {
		t.Fatalf("invalid aggregate json: %v", err)
	}
	if summary.RepoID != "fixture-a" || summary.FreshCount != 1 || summary.CacheEmpty || len(summary.Results) != 1 {
		t.Fatalf("sync-status aggregate = %#v", summary)
	}
}

func TestLinkCheckJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"link-check", "--repo", "fixture-a", "--format", "json"}, &stdout, &stderr, spyFactory())
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var result service.LinkCheckResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if result.CheckedCount == 0 || result.BrokenCount == 0 || len(result.BrokenLinks) == 0 || result.SuggestedAliases == nil {
		t.Fatalf("missing link-check fields: %#v", result)
	}
}

func TestStaleIndexJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"stale-index", "--repo", "fixture-a", "--format", "json"}, &stdout, &stderr, spyFactory())
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var result service.StaleIndexResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if result.StaleCount == 0 || len(result.AffectedSourceIDs) == 0 || len(result.MissingTargetIDs) == 0 {
		t.Fatalf("missing stale-index fields: %#v", result)
	}
}

func TestHelpDocumentsShellMapping(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute([]string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d", code)
	}
	for _, want := range []string{"find -> list", "rg -n -> search", "rg --files -> list", "sed -n -> get-snippet", "handoff/review inspection -> recent", "broken pointer search -> link-check", "stale derived data search -> stale-index", "sync -> search -> list -> get -> backlinks"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("help missing %q in %q", want, stdout.String())
		}
	}
}

func TestUnknownRepoIsNotFound(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"list", "--repo", "missing-repo", "--format", "json"}, &stdout, &stderr, cacheBackedFactory(t))
	if code != 3 || !strings.Contains(stderr.String(), "not_found") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestQueryCommandErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"empty cache", []string{"list", "--repo", "fixture-a"}, 2},
		{"not found", []string{"get", "--repo", "fixture-a", "MISSING"}, 3},
		{"invalid snippet", []string{"get-snippet", "--repo", "fixture-a", "--line-start", "5", "--line-end", "1", "DOC-123"}, 4},
		{"clamped snippet", []string{"get-snippet", "--repo", "fixture-a", "--line-start", "1", "--line-end", "50", "DOC-123"}, 0},
		{"stale strict", []string{"stale-index", "--repo", "fixture-a", "--strict"}, 5},
		{"link strict", []string{"link-check", "--repo", "fixture-a", "--strict"}, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			factory := cacheBackedFactory(t)
			if tc.name == "empty cache" {
				factory = emptyFactory(t)
			}
			if tc.name == "stale strict" || tc.name == "link strict" {
				factory = spyFactory()
			}
			code := executeWithFactory(tc.args, &stdout, &stderr, factory)
			if code != tc.want {
				t.Fatalf("code=%d want=%d stdout=%q stderr=%q", code, tc.want, stdout.String(), stderr.String())
			}
			if tc.name == "clamped snippet" && (stdout.Len() == 0 || stderr.Len() == 0) {
				t.Fatalf("clamped snippet should write stdout and warning stderr")
			}
		})
	}
}

func TestRepoRegistryCLI(t *testing.T) {
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	factory := func(ctx context.Context, path string) (queryService, func() error, error) {
		store, err := cache.NewSQLiteStore(ctx, path)
		if err != nil {
			return nil, nil, err
		}
		return service.New(store), store.Close, nil
	}
	var addOut, addErr bytes.Buffer
	code := executeWithFactory([]string{"repo", "add", "--cache-path", cachePath, "--repo", "fixture-a", "--owner", "owner-a", "--name", "repo-a", "--api-base-url", "https://user:pass@example.invalid/api?access_token=secret&safe=1", "--scopes", "issues,wiki,issues", "--alias", "proj"}, &addOut, &addErr, factory)
	if code != 0 {
		t.Fatalf("repo add code=%d stderr=%q", code, addErr.String())
	}
	var statusOut, statusErr bytes.Buffer
	code = executeWithFactory([]string{"repo", "status", "--cache-path", cachePath, "--repo", "fixture-a"}, &statusOut, &statusErr, factory)
	if code != 0 {
		t.Fatalf("repo status code=%d stderr=%q", code, statusErr.String())
	}
	out := statusOut.String()
	for _, want := range []string{"repo_id: fixture-a", "owner: owner-a", "name: repo-a", "api_base_url: https://example.invalid/api?safe=1", "scopes: issues,wiki", "aliases: proj", "binding_state: ready", "alias_conflict_state: none", "cache_state: unknown", "index_state: unknown"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status output missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "secret") || strings.Contains(out, "user:pass") {
		t.Fatalf("status output leaked sensitive URL parts: %q", out)
	}
	var dupOut, dupErr bytes.Buffer
	code = executeWithFactory([]string{"repo", "add", "--cache-path", cachePath, "--repo", "fixture-a", "--owner", "owner-a", "--name", "repo-a", "--api-base-url", "https://example.invalid/api", "--scopes", "issues"}, &dupOut, &dupErr, factory)
	if code == 0 || !strings.Contains(dupErr.String(), "conflict") {
		t.Fatalf("duplicate repo code=%d stderr=%q", code, dupErr.String())
	}
	var aliasOut, aliasErr bytes.Buffer
	code = executeWithFactory([]string{"repo", "add", "--cache-path", cachePath, "--repo", "fixture-b", "--owner", "owner-b", "--name", "repo-b", "--api-base-url", "https://example.invalid/api", "--scopes", "issues", "--alias", "proj"}, &aliasOut, &aliasErr, factory)
	if code == 0 || !strings.Contains(aliasErr.String(), "conflict") {
		t.Fatalf("alias conflict code=%d stderr=%q", code, aliasErr.String())
	}
	var missingOut, missingErr bytes.Buffer
	code = executeWithFactory([]string{"repo", "status", "--cache-path", cachePath, "--repo", "missing-repo"}, &missingOut, &missingErr, factory)
	if code != 3 || !strings.Contains(missingErr.String(), "repository") || !strings.Contains(missingErr.String(), "not found") {
		t.Fatalf("missing status code=%d stderr=%q", code, missingErr.String())
	}
}

func TestQueryCommandsUseServiceOnly(t *testing.T) {
	spy := &spyService{}
	factory := func(context.Context, string) (queryService, func() error, error) { return spy, nil, nil }
	commands := [][]string{
		{"ingest"}, {"index", "--repo", "fixture-a", "--full"}, {"search", "--repo", "fixture-a", "backlog"}, {"list", "--repo", "fixture-a"}, {"get", "--repo", "fixture-a", "DOC-123"}, {"backlinks", "--repo", "fixture-a", "DOC-123"}, {"get-snippet", "--repo", "fixture-a", "DOC-123", "--line-start", "1", "--line-end", "1"}, {"snippet", "--repo", "fixture-a", "DOC-123", "--line-start", "1", "--line-end", "1"}, {"snippets", "--repo", "fixture-a", "DOC-123", "--line-start", "1", "--line-end", "1"}, {"list-chunks", "--repo", "fixture-a"}, {"recent", "--repo", "fixture-a"}, {"link-check", "--repo", "fixture-a"}, {"stale-index", "--repo", "fixture-a"}, {"sync", "--repo", "fixture-a"}, {"cache-status", "--repo", "fixture-a"}, {"sync-status", "--repo", "fixture-a", "DOC-123"}, {"sync_status", "--repo", "fixture-a"}, {"export", "--repo", "fixture-a"}, {"diff", "--repo", "fixture-a"}, {"repo", "add", "--repo", "fixture-a", "--owner", "owner", "--name", "repo", "--api-base-url", "https://example.invalid/api", "--scopes", "issues"}, {"repo", "status", "--repo", "fixture-a"}, {"create-issue", "--repo", "fixture-a", "--title", "t", "--dry-run"}, {"update-issue", "--repo", "fixture-a", "--number", "1", "--dry-run"}, {"create-page", "--repo", "fixture-a", "--title", "t", "--body", "b", "--dry-run"}, {"update-page", "--repo", "fixture-a", "--slug", "s", "--dry-run"}, {"add-comment", "--repo", "fixture-a", "--number", "1", "--body", "b", "--dry-run"}, {"add-label", "--repo", "fixture-a", "--number", "1", "--label", "l", "--dry-run"},
	}
	for _, args := range commands {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := executeWithFactory(args, &stdout, &stderr, factory); code != 0 {
			t.Fatalf("%v code=%d stderr=%q", args, code, stderr.String())
		}
	}
	wantCalls := map[string]int{"Ingest": 1, "Index": 1, "SearchSources": 1, "ListSources": 1, "GetSource": 1, "GetBacklinks": 1, "GetSnippet": 3, "ListChunks": 1, "RecentChanges": 1, "LinkCheck": 1, "StaleIndex": 1, "SyncToCache": 1, "CacheStatus": 1, "GetSyncStatus": 1, "SyncStatus": 1, "ExportSnapshot": 1, "DiffSnapshot": 1, "AddRepository": 1, "RepositoryStatus": 1, "CreateIssue": 1, "UpdateIssue": 1, "CreatePage": 1, "UpdatePage": 1, "AddComment": 1, "AddLabel": 1}
	for method, want := range wantCalls {
		if spy.calls[method] != want {
			t.Fatalf("%s calls=%d want %d", method, spy.calls[method], want)
		}
	}
}

func cacheBackedFactory(t *testing.T) serviceFactory {
	t.Helper()
	return func(context.Context, string) (queryService, func() error, error) {
		store := populatedStore(t)
		return service.New(store), store.Close, nil
	}
}

func emptyFactory(t *testing.T) serviceFactory {
	t.Helper()
	return func(context.Context, string) (queryService, func() error, error) {
		store, err := cache.NewInMemorySQLiteStore(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
			t.Fatal(err)
		}
		return service.New(store), store.Close, nil
	}
}

func populatedStore(t *testing.T) *cache.SQLiteStore {
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
		{Source: cache.Source{RepoID: "fixture-a", ID: "DOC-123", Kind: "doc", Path: "docs/backlog.md", Title: "Backlog", Body: "backlog overview\nready task details\nmore context", Status: "active", Labels: []string{"knowledge"}, ContentHash: "h1", CreatedAt: now, UpdatedAt: now}, SyncStatus: &cache.SyncStatus{RepoID: "fixture-a", RemoteType: "issue", RemoteID: "100", RemoteRevision: "r1", Status: "fresh", LastFetchedAt: now}},
		{Source: cache.Source{RepoID: "fixture-a", ID: "TASK-1", Kind: "task", Path: "project/tasks/task-1.md", Title: "Ready Task", Body: "task references DOC-123", Status: "ready", ContentHash: "h2", CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute)}, Links: []cache.Link{{RepoID: "fixture-a", TargetID: "DOC-123", Kind: "mentions", Text: "DOC-123"}}},
	}
	for _, graph := range graphs {
		if err := store.UpsertSourceGraph(context.Background(), graph); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

type spyService struct{ calls map[string]int }

func (s *spyService) called(name string) {
	if s.calls == nil {
		s.calls = map[string]int{}
	}
	s.calls[name]++
}
func (s *spyService) Ingest(context.Context, service.OperationRequest) (service.OperationResult, error) {
	s.called("Ingest")
	return service.OperationResult{Command: "ingest", Status: "ok", ProcessedCount: 1, GeneratedAt: time.Now()}, nil
}
func (s *spyService) Index(context.Context, service.OperationRequest) (service.OperationResult, error) {
	s.called("Index")
	return service.OperationResult{Command: "index", Status: "ok", ProcessedCount: 1, GeneratedAt: time.Now()}, nil
}
func (s *spyService) SearchSources(context.Context, service.SearchSourcesRequest) (service.SearchSourcesResult, error) {
	s.called("SearchSources")
	line := 1
	return service.SearchSourcesResult{RepoID: "fixture-a", Query: "backlog", Results: []service.SearchSourceResult{{ID: "DOC-123", Path: "docs/backlog.md", Title: "Backlog", Kind: "doc", Status: "active", Snippet: "backlog", LineStart: &line, LineEnd: &line, Score: 1}}}, nil
}
func (s *spyService) ListSources(context.Context, service.ListSourcesRequest) (service.ListSourcesResult, error) {
	s.called("ListSources")
	return service.ListSourcesResult{RepoID: "fixture-a", Results: []service.SourceSummary{{ID: "DOC-123", Path: "docs/backlog.md", Title: "Backlog"}}}, nil
}
func (s *spyService) GetSource(context.Context, service.GetSourceRequest) (service.SourceRecord, error) {
	s.called("GetSource")
	return service.SourceRecord{ID: "DOC-123", Path: "docs/backlog.md", Title: "Backlog", Body: "body"}, nil
}
func (s *spyService) GetBacklinks(context.Context, service.GetBacklinksRequest) (service.BacklinksResult, error) {
	s.called("GetBacklinks")
	return service.BacklinksResult{RepoID: "fixture-a", ID: "DOC-123", Backlinks: []service.BacklinkResult{{SourceSummary: service.SourceSummary{ID: "TASK-1", Path: "project/tasks/task-1.md"}, TargetID: "DOC-123"}}}, nil
}
func (s *spyService) GetSnippet(context.Context, service.SnippetRequest) (service.SnippetResult, error) {
	s.called("GetSnippet")
	return service.SnippetResult{ID: "DOC-123", Path: "docs/backlog.md", Text: "body", LineStart: 1, LineEnd: 1}, nil
}
func (s *spyService) ListChunks(context.Context, service.ChunkQuery) (service.ChunkQueryResult, error) {
	s.called("ListChunks")
	return service.ChunkQueryResult{Chunks: []service.ChunkResult{{ID: "chunk-1", SourceID: "DOC-123", Policy: "heading", Text: "body"}}, Total: 1}, nil
}
func (s *spyService) SearchChunks(context.Context, service.ChunkSearchQuery) (service.ChunkQueryResult, error) {
	s.called("SearchChunks")
	return service.ChunkQueryResult{Chunks: []service.ChunkResult{{ID: "chunk-1", SourceID: "DOC-123", Policy: "heading", Text: "body"}}, Total: 1}, nil
}
func (s *spyService) GetChunkSnippet(context.Context, service.SnippetQuery) (service.ChunkQueryResult, error) {
	s.called("GetChunkSnippet")
	return service.ChunkQueryResult{Chunks: []service.ChunkResult{{ID: "chunk-1", SourceID: "DOC-123", Policy: "heading", SnippetText: "body"}}, Total: 1}, nil
}
func (s *spyService) GetSyncStatus(context.Context, service.SyncStatusRequest) (service.SyncStatusResult, error) {
	s.called("GetSyncStatus")
	return service.SyncStatusResult{RepoID: "fixture-a", SourceID: "DOC-123", Status: "fresh", LastFetchedAt: time.Now()}, nil
}
func (s *spyService) SyncStatus(context.Context, service.ListSourcesRequest) (service.SyncStatusSummaryResult, error) {
	s.called("SyncStatus")
	return service.SyncStatusSummaryResult{RepoID: "fixture-a", FreshCount: 1, Results: []service.SyncStatusResult{{RepoID: "fixture-a", SourceID: "DOC-123", Status: "fresh", LastFetchedAt: time.Now()}}}, nil
}
func (s *spyService) RecentChanges(context.Context, service.RecentChangesRequest) (service.RecentChangesResult, error) {
	s.called("RecentChanges")
	return service.RecentChangesResult{RepoID: "fixture-a", Results: []service.RecentChangeResult{{ID: "DOC-123", Path: "docs/backlog.md", UpdatedAt: time.Now()}}}, nil
}
func (s *spyService) LinkCheck(_ context.Context, req service.LinkCheckRequest) (service.LinkCheckResult, error) {
	s.called("LinkCheck")
	result := service.LinkCheckResult{CheckedCount: 1, BrokenCount: 1, BrokenLinks: []service.BrokenLinkResult{{SourceID: "DOC-123", TargetID: "MISSING", Kind: "mentions", Text: "MISSING"}}, SuggestedAliases: map[string][]string{}}
	if req.Strict {
		return result, service.ErrLinkCheckFailed{BrokenCount: 1}
	}
	return result, nil
}
func (s *spyService) StaleIndex(_ context.Context, req service.StaleIndexRequest) (service.StaleIndexResult, error) {
	s.called("StaleIndex")
	result := service.StaleIndexResult{StaleCount: 1, AffectedSourceIDs: []string{"DOC-123"}, MissingTargetIDs: []string{"MISSING"}}
	if req.Strict {
		return result, service.ErrStaleIndex{StaleCount: 1}
	}
	return result, nil
}
func (s *spyService) SyncToCache(context.Context, service.SyncRequest) (service.SyncResult, error) {
	s.called("SyncToCache")
	return service.SyncResult{Status: "succeeded", Counts: service.SyncCounts{Fetched: 1}, IdempotencyKey: "key", GeneratedAt: time.Now()}, nil
}
func (s *spyService) CacheStatus(context.Context, service.CacheStatusRequest) (service.CacheStatusResult, error) {
	s.called("CacheStatus")
	return service.CacheStatusResult{RepoID: "fixture-a", WALCapable: true, JournalMode: "wal", Records: 1}, nil
}
func (s *spyService) ExportSnapshot(context.Context, service.ExportSnapshotRequest) (service.ExportSnapshotResult, error) {
	s.called("ExportSnapshot")
	return service.ExportSnapshotResult{SnapshotID: "snap", Format: "text", RecordCount: 1, GeneratedAt: time.Now(), ContentHash: "hash", InlineContent: "DOC-123\n"}, nil
}
func (s *spyService) DiffSnapshot(context.Context, service.DiffSnapshotRequest) (service.DiffSnapshotResult, error) {
	s.called("DiffSnapshot")
	return service.DiffSnapshotResult{BaseSnapshotID: "base", HeadSnapshotID: "head", Format: "text", ChangedSourceIDs: []string{"DOC-123"}, DiffText: "changed\n"}, nil
}
func (s *spyService) AddRepository(context.Context, service.AddRepositoryRequest) (service.RepositoryBinding, error) {
	s.called("AddRepository")
	return service.RepositoryBinding{RepoID: "fixture-a", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []service.RepositoryScope{service.RepositoryScopeIssues}}, nil
}
func (s *spyService) RepositoryStatus(context.Context, service.RepositoryStatusRequest) (service.RepositoryStatus, error) {
	s.called("RepositoryStatus")
	return service.RepositoryStatus{RepoID: "fixture-a", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []service.RepositoryScope{service.RepositoryScopeIssues}, BindingState: "ready", AliasConflictState: "none", CacheState: "unknown", IndexState: "unknown"}, nil
}
func (s *spyService) CreateIssue(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("CreateIssue")
	return service.WriteCommandResult{Command: "create-issue", Status: "dry_run_valid", IdempotencyKey: "key", GeneratedAt: time.Now()}, nil
}
func (s *spyService) UpdateIssue(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("UpdateIssue")
	return service.WriteCommandResult{Command: "update-issue", Status: "dry_run_valid", IdempotencyKey: "key", GeneratedAt: time.Now()}, nil
}
func (s *spyService) CreatePage(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("CreatePage")
	return service.WriteCommandResult{Command: "create-page", Status: "dry_run_valid", IdempotencyKey: "key", GeneratedAt: time.Now()}, nil
}
func (s *spyService) UpdatePage(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("UpdatePage")
	return service.WriteCommandResult{Command: "update-page", Status: "dry_run_valid", IdempotencyKey: "key", GeneratedAt: time.Now()}, nil
}
func (s *spyService) AddComment(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("AddComment")
	return service.WriteCommandResult{Command: "add-comment", Status: "dry_run_valid", IdempotencyKey: "key", GeneratedAt: time.Now()}, nil
}
func (s *spyService) AddLabel(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("AddLabel")
	return service.WriteCommandResult{Command: "add-label", Status: "dry_run_valid", IdempotencyKey: "key", GeneratedAt: time.Now()}, nil
}

func spyFactory() serviceFactory {
	return func(context.Context, string) (queryService, func() error, error) { return &spyService{}, nil, nil }
}

var _ queryService = (*spyService)(nil)

func TestCommandHelpExitsZero(t *testing.T) {
	commands := []string{
		"sync", "index", "search", "list", "get",
		"get-snippet", "snippet", "snippets", "backlinks", "list-chunks",
		"recent", "link-check", "stale-index", "cache-status",
		"sync-status", "sync_status", "export", "export-snapshot",
		"diff", "diff-snapshot",
		"create-issue", "update-issue", "create-page", "update-page",
		"add-comment", "add-label",
		"ingest",
	}
	for _, command := range commands {
		t.Run(command+" --help", func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Execute([]string{command, "--help"}, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), command) {
				t.Fatalf("help output missing command name %q in %q", command, stdout.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr must be empty, got %q", stderr.String())
			}
			if strings.Contains(stdout.String(), "invalid_query") {
				t.Fatalf("help output contains invalid_query: %q", stdout.String())
			}
		})
	}
}

func TestCommandHelpShortForm(t *testing.T) {
	commands := []string{"sync", "index", "search"}
	for _, command := range commands {
		t.Run(command+" -h", func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Execute([]string{command, "-h"}, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), command) {
				t.Fatalf("help output missing command name %q in %q", command, stdout.String())
			}
		})
	}
}

func TestLocalCommandHelpExitsZero(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"auth --help", []string{"auth", "--help"}},
		{"auth -h", []string{"auth", "-h"}},
		{"config --help", []string{"config", "--help"}},
		{"doctor --help", []string{"doctor", "--help"}},
		{"migrate-cache --help", []string{"migrate-cache", "--help"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Execute(tc.args, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), tc.args[0]) {
				t.Fatalf("help output missing command name %q in %q", tc.args[0], stdout.String())
			}
		})
	}
}

func TestLocalSubcommandHelpExitsZero(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"config init --help", []string{"config", "init", "--help"}},
		{"config locate --help", []string{"config", "locate", "--help"}},
		{"config show --help", []string{"config", "show", "--help"}},
		{"auth status --help", []string{"auth", "status", "--help"}},
		{"repo add --help", []string{"repo", "add", "--help"}},
		{"repo status --help", []string{"repo", "status", "--help"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Execute(tc.args, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			if !strings.Contains(stdout.String(), "Usage") {
				t.Fatalf("help output missing Usage line in %q", stdout.String())
			}
		})
	}
}

func TestAliasCommandHelpExitsZero(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"snippet --help", []string{"snippet", "--help"}},
		{"sync_status --help", []string{"sync_status", "--help"}},
		{"export-snapshot --help", []string{"export-snapshot", "--help"}},
		{"diff-snapshot --help", []string{"diff-snapshot", "--help"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Execute(tc.args, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			if stdout.Len() == 0 {
				t.Fatalf("empty help output")
			}
		})
	}
}

func TestHelpDoesNotCreateService(t *testing.T) {
	factoryCalls := 0
	factory := func(ctx context.Context, path string) (queryService, func() error, error) {
		factoryCalls++
		return &spyService{}, nil, nil
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"sync --help", []string{"sync", "--help"}},
		{"index --help", []string{"index", "--help"}},
		{"search --help", []string{"search", "--help"}},
		{"list --help", []string{"list", "--help"}},
		{"get --help", []string{"get", "--help"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			factoryCalls = 0
			var stdout, stderr bytes.Buffer
			code := executeWithFactory(tc.args, &stdout, &stderr, factory)
			if code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			if factoryCalls != 0 {
				t.Fatalf("service factory was called %d times, want 0", factoryCalls)
			}
		})
	}
}

func TestUnknownCommandErrors(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Execute([]string{"nonexistent", "--help"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code=%d want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("expected unknown command error, got stderr=%q", stderr.String())
	}
}
