package localauthority

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"gitcode-mcp/internal/authority"
)

func documentRequest(t *testing.T, operation, key, ref string, citation authority.Citation, markdown string) authority.DocumentMutationRequest {
	t.Helper()
	payload := json.RawMessage(`{}`)
	if operation != "document.tombstone" {
		payload, _ = json.Marshal(authority.DocumentPayload{Title: "Example document", Markdown: markdown})
	}
	r := authority.DocumentMutationRequest{Schema: 1, Operation: operation, Intent: authority.DocumentWriteIntent{ProjectID: testProject, WriteMode: "local", IdempotencyKey: key, ExpectedRefOID: ref}, Payload: payload}
	if operation != "document.create" {
		r.Intent.EntityID, r.Intent.ExpectedRevision = citation.EntityID, citation.Revision
	}
	if r.Validate() != nil {
		t.Fatal("invalid synthetic document request")
	}
	return r
}

func readyDocumentStore(t *testing.T) (*Store, *Registry, string) {
	t.Helper()
	s, registry, name := testStore(t)
	if _, err := s.SetupKnowledge(context.Background(), testProject, "synthetic-document-setup"); err != nil {
		t.Fatal("explicit test setup failed")
	}
	return s, registry, name
}

func TestKnowledgeDocumentWriteReadHistoryAndExactReplay(t *testing.T) {
	s, registry, name := readyDocumentStore(t)
	ctx := context.Background()
	original := documentRequest(t, "document.create", "synthetic-first-doc", authority.EmptyKnowledgeRef, authority.Citation{}, "# Original Git-only-byte-sentinel\n")
	first, err := s.MutateDocument(ctx, original, testActor)
	if err != nil || first.Validate() != nil || first.Receipt.Replayed || first.Receipt.Citation.Revision != 1 {
		t.Fatal("explicit document create failed")
	}
	secondRequest := documentRequest(t, "document.update", "synthetic-second-doc", first.Result.Locator.CommitOID, first.Receipt.Citation, "# Changed Git-only-byte-sentinel\n")
	second, err := s.MutateDocument(ctx, secondRequest, testActor)
	if err != nil || second.Receipt.Citation.Revision != 2 {
		t.Fatal("explicit document update failed")
	}
	tombstone, err := s.MutateDocument(ctx, documentRequest(t, "document.tombstone", "synthetic-third-doc", second.Result.Locator.CommitOID, second.Receipt.Citation, ""), testActor)
	if err != nil || tombstone.Result.State != "tombstone" || tombstone.Validate() != nil {
		t.Fatal("explicit document tombstone failed")
	}
	for revision, markdown := range map[uint64]string{1: "# Original Git-only-byte-sentinel\n", 2: "# Changed Git-only-byte-sentinel\n", 3: ""} {
		result, err := s.Read(ctx, authority.ReadRequest{ProjectID: testProject, Kind: authority.Document, Operation: "get", EntityID: first.Receipt.Citation.EntityID, Revision: revision})
		if err != nil || len(result.Entities) != 1 || result.Entities[0].Citation.Revision != revision {
			t.Fatal("immutable historical document read failed")
		}
		if revision == 3 {
			if !result.Entities[0].Tombstone || string(result.Entities[0].Payload) != "{}" {
				t.Fatal("tombstone fabricated a live locator/payload")
			}
		} else {
			var p authority.DocumentPayload
			if authority.DecodeJSON(result.Entities[0].Payload, &p) != nil || p.Markdown != markdown {
				t.Fatal("historical read substituted current bytes")
			}
		}
	}
	if err := s.verifyIdentity(ctx, testProject); err != nil {
		t.Fatal("settled journal/revision/receipt/audit cross-binding failed")
	}
	status, err := s.KnowledgeStatus(ctx, testProject)
	if err != nil || status.Validate() != nil || status.CurrentRefOID != tombstone.Result.Tombstone.CommitOID {
		t.Fatal("fresh protected status did not supply exact verified ref CAS")
	}
	history, err := s.Read(ctx, authority.ReadRequest{ProjectID: testProject, Kind: authority.Document, Operation: "history", EntityID: first.Receipt.Citation.EntityID, Limit: 2})
	if err != nil || len(history.Entities) != 2 || history.NextCursor == "" {
		t.Fatal("document history pagination failed")
	}
	lastPage, err := s.Read(ctx, authority.ReadRequest{ProjectID: testProject, Kind: authority.Document, Operation: "history", EntityID: first.Receipt.Citation.EntityID, Limit: 2, Cursor: history.NextCursor})
	if err != nil || len(lastPage.Entities) != 1 || !lastPage.Entities[0].Tombstone || lastPage.NextCursor != "" {
		t.Fatal("document history lost tombstone continuation")
	}
	listed, err := s.Read(ctx, authority.ReadRequest{ProjectID: testProject, Kind: authority.Document, Operation: "list", Limit: 1})
	if err != nil || len(listed.Entities) != 1 || listed.Entities[0].Citation.Revision != 3 || !listed.Entities[0].Tombstone {
		t.Fatal("document list did not hydrate the canonical current revision")
	}
	for _, filename := range []string{"tracker.db", "tracker.db-wal"} {
		data, err := os.ReadFile(filepath.Join(s.directory, filename))
		if os.IsNotExist(err) && filename == "tracker.db-wal" {
			continue
		}
		if err != nil || bytes.Contains(data, []byte("Git-only-byte-sentinel")) {
			t.Fatal("Markdown entered SQL or SQL inspection failed")
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal("test close failed")
	}
	reopened, err := OpenWriter(ctx, registry, name, testProject)
	if err != nil {
		t.Fatal("populated authority failed reopen")
	}
	defer reopened.Close()
	replayed, err := reopened.MutateDocument(ctx, original, testActor)
	if err != nil || !replayed.Receipt.Replayed {
		t.Fatal("original request did not replay after subsequent changes/reopen")
	}
	replayed.Receipt.Replayed = false
	if !equalDocumentResult(replayed, first) {
		t.Fatal("replay changed original receipt/locator")
	}
	changed := original
	changed.Payload = json.RawMessage(`{"title":"Different","markdown":"# Other"}`)
	if _, err := reopened.MutateDocument(ctx, changed, testActor); err == nil {
		t.Fatal("changed intent reused admitted key")
	}
}

func equalDocumentResult(a, b authority.DocumentMutationResult) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return bytes.Equal(left, right)
}

