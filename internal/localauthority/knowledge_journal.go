package localauthority

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"time"

	"gitcode-mcp/internal/authority"
)

type documentOperation struct {
	Key, Intent     string
	ID, Entity      authority.UUID
	Alias           int64
	Operation       string
	Metadata        documentRevisionMetadata
	Documents, Root string
	Actor           Actor
	RecordedAt      string
	Phase           string
	Generation      int
	Result          *authority.DocumentMutationResult
	PinCleanup      string
}

func (o documentOperation) commitOID() string {
	if o.Metadata.Result.State == "live" {
		return o.Metadata.Result.Locator.CommitOID
	}
	return o.Metadata.Result.Tombstone.CommitOID
}

const documentOperationQuery = `SELECT
 CASE WHEN length(key_hash)=64 THEN key_hash END,
 CASE WHEN length(intent_hash)=64 THEN intent_hash END,
 CASE WHEN length(operation_id)=36 THEN operation_id END,
 CASE WHEN length(entity_id)=36 THEN entity_id END,
 CASE WHEN typeof(alias_number)='integer' AND alias_number>0 THEN alias_number END,
 CASE WHEN length(operation)<=32 THEN operation END,
 CASE WHEN length(CAST(metadata AS BLOB))<=4096 THEN metadata END,
 CASE WHEN length(documents_oid)=64 THEN documents_oid END,
 CASE WHEN length(root_oid)=64 THEN root_oid END,
 CASE WHEN length(CAST(actor_id AS BLOB))<=128 THEN actor_id END,
 CASE WHEN length(recorded_at)<=64 THEN recorded_at END,
 CASE WHEN length(phase)<=8 THEN phase END,
 CASE WHEN typeof(generation)='integer' AND generation BETWEEN 1 AND 3 THEN generation END,
 CASE WHEN result IS NULL OR length(CAST(result AS BLOB))<=8192 THEN result END,result IS NULL,
 CASE WHEN length(pin_cleanup)<=8 THEN pin_cleanup END,
 CASE WHEN typeof(lane)='integer' AND lane=1 THEN lane END
 FROM document_operations`

func scanDocumentOperation(ctx context.Context, row scanner, project authority.UUID) (documentOperation, error) {
	var o documentOperation
	var metadata string
	var result sql.NullString
	var resultIsNull bool
	var lane int
	err := row.Scan(&o.Key, &o.Intent, &o.ID, &o.Entity, &o.Alias, &o.Operation, &metadata, &o.Documents, &o.Root, &o.Actor.ID, &o.RecordedAt, &o.Phase, &o.Generation, &result, &resultIsNull, &o.PinCleanup, &lane)
	if errors.Is(err, sql.ErrNoRows) {
		return o, err
	}
	if err != nil {
		if ctx.Err() != nil {
			return o, ctx.Err()
		}
		return documentOperation{}, Error{Corrupt}
	}
	m, err := decodeDocumentMetadata([]byte(metadata))
	stamp, timeErr := time.Parse(time.RFC3339Nano, o.RecordedAt)
	if err != nil || !validDigest(o.Key) || !validDigest(o.Intent) || !o.ID.Valid() || !o.Entity.Valid() || o.Alias < 1 || !o.Actor.valid() || timeErr != nil || stamp.UTC().Format(time.RFC3339Nano) != o.RecordedAt || lane != 1 || result.Valid == resultIsNull {
		return documentOperation{}, Error{Corrupt}
	}
	o.Metadata = m
	c := m.citation()
	if c.ProjectID != project || c.EntityID != o.Entity || c.Revision > math.MaxInt64 || m.OperationID != o.ID || m.operation() != o.Operation || !knowledgeOIDValid(o.Documents, false) || !knowledgeOIDValid(o.Root, false) {
		return documentOperation{}, Error{Corrupt}
	}
	fingerprint, err := documentMetadataFingerprint(m, o.Actor)
	root, rootErr := knowledgeRoot(o.Documents)
	commit, commitErr := knowledgeCommit(o.Root, m.ExpectedRefOID, o.ID)
	if err != nil || fingerprint != o.Intent || rootErr != nil || root.oid != o.Root || commitErr != nil || commit.oid != o.commitOID() {
		return documentOperation{}, Error{Corrupt}
	}
	switch o.Phase {
	case "reserved", "bound":
		generation := 1
		if o.Phase == "bound" {
			generation = 2
		}
		if o.Generation != generation || result.Valid || o.PinCleanup != "retained" {
			return documentOperation{}, Error{Corrupt}
		}
	case "settled":
		if o.Generation != 3 || !result.Valid || o.PinCleanup != "retained" && o.PinCleanup != "cleared" {
			return documentOperation{}, Error{Corrupt}
		}
		var r authority.DocumentMutationResult
		if authority.DecodeJSON([]byte(result.String), &r) != nil || r.Receipt.Replayed || r.Receipt.ProjectID != project || r.Receipt.IdempotencyKeyHash != o.Key || r.Receipt.IntentHash != o.Intent || r.Receipt.OperationID != o.ID || r.Receipt.Citation != c {
			return documentOperation{}, Error{Corrupt}
		}
		canonical, _ := json.Marshal(r)
		actualOutcome, _ := json.Marshal(r.Result)
		plannedOutcome, _ := json.Marshal(m.Result)
		if !bytes.Equal(canonical, []byte(result.String)) || !bytes.Equal(actualOutcome, plannedOutcome) {
			return documentOperation{}, Error{Corrupt}
		}
		o.Result = &r
	default:
		return documentOperation{}, Error{Corrupt}
	}
	return o, nil
}

