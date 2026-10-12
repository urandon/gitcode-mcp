package localauthority

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"gitcode-mcp/internal/authority"
)

// documentRevisionMetadata is protected SQL metadata, never document bytes.
// Planned candidates and settled revisions can share its immutable locators,
// but only a separate verified binding and settlement grant acknowledgement.
type documentRevisionMetadata struct {
	Schema         int                       `json:"schema"`
	Algorithm      int                       `json:"algorithm"`
	OperationID    authority.UUID            `json:"operation_id"`
	ExpectedRefOID string                    `json:"expected_ref_oid"`
	Title          string                    `json:"title,omitempty"`
	Result         authority.DocumentOutcome `json:"result"`
}

func (m documentRevisionMetadata) citation() authority.Citation {
	if m.Result.State == "live" && m.Result.Locator != nil {
		return m.Result.Locator.Citation
	}
	if m.Result.State == "tombstone" && m.Result.Tombstone != nil {
		return m.Result.Tombstone.Citation
	}
	return authority.Citation{}
}

func (m documentRevisionMetadata) operation() string {
	if m.Result.State == "tombstone" {
		return "document.tombstone"
	}
	if m.citation().Revision == 1 {
		return "document.create"
	}
	return "document.update"
}

func (m documentRevisionMetadata) Validate() error {
	if m.Schema != 1 || m.Algorithm != knowledgeAlgorithm || !m.OperationID.Valid() || !knowledgeOIDValid(m.ExpectedRefOID, true) || m.Result.Validate() != nil {
		return Error{Corrupt}
	}
	if m.Result.State == "live" {
		if !utf8.ValidString(m.Title) || len(m.Title) > 256 || strings.TrimSpace(m.Title) == "" || m.citation().Revision > 1 && m.ExpectedRefOID == authority.EmptyKnowledgeRef {
			return Error{Corrupt}
		}
	} else if m.Title != "" || m.Result.Tombstone.OperationID != m.OperationID || m.Result.Tombstone.ParentOID != m.ExpectedRefOID {
		return Error{Corrupt}
	}
	return nil
}

func decodeDocumentMetadata(body []byte) (documentRevisionMetadata, error) {
	var m documentRevisionMetadata
	if len(body) > 4096 || authority.DecodeJSON(body, &m) != nil {
		return m, Error{Corrupt}
	}
	canonical, err := json.Marshal(m)
	if err != nil || !bytes.Equal(body, canonical) {
		return documentRevisionMetadata{}, Error{Corrupt}
	}
	return m, nil
}

type documentFingerprintFields struct {
	Schema           int            `json:"schema"`
	ProjectID        authority.UUID `json:"project_id"`
	Operation        string         `json:"operation"`
	EntityID         authority.UUID `json:"entity_id"`
	ExpectedRevision uint64         `json:"expected_revision"`
	ExpectedRefOID   string         `json:"expected_ref_oid"`
	Title            string         `json:"title"`
	SHA256           string         `json:"sha256"`
	ByteLength       int64          `json:"byte_length"`
	Tombstone        bool           `json:"tombstone"`
	Actor            string         `json:"actor"`
}

func (f documentFingerprintFields) digest() string {
	body, _ := json.Marshal(f)
	return hash(body)
}

func documentFingerprint(request authority.DocumentMutationRequest, actor Actor) (string, error) {
	if request.Validate() != nil || !actor.valid() {
		return "", authority.Error{Code: authority.InvalidContract}
	}
	f := documentFingerprintFields{Schema: request.Schema, ProjectID: request.Intent.ProjectID, Operation: request.Operation,
		EntityID: request.Intent.EntityID, ExpectedRevision: request.Intent.ExpectedRevision,
		ExpectedRefOID: request.Intent.ExpectedRefOID, Actor: actor.ID, Tombstone: request.Operation == "document.tombstone"}
	if !f.Tombstone {
		var payload authority.DocumentPayload
		if authority.DecodeJSON(request.Payload, &payload) != nil {
			return "", authority.Error{Code: authority.InvalidContract}
		}
		f.Title, f.SHA256, f.ByteLength = payload.Title, hash([]byte(payload.Markdown)), int64(len(payload.Markdown))
	}
	return f.digest(), nil
}

