package cache

import (
	"context"
	"database/sql"
	"time"
)

func (s *SQLiteStore) UpsertPRReviewDiscussion(ctx context.Context, discussion PRReviewDiscussion) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txRollbackOnError(tx, &err)
	if err := upsertPRReviewDiscussionTx(ctx, tx, discussion); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertPRReviewDiscussionTx(ctx context.Context, tx *sql.Tx, discussion PRReviewDiscussion) error {
	createdAt := discussion.CreatedAt
	updatedAt := discussion.UpdatedAt
	if createdAt.IsZero() {
		createdAt = time.Unix(0, 0).UTC()
	}
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	return execTx(ctx, tx, `INSERT INTO pr_review_discussions (repo_id, pr_number, discussion_id, kind, resolved, resolvable, first_comment_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(repo_id, pr_number, discussion_id) DO UPDATE SET kind = excluded.kind, resolved = excluded.resolved, resolvable = excluded.resolvable, first_comment_id = CASE WHEN pr_review_discussions.first_comment_id = '' THEN excluded.first_comment_id ELSE pr_review_discussions.first_comment_id END, updated_at = excluded.updated_at`,
		discussion.RepoID, discussion.PRNumber, discussion.DiscussionID, discussion.Kind, encodeNullableBool(discussion.Resolved), encodeNullableBool(discussion.Resolvable), discussion.FirstCommentID, createdAt.Format(time.RFC3339Nano), updatedAt.Format(time.RFC3339Nano))
}

func (s *SQLiteStore) ListPRReviewDiscussions(ctx context.Context, filter PRReviewDiscussionFilter) ([]PRReviewDiscussion, error) {
	query := `SELECT repo_id, pr_number, discussion_id, kind, resolved, resolvable, first_comment_id, created_at, updated_at FROM pr_review_discussions WHERE (? = '' OR repo_id = ?) AND (? = 0 OR pr_number = ?) AND (? = '' OR discussion_id = ?) ORDER BY pr_number, created_at, discussion_id`
	rows, err := s.db.QueryContext(ctx, query, filter.RepoID, filter.RepoID, filter.PRNumber, filter.PRNumber, filter.DiscussionID, filter.DiscussionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PRReviewDiscussion{}
	for rows.Next() {
		var discussion PRReviewDiscussion
		var resolvedRaw, resolvableRaw, createdRaw, updatedRaw string
		if err := rows.Scan(&discussion.RepoID, &discussion.PRNumber, &discussion.DiscussionID, &discussion.Kind, &resolvedRaw, &resolvableRaw, &discussion.FirstCommentID, &createdRaw, &updatedRaw); err != nil {
			return nil, err
		}
		discussion.Resolved = decodeNullableBool(resolvedRaw)
		discussion.Resolvable = decodeNullableBool(resolvableRaw)
		discussion.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdRaw)
		discussion.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedRaw)
		out = append(out, discussion)
	}
	return out, rows.Err()
}

func (s *SQLiteStore) UpsertPRReviewPosition(ctx context.Context, position PRReviewPosition) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txRollbackOnError(tx, &err)
	if err := upsertPRReviewPositionTx(ctx, tx, position); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertPRReviewPositionTx(ctx context.Context, tx *sql.Tx, position PRReviewPosition) error {
	createdAt := position.CreatedAt
	updatedAt := position.UpdatedAt
	if createdAt.IsZero() {
		createdAt = time.Unix(0, 0).UTC()
	}
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	if position.PositionKind == "" {
		position.PositionKind = "current"
	}
	return execTx(ctx, tx, `INSERT INTO pr_review_positions (repo_id, pr_number, comment_id, position_kind, discussion_id, position_type, base_sha, start_sha, head_sha, old_path, new_path, old_line, new_line, start_old_line, start_new_line, line_code, start_line_code, patchset_iid, diff_id, version_sha, side, is_outdated, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(repo_id, pr_number, comment_id, position_kind) DO UPDATE SET discussion_id = excluded.discussion_id, position_type = excluded.position_type, base_sha = excluded.base_sha, start_sha = excluded.start_sha, head_sha = excluded.head_sha, old_path = excluded.old_path, new_path = excluded.new_path, old_line = excluded.old_line, new_line = excluded.new_line, start_old_line = excluded.start_old_line, start_new_line = excluded.start_new_line, line_code = excluded.line_code, start_line_code = excluded.start_line_code, patchset_iid = excluded.patchset_iid, diff_id = excluded.diff_id, version_sha = excluded.version_sha, side = excluded.side, is_outdated = excluded.is_outdated, updated_at = excluded.updated_at`,
		position.RepoID, position.PRNumber, position.CommentID, position.PositionKind, position.DiscussionID, position.PositionType, position.BaseSHA, position.StartSHA, position.HeadSHA, position.OldPath, position.NewPath, position.OldLine, position.NewLine, position.StartOldLine, position.StartNewLine, position.LineCode, position.StartLineCode, position.PatchsetIID, position.DiffID, position.VersionSHA, position.Side, encodeNullableBool(position.IsOutdated), createdAt.Format(time.RFC3339Nano), updatedAt.Format(time.RFC3339Nano))
}

func (s *SQLiteStore) ListPRReviewPositions(ctx context.Context, filter PRReviewPositionFilter) ([]PRReviewPosition, error) {
	query := `SELECT repo_id, pr_number, comment_id, position_kind, discussion_id, position_type, base_sha, start_sha, head_sha, old_path, new_path, old_line, new_line, start_old_line, start_new_line, line_code, start_line_code, patchset_iid, diff_id, version_sha, side, is_outdated, created_at, updated_at FROM pr_review_positions WHERE (? = '' OR repo_id = ?) AND (? = 0 OR pr_number = ?) AND (? = '' OR comment_id = ?) AND (? = '' OR discussion_id = ?) ORDER BY pr_number, comment_id, position_kind`
	rows, err := s.db.QueryContext(ctx, query, filter.RepoID, filter.RepoID, filter.PRNumber, filter.PRNumber, filter.CommentID, filter.CommentID, filter.DiscussionID, filter.DiscussionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PRReviewPosition{}
	for rows.Next() {
		var position PRReviewPosition
		var outdatedRaw, createdRaw, updatedRaw string
		if err := rows.Scan(&position.RepoID, &position.PRNumber, &position.CommentID, &position.PositionKind, &position.DiscussionID, &position.PositionType, &position.BaseSHA, &position.StartSHA, &position.HeadSHA, &position.OldPath, &position.NewPath, &position.OldLine, &position.NewLine, &position.StartOldLine, &position.StartNewLine, &position.LineCode, &position.StartLineCode, &position.PatchsetIID, &position.DiffID, &position.VersionSHA, &position.Side, &outdatedRaw, &createdRaw, &updatedRaw); err != nil {
			return nil, err
		}
		position.IsOutdated = decodeNullableBool(outdatedRaw)
		position.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdRaw)
		position.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedRaw)
		out = append(out, position)
	}
	return out, rows.Err()
}
