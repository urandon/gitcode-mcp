package cache

const initialSchema = `
CREATE TABLE IF NOT EXISTS sources (
	id TEXT PRIMARY KEY,
	kind TEXT NOT NULL,
	path TEXT NOT NULL,
	title TEXT NOT NULL,
	body TEXT NOT NULL,
	status TEXT NOT NULL,
	labels TEXT NOT NULL,
	content_hash TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sources_kind_status ON sources(kind, status);

CREATE TABLE IF NOT EXISTS identity_map (
	source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
	alias_type TEXT NOT NULL,
	alias TEXT NOT NULL,
	remote_type TEXT NOT NULL DEFAULT '',
	remote_id TEXT NOT NULL DEFAULT '',
	PRIMARY KEY(alias_type, alias),
	UNIQUE(source_id, alias_type, alias)
);

CREATE INDEX IF NOT EXISTS idx_identity_source ON identity_map(source_id);

CREATE TABLE IF NOT EXISTS links (
	source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
	target_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
	kind TEXT NOT NULL,
	text TEXT NOT NULL,
	PRIMARY KEY(source_id, target_id, kind, text)
);

CREATE INDEX IF NOT EXISTS idx_links_target ON links(target_id);

CREATE TABLE IF NOT EXISTS chunks (
	id TEXT PRIMARY KEY,
	source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
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
	UNIQUE(source_id, content_hash, byte_start)
);

CREATE INDEX IF NOT EXISTS idx_chunks_source ON chunks(source_id);

CREATE TABLE IF NOT EXISTS remote_revisions (
	source_id TEXT PRIMARY KEY REFERENCES sources(id) ON DELETE CASCADE,
	remote_type TEXT NOT NULL,
	remote_id TEXT NOT NULL,
	remote_revision TEXT NOT NULL,
	status TEXT NOT NULL,
	last_fetched_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sync_events (
	id TEXT PRIMARY KEY,
	source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
	remote_type TEXT NOT NULL,
	remote_id TEXT NOT NULL,
	remote_revision TEXT NOT NULL,
	status TEXT NOT NULL,
	idempotency_key TEXT NOT NULL,
	message TEXT NOT NULL,
	created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sync_events_source ON sync_events(source_id);

CREATE TABLE IF NOT EXISTS conflicts (
	id TEXT PRIMARY KEY,
	source_id TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
	kind TEXT NOT NULL,
	local_payload TEXT NOT NULL,
	remote_payload TEXT NOT NULL,
	created_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_conflicts_source ON conflicts(source_id);
`
