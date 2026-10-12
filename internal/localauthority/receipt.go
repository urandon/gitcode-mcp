package localauthority

import (
	"context"
	"database/sql"
	"errors"
	"gitcode-mcp/internal/authority"
)

type receiptReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadReceipt(ctx context.Context, db receiptReader, key string, project authority.UUID) (authority.MutationReceipt, error) {
	return scanReceipt(db.QueryRowContext(ctx, receiptQuery+" WHERE rec.key_hash=?", key), project, ctx)
}

const receiptQuery = `SELECT rec.key_hash,rec.intent_hash,rec.operation_id,rec.sequence,
 CASE WHEN length(CAST(rec.receipt AS BLOB))<=8192 THEN rec.receipt END,e.entity_id,e.kind,r.revision
 FROM receipts rec JOIN revisions r ON r.sequence=rec.sequence JOIN entities e ON e.entity_id=r.entity_id`

func scanReceipt(row scanner, project authority.UUID, ctx context.Context) (authority.MutationReceipt, error) {
	var result authority.MutationReceipt
	var savedKey, intent, op, body, id, kind string
	var sequence, revision int64
	err := row.Scan(&savedKey, &intent, &op, &sequence, &body, &id, &kind, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if err != nil {
		return result, storageError(ctx)
	}
	if authority.DecodeJSON([]byte(body), &result) != nil || result.ProjectID != project || result.IdempotencyKeyHash != savedKey || result.IntentHash != intent || result.OperationID != authority.UUID(op) || sequence < 1 || result.ProjectSequence != uint64(sequence) || result.Citation.EntityID != authority.UUID(id) || result.Citation.Kind != authority.EntityKind(kind) || revision < 1 || result.Citation.Revision != uint64(revision) || result.Replayed {
		return authority.MutationReceipt{}, Error{Corrupt}
	}
	return result, nil
}
