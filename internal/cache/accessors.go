package cache

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

func (s *SQLiteStore) AddRepository(ctx context.Context, repo RepositoryBinding) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txRollbackOnError(tx, &err)
	createdAt := repo.CreatedAt
	updatedAt := repo.UpdatedAt
	if createdAt.IsZero() {
		createdAt = time.Unix(0, 0).UTC()
	}
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	scopes, err := marshalJSON(repo.Scopes)
	if err != nil {
		return err
	}
	if err = execTx(ctx, tx, `INSERT INTO repos (repo_id, owner, name, api_base_url, scopes, display_name, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, repo.RepoID, repo.Owner, repo.Name, repo.APIBaseURL, scopes, repo.DisplayName, createdAt.Format(time.RFC3339Nano), updatedAt.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	for _, alias := range repo.Aliases {
		if err = execTx(ctx, tx, `INSERT INTO repo_aliases (alias, repo_id, created_at) VALUES (?, ?, ?)`, alias, repo.RepoID, createdAt.Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLiteStore) UpsertRepo(ctx context.Context, repo RepositoryBinding) error {
	return s.AddRepository(ctx, repo)
}

func (s *SQLiteStore) GetRepo(ctx context.Context, repoID string) (RepositoryBinding, error) {
	return s.GetRepository(ctx, repoID)
}

func (s *SQLiteStore) GetRepository(ctx context.Context, repoID string) (RepositoryBinding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, owner, name, api_base_url, scopes, display_name, created_at, updated_at FROM repos WHERE repo_id = ?`, repoID)
	if err != nil {
		return RepositoryBinding{}, err
	}
	defer rows.Close()
	repos, err := scanRepositories(rows)
	if err != nil {
		return RepositoryBinding{}, err
	}
	if len(repos) == 0 {
		return RepositoryBinding{}, notFoundErr("repository", repoID)
	}
	aliases, err := s.repositoryAliases(ctx, repoID)
	if err != nil {
		return RepositoryBinding{}, err
	}
	repos[0].Aliases = aliases
	return repos[0], nil
}

func (s *SQLiteStore) ListRepositories(ctx context.Context) ([]RepositoryBinding, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, owner, name, api_base_url, scopes, display_name, created_at, updated_at FROM repos ORDER BY repo_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	repos, err := scanRepositories(rows)
	if err != nil {
		return nil, err
	}
	for i := range repos {
		repos[i].Aliases, err = s.repositoryAliases(ctx, repos[i].RepoID)
		if err != nil {
			return nil, err
		}
	}
	return repos, nil
}

func scanRepositories(rows *sql.Rows) ([]RepositoryBinding, error) {
	var repos []RepositoryBinding
	for rows.Next() {
		var repo RepositoryBinding
		var scopesRaw, createdRaw, updatedRaw string
		if err := rows.Scan(&repo.RepoID, &repo.Owner, &repo.Name, &repo.APIBaseURL, &scopesRaw, &repo.DisplayName, &createdRaw, &updatedRaw); err != nil {
			return nil, err
		}
		scopes, err := unmarshalJSON[[]RepositoryScope](scopesRaw)
		if err != nil {
			return nil, err
		}
		repo.Scopes = scopes
		repo.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdRaw)
		repo.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedRaw)
		repos = append(repos, repo)
	}
	return repos, rows.Err()
}

func (s *SQLiteStore) repositoryAliases(ctx context.Context, repoID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT alias FROM repo_aliases WHERE repo_id = ? ORDER BY alias`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var aliases []string
	for rows.Next() {
		var alias string
		if err := rows.Scan(&alias); err != nil {
			return nil, err
		}
		aliases = append(aliases, alias)
	}
	return aliases, rows.Err()
}

func IsConstraintError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "constraint")
}

func (s *SQLiteStore) repoIDForSource(ctx context.Context, sourceID string) (string, error) {
	row := s.db.QueryRowContext(ctx, `SELECT repo_id FROM sources WHERE id = ? ORDER BY repo_id LIMIT 1`, sourceID)
	var repoID string
	if err := row.Scan(&repoID); err != nil {
		if err == sql.ErrNoRows {
			return "", notFoundErr("source", sourceID)
		}
		return "", err
	}
	return repoID, nil
}

func (s *SQLiteStore) UpsertIdentity(ctx context.Context, identity Identity) (err error) {
	if identity.RepoID == "" && identity.SourceID != "" {
		identity.RepoID, err = s.repoIDForSource(ctx, identity.SourceID)
		if err != nil {
			return err
		}
	}
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
	return execTx(ctx, tx, `INSERT INTO identity_map (repo_id, source_id, alias_type, alias, remote_type, remote_id)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(repo_id, alias_type, alias) DO UPDATE SET source_id = excluded.source_id, remote_type = excluded.remote_type, remote_id = excluded.remote_id`,
		identity.RepoID, identity.SourceID, identity.AliasType, identity.Alias, identity.Remote.Type, identity.Remote.ID)
}

func (s *SQLiteStore) GetIdentityMap(ctx context.Context, sourceID string) ([]Identity, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, source_id, alias_type, alias, remote_type, remote_id FROM identity_map WHERE source_id = ? ORDER BY repo_id, alias_type, alias`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIdentities(rows)
}

