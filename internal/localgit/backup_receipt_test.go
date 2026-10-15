package localgit

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBundleReceiptRejectsDeleteReceiptForLiveEntity(t *testing.T) {
	ctx := context.Background()
	s := newWritable(t)
	snap := pinFixture(t, s)
	live, err := snap.Get(ctx, Issue, "ISSUE-1")
	if err != nil {
		t.Fatal("live fixture unavailable")
	}
	req := Mutation{Operation: "delete_issue", ID: "ISSUE-1", ExpectedRevision: live.Citation.Blob, WriteMode: "local", IdempotencyKey: "review-delete"}
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
	receipt := Receipt{Schema: 1, ProjectID: snap.project.ID, KeyHash: digest([]byte(req.IdempotencyKey)), IntentHash: digest(intent), Operation: req.Operation, Result: Reference{Issue, "ISSUE-1"}, ResultBlob: live.Citation.Blob}
	files := map[string]string{}
	for path := range snap.entries {
		data, _, e := snap.file(ctx, path, MaxFileBytes)
		if e != nil {
			t.Fatal("exact fixture read failed")
		}
		files[path] = string(data)
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
			t.Fatal("refused import retained invalid store")
		}
		return
	}
	replay, err := restored.Mutate(ctx, mainRef, req)
	if err != nil {
		t.Fatal("accepted receipt replay unavailable")
	}
	if replay.Replayed && replay.Entity.Metadata.State != "deleted" {
		t.Fatal("restored delete intent falsely replays a live entity")
	}
	t.Fatal("operation/result-inconsistent bundle accepted")
}
func TestBundleReceiptLegitimateDeleteReceiptRoundtrip(t *testing.T) {
	ctx := context.Background()
	s := newWritable(t)
	live, err := pinFixture(t, s).Get(ctx, Issue, "ISSUE-1")
	if err != nil {
		t.Fatal("fixture unavailable")
	}
	req := Mutation{Operation: "delete_issue", ID: "ISSUE-1", ExpectedRevision: live.Citation.Blob, WriteMode: "local", IdempotencyKey: "review-real-delete"}
	genuine := mutateOK(t, s, req)
	bundle := filepath.Join(t.TempDir(), "private.bundle")
	exported, err := s.ExportBundle(ctx, mainRef, "fixture-project", bundle)
	if err != nil {
		t.Fatal("legitimate export rejected")
	}
	restored, _, err := RestoreBundle(ctx, filepath.Join(t.TempDir(), "restored.git"), bundle, "fixture-project", mainRef, exported.Commit)
	if err != nil {
		t.Fatal("legitimate restore rejected")
	}
	replay, err := restored.Mutate(ctx, mainRef, req)
	if err != nil || !replay.Replayed || replay.Entity.Metadata.State != "deleted" || replay.Entity.Citation != genuine.Entity.Citation {
		t.Fatal("legitimate original delete replay failed")
	}
}

func TestBundleReceiptRejectsCreateReceiptForPreexistingEntity(t *testing.T) {
	ctx := context.Background()
	s := newWritable(t)
	snap := pinFixture(t, s)
	live, err := snap.Get(ctx, Issue, "ISSUE-1")
	if err != nil {
		t.Fatal("fixture unavailable")
	}
	req := Mutation{Operation: "create_issue", Title: live.Metadata.Title, Body: live.Body, Labels: live.Metadata.Labels, Milestone: live.Metadata.Milestone, Links: live.Metadata.Links, ExpectedHead: snap.commit, WriteMode: "local", IdempotencyKey: "review-create"}
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
	receipt := Receipt{Schema: 1, ProjectID: snap.project.ID, KeyHash: digest([]byte(req.IdempotencyKey)), IntentHash: digest(intent), Operation: req.Operation, Result: Reference{Issue, "ISSUE-1"}, ResultBlob: live.Citation.Blob}
	files := map[string]string{}
	for path := range snap.entries {
		data, _, e := snap.file(ctx, path, MaxFileBytes)
		if e != nil {
			t.Fatal("exact fixture read failed")
		}
		files[path] = string(data)
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
		t.Fatal("accepted replay failed")
	}
	if replay.Replayed && replay.Entity.Metadata.ID == "ISSUE-1" {
		t.Fatal("restored create intent falsely replays preexisting entity without counter allocation")
	}
	t.Fatal("inconsistent create origin accepted")
}
func TestBundleReceiptAcceptsLegitimateSameBlobUpdate(t *testing.T) {
	ctx := context.Background()
	s := newWritable(t)
	live, err := pinFixture(t, s).Get(ctx, Issue, "ISSUE-1")
	if err != nil {
		t.Fatal("fixture unavailable")
	}
	req := Mutation{Operation: "update_issue", ID: "ISSUE-1", Title: live.Metadata.Title, State: live.Metadata.State, Body: live.Body, Labels: live.Metadata.Labels, Milestone: live.Metadata.Milestone, Links: live.Metadata.Links, ExpectedRevision: live.Citation.Blob, WriteMode: "local", IdempotencyKey: "review-update"}
	genuine := mutateOK(t, s, req)
	second := req
	second.ExpectedRevision = genuine.Entity.Citation.Blob
	second.IdempotencyKey = "review-same-blob-update"
	same := mutateOK(t, s, second)
	if same.Entity.Citation.Blob != genuine.Entity.Citation.Blob {
		t.Fatal("same-blob positive fixture failed")
	}
	bundle := filepath.Join(t.TempDir(), "private.bundle")
	exported, err := s.ExportBundle(ctx, mainRef, "fixture-project", bundle)
	if err != nil {
		t.Fatal("legitimate export rejected")
	}
	restored, _, err := RestoreBundle(ctx, filepath.Join(t.TempDir(), "restored.git"), bundle, "fixture-project", mainRef, exported.Commit)
	if err != nil {
		t.Fatal("legitimate restore rejected")
	}
	replay, err := restored.Mutate(ctx, mainRef, second)
	if err != nil || !replay.Replayed || replay.Entity.Citation != same.Entity.Citation {
		t.Fatal("same-blob original update replay failed")
	}
}
