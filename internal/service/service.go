package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/index"
)

type Service struct {
	store    cache.Store
	client   gitcode.Client
	now      func() time.Time
	lockPath string
}

func New(store cache.Store) *Service {
	return &Service{store: store, client: sanitizedFixtureClient{}, now: func() time.Time { return time.Now().UTC() }, lockPath: filepath.Join(os.TempDir(), "gitcode-mcp-sync.lock")}
}

func NewWithClient(store cache.Store, client gitcode.Client) *Service {
	svc := New(store)
	svc.client = client
	return svc
}

type sanitizedFixtureClient struct{}

func (sanitizedFixtureClient) ListIssues(context.Context, gitcode.IssueListRequest) (gitcode.Page[gitcode.IssueSummary], error) {
	now := fixtureNow()
	return gitcode.Page[gitcode.IssueSummary]{Items: []gitcode.IssueSummary{{Number: 42, Title: "Fixture Issue", State: "open", CreatedAt: now, UpdatedAt: now}}, Page: 1, PerPage: 1, TotalCount: 1}, nil
}

func (sanitizedFixtureClient) GetIssue(context.Context, gitcode.IssueRequest) (gitcode.Issue, error) {
	now := fixtureNow()
	return gitcode.Issue{Number: 42, Title: "Fixture Issue", Body: "# Issue\n\nremote issue body", State: "open", CreatedAt: now, UpdatedAt: now}, nil
}

func (sanitizedFixtureClient) ListIssueComments(context.Context, gitcode.IssueRequest) (gitcode.Page[gitcode.Comment], error) {
	now := fixtureNow()
	return gitcode.Page[gitcode.Comment]{Items: []gitcode.Comment{{ID: "c1", Author: "fixture-user", Body: "comment", CreatedAt: now, UpdatedAt: now}}, Page: 1, PerPage: 1, TotalCount: 1}, nil
}

func (sanitizedFixtureClient) GetWikiPage(context.Context, gitcode.WikiPageRequest) (gitcode.WikiPage, error) {
	now := fixtureNow()
	return gitcode.WikiPage{Slug: "Home", Title: "Fixture Wiki", Body: "# Wiki\n\nremote wiki body", Revision: "rev-home", CreatedAt: now, UpdatedAt: now}, nil
}

func (sanitizedFixtureClient) ListWikiPages(context.Context, gitcode.WikiListRequest) (gitcode.Page[gitcode.WikiPage], error) {
	now := fixtureNow()
	return gitcode.Page[gitcode.WikiPage]{Items: []gitcode.WikiPage{{Slug: "Home", Title: "Fixture Wiki", Body: "# Wiki\n\nremote wiki body", Revision: "rev-home", CreatedAt: now, UpdatedAt: now}}, Page: 1, PerPage: 1, TotalCount: 1}, nil
}

func (sanitizedFixtureClient) Search(context.Context, gitcode.SearchRequest) (gitcode.Page[gitcode.SearchResult], error) {
	return gitcode.Page[gitcode.SearchResult]{}, nil
}

func (sanitizedFixtureClient) ListIssueAttachments(context.Context, gitcode.IssueRequest) (gitcode.Page[gitcode.AttachmentSummary], error) {
	return gitcode.Page[gitcode.AttachmentSummary]{}, nil
}

func (sanitizedFixtureClient) GetAttachment(context.Context, gitcode.AttachmentRequest) (gitcode.AttachmentBody, error) {
	return gitcode.AttachmentBody{}, ErrInvalidQuery{Field: "attachment", Message: "fixture attachment is unavailable"}
}

func (sanitizedFixtureClient) CreateIssue(context.Context, gitcode.CreateIssueRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	return gitcode.WriteResult[gitcode.Issue]{}, ErrInvalidQuery{Field: "write", Message: "fixture client is read-only"}
}

func (sanitizedFixtureClient) UpdateIssue(context.Context, gitcode.UpdateIssueRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	return gitcode.WriteResult[gitcode.Issue]{}, ErrInvalidQuery{Field: "write", Message: "fixture client is read-only"}
}

func (sanitizedFixtureClient) CreateIssueComment(context.Context, gitcode.CreateIssueCommentRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Comment], error) {
	return gitcode.WriteResult[gitcode.Comment]{}, ErrInvalidQuery{Field: "write", Message: "fixture client is read-only"}
}

func (sanitizedFixtureClient) CreateWikiPage(context.Context, gitcode.CreateWikiPageRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.WikiPage], error) {
	return gitcode.WriteResult[gitcode.WikiPage]{}, ErrInvalidQuery{Field: "write", Message: "fixture client is read-only"}
}

func (sanitizedFixtureClient) UpdateWikiPage(context.Context, gitcode.UpdateWikiPageRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.WikiPage], error) {
	return gitcode.WriteResult[gitcode.WikiPage]{}, ErrInvalidQuery{Field: "write", Message: "fixture client is read-only"}
}

func (sanitizedFixtureClient) AddLabel(context.Context, gitcode.LabelRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	return gitcode.WriteResult[gitcode.Issue]{}, ErrInvalidQuery{Field: "write", Message: "fixture client is read-only"}
}

func (sanitizedFixtureClient) RemoveLabel(context.Context, gitcode.LabelRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	return gitcode.WriteResult[gitcode.Issue]{}, ErrInvalidQuery{Field: "write", Message: "fixture client is read-only"}
}

func fixtureNow() time.Time {
	return time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)
}

func (s *Service) AddRepository(ctx context.Context, req AddRepositoryRequest) (RepositoryBinding, error) {
	repo, err := normalizeRepositoryRequest(req, s.now())
	if err != nil {
		return RepositoryBinding{}, err
	}
	cacheRepo := cache.RepositoryBinding{RepoID: repo.RepoID, Owner: repo.Owner, Name: repo.Name, APIBaseURL: repo.APIBaseURL, DisplayName: repo.DisplayName, CreatedAt: repo.CreatedAt, UpdatedAt: repo.UpdatedAt, Aliases: repo.Aliases}
	for _, scope := range repo.Scopes {
		cacheRepo.Scopes = append(cacheRepo.Scopes, cache.RepositoryScope(scope))
	}
	if err := s.store.AddRepository(ctx, cacheRepo); err != nil {
		if cache.IsConstraintError(err) {
			return RepositoryBinding{}, ErrConflict{Kind: "repository", ID: repo.RepoID, Message: "repo_id or repository alias already exists"}
		}
		return RepositoryBinding{}, err
	}
	return repo, nil
}

func (s *Service) CacheStatus(ctx context.Context, req CacheStatusRequest) (CacheStatusResult, error) {
	repoID, err := s.requireRepo(ctx, req.RepoID, "cache-status")
	if err != nil {
		return CacheStatusResult{}, err
	}
	counts, err := s.store.RecordCounts(ctx, repoID)
	if err != nil {
		return CacheStatusResult{}, normalizeError(err, "cache", repoID)
	}
	walCapable, journalMode, err := s.store.WALCapable(ctx)
	if err != nil {
		return CacheStatusResult{}, normalizeError(err, "cache", repoID)
	}
	freshness, err := s.freshnessReport(ctx, repoID, index.ChunkQuery{RepoID: repoID})
	if err != nil {
		return CacheStatusResult{}, err
	}
	byWarning := map[string]int{}
	for _, warning := range freshness.Warnings {
		byWarning[warning.Code]++
	}
	return CacheStatusResult{RepoID: repoID, WALCapable: walCapable, JournalMode: journalMode, Records: counts.Records, Comments: counts.Comments, IdentityAliases: counts.IdentityAliases, SyncEvents: counts.SyncEvents, AuditRows: counts.AuditRows, Snapshots: counts.Snapshots, SnapshotChunks: counts.SnapshotChunks, Chunks: counts.Chunks, RemoteRevisions: counts.RemoteRevisions, IndexFreshnessWarnings: len(freshness.Warnings), IndexFreshnessByWarning: byWarning}, nil
}

