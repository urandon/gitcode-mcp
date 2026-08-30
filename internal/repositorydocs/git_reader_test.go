package repositorydocs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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
		if change.IndexState != ' ' || change.TreeState != 'M' {
			t.Fatalf("unstaged XY state=%q%q change=%#v", change.IndexState, change.TreeState, change)
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

func TestRepositoryTrackedWorktreeOverlayPreservesIndexAndWorktreeStates(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "docs/staged.md", "base\n")
	writeTestFile(t, root, "docs/unstaged.md", "base\n")
	writeTestFile(t, root, "docs/mixed.md", "base\n")
	base := commitTestRepository(t, root, "base")
	writeTestFile(t, root, "docs/staged.md", "staged\n")
	writeTestFile(t, root, "docs/mixed.md", "staged\n")
	runGit(t, root, "add", "docs/staged.md", "docs/mixed.md")
	writeTestFile(t, root, "docs/unstaged.md", "unstaged\n")
	writeTestFile(t, root, "docs/mixed.md", "staged then unstaged\n")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := repo.TrackedChangesAt(ctx, base, 1024)
	if err != nil {
		t.Fatal(err)
	}
	states := make(map[string]string, len(changes))
	for _, change := range changes {
		states[change.Path] = string([]byte{change.IndexState, change.TreeState})
	}
	want := map[string]string{"docs/staged.md": "M ", "docs/unstaged.md": " M", "docs/mixed.md": "MM"}
	for path, xy := range want {
		if states[path] != xy {
			t.Fatalf("%s XY=%q want=%q all=%v", path, states[path], xy, states)
		}
	}
}

func TestWorktreeErrorsDoNotExposeAbsolutePaths(t *testing.T) {
	repo := &Repository{root: t.TempDir(), ObjectFormat: "sha1"}
	secretPath := filepath.Join(t.TempDir(), "private", "docs.md")
	_, err := repo.ReadTrackedWorktreeFile(context.Background(), secretPath, 1024)
	if !errors.Is(err, ErrWorktreeUnavailable) {
		t.Fatalf("absolute-path error=%v", err)
	}
	if strings.Contains(err.Error(), secretPath) || strings.Contains(err.Error(), "private") {
		t.Fatalf("absolute path leaked: %v", err)
	}
}

func TestGitObjectDiagnosticJSONDoesNotExposeRequestedObject(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "private-revision")
	err := &GitObjectError{
		Object:       secret,
		Reason:       "revision cannot be resolved from the local object database",
		Availability: GitObjectShallow,
		Recovery:     GitObjectRecovery{Action: "deepen_git_history", Message: "deepen or fetch the selected local Git authority, then retry"},
	}
	data, marshalErr := json.Marshal(err)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if strings.Contains(string(data), secret) || strings.Contains(err.Error(), secret) || strings.Contains(err.Remediation(), secret) {
		t.Fatalf("public diagnostic leaked requested object: json=%s error=%v", data, err)
	}
}

func TestReadTrackedWorktreeFileRejectsSymlinkedParentAndFinalComponent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture requires developer-mode privileges on Windows")
	}
	ctx := context.Background()
	for _, replace := range []string{"parent", "final"} {
		t.Run(replace, func(t *testing.T) {
			root := initTestRepository(t)
			writeTestFile(t, root, "docs/guide.md", "committed\n")
			_ = commitTestRepository(t, root, "base")
			outside := t.TempDir()
			writeTestFile(t, outside, "guide.md", "outside-secret\n")
			if replace == "parent" {
				if err := os.RemoveAll(filepath.Join(root, "docs")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(root, "docs")); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Remove(filepath.Join(root, "docs", "guide.md")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(outside, "guide.md"), filepath.Join(root, "docs", "guide.md")); err != nil {
					t.Fatal(err)
				}
			}
			repo, err := OpenRepository(ctx, root)
			if err != nil {
				t.Fatal(err)
			}
			body, err := repo.ReadTrackedWorktreeFile(ctx, "docs/guide.md", 1024)
			if !errors.Is(err, ErrWorktreeUnavailable) || len(body) != 0 {
				t.Fatalf("body=%q err=%v", body, err)
			}
			if strings.Contains(err.Error(), outside) || strings.Contains(err.Error(), "outside-secret") {
				t.Fatalf("outside authority leaked: %v", err)
			}
		})
	}
}

