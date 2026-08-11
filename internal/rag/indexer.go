package rag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/service"
)

const (
	RAGIndexStatusQueued      = "queued"
	RAGIndexStatusRunning     = "running"
	RAGIndexStatusSucceeded   = "succeeded"
	RAGIndexStatusFailed      = "failed"
	RAGIndexStatusCancelled   = "canceled"
	RAGIndexStatusInterrupted = "interrupted"
)

type IndexRequest struct {
	RepoID                string
	ProfileID             string
	ChunkPolicyID         string
	LanguagePolicyID      string
	DocumentInstructionID string
	QueryInstructionID    string
	BatchSize             int
	RunID                 string
	ProgressChan          chan<- service.ProgressEvent
}

type IndexResult struct {
	RepoID            string                  `json:"repo_id"`
	RunID             string                  `json:"run_id"`
	NamespaceID       string                  `json:"namespace_id"`
	ProfileID         string                  `json:"profile_id"`
	Status            string                  `json:"status"`
	TotalChunks       int                     `json:"total_chunks"`
	EmbeddedChunks    int                     `json:"embedded_chunks"`
	SkippedChunks     int                     `json:"skipped_chunks"`
	FailedChunks      int                     `json:"failed_chunks"`
	StartGeneration   int64                   `json:"start_generation,omitempty"`
	CoveredGeneration int64                   `json:"covered_generation,omitempty"`
	StartedAt         time.Time               `json:"started_at"`
	CompletedAt       time.Time               `json:"completed_at,omitempty"`
	ErrorClass        string                  `json:"error_class,omitempty"`
	Message           string                  `json:"message,omitempty"`
	Progress          []service.ProgressEvent `json:"progress,omitempty"`
}

type RAGIndexer struct {
	store    ragIndexStore
	provider EmbeddingProvider
	now      func() time.Time
	lockPath string
}

type RAGIndexerOptions struct {
	Now      func() time.Time
	LockPath string
}

type ragIndexStore interface {
	ResolveEmbeddingNamespace(context.Context, cache.EmbeddingNamespaceIdentity) (cache.EmbeddingNamespace, bool, error)
	UpsertEmbeddingNamespace(context.Context, cache.EmbeddingNamespace) (cache.EmbeddingNamespace, error)
	ListChunks(context.Context, cache.ChunkFilter) ([]cache.Chunk, error)
	ListChunkEmbeddings(context.Context, cache.ChunkEmbeddingFilter) ([]cache.ChunkEmbedding, error)
	UpsertChunkEmbedding(context.Context, cache.ChunkEmbedding) error
	UpsertRAGIndexRun(context.Context, cache.RAGIndexRun) error
	AcquireWriter(context.Context, cache.WriterRequest) (*cache.WriterLease, error)
	ReleaseWriter(context.Context, *cache.WriterLease) error
	GetRepoContentState(context.Context, string) (cache.RepoContentState, error)
	UpsertRAGCoverageState(context.Context, cache.RAGCoverageState) error
}

func NewRAGIndexer(store ragIndexStore, provider EmbeddingProvider, opts RAGIndexerOptions) *RAGIndexer {
	now := opts.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &RAGIndexer{store: store, provider: provider, now: now, lockPath: opts.LockPath}
}

