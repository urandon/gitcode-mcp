package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
)

type Service struct {
	store    cache.Store
	client   gitcode.Client
	now      func() time.Time
	lockPath string
}

func New(store cache.Store) *Service {
	return &Service{store: store, now: func() time.Time { return time.Now().UTC() }, lockPath: "gitcode-mcp-sync.lock"}
}

func NewWithClient(store cache.Store, client gitcode.Client) *Service {
	svc := New(store)
	svc.client = client
	return svc
}

func (s *Service) SearchSources(ctx context.Context, req SearchSourcesRequest) ([]SearchSourceResult, error) {
	if strings.TrimSpace(req.Query) == "" {
		return nil, ErrInvalidQuery{Field: "query", Message: "query is required"}
	}
	results, err := s.store.SearchSources(ctx, cache.SearchQuery{Query: req.Query, Kind: req.Kind, Limit: req.Limit})
	if err != nil {
		return nil, normalizeError(err, "search", req.Query)
	}
	if len(results) == 0 {
		return nil, ErrCacheEmpty{Message: "no cached search results"}
	}
	out := make([]SearchSourceResult, 0, len(results))
	updated := map[string]time.Time{}
	for _, result := range results {
		source, err := s.store.GetSource(ctx, result.ID)
		if err != nil {
			return nil, normalizeError(err, "source", result.ID)
		}
		updated[result.ID] = source.UpdatedAt.UTC()
		line := nullableLine(result.Line)
		out = append(out, SearchSourceResult{ID: result.ID, Path: result.Path, Title: result.Title, Kind: source.Kind, Status: source.Status, Snippet: result.Snippet, LineStart: line, LineEnd: line, Score: result.Score})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if !updated[out[i].ID].Equal(updated[out[j].ID]) {
			return updated[out[i].ID].Before(updated[out[j].ID])
		}
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}

func (s *Service) GetSource(ctx context.Context, req GetSourceRequest) (SourceRecord, error) {
	id, err := s.resolveStableID(ctx, req.ID, req.AliasType, req.AliasID)
	if err != nil {
		return SourceRecord{}, err
	}
	source, err := s.store.GetSource(ctx, id)
	if err != nil {
		return SourceRecord{}, normalizeError(err, "source", id)
	}
	links, err := s.store.ListLinks(ctx, cache.LinkFilter{SourceID: source.ID})
	if err != nil {
		return SourceRecord{}, normalizeError(err, "links", source.ID)
	}
	backlinks, err := s.GetBacklinks(ctx, GetBacklinksRequest{ID: source.ID})
	if err != nil && !IsCacheEmpty(err) {
		return SourceRecord{}, err
	}
	return sourceRecord(source, links, backlinks), nil
}

func (s *Service) ListSources(ctx context.Context, req ListSourcesRequest) ([]SourceSummary, error) {
	sources, err := s.store.ListSources(ctx, cache.SourceFilter{Kind: req.Kind, Status: req.Status, Limit: req.limitPlusOffset()})
	if err != nil {
		return nil, normalizeError(err, "sources", "")
	}
	if len(sources) == 0 {
		return nil, ErrCacheEmpty{Message: "cache has no sources"}
	}
	sort.SliceStable(sources, func(i, j int) bool {
		if sources[i].ID != sources[j].ID {
			return sources[i].ID < sources[j].ID
		}
		return sources[i].Path < sources[j].Path
	})
	sources = sliceSources(sources, req.Offset, req.Limit)
	out := make([]SourceSummary, 0, len(sources))
	for _, source := range sources {
		out = append(out, sourceSummary(source))
	}
	return out, nil
}

func (s *Service) GetBacklinks(ctx context.Context, req GetBacklinksRequest) ([]BacklinkResult, error) {
	id, err := s.resolveStableID(ctx, req.ID, req.AliasType, req.AliasID)
	if err != nil {
		return nil, err
	}
	backlinks, err := s.store.GetBacklinks(ctx, id)
	if err != nil {
		return nil, normalizeError(err, "backlinks", id)
	}
	out := make([]BacklinkResult, 0, len(backlinks))
	for _, source := range backlinks {
		out = append(out, BacklinkResult{SourceSummary: sourceSummary(source), TargetID: id})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Service) ResolveID(ctx context.Context, req ResolveIDRequest) (ResolvedID, error) {
	id, err := s.resolveStableID(ctx, req.ID, req.AliasType, req.AliasID)
	if err != nil {
		return ResolvedID{}, err
	}
	source, err := s.store.GetSource(ctx, id)
	if err != nil {
		return ResolvedID{}, normalizeError(err, "source", id)
	}
	return ResolvedID{ID: source.ID, Path: source.Path, RemoteAlias: remoteAlias(source.Aliases), Kind: source.Kind, Title: source.Title}, nil
}

func (s *Service) GetSnippet(ctx context.Context, req SnippetRequest) (SnippetResult, error) {
	id, err := s.resolveStableID(ctx, req.ID, req.AliasType, req.AliasID)
	if err != nil {
		return SnippetResult{}, err
	}
	if req.LineStart > 0 || req.LineEnd > 0 {
		return s.snippetFromLines(ctx, id, req.LineStart, req.LineEnd)
	}
	if req.ChunkID != "" {
		return s.snippetFromChunk(ctx, id, req.ChunkID)
	}
	return SnippetResult{}, ErrInvalidQuery{Field: "range", Message: "line range or chunk id is required"}
}

func (s *Service) GetSyncStatus(ctx context.Context, req SyncStatusRequest) (SyncStatusResult, error) {
	id, err := s.resolveStableID(ctx, req.ID, req.AliasType, req.AliasID)
	if err != nil {
		return SyncStatusResult{}, err
	}
	source, err := s.store.GetSource(ctx, id)
	if err != nil {
		return SyncStatusResult{}, normalizeError(err, "source", id)
	}
	status, err := s.store.GetSyncStatus(ctx, id)
	if err != nil {
		return SyncStatusResult{}, normalizeError(err, "sync status", id)
	}
	freshness := freshnessFor(source, status)
	return SyncStatusResult{SourceID: status.SourceID, RemoteType: status.RemoteType, RemoteID: status.RemoteID, RemoteRevision: status.RemoteRevision, Status: status.Status, Freshness: freshness, LocalUpdatedAt: source.UpdatedAt.UTC(), LastFetchedAt: status.LastFetchedAt.UTC()}, nil
}

func (s *Service) RecentChanges(ctx context.Context, req RecentChangesRequest) ([]RecentChangeResult, error) {
	sources, err := s.store.ListSources(ctx, cache.SourceFilter{Kind: req.Kind, Status: req.Status})
	if err != nil {
		return nil, normalizeError(err, "sources", "")
	}
	if len(sources) == 0 {
		return nil, ErrCacheEmpty{Message: "cache has no sources"}
	}
	sort.SliceStable(sources, func(i, j int) bool {
		if !sources[i].UpdatedAt.Equal(sources[j].UpdatedAt) {
			return sources[i].UpdatedAt.After(sources[j].UpdatedAt)
		}
		return sources[i].ID < sources[j].ID
	})
	sources = sliceSources(sources, req.Offset, req.Limit)
	out := make([]RecentChangeResult, 0, len(sources))
	for _, source := range sources {
		out = append(out, RecentChangeResult{ID: source.ID, Path: source.Path, Title: source.Title, Kind: source.Kind, Status: source.Status, UpdatedAt: source.UpdatedAt.UTC()})
	}
	return out, nil
}

func (s *Service) LinkCheck(ctx context.Context, req LinkCheckRequest) (LinkCheckResult, error) {
	links, err := s.store.ListLinks(ctx, cache.LinkFilter{})
	if err != nil {
		return LinkCheckResult{}, normalizeError(err, "links", "")
	}
	result := LinkCheckResult{CheckedCount: len(links), SuggestedAliases: map[string][]string{}}
	for _, link := range links {
		if _, err := s.store.GetSource(ctx, link.TargetID); err != nil {
			if isCacheNotFound(err) {
				result.BrokenLinks = append(result.BrokenLinks, BrokenLinkResult{SourceID: link.SourceID, TargetID: link.TargetID, Kind: link.Kind, Text: link.Text})
				continue
			}
			return LinkCheckResult{}, normalizeError(err, "source", link.TargetID)
		}
	}
	result.BrokenCount = len(result.BrokenLinks)
	if req.Strict && result.BrokenCount > 0 {
		return result, ErrLinkCheckFailed{BrokenCount: result.BrokenCount}
	}
	return result, nil
}

func (s *Service) StaleIndex(ctx context.Context, req StaleIndexRequest) (StaleIndexResult, error) {
	links, err := s.store.ListLinks(ctx, cache.LinkFilter{})
	if err != nil {
		return StaleIndexResult{}, normalizeError(err, "links", "")
	}
	missing := map[string]struct{}{}
	affected := map[string]struct{}{}
	for _, link := range links {
		if _, err := s.store.GetSource(ctx, link.TargetID); err != nil {
			if isCacheNotFound(err) {
				missing[link.TargetID] = struct{}{}
				affected[link.SourceID] = struct{}{}
				continue
			}
			return StaleIndexResult{}, normalizeError(err, "source", link.TargetID)
		}
	}
	result := StaleIndexResult{StaleCount: len(missing), AffectedSourceIDs: sortedKeys(affected), MissingTargetIDs: sortedKeys(missing)}
	if req.Strict && result.StaleCount > 0 {
		return result, ErrStaleIndex{StaleCount: result.StaleCount}
	}
	return result, nil
}

func (s *Service) ExportSnapshot(ctx context.Context, req ExportSnapshotRequest) (ExportSnapshotResult, error) {
	content, count, err := s.renderSnapshot(ctx, req.Format)
	if err != nil {
		return ExportSnapshotResult{}, err
	}
	hash := sha256.Sum256([]byte(content))
	result := ExportSnapshotResult{SnapshotID: hex.EncodeToString(hash[:16]), Format: normalizeFormat(req.Format), RecordCount: count, GeneratedAt: s.now().UTC(), ContentHash: hex.EncodeToString(hash[:]), InlineContent: content}
	if req.OutputPath != "" && (req.InlineLimit <= 0 || len(content) > req.InlineLimit) {
		result.OutputPath = req.OutputPath
		result.InlineContent = ""
	}
	return result, nil
}

func (s *Service) DiffSnapshot(ctx context.Context, req DiffSnapshotRequest) (DiffSnapshotResult, error) {
	head, _, err := s.renderSnapshot(ctx, req.Format)
	if err != nil {
		return DiffSnapshotResult{}, err
	}
	base := req.BaseContent
	if base == "" {
		base = req.BaseSnapshotContent
	}
	ids := changedIDs(base, head)
	return DiffSnapshotResult{BaseSnapshotID: req.BaseSnapshotID, HeadSnapshotID: req.HeadSnapshotID, Format: normalizeFormat(req.Format), ChangedSourceIDs: ids, AddedSourceIDs: ids, ModifiedSourceIDs: []string{}, RemovedSourceIDs: []string{}, DiffText: simpleDiff(base, head)}, nil
}

func (s *Service) Ingest(ctx context.Context, req OperationRequest) (OperationResult, error) {
	return s.operationResult(ctx, "ingest", req)
}

func (s *Service) Index(ctx context.Context, req OperationRequest) (OperationResult, error) {
	return s.operationResult(ctx, "index", req)
}

func (s *Service) SyncToCache(ctx context.Context, req SyncRequest) (SyncResult, error) {
	if err := ctx.Err(); err != nil {
		return SyncResult{}, err
	}
	key := strings.TrimSpace(req.IdempotencyKey)
	if key == "" {
		key = s.syncIdempotencyKey(req)
	}
	if event, err := s.store.GetSyncEventByKey(ctx, key); err != nil {
		return SyncResult{}, err
	} else if event != nil {
		switch event.Status {
		case "succeeded":
			result := syncResultFromEvent(*event, s.now().UTC())
			result.Replayed = true
			return result, nil
		case "in_progress":
			return SyncResult{}, ErrSyncInProgress{EventID: event.ID, IdempotencyKey: key}
		}
	}
	lock, err := s.store.AcquireLock(ctx, s.lockPath)
	if err != nil {
		return SyncResult{}, err
	}
	defer s.store.ReleaseLock(context.Background(), lock)
	remoteType, remoteID, err := s.syncTarget(ctx, req)
	if err != nil {
		return SyncResult{}, err
	}
	eventID := syncEventID(key)
	now := s.now().UTC()
	inProgress := cache.SyncEvent{ID: eventID, SourceID: s.syncEventSourceID(ctx, req, remoteType, remoteID), RemoteType: remoteType, RemoteID: remoteID, Status: "in_progress", IdempotencyKey: key, Message: "sync started", CreatedAt: now}
	if err := s.store.RecordSyncEvent(ctx, inProgress); err != nil {
		return SyncResult{}, err
	}
	if err := s.store.IntegrityCheck(ctx); err != nil {
		failure := s.normalizeSyncFailure(err, req, remoteType, remoteID)
		_ = s.store.RecordSyncEvent(ctx, failedSyncEvent(inProgress, failure, s.now().UTC()))
		return SyncResult{}, failure
	}
	graph, counts, err := s.fetchAndStage(ctx, req, remoteType, remoteID)
	if err != nil {
		failure := s.normalizeSyncFailure(err, req, remoteType, remoteID)
		_ = s.store.RecordSyncEvent(ctx, failedSyncEvent(inProgress, failure, s.now().UTC()))
		if markErr := s.markMissingRemote(ctx, inProgress, failure, remoteType, remoteID); markErr != nil {
			return SyncResult{}, markErr
		}
		return SyncResult{}, failure
	}
	graph.SyncEvents = append(graph.SyncEvents, cache.SyncEvent{ID: eventID, SourceID: graph.Source.ID, RemoteType: remoteType, RemoteID: remoteID, RemoteRevision: graph.SyncStatus.RemoteRevision, Status: "succeeded", IdempotencyKey: key, Message: syncEventMessage(counts), CreatedAt: now})
	if err := s.store.UpsertSourceGraph(ctx, graph); err != nil {
		_ = s.store.RecordSyncEvent(ctx, failedSyncEvent(inProgress, err, s.now().UTC()))
		return SyncResult{}, err
	}
	return SyncResult{IdempotencyKey: key, Status: "succeeded", Counts: counts, SyncEventID: eventID, Freshness: string(FreshnessFresh), GeneratedAt: s.now().UTC()}, nil
}

func (s *Service) CreateIssue(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if strings.TrimSpace(req.Title) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "title", Message: "title is required"}
	}
	return s.writeResult(ctx, "create-issue", req)
}