func (s *Service) RepositoryStatus(ctx context.Context, req RepositoryStatusRequest) (RepositoryStatus, error) {
	repoID := strings.TrimSpace(req.RepoID)
	if repoID == "" {
		return RepositoryStatus{}, ErrInvalidQuery{Field: "repo", Message: "repo is required"}
	}
	repo, err := s.store.GetRepository(ctx, repoID)
	if err != nil {
		return RepositoryStatus{}, normalizeError(err, "repository", repoID)
	}
	status := RepositoryStatus{RepoID: repo.RepoID, Owner: repo.Owner, Name: repo.Name, APIBaseURL: sanitizeAPIBaseURL(repo.APIBaseURL), DisplayName: repo.DisplayName, Aliases: append([]string(nil), repo.Aliases...), BindingState: "ready", AliasConflictState: "none", CacheState: "unknown", IndexState: "unknown"}
	for _, scope := range repo.Scopes {
		status.Scopes = append(status.Scopes, RepositoryScope(scope))
	}
	return status, nil
}

func normalizeRepositoryRequest(req AddRepositoryRequest, now time.Time) (RepositoryBinding, error) {
	repo := RepositoryBinding{RepoID: strings.TrimSpace(req.RepoID), Owner: strings.TrimSpace(req.Owner), Name: strings.TrimSpace(req.Name), APIBaseURL: strings.TrimSpace(req.APIBaseURL), DisplayName: strings.TrimSpace(req.DisplayName), CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	if repo.RepoID == "" {
		return RepositoryBinding{}, ErrInvalidQuery{Field: "repo", Message: "repo is required"}
	}
	if repo.Owner == "" {
		return RepositoryBinding{}, ErrInvalidQuery{Field: "owner", Message: "owner is required"}
	}
	if repo.Name == "" {
		return RepositoryBinding{}, ErrInvalidQuery{Field: "name", Message: "name is required"}
	}
	parsed, err := url.Parse(repo.APIBaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return RepositoryBinding{}, ErrInvalidQuery{Field: "api-base-url", Message: "valid api base url is required"}
	}
	parsed.User = nil
	repo.APIBaseURL = sanitizeAPIBaseURL(parsed.String())
	scopes, err := normalizeRepositoryScopes(req.Scopes)
	if err != nil {
		return RepositoryBinding{}, err
	}
	repo.Scopes = scopes
	repo.Aliases = normalizeAliases(req.Aliases)
	return repo, nil
}

func normalizeRepositoryScopes(raw []string) ([]RepositoryScope, error) {
	seen := map[RepositoryScope]bool{}
	for _, value := range raw {
		for _, part := range strings.Split(value, ",") {
			scope := RepositoryScope(strings.ToLower(strings.TrimSpace(part)))
			if scope == "" {
				continue
			}
			if scope != RepositoryScopeIssues && scope != RepositoryScopeWiki {
				return nil, ErrInvalidQuery{Field: "scopes", Message: "scopes must contain issues or wiki"}
			}
			seen[scope] = true
		}
	}
	if len(seen) == 0 {
		return nil, ErrInvalidQuery{Field: "scopes", Message: "at least one scope is required"}
	}
	out := []RepositoryScope{}
	for _, scope := range []RepositoryScope{RepositoryScopeIssues, RepositoryScopeWiki} {
		if seen[scope] {
			out = append(out, scope)
		}
	}
	return out, nil
}

func normalizeAliases(raw []string) []string {
	seen := map[string]bool{}
	aliases := []string{}
	for _, value := range raw {
		for _, part := range strings.Split(value, ",") {
			alias := strings.TrimSpace(part)
			if alias == "" || seen[alias] {
				continue
			}
			seen[alias] = true
			aliases = append(aliases, alias)
		}
	}
	sort.Strings(aliases)
	return aliases
}

func sanitizeAPIBaseURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parsed.User = nil
	query := parsed.Query()
	for key := range query {
		lower := strings.ToLower(key)
		if strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "key") || strings.Contains(lower, "auth") {
			query.Del(key)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func (s *Service) SearchSources(ctx context.Context, req SearchSourcesRequest) ([]SearchSourceResult, error) {
	repoID, err := s.requireRepo(ctx, req.RepoID, "search")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.Query) == "" {
		return nil, ErrInvalidQuery{Field: "query", Message: "query is required"}
	}
	results, err := s.store.SearchSources(ctx, cache.SearchQuery{RepoID: repoID, Query: req.Query, Kind: req.Kind, Limit: req.Limit})
	if err != nil {
		return nil, normalizeError(err, "search", req.Query)
	}
	if len(results) == 0 {
		return nil, ErrCacheEmpty{Message: "no cached search results"}
	}
	out := make([]SearchSourceResult, 0, len(results))
	updated := map[string]time.Time{}
	for _, result := range results {
		source, err := s.store.GetSourceScoped(ctx, repoID, result.ID)
		if err != nil {
			return nil, normalizeError(err, "source", result.ID)
		}
		updated[result.ID] = source.UpdatedAt.UTC()
		line := nullableLine(result.Line)
		out = append(out, SearchSourceResult{RepoID: source.RepoID, ID: result.ID, Path: result.Path, Title: result.Title, Kind: source.Kind, Status: source.Status, Snippet: result.Snippet, LineStart: line, LineEnd: line, Score: result.Score})
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
	repoID, err := s.requireRepo(ctx, req.RepoID, "get")
	if err != nil {
		return SourceRecord{}, err
	}
	id, err := s.resolveScopedStableID(ctx, repoID, req.ID, req.AliasType, req.AliasID)
	if err != nil {
		return SourceRecord{}, err
	}
	source, err := s.store.GetSourceScoped(ctx, repoID, id)
	if err != nil {
		return SourceRecord{}, normalizeError(err, "source", id)
	}
	links, err := s.store.ListLinks(ctx, cache.LinkFilter{RepoID: repoID, SourceID: source.ID})
	if err != nil {
		return SourceRecord{}, normalizeError(err, "links", source.ID)
	}
	backlinks, err := s.GetBacklinks(ctx, GetBacklinksRequest{RepoID: repoID, ID: source.ID})
	if err != nil && !IsCacheEmpty(err) {
		return SourceRecord{}, err
	}
	return sourceRecord(source, links, backlinks), nil
}

func (s *Service) ListSources(ctx context.Context, req ListSourcesRequest) ([]SourceSummary, error) {
	repoID, err := s.requireRepo(ctx, req.RepoID, "list")
	if err != nil {
		return nil, err
	}
	sources, err := s.store.ListSources(ctx, cache.SourceFilter{RepoID: repoID, Kind: req.Kind, Status: req.Status, Limit: req.limitPlusOffset()})
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
	repoID, err := s.requireRepo(ctx, req.RepoID, "backlinks")
	if err != nil {
		return nil, err
	}
	id, err := s.resolveScopedStableID(ctx, repoID, req.ID, req.AliasType, req.AliasID)
	if err != nil {
		return nil, err
	}
	backlinks, err := s.store.GetBacklinksScoped(ctx, repoID, id)
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
	repoID, err := s.requireRepo(ctx, req.RepoID, "resolve")
	if err != nil {
		return ResolvedID{}, err
	}
	id, err := s.resolveScopedStableID(ctx, repoID, req.ID, req.AliasType, req.AliasID)
	if err != nil {
		return ResolvedID{}, err
	}
	source, err := s.store.GetSourceScoped(ctx, repoID, id)
	if err != nil {
		return ResolvedID{}, normalizeError(err, "source", id)
	}
	return ResolvedID{RepoID: source.RepoID, ID: source.ID, Path: source.Path, RemoteAlias: remoteAlias(source.Aliases), Kind: source.Kind, Title: source.Title}, nil
}

func (s *Service) GetSnippet(ctx context.Context, req SnippetRequest) (SnippetResult, error) {
	repoID, err := s.requireRepo(ctx, req.RepoID, "snippet")
	if err != nil {
		return SnippetResult{}, err
	}
	id, err := s.resolveScopedStableID(ctx, repoID, req.ID, req.AliasType, req.AliasID)
	if err != nil {
		return SnippetResult{}, err
	}
	if req.LineStart > 0 || req.LineEnd > 0 {
		return s.snippetFromLines(ctx, repoID, id, req.LineStart, req.LineEnd)
	}
	if req.ChunkID != "" {
		return s.snippetFromChunk(ctx, repoID, id, req.ChunkID)
	}
	return SnippetResult{}, ErrInvalidQuery{Field: "range", Message: "line range or chunk id is required"}
}

func (s *Service) ListChunks(ctx context.Context, req ChunkQuery) (ChunkQueryResult, error) {
	repoID, err := s.requireRepo(ctx, req.RepoID, "list-chunks")
	if err != nil {
		return ChunkQueryResult{}, err
	}
	req.RepoID = repoID
	chunks, err := s.store.ListChunks(ctx, cache.ChunkFilter{RepoID: req.RepoID, SourceID: req.SourceID, RecordID: req.RecordID, SnapshotID: req.SnapshotID, Policy: string(req.Policy)})
	if err != nil {
		return ChunkQueryResult{}, normalizeError(err, "chunks", req.SourceID)
	}
	freshness, err := s.freshnessReport(ctx, repoID, req)
	if err != nil {
		return ChunkQueryResult{}, err
	}
	return index.NewMemoryChunkIndex(indexChunks(chunks)).ListChunksWithWarnings(ctx, req, freshness.Warnings)
}

func (s *Service) SearchChunks(ctx context.Context, req ChunkSearchQuery) (ChunkQueryResult, error) {
	repoID, err := s.requireRepo(ctx, req.RepoID, "search-chunks")
	if err != nil {
		return ChunkQueryResult{}, err
	}
	req.RepoID = repoID
	chunks, err := s.store.ListChunks(ctx, cache.ChunkFilter{RepoID: req.RepoID, SourceID: req.SourceID, RecordID: req.RecordID, SnapshotID: req.SnapshotID, Policy: string(req.Policy)})
	if err != nil {
		return ChunkQueryResult{}, normalizeError(err, "chunks", req.SourceID)
	}
	freshness, err := s.freshnessReport(ctx, repoID, req.ChunkQuery)
	if err != nil {
		return ChunkQueryResult{}, err
	}
	return index.NewMemoryChunkIndex(indexChunks(chunks)).SearchChunksWithWarnings(ctx, req, freshness.Warnings)
}

func (s *Service) GetChunkSnippet(ctx context.Context, req SnippetQuery) (ChunkQueryResult, error) {
	repoID, err := s.requireRepo(ctx, req.RepoID, "get-snippet")
	if err != nil {
		return ChunkQueryResult{}, err
	}
	req.RepoID = repoID
	chunks, err := s.store.ListChunks(ctx, cache.ChunkFilter{RepoID: req.RepoID, SourceID: req.SourceID, RecordID: req.RecordID, SnapshotID: req.SnapshotID, Policy: string(req.Policy)})
	if err != nil {
		return ChunkQueryResult{}, normalizeError(err, "chunks", req.SourceID)
	}
	freshnessQuery := index.ChunkQuery{RepoID: req.RepoID, SourceID: req.SourceID, RecordID: req.RecordID, SnapshotID: req.SnapshotID, Policy: req.Policy}
	freshness, err := s.freshnessReport(ctx, repoID, freshnessQuery)
	if err != nil {
		return ChunkQueryResult{}, err
	}
	return index.NewMemoryChunkIndex(indexChunks(chunks)).GetSnippetWithWarnings(ctx, req, freshness.Warnings)
}

func (s *Service) GetSyncStatus(ctx context.Context, req SyncStatusRequest) (SyncStatusResult, error) {
	repoID, err := s.requireRepo(ctx, req.RepoID, "sync-status")
	if err != nil {
		return SyncStatusResult{}, err
	}
	id, err := s.resolveScopedStableID(ctx, repoID, req.ID, req.AliasType, req.AliasID)
	if err != nil {
		return SyncStatusResult{}, err
	}
	source, err := s.store.GetSourceScoped(ctx, repoID, id)
	if err != nil {
		return SyncStatusResult{}, normalizeError(err, "source", id)
	}
	status, err := s.store.GetSyncStatusScoped(ctx, repoID, id)
	if err != nil {
		return SyncStatusResult{}, normalizeError(err, "sync status", id)
	}
	freshness := freshnessFor(source, status)
	return SyncStatusResult{RepoID: status.RepoID, SourceID: status.SourceID, RemoteType: status.RemoteType, RemoteID: status.RemoteID, RemoteRevision: status.RemoteRevision, Status: status.Status, Freshness: freshness, LocalUpdatedAt: source.UpdatedAt.UTC(), LastFetchedAt: status.LastFetchedAt.UTC()}, nil
}

func (s *Service) RecentChanges(ctx context.Context, req RecentChangesRequest) ([]RecentChangeResult, error) {
	repoID, err := s.requireRepo(ctx, req.RepoID, "recent")
	if err != nil {
		return nil, err
	}
	sources, err := s.store.ListSources(ctx, cache.SourceFilter{RepoID: repoID, Kind: req.Kind, Status: req.Status})
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
		out = append(out, RecentChangeResult{RepoID: source.RepoID, ID: source.ID, Path: source.Path, Title: source.Title, Kind: source.Kind, Status: source.Status, UpdatedAt: source.UpdatedAt.UTC()})
	}
	return out, nil
}

func (s *Service) LinkCheck(ctx context.Context, req LinkCheckRequest) (LinkCheckResult, error) {
	repoID, err := s.requireRepo(ctx, req.RepoID, "link-check")
	if err != nil {
		return LinkCheckResult{}, err
	}
	links, err := s.store.ListLinks(ctx, cache.LinkFilter{RepoID: repoID})
	if err != nil {
		return LinkCheckResult{}, normalizeError(err, "links", "")
	}
	result := LinkCheckResult{RepoID: repoID, CheckedCount: len(links), SuggestedAliases: map[string][]string{}}
	for _, link := range links {
		if _, err := s.store.GetSourceScoped(ctx, repoID, link.TargetID); err != nil {
			if isCacheNotFound(err) {
				result.BrokenLinks = append(result.BrokenLinks, BrokenLinkResult{RepoID: link.RepoID, SourceID: link.SourceID, TargetID: link.TargetID, Kind: link.Kind, Text: link.Text})
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
	repoID, err := s.requireRepo(ctx, req.RepoID, "stale-index")
	if err != nil {
		return StaleIndexResult{}, err
	}
	report, err := s.freshnessReport(ctx, repoID, index.ChunkQuery{RepoID: repoID})
	if err != nil {
		return StaleIndexResult{}, err
	}
	affected := map[string]struct{}{}
	missing := map[string]struct{}{}
	var lastIndexed time.Time
	for _, record := range report.Records {
		if !record.IndexedAt.IsZero() && record.IndexedAt.After(lastIndexed) {
			lastIndexed = record.IndexedAt
		}
		if record.WarningCode == "" {
			continue
		}
		affected[record.SourceID] = struct{}{}
		for _, target := range record.MissingTargetIDs {
			missing[target] = struct{}{}
		}
	}
	result := StaleIndexResult{RepoID: repoID, StaleCount: len(report.Warnings), AffectedSourceIDs: sortedKeys(affected), MissingTargetIDs: sortedKeys(missing), LastIndexedAt: lastIndexed.UTC(), Warnings: report.Warnings, Records: report.Records}
	if req.Strict && result.StaleCount > 0 {
		return result, ErrStaleIndex{StaleCount: result.StaleCount}
	}
	return result, nil
}

func (s *Service) ExportSnapshot(ctx context.Context, req ExportSnapshotRequest) (ExportSnapshotResult, error) {
	format := normalizeSnapshotFormat(req.Format)
	snapshot, err := s.buildSnapshot(ctx, req)
	if err != nil {
		return ExportSnapshotResult{}, err
	}
	content, err := renderSnapshotContent(snapshot, format)
	if err != nil {
		return ExportSnapshotResult{}, err
	}
	hash := sha256.Sum256(content)
	result := ExportSnapshotResult{RepoID: snapshot.RepoID, SnapshotID: hex.EncodeToString(hash[:16]), Format: format, RecordCount: len(snapshot.Sources), GeneratedAt: snapshot.CreatedAt, ContentHash: hex.EncodeToString(hash[:]), InlineContent: string(content), Warnings: warningCodes(snapshot.Warnings)}
	if req.OutputPath != "" {
		if err := os.WriteFile(req.OutputPath, content, 0o600); err != nil {
			return ExportSnapshotResult{}, err
		}
		result.OutputPath = req.OutputPath
		if req.InlineLimit <= 0 || len(content) > req.InlineLimit {
			result.InlineContent = ""
		}
	}
	return result, nil
}

func (s *Service) DiffSnapshot(ctx context.Context, req DiffSnapshotRequest) (DiffSnapshotResult, error) {
	format := normalizeSnapshotFormat(req.Format)
	baseRef := req.Base
	if baseRef.Kind == "" {
		base := req.BaseContent
		if base == "" {
			base = req.BaseSnapshotContent
		}
		if base == "" {
			baseRef = SnapshotRef{Kind: "current", Format: format}
		} else {
			baseRef = SnapshotRef{Kind: "bytes", Bytes: []byte(base), Format: format}
		}
	}
	headRef := req.Head
	if headRef.Kind == "" {
		headRef = SnapshotRef{Kind: "current", Format: format}
	}
	base, err := s.loadSnapshotRef(ctx, req.RepoID, baseRef, format)
	if err != nil {
		return DiffSnapshotResult{}, err
	}
	head, err := s.loadSnapshotRef(ctx, req.RepoID, headRef, format)
	if err != nil {
		return DiffSnapshotResult{}, err
	}
	result := diffSnapshots(base, head)
	result.RepoID = req.RepoID
	result.BaseSnapshotID = req.BaseSnapshotID
	result.HeadSnapshotID = req.HeadSnapshotID
	result.Format = format
	baseBytes, _ := renderSnapshotContent(base, format)
	headBytes, _ := renderSnapshotContent(head, format)
	result.DiffText = simpleDiff(string(baseBytes), string(headBytes))
	return result, nil
}

func (s *Service) Ingest(ctx context.Context, req OperationRequest) (OperationResult, error) {
	if err := ctx.Err(); err != nil {
		return OperationResult{}, err
	}
	sources, err := s.store.ListSources(ctx, cache.SourceFilter{})
	if err != nil && !isCacheNotFound(err) {
		return OperationResult{}, normalizeError(err, "sources", "")
	}
	if len(sources) == 0 {
		if err := s.seedMinimumCorpus(ctx); err != nil {
			return OperationResult{}, err
		}
		sources, err = s.store.ListSources(ctx, cache.SourceFilter{})
		if err != nil {
			return OperationResult{}, normalizeError(err, "sources", "")
		}
	}
	return OperationResult{Command: "ingest", Status: "ok", ProcessedCount: len(sources), Evidence: operationMode(req.Mode), GeneratedAt: s.now().UTC()}, nil
}

func (s *Service) Index(ctx context.Context, req OperationRequest) (OperationResult, error) {
	if err := ctx.Err(); err != nil {
		return OperationResult{}, err
	}
	repoID, err := s.requireRepo(ctx, req.RepoID, "index")
	if err != nil {
		return OperationResult{}, err
	}
	sources, err := s.store.ListSources(ctx, cache.SourceFilter{RepoID: repoID})
	if err != nil {
		if isCacheNotFound(err) {
			return OperationResult{Command: "index", Status: "ok", Evidence: operationMode(req.Mode), GeneratedAt: s.now().UTC()}, nil
		}
		return OperationResult{}, normalizeError(err, "sources", "")
	}
	processed := 0
	for _, source := range sources {
		chunks := index.ChunkSource(indexSourceRecord(source), index.ParseSource(indexSourceRecord(source)))
		for _, chunk := range chunks {
			if _, err := s.store.UpsertChunk(ctx, cacheChunk(chunk)); err != nil {
				return OperationResult{}, normalizeError(err, "chunk", chunk.ID)
			}
		}
		processed++
	}
	return OperationResult{Command: "index", Status: "ok", ProcessedCount: processed, Evidence: operationMode(req.Mode), GeneratedAt: s.now().UTC()}, nil
}

func (s *Service) SyncToCache(ctx context.Context, req SyncRequest) (SyncResult, error) {
	if err := ctx.Err(); err != nil {
		return SyncResult{}, err
	}
	repoID, err := s.requireRepo(ctx, req.RepoID, "sync")
	if err != nil {
		return SyncResult{}, err
	}
	req.RepoID = repoID
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
	lease, err := s.store.AcquireWriter(ctx, cache.WriterRequest{Operation: "sync", RepoID: repoID, LockPath: s.lockPath})
	if err != nil {
		return SyncResult{}, err
	}
	defer s.store.ReleaseWriter(context.Background(), lease)
	remoteType, remoteID, err := s.syncTarget(ctx, req)
	if err != nil {
		return SyncResult{}, err
	}
	if err := s.validateRepoScope(ctx, repoID, remoteType); err != nil {
		return SyncResult{}, err
	}
	eventID := syncEventID(key)
	now := s.now().UTC()
	inProgress := cache.SyncEvent{RepoID: repoID, ID: eventID, SourceID: s.syncEventSourceID(ctx, req, remoteType, remoteID), RemoteType: remoteType, RemoteID: remoteID, Status: "in_progress", IdempotencyKey: key, Message: "sync started", CreatedAt: now}
	inProgressRecorded := true
	if _, err := s.store.GetSourceScoped(ctx, repoID, inProgress.SourceID); err == nil {
		if err := s.store.RecordSyncEvent(ctx, inProgress); err != nil {
			return SyncResult{}, err
		}
	} else if isCacheNotFound(err) {
		inProgressRecorded = false
	} else {
		return SyncResult{}, err
	}
	if err := s.store.IntegrityCheck(ctx); err != nil {
		failure := s.normalizeSyncFailure(err, req, remoteType, remoteID)
		if inProgressRecorded {
			_ = s.store.RecordSyncEvent(ctx, failedSyncEvent(inProgress, failure, s.now().UTC()))
		}
		return SyncResult{}, failure
	}
	graph, counts, err := s.fetchAndStage(ctx, req, remoteType, remoteID)
	if err != nil {
		failure := s.normalizeSyncFailure(err, req, remoteType, remoteID)
		if inProgressRecorded {
			_ = s.store.RecordSyncEvent(ctx, failedSyncEvent(inProgress, failure, s.now().UTC()))
		}
		if markErr := s.markMissingRemote(ctx, inProgress, failure, remoteType, remoteID); markErr != nil {
			return SyncResult{}, markErr
		}
		return SyncResult{}, failure
	}
	revision := ""
	if graph.SyncStatus != nil {
		revision = graph.SyncStatus.RemoteRevision
	}
	graph.SyncEvents = append(graph.SyncEvents, cache.SyncEvent{ID: eventID, SourceID: graph.Source.ID, RemoteType: remoteType, RemoteID: remoteID, RemoteRevision: revision, Status: "succeeded", IdempotencyKey: key, Message: syncEventMessage(counts), CreatedAt: now})
	if err := s.store.UpsertSyncGraph(ctx, syncGraphFromSourceGraph(req.RepoID, graph)); err != nil {
		if inProgressRecorded {
			_ = s.store.RecordSyncEvent(ctx, failedSyncEvent(inProgress, err, s.now().UTC()))
		}
		return SyncResult{}, err
	}
	if err := s.store.Checkpoint(ctx, "sync-complete"); err != nil {
		if inProgressRecorded {
			_ = s.store.RecordSyncEvent(ctx, failedSyncEvent(inProgress, err, s.now().UTC()))
		}
		return SyncResult{}, err
	}
	return SyncResult{IdempotencyKey: key, Status: "succeeded", Counts: counts, SyncEventID: eventID, Freshness: string(FreshnessFresh), GeneratedAt: s.now().UTC()}, nil
}

func (s *Service) CreateIssue(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if err := s.validateWriteScope(ctx, req, RepositoryScopeIssues); err != nil {
		return WriteCommandResult{}, err
	}
	if strings.TrimSpace(req.Title) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "title", Message: "title is required"}
	}
	return s.writeResult(ctx, "create-issue", req)
}

func (s *Service) UpdateIssue(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if err := s.validateWriteScope(ctx, req, RepositoryScopeIssues); err != nil {
		return WriteCommandResult{}, err
	}
	if req.Number == 0 && strings.TrimSpace(req.ID) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "issue", Message: "number or id is required"}
	}
	return s.writeResult(ctx, "update-issue", req)
}