func (i *RAGIndexer) Run(ctx context.Context, req IndexRequest) (IndexResult, error) {
	if i == nil || i.store == nil {
		return IndexResult{}, fmt.Errorf("rag indexer: cache store is required")
	}
	if i.provider == nil {
		return IndexResult{}, fmt.Errorf("rag indexer: embedding provider is required")
	}
	if req.RepoID == "" {
		return IndexResult{}, fmt.Errorf("rag indexer: repo id is required")
	}
	profile := i.provider.Profile()
	if req.ProfileID == "" {
		req.ProfileID = profile.ProfileID
	}
	if req.BatchSize <= 0 {
		req.BatchSize = profile.BatchSize
	}
	if req.BatchSize <= 0 {
		req.BatchSize = 16
	}
	req.ChunkPolicyID = firstNonEmpty(req.ChunkPolicyID, DefaultChunkPolicyID)
	req.LanguagePolicyID = firstNonEmpty(req.LanguagePolicyID, DefaultLanguagePolicyID)
	req.DocumentInstructionID = firstNonEmpty(req.DocumentInstructionID, DefaultDocumentInstructionID)
	req.QueryInstructionID = firstNonEmpty(req.QueryInstructionID, DefaultQueryInstructionID)

	namespace, err := EnsureEmbeddingNamespace(ctx, i.store, i.provider, NamespaceRequest{
		RepoID:                req.RepoID,
		ChunkPolicyID:         req.ChunkPolicyID,
		LanguagePolicyID:      req.LanguagePolicyID,
		DocumentInstructionID: req.DocumentInstructionID,
		QueryInstructionID:    req.QueryInstructionID,
	})
	if err != nil {
		return IndexResult{}, err
	}
	runID := firstNonEmpty(req.RunID, deterministicIndexRunID(req.RepoID, namespace.ID, req.ProfileID))
	started := i.now().UTC()
	run := cache.RAGIndexRun{RepoID: req.RepoID, ID: runID, NamespaceID: namespace.ID, ProfileID: req.ProfileID, Status: RAGIndexStatusQueued, StartedAt: started, UpdatedAt: started, Metadata: map[string]string{"provider": profile.ProviderID, "model": profile.Model}}
	if err := i.store.UpsertRAGIndexRun(ctx, run); err != nil {
		return IndexResult{}, err
	}
	progress := []service.ProgressEvent{}
	emit := func(ev service.ProgressEvent) {
		progress = append(progress, ev)
		emitProgress(req.ProgressChan, ev)
	}
	emit(service.ProgressEvent{Type: "queued", Phase: RAGIndexStatusQueued, Collection: "rag_index", Message: "rag index queued"})
	lease, err := i.store.AcquireWriter(ctx, cache.WriterRequest{Operation: "rag-index", RepoID: req.RepoID, LockPath: i.lockPath})
	if err != nil {
		return i.failRun(ctx, run, progress, "lease_failed", err)
	}
	defer i.store.ReleaseWriter(context.Background(), lease)
	contentState, err := i.store.GetRepoContentState(ctx, req.RepoID)
	if err != nil {
		return i.failRun(ctx, run, progress, "content_state_failed", err)
	}
	startGeneration := contentState.ContentGeneration
	run.Metadata["start_generation"] = fmt.Sprintf("%d", startGeneration)

	chunks, err := i.store.ListChunks(ctx, cache.ChunkFilter{RepoID: req.RepoID, Policy: req.ChunkPolicyID})
	if err != nil {
		return i.failRun(ctx, run, progress, "list_chunks_failed", err)
	}
	sort.SliceStable(chunks, func(a, b int) bool {
		if chunks[a].SourceID != chunks[b].SourceID {
			return chunks[a].SourceID < chunks[b].SourceID
		}
		if chunks[a].ByteStart != chunks[b].ByteStart {
			return chunks[a].ByteStart < chunks[b].ByteStart
		}
		return chunks[a].ID < chunks[b].ID
	})
	existing, err := i.store.ListChunkEmbeddings(ctx, cache.ChunkEmbeddingFilter{RepoID: req.RepoID, NamespaceID: namespace.ID})
	if err != nil {
		return i.failRun(ctx, run, progress, "list_embeddings_failed", err)
	}
	fresh := map[string]cache.ChunkEmbedding{}
	for _, embedding := range existing {
		fresh[embedding.ChunkID] = embedding
	}
	missing := make([]cache.Chunk, 0, len(chunks))
	skipped := 0
	for _, chunk := range chunks {
		embedding, ok := fresh[chunk.ID]
		if ok && embedding.ChunkContentHash == chunk.ContentHash && embedding.Dimensions == namespace.Dimensions && embedding.DType == namespace.DType {
			skipped++
			continue
		}
		missing = append(missing, chunk)
	}
	run.Status = RAGIndexStatusRunning
	run.TotalChunks = len(chunks)
	run.SkippedChunks = skipped
	run.UpdatedAt = i.now().UTC()
	if err := i.store.UpsertRAGIndexRun(ctx, run); err != nil {
		return i.failRun(ctx, run, progress, "run_update_failed", err)
	}
	emit(service.ProgressEvent{Type: "started", Phase: RAGIndexStatusRunning, Collection: "rag_index", RecordsListed: len(chunks), RecordsSkipped: skipped, Message: "rag index started"})
	writeEmbedding := func(chunk cache.Chunk, vector []float32) error {
		blob, err := EncodeNormalizedFloat32Vector(vector)
		if err != nil {
			return err
		}
		return i.store.UpsertChunkEmbedding(ctx, cache.ChunkEmbedding{
			RepoID:           req.RepoID,
			NamespaceID:      namespace.ID,
			ChunkID:          chunk.ID,
			SourceID:         chunk.SourceID,
			RecordID:         chunk.RecordID,
			SnapshotID:       chunk.SnapshotID,
			ChunkContentHash: chunk.ContentHash,
			Vector:           blob,
			Dimensions:       len(vector),
			DType:            DefaultEmbeddingDType,
			EmbeddedAt:       i.now().UTC(),
		})
	}
	emitRecords := func(message string) {
		emit(service.ProgressEvent{Type: "records", Phase: RAGIndexStatusRunning, Collection: "rag_index", RecordsListed: run.TotalChunks, RecordsFetched: run.EmbeddedChunks, RecordsSkipped: run.SkippedChunks, RecordsFailed: run.FailedChunks, Message: message})
	}
	for start := 0; start < len(missing); start += req.BatchSize {
		if err := ctx.Err(); err != nil {
			return i.cancelRun(ctx, run, progress, err)
		}
		end := start + req.BatchSize
		if end > len(missing) {
			end = len(missing)
		}
		batch := missing[start:end]
		inputs := make([]string, 0, len(batch))
		for _, chunk := range batch {
			inputs = append(inputs, firstNonEmpty(chunk.NormalizedText, chunk.Text))
		}
		response, err := i.provider.Embed(ctx, EmbedRequest{Inputs: inputs})
		if err != nil {
			if len(batch) == 1 {
				run.FailedChunks++
				run.UpdatedAt = i.now().UTC()
				if run.EmbeddedChunks+run.SkippedChunks == 0 {
					return i.failRun(ctx, run, progress, providerFailureClass(err), err)
				}
				if updateErr := i.store.UpsertRAGIndexRun(ctx, run); updateErr != nil {
					return i.failRun(ctx, run, progress, "run_update_failed", updateErr)
				}
				emitRecords(fmt.Sprintf("failed chunk %s: %s", batch[0].ID, err.Error()))
				continue
			}
			for _, chunk := range batch {
				if err := ctx.Err(); err != nil {
					return i.cancelRun(ctx, run, progress, err)
				}
				response, err := i.provider.Embed(ctx, EmbedRequest{Inputs: []string{firstNonEmpty(chunk.NormalizedText, chunk.Text)}})
				if err != nil {
					run.FailedChunks++
					emitRecords(fmt.Sprintf("failed chunk %s: %s", chunk.ID, err.Error()))
					continue
				}
				if len(response.Embeddings) != 1 {
					run.FailedChunks++
					emitRecords(fmt.Sprintf("failed chunk %s: embedding count = %d, want 1", chunk.ID, len(response.Embeddings)))
					continue
				}
				if err := writeEmbedding(chunk, response.Embeddings[0]); err != nil {
					run.FailedChunks++
					emitRecords(fmt.Sprintf("failed chunk %s: %s", chunk.ID, err.Error()))
					continue
				}
				run.EmbeddedChunks++
			}
			run.UpdatedAt = i.now().UTC()
			if updateErr := i.store.UpsertRAGIndexRun(ctx, run); updateErr != nil {
				return i.failRun(ctx, run, progress, "run_update_failed", updateErr)
			}
			emitRecords(fmt.Sprintf("embedded %d/%d chunks", run.EmbeddedChunks, len(missing)))
			continue
		}
		if len(response.Embeddings) != len(batch) {
			run.FailedChunks += len(batch)
			return i.failRun(ctx, run, progress, ProviderFailureUnsupportedResponse, fmt.Errorf("embedding count = %d, want %d", len(response.Embeddings), len(batch)))
		}
		for idx, vector := range response.Embeddings {
			blob, err := EncodeNormalizedFloat32Vector(vector)
			if err != nil {
				run.FailedChunks++
				return i.failRun(ctx, run, progress, ProviderFailureUnsupportedResponse, err)
			}
			chunk := batch[idx]
			if err := i.store.UpsertChunkEmbedding(ctx, cache.ChunkEmbedding{
				RepoID:           req.RepoID,
				NamespaceID:      namespace.ID,
				ChunkID:          chunk.ID,
				SourceID:         chunk.SourceID,
				RecordID:         chunk.RecordID,
				SnapshotID:       chunk.SnapshotID,
				ChunkContentHash: chunk.ContentHash,
				Vector:           blob,
				Dimensions:       len(vector),
				DType:            DefaultEmbeddingDType,
				EmbeddedAt:       i.now().UTC(),
			}); err != nil {
				run.FailedChunks++
				return i.failRun(ctx, run, progress, "write_embedding_failed", err)
			}
			run.EmbeddedChunks++
		}
		run.UpdatedAt = i.now().UTC()
		if err := i.store.UpsertRAGIndexRun(ctx, run); err != nil {
			return i.failRun(ctx, run, progress, "run_update_failed", err)
		}
		emitRecords(fmt.Sprintf("embedded %d/%d chunks", run.EmbeddedChunks, len(missing)))
	}
	completed := i.now().UTC()
	run.Status = RAGIndexStatusSucceeded
	if run.FailedChunks > 0 {
		run.Message = fmt.Sprintf("rag index finished with %d failed chunks", run.FailedChunks)
	}
	run.UpdatedAt = completed
	run.CompletedAt = completed
	coverageStatus := "ready"
	if run.FailedChunks > 0 {
		coverageStatus = "partial"
	}
	if err := i.store.UpsertRAGCoverageState(ctx, cache.RAGCoverageState{RepoID: req.RepoID, NamespaceID: namespace.ID, CoveredGeneration: startGeneration, Status: coverageStatus, UpdatedAt: completed}); err != nil {
		return i.failRun(ctx, run, progress, "coverage_state_failed", err)
	}
	if err := i.store.UpsertRAGIndexRun(ctx, run); err != nil {
		return i.failRun(ctx, run, progress, "run_update_failed", err)
	}
	emit(service.ProgressEvent{Type: "finished", Phase: RAGIndexStatusSucceeded, Collection: "rag_index", RecordsListed: run.TotalChunks, RecordsFetched: run.EmbeddedChunks, RecordsSkipped: run.SkippedChunks, RecordsFailed: run.FailedChunks, Message: firstNonEmpty(run.Message, "rag index finished")})
	result := indexResultFromRun(run, progress)
	result.StartGeneration = startGeneration
	result.CoveredGeneration = startGeneration
	return result, nil
}

