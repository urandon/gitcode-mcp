package rag

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/observability"
)

func embeddedCounter(t *testing.T, metrics *observability.Metrics) float64 {
	t.Helper()
	for _, series := range metrics.Snapshot().Series {
		if series.Name == "rag_embedded" {
			if series.Value == nil {
				t.Fatal("embedding throughput origin missing")
			}
			return *series.Value
		}
	}
	t.Fatal("embedding throughput series missing")
	return 0
}

type metricFaultIndexStore struct {
	ragIndexStore
	metrics                         *observability.Metrics
	t                               *testing.T
	writeCalls, persisted, releases int
	failWrite, failRunAfter         int
	cancelAfterWrite                context.CancelFunc
	beforeRelease                   float64
}

func (s *metricFaultIndexStore) UpsertChunkEmbedding(ctx context.Context, embedding cache.ChunkEmbedding) error {
	s.writeCalls++
	if s.writeCalls == s.failWrite {
		return errors.New("synthetic vector persistence failure")
	}
	if err := s.ragIndexStore.UpsertChunkEmbedding(ctx, embedding); err != nil {
		return err
	}
	s.persisted++
	if s.cancelAfterWrite != nil {
		s.cancelAfterWrite()
	}
	return nil
}

func (s *metricFaultIndexStore) UpsertRAGIndexRun(ctx context.Context, run cache.RAGIndexRun) error {
	if s.failRunAfter > 0 && s.persisted >= s.failRunAfter {
		return errors.New("synthetic run persistence failure")
	}
	return s.ragIndexStore.UpsertRAGIndexRun(ctx, run)
}

func (s *metricFaultIndexStore) ReleaseWriter(ctx context.Context, lease *cache.WriterLease) error {
	if embeddedCounter(s.t, s.metrics) != s.beforeRelease {
		s.t.Fatal("throughput recorded while cache writer lease was held")
	}
	err := s.ragIndexStore.ReleaseWriter(ctx, lease)
	s.releases++
	return err
}

func TestRAGConfirmedThroughputRetainsPartialWritesAndSkipsResume(t *testing.T) {
	for _, tc := range []struct {
		name               string
		failWrite, failRun int
		cancel, fallback   bool
		wantFirst          float64
		wantError          bool
	}{
		{name: "fresh_skip", wantFirst: 2},
		{name: "vector_write_failure", failWrite: 2, wantFirst: 1, wantError: true},
		{name: "run_write_failure", failRun: 1, wantFirst: 1, wantError: true},
		{name: "cancel_after_confirmed_write", cancel: true, wantFirst: 1, wantError: true},
		{name: "batch_fallback", fallback: true, wantFirst: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			baseCtx := context.Background()
			store := newVectorTestStore(t, baseCtx)
			defer store.Close()
			mustUpsertVectorChunks(t, baseCtx, store, "fixture-a", []cache.Chunk{
				vectorTestChunk("chunk-a", "ISSUE-1", "hash-a"),
				vectorTestChunk("chunk-b", "ISSUE-1", "hash-b"),
				vectorTestChunk("chunk-c", "ISSUE-1", "hash-c"),
			})
			provider := mustFakeIndexerProvider(t, 2)
			namespace, err := EnsureEmbeddingNamespace(baseCtx, store, provider, NamespaceRequest{RepoID: "fixture-a", ChunkPolicyID: DefaultChunkPolicyID, LanguagePolicyID: DefaultLanguagePolicyID, DocumentInstructionID: DefaultDocumentInstructionID, QueryInstructionID: DefaultQueryInstructionID})
			if err != nil {
				t.Fatal("namespace fixture failed")
			}
			mustUpsertVectorEmbedding(t, baseCtx, store, namespace.ID, "chunk-a", []float32{1, 0})
			metrics := observability.NewMetrics(strings.Repeat("a", 32), time.Now().UTC())
			ctx, cancel := context.WithCancel(observability.WithMetrics(baseCtx, metrics))
			defer cancel()
			faults := &metricFaultIndexStore{ragIndexStore: store, metrics: metrics, t: t, failWrite: tc.failWrite, failRunAfter: tc.failRun}
			if tc.cancel {
				faults.cancelAfterWrite = cancel
			}
			var selected EmbeddingProvider = provider
			batchSize := 1
			if tc.fallback {
				selected = &badInputProvider{base: provider, badInput: "chunk-b"}
				batchSize = 2
			}
			_, err = NewRAGIndexer(faults, selected, RAGIndexerOptions{}).Run(ctx, IndexRequest{RepoID: "fixture-a", BatchSize: batchSize})
			if (err != nil) != tc.wantError {
				t.Fatal("fault changed index result")
			}
			if faults.releases != 1 || embeddedCounter(t, metrics) != tc.wantFirst {
				t.Fatal("confirmed writes not counted after lease release")
			}
			// The same namespace resumes only missing vectors. Successfully
			// persisted bytes from the failed/partial run are not counted twice.
			faults.failWrite, faults.failRunAfter, faults.cancelAfterWrite = 0, 0, nil
			faults.beforeRelease = tc.wantFirst
			ctx = observability.WithMetrics(baseCtx, metrics)
			if _, err := NewRAGIndexer(faults, provider, RAGIndexerOptions{}).Run(ctx, IndexRequest{RepoID: "fixture-a", BatchSize: 2}); err != nil {
				t.Fatal("resume failed")
			}
			if embeddedCounter(t, metrics) != 2 || faults.releases != 2 {
				t.Fatal("resume duplicated confirmed embeddings")
			}
		})
	}
}