func (s *Service) CreatePage(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if err := s.validateWriteScope(ctx, req, RepositoryScopeWiki); err != nil {
		return WriteCommandResult{}, err
	}
	if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Body) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "page", Message: "title and body are required"}
	}
	return s.writeResult(ctx, "create-page", req)
}

func (s *Service) UpdatePage(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if err := s.validateWriteScope(ctx, req, RepositoryScopeWiki); err != nil {
		return WriteCommandResult{}, err
	}
	if strings.TrimSpace(req.Slug) == "" && strings.TrimSpace(req.ID) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "page", Message: "slug or id is required"}
	}
	return s.writeResult(ctx, "update-page", req)
}

func (s *Service) AddComment(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if err := s.validateWriteScope(ctx, req, RepositoryScopeIssues); err != nil {
		return WriteCommandResult{}, err
	}
	if (req.Number == 0 && strings.TrimSpace(req.ID) == "") || strings.TrimSpace(req.Body) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "comment", Message: "issue and body are required"}
	}
	return s.writeResult(ctx, "add-comment", req)
}

func (s *Service) AddLabel(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if err := s.validateWriteScope(ctx, req, RepositoryScopeIssues); err != nil {
		return WriteCommandResult{}, err
	}
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
	return OperationResult{Command: command, Status: "ok", ProcessedCount: len(sources), Evidence: operationMode(req.Mode), GeneratedAt: s.now().UTC()}, nil
}

