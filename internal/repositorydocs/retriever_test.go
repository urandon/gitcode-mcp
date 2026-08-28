package repositorydocs

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/rag"
)

func TestRetrieverHydratesHistoricalCommitWithVerifiedCitation(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "docs/guide.md", "# Guide\n\nHistorical sentinel explains the offline cache.\n")
	historical := commitTestRepository(t, root, "historical")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	store := repositoryDocsSearchStore(t, ctx)
	defer store.Close()
	provider := repositoryDocsSearchProvider(t)
	indexed, err := NewIndexer(store, provider).Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: historical})
	if err != nil || indexed.State != cache.RepoDocSetReady {
		t.Fatalf("indexed=%#v err=%v", indexed, err)
	}
	writeTestFile(t, root, "docs/guide.md", "# Guide\n\nCurrent content no longer has the marker.\n")
	_ = commitTestRepository(t, root, "current")

	result, err := NewRetriever(store, provider).Search(ctx, SearchRequest{RepoID: "owner/repo", Repository: repo, Revision: historical, Query: "Historical sentinel", Mode: SearchModeHybrid})
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectiveRevision != historical || result.RevisionSetID != indexed.RevisionSetID || len(result.Hits) == 0 {
		t.Fatalf("result = %#v", result)
	}
	if result.RequestedMode != SearchModeHybrid || result.EffectiveMode != SearchModeHybrid || result.Authority != "git" || result.PolicySource == "" {
		t.Fatalf("result contract = %#v", result)
	}
	hit := result.Hits[0]
	if !strings.Contains(hit.Snippet, "Historical sentinel") || hit.Citation.CommitOID != historical || hit.Citation.BlobOID == "" || hit.Citation.RawSliceDigest == "" {
		t.Fatalf("hit = %#v", hit)
	}
}

func TestRetrieverFullTextWorksWithoutIndexOrProvider(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "README.md", "Русский поиск и 中文搜索 work offline.\n")
	commit := commitTestRepository(t, root, "base")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	store := repositoryDocsSearchStore(t, ctx)
	defer store.Close()
	result, err := NewRetriever(store, nil).Search(ctx, SearchRequest{RepoID: "owner/repo", Repository: repo, Revision: commit, Query: "中文搜索", Mode: SearchModeFullText})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hits) != 1 || result.Fallback != "" || result.RequestedMode != SearchModeFullText || result.EffectiveMode != SearchModeFullText || result.Authority != "git" || result.Coverage.State != "lexical-only" || result.Coverage.EligibleFiles != 1 || result.Coverage.EligibleChunks != 1 || !strings.Contains(result.Hits[0].Snippet, "中文搜索") {
		t.Fatalf("result = %#v", result)
	}
}

func TestTruncateUTF8BytesKeepsValidMultilingualSnippet(t *testing.T) {
	value := strings.Repeat("中文 русский ", 100)
	got := truncateUTF8Bytes(value, 800)
	if len(got) > 800 || !utf8.ValidString(got) {
		t.Fatalf("truncated snippet bytes=%d valid=%t", len(got), utf8.ValidString(got))
	}
}

func TestFuseRanksDoesNotBreakScoreTiesByPath(t *testing.T) {
	lexical := map[string]rankedHit{
		"en": {candidate: cache.RepositoryDocCandidate{RepositoryDocMembership: cache.RepositoryDocMembership{ChunkID: "en", Path: "docs/runtime-en.md"}}, score: 1, lexical: 1},
		"zh": {candidate: cache.RepositoryDocCandidate{RepositoryDocMembership: cache.RepositoryDocMembership{ChunkID: "zh", Path: "docs/runtime-zh.md"}}, score: 1, lexical: 1},
	}
	semantic := map[string]rankedHit{
		"en": {candidate: lexical["en"].candidate, score: 0.71, semantic: 0.71},
		"zh": {candidate: lexical["zh"].candidate, score: 0.79, semantic: 0.79},
	}

	got := fuseRanks(lexical, semantic)
	if len(got) != 2 || got[0].candidate.ChunkID != "zh" {
		t.Fatalf("fused ranks = %#v, want semantic winner after lexical tie", got)
	}
}

func repositoryDocsSearchStore(t *testing.T, ctx context.Context) *cache.SQLiteStore {
	t.Helper()
	store, err := cache.NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	return store
}

func repositoryDocsSearchProvider(t *testing.T) *rag.FakeProvider {
	t.Helper()
	provider, err := rag.NewFakeProvider(rag.EmbeddingProviderProfile{ProfileID: "repo-docs", ProviderID: "fake", ProviderType: "fake", Model: "fake", Dimensions: 8, BatchSize: 2, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}
