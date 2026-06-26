package gitcode

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

type Provider interface {
	ProbeAuth(context.Context, AuthProbeRequest) (AuthProbeResult, error)
	GetRepo(context.Context, RepoRequest) (Repo, error)
	ListIssues(context.Context, IssueListRequest) (Page[IssueSummary], error)
	GetIssue(context.Context, IssueRequest) (Issue, error)
	ListIssueComments(context.Context, IssueRequest) (Page[Comment], error)
	ListPRs(context.Context, PRListRequest) (Page[PullRequest], error)
	GetPR(context.Context, PRRequest) (PullRequest, error)
	ListPRComments(context.Context, PRRequest) (Page[PRComment], error)
	ListWikiPages(context.Context, WikiListRequest) (Page[WikiPage], error)
	GetWikiPage(context.Context, WikiPageRequest) (WikiPage, error)
	Search(context.Context, SearchRequest) (Page[SearchResult], error)
	CreateIssue(context.Context, CreateIssueRequest, WriteOptions) (WriteResult[Issue], error)
	UpdateIssue(context.Context, UpdateIssueRequest, WriteOptions) (WriteResult[Issue], error)
	CreateIssueComment(context.Context, CreateIssueCommentRequest, WriteOptions) (WriteResult[Comment], error)
	CreatePRComment(context.Context, CreatePRCommentRequest, WriteOptions) (WriteResult[PRComment], error)
	CreateWikiPage(context.Context, CreateWikiPageRequest, WriteOptions) (WriteResult[WikiPage], error)
	UpdateWikiPage(context.Context, UpdateWikiPageRequest, WriteOptions) (WriteResult[WikiPage], error)
	DeleteWikiPage(context.Context, DeleteWikiPageRequest, WriteOptions) (WriteResult[WikiPage], error)
	ListMilestones(context.Context, MilestoneListRequest) (Page[Milestone], error)
	GetMilestone(context.Context, MilestoneRequest) (Milestone, error)
}

type ProviderMode string

const (
	ProviderModeLive        ProviderMode = "live"
	ProviderModeFixture     ProviderMode = "fixture"
	ProviderModeUnavailable ProviderMode = "unavailable"
)

const (
	FixtureBoundaryMode = "offline-fixture"
	FixtureIssueMarker  = "ISSUE-42"
	FixtureWikiMarker   = "WIKI-HOME"
)

type FixtureBoundary interface {
	FixtureBoundaryMode() string
	FixtureMarkerIDs() []string
}

func IsFixtureBoundary(v any) bool {
	boundary, ok := v.(FixtureBoundary)
	return ok && boundary.FixtureBoundaryMode() == FixtureBoundaryMode
}

func FixtureMarkerIDs() []string {
	return []string{FixtureIssueMarker, FixtureWikiMarker}
}

type ProviderConfig struct {
	Mode            ProviderMode
	LiveAllowed     bool
	BaseURL         string
	Token           string
	Timeout         time.Duration
	MaxResponseSize int64
	MaxRetries      int
	UserAgent       string
	Pagination      PaginationConfig
}

type FixtureConfig struct {
	RootDir    string
	Owner      string
	Repo       string
	BaseURL    string
	Scenario   string
	Pagination PaginationConfig
}

type RepoRequest struct {
	Owner string
	Repo  string
}

type Repo struct {
	ID          string    `json:"id"`
	Owner       string    `json:"owner"`
	Name        string    `json:"name"`
	FullName    string    `json:"full_name"`
	DefaultRef  string    `json:"default_ref"`
	Description string    `json:"description"`
	HTMLURL     string    `json:"html_url"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type AuthProbeRequest struct {
	Owner string
	Repo  string
}

type AuthProbeResult struct {
	Authenticated bool      `json:"authenticated"`
	TokenPresent  bool      `json:"token_present"`
	Scopes        []string  `json:"scopes"`
	User          string    `json:"user"`
	CheckedAt     time.Time `json:"checked_at"`
	Mode          string    `json:"mode"`
}

type RedactedCapture struct {
	URL     string              `json:"url,omitempty"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    []byte              `json:"body,omitempty"`
	Error   string              `json:"error,omitempty"`
}

var newHTTPClientForProvider = NewHTTPClient

type LiveProviderOption func(*liveProvider)

func WithRouteSchemaMatrix(matrix RouteSchemaMatrix) LiveProviderOption {
	return func(lp *liveProvider) {
		lp.matrix = matrix
	}
}

