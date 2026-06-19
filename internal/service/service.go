package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"gitcode-mcp/internal/cache"
)

type Service struct {
	store cache.Store
	now   func() time.Time
}

func New(store cache.Store) *Service {
	return &Service{store: store, now: func() time.Time { return time.Now().UTC() }}
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
	status, err := s.store.GetSyncStatus(ctx, id)
	if err != nil {
		return SyncStatusResult{}, normalizeError(err, "sync status", id)
	}
	return SyncStatusResult{SourceID: status.SourceID, RemoteType: status.RemoteType, RemoteID: status.RemoteID, RemoteRevision: status.RemoteRevision, Status: status.Status, LastFetchedAt: status.LastFetchedAt.UTC()}, nil
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

func (s *Service) SyncToCache(ctx context.Context, req OperationRequest) (OperationResult, error) {
	return s.operationResult(ctx, "sync", req)
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
