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
	if active, ok := m.ActiveWork(workKey); ok {
		return active, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	job := m.createJobWithMetadata(SyncJobType, req.RepoID, "", 0, cancel)
	job = m.SetWorkIdentity(job.ID, workKey, req.CacheUUID, req.RegistrationID, "")
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
	result, err := runSync(ctx, manager, req, progressCh)
	close(progressCh)
	<-done
	if result == nil {
		result = &service.SyncResourcesResult{}
	}
	if strings.TrimSpace(req.Lane) != "" {
		if frontierErr := recordMaintenanceSyncFrontiers(context.Background(), req, result, err); frontierErr != nil && err == nil {
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
			job.Error = err.Error()
			job.Progress = append(job.Progress, service.ProgressEvent{Type: status, Phase: status, Collection: SyncJobType, RecordsListed: result.RecordsListed, RecordsFetched: result.SuccessCount, RecordsFailed: result.FailureCount, Message: err.Error()})
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

func recordMaintenanceSyncFrontiers(ctx context.Context, req StartSyncJobRequest, result *service.SyncResourcesResult, syncErr error) error {
	store, err := cache.NewSQLiteStore(ctx, req.CachePath)
	if err != nil {
		return err
	}
	defer store.Close()
	status := "fresh"
	if req.Lane == "tail" {
		status = "backfilling"
		if syncErr == nil && result != nil && result.TraversalStatus == "complete" {
			status = "complete"
		}
	}
	errorClass := ""
	if syncErr != nil {
		status = "degraded"
		errorClass = "sync_failed"
		if coded, ok := syncErr.(interface{ DiagnosticCode() string }); ok {
			errorClass = coded.DiagnosticCode()
		}
	}
	remoteTypes := []string{}
	if req.Issues {
		remoteTypes = append(remoteTypes, "issue")
	}
	if req.Wiki {
		remoteTypes = append(remoteTypes, "wiki")
	}
	if req.Pulls {
		remoteTypes = append(remoteTypes, "pull_request")
	}
	if req.IssueComments {
		remoteTypes = append(remoteTypes, "issue_comment")
	}
	if req.PRComments || req.Comments {
		remoteTypes = append(remoteTypes, "pr_comment")
	}
	if len(remoteTypes) == 0 {
		remoteTypes = append(remoteTypes, "all")
	}
	for _, remoteType := range remoteTypes {
		frontier := cache.MaintenanceFrontier{RepoID: req.RepoID, RemoteType: remoteType, Ordering: "updated_at_desc", FilterKey: "all", Lane: req.Lane, Status: status, LastErrorClass: errorClass, UpdatedAt: time.Now().UTC()}
		if result != nil {
			frontier.StopReason = result.StopReason
			frontier.PagesListed = result.PagesListed
			frontier.RecordsListed = result.RecordsListed
			frontier.Checkpoint = fmt.Sprintf("max_pages:%d", req.MaxPages)
		}
		if err := store.UpsertMaintenanceFrontier(ctx, frontier); err != nil {
			return err
		}
	}
	return nil
}

func runSync(ctx context.Context, manager Manager, req StartSyncJobRequest, progressCh chan<- service.ProgressEvent) (*service.SyncResourcesResult, error) {
	src := manager.Source
	if src == nil {
		src = config.OSSource{}
	}
	eff, err := config.LoadEffective(src, config.Overrides{CachePath: req.CachePath})
	if err != nil {
		return nil, err
	}
	store, err := cache.NewSQLiteStore(ctx, eff.Config.CachePath)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	mode, err := syncJobProviderMode(req)
	if err != nil {
		return nil, err
	}
	token := ""
	if mode == gitcode.ProviderModeLive {
		secret, _, err := config.DefaultCredentialProvider(src).Resolve(ctx, eff)
		if err != nil {
			return nil, err
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
		return nil, err
	}
	bulkReq := service.BulkSyncRequest{RepoID: req.RepoID, IdempotencyKey: strings.TrimSpace(req.IdempotencyKey), PerPage: req.PerPage, Bounds: &service.SyncBounds{MaxPages: req.MaxPages, MaxRecords: req.MaxRecords, ProgressChan: progressCh}, ProgressChan: progressCh}
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

func runSyncSelections(ctx context.Context, svc bulkSyncService, req service.BulkSyncRequest, sel StartSyncJobRequest) (*service.SyncResourcesResult, error) {
	if !sel.Issues && !sel.Wiki && !sel.Pulls && !sel.Comments && !sel.IssueComments && !sel.PRComments {
		return svc.BulkSyncAll(ctx, req)
	}
	aggregate := &service.SyncResourcesResult{Results: []service.SyncResult{}, Failures: []service.ResourceError{}}
	var syncErr error
	run := func(fn func(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)) {
		part, err := fn(ctx, req)
		mergeSyncResources(aggregate, part)
		if err != nil {
			syncErr = mergeSyncError(syncErr, aggregate, err)
		}
	}
	if sel.Issues {
		run(svc.BulkSyncIssues)
	}
	if sel.IssueComments {
		run(svc.BulkSyncIssueComments)
	}
	if sel.Wiki {
		run(svc.BulkSyncWiki)
	}
	if sel.Pulls {
		run(svc.BulkSyncPullRequests)
	}
	if sel.Comments || sel.PRComments {
		run(svc.BulkSyncPRComments)
	}
	if aggregate.SuccessCount == 0 && aggregate.FailureCount == 0 {
		aggregate.SuccessCount = len(aggregate.Results)
		aggregate.FailureCount = len(aggregate.Failures)
	}
	return aggregate, syncErr
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
