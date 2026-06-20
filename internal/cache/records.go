package cache

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

func (s *SQLiteStore) UpsertRecordGraph(ctx context.Context, graph RecordGraph) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txRollbackOnError(tx, &err)
	if err = upsertSourceTx(ctx, tx, sourceFromRecord(graph.Record)); err != nil {
		return err
	}
	if err = upsertSearchProjectionTx(ctx, tx, sourceFromRecord(graph.Record), s.useFTS); err != nil {
		return err
	}
	if err = upsertRecordTx(ctx, tx, graph.Record); err != nil {
		return err
	}
	for _, comment := range graph.Comments {
		if comment.RepoID == "" {
			comment.RepoID = graph.Record.RepoID
		}
		if comment.RecordID == "" {
			comment.RecordID = graph.Record.ID
		}
		if err = upsertRecordCommentTx(ctx, tx, comment); err != nil {
			return err
		}
	}
	for _, identity := range graph.Identities {
		if identity.RepoID == "" {
			identity.RepoID = graph.Record.RepoID
		}
		if identity.SourceID == "" {
			identity.SourceID = graph.Record.ID
		}
		if err = upsertIdentityTx(ctx, tx, identity); err != nil {
			return err
		}
	}
	for _, revision := range graph.RemoteRevisions {
		if revision.RepoID == "" {
			revision.RepoID = graph.Record.RepoID
		}
		if revision.RecordID == "" {
			revision.RecordID = graph.Record.ID
		}
		if err = upsertRemoteRevisionTx(ctx, tx, revision); err != nil {
			return err
		}
	}
	for _, event := range graph.SyncEvents {
		if event.RepoID == "" {
			event.RepoID = graph.Record.RepoID
		}
		if event.SourceID == "" {
			event.SourceID = graph.Record.ID
		}
		if err = recordSyncEventTx(ctx, tx, event); err != nil {
			return err
		}
	}
	for _, entry := range graph.AuditTrail {
		if entry.RepoID == "" {
			entry.RepoID = graph.Record.RepoID
		}
		if entry.RecordID == "" {
			entry.RecordID = graph.Record.ID
		}
		if err = insertAuditTrailTx(ctx, tx, entry); err != nil {
			return err
		}
	}
	for _, snapshot := range graph.Snapshots {
		if snapshot.RepoID == "" {
			snapshot.RepoID = graph.Record.RepoID
		}
		if err = upsertSnapshotTx(ctx, tx, snapshot); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLiteStore) UpsertSyncGraph(ctx context.Context, graph SyncGraph) (err error) {
	repoID := strings.TrimSpace(graph.RepoID)
	if repoID == "" {
		repoID = graph.Record.RepoID
	}
	if repoID == "" {
		return notFoundErr("repository", "")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txRollbackOnError(tx, &err)
	if err = requireRepoTx(ctx, tx, repoID); err != nil {
		return err
	}
	graph.Record.RepoID = repoID
	if graph.Record.Provenance == "" {
		graph.Record.Provenance = ProvenanceRemote
	}
	if err = upsertSourceTx(ctx, tx, sourceFromRecord(graph.Record)); err != nil {
		return err
	}
	if err = upsertSearchProjectionTx(ctx, tx, sourceFromRecord(graph.Record), s.useFTS); err != nil {
		return err
	}
	if err = upsertRecordTx(ctx, tx, graph.Record); err != nil {
		return err
	}
	for _, comment := range graph.Comments {
		if comment.RepoID == "" {
			comment.RepoID = repoID
		}
		if comment.RecordID == "" {
			comment.RecordID = graph.Record.ID
		}
		if err = upsertRecordCommentTx(ctx, tx, comment); err != nil {
			return err
		}
	}
	for _, identity := range graph.Identities {
		if identity.RepoID == "" {
			identity.RepoID = repoID
		}
		if identity.SourceID == "" {
			identity.SourceID = graph.Record.ID
		}
		if err = upsertIdentityTx(ctx, tx, identity); err != nil {
			return err
		}
	}
	for _, link := range graph.Links {
		if link.RepoID == "" {
			link.RepoID = repoID
		}
		if link.SourceID == "" {
			link.SourceID = graph.Record.ID
		}
		if err = upsertLinkTx(ctx, tx, link); err != nil {
			return err
		}
	}
	for _, revision := range graph.RemoteRevisions {
		if revision.RepoID == "" {
			revision.RepoID = repoID
		}
		if revision.RecordID == "" {
			revision.RecordID = graph.Record.ID
		}
		if err = upsertRemoteRevisionTx(ctx, tx, revision); err != nil {
			return err
		}
	}
	for _, chunk := range graph.Chunks {
		if chunk.RepoID == "" {
			chunk.RepoID = repoID
		}
		if chunk.SourceID == "" {
			chunk.SourceID = graph.Record.ID
		}
		if _, err = upsertChunkTx(ctx, tx, chunk); err != nil {
			return err
		}
	}
	for _, event := range graph.SyncEvents {
		if event.RepoID == "" {
			event.RepoID = repoID
		}
		if event.SourceID == "" {
			event.SourceID = graph.Record.ID
		}
		if err = recordSyncEventTx(ctx, tx, event); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func requireRepoTx(ctx context.Context, tx *sql.Tx, repoID string) error {
	var id string
	if err := tx.QueryRowContext(ctx, `SELECT repo_id FROM repos WHERE repo_id = ?`, repoID).Scan(&id); err != nil {
		if err == sql.ErrNoRows {
			return notFoundErr("repository", repoID)
		}
		return err
	}
	return nil
}

func sourceFromRecord(record Record) Source {
	return Source{RepoID: record.RepoID, ID: record.ID, Kind: record.Type, Path: record.Path, Title: record.Title, Body: record.Body, Status: record.Status, Labels: record.Labels, ContentHash: record.ContentHash, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt}
}

func upsertRecordTx(ctx context.Context, tx *sql.Tx, record Record) error {
	labels, err := marshalJSON(record.Labels)
	if err != nil {
		return err
	}
	createdAt := record.CreatedAt
	updatedAt := record.UpdatedAt
	if createdAt.IsZero() {
		createdAt = time.Unix(0, 0).UTC()
	}
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	if record.Provenance == "" {
		record.Provenance = ProvenanceRemote
	}
	return execTx(ctx, tx, `INSERT INTO records (repo_id, record_id, record_type, path, title, body, status, labels, content_hash, provenance, remote_type, remote_id, remote_revision, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(repo_id, record_id) DO UPDATE SET record_type = excluded.record_type, path = excluded.path, title = excluded.title, body = excluded.body, status = excluded.status, labels = excluded.labels, content_hash = excluded.content_hash, provenance = CASE WHEN records.provenance = 'remote' AND excluded.provenance <> 'remote' THEN records.provenance ELSE excluded.provenance END, remote_type = CASE WHEN records.provenance = 'remote' AND excluded.provenance <> 'remote' THEN records.remote_type ELSE excluded.remote_type END, remote_id = CASE WHEN records.provenance = 'remote' AND excluded.provenance <> 'remote' THEN records.remote_id ELSE excluded.remote_id END, remote_revision = excluded.remote_revision, updated_at = excluded.updated_at`,
		record.RepoID, record.ID, record.Type, record.Path, record.Title, record.Body, record.Status, labels, record.ContentHash, string(record.Provenance), record.RemoteType, record.RemoteID, record.RemoteRevision, createdAt.Format(time.RFC3339Nano), updatedAt.Format(time.RFC3339Nano))
}

func upsertRecordCommentTx(ctx context.Context, tx *sql.Tx, comment RecordComment) error {
	createdAt := comment.CreatedAt
	updatedAt := comment.UpdatedAt
	if createdAt.IsZero() {
		createdAt = time.Unix(0, 0).UTC()
	}
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	return execTx(ctx, tx, `INSERT INTO record_comments (repo_id, record_id, comment_id, author, body, content_hash, remote_revision, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(repo_id, record_id, comment_id) DO UPDATE SET author = excluded.author, body = excluded.body, content_hash = excluded.content_hash, remote_revision = excluded.remote_revision, updated_at = excluded.updated_at`,
		comment.RepoID, comment.RecordID, comment.CommentID, comment.Author, comment.Body, comment.ContentHash, comment.RemoteRevision, createdAt.Format(time.RFC3339Nano), updatedAt.Format(time.RFC3339Nano))
}

func upsertRemoteRevisionTx(ctx context.Context, tx *sql.Tx, revision RemoteRevision) error {
	if revision.LastFetchedAt.IsZero() {
		revision.LastFetchedAt = time.Unix(0, 0).UTC()
	}
	return execTx(ctx, tx, `INSERT INTO remote_revisions (repo_id, source_id, remote_type, remote_id, remote_revision, status, last_fetched_at) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(repo_id, source_id) DO UPDATE SET remote_type = excluded.remote_type, remote_id = excluded.remote_id, remote_revision = excluded.remote_revision, status = excluded.status, last_fetched_at = excluded.last_fetched_at`, revision.RepoID, revision.RecordID, revision.RemoteType, revision.RemoteID, revision.RemoteRevision, revision.Status, revision.LastFetchedAt.Format(time.RFC3339Nano))
}

func insertAuditTrailTx(ctx context.Context, tx *sql.Tx, entry AuditTrailEntry) error {
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Unix(0, 0).UTC()
	}
	return execTx(ctx, tx, `INSERT INTO audit_trail (repo_id, id, operation, record_id, remote_type, remote_id, idempotency_key, status, message, payload_hash, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(repo_id, id) DO UPDATE SET status = excluded.status, message = excluded.message`, entry.RepoID, entry.ID, entry.Operation, entry.RecordID, entry.RemoteType, entry.RemoteID, entry.IdempotencyKey, entry.Status, entry.Message, entry.PayloadHash, entry.CreatedAt.Format(time.RFC3339Nano))
}

func (s *SQLiteStore) GetRecord(ctx context.Context, repoID, recordID string) (Record, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, record_id, record_type, path, title, body, status, labels, content_hash, provenance, remote_type, remote_id, remote_revision, created_at, updated_at FROM records WHERE repo_id = ? AND record_id = ?`, repoID, recordID)
	if err != nil {
		return Record{}, err
	}
	defer rows.Close()
	records, err := scanRecords(rows)
	if err != nil {
		return Record{}, err
	}
	if len(records) == 0 {
		return Record{}, notFoundErr("record", recordID)
	}
	records[0].Aliases, err = s.GetIdentityMapScoped(ctx, repoID, recordID)
	if err != nil {
		return Record{}, err
	}
	records[0].Comments, err = s.recordComments(ctx, repoID, recordID)
	if err != nil {
		return Record{}, err
	}
	return records[0], nil
}

func (s *SQLiteStore) ListRecords(ctx context.Context, filter RecordFilter) ([]Record, error) {
	query := `SELECT repo_id, record_id, record_type, path, title, body, status, labels, content_hash, provenance, remote_type, remote_id, remote_revision, created_at, updated_at FROM records WHERE (? = '' OR repo_id = ?) AND (? = '' OR record_type = ?) AND (? = '' OR status = ?) ORDER BY repo_id, record_id`
	args := []any{filter.RepoID, filter.RepoID, filter.Type, filter.Type, filter.Status, filter.Status}
	if filter.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecords(rows)
}

func (s *SQLiteStore) SearchRecords(ctx context.Context, query SearchQuery) ([]SearchResult, error) {
	like := "%" + query.Query + "%"
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, record_id, path, title, body FROM records WHERE (? = '' OR repo_id = ?) AND (? = '' OR record_type = ?) AND (title LIKE ? OR body LIKE ? OR path LIKE ?) ORDER BY repo_id, record_id`, query.RepoID, query.RepoID, query.Kind, query.Kind, like, like, like)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results := []SearchResult{}
	for rows.Next() {
		var result SearchResult
		var body string
		if err := rows.Scan(&result.RepoID, &result.ID, &result.Path, &result.Title, &body); err != nil {
			return nil, err
		}
		needle := normalizeSearchQuery(query.Query)
		result.Snippet = snippet(result.Title+"\n"+body, needle)
		result.Score = searchScore(result.Title, body, needle)
		results = append(results, result)
		if query.Limit > 0 && len(results) >= query.Limit {
			break
		}
	}
	return results, rows.Err()
}

func scanRecords(rows *sql.Rows) ([]Record, error) {
	records := []Record{}
	for rows.Next() {
		var record Record
		var labelsRaw, provenance, createdRaw, updatedRaw string
		if err := rows.Scan(&record.RepoID, &record.ID, &record.Type, &record.Path, &record.Title, &record.Body, &record.Status, &labelsRaw, &record.ContentHash, &provenance, &record.RemoteType, &record.RemoteID, &record.RemoteRevision, &createdRaw, &updatedRaw); err != nil {
			return nil, err
		}
		labels, err := unmarshalJSON[[]string](labelsRaw)
		if err != nil {
			return nil, err
		}
		record.Labels = labels
		record.Provenance = Provenance(provenance)
		record.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdRaw)
		record.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedRaw)
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *SQLiteStore) recordComments(ctx context.Context, repoID, recordID string) ([]RecordComment, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, record_id, comment_id, author, body, content_hash, remote_revision, created_at, updated_at FROM record_comments WHERE repo_id = ? AND record_id = ? ORDER BY comment_id`, repoID, recordID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	comments := []RecordComment{}
	for rows.Next() {
		var comment RecordComment
		var createdRaw, updatedRaw string
		if err := rows.Scan(&comment.RepoID, &comment.RecordID, &comment.CommentID, &comment.Author, &comment.Body, &comment.ContentHash, &comment.RemoteRevision, &createdRaw, &updatedRaw); err != nil {
			return nil, err
		}
		comment.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdRaw)
		comment.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedRaw)
		comments = append(comments, comment)
	}
	return comments, rows.Err()
}

func (s *SQLiteStore) RecordCounts(ctx context.Context, repoID string) (RecordCounts, error) {
	counts := RecordCounts{RepoID: repoID}
	queries := []struct {
		value *int
		query string
	}{
		{&counts.Records, `SELECT count(*) FROM records WHERE repo_id = ?`},
		{&counts.Comments, `SELECT count(*) FROM record_comments WHERE repo_id = ?`},
		{&counts.IdentityAliases, `SELECT count(*) FROM identity_map WHERE repo_id = ?`},
		{&counts.SyncEvents, `SELECT count(*) FROM sync_events WHERE repo_id = ?`},
		{&counts.AuditRows, `SELECT count(*) FROM audit_trail WHERE repo_id = ?`},
		{&counts.Snapshots, `SELECT count(*) FROM snapshots WHERE repo_id = ?`},
		{&counts.SnapshotChunks, `SELECT count(*) FROM snapshot_chunks WHERE repo_id = ?`},
		{&counts.Chunks, `SELECT count(*) FROM chunks WHERE repo_id = ?`},
		{&counts.RemoteRevisions, `SELECT count(*) FROM remote_revisions WHERE repo_id = ?`},
	}
	for _, q := range queries {
		if err := s.db.QueryRowContext(ctx, q.query, repoID).Scan(q.value); err != nil {
			return RecordCounts{}, err
		}
	}
	return counts, nil
}

func (s *SQLiteStore) WALCapable(ctx context.Context) (bool, string, error) {
	var mode string
	if err := s.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
		return false, "", err
	}
	return mode == "wal" || mode == "memory", mode, nil
}

func (s *SQLiteStore) UpsertSnapshot(ctx context.Context, snapshot Snapshot) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txRollbackOnError(tx, &err)
	if err = upsertSnapshotTx(ctx, tx, snapshot); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertSnapshotTx(ctx context.Context, tx *sql.Tx, snapshot Snapshot) error {
	if snapshot.CreatedAt.IsZero() {
		snapshot.CreatedAt = time.Unix(0, 0).UTC()
	}
	metadata, err := marshalJSON(snapshot.Metadata)
	if err != nil {
		return err
	}
	if err = execTx(ctx, tx, `INSERT INTO snapshots (repo_id, snapshot_id, format, content_hash, record_count, created_at, metadata) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(repo_id, snapshot_id) DO UPDATE SET format = excluded.format, content_hash = excluded.content_hash, record_count = excluded.record_count, metadata = excluded.metadata`, snapshot.RepoID, snapshot.ID, snapshot.Format, snapshot.ContentHash, snapshot.RecordCount, snapshot.CreatedAt.Format(time.RFC3339Nano), metadata); err != nil {
		return err
	}
	for _, chunk := range snapshot.Chunks {
		if chunk.RepoID == "" {
			chunk.RepoID = snapshot.RepoID
		}
		if chunk.SnapshotID == "" {
			chunk.SnapshotID = snapshot.ID
		}
		if err = execTx(ctx, tx, `INSERT INTO snapshot_chunks (repo_id, snapshot_id, chunk_id, record_id, byte_start, byte_end, line_start, line_end, citation, content_hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(repo_id, snapshot_id, chunk_id) DO UPDATE SET record_id = excluded.record_id, byte_start = excluded.byte_start, byte_end = excluded.byte_end, line_start = excluded.line_start, line_end = excluded.line_end, citation = excluded.citation, content_hash = excluded.content_hash`, chunk.RepoID, chunk.SnapshotID, chunk.ChunkID, chunk.RecordID, chunk.ByteStart, chunk.ByteEnd, chunk.LineStart, chunk.LineEnd, chunk.Citation, chunk.ContentHash); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLiteStore) ListSnapshotChunks(ctx context.Context, repoID, snapshotID string) ([]SnapshotChunk, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, snapshot_id, chunk_id, record_id, byte_start, byte_end, line_start, line_end, citation, content_hash FROM snapshot_chunks WHERE repo_id = ? AND snapshot_id = ? ORDER BY chunk_id`, repoID, snapshotID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	chunks := []SnapshotChunk{}
	for rows.Next() {
		var chunk SnapshotChunk
		if err := rows.Scan(&chunk.RepoID, &chunk.SnapshotID, &chunk.ChunkID, &chunk.RecordID, &chunk.ByteStart, &chunk.ByteEnd, &chunk.LineStart, &chunk.LineEnd, &chunk.Citation, &chunk.ContentHash); err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}
	return chunks, rows.Err()
}
