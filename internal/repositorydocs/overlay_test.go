package repositorydocs

import (
	"context"
	"strings"
	"testing"

	"gitcode-mcp/internal/cache"
)

func TestTrackedOverlayIsExplicitAndStaleResultsAreOmitted(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "docs/guide.md", "committed text\n")
	base := commitTestRepository(t, root, "base")
	writeTestFile(t, root, "docs/guide.md", "dirty sentinel version one\n")
	writeTestFile(t, root, "docs/untracked.md", "untracked sentinel must never index\n")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	store := repositoryDocsSearchStore(t, ctx)
	defer store.Close()
	provider := repositoryDocsSearchProvider(t)
	indexer := NewIndexer(store, provider)
	committed, err := indexer.Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: base})
	if err != nil {
		t.Fatal(err)
	}
	if committed.EligibleFiles != 1 {
		t.Fatalf("committed = %#v", committed)
	}
	overlay, err := indexer.Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: base, IncludeWorktree: true})
	if err != nil {
		t.Fatal(err)
	}
	if overlay.State != cache.RepoDocSetReady || overlay.RevisionSetID == committed.RevisionSetID || overlay.EligibleFiles != 1 {
		t.Fatalf("overlay = %#v", overlay)
	}
	result, err := NewRetriever(store, provider).Search(ctx, SearchRequest{RepoID: "owner/repo", Repository: repo, Revision: base, IncludeWorktree: true, Query: "dirty sentinel"})
	if err != nil || len(result.Hits) == 0 || result.Hits[0].Citation.Authority != "worktree" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if result.Authority != "worktree_overlay" || result.OverlayDigest == "" || result.RequestedMode != SearchModeHybrid || result.EffectiveMode != SearchModeHybrid {
		t.Fatalf("overlay result contract=%#v", result)
	}
	writeTestFile(t, root, "docs/guide.md", "replacement content after indexing\n")
	stale, err := NewRetriever(store, provider).Search(ctx, SearchRequest{RepoID: "owner/repo", Repository: repo, Revision: base, IncludeWorktree: true, Query: "dirty sentinel"})
	if err != nil {
		t.Fatal(err)
	}
	if len(stale.Hits) != 0 {
		t.Fatalf("stale hits = %#v", stale.Hits)
	}
	found := false
	for _, warning := range stale.Warnings {
		found = found || strings.Contains(warning, "worktree_overlay_stale")
	}
	if !found {
		t.Fatalf("stale warnings = %#v", stale.Warnings)
	}
}

func TestCommittedDirtyContentReusesOverlayVector(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "docs/guide.md", "base\n")
	base := commitTestRepository(t, root, "base")
	writeTestFile(t, root, "docs/guide.md", "same bytes before and after commit\n")
	repo, _ := OpenRepository(ctx, root)
	store := repositoryDocsSearchStore(t, ctx)
	defer store.Close()
	provider := repositoryDocsSearchProvider(t)
	indexer := NewIndexer(store, provider)
	overlay, err := indexer.Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: base, IncludeWorktree: true})
	if err != nil {
		t.Fatal(err)
	}
	committedOID := commitTestRepository(t, root, "commit dirty content")
	committed, err := indexer.Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: committedOID})
	if err != nil {
		t.Fatal(err)
	}
	if committed.State != cache.RepoDocSetReady || committed.EmbeddedChunks != 0 || committed.ReusedChunks != overlay.EligibleChunks {
		t.Fatalf("overlay=%#v committed=%#v", overlay, committed)
	}
}
