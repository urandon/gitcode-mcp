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

	"gitcode-mcp/internal/audit"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/index"
)

type Service struct {
	store                  cache.Store
	client                 gitcode.Client
	now                    func() time.Time
	lockPath               string
	providerMode           gitcode.ProviderMode
	writeCredentialPresent bool
}

func New(store cache.Store) *Service {
	svc, err := NewWithMode(store, gitcode.ProviderModeFixture, "", ServiceConfig{})
	if err != nil {
		return &Service{store: store, client: sanitizedFixtureClient{}, now: func() time.Time { return time.Now().UTC() }, lockPath: filepath.Join(os.TempDir(), "gitcode-mcp-sync.lock"), providerMode: gitcode.ProviderModeFixture}
	}
	return svc
}

func NewWithClient(store cache.Store, client gitcode.Client) *Service {
	svc := New(store)
	svc.client = client
	svc.providerMode = gitcode.ProviderMode("custom")
	return svc
}

func (s *Service) ProviderMode() gitcode.ProviderMode {
	if s.providerMode == "" {
		return gitcode.ProviderModeFixture
	}
	return s.providerMode
}

func NewWithMode(store cache.Store, mode gitcode.ProviderMode, token string, cfg ServiceConfig) (*Service, error) {
	switch mode {
	case gitcode.ProviderModeFixture:
		return &Service{
			store:        store,
			client:       sanitizedFixtureClient{},
			now:          func() time.Time { return time.Now().UTC() },
			lockPath:     filepath.Join(os.TempDir(), "gitcode-mcp-sync.lock"),
			providerMode: gitcode.ProviderModeFixture,
		}, nil
	case gitcode.ProviderModeLive:
		token = strings.TrimSpace(token)
		if token == "" {
			return nil, gitcode.ErrProviderUnavailable{Reason: "live provider requires a token"}
		}
		baseURL := strings.TrimSpace(cfg.BaseURL)
		if baseURL == "" {
			baseURL = "https://gitcode.com"
		}
		timeout := cfg.Timeout
		maxResponseSize := cfg.MaxResponseSize
		if maxResponseSize <= 0 {
			maxResponseSize = 10 << 20
		}
		maxRetries := cfg.MaxRetries
		userAgent := cfg.UserAgent
		if userAgent == "" {
			userAgent = "gitcode-mcp"
		}
		client, err := gitcode.NewHTTPClient(gitcode.Config{
			BaseURL:         baseURL,
			Token:           token,
			Timeout:         timeout,
			MaxResponseSize: maxResponseSize,
			MaxRetries:      maxRetries,
			UserAgent:       userAgent,
			Pagination:      cfg.Pagination,
		})
		if err != nil {
			return nil, err
		}
		return &Service{
			store:                  store,
			client:                 gitcode.Client(client),
			now:                    func() time.Time { return time.Now().UTC() },
			lockPath:               filepath.Join(os.TempDir(), "gitcode-mcp-sync.lock"),
			providerMode:           gitcode.ProviderModeLive,
			writeCredentialPresent: true,
		}, nil
	case gitcode.ProviderModeUnavailable:
		return nil, gitcode.ErrProviderUnavailable{Reason: "provider unavailable"}
	default:
		return nil, gitcode.ErrProviderUnavailable{Reason: "unknown provider mode " + string(mode)}
	}
}

type sanitizedFixtureClient struct{}

func (sanitizedFixtureClient) FixtureBoundaryMode() string {
	return gitcode.FixtureBoundaryMode
}

func (sanitizedFixtureClient) FixtureMarkerIDs() []string {
	return gitcode.FixtureMarkerIDs()
}

func (sanitizedFixtureClient) ListIssues(context.Context, gitcode.IssueListRequest) (gitcode.Page[gitcode.IssueSummary], error) {
	now := fixtureNow()
	return gitcode.Page[gitcode.IssueSummary]{Items: []gitcode.IssueSummary{{Number: 42, Title: "Fixture Issue", State: "open", CreatedAt: now, UpdatedAt: now}}, Page: 1, PerPage: 1, TotalCount: 1}, nil
}

func (sanitizedFixtureClient) GetIssue(context.Context, gitcode.IssueRequest) (gitcode.Issue, error) {
	now := fixtureNow()
	return gitcode.Issue{Number: 42, Title: "Fixture Issue", Body: "# Issue\n\nremote fixture issue body for offline search test", State: "open", CreatedAt: now, UpdatedAt: now}, nil
}

func (sanitizedFixtureClient) ListIssueComments(context.Context, gitcode.IssueRequest) (gitcode.Page[gitcode.Comment], error) {
	now := fixtureNow()
	return gitcode.Page[gitcode.Comment]{Items: []gitcode.Comment{{ID: "c1", Author: "fixture-user", Body: "comment", CreatedAt: now, UpdatedAt: now}}, Page: 1, PerPage: 1, TotalCount: 1}, nil
}

func (sanitizedFixtureClient) GetWikiPage(context.Context, gitcode.WikiPageRequest) (gitcode.WikiPage, error) {
	now := fixtureNow()
	return gitcode.WikiPage{Slug: "Home", Title: "Fixture Wiki", Body: "# Wiki\n\nremote fixture wiki body for offline search test", Revision: "rev-home", CreatedAt: now, UpdatedAt: now}, nil
}

func (sanitizedFixtureClient) ListWikiPages(context.Context, gitcode.WikiListRequest) (gitcode.Page[gitcode.WikiPage], error) {
	now := fixtureNow()
	return gitcode.Page[gitcode.WikiPage]{Items: []gitcode.WikiPage{{Slug: "Home", Title: "Fixture Wiki", Body: "# Wiki\n\nremote fixture wiki body for offline search test", Revision: "rev-home", CreatedAt: now, UpdatedAt: now}}, Page: 1, PerPage: 1, TotalCount: 1}, nil
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
	return gitcode.WriteResult[gitcode.Issue]{}, gitcode.FixtureReadOnlyError("sanitized fixture write")
}

func (sanitizedFixtureClient) UpdateIssue(context.Context, gitcode.UpdateIssueRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	return gitcode.WriteResult[gitcode.Issue]{}, gitcode.FixtureReadOnlyError("sanitized fixture write")
}

func (sanitizedFixtureClient) CreateIssueComment(context.Context, gitcode.CreateIssueCommentRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Comment], error) {
	return gitcode.WriteResult[gitcode.Comment]{}, gitcode.FixtureReadOnlyError("sanitized fixture write")
}

func (sanitizedFixtureClient) CreateWikiPage(context.Context, gitcode.CreateWikiPageRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.WikiPage], error) {
	return gitcode.WriteResult[gitcode.WikiPage]{}, gitcode.FixtureReadOnlyError("sanitized fixture write")
}

func (sanitizedFixtureClient) UpdateWikiPage(context.Context, gitcode.UpdateWikiPageRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.WikiPage], error) {
	return gitcode.WriteResult[gitcode.WikiPage]{}, gitcode.FixtureReadOnlyError("sanitized fixture write")
}

func (sanitizedFixtureClient) AddLabel(context.Context, gitcode.LabelRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	return gitcode.WriteResult[gitcode.Issue]{}, gitcode.FixtureReadOnlyError("sanitized fixture write")
}

