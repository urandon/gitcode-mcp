package cache

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

func (s *SQLiteStore) CacheIdentity(ctx context.Context) (CacheIdentity, error) {
	var identity CacheIdentity
	var createdRaw string
	err := s.db.QueryRowContext(ctx, `SELECT cache_uuid, created_at FROM cache_identity WHERE identity_key = 1`).Scan(&identity.UUID, &createdRaw)
	if err != nil {
		return CacheIdentity{}, err
	}
	identity.CreatedAt = parseTimeOrZero(createdRaw)
	return identity, nil
}

func (s *SQLiteStore) GetRepoContentState(ctx context.Context, repoID string) (RepoContentState, error) {
	var state RepoContentState
	var changedRaw string
	err := s.db.QueryRowContext(ctx, `SELECT repo_id, content_generation, content_changed_at, last_projection_id FROM repo_content_state WHERE repo_id = ?`, repoID).
		Scan(&state.RepoID, &state.ContentGeneration, &changedRaw, &state.LastProjectionID)
	if err == sql.ErrNoRows {
		return RepoContentState{RepoID: repoID}, nil
	}
	if err != nil {
		return RepoContentState{}, err
	}
	state.ContentChangedAt = parseTimeOrZero(changedRaw)
	return state, nil
}

func (s *SQLiteStore) GetRAGCoverageState(ctx context.Context, repoID, namespaceID string) (RAGCoverageState, bool, error) {
	var state RAGCoverageState
	var updatedRaw string
	err := s.db.QueryRowContext(ctx, `SELECT repo_id, namespace_id, covered_generation, status, updated_at FROM rag_coverage_state WHERE repo_id = ? AND namespace_id = ?`, repoID, namespaceID).
		Scan(&state.RepoID, &state.NamespaceID, &state.CoveredGeneration, &state.Status, &updatedRaw)
	if err == sql.ErrNoRows {
		return RAGCoverageState{}, false, nil
	}
	if err != nil {
		return RAGCoverageState{}, false, err
	}
	state.UpdatedAt = parseTimeOrZero(updatedRaw)
	return state, true, nil
}

