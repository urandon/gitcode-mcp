package service

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
)

func TestSearchSources(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	results, err := svc.SearchSources(ctx, SearchSourcesRequest{Query: "backlog", Limit: 10})
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
	record, err := svc.GetSource(ctx, GetSourceRequest{ID: "DOC-123"})
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
	results, err := svc.ListSources(ctx, ListSourcesRequest{Kind: "task", Status: "ready", Limit: 1})
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
	results, err := svc.GetBacklinks(ctx, GetBacklinksRequest{AliasType: "remote", AliasID: "wiki/design"})
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
	resolved, err := svc.ResolveID(ctx, ResolveIDRequest{AliasType: "path", AliasID: "docs/design.md"})
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
	snippet, err := svc.GetSnippet(ctx, SnippetRequest{ID: "DOC-123", LineStart: 2, LineEnd: 99})
	if err != nil {
		t.Fatalf("GetSnippet returned error: %v", err)
	}
	if snippet.LineStart != 2 || snippet.LineEnd != 3 || len(snippet.Warnings) != 1 || snippet.Text == "" {
		t.Fatalf("GetSnippet did not clamp as expected: %#v", snippet)
	}
	chunk, err := svc.GetSnippet(ctx, SnippetRequest{ID: "DOC-123", ChunkID: "chunk-doc"})
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
	status, err := svc.GetSyncStatus(ctx, SyncStatusRequest{ID: "DOC-123"})
	if err != nil {
		t.Fatalf("GetSyncStatus returned error: %v", err)
	}
	if status.Status != "fresh" || status.RemoteID != "wiki/design" {
		t.Fatalf("GetSyncStatus = %#v", status)
	}
}

func TestRecentChanges(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	recent, err := svc.RecentChanges(ctx, RecentChangesRequest{Limit: 2})
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
	result, err := svc.LinkCheck(ctx, LinkCheckRequest{})
	if err != nil {
		t.Fatalf("LinkCheck returned error: %v", err)
	}
	if result.BrokenCount != 1 || result.BrokenLinks[0].TargetID != "MISSING-1" {
		t.Fatalf("LinkCheck = %#v", result)
	}
	_, err = svc.LinkCheck(ctx, LinkCheckRequest{Strict: true})
	var failed ErrLinkCheckFailed
	if !errors.As(err, &failed) {
		t.Fatalf("LinkCheck strict error = %v, want ErrLinkCheckFailed", err)
	}
}

func TestStaleIndex(t *testing.T) {
	ctx := context.Background()
	svc := New(fakeBrokenStore())
	result, err := svc.StaleIndex(ctx, StaleIndexRequest{})
	if err != nil {
		t.Fatalf("StaleIndex returned error: %v", err)
	}
	if result.StaleCount != 1 || result.AffectedSourceIDs[0] != "DOC-1" || result.MissingTargetIDs[0] != "MISSING-1" {
		t.Fatalf("StaleIndex = %#v", result)
	}
	_, err = svc.StaleIndex(ctx, StaleIndexRequest{Strict: true})
	var stale ErrStaleIndex
	if !errors.As(err, &stale) {
		t.Fatalf("StaleIndex strict error = %v, want ErrStaleIndex", err)
	}
}

func TestExportSnapshot(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	svc.now = func() time.Time { return time.Unix(100, 0).UTC() }
	first, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{Format: "text"})
	if err != nil {
		t.Fatalf("ExportSnapshot returned error: %v", err)
	}
	second, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{Format: "text"})
	if err != nil {
		t.Fatalf("ExportSnapshot second returned error: %v", err)
	}
	if first.ContentHash != second.ContentHash || first.InlineContent != second.InlineContent || first.RecordCount != 2 {
		t.Fatalf("ExportSnapshot not deterministic: %#v %#v", first, second)
	}
}