func (sanitizedFixtureClient) RemoveLabel(context.Context, gitcode.LabelRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	return gitcode.WriteResult[gitcode.Issue]{}, gitcode.FixtureReadOnlyError("sanitized fixture write")
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

func (s *Service) SearchSources(ctx context.Context, req SearchSourcesRequest) (SearchSourcesResult, error) {
	repoID, err := s.requireRepo(ctx, req.RepoID, "search")
	if err != nil {
		return SearchSourcesResult{}, err
	}
	if strings.TrimSpace(req.Query) == "" {
		return SearchSourcesResult{}, ErrInvalidQuery{Field: "query", Message: "query is required"}
	}
	results, err := s.store.SearchSources(ctx, cache.SearchQuery{RepoID: repoID, Query: req.Query, Kind: req.Kind, Limit: req.Limit})
	if err != nil {
		return SearchSourcesResult{}, normalizeError(err, "search", req.Query)
	}
	out := make([]SearchSourceResult, 0, len(results))
	updated := map[string]time.Time{}
	for _, result := range results {
		source, err := s.store.GetSourceScoped(ctx, repoID, result.ID)
		if err != nil {
			return SearchSourcesResult{}, normalizeError(err, "source", result.ID)
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
	return SearchSourcesResult{RepoID: repoID, Query: req.Query, Results: out, Limit: req.Limit, Offset: req.Offset}, nil
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
	return sourceRecord(source, links, backlinks.Backlinks), nil
}

func (s *Service) ListSources(ctx context.Context, req ListSourcesRequest) (ListSourcesResult, error) {
	repoID, err := s.requireRepo(ctx, req.RepoID, "list")
	if err != nil {
		return ListSourcesResult{}, err
	}
	sources, err := s.store.ListSources(ctx, cache.SourceFilter{RepoID: repoID, Kind: req.Kind, Status: req.Status, Limit: req.limitPlusOffset()})
	if err != nil {
		return ListSourcesResult{}, normalizeError(err, "sources", "")
	}
	if len(sources) == 0 {
		return ListSourcesResult{}, ErrCacheEmpty{Message: "cache has no sources"}
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
	return ListSourcesResult{RepoID: repoID, Results: out, Limit: req.Limit, Offset: req.Offset}, nil
}

func (s *Service) GetBacklinks(ctx context.Context, req GetBacklinksRequest) (BacklinksResult, error) {
	repoID, err := s.requireRepo(ctx, req.RepoID, "backlinks")
	if err != nil {
		return BacklinksResult{}, err
	}
	id, err := s.resolveScopedStableID(ctx, repoID, req.ID, req.AliasType, req.AliasID)
	if err != nil {
		return BacklinksResult{}, err
	}
	backlinks, err := s.store.GetBacklinksScoped(ctx, repoID, id)
	if err != nil {
		return BacklinksResult{}, normalizeError(err, "backlinks", id)
	}
	out := make([]BacklinkResult, 0, len(backlinks))
	for _, source := range backlinks {
		out = append(out, BacklinkResult{SourceSummary: sourceSummary(source), TargetID: id})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return BacklinksResult{RepoID: repoID, ID: id, Backlinks: out, Limit: req.Limit, Offset: req.Offset}, nil
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

func (s *Service) SyncStatus(ctx context.Context, req ListSourcesRequest) (SyncStatusSummaryResult, error) {
	repoID, err := s.requireRepo(ctx, req.RepoID, "sync-status")
	if err != nil {
		return SyncStatusSummaryResult{}, err
	}
	listed, err := s.ListSources(ctx, req)
	if err != nil {
		if IsCacheEmpty(err) {
			return SyncStatusSummaryResult{RepoID: repoID, CacheEmpty: true, Limit: req.Limit, Offset: req.Offset}, nil
		}
		return SyncStatusSummaryResult{}, err
	}
	completedEvents, err := s.store.ListCompletedSyncEventsScoped(ctx, repoID)
	if err != nil {
		return SyncStatusSummaryResult{}, err
	}
	sourceToLatestCompleted := map[string]cache.SyncEvent{}
	for _, event := range completedEvents {
		existing, ok := sourceToLatestCompleted[event.SourceID]
		if !ok || event.CompletedAt.After(existing.CompletedAt) {
			sourceToLatestCompleted[event.SourceID] = event
		}
	}
	result := SyncStatusSummaryResult{RepoID: repoID, Limit: req.Limit, Offset: req.Offset, Results: []SyncStatusResult{}}
	var latestCompleted cache.SyncEvent
	for _, source := range listed.Results {
		status, err := s.GetSyncStatus(ctx, SyncStatusRequest{RepoID: repoID, ID: source.ID})
		if err != nil {
			if IsNotFound(err) {
				result.Warnings = append(result.Warnings, "sync_status_missing:"+source.ID)
				result.StaleCount++
				continue
			}
			return SyncStatusSummaryResult{}, err
		}
		result.Results = append(result.Results, status)
		if status.Freshness == FreshnessFresh || status.Status == "fresh" {
			result.FreshCount++
		} else {
			result.StaleCount++
		}
		if status.LastFetchedAt.After(result.LastSyncAt) {
			result.LastSyncAt = status.LastFetchedAt.UTC()
		}
		if event, ok := sourceToLatestCompleted[source.ID]; ok && event.CompletedAt.After(latestCompleted.CompletedAt) {
			latestCompleted = event
		}
	}
	if !latestCompleted.StartedAt.IsZero() {
		result.LastSyncStartedAt = latestCompleted.StartedAt.UTC()
	}
	if !latestCompleted.CompletedAt.IsZero() {
		result.LastSyncCompletedAt = latestCompleted.CompletedAt.UTC()
	}
	result.ZeroDelta = latestCompleted.ZeroDelta
	result.CacheEmpty = len(result.Results) == 0 && len(result.Warnings) == 0
	return result, nil
}

func (s *Service) RecentChanges(ctx context.Context, req RecentChangesRequest) (RecentChangesResult, error) {
	repoID, err := s.requireRepo(ctx, req.RepoID, "recent")
	if err != nil {
		return RecentChangesResult{}, err
	}
	sources, err := s.store.ListSources(ctx, cache.SourceFilter{RepoID: repoID, Kind: req.Kind, Status: req.Status})
	if err != nil {
		return RecentChangesResult{}, normalizeError(err, "sources", "")
	}
	if len(sources) == 0 {
		return RecentChangesResult{}, ErrCacheEmpty{Message: "cache has no sources"}
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
	return RecentChangesResult{RepoID: repoID, Results: out, Limit: req.Limit, Offset: req.Offset}, nil
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
	snapshot, err := s.storedSnapshot(ctx, req)
	if err != nil {
		return ExportSnapshotResult{}, err
	}
	content, err := renderSnapshotContent(snapshot, format)
	if err != nil {
		return ExportSnapshotResult{}, err
	}
	hash := sha256.Sum256(content)
	result := ExportSnapshotResult{RepoID: snapshot.RepoID, SnapshotID: req.SnapshotID, Format: format, RecordCount: len(snapshot.Sources), GeneratedAt: snapshot.CreatedAt, ContentHash: hex.EncodeToString(hash[:]), InlineContent: string(content), Warnings: warningCodes(snapshot.Warnings)}
	if result.SnapshotID == "" {
		result.SnapshotID = snapshot.ManifestHash
		if len(result.SnapshotID) > 32 {
			result.SnapshotID = result.SnapshotID[:32]
		}
	}
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
	if req.BaseSnapshotID == "" || req.HeadSnapshotID == "" {
		if req.Base.Kind != "" || req.Head.Kind != "" || req.BaseContent != "" || req.BaseSnapshotContent != "" {
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
			if result.BaseSnapshotID == "" {
				result.BaseSnapshotID = req.Base.Path
			}
			if result.HeadSnapshotID == "" {
				result.HeadSnapshotID = req.Head.Path
			}
			result.Format = format

			baseBytes, _ := renderSnapshotContent(base, format)
			headBytes, _ := renderSnapshotContent(head, format)
			result.DiffText = simpleDiff(string(baseBytes), string(headBytes))
			return result, nil
		}
		current, err := s.createStoredSnapshot(ctx, req.RepoID, ExportSnapshotRequest{RepoID: req.RepoID, Format: format, IncludeBody: true})
		if err != nil {
			return DiffSnapshotResult{}, err
		}
		req.BaseSnapshotID = current.ID
		req.HeadSnapshotID = current.ID
	}
	base, err := s.storedSnapshot(ctx, ExportSnapshotRequest{RepoID: req.RepoID, SnapshotID: req.BaseSnapshotID, Format: format, IncludeBody: true})
	if err != nil {
		if IsNotFound(err) {
			return DiffSnapshotResult{}, ErrNotFound{Kind: "base_id", ID: req.BaseSnapshotID}
		}
		return DiffSnapshotResult{}, err
	}
	head, err := s.storedSnapshot(ctx, ExportSnapshotRequest{RepoID: req.RepoID, SnapshotID: req.HeadSnapshotID, Format: format, IncludeBody: true})
	if err != nil {
		if IsNotFound(err) {
			return DiffSnapshotResult{}, ErrNotFound{Kind: "head_id", ID: req.HeadSnapshotID}
		}
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
	stored, err := s.createStoredSnapshot(ctx, repoID, ExportSnapshotRequest{RepoID: repoID, Format: "json", IncludeBody: true})
	if err != nil {
		return OperationResult{}, err
	}
	return OperationResult{Command: "index", Status: "ok", ProcessedCount: processed, Evidence: "snapshot_id=" + stored.ID, GeneratedAt: s.now().UTC()}, nil
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
	syncStartedAt := s.now().UTC()
	inProgress := cache.SyncEvent{RepoID: repoID, ID: eventID, SourceID: s.syncEventSourceID(ctx, req, remoteType, remoteID), RemoteType: remoteType, RemoteID: remoteID, Status: "in_progress", IdempotencyKey: key, Message: "sync started", CreatedAt: syncStartedAt, StartedAt: syncStartedAt}
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
	if err := s.validateLiveSourceGraph(graph); err != nil {
		failure := s.normalizeSyncFailure(err, req, remoteType, remoteID)
		if inProgressRecorded {
			_ = s.store.RecordSyncEvent(ctx, failedSyncEvent(inProgress, failure, s.now().UTC()))
		}
		return SyncResult{}, failure
	}
	syncCompletedAt := s.now().UTC()
	revision := ""
	if graph.SyncStatus != nil {
		revision = graph.SyncStatus.RemoteRevision
	}
	zeroDelta := counts.Fetched > 0 && counts.Skipped == counts.Fetched && counts.Updated == 0 && counts.Inserted == 0 && counts.Conflicts == 0
	graph.SyncEvents = append(graph.SyncEvents, cache.SyncEvent{ID: eventID, SourceID: graph.Source.ID, RemoteType: remoteType, RemoteID: remoteID, RemoteRevision: revision, Status: "succeeded", IdempotencyKey: key, Message: syncEventMessage(counts), CreatedAt: syncCompletedAt, StartedAt: syncStartedAt, CompletedAt: syncCompletedAt, ZeroDelta: zeroDelta})
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
	return SyncResult{IdempotencyKey: key, Status: "succeeded", Counts: counts, SyncEventID: eventID, Freshness: string(FreshnessFresh), GeneratedAt: syncCompletedAt, StartedAt: syncStartedAt, CompletedAt: syncCompletedAt, ZeroDelta: zeroDelta}, nil
}

// SyncResources processes multiple SyncRequest values independently via SyncToCache.
// Each resource is synced atomically; failures do not short-circuit remaining resources.
// On any failure, the returned (*SyncResourcesResult, error) pair carries a PartialSyncError
// with structured per-resource failure details. Results always contains entries from
// successful SyncToCache calls; Failures contains entries from failed calls.
// Callers should check PartialSyncError before using the result.
func (s *Service) SyncResources(ctx context.Context, reqs []SyncRequest) (*SyncResourcesResult, error) {
	result := &SyncResourcesResult{
		Results:  make([]SyncResult, 0, len(reqs)),
		Failures: make([]ResourceError, 0),
	}
	var partial *PartialSyncError
	for i, req := range reqs {
		if err := ctx.Err(); err != nil {
			re := ResourceError{
				SourceID:   req.StableID,
				RemoteType: "",
				Err:        err,
				Message:    fmt.Sprintf("sync resources: context cancelled at request %d", i),
			}
			result.Failures = append(result.Failures, re)
			continue
		}
		syncResult, err := s.SyncToCache(ctx, req)
		if err != nil {
			remoteType := ""
			if req.AliasType != "" {
				remoteType = req.AliasType
			}
			sourceID := req.StableID
			if sourceID == "" {
				sourceID = req.RemoteAlias
			}
			re := ResourceError{
				SourceID:   sourceID,
				RemoteType: remoteType,
				Err:        err,
				Message:    err.Error(),
			}
			result.Failures = append(result.Failures, re)
			continue
		}
		result.Results = append(result.Results, syncResult)
	}
	result.SuccessCount = len(result.Results)
	result.FailureCount = len(result.Failures)
	if result.FailureCount > 0 {
		partial = &PartialSyncError{
			Errors:       result.Failures,
			SuccessCount: result.SuccessCount,
			FailureCount: result.FailureCount,
		}
	}
	if partial != nil {
		return result, partial
	}
	return result, nil
}

func (s *Service) BulkSyncIssues(ctx context.Context, req BulkSyncRequest) (*SyncResourcesResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repoID, err := s.requireRepo(ctx, req.RepoID, "bulk-sync-issues")
	if err != nil {
		return nil, err
	}
	req.RepoID = repoID
	if err := s.validateRepoScope(ctx, repoID, "issues"); err != nil {
		return nil, err
	}
	route, err := s.BuildAdapterRoute(ctx, repoID, RepositoryScopeIssues)
	if err != nil {
		return nil, err
	}
	page, err := s.client.ListIssues(ctx, gitcode.IssueListRequest{Owner: route.Owner, Repo: route.Name, Page: req.Page, PerPage: req.PerPage})
	if err != nil {
		return bulkSyncFailureResult(s.normalizeSyncFailure(err, SyncRequest{RepoID: req.RepoID, RemoteAlias: "issue:*"}, "issues", "*"), "issue:*", "issues")
	}
	reqs := make([]SyncRequest, 0, len(page.Items))
	for _, summary := range page.Items {
		reqs = append(reqs, SyncRequest{
			RepoID:         req.RepoID,
			AliasType:      "issue",
			AliasID:        strconv.Itoa(summary.Number),
			IdempotencyKey: scopedBulkSyncKey(req.IdempotencyKey, "issue", strconv.Itoa(summary.Number)),
			MaxAttempts:    req.MaxAttempts,
			MaxSize:        req.MaxSize,
		})
	}
	return s.SyncResources(ctx, reqs)
}

func (s *Service) BulkSyncWiki(ctx context.Context, req BulkSyncRequest) (*SyncResourcesResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repoID, err := s.requireRepo(ctx, req.RepoID, "bulk-sync-wiki")
	if err != nil {
		return nil, err
	}
	req.RepoID = repoID
	if err := s.validateRepoScope(ctx, repoID, "wiki"); err != nil {
		return nil, err
	}
	route, err := s.BuildAdapterRoute(ctx, repoID, RepositoryScopeWiki)
	if err != nil {
		return nil, err
	}
	page, err := s.client.ListWikiPages(ctx, gitcode.WikiListRequest{Owner: route.Owner, Repo: route.Name, Page: req.Page, PerPage: req.PerPage})
	if err != nil {
		return bulkSyncFailureResult(s.normalizeSyncFailure(err, SyncRequest{RepoID: req.RepoID, RemoteAlias: "wiki:*"}, "wiki", "*"), "wiki:*", "wiki")
	}
	reqs := make([]SyncRequest, 0, len(page.Items))
	for _, wp := range page.Items {
		reqs = append(reqs, SyncRequest{
			RepoID:         req.RepoID,
			AliasType:      "wiki",
			AliasID:        wp.Slug,
			IdempotencyKey: scopedBulkSyncKey(req.IdempotencyKey, "wiki", wp.Slug),
			MaxAttempts:    req.MaxAttempts,
			MaxSize:        req.MaxSize,
		})
	}
	return s.SyncResources(ctx, reqs)
}

func (s *Service) BulkSyncAll(ctx context.Context, req BulkSyncRequest) (*SyncResourcesResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repoID, err := s.requireRepo(ctx, req.RepoID, "bulk-sync-all")
	if err != nil {
		return nil, err
	}
	req.RepoID = repoID
	var issuesResult, wikiResult *SyncResourcesResult
	var issuesErr, wikiErr error

	if err := s.validateRepoScope(ctx, repoID, "issues"); err == nil {
		issuesResult, issuesErr = s.BulkSyncIssues(ctx, req)
	}

	if err := s.validateRepoScope(ctx, repoID, "wiki"); err == nil {
		wikiResult, wikiErr = s.BulkSyncWiki(ctx, req)
	}

	aggregated := &SyncResourcesResult{
		Results:  make([]SyncResult, 0),
		Failures: make([]ResourceError, 0),
	}
	if issuesResult != nil {
		aggregated.Results = append(aggregated.Results, issuesResult.Results...)
		aggregated.Failures = append(aggregated.Failures, issuesResult.Failures...)
	}
	if wikiResult != nil {
		aggregated.Results = append(aggregated.Results, wikiResult.Results...)
		aggregated.Failures = append(aggregated.Failures, wikiResult.Failures...)
	}
	aggregated.SuccessCount = len(aggregated.Results)
	aggregated.FailureCount = len(aggregated.Failures)

	if issuesErr != nil || wikiErr != nil {
		return aggregated, &PartialSyncError{
			Errors:       aggregated.Failures,
			SuccessCount: aggregated.SuccessCount,
			FailureCount: aggregated.FailureCount,
		}
	}
	if aggregated.FailureCount > 0 {
		return aggregated, &PartialSyncError{
			Errors:       aggregated.Failures,
			SuccessCount: aggregated.SuccessCount,
			FailureCount: aggregated.FailureCount,
		}
	}
	return aggregated, nil
}

func bulkSyncFailureResult(err error, sourceID, remoteType string) (*SyncResourcesResult, error) {
	re := ResourceError{SourceID: sourceID, RemoteType: remoteType, Err: err, Message: err.Error()}
	result := &SyncResourcesResult{Failures: []ResourceError{re}, FailureCount: 1}
	return result, &PartialSyncError{Errors: result.Failures, FailureCount: 1}
}

func scopedBulkSyncKey(base, scope, id string) string {
	if strings.TrimSpace(base) == "" {
		return ""
	}
	return base + "-" + scope + "-" + id
}

func (s *Service) CreateIssue(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if strings.TrimSpace(req.Title) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "title", Message: "title is required"}
	}
	return s.executeWrite(ctx, "create-issue", req, RepositoryScopeIssues)
}

func (s *Service) UpdateIssue(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if req.Number == 0 && strings.TrimSpace(req.ID) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "issue", Message: "number or id is required"}
	}
	return s.executeWrite(ctx, "update-issue", req, RepositoryScopeIssues)
}

func (s *Service) CreatePage(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Body) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "page", Message: "title and body are required"}
	}
	return s.executeWrite(ctx, "create-page", req, RepositoryScopeWiki)
}

func (s *Service) UpdatePage(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if strings.TrimSpace(req.Slug) == "" && strings.TrimSpace(req.ID) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "page", Message: "slug or id is required"}
	}
	return s.executeWrite(ctx, "update-page", req, RepositoryScopeWiki)
}

