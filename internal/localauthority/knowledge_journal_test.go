package localauthority

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
)

func documentOperationFixture(t *testing.T, s *Store) documentOperation {
	t.Helper()
	request := authority.DocumentMutationRequest{Schema: 1, Operation: "document.create",
		Intent:  authority.DocumentWriteIntent{ProjectID: testProject, WriteMode: "local", IdempotencyKey: "synthetic-doc-original", ExpectedRefOID: authority.EmptyKnowledgeRef},
		Payload: json.RawMessage(`{"title":"Example","markdown":"# Original document-byte-sentinel"}`)}
	entity, _ := authority.NewUUID()
	operation, _ := authority.NewUUID()
	candidate, err := planKnowledgeCandidate(nil, authority.EmptyKnowledgeRef, operation, entity, []byte("# Original document-byte-sentinel"), false)
	if err != nil {
		t.Fatal("test-only document candidate failed")
	}
	metadata, err := documentMetadataForCandidate(request, entity, operation, 1, candidate)
	if err != nil {
		t.Fatal("test-only planned document metadata failed")
	}
	intent, err := documentFingerprint(request, testActor)
	if err != nil {
		t.Fatal("test-only document intent failed")
	}
	o := documentOperation{Key: hash([]byte(string(testProject) + "\x00" + request.Intent.IdempotencyKey)), Intent: intent,
		ID: operation, Entity: entity, Alias: 1, Operation: request.Operation, Metadata: metadata,
		Documents: candidate.documents.oid, Root: candidate.root.oid, Actor: testActor,
		RecordedAt: time.Now().UTC().Format(time.RFC3339Nano), Phase: "reserved", Generation: 1, PinCleanup: "retained"}
	body, _ := json.Marshal(metadata)
	if _, err = s.db.Exec(`INSERT INTO document_operations VALUES(?,?,?,?,?,?,?,?,?,?,?,1,'reserved',1,NULL,'retained')`, o.Key, o.Intent, o.ID, o.Entity, o.Alias, o.Operation, string(body), o.Documents, o.Root, o.Actor.ID, o.RecordedAt); err != nil {
		t.Fatal("test-only document journal insertion failed")
	}
	return o
}

func TestKnowledgePendingJournalReopenAndTrackerIsolation(t *testing.T) {
	s, registry, name := testStore(t)
	ctx := context.Background()
	if _, err := s.SetupKnowledge(ctx, testProject, "synthetic-journal-setup"); err != nil {
		t.Fatal("explicit test knowledge setup failed")
	}
	o := documentOperationFixture(t, s)
	if _, err := s.db.Exec(`UPDATE aliases SET next_number=2 WHERE kind='document'`); err != nil {
		t.Fatal("test-only pending alias reservation failed")
	}
	if err := s.verifyIdentity(ctx, testProject); err != nil {
		t.Fatal("valid pending journal was mistaken for SQL corruption")
	}
	if err := s.publishBarrier(); err != nil {
		t.Fatal("test-only journal durability failed")
	}
	if err := s.Close(); err != nil {
		t.Fatal("test store close failed")
	}
	reopened, err := OpenWriter(ctx, registry, name, testProject)
	if err != nil {
		t.Fatal("valid pending operation prevented reopen")
	}
	defer reopened.Close()
	loaded, err := loadDocumentOperation(ctx, reopened.db, testProject, o.Key)
	if err != nil || loaded.Entity != o.Entity || loaded.ID != o.ID || loaded.Alias != 1 || loaded.Phase != "reserved" {
		t.Fatal("reopen changed original pending identity")
	}
	tracker := request(t, "issue.create", "synthetic-pending-tracker", authority.IssuePayload{Title: "Example", Body: "Tracker remains usable", State: "open"})
	if _, err := reopened.Mutate(ctx, tracker, testActor); err != nil {
		t.Fatal("pending knowledge operation blocked unrelated tracker write")
	}
	if err := reopened.verifyIdentity(ctx, testProject); err != nil {
		t.Fatal("tracker commit invalidated pending document reservation")
	}
}

func TestKnowledgePendingJournalCrossBindingRejects(t *testing.T) {
	for _, damage := range []string{"alias-unreserved", "setup-not-ready", "entity-already-present", "orphan-document-revision"} {
		t.Run(damage, func(t *testing.T) {
			s, _, _ := testStore(t)
			ctx := context.Background()
			if _, err := s.SetupKnowledge(ctx, testProject, "synthetic-journal-setup"); err != nil {
				t.Fatal("explicit test knowledge setup failed")
			}
			o := documentOperationFixture(t, s)
			if _, err := s.db.Exec(`UPDATE aliases SET next_number=2 WHERE kind='document'`); err != nil {
				t.Fatal("test-only alias reservation failed")
			}
			switch damage {
			case "alias-unreserved":
				corruptExec(t, s.db, `UPDATE aliases SET next_number=1 WHERE kind='document'`)
			case "setup-not-ready":
				corruptExec(t, s.db, `DROP TRIGGER immutable_knowledge_setup_delete`)
				corruptExec(t, s.db, `DELETE FROM knowledge_setup`)
			case "entity-already-present", "orphan-document-revision":
				// Commit a deferred-FK-consistent entity/revision pair. Missing
				// receipt/audit/settled journal still makes the authority invalid.
				tx, err := s.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal("test-only transaction failed")
				}
				defer tx.Rollback()
				metadata, _ := json.Marshal(o.Metadata)
				if _, err = tx.Exec(`INSERT INTO entities VALUES(?,'document',1,1)`, o.Entity); err != nil {
					t.Fatal("test-only entity failed")
				}
				if _, err = tx.Exec(`INSERT INTO revisions VALUES(?,1,1,?,?,0,?,?)`, o.Entity, string(metadata), hash(metadata), o.Actor.ID, o.RecordedAt); err != nil {
					t.Fatal("test-only revision failed")
				}
				if err = tx.Commit(); err != nil {
					t.Fatal("test-only pair failed")
				}
			}
			if err := s.verifyIdentity(ctx, testProject); err == nil {
				t.Fatal("inconsistent journal/alias/setup/revision was admitted")
			}
		})
	}
}

