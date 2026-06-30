package cache

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func createFullVersion2Cache(t *testing.T) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "cache-v2-full.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open temp db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		t.Fatalf("set busy_timeout: %v", err)
	}

	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		t.Fatalf("create schema_version: %v", err)
	}

	useFTS := detectFTS5(ctx, db)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}
	if err := applyInitialMigration(ctx, tx, useFTS); err != nil {
		_ = tx.Rollback()
		t.Fatalf("apply v1 migration: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit v1: %v", err)
	}

	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx v2: %v", err)
	}
	if err := applyRepoScopedCacheMigration(ctx, tx, useFTS); err != nil {
		_ = tx.Rollback()
		t.Fatalf("apply v2 migration: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit v2: %v", err)
	}

	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx v3: %v", err)
	}
	if err := applyChunkPolicyMigration(ctx, tx, useFTS); err != nil {
		_ = tx.Rollback()
		t.Fatalf("apply v3 migration: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit v3: %v", err)
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM schema_version`); err != nil {
		t.Fatalf("clear schema_version: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, 2); err != nil {
		t.Fatalf("set schema version to 2: %v", err)
	}

	return path
}

func insertV2TestData(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open v2 cache: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO repos (repo_id, owner, name, api_base_url, scopes, display_name, created_at, updated_at)
		VALUES ('test-repo', 'owner', 'test', 'https://example.invalid/api', 'issues,wiki', 'Test Repo', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert repo: %v", err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO sources (repo_id, id, kind, path, title, body, status, labels, content_hash, created_at, updated_at)
		VALUES ('test-repo', 'SRC-001', 'issue', 'issues/1.md', 'Test Issue', 'This is a test issue body.', 'open', '["bug"]', 'hash-src-001', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert source: %v", err)
	}

	if _, err := db.ExecContext(ctx, `INSERT INTO records (repo_id, record_id, record_type, path, title, body, status, labels, content_hash, provenance, remote_type, remote_id, remote_revision, created_at, updated_at)
		VALUES ('test-repo', 'REC-001', 'issue', 'issues/1.md', 'Record Issue', 'Record body.', 'open', '[]', 'hash-rec-001', 'remote', 'issue', '42', 'rev-1', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert record: %v", err)
	}
}

