package cache

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestBacklinks(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()

	mustUpsertGraph(t, ctx, store, SourceGraph{Source: testSource("DOC-123", "doc", "Design Doc"), Identities: []Identity{{AliasType: "path", Alias: "docs/DOC-123.md"}, {AliasType: "remote", Alias: "issue/123"}}})
	mustUpsertGraph(t, ctx, store, SourceGraph{Source: testSource("TASK-001", "task", "Task"), Links: []Link{{TargetID: "DOC-123", Kind: "references", Text: "DOC-123"}}})

	backlinks, err := store.GetBacklinks(ctx, "DOC-123")
	if err != nil {
		t.Fatalf("GetBacklinks returned error: %v", err)
	}
	if len(backlinks) != 1 {
		t.Fatalf("GetBacklinks returned %d records, want 1", len(backlinks))
	}
	if backlinks[0].ID != "TASK-001" {
		t.Fatalf("backlink source id = %q, want TASK-001", backlinks[0].ID)
	}
	if backlinks[0].Path != "project/task-001.md" {
		t.Fatalf("backlink path = %q, want project/task-001.md", backlinks[0].Path)
	}

	source, err := store.GetSource(ctx, "DOC-123")
	if err != nil {
		t.Fatalf("GetSource returned error: %v", err)
	}
	if len(source.Aliases) != 2 {
		t.Fatalf("GetSource aliases = %d, want 2", len(source.Aliases))
	}
}

