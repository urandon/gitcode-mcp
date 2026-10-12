package localauthority

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"gitcode-mcp/internal/authority"
	"math"
	"time"
)

// AuditEntry is deliberately content-free. Protected actor text belongs only
// to revision metadata; requests, keys, titles, bodies and paths are excluded.
type AuditEntry struct {
	Sequence    uint64         `json:"sequence"`
	OperationID authority.UUID `json:"operation_id"`
	EntityID    authority.UUID `json:"entity_id"`
	Revision    uint64         `json:"revision"`
	Operation   string         `json:"operation"`
	KeyHash     string         `json:"key_hash"`
	IntentHash  string         `json:"intent_hash"`
	ActorHash   string         `json:"actor_hash"`
	RecordedAt  string         `json:"recorded_at"`
}

const auditQuery = `SELECT a.sequence,
 CASE WHEN length(a.operation_id)=36 THEN a.operation_id END,
 CASE WHEN length(a.entity_id)=36 THEN a.entity_id END,a.revision,
 CASE WHEN length(a.operation)<=32 THEN a.operation END,
 CASE WHEN length(a.key_hash)=64 THEN a.key_hash END,
 CASE WHEN length(a.intent_hash)=64 THEN a.intent_hash END,
 CASE WHEN length(a.actor_hash)=64 THEN a.actor_hash END,
 CASE WHEN length(a.recorded_at)<=64 THEN a.recorded_at END,
 rec.operation_id,rec.key_hash,rec.intent_hash,e.entity_id,e.kind,r.revision,
 CASE WHEN length(CAST(r.actor_id AS BLOB))<=128 THEN r.actor_id END,
 CASE WHEN length(r.recorded_at)<=64 THEN r.recorded_at END,r.tombstone,
 CASE WHEN length(CAST(r.payload AS BLOB))<=516096 THEN r.payload END
 FROM audit a JOIN receipts rec ON rec.sequence=a.sequence
 JOIN revisions r ON r.sequence=a.sequence JOIN entities e ON e.entity_id=r.entity_id`

func validDigest(value string) bool {
	data, err := hex.DecodeString(value)
	return err == nil && len(data) == 32 && hex.EncodeToString(data) == value
}

func scanAudit(row scanner, project authority.UUID) (AuditEntry, error) {
	var a AuditEntry
	var operation, key, intent, entity, kind, actor, at, payload string
	var revision uint64
	var tombstone bool
	if row.Scan(&a.Sequence, &a.OperationID, &a.EntityID, &a.Revision, &a.Operation, &a.KeyHash, &a.IntentHash, &a.ActorHash, &a.RecordedAt, &operation, &key, &intent, &entity, &kind, &revision, &actor, &at, &tombstone, &payload) != nil {
		return AuditEntry{}, Error{Corrupt}
	}
	wantOperation := kind + ".update"
	if revision == 1 {
		wantOperation = kind + ".create"
	}
	if tombstone {
		wantOperation = kind + ".tombstone"
	}
	stamp, err := time.Parse(time.RFC3339Nano, a.RecordedAt)
	if a.Sequence == 0 || !a.OperationID.Valid() || !a.EntityID.Valid() || a.Revision == 0 || a.Revision != revision || a.OperationID != authority.UUID(operation) || a.EntityID != authority.UUID(entity) || a.Operation != wantOperation || a.KeyHash != key || a.IntentHash != intent || !validDigest(a.KeyHash) || !validDigest(a.IntentHash) || !validDigest(a.ActorHash) || !(Actor{ID: actor}).valid() || a.ActorHash != hash([]byte(string(project)+"\x00"+actor)) || a.RecordedAt != at || err != nil || stamp.UTC().Format(time.RFC3339Nano) != at {
		return AuditEntry{}, Error{Corrupt}
	}
	request := authority.MutationRequest{Intent: authority.WriteIntent{ProjectID: project, WriteMode: "local", IdempotencyKey: "synthetic-integrity-validation"}, Operation: a.Operation, Payload: json.RawMessage(payload)}
	if kind == string(authority.Document) {
		metadata, err := decodeDocumentMetadata([]byte(payload))
		if err != nil || metadata.citation() != (authority.Citation{ProjectID: project, EntityID: a.EntityID, Kind: authority.Document, Revision: revision}) || metadata.OperationID != a.OperationID || metadata.operation() != a.Operation || tombstone != (metadata.Result.State == "tombstone") {
			return AuditEntry{}, Error{Corrupt}
		}
		fingerprint, err := documentMetadataFingerprint(metadata, Actor{ID: actor})
		if err != nil || fingerprint != a.IntentHash {
			return AuditEntry{}, Error{Corrupt}
		}
		return a, nil
	}
	if revision > 1 {
		request.Intent.EntityID = a.EntityID
		request.Intent.ExpectedRevision = revision - 1
	}
	canonical, err := canonicalPayload(request)
	if err != nil || !bytes.Equal(canonical, []byte(payload)) || mutationFingerprint(request, actor) != a.IntentHash {
		return AuditEntry{}, Error{Corrupt}
	}
	return a, nil
}

func (s *Store) Audit(ctx context.Context, after uint64, limit int) ([]AuditEntry, error) {
	if after > math.MaxInt64 || limit < 1 || limit > 100 {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, Error{ReadOnly}
	}
	rows, err := s.db.QueryContext(ctx, auditQuery+` WHERE a.sequence>? ORDER BY a.sequence LIMIT ?`, after, limit)
	if err != nil {
		return nil, storageError(ctx)
	}
	defer rows.Close()
	result := []AuditEntry{}
	for rows.Next() {
		a, err := scanAudit(rows, s.manifest.ID)
		if err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	if rows.Err() != nil {
		return nil, storageError(ctx)
	}
	return result, nil
}