func assertV2DataPreserved(t *testing.T, path string) {
	t.Helper()
	ctx := context.Background()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open migrated cache: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}

	var repoCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM repos WHERE repo_id = 'test-repo'`).Scan(&repoCount); err != nil {
		t.Fatalf("count repos: %v", err)
	}
	if repoCount != 1 {
		t.Fatalf("repo count = %d, want 1 (data preserved)", repoCount)
	}

	var srcTitle string
	if err := db.QueryRowContext(ctx, `SELECT title FROM sources WHERE id = 'SRC-001'`).Scan(&srcTitle); err != nil {
		t.Fatalf("query source: %v", err)
	}
	if srcTitle != "Test Issue" {
		t.Fatalf("source title = %q, want 'Test Issue' (data preserved)", srcTitle)
	}

	var recBody string
	if err := db.QueryRowContext(ctx, `SELECT body FROM records WHERE record_id = 'REC-001'`).Scan(&recBody); err != nil {
		t.Fatalf("query record: %v", err)
	}
	if recBody != "Record body." {
		t.Fatalf("record body = %q, want 'Record body.' (data preserved)", recBody)
	}
}

func TestMigrateFromVersion2ToVersion4(t *testing.T) {
	ctx := context.Background()
	path := createFullVersion2Cache(t)
	insertV2TestData(t, path)

	result, err := MigrateCacheWithConfirm(ctx, path, false, Confirmation{Confirmed: true})
	if err != nil {
		t.Fatalf("MigrateCacheWithConfirm returned error: %v", err)
	}
	if result.FromVersion != 2 {
		t.Fatalf("FromVersion = %d, want 2", result.FromVersion)
	}
	if result.ToVersion != currentSchemaVersion {
		t.Fatalf("ToVersion = %d, want %d", result.ToVersion, currentSchemaVersion)
	}

	foundV3 := false
	foundV4 := false
	foundV5 := false
	for _, v := range result.Applied {
		if v == 3 {
			foundV3 = true
		}
		if v == 4 {
			foundV4 = true
		}
		if v == 5 {
			foundV5 = true
		}
	}
	if !foundV3 {
		t.Fatalf("Migration version 3 was not applied; Applied=%v", result.Applied)
	}
	if !foundV4 {
		t.Fatalf("Migration version 4 was not applied; Applied=%v", result.Applied)
	}
	if !foundV5 {
		t.Fatalf("Migration version 5 was not applied; Applied=%v", result.Applied)
	}
	if result.BackupPath == "" {
		t.Fatalf("BackupPath is empty; backup should have been created")
	}
	if _, err := os.Stat(result.BackupPath); err != nil {
		t.Fatalf("backup file does not exist at %s: %v", result.BackupPath, err)
	}

	assertSchemaVersion(t, ctx, path, currentSchemaVersion)
	assertV2DataPreserved(t, path)
	assertV4SchemaTables(t, ctx, path)
}

func TestMigrateFromVersion2RequiresConfirm(t *testing.T) {
	ctx := context.Background()
	path := createFullVersion2Cache(t)

	result, err := MigrateCacheWithConfirm(ctx, path, false, Confirmation{Confirmed: false})
	if err != nil {
		t.Fatalf("MigrateCacheWithConfirm without confirm returned error: %v", err)
	}
	if len(result.Applied) != 0 {
		t.Fatalf("Applied = %v, want no migrations applied without confirm", result.Applied)
	}
	if result.FromVersion != 2 {
		t.Fatalf("FromVersion = %d, want 2", result.FromVersion)
	}

	assertSchemaVersion(t, ctx, path, 2)
}

func TestMigrateFromVersion2BackupBeforeMigration(t *testing.T) {
	ctx := context.Background()
	path := createFullVersion2Cache(t)
	insertV2TestData(t, path)

	result, err := MigrateCacheWithConfirm(ctx, path, false, Confirmation{Confirmed: true})
	if err != nil {
		t.Fatalf("MigrateCacheWithConfirm returned error: %v", err)
	}
	if result.BackupPath == "" {
		t.Fatalf("BackupPath is empty")
	}

	backupInfo, err := os.Stat(result.BackupPath)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	originalInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat original: %v", err)
	}
	if backupInfo.Size() == 0 {
		t.Fatalf("backup file is empty")
	}
	if originalInfo.Size() == 0 {
		t.Fatalf("original file is empty after migration")
	}

	assertV2DataPreserved(t, result.BackupPath)

	if !strings.HasSuffix(result.BackupPath, "Z") {
		t.Fatalf("BackupPath %q does not end with UTC timestamp suffix", result.BackupPath)
	}
	if !strings.Contains(result.BackupPath, ".backup-") {
		t.Fatalf("BackupPath %q does not contain '.backup-' pattern", result.BackupPath)
	}
}

func TestMigrateFromVersion1Blocked(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache-v1.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open temp db: %v", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		t.Fatalf("enable foreign keys: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE legacy_sources (id TEXT PRIMARY KEY, title TEXT)`); err != nil {
		db.Close()
		t.Fatalf("create legacy table: %v", err)
	}
	db.Close()

	result, err := MigrateCacheWithConfirm(ctx, path, false, Confirmation{Confirmed: true})
	if err != nil {
		t.Fatalf("MigrateCacheWithConfirm returned error: %v", err)
	}
	if result.Compatibility.Compatible {
		t.Fatalf("Compatibility.Compatible = true for iter-1 cache, want false")
	}
	if len(result.Applied) != 0 {
		t.Fatalf("Applied = %v, want no migrations applied for iter-1 cache", result.Applied)
	}
	if result.BackupPath != "" {
		t.Fatalf("BackupPath = %q, want empty (no backup for incompatible cache)", result.BackupPath)
	}
	if !strings.Contains(result.Compatibility.Remediation, "re-initialize") && !strings.Contains(result.Compatibility.Remediation, "reinit") {
		t.Fatalf("Remediation = %q, want re-initialization recommendation", result.Compatibility.Remediation)
	}
}

func TestMigrateFromCurrentVersionNoOp(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache-current.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open temp db: %v", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		t.Fatalf("enable foreign keys: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		db.Close()
		t.Fatalf("create schema_version: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, currentSchemaVersion); err != nil {
		db.Close()
		t.Fatalf("insert version: %v", err)
	}
	db.Close()

	result, err := MigrateCacheWithConfirm(ctx, path, false, Confirmation{Confirmed: true})
	if err != nil {
		t.Fatalf("MigrateCacheWithConfirm returned error: %v", err)
	}
	if result.FromVersion != currentSchemaVersion {
		t.Fatalf("FromVersion = %d, want %d", result.FromVersion, currentSchemaVersion)
	}
	if result.ToVersion != currentSchemaVersion {
		t.Fatalf("ToVersion = %d, want %d", result.ToVersion, currentSchemaVersion)
	}
	if len(result.Applied) != 0 {
		t.Fatalf("Applied = %v, want no migrations applied for current version", result.Applied)
	}
	if result.BackupPath != "" {
		t.Fatalf("BackupPath = %q, want empty (no backup for current version)", result.BackupPath)
	}
}

