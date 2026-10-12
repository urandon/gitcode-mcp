package localauthority

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"gitcode-mcp/internal/authority"
)

func (s *Store) readyKnowledgeRepository(ctx context.Context) (knowledgeRepository, error) {
	k, err := loadKnowledgeSetup(ctx, s.db, s.manifest.ID, s.instance, s.directory)
	if errors.Is(err, sql.ErrNoRows) {
		return knowledgeRepository{}, Error{KnowledgeSetupRequired}
	}
	if err != nil {
		return knowledgeRepository{}, err
	}
	if k.Phase != "ready" {
		return knowledgeRepository{}, Error{KnowledgeInitializationPending}
	}
	g := s.knowledgeRepository(k)
	pending, err := s.pendingDocumentOperation(ctx)
	if err != nil {
		return knowledgeRepository{}, err
	}
	if _, _, err = g.verifyJournalLayout(pending); err != nil {
		return knowledgeRepository{}, err
	}
	if err = s.verifyDocumentInventory(ctx, g); err != nil {
		return knowledgeRepository{}, err
	}
	return g, nil
}

func (s *Store) pendingDocumentOperation(ctx context.Context) (*documentOperation, error) {
	var key string
	err := s.db.QueryRowContext(ctx, `SELECT key_hash FROM document_operations WHERE phase<>'settled'`).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, storageError(ctx)
	}
	o, err := loadDocumentOperation(ctx, s.db, s.manifest.ID, key)
	if err != nil {
		return nil, s.documentSQLError(err)
	}
	return &o, nil
}

// Hydration follows the immutable SQL locator, never the current ref/worktree.
// The resulting Markdown is transient protected response data, not a SQL/cache
// copy. Structural operation/parent checks bind it to the original revision.
func hydrateDocument(g knowledgeRepository, v authority.EntityView) (authority.EntityView, error) {
	m, err := decodeDocumentMetadata(v.Payload)
	if err != nil || m.citation() != v.Citation || v.Tombstone != (m.Result.State == "tombstone") {
		return authority.EntityView{}, Error{Corrupt}
	}
	commitOID := ""
	if m.Result.State == "live" {
		commitOID = m.Result.Locator.CommitOID
	} else {
		commitOID = m.Result.Tombstone.CommitOID
	}
	commit, err := g.readObject(commitOID, "commit")
	if err != nil {
		return authority.EntityView{}, err
	}
	_, parent, operation, err := decodeKnowledgeCommit(commit.data)
	if err != nil || parent != m.ExpectedRefOID || operation != m.OperationID {
		return authority.EntityView{}, Error{KnowledgeCorrupt}
	}
	entries, err := g.treeAt(commitOID)
	if err != nil {
		return authority.EntityView{}, err
	}
	if m.Result.State == "tombstone" {
		if entries[v.Citation.EntityID] != "" {
			return authority.EntityView{}, Error{KnowledgeCorrupt}
		}
		v.Payload = json.RawMessage(`{}`)
	} else {
		locator := m.Result.Locator
		if entries[v.Citation.EntityID] != locator.BlobOID {
			return authority.EntityView{}, Error{KnowledgeCorrupt}
		}
		blob, err := g.readObject(locator.BlobOID, "blob")
		if err != nil || hash(blob.data) != locator.SHA256 || int64(len(blob.data)) != locator.ByteLength {
			return authority.EntityView{}, Error{KnowledgeCorrupt}
		}
		v.Payload, err = json.Marshal(authority.DocumentPayload{Title: m.Title, Markdown: string(blob.data)})
		if err != nil {
			return authority.EntityView{}, Error{KnowledgeCorrupt}
		}
	}
	if v.Validate() != nil {
		return authority.EntityView{}, Error{KnowledgeCorrupt}
	}
	return v, nil
}
