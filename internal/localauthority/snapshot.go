package localauthority

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"

	"gitcode-mcp/internal/authority"
)

// SnapshotRequest is an internal projection input, not a storage registration
// or an authority mutation. Continuations retain the original project sequence.
type SnapshotRequest struct {
	ProjectID authority.UUID
	Kind      authority.EntityKind
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
	Scanned    int              `json:"scanned"`
	Entities   []SnapshotEntity `json:"entities"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

type snapshotCursor struct {
	ProjectID authority.UUID       `json:"project_id"`
	Sequence  uint64               `json:"sequence"`
	Last      authority.UUID       `json:"last"`
	Kind      authority.EntityKind `json:"kind,omitempty"`
}

type snapshotCandidate struct {
	id       authority.UUID
	revision uint64
	kind     authority.EntityKind
	view     *authority.EntityView
}

// revisionAtSequence resolves a contiguous immutable revision domain using at
// most 63 primary-key point probes, independent of retained history length.
// Revision zero is the sentinel for an entity created after the sequence.
func revisionAtSequence(ctx context.Context, latest, fence uint64, sequence func(uint64) (uint64, error)) (uint64, error) {
	if latest < 1 || latest > math.MaxInt64 || fence > math.MaxInt64 {
		return 0, Error{Corrupt}
	}
	low, high := uint64(0), latest
	for low < high {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		mid := low + (high-low+1)/2
		at, err := sequence(mid)
		if err != nil {
			return 0, err
		}
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		if at < 1 || at > math.MaxInt64 {
			return 0, Error{Corrupt}
		}
		if at <= fence {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return low, nil
}

// Snapshot captures tracker and Git citations at one sequence. It never starts
// repair, writes an index, changes authority or follows the current Git HEAD.
// Tombstones are included so a derived owner can remove previously live entries.
func (s *Store) Snapshot(ctx context.Context, req SnapshotRequest) (SnapshotPage, error) {
	zero := SnapshotPage{}
	if !req.ProjectID.Valid() || req.Kind != "" && !req.Kind.Valid() || req.Limit < 1 || req.Limit > 100 || len(req.Cursor) > 512 {
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
	c := snapshotCursor{ProjectID: req.ProjectID, Sequence: uint64(current), Kind: req.Kind}
	if req.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(req.Cursor)
		if err != nil || authority.DecodeJSON(data, &c) != nil || c.ProjectID != req.ProjectID || c.Kind != req.Kind || c.Sequence > uint64(current) || c.Sequence > math.MaxInt64 || !c.Last.Valid() {
			return zero, authority.Error{Code: authority.InvalidContract}
		}
	}
	// Bound physical candidates BEFORE applying the historical fence. Sparse
	// old snapshots must progress through empty pages instead of scanning all
	// later-created entities under this authority lock. One lookahead establishes
	// continuation; only Limit candidates receive bounded history point probes.
	rows, err := tx.QueryContext(ctx, `SELECT entity_id,kind,alias_number,current_revision FROM entities
	 WHERE entity_id>? ORDER BY entity_id LIMIT ?`, c.Last, req.Limit+1)
	if err != nil {
		return zero, storageError(ctx)
	}
	candidates := make([]snapshotCandidate, 0, req.Limit)
	more := false
	for rows.Next() {
		var id string
		var kind authority.EntityKind
		var alias, revision int64
		if rows.Scan(&id, &kind, &alias, &revision) != nil || !authority.UUID(id).Valid() || !kind.Valid() || alias < 1 || revision < 1 || revision > current {
			_ = rows.Close()
			return zero, Error{Corrupt}
		}
		if len(candidates) == req.Limit {
			more = true
			break
		}
		candidates = append(candidates, snapshotCandidate{id: authority.UUID(id), revision: uint64(revision), kind: kind})
	}
	readErr := rows.Err()
	_ = rows.Close()
	if readErr != nil {
		return zero, storageError(ctx)
	}
	for i := range candidates {
		candidate := &candidates[i]
		// Kind is immutable. Skip excluded physical candidates BEFORE history
		// lookup/Git hydration, while retaining the same physical scan budget.
		if req.Kind != "" && candidate.kind != req.Kind {
			continue
		}
		revision, err := revisionAtSequence(ctx, candidate.revision, c.Sequence, func(revision uint64) (uint64, error) {
			var sequence int64
			err := tx.QueryRowContext(ctx, "SELECT sequence FROM revisions WHERE entity_id=? AND revision=?", candidate.id, int64(revision)).Scan(&sequence)
			if errors.Is(err, sql.ErrNoRows) || err == nil && (sequence < 1 || sequence > current) {
				return 0, Error{Corrupt}
			}
			if err != nil {
				return 0, storageError(ctx)
			}
			return uint64(sequence), nil
		})
		if err != nil {
			return zero, err
		}
		if revision == 0 {
			continue
		}
		v, err := scanView(tx.QueryRowContext(ctx, "SELECT "+revisionColumns+` FROM entities e JOIN revisions r ON r.entity_id=e.entity_id
		 WHERE e.entity_id=? AND r.revision=?`, candidate.id, int64(revision)), req.ProjectID)
		if err != nil {
			if ctx.Err() != nil {
				return zero, ctx.Err()
			}
			return zero, err
		}
		if v.ProjectSequence > c.Sequence {
			return zero, Error{Corrupt}
		}
		candidate.view = &v
	}
	if tx.Commit() != nil {
		return zero, storageError(ctx)
	}
	page := SnapshotPage{ProjectID: req.ProjectID, Sequence: c.Sequence, Scanned: len(candidates), Entities: []SnapshotEntity{}}
	var knowledge *knowledgeRepository
	for _, candidate := range candidates {
		if candidate.view == nil {
			c.Last = candidate.id
			continue
		}
		v := *candidate.view
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
		if len(data) > authority.MaxProtectedResultBytes-1024 {
			if len(page.Entities) == 0 {
				return zero, Error{Capacity}
			}
			more = true
			break
		}
		page = candidate
		c.Last = v.Citation.EntityID
	}
	if more {
		data, _ := json.Marshal(c)
		page.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	return page, nil
}