func (s *SQLiteStore) GetIdentityMapScoped(ctx context.Context, repoID, sourceID string) ([]Identity, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, source_id, alias_type, alias, remote_type, remote_id FROM identity_map WHERE repo_id = ? AND source_id = ? ORDER BY alias_type, alias`, repoID, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIdentities(rows)
}

func (s *SQLiteStore) ResolveAlias(ctx context.Context, alias RemoteAlias) (Identity, error) {
	identities, err := s.DiagnoseAlias(ctx, alias)
	if err != nil {
		return Identity{}, err
	}
	if len(identities) == 0 {
		return Identity{}, notFoundErr("alias", alias.Type+":"+alias.ID)
	}
	repos := map[string]struct{}{}
	for _, identity := range identities {
		repos[identity.RepoID] = struct{}{}
	}
	if len(repos) > 1 {
		repoIDs := make([]string, 0, len(repos))
		for repoID := range repos {
			repoIDs = append(repoIDs, repoID)
		}
		return Identity{}, ErrAliasConflict{Alias: alias.Type + ":" + alias.ID, Repos: repoIDs}
	}
	return Identity{}, ErrUnscopedAliasResolution{Alias: alias.Type + ":" + alias.ID}
}

func (s *SQLiteStore) ResolveAliasScoped(ctx context.Context, repoID string, alias RemoteAlias) (Identity, error) {
	row := s.db.QueryRowContext(ctx, `SELECT repo_id, source_id, alias_type, alias, remote_type, remote_id FROM identity_map WHERE repo_id = ? AND ((alias_type = ? AND alias = ?) OR (remote_type = ? AND remote_id = ?)) ORDER BY source_id LIMIT 1`, repoID, alias.Type, alias.ID, alias.Type, alias.ID)
	var identity Identity
	if err := row.Scan(&identity.RepoID, &identity.SourceID, &identity.AliasType, &identity.Alias, &identity.Remote.Type, &identity.Remote.ID); err != nil {
		if err == sql.ErrNoRows {
			return Identity{}, notFoundErr("alias", alias.Type+":"+alias.ID)
		}
		return Identity{}, err
	}
	return identity, nil
}

func (s *SQLiteStore) ResolveRepoAlias(ctx context.Context, repoID string, alias RemoteAlias) (Identity, error) {
	return s.ResolveAliasScoped(ctx, repoID, alias)
}

func (s *SQLiteStore) DiagnoseAlias(ctx context.Context, alias RemoteAlias) ([]Identity, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, source_id, alias_type, alias, remote_type, remote_id FROM identity_map WHERE (alias_type = ? AND alias = ?) OR (remote_type = ? AND remote_id = ?) ORDER BY repo_id, source_id`, alias.Type, alias.ID, alias.Type, alias.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanIdentities(rows)
}

func scanIdentities(rows *sql.Rows) ([]Identity, error) {
	var identities []Identity
	for rows.Next() {
		var identity Identity
		if err := rows.Scan(&identity.RepoID, &identity.SourceID, &identity.AliasType, &identity.Alias, &identity.Remote.Type, &identity.Remote.ID); err != nil {
			return nil, err
		}
		identities = append(identities, identity)
	}
	return identities, rows.Err()
}

