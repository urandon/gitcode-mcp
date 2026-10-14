package localauthority

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"gitcode-mcp/internal/authority"
)

const revisionColumns = `e.entity_id,e.kind,e.alias_number,r.revision,r.sequence,r.tombstone,
 CASE WHEN length(CAST(r.payload AS BLOB))<=524288 THEN r.payload END,
 CASE WHEN length(r.payload_hash)=64 THEN r.payload_hash END`

type scanner interface{ Scan(...any) error }

func scanView(row scanner, project authority.UUID) (authority.EntityView, error) {
	var v authority.EntityView
	var id, kind, payload, payloadHash string
	var alias, revision, sequence int64
	if row.Scan(&id, &kind, &alias, &revision, &sequence, &v.Tombstone, &payload, &payloadHash) != nil {
		return v, Error{Corrupt}
	}
	if alias < 1 || revision < 1 || sequence < 1 || hash([]byte(payload)) != payloadHash {
		return v, Error{Corrupt}
	}
	prefix := map[authority.EntityKind]string{authority.Issue: "ISSUE", authority.Comment: "COMMENT", authority.Label: "LABEL", authority.Milestone: "MILESTONE", authority.Document: "DOC", authority.Link: "LINK"}
	v.Citation = authority.Citation{ProjectID: project, EntityID: authority.UUID(id), Kind: authority.EntityKind(kind), Revision: uint64(revision)}
	v.DisplayAlias = fmt.Sprintf("%s-%d", prefix[v.Citation.Kind], alias)
	v.ProjectSequence = uint64(sequence)
	v.Payload = json.RawMessage(payload)
	if v.Citation.Kind == authority.Document {
		metadata, err := decodeDocumentMetadata(v.Payload)
		if err != nil || metadata.citation() != v.Citation || v.Tombstone != (metadata.Result.State == "tombstone") || v.Citation.Validate() != nil {
			return authority.EntityView{}, Error{Corrupt}
		}
		return v, nil // internal metadata view; public reads hydrate exact Git bytes.
	}
	if v.Validate() != nil {
		return authority.EntityView{}, Error{Corrupt}
	}
	return v, nil
}

type pageCursor struct {
	Project   authority.UUID       `json:"project"`
	Kind      authority.EntityKind `json:"kind"`
	Operation string               `json:"operation"`
	Entity    authority.UUID       `json:"entity"`
	Snapshot  int64                `json:"snapshot"`
	Last      int64                `json:"last"`
}

