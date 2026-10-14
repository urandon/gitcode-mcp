package localgit

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBundleRoundtripExactHistoryAndOriginalReceipt(t *testing.T) {
	ctx := context.Background()
	s, err := Initialize(ctx, filepath.Join(t.TempDir(), "source.git"), "lab", mainRef)
	if err != nil {
		t.Fatal("initialize fixture")
	}
	req := createRequest(pinFixture(t, s).Commit(), "question")
	first := mutateOK(t, s, req)
	updated := mutateOK(t, s, Mutation{Operation: "update_issue", ID: "ISSUE-1", Title: "Answered", Body: "Later evidence", State: "closed", ExpectedRevision: first.Entity.Citation.Blob, WriteMode: "local", IdempotencyKey: "answer"})
	mutateOK(t, s, Mutation{Operation: "add_comment", ParentID: "ISSUE-1", Title: "Note", Body: "Comment evidence", ExpectedHead: updated.Entity.Citation.Commit, WriteMode: "local", IdempotencyKey: "note"})
	page := mutateOK(t, s, Mutation{Operation: "create_wiki", Title: "Page", Body: "Wiki evidence", ExpectedHead: pinFixture(t, s).Commit(), WriteMode: "local", IdempotencyKey: "page"})
	mutateOK(t, s, Mutation{Operation: "delete_wiki", ID: "DOC-1", ExpectedRevision: page.Entity.Citation.Blob, WriteMode: "local", IdempotencyKey: "delete-page"})
	head := pinFixture(t, s).Commit()
	bundle := filepath.Join(t.TempDir(), "project.bundle")
	exported, err := s.ExportBundle(ctx, mainRef, "lab", bundle)
	if err != nil || exported.Commit != head || exported.Commits != 6 || exported.Status != "exported" {
		t.Fatalf("export unavailable: %v; commits=%d status=%s", err, exported.Commits, exported.Status)
	}
	info, _ := os.Stat(bundle)
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal("bundle permissions")
	}
	if _, err = s.ExportBundle(ctx, mainRef, "lab", bundle); err == nil {
		t.Fatal("bundle overwrite accepted")
	}
	target := filepath.Join(t.TempDir(), "restored.git")
	restored, result, err := RestoreBundle(ctx, target, bundle, "lab", mainRef, head)
	if err != nil || result.Commit != head || result.Commits != exported.Commits || result.Status != "restored" {
		t.Fatal("restore unavailable")
	}
	if fixtureGit(t, target, "", "rev-parse", "HEAD") != head {
		t.Fatal("restored ref changed")
	}
	for _, kind := range []Kind{Issue, Comment, Wiki} {
		a, _ := pinFixture(t, s).List(ctx, kind, 0, 20)
		b, e := pinFixture(t, restored).List(ctx, kind, 0, 20)
		x, _ := json.Marshal(a)
		y, _ := json.Marshal(b)
		if e != nil || string(x) != string(y) {
			t.Fatal("restored bytes/citations differ")
		}
	}
	a, _ := pinFixture(t, s).History(ctx, Issue, "ISSUE-1", 64)
	b, e := pinFixture(t, restored).History(ctx, Issue, "ISSUE-1", 64)
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	if e != nil || string(x) != string(y) {
		t.Fatal("restored history differs")
	}
	replay, err := restored.Mutate(ctx, mainRef, req)
	if err != nil || !replay.Replayed || replay.Entity.Citation != first.Entity.Citation || replay.Entity.Body != first.Entity.Body || pinFixture(t, restored).Commit() != head {
		t.Fatal("original receipt lost")
	}
	if _, _, err = RestoreBundle(ctx, target, bundle, "lab", mainRef, head); err == nil {
		t.Fatal("existing store overwritten")
	}
}

func TestBundleRestoreRejectsUntrustedBoundaries(t *testing.T) {
	ctx := context.Background()
	s := newWritable(t)
	head := pinFixture(t, s).Commit()
	bundle := filepath.Join(t.TempDir(), "project.bundle")
	if _, err := s.ExportBundle(ctx, mainRef, "fixture-project", bundle); err != nil {
		t.Fatal("fixture export")
	}
	for _, tc := range []struct{ name, id, ref, head string }{
		{"identity", "other", mainRef, head}, {"head", "fixture-project", mainRef, strings.Repeat("a", 40)}, {"ref", "fixture-project", "refs/heads/other", head},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "restore.git")
			if _, _, err := RestoreBundle(ctx, target, bundle, tc.id, tc.ref, tc.head); err == nil {
				t.Fatal("mismatched restore accepted")
			}
			if _, e := os.Stat(target); !os.IsNotExist(e) {
				t.Fatal("invalid store retained")
			}
		})
	}
	for _, name := range []string{"truncated", "extra-ref", "pack-fanout", "incremental"} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "invalid.bundle")
			switch name {
			case "truncated":
				data, _ := os.ReadFile(bundle)
				os.WriteFile(file, data[:len(data)/2], 0600)
			case "extra-ref":
				fixtureGit(t, s.dir, "", "update-ref", "refs/heads/extra", head)
				fixtureGit(t, s.dir, "", "bundle", "create", file, mainRef, "refs/heads/extra")
			case "pack-fanout":
				data, _ := os.ReadFile(bundle)
				pos := strings.Index(string(data), "\n\nPACK") + 2
				if pos < 2 {
					t.Fatal("pack fixture header")
				}
				binary.BigEndian.PutUint32(data[pos+8:pos+12], 4*MaxEntries+MaxHistory+1)
				os.WriteFile(file, data, 0600)
			case "incremental":
				next := mutateOK(t, s, createRequest(head, "incremental"))
				fixtureGit(t, s.dir, "", "bundle", "create", file, mainRef, "^"+head)
				head = next.Entity.Citation.Commit
			}
			target := filepath.Join(t.TempDir(), "restore.git")
			if _, _, err := RestoreBundle(ctx, target, file, "fixture-project", mainRef, head); err == nil {
				t.Fatal("invalid bundle accepted")
			}
			if _, e := os.Stat(target); !os.IsNotExist(e) {
				t.Fatal("invalid import retained")
			}
		})
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := RestoreBundle(ctx, filepath.Join(t.TempDir(), "cancel.git"), bundle, "fixture-project", mainRef, head); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost")
	}
}

