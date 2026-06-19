package cache

import (
	"context"
	"database/sql"
	"time"
)

func (s *SQLiteStore) UpsertIdentity(ctx context.Context, identity Identity) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txRollbackOnError(tx, &err)
	if err = upsertIdentityTx(ctx, tx, identity); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertIdentityTx(ctx context.Context, tx *sql.Tx, identity Identity) error {
	return execTx(ctx, tx, `INSERT INTO identity_map (source_id, alias_type, alias, remote_type, remote_id)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(alias_type, alias) DO UPDATE SET source_id = excluded.source_id, remote_type = excluded.remote_type, remote_id = excluded.remote_id`,
		identity.SourceID, identity.AliasType, identity.Alias, identity.Remote.Type, identity.Remote.ID)
}

func (s *SQLiteStore) GetIdentityMap(ctx context.Context, sourceID string) ([]Identity, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source_id, alias_type, alias, remote_type, remote_id FROM identity_map WHERE source_id = ? ORDER BY alias_type, alias`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIdentities(rows)
}

func (s *SQLiteStore) ResolveAlias(ctx context.Context, alias RemoteAlias) (Identity, error) {
	row := s.db.QueryRowContext(ctx, `SELECT source_id, alias_type, alias, remote_type, remote_id FROM identity_map WHERE (alias_type = ? AND alias = ?) OR (remote_type = ? AND remote_id = ?) ORDER BY source_id LIMIT 1`, alias.Type, alias.ID, alias.Type, alias.ID)
	var identity Identity
	if err := row.Scan(&identity.SourceID, &identity.AliasType, &identity.Alias, &identity.Remote.Type, &identity.Remote.ID); err != nil {
		if err == sql.ErrNoRows {
			return Identity{}, notFoundErr("alias", alias.Type+":"+alias.ID)
		}
		return Identity{}, err
	}
	return identity, nil
}

func scanIdentities(rows *sql.Rows) ([]Identity, error) {
	var identities []Identity
	for rows.Next() {
		var identity Identity
		if err := rows.Scan(&identity.SourceID, &identity.AliasType, &identity.Alias, &identity.Remote.Type, &identity.Remote.ID); err != nil {
			return nil, err
		}
		identities = append(identities, identity)
	}
	return identities, rows.Err()
}

func (s *SQLiteStore) UpsertLink(ctx context.Context, link Link) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txRollbackOnError(tx, &err)
	if err = upsertLinkTx(ctx, tx, link); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertLinkTx(ctx context.Context, tx *sql.Tx, link Link) error {
	return execTx(ctx, tx, `INSERT INTO links (source_id, target_id, kind, text) VALUES (?, ?, ?, ?) ON CONFLICT(source_id, target_id, kind, text) DO NOTHING`, link.SourceID, link.TargetID, link.Kind, link.Text)
}

func (s *SQLiteStore) ListLinks(ctx context.Context, filter LinkFilter) ([]Link, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source_id, target_id, kind, text FROM links WHERE (? = '' OR source_id = ?) AND (? = '' OR target_id = ?) ORDER BY source_id, target_id, kind, text`, filter.SourceID, filter.SourceID, filter.TargetID, filter.TargetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var links []Link
	for rows.Next() {
		var link Link
		if err := rows.Scan(&link.SourceID, &link.TargetID, &link.Kind, &link.Text); err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

func (s *SQLiteStore) GetBacklinks(ctx context.Context, targetID string) ([]Source, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.id, s.kind, s.path, s.title, s.body, s.status, s.labels, s.content_hash, s.created_at, s.updated_at FROM sources s JOIN links l ON l.source_id = s.id WHERE l.target_id = ? ORDER BY s.id`, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sources, err := scanSources(rows)
	if err != nil {
		return nil, err
	}
	for i := range sources {
		aliases, err := s.GetIdentityMap(ctx, sources[i].ID)
		if err != nil {
			return nil, err
		}
		sources[i].Aliases = aliases
	}
	return sources, nil
}

func (s *SQLiteStore) UpsertChunk(ctx context.Context, chunk Chunk) (Chunk, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Chunk{}, err
	}
	defer txRollbackOnError(tx, &err)
	chunk, err = upsertChunkTx(ctx, tx, chunk)
	if err != nil {
		return Chunk{}, err
	}
	return chunk, tx.Commit()
}

func upsertChunkTx(ctx context.Context, tx *sql.Tx, chunk Chunk) (Chunk, error) {
	if chunk.ID == "" {
		chunk.ID = deterministicChunkID(chunk)
	}
	headingPath, err := marshalJSON(chunk.HeadingPath)
	if err != nil {
		return Chunk{}, err
	}
	metadata, err := marshalJSON(chunk.InheritedMetadata)
	if err != nil {
		return Chunk{}, err
	}
	outboundLinks, err := marshalJSON(chunk.OutboundLinks)
	if err != nil {
		return Chunk{}, err
	}
	resolvedAliases, err := marshalJSON(chunk.ResolvedAliases)
	if err != nil {
		return Chunk{}, err
	}
	err = execTx(ctx, tx, `INSERT INTO chunks (id, source_id, content_hash, byte_start, byte_end, line_start, line_end, heading_path, text, normalized_text, inherited_metadata, outbound_links, resolved_aliases, embedding)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET byte_end = excluded.byte_end, line_start = excluded.line_start, line_end = excluded.line_end, heading_path = excluded.heading_path, text = excluded.text, normalized_text = excluded.normalized_text, inherited_metadata = excluded.inherited_metadata, outbound_links = excluded.outbound_links, resolved_aliases = excluded.resolved_aliases, embedding = excluded.embedding`,
		chunk.ID, chunk.SourceID, chunk.ContentHash, chunk.ByteStart, chunk.ByteEnd, chunk.LineStart, chunk.LineEnd, headingPath, chunk.Text, chunk.NormalizedText, metadata, outboundLinks, resolvedAliases, chunk.Embedding)
	return chunk, err
}

func (s *SQLiteStore) GetChunks(ctx context.Context, sourceID string) ([]Chunk, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, source_id, content_hash, byte_start, byte_end, line_start, line_end, heading_path, text, normalized_text, inherited_metadata, outbound_links, resolved_aliases, embedding FROM chunks WHERE source_id = ? ORDER BY byte_start`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var chunks []Chunk
	for rows.Next() {
		var chunk Chunk
		var headingPath, metadata, outboundLinks, resolvedAliases string
		if err := rows.Scan(&chunk.ID, &chunk.SourceID, &chunk.ContentHash, &chunk.ByteStart, &chunk.ByteEnd, &chunk.LineStart, &chunk.LineEnd, &headingPath, &chunk.Text, &chunk.NormalizedText, &metadata, &outboundLinks, &resolvedAliases, &chunk.Embedding); err != nil {
			return nil, err
		}
		if chunk.HeadingPath, err = unmarshalJSON[[]string](headingPath); err != nil {
			return nil, err
		}
		if chunk.InheritedMetadata, err = unmarshalJSON[map[string]string](metadata); err != nil {
			return nil, err
		}
		if chunk.OutboundLinks, err = unmarshalJSON[[]string](outboundLinks); err != nil {
			return nil, err
		}
		if chunk.ResolvedAliases, err = unmarshalJSON[map[string]string](resolvedAliases); err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}
	return chunks, rows.Err()
}

func (s *SQLiteStore) RecordSyncEvent(ctx context.Context, event SyncEvent) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txRollbackOnError(tx, &err)
	if err = recordSyncEventTx(ctx, tx, event); err != nil {
		return err
	}
	return tx.Commit()
}

func recordSyncEventTx(ctx context.Context, tx *sql.Tx, event SyncEvent) error {
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Unix(0, 0).UTC()
	}
	return execTx(ctx, tx, `INSERT INTO sync_events (id, source_id, remote_type, remote_id, remote_revision, status, idempotency_key, message, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET status = excluded.status, message = excluded.message`, event.ID, event.SourceID, event.RemoteType, event.RemoteID, event.RemoteRevision, event.Status, event.IdempotencyKey, event.Message, event.CreatedAt.Format(time.RFC3339Nano))
}

