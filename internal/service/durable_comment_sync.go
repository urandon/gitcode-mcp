package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
)

// DurableIssueCommentSyncBatch contains provider-complete comment lists grouped
// by their already-cached issue parent. The provider bodies live only in the
// checksummed staging journal until CommitIssueCommentSyncBatch publishes the
// normalized cache graphs atomically.
type DurableIssueCommentSyncBatch struct {
	Version             int                        `json:"version"`
	RepoID              string                     `json:"repo_id"`
	Collection          string                     `json:"collection"`
	IdempotencyKey      string                     `json:"idempotency_key"`
	Groups              []DurableIssueCommentGroup `json:"groups"`
	PagesListed         int                        `json:"pages_listed"`
	RecordsListed       int                        `json:"records_listed"`
	StopReason          string                     `json:"stop_reason"`
	TraversalStatus     string                     `json:"traversal_status"`
	ProviderRevision    string                     `json:"provider_revision"`
	FetchedAt           time.Time                  `json:"fetched_at"`
	MaintenanceFrontier *cache.MaintenanceFrontier `json:"-"`
	CommitReceipt       *cache.SyncCommitReceipt   `json:"-"`
}

type DurableIssueCommentGroup struct {
	Parent   cache.IssueCommentSync `json:"parent"`
	Comments []gitcode.Comment      `json:"comments"`
}

func (b DurableIssueCommentSyncBatch) RecordCount() int {
	total := len(b.Groups)
	for _, group := range b.Groups {
		total += len(group.Comments)
	}
	return total
}

func (s *Service) FetchIssueCommentSyncBatch(ctx context.Context, req BulkSyncRequest) (DurableIssueCommentSyncBatch, error) {
	repoID, err := s.requireRepo(ctx, req.RepoID, "bulk-sync-issue-comments-fetch")
	if err != nil {
		return DurableIssueCommentSyncBatch{}, err
	}
	req.RepoID = repoID
	s.ensureBulkIdempotencyKey(&req, "issue_comments")
	if err := s.validateRepoScope(ctx, repoID, "issues"); err != nil {
		return DurableIssueCommentSyncBatch{}, err
	}

	// Fetch is strictly read/provider-only. Legacy queue repair remains a
	// separate cache-local maintenance concern; it must never make remote fetch
	// admission depend on an available SQLite writer.
	limit := 0
	if req.Bounds != nil {
		limit = req.Bounds.MaxRecords
		if limit <= 0 && req.Bounds.MaxPages > 0 {
			limit = req.Bounds.MaxPages
		}
	}
	pending, err := s.store.ListIssueCommentSync(ctx, cache.IssueCommentSyncFilter{RepoID: repoID, Statuses: []string{"pending", "deferred"}, Limit: limit})
	if err != nil {
		return DurableIssueCommentSyncBatch{}, err
	}
	route, err := s.BuildAdapterRoute(ctx, repoID, RepositoryScopeIssues)
	if err != nil {
		return DurableIssueCommentSyncBatch{}, err
	}
	batch := DurableIssueCommentSyncBatch{
		Version: DurableSyncBatchVersion, RepoID: repoID, Collection: "issue_comments",
		IdempotencyKey: req.IdempotencyKey, StopReason: "queue_empty", TraversalStatus: "complete",
		FetchedAt: s.now().UTC(),
	}
	for index, item := range pending {
		if err := ctx.Err(); err != nil {
			batch.StopReason, batch.TraversalStatus = "cancelled", "cancelled"
			if errors.Is(err, context.DeadlineExceeded) {
				batch.StopReason, batch.TraversalStatus = "timeout", "timeout"
			}
			batch.ProviderRevision = durableIssueCommentProviderRevision(batch.Groups)
			return batch, err
		}
		page, listErr := s.client.ListIssueComments(ctx, gitcode.IssueRequest{Owner: route.Owner, Repo: route.Name, Number: item.IssueNumber, KnownRemoteAlias: true, RemoteAlias: item.RemoteID})
		if listErr != nil {
			batch.StopReason, batch.TraversalStatus = "provider_failure", "partial"
			batch.ProviderRevision = durableIssueCommentProviderRevision(batch.Groups)
			return batch, s.normalizeSyncFailure(listErr, SyncRequest{RepoID: repoID, RemoteAlias: fmt.Sprintf("issue_comment:%d:*", item.IssueNumber)}, "issue_comments", item.RemoteID)
		}
		for _, comment := range page.Items {
			if s.syncProviderMode() == gitcode.ProviderModeLive && !s.liveCommentParentReconciles(comment, item.RemoteID, item.ProviderID) {
				batch.StopReason, batch.TraversalStatus = "provider_graph_invalid", "partial"
				batch.ProviderRevision = durableIssueCommentProviderRevision(batch.Groups)
				return batch, s.liveGraphError("comment parent issue id is unreconciled")
			}
		}
		batch.Groups = append(batch.Groups, DurableIssueCommentGroup{Parent: item, Comments: page.Items})
		batch.PagesListed++
		batch.RecordsListed += len(page.Items)
		batch.StopReason = "queue_drained"
		emitProgress(req.ProgressChan, ProgressEvent{Collection: "issue_comments", Phase: "fetching", Page: index + 1, RecordsListed: len(page.Items), RecordsFetched: len(page.Items)})
	}
	if limit > 0 && len(pending) == limit {
		batch.StopReason, batch.TraversalStatus = "max_records", "bounded"
	}
	batch.ProviderRevision = durableIssueCommentProviderRevision(batch.Groups)
	return batch, nil
}