func TestKnowledgeJournalCanonicalPlanAndMonotonicResult(t *testing.T) {
	s, _, _ := testStore(t)
	o := documentOperationFixture(t, s)
	loaded, err := loadDocumentOperation(context.Background(), s.db, testProject, o.Key)
	if err != nil || loaded.ID != o.ID || loaded.Metadata.citation() != o.Metadata.citation() || loaded.commitOID() != o.commitOID() || loaded.Result != nil {
		t.Fatal("original reserved plan changed or fabricated acknowledgement")
	}
	for _, query := range []string{`UPDATE document_operations SET phase='settled',generation=3,result='{}'`, `UPDATE document_operations SET pin_cleanup='cleared'`, `DELETE FROM document_operations`} {
		if _, err := s.db.Exec(query); err == nil {
			t.Fatal("journal skipped binding, evicted original operation or cleaned unprotected pin")
		}
	}
	if _, err := s.db.Exec(`UPDATE document_operations SET phase='bound',generation=2`); err != nil {
		t.Fatal("test-only bound phase failed")
	}
	loaded, err = loadDocumentOperation(context.Background(), s.db, testProject, o.Key)
	if err != nil || loaded.Phase != "bound" || loaded.Generation != 2 || loaded.Result != nil {
		t.Fatal("bound plan was treated as settled receipt")
	}
	r := authority.DocumentMutationResult{Schema: 1, Durability: authority.OrderedOSBarriersV1, Result: o.Metadata.Result,
		Receipt: authority.MutationReceipt{ProjectID: testProject, OperationID: o.ID, Citation: o.Metadata.citation(), ProjectSequence: 1,
			IdempotencyKeyHash: o.Key, IntentHash: o.Intent, Evidence: "local_commit_confirmed", ProjectionState: "pending"}}
	body, _ := json.Marshal(r)
	if _, err := s.db.Exec(`UPDATE document_operations SET phase='settled',generation=3,result=?`, string(body)); err != nil {
		t.Fatal("test-only settled result failed")
	}
	loaded, err = loadDocumentOperation(context.Background(), s.db, testProject, o.Key)
	if err != nil || loaded.Result == nil || loaded.Result.Validate() != nil || loaded.Result.Receipt != r.Receipt {
		t.Fatal("settled original result did not round trip")
	}
	if _, err := s.db.Exec(`UPDATE document_operations SET pin_cleanup='cleared'`); err != nil {
		t.Fatal("test-only settled pin bookkeeping failed")
	}
	if _, err := s.db.Exec(`UPDATE document_operations SET phase='bound',generation=2,result=NULL`); err == nil {
		t.Fatal("settled result was downgraded")
	}
	if _, err := s.db.Exec(`UPDATE document_operations SET pin_cleanup='retained'`); err == nil {
		t.Fatal("settled cleanup was downgraded")
	}
}

func TestKnowledgeJournalRejectsCorruptPlanWithoutDocumentBytes(t *testing.T) {
	for _, column := range []string{"intent_hash", "root_oid", "documents_oid", "metadata", "actor_id", "operation_id", "result", "lane"} {
		t.Run(column, func(t *testing.T) {
			s, _, _ := testStore(t)
			o := documentOperationFixture(t, s)
			corruptExec(t, s.db, "DROP TRIGGER immutable_document_operation_identity")
			corruptExec(t, s.db, "DROP TRIGGER monotonic_document_operation")
			corruptExec(t, s.db, "PRAGMA ignore_check_constraints=ON")
			value := strings.Repeat("a", 64)
			switch column {
			case "metadata", "result", "lane":
				value = strings.Repeat("synthetic-private-journal", 8192)
			case "actor_id":
				value = "different-actor"
			case "operation_id":
				value = string(knowledgeTestOperation)
			}
			corruptExec(t, s.db, "UPDATE document_operations SET "+column+"=?", value)
			_, err := loadDocumentOperation(context.Background(), s.db, testProject, o.Key)
			requireKnowledgeError(t, err, Corrupt)
			if strings.Contains(err.Error(), "synthetic-private-journal") {
				t.Fatal("corrupt private journal value escaped error")
			}
		})
	}
	metadata := documentRevisionMetadata{}
	if decodeErr := json.Unmarshal([]byte(`{}`), &metadata); decodeErr != nil {
		t.Fatal("synthetic parser setup failed")
	}
	if metadata.Validate() == nil {
		t.Fatal("empty metadata became a document plan")
	}
}
