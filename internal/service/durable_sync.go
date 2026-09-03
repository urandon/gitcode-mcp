package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
)

const DurableSyncBatchVersion = 1

func durableBatchFitsByteBound(bounds *SyncBounds, batch any) bool {
	if bounds == nil || bounds.MaxBytes <= 0 {
		return true
	}
	payload, err := json.Marshal(batch)
	return err == nil && int64(len(payload)) <= bounds.MaxBytes
}

func durableBatchBoundError(collection, dimension string) error {
	return ErrInvalidQuery{Field: "bounds", Message: collection + " provider response exceeds durable stage " + dimension + " limit"}
}

// DurableIssueSyncBatch is the provider-complete, cache-uncommitted form of a
// bounded issue collection traversal. It is JSON-safe for the daemon-private
// checksummed journal. CommitIssueSyncBatch performs no provider calls.
type DurableIssueSyncBatch struct {
	Version             int                        `json:"version"`
	RepoID              string                     `json:"repo_id"`
	Collection          string                     `json:"collection"`
	IdempotencyKey      string                     `json:"idempotency_key"`
	Items               []DurableIssueItem         `json:"items"`
	PagesListed         int                        `json:"pages_listed"`
	RecordsListed       int                        `json:"records_listed"`
	SkippedByWatermark  int                        `json:"skipped_by_watermark,omitempty"`
	StopReason          string                     `json:"stop_reason"`
	TraversalStatus     string                     `json:"traversal_status"`
	WatermarkStatus     string                     `json:"watermark_status"`
	WatermarkReason     string                     `json:"watermark_reason"`
	HighUpdatedAt       time.Time                  `json:"high_updated_at,omitempty"`
	HighRemoteID        string                     `json:"high_remote_id,omitempty"`
	HighNumber          int                        `json:"high_number,omitempty"`
	FetchedAt           time.Time                  `json:"fetched_at"`
	MaintenanceFrontier *cache.MaintenanceFrontier `json:"-"`
	CommitReceipt       *cache.SyncCommitReceipt   `json:"-"`
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
	graphs := make([]cache.SyncGraph, 0, len(batch.Items))
	queue := make([]cache.IssueCommentSync, 0, len(batch.Items))
	clearComments := make([]cache.RecordRef, 0)
	plans := make([]durableResultPlan, 0, len(batch.Items))
	for _, item := range batch.Items {
		issue := item.providerItem()
		remoteID := strconv.Itoa(issue.Number)
		syncReq := SyncRequest{RepoID: repoID, AliasType: "issue", AliasID: remoteID, IdempotencyKey: scopedBulkSyncKey(batch.IdempotencyKey, "issue", remoteID)}
		graph, counts, stageErr := s.stageIssueParent(ctx, syncReq, "issue", remoteID, gitcode.Issue{ID: issue.ID, Number: issue.Number, Title: issue.Title, Body: issue.Body, Status: issue.Status, State: issue.State, Comments: issue.Comments, Labels: issue.Labels, CreatedAt: issue.CreatedAt, UpdatedAt: issue.UpdatedAt})
		if stageErr != nil {
			result.Failures = append(result.Failures, newResourceError(remoteID, "issue", stageErr))
			continue
		}
		counts.Listed = 1
		completedAt := s.now().UTC()
		eventID := syncEventID(syncReq.IdempotencyKey)
		zeroDelta := durableZeroDelta(counts)
		graph.SyncEvents = append(graph.SyncEvents, cache.SyncEvent{ID: eventID, SourceID: graph.Source.ID, RemoteType: "issue", RemoteID: remoteID, RemoteRevision: graph.SyncStatus.RemoteRevision, Status: "succeeded", IdempotencyKey: syncReq.IdempotencyKey, Message: syncEventMessage(counts), CreatedAt: completedAt, StartedAt: completedAt, CompletedAt: completedAt, ZeroDelta: zeroDelta})
		queueItem, shouldClear, queued, queueErr := s.prepareIssueCommentSync(ctx, graph, gitcode.Issue{ID: issue.ID, Number: issue.Number, Comments: issue.Comments})
		if queueErr != nil {
			result.Failures = append(result.Failures, newResourceError(remoteID, "issue_comments", queueErr))
			continue
		}
		if queued {
			counts.Deferred++
		}
		if shouldClear {
			clearComments = append(clearComments, cache.RecordRef{RepoID: repoID, RecordID: graph.Source.ID})
		}
		graphs = append(graphs, s.syncGraphFromSourceGraph(repoID, graph))
		queue = append(queue, queueItem)
		plans = append(plans, durableResultPlan{graph: graph, remoteID: remoteID, counts: counts, idempotencyKey: syncReq.IdempotencyKey, eventID: eventID, completedAt: completedAt, zeroDelta: zeroDelta})
	}
	result.FailureCount = len(result.Failures)
	high := syncHighWatermark{UpdatedAt: batch.HighUpdatedAt, RemoteID: batch.HighRemoteID, Number: batch.HighNumber}
	if result.FailureCount > 0 {
		result.TraversalStatus = "partial"
		abortDurablePlans(result, plans, "issue")
		return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount}
	}
	result.SuccessCount = len(plans)
	frontier, err := s.syncFrontierForResult(ctx, repoID, "issue", result, high)
	if err != nil {
		return bulkSyncFailureResult(err, "issue:*", "issues")
	}
	if err := s.commitDurableSyncBatch(ctx, cache.SyncBatch{Graphs: graphs, Frontier: frontier, MaintenanceFrontier: batch.MaintenanceFrontier, Receipt: batch.CommitReceipt, IssueCommentSyncs: queue, ClearRecordCommentRefs: clearComments}); err != nil {
		return bulkSyncFailureResult(err, "issue:*", "issues")
	}
	result.Results = durableResults(plans)
	emitProgress(progress, ProgressEvent{Collection: "issues", Phase: "committing", RecordsListed: len(batch.Items), RecordsFetched: len(result.Results)})
	// The transaction above is authoritative. Queue summary is optional UX
	// enrichment and cannot turn a committed batch into a reported failure.
	_ = s.attachIssueCommentQueueSummary(ctx, result, repoID, "parent_backfill")
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
	Version             int                        `json:"version"`
	RepoID              string                     `json:"repo_id"`
	Collection          string                     `json:"collection"`
	IdempotencyKey      string                     `json:"idempotency_key"`
	Items               []DurablePullItem          `json:"items"`
	PagesListed         int                        `json:"pages_listed"`
	RecordsListed       int                        `json:"records_listed"`
	SkippedByWatermark  int                        `json:"skipped_by_watermark,omitempty"`
	StopReason          string                     `json:"stop_reason"`
	TraversalStatus     string                     `json:"traversal_status"`
	WatermarkStatus     string                     `json:"watermark_status"`
	WatermarkReason     string                     `json:"watermark_reason"`
	HighUpdatedAt       time.Time                  `json:"high_updated_at,omitempty"`
	HighRemoteID        string                     `json:"high_remote_id,omitempty"`
	HighNumber          int                        `json:"high_number,omitempty"`
	FetchedAt           time.Time                  `json:"fetched_at"`
	MaintenanceFrontier *cache.MaintenanceFrontier `json:"-"`
	CommitReceipt       *cache.SyncCommitReceipt   `json:"-"`
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
	if err := validateDurablePullBatch(batch); err != nil {
		return nil, err
	}
	repoID, err := s.requireRepo(ctx, batch.RepoID, "bulk-sync-pulls-commit")
	if err != nil {
		return nil, err
	}
	if repoID != batch.RepoID {
		return nil, ErrInvalidQuery{Field: "repo_id", Message: "staged repository binding changed before commit"}
	}
	ctx, releaseWriter, err := s.acquireBulkWriter(ctx, batch.RepoID, "bulk-sync-pulls-commit")
	if err != nil {
		return nil, err
	}
	defer releaseWriter()
	result := &SyncResourcesResult{Results: []SyncResult{}, Failures: []ResourceError{}, PagesListed: batch.PagesListed, RecordsListed: batch.RecordsListed, SkippedByWatermark: batch.SkippedByWatermark, StopReason: batch.StopReason, TraversalStatus: batch.TraversalStatus, Ordering: syncOrderingUpdatedAtDesc, WatermarkStatus: batch.WatermarkStatus, WatermarkReason: batch.WatermarkReason}
	graphs := make([]cache.SyncGraph, 0, len(batch.Items))
	plans := make([]durableResultPlan, 0, len(batch.Items))
	for _, item := range batch.Items {
		pr := item.providerItem()
		remoteID := strconv.Itoa(pr.Number)
		syncReq := SyncRequest{RepoID: batch.RepoID, AliasType: "pull_request", AliasID: remoteID, IdempotencyKey: scopedBulkSyncKey(batch.IdempotencyKey, "pull_request", remoteID)}
		graph, counts, stageErr := s.stagePullRequest(ctx, syncReq, "pull_request", remoteID, pr)
		if stageErr != nil {
			result.Failures = append(result.Failures, newResourceError(remoteID, "pull_request", stageErr))
			continue
		}
		counts.Listed = 1
		completedAt := s.now().UTC()
		eventID := syncEventID(syncReq.IdempotencyKey)
		zeroDelta := durableZeroDelta(counts)
		graph.SyncEvents = append(graph.SyncEvents, cache.SyncEvent{ID: eventID, SourceID: graph.Source.ID, RemoteType: "pull_request", RemoteID: remoteID, RemoteRevision: graph.SyncStatus.RemoteRevision, Status: "succeeded", IdempotencyKey: syncReq.IdempotencyKey, Message: syncEventMessage(counts), CreatedAt: completedAt, StartedAt: completedAt, CompletedAt: completedAt, ZeroDelta: zeroDelta})
		graphs = append(graphs, s.syncGraphFromSourceGraph(batch.RepoID, graph))
		plans = append(plans, durableResultPlan{graph: graph, remoteID: remoteID, counts: counts, idempotencyKey: syncReq.IdempotencyKey, eventID: eventID, completedAt: completedAt, zeroDelta: zeroDelta})
	}
	result.FailureCount = len(result.Failures)
	high := syncHighWatermark{UpdatedAt: batch.HighUpdatedAt, RemoteID: batch.HighRemoteID, Number: batch.HighNumber}
	if result.FailureCount > 0 {
		result.TraversalStatus = "partial"
		abortDurablePlans(result, plans, "pull_request")
		return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount}
	}
	result.SuccessCount = len(plans)
	frontier, err := s.syncFrontierForResult(ctx, batch.RepoID, "pull_request", result, high)
	if err != nil {
		return bulkSyncFailureResult(err, "pull_request:*", "pull_request")
	}
	if err := s.commitDurableSyncBatch(ctx, cache.SyncBatch{Graphs: graphs, Frontier: frontier, MaintenanceFrontier: batch.MaintenanceFrontier, Receipt: batch.CommitReceipt}); err != nil {
		return bulkSyncFailureResult(err, "pull_request:*", "pull_request")
	}
	result.Results = durableResults(plans)
	emitProgress(progress, ProgressEvent{Collection: "pulls", Phase: "committing", RecordsListed: len(batch.Items), RecordsFetched: len(result.Results)})
	return result, nil
}

