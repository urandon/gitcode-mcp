package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
)

func TestRepositoryRegistry(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := New(store)
	svc.now = func() time.Time { return time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC) }
	repo, err := svc.AddRepository(ctx, AddRepositoryRequest{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://user:pass@example.invalid/api?token=secret&safe=1", Scopes: []string{"issues,wiki", "issues"}, DisplayName: "Fixture A", Aliases: []string{"proj", "proj"}})
	if err != nil {
		t.Fatalf("AddRepository returned error: %v", err)
	}
	if repo.RepoID != "fixture-a" || !reflect.DeepEqual(repo.Scopes, []RepositoryScope{RepositoryScopeIssues, RepositoryScopeWiki}) || !reflect.DeepEqual(repo.Aliases, []string{"proj"}) {
		t.Fatalf("repo = %#v", repo)
	}
	status, err := svc.RepositoryStatus(ctx, RepositoryStatusRequest{RepoID: "fixture-a"})
	if err != nil {
		t.Fatalf("RepositoryStatus returned error: %v", err)
	}
	if status.APIBaseURL != "https://example.invalid/api?safe=1" || status.BindingState != "ready" || status.CacheState != "unknown" || status.IndexState != "unknown" {
		t.Fatalf("status = %#v", status)
	}
	_, err = svc.AddRepository(ctx, AddRepositoryRequest{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []string{"issues"}})
	var conflict ErrConflict
	if !errors.As(err, &conflict) {
		t.Fatalf("duplicate err=%v want ErrConflict", err)
	}
	_, err = svc.AddRepository(ctx, AddRepositoryRequest{RepoID: "fixture-b", Owner: "owner-b", Name: "repo-b", APIBaseURL: "https://example.invalid/api", Scopes: []string{"issues"}, Aliases: []string{"proj"}})
	if !errors.As(err, &conflict) {
		t.Fatalf("alias err=%v want ErrConflict", err)
	}
	_, err = svc.RepositoryStatus(ctx, RepositoryStatusRequest{RepoID: "missing-repo"})
	var notFound ErrNotFound
	if !errors.As(err, &notFound) || notFound.Kind != "repository" {
		t.Fatalf("missing err=%v want repository ErrNotFound", err)
	}
}

func TestSearchSources(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	results, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "backlog", Limit: 10})
	if err != nil {
		t.Fatalf("SearchSources returned error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("SearchSources returned %d results, want 2", len(results))
	}
	if results[0].ID == "" || results[0].Path == "" || results[0].Title == "" || results[0].Kind == "" || results[0].Status == "" || results[0].Snippet == "" || results[0].LineStart == nil || results[0].LineEnd == nil {
		t.Fatalf("SearchSources result missing contract fields: %#v", results[0])
	}
}

func TestGetSource(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	record, err := svc.GetSource(ctx, GetSourceRequest{RepoID: "fixture-a", ID: "DOC-123"})
	if err != nil {
		t.Fatalf("GetSource returned error: %v", err)
	}
	if record.ID != "DOC-123" || record.RemoteAlias != "remote:wiki/design" || len(record.Links) != 1 || len(record.Backlinks) != 1 || len(record.Labels) != 2 {
		t.Fatalf("GetSource returned incomplete record: %#v", record)
	}
}

func TestListSources(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	results, err := svc.ListSources(ctx, ListSourcesRequest{RepoID: "fixture-a", Kind: "task", Status: "ready", Limit: 1})
	if err != nil {
		t.Fatalf("ListSources returned error: %v", err)
	}
	if len(results) != 1 || results[0].ID != "TASK-001" {
		t.Fatalf("ListSources = %#v, want TASK-001", results)
	}
}

func TestGetBacklinks(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	results, err := svc.GetBacklinks(ctx, GetBacklinksRequest{RepoID: "fixture-a", AliasType: "remote", AliasID: "wiki/design"})
	if err != nil {
		t.Fatalf("GetBacklinks returned error: %v", err)
	}
	if len(results) != 1 || results[0].ID != "TASK-001" || results[0].TargetID != "DOC-123" {
		t.Fatalf("GetBacklinks = %#v", results)
	}
}

func TestResolveID(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	resolved, err := svc.ResolveID(ctx, ResolveIDRequest{RepoID: "fixture-a", AliasType: "path", AliasID: "docs/design.md"})
	if err != nil {
		t.Fatalf("ResolveID returned error: %v", err)
	}
	if resolved.ID != "DOC-123" || resolved.Path != "docs/design.md" || resolved.RemoteAlias != "remote:wiki/design" {
		t.Fatalf("ResolveID = %#v", resolved)
	}
}

func TestGetSnippet(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	snippet, err := svc.GetSnippet(ctx, SnippetRequest{RepoID: "fixture-a", ID: "DOC-123", LineStart: 2, LineEnd: 99})
	if err != nil {
		t.Fatalf("GetSnippet returned error: %v", err)
	}
	if snippet.LineStart != 2 || snippet.LineEnd != 3 || len(snippet.Warnings) != 1 || snippet.Text == "" {
		t.Fatalf("GetSnippet did not clamp as expected: %#v", snippet)
	}
	chunk, err := svc.GetSnippet(ctx, SnippetRequest{RepoID: "fixture-a", ID: "DOC-123", ChunkID: "chunk-doc"})
	if err != nil {
		t.Fatalf("GetSnippet chunk returned error: %v", err)
	}
	if chunk.Text != "backlog chunk" || chunk.LineStart != 2 {
		t.Fatalf("GetSnippet chunk = %#v", chunk)
	}
}

func TestGetSyncStatus(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	status, err := svc.GetSyncStatus(ctx, SyncStatusRequest{RepoID: "fixture-a", ID: "DOC-123"})
	if err != nil {
		t.Fatalf("GetSyncStatus returned error: %v", err)
	}
	if status.Status != "fresh" || status.RemoteID != "wiki/design" {
		t.Fatalf("GetSyncStatus = %#v", status)
	}
}

