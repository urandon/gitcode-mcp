package rag

import (
	"context"
	"fmt"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/progress"
)

type StatusRequest struct {
	RepoID                string
	ProfileID             string
	ChunkPolicyID         string
	LanguagePolicyID      string
	DocumentInstructionID string
	QueryInstructionID    string
	ActiveJob             *JobStatus
	Service               *ServiceStatus
}

type StatusResult struct {
	RepoID       string          `json:"repo_id"`
	Status       string          `json:"status"`
	Provider     ProviderStatus  `json:"provider"`
	Namespace    NamespaceStatus `json:"namespace"`
	Coverage     CoverageStatus  `json:"coverage"`
	ActiveJob    *JobStatus      `json:"active_job,omitempty"`
	LastRun      *RunStatus      `json:"last_run,omitempty"`
	Service      *ServiceStatus  `json:"service,omitempty"`
	FailureClass string          `json:"failure_class,omitempty"`
	Message      string          `json:"message,omitempty"`
	GeneratedAt  time.Time       `json:"generated_at"`
}

type ProviderStatus struct {
	ProfileID    string `json:"profile_id,omitempty"`
	ProviderID   string `json:"provider_id,omitempty"`
	ProviderType string `json:"provider_type,omitempty"`
	Endpoint     string `json:"endpoint,omitempty"`
	Model        string `json:"model,omitempty"`
	Revision     string `json:"revision,omitempty"`
	Dimensions   int    `json:"dimensions,omitempty"`
	BatchSize    int    `json:"batch_size,omitempty"`
	Ready        bool   `json:"ready"`
	ErrorClass   string `json:"error_class,omitempty"`
	Message      string `json:"message,omitempty"`
}

type NamespaceStatus struct {
	ID      string `json:"id,omitempty"`
	Exists  bool   `json:"exists"`
	Current bool   `json:"current"`
}

type CoverageStatus struct {
	TotalChunks       int   `json:"total_chunks"`
	EmbeddedChunks    int   `json:"embedded_chunks"`
	MissingChunks     int   `json:"missing_chunks"`
	StaleChunks       int   `json:"stale_chunks"`
	FailedChunks      int   `json:"failed_chunks"`
	SkippedChunks     int   `json:"skipped_chunks"`
	ContentGeneration int64 `json:"content_generation,omitempty"`
	CoveredGeneration int64 `json:"covered_generation,omitempty"`
	GenerationTracked bool  `json:"generation_tracked,omitempty"`
}

type JobStatus struct {
	ID        string           `json:"id"`
	Type      string           `json:"type,omitempty"`
	RepoID    string           `json:"repo_id,omitempty"`
	ProfileID string           `json:"profile_id,omitempty"`
	Status    string           `json:"status"`
	Steps     int              `json:"steps,omitempty"`
	Completed int              `json:"completed,omitempty"`
	Error     string           `json:"error,omitempty"`
	Progress  []progress.Event `json:"progress,omitempty"`
}

type ServiceStatus struct {
	Status        string `json:"status"`
	Running       bool   `json:"running"`
	PID           int    `json:"pid,omitempty"`
	SocketPresent bool   `json:"socket_present"`
	SocketPath    string `json:"socket_path,omitempty"`
	Message       string `json:"message,omitempty"`
}

