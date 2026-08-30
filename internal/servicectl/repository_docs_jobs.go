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

type RepositoryDocsProviderBoundaryError struct {
	ProviderID string
	Boundary   string
}

func (e RepositoryDocsProviderBoundaryError) Error() string {
	return "repository docs: embedding repository content requires a local_process or local_network provider boundary"
}

func (e RepositoryDocsProviderBoundaryError) DiagnosticCode() string {
	return "repository_docs_provider_boundary_blocked"
}

// StartRepositoryDocsIndexJobRequest carries the local authority needed by the
// daemon. RepositoryPath and CachePath are accepted only over the local IPC
// boundary and are never copied into the public Job representation.
type StartRepositoryDocsIndexJobRequest struct {
	RepoID                        string `json:"repo_id"`
	RepositoryPath                string `json:"repository_path"`
	Revision                      string `json:"revision,omitempty"`
	IncludeWorktree               bool   `json:"include_worktree,omitempty"`
	Profile                       string `json:"profile,omitempty"`
	CachePath                     string `json:"cache_path,omitempty"`
	BatchSize                     int    `json:"batch_size,omitempty"`
	MaxChunks                     int    `json:"max_chunks,omitempty"`
	CacheUUID                     string `json:"cache_uuid,omitempty"`
	RegistrationID                string `json:"registration_id,omitempty"`
	SourceRegistrationID          string `json:"source_registration_id,omitempty"`
	SourceRegistrationGeneration  int64  `json:"source_registration_generation,omitempty"`
	expectedCommitOID             string
	expectedPolicyHash            string
	expectedConfigDigest          string
	expectedOverlayDigest         string
	expectedNamespaceID           string
	recoveryExpectedRevisionSetID string
	recoveryExpectedWorkKey       string
	recoveryJobID                 string
}

type RepositoryDocsAdmissionStaleError struct{}

func (RepositoryDocsAdmissionStaleError) Error() string {
	return "repository docs: durable admission no longer matches the registered Git snapshot"
}

func (RepositoryDocsAdmissionStaleError) DiagnosticCode() string {
	return "repository_docs_admission_stale"
}

func (m *JobManager) StartRepositoryDocsIndex(ctx context.Context, manager Manager, req StartRepositoryDocsIndexJobRequest) (Job, error) {
	prepared, err := prepareRepositoryDocsIndex(ctx, manager, req)
	if err != nil {
		return Job{}, err
	}
	return m.startPreparedRepositoryDocsIndex(ctx, manager, prepared)
}

type preparedRepositoryDocsIndex struct {
	request     StartRepositoryDocsIndexJobRequest
	repository  *repositorydocs.Repository
	policy      repositorydocs.PolicyResult
	namespaceID string
}

