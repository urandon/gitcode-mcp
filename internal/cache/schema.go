package cache

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

const currentSchemaVersion = 12

type VersionCompatibility struct {
	DetectedVersion int
	ExpectedVersion int
	Compatible      bool
	PermitWrites    bool
	Message         string
	Remediation     string
}

var ErrSchemaVersionIncompatible = errors.New("cache: schema version is incompatible")

type SchemaVersionError struct {
	Compat VersionCompatibility
}

func (e *SchemaVersionError) Error() string {
	if e == nil {
		return ErrSchemaVersionIncompatible.Error()
	}
	if e.Compat.Remediation == "" {
		return fmt.Sprintf("%s: detected=%d expected=%d: %s", ErrSchemaVersionIncompatible, e.Compat.DetectedVersion, e.Compat.ExpectedVersion, e.Compat.Message)
	}
	return fmt.Sprintf("%s: detected=%d expected=%d: %s; %s", ErrSchemaVersionIncompatible, e.Compat.DetectedVersion, e.Compat.ExpectedVersion, e.Compat.Message, e.Compat.Remediation)
}

func (e *SchemaVersionError) Unwrap() error {
	return ErrSchemaVersionIncompatible
}

func CheckVersionCompatibility(ctx context.Context, db *sql.DB) (VersionCompatibility, error) {
	expected := currentSchemaVersion

	hasTable, err := hasSchemaVersionTable(ctx, db)
	if err != nil {
		return VersionCompatibility{}, err
	}
	if !hasTable {
		empty, err := isEmptyDatabase(ctx, db)
		if err != nil {
			return VersionCompatibility{}, err
		}
		if empty {
			return VersionCompatibility{
				DetectedVersion: 0,
				ExpectedVersion: expected,
				Compatible:      true,
				PermitWrites:    true,
				Message:         "cache database is uninitialized",
				Remediation:     "",
			}, nil
		}
		return VersionCompatibility{
			DetectedVersion: 0,
			ExpectedVersion: expected,
			Compatible:      false,
			PermitWrites:    false,
			Message:         "cache database was created by a pre-schema-versioning binary (iteration 1 equivalent)",
			Remediation:     "re-initialize the cache with 'gitcode-mcp reinit-cache' or delete the cache file and re-sync",
		}, nil
	}

	detected, err := schemaVersion(ctx, db)
	if err != nil {
		return VersionCompatibility{}, err
	}

	if detected == 0 {
		return VersionCompatibility{
			DetectedVersion: 0,
			ExpectedVersion: expected,
			Compatible:      false,
			PermitWrites:    false,
			Message:         "cache database contains an empty schema_version table (iteration 1 equivalent)",
			Remediation:     "re-initialize the cache with 'gitcode-mcp reinit-cache' or delete the cache file and re-sync",
		}, nil
	}

	if detected > expected {
		return VersionCompatibility{
			DetectedVersion: detected,
			ExpectedVersion: expected,
			Compatible:      false,
			PermitWrites:    false,
			Message:         fmt.Sprintf("cache schema version %d is newer than supported version %d", detected, expected),
			Remediation:     "upgrade the gitcode-mcp binary to a version that supports this schema, or downgrade the cache",
		}, nil
	}

	if detected < expected {
		return VersionCompatibility{
			DetectedVersion: detected,
			ExpectedVersion: expected,
			Compatible:      true,
			PermitWrites:    false,
			Message:         fmt.Sprintf("cache schema version %d is older than expected version %d; writes are blocked until migration completes", detected, expected),
			Remediation:     fmt.Sprintf("run 'gitcode-mcp migrate-cache' to upgrade the schema from version %d to version %d", detected, expected),
		}, nil
	}

	return VersionCompatibility{
		DetectedVersion: detected,
		ExpectedVersion: expected,
		Compatible:      true,
		PermitWrites:    true,
		Message:         "cache schema is up to date",
		Remediation:     "",
	}, nil
}

type migration struct {
	version int
	apply   func(context.Context, *sql.Tx, bool) error
}