type RunStatus struct {
	ID             string    `json:"id"`
	NamespaceID    string    `json:"namespace_id"`
	ProfileID      string    `json:"profile_id"`
	Status         string    `json:"status"`
	TotalChunks    int       `json:"total_chunks"`
	EmbeddedChunks int       `json:"embedded_chunks"`
	SkippedChunks  int       `json:"skipped_chunks"`
	FailedChunks   int       `json:"failed_chunks"`
	StartedAt      time.Time `json:"started_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	CompletedAt    time.Time `json:"completed_at,omitempty"`
	ErrorClass     string    `json:"error_class,omitempty"`
	Message        string    `json:"message,omitempty"`
}

type statusStore interface {
	ResolveEmbeddingNamespace(context.Context, cache.EmbeddingNamespaceIdentity) (cache.EmbeddingNamespace, bool, error)
	ListChunks(context.Context, cache.ChunkFilter) ([]cache.Chunk, error)
	ListChunkEmbeddings(context.Context, cache.ChunkEmbeddingFilter) ([]cache.ChunkEmbedding, error)
	ListRAGIndexRuns(context.Context, cache.RAGIndexRunFilter) ([]cache.RAGIndexRun, error)
	GetRepoContentState(context.Context, string) (cache.RepoContentState, error)
	GetRAGCoverageState(context.Context, string, string) (cache.RAGCoverageState, bool, error)
}

func Status(ctx context.Context, store statusStore, provider EmbeddingProvider, req StatusRequest) (StatusResult, error) {
	if store == nil {
		return StatusResult{}, fmt.Errorf("rag status: cache store is required")
	}
	if provider == nil {
		return StatusResult{}, fmt.Errorf("rag status: embedding provider is required")
	}
	if req.RepoID == "" {
		return StatusResult{}, fmt.Errorf("rag status: repo id is required")
	}
	profile := provider.Profile()
	result := StatusResult{
		RepoID:      req.RepoID,
		Status:      "unknown",
		GeneratedAt: time.Now().UTC(),
		Provider: ProviderStatus{
			ProfileID:    firstNonEmpty(req.ProfileID, profile.ProfileID),
			ProviderID:   profile.ProviderID,
			ProviderType: profile.ProviderType,
			Endpoint:     profile.Endpoint,
			Model:        profile.Model,
			Dimensions:   profile.Dimensions,
			BatchSize:    profile.BatchSize,
		},
		ActiveJob: req.ActiveJob,
		Service:   req.Service,
	}
	req.ChunkPolicyID = firstNonEmpty(req.ChunkPolicyID, DefaultChunkPolicyID)
	req.LanguagePolicyID = firstNonEmpty(req.LanguagePolicyID, DefaultLanguagePolicyID)
	req.DocumentInstructionID = firstNonEmpty(req.DocumentInstructionID, DefaultDocumentInstructionID)
	req.QueryInstructionID = firstNonEmpty(req.QueryInstructionID, DefaultQueryInstructionID)
	info, providerErr := provider.ModelInfo(ctx)
	if providerErr != nil {
		result.Provider.Ready = false
		result.Provider.ErrorClass = providerFailureClass(providerErr)
		result.Provider.Message = providerErr.Error()
	} else {
		result.Provider.Ready = true
		result.Provider.Model = info.Model
		result.Provider.Revision = info.Revision
	}
	identity, err := provider.NamespaceIdentity(ctx, NamespaceRequest{RepoID: req.RepoID, ChunkPolicyID: req.ChunkPolicyID, LanguagePolicyID: req.LanguagePolicyID, DocumentInstructionID: req.DocumentInstructionID, QueryInstructionID: req.QueryInstructionID})
	if err != nil {
		result.Status = "provider_not_ready"
		result.FailureClass = providerFailureClass(err)
		result.Message = err.Error()
		return result, nil
	}
	namespace, ok, err := store.ResolveEmbeddingNamespace(ctx, identity)
	if err != nil {
		return StatusResult{}, err
	}
	result.Namespace.Exists = ok
	result.Namespace.Current = ok
	if ok {
		result.Namespace.ID = namespace.ID
	}
	contentState, err := store.GetRepoContentState(ctx, req.RepoID)
	if err != nil {
		return StatusResult{}, err
	}
	result.Coverage.ContentGeneration = contentState.ContentGeneration
	if ok {
		coverageState, exists, err := store.GetRAGCoverageState(ctx, req.RepoID, namespace.ID)
		if err != nil {
			return StatusResult{}, err
		}
		if exists {
			result.Coverage.CoveredGeneration = coverageState.CoveredGeneration
			result.Coverage.GenerationTracked = true
		}
	}
	chunks, err := store.ListChunks(ctx, cache.ChunkFilter{RepoID: req.RepoID, Policy: req.ChunkPolicyID})
	if err != nil {
		return StatusResult{}, err
	}
	result.Coverage.TotalChunks = len(chunks)
	currentHashes := make(map[string]string, len(chunks))
	for _, chunk := range chunks {
		currentHashes[chunk.ID] = chunk.ContentHash
	}
	if ok {
		embeddings, err := store.ListChunkEmbeddings(ctx, cache.ChunkEmbeddingFilter{RepoID: req.RepoID, NamespaceID: namespace.ID})
		if err != nil {
			return StatusResult{}, err
		}
		seen := map[string]bool{}
		for _, embedding := range embeddings {
			hash, exists := currentHashes[embedding.ChunkID]
			if !exists {
				result.Coverage.StaleChunks++
				continue
			}
			if embedding.ChunkContentHash == hash && embedding.Dimensions == namespace.Dimensions && embedding.DType == namespace.DType {
				result.Coverage.EmbeddedChunks++
				seen[embedding.ChunkID] = true
				continue
			}
			result.Coverage.StaleChunks++
		}
		result.Coverage.MissingChunks = result.Coverage.TotalChunks - len(seen)
	} else {
		result.Coverage.MissingChunks = result.Coverage.TotalChunks
	}
	runs, err := store.ListRAGIndexRuns(ctx, cache.RAGIndexRunFilter{RepoID: req.RepoID, Limit: 1})
	if err != nil {
		return StatusResult{}, err
	}
	if len(runs) > 0 {
		run := runStatusFromCache(runs[0])
		result.LastRun = &run
		result.Coverage.FailedChunks = run.FailedChunks
		result.Coverage.SkippedChunks = run.SkippedChunks
	}
	result.Status = deriveStatus(result)
	if providerErr != nil && result.Status == "ready" {
		result.Status = "provider_not_ready"
	}
	return result, nil
}

func runStatusFromCache(run cache.RAGIndexRun) RunStatus {
	return RunStatus{ID: run.ID, NamespaceID: run.NamespaceID, ProfileID: run.ProfileID, Status: run.Status, TotalChunks: run.TotalChunks, EmbeddedChunks: run.EmbeddedChunks, SkippedChunks: run.SkippedChunks, FailedChunks: run.FailedChunks, StartedAt: run.StartedAt, UpdatedAt: run.UpdatedAt, CompletedAt: run.CompletedAt, ErrorClass: run.ErrorClass, Message: run.Message}
}

func deriveStatus(result StatusResult) string {
	if result.ActiveJob != nil && (result.ActiveJob.Status == RAGIndexStatusQueued || result.ActiveJob.Status == RAGIndexStatusRunning || result.ActiveJob.Status == "queued" || result.ActiveJob.Status == "running") {
		return "indexing"
	}
	if !result.Provider.Ready {
		return "provider_not_ready"
	}
	if !result.Namespace.Exists {
		if result.Coverage.TotalChunks == 0 {
			return "empty"
		}
		return "no_namespace"
	}
	if result.Coverage.TotalChunks == 0 {
		return "empty"
	}
	if result.LastRun != nil && result.LastRun.Status == RAGIndexStatusFailed {
		return "failed"
	}
	if result.Coverage.MissingChunks > 0 || result.Coverage.StaleChunks > 0 || result.Coverage.FailedChunks > 0 || (result.Coverage.GenerationTracked && result.Coverage.CoveredGeneration < result.Coverage.ContentGeneration) {
		return "partial"
	}
	return "ready"
}
