package localauthority

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"gitcode-mcp/internal/authority"
)

// Actor is asserted local attribution, not authenticated remote identity.
// It appears only in protected revision metadata; audit carries its hash.
type Actor struct{ ID string }

func (a Actor) valid() bool {
	if !utf8.ValidString(a.ID) || len(a.ID) < 1 || len(a.ID) > 128 || strings.TrimSpace(a.ID) != a.ID {
		return false
	}
	for _, r := range a.ID {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}

func hash(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

func canonicalPayload(m authority.MutationRequest) (json.RawMessage, error) {
	if err := m.Validate(); err != nil {
		return nil, err
	}
	var target any
	switch strings.Split(m.Operation, ".")[0] {
	case "issue":
		target = &authority.IssuePayload{}
	case "comment":
		target = &authority.CommentPayload{}
	case "label":
		target = &authority.LabelPayload{}
	case "milestone":
		target = &authority.MilestonePayload{}
	case "link":
		if m.Operation == "link.tombstone" {
			target = &struct{}{}
		} else {
			target = &authority.LinkPayload{}
		}
	default:
		return nil, authority.Error{Code: authority.Unsupported}
	}
	if authority.DecodeJSON(m.Payload, target) != nil {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	data, err := json.Marshal(target)
	if err != nil {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	// Reserve the response/envelope budget, so one accepted entity always fits
	// a protected canonical read. No truncated bytes or silently dropped entity.
	if len(data) > authority.MaxContractBytes-8192 {
		return nil, Error{Capacity}
	}
	return data, nil
}

func (s *Store) Mutate(ctx context.Context, m authority.MutationRequest, actor Actor) (authority.MutationReceipt, error) {
	var zero authority.MutationReceipt
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if !actor.valid() {
		return zero, authority.Error{Code: authority.InvalidContract}
	}
	payload, err := canonicalPayload(m)
	if err != nil {
		return zero, err
	}
	encoded, err := json.Marshal(m)
	if err != nil || len(encoded) > authority.MaxContractBytes {
		return zero, authority.Error{Code: authority.InvalidContract}
	}
	keyHash := hash([]byte(string(m.Intent.ProjectID) + "\x00" + m.Intent.IdempotencyKey))
	m.Payload = payload
	intentHash := mutationFingerprint(m, actor.ID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if s.closed || s.lease == nil {
		return zero, Error{ReadOnly}
	}
	if m.Intent.ProjectID != s.manifest.ID {
		return zero, authority.Error{Code: authority.NoAuthority}
	}
	if s.logicalFenced {
		return zero, Error{Corrupt}
	}
	if err = s.verifyPhysicalBinding(); err != nil {
		s.fenced = true
		return zero, err
	}
	if err = verifyWriter(ctx, s.db); err != nil {
		return zero, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, storageError(ctx)
	}
	defer tx.Rollback()
	if s.schema == 2 {
		if err = checkKnowledgeSetup(ctx, tx, s.manifest.ID, s.instance, s.directory); err != nil {
			var e Error
			if errors.As(err, &e) && e.Code == Corrupt {
				s.fenced, s.logicalFenced = true, true
			}
			return zero, err
		}
		if err = checkReservedAliases(ctx, tx); err == nil {
			err = checkDocumentOperations(ctx, tx, s.manifest.ID, s.instance, s.directory)
		}
		if err != nil {
			return zero, s.documentSQLError(err)
		}
		var used int
		if tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM knowledge_setup WHERE key_hash=?)+(SELECT count(*) FROM document_operations WHERE key_hash=?)`, keyHash, keyHash).Scan(&used) != nil {
			return zero, storageError(ctx)
		}
		if used != 0 {
			return zero, authority.Error{Code: authority.Conflict}
		}
	}
	original, receiptErr := loadReceipt(ctx, tx, keyHash, s.manifest.ID)
	err = receiptErr
	if err == nil {
		if original.IntentHash != intentHash {
			return zero, authority.Error{Code: authority.Conflict}
		}
		if err = s.publishBarrier(); err != nil {
			s.fenced = true
			return zero, err
		}
		s.fenced = false
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		original.Replayed = true
		return original, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return zero, err
	}
	if s.fenced {
		return zero, Error{ReadOnly}
	}
	parts := strings.Split(m.Operation, ".")
	kind := authority.EntityKind(parts[0])
	create := parts[1] == "create"
	id := m.Intent.EntityID
	var revision, alias, sequence, generation int64
	if tx.QueryRowContext(ctx, "SELECT sequence,generation FROM project WHERE singleton=1").Scan(&sequence, &generation) != nil {
		return zero, storageError(ctx)
	}
	if sequence == math.MaxInt64 || generation == math.MaxInt64 {
		return zero, Error{Capacity}
	}
	sequence++
	generation++
	if create {
		id, err = authority.NewUUID()
		if err != nil {
			return zero, err
		}
		revision = 1
		if tx.QueryRowContext(ctx, "SELECT next_number FROM aliases WHERE kind=?", kind).Scan(&alias) != nil {
			return zero, storageError(ctx)
		}
		if alias == math.MaxInt64 {
			return zero, Error{Capacity}
		}
		if _, err = tx.ExecContext(ctx, "UPDATE aliases SET next_number=next_number+1 WHERE kind=?", kind); err != nil {
			return zero, storageError(ctx)
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO entities VALUES(?,?,?,?)", id, kind, alias, revision); err != nil {
			return zero, storageError(ctx)
		}
	} else {
		var storedKind string
		var tombstone bool
		err = tx.QueryRowContext(ctx, "SELECT e.kind,e.alias_number,e.current_revision,r.tombstone FROM entities e JOIN revisions r ON r.entity_id=e.entity_id AND r.revision=e.current_revision WHERE e.entity_id=?", id).Scan(&storedKind, &alias, &revision, &tombstone)
		if errors.Is(err, sql.ErrNoRows) {
			return zero, authority.Error{Code: authority.NoAuthority}
		}
		if err != nil {
			return zero, storageError(ctx)
		}
		if storedKind != string(kind) {
			return zero, authority.Error{Code: authority.InvalidContract}
		}
		if m.Intent.ExpectedRevision > math.MaxInt64 || uint64(revision) != m.Intent.ExpectedRevision {
			return zero, authority.Error{Code: authority.RevisionConflict}
		}
		if tombstone {
			return zero, authority.Error{Code: authority.Conflict}
		}
		if revision == math.MaxInt64 {
			return zero, Error{Capacity}
		}
		revision++
	}
	if err = validateRelationships(ctx, tx, id, m, create); err != nil {
		return zero, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tombstone := parts[1] == "tombstone"
	if _, err = tx.ExecContext(ctx, "INSERT INTO revisions VALUES(?,?,?,?,?,?,?,?)", id, revision, sequence, string(payload), hash(payload), tombstone, actor.ID, now); err != nil {
		return zero, storageError(ctx)
	}
	if err = s.inject("revision"); err != nil {
		return zero, err
	}
	if !create {
		result, e := tx.ExecContext(ctx, "UPDATE entities SET current_revision=? WHERE entity_id=? AND current_revision=?", revision, id, revision-1)
		if e != nil {
			return zero, storageError(ctx)
		}
		n, e := result.RowsAffected()
		if e != nil || n != 1 {
			return zero, authority.Error{Code: authority.RevisionConflict}
		}
	}
	if kind == authority.Link {
		if _, err = tx.ExecContext(ctx, "DELETE FROM relations WHERE link_id=?", id); err != nil {
			return zero, storageError(ctx)
		}
		if !tombstone {
			var p authority.LinkPayload
			_ = json.Unmarshal(payload, &p)
			if _, err = tx.ExecContext(ctx, "INSERT INTO relations VALUES(?,?,?,?,?,?)", id, p.From.EntityID, p.From.Revision, p.To.EntityID, p.To.Revision, p.Relation); err != nil {
				return zero, storageError(ctx)
			}
		}
	}
	if err = s.inject("relations"); err != nil {
		return zero, err
	}
	op, err := authority.NewUUID()
	if err != nil {
		return zero, err
	}
	r := authority.MutationReceipt{ProjectID: s.manifest.ID, OperationID: op, Citation: authority.Citation{ProjectID: s.manifest.ID, EntityID: id, Kind: kind, Revision: uint64(revision)}, ProjectSequence: uint64(sequence), IdempotencyKeyHash: keyHash, IntentHash: intentHash, Evidence: "local_commit_confirmed", ProjectionState: "pending"}
	data, err := json.Marshal(r)
	if err != nil {
		return zero, storageError(ctx)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO receipts VALUES(?,?,?,?,?)", keyHash, intentHash, op, sequence, string(data)); err != nil {
		return zero, storageError(ctx)
	}
	if err = s.inject("receipt"); err != nil {
		return zero, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO audit VALUES(?,?,?,?,?,?,?,?,?)", sequence, op, id, revision, m.Operation, keyHash, intentHash, hash([]byte(string(s.manifest.ID)+"\x00"+actor.ID)), now); err != nil {
		return zero, storageError(ctx)
	}
	if _, err = tx.ExecContext(ctx, "UPDATE project SET sequence=?,generation=? WHERE singleton=1", sequence, generation); err != nil {
		return zero, storageError(ctx)
	}
	if err = s.inject("generation"); err != nil {
		return zero, err
	}
	captureCanonical(ctx, s.manifest.ID, false, false)
	if err = tx.Commit(); err != nil {
		s.fenced = true
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		return zero, Error{CommitUncertain}
	}
	if err = s.publishBarrier(); err != nil {
		s.fenced = true
		return zero, err
	}
	captureCanonical(ctx, s.manifest.ID, false, true)
	// Loss of caller acknowledgement after commit does not roll back. The same
	// key recovers the settled original receipt without another mutation.
	if err = s.inject("acknowledgement"); err != nil {
		return zero, err
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	return r, nil
}

func mutationFingerprint(m authority.MutationRequest, actor string) string {
	m.Intent.IdempotencyKey = ""
	canonical, _ := json.Marshal(struct {
		Request authority.MutationRequest
		Actor   string
	}{m, actor})
	return hash(canonical)
}

func (s *Store) inject(phase string) error {
	if s.fault != nil {
		return s.fault(phase)
	}
	return nil
}
