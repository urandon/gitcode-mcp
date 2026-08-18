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
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gitcode-mcp/internal/audit"
	"gitcode-mcp/internal/buildinfo"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/feedback"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/index"
)

const SearchModeFullText = index.SearchModeFullText

type Service struct {
	store                  cache.Store
	client                 gitcode.Client
	now                    func() time.Time
	lockPath               string
	providerMode           gitcode.ProviderMode
	writeCredentialPresent bool
	feedbackConfig         feedback.Config
}

type auditClaimStore interface {
	ClaimAuditEvent(context.Context, cache.AuditTrailEntry) (bool, error)
}

func New(store cache.Store) *Service {
	svc, err := NewWithMode(store, gitcode.ProviderModeFixture, "", ServiceConfig{})
	if err != nil {
		return &Service{store: store, client: sanitizedFixtureClient{}, now: func() time.Time { return time.Now().UTC() }, lockPath: filepath.Join(os.TempDir(), "gitcode-mcp-sync.lock"), providerMode: gitcode.ProviderModeFixture}
	}
	return svc
}

func NewWithClient(store cache.Store, client gitcode.Client) *Service {
	return NewWithClientConfig(store, client, ServiceConfig{})
}

func NewWithClientConfig(store cache.Store, client gitcode.Client, cfg ServiceConfig) *Service {
	svc := New(store)
	svc.client = client
	svc.providerMode = gitcode.ProviderMode("custom")
	svc.lockPath = serviceLockPath(cfg.LockPath)
	svc.ConfigureFeedback(cfg.Feedback)
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
			store:          store,
			client:         sanitizedFixtureClient{},
			now:            func() time.Time { return time.Now().UTC() },
			lockPath:       serviceLockPath(cfg.LockPath),
			providerMode:   gitcode.ProviderModeFixture,
			feedbackConfig: normalizeFeedbackConfig(cfg.Feedback),
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
			RateLimitRPS:    cfg.RateLimitRPS,
			RateLimitBurst:  cfg.RateLimitBurst,
		})
		if err != nil {
			return nil, err
		}
		return &Service{
			store:                  store,
			client:                 gitcode.Client(client),
			now:                    func() time.Time { return time.Now().UTC() },
			lockPath:               serviceLockPath(cfg.LockPath),
			providerMode:           gitcode.ProviderModeLive,
			writeCredentialPresent: true,
			feedbackConfig:         normalizeFeedbackConfig(cfg.Feedback),
		}, nil
	case gitcode.ProviderModeUnavailable:
		return nil, gitcode.ErrProviderUnavailable{Reason: "provider unavailable"}
	default:
		return nil, gitcode.ErrProviderUnavailable{Reason: "unknown provider mode " + string(mode)}
	}
}

func serviceLockPath(lockPath string) string {
	lockPath = strings.TrimSpace(lockPath)
	if lockPath != "" {
		return lockPath
	}
	return filepath.Join(os.TempDir(), "gitcode-mcp-sync.lock")
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

func (sanitizedFixtureClient) ListPRs(context.Context, gitcode.PRListRequest) (gitcode.Page[gitcode.PullRequest], error) {
	return gitcode.Page[gitcode.PullRequest]{}, nil
}

func (sanitizedFixtureClient) GetPR(context.Context, gitcode.PRRequest) (gitcode.PullRequest, error) {
	return gitcode.PullRequest{}, ErrInvalidQuery{Field: "pull_request", Message: "fixture pull request is unavailable"}
}

func (sanitizedFixtureClient) ListPRComments(context.Context, gitcode.PRRequest) (gitcode.Page[gitcode.PRComment], error) {
	return gitcode.Page[gitcode.PRComment]{}, nil
}

func (sanitizedFixtureClient) CreatePR(context.Context, gitcode.CreatePRRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.PullRequest], error) {
	return gitcode.WriteResult[gitcode.PullRequest]{}, gitcode.FixtureReadOnlyError("CreatePR")
}

func (sanitizedFixtureClient) UpdatePR(context.Context, gitcode.UpdatePRRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.PullRequest], error) {
	return gitcode.WriteResult[gitcode.PullRequest]{}, gitcode.FixtureReadOnlyError("UpdatePR")
}

func (sanitizedFixtureClient) LinkPRIssue(context.Context, gitcode.LinkPRIssueRequest, gitcode.WriteOptions) (gitcode.WriteResult[[]gitcode.Issue], error) {
	return gitcode.WriteResult[[]gitcode.Issue]{}, gitcode.FixtureReadOnlyError("LinkPRIssue")
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

func (sanitizedFixtureClient) UpdateIssueComment(context.Context, gitcode.UpdateIssueCommentRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Comment], error) {
	return gitcode.WriteResult[gitcode.Comment]{}, gitcode.FixtureReadOnlyError("sanitized fixture write")
}

func (sanitizedFixtureClient) CreatePRComment(context.Context, gitcode.CreatePRCommentRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.PRComment], error) {
	return gitcode.WriteResult[gitcode.PRComment]{}, gitcode.FixtureReadOnlyError("sanitized fixture write")
}

func (sanitizedFixtureClient) CreatePRReviewComment(context.Context, gitcode.CreatePRReviewCommentRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.PRComment], error) {
	return gitcode.WriteResult[gitcode.PRComment]{}, gitcode.FixtureReadOnlyError("sanitized fixture write")
}

func (sanitizedFixtureClient) ReplyPRReviewComment(context.Context, gitcode.ReplyPRReviewCommentRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.PRComment], error) {
	return gitcode.WriteResult[gitcode.PRComment]{}, gitcode.FixtureReadOnlyError("sanitized fixture write")
}

func (sanitizedFixtureClient) CreateWikiPage(context.Context, gitcode.CreateWikiPageRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.WikiPage], error) {
	return gitcode.WriteResult[gitcode.WikiPage]{}, gitcode.FixtureReadOnlyError("sanitized fixture write")
}

func (sanitizedFixtureClient) UpdateWikiPage(context.Context, gitcode.UpdateWikiPageRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.WikiPage], error) {
	return gitcode.WriteResult[gitcode.WikiPage]{}, gitcode.FixtureReadOnlyError("sanitized fixture write")
}

func (sanitizedFixtureClient) DeleteWikiPage(context.Context, gitcode.DeleteWikiPageRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.WikiPage], error) {
	return gitcode.WriteResult[gitcode.WikiPage]{}, gitcode.FixtureReadOnlyError("sanitized fixture write")
}

func (sanitizedFixtureClient) AddLabel(context.Context, gitcode.LabelRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	return gitcode.WriteResult[gitcode.Issue]{}, gitcode.FixtureReadOnlyError("sanitized fixture write")
}

func (sanitizedFixtureClient) RemoveLabel(context.Context, gitcode.LabelRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	return gitcode.WriteResult[gitcode.Issue]{}, gitcode.FixtureReadOnlyError("sanitized fixture write")
}

func (sanitizedFixtureClient) ListMilestones(context.Context, gitcode.MilestoneListRequest) (gitcode.Page[gitcode.Milestone], error) {
	return gitcode.Page[gitcode.Milestone]{}, gitcode.FixtureReadOnlyError("sanitized fixture write")
}

func (sanitizedFixtureClient) ListPushRemoteMirrors(context.Context, gitcode.PushMirrorListRequest) ([]gitcode.PushMirror, error) {
	return nil, gitcode.FixtureReadOnlyError("sanitized fixture read")
}

func (sanitizedFixtureClient) TriggerPushRemoteMirror(context.Context, gitcode.PushMirrorTriggerRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.PushMirror], error) {
	return gitcode.WriteResult[gitcode.PushMirror]{}, gitcode.FixtureReadOnlyError("sanitized fixture write")
}

func (sanitizedFixtureClient) GetMilestone(context.Context, gitcode.MilestoneRequest) (gitcode.Milestone, error) {
	return gitcode.Milestone{}, gitcode.FixtureReadOnlyError("sanitized fixture write")
}

func (sanitizedFixtureClient) CreateMilestone(context.Context, gitcode.MilestoneWriteRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Milestone], error) {
	return gitcode.WriteResult[gitcode.Milestone]{}, gitcode.FixtureReadOnlyError("sanitized fixture write")
}

func (sanitizedFixtureClient) UpdateMilestone(context.Context, gitcode.MilestoneWriteRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Milestone], error) {
	return gitcode.WriteResult[gitcode.Milestone]{}, gitcode.FixtureReadOnlyError("sanitized fixture write")
}

func (sanitizedFixtureClient) GetRelease(context.Context, gitcode.ReleaseRequest) (gitcode.Release, error) {
	return gitcode.Release{}, gitcode.FixtureReadOnlyError("sanitized fixture write")
}

func (sanitizedFixtureClient) CreateRelease(context.Context, gitcode.ReleaseWriteRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Release], error) {
	return gitcode.WriteResult[gitcode.Release]{}, gitcode.FixtureReadOnlyError("sanitized fixture write")
}

func (sanitizedFixtureClient) UpdateRelease(context.Context, gitcode.ReleaseWriteRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Release], error) {
	return gitcode.WriteResult[gitcode.Release]{}, gitcode.FixtureReadOnlyError("sanitized fixture write")
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

func (s *Service) ResetLiveCache(ctx context.Context, req ResetLiveCacheRequest) (ResetLiveCacheResult, error) {
	repoID, err := s.requireRepo(ctx, req.RepoID, "cache reset live")
	if err != nil {
		return ResetLiveCacheResult{}, err
	}
	if err := s.store.ResetLive(ctx, repoID); err != nil {
		return ResetLiveCacheResult{}, normalizeError(err, "cache reset live", repoID)
	}
	return ResetLiveCacheResult{RepoID: repoID, Reset: "live"}, nil
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
	return CacheStatusResult{RepoID: repoID, WALCapable: walCapable, JournalMode: journalMode, Records: counts.Records, Comments: counts.Comments, IdentityAliases: counts.IdentityAliases, SyncEvents: counts.SyncEvents, AuditRows: counts.AuditRows, Snapshots: counts.Snapshots, SnapshotChunks: counts.SnapshotChunks, Chunks: counts.Chunks, RemoteRevisions: counts.RemoteRevisions, RAGNamespaces: counts.RAGNamespaces, RAGEmbeddings: counts.RAGEmbeddings, RAGIndexRuns: counts.RAGIndexRuns, IndexFreshnessWarnings: len(freshness.Warnings), IndexFreshnessByWarning: byWarning}, nil
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
	schemaVersion, err := s.store.SchemaVersion(ctx)
	if err != nil {
		return RepositoryStatus{}, normalizeError(err, "cache schema", repoID)
	}
	counts, err := s.store.RecordCounts(ctx, repoID)
	if err != nil {
		return RepositoryStatus{}, normalizeError(err, "cache", repoID)
	}
	issues, err := s.store.ListRecords(ctx, cache.RecordFilter{RepoID: repoID, Type: "issue"})
	if err != nil {
		return RepositoryStatus{}, normalizeError(err, "issues", repoID)
	}
	queueState := "schema_unavailable"
	var queueSummary *IssueCommentQueueSummary
	if schemaVersion >= cache.IssueCommentSyncSchemaVersion() {
		queue, err := s.store.IssueCommentSyncSummary(ctx, repoID)
		if err != nil {
			return RepositoryStatus{}, normalizeError(err, "issue comments", repoID)
		}
		queueState = "available"
		queueSummary = &IssueCommentQueueSummary{Phase: "status", Pending: queue.Pending, Deferred: queue.Deferred, Complete: queue.Complete, Total: queue.Total}
	}
	build := buildinfo.Current()
	expectedSchemaVersion := cache.CurrentSchemaVersion()
	status := RepositoryStatus{
		RepoID:                     repo.RepoID,
		Owner:                      repo.Owner,
		Name:                       repo.Name,
		APIBaseURL:                 sanitizeAPIBaseURL(repo.APIBaseURL),
		DisplayName:                repo.DisplayName,
		Aliases:                    append([]string(nil), repo.Aliases...),
		BindingState:               "ready",
		AliasConflictState:         "none",
		CacheState:                 repositoryCacheState(schemaVersion, expectedSchemaVersion),
		IndexState:                 "unknown",
		BinaryVersion:              build.Version,
		BinaryCommit:               build.ShortCommit(),
		BinaryBuildDate:            build.Date,
		BinaryVersionSource:        build.Source,
		CacheSchemaVersion:         schemaVersion,
		ExpectedCacheSchemaVersion: expectedSchemaVersion,
		IssueRecords:               len(issues),
		IssueComments:              counts.Comments,
		IssueCommentQueueState:     queueState,
		IssueCommentQueue:          queueSummary,
	}
	for _, scope := range repo.Scopes {
		status.Scopes = append(status.Scopes, RepositoryScope(scope))
	}
	return status, nil
}

func repositoryCacheState(detected, expected int) string {
	switch {
	case detected == expected:
		return "ready"
	case detected > 0 && detected < expected:
		return "migration_required"
	case detected > expected:
		return "binary_upgrade_required"
	default:
		return "unknown"
	}
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
			switch scope {
			case "pull", "pulls", "pr", "prs", "pull_request", "pull_requests", "comment", "comments":
				scope = RepositoryScopeIssues
			}
			if scope != RepositoryScopeIssues && scope != RepositoryScopeWiki {
				return nil, ErrInvalidQuery{Field: "scopes", Message: "scopes must contain issues, wiki, pulls, or comments"}
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
	results, err := s.store.SearchSources(ctx, cache.SearchQuery{RepoID: repoID, Query: req.Query, Kind: req.Kind, Provenance: cache.Provenance(req.Provenance), Limit: req.Limit})
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
		out = append(out, SearchSourceResult{RepoID: source.RepoID, ID: result.ID, Path: result.Path, Title: result.Title, Kind: source.Kind, Status: source.Status, Provenance: string(source.Provenance), Snippet: result.Snippet, LineStart: line, LineEnd: line, Score: result.Score})
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
	return SearchSourcesResult{RepoID: repoID, Query: req.Query, SearchMode: SearchModeFullText, Results: out, Limit: req.Limit, Offset: req.Offset}, nil
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
	sources, err := s.store.ListSources(ctx, cache.SourceFilter{RepoID: repoID, Kind: req.Kind, Status: req.Status, Provenance: cache.Provenance(req.Provenance), Limit: req.limitPlusOffset()})
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
	result := SyncStatusResult{RepoID: status.RepoID, SourceID: status.SourceID, RemoteType: status.RemoteType, RemoteID: status.RemoteID, RemoteRevision: status.RemoteRevision, Status: status.Status, Freshness: freshness, Provenance: string(source.Provenance), LocalUpdatedAt: source.UpdatedAt.UTC(), LastFetchedAt: status.LastFetchedAt.UTC()}
	if source.Kind == "issue" {
		queued, ok, err := s.store.GetIssueCommentSync(ctx, repoID, id)
		if err != nil {
			return SyncStatusResult{}, err
		}
		if ok {
			result.IssueComments = &IssueCommentCoverageStatus{Status: queued.Status, RemoteRevision: queued.RemoteRevision, ExpectedCount: queued.ExpectedCount, Attempts: queued.Attempts, LastErrorClass: queued.LastErrorClass, RetryAfter: queued.RetryAfter, LastAttemptAt: queued.LastAttemptAt.UTC(), QueueUpdatedAt: queued.UpdatedAt.UTC()}
		}
	}
	return result, nil
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
	queue, err := s.store.IssueCommentSyncSummary(ctx, repoID)
	if err != nil {
		return SyncStatusSummaryResult{}, err
	}
	result.IssueComments = &IssueCommentQueueSummary{Phase: "status", Pending: queue.Pending, Deferred: queue.Deferred, Complete: queue.Complete, Total: queue.Total}
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
	if err := s.store.UpsertSyncGraph(ctx, s.syncGraphFromSourceGraph(req.RepoID, graph)); err != nil {
		if inProgressRecorded {
			_ = s.store.RecordSyncEvent(ctx, failedSyncEvent(inProgress, err, s.now().UTC()))
		}
		return SyncResult{}, err
	}
	if err := s.recordTargetedIssueCommentCoverage(ctx, remoteType, graph, counts); err != nil {
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
	stored, err := s.store.GetSourceScoped(ctx, repoID, graph.Source.ID)
	if err != nil {
		return SyncResult{}, err
	}
	return SyncResult{IdempotencyKey: key, Status: "succeeded", Counts: counts, SyncEventID: eventID, Freshness: string(FreshnessFresh), Record: sourceSummary(stored), GeneratedAt: syncCompletedAt, StartedAt: syncStartedAt, CompletedAt: syncCompletedAt, ZeroDelta: zeroDelta}, nil
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
			re := newResourceErrorWithMessage(req.StableID, "", err, fmt.Sprintf("sync resources: context cancelled at request %d", i))
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
			re := newResourceError(sourceID, remoteType, err)
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
		if req.Bounds != nil {
			diag := SyncDiagnosticCancelled
			if errors.Is(err, context.DeadlineExceeded) {
				diag = SyncDiagnosticTimeout
			}
			return nil, &PartialSyncError{Errors: nil, SuccessCount: 0, FailureCount: 0, Diagnostic: diag}
		}
		return nil, err
	}
	ctx = withBulkRateLimitProgress(ctx, bulkProgressChan(req))
	repoID, err := s.requireRepo(ctx, req.RepoID, "bulk-sync-issues")
	if err != nil {
		return nil, err
	}
	req.RepoID = repoID
	s.ensureBulkIdempotencyKey(&req, "issues")
	if err := s.validateRepoScope(ctx, repoID, "issues"); err != nil {
		return nil, err
	}
	route, err := s.BuildAdapterRoute(ctx, repoID, RepositoryScopeIssues)
	if err != nil {
		return nil, err
	}

	if req.Bounds == nil {
		page, err := s.client.ListIssues(ctx, gitcode.IssueListRequest{Owner: route.Owner, Repo: route.Name, State: "all", OrderBy: "updated_at", Direction: "desc", Page: req.Page, PerPage: req.PerPage})
		if err != nil {
			return bulkSyncFailureResult(s.normalizeSyncFailure(err, SyncRequest{RepoID: req.RepoID, RemoteAlias: "issue:*"}, "issues", "*"), "issue:*", "issues")
		}
		result := &SyncResourcesResult{Results: make([]SyncResult, 0, len(page.Items)), Failures: make([]ResourceError, 0)}
		beforeCount := len(result.Results)
		beforeDeferred := syncResultsDeferredCount(result.Results)
		s.stageIssuePage(ctx, req, page.Items, result, 0)
		emitProgress(req.ProgressChan, ProgressEvent{Collection: "issues", Page: firstNonZeroInt(req.Page, 1), RecordsFetched: len(result.Results) - beforeCount, RecordsDeferred: syncResultsDeferredCount(result.Results) - beforeDeferred})
		result.SuccessCount = len(result.Results)
		result.FailureCount = len(result.Failures)
		if summaryErr := s.attachIssueCommentQueueSummary(ctx, result, repoID, "parent_backfill"); summaryErr != nil {
			return result, summaryErr
		}
		if result.FailureCount > 0 {
			return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount}
		}
		return result, nil
	}

	return s.bulkSyncIssuesBounded(ctx, req, route)
}

func (s *Service) bulkSyncIssuesBounded(ctx context.Context, req BulkSyncRequest, route RepositoryRoute) (*SyncResourcesResult, error) {
	bounds := req.Bounds
	result := &SyncResourcesResult{Results: make([]SyncResult, 0), Failures: make([]ResourceError, 0), Ordering: syncOrderingUpdatedAtDesc}
	watermark, watermarkOK, err := s.completeSyncWatermark(ctx, req.RepoID, "issue")
	if err != nil {
		return bulkSyncFailureResult(err, "issue:*", "issues")
	}
	setWatermarkSummary(result, watermark, watermarkOK)
	var observed syncHighWatermark
	currentPage := req.Page
	if currentPage < 1 {
		currentPage = 1
	}
	perPage := req.PerPage
	if perPage < 1 {
		perPage = 10
	}
	totalRequested := bounds.MaxRecords
	if totalRequested <= 0 && bounds.MaxPages > 0 {
		totalRequested = bounds.MaxPages * perPage
	}
	recordsAdvanced := 0
	pagesAdvanced := 0

	for {
		if ctx.Err() != nil {
			diag := SyncDiagnosticCancelled
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				diag = SyncDiagnosticTimeout
				result.TraversalStatus = "timeout"
			} else {
				result.TraversalStatus = "cancelled"
			}
			result.SuccessCount = len(result.Results)
			result.FailureCount = len(result.Failures)
			s.recordSyncFrontierBestEffort(ctx, req.RepoID, "issue", result, observed)
			return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount, Diagnostic: diag, TotalRequested: totalRequested}
		}
		if bounds.MaxPages > 0 && pagesAdvanced >= bounds.MaxPages {
			result.StopReason = "max_pages"
			result.TraversalStatus = "bounded"
			break
		}
		if bounds.MaxRecords > 0 && recordsAdvanced >= bounds.MaxRecords {
			result.StopReason = "max_records"
			result.TraversalStatus = "bounded"
			break
		}
		page, err := s.client.ListIssues(ctx, gitcode.IssueListRequest{Owner: route.Owner, Repo: route.Name, State: "all", OrderBy: "updated_at", Direction: "desc", Page: currentPage, PerPage: perPage})
		if err != nil {
			result.SuccessCount = len(result.Results)
			result.FailureCount = len(result.Failures)
			result.TraversalStatus = "partial"
			re := newResourceErrorWithMessage("issue:*", "issues", s.normalizeSyncFailure(err, SyncRequest{RepoID: req.RepoID, RemoteAlias: "issue:*"}, "issues", "*"), err.Error())
			result.Failures = append(result.Failures, re)
			result.FailureCount++
			s.recordSyncFrontierBestEffort(ctx, req.RepoID, "issue", result, observed)
			return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount, TotalRequested: totalRequested}
		}
		result.PagesListed++
		result.RecordsListed += len(page.Items)
		observeIssueHighWatermark(&observed, page.Items)
		items, stopByWatermark, skippedByWatermark := filterIssueSummariesByCompleteWatermark(page.Items, watermark, watermarkOK)
		result.SkippedByWatermark += skippedByWatermark
		beforeCount := len(result.Results)
		beforeDeferred := syncResultsDeferredCount(result.Results)
		remaining := 0
		if bounds.MaxRecords > 0 {
			remaining = bounds.MaxRecords - recordsAdvanced
		}
		advanced, boundedByRecords := s.stageIssuePage(ctx, req, items, result, remaining)
		recordsAdvanced += advanced
		if advanced > 0 {
			pagesAdvanced++
		}
		recordsFetched := len(result.Results) - beforeCount
		emitProgress(bounds.ProgressChan, ProgressEvent{Collection: "issues", Page: currentPage, RecordsFetched: recordsFetched, RecordsDeferred: syncResultsDeferredCount(result.Results) - beforeDeferred})
		if stopByWatermark {
			result.StopReason = "watermark"
			result.TraversalStatus = "complete"
			result.WatermarkStatus = "used"
			result.WatermarkReason = "previous_complete_frontier"
			break
		}
		if len(page.Items) < perPage {
			result.StopReason = "end_of_collection"
			result.TraversalStatus = "complete"
			break
		}
		if boundedByRecords {
			result.StopReason = "max_records"
			result.TraversalStatus = "bounded"
			break
		}
		currentPage++
	}
	result.SuccessCount = len(result.Results)
	result.FailureCount = len(result.Failures)
	if summaryErr := s.attachIssueCommentQueueSummary(ctx, result, req.RepoID, "parent_backfill"); summaryErr != nil {
		return result, summaryErr
	}
	if result.FailureCount > 0 {
		result.TraversalStatus = "partial"
		s.recordSyncFrontierBestEffort(ctx, req.RepoID, "issue", result, observed)
		return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount}
	}
	if err := s.recordSyncFrontier(ctx, req.RepoID, "issue", result, observed); err != nil {
		return bulkSyncFailureResult(err, "issue:*", "issues")
	}
	return result, nil
}

func (s *Service) stageIssuePage(ctx context.Context, req BulkSyncRequest, items []gitcode.IssueSummary, result *SyncResourcesResult, maxAdvances int) (int, bool) {
	advanced := 0
	for _, summary := range items {
		if maxAdvances > 0 && advanced >= maxAdvances {
			return advanced, true
		}
		remoteID := strconv.Itoa(summary.Number)
		issue := gitcode.Issue{ID: summary.ID, Number: summary.Number, Title: summary.Title, Body: summary.Body, Status: summary.Status, State: summary.State, Comments: summary.Comments, Labels: summary.Labels, CreatedAt: summary.CreatedAt, UpdatedAt: summary.UpdatedAt}
		syncReq := SyncRequest{RepoID: req.RepoID, AliasType: "issue", AliasID: remoteID, IdempotencyKey: scopedBulkSyncKey(req.IdempotencyKey, "issue", remoteID), MaxAttempts: req.MaxAttempts, MaxSize: req.MaxSize}
		graph, counts, err := s.stageIssueParent(ctx, syncReq, "issue", remoteID, issue)
		if err != nil {
			result.Failures = append(result.Failures, newResourceError(remoteID, "issue", err))
			continue
		}
		counts.Listed = 1
		completedAt := s.now().UTC()
		eventID := syncEventID(syncReq.IdempotencyKey)
		zeroDelta := counts.Fetched > 0 && counts.Skipped == counts.Fetched && counts.Updated == 0 && counts.Inserted == 0 && counts.Conflicts == 0
		graph.SyncEvents = append(graph.SyncEvents, cache.SyncEvent{ID: eventID, SourceID: graph.Source.ID, RemoteType: "issue", RemoteID: remoteID, RemoteRevision: graph.SyncStatus.RemoteRevision, Status: "succeeded", IdempotencyKey: syncReq.IdempotencyKey, Message: syncEventMessage(counts), CreatedAt: completedAt, StartedAt: completedAt, CompletedAt: completedAt, ZeroDelta: zeroDelta})
		if err := s.store.UpsertSyncGraph(ctx, s.syncGraphFromSourceGraph(req.RepoID, graph)); err != nil {
			result.Failures = append(result.Failures, newResourceError(remoteID, "issue", err))
			continue
		}
		queued, err := s.enqueueIssueCommentSync(ctx, graph, issue)
		if err != nil {
			result.Failures = append(result.Failures, newResourceError(remoteID, "issue_comments", err))
			continue
		}
		if queued {
			counts.Deferred++
		}
		stored, err := s.store.GetSourceScoped(ctx, req.RepoID, graph.Source.ID)
		if err != nil {
			result.Failures = append(result.Failures, newResourceError(remoteID, "issue", err))
			continue
		}
		result.Results = append(result.Results, SyncResult{IdempotencyKey: syncReq.IdempotencyKey, Status: "succeeded", Counts: counts, SyncEventID: eventID, Freshness: string(FreshnessFresh), Record: sourceSummary(stored), GeneratedAt: completedAt, StartedAt: completedAt, CompletedAt: completedAt, ZeroDelta: zeroDelta})
		if counts.SkippedByRevision == 0 {
			advanced++
		}
	}
	return advanced, false
}

