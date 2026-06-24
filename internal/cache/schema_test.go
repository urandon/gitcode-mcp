package cache

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
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

func TestCheckVersionCompatibilityCurrent(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()

	compat, err := CheckVersionCompatibility(ctx, store.db)
	if err != nil {
		t.Fatalf("CheckVersionCompatibility returned error: %v", err)
	}
	if !compat.Compatible {
		t.Fatalf("compat.Compatible = false, want true: %#v", compat)
	}
	if !compat.PermitWrites {
		t.Fatalf("compat.PermitWrites = false, want true: %#v", compat)
	}
	if compat.DetectedVersion != currentSchemaVersion || compat.ExpectedVersion != currentSchemaVersion {
		t.Fatalf("versions = detected %d expected %d, want %d", compat.DetectedVersion, compat.ExpectedVersion, currentSchemaVersion)
	}
}

func TestCheckVersionCompatibilityFuture(t *testing.T) {
	ctx := context.Background()
	db := openTempSchemaDB(t, ctx)
	defer db.Close()
	setSchemaVersion(t, ctx, db, currentSchemaVersion+1)

	compat, err := CheckVersionCompatibility(ctx, db)
	if err != nil {
		t.Fatalf("CheckVersionCompatibility returned error: %v", err)
	}
	if compat.Compatible {
		t.Fatalf("compat.Compatible = true, want false: %#v", compat)
	}
	if compat.PermitWrites {
		t.Fatalf("compat.PermitWrites = true, want false: %#v", compat)
	}
	if !strings.Contains(compat.Message, "newer than supported") || !strings.Contains(compat.Remediation, "upgrade") {
		t.Fatalf("future compatibility message/remediation not actionable: %#v", compat)
	}
}

