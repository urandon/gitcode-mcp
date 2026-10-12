package localauthority

import (
	"context"
	"errors"
	"gitcode-mcp/internal/authority"
	"testing"
)

func mustMutate(t *testing.T, s *Store, m authority.MutationRequest) authority.MutationReceipt {
	t.Helper()
	r, err := s.Mutate(context.Background(), m, testActor)
	if err != nil {
		t.Fatalf("fixture mutation failed: %T", err)
	}
	return r
}

func expectCode(t *testing.T, s *Store, m authority.MutationRequest, code authority.Code) {
	t.Helper()
	_, err := s.Mutate(context.Background(), m, testActor)
	var typed authority.Error
	if !errors.As(err, &typed) || typed.Code != code {
		t.Fatal("typed relation conflict missing")
	}
}

func TestTypedAssignmentsArchiveAndTombstoneHistory(t *testing.T) {
	s, _, _ := testStore(t)
	issue := createIssue(t, s, "synthetic-issue")
	label := mustMutate(t, s, request(t, "label.create", "synthetic-label", authority.LabelPayload{Name: "fixture", Color: "#123456"}))
	link := authority.LinkPayload{From: issue.Citation, To: label.Citation, Relation: "label"}
	r := mustMutate(t, s, request(t, "link.create", "synthetic-assignment", link))
	expectCode(t, s, request(t, "link.create", "synthetic-duplicate", link), authority.Conflict)
	mustMutate(t, s, request(t, "label.update", "synthetic-archive", authority.LabelPayload{Name: "fixture", Color: "#123456", Archived: true}, label))
	expectCode(t, s, request(t, "link.update", "synthetic-archived-target", link, r), authority.Conflict)
	tombstone := mustMutate(t, s, request(t, "link.tombstone", "synthetic-tombstone", struct{}{}, r))
	expectCode(t, s, request(t, "link.update", "synthetic-resurrect", link, tombstone), authority.Conflict)
	history, err := s.Read(context.Background(), authority.ReadRequest{ProjectID: testProject, Kind: authority.Link, EntityID: r.Citation.EntityID, Operation: "history"})
	if err != nil || len(history.Entities) != 2 || history.Entities[0].Tombstone || !history.Entities[1].Tombstone {
		t.Fatal("tombstone erased relationship history")
	}
	var active int
	if s.db.QueryRow("SELECT count(*) FROM relations").Scan(&active) != nil || active != 0 {
		t.Fatal("tombstone retained active assignment")
	}
	replacement := mustMutate(t, s, request(t, "label.create", "synthetic-other-label", authority.LabelPayload{Name: "other", Color: "#654321"}))
	link.To = replacement.Citation
	newLink := mustMutate(t, s, request(t, "link.create", "synthetic-reassign", link))
	view, err := s.Read(context.Background(), authority.ReadRequest{ProjectID: testProject, Kind: authority.Link, EntityID: newLink.Citation.EntityID, Operation: "get"})
	if err != nil || view.Entities[0].DisplayAlias != "LINK-2" {
		t.Fatal("tombstone reused display alias")
	}
}

func TestBlockCyclesAndImmutableCommentParent(t *testing.T) {
	s, _, _ := testStore(t)
	a := createIssue(t, s, "synthetic-a")
	b := createIssue(t, s, "synthetic-b")
	c := createIssue(t, s, "synthetic-c")
	for i, p := range []authority.LinkPayload{{From: a.Citation, To: b.Citation, Relation: "blocks"}, {From: b.Citation, To: c.Citation, Relation: "blocks"}} {
		mustMutate(t, s, request(t, "link.create", []string{"synthetic-ab", "synthetic-bc"}[i], p))
	}
	expectCode(t, s, request(t, "link.create", "synthetic-cycle", authority.LinkPayload{From: c.Citation, To: a.Citation, Relation: "blocks"}), authority.Conflict)
	expectCode(t, s, request(t, "link.create", "synthetic-self", authority.LinkPayload{From: a.Citation, To: a.Citation, Relation: "relates_to"}), authority.InvalidContract)
	comment := mustMutate(t, s, request(t, "comment.create", "synthetic-comment", authority.CommentPayload{Issue: a.Citation, Body: "synthetic evidence"}))
	expectCode(t, s, request(t, "comment.update", "synthetic-reparent", authority.CommentPayload{Issue: b.Citation, Body: "changed synthetic evidence"}, comment), authority.Conflict)
	missing := a.Citation
	missing.Revision = 999
	expectCode(t, s, request(t, "comment.create", "synthetic-missing", authority.CommentPayload{Issue: missing, Body: "synthetic evidence"}), authority.NoAuthority)
	wrong := authority.LinkPayload{From: a.Citation, To: b.Citation, Relation: "label"}
	expectCode(t, s, request(t, "link.create", "synthetic-kind", wrong), authority.InvalidContract)
}

func TestMilestoneMembershipIsSingularAndRevisionsImmutable(t *testing.T) {
	s, _, _ := testStore(t)
	issue := createIssue(t, s, "synthetic-member")
	a := mustMutate(t, s, request(t, "milestone.create", "synthetic-ma", authority.MilestonePayload{Title: "first", Description: "", DueDate: ""}))
	b := mustMutate(t, s, request(t, "milestone.create", "synthetic-mb", authority.MilestonePayload{Title: "second", Description: "", DueDate: ""}))
	p := authority.LinkPayload{From: issue.Citation, To: a.Citation, Relation: "milestone"}
	link := mustMutate(t, s, request(t, "link.create", "synthetic-membership", p))
	p.To = b.Citation
	expectCode(t, s, request(t, "link.create", "synthetic-second-membership", p), authority.Conflict)
	mustMutate(t, s, request(t, "link.update", "synthetic-move", p, link))
	for _, query := range []string{"UPDATE revisions SET payload='{}'", "DELETE FROM revisions", "UPDATE receipts SET intent_hash='changed'", "DELETE FROM receipts", "UPDATE audit SET operation='changed'", "DELETE FROM audit", "UPDATE entities SET alias_number=100", "DELETE FROM entities", "UPDATE project SET project_id='changed'"} {
		if _, err := s.db.Exec(query); err == nil {
			t.Fatal("immutable durable state could be overwritten")
		}
	}
}