func TestDiffSnapshot(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	diff, err := svc.DiffSnapshot(ctx, DiffSnapshotRequest{BaseContent: "DOC-123\tdocs/design.md\tdoc\tready\tDesign Doc\tdesign\t2026-01-01T00:00:00Z\n", Format: "text"})
	if err != nil {
		t.Fatalf("DiffSnapshot returned error: %v", err)
	}
	if len(diff.ChangedSourceIDs) == 0 || diff.DiffText == "" {
		t.Fatalf("DiffSnapshot = %#v", diff)
	}
}

func TestMCPToolDTOContract(t *testing.T) {
	ctx := context.Background()
	svc := seededService(t, ctx)
	if _, err := svc.SearchSources(ctx, SearchSourcesRequest{Query: "backlog"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetSource(ctx, GetSourceRequest{ID: "DOC-123"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ListSources(ctx, ListSourcesRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetBacklinks(ctx, GetBacklinksRequest{ID: "DOC-123"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveID(ctx, ResolveIDRequest{ID: "DOC-123"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.GetSyncStatus(ctx, SyncStatusRequest{ID: "DOC-123"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.DiffSnapshot(ctx, DiffSnapshotRequest{}); err != nil {
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
	_, err = New(empty).ListSources(ctx, ListSourcesRequest{})
	var cacheEmpty ErrCacheEmpty
	if !errors.As(err, &cacheEmpty) {
		t.Fatalf("empty cache error = %v, want ErrCacheEmpty", err)
	}
	svc := seededService(t, ctx)
	_, err = svc.GetSource(ctx, GetSourceRequest{ID: "NOPE"})
	var notFound ErrNotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("not found error = %v, want ErrNotFound", err)
	}
	_, err = svc.SearchSources(ctx, SearchSourcesRequest{})
	var invalid ErrInvalidQuery
	if !errors.As(err, &invalid) {
		t.Fatalf("invalid query error = %v, want ErrInvalidQuery", err)
	}
	results, err := svc.SearchSources(ctx, SearchSourcesRequest{Query: "same"})
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
		func() error { _, err := svc.SearchSources(ctx, SearchSourcesRequest{Query: "backlog"}); return err },
		func() error { _, err := svc.GetSource(ctx, GetSourceRequest{ID: "DOC-123"}); return err },
		func() error { _, err := svc.ListSources(ctx, ListSourcesRequest{}); return err },
		func() error { _, err := svc.GetBacklinks(ctx, GetBacklinksRequest{ID: "DOC-123"}); return err },
		func() error { _, err := svc.ResolveID(ctx, ResolveIDRequest{ID: "DOC-123"}); return err },
		func() error {
			_, err := svc.GetSnippet(ctx, SnippetRequest{ID: "DOC-123", LineStart: 1, LineEnd: 1})
			return err
		},
		func() error { _, err := svc.GetSyncStatus(ctx, SyncStatusRequest{ID: "DOC-123"}); return err },
		func() error { _, err := svc.RecentChanges(ctx, RecentChangesRequest{}); return err },
		func() error { _, err := svc.LinkCheck(ctx, LinkCheckRequest{}); return err },
		func() error { _, err := svc.StaleIndex(ctx, StaleIndexRequest{}); return err },
		func() error { _, err := svc.ExportSnapshot(ctx, ExportSnapshotRequest{}); return err },
		func() error { _, err := svc.DiffSnapshot(ctx, DiffSnapshotRequest{}); return err },
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
	err := store.UpsertSource(ctx, cache.Source{ID: "DOC-123", Kind: "doc", Path: "docs/design.md", Title: "Design Doc", Body: "intro same\nbacklog design same\nfinal", Status: "ready", Labels: []string{"zeta", "design"}, ContentHash: "hash-doc", CreatedAt: base, UpdatedAt: base})
	if err != nil {
		t.Fatal(err)
	}
	err = store.UpsertSource(ctx, cache.Source{ID: "TASK-001", Kind: "task", Path: "project/tasks/task.md", Title: "Task Backlog", Body: "task same\nbacklog item same", Status: "ready", Labels: []string{"task"}, ContentHash: "hash-task", CreatedAt: base, UpdatedAt: base.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	err = store.UpsertSourceGraph(ctx, cache.SourceGraph{
		Source:     cache.Source{ID: "DOC-123", Kind: "doc", Path: "docs/design.md", Title: "Design Doc", Body: "intro same\nbacklog design same\nfinal", Status: "ready", Labels: []string{"zeta", "design"}, ContentHash: "hash-doc", CreatedAt: base, UpdatedAt: base},
		Identities: []cache.Identity{{AliasType: "path", Alias: "docs/design.md", Remote: cache.RemoteAlias{Type: "remote", ID: "wiki/design"}}},
		Links:      []cache.Link{{TargetID: "TASK-001", Kind: "mentions", Text: "task"}},
		Chunks:     []cache.Chunk{{ID: "chunk-doc", ContentHash: "hash-doc", ByteStart: 0, ByteEnd: 13, LineStart: 2, LineEnd: 2, Text: "backlog chunk", NormalizedText: "backlog chunk"}},
		SyncStatus: &cache.SyncStatus{RemoteType: "remote", RemoteID: "wiki/design", RemoteRevision: "rev-1", Status: "fresh", LastFetchedAt: base},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = store.UpsertLink(ctx, cache.Link{SourceID: "TASK-001", TargetID: "DOC-123", Kind: "mentions", Text: "doc"})
	if err != nil {
		t.Fatal(err)
	}
}

type brokenStore struct {
	sources map[string]cache.Source
	links   []cache.Link
}

func fakeBrokenStore() *brokenStore {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return &brokenStore{sources: map[string]cache.Source{"DOC-1": {ID: "DOC-1", Kind: "doc", Path: "doc.md", Title: "Doc", Body: "body", Status: "ready", UpdatedAt: now}}, links: []cache.Link{{SourceID: "DOC-1", TargetID: "MISSING-1", Kind: "mentions", Text: "missing"}}}
}

func (f *brokenStore) UpsertSourceGraph(context.Context, cache.SourceGraph) error { return nil }
func (f *brokenStore) UpsertSource(context.Context, cache.Source) error           { return nil }
func (f *brokenStore) GetSource(_ context.Context, id string) (cache.Source, error) {
	if source, ok := f.sources[id]; ok {
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
func (f *brokenStore) UpsertIdentity(context.Context, cache.Identity) error { return nil }
func (f *brokenStore) GetIdentityMap(context.Context, string) ([]cache.Identity, error) {
	return nil, nil
}
func (f *brokenStore) ResolveAlias(context.Context, cache.RemoteAlias) (cache.Identity, error) {
	return cache.Identity{}, cache.ErrNotFound
}
func (f *brokenStore) UpsertLink(context.Context, cache.Link) error { return nil }
func (f *brokenStore) ListLinks(context.Context, cache.LinkFilter) ([]cache.Link, error) {
	return f.links, nil
}
func (f *brokenStore) GetBacklinks(context.Context, string) ([]cache.Source, error) { return nil, nil }
func (f *brokenStore) UpsertChunk(context.Context, cache.Chunk) (cache.Chunk, error) {
	return cache.Chunk{}, nil
}
func (f *brokenStore) GetChunks(context.Context, string) ([]cache.Chunk, error) { return nil, nil }
func (f *brokenStore) RecordSyncEvent(context.Context, cache.SyncEvent) error   { return nil }
func (f *brokenStore) GetSyncStatus(context.Context, string) (cache.SyncStatus, error) {
	return cache.SyncStatus{}, nil
}
func (f *brokenStore) UpsertConflict(context.Context, cache.Conflict) error { return nil }
func (f *brokenStore) GetConflicts(context.Context, string) ([]cache.Conflict, error) {
	return nil, nil
}
func (f *brokenStore) IntegrityCheck(context.Context) error { return nil }
func (f *brokenStore) AcquireLock(context.Context, string) (*cache.LockHandle, error) {
	return nil, nil
}
func (f *brokenStore) ReleaseLock(context.Context, *cache.LockHandle) error { return nil }
func (f *brokenStore) Close() error                                         { return nil }