func operationMode(mode string) string {
	mode = strings.TrimSpace(mode)
	if mode == "" {
		return "default"
	}
	return mode
}

func (s *Service) seedMinimumCorpus(ctx context.Context) error {
	now := s.now().UTC()
	taskBody := "# Task\n\nTASK-001 keeps the offline export walkthrough cache-first.\n"
	taskHash := index.ContentHash(taskBody)
	if err := s.store.UpsertSourceGraph(ctx, cache.SourceGraph{
		Source:     cache.Source{ID: "TASK-001", Kind: "task", Path: "project/tasks/day7.md", Title: "Offline Export Walkthrough", Body: taskBody, Status: "ready", Labels: []string{"task"}, ContentHash: taskHash, CreatedAt: now, UpdatedAt: now},
		Identities: []cache.Identity{{SourceID: "TASK-001", AliasType: "id", Alias: "TASK-001"}},
		SyncStatus: &cache.SyncStatus{SourceID: "TASK-001", RemoteType: "fixture", RemoteID: "task-001", RemoteRevision: taskHash, Status: "fresh", LastFetchedAt: now},
	}); err != nil {
		return normalizeError(err, "source", "TASK-001")
	}
	body := "---\nstatus: ready\nlabels: backlog,design\n---\n# Backlog\n\nDOC-123 describes the cache-first backlog.\n\nSee [task](TASK-001).\n"
	hash := index.ContentHash(body)
	graph := cache.SourceGraph{
		Source:     cache.Source{ID: "DOC-123", Kind: "doc", Path: "docs/day7-offline.md", Title: "Day 7 Offline Backlog", Body: body, Status: "ready", Labels: []string{"backlog", "design"}, ContentHash: hash, CreatedAt: now, UpdatedAt: now},
		Identities: []cache.Identity{{SourceID: "DOC-123", AliasType: "remote", Alias: "wiki/day7-offline", Remote: cache.RemoteAlias{Type: "wiki", ID: "day7-offline"}}},
		Links:      []cache.Link{{SourceID: "DOC-123", TargetID: "TASK-001", Kind: "markdown", Text: "task"}},
		SyncStatus: &cache.SyncStatus{SourceID: "DOC-123", RemoteType: "fixture", RemoteID: "day7-offline", RemoteRevision: hash, Status: "fresh", LastFetchedAt: now},
	}
	if err := s.store.UpsertSourceGraph(ctx, graph); err != nil {
		return normalizeError(err, "source", graph.Source.ID)
	}
	return nil
}