func (s *Service) CommitIssueCommentSyncBatch(ctx context.Context, batch DurableIssueCommentSyncBatch, progress chan<- ProgressEvent) (*SyncResourcesResult, error) {
	if err := validateDurableIssueCommentBatch(batch); err != nil {
		return nil, err
	}
	repoID, err := s.requireRepo(ctx, batch.RepoID, "bulk-sync-issue-comments-commit")
	if err != nil {
		return nil, err
	}
	if repoID != batch.RepoID {
		return nil, ErrInvalidQuery{Field: "repo_id", Message: "staged repository binding changed before commit"}
	}
	ctx, releaseWriter, err := s.acquireBulkWriter(ctx, repoID, "bulk-sync-issue-comments-commit")
	if err != nil {
		return nil, err
	}
	defer releaseWriter()
	if err := s.validateRepoScope(ctx, repoID, "issues"); err != nil {
		return nil, err
	}
	result := &SyncResourcesResult{Results: []SyncResult{}, Failures: []ResourceError{}, PagesListed: batch.PagesListed, RecordsListed: batch.RecordsListed, StopReason: batch.StopReason, TraversalStatus: batch.TraversalStatus, Ordering: "queue_updated_at_asc"}
	commit := cache.SyncBatch{}
	for _, group := range batch.Groups {
		item := group.Parent
		current, ok, currentErr := s.store.GetIssueCommentSync(ctx, repoID, item.SourceID)
		if currentErr != nil || !ok || current.IssueNumber != item.IssueNumber || current.RemoteRevision != item.RemoteRevision {
			if currentErr == nil {
				currentErr = ErrInvalidQuery{Field: "batch.groups", Message: "issue comment parent queue changed after fetch"}
			}
			result.Failures = append(result.Failures, newResourceError(item.RemoteID, "issue_comments", currentErr))
			continue
		}
		parent, parentErr := s.store.GetSourceScoped(ctx, repoID, item.SourceID)
		if parentErr != nil || parent.Kind != "issue" {
			if parentErr == nil {
				parentErr = ErrInvalidQuery{Field: "batch.groups", Message: "issue comment parent no longer resolves to an issue"}
			}
			result.Failures = append(result.Failures, newResourceError(item.RemoteID, "issue_comments", parentErr))
			continue
		}
		now := s.now().UTC()
		cached := make([]cache.RecordComment, 0, len(group.Comments))
		keep := make([]string, 0, len(group.Comments))
		for _, comment := range group.Comments {
			recordComment, stageErr := cachedIssueComment(comment, item, now)
			if stageErr != nil {
				result.Failures = append(result.Failures, newResourceError(strings.TrimSpace(comment.ID), "issue_comments", stageErr))
				continue
			}
			graph, sourceID, stageErr := s.stageIssueCommentProjection(ctx, item, recordComment)
			if stageErr != nil {
				result.Failures = append(result.Failures, newResourceError(strings.TrimSpace(comment.ID), "issue_comments", stageErr))
				continue
			}
			cached = append(cached, recordComment)
			keep = append(keep, sourceID)
			commit.Graphs = append(commit.Graphs, s.syncGraphFromSourceGraph(repoID, graph))
		}
		item.Status = "complete"
		item.ExpectedCount = len(cached)
		item.Attempts++
		item.LastErrorClass, item.RetryAfter = "", ""
		item.LastAttemptAt, item.UpdatedAt = now, now
		commit.IssueCommentSyncs = append(commit.IssueCommentSyncs, item)
		commit.ReplaceRecordComments = append(commit.ReplaceRecordComments, cache.RecordCommentsReplacement{RepoID: repoID, RecordID: item.SourceID, Comments: cached})
		sort.Strings(keep)
		commit.ReconcileChildren = append(commit.ReconcileChildren, cache.ChildSourceReconciliation{RepoID: repoID, ParentID: item.SourceID, Kind: "issue_comment", KeepSourceIDs: keep})
		result.Results = append(result.Results, SyncResult{Status: "succeeded", Freshness: string(FreshnessFresh), Counts: SyncCounts{Fetched: 1, FetchedDetail: 1}, Record: sourceSummary(parent), GeneratedAt: now, StartedAt: now, CompletedAt: now})
	}
	result.FailureCount = len(result.Failures)
	if result.FailureCount > 0 {
		result.Results = nil
		result.SuccessCount = 0
		result.TraversalStatus = "partial"
		return result, &PartialSyncError{Errors: result.Failures, FailureCount: result.FailureCount}
	}
	commit.MaintenanceFrontier = batch.MaintenanceFrontier
	commit.Receipt = batch.CommitReceipt
	if err := s.commitDurableSyncBatch(ctx, commit); err != nil {
		return bulkSyncFailureResult(err, "issue_comment:*", "issue_comments")
	}
	result.SuccessCount = len(result.Results)
	emitProgress(progress, ProgressEvent{Collection: "issue_comments", Phase: "committing", RecordsListed: batch.RecordsListed, RecordsFetched: result.SuccessCount})
	_ = s.attachIssueCommentQueueSummary(ctx, result, repoID, "drain")
	return result, nil
}

