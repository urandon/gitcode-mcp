package repositorydocs

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/rag"
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

func TestSearchRejectsOverlayThatChangesDuringRetrieval(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "docs/guide.md", "committed text\n")
	base := commitTestRepository(t, root, "base")
	writeTestFile(t, root, "docs/guide.md", "dirty sentinel version one\n")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	store := repositoryDocsSearchStore(t, ctx)
	defer store.Close()
	provider := repositoryDocsSearchProvider(t)
	if _, err := NewIndexer(store, provider).Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: base, IncludeWorktree: true}); err != nil {
		t.Fatal(err)
	}
	mutating := &mutatingEmbeddingProvider{EmbeddingProvider: provider, mutate: func() {
		writeTestFile(t, root, "docs/guide.md", "replacement while search is running\n")
	}}
	_, err = NewRetriever(store, mutating).Search(ctx, SearchRequest{RepoID: "owner/repo", Repository: repo, Revision: base, IncludeWorktree: true, Query: "dirty sentinel"})
	if !errors.Is(err, ErrWorktreeOverlayStale) {
		t.Fatalf("err = %v, want worktree overlay stale", err)
	}
}

func TestTrackedOverlayUsesRequestedRevisionAsItsBase(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "docs/guide.md", "historical version\n")
	historical := commitTestRepository(t, root, "historical")
	writeTestFile(t, root, "docs/guide.md", "clean current worktree sentinel\n")
	_ = commitTestRepository(t, root, "current")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	store := repositoryDocsSearchStore(t, ctx)
	defer store.Close()
	provider := repositoryDocsSearchProvider(t)
	indexed, err := NewIndexer(store, provider).Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: historical, IncludeWorktree: true})
	if err != nil || indexed.State != cache.RepoDocSetReady {
		t.Fatalf("indexed=%#v err=%v", indexed, err)
	}
	result, err := NewRetriever(store, provider).Search(ctx, SearchRequest{RepoID: "owner/repo", Repository: repo, Revision: historical, IncludeWorktree: true, Query: "current worktree sentinel"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) == 0 || result.Hits[0].Citation.Authority != "worktree" || result.OverlayDigest == "" {
		t.Fatalf("result=%#v", result)
	}
}

func TestTrackedOverlayIgnoresOversizedChangesOutsideDocumentationPolicy(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "docs/guide.md", "committed documentation\n")
	writeTestFile(t, root, "src/generated.bin", "small tracked source\n")
	base := commitTestRepository(t, root, "base")
	writeTestFile(t, root, "docs/guide.md", "dirty documentation sentinel\n")
	writeTestFile(t, root, "src/generated.bin", strings.Repeat("x", 4096))
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	store := repositoryDocsSearchStore(t, ctx)
	defer store.Close()
	indexed, err := NewIndexer(store, repositoryDocsSearchProvider(t)).Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: base, IncludeWorktree: true, MaxFileBytes: 128})
	if err != nil || indexed.State != cache.RepoDocSetReady || indexed.EligibleFiles != 1 {
		t.Fatalf("indexed=%#v err=%v", indexed, err)
	}
}

func TestIndexerSupersedesOverlayChangedDuringEmbedding(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "docs/guide.md", "committed documentation\n")
	base := commitTestRepository(t, root, "base")
	writeTestFile(t, root, "docs/guide.md", "dirty documentation generation one\n")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	store := repositoryDocsSearchStore(t, ctx)
	defer store.Close()
	provider := &mutatingEmbeddingProvider{EmbeddingProvider: repositoryDocsSearchProvider(t), mutate: func() {
		writeTestFile(t, root, "docs/guide.md", "dirty documentation generation two\n")
	}}
	indexed, err := NewIndexer(store, provider).Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: base, IncludeWorktree: true})
	if err != nil || indexed.State != cache.RepoDocSetSuperseded {
		t.Fatalf("indexed=%#v err=%v", indexed, err)
	}
}

func TestIndexerDoesNotPublishReadyWhenSnapshottedOverlayBecomesUnreadable(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "docs/guide.md", "committed documentation\n")
	base := commitTestRepository(t, root, "base")
	writeTestFile(t, root, "docs/guide.md", "dirty documentation\n")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	store := repositoryDocsSearchStore(t, ctx)
	defer store.Close()
	indexer := NewIndexer(store, repositoryDocsSearchProvider(t))
	indexer.beforeOverlayRead = func(repoPath string) {
		if repoPath == "docs/guide.md" {
			if err := os.Remove(root + "/docs/guide.md"); err != nil {
				t.Fatal(err)
			}
		}
	}
	indexed, err := indexer.Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: base, IncludeWorktree: true})
	if err != nil || indexed.State != cache.RepoDocSetSuperseded {
		t.Fatalf("indexed=%#v err=%v", indexed, err)
	}
}

func TestIndexerRejectsChangedExpectedSnapshotBeforePublishing(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "docs/guide.md", "committed documentation\n")
	base := commitTestRepository(t, root, "base")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	store := repositoryDocsSearchStore(t, ctx)
	defer store.Close()
	_, err = NewIndexer(store, repositoryDocsSearchProvider(t)).Run(ctx, IndexRequest{
		RepoID: "owner/repo", Repository: repo, Revision: base, EnforceExpectedSnapshot: true,
		ExpectedCommitOID: base, ExpectedPolicyHash: "stale-policy", ExpectedConfigDigest: "stale-config", ExpectedNamespaceID: "stale-namespace",
	})
	if !errors.Is(err, ErrIndexSnapshotStale) {
		t.Fatalf("err=%v, want repository documentation snapshot stale", err)
	}
}

type mutatingEmbeddingProvider struct {
	rag.EmbeddingProvider
	once   sync.Once
	mutate func()
}

func (p *mutatingEmbeddingProvider) Embed(ctx context.Context, req rag.EmbedRequest) (rag.EmbedResponse, error) {
	p.once.Do(p.mutate)
	return p.EmbeddingProvider.Embed(ctx, req)
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
