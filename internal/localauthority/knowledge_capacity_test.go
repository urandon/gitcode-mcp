package localauthority

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"gitcode-mcp/internal/authority"
)

func TestKnowledgeDocumentAdmissionExactCapacityBoundaries(t *testing.T) {
	for _, sample := range []struct {
		active, pins, objects int
		create, pass          bool
	}{
		{4095, 63, maxKnowledgeLooseObjects - 4, true, true},
		{4096, 63, maxKnowledgeLooseObjects - 4, true, false},
		{4096, 63, maxKnowledgeLooseObjects - 4, false, true},
		{0, 64, 0, true, false},
		{1, 0, maxKnowledgeLooseObjects - 3, false, false},
	} {
		if err := knowledgeAdmissionCapacity(sample.active, sample.pins, sample.objects, sample.create); (err == nil) != sample.pass {
			t.Fatal("exact capacity boundary changed")
		}
	}
	s, _, _ := readyDocumentStore(t)
	request := documentRequest(t, "document.create", "synthetic-encoded-capacity", authority.EmptyKnowledgeRef, authority.Citation{}, strings.Repeat("\\", 258100))
	_, err := s.MutateDocument(context.Background(), request, testActor)
	requireKnowledgeError(t, err, Capacity)
	var operations, next int
	if s.db.QueryRow(`SELECT count(*) FROM document_operations`).Scan(&operations) != nil || operations != 0 || s.db.QueryRow(`SELECT next_number FROM aliases WHERE kind='document'`).Scan(&next) != nil || next != 1 {
		t.Fatal("encoded reply capacity consumed a key/alias")
	}
}

func TestKnowledgeRetainedPinCapacityNoEvictionAndExplicitRelief(t *testing.T) {
	s, _, _ := readyDocumentStore(t)
	ctx := context.Background()
	ref := authority.EmptyKnowledgeRef
	var citation authority.Citation
	for i := 0; i < maxRetainedKnowledgePins; i++ {
		operation := "document.update"
		if i == 0 {
			operation = "document.create"
		}
		request := documentRequest(t, operation, "synthetic-pin-capacity-"+strconv.Itoa(i), ref, citation, "# Exact capacity document")
		result, err := s.MutateDocument(ctx, request, testActor)
		if err != nil {
			t.Fatal("allowed retained-pin admission failed")
		}
		ref, citation = result.Result.Locator.CommitOID, result.Receipt.Citation
	}
	request := documentRequest(t, "document.update", "synthetic-pin-capacity-blocked", ref, citation, "# Exact capacity document")
	_, err := s.MutateDocument(ctx, request, testActor)
	requireKnowledgeError(t, err, Capacity)
	key := hash([]byte(string(testProject) + "\x00" + request.Intent.IdempotencyKey))
	var count int
	if s.db.QueryRow(`SELECT count(*) FROM document_operations WHERE key_hash=?`, key).Scan(&count) != nil || count != 0 {
		t.Fatal("capacity rejection consumed original key")
	}
	k, err := loadKnowledgeSetup(ctx, s.db, testProject, s.instance, s.directory)
	if err != nil {
		t.Fatal("test setup unavailable")
	}
	_, pins, err := s.knowledgeRepository(k).verifyLayout()
	if err != nil || pins != maxRetainedKnowledgePins {
		t.Fatal("capacity admission evicted original evidence")
	}
	if _, err := s.CleanupDocumentPin(ctx, testProject, "synthetic-pin-capacity-0"); err != nil {
		t.Fatal("bounded explicit cleanup did not free safe retained pin")
	}
	if _, err := s.MutateDocument(ctx, request, testActor); err != nil {
		t.Fatal("non-admitted request could not proceed after explicit capacity relief")
	}
}
