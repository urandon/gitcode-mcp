package rag

import (
	"context"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
)

func TestRAGRetrieverHybridRanksSemanticAndLexicalCandidates(t *testing.T) {
	ctx := context.Background()
	store := newVectorTestStore(t, ctx)
	defer store.Close()
	namespace := mustUpsertVectorNamespace(t, ctx, store, "fixture-a", "stub-revision")
	chunks := []cache.Chunk{
		vectorTextChunk("chunk-semantic", "ISSUE-1", "hash-semantic", "daemon lifecycle design"),
		vectorTextChunk("chunk-hybrid", "ISSUE-2", "hash-hybrid", "api token rate limit retry plan"),
	}
	mustUpsertVectorChunks(t, ctx, store, "fixture-a", chunks)
	mustUpsertVectorEmbedding(t, ctx, store, namespace.ID, "chunk-semantic", []float32{1, 0})
	mustUpsertVectorEmbedding(t, ctx, store, namespace.ID, "chunk-hybrid", []float32{0.8, 0.6})
	provider := newStubSearchProvider(namespace.EmbeddingNamespaceIdentity, map[string][]float32{"api token": []float32{1, 0}})

	result, err := NewRAGRetriever(store, provider, RAGRetrieverOptions{Now: fixedSearchNow}).Search(ctx, SearchRequest{RepoID: "fixture-a", Query: "api token", Limit: 2})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if result.Status != RAGSearchStatusReady {
		t.Fatalf("status = %q, want %q", result.Status, RAGSearchStatusReady)
	}
	if result.SearchMode != SearchModeHybridRAG {
		t.Fatalf("search mode = %q, want %q", result.SearchMode, SearchModeHybridRAG)
	}
	if len(result.Results) != 2 {
		t.Fatalf("results = %d, want 2: %#v", len(result.Results), result.Results)
	}
	if result.Results[0].ChunkID != "chunk-hybrid" {
		t.Fatalf("top chunk = %q, want lexical-boosted hybrid chunk", result.Results[0].ChunkID)
	}
	if result.Results[0].Score.Semantic <= 0 || result.Results[0].Score.Lexical != 1 || result.Results[0].Score.Hybrid <= result.Results[1].Score.Hybrid {
		t.Fatalf("unexpected score breakdown: %#v", result.Results)
	}
	if result.Results[0].Path != "issues/ISSUE-2.md" || result.Results[0].LineStart != 1 || result.Results[0].NamespaceID != namespace.ID {
		t.Fatalf("missing context provenance: %#v", result.Results[0])
	}
}

func TestRAGRetrieverNoNamespaceIsStableEmptyResult(t *testing.T) {
	ctx := context.Background()
	store := newVectorTestStore(t, ctx)
	defer store.Close()
	identity := testSearchNamespaceIdentity("fixture-a")
	mustUpsertVectorChunks(t, ctx, store, "fixture-a", []cache.Chunk{vectorTextChunk("chunk-a", "ISSUE-1", "hash-a", "rate limits")})
	provider := newStubSearchProvider(identity, map[string][]float32{"rate": []float32{1, 0}})

	result, err := NewRAGRetriever(store, provider, RAGRetrieverOptions{Now: fixedSearchNow}).Search(ctx, SearchRequest{RepoID: "fixture-a", Query: "rate"})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if result.Status != RAGSearchStatusNoNamespace {
		t.Fatalf("status = %q, want %q", result.Status, RAGSearchStatusNoNamespace)
	}
	if len(result.Results) != 0 || len(result.Warnings) == 0 {
		t.Fatalf("no namespace should return empty results with warning: %#v", result)
	}
}

func TestRAGRetrieverReportsIncompleteCoverage(t *testing.T) {
	ctx := context.Background()
	store := newVectorTestStore(t, ctx)
	defer store.Close()
	namespace := mustUpsertVectorNamespace(t, ctx, store, "fixture-a", "stub-revision")
	mustUpsertVectorChunks(t, ctx, store, "fixture-a", []cache.Chunk{
		vectorTextChunk("chunk-fresh", "ISSUE-1", "hash-fresh", "fresh embedding"),
		vectorTextChunk("chunk-stale", "ISSUE-1", "hash-new", "stale embedding"),
		vectorTextChunk("chunk-missing", "ISSUE-1", "hash-missing", "missing embedding"),
	})
	mustUpsertVectorEmbedding(t, ctx, store, namespace.ID, "chunk-fresh", []float32{1, 0})
	mustUpsertVectorEmbeddingWithHash(t, ctx, store, namespace.ID, "chunk-stale", "hash-old", []float32{0.9, 0.1})
	provider := newStubSearchProvider(namespace.EmbeddingNamespaceIdentity, map[string][]float32{"fresh": []float32{1, 0}})

	result, err := NewRAGRetriever(store, provider, RAGRetrieverOptions{Now: fixedSearchNow}).Search(ctx, SearchRequest{RepoID: "fixture-a", Query: "fresh", Limit: 3})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	if result.Coverage.TotalChunks != 3 || result.Coverage.EmbeddedChunks != 1 || result.Coverage.StaleChunks != 1 || result.Coverage.MissingChunks != 1 {
		t.Fatalf("coverage=%#v", result.Coverage)
	}
	if len(result.Warnings) != 1 || result.Warnings[0] != "rag coverage is incomplete: 1 missing, 1 stale" {
		t.Fatalf("warnings=%#v", result.Warnings)
	}
}

