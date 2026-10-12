package localauthority

import (
	"context"
	"database/sql"
	"encoding/json"
	"math"

	"gitcode-mcp/internal/authority"
)

// A reference binds an immutable revision, but new relationships additionally
// require a live current target. Historical reads remain available afterward.
func reference(ctx context.Context, tx *sql.Tx, c authority.Citation) error {
	if c.Revision > math.MaxInt64 {
		return authority.Error{Code: authority.InvalidContract}
	}
	var count int
	if tx.QueryRowContext(ctx, "SELECT count(*) FROM revisions WHERE entity_id=? AND revision=?", c.EntityID, c.Revision).Scan(&count) != nil {
		return storageError(ctx)
	}
	if count == 0 {
		return authority.Error{Code: authority.NoAuthority}
	}
	historical, err := scanView(tx.QueryRowContext(ctx, "SELECT "+revisionColumns+" FROM entities e JOIN revisions r ON r.entity_id=e.entity_id WHERE e.entity_id=? AND r.revision=?", c.EntityID, c.Revision), c.ProjectID)
	if err != nil {
		return err
	}
	current, err := scanView(tx.QueryRowContext(ctx, "SELECT "+revisionColumns+" FROM entities e JOIN revisions r ON r.entity_id=e.entity_id AND r.revision=e.current_revision WHERE e.entity_id=?", c.EntityID), c.ProjectID)
	if err != nil {
		return err
	}
	if historical.Citation.Kind != c.Kind || current.Tombstone {
		return authority.Error{Code: authority.Conflict}
	}
	if c.Kind == authority.Label || c.Kind == authority.Milestone {
		var lifecycle struct {
			Archived bool `json:"archived"`
		}
		if json.Unmarshal(current.Payload, &lifecycle) != nil {
			return Error{Corrupt}
		}
		if lifecycle.Archived {
			return authority.Error{Code: authority.Conflict}
		}
	}
	return nil
}

func validateRelationships(ctx context.Context, tx *sql.Tx, id authority.UUID, m authority.MutationRequest, create bool) error {
	switch m.Operation {
	case "comment.create", "comment.update":
		var p authority.CommentPayload
		_ = json.Unmarshal(m.Payload, &p)
		if err := reference(ctx, tx, p.Issue); err != nil {
			return err
		}
		if !create {
			var previous string
			if tx.QueryRowContext(ctx, "SELECT r.payload FROM entities e JOIN revisions r ON r.entity_id=e.entity_id AND r.revision=e.current_revision WHERE e.entity_id=?", id).Scan(&previous) != nil {
				return storageError(ctx)
			}
			var old authority.CommentPayload
			if authority.DecodeJSON([]byte(previous), &old) != nil {
				return Error{Corrupt}
			}
			if old.Issue != p.Issue {
				return authority.Error{Code: authority.Conflict}
			}
		}
	case "link.create", "link.update":
		var p authority.LinkPayload
		_ = json.Unmarshal(m.Payload, &p)
		if !validRelationShape(p) {
			return authority.Error{Code: authority.InvalidContract}
		}
		if err := reference(ctx, tx, p.From); err != nil {
			return err
		}
		if err := reference(ctx, tx, p.To); err != nil {
			return err
		}
		switch p.Relation {
		case "label":
			if p.From.Kind != authority.Issue || p.To.Kind != authority.Label {
				return authority.Error{Code: authority.InvalidContract}
			}
			var count int
			if tx.QueryRowContext(ctx, "SELECT count(*) FROM relations WHERE relation='label' AND from_id=? AND to_id=? AND link_id<>?", p.From.EntityID, p.To.EntityID, id).Scan(&count) != nil {
				return storageError(ctx)
			}
			if count != 0 {
				return authority.Error{Code: authority.Conflict}
			}
		case "milestone":
			if p.From.Kind != authority.Issue || p.To.Kind != authority.Milestone {
				return authority.Error{Code: authority.InvalidContract}
			}
			var count int
			if tx.QueryRowContext(ctx, "SELECT count(*) FROM relations WHERE relation='milestone' AND from_id=? AND link_id<>?", p.From.EntityID, id).Scan(&count) != nil {
				return storageError(ctx)
			}
			if count != 0 {
				return authority.Error{Code: authority.Conflict}
			}
		case "evidence_for":
			if p.To.Kind != authority.Issue || p.From.Kind != authority.Comment && p.From.Kind != authority.Document {
				return authority.Error{Code: authority.InvalidContract}
			}
		case "blocks":
			if p.From.Kind != authority.Issue || p.To.Kind != authority.Issue {
				return authority.Error{Code: authority.InvalidContract}
			}
			// UNION deduplicates cycles in damaged data, and the hard cap bounds
			// fanout/memory. At capacity, refuse rather than guess acyclicity.
			rows, err := tx.QueryContext(ctx, `WITH RECURSIVE reachable(id) AS (
 SELECT ? UNION SELECT r.to_id FROM relations r JOIN reachable v ON r.from_id=v.id
 WHERE r.relation='blocks' AND r.link_id<>? LIMIT 4097)
 SELECT id FROM reachable`, p.To.EntityID, id)
			if err != nil {
				return storageError(ctx)
			}
			defer rows.Close()
			count := 0
			for rows.Next() {
				var next string
				if rows.Scan(&next) != nil {
					return storageError(ctx)
				}
				count++
				if next == string(p.From.EntityID) {
					return authority.Error{Code: authority.Conflict}
				}
				if count > 4096 {
					return Error{Capacity}
				}
			}
			if rows.Err() != nil {
				return storageError(ctx)
			}
		}
	}
	return nil
}

func validRelationShape(p authority.LinkPayload) bool {
	if p.From.EntityID == p.To.EntityID || p.From.Kind == authority.Link || p.To.Kind == authority.Link {
		return false
	}
	switch p.Relation {
	case "relates_to":
		return true
	case "blocks":
		return p.From.Kind == authority.Issue && p.To.Kind == authority.Issue
	case "label":
		return p.From.Kind == authority.Issue && p.To.Kind == authority.Label
	case "milestone":
		return p.From.Kind == authority.Issue && p.To.Kind == authority.Milestone
	case "evidence_for":
		return p.To.Kind == authority.Issue && (p.From.Kind == authority.Comment || p.From.Kind == authority.Document)
	default:
		return false
	}
}
