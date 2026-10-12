package localauthority

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"gitcode-mcp/internal/authority"
)

var testActor = Actor{ID: "synthetic-local-agent"}

func request(t *testing.T, operation, key string, payload any, existing ...authority.MutationReceipt) authority.MutationRequest {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal("fixture encoding failed")
	}
	m := authority.MutationRequest{Operation: operation, Payload: body, Intent: authority.WriteIntent{ProjectID: testProject, WriteMode: "local", IdempotencyKey: key}}
	if len(existing) != 0 {
		m.Intent.EntityID = existing[0].Citation.EntityID
		m.Intent.ExpectedRevision = existing[0].Citation.Revision
	}
	return m
}

func createIssue(t *testing.T, s *Store, key string) authority.MutationReceipt {
	t.Helper()
	r, err := s.Mutate(context.Background(), request(t, "issue.create", key, authority.IssuePayload{Title: "Synthetic task", Body: "synthetic evidence", State: "open"}), testActor)
	if err != nil {
		t.Fatalf("issue create failed: %T", err)
	}
	if r.Validate() != nil {
		t.Fatal("invalid confirmed receipt")
	}
	return r
}

func TestTrackerCanonicalReplayCASHistoryAndRestart(t *testing.T) {
	s, registry, dir := testStore(t)
	ctx := context.Background()
	m := request(t, "issue.create", "synthetic-key", authority.IssuePayload{Title: "Synthetic task", Body: "synthetic evidence", State: "open"})
	first, err := s.Mutate(ctx, m, testActor)
	if err != nil {
		t.Fatalf("create failed: %T", err)
	}
	m.Payload = json.RawMessage(`{ "state":"open", "body":"synthetic evidence", "title":"Synthetic task" }`)
	replay, err := s.Mutate(ctx, m, testActor)
	expected := first
	expected.Replayed = true
	if err != nil || !reflect.DeepEqual(expected, replay) {
		t.Fatal("canonical replay changed original receipt")
	}
	changed := request(t, "issue.create", "synthetic-key", authority.IssuePayload{Title: "Changed", Body: "synthetic evidence", State: "open"})
	_, err = s.Mutate(ctx, changed, testActor)
	var typed authority.Error
	if !errors.As(err, &typed) || typed.Code != authority.Conflict {
		t.Fatal("changed intent replay accepted")
	}
	update := request(t, "issue.update", "synthetic-update", authority.IssuePayload{Title: "Closed task", Body: "different evidence", State: "closed"}, first)
	second, err := s.Mutate(ctx, update, testActor)
	if err != nil || second.Citation.Revision != 2 || second.ProjectSequence != 2 {
		t.Fatal("CAS update failed")
	}
	stale := update
	stale.Intent.IdempotencyKey = "synthetic-stale"
	_, err = s.Mutate(ctx, stale, testActor)
	if !errors.As(err, &typed) || typed.Code != authority.RevisionConflict {
		t.Fatal("stale revision accepted")
	}
	get, err := s.Read(ctx, authority.ReadRequest{ProjectID: testProject, Kind: authority.Issue, EntityID: first.Citation.EntityID, Revision: 1, Operation: "get"})
	if err != nil || len(get.Entities) != 1 || get.Entities[0].DisplayAlias != "ISSUE-1" || !strings.Contains(string(get.Entities[0].Payload), "synthetic evidence") {
		t.Fatal("immutable original citation lost")
	}
	if err = s.Close(); err != nil {
		t.Fatal("close failed")
	}
	next, err := OpenWriter(ctx, registry, dir, testProject)
	if err != nil {
		t.Fatalf("restart failed: %T", err)
	}
	defer next.Close()
	replay, err = next.Mutate(ctx, m, testActor)
	if err != nil || !reflect.DeepEqual(expected, replay) {
		t.Fatal("restart/delayed replay replaced settled receipt")
	}
	newIssue := createIssue(t, next, "synthetic-new")
	if newIssue.ProjectSequence != 3 {
		t.Fatal("replay/conflict consumed project order")
	}
	view, err := next.Read(ctx, authority.ReadRequest{ProjectID: testProject, Kind: authority.Issue, EntityID: newIssue.Citation.EntityID, Operation: "get"})
	if err != nil || view.Entities[0].DisplayAlias != "ISSUE-2" {
		t.Fatal("alias reused or allocated on replay/conflict")
	}
	metadata, err := next.Metadata(ctx, first.Citation)
	if err != nil || metadata.ActorID != testActor.ID {
		t.Fatal("asserted actor metadata lost")
	}
}

