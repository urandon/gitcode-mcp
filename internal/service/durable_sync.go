package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gitcode-mcp/internal/gitcode"
)

const DurableSyncBatchVersion = 1

// DurableIssueSyncBatch is the provider-complete, cache-uncommitted form of a
// bounded issue collection traversal. It is JSON-safe for the daemon-private
// checksummed journal. CommitIssueSyncBatch performs no provider calls.
type DurableIssueSyncBatch struct {
	Version            int                    `json:"version"`
	RepoID             string                 `json:"repo_id"`
	Collection         string                 `json:"collection"`
	IdempotencyKey     string                 `json:"idempotency_key"`
	Items              []gitcode.IssueSummary `json:"items"`
	PagesListed        int                    `json:"pages_listed"`
	RecordsListed      int                    `json:"records_listed"`
	SkippedByWatermark int                    `json:"skipped_by_watermark,omitempty"`
	StopReason         string                 `json:"stop_reason"`
	TraversalStatus    string                 `json:"traversal_status"`
	WatermarkStatus    string                 `json:"watermark_status"`
	WatermarkReason    string                 `json:"watermark_reason"`
	HighUpdatedAt      time.Time              `json:"high_updated_at,omitempty"`
	HighRemoteID       string                 `json:"high_remote_id,omitempty"`
	HighNumber         int                    `json:"high_number,omitempty"`
	FetchedAt          time.Time              `json:"fetched_at"`
}

func (b DurableIssueSyncBatch) RecordCount() int { return len(b.Items) }

func (s *Service) FetchIssueSyncBatch(ctx context.Context, req BulkSyncRequest) (DurableIssueSyncBatch, error) {
	if err := ctx.Err(); err != nil {
		return DurableIssueSyncBatch{}, err
	}
	repoID, err := s.requireRepo(ctx, req.RepoID, "bulk-sync-issues-fetch")
	if err != nil {
		return DurableIssueSyncBatch{}, err
	}
	req.RepoID = repoID
	s.ensureBulkIdempotencyKey(&req, "issues")
	if err := s.validateRepoScope(ctx, repoID, "issues"); err != nil {
		return DurableIssueSyncBatch{}, err
	}
	route, err := s.BuildAdapterRoute(ctx, repoID, RepositoryScopeIssues)
	if err != nil {
		return DurableIssueSyncBatch{}, err
	}
	frontier, frontierOK, err := s.completeSyncWatermark(ctx, repoID, "issue")
	if err != nil {
		return DurableIssueSyncBatch{}, err
	}
	batch := DurableIssueSyncBatch{
		Version: DurableSyncBatchVersion, RepoID: repoID, Collection: "issues",
		IdempotencyKey: req.IdempotencyKey, TraversalStatus: "partial",
		FetchedAt: s.now().UTC(),
	}
	summary := &SyncResourcesResult{}
	setWatermarkSummary(summary, frontier, frontierOK)
	batch.WatermarkStatus, batch.WatermarkReason = summary.WatermarkStatus, summary.WatermarkReason
	currentPage := req.Page
	if currentPage < 1 {
		currentPage = 1
	}
	perPage := req.PerPage
	if perPage < 1 {
		perPage = 100
	}
	maxPages, maxRecords := 1, 0
	if req.Bounds != nil {
		maxPages, maxRecords = req.Bounds.MaxPages, req.Bounds.MaxRecords
		if maxPages <= 0 {
			maxPages = int(^uint(0) >> 1)
		}
	}
	var high syncHighWatermark
	for pageIndex := 0; pageIndex < maxPages; pageIndex++ {
		if err := ctx.Err(); err != nil {
			batch.StopReason = "cancelled"
			batch.TraversalStatus = "cancelled"
			if errors.Is(err, context.DeadlineExceeded) {
				batch.StopReason, batch.TraversalStatus = "timeout", "timeout"
			}
			return batch, err
		}
		if maxRecords > 0 && len(batch.Items) >= maxRecords {
			batch.StopReason, batch.TraversalStatus = "max_records", "bounded"
			break
		}
		page, listErr := s.client.ListIssues(ctx, gitcode.IssueListRequest{Owner: route.Owner, Repo: route.Name, State: "all", OrderBy: "updated_at", Direction: "desc", Page: currentPage, PerPage: perPage})
		if listErr != nil && !recoverableIssueListPage(listErr) {
			batch.StopReason, batch.TraversalStatus = "provider_failure", "partial"
			return batch, s.normalizeSyncFailure(listErr, SyncRequest{RepoID: repoID, RemoteAlias: "issue:*"}, "issues", "*")
		}
		if listErr == nil || len(page.Items) > 0 {
			batch.PagesListed++
			batch.RecordsListed += len(page.Items)
		}
		observeIssueHighWatermark(&high, page.Items)
		items, stopByWatermark, skipped := filterIssueSummariesByCompleteWatermark(page.Items, frontier, frontierOK)
		batch.SkippedByWatermark += skipped
		if maxRecords > 0 && len(batch.Items)+len(items) > maxRecords {
			items = items[:maxRecords-len(batch.Items)]
		}
		batch.Items = append(batch.Items, items...)
		emitProgress(req.ProgressChan, ProgressEvent{Collection: "issues", Phase: "fetching", Page: currentPage, RecordsListed: len(page.Items), RecordsFetched: len(items)})
		if listErr != nil {
			batch.StopReason, batch.TraversalStatus = "provider_partial_response", "partial"
			batch.HighUpdatedAt, batch.HighRemoteID, batch.HighNumber = high.UpdatedAt, high.RemoteID, high.Number
			return batch, s.normalizeSyncFailure(listErr, SyncRequest{RepoID: repoID, RemoteAlias: "issue:*"}, "issues", "*")
		}
		if stopByWatermark {
			batch.StopReason, batch.TraversalStatus = "watermark", "complete"
			batch.WatermarkStatus, batch.WatermarkReason = "used", "previous_complete_frontier"
			break
		}
		if len(page.Items) < perPage {
			batch.StopReason, batch.TraversalStatus = "end_of_collection", "complete"
			break
		}
		if maxRecords > 0 && len(batch.Items) >= maxRecords {
			batch.StopReason, batch.TraversalStatus = "max_records", "bounded"
			break
		}
		currentPage++
	}
	if batch.StopReason == "" {
		batch.StopReason, batch.TraversalStatus = "max_pages", "bounded"
	}
	batch.HighUpdatedAt, batch.HighRemoteID, batch.HighNumber = high.UpdatedAt, high.RemoteID, high.Number
	return batch, nil
}

