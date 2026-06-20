package gitcode

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestFixtureProviderContract(t *testing.T) {
	provider, err := NewFixtureProvider(FixtureConfig{Pagination: PaginationConfig{PerPage: 1}})
	if err != nil {
		t.Fatalf("NewFixtureProvider: %v", err)
	}
	ctx := context.Background()
	auth, err := provider.ProbeAuth(ctx, AuthProbeRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil || !auth.Authenticated || auth.TokenPresent {
		t.Fatalf("unexpected auth probe auth=%+v err=%v", auth, err)
	}
	repo, err := provider.GetRepo(ctx, RepoRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil || repo.FullName != "example-owner/example-repo" {
		t.Fatalf("unexpected repo repo=%+v err=%v", repo, err)
	}
	issues, err := provider.ListIssues(ctx, IssueListRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil || len(issues.Items) != 2 || issues.Items[1].ID != "ISSUE-42" {
		t.Fatalf("unexpected issues page=%+v err=%v", issues, err)
	}
	issue, err := provider.GetIssue(ctx, IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 42})
	if err != nil || issue.ID != "ISSUE-42" || !strings.Contains(issue.Body, "Structured body") {
		t.Fatalf("unexpected issue issue=%+v err=%v", issue, err)
	}
	comments, err := provider.ListIssueComments(ctx, IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 42})
	if err != nil || len(comments.Items) != 1 || comments.Items[0].ID != "COMMENT-1" {
		t.Fatalf("unexpected comments page=%+v err=%v", comments, err)
	}
	wikis, err := provider.ListWikiPages(ctx, WikiListRequest{Owner: "example-owner", Repo: "example-repo"})
	if err != nil || len(wikis.Items) != 2 || wikis.Items[0].Slug != "Home" {
		t.Fatalf("unexpected wikis page=%+v err=%v", wikis, err)
	}
	wiki, err := provider.GetWikiPage(ctx, WikiPageRequest{Owner: "example-owner", Repo: "example-repo", Slug: "Home"})
	if err != nil || wiki.ID != "WIKI-HOME" || !strings.Contains(wiki.Body, "api.example.com") {
		t.Fatalf("unexpected wiki page=%+v err=%v", wiki, err)
	}
	search, err := provider.Search(ctx, SearchRequest{Query: "cache", Owner: "example-owner", Repo: "example-repo"})
	if err != nil || len(search.Items) == 0 {
		t.Fatalf("unexpected search page=%+v err=%v", search, err)
	}
}

func TestFixtureProviderScenarios(t *testing.T) {
	tests := []struct {
		scenario string
		check    func(error) bool
	}{
		{"auth-error", func(err error) bool { var target ErrAuthExpired; return errors.As(err, &target) }},
		{"conflict", func(err error) bool { var target ErrConflict; return errors.As(err, &target) }},
		{"rate-limit", func(err error) bool {
			var target ErrRateLimited
			return errors.As(err, &target) && target.RawRetryAfter == "1"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.scenario, func(t *testing.T) {
			provider, err := NewFixtureProvider(FixtureConfig{Scenario: tt.scenario})
			if err != nil {
				t.Fatalf("NewFixtureProvider: %v", err)
			}
			_, err = provider.ListIssues(context.Background(), IssueListRequest{Owner: "example-owner", Repo: "example-repo"})
			if !tt.check(err) {
				t.Fatalf("unexpected error %T %v", err, err)
			}
		})
	}
}

func TestLiveProviderAdmission(t *testing.T) {
	called := false
	old := newHTTPClientForProvider
	newHTTPClientForProvider = func(Config) (*HTTPClient, error) {
		called = true
		return &HTTPClient{}, nil
	}
	defer func() { newHTTPClientForProvider = old }()
	if _, err := NewLiveProvider(ProviderConfig{Mode: ProviderModeLive, Token: "token"}); !IsProviderUnavailable(err) {
		t.Fatalf("expected unavailable without live allowance, got %T %v", err, err)
	}
	if called {
		t.Fatalf("HTTP client constructed for disallowed live provider")
	}
	if _, err := NewLiveProvider(ProviderConfig{Mode: ProviderModeLive, LiveAllowed: true}); !IsProviderUnavailable(err) {
		t.Fatalf("expected unavailable without token, got %T %v", err, err)
	}
	if called {
		t.Fatalf("HTTP client constructed without token")
	}
	if _, err := NewLiveProvider(ProviderConfig{Mode: ProviderModeLive, LiveAllowed: true, Token: "token"}); err != nil {
		t.Fatalf("expected admitted live provider: %v", err)
	}
	if !called {
		t.Fatalf("HTTP client not constructed for admitted live provider")
	}
}

func TestProviderWriteUnavailableDoesNotConfirm(t *testing.T) {
	providers := map[string]Provider{
		"fixture":     mustFixtureProvider(t),
		"unavailable": NewUnavailableProvider("write disabled"),
	}
	for name, provider := range providers {
		t.Run(name, func(t *testing.T) {
			result, err := provider.CreateIssue(context.Background(), CreateIssueRequest{Owner: "example-owner", Repo: "example-repo", Title: "blocked"}, WriteOptions{IdempotencyKey: "key"})
			if !IsProviderUnavailable(err) {
				t.Fatalf("expected provider unavailable, got %T %v", err, err)
			}
			if result.Confirmed || result.IdempotencyKey != "" || result.ResponseHash != "" || !result.ConfirmedAt.IsZero() {
				t.Fatalf("unavailable provider returned success-shaped metadata: %+v", result)
			}
		})
	}
}

func mustFixtureProvider(t *testing.T) Provider {
	t.Helper()
	provider, err := NewFixtureProvider(FixtureConfig{})
	if err != nil {
		t.Fatalf("NewFixtureProvider: %v", err)
	}
	return provider
}

func TestProviderPaginationGuards(t *testing.T) {
	t.Run("malformed fixture", func(t *testing.T) {
		provider, err := NewFixtureProvider(FixtureConfig{Scenario: "malformed-page"})
		if err != nil {
			t.Fatalf("NewFixtureProvider: %v", err)
		}
		_, err = provider.ListIssues(context.Background(), IssueListRequest{Owner: "example-owner", Repo: "example-repo"})
		var target ErrPaginationMalformed
		if !errors.As(err, &target) {
			t.Fatalf("expected ErrPaginationMalformed, got %T %v", err, err)
		}
	})
	t.Run("loop fixture", func(t *testing.T) {
		provider, err := NewFixtureProvider(FixtureConfig{Scenario: "pagination-loop"})
		if err != nil {
			t.Fatalf("NewFixtureProvider: %v", err)
		}
		_, err = provider.ListIssues(context.Background(), IssueListRequest{Owner: "example-owner", Repo: "example-repo"})
		var target ErrPaginationLoop
		if !errors.As(err, &target) {
			t.Fatalf("expected ErrPaginationLoop, got %T %v", err, err)
		}
	})
}

func TestRedactedCapture(t *testing.T) {
	capture := NewRedactedCapture("https://secret.gitcode.invalid/api?access_token=raw-token", http.Header{"Authorization": []string{"Bearer raw-token"}, "Cookie": []string{"sid=raw-cookie"}, "X-Trace": []string{"owner-a/repo-a"}}, []byte(`{\"token\":\"raw-token\",\"owner\":\"owner-a\",\"repo\":\"repo-a\",\"body\":\"Bearer raw-token at secret.gitcode.invalid\"}`), errors.New("Bearer raw-token failed for owner-a"), []string{"api.example.com"}, "raw-token", "raw-cookie", "owner-a", "repo-a", "secret.gitcode.invalid")
	if !CaptureIsSanitized(capture, "raw-token", "raw-cookie", "owner-a", "repo-a", "secret.gitcode.invalid") {
		t.Fatalf("capture not sanitized: %+v body=%s", capture, string(capture.Body))
	}
	if !strings.Contains(capture.URL, "redacted.example.com") {
		t.Fatalf("host not redacted: %s", capture.URL)
	}
}

func TestRequireLiveProviderForTestGate(t *testing.T) {
	t.Setenv("GITCODE_LIVE_TEST", "")
	t.Setenv("GITCODE_LIVE_TOKEN", "token")
	if _, ok := RequireLiveProviderForTest(); ok {
		t.Fatalf("live provider gate admitted without GITCODE_LIVE_TEST=1")
	}
	t.Setenv("GITCODE_LIVE_TEST", "1")
	t.Setenv("GITCODE_LIVE_TOKEN", "")
	t.Setenv("GITCODE_TEST_TOKEN", "")
	if _, ok := RequireLiveProviderForTest(); ok {
		t.Fatalf("live provider gate admitted without token")
	}
	t.Setenv("GITCODE_TEST_TOKEN", "alias-token")
	if token, ok := RequireLiveProviderForTest(); !ok || token != "alias-token" {
		t.Fatalf("live provider gate did not accept transitional alias")
	}
}