func TestMigrateNoCacheFile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "nonexistent.db")

	result, err := MigrateCacheWithConfirm(ctx, path, false, Confirmation{Confirmed: true})
	if err != nil {
		t.Fatalf("MigrateCacheWithConfirm returned error: %v", err)
	}
	if result.FromVersion != 0 {
		t.Fatalf("FromVersion = %d, want 0", result.FromVersion)
	}
	if len(result.Applied) != 0 {
		t.Fatalf("Applied = %v, want nil for no cache", result.Applied)
	}
}

func TestReadOnlyStoreAcceptsVersionTwo(t *testing.T) {
	ctx := context.Background()
	path := createFullVersion2Cache(t)
	insertV2TestData(t, path)

	store, err := NewSQLiteReadOnlyStore(ctx, path)
	if err != nil {
		t.Fatalf("NewSQLiteReadOnlyStore returned error: %v", err)
	}
	defer store.Close()

	version, err := store.SchemaVersion(ctx)
	if err != nil {
		t.Fatalf("SchemaVersion returned error: %v", err)
	}
	if version != 2 {
		t.Fatalf("SchemaVersion = %d, want 2", version)
	}

	sources, err := store.ListSources(ctx, SourceFilter{Kind: "issue"})
	if err != nil {
		t.Fatalf("ListSources returned error: %v", err)
	}
	if len(sources) < 1 {
		t.Fatalf("ListSources returned %d sources, want >= 1", len(sources))
	}
}

func TestReadOnlyStoreRejectsFutureVersion(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "cache-future-ro.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open temp db: %v", err)
	}
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		t.Fatalf("enable foreign keys: %v", err)
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		db.Close()
		t.Fatalf("create schema_version: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, currentSchemaVersion+1); err != nil {
		db.Close()
		t.Fatalf("insert version: %v", err)
	}
	db.Close()

	store, err := NewSQLiteReadOnlyStore(ctx, path)
	if err == nil {
		store.Close()
		t.Fatalf("NewSQLiteReadOnlyStore returned nil error for future version")
	}
	if !errors.Is(err, ErrSchemaVersionIncompatible) {
		t.Fatalf("NewSQLiteReadOnlyStore error = %v, want ErrSchemaVersionIncompatible", err)
	}
}

func TestMigrateCacheBackupFileRespectsLock(t *testing.T) {
	ctx := context.Background()
	path := createFullVersion2Cache(t)

	result, err := MigrateCacheWithConfirm(ctx, path, false, Confirmation{Confirmed: true})
	if err != nil {
		t.Fatalf("MigrateCacheWithConfirm returned error: %v", err)
	}
	if result.BackupPath == "" {
		t.Fatalf("BackupPath is empty")
	}

	backupInfo, err := os.Stat(result.BackupPath)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	if backupInfo.Mode().Perm()&0077 != 0 {
		t.Fatalf("Backup file permissions = %#o, want owner-only 0600", backupInfo.Mode().Perm())
	}
}

func assertSchemaVersion(t *testing.T, ctx context.Context, path string, expected int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open cache for schema version check: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	version, err := schemaVersion(ctx, db)
	if err != nil {
		t.Fatalf("schemaVersion returned error: %v", err)
	}
	if version != expected {
		t.Fatalf("schema version = %d, want %d", version, expected)
	}
}

func assertV4SchemaTables(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open cache for table check: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		t.Fatalf("enable foreign keys: %v", err)
	}
	tables := tableNames(t, ctx, db)
	for _, want := range []string{"repos", "sources", "chunks", "records", "record_comments", "audit_trail", "snapshots", "snapshot_chunks", "sync_events", "sync_frontiers", "remote_revisions"} {
		if !tables[want] {
			t.Fatalf("missing table %s after migration", want)
		}
	}
	indexes := indexNames(t, ctx, db)
	for _, want := range []string{"idx_chunks_query", "idx_records_remote", "idx_records_remote_unique", "idx_snapshot_chunks_record", "idx_snapshot_chunks_order", "idx_sync_frontiers_repo"} {
		if !indexes[want] {
			t.Fatalf("missing index %s after migration", want)
		}
	}
}