func (s *SQLiteStore) UpsertLink(ctx context.Context, link Link) (err error) {
	if link.RepoID == "" && link.SourceID != "" {
		link.RepoID, err = s.repoIDForSource(ctx, link.SourceID)
		if err != nil {
			return err
		}
	}
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
	return execTx(ctx, tx, `INSERT INTO links (repo_id, source_id, target_id, kind, text) VALUES (?, ?, ?, ?, ?) ON CONFLICT(repo_id, source_id, target_id, kind, text) DO NOTHING`, link.RepoID, link.SourceID, link.TargetID, link.Kind, link.Text)
}

func (s *SQLiteStore) ListLinks(ctx context.Context, filter LinkFilter) ([]Link, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, source_id, target_id, kind, text FROM links WHERE (? = '' OR repo_id = ?) AND (? = '' OR source_id = ?) AND (? = '' OR target_id = ?) ORDER BY repo_id, source_id, target_id, kind, text`, filter.RepoID, filter.RepoID, filter.SourceID, filter.SourceID, filter.TargetID, filter.TargetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var links []Link
	for rows.Next() {
		var link Link
		if err := rows.Scan(&link.RepoID, &link.SourceID, &link.TargetID, &link.Kind, &link.Text); err != nil {
			return nil, err
		}
		links = append(links, link)
	}
	return links, rows.Err()
}

func (s *SQLiteStore) GetBacklinks(ctx context.Context, targetID string) ([]Source, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.repo_id, s.id, s.kind, s.path, s.title, s.body, s.status, s.labels, s.content_hash, s.created_at, s.updated_at FROM sources s JOIN links l ON l.repo_id = s.repo_id AND l.source_id = s.id WHERE l.target_id = ? ORDER BY s.repo_id, s.id`, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sources, err := scanSources(rows)
	if err != nil {
		return nil, err
	}
	for i := range sources {
		aliases, err := s.GetIdentityMapScoped(ctx, sources[i].RepoID, sources[i].ID)
		if err != nil {
			return nil, err
		}
		sources[i].Aliases = aliases
	}
	return sources, nil
}

func (s *SQLiteStore) GetBacklinksScoped(ctx context.Context, repoID, targetID string) ([]Source, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT s.repo_id, s.id, s.kind, s.path, s.title, s.body, s.status, s.labels, s.content_hash, s.created_at, s.updated_at FROM sources s JOIN links l ON l.repo_id = s.repo_id AND l.source_id = s.id WHERE l.repo_id = ? AND l.target_id = ? ORDER BY s.id`, repoID, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sources, err := scanSources(rows)
	if err != nil {
		return nil, err
	}
	for i := range sources {
		aliases, err := s.GetIdentityMapScoped(ctx, repoID, sources[i].ID)
		if err != nil {
			return nil, err
		}
		sources[i].Aliases = aliases
	}
	return sources, nil
}

func (s *SQLiteStore) UpsertChunk(ctx context.Context, chunk Chunk) (Chunk, error) {
	if chunk.RepoID == "" && chunk.SourceID != "" {
		repoID, err := s.repoIDForSource(ctx, chunk.SourceID)
		if err != nil {
			return Chunk{}, err
		}
		chunk.RepoID = repoID
	}
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
	if chunk.RecordID == "" {
		chunk.RecordID = chunk.SourceID
	}
	if chunk.Policy == "" {
		chunk.Policy = "heading"
	}
	err = execTx(ctx, tx, `INSERT INTO chunks (repo_id, id, source_id, record_id, snapshot_id, content_hash, byte_start, byte_end, line_start, line_end, heading_path, text, normalized_text, inherited_metadata, outbound_links, resolved_aliases, embedding, policy)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(repo_id, id) DO UPDATE SET record_id = excluded.record_id, snapshot_id = excluded.snapshot_id, byte_end = excluded.byte_end, line_start = excluded.line_start, line_end = excluded.line_end, heading_path = excluded.heading_path, text = excluded.text, normalized_text = excluded.normalized_text, inherited_metadata = excluded.inherited_metadata, outbound_links = excluded.outbound_links, resolved_aliases = excluded.resolved_aliases, embedding = excluded.embedding, policy = excluded.policy`,
		chunk.RepoID, chunk.ID, chunk.SourceID, chunk.RecordID, chunk.SnapshotID, chunk.ContentHash, chunk.ByteStart, chunk.ByteEnd, chunk.LineStart, chunk.LineEnd, headingPath, chunk.Text, chunk.NormalizedText, metadata, outboundLinks, resolvedAliases, chunk.Embedding, chunk.Policy)
	return chunk, err
}

func (s *SQLiteStore) GetChunks(ctx context.Context, sourceID string) ([]Chunk, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, id, source_id, record_id, snapshot_id, content_hash, byte_start, byte_end, line_start, line_end, heading_path, text, normalized_text, inherited_metadata, outbound_links, resolved_aliases, embedding, policy FROM chunks WHERE source_id = ? ORDER BY repo_id, source_id, policy, byte_start, id`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChunks(rows)
}