func TestCheckVersionCompatibilityPreSchemaVersion(t *testing.T) {
	ctx := context.Background()
	db := openTempSchemaDB(t, ctx)
	defer db.Close()
	if _, err := db.ExecContext(ctx, `CREATE TABLE legacy_sources (id TEXT PRIMARY KEY)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}

	compat, err := CheckVersionCompatibility(ctx, db)
	if err != nil {
		t.Fatalf("CheckVersionCompatibility returned error: %v", err)
	}
	if compat.Compatible {
		t.Fatalf("compat.Compatible = true, want false: %#v", compat)
	}
	if compat.PermitWrites {
		t.Fatalf("compat.PermitWrites = true, want false: %#v", compat)
	}
	if !strings.Contains(compat.Message, "pre-schema-versioning") || !strings.Contains(compat.Remediation, "re-initialize") {
		t.Fatalf("pre-version compatibility message/remediation not actionable: %#v", compat)
	}
}

func TestNewSQLiteStoreFutureSchemaBlocked(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "future.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open temp db: %v", err)
	}
	setSchemaVersion(t, ctx, db, currentSchemaVersion+1)
	if err := db.Close(); err != nil {
		t.Fatalf("close temp db: %v", err)
	}

	store, err := NewSQLiteStore(ctx, path)
	if err == nil {
		store.Close()
		t.Fatalf("NewSQLiteStore returned nil error for future schema")
	}
	if !errors.Is(err, ErrSchemaVersionIncompatible) {
		t.Fatalf("NewSQLiteStore error = %v, want ErrSchemaVersionIncompatible", err)
	}
	if !strings.Contains(err.Error(), "binary") && !strings.Contains(err.Error(), "upgrade") {
		t.Fatalf("NewSQLiteStore error is not actionable: %v", err)
	}
}

func TestNewSQLiteStoreVersionTwoBlockedWithMigrateHint(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "version-two.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open temp db: %v", err)
	}
	setSchemaVersion(t, ctx, db, 2)
	if err := db.Close(); err != nil {
		t.Fatalf("close temp db: %v", err)
	}

	store, err := NewSQLiteStore(ctx, path)
	if err == nil {
		store.Close()
		t.Fatalf("NewSQLiteStore returned nil error for version 2 schema")
	}
	if !errors.Is(err, ErrSchemaVersionIncompatible) {
		t.Fatalf("NewSQLiteStore error = %v, want ErrSchemaVersionIncompatible", err)
	}
	if !strings.Contains(err.Error(), "detected=2") || !strings.Contains(err.Error(), fmt.Sprintf("expected=%d", currentSchemaVersion)) || !strings.Contains(err.Error(), "migrate-cache") {
		t.Fatalf("NewSQLiteStore error is not actionable: %v", err)
	}
}

func TestCheckVersionCompatibilityVersionTwoSuggestsMigration(t *testing.T) {
	ctx := context.Background()
	db := openTempSchemaDB(t, ctx)
	defer db.Close()
	setSchemaVersion(t, ctx, db, 2)

	compat, err := CheckVersionCompatibility(ctx, db)
	if err != nil {
		t.Fatalf("CheckVersionCompatibility returned error: %v", err)
	}
	if !compat.Compatible {
		t.Fatalf("compat.Compatible = false, want true: %#v", compat)
	}
	if compat.PermitWrites {
		t.Fatalf("compat.PermitWrites = true, want false before migration: %#v", compat)
	}
	if !strings.Contains(compat.Remediation, "migrate-cache") {
		t.Fatalf("compat.Remediation = %q, want migrate-cache hint", compat.Remediation)
	}
}

func TestInitialMigration(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()

	tables := tableNames(t, ctx, store.db)
	for _, want := range []string{"schema_version", "repos", "repo_aliases", "sources", "identity_map", "links", "remote_revisions", "sync_events", "conflicts", "chunks", "records", "record_comments", "audit_trail", "cache_confirmations", "snapshots", "snapshot_chunks"} {
		if !tables[want] {
			t.Fatalf("missing table %s; tables=%v", want, tables)
		}
	}
	if store.useFTS && !tables["fts_index"] {
		t.Fatalf("FTS enabled store missing fts_index table")
	}
	indexes := indexNames(t, ctx, store.db)
	for _, want := range []string{"idx_repo_aliases_repo", "idx_sources_kind_status", "idx_identity_source", "idx_identity_remote", "idx_links_target", "idx_sync_events_source", "idx_chunks_source", "idx_records_type_status", "idx_records_remote", "idx_records_remote_unique", "idx_record_comments_record", "idx_audit_trail_record", "idx_audit_trail_idempotency_unique", "idx_cache_confirmations_record", "idx_cache_confirmations_remote", "idx_snapshot_chunks_record"} {
		if !indexes[want] {
			t.Fatalf("missing index %s; indexes=%v", want, indexes)
		}
	}
	assertEmbeddingNullable(t, ctx, store.db)
}

func TestMigrationZeroDelta(t *testing.T) {
	ctx := context.Background()
	db := openTempSchemaDB(t, ctx)
	defer db.Close()
	for _, m := range migrations {
		if m.version > 5 {
			break
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin migration v%d: %v", m.version, err)
		}
		if err := m.apply(ctx, tx, true); err != nil {
			_ = tx.Rollback()
			t.Fatalf("apply migration v%d: %v", m.version, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit migration v%d: %v", m.version, err)
		}
	}
	setSchemaVersion(t, ctx, db, 5)
	if err := runMigrations(ctx, db, true); err != nil {
		t.Fatalf("runMigrations returned error: %v", err)
	}
	columns := syncEventColumns(t, ctx, db)
	column, ok := columns["zero_delta"]
	if !ok {
		t.Fatalf("zero_delta column missing; columns=%v", columns)
	}
	if column.notNull != 1 || column.defaultValue != "0" {
		t.Fatalf("zero_delta notNull/default = %d/%q, want 1/0", column.notNull, column.defaultValue)
	}
	version, err := schemaVersion(ctx, db)
	if err != nil {
		t.Fatalf("schemaVersion returned error: %v", err)
	}
	if version != currentSchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, currentSchemaVersion)
	}
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

func openTempSchemaDB(t *testing.T, ctx context.Context) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "schema.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open temp schema db: %v", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		_ = db.Close()
		t.Fatalf("enable foreign keys: %v", err)
	}
	return db
}

func setSchemaVersion(t *testing.T, ctx context.Context, db *sql.DB, version int) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create schema_version: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM schema_version`); err != nil {
		t.Fatalf("clear schema_version: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, version); err != nil {
		t.Fatalf("insert schema_version: %v", err)
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

type tableColumn struct {
	columnType   string
	notNull      int
	defaultValue string
	pk           int
}

func syncEventColumns(t *testing.T, ctx context.Context, db *sql.DB) map[string]tableColumn {
	t.Helper()
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(sync_events)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(sync_events) returned error: %v", err)
	}
	defer rows.Close()
	columns := map[string]tableColumn{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, pk int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		column := tableColumn{columnType: columnType, notNull: notNull, pk: pk}
		if defaultValue.Valid {
			column.defaultValue = defaultValue.String
		}
		columns[name] = column
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows error: %v", err)
	}
	return columns
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