func validateDurableIssueCommentBatch(batch DurableIssueCommentSyncBatch) error {
	if batch.Version != DurableSyncBatchVersion || strings.TrimSpace(batch.RepoID) == "" || batch.Collection != "issue_comments" || strings.TrimSpace(batch.IdempotencyKey) == "" || strings.TrimSpace(batch.ProviderRevision) == "" {
		return ErrInvalidQuery{Field: "batch", Message: "staged issue comment batch is incompatible or incomplete"}
	}
	for _, group := range batch.Groups {
		if group.Parent.RepoID != batch.RepoID || strings.TrimSpace(group.Parent.SourceID) == "" || group.Parent.IssueNumber <= 0 {
			return ErrInvalidQuery{Field: "batch.groups", Message: "staged issue comment parent identity is incomplete"}
		}
	}
	return nil
}

func durableIssueCommentProviderRevision(groups []DurableIssueCommentGroup) string {
	parts := []any{"issue-comment-batch"}
	for _, group := range groups {
		parts = append(parts, group.Parent.SourceID, group.Parent.RemoteRevision, len(group.Comments))
		for _, comment := range group.Comments {
			parts = append(parts, strings.TrimSpace(comment.ID), comment.UpdatedAt.UTC())
		}
	}
	return contentHash(parts...)
}

