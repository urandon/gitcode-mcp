package servicectl

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/rag"
	"gitcode-mcp/internal/repositorydocs"
	"gitcode-mcp/internal/service"
)

const RepositoryDocsIndexJobType = "repository-docs-index"

const (
	EnvRepositoryDocsVectorByteCeiling = "GITCODE_MCP_REPO_DOC_VECTOR_BYTES"
	DefaultRepositoryDocsVectorBytes   = int64(512 << 20)
)

// StartRepositoryDocsIndexJobRequest carries the local authority needed by the
// daemon. RepositoryPath and CachePath are accepted only over the local IPC
// boundary and are never copied into the public Job representation.
type StartRepositoryDocsIndexJobRequest struct {
	RepoID          string `json:"repo_id"`
	RepositoryPath  string `json:"repository_path"`
	Revision        string `json:"revision,omitempty"`
	IncludeWorktree bool   `json:"include_worktree,omitempty"`
	Profile         string `json:"profile,omitempty"`
	CachePath       string `json:"cache_path,omitempty"`
	BatchSize       int    `json:"batch_size,omitempty"`
	MaxChunks       int    `json:"max_chunks,omitempty"`
	CacheUUID       string `json:"cache_uuid,omitempty"`
	RegistrationID  string `json:"registration_id,omitempty"`
}

func (m *JobManager) StartRepositoryDocsIndex(ctx context.Context, manager Manager, req StartRepositoryDocsIndexJobRequest) (Job, error) {
	req.RepoID = strings.TrimSpace(req.RepoID)
	req.RepositoryPath = strings.TrimSpace(req.RepositoryPath)
	if req.RepoID == "" || req.RepositoryPath == "" {
		return Job{}, errors.New("repo_id and repository_path are required")
	}
	repositoryPath, err := filepath.Abs(req.RepositoryPath)
	if err != nil {
		return Job{}, fmt.Errorf("repository docs: resolve repository path: %w", err)
	}
	req.RepositoryPath = repositoryPath

	// Resolve aliases before deriving job/cache identities. This prevents a
	// request such as owner/legacy-name from creating a parallel index beside
	// the canonical repository binding.
	eff, err := effectiveJobConfig(manager, req.CachePath)
	if err != nil {
		return Job{}, err
	}
	store, err := cache.NewSQLiteReadOnlyStore(ctx, eff.Config.CachePath)
	if err != nil {
		return Job{}, err
	}
	binding, err := store.ResolveRepositoryBinding(ctx, req.RepoID)
	if err != nil {
		store.Close()
		return Job{}, fmt.Errorf("repository docs: repository binding: %w", err)
	}
	identity, identityErr := store.CacheIdentity(ctx)
	store.Close()
	if identityErr != nil {
		return Job{}, identityErr
	}
	req.RepoID = binding.RepoID
	if req.CacheUUID == "" {
		req.CacheUUID = identity.UUID
	}

	repo, err := repositorydocs.OpenRepository(ctx, repositoryPath)
	if err != nil {
		return Job{}, err
	}
	commitOID, err := repo.ResolveRevision(ctx, req.Revision)
	if err != nil {
		return Job{}, err
	}
	workKey := strings.Join([]string{
		RepositoryDocsIndexJobType, req.CacheUUID, req.RepoID, repo.GitStoreRef,
		commitOID, strconv.FormatBool(req.IncludeWorktree), strings.TrimSpace(req.Profile),
		"max=" + strconv.Itoa(req.MaxChunks),
	}, ":")
	jobCtx, cancel := context.WithCancel(ctx)
	job, created, err := m.createCoalescedJob(RepositoryDocsIndexJobType, req.RepoID, strings.TrimSpace(req.Profile), 0, workKey, req.CacheUUID, req.RegistrationID, "", cancel)
	if err != nil {
		cancel()
		return Job{}, err
	}
	if !created {
		cancel()
		return job, nil
	}
	go m.runRepositoryDocsIndexJob(jobCtx, manager, job.ID, req)
	return job, nil
}