func TestSafeWorktreeOpenKeepsOpenedFileAcrossFinalPathSwap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink fixture requires developer-mode privileges on Windows")
	}
	root := t.TempDir()
	writeTestFile(t, root, "docs/guide.md", "opened-authority\n")
	out := t.TempDir()
	writeTestFile(t, out, "secret.md", "outside-secret\n")
	var hookErr error
	body, _, err := readWorktreeFileNoFollowAfterOpen(context.Background(), root, "docs/guide.md", 1024, func() {
		original := filepath.Join(root, "docs", "guide.md")
		hookErr = os.Rename(original, original+".opened")
		if hookErr == nil {
			hookErr = os.Symlink(filepath.Join(out, "secret.md"), original)
		}
	})
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if err != nil || string(body) != "opened-authority\n" {
		t.Fatalf("body=%q err=%v", body, err)
	}
}

func TestRepositoryBoundsAndMissingObjects(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	// This must exceed common kernel pipe capacities so an early bounded read
	// makes git cat-file observe a closed reader while it is still producing.
	writeTestFile(t, root, "README.md", strings.Repeat("x", 4<<20))
	commit := commitTestRepository(t, root, "base")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := repo.ListTree(ctx, commit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ReadBlob(ctx, entries[0].OID, 8); !errors.Is(err, ErrDocumentTooLarge) || errors.Is(err, ErrGitObjectUnavailable) {
		t.Fatalf("bounded read err=%v", err)
	} else if coded, ok := err.(interface{ DiagnosticCode() string }); !ok || coded.DiagnosticCode() != "repository_document_too_large" {
		t.Fatalf("bounded read diagnostic=%T %v", err, err)
	}
	if _, err := repo.ResolveRevision(ctx, "does-not-exist"); !errors.Is(err, ErrGitObjectUnavailable) {
		t.Fatalf("missing revision err=%v", err)
	} else {
		assertGitObjectDiagnostic(t, err, GitObjectUnavailable, "git_object_unavailable", "fetch_git_object", root)
	}
	if _, err := repo.ResolveRevision(ctx, "--help"); !errors.Is(err, ErrGitObjectUnavailable) {
		t.Fatalf("option-like revision err=%v", err)
	}
	missingOID := strings.Repeat("0", len(entries[0].OID))
	if _, err := repo.ReadBlob(ctx, missingOID, 1024); !errors.Is(err, ErrGitObjectUnavailable) {
		t.Fatalf("missing blob read err=%v", err)
	} else {
		assertGitObjectDiagnostic(t, err, GitObjectUnavailable, "git_object_unavailable", "fetch_git_object", root)
	}
}

func TestWalkTreeStreamsDeterministicNULRecords(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "z-last.md", "z\n")
	writeTestFile(t, root, "docs/a-first.md", "a\n")
	writeTestFile(t, root, "docs/b-second.md", "b\n")
	commit := commitTestRepository(t, root, "tree")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}

	var paths []string
	if err := repo.WalkTree(ctx, commit, func(entry TreeEntry) error {
		paths = append(paths, entry.Path)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"docs/a-first.md", "docs/b-second.md", "z-last.md"}
	if strings.Join(paths, "|") != strings.Join(want, "|") {
		t.Fatalf("paths=%q want=%q", paths, want)
	}
}

func TestWalkTreeRecordsRejectsMalformedAndTruncatedInput(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{name: "missing-tab", data: "100644 blob abc 1\x00"},
		{name: "bad-size", data: "100644 blob abc nope\tREADME.md\x00"},
		{name: "truncated", data: "100644 blob abc 1\tREADME.md"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := walkTreeRecords(context.Background(), strings.NewReader(tt.data), func(TreeEntry) error { return nil })
			if !errors.Is(err, ErrMalformedGitTree) {
				t.Fatalf("err=%v", err)
			}
		})
	}
	oversized := "100644 blob " + strings.Repeat("a", 40) + " 1\t" + strings.Repeat("p", maxGitTreeRecordBytes) + "\x00"
	if err := walkTreeRecords(context.Background(), strings.NewReader(oversized), func(TreeEntry) error { return nil }); !errors.Is(err, ErrMalformedGitTree) {
		t.Fatalf("oversized record err=%v", err)
	}
}