func TestRepoScopedAliasResolution(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := New(store)
	for _, repoID := range []string{"fixture-a", "fixture-b"} {
		if _, err := svc.AddRepository(ctx, AddRepositoryRequest{RepoID: repoID, Owner: "owner", Name: repoID, APIBaseURL: "https://example.invalid/api", Scopes: []string{"issues,wiki"}}); err != nil {
			t.Fatal(err)
		}
		if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{Source: cache.Source{RepoID: repoID, ID: "ISSUE-42", Kind: "issue", Path: repoID + "/issues/42.md", Title: repoID + " issue", Body: repoID + " body", Status: "open", ContentHash: repoID + "-hash"}, Identities: []cache.Identity{{RepoID: repoID, AliasType: "issue", Alias: "42", Remote: cache.RemoteAlias{Type: "issue", ID: "42"}}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{Source: cache.Source{RepoID: "fixture-a", ID: "WIKI-HOME", Kind: "wiki", Path: "wiki/Home.md", Title: "Home", Body: "home", Status: "fresh", ContentHash: "home-hash"}, Identities: []cache.Identity{{RepoID: "fixture-a", AliasType: "wiki", Alias: "Home", Remote: cache.RemoteAlias{Type: "wiki", ID: "Home"}}}}); err != nil {
		t.Fatal(err)
	}
	a, err := svc.GetSource(ctx, GetSourceRequest{RepoID: "fixture-a", ID: "issue:42"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.GetSource(ctx, GetSourceRequest{RepoID: "fixture-b", ID: "issue:42"})
	if err != nil {
		t.Fatal(err)
	}
	if a.RepoID != "fixture-a" || b.RepoID != "fixture-b" || a.Body == b.Body {
		t.Fatalf("scoped records crossed repos: a=%#v b=%#v", a, b)
	}
	if err := svc.DiagnoseUnscopedAlias(ctx, "issue", "42"); !errors.As(err, &ErrAmbiguousAlias{}) {
		t.Fatalf("DiagnoseUnscopedAlias error=%v want ambiguous", err)
	}
	wiki, err := svc.ResolveID(ctx, ResolveIDRequest{RepoID: "fixture-a", ID: "wiki:Home"})
	if err != nil || wiki.ID != "WIKI-HOME" || wiki.RepoID != "fixture-a" {
		t.Fatalf("wiki resolve=%#v err=%v", wiki, err)
	}
	_, err = svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", SourceIDs: []string{"ISSUE-42"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", SourceIDs: []string{"fixture-b:ISSUE-42"}})
	var empty ErrCacheEmpty
	if !errors.As(err, &empty) {
		t.Fatalf("cross repo export err=%v want cache empty", err)
	}
}

func TestDisabledWikiScopeRejectedBeforeClient(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "issues-only", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	client := &fakeGitCodeClient{wiki: gitcode.WikiPage{Slug: "Home", Title: "Home", Body: "body"}}
	svc := NewWithClient(store, client)
	svc.lockPath = filepath.Join(t.TempDir(), "sync.lock")
	_, err = svc.SyncToCache(ctx, SyncRequest{RepoID: "issues-only", RemoteAlias: "wiki:Home", IdempotencyKey: "wiki-disabled"})
	var invalid ErrInvalidQuery
	if !errors.As(err, &invalid) || client.wikiCalls != 0 {
		t.Fatalf("sync err=%v wikiCalls=%d want local validation before client", err, client.wikiCalls)
	}
	_, err = svc.CreatePage(ctx, WriteCommandRequest{Repo: "issues-only", Title: "Home", Body: "body"})
	if !errors.As(err, &invalid) {
		t.Fatalf("write err=%v want invalid disabled scope", err)
	}
}

func TestSyncGraphFixtureOfflineReadsIssueWikiCommentsAndChunks(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
	svc := NewWithClient(store, &fakeGitCodeClient{issue: gitcode.Issue{Number: 42, Title: "Fixture Issue", Body: "# Issue\n\nremote issue body", State: "open", CreatedAt: base, UpdatedAt: base}, comments: []gitcode.Comment{{ID: "c1", Author: "fixture-user", Body: "comment", CreatedAt: base, UpdatedAt: base}}, wiki: gitcode.WikiPage{Slug: "Home", Title: "Fixture Wiki", Body: "# Wiki\n\nremote wiki body", Revision: "rev-home", CreatedAt: base, UpdatedAt: base}})
	svc.lockPath = filepath.Join(t.TempDir(), "sync.lock")
	if _, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "fixture-a", RemoteAlias: "issue:42", IdempotencyKey: "sync-issue-42"}); err != nil {
		t.Fatalf("issue sync returned error: %v", err)
	}
	if _, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "fixture-a", RemoteAlias: "wiki:Home", IdempotencyKey: "sync-wiki-home"}); err != nil {
		t.Fatalf("wiki sync returned error: %v", err)
	}
	results, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "remote"})
	if err != nil || len(results) < 2 {
		t.Fatalf("search results = %#v, %v", results, err)
	}
	issue, err := svc.GetSource(ctx, GetSourceRequest{RepoID: "fixture-a", ID: "ISSUE-42"})
	if err != nil || issue.Kind != "issue" {
		t.Fatalf("issue get = %#v, %v", issue, err)
	}
	wiki, err := svc.GetSource(ctx, GetSourceRequest{RepoID: "fixture-a", ID: "WIKI-HOME"})
	if err != nil || wiki.Kind != "wiki" {
		t.Fatalf("wiki get = %#v, %v", wiki, err)
	}
	snippet, err := svc.GetSnippet(ctx, SnippetRequest{RepoID: "fixture-a", ID: "ISSUE-42", LineStart: 1, LineEnd: 2})
	if err != nil || snippet.Text == "" {
		t.Fatalf("snippet = %#v, %v", snippet, err)
	}
	record, err := store.GetRecord(ctx, "fixture-a", "ISSUE-42")
	if err != nil || record.Provenance != cache.ProvenanceRemote || len(record.Comments) != 1 {
		t.Fatalf("record = %#v, %v", record, err)
	}
	counts, err := store.RecordCounts(ctx, "fixture-a")
	if err != nil {
		t.Fatal(err)
	}
	if counts.Records != 2 || counts.Comments != 1 || counts.SyncEvents != 2 || counts.RemoteRevisions != 2 || counts.Chunks == 0 {
		t.Fatalf("counts = %#v", counts)
	}
}

