package cache

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

func TestSchemaVersion(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()

	version, err := schemaVersion(ctx, store.db)
	if err != nil {
		t.Fatalf("schemaVersion returned error: %v", err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, currentSchemaVersion)
	}
	if err := runMigrations(ctx, store.db, store.useFTS); err != nil {
		t.Fatalf("second runMigrations returned error: %v", err)
	}
	version, err = schemaVersion(ctx, store.db)
	if err != nil {
		t.Fatalf("schemaVersion after rerun returned error: %v", err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("schema version after rerun = %d, want %d", version, currentSchemaVersion)
	}
}

func TestInitialMigration(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()

	tables := tableNames(t, ctx, store.db)
	for _, want := range []string{"schema_version", "repos", "repo_aliases", "sources", "identity_map", "links", "remote_revisions", "sync_events", "conflicts", "chunks", "records", "record_comments", "audit_trail", "snapshots", "snapshot_chunks"} {
		if !tables[want] {
			t.Fatalf("missing table %s; tables=%v", want, tables)
		}
	}
	if store.useFTS && !tables["fts_index"] {
		t.Fatalf("FTS enabled store missing fts_index table")
	}
	indexes := indexNames(t, ctx, store.db)
	for _, want := range []string{"idx_repo_aliases_repo", "idx_sources_kind_status", "idx_identity_source", "idx_identity_remote", "idx_links_target", "idx_sync_events_source", "idx_chunks_source", "idx_records_type_status", "idx_records_remote", "idx_records_remote_unique", "idx_record_comments_record", "idx_audit_trail_record", "idx_snapshot_chunks_record"} {
		if !indexes[want] {
			t.Fatalf("missing index %s; indexes=%v", want, indexes)
		}
	}
	assertEmbeddingNullable(t, ctx, store.db)
}

func TestRepoScopedCacheMigrationConstraints(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()

	if err := store.UpsertRecordGraph(ctx, RecordGraph{Record: Record{RepoID: "fixture-a", ID: "BAD", Type: "issue", Path: "issues/bad.md", Title: "Bad", Body: "bad", Status: "open", ContentHash: "bad", Provenance: "invalid"}}); err == nil {
		t.Fatalf("invalid provenance was accepted")
	}

	if err := store.UpsertRecordGraph(ctx, RecordGraph{Record: Record{RepoID: "fixture-a", ID: "ISSUE-1", Type: "issue", Path: "issues/1.md", Title: "Issue", Body: "body", Status: "open", ContentHash: "h1", Provenance: ProvenanceRemote, RemoteType: "issue", RemoteID: "1"}}); err != nil {
		t.Fatalf("UpsertRecordGraph remote returned error: %v", err)
	}
	if err := store.UpsertRecordGraph(ctx, RecordGraph{Record: Record{RepoID: "fixture-a", ID: "ISSUE-1-DUP", Type: "issue", Path: "issues/1-dup.md", Title: "Issue dup", Body: "body", Status: "open", ContentHash: "h2", Provenance: ProvenanceRemote, RemoteType: "issue", RemoteID: "1"}}); err == nil {
		t.Fatalf("duplicate remote identity was accepted")
	}
}

func TestSearchFallbackParity(t *testing.T) {
	ctx := context.Background()
	ftsStore := newTestStore(t, ctx)
	defer ftsStore.Close()
	fallbackStore, err := newSQLiteStore(ctx, ":memory:", true)
	if err != nil {
		t.Fatalf("new fallback store returned error: %v", err)
	}
	defer fallbackStore.Close()
	mustAddTestRepo(t, ctx, fallbackStore, "fixture-a")

	graphs := []SourceGraph{
		{Source: Source{ID: "DOC-001", Kind: "doc", Path: "docs/doc-001.md", Title: "Backlog Architecture", Body: "Cache-first backlog source.\nMore cache text.", Status: "ready", Labels: []string{"cache"}, ContentHash: "hash-1"}},
		{Source: Source{ID: "TASK-002", Kind: "task", Path: "project/task-002.md", Title: "Cache task", Body: "This task references the backlog and migration search.", Status: "ready", Labels: []string{"cache"}, ContentHash: "hash-2"}},
		{Source: Source{ID: "DOC-003", Kind: "doc", Path: "docs/doc-003.md", Title: "Other", Body: "Unrelated source text.", Status: "ready", Labels: []string{"cache"}, ContentHash: "hash-3"}},
	}
	for _, graph := range graphs {
		mustUpsertGraph(t, ctx, ftsStore, graph)
		mustUpsertGraph(t, ctx, fallbackStore, graph)
	}

	ftsResults, err := ftsStore.SearchSources(ctx, SearchQuery{Query: "backlog", Limit: 10})
	if err != nil {
		t.Fatalf("FTS SearchSources returned error: %v", err)
	}
	fallbackResults, err := fallbackStore.SearchSources(ctx, SearchQuery{Query: "backlog", Limit: 10})
	if err != nil {
		t.Fatalf("fallback SearchSources returned error: %v", err)
	}
	if !reflect.DeepEqual(visibleSearchResults(ftsResults), visibleSearchResults(fallbackResults)) {
		t.Fatalf("visible search results differ\nfts=%#v\nfallback=%#v", visibleSearchResults(ftsResults), visibleSearchResults(fallbackResults))
	}
	if _, err := json.Marshal(ftsResults); err != nil {
		t.Fatalf("SearchResult JSON marshal returned error: %v", err)
	}

	ftsTaskResults, err := ftsStore.SearchSources(ctx, SearchQuery{Query: "backlog", Kind: "task", Limit: 1})
	if err != nil {
		t.Fatalf("FTS kind SearchSources returned error: %v", err)
	}
	fallbackTaskResults, err := fallbackStore.SearchSources(ctx, SearchQuery{Query: "backlog", Kind: "task", Limit: 1})
	if err != nil {
		t.Fatalf("fallback kind SearchSources returned error: %v", err)
	}
	if !reflect.DeepEqual(visibleSearchResults(ftsTaskResults), visibleSearchResults(fallbackTaskResults)) {
		t.Fatalf("visible kind search results differ\nfts=%#v\nfallback=%#v", visibleSearchResults(ftsTaskResults), visibleSearchResults(fallbackTaskResults))
	}
}

func TestFTSAvailability(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	if store.useFTS != detectFTS5(ctx, store.db) {
		t.Fatalf("store.useFTS = %v, want detectFTS5 result", store.useFTS)
	}

	fallbackStore, err := newSQLiteStore(ctx, ":memory:", true)
	if err != nil {
		t.Fatalf("new fallback store returned error: %v", err)
	}
	defer fallbackStore.Close()
	if fallbackStore.useFTS {
		t.Fatalf("forced fallback store has useFTS=true")
	}
	if tableNames(t, ctx, fallbackStore.db)["fts_index"] {
		t.Fatalf("forced fallback store should not create fts_index")
	}
}

func tableNames(t *testing.T, ctx context.Context, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type IN ('table', 'virtual table', 'shadow table')`)
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	defer rows.Close()
	names := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		names[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table rows: %v", err)
	}
	return names
}

func indexNames(t *testing.T, ctx context.Context, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'index'`)
	if err != nil {
		t.Fatalf("query indexes: %v", err)
	}
	defer rows.Close()
	names := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan index name: %v", err)
		}
		names[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("index rows: %v", err)
	}
	return names
}

func assertEmbeddingNullable(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(chunks)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info returned error: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, pk int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		if name == "embedding" {
			if columnType != "BLOB" || notNull != 0 || (defaultValue.Valid && defaultValue.String != "NULL") {
				t.Fatalf("embedding column type/default/notnull = %q/%v/%d, want BLOB/NULL/0", columnType, defaultValue, notNull)
			}
			return
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows error: %v", err)
	}
	t.Fatalf("chunks table missing embedding column")
}

type visibleSearchResult struct {
	ID      string
	Path    string
	Title   string
	Snippet string
	Line    int
}

func visibleSearchResults(results []SearchResult) []visibleSearchResult {
	visible := make([]visibleSearchResult, 0, len(results))
	for _, result := range results {
		visible = append(visible, visibleSearchResult{ID: result.ID, Path: result.Path, Title: result.Title, Snippet: result.Snippet, Line: result.Line})
	}
	sort.SliceStable(visible, func(i, j int) bool {
		return visible[i].ID < visible[j].ID
	})
	return visible
}