func (s *Service) enqueueIssueCommentSync(ctx context.Context, graph cache.SourceGraph, issue gitcode.Issue) (bool, error) {
	if graph.SyncStatus == nil {
		return false, errors.New("issue comment queue requires parent sync status")
	}
	now := s.now().UTC()
	item := cache.IssueCommentSync{
		RepoID:         graph.Source.RepoID,
		SourceID:       graph.Source.ID,
		IssueNumber:    issue.Number,
		RemoteID:       graph.SyncStatus.RemoteID,
		ProviderID:     issue.ID,
		RemoteRevision: graph.SyncStatus.RemoteRevision,
		ExpectedCount:  issue.Comments,
		Status:         "pending",
		UpdatedAt:      now,
	}
	if issue.Comments == 0 {
		item.Status = "complete"
		if err := s.store.ReplaceRecordComments(ctx, item.RepoID, item.SourceID, nil); err != nil {
			return false, err
		}
	}
	existing, ok, err := s.store.GetIssueCommentSync(ctx, item.RepoID, item.SourceID)
	if err != nil {
		return false, err
	}
	if ok && existing.RemoteRevision == item.RemoteRevision {
		existing.IssueNumber = item.IssueNumber
		existing.RemoteID = item.RemoteID
		existing.ProviderID = item.ProviderID
		existing.ExpectedCount = item.ExpectedCount
		existing.UpdatedAt = now
		if issue.Comments == 0 {
			existing.Status = "complete"
			existing.LastErrorClass = ""
			existing.RetryAfter = ""
		}
		if err := s.store.UpsertIssueCommentSync(ctx, existing); err != nil {
			return false, err
		}
		return existing.Status != "complete", nil
	}
	if err := s.store.UpsertIssueCommentSync(ctx, item); err != nil {
		return false, err
	}
	return item.Status != "complete", nil
}

func (s *Service) BulkSyncIssueComments(ctx context.Context, req BulkSyncRequest) (*SyncResourcesResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	repoID, err := s.requireRepo(ctx, req.RepoID, "bulk-sync-issue-comments")
	if err != nil {
		return nil, err
	}
	req.RepoID = repoID
	if err := s.validateRepoScope(ctx, repoID, "issues"); err != nil {
		return nil, err
	}
	ctx = withBulkRateLimitProgress(ctx, bulkProgressChan(req))
	if err := s.seedLegacyIssueCommentQueue(ctx, repoID); err != nil {
		return nil, err
	}
	pending, err := s.store.ListIssueCommentSync(ctx, cache.IssueCommentSyncFilter{RepoID: repoID, Statuses: []string{"pending", "deferred"}})
	if err != nil {
		return nil, err
	}
	if len(pending) == 0 {
		result := &SyncResourcesResult{Results: []SyncResult{}, Failures: []ResourceError{}, Ordering: "queue_updated_at_asc", TraversalStatus: "complete", StopReason: "queue_empty"}
		if err := s.attachIssueCommentQueueSummary(ctx, result, repoID, "drain"); err != nil {
			return result, err
		}
		return result, nil
	}
	route, err := s.BuildAdapterRoute(ctx, repoID, RepositoryScopeIssues)
	if err != nil {
		return nil, err
	}
	if req.IncrementalQueue {
		return s.bulkSyncIssueCommentsPerIssue(ctx, req, route, "incremental_queue")
	}
	lister, aggregateAvailable := s.client.(gitcode.RepositoryIssueCommentLister)
	_, parentsComplete, frontierErr := s.completeSyncWatermark(ctx, repoID, "issue")
	if frontierErr != nil {
		return nil, frontierErr
	}
	if aggregateAvailable && parentsComplete {
		items, err := s.store.ListIssueCommentSync(ctx, cache.IssueCommentSyncFilter{RepoID: repoID})
		if err != nil {
			return nil, err
		}
		result, aggregateErr := s.bulkSyncRepositoryIssueComments(ctx, req, route, lister, pending, items)
		if aggregateErr == nil {
			return result, nil
		}
		if !repositoryIssueCommentsUnsupported(aggregateErr) {
			return result, aggregateErr
		}
		return s.bulkSyncIssueCommentsPerIssue(ctx, req, route, "aggregate_endpoint_unsupported")
	}
	reason := "aggregate_not_implemented"
	if aggregateAvailable && !parentsComplete {
		reason = "parent_frontier_incomplete"
	}
	return s.bulkSyncIssueCommentsPerIssue(ctx, req, route, reason)
}

func (s *Service) bulkSyncIssueCommentsPerIssue(ctx context.Context, req BulkSyncRequest, route RepositoryRoute, fallbackReason string) (*SyncResourcesResult, error) {
	limit := 0
	if req.Bounds != nil {
		limit = req.Bounds.MaxRecords
		if limit <= 0 && req.Bounds.MaxPages > 0 {
			if req.IncrementalQueue {
				limit = req.Bounds.MaxPages
			} else {
				perPage := req.PerPage
				if perPage <= 0 {
					perPage = 100
				}
				limit = req.Bounds.MaxPages * perPage
			}
		}
	}
	items, err := s.store.ListIssueCommentSync(ctx, cache.IssueCommentSyncFilter{RepoID: req.RepoID, Statuses: []string{"pending", "deferred"}, Limit: limit})
	if err != nil {
		return nil, err
	}
	result := &SyncResourcesResult{Results: []SyncResult{}, Failures: []ResourceError{}, Ordering: "queue_updated_at_asc", TraversalStatus: "complete", StopReason: "queue_empty"}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			result.TraversalStatus = "cancelled"
			result.StopReason = "cancelled"
			break
		}
		result.StopReason = "queue_drained"
		syncResult, syncErr := s.syncIssueCommentItem(ctx, route, item)
		if syncErr == nil {
			result.Results = append(result.Results, syncResult)
			emitProgress(req.ProgressChan, ProgressEvent{Collection: "issue_comments", Phase: "drain", RecordsFetched: 1})
			continue
		}
		if isDeferredIssueCommentsRead(syncErr) {
			item.Status = "deferred"
			item.Attempts++
			item.LastAttemptAt = s.now().UTC()
			item.UpdatedAt = item.LastAttemptAt
			item.LastErrorClass, _, _, _ = resourceFailureMetadata(syncErr)
			if item.LastErrorClass == "" {
				item.LastErrorClass = "comments_read"
			}
			if wait := retryDelay(syncErr); wait > 0 {
				item.RetryAfter = wait.String()
			}
			if err := s.store.UpsertIssueCommentSync(ctx, item); err != nil {
				result.Failures = append(result.Failures, newResourceError(item.RemoteID, "issue_comments", err))
				continue
			}
			source, sourceErr := s.store.GetSourceScoped(ctx, req.RepoID, item.SourceID)
			if sourceErr == nil {
				now := s.now().UTC()
				result.Results = append(result.Results, SyncResult{Status: "deferred", Freshness: string(FreshnessStale), Counts: SyncCounts{Fetched: 1, FetchedDetail: 1, Deferred: 1}, Record: sourceSummary(source), GeneratedAt: now, StartedAt: now, CompletedAt: now})
			}
			emitProgress(req.ProgressChan, ProgressEvent{Collection: "issue_comments", Phase: "deferred", RecordsDeferred: 1, Endpoint: resourceEndpoint(syncErr), RetryAfter: item.RetryAfter})
			result.TraversalStatus = "deferred"
			result.StopReason = deferredIssueCommentStopReason(syncErr)
			break
		}
		result.Failures = append(result.Failures, newResourceError(item.RemoteID, "issue_comments", syncErr))
	}
	result.SuccessCount = len(result.Results)
	result.FailureCount = len(result.Failures)
	result.PagesListed = len(items)
	result.RecordsListed = len(items)
	if err := s.attachIssueCommentQueueSummary(ctx, result, req.RepoID, "drain"); err != nil {
		return result, err
	}
	result.IssueComments.Strategy = "per_issue_fallback"
	result.IssueComments.FallbackReason = fallbackReason
	if limit > 0 && len(items) == limit && result.TraversalStatus == "complete" && result.IssueComments != nil && result.IssueComments.Pending+result.IssueComments.Deferred > 0 {
		result.TraversalStatus = "bounded"
		result.StopReason = "max_records"
	}
	if result.FailureCount > 0 {
		return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount}
	}
	return result, nil
}

func (s *Service) bulkSyncRepositoryIssueComments(ctx context.Context, req BulkSyncRequest, route RepositoryRoute, lister gitcode.RepositoryIssueCommentLister, pending, allItems []cache.IssueCommentSync) (*SyncResourcesResult, error) {
	result := &SyncResourcesResult{Results: []SyncResult{}, Failures: []ResourceError{}, Ordering: "repository_comment_order", TraversalStatus: "complete"}
	pendingBySource := make(map[string]cache.IssueCommentSync, len(pending))
	byProviderID := make(map[string]cache.IssueCommentSync, len(allItems))
	byNumber := make(map[int]cache.IssueCommentSync, len(allItems))
	for _, item := range pending {
		pendingBySource[item.SourceID] = item
	}
	for _, item := range allItems {
		if id := strings.TrimSpace(item.ProviderID); id != "" {
			byProviderID[id] = item
		}
		if item.IssueNumber > 0 {
			byNumber[item.IssueNumber] = item
		}
	}
	seen := make(map[string]map[string]struct{}, len(pending))
	seenSources := make(map[string]map[string]struct{}, len(pending))
	expectedTotal := 0
	// An interrupted aggregate traversal restarts at page one. Page-level upserts
	// make that retry idempotent, while starting later could falsely reconcile a
	// parent after missing comments from earlier pages.
	pageNumber := 1
	perPage := req.PerPage
	if perPage < 1 {
		perPage = 100
	}
	if req.Bounds != nil && req.Bounds.MaxRecords > 0 && req.Bounds.MaxRecords < perPage {
		perPage = req.Bounds.MaxRecords
	}
	for {
		if err := ctx.Err(); err != nil {
			result.TraversalStatus = "cancelled"
			result.StopReason = "cancelled"
			if errors.Is(err, context.DeadlineExceeded) {
				result.TraversalStatus = "timeout"
				result.StopReason = "timeout"
			}
			if summaryErr := s.attachRepositoryIssueCommentSummary(context.WithoutCancel(ctx), result, req.RepoID, len(pending)); summaryErr != nil {
				return result, summaryErr
			}
			diagnostic := SyncDiagnosticCancelled
			if errors.Is(err, context.DeadlineExceeded) {
				diagnostic = SyncDiagnosticTimeout
			}
			return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount, Diagnostic: diagnostic}
		}
		if req.Bounds != nil && req.Bounds.MaxPages > 0 && result.PagesListed >= req.Bounds.MaxPages {
			result.TraversalStatus = "bounded"
			result.StopReason = "max_pages"
			break
		}
		if req.Bounds != nil && req.Bounds.MaxRecords > 0 {
			if result.RecordsListed >= req.Bounds.MaxRecords {
				result.TraversalStatus = "bounded"
				result.StopReason = "max_records"
				break
			}
			if result.RecordsListed > 0 && result.RecordsListed+perPage > req.Bounds.MaxRecords {
				result.TraversalStatus = "bounded"
				result.StopReason = "max_records"
				break
			}
		}
		page, err := lister.ListRepositoryIssueComments(ctx, gitcode.RepositoryIssueCommentListRequest{Owner: route.Owner, Repo: route.Name, Page: pageNumber, PerPage: perPage})
		if err != nil {
			if repositoryIssueCommentsUnsupported(err) && result.PagesListed == 0 {
				return result, err
			}
			var rateLimited gitcode.ErrRateLimited
			if errors.As(err, &rateLimited) {
				result.TraversalStatus = "deferred"
				result.StopReason = "rate_limited"
				result.FailureCount = len(result.Failures)
				emitProgress(req.ProgressChan, ProgressEvent{Collection: "issue_comments", Phase: "aggregate_deferred", Endpoint: rateLimited.Endpoint, RetryAfter: rateLimited.RetryAfter.String()})
				if summaryErr := s.attachRepositoryIssueCommentSummary(ctx, result, req.RepoID, len(pending)); summaryErr != nil {
					return result, summaryErr
				}
				if result.FailureCount > 0 {
					return result, &PartialSyncError{Errors: result.Failures, SuccessCount: 0, FailureCount: result.FailureCount}
				}
				return result, nil
			}
			result.TraversalStatus = "partial"
			result.StopReason = "provider_error"
			result.Failures = append(result.Failures, newResourceError("issue-comment:*", "issue_comments", err))
			result.FailureCount = len(result.Failures)
			if summaryErr := s.attachRepositoryIssueCommentSummary(ctx, result, req.RepoID, len(pending)); summaryErr != nil {
				return result, summaryErr
			}
			return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount}
		}
		result.PagesListed++
		result.RecordsListed += len(page.Items)
		if page.TotalCount > 0 {
			if expectedTotal > 0 && expectedTotal != page.TotalCount {
				result.Failures = append(result.Failures, newResourceError("issue-comment:*", "issue_comment_reconciliation", issueCommentReconciliationError(fmt.Sprintf("aggregate total_count changed from %d to %d during traversal", expectedTotal, page.TotalCount))))
			} else {
				expectedTotal = page.TotalCount
			}
		}
		pageComments := make(map[string][]cache.RecordComment)
		for _, comment := range page.Items {
			item, reconcileErr := reconcileRepositoryIssueCommentParent(comment, byProviderID, byNumber)
			if reconcileErr != nil {
				result.Failures = append(result.Failures, newResourceError(strings.TrimSpace(comment.ID), "issue_comment_reconciliation", issueCommentReconciliationError(reconcileErr.Error())))
				continue
			}
			if _, needsRefresh := pendingBySource[item.SourceID]; !needsRefresh {
				continue
			}
			cached, err := cachedIssueComment(comment, item, s.now().UTC())
			if err != nil {
				result.Failures = append(result.Failures, newResourceError(strings.TrimSpace(comment.ID), "issue_comment_reconciliation", err))
				continue
			}
			commentSourceID, err := s.upsertIssueCommentProjection(ctx, item, cached)
			if err != nil {
				result.Failures = append(result.Failures, newResourceError(strings.TrimSpace(comment.ID), "issue_comments", err))
				continue
			}
			pageComments[item.SourceID] = append(pageComments[item.SourceID], cached)
			if seen[item.SourceID] == nil {
				seen[item.SourceID] = map[string]struct{}{}
			}
			seen[item.SourceID][cached.CommentID] = struct{}{}
			if seenSources[item.SourceID] == nil {
				seenSources[item.SourceID] = map[string]struct{}{}
			}
			seenSources[item.SourceID][commentSourceID] = struct{}{}
		}
		for sourceID, comments := range pageComments {
			if err := s.store.UpsertRecordComments(ctx, req.RepoID, sourceID, comments); err != nil {
				result.Failures = append(result.Failures, newResourceError(sourceID, "issue_comments", err))
			}
		}
		emitProgress(req.ProgressChan, ProgressEvent{Collection: "issue_comments", Phase: "aggregate_page", Page: pageNumber, RecordsListed: len(page.Items), RecordsFetched: len(page.Items), RecordsFailed: len(result.Failures)})
		if page.NextPage <= 0 {
			result.StopReason = "end_of_collection"
			break
		}
		if page.NextPage <= pageNumber {
			result.Failures = append(result.Failures, newResourceError("issue-comment:*", "issue_comment_reconciliation", issueCommentReconciliationError(fmt.Sprintf("aggregate pagination did not advance from page %d", pageNumber))))
			result.TraversalStatus = "partial"
			result.StopReason = "pagination_invalid"
			break
		}
		pageNumber = page.NextPage
	}
	if result.TraversalStatus == "bounded" {
		result.FailureCount = len(result.Failures)
		if result.FailureCount > 0 {
			result.TraversalStatus = "partial"
			result.StopReason = "reconciliation_failed"
		}
		if summaryErr := s.attachRepositoryIssueCommentSummary(ctx, result, req.RepoID, len(pending)); summaryErr != nil {
			return result, summaryErr
		}
		if result.FailureCount > 0 {
			return result, &PartialSyncError{Errors: result.Failures, SuccessCount: 0, FailureCount: result.FailureCount}
		}
		return result, nil
	}
	if expectedTotal > 0 && result.RecordsListed != expectedTotal {
		result.Failures = append(result.Failures, newResourceError("issue-comment:*", "issue_comment_reconciliation", issueCommentReconciliationError(fmt.Sprintf("aggregate listed %d comments, expected total_count %d", result.RecordsListed, expectedTotal))))
	}
	if len(result.Failures) > 0 {
		result.TraversalStatus = "partial"
		result.StopReason = "reconciliation_failed"
		result.FailureCount = len(result.Failures)
		if summaryErr := s.attachRepositoryIssueCommentSummary(ctx, result, req.RepoID, len(pending)); summaryErr != nil {
			return result, summaryErr
		}
		return result, &PartialSyncError{Errors: result.Failures, SuccessCount: 0, FailureCount: result.FailureCount}
	}
	for _, item := range pending {
		commentIDs := make([]string, 0, len(seen[item.SourceID]))
		for id := range seen[item.SourceID] {
			commentIDs = append(commentIDs, id)
		}
		sort.Strings(commentIDs)
		if item.ExpectedCount >= 0 && len(commentIDs) != item.ExpectedCount {
			err := issueCommentReconciliationError(fmt.Sprintf("aggregate comments for issue %d = %d, expected %d", item.IssueNumber, len(commentIDs), item.ExpectedCount))
			result.Failures = append(result.Failures, newResourceError(item.RemoteID, "issue_comment_reconciliation", err))
			item.Attempts++
			item.LastAttemptAt = s.now().UTC()
			item.UpdatedAt = item.LastAttemptAt
			item.LastErrorClass = "live_graph_invalid"
			_ = s.store.UpsertIssueCommentSync(ctx, item)
			continue
		}
		if err := s.store.ReconcileRecordComments(ctx, req.RepoID, item.SourceID, commentIDs); err != nil {
			result.Failures = append(result.Failures, newResourceError(item.RemoteID, "issue_comments", err))
			continue
		}
		commentSourceIDs := make([]string, 0, len(seenSources[item.SourceID]))
		for id := range seenSources[item.SourceID] {
			commentSourceIDs = append(commentSourceIDs, id)
		}
		sort.Strings(commentSourceIDs)
		if err := s.store.ReconcileChildSources(ctx, req.RepoID, item.SourceID, "issue_comment", commentSourceIDs); err != nil {
			result.Failures = append(result.Failures, newResourceError(item.RemoteID, "issue_comments", err))
			continue
		}
		now := s.now().UTC()
		item.Status = "complete"
		item.ExpectedCount = len(commentIDs)
		item.Attempts++
		item.LastErrorClass = ""
		item.RetryAfter = ""
		item.LastAttemptAt = now
		item.UpdatedAt = now
		if err := s.store.UpsertIssueCommentSync(ctx, item); err != nil {
			result.Failures = append(result.Failures, newResourceError(item.RemoteID, "issue_comments", err))
			continue
		}
		source, err := s.store.GetSourceScoped(ctx, req.RepoID, item.SourceID)
		if err != nil {
			result.Failures = append(result.Failures, newResourceError(item.RemoteID, "issue_comments", err))
			continue
		}
		result.Results = append(result.Results, SyncResult{Status: "succeeded", Freshness: string(FreshnessFresh), Counts: SyncCounts{Fetched: 1, Listed: len(commentIDs)}, Record: sourceSummary(source), GeneratedAt: now, StartedAt: now, CompletedAt: now})
	}
	result.SuccessCount = len(result.Results)
	result.FailureCount = len(result.Failures)
	if result.FailureCount > 0 {
		result.TraversalStatus = "partial"
		result.StopReason = "reconciliation_failed"
	}
	if summaryErr := s.attachRepositoryIssueCommentSummary(ctx, result, req.RepoID, len(pending)); summaryErr != nil {
		return result, summaryErr
	}
	if result.FailureCount > 0 {
		return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount}
	}
	return result, nil
}

func (s *Service) attachRepositoryIssueCommentSummary(ctx context.Context, result *SyncResourcesResult, repoID string, attempted int) error {
	if err := s.attachIssueCommentQueueSummary(ctx, result, repoID, "aggregate_backfill"); err != nil {
		return err
	}
	result.IssueComments.Strategy = "repository_aggregate"
	result.IssueComments.Attempted = attempted
	result.IssueComments.Drained = result.SuccessCount
	result.IssueComments.AggregateRequests = result.PagesListed
	result.IssueComments.CommentsListed = result.RecordsListed
	for _, failure := range result.Failures {
		if failure.RemoteType == "issue_comment_reconciliation" {
			result.IssueComments.Unreconciled++
		}
	}
	if result.TraversalStatus == "complete" && result.FailureCount == 0 {
		avoided := attempted - result.PagesListed
		if avoided > 0 {
			result.IssueComments.ParentRequestsAvoided = avoided
		}
	}
	return nil
}

func reconcileRepositoryIssueCommentParent(comment gitcode.Comment, byProviderID map[string]cache.IssueCommentSync, byNumber map[int]cache.IssueCommentSync) (cache.IssueCommentSync, error) {
	provider, providerOK := byProviderID[strings.TrimSpace(comment.IssueID)]
	number, numberOK := byNumber[comment.IssueNumber]
	if providerOK && numberOK && provider.SourceID != number.SourceID {
		return cache.IssueCommentSync{}, fmt.Errorf("comment %s parent ids resolve to different cached issues", comment.ID)
	}
	if providerOK {
		return provider, nil
	}
	if numberOK {
		return number, nil
	}
	return cache.IssueCommentSync{}, fmt.Errorf("comment %s parent issue is not present in the completed parent cache", comment.ID)
}

func cachedIssueComment(comment gitcode.Comment, item cache.IssueCommentSync, now time.Time) (cache.RecordComment, error) {
	commentID := strings.TrimSpace(comment.ID)
	if commentID == "" {
		return cache.RecordComment{}, ErrSyncFailure{Mode: "live_graph_invalid", Cause: ErrInvalidQuery{Field: "live_graph", Message: "comment missing provider id"}}
	}
	updated := comment.UpdatedAt.UTC()
	if updated.IsZero() {
		updated = now
	}
	created := comment.CreatedAt.UTC()
	if created.IsZero() {
		created = updated
	}
	return cache.RecordComment{RepoID: item.RepoID, RecordID: item.SourceID, CommentID: commentID, Author: comment.Author, Body: comment.Body, ContentHash: contentHash(commentID, comment.Author, comment.Body), RemoteRevision: contentHash(updated), CreatedAt: created, UpdatedAt: updated}, nil
}

func (s *Service) upsertIssueCommentProjection(ctx context.Context, item cache.IssueCommentSync, comment cache.RecordComment) (string, error) {
	commentID := strings.TrimSpace(comment.CommentID)
	if commentID == "" {
		return "", s.liveGraphError("comment missing provider id")
	}
	remoteID := issueCommentRemoteID(item.IssueNumber, commentID)
	stableID := s.resolveOrFallback(ctx, item.RepoID, "issue_comment", remoteID, issueCommentStableID(item.IssueNumber, commentID))
	if err := s.guardRemoteAlias(ctx, item.RepoID, "issue_comment", remoteID, stableID); err != nil {
		return "", err
	}
	now := s.now().UTC()
	updated := comment.UpdatedAt.UTC()
	if updated.IsZero() {
		updated = now
	}
	created := comment.CreatedAt.UTC()
	if created.IsZero() {
		created = updated
	}
	hash := comment.ContentHash
	if hash == "" {
		hash = contentHash(commentID, comment.Author, comment.Body)
	}
	revision := comment.RemoteRevision
	if revision == "" {
		revision = contentHash(updated)
	}
	source := cache.Source{
		RepoID:      item.RepoID,
		ID:          stableID,
		Kind:        "issue_comment",
		Path:        fmt.Sprintf("issues/%d/comments/%s.md", item.IssueNumber, safeIDPart(commentID)),
		Title:       fmt.Sprintf("Issue #%d comment %s", item.IssueNumber, commentID),
		Body:        comment.Body,
		Status:      "current",
		ContentHash: hash,
		CreatedAt:   created,
		UpdatedAt:   updated,
	}
	graph := cache.SourceGraph{
		Source:        source,
		Identities:    []cache.Identity{{RepoID: item.RepoID, SourceID: stableID, AliasType: "issue_comment", Alias: remoteID, Remote: cache.RemoteAlias{Type: "issue_comment", ID: remoteID}}},
		Links:         []cache.Link{{RepoID: item.RepoID, SourceID: stableID, TargetID: item.SourceID, Kind: "parent", Text: "issue"}},
		Chunks:        chunksForSource(source),
		ReplaceChunks: true,
		SyncStatus:    &cache.SyncStatus{RepoID: item.RepoID, SourceID: stableID, RemoteType: "issue_comment", RemoteID: remoteID, RemoteRevision: revision, Status: "fresh", LastFetchedAt: now},
	}
	if err := s.store.UpsertSyncGraph(ctx, s.syncGraphFromSourceGraph(item.RepoID, graph)); err != nil {
		return "", err
	}
	return stableID, nil
}

func (s *Service) projectIssueComments(ctx context.Context, item cache.IssueCommentSync, comments []cache.RecordComment) error {
	sourceIDs := make([]string, 0, len(comments))
	for _, comment := range comments {
		sourceID, err := s.upsertIssueCommentProjection(ctx, item, comment)
		if err != nil {
			return err
		}
		sourceIDs = append(sourceIDs, sourceID)
	}
	sort.Strings(sourceIDs)
	return s.store.ReconcileChildSources(ctx, item.RepoID, item.SourceID, "issue_comment", sourceIDs)
}

func repositoryIssueCommentsUnsupported(err error) bool {
	var capability gitcode.ErrUnsupportedCapability
	return errors.As(err, &capability) && capability.CapabilityKey == "repository_issue_comments"
}

func issueCommentReconciliationError(message string) error {
	return ErrSyncFailure{
		Mode:           "live_graph_invalid",
		RecoveryAction: "refresh issue parents with `sync --issues`, then retry `sync --issue-comments`",
		Cause:          ErrInvalidQuery{Field: "live_graph", Message: message},
	}
}

