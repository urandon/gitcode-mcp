package cache

import (
	"context"
)

// ReconcileChildSources removes stale child source projections only after the
// caller has proved complete coverage for the parent collection.
func (s *SQLiteStore) ReconcileChildSources(ctx context.Context, repoID, parentID, kind string, keepSourceIDs []string) (err error) {
	keep := make(map[string]struct{}, len(keepSourceIDs))
	for _, id := range keepSourceIDs {
		if id != "" {
			keep[id] = struct{}{}
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer txRollbackOnError(tx, &err)
	rows, err := tx.QueryContext(ctx, `SELECT s.id
FROM sources s
JOIN links l ON l.repo_id = s.repo_id AND l.source_id = s.id
WHERE s.repo_id = ? AND s.kind = ? AND l.target_id = ? AND l.kind = 'parent'
ORDER BY s.id`, repoID, kind, parentID)
	if err != nil {
		return err
	}
	var stale []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		if _, ok := keep[id]; !ok {
			stale = append(stale, id)
		}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for _, id := range stale {
		if s.useFTS {
			if err = execTx(ctx, tx, `DELETE FROM fts_index WHERE repo_id = ? AND source_id = ?`, repoID, id); err != nil {
				return err
			}
		}
		if err = execTx(ctx, tx, `DELETE FROM records WHERE repo_id = ? AND record_id = ?`, repoID, id); err != nil {
			return err
		}
		if err = execTx(ctx, tx, `DELETE FROM sources WHERE repo_id = ? AND id = ?`, repoID, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