func TestWalkTreeHonorsCancellationAndReapsCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	installNonTerminatingTreeProducer(t)
	ctx, cancel := context.WithCancel(context.Background())
	repo := &Repository{root: t.TempDir(), ObjectFormat: "sha1"}

	visited := 0
	started := time.Now()
	err := repo.WalkTree(ctx, strings.Repeat("a", 40), func(TreeEntry) error {
		visited++
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) || visited != 1 {
		t.Fatalf("visited=%d err=%v", visited, err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("canceled walk took %v", elapsed)
	}
}

func TestWalkTreeVisitorErrorStopsAndReapsLargeProducer(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	installNonTerminatingTreeProducer(t)
	repo := &Repository{root: t.TempDir(), ObjectFormat: "sha1"}
	wantErr := errors.New("stop before producer EOF")
	visited := 0
	started := time.Now()
	err := repo.WalkTree(context.Background(), strings.Repeat("a", 40), func(TreeEntry) error {
		visited++
		return wantErr
	})
	if !errors.Is(err, wantErr) || visited != 1 {
		t.Fatalf("visited=%d err=%v", visited, err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("visitor-error walk took %v", elapsed)
	}
}

func TestReadFileAtCommitUsesLiteralExactPath(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "docs/[draft].md", "literal\n")
	writeTestFile(t, root, "docs/d.md", "neighbor\n")
	commit := commitTestRepository(t, root, "literal")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}

	body, found, err := repo.ReadFileAtCommit(ctx, commit, "docs/[draft].md", 1024)
	if err != nil || !found || string(body) != "literal\n" {
		t.Fatalf("body=%q found=%v err=%v", body, found, err)
	}
	if _, found, err := repo.ReadFileAtCommit(ctx, commit, "docs/*.md", 1024); err != nil || found {
		t.Fatalf("glob lookup found=%v err=%v", found, err)
	}
}

func TestGitCommandsDisableLazyFetchAndDoNotExposeStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	binDir := t.TempDir()
	gitPath := filepath.Join(binDir, "git")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"probe-env\" ]; then printf '%s' \"$GIT_NO_LAZY_FETCH\"; exit 0; fi\n" +
		"printf '%s\\n' 'private-fixture-stderr' >&2\n" +
		"exit 1\n"
	if err := os.WriteFile(gitPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cmd := gitCommandContext(context.Background(), "probe-env")
	out, err := cmd.Output()
	if err != nil || string(out) != "1" {
		t.Fatalf("GIT_NO_LAZY_FETCH=%q err=%v", out, err)
	}
	repo := &Repository{root: t.TempDir(), ObjectFormat: "sha1"}
	_, err = repo.ReadBlob(context.Background(), strings.Repeat("0", 40), 1024)
	if !errors.Is(err, ErrGitObjectUnavailable) {
		t.Fatalf("err=%v", err)
	}
	if strings.Contains(err.Error(), "private-fixture-stderr") {
		t.Fatalf("stderr leaked: %v", err)
	}
}

