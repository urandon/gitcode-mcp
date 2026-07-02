package rag

import (
	"context"
	"math"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
)

func TestExactScanVectorStoreSearch(t *testing.T) {
	ctx := context.Background()
	store := newVectorTestStore(t, ctx)
	defer store.Close()
	namespace := mustUpsertVectorNamespace(t, ctx, store, "fixture-a", "sha256:one")
	mustUpsertVectorChunks(t, ctx, store, "fixture-a", []cache.Chunk{
		vectorTestChunk("chunk-a", "ISSUE-1", "hash-a"),
		vectorTestChunk("chunk-b", "ISSUE-1", "hash-b"),
		vectorTestChunk("chunk-c", "ISSUE-2", "hash-c"),
		vectorTestChunk("chunk-d", "ISSUE-2", "hash-d"),
	})
	mustUpsertVectorEmbedding(t, ctx, store, namespace.ID, "chunk-a", []float32{1, 0})
	mustUpsertVectorEmbedding(t, ctx, store, namespace.ID, "chunk-b", []float32{0.8, 0.6})
	mustUpsertVectorEmbedding(t, ctx, store, namespace.ID, "chunk-c", []float32{0, 1})
	mustUpsertVectorEmbedding(t, ctx, store, namespace.ID, "chunk-d", []float32{0.8, 0.6})

	results, err := NewExactScanVectorStore(store).Search(ctx, VectorSearchRequest{
		RepoID:      "fixture-a",
		NamespaceID: namespace.ID,
		QueryVector: []float32{1, 0},
		TopK:        3,
	})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	got := resultChunkIDs(results)
	want := []string{"chunk-a", "chunk-b", "chunk-d"}
	if !sameStrings(got, want) {
		t.Fatalf("chunk ids = %v, want %v", got, want)
	}
	if math.Abs(float64(results[0].Score-1)) > 0.0001 {
		t.Fatalf("top score = %f, want 1", results[0].Score)
	}
}

func TestExactScanVectorStoreFiltersNamespaceAndCurrentHashes(t *testing.T) {
	ctx := context.Background()
	store := newVectorTestStore(t, ctx)
	defer store.Close()
	namespace := mustUpsertVectorNamespace(t, ctx, store, "fixture-a", "sha256:one")
	otherNamespace := mustUpsertVectorNamespace(t, ctx, store, "fixture-a", "sha256:two")
	mustUpsertVectorChunks(t, ctx, store, "fixture-a", []cache.Chunk{
		vectorTestChunk("chunk-current", "ISSUE-1", "hash-current"),
		vectorTestChunk("chunk-stale", "ISSUE-1", "hash-new"),
		vectorTestChunk("chunk-other-source", "ISSUE-2", "hash-other"),
	})
	mustUpsertVectorEmbedding(t, ctx, store, namespace.ID, "chunk-current", []float32{1, 0})
	mustUpsertVectorEmbeddingWithHash(t, ctx, store, namespace.ID, "chunk-stale", "hash-old", []float32{1, 0})
	mustUpsertVectorEmbedding(t, ctx, store, otherNamespace.ID, "chunk-current", []float32{1, 0})
	mustUpsertVectorEmbedding(t, ctx, store, namespace.ID, "chunk-other-source", []float32{1, 0})

	results, err := NewExactScanVectorStore(store).Search(ctx, VectorSearchRequest{
		RepoID:      "fixture-a",
		NamespaceID: namespace.ID,
		QueryVector: []float32{1, 0},
		TopK:        10,
		SourceID:    "ISSUE-1",
	})
	if err != nil {
		t.Fatalf("Search returned error: %v", err)
	}
	got := resultChunkIDs(results)
	want := []string{"chunk-current"}
	if !sameStrings(got, want) {
		t.Fatalf("chunk ids = %v, want %v", got, want)
	}
}

func TestExactScanVectorStoreMalformedVector(t *testing.T) {
	ctx := context.Background()
	store := newVectorTestStore(t, ctx)
	defer store.Close()
	namespace := mustUpsertVectorNamespace(t, ctx, store, "fixture-a", "sha256:one")
	mustUpsertVectorChunks(t, ctx, store, "fixture-a", []cache.Chunk{vectorTestChunk("chunk-bad", "ISSUE-1", "hash-bad")})
	if err := store.UpsertChunkEmbedding(ctx, cache.ChunkEmbedding{
		RepoID:      "fixture-a",
		NamespaceID: namespace.ID,
		ChunkID:     "chunk-bad",
		Vector:      []byte{1, 2, 3},
		Dimensions:  2,
		DType:       DefaultEmbeddingDType,
	}); err != nil {
		t.Fatalf("UpsertChunkEmbedding returned error: %v", err)
	}

	_, err := NewExactScanVectorStore(store).Search(ctx, VectorSearchRequest{
		RepoID:      "fixture-a",
		NamespaceID: namespace.ID,
		QueryVector: []float32{1, 0},
		TopK:        1,
	})
	if err == nil {
		t.Fatalf("Search returned nil error for malformed vector")
	}
}