func loadDocumentOperation(ctx context.Context, db receiptReader, project authority.UUID, key string) (documentOperation, error) {
	if ctx.Err() != nil {
		return documentOperation{}, ctx.Err()
	}
	return scanDocumentOperation(ctx, db.QueryRowContext(ctx, documentOperationQuery+" WHERE key_hash=?", key), project)
}

func checkReservedAliases(ctx context.Context, tx *sql.Tx) error {
	for _, kind := range []authority.EntityKind{authority.Issue, authority.Comment, authority.Label, authority.Milestone, authority.Document, authority.Link} {
		var next, count, maximum int64
		if tx.QueryRowContext(ctx, "SELECT next_number FROM aliases WHERE kind=?", kind).Scan(&next) != nil || next < 1 {
			return Error{Corrupt}
		}
		if tx.QueryRowContext(ctx, `SELECT count(*),coalesce(max(alias_number),0) FROM (
 SELECT alias_number FROM entities WHERE kind=?
 UNION ALL SELECT alias_number FROM document_operations WHERE operation='document.create' AND phase<>'settled' AND ?='document'
 )`, kind, kind).Scan(&count, &maximum) != nil || count == math.MaxInt64 || maximum == math.MaxInt64 || next != count+1 || next != maximum+1 {
			return Error{Corrupt}
		}
	}
	return nil
}

// All validation is SQL-only. Git object/pin/ref evidence is separately checked
// on document admission/recovery/read, not on unrelated tracker admission.
func checkDocumentOperations(ctx context.Context, tx *sql.Tx, project, tracker authority.UUID, directory string) error {
	rows, err := tx.QueryContext(ctx, documentOperationQuery+" ORDER BY key_hash")
	if err != nil {
		return storageError(ctx)
	}
	defer rows.Close()
	seen, pending := 0, 0
	for rows.Next() {
		seen++
		o, err := scanDocumentOperation(ctx, rows, project)
		if err != nil {
			return err
		}
		if o.Phase != "settled" {
			pending++
			if pending > 1 {
				return Error{Corrupt}
			}
			var receipts int
			if tx.QueryRowContext(ctx, "SELECT count(*) FROM receipts WHERE key_hash=? OR operation_id=?", o.Key, o.ID).Scan(&receipts) != nil || receipts != 0 {
				return Error{Corrupt}
			}
			if o.Operation == "document.create" {
				var exists int
				var next int64
				if tx.QueryRowContext(ctx, "SELECT count(*) FROM entities WHERE entity_id=?", o.Entity).Scan(&exists) != nil || exists != 0 || tx.QueryRowContext(ctx, "SELECT next_number FROM aliases WHERE kind='document'").Scan(&next) != nil || next < 2 || o.Alias != next-1 {
					return Error{Corrupt}
				}
			} else {
				var revision, alias int64
				var tombstone bool
				if tx.QueryRowContext(ctx, `SELECT e.current_revision,e.alias_number,r.tombstone FROM entities e JOIN revisions r ON r.entity_id=e.entity_id AND r.revision=e.current_revision WHERE e.entity_id=? AND e.kind='document'`, o.Entity).Scan(&revision, &alias, &tombstone) != nil || revision < 1 || uint64(revision) != o.Metadata.citation().Revision-1 || alias != o.Alias || tombstone {
					return Error{Corrupt}
				}
			}
			continue
		}
		r, err := loadReceipt(ctx, tx, o.Key, project)
		if err != nil || o.Result == nil || r != o.Result.Receipt {
			return Error{Corrupt}
		}
		var metadata, digest, actor, at string
		var alias int64
		var tombstone bool
		if tx.QueryRowContext(ctx, `SELECT CASE WHEN length(CAST(r.payload AS BLOB))<=4096 THEN r.payload END,
 CASE WHEN length(r.payload_hash)=64 THEN r.payload_hash END,
 CASE WHEN length(CAST(r.actor_id AS BLOB))<=128 THEN r.actor_id END,
 CASE WHEN length(r.recorded_at)<=64 THEN r.recorded_at END,e.alias_number,r.tombstone
 FROM entities e JOIN revisions r ON r.entity_id=e.entity_id WHERE e.entity_id=? AND e.kind='document' AND r.revision=? AND r.sequence=?`, o.Entity, r.Citation.Revision, r.ProjectSequence).Scan(&metadata, &digest, &actor, &at, &alias, &tombstone) != nil {
			return Error{Corrupt}
		}
		canonical, _ := json.Marshal(o.Metadata)
		if metadata != string(canonical) || digest != hash(canonical) || actor != o.Actor.ID || at != o.RecordedAt || alias != o.Alias || tombstone != (o.Metadata.Result.State == "tombstone") {
			return Error{Corrupt}
		}
	}
	if rows.Err() != nil {
		return storageError(ctx)
	}
	_ = rows.Close()
	if seen != 0 {
		setup, err := loadKnowledgeSetup(ctx, tx, project, tracker, directory)
		if err != nil || setup.Phase != "ready" {
			return Error{Corrupt}
		}
	}
	var orphans int
	if tx.QueryRowContext(ctx, `SELECT count(*) FROM entities e JOIN revisions r ON r.entity_id=e.entity_id
 WHERE e.kind='document' AND NOT EXISTS(SELECT 1 FROM document_operations o JOIN receipts rec ON rec.key_hash=o.key_hash
 WHERE o.phase='settled' AND o.entity_id=e.entity_id AND rec.sequence=r.sequence)`).Scan(&orphans) != nil || orphans != 0 {
		return Error{Corrupt}
	}
	return nil
}
