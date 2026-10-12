package localauthority

import (
	"context"
	"database/sql"
	"gitcode-mcp/internal/authority"
	"strings"
)

// Schema version means the entire constraint/trigger contract, not merely a
// writable user_version marker. The catalog and each value have fixed bounds.
func checkSchema(ctx context.Context, tx *sql.Tx) error {
	want := map[string]string{}
	for _, statement := range strings.Split(schemaSQL, ";\n") {
		statement = strings.TrimSuffix(strings.TrimSpace(statement), ";")
		if statement == "" {
			continue
		}
		fields := strings.Fields(statement)
		if len(fields) < 3 {
			return Error{Corrupt}
		}
		want[fields[2]] = statement
	}
	rows, err := tx.QueryContext(ctx, `SELECT type,
 CASE WHEN length(name)<=128 THEN name END,
 CASE WHEN sql IS NULL OR length(CAST(sql AS BLOB))<=16384 THEN sql END
 FROM sqlite_schema ORDER BY name LIMIT 65`)
	if err != nil {
		return Error{Corrupt}
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var kind, name string
		var ddl sql.NullString
		if rows.Scan(&kind, &name, &ddl) != nil {
			return Error{Corrupt}
		}
		seen++
		if seen > 64 {
			return Error{Corrupt}
		}
		if !ddl.Valid && kind == "index" && strings.HasPrefix(name, "sqlite_autoindex_") {
			continue
		}
		expected, exists := want[name]
		if !exists || !ddl.Valid || strings.TrimSpace(ddl.String) != expected {
			return Error{Corrupt}
		}
		delete(want, name)
	}
	if rows.Err() != nil || len(want) != 0 {
		return Error{Corrupt}
	}
	return nil
}

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
	for rows.Next() {
		if _, err := scanReceipt(rows, project, ctx); err != nil {
			_ = rows.Close()
			return err
		}
	}
	if rows.Err() != nil {
		_ = rows.Close()
		return storageError(ctx)
	}
	_ = rows.Close()
	rows, err = tx.QueryContext(ctx, auditQuery)
	if err != nil {
		return storageError(ctx)
	}
	for rows.Next() {
		if _, err := scanAudit(rows, project); err != nil {
			_ = rows.Close()
			return err
		}
	}
	if rows.Err() != nil {
		_ = rows.Close()
		return storageError(ctx)
	}
	_ = rows.Close()
	return checkActiveRelations(ctx, tx, project)
}

type tailScanner struct {
	row  scanner
	tail []any
}

func (s tailScanner) Scan(values ...any) error { return s.row.Scan(append(values, s.tail...)...) }

func checkActiveRelations(ctx context.Context, tx *sql.Tx, project authority.UUID) error {
	rows, err := tx.QueryContext(ctx, "SELECT "+revisionColumns+`,rel.link_id,rel.from_id,rel.from_revision,rel.to_id,rel.to_revision,rel.relation
 FROM entities e JOIN revisions r ON r.entity_id=e.entity_id AND r.revision=e.current_revision
 LEFT JOIN relations rel ON rel.link_id=e.entity_id`)
	if err != nil {
		return storageError(ctx)
	}
	defer rows.Close()
	for rows.Next() {
		var link, from, to, relation sql.NullString
		var fromRev, toRev sql.NullInt64
		view, err := scanView(tailScanner{rows, []any{&link, &from, &fromRev, &to, &toRev, &relation}}, project)
		if err != nil {
			return err
		}
		if view.Citation.Kind != authority.Link || view.Tombstone {
			if link.Valid {
				return Error{Corrupt}
			}
			continue
		}
		var payload authority.LinkPayload
		if authority.DecodeJSON(view.Payload, &payload) != nil || !link.Valid || !from.Valid || !to.Valid || !fromRev.Valid || !toRev.Valid || !relation.Valid || link.String != string(view.Citation.EntityID) || payload.From.ProjectID != project || payload.To.ProjectID != project || from.String != string(payload.From.EntityID) || to.String != string(payload.To.EntityID) || fromRev.Int64 < 1 || toRev.Int64 < 1 || uint64(fromRev.Int64) != payload.From.Revision || uint64(toRev.Int64) != payload.To.Revision || relation.String != payload.Relation {
			return Error{Corrupt}
		}
		// Immutable targets must still exist with their exact kinds. Archive is
		// not a retroactive invalidation of an already authoritative link.
		for _, citation := range []authority.Citation{payload.From, payload.To} {
			var count int
			if tx.QueryRowContext(ctx, `SELECT count(*) FROM entities e JOIN revisions r ON r.entity_id=e.entity_id WHERE e.entity_id=? AND e.kind=? AND r.revision=?`, citation.EntityID, citation.Kind, citation.Revision).Scan(&count) != nil || count != 1 {
				return Error{Corrupt}
			}
		}
		if !validRelationShape(payload) {
			return Error{Corrupt}
		}
	}
	if rows.Err() != nil {
		return storageError(ctx)
	}
	_ = rows.Close()
	var invalid int
	if tx.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT from_id FROM relations WHERE relation='milestone' GROUP BY from_id HAVING count(*)>1)`).Scan(&invalid) != nil || invalid != 0 {
		return Error{Corrupt}
	}
	if tx.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT from_id,to_id FROM relations WHERE relation='label' GROUP BY from_id,to_id HAVING count(*)>1)`).Scan(&invalid) != nil || invalid != 0 {
		return Error{Corrupt}
	}
	return nil
}