func NewLiveProvider(cfg ProviderConfig, opts ...LiveProviderOption) (Provider, error) {
	baseURL, err := validateLiveProviderConfig(cfg)
	if err != nil {
		return nil, err
	}
	client, err := newHTTPClientForProvider(Config{BaseURL: baseURL, Token: cfg.Token, Timeout: cfg.Timeout, MaxResponseSize: cfg.MaxResponseSize, MaxRetries: cfg.MaxRetries, UserAgent: cfg.UserAgent, Pagination: cfg.Pagination})
	if err != nil {
		return nil, err
	}
	lp := liveProvider{HTTPClient: client, matrix: DefaultRouteSchemaMatrix()}
	for _, opt := range opts {
		opt(&lp)
	}
	if err := lp.matrix.ValidateCoverage([]ProductArea{
		ProductAreaIssues, ProductAreaLabels, ProductAreaMilestones,
		ProductAreaPullRequests, ProductAreaComments, ProductAreaWiki,
	}); err != nil {
		return nil, fmt.Errorf("gitcode: live provider route schema matrix validation failed: %w", err)
	}
	return lp, nil
}

func validateLiveProviderConfig(cfg ProviderConfig) (string, error) {
	if cfg.Mode != ProviderModeLive || !cfg.LiveAllowed || strings.TrimSpace(cfg.Token) == "" {
		return "", ErrProviderUnavailable{Reason: "live provider requires live mode, explicit live allowance, and token"}
	}
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		return "", ErrProviderUnavailable{Reason: "live provider requires selected absolute http or https base URL"}
	}
	parsed, err := url.Parse(base)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", ErrProviderUnavailable{Reason: "live provider requires selected absolute http or https base URL"}
	}
	return parsed.String(), nil
}

func NewUnavailableProvider(reason string) Provider {
	return unavailableProvider{reason: reason}
}

func RequireLiveProviderForTest() (string, bool) {
	if os.Getenv("GITCODE_LIVE_TEST") != "1" {
		return "", false
	}
	token := strings.TrimSpace(os.Getenv("GITCODE_LIVE_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GITCODE_TEST_TOKEN"))
	}
	return token, token != ""
}

type liveProvider struct {
	*HTTPClient
	matrix RouteSchemaMatrix
}

func (p liveProvider) ProbeAuth(ctx context.Context, req AuthProbeRequest) (AuthProbeResult, error) {
	_, err := p.GetRepo(ctx, RepoRequest{Owner: req.Owner, Repo: req.Repo})
	if err != nil {
		return AuthProbeResult{}, err
	}
	return AuthProbeResult{Authenticated: true, TokenPresent: strings.TrimSpace(p.token) != "", CheckedAt: time.Now().UTC(), Mode: string(ProviderModeLive)}, nil
}

func (p liveProvider) GetRepo(ctx context.Context, req RepoRequest) (Repo, error) {
	var repo Repo
	err := p.getJSON(ctx, getRepoEndpoint(req.Owner, req.Repo), nil, &repo)
	if err != nil {
		return Repo{}, err
	}
	if repo.Owner == "" {
		repo.Owner = req.Owner
	}
	if repo.Name == "" {
		repo.Name = req.Repo
	}
	if repo.FullName == "" {
		repo.FullName = req.Owner + "/" + req.Repo
	}
	return repo, nil
}

func (p liveProvider) ListIssueComments(ctx context.Context, req IssueRequest) (Page[Comment], error) {
	if err := p.matrix.Preflight(ProductAreaComments); err != nil {
		return Page[Comment]{}, err
	}
	return p.HTTPClient.ListIssueComments(ctx, req)
}

func (p liveProvider) CreateIssueComment(ctx context.Context, req CreateIssueCommentRequest, opts WriteOptions) (WriteResult[Comment], error) {
	if err := p.matrix.Preflight(ProductAreaComments); err != nil {
		return WriteResult[Comment]{}, err
	}
	return p.HTTPClient.CreateIssueComment(ctx, req, opts)
}

func (p liveProvider) ListPRs(ctx context.Context, req PRListRequest) (Page[PullRequest], error) {
	if err := p.matrix.Preflight(ProductAreaPullRequests); err != nil {
		return Page[PullRequest]{}, err
	}
	return p.HTTPClient.ListPRs(ctx, req)
}

func (p liveProvider) GetPR(ctx context.Context, req PRRequest) (PullRequest, error) {
	if err := p.matrix.Preflight(ProductAreaPullRequests); err != nil {
		return PullRequest{}, err
	}
	return p.HTTPClient.GetPR(ctx, req)
}

func (p liveProvider) ListPRComments(ctx context.Context, req PRRequest) (Page[PRComment], error) {
	if err := p.matrix.Preflight(ProductAreaPullRequests); err != nil {
		return Page[PRComment]{}, err
	}
	return p.HTTPClient.ListPRComments(ctx, req)
}

func (p liveProvider) CreatePRComment(ctx context.Context, req CreatePRCommentRequest, opts WriteOptions) (WriteResult[PRComment], error) {
	if err := p.matrix.Preflight(ProductAreaPullRequests); err != nil {
		return WriteResult[PRComment]{}, err
	}
	return p.HTTPClient.CreatePRComment(ctx, req, opts)
}