func TestSyncStateMachine(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{Source: cache.Source{RepoID: "fixture-a", ID: "DOC-123", Kind: "wiki", Path: "wiki/design.md", Title: "Old", Body: "old", Status: "fresh", ContentHash: "old-hash", CreatedAt: base, UpdatedAt: base.Add(time.Hour)}, Identities: []cache.Identity{{RepoID: "fixture-a", AliasType: "remote", Alias: "wiki/design", Remote: cache.RemoteAlias{Type: "remote", ID: "wiki/design"}}}, SyncStatus: &cache.SyncStatus{RepoID: "fixture-a", RemoteType: "remote", RemoteID: "wiki/design", RemoteRevision: "old", Status: "fresh", LastFetchedAt: base}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, &fakeGitCodeClient{wiki: gitcode.WikiPage{Slug: "wiki/design", Title: "Design", Body: "new body", Revision: "rev-2", CreatedAt: base, UpdatedAt: base.Add(2 * time.Hour)}})
	svc.lockPath = filepath.Join(t.TempDir(), "sync.lock")
	before, err := svc.GetSyncStatus(ctx, SyncStatusRequest{RepoID: "fixture-a", ID: "DOC-123"})
	if err != nil {
		t.Fatal(err)
	}
	if before.Freshness != FreshnessStale {
		t.Fatalf("before freshness=%s want stale", before.Freshness)
	}
	result, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "sync-key"})
	if err != nil {
		t.Fatalf("SyncToCache returned error: %v", err)
	}
	if result.Status != "succeeded" || result.IdempotencyKey != "sync-key" || result.Counts.Updated != 1 {
		t.Fatalf("SyncToCache result = %#v", result)
	}
	after, err := svc.GetSyncStatus(ctx, SyncStatusRequest{RepoID: "fixture-a", ID: "DOC-123"})
	if err != nil {
		t.Fatal(err)
	}
	if after.Freshness != FreshnessFresh || after.RemoteRevision != "rev-2" {
		t.Fatalf("after status = %#v", after)
	}
	event, err := store.GetSyncEventByKey(ctx, "sync-key")
	if err != nil {
		t.Fatal(err)
	}
	if event == nil || event.Status != "succeeded" || event.IdempotencyKey != "sync-key" || event.Message == "" {
		t.Fatalf("sync event = %#v", event)
	}
	source, err := store.GetSource(ctx, "DOC-123")
	if err != nil {
		t.Fatal(err)
	}
	if source.Body != "new body" || source.Title != "Design" {
		t.Fatalf("source not updated: %#v", source)
	}
	record, err := store.GetRecord(ctx, "fixture-a", "DOC-123")
	if err != nil {
		t.Fatal(err)
	}
	if record.Provenance != cache.ProvenanceRemote || record.RemoteType != "remote" || record.RemoteID != "wiki/design" {
		t.Fatalf("record = %#v", record)
	}
	chunks, err := store.GetChunksScoped(ctx, "fixture-a", "DOC-123")
	if err != nil || len(chunks) == 0 {
		t.Fatalf("chunks = %#v, %v", chunks, err)
	}
}

func TestSyncLockContention(t *testing.T) {
	ctx := context.Background()
	svc := seededSyncService(t, ctx, nil)
	svc.lockPath = filepath.Join(t.TempDir(), "sync.lock")
	lock, err := svc.store.AcquireLock(ctx, svc.lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.store.ReleaseLock(ctx, lock)
	client := &fakeGitCodeClient{wiki: gitcode.WikiPage{Slug: "wiki/design", Title: "Design", Body: "new"}}
	svc.client = client
	_, err = svc.SyncToCache(ctx, SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "lock-key"})
	var contention cache.ErrLockContention
	if !errors.As(err, &contention) {
		t.Fatalf("error=%v want ErrLockContention", err)
	}
	if client.wikiCalls != 0 {
		t.Fatalf("remote calls=%d want 0", client.wikiCalls)
	}
	source, err := svc.store.GetSource(ctx, "DOC-123")
	if err != nil {
		t.Fatal(err)
	}
	if source.Body != "intro same\nbacklog design same\nfinal" {
		t.Fatalf("source mutated during lock contention: %#v", source)
	}
}

func TestSyncIdempotencyReplay(t *testing.T) {
	ctx := context.Background()
	client := &fakeGitCodeClient{wiki: gitcode.WikiPage{Slug: "wiki/design", Title: "Design", Body: "new body", Revision: "rev-2", UpdatedAt: time.Now().UTC()}}
	svc := seededSyncService(t, ctx, client)
	svc.lockPath = filepath.Join(t.TempDir(), "sync.lock")
	first, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "replay-key"})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := svc.store.AcquireLock(ctx, svc.lockPath)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.store.ReleaseLock(ctx, lock)
	second, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "replay-key"})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Replayed || second.SyncEventID != first.SyncEventID {
		t.Fatalf("replay result=%#v first=%#v", second, first)
	}
	if client.wikiCalls != 1 {
		t.Fatalf("remote calls=%d want 1", client.wikiCalls)
	}
}

func TestSyncBoundedStaging(t *testing.T) {
	ctx := context.Background()
	client := &fakeGitCodeClient{wiki: gitcode.WikiPage{Slug: "wiki/design", Title: "Design", Body: "body too large for limit", Revision: "rev-2"}}
	svc := seededSyncService(t, ctx, client)
	svc.lockPath = filepath.Join(t.TempDir(), "sync.lock")
	_, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "large-key", MaxSize: 5})
	var tooLarge gitcode.ErrPayloadTooLarge
	if !errors.As(err, &tooLarge) {
		t.Fatalf("error=%v want ErrPayloadTooLarge", err)
	}
	source, err := svc.store.GetSource(ctx, "DOC-123")
	if err != nil {
		t.Fatal(err)
	}
	if source.Body != "intro same\nbacklog design same\nfinal" || source.ContentHash != "hash-doc" {
		t.Fatalf("source mutated after staging failure: %#v", source)
	}
	event, err := svc.store.GetSyncEventByKey(ctx, "large-key")
	if err != nil {
		t.Fatal(err)
	}
	if event == nil || event.Status != "failed" {
		t.Fatalf("failed event = %#v", event)
	}
}

func TestSyncRetry(t *testing.T) {
	ctx := context.Background()
	client := &fakeGitCodeClient{wiki: gitcode.WikiPage{Slug: "wiki/design", Title: "Design", Body: "new body", Revision: "rev-2", UpdatedAt: time.Now().UTC()}, errors: []error{gitcode.ErrRateLimited{RetryAfter: time.Nanosecond, Endpoint: "/wiki", Attempts: 1}}}
	svc := seededSyncService(t, ctx, client)
	svc.lockPath = filepath.Join(t.TempDir(), "sync.lock")
	result, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "retry-key", MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "succeeded" || client.wikiCalls != 2 || result.Counts.Updated != 1 {
		t.Fatalf("result=%#v calls=%d", result, client.wikiCalls)
	}
	event, err := svc.store.GetSyncEventByKey(ctx, "retry-key")
	if err != nil {
		t.Fatal(err)
	}
	if event == nil || event.Status != "succeeded" {
		t.Fatalf("event=%#v", event)
	}
}