func (s *Service) UpdateIssue(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if req.Number == 0 && strings.TrimSpace(req.ID) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "issue", Message: "number or id is required"}
	}
	return s.writeResult(ctx, "update-issue", req)
}

func (s *Service) CreatePage(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Body) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "page", Message: "title and body are required"}
	}
	return s.writeResult(ctx, "create-page", req)
}

func (s *Service) UpdatePage(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if strings.TrimSpace(req.Slug) == "" && strings.TrimSpace(req.ID) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "page", Message: "slug or id is required"}
	}
	return s.writeResult(ctx, "update-page", req)
}

func (s *Service) AddComment(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if (req.Number == 0 && strings.TrimSpace(req.ID) == "") || strings.TrimSpace(req.Body) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "comment", Message: "issue and body are required"}
	}
	return s.writeResult(ctx, "add-comment", req)
}

func (s *Service) AddLabel(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if (req.Number == 0 && strings.TrimSpace(req.ID) == "") || strings.TrimSpace(req.Label) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "label", Message: "issue and label are required"}
	}
	return s.writeResult(ctx, "add-label", req)
}

func (s *Service) operationResult(ctx context.Context, command string, req OperationRequest) (OperationResult, error) {
	if err := ctx.Err(); err != nil {
		return OperationResult{}, err
	}
	sources, err := s.store.ListSources(ctx, cache.SourceFilter{})
	if err != nil && !isCacheNotFound(err) {
		return OperationResult{}, normalizeError(err, "sources", "")
	}
	mode := strings.TrimSpace(req.Mode)
	if mode == "" {
		mode = "default"
	}
	return OperationResult{Command: command, Status: "ok", ProcessedCount: len(sources), Evidence: mode, GeneratedAt: s.now().UTC()}, nil
}

