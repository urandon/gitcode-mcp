package repositorydocs

import (
	"context"
	"errors"
	"fmt"
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

func TestRetrieverMultilingualLexicalHybridAndCitationMatrix(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "docs/runtime-en.md", "Writer admission uses a durable generation fence.\n")
	writeTestFile(t, root, "docs/runtime-ru.md", "Повторный запуск сохраняет устойчивый идентификатор задания.\n")
	writeTestFile(t, root, "docs/runtime-zh.md", "离线检索返回经过摘要验证的引用。\n")
	commit := commitTestRepository(t, root, "multilingual retrieval")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	store := repositoryDocsSearchStore(t, ctx)
	defer store.Close()
	provider := repositoryDocsSearchProvider(t)
	indexed, err := NewIndexer(store, provider).Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: commit})
	if err != nil || indexed.State != cache.RepoDocSetReady {
		t.Fatalf("indexed=%#v err=%v", indexed, err)
	}
	cases := []struct {
		query string
		path  string
	}{
		{query: "durable generation fence", path: "docs/runtime-en.md"},
		{query: "устойчивый идентификатор задания", path: "docs/runtime-ru.md"},
		{query: "摘要验证的引用", path: "docs/runtime-zh.md"},
	}
	for _, mode := range []string{SearchModeFullText, SearchModeHybrid} {
		for _, test := range cases {
			result, err := NewRetriever(store, provider).Search(ctx, SearchRequest{RepoID: "owner/repo", Repository: repo, Revision: commit, Query: test.query, Mode: mode, Limit: 3})
			if err != nil {
				t.Fatalf("mode=%s query=%q: %v", mode, test.query, err)
			}
			if len(result.Hits) == 0 || result.Hits[0].Citation.Path != test.path || result.Hits[0].Citation.CommitOID != commit || result.Hits[0].Citation.BlobOID == "" || result.Hits[0].Citation.RawSliceDigest == "" {
				t.Fatalf("mode=%s query=%q result=%#v", mode, test.query, result)
			}
			if mode == SearchModeHybrid && (result.EffectiveMode != SearchModeHybrid || result.Hits[0].Semantic == 0) {
				t.Fatalf("hybrid contribution missing for query=%q: %#v", test.query, result.Hits[0])
			}
		}
	}
}

func TestRetrieverDoesNotScoreARevisionSetFromAnotherEmbeddingNamespace(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "README.md", "namespace sentinel remains available lexically\n")
	commit := commitTestRepository(t, root, "base")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	store := repositoryDocsSearchStore(t, ctx)
	defer store.Close()
	providerA, _ := rag.NewFakeProvider(rag.EmbeddingProviderProfile{ProfileID: "a", ProviderID: "fake", ProviderType: "fake", Model: "model-a", Dimensions: 8, BatchSize: 2, Timeout: time.Second})
	providerB, _ := rag.NewFakeProvider(rag.EmbeddingProviderProfile{ProfileID: "b", ProviderID: "fake", ProviderType: "fake", Model: "model-b", Dimensions: 8, BatchSize: 2, Timeout: time.Second})
	indexed, err := NewIndexer(store, providerA).Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: commit})
	if err != nil || indexed.State != cache.RepoDocSetReady {
		t.Fatalf("indexed=%#v err=%v", indexed, err)
	}

	result, err := NewRetriever(store, providerB).Search(ctx, SearchRequest{RepoID: "owner/repo", Repository: repo, Revision: commit, Query: "namespace sentinel", Mode: SearchModeHybrid})
	if err != nil {
		t.Fatal(err)
	}
	if result.RevisionSetID != "" || result.NamespaceID != "" || result.EffectiveMode != SearchModeFullText || result.Fallback != "revision_set_unavailable" || len(result.Hits) == 0 {
		t.Fatalf("result = %#v", result)
	}
	if len(result.WarningDetails) != 1 || result.WarningDetails[0].Code != "revision_set_unavailable" || result.WarningDetails[0].Message != result.Warnings[0] {
		t.Fatalf("typed warnings = %#v legacy=%#v", result.WarningDetails, result.Warnings)
	}
}