func TestFailureModes(t *testing.T) {
	ctx := context.Background()
	baseWiki := gitcode.WikiPage{Slug: "wiki/design", Title: "Design", Body: "new body", Revision: "rev-2", UpdatedAt: time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC)}
	tests := []struct {
		name         string
		client       *fakeGitCodeClient
		request      SyncRequest
		prelock      bool
		corrupt      bool
		wantMode     string
		wantErrAs    func(error) bool
		wantMessage  string
		wantRemote   int
		wantNotFound bool
	}{
		{name: "failure-timeout-network-unavailable", client: &fakeGitCodeClient{errors: []error{gitcode.ErrNetworkUnavailable{Endpoint: "/wiki", Attempts: 1}}}, request: SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "failure-timeout-network-unavailable"}, wantMode: "network_timeout", wantErrAs: func(err error) bool { var target gitcode.ErrNetworkUnavailable; return errors.As(err, &target) }, wantMessage: "sync: network timeout for record DOC-123: retry with --timeout to increase deadline or check connectivity", wantRemote: 1},
		{name: "failure-rate-limited-retry-after", client: &fakeGitCodeClient{errors: []error{gitcode.ErrRateLimited{RetryAfter: time.Second, Endpoint: "/wiki", Attempts: 1}}}, request: SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "failure-rate-limited-retry-after", MaxAttempts: 1}, wantMode: "rate_limited", wantErrAs: func(err error) bool {
			var target gitcode.ErrRateLimited
			return errors.As(err, &target) && target.RetryAfter == time.Second
		}, wantMessage: "sync: rate limited. Retry after 1 seconds.", wantRemote: 1},
		{name: "failure-partial-response", client: &fakeGitCodeClient{errors: []error{gitcode.ErrPartialResponse{Endpoint: "/wiki", Expected: 100, Got: 40}}}, request: SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "failure-partial-response"}, wantMode: "partial_response", wantErrAs: func(err error) bool { var target gitcode.ErrPartialResponse; return errors.As(err, &target) }, wantMessage: "sync: received partial response for /wiki: expected 100 bytes, got 40 bytes. Run sync again to resume.", wantRemote: 1},
		{name: "failure-auth-expired", client: &fakeGitCodeClient{errors: []error{gitcode.ErrAuthExpired{Endpoint: "/wiki", Status: 401}}}, request: SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "failure-auth-expired"}, wantMode: "auth_expired", wantErrAs: func(err error) bool { var target gitcode.ErrAuthExpired; return errors.As(err, &target) }, wantMessage: "sync: authentication expired. Renew your GITCODE_TOKEN and try again.", wantRemote: 1},
		{name: "failure-remote-id-collision", client: &fakeGitCodeClient{wiki: baseWiki}, request: SyncRequest{RepoID: "fixture-a", StableID: "TASK-001", RemoteAlias: "remote:wiki/design", IdempotencyKey: "failure-remote-id-collision"}, wantMode: "remote_collision", wantErrAs: func(err error) bool {
			var target gitcode.ErrRemoteCollision
			return errors.As(err, &target) && target.ExistingID == "DOC-123" && target.NewID == "TASK-001"
		}, wantMessage: "sync: remote id remote:wiki/design already maps to local id DOC-123; cannot map to TASK-001. Run link-check for guidance.", wantRemote: 1},
		{name: "failure-cache-corruption", client: &fakeGitCodeClient{wiki: baseWiki}, request: SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "failure-cache-corruption"}, corrupt: true, wantMode: "cache_corruption", wantErrAs: func(err error) bool { var target cache.ErrCacheCorruption; return errors.As(err, &target) }, wantMessage: "cache: integrity check failed at memory. Recover from backup or re-ingest with gitcode-mcp sync --full.", wantRemote: 0},
		{name: "failure-lock-contention", client: &fakeGitCodeClient{wiki: baseWiki}, request: SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "failure-lock-contention"}, prelock: true, wantErrAs: func(err error) bool { var target cache.ErrLockContention; return errors.As(err, &target) }, wantRemote: 0},
		{name: "failure-missing-remote-record", client: &fakeGitCodeClient{errors: []error{gitcode.ErrRemoteNotFound{Endpoint: "/wiki", Alias: "remote:wiki/design"}}}, request: SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "failure-missing-remote-record"}, wantMode: "remote_not_found", wantErrAs: func(err error) bool { var target gitcode.ErrRemoteNotFound; return errors.As(err, &target) }, wantMessage: "sync: remote record for alias remote:wiki/design not found. It may have been deleted or moved. Run link-check to find affected references.", wantRemote: 1, wantNotFound: true},
		{name: "failure-oversized-payload", client: &fakeGitCodeClient{errors: []error{gitcode.ErrPayloadTooLarge{Endpoint: "/wiki", Limit: 5, Size: 50}}}, request: SyncRequest{RepoID: "fixture-a", StableID: "DOC-123", IdempotencyKey: "failure-oversized-payload"}, wantMode: "payload_too_large", wantErrAs: func(err error) bool {
			var target gitcode.ErrPayloadTooLarge
			return errors.As(err, &target) && target.Limit == 5
		}, wantMessage: "sync: record DOC-123 exceeds maximum size 5 bytes. Use --max-size to increase limit or skip with --skip-large.", wantRemote: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := seededSyncService(t, ctx, tt.client)
			svc.lockPath = filepath.Join(t.TempDir(), "sync.lock")
			if tt.corrupt {
				svc.store = corruptingStore{Store: svc.store}
			}
			var lock *cache.LockHandle
			if tt.prelock {
				var err error
				lock, err = svc.store.AcquireLock(ctx, svc.lockPath)
				if err != nil {
					t.Fatal(err)
				}
				defer svc.store.ReleaseLock(ctx, lock)
			}
			_, err := svc.SyncToCache(ctx, tt.request)
			if err == nil {
				t.Fatal("SyncToCache succeeded, want failure")
			}
			if !tt.wantErrAs(err) {
				t.Fatalf("error=%T %[1]v did not match typed expectation", err)
			}
			if tt.wantMode != "" {
				var failure ErrSyncFailure
				if !errors.As(err, &failure) || failure.Mode != tt.wantMode || failure.RecoveryAction == "" {
					t.Fatalf("failure=%#v err=%v", failure, err)
				}
			}
			if tt.wantMessage != "" && err.Error() != tt.wantMessage {
				t.Fatalf("message=%q want %q", err.Error(), tt.wantMessage)
			}
			if tt.client.wikiCalls != tt.wantRemote {
				t.Fatalf("remote calls=%d want %d", tt.client.wikiCalls, tt.wantRemote)
			}
			source, err := svc.store.GetSource(ctx, "DOC-123")
			if err != nil {
				t.Fatal(err)
			}
			if source.Body != "intro same\nbacklog design same\nfinal" || source.ContentHash != "hash-doc" {
				t.Fatalf("source mutated after failure: %#v", source)
			}
			if tt.prelock {
				if event, err := svc.store.GetSyncEventByKey(ctx, tt.request.IdempotencyKey); err != nil || event != nil {
					t.Fatalf("lock contention event=%#v err=%v", event, err)
				}
				return
			}
			event, err := svc.store.GetSyncEventByKey(ctx, tt.request.IdempotencyKey)
			if err != nil {
				t.Fatal(err)
			}
			if event == nil || event.Status != "failed" || event.Message == "" {
				t.Fatalf("failed event=%#v", event)
			}
			status, err := svc.store.GetSyncStatus(ctx, "DOC-123")
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantNotFound {
				if status.Status != "not_found" {
					t.Fatalf("status=%#v want not_found", status)
				}
			} else if status.RemoteRevision != "rev-1" || status.Status != "fresh" {
				t.Fatalf("sync status mutated after failure: %#v", status)
			}
			conflicts, err := svc.store.GetConflicts(ctx, "DOC-123")
			if err != nil {
				t.Fatal(err)
			}
			if len(conflicts) != 0 {
				t.Fatalf("conflicts written after failure: %#v", conflicts)
			}
		})
	}
}