func TestTrackerTransactionsRollbackEveryBoundary(t *testing.T) {
	for _, phase := range []string{"revision", "relations", "receipt", "generation"} {
		t.Run(phase, func(t *testing.T) {
			s, _, _ := testStore(t)
			s.fault = func(at string) error {
				if at == phase {
					return Error{Corrupt}
				}
				return nil
			}
			_, err := s.Mutate(context.Background(), request(t, "issue.create", "synthetic-rollback", authority.IssuePayload{Title: "Fixture", Body: "", State: "open"}), testActor)
			if err == nil {
				t.Fatal("fault did not interrupt transaction")
			}
			s.fault = nil
			for _, table := range []string{"entities", "revisions", "receipts", "audit", "relations"} {
				var count int
				if s.db.QueryRow("SELECT count(*) FROM "+table).Scan(&count) != nil || count != 0 {
					t.Fatal("partial authority escaped rollback")
				}
			}
			status, err := s.Status(context.Background())
			if err != nil || status.Generation != 1 || status.AuthorityGeneration != 0 {
				t.Fatal("rollback consumed generation")
			}
			r := createIssue(t, s, "synthetic-rollback")
			if r.ProjectSequence != 1 {
				t.Fatal("rollback reserved receipt or alias")
			}
		})
	}
}

func TestConcurrentSameKeyAndRevisionCAS(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	m := request(t, "issue.create", "synthetic-concurrent", authority.IssuePayload{Title: "Fixture", Body: "", State: "open"})
	var wg sync.WaitGroup
	receipts := make([]authority.MutationReceipt, 16)
	errs := make([]error, 16)
	for i := range receipts {
		wg.Add(1)
		go func(i int) { defer wg.Done(); receipts[i], errs[i] = s.Mutate(ctx, m, testActor) }(i)
	}
	wg.Wait()
	var first authority.MutationReceipt
	for i, r := range receipts {
		if errs[i] != nil {
			t.Fatal("same-key concurrent call failed")
		}
		if first.OperationID == "" {
			first = r
		}
		if r.OperationID != first.OperationID || r.ProjectSequence != 1 {
			t.Fatal("concurrent duplicate operation")
		}
	}
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.Mutate(ctx, request(t, "issue.update", fmt.Sprintf("synthetic-cas-%d", i), authority.IssuePayload{Title: "Updated fixture", Body: "", State: "open"}, first), testActor)
		}(i)
	}
	wg.Wait()
	success := 0
	for _, err := range errs {
		if err == nil {
			success++
			continue
		}
		var typed authority.Error
		if !errors.As(err, &typed) || typed.Code != authority.RevisionConflict {
			t.Fatal("CAS caller lost typed conflict")
		}
	}
	if success != 1 {
		t.Fatal("concurrent revision overwrite")
	}
}

func TestLostAcknowledgementReplaysWithoutDuplicate(t *testing.T) {
	s, _, _ := testStore(t)
	m := request(t, "issue.create", "synthetic-ack", authority.IssuePayload{Title: "Fixture", Body: "", State: "open"})
	s.fault = func(at string) error {
		if at == "acknowledgement" {
			return context.Canceled
		}
		return nil
	}
	if _, err := s.Mutate(context.Background(), m, testActor); !errors.Is(err, context.Canceled) {
		t.Fatal("lost ack fixture did not interrupt")
	}
	s.fault = nil
	r, err := s.Mutate(context.Background(), m, testActor)
	if err != nil || !r.Replayed || r.ProjectSequence != 1 {
		t.Fatal("ack loss duplicated committed operation")
	}
}

func TestReadOnlyAndDocumentWritesRefused(t *testing.T) {
	s, _, dir := testStore(t)
	ro, err := OpenReadOnly(context.Background(), dir, testProject)
	if err != nil {
		t.Fatal("read-only open failed")
	}
	defer ro.Close()
	m := request(t, "issue.create", "synthetic-ro", authority.IssuePayload{Title: "Fixture", Body: "", State: "open"})
	_, err = ro.Mutate(context.Background(), m, testActor)
	var typed Error
	if !errors.As(err, &typed) || typed.Code != ReadOnly {
		t.Fatal("read-only store admitted mutation")
	}
	doc := request(t, "document.create", "synthetic-doc", authority.DocumentPayload{Title: "Fixture", Markdown: "synthetic Markdown never belongs in SQLite"})
	_, err = s.Mutate(context.Background(), doc, testActor)
	var a authority.Error
	if !errors.As(err, &a) || a.Code != authority.Unsupported {
		t.Fatal("tracker persisted document bytes")
	}
}
