package localgit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const mainRef = "refs/heads/main"

func createRequest(head, key string) Mutation {
	return Mutation{Operation: "create_issue", Title: "Created issue", Body: "Created body.\n", ExpectedHead: head, IdempotencyKey: key, WriteMode: "local"}
}
func newWritable(t *testing.T) *Store {
	t.Helper()
	dir := newFixture(t)
	fixtureCommit(t, dir, fixtureFiles(), "")
	return openFixture(t, dir, Limits{})
}
func mutateOK(t *testing.T, s *Store, req Mutation) MutationResult {
	t.Helper()
	r, err := s.Mutate(context.Background(), mainRef, req)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestMutationOneCommitCreateUpdateCommentWikiAndTombstone(t *testing.T) {
	s := newWritable(t)
	before := pinFixture(t, s)
	r := mutateOK(t, s, createRequest(before.Commit(), "create-1"))
	if r.Replayed || r.Entity.Metadata.ID != "ISSUE-3" {
		t.Fatal("create identity/replay mismatch")
	}
	created := pinFixture(t, s)
	if created.Project().NextIssue != 4 {
		t.Fatal("counter not published")
	}
	parent := fixtureGit(t, s.dir, "", "rev-parse", created.Commit()+"^")
	if parent != before.Commit() || created.Commit() != r.Entity.Citation.Commit {
		t.Fatal("not one parented mutation commit")
	}
	receipt, _, err := created.Receipt(context.Background(), digest([]byte("create-1")))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ResultBlob != r.Entity.Citation.Blob || receipt.Result.ID != r.Entity.Metadata.ID {
		t.Fatal("receipt is not in same commit")
	}
	u := Mutation{Operation: "update_issue", ID: "ISSUE-3", Title: "Updated", Body: "Updated body.\n", State: "closed", Labels: []string{"done"}, ExpectedRevision: r.Entity.Citation.Blob, IdempotencyKey: "update-1", WriteMode: "local"}
	updated := mutateOK(t, s, u)
	comment := mutateOK(t, s, Mutation{Operation: "add_comment", ParentID: "ISSUE-3", Title: "Result", Body: "Comment.\n", ExpectedHead: pinFixture(t, s).Commit(), IdempotencyKey: "comment-1", WriteMode: "local"})
	if comment.Entity.Metadata.ID != "COMMENT-2" || comment.Entity.Metadata.ParentID != "ISSUE-3" {
		t.Fatal("comment identity mismatch")
	}
	wiki := mutateOK(t, s, Mutation{Operation: "create_wiki", Title: "Page", Body: "Wiki.\n", ExpectedHead: pinFixture(t, s).Commit(), IdempotencyKey: "wiki-1", WriteMode: "local"})
	if wiki.Entity.Metadata.ID != "DOC-2" {
		t.Fatal("wiki identity mismatch")
	}
	wikiUpdated := mutateOK(t, s, Mutation{Operation: "update_wiki", ID: "DOC-2", Title: "Page updated", Body: "Wiki updated.\n", State: "active", ExpectedRevision: wiki.Entity.Citation.Blob, IdempotencyKey: "wiki-update", WriteMode: "local"})
	commentUpdated := mutateOK(t, s, Mutation{Operation: "update_comment", ID: "COMMENT-2", Title: "Result updated", Body: "Comment updated.\n", State: "active", ExpectedRevision: comment.Entity.Citation.Blob, IdempotencyKey: "comment-update", WriteMode: "local"})
	for _, target := range []struct {
		op string
		e  Entity
	}{{"delete_wiki", wikiUpdated.Entity}, {"delete_comment", commentUpdated.Entity}} {
		r := mutateOK(t, s, Mutation{Operation: target.op, ID: target.e.Metadata.ID, ExpectedRevision: target.e.Citation.Blob, IdempotencyKey: target.op, WriteMode: "local"})
		if r.Entity.Metadata.State != "deleted" || r.Entity.Body != "" {
			t.Fatal("missing typed tombstone")
		}
	}
	del := mutateOK(t, s, Mutation{Operation: "delete_issue", ID: "ISSUE-3", ExpectedRevision: updated.Entity.Citation.Blob, IdempotencyKey: "delete-1", WriteMode: "local"})
	if del.Entity.Metadata.State != "deleted" || del.Entity.Body != "" {
		t.Fatal("missing tombstone")
	}
	if pinFixture(t, s).Project().NextIssue != 4 {
		t.Fatal("tombstone recycled ID")
	}
	for _, id := range []string{"delete-1", "create-1"} {
		if _, _, err := pinFixture(t, s).Receipt(context.Background(), digest([]byte(id))); err != nil {
			t.Fatal("receipt pruned")
		}
	}
}

func TestMutationReplayBeforeGuardsOriginalResultAndDifferentIntent(t *testing.T) {
	s := newWritable(t)
	req := createRequest(pinFixture(t, s).Commit(), "same-key")
	first := mutateOK(t, s, req)
	mutateOK(t, s, Mutation{Operation: "update_issue", ID: first.Entity.Metadata.ID, Title: "Later title", Body: "Later.\n", State: "open", ExpectedRevision: first.Entity.Citation.Blob, IdempotencyKey: "later", WriteMode: "local"})
	head := pinFixture(t, s).Commit()
	replayed := mutateOK(t, s, req) // old expected HEAD must not reject known receipt.
	if !replayed.Replayed || replayed.Entity.Citation != first.Entity.Citation || replayed.Entity.Body != first.Entity.Body || replayed.Entity.Metadata.Title != first.Entity.Metadata.Title {
		t.Fatal("replay did not return original result")
	}
	if pinFixture(t, s).Commit() != head {
		t.Fatal("replay created commit")
	}
	req.ExpectedHead = head
	if !mutateOK(t, s, req).Replayed {
		t.Fatal("guard change altered logical replay")
	}
	req.Body = "Different intent.\n"
	_, err := s.Mutate(context.Background(), mainRef, req)
	requireCode(t, err, "idempotency_conflict")
}

func TestMutationGuardsAndRejectedPayloadLeaveRefUnchanged(t *testing.T) {
	s := newWritable(t)
	head := pinFixture(t, s).Commit()
	for _, edit := range []func(*Mutation){
		func(r *Mutation) { r.WriteMode = "live" }, func(r *Mutation) { r.IdempotencyKey = "" }, func(r *Mutation) { r.ID = "ISSUE-99" }, func(r *Mutation) { r.State = "deleted" }, func(r *Mutation) { r.Body = "\x00" }, func(r *Mutation) { r.ExpectedHead = "" },
	} {
		req := createRequest(head, "invalid")
		edit(&req)
		if _, err := s.Mutate(context.Background(), mainRef, req); err == nil {
			t.Fatal("invalid payload accepted")
		}
		if pinFixture(t, s).Commit() != head {
			t.Fatal("invalid payload published")
		}
	}
	req := createRequest(strings.Repeat("a", 40), "stale-head")
	_, err := s.Mutate(context.Background(), mainRef, req)
	requireCode(t, err, "write_conflict")
	old, err := pinFixture(t, s).Get(context.Background(), Issue, "ISSUE-1")
	if err != nil {
		t.Fatal(err)
	}
	u := Mutation{Operation: "update_issue", ID: "ISSUE-1", Title: "Changed", State: "open", ExpectedRevision: strings.Repeat("b", 40), IdempotencyKey: "stale-blob", WriteMode: "local"}
	_, err = s.Mutate(context.Background(), mainRef, u)
	requireCode(t, err, "write_conflict")
	if pinFixture(t, s).Commit() != head || old.Citation.Commit != head {
		t.Fatal("stale guard published")
	}
}

func TestMutationCancellationAndAmbiguousReplyRecovery(t *testing.T) {
	s := newWritable(t)
	initial := pinFixture(t, s).Commit()
	ctx, cancel := context.WithCancel(context.Background())
	s.hooks = &mutationHooks{beforePublish: func() error { cancel(); return nil }}
	req := createRequest(initial, "before-cancel")
	_, err := s.Mutate(ctx, mainRef, req)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("pre-publication cancellation lost")
	}
	if pinFixture(t, s).Commit() != initial {
		t.Fatal("cancelled before ref changed")
	}
	s.hooks = &mutationHooks{afterPublish: func() error { return errors.New("private reply sentinel") }}
	req = createRequest(initial, "lost-reply")
	_, err = s.Mutate(context.Background(), mainRef, req)
	requireCode(t, err, "publication_ambiguous")
	if strings.Contains(err.Error(), "sentinel") {
		t.Fatal("private error leaked")
	}
	committed := pinFixture(t, s).Commit()
	if committed == initial {
		t.Fatal("lost reply fixture did not publish")
	}
	// Recovery in a fresh store proves that no process-local claim is authority.
	fresh := openFixture(t, s.dir, Limits{})
	r := mutateOK(t, fresh, req)
	if !r.Replayed || r.Entity.Citation.Commit != committed || pinFixture(t, fresh).Commit() != committed {
		t.Fatal("unsafe ambiguous reply recovery")
	}
}

