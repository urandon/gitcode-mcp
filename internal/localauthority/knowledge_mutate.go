package localauthority

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"time"

	"gitcode-mcp/internal/authority"
)

// MutateDocument is explicit trusted local service admission. It is not the
// legacy mutation envelope and does not advertise a transport capability.
// Only content-free metadata enters SQL; candidate bytes remain transient/Git.
func (s *Store) MutateDocument(ctx context.Context, request authority.DocumentMutationRequest, actor Actor) (authority.DocumentMutationResult, error) {
	var zero authority.DocumentMutationResult
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	intent, err := documentFingerprint(request, actor)
	if err != nil {
		return zero, err
	}
	encoded, err := json.Marshal(request)
	if err != nil || len(encoded) > authority.MaxContractBytes {
		return zero, authority.Error{Code: authority.InvalidContract}
	}
	var payload authority.DocumentPayload
	if request.Operation != "document.tombstone" {
		if authority.DecodeJSON(request.Payload, &payload) != nil {
			return zero, authority.Error{Code: authority.InvalidContract}
		}
		canonical, _ := json.Marshal(payload)
		if len(canonical) > authority.MaxContractBytes-8192 {
			return zero, Error{Capacity}
		}
	}
	key := hash([]byte(string(request.Intent.ProjectID) + "\x00" + request.Intent.IdempotencyKey))
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if s.closed || s.lease == nil {
		return zero, Error{ReadOnly}
	}
	if request.Intent.ProjectID != s.manifest.ID {
		return zero, authority.Error{Code: authority.NoAuthority}
	}
	if s.schema == 1 {
		return zero, Error{KnowledgeMigrationRequired}
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
	o, err := loadDocumentOperation(ctx, s.db, s.manifest.ID, key)
	replay := err == nil
	if replay && o.Intent != intent {
		return zero, authority.Error{Code: authority.Conflict}
	}
	if !replay && !errors.Is(err, sql.ErrNoRows) {
		return zero, s.documentSQLError(err)
	}
	if err = s.verifyIdentity(ctx, s.manifest.ID); err != nil {
		return zero, s.documentSQLError(err)
	}
	if !replay && s.fenced {
		return zero, Error{ReadOnly}
	}
	if replay {
		// Requalify the global SQL writer after an ambiguous earlier commit;
		// logical corruption is never cleared by an unrelated saved result.
		if err = s.publishBarrier(); err != nil {
			s.fenced = true
			return zero, err
		}
		s.fenced = false
	}
	if !replay {
		var occupied int
		if s.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM receipts WHERE key_hash=?)+(SELECT count(*) FROM knowledge_setup WHERE key_hash=?)`, key, key).Scan(&occupied) != nil {
			return zero, storageError(ctx)
		}
		if occupied != 0 {
			return zero, authority.Error{Code: authority.Conflict}
		}
		if s.db.QueryRowContext(ctx, `SELECT count(*) FROM document_operations WHERE phase<>'settled'`).Scan(&occupied) != nil {
			return zero, storageError(ctx)
		}
		if occupied != 0 {
			return zero, Error{DocumentPending}
		}
	}
	k, err := loadKnowledgeSetup(ctx, s.db, s.manifest.ID, s.instance, s.directory)
	if errors.Is(err, sql.ErrNoRows) {
		return zero, Error{KnowledgeSetupRequired}
	}
	if err != nil {
		return zero, s.documentSQLError(err)
	}
	if k.Phase != "ready" {
		return zero, Error{KnowledgeInitializationPending}
	}
	g := s.knowledgeRepository(k)
	if !replay {
		if err = qualifyFilesystem(g.directory); err != nil {
			return zero, err
		}
		objects, pins, err := g.verifyLayout()
		if err != nil {
			return zero, err
		}
		if err = s.verifyDocumentInventory(ctx, g); err != nil {
			return zero, err
		}
		current, err := s.verifySettledHead(ctx, g)
		if err != nil {
			return zero, err
		}
		if current != request.Intent.ExpectedRefOID {
			return zero, Error{RefConflict}
		}
		if lock, err := g.readRef(filepath.Join("heads", "knowledge.lock")); err != nil || lock != authority.EmptyKnowledgeRef {
			return zero, Error{DocumentRecoveryRequired}
		}
		entity := request.Intent.EntityID
		var revision, alias int64
		if request.Operation == "document.create" {
			entity, err = authority.NewUUID()
			if err != nil {
				return zero, err
			}
			revision = 1
			if s.db.QueryRowContext(ctx, `SELECT next_number FROM aliases WHERE kind='document'`).Scan(&alias) != nil {
				return zero, storageError(ctx)
			}
			if alias == math.MaxInt64 {
				return zero, Error{Capacity}
			}
		} else {
			var tombstone bool
			err = s.db.QueryRowContext(ctx, `SELECT e.current_revision,e.alias_number,r.tombstone FROM entities e JOIN revisions r ON r.entity_id=e.entity_id AND r.revision=e.current_revision WHERE e.entity_id=? AND e.kind='document'`, entity).Scan(&revision, &alias, &tombstone)
			if errors.Is(err, sql.ErrNoRows) {
				return zero, authority.Error{Code: authority.NoAuthority}
			}
			if err != nil {
				return zero, storageError(ctx)
			}
			if uint64(revision) != request.Intent.ExpectedRevision {
				return zero, authority.Error{Code: authority.RevisionConflict}
			}
			if tombstone {
				return zero, authority.Error{Code: authority.RevisionConflict}
			}
			if revision == math.MaxInt64 {
				return zero, Error{Capacity}
			}
			revision++
		}
		prior, err := g.treeAt(current)
		if err != nil {
			return zero, err
		}
		if err = knowledgeAdmissionCapacity(len(prior), pins, objects, request.Operation == "document.create"); err != nil {
			return zero, err
		}
		var seq, gen int64
		if s.db.QueryRowContext(ctx, `SELECT sequence,generation FROM project WHERE singleton=1`).Scan(&seq, &gen) != nil {
			return zero, storageError(ctx)
		}
		if seq == math.MaxInt64 || gen == math.MaxInt64 {
			return zero, Error{Capacity}
		}
		operation, err := authority.NewUUID()
		if err != nil {
			return zero, err
		}
		candidate, err := planKnowledgeCandidate(prior, current, operation, entity, []byte(payload.Markdown), request.Operation == "document.tombstone")
		if err != nil {
			return zero, err
		}
		metadata, err := documentMetadataForCandidate(request, entity, operation, uint64(revision), candidate)
		if err != nil {
			return zero, err
		}
		o = documentOperation{Key: key, Intent: intent, ID: operation, Entity: entity, Alias: alias, Operation: request.Operation, Metadata: metadata, Documents: candidate.documents.oid, Root: candidate.root.oid, Actor: actor, RecordedAt: time.Now().UTC().Format(time.RFC3339Nano), Phase: "reserved", Generation: 1, PinCleanup: "retained"}
		if err = s.reserveDocument(ctx, o); err != nil {
			return zero, err
		}
	}
	if err = qualifyFilesystem(g.directory); err != nil {
		return zero, err
	}
	if o.Phase == "settled" {
		return s.replayDocument(ctx, g, o)
	}
	if _, _, err = g.verifyJournalLayout(&o); err != nil {
		return zero, err
	}
	if err = s.verifyDocumentInventory(ctx, g); err != nil {
		return zero, err
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	current, err := g.readRef(filepath.Join("heads", "knowledge"))
	if err != nil {
		return zero, err
	}
	lock, err := g.readRef(filepath.Join("heads", "knowledge.lock"))
	if err != nil {
		return zero, err
	}
	if o.Phase == "reserved" && (current != o.Metadata.ExpectedRefOID || lock != authority.EmptyKnowledgeRef) || o.Phase == "bound" && (current != o.Metadata.ExpectedRefOID && current != o.commitOID() || lock != authority.EmptyKnowledgeRef && lock != o.commitOID()) {
		return zero, Error{DocumentRecoveryRequired}
	}
	if o.Phase == "reserved" {
		prior, err := g.treeAt(o.Metadata.ExpectedRefOID)
		if err != nil {
			return zero, err
		}
		candidate, err := planKnowledgeCandidate(prior, o.Metadata.ExpectedRefOID, o.ID, o.Entity, []byte(payload.Markdown), o.Operation == "document.tombstone")
		if err != nil || candidate.commit.oid != o.commitOID() || candidate.documents.oid != o.Documents || candidate.root.oid != o.Root {
			return zero, Error{DocumentRecoveryRequired}
		}
		objects := []knowledgeObject{candidate.documents, candidate.root, candidate.commit}
		if o.Operation != "document.tombstone" {
			objects = append([]knowledgeObject{candidate.blob}, objects...)
		}
		for _, object := range objects {
			if ctx.Err() != nil {
				return zero, ctx.Err()
			}
			if err = g.writeObject(object); err != nil {
				return zero, err
			}
		}
		if err = g.publishJournalRef(o, false); err != nil {
			return zero, err
		}
		if ctx.Err() != nil {
			return zero, ctx.Err()
		}
		if err = s.bindDocument(ctx, o); err != nil {
			return zero, err
		}
		o.Phase, o.Generation = "bound", 2
	}
	if err = g.verifyDocumentEvidence(o); err != nil {
		return zero, err
	}
	if pin, err := g.readRef(filepath.Join("pins", string(o.ID))); err != nil || pin != o.commitOID() {
		// SQL bound means the original pin barrier already completed. Its
		// unexplained disappearance is not permission to manufacture proof.
		return zero, Error{DocumentRecoveryRequired}
	}
	if err = g.syncDocumentEvidence(o); err != nil {
		return zero, err
	}
	if err = g.publishJournalRef(o, false); err != nil {
		return zero, err
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if err = g.publishJournalRef(o, true); err != nil {
		return zero, err
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	result, err := s.settleDocument(ctx, o)
	if err != nil {
		return zero, err
	}
	if err = s.inject("knowledge_document_acknowledgement"); err != nil {
		return zero, err
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	result.Receipt.Replayed = replay
	return result, nil
}

func knowledgeAdmissionCapacity(active, pins, objects int, create bool) error {
	if active < 0 || active > maxActiveDocuments || pins < 0 || objects < 0 {
		return Error{KnowledgeCorrupt}
	}
	if create && active == maxActiveDocuments || pins >= maxRetainedKnowledgePins || objects > maxKnowledgeLooseObjects-4 {
		return Error{Capacity}
	}
	return nil
}

func (s *Store) documentSQLError(err error) error {
	var e Error
	if errors.As(err, &e) && e.Code == Corrupt {
		s.fenced, s.logicalFenced = true, true
	}
	return err
}

func (s *Store) reserveDocument(ctx context.Context, o documentOperation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError(ctx)
	}
	defer tx.Rollback()
	metadata, _ := json.Marshal(o.Metadata)
	if o.Operation == "document.create" {
		r, err := tx.ExecContext(ctx, `UPDATE aliases SET next_number=next_number+1 WHERE kind='document' AND next_number=?`, o.Alias)
		if err != nil {
			return storageError(ctx)
		}
		if n, err := r.RowsAffected(); err != nil || n != 1 {
			return Error{Corrupt}
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO document_operations VALUES(?,?,?,?,?,?,?,?,?,?,?,1,'reserved',1,NULL,'retained')`, o.Key, o.Intent, o.ID, o.Entity, o.Alias, o.Operation, string(metadata), o.Documents, o.Root, o.Actor.ID, o.RecordedAt); err != nil {
		return storageError(ctx)
	}
	return s.commitKnowledge(ctx, tx, "knowledge_document_reserved")
}

