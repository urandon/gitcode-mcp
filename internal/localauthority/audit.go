package localauthority

import (
	"context"
	"gitcode-mcp/internal/authority"
	"math"
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

func (s *Store) Audit(ctx context.Context, after uint64, limit int) ([]AuditEntry, error) {
	if after > math.MaxInt64 || limit < 1 || limit > 100 {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, Error{ReadOnly}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT sequence,operation_id,entity_id,revision,operation,key_hash,intent_hash,actor_hash,recorded_at FROM audit WHERE sequence>? ORDER BY sequence LIMIT ?`, after, limit)
	if err != nil {
		return nil, storageError(ctx)
	}
	defer rows.Close()
	result := []AuditEntry{}
	for rows.Next() {
		var a AuditEntry
		if rows.Scan(&a.Sequence, &a.OperationID, &a.EntityID, &a.Revision, &a.Operation, &a.KeyHash, &a.IntentHash, &a.ActorHash, &a.RecordedAt) != nil {
			return nil, storageError(ctx)
		}
		result = append(result, a)
	}
	if rows.Err() != nil {
		return nil, storageError(ctx)
	}
	return result, nil
}