type DurablePRCommentSyncBatch struct {
	Version             int                        `json:"version"`
	RepoID              string                     `json:"repo_id"`
	Collection          string                     `json:"collection"`
	IdempotencyKey      string                     `json:"idempotency_key"`
	Items               []DurablePRCommentItem     `json:"items"`
	PagesListed         int                        `json:"pages_listed"`
	RecordsListed       int                        `json:"records_listed"`
	StopReason          string                     `json:"stop_reason"`
	TraversalStatus     string                     `json:"traversal_status"`
	ProviderRevision    string                     `json:"provider_revision"`
	FetchedAt           time.Time                  `json:"fetched_at"`
	MaintenanceFrontier *cache.MaintenanceFrontier `json:"-"`
	CommitReceipt       *cache.SyncCommitReceipt   `json:"-"`
}

type DurablePRCommentItem struct {
	PRNumber       int               `json:"pr_number"`
	ParentSourceID string            `json:"parent_source_id"`
	Comment        gitcode.PRComment `json:"comment"`
}

func (b DurablePRCommentSyncBatch) RecordCount() int { return len(b.Items) }

func (s *Service) FetchPRCommentSyncBatch(ctx context.Context, req BulkSyncRequest) (DurablePRCommentSyncBatch, error) {
	target := 0
	if strings.TrimSpace(req.RemoteAlias) != "" {
		if req.Page != 0 || req.PerPage != 0 || (req.Bounds != nil && (req.Bounds.MaxPages != 0 || req.Bounds.MaxRecords != 0)) {
			return DurablePRCommentSyncBatch{}, ErrInvalidQuery{Field: "bounds", Message: "targeted PR comment sync does not accept pagination bounds"}
		}
		var err error
		target, err = targetedPRCommentNumber(req.RemoteAlias)
		if err != nil {
			return DurablePRCommentSyncBatch{}, err
		}
	}
	repoID, err := s.requireRepo(ctx, req.RepoID, "bulk-sync-pr-comments-fetch")
	if err != nil {
		return DurablePRCommentSyncBatch{}, err
	}
	req.RepoID = repoID
	s.ensureBulkIdempotencyKey(&req, "pr_comments")
	if err := s.validateRepoScope(ctx, repoID, "pull_request"); err != nil {
		return DurablePRCommentSyncBatch{}, err
	}
	route, err := s.BuildAdapterRoute(ctx, repoID, RepositoryScopeIssues)
	if err != nil {
		return DurablePRCommentSyncBatch{}, err
	}
	var sources []cache.Source
	if target > 0 {
		identity, resolveErr := s.store.ResolveAliasScoped(ctx, repoID, cache.RemoteAlias{Type: "pull_request", ID: strconv.Itoa(target)})
		if resolveErr != nil {
			return DurablePRCommentSyncBatch{}, resolveErr
		}
		source, sourceErr := s.store.GetSourceScoped(ctx, repoID, identity.SourceID)
		if sourceErr != nil {
			return DurablePRCommentSyncBatch{}, sourceErr
		}
		sources = []cache.Source{source}
	} else {
		sources, err = s.store.ListSources(ctx, cache.SourceFilter{RepoID: repoID, Kind: "pull_request"})
		if err != nil && !isCacheNotFound(err) {
			return DurablePRCommentSyncBatch{}, err
		}
	}
	sort.SliceStable(sources, func(i, j int) bool { return sources[i].ID < sources[j].ID })
	start := 0
	if req.Bounds != nil && req.Page > 1 {
		start = min(req.Page-1, len(sources))
	}
	end := len(sources)
	bounded := false
	if req.Bounds != nil && req.Bounds.MaxPages > 0 && end > start+req.Bounds.MaxPages {
		end, bounded = start+req.Bounds.MaxPages, true
	}
	sources = sources[start:end]
	batch := DurablePRCommentSyncBatch{Version: DurableSyncBatchVersion, RepoID: repoID, Collection: "pr_comments", IdempotencyKey: req.IdempotencyKey, StopReason: "end_of_collection", TraversalStatus: "complete", FetchedAt: s.now().UTC()}
	if bounded {
		batch.StopReason, batch.TraversalStatus = "max_pages", "bounded"
	}
	for index, source := range sources {
		number := target
		if number == 0 {
			var ok bool
			number, ok = pullRequestNumberFromSource(source)
			if !ok {
				batch.ProviderRevision = durablePRCommentProviderRevision(batch.Items)
				return batch, ErrInvalidQuery{Field: "pull_request", Message: "cached pull request has no numeric remote alias"}
			}
		}
		page, listErr := s.client.ListPRComments(ctx, gitcode.PRRequest{Owner: route.Owner, Repo: route.Name, Number: number})
		if listErr != nil {
			batch.StopReason, batch.TraversalStatus = "provider_failure", "partial"
			batch.ProviderRevision = durablePRCommentProviderRevision(batch.Items)
			return batch, s.normalizeSyncFailure(listErr, SyncRequest{RepoID: repoID, RemoteAlias: fmt.Sprintf("pr_comment:%d:*", number)}, "pr_comments", strconv.Itoa(number))
		}
		items := page.Items
		if req.Bounds != nil && req.Bounds.MaxRecords > 0 {
			remaining := req.Bounds.MaxRecords - len(batch.Items)
			if remaining <= 0 {
				batch.StopReason, batch.TraversalStatus = "max_records", "bounded"
				break
			}
			if len(items) > remaining {
				items = items[:remaining]
				batch.StopReason, batch.TraversalStatus = "max_records", "bounded"
			}
		}
		for _, comment := range items {
			batch.Items = append(batch.Items, DurablePRCommentItem{PRNumber: number, ParentSourceID: source.ID, Comment: comment})
		}
		batch.PagesListed++
		batch.RecordsListed += len(items)
		emitProgress(req.ProgressChan, ProgressEvent{Collection: "pr_comments", Phase: "fetching", Page: index + 1, RecordsListed: len(items), RecordsFetched: len(items)})
	}
	batch.ProviderRevision = durablePRCommentProviderRevision(batch.Items)
	return batch, nil
}

