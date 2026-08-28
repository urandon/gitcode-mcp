package repositorydocs

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryCommittedPolicyAndBlobAccess(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "README.md", "first revision\n")
	writeTestFile(t, root, PolicyConfigPath, "cache_mode: repo-local\nrepository_docs:\n  preset: conventional-docs-v1\n")
	commit1 := commitTestRepository(t, root, "first")
	writeTestFile(t, root, "README.md", "second revision\n")
	writeTestFile(t, root, PolicyConfigPath, "repository_docs:\n  preset: none\n  include:\n    - architecture/**\n")
	writeTestFile(t, root, "architecture/design.md", "versioned design\n")
	_ = commitTestRepository(t, root, "second")

	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if repo.GitStoreRef == "" || repo.WorktreeRef == "" || strings.Contains(repo.GitStoreRef, root) || strings.Contains(repo.WorktreeRef, root) {
		t.Fatalf("unsafe repository refs: %#v", repo)
	}
	resolved, policy, err := ResolvePolicy(ctx, repo, commit1, false)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != commit1 || policy.Source != PolicySourceCommitted || !policy.Policy.Matches("README.md") || policy.Policy.Matches("architecture/design.md") {
		t.Fatalf("resolved=%q policy=%#v", resolved, policy)
	}
	entries, err := repo.ListTree(ctx, commit1)
	if err != nil {
		t.Fatal(err)
	}
	var readme TreeEntry
	for _, entry := range entries {
		if entry.Path == "README.md" {
			readme = entry
		}
	}
	if readme.OID == "" || readme.Type != "blob" {
		t.Fatalf("README entry = %#v", readme)
	}
	body, err := repo.ReadBlob(ctx, readme.OID, 1024)
	if err != nil || string(body) != "first revision\n" {
		t.Fatalf("body=%q err=%v", body, err)
	}
}

func TestRepositoryTrackedWorktreeOverlay(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "docs/guide.md", "committed\n")
	writeTestFile(t, root, PolicyConfigPath, "repository_docs:\n  preset: conventional-docs-v1\n")
	_ = commitTestRepository(t, root, "base")
	writeTestFile(t, root, "docs/guide.md", "dirty tracked\n")
	writeTestFile(t, root, PolicyConfigPath, "repository_docs:\n  enabled: false\n")
	writeTestFile(t, root, "docs/untracked.md", "must stay excluded\n")

	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := repo.TrackedChanges(ctx, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("changes=%#v", changes)
	}
	for _, change := range changes {
		if change.Path == "docs/untracked.md" || change.Digest == "" {
			t.Fatalf("unexpected change=%#v", change)
		}
	}
	_, policy, err := ResolvePolicy(ctx, repo, "HEAD", true)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Source != PolicySourceWorktree || policy.Status != PolicyStatusDisabled {
		t.Fatalf("policy=%#v", policy)
	}
	if _, err := repo.ReadTrackedWorktreeFile(ctx, "docs/untracked.md", 1024); !errors.Is(err, ErrWorktreeUnavailable) {
		t.Fatalf("untracked read err=%v", err)
	}
}

func TestRepositoryBoundsAndMissingObjects(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "README.md", strings.Repeat("x", 32))
	commit := commitTestRepository(t, root, "base")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := repo.ListTree(ctx, commit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReadBlob(ctx, entries[0].OID, 8); !errors.Is(err, ErrGitObjectUnavailable) {
		t.Fatalf("bounded read err=%v", err)
	}
	if _, err := repo.ResolveRevision(ctx, "does-not-exist"); !errors.Is(err, ErrGitObjectUnavailable) {
		t.Fatalf("missing revision err=%v", err)
	}
	if _, err := repo.ResolveRevision(ctx, "--help"); !errors.Is(err, ErrGitObjectUnavailable) {
		t.Fatalf("option-like revision err=%v", err)
	}
	missingOID := strings.Repeat("0", len(entries[0].OID))
	if _, err := repo.ReadBlob(ctx, missingOID, 1024); !errors.Is(err, ErrGitObjectUnavailable) {
		t.Fatalf("missing blob read err=%v", err)
	}
}

func initTestRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.name", "Fixture User")
	runGit(t, root, "config", "user.email", "fixture@example.invalid")
	return root
}

func writeTestFile(t *testing.T, root, name, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitTestRepository(t *testing.T, root, message string) string {
	t.Helper()
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", message)
	return strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
}

func runGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"-C", root}, args...)
	out, err := exec.Command("git", cmdArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}
