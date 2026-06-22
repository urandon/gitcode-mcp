package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
)

func TestBulkSyncIssuesSyncsListedIssuesAndZeroDeltaOnResync(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 22, 14, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{
			{Items: []gitcode.IssueSummary{{Number: 1, Title: "First", State: "open"}, {Number: 2, Title: "Second", State: "open"}}, Page: 1, PerPage: 100},
			{Items: []gitcode.IssueSummary{{Number: 1, Title: "First", State: "open"}, {Number: 2, Title: "Second", State: "open"}}, Page: 1, PerPage: 100},
		},
		issuesByNumber: map[int]gitcode.Issue{
			1: {Number: 1, Title: "First", Body: "first body", State: "open", CreatedAt: base, UpdatedAt: base},
			2: {Number: 2, Title: "Second", Body: "second body", State: "open", CreatedAt: base, UpdatedAt: base},
		},
		commentsByIssue: map[int][]gitcode.Comment{
			1: {{ID: "c1", Author: "author", Body: "comment one", CreatedAt: base, UpdatedAt: base}},
		},
	}
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "bulk-issues", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)
	first, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{RepoID: "bulk-issues", IdempotencyKey: "bulk-issues-first", PerPage: 100})
	if err != nil {
		t.Fatalf("BulkSyncIssues first returned error: %v", err)
	}
	if first.SuccessCount != 2 || first.FailureCount != 0 {
		t.Fatalf("first counts = success %d failure %d, want 2/0", first.SuccessCount, first.FailureCount)
	}
	if _, err := store.GetSourceScoped(ctx, "bulk-issues", "ISSUE-1"); err != nil {
		t.Fatalf("ISSUE-1 missing: %v", err)
	}
	if _, err := store.GetSourceScoped(ctx, "bulk-issues", "ISSUE-2"); err != nil {
		t.Fatalf("ISSUE-2 missing: %v", err)
	}
	second, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{RepoID: "bulk-issues", IdempotencyKey: "bulk-issues-second", PerPage: 100})
	if err != nil {
		t.Fatalf("BulkSyncIssues second returned error: %v", err)
	}
	if second.SuccessCount != 2 || second.FailureCount != 0 {
		t.Fatalf("second counts = success %d failure %d, want 2/0", second.SuccessCount, second.FailureCount)
	}
	for i, result := range second.Results {
		if !result.ZeroDelta {
			t.Fatalf("second result %d ZeroDelta = false, want true", i)
		}
		if result.Counts.Fetched != 1 || result.Counts.Skipped != 1 || result.Counts.Inserted != 0 || result.Counts.Updated != 0 {
			t.Fatalf("second result %d counts = %#v, want fetched/skipped only", i, result.Counts)
		}
	}
	sources, err := store.ListSources(ctx, cache.SourceFilter{RepoID: "bulk-issues", Kind: "issue"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 {
		t.Fatalf("issue source count = %d, want 2", len(sources))
	}
}

func TestBulkSyncWikiPartialFailureCollectsSuccessAndFailure(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 22, 15, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listWikiPages: []gitcode.Page[gitcode.WikiPage]{
			{Items: []gitcode.WikiPage{{Slug: "Home", Title: "Home"}, {Slug: "Missing", Title: "Missing"}}, Page: 1, PerPage: 100},
		},
		wikiBySlug: map[string]gitcode.WikiPage{
			"Home": {Slug: "Home", Title: "Home", Body: "home body", Revision: "rev-home", CreatedAt: base, UpdatedAt: base},
		},
		errors: []error{nil, gitcode.ErrRemoteNotFound{Endpoint: "/wiki/Missing", Alias: "wiki:Missing"}},
	}
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "bulk-wiki", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)
	result, err := svc.BulkSyncWiki(ctx, BulkSyncRequest{RepoID: "bulk-wiki", IdempotencyKey: "bulk-wiki"})
	if err == nil {
		t.Fatal("BulkSyncWiki expected partial error, got nil")
	}
	var partial *PartialSyncError
	if !errors.As(err, &partial) {
		t.Fatalf("BulkSyncWiki error = %T %v, want *PartialSyncError", err, err)
	}
	if result.SuccessCount != 1 || result.FailureCount != 1 {
		t.Fatalf("counts = success %d failure %d, want 1/1", result.SuccessCount, result.FailureCount)
	}
	if len(result.Results) != 1 || len(result.Failures) != 1 {
		t.Fatalf("result lengths = %d/%d, want 1/1", len(result.Results), len(result.Failures))
	}
	if !strings.Contains(partial.Error(), "1 succeeded") || !strings.Contains(partial.Error(), "1 failed") {
		t.Fatalf("PartialSyncError.Error() = %q, want summary counts", partial.Error())
	}
	if _, err := store.GetSourceScoped(ctx, "bulk-wiki", "WIKI-HOME"); err != nil {
		t.Fatalf("WIKI-HOME missing: %v", err)
	}
}

func TestBulkSyncAllAggregatesIssuesAndWiki(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 22, 16, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{{Items: []gitcode.IssueSummary{{Number: 42, Title: "Issue"}}, Page: 1, PerPage: 100}},
		issuesByNumber:  map[int]gitcode.Issue{42: {Number: 42, Title: "Issue", Body: "body", State: "open", CreatedAt: base, UpdatedAt: base}},
		listWikiPages:   []gitcode.Page[gitcode.WikiPage]{{Items: []gitcode.WikiPage{{Slug: "Home", Title: "Home"}}, Page: 1, PerPage: 100}},
		wikiBySlug:      map[string]gitcode.WikiPage{"Home": {Slug: "Home", Title: "Home", Body: "body", Revision: "rev", CreatedAt: base, UpdatedAt: base}},
	}
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "bulk-all", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)
	result, err := svc.BulkSyncAll(ctx, BulkSyncRequest{RepoID: "bulk-all", IdempotencyKey: "bulk-all"})
	if err != nil {
		t.Fatalf("BulkSyncAll returned error: %v", err)
	}
	if result.SuccessCount != 2 || result.FailureCount != 0 {
		t.Fatalf("counts = success %d failure %d, want 2/0", result.SuccessCount, result.FailureCount)
	}
	if _, err := store.GetSourceScoped(ctx, "bulk-all", "ISSUE-42"); err != nil {
		t.Fatalf("ISSUE-42 missing: %v", err)
	}
	if _, err := store.GetSourceScoped(ctx, "bulk-all", "WIKI-HOME"); err != nil {
		t.Fatalf("WIKI-HOME missing: %v", err)
	}
}

func TestBulkSyncIssuesListFailureReturnsError(t *testing.T) {
	ctx := context.Background()
	client := &fakeGitCodeClient{listIssuesErrors: []error{gitcode.ErrRateLimited{Endpoint: "/issues", RetryAfter: time.Second, Attempts: 1}}}
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "bulk-list-failure", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)
	result, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{RepoID: "bulk-list-failure"})
	if err == nil {
		t.Fatal("BulkSyncIssues expected error, got nil")
	}
	var partial *PartialSyncError
	if !errors.As(err, &partial) {
		t.Fatalf("BulkSyncIssues error = %T %v, want *PartialSyncError", err, err)
	}
	if result.SuccessCount != 0 || result.FailureCount != 1 {
		t.Fatalf("counts = success %d failure %d, want 0/1", result.SuccessCount, result.FailureCount)
	}
	var failure ErrSyncFailure
	if !errors.As(result.Failures[0].Err, &failure) || failure.Mode != "rate_limited" {
		t.Fatalf("failure error = %T %v, want rate_limited ErrSyncFailure", result.Failures[0].Err, result.Failures[0].Err)
	}
}