func TestRAGRetrieverMultilingualDeterministicFixtures(t *testing.T) {
	ctx := context.Background()
	store := newVectorTestStore(t, ctx)
	defer store.Close()
	fixture := loadMultilingualEvalFixture(t)
	namespace := mustUpsertVectorNamespace(t, ctx, store, "fixture-a", "stub-revision")
	var chunks []cache.Chunk
	queryVectors := map[string][]float32{}
	for _, tc := range fixture.Cases {
		queryVectors[tc.Query] = tc.QueryVector
		for _, chunk := range tc.Chunks {
			cacheChunk := vectorTextChunk(chunk.ID, chunk.SourceID, "hash-"+chunk.ID, chunk.Text)
			chunks = append(chunks, cacheChunk)
		}
	}
	mustUpsertVectorChunks(t, ctx, store, "fixture-a", chunks)
	for _, tc := range fixture.Cases {
		for _, chunk := range tc.Chunks {
			mustUpsertVectorEmbedding(t, ctx, store, namespace.ID, chunk.ID, chunk.Vector)
		}
	}
	provider := newStubSearchProvider(namespace.EmbeddingNamespaceIdentity, queryVectors)

	for _, tc := range fixture.Cases {
		t.Run(tc.Language, func(t *testing.T) {
			result, err := NewRAGRetriever(store, provider, RAGRetrieverOptions{Now: fixedSearchNow}).Search(ctx, SearchRequest{RepoID: "fixture-a", Query: tc.Query, Limit: 1})
			if err != nil {
				t.Fatalf("Search returned error: %v", err)
			}
			if len(result.Results) != 1 || result.Results[0].ChunkID != tc.ExpectedChunkID {
				t.Fatalf("top result = %#v, want %s", result.Results, tc.ExpectedChunkID)
			}
		})
	}
}

func vectorTextChunk(id, sourceID, contentHash, text string) cache.Chunk {
	chunk := vectorTestChunk(id, sourceID, contentHash)
	chunk.Text = text
	chunk.NormalizedText = text
	return chunk
}

func fixedSearchNow() time.Time {
	return time.Unix(100, 0).UTC()
}

type stubSearchProvider struct {
	identity   cache.EmbeddingNamespaceIdentity
	embeddings map[string][]float32
}

func newStubSearchProvider(identity cache.EmbeddingNamespaceIdentity, embeddings map[string][]float32) stubSearchProvider {
	return stubSearchProvider{identity: identity, embeddings: embeddings}
}

func (p stubSearchProvider) Profile() EmbeddingProviderProfile {
	return EmbeddingProviderProfile{ProfileID: p.identity.ProfileID, ProviderID: p.identity.ProviderID, ProviderType: p.identity.ProviderType, Model: p.identity.ModelID, Dimensions: p.identity.Dimensions, BatchSize: 8}
}

func (p stubSearchProvider) ModelInfo(context.Context) (EmbeddingModelInfo, error) {
	return EmbeddingModelInfo{Model: p.identity.ModelID, Revision: p.identity.ModelRevision}, nil
}

func (p stubSearchProvider) Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error) {
	if err := ctx.Err(); err != nil {
		return EmbedResponse{}, err
	}
	out := make([][]float32, 0, len(req.Inputs))
	for _, input := range req.Inputs {
		vector := p.embeddings[input]
		if len(vector) == 0 {
			vector = make([]float32, p.identity.Dimensions)
			vector[0] = 1
		}
		out = append(out, vector)
	}
	return EmbedResponse{Model: p.identity.ModelID, Revision: p.identity.ModelRevision, Dimensions: p.identity.Dimensions, Embeddings: out}, nil
}

func (p stubSearchProvider) NamespaceIdentity(context.Context, NamespaceRequest) (cache.EmbeddingNamespaceIdentity, error) {
	return p.identity, nil
}

func testSearchNamespaceIdentity(repoID string) cache.EmbeddingNamespaceIdentity {
	return cache.EmbeddingNamespaceIdentity{
		RepoID:                repoID,
		ProfileID:             "qwen3-test",
		ProviderID:            "fake-local",
		ProviderType:          "fake",
		ModelID:               "fake-embedding",
		ModelRevision:         "stub-revision",
		Dimensions:            2,
		DType:                 DefaultEmbeddingDType,
		Normalization:         DefaultEmbeddingNormalization,
		DocumentInstructionID: DefaultDocumentInstructionID,
		QueryInstructionID:    DefaultQueryInstructionID,
		ChunkPolicyID:         "heading-v1",
		LanguagePolicyID:      DefaultLanguagePolicyID,
		ConfigHash:            "config-hash-stub-revision",
	}
}