func TestClassifyDocumentContent(t *testing.T) {
	validOID := strings.Repeat("a", 64)
	tests := []struct {
		name  string
		entry TreeEntry
		body  []byte
		want  DocumentContentClass
	}{
		{name: "symlink", entry: TreeEntry{Mode: "120000", Type: "blob"}, want: DocumentContentSymlink},
		{name: "submodule", entry: TreeEntry{Mode: "160000", Type: "commit"}, want: DocumentContentSubmodule},
		{name: "canonical LFS pointer", entry: TreeEntry{Mode: "100644", Type: "blob"}, body: []byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + validOID + "\nsize 1\n"), want: DocumentContentLFSPointer},
		{name: "valid LFS extension", entry: TreeEntry{Mode: "100644", Type: "blob"}, body: []byte("version https://git-lfs.github.com/spec/v1\next-0-lock sha256:" + validOID + "\noid sha256:" + validOID + "\nsize 1\n"), want: DocumentContentLFSPointer},
		{name: "short LFS oid is prose", entry: TreeEntry{Mode: "100644", Type: "blob"}, body: []byte("version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 1\n"), want: DocumentContentRegular},
		{name: "negative LFS size is prose", entry: TreeEntry{Mode: "100644", Type: "blob"}, body: []byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + validOID + "\nsize -1\n"), want: DocumentContentRegular},
		{name: "LFS-looking document body is prose", entry: TreeEntry{Mode: "100644", Type: "blob"}, body: []byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + validOID + "\nsize 1\n# explanation\n"), want: DocumentContentRegular},
		{name: "invented three-field LFS extension is prose", entry: TreeEntry{Mode: "100644", Type: "blob"}, body: []byte("version https://git-lfs.github.com/spec/v1\next-lock 1 sha256:" + validOID + "\noid sha256:" + validOID + "\nsize 1\n"), want: DocumentContentRegular},
		{name: "invalid LFS extension order is prose", entry: TreeEntry{Mode: "100644", Type: "blob"}, body: []byte("version https://git-lfs.github.com/spec/v1\next-next-lock sha256:" + validOID + "\noid sha256:" + validOID + "\nsize 1\n"), want: DocumentContentRegular},
		{name: "non-sha256 LFS extension is prose", entry: TreeEntry{Mode: "100644", Type: "blob"}, body: []byte("version https://git-lfs.github.com/spec/v1\next-0-lock md5:" + strings.Repeat("a", 32) + "\noid sha256:" + validOID + "\nsize 1\n"), want: DocumentContentRegular},
		{name: "duplicate LFS extension priority is prose", entry: TreeEntry{Mode: "100644", Type: "blob"}, body: []byte("version https://git-lfs.github.com/spec/v1\next-0-lock sha256:" + validOID + "\next-0-encrypt sha256:" + validOID + "\noid sha256:" + validOID + "\nsize 1\n"), want: DocumentContentRegular},
		{name: "oversized LFS-looking prose", entry: TreeEntry{Mode: "100644", Type: "blob"}, body: []byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + validOID + "\nsize 1\n" + strings.Repeat(" ", maxLFSPointerBytes)), want: DocumentContentRegular},
		{name: "NUL", entry: TreeEntry{Mode: "100644", Type: "blob"}, body: []byte{'a', 0, 'b'}, want: DocumentContentNUL},
		{name: "invalid UTF-8", entry: TreeEntry{Mode: "100644", Type: "blob"}, body: []byte{0xff}, want: DocumentContentInvalidUTF8},
		{name: "regular", entry: TreeEntry{Mode: "100644", Type: "blob"}, body: []byte("hello"), want: DocumentContentRegular},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyDocumentContent(tt.entry, tt.body); got != tt.want {
				t.Fatalf("ClassifyDocumentContent(%#v, %q)=%q want=%q", tt.entry, tt.body, got, tt.want)
			}
		})
	}
}

