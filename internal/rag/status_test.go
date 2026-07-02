package rag

import (
	"context"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/service"
)

func TestStatusEmptyCacheWithoutNamespace(t *testing.T) {
	ctx := context.Background()
	store := newVectorTestStore(t, ctx)
	defer store.Close()
	provider := mustFakeIndexerProvider(t, 2)

	result, err := Status(ctx, store, provider, StatusRequest{RepoID: "fixture-a"})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if result.Status != "empty" || result.Namespace.Exists || result.Coverage.TotalChunks != 0 || !result.Provider.Ready {
		t.Fatalf("result=%#v", result)
	}
}

func TestStatusNoNamespaceWithChunks(t *testing.T) {
	ctx := context.Background()
	store := newVectorTestStore(t, ctx)
	defer store.Close()
	mustUpsertVectorChunks(t, ctx, store, "fixture-a", []cache.Chunk{vectorTestChunk("chunk-a", "ISSUE-1", "hash-a")})
	provider := mustFakeIndexerProvider(t, 2)

	result, err := Status(ctx, store, provider, StatusRequest{RepoID: "fixture-a"})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if result.Status != "no_namespace" || result.Namespace.Exists || result.Coverage.TotalChunks != 1 || result.Coverage.MissingChunks != 1 {
		t.Fatalf("result=%#v", result)
	}
}

func TestStatusPartialAndStaleCoverage(t *testing.T) {
	ctx := context.Background()
	store := newVectorTestStore(t, ctx)
	defer store.Close()
	mustUpsertVectorChunks(t, ctx, store, "fixture-a", []cache.Chunk{
		vectorTestChunk("chunk-fresh", "ISSUE-1", "hash-fresh"),
		vectorTestChunk("chunk-stale", "ISSUE-1", "hash-new"),
		vectorTestChunk("chunk-missing", "ISSUE-2", "hash-missing"),
	})
	provider := mustFakeIndexerProvider(t, 2)
	namespace, err := EnsureEmbeddingNamespace(ctx, store, provider, NamespaceRequest{RepoID: "fixture-a", ChunkPolicyID: DefaultChunkPolicyID, LanguagePolicyID: DefaultLanguagePolicyID, DocumentInstructionID: DefaultDocumentInstructionID, QueryInstructionID: DefaultQueryInstructionID})
	if err != nil {
		t.Fatalf("EnsureEmbeddingNamespace returned error: %v", err)
	}
	mustUpsertVectorEmbedding(t, ctx, store, namespace.ID, "chunk-fresh", []float32{1, 0})
	mustUpsertVectorEmbeddingWithHash(t, ctx, store, namespace.ID, "chunk-stale", "hash-old", []float32{0, 1})

	result, err := Status(ctx, store, provider, StatusRequest{RepoID: "fixture-a"})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if result.Status != "partial" || !result.Namespace.Exists || result.Coverage.TotalChunks != 3 || result.Coverage.EmbeddedChunks != 1 || result.Coverage.MissingChunks != 2 || result.Coverage.StaleChunks != 1 {
		t.Fatalf("result=%#v", result)
	}
}

func TestStatusReportsFailedLastRunAndActiveJob(t *testing.T) {
	ctx := context.Background()
	store := newVectorTestStore(t, ctx)
	defer store.Close()
	mustUpsertVectorChunks(t, ctx, store, "fixture-a", []cache.Chunk{vectorTestChunk("chunk-a", "ISSUE-1", "hash-a")})
	provider := mustFakeIndexerProvider(t, 2)
	namespace, err := EnsureEmbeddingNamespace(ctx, store, provider, NamespaceRequest{RepoID: "fixture-a", ChunkPolicyID: DefaultChunkPolicyID, LanguagePolicyID: DefaultLanguagePolicyID, DocumentInstructionID: DefaultDocumentInstructionID, QueryInstructionID: DefaultQueryInstructionID})
	if err != nil {
		t.Fatalf("EnsureEmbeddingNamespace returned error: %v", err)
	}
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	if err := store.UpsertRAGIndexRun(ctx, cache.RAGIndexRun{RepoID: "fixture-a", ID: "rag-run-failed", NamespaceID: namespace.ID, ProfileID: "fake-rag", Status: RAGIndexStatusFailed, TotalChunks: 1, FailedChunks: 1, StartedAt: now, UpdatedAt: now, CompletedAt: now, ErrorClass: "provider_error", Message: "provider down"}); err != nil {
		t.Fatalf("UpsertRAGIndexRun returned error: %v", err)
	}
	active := &JobStatus{ID: "job-1", Type: "rag-index", RepoID: "fixture-a", Status: RAGIndexStatusRunning, Steps: 1, Completed: 0, Progress: []service.ProgressEvent{{Type: "started", Phase: "running"}}}

	result, err := Status(ctx, store, provider, StatusRequest{RepoID: "fixture-a", ActiveJob: active})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if result.Status != "indexing" || result.ActiveJob == nil || result.LastRun == nil || result.LastRun.ID != "rag-run-failed" || result.Coverage.FailedChunks != 1 {
		t.Fatalf("result=%#v", result)
	}
}

func TestStatusReportsPartialWhenSucceededRunHasFailedChunks(t *testing.T) {
	ctx := context.Background()
	store := newVectorTestStore(t, ctx)
	defer store.Close()
	mustUpsertVectorChunks(t, ctx, store, "fixture-a", []cache.Chunk{
		vectorTestChunk("chunk-a", "ISSUE-1", "hash-a"),
		vectorTestChunk("chunk-bad", "ISSUE-1", "hash-bad"),
	})
	provider := mustFakeIndexerProvider(t, 2)
	namespace, err := EnsureEmbeddingNamespace(ctx, store, provider, NamespaceRequest{RepoID: "fixture-a", ChunkPolicyID: DefaultChunkPolicyID, LanguagePolicyID: DefaultLanguagePolicyID, DocumentInstructionID: DefaultDocumentInstructionID, QueryInstructionID: DefaultQueryInstructionID})
	if err != nil {
		t.Fatalf("EnsureEmbeddingNamespace returned error: %v", err)
	}
	mustUpsertVectorEmbedding(t, ctx, store, namespace.ID, "chunk-a", []float32{1, 0})
	now := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	if err := store.UpsertRAGIndexRun(ctx, cache.RAGIndexRun{RepoID: "fixture-a", ID: "rag-run-partial", NamespaceID: namespace.ID, ProfileID: "fake-rag", Status: RAGIndexStatusSucceeded, TotalChunks: 2, EmbeddedChunks: 1, FailedChunks: 1, StartedAt: now, UpdatedAt: now, CompletedAt: now, Message: "rag index finished with 1 failed chunks"}); err != nil {
		t.Fatalf("UpsertRAGIndexRun returned error: %v", err)
	}

	result, err := Status(ctx, store, provider, StatusRequest{RepoID: "fixture-a"})
	if err != nil {
		t.Fatalf("Status returned error: %v", err)
	}
	if result.Status != "partial" || result.Coverage.EmbeddedChunks != 1 || result.Coverage.MissingChunks != 1 || result.Coverage.FailedChunks != 1 {
		t.Fatalf("result=%#v", result)
	}
}
