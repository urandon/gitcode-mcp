package localauthority

import (
	"context"
	"database/sql"
	"gitcode-mcp/internal/authority"
)

// Inspection uses one read transaction snapshot. Each individual value is
// bounded before Scan; no unbounded in-memory history map is constructed.
func checkLogicalRows(ctx context.Context, tx *sql.Tx, project authority.UUID) error {
	rows, err := tx.QueryContext(ctx, "SELECT "+revisionColumns+" FROM entities e JOIN revisions r ON r.entity_id=e.entity_id")
	if err != nil {
		return storageError(ctx)
	}
	for rows.Next() {
		v, e := scanView(rows, project)
		if e != nil {
			_ = rows.Close()
			return e
		}
		if v.Citation.Kind == authority.Document {
			_ = rows.Close()
			return Error{Corrupt}
		}
	}
	if rows.Err() != nil {
		_ = rows.Close()
		return storageError(ctx)
	}
	_ = rows.Close()
	rows, err = tx.QueryContext(ctx, receiptQuery)
	if err != nil {
		return storageError(ctx)
	}
	defer rows.Close()
	for rows.Next() {
		if _, err := scanReceipt(rows, project, ctx); err != nil {
			return err
		}
	}
	if rows.Err() != nil {
		return storageError(ctx)
	}
	return nil
}