func TestMutationRealCASConflictAndCanonicalStoreWriterLane(t *testing.T) {
	s := newWritable(t)
	head := pinFixture(t, s).Commit()
	other := fixtureCommit(t, s.dir, fixtureFiles(), head)
	fixtureGit(t, s.dir, "", "update-ref", mainRef, head)
	s.hooks = &mutationHooks{beforePublish: func() error { fixtureGit(t, s.dir, "", "update-ref", mainRef, other); return nil }}
	_, err := s.Mutate(context.Background(), mainRef, createRequest(head, "cas-race"))
	requireCode(t, err, "write_conflict")
	if pinFixture(t, s).Commit() != other {
		t.Fatal("CAS overwrote concurrent ref")
	}
	s.hooks = nil
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(s.dir, alias); err != nil {
		t.Skip("symlink unavailable")
	}
	second := openFixture(t, alias, Limits{})
	admitted, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var once sync.Once
	releaseWriter := func() { once.Do(func() { close(release) }) }
	defer releaseWriter()
	s.hooks = &mutationHooks{beforePublish: func() error {
		close(admitted)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	done := make(chan error, 1)
	go func() {
		_, err := s.Mutate(ctx, mainRef, createRequest(other, "writer-one"))
		done <- err
	}()
	select {
	case <-admitted:
	case err := <-done:
		t.Fatalf("writer failed before barrier: %v", err)
	case <-ctx.Done():
		t.Fatal("writer admission timed out")
	}
	_, err = second.Mutate(context.Background(), mainRef, createRequest(other, "writer-two"))
	requireCode(t, err, "writer_busy")
	releaseWriter()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("writer completion timed out")
	}
	req := createRequest(pinFixture(t, second).Commit(), "writer-two")
	r := mutateOK(t, second, req)
	if r.Entity.Metadata.ID != "ISSUE-4" || pinFixture(t, s).Project().NextIssue != 5 {
		t.Fatal("concurrent allocation reused ID")
	}
}

func TestMutationReceiptOriginExhaustionDoesNotBlockNewKeys(t *testing.T) {
	s := newWritable(t)
	old := createRequest(pinFixture(t, s).Commit(), "old-key")
	mutateOK(t, s, old)
	mutateOK(t, s, createRequest(pinFixture(t, s).Commit(), "newer-key"))
	limited := openFixture(t, s.dir, Limits{History: 2})
	head := pinFixture(t, limited).Commit()
	_, err := limited.Mutate(context.Background(), mainRef, old)
	requireCode(t, err, "history_unavailable")
	if pinFixture(t, limited).Commit() != head {
		t.Fatal("history exhaustion produced duplicate")
	}
	mutateOK(t, limited, createRequest(head, "fresh-key"))
}

func TestMutationCapacityAndImplicitDeleteNeverPublish(t *testing.T) {
	s := newWritable(t)
	head := pinFixture(t, s).Commit()
	for _, req := range []Mutation{
		{Operation: "update_issue", ID: "ISSUE-1", Title: "Forbidden tombstone", State: "deleted", ExpectedHead: head, IdempotencyKey: "implicit-delete", WriteMode: "local"},
		{Operation: "create_issue", Title: strings.Repeat("x", 513), ExpectedHead: head, IdempotencyKey: "long-title", WriteMode: "local"},
		{Operation: "create_issue", Title: "Too many labels", Labels: make([]string, 33), ExpectedHead: head, IdempotencyKey: "labels", WriteMode: "local"},
		{Operation: "create_wiki", Title: "Wrong metadata", Labels: []string{"issue-only"}, ExpectedHead: head, IdempotencyKey: "wiki-labels", WriteMode: "local"},
	} {
		if _, err := s.Mutate(context.Background(), mainRef, req); err == nil {
			t.Fatal("invalid metadata accepted")
		}
	}
	// All five existing files fit; adding entity + receipt exceeds the tree cap.
	limited := openFixture(t, s.dir, Limits{Entries: 6})
	_, err := limited.Mutate(context.Background(), mainRef, createRequest(head, "entry-cap"))
	requireCode(t, err, "limit_exceeded")
	limited = openFixture(t, s.dir, Limits{FileBytes: 256})
	req := createRequest(head, "file-cap")
	req.Body = strings.Repeat("x", 250) // body fits, serialized entity does not.
	_, err = limited.Mutate(context.Background(), mainRef, req)
	requireCode(t, err, "limit_exceeded")
	if pinFixture(t, s).Commit() != head {
		t.Fatal("rejected candidate published")
	}
}

func TestMutationIgnoresHooksSigningAndMachineAuthor(t *testing.T) {
	s := newWritable(t)
	sentinel := filepath.Join(t.TempDir(), "hook-ran")
	hook := "#!/bin/sh\nprintf ran > '" + sentinel + "'\nexit 1\n"
	for _, name := range []string{"reference-transaction", "pre-commit", "post-commit"} {
		if err := os.WriteFile(filepath.Join(s.dir, "hooks", name), []byte(hook), 0700); err != nil {
			t.Fatal("hook fixture unavailable")
		}
	}
	fixtureGit(t, s.dir, "", "config", "user.name", "Private machine identity")
	fixtureGit(t, s.dir, "", "config", "user.email", "private@example.invalid")
	fixtureGit(t, s.dir, "", "config", "commit.gpgsign", "true")
	fixtureGit(t, s.dir, "", "config", "gpg.program", filepath.Join(s.dir, "hooks", "pre-commit"))
	r := mutateOK(t, s, createRequest(pinFixture(t, s).Commit(), "isolated-write"))
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("Git write executed machine helper")
	}
	commit, err := s.run(context.Background(), 1024, "cat-file", "commit", r.Entity.Citation.Commit)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(commit), "Private") || strings.Contains(string(commit), "private@example") || !strings.Contains(string(commit), "gitcode-mcp <local@example.invalid>") {
		t.Fatal("machine author entered project history")
	}
}

func TestMutationRegisteredProjectGuardAndExactBranch(t *testing.T) {
	s := newWritable(t)
	head := pinFixture(t, s).Commit()
	req := createRequest(head, "project-guard")
	_, err := s.MutateProject(context.Background(), mainRef, "different-project", req)
	requireCode(t, err, "invalid_configuration")
	if pinFixture(t, s).Commit() != head {
		t.Fatal("foreign registration changed authority")
	}
	fixtureGit(t, s.dir, "", "symbolic-ref", mainRef+"-alias", mainRef)
	if err := s.ValidateLocalBranch(context.Background(), mainRef); err != nil {
		t.Fatal("unrelated prefixed branch invalidated exact authority")
	}
	requireCode(t, s.ValidateLocalBranch(context.Background(), mainRef+"-alias"), "unsupported_store")
}