func validateDurablePullBatch(batch DurablePullSyncBatch) error {
	if batch.Version != DurableSyncBatchVersion || strings.TrimSpace(batch.RepoID) == "" || batch.Collection != "pulls" || strings.TrimSpace(batch.IdempotencyKey) == "" {
		return ErrInvalidQuery{Field: "batch", Message: "staged pull request batch is incompatible or incomplete"}
	}
	for index, item := range batch.Items {
		if item.Number <= 0 {
			return ErrInvalidQuery{Field: "batch.items", Message: fmt.Sprintf("staged pull request %d has invalid number %s", index, strconv.Itoa(item.Number))}
		}
	}
	return nil
}

type DurableWikiSyncBatch struct {
	Version             int                        `json:"version"`
	RepoID              string                     `json:"repo_id"`
	Collection          string                     `json:"collection"`
	IdempotencyKey      string                     `json:"idempotency_key"`
	Items               []gitcode.WikiPage         `json:"items"`
	PagesListed         int                        `json:"pages_listed"`
	RecordsListed       int                        `json:"records_listed"`
	StopReason          string                     `json:"stop_reason"`
	TraversalStatus     string                     `json:"traversal_status"`
	NextPage            int                        `json:"next_page,omitempty"`
	ProviderRevision    string                     `json:"provider_revision"`
	FetchedAt           time.Time                  `json:"fetched_at"`
	MaintenanceFrontier *cache.MaintenanceFrontier `json:"-"`
	CommitReceipt       *cache.SyncCommitReceipt   `json:"-"`
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
	maxBytes := int64(0)
	if req.Bounds != nil {
		maxRecords = req.Bounds.MaxRecords
		maxBytes = req.Bounds.MaxBytes
		if req.Bounds.MaxPages > 0 {
			perPage := req.PerPage
			if perPage < 1 {
				perPage = 100
			}
			maxInt := int(^uint(0) >> 1)
			pageBound := maxInt
			if req.Bounds.MaxPages <= maxInt/perPage {
				pageBound = req.Bounds.MaxPages * perPage
			}
			if maxRecords <= 0 || maxRecords > pageBound {
				maxRecords = pageBound
			}
		}
	}
	wikiBounds := &gitcode.WikiBounds{MaxRecords: maxRecords, MaxBytes: maxBytes, OffsetPaging: true}
	page, err := s.client.ListWikiPages(ctx, gitcode.WikiListRequest{Owner: route.Owner, Repo: route.Name, Page: req.Page, PerPage: req.PerPage, Bounds: wikiBounds})
	batch := DurableWikiSyncBatch{Version: DurableSyncBatchVersion, RepoID: repoID, Collection: "wiki", IdempotencyKey: req.IdempotencyKey, Items: page.Items, RecordsListed: len(page.Items), NextPage: page.NextPage, ProviderRevision: durableWikiProviderRevision(page.Items), FetchedAt: s.now().UTC()}
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
	if err := validateDurableWikiBatch(batch); err != nil {
		return nil, err
	}
	repoID, err := s.requireRepo(ctx, batch.RepoID, "bulk-sync-wiki-commit")
	if err != nil {
		return nil, err
	}
	if repoID != batch.RepoID {
		return nil, ErrInvalidQuery{Field: "repo_id", Message: "staged repository binding changed before commit"}
	}
	ctx, releaseWriter, err := s.acquireBulkWriter(ctx, batch.RepoID, "bulk-sync-wiki-commit")
	if err != nil {
		return nil, err
	}
	defer releaseWriter()
	result := &SyncResourcesResult{Results: []SyncResult{}, Failures: []ResourceError{}, PagesListed: batch.PagesListed, RecordsListed: batch.RecordsListed, StopReason: batch.StopReason, TraversalStatus: batch.TraversalStatus, Ordering: "path_asc"}
	graphs := make([]cache.SyncGraph, 0, len(batch.Items))
	plans := make([]durableResultPlan, 0, len(batch.Items))
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
		zeroDelta := durableZeroDelta(counts)
		graph.SyncEvents = append(graph.SyncEvents, cache.SyncEvent{ID: eventID, SourceID: graph.Source.ID, RemoteType: "wiki", RemoteID: remoteID, RemoteRevision: graph.SyncStatus.RemoteRevision, Status: "succeeded", IdempotencyKey: req.IdempotencyKey, Message: syncEventMessage(counts), CreatedAt: completedAt, StartedAt: completedAt, CompletedAt: completedAt, ZeroDelta: zeroDelta})
		graphs = append(graphs, s.syncGraphFromSourceGraph(batch.RepoID, graph))
		plans = append(plans, durableResultPlan{graph: graph, remoteID: remoteID, counts: counts, idempotencyKey: req.IdempotencyKey, eventID: eventID, completedAt: completedAt, zeroDelta: zeroDelta})
	}
	result.FailureCount = len(result.Failures)
	if result.FailureCount > 0 {
		abortDurablePlans(result, plans, "wiki")
		return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount}
	}
	result.SuccessCount = len(plans)
	if err := s.commitDurableSyncBatch(ctx, cache.SyncBatch{Graphs: graphs, MaintenanceFrontier: batch.MaintenanceFrontier, Receipt: batch.CommitReceipt}); err != nil {
		return bulkSyncFailureResult(err, "wiki:*", "wiki")
	}
	result.Results = durableResults(plans)
	emitProgress(progress, ProgressEvent{Collection: "wiki", Phase: "committing", RecordsListed: len(batch.Items), RecordsFetched: len(result.Results)})
	return result, nil
}