func (s *Service) seedLegacyIssueCommentQueue(ctx context.Context, repoID string) error {
	records, err := s.store.ListRecords(ctx, cache.RecordFilter{RepoID: repoID, Type: "issue"})
	if err != nil {
		return err
	}
	for _, record := range records {
		if _, ok, err := s.store.GetIssueCommentSync(ctx, repoID, record.ID); err != nil {
			return err
		} else if ok {
			continue
		}
		number, err := strconv.Atoi(record.RemoteID)
		if err != nil || number <= 0 {
			continue
		}
		providerID := ""
		identities, err := s.store.GetIdentityMapScoped(ctx, repoID, record.ID)
		if err != nil {
			return err
		}
		for _, identity := range identities {
			if identity.AliasType == "gitcode_issue_id" {
				providerID = identity.Alias
				break
			}
		}
		if err := s.store.UpsertIssueCommentSync(ctx, cache.IssueCommentSync{RepoID: repoID, SourceID: record.ID, IssueNumber: number, RemoteID: record.RemoteID, ProviderID: providerID, RemoteRevision: record.RemoteRevision, ExpectedCount: -1, Status: "pending", UpdatedAt: s.now().UTC()}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) syncIssueCommentItem(ctx context.Context, route RepositoryRoute, item cache.IssueCommentSync) (SyncResult, error) {
	comments, err := s.client.ListIssueComments(ctx, gitcode.IssueRequest{Owner: route.Owner, Repo: route.Name, Number: item.IssueNumber, KnownRemoteAlias: true, RemoteAlias: item.RemoteID})
	if err != nil {
		return SyncResult{}, err
	}
	now := s.now().UTC()
	cached := make([]cache.RecordComment, 0, len(comments.Items))
	for _, comment := range comments.Items {
		if s.syncProviderMode() == gitcode.ProviderModeLive && !s.liveCommentParentReconciles(comment, item.RemoteID, item.ProviderID) {
			return SyncResult{}, s.liveGraphError("comment parent issue id is unreconciled")
		}
		recordComment, err := cachedIssueComment(comment, item, now)
		if err != nil {
			return SyncResult{}, err
		}
		cached = append(cached, recordComment)
	}
	if err := s.store.ReplaceRecordComments(ctx, item.RepoID, item.SourceID, cached); err != nil {
		return SyncResult{}, err
	}
	if err := s.projectIssueComments(ctx, item, cached); err != nil {
		return SyncResult{}, err
	}
	item.Status = "complete"
	item.ExpectedCount = len(cached)
	item.Attempts++
	item.LastErrorClass = ""
	item.RetryAfter = ""
	item.LastAttemptAt = now
	item.UpdatedAt = now
	if err := s.store.UpsertIssueCommentSync(ctx, item); err != nil {
		return SyncResult{}, err
	}
	source, err := s.store.GetSourceScoped(ctx, item.RepoID, item.SourceID)
	if err != nil {
		return SyncResult{}, err
	}
	return SyncResult{Status: "succeeded", Freshness: string(FreshnessFresh), Counts: SyncCounts{Fetched: 1, FetchedDetail: 1}, Record: sourceSummary(source), GeneratedAt: now, StartedAt: now, CompletedAt: now}, nil
}

func (s *Service) attachIssueCommentQueueSummary(ctx context.Context, result *SyncResourcesResult, repoID, phase string) error {
	summary, err := s.store.IssueCommentSyncSummary(ctx, repoID)
	if err != nil {
		return err
	}
	queue := &IssueCommentQueueSummary{Phase: phase, Pending: summary.Pending, Deferred: summary.Deferred, Complete: summary.Complete, Total: summary.Total}
	if result != nil {
		for _, item := range result.Results {
			queue.Attempted += item.Counts.FetchedDetail
			if item.Status == "succeeded" && item.Counts.FetchedDetail > 0 {
				queue.Drained++
			}
		}
		result.IssueComments = queue
	}
	return nil
}

func syncResultsDeferredCount(results []SyncResult) int {
	total := 0
	for _, result := range results {
		total += result.Counts.Deferred
	}
	return total
}

func (s *Service) BulkSyncPullRequests(ctx context.Context, req BulkSyncRequest) (*SyncResourcesResult, error) {
	if err := ctx.Err(); err != nil {
		if req.Bounds != nil {
			diag := SyncDiagnosticCancelled
			if errors.Is(err, context.DeadlineExceeded) {
				diag = SyncDiagnosticTimeout
			}
			return nil, &PartialSyncError{Errors: nil, SuccessCount: 0, FailureCount: 0, Diagnostic: diag}
		}
		return nil, err
	}
	ctx = withBulkRateLimitProgress(ctx, bulkProgressChan(req))
	repoID, err := s.requireRepo(ctx, req.RepoID, "bulk-sync-pulls")
	if err != nil {
		return nil, err
	}
	req.RepoID = repoID
	s.ensureBulkIdempotencyKey(&req, "pulls")
	if err := s.validateRepoScope(ctx, repoID, "pull_request"); err != nil {
		return nil, err
	}
	route, err := s.BuildAdapterRoute(ctx, repoID, RepositoryScopeIssues)
	if err != nil {
		return nil, err
	}

	if req.Bounds == nil {
		page, err := s.client.ListPRs(ctx, gitcode.PRListRequest{Owner: route.Owner, Repo: route.Name, State: "all", OrderBy: "updated_at", Direction: "desc", Page: req.Page, PerPage: req.PerPage})
		if err != nil {
			return bulkSyncFailureResult(s.normalizeSyncFailure(err, SyncRequest{RepoID: req.RepoID, RemoteAlias: "pull_request:*"}, "pull_request", "*"), "pull_request:*", "pull_request")
		}
		result := &SyncResourcesResult{Results: make([]SyncResult, 0, len(page.Items)), Failures: make([]ResourceError, 0)}
		beforeCount := len(result.Results)
		s.stagePullRequestPage(ctx, req, page.Items, result)
		emitProgress(req.ProgressChan, ProgressEvent{Collection: "pulls", Page: firstNonZeroInt(req.Page, 1), RecordsFetched: len(result.Results) - beforeCount})
		result.SuccessCount = len(result.Results)
		result.FailureCount = len(result.Failures)
		if result.FailureCount > 0 {
			return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount}
		}
		return result, nil
	}

	return s.bulkSyncPullRequestsBounded(ctx, req, route)
}

func (s *Service) bulkSyncPullRequestsBounded(ctx context.Context, req BulkSyncRequest, route RepositoryRoute) (*SyncResourcesResult, error) {
	bounds := req.Bounds
	result := &SyncResourcesResult{Results: make([]SyncResult, 0), Failures: make([]ResourceError, 0), Ordering: syncOrderingUpdatedAtDesc}
	watermark, watermarkOK, err := s.completeSyncWatermark(ctx, req.RepoID, "pull_request")
	if err != nil {
		return bulkSyncFailureResult(err, "pull_request:*", "pull_request")
	}
	setWatermarkSummary(result, watermark, watermarkOK)
	var observed syncHighWatermark
	currentPage := req.Page
	if currentPage < 1 {
		currentPage = 1
	}
	perPage := req.PerPage
	if perPage < 1 {
		perPage = 10
	}
	totalRequested := bounds.MaxRecords
	if totalRequested <= 0 && bounds.MaxPages > 0 {
		totalRequested = bounds.MaxPages * perPage
	}

	for pageNum := 0; ; pageNum++ {
		if ctx.Err() != nil {
			diag := SyncDiagnosticCancelled
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				diag = SyncDiagnosticTimeout
				result.TraversalStatus = "timeout"
			} else {
				result.TraversalStatus = "cancelled"
			}
			result.SuccessCount = len(result.Results)
			result.FailureCount = len(result.Failures)
			s.recordSyncFrontierBestEffort(ctx, req.RepoID, "pull_request", result, observed)
			return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount, Diagnostic: diag, TotalRequested: totalRequested}
		}
		if bounds.MaxPages > 0 && pageNum >= bounds.MaxPages {
			result.StopReason = "max_pages"
			result.TraversalStatus = "bounded"
			break
		}
		if bounds.MaxRecords > 0 && len(result.Results) >= bounds.MaxRecords {
			result.StopReason = "max_records"
			result.TraversalStatus = "bounded"
			break
		}
		page, err := s.client.ListPRs(ctx, gitcode.PRListRequest{Owner: route.Owner, Repo: route.Name, State: "all", OrderBy: "updated_at", Direction: "desc", Page: currentPage, PerPage: perPage})
		if err != nil {
			normalized := s.normalizeSyncFailure(err, SyncRequest{RepoID: req.RepoID, RemoteAlias: "pull_request:*"}, "pull_request", "*")
			result.Failures = append(result.Failures, newResourceErrorWithMessage("pull_request:*", "pull_request", normalized, err.Error()))
			result.SuccessCount = len(result.Results)
			result.FailureCount = len(result.Failures)
			result.TraversalStatus = "partial"
			s.recordSyncFrontierBestEffort(ctx, req.RepoID, "pull_request", result, observed)
			return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount, TotalRequested: totalRequested}
		}
		result.PagesListed++
		result.RecordsListed += len(page.Items)
		observePullRequestHighWatermark(&observed, page.Items)
		items, stopByWatermark, skippedByWatermark := filterPullRequestsByCompleteWatermark(page.Items, watermark, watermarkOK)
		result.SkippedByWatermark += skippedByWatermark
		if bounds.MaxRecords > 0 {
			remaining := bounds.MaxRecords - len(result.Results)
			if remaining <= 0 {
				result.StopReason = "max_records"
				result.TraversalStatus = "bounded"
				break
			}
			if len(items) > remaining {
				items = items[:remaining]
			}
		}
		beforeCount := len(result.Results)
		s.stagePullRequestPage(ctx, req, items, result)
		recordsFetched := len(result.Results) - beforeCount
		emitProgress(bounds.ProgressChan, ProgressEvent{Collection: "pulls", Page: currentPage, RecordsFetched: recordsFetched})
		if stopByWatermark {
			result.StopReason = "watermark"
			result.TraversalStatus = "complete"
			result.WatermarkStatus = "used"
			result.WatermarkReason = "previous_complete_frontier"
			break
		}
		if len(page.Items) < perPage {
			result.StopReason = "end_of_collection"
			result.TraversalStatus = "complete"
			break
		}
		currentPage++
	}
	result.SuccessCount = len(result.Results)
	result.FailureCount = len(result.Failures)
	if result.FailureCount > 0 {
		result.TraversalStatus = "partial"
		s.recordSyncFrontierBestEffort(ctx, req.RepoID, "pull_request", result, observed)
		return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount}
	}
	if err := s.recordSyncFrontier(ctx, req.RepoID, "pull_request", result, observed); err != nil {
		return bulkSyncFailureResult(err, "pull_request:*", "pull_request")
	}
	return result, nil
}

func (s *Service) stagePullRequestPage(ctx context.Context, req BulkSyncRequest, items []gitcode.PullRequest, result *SyncResourcesResult) {
	for _, pr := range items {
		remoteID := strconv.Itoa(pr.Number)
		syncReq := SyncRequest{RepoID: req.RepoID, AliasType: "pull_request", AliasID: remoteID, IdempotencyKey: scopedBulkSyncKey(req.IdempotencyKey, "pull_request", remoteID), MaxAttempts: req.MaxAttempts, MaxSize: req.MaxSize}
		graph, counts, err := s.stagePullRequest(ctx, syncReq, "pull_request", remoteID, pr)
		if err != nil {
			result.Failures = append(result.Failures, newResourceError(remoteID, "pull_request", err))
			continue
		}
		counts.Listed = 1
		completedAt := s.now().UTC()
		eventID := syncEventID(syncReq.IdempotencyKey)
		zeroDelta := counts.Fetched > 0 && counts.Skipped == counts.Fetched && counts.Updated == 0 && counts.Inserted == 0 && counts.Conflicts == 0
		graph.SyncEvents = append(graph.SyncEvents, cache.SyncEvent{ID: eventID, SourceID: graph.Source.ID, RemoteType: "pull_request", RemoteID: remoteID, RemoteRevision: graph.SyncStatus.RemoteRevision, Status: "succeeded", IdempotencyKey: syncReq.IdempotencyKey, Message: syncEventMessage(counts), CreatedAt: completedAt, StartedAt: completedAt, CompletedAt: completedAt, ZeroDelta: zeroDelta})
		if err := s.store.UpsertSyncGraph(ctx, s.syncGraphFromSourceGraph(req.RepoID, graph)); err != nil {
			result.Failures = append(result.Failures, newResourceError(remoteID, "pull_request", err))
			continue
		}
		stored, err := s.store.GetSourceScoped(ctx, req.RepoID, graph.Source.ID)
		if err != nil {
			result.Failures = append(result.Failures, newResourceError(remoteID, "pull_request", err))
			continue
		}
		result.Results = append(result.Results, SyncResult{IdempotencyKey: syncReq.IdempotencyKey, Status: "succeeded", Counts: counts, SyncEventID: eventID, Freshness: string(FreshnessFresh), Record: sourceSummary(stored), GeneratedAt: completedAt, StartedAt: completedAt, CompletedAt: completedAt, ZeroDelta: zeroDelta})
	}
}

const (
	syncOrderingUpdatedAtDesc = "updated_at_desc"
	syncFilterStateAll        = "state=all"
)

type syncHighWatermark struct {
	UpdatedAt time.Time
	RemoteID  string
	Number    int
}

func (s *Service) completeSyncWatermark(ctx context.Context, repoID, remoteType string) (cache.SyncFrontier, bool, error) {
	frontier, ok, err := s.store.GetSyncFrontier(ctx, repoID, remoteType, syncOrderingUpdatedAtDesc, syncFilterStateAll)
	if err != nil || !ok {
		return cache.SyncFrontier{}, false, err
	}
	return frontier, frontier.Status == "complete" && !frontier.HighUpdatedAt.IsZero(), nil
}

func setWatermarkSummary(result *SyncResourcesResult, frontier cache.SyncFrontier, ok bool) {
	if ok {
		result.WatermarkStatus = "eligible"
		result.WatermarkReason = "previous_complete_frontier"
		return
	}
	result.WatermarkStatus = "disabled"
	if frontier.Status != "" {
		result.WatermarkReason = "previous_frontier_" + frontier.Status
		return
	}
	result.WatermarkReason = "no_complete_frontier_metadata"
}

func observeIssueHighWatermark(mark *syncHighWatermark, items []gitcode.IssueSummary) {
	for _, item := range items {
		observeHighWatermark(mark, item.UpdatedAt, strconv.Itoa(item.Number), item.Number)
	}
}

func observePullRequestHighWatermark(mark *syncHighWatermark, items []gitcode.PullRequest) {
	for _, item := range items {
		observeHighWatermark(mark, item.UpdatedAt, strconv.Itoa(item.Number), item.Number)
	}
}

func observeHighWatermark(mark *syncHighWatermark, updatedAt time.Time, remoteID string, number int) {
	updatedAt = updatedAt.UTC()
	if updatedAt.IsZero() {
		return
	}
	if mark.UpdatedAt.IsZero() || updatedAt.After(mark.UpdatedAt) || (updatedAt.Equal(mark.UpdatedAt) && number > mark.Number) {
		mark.UpdatedAt = updatedAt
		mark.RemoteID = remoteID
		mark.Number = number
	}
}

func filterIssueSummariesByCompleteWatermark(items []gitcode.IssueSummary, frontier cache.SyncFrontier, ok bool) ([]gitcode.IssueSummary, bool, int) {
	if !ok {
		return items, false, 0
	}
	out := make([]gitcode.IssueSummary, 0, len(items))
	skipped := 0
	stop := false
	for _, item := range items {
		if item.UpdatedAt.IsZero() || !item.UpdatedAt.UTC().Before(frontier.HighUpdatedAt) {
			out = append(out, item)
			continue
		}
		skipped++
		stop = true
	}
	return out, stop, skipped
}

func filterPullRequestsByCompleteWatermark(items []gitcode.PullRequest, frontier cache.SyncFrontier, ok bool) ([]gitcode.PullRequest, bool, int) {
	if !ok {
		return items, false, 0
	}
	out := make([]gitcode.PullRequest, 0, len(items))
	skipped := 0
	stop := false
	for _, item := range items {
		if item.UpdatedAt.IsZero() || !item.UpdatedAt.UTC().Before(frontier.HighUpdatedAt) {
			out = append(out, item)
			continue
		}
		skipped++
		stop = true
	}
	return out, stop, skipped
}

func (s *Service) recordSyncFrontier(ctx context.Context, repoID, remoteType string, result *SyncResourcesResult, high syncHighWatermark) error {
	status := result.TraversalStatus
	if result.StopReason == "watermark" || result.StopReason == "end_of_collection" {
		status = "complete"
	}
	if status == "" {
		status = "partial"
	}
	previous, ok, err := s.store.GetSyncFrontier(ctx, repoID, remoteType, syncOrderingUpdatedAtDesc, syncFilterStateAll)
	if err != nil {
		return err
	}
	if ok && previous.Status == "complete" && status != "complete" {
		// Keep the last proven full-corpus watermark while a bounded head pass is
		// still working toward it. Maintenance stores the in-progress checkpoint
		// and candidate high watermark separately.
		return nil
	}
	return s.store.UpsertSyncFrontier(ctx, cache.SyncFrontier{
		RepoID:        repoID,
		RemoteType:    remoteType,
		Ordering:      syncOrderingUpdatedAtDesc,
		FilterKey:     syncFilterStateAll,
		Status:        status,
		HighUpdatedAt: high.UpdatedAt,
		HighRemoteID:  high.RemoteID,
		HighNumber:    high.Number,
		StopReason:    result.StopReason,
		PagesListed:   result.PagesListed,
		RecordsListed: result.RecordsListed,
		UpdatedAt:     s.now().UTC(),
	})
}

func (s *Service) recordSyncFrontierBestEffort(ctx context.Context, repoID, remoteType string, result *SyncResourcesResult, high syncHighWatermark) {
	if ctx.Err() != nil {
		ctx = context.WithoutCancel(ctx)
	}
	_ = s.recordSyncFrontier(ctx, repoID, remoteType, result, high)
}

func (s *Service) BulkSyncPRComments(ctx context.Context, req BulkSyncRequest) (*SyncResourcesResult, error) {
	remoteAlias := strings.TrimSpace(req.RemoteAlias)
	targetedPRNumber := 0
	if remoteAlias != "" {
		if req.Page != 0 || req.PerPage != 0 || (req.Bounds != nil && (req.Bounds.MaxPages != 0 || req.Bounds.MaxRecords != 0)) {
			return nil, ErrInvalidQuery{Field: "bounds", Message: "targeted PR comment sync does not accept page, per_page, max_pages, or max_records"}
		}
		var err error
		targetedPRNumber, err = targetedPRCommentNumber(remoteAlias)
		if err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		if req.Bounds != nil {
			diag := SyncDiagnosticCancelled
			if errors.Is(err, context.DeadlineExceeded) {
				diag = SyncDiagnosticTimeout
			}
			return nil, &PartialSyncError{Errors: nil, SuccessCount: 0, FailureCount: 0, Diagnostic: diag}
		}
		return nil, err
	}
	ctx = withBulkRateLimitProgress(ctx, bulkProgressChan(req))
	repoID, err := s.requireRepo(ctx, req.RepoID, "bulk-sync-pr-comments")
	if err != nil {
		return nil, err
	}
	req.RepoID = repoID
	s.ensureBulkIdempotencyKey(&req, "pr_comments")
	if err := s.validateRepoScope(ctx, repoID, "pull_request"); err != nil {
		return nil, err
	}
	route, err := s.BuildAdapterRoute(ctx, repoID, RepositoryScopeIssues)
	if err != nil {
		return nil, err
	}
	var prSources []cache.Source
	if targetedPRNumber > 0 {
		identity, err := s.store.ResolveAliasScoped(ctx, repoID, cache.RemoteAlias{Type: "pull_request", ID: strconv.Itoa(targetedPRNumber)})
		if err != nil {
			if isCacheNotFound(err) {
				return nil, ErrInvalidQuery{Field: "remote_alias", Message: fmt.Sprintf("pull request %d is not cached; sync --pulls --input pr:%d first", targetedPRNumber, targetedPRNumber)}
			}
			return nil, normalizeError(err, "pull_request", strconv.Itoa(targetedPRNumber))
		}
		source, err := s.store.GetSourceScoped(ctx, repoID, identity.SourceID)
		if err != nil {
			if isCacheNotFound(err) {
				return nil, ErrInvalidQuery{Field: "remote_alias", Message: fmt.Sprintf("pull request %d is not cached; sync --pulls --input pr:%d first", targetedPRNumber, targetedPRNumber)}
			}
			return nil, normalizeError(err, "pull_request", strconv.Itoa(targetedPRNumber))
		}
		if source.Kind != "pull_request" {
			return nil, ErrInvalidQuery{Field: "remote_alias", Message: fmt.Sprintf("pull_request:%d resolves to cached %s %s, not a pull request", targetedPRNumber, source.Kind, source.ID)}
		}
		prSources = []cache.Source{source}
	} else {
		prSources, err = s.store.ListSources(ctx, cache.SourceFilter{RepoID: repoID, Kind: "pull_request"})
		if err != nil {
			if isCacheNotFound(err) {
				return &SyncResourcesResult{Results: []SyncResult{}, Failures: []ResourceError{}}, nil
			}
			return nil, normalizeError(err, "sources", repoID)
		}
	}
	sort.SliceStable(prSources, func(i, j int) bool { return prSources[i].ID < prSources[j].ID })
	start := 0
	if req.Bounds != nil && req.Page > 1 {
		start = req.Page - 1
	}
	if start > len(prSources) {
		start = len(prSources)
	}
	end := len(prSources)
	bounded := false
	if req.Bounds != nil && req.Bounds.MaxPages > 0 && end > start+req.Bounds.MaxPages {
		end = start + req.Bounds.MaxPages
		bounded = true
	}
	prSources = prSources[start:end]

	result := &SyncResourcesResult{Results: make([]SyncResult, 0), Failures: make([]ResourceError, 0), Ordering: "source_id_asc", PagesListed: len(prSources), RecordsListed: len(prSources), TraversalStatus: "complete", StopReason: "end_of_collection"}
	if bounded {
		result.TraversalStatus = "bounded"
		result.StopReason = "max_pages"
	}
	totalRequested := 0
	if req.Bounds != nil {
		totalRequested = req.Bounds.MaxRecords
	}
	for idx, source := range prSources {
		if ctx.Err() != nil {
			diag := SyncDiagnosticCancelled
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				diag = SyncDiagnosticTimeout
			}
			result.SuccessCount = len(result.Results)
			result.FailureCount = len(result.Failures)
			return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount, Diagnostic: diag, TotalRequested: totalRequested}
		}
		if req.Bounds != nil && req.Bounds.MaxRecords > 0 && len(result.Results) >= req.Bounds.MaxRecords {
			break
		}
		prNumber := targetedPRNumber
		if prNumber == 0 {
			var ok bool
			prNumber, ok = pullRequestNumberFromSource(source)
			if !ok {
				err := ErrInvalidQuery{Field: "pull_request", Message: "cached pull request has no numeric remote alias"}
				result.Failures = append(result.Failures, newResourceErrorWithMessage(source.ID, "pr_comment", err, "cached pull request has no numeric remote alias"))
				continue
			}
		}
		if prNumber <= 0 {
			err := ErrInvalidQuery{Field: "pull_request", Message: "cached pull request has no numeric remote alias"}
			result.Failures = append(result.Failures, newResourceErrorWithMessage(source.ID, "pr_comment", err, "cached pull request has no numeric remote alias"))
			continue
		}
		page, err := s.client.ListPRComments(ctx, gitcode.PRRequest{Owner: route.Owner, Repo: route.Name, Number: prNumber})
		if err != nil {
			normalized := s.normalizeSyncFailure(err, SyncRequest{RepoID: req.RepoID, RemoteAlias: fmt.Sprintf("pr_comment:%d:*", prNumber)}, "pr_comment", strconv.Itoa(prNumber))
			result.Failures = append(result.Failures, newResourceErrorWithMessage(fmt.Sprintf("PR-%d", prNumber), "pr_comment", normalized, err.Error()))
			continue
		}
		items := page.Items
		if req.Bounds != nil && req.Bounds.MaxRecords > 0 {
			remaining := req.Bounds.MaxRecords - len(result.Results)
			if remaining <= 0 {
				break
			}
			if len(items) > remaining {
				items = items[:remaining]
			}
		}
		beforeCount := len(result.Results)
		s.stagePRCommentPage(ctx, req, prNumber, source.ID, items, result)
		emitProgress(bulkProgressChan(req), ProgressEvent{Collection: "pr_comments", Page: idx + 1, RecordsFetched: len(result.Results) - beforeCount})
	}
	result.SuccessCount = len(result.Results)
	result.FailureCount = len(result.Failures)
	if result.FailureCount > 0 {
		result.TraversalStatus = "partial"
		result.StopReason = "provider_error"
		return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount, TotalRequested: totalRequested}
	}
	return result, nil
}

func targetedPRCommentNumber(alias string) (int, error) {
	remoteType, remoteID, ok := strings.Cut(strings.TrimSpace(alias), ":")
	if !ok {
		return 0, ErrInvalidQuery{Field: "remote_alias", Message: "targeted PR comment sync requires pr:N"}
	}
	switch strings.ToLower(strings.TrimSpace(remoteType)) {
	case "pr", "pull", "pulls", "pull_request":
	default:
		return 0, ErrInvalidQuery{Field: "remote_alias", Message: "targeted PR comment sync requires a pull request alias such as pr:N"}
	}
	number, err := strconv.Atoi(strings.TrimSpace(remoteID))
	if err != nil || number <= 0 {
		return 0, ErrInvalidQuery{Field: "remote_alias", Message: "targeted PR comment sync requires a positive pull request number"}
	}
	return number, nil
}

func (s *Service) ListPRDiscussions(ctx context.Context, req PRDiscussionRequest) (PRDiscussionsResult, error) {
	if strings.TrimSpace(req.RepoID) == "" {
		return PRDiscussionsResult{}, ErrInvalidQuery{Field: "repo_id", Message: "repo_id is required"}
	}
	if req.Number <= 0 {
		return PRDiscussionsResult{}, ErrInvalidQuery{Field: "number", Message: "positive pull request number is required"}
	}
	repoID, err := s.requireRepo(ctx, req.RepoID, "list-pr-discussions")
	if err != nil {
		return PRDiscussionsResult{}, err
	}
	metadata, err := s.store.ListPRReviewComments(ctx, cache.PRReviewCommentFilter{RepoID: repoID, PRNumber: req.Number})
	if err != nil {
		return PRDiscussionsResult{}, normalizeError(err, "pr_review_comments", repoID)
	}
	positions, err := s.store.ListPRReviewPositions(ctx, cache.PRReviewPositionFilter{RepoID: repoID, PRNumber: req.Number})
	if err != nil {
		return PRDiscussionsResult{}, normalizeError(err, "pr_review_positions", repoID)
	}
	positionsByComment := map[string][]PRReviewPosition{}
	for _, position := range positions {
		positionsByComment[position.CommentID] = append(positionsByComment[position.CommentID], prReviewPositionFromCache(position))
	}
	metadataByComment := make(map[string]cache.PRReviewComment, len(metadata))
	for _, meta := range metadata {
		metadataByComment[meta.CommentID] = meta
	}
	result := PRDiscussionsResult{RepoID: repoID, Number: req.Number, UnresolvedOnly: req.UnresolvedOnly, Discussions: []PRDiscussion{}, GeneratedAt: s.now().UTC()}
	groups := map[string]int{}
	for _, meta := range metadata {
		source, err := s.store.GetSourceScoped(ctx, repoID, meta.SourceID)
		if err != nil {
			return PRDiscussionsResult{}, normalizeError(err, meta.SourceID, repoID)
		}
		comment := prReviewCommentFromCache(meta, source)
		comment.Positions = positionsByComment[meta.CommentID]
		groupID := meta.DiscussionID
		if groupID == "" {
			rootID := meta.CommentID
			parentID := meta.ParentID
			providerDiscussionID := ""
			seen := map[string]bool{rootID: true}
			for parentID != "" && !seen[parentID] {
				seen[parentID] = true
				parent, ok := metadataByComment[parentID]
				if !ok {
					break
				}
				rootID = parent.CommentID
				if parent.DiscussionID != "" {
					providerDiscussionID = parent.DiscussionID
					break
				}
				parentID = parent.ParentID
			}
			groupID = providerDiscussionID
			if groupID == "" {
				groupID = "comment:" + rootID
			}
		}
		idx, ok := groups[groupID]
		if !ok {
			kind := discussionKind(comment)
			replyable := kind == "inline" && !strings.HasPrefix(groupID, "comment:")
			replyDiscussionID := ""
			if replyable {
				replyDiscussionID = groupID
			}
			discussion := PRDiscussion{ID: groupID, Replyable: replyable, ReplyDiscussionID: replyDiscussionID, Kind: kind, Resolved: meta.Resolved, Resolvable: meta.Resolvable, Path: meta.Path, Line: meta.Line, StartLine: meta.StartLine, EndLine: meta.EndLine, Position: firstCurrentPosition(comment.Positions), Comments: []PRReviewComment{}}
			if !replyable {
				discussion.ReplyUnavailableReason = "provider discussion id unavailable; refresh PR discussions before replying"
				if kind != "inline" {
					discussion.ReplyUnavailableReason = "general PR comments do not expose an inline discussion reply target"
				}
			}
			result.Discussions = append(result.Discussions, discussion)
			idx = len(result.Discussions) - 1
			groups[groupID] = idx
		}
		discussion := &result.Discussions[idx]
		if discussion.Kind != "inline" && discussionKind(comment) == "inline" {
			discussion.Kind = "inline"
		}
		if discussion.Path == "" {
			discussion.Path = meta.Path
		}
		if discussion.Line == 0 {
			discussion.Line = meta.Line
		}
		if discussion.StartLine == 0 {
			discussion.StartLine = meta.StartLine
		}
		if discussion.EndLine == 0 {
			discussion.EndLine = meta.EndLine
		}
		if discussion.Position == nil {
			discussion.Position = firstCurrentPosition(comment.Positions)
		}
		discussion.Resolved = mergeDiscussionResolved(discussion.Resolved, meta.Resolved)
		if discussion.Resolvable == nil && meta.Resolvable != nil {
			discussion.Resolvable = meta.Resolvable
		}
		discussion.Comments = append(discussion.Comments, comment)
	}
	if req.UnresolvedOnly {
		filtered := result.Discussions[:0]
		for _, discussion := range result.Discussions {
			if discussion.Resolved == nil || !*discussion.Resolved {
				filtered = append(filtered, discussion)
			}
		}
		result.Discussions = filtered
	}
	return result, nil
}