func (m *JobManager) runRepositoryDocsIndexJob(ctx context.Context, manager Manager, jobID string, req StartRepositoryDocsIndexJobRequest) {
	m.updateJob(jobID, func(job *Job, now time.Time) {
		job.Status = JobStatusRunning
		job.StartedAt = &now
		job.UpdatedAt = now
		job.Progress = append(job.Progress, service.ProgressEvent{Type: "started", Phase: "indexing", Collection: RepositoryDocsIndexJobType, Message: "repository documentation indexing started"})
	})
	result, err := runRepositoryDocsIndex(ctx, manager, req)
	if err != nil {
		status := JobStatusFailed
		class := maintenanceJobErrorClass(err, "repository_docs_index_failed")
		if errors.Is(err, context.Canceled) {
			status = JobStatusCancelled
			class = "cancelled"
		}
		m.updateJob(jobID, func(job *Job, now time.Time) {
			job.Status = status
			job.UpdatedAt = now
			job.FinishedAt = &now
			job.ErrorClass = class
			job.Error = publicMaintenanceJobError(RepositoryDocsIndexJobType, class)
			job.Steps = result.EligibleChunks
			job.Completed = result.EmbeddedChunks + result.ReusedChunks
			job.NamespaceID = result.NamespaceID
			job.Progress = append(job.Progress, service.ProgressEvent{Type: status, Phase: status, Collection: RepositoryDocsIndexJobType, RecordsListed: result.EligibleChunks, RecordsFetched: result.EmbeddedChunks, RecordsSkipped: result.ReusedChunks, RecordsFailed: result.FailedChunks, Message: job.Error})
			delete(m.cancel, jobID)
		})
		return
	}
	status := JobStatusSucceeded
	if result.State == cache.RepoDocSetSuperseded {
		status = JobStatusSuperseded
	} else if result.State == cache.RepoDocSetBlocked || result.State == cache.RepoDocSetPartial {
		status = JobStatusFailed
	}
	m.updateJob(jobID, func(job *Job, now time.Time) {
		job.Status = status
		job.UpdatedAt = now
		job.FinishedAt = &now
		job.Steps = result.EligibleChunks
		job.Completed = result.EmbeddedChunks + result.ReusedChunks
		job.NamespaceID = result.NamespaceID
		if result.GCBytesBefore > 0 || result.GCRevisionSets > 0 {
			job.Progress = append(job.Progress, service.ProgressEvent{Type: "gc", Phase: "gc", Collection: RepositoryDocsIndexJobType, RecordsDeleted: int(result.GCRevisionSets), RevisionSetsDeleted: int(result.GCRevisionSets), ChunksDeleted: int(result.GCChunks), VectorsDeleted: int(result.GCVectors), BytesBefore: result.GCBytesBefore, BytesAfter: result.GCBytesAfter, Message: "repository documentation derived-state retention reconciled"})
		}
		job.Progress = append(job.Progress, service.ProgressEvent{Type: status, Phase: status, Collection: RepositoryDocsIndexJobType, RecordsListed: result.EligibleChunks, RecordsFetched: result.EmbeddedChunks, RecordsSkipped: result.ReusedChunks, RecordsFailed: result.FailedChunks, Message: "repository documentation revision set published"})
		delete(m.cancel, jobID)
	})
}

func runRepositoryDocsIndex(ctx context.Context, manager Manager, req StartRepositoryDocsIndexJobRequest) (repositorydocs.IndexResult, error) {
	eff, err := effectiveJobConfig(manager, req.CachePath)
	if err != nil {
		return repositorydocs.IndexResult{}, err
	}
	store, err := cache.NewSQLiteStore(ctx, eff.Config.CachePath)
	if err != nil {
		return repositorydocs.IndexResult{}, err
	}
	defer store.Close()
	provider, err := rag.NewEmbeddingProviderFromConfig(eff.Config, req.Profile, rag.ProviderOptions{})
	if err != nil {
		return repositorydocs.IndexResult{}, err
	}
	repo, err := repositorydocs.OpenRepository(ctx, req.RepositoryPath)
	if err != nil {
		return repositorydocs.IndexResult{}, err
	}
	result, err := repositorydocs.NewIndexer(store, provider).Run(ctx, repositorydocs.IndexRequest{
		RepoID: req.RepoID, Repository: repo, Revision: req.Revision,
		IncludeWorktree: req.IncludeWorktree, BatchSize: req.BatchSize, MaxChunks: req.MaxChunks,
	})
	if err != nil {
		return result, err
	}
	vectorByteCeiling, err := repositoryDocsVectorByteCeiling(manager)
	if err != nil {
		return result, err
	}
	now := time.Now().UTC()
	gcResult, gcErr := store.PruneRepositoryDocRevisionSets(ctx, req.RepoID, cache.RepositoryDocRetentionPolicy{
		RetainCommittedPerIdentity: 8,
		RetainOverlaysPerIdentity:  0,
		CommittedCutoff:            now.Add(-30 * 24 * time.Hour),
		OverlayCutoff:              now.Add(-24 * time.Hour),
		TerminalCutoff:             now.Add(-7 * 24 * time.Hour),
		MaxVectorBytes:             vectorByteCeiling,
		ProtectedSetIDs:            []string{result.RevisionSetID},
	})
	if gcErr != nil {
		return result, fmt.Errorf("repository docs: metadata retention: %w", gcErr)
	}
	result.GCRevisionSets = gcResult.RevisionSetsDeleted
	result.GCChunks = gcResult.ChunksDeleted
	result.GCVectors = gcResult.VectorsDeleted
	result.GCBytesBefore = gcResult.VectorBytesBefore
	result.GCBytesAfter = gcResult.VectorBytesAfter
	return result, nil
}

func repositoryDocsVectorByteCeiling(manager Manager) (int64, error) {
	src := manager.Source
	if src == nil {
		src = config.OSSource{}
	}
	raw := strings.TrimSpace(src.Env(EnvRepositoryDocsVectorByteCeiling))
	if raw == "" {
		return DefaultRepositoryDocsVectorBytes, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("repository docs: %s must be a positive byte count", EnvRepositoryDocsVectorByteCeiling)
	}
	return value, nil
}
