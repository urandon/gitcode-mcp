package rag

import (
	"context"
	"errors"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
)

func TestRAGIndexerEmbedsMissingAndSkipsFreshChunks(t *testing.T) {
	ctx := context.Background()
	store := newVectorTestStore(t, ctx)
	defer store.Close()
	mustUpsertVectorChunks(t, ctx, store, "fixture-a", []cache.Chunk{
		vectorTestChunk("chunk-a", "ISSUE-1", "hash-a"),
		vectorTestChunk("chunk-b", "ISSUE-1", "hash-b"),
		vectorTestChunk("chunk-c", "ISSUE-2", "hash-c"),
	})
	provider := mustFakeIndexerProvider(t, 2)
	namespace, err := EnsureEmbeddingNamespace(ctx, store, provider, NamespaceRequest{RepoID: "fixture-a", ChunkPolicyID: DefaultChunkPolicyID, LanguagePolicyID: DefaultLanguagePolicyID, DocumentInstructionID: DefaultDocumentInstructionID, QueryInstructionID: DefaultQueryInstructionID})
	if err != nil {
		t.Fatalf("EnsureEmbeddingNamespace returned error: %v", err)
	}
	mustUpsertVectorEmbedding(t, ctx, store, namespace.ID, "chunk-a", []float32{1, 0})

	result, err := NewRAGIndexer(store, provider, RAGIndexerOptions{}).Run(ctx, IndexRequest{RepoID: "fixture-a", ChunkPolicyID: DefaultChunkPolicyID, BatchSize: 2})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Status != RAGIndexStatusSucceeded || result.TotalChunks != 3 || result.SkippedChunks != 1 || result.EmbeddedChunks != 2 || result.FailedChunks != 0 {
		t.Fatalf("result=%#v", result)
	}
	embeddings, err := store.ListChunkEmbeddings(ctx, cache.ChunkEmbeddingFilter{RepoID: "fixture-a", NamespaceID: namespace.ID})
	if err != nil {
		t.Fatalf("ListChunkEmbeddings returned error: %v", err)
	}
	if len(embeddings) != 3 {
		t.Fatalf("embeddings=%d, want 3", len(embeddings))
	}
	run, err := store.GetRAGIndexRun(ctx, "fixture-a", result.RunID)
	if err != nil {
		t.Fatalf("GetRAGIndexRun returned error: %v", err)
	}
	if run.Status != RAGIndexStatusSucceeded || run.EmbeddedChunks != 2 || run.SkippedChunks != 1 {
		t.Fatalf("run=%#v", run)
	}
}

func TestRAGIndexerResumesStaleCoverage(t *testing.T) {
	ctx := context.Background()
	store := newVectorTestStore(t, ctx)
	defer store.Close()
	mustUpsertVectorChunks(t, ctx, store, "fixture-a", []cache.Chunk{
		vectorTestChunk("chunk-a", "ISSUE-1", "hash-new"),
		vectorTestChunk("chunk-b", "ISSUE-1", "hash-b"),
	})
	provider := mustFakeIndexerProvider(t, 2)
	namespace, err := EnsureEmbeddingNamespace(ctx, store, provider, NamespaceRequest{RepoID: "fixture-a", ChunkPolicyID: DefaultChunkPolicyID, LanguagePolicyID: DefaultLanguagePolicyID, DocumentInstructionID: DefaultDocumentInstructionID, QueryInstructionID: DefaultQueryInstructionID})
	if err != nil {
		t.Fatalf("EnsureEmbeddingNamespace returned error: %v", err)
	}
	mustUpsertVectorEmbeddingWithHash(t, ctx, store, namespace.ID, "chunk-a", "hash-old", []float32{1, 0})
	mustUpsertVectorEmbedding(t, ctx, store, namespace.ID, "chunk-b", []float32{0, 1})

	result, err := NewRAGIndexer(store, provider, RAGIndexerOptions{}).Run(ctx, IndexRequest{RepoID: "fixture-a", ChunkPolicyID: DefaultChunkPolicyID, BatchSize: 1})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.EmbeddedChunks != 1 || result.SkippedChunks != 1 {
		t.Fatalf("result=%#v", result)
	}
	embeddings, err := store.ListChunkEmbeddings(ctx, cache.ChunkEmbeddingFilter{RepoID: "fixture-a", NamespaceID: namespace.ID, ChunkID: "chunk-a"})
	if err != nil {
		t.Fatalf("ListChunkEmbeddings returned error: %v", err)
	}
	if len(embeddings) != 1 || embeddings[0].ChunkContentHash != "hash-new" {
		t.Fatalf("stale embedding not refreshed: %#v", embeddings)
	}
}