func (s *Store) bindDocument(ctx context.Context, o documentOperation) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storageError(ctx)
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, `UPDATE document_operations SET phase='bound',generation=2 WHERE key_hash=? AND intent_hash=? AND operation_id=? AND phase='reserved' AND generation=1`, o.Key, o.Intent, o.ID)
	if err != nil {
		return storageError(ctx)
	}
	if n, err := r.RowsAffected(); err != nil || n != 1 {
		return Error{DocumentRecoveryRequired}
	}
	return s.commitKnowledge(ctx, tx, "knowledge_document_bound")
}

func (s *Store) settleDocument(ctx context.Context, o documentOperation) (authority.DocumentMutationResult, error) {
	var zero authority.DocumentMutationResult
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, storageError(ctx)
	}
	defer tx.Rollback()
	var sequence, generation int64
	if tx.QueryRowContext(ctx, `SELECT sequence,generation FROM project WHERE singleton=1`).Scan(&sequence, &generation) != nil {
		return zero, storageError(ctx)
	}
	if sequence == math.MaxInt64 || generation == math.MaxInt64 {
		return zero, Error{Capacity}
	}
	sequence++
	generation++
	citation := o.Metadata.citation()
	if o.Operation == "document.create" {
		if _, err = tx.ExecContext(ctx, `INSERT INTO entities VALUES(?,'document',?,1)`, o.Entity, o.Alias); err != nil {
			return zero, storageError(ctx)
		}
	} else {
		r, err := tx.ExecContext(ctx, `UPDATE entities SET current_revision=? WHERE entity_id=? AND kind='document' AND current_revision=?`, citation.Revision, o.Entity, citation.Revision-1)
		if err != nil {
			return zero, storageError(ctx)
		}
		if n, err := r.RowsAffected(); err != nil || n != 1 {
			return zero, Error{DocumentRecoveryRequired}
		}
	}
	metadata, _ := json.Marshal(o.Metadata)
	if _, err = tx.ExecContext(ctx, `INSERT INTO revisions VALUES(?,?,?,?,?,?,?,?)`, o.Entity, citation.Revision, sequence, string(metadata), hash(metadata), o.Metadata.Result.State == "tombstone", o.Actor.ID, o.RecordedAt); err != nil {
		return zero, storageError(ctx)
	}
	if err = s.inject("knowledge_document_revision"); err != nil {
		return zero, err
	}
	r := authority.DocumentMutationResult{Schema: 1, Durability: authority.OrderedOSBarriersV1, Result: o.Metadata.Result, Receipt: authority.MutationReceipt{ProjectID: s.manifest.ID, OperationID: o.ID, Citation: citation, ProjectSequence: uint64(sequence), IdempotencyKeyHash: o.Key, IntentHash: o.Intent, Evidence: "local_commit_confirmed", ProjectionState: "pending"}}
	if r.Validate() != nil {
		return zero, Error{Corrupt}
	}
	receipt, _ := json.Marshal(r.Receipt)
	result, _ := json.Marshal(r)
	if _, err = tx.ExecContext(ctx, `INSERT INTO receipts VALUES(?,?,?,?,?)`, o.Key, o.Intent, o.ID, sequence, string(receipt)); err != nil {
		return zero, storageError(ctx)
	}
	if err = s.inject("knowledge_document_receipt"); err != nil {
		return zero, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO audit VALUES(?,?,?,?,?,?,?,?,?)`, sequence, o.ID, o.Entity, citation.Revision, o.Operation, o.Key, o.Intent, hash([]byte(string(s.manifest.ID)+"\x00"+o.Actor.ID)), o.RecordedAt); err != nil {
		return zero, storageError(ctx)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE project SET sequence=?,generation=? WHERE singleton=1`, sequence, generation); err != nil {
		return zero, storageError(ctx)
	}
	changed, err := tx.ExecContext(ctx, `UPDATE document_operations SET phase='settled',generation=3,result=? WHERE key_hash=? AND intent_hash=? AND operation_id=? AND phase='bound' AND generation=2`, string(result), o.Key, o.Intent, o.ID)
	if err != nil {
		return zero, storageError(ctx)
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, Error{DocumentRecoveryRequired}
	}
	if err = s.commitKnowledge(ctx, tx, "knowledge_document_settled"); err != nil {
		return zero, err
	}
	return r, nil
}

