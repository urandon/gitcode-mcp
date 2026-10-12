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

func TestKnowledgePinCleanupRetainsReceiptHistoryAndResumes(t *testing.T) {
	for _, phase := range []string{"none", "knowledge_pin_cleanup_unlink", "knowledge_pin_cleanup_directory", "knowledge_pin_cleanup_settled"} {
		t.Run(phase, func(t *testing.T) {
			s, registry, directory := readyDocumentStore(t)
			ctx := context.Background()
			request := documentRequest(t, "document.create", "synthetic-cleanup-document", authority.EmptyKnowledgeRef, authority.Citation{}, "# Preserved original document")
			original, err := s.MutateDocument(ctx, request, testActor)
			if err != nil {
				t.Fatal("test document create failed")
			}
			if _, err := s.MutateDocument(ctx, documentRequest(t, "document.update", "synthetic-later-document", original.Result.Locator.CommitOID, original.Receipt.Citation, "# Later document"), testActor); err != nil {
				t.Fatal("test later update failed")
			}
			status, err := s.DocumentOperationStatus(ctx, testProject, request.Intent.IdempotencyKey)
			if err != nil || status.Validate() != nil || status.PinState != "retained" || status.Phase != "settled" {
				t.Fatal("retained pin status unavailable")
			}
			interrupted := errors.New("synthetic cleanup interruption")
			s.fault = func(at string) error {
				if at == phase {
					return interrupted
				}
				return nil
			}
			state, err := s.CleanupDocumentPin(ctx, testProject, request.Intent.IdempotencyKey)
			if phase == "none" {
				if err != nil || state.Validate() != nil || state.PinState != "cleared" {
					t.Fatal("bounded cleanup failed")
				}
			} else if !errors.Is(err, interrupted) {
				t.Fatal("cleanup missed exact requested boundary")
			}
			s.fault = nil
			if err := s.Close(); err != nil {
				t.Fatal("test close failed")
			}
			reopened, err := OpenWriter(ctx, registry, directory, testProject)
			if err != nil {
				t.Fatal("cleanup interrupted original SQL integrity")
			}
			defer reopened.Close()
			result, err := reopened.MutateDocument(ctx, request, testActor)
			if err != nil || !result.Receipt.Replayed {
				t.Fatal("safe cleanup changed immutable acknowledgement")
			}
			result.Receipt.Replayed = false
			if !equalDocumentResult(result, original) {
				t.Fatal("cleanup substituted original result")
			}
			state, err = reopened.CleanupDocumentPin(ctx, testProject, request.Intent.IdempotencyKey)
			if err != nil || state.Validate() != nil || state.PinState != "cleared" {
				t.Fatal("original bounded cleanup did not resume")
			}
			replayed, err := reopened.CleanupDocumentPin(ctx, testProject, request.Intent.IdempotencyKey)
			if err != nil || replayed != state {
				t.Fatal("same-key cleanup was not idempotent")
			}
			view, err := reopened.Read(ctx, authority.ReadRequest{ProjectID: testProject, Kind: authority.Document, Operation: "get", EntityID: original.Receipt.Citation.EntityID, Revision: 1})
			if err != nil || len(view.Entities) != 1 {
				t.Fatal("cleanup lost original Git history bytes")
			}
		})
	}
}

