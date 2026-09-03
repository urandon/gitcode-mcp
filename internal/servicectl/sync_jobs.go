package servicectl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
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
	case "repository_binding_changed":
		return "service: repository binding changed after the staged fetch"
	default:
		return "service: selected cache authority is unavailable"
	}
}

func (e CacheWriterIdentityError) DiagnosticCode() string { return e.code }

type StartSyncJobRequest struct {
	RepoID              string `json:"repo_id"`
	ProviderMode        string `json:"provider_mode,omitempty"`
	CachePath           string `json:"cache_path,omitempty"`
	Issues              bool   `json:"issues,omitempty"`
	Wiki                bool   `json:"wiki,omitempty"`
	Pulls               bool   `json:"pulls,omitempty"`
	Comments            bool   `json:"comments,omitempty"`
	IssueComments       bool   `json:"issue_comments,omitempty"`
	PRComments          bool   `json:"pr_comments,omitempty"`
	IdempotencyKey      string `json:"idempotency_key,omitempty"`
	MaxPages            int    `json:"max_pages,omitempty"`
	MaxRecords          int    `json:"max_records,omitempty"`
	PerPage             int    `json:"per_page,omitempty"`
	Page                int    `json:"page,omitempty"`
	CacheUUID           string `json:"cache_uuid,omitempty"`
	RegistrationID      string `json:"registration_id,omitempty"`
	Lane                string `json:"lane,omitempty"`
	collectionPages     map[string]int
	workflowCollections []string
	workflowStart       int
	workflowOutcome     SyncStageWorkflowOutcome
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
	if err != nil {
		status := JobStatusFailed
		health := SyncHealthFailed
		if current, ok := m.Get(jobID); ok {
			health = aggregateSyncCollectionOutcome(current.SyncCollections)
			if health == SyncHealthPartial {
				status = JobStatusSucceeded
			}
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			status = JobStatusCancelled
			health = SyncHealthCancelled
		}
		persistErr := m.updateJobPersisted(jobID, func(job *Job, now time.Time) {
			job.Status = status
			job.SyncHealth = health
			job.UpdatedAt = now
			job.FinishedAt = &now
			job.ErrorClass = maintenanceJobErrorClass(err, "sync_failed")
			if status == JobStatusCancelled {
				job.ErrorClass = "cancelled"
			}
			job.Error = publicMaintenanceJobError(SyncJobType, job.ErrorClass)
			job.Progress = append(job.Progress, failedSyncCollectionProgress(collections)...)
			message := job.Error
			if status == JobStatusSucceeded {
				message = "sync job finished with usable partial results"
			}
			job.Progress = append(job.Progress, service.ProgressEvent{Type: status, Phase: status, Collection: SyncJobType, RecordsListed: result.RecordsListed, RecordsFetched: result.SuccessCount, RecordsFailed: result.FailureCount, Message: message})
			delete(m.cancel, jobID)
		})
		if persistErr == nil {
			m.removeTerminalSyncStages(manager, jobID)
		}
		return
	}
	persistErr := m.updateJobPersisted(jobID, func(job *Job, now time.Time) {
		job.Status = JobStatusSucceeded
		job.SyncHealth = aggregateSyncCollectionOutcome(job.SyncCollections)
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
	if persistErr == nil {
		m.removeTerminalSyncStages(manager, jobID)
	}
}

func (m *JobManager) removeTerminalSyncStages(manager Manager, jobID string) {
	runtimeDir := strings.TrimSpace(manager.RuntimeDir)
	if runtimeDir == "" && strings.TrimSpace(m.snapshotPath) != "" {
		runtimeDir = filepath.Dir(m.snapshotPath)
	}
	if runtimeDir != "" {
		_ = NewSyncStageJournal(runtimeDir, SyncStageLimits{}).RemoveJobStages(jobID)
	}
}

func syncDurableCollections(req StartSyncJobRequest) bool {
	// Every daemon sync uses the same fetch -> durable stage -> atomic commit
	// protocol. Selector combinations must not silently fall back to the legacy
	// writer-held network path.
	return true
}

type durableCollectionBatch struct {
	payload          []byte
	recordCount      int
	checkpoint       string
	providerRevision string
	idempotencyKey   string
	fetchedAt        time.Time
	pagesListed      int
	recordsListed    int
	traversalStatus  string
	nextPage         int
	highUpdatedAt    time.Time
	highRemoteID     string
	highNumber       int
}

type durableCollectionWork struct {
	collection string
	remoteType string
	fetch      func(context.Context) (durableCollectionBatch, error)
	commit     func(context.Context, SyncStageEnvelope) (*service.SyncResourcesResult, error)
}

const (
	durableSyncPayloadMaxBytes  = defaultSyncStageMaxBytes - (512 << 10)
	durableSyncResponseMaxBytes = (defaultSyncStageMaxBytes / 2) - (512 << 10)
)

func validateDurableFetchedPayload(payload []byte, records int) error {
	if records < 0 || records > defaultSyncStageMaxRecords || int64(len(payload)) > defaultSyncStageMaxBytes {
		return ErrSyncStageBound
	}
	return nil
}

func (m *JobManager) runDurableSync(ctx context.Context, manager Manager, jobID string, req StartSyncJobRequest, progressCh chan<- service.ProgressEvent) (*service.SyncResourcesResult, []syncCollectionResult, error) {
	req = normalizeDurableSyncRequest(req)
	store, svc, err := newSyncJobService(ctx, manager, req)
	if err != nil {
		return nil, nil, err
	}
	defer store.Close()
	schema, err := store.SchemaVersion(ctx)
	if err != nil {
		return nil, nil, err
	}
	binding, err := store.ResolveRepositoryBinding(ctx, req.RepoID)
	if err != nil {
		return nil, nil, err
	}
	bindingFingerprint := syncRepositoryBindingFingerprint(binding)
	bulkReq := syncBulkRequest(req, progressCh)
	works := durableCollectionWorks(svc, bulkReq, req)
	aggregate := &service.SyncResourcesResult{Results: []service.SyncResult{}, Failures: []service.ResourceError{}}
	collections := make([]syncCollectionResult, 0, len(works))
	var syncErr error
	workflowCollections := append([]string(nil), req.workflowCollections...)
	if len(workflowCollections) == 0 {
		for _, work := range works {
			workflowCollections = append(workflowCollections, work.collection)
		}
	}
	for offset, work := range works {
		workReq := durableCollectionJobRequest(req, work.remoteType)
		workflow := syncStageWorkflowFromRequest(req, workflowCollections, req.workflowStart+offset)
		privateFrontier := fmt.Sprintf("%s:%d", work.remoteType, workReq.Page)
		result, collection, collectionErr := runSyncCollectionWithRetry(
			ctx, req.CacheUUID, req.RepoID, work.collection, privateFrontier, defaultSyncCollectionRetryBudget,
			func() (*service.SyncResourcesResult, syncCollectionResult, error) {
				return m.runDurableCollection(ctx, manager, jobID, workReq, schema, bindingFingerprint, work, workflow)
			},
			func(view SyncCollectionView) { m.observeSyncCollection(jobID, view) },
			nil,
			nil,
		)
		mergeSyncResources(aggregate, result)
		collections = append(collections, collection)
		syncErr = mergeSyncError(syncErr, result, collectionErr)
		req.workflowOutcome = mergeSyncWorkflowOutcome(req.workflowOutcome, result, collectionErr, work.remoteType)
		if errors.Is(ctx.Err(), context.Canceled) {
			break
		}
	}
	return aggregate, collections, syncErr
}

func (m *JobManager) observeSyncCollection(jobID string, view SyncCollectionView) {
	m.updateJob(jobID, func(job *Job, now time.Time) {
		replaced := false
		for i := range job.SyncCollections {
			if job.SyncCollections[i].Collection == view.Collection {
				job.SyncCollections[i] = view
				replaced = true
				break
			}
		}
		if !replaced {
			job.SyncCollections = append(job.SyncCollections, view)
		}
		job.SyncHealth = aggregateSyncCollectionOutcome(job.SyncCollections)
		job.UpdatedAt = now
		if view.Outcome == SyncCollectionRetryScheduled {
			job.Progress = append(job.Progress, service.ProgressEvent{
				Type: "retry_scheduled", Phase: JobStatusRunning, Collection: view.Collection,
				RecordsListed: view.RecordsListed, RecordsFetched: view.Committed, RecordsFailed: view.Failed,
				RetryAfter: formatOptionalTimePointer(view.RetryAfter), Attempt: view.Attempt,
				Message: "collection retry scheduled",
			})
		}
	})
}

func formatOptionalTimePointer(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func syncStageWorkflowFromRequest(req StartSyncJobRequest, collections []string, current int) *SyncStageWorkflow {
	pages := map[string]int{}
	for remoteType, page := range req.collectionPages {
		if page > 0 {
			pages[remoteType] = page
		}
	}
	workflow := &SyncStageWorkflow{
		Collections: append([]string(nil), collections...), Current: current,
		ProviderMode: req.ProviderMode, RequestIdempotencyKey: req.IdempotencyKey,
		MaxPages: req.MaxPages, MaxRecords: req.MaxRecords, PerPage: req.PerPage, Page: req.Page,
		Lane: req.Lane, CollectionPages: pages,
	}
	if req.workflowOutcome != (SyncStageWorkflowOutcome{}) {
		outcome := req.workflowOutcome
		workflow.Outcome = &outcome
	}
	return workflow
}

func syncRequestFromWorkflow(stage SyncStageEnvelope) (StartSyncJobRequest, bool) {
	workflow := stage.Workflow
	if !workflow.hasRemaining() {
		return StartSyncJobRequest{}, false
	}
	req := StartSyncJobRequest{
		RepoID: stage.RepoID, ProviderMode: workflow.ProviderMode, CachePath: stage.CachePath,
		IdempotencyKey: workflow.RequestIdempotencyKey, MaxPages: workflow.MaxPages,
		MaxRecords: workflow.MaxRecords, PerPage: workflow.PerPage, Page: workflow.Page,
		CacheUUID: stage.CacheUUID, RegistrationID: stage.RegistrationID, Lane: workflow.Lane,
		collectionPages:     appendSyncCollectionPages(workflow.CollectionPages),
		workflowCollections: append([]string(nil), workflow.Collections...), workflowStart: workflow.Current + 1,
	}
	if workflow.Outcome != nil {
		req.workflowOutcome = *workflow.Outcome
	}
	for _, collection := range workflow.Collections[workflow.Current+1:] {
		switch collection {
		case "issues":
			req.Issues = true
		case "issue_comments":
			req.IssueComments = true
		case "wiki":
			req.Wiki = true
		case "pulls":
			req.Pulls = true
		case "pr_comments":
			req.PRComments = true
		}
	}
	return req, true
}

func mergeSyncWorkflowOutcome(previous SyncStageWorkflowOutcome, result *service.SyncResourcesResult, err error, remoteType string) SyncStageWorkflowOutcome {
	if result != nil {
		previous.RecordsListed += result.RecordsListed
		previous.SuccessCount += result.SuccessCount
		previous.FailureCount += result.FailureCount
	}
	previous.ErrorClass, previous.ErrorCollection = mergeSyncWorkflowError(previous.ErrorClass, previous.ErrorCollection, err, remoteType)
	return previous
}

func mergeSyncWorkflowError(previousClass, previousCollection string, err error, remoteType string) (string, string) {
	if err == nil {
		return previousClass, previousCollection
	}
	current := maintenanceJobErrorClass(err, "sync_failed")
	if previousClass == "" {
		return current, strings.TrimSpace(remoteType)
	}
	if previousClass == current {
		return previousClass, previousCollection
	}
	return "sync_failed", firstNonEmpty(previousCollection, strings.TrimSpace(remoteType))
}

type recoveredSyncWorkflowError struct{ code string }

func (e recoveredSyncWorkflowError) Error() string {
	return "recovered sync workflow contains a failed collection"
}
func (e recoveredSyncWorkflowError) DiagnosticCode() string {
	return sanitizeMaintenanceErrorClass(e.code, "sync_failed")
}

func recoveredSyncWorkflowOutcome(stage SyncStageEnvelope) (*service.SyncResourcesResult, error, []syncCollectionResult) {
	if stage.Workflow == nil || stage.Workflow.Outcome == nil {
		return &service.SyncResourcesResult{RecordsListed: stage.RecordCount, SuccessCount: stage.RecordCount}, nil, nil
	}
	outcome := *stage.Workflow.Outcome
	result := &service.SyncResourcesResult{
		RecordsListed: outcome.RecordsListed,
		SuccessCount:  outcome.SuccessCount,
		FailureCount:  outcome.FailureCount,
	}
	if outcome.ErrorClass == "" {
		return result, nil, nil
	}
	err := recoveredSyncWorkflowError{code: outcome.ErrorClass}
	collections := []syncCollectionResult{{RemoteType: firstNonEmpty(outcome.ErrorCollection, stage.Collection), Err: err}}
	return result, err, collections
}

func appendSyncCollectionPages(source map[string]int) map[string]int {
	if len(source) == 0 {
		return nil
	}
	result := make(map[string]int, len(source))
	for remoteType, page := range source {
		result[remoteType] = page
	}
	return result
}

func durableCollectionJobRequest(req StartSyncJobRequest, remoteType string) StartSyncJobRequest {
	request := req
	if page := req.collectionPages[remoteType]; page > 0 {
		request.Page = page
	}
	return request
}

func normalizeDurableSyncRequest(req StartSyncJobRequest) StartSyncJobRequest {
	if !req.Issues && !req.Wiki && !req.Pulls && !req.Comments && !req.IssueComments && !req.PRComments {
		// Preserve the historical default selection of BulkSyncAll.
		req.Issues, req.Wiki = true, true
	}
	if req.Comments {
		req.PRComments = true
	}
	return req
}

func durableCollectionWorks(svc *service.Service, bulkReq service.BulkSyncRequest, req StartSyncJobRequest) []durableCollectionWork {
	works := make([]durableCollectionWork, 0, 5)
	if req.Issues {
		works = append(works, durableCollectionWork{
			collection: "issues", remoteType: "issue",
			fetch: func(ctx context.Context) (durableCollectionBatch, error) {
				batch, err := svc.FetchIssueSyncBatch(ctx, durableCollectionRequest(bulkReq, req, "issue"))
				payload, marshalErr := json.Marshal(batch)
				if marshalErr != nil {
					return durableCollectionBatch{}, marshalErr
				}
				if boundErr := validateDurableFetchedPayload(payload, batch.RecordCount()); boundErr != nil {
					return durableCollectionBatch{}, boundErr
				}
				return durableCollectionBatch{payload: payload, recordCount: batch.RecordCount(), checkpoint: batch.StopReason, providerRevision: batch.HighUpdatedAt.Format(time.RFC3339Nano), idempotencyKey: batch.IdempotencyKey, fetchedAt: batch.FetchedAt, pagesListed: batch.PagesListed, recordsListed: batch.RecordsListed, traversalStatus: batch.TraversalStatus, highUpdatedAt: batch.HighUpdatedAt, highRemoteID: batch.HighRemoteID, highNumber: batch.HighNumber}, err
			},
			commit: func(ctx context.Context, stage SyncStageEnvelope) (*service.SyncResourcesResult, error) {
				var batch service.DurableIssueSyncBatch
				if err := json.Unmarshal(stage.Payload, &batch); err != nil {
					return nil, err
				}
				batch.MaintenanceFrontier, batch.CommitReceipt = stage.MaintenanceFrontier, syncStageCommitReceipt(stage)
				return svc.CommitIssueSyncBatch(ctx, batch, bulkReq.ProgressChan)
			},
		})
	}
	if req.IssueComments {
		works = append(works, durableCollectionWork{
			collection: "issue_comments", remoteType: "issue_comment",
			fetch: func(ctx context.Context) (durableCollectionBatch, error) {
				batch, err := svc.FetchIssueCommentSyncBatch(ctx, durableCollectionRequest(bulkReq, req, "issue_comment"))
				payload, marshalErr := json.Marshal(batch)
				if marshalErr != nil {
					return durableCollectionBatch{}, marshalErr
				}
				if boundErr := validateDurableFetchedPayload(payload, batch.RecordCount()); boundErr != nil {
					return durableCollectionBatch{}, boundErr
				}
				return durableCollectionBatch{payload: payload, recordCount: batch.RecordCount(), checkpoint: batch.StopReason, providerRevision: batch.ProviderRevision, idempotencyKey: batch.IdempotencyKey, fetchedAt: batch.FetchedAt, pagesListed: batch.PagesListed, recordsListed: batch.RecordsListed, traversalStatus: batch.TraversalStatus}, err
			},
			commit: func(ctx context.Context, stage SyncStageEnvelope) (*service.SyncResourcesResult, error) {
				var batch service.DurableIssueCommentSyncBatch
				if err := json.Unmarshal(stage.Payload, &batch); err != nil {
					return nil, err
				}
				batch.MaintenanceFrontier, batch.CommitReceipt = stage.MaintenanceFrontier, syncStageCommitReceipt(stage)
				return svc.CommitIssueCommentSyncBatch(ctx, batch, bulkReq.ProgressChan)
			},
		})
	}
	if req.Wiki {
		works = append(works, durableCollectionWork{
			collection: "wiki", remoteType: "wiki",
			fetch: func(ctx context.Context) (durableCollectionBatch, error) {
				batch, err := svc.FetchWikiSyncBatch(ctx, durableCollectionRequest(bulkReq, req, "wiki"))
				payload, marshalErr := json.Marshal(batch)
				if marshalErr != nil {
					return durableCollectionBatch{}, marshalErr
				}
				if boundErr := validateDurableFetchedPayload(payload, batch.RecordCount()); boundErr != nil {
					return durableCollectionBatch{}, boundErr
				}
				return durableCollectionBatch{payload: payload, recordCount: batch.RecordCount(), checkpoint: batch.StopReason, providerRevision: batch.ProviderRevision, idempotencyKey: batch.IdempotencyKey, fetchedAt: batch.FetchedAt, pagesListed: batch.PagesListed, recordsListed: batch.RecordsListed, traversalStatus: batch.TraversalStatus, nextPage: batch.NextPage}, err
			},
			commit: func(ctx context.Context, stage SyncStageEnvelope) (*service.SyncResourcesResult, error) {
				var batch service.DurableWikiSyncBatch
				if err := json.Unmarshal(stage.Payload, &batch); err != nil {
					return nil, err
				}
				batch.MaintenanceFrontier, batch.CommitReceipt = stage.MaintenanceFrontier, syncStageCommitReceipt(stage)
				return svc.CommitWikiSyncBatch(ctx, batch, bulkReq.ProgressChan)
			},
		})
	}
	if req.Pulls {
		works = append(works, durableCollectionWork{
			collection: "pulls", remoteType: "pull_request",
			fetch: func(ctx context.Context) (durableCollectionBatch, error) {
				batch, err := svc.FetchPullSyncBatch(ctx, durableCollectionRequest(bulkReq, req, "pull_request"))
				payload, marshalErr := json.Marshal(batch)
				if marshalErr != nil {
					return durableCollectionBatch{}, marshalErr
				}
				if boundErr := validateDurableFetchedPayload(payload, batch.RecordCount()); boundErr != nil {
					return durableCollectionBatch{}, boundErr
				}
				return durableCollectionBatch{payload: payload, recordCount: batch.RecordCount(), checkpoint: batch.StopReason, providerRevision: batch.HighUpdatedAt.Format(time.RFC3339Nano), idempotencyKey: batch.IdempotencyKey, fetchedAt: batch.FetchedAt, pagesListed: batch.PagesListed, recordsListed: batch.RecordsListed, traversalStatus: batch.TraversalStatus, highUpdatedAt: batch.HighUpdatedAt, highRemoteID: batch.HighRemoteID, highNumber: batch.HighNumber}, err
			},
			commit: func(ctx context.Context, stage SyncStageEnvelope) (*service.SyncResourcesResult, error) {
				var batch service.DurablePullSyncBatch
				if err := json.Unmarshal(stage.Payload, &batch); err != nil {
					return nil, err
				}
				batch.MaintenanceFrontier, batch.CommitReceipt = stage.MaintenanceFrontier, syncStageCommitReceipt(stage)
				return svc.CommitPullSyncBatch(ctx, batch, bulkReq.ProgressChan)
			},
		})
	}
	if req.PRComments {
		works = append(works, durableCollectionWork{
			collection: "pr_comments", remoteType: "pr_comment",
			fetch: func(ctx context.Context) (durableCollectionBatch, error) {
				batch, err := svc.FetchPRCommentSyncBatch(ctx, durableCollectionRequest(bulkReq, req, "pr_comment"))
				payload, marshalErr := json.Marshal(batch)
				if marshalErr != nil {
					return durableCollectionBatch{}, marshalErr
				}
				if boundErr := validateDurableFetchedPayload(payload, batch.RecordCount()); boundErr != nil {
					return durableCollectionBatch{}, boundErr
				}
				return durableCollectionBatch{payload: payload, recordCount: batch.RecordCount(), checkpoint: batch.StopReason, providerRevision: batch.ProviderRevision, idempotencyKey: batch.IdempotencyKey, fetchedAt: batch.FetchedAt, pagesListed: batch.PagesListed, recordsListed: batch.RecordsListed, traversalStatus: batch.TraversalStatus}, err
			},
			commit: func(ctx context.Context, stage SyncStageEnvelope) (*service.SyncResourcesResult, error) {
				var batch service.DurablePRCommentSyncBatch
				if err := json.Unmarshal(stage.Payload, &batch); err != nil {
					return nil, err
				}
				batch.MaintenanceFrontier, batch.CommitReceipt = stage.MaintenanceFrontier, syncStageCommitReceipt(stage)
				return svc.CommitPRCommentSyncBatch(ctx, batch, bulkReq.ProgressChan)
			},
		})
	}
	return works
}

func durableCollectionRequest(base service.BulkSyncRequest, req StartSyncJobRequest, remoteType string) service.BulkSyncRequest {
	request := base
	if page := req.collectionPages[remoteType]; page > 0 {
		request.Page = page
	}
	return request
}

func syncRepositoryBindingFingerprint(binding cache.RepositoryBinding) string {
	scopes := make([]string, 0, len(binding.Scopes))
	for _, scope := range binding.Scopes {
		scopes = append(scopes, string(scope))
	}
	sort.Strings(scopes)
	data, _ := json.Marshal(struct {
		RepoID     string   `json:"repo_id"`
		Owner      string   `json:"owner"`
		Name       string   `json:"name"`
		APIBaseURL string   `json:"api_base_url"`
		Scopes     []string `json:"scopes"`
	}{strings.TrimSpace(binding.RepoID), strings.TrimSpace(binding.Owner), strings.TrimSpace(binding.Name), strings.TrimSpace(binding.APIBaseURL), scopes})
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func syncStageCommitReceipt(stage SyncStageEnvelope) *cache.SyncCommitReceipt {
	return &cache.SyncCommitReceipt{StageID: stage.StageID, Checksum: stage.Checksum, RepoID: stage.RepoID, Collection: stage.Collection, CommittedAt: time.Now().UTC()}
}

func syncStageReceiptExists(ctx context.Context, stage SyncStageEnvelope) (bool, error) {
	store, err := cache.NewSQLiteReadOnlyStore(ctx, stage.CachePath)
	if err != nil {
		return false, err
	}
	defer store.Close()
	receipt, ok, err := store.GetSyncCommitReceipt(ctx, stage.StageID)
	if err != nil || !ok {
		return false, err
	}
	return receipt.Checksum == stage.Checksum && receipt.RepoID == stage.RepoID && receipt.Collection == stage.Collection, nil
}

func stagedMaintenanceFrontier(ctx context.Context, req StartSyncJobRequest, remoteType string, batch durableCollectionBatch) (*cache.MaintenanceFrontier, error) {
	if strings.TrimSpace(req.Lane) == "" {
		return nil, nil
	}
	store, err := cache.NewSQLiteReadOnlyStore(ctx, req.CachePath)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	var previous cache.MaintenanceFrontier
	frontiers, err := store.ListMaintenanceFrontiers(ctx, req.RepoID)
	if err != nil {
		return nil, err
	}
	for _, candidate := range frontiers {
		if candidate.RemoteType == remoteType && candidate.Lane == req.Lane {
			previous = candidate
			break
		}
	}
	status := "fresh"
	if req.Lane == "tail" {
		status = "backfilling"
		if batch.traversalStatus == "complete" {
			status = "complete"
		}
	} else if batch.traversalStatus != "complete" {
		status = "partial"
	}
	result := &service.SyncResourcesResult{PagesListed: batch.pagesListed, RecordsListed: batch.recordsListed, StopReason: batch.checkpoint, TraversalStatus: batch.traversalStatus}
	collection := syncCollectionResult{RemoteType: remoteType, Result: result}
	frontier := &cache.MaintenanceFrontier{
		RepoID: req.RepoID, RemoteType: remoteType, Ordering: "updated_at_desc", FilterKey: "all", Lane: req.Lane,
		Status: status, HighUpdatedAt: previous.HighUpdatedAt, HighRemoteID: previous.HighRemoteID, HighNumber: previous.HighNumber,
		StopReason: batch.checkpoint, PagesListed: batch.pagesListed, RecordsListed: batch.recordsListed,
		Checkpoint: nextMaintenanceCheckpoint(req, collection), UpdatedAt: time.Now().UTC(),
	}
	if batch.nextPage > 0 && (req.Lane == "head" || req.Lane == "tail") {
		frontier.Checkpoint = fmt.Sprintf("next_page:%d", batch.nextPage)
	}
	observeMaintenanceHigh(frontier, batch.highUpdatedAt, batch.highRemoteID, batch.highNumber)
	if req.Lane == "tail" && status == "backfilling" && checkpointPage(previous.Checkpoint) > checkpointPage(frontier.Checkpoint) {
		frontier.Checkpoint = previous.Checkpoint
	}
	return frontier, nil
}

func (m *JobManager) runDurableCollection(ctx context.Context, manager Manager, jobID string, req StartSyncJobRequest, schema int, bindingFingerprint string, work durableCollectionWork, workflow *SyncStageWorkflow) (*service.SyncResourcesResult, syncCollectionResult, error) {
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
	if workflow != nil {
		outcome := SyncStageWorkflowOutcome{}
		if workflow.Outcome != nil {
			outcome = *workflow.Outcome
		}
		outcome.RecordsListed += batch.recordsListed
		outcome.SuccessCount += batch.recordCount
		outcome.ErrorClass, outcome.ErrorCollection = mergeSyncWorkflowError(outcome.ErrorClass, outcome.ErrorCollection, fetchErr, work.remoteType)
		workflow.Outcome = &outcome
	}
	runtimeDir := strings.TrimSpace(manager.RuntimeDir)
	if runtimeDir == "" && strings.TrimSpace(m.snapshotPath) != "" {
		runtimeDir = filepath.Dir(m.snapshotPath)
	}
	journal := NewSyncStageJournal(runtimeDir, SyncStageLimits{})
	frontier, frontierErr := stagedMaintenanceFrontier(ctx, req, work.remoteType, batch)
	if frontierErr != nil {
		m.rejectUnpersistedSyncStage(jobID, maintenanceJobErrorClass(frontierErr, "frontier_prepare_failed"))
		collection := syncCollectionResult{RemoteType: work.remoteType, Err: frontierErr}
		return nil, collection, frontierErr
	}
	stage, err := journal.Create(SyncStageEnvelope{
		JobID: jobID, CacheUUID: req.CacheUUID, CacheSchema: schema, CachePath: req.CachePath, RegistrationID: req.RegistrationID,
		RepoID: req.RepoID, BindingFingerprint: bindingFingerprint, Collection: work.collection, Checkpoint: batch.checkpoint,
		ProviderRevision: batch.providerRevision,
		IdempotencyKey:   batch.idempotencyKey, RecordCount: batch.recordCount, Payload: batch.payload,
		MaintenanceFrontier: frontier,
		Workflow:            workflow,
		State:               SyncStageState{Phase: SyncStageStaged, RetryBudget: defaultSyncCommitRetries, FetchedAt: batch.fetchedAt},
	})
	if err != nil {
		m.rejectUnpersistedSyncStage(jobID, maintenanceJobErrorClass(err, "stage_persist_failed"))
		collection := syncCollectionResult{RemoteType: work.remoteType, Err: err}
		return nil, collection, err
	}
	defer func() { _, _ = journal.GC() }()
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
		reservedStage := stage
		reservedStage.State = state
		reservation, blockingOp, blockingRef, reserved, reserveErr := m.tryReserveSyncCommit(jobID, reservedStage, "staged batch commit reserved")
		if reserveErr != nil {
			releaseTurn()
			collection := syncCollectionResult{RemoteType: work.remoteType, Err: reserveErr}
			return nil, collection, reserveErr
		}
		if !reserved {
			releaseTurn()
			state, retry := nextSyncCommitRetry(stage.StageID, stage.State, time.Now().UTC())
			state.BlockingOp, state.BlockingJobRef = blockingOp, blockingRef
			stage, err = journal.UpdateState(stage.StageID, state)
			if err != nil || !retry {
				if err == nil {
					err = ErrCacheWriterBusy{ActiveJobID: blockingRef, ActiveType: blockingOp}
				}
				collection := syncCollectionResult{RemoteType: work.remoteType, Err: err}
				return nil, collection, err
			}
			m.setJobSyncStage(jobID, stage, "waiting for admitted cache writer")
			if err := waitForSyncRetry(ctx, stage.State.RetryAfter); err != nil {
				stage = rejectCancelledSyncStage(journal, stage)
				m.setJobSyncStage(jobID, stage, "staged batch cancelled")
				collection := syncCollectionResult{RemoteType: work.remoteType, Err: err}
				return nil, collection, err
			}
			continue
		}
		stage, err = journal.UpdateState(stage.StageID, state)
		if err != nil {
			m.rollbackSyncCommitReservation(jobID, reservation)
			releaseTurn()
			collection := syncCollectionResult{RemoteType: work.remoteType, Err: err}
			return nil, collection, err
		}
		m.setJobSyncStage(jobID, stage, "staged batch commit started")
		result, commitErr := work.commit(ctx, stage)
		releaseTurn()
		if commitErr == nil {
			state = stage.State
			state.Phase = SyncStageCommitted
			state.CommittedAt = time.Now().UTC()
			state.BlockerClass, state.BlockingOp, state.BlockingJobRef = "", "", ""
			stage, err = journal.UpdateState(stage.StageID, state)
			if err != nil {
				if committed, receiptErr := syncStageReceiptExists(context.Background(), stage); receiptErr == nil && committed {
					stage.State = state
					m.setJobSyncStage(jobID, stage, "staged batch committed; journal terminal write deferred")
					collection := syncCollectionResult{RemoteType: work.remoteType, Result: result, Err: fetchErr}
					return result, collection, fetchErr
				}
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
	if syncRepositoryBindingFingerprint(binding) != stage.BindingFingerprint {
		return CacheWriterIdentityError{code: "repository_binding_changed"}
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
	defer func() { _, _ = journal.GC() }()
	stages, rejections, err := journal.ListForRecovery()
	if err != nil {
		return err
	}
	for _, rejection := range rejections {
		m.rejectInterruptedSyncStageByRef(rejection.StageRef, rejection.Reason)
	}
	// A workflow may retain its last committed checkpoint and a newer staged
	// collection. Recover only the furthest durable collection so an older
	// receipt cannot start duplicate remaining work.
	latest := map[string]SyncStageEnvelope{}
	for _, stage := range stages {
		current, ok := latest[stage.JobID]
		if !ok || laterSyncWorkflowStage(stage, current) {
			latest[stage.JobID] = stage
		}
	}
	stages = stages[:0]
	for _, stage := range latest {
		stages = append(stages, stage)
	}
	sort.Slice(stages, func(i, j int) bool { return stages[i].State.UpdatedAt.Before(stages[j].State.UpdatedAt) })
	now := time.Now().UTC()
	for _, stage := range stages {
		job, ok := m.Get(stage.JobID)
		if ok && job.Type == SyncJobType && job.Status != JobStatusInterrupted {
			if jobTerminalStatus(job.Status) {
				_ = journal.RemoveJobStages(stage.JobID)
			}
			continue
		}
		if stage.State.Phase == SyncStageCommitted {
			if !ok || job.Type != SyncJobType || job.Status != JobStatusInterrupted {
				continue
			}
			if err := m.resumeCommittedSyncWorkflow(ctx, manager, journal, stage); err != nil {
				return err
			}
			continue
		}
		if stage.State.Phase == SyncStageRejected || stage.State.Phase == SyncStageSuperseded {
			m.rejectInterruptedSyncStage(stage, firstNonEmpty(stage.State.TerminalReason, string(stage.State.Phase)))
			continue
		}
		if committed, receiptErr := syncStageReceiptExists(ctx, stage); receiptErr == nil && committed {
			state := stage.State
			state.Phase, state.CommittedAt, state.RetryAfter = SyncStageCommitted, now, time.Time{}
			if updated, updateErr := journal.UpdateState(stage.StageID, state); updateErr == nil {
				stage = updated
			} else {
				stage.State = state
			}
			if err := m.resumeCommittedSyncWorkflow(ctx, manager, journal, stage); err != nil {
				return err
			}
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
		if !supportedDurableSyncCollection(stage.Collection) {
			state := stage.State
			state.Phase, state.TerminalReason = SyncStageRejected, "unsupported_stage_collection"
			updated, err := journal.UpdateState(stage.StageID, state)
			if err != nil {
				return err
			}
			m.rejectInterruptedSyncStage(updated, state.TerminalReason)
			continue
		}
		job, ok = m.Get(stage.JobID)
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

func laterSyncWorkflowStage(candidate, current SyncStageEnvelope) bool {
	candidateIndex, currentIndex := -1, -1
	if candidate.Workflow != nil {
		candidateIndex = candidate.Workflow.Current
	}
	if current.Workflow != nil {
		currentIndex = current.Workflow.Current
	}
	if candidateIndex != currentIndex {
		return candidateIndex > currentIndex
	}
	if !candidate.State.UpdatedAt.Equal(current.State.UpdatedAt) {
		return candidate.State.UpdatedAt.After(current.State.UpdatedAt)
	}
	return candidate.StageID > current.StageID
}

func (m *JobManager) resumeCommittedSyncWorkflow(ctx context.Context, manager Manager, journal *SyncStageJournal, stage SyncStageEnvelope) error {
	if _, remaining := syncRequestFromWorkflow(stage); !remaining {
		if err := m.completeInterruptedSyncStageFromReceipt(stage); err != nil {
			return err
		}
		return journal.RemoveJobStages(stage.JobID)
	}
	workerCtx, cancel := context.WithCancel(ctx)
	if err := m.resumeInterruptedSyncStage(stage, cancel); err != nil {
		cancel()
		return err
	}
	go m.runRecoveredSyncWorkflow(workerCtx, manager, journal, stage, false)
	return nil
}

func (m *JobManager) completeInterruptedSyncStageFromReceipt(stage SyncStageEnvelope) error {
	view := stage.PublicView()
	result, outcomeErr, _ := recoveredSyncWorkflowOutcome(stage)
	return m.updateJobPersisted(stage.JobID, func(job *Job, now time.Time) {
		if job.Type != SyncJobType {
			return
		}
		job.Status, job.Error, job.ErrorClass = JobStatusSucceeded, "", ""
		if outcomeErr != nil {
			job.Status = JobStatusFailed
			job.ErrorClass = maintenanceJobErrorClass(outcomeErr, "sync_failed")
			job.Error = publicMaintenanceJobError(SyncJobType, job.ErrorClass)
		}
		job.UpdatedAt, job.FinishedAt, job.SyncStage = now, &now, &view
		job.Steps, job.Completed = result.RecordsListed, result.SuccessCount
		job.Progress = append(job.Progress, service.ProgressEvent{Type: job.Status, Phase: job.Status, Collection: stage.Collection, RecordsListed: result.RecordsListed, RecordsFetched: result.SuccessCount, RecordsFailed: result.FailureCount, Message: firstNonEmpty(job.Error, "cache commit receipt recovered after journal interruption")})
		delete(m.cancel, job.ID)
	})
}

func supportedDurableSyncCollection(collection string) bool {
	switch collection {
	case "issues", "issue_comments", "wiki", "pulls", "pr_comments":
		return true
	default:
		return false
	}
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
	m.runRecoveredSyncWorkflow(ctx, manager, journal, stage, true)
}

func (m *JobManager) runRecoveredSyncWorkflow(ctx context.Context, manager Manager, journal *SyncStageJournal, stage SyncStageEnvelope, commitCurrent bool) {
	defer func() { _, _ = journal.GC() }()
	defer m.markWorkerFinished(stage.JobID)
	result, prefixErr, collections := recoveredSyncWorkflowOutcome(stage)
	finalStage := stage
	var commitErr error
	if commitCurrent {
		commitResult, committedStage, err := m.commitRecoveredSyncStage(ctx, manager, journal, stage)
		finalStage, commitErr = committedStage, err
		if stage.Workflow == nil || stage.Workflow.Outcome == nil {
			result = commitResult
		}
	}
	if result == nil {
		result = &service.SyncResourcesResult{}
	}
	err := commitErr
	if err == nil {
		err = prefixErr
		if req, remaining := syncRequestFromWorkflow(finalStage); remaining {
			remainingResult, remainingCollections, remainingErr := m.runDurableSync(ctx, manager, stage.JobID, req, nil)
			mergeSyncResources(result, remainingResult)
			collections = append(collections, remainingCollections...)
			if remainingErr != nil {
				if err == nil {
					err = remainingErr
				} else {
					err = recoveredSyncWorkflowError{code: "sync_failed"}
				}
			}
		}
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
	if current, ok := m.Get(stage.JobID); ok && current.SyncStage != nil {
		view = *current.SyncStage
	}
	persistErr := m.updateJobPersisted(stage.JobID, func(job *Job, now time.Time) {
		job.Status, job.ErrorClass, job.Error = status, errorClass, publicError
		job.UpdatedAt, job.FinishedAt, job.SyncStage = now, &now, &view
		job.Steps, job.Completed = result.RecordsListed, result.SuccessCount
		job.Progress = append(job.Progress, failedSyncCollectionProgress(collections)...)
		job.Progress = append(job.Progress, service.ProgressEvent{Type: status, Phase: status, Collection: SyncJobType, RecordsListed: result.RecordsListed, RecordsFetched: result.SuccessCount, RecordsFailed: result.FailureCount, Message: map[bool]string{true: "recovered sync workflow finished", false: publicError}[err == nil]})
		delete(m.cancel, job.ID)
	})
	if persistErr == nil {
		_ = journal.RemoveJobStages(stage.JobID)
	}
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
			batch.MaintenanceFrontier, batch.CommitReceipt = stage.MaintenanceFrontier, syncStageCommitReceipt(stage)
			return svc.CommitIssueSyncBatch(ctx, batch, nil)
		case "wiki":
			var batch service.DurableWikiSyncBatch
			if err := json.Unmarshal(stage.Payload, &batch); err != nil {
				return nil, err
			}
			batch.MaintenanceFrontier, batch.CommitReceipt = stage.MaintenanceFrontier, syncStageCommitReceipt(stage)
			return svc.CommitWikiSyncBatch(ctx, batch, nil)
		case "pulls":
			var batch service.DurablePullSyncBatch
			if err := json.Unmarshal(stage.Payload, &batch); err != nil {
				return nil, err
			}
			batch.MaintenanceFrontier, batch.CommitReceipt = stage.MaintenanceFrontier, syncStageCommitReceipt(stage)
			return svc.CommitPullSyncBatch(ctx, batch, nil)
		case "issue_comments":
			var batch service.DurableIssueCommentSyncBatch
			if err := json.Unmarshal(stage.Payload, &batch); err != nil {
				return nil, err
			}
			batch.MaintenanceFrontier, batch.CommitReceipt = stage.MaintenanceFrontier, syncStageCommitReceipt(stage)
			return svc.CommitIssueCommentSyncBatch(ctx, batch, nil)
		case "pr_comments":
			var batch service.DurablePRCommentSyncBatch
			if err := json.Unmarshal(stage.Payload, &batch); err != nil {
				return nil, err
			}
			batch.MaintenanceFrontier, batch.CommitReceipt = stage.MaintenanceFrontier, syncStageCommitReceipt(stage)
			return svc.CommitPRCommentSyncBatch(ctx, batch, nil)
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
		reservedStage := stage
		reservedStage.State = state
		reservation, blockingOp, blockingRef, reserved, reserveErr := m.tryReserveSyncCommit(stage.JobID, reservedStage, "recovered staged batch commit reserved")
		if reserveErr != nil {
			releaseTurn()
			return nil, stage, reserveErr
		}
		if !reserved {
			releaseTurn()
			state, retry := nextSyncCommitRetry(stage.StageID, stage.State, time.Now().UTC())
			state.BlockingOp, state.BlockingJobRef = blockingOp, blockingRef
			stage, err = journal.UpdateState(stage.StageID, state)
			if err != nil {
				return nil, stage, err
			}
			if !retry {
				return nil, stage, ErrCacheWriterBusy{ActiveJobID: blockingRef, ActiveType: blockingOp}
			}
			m.setJobSyncStage(stage.JobID, stage, "recovered stage waiting for admitted cache writer")
			if err := waitForSyncRetry(ctx, stage.State.RetryAfter); err != nil {
				return nil, stage, err
			}
			continue
		}
		stage, err = journal.UpdateState(stage.StageID, state)
		if err != nil {
			m.rollbackSyncCommitReservation(stage.JobID, reservation)
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
			} else if committed, receiptErr := syncStageReceiptExists(context.Background(), stage); receiptErr == nil && committed {
				stage.State = state
				err = nil
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

type syncCommitReservation struct {
	previous         *SyncStageView
	previousProgress []service.ProgressEvent
	stageRef         string
}

// tryReserveSyncCommit combines the external-writer comparison and public
// committing transition under the JobManager lock. Writer admission therefore
// observes either the pre-commit stage or the exclusive committing lease,
// never the gap between two separate operations.
func (m *JobManager) tryReserveSyncCommit(jobID string, stage SyncStageEnvelope, message string) (syncCommitReservation, string, string, bool, error) {
	if stage.State.Phase != SyncStageCommitting {
		return syncCommitReservation{}, "", "", false, fmt.Errorf("reserve sync commit: %w", ErrSyncStageCorrupt)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if writerID := m.directCacheWriters[stage.CacheUUID]; writerID != "" && writerID != jobID {
		return syncCommitReservation{}, "direct_cache_write", writerID, false, nil
	}
	for id, candidate := range m.jobs {
		if id == jobID || candidate.CacheUUID != stage.CacheUUID || !isCacheWriterJob(candidate.Type) {
			continue
		}
		if candidate.Type == SyncJobType && (candidate.SyncStage == nil || candidate.SyncStage.Phase != SyncStageCommitting) {
			continue
		}
		if jobActiveStatus(candidate.Status) || m.inflightWorkers[id] {
			return syncCommitReservation{}, candidate.Type, candidate.ID, false, nil
		}
	}
	job := m.jobs[jobID]
	if job == nil {
		return syncCommitReservation{}, "", "", false, fmt.Errorf("reserve sync commit: job %s is unavailable", jobID)
	}
	reservation := syncCommitReservation{
		previousProgress: append([]service.ProgressEvent(nil), job.Progress...),
		stageRef:         publicStageRef(stage.StageID),
	}
	if job.SyncStage != nil {
		previous := *job.SyncStage
		reservation.previous = &previous
	}
	view := stage.PublicView()
	job.SyncStage = &view
	job.UpdatedAt = m.now()
	job.Progress = append(job.Progress, service.ProgressEvent{
		Type: "phase", Phase: string(view.Phase), Collection: stage.Collection,
		RecordsListed: view.Staged, RecordsFetched: view.Fetched,
		RetryAfter: formatOptionalTime(view.RetryAfter), Attempt: view.Attempt, Message: message,
	})
	trimJobProgress(job, m.retention.MaxProgressEvents)
	if err := m.saveLocked(); err != nil {
		job.SyncStage = reservation.previous
		job.Progress = reservation.previousProgress
		return syncCommitReservation{}, "", "", false, JobAdmissionPersistenceError{}
	}
	return reservation, "", "", true, nil
}

func (m *JobManager) rollbackSyncCommitReservation(jobID string, reservation syncCommitReservation) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[jobID]
	if job == nil || job.SyncStage == nil || job.SyncStage.Phase != SyncStageCommitting || job.SyncStage.StageRef != reservation.stageRef {
		return
	}
	job.SyncStage = reservation.previous
	job.Progress = reservation.previousProgress
	job.UpdatedAt = m.now()
	_ = m.saveLocked()
}

func waitForSyncRetry(ctx context.Context, retryAt time.Time) error {
	timer := time.NewTimer(max(time.Until(retryAt), 0))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
	maxResponseSize := durableSyncResponseLimit(eff.Config.MaxResponseSize)
	svc, err := service.NewWithMode(store, mode, token, service.ServiceConfig{
		BaseURL:         eff.Config.GitCodeBaseURL,
		LockPath:        eff.Config.LockPath,
		Timeout:         eff.Config.DefaultTimeout,
		MaxResponseSize: maxResponseSize,
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

func durableSyncResponseLimit(configured int64) int64 {
	if configured <= 0 || configured > durableSyncResponseMaxBytes {
		return durableSyncResponseMaxBytes
	}
	return configured
}

func syncBulkRequest(req StartSyncJobRequest, progressCh chan<- service.ProgressEvent) service.BulkSyncRequest {
	maxPages, maxRecords := req.MaxPages, req.MaxRecords
	// A daemon sync never interprets omitted user bounds as an unbounded
	// provider traversal. A durable stage is always at most one provider page;
	// caller bounds can tighten that chunk, but cannot enlarge it beyond the
	// staging envelope. Maintenance checkpoints advance the next run.
	if maxPages <= 0 || maxPages > 1 {
		maxPages = 1
	}
	if maxRecords <= 0 {
		maxRecords = defaultSyncStageMaxRecords
	} else if maxRecords > defaultSyncStageMaxRecords {
		maxRecords = defaultSyncStageMaxRecords
	}
	bulkReq := service.BulkSyncRequest{RepoID: req.RepoID, IdempotencyKey: strings.TrimSpace(req.IdempotencyKey), Page: req.Page, PerPage: req.PerPage, Bounds: &service.SyncBounds{MaxPages: maxPages, MaxRecords: maxRecords, MaxBytes: durableSyncPayloadMaxBytes, ProgressChan: progressCh}, ProgressChan: progressCh, IncrementalQueue: strings.TrimSpace(req.Lane) != ""}
	if bulkReq.PerPage <= 0 {
		bulkReq.PerPage = 100
	} else if bulkReq.PerPage > 100 {
		bulkReq.PerPage = 100
	}
	bulkReq.PerPage = min(bulkReq.PerPage, maxRecords)
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