func (s *Store) verifySettledHead(ctx context.Context, g knowledgeRepository) (string, error) {
	expected, err := s.verifySettledKnowledgeHistory(ctx, g)
	if err != nil {
		return "", err
	}
	current, err := g.readRef(filepath.Join("heads", "knowledge"))
	if err != nil {
		return "", err
	}
	if current != expected {
		return "", Error{DocumentRecoveryRequired}
	}
	return current, nil
}

func (s *Store) replayDocument(ctx context.Context, g knowledgeRepository, o documentOperation) (authority.DocumentMutationResult, error) {
	var zero authority.DocumentMutationResult
	if o.Result == nil {
		return zero, Error{Corrupt}
	}
	if err := g.verifyDocumentEvidence(o); err != nil {
		return zero, err
	}
	if err := s.verifyDocumentHistory(ctx, g, o.commitOID()); err != nil {
		return zero, err
	}
	// Immutable history keeps the original candidate alive even after later
	// updates. Pins are separately inspected/retained; replay never substitutes
	// a locator or manufactures a missing object from caller payload.
	if o.PinCleanup == "retained" {
		pin, err := g.readRef(filepath.Join("pins", string(o.ID)))
		if err != nil || pin != o.commitOID() && pin != authority.EmptyKnowledgeRef {
			return zero, Error{DocumentRecoveryRequired}
		}
		if pin != authority.EmptyKnowledgeRef {
			if err = g.publishRef(filepath.Join("pins", string(o.ID)), o.commitOID(), o.commitOID()); err != nil {
				return zero, err
			}
		}
	}
	for _, pair := range [][2]string{{o.Documents, "tree"}, {o.Root, "tree"}, {o.commitOID(), "commit"}} {
		object, err := g.readObject(pair[0], pair[1])
		if err != nil {
			return zero, err
		}
		if err = g.writeObject(object); err != nil {
			return zero, err
		}
	}
	if o.Metadata.Result.State == "live" {
		blob, err := g.readObject(o.Metadata.Result.Locator.BlobOID, "blob")
		if err != nil {
			return zero, err
		}
		if err = g.writeObject(blob); err != nil {
			return zero, err
		}
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	r := *o.Result
	r.Receipt.Replayed = true
	return r, nil
}