func TestKnowledgePendingPinCannotBeEvicted(t *testing.T) {
	s, _, _ := readyDocumentStore(t)
	ctx := context.Background()
	request := documentRequest(t, "document.create", "synthetic-pending-cleanup", authority.EmptyKnowledgeRef, authority.Citation{}, "# Pending evidence")
	interrupted := errors.New("synthetic interruption")
	s.fault = func(at string) error {
		if at == "knowledge_document_bound" {
			return interrupted
		}
		return nil
	}
	if _, err := s.MutateDocument(ctx, request, testActor); !errors.Is(err, interrupted) {
		t.Fatal("test pending admission failed")
	}
	s.fault = nil
	state, err := s.DocumentOperationStatus(ctx, testProject, request.Intent.IdempotencyKey)
	if err != nil || state.Validate() != nil || state.Phase != "bound" {
		t.Fatal("pending status unavailable")
	}
	if _, err := s.CleanupDocumentPin(ctx, testProject, request.Intent.IdempotencyKey); err == nil {
		t.Fatal("cleanup evicted unresolved original operation")
	}
	k, err := loadKnowledgeSetup(ctx, s.db, testProject, s.instance, s.directory)
	if err != nil {
		t.Fatal("test setup binding failed")
	}
	pin, err := s.knowledgeRepository(k).readRef(filepath.Join("pins", string(state.OperationID)))
	if err != nil || pin == authority.EmptyKnowledgeRef {
		t.Fatal("pending pin was lost")
	}
	other := documentRequest(t, "document.create", "synthetic-other-new-key", authority.EmptyKnowledgeRef, authority.Citation{}, "# Other")
	_, err = s.MutateDocument(ctx, other, testActor)
	requireKnowledgeError(t, err, DocumentPending)
	createIssue(t, s, "synthetic-tracker-during-document-pending")
}

func TestKnowledgeOperationStatusRefusesSettledForeignPinEvidence(t *testing.T) {
	for _, scenario := range []string{"retained-equal-pair", "retained-conflicting-pair", "retained-lock-only", "cleared-final", "cleared-lock", "cleared-equal-pair", "cleared-conflicting-pair"} {
		t.Run(scenario, func(t *testing.T) {
			s, _, _ := readyDocumentStore(t)
			ctx := context.Background()
			key := "synthetic-status-foreign-pin"
			result, err := s.MutateDocument(ctx, documentRequest(t, "document.create", key, authority.EmptyKnowledgeRef, authority.Citation{}, "# Immutable status fixture"), testActor)
			if err != nil {
				t.Fatal("test original document failed")
			}
			cleared := strings.HasPrefix(scenario, "cleared-")
			if cleared {
				if _, err := s.CleanupDocumentPin(ctx, testProject, key); err != nil {
					t.Fatal("test original pin cleanup failed")
				}
			}
			pinPath := filepath.Join(s.directory, knowledgeDirectoryName, "refs", "pins", string(result.Receipt.OperationID))
			originalBytes := []byte(result.Result.Locator.CommitOID + "\n")
			final := !strings.HasSuffix(scenario, "lock-only") && scenario != "cleared-lock"
			lock := scenario != "cleared-final"
			if scenario == "retained-lock-only" {
				if os.Rename(pinPath, filepath.Join(s.directory, "preserved-status-pin")) != nil {
					t.Fatal("test pin relocation failed")
				}
			}
			if cleared && final {
				if os.WriteFile(pinPath, originalBytes, 0600) != nil {
					t.Fatal("test final pin recreation failed")
				}
			}
			lockBytes := originalBytes
			if strings.Contains(scenario, "conflicting") {
				lockBytes = []byte(strings.Repeat("a", 64) + "\n")
			}
			if lock {
				if os.WriteFile(pinPath+".lock", lockBytes, 0600) != nil {
					t.Fatal("test lock recreation failed")
				}
			}
			_, err = s.DocumentOperationStatus(ctx, testProject, key)
			requireKnowledgeError(t, err, KnowledgeCorrupt)
			state, err := s.KnowledgeStatus(ctx, testProject)
			if err != nil || state.Validate() != nil || state.Code != string(KnowledgeCorrupt) || state.Remediation != "operator_recovery" {
				t.Fatal("operation/global status disagreed about foreign pin evidence")
			}
			if final {
				if bytes, err := os.ReadFile(pinPath); err != nil || string(bytes) != string(originalBytes) {
					t.Fatal("read-only status rewrote finalized evidence")
				}
			}
			if lock {
				if bytes, err := os.ReadFile(pinPath + ".lock"); err != nil || string(bytes) != string(lockBytes) {
					t.Fatal("read-only status rewrote lock evidence")
				}
			}
		})
	}
}