func progressChan(bounds *SyncBounds) chan<- ProgressEvent {
	if bounds == nil {
		return nil
	}
	return bounds.ProgressChan
}

func bulkProgressChan(req BulkSyncRequest) chan<- ProgressEvent {
	if req.Bounds != nil && req.Bounds.ProgressChan != nil {
		return req.Bounds.ProgressChan
	}
	return req.ProgressChan
}

func withBulkRateLimitProgress(ctx context.Context, ch chan<- ProgressEvent) context.Context {
	if ch == nil {
		return ctx
	}
	return gitcode.WithRateLimitObserver(ctx, func(ev gitcode.RateLimitEvent) {
		emitProgress(ch, progressEventFromRateLimit(ev))
	})
}

func progressEventFromRateLimit(ev gitcode.RateLimitEvent) ProgressEvent {
	progress := ProgressEvent{
		Type:           "rate_limit",
		Endpoint:       ev.Endpoint,
		Attempt:        ev.Attempt,
		RateLimitState: string(ev.Type),
		RateLimitBurst: ev.Burst,
	}
	if ev.Wait >= 0 && (ev.Wait > 0 || ev.RawRetryAfter != "") {
		progress.RetryAfter = ev.Wait.String()
	}
	if !ev.ResumeAt.IsZero() {
		progress.ResumeAt = ev.ResumeAt.UTC().Format(time.RFC3339Nano)
	}
	if ev.RPS > 0 {
		progress.RateLimitRPS = strconv.FormatFloat(ev.RPS, 'f', -1, 64)
	}
	if ev.RawRetryAfter != "" {
		progress.Message = "retry_after=" + ev.RawRetryAfter
	}
	return progress
}

func (s *Service) stagePRCommentPage(ctx context.Context, req BulkSyncRequest, prNumber int, parentSourceID string, items []gitcode.PRComment, result *SyncResourcesResult) {
	for _, comment := range items {
		remoteID := prCommentRemoteID(prNumber, comment.ID)
		syncReq := SyncRequest{RepoID: req.RepoID, AliasType: "pr_comment", AliasID: remoteID, IdempotencyKey: scopedBulkSyncKey(req.IdempotencyKey, "pr_comment", remoteID), MaxAttempts: req.MaxAttempts, MaxSize: req.MaxSize, ParentSourceID: parentSourceID}
		graph, counts, err := s.stagePRComment(ctx, syncReq, "pr_comment", remoteID, prNumber, comment)
		if err != nil {
			result.Failures = append(result.Failures, newResourceError(remoteID, "pr_comment", err))
			continue
		}
		counts.Listed = 1
		completedAt := s.now().UTC()
		eventID := syncEventID(syncReq.IdempotencyKey)
		zeroDelta := counts.Fetched > 0 && counts.Skipped == counts.Fetched && counts.Updated == 0 && counts.Inserted == 0 && counts.Conflicts == 0
		graph.SyncEvents = append(graph.SyncEvents, cache.SyncEvent{ID: eventID, SourceID: graph.Source.ID, RemoteType: "pr_comment", RemoteID: remoteID, RemoteRevision: graph.SyncStatus.RemoteRevision, Status: "succeeded", IdempotencyKey: syncReq.IdempotencyKey, Message: syncEventMessage(counts), CreatedAt: completedAt, StartedAt: completedAt, CompletedAt: completedAt, ZeroDelta: zeroDelta})
		if err := s.store.UpsertSyncGraph(ctx, s.syncGraphFromSourceGraph(req.RepoID, graph)); err != nil {
			result.Failures = append(result.Failures, newResourceError(remoteID, "pr_comment", err))
			continue
		}
		stored, err := s.store.GetSourceScoped(ctx, req.RepoID, graph.Source.ID)
		if err != nil {
			result.Failures = append(result.Failures, newResourceError(remoteID, "pr_comment", err))
			continue
		}
		result.Results = append(result.Results, SyncResult{IdempotencyKey: syncReq.IdempotencyKey, Status: "succeeded", Counts: counts, SyncEventID: eventID, Freshness: string(FreshnessFresh), Record: sourceSummary(stored), GeneratedAt: completedAt, StartedAt: completedAt, CompletedAt: completedAt, ZeroDelta: zeroDelta})
	}
}

func (s *Service) BulkSyncWiki(ctx context.Context, req BulkSyncRequest) (*SyncResourcesResult, error) {
	if err := ctx.Err(); err != nil {
		if req.Bounds != nil {
			diag := SyncDiagnosticCancelled
			if errors.Is(err, context.DeadlineExceeded) {
				diag = SyncDiagnosticTimeout
			}
			return nil, &PartialSyncError{Errors: nil, SuccessCount: 0, FailureCount: 0, Diagnostic: diag}
		}
		return nil, err
	}
	ctx = withBulkRateLimitProgress(ctx, bulkProgressChan(req))
	repoID, err := s.requireRepo(ctx, req.RepoID, "bulk-sync-wiki")
	if err != nil {
		return nil, err
	}
	req.RepoID = repoID
	s.ensureBulkIdempotencyKey(&req, "wiki")
	if err := s.validateRepoScope(ctx, repoID, "wiki"); err != nil {
		return nil, err
	}
	route, err := s.BuildAdapterRoute(ctx, repoID, RepositoryScopeWiki)
	if err != nil {
		return nil, err
	}

	if req.Bounds == nil {
		page, err := s.client.ListWikiPages(ctx, gitcode.WikiListRequest{Owner: route.Owner, Repo: route.Name, Page: req.Page, PerPage: req.PerPage})
		if err != nil {
			normalized := s.normalizeSyncFailure(err, SyncRequest{RepoID: req.RepoID, RemoteAlias: "wiki:*"}, "wiki", "*")
			var sfErr ErrSyncFailure
			if errors.As(normalized, &sfErr) && sfErr.Mode == "empty_wiki" {
				re := newResourceError("wiki:*", "wiki", normalized)
				result := &SyncResourcesResult{Failures: []ResourceError{re}, FailureCount: 1}
				return result, &PartialSyncError{Errors: result.Failures, FailureCount: 1, Diagnostic: SyncDiagnosticEmptyWiki}
			}
			return bulkSyncFailureResult(normalized, "wiki:*", "wiki")
		}
		result := &SyncResourcesResult{Results: make([]SyncResult, 0, len(page.Items)), Failures: make([]ResourceError, 0)}
		beforeCount := len(result.Results)
		s.stageWikiPage(ctx, req, page.Items, result)
		emitProgress(req.ProgressChan, ProgressEvent{Collection: "wiki", Page: firstNonZeroInt(req.Page, 1), RecordsFetched: len(result.Results) - beforeCount})
		result.SuccessCount = len(result.Results)
		result.FailureCount = len(result.Failures)
		if result.FailureCount > 0 {
			return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount}
		}
		return result, nil
	}

	return s.bulkSyncWikiBounded(ctx, req, route)
}

func (s *Service) bulkSyncWikiBounded(ctx context.Context, req BulkSyncRequest, route RepositoryRoute) (*SyncResourcesResult, error) {
	bounds := req.Bounds
	result := &SyncResourcesResult{Results: make([]SyncResult, 0), Failures: make([]ResourceError, 0), Ordering: "path_asc"}

	var wikiBounds *gitcode.WikiBounds
	totalRequested := bounds.MaxRecords
	if totalRequested <= 0 && bounds.MaxPages > 0 {
		perPage := req.PerPage
		if perPage < 1 {
			perPage = 10
		}
		totalRequested = bounds.MaxPages * perPage
	}

	wikiBounds = &gitcode.WikiBounds{
		MaxRecords: totalRequested,
	}
	if bounds.ProgressChan != nil {
		progressCh := make(chan gitcode.WikiProgressEvent, 10)
		wikiBounds.ProgressChan = progressCh
		go func() {
			for ev := range progressCh {
				emitProgress(bounds.ProgressChan, ProgressEvent{Collection: "wiki", Page: 1, RecordsFetched: ev.RecordsFetched})
			}
		}()
	}

	page, err := s.client.ListWikiPages(ctx, gitcode.WikiListRequest{
		Owner:   route.Owner,
		Repo:    route.Name,
		Page:    req.Page,
		PerPage: req.PerPage,
		Bounds:  wikiBounds,
	})
	if bounds.ProgressChan != nil {
		close(wikiBounds.ProgressChan)
	}

	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			diag := SyncDiagnosticCancelled
			if errors.Is(ctxErr, context.DeadlineExceeded) {
				diag = SyncDiagnosticTimeout
			}
			result.SuccessCount = len(result.Results)
			result.FailureCount = len(result.Failures)
			if result.SuccessCount == 0 && result.FailureCount == 0 {
				return nil, &PartialSyncError{Errors: nil, SuccessCount: 0, FailureCount: 0, Diagnostic: diag, TotalRequested: totalRequested}
			}
			return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount, Diagnostic: diag, TotalRequested: totalRequested}
		}
		result.SuccessCount = len(result.Results)
		result.FailureCount = len(result.Failures)
		normalized := s.normalizeSyncFailure(err, SyncRequest{RepoID: req.RepoID, RemoteAlias: "wiki:*"}, "wiki", "*")
		re := newResourceErrorWithMessage("wiki:*", "wiki", normalized, err.Error())
		result.Failures = append(result.Failures, re)
		result.FailureCount++
		var sfErr ErrSyncFailure
		if errors.As(normalized, &sfErr) && sfErr.Mode == "empty_wiki" {
			return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount, Diagnostic: SyncDiagnosticEmptyWiki, TotalRequested: totalRequested}
		}
		return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount, TotalRequested: totalRequested}
	}

	items := page.Items
	originalCount := len(items)
	if bounds.MaxPages > 0 {
		perPage := req.PerPage
		if perPage < 1 {
			perPage = 10
		}
		maxItems := bounds.MaxPages * perPage
		if len(items) > maxItems {
			items = items[:maxItems]
		}
	}
	if bounds.MaxRecords > 0 && len(items) > bounds.MaxRecords {
		items = items[:bounds.MaxRecords]
	}
	perPage := req.PerPage
	if perPage < 1 {
		perPage = 10
	}
	result.RecordsListed = len(items)
	if len(items) > 0 {
		result.PagesListed = (len(items) + perPage - 1) / perPage
	}
	if page.NextPage > 0 || originalCount > len(items) {
		result.TraversalStatus = "bounded"
		if bounds.MaxRecords > 0 {
			result.StopReason = "max_records"
		} else {
			result.StopReason = "max_pages"
		}
	} else {
		result.TraversalStatus = "complete"
		result.StopReason = "end_of_collection"
	}

	s.stageWikiPage(ctx, req, items, result)

	if ctx.Err() != nil {
		diag := SyncDiagnosticCancelled
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			diag = SyncDiagnosticTimeout
		}
		result.SuccessCount = len(result.Results)
		result.FailureCount = len(result.Failures)
		return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount, Diagnostic: diag, TotalRequested: totalRequested}
	}

	result.SuccessCount = len(result.Results)
	result.FailureCount = len(result.Failures)
	if result.FailureCount > 0 {
		return result, &PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount}
	}
	return result, nil
}

func (s *Service) stageWikiPage(ctx context.Context, req BulkSyncRequest, items []gitcode.WikiPage, result *SyncResourcesResult) {
	for _, wp := range items {
		if err := ctx.Err(); err != nil {
			result.Failures = append(result.Failures, newResourceError(wp.Slug, "wiki", err))
			continue
		}
		remoteID := strings.TrimSpace(wp.Slug)
		if remoteID == "" {
			err := ErrInvalidQuery{Field: "wiki_slug", Message: "wiki list item missing slug"}
			result.Failures = append(result.Failures, newResourceErrorWithMessage(wp.Slug, "wiki", err, "wiki list item missing slug"))
			continue
		}
		syncReq := SyncRequest{
			RepoID:         req.RepoID,
			AliasType:      "wiki",
			AliasID:        remoteID,
			IdempotencyKey: scopedBulkSyncKey(req.IdempotencyKey, "wiki", remoteID),
			MaxAttempts:    req.MaxAttempts,
			MaxSize:        req.MaxSize,
		}
		syncResult, err := s.syncWikiListItem(ctx, syncReq, wp)
		if err != nil {
			result.Failures = append(result.Failures, newResourceError(remoteID, "wiki", err))
			continue
		}
		result.Results = append(result.Results, syncResult)
	}
}

func (s *Service) syncWikiListItem(ctx context.Context, req SyncRequest, item gitcode.WikiPage) (SyncResult, error) {
	remoteID := strings.TrimSpace(item.Slug)
	if remoteID == "" {
		return SyncResult{}, ErrInvalidQuery{Field: "wiki_slug", Message: "wiki list item missing slug"}
	}
	revision := strings.TrimSpace(item.Revision)
	if revision != "" {
		skipped, ok, err := s.skipWikiListItemByRevision(ctx, req, remoteID, item, revision)
		if err != nil {
			return SyncResult{}, err
		}
		if ok {
			return skipped, nil
		}
	}

	graph, counts, err := s.fetchAndStage(ctx, req, "wiki", remoteID)
	if err != nil {
		return SyncResult{}, err
	}
	counts.Listed = 1
	counts.FetchedDetail = 1
	completedAt := s.now().UTC()
	eventID := syncEventID(req.IdempotencyKey)
	zeroDelta := counts.Fetched > 0 && counts.Skipped == counts.Fetched && counts.Updated == 0 && counts.Inserted == 0 && counts.Conflicts == 0
	graph.SyncEvents = append(graph.SyncEvents, cache.SyncEvent{ID: eventID, SourceID: graph.Source.ID, RemoteType: "wiki", RemoteID: remoteID, RemoteRevision: graph.SyncStatus.RemoteRevision, Status: "succeeded", IdempotencyKey: req.IdempotencyKey, Message: syncEventMessage(counts), CreatedAt: completedAt, StartedAt: completedAt, CompletedAt: completedAt, ZeroDelta: zeroDelta})
	if err := s.store.UpsertSyncGraph(ctx, s.syncGraphFromSourceGraph(req.RepoID, graph)); err != nil {
		return SyncResult{}, err
	}
	stored, err := s.store.GetSourceScoped(ctx, req.RepoID, graph.Source.ID)
	if err != nil {
		return SyncResult{}, err
	}
	return SyncResult{IdempotencyKey: req.IdempotencyKey, Status: "succeeded", Counts: counts, SyncEventID: eventID, Freshness: string(FreshnessFresh), Record: sourceSummary(stored), GeneratedAt: completedAt, StartedAt: completedAt, CompletedAt: completedAt, ZeroDelta: zeroDelta}, nil
}

func (s *Service) skipWikiListItemByRevision(ctx context.Context, req SyncRequest, remoteID string, item gitcode.WikiPage, revision string) (SyncResult, bool, error) {
	stableID := req.StableID
	if stableID == "" {
		stableID = s.resolveOrFallback(ctx, req.RepoID, "wiki", remoteID, liveFallbackSourceID(s.syncProviderMode(), "wiki", remoteID, item.ID))
	}
	if err := s.guardRemoteAlias(ctx, req.RepoID, "wiki", remoteID, stableID); err != nil {
		return SyncResult{}, false, err
	}
	source, err := s.store.GetSourceScoped(ctx, req.RepoID, stableID)
	if err != nil {
		if isCacheNotFound(err) {
			return SyncResult{}, false, nil
		}
		return SyncResult{}, false, err
	}
	status, err := s.store.GetSyncStatusScoped(ctx, req.RepoID, stableID)
	if err != nil {
		if isCacheNotFound(err) {
			return SyncResult{}, false, nil
		}
		return SyncResult{}, false, err
	}
	if source.Kind != "wiki" || source.ContentHash == "" || status.RemoteType != "wiki" || status.RemoteID != remoteID || status.RemoteRevision != revision || status.Status != "fresh" {
		return SyncResult{}, false, nil
	}

	completedAt := s.now().UTC()
	counts := SyncCounts{Fetched: 1, Skipped: 1, Listed: 1, SkippedByRevision: 1}
	eventID := syncEventID(req.IdempotencyKey)
	event := cache.SyncEvent{ID: eventID, SourceID: stableID, RemoteType: "wiki", RemoteID: remoteID, RemoteRevision: revision, Status: "succeeded", IdempotencyKey: req.IdempotencyKey, Message: syncEventMessage(counts), CreatedAt: completedAt, StartedAt: completedAt, CompletedAt: completedAt, ZeroDelta: true}
	graph := cache.SourceGraph{
		Source:     source,
		Identities: []cache.Identity{{RepoID: req.RepoID, SourceID: stableID, AliasType: "wiki", Alias: remoteID, Remote: cache.RemoteAlias{Type: "wiki", ID: remoteID}}},
		SyncStatus: &cache.SyncStatus{RepoID: req.RepoID, SourceID: stableID, RemoteType: "wiki", RemoteID: remoteID, RemoteRevision: revision, Status: "fresh", LastFetchedAt: completedAt},
		SyncEvents: []cache.SyncEvent{event},
	}
	if item.ID != "" && item.ID != remoteID {
		graph.Identities = append(graph.Identities, cache.Identity{RepoID: req.RepoID, SourceID: stableID, AliasType: "gitcode_wiki_id", Alias: item.ID, Remote: cache.RemoteAlias{Type: "gitcode_wiki_id", ID: item.ID}})
	}
	if err := s.store.UpsertSyncGraph(ctx, s.syncGraphFromSourceGraph(req.RepoID, graph)); err != nil {
		return SyncResult{}, false, err
	}
	stored, err := s.store.GetSourceScoped(ctx, req.RepoID, stableID)
	if err != nil {
		return SyncResult{}, false, err
	}
	return SyncResult{IdempotencyKey: req.IdempotencyKey, Status: "succeeded", Counts: counts, SyncEventID: eventID, Freshness: string(FreshnessFresh), Record: sourceSummary(stored), GeneratedAt: completedAt, StartedAt: completedAt, CompletedAt: completedAt, ZeroDelta: true}, true, nil
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
	var firstDiagnostic SyncDiagnostic

	if err := s.validateRepoScope(ctx, repoID, "issues"); err == nil {
		issuesResult, issuesErr = s.BulkSyncIssues(ctx, req)
		if issuesErr != nil {
			if partial, ok := extractPartialSyncError(issuesErr); ok && partial.Diagnostic != "" {
				firstDiagnostic = partial.Diagnostic
			}
		}
	}

	if err := s.validateRepoScope(ctx, repoID, "wiki"); err == nil {
		wikiResult, wikiErr = s.BulkSyncWiki(ctx, req)
		if wikiErr != nil && firstDiagnostic == "" {
			if partial, ok := extractPartialSyncError(wikiErr); ok && partial.Diagnostic != "" {
				firstDiagnostic = partial.Diagnostic
			}
		}
	}

	aggregated := &SyncResourcesResult{
		Results:  make([]SyncResult, 0),
		Failures: make([]ResourceError, 0),
	}
	if issuesResult != nil {
		aggregated.Results = append(aggregated.Results, issuesResult.Results...)
		aggregated.Failures = append(aggregated.Failures, issuesResult.Failures...)
		aggregated.IssueComments = issuesResult.IssueComments
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
			Diagnostic:   firstDiagnostic,
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

func extractPartialSyncError(err error) (*PartialSyncError, bool) {
	var partial *PartialSyncError
	if errors.As(err, &partial) {
		return partial, true
	}
	return nil, false
}

func bulkSyncFailureResult(err error, sourceID, remoteType string) (*SyncResourcesResult, error) {
	re := newResourceError(sourceID, remoteType, err)
	result := &SyncResourcesResult{Failures: []ResourceError{re}, FailureCount: 1}
	return result, &PartialSyncError{Errors: result.Failures, FailureCount: 1}
}

func (s *Service) ensureBulkIdempotencyKey(req *BulkSyncRequest, scope string) {
	if req == nil || strings.TrimSpace(req.IdempotencyKey) != "" {
		return
	}
	key := contentHash("bulk-sync", scope, req.RepoID, s.now().UTC().Format(time.RFC3339Nano))
	if len(key) > 32 {
		key = key[:32]
	}
	req.IdempotencyKey = key
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
	if req.ClearMilestone {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "clear_milestone", Message: "clear_milestone is not valid when creating an issue"}
	}
	return s.executeWrite(ctx, "create-issue", req, RepositoryScopeIssues)
}

func (s *Service) UpdateIssue(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if req.Number == 0 && strings.TrimSpace(req.ID) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "issue", Message: "number or id is required"}
	}
	switch strings.TrimSpace(req.State) {
	case "", "open", "closed":
	default:
		return WriteCommandResult{}, ErrInvalidQuery{Field: "state", Message: "issue state must be open or closed"}
	}
	if strings.TrimSpace(req.Milestone) != "" && req.ClearMilestone {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "milestone", Message: "milestone and clear_milestone are mutually exclusive"}
	}
	return s.executeWrite(ctx, "update-issue", req, RepositoryScopeIssues)
}

func (s *Service) CreatePage(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if strings.TrimSpace(req.Body) == "" || strings.TrimSpace(firstNonEmptyString(req.Path, req.Slug, req.Title)) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "page", Message: "path and body are required"}
	}
	return s.executeWrite(ctx, "create-page", req, RepositoryScopeWiki)
}

func (s *Service) UpdatePage(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if strings.TrimSpace(firstNonEmptyString(req.Path, req.Slug, req.ID)) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "page", Message: "path or id is required"}
	}
	return s.executeWrite(ctx, "update-page", req, RepositoryScopeWiki)
}

func (s *Service) DeletePage(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if strings.TrimSpace(firstNonEmptyString(req.Path, req.Slug, req.ID)) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "page", Message: "path or id is required"}
	}
	return s.executeWrite(ctx, "delete-page", req, RepositoryScopeWiki)
}

func (s *Service) AddComment(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if (req.Number == 0 && strings.TrimSpace(req.ID) == "") || strings.TrimSpace(req.Body) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "comment", Message: "issue and body are required"}
	}
	return s.executeWrite(ctx, "add-comment", req, RepositoryScopeIssues)
}

func (s *Service) UpdateComment(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if strings.TrimSpace(firstNonEmptyString(req.CommentID, req.ID)) == "" || strings.TrimSpace(req.Body) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "comment", Message: "comment id and body are required"}
	}
	return s.executeWrite(ctx, "update-comment", req, RepositoryScopeIssues)
}

func (s *Service) CreatePR(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if strings.TrimSpace(req.Title) == "" || strings.TrimSpace(req.Head) == "" || strings.TrimSpace(req.Base) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "pull_request", Message: "title, head, and base are required"}
	}
	return s.executeWrite(ctx, "create-pr", req, RepositoryScopeIssues)
}

func (s *Service) UpdatePR(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if req.Number == 0 {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "pull_request", Message: "number is required"}
	}
	return s.executeWrite(ctx, "update-pr", req, RepositoryScopeIssues)
}

func (s *Service) ListMilestones(ctx context.Context, req MilestoneListRequest) (MilestoneListResult, error) {
	repoID := firstNonEmptyString(req.RepoID, req.Repo)
	route, err := s.BuildAdapterRoute(ctx, repoID, RepositoryScopeIssues)
	if err != nil {
		return MilestoneListResult{}, err
	}
	perPage := req.PerPage
	if perPage <= 0 {
		perPage = 100
	}
	page, err := s.client.ListMilestones(ctx, gitcode.MilestoneListRequest{Owner: route.Owner, Repo: route.Name, State: req.State, Page: req.Page, PerPage: perPage})
	if err != nil {
		return MilestoneListResult{}, err
	}
	now := s.now().UTC()
	milestones := make([]MilestoneRecord, 0, len(page.Items))
	for _, milestone := range page.Items {
		_, graph := s.milestoneWriteGraph(route.RepoID, milestone, gitcode.WriteResult[gitcode.Milestone]{Record: milestone, Confirmed: true, Operation: "ListMilestones", RemoteID: milestone.RemoteID, RemoteRevision: milestone.UpdatedAt, BrowserURL: milestone.HTMLURL, ConfirmedAt: now}, now)
		_ = s.store.UpsertRecordGraph(ctx, graph)
		milestones = append(milestones, milestoneRecord(milestone))
	}
	return MilestoneListResult{RepoID: route.RepoID, Milestones: milestones, Page: page.Page, PerPage: page.PerPage, Count: len(milestones), Evidence: "adapter-confirmed read with cache refresh", GeneratedAt: now}, nil
}

func (s *Service) ListPushRemoteMirrors(ctx context.Context, req PushMirrorListRequest) (PushMirrorListResult, error) {
	repoID := firstNonEmptyString(req.RepoID, req.Repo)
	route, err := s.BuildAdapterRoute(ctx, repoID, RepositoryScopeIssues)
	if err != nil {
		return PushMirrorListResult{}, err
	}
	mirrors, err := s.client.ListPushRemoteMirrors(ctx, gitcode.PushMirrorListRequest{Owner: route.Owner, Repo: route.Name})
	if err != nil {
		return PushMirrorListResult{}, err
	}
	sort.Slice(mirrors, func(i, j int) bool {
		left, leftErr := strconv.ParseInt(mirrors[i].RemoteID, 10, 64)
		right, rightErr := strconv.ParseInt(mirrors[j].RemoteID, 10, 64)
		if leftErr == nil && rightErr == nil && left != right {
			return left < right
		}
		if mirrors[i].RemoteID != mirrors[j].RemoteID {
			return mirrors[i].RemoteID < mirrors[j].RemoteID
		}
		return mirrors[i].URL < mirrors[j].URL
	})
	now := s.now().UTC()
	records := make([]PushMirrorRecord, 0, len(mirrors))
	for _, mirror := range mirrors {
		record, graph := pushMirrorRecordGraph(route.RepoID, mirror, now)
		if err := s.store.UpsertRecordGraph(ctx, graph); err != nil {
			return PushMirrorListResult{}, normalizeError(err, "push mirror cache", route.RepoID)
		}
		records = append(records, record)
	}
	return PushMirrorListResult{RepoID: route.RepoID, Mirrors: records, Count: len(records), Evidence: "adapter-confirmed read with sanitized cache refresh", GeneratedAt: now}, nil
}