type mismatchedQueryEmbeddingProvider struct {
	*rag.FakeProvider
}

func (p mismatchedQueryEmbeddingProvider) Embed(context.Context, rag.EmbedRequest) (rag.EmbedResponse, error) {
	return rag.EmbedResponse{Model: "fake", Revision: "fake-v1", Dimensions: 3, Embeddings: [][]float32{{1, 0, 0}}}, nil
}

func TestRetrieverFallsBackWithTypedWarningOnQueryDimensionMismatch(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "README.md", "dimension mismatch remains available through verified lexical retrieval\n")
	commit := commitTestRepository(t, root, "dimension fallback")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	store := repositoryDocsSearchStore(t, ctx)
	defer store.Close()
	provider := repositoryDocsSearchProvider(t)
	indexed, err := NewIndexer(store, provider).Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: commit})
	if err != nil || indexed.State != cache.RepoDocSetReady {
		t.Fatalf("indexed=%#v err=%v", indexed, err)
	}

	result, err := NewRetriever(store, mismatchedQueryEmbeddingProvider{FakeProvider: provider}).Search(ctx, SearchRequest{RepoID: "owner/repo", Repository: repo, Revision: commit, Query: "dimension mismatch", Mode: SearchModeHybrid})
	if err != nil {
		t.Fatal(err)
	}
	if result.EffectiveMode != SearchModeFullText || result.Fallback != "query_embedding_dimension_mismatch" || len(result.Hits) == 0 {
		t.Fatalf("fallback result=%#v", result)
	}
	if len(result.WarningDetails) != 1 || result.WarningDetails[0].Code != "query_embedding_dimension_mismatch" || result.WarningDetails[0].Message != result.Warnings[0] {
		t.Fatalf("typed warnings=%#v legacy=%#v", result.WarningDetails, result.Warnings)
	}
}

type pruningSearchStore struct {
	*cache.SQLiteStore
	pruned bool
}

func (s *pruningSearchStore) LoadRepositoryDocSearchSnapshot(ctx context.Context, repoID, setID, namespaceID string) (cache.RepositoryDocSearchSnapshot, error) {
	snapshot, err := s.SQLiteStore.LoadRepositoryDocSearchSnapshot(ctx, repoID, setID, namespaceID)
	if err != nil {
		return snapshot, err
	}
	_, err = s.SQLiteStore.PruneRepositoryDocRevisionSets(ctx, repoID, cache.RepositoryDocRetentionPolicy{RetainCommittedPerIdentity: 1})
	s.pruned = err == nil
	return snapshot, err
}