func (s *Service) CommitPRCommentSyncBatch(ctx context.Context, batch DurablePRCommentSyncBatch, progress chan<- ProgressEvent) (*SyncResourcesResult, error) {
	if err := validateDurablePRCommentBatch(batch); err != nil {
		return nil, err
	}
	repoID, err := s.requireRepo(ctx, batch.RepoID, "bulk-sync-pr-comments-commit")
	if err != nil {
		return nil, err
	}
	if repoID != batch.RepoID {
		return nil, ErrInvalidQuery{Field: "repo_id", Message: "staged repository binding changed before commit"}
	}
	ctx, releaseWriter, err := s.acquireBulkWriter(ctx, repoID, "bulk-sync-pr-comments-commit")
	if err != nil {
		return nil, err
	}
	defer releaseWriter()
	result := &SyncResourcesResult{Results: []SyncResult{}, Failures: []ResourceError{}, PagesListed: batch.PagesListed, RecordsListed: batch.RecordsListed, StopReason: batch.StopReason, TraversalStatus: batch.TraversalStatus, Ordering: "source_id_asc"}
	graphs := make([]cache.SyncGraph, 0, len(batch.Items))
	plans := make([]durableResultPlan, 0, len(batch.Items))
	for _, item := range batch.Items {
		parent, parentErr := s.store.GetSourceScoped(ctx, repoID, item.ParentSourceID)
		parentNumber, parentNumberOK := pullRequestNumberFromSource(parent)
		if parentErr != nil || parent.Kind != "pull_request" || !parentNumberOK || parentNumber != item.PRNumber {
			if parentErr == nil {
				parentErr = ErrInvalidQuery{Field: "batch.items", Message: "PR comment parent binding changed after fetch"}
			}
			result.Failures = append(result.Failures, newResourceError(strconv.Itoa(item.PRNumber), "pr_comment", parentErr))
			continue
		}
		remoteID := prCommentRemoteID(item.PRNumber, item.Comment.ID)
		req := SyncRequest{RepoID: repoID, AliasType: "pr_comment", AliasID: remoteID, ParentSourceID: item.ParentSourceID, IdempotencyKey: scopedBulkSyncKey(batch.IdempotencyKey, "pr_comment", remoteID)}
		graph, counts, stageErr := s.stagePRComment(ctx, req, "pr_comment", remoteID, item.PRNumber, item.Comment)
		if stageErr != nil {
			result.Failures = append(result.Failures, newResourceError(remoteID, "pr_comment", stageErr))
			continue
		}
		counts.Listed = 1
		now := s.now().UTC()
		eventID := syncEventID(req.IdempotencyKey)
		zeroDelta := durableZeroDelta(counts)
		graph.SyncEvents = append(graph.SyncEvents, cache.SyncEvent{ID: eventID, SourceID: graph.Source.ID, RemoteType: "pr_comment", RemoteID: remoteID, RemoteRevision: graph.SyncStatus.RemoteRevision, Status: "succeeded", IdempotencyKey: req.IdempotencyKey, Message: syncEventMessage(counts), CreatedAt: now, StartedAt: now, CompletedAt: now, ZeroDelta: zeroDelta})
		graphs = append(graphs, s.syncGraphFromSourceGraph(repoID, graph))
		plans = append(plans, durableResultPlan{graph: graph, remoteID: remoteID, counts: counts, idempotencyKey: req.IdempotencyKey, eventID: eventID, completedAt: now, zeroDelta: zeroDelta})
	}
	result.FailureCount = len(result.Failures)
	if result.FailureCount > 0 {
		abortDurablePlans(result, plans, "pr_comment")
		return result, &PartialSyncError{Errors: result.Failures, FailureCount: result.FailureCount}
	}
	if err := s.commitDurableSyncBatch(ctx, cache.SyncBatch{Graphs: graphs, MaintenanceFrontier: batch.MaintenanceFrontier, Receipt: batch.CommitReceipt}); err != nil {
		return bulkSyncFailureResult(err, "pr_comment:*", "pr_comments")
	}
	result.Results = durableResults(plans)
	result.SuccessCount = len(result.Results)
	emitProgress(progress, ProgressEvent{Collection: "pr_comments", Phase: "committing", RecordsListed: batch.RecordsListed, RecordsFetched: result.SuccessCount})
	return result, nil
}

func validateDurablePRCommentBatch(batch DurablePRCommentSyncBatch) error {
	if batch.Version != DurableSyncBatchVersion || strings.TrimSpace(batch.RepoID) == "" || batch.Collection != "pr_comments" || strings.TrimSpace(batch.IdempotencyKey) == "" || strings.TrimSpace(batch.ProviderRevision) == "" {
		return ErrInvalidQuery{Field: "batch", Message: "staged PR comment batch is incompatible or incomplete"}
	}
	for _, item := range batch.Items {
		if item.PRNumber <= 0 || strings.TrimSpace(item.ParentSourceID) == "" {
			return ErrInvalidQuery{Field: "batch.items", Message: "staged PR comment parent identity is incomplete"}
		}
	}
	return nil
}

func durablePRCommentProviderRevision(items []DurablePRCommentItem) string {
	parts := []any{"pr-comment-batch"}
	for _, item := range items {
		parts = append(parts, item.PRNumber, item.ParentSourceID, strings.TrimSpace(item.Comment.ID), strings.TrimSpace(item.Comment.DiscussionID), item.Comment.UpdatedAt.UTC())
	}
	return contentHash(parts...)
}