func (i *RAGIndexer) failRun(ctx context.Context, run cache.RAGIndexRun, progress []service.ProgressEvent, class string, err error) (IndexResult, error) {
	now := i.now().UTC()
	run.Status = RAGIndexStatusFailed
	run.ErrorClass = class
	run.Message = err.Error()
	run.UpdatedAt = now
	run.CompletedAt = now
	_ = i.store.UpsertRAGIndexRun(ctx, run)
	progress = append(progress, service.ProgressEvent{Type: "failed", Phase: RAGIndexStatusFailed, Collection: "rag_index", RecordsListed: run.TotalChunks, RecordsFetched: run.EmbeddedChunks, RecordsSkipped: run.SkippedChunks, RecordsFailed: run.FailedChunks, Message: err.Error()})
	return indexResultFromRun(run, progress), err
}

func (i *RAGIndexer) cancelRun(ctx context.Context, run cache.RAGIndexRun, progress []service.ProgressEvent, err error) (IndexResult, error) {
	now := i.now().UTC()
	run.Status = RAGIndexStatusCancelled
	run.ErrorClass = "cancelled"
	run.Message = err.Error()
	run.UpdatedAt = now
	run.CompletedAt = now
	_ = i.store.UpsertRAGIndexRun(ctx, run)
	progress = append(progress, service.ProgressEvent{Type: "cancelled", Phase: RAGIndexStatusCancelled, Collection: "rag_index", RecordsListed: run.TotalChunks, RecordsFetched: run.EmbeddedChunks, RecordsSkipped: run.SkippedChunks, RecordsFailed: run.FailedChunks, Message: err.Error()})
	return indexResultFromRun(run, progress), err
}