func (s *SQLiteStore) GetChunksScoped(ctx context.Context, repoID, sourceID string) ([]Chunk, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, id, source_id, record_id, snapshot_id, content_hash, byte_start, byte_end, line_start, line_end, heading_path, text, normalized_text, inherited_metadata, outbound_links, resolved_aliases, embedding, policy FROM chunks WHERE repo_id = ? AND source_id = ? ORDER BY source_id, policy, byte_start, id`, repoID, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChunks(rows)
}

func (s *SQLiteStore) ListChunks(ctx context.Context, filter ChunkFilter) ([]Chunk, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, id, source_id, record_id, snapshot_id, content_hash, byte_start, byte_end, line_start, line_end, heading_path, text, normalized_text, inherited_metadata, outbound_links, resolved_aliases, embedding, policy FROM chunks WHERE (? = '' OR repo_id = ?) AND (? = '' OR source_id = ?) AND (? = '' OR record_id = ?) AND (? = '' OR snapshot_id = ?) AND (? = '' OR policy = ?) ORDER BY repo_id, source_id, policy, byte_start, id`, filter.RepoID, filter.RepoID, filter.SourceID, filter.SourceID, filter.RecordID, filter.RecordID, filter.SnapshotID, filter.SnapshotID, filter.Policy, filter.Policy)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChunks(rows)
}

