package localgit

import (
	"context"
	"strings"
	"testing"
)

func TestMutationPreviewValidatesWithoutPublishingObjectsOrReceipts(t *testing.T) {
	s := newWritable(t)
	ctx := context.Background()
	head := pinFixture(t, s).Commit()
	objects := fixtureGit(t, s.dir, "", "count-objects", "-v")
	req := createRequest(head, "preview-only")
	commit, err := s.PreviewMutation(ctx, mainRef, pinFixture(t, s).Project().ID, req)
	if err != nil || commit != head {
		t.Fatal("valid preview did not return the pinned revision")
	}
	if pinFixture(t, s).Commit() != head || fixtureGit(t, s.dir, "", "count-objects", "-v") != objects {
		t.Fatal("preview published Git objects or advanced the ref")
	}
	if _, _, err := pinFixture(t, s).Receipt(ctx, digest([]byte(req.IdempotencyKey))); err == nil {
		t.Fatal("preview created a durable receipt")
	}
	created := mutateOK(t, s, req)
	if created.Replayed || created.Entity.Metadata.ID != "ISSUE-3" {
		t.Fatal("preview consumed an identity or receipt")
	}
}

func TestMutationPreviewUsesWriterGuardsAndMetadataValidation(t *testing.T) {
	s := newWritable(t)
	ctx := context.Background()
	head := pinFixture(t, s).Commit()
	project := pinFixture(t, s).Project().ID
	objects := fixtureGit(t, s.dir, "", "count-objects", "-v")
	for _, edit := range []func(*Mutation){
		func(q *Mutation) { q.WriteMode = "live" },
		func(q *Mutation) { q.IdempotencyKey = "" },
		func(q *Mutation) { q.Body = "\x00" },
		func(q *Mutation) { q.State = "deleted" },
		func(q *Mutation) { q.ExpectedHead = "" },
		func(q *Mutation) { q.ExpectedHead = strings.Repeat("a", 40) },
	} {
		q := createRequest(head, "invalid-preview")
		edit(&q)
		if _, err := s.PreviewMutation(ctx, mainRef, project, q); err == nil {
			t.Fatal("preview accepted input rejected by the writer")
		}
	}
	q := Mutation{Operation: "update_issue", ID: "ISSUE-1", Title: "Draft", State: "open", WriteMode: "local", ExpectedRevision: strings.Repeat("b", 40), IdempotencyKey: "stale-preview"}
	_, err := s.PreviewMutation(ctx, mainRef, project, q)
	requireCode(t, err, "write_conflict")
	if pinFixture(t, s).Commit() != head || fixtureGit(t, s.dir, "", "count-objects", "-v") != objects {
		t.Fatal("rejected preview changed Git")
	}
}