func TestBundleSHA256AndCounterHistory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sha256.git")
	fixtureGit(t, dir, "", "init", "--bare", "--object-format=sha256", dir)
	head := fixtureCommit(t, dir, fixtureFiles(), "")
	if len(head) != 64 {
		t.Fatal("SHA256 fixture")
	}
	s := openFixture(t, dir, Limits{})
	bundle := filepath.Join(t.TempDir(), "private.bundle")
	if _, err := s.ExportBundle(context.Background(), mainRef, "fixture-project", bundle); err != nil {
		t.Fatal("SHA256 export")
	}
	restored, _, err := RestoreBundle(context.Background(), filepath.Join(t.TempDir(), "restored.git"), bundle, "fixture-project", mainRef, head)
	if err != nil || pinFixture(t, restored).Commit() != head {
		t.Fatal("SHA256 identity changed")
	}
	dir = newFixture(t)
	files := fixtureFiles()
	files[".harness/project.yaml"] = strings.Replace(files[".harness/project.yaml"], "next_issue: 3", "next_issue: 9", 1)
	parent := fixtureCommit(t, dir, files, "")
	files[".harness/project.yaml"] = strings.Replace(files[".harness/project.yaml"], "next_issue: 9", "next_issue: 5", 1)
	head = fixtureCommit(t, dir, files, parent)
	s = openFixture(t, dir, Limits{})
	_, err = s.validateBackup(context.Background(), head, "fixture-project")
	requireCode(t, err, "invalid_schema")
}

func TestBackupValidationCompleteHistoryAndReceiptContinuity(t *testing.T) {
	for _, name := range []string{"receipt-pruned", "receipt-changed", "wrong-origin", "entity-pruned", "foreign-history", "wrong-path", "same-blob-wrong-id"} {
		t.Run(name, func(t *testing.T) {
			s := newWritable(t)
			req := createRequest(pinFixture(t, s).Commit(), "key")
			mutateOK(t, s, req)
			snap := pinFixture(t, s)
			files := map[string]string{}
			for path, e := range snap.entries {
				files[path] = fixtureGit(t, s.dir, "", "cat-file", "blob", e.OID)
			}
			receiptPath := ".harness/receipts/" + digest([]byte("key")) + ".json"
			switch name {
			case "receipt-pruned":
				delete(files, receiptPath)
			case "receipt-changed":
				files[receiptPath] = strings.Replace(files[receiptPath], "create_issue", "update_issue", 1)
			case "wrong-origin":
				r, _, _ := snap.Receipt(context.Background(), digest([]byte("key")))
				r.KeyHash = digest([]byte("new"))
				r.ResultBlob = strings.Repeat("a", 40)
				raw, _ := json.Marshal(r)
				files[".harness/receipts/"+r.KeyHash+".json"] = string(raw)
			case "entity-pruned":
				delete(files, "issues/ISSUE-1/issue.md")
			case "foreign-history":
				files[".harness/project.yaml"] = strings.Replace(files[".harness/project.yaml"], "fixture-project", "other", 1)
			case "wrong-path":
				files[".harness/unknown"] = "unsupported"
			case "same-blob-wrong-id":
				files["issues/ISSUE-2/issue.md"] = files["issues/ISSUE-1/issue.md"]
			}
			head := fixtureCommit(t, s.dir, files, snap.commit)
			if _, err := s.validateBackup(context.Background(), head, "fixture-project"); err == nil {
				t.Fatal("invalid continuity accepted")
			}
		})
	}
	s := newWritable(t)
	mutateOK(t, s, createRequest(pinFixture(t, s).Commit(), "key"))
	s.limits.History = 1
	_, err := s.validateBackup(context.Background(), pinFixture(t, s).Commit(), "fixture-project")
	requireCode(t, err, "history_unavailable")
	s.limits.History = 256
	s.limits.ReadBytes = 1
	_, err = s.validateBackup(context.Background(), pinFixture(t, s).Commit(), "fixture-project")
	requireCode(t, err, "history_unavailable")
}