func validateDurableWikiBatch(batch DurableWikiSyncBatch) error {
	if batch.Version != DurableSyncBatchVersion || strings.TrimSpace(batch.RepoID) == "" || batch.Collection != "wiki" || strings.TrimSpace(batch.IdempotencyKey) == "" || strings.TrimSpace(batch.ProviderRevision) == "" {
		return ErrInvalidQuery{Field: "batch", Message: "staged wiki batch is incompatible or incomplete"}
	}
	for index, item := range batch.Items {
		if strings.TrimSpace(item.Slug) == "" {
			return ErrInvalidQuery{Field: "batch.items", Message: fmt.Sprintf("staged wiki page %d has an empty slug", index)}
		}
	}
	return nil
}

func durableWikiProviderRevision(items []gitcode.WikiPage) string {
	parts := make([]any, 0, 1+len(items)*3)
	parts = append(parts, "wiki-batch")
	for _, item := range items {
		parts = append(parts, strings.TrimSpace(item.Slug), strings.TrimSpace(item.Revision), item.UpdatedAt.UTC())
	}
	return contentHash(parts...)
}

type durableSyncBatchStore interface {
	CommitSyncBatch(context.Context, cache.SyncBatch) error
}

type durableResultPlan struct {
	graph          cache.SourceGraph
	remoteID       string
	counts         SyncCounts
	idempotencyKey string
	eventID        string
	completedAt    time.Time
	zeroDelta      bool
}

