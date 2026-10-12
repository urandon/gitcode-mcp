package localauthority

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitcode-mcp/internal/authority"
)

func TestKnowledgeOwnedPairedUpdateRecoveryAndOldRead(t *testing.T) {
	for _, secondCut := range []string{"none", "knowledge_owned_ref_file", "knowledge_owned_ref_rename"} {
		t.Run(secondCut, func(t *testing.T) {
			s, _, _ := readyDocumentStore(t)
			ctx := context.Background()
			first, err := s.MutateDocument(ctx, documentRequest(t, "document.create", "synthetic-pair-parent", authority.EmptyKnowledgeRef, authority.Citation{}, "# Parent immutable"), testActor)
			if err != nil {
				t.Fatal("test parent failed")
			}
			update := documentRequest(t, "document.update", "synthetic-pair-update", first.Result.Locator.CommitOID, first.Receipt.Citation, "# Exact update")
			stop := errors.New("synthetic ref interruption")
			seen := 0
			s.fault = func(at string) error {
				if at == "knowledge_ref_file" {
					seen++
					if seen == 2 {
						return stop
					}
				}
				return nil
			}
			if _, err := s.MutateDocument(ctx, update, testActor); !errors.Is(err, stop) {
				t.Fatal("head lock boundary was not reached")
			}
			s.fault = nil
			status, err := s.KnowledgeStatus(ctx, testProject)
			if err != nil || status.Validate() != nil || status.Pending == nil || status.Pending.Phase != "bound" {
				t.Fatal("owned paired operation lost finite pending status")
			}
			read, err := s.Read(ctx, authority.ReadRequest{ProjectID: testProject, Kind: authority.Document, Operation: "get", EntityID: first.Receipt.Citation.EntityID, Revision: 1})
			if err != nil || len(read.Entities) != 1 {
				t.Fatal("known pending lock made immutable prior document unavailable")
			}
			if secondCut != "none" {
				s.fault = func(at string) error {
					if at == secondCut {
						return stop
					}
					return nil
				}
				if _, err := s.MutateDocument(ctx, update, testActor); !errors.Is(err, stop) {
					t.Fatal("owned recovery missed exact barrier")
				}
				s.fault = nil
			}
			result, err := s.MutateDocument(ctx, update, testActor)
			if err != nil || !result.Receipt.Replayed || result.Receipt.Citation.Revision != 2 {
				t.Fatal("original owned pair did not recover")
			}
		})
	}
}

func TestKnowledgeGitCorruptionFencesDocumentsNotTracker(t *testing.T) {
	for _, damage := range []string{"missing-blob", "foreign-pin", "settled-lock", "head-divergence", "replacement-directory"} {
		t.Run(damage, func(t *testing.T) {
			s, registry, directory := readyDocumentStore(t)
			ctx := context.Background()
			request := documentRequest(t, "document.create", "synthetic-corrupt-doc", authority.EmptyKnowledgeRef, authority.Citation{}, "# Original private fixture")
			original, err := s.MutateDocument(ctx, request, testActor)
			if err != nil {
				t.Fatal("test original failed")
			}
			root := filepath.Join(s.directory, knowledgeDirectoryName)
			switch damage {
			case "missing-blob":
				oid := original.Result.Locator.BlobOID
				if os.Rename(filepath.Join(root, "objects", oid[:2], oid[2:]), filepath.Join(directory, "preserved-object")) != nil {
					t.Fatal("test object relocation failed")
				}
			case "foreign-pin":
				foreign, _ := authority.NewUUID()
				if os.WriteFile(filepath.Join(root, "refs", "pins", string(foreign)), []byte(original.Result.Locator.CommitOID+"\n"), 0600) != nil {
					t.Fatal("test foreign pin failed")
				}
			case "settled-lock":
				if os.WriteFile(filepath.Join(root, "refs", "heads", "knowledge.lock"), []byte(original.Result.Locator.CommitOID+"\n"), 0600) != nil {
					t.Fatal("test settled paired lock failed")
				}
			case "head-divergence":
				if os.WriteFile(filepath.Join(root, "refs", "heads", "knowledge"), []byte(strings.Repeat("a", 64)+"\n"), 0600) != nil {
					t.Fatal("test foreign head failed")
				}
			case "replacement-directory":
				if os.Rename(root, filepath.Join(directory, "preserved-knowledge")) != nil || os.Mkdir(root, 0700) != nil {
					t.Fatal("test physical replacement failed")
				}
			}
			if _, err := s.MutateDocument(ctx, request, testActor); err == nil {
				t.Fatal("corrupt Git returned new confirmed acknowledgement")
			}
			fresh := documentRequest(t, "document.create", "synthetic-new-after-damage", original.Result.Locator.CommitOID, authority.Citation{}, "# New")
			if _, err := s.MutateDocument(ctx, fresh, testActor); err == nil {
				t.Fatal("corrupt Git admitted new key")
			}
			createIssue(t, s, "synthetic-tracker-after-git-damage")
			if s.Close() != nil {
				t.Fatal("test close failed")
			}
			reopened, err := OpenWriter(ctx, registry, directory, testProject)
			if err != nil {
				t.Fatal("Git-only damage blocked intact SQL reopen")
			}
			defer reopened.Close()
			createIssue(t, reopened, "synthetic-tracker-after-reopen")
		})
	}
}