func indexResultFromRun(run cache.RAGIndexRun, progress []service.ProgressEvent) IndexResult {
	return IndexResult{RepoID: run.RepoID, RunID: run.ID, NamespaceID: run.NamespaceID, ProfileID: run.ProfileID, Status: run.Status, TotalChunks: run.TotalChunks, EmbeddedChunks: run.EmbeddedChunks, SkippedChunks: run.SkippedChunks, FailedChunks: run.FailedChunks, StartedAt: run.StartedAt, CompletedAt: run.CompletedAt, ErrorClass: run.ErrorClass, Message: run.Message, Progress: append([]service.ProgressEvent(nil), progress...)}
}

func deterministicIndexRunID(repoID, namespaceID, profileID string) string {
	sum := sha256.Sum256([]byte(repoID + "\x00" + namespaceID + "\x00" + profileID))
	return "rag-run-" + hex.EncodeToString(sum[:8])
}

func providerFailureClass(err error) string {
	if coded, ok := err.(interface{ DiagnosticCode() string }); ok {
		return coded.DiagnosticCode()
	}
	return "provider_error"
}

func emitProgress(ch chan<- service.ProgressEvent, ev service.ProgressEvent) {
	if ch == nil {
		return
	}
	select {
	case ch <- ev:
	default:
	}
}