func chunksForSource(source cache.Source) []cache.Chunk {
	idxSource := indexSourceRecord(source)
	parsed := index.ParseSource(idxSource)
	chunks := index.ChunkSource(idxSource, parsed)
	out := make([]cache.Chunk, 0, len(chunks))
	for _, chunk := range chunks {
		out = append(out, cacheChunk(chunk))
	}
	return out
}

func cacheChunk(chunk index.Chunk) cache.Chunk {
	return cache.Chunk{RepoID: chunk.RepoID, ID: chunk.ID, SourceID: chunk.SourceID, RecordID: chunk.RecordID, SnapshotID: chunk.SnapshotID, ContentHash: chunk.ContentHash, ByteStart: chunk.ByteStart, ByteEnd: chunk.ByteEnd, LineStart: chunk.LineStart, LineEnd: chunk.LineEnd, HeadingPath: append([]string(nil), chunk.HeadingPath...), Text: chunk.Text, NormalizedText: chunk.NormalizedText, InheritedMetadata: copyStringMap(chunk.InheritedMetadata), OutboundLinks: sortedStrings(chunk.OutboundLinks), ResolvedAliases: copyStringMap(chunk.ResolvedAliases), Embedding: append([]byte(nil), chunk.Embedding...), Policy: string(chunk.Policy)}
}

func indexChunks(chunks []cache.Chunk) []index.Chunk {
	out := make([]index.Chunk, 0, len(chunks))
	for _, chunk := range chunks {
		out = append(out, index.Chunk{RepoID: chunk.RepoID, ID: chunk.ID, SourceID: chunk.SourceID, RecordID: chunk.RecordID, SnapshotID: chunk.SnapshotID, ContentHash: chunk.ContentHash, ByteStart: chunk.ByteStart, ByteEnd: chunk.ByteEnd, LineStart: chunk.LineStart, LineEnd: chunk.LineEnd, HeadingPath: append([]string(nil), chunk.HeadingPath...), Text: chunk.Text, NormalizedText: chunk.NormalizedText, InheritedMetadata: copyStringMap(chunk.InheritedMetadata), OutboundLinks: sortedStrings(chunk.OutboundLinks), ResolvedAliases: copyStringMap(chunk.ResolvedAliases), Embedding: append([]byte(nil), chunk.Embedding...), Policy: index.ChunkPolicy(chunk.Policy)})
	}
	return out
}

