package gitcode

import (
	"context"
	"errors"
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
	ListWikiPages(context.Context, WikiListRequest) (Page[WikiPage], error)
	GetWikiPage(context.Context, WikiPageRequest) (WikiPage, error)
	Search(context.Context, SearchRequest) (Page[SearchResult], error)
	CreateIssue(context.Context, CreateIssueRequest, WriteOptions) (WriteResult[Issue], error)
	UpdateIssue(context.Context, UpdateIssueRequest, WriteOptions) (WriteResult[Issue], error)
	CreateIssueComment(context.Context, CreateIssueCommentRequest, WriteOptions) (WriteResult[Comment], error)
	CreateWikiPage(context.Context, CreateWikiPageRequest, WriteOptions) (WriteResult[WikiPage], error)
	UpdateWikiPage(context.Context, UpdateWikiPageRequest, WriteOptions) (WriteResult[WikiPage], error)
}

type ProviderMode string

const (
	ProviderModeLive        ProviderMode = "live"
	ProviderModeFixture     ProviderMode = "fixture"
	ProviderModeUnavailable ProviderMode = "unavailable"
)

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

func NewLiveProvider(cfg ProviderConfig) (Provider, error) {
	baseURL, err := validateLiveProviderConfig(cfg)
	if err != nil {
		return nil, err
	}
	client, err := newHTTPClientForProvider(Config{BaseURL: baseURL, Token: cfg.Token, Timeout: cfg.Timeout, MaxResponseSize: cfg.MaxResponseSize, MaxRetries: cfg.MaxRetries, UserAgent: cfg.UserAgent, Pagination: cfg.Pagination})
	if err != nil {
		return nil, err
	}
	return liveProvider{HTTPClient: client}, nil
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
func (p unavailableProvider) CreateWikiPage(context.Context, CreateWikiPageRequest, WriteOptions) (WriteResult[WikiPage], error) {
	return WriteResult[WikiPage]{}, p.err()
}
func (p unavailableProvider) UpdateWikiPage(context.Context, UpdateWikiPageRequest, WriteOptions) (WriteResult[WikiPage], error) {
	return WriteResult[WikiPage]{}, p.err()
}

func IsProviderUnavailable(err error) bool {
	var target ErrProviderUnavailable
	return errors.As(err, &target)
}