func (s *SQLiteStore) GetSyncStatus(ctx context.Context, sourceID string) (SyncStatus, error) {
	row := s.db.QueryRowContext(ctx, `SELECT source_id, remote_type, remote_id, remote_revision, status, last_fetched_at FROM remote_revisions WHERE source_id = ?`, sourceID)
	var status SyncStatus
	var lastFetched string
	if err := row.Scan(&status.SourceID, &status.RemoteType, &status.RemoteID, &status.RemoteRevision, &status.Status, &lastFetched); err != nil {
		if err == sql.ErrNoRows {
			return SyncStatus{}, notFoundErr("sync status", sourceID)
		}
		return SyncStatus{}, err
	}
	status.LastFetchedAt, _ = time.Parse(time.RFC3339Nano, lastFetched)
	return status, nil
}

func upsertSyncStatusTx(ctx context.Context, tx *sql.Tx, status SyncStatus) error {
	if status.LastFetchedAt.IsZero() {
		status.LastFetchedAt = time.Unix(0, 0).UTC()
	}
	return execTx(ctx, tx, `INSERT INTO remote_revisions (source_id, remote_type, remote_id, remote_revision, status, last_fetched_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(source_id) DO UPDATE SET remote_type = excluded.remote_type, remote_id = excluded.remote_id, remote_revision = excluded.remote_revision, status = excluded.status, last_fetched_at = excluded.last_fetched_at`, status.SourceID, status.RemoteType, status.RemoteID, status.RemoteRevision, status.Status, status.LastFetchedAt.Format(time.RFC3339Nano))
}

func (s *SQLiteStore) UpsertConflict(ctx context.Context, conflict Conflict) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txRollbackOnError(tx, &err)
	if err = upsertConflictTx(ctx, tx, conflict); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertConflictTx(ctx context.Context, tx *sql.Tx, conflict Conflict) error {
	if conflict.CreatedAt.IsZero() {
		conflict.CreatedAt = time.Unix(0, 0).UTC()
	}
	return execTx(ctx, tx, `INSERT INTO conflicts (id, source_id, kind, local_payload, remote_payload, created_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET kind = excluded.kind, local_payload = excluded.local_payload, remote_payload = excluded.remote_payload`, conflict.ID, conflict.SourceID, conflict.Kind, conflict.LocalPayload, conflict.RemotePayload, conflict.CreatedAt.Format(time.RFC3339Nano))
}

func (s *SQLiteStore) GetConflicts(ctx context.Context, sourceID string) ([]Conflict, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, source_id, kind, local_payload, remote_payload, created_at FROM conflicts WHERE source_id = ? ORDER BY id`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var conflicts []Conflict
	for rows.Next() {
		var conflict Conflict
		var created string
		if err := rows.Scan(&conflict.ID, &conflict.SourceID, &conflict.Kind, &conflict.LocalPayload, &conflict.RemotePayload, &created); err != nil {
			return nil, err
		}
		conflict.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		conflicts = append(conflicts, conflict)
	}
	return conflicts, rows.Err()
}
