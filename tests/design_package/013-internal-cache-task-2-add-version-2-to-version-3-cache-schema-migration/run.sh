#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
cd "${REPO_ROOT}"

TMP_DIR="$(mktemp -d)"
trap 'rm -rf "${TMP_DIR}"' EXIT

PASSED=0
FAILED=0
FAILURES=""

pass() { PASSED=$((PASSED+1)); echo "  PASS: $1"; }
fail() { FAILED=$((FAILED+1)); FAILURES="${FAILURES}\n  $1"; echo "  FAIL: $1"; }

HELPER="${TMP_DIR}/validation_helper.go"
cat > "${HELPER}" <<'GO'
package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) != 3 {
		fatalf("usage: validation_helper <create-v2|check-v2|create-v1|check-v1> <db-path>")
	}
	mode := os.Args[1]
	path := os.Args[2]
	switch mode {
	case "create-v2":
		createV2(path)
	case "check-v2":
		checkV2(path)
	case "create-v1":
		createV1(path)
	case "check-v1":
		checkV1(path)
	default:
		fatalf("unknown mode %q", mode)
	}
}

func createV2(path string) {
	ctx := context.Background()
	db := open(path)
	defer db.Close()
	exec(ctx, db, `PRAGMA foreign_keys = ON`)
	exec(ctx, db, `PRAGMA user_version = 2`)
	exec(ctx, db, `CREATE TABLE schema_version (version INTEGER NOT NULL)`)
	exec(ctx, db, `INSERT INTO schema_version (version) VALUES (2)`)
	exec(ctx, db, `CREATE TABLE repos (
		repo_id TEXT PRIMARY KEY,
		owner TEXT NOT NULL,
		name TEXT NOT NULL,
		api_base_url TEXT NOT NULL,
		scopes TEXT NOT NULL,
		display_name TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`)
	exec(ctx, db, `CREATE TABLE sources (
		repo_id TEXT NOT NULL REFERENCES repos(repo_id) ON DELETE CASCADE,
		id TEXT NOT NULL,
		kind TEXT NOT NULL,
		path TEXT NOT NULL,
		title TEXT NOT NULL,
		body TEXT NOT NULL,
		status TEXT NOT NULL,
		labels TEXT NOT NULL,
		content_hash TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY(repo_id, id)
	)`)
	exec(ctx, db, `CREATE TABLE chunks (
		repo_id TEXT NOT NULL,
		id TEXT NOT NULL,
		source_id TEXT NOT NULL,
		content_hash TEXT NOT NULL,
		byte_start INTEGER NOT NULL,
		byte_end INTEGER NOT NULL,
		line_start INTEGER NOT NULL,
		line_end INTEGER NOT NULL,
		heading_path TEXT NOT NULL,
		text TEXT NOT NULL,
		normalized_text TEXT NOT NULL,
		inherited_metadata TEXT NOT NULL,
		outbound_links TEXT NOT NULL,
		resolved_aliases TEXT NOT NULL,
		embedding BLOB DEFAULT NULL,
		PRIMARY KEY(repo_id, id),
		FOREIGN KEY(repo_id, source_id) REFERENCES sources(repo_id, id) ON DELETE CASCADE
	)`)
	exec(ctx, db, `CREATE TABLE records (
		repo_id TEXT NOT NULL REFERENCES repos(repo_id) ON DELETE CASCADE,
		record_id TEXT NOT NULL,
		record_type TEXT NOT NULL,
		path TEXT NOT NULL,
		title TEXT NOT NULL,
		body TEXT NOT NULL,
		status TEXT NOT NULL,
		labels TEXT NOT NULL,
		content_hash TEXT NOT NULL,
		provenance TEXT NOT NULL CHECK(provenance IN ('remote', 'projection', 'bridge')),
		remote_type TEXT NOT NULL DEFAULT '',
		remote_id TEXT NOT NULL DEFAULT '',
		remote_revision TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		PRIMARY KEY(repo_id, record_id)
	)`)
	exec(ctx, db, `CREATE TABLE snapshots (
		repo_id TEXT NOT NULL REFERENCES repos(repo_id) ON DELETE CASCADE,
		snapshot_id TEXT NOT NULL,
		format TEXT NOT NULL,
		content_hash TEXT NOT NULL,
		record_count INTEGER NOT NULL,
		created_at TEXT NOT NULL,
		metadata TEXT NOT NULL DEFAULT '{}',
		PRIMARY KEY(repo_id, snapshot_id)
	)`)
	exec(ctx, db, `CREATE TABLE snapshot_chunks (
		repo_id TEXT NOT NULL,
		snapshot_id TEXT NOT NULL,
		chunk_id TEXT NOT NULL,
		record_id TEXT NOT NULL,
		byte_start INTEGER NOT NULL,
		byte_end INTEGER NOT NULL,
		line_start INTEGER NOT NULL,
		line_end INTEGER NOT NULL,
		citation TEXT NOT NULL DEFAULT '',
		content_hash TEXT NOT NULL DEFAULT '',
		PRIMARY KEY(repo_id, snapshot_id, chunk_id),
		FOREIGN KEY(repo_id, snapshot_id) REFERENCES snapshots(repo_id, snapshot_id) ON DELETE CASCADE
	)`)
	exec(ctx, db, `INSERT INTO repos (repo_id, owner, name, api_base_url, scopes, display_name, created_at, updated_at)
		VALUES ('fixture-repo', 'public-owner', 'public-name', 'https://example.invalid/api', 'issues,wiki', 'Fixture Repo', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	exec(ctx, db, `INSERT INTO sources (repo_id, id, kind, path, title, body, status, labels, content_hash, created_at, updated_at)
		VALUES ('fixture-repo', 'SRC-013', 'issue', 'issues/13.md', 'Migration Sentinel Source', 'source body survives migration', 'open', '[]', 'hash-src-013', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	exec(ctx, db, `INSERT INTO records (repo_id, record_id, record_type, path, title, body, status, labels, content_hash, provenance, remote_type, remote_id, remote_revision, created_at, updated_at)
		VALUES ('fixture-repo', 'REC-013', 'issue', 'issues/13.md', 'Migration Sentinel Record', 'record body survives migration', 'open', '[]', 'hash-rec-013', 'remote', 'issue', '13', 'rev-13', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`)
	exec(ctx, db, `INSERT INTO chunks (repo_id, id, source_id, content_hash, byte_start, byte_end, line_start, line_end, heading_path, text, normalized_text, inherited_metadata, outbound_links, resolved_aliases)
		VALUES ('fixture-repo', 'CHUNK-013', 'SRC-013', 'hash-chunk-013', 0, 32, 1, 1, '[]', 'chunk text survives migration', 'chunk text survives migration', '{}', '[]', '{}')`)
	exec(ctx, db, `INSERT INTO snapshots (repo_id, snapshot_id, format, content_hash, record_count, created_at, metadata)
		VALUES ('fixture-repo', 'SNAP-013', 'json', 'hash-snap-013', 1, '2026-01-01T00:00:00Z', '{}')`)
	exec(ctx, db, `INSERT INTO snapshot_chunks (repo_id, snapshot_id, chunk_id, record_id, byte_start, byte_end, line_start, line_end, citation, content_hash)
		VALUES ('fixture-repo', 'SNAP-013', 'CHUNK-013', 'REC-013', 0, 32, 1, 1, 'citation', 'hash-chunk-013')`)
}

func checkV2(path string) {
	ctx := context.Background()
	db := open(path)
	defer db.Close()
	assertScalar(ctx, db, `SELECT version FROM schema_version`, "4", "schema_version table should be upgraded to current schema version 4")
	assertPragma(ctx, db, `PRAGMA user_version`, "4", "PRAGMA user_version should match current schema version after migration")
	assertScalar(ctx, db, `SELECT title FROM sources WHERE repo_id='fixture-repo' AND id='SRC-013'`, "Migration Sentinel Source", "source row should be preserved")
	assertScalar(ctx, db, `SELECT body FROM records WHERE repo_id='fixture-repo' AND record_id='REC-013'`, "record body survives migration", "record row should be preserved")
	assertScalar(ctx, db, `SELECT text FROM chunks WHERE repo_id='fixture-repo' AND id='CHUNK-013'`, "chunk text survives migration", "chunk row should be preserved")
	for _, column := range []string{"record_id", "snapshot_id", "policy"} {
		assertColumn(ctx, db, "chunks", column)
	}
	for _, column := range []string{"schema_version", "manifest_hash", "chunk_set_hash", "chunk_count", "manifest_json", "warnings_json"} {
		assertColumn(ctx, db, "snapshots", column)
	}
	for _, column := range []string{"source_type", "source_id", "source_content_hash", "source_revision_hash", "index_build_id", "chunk_content_hash", "heading_path", "ordinal", "text", "metadata_json", "outbound_links_json", "resolved_aliases_json"} {
		assertColumn(ctx, db, "snapshot_chunks", column)
	}
}

func createV1(path string) {
	ctx := context.Background()
	db := open(path)
	defer db.Close()
	exec(ctx, db, `PRAGMA user_version = 1`)
	exec(ctx, db, `CREATE TABLE legacy_sources (id TEXT PRIMARY KEY, title TEXT NOT NULL)`)
	exec(ctx, db, `INSERT INTO legacy_sources (id, title) VALUES ('legacy-013', 'Legacy Sentinel')`)
}

func checkV1(path string) {
	ctx := context.Background()
	db := open(path)
	defer db.Close()
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='schema_version'`).Scan(&count); err != nil {
		fatalf("query schema_version table existence: %v", err)
	}
	if count != 0 {
		fatalf("iter-1 cache should not have schema_version table created by failed migration; found %d", count)
	}
	assertScalar(ctx, db, `SELECT title FROM legacy_sources WHERE id='legacy-013'`, "Legacy Sentinel", "legacy data should be preserved")
}

func open(path string) *sql.DB {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		fatalf("open %s: %v", path, err)
	}
	return db
}

func exec(ctx context.Context, db *sql.DB, stmt string) {
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		fatalf("exec failed: %v\nSQL: %s", err, stmt)
	}
}

func assertScalar(ctx context.Context, db *sql.DB, query, want, msg string) {
	var got string
	if err := db.QueryRowContext(ctx, query).Scan(&got); err != nil {
		fatalf("%s: query failed: %v", msg, err)
	}
	if got != want {
		fatalf("%s: got %q want %q", msg, got, want)
	}
}

func assertPragma(ctx context.Context, db *sql.DB, query, want, msg string) {
	var got int
	if err := db.QueryRowContext(ctx, query).Scan(&got); err != nil {
		fatalf("%s: query failed: %v", msg, err)
	}
	if fmt.Sprintf("%d", got) != want {
		fatalf("%s: got %d want %s", msg, got, want)
	}
}

func assertColumn(ctx context.Context, db *sql.DB, table, column string) {
	rows, err := db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		fatalf("PRAGMA table_info(%s): %v", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var def any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &def, &pk); err != nil {
			fatalf("scan table_info(%s): %v", table, err)
		}
		if name == column {
			return
		}
	}
	if err := rows.Err(); err != nil {
		fatalf("table_info(%s) rows: %v", table, err)
	}
	fatalf("missing column %s.%s after migration", table, column)
}

func fatalf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if !strings.HasSuffix(msg, "\n") {
		msg += "\n"
	}
	fmt.Fprint(os.Stderr, msg)
	os.Exit(1)
}
GO

run_helper() {
	go run "${HELPER}" "$@"
}

V2_DB="${TMP_DIR}/iter2-cache.db"
V2_OUT="${TMP_DIR}/iter2-migrate.out"
V1_DB="${TMP_DIR}/iter1-cache.db"
V1_OUT="${TMP_DIR}/iter1-migrate.out"

if run_helper create-v2 "${V2_DB}"; then
	pass "created deterministic iter-2-shaped cache fixture"
else
	fail "failed to create iter-2-shaped cache fixture"
fi

if go run ./cmd/gitcode-mcp migrate-cache --cache-path "${V2_DB}" --confirm --format json > "${V2_OUT}" 2>&1; then
	pass "gitcode-mcp migrate-cache exits 0 for iter-2 cache"
else
	fail "gitcode-mcp migrate-cache should exit 0 for iter-2 cache"
	cat "${V2_OUT}"
fi

if grep -q '"status": "migrated"' "${V2_OUT}" && grep -q '"backup_path":' "${V2_OUT}"; then
	pass "iter-2 migration CLI output reports migrated status and backup path"
else
	fail "iter-2 migration CLI output missing migrated status or backup path"
	cat "${V2_OUT}"
fi

if run_helper check-v2 "${V2_DB}"; then
	pass "iter-2 migration preserves data and updates schema metadata"
else
	fail "iter-2 migration database state check failed"
fi

if run_helper create-v1 "${V1_DB}"; then
	pass "created deterministic iter-1 cache fixture"
else
	fail "failed to create iter-1 cache fixture"
fi

if go run ./cmd/gitcode-mcp migrate-cache --cache-path "${V1_DB}" --confirm --format json > "${V1_OUT}" 2>&1; then
	fail "gitcode-mcp migrate-cache should exit non-zero for iter-1 cache"
	cat "${V1_OUT}"
else
	pass "gitcode-mcp migrate-cache exits non-zero for iter-1 cache"
fi

if grep -q '"status": "incompatible"' "${V1_OUT}" && grep -Eq 're-initialize|reinit' "${V1_OUT}"; then
	pass "iter-1 migration CLI output reports incompatibility and reinit remediation"
else
	fail "iter-1 migration CLI output missing incompatibility or reinit remediation"
	cat "${V1_OUT}"
fi

if run_helper check-v1 "${V1_DB}"; then
	pass "iter-1 cache remains unmigrated and preserves legacy data"
else
	fail "iter-1 no-migration database state check failed"
fi

if compgen -G "${V1_DB}.backup-*" > /dev/null; then
	fail "iter-1 incompatible cache should not create migration backup"
else
	pass "iter-1 incompatible cache did not create backup"
fi

echo ""
echo "=== Summary ==="
echo "Passed: ${PASSED}"
echo "Failed: ${FAILED}"
if [ -n "${FAILURES}" ]; then
	printf "Failures:%b\n" "${FAILURES}"
fi

if [ "${FAILED}" -gt 0 ]; then
	exit 1
fi
exit 0
