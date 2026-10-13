package localauthority

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"math"

	"gitcode-mcp/internal/authority"
)

// SnapshotRequest is an internal projection input, not a storage registration
// or an authority mutation. Continuations retain the original project sequence.
type SnapshotRequest struct {
	ProjectID authority.UUID
	Limit     int
	Cursor    string
}

// SnapshotEntity carries transient protected content. Callers may retain its
// citation/digest, but must not persist document payload bytes in a projection.
type SnapshotEntity struct {
	Entity        authority.EntityView `json:"entity"`
	ContentDigest string               `json:"content_digest"`
}

type SnapshotPage struct {
	ProjectID  authority.UUID   `json:"project_id"`
	Sequence   uint64           `json:"sequence"`
	Entities   []SnapshotEntity `json:"entities"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

type snapshotCursor struct {
	ProjectID authority.UUID `json:"project_id"`
	Sequence  uint64         `json:"sequence"`
	Last      authority.UUID `json:"last"`
}

// Snapshot captures tracker and Git citations at one sequence. It never starts
// repair, writes an index, changes authority or follows the current Git HEAD.
// Tombstones are included so a derived owner can remove previously live entries.
func (s *Store) Snapshot(ctx context.Context, req SnapshotRequest) (SnapshotPage, error) {
	zero := SnapshotPage{}
	if !req.ProjectID.Valid() || req.Limit < 1 || req.Limit > 100 || len(req.Cursor) > 512 {
		return zero, authority.Error{Code: authority.InvalidContract}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return zero, Error{ReadOnly}
	}
	if req.ProjectID != s.manifest.ID {
		return zero, authority.Error{Code: authority.NoAuthority}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, storageError(ctx)
	}
	defer tx.Rollback()
	var current int64
	if tx.QueryRowContext(ctx, "SELECT sequence FROM project WHERE singleton=1").Scan(&current) != nil || current < 0 {
		return zero, storageError(ctx)
	}
	c := snapshotCursor{ProjectID: req.ProjectID, Sequence: uint64(current)}
	if req.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(req.Cursor)
		if err != nil || authority.DecodeJSON(data, &c) != nil || c.ProjectID != req.ProjectID || c.Sequence > uint64(current) || c.Sequence > math.MaxInt64 || !c.Last.Valid() {
			return zero, authority.Error{Code: authority.InvalidContract}
		}
	}
	// The subquery selects the latest immutable revision at the captured
	// sequence, even if a subsequent page is read after newer writes.
	rows, err := tx.QueryContext(ctx, "SELECT "+revisionColumns+` FROM entities e JOIN revisions r ON r.entity_id=e.entity_id
	 WHERE e.entity_id>? AND r.revision=(SELECT max(v.revision) FROM revisions v WHERE v.entity_id=e.entity_id AND v.sequence<=?)
	 ORDER BY e.entity_id LIMIT ?`, c.Last, int64(c.Sequence), req.Limit+1)
	if err != nil {
		return zero, storageError(ctx)
	}
	views := make([]authority.EntityView, 0, req.Limit+1)
	for rows.Next() {
		v, err := scanView(rows, req.ProjectID)
		if err != nil {
			_ = rows.Close()
			return zero, err
		}
		views = append(views, v)
	}
	readErr := rows.Err()
	_ = rows.Close()
	if readErr != nil {
		return zero, storageError(ctx)
	}
	if tx.Commit() != nil {
		return zero, storageError(ctx)
	}
	page := SnapshotPage{ProjectID: req.ProjectID, Sequence: c.Sequence, Entities: []SnapshotEntity{}}
	var knowledge *knowledgeRepository
	for _, v := range views {
		if len(page.Entities) == req.Limit {
			break
		}
		if v.Citation.Kind == authority.Document {
			if knowledge == nil {
				g, err := s.readyKnowledgeRepository(ctx)
				if err != nil {
					return zero, err
				}
				knowledge = &g
			}
			v, err = hydrateDocument(*knowledge, v)
			if err != nil {
				return zero, err
			}
		}
		// Payload is canonical typed content, deliberately excluding actor,
		// timestamps, sequence and revision. Equal content can reuse vectors
		// while its new immutable citation/generation is still published.
		entry := SnapshotEntity{Entity: v, ContentDigest: hash(v.Payload)}
		candidate := page
		candidate.Entities = append(append([]SnapshotEntity{}, page.Entities...), entry)
		data, err := json.Marshal(candidate)
		if err != nil {
			return zero, Error{Corrupt}
		}
		if len(data) > authority.MaxContractBytes-4096 {
			if len(page.Entities) == 0 {
				return zero, Error{Capacity}
			}
			break
		}
		page = candidate
		c.Last = v.Citation.EntityID
	}
	if len(page.Entities) < len(views) {
		data, _ := json.Marshal(c)
		page.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	return page, nil
}
