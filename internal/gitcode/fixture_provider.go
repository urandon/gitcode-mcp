package gitcode

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type fixtureProvider struct {
	cfg      FixtureConfig
	repo     Repo
	issues   []IssueSummary
	issue    map[int]Issue
	comments map[int][]Comment
	wikis    []WikiPage
	wiki     map[string]WikiPage
	search   []SearchResult
}

func NewFixtureProvider(cfg FixtureConfig) (Provider, error) {
	if cfg.Owner == "" {
		cfg.Owner = "example-owner"
	}
	if cfg.Repo == "" {
		cfg.Repo = "example-repo"
	}
	if cfg.RootDir == "" {
		cfg.RootDir = filepath.Join("..", "..", "fixtures")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.example.com"
	}
	p := &fixtureProvider{cfg: cfg, issue: map[int]Issue{}, comments: map[int][]Comment{}, wiki: map[string]WikiPage{}}
	p.repo = Repo{ID: "REPO-EXAMPLE", Owner: cfg.Owner, Name: cfg.Repo, FullName: cfg.Owner + "/" + cfg.Repo, DefaultRef: "main", Description: "sanitized fixture repository", HTMLURL: cfg.BaseURL + "/" + cfg.Owner + "/" + cfg.Repo}
	if err := p.load(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *fixtureProvider) load() error {
	base := filepath.Join(p.cfg.RootDir, "api", "v5", "repos", p.cfg.Owner, p.cfg.Repo)
	if err := readFixture(filepath.Join(base, "issues.json"), &p.issues); err != nil {
		return err
	}
	for _, item := range p.issues {
		var issue Issue
		if err := readFixture(filepath.Join(base, "issues", strconv.Itoa(item.Number)+".json"), &issue); err == nil {
			p.issue[issue.Number] = issue
		} else if errors.Is(err, os.ErrNotExist) {
			p.issue[item.Number] = Issue{ID: item.ID, Number: item.Number, Title: item.Title, Status: item.Status, State: item.State, Labels: append([]string(nil), item.Labels...), CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}
		} else {
			return err
		}
		var comments []Comment
		if err := readFixture(filepath.Join(base, "issues", strconv.Itoa(item.Number), "comments.json"), &comments); err == nil {
			p.comments[item.Number] = comments
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := readFixture(filepath.Join(base, "wiki", "pages.json"), &p.wikis); err != nil {
		return err
	}
	for _, page := range p.wikis {
		var full WikiPage
		if err := readFixture(filepath.Join(base, "wiki", page.Slug+".json"), &full); err == nil {
			p.wiki[full.Slug] = full
			if full.ID != "" {
				p.wiki[full.ID] = full
			}
		} else if errors.Is(err, os.ErrNotExist) {
			p.wiki[page.Slug] = page
			if page.ID != "" {
				p.wiki[page.ID] = page
			}
		} else {
			return err
		}
	}
	for _, issue := range p.issue {
		p.search = append(p.search, SearchResult{ID: issue.ID, Type: "issue", Title: issue.Title, Body: issue.Body, URL: p.cfg.BaseURL + "/" + p.cfg.Owner + "/" + p.cfg.Repo + "/issues/" + strconv.Itoa(issue.Number), Score: 1, CreatedAt: issue.CreatedAt, UpdatedAt: issue.UpdatedAt})
	}
	for _, page := range p.wiki {
		p.search = append(p.search, SearchResult{ID: page.ID, Type: "wiki", Title: page.Title, Body: page.Body, URL: p.cfg.BaseURL + "/" + p.cfg.Owner + "/" + p.cfg.Repo + "/wiki/" + page.Slug, Score: 1, CreatedAt: page.CreatedAt, UpdatedAt: page.UpdatedAt})
	}
	sort.Slice(p.search, func(i, j int) bool { return p.search[i].ID < p.search[j].ID })
	return nil
}

func readFixture(path string, out any) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

func (p *fixtureProvider) FixtureBoundaryMode() string {
	return FixtureBoundaryMode
}

func (p *fixtureProvider) FixtureMarkerIDs() []string {
	return FixtureMarkerIDs()
}

func (p *fixtureProvider) ProbeAuth(context.Context, AuthProbeRequest) (AuthProbeResult, error) {
	if err := p.scenarioError("auth"); err != nil {
		return AuthProbeResult{}, err
	}
	return AuthProbeResult{Authenticated: true, TokenPresent: false, Scopes: []string{"issues", "wiki"}, User: "fixture-user", CheckedAt: time.Date(2026, 6, 18, 12, 0, 0, 0, time.UTC), Mode: string(ProviderModeFixture)}, nil
}

func (p *fixtureProvider) GetRepo(context.Context, RepoRequest) (Repo, error) {
	if err := p.scenarioError("repo"); err != nil {
		return Repo{}, err
	}
	return p.repo, nil
}

func (p *fixtureProvider) ListIssues(ctx context.Context, req IssueListRequest) (Page[IssueSummary], error) {
	if err := p.scenarioError("issues"); err != nil {
		return Page[IssueSummary]{}, err
	}
	items := filterIssues(p.issues, req)
	return paginateFixture(ctx, listIssuesEndpoint(req.Owner, req.Repo), items, PageState{Page: req.Page, PerPage: req.PerPage}, p.cfg.Pagination, p.cfg.Scenario)
}

func (p *fixtureProvider) GetIssue(_ context.Context, req IssueRequest) (Issue, error) {
	if err := p.scenarioError("issue"); err != nil {
		return Issue{}, err
	}
	issue, ok := p.issue[req.Number]
	if !ok {
		return Issue{}, ErrNotFound{Endpoint: getIssueEndpoint(req.Owner, req.Repo, req.Number), ID: strconv.Itoa(req.Number)}
	}
	return issue, nil
}

func (p *fixtureProvider) ListIssueComments(ctx context.Context, req IssueRequest) (Page[Comment], error) {
	if err := p.scenarioError("comments"); err != nil {
		return Page[Comment]{}, err
	}
	return paginateFixture(ctx, listIssueCommentsEndpoint(req.Owner, req.Repo, req.Number), append([]Comment(nil), p.comments[req.Number]...), PageState{}, p.cfg.Pagination, p.cfg.Scenario)
}

func (p *fixtureProvider) ListPRs(context.Context, PRListRequest) (Page[PullRequest], error) {
	return Page[PullRequest]{}, FixtureReadOnlyError("ListPRs")
}

func (p *fixtureProvider) GetPR(context.Context, PRRequest) (PullRequest, error) {
	return PullRequest{}, FixtureReadOnlyError("GetPR")
}

func (p *fixtureProvider) ListPRComments(context.Context, PRRequest) (Page[PRComment], error) {
	return Page[PRComment]{}, FixtureReadOnlyError("ListPRComments")
}

func (p *fixtureProvider) CreatePR(context.Context, CreatePRRequest, WriteOptions) (WriteResult[PullRequest], error) {
	return WriteResult[PullRequest]{}, FixtureReadOnlyError("CreatePR")
}

func (p *fixtureProvider) UpdatePR(context.Context, UpdatePRRequest, WriteOptions) (WriteResult[PullRequest], error) {
	return WriteResult[PullRequest]{}, FixtureReadOnlyError("UpdatePR")
}

func (p *fixtureProvider) LinkPRIssue(context.Context, LinkPRIssueRequest, WriteOptions) (WriteResult[[]Issue], error) {
	return WriteResult[[]Issue]{}, FixtureReadOnlyError("LinkPRIssue")
}

func (p *fixtureProvider) ListWikiPages(ctx context.Context, req WikiListRequest) (Page[WikiPage], error) {
	if err := p.scenarioError("wiki"); err != nil {
		return Page[WikiPage]{}, err
	}
	return paginateFixture(ctx, listWikiPagesEndpoint(req.Owner, req.Repo), append([]WikiPage(nil), p.wikis...), PageState{Page: req.Page, PerPage: req.PerPage}, p.cfg.Pagination, p.cfg.Scenario)
}

func (p *fixtureProvider) GetWikiPage(_ context.Context, req WikiPageRequest) (WikiPage, error) {
	if err := p.scenarioError("wiki-page"); err != nil {
		return WikiPage{}, err
	}
	wikiPath := wikiRequestPath(req.Path, req.Slug)
	page, ok := p.wiki[wikiPath]
	if !ok {
		return WikiPage{}, ErrNotFound{Endpoint: getWikiPageEndpoint(req.Owner, req.Repo, wikiPath), ID: wikiPath}
	}
	return page, nil
}

func (p *fixtureProvider) Search(ctx context.Context, req SearchRequest) (Page[SearchResult], error) {
	if err := p.scenarioError("search"); err != nil {
		return Page[SearchResult]{}, err
	}
	items := make([]SearchResult, 0, len(p.search))
	query := strings.ToLower(strings.TrimSpace(req.Query))
	for _, item := range p.search {
		if req.Type != "" && item.Type != req.Type {
			continue
		}
		if query == "" || strings.Contains(strings.ToLower(item.Title), query) || strings.Contains(strings.ToLower(item.Body), query) {
			items = append(items, item)
		}
	}
	return paginateFixture(ctx, searchIssuesEndpoint(), items, PageState{Page: req.Page, PerPage: req.PerPage}, p.cfg.Pagination, p.cfg.Scenario)
}

func (p *fixtureProvider) CreateIssue(context.Context, CreateIssueRequest, WriteOptions) (WriteResult[Issue], error) {
	return WriteResult[Issue]{}, FixtureReadOnlyError("CreateIssue")
}

func (p *fixtureProvider) UpdateIssue(context.Context, UpdateIssueRequest, WriteOptions) (WriteResult[Issue], error) {
	return WriteResult[Issue]{}, FixtureReadOnlyError("UpdateIssue")
}

func (p *fixtureProvider) CreateIssueComment(context.Context, CreateIssueCommentRequest, WriteOptions) (WriteResult[Comment], error) {
	return WriteResult[Comment]{}, FixtureReadOnlyError("CreateIssueComment")
}

func (p *fixtureProvider) CreatePRComment(context.Context, CreatePRCommentRequest, WriteOptions) (WriteResult[PRComment], error) {
	return WriteResult[PRComment]{}, FixtureReadOnlyError("CreatePRComment")
}

func (p *fixtureProvider) CreateWikiPage(context.Context, CreateWikiPageRequest, WriteOptions) (WriteResult[WikiPage], error) {
	return WriteResult[WikiPage]{}, FixtureReadOnlyError("CreateWikiPage")
}

func (p *fixtureProvider) UpdateWikiPage(context.Context, UpdateWikiPageRequest, WriteOptions) (WriteResult[WikiPage], error) {
	return WriteResult[WikiPage]{}, FixtureReadOnlyError("UpdateWikiPage")
}

func (p *fixtureProvider) DeleteWikiPage(context.Context, DeleteWikiPageRequest, WriteOptions) (WriteResult[WikiPage], error) {
	return WriteResult[WikiPage]{}, FixtureReadOnlyError("DeleteWikiPage")
}

func (p *fixtureProvider) ListMilestones(context.Context, MilestoneListRequest) (Page[Milestone], error) {
	return Page[Milestone]{}, FixtureReadOnlyError("ListMilestones")
}

func (p *fixtureProvider) GetMilestone(context.Context, MilestoneRequest) (Milestone, error) {
	return Milestone{}, FixtureReadOnlyError("GetMilestone")
}

func (p *fixtureProvider) scenarioError(endpoint string) error {
	switch p.cfg.Scenario {
	case "auth-error":
		return ErrAuthExpired{Endpoint: endpoint, Status: http.StatusUnauthorized, Message: "fixture auth expired"}
	case "conflict":
		return ErrConflict{Endpoint: endpoint, Status: http.StatusConflict, RemotePayload: []byte(`{"message":"fixture conflict"}`), Message: "fixture conflict"}
	case "rate-limit":
		return ErrRateLimited{Endpoint: endpoint, Attempts: 1, RetryAfter: time.Second, RawRetryAfter: "1"}
	default:
		return nil
	}
}

func filterIssues(items []IssueSummary, req IssueListRequest) []IssueSummary {
	out := make([]IssueSummary, 0, len(items))
	for _, item := range items {
		if req.State != "" && item.State != req.State && item.Status != req.State {
			continue
		}
		if len(req.Labels) > 0 && !hasAllLabels(item.Labels, req.Labels) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func hasAllLabels(have, want []string) bool {
	seen := map[string]bool{}
	for _, label := range have {
		seen[label] = true
	}
	for _, label := range want {
		if !seen[label] {
			return false
		}
	}
	return true
}