func (s *Service) TriggerPushRemoteMirror(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if req.Mode == WriteModeLive && strings.TrimSpace(req.IdempotencyKey) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "idempotency_key", Message: "is required for live push mirror triggers"}
	}
	repoID := firstNonEmptyString(req.RepoID, req.Repo)
	route, err := s.BuildAdapterRoute(ctx, repoID, RepositoryScopeIssues)
	if err != nil {
		return WriteCommandResult{}, err
	}
	mirrors, err := s.client.ListPushRemoteMirrors(ctx, gitcode.PushMirrorListRequest{Owner: route.Owner, Repo: route.Name})
	if err != nil {
		return WriteCommandResult{}, err
	}
	mirror, err := resolvePushMirror(mirrors, req.ID)
	if err != nil {
		return WriteCommandResult{}, err
	}
	req.RepoID = route.RepoID
	req.Repo = route.RepoID
	req.ID = mirror.RemoteID
	req.pushMirrorPreviousStatus = mirror.UpdateStatus
	return s.executeWrite(ctx, "trigger-push-mirror", req, RepositoryScopeIssues)
}

func (s *Service) WaitPushRemoteMirror(ctx context.Context, req PushMirrorWaitRequest) (PushMirrorWaitResult, error) {
	timeoutSeconds := req.TimeoutSeconds
	if timeoutSeconds == 0 {
		timeoutSeconds = 120
	}
	if timeoutSeconds < 1 || timeoutSeconds > 600 {
		return PushMirrorWaitResult{}, ErrInvalidQuery{Field: "timeout_seconds", Message: "must be between 1 and 600"}
	}
	pollInterval := req.PollInterval
	if pollInterval <= 0 {
		pollInterval = 2 * time.Second
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = time.Duration(timeoutSeconds) * time.Second
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	mirrorID := strings.TrimSpace(req.MirrorID)
	for {
		list, err := s.ListPushRemoteMirrors(ctx, PushMirrorListRequest{RepoID: firstNonEmptyString(req.RepoID, req.Repo)})
		if err != nil {
			return PushMirrorWaitResult{}, err
		}
		record, err := resolvePushMirrorRecord(list.Mirrors, mirrorID)
		if err != nil {
			return PushMirrorWaitResult{}, err
		}
		mirrorID = record.RemoteID
		result := pushMirrorWaitResult(list.RepoID, record, req.After, s.now().UTC())
		if pushMirrorTerminalAfter(record, req.After) {
			result.Evidence = "terminal status confirmed by sanitized live readback"
			return result, nil
		}
		select {
		case <-ctx.Done():
			return PushMirrorWaitResult{}, ctx.Err()
		case <-deadline.C:
			result.Status = "timeout"
			result.Evidence = "timeout while polling sanitized live mirror status"
			result.GeneratedAt = s.now().UTC()
			return result, nil
		case <-time.After(pollInterval):
		}
	}
}

func resolvePushMirror(mirrors []gitcode.PushMirror, mirrorID string) (gitcode.PushMirror, error) {
	id := strings.TrimSpace(mirrorID)
	if id == "" {
		if len(mirrors) != 1 {
			return gitcode.PushMirror{}, ErrPushMirrorSelectionRequired{Count: len(mirrors)}
		}
		return mirrors[0], nil
	}
	for _, mirror := range mirrors {
		if mirror.RemoteID == id || fallbackSourceID("pushmirror", mirror.RemoteID) == id {
			return mirror, nil
		}
	}
	return gitcode.PushMirror{}, ErrPushMirrorNotFound{MirrorID: id}
}

func resolvePushMirrorRecord(mirrors []PushMirrorRecord, mirrorID string) (PushMirrorRecord, error) {
	id := strings.TrimSpace(mirrorID)
	if id == "" {
		if len(mirrors) != 1 {
			return PushMirrorRecord{}, ErrPushMirrorSelectionRequired{Count: len(mirrors)}
		}
		return mirrors[0], nil
	}
	for _, mirror := range mirrors {
		if mirror.RemoteID == id || mirror.ID == id {
			return mirror, nil
		}
	}
	return PushMirrorRecord{}, ErrPushMirrorNotFound{MirrorID: id}
}

func pushMirrorTerminalAfter(mirror PushMirrorRecord, after time.Time) bool {
	status := strings.ToLower(strings.TrimSpace(mirror.UpdateStatus))
	if status != "finished" && status != "failed" {
		return false
	}
	if after.IsZero() {
		return true
	}
	if status == "finished" {
		return !mirror.LastSuccessfulUpdateAt.IsZero() && !mirror.LastSuccessfulUpdateAt.Before(after)
	}
	return !mirror.LastUpdateAt.IsZero() && !mirror.LastUpdateAt.Before(after)
}

func pushMirrorWaitResult(repoID string, mirror PushMirrorRecord, after, now time.Time) PushMirrorWaitResult {
	status := strings.ToLower(strings.TrimSpace(mirror.UpdateStatus))
	if status != "finished" && status != "failed" {
		status = "waiting"
	}
	return PushMirrorWaitResult{
		RepoID:                 repoID,
		MirrorID:               mirror.RemoteID,
		Status:                 status,
		UpdateStatus:           mirror.UpdateStatus,
		NumberOfFailures:       mirror.NumberOfFailures,
		Message:                mirror.Message,
		LastUpdateAt:           mirror.LastUpdateAt,
		LastSuccessfulUpdateAt: mirror.LastSuccessfulUpdateAt,
		After:                  after,
		GeneratedAt:            now,
	}
}

func (s *Service) CreateMilestone(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if strings.TrimSpace(req.Title) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "milestone.title", Message: "title is required"}
	}
	if strings.TrimSpace(req.DueOn) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "milestone.due_on", Message: "due_on is required"}
	}
	return s.executeWrite(ctx, "create-milestone", req, RepositoryScopeIssues)
}

func (s *Service) UpdateMilestone(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if strings.TrimSpace(firstNonEmptyString(req.Milestone, req.ID)) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "milestone", Message: "milestone id or title is required"}
	}
	return s.executeWrite(ctx, "update-milestone", req, RepositoryScopeIssues)
}

func (s *Service) SetIssueMilestone(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if req.Number == 0 {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "issue", Message: "issue number is required"}
	}
	if strings.TrimSpace(firstNonEmptyString(req.Milestone, req.ID)) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "milestone", Message: "milestone id or title is required"}
	}
	return s.executeWrite(ctx, "set-issue-milestone", req, RepositoryScopeIssues)
}

func (s *Service) ClearIssueMilestone(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if req.Number == 0 {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "issue", Message: "issue number is required"}
	}
	return s.executeWrite(ctx, "clear-issue-milestone", req, RepositoryScopeIssues)
}

func (s *Service) AddPRComment(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if req.Number == 0 || strings.TrimSpace(req.Body) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "pr_comment", Message: "pull request number and body are required"}
	}
	return s.executeWrite(ctx, "add-pr-comment", req, RepositoryScopeIssues)
}

func (s *Service) AddPRReviewComment(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if req.Number == 0 || strings.TrimSpace(req.Body) == "" || strings.TrimSpace(req.Path) == "" || (req.Line == 0 && req.Position == 0) {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "pr_review_comment", Message: "pull request number, body, path, and line or position are required"}
	}
	if req.Line == 0 {
		req.Line = req.Position
	}
	if req.Position > 0 && req.Position != req.Line {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "position", Message: "position is a deprecated file-line alias and must equal line when both are supplied"}
	}
	if req.EndLine > 0 && req.EndLine != req.Line {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "end_line", Message: "end_line must equal the anchor line"}
	}
	if req.StartLine > req.Line {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "start_line", Message: "start_line must be less than or equal to the anchor line"}
	}
	req.Position = req.Line
	return s.executeWrite(ctx, "add-pr-review-comment", req, RepositoryScopeIssues)
}

func (s *Service) ReplyPRReviewComment(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if req.Number == 0 || strings.TrimSpace(req.Body) == "" || strings.TrimSpace(req.DiscussionID) == "" || strings.TrimSpace(req.ParentID) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "pr_review_reply", Message: "pull request number, discussion id, parent comment id, and body are required"}
	}
	return s.executeWrite(ctx, "reply-pr-review-comment", req, RepositoryScopeIssues)
}

func (s *Service) LinkPRIssue(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if req.Number == 0 || req.IssueNumber == 0 {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "link", Message: "pull request number and issue number are required"}
	}
	return s.executeWrite(ctx, "link-pr-issue", req, RepositoryScopeIssues)
}

func (s *Service) AddLabel(ctx context.Context, req WriteCommandRequest) (WriteCommandResult, error) {
	if (req.Number == 0 && strings.TrimSpace(req.ID) == "") || strings.TrimSpace(req.Label) == "" {
		return WriteCommandResult{}, ErrInvalidQuery{Field: "label", Message: "issue and label are required"}
	}
	if req.Number == 0 {
		number, err := issueNumberFromWriteID(req.ID)
		if err != nil {
			return WriteCommandResult{}, err
		}
		req.Number = number
	}
	return s.executeWrite(ctx, "add-label", req, RepositoryScopeIssues)
}

func (s *Service) PublishRelease(ctx context.Context, req PublishReleaseRequest) (PublishReleaseResult, error) {
	if strings.TrimSpace(req.Tag) == "" {
		return PublishReleaseResult{}, ErrInvalidQuery{Field: "tag", Message: "release tag is required"}
	}
	if strings.TrimSpace(req.Title) == "" {
		return PublishReleaseResult{}, ErrInvalidQuery{Field: "title", Message: "release title is required"}
	}
	if strings.TrimSpace(req.Body) == "" {
		return PublishReleaseResult{}, ErrInvalidQuery{Field: "body", Message: "release body is required"}
	}
	if req.Mode != WriteModeDryRun && req.Mode != WriteModeLive {
		return PublishReleaseResult{}, ErrInvalidQuery{Field: "write_mode", Message: "exactly one of dry_run or live is required"}
	}
	route, err := s.BuildAdapterRoute(ctx, firstNonEmptyString(req.RepoID, req.Repo), RepositoryScopeIssues)
	if err != nil {
		return PublishReleaseResult{}, err
	}
	status, err := releaseStatus(req.Status)
	if err != nil {
		return PublishReleaseResult{}, err
	}
	if strings.TrimSpace(req.Ref) == "" {
		req.Ref = "main"
	}
	key, fingerprint := releaseIdempotency(req, status)
	base := PublishReleaseResult{Command: "publish-release", Status: "dry_run_valid", RepoID: route.RepoID, Tag: strings.TrimSpace(req.Tag), ReleaseStatus: status, AssetLinks: req.Assets, IdempotencyKey: key, SourceFingerprint: fingerprint, Evidence: "validated release publish command", GeneratedAt: s.now().UTC()}
	if req.Mode == WriteModeDryRun {
		return base, nil
	}
	if !s.hasWriteCredential() {
		return PublishReleaseResult{}, ErrWriteFailure{Code: "write_missing_credential", RepoID: route.RepoID, IdempotencyKey: key}
	}
	links := make([]gitcode.ReleaseLink, 0, len(req.Assets))
	for _, asset := range req.Assets {
		links = append(links, gitcode.ReleaseLink{Name: strings.TrimSpace(asset.Name), URL: strings.TrimSpace(asset.URL)})
	}
	writeReq := gitcode.ReleaseWriteRequest{
		Owner:         route.Owner,
		Repo:          route.Name,
		TagName:       strings.TrimSpace(req.Tag),
		Ref:           strings.TrimSpace(req.Ref),
		Name:          strings.TrimSpace(req.Title),
		Description:   releaseBodyWithAssets(req.Body, req.Assets),
		ReleaseStatus: status,
		Links:         links,
	}
	existing, err := s.client.GetRelease(ctx, gitcode.ReleaseRequest{Owner: route.Owner, Repo: route.Name, Tag: writeReq.TagName})
	var result gitcode.WriteResult[gitcode.Release]
	if err == nil && strings.TrimSpace(existing.TagName) != "" {
		result, err = s.client.UpdateRelease(ctx, writeReq, gitcode.WriteOptions{IdempotencyKey: key})
	} else if isReleaseNotFound(err) {
		result, err = s.client.CreateRelease(ctx, writeReq, gitcode.WriteOptions{IdempotencyKey: key})
	} else if err != nil {
		return PublishReleaseResult{}, ErrWriteFailure{Code: s.writeAdapterErrorCode(req.Mode, err), RepoID: route.RepoID, IdempotencyKey: key, PayloadSource: failureSource(err), Cause: writeFailureCause(s.writeAdapterErrorCode(req.Mode, err), err)}
	}
	if err != nil {
		code := s.writeAdapterErrorCode(req.Mode, err)
		return PublishReleaseResult{}, ErrWriteFailure{Code: code, RepoID: route.RepoID, IdempotencyKey: key, PayloadSource: failureSource(err), Cause: writeFailureCause(code, err)}
	}
	if !result.Confirmed || strings.TrimSpace(result.RemoteID) == "" {
		return PublishReleaseResult{}, ErrWriteFailure{Code: "write_unconfirmed_remote", RepoID: route.RepoID, IdempotencyKey: key}
	}
	base.Status = "succeeded"
	base.RemoteID = result.RemoteID
	base.Evidence = "adapter-confirmed release publish"
	base.GeneratedAt = firstNonZeroTime(result.ConfirmedAt, s.now().UTC())
	return base, nil
}