func (s *Store) Read(ctx context.Context, r authority.ReadRequest) (authority.ReadResult, error) {
	zero := authority.ReadResult{}
	if r.Validate() != nil || r.Revision > math.MaxInt64 {
		return zero, authority.Error{Code: authority.InvalidContract}
	}
	if r.Operation == "status" {
		return zero, authority.Error{Code: authority.Unsupported}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return zero, Error{ReadOnly}
	}
	if r.ProjectID != s.manifest.ID {
		return zero, authority.Error{Code: authority.NoAuthority}
	}
	var knowledge *knowledgeRepository
	if r.Kind == authority.Document {
		if s.schema == 1 {
			return zero, Error{KnowledgeMigrationRequired}
		}
		g, err := s.readyKnowledgeRepository(ctx)
		if err != nil {
			return zero, err
		}
		knowledge = &g
	}
	result := authority.ReadResult{ProjectID: r.ProjectID, Entities: []authority.EntityView{}}
	if r.Operation == "get" {
		query := "SELECT " + revisionColumns + " FROM entities e JOIN revisions r ON r.entity_id=e.entity_id AND r.revision=e.current_revision WHERE e.entity_id=? AND e.kind=?"
		args := []any{r.EntityID, r.Kind}
		if r.Revision != 0 {
			query = "SELECT " + revisionColumns + " FROM entities e JOIN revisions r ON r.entity_id=e.entity_id WHERE e.entity_id=? AND e.kind=? AND r.revision=?"
			args = append(args, r.Revision)
		}
		row := s.db.QueryRowContext(ctx, query, args...)
		v, err := scanView(row, r.ProjectID)
		// scanView deliberately hides driver details. Check existence separately
		// to distinguish a missing citation from malformed persisted data.
		if err != nil {
			var count int
			q := "SELECT count(*) FROM entities e JOIN revisions r ON r.entity_id=e.entity_id WHERE e.entity_id=? AND e.kind=?"
			a := []any{r.EntityID, r.Kind}
			if r.Revision != 0 {
				q += " AND r.revision=?"
				a = append(a, r.Revision)
			}
			if s.db.QueryRowContext(ctx, q, a...).Scan(&count) != nil {
				return zero, storageError(ctx)
			}
			if count == 0 {
				return zero, authority.Error{Code: authority.NoAuthority}
			}
			return zero, err
		}
		if knowledge != nil {
			v, err = hydrateDocument(*knowledge, v)
			if err != nil {
				return zero, err
			}
		}
		result.Entities = append(result.Entities, v)
		encoded, err := json.Marshal(result)
		if err != nil || len(encoded) > authority.MaxProtectedResultBytes {
			return zero, Error{Capacity}
		}
		return result, nil
	}
	var current int64
	if s.db.QueryRowContext(ctx, "SELECT sequence FROM project WHERE singleton=1").Scan(&current) != nil {
		return zero, storageError(ctx)
	}
	c := pageCursor{Project: r.ProjectID, Kind: r.Kind, Operation: r.Operation, Entity: r.EntityID, Snapshot: current}
	if r.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(r.Cursor)
		if err != nil || authority.DecodeJSON(data, &c) != nil || c.Project != r.ProjectID || c.Kind != r.Kind || c.Operation != r.Operation || c.Entity != r.EntityID || c.Snapshot < 0 || c.Snapshot > current || c.Last < 0 || c.Last > c.Snapshot {
			return zero, authority.Error{Code: authority.InvalidContract}
		}
	}
	limit := r.Limit
	if limit == 0 {
		limit = 50
	}
	query := "SELECT " + revisionColumns + ",e.alias_number FROM entities e JOIN revisions r ON r.entity_id=e.entity_id WHERE e.kind=? AND e.alias_number>? AND r.revision=(SELECT max(v.revision) FROM revisions v WHERE v.entity_id=e.entity_id AND v.sequence<=?) ORDER BY e.alias_number LIMIT ?"
	args := []any{r.Kind, c.Last, c.Snapshot, limit + 1}
	if r.Operation == "history" {
		var count int
		if s.db.QueryRowContext(ctx, "SELECT count(*) FROM entities WHERE entity_id=? AND kind=?", r.EntityID, r.Kind).Scan(&count) != nil {
			return zero, storageError(ctx)
		}
		if count == 0 {
			return zero, authority.Error{Code: authority.NoAuthority}
		}
		query = "SELECT " + revisionColumns + ",r.revision FROM entities e JOIN revisions r ON r.entity_id=e.entity_id WHERE e.kind=? AND e.entity_id=? AND r.revision>? AND r.sequence<=? ORDER BY r.revision LIMIT ?"
		args = []any{r.Kind, r.EntityID, c.Last, c.Snapshot, limit + 1}
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return zero, storageError(ctx)
	}
	defer rows.Close()
	for rows.Next() {
		var last int64
		v, err := scanView(extraScanner{row: rows, last: &last}, r.ProjectID)
		if err != nil {
			return zero, err
		}
		if knowledge != nil {
			v, err = hydrateDocument(*knowledge, v)
			if err != nil {
				return zero, err
			}
		}
		candidate := result
		candidate.Entities = append(append([]authority.EntityView{}, result.Entities...), v)
		data, err := json.Marshal(candidate)
		if err != nil {
			return zero, Error{Corrupt}
		}
		if len(result.Entities) >= limit || len(data) > authority.MaxProtectedResultBytes-1024 {
			if len(result.Entities) == 0 {
				return zero, Error{Capacity}
			}
			encoded, _ := json.Marshal(c)
			result.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
			break
		}
		result = candidate
		c.Last = last
	}
	if rows.Err() != nil {
		return zero, storageError(ctx)
	}
	return result, nil
}