func TestChunkSchemaEmbeddingColumn(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()

	var columnType string
	var defaultValue sql.NullString
	var found bool
	rows, err := store.db.QueryContext(ctx, `PRAGMA table_info(chunks)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info returned error: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name string
		var notNull int
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if name == "embedding" {
			found = true
			if columnType != "BLOB" || (defaultValue.Valid && defaultValue.String != "NULL") || notNull != 0 {
				t.Fatalf("embedding column type/default/notnull = %q/%v/%d, want BLOB/NULL/0", columnType, defaultValue, notNull)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows error: %v", err)
	}
	if !found {
		t.Fatalf("chunks table missing embedding column")
	}

	contentHash := "hash-doc-123"
	mustUpsertGraph(t, ctx, store, SourceGraph{Source: testSourceWithHash("DOC-123", "doc", "Design Doc", contentHash)})
	first := Chunk{SourceID: "DOC-123", ContentHash: contentHash, ByteStart: 0, ByteEnd: 20, LineStart: 1, LineEnd: 2, HeadingPath: []string{"Design"}, Text: "first chunk", NormalizedText: "first chunk"}
	second := Chunk{SourceID: "DOC-123", ContentHash: contentHash, ByteStart: 21, ByteEnd: 40, LineStart: 3, LineEnd: 4, HeadingPath: []string{"Design", "Details"}, Text: "second chunk", NormalizedText: "second chunk"}
	if _, err := store.UpsertChunk(ctx, first); err != nil {
		t.Fatalf("UpsertChunk first returned error: %v", err)
	}
	if _, err := store.UpsertChunk(ctx, second); err != nil {
		t.Fatalf("UpsertChunk second returned error: %v", err)
	}
	chunks, err := store.GetChunks(ctx, "DOC-123")
	if err != nil {
		t.Fatalf("GetChunks returned error: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("GetChunks returned %d records, want 2", len(chunks))
	}
	for _, chunk := range chunks {
		if chunk.Embedding != nil {
			t.Fatalf("chunk embedding = %v, want nil", chunk.Embedding)
		}
	}
	duplicate := first
	duplicate.ID = "different-id"
	duplicate.ByteEnd = 30
	if _, err := store.UpsertChunk(ctx, duplicate); err == nil {
		t.Fatalf("duplicate source_id/content_hash/byte_start was accepted")
	}
}

func TestChunkIdentity(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()

	contentHash := "hash-doc-123"
	graph := SourceGraph{
		Source: testSourceWithHash("DOC-123", "doc", "Design Doc", contentHash),
		Chunks: []Chunk{
			{ContentHash: contentHash, ByteStart: 0, ByteEnd: 20, LineStart: 1, LineEnd: 2, HeadingPath: []string{"Design"}, Text: "first chunk", NormalizedText: "first chunk"},
			{ContentHash: contentHash, ByteStart: 21, ByteEnd: 40, LineStart: 3, LineEnd: 4, HeadingPath: []string{"Design", "Details"}, Text: "second chunk", NormalizedText: "second chunk"},
		},
	}
	mustUpsertGraph(t, ctx, store, graph)
	mustUpsertGraph(t, ctx, store, graph)

	chunks, err := store.GetChunks(ctx, "DOC-123")
	if err != nil {
		t.Fatalf("GetChunks returned error: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("GetChunks returned %d records, want 2", len(chunks))
	}
	for _, chunk := range chunks {
		want := deterministicChunkID(chunk)
		if chunk.ID != want {
			t.Fatalf("chunk id = %q, want deterministic %q", chunk.ID, want)
		}
	}
	if chunks[0].ContentHash != chunks[1].ContentHash {
		t.Fatalf("chunks should share content hash")
	}
}

func TestIdentityResolution(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()

	mustUpsertGraph(t, ctx, store, SourceGraph{
		Source: testSource("DOC-123", "doc", "Design Doc"),
		Identities: []Identity{
			{AliasType: "path", Alias: "docs/design.md"},
			{AliasType: "remote", Alias: "wiki/design-doc"},
		},
	})

	identities, err := store.GetIdentityMap(ctx, "DOC-123")
	if err != nil {
		t.Fatalf("GetIdentityMap returned error: %v", err)
	}
	if len(identities) != 2 {
		t.Fatalf("GetIdentityMap returned %d identities, want 2", len(identities))
	}
	resolved, err := store.ResolveAlias(ctx, RemoteAlias{Type: "path", ID: "docs/design.md"})
	if err != nil {
		t.Fatalf("ResolveAlias(path) returned error: %v", err)
	}
	if resolved.SourceID != "DOC-123" {
		t.Fatalf("ResolveAlias(path) = %q, want DOC-123", resolved.SourceID)
	}
	resolved, err = store.ResolveAlias(ctx, RemoteAlias{Type: "remote", ID: "wiki/design-doc"})
	if err != nil {
		t.Fatalf("ResolveAlias(remote) returned error: %v", err)
	}
	if resolved.SourceID != "DOC-123" {
		t.Fatalf("ResolveAlias(remote) = %q, want DOC-123", resolved.SourceID)
	}
}

func TestSourceGraphRollback(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()

	mustUpsertGraph(t, ctx, store, SourceGraph{Source: testSource("DOC-123", "doc", "Design Doc")})
	graph := SourceGraph{
		Source:     testSource("TASK-001", "task", "Task"),
		Identities: []Identity{{AliasType: "path", Alias: "project/task-001.md"}},
		Links:      []Link{{TargetID: "MISSING-999", Kind: "references", Text: "missing target"}},
		Chunks:     []Chunk{{ContentHash: "hash-task-001", ByteStart: 0, ByteEnd: 10, LineStart: 1, LineEnd: 1, Text: "task", NormalizedText: "task"}},
		SyncEvents: []SyncEvent{{ID: "sync-task-001", IdempotencyKey: "key-1", Message: "ingest", Status: "started"}},
		SyncStatus: &SyncStatus{RemoteType: "issue", RemoteID: "1", RemoteRevision: "rev-1", Status: "fresh"},
		Conflicts:  []Conflict{{ID: "conflict-task-001", Kind: "test", LocalPayload: "local", RemotePayload: "remote"}},
	}

	if err := store.UpsertSourceGraph(ctx, graph); err == nil {
		t.Fatalf("UpsertSourceGraph succeeded, want foreign key failure")
	}
	if _, err := store.GetSource(ctx, "TASK-001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSource after rollback error = %v, want ErrNotFound", err)
	}
	if identities, err := store.GetIdentityMap(ctx, "TASK-001"); err != nil || len(identities) != 0 {
		t.Fatalf("identities after rollback = %v, %v; want none", identities, err)
	}
	if chunks, err := store.GetChunks(ctx, "TASK-001"); err != nil || len(chunks) != 0 {
		t.Fatalf("chunks after rollback = %v, %v; want none", chunks, err)
	}
	if _, err := store.GetSyncStatus(ctx, "TASK-001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSyncStatus after rollback error = %v, want ErrNotFound", err)
	}
	if conflicts, err := store.GetConflicts(ctx, "TASK-001"); err != nil || len(conflicts) != 0 {
		t.Fatalf("conflicts after rollback = %v, %v; want none", conflicts, err)
	}
	backlinks, err := store.GetBacklinks(ctx, "MISSING-999")
	if err != nil {
		t.Fatalf("GetBacklinks after rollback returned error: %v", err)
	}
	if len(backlinks) != 0 {
		t.Fatalf("backlinks after rollback = %d, want none", len(backlinks))
	}
}

func newTestStore(t *testing.T, ctx context.Context) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	return store
}

func mustUpsertGraph(t *testing.T, ctx context.Context, store *SQLiteStore, graph SourceGraph) {
	t.Helper()
	if err := store.UpsertSourceGraph(ctx, graph); err != nil {
		t.Fatalf("UpsertSourceGraph returned error: %v", err)
	}
}

func testSource(id string, kind string, title string) Source {
	return testSourceWithHash(id, kind, title, "hash-"+id)
}

func testSourceWithHash(id string, kind string, title string, contentHash string) Source {
	path := "docs/" + id + ".md"
	if kind == "task" {
		path = "project/task-001.md"
	}
	return Source{
		ID:          id,
		Kind:        kind,
		Title:       title,
		Path:        path,
		Body:        "This source body mentions backlog and cache-first design.",
		Status:      "ready",
		Labels:      []string{"cache"},
		ContentHash: contentHash,
		CreatedAt:   time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, 6, 18, 12, 30, 0, 0, time.UTC),
	}
}
