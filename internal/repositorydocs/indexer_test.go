package repositorydocs

import (
	"context"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/rag"
)

func TestIndexerPublishesCommittedRevisionAndReusesRenamedBlob(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "docs/guide.md", "# Guide\n\nРусский, English, 中文.\n")
	firstCommit := commitTestRepository(t, root, "first")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := cache.NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	provider, err := rag.NewFakeProvider(rag.EmbeddingProviderProfile{ProfileID: "repo-docs", ProviderID: "fake", ProviderType: "fake", Model: "fake-embedding", Dimensions: 8, BatchSize: 2, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	indexer := NewIndexer(store, provider)
	first, err := indexer.Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: firstCommit, ChunkBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	if first.State != cache.RepoDocSetReady || first.EligibleFiles != 1 || first.EligibleChunks == 0 || first.EmbeddedChunks != first.EligibleChunks {
		t.Fatalf("first result = %#v", first)
	}

	runGit(t, root, "mv", "docs/guide.md", "docs/renamed.md")
	secondCommit := commitTestRepository(t, root, "rename")
	second, err := indexer.Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: secondCommit, ChunkBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	if second.State != cache.RepoDocSetReady || second.EmbeddedChunks != 0 || second.ReusedChunks != first.EligibleChunks {
		t.Fatalf("second result = %#v", second)
	}
	memberships, err := store.ListRepositoryDocMembership(ctx, "owner/repo", second.RevisionSetID)
	if err != nil {
		t.Fatal(err)
	}
	if len(memberships) == 0 || memberships[0].Path != "docs/renamed.md" {
		t.Fatalf("memberships = %#v", memberships)
	}
}

func TestIndexerLeavesBoundedRunPartial(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "README.md", "one paragraph\n\ntwo paragraph\n\nthree paragraph\n")
	commit := commitTestRepository(t, root, "base")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := cache.NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	provider, _ := rag.NewFakeProvider(rag.EmbeddingProviderProfile{ProfileID: "repo-docs", ProviderID: "fake", ProviderType: "fake", Model: "fake", Dimensions: 4, BatchSize: 1, Timeout: time.Second})
	result, err := NewIndexer(store, provider).Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: commit, ChunkBytes: 16, MaxChunks: 1})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != cache.RepoDocSetPartial || result.FailedChunks == 0 {
		t.Fatalf("result = %#v", result)
	}
}

func TestIndexerStreamsProviderBatches(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "README.md", strings.Repeat("bounded repository documentation\n\n", 32))
	commit := commitTestRepository(t, root, "base")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := cache.NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	fake, _ := rag.NewFakeProvider(rag.EmbeddingProviderProfile{ProfileID: "repo-docs", ProviderID: "fake", ProviderType: "fake", Model: "fake", Dimensions: 4, BatchSize: 2, Timeout: time.Second})
	provider := &recordingEmbeddingProvider{EmbeddingProvider: fake}
	result, err := NewIndexer(store, provider).Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: commit, ChunkBytes: 32, BatchSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if result.State != cache.RepoDocSetReady || provider.calls < 2 || provider.maxBatch > 2 {
		t.Fatalf("result=%#v calls=%d max_batch=%d", result, provider.calls, provider.maxBatch)
	}
}

type recordingEmbeddingProvider struct {
	rag.EmbeddingProvider
	calls    int
	maxBatch int
}

func (p *recordingEmbeddingProvider) Embed(ctx context.Context, req rag.EmbedRequest) (rag.EmbedResponse, error) {
	p.calls++
	if len(req.Inputs) > p.maxBatch {
		p.maxBatch = len(req.Inputs)
	}
	return p.EmbeddingProvider.Embed(ctx, req)
}