func prepareRepositoryDocsIndex(ctx context.Context, manager Manager, req StartRepositoryDocsIndexJobRequest) (preparedRepositoryDocsIndex, error) {
	req.RepoID = strings.TrimSpace(req.RepoID)
	req.RepositoryPath = strings.TrimSpace(req.RepositoryPath)
	if req.RepoID == "" || req.RepositoryPath == "" {
		return preparedRepositoryDocsIndex{}, errors.New("repo_id and repository_path are required")
	}
	repositoryPath, err := filepath.Abs(req.RepositoryPath)
	if err != nil {
		return preparedRepositoryDocsIndex{}, RepositoryDocsSourceUnavailableError{}
	}
	req.RepositoryPath = repositoryPath

	// Resolve aliases before deriving job/cache identities. This prevents a
	// request such as owner/legacy-name from creating a parallel index beside
	// the canonical repository binding.
	eff, err := effectiveJobConfig(manager, req.CachePath)
	if err != nil {
		return preparedRepositoryDocsIndex{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_configuration_unavailable"}
	}
	effectiveProfile, _, _, err := requireLocalRepositoryDocsProvider(eff.Config, req.Profile)
	if err != nil {
		return preparedRepositoryDocsIndex{}, err
	}
	req.Profile = effectiveProfile
	store, err := cache.NewSQLiteReadOnlyStore(ctx, eff.Config.CachePath)
	if err != nil {
		return preparedRepositoryDocsIndex{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_cache_unavailable"}
	}
	binding, err := store.ResolveRepositoryBinding(ctx, req.RepoID)
	if err != nil {
		store.Close()
		return preparedRepositoryDocsIndex{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_binding_unavailable"}
	}
	identity, identityErr := store.CacheIdentity(ctx)
	store.Close()
	if identityErr != nil {
		return preparedRepositoryDocsIndex{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_cache_unavailable"}
	}
	req.RepoID = binding.RepoID
	if req.CacheUUID == "" {
		req.CacheUUID = identity.UUID
	} else if req.CacheUUID != identity.UUID {
		return preparedRepositoryDocsIndex{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_cache_identity_conflict"}
	}

	repo, err := repositorydocs.OpenRepository(ctx, repositoryPath)
	if err != nil {
		return preparedRepositoryDocsIndex{}, RepositoryDocsSourceUnavailableError{}
	}
	policy, err := repositorydocs.InspectPolicy(ctx, repo, repositorydocs.PolicyRequest{RepoID: req.RepoID, Revision: req.Revision, IncludeWorktree: req.IncludeWorktree})
	if err != nil {
		return preparedRepositoryDocsIndex{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_source_invalid"}
	}
	provider, err := rag.NewEmbeddingProviderFromConfig(eff.Config, effectiveProfile, rag.ProviderOptions{})
	if err != nil {
		return preparedRepositoryDocsIndex{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_provider_unavailable"}
	}
	namespaceIdentity, err := provider.NamespaceIdentity(ctx, rag.NamespaceRequest{RepoID: req.RepoID, ChunkPolicyID: repositorydocs.DefaultChunkPolicyID, LanguagePolicyID: rag.DefaultLanguagePolicyID, DocumentInstructionID: "repo-doc-v1", QueryInstructionID: "repo-doc-query-v1"})
	if err != nil {
		return preparedRepositoryDocsIndex{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_provider_unavailable"}
	}
	namespaceID := cache.EmbeddingNamespaceID(namespaceIdentity)
	req.expectedCommitOID = policy.CommitOID
	req.expectedPolicyHash = policy.Policy.PolicyHash
	req.expectedConfigDigest = policy.Policy.ConfigDigest
	req.expectedOverlayDigest = policy.OverlayDigest
	req.expectedNamespaceID = namespaceID
	if req.recoveryExpectedRevisionSetID != "" || req.recoveryExpectedWorkKey != "" {
		if strings.TrimSpace(req.SourceRegistrationID) == "" || req.SourceRegistrationGeneration <= 0 || strings.TrimSpace(req.RegistrationID) == "" {
			return preparedRepositoryDocsIndex{}, RepositoryDocsAdmissionStaleError{}
		}
		actualSetID := repositoryDocsRevisionSetIdentity(req, repo, policy, namespaceID).ID()
		actualWorkKey := repositoryDocsIndexWorkKey(req, repo, policy, namespaceID)
		if req.recoveryExpectedRevisionSetID != actualSetID || req.recoveryExpectedWorkKey != actualWorkKey {
			return preparedRepositoryDocsIndex{}, RepositoryDocsAdmissionStaleError{}
		}
	}
	return preparedRepositoryDocsIndex{request: req, repository: repo, policy: policy, namespaceID: namespaceID}, nil
}

func (m *JobManager) startPreparedRepositoryDocsIndex(ctx context.Context, manager Manager, prepared preparedRepositoryDocsIndex) (Job, error) {
	req := prepared.request
	if strings.TrimSpace(req.SourceRegistrationID) == "" || req.SourceRegistrationGeneration <= 0 {
		return Job{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_source_not_registered"}
	}
	workKey := repositoryDocsIndexWorkKey(req, prepared.repository, prepared.policy, prepared.namespaceID)
	expectedSetID := repositoryDocsRevisionSetIdentity(req, prepared.repository, prepared.policy, prepared.namespaceID).ID()
	jobCtx, cancel := context.WithCancel(ctx)
	if strings.TrimSpace(req.recoveryJobID) != "" {
		job, resumed, err := m.ResumeRepositoryDocsAdmission(req.recoveryJobID, req.RegistrationID, req.SourceRegistrationID, req.SourceRegistrationGeneration, expectedSetID, workKey, cancel)
		if err != nil {
			cancel()
			return Job{}, err
		}
		if resumed {
			go m.runRepositoryDocsIndexJob(jobCtx, manager, job.ID, req)
			return job, nil
		}
		cancel()
		return Job{}, RepositoryDocsAdmissionStaleError{}
	}
	job, created, err := m.createCoalescedJobWithIntent(RepositoryDocsIndexJobType, req.RepoID, strings.TrimSpace(req.Profile), 0, workKey, req.CacheUUID, req.RegistrationID, prepared.namespaceID, JobRecoveryIntent{
		SourceRegistrationID: req.SourceRegistrationID, SourceRegistrationGeneration: req.SourceRegistrationGeneration, ExpectedRevisionSetID: expectedSetID,
	}, cancel)
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

func repositoryDocsIndexWorkKey(req StartRepositoryDocsIndexJobRequest, repo *repositorydocs.Repository, policy repositorydocs.PolicyResult, namespaceID string) string {
	identity := repositoryDocsRevisionSetIdentity(req, repo, policy, namespaceID)
	return strings.Join([]string{
		RepositoryDocsIndexJobType, req.CacheUUID, req.RegistrationID, identity.ID(),
	}, ":")
}

func repositoryDocsRevisionSetIdentity(req StartRepositoryDocsIndexJobRequest, repo *repositorydocs.Repository, policy repositorydocs.PolicyResult, namespaceID string) repositorydocs.RevisionSetIdentity {
	return repositorydocs.NewRevisionSetIdentity(req.RepoID, req.SourceRegistrationID, req.SourceRegistrationGeneration, repo, policy.CommitOID, policy.Policy, policy.OverlayDigest, repositorydocs.DefaultProcessingPolicy(), namespaceID)
}

func (m *JobManager) runRepositoryDocsIndexJob(ctx context.Context, manager Manager, jobID string, req StartRepositoryDocsIndexJobRequest) {
	if !m.beginRepositoryDocsJob(jobID) {
		return
	}
	result, err := runRepositoryDocsIndex(ctx, manager, req, func(progress repositorydocs.IndexProgress) {
		m.updateJob(jobID, func(job *Job, now time.Time) {
			if repositoryDocsJobSourceFenced(job) || job.Status != JobStatusRunning {
				return
			}
			job.UpdatedAt = now
			job.Steps = progress.EligibleChunks
			job.Completed = progress.EmbeddedChunks + progress.ReusedChunks
			job.Progress = append(job.Progress, service.ProgressEvent{Type: "progress", Phase: progress.Phase, Collection: RepositoryDocsIndexJobType, RecordsListed: progress.EligibleChunks, RecordsFetched: progress.EmbeddedChunks, RecordsSkipped: progress.ReusedChunks + progress.ExcludedFiles, RecordsFailed: progress.FailedChunks + progress.MissingObjects, Message: "repository documentation " + progress.Phase})
		})
	})
	if err != nil {
		status, class := repositoryDocsIndexErrorStatus(err)
		m.updateRepositoryDocsTerminalJob(jobID, func(job *Job, now time.Time) {
			if repositoryDocsJobSourceFenced(job) {
				return
			}
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
		if status == JobStatusCancelled {
			m.persistRepositoryDocsTerminalCancellation(jobID)
		}
		return
	}
	status := JobStatusSucceeded
	if result.State == cache.RepoDocSetSuperseded {
		status = JobStatusSuperseded
	} else if result.State == cache.RepoDocSetBlocked || result.State == cache.RepoDocSetPartial {
		status = JobStatusFailed
	}
	m.updateRepositoryDocsTerminalJob(jobID, func(job *Job, now time.Time) {
		if repositoryDocsJobSourceFenced(job) {
			return
		}
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

// persistRepositoryDocsTerminalCancellation covers cancellations originating
// outside JobManager.Cancel (for example, daemon shutdown). It deliberately
// re-reads the committed terminal state: a generation rebind or a concurrent
// durable Cancel may have won while the worker was unwinding.
func (m *JobManager) persistRepositoryDocsTerminalCancellation(jobID string) {
	cancelled, ok := m.Get(jobID)
	if !ok || cancelled.Status != JobStatusCancelled || m.onRepositoryDocsCancelled == nil {
		return
	}
	if persistErr := m.onRepositoryDocsCancelled(cancelled); persistErr != nil {
		m.updateJob(jobID, func(job *Job, now time.Time) {
			if repositoryDocsJobSourceFenced(job) || job.Status != JobStatusCancelled {
				return
			}
			job.Status = JobStatusFailed
			job.UpdatedAt = now
			job.FinishedAt = &now
			job.ErrorClass = "repository_docs_cancel_persist_failed"
			job.Error = publicMaintenanceJobError(RepositoryDocsIndexJobType, job.ErrorClass)
			job.Progress = append(job.Progress, service.ProgressEvent{Type: JobStatusFailed, Phase: JobStatusFailed, Collection: RepositoryDocsIndexJobType, Message: job.Error})
		})
	}
}

func repositoryDocsJobSourceFenced(job *Job) bool {
	return job != nil && job.Status == JobStatusSuperseded && job.ErrorClass == "repository_docs_source_generation_superseded"
}

func repositoryDocsIndexErrorStatus(err error) (string, string) {
	if errors.Is(err, repositorydocs.ErrIndexSnapshotStale) {
		return JobStatusSuperseded, "repository_docs_snapshot_stale"
	}
	if errors.Is(err, context.Canceled) {
		return JobStatusCancelled, "cancelled"
	}
	return JobStatusFailed, maintenanceJobErrorClass(err, "repository_docs_index_failed")
}

func runRepositoryDocsIndex(ctx context.Context, manager Manager, req StartRepositoryDocsIndexJobRequest, progress func(repositorydocs.IndexProgress)) (repositorydocs.IndexResult, error) {
	eff, err := effectiveJobConfig(manager, req.CachePath)
	if err != nil {
		return repositorydocs.IndexResult{}, err
	}
	effectiveProfile, _, _, err := requireLocalRepositoryDocsProvider(eff.Config, req.Profile)
	if err != nil {
		return repositorydocs.IndexResult{}, err
	}
	store, err := cache.NewSQLiteStore(ctx, eff.Config.CachePath)
	if err != nil {
		return repositorydocs.IndexResult{}, err
	}
	defer store.Close()
	provider, err := rag.NewEmbeddingProviderFromConfig(eff.Config, effectiveProfile, rag.ProviderOptions{})
	if err != nil {
		return repositorydocs.IndexResult{}, err
	}
	repo, err := repositorydocs.OpenRepository(ctx, req.RepositoryPath)
	if err != nil {
		return repositorydocs.IndexResult{}, err
	}
	checkpoints, err := repositorydocs.NewFileVectorCheckpointStore(eff.Config.CachePath + ".repository-doc-vector-checkpoints")
	if err != nil {
		return repositorydocs.IndexResult{}, err
	}
	pendingVectors, err := store.ListRepositoryDocPendingVectorIdentities(ctx)
	if err != nil {
		return repositorydocs.IndexResult{}, err
	}
	activeCheckpoints := make(map[string]struct{}, len(pendingVectors))
	for _, pending := range pendingVectors {
		activeCheckpoints[repositorydocs.VectorCheckpointIdentity(pending.RepoID, pending.NamespaceID, pending.ChunkID)] = struct{}{}
	}
	if _, err := checkpoints.Prune(ctx, repositorydocs.VectorCheckpointRetentionPolicy{MaxAge: 7 * 24 * time.Hour, MaxBytes: 512 << 20, ActiveIdentities: activeCheckpoints}); err != nil {
		return repositorydocs.IndexResult{}, err
	}
	result, err := repositorydocs.NewIndexer(store, provider).WithVectorCheckpointStore(checkpoints).Run(ctx, repositorydocs.IndexRequest{
		RepoID: req.RepoID, SourceRegistrationID: req.SourceRegistrationID, SourceRegistrationGeneration: req.SourceRegistrationGeneration,
		Repository: repo, Revision: req.Revision,
		IncludeWorktree: req.IncludeWorktree, BatchSize: req.BatchSize, MaxChunks: req.MaxChunks,
		EnforceExpectedSnapshot: true, ExpectedCommitOID: req.expectedCommitOID,
		ExpectedPolicyHash: req.expectedPolicyHash, ExpectedConfigDigest: req.expectedConfigDigest,
		ExpectedOverlayDigest: req.expectedOverlayDigest, ExpectedNamespaceID: req.expectedNamespaceID,
		Progress: progress,
	})
	if err != nil {
		return result, err
	}
	vectorByteCeiling, err := repositoryDocsVectorByteCeiling(manager)
	if err != nil {
		return result, err
	}
	now := time.Now().UTC()
	gcLease, leaseErr := store.AcquireWriter(ctx, cache.WriterRequest{Operation: "repository-docs-gc", RepoID: req.RepoID})
	if leaseErr != nil {
		var contention cache.ErrLockContention
		if errors.As(leaseErr, &contention) {
			result.Message = "repository documentation retention deferred because another local writer is active"
			return result, nil
		}
		return result, leaseErr
	}
	gcResult, gcErr := store.PruneRepositoryDocRevisionSets(ctx, req.RepoID, cache.RepositoryDocRetentionPolicy{
		RetainCommittedPerIdentity: 8,
		RetainOverlaysPerIdentity:  0,
		CommittedCutoff:            now.Add(-30 * 24 * time.Hour),
		OverlayCutoff:              now.Add(-24 * time.Hour),
		TerminalCutoff:             now.Add(-7 * 24 * time.Hour),
		MaxVectorBytes:             vectorByteCeiling,
		ProtectedSetIDs:            []string{result.RevisionSetID},
	})
	releaseErr := store.ReleaseWriter(context.Background(), gcLease)
	if gcErr != nil {
		return result, fmt.Errorf("repository docs: metadata retention: %w", gcErr)
	}
	if releaseErr != nil {
		return result, fmt.Errorf("repository docs: release metadata retention writer: %w", releaseErr)
	}
	result.GCRevisionSets = gcResult.RevisionSetsDeleted
	result.GCChunks = gcResult.ChunksDeleted
	result.GCVectors = gcResult.VectorsDeleted
	result.GCBytesBefore = gcResult.VectorBytesBefore
	result.GCBytesAfter = gcResult.VectorBytesAfter
	return result, nil
}

func requireLocalRepositoryDocsProvider(cfg config.Config, requestedProfile string) (string, string, string, error) {
	profileID := strings.TrimSpace(requestedProfile)
	if profileID == "" {
		profileID = strings.TrimSpace(cfg.RAG.Indexing.Profile)
	}
	if profileID == "" {
		profileID = strings.TrimSpace(cfg.RAG.DefaultProfile)
	}
	profile, ok := cfg.RAG.Profiles[profileID]
	if !ok {
		return "", "", "", fmt.Errorf("repository docs: embedding profile %q is not configured", profileID)
	}
	providerID := strings.TrimSpace(profile.Provider)
	provider, ok := cfg.RAG.Providers[providerID]
	if !ok || providerID == "" {
		return "", "", "", fmt.Errorf("repository docs: embedding provider for profile %q is not configured", profileID)
	}
	boundary := strings.TrimSpace(provider.DataBoundary)
	if boundary != "local_process" && boundary != "local_network" {
		return profileID, providerID, boundary, RepositoryDocsProviderBoundaryError{ProviderID: providerID, Boundary: boundary}
	}
	return profileID, providerID, boundary, nil
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