func TestRetrieverCompletesFromExactSnapshotWhenGCEvictsHistoricalSet(t *testing.T) {
	ctx := context.Background()
	root := initTestRepository(t)
	writeTestFile(t, root, "docs/guide.md", "historical snapshot survives concurrent retention\n")
	historical := commitTestRepository(t, root, "historical")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	store := repositoryDocsSearchStore(t, ctx)
	defer store.Close()
	provider := repositoryDocsSearchProvider(t)
	historicalSet, err := NewIndexer(store, provider).Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: historical})
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, root, "docs/guide.md", "current snapshot remains protected\n")
	current := commitTestRepository(t, root, "current")
	if _, err := NewIndexer(store, provider).Run(ctx, IndexRequest{RepoID: "owner/repo", Repository: repo, Revision: current}); err != nil {
		t.Fatal(err)
	}

	gcStore := &pruningSearchStore{SQLiteStore: store}
	result, err := NewRetriever(gcStore, provider).Search(ctx, SearchRequest{RepoID: "owner/repo", Repository: repo, Revision: historical, Query: "historical snapshot", Mode: SearchModeHybrid})
	if err != nil {
		t.Fatal(err)
	}
	if !gcStore.pruned || result.RevisionSetID != historicalSet.RevisionSetID || len(result.Hits) == 0 || !strings.Contains(result.Hits[0].Snippet, "historical snapshot") {
		t.Fatalf("search result after GC = %#v pruned=%t", result, gcStore.pruned)
	}
	if _, err := store.GetRepositoryDocRevisionSet(ctx, "owner/repo", historicalSet.RevisionSetID); !errors.Is(err, cache.ErrNotFound) {
		t.Fatalf("historical set still present after retention: %v", err)
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

func TestFuseRanksKeepsSemanticOnlyScoresOnRRFScaleAndDistinctPaths(t *testing.T) {
	left := cache.RepositoryDocCandidate{RepositoryDocMembership: cache.RepositoryDocMembership{ChunkID: "shared", Path: "docs/left.md"}}
	right := cache.RepositoryDocCandidate{RepositoryDocMembership: cache.RepositoryDocMembership{ChunkID: "shared", Path: "docs/right.md"}}
	semantic := map[string]rankedHit{
		candidateRankKey(left):  {candidate: left, score: 0.99, semantic: 0.99},
		candidateRankKey(right): {candidate: right, score: 0.50, semantic: 0.50},
	}

	got := fuseRanks(nil, semantic)
	if len(got) != 2 || got[0].candidate.Path != "docs/left.md" {
		t.Fatalf("fused ranks = %#v", got)
	}
	if got[0].score >= 0.1 || got[1].score >= 0.1 {
		t.Fatalf("semantic-only scores escaped reciprocal-rank scale: %#v", got)
	}
}

func TestTrimRankedHitsKeepsDistinctPathsWithTheSameChunkID(t *testing.T) {
	left := cache.RepositoryDocCandidate{RepositoryDocMembership: cache.RepositoryDocMembership{ChunkID: "shared", Path: "docs/left.md"}}
	right := cache.RepositoryDocCandidate{RepositoryDocMembership: cache.RepositoryDocMembership{ChunkID: "shared", Path: "docs/right.md"}}
	values := map[string]rankedHit{
		candidateRankKey(left):  {candidate: left, score: 100},
		candidateRankKey(right): {candidate: right, score: 99},
	}
	for index := 0; index < 64; index++ {
		candidate := cache.RepositoryDocCandidate{RepositoryDocMembership: cache.RepositoryDocMembership{ChunkID: fmt.Sprintf("chunk-%02d", index), Path: fmt.Sprintf("docs/%02d.md", index)}}
		values[candidateRankKey(candidate)] = rankedHit{candidate: candidate, score: float64(64 - index)}
	}
	trimmed := trimRankedHits(values, 64)
	if len(trimmed) != 64 {
		t.Fatalf("trimmed size=%d, want 64", len(trimmed))
	}
	if _, ok := trimmed[candidateRankKey(left)]; !ok {
		t.Fatal("left path with shared chunk id was dropped")
	}
	if _, ok := trimmed[candidateRankKey(right)]; !ok {
		t.Fatal("right path with shared chunk id was dropped")
	}
}

func BenchmarkRepositoryDocsFullTextRetrieval(b *testing.B) {
	ctx := context.Background()
	root := initTestRepository(b)
	for index := 0; index < 96; index++ {
		writeTestFile(b, root, fmt.Sprintf("docs/guide-%03d.md", index), strings.Repeat("English Русский 中文 bounded offline documentation.\n", 48))
	}
	commit := commitTestRepository(b, root, "benchmark corpus")
	repo, err := OpenRepository(ctx, root)
	if err != nil {
		b.Fatal(err)
	}
	store := repositoryDocsSearchStore(b, ctx)
	defer store.Close()
	retriever := NewRetriever(store, nil)
	b.ReportAllocs()
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		result, err := retriever.Search(ctx, SearchRequest{RepoID: "owner/repo", Repository: repo, Revision: commit, Query: "bounded offline documentation", Mode: SearchModeFullText, Limit: 10})
		if err != nil || len(result.Hits) == 0 {
			b.Fatalf("hits=%d err=%v", len(result.Hits), err)
		}
	}
}

func repositoryDocsSearchStore(t testing.TB, ctx context.Context) *cache.SQLiteStore {
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

func repositoryDocsSearchProvider(t testing.TB) *rag.FakeProvider {
	t.Helper()
	provider, err := rag.NewFakeProvider(rag.EmbeddingProviderProfile{ProfileID: "repo-docs", ProviderID: "fake", ProviderType: "fake", Model: "fake", Dimensions: 8, BatchSize: 2, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}