type stagedRemote struct {
	source       cache.Source
	identity     cache.Identity
	syncStatus   cache.SyncStatus
	remoteType   string
	remoteID     string
	revision     string
	contentBytes int64
}

func (s *Service) syncIdempotencyKey(req SyncRequest) string {
	payload := strings.Join([]string{"sync", req.Source, req.TrackerID, req.StableID, req.RemoteAlias, req.AliasType, req.AliasID, s.now().UTC().Format(time.RFC3339Nano)}, "|")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])[:32]
}

func syncEventID(key string) string {
	sum := sha256.Sum256([]byte("sync-event|" + key))
	return hex.EncodeToString(sum[:])[:32]
}

func (s *Service) syncEventSourceID(ctx context.Context, req SyncRequest, remoteType, remoteID string) string {
	if req.StableID != "" {
		return req.StableID
	}
	identity, err := s.store.ResolveAlias(ctx, cache.RemoteAlias{Type: remoteType, ID: remoteID})
	if err == nil && identity.SourceID != "" {
		return identity.SourceID
	}
	return fallbackSourceID(remoteType, remoteID)
}

func (s *Service) syncTarget(ctx context.Context, req SyncRequest) (string, string, error) {
	if req.AliasType != "" || req.AliasID != "" {
		if req.AliasType == "" || req.AliasID == "" {
			return "", "", ErrInvalidQuery{Field: "alias", Message: "alias type and id are required together"}
		}
		return req.AliasType, req.AliasID, nil
	}
	if req.RemoteAlias != "" {
		parts := strings.SplitN(req.RemoteAlias, ":", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return "", "", ErrInvalidQuery{Field: "remote_alias", Message: "remote alias must be type:id"}
		}
		return parts[0], parts[1], nil
	}
	if req.StableID != "" {
		source, err := s.store.GetSource(ctx, req.StableID)
		if err != nil {
			return "", "", normalizeError(err, "source", req.StableID)
		}
		for _, identity := range source.Aliases {
			if identity.Remote.Type != "" && identity.Remote.ID != "" {
				return identity.Remote.Type, identity.Remote.ID, nil
			}
		}
		status, err := s.store.GetSyncStatus(ctx, req.StableID)
		if err == nil && status.RemoteType != "" && status.RemoteID != "" {
			return status.RemoteType, status.RemoteID, nil
		}
		return "", "", ErrSyncNoRemoteAlias{Target: req.StableID}
	}
	return "", "", ErrInvalidQuery{Field: "sync target", Message: "stable id or remote alias is required"}
}