func scanChunks(rows *sql.Rows) ([]Chunk, error) {
	var chunks []Chunk
	for rows.Next() {
		var err error
		var chunk Chunk
		var headingPath, metadata, outboundLinks, resolvedAliases string
		if err := rows.Scan(&chunk.RepoID, &chunk.ID, &chunk.SourceID, &chunk.RecordID, &chunk.SnapshotID, &chunk.ContentHash, &chunk.ByteStart, &chunk.ByteEnd, &chunk.LineStart, &chunk.LineEnd, &headingPath, &chunk.Text, &chunk.NormalizedText, &metadata, &outboundLinks, &resolvedAliases, &chunk.Embedding, &chunk.Policy); err != nil {
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
	if event.RepoID == "" && event.SourceID != "" {
		event.RepoID, err = s.repoIDForSource(ctx, event.SourceID)
		if err != nil {
			return err
		}
	}
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
	startedRaw := formatTimeOrEmpty(event.StartedAt)
	completedRaw := formatTimeOrEmpty(event.CompletedAt)
	return execTx(ctx, tx, `INSERT INTO sync_events (repo_id, id, source_id, remote_type, remote_id, remote_revision, status, idempotency_key, message, created_at, started_at, completed_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(repo_id, id) DO UPDATE SET status = excluded.status, message = excluded.message, started_at = excluded.started_at, completed_at = excluded.completed_at`, event.RepoID, event.ID, event.SourceID, event.RemoteType, event.RemoteID, event.RemoteRevision, event.Status, event.IdempotencyKey, event.Message, event.CreatedAt.Format(time.RFC3339Nano), startedRaw, completedRaw)
}

func (s *SQLiteStore) GetSyncEventByKey(ctx context.Context, key string) (*SyncEvent, error) {
	row := s.db.QueryRowContext(ctx, `SELECT repo_id, id, source_id, remote_type, remote_id, remote_revision, status, idempotency_key, message, created_at, started_at, completed_at FROM sync_events WHERE idempotency_key = ? ORDER BY created_at DESC LIMIT 1`, key)
	var event SyncEvent
	var createdRaw, startedRaw, completedRaw string
	if err := row.Scan(&event.RepoID, &event.ID, &event.SourceID, &event.RemoteType, &event.RemoteID, &event.RemoteRevision, &event.Status, &event.IdempotencyKey, &event.Message, &createdRaw, &startedRaw, &completedRaw); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	event.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdRaw)
	event.StartedAt = parseTimeOrZero(startedRaw)
	event.CompletedAt = parseTimeOrZero(completedRaw)
	return &event, nil
}

func (s *SQLiteStore) ListCompletedSyncEventsScoped(ctx context.Context, repoID string) ([]SyncEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, id, source_id, remote_type, remote_id, remote_revision, status, idempotency_key, message, created_at, started_at, completed_at FROM sync_events WHERE repo_id = ? AND completed_at != '' ORDER BY completed_at ASC`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []SyncEvent
	for rows.Next() {
		var event SyncEvent
		var createdRaw, startedRaw, completedRaw string
		if err := rows.Scan(&event.RepoID, &event.ID, &event.SourceID, &event.RemoteType, &event.RemoteID, &event.RemoteRevision, &event.Status, &event.IdempotencyKey, &event.Message, &createdRaw, &startedRaw, &completedRaw); err != nil {
			return nil, err
		}
		event.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdRaw)
		event.StartedAt = parseTimeOrZero(startedRaw)
		event.CompletedAt = parseTimeOrZero(completedRaw)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *SQLiteStore) GetSyncStatus(ctx context.Context, sourceID string) (SyncStatus, error) {
	row := s.db.QueryRowContext(ctx, `SELECT repo_id, source_id, remote_type, remote_id, remote_revision, status, last_fetched_at FROM remote_revisions WHERE source_id = ? ORDER BY repo_id LIMIT 1`, sourceID)
	return scanSyncStatus(row, sourceID)
}

func (s *SQLiteStore) GetSyncStatusScoped(ctx context.Context, repoID, sourceID string) (SyncStatus, error) {
	row := s.db.QueryRowContext(ctx, `SELECT repo_id, source_id, remote_type, remote_id, remote_revision, status, last_fetched_at FROM remote_revisions WHERE repo_id = ? AND source_id = ?`, repoID, sourceID)
	return scanSyncStatus(row, sourceID)
}

func scanSyncStatus(row *sql.Row, sourceID string) (SyncStatus, error) {
	var status SyncStatus
	var lastFetched string
	if err := row.Scan(&status.RepoID, &status.SourceID, &status.RemoteType, &status.RemoteID, &status.RemoteRevision, &status.Status, &lastFetched); err != nil {
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
	return execTx(ctx, tx, `INSERT INTO remote_revisions (repo_id, source_id, remote_type, remote_id, remote_revision, status, last_fetched_at) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(repo_id, source_id) DO UPDATE SET remote_type = excluded.remote_type, remote_id = excluded.remote_id, remote_revision = excluded.remote_revision, status = excluded.status, last_fetched_at = excluded.last_fetched_at`, status.RepoID, status.SourceID, status.RemoteType, status.RemoteID, status.RemoteRevision, status.Status, status.LastFetchedAt.Format(time.RFC3339Nano))
}

func (s *SQLiteStore) UpsertConflict(ctx context.Context, conflict Conflict) (err error) {
	if conflict.RepoID == "" && conflict.SourceID != "" {
		conflict.RepoID, err = s.repoIDForSource(ctx, conflict.SourceID)
		if err != nil {
			return err
		}
	}
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
	return execTx(ctx, tx, `INSERT INTO conflicts (repo_id, id, source_id, kind, local_payload, remote_payload, created_at) VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT(repo_id, id) DO UPDATE SET kind = excluded.kind, local_payload = excluded.local_payload, remote_payload = excluded.remote_payload`, conflict.RepoID, conflict.ID, conflict.SourceID, conflict.Kind, conflict.LocalPayload, conflict.RemotePayload, conflict.CreatedAt.Format(time.RFC3339Nano))
}

func (s *SQLiteStore) GetConflicts(ctx context.Context, sourceID string) ([]Conflict, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, id, source_id, kind, local_payload, remote_payload, created_at FROM conflicts WHERE source_id = ? ORDER BY repo_id, id`, sourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var conflicts []Conflict
	for rows.Next() {
		var conflict Conflict
		var created string
		if err := rows.Scan(&conflict.RepoID, &conflict.ID, &conflict.SourceID, &conflict.Kind, &conflict.LocalPayload, &conflict.RemotePayload, &created); err != nil {
			return nil, err
		}
		conflict.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		conflicts = append(conflicts, conflict)
	}
	return conflicts, rows.Err()
}

func formatTimeOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

func parseTimeOrZero(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, raw)
	return t
}