// Metadata can reconstruct the original intent fingerprint after restart
// without retrieving or storing Markdown. Reserved create UUIDs are not caller
// intent fields; operation identity and idempotency key text are excluded.
func documentMetadataFingerprint(m documentRevisionMetadata, actor Actor) (string, error) {
	if m.Validate() != nil || !actor.valid() {
		return "", Error{Corrupt}
	}
	citation := m.citation()
	f := documentFingerprintFields{Schema: authority.DocumentContractSchema, ProjectID: citation.ProjectID,
		Operation: m.operation(), ExpectedRefOID: m.ExpectedRefOID, Title: m.Title,
		Actor: actor.ID, Tombstone: m.Result.State == "tombstone"}
	if citation.Revision > 1 {
		f.EntityID, f.ExpectedRevision = citation.EntityID, citation.Revision-1
	}
	if m.Result.State == "live" {
		f.SHA256, f.ByteLength = m.Result.Locator.SHA256, m.Result.Locator.ByteLength
	}
	return f.digest(), nil
}

func documentMetadataForCandidate(request authority.DocumentMutationRequest, entity, operation authority.UUID, revision uint64, candidate knowledgeCandidate) (documentRevisionMetadata, error) {
	var m documentRevisionMetadata
	if request.Validate() != nil || !entity.Valid() || !operation.Valid() || revision == 0 {
		return m, authority.Error{Code: authority.InvalidContract}
	}
	citation := authority.Citation{ProjectID: request.Intent.ProjectID, EntityID: entity, Kind: authority.Document, Revision: revision}
	m = documentRevisionMetadata{Schema: 1, Algorithm: knowledgeAlgorithm, OperationID: operation, ExpectedRefOID: request.Intent.ExpectedRefOID}
	if request.Operation == "document.tombstone" {
		m.Result = authority.DocumentOutcome{State: "tombstone", Tombstone: &authority.DocumentTombstoneEvidence{
			Citation: citation, OperationID: operation, CommitOID: candidate.commit.oid, ParentOID: request.Intent.ExpectedRefOID}}
	} else {
		var p authority.DocumentPayload
		if authority.DecodeJSON(request.Payload, &p) != nil || !bytes.Equal(candidate.blob.data, []byte(p.Markdown)) || candidate.contentHash != hash([]byte(p.Markdown)) || candidate.byteLength != int64(len(p.Markdown)) {
			return documentRevisionMetadata{}, Error{Corrupt}
		}
		m.Title = p.Title
		m.Result = authority.DocumentOutcome{State: "live", Locator: &authority.DocumentLocator{
			Citation: citation, CommitOID: candidate.commit.oid, BlobOID: candidate.blob.oid, SHA256: candidate.contentHash, ByteLength: candidate.byteLength}}
	}
	if m.Validate() != nil || m.operation() != request.Operation || request.Operation != "document.create" && (request.Intent.EntityID != entity || request.Intent.ExpectedRevision != revision-1) {
		return documentRevisionMetadata{}, Error{Corrupt}
	}
	objects := []knowledgeObject{candidate.documents, candidate.root, candidate.commit}
	if request.Operation != "document.tombstone" {
		objects = append(objects, candidate.blob)
	} else if candidate.blob.oid != "" || len(candidate.blob.data) != 0 || candidate.contentHash != "" || candidate.byteLength != 0 {
		return documentRevisionMetadata{}, Error{Corrupt}
	}
	for _, object := range objects {
		canonical, err := newKnowledgeObject(object.kind, object.data)
		if err != nil || canonical.oid != object.oid {
			return documentRevisionMetadata{}, Error{Corrupt}
		}
	}
	documentsOID, err := decodeKnowledgeRoot(candidate.root.data)
	if err != nil || documentsOID != candidate.documents.oid {
		return documentRevisionMetadata{}, Error{Corrupt}
	}
	entries, err := decodeDocumentTree(candidate.documents.data)
	if err != nil || request.Operation == "document.tombstone" && entries[entity] != "" || request.Operation != "document.tombstone" && entries[entity] != candidate.blob.oid {
		return documentRevisionMetadata{}, Error{Corrupt}
	}
	root, parent, op, err := decodeKnowledgeCommit(candidate.commit.data)
	if err != nil || parent != request.Intent.ExpectedRefOID || op != operation || root != candidate.root.oid {
		return documentRevisionMetadata{}, Error{Corrupt}
	}
	return m, nil
}
