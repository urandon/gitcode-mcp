package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
)

const DurableSyncBatchVersion = 1

// DurableIssueSyncBatch is the provider-complete, cache-uncommitted form of a
// bounded issue collection traversal. It is JSON-safe for the daemon-private
// checksummed journal. CommitIssueSyncBatch performs no provider calls.
type DurableIssueSyncBatch struct {
	Version            int                `json:"version"`
	RepoID             string             `json:"repo_id"`
	Collection         string             `json:"collection"`
	IdempotencyKey     string             `json:"idempotency_key"`
	Items              []DurableIssueItem `json:"items"`
	PagesListed        int                `json:"pages_listed"`
	RecordsListed      int                `json:"records_listed"`
	SkippedByWatermark int                `json:"skipped_by_watermark,omitempty"`
	StopReason         string             `json:"stop_reason"`
	TraversalStatus    string             `json:"traversal_status"`
	WatermarkStatus    string             `json:"watermark_status"`
	WatermarkReason    string             `json:"watermark_reason"`
	HighUpdatedAt      time.Time          `json:"high_updated_at,omitempty"`
	HighRemoteID       string             `json:"high_remote_id,omitempty"`
	HighNumber         int                `json:"high_number,omitempty"`
	FetchedAt          time.Time          `json:"fetched_at"`
}

