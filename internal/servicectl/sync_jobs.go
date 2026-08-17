package servicectl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/service"
)

const SyncJobType = "sync"

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

func (m *JobManager) StartSync(ctx context.Context, manager Manager, req StartSyncJobRequest) (Job, error) {
	req.RepoID = strings.TrimSpace(req.RepoID)
	if req.RepoID == "" {
		return Job{}, errors.New("repo_id is required")
	}
	workKey := syncWorkKey(req)
	ctx, cancel := context.WithCancel(ctx)
	job, created := m.createCoalescedJob(SyncJobType, req.RepoID, "", 0, workKey, req.CacheUUID, req.RegistrationID, "", cancel)
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
	result, collections, err := runSync(ctx, manager, req, progressCh)
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
	src := manager.Source
	if src == nil {
		src = config.OSSource{}
	}
	eff, err := config.LoadEffective(src, config.Overrides{CachePath: req.CachePath})
	if err != nil {
		return nil, nil, err
	}
	store, err := cache.NewSQLiteStore(ctx, eff.Config.CachePath)
	if err != nil {
		return nil, nil, err
	}
	defer store.Close()
	mode, err := syncJobProviderMode(req)
	if err != nil {
		return nil, nil, err
	}
	token := ""
	if mode == gitcode.ProviderModeLive {
		secret, _, err := config.DefaultCredentialProvider(src).Resolve(ctx, eff)
		if err != nil {
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
		return nil, nil, err
	}
	bulkReq := service.BulkSyncRequest{RepoID: req.RepoID, IdempotencyKey: strings.TrimSpace(req.IdempotencyKey), Page: req.Page, PerPage: req.PerPage, Bounds: &service.SyncBounds{MaxPages: req.MaxPages, MaxRecords: req.MaxRecords, ProgressChan: progressCh}, ProgressChan: progressCh, IncrementalQueue: strings.TrimSpace(req.Lane) != ""}
	if bulkReq.PerPage <= 0 {
		bulkReq.PerPage = 100
	}
	return runSyncSelections(ctx, svc, bulkReq, req)
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
	run := func(remoteType string, fn func(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)) {
		part, err := fn(ctx, req)
		collections = append(collections, syncCollectionResult{RemoteType: remoteType, Result: part, Err: err})
		mergeSyncResources(aggregate, part)
		if err != nil {
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
