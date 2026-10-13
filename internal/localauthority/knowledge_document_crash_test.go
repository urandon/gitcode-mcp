package localauthority

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
)

func TestKnowledgeDocumentProcessHelper(t *testing.T) {
	if os.Getenv("GCM_DOCUMENT_CHILD") != "1" {
		return
	}
	registry, err := newRegistry(os.Getenv("GCM_DOCUMENT_REGISTRY"))
	if err != nil {
		t.Fatal("test child registry unavailable")
	}
	s, err := OpenWriter(context.Background(), registry, os.Getenv("GCM_DOCUMENT_DIRECTORY"), testProject)
	if err != nil {
		t.Fatal("test child writer unavailable")
	}
	defer s.Close()
	var request authority.DocumentMutationRequest
	if authority.DecodeJSON([]byte(os.Getenv("GCM_DOCUMENT_REQUEST")), &request) != nil {
		t.Fatal("test child request unavailable")
	}
	cut, _ := strconv.Atoi(os.Getenv("GCM_DOCUMENT_CUT"))
	seen := 0
	s.fault = func(at string) error {
		if at == os.Getenv("GCM_DOCUMENT_PHASE") {
			seen++
			if seen == cut {
				os.Exit(77)
			}
		}
		return nil
	}
	if _, err := s.MutateDocument(context.Background(), request, testActor); err != nil {
		t.Fatal("test child stopped before specified barrier")
	}
	t.Fatal("test child did not reach specified barrier")
}

func TestKnowledgeDocumentAbruptExitPreservesOriginalCandidate(t *testing.T) {
	for _, mode := range []string{"create", "update", "tombstone"} {
		for _, phase := range []struct {
			name  string
			count int
		}{
			{"knowledge_document_reserved", 1}, {"knowledge_object_file", 8}, {"knowledge_object_directory", 8},
			{"knowledge_ref_file", 2}, {"knowledge_ref_rename", 2}, {"knowledge_ref_directory", 3},
			{"knowledge_document_bound", 1}, {"knowledge_document_revision", 1}, {"knowledge_document_receipt", 1},
			{"knowledge_document_settled", 1}, {"knowledge_document_acknowledgement", 1},
			{"database_sync", 3}, {"wal_sync", 3}, {"directory_sync", 3},
		} {
			occurrences := phase.count
			if mode == "tombstone" && (phase.name == "knowledge_object_file" || phase.name == "knowledge_object_directory") {
				occurrences = 6
			}
			for cut := 1; cut <= occurrences; cut++ {
				t.Run(mode+"-"+phase.name+"-"+strconv.Itoa(cut), func(t *testing.T) {
					s, registry, directory := readyDocumentStore(t)
					request := documentRequest(t, "document.create", "synthetic-crash-doc", authority.EmptyKnowledgeRef, authority.Citation{}, "# Exact original interrupted document")
					if mode != "create" {
						first, err := s.MutateDocument(context.Background(), documentRequest(t, "document.create", "synthetic-parent-doc", authority.EmptyKnowledgeRef, authority.Citation{}, "# Exact parent"), testActor)
						if err != nil {
							t.Fatal("test original parent failed")
						}
						request = documentRequest(t, "document."+mode, "synthetic-crash-doc", first.Result.Locator.CommitOID, first.Receipt.Citation, "# Exact original interrupted document")
					}
					if s.Close() != nil {
						t.Fatal("test writer quiesce failed")
					}
					encoded, _ := json.Marshal(request)
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					defer cancel()
					cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestKnowledgeDocumentProcessHelper$")
					cmd.Env = append(os.Environ(), "GCM_DOCUMENT_CHILD=1", "GCM_DOCUMENT_REGISTRY="+registry.root, "GCM_DOCUMENT_DIRECTORY="+directory, "GCM_DOCUMENT_REQUEST="+string(encoded), "GCM_DOCUMENT_PHASE="+phase.name, "GCM_DOCUMENT_CUT="+strconv.Itoa(cut))
					var exit *exec.ExitError
					if !errors.As(cmd.Run(), &exit) || exit.ExitCode() != 77 {
						t.Fatal("child did not exit at exact requested boundary")
					}
					reopened, err := OpenWriter(context.Background(), registry, directory, testProject)
					if err != nil {
						t.Fatal("original SQL authority failed crash reopen")
					}
					defer reopened.Close()
					key := hash([]byte(string(testProject) + "\x00" + request.Intent.IdempotencyKey))
					original, err := loadDocumentOperation(context.Background(), reopened.db, testProject, key)
					if err != nil {
						t.Fatal("original reservation disappeared")
					}
					// Daemon recovery must not need the original Markdown/key once
					// an exact published pin or settled receipt exists. An earlier
					// reservation/lock-only still requires the original caller.
					setup, err := loadKnowledgeSetup(context.Background(), reopened.db, testProject, reopened.instance, reopened.directory)
					if err != nil {
						t.Fatal("crash recovery setup unavailable")
					}
					pin, err := reopened.knowledgeRepository(setup).readRef("pins/" + string(original.ID))
					if err != nil {
						t.Fatal("crash recovery pin inspection unavailable")
					}
					automatic, autoErr := reopened.recoverSelectedDocument(context.Background(), selectRecovery(original))
					if original.Phase == "reserved" && pin == authority.EmptyKnowledgeRef {
						requireKnowledgeError(t, autoErr, DocumentRecoveryRequired)
					} else if autoErr != nil || automatic.Validate() != nil || !automatic.Receipt.Replayed || automatic.Receipt.OperationID != original.ID || automatic.Receipt.Citation.EntityID != original.Entity {
						t.Fatal("exact pinned crash recovery failed or substituted identity")
					}
					result, err := reopened.MutateDocument(context.Background(), request, testActor)
					if err != nil || result.Validate() != nil || !result.Receipt.Replayed || result.Receipt.OperationID != original.ID || result.Receipt.Citation.EntityID != original.Entity {
						t.Fatal("crash recovery substituted identity/candidate or failed")
					}
					commit := ""
					if result.Result.State == "live" {
						commit = result.Result.Locator.CommitOID
					} else {
						commit = result.Result.Tombstone.CommitOID
					}
					if commit != original.commitOID() {
						t.Fatal("crash recovery substituted original commit")
					}
					if err := reopened.verifyIdentity(context.Background(), testProject); err != nil {
						t.Fatal("recovered settled authority failed integrity")
					}
				})
			}
		}
	}
}
