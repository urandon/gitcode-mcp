package cache

import (
	"context"
	"database/sql"
)

// RepairIssueProviderPlaceholders merges synthetic issue parents created when
// a provider id was previously mistaken for a repository-local issue number.
// Candidates are deliberately narrow: either an empty generic issue record
// without aliases, or an exact title/body copy carrying only the mistaken
// issue:<provider-id> alias. In both cases the remote id must already be the
// provider alias of a different canonical issue.
func (s *SQLiteStore) RepairIssueProviderPlaceholders(ctx context.Context, repoID string) (repaired int, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer txRollbackOnError(tx, &err)
	rows, err := tx.QueryContext(ctx, `SELECT placeholder.record_id, canonical.source_id
FROM records placeholder
JOIN identity_map canonical
  ON canonical.repo_id = placeholder.repo_id
 AND canonical.alias_type = 'gitcode_issue_id'
 AND canonical.alias = placeholder.remote_id
JOIN records canonical_record
  ON canonical_record.repo_id = canonical.repo_id
 AND canonical_record.record_id = canonical.source_id
WHERE placeholder.repo_id = ?
  AND placeholder.record_type = 'issue'
  AND placeholder.remote_type = 'issue'
  AND placeholder.record_id = 'ISSUE-' || placeholder.remote_id
  AND placeholder.record_id <> canonical.source_id
  AND (
    (placeholder.title = 'Issue ' || placeholder.remote_id
      AND trim(placeholder.body) = '' AND NOT EXISTS (
      SELECT 1 FROM identity_map own
      WHERE own.repo_id = placeholder.repo_id
        AND own.source_id = placeholder.record_id
    ))
    OR (
      placeholder.title = canonical_record.title
      AND placeholder.body = canonical_record.body
      AND EXISTS (
        SELECT 1 FROM identity_map own
        WHERE own.repo_id = placeholder.repo_id
          AND own.source_id = placeholder.record_id
          AND own.alias_type = 'issue'
          AND own.alias = placeholder.remote_id
      )
      AND NOT EXISTS (
        SELECT 1 FROM identity_map own
        WHERE own.repo_id = placeholder.repo_id
          AND own.source_id = placeholder.record_id
          AND NOT (own.alias_type = 'issue' AND own.alias = placeholder.remote_id)
      )
    )
  )
ORDER BY placeholder.record_id`, repoID)
	if err != nil {
		return 0, err
	}
	type candidate struct {
		placeholder string
		canonical   string
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err = rows.Scan(&item.placeholder, &item.canonical); err != nil {
			_ = rows.Close()
			return 0, err
		}
		candidates = append(candidates, item)
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}
	if err = rows.Err(); err != nil {
		return 0, err
	}
	for _, item := range candidates {
		if err = mergeIssueProviderPlaceholderTx(ctx, tx, s.useFTS, repoID, item.placeholder, item.canonical); err != nil {
			return repaired, err
		}
		repaired++
	}
	if err = tx.Commit(); err != nil {
		return repaired, err
	}
	return repaired, nil
}

func mergeIssueProviderPlaceholderTx(ctx context.Context, tx *sql.Tx, useFTS bool, repoID, placeholderID, canonicalID string) error {
	if err := execTx(ctx, tx, `INSERT INTO record_comments (repo_id, record_id, comment_id, author, body, content_hash, remote_revision, created_at, updated_at)
SELECT repo_id, ?, comment_id, author, body, content_hash, remote_revision, created_at, updated_at
FROM record_comments
WHERE repo_id = ? AND record_id = ? AND true
ON CONFLICT(repo_id, record_id, comment_id) DO UPDATE SET
  author = excluded.author,
  body = excluded.body,
  content_hash = excluded.content_hash,
  remote_revision = excluded.remote_revision,
  created_at = excluded.created_at,
  updated_at = excluded.updated_at`, canonicalID, repoID, placeholderID); err != nil {
		return err
	}
	if err := execTx(ctx, tx, `INSERT OR IGNORE INTO links (repo_id, source_id, target_id, kind, text)
SELECT repo_id, source_id, ?, kind, text
FROM links
WHERE repo_id = ? AND target_id = ? AND source_id <> ?`, canonicalID, repoID, placeholderID, placeholderID); err != nil {
		return err
	}
	if err := execTx(ctx, tx, `DELETE FROM links WHERE repo_id = ? AND target_id = ?`, repoID, placeholderID); err != nil {
		return err
	}
	if err := execTx(ctx, tx, `UPDATE audit_trail SET record_id = ? WHERE repo_id = ? AND record_id = ?`, canonicalID, repoID, placeholderID); err != nil {
		return err
	}
	if err := execTx(ctx, tx, `DELETE FROM issue_comment_sync WHERE repo_id = ? AND source_id = ?`, repoID, placeholderID); err != nil {
		return err
	}
	if useFTS {
		if err := execTx(ctx, tx, `DELETE FROM fts_index WHERE repo_id = ? AND source_id = ?`, repoID, placeholderID); err != nil {
			return err
		}
	}
	if err := execTx(ctx, tx, `DELETE FROM records WHERE repo_id = ? AND record_id = ?`, repoID, placeholderID); err != nil {
		return err
	}
	return execTx(ctx, tx, `DELETE FROM sources WHERE repo_id = ? AND id = ?`, repoID, placeholderID)
}