type extraScanner struct {
	row  scanner
	last *int64
}

func (s extraScanner) Scan(values ...any) error { return s.row.Scan(append(values, s.last)...) }

func (s *Store) Status(ctx context.Context) (authority.ProjectStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked(ctx)
}

// Caller owns Store.mu; no filesystem or identity qualification is implied.
func (s *Store) statusLocked(ctx context.Context) (authority.ProjectStatus, error) {
	if s.closed {
		return authority.ProjectStatus{}, Error{ReadOnly}
	}
	var generation, sequence int64
	if s.db.QueryRowContext(ctx, "SELECT generation,sequence FROM project WHERE singleton=1").Scan(&generation, &sequence) != nil || generation < 1 || sequence < 0 {
		return authority.ProjectStatus{}, storageError(ctx)
	}
	state, writer := "ready", "owned"
	if s.lease == nil {
		state = "read_only"
		writer = "unavailable"
	} else if s.fenced {
		state = "recovery_required"
		writer = "quiesced"
	}
	// NetworkState describes manifest policy, not process isolation. Runtime
	// qualification and actual projection coverage remain the L4 cohort.
	return authority.ProjectStatus{ProjectID: s.manifest.ID, Authority: authority.Local, Readiness: state, Generation: uint64(generation), NetworkState: s.manifest.Network.Label(), WriterState: writer, AuthorityGeneration: uint64(sequence)}, nil
}

func (s *Store) Receipt(ctx context.Context, key string) (authority.MutationReceipt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var zero authority.MutationReceipt
	if s.closed {
		return zero, Error{ReadOnly}
	}
	if (authority.WriteIntent{ProjectID: s.manifest.ID, WriteMode: "local", IdempotencyKey: key}).Validate(true) != nil {
		return zero, authority.Error{Code: authority.InvalidContract}
	}
	r, err := loadReceipt(ctx, s.db, hash([]byte(string(s.manifest.ID)+"\x00"+key)), s.manifest.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, authority.Error{Code: authority.NoAuthority}
	}
	if err != nil {
		return zero, err
	}
	return r, nil
}

// RevisionMetadata is intentional protected project content, not telemetry.
type RevisionMetadata struct {
	Citation   authority.Citation `json:"citation"`
	ActorID    string             `json:"actor_id"`
	RecordedAt string             `json:"recorded_at"`
}

func (s *Store) Metadata(ctx context.Context, c authority.Citation) (RevisionMetadata, error) {
	var result RevisionMetadata
	if c.Validate() != nil || c.Revision > math.MaxInt64 {
		return result, authority.Error{Code: authority.InvalidContract}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return result, Error{ReadOnly}
	}
	if c.ProjectID != s.manifest.ID {
		return result, authority.Error{Code: authority.NoAuthority}
	}
	err := s.db.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(r.actor_id AS BLOB))<=128 THEN r.actor_id END,CASE WHEN length(r.recorded_at)<=64 THEN r.recorded_at END
 FROM entities e JOIN revisions r ON r.entity_id=e.entity_id WHERE e.entity_id=? AND e.kind=? AND r.revision=?`, c.EntityID, c.Kind, c.Revision).Scan(&result.ActorID, &result.RecordedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return result, authority.Error{Code: authority.NoAuthority}
	}
	if err != nil {
		return result, storageError(ctx)
	}
	result.Citation = c
	if !(Actor{ID: result.ActorID}).valid() {
		return RevisionMetadata{}, Error{Corrupt}
	}
	if _, err := time.Parse(time.RFC3339Nano, result.RecordedAt); err != nil {
		return RevisionMetadata{}, Error{Corrupt}
	}
	return result, nil
}