func (s *Service) AddComment(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if (req.Number == 0 && strings.TrimSpace(req.ID) == "") || strings.TrimSpace(req.Body) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "comment", Message: "issue and body are required"}
	}
	return s.executeWrite(ctx, "add-comment", req, RepositoryScopeIssues)
}

func (s *Service) AddLabel(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if (req.Number == 0 && strings.TrimSpace(req.ID) == "") || strings.TrimSpace(req.Label) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "label", Message: "issue and label are required"}
	}
	return s.executeWrite(ctx, "add-label", req, RepositoryScopeIssues)
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
		route, err := s.BuildAdapterRoute(ctx, req.RepoID, RepositoryScopeIssues)
		if err != nil {
			return cache.SourceGraph{}, SyncCounts{}, err
		}
		number, err := strconv.Atoi(remoteID)
		if err != nil {
			return cache.SourceGraph{}, SyncCounts{}, ErrInvalidQuery{Field: "remote_id", Message: "issue remote id must be numeric"}
		}
		issue, err := s.client.GetIssue(ctx, gitcode.IssueRequest{Owner: route.Owner, Repo: route.Name, Number: number, KnownRemoteAlias: true, RemoteAlias: remoteID})
		if err != nil {
			return cache.SourceGraph{}, SyncCounts{}, err
		}
		return s.stageIssue(ctx, req, remoteType, remoteID, issue)
	case "wiki", "page", "remote":
		route, err := s.BuildAdapterRoute(ctx, req.RepoID, RepositoryScopeWiki)
		if err != nil {
			return cache.SourceGraph{}, SyncCounts{}, err
		}
		page, err := s.client.GetWikiPage(ctx, gitcode.WikiPageRequest{Owner: route.Owner, Repo: route.Name, Slug: remoteID})
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
	providerID := strings.TrimSpace(issue.ID)
	if s.syncProviderMode() == gitcode.ProviderModeLive && providerID == "" {
		return cache.SourceGraph{}, SyncCounts{}, s.liveGraphError("issue missing provider id")
	}
	stableID := req.StableID
	if stableID == "" {
		stableID = s.resolveOrFallback(ctx, req.RepoID, remoteType, remoteID, liveFallbackSourceID(s.syncProviderMode(), remoteType, remoteID, providerID))
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
	if providerID != "" && providerID != remoteID {
		graph.Identities = append(graph.Identities, cache.Identity{RepoID: req.RepoID, SourceID: stableID, AliasType: "gitcode_issue_id", Alias: providerID, Remote: cache.RemoteAlias{Type: "gitcode_issue_id", ID: providerID}})
	}
	route, err := s.BuildAdapterRoute(ctx, req.RepoID, RepositoryScopeIssues)
	if err != nil {
		return cache.SourceGraph{}, SyncCounts{}, err
	}
	comments, err := s.client.ListIssueComments(ctx, gitcode.IssueRequest{Owner: route.Owner, Repo: route.Name, Number: issue.Number, KnownRemoteAlias: true, RemoteAlias: remoteID})
	if err != nil {
		return cache.SourceGraph{}, SyncCounts{}, err
	}
	for _, comment := range comments.Items {
		commentID := strings.TrimSpace(comment.ID)
		if s.syncProviderMode() == gitcode.ProviderModeLive {
			if commentID == "" {
				return cache.SourceGraph{}, SyncCounts{}, s.liveGraphError("comment missing provider id")
			}
			if !s.liveCommentParentReconciles(comment, remoteID, providerID) {
				return cache.SourceGraph{}, SyncCounts{}, s.liveGraphError("comment parent issue id is unreconciled")
			}
		}
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
	providerID := strings.TrimSpace(page.ID)
	if s.syncProviderMode() == gitcode.ProviderModeLive && providerID == "" {
		return cache.SourceGraph{}, SyncCounts{}, s.liveGraphError("wiki missing provider id")
	}
	stableID := req.StableID
	if stableID == "" {
		stableID = s.resolveOrFallback(ctx, req.RepoID, remoteType, remoteID, liveFallbackSourceID(s.syncProviderMode(), remoteType, remoteID, providerID))
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
	if providerID != "" && providerID != remoteID {
		graph.Identities = append(graph.Identities, cache.Identity{RepoID: req.RepoID, SourceID: stableID, AliasType: "gitcode_wiki_id", Alias: providerID, Remote: cache.RemoteAlias{Type: "gitcode_wiki_id", ID: providerID}})
	}
	graph.Chunks = chunksForSource(graph.Source)
	return graph, counts, nil
}

func (s *Service) BuildAdapterRoute(ctx context.Context, repoID string, requestedScope RepositoryScope) (RepositoryRoute, error) {
	repoID, err := s.requireRepo(ctx, repoID, "route")
	if err != nil {
		return RepositoryRoute{}, err
	}
	repo, err := s.repositoryWithScope(ctx, repoID, requestedScope)
	if err != nil {
		return RepositoryRoute{}, err
	}
	route := RepositoryRoute{RepoID: repo.RepoID, Owner: repo.Owner, Name: repo.Name, APIBaseURL: repo.APIBaseURL}
	for _, configured := range repo.Scopes {
		route.Scopes = append(route.Scopes, RepositoryScope(configured))
	}
	return route, nil
}

func (s *Service) ResolveLiveRepositoryBinding(ctx context.Context, req LiveRepositoryBindingRequest) (LiveRepositoryBinding, error) {
	repoID, err := s.requireRepo(ctx, req.RepoID, "live repository binding")
	if err != nil {
		return LiveRepositoryBinding{}, err
	}
	repo, err := s.repositoryWithScope(ctx, repoID, req.RequestedScope)
	if err != nil {
		return LiveRepositoryBinding{}, err
	}
	selected := strings.TrimSpace(repo.APIBaseURL)
	if selected == "" {
		return LiveRepositoryBinding{}, ErrInvalidQuery{Field: "api_base_url", Message: "live repository binding requires api_base_url"}
	}
	baseURL, err := normalizeLiveAPIBaseURL(selected)
	if err != nil {
		return LiveRepositoryBinding{}, err
	}
	binding := LiveRepositoryBinding{RepoID: repo.RepoID, Owner: repo.Owner, Name: repo.Name, APIBaseURL: baseURL, CachePath: strings.TrimSpace(req.CachePath), AuditPath: strings.TrimSpace(req.AuditPath), BaseURLSource: "repository_binding"}
	for _, configured := range repo.Scopes {
		binding.Scopes = append(binding.Scopes, RepositoryScope(configured))
	}
	return binding, nil
}

func (s *Service) repositoryWithScope(ctx context.Context, repoID string, requestedScope RepositoryScope) (cache.RepositoryBinding, error) {
	repo, err := s.store.GetRepository(ctx, repoID)
	if err != nil {
		return cache.RepositoryBinding{}, normalizeError(err, "repository", repoID)
	}
	for _, scope := range repo.Scopes {
		if RepositoryScope(scope) == requestedScope {
			return repo, nil
		}
	}
	return cache.RepositoryBinding{}, ErrInvalidQuery{Field: "scope", Message: string(requestedScope) + " scope is not enabled for repo " + repoID}
}

func normalizeLiveAPIBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", ErrInvalidQuery{Field: "api_base_url", Message: "valid absolute http(s) api_base_url is required for live mode"}
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", ErrInvalidQuery{Field: "api_base_url", Message: "api_base_url must use http or https for live mode"}
	}
	if parsed.User != nil {
		return "", ErrInvalidQuery{Field: "api_base_url", Message: "api_base_url must not contain credentials"}
	}
	return sanitizeAPIBaseURL(parsed.String()), nil
}

