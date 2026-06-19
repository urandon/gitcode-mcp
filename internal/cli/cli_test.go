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
		{"ingest"},
		{"search_sources", "backlog"},
		{"list_sources", "--kind", "task", "--status", "ready"},
		{"get_source", "DOC-123"},
		{"source_backlinks", "DOC-123"},
		{"sync_status", "DOC-123"},
	}
	for _, args := range cases {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		code := executeWithFactory(args, &stdout, &stderr, factory)
		if code != 0 {
			t.Fatalf("%v code = %d stderr=%q stdout=%q", args, code, stderr.String(), stdout.String())
		}
		if stdout.Len() == 0 {
			t.Fatalf("%v produced no output", args)
		}
	}
}

func TestSearchJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"search_sources", "backlog", "--format", "json"}, &stdout, &stderr, cacheBackedFactory(t))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var results []service.SearchSourceResult
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatalf("invalid json: %v: %q", err, stdout.String())
	}
	if len(results) == 0 || results[0].ID == "" || results[0].Path == "" || results[0].Title == "" || results[0].Snippet == "" {
		t.Fatalf("missing fields: %#v", results)
	}
}

func TestGetSource(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"get", "DOC-123"}, &stdout, &stderr, cacheBackedFactory(t))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	for _, want := range []string{"id: DOC-123", "path: docs/backlog.md", "title: Backlog", "body:", "status: active"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("get output missing %q in %q", want, stdout.String())
		}
	}
}

func TestExportJSONDeterministic(t *testing.T) {
	factory := cacheBackedFactory(t)
	var firstOut bytes.Buffer
	var firstErr bytes.Buffer
	if code := executeWithFactory([]string{"export", "--format", "json"}, &firstOut, &firstErr, factory); code != 0 {
		t.Fatalf("first export code=%d stderr=%q", code, firstErr.String())
	}
	var secondOut bytes.Buffer
	var secondErr bytes.Buffer
	if code := executeWithFactory([]string{"export", "--format", "json"}, &secondOut, &secondErr, factory); code != 0 {
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
	if code := executeWithFactory([]string{"export", "--format", "json", "--output", basePath}, &exportOut, &exportErr, factory); code != 0 {
		t.Fatalf("export code=%d stderr=%q", code, exportErr.String())
	}
	if _, err := os.Stat(basePath); err != nil {
		t.Fatalf("base snapshot not written: %v", err)
	}
	var diffOut bytes.Buffer
	var diffErr bytes.Buffer
	if code := executeWithFactory([]string{"diff", "--format", "json", "--base", basePath}, &diffOut, &diffErr, factory); code != 0 {
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
	for _, want := range []string{"ingest", "index", "search", "search_sources", "list", "list_sources", "get", "get_source", "snippet", "get_snippet", "backlinks", "source_backlinks", "tasks", "tracks", "link-check", "stale-index", "recent", "sync-status", "sync_status", "sync", "export", "diff", "create-issue", "update-issue", "create-page", "update-page", "add-comment", "add-label"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("help missing command %q in %q", want, stdout.String())
		}
	}
}

func TestRecentJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"recent", "--format", "json"}, &stdout, &stderr, cacheBackedFactory(t))
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	var results []service.RecentChangeResult
	if err := json.Unmarshal(stdout.Bytes(), &results); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(results) == 0 || results[0].UpdatedAt.IsZero() {
		t.Fatalf("missing recent fields: %#v", results)
	}
}

func TestLinkCheckJSON(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := executeWithFactory([]string{"link-check", "--format", "json"}, &stdout, &stderr, spyFactory())
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
	code := executeWithFactory([]string{"stale-index", "--format", "json"}, &stdout, &stderr, spyFactory())
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
	for _, want := range []string{"find -> list_sources", "rg -n -> search_sources", "rg --files -> list_sources", "sed -n -> get_snippet", "handoff/review inspection -> recent", "broken pointer search -> link-check", "stale derived data search -> stale-index", "ingest -> search_sources -> list_sources -> get_source -> source_backlinks -> sync_status"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("help missing %q in %q", want, stdout.String())
		}
	}
}

func TestQueryCommandErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want int
	}{
		{"empty cache", []string{"list_sources"}, 2},
		{"not found", []string{"get_source", "MISSING"}, 3},
		{"invalid snippet", []string{"get_snippet", "--line-start", "5", "--line-end", "1", "DOC-123"}, 4},
		{"clamped snippet", []string{"get_snippet", "--line-start", "1", "--line-end", "50", "DOC-123"}, 0},
		{"stale strict", []string{"stale-index", "--strict"}, 5},
		{"link strict", []string{"link-check", "--strict"}, 5},
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
		{"ingest"}, {"index", "--full"}, {"search_sources", "backlog"}, {"list_sources"}, {"get_source", "DOC-123"}, {"source_backlinks", "DOC-123"}, {"get_snippet", "DOC-123", "--line-start", "1", "--line-end", "1"}, {"sync_status", "DOC-123"}, {"recent"}, {"link-check"}, {"stale-index"}, {"sync"}, {"export"}, {"diff"}, {"repo", "add", "--repo", "fixture-a", "--owner", "owner", "--name", "repo", "--api-base-url", "https://example.invalid/api", "--scopes", "issues"}, {"repo", "status", "--repo", "fixture-a"}, {"create-issue", "--title", "t"}, {"update-issue", "--number", "1"}, {"create-page", "--title", "t", "--body", "b"}, {"update-page", "--slug", "s"}, {"add-comment", "--number", "1", "--body", "b"}, {"add-label", "--number", "1", "--label", "l"},
	}
	for _, args := range commands {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		if code := executeWithFactory(args, &stdout, &stderr, factory); code != 0 {
			t.Fatalf("%v code=%d stderr=%q", args, code, stderr.String())
		}
	}
	for _, method := range []string{"Ingest", "Index", "SearchSources", "ListSources", "GetSource", "GetBacklinks", "GetSnippet", "GetSyncStatus", "RecentChanges", "LinkCheck", "StaleIndex", "SyncToCache", "ExportSnapshot", "DiffSnapshot", "AddRepository", "RepositoryStatus", "CreateIssue", "UpdateIssue", "CreatePage", "UpdatePage", "AddComment", "AddLabel"} {
		if spy.calls[method] != 1 {
			t.Fatalf("%s calls=%d want 1", method, spy.calls[method])
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
	graphs := []cache.SourceGraph{
		{Source: cache.Source{ID: "DOC-123", Kind: "doc", Path: "docs/backlog.md", Title: "Backlog", Body: "backlog overview\nready task details\nmore context", Status: "active", Labels: []string{"knowledge"}, ContentHash: "h1", CreatedAt: now, UpdatedAt: now}, SyncStatus: &cache.SyncStatus{RemoteType: "issue", RemoteID: "100", RemoteRevision: "r1", Status: "fresh", LastFetchedAt: now}},
		{Source: cache.Source{ID: "TASK-1", Kind: "task", Path: "project/tasks/task-1.md", Title: "Ready Task", Body: "task references DOC-123", Status: "ready", ContentHash: "h2", CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute)}, Links: []cache.Link{{TargetID: "DOC-123", Kind: "mentions", Text: "DOC-123"}}},
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
func (s *spyService) SearchSources(context.Context, service.SearchSourcesRequest) ([]service.SearchSourceResult, error) {
	s.called("SearchSources")
	line := 1
	return []service.SearchSourceResult{{ID: "DOC-123", Path: "docs/backlog.md", Title: "Backlog", Kind: "doc", Status: "active", Snippet: "backlog", LineStart: &line, LineEnd: &line, Score: 1}}, nil
}
func (s *spyService) ListSources(context.Context, service.ListSourcesRequest) ([]service.SourceSummary, error) {
	s.called("ListSources")
	return []service.SourceSummary{{ID: "DOC-123", Path: "docs/backlog.md", Title: "Backlog"}}, nil
}
func (s *spyService) GetSource(context.Context, service.GetSourceRequest) (service.SourceRecord, error) {
	s.called("GetSource")
	return service.SourceRecord{ID: "DOC-123", Path: "docs/backlog.md", Title: "Backlog", Body: "body"}, nil
}
func (s *spyService) GetBacklinks(context.Context, service.GetBacklinksRequest) ([]service.BacklinkResult, error) {
	s.called("GetBacklinks")
	return []service.BacklinkResult{{SourceSummary: service.SourceSummary{ID: "TASK-1", Path: "project/tasks/task-1.md"}, TargetID: "DOC-123"}}, nil
}
func (s *spyService) GetSnippet(context.Context, service.SnippetRequest) (service.SnippetResult, error) {
	s.called("GetSnippet")
	return service.SnippetResult{ID: "DOC-123", Path: "docs/backlog.md", Text: "body", LineStart: 1, LineEnd: 1}, nil
}
func (s *spyService) GetSyncStatus(context.Context, service.SyncStatusRequest) (service.SyncStatusResult, error) {
	s.called("GetSyncStatus")
	return service.SyncStatusResult{SourceID: "DOC-123", Status: "fresh", LastFetchedAt: time.Now()}, nil
}
func (s *spyService) RecentChanges(context.Context, service.RecentChangesRequest) ([]service.RecentChangeResult, error) {
	s.called("RecentChanges")
	return []service.RecentChangeResult{{ID: "DOC-123", Path: "docs/backlog.md", UpdatedAt: time.Now()}}, nil
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
	return service.WriteCommandResult{Command: "create-issue", Status: "queued", IdempotencyKey: "key", GeneratedAt: time.Now()}, nil
}
func (s *spyService) UpdateIssue(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("UpdateIssue")
	return service.WriteCommandResult{Command: "update-issue", Status: "queued", IdempotencyKey: "key", GeneratedAt: time.Now()}, nil
}
func (s *spyService) CreatePage(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("CreatePage")
	return service.WriteCommandResult{Command: "create-page", Status: "queued", IdempotencyKey: "key", GeneratedAt: time.Now()}, nil
}
func (s *spyService) UpdatePage(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("UpdatePage")
	return service.WriteCommandResult{Command: "update-page", Status: "queued", IdempotencyKey: "key", GeneratedAt: time.Now()}, nil
}
func (s *spyService) AddComment(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("AddComment")
	return service.WriteCommandResult{Command: "add-comment", Status: "queued", IdempotencyKey: "key", GeneratedAt: time.Now()}, nil
}
func (s *spyService) AddLabel(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error) {
	s.called("AddLabel")
	return service.WriteCommandResult{Command: "add-label", Status: "queued", IdempotencyKey: "key", GeneratedAt: time.Now()}, nil
}

func spyFactory() serviceFactory {
	return func(context.Context, string) (queryService, func() error, error) { return &spyService{}, nil, nil }
}

var _ queryService = (*spyService)(nil)