func TestRecentChanges(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	recent, err := svc.RecentChanges(ctx, RecentChangesRequest{RepoID: "fixture-a", Limit: 2})
	if err != nil {
		t.Fatalf("RecentChanges returned error: %v", err)
	}
	if len(recent) != 2 || recent[0].ID != "TASK-001" {
		t.Fatalf("RecentChanges = %#v", recent)
	}
}

func TestLinkCheck(t *testing.T) {
	ctx := context.Background()
	svc := New(fakeBrokenStore())
	result, err := svc.LinkCheck(ctx, LinkCheckRequest{RepoID: "fixture-a"})
	if err != nil {
		t.Fatalf("LinkCheck returned error: %v", err)
	}
	if result.BrokenCount != 1 || result.BrokenLinks[0].TargetID != "MISSING-1" {
		t.Fatalf("LinkCheck = %#v", result)
	}
	_, err = svc.LinkCheck(ctx, LinkCheckRequest{RepoID: "fixture-a", Strict: true})
	var failed ErrLinkCheckFailed
	if !errors.As(err, &failed) {
		t.Fatalf("LinkCheck strict error = %v, want ErrLinkCheckFailed", err)
	}
}

func TestStaleIndex(t *testing.T) {
	ctx := context.Background()
	svc := New(fakeBrokenStore())
	result, err := svc.StaleIndex(ctx, StaleIndexRequest{RepoID: "fixture-a"})
	if err != nil {
		t.Fatalf("StaleIndex returned error: %v", err)
	}
	if result.StaleCount != 1 || result.AffectedSourceIDs[0] != "DOC-1" || result.MissingTargetIDs[0] != "MISSING-1" {
		t.Fatalf("StaleIndex = %#v", result)
	}
	_, err = svc.StaleIndex(ctx, StaleIndexRequest{RepoID: "fixture-a", Strict: true})
	var stale ErrStaleIndex
	if !errors.As(err, &stale) {
		t.Fatalf("StaleIndex strict error = %v, want ErrStaleIndex", err)
	}
}

func TestExportSnapshot(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	svc.now = func() time.Time { return time.Unix(100, 0).UTC() }
	first, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", Format: "text"})
	if err != nil {
		t.Fatalf("ExportSnapshot returned error: %v", err)
	}
	second, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", Format: "text"})
	if err != nil {
		t.Fatalf("ExportSnapshot second returned error: %v", err)
	}
	if first.ContentHash != second.ContentHash || first.InlineContent != second.InlineContent || first.RecordCount != 2 {
		t.Fatalf("ExportSnapshot not deterministic: %#v %#v", first, second)
	}
}

func TestExportDeterminism(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	svc.now = func() time.Time { return time.Unix(100, 0).UTC() }
	jsonFirst, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", Format: "json", IncludeBody: true})
	if err != nil {
		t.Fatalf("ExportSnapshot json returned error: %v", err)
	}
	jsonSecond, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", Format: "json", IncludeBody: true})
	if err != nil {
		t.Fatalf("ExportSnapshot json second returned error: %v", err)
	}
	markdownFirst, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", Format: "markdown", IncludeBody: true})
	if err != nil {
		t.Fatalf("ExportSnapshot markdown returned error: %v", err)
	}
	markdownSecond, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", Format: "markdown", IncludeBody: true})
	if err != nil {
		t.Fatalf("ExportSnapshot markdown second returned error: %v", err)
	}
	if jsonFirst.InlineContent != jsonSecond.InlineContent || markdownFirst.InlineContent != markdownSecond.InlineContent {
		t.Fatalf("exports are not byte-identical")
	}
}

func TestGoldenExport(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSourceGraph(ctx, cache.SourceGraph{
		Source:     cache.Source{RepoID: "fixture-a", ID: "DOC-123", Kind: "doc", Path: "docs/design.md", Title: "Design Doc", Body: "body", Status: "ready", Labels: []string{"zeta", "design"}, ContentHash: "hash-doc", CreatedAt: base, UpdatedAt: base},
		Identities: []cache.Identity{{RepoID: "fixture-a", AliasType: "path", Alias: "docs/design.md", Remote: cache.RemoteAlias{Type: "remote", ID: "wiki/design"}}},
		Links:      []cache.Link{{RepoID: "fixture-a", TargetID: "DOC-123", Kind: "mentions", Text: "doc"}},
		Chunks:     []cache.Chunk{{RepoID: "fixture-a", ID: "chunk-doc", ContentHash: "hash-doc", ByteStart: 0, ByteEnd: 4, LineStart: 1, LineEnd: 1, HeadingPath: []string{"Design"}, Text: "body", NormalizedText: "body"}},
		SyncStatus: &cache.SyncStatus{RepoID: "fixture-a", RemoteType: "remote", RemoteID: "wiki/design", RemoteRevision: "rev-1", Status: "fresh", LastFetchedAt: base},
	}); err != nil {
		t.Fatal(err)
	}
	svc := New(store)
	svc.now = func() time.Time { return base }
	result, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", Format: "markdown", IncludeBody: true})
	if err != nil {
		t.Fatalf("ExportSnapshot markdown returned error: %v", err)
	}
	want, err := os.ReadFile("testdata/golden_export.md")
	if err != nil {
		t.Fatal(err)
	}
	if result.InlineContent != string(want) {
		t.Fatalf("golden export mismatch\n got: %q\nwant: %q", result.InlineContent, string(want))
	}
}