func (s *SQLiteStore) UpsertRAGCoverageState(ctx context.Context, state RAGCoverageState) error {
	if state.RepoID == "" || state.NamespaceID == "" || state.Status == "" {
		return fmt.Errorf("cache: rag coverage state requires repo id, namespace id, and status")
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO rag_coverage_state (repo_id, namespace_id, covered_generation, status, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(repo_id, namespace_id) DO UPDATE SET covered_generation = excluded.covered_generation, status = excluded.status, updated_at = excluded.updated_at`,
		state.RepoID, state.NamespaceID, state.CoveredGeneration, state.Status, state.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

func (s *SQLiteStore) UpsertMaintenanceFrontier(ctx context.Context, frontier MaintenanceFrontier) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := upsertMaintenanceFrontierTx(ctx, tx, frontier); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func upsertMaintenanceFrontierTx(ctx context.Context, tx *sql.Tx, frontier MaintenanceFrontier) error {
	if frontier.RepoID == "" || frontier.RemoteType == "" || frontier.Ordering == "" || frontier.FilterKey == "" || frontier.Lane == "" || frontier.Status == "" {
		return fmt.Errorf("cache: maintenance frontier identity and status are required")
	}
	if frontier.UpdatedAt.IsZero() {
		frontier.UpdatedAt = time.Now().UTC()
	}
	if frontier.LastSuccessAt.IsZero() && frontier.LastErrorClass == "" && frontier.Status != "degraded" {
		frontier.LastSuccessAt = frontier.UpdatedAt
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO maintenance_frontiers (repo_id, remote_type, ordering, filter_key, lane, status, high_updated_at, high_remote_id, high_number, stop_reason, pages_listed, records_listed, checkpoint, last_error_class, last_success_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(repo_id, remote_type, ordering, filter_key, lane) DO UPDATE SET status = excluded.status, high_updated_at = excluded.high_updated_at, high_remote_id = excluded.high_remote_id, high_number = excluded.high_number, stop_reason = excluded.stop_reason, pages_listed = excluded.pages_listed, records_listed = excluded.records_listed, checkpoint = excluded.checkpoint, last_error_class = excluded.last_error_class, last_success_at = CASE WHEN excluded.last_success_at <> '' THEN excluded.last_success_at ELSE maintenance_frontiers.last_success_at END, updated_at = excluded.updated_at`,
		frontier.RepoID, frontier.RemoteType, frontier.Ordering, frontier.FilterKey, frontier.Lane, frontier.Status, formatTimeOrEmpty(frontier.HighUpdatedAt), frontier.HighRemoteID, frontier.HighNumber, frontier.StopReason, frontier.PagesListed, frontier.RecordsListed, frontier.Checkpoint, frontier.LastErrorClass, formatTimeOrEmpty(frontier.LastSuccessAt), frontier.UpdatedAt.Format(time.RFC3339Nano))
	return err
}

func upsertSyncCommitReceiptTx(ctx context.Context, tx *sql.Tx, receipt SyncCommitReceipt) error {
	if receipt.StageID == "" || receipt.Checksum == "" || receipt.RepoID == "" || receipt.Collection == "" {
		return fmt.Errorf("cache: sync commit receipt identity is required")
	}
	if receipt.CommittedAt.IsZero() {
		receipt.CommittedAt = time.Now().UTC()
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO sync_commit_receipts (stage_id, checksum, repo_id, collection, committed_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(stage_id) DO UPDATE SET checksum = excluded.checksum, repo_id = excluded.repo_id, collection = excluded.collection, committed_at = excluded.committed_at`,
		receipt.StageID, receipt.Checksum, receipt.RepoID, receipt.Collection, receipt.CommittedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *SQLiteStore) GetSyncCommitReceipt(ctx context.Context, stageID string) (SyncCommitReceipt, bool, error) {
	var receipt SyncCommitReceipt
	var committedRaw string
	err := s.db.QueryRowContext(ctx, `SELECT stage_id, checksum, repo_id, collection, committed_at FROM sync_commit_receipts WHERE stage_id = ?`, stageID).
		Scan(&receipt.StageID, &receipt.Checksum, &receipt.RepoID, &receipt.Collection, &committedRaw)
	if err == sql.ErrNoRows {
		return SyncCommitReceipt{}, false, nil
	}
	if err != nil {
		return SyncCommitReceipt{}, false, err
	}
	receipt.CommittedAt = parseTimeOrZero(committedRaw)
	return receipt, true, nil
}

func (s *SQLiteStore) ListMaintenanceFrontiers(ctx context.Context, repoID string) ([]MaintenanceFrontier, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT repo_id, remote_type, ordering, filter_key, lane, status, high_updated_at, high_remote_id, high_number, stop_reason, pages_listed, records_listed, checkpoint, last_error_class, last_success_at, updated_at FROM maintenance_frontiers WHERE (? = '' OR repo_id = ?) ORDER BY repo_id, remote_type, lane`, repoID, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MaintenanceFrontier
	for rows.Next() {
		var frontier MaintenanceFrontier
		var highRaw, lastSuccessRaw, updatedRaw string
		if err := rows.Scan(&frontier.RepoID, &frontier.RemoteType, &frontier.Ordering, &frontier.FilterKey, &frontier.Lane, &frontier.Status, &highRaw, &frontier.HighRemoteID, &frontier.HighNumber, &frontier.StopReason, &frontier.PagesListed, &frontier.RecordsListed, &frontier.Checkpoint, &frontier.LastErrorClass, &lastSuccessRaw, &updatedRaw); err != nil {
			return nil, err
		}
		frontier.HighUpdatedAt = parseTimeOrZero(highRaw)
		frontier.LastSuccessAt = parseTimeOrZero(lastSuccessRaw)
		frontier.UpdatedAt = parseTimeOrZero(updatedRaw)
		out = append(out, frontier)
	}
	return out, rows.Err()
}