func TestWalkTreeRecordsPreservesNewlinesInPath(t *testing.T) {
	record := append([]byte("100644 blob "+strings.Repeat("a", 40)+" 4\tdocs/line\nbreak.md"), 0)
	var got TreeEntry
	if err := walkTreeRecords(context.Background(), bytes.NewReader(record), func(entry TreeEntry) error {
		got = entry
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got.Path != "docs/line\nbreak.md" {
		t.Fatalf("path=%q", got.Path)
	}
}

func TestOpenRepositorySupportsBareLinkedAndSymlinkedTopologies(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink topology has platform-specific privileges")
	}
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "docs/guide.md", "topology-safe documentation\n")
	commit := commitTestRepository(t, root, "base")
	mainRepo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(t.TempDir(), "linked")
	runGit(t, root, "worktree", "add", "--detach", linked, commit)
	linkedRepo, err := OpenRepository(ctx, linked)
	if err != nil {
		t.Fatal(err)
	}
	if linkedRepo.GitStoreRef != mainRepo.GitStoreRef || linkedRepo.WorktreeRef == mainRepo.WorktreeRef {
		t.Fatalf("linked topology main=%+v linked=%+v", mainRepo, linkedRepo)
	}
	symlink := filepath.Join(t.TempDir(), "linked-alias")
	if err := os.Symlink(linked, symlink); err != nil {
		t.Fatal(err)
	}
	symlinkRepo, err := OpenRepository(ctx, symlink)
	if err != nil {
		t.Fatal(err)
	}
	if symlinkRepo.GitStoreRef != linkedRepo.GitStoreRef || symlinkRepo.WorktreeRef != linkedRepo.WorktreeRef {
		t.Fatalf("symlink forked identity linked=%+v symlink=%+v", linkedRepo, symlinkRepo)
	}
	bare := filepath.Join(t.TempDir(), "repo.git")
	if output, err := exec.Command("git", "clone", "--bare", "--no-local", root, bare).CombinedOutput(); err != nil {
		t.Fatalf("clone bare: %v: %s", err, output)
	}
	bareRepo, err := OpenRepository(ctx, bare)
	if err != nil {
		t.Fatal(err)
	}
	if bareRepo.WorktreeRef != "" {
		t.Fatalf("bare repository exposed worktree identity: %+v", bareRepo)
	}
	resolved, err := bareRepo.ResolveRevision(ctx, "HEAD")
	if err != nil || resolved != commit {
		t.Fatalf("bare revision=%q err=%v want=%q", resolved, err, commit)
	}
	entries, err := bareRepo.ListTree(ctx, commit)
	if err != nil || len(entries) != 1 || entries[0].Path != "docs/guide.md" {
		t.Fatalf("bare entries=%+v err=%v", entries, err)
	}
}

func TestMissingRevisionInShallowCloneHasDeepenHandoff(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file URL shallow-clone fixture")
	}
	ctx := context.Background()
	origin := initTestRepository(t)
	writeTestFile(t, origin, "docs/guide.md", "old history\n")
	oldCommit := commitTestRepository(t, origin, "old")
	writeTestFile(t, origin, "docs/guide.md", "shallow tip\n")
	_ = commitTestRepository(t, origin, "tip")
	shallow := filepath.Join(t.TempDir(), "shallow")
	cloneGit(t, "clone", "-q", "--depth=1", "--no-local", "file://"+filepath.ToSlash(origin), shallow)
	repo, err := OpenRepository(ctx, shallow)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.ResolveRevision(ctx, oldCommit)
	assertGitObjectDiagnostic(t, err, GitObjectShallow, "git_object_shallow", "deepen_git_history", origin, shallow)
}

func TestMissingBlobInPartialCloneHasMaterializeHandoff(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file URL partial-clone fixture")
	}
	ctx := context.Background()
	origin := initTestRepository(t)
	writeTestFile(t, origin, "docs/guide.md", "partial clone blob\n")
	_ = commitTestRepository(t, origin, "base")
	blobOID := strings.TrimSpace(runGit(t, origin, "rev-parse", "HEAD:docs/guide.md"))
	runGit(t, origin, "config", "uploadpack.allowFilter", "true")
	partial := filepath.Join(t.TempDir(), "partial")
	cloneGit(t, "clone", "-q", "--filter=blob:none", "--no-checkout", "file://"+filepath.ToSlash(origin), partial)
	repo, err := OpenRepository(ctx, partial)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.ReadBlob(ctx, blobOID, 1024)
	assertGitObjectDiagnostic(t, err, GitObjectPartial, "git_object_partial_clone", "materialize_partial_clone_object", origin, partial)
}

func TestMissingPromisedBlobHasPromisorHandoff(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "docs/guide.md", "promised blob\n")
	_ = commitTestRepository(t, root, "base")
	blobOID := strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD:docs/guide.md"))
	objectPath := filepath.Join(root, ".git", "objects", blobOID[:2], blobOID[2:])
	if err := os.Remove(objectPath); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "config", "remote.fixture.promisor", "true")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.ReadBlob(ctx, blobOID, 1024)
	assertGitObjectDiagnostic(t, err, GitObjectPromisor, "git_object_promisor_unavailable", "materialize_promisor_object", root)
}

