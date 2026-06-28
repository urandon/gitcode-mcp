package cache

import (
	"context"
	"database/sql"
	"time"
)

func (s *SQLiteStore) UpsertPRReviewComment(ctx context.Context, comment PRReviewComment) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txRollbackOnError(tx, &err)
	if err := upsertPRReviewCommentTx(ctx, tx, comment); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertPRReviewCommentTx(ctx context.Context, tx *sql.Tx, comment PRReviewComment) error {
	createdAt := comment.CreatedAt
	updatedAt := comment.UpdatedAt
	if createdAt.IsZero() {
		createdAt = time.Unix(0, 0).UTC()
	}
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	return execTx(ctx, tx, `INSERT INTO pr_review_comments (repo_id, source_id, pr_number, comment_id, discussion_id, review_kind, author, path, line, start_line, end_line, position, original_position, resolved, resolvable, parent_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(repo_id, source_id) DO UPDATE SET pr_number = excluded.pr_number, comment_id = excluded.comment_id, discussion_id = excluded.discussion_id, review_kind = excluded.review_kind, author = excluded.author, path = excluded.path, line = excluded.line, start_line = excluded.start_line, end_line = excluded.end_line, position = excluded.position, original_position = excluded.original_position, resolved = excluded.resolved, resolvable = excluded.resolvable, parent_id = excluded.parent_id, updated_at = excluded.updated_at`,
		comment.RepoID, comment.SourceID, comment.PRNumber, comment.CommentID, comment.DiscussionID, comment.ReviewKind, comment.Author, comment.Path, comment.Line, comment.StartLine, comment.EndLine, comment.Position, comment.OriginalPosition, encodeNullableBool(comment.Resolved), encodeNullableBool(comment.Resolvable), comment.ParentID, createdAt.Format(time.RFC3339Nano), updatedAt.Format(time.RFC3339Nano))
}

func (s *SQLiteStore) ListPRReviewComments(ctx context.Context, filter PRReviewCommentFilter) ([]PRReviewComment, error) {
	query := `SELECT repo_id, source_id, pr_number, comment_id, discussion_id, review_kind, author, path, line, start_line, end_line, position, original_position, resolved, resolvable, parent_id, created_at, updated_at FROM pr_review_comments WHERE (? = '' OR repo_id = ?) AND (? = 0 OR pr_number = ?) AND (? = '' OR source_id = ?) ORDER BY pr_number, created_at, comment_id`
	rows, err := s.db.QueryContext(ctx, query, filter.RepoID, filter.RepoID, filter.PRNumber, filter.PRNumber, filter.SourceID, filter.SourceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PRReviewComment{}
	for rows.Next() {
		var comment PRReviewComment
		var resolvedRaw, resolvableRaw, createdRaw, updatedRaw string
		if err := rows.Scan(&comment.RepoID, &comment.SourceID, &comment.PRNumber, &comment.CommentID, &comment.DiscussionID, &comment.ReviewKind, &comment.Author, &comment.Path, &comment.Line, &comment.StartLine, &comment.EndLine, &comment.Position, &comment.OriginalPosition, &resolvedRaw, &resolvableRaw, &comment.ParentID, &createdRaw, &updatedRaw); err != nil {
			return nil, err
		}
		comment.Resolved = decodeNullableBool(resolvedRaw)
		comment.Resolvable = decodeNullableBool(resolvableRaw)
		comment.CreatedAt, _ = time.Parse(time.RFC3339Nano, createdRaw)
		comment.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updatedRaw)
		out = append(out, comment)
	}
	return out, rows.Err()
}

func encodeNullableBool(value *bool) string {
	if value == nil {
		return ""
	}
	if *value {
		return "true"
	}
	return "false"
}

func decodeNullableBool(value string) *bool {
	switch value {
	case "true":
		v := true
		return &v
	case "false":
		v := false
		return &v
	default:
		return nil
	}
}
