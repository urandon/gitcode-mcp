package servicectl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/rag"
	"gitcode-mcp/internal/service"
)

const RAGIndexJobType = "rag-index"

type StartRAGIndexJobRequest struct {
	RepoID         string `json:"repo_id"`
	Profile        string `json:"profile,omitempty"`
	CachePath      string `json:"cache_path,omitempty"`
	BatchSize      int    `json:"batch_size,omitempty"`
	ChunkPolicy    string `json:"chunk_policy,omitempty"`
	CacheUUID      string `json:"cache_uuid,omitempty"`
	RegistrationID string `json:"registration_id,omitempty"`
	NamespaceID    string `json:"namespace_id,omitempty"`
}

func (m *JobManager) StartRAGIndex(ctx context.Context, manager Manager, req StartRAGIndexJobRequest) (Job, error) {
	req.RepoID = strings.TrimSpace(req.RepoID)
	if req.RepoID == "" {
		return Job{}, errors.New("repo_id is required")
	}
	workKey := ragIndexWorkKey(req)
	ctx, cancel := context.WithCancel(ctx)
	profile := strings.TrimSpace(req.Profile)
	job, created := m.createCoalescedJob(RAGIndexJobType, req.RepoID, profile, 0, workKey, req.CacheUUID, req.RegistrationID, req.NamespaceID, cancel)
	if !created {
		cancel()
		return job, nil
	}
	go m.runRAGIndexJob(ctx, manager, job.ID, req)
	return job, nil
}

func ragIndexWorkKey(req StartRAGIndexJobRequest) string {
	cacheID := strings.TrimSpace(req.CacheUUID)
	if cacheID == "" {
		cacheID = strings.TrimSpace(req.CachePath)
	}
	return strings.Join([]string{RAGIndexJobType, cacheID, strings.TrimSpace(req.RepoID), strings.TrimSpace(req.Profile), strings.TrimSpace(req.NamespaceID), strings.TrimSpace(req.ChunkPolicy)}, ":")
}

func (m *JobManager) runRAGIndexJob(ctx context.Context, manager Manager, jobID string, req StartRAGIndexJobRequest) {
	m.updateJob(jobID, func(job *Job, now time.Time) {
		job.Status = JobStatusRunning
		job.StartedAt = &now
		job.UpdatedAt = now
	})
	progressCh := make(chan service.ProgressEvent, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range progressCh {
			ev = sanitizeMaintenanceProgress(RAGIndexJobType, ev)
			m.updateJob(jobID, func(job *Job, now time.Time) {
				job.UpdatedAt = now
				job.Progress = append(job.Progress, ev)
				if ev.RecordsListed > 0 {
					job.Steps = ev.RecordsListed
				}
				if ev.RecordsFetched+ev.RecordsSkipped > 0 {
					job.Completed = ev.RecordsFetched + ev.RecordsSkipped
				}
			})
		}
	}()
	result, err := runRAGIndex(ctx, manager, req, progressCh)
	close(progressCh)
	<-done
	if err != nil {
		m.updateJob(jobID, func(job *Job, now time.Time) {
			job.Status = JobStatusFailed
			if result.Status == rag.RAGIndexStatusFailed {
				job.Status = JobStatusFailed
			}
			if result.Status == rag.RAGIndexStatusCancelled {
				job.Status = JobStatusCancelled
			}
			job.UpdatedAt = now
			job.FinishedAt = &now
			job.ErrorClass = maintenanceJobErrorClass(err, "rag_failed")
			if job.Status == JobStatusCancelled {
				job.ErrorClass = "cancelled"
			}
			job.Error = publicMaintenanceJobError(RAGIndexJobType, job.ErrorClass)
			job.Steps = result.TotalChunks
			job.Completed = result.EmbeddedChunks + result.SkippedChunks
			job.NamespaceID = result.NamespaceID
			job.ProfileID = result.ProfileID
			job.Progress = append(job.Progress, service.ProgressEvent{Type: job.Status, Phase: job.Status, Collection: RAGIndexJobType, RecordsListed: result.TotalChunks, RecordsFetched: result.EmbeddedChunks, RecordsSkipped: result.SkippedChunks, RecordsFailed: result.FailedChunks, Message: job.Error})
			delete(m.cancel, jobID)
		})
		return
	}
	m.updateJob(jobID, func(job *Job, now time.Time) {
		job.Status = JobStatusSucceeded
		if result.Status == rag.RAGIndexStatusSuperseded {
			job.Status = JobStatusSuperseded
		}
		job.UpdatedAt = now
		job.FinishedAt = &now
		job.Steps = result.TotalChunks
		job.Completed = result.EmbeddedChunks + result.SkippedChunks
		job.ProfileID = result.ProfileID
		job.NamespaceID = result.NamespaceID
		delete(m.cancel, jobID)
	})
}

func runRAGIndex(ctx context.Context, manager Manager, req StartRAGIndexJobRequest, progressCh chan<- service.ProgressEvent) (rag.IndexResult, error) {
	eff, err := effectiveJobConfig(manager, req.CachePath)
	if err != nil {
		return rag.IndexResult{}, err
	}
	store, err := cache.NewSQLiteStore(ctx, eff.Config.CachePath)
	if err != nil {
		return rag.IndexResult{}, err
	}
	defer store.Close()
	provider, err := rag.NewEmbeddingProviderFromConfig(eff.Config, req.Profile, rag.ProviderOptions{})
	if err != nil {
		return rag.IndexResult{}, err
	}
	profile := provider.Profile()
	batchSize := req.BatchSize
	if batchSize <= 0 {
		batchSize = eff.Config.RAG.Indexing.BatchSize
	}
	if batchSize <= 0 {
		batchSize = profile.BatchSize
	}
	indexer := rag.NewRAGIndexer(store, provider, rag.RAGIndexerOptions{LockPath: eff.Config.LockPath})
	chunkPolicy := strings.TrimSpace(req.ChunkPolicy)
	if chunkPolicy == "" {
		chunkPolicy = rag.DefaultChunkPolicyID
	}
	result, err := indexer.Run(ctx, rag.IndexRequest{
		RepoID:                req.RepoID,
		ProfileID:             profile.ProfileID,
		ChunkPolicyID:         chunkPolicy,
		LanguagePolicyID:      rag.DefaultLanguagePolicyID,
		DocumentInstructionID: rag.DefaultDocumentInstructionID,
		QueryInstructionID:    rag.DefaultQueryInstructionID,
		BatchSize:             batchSize,
		ProgressChan:          progressCh,
	})
	if err != nil {
		return result, fmt.Errorf("rag index job failed: %w", err)
	}
	return result, nil
}