func TestOpenRepositoryReadsObjectsThroughAlternates(t *testing.T) {
	ctx := context.Background()
	origin := initTestRepository(t)
	writeTestFile(t, origin, "docs/guide.md", "alternate object store\n")
	commit := commitTestRepository(t, origin, "base")
	shared := filepath.Join(t.TempDir(), "shared")
	cloneGit(t, "clone", "-q", "--shared", "--no-checkout", origin, shared)
	repo, err := OpenRepository(ctx, shared)
	if err != nil {
		t.Fatal(err)
	}
	body, found, err := repo.ReadFileAtCommit(ctx, commit, "docs/guide.md", 1024)
	if err != nil || !found || string(body) != "alternate object store\n" {
		t.Fatalf("body=%q found=%v err=%v", body, found, err)
	}
	if strings.Contains(repo.GitStoreRef, origin) || strings.Contains(repo.GitStoreRef, shared) {
		t.Fatalf("alternate paths leaked through opaque identity: %+v", repo)
	}
}

func TestOpenRepositoryReadsSHA256ObjectFormatWhenSupported(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	cmd := exec.Command("git", "-C", root, "init", "-q", "--object-format=sha256")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("Git build does not support SHA-256 repositories: %v: %s", err, output)
	}
	runGit(t, root, "config", "user.name", "Fixture User")
	runGit(t, root, "config", "user.email", "fixture@example.invalid")
	writeTestFile(t, root, "docs/guide.md", "sha256 object format\n")
	commit := commitTestRepository(t, root, "base")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if repo.ObjectFormat != "sha256" || len(commit) != 64 {
		t.Fatalf("format=%q commit=%q", repo.ObjectFormat, commit)
	}
	body, found, err := repo.ReadFileAtCommit(ctx, commit, "docs/guide.md", 1024)
	if err != nil || !found || string(body) != "sha256 object format\n" {
		t.Fatalf("body=%q found=%v err=%v", body, found, err)
	}
}

func initTestRepository(t testing.TB) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-q")
	runGit(t, root, "config", "user.name", "Fixture User")
	runGit(t, root, "config", "user.email", "fixture@example.invalid")
	return root
}

func installNonTerminatingTreeProducer(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	gitPath := filepath.Join(binDir, "git")
	script := "#!/bin/sh\n" +
		"printf '100644 blob " + strings.Repeat("a", 40) + " 1\\tfirst.md\\0'\n" +
		"trap '' PIPE TERM\n" +
		"while :; do\n" +
		"  :\n" +
		"done\n"
	if err := os.WriteFile(gitPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func writeTestFile(t testing.TB, root, name, body string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commitTestRepository(t testing.TB, root, message string) string {
	t.Helper()
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", message)
	return strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD"))
}

func cloneGit(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func assertGitObjectDiagnostic(t *testing.T, err error, availability GitObjectAvailability, code, action string, forbidden ...string) {
	t.Helper()
	if !errors.Is(err, ErrGitObjectUnavailable) {
		t.Fatalf("error=%v, want %v", err, ErrGitObjectUnavailable)
	}
	var objectErr *GitObjectError
	if !errors.As(err, &objectErr) {
		t.Fatalf("error type=%T, want *GitObjectError", err)
	}
	if objectErr.Availability != availability || objectErr.DiagnosticCode() != code || objectErr.Recovery.Action != action || objectErr.Remediation() == "" {
		t.Fatalf("diagnostic=%+v code=%q", objectErr, objectErr.DiagnosticCode())
	}
	public := objectErr.Error() + " " + objectErr.Remediation()
	for _, value := range forbidden {
		if value != "" && strings.Contains(public, value) {
			t.Fatalf("diagnostic leaked forbidden local value %q: %s", value, public)
		}
	}
}

func runGit(t testing.TB, root string, args ...string) string {
	t.Helper()
	cmdArgs := append([]string{"-C", root}, args...)
	out, err := exec.Command("git", cmdArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return string(out)
}
