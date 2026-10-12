package localauthority

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"

	"gitcode-mcp/internal/authority"
)

// Every pin/ref name must be owned by an exact SQL operation. Objects can be
// partially published before pin binding; they are never evicted by inspection.
func (s *Store) verifyDocumentInventory(ctx context.Context, g knowledgeRepository) error {
	pending, err := s.pendingDocumentOperation(ctx)
	if err != nil {
		return err
	}
	lock, err := g.readRef(filepath.Join("heads", "knowledge.lock"))
	if err != nil {
		return err
	}
	if lock != authority.EmptyKnowledgeRef && (pending == nil || pending.Phase != "bound" || lock != pending.commitOID()) {
		return Error{KnowledgeCorrupt}
	}
	if pending != nil {
		current, err := g.readRef(filepath.Join("heads", "knowledge"))
		if err != nil {
			return err
		}
		if current != pending.Metadata.ExpectedRefOID && (pending.Phase != "bound" || current != pending.commitOID()) {
			return Error{DocumentRecoveryRequired}
		}
		if pending.Phase == "bound" {
			pin, err := g.readRef(filepath.Join("pins", string(pending.ID)))
			if err != nil || pin != pending.commitOID() {
				return Error{DocumentRecoveryRequired}
			}
			if err = g.verifyDocumentEvidence(*pending); err != nil {
				return err
			}
		}
	}
	entries, err := knowledgeNames(filepath.Join(g.directory, "refs", "pins"), maxRetainedKnowledgePins*2)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		id := authority.UUID(strings.TrimSuffix(entry.Name(), ".lock"))
		o, err := scanDocumentOperation(ctx, s.db.QueryRowContext(ctx, documentOperationQuery+" WHERE operation_id=?", id), s.manifest.ID)
		if errors.Is(err, sql.ErrNoRows) {
			return Error{KnowledgeCorrupt}
		}
		if err != nil {
			return s.documentSQLError(err)
		}
		if o.PinCleanup != "retained" || o.Phase == "settled" && strings.HasSuffix(entry.Name(), ".lock") {
			return Error{KnowledgeCorrupt}
		}
		oid, err := g.readRef(filepath.Join("pins", entry.Name()))
		if err != nil || oid != o.commitOID() {
			return Error{KnowledgeCorrupt}
		}
	}
	return nil
}

// Prove that durable controlled history still protects an exact candidate,
// allowing immutable replay/explicit pin cleanup after later revisions. A
// pending original may account for the tip only after its verified binding.
func (s *Store) verifyDocumentHistory(ctx context.Context, g knowledgeRepository, target string) error {
	var key string
	err := s.db.QueryRowContext(ctx, `SELECT key_hash FROM document_operations WHERE phase<>'settled'`).Scan(&key)
	var pending *documentOperation
	if err == nil {
		o, e := loadDocumentOperation(ctx, s.db, s.manifest.ID, key)
		if e != nil {
			return s.documentSQLError(e)
		}
		pending = &o
	} else if !errors.Is(err, sql.ErrNoRows) {
		return storageError(ctx)
	}
	if _, _, err = g.verifyJournalLayout(pending); err != nil {
		return err
	}
	if err = s.verifyDocumentInventory(ctx, g); err != nil {
		return err
	}
	current, err := g.readRef(filepath.Join("heads", "knowledge"))
	if err != nil {
		return err
	}
	var latest string
	err = s.db.QueryRowContext(ctx, `SELECT o.key_hash FROM document_operations o JOIN receipts r ON r.key_hash=o.key_hash WHERE o.phase='settled' ORDER BY r.sequence DESC LIMIT 1`).Scan(&latest)
	expected := authority.EmptyKnowledgeRef
	if err == nil {
		o, err := loadDocumentOperation(ctx, s.db, s.manifest.ID, latest)
		if err != nil {
			return s.documentSQLError(err)
		}
		expected = o.commitOID()
		if err = g.verifyDocumentEvidence(o); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return storageError(ctx)
	}
	if current != expected {
		if pending == nil || pending.Phase != "bound" || pending.Metadata.ExpectedRefOID != expected || current != pending.commitOID() || g.verifyDocumentEvidence(*pending) != nil {
			return Error{DocumentRecoveryRequired}
		}
	}
	var count int
	if s.db.QueryRowContext(ctx, `SELECT count(*) FROM document_operations`).Scan(&count) != nil || count > maxKnowledgeLooseObjects {
		return Error{Corrupt}
	}
	found := false
	for step := 0; current != authority.EmptyKnowledgeRef && step < count; step++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		object, err := g.readObject(current, "commit")
		if err != nil {
			return err
		}
		root, parent, operation, err := decodeKnowledgeCommit(object.data)
		if err != nil {
			return err
		}
		o, err := scanDocumentOperation(ctx, s.db.QueryRowContext(ctx, documentOperationQuery+" WHERE operation_id=?", operation), s.manifest.ID)
		if err != nil {
			return Error{KnowledgeCorrupt}
		}
		if o.commitOID() != current || o.Metadata.ExpectedRefOID != parent || o.Phase != "settled" && (pending == nil || pending.ID != o.ID || o.Phase != "bound") {
			return Error{KnowledgeCorrupt}
		}
		// Commit bytes and original SQL root/parent/op bind ancestry. Full
		// candidate/blob proof is needed for the tip and target, not an
		// O(history * active documents) reread of every historical blob.
		if o.Root != root {
			return Error{KnowledgeCorrupt}
		}
		if current == target {
			found = true
		}
		current = parent
	}
	if current != authority.EmptyKnowledgeRef || !found {
		return Error{DocumentRecoveryRequired}
	}
	return nil
}
