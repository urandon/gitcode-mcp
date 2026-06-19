package cache

import (
	"context"
	"database/sql"
	"fmt"
)

const currentSchemaVersion = 1

type migration struct {
	version int
	apply   func(context.Context, *sql.Tx, bool) error
}

var migrations = []migration{
	{version: 1, apply: applyInitialMigration},
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
	UNIQUE(repo_id, source_id, content_hash, byte_start),
	FOREIGN KEY(repo_id, source_id) REFERENCES sources(repo_id, id) ON DELETE CASCADE
)`,
		`CREATE INDEX IF NOT EXISTS idx_chunks_source ON chunks(repo_id, source_id)`,
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