func TestExportIncludesChunkProvenance(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	result, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", Format: "json", IncludeBody: true})
	if err != nil {
		t.Fatalf("ExportSnapshot returned error: %v", err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal([]byte(result.InlineContent), &snapshot); err != nil {
		t.Fatalf("snapshot json invalid: %v", err)
	}
	if len(snapshot.Chunks) == 0 {
		t.Fatalf("snapshot has no chunks: %#v", snapshot)
	}
	chunk := snapshot.Chunks[0]
	if chunk.ID == "" || chunk.SourceID == "" || chunk.ContentHash == "" || chunk.ByteStart < 0 || chunk.ByteEnd == 0 || chunk.LineStart == 0 || chunk.LineEnd == 0 || chunk.Text == "" {
		t.Fatalf("chunk missing provenance: %#v", chunk)
	}
	if chunk.InheritedMetadata["owner"] != "docs" || len(chunk.OutboundLinks) == 0 || chunk.ResolvedAliases["TASK-001"] != "task:1" {
		t.Fatalf("chunk missing nested provenance: %#v", chunk)
	}
}

func TestDiffSnapshot(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	base, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a", Format: "json", IncludeBody: true})
	if err != nil {
		t.Fatalf("base export returned error: %v", err)
	}
	if err := svc.store.UpsertSource(ctx, cache.Source{RepoID: "fixture-a", ID: "TASK-001", Kind: "task", Path: "project/tasks/task.md", Title: "Task Backlog Changed", Body: "task changed", Status: "ready", Labels: []string{"task"}, ContentHash: "hash-task-2", CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), UpdatedAt: time.Date(2026, 1, 1, 2, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	if err := svc.store.UpsertSourceGraph(ctx, cache.SourceGraph{Source: cache.Source{RepoID: "fixture-a", ID: "DOC-999", Kind: "doc", Path: "docs/new.md", Title: "New", Body: "new", Status: "ready", ContentHash: "hash-new"}, Chunks: []cache.Chunk{{RepoID: "fixture-a", ID: "chunk-new", SourceID: "DOC-999", ContentHash: "hash-new", ByteStart: 0, ByteEnd: 3, LineStart: 1, LineEnd: 1, Text: "new"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.store.UpsertChunk(ctx, cache.Chunk{RepoID: "fixture-a", ID: "chunk-doc", SourceID: "DOC-123", ContentHash: "hash-doc", ByteStart: 0, ByteEnd: 14, LineStart: 2, LineEnd: 2, HeadingPath: []string{"Design"}, Text: "backlog chunk!", NormalizedText: "backlog chunk", InheritedMetadata: map[string]string{"owner": "docs"}, OutboundLinks: []string{"TASK-001"}, ResolvedAliases: map[string]string{"TASK-001": "task:1"}}); err != nil {
		t.Fatal(err)
	}
	diff, err := svc.DiffSnapshot(ctx, DiffSnapshotRequest{RepoID: "fixture-a", Base: SnapshotRef{Kind: "bytes", Bytes: []byte(base.InlineContent), Format: "json"}, Head: SnapshotRef{Kind: "current", Format: "json"}, Format: "json"})
	if err != nil {
		t.Fatalf("DiffSnapshot returned error: %v", err)
	}
	if len(diff.AddedSources) == 0 || len(diff.ChangedSources) == 0 || len(diff.AddedChunks) == 0 || len(diff.ChangedChunks) == 0 || !strings.Contains(strings.Join(diff.ChangedSources[0].ChangedFields, ","), "content_hash") {
		t.Fatalf("DiffSnapshot = %#v", diff)
	}
}

func TestMCPToolDTOContract(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	if _, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "backlog"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetSource(ctx, GetSourceRequest{RepoID: "fixture-a", ID: "DOC-123"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListSources(ctx, ListSourcesRequest{RepoID: "fixture-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetBacklinks(ctx, GetBacklinksRequest{RepoID: "fixture-a", ID: "DOC-123"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveID(ctx, ResolveIDRequest{RepoID: "fixture-a", ID: "DOC-123"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetSyncStatus(ctx, SyncStatusRequest{RepoID: "fixture-a", ID: "DOC-123"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DiffSnapshot(ctx, DiffSnapshotRequest{RepoID: "fixture-a"}); err != nil {
		t.Fatal(err)
	}
}

func TestQueryEdgeCases(t *testing.T) {
	ctx := context.Background()
	empty, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	if err := empty.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	_, err = New(empty).ListSources(ctx, ListSourcesRequest{RepoID: "fixture-a"})
	var cacheEmpty ErrCacheEmpty
	if !errors.As(err, &cacheEmpty) {
		t.Fatalf("empty cache error = %v, want ErrCacheEmpty", err)
	}
	svc := seededService(t, ctx)
	_, err = svc.GetSource(ctx, GetSourceRequest{RepoID: "fixture-a", ID: "NOPE"})
	var notFound ErrNotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("not found error = %v, want ErrNotFound", err)
	}
	_, err = svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a"})
	var invalid ErrInvalidQuery
	if !errors.As(err, &invalid) {
		t.Fatalf("invalid query error = %v, want ErrInvalidQuery", err)
	}
	results, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "same"})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{results[0].ID, results[1].ID}
	if !reflect.DeepEqual(ids, []string{"DOC-123", "TASK-001"}) {
		t.Fatalf("equal score ordering ids = %v", ids)
	}
}

func TestQueryMethodsDoNotUseLiveNetwork(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	calls := []func() error{
		func() error {
			_, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "fixture-a", Query: "backlog"})
			return err
		},
		func() error {
			_, err := svc.GetSource(ctx, GetSourceRequest{RepoID: "fixture-a", ID: "DOC-123"})
			return err
		},
		func() error { _, err := svc.ListSources(ctx, ListSourcesRequest{RepoID: "fixture-a"}); return err },
		func() error {
			_, err := svc.GetBacklinks(ctx, GetBacklinksRequest{RepoID: "fixture-a", ID: "DOC-123"})
			return err
		},
		func() error {
			_, err := svc.ResolveID(ctx, ResolveIDRequest{RepoID: "fixture-a", ID: "DOC-123"})
			return err
		},
		func() error {
			_, err := svc.GetSnippet(ctx, SnippetRequest{RepoID: "fixture-a", ID: "DOC-123", LineStart: 1, LineEnd: 1})
			return err
		},
		func() error {
			_, err := svc.GetSyncStatus(ctx, SyncStatusRequest{RepoID: "fixture-a", ID: "DOC-123"})
			return err
		},
		func() error { _, err := svc.RecentChanges(ctx, RecentChangesRequest{RepoID: "fixture-a"}); return err },
		func() error { _, err := svc.LinkCheck(ctx, LinkCheckRequest{RepoID: "fixture-a"}); return err },
		func() error { _, err := svc.StaleIndex(ctx, StaleIndexRequest{RepoID: "fixture-a"}); return err },
		func() error {
			_, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{RepoID: "fixture-a"})
			return err
		},
		func() error { _, err := svc.DiffSnapshot(ctx, DiffSnapshotRequest{RepoID: "fixture-a"}); return err },
	}
	for i, call := range calls {
		if err := call(); err != nil {
			t.Fatalf("query call %d returned error: %v", i, err)
		}
	}
}

func seededService(t *testing.T, ctx context.Context) *Service {
	t.Helper()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	seedStore(t, ctx, store)
	return New(store)
}

func seedStore(t *testing.T, ctx context.Context, store cache.Store) {
	t.Helper()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	err := store.UpsertSource(ctx, cache.Source{RepoID: "fixture-a", ID: "DOC-123", Kind: "doc", Path: "docs/design.md", Title: "Design Doc", Body: "intro same\nbacklog design same\nfinal", Status: "ready", Labels: []string{"zeta", "design"}, ContentHash: "hash-doc", CreatedAt: base, UpdatedAt: base})
	if err != nil {
		t.Fatal(err)
	}
	err = store.UpsertSource(ctx, cache.Source{RepoID: "fixture-a", ID: "TASK-001", Kind: "task", Path: "project/tasks/task.md", Title: "Task Backlog", Body: "task same\nbacklog item same", Status: "ready", Labels: []string{"task"}, ContentHash: "hash-task", CreatedAt: base, UpdatedAt: base.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	err = store.UpsertSourceGraph(ctx, cache.SourceGraph{
		Source:     cache.Source{RepoID: "fixture-a", ID: "DOC-123", Kind: "doc", Path: "docs/design.md", Title: "Design Doc", Body: "intro same\nbacklog design same\nfinal", Status: "ready", Labels: []string{"zeta", "design"}, ContentHash: "hash-doc", CreatedAt: base, UpdatedAt: base},
		Identities: []cache.Identity{{RepoID: "fixture-a", AliasType: "path", Alias: "docs/design.md", Remote: cache.RemoteAlias{Type: "remote", ID: "wiki/design"}}},
		Links:      []cache.Link{{RepoID: "fixture-a", TargetID: "TASK-001", Kind: "mentions", Text: "task"}},
		Chunks:     []cache.Chunk{{RepoID: "fixture-a", ID: "chunk-doc", ContentHash: "hash-doc", ByteStart: 0, ByteEnd: 13, LineStart: 2, LineEnd: 2, HeadingPath: []string{"Design"}, Text: "backlog chunk", NormalizedText: "backlog chunk", InheritedMetadata: map[string]string{"owner": "docs"}, OutboundLinks: []string{"TASK-001"}, ResolvedAliases: map[string]string{"TASK-001": "task:1"}}},
		SyncStatus: &cache.SyncStatus{RepoID: "fixture-a", RemoteType: "remote", RemoteID: "wiki/design", RemoteRevision: "rev-1", Status: "fresh", LastFetchedAt: base},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = store.UpsertLink(ctx, cache.Link{RepoID: "fixture-a", SourceID: "TASK-001", TargetID: "DOC-123", Kind: "mentions", Text: "doc"})
	if err != nil {
		t.Fatal(err)
	}
}

func seededSyncService(t *testing.T, ctx context.Context, client gitcode.Client) *Service {
	t.Helper()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	seedStore(t, ctx, store)
	return NewWithClient(store, client)
}

type fakeGitCodeClient struct {
	wiki         gitcode.WikiPage
	issue        gitcode.Issue
	comments     []gitcode.Comment
	errors       []error
	wikiCalls    int
	issueCalls   int
	commentCalls int
}

func (f *fakeGitCodeClient) nextError() error {
	if len(f.errors) == 0 {
		return nil
	}
	err := f.errors[0]
	f.errors = f.errors[1:]
	return err
}

func (f *fakeGitCodeClient) ListIssues(context.Context, gitcode.IssueListRequest) (gitcode.Page[gitcode.IssueSummary], error) {
	return gitcode.Page[gitcode.IssueSummary]{}, nil
}
func (f *fakeGitCodeClient) GetIssue(context.Context, gitcode.IssueRequest) (gitcode.Issue, error) {
	f.issueCalls++
	if err := f.nextError(); err != nil {
		return gitcode.Issue{}, err
	}
	return f.issue, nil
}
func (f *fakeGitCodeClient) ListIssueComments(context.Context, gitcode.IssueRequest) (gitcode.Page[gitcode.Comment], error) {
	f.commentCalls++
	return gitcode.Page[gitcode.Comment]{Items: f.comments}, nil
}
func (f *fakeGitCodeClient) GetWikiPage(context.Context, gitcode.WikiPageRequest) (gitcode.WikiPage, error) {
	f.wikiCalls++
	if err := f.nextError(); err != nil {
		return gitcode.WikiPage{}, err
	}
	return f.wiki, nil
}
func (f *fakeGitCodeClient) ListWikiPages(context.Context, gitcode.WikiListRequest) (gitcode.Page[gitcode.WikiPage], error) {
	return gitcode.Page[gitcode.WikiPage]{}, nil
}
func (f *fakeGitCodeClient) Search(context.Context, gitcode.SearchRequest) (gitcode.Page[gitcode.SearchResult], error) {
	return gitcode.Page[gitcode.SearchResult]{}, nil
}
func (f *fakeGitCodeClient) ListIssueAttachments(context.Context, gitcode.IssueRequest) (gitcode.Page[gitcode.AttachmentSummary], error) {
	return gitcode.Page[gitcode.AttachmentSummary]{}, nil
}
func (f *fakeGitCodeClient) GetAttachment(context.Context, gitcode.AttachmentRequest) (gitcode.AttachmentBody, error) {
	return gitcode.AttachmentBody{}, nil
}
func (f *fakeGitCodeClient) CreateIssue(context.Context, gitcode.CreateIssueRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	return gitcode.WriteResult[gitcode.Issue]{}, nil
}
func (f *fakeGitCodeClient) UpdateIssue(context.Context, gitcode.UpdateIssueRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	return gitcode.WriteResult[gitcode.Issue]{}, nil
}
func (f *fakeGitCodeClient) CreateIssueComment(context.Context, gitcode.CreateIssueCommentRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Comment], error) {
	return gitcode.WriteResult[gitcode.Comment]{}, nil
}
func (f *fakeGitCodeClient) CreateWikiPage(context.Context, gitcode.CreateWikiPageRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.WikiPage], error) {
	return gitcode.WriteResult[gitcode.WikiPage]{}, nil
}
func (f *fakeGitCodeClient) UpdateWikiPage(context.Context, gitcode.UpdateWikiPageRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.WikiPage], error) {
	return gitcode.WriteResult[gitcode.WikiPage]{}, nil
}
func (f *fakeGitCodeClient) AddLabel(context.Context, gitcode.LabelRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	return gitcode.WriteResult[gitcode.Issue]{}, nil
}
func (f *fakeGitCodeClient) RemoveLabel(context.Context, gitcode.LabelRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	return gitcode.WriteResult[gitcode.Issue]{}, nil
}

var _ gitcode.Client = (*fakeGitCodeClient)(nil)

type corruptingStore struct {
	cache.Store
}

func (s corruptingStore) IntegrityCheck(context.Context) error {
	return cache.ErrCacheCorruption{Path: "memory", Detail: "test corruption"}
}

type brokenStore struct {
	sources map[string]cache.Source
	links   []cache.Link
}

func fakeBrokenStore() *brokenStore {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return &brokenStore{sources: map[string]cache.Source{"DOC-1": {RepoID: "fixture-a", ID: "DOC-1", Kind: "doc", Path: "doc.md", Title: "Doc", Body: "body", Status: "ready", UpdatedAt: now}}, links: []cache.Link{{RepoID: "fixture-a", SourceID: "DOC-1", TargetID: "MISSING-1", Kind: "mentions", Text: "missing"}}}
}

func (f *brokenStore) AddRepository(context.Context, cache.RepositoryBinding) error { return nil }
func (f *brokenStore) UpsertRepo(context.Context, cache.RepositoryBinding) error    { return nil }
func (f *brokenStore) GetRepository(context.Context, string) (cache.RepositoryBinding, error) {
	return cache.RepositoryBinding{RepoID: "fixture-a", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}, nil
}
func (f *brokenStore) GetRepo(ctx context.Context, repoID string) (cache.RepositoryBinding, error) {
	return f.GetRepository(ctx, repoID)
}
func (f *brokenStore) ListRepositories(context.Context) ([]cache.RepositoryBinding, error) {
	return nil, nil
}
func (f *brokenStore) UpsertSourceGraph(context.Context, cache.SourceGraph) error { return nil }
func (f *brokenStore) UpsertRecordGraph(context.Context, cache.RecordGraph) error { return nil }
func (f *brokenStore) UpsertSyncGraph(context.Context, cache.SyncGraph) error { return nil }
func (f *brokenStore) UpsertSource(context.Context, cache.Source) error { return nil }
func (f *brokenStore) GetSource(_ context.Context, id string) (cache.Source, error) {
	if source, ok := f.sources[id]; ok {
		return source, nil
	}
	return cache.Source{}, cache.ErrNotFound
}
func (f *brokenStore) GetSourceScoped(_ context.Context, repoID, id string) (cache.Source, error) {
	if source, ok := f.sources[id]; ok && source.RepoID == repoID {
		return source, nil
	}
	return cache.Source{}, cache.ErrNotFound
}
func (f *brokenStore) ListSources(context.Context, cache.SourceFilter) ([]cache.Source, error) {
	out := make([]cache.Source, 0, len(f.sources))
	for _, source := range f.sources {
		out = append(out, source)
	}
	return out, nil
}
func (f *brokenStore) SearchSources(context.Context, cache.SearchQuery) ([]cache.SearchResult, error) {
	return nil, nil
}
func (f *brokenStore) GetRecord(context.Context, string, string) (cache.Record, error) {
	return cache.Record{}, cache.ErrNotFound
}
func (f *brokenStore) ListRecords(context.Context, cache.RecordFilter) ([]cache.Record, error) {
	return nil, nil
}
func (f *brokenStore) SearchRecords(context.Context, cache.SearchQuery) ([]cache.SearchResult, error) {
	return nil, nil
}
func (f *brokenStore) UpsertIdentity(context.Context, cache.Identity) error { return nil }
func (f *brokenStore) GetIdentityMap(context.Context, string) ([]cache.Identity, error) {
	return nil, nil
}
func (f *brokenStore) GetIdentityMapScoped(context.Context, string, string) ([]cache.Identity, error) {
	return nil, nil
}
func (f *brokenStore) ResolveAlias(context.Context, cache.RemoteAlias) (cache.Identity, error) {
	return cache.Identity{}, cache.ErrNotFound
}
func (f *brokenStore) ResolveAliasScoped(context.Context, string, cache.RemoteAlias) (cache.Identity, error) {
	return cache.Identity{}, cache.ErrNotFound
}
func (f *brokenStore) ResolveRepoAlias(context.Context, string, cache.RemoteAlias) (cache.Identity, error) {
	return cache.Identity{}, cache.ErrNotFound
}
func (f *brokenStore) DiagnoseAlias(context.Context, cache.RemoteAlias) ([]cache.Identity, error) {
	return nil, nil
}
func (f *brokenStore) UpsertLink(context.Context, cache.Link) error { return nil }
func (f *brokenStore) ListLinks(context.Context, cache.LinkFilter) ([]cache.Link, error) {
	return f.links, nil
}
func (f *brokenStore) GetBacklinks(context.Context, string) ([]cache.Source, error) { return nil, nil }
func (f *brokenStore) GetBacklinksScoped(context.Context, string, string) ([]cache.Source, error) {
	return nil, nil
}
func (f *brokenStore) UpsertChunk(context.Context, cache.Chunk) (cache.Chunk, error) {
	return cache.Chunk{}, nil
}
func (f *brokenStore) GetChunks(context.Context, string) ([]cache.Chunk, error) { return nil, nil }
func (f *brokenStore) GetChunksScoped(context.Context, string, string) ([]cache.Chunk, error) {
	return nil, nil
}
func (f *brokenStore) RecordSyncEvent(context.Context, cache.SyncEvent) error { return nil }
func (f *brokenStore) GetSyncEventByKey(ctx context.Context, key string) (*cache.SyncEvent, error) {
	return nil, nil
}
func (f *brokenStore) GetSyncStatus(context.Context, string) (cache.SyncStatus, error) {
	return cache.SyncStatus{}, nil
}
func (f *brokenStore) GetSyncStatusScoped(context.Context, string, string) (cache.SyncStatus, error) {
	return cache.SyncStatus{}, nil
}
func (f *brokenStore) UpsertConflict(context.Context, cache.Conflict) error { return nil }
func (f *brokenStore) GetConflicts(context.Context, string) ([]cache.Conflict, error) {
	return nil, nil
}
func (f *brokenStore) RecordCounts(context.Context, string) (cache.RecordCounts, error) {
	return cache.RecordCounts{}, nil
}
func (f *brokenStore) WALCapable(context.Context) (bool, string, error)     { return true, "memory", nil }
func (f *brokenStore) UpsertSnapshot(context.Context, cache.Snapshot) error { return nil }
func (f *brokenStore) ListSnapshotChunks(context.Context, string, string) ([]cache.SnapshotChunk, error) {
	return nil, nil
}
func (f *brokenStore) IntegrityCheck(context.Context) error { return nil }
func (f *brokenStore) AcquireLock(context.Context, string) (*cache.LockHandle, error) {
	return nil, nil
}
func (f *brokenStore) ReleaseLock(context.Context, *cache.LockHandle) error { return nil }
func (f *brokenStore) Close() error                                         { return nil }