func abortDurablePlans(result *SyncResourcesResult, plans []durableResultPlan, remoteType string) {
	for _, plan := range plans {
		result.Failures = append(result.Failures, newResourceError(plan.remoteID, remoteType, errors.New("atomic staged batch rejected because another item failed validation")))
	}
	result.SuccessCount = 0
	result.FailureCount = len(result.Failures)
}

func (s *Service) commitDurableSyncBatch(ctx context.Context, batch cache.SyncBatch) error {
	store, ok := s.store.(durableSyncBatchStore)
	if !ok {
		return errors.New("cache store does not support atomic durable sync batches")
	}
	return store.CommitSyncBatch(ctx, batch)
}

func durableZeroDelta(counts SyncCounts) bool {
	return counts.Fetched > 0 && counts.Skipped == counts.Fetched && counts.Updated == 0 && counts.Inserted == 0 && counts.Conflicts == 0
}

func durableResults(plans []durableResultPlan) []SyncResult {
	results := make([]SyncResult, 0, len(plans))
	for _, plan := range plans {
		source := plan.graph.Source
		source.Aliases = append([]cache.Identity(nil), plan.graph.Identities...)
		results = append(results, SyncResult{IdempotencyKey: plan.idempotencyKey, Status: "succeeded", Counts: plan.counts, SyncEventID: plan.eventID, Freshness: string(FreshnessFresh), Record: sourceSummary(source), GeneratedAt: plan.completedAt, StartedAt: plan.completedAt, CompletedAt: plan.completedAt, ZeroDelta: plan.zeroDelta})
	}
	return results
}