func TestRAGIndexerRecordsProviderFailure(t *testing.T) {
	ctx := context.Background()
	store := newVectorTestStore(t, ctx)
	defer store.Close()
	mustUpsertVectorChunks(t, ctx, store, "fixture-a", []cache.Chunk{vectorTestChunk("chunk-a", "ISSUE-1", "hash-a")})
	provider := &failingProvider{profile: fakeIndexerProfile(2), err: providerError(ProviderFailureUnavailable, "provider down", errors.New("dial failed"))}

	result, err := NewRAGIndexer(store, provider, RAGIndexerOptions{}).Run(ctx, IndexRequest{RepoID: "fixture-a", ChunkPolicyID: DefaultChunkPolicyID})
	if err == nil {
		t.Fatalf("Run returned nil error")
	}
	if result.Status != RAGIndexStatusFailed || result.FailedChunks != 1 || result.ErrorClass != ProviderFailureUnavailable {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestRAGIndexerFallsBackAndContinuesPastBadChunk(t *testing.T) {
	ctx := context.Background()
	store := newVectorTestStore(t, ctx)
	defer store.Close()
	mustUpsertVectorChunks(t, ctx, store, "fixture-a", []cache.Chunk{
		vectorTestChunk("chunk-a", "ISSUE-1", "hash-a"),
		vectorTestChunk("chunk-bad", "ISSUE-1", "hash-bad"),
		vectorTestChunk("chunk-c", "ISSUE-1", "hash-c"),
	})
	provider := &badInputProvider{base: mustFakeIndexerProvider(t, 2), badInput: "chunk-bad"}

	result, err := NewRAGIndexer(store, provider, RAGIndexerOptions{}).Run(ctx, IndexRequest{RepoID: "fixture-a", ChunkPolicyID: DefaultChunkPolicyID, BatchSize: 3})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Status != RAGIndexStatusSucceeded || result.EmbeddedChunks != 2 || result.FailedChunks != 1 {
		t.Fatalf("result=%#v", result)
	}
	if result.Message != "rag index finished with 1 failed chunks" {
		t.Fatalf("message=%q", result.Message)
	}
	namespace, err := EnsureEmbeddingNamespace(ctx, store, provider, NamespaceRequest{RepoID: "fixture-a", ChunkPolicyID: DefaultChunkPolicyID, LanguagePolicyID: DefaultLanguagePolicyID, DocumentInstructionID: DefaultDocumentInstructionID, QueryInstructionID: DefaultQueryInstructionID})
	if err != nil {
		t.Fatalf("EnsureEmbeddingNamespace returned error: %v", err)
	}
	embeddings, err := store.ListChunkEmbeddings(ctx, cache.ChunkEmbeddingFilter{RepoID: "fixture-a", NamespaceID: namespace.ID})
	if err != nil {
		t.Fatalf("ListChunkEmbeddings returned error: %v", err)
	}
	if len(embeddings) != 2 {
		t.Fatalf("embeddings=%d, want 2", len(embeddings))
	}
}

func mustFakeIndexerProvider(t *testing.T, dimensions int) *FakeProvider {
	t.Helper()
	provider, err := NewFakeProvider(fakeIndexerProfile(dimensions))
	if err != nil {
		t.Fatalf("NewFakeProvider returned error: %v", err)
	}
	return provider
}

func fakeIndexerProfile(dimensions int) EmbeddingProviderProfile {
	return EmbeddingProviderProfile{
		ProfileID:    "fake-rag",
		ProviderID:   "fake-local",
		ProviderType: "fake",
		Model:        "fake-embedding",
		Dimensions:   dimensions,
		BatchSize:    2,
		Timeout:      time.Second,
	}
}

type failingProvider struct {
	profile EmbeddingProviderProfile
	err     error
}

func (p *failingProvider) Profile() EmbeddingProviderProfile { return p.profile }

func (p *failingProvider) ModelInfo(context.Context) (EmbeddingModelInfo, error) {
	return EmbeddingModelInfo{Model: p.profile.Model, Revision: "failing"}, nil
}

func (p *failingProvider) Embed(context.Context, EmbedRequest) (EmbedResponse, error) {
	return EmbedResponse{}, p.err
}

func (p *failingProvider) NamespaceIdentity(ctx context.Context, req NamespaceRequest) (cache.EmbeddingNamespaceIdentity, error) {
	info, err := p.ModelInfo(ctx)
	if err != nil {
		return cache.EmbeddingNamespaceIdentity{}, err
	}
	return namespaceIdentity(p.profile, info, req)
}

type badInputProvider struct {
	base     *FakeProvider
	badInput string
}

func (p *badInputProvider) Profile() EmbeddingProviderProfile { return p.base.Profile() }

func (p *badInputProvider) ModelInfo(ctx context.Context) (EmbeddingModelInfo, error) {
	return p.base.ModelInfo(ctx)
}

func (p *badInputProvider) Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error) {
	for _, input := range req.Inputs {
		if input == p.badInput {
			return EmbedResponse{}, providerError(ProviderFailureUnavailable, "provider down", errors.New("bad input"))
		}
	}
	return p.base.Embed(ctx, req)
}

func (p *badInputProvider) NamespaceIdentity(ctx context.Context, req NamespaceRequest) (cache.EmbeddingNamespaceIdentity, error) {
	return p.base.NamespaceIdentity(ctx, req)
}
