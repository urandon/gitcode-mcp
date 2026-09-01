package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/service"
)

const SyncJobType = "sync"

// CacheWriterIdentityError deliberately carries no underlying error. Cache
// authority discovery can fail with messages containing private absolute
// paths, which must not cross the IPC/CLI public diagnostic boundary.
type CacheWriterIdentityError struct{ code string }

func (e CacheWriterIdentityError) Error() string {
	switch e.code {
	case "cache_uuid_mismatch":
		return "service: cache uuid does not match the selected cache authority"
	case "registration_id_mismatch":
		return "service: registration id does not match the selected cache repository"
	case "repository_binding_unavailable":
		return "service: repository binding is unavailable"
	default:
		return "service: selected cache authority is unavailable"
	}
}

func (e CacheWriterIdentityError) DiagnosticCode() string { return e.code }

type StartSyncJobRequest struct {
	RepoID         string `json:"repo_id"`
	ProviderMode   string `json:"provider_mode,omitempty"`
	CachePath      string `json:"cache_path,omitempty"`
	Issues         bool   `json:"issues,omitempty"`
	Wiki           bool   `json:"wiki,omitempty"`
	Pulls          bool   `json:"pulls,omitempty"`
	Comments       bool   `json:"comments,omitempty"`
	IssueComments  bool   `json:"issue_comments,omitempty"`
	PRComments     bool   `json:"pr_comments,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	MaxPages       int    `json:"max_pages,omitempty"`
	MaxRecords     int    `json:"max_records,omitempty"`
	PerPage        int    `json:"per_page,omitempty"`
	Page           int    `json:"page,omitempty"`
	CacheUUID      string `json:"cache_uuid,omitempty"`
	RegistrationID string `json:"registration_id,omitempty"`
	Lane           string `json:"lane,omitempty"`
}

func normalizeCacheWriterIdentity(ctx context.Context, manager Manager, cachePath, cacheUUID, registrationID, repoID *string) error {
	eff, err := effectiveJobConfig(manager, strings.TrimSpace(*cachePath))
	if err != nil {
		return CacheWriterIdentityError{code: "cache_authority_unavailable"}
	}
	store, err := cache.NewSQLiteReadOnlyStore(ctx, eff.Config.CachePath)
	if err != nil {
		return CacheWriterIdentityError{code: "cache_authority_unavailable"}
	}
	defer store.Close()
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		return CacheWriterIdentityError{code: "cache_authority_unavailable"}
	}
	if provided := strings.TrimSpace(*cacheUUID); provided != "" && provided != identity.UUID {
		return CacheWriterIdentityError{code: "cache_uuid_mismatch"}
	}
	binding, err := store.ResolveRepositoryBinding(ctx, strings.TrimSpace(*repoID))
	if err != nil {
		return CacheWriterIdentityError{code: "repository_binding_unavailable"}
	}
	*cachePath = eff.Config.CachePath
	*cacheUUID = identity.UUID
	*repoID = binding.RepoID
	wantRegistration := maintenanceRegistrationID(identity.UUID, binding.RepoID)
	if provided := strings.TrimSpace(*registrationID); provided != "" && provided != wantRegistration {
		return CacheWriterIdentityError{code: "registration_id_mismatch"}
	}
	*registrationID = wantRegistration
	return nil
}

func (m *JobManager) StartSync(ctx context.Context, manager Manager, req StartSyncJobRequest) (Job, error) {
	req.RepoID = strings.TrimSpace(req.RepoID)
	if req.RepoID == "" {
		return Job{}, errors.New("repo_id is required")
	}
	if err := normalizeCacheWriterIdentity(ctx, manager, &req.CachePath, &req.CacheUUID, &req.RegistrationID, &req.RepoID); err != nil {
		return Job{}, err
	}
	workKey := syncWorkKey(req)
	ctx, cancel := context.WithCancel(ctx)
	job, created, err := m.createCoalescedJob(SyncJobType, req.RepoID, "", 0, workKey, req.CacheUUID, req.RegistrationID, "", cancel)
	if err != nil {
		cancel()
		return Job{}, err
	}
	if !created {
		cancel()
		return job, nil
	}
	go m.runSyncJob(ctx, manager, job.ID, req)
	return job, nil
}

func syncWorkKey(req StartSyncJobRequest) string {
	cacheID := strings.TrimSpace(req.CacheUUID)
	if cacheID == "" {
		cacheID = strings.TrimSpace(req.CachePath)
	}
	lane := strings.TrimSpace(req.Lane)
	if lane == "" {
		lane = "manual"
	}
	collections := fmt.Sprintf("%t%t%t%t%t", req.Issues, req.Wiki, req.Pulls, req.IssueComments, req.PRComments || req.Comments)
	return strings.Join([]string{SyncJobType, cacheID, strings.TrimSpace(req.RepoID), lane, collections}, ":")
}

func (m *JobManager) runSyncJob(ctx context.Context, manager Manager, jobID string, req StartSyncJobRequest) {
	defer m.markWorkerFinished(jobID)
	m.updateJob(jobID, func(job *Job, now time.Time) {
		job.Status = JobStatusRunning
		job.StartedAt = &now
		job.UpdatedAt = now
		job.Progress = append(job.Progress, service.ProgressEvent{Type: "started", Phase: JobStatusRunning, Collection: SyncJobType, Message: "sync job started"})
	})
	progressCh := make(chan service.ProgressEvent, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range progressCh {
			ev = sanitizeMaintenanceProgress(SyncJobType, ev)
			m.updateJob(jobID, func(job *Job, now time.Time) {
				job.UpdatedAt = now
				job.Progress = append(job.Progress, ev)
				if ev.RecordsListed > 0 {
					job.Steps += ev.RecordsListed
				}
				if ev.RecordsFetched > 0 {
					job.Completed += ev.RecordsFetched
				}
			})
		}
	}()
	var result *service.SyncResourcesResult
	var collections []syncCollectionResult
	var err error
	if syncDurableCollections(req) {
		result, collections, err = m.runDurableSync(ctx, manager, jobID, req, progressCh)
	} else {
		result, collections, err = runSync(ctx, manager, req, progressCh)
	}
	close(progressCh)
	<-done
	if result == nil {
		result = &service.SyncResourcesResult{}
	}
	if strings.TrimSpace(req.Lane) != "" {
		if frontierErr := recordMaintenanceSyncFrontiers(context.Background(), req, collections); frontierErr != nil && err == nil {
			err = fmt.Errorf("record maintenance frontier: %w", frontierErr)
		}
	}
	if err != nil {
		status := JobStatusFailed
		if errors.Is(ctx.Err(), context.Canceled) {
			status = JobStatusCancelled
		}
		m.updateJob(jobID, func(job *Job, now time.Time) {
			job.Status = status
			job.UpdatedAt = now
			job.FinishedAt = &now
			job.ErrorClass = maintenanceJobErrorClass(err, "sync_failed")
			if status == JobStatusCancelled {
				job.ErrorClass = "cancelled"
			}
			job.Error = publicMaintenanceJobError(SyncJobType, job.ErrorClass)
			job.Progress = append(job.Progress, failedSyncCollectionProgress(collections)...)
			job.Progress = append(job.Progress, service.ProgressEvent{Type: status, Phase: status, Collection: SyncJobType, RecordsListed: result.RecordsListed, RecordsFetched: result.SuccessCount, RecordsFailed: result.FailureCount, Message: job.Error})
			delete(m.cancel, jobID)
		})
		return
	}
	m.updateJob(jobID, func(job *Job, now time.Time) {
		job.Status = JobStatusSucceeded
		job.UpdatedAt = now
		job.FinishedAt = &now
		if result.RecordsListed > 0 {
			job.Steps = result.RecordsListed
		}
		if result.SuccessCount > 0 {
			job.Completed = result.SuccessCount
		}
		job.Progress = append(job.Progress, service.ProgressEvent{Type: "finished", Phase: JobStatusSucceeded, Collection: SyncJobType, RecordsListed: result.RecordsListed, RecordsFetched: result.SuccessCount, RecordsFailed: result.FailureCount, Message: "sync job finished"})
		delete(m.cancel, jobID)
	})
}

func syncDurableCollections(req StartSyncJobRequest) bool {
	return (req.Issues || req.Wiki || req.Pulls) && !req.Comments && !req.IssueComments && !req.PRComments
}

type durableCollectionBatch struct {
	payload          []byte
	recordCount      int
	checkpoint       string
	providerRevision string
	idempotencyKey   string
	fetchedAt        time.Time
}

type durableCollectionWork struct {
	collection string
	remoteType string
	fetch      func(context.Context) (durableCollectionBatch, error)
	commit     func(context.Context, []byte) (*service.SyncResourcesResult, error)
}

func (m *JobManager) runDurableSync(ctx context.Context, manager Manager, jobID string, req StartSyncJobRequest, progressCh chan<- service.ProgressEvent) (*service.SyncResourcesResult, []syncCollectionResult, error) {
	store, svc, err := newSyncJobService(ctx, manager, req)
	if err != nil {
		return nil, nil, err
	}
	defer store.Close()
	schema, err := store.SchemaVersion(ctx)
	if err != nil {
		return nil, nil, err
	}
	bulkReq := syncBulkRequest(req, progressCh)
	works := durableCollectionWorks(svc, bulkReq, req)
	aggregate := &service.SyncResourcesResult{Results: []service.SyncResult{}, Failures: []service.ResourceError{}}
	collections := make([]syncCollectionResult, 0, len(works))
	var syncErr error
	for _, work := range works {
		result, collection, collectionErr := m.runDurableCollection(ctx, manager, jobID, req, schema, work)
		mergeSyncResources(aggregate, result)
		collections = append(collections, collection)
		syncErr = mergeSyncError(syncErr, result, collectionErr)
		if errors.Is(ctx.Err(), context.Canceled) {
			break
		}
	}
	return aggregate, collections, syncErr
}

func durableCollectionWorks(svc *service.Service, bulkReq service.BulkSyncRequest, req StartSyncJobRequest) []durableCollectionWork {
	works := make([]durableCollectionWork, 0, 3)
	if req.Issues {
		works = append(works, durableCollectionWork{
			collection: "issues", remoteType: "issue",
			fetch: func(ctx context.Context) (durableCollectionBatch, error) {
				batch, err := svc.FetchIssueSyncBatch(ctx, bulkReq)
				payload, marshalErr := json.Marshal(batch)
				if marshalErr != nil {
					return durableCollectionBatch{}, marshalErr
				}
				return durableCollectionBatch{payload: payload, recordCount: batch.RecordCount(), checkpoint: batch.StopReason, providerRevision: batch.HighUpdatedAt.Format(time.RFC3339Nano), idempotencyKey: batch.IdempotencyKey, fetchedAt: batch.FetchedAt}, err
			},
			commit: func(ctx context.Context, payload []byte) (*service.SyncResourcesResult, error) {
				var batch service.DurableIssueSyncBatch
				if err := json.Unmarshal(payload, &batch); err != nil {
					return nil, err
				}
				return svc.CommitIssueSyncBatch(ctx, batch, bulkReq.ProgressChan)
			},
		})
	}
	if req.Wiki {
		works = append(works, durableCollectionWork{
			collection: "wiki", remoteType: "wiki",
			fetch: func(ctx context.Context) (durableCollectionBatch, error) {
				batch, err := svc.FetchWikiSyncBatch(ctx, bulkReq)
				payload, marshalErr := json.Marshal(batch)
				if marshalErr != nil {
					return durableCollectionBatch{}, marshalErr
				}
				return durableCollectionBatch{payload: payload, recordCount: batch.RecordCount(), checkpoint: batch.StopReason, providerRevision: batch.ProviderRevision, idempotencyKey: batch.IdempotencyKey, fetchedAt: batch.FetchedAt}, err
			},
			commit: func(ctx context.Context, payload []byte) (*service.SyncResourcesResult, error) {
				var batch service.DurableWikiSyncBatch
				if err := json.Unmarshal(payload, &batch); err != nil {
					return nil, err
				}
				return svc.CommitWikiSyncBatch(ctx, batch, bulkReq.ProgressChan)
			},
		})
	}
	if req.Pulls {
		works = append(works, durableCollectionWork{
			collection: "pulls", remoteType: "pull_request",
			fetch: func(ctx context.Context) (durableCollectionBatch, error) {
				batch, err := svc.FetchPullSyncBatch(ctx, bulkReq)
				payload, marshalErr := json.Marshal(batch)
				if marshalErr != nil {
					return durableCollectionBatch{}, marshalErr
				}
				return durableCollectionBatch{payload: payload, recordCount: batch.RecordCount(), checkpoint: batch.StopReason, providerRevision: batch.HighUpdatedAt.Format(time.RFC3339Nano), idempotencyKey: batch.IdempotencyKey, fetchedAt: batch.FetchedAt}, err
			},
			commit: func(ctx context.Context, payload []byte) (*service.SyncResourcesResult, error) {
				var batch service.DurablePullSyncBatch
				if err := json.Unmarshal(payload, &batch); err != nil {
					return nil, err
				}
				return svc.CommitPullSyncBatch(ctx, batch, bulkReq.ProgressChan)
			},
		})
	}
	return works
}

func (m *JobManager) runDurableCollection(ctx context.Context, manager Manager, jobID string, req StartSyncJobRequest, schema int, work durableCollectionWork) (*service.SyncResourcesResult, syncCollectionResult, error) {
	m.updateJob(jobID, func(job *Job, now time.Time) {
		job.SyncStage = &SyncStageView{RepoID: req.RepoID, Collection: work.collection, Phase: SyncStageFetching, UpdatedAt: now}
		job.Progress = append(job.Progress, service.ProgressEvent{Type: "phase", Phase: string(SyncStageFetching), Collection: work.collection, Message: "collection fetch started"})
	})
	batch, fetchErr := work.fetch(ctx)
	if fetchErr != nil && batch.recordCount == 0 {
		m.rejectUnpersistedSyncStage(jobID, maintenanceJobErrorClass(fetchErr, "provider_fetch_failed"))
		collection := syncCollectionResult{RemoteType: work.remoteType, Err: fetchErr}
		return nil, collection, fetchErr
	}
	runtimeDir := strings.TrimSpace(manager.RuntimeDir)
	if runtimeDir == "" && strings.TrimSpace(m.snapshotPath) != "" {
		runtimeDir = filepath.Dir(m.snapshotPath)
	}
	journal := NewSyncStageJournal(runtimeDir, SyncStageLimits{})
	stage, err := journal.Create(SyncStageEnvelope{
		JobID: jobID, CacheUUID: req.CacheUUID, CacheSchema: schema, CachePath: req.CachePath, RegistrationID: req.RegistrationID,
		RepoID: req.RepoID, Collection: work.collection, Checkpoint: batch.checkpoint,
		ProviderRevision: batch.providerRevision,
		IdempotencyKey:   batch.idempotencyKey, RecordCount: batch.recordCount, Payload: batch.payload,
		State: SyncStageState{Phase: SyncStageStaged, RetryBudget: defaultSyncCommitRetries, FetchedAt: batch.fetchedAt},
	})
	if err != nil {
		m.rejectUnpersistedSyncStage(jobID, maintenanceJobErrorClass(err, "stage_persist_failed"))
		collection := syncCollectionResult{RemoteType: work.remoteType, Err: err}
		return nil, collection, err
	}
	m.setJobSyncStage(jobID, stage, "collection batch staged")

	for {
		if err := ctx.Err(); err != nil {
			stage = rejectCancelledSyncStage(journal, stage)
			m.setJobSyncStage(jobID, stage, "staged batch cancelled")
			collection := syncCollectionResult{RemoteType: work.remoteType, Err: err}
			return nil, collection, err
		}
		if targetErr := validateSyncStageTargetReadOnly(ctx, stage); targetErr != nil {
			state := stage.State
			state.Phase = SyncStageRejected
			state.TerminalReason = "cache_identity_or_schema_changed"
			stage, _ = journal.UpdateState(stage.StageID, state)
			m.setJobSyncStage(jobID, stage, "staged batch rejected")
			collection := syncCollectionResult{RemoteType: work.remoteType, Err: targetErr}
			return nil, collection, targetErr
		}
		releaseTurn, turnErr := m.acquireSyncCommitTurn(ctx, stage.CacheUUID, stage.StageID)
		if turnErr != nil {
			collection := syncCollectionResult{RemoteType: work.remoteType, Err: turnErr}
			return nil, collection, turnErr
		}
		state := stage.State
		state.Phase = SyncStageCommitting
		stage, err = journal.UpdateState(stage.StageID, state)
		if err != nil {
			releaseTurn()
			collection := syncCollectionResult{RemoteType: work.remoteType, Err: err}
			return nil, collection, err
		}
		m.setJobSyncStage(jobID, stage, "staged batch commit started")
		result, commitErr := work.commit(ctx, stage.Payload)
		releaseTurn()
		if commitErr == nil {
			state = stage.State
			state.Phase = SyncStageCommitted
			state.CommittedAt = time.Now().UTC()
			state.BlockerClass, state.BlockingOp, state.BlockingJobRef = "", "", ""
			stage, err = journal.UpdateState(stage.StageID, state)
			if err != nil {
				collection := syncCollectionResult{RemoteType: work.remoteType, Result: result, Err: err}
				return result, collection, err
			}
			m.setJobSyncStage(jobID, stage, "staged batch committed")
			collectionErr := fetchErr
			collection := syncCollectionResult{RemoteType: work.remoteType, Result: result, Err: collectionErr}
			return result, collection, collectionErr
		}
		var contention cache.ErrLockContention
		if !errors.As(commitErr, &contention) {
			state = stage.State
			state.Phase, state.TerminalReason = SyncStageRejected, maintenanceJobErrorClass(commitErr, "sync_commit_failed")
			stage, _ = journal.UpdateState(stage.StageID, state)
			m.setJobSyncStage(jobID, stage, "staged batch commit rejected")
			collection := syncCollectionResult{RemoteType: work.remoteType, Result: result, Err: commitErr}
			return result, collection, commitErr
		}
		state, retry := nextSyncCommitRetry(stage.StageID, stage.State, time.Now().UTC())
		state.BlockingOp = contention.PublicOperation()
		state.BlockingJobRef = m.blockingCacheWriterRef(jobID, stage.CacheUUID)
		stage, err = journal.UpdateState(stage.StageID, state)
		if err != nil {
			collection := syncCollectionResult{RemoteType: work.remoteType, Result: result, Err: err}
			return result, collection, err
		}
		m.setJobSyncStage(jobID, stage, "waiting for cache writer")
		if !retry {
			collection := syncCollectionResult{RemoteType: work.remoteType, Result: result, Err: commitErr}
			return result, collection, commitErr
		}
		wait := time.Until(stage.State.RetryAfter)
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			stage = rejectCancelledSyncStage(journal, stage)
			m.setJobSyncStage(jobID, stage, "staged batch cancelled")
			collection := syncCollectionResult{RemoteType: work.remoteType, Result: result, Err: ctx.Err()}
			return result, collection, ctx.Err()
		case <-timer.C:
		}
	}
}

func (m *JobManager) rejectUnpersistedSyncStage(jobID, reason string) {
	m.updateJob(jobID, func(job *Job, now time.Time) {
		if job.SyncStage == nil {
			job.SyncStage = &SyncStageView{}
		}
		job.SyncStage.Phase = SyncStageRejected
		job.SyncStage.TerminalCause = strings.TrimSpace(reason)
		job.SyncStage.RetryAfter = time.Time{}
		job.SyncStage.UpdatedAt = now
	})
}

// validateSyncStageTargetReadOnly proves the exact cache and repository
// authority before any writable SQLite open. This prevents recovery from
// creating a removed cache or migrating a replacement cache that does not own
// the staged batch.
func validateSyncStageTargetReadOnly(ctx context.Context, stage SyncStageEnvelope) error {
	store, err := cache.NewSQLiteReadOnlyStore(ctx, stage.CachePath)
	if err != nil {
		return CacheWriterIdentityError{code: "cache_authority_unavailable"}
	}
	defer store.Close()
	identity, err := store.CacheIdentity(ctx)
	if err != nil || identity.UUID != stage.CacheUUID {
		return CacheWriterIdentityError{code: "cache_uuid_mismatch"}
	}
	schema, err := store.SchemaVersion(ctx)
	if err != nil || schema != stage.CacheSchema {
		return CacheWriterIdentityError{code: "cache_schema_mismatch"}
	}
	binding, err := store.ResolveRepositoryBinding(ctx, stage.RepoID)
	if err != nil || binding.RepoID != stage.RepoID {
		return CacheWriterIdentityError{code: "repository_binding_unavailable"}
	}
	if maintenanceRegistrationID(identity.UUID, binding.RepoID) != stage.RegistrationID {
		return CacheWriterIdentityError{code: "registration_id_mismatch"}
	}
	return nil
}

func rejectCancelledSyncStage(journal *SyncStageJournal, stage SyncStageEnvelope) SyncStageEnvelope {
	state := stage.State
	state.Phase = SyncStageRejected
	state.RetryAfter = time.Time{}
	state.TerminalReason = "cancelled"
	updated, err := journal.UpdateState(stage.StageID, state)
	if err == nil {
		return updated
	}
	return stage
}

// RecoverSyncStages resumes private, checksummed batches after the durable job
// snapshot has been loaded and active jobs have been marked interrupted. No
// provider client is constructed: recovery is a cache-only commit replay.
func (m *JobManager) RecoverSyncStages(ctx context.Context, manager Manager) error {
	runtimeDir := strings.TrimSpace(manager.RuntimeDir)
	if runtimeDir == "" {
		paths, err := manager.ResolvePaths()
		if err != nil {
			return err
		}
		runtimeDir = paths.RuntimeDir
	}
	journal := NewSyncStageJournal(runtimeDir, SyncStageLimits{})
	stages, rejections, err := journal.ListForRecovery()
	if err != nil {
		return err
	}
	for _, rejection := range rejections {
		m.rejectInterruptedSyncStageByRef(rejection.StageRef, rejection.Reason)
	}
	now := time.Now().UTC()
	for _, stage := range stages {
		if syncStageTerminal(stage.State.Phase) {
			continue
		}
		if !stage.ExpiresAt.After(now) {
			state := stage.State
			state.Phase, state.TerminalReason, state.RetryAfter = SyncStageRejected, "stale_stage_expired", time.Time{}
			updated, err := journal.UpdateState(stage.StageID, state)
			if err != nil {
				return err
			}
			m.rejectInterruptedSyncStage(updated, state.TerminalReason)
			continue
		}
		if stage.Collection != "issues" && stage.Collection != "wiki" && stage.Collection != "pulls" {
			state := stage.State
			state.Phase, state.TerminalReason = SyncStageRejected, "unsupported_stage_collection"
			updated, err := journal.UpdateState(stage.StageID, state)
			if err != nil {
				return err
			}
			m.rejectInterruptedSyncStage(updated, state.TerminalReason)
			continue
		}
		job, ok := m.Get(stage.JobID)
		if !ok || job.Type != SyncJobType || job.Status != JobStatusInterrupted {
			state := stage.State
			state.Phase, state.TerminalReason = SyncStageRejected, "orphaned_or_incompatible_job"
			updated, err := journal.UpdateState(stage.StageID, state)
			if err != nil {
				return err
			}
			m.rejectInterruptedSyncStage(updated, state.TerminalReason)
			continue
		}
		workerCtx, cancel := context.WithCancel(ctx)
		if err := m.resumeInterruptedSyncStage(stage, cancel); err != nil {
			cancel()
			return err
		}
		go m.runRecoveredSyncStage(workerCtx, manager, journal, stage)
	}
	return nil
}

func (m *JobManager) rejectInterruptedSyncStage(stage SyncStageEnvelope, reason string) {
	m.rejectInterruptedSyncStageByRef(stage.PublicView().StageRef, reason)
}

func (m *JobManager) rejectInterruptedSyncStageByRef(stageRef, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	for _, job := range m.jobs {
		if job.Status != JobStatusInterrupted || job.SyncStage == nil || job.SyncStage.StageRef != stageRef {
			continue
		}
		job.Status, job.ErrorClass = JobStatusFailed, reason
		job.Error = publicMaintenanceJobError(SyncJobType, reason)
		job.UpdatedAt, job.FinishedAt = now, &now
		job.SyncStage.Phase, job.SyncStage.TerminalCause, job.SyncStage.RetryAfter = SyncStageRejected, reason, time.Time{}
		job.Progress = append(job.Progress, service.ProgressEvent{Type: JobStatusFailed, Phase: string(SyncStageRejected), Collection: job.SyncStage.Collection, Message: "staged batch rejected during restart recovery"})
	}
	_ = m.saveLocked()
}

func (m *JobManager) resumeInterruptedSyncStage(stage SyncStageEnvelope, cancel context.CancelFunc) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[stage.JobID]
	now := m.now().UTC()
	view := stage.PublicView()
	job.Status, job.Error, job.ErrorClass = JobStatusRunning, "", ""
	job.FinishedAt = nil
	job.UpdatedAt = now
	job.SyncStage = &view
	job.Progress = append(job.Progress, service.ProgressEvent{Type: "recovered", Phase: string(stage.State.Phase), Collection: stage.Collection, RecordsListed: stage.RecordCount, RecordsFetched: stage.RecordCount, Message: "staged batch recovered after service restart"})
	m.cancel[job.ID] = cancel
	m.inflightWorkers[job.ID] = true
	return m.saveLocked()
}

func (m *JobManager) runRecoveredSyncStage(ctx context.Context, manager Manager, journal *SyncStageJournal, stage SyncStageEnvelope) {
	defer m.markWorkerFinished(stage.JobID)
	result, finalStage, err := m.commitRecoveredSyncStage(ctx, manager, journal, stage)
	if result == nil {
		result = &service.SyncResourcesResult{}
	}
	status := JobStatusSucceeded
	errorClass, publicError := "", ""
	if err != nil {
		status = JobStatusFailed
		errorClass = maintenanceJobErrorClass(err, "sync_commit_failed")
		publicError = publicMaintenanceJobError(SyncJobType, errorClass)
		if errors.Is(ctx.Err(), context.Canceled) {
			status, errorClass = JobStatusCancelled, "cancelled"
			publicError = publicMaintenanceJobError(SyncJobType, errorClass)
		}
	}
	view := finalStage.PublicView()
	m.updateJob(stage.JobID, func(job *Job, now time.Time) {
		job.Status, job.ErrorClass, job.Error = status, errorClass, publicError
		job.UpdatedAt, job.FinishedAt, job.SyncStage = now, &now, &view
		job.Steps, job.Completed = result.RecordsListed, result.SuccessCount
		job.Progress = append(job.Progress, service.ProgressEvent{Type: status, Phase: status, Collection: stage.Collection, RecordsListed: result.RecordsListed, RecordsFetched: result.SuccessCount, RecordsFailed: result.FailureCount, Message: map[bool]string{true: "recovered staged batch committed", false: publicError}[err == nil]})
		delete(m.cancel, job.ID)
	})
}

func (m *JobManager) commitRecoveredSyncStage(ctx context.Context, manager Manager, journal *SyncStageJournal, stage SyncStageEnvelope) (*service.SyncResourcesResult, SyncStageEnvelope, error) {
	eff, err := effectiveJobConfig(manager, stage.CachePath)
	if err != nil {
		return nil, stage, err
	}
	if err := validateSyncStageTargetReadOnly(ctx, stage); err != nil {
		state := stage.State
		state.Phase, state.TerminalReason = SyncStageRejected, "cache_identity_schema_or_binding_changed"
		updated, updateErr := journal.UpdateState(stage.StageID, state)
		if updateErr == nil {
			stage = updated
			m.setJobSyncStage(stage.JobID, stage, "recovered staged batch rejected before writable open")
		}
		return nil, stage, err
	}
	store, err := cache.NewSQLiteStore(ctx, stage.CachePath)
	if err != nil {
		return nil, stage, err
	}
	defer store.Close()
	svc := service.NewWithClientConfig(store, nil, service.ServiceConfig{LockPath: eff.Config.LockPath})
	commit := func() (*service.SyncResourcesResult, error) {
		switch stage.Collection {
		case "issues":
			var batch service.DurableIssueSyncBatch
			if err := json.Unmarshal(stage.Payload, &batch); err != nil {
				return nil, err
			}
			return svc.CommitIssueSyncBatch(ctx, batch, nil)
		case "wiki":
			var batch service.DurableWikiSyncBatch
			if err := json.Unmarshal(stage.Payload, &batch); err != nil {
				return nil, err
			}
			return svc.CommitWikiSyncBatch(ctx, batch, nil)
		case "pulls":
			var batch service.DurablePullSyncBatch
			if err := json.Unmarshal(stage.Payload, &batch); err != nil {
				return nil, err
			}
			return svc.CommitPullSyncBatch(ctx, batch, nil)
		default:
			return nil, ErrSyncStageCorrupt
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			stage = rejectCancelledSyncStage(journal, stage)
			m.setJobSyncStage(stage.JobID, stage, "recovered staged batch cancelled")
			return nil, stage, err
		}
		identity, identityErr := store.CacheIdentity(ctx)
		currentSchema, schemaErr := store.SchemaVersion(ctx)
		if identityErr != nil || schemaErr != nil || identity.UUID != stage.CacheUUID || currentSchema != stage.CacheSchema {
			state := stage.State
			state.Phase, state.TerminalReason = SyncStageRejected, "cache_identity_or_schema_changed"
			stage, _ = journal.UpdateState(stage.StageID, state)
			m.setJobSyncStage(stage.JobID, stage, "recovered staged batch rejected")
			return nil, stage, CacheWriterIdentityError{code: "cache_uuid_mismatch"}
		}
		state := stage.State
		state.Phase = SyncStageCommitting
		releaseTurn, turnErr := m.acquireSyncCommitTurn(ctx, stage.CacheUUID, stage.StageID)
		if turnErr != nil {
			return nil, stage, turnErr
		}
		stage, err = journal.UpdateState(stage.StageID, state)
		if err != nil {
			releaseTurn()
			return nil, stage, err
		}
		m.setJobSyncStage(stage.JobID, stage, "recovered staged batch commit started")
		result, commitErr := commit()
		releaseTurn()
		if commitErr == nil {
			state = stage.State
			state.Phase, state.CommittedAt = SyncStageCommitted, time.Now().UTC()
			state.BlockerClass, state.BlockingOp, state.BlockingJobRef, state.RetryAfter = "", "", "", time.Time{}
			stage, err = journal.UpdateState(stage.StageID, state)
			if err == nil {
				m.setJobSyncStage(stage.JobID, stage, "recovered staged batch committed")
			}
			return result, stage, err
		}
		var contention cache.ErrLockContention
		if !errors.As(commitErr, &contention) {
			state = stage.State
			state.Phase, state.TerminalReason = SyncStageRejected, maintenanceJobErrorClass(commitErr, "sync_commit_failed")
			stage, _ = journal.UpdateState(stage.StageID, state)
			return result, stage, commitErr
		}
		state, retry := nextSyncCommitRetry(stage.StageID, stage.State, time.Now().UTC())
		state.BlockingOp = contention.PublicOperation()
		state.BlockingJobRef = m.blockingCacheWriterRef(stage.JobID, stage.CacheUUID)
		stage, err = journal.UpdateState(stage.StageID, state)
		if err != nil || !retry {
			if err != nil {
				return result, stage, err
			}
			return result, stage, commitErr
		}
		m.setJobSyncStage(stage.JobID, stage, "recovered stage waiting for cache writer")
		timer := time.NewTimer(max(time.Until(stage.State.RetryAfter), 0))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			continue
		}
	}
}

// blockingCacheWriterRef exposes only the retained public job identifier. It
// deliberately excludes the current durable sync worker and never returns a
// cache path or other private writer metadata.
func (m *JobManager) blockingCacheWriterRef(currentJobID, cacheUUID string) string {
	cacheUUID = strings.TrimSpace(cacheUUID)
	if cacheUUID == "" {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if writerID := m.directCacheWriters[cacheUUID]; writerID != "" && writerID != currentJobID {
		return writerID
	}
	for id, job := range m.jobs {
		if id == currentJobID || job.CacheUUID != cacheUUID || !isCacheWriterJob(job.Type) {
			continue
		}
		if jobActiveStatus(job.Status) || m.inflightWorkers[id] {
			return job.ID
		}
	}
	return ""
}

// acquireSyncCommitTurn provides FIFO admission among staged commits sharing a
// cache UUID. The turn is held only for one cache-local commit attempt and is
// released before contention backoff, so one repository cannot monopolize the
// retry loop or block unrelated provider fetches.
func (m *JobManager) acquireSyncCommitTurn(ctx context.Context, cacheUUID, stageID string) (func(), error) {
	waiter := syncCommitWaiter{stageID: stageID, ready: make(chan struct{})}
	m.mu.Lock()
	queue := append(m.syncCommitQueues[cacheUUID], waiter)
	m.syncCommitQueues[cacheUUID] = queue
	if len(queue) == 1 {
		close(waiter.ready)
	}
	m.mu.Unlock()

	select {
	case <-waiter.ready:
		var once sync.Once
		return func() { once.Do(func() { m.releaseSyncCommitTurn(cacheUUID, stageID) }) }, nil
	case <-ctx.Done():
		m.removeSyncCommitWaiter(cacheUUID, stageID)
		return nil, ctx.Err()
	}
}

func (m *JobManager) releaseSyncCommitTurn(cacheUUID, stageID string) {
	m.removeSyncCommitWaiter(cacheUUID, stageID)
}

func (m *JobManager) removeSyncCommitWaiter(cacheUUID, stageID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	queue := m.syncCommitQueues[cacheUUID]
	for index, waiter := range queue {
		if waiter.stageID != stageID {
			continue
		}
		wasHead := index == 0
		queue = append(queue[:index], queue[index+1:]...)
		if len(queue) == 0 {
			delete(m.syncCommitQueues, cacheUUID)
			return
		}
		m.syncCommitQueues[cacheUUID] = queue
		if wasHead {
			close(queue[0].ready)
		}
		return
	}
}

func (m *JobManager) setJobSyncStage(jobID string, stage SyncStageEnvelope, message string) {
	view := stage.PublicView()
	m.updateJob(jobID, func(job *Job, now time.Time) {
		job.SyncStage = &view
		job.UpdatedAt = now
		job.Progress = append(job.Progress, service.ProgressEvent{
			Type: "phase", Phase: string(view.Phase), Collection: stage.Collection,
			RecordsListed: view.Staged, RecordsFetched: view.Fetched,
			RetryAfter: formatOptionalTime(view.RetryAfter), Attempt: view.Attempt, Message: message,
		})
	})
}

func formatOptionalTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func failedSyncCollectionProgress(collections []syncCollectionResult) []service.ProgressEvent {
	events := []service.ProgressEvent{}
	for _, collection := range collections {
		failed := 0
		if collection.Result != nil {
			failed = collection.Result.FailureCount
			if failed == 0 {
				failed = len(collection.Result.Failures)
			}
		}
		if collection.Err != nil && failed == 0 {
			failed = 1
		}
		if failed > 0 {
			events = append(events, service.ProgressEvent{Type: JobStatusFailed, Phase: JobStatusFailed, Collection: collection.RemoteType, RecordsFailed: failed, Message: "collection sync failed"})
		}
	}
	return events
}

type syncCollectionResult struct {
	RemoteType string
	Result     *service.SyncResourcesResult
	Err        error
}

func recordMaintenanceSyncFrontiers(ctx context.Context, req StartSyncJobRequest, collections []syncCollectionResult) error {
	store, err := cache.NewSQLiteStore(ctx, req.CachePath)
	if err != nil {
		return err
	}
	defer store.Close()
	existing := map[string]cache.MaintenanceFrontier{}
	if frontiers, listErr := store.ListMaintenanceFrontiers(ctx, req.RepoID); listErr == nil {
		for _, frontier := range frontiers {
			existing[frontier.RemoteType+"\x00"+frontier.Lane] = frontier
		}
	}
	for _, collection := range collections {
		status := "fresh"
		if req.Lane == "head" && (collection.Result == nil || collection.Result.TraversalStatus != "complete") {
			status = "partial"
		} else if req.Lane == "tail" {
			status = "backfilling"
			if collection.Err == nil && collection.Result != nil && collection.Result.TraversalStatus == "complete" {
				status = "complete"
			}
		}
		errorClass := ""
		if collection.Err != nil {
			status = "degraded"
			errorClass = "sync_failed"
			if coded, ok := collection.Err.(interface{ DiagnosticCode() string }); ok {
				errorClass = coded.DiagnosticCode()
			}
		}
		frontier := cache.MaintenanceFrontier{RepoID: req.RepoID, RemoteType: collection.RemoteType, Ordering: "updated_at_desc", FilterKey: "all", Lane: req.Lane, Status: status, LastErrorClass: errorClass, UpdatedAt: time.Now().UTC()}
		previous := existing[collection.RemoteType+"\x00"+req.Lane]
		frontier.HighUpdatedAt = previous.HighUpdatedAt
		frontier.HighRemoteID = previous.HighRemoteID
		frontier.HighNumber = previous.HighNumber
		if collection.Result != nil {
			frontier.StopReason = collection.Result.StopReason
			frontier.PagesListed = collection.Result.PagesListed
			frontier.RecordsListed = collection.Result.RecordsListed
			observeMaintenanceResultHigh(&frontier, collection.Result.Results)
		}
		frontier.Checkpoint = nextMaintenanceCheckpoint(req, collection)
		if req.Lane == "tail" && frontier.Status == "backfilling" {
			if checkpointPage(previous.Checkpoint) > checkpointPage(frontier.Checkpoint) {
				frontier.Checkpoint = previous.Checkpoint
			}
		}
		if collection.Err == nil && collection.Result != nil && collection.Result.TraversalStatus == "complete" && (collection.RemoteType == "issue" || collection.RemoteType == "pull_request") {
			filterKey := "state=all"
			current, ok, err := store.GetSyncFrontier(ctx, req.RepoID, collection.RemoteType, "updated_at_desc", filterKey)
			if err != nil {
				return err
			}
			if ok {
				observeMaintenanceHigh(&frontier, current.HighUpdatedAt, current.HighRemoteID, current.HighNumber)
			}
			if err := store.UpsertSyncFrontier(ctx, cache.SyncFrontier{RepoID: req.RepoID, RemoteType: collection.RemoteType, Ordering: "updated_at_desc", FilterKey: filterKey, Status: "complete", HighUpdatedAt: frontier.HighUpdatedAt, HighRemoteID: frontier.HighRemoteID, HighNumber: frontier.HighNumber, StopReason: collection.Result.StopReason, PagesListed: collection.Result.PagesListed, RecordsListed: collection.Result.RecordsListed, UpdatedAt: frontier.UpdatedAt}); err != nil {
				return err
			}
		}
		if err := store.UpsertMaintenanceFrontier(ctx, frontier); err != nil {
			return err
		}
	}
	return nil
}

func observeMaintenanceResultHigh(frontier *cache.MaintenanceFrontier, results []service.SyncResult) {
	for _, result := range results {
		observeMaintenanceHigh(frontier, result.Record.UpdatedAt, firstNonEmptyString(result.Record.RemoteAlias, result.Record.ID), 0)
	}
}

func observeMaintenanceHigh(frontier *cache.MaintenanceFrontier, updatedAt time.Time, remoteID string, number int) {
	updatedAt = updatedAt.UTC()
	if updatedAt.IsZero() || !frontier.HighUpdatedAt.IsZero() && !updatedAt.After(frontier.HighUpdatedAt) {
		return
	}
	frontier.HighUpdatedAt = updatedAt
	frontier.HighRemoteID = remoteID
	frontier.HighNumber = number
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func checkpointPage(checkpoint string) int {
	page := 0
	_, _ = fmt.Sscanf(checkpoint, "next_page:%d", &page)
	return page
}

func nextMaintenanceCheckpoint(req StartSyncJobRequest, collection syncCollectionResult) string {
	if req.Lane != "head" && req.Lane != "tail" || (collection.Result != nil && collection.Result.TraversalStatus == "complete") {
		return ""
	}
	if req.Lane == "tail" && collection.RemoteType == "issue_comment" {
		return "next_page:1"
	}
	page := req.Page
	if page <= 0 {
		page = 1
	}
	if collection.Err != nil || collection.Result == nil || collection.Result.PagesListed <= 0 {
		return fmt.Sprintf("next_page:%d", page)
	}
	advance := collection.Result.PagesListed
	if advance > 1 {
		advance-- // replay one page so page-number shifts cannot create a boundary gap
	}
	return fmt.Sprintf("next_page:%d", page+advance)
}

func runSync(ctx context.Context, manager Manager, req StartSyncJobRequest, progressCh chan<- service.ProgressEvent) (*service.SyncResourcesResult, []syncCollectionResult, error) {
	store, svc, err := newSyncJobService(ctx, manager, req)
	if err != nil {
		return nil, nil, err
	}
	defer store.Close()
	return runSyncSelections(ctx, svc, syncBulkRequest(req, progressCh), req)
}

func newSyncJobService(ctx context.Context, manager Manager, req StartSyncJobRequest) (*cache.SQLiteStore, *service.Service, error) {
	src := manager.Source
	if src == nil {
		src = config.OSSource{}
	}
	eff, err := effectiveJobConfig(manager, req.CachePath)
	if err != nil {
		return nil, nil, err
	}
	store, err := cache.NewSQLiteStore(ctx, eff.Config.CachePath)
	if err != nil {
		return nil, nil, err
	}
	mode, err := syncJobProviderMode(req)
	if err != nil {
		store.Close()
		return nil, nil, err
	}
	token := ""
	if mode == gitcode.ProviderModeLive {
		secret, _, err := config.DefaultCredentialProvider(src).Resolve(ctx, eff)
		if err != nil {
			store.Close()
			return nil, nil, err
		}
		token = secret.Value()
	}
	svc, err := service.NewWithMode(store, mode, token, service.ServiceConfig{
		BaseURL:         eff.Config.GitCodeBaseURL,
		LockPath:        eff.Config.LockPath,
		Timeout:         eff.Config.DefaultTimeout,
		MaxResponseSize: eff.Config.MaxResponseSize,
		MaxRetries:      eff.Config.MaxRetries,
		RateLimitRPS:    eff.Config.RateLimitRPS,
		RateLimitBurst:  eff.Config.RateLimitBurst,
	})
	if err != nil {
		store.Close()
		return nil, nil, err
	}
	return store, svc, nil
}

func syncBulkRequest(req StartSyncJobRequest, progressCh chan<- service.ProgressEvent) service.BulkSyncRequest {
	bulkReq := service.BulkSyncRequest{RepoID: req.RepoID, IdempotencyKey: strings.TrimSpace(req.IdempotencyKey), Page: req.Page, PerPage: req.PerPage, Bounds: &service.SyncBounds{MaxPages: req.MaxPages, MaxRecords: req.MaxRecords, ProgressChan: progressCh}, ProgressChan: progressCh, IncrementalQueue: strings.TrimSpace(req.Lane) != ""}
	if bulkReq.PerPage <= 0 {
		bulkReq.PerPage = 100
	}
	return bulkReq
}

type bulkSyncService interface {
	BulkSyncIssues(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)
	BulkSyncIssueComments(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)
	BulkSyncWiki(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)
	BulkSyncPullRequests(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)
	BulkSyncPRComments(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)
	BulkSyncAll(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)
}

func runSyncSelections(ctx context.Context, svc bulkSyncService, req service.BulkSyncRequest, sel StartSyncJobRequest) (*service.SyncResourcesResult, []syncCollectionResult, error) {
	if !sel.Issues && !sel.Wiki && !sel.Pulls && !sel.Comments && !sel.IssueComments && !sel.PRComments {
		part, err := svc.BulkSyncAll(ctx, req)
		return part, []syncCollectionResult{{RemoteType: "all", Result: part, Err: err}}, err
	}
	aggregate := &service.SyncResourcesResult{Results: []service.SyncResult{}, Failures: []service.ResourceError{}}
	collections := []syncCollectionResult{}
	var syncErr error
	halted := false
	run := func(remoteType string, fn func(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)) {
		if halted {
			return
		}
		part, err := fn(ctx, req)
		collections = append(collections, syncCollectionResult{RemoteType: remoteType, Result: part, Err: err})
		mergeSyncResources(aggregate, part)
		if err != nil {
			var contention cache.ErrLockContention
			if part == nil && errors.As(err, &contention) {
				syncErr = err
				halted = true
				return
			}
			syncErr = mergeSyncError(syncErr, aggregate, err)
		}
	}
	if sel.Issues {
		run("issue", svc.BulkSyncIssues)
	}
	if sel.IssueComments {
		run("issue_comment", svc.BulkSyncIssueComments)
	}
	if sel.Wiki {
		run("wiki", svc.BulkSyncWiki)
	}
	if sel.Pulls {
		run("pull_request", svc.BulkSyncPullRequests)
	}
	if sel.Comments || sel.PRComments {
		run("pr_comment", svc.BulkSyncPRComments)
	}
	if aggregate.SuccessCount == 0 && aggregate.FailureCount == 0 {
		aggregate.SuccessCount = len(aggregate.Results)
		aggregate.FailureCount = len(aggregate.Failures)
	}
	return aggregate, collections, syncErr
}

func mergeSyncResources(dst *service.SyncResourcesResult, src *service.SyncResourcesResult) {
	if dst == nil || src == nil {
		return
	}
	dst.Results = append(dst.Results, src.Results...)
	dst.Failures = append(dst.Failures, src.Failures...)
	dst.SuccessCount += src.SuccessCount
	dst.FailureCount += src.FailureCount
	dst.PagesListed += src.PagesListed
	dst.RecordsListed += src.RecordsListed
	dst.SkippedByWatermark += src.SkippedByWatermark
	if dst.Ordering == "" {
		dst.Ordering = src.Ordering
	}
	previousTraversal := dst.TraversalStatus
	dst.TraversalStatus = mergeTraversalStatus(previousTraversal, src.TraversalStatus)
	if dst.StopReason == "" || traversalPriority(src.TraversalStatus) > traversalPriority(previousTraversal) {
		dst.StopReason = src.StopReason
	}
	if dst.WatermarkStatus == "" {
		dst.WatermarkStatus = src.WatermarkStatus
	}
	if dst.WatermarkReason == "" {
		dst.WatermarkReason = src.WatermarkReason
	}
	if src.IssueComments != nil {
		queue := *src.IssueComments
		dst.IssueComments = &queue
	}
}

func mergeTraversalStatus(left, right string) string {
	if traversalPriority(right) > traversalPriority(left) {
		return right
	}
	return left
}

func traversalPriority(status string) int {
	switch status {
	case "partial", "deferred", "timeout", "cancelled":
		return 3
	case "bounded":
		return 2
	case "complete":
		return 1
	default:
		return 0
	}
}

func mergeSyncError(existing error, result *service.SyncResourcesResult, err error) error {
	if err == nil {
		return existing
	}
	if existing == nil {
		return err
	}
	if result == nil {
		return existing
	}
	return &service.PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount}
}

func syncJobProviderMode(req StartSyncJobRequest) (gitcode.ProviderMode, error) {
	mode := gitcode.ProviderMode(strings.TrimSpace(req.ProviderMode))
	switch mode {
	case "", gitcode.ProviderModeLive:
		return gitcode.ProviderModeLive, nil
	case gitcode.ProviderModeFixture:
		return gitcode.ProviderModeFixture, nil
	default:
		return "", fmt.Errorf("unsupported sync provider mode %q", req.ProviderMode)
	}
}
