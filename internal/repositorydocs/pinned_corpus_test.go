package repositorydocs

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
)

func pinnedFixtureFile(path, text string) PinnedFile {
	data := []byte(text)
	h := sha1.Sum(append([]byte(fmt.Sprintf("blob %d\x00", len(data))), data...))
	return PinnedFile{Entry: TreeEntry{Path: path, Mode: "100644", Type: "blob", OID: hex.EncodeToString(h[:]), Size: int64(len(data))}, Data: data}
}

func TestPinnedProjectCorpusUsesExistingRetrieverWithoutFilesystem(t *testing.T) {
	ctx := context.Background()
	commit := strings.Repeat("a", 40)
	file := pinnedFixtureFile("wiki/DOC-1.md", "Pinned lexical corpus fixture")
	repo, err := NewPinnedProjectRepository("opaque-fixture", commit, []PinnedFile{file})
	if err != nil {
		t.Fatal("pinned corpus unavailable")
	}
	file.Data[0] = 'X'
	r, err := NewRetriever(nil, nil).Search(ctx, SearchRequest{RepoID: "local-project/lab", Repository: repo, Query: "Pinned", Mode: "fulltext"})
	if err != nil || len(r.Hits) != 1 || !strings.HasPrefix(r.Hits[0].Snippet, "Pinned") || r.Hits[0].Citation.CommitOID != commit {
		t.Fatal("pinned corpus mutated or required filesystem/cache/provider")
	}
	if _, _, err := ResolvePolicy(ctx, repo, "HEAD", true); err == nil {
		t.Fatal("pinned corpus enabled worktree discovery")
	}
	if _, err := repo.TrackedChangesAtFiltered(ctx, commit, 1024, nil); err == nil {
		t.Fatal("pinned corpus walked process working directory")
	}
	if _, err := repo.ReadTrackedWorktreeFile(ctx, "README.md", 1024); err == nil {
		t.Fatal("pinned corpus read process working directory")
	}
	if _, err := repo.ResolveRevision(ctx, strings.Repeat("b", 40)); err == nil {
		t.Fatal("pinned corpus accepted another revision")
	}
	policy := pinnedProjectPolicy().Policy
	for _, path := range []string{".harness/project.yaml", ".harness/receipts/key.json", "docs/guide.md"} {
		if policy.Matches(path) {
			t.Fatal("internal corpus policy admitted unrelated content")
		}
	}
	for _, bad := range []PinnedFile{pinnedFixtureFile(".harness/project.yaml", "counter"), pinnedFixtureFile("docs/guide.md", "source docs"), pinnedFixtureFile("issues/../wiki/DOC-1.md", "alias"), {Entry: file.Entry, Data: []byte("altered bytes")}} {
		if _, err := NewPinnedProjectRepository("opaque-fixture", commit, []PinnedFile{bad}); err == nil {
			t.Fatal("invalid pinned corpus accepted")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := repo.ResolveRevision(cancelled, "HEAD"); err == nil {
		t.Fatal("pinned corpus ignored cancellation")
	}
}
