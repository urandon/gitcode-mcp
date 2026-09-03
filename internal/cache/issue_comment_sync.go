package cache

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

func (s *SQLiteStore) UpsertIssueCommentSync(ctx context.Context, item IssueCommentSync) error {
	return upsertIssueCommentSyncExec(ctx, s.db, item)
}

func upsertIssueCommentSyncTx(ctx context.Context, tx *sql.Tx, item IssueCommentSync) error {
	return upsertIssueCommentSyncExec(ctx, tx, item)
}

func upsertIssueCommentSyncExec(ctx context.Context, executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, item IssueCommentSync) error {
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = time.Now().UTC()
	}
	lastAttempt := ""
	if !item.LastAttemptAt.IsZero() {
		lastAttempt = item.LastAttemptAt.UTC().Format(time.RFC3339Nano)
	}
	_, err := executor.ExecContext(ctx, `INSERT INTO issue_comment_sync (repo_id, source_id, issue_number, remote_id, provider_id, remote_revision, expected_count, status, attempts, last_error_class, retry_after, last_attempt_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(repo_id, source_id) DO UPDATE SET issue_number = excluded.issue_number, remote_id = excluded.remote_id, provider_id = excluded.provider_id, remote_revision = excluded.remote_revision, expected_count = excluded.expected_count, status = excluded.status, attempts = excluded.attempts, last_error_class = excluded.last_error_class, retry_after = excluded.retry_after, last_attempt_at = excluded.last_attempt_at, updated_at = excluded.updated_at`,
		item.RepoID, item.SourceID, item.IssueNumber, item.RemoteID, item.ProviderID, item.RemoteRevision, item.ExpectedCount, item.Status, item.Attempts, item.LastErrorClass, item.RetryAfter, lastAttempt, item.UpdatedAt.UTC().Format(time.RFC3339Nano))
	return err
}

func (s *SQLiteStore) GetIssueCommentSync(ctx context.Context, repoID, sourceID string) (IssueCommentSync, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT repo_id, source_id, issue_number, remote_id, provider_id, remote_revision, expected_count, status, attempts, last_error_class, retry_after, last_attempt_at, updated_at FROM issue_comment_sync WHERE repo_id = ? AND source_id = ?`, repoID, sourceID)
	item, err := scanIssueCommentSync(row)
	if err == sql.ErrNoRows {
		return IssueCommentSync{}, false, nil
	}
	return item, err == nil, err
}

func (s *SQLiteStore) ListIssueCommentSync(ctx context.Context, filter IssueCommentSyncFilter) ([]IssueCommentSync, error) {
	query := `SELECT repo_id, source_id, issue_number, remote_id, provider_id, remote_revision, expected_count, status, attempts, last_error_class, retry_after, last_attempt_at, updated_at FROM issue_comment_sync WHERE repo_id = ?`
	args := []any{filter.RepoID}
	if len(filter.Statuses) > 0 {
		placeholders := make([]string, len(filter.Statuses))
		for i, status := range filter.Statuses {
			placeholders[i] = "?"
			args = append(args, status)
		}
		query += ` AND status IN (` + strings.Join(placeholders, ",") + `)`
	}
	query += ` ORDER BY CASE status WHEN 'pending' THEN 0 WHEN 'deferred' THEN 1 ELSE 2 END, updated_at, issue_number DESC`
	if filter.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []IssueCommentSync{}
	for rows.Next() {
		item, err := scanIssueCommentSync(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) IssueCommentSyncSummary(ctx context.Context, repoID string) (IssueCommentSyncSummary, error) {
	summary := IssueCommentSyncSummary{RepoID: repoID}
	rows, err := s.db.QueryContext(ctx, `SELECT status, count(*) FROM issue_comment_sync WHERE repo_id = ? GROUP BY status`, repoID)
	if err != nil {
		return summary, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return summary, err
		}
		summary.Total += count
		switch status {
		case "pending":
			summary.Pending = count
		case "deferred":
			summary.Deferred = count
		case "complete":
			summary.Complete = count
		}
	}
	return summary, rows.Err()
}

func (s *SQLiteStore) UpsertRecordComments(ctx context.Context, repoID, recordID string, comments []RecordComment) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txRollbackOnError(tx, &err)
	for _, comment := range comments {
		comment.RepoID = repoID
		comment.RecordID = recordID
		if err = upsertRecordCommentTx(ctx, tx, comment); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLiteStore) ReconcileRecordComments(ctx context.Context, repoID, recordID string, keepCommentIDs []string) (err error) {
	keep := make(map[string]struct{}, len(keepCommentIDs))
	for _, id := range keepCommentIDs {
		keep[id] = struct{}{}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txRollbackOnError(tx, &err)
	rows, err := tx.QueryContext(ctx, `SELECT comment_id FROM record_comments WHERE repo_id = ? AND record_id = ?`, repoID, recordID)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if _, ok := keep[id]; !ok {
			stale = append(stale, id)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM record_comments WHERE repo_id = ? AND record_id = ? AND comment_id = ?`, repoID, recordID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLiteStore) ReplaceRecordComments(ctx context.Context, repoID, recordID string, comments []RecordComment) (err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txRollbackOnError(tx, &err)
	if _, err = tx.ExecContext(ctx, `DELETE FROM record_comments WHERE repo_id = ? AND record_id = ?`, repoID, recordID); err != nil {
		return err
	}
	for _, comment := range comments {
		comment.RepoID = repoID
		comment.RecordID = recordID
		if err = upsertRecordCommentTx(ctx, tx, comment); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func scanIssueCommentSync(row interface{ Scan(...any) error }) (IssueCommentSync, error) {
	var item IssueCommentSync
	var lastAttemptRaw, updatedRaw string
	if err := row.Scan(&item.RepoID, &item.SourceID, &item.IssueNumber, &item.RemoteID, &item.ProviderID, &item.RemoteRevision, &item.ExpectedCount, &item.Status, &item.Attempts, &item.LastErrorClass, &item.RetryAfter, &lastAttemptRaw, &updatedRaw); err != nil {
		return IssueCommentSync{}, err
	}
	if lastAttemptRaw != "" {
		item.LastAttemptAt, _ = time.Parse(time.RFC3339Nano, lastAttemptRaw)
	}
	item.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedRaw)
	return item, nil
}