func indexSourceRecord(source cache.Source) index.SourceRecord {
	aliases := make([]index.Alias, 0, len(source.Aliases))
	remoteAliases := make([]index.Alias, 0, len(source.Aliases))
	for _, alias := range source.Aliases {
		if alias.AliasType != "" && alias.Alias != "" {
			aliases = append(aliases, index.Alias{Type: alias.AliasType, ID: alias.Alias})
		}
		if alias.Remote.Type != "" && alias.Remote.ID != "" {
			remoteAliases = append(remoteAliases, index.Alias{Type: alias.Remote.Type, ID: alias.Remote.ID})
		}
	}
	return index.SourceRecord{RepoID: source.RepoID, ID: source.ID, RecordID: source.ID, Kind: source.Kind, Path: source.Path, Title: source.Title, Body: source.Body, Metadata: map[string]string{"content_hash": source.ContentHash, "source_updated_at": source.UpdatedAt.UTC().Format(time.RFC3339Nano)}, Status: source.Status, UpdatedAt: source.UpdatedAt.UTC(), Aliases: aliases, RemoteAliases: remoteAliases}
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
	identity, err := s.store.ResolveAliasScoped(ctx, req.RepoID, cache.RemoteAlias{Type: remoteType, ID: remoteID})
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
		source, err := s.store.GetSourceScoped(ctx, req.RepoID, req.StableID)
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

func syncGraphFromSourceGraph(repoID string, graph cache.SourceGraph) cache.SyncGraph {
	revision := ""
	if graph.SyncStatus != nil {
		revision = graph.SyncStatus.RemoteRevision
	}
	record := cache.Record{RepoID: repoID, ID: graph.Source.ID, Type: graph.Source.Kind, Path: graph.Source.Path, Title: graph.Source.Title, Body: graph.Source.Body, Status: graph.Source.Status, Labels: graph.Source.Labels, ContentHash: graph.Source.ContentHash, Provenance: cache.ProvenanceRemote, CreatedAt: graph.Source.CreatedAt, UpdatedAt: graph.Source.UpdatedAt, RemoteRevision: revision}
	if graph.SyncStatus != nil {
		record.RemoteType = graph.SyncStatus.RemoteType
		record.RemoteID = graph.SyncStatus.RemoteID
	}
	revisions := []cache.RemoteRevision{}
	if graph.SyncStatus != nil {
		revisions = append(revisions, cache.RemoteRevision{RepoID: repoID, RecordID: graph.Source.ID, RemoteType: graph.SyncStatus.RemoteType, RemoteID: graph.SyncStatus.RemoteID, RemoteRevision: graph.SyncStatus.RemoteRevision, Status: graph.SyncStatus.Status, LastFetchedAt: graph.SyncStatus.LastFetchedAt})
	}
	return cache.SyncGraph{RepoID: repoID, Record: record, Comments: graph.Comments, Identities: graph.Identities, Links: graph.Links, Chunks: graph.Chunks, RemoteRevisions: revisions, SyncEvents: graph.SyncEvents}
}

func (s *Service) stageIssue(ctx context.Context, req SyncRequest, remoteType, remoteID string, issue gitcode.Issue) (cache.SourceGraph, SyncCounts, error) {
	body := issue.Body
	if req.MaxSize > 0 && int64(len(body)+len(issue.Title)) > req.MaxSize {
		return cache.SourceGraph{}, SyncCounts{}, gitcode.ErrPayloadTooLarge{Endpoint: remoteID, Limit: req.MaxSize, Size: int64(len(body) + len(issue.Title))}
	}
	stableID := req.StableID
	if stableID == "" {
		stableID = s.resolveOrFallback(ctx, req.RepoID, remoteType, remoteID, fallbackSourceID(remoteType, remoteID))
	}
	if err := s.guardRemoteAlias(ctx, req.RepoID, remoteType, remoteID, stableID); err != nil {
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
	existing, err := s.store.GetSourceScoped(ctx, req.RepoID, stableID)
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
	graph := cache.SourceGraph{Source: cache.Source{RepoID: req.RepoID, ID: stableID, Kind: "issue", Path: "issues/" + remoteID + ".md", Title: issue.Title, Body: body, Status: status, Labels: issue.Labels, ContentHash: hash, CreatedAt: created, UpdatedAt: updated}, Identities: []cache.Identity{{RepoID: req.RepoID, SourceID: stableID, AliasType: remoteType, Alias: remoteID, Remote: cache.RemoteAlias{Type: remoteType, ID: remoteID}}}, SyncStatus: &cache.SyncStatus{RepoID: req.RepoID, SourceID: stableID, RemoteType: remoteType, RemoteID: remoteID, RemoteRevision: hash, Status: "fresh", LastFetchedAt: now}}
	comments, err := s.client.ListIssueComments(ctx, gitcode.IssueRequest{Number: issue.Number, KnownRemoteAlias: true, RemoteAlias: remoteID})
	if err != nil {
		return cache.SourceGraph{}, SyncCounts{}, err
	}
	for _, comment := range comments.Items {
		commentID := comment.ID
		if commentID == "" {
			commentID = contentHash(remoteID, comment.Author, comment.Body, comment.CreatedAt)
		}
		commentUpdated := comment.UpdatedAt.UTC()
		if commentUpdated.IsZero() {
			commentUpdated = now
		}
		commentCreated := comment.CreatedAt.UTC()
		if commentCreated.IsZero() {
			commentCreated = commentUpdated
		}
		graph.Comments = append(graph.Comments, cache.RecordComment{RepoID: req.RepoID, RecordID: stableID, CommentID: commentID, Author: comment.Author, Body: comment.Body, ContentHash: contentHash(commentID, comment.Author, comment.Body), RemoteRevision: contentHash(commentUpdated), CreatedAt: commentCreated, UpdatedAt: commentUpdated})
	}
	graph.Chunks = chunksForSource(graph.Source)
	return graph, counts, nil
}

func (s *Service) stageWiki(ctx context.Context, req SyncRequest, remoteType, remoteID string, page gitcode.WikiPage) (cache.SourceGraph, SyncCounts, error) {
	body := page.Body
	if req.MaxSize > 0 && int64(len(body)+len(page.Title)) > req.MaxSize {
		return cache.SourceGraph{}, SyncCounts{}, gitcode.ErrPayloadTooLarge{Endpoint: remoteID, Limit: req.MaxSize, Size: int64(len(body) + len(page.Title))}
	}
	stableID := req.StableID
	if stableID == "" {
		stableID = s.resolveOrFallback(ctx, req.RepoID, remoteType, remoteID, fallbackSourceID(remoteType, remoteID))
	}
	if err := s.guardRemoteAlias(ctx, req.RepoID, remoteType, remoteID, stableID); err != nil {
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
	existing, err := s.store.GetSourceScoped(ctx, req.RepoID, stableID)
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
	graph := cache.SourceGraph{Source: cache.Source{RepoID: req.RepoID, ID: stableID, Kind: "wiki", Path: "wiki/" + remoteID + ".md", Title: page.Title, Body: body, Status: "fresh", ContentHash: hash, CreatedAt: created, UpdatedAt: updated}, Identities: []cache.Identity{{RepoID: req.RepoID, SourceID: stableID, AliasType: remoteType, Alias: remoteID, Remote: cache.RemoteAlias{Type: remoteType, ID: remoteID}}}, SyncStatus: &cache.SyncStatus{RepoID: req.RepoID, SourceID: stableID, RemoteType: remoteType, RemoteID: remoteID, RemoteRevision: revision, Status: "fresh", LastFetchedAt: now}}
	graph.Chunks = chunksForSource(graph.Source)
	return graph, counts, nil
}

func (s *Service) BuildAdapterRoute(ctx context.Context, repoID string, requestedScope RepositoryScope) (RepositoryRoute, error) {
	repoID, err := s.requireRepo(ctx, repoID, "route")
	if err != nil {
		return RepositoryRoute{}, err
	}
	repo, err := s.store.GetRepository(ctx, repoID)
	if err != nil {
		return RepositoryRoute{}, normalizeError(err, "repository", repoID)
	}
	for _, scope := range repo.Scopes {
		if RepositoryScope(scope) == requestedScope {
			route := RepositoryRoute{RepoID: repo.RepoID, Owner: repo.Owner, Name: repo.Name, APIBaseURL: repo.APIBaseURL}
			for _, configured := range repo.Scopes {
				route.Scopes = append(route.Scopes, RepositoryScope(configured))
			}
			return route, nil
		}
	}
	return RepositoryRoute{}, ErrInvalidQuery{Field: "scope", Message: string(requestedScope) + " scope is not enabled for repo " + repoID}
}

func (s *Service) validateRepoScope(ctx context.Context, repoID, remoteType string) error {
	want := RepositoryScopeIssues
	if remoteType == "wiki" || remoteType == "page" || remoteType == "remote" {
		want = RepositoryScopeWiki
	}
	_, err := s.BuildAdapterRoute(ctx, repoID, want)
	return err
}

func (s *Service) guardRemoteAlias(ctx context.Context, repoID, remoteType, remoteID, stableID string) error {
	identity, err := s.store.ResolveAliasScoped(ctx, repoID, cache.RemoteAlias{Type: remoteType, ID: remoteID})
	if err == nil && identity.SourceID != "" && identity.SourceID != stableID {
		return gitcode.ErrRemoteCollision{Alias: remoteType + ":" + remoteID, ExistingID: identity.SourceID, NewID: stableID}
	}
	if err != nil && !isCacheNotFound(err) {
		return err
	}
	return nil
}

func (s *Service) resolveOrFallback(ctx context.Context, repoID, remoteType, remoteID, fallback string) string {
	identity, err := s.store.ResolveAliasScoped(ctx, repoID, cache.RemoteAlias{Type: remoteType, ID: remoteID})
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
	source, err := s.store.GetSourceScoped(ctx, event.RepoID, event.SourceID)
	if err != nil {
		return failure
	}
	graph := cache.SourceGraph{Source: source, SyncStatus: &cache.SyncStatus{RepoID: event.RepoID, SourceID: event.SourceID, RemoteType: remoteType, RemoteID: remoteID, RemoteRevision: event.RemoteRevision, Status: "not_found", LastFetchedAt: s.now().UTC()}}
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

func (s *Service) validateWriteScope(ctx context.Context, req WriteCommandRequest, scope RepositoryScope) error {
	repoID, err := s.requireRepo(ctx, req.Repo, "write")
	if err != nil {
		return err
	}
	req.Repo = repoID
	remoteType := "issue"
	if scope == RepositoryScopeWiki {
		remoteType = "wiki"
	}
	return s.validateRepoScope(ctx, repoID, remoteType)
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

func (s *Service) requireRepo(ctx context.Context, repoID, operation string) (string, error) {
	repoID = strings.TrimSpace(repoID)
	if repoID == "" {
		return "", ErrRepoRequired{Operation: operation}
	}
	if _, err := s.store.GetRepository(ctx, repoID); err != nil {
		return "", normalizeError(err, "repository", repoID)
	}
	return repoID, nil
}

func (s *Service) resolveScopedStableID(ctx context.Context, repoID, id, aliasType, aliasID string) (string, error) {
	if id != "" {
		if aliasType == "" && aliasID == "" {
			if parsedType, parsedID, ok := parseRecordRef(id); ok {
				aliasType, aliasID = parsedType, parsedID
			} else if _, err := s.store.GetSourceScoped(ctx, repoID, id); err == nil {
				return id, nil
			} else if !isCacheNotFound(err) {
				return "", normalizeError(err, "source", id)
			}
		}
	}
	if aliasType != "" || aliasID != "" {
		if aliasType == "" || aliasID == "" {
			return "", ErrInvalidQuery{Field: "alias", Message: "alias type and id are required together"}
		}
		identity, err := s.store.ResolveAliasScoped(ctx, repoID, cache.RemoteAlias{Type: aliasType, ID: aliasID})
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

func parseRecordRef(ref string) (string, string, bool) {
	parts := strings.SplitN(strings.TrimSpace(ref), ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func (s *Service) DiagnoseUnscopedAlias(ctx context.Context, aliasType, aliasID string) error {
	identities, err := s.store.DiagnoseAlias(ctx, cache.RemoteAlias{Type: aliasType, ID: aliasID})
	if err != nil {
		return normalizeError(err, "alias", aliasType+":"+aliasID)
	}
	if len(identities) == 0 {
		return ErrNotFound{Kind: "alias", ID: aliasType + ":" + aliasID}
	}
	repos := map[string]struct{}{}
	for _, identity := range identities {
		repos[identity.RepoID] = struct{}{}
	}
	if len(repos) > 1 {
		return ErrAmbiguousAlias{Alias: aliasType + ":" + aliasID, Repos: sortedKeys(repos)}
	}
	return ErrRepoRequired{Operation: "alias lookup"}
}

func (s *Service) snippetFromLines(ctx context.Context, repoID, id string, start, end int) (SnippetResult, error) {
	if start <= 0 || end <= 0 || end < start {
		return SnippetResult{}, ErrInvalidQuery{Field: "line range", Message: "line_start and line_end must be positive and ordered"}
	}
	freshness, err := s.freshnessReport(ctx, repoID, index.ChunkQuery{RepoID: repoID, SourceID: id})
	if err != nil {
		return SnippetResult{}, err
	}
	source, err := s.store.GetSourceScoped(ctx, repoID, id)
	if err != nil {
		return SnippetResult{}, normalizeError(err, "source", id)
	}
	lines := strings.Split(source.Body, "\n")
	if start > len(lines) {
		return SnippetResult{}, ErrInvalidQuery{Field: "line_start", Message: "line_start is beyond source body"}
	}
	warnings := warningCodes(freshness.Warnings)
	actualEnd := end
	if actualEnd > len(lines) {
		actualEnd = len(lines)
		warnings = append(warnings, ErrRangeClamped{RequestedStart: start, RequestedEnd: end, ActualStart: start, ActualEnd: actualEnd}.Error())
	}
	return SnippetResult{RepoID: source.RepoID, ID: source.ID, Path: source.Path, Text: strings.Join(lines[start-1:actualEnd], "\n"), LineStart: start, LineEnd: actualEnd, Warnings: warnings}, nil
}

func (s *Service) freshnessReport(ctx context.Context, repoID string, query index.ChunkQuery) (index.IndexFreshnessReport, error) {
	sources, err := s.indexSources(ctx, repoID)
	if err != nil {
		return index.IndexFreshnessReport{}, err
	}
	chunks, err := s.store.ListChunks(ctx, cache.ChunkFilter{RepoID: query.RepoID, SourceID: query.SourceID, RecordID: query.RecordID, SnapshotID: query.SnapshotID, Policy: string(query.Policy)})
	if err != nil {
		return index.IndexFreshnessReport{}, normalizeError(err, "chunks", query.SourceID)
	}
	links, err := s.store.ListLinks(ctx, cache.LinkFilter{RepoID: repoID})
	if err != nil {
		return index.IndexFreshnessReport{}, normalizeError(err, "links", "")
	}
	linkReport := linkStaleReport(sources, links)
	return index.BuildFreshnessReport(ctx, sources, nil, indexChunks(chunks), nil, linkReport, query), nil
}

func (s *Service) indexSources(ctx context.Context, repoID string) ([]index.SourceRecord, error) {
	sources, err := s.store.ListSources(ctx, cache.SourceFilter{RepoID: repoID})
	if err != nil {
		return nil, normalizeError(err, "sources", "")
	}
	out := make([]index.SourceRecord, 0, len(sources))
	for _, source := range sources {
		record := indexSourceRecord(source)
		if status, err := s.store.GetSyncStatusScoped(ctx, repoID, source.ID); err == nil {
			record.RemoteRevision = status.RemoteRevision
			record.SyncRevision = status.RemoteRevision
		}
		out = append(out, record)
	}
	return out, nil
}

func linkStaleReport(sources []index.SourceRecord, links []cache.Link) index.StaleReport {
	sourceIDs := map[string]struct{}{}
	for _, source := range sources {
		sourceIDs[source.ID] = struct{}{}
	}
	affected := map[string]bool{}
	missing := map[string]bool{}
	for _, link := range links {
		if _, ok := sourceIDs[link.TargetID]; !ok {
			affected[link.SourceID] = true
			missing[link.TargetID] = true
		}
	}
	return index.StaleReport{TotalStaleBacklinks: len(missing), AffectedSourceIDs: indexSortedKeys(affected), UnresolvedTargets: indexSortedKeys(missing)}
}

func warningCodes(warnings []IndexWarning) []string {
	out := make([]string, 0, len(warnings))
	seen := map[string]bool{}
	for _, warning := range warnings {
		if warning.Code == "" || seen[warning.Code] {
			continue
		}
		seen[warning.Code] = true
		out = append(out, warning.Code)
	}
	return out
}

func indexSortedKeys(values map[string]bool) []string {
	keys := []string{}
	for key := range values {
		if key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func filterWarningsForSources(warnings []IndexWarning, include map[string]struct{}) []IndexWarning {
	if len(include) == 0 {
		return append([]IndexWarning(nil), warnings...)
	}
	out := make([]IndexWarning, 0, len(warnings))
	for _, warning := range warnings {
		if _, ok := include[warning.SourceID]; ok {
			out = append(out, warning)
		}
	}
	return out
}

func (s *Service) snippetFromChunk(ctx context.Context, repoID, id, chunkID string) (SnippetResult, error) {
	freshness, err := s.freshnessReport(ctx, repoID, index.ChunkQuery{RepoID: repoID, SourceID: id})
	if err != nil {
		return SnippetResult{}, err
	}
	chunks, err := s.store.GetChunksScoped(ctx, repoID, id)
	if err != nil {
		return SnippetResult{}, normalizeError(err, "chunks", id)
	}
	for _, chunk := range chunks {
		if chunk.ID == chunkID {
			source, err := s.store.GetSourceScoped(ctx, repoID, id)
			if err != nil {
				return SnippetResult{}, normalizeError(err, "source", id)
			}
			return SnippetResult{RepoID: source.RepoID, ID: id, Path: source.Path, Text: chunk.Text, LineStart: chunk.LineStart, LineEnd: chunk.LineEnd, ChunkID: chunk.ID, Warnings: warningCodes(freshness.Warnings)}, nil
		}
	}
	if len(freshness.Warnings) > 0 {
		source, err := s.store.GetSourceScoped(ctx, repoID, id)
		if err != nil {
			return SnippetResult{}, normalizeError(err, "source", id)
		}
		return SnippetResult{RepoID: source.RepoID, ID: id, Path: source.Path, Warnings: warningCodes(freshness.Warnings)}, nil
	}
	return SnippetResult{}, ErrNotFound{Kind: "chunk", ID: chunkID}
}

func (s *Service) buildSnapshot(ctx context.Context, req ExportSnapshotRequest) (Snapshot, error) {
	repoID, err := s.requireRepo(ctx, req.RepoID, "export")
	if err != nil {
		return Snapshot{}, err
	}
	sources, err := s.store.ListSources(ctx, cache.SourceFilter{RepoID: repoID})
	if err != nil {
		return Snapshot{}, normalizeError(err, "sources", "")
	}
	if len(sources) == 0 {
		return Snapshot{}, ErrCacheEmpty{Message: "cache has no sources"}
	}
	include := map[string]struct{}{}
	for _, id := range req.SourceIDs {
		include[id] = struct{}{}
	}
	freshnessQuery := index.ChunkQuery{RepoID: repoID}
	if len(include) == 1 {
		for id := range include {
			freshnessQuery.SourceID = id
		}
	}
	freshness, err := s.freshnessReport(ctx, repoID, freshnessQuery)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{SchemaVersion: "gitcode-mcp.snapshot.v1", RepoID: repoID, Warnings: filterWarningsForSources(freshness.Warnings, include)}
	for _, source := range sources {
		if len(include) > 0 {
			if _, ok := include[source.ID]; !ok {
				continue
			}
		}
		labels := append([]string(nil), source.Labels...)
		sort.Strings(labels)
		body := ""
		if req.IncludeBody {
			body = source.Body
		}
		snapshot.Sources = append(snapshot.Sources, SnapshotSource{RepoID: source.RepoID, ID: source.ID, Kind: source.Kind, Path: source.Path, Title: source.Title, Body: body, Status: source.Status, Labels: labels, ContentHash: source.ContentHash, CreatedAt: source.CreatedAt.UTC(), UpdatedAt: source.UpdatedAt.UTC()})
		aliases, err := s.store.GetIdentityMapScoped(ctx, repoID, source.ID)
		if err != nil {
			return Snapshot{}, normalizeError(err, "aliases", source.ID)
		}
		for _, alias := range aliases {
			snapshot.Aliases = append(snapshot.Aliases, SnapshotAlias{RepoID: alias.RepoID, SourceID: alias.SourceID, AliasKind: alias.AliasType, AliasValue: alias.Alias, RemoteKind: alias.Remote.Type, RemoteID: alias.Remote.ID})
		}
		links, err := s.store.ListLinks(ctx, cache.LinkFilter{RepoID: repoID, SourceID: source.ID})
		if err != nil {
			return Snapshot{}, normalizeError(err, "links", source.ID)
		}
		for _, link := range links {
			snapshot.Links = append(snapshot.Links, SnapshotLink{RepoID: link.RepoID, SourceID: link.SourceID, TargetID: link.TargetID, LinkType: link.Kind, Text: link.Text})
		}
		backlinks, err := s.store.ListLinks(ctx, cache.LinkFilter{RepoID: repoID, TargetID: source.ID})
		if err != nil {
			return Snapshot{}, normalizeError(err, "backlinks", source.ID)
		}
		for _, link := range backlinks {
			snapshot.Backlinks = append(snapshot.Backlinks, SnapshotLink{RepoID: link.RepoID, SourceID: link.SourceID, TargetID: link.TargetID, LinkType: link.Kind, Text: link.Text})
		}
		status, err := s.store.GetSyncStatusScoped(ctx, repoID, source.ID)
		if err != nil {
			var notFound ErrNotFound
			if !errors.As(normalizeError(err, "sync status", source.ID), &notFound) {
				return Snapshot{}, normalizeError(err, "sync status", source.ID)
			}
			snapshot.SyncStatus = append(snapshot.SyncStatus, SnapshotSyncStatus{RepoID: source.RepoID, SourceID: source.ID, Status: "unknown", Freshness: "unknown"})
		} else {
			snapshot.SyncStatus = append(snapshot.SyncStatus, SnapshotSyncStatus{RepoID: status.RepoID, SourceID: source.ID, RemoteType: status.RemoteType, RemoteID: status.RemoteID, RemoteRevision: status.RemoteRevision, Status: status.Status, Freshness: freshnessFor(source, status), LastFetchedAt: status.LastFetchedAt.UTC()})
		}
		chunks, err := s.store.GetChunksScoped(ctx, repoID, source.ID)
		if err != nil {
			return Snapshot{}, normalizeError(err, "chunks", source.ID)
		}
		for _, chunk := range chunks {
			snapshot.Chunks = append(snapshot.Chunks, SnapshotChunk{RepoID: chunk.RepoID, ID: chunk.ID, SourceID: chunk.SourceID, ContentHash: chunk.ContentHash, ByteStart: chunk.ByteStart, ByteEnd: chunk.ByteEnd, LineStart: chunk.LineStart, LineEnd: chunk.LineEnd, HeadingPath: append([]string(nil), chunk.HeadingPath...), Text: chunk.Text, NormalizedText: chunk.NormalizedText, InheritedMetadata: copyStringMap(chunk.InheritedMetadata), OutboundLinks: sortedStrings(chunk.OutboundLinks), ResolvedAliases: copyStringMap(chunk.ResolvedAliases)})
		}
	}
	if len(snapshot.Sources) == 0 {
		return Snapshot{}, ErrCacheEmpty{Message: "cache has no matching sources"}
	}
	snapshot.CreatedAt = snapshotCreatedAt(snapshot)
	sortSnapshot(&snapshot)
	return snapshot, nil
}

func (s *Service) loadSnapshotRef(ctx context.Context, repoID string, ref SnapshotRef, format string) (Snapshot, error) {
	switch strings.ToLower(ref.Kind) {
	case "", "current":
		return s.buildSnapshot(ctx, ExportSnapshotRequest{RepoID: repoID, Format: format, IncludeBody: true})
	case "path":
		b, err := os.ReadFile(ref.Path)
		if err != nil {
			return Snapshot{}, err
		}
		return parseSnapshotBytes(b, ref.Format)
	case "bytes":
		return parseSnapshotBytes(ref.Bytes, ref.Format)
	default:
		return Snapshot{}, ErrInvalidQuery{Field: "snapshot_ref", Message: "kind must be current, path, or bytes"}
	}
}
