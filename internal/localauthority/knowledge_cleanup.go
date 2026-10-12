package localauthority

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	"gitcode-mcp/internal/authority"
)

// DocumentOperationState is a protected content-free observation, not a
// replacement/patch to the immutable confirmed document result. Raw keys,
// paths, titles, provider bodies and actor text never cross this boundary.
type DocumentOperationState struct {
	Schema      int            `json:"schema"`
	ProjectID   authority.UUID `json:"project_id"`
	OperationID authority.UUID `json:"operation_id"`
	EntityID    authority.UUID `json:"entity_id"`
	Scope       string         `json:"scope"`
	Phase       string         `json:"phase"`
	PinState    string         `json:"pin_state"`
	Code        Code           `json:"code,omitempty"`
	Remediation string         `json:"remediation"`
}

func (r DocumentOperationState) Validate() error {
	if r.Schema != 1 || !r.ProjectID.Valid() || !r.OperationID.Valid() || !r.EntityID.Valid() || r.Scope != "document_operation" {
		return authority.Error{Code: authority.InvalidContract}
	}
	switch r.Phase {
	case "reserved", "bound":
		if r.PinState != "retained" || r.Code != DocumentPending || r.Remediation != "resume_original_intent" {
			return authority.Error{Code: authority.InvalidContract}
		}
	case "settled":
		want := "cleanup_retained_pin"
		if r.PinState == "cleared" {
			want = "none"
		}
		if r.PinState != "retained" && r.PinState != "cleared" && r.PinState != "retained_partial" || r.Code != "" || r.Remediation != want {
			return authority.Error{Code: authority.InvalidContract}
		}
	default:
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

func operationState(o documentOperation, project authority.UUID) DocumentOperationState {
	r := DocumentOperationState{Schema: 1, ProjectID: project, OperationID: o.ID, EntityID: o.Entity, Scope: "document_operation", Phase: o.Phase, PinState: o.PinCleanup, Remediation: "none"}
	if o.Phase != "settled" {
		r.Code, r.Remediation = DocumentPending, "resume_original_intent"
	} else if o.PinCleanup == "retained" {
		r.Remediation = "cleanup_retained_pin"
	}
	return r
}

func (s *Store) DocumentOperationStatus(ctx context.Context, project authority.UUID, key string) (DocumentOperationState, error) {
	var zero DocumentOperationState
	if (authority.WriteIntent{ProjectID: project, WriteMode: "local", IdempotencyKey: key}).Validate(true) != nil {
		return zero, authority.Error{Code: authority.InvalidContract}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return zero, Error{ReadOnly}
	}
	if project != s.manifest.ID {
		return zero, authority.Error{Code: authority.NoAuthority}
	}
	if s.schema == 1 {
		return zero, Error{KnowledgeMigrationRequired}
	}
	o, err := loadDocumentOperation(ctx, s.db, project, hash([]byte(string(project)+"\x00"+key)))
	if errors.Is(err, sql.ErrNoRows) {
		return zero, authority.Error{Code: authority.NoAuthority}
	}
	if err != nil {
		return zero, s.documentSQLError(err)
	}
	state := operationState(o, project)
	if o.Phase == "settled" && o.PinCleanup == "retained" {
		k, err := loadKnowledgeSetup(ctx, s.db, project, s.instance, s.directory)
		if err != nil {
			return zero, s.documentSQLError(err)
		}
		pin, err := s.knowledgeRepository(k).readRef(filepath.Join("pins", string(o.ID)))
		if err != nil {
			return zero, err
		}
		if pin == authority.EmptyKnowledgeRef {
			state.PinState = "retained_partial"
		} else if pin != o.commitOID() {
			return zero, Error{KnowledgeCorrupt}
		}
	}
	return state, nil
}

// CleanupDocumentPin is explicit and bounded to ONE original settled key.
// It never evicts pending evidence, deletes objects/history, repairs a ref,
// substitutes an acknowledgement, or creates/changes a document receipt.
func (s *Store) CleanupDocumentPin(ctx context.Context, project authority.UUID, key string) (DocumentOperationState, error) {
	var zero DocumentOperationState
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if (authority.WriteIntent{ProjectID: project, WriteMode: "local", IdempotencyKey: key}).Validate(true) != nil {
		return zero, authority.Error{Code: authority.InvalidContract}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.lease == nil || s.fenced {
		return zero, Error{ReadOnly}
	}
	if project != s.manifest.ID {
		return zero, authority.Error{Code: authority.NoAuthority}
	}
	if s.schema == 1 {
		return zero, Error{KnowledgeMigrationRequired}
	}
	if err := s.verifyPhysicalBinding(); err != nil {
		s.fenced = true
		return zero, err
	}
	if err := verifyWriter(ctx, s.db); err != nil {
		return zero, err
	}
	if err := s.verifyIdentity(ctx, project); err != nil {
		return zero, s.documentSQLError(err)
	}
	o, err := loadDocumentOperation(ctx, s.db, project, hash([]byte(string(project)+"\x00"+key)))
	if errors.Is(err, sql.ErrNoRows) {
		return zero, authority.Error{Code: authority.NoAuthority}
	}
	if err != nil {
		return zero, s.documentSQLError(err)
	}
	if o.Phase != "settled" {
		return zero, Error{DocumentPending}
	}
	k, err := loadKnowledgeSetup(ctx, s.db, project, s.instance, s.directory)
	if err != nil {
		return zero, s.documentSQLError(err)
	}
	g := s.knowledgeRepository(k)
	if err = qualifyFilesystem(g.directory); err != nil {
		return zero, err
	}
	if err = s.verifyDocumentHistory(ctx, g, o.commitOID()); err != nil {
		return zero, err
	}
	if err = g.syncDocumentEvidence(o); err != nil {
		return zero, err
	}
	head, err := g.readRef(filepath.Join("heads", "knowledge"))
	if err != nil || head == authority.EmptyKnowledgeRef {
		return zero, Error{DocumentRecoveryRequired}
	}
	if err = g.publishRef(filepath.Join("heads", "knowledge"), head, head); err != nil {
		return zero, err
	}
	if o.PinCleanup == "cleared" {
		return operationState(o, project), nil
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	name := filepath.Join("pins", string(o.ID))
	pin, err := g.readRef(name)
	if err != nil || pin != authority.EmptyKnowledgeRef && pin != o.commitOID() {
		return zero, Error{KnowledgeCorrupt}
	}
	if lock, err := g.readRef(name + ".lock"); err != nil || lock != authority.EmptyKnowledgeRef {
		return zero, Error{DocumentRecoveryRequired}
	}
	if pin != authority.EmptyKnowledgeRef {
		if g.verifyPhysical() != nil || os.Remove(filepath.Join(g.directory, "refs", name)) != nil {
			return zero, Error{DocumentRecoveryRequired}
		}
		if err = g.inject("knowledge_pin_cleanup_unlink"); err != nil {
			return zero, err
		}
	}
	if err = syncDirectory(filepath.Join(g.directory, "refs", "pins")); err == nil {
		err = syncDirectory(filepath.Join(g.directory, "refs"))
	}
	if err != nil {
		return zero, err
	}
	if err = g.inject("knowledge_pin_cleanup_directory"); err != nil {
		return zero, err
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, storageError(ctx)
	}
	defer tx.Rollback()
	changed, err := tx.ExecContext(ctx, `UPDATE document_operations SET pin_cleanup='cleared' WHERE key_hash=? AND operation_id=? AND phase='settled' AND generation=3 AND pin_cleanup='retained'`, o.Key, o.ID)
	if err != nil {
		return zero, storageError(ctx)
	}
	if n, err := changed.RowsAffected(); err != nil || n != 1 {
		return zero, Error{DocumentRecoveryRequired}
	}
	if err = s.commitKnowledge(ctx, tx, "knowledge_pin_cleanup_settled"); err != nil {
		return zero, err
	}
	o.PinCleanup = "cleared"
	return operationState(o, project), nil
}
