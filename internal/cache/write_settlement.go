package cache

import (
	"context"
	"errors"
	"time"
)

// SettleWriteGraphGeneration atomically publishes a graph and its successful
// receipt only while the exact observed pending receipt still owns settlement.
// In particular, a delayed writer cannot overwrite a completed recovery, or a
// newer pending readback, even when both callers share the audit generation.
func (s *SQLiteStore) SettleWriteGraphGeneration(ctx context.Context, graph RecordGraph, complete, pending AuditTrailEntry, generation time.Time) (settled bool, err error) {
	if graph.Record.RepoID != complete.RepoID || graph.Record.ID != complete.RecordID || len(graph.AuditTrail) != 0 || pending.ID != complete.ID || pending.RepoID != complete.RepoID || pending.PayloadHash != complete.PayloadHash || pending.Status != "remote_confirmed_cache_refresh_pending" || complete.Status != "succeeded" {
		return false, errors.New("cache: invalid write graph settlement")
	}
	expectedMetadata, err := marshalJSON(pending.RequestMetadata)
	if err != nil {
		return false, err
	}
	metadata, err := marshalJSON(complete.RequestMetadata)
	if err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE audit_trail SET
operation = ?, command = ?, mode = ?, record_id = ?, remote_type = ?, remote_id = ?, status = ?, message = ?, request_metadata = ?
WHERE repo_id = ? AND id = ? AND created_at = ? AND status = ? AND payload_hash = ? AND request_metadata = ?`,
		complete.Operation, complete.Command, complete.Mode, complete.RecordID, complete.RemoteType, complete.RemoteID, complete.Status, complete.Message, metadata,
		pending.RepoID, pending.ID, generation.UTC().Format(time.RFC3339Nano), pending.Status, pending.PayloadHash, expectedMetadata)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return false, err
	}
	if err = s.upsertRecordGraphTx(ctx, tx, graph); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