func TestKnowledgeDocumentStalePreflightHasZeroEffectsAndValidNextWorks(t *testing.T) {
	s, _, _ := readyDocumentStore(t)
	ctx := context.Background()
	first, err := s.MutateDocument(ctx, documentRequest(t, "document.create", "synthetic-client-a", authority.EmptyKnowledgeRef, authority.Citation{}, "# A"), testActor)
	if err != nil {
		t.Fatal("initial create failed")
	}
	for _, scenario := range []string{"stale-ref", "stale-revision"} {
		before, err := s.Status(ctx)
		if err != nil {
			t.Fatal("test status failed")
		}
		ref := first.Result.Locator.CommitOID
		citation := first.Receipt.Citation
		if scenario == "stale-ref" {
			ref = authority.EmptyKnowledgeRef
		} else {
			citation.Revision++
		}
		operation := "document.update"
		if scenario == "stale-ref" {
			operation = "document.create"
		}
		request := documentRequest(t, operation, "synthetic-"+scenario, ref, citation, "# B")
		_, err = s.MutateDocument(ctx, request, testActor)
		if err == nil {
			t.Fatal("stale caller was admitted")
		}
		after, statusErr := s.Status(ctx)
		var count int
		key := hash([]byte(string(testProject) + "\x00" + request.Intent.IdempotencyKey))
		if statusErr != nil || before != after || s.db.QueryRow(`SELECT count(*) FROM document_operations WHERE key_hash=?`, key).Scan(&count) != nil || count != 0 {
			t.Fatal("preflight consumed key or mutated state")
		}
	}
	if _, err := s.MutateDocument(ctx, documentRequest(t, "document.update", "synthetic-client-c", first.Result.Locator.CommitOID, first.Receipt.Citation, "# C"), testActor); err != nil {
		t.Fatal("stale client poisoned subsequent valid operation")
	}
}

