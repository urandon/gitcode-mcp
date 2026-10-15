package localgit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBundleReceiptRejectsPriorDeletedStateReceipts(t *testing.T) {
	for _, cut := range []string{"add_to_deleted_parent", "revive_deleted_target"} {
		t.Run(cut, func(t *testing.T) {
			ctx := context.Background()
			s := newWritable(t)
			snap := pinFixture(t, s)
			old, err := snap.Get(ctx, Issue, "ISSUE-2")
			if err != nil || old.Metadata.State != "deleted" {
				t.Fatal("deleted fixture unavailable")
			}
			req := Mutation{Operation: "add_comment", ParentID: "ISSUE-2", Title: "Note", Body: "Evidence", ExpectedHead: snap.commit, WriteMode: "local", IdempotencyKey: "review-state"}
			outcome := Entity{Metadata: Metadata{Schema: 1, Kind: Comment, ID: "COMMENT-2", ParentID: "ISSUE-2", Title: "Note", State: "active"}, Body: "Evidence"}
			if cut == "revive_deleted_target" {
				req = Mutation{Operation: "update_issue", ID: "ISSUE-2", Title: "Revived", State: "open", Body: "Evidence", ExpectedRevision: old.Citation.Blob, WriteMode: "local", IdempotencyKey: "review-state"}
				outcome = Entity{Metadata: Metadata{Schema: 1, Kind: Issue, ID: "ISSUE-2", Title: "Revived", State: "open"}, Body: "Evidence"}
			}
			_, err = s.Mutate(ctx, mainRef, req)
			requireCode(t, err, "write_conflict")
			if pinFixture(t, s).commit != snap.commit {
				t.Fatal("rejected original intent published")
			}
			logical := req
			logical.ExpectedHead = ""
			logical.ExpectedRevision = ""
			logical.IdempotencyKey = ""
			logical.WriteMode = ""
			intent, _ := json.Marshal(struct {
				Project  string   `json:"project"`
				Ref      string   `json:"ref"`
				Mutation Mutation `json:"mutation"`
			}{snap.project.ID, mainRef, logical})
			data, err := s.renderMutationEntity(outcome)
			if err != nil {
				t.Fatal("outcome fixture invalid")
			}
			blob, err := s.writeBlob(ctx, data)
			if err != nil {
				t.Fatal("blob fixture unavailable")
			}
			receipt := Receipt{Schema: 1, ProjectID: snap.project.ID, KeyHash: digest([]byte(req.IdempotencyKey)), IntentHash: digest(intent), Operation: req.Operation, Result: Reference{outcome.Metadata.Kind, outcome.Metadata.ID}, ResultBlob: blob}
			files := map[string]string{}
			for path := range snap.entries {
				b, _, e := snap.file(ctx, path, MaxFileBytes)
				if e != nil {
					t.Fatal("exact fixture read failed")
				}
				files[path] = string(b)
			}
			path, _ := entityPath(outcome.Metadata.Kind, outcome.Metadata.ID, outcome.Metadata.ParentID)
			files[path] = string(data)
			// Replace only the counter needed for the add; identity and all existing blobs remain exact.
			if cut == "add_to_deleted_parent" {
				files[".harness/project.yaml"] = "schema: 1\nproject_id: fixture-project\nnext_issue: 3\nnext_comment: 3\nnext_document: 2\n"
			}
			raw, _ := json.Marshal(receipt)
			files[".harness/receipts/"+receipt.KeyHash+".json"] = string(raw)
			head := fixtureCommit(t, s.dir, files, snap.commit)
			bundle := filepath.Join(t.TempDir(), "private.bundle")
			fixtureGit(t, s.dir, "", "bundle", "create", bundle, mainRef)
			target := filepath.Join(t.TempDir(), "restored.git")
			restored, _, err := RestoreBundle(ctx, target, bundle, snap.project.ID, mainRef, head)
			if err != nil {
				requireCode(t, err, "invalid_schema")
				if _, e := os.Stat(target); !os.IsNotExist(e) {
					t.Fatal("invalid import retained")
				}
				return
			}
			replay, err := restored.Mutate(ctx, mainRef, req)
			if err != nil {
				t.Fatal("accepted receipt replay unavailable")
			}
			if replay.Replayed {
				t.Fatal("restored receipt falsely acknowledges an operation forbidden by prior deleted state")
			}
			t.Fatal("invalid prior-state receipt accepted")
		})
	}
}
func TestBundleReceiptKeepsCommentReceiptAfterLaterParentDeletion(t *testing.T) {
	ctx := context.Background()
	s := newWritable(t)
	snap := pinFixture(t, s)
	parent, err := snap.Get(ctx, Issue, "ISSUE-1")
	if err != nil {
		t.Fatal("parent fixture unavailable")
	}
	req := Mutation{Operation: "add_comment", ParentID: "ISSUE-1", Title: "Note", Body: "Evidence", ExpectedHead: snap.commit, WriteMode: "local", IdempotencyKey: "review-real-note"}
	genuine := mutateOK(t, s, req)
	mutateOK(t, s, Mutation{Operation: "delete_issue", ID: "ISSUE-1", ExpectedRevision: parent.Citation.Blob, WriteMode: "local", IdempotencyKey: "review-real-delete-parent"})
	bundle := filepath.Join(t.TempDir(), "private.bundle")
	exported, err := s.ExportBundle(ctx, mainRef, "fixture-project", bundle)
	if err != nil {
		t.Fatal("genuine export rejected")
	}
	restored, _, err := RestoreBundle(ctx, filepath.Join(t.TempDir(), "restored.git"), bundle, "fixture-project", mainRef, exported.Commit)
	if err != nil {
		t.Fatal("genuine restore rejected")
	}
	replay, err := restored.Mutate(ctx, mainRef, req)
	if err != nil || !replay.Replayed || replay.Entity.Citation != genuine.Entity.Citation {
		t.Fatal("later parent deletion invalidated original comment receipt")
	}
}