func (s *Service) CommitIssueSyncBatch(ctx context.Context, batch DurableIssueSyncBatch, progress chan<- ProgressEvent) (*SyncResourcesResult, error) {
	if err := validateDurableIssueBatch(batch); err != nil {
		return nil, err
	}
	repoID, err := s.requireRepo(ctx, batch.RepoID, "bulk-sync-issues-commit")
	if err != nil {
		return nil, err
	}
	if repoID != batch.RepoID {
		return nil, ErrInvalidQuery{Field: "repo_id", Message: "staged repository binding changed before commit"}
	}
	ctx, releaseWriter, err := s.acquireBulkWriter(ctx, repoID, "bulk-sync-issues-commit")
	if err != nil {
		return nil, err
	}
	defer releaseWriter()
	if err := s.validateRepoScope(ctx, repoID, "issues"); err != nil {
		return nil, err
	}
	result := &SyncResourcesResult{
		Results: []SyncResult{}, Failures: []ResourceError{}, PagesListed: batch.PagesListed,
		RecordsListed: batch.RecordsListed, SkippedByWatermark: batch.SkippedByWatermark,
		StopReason: batch.StopReason, TraversalStatus: batch.TraversalStatus,
		Ordering: syncOrderingUpdatedAtDesc, WatermarkStatus: batch.WatermarkStatus,
		WatermarkReason: batch.WatermarkReason,
	}
	req := BulkSyncRequest{RepoID: repoID, IdempotencyKey: batch.IdempotencyKey, ProgressChan: progress}
	before := len(result.Results)
	s.stageIssuePage(ctx, req, batch.Items, result, 0)
	emitProgress(progress, ProgressEvent{Collection: "issues", Phase: "committing", RecordsListed: len(batch.Items), RecordsFetched: len(result.Results) - before})
	result.SuccessCount, result.FailureCount = len(result.Results), len(result.Failures)
	if summaryErr := s.attachIssueCommentQueueSummary(ctx, result, repoID, "parent_backfill"); summaryErr != nil {
		return result, summaryErr
	}
	high := syncHighWatermark{UpdatedAt: batch.HighUpdatedAt, RemoteID: batch.HighRemoteID, Number: batch.HighNumber}
	if result.FailureCount > 0 {
		result.TraversalStatus = "partial"
		s.recordSyncFrontierBestEffort(ctx, repoID, "issue", result, high)
		return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount}
	}
	if err := s.recordSyncFrontier(ctx, repoID, "issue", result, high); err != nil {
		return bulkSyncFailureResult(err, "issue:*", "issues")
	}
	return result, nil
}

func validateDurableIssueBatch(batch DurableIssueSyncBatch) error {
	if batch.Version != DurableSyncBatchVersion {
		return ErrInvalidQuery{Field: "batch_version", Message: "staged sync batch version is incompatible"}
	}
	if strings.TrimSpace(batch.RepoID) == "" || batch.Collection != "issues" || strings.TrimSpace(batch.IdempotencyKey) == "" {
		return ErrInvalidQuery{Field: "batch", Message: "staged issue batch identity is incomplete"}
	}
	for index, item := range batch.Items {
		if item.Number <= 0 {
			return ErrInvalidQuery{Field: "batch.items", Message: fmt.Sprintf("staged issue %d has invalid number %s", index, strconv.Itoa(item.Number))}
		}
	}
	return nil
}