func TestKnowledgeDocumentOriginalKeyResumesEveryLogicalBarrier(t *testing.T) {
	for _, phase := range []string{"knowledge_document_reserved", "knowledge_object_file", "knowledge_object_directory", "knowledge_ref_file", "knowledge_ref_rename", "knowledge_ref_directory", "knowledge_document_bound", "knowledge_document_revision", "knowledge_document_receipt", "knowledge_document_settled", "knowledge_document_acknowledgement"} {
		t.Run(phase, func(t *testing.T) {
			s, registry, name := readyDocumentStore(t)
			ctx := context.Background()
			request := documentRequest(t, "document.create", "synthetic-interrupted-doc", authority.EmptyKnowledgeRef, authority.Citation{}, "# Exact interrupted Git-byte-sentinel")
			interrupted := errors.New("synthetic interruption")
			fired := false
			s.fault = func(p string) error {
				if p == phase && !fired {
					fired = true
					return interrupted
				}
				return nil
			}
			if _, err := s.MutateDocument(ctx, request, testActor); !errors.Is(err, interrupted) || !fired {
				t.Fatal("specified barrier did not interrupt operation")
			}
			key := hash([]byte(string(testProject) + "\x00" + request.Intent.IdempotencyKey))
			original, err := loadDocumentOperation(ctx, s.db, testProject, key)
			if err != nil {
				t.Fatal("admitted original journal missing")
			}
			s.fault = nil
			if err := s.Close(); err != nil {
				t.Fatal("test close failed")
			}
			reopened, err := OpenWriter(ctx, registry, name, testProject)
			if err != nil {
				t.Fatal("interrupted authority failed verified reopen")
			}
			defer reopened.Close()
			result, err := reopened.MutateDocument(ctx, request, testActor)
			if err != nil || !result.Receipt.Replayed || result.Receipt.OperationID != original.ID || result.Receipt.Citation.EntityID != original.Entity || result.Result.Locator.CommitOID != original.commitOID() {
				t.Fatal("retry failed or changed original candidate identity")
			}
			if err := reopened.verifyIdentity(ctx, testProject); err != nil {
				t.Fatal("resumed settlement failed integrity")
			}
		})
	}
}

func TestKnowledgeDocumentGlobalKeyPrecedesSetupReadiness(t *testing.T) {
	for _, state := range []string{"uninitialized-tracker-key", "reserved-setup-key", "ready-setup-key"} {
		t.Run(state, func(t *testing.T) {
			s, _, _ := testStore(t)
			ctx := context.Background()
			key := "synthetic-global-original"
			if state == "uninitialized-tracker-key" {
				createIssue(t, s, key)
			} else {
				if state == "reserved-setup-key" {
					s.fault = func(at string) error {
						if at == "knowledge_setup_reserved" {
							return errors.New("synthetic interruption")
						}
						return nil
					}
				}
				_, err := s.SetupKnowledge(ctx, testProject, key)
				if state == "ready-setup-key" && err != nil {
					t.Fatal("test setup failed")
				}
				s.fault = nil
			}
			_, err := s.MutateDocument(ctx, documentRequest(t, "document.create", key, authority.EmptyKnowledgeRef, authority.Citation{}, "# Different canonical intent"), testActor)
			var conflict authority.Error
			if !errors.As(err, &conflict) || conflict.Code != authority.Conflict {
				t.Fatal("global original key was hidden by setup readiness")
			}
			var count int
			if s.db.QueryRow(`SELECT count(*) FROM document_operations`).Scan(&count) != nil || count != 0 {
				t.Fatal("conflicting role consumed document state")
			}
		})
	}
}