// DurableIssueItem is a daemon-journal wire type. Provider models deliberately
// use strict JSON decoders; staged state must not be reinterpreted as a fresh
// provider response after a daemon restart.
type DurableIssueItem struct {
	ID        string    `json:"id,omitempty"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	Body      string    `json:"body,omitempty"`
	Status    string    `json:"status,omitempty"`
	State     string    `json:"state,omitempty"`
	Comments  int       `json:"comments,omitempty"`
	Labels    []string  `json:"labels,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

func durableIssueItems(items []gitcode.IssueSummary) []DurableIssueItem {
	result := make([]DurableIssueItem, 0, len(items))
	for _, item := range items {
		result = append(result, DurableIssueItem{ID: item.ID, Number: item.Number, Title: item.Title, Body: item.Body, Status: item.Status, State: item.State, Comments: item.Comments, Labels: append([]string(nil), item.Labels...), CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt})
	}
	return result
}

func (i DurableIssueItem) providerItem() gitcode.IssueSummary {
	return gitcode.IssueSummary{ID: i.ID, Number: i.Number, Title: i.Title, Body: i.Body, Status: i.Status, State: i.State, Comments: i.Comments, Labels: append([]string(nil), i.Labels...), CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt}
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
		batch.Items = append(batch.Items, durableIssueItems(items)...)
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
	items := make([]gitcode.IssueSummary, 0, len(batch.Items))
	for _, item := range batch.Items {
		items = append(items, item.providerItem())
	}
	s.stageIssuePage(ctx, req, items, result, 0)
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

type DurablePullSyncBatch struct {
	Version            int               `json:"version"`
	RepoID             string            `json:"repo_id"`
	Collection         string            `json:"collection"`
	IdempotencyKey     string            `json:"idempotency_key"`
	Items              []DurablePullItem `json:"items"`
	PagesListed        int               `json:"pages_listed"`
	RecordsListed      int               `json:"records_listed"`
	SkippedByWatermark int               `json:"skipped_by_watermark,omitempty"`
	StopReason         string            `json:"stop_reason"`
	TraversalStatus    string            `json:"traversal_status"`
	WatermarkStatus    string            `json:"watermark_status"`
	WatermarkReason    string            `json:"watermark_reason"`
	HighUpdatedAt      time.Time         `json:"high_updated_at,omitempty"`
	HighRemoteID       string            `json:"high_remote_id,omitempty"`
	HighNumber         int               `json:"high_number,omitempty"`
	FetchedAt          time.Time         `json:"fetched_at"`
}

type DurablePullItem struct {
	Kind      string    `json:"kind,omitempty"`
	SourceID  string    `json:"source_id,omitempty"`
	ID        string    `json:"id,omitempty"`
	Number    int       `json:"number"`
	HTMLURL   string    `json:"html_url,omitempty"`
	State     string    `json:"state,omitempty"`
	Title     string    `json:"title"`
	Body      string    `json:"body,omitempty"`
	User      string    `json:"user,omitempty"`
	Labels    []string  `json:"labels,omitempty"`
	Base      string    `json:"base,omitempty"`
	BaseSHA   string    `json:"base_sha,omitempty"`
	Head      string    `json:"head,omitempty"`
	HeadSHA   string    `json:"head_sha,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

func durablePullItems(items []gitcode.PullRequest) []DurablePullItem {
	result := make([]DurablePullItem, 0, len(items))
	for _, item := range items {
		result = append(result, DurablePullItem{Kind: item.Kind, SourceID: item.SourceID, ID: item.ID, Number: item.Number, HTMLURL: item.HTMLURL, State: item.State, Title: item.Title, Body: item.Body, User: item.User, Labels: append([]string(nil), item.Labels...), Base: item.Base, BaseSHA: item.BaseSHA, Head: item.Head, HeadSHA: item.HeadSHA, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt})
	}
	return result
}

func (i DurablePullItem) providerItem() gitcode.PullRequest {
	return gitcode.PullRequest{Kind: i.Kind, SourceID: i.SourceID, ID: i.ID, Number: i.Number, HTMLURL: i.HTMLURL, State: i.State, Title: i.Title, Body: i.Body, User: i.User, Labels: append([]string(nil), i.Labels...), Base: i.Base, BaseSHA: i.BaseSHA, Head: i.Head, HeadSHA: i.HeadSHA, CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt}
}

func (b DurablePullSyncBatch) RecordCount() int { return len(b.Items) }

func (s *Service) FetchPullSyncBatch(ctx context.Context, req BulkSyncRequest) (DurablePullSyncBatch, error) {
	repoID, err := s.requireRepo(ctx, req.RepoID, "bulk-sync-pulls-fetch")
	if err != nil {
		return DurablePullSyncBatch{}, err
	}
	req.RepoID = repoID
	s.ensureBulkIdempotencyKey(&req, "pulls")
	if err := s.validateRepoScope(ctx, repoID, "pull_request"); err != nil {
		return DurablePullSyncBatch{}, err
	}
	route, err := s.BuildAdapterRoute(ctx, repoID, RepositoryScopeIssues)
	if err != nil {
		return DurablePullSyncBatch{}, err
	}
	frontier, frontierOK, err := s.completeSyncWatermark(ctx, repoID, "pull_request")
	if err != nil {
		return DurablePullSyncBatch{}, err
	}
	batch := DurablePullSyncBatch{Version: DurableSyncBatchVersion, RepoID: repoID, Collection: "pulls", IdempotencyKey: req.IdempotencyKey, TraversalStatus: "partial", FetchedAt: s.now().UTC()}
	summary := &SyncResourcesResult{}
	setWatermarkSummary(summary, frontier, frontierOK)
	batch.WatermarkStatus, batch.WatermarkReason = summary.WatermarkStatus, summary.WatermarkReason
	pageNumber, perPage := req.Page, req.PerPage
	if pageNumber < 1 {
		pageNumber = 1
	}
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
			return batch, err
		}
		page, listErr := s.client.ListPRs(ctx, gitcode.PRListRequest{Owner: route.Owner, Repo: route.Name, State: "all", OrderBy: "updated_at", Direction: "desc", Page: pageNumber, PerPage: perPage})
		if listErr != nil {
			batch.StopReason, batch.TraversalStatus = "provider_failure", "partial"
			return batch, s.normalizeSyncFailure(listErr, SyncRequest{RepoID: repoID, RemoteAlias: "pull_request:*"}, "pull_request", "*")
		}
		batch.PagesListed++
		batch.RecordsListed += len(page.Items)
		observePullRequestHighWatermark(&high, page.Items)
		items, stopByWatermark, skipped := filterPullRequestsByCompleteWatermark(page.Items, frontier, frontierOK)
		batch.SkippedByWatermark += skipped
		if maxRecords > 0 && len(batch.Items)+len(items) > maxRecords {
			items = items[:maxRecords-len(batch.Items)]
		}
		batch.Items = append(batch.Items, durablePullItems(items)...)
		emitProgress(req.ProgressChan, ProgressEvent{Collection: "pulls", Phase: "fetching", Page: pageNumber, RecordsListed: len(page.Items), RecordsFetched: len(items)})
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
		pageNumber++
	}
	if batch.StopReason == "" {
		batch.StopReason, batch.TraversalStatus = "max_pages", "bounded"
	}
	batch.HighUpdatedAt, batch.HighRemoteID, batch.HighNumber = high.UpdatedAt, high.RemoteID, high.Number
	return batch, nil
}

func (s *Service) CommitPullSyncBatch(ctx context.Context, batch DurablePullSyncBatch, progress chan<- ProgressEvent) (*SyncResourcesResult, error) {
	if batch.Version != DurableSyncBatchVersion || strings.TrimSpace(batch.RepoID) == "" || batch.Collection != "pulls" || strings.TrimSpace(batch.IdempotencyKey) == "" {
		return nil, ErrInvalidQuery{Field: "batch", Message: "staged pull request batch is incompatible or incomplete"}
	}
	ctx, releaseWriter, err := s.acquireBulkWriter(ctx, batch.RepoID, "bulk-sync-pulls-commit")
	if err != nil {
		return nil, err
	}
	defer releaseWriter()
	result := &SyncResourcesResult{Results: []SyncResult{}, Failures: []ResourceError{}, PagesListed: batch.PagesListed, RecordsListed: batch.RecordsListed, SkippedByWatermark: batch.SkippedByWatermark, StopReason: batch.StopReason, TraversalStatus: batch.TraversalStatus, Ordering: syncOrderingUpdatedAtDesc, WatermarkStatus: batch.WatermarkStatus, WatermarkReason: batch.WatermarkReason}
	items := make([]gitcode.PullRequest, 0, len(batch.Items))
	for _, item := range batch.Items {
		items = append(items, item.providerItem())
	}
	s.stagePullRequestPage(ctx, BulkSyncRequest{RepoID: batch.RepoID, IdempotencyKey: batch.IdempotencyKey}, items, result)
	emitProgress(progress, ProgressEvent{Collection: "pulls", Phase: "committing", RecordsListed: len(batch.Items), RecordsFetched: len(result.Results)})
	result.SuccessCount, result.FailureCount = len(result.Results), len(result.Failures)
	high := syncHighWatermark{UpdatedAt: batch.HighUpdatedAt, RemoteID: batch.HighRemoteID, Number: batch.HighNumber}
	if result.FailureCount > 0 {
		result.TraversalStatus = "partial"
		s.recordSyncFrontierBestEffort(ctx, batch.RepoID, "pull_request", result, high)
		return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount}
	}
	if err := s.recordSyncFrontier(ctx, batch.RepoID, "pull_request", result, high); err != nil {
		return bulkSyncFailureResult(err, "pull_request:*", "pull_request")
	}
	return result, nil
}

type DurableWikiSyncBatch struct {
	Version         int                `json:"version"`
	RepoID          string             `json:"repo_id"`
	Collection      string             `json:"collection"`
	IdempotencyKey  string             `json:"idempotency_key"`
	Items           []gitcode.WikiPage `json:"items"`
	PagesListed     int                `json:"pages_listed"`
	RecordsListed   int                `json:"records_listed"`
	StopReason      string             `json:"stop_reason"`
	TraversalStatus string             `json:"traversal_status"`
	FetchedAt       time.Time          `json:"fetched_at"`
}

func (b DurableWikiSyncBatch) RecordCount() int { return len(b.Items) }

func (s *Service) FetchWikiSyncBatch(ctx context.Context, req BulkSyncRequest) (DurableWikiSyncBatch, error) {
	repoID, err := s.requireRepo(ctx, req.RepoID, "bulk-sync-wiki-fetch")
	if err != nil {
		return DurableWikiSyncBatch{}, err
	}
	req.RepoID = repoID
	s.ensureBulkIdempotencyKey(&req, "wiki")
	if err := s.validateRepoScope(ctx, repoID, "wiki"); err != nil {
		return DurableWikiSyncBatch{}, err
	}
	route, err := s.BuildAdapterRoute(ctx, repoID, RepositoryScopeWiki)
	if err != nil {
		return DurableWikiSyncBatch{}, err
	}
	maxRecords := 0
	if req.Bounds != nil {
		maxRecords = req.Bounds.MaxRecords
		if maxRecords <= 0 && req.Bounds.MaxPages > 0 {
			perPage := req.PerPage
			if perPage < 1 {
				perPage = 100
			}
			maxRecords = req.Bounds.MaxPages * perPage
		}
	}
	wikiBounds := &gitcode.WikiBounds{MaxRecords: maxRecords}
	page, err := s.client.ListWikiPages(ctx, gitcode.WikiListRequest{Owner: route.Owner, Repo: route.Name, Page: req.Page, PerPage: req.PerPage, Bounds: wikiBounds})
	batch := DurableWikiSyncBatch{Version: DurableSyncBatchVersion, RepoID: repoID, Collection: "wiki", IdempotencyKey: req.IdempotencyKey, Items: page.Items, RecordsListed: len(page.Items), FetchedAt: s.now().UTC()}
	perPage := req.PerPage
	if perPage < 1 {
		perPage = max(1, len(page.Items))
	}
	if len(page.Items) > 0 {
		batch.PagesListed = (len(page.Items) + perPage - 1) / perPage
	}
	if err != nil {
		batch.StopReason, batch.TraversalStatus = "provider_failure", "partial"
		return batch, s.normalizeSyncFailure(err, SyncRequest{RepoID: repoID, RemoteAlias: "wiki:*"}, "wiki", "*")
	}
	if page.NextPage > 0 {
		batch.StopReason, batch.TraversalStatus = "max_records", "bounded"
	} else {
		batch.StopReason, batch.TraversalStatus = "end_of_collection", "complete"
	}
	emitProgress(req.ProgressChan, ProgressEvent{Collection: "wiki", Phase: "fetching", RecordsListed: len(page.Items), RecordsFetched: len(page.Items)})
	return batch, nil
}

func (s *Service) CommitWikiSyncBatch(ctx context.Context, batch DurableWikiSyncBatch, progress chan<- ProgressEvent) (*SyncResourcesResult, error) {
	if batch.Version != DurableSyncBatchVersion || strings.TrimSpace(batch.RepoID) == "" || batch.Collection != "wiki" || strings.TrimSpace(batch.IdempotencyKey) == "" {
		return nil, ErrInvalidQuery{Field: "batch", Message: "staged wiki batch is incompatible or incomplete"}
	}
	ctx, releaseWriter, err := s.acquireBulkWriter(ctx, batch.RepoID, "bulk-sync-wiki-commit")
	if err != nil {
		return nil, err
	}
	defer releaseWriter()
	result := &SyncResourcesResult{Results: []SyncResult{}, Failures: []ResourceError{}, PagesListed: batch.PagesListed, RecordsListed: batch.RecordsListed, StopReason: batch.StopReason, TraversalStatus: batch.TraversalStatus, Ordering: "path_asc"}
	for _, page := range batch.Items {
		remoteID := strings.TrimSpace(page.Slug)
		req := SyncRequest{RepoID: batch.RepoID, AliasType: "wiki", AliasID: remoteID, IdempotencyKey: scopedBulkSyncKey(batch.IdempotencyKey, "wiki", remoteID)}
		graph, counts, stageErr := s.stageWiki(ctx, req, "wiki", remoteID, page)
		if stageErr != nil {
			result.Failures = append(result.Failures, newResourceError(remoteID, "wiki", stageErr))
			continue
		}
		counts.Listed, counts.FetchedDetail = 1, 1
		completedAt := s.now().UTC()
		eventID := syncEventID(req.IdempotencyKey)
		zeroDelta := counts.Fetched > 0 && counts.Skipped == counts.Fetched && counts.Updated == 0 && counts.Inserted == 0 && counts.Conflicts == 0
		graph.SyncEvents = append(graph.SyncEvents, cache.SyncEvent{ID: eventID, SourceID: graph.Source.ID, RemoteType: "wiki", RemoteID: remoteID, RemoteRevision: graph.SyncStatus.RemoteRevision, Status: "succeeded", IdempotencyKey: req.IdempotencyKey, Message: syncEventMessage(counts), CreatedAt: completedAt, StartedAt: completedAt, CompletedAt: completedAt, ZeroDelta: zeroDelta})
		if err := s.store.UpsertSyncGraph(ctx, s.syncGraphFromSourceGraph(batch.RepoID, graph)); err != nil {
			result.Failures = append(result.Failures, newResourceError(remoteID, "wiki", err))
			continue
		}
		stored, err := s.store.GetSourceScoped(ctx, batch.RepoID, graph.Source.ID)
		if err != nil {
			result.Failures = append(result.Failures, newResourceError(remoteID, "wiki", err))
			continue
		}
		result.Results = append(result.Results, SyncResult{IdempotencyKey: req.IdempotencyKey, Status: "succeeded", Counts: counts, SyncEventID: eventID, Freshness: string(FreshnessFresh), Record: sourceSummary(stored), GeneratedAt: completedAt, StartedAt: completedAt, CompletedAt: completedAt, ZeroDelta: zeroDelta})
	}
	emitProgress(progress, ProgressEvent{Collection: "wiki", Phase: "committing", RecordsListed: len(batch.Items), RecordsFetched: len(result.Results)})
	result.SuccessCount, result.FailureCount = len(result.Results), len(result.Failures)
	if result.FailureCount > 0 {
		return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount}
	}
	return result, nil
}
