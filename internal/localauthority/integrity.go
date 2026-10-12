package localauthority

import (
	"context"
	"database/sql"
	"encoding/json"
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
		if err := checkHistoricalReferences(ctx, tx, v); err != nil {
			_ = rows.Close()
			return err
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
	return checkActiveBlocks(ctx, tx)
}

// Validate the existing snapshot once in O(vertices + edges), rather than run
// one recursive reachability/count query per edge. The edge bound also bounds
// both maps and the queue to at most 8192 distinct endpoint identities.
func checkActiveBlocks(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT CASE WHEN length(from_id)=36 THEN from_id END,
 CASE WHEN length(to_id)=36 THEN to_id END FROM relations WHERE relation='blocks' LIMIT 4097`)
	if err != nil {
		return storageError(ctx)
	}
	defer rows.Close()
	adjacent := map[authority.UUID][]authority.UUID{}
	indegree := map[authority.UUID]int{}
	edges := 0
	for rows.Next() {
		var from, to authority.UUID
		if rows.Scan(&from, &to) != nil || !from.Valid() || !to.Valid() {
			return Error{Corrupt}
		}
		edges++
		if edges > maxActiveBlocks {
			return Error{Capacity}
		}
		adjacent[from] = append(adjacent[from], to)
		if _, ok := indegree[from]; !ok {
			indegree[from] = 0
		}
		indegree[to]++
	}
	if rows.Err() != nil {
		return storageError(ctx)
	}
	_ = rows.Close()
	queue := make([]authority.UUID, 0, len(indegree))
	for id, degree := range indegree {
		if degree == 0 {
			queue = append(queue, id)
		}
	}
	for head := 0; head < len(queue); head++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		for _, to := range adjacent[queue[head]] {
			indegree[to]--
			if indegree[to] == 0 {
				queue = append(queue, to)
			}
		}
	}
	if len(queue) != len(indegree) {
		return Error{Corrupt}
	}
	return nil
}

func checkHistoricalReferences(ctx context.Context, tx *sql.Tx, view authority.EntityView) error {
	if view.Tombstone {
		if view.Citation.Revision == 1 {
			return Error{Corrupt}
		}
		var later int
		if tx.QueryRowContext(ctx, "SELECT count(*) FROM revisions WHERE entity_id=? AND revision>?", view.Citation.EntityID, view.Citation.Revision).Scan(&later) != nil || later != 0 {
			return Error{Corrupt}
		}
		return nil
	}
	var refs []authority.Citation
	switch view.Citation.Kind {
	case authority.Comment:
		var payload authority.CommentPayload
		if authority.DecodeJSON(view.Payload, &payload) != nil {
			return Error{Corrupt}
		}
		var original string
		if tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(payload AS BLOB))<=516096 THEN payload END FROM revisions WHERE entity_id=? AND revision=1`, view.Citation.EntityID).Scan(&original) != nil {
			return Error{Corrupt}
		}
		var first authority.CommentPayload
		if authority.DecodeJSON([]byte(original), &first) != nil || first.Issue != payload.Issue {
			return Error{Corrupt}
		}
		refs = []authority.Citation{payload.Issue}
	case authority.Link:
		var payload authority.LinkPayload
		if authority.DecodeJSON(view.Payload, &payload) != nil || !validRelationShape(payload) {
			return Error{Corrupt}
		}
		refs = []authority.Citation{payload.From, payload.To}
	}
	for _, ref := range refs {
		var matches int
		if ref.ProjectID != view.Citation.ProjectID || tx.QueryRowContext(ctx, `SELECT count(*) FROM entities e JOIN revisions r ON r.entity_id=e.entity_id WHERE e.entity_id=? AND e.kind=? AND r.revision=? AND r.sequence<? AND r.tombstone=0`, ref.EntityID, ref.Kind, ref.Revision, view.ProjectSequence).Scan(&matches) != nil || matches != 1 {
			return Error{Corrupt}
		}
		current, err := scanView(tx.QueryRowContext(ctx, "SELECT "+revisionColumns+` FROM entities e JOIN revisions r ON r.entity_id=e.entity_id WHERE e.entity_id=? AND r.sequence=(SELECT max(sequence) FROM revisions WHERE entity_id=? AND sequence<?)`, ref.EntityID, ref.EntityID, view.ProjectSequence), view.Citation.ProjectID)
		if err != nil || current.Tombstone || current.Citation.Kind != ref.Kind {
			return Error{Corrupt}
		}
		if ref.Kind == authority.Label || ref.Kind == authority.Milestone {
			var state struct {
				Archived bool `json:"archived"`
			}
			if json.Unmarshal(current.Payload, &state) != nil || state.Archived {
				return Error{Corrupt}
			}
		}
	}
	return nil
}
