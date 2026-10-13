package localauthority

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"testing"

	"gitcode-mcp/internal/authority"
)

func TestRevisionAtSequenceFinitePointProbeBudget(t *testing.T) {
	for _, latest := range []uint64{1, 100, 1000, 10000, math.MaxInt64} {
		for _, fence := range []uint64{0, 1, latest / 2, latest} {
			probes := 0
			got, err := revisionAtSequence(context.Background(), latest, fence, func(revision uint64) (uint64, error) {
				probes++
				if revision < 1 || revision > latest {
					t.Fatal("lookup escaped contiguous revision domain")
				}
				return revision, nil
			})
			want := fence
			if want > latest {
				want = latest
			}
			if err != nil || got != want || probes < 1 || probes > 63 {
				t.Fatal("historical lookup did not retain exact result under finite probe budget")
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	probes := 0
	_, err := revisionAtSequence(ctx, 1, 1, func(revision uint64) (uint64, error) {
		probes++
		cancel()
		return revision, nil
	})
	if !errors.Is(err, context.Canceled) || probes != 1 {
		t.Fatal("last point lookup lost cancellation")
	}
	for _, latest := range []uint64{0, uint64(math.MaxInt64) + 1} {
		called := false
		_, err := revisionAtSequence(context.Background(), latest, 1, func(uint64) (uint64, error) { called = true; return 1, nil })
		if err == nil || called {
			t.Fatal("invalid revision domain performed work")
		}
	}
}

func TestSnapshotSparseHistoricalSlicesPreserveBoundedProgress(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	for i := 0; i < lexicalEntityBudget+2; i++ {
		mustMutate(t, s, request(t, "issue.create", fmt.Sprintf("sparse-history-%d", i), authority.IssuePayload{Title: "Example", Body: "keyword", State: "open"}))
	}
	first, err := s.Snapshot(ctx, SnapshotRequest{ProjectID: testProject, Limit: 1})
	if err != nil || first.NextCursor == "" {
		t.Fatal("physical fixture cursor failed")
	}
	data, _ := base64.RawURLEncoding.DecodeString(first.NextCursor)
	var c snapshotCursor
	if authority.DecodeJSON(data, &c) != nil {
		t.Fatal("fixture cursor decoding failed")
	}
	// A syntactically valid older sequence has no remaining historical rows.
	// Work still must stop after the physical candidate budget, not filter to end.
	c.Sequence = 0
	data, _ = json.Marshal(c)
	cursor := base64.RawURLEncoding.EncodeToString(data)
	page, err := s.Snapshot(ctx, SnapshotRequest{ProjectID: testProject, Limit: 25, Cursor: cursor})
	if err != nil || len(page.Entities) != 0 || page.NextCursor == "" || page.Scanned != 25 || page.Sequence != 0 {
		t.Fatal("empty historical slice scanned to end or lost progress")
	}
	query := "keyword"
	data, _ = json.Marshal(lexicalCursor{QueryHash: hash([]byte(query)), Snapshot: cursor})
	req := LexicalRequest{ProjectID: testProject, Query: query, Limit: 10, Cursor: base64.RawURLEncoding.EncodeToString(data)}
	result, err := s.LexicalSearch(ctx, req)
	if err != nil || result.Scanned != lexicalEntityBudget || len(result.Hits) != 0 || !result.Partial || result.NextCursor == "" || result.Sequence != 0 {
		t.Fatal("lexical budget ignored physically inspected excluded entities")
	}
	req.Cursor = result.NextCursor
	last, err := s.LexicalSearch(ctx, req)
	if err != nil || last.Scanned != 1 || len(last.Hits) != 0 || last.Partial || last.NextCursor != "" || last.Sequence != 0 {
		t.Fatal("empty historical continuation did not finish bounded traversal")
	}
}

func TestSnapshotHighHistoryKeepsExactOriginalRevision(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	initial := mustMutate(t, s, request(t, "issue.create", "high-history-create", authority.IssuePayload{Title: "Original", Body: "original body", State: "open"}))
	last := initial
	for i := 0; i < 64; i++ {
		last = mustMutate(t, s, request(t, "issue.update", fmt.Sprintf("high-history-edit-%d", i), authority.IssuePayload{Title: "Changed", Body: fmt.Sprintf("revision %d", i), State: "closed"}, last))
	}
	data, _ := json.Marshal(snapshotCursor{ProjectID: testProject, Sequence: initial.ProjectSequence, Last: "00000000-0000-4000-8000-000000000000"})
	page, err := s.Snapshot(ctx, SnapshotRequest{ProjectID: testProject, Limit: 1, Cursor: base64.RawURLEncoding.EncodeToString(data)})
	if err != nil || page.Scanned != 1 || len(page.Entities) != 1 || page.Entities[0].Entity.Citation != initial.Citation || page.NextCursor != "" {
		t.Fatal("bounded point lookup lost exact original citation")
	}
	var payload authority.IssuePayload
	if authority.DecodeJSON(page.Entities[0].Entity.Payload, &payload) != nil || payload.Title != "Original" || payload.Body != "original body" {
		t.Fatal("bounded historical lookup substituted current payload")
	}
}

func TestSnapshotConcurrentWritesPreserveCapturedSequence(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	createIssue(t, s, "concurrent-snapshot-first")
	createIssue(t, s, "concurrent-snapshot-second")
	all, err := s.Snapshot(ctx, SnapshotRequest{ProjectID: testProject, Limit: 2})
	if err != nil || len(all.Entities) != 2 {
		t.Fatal("concurrent fixture snapshot failed")
	}
	first, err := s.Snapshot(ctx, SnapshotRequest{ProjectID: testProject, Limit: 1})
	if err != nil || first.NextCursor == "" {
		t.Fatal("concurrent fixture continuation failed")
	}
	original := all.Entities[1].Entity
	update := authority.MutationRequest{Operation: "issue.update", Intent: authority.WriteIntent{ProjectID: testProject, WriteMode: "local", EntityID: original.Citation.EntityID, ExpectedRevision: original.Citation.Revision}, Payload: json.RawMessage(`{"title":"Concurrent change","body":"later content","state":"closed"}`)}
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 32; i++ {
			update.Intent.IdempotencyKey = fmt.Sprintf("concurrent-snapshot-update-%d", i)
			receipt, err := s.Mutate(ctx, update, testActor)
			if err != nil {
				done <- err
				return
			}
			update.Intent.ExpectedRevision = receipt.Citation.Revision
		}
		done <- nil
	}()
	var readFailed bool
	for i := 0; i < 32; i++ {
		page, err := s.Snapshot(ctx, SnapshotRequest{ProjectID: testProject, Limit: 1, Cursor: first.NextCursor})
		if err != nil || page.Sequence != first.Sequence || len(page.Entities) != 1 || page.Entities[0].Entity.Citation != original.Citation || string(page.Entities[0].Entity.Payload) != string(original.Payload) {
			readFailed = true
			break
		}
	}
	// Join the real writer even on assertion failure, before store teardown.
	if err := <-done; err != nil || readFailed {
		t.Fatal("concurrent writes changed captured sequence/content")
	}
}