func (s *Service) requireCachedPRParent(ctx context.Context, repoID string, number int) error {
	record, err := s.store.GetRecord(ctx, repoID, pullRequestStableID(number))
	if err != nil {
		if isCacheNotFound(err) {
			return ErrParentPRNotCached{RepoID: repoID, Number: number}
		}
		return err
	}
	if record.Type != "pull_request" {
		return ErrParentPRNotCached{RepoID: repoID, Number: number}
	}
	return nil
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
		if issue.Number != number {
			return cache.SourceGraph{}, SyncCounts{}, ErrSyncFailure{
				Mode:           "remote_identity_mismatch",
				Target:         remoteType + ":" + remoteID,
				ExpectedID:     strconv.Itoa(number),
				ActualID:       strconv.Itoa(issue.Number),
				RecoveryAction: "inspect the GitCode single-issue response contract before retrying",
			}
		}
		return s.stageIssue(ctx, req, remoteType, remoteID, issue)
	case "pull_request", "pull", "pulls", "pr":
		route, err := s.BuildAdapterRoute(ctx, req.RepoID, RepositoryScopeIssues)
		if err != nil {
			return cache.SourceGraph{}, SyncCounts{}, err
		}
		number, err := strconv.Atoi(remoteID)
		if err != nil {
			return cache.SourceGraph{}, SyncCounts{}, ErrInvalidQuery{Field: "remote_id", Message: "pull request remote id must be numeric"}
		}
		pr, err := s.client.GetPR(ctx, gitcode.PRRequest{Owner: route.Owner, Repo: route.Name, Number: number})
		if err != nil {
			return cache.SourceGraph{}, SyncCounts{}, err
		}
		return s.stagePullRequest(ctx, req, "pull_request", remoteID, pr)
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

func (s *Service) syncGraphFromSourceGraph(repoID string, graph cache.SourceGraph) cache.SyncGraph {
	revision := ""
	if graph.SyncStatus != nil {
		revision = graph.SyncStatus.RemoteRevision
	}
	record := cache.Record{RepoID: repoID, ID: graph.Source.ID, Type: graph.Source.Kind, Path: graph.Source.Path, Title: graph.Source.Title, Body: graph.Source.Body, Status: graph.Source.Status, Labels: graph.Source.Labels, ContentHash: graph.Source.ContentHash, CreatedAt: graph.Source.CreatedAt, UpdatedAt: graph.Source.UpdatedAt, RemoteRevision: revision}
	if graph.SyncStatus != nil {
		record.RemoteType = graph.SyncStatus.RemoteType
		record.RemoteID = graph.SyncStatus.RemoteID
	}
	revisions := []cache.RemoteRevision{}
	if graph.SyncStatus != nil {
		revisions = append(revisions, cache.RemoteRevision{RepoID: repoID, RecordID: graph.Source.ID, RemoteType: graph.SyncStatus.RemoteType, RemoteID: graph.SyncStatus.RemoteID, RemoteRevision: graph.SyncStatus.RemoteRevision, Status: graph.SyncStatus.Status, LastFetchedAt: graph.SyncStatus.LastFetchedAt})
	}
	return cache.SyncGraph{RepoID: repoID, Provenance: s.syncOriginProvenance(), Record: record, Comments: graph.Comments, PRReviewComments: graph.PRReviewComments, PRReviewDiscussions: graph.PRReviewDiscussions, PRReviewPositions: graph.PRReviewPositions, Identities: graph.Identities, Links: graph.Links, Chunks: graph.Chunks, ReplaceChunks: graph.ReplaceChunks, RemoteRevisions: revisions, SyncEvents: graph.SyncEvents}
}

func (s *Service) stageIssue(ctx context.Context, req SyncRequest, remoteType, remoteID string, issue gitcode.Issue) (cache.SourceGraph, SyncCounts, error) {
	graph, counts, err := s.stageIssueParent(ctx, req, remoteType, remoteID, issue)
	if err != nil {
		return graph, counts, err
	}
	if counts.SkippedByRevision > 0 {
		queued, ok, queueErr := s.store.GetIssueCommentSync(ctx, req.RepoID, graph.Source.ID)
		if queueErr != nil {
			return cache.SourceGraph{}, SyncCounts{}, queueErr
		}
		if !ok || queued.Status == "complete" {
			return graph, counts, nil
		}
	}
	route, err := s.BuildAdapterRoute(ctx, req.RepoID, RepositoryScopeIssues)
	if err != nil {
		return cache.SourceGraph{}, SyncCounts{}, err
	}
	counts.FetchedDetail = 1
	comments, err := s.client.ListIssueComments(ctx, gitcode.IssueRequest{Owner: route.Owner, Repo: route.Name, Number: issue.Number, KnownRemoteAlias: true, RemoteAlias: remoteID})
	if err != nil {
		if !isDeferredIssueCommentsRead(err) {
			return cache.SourceGraph{}, SyncCounts{}, err
		}
		counts.Deferred = 1
		comments = gitcode.Page[gitcode.Comment]{}
	}
	if err := appendIssueCommentsToGraph(s, &graph, comments.Items, remoteID, issue.ID); err != nil {
		return cache.SourceGraph{}, SyncCounts{}, err
	}
	return graph, counts, nil
}

func (s *Service) recordTargetedIssueCommentCoverage(ctx context.Context, remoteType string, graph cache.SourceGraph, counts SyncCounts) error {
	if remoteType != "issue" || graph.SyncStatus == nil || counts.FetchedDetail == 0 {
		return nil
	}
	now := s.now().UTC()
	item, ok, err := s.store.GetIssueCommentSync(ctx, graph.Source.RepoID, graph.Source.ID)
	if err != nil {
		return err
	}
	if !ok {
		number, err := strconv.Atoi(graph.SyncStatus.RemoteID)
		if err != nil {
			return err
		}
		item = cache.IssueCommentSync{RepoID: graph.Source.RepoID, SourceID: graph.Source.ID, IssueNumber: number, RemoteID: graph.SyncStatus.RemoteID, ExpectedCount: len(graph.Comments)}
		for _, identity := range graph.Identities {
			if identity.AliasType == "gitcode_issue_id" {
				item.ProviderID = identity.Alias
				break
			}
		}
	}
	item.RemoteRevision = graph.SyncStatus.RemoteRevision
	item.Attempts++
	item.LastAttemptAt = now
	item.UpdatedAt = now
	if counts.Deferred > 0 {
		item.Status = "deferred"
		item.LastErrorClass = "comments_read"
	} else {
		if err := s.projectIssueComments(ctx, item, graph.Comments); err != nil {
			return err
		}
		item.Status = "complete"
		item.ExpectedCount = len(graph.Comments)
		item.LastErrorClass = ""
		item.RetryAfter = ""
	}
	return s.store.UpsertIssueCommentSync(ctx, item)
}

func (s *Service) stageIssueParent(ctx context.Context, req SyncRequest, remoteType, remoteID string, issue gitcode.Issue) (cache.SourceGraph, SyncCounts, error) {
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
	revision := issueRemoteRevision(issue, hash)
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
	graph := cache.SourceGraph{Source: cache.Source{RepoID: req.RepoID, ID: stableID, Kind: "issue", Path: "issues/" + remoteID + ".md", Title: issue.Title, Body: body, Status: status, Labels: issue.Labels, ContentHash: hash, CreatedAt: created, UpdatedAt: updated}, Identities: []cache.Identity{{RepoID: req.RepoID, SourceID: stableID, AliasType: remoteType, Alias: remoteID, Remote: cache.RemoteAlias{Type: remoteType, ID: remoteID}}}, SyncStatus: &cache.SyncStatus{RepoID: req.RepoID, SourceID: stableID, RemoteType: remoteType, RemoteID: remoteID, RemoteRevision: revision, Status: "fresh", LastFetchedAt: now}}
	if providerID != "" && providerID != remoteID {
		graph.Identities = append(graph.Identities, cache.Identity{RepoID: req.RepoID, SourceID: stableID, AliasType: "gitcode_issue_id", Alias: providerID, Remote: cache.RemoteAlias{Type: "gitcode_issue_id", ID: providerID}})
	}
	if err == nil {
		status, statusErr := s.store.GetSyncStatusScoped(ctx, req.RepoID, stableID)
		if statusErr != nil && !isCacheNotFound(statusErr) {
			return cache.SourceGraph{}, SyncCounts{}, statusErr
		}
		if statusErr == nil && status.RemoteType == remoteType && status.RemoteID == remoteID && status.RemoteRevision == revision && status.Status == "fresh" {
			counts.SkippedByRevision = 1
			return graph, counts, nil
		}
	}
	graph.Chunks = chunksForSource(graph.Source)
	graph.ReplaceChunks = true
	return graph, counts, nil
}

func appendIssueCommentsToGraph(s *Service, graph *cache.SourceGraph, comments []gitcode.Comment, remoteID, providerID string) error {
	if graph == nil {
		return errors.New("issue comment graph is nil")
	}
	now := s.now().UTC()
	for _, comment := range comments {
		commentID := strings.TrimSpace(comment.ID)
		if s.syncProviderMode() == gitcode.ProviderModeLive {
			if commentID == "" {
				return s.liveGraphError("comment missing provider id")
			}
			if !s.liveCommentParentReconciles(comment, remoteID, providerID) {
				return s.liveGraphError("comment parent issue id is unreconciled")
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
		graph.Comments = append(graph.Comments, cache.RecordComment{RepoID: graph.Source.RepoID, RecordID: graph.Source.ID, CommentID: commentID, Author: comment.Author, Body: comment.Body, ContentHash: contentHash(commentID, comment.Author, comment.Body), RemoteRevision: contentHash(commentUpdated), CreatedAt: commentCreated, UpdatedAt: commentUpdated})
	}
	return nil
}

func isDeferredIssueCommentsRead(err error) bool {
	var capability gitcode.ErrUnsupportedCapability
	if errors.As(err, &capability) && capability.CapabilityKey == "comments_read" {
		return true
	}
	var rate gitcode.ErrRateLimited
	return errors.As(err, &rate)
}

func deferredIssueCommentStopReason(err error) string {
	var capability gitcode.ErrUnsupportedCapability
	if errors.As(err, &capability) && capability.CapabilityKey == "comments_read" {
		return "comments_read_unsupported"
	}
	return "rate_limited"
}

func (s *Service) stagePullRequest(ctx context.Context, req SyncRequest, remoteType, remoteID string, pr gitcode.PullRequest) (cache.SourceGraph, SyncCounts, error) {
	body := pr.Body
	if req.MaxSize > 0 && int64(len(body)+len(pr.Title)) > req.MaxSize {
		return cache.SourceGraph{}, SyncCounts{}, gitcode.ErrPayloadTooLarge{Endpoint: remoteID, Limit: req.MaxSize, Size: int64(len(body) + len(pr.Title))}
	}
	if pr.Number <= 0 {
		return cache.SourceGraph{}, SyncCounts{}, s.liveGraphError("pull request number is required")
	}
	stableID := req.StableID
	if stableID == "" {
		stableID = s.resolveOrFallback(ctx, req.RepoID, remoteType, remoteID, pullRequestStableID(pr.Number))
	}
	if err := s.guardRemoteAlias(ctx, req.RepoID, remoteType, remoteID, stableID); err != nil {
		return cache.SourceGraph{}, SyncCounts{}, err
	}
	now := s.now().UTC()
	updated := pr.UpdatedAt.UTC()
	if updated.IsZero() {
		updated = now
	}
	created := pr.CreatedAt.UTC()
	if created.IsZero() {
		created = updated
	}
	status := strings.TrimSpace(pr.State)
	if status == "" {
		status = "open"
	}
	hash := contentHash(pr.ID, pr.Number, pr.Title, body, status, pr.Labels, pr.Base, pr.Head, pr.HTMLURL)
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
	revision := pullRequestRemoteRevision(pr, hash)
	graph := cache.SourceGraph{Source: cache.Source{RepoID: req.RepoID, ID: stableID, Kind: "pull_request", Path: "pulls/" + remoteID + ".md", Title: pr.Title, Body: body, Status: status, Labels: pr.Labels, ContentHash: hash, CreatedAt: created, UpdatedAt: updated}, Identities: []cache.Identity{{RepoID: req.RepoID, SourceID: stableID, AliasType: remoteType, Alias: remoteID, Remote: cache.RemoteAlias{Type: remoteType, ID: remoteID}}}, SyncStatus: &cache.SyncStatus{RepoID: req.RepoID, SourceID: stableID, RemoteType: remoteType, RemoteID: remoteID, RemoteRevision: revision, Status: "fresh", LastFetchedAt: now}}
	if pr.ID != "" && pr.ID != remoteID {
		graph.Identities = append(graph.Identities, cache.Identity{RepoID: req.RepoID, SourceID: stableID, AliasType: "gitcode_pr_id", Alias: pr.ID, Remote: cache.RemoteAlias{Type: "gitcode_pr_id", ID: pr.ID}})
	}
	graph.Chunks = chunksForSource(graph.Source)
	graph.ReplaceChunks = true
	return graph, counts, nil
}

func (s *Service) stagePRComment(ctx context.Context, req SyncRequest, remoteType, remoteID string, prNumber int, comment gitcode.PRComment) (cache.SourceGraph, SyncCounts, error) {
	body := comment.Body
	title := "PR " + strconv.Itoa(prNumber) + " comment"
	if comment.ID != "" {
		title += " " + comment.ID
	}
	if req.MaxSize > 0 && int64(len(body)+len(title)) > req.MaxSize {
		return cache.SourceGraph{}, SyncCounts{}, gitcode.ErrPayloadTooLarge{Endpoint: remoteID, Limit: req.MaxSize, Size: int64(len(body) + len(title))}
	}
	commentID := strings.TrimSpace(comment.ID)
	if commentID == "" {
		commentID = contentHash(prNumber, comment.Author, comment.Body, comment.CreatedAt)
		remoteID = prCommentRemoteID(prNumber, commentID)
	}
	stableID := req.StableID
	if stableID == "" {
		stableID = s.resolveOrFallback(ctx, req.RepoID, remoteType, remoteID, prCommentStableID(prNumber, commentID))
	}
	if err := s.guardRemoteAlias(ctx, req.RepoID, remoteType, remoteID, stableID); err != nil {
		return cache.SourceGraph{}, SyncCounts{}, err
	}
	now := s.now().UTC()
	updated := comment.UpdatedAt.UTC()
	if updated.IsZero() {
		updated = now
	}
	created := comment.CreatedAt.UTC()
	if created.IsZero() {
		created = updated
	}
	hash := contentHash(prNumber, commentID, comment.DiscussionID, comment.ReviewKind, comment.Path, comment.Line, comment.StartLine, comment.EndLine, comment.Position, comment.OriginalPosition, comment.Positions, nullableBoolHash(comment.Resolved), nullableBoolHash(comment.Resolvable), comment.ParentID, comment.Author, body, updated)
	revision := prCommentRemoteRevision(comment, hash)
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
	parentID := firstNonEmptyString(req.ParentSourceID, pullRequestStableID(prNumber))
	graph := cache.SourceGraph{Source: cache.Source{RepoID: req.RepoID, ID: stableID, Kind: "pr_comment", Path: fmt.Sprintf("pulls/%d/comments/%s.md", prNumber, safeIDPart(commentID)), Title: title, Body: body, Status: "current", ContentHash: hash, CreatedAt: created, UpdatedAt: updated}, Identities: []cache.Identity{{RepoID: req.RepoID, SourceID: stableID, AliasType: remoteType, Alias: remoteID, Remote: cache.RemoteAlias{Type: remoteType, ID: remoteID}}}, Links: []cache.Link{{RepoID: req.RepoID, SourceID: stableID, TargetID: parentID, Kind: "parent", Text: "pull_request"}}, SyncStatus: &cache.SyncStatus{RepoID: req.RepoID, SourceID: stableID, RemoteType: remoteType, RemoteID: remoteID, RemoteRevision: revision, Status: "fresh", LastFetchedAt: now}}
	graph.PRReviewComments = []cache.PRReviewComment{{
		RepoID:           req.RepoID,
		SourceID:         stableID,
		PRNumber:         prNumber,
		CommentID:        commentID,
		DiscussionID:     comment.DiscussionID,
		ReviewKind:       firstNonEmptyString(comment.ReviewKind, "general"),
		Author:           comment.Author,
		Path:             comment.Path,
		Line:             comment.Line,
		StartLine:        comment.StartLine,
		EndLine:          comment.EndLine,
		Position:         comment.Position,
		OriginalPosition: comment.OriginalPosition,
		Resolved:         comment.Resolved,
		Resolvable:       comment.Resolvable,
		ParentID:         comment.ParentID,
		CreatedAt:        created,
		UpdatedAt:        updated,
	}}
	if comment.DiscussionID != "" {
		graph.PRReviewDiscussions = []cache.PRReviewDiscussion{{
			RepoID:         req.RepoID,
			PRNumber:       prNumber,
			DiscussionID:   comment.DiscussionID,
			Kind:           firstNonEmptyString(comment.ReviewKind, "general"),
			Resolved:       comment.Resolved,
			Resolvable:     comment.Resolvable,
			FirstCommentID: commentID,
			CreatedAt:      created,
			UpdatedAt:      updated,
		}}
	}
	for _, position := range comment.Positions {
		graph.PRReviewPositions = append(graph.PRReviewPositions, cache.PRReviewPosition{
			RepoID:        req.RepoID,
			PRNumber:      prNumber,
			CommentID:     commentID,
			PositionKind:  firstNonEmptyString(position.PositionKind, "current"),
			DiscussionID:  comment.DiscussionID,
			PositionType:  position.PositionType,
			BaseSHA:       position.BaseSHA,
			StartSHA:      position.StartSHA,
			HeadSHA:       position.HeadSHA,
			OldPath:       position.OldPath,
			NewPath:       position.NewPath,
			OldLine:       position.OldLine,
			NewLine:       position.NewLine,
			StartOldLine:  position.StartOldLine,
			StartNewLine:  position.StartNewLine,
			LineCode:      position.LineCode,
			StartLineCode: position.StartLineCode,
			PatchsetIID:   position.PatchsetIID,
			DiffID:        position.DiffID,
			VersionSHA:    position.VersionSHA,
			Side:          position.Side,
			IsOutdated:    position.IsOutdated,
			CreatedAt:     created,
			UpdatedAt:     updated,
		})
	}
	if comment.DiscussionID != "" {
		graph.Identities = append(graph.Identities, cache.Identity{RepoID: req.RepoID, SourceID: stableID, AliasType: "gitcode_pr_discussion", Alias: comment.DiscussionID, Remote: cache.RemoteAlias{Type: "gitcode_pr_discussion", ID: comment.DiscussionID}})
	}
	graph.Chunks = chunksForSource(graph.Source)
	graph.ReplaceChunks = true
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
	graph := cache.SourceGraph{Source: cache.Source{RepoID: req.RepoID, ID: stableID, Kind: "wiki", Path: normalizeWikiCachePath(remoteID), Title: page.Title, Body: body, Status: "fresh", ContentHash: hash, CreatedAt: created, UpdatedAt: updated}, Identities: []cache.Identity{{RepoID: req.RepoID, SourceID: stableID, AliasType: remoteType, Alias: remoteID, Remote: cache.RemoteAlias{Type: remoteType, ID: remoteID}}}, SyncStatus: &cache.SyncStatus{RepoID: req.RepoID, SourceID: stableID, RemoteType: remoteType, RemoteID: remoteID, RemoteRevision: revision, Status: "fresh", LastFetchedAt: now}}
	if providerID != "" && providerID != remoteID {
		graph.Identities = append(graph.Identities, cache.Identity{RepoID: req.RepoID, SourceID: stableID, AliasType: "gitcode_wiki_id", Alias: providerID, Remote: cache.RemoteAlias{Type: "gitcode_wiki_id", ID: providerID}})
	}
	graph.Chunks = chunksForSource(graph.Source)
	graph.ReplaceChunks = true
	return graph, counts, nil
}

func normalizeWikiCachePath(remoteID string) string {
	remoteID = strings.TrimSpace(remoteID)
	remoteID = strings.TrimPrefix(remoteID, "/")
	remoteID = strings.TrimPrefix(remoteID, "wiki/")
	if remoteID == "" || remoteID == "." {
		remoteID = "Home"
	}
	base := path.Base(remoteID)
	ext := strings.ToLower(path.Ext(base))
	switch ext {
	case ".md", ".markdown", ".mdown", ".mkd":
		dir := path.Dir(remoteID)
		withoutExt := strings.TrimSuffix(base, ext)
		if dir != "." {
			return "wiki/" + dir + "/" + withoutExt + ".md"
		}
		return "wiki/" + withoutExt + ".md"
	default:
		return "wiki/" + remoteID + ".md"
	}
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

func (s *Service) syncOriginProvenance() cache.Provenance {
	if s.syncProviderMode() == gitcode.ProviderModeLive {
		return cache.ProvenanceLive
	}
	return cache.ProvenanceFixture
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
	providerID = strings.TrimSpace(providerID)
	if mode == gitcode.ProviderModeLive && providerID != "" {
		if remoteType != "issue" && remoteType != "issues" && remoteType != "pull_request" {
			return fallbackSourceID(remoteType, providerID)
		}
		if _, err := strconv.ParseInt(providerID, 10, 64); err != nil {
			return fallbackSourceID(remoteType, providerID)
		}
	}
	return fallbackSourceID(remoteType, remoteID)
}

func fallbackSourceID(remoteType, remoteID string) string {
	clean := strings.NewReplacer("/", "-", " ", "-", ":", "-").Replace(strings.ToUpper(remoteID))
	switch remoteType {
	case "issue", "issues":
		return "ISSUE-" + clean
	case "pull_request", "pull", "pulls", "pr":
		return "PR-" + clean
	case "pr_comment":
		return "PRCOMMENT-" + clean
	case "milestone", "milestones":
		return "MILESTONE-" + clean
	case "push_mirror", "push_mirrors", "pushmirror", "push_remote_mirror":
		return "PUSHMIRROR-" + clean
	case "wiki", "page", "remote":
		return "WIKI-" + clean
	default:
		return "REMOTE-" + clean
	}
}

func pullRequestStableID(number int) string {
	return "PR-" + strconv.Itoa(number)
}

func prCommentStableID(prNumber int, commentID string) string {
	return "PRCOMMENT-" + strconv.Itoa(prNumber) + "-" + safeIDPart(commentID)
}

func prCommentRemoteID(prNumber int, commentID string) string {
	return strconv.Itoa(prNumber) + ":" + strings.TrimSpace(commentID)
}

func issueCommentStableID(issueNumber int, commentID string) string {
	return "ISSUECOMMENT-" + strconv.Itoa(issueNumber) + "-" + safeIDPart(commentID)
}

func issueCommentRemoteID(issueNumber int, commentID string) string {
	return strconv.Itoa(issueNumber) + ":" + strings.TrimSpace(commentID)
}

func safeIDPart(value string) string {
	clean := strings.TrimSpace(value)
	if clean == "" {
		return "unknown"
	}
	return strings.NewReplacer("/", "-", " ", "-", ":", "-", "\\", "-", "#", "-", "?", "-").Replace(clean)
}

func pullRequestNumberFromSource(source cache.Source) (int, bool) {
	for _, alias := range source.Aliases {
		if alias.Remote.Type == "pull_request" || alias.AliasType == "pull_request" {
			id := strings.TrimSpace(alias.Remote.ID)
			if id == "" {
				id = strings.TrimSpace(alias.Alias)
			}
			if n, err := strconv.Atoi(id); err == nil && n > 0 {
				return n, true
			}
		}
	}
	id := strings.TrimPrefix(source.ID, "PR-")
	if n, err := strconv.Atoi(id); err == nil && n > 0 {
		return n, true
	}
	return 0, false
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

func nullableBoolHash(value *bool) string {
	if value == nil {
		return ""
	}
	if *value {
		return "true"
	}
	return "false"
}

func prReviewCommentFromCache(meta cache.PRReviewComment, source cache.Source) PRReviewComment {
	kind := firstNonEmptyString(meta.ReviewKind, "general")
	return PRReviewComment{
		ID:               meta.CommentID,
		SourceID:         meta.SourceID,
		DiscussionID:     meta.DiscussionID,
		Kind:             kind,
		Body:             source.Body,
		Author:           meta.Author,
		Path:             meta.Path,
		Line:             meta.Line,
		StartLine:        meta.StartLine,
		EndLine:          meta.EndLine,
		Position:         meta.Position,
		OriginalPosition: meta.OriginalPosition,
		Resolved:         meta.Resolved,
		Resolvable:       meta.Resolvable,
		ParentID:         meta.ParentID,
		CreatedAt:        firstNonZeroTime(meta.CreatedAt, source.CreatedAt),
		UpdatedAt:        firstNonZeroTime(meta.UpdatedAt, source.UpdatedAt),
	}
}

func prReviewPositionFromCache(position cache.PRReviewPosition) PRReviewPosition {
	return PRReviewPosition{
		Kind:          firstNonEmptyString(position.PositionKind, "current"),
		PositionType:  position.PositionType,
		BaseSHA:       position.BaseSHA,
		StartSHA:      position.StartSHA,
		HeadSHA:       position.HeadSHA,
		OldPath:       position.OldPath,
		NewPath:       position.NewPath,
		OldLine:       position.OldLine,
		NewLine:       position.NewLine,
		StartOldLine:  position.StartOldLine,
		StartNewLine:  position.StartNewLine,
		LineCode:      position.LineCode,
		StartLineCode: position.StartLineCode,
		PatchsetIID:   position.PatchsetIID,
		DiffID:        position.DiffID,
		VersionSHA:    position.VersionSHA,
		Side:          position.Side,
		IsOutdated:    position.IsOutdated,
	}
}

func firstCurrentPosition(positions []PRReviewPosition) *PRReviewPosition {
	if len(positions) == 0 {
		return nil
	}
	for i := range positions {
		if positions[i].Kind == "current" {
			return &positions[i]
		}
	}
	return &positions[0]
}

func discussionKind(comment PRReviewComment) string {
	if comment.Kind == "inline" || comment.Path != "" || comment.Line > 0 || comment.Position > 0 {
		return "inline"
	}
	return "general"
}

func mergeDiscussionResolved(current, next *bool) *bool {
	if next == nil {
		return current
	}
	if !*next {
		return next
	}
	if current == nil {
		return next
	}
	if !*current {
		return current
	}
	return next
}

func issueRemoteRevision(issue gitcode.Issue, contentRevision string) string {
	return contentHash("issue", issue.ID, issue.Number, issue.UpdatedAt.UTC(), issue.Comments, contentRevision)
}

func pullRequestRemoteRevision(pr gitcode.PullRequest, contentRevision string) string {
	return contentHash("pull_request", pr.ID, pr.Number, pr.UpdatedAt.UTC(), pr.Base, pr.Head, contentRevision)
}

func prCommentRemoteRevision(comment gitcode.PRComment, contentRevision string) string {
	return contentHash("pr_comment", comment.ID, comment.DiscussionID, comment.UpdatedAt.UTC(), contentRevision)
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
		return ErrSyncFailure{Mode: "partial_response", Target: target, Endpoint: partial.Endpoint, ExpectedBytes: partial.Expected, GotBytes: partial.Got, ResponseBytes: partial.Got, ContentType: partial.ContentType, DecodeOffset: partial.Offset, Attempts: partial.Attempts, RecoveryAction: "response remained truncated after bounded retries; reduce per_page or inspect provider transport health", Cause: err}
	}
	var malformed gitcode.ErrMalformedJSON
	if errors.As(err, &malformed) {
		return ErrSyncFailure{Mode: "malformed_response", Target: target, Endpoint: malformed.Endpoint, ResponseBytes: malformed.ResponseSize, DecodeOffset: malformed.Offset, Attempts: malformed.Attempts, RecoveryAction: "provider returned malformed JSON after bounded retries; inspect provider status or update the captured adapter contract", Cause: err}
	}
	var contentType gitcode.ErrUnexpectedContentType
	if errors.As(err, &contentType) {
		return ErrSyncFailure{Mode: "unexpected_content_type", Target: target, Endpoint: contentType.Endpoint, ResponseBytes: contentType.ResponseSize, ContentType: contentType.ContentType, Attempts: contentType.Attempts, RecoveryAction: "provider returned a non-JSON success response after bounded retries; inspect provider gateway status", Cause: err}
	}
	var schema *gitcode.ErrSchemaDecode
	if errors.As(err, &schema) {
		return ErrSyncFailure{Mode: "schema_decode", Target: target, Endpoint: schema.Endpoint, RecoveryAction: "changing sync bounds will not recover; update the captured GitCode response fixture and adapter schema", Cause: err}
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
		return ErrSyncFailure{Mode: "payload_too_large", Target: target, Endpoint: tooLarge.Endpoint, LimitBytes: tooLarge.Limit, SizeBytes: tooLarge.Size, PayloadSource: tooLarge.Source, RecoveryAction: "increase --max-size or skip with --skip-large", Cause: err}
	}
	var conflict gitcode.ErrConflict
	if errors.As(err, &conflict) {
		return ErrSyncFailure{Mode: "conflict", Target: target, Endpoint: conflict.Endpoint, LocalPayload: append([]byte(nil), conflict.LocalPayload...), RemotePayload: append([]byte(nil), conflict.RemotePayload...), RecoveryAction: "resolve local and remote payloads manually", Cause: err}
	}
	if errorHasDiagnosticCode(err, "empty_wiki") {
		return ErrSyncFailure{Mode: "empty_wiki", Target: target, RecoveryAction: "run `gitcode-mcp wiki init --repo ...` or create a page via the GitCode UI", Cause: err}
	}
	return err
}

func errorHasDiagnosticCode(err error, want string) bool {
	for err != nil {
		if coded, ok := err.(interface{ DiagnosticCode() string }); ok {
			if coded.DiagnosticCode() == want {
				return true
			}
		}
		if unwrapped := errors.Unwrap(err); unwrapped != nil {
			err = unwrapped
		} else {
			break
		}
	}
	return false
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
	if command == "add-pr-review-comment" {
		if err := s.requireCachedPRParent(ctx, route.RepoID, req.Number); err != nil {
			return WriteCommandResult{}, err
		}
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
				partial := audit.WithRequestMetadata(audit.RemoteConfirmedCacheRefreshFailed(route.RepoID, key, command, prior.RecordID, prior.RemoteType, prior.RemoteID, fingerprint, err.Error(), s.now().UTC()), prior.RequestMetadata)
				_ = s.store.RecordAuditEvent(ctx, partial)
				return WriteCommandResult{}, ErrWriteFailure{Code: "write_partial_cache_refresh_failed", RepoID: route.RepoID, RemoteID: prior.RemoteID, IdempotencyKey: key, Cause: err}
			}
			if err := s.recordCacheConfirmation(ctx, command, route.RepoID, key, fingerprint, graph, prior.RemoteID, "succeeded", s.now().UTC()); err != nil {
				return WriteCommandResult{}, ErrWriteFailure{Code: "write_partial_cache_refresh_failed", RepoID: route.RepoID, RemoteID: prior.RemoteID, IdempotencyKey: key, Cause: err}
			}
			completed := audit.WithRequestMetadata(audit.Success(route.RepoID, key, command, graph.Record.ID, graph.Record.RemoteType, prior.RemoteID, fingerprint, "cache refresh replay completed", s.now().UTC()), prior.RequestMetadata)
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
		if lookup.InProgress {
			return WriteCommandResult{}, ErrWriteFailure{Code: "write_idempotency_in_progress", RepoID: route.RepoID, RemoteID: prior.RemoteID, IdempotencyKey: key}
		}
		if lookup.Unsafe {
			return WriteCommandResult{}, ErrWriteFailure{Code: firstNonEmptyString(prior.Message, "write_ambiguous_remote"), RepoID: route.RepoID, RemoteID: prior.RemoteID, IdempotencyKey: key}
		}
	}
	if command == "create-issue" {
		entry := audit.WithRequestMetadata(
			audit.InProgress(route.RepoID, key, command, fallbackSourceID("issue", fingerprint), "issue", "", fingerprint, "remote issue creation pending confirmation", s.now().UTC()),
			map[string]string{
				"method":             "POST",
				"idempotency_key":    key,
				"remote_type":        "issue",
				"provider":           "gitcode-http",
				"provider_mode":      string(gitcode.ProviderModeLive),
				"source_fingerprint": fingerprint,
			},
		)
		if claimer, ok := s.store.(auditClaimStore); ok {
			claimed, err := claimer.ClaimAuditEvent(ctx, entry)
			if err != nil {
				return WriteCommandResult{}, ErrWriteFailure{Code: "write_audit_start_failed", RepoID: route.RepoID, IdempotencyKey: key, Cause: err}
			}
			if !claimed {
				return s.executeWrite(ctx, command, req, scope)
			}
		} else if err := s.store.RecordAuditEvent(ctx, entry); err != nil {
			return WriteCommandResult{}, ErrWriteFailure{Code: "write_audit_start_failed", RepoID: route.RepoID, IdempotencyKey: key, Cause: err}
		}
	}
	if command == "trigger-push-mirror" {
		entry := audit.WithRequestMetadata(
			audit.InProgress(route.RepoID, key, command, fallbackSourceID("pushmirror", req.ID), "push_remote_mirror", req.ID, fingerprint, "remote trigger pending confirmation", s.now().UTC()),
			map[string]string{
				"method":             "POST",
				"idempotency_key":    key,
				"remote_alias":       req.ID,
				"remote_type":        "push_remote_mirror",
				"provider":           "gitcode-http",
				"provider_mode":      string(gitcode.ProviderModeLive),
				"source_fingerprint": fingerprint,
			},
		)
		if claimer, ok := s.store.(auditClaimStore); ok {
			claimed, err := claimer.ClaimAuditEvent(ctx, entry)
			if err != nil {
				return WriteCommandResult{}, ErrWriteFailure{Code: "write_audit_start_failed", RepoID: route.RepoID, RemoteID: req.ID, IdempotencyKey: key, Cause: err}
			}
			if !claimed {
				return s.executeWrite(ctx, command, req, scope)
			}
		} else if err := s.store.RecordAuditEvent(ctx, entry); err != nil {
			return WriteCommandResult{}, ErrWriteFailure{Code: "write_audit_start_failed", RepoID: route.RepoID, RemoteID: req.ID, IdempotencyKey: key, Cause: err}
		}
	}
	confirmed, graph, err := s.callWriteAdapter(ctx, command, route, req, key)
	if err != nil {
		code := s.writeAdapterErrorCode(req.Mode, err)
		remoteID := remoteWriteID(err)
		if remoteID != "" {
			remoteType := writeCommandRemoteType(command)
			entry := audit.RemoteConfirmedUnsafe(route.RepoID, key, command, fallbackSourceID(remoteType, remoteID), remoteType, remoteID, fingerprint, code, s.now().UTC())
			if auditErr := s.store.RecordAuditEvent(ctx, entry); auditErr != nil {
				partial := audit.RemoteConfirmedAuditFailed(route.RepoID, key, command, entry.RecordID, remoteType, remoteID, fingerprint, auditErr.Error(), s.now().UTC())
				_ = s.store.RecordAuditEvent(ctx, partial)
				return WriteCommandResult{}, ErrWriteFailure{Code: "write_partial_remote_confirmed_audit_failed", RepoID: route.RepoID, RemoteID: remoteID, IdempotencyKey: key, Cause: auditErr}
			}
			return WriteCommandResult{}, ErrWriteFailure{Code: code, RepoID: route.RepoID, RemoteID: remoteID, IdempotencyKey: key, PayloadSource: failureSource(err), Cause: writeFailureCause(code, err)}
		}
		if command == "trigger-push-mirror" && !safePushMirrorTriggerFailure(err) {
			return WriteCommandResult{}, ErrWriteFailure{Code: "write_ambiguous_remote", RepoID: route.RepoID, RemoteID: req.ID, IdempotencyKey: key, PayloadSource: failureSource(err), Cause: err}
		}
		_ = s.store.RecordAuditEvent(ctx, audit.Failure(route.RepoID, key, command, fingerprint, code, s.now().UTC()))
		return WriteCommandResult{}, ErrWriteFailure{Code: code, RepoID: route.RepoID, IdempotencyKey: key, PayloadSource: failureSource(err), Cause: writeFailureCause(code, err)}
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
	auditEntry = withWriteAuditMetadata(auditEntry, command, key, fingerprint, graph.Record.RemoteType, confirmed)
	if err := s.store.RecordAuditEvent(ctx, auditEntry); err != nil {
		partial := withWriteAuditMetadata(audit.RemoteConfirmedAuditFailed(route.RepoID, key, command, graph.Record.ID, graph.Record.RemoteType, confirmed.remoteID, fingerprint, err.Error(), s.now().UTC()), command, key, fingerprint, graph.Record.RemoteType, confirmed)
		_ = s.store.RecordAuditEvent(ctx, partial)
		return WriteCommandResult{}, ErrWriteFailure{Code: "write_partial_remote_confirmed_audit_failed", RepoID: route.RepoID, RemoteID: confirmed.remoteID, IdempotencyKey: key, Cause: err}
	}
	if err := s.store.UpsertRecordGraph(ctx, graph); err != nil {
		partial := withWriteAuditMetadata(audit.RemoteConfirmedCacheRefreshFailed(route.RepoID, key, command, graph.Record.ID, graph.Record.RemoteType, confirmed.remoteID, fingerprint, err.Error(), s.now().UTC()), command, key, fingerprint, graph.Record.RemoteType, confirmed)
		_ = s.store.RecordAuditEvent(ctx, partial)
		return WriteCommandResult{}, ErrWriteFailure{Code: "write_partial_cache_refresh_failed", RepoID: route.RepoID, RemoteID: confirmed.remoteID, IdempotencyKey: key, Cause: err}
	}
	if err := s.recordCacheConfirmation(ctx, command, route.RepoID, key, fingerprint, graph, confirmed.remoteID, "succeeded", confirmed.completedAt); err != nil {
		partial := withWriteAuditMetadata(audit.RemoteConfirmedCacheRefreshFailed(route.RepoID, key, command, graph.Record.ID, graph.Record.RemoteType, confirmed.remoteID, fingerprint, err.Error(), s.now().UTC()), command, key, fingerprint, graph.Record.RemoteType, confirmed)
		_ = s.store.RecordAuditEvent(ctx, partial)
		return WriteCommandResult{}, ErrWriteFailure{Code: "write_partial_cache_refresh_failed", RepoID: route.RepoID, RemoteID: confirmed.remoteID, IdempotencyKey: key, Cause: err}
	}
	base.Status = "succeeded"
	base.ID = graph.Record.ID
	base.RemoteID = confirmed.remoteID
	base.RemoteNumber = confirmed.remoteNumber
	base.RemoteSlug = confirmed.remoteSlug
	base.RemoteRevision = confirmed.remoteRevision
	base.APIPath = confirmed.apiPath
	base.CachePath = confirmed.cachePath
	base.BrowserURL = confirmed.browserURL
	base.Milestone = confirmed.milestone
	base.PushMirror = confirmed.pushMirror
	base.Evidence = "adapter-confirmed write with audit and cache refresh"
	return base, nil
}

type writeConfirmation struct {
	confirmed      bool
	remoteID       string
	remoteNumber   int
	remoteSlug     string
	remoteRevision string
	apiPath        string
	cachePath      string
	browserURL     string
	message        string
	completedAt    time.Time
	milestone      *WriteMilestoneReceipt
	pushMirror     *WritePushMirrorReceipt
	parentRemoteID string
}

func writeAuditMetadata(command, key, fingerprint, remoteType string, confirmed writeConfirmation) map[string]string {
	method := "POST"
	switch command {
	case "update-issue", "set-issue-milestone", "clear-issue-milestone":
		method = "PATCH"
	}
	metadata := map[string]string{
		"method":             method,
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
	if confirmed.parentRemoteID != "" {
		metadata["pr_discussion_id"] = confirmed.parentRemoteID
	}
	if command != "" {
		metadata["provider"] = "gitcode-http"
	}
	if confirmed.milestone != nil {
		if confirmed.milestone.ID != "" {
			metadata["milestone_id"] = confirmed.milestone.ID
		}
		if confirmed.milestone.RemoteID != "" {
			metadata["milestone_remote_id"] = confirmed.milestone.RemoteID
		}
		if confirmed.milestone.Title != "" {
			metadata["milestone_title"] = confirmed.milestone.Title
		}
		if confirmed.milestone.Cleared {
			metadata["milestone_cleared"] = "true"
		}
	}
	if confirmed.pushMirror != nil {
		metadata["push_mirror_id"] = confirmed.pushMirror.MirrorID
		metadata["push_mirror_status"] = confirmed.pushMirror.Status
		metadata["push_mirror_previous_status"] = confirmed.pushMirror.PreviousStatus
		metadata["push_mirror_triggered_at"] = confirmed.pushMirror.TriggeredAt.UTC().Format(time.RFC3339Nano)
	}
	return metadata
}

func withWriteAuditMetadata(entry cache.AuditTrailEntry, command, key, fingerprint, remoteType string, confirmed writeConfirmation) cache.AuditTrailEntry {
	if command != "create-issue" && command != "reply-pr-review-comment" && confirmed.milestone == nil && confirmed.pushMirror == nil {
		return entry
	}
	return audit.WithRequestMetadata(entry, writeAuditMetadata(command, key, fingerprint, remoteType, confirmed))
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
		var resolved *gitcode.Milestone
		var encoded json.RawMessage
		if strings.TrimSpace(req.Milestone) != "" {
			milestone, err := s.resolveMilestone(ctx, route, req.Milestone)
			if err != nil {
				return writeConfirmation{}, cache.RecordGraph{}, err
			}
			resolved = &milestone
			encoded = gitcode.EncodeIssueMilestone(milestone.RemoteID)
		}
		result, err := s.client.CreateIssue(ctx, gitcode.CreateIssueRequest{Owner: route.Owner, Repo: route.Name, Title: strings.TrimSpace(req.Title), Body: req.Body, Labels: gitcode.EncodeIssueLabels(req.Labels), Milestone: encoded}, opts)
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		var receipt *WriteMilestoneReceipt
		if resolved != nil {
			result, receipt, err = s.confirmIssueMilestone(ctx, route, result, resolved, false)
			if err != nil {
				return writeConfirmation{}, cache.RecordGraph{}, err
			}
		}
		confirmation, graph := s.issueWriteGraph(route.RepoID, result.Record, result, now)
		confirmation.milestone = receipt
		return confirmation, graph, nil
	case "update-issue":
		var resolved *gitcode.Milestone
		var encoded json.RawMessage
		if strings.TrimSpace(req.Milestone) != "" {
			milestone, err := s.resolveMilestone(ctx, route, req.Milestone)
			if err != nil {
				return writeConfirmation{}, cache.RecordGraph{}, err
			}
			resolved = &milestone
			encoded = gitcode.EncodeIssueMilestone(milestone.RemoteID)
		} else if req.ClearMilestone {
			encoded = gitcode.EncodeIssueMilestone("null")
		}
		result, err := s.client.UpdateIssue(ctx, gitcode.UpdateIssueRequest{Owner: route.Owner, Repo: route.Name, Number: req.Number, Title: req.Title, Body: req.Body, State: req.State, Labels: gitcode.EncodeIssueLabels(req.Labels), Milestone: encoded}, opts)
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		var receipt *WriteMilestoneReceipt
		if resolved != nil || req.ClearMilestone {
			result, receipt, err = s.confirmIssueMilestone(ctx, route, result, resolved, req.ClearMilestone)
			if err != nil {
				return writeConfirmation{}, cache.RecordGraph{}, err
			}
		}
		confirmation, graph := s.issueWriteGraph(route.RepoID, result.Record, result, now)
		confirmation.milestone = receipt
		return confirmation, graph, nil
	case "add-comment":
		result, err := s.client.CreateIssueComment(ctx, gitcode.CreateIssueCommentRequest{Owner: route.Owner, Repo: route.Name, Number: req.Number, Body: req.Body}, opts)
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		return s.commentWriteGraph(ctx, route.RepoID, req.Number, result.Record, result, now)
	case "update-comment":
		commentID := strings.TrimSpace(firstNonEmptyString(req.CommentID, req.ID))
		result, err := s.client.UpdateIssueComment(ctx, gitcode.UpdateIssueCommentRequest{Owner: route.Owner, Repo: route.Name, Number: req.Number, CommentID: commentID, Body: req.Body}, opts)
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		return s.commentWriteGraph(ctx, route.RepoID, req.Number, result.Record, result, now)
	case "create-pr":
		result, err := s.client.CreatePR(ctx, gitcode.CreatePRRequest{Owner: route.Owner, Repo: route.Name, Title: strings.TrimSpace(req.Title), Body: req.Body, Head: strings.TrimSpace(req.Head), Base: strings.TrimSpace(req.Base)}, opts)
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		return s.pullRequestWriteGraph(ctx, route.RepoID, result.Record, result, now)
	case "update-pr":
		result, err := s.client.UpdatePR(ctx, gitcode.UpdatePRRequest{Owner: route.Owner, Repo: route.Name, Number: req.Number, Title: req.Title, Body: req.Body, State: req.State}, opts)
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		return s.pullRequestWriteGraph(ctx, route.RepoID, result.Record, result, now)
	case "create-milestone":
		result, err := s.client.CreateMilestone(ctx, gitcode.MilestoneWriteRequest{Owner: route.Owner, Repo: route.Name, Title: strings.TrimSpace(req.Title), Description: req.Description, DueOn: req.DueOn, State: req.State}, opts)
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		confirmation, graph := s.milestoneWriteGraph(route.RepoID, result.Record, result, now)
		return confirmation, graph, nil
	case "update-milestone":
		id, err := s.resolveMilestoneID(ctx, route, firstNonEmptyString(req.Milestone, req.ID))
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		result, err := s.client.UpdateMilestone(ctx, gitcode.MilestoneWriteRequest{Owner: route.Owner, Repo: route.Name, ID: id, Title: req.Title, Description: req.Description, DueOn: req.DueOn, State: req.State}, opts)
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		confirmation, graph := s.milestoneWriteGraph(route.RepoID, result.Record, result, now)
		return confirmation, graph, nil
	case "trigger-push-mirror":
		result, err := s.client.TriggerPushRemoteMirror(ctx, gitcode.PushMirrorTriggerRequest{Owner: route.Owner, Repo: route.Name, MirrorID: req.ID}, opts)
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		_, graph := pushMirrorRecordGraph(route.RepoID, result.Record, now)
		confirmation := writeConfirmation{
			confirmed:      result.Confirmed,
			remoteID:       result.RemoteID,
			remoteRevision: result.RemoteRevision,
			apiPath:        triggerPushMirrorAPIPath(route.Owner, route.Name, result.RemoteID),
			cachePath:      graph.Record.Path,
			message:        result.ProviderStatus,
			completedAt:    firstNonZeroTime(result.ConfirmedAt, now),
			pushMirror: &WritePushMirrorReceipt{
				MirrorID:       result.RemoteID,
				Status:         "triggered",
				PreviousStatus: req.pushMirrorPreviousStatus,
				ReadbackStatus: result.Record.UpdateStatus,
				TriggeredAt:    firstNonZeroTime(result.ConfirmedAt, now),
			},
		}
		return confirmation, graph, nil
	case "set-issue-milestone":
		milestone, err := s.resolveMilestone(ctx, route, firstNonEmptyString(req.Milestone, req.ID))
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		return s.updateIssueMilestone(ctx, route, req.Number, &milestone, false, opts, now)
	case "clear-issue-milestone":
		return s.updateIssueMilestone(ctx, route, req.Number, nil, true, opts, now)
	case "add-pr-comment":
		result, err := s.client.CreatePRComment(ctx, gitcode.CreatePRCommentRequest{Owner: route.Owner, Repo: route.Name, Number: req.Number, Body: req.Body}, opts)
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		return s.prCommentWriteGraph(ctx, route.RepoID, req.Number, result.Record, result, now)
	case "add-pr-review-comment":
		result, err := s.client.CreatePRReviewComment(ctx, gitcode.CreatePRReviewCommentRequest{Owner: route.Owner, Repo: route.Name, Number: req.Number, Body: req.Body, Path: req.Path, Line: req.Line, StartLine: req.StartLine, EndLine: req.EndLine, Position: req.Position}, opts)
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		return s.prCommentWriteGraph(ctx, route.RepoID, req.Number, result.Record, result, now)
	case "reply-pr-review-comment":
		result, err := s.client.ReplyPRReviewComment(ctx, gitcode.ReplyPRReviewCommentRequest{Owner: route.Owner, Repo: route.Name, Number: req.Number, DiscussionID: req.DiscussionID, ParentCommentID: req.ParentID, Body: req.Body}, opts)
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		return s.prCommentThreadWriteGraph(ctx, route.RepoID, req.Number, result, now)
	case "link-pr-issue":
		if strings.TrimSpace(req.Strategy) != "description_fallback" {
			result, err := s.client.LinkPRIssue(ctx, gitcode.LinkPRIssueRequest{Owner: route.Owner, Repo: route.Name, Number: req.Number, IssueNumber: req.IssueNumber}, opts)
			if err == nil {
				return s.prIssueLinkWriteGraph(ctx, route.RepoID, req.Number, req.IssueNumber, result, now)
			}
			if !isPRIssueRelationUnsupported(err) {
				return writeConfirmation{}, cache.RecordGraph{}, err
			}
		}
		confirmation, graph, err := s.linkPRIssueDescriptionFallback(ctx, route, req, opts, now)
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		return confirmation, graph, nil
	case "add-label":
		return s.addLabelViaUpdateIssue(ctx, route, req, opts, now)
	case "create-page":
		result, err := s.client.CreateWikiPage(ctx, gitcode.CreateWikiPageRequest{Owner: route.Owner, Repo: route.Name, Path: firstNonEmptyString(req.Path, req.Slug, req.Title), Slug: req.Slug, Title: strings.TrimSpace(req.Title), Body: req.Body}, opts)
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		confirmation, graph := s.wikiWriteGraph(route.RepoID, result.Record, result, now)
		return confirmation, graph, nil
	case "update-page":
		result, err := s.client.UpdateWikiPage(ctx, gitcode.UpdateWikiPageRequest{Owner: route.Owner, Repo: route.Name, Path: firstNonEmptyString(req.Path, req.Slug, req.ID), Slug: firstNonEmptyString(req.Slug, req.ID), Title: req.Title, Body: req.Body, Sha: req.Sha}, opts)
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		confirmation, graph := s.wikiWriteGraph(route.RepoID, result.Record, result, now)
		return confirmation, graph, nil
	case "delete-page":
		result, err := s.client.DeleteWikiPage(ctx, gitcode.DeleteWikiPageRequest{Owner: route.Owner, Repo: route.Name, Path: firstNonEmptyString(req.Path, req.Slug, req.ID), Slug: firstNonEmptyString(req.Slug, req.ID), Sha: req.Sha}, opts)
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		confirmation, graph := s.wikiWriteGraph(route.RepoID, result.Record, result, now)
		return confirmation, graph, nil
	default:
		return writeConfirmation{}, cache.RecordGraph{}, ErrWriteFailure{Code: "write_unsupported_deferred", RepoID: route.RepoID}
	}
}

func (s *Service) addLabelViaUpdateIssue(ctx context.Context, route RepositoryRoute, req WriteCommandRequest, opts gitcode.WriteOptions, now time.Time) (writeConfirmation, cache.RecordGraph, error) {
	label := strings.TrimSpace(req.Label)
	issue, err := s.client.GetIssue(ctx, gitcode.IssueRequest{Owner: route.Owner, Repo: route.Name, Number: req.Number})
	if err != nil {
		return writeConfirmation{}, cache.RecordGraph{}, err
	}
	labels, changed := mergeIssueLabel(issue.Labels, label)
	if !changed {
		result := gitcode.WriteResult[gitcode.Issue]{
			Record:       issue,
			Confirmed:    true,
			Operation:    "AddLabelNoop",
			RemoteID:     issue.ID,
			RemoteNumber: issue.Number,
			ConfirmedAt:  now,
		}
		confirmation, graph := s.issueWriteGraph(route.RepoID, issue, result, now)
		return confirmation, graph, nil
	}
	result, err := s.client.UpdateIssue(ctx, gitcode.UpdateIssueRequest{Owner: route.Owner, Repo: route.Name, Number: req.Number, Labels: gitcode.EncodeIssueLabels(labels)}, opts)
	if err != nil {
		return writeConfirmation{}, cache.RecordGraph{}, err
	}
	confirmation, graph := s.issueWriteGraph(route.RepoID, result.Record, result, now)
	return confirmation, graph, nil
}

func mergeIssueLabel(existing []string, label string) ([]string, bool) {
	label = strings.TrimSpace(label)
	labels := make([]string, 0, len(existing)+1)
	found := false
	for _, current := range existing {
		trimmed := strings.TrimSpace(current)
		if trimmed == "" {
			continue
		}
		if trimmed == label {
			found = true
		}
		labels = append(labels, trimmed)
	}
	if found {
		return labels, false
	}
	labels = append(labels, label)
	return labels, true
}

func (s *Service) resolveMilestone(ctx context.Context, route RepositoryRoute, milestone string) (gitcode.Milestone, error) {
	trimmed := strings.TrimSpace(milestone)
	if trimmed == "" {
		return gitcode.Milestone{}, ErrInvalidQuery{Field: "milestone", Message: "milestone id or title is required"}
	}
	normalized := strings.TrimPrefix(strings.ToUpper(trimmed), "MILESTONE-")
	if id, err := strconv.Atoi(normalized); err == nil && id > 0 {
		resolved, err := s.client.GetMilestone(ctx, gitcode.MilestoneRequest{Owner: route.Owner, Repo: route.Name, ID: id})
		if err != nil {
			return gitcode.Milestone{}, err
		}
		if resolved.RemoteID != strconv.Itoa(id) {
			return gitcode.Milestone{}, ErrInvalidQuery{Field: "milestone", Message: "milestone readback id does not match requested repository milestone"}
		}
		return resolved, nil
	}
	page, err := s.client.ListMilestones(ctx, gitcode.MilestoneListRequest{Owner: route.Owner, Repo: route.Name, PerPage: 100})
	if err != nil {
		return gitcode.Milestone{}, err
	}
	var match *gitcode.Milestone
	for idx := range page.Items {
		if strings.EqualFold(strings.TrimSpace(page.Items[idx].Title), trimmed) {
			if match != nil {
				return gitcode.Milestone{}, ErrInvalidQuery{Field: "milestone", Message: "milestone title is ambiguous; use numeric id"}
			}
			match = &page.Items[idx]
		}
	}
	if match == nil {
		return gitcode.Milestone{}, ErrInvalidQuery{Field: "milestone", Message: "milestone not found by id or title"}
	}
	id, err := strconv.Atoi(match.RemoteID)
	if err != nil || id <= 0 {
		return gitcode.Milestone{}, ErrInvalidQuery{Field: "milestone", Message: "milestone id is invalid"}
	}
	return *match, nil
}

func (s *Service) resolveMilestoneID(ctx context.Context, route RepositoryRoute, milestone string) (int, error) {
	resolved, err := s.resolveMilestone(ctx, route, milestone)
	if err != nil {
		return 0, err
	}
	id, err := strconv.Atoi(resolved.RemoteID)
	if err != nil || id <= 0 {
		return 0, ErrInvalidQuery{Field: "milestone", Message: "milestone id is invalid"}
	}
	return id, nil
}

func (s *Service) confirmIssueMilestone(ctx context.Context, route RepositoryRoute, result gitcode.WriteResult[gitcode.Issue], milestone *gitcode.Milestone, clear bool) (gitcode.WriteResult[gitcode.Issue], *WriteMilestoneReceipt, error) {
	number := firstNonZeroInt(result.Record.Number, result.RemoteNumber)
	if number <= 0 {
		return gitcode.WriteResult[gitcode.Issue]{}, nil, ErrWriteFailure{Code: "write_readback_mismatch", RepoID: route.RepoID, RemoteID: result.RemoteID}
	}
	readback, err := s.client.GetIssue(ctx, gitcode.IssueRequest{Owner: route.Owner, Repo: route.Name, Number: number})
	if err != nil {
		return gitcode.WriteResult[gitcode.Issue]{}, nil, err
	}
	if clear {
		if readback.Milestone != nil {
			return gitcode.WriteResult[gitcode.Issue]{}, nil, ErrWriteFailure{Code: "write_readback_mismatch", RepoID: route.RepoID, RemoteID: strconv.Itoa(number)}
		}
		result.Record = readback
		result.RemoteID = firstNonEmptyString(result.RemoteID, readback.ID, strconv.Itoa(readback.Number))
		result.RemoteNumber = firstNonZeroInt(result.RemoteNumber, readback.Number)
		result.RemoteRevision = firstNonEmptyString(result.RemoteRevision, result.ResponseHash, readback.UpdatedAt.UTC().Format(time.RFC3339Nano))
		return result, &WriteMilestoneReceipt{Cleared: true}, nil
	}
	if milestone == nil || readback.Milestone == nil || readback.Milestone.RemoteID != milestone.RemoteID {
		return gitcode.WriteResult[gitcode.Issue]{}, nil, ErrWriteFailure{Code: "write_readback_mismatch", RepoID: route.RepoID, RemoteID: strconv.Itoa(number)}
	}
	resolved := readback.Milestone
	if strings.TrimSpace(resolved.Title) == "" {
		resolved = milestone
	}
	result.Record = readback
	result.RemoteID = firstNonEmptyString(result.RemoteID, readback.ID, strconv.Itoa(readback.Number))
	result.RemoteNumber = firstNonZeroInt(result.RemoteNumber, readback.Number)
	result.RemoteRevision = firstNonEmptyString(result.RemoteRevision, result.ResponseHash, readback.UpdatedAt.UTC().Format(time.RFC3339Nano))
	return result, milestoneWriteReceipt(resolved, false), nil
}

func (s *Service) updateIssueMilestone(ctx context.Context, route RepositoryRoute, number int, milestone *gitcode.Milestone, clear bool, opts gitcode.WriteOptions, now time.Time) (writeConfirmation, cache.RecordGraph, error) {
	raw := "null"
	if milestone != nil {
		raw = milestone.RemoteID
	}
	encoded := gitcode.EncodeIssueMilestone(raw)
	if len(encoded) == 0 {
		return writeConfirmation{}, cache.RecordGraph{}, ErrInvalidQuery{Field: "milestone", Message: "milestone id must be positive or null"}
	}
	result, err := s.client.UpdateIssue(ctx, gitcode.UpdateIssueRequest{Owner: route.Owner, Repo: route.Name, Number: number, Milestone: encoded}, opts)
	if err != nil {
		return writeConfirmation{}, cache.RecordGraph{}, err
	}
	result, receipt, err := s.confirmIssueMilestone(ctx, route, result, milestone, clear)
	if err != nil {
		return writeConfirmation{}, cache.RecordGraph{}, err
	}
	if clear {
		result.Operation = "ClearIssueMilestone"
	} else {
		result.Operation = "SetIssueMilestone"
	}
	confirmation, graph := s.issueWriteGraph(route.RepoID, result.Record, result, now)
	confirmation.milestone = receipt
	return confirmation, graph, nil
}

func milestoneWriteReceipt(milestone *gitcode.Milestone, cleared bool) *WriteMilestoneReceipt {
	if cleared {
		return &WriteMilestoneReceipt{Cleared: true}
	}
	if milestone == nil {
		return nil
	}
	return &WriteMilestoneReceipt{
		ID:       firstNonEmptyString(milestone.SourceID, fallbackSourceID("milestone", milestone.RemoteID)),
		RemoteID: milestone.RemoteID,
		Title:    milestone.Title,
	}
}

func issueNumberFromWriteID(id string) (int, error) {
	normalized := strings.TrimSpace(id)
	normalized = strings.TrimPrefix(normalized, "ISSUE-")
	number, err := strconv.Atoi(normalized)
	if err != nil || number <= 0 {
		return 0, ErrInvalidQuery{Field: "issue", Message: "issue id must be a positive number or ISSUE-N"}
	}
	return number, nil
}

func (s *Service) replayWriteGraph(ctx context.Context, command string, repoID string, req WriteCommandRequest, prior cache.AuditTrailEntry) (cache.RecordGraph, error) {
	now := s.now().UTC()
	switch command {
	case "create-issue", "update-issue", "add-label", "set-issue-milestone", "clear-issue-milestone":
		number, _ := strconv.Atoi(prior.RemoteID)
		issue := gitcode.Issue{ID: prior.RemoteID, Number: number, Title: strings.TrimSpace(req.Title), Body: req.Body, State: firstNonEmptyString(req.State, "open"), CreatedAt: now, UpdatedAt: now}
		if receipt := milestoneReceiptFromAudit(prior); receipt != nil && !receipt.Cleared {
			issue.Milestone = &gitcode.Milestone{RemoteID: receipt.RemoteID, SourceID: receipt.ID, Title: receipt.Title}
		}
		if issue.Title == "" {
			issue.Title = "Issue " + prior.RemoteID
		}
		if command == "add-label" && strings.TrimSpace(req.Label) != "" {
			issue.Labels = append(issue.Labels, strings.TrimSpace(req.Label))
		}
		result := gitcode.WriteResult[gitcode.Issue]{Record: issue, Confirmed: true, RemoteID: prior.RemoteID, RemoteNumber: number, RemoteRevision: firstNonEmptyString(prior.Message, prior.PayloadHash), ConfirmedAt: now}
		_, graph := s.issueWriteGraph(repoID, issue, result, now)
		return graph, nil
	case "create-milestone", "update-milestone":
		milestone := gitcode.Milestone{RemoteID: prior.RemoteID, SourceID: fallbackSourceID("milestone", prior.RemoteID), Title: req.Title, Body: req.Description, Status: firstNonEmptyString(req.State, "open"), DueOn: req.DueOn, UpdatedAt: now.Format(time.RFC3339)}
		if milestone.Title == "" {
			milestone.Title = firstNonEmptyString(req.Milestone, req.ID, "Milestone "+prior.RemoteID)
		}
		result := gitcode.WriteResult[gitcode.Milestone]{Record: milestone, Confirmed: true, RemoteID: prior.RemoteID, RemoteRevision: firstNonEmptyString(prior.Message, prior.PayloadHash), ConfirmedAt: now}
		_, graph := s.milestoneWriteGraph(repoID, milestone, result, now)
		return graph, nil
	case "trigger-push-mirror":
		route, err := s.BuildAdapterRoute(ctx, repoID, RepositoryScopeIssues)
		if err != nil {
			return cache.RecordGraph{}, err
		}
		mirrors, err := s.client.ListPushRemoteMirrors(ctx, gitcode.PushMirrorListRequest{Owner: route.Owner, Repo: route.Name})
		if err != nil {
			return cache.RecordGraph{}, err
		}
		mirror, err := resolvePushMirror(mirrors, prior.RemoteID)
		if err != nil {
			return cache.RecordGraph{}, err
		}
		_, graph := pushMirrorRecordGraph(repoID, mirror, now)
		return graph, nil
	case "add-comment", "update-comment":
		number := req.Number
		if number == 0 {
			number, _ = strconv.Atoi(prior.RecordID)
		}
		commentID := firstNonEmptyString(req.CommentID, req.ID, prior.RemoteID)
		comment := gitcode.Comment{ID: commentID, Body: req.Body, CreatedAt: now, UpdatedAt: now}
		result := gitcode.WriteResult[gitcode.Comment]{Record: comment, Confirmed: true, RemoteID: commentID, ParentIssueNumber: number, ParentIssueID: prior.RecordID, RemoteRevision: firstNonEmptyString(prior.Message, prior.PayloadHash), ConfirmedAt: now}
		_, graph, err := s.commentWriteGraph(ctx, repoID, number, comment, result, now)
		return graph, err
	case "create-pr", "update-pr", "link-pr-issue":
		number := req.Number
		if number == 0 {
			number, _ = strconv.Atoi(prior.RemoteID)
		}
		pr := gitcode.PullRequest{ID: prior.RemoteID, Number: number, Title: req.Title, Body: req.Body, State: firstNonEmptyString(req.State, "open"), Base: req.Base, Head: req.Head, CreatedAt: now, UpdatedAt: now}
		if pr.Title == "" {
			pr.Title = "Pull request " + strconv.Itoa(number)
		}
		result := gitcode.WriteResult[gitcode.PullRequest]{Record: pr, Confirmed: true, RemoteID: prior.RemoteID, RemoteNumber: number, RemoteRevision: firstNonEmptyString(prior.Message, prior.PayloadHash), ConfirmedAt: now}
		_, graph, err := s.pullRequestWriteGraph(ctx, repoID, pr, result, now)
		return graph, err
	case "add-pr-comment":
		number := req.Number
		comment := gitcode.PRComment{ID: prior.RemoteID, Body: req.Body, PRNumber: number, CreatedAt: now, UpdatedAt: now}
		result := gitcode.WriteResult[gitcode.PRComment]{Record: comment, Confirmed: true, RemoteID: prior.RemoteID, ParentIssueNumber: number, ParentIssueID: strconv.Itoa(number), RemoteRevision: firstNonEmptyString(prior.Message, prior.PayloadHash), ConfirmedAt: now}
		_, graph, err := s.prCommentWriteGraph(ctx, repoID, number, comment, result, now)
		return graph, err
	case "add-pr-review-comment":
		number := req.Number
		comment := gitcode.PRComment{ID: prior.RemoteID, Body: req.Body, PRNumber: number, ReviewKind: "inline", Path: req.Path, Line: req.Line, StartLine: req.StartLine, EndLine: req.EndLine, Position: req.Position, CreatedAt: now, UpdatedAt: now}
		result := gitcode.WriteResult[gitcode.PRComment]{Record: comment, Confirmed: true, RemoteID: prior.RemoteID, ParentIssueNumber: number, ParentIssueID: strconv.Itoa(number), RemoteRevision: firstNonEmptyString(prior.Message, prior.PayloadHash), ConfirmedAt: now}
		_, graph, err := s.prCommentWriteGraph(ctx, repoID, number, comment, result, now)
		return graph, err
	case "reply-pr-review-comment":
		number := req.Number
		discussionID := firstNonEmptyString(prior.RequestMetadata["pr_discussion_id"], req.DiscussionID)
		comment := gitcode.PRComment{ID: prior.RemoteID, Body: req.Body, PRNumber: number, DiscussionID: discussionID, ParentID: req.ParentID, ReviewKind: "inline", CreatedAt: now, UpdatedAt: now}
		result := gitcode.WriteResult[gitcode.PRComment]{Record: comment, Confirmed: true, RemoteID: prior.RemoteID, ParentIssueNumber: number, ParentIssueID: discussionID, RemoteRevision: firstNonEmptyString(prior.Message, prior.PayloadHash), ConfirmedAt: now}
		_, graph, err := s.prCommentWriteGraph(ctx, repoID, number, comment, result, now)
		return graph, err
	case "create-page", "update-page", "delete-page":
		page := gitcode.WikiPage{ID: prior.RemoteID, Slug: firstNonEmptyString(req.Path, req.Slug, req.ID, prior.RemoteID), Title: req.Title, Body: req.Body, Revision: firstNonEmptyString(prior.Message, prior.PayloadHash), CreatedAt: now, UpdatedAt: now}
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
	remoteAlias := remoteID
	if issue.Number > 0 {
		remoteAlias = strconv.Itoa(issue.Number)
	}
	stableID := fallbackSourceID("issue", remoteAlias)
	status := firstNonEmptyString(issue.State, issue.Status, "open")
	updated := issue.UpdatedAt.UTC()
	if updated.IsZero() {
		updated = now
	}
	created := issue.CreatedAt.UTC()
	if created.IsZero() {
		created = updated
	}
	milestoneID := ""
	if issue.Milestone != nil {
		milestoneID = issue.Milestone.RemoteID
	}
	hash := contentHash(issue.Title, issue.Body, status, issue.Labels)
	if milestoneID != "" {
		hash = contentHash(issue.Title, issue.Body, status, issue.Labels, milestoneID)
	}
	revision := firstNonEmptyString(result.RemoteRevision, result.ResponseHash, hash)
	record := cache.Record{RepoID: repoID, ID: stableID, Type: "issue", Path: "issues/" + remoteAlias + ".md", Title: issue.Title, Body: issue.Body, Status: status, Labels: issue.Labels, ContentHash: hash, Provenance: cache.ProvenanceRemote, RemoteType: "issue", RemoteID: remoteAlias, RemoteRevision: revision, CreatedAt: created, UpdatedAt: updated}
	graph := cache.RecordGraph{Record: record, Identities: []cache.Identity{{RepoID: repoID, SourceID: stableID, AliasType: "issue", Alias: remoteAlias, Remote: cache.RemoteAlias{Type: "issue", ID: remoteAlias}}}, ReplaceLinkKinds: []string{"milestone"}, RemoteRevisions: []cache.RemoteRevision{{RepoID: repoID, RecordID: stableID, RemoteType: "issue", RemoteID: remoteAlias, RemoteRevision: revision, Status: "fresh", LastFetchedAt: now}}}
	if remoteID != remoteAlias {
		graph.Identities = append(graph.Identities, cache.Identity{RepoID: repoID, SourceID: stableID, AliasType: "gitcode_issue_id", Alias: remoteID, Remote: cache.RemoteAlias{Type: "gitcode_issue_id", ID: remoteID}})
	}
	if issue.Milestone != nil {
		milestoneResult := gitcode.WriteResult[gitcode.Milestone]{Record: *issue.Milestone, Confirmed: true, RemoteID: issue.Milestone.RemoteID, RemoteRevision: issue.Milestone.UpdatedAt, BrowserURL: issue.Milestone.HTMLURL, ConfirmedAt: now}
		_, milestoneGraph := s.milestoneWriteGraph(repoID, *issue.Milestone, milestoneResult, now)
		graph.RelatedRecords = append(graph.RelatedRecords, milestoneGraph.Record)
		graph.Identities = append(graph.Identities, milestoneGraph.Identities...)
		graph.RemoteRevisions = append(graph.RemoteRevisions, milestoneGraph.RemoteRevisions...)
		graph.Links = append(graph.Links, cache.Link{RepoID: repoID, SourceID: stableID, TargetID: milestoneGraph.Record.ID, Kind: "milestone", Text: issue.Milestone.Title})
	}
	return writeConfirmation{confirmed: result.Confirmed, remoteID: remoteID, remoteNumber: issue.Number, remoteRevision: revision, message: result.Operation, completedAt: firstNonZeroTime(result.ConfirmedAt, now)}, graph
}

func (s *Service) milestoneWriteGraph(repoID string, milestone gitcode.Milestone, result gitcode.WriteResult[gitcode.Milestone], now time.Time) (writeConfirmation, cache.RecordGraph) {
	remoteID := firstNonEmptyString(result.RemoteID, milestone.RemoteID)
	stableID := firstNonEmptyString(milestone.SourceID, fallbackSourceID("milestone", remoteID))
	updated := parseMilestoneTime(milestone.UpdatedAt)
	if updated.IsZero() {
		updated = now
	}
	created := parseMilestoneTime(milestone.CreatedAt)
	if created.IsZero() {
		created = updated
	}
	status := firstNonEmptyString(milestone.Status, "open")
	revision := firstNonEmptyString(result.RemoteRevision, milestone.UpdatedAt, result.ResponseHash, contentHash(milestone.Title, milestone.Body, status, milestone.DueOn))
	record := cache.Record{RepoID: repoID, ID: stableID, Type: "milestone", Path: "milestones/" + remoteID + ".md", Title: milestone.Title, Body: milestone.Body, Status: status, ContentHash: contentHash(milestone.Title, milestone.Body, status, milestone.DueOn), Provenance: cache.ProvenanceRemote, RemoteType: "milestone", RemoteID: remoteID, RemoteRevision: revision, CreatedAt: created, UpdatedAt: updated}
	graph := cache.RecordGraph{Record: record, Identities: []cache.Identity{{RepoID: repoID, SourceID: stableID, AliasType: "milestone", Alias: remoteID, Remote: cache.RemoteAlias{Type: "milestone", ID: remoteID}}}, RemoteRevisions: []cache.RemoteRevision{{RepoID: repoID, RecordID: stableID, RemoteType: "milestone", RemoteID: remoteID, RemoteRevision: revision, Status: "fresh", LastFetchedAt: now}}}
	remoteNumber, _ := strconv.Atoi(remoteID)
	return writeConfirmation{confirmed: result.Confirmed, remoteID: remoteID, remoteNumber: remoteNumber, remoteRevision: revision, browserURL: result.BrowserURL, message: result.Operation, completedAt: firstNonZeroTime(result.ConfirmedAt, now)}, graph
}

func milestoneRecord(m gitcode.Milestone) MilestoneRecord {
	return MilestoneRecord{ID: firstNonEmptyString(m.SourceID, fallbackSourceID("milestone", m.RemoteID)), RemoteID: m.RemoteID, Title: m.Title, Description: m.Body, State: firstNonEmptyString(m.Status, "open"), DueOn: m.DueOn, BrowserURL: m.HTMLURL, CreatedAt: parseMilestoneTime(m.CreatedAt), UpdatedAt: parseMilestoneTime(m.UpdatedAt)}
}

func parseMilestoneTime(raw string) time.Time {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func pushMirrorRecordGraph(repoID string, mirror gitcode.PushMirror, now time.Time) (PushMirrorRecord, cache.RecordGraph) {
	stableID := fallbackSourceID("pushmirror", mirror.RemoteID)
	created := parseMilestoneTime(mirror.CreatedAt)
	updated := parseMilestoneTime(mirror.LastUpdateAt)
	if updated.IsZero() {
		updated = parseMilestoneTime(mirror.LastSuccessfulUpdateAt)
	}
	if updated.IsZero() {
		updated = created
	}
	if updated.IsZero() {
		updated = now
	}
	if created.IsZero() {
		created = updated
	}
	status := firstNonEmptyString(mirror.UpdateStatus, "configured")
	body := fmt.Sprintf(
		"destination: %s\nforce: %t\nprivate: %t\nupdate_status: %s\nnumber_of_failures: %d\nmessage: %s\nlast_update_at: %s\nlast_successful_update_at: %s\n",
		mirror.URL,
		mirror.Force,
		mirror.Private,
		status,
		mirror.NumberOfFailures,
		mirror.Message,
		mirror.LastUpdateAt,
		mirror.LastSuccessfulUpdateAt,
	)
	hash := contentHash(mirror.RemoteID, mirror.ProjectID, mirror.URL, mirror.Force, mirror.Private, status, mirror.NumberOfFailures, mirror.Message, mirror.CreatedAt, mirror.LastUpdateAt, mirror.LastSuccessfulUpdateAt)
	record := cache.Record{
		RepoID:         repoID,
		ID:             stableID,
		Type:           "push_remote_mirror",
		Path:           "push-mirrors/" + mirror.RemoteID + ".md",
		Title:          "Push mirror " + mirror.RemoteID,
		Body:           body,
		Status:         status,
		ContentHash:    hash,
		Provenance:     cache.ProvenanceRemote,
		RemoteType:     "push_remote_mirror",
		RemoteID:       mirror.RemoteID,
		RemoteRevision: hash,
		CreatedAt:      created,
		UpdatedAt:      updated,
	}
	graph := cache.RecordGraph{
		Record: record,
		Identities: []cache.Identity{{
			RepoID:    repoID,
			SourceID:  stableID,
			AliasType: "push_remote_mirror",
			Alias:     mirror.RemoteID,
			Remote:    cache.RemoteAlias{Type: "push_remote_mirror", ID: mirror.RemoteID},
		}},
		RemoteRevisions: []cache.RemoteRevision{{
			RepoID:         repoID,
			RecordID:       stableID,
			RemoteType:     "push_remote_mirror",
			RemoteID:       mirror.RemoteID,
			RemoteRevision: hash,
			Status:         "fresh",
			LastFetchedAt:  now,
		}},
	}
	return PushMirrorRecord{
		ID:                     stableID,
		RemoteID:               mirror.RemoteID,
		ProjectID:              mirror.ProjectID,
		Destination:            mirror.URL,
		Force:                  mirror.Force,
		Private:                mirror.Private,
		UpdateStatus:           mirror.UpdateStatus,
		NumberOfFailures:       mirror.NumberOfFailures,
		Message:                mirror.Message,
		CreatedAt:              parseMilestoneTime(mirror.CreatedAt),
		LastUpdateAt:           parseMilestoneTime(mirror.LastUpdateAt),
		LastSuccessfulUpdateAt: parseMilestoneTime(mirror.LastSuccessfulUpdateAt),
	}, graph
}

func triggerPushMirrorAPIPath(owner, repo, mirrorID string) string {
	return fmt.Sprintf("/api/v5/repos/%s/%s/push_remote_mirrors/%s", url.PathEscape(owner), url.PathEscape(repo), url.PathEscape(mirrorID))
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
	record := cache.Record{RepoID: repoID, ID: stableID, Type: "wiki", Path: normalizeWikiCachePath(remoteID), Title: page.Title, Body: page.Body, Status: "fresh", ContentHash: contentHash(page.Title, page.Body, revision), Provenance: cache.ProvenanceRemote, RemoteType: "wiki", RemoteID: remoteID, RemoteRevision: revision, CreatedAt: created, UpdatedAt: updated}
	graph := cache.RecordGraph{Record: record, Identities: []cache.Identity{{RepoID: repoID, SourceID: stableID, AliasType: "wiki", Alias: remoteID, Remote: cache.RemoteAlias{Type: "wiki", ID: remoteID}}}, RemoteRevisions: []cache.RemoteRevision{{RepoID: repoID, RecordID: stableID, RemoteType: "wiki", RemoteID: remoteID, RemoteRevision: revision, Status: "fresh", LastFetchedAt: now}}}
	return writeConfirmation{confirmed: result.Confirmed, remoteID: remoteID, remoteSlug: remoteID, remoteRevision: revision, apiPath: result.APIPath, cachePath: firstNonEmptyString(result.CachePath, record.Path), browserURL: result.BrowserURL, message: result.Operation, completedAt: firstNonZeroTime(result.ConfirmedAt, now)}, graph
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

func (s *Service) pullRequestWriteGraph(ctx context.Context, repoID string, pr gitcode.PullRequest, result gitcode.WriteResult[gitcode.PullRequest], now time.Time) (writeConfirmation, cache.RecordGraph, error) {
	pr.Number = firstNonZeroInt(pr.Number, result.RemoteNumber)
	remoteID := strconv.Itoa(pr.Number)
	if remoteID == "0" {
		remoteID = firstNonEmptyString(result.RemoteID, pr.ID)
	}
	sourceGraph, _, err := s.stagePullRequest(ctx, SyncRequest{RepoID: repoID}, "pull_request", remoteID, pr)
	if err != nil {
		return writeConfirmation{}, cache.RecordGraph{}, err
	}
	graph := recordGraphFromSourceGraph(sourceGraph)
	revision := firstNonEmptyString(result.RemoteRevision, sourceGraph.SyncStatus.RemoteRevision, result.ResponseHash)
	if len(graph.RemoteRevisions) > 0 {
		graph.RemoteRevisions[0].RemoteRevision = revision
	}
	return writeConfirmation{confirmed: result.Confirmed, remoteID: remoteID, remoteNumber: pr.Number, remoteRevision: revision, message: result.Operation, completedAt: firstNonZeroTime(result.ConfirmedAt, now)}, graph, nil
}

func (s *Service) prCommentWriteGraph(ctx context.Context, repoID string, number int, comment gitcode.PRComment, result gitcode.WriteResult[gitcode.PRComment], now time.Time) (writeConfirmation, cache.RecordGraph, error) {
	comment.PRNumber = firstNonZeroInt(comment.PRNumber, number, result.ParentIssueNumber)
	remoteCommentID := firstNonEmptyString(result.RemoteID, comment.ID)
	remoteID := prCommentRemoteID(comment.PRNumber, remoteCommentID)
	sourceGraph, _, err := s.stagePRComment(ctx, SyncRequest{RepoID: repoID}, "pr_comment", remoteID, comment.PRNumber, comment)
	if err != nil {
		return writeConfirmation{}, cache.RecordGraph{}, err
	}
	graph := recordGraphFromSourceGraph(sourceGraph)
	revision := firstNonEmptyString(result.RemoteRevision, sourceGraph.SyncStatus.RemoteRevision, result.ResponseHash)
	if len(graph.RemoteRevisions) > 0 {
		graph.RemoteRevisions[0].RemoteRevision = revision
	}
	return writeConfirmation{confirmed: result.Confirmed, remoteID: remoteCommentID, remoteNumber: comment.PRNumber, remoteRevision: revision, message: result.Operation, completedAt: firstNonZeroTime(result.ConfirmedAt, now), parentRemoteID: firstNonEmptyString(result.ParentIssueID, comment.DiscussionID)}, graph, nil
}

func (s *Service) prCommentThreadWriteGraph(ctx context.Context, repoID string, number int, result gitcode.WriteResult[gitcode.PRComment], now time.Time) (writeConfirmation, cache.RecordGraph, error) {
	confirmation, graph, err := s.prCommentWriteGraph(ctx, repoID, number, result.Record, result, now)
	if err != nil {
		return writeConfirmation{}, cache.RecordGraph{}, err
	}
	seen := map[string]bool{result.Record.ID: true}
	for _, comment := range result.Record.Thread {
		if comment.ID == "" || seen[comment.ID] {
			continue
		}
		seen[comment.ID] = true
		threadResult := gitcode.WriteResult[gitcode.PRComment]{Record: comment, Confirmed: true, Operation: "ReplyPRReviewCommentReadback", RemoteID: comment.ID, ParentIssueNumber: number, ParentIssueID: comment.DiscussionID, ConfirmedAt: result.ConfirmedAt}
		_, related, err := s.prCommentWriteGraph(ctx, repoID, number, comment, threadResult, now)
		if err != nil {
			return writeConfirmation{}, cache.RecordGraph{}, err
		}
		graph.RelatedRecords = append(graph.RelatedRecords, related.Record)
		graph.Comments = append(graph.Comments, related.Comments...)
		graph.PRReviewComments = append(graph.PRReviewComments, related.PRReviewComments...)
		graph.PRReviewDiscussions = append(graph.PRReviewDiscussions, related.PRReviewDiscussions...)
		graph.PRReviewPositions = append(graph.PRReviewPositions, related.PRReviewPositions...)
		graph.Identities = append(graph.Identities, related.Identities...)
		graph.Links = append(graph.Links, related.Links...)
		graph.RemoteRevisions = append(graph.RemoteRevisions, related.RemoteRevisions...)
		graph.SyncEvents = append(graph.SyncEvents, related.SyncEvents...)
	}
	return confirmation, graph, nil
}

func (s *Service) prIssueLinkWriteGraph(ctx context.Context, repoID string, prNumber int, issueNumber int, result gitcode.WriteResult[[]gitcode.Issue], now time.Time) (writeConfirmation, cache.RecordGraph, error) {
	remoteID := strconv.Itoa(firstNonZeroInt(result.RemoteNumber, prNumber))
	stableID := s.resolveOrFallback(ctx, repoID, "pull_request", remoteID, fallbackSourceID("pull_request", remoteID))
	record, err := s.store.GetRecord(ctx, repoID, stableID)
	if err != nil {
		record = cache.Record{RepoID: repoID, ID: stableID, Type: "pull_request", Path: "pulls/" + remoteID + ".md", Title: "Pull request " + remoteID, Status: "open", ContentHash: contentHash(remoteID), Provenance: cache.ProvenanceRemote, RemoteType: "pull_request", RemoteID: remoteID, CreatedAt: now, UpdatedAt: now}
	}
	revision := firstNonEmptyString(result.RemoteRevision, result.ResponseHash, contentHash(remoteID, strconv.Itoa(issueNumber), result.Record))
	record.RemoteType = "pull_request"
	record.RemoteID = remoteID
	record.RemoteRevision = revision
	record.UpdatedAt = now
	graph := cache.RecordGraph{Record: record, Identities: []cache.Identity{{RepoID: repoID, SourceID: stableID, AliasType: "pull_request", Alias: remoteID, Remote: cache.RemoteAlias{Type: "pull_request", ID: remoteID}}}, RemoteRevisions: []cache.RemoteRevision{{RepoID: repoID, RecordID: stableID, RemoteType: "pull_request", RemoteID: remoteID, RemoteRevision: revision, Status: "fresh", LastFetchedAt: now}}}
	return writeConfirmation{confirmed: result.Confirmed, remoteID: remoteID, remoteNumber: prNumber, remoteRevision: revision, message: result.Operation, completedAt: firstNonZeroTime(result.ConfirmedAt, now)}, graph, nil
}

func (s *Service) linkPRIssueDescriptionFallback(ctx context.Context, route RepositoryRoute, req WriteCommandRequest, opts gitcode.WriteOptions, now time.Time) (writeConfirmation, cache.RecordGraph, error) {
	pr, err := s.client.GetPR(ctx, gitcode.PRRequest{Owner: route.Owner, Repo: route.Name, Number: req.Number})
	if err != nil {
		return writeConfirmation{}, cache.RecordGraph{}, err
	}
	body := linkPRIssueBody(pr.Body, req.IssueNumber)
	result, err := s.client.UpdatePR(ctx, gitcode.UpdatePRRequest{Owner: route.Owner, Repo: route.Name, Number: req.Number, Body: body}, opts)
	if err != nil {
		return writeConfirmation{}, cache.RecordGraph{}, err
	}
	return s.pullRequestWriteGraph(ctx, route.RepoID, result.Record, result, now)
}

func isPRIssueRelationUnsupported(err error) bool {
	var unsupported gitcode.ErrUnsupportedCapability
	return errors.As(err, &unsupported) && unsupported.CapabilityKey == "pr_issue_relation"
}

func recordGraphFromSourceGraph(graph cache.SourceGraph) cache.RecordGraph {
	record := cache.Record{RepoID: graph.Source.RepoID, ID: graph.Source.ID, Type: graph.Source.Kind, Path: graph.Source.Path, Title: graph.Source.Title, Body: graph.Source.Body, Status: graph.Source.Status, Labels: graph.Source.Labels, ContentHash: graph.Source.ContentHash, Provenance: cache.ProvenanceRemote, CreatedAt: graph.Source.CreatedAt, UpdatedAt: graph.Source.UpdatedAt}
	out := cache.RecordGraph{Record: record, Comments: graph.Comments, PRReviewComments: graph.PRReviewComments, PRReviewDiscussions: graph.PRReviewDiscussions, PRReviewPositions: graph.PRReviewPositions, Identities: graph.Identities, Links: graph.Links, SyncEvents: graph.SyncEvents}
	if graph.SyncStatus != nil {
		record.RemoteType = graph.SyncStatus.RemoteType
		record.RemoteID = graph.SyncStatus.RemoteID
		record.RemoteRevision = graph.SyncStatus.RemoteRevision
		out.Record = record
		out.RemoteRevisions = []cache.RemoteRevision{{RepoID: graph.SyncStatus.RepoID, RecordID: graph.Source.ID, RemoteType: graph.SyncStatus.RemoteType, RemoteID: graph.SyncStatus.RemoteID, RemoteRevision: graph.SyncStatus.RemoteRevision, Status: graph.SyncStatus.Status, LastFetchedAt: graph.SyncStatus.LastFetchedAt}}
	}
	return out
}

func linkPRIssueBody(body string, issueNumber int) string {
	marker := fmt.Sprintf("<!-- gitcode-mcp-link:issue:%d -->", issueNumber)
	if strings.Contains(body, marker) {
		return body
	}
	line := fmt.Sprintf("%s\nFixes #%d", marker, issueNumber)
	trimmed := strings.TrimRight(body, "\n")
	if strings.TrimSpace(trimmed) == "" {
		return line
	}
	return trimmed + "\n\n" + line
}

func isReleaseNotFound(err error) bool {
	var notFound gitcode.ErrNotFound
	return errors.As(err, &notFound)
}

func releaseStatus(value string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "latest":
		return gitcode.ReleaseStatusLatest, nil
	case "unset", "default":
		return gitcode.ReleaseStatusUnset, nil
	case "pre", "prerelease", "pre-release":
		return gitcode.ReleaseStatusPreRelease, nil
	default:
		return 0, ErrInvalidQuery{Field: "status", Message: "release status must be latest, prerelease, or unset"}
	}
}

func releaseBodyWithAssets(body string, assets []PublishAssetLink) string {
	body = strings.TrimRight(body, "\n")
	if len(assets) == 0 {
		return body
	}
	var b strings.Builder
	b.WriteString(body)
	if !strings.Contains(body, "## Assets") {
		b.WriteString("\n\n## Assets")
	}
	for _, asset := range assets {
		name := strings.TrimSpace(asset.Name)
		url := strings.TrimSpace(asset.URL)
		if name == "" || url == "" || strings.Contains(body, url) {
			continue
		}
		b.WriteString("\n- [")
		b.WriteString(name)
		b.WriteString("](")
		b.WriteString(url)
		b.WriteString(")")
	}
	return b.String()
}

func releaseIdempotency(req PublishReleaseRequest, status int) (string, string) {
	payload, _ := json.Marshal(struct {
		Command string
		RepoID  string
		Tag     string
		Ref     string
		Title   string
		Body    string
		Status  int
		Assets  []PublishAssetLink
	}{
		Command: "publish-release",
		RepoID:  firstNonEmptyString(req.RepoID, req.Repo),
		Tag:     strings.TrimSpace(req.Tag),
		Ref:     strings.TrimSpace(req.Ref),
		Title:   strings.TrimSpace(req.Title),
		Body:    req.Body,
		Status:  status,
		Assets:  req.Assets,
	})
	sum := sha256.Sum256(payload)
	fingerprint := hex.EncodeToString(sum[:])
	key := strings.TrimSpace(req.IdempotencyKey)
	if key == "" {
		key = "publish-release-" + fingerprint
		if len(key) > 64 {
			key = key[:64]
		}
	}
	return key, fingerprint
}

func writeIdempotency(command string, req WriteCommandRequest) (string, string) {
	payload, _ := json.Marshal(struct {
		Command        string
		RepoID         string
		ID             string
		Number         int
		IssueNumber    int
		DiscussionID   string
		ParentID       string
		Slug           string
		Path           string
		Sha            string
		Line           int
		Position       int
		StartLine      int
		EndLine        int
		Title          string
		Body           string
		Description    string
		DueOn          string
		Milestone      string
		ClearMilestone bool
		Head           string
		Base           string
		State          string
		Label          string
		Labels         []string
		Strategy       string
	}{command, req.RepoID, req.ID, req.Number, req.IssueNumber, req.DiscussionID, req.ParentID, req.Slug, req.Path, req.Sha, req.Line, req.Position, req.StartLine, req.EndLine, strings.TrimSpace(req.Title), req.Body, req.Description, strings.TrimSpace(req.DueOn), strings.TrimSpace(req.Milestone), req.ClearMilestone, strings.TrimSpace(req.Head), strings.TrimSpace(req.Base), req.State, strings.TrimSpace(req.Label), req.Labels, strings.TrimSpace(req.Strategy)})
	sum := sha256.Sum256(payload)
	fingerprint := hex.EncodeToString(sum[:])
	if override := strings.TrimSpace(req.idempotencyFingerprint); override != "" {
		fingerprint = override
	}
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
	return firstNonEmptyString(req.Milestone, req.Path, req.Slug)
}

func replayWriteResult(command string, entry cache.AuditTrailEntry, fingerprint string, now time.Time) WriteCommandResult {
	return WriteCommandResult{Command: command, Status: "already_applied", RepoID: entry.RepoID, ID: entry.RecordID, RemoteID: entry.RemoteID, IdempotencyKey: entry.IdempotencyKey, SourceFingerprint: fingerprint, Replayed: true, Milestone: milestoneReceiptFromAudit(entry), PushMirror: pushMirrorReceiptFromAudit(entry), Evidence: "replayed from audit_trail", GeneratedAt: now}
}

func milestoneReceiptFromAudit(entry cache.AuditTrailEntry) *WriteMilestoneReceipt {
	metadata := entry.RequestMetadata
	if len(metadata) == 0 {
		return nil
	}
	receipt := &WriteMilestoneReceipt{
		ID:       metadata["milestone_id"],
		RemoteID: metadata["milestone_remote_id"],
		Title:    metadata["milestone_title"],
		Cleared:  metadata["milestone_cleared"] == "true",
	}
	if receipt.ID == "" && receipt.RemoteID == "" && receipt.Title == "" && !receipt.Cleared {
		return nil
	}
	return receipt
}

func pushMirrorReceiptFromAudit(entry cache.AuditTrailEntry) *WritePushMirrorReceipt {
	metadata := entry.RequestMetadata
	if len(metadata) == 0 || strings.TrimSpace(metadata["push_mirror_id"]) == "" {
		return nil
	}
	triggeredAt, _ := time.Parse(time.RFC3339Nano, metadata["push_mirror_triggered_at"])
	return &WritePushMirrorReceipt{
		MirrorID:       metadata["push_mirror_id"],
		Status:         firstNonEmptyString(metadata["push_mirror_status"], "triggered"),
		PreviousStatus: metadata["push_mirror_previous_status"],
		TriggeredAt:    triggeredAt,
	}
}

func (s *Service) writeAdapterErrorCode(mode WriteMode, err error) string {
	if mode == WriteModeLive && gitcode.IsFixtureReadOnly(err) {
		return "write_fixture_fallback_detected"
	}
	return writeErrorCode(err)
}

func writeErrorCode(err error) string {
	var replyUnavailable gitcode.ErrDiscussionReplyUnavailable
	if errors.As(err, &replyUnavailable) {
		return "discussion_reply_unavailable"
	}
	var anchorMismatch gitcode.ErrPRReviewAnchorMismatch
	if errors.As(err, &anchorMismatch) {
		return "pr_review_anchor_mismatch"
	}
	var incomplete gitcode.ErrWriteConfirmationIncomplete
	if errors.As(err, &incomplete) {
		return "write_confirmation_incomplete"
	}
	var schema *gitcode.ErrSchemaDecode
	if errors.As(err, &schema) {
		return "schema_decode"
	}
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
	var mirrorInProgress gitcode.ErrPushMirrorSyncInProgress
	if errors.As(err, &mirrorInProgress) {
		return "push_mirror_sync_in_progress"
	}
	var forbidden gitcode.ErrForbidden
	if errors.As(err, &forbidden) {
		return "write_forbidden"
	}
	var notFound gitcode.ErrRemoteNotFound
	if errors.As(err, &notFound) {
		return "push_mirror_not_found"
	}
	var network gitcode.ErrNetworkUnavailable
	if errors.As(err, &network) {
		return "write_network_unavailable"
	}
	return "write_provider_error"
}

func safePushMirrorTriggerFailure(err error) bool {
	var inProgress gitcode.ErrPushMirrorSyncInProgress
	if errors.As(err, &inProgress) {
		return true
	}
	var auth gitcode.ErrAuthExpired
	if errors.As(err, &auth) {
		return true
	}
	var forbidden gitcode.ErrForbidden
	if errors.As(err, &forbidden) {
		return true
	}
	var notFound gitcode.ErrRemoteNotFound
	if errors.As(err, &notFound) {
		return true
	}
	var validation gitcode.ErrAPIValidation
	if errors.As(err, &validation) {
		return true
	}
	var limited gitcode.ErrRateLimited
	return errors.As(err, &limited)
}

func writeFailureCause(code string, err error) error {
	if code == "write_fixture_fallback_detected" {
		return nil
	}
	return err
}

func failureSource(err error) string {
	var tooLarge gitcode.ErrPayloadTooLarge
	if errors.As(err, &tooLarge) {
		return tooLarge.Source
	}
	var schema *gitcode.ErrSchemaDecode
	if errors.As(err, &schema) {
		return "schema_decode"
	}
	var partial gitcode.ErrPartialResponse
	if errors.As(err, &partial) {
		return "partial_response"
	}
	return ""
}

func remoteWriteID(err error) string {
	var remote interface{ RemoteWriteID() string }
	if errors.As(err, &remote) {
		return strings.TrimSpace(remote.RemoteWriteID())
	}
	return ""
}

func writeCommandRemoteType(command string) string {
	switch command {
	case "add-pr-comment", "add-pr-review-comment", "reply-pr-review-comment":
		return "pr_comment"
	case "add-comment", "update-comment":
		return "issue_comment"
	case "create-issue", "update-issue":
		return "issue"
	case "create-page", "update-page", "delete-page":
		return "wiki"
	default:
		return "remote"
	}
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
