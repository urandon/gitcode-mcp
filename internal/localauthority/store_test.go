package localauthority

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"gitcode-mcp/internal/authority"
)

const testProject authority.UUID = "11111111-1111-4111-8111-111111111111"

func testManifest() authority.Manifest {
	return authority.Manifest{Schema: 1, ID: testProject, Slug: "research-example", Authority: authority.Local,
		Entities: []authority.EntityKind{authority.Issue, authority.Comment, authority.Label, authority.Milestone, authority.Document, authority.Link},
		Network:  authority.NetworkPolicy{DataEgress: "deny", ControlTransport: "ipc_only"},
		Search:   authority.SearchIntent{Default: "hybrid", Fallback: "full_text"}}
}

func testStore(t *testing.T) (*Store, *Registry, string) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("writer persistence is not qualified on this platform")
	}
	r, err := newRegistry(privateTestDirectory(t))
	if err != nil {
		t.Fatal("machine registry qualification failed")
	}
	dir := privateTestDirectory(t)
	s, err := Initialize(context.Background(), r, dir, testManifest())
	if err != nil {
		t.Fatalf("initialization failed: %T", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, r, dir
}

func privateTestDirectory(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(t.TempDir())
	if err != nil || os.Chmod(path, 0700) != nil {
		t.Fatal("private fixture directory unavailable")
	}
	return path
}

func TestExplicitAuthorityInitializationAndReopen(t *testing.T) {
	s, r, dir := testStore(t)
	if err := verifyWriter(context.Background(), s.db); err != nil {
		t.Fatal("WAL/FULL writer not qualified")
	}
	if _, err := OpenWriter(context.Background(), r, dir, testProject); err == nil {
		t.Fatal("second writer admitted")
	}
	if err := s.Close(); err != nil {
		t.Fatal("writer close failed")
	}
	next, err := OpenWriter(context.Background(), r, dir, testProject)
	if err != nil {
		t.Fatalf("reopen failed: %T", err)
	}
	defer next.Close()
	if next.instance != s.instance || next.manifest.ID != s.manifest.ID {
		t.Fatal("identity changed on restart")
	}
	ro, err := OpenReadOnly(context.Background(), dir, testProject)
	if err != nil {
		t.Fatal("authority inspection failed")
	}
	defer ro.Close()
	if ro.lease != nil {
		t.Fatal("read-only inspection acquired writer authority")
	}
}

func TestDuplicatePhysicalAuthorityRefusedAfterWriterRelease(t *testing.T) {
	s, r, dir := testStore(t)
	if err := s.Close(); err != nil {
		t.Fatal("writer close failed")
	}
	clone := privateTestDirectory(t)
	body, err := os.ReadFile(filepath.Join(dir, "tracker.db"))
	if err != nil {
		t.Fatal("fixture read failed")
	}
	if err = os.WriteFile(filepath.Join(clone, "tracker.db"), body, 0600); err != nil {
		t.Fatal("fixture clone failed")
	}
	_, err = OpenWriter(context.Background(), r, clone, testProject)
	var typed authority.Error
	if !errors.As(err, &typed) || typed.Code != authority.Conflict {
		t.Fatal("same UUID clone gained writer authority")
	}
	owner, err := OpenWriter(context.Background(), r, dir, testProject)
	if err != nil {
		t.Fatal("clone conflict poisoned original authority")
	}
	defer owner.Close()
}

func TestForeignDatabaseCannotBecomeAuthority(t *testing.T) {
	dir := privateTestDirectory(t)
	if err := os.WriteFile(filepath.Join(dir, "tracker.db"), []byte("synthetic foreign database"), 0600); err != nil {
		t.Fatal("fixture create failed")
	}
	_, err := OpenReadOnly(context.Background(), dir, testProject)
	var typed Error
	if !errors.As(err, &typed) || typed.Code != Corrupt {
		t.Fatal("foreign database accepted as authority")
	}
	body, err := os.ReadFile(filepath.Join(dir, "tracker.db"))
	if err != nil || string(body) != "synthetic foreign database" {
		t.Fatal("foreign bytes changed")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("foreign rejection created sidecars")
	}
}

func TestLiveWriterPhysicalReplacementCannotAcknowledgeMutation(t *testing.T) {
	s, _, dir := testStore(t)
	createIssue(t, s, "synthetic-before-replacement")
	path := filepath.Join(dir, "tracker.db")
	body, err := os.ReadFile(path)
	if err != nil || os.Rename(path, path+".retained") != nil || os.WriteFile(path, body, 0600) != nil {
		t.Fatal("live replacement fixture unavailable")
	}
	_, err = s.Mutate(context.Background(), request(t, "issue.create", "synthetic-after-replacement", authority.IssuePayload{Title: "Synthetic", Body: "", State: "open"}), testActor)
	var typed Error
	if !errors.As(err, &typed) || typed.Code != Corrupt {
		t.Fatal("live writer acknowledged a replaced physical authority")
	}
	if !s.fenced {
		t.Fatal("physical identity mismatch did not fence writer")
	}
}