func (s *Service) fetchAndStage(ctx context.Context, req SyncRequest, remoteType, remoteID string) (cache.SourceGraph, SyncCounts, error) {
	if s.client == nil {
		return cache.SourceGraph{}, SyncCounts{}, ErrInvalidQuery{Field: "client", Message: "sync requires a GitCode client"}
	}
	attempts := req.MaxAttempts
	if attempts <= 0 {
		attempts = 1
	}
	var graph cache.SourceGraph
	var counts SyncCounts
	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		graph, counts, err = s.fetchOnce(ctx, req, remoteType, remoteID)
		if err == nil {
			return graph, counts, nil
		}
		if attempt == attempts-1 || !isRetryableSyncError(err) {
			return cache.SourceGraph{}, SyncCounts{}, err
		}
		if wait := retryDelay(err); wait > 0 {
			if deadline, ok := ctx.Deadline(); ok && time.Now().Add(wait).After(deadline) {
				return cache.SourceGraph{}, SyncCounts{}, err
			}
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return cache.SourceGraph{}, SyncCounts{}, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return cache.SourceGraph{}, SyncCounts{}, err
}

func (s *Service) fetchOnce(ctx context.Context, req SyncRequest, remoteType, remoteID string) (cache.SourceGraph, SyncCounts, error) {
	switch remoteType {
	case "issue", "issues":
		number, err := strconv.Atoi(remoteID)
		if err != nil {
			return cache.SourceGraph{}, SyncCounts{}, ErrInvalidQuery{Field: "remote_id", Message: "issue remote id must be numeric"}
		}
		issue, err := s.client.GetIssue(ctx, gitcode.IssueRequest{Number: number, KnownRemoteAlias: true, RemoteAlias: remoteID})
		if err != nil {
			return cache.SourceGraph{}, SyncCounts{}, err
		}
		return s.stageIssue(ctx, req, remoteType, remoteID, issue)
	case "wiki", "page", "remote":
		page, err := s.client.GetWikiPage(ctx, gitcode.WikiPageRequest{Slug: remoteID})
		if err != nil {
			return cache.SourceGraph{}, SyncCounts{}, err
		}
		return s.stageWiki(ctx, req, remoteType, remoteID, page)
	default:
		return cache.SourceGraph{}, SyncCounts{}, ErrInvalidQuery{Field: "remote_type", Message: "unsupported remote type " + remoteType}
	}
}

func (s *Service) stageIssue(ctx context.Context, req SyncRequest, remoteType, remoteID string, issue gitcode.Issue) (cache.SourceGraph, SyncCounts, error) {
	body := issue.Body
	if req.MaxSize > 0 && int64(len(body)+len(issue.Title)) > req.MaxSize {
		return cache.SourceGraph{}, SyncCounts{}, gitcode.ErrPayloadTooLarge{Endpoint: remoteID, Limit: req.MaxSize, Size: int64(len(body) + len(issue.Title))}
	}
	stableID := req.StableID
	if stableID == "" {
		stableID = resolveOrFallback(ctx, s.store, remoteType, remoteID, fallbackSourceID(remoteType, remoteID))
	}
	if err := s.guardRemoteAlias(ctx, remoteType, remoteID, stableID); err != nil {
		return cache.SourceGraph{}, SyncCounts{}, err
	}
	now := s.now().UTC()
	updated := issue.UpdatedAt.UTC()
	if updated.IsZero() {
		updated = now
	}
	created := issue.CreatedAt.UTC()
	if created.IsZero() {
		created = updated
	}
	hash := contentHash(issue.Title, body, issue.State, issue.Labels)
	existing, err := s.store.GetSource(ctx, stableID)
	counts := SyncCounts{Fetched: 1}
	if err == nil && existing.ContentHash == hash {
		counts.Skipped = 1
	} else if err == nil {
		counts.Updated = 1
	} else if isCacheNotFound(err) {
		counts.Inserted = 1
	} else {
		return cache.SourceGraph{}, SyncCounts{}, err
	}
	status := issue.State
	if status == "" {
		status = issue.Status
	}
	if status == "" {
		status = "open"
	}
	graph := cache.SourceGraph{Source: cache.Source{ID: stableID, Kind: "issue", Path: "issues/" + remoteID + ".md", Title: issue.Title, Body: body, Status: status, Labels: issue.Labels, ContentHash: hash, CreatedAt: created, UpdatedAt: updated}, Identities: []cache.Identity{{SourceID: stableID, AliasType: remoteType, Alias: remoteID, Remote: cache.RemoteAlias{Type: remoteType, ID: remoteID}}}, SyncStatus: &cache.SyncStatus{SourceID: stableID, RemoteType: remoteType, RemoteID: remoteID, RemoteRevision: hash, Status: "fresh", LastFetchedAt: now}}
	return graph, counts, nil
}

func (s *Service) stageWiki(ctx context.Context, req SyncRequest, remoteType, remoteID string, page gitcode.WikiPage) (cache.SourceGraph, SyncCounts, error) {
	body := page.Body
	if req.MaxSize > 0 && int64(len(body)+len(page.Title)) > req.MaxSize {
		return cache.SourceGraph{}, SyncCounts{}, gitcode.ErrPayloadTooLarge{Endpoint: remoteID, Limit: req.MaxSize, Size: int64(len(body) + len(page.Title))}
	}
	stableID := req.StableID
	if stableID == "" {
		stableID = resolveOrFallback(ctx, s.store, remoteType, remoteID, fallbackSourceID(remoteType, remoteID))
	}
	if err := s.guardRemoteAlias(ctx, remoteType, remoteID, stableID); err != nil {
		return cache.SourceGraph{}, SyncCounts{}, err
	}
	now := s.now().UTC()
	updated := page.UpdatedAt.UTC()
	if updated.IsZero() {
		updated = now
	}
	created := page.CreatedAt.UTC()
	if created.IsZero() {
		created = updated
	}
	revision := page.Revision
	if revision == "" {
		revision = contentHash(page.Title, body)
	}
	hash := contentHash(page.Title, body, revision)
	existing, err := s.store.GetSource(ctx, stableID)
	counts := SyncCounts{Fetched: 1}
	if err == nil && existing.ContentHash == hash {
		counts.Skipped = 1
	} else if err == nil {
		counts.Updated = 1
	} else if isCacheNotFound(err) {
		counts.Inserted = 1
	} else {
		return cache.SourceGraph{}, SyncCounts{}, err
	}
	graph := cache.SourceGraph{Source: cache.Source{ID: stableID, Kind: "wiki", Path: "wiki/" + remoteID + ".md", Title: page.Title, Body: body, Status: "fresh", ContentHash: hash, CreatedAt: created, UpdatedAt: updated}, Identities: []cache.Identity{{SourceID: stableID, AliasType: remoteType, Alias: remoteID, Remote: cache.RemoteAlias{Type: remoteType, ID: remoteID}}}, SyncStatus: &cache.SyncStatus{SourceID: stableID, RemoteType: remoteType, RemoteID: remoteID, RemoteRevision: revision, Status: "fresh", LastFetchedAt: now}}
	return graph, counts, nil
}

func (s *Service) guardRemoteAlias(ctx context.Context, remoteType, remoteID, stableID string) error {
	identity, err := s.store.ResolveAlias(ctx, cache.RemoteAlias{Type: remoteType, ID: remoteID})
	if err == nil && identity.SourceID != "" && identity.SourceID != stableID {
		return gitcode.ErrRemoteCollision{Alias: remoteType + ":" + remoteID, ExistingID: identity.SourceID, NewID: stableID}
	}
	if err != nil && !isCacheNotFound(err) {
		return err
	}
	return nil
}

func resolveOrFallback(ctx context.Context, store cache.Store, remoteType, remoteID, fallback string) string {
	identity, err := store.ResolveAlias(ctx, cache.RemoteAlias{Type: remoteType, ID: remoteID})
	if err == nil && identity.SourceID != "" {
		return identity.SourceID
	}
	return fallback
}

func fallbackSourceID(remoteType, remoteID string) string {
	clean := strings.NewReplacer("/", "-", " ", "-", ":", "-").Replace(strings.ToUpper(remoteID))
	switch remoteType {
	case "issue", "issues":
		return "ISSUE-" + clean
	case "wiki", "page", "remote":
		return "WIKI-" + clean
	default:
		return "REMOTE-" + clean
	}
}

func contentHash(parts ...any) string {
	b, _ := json.Marshal(parts)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func syncEventMessage(counts SyncCounts) string {
	b, _ := json.Marshal(counts)
	return string(b)
}

func syncResultFromEvent(event cache.SyncEvent, generated time.Time) SyncResult {
	var counts SyncCounts
	_ = json.Unmarshal([]byte(event.Message), &counts)
	freshness := string(FreshnessFresh)
	if event.Status != "succeeded" {
		freshness = string(FreshnessUnknown)
	}
	return SyncResult{IdempotencyKey: event.IdempotencyKey, Status: event.Status, Counts: counts, SyncEventID: event.ID, Freshness: freshness, GeneratedAt: generated}
}

func failedSyncEvent(event cache.SyncEvent, cause error, at time.Time) cache.SyncEvent {
	event.Status = "failed"
	event.Message = cause.Error()
	event.CreatedAt = at
	return event
}

func (s *Service) normalizeSyncFailure(err error, req SyncRequest, remoteType, remoteID string) error {
	target := syncFailureTarget(req, remoteType, remoteID)
	var network gitcode.ErrNetworkUnavailable
	if errors.As(err, &network) {
		return ErrSyncFailure{Mode: "network_timeout", Target: target, Endpoint: network.Endpoint, RecoveryAction: "retry with --timeout to increase deadline or check connectivity", Cause: err}
	}
	var rate gitcode.ErrRateLimited
	if errors.As(err, &rate) {
		return ErrSyncFailure{Mode: "rate_limited", Target: target, Endpoint: rate.Endpoint, RetryAfter: rate.RetryAfter, RecoveryAction: fmt.Sprintf("wait %s before retrying sync", rate.RetryAfter), Cause: err}
	}
	var partial gitcode.ErrPartialResponse
	if errors.As(err, &partial) {
		return ErrSyncFailure{Mode: "partial_response", Target: target, Endpoint: partial.Endpoint, ExpectedBytes: partial.Expected, GotBytes: partial.Got, RecoveryAction: "run sync again to resume", Cause: err}
	}
	var auth gitcode.ErrAuthExpired
	if errors.As(err, &auth) {
		return ErrSyncFailure{Mode: "auth_expired", Target: target, Endpoint: auth.Endpoint, RecoveryAction: "renew GITCODE_TOKEN and retry sync", Cause: err}
	}
	var collision gitcode.ErrRemoteCollision
	if errors.As(err, &collision) {
		return ErrSyncFailure{Mode: "remote_collision", Target: target, Endpoint: collision.Endpoint, Alias: collision.Alias, ExistingID: collision.ExistingID, NewID: collision.NewID, RecoveryAction: "run link-check for guidance", Cause: err}
	}
	var corruption cache.ErrCacheCorruption
	if errors.As(err, &corruption) {
		return ErrSyncFailure{Mode: "cache_corruption", Target: target, Endpoint: corruption.Path, RecoveryAction: "recover from backup or re-ingest with gitcode-mcp sync --full", Cause: err}
	}
	var missing gitcode.ErrRemoteNotFound
	if errors.As(err, &missing) {
		alias := missing.Alias
		if alias == "" {
			alias = remoteType + ":" + remoteID
		}
		return ErrSyncFailure{Mode: "remote_not_found", Target: target, Endpoint: missing.Endpoint, Alias: alias, RecoveryAction: "run link-check to find affected references", Cause: err}
	}
	var tooLarge gitcode.ErrPayloadTooLarge
	if errors.As(err, &tooLarge) {
		return ErrSyncFailure{Mode: "payload_too_large", Target: target, Endpoint: tooLarge.Endpoint, LimitBytes: tooLarge.Limit, SizeBytes: tooLarge.Size, RecoveryAction: "increase --max-size or skip with --skip-large", Cause: err}
	}
	var conflict gitcode.ErrConflict
	if errors.As(err, &conflict) {
		return ErrSyncFailure{Mode: "conflict", Target: target, Endpoint: conflict.Endpoint, LocalPayload: append([]byte(nil), conflict.LocalPayload...), RemotePayload: append([]byte(nil), conflict.RemotePayload...), RecoveryAction: "resolve local and remote payloads manually", Cause: err}
	}
	return err
}

func syncFailureTarget(req SyncRequest, remoteType, remoteID string) string {
	if req.StableID != "" {
		return req.StableID
	}
	if req.RemoteAlias != "" {
		return req.RemoteAlias
	}
	if remoteType != "" || remoteID != "" {
		return remoteType + ":" + remoteID
	}
	return req.Source
}

func (s *Service) markMissingRemote(ctx context.Context, event cache.SyncEvent, failure error, remoteType, remoteID string) error {
	var syncFailure ErrSyncFailure
	if !errors.As(failure, &syncFailure) || syncFailure.Mode != "remote_not_found" || event.SourceID == "" {
		return nil
	}
	source, err := s.store.GetSource(ctx, event.SourceID)
	if err != nil {
		return failure
	}
	graph := cache.SourceGraph{Source: source, SyncStatus: &cache.SyncStatus{SourceID: event.SourceID, RemoteType: remoteType, RemoteID: remoteID, RemoteRevision: event.RemoteRevision, Status: "not_found", LastFetchedAt: s.now().UTC()}}
	if err := s.store.UpsertSourceGraph(ctx, graph); err != nil {
		return err
	}
	return nil
}

func isRetryableSyncError(err error) bool {
	var network gitcode.ErrNetworkUnavailable
	if errors.As(err, &network) {
		return true
	}
	var rate gitcode.ErrRateLimited
	return errors.As(err, &rate)
}

func retryDelay(err error) time.Duration {
	var rate gitcode.ErrRateLimited
	if errors.As(err, &rate) {
		return rate.RetryAfter
	}
	return 0
}

func freshnessFor(source cache.Source, status cache.SyncStatus) string {
	if status.Status == "missing_remote" || status.Status == "not_found" {
		return string(FreshnessMissingRemote)
	}
	if status.LastFetchedAt.IsZero() || source.UpdatedAt.IsZero() {
		return string(FreshnessUnknown)
	}
	if source.UpdatedAt.After(status.LastFetchedAt) {
		return string(FreshnessStale)
	}
	return string(FreshnessFresh)
}

func (s *Service) writeResult(ctx context.Context, command string, req WriteCommandRequest) (WriteCommandResult, error) {
	if err := ctx.Err(); err != nil {
		return WriteCommandResult{}, err
	}
	key := req.IdempotencyKey
	if key == "" {
		sum := sha256.Sum256([]byte(command + "|" + req.Owner + "|" + req.Repo + "|" + req.ID + "|" + req.Title + "|" + req.Body + "|" + req.Label))
		key = hex.EncodeToString(sum[:])[:32]
	}
	id := req.ID
	if id == "" && req.Number != 0 {
		id = fmt.Sprintf("%d", req.Number)
	}
	return WriteCommandResult{Command: command, Status: "queued", ID: id, IdempotencyKey: key, Evidence: "explicit CLI write command", GeneratedAt: s.now().UTC()}, nil
}

func (s *Service) resolveStableID(ctx context.Context, id, aliasType, aliasID string) (string, error) {
	if id != "" {
		if _, err := s.store.GetSource(ctx, id); err == nil {
			return id, nil
		} else if !isCacheNotFound(err) {
			return "", normalizeError(err, "source", id)
		}
	}
	if aliasType != "" || aliasID != "" {
		if aliasType == "" || aliasID == "" {
			return "", ErrInvalidQuery{Field: "alias", Message: "alias type and id are required together"}
		}
		identity, err := s.store.ResolveAlias(ctx, cache.RemoteAlias{Type: aliasType, ID: aliasID})
		if err != nil {
			return "", normalizeError(err, "alias", aliasType+":"+aliasID)
		}
		return identity.SourceID, nil
	}
	if id == "" {
		return "", ErrInvalidQuery{Field: "id", Message: "id is required"}
	}
	return "", ErrNotFound{Kind: "source", ID: id}
}

func (s *Service) snippetFromLines(ctx context.Context, id string, start, end int) (SnippetResult, error) {
	if start <= 0 || end <= 0 || end < start {
		return SnippetResult{}, ErrInvalidQuery{Field: "line range", Message: "line_start and line_end must be positive and ordered"}
	}
	source, err := s.store.GetSource(ctx, id)
	if err != nil {
		return SnippetResult{}, normalizeError(err, "source", id)
	}
	lines := strings.Split(source.Body, "\n")
	if start > len(lines) {
		return SnippetResult{}, ErrInvalidQuery{Field: "line_start", Message: "line_start is beyond source body"}
	}
	warnings := []string{}
	actualEnd := end
	if actualEnd > len(lines) {
		actualEnd = len(lines)
		warnings = append(warnings, ErrRangeClamped{RequestedStart: start, RequestedEnd: end, ActualStart: start, ActualEnd: actualEnd}.Error())
	}
	return SnippetResult{ID: source.ID, Path: source.Path, Text: strings.Join(lines[start-1:actualEnd], "\n"), LineStart: start, LineEnd: actualEnd, Warnings: warnings}, nil
}

func (s *Service) snippetFromChunk(ctx context.Context, id, chunkID string) (SnippetResult, error) {
	chunks, err := s.store.GetChunks(ctx, id)
	if err != nil {
		return SnippetResult{}, normalizeError(err, "chunks", id)
	}
	for _, chunk := range chunks {
		if chunk.ID == chunkID {
			source, err := s.store.GetSource(ctx, id)
			if err != nil {
				return SnippetResult{}, normalizeError(err, "source", id)
			}
			return SnippetResult{ID: id, Path: source.Path, Text: chunk.Text, LineStart: chunk.LineStart, LineEnd: chunk.LineEnd, ChunkID: chunk.ID}, nil
		}
	}
	return SnippetResult{}, ErrNotFound{Kind: "chunk", ID: chunkID}
}

func (s *Service) renderSnapshot(ctx context.Context, format string) (string, int, error) {
	sources, err := s.store.ListSources(ctx, cache.SourceFilter{})
	if err != nil {
		return "", 0, normalizeError(err, "sources", "")
	}
	if len(sources) == 0 {
		return "", 0, ErrCacheEmpty{Message: "cache has no sources"}
	}
	sort.SliceStable(sources, func(i, j int) bool {
		if sources[i].ID != sources[j].ID {
			return sources[i].ID < sources[j].ID
		}
		return sources[i].Path < sources[j].Path
	})
	if normalizeFormat(format) == "json" {
		records := make([]SourceRecord, 0, len(sources))
		for _, source := range sources {
			links, err := s.store.ListLinks(ctx, cache.LinkFilter{SourceID: source.ID})
			if err != nil {
				return "", 0, normalizeError(err, "links", source.ID)
			}
			records = append(records, sourceRecord(source, links, nil))
		}
		b, err := json.Marshal(records)
		if err != nil {
			return "", 0, err
		}
		return string(b), len(sources), nil
	}
	var b strings.Builder
	for _, source := range sources {
		labels := append([]string(nil), source.Labels...)
		sort.Strings(labels)
		b.WriteString(source.ID + "\t" + source.Path + "\t" + source.Kind + "\t" + source.Status + "\t" + source.Title + "\t" + strings.Join(labels, ",") + "\t" + source.UpdatedAt.UTC().Format(time.RFC3339) + "\n")
	}
	return b.String(), len(sources), nil
}