func (p liveProvider) ListMilestones(ctx context.Context, req MilestoneListRequest) (Page[Milestone], error) {
	if err := p.matrix.Preflight(ProductAreaMilestones); err != nil {
		return Page[Milestone]{}, err
	}
	return p.HTTPClient.ListMilestones(ctx, req)
}

func (p liveProvider) GetMilestone(ctx context.Context, req MilestoneRequest) (Milestone, error) {
	if err := p.matrix.Preflight(ProductAreaMilestones); err != nil {
		return Milestone{}, err
	}
	return p.HTTPClient.GetMilestone(ctx, req)
}

func (p liveProvider) AddLabel(ctx context.Context, req LabelRequest, opts WriteOptions) (WriteResult[Issue], error) {
	return WriteResult[Issue]{}, ErrUnsupportedCapability{
		CapabilityKey: "add_label",
		Message:       "add-label is not supported through the live provider: use update-issue --labels instead",
	}
}

type unavailableProvider struct {
	reason string
}

func (p unavailableProvider) err() error {
	if strings.TrimSpace(p.reason) == "" {
		p.reason = "provider unavailable"
	}
	return ErrProviderUnavailable{Reason: p.reason}
}

func (p unavailableProvider) ProbeAuth(context.Context, AuthProbeRequest) (AuthProbeResult, error) {
	return AuthProbeResult{}, p.err()
}
func (p unavailableProvider) GetRepo(context.Context, RepoRequest) (Repo, error) {
	return Repo{}, p.err()
}
func (p unavailableProvider) ListIssues(context.Context, IssueListRequest) (Page[IssueSummary], error) {
	return Page[IssueSummary]{}, p.err()
}
func (p unavailableProvider) GetIssue(context.Context, IssueRequest) (Issue, error) {
	return Issue{}, p.err()
}
func (p unavailableProvider) ListIssueComments(context.Context, IssueRequest) (Page[Comment], error) {
	return Page[Comment]{}, p.err()
}
func (p unavailableProvider) ListPRs(context.Context, PRListRequest) (Page[PullRequest], error) {
	return Page[PullRequest]{}, p.err()
}
func (p unavailableProvider) GetPR(context.Context, PRRequest) (PullRequest, error) {
	return PullRequest{}, p.err()
}
func (p unavailableProvider) ListPRComments(context.Context, PRRequest) (Page[PRComment], error) {
	return Page[PRComment]{}, p.err()
}
func (p unavailableProvider) ListWikiPages(context.Context, WikiListRequest) (Page[WikiPage], error) {
	return Page[WikiPage]{}, p.err()
}
func (p unavailableProvider) GetWikiPage(context.Context, WikiPageRequest) (WikiPage, error) {
	return WikiPage{}, p.err()
}
func (p unavailableProvider) Search(context.Context, SearchRequest) (Page[SearchResult], error) {
	return Page[SearchResult]{}, p.err()
}
func (p unavailableProvider) CreateIssue(context.Context, CreateIssueRequest, WriteOptions) (WriteResult[Issue], error) {
	return WriteResult[Issue]{}, p.err()
}
func (p unavailableProvider) UpdateIssue(context.Context, UpdateIssueRequest, WriteOptions) (WriteResult[Issue], error) {
	return WriteResult[Issue]{}, p.err()
}
func (p unavailableProvider) CreateIssueComment(context.Context, CreateIssueCommentRequest, WriteOptions) (WriteResult[Comment], error) {
	return WriteResult[Comment]{}, p.err()
}
func (p unavailableProvider) CreatePRComment(context.Context, CreatePRCommentRequest, WriteOptions) (WriteResult[PRComment], error) {
	return WriteResult[PRComment]{}, p.err()
}
func (p unavailableProvider) CreateWikiPage(context.Context, CreateWikiPageRequest, WriteOptions) (WriteResult[WikiPage], error) {
	return WriteResult[WikiPage]{}, p.err()
}
func (p unavailableProvider) UpdateWikiPage(context.Context, UpdateWikiPageRequest, WriteOptions) (WriteResult[WikiPage], error) {
	return WriteResult[WikiPage]{}, p.err()
}
func (p unavailableProvider) DeleteWikiPage(context.Context, DeleteWikiPageRequest, WriteOptions) (WriteResult[WikiPage], error) {
	return WriteResult[WikiPage]{}, p.err()
}

func (p unavailableProvider) ListMilestones(context.Context, MilestoneListRequest) (Page[Milestone], error) {
	return Page[Milestone]{}, p.err()
}
func (p unavailableProvider) GetMilestone(context.Context, MilestoneRequest) (Milestone, error) {
	return Milestone{}, p.err()
}

func IsProviderUnavailable(err error) bool {
	var target ErrProviderUnavailable
	return errors.As(err, &target)
}