func (s *Service) validateRepoScope(ctx context.Context, repoID, remoteType string) error {
	want := RepositoryScopeIssues
	if remoteType == "wiki" || remoteType == "page" || remoteType == "remote" {
		want = RepositoryScopeWiki
	}
	_, err := s.BuildAdapterRoute(ctx, repoID, want)
	return err
}

func (s *Service) syncProviderMode() gitcode.ProviderMode {
	return s.ProviderMode()
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

func liveFallbackSourceID(mode gitcode.ProviderMode, remoteType, remoteID, providerID string) string {
	if mode == gitcode.ProviderModeLive && strings.TrimSpace(providerID) != "" {
		return fallbackSourceID(remoteType, providerID)
	}
	return fallbackSourceID(remoteType, remoteID)
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

func (s *Service) validateLiveSourceGraph(graph cache.SourceGraph) error {
	if s.syncProviderMode() != gitcode.ProviderModeLive {
		return nil
	}
	if gitcode.IsFixtureBoundary(s.client) {
		return s.liveGraphError("fixture provider is forbidden in live graph")
	}
	for _, marker := range gitcode.FixtureMarkerIDs() {
		if graph.Source.ID == marker {
			return s.liveGraphError("fixture marker " + marker + " is forbidden in live graph")
		}
		if graph.SyncStatus != nil && graph.SyncStatus.RemoteID == marker {
			return s.liveGraphError("fixture remote marker " + marker + " is forbidden in live graph")
		}
		for _, identity := range graph.Identities {
			if identity.SourceID == marker || identity.Alias == marker || identity.Remote.ID == marker {
				return s.liveGraphError("fixture identity marker " + marker + " is forbidden in live graph")
			}
		}
		for _, comment := range graph.Comments {
			if comment.RecordID == marker || comment.CommentID == marker {
				return s.liveGraphError("fixture comment marker " + marker + " is forbidden in live graph")
			}
		}
	}
	if graph.Source.ID == "" {
		return s.liveGraphError("source id is required")
	}
	if graph.SyncStatus == nil || strings.TrimSpace(graph.SyncStatus.RemoteID) == "" {
		return s.liveGraphError("remote id is required")
	}
	for _, comment := range graph.Comments {
		if strings.TrimSpace(comment.CommentID) == "" {
			return s.liveGraphError("comment provider id is required")
		}
		if comment.RecordID != graph.Source.ID {
			return s.liveGraphError("comment parent record is unreconciled")
		}
	}
	return nil
}

func (s *Service) liveCommentParentReconciles(comment gitcode.Comment, remoteID, providerID string) bool {
	parent := strings.TrimSpace(comment.IssueID)
	return parent == "" || parent == strings.TrimSpace(remoteID) || parent == strings.TrimSpace(providerID)
}

func (s *Service) liveGraphError(message string) error {
	return ErrSyncFailure{Mode: "live_graph_invalid", Cause: ErrInvalidQuery{Field: "live_graph", Message: message}}
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
	return SyncResult{IdempotencyKey: event.IdempotencyKey, Status: event.Status, Counts: counts, SyncEventID: event.ID, Freshness: freshness, GeneratedAt: generated, StartedAt: event.StartedAt, CompletedAt: event.CompletedAt, ZeroDelta: event.ZeroDelta}
}

func failedSyncEvent(event cache.SyncEvent, cause error, at time.Time) cache.SyncEvent {
	event.Status = "failed"
	event.Message = cause.Error()
	event.CreatedAt = at
	event.CompletedAt = at
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
		mode := "auth_expired"
		if s.syncProviderMode() == gitcode.ProviderModeLive && (auth.Status == 401 || auth.Status == 403) {
			mode = "live_auth_failure"
		}
		return ErrSyncFailure{Mode: mode, Target: target, Endpoint: auth.Endpoint, RecoveryAction: "renew GITCODE_TOKEN and retry sync", Cause: err}
	}
	var forbidden gitcode.ErrForbidden
	if errors.As(err, &forbidden) {
		if s.syncProviderMode() == gitcode.ProviderModeLive && (forbidden.Status == 401 || forbidden.Status == 403) {
			return ErrSyncFailure{Mode: "live_auth_failure", Target: target, Endpoint: forbidden.Endpoint, RecoveryAction: "renew GITCODE_TOKEN and retry sync", Cause: err}
		}
	}
	var alreadySync ErrSyncFailure
	if errors.As(err, &alreadySync) {
		return err
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

func (s *Service) executeWrite(ctx context.Context, command string, req WriteCommandRequest, scope RepositoryScope) (WriteCommandResult, error) {
	if err := ctx.Err(); err != nil {
		return WriteCommandResult{}, err
	}
	repoID := firstNonEmptyString(req.RepoID, req.Repo)
	route, err := s.BuildAdapterRoute(ctx, repoID, scope)
	if err != nil {
		return WriteCommandResult{}, err
	}
	req.RepoID = route.RepoID
	req.Repo = route.RepoID
	if req.Mode != WriteModeDryRun && req.Mode != WriteModeLive {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "write_mode", Message: "exactly one of dry_run or live is required"}
	}
	key, fingerprint := writeIdempotency(command, req)
	base := WriteCommandResult{Command: command, RepoID: route.RepoID, Status: "dry_run_valid", ID: writeTargetID(req), IdempotencyKey: key, SourceFingerprint: fingerprint, Evidence: "validated write command", GeneratedAt: s.now().UTC()}
	if req.Mode == WriteModeDryRun {
		return base, nil
	}
	if !s.hasWriteCredential() {
		return WriteCommandResult{}, ErrWriteFailure{Code: "write_missing_credential", RepoID: route.RepoID, IdempotencyKey: key}
	}
	lookup, err := audit.LookupIdempotency(ctx, s.store, route.RepoID, key, fingerprint)
	if err != nil {
		return WriteCommandResult{}, err
	}
	if lookup.Entry != nil {
		prior := *lookup.Entry
		if lookup.Conflict {
			return WriteCommandResult{}, ErrWriteFailure{Code: "write_idempotency_conflict", RepoID: route.RepoID, RemoteID: prior.RemoteID, IdempotencyKey: key}
		}
		if lookup.Replay {
			return replayWriteResult(command, prior, fingerprint, s.now().UTC()), nil
		}
		if lookup.Partial {
			graph, err := s.replayWriteGraph(ctx, command, route.RepoID, req, prior)
			if err != nil {
				return WriteCommandResult{}, err
			}
			if err := s.store.UpsertRecordGraph(ctx, graph); err != nil {
				_ = s.store.RecordAuditEvent(ctx, audit.RemoteConfirmedCacheRefreshFailed(route.RepoID, key, command, prior.RecordID, prior.RemoteType, prior.RemoteID, fingerprint, err.Error(), s.now().UTC()))
				return WriteCommandResult{}, ErrWriteFailure{Code: "write_partial_cache_refresh_failed", RepoID: route.RepoID, RemoteID: prior.RemoteID, IdempotencyKey: key, Cause: err}
			}
			if err := s.recordCacheConfirmation(ctx, command, route.RepoID, key, fingerprint, graph, prior.RemoteID, "succeeded", s.now().UTC()); err != nil {
				return WriteCommandResult{}, ErrWriteFailure{Code: "write_partial_cache_refresh_failed", RepoID: route.RepoID, RemoteID: prior.RemoteID, IdempotencyKey: key, Cause: err}
			}
			completed := audit.Success(route.RepoID, key, command, graph.Record.ID, graph.Record.RemoteType, prior.RemoteID, fingerprint, "cache refresh replay completed", s.now().UTC())
			if err := s.store.RecordAuditEvent(ctx, completed); err != nil {
				return WriteCommandResult{}, ErrWriteFailure{Code: "write_partial_remote_confirmed_audit_failed", RepoID: route.RepoID, RemoteID: prior.RemoteID, IdempotencyKey: key, Cause: err}
			}
			result := replayWriteResult(command, completed, fingerprint, s.now().UTC())
			result.Status = "succeeded"
			return result, nil
		}
		if prior.Status == audit.StatusRemoteConfirmedAuditFailed {
			return WriteCommandResult{}, ErrWriteFailure{Code: "write_partial_remote_confirmed_audit_failed", RepoID: route.RepoID, RemoteID: prior.RemoteID, IdempotencyKey: key}
		}
	}
	confirmed, graph, err := s.callWriteAdapter(ctx, command, route, req, key)
	if err != nil {
		code := s.writeAdapterErrorCode(req.Mode, err)
		_ = s.store.RecordAuditEvent(ctx, audit.Failure(route.RepoID, key, command, fingerprint, code, s.now().UTC()))
		return WriteCommandResult{}, ErrWriteFailure{Code: code, RepoID: route.RepoID, IdempotencyKey: key, Cause: writeFailureCause(code, err)}
	}
	if !confirmed.confirmed || confirmed.remoteID == "" {
		_ = s.store.RecordAuditEvent(ctx, audit.Failure(route.RepoID, key, command, fingerprint, "write_unconfirmed_remote", s.now().UTC()))
		return WriteCommandResult{}, ErrWriteFailure{Code: "write_unconfirmed_remote", RepoID: route.RepoID, IdempotencyKey: key}
	}
	auditEntry := audit.Success(route.RepoID, key, command, graph.Record.ID, graph.Record.RemoteType, confirmed.remoteID, fingerprint, confirmed.message, confirmed.completedAt)
	if command == "create-issue" {
		entry, err := audit.LiveCreateIssueConfirmation(audit.ConfirmationInput{RepoID: route.RepoID, Key: key, Command: command, Mode: string(req.Mode), RecordID: graph.Record.ID, RemoteType: graph.Record.RemoteType, RemoteID: confirmed.remoteID, PayloadHash: fingerprint, Message: confirmed.message, RequestMetadata: writeAuditMetadata(command, key, fingerprint, graph.Record.RemoteType, confirmed), CreatedAt: confirmed.completedAt})
		if err != nil {
			_ = s.store.RecordAuditEvent(ctx, audit.Failure(route.RepoID, key, command, fingerprint, "write_unconfirmed_remote", s.now().UTC()))
			return WriteCommandResult{}, ErrWriteFailure{Code: "write_unconfirmed_remote", RepoID: route.RepoID, IdempotencyKey: key}
		}
		auditEntry = entry
	}
	if err := s.store.RecordAuditEvent(ctx, auditEntry); err != nil {
		_ = s.store.RecordAuditEvent(ctx, audit.RemoteConfirmedAuditFailed(route.RepoID, key, command, graph.Record.ID, graph.Record.RemoteType, confirmed.remoteID, fingerprint, err.Error(), s.now().UTC()))
		return WriteCommandResult{}, ErrWriteFailure{Code: "write_partial_remote_confirmed_audit_failed", RepoID: route.RepoID, RemoteID: confirmed.remoteID, IdempotencyKey: key, Cause: err}
	}
	if err := s.store.UpsertRecordGraph(ctx, graph); err != nil {
		_ = s.store.RecordAuditEvent(ctx, audit.RemoteConfirmedCacheRefreshFailed(route.RepoID, key, command, graph.Record.ID, graph.Record.RemoteType, confirmed.remoteID, fingerprint, err.Error(), s.now().UTC()))
		return WriteCommandResult{}, ErrWriteFailure{Code: "write_partial_cache_refresh_failed", RepoID: route.RepoID, RemoteID: confirmed.remoteID, IdempotencyKey: key, Cause: err}
	}
	if err := s.recordCacheConfirmation(ctx, command, route.RepoID, key, fingerprint, graph, confirmed.remoteID, "succeeded", confirmed.completedAt); err != nil {
		_ = s.store.RecordAuditEvent(ctx, audit.RemoteConfirmedCacheRefreshFailed(route.RepoID, key, command, graph.Record.ID, graph.Record.RemoteType, confirmed.remoteID, fingerprint, err.Error(), s.now().UTC()))
		return WriteCommandResult{}, ErrWriteFailure{Code: "write_partial_cache_refresh_failed", RepoID: route.RepoID, RemoteID: confirmed.remoteID, IdempotencyKey: key, Cause: err}
	}
	base.Status = "succeeded"
	base.ID = graph.Record.ID
	base.RemoteID = confirmed.remoteID
	base.RemoteNumber = confirmed.remoteNumber
	base.RemoteSlug = confirmed.remoteSlug
	base.RemoteRevision = confirmed.remoteRevision
	base.Evidence = "adapter-confirmed write with audit and cache refresh"
	return base, nil
}

type writeConfirmation struct {
	confirmed      bool
	remoteID       string
	remoteNumber   int
	remoteSlug     string
	remoteRevision string
	message        string
	completedAt    time.Time
}

func writeAuditMetadata(command, key, fingerprint, remoteType string, confirmed writeConfirmation) map[string]string {
	metadata := map[string]string{
		"method":             "POST",
		"idempotency_key":    key,
		"remote_type":        remoteType,
		"provider_mode":      string(gitcode.ProviderModeLive),
		"source_fingerprint": fingerprint,
	}
	if confirmed.remoteID != "" {
		metadata["remote_alias"] = confirmed.remoteID
	}
	if confirmed.remoteNumber > 0 {
		metadata["remote_number"] = strconv.Itoa(confirmed.remoteNumber)
	}
	if command != "" {
		metadata["provider"] = "gitcode-http"
	}
	return metadata
}

func (s *Service) recordCacheConfirmation(ctx context.Context, command, repoID, key, fingerprint string, graph cache.RecordGraph, remoteID, status string, createdAt time.Time) error {
	if command != "create-issue" {
		return nil
	}
	return s.store.RecordCacheConfirmation(ctx, cache.CacheConfirmationRecord{RepoID: repoID, Command: command, RecordID: graph.Record.ID, RecordType: graph.Record.Type, RemoteType: graph.Record.RemoteType, RemoteID: firstNonEmptyString(remoteID, graph.Record.RemoteID), IdempotencyKey: key, Status: status, SourceFingerprint: fingerprint, CreatedAt: createdAt})
}

func (s *Service) hasWriteCredential() bool {
	if s.providerMode == gitcode.ProviderModeLive {
		return s.writeCredentialPresent
	}
	return strings.TrimSpace(os.Getenv("GITCODE_TOKEN")) != ""
}

func (s *Service) callWriteAdapter(ctx context.Context, command string, route RepositoryRoute, req WriteCommandRequest, key string) (writeConfirmation, cache.RecordGraph, error) {
	opts := gitcode.WriteOptions{IdempotencyKey: key}
	now := s.now().UTC()
	switch command {
	case "create-issue":
		result, err := s.client.CreateIssue(ctx, gitcode.CreateIssueRequest{Owner: route.Owner, Repo: route.Name, Title: strings.TrimSpace(req.Title), Body: req.Body, Labels: req.Labels}, opts)
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		confirmation, graph := s.issueWriteGraph(route.RepoID, result.Record, result, now)
		return confirmation, graph, nil
	case "update-issue":
		result, err := s.client.UpdateIssue(ctx, gitcode.UpdateIssueRequest{Owner: route.Owner, Repo: route.Name, Number: req.Number, Title: req.Title, Body: req.Body, State: req.State, Labels: req.Labels}, opts)
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		confirmation, graph := s.issueWriteGraph(route.RepoID, result.Record, result, now)
		return confirmation, graph, nil
	case "add-comment":
		result, err := s.client.CreateIssueComment(ctx, gitcode.CreateIssueCommentRequest{Owner: route.Owner, Repo: route.Name, Number: req.Number, Body: req.Body}, opts)
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		return s.commentWriteGraph(ctx, route.RepoID, req.Number, result.Record, result, now)
	case "add-label":
		result, err := s.client.AddLabel(ctx, gitcode.LabelRequest{Owner: route.Owner, Repo: route.Name, Number: req.Number, Label: strings.TrimSpace(req.Label)}, opts)
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		confirmation, graph := s.issueWriteGraph(route.RepoID, result.Record, result, now)
		return confirmation, graph, nil
	case "create-page":
		result, err := s.client.CreateWikiPage(ctx, gitcode.CreateWikiPageRequest{Owner: route.Owner, Repo: route.Name, Slug: req.Slug, Title: strings.TrimSpace(req.Title), Body: req.Body}, opts)
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		confirmation, graph := s.wikiWriteGraph(route.RepoID, result.Record, result, now)
		return confirmation, graph, nil
	case "update-page":
		result, err := s.client.UpdateWikiPage(ctx, gitcode.UpdateWikiPageRequest{Owner: route.Owner, Repo: route.Name, Slug: firstNonEmptyString(req.Slug, req.ID), Title: req.Title, Body: req.Body}, opts)
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		confirmation, graph := s.wikiWriteGraph(route.RepoID, result.Record, result, now)
		return confirmation, graph, nil
	default:
		return writeConfirmation{}, cache.RecordGraph{}, ErrWriteFailure{Code: "write_unsupported_deferred", RepoID: route.RepoID}
	}
}

func (s *Service) replayWriteGraph(ctx context.Context, command string, repoID string, req WriteCommandRequest, prior cache.AuditTrailEntry) (cache.RecordGraph, error) {
	now := s.now().UTC()
	switch command {
	case "create-issue", "update-issue", "add-label":
		number, _ := strconv.Atoi(prior.RemoteID)
		issue := gitcode.Issue{ID: prior.RemoteID, Number: number, Title: strings.TrimSpace(req.Title), Body: req.Body, State: firstNonEmptyString(req.State, "open"), CreatedAt: now, UpdatedAt: now}
		if issue.Title == "" {
			issue.Title = "Issue " + prior.RemoteID
		}
		if command == "add-label" && strings.TrimSpace(req.Label) != "" {
			issue.Labels = append(issue.Labels, strings.TrimSpace(req.Label))
		}
		result := gitcode.WriteResult[gitcode.Issue]{Record: issue, Confirmed: true, RemoteID: prior.RemoteID, RemoteNumber: number, RemoteRevision: firstNonEmptyString(prior.Message, prior.PayloadHash), ConfirmedAt: now}
		_, graph := s.issueWriteGraph(repoID, issue, result, now)
		return graph, nil
	case "add-comment":
		number := req.Number
		if number == 0 {
			number, _ = strconv.Atoi(prior.RecordID)
		}
		comment := gitcode.Comment{ID: prior.RemoteID, Body: req.Body, CreatedAt: now, UpdatedAt: now}
		result := gitcode.WriteResult[gitcode.Comment]{Record: comment, Confirmed: true, RemoteID: prior.RemoteID, ParentIssueNumber: number, ParentIssueID: prior.RecordID, RemoteRevision: firstNonEmptyString(prior.Message, prior.PayloadHash), ConfirmedAt: now}
		_, graph, err := s.commentWriteGraph(ctx, repoID, number, comment, result, now)
		return graph, err
	case "create-page", "update-page":
		page := gitcode.WikiPage{ID: prior.RemoteID, Slug: firstNonEmptyString(req.Slug, req.ID, prior.RemoteID), Title: req.Title, Body: req.Body, Revision: firstNonEmptyString(prior.Message, prior.PayloadHash), CreatedAt: now, UpdatedAt: now}
		if page.Title == "" {
			page.Title = page.Slug
		}
		result := gitcode.WriteResult[gitcode.WikiPage]{Record: page, Confirmed: true, RemoteID: prior.RemoteID, RemoteSlug: page.Slug, RemoteRevision: page.Revision, ConfirmedAt: now}
		_, graph := s.wikiWriteGraph(repoID, page, result, now)
		return graph, nil
	default:
		return cache.RecordGraph{}, ErrWriteFailure{Code: "write_unsupported_deferred", RepoID: repoID, RemoteID: prior.RemoteID, IdempotencyKey: prior.IdempotencyKey}
	}
}

func (s *Service) issueWriteGraph(repoID string, issue gitcode.Issue, result gitcode.WriteResult[gitcode.Issue], now time.Time) (writeConfirmation, cache.RecordGraph) {
	remoteID := firstNonEmptyString(result.RemoteID, issue.ID, strconv.Itoa(firstNonZeroInt(result.RemoteNumber, issue.Number)))
	issue.Number = firstNonZeroInt(issue.Number, result.RemoteNumber)
	stableID := fallbackSourceID("issue", remoteID)
	status := firstNonEmptyString(issue.State, issue.Status, "open")
	updated := issue.UpdatedAt.UTC()
	if updated.IsZero() {
		updated = now
	}
	created := issue.CreatedAt.UTC()
	if created.IsZero() {
		created = updated
	}
	revision := firstNonEmptyString(result.RemoteRevision, result.ResponseHash, contentHash(issue.Title, issue.Body, status, issue.Labels))
	record := cache.Record{RepoID: repoID, ID: stableID, Type: "issue", Path: "issues/" + remoteID + ".md", Title: issue.Title, Body: issue.Body, Status: status, Labels: issue.Labels, ContentHash: contentHash(issue.Title, issue.Body, status, issue.Labels), Provenance: cache.ProvenanceRemote, RemoteType: "issue", RemoteID: remoteID, RemoteRevision: revision, CreatedAt: created, UpdatedAt: updated}
	graph := cache.RecordGraph{Record: record, Identities: []cache.Identity{{RepoID: repoID, SourceID: stableID, AliasType: "issue", Alias: remoteID, Remote: cache.RemoteAlias{Type: "issue", ID: remoteID}}}, RemoteRevisions: []cache.RemoteRevision{{RepoID: repoID, RecordID: stableID, RemoteType: "issue", RemoteID: remoteID, RemoteRevision: revision, Status: "fresh", LastFetchedAt: now}}}
	return writeConfirmation{confirmed: result.Confirmed, remoteID: remoteID, remoteNumber: issue.Number, remoteRevision: revision, message: result.Operation, completedAt: firstNonZeroTime(result.ConfirmedAt, now)}, graph
}

func (s *Service) wikiWriteGraph(repoID string, page gitcode.WikiPage, result gitcode.WriteResult[gitcode.WikiPage], now time.Time) (writeConfirmation, cache.RecordGraph) {
	remoteID := firstNonEmptyString(result.RemoteSlug, page.Slug, result.RemoteID, page.ID)
	stableID := fallbackSourceID("wiki", remoteID)
	updated := page.UpdatedAt.UTC()
	if updated.IsZero() {
		updated = now
	}
	created := page.CreatedAt.UTC()
	if created.IsZero() {
		created = updated
	}
	revision := firstNonEmptyString(result.RemoteRevision, page.Revision, result.ResponseHash, contentHash(page.Title, page.Body))
	record := cache.Record{RepoID: repoID, ID: stableID, Type: "wiki", Path: "wiki/" + remoteID + ".md", Title: page.Title, Body: page.Body, Status: "fresh", ContentHash: contentHash(page.Title, page.Body, revision), Provenance: cache.ProvenanceRemote, RemoteType: "wiki", RemoteID: remoteID, RemoteRevision: revision, CreatedAt: created, UpdatedAt: updated}
	graph := cache.RecordGraph{Record: record, Identities: []cache.Identity{{RepoID: repoID, SourceID: stableID, AliasType: "wiki", Alias: remoteID, Remote: cache.RemoteAlias{Type: "wiki", ID: remoteID}}}, RemoteRevisions: []cache.RemoteRevision{{RepoID: repoID, RecordID: stableID, RemoteType: "wiki", RemoteID: remoteID, RemoteRevision: revision, Status: "fresh", LastFetchedAt: now}}}
	return writeConfirmation{confirmed: result.Confirmed, remoteID: remoteID, remoteSlug: remoteID, remoteRevision: revision, message: result.Operation, completedAt: firstNonZeroTime(result.ConfirmedAt, now)}, graph
}

func (s *Service) commentWriteGraph(ctx context.Context, repoID string, number int, comment gitcode.Comment, result gitcode.WriteResult[gitcode.Comment], now time.Time) (writeConfirmation, cache.RecordGraph, error) {
	remoteID := firstNonEmptyString(result.ParentIssueID, comment.IssueID, strconv.Itoa(firstNonZeroInt(result.ParentIssueNumber, number)))
	stableID := s.resolveOrFallback(ctx, repoID, "issue", remoteID, fallbackSourceID("issue", remoteID))
	record, err := s.store.GetRecord(ctx, repoID, stableID)
	if err != nil {
		record = cache.Record{RepoID: repoID, ID: stableID, Type: "issue", Path: "issues/" + remoteID + ".md", Title: "Issue " + remoteID, Status: "open", ContentHash: contentHash(remoteID), Provenance: cache.ProvenanceRemote, RemoteType: "issue", RemoteID: remoteID, CreatedAt: now, UpdatedAt: now}
	}
	commentID := firstNonEmptyString(result.RemoteID, comment.ID, contentHash(remoteID, comment.Body, now))
	created := firstNonZeroTime(comment.CreatedAt.UTC(), now)
	updated := firstNonZeroTime(comment.UpdatedAt.UTC(), created)
	graph := cache.RecordGraph{Record: record, Comments: []cache.RecordComment{{RepoID: repoID, RecordID: stableID, CommentID: commentID, Author: comment.Author, Body: comment.Body, ContentHash: contentHash(commentID, comment.Body), RemoteRevision: firstNonEmptyString(result.RemoteRevision, result.ResponseHash), CreatedAt: created, UpdatedAt: updated}}}
	return writeConfirmation{confirmed: result.Confirmed, remoteID: commentID, remoteNumber: firstNonZeroInt(result.ParentIssueNumber, number), remoteRevision: result.RemoteRevision, message: result.Operation, completedAt: firstNonZeroTime(result.ConfirmedAt, now)}, graph, nil
}

func writeIdempotency(command string, req WriteCommandRequest) (string, string) {
	payload, _ := json.Marshal(struct {
		Command string
		RepoID  string
		ID      string
		Number  int
		Slug    string
		Title   string
		Body    string
		State   string
		Label   string
		Labels  []string
	}{command, req.RepoID, req.ID, req.Number, req.Slug, strings.TrimSpace(req.Title), req.Body, req.State, strings.TrimSpace(req.Label), req.Labels})
	sum := sha256.Sum256(payload)
	fingerprint := hex.EncodeToString(sum[:])
	if strings.TrimSpace(req.IdempotencyKey) != "" {
		return strings.TrimSpace(req.IdempotencyKey), fingerprint
	}
	return fingerprint[:32], fingerprint
}

func writeTargetID(req WriteCommandRequest) string {
	if req.ID != "" {
		return req.ID
	}
	if req.Number != 0 {
		return strconv.Itoa(req.Number)
	}
	return req.Slug
}

func replayWriteResult(command string, entry cache.AuditTrailEntry, fingerprint string, now time.Time) WriteCommandResult {
	return WriteCommandResult{Command: command, Status: "already_applied", RepoID: entry.RepoID, ID: entry.RecordID, RemoteID: entry.RemoteID, IdempotencyKey: entry.IdempotencyKey, SourceFingerprint: fingerprint, Replayed: true, Evidence: "replayed from audit_trail", GeneratedAt: now}
}

func (s *Service) writeAdapterErrorCode(mode WriteMode, err error) string {
	if mode == WriteModeLive && gitcode.IsFixtureReadOnly(err) {
		return "write_fixture_fallback_detected"
	}
	return writeErrorCode(err)
}

func writeErrorCode(err error) string {
	var conflict gitcode.ErrConflict
	if errors.As(err, &conflict) {
		return "write_conflict"
	}
	var auth gitcode.ErrAuthExpired
	if errors.As(err, &auth) {
		return "write_unauthorized"
	}
	var limited gitcode.ErrRateLimited
	if errors.As(err, &limited) {
		return "write_rate_limited"
	}
	var network gitcode.ErrNetworkUnavailable
	if errors.As(err, &network) {
		return "write_network_unavailable"
	}
	return "write_provider_error"
}

func writeFailureCause(code string, err error) error {
	if code == "write_fixture_fallback_detected" {
		return nil
	}
	return err
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstNonZeroInt(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func firstNonZeroTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Time{}
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

func (s *Service) storedSnapshot(ctx context.Context, req ExportSnapshotRequest) (Snapshot, error) {
	if strings.TrimSpace(req.SnapshotID) == "" {
		stored, err := s.createStoredSnapshot(ctx, req.RepoID, req)
		if err != nil {
			return Snapshot{}, err
		}
		req.SnapshotID = stored.ID
	}
	repoID, err := s.requireRepo(ctx, req.RepoID, "export-snapshot")
	if err != nil {
		return Snapshot{}, err
	}
	stored, err := s.store.GetSnapshot(ctx, repoID, req.SnapshotID)
	if err != nil {
		return Snapshot{}, normalizeError(err, "snapshot", req.SnapshotID)
	}
	chunks, err := s.store.ListSnapshotChunks(ctx, repoID, req.SnapshotID)
	if err != nil {
		return Snapshot{}, err
	}
	if stored.ChunkCount != len(chunks) {
		return Snapshot{}, ErrSnapshotConsistency{RepoID: repoID, SnapshotID: req.SnapshotID, Expectation: "chunk_count"}
	}
	if stored.ChunkSetHash != "" {
		recomputed, err := snapshotHash(snapshotChunkHashRows(chunks))
		if err != nil {
			return Snapshot{}, err
		}
		if recomputed != stored.ChunkSetHash {
			return Snapshot{}, ErrSnapshotConsistency{RepoID: repoID, SnapshotID: req.SnapshotID, Expectation: "chunk_set_hash"}
		}
	}
	var snapshot Snapshot
	if err := json.Unmarshal([]byte(stored.ManifestJSON), &snapshot); err != nil {
		return Snapshot{}, err
	}
	if stored.ManifestHash != "" {
		recomputedManifest, err := snapshotHash(snapshotManifest(snapshot))
		if err != nil {
			return Snapshot{}, err
		}
		if recomputedManifest != stored.ManifestHash {
			return Snapshot{}, ErrSnapshotConsistency{RepoID: repoID, SnapshotID: req.SnapshotID, Expectation: "manifest_hash"}
		}
	}
	if stored.WarningsJSON != "" {
		_ = json.Unmarshal([]byte(stored.WarningsJSON), &snapshot.Warnings)
	}
	snapshot.Chunks = snapshotChunksFromCache(chunks)
	snapshot.ManifestHash = stored.ManifestHash
	snapshot.ChunkSetHash = stored.ChunkSetHash
	sortSnapshot(&snapshot)
	return snapshot, nil
}

func (s *Service) createStoredSnapshot(ctx context.Context, repoID string, req ExportSnapshotRequest) (cache.Snapshot, error) {
	snapshot, err := s.buildSnapshot(ctx, req)
	if err != nil {
		return cache.Snapshot{}, err
	}
	manifestHash, err := snapshotHash(snapshotManifest(snapshot))
	if err != nil {
		return cache.Snapshot{}, err
	}
	snapshotID := manifestHash
	if len(snapshotID) > 32 {
		snapshotID = snapshotID[:32]
	}
	chunks := cacheSnapshotChunks(snapshot, snapshotID)
	chunkSetHash, err := snapshotHash(snapshotChunkHashRows(chunks))
	if err != nil {
		return cache.Snapshot{}, err
	}
	manifestJSON, err := json.Marshal(snapshot)
	if err != nil {
		return cache.Snapshot{}, err
	}
	warningsJSON, err := json.Marshal(snapshot.Warnings)
	if err != nil {
		return cache.Snapshot{}, err
	}
	stored := cache.Snapshot{RepoID: snapshot.RepoID, ID: snapshotID, Format: normalizeSnapshotFormat(req.Format), ContentHash: manifestHash, RecordCount: len(snapshot.Sources), CreatedAt: snapshot.CreatedAt, SchemaVersion: snapshot.SchemaVersion, ManifestHash: manifestHash, ChunkSetHash: chunkSetHash, ChunkCount: len(chunks), ManifestJSON: string(manifestJSON), WarningsJSON: string(warningsJSON), Metadata: map[string]string{"schema_version": snapshot.SchemaVersion}, Chunks: chunks}
	if err := s.store.UpsertSnapshot(ctx, stored); err != nil {
		return cache.Snapshot{}, err
	}
	return stored, nil
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
		if len(chunks) == 0 {
			snapshot.Warnings = append(snapshot.Warnings, IndexWarning{RepoID: repoID, SourceID: source.ID, RecordID: source.ID, Code: "missing_index", State: "missing_index", Message: "source has no indexed chunks"})
		}
		for _, chunk := range chunks {
			if chunk.ContentHash != source.ContentHash {
				snapshot.Warnings = append(snapshot.Warnings, IndexWarning{RepoID: repoID, SourceID: source.ID, RecordID: source.ID, Code: "stale_index", State: "stale_index", Message: "chunk content hash differs from source content hash"})
			}
			if chunk.LineStart <= 0 || chunk.LineEnd <= 0 || chunk.ByteEnd <= chunk.ByteStart {
				snapshot.Warnings = append(snapshot.Warnings, IndexWarning{RepoID: repoID, SourceID: source.ID, RecordID: source.ID, Code: "missing_citation", State: "missing_citation", Message: "chunk citation range is unavailable"})
			}
			snapshot.Chunks = append(snapshot.Chunks, SnapshotChunk{RepoID: chunk.RepoID, ID: chunk.ID, SourceID: chunk.SourceID, RecordID: chunk.RecordID, ContentHash: chunk.ContentHash, ByteStart: chunk.ByteStart, ByteEnd: chunk.ByteEnd, LineStart: chunk.LineStart, LineEnd: chunk.LineEnd, HeadingPath: append([]string(nil), chunk.HeadingPath...), Text: chunk.Text, NormalizedText: chunk.NormalizedText, InheritedMetadata: copyStringMap(chunk.InheritedMetadata), OutboundLinks: sortedStrings(chunk.OutboundLinks), ResolvedAliases: copyStringMap(chunk.ResolvedAliases), SourceRevisionHash: source.ContentHash, IndexBuildID: chunk.SnapshotID})
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