func TestKnowledgeBoundMissingPinIsNotManufactured(t *testing.T) {
	s, _, _ := readyDocumentStore(t)
	ctx := context.Background()
	request := documentRequest(t, "document.create", "synthetic-bound-missing-pin", authority.EmptyKnowledgeRef, authority.Citation{}, "# Original pinned proof")
	stop := errors.New("synthetic binding interruption")
	s.fault = func(at string) error {
		if at == "knowledge_document_bound" {
			return stop
		}
		return nil
	}
	if _, err := s.MutateDocument(ctx, request, testActor); !errors.Is(err, stop) {
		t.Fatal("test binding did not stop")
	}
	s.fault = nil
	o, err := loadDocumentOperation(ctx, s.db, testProject, hash([]byte(string(testProject)+"\x00"+request.Intent.IdempotencyKey)))
	if err != nil || o.Phase != "bound" {
		t.Fatal("test bound record unavailable")
	}
	path := filepath.Join(s.directory, knowledgeDirectoryName, "refs", "pins", string(o.ID))
	if os.Rename(path, filepath.Join(s.directory, "preserved-pin")) != nil {
		t.Fatal("test pin relocation failed")
	}
	_, err = s.MutateDocument(ctx, request, testActor)
	requireKnowledgeError(t, err, DocumentRecoveryRequired)
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatal("missing bound proof was manufactured")
	}
	status, err := s.KnowledgeStatus(ctx, testProject)
	if err != nil || status.Validate() != nil || status.Code != string(DocumentRecoveryRequired) || status.Pending == nil {
		t.Fatal("original recovery fence lost finite status")
	}
	createIssue(t, s, "synthetic-tracker-with-missing-bound-pin")
}

func TestKnowledgeDocumentSQLCorruptionFencesTrackerImmediately(t *testing.T) {
	s, _, _ := readyDocumentStore(t)
	ctx := context.Background()
	original, err := s.MutateDocument(ctx, documentRequest(t, "document.create", "synthetic-sql-damaged-doc", authority.EmptyKnowledgeRef, authority.Citation{}, "# SQL-free body"), testActor)
	if err != nil {
		t.Fatal("test document failed")
	}
	corruptExec(t, s.db, `DROP TRIGGER immutable_document_operation_identity`)
	corruptExec(t, s.db, `UPDATE document_operations SET actor_id='another-synthetic-actor'`)
	tracker := request(t, "issue.create", "synthetic-new-after-sql-damage", authority.IssuePayload{Title: "Example", Body: "No new effect", State: "open"})
	_, err = s.Mutate(ctx, tracker, testActor)
	requireKnowledgeError(t, err, Corrupt)
	if !s.fenced || !s.logicalFenced {
		t.Fatal("logical knowledge corruption did not fence global SQL writer")
	}
	var count int
	if s.db.QueryRow(`SELECT count(*) FROM entities WHERE kind='issue'`).Scan(&count) != nil || count != 0 {
		t.Fatal("tracker write escaped logical corruption fence")
	}
	if original.Receipt.Citation.Kind != authority.Document {
		t.Fatal("test contract mismatch")
	}
}

func TestKnowledgeSupersededBlobLossFencesNewAdmission(t *testing.T) {
	s, _, _ := readyDocumentStore(t)
	ctx := context.Background()
	first, err := s.MutateDocument(ctx, documentRequest(t, "document.create", "synthetic-old-blob", authority.EmptyKnowledgeRef, authority.Citation{}, "# Old authority bytes"), testActor)
	if err != nil {
		t.Fatal("test original create failed")
	}
	second, err := s.MutateDocument(ctx, documentRequest(t, "document.update", "synthetic-new-blob", first.Result.Locator.CommitOID, first.Receipt.Citation, "# New authority bytes"), testActor)
	if err != nil {
		t.Fatal("test update failed")
	}
	oid := first.Result.Locator.BlobOID
	if os.Rename(filepath.Join(s.directory, knowledgeDirectoryName, "objects", oid[:2], oid[2:]), filepath.Join(s.directory, "preserved-old-blob")) != nil {
		t.Fatal("test old blob relocation failed")
	}
	request := documentRequest(t, "document.update", "synthetic-after-old-loss", second.Result.Locator.CommitOID, second.Receipt.Citation, "# No new effect")
	_, err = s.MutateDocument(ctx, request, testActor)
	requireKnowledgeError(t, err, KnowledgeCorrupt)
	var count int
	key := hash([]byte(string(testProject) + "\x00" + request.Intent.IdempotencyKey))
	if s.db.QueryRow(`SELECT count(*) FROM document_operations WHERE key_hash=?`, key).Scan(&count) != nil || count != 0 {
		t.Fatal("superseded authority loss was ignored during new admission")
	}
	createIssue(t, s, "synthetic-tracker-with-superseded-blob-loss")
}
