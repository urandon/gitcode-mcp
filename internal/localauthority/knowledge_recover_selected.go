package localauthority

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"

	"gitcode-mcp/internal/authority"
)

// recoverySelection is internal canonical-journal selection, not a public
// request or permission derived from caller paths/OIDs/keys/document bytes.
// The later durable job owner must separately gate explicit maintenance intent.
type recoverySelection struct {
	project, operation authority.UUID
	intent             string
	generation         int
	readCheckpoint     func(string) // test-only; no transport/config constructor
}

// recoverSelectedDocument advances at most ONE original journal operation.
// It never guesses missing source bytes, reserves a new operation, initializes
// Git/SQL, or changes identity/actor/audit provenance. The daemon job owner is
// still required for public admission, persistent queue and lifecycle behavior.
func (s *Store) recoverSelectedDocument(ctx context.Context, selected recoverySelection) (authority.DocumentMutationResult, error) {
	var zero authority.DocumentMutationResult
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if !selected.project.Valid() || !selected.operation.Valid() || !validDigest(selected.intent) || selected.generation < 1 || selected.generation > 3 {
		return zero, authority.Error{Code: authority.InvalidContract}
	}
	if !s.mu.TryLock() {
		return zero, authority.Error{Code: authority.Conflict}
	}
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, CoordinatorCallBudget)
	defer cancel()
	if s.closed || s.lease == nil {
		return zero, Error{ReadOnly}
	}
	if selected.project != s.manifest.ID {
		return zero, authority.Error{Code: authority.NoAuthority}
	}
	if s.schema == 1 {
		return zero, Error{KnowledgeMigrationRequired}
	}
	if s.logicalFenced {
		return zero, Error{Corrupt}
	}
	if err := s.verifyPhysicalBinding(); err != nil {
		s.fenced = true
		return zero, err
	}
	if err := verifyWriter(ctx, s.db); err != nil {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		return zero, err
	}
	o, err := scanDocumentOperation(ctx, s.db.QueryRowContext(ctx, documentOperationQuery+" WHERE operation_id=?", selected.operation), selected.project)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, authority.Error{Code: authority.NoAuthority}
	}
	if err != nil {
		return zero, s.documentSQLError(err)
	}
	if o.Intent != selected.intent || selected.generation > o.Generation {
		return zero, authority.Error{Code: authority.Conflict}
	}
	if err := s.verifyIdentity(ctx, selected.project); err != nil {
		if ctx.Err() != nil {
			// Interrupted SQL inspection is not proof of logical corruption.
			return zero, ctx.Err()
		}
		return zero, s.documentSQLError(err)
	}
	k, err := loadKnowledgeSetup(ctx, s.db, selected.project, s.instance, s.directory)
	if err != nil {
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		return zero, s.documentSQLError(err)
	}
	if k.Phase != "ready" {
		return zero, Error{KnowledgeInitializationPending}
	}
	g := s.knowledgeRepository(k).withContext(ctx)
	g.readCheckpoint = selected.readCheckpoint
	if err := qualifyFilesystem(g.directory); err != nil {
		return zero, err
	}
	if o.Phase == "settled" {
		if s.fenced {
			// A stored receipt after an ambiguous publication barrier is not
			// a new durability acknowledgement. Proof-only replay cannot lift
			// that fence; verified owner reopen/original retry must do so.
			return zero, Error{CommitUncertain}
		}
		// Proof-only replay: no publishBarrier, writeObject, publishRef, pin
		// cleanup or SQL mutation. A newer operation cannot redirect this one.
		if err := g.verifyDocumentEvidence(o); err != nil {
			return zero, err
		}
		if err := s.verifyDocumentHistory(ctx, g, o.commitOID()); err != nil {
			return zero, err
		}
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		result := *o.Result
		result.Receipt.Replayed = true
		return result, nil
	}
	if _, _, err := g.verifyJournalLayout(&o); err != nil {
		return zero, err
	}
	if err := s.verifyDocumentInventory(ctx, g); err != nil {
		return zero, err
	}
	prior, err := s.verifySettledKnowledgeHistory(ctx, g)
	if err != nil {
		return zero, err
	}
	if prior != o.Metadata.ExpectedRefOID {
		return zero, Error{DocumentRecoveryRequired}
	}
	pin, err := g.readRef(filepath.Join("pins", string(o.ID)))
	if err != nil {
		return zero, err
	}
	if pin != o.commitOID() {
		// Only a published exact pin establishes the recovery entry point.
		// A reservation, object collection, or owned pin-lock alone is not it.
		return zero, Error{DocumentRecoveryRequired}
	}
	if err := g.verifyDocumentEvidence(o); err != nil {
		return zero, err
	}
	// Requalify an earlier ambiguous SQL commit only after original selection
	// and candidate proof; never clear logical corruption through a barrier.
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if err := s.publishBarrier(); err != nil {
		s.fenced = true
		return zero, err
	}
	s.fenced = false
	if o.Phase == "reserved" {
		if err := g.syncDocumentEvidence(o); err != nil {
			return zero, err
		}
		if err := g.publishJournalRef(o, false); err != nil {
			return zero, err
		}
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		if err := s.bindDocument(ctx, o); err != nil {
			return zero, err
		}
		o.Phase, o.Generation = "bound", 2
	}
	result, err := s.completeBoundDocument(ctx, g, o)
	if err != nil {
		return zero, err
	}
	result.Receipt.Replayed = true
	return result, nil
}