var migrations = []migration{
	{version: 1, apply: applyInitialMigration},
	{version: 2, apply: applyRepoScopedCacheMigration},
	{version: 3, apply: applyChunkPolicyMigration},
	{version: 4, apply: applyStoredSnapshotMigration},
	{version: 5, apply: applySyncEventTimestampsMigration},
	{version: 6, apply: applySyncEventZeroDeltaMigration},
	{version: 7, apply: applyAuditIdempotencyMigration},
	{version: 8, apply: applyCacheConfirmationsMigration},
	{version: 9, apply: applyAuditConfirmationsMigration},
	{version: 10, apply: applySourceOriginProvenanceMigration},
	{version: 11, apply: applyRecordFixtureLiveProvenanceMigration},
	{version: 12, apply: applyPRReviewCommentsMigration},
}

func runMigrations(ctx context.Context, db *sql.DB, ftsAvailable bool) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	version, err := schemaVersion(ctx, db)
	if err != nil {
		return err
	}
	if version > currentSchemaVersion {
		return fmt.Errorf("cache: schema version %d is newer than supported version %d", version, currentSchemaVersion)
	}
	for _, m := range migrations {
		if m.version <= version {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if err = m.apply(ctx, tx, ftsAvailable); err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err = tx.ExecContext(ctx, `DELETE FROM schema_version`); err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, m.version); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		version = m.version
	}
	return nil
}

func schemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM schema_version`).Scan(&count); err != nil {
		return 0, err
	}
	if count == 0 {
		return 0, nil
	}
	if count > 1 {
		return 0, fmt.Errorf("cache: schema_version must contain one row, found %d", count)
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}

func applyInitialMigration(ctx context.Context, tx *sql.Tx, ftsAvailable bool) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS repos (
	repo_id TEXT PRIMARY KEY,
	owner TEXT NOT NULL,
	name TEXT NOT NULL,
	api_base_url TEXT NOT NULL,
	scopes TEXT NOT NULL,
	display_name TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`,
		`CREATE TABLE IF NOT EXISTS repo_aliases (
	alias TEXT PRIMARY KEY,
	repo_id TEXT NOT NULL REFERENCES repos(repo_id) ON DELETE CASCADE,
	created_at TEXT NOT NULL
)`,
		`CREATE INDEX IF NOT EXISTS idx_repo_aliases_repo ON repo_aliases(repo_id)`,
		`CREATE TABLE IF NOT EXISTS sources (
	repo_id TEXT NOT NULL REFERENCES repos(repo_id) ON DELETE CASCADE,
	id TEXT NOT NULL,
	kind TEXT NOT NULL,
	path TEXT NOT NULL,
	title TEXT NOT NULL,
	body TEXT NOT NULL,
	status TEXT NOT NULL,
	labels TEXT NOT NULL,
	content_hash TEXT NOT NULL,
	provenance TEXT NOT NULL DEFAULT 'fixture' CHECK(provenance IN ('fixture', 'live')),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(repo_id, id)
)`,
		`CREATE INDEX IF NOT EXISTS idx_sources_kind_status ON sources(repo_id, kind, status)`,
		`CREATE TABLE IF NOT EXISTS identity_map (
	repo_id TEXT NOT NULL,
	source_id TEXT NOT NULL,
	alias_type TEXT NOT NULL,
	alias TEXT NOT NULL,
	remote_type TEXT NOT NULL DEFAULT '',
	remote_id TEXT NOT NULL DEFAULT '',
	PRIMARY KEY(repo_id, alias_type, alias),
	UNIQUE(repo_id, source_id, alias_type, alias),
	FOREIGN KEY(repo_id, source_id) REFERENCES sources(repo_id, id) ON DELETE CASCADE
)`,
		`CREATE INDEX IF NOT EXISTS idx_identity_source ON identity_map(repo_id, source_id)`,
		`CREATE INDEX IF NOT EXISTS idx_identity_remote ON identity_map(repo_id, remote_type, remote_id)`,
		`CREATE TABLE IF NOT EXISTS links (
	repo_id TEXT NOT NULL,
	source_id TEXT NOT NULL,
	target_id TEXT NOT NULL,
	kind TEXT NOT NULL,
	text TEXT NOT NULL,
	PRIMARY KEY(repo_id, source_id, target_id, kind, text),
	FOREIGN KEY(repo_id, source_id) REFERENCES sources(repo_id, id) ON DELETE CASCADE,
	FOREIGN KEY(repo_id, target_id) REFERENCES sources(repo_id, id) ON DELETE CASCADE
)`,
		`CREATE INDEX IF NOT EXISTS idx_links_target ON links(repo_id, target_id)`,
		`CREATE TABLE IF NOT EXISTS chunks (
	repo_id TEXT NOT NULL,
	id TEXT NOT NULL,
	source_id TEXT NOT NULL,
	record_id TEXT NOT NULL DEFAULT '',
	snapshot_id TEXT NOT NULL DEFAULT '',
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
	policy TEXT NOT NULL DEFAULT 'heading',
	PRIMARY KEY(repo_id, id),
	UNIQUE(repo_id, source_id, content_hash, byte_start, policy, snapshot_id),
	FOREIGN KEY(repo_id, source_id) REFERENCES sources(repo_id, id) ON DELETE CASCADE
)`,
		`CREATE INDEX IF NOT EXISTS idx_chunks_source ON chunks(repo_id, source_id)`,
		`CREATE INDEX IF NOT EXISTS idx_chunks_query ON chunks(repo_id, source_id, record_id, snapshot_id, policy, byte_start, id)`,
		`CREATE TABLE IF NOT EXISTS remote_revisions (
	repo_id TEXT NOT NULL,
	source_id TEXT NOT NULL,
	remote_type TEXT NOT NULL,
	remote_id TEXT NOT NULL,
	remote_revision TEXT NOT NULL,
	status TEXT NOT NULL,
	last_fetched_at TEXT NOT NULL,
	PRIMARY KEY(repo_id, source_id),
	FOREIGN KEY(repo_id, source_id) REFERENCES sources(repo_id, id) ON DELETE CASCADE
)`,
		`CREATE TABLE IF NOT EXISTS sync_events (
	repo_id TEXT NOT NULL,
	id TEXT NOT NULL,
	source_id TEXT NOT NULL,
	remote_type TEXT NOT NULL,
	remote_id TEXT NOT NULL,
	remote_revision TEXT NOT NULL,
	status TEXT NOT NULL,
	idempotency_key TEXT NOT NULL,
	message TEXT NOT NULL,
	created_at TEXT NOT NULL,
	started_at TEXT NOT NULL DEFAULT '',
	completed_at TEXT NOT NULL DEFAULT '',
	zero_delta INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY(repo_id, id),
	FOREIGN KEY(repo_id, source_id) REFERENCES sources(repo_id, id) ON DELETE CASCADE
)`,
		`CREATE INDEX IF NOT EXISTS idx_sync_events_source ON sync_events(repo_id, source_id)`,
		`CREATE INDEX IF NOT EXISTS idx_sync_events_idempotency_key ON sync_events(repo_id, idempotency_key)`,
		`CREATE TABLE IF NOT EXISTS conflicts (
	repo_id TEXT NOT NULL,
	id TEXT NOT NULL,
	source_id TEXT NOT NULL,
	kind TEXT NOT NULL,
	local_payload TEXT NOT NULL,
	remote_payload TEXT NOT NULL,
	created_at TEXT NOT NULL,
	PRIMARY KEY(repo_id, id),
	FOREIGN KEY(repo_id, source_id) REFERENCES sources(repo_id, id) ON DELETE CASCADE
)`,
		`CREATE INDEX IF NOT EXISTS idx_conflicts_source ON conflicts(repo_id, source_id)`,
	}
	if ftsAvailable {
		statements = append(statements, `CREATE VIRTUAL TABLE IF NOT EXISTS fts_index USING fts5(repo_id UNINDEXED, source_id UNINDEXED, path UNINDEXED, title, body)`)
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func applyRepoScopedCacheMigration(ctx context.Context, tx *sql.Tx, ftsAvailable bool) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS records (
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
)`,
		`CREATE INDEX IF NOT EXISTS idx_records_type_status ON records(repo_id, record_type, status)`,
		`CREATE INDEX IF NOT EXISTS idx_records_remote ON records(repo_id, remote_type, remote_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_records_remote_unique ON records(repo_id, remote_type, remote_id) WHERE remote_type <> '' AND remote_id <> ''`,
		`CREATE TABLE IF NOT EXISTS record_comments (
	repo_id TEXT NOT NULL,
	record_id TEXT NOT NULL,
	comment_id TEXT NOT NULL,
	author TEXT NOT NULL,
	body TEXT NOT NULL,
	content_hash TEXT NOT NULL,
	remote_revision TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(repo_id, record_id, comment_id),
	FOREIGN KEY(repo_id, record_id) REFERENCES records(repo_id, record_id) ON DELETE CASCADE
)`,
		`CREATE INDEX IF NOT EXISTS idx_record_comments_record ON record_comments(repo_id, record_id)`,
		`CREATE TABLE IF NOT EXISTS audit_trail (
	repo_id TEXT NOT NULL REFERENCES repos(repo_id) ON DELETE CASCADE,
	id TEXT NOT NULL,
	operation TEXT NOT NULL,
	record_id TEXT NOT NULL DEFAULT '',
	remote_type TEXT NOT NULL DEFAULT '',
	remote_id TEXT NOT NULL DEFAULT '',
	idempotency_key TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL,
	message TEXT NOT NULL DEFAULT '',
	payload_hash TEXT NOT NULL DEFAULT '',
	command TEXT NOT NULL DEFAULT '',
	mode TEXT NOT NULL DEFAULT '',
	request_metadata TEXT NOT NULL DEFAULT '{}',
	created_at TEXT NOT NULL,
	PRIMARY KEY(repo_id, id)
)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_trail_record ON audit_trail(repo_id, record_id)`,
		`CREATE INDEX IF NOT EXISTS idx_audit_trail_idempotency ON audit_trail(repo_id, idempotency_key)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_audit_trail_idempotency_unique ON audit_trail(repo_id, idempotency_key) WHERE idempotency_key <> ''`,
		`CREATE TABLE IF NOT EXISTS snapshots (
	repo_id TEXT NOT NULL REFERENCES repos(repo_id) ON DELETE CASCADE,
	snapshot_id TEXT NOT NULL,
	format TEXT NOT NULL,
	content_hash TEXT NOT NULL,
	record_count INTEGER NOT NULL,
	created_at TEXT NOT NULL,
	schema_version TEXT NOT NULL DEFAULT 'gitcode-mcp.snapshot.v1',
	manifest_hash TEXT NOT NULL DEFAULT '',
	chunk_set_hash TEXT NOT NULL DEFAULT '',
	chunk_count INTEGER NOT NULL DEFAULT 0,
	manifest_json TEXT NOT NULL DEFAULT '{}',
	warnings_json TEXT NOT NULL DEFAULT '[]',
	metadata TEXT NOT NULL DEFAULT '{}',
	PRIMARY KEY(repo_id, snapshot_id)
)`,
		`CREATE TABLE IF NOT EXISTS snapshot_chunks (
	repo_id TEXT NOT NULL,
	snapshot_id TEXT NOT NULL,
	chunk_id TEXT NOT NULL,
	source_type TEXT NOT NULL DEFAULT '',
	source_id TEXT NOT NULL DEFAULT '',
	record_id TEXT NOT NULL,
	source_content_hash TEXT NOT NULL DEFAULT '',
	source_revision_hash TEXT NOT NULL DEFAULT '',
	index_build_id TEXT NOT NULL DEFAULT '',
	chunk_content_hash TEXT NOT NULL DEFAULT '',
	byte_start INTEGER NOT NULL,
	byte_end INTEGER NOT NULL,
	line_start INTEGER NOT NULL,
	line_end INTEGER NOT NULL,
	heading_path TEXT NOT NULL DEFAULT '[]',
	ordinal INTEGER NOT NULL DEFAULT 0,
	text TEXT NOT NULL DEFAULT '',
	metadata_json TEXT NOT NULL DEFAULT '{}',
	outbound_links_json TEXT NOT NULL DEFAULT '[]',
	resolved_aliases_json TEXT NOT NULL DEFAULT '{}',
	citation TEXT NOT NULL DEFAULT '',
	content_hash TEXT NOT NULL DEFAULT '',
	PRIMARY KEY(repo_id, snapshot_id, chunk_id),
	FOREIGN KEY(repo_id, snapshot_id) REFERENCES snapshots(repo_id, snapshot_id) ON DELETE CASCADE
)`,
		`CREATE INDEX IF NOT EXISTS idx_snapshot_chunks_record ON snapshot_chunks(repo_id, record_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_remote_revisions_remote_unique ON remote_revisions(repo_id, remote_type, remote_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_sync_events_idempotency_unique ON sync_events(repo_id, idempotency_key)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func applyChunkPolicyMigration(ctx context.Context, tx *sql.Tx, ftsAvailable bool) error {
	columns, err := tableColumns(ctx, tx, "chunks")
	if err != nil {
		return err
	}
	addColumns := map[string]string{
		"record_id":   `ALTER TABLE chunks ADD COLUMN record_id TEXT NOT NULL DEFAULT ''`,
		"snapshot_id": `ALTER TABLE chunks ADD COLUMN snapshot_id TEXT NOT NULL DEFAULT ''`,
		"policy":      `ALTER TABLE chunks ADD COLUMN policy TEXT NOT NULL DEFAULT 'heading'`,
	}
	for column, statement := range addColumns {
		if columns[column] {
			continue
		}
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_chunks_query ON chunks(repo_id, source_id, record_id, snapshot_id, policy, byte_start, id)`)
	return err
}

func applyStoredSnapshotMigration(ctx context.Context, tx *sql.Tx, ftsAvailable bool) error {
	snapshotColumns, err := tableColumns(ctx, tx, "snapshots")
	if err != nil {
		return err
	}
	snapshotAdds := map[string]string{
		"schema_version": `ALTER TABLE snapshots ADD COLUMN schema_version TEXT NOT NULL DEFAULT 'gitcode-mcp.snapshot.v1'`,
		"manifest_hash":  `ALTER TABLE snapshots ADD COLUMN manifest_hash TEXT NOT NULL DEFAULT ''`,
		"chunk_set_hash": `ALTER TABLE snapshots ADD COLUMN chunk_set_hash TEXT NOT NULL DEFAULT ''`,
		"chunk_count":    `ALTER TABLE snapshots ADD COLUMN chunk_count INTEGER NOT NULL DEFAULT 0`,
		"manifest_json":  `ALTER TABLE snapshots ADD COLUMN manifest_json TEXT NOT NULL DEFAULT '{}'`,
		"warnings_json":  `ALTER TABLE snapshots ADD COLUMN warnings_json TEXT NOT NULL DEFAULT '[]'`,
	}
	for column, statement := range snapshotAdds {
		if !snapshotColumns[column] {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
	}
	chunkColumns, err := tableColumns(ctx, tx, "snapshot_chunks")
	if err != nil {
		return err
	}
	chunkAdds := map[string]string{
		"source_type":           `ALTER TABLE snapshot_chunks ADD COLUMN source_type TEXT NOT NULL DEFAULT ''`,
		"source_id":             `ALTER TABLE snapshot_chunks ADD COLUMN source_id TEXT NOT NULL DEFAULT ''`,
		"source_content_hash":   `ALTER TABLE snapshot_chunks ADD COLUMN source_content_hash TEXT NOT NULL DEFAULT ''`,
		"source_revision_hash":  `ALTER TABLE snapshot_chunks ADD COLUMN source_revision_hash TEXT NOT NULL DEFAULT ''`,
		"index_build_id":        `ALTER TABLE snapshot_chunks ADD COLUMN index_build_id TEXT NOT NULL DEFAULT ''`,
		"chunk_content_hash":    `ALTER TABLE snapshot_chunks ADD COLUMN chunk_content_hash TEXT NOT NULL DEFAULT ''`,
		"heading_path":          `ALTER TABLE snapshot_chunks ADD COLUMN heading_path TEXT NOT NULL DEFAULT '[]'`,
		"ordinal":               `ALTER TABLE snapshot_chunks ADD COLUMN ordinal INTEGER NOT NULL DEFAULT 0`,
		"text":                  `ALTER TABLE snapshot_chunks ADD COLUMN text TEXT NOT NULL DEFAULT ''`,
		"metadata_json":         `ALTER TABLE snapshot_chunks ADD COLUMN metadata_json TEXT NOT NULL DEFAULT '{}'`,
		"outbound_links_json":   `ALTER TABLE snapshot_chunks ADD COLUMN outbound_links_json TEXT NOT NULL DEFAULT '[]'`,
		"resolved_aliases_json": `ALTER TABLE snapshot_chunks ADD COLUMN resolved_aliases_json TEXT NOT NULL DEFAULT '{}'`,
	}
	for column, statement := range chunkAdds {
		if !chunkColumns[column] {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
	}
	_, err = tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS idx_snapshot_chunks_order ON snapshot_chunks(repo_id, snapshot_id, source_type, source_id, record_id, ordinal, chunk_id)`)
	return err
}

func applySyncEventTimestampsMigration(ctx context.Context, tx *sql.Tx, ftsAvailable bool) error {
	columns, err := tableColumns(ctx, tx, "sync_events")
	if err != nil {
		return err
	}
	addColumns := map[string]string{
		"started_at":   `ALTER TABLE sync_events ADD COLUMN started_at TEXT NOT NULL DEFAULT ''`,
		"completed_at": `ALTER TABLE sync_events ADD COLUMN completed_at TEXT NOT NULL DEFAULT ''`,
	}
	for column, statement := range addColumns {
		if columns[column] {
			continue
		}
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func applySyncEventZeroDeltaMigration(ctx context.Context, tx *sql.Tx, ftsAvailable bool) error {
	columns, err := tableColumns(ctx, tx, "sync_events")
	if err != nil {
		return err
	}
	if columns["zero_delta"] {
		return nil
	}
	_, err = tx.ExecContext(ctx, `ALTER TABLE sync_events ADD COLUMN zero_delta INTEGER NOT NULL DEFAULT 0`)
	return err
}

func applyAuditIdempotencyMigration(ctx context.Context, tx *sql.Tx, ftsAvailable bool) error {
	_, err := tx.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_audit_trail_idempotency_unique ON audit_trail(repo_id, idempotency_key) WHERE idempotency_key <> ''`)
	return err
}

func applyCacheConfirmationsMigration(ctx context.Context, tx *sql.Tx, ftsAvailable bool) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS cache_confirmations (
	repo_id TEXT NOT NULL REFERENCES repos(repo_id) ON DELETE CASCADE,
	id TEXT NOT NULL,
	command TEXT NOT NULL,
	record_id TEXT NOT NULL,
	record_type TEXT NOT NULL DEFAULT '',
	remote_type TEXT NOT NULL,
	remote_id TEXT NOT NULL,
	idempotency_key TEXT NOT NULL,
	status TEXT NOT NULL,
	source_fingerprint TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	PRIMARY KEY(repo_id, id),
	UNIQUE(repo_id, idempotency_key),
	FOREIGN KEY(repo_id, record_id) REFERENCES records(repo_id, record_id) ON DELETE CASCADE
)`,
		`CREATE INDEX IF NOT EXISTS idx_cache_confirmations_record ON cache_confirmations(repo_id, record_id)`,
		`CREATE INDEX IF NOT EXISTS idx_cache_confirmations_remote ON cache_confirmations(repo_id, remote_type, remote_id)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func applyAuditConfirmationsMigration(ctx context.Context, tx *sql.Tx, ftsAvailable bool) error {
	columns, err := tableColumns(ctx, tx, "audit_trail")
	if err != nil {
		return err
	}
	addColumns := map[string]string{
		"command":          `ALTER TABLE audit_trail ADD COLUMN command TEXT NOT NULL DEFAULT ''`,
		"mode":             `ALTER TABLE audit_trail ADD COLUMN mode TEXT NOT NULL DEFAULT ''`,
		"request_metadata": `ALTER TABLE audit_trail ADD COLUMN request_metadata TEXT NOT NULL DEFAULT '{}'`,
	}
	for column, statement := range addColumns {
		if columns[column] {
			continue
		}
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func applySourceOriginProvenanceMigration(ctx context.Context, tx *sql.Tx, ftsAvailable bool) error {
	columns, err := tableColumns(ctx, tx, "sources")
	if err != nil {
		return err
	}
	if columns["provenance"] {
		return nil
	}
	_, err = tx.ExecContext(ctx, `ALTER TABLE sources ADD COLUMN provenance TEXT NOT NULL DEFAULT 'fixture' CHECK(provenance IN ('fixture', 'live'))`)
	return err
}

func applyRecordFixtureLiveProvenanceMigration(ctx context.Context, tx *sql.Tx, ftsAvailable bool) error {
	// In SQLite we cannot ALTER a CHECK constraint.  Recreate the records
	// table with the expanded provenance domain, repopulate, and recreate
	// foreign keys on dependent tables.
	//
	// Because record_comments carries FOREIGN KEY REFERENCES records and
	// SQLite does not support ALTER TABLE ADD CONSTRAINT, disable foreign
	// key enforcement during the replay window.

	if _, err := tx.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS records_new (
	repo_id TEXT NOT NULL REFERENCES repos(repo_id) ON DELETE CASCADE,
	record_id TEXT NOT NULL,
	record_type TEXT NOT NULL,
	path TEXT NOT NULL,
	title TEXT NOT NULL,
	body TEXT NOT NULL,
	status TEXT NOT NULL,
	labels TEXT NOT NULL,
	content_hash TEXT NOT NULL,
	provenance TEXT NOT NULL CHECK(provenance IN ('remote', 'projection', 'bridge', 'fixture', 'live')),
	remote_type TEXT NOT NULL DEFAULT '',
	remote_id TEXT NOT NULL DEFAULT '',
	remote_revision TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(repo_id, record_id)
)`); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO records_new SELECT * FROM records`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE records`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE records_new RENAME TO records`); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		return err
	}

	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_records_type_status ON records(repo_id, record_type, status)`,
		`CREATE INDEX IF NOT EXISTS idx_records_remote ON records(repo_id, remote_type, remote_id)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_records_remote_unique ON records(repo_id, remote_type, remote_id) WHERE remote_type <> '' AND remote_id <> ''`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func applyPRReviewCommentsMigration(ctx context.Context, tx *sql.Tx, ftsAvailable bool) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS pr_review_comments (
	repo_id TEXT NOT NULL,
	source_id TEXT NOT NULL,
	pr_number INTEGER NOT NULL,
	comment_id TEXT NOT NULL,
	discussion_id TEXT NOT NULL DEFAULT '',
	review_kind TEXT NOT NULL DEFAULT '',
	author TEXT NOT NULL DEFAULT '',
	path TEXT NOT NULL DEFAULT '',
	line INTEGER NOT NULL DEFAULT 0,
	start_line INTEGER NOT NULL DEFAULT 0,
	end_line INTEGER NOT NULL DEFAULT 0,
	position INTEGER NOT NULL DEFAULT 0,
	original_position INTEGER NOT NULL DEFAULT 0,
	resolved TEXT NOT NULL DEFAULT '',
	resolvable TEXT NOT NULL DEFAULT '',
	parent_id TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	PRIMARY KEY(repo_id, source_id),
	FOREIGN KEY(repo_id, source_id) REFERENCES sources(repo_id, id) ON DELETE CASCADE
)`,
		`CREATE INDEX IF NOT EXISTS idx_pr_review_comments_pr ON pr_review_comments(repo_id, pr_number)`,
		`CREATE INDEX IF NOT EXISTS idx_pr_review_comments_discussion ON pr_review_comments(repo_id, pr_number, discussion_id)`,
	}
	for _, stmt := range statements {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func tableColumns(ctx context.Context, tx *sql.Tx, table string) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &pk); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func detectFTS5(ctx context.Context, db *sql.DB) bool {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE VIRTUAL TABLE temp.fts5_probe USING fts5(value)`); err != nil {
		return false
	}
	return true
}