func TestFloat32VectorCodecNormalizesAndValidates(t *testing.T) {
	blob, err := EncodeNormalizedFloat32Vector([]float32{3, 4})
	if err != nil {
		t.Fatalf("EncodeNormalizedFloat32Vector returned error: %v", err)
	}
	vector, err := DecodeFloat32Vector(blob, 2)
	if err != nil {
		t.Fatalf("DecodeFloat32Vector returned error: %v", err)
	}
	if math.Abs(float64(vector[0]-0.6)) > 0.0001 || math.Abs(float64(vector[1]-0.8)) > 0.0001 {
		t.Fatalf("decoded vector = %v, want normalized [0.6 0.8]", vector)
	}
	if _, err := EncodeNormalizedFloat32Vector([]float32{0, 0}); err == nil {
		t.Fatalf("EncodeNormalizedFloat32Vector returned nil error for zero vector")
	}
	if _, err := DecodeFloat32Vector([]byte{1, 2, 3}, 2); err == nil {
		t.Fatalf("DecodeFloat32Vector returned nil error for malformed blob")
	}
}

func newVectorTestStore(t *testing.T, ctx context.Context) *cache.SQLiteStore {
	t.Helper()
	store, err := cache.NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatalf("NewSQLiteStore returned error: %v", err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner", Name: "fixture-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatalf("AddRepository returned error: %v", err)
	}
	return store
}

func mustUpsertVectorNamespace(t *testing.T, ctx context.Context, store *cache.SQLiteStore, repoID, revision string) cache.EmbeddingNamespace {
	t.Helper()
	namespace, err := store.UpsertEmbeddingNamespace(ctx, cache.EmbeddingNamespace{
		EmbeddingNamespaceIdentity: cache.EmbeddingNamespaceIdentity{
			RepoID:                repoID,
			ProfileID:             "qwen3-test",
			ProviderID:            "fake-local",
			ProviderType:          "fake",
			ModelID:               "fake-embedding",
			ModelRevision:         revision,
			Dimensions:            2,
			DType:                 DefaultEmbeddingDType,
			Normalization:         DefaultEmbeddingNormalization,
			DocumentInstructionID: DefaultDocumentInstructionID,
			QueryInstructionID:    DefaultQueryInstructionID,
			ChunkPolicyID:         "heading-v1",
			LanguagePolicyID:      DefaultLanguagePolicyID,
			ConfigHash:            "config-hash-" + revision,
		},
		CreatedAt: time.Unix(0, 0).UTC(),
		UpdatedAt: time.Unix(0, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("UpsertEmbeddingNamespace returned error: %v", err)
	}
	return namespace
}

func mustUpsertVectorChunks(t *testing.T, ctx context.Context, store *cache.SQLiteStore, repoID string, chunks []cache.Chunk) {
	t.Helper()
	graph := cache.SourceGraph{Source: cache.Source{
		RepoID:      repoID,
		ID:          "ISSUE-1",
		Kind:        "issue",
		Path:        "issues/1.md",
		Title:       "RAG vector store",
		Body:        "RAG vector store fixture",
		Status:      "open",
		ContentHash: "source-hash",
		CreatedAt:   time.Unix(0, 0).UTC(),
		UpdatedAt:   time.Unix(0, 0).UTC(),
	}}
	bySource := map[string][]cache.Chunk{}
	for _, chunk := range chunks {
		bySource[chunk.SourceID] = append(bySource[chunk.SourceID], chunk)
	}
	first := true
	for sourceID, sourceChunks := range bySource {
		sourceGraph := graph
		sourceGraph.Source.ID = sourceID
		sourceGraph.Source.Path = "issues/" + sourceID + ".md"
		sourceGraph.Source.ContentHash = "source-hash-" + sourceID
		sourceGraph.Chunks = sourceChunks
		if !first {
			sourceGraph.Source.Title = "RAG vector store " + sourceID
		}
		first = false
		if err := store.UpsertSourceGraph(ctx, sourceGraph); err != nil {
			t.Fatalf("UpsertSourceGraph returned error: %v", err)
		}
	}
}

func vectorTestChunk(id, sourceID, contentHash string) cache.Chunk {
	return cache.Chunk{
		RepoID:         "fixture-a",
		ID:             id,
		SourceID:       sourceID,
		RecordID:       sourceID,
		ContentHash:    contentHash,
		ByteStart:      0,
		ByteEnd:        10,
		LineStart:      1,
		LineEnd:        1,
		Text:           id,
		NormalizedText: id,
		Policy:         "heading-v1",
	}
}

func mustUpsertVectorEmbedding(t *testing.T, ctx context.Context, store *cache.SQLiteStore, namespaceID, chunkID string, vector []float32) {
	t.Helper()
	mustUpsertVectorEmbeddingWithHash(t, ctx, store, namespaceID, chunkID, "", vector)
}

func mustUpsertVectorEmbeddingWithHash(t *testing.T, ctx context.Context, store *cache.SQLiteStore, namespaceID, chunkID, contentHash string, vector []float32) {
	t.Helper()
	blob, err := EncodeNormalizedFloat32Vector(vector)
	if err != nil {
		t.Fatalf("EncodeNormalizedFloat32Vector returned error: %v", err)
	}
	embedding := cache.ChunkEmbedding{
		RepoID:           "fixture-a",
		NamespaceID:      namespaceID,
		ChunkID:          chunkID,
		Vector:           blob,
		Dimensions:       len(vector),
		DType:            DefaultEmbeddingDType,
		ChunkContentHash: contentHash,
	}
	if err := store.UpsertChunkEmbedding(ctx, embedding); err != nil {
		t.Fatalf("UpsertChunkEmbedding returned error: %v", err)
	}
}

func resultChunkIDs(results []VectorSearchResult) []string {
	ids := make([]string, 0, len(results))
	for _, result := range results {
		ids = append(ids, result.ChunkID)
	}
	return ids
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
