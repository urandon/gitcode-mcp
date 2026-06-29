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
			{Items: []gitcode.IssueSummary{{ID: "1", Number: 1, Title: "First", State: "open", Comments: 1}, {ID: "2", Number: 2, Title: "Second", State: "open"}}, Page: 1, PerPage: 100},
			{Items: []gitcode.IssueSummary{{ID: "1", Number: 1, Title: "First", State: "open", Comments: 1}, {ID: "2", Number: 2, Title: "Second", State: "open"}}, Page: 1, PerPage: 100},
		},
		issuesByNumber: map[int]gitcode.Issue{
			1: {ID: "1", Number: 1, Title: "First", Body: "first body", State: "open", CreatedAt: base, UpdatedAt: base},
			2: {ID: "2", Number: 2, Title: "Second", Body: "second body", State: "open", CreatedAt: base, UpdatedAt: base},
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
	first, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{RepoID: "bulk-issues", PerPage: 100})
	if err != nil {
		t.Fatalf("BulkSyncIssues first returned error: %v", err)
	}
	if first.SuccessCount != 2 || first.FailureCount != 0 {
		t.Fatalf("first counts = success %d failure %d, want 2/0", first.SuccessCount, first.FailureCount)
	}
	if client.issueCalls != 0 {
		t.Fatalf("bulk issue sync GetIssue calls = %d, want 0 because list payload is the current sync source", client.issueCalls)
	}
	if client.commentCalls != 2 {
		t.Fatalf("first bulk issue sync ListIssueComments calls = %d, want 2 before revision cache is established", client.commentCalls)
	}
	seenKeys := map[string]bool{}
	seenEvents := map[string]bool{}
	for i, result := range first.Results {
		if result.IdempotencyKey == "" || result.SyncEventID == "" {
			t.Fatalf("first result %d missing idempotency/event: %+v", i, result)
		}
		if seenKeys[result.IdempotencyKey] {
			t.Fatalf("first result %d duplicate idempotency key %q", i, result.IdempotencyKey)
		}
		if seenEvents[result.SyncEventID] {
			t.Fatalf("first result %d duplicate sync event id %q", i, result.SyncEventID)
		}
		if result.Counts.Listed != 1 || result.Counts.FetchedDetail != 1 {
			t.Fatalf("first result %d counts = %#v, want listed=1 fetched_detail=1", i, result.Counts)
		}
		seenKeys[result.IdempotencyKey] = true
		seenEvents[result.SyncEventID] = true
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
	if client.commentCalls != 2 {
		t.Fatalf("ListIssueComments calls after unchanged second sync = %d, want still 2", client.commentCalls)
	}
	for i, result := range second.Results {
		if !result.ZeroDelta {
			t.Fatalf("second result %d ZeroDelta = false, want true", i)
		}
		if result.Counts.Fetched != 1 || result.Counts.Skipped != 1 || result.Counts.Inserted != 0 || result.Counts.Updated != 0 {
			t.Fatalf("second result %d counts = %#v, want fetched/skipped only", i, result.Counts)
		}
		if result.Counts.Listed != 1 || result.Counts.FetchedDetail != 0 || result.Counts.SkippedByRevision != 1 {
			t.Fatalf("second result %d counts = %#v, want listed=1 fetched_detail=0 skipped_by_revision=1", i, result.Counts)
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

func TestBulkSyncIssuesIgnoresDeferredIssueCommentsRead(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 26, 16, 30, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{
			{Items: []gitcode.IssueSummary{{ID: "4119847", Number: 16, Title: "Live issue", Body: "live body", State: "open", CreatedAt: base, UpdatedAt: base}}, Page: 1, PerPage: 1},
		},
		listIssueCommentsErr: gitcode.ErrUnsupportedCapability{
			CapabilityKey: "comments_read",
			Message:       "comments are deferred",
		},
	}
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "bulk-issues-comments-deferred", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)
	result, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{RepoID: "bulk-issues-comments-deferred", PerPage: 1})
	if err != nil {
		t.Fatalf("BulkSyncIssues returned error: %v", err)
	}
	if result.SuccessCount != 1 || result.FailureCount != 0 {
		t.Fatalf("counts = success %d failure %d, want 1/0", result.SuccessCount, result.FailureCount)
	}
	source, err := store.GetSourceScoped(ctx, "bulk-issues-comments-deferred", "ISSUE-16")
	if err != nil {
		t.Fatalf("ISSUE-16 missing: %v", err)
	}
	if source.Title != "Live issue" || source.Body != "live body" {
		t.Fatalf("source=%+v", source)
	}
}

func TestBulkSyncIssuesFetchesCommentsWhenListRevisionChanges(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 27, 11, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{
			{Items: []gitcode.IssueSummary{{ID: "11", Number: 11, Title: "Issue", Body: "body", State: "open", Comments: 0, CreatedAt: base, UpdatedAt: base}}, Page: 1, PerPage: 100},
			{Items: []gitcode.IssueSummary{{ID: "11", Number: 11, Title: "Issue", Body: "body", State: "open", Comments: 1, CreatedAt: base, UpdatedAt: base.Add(time.Minute)}}, Page: 1, PerPage: 100},
		},
		commentsByIssue: map[int][]gitcode.Comment{
			11: {{ID: "c11", Author: "author", Body: "new comment", CreatedAt: base.Add(time.Minute), UpdatedAt: base.Add(time.Minute)}},
		},
	}
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "bulk-issues-comment-revision", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)

	if _, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{RepoID: "bulk-issues-comment-revision", IdempotencyKey: "issues-first", PerPage: 100}); err != nil {
		t.Fatalf("first BulkSyncIssues returned error: %v", err)
	}
	second, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{RepoID: "bulk-issues-comment-revision", IdempotencyKey: "issues-second", PerPage: 100})
	if err != nil {
		t.Fatalf("second BulkSyncIssues returned error: %v", err)
	}
	if client.commentCalls != 2 {
		t.Fatalf("ListIssueComments calls = %d, want 2 after changed list revision", client.commentCalls)
	}
	if second.SuccessCount != 1 || len(second.Results) != 1 {
		t.Fatalf("second result count = %d/%d, want 1/1", second.SuccessCount, len(second.Results))
	}
	if second.Results[0].Counts.FetchedDetail != 1 || second.Results[0].Counts.SkippedByRevision != 0 {
		t.Fatalf("second counts = %#v, want fetched_detail=1 skipped_by_revision=0", second.Results[0].Counts)
	}
	record, err := store.GetRecord(ctx, "bulk-issues-comment-revision", "ISSUE-11")
	if err != nil {
		t.Fatalf("ISSUE-11 record missing: %v", err)
	}
	if len(record.Comments) != 1 || record.Comments[0].CommentID != "c11" {
		t.Fatalf("comments=%+v, want c11", record.Comments)
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

func TestBulkSyncWikiSkipsUnchangedPageByListRevision(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 27, 9, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listWikiPages: []gitcode.Page[gitcode.WikiPage]{
			{Items: []gitcode.WikiPage{{Slug: "Home", Title: "Home", Revision: "rev-home"}}, Page: 1, PerPage: 100},
			{Items: []gitcode.WikiPage{{Slug: "Home", Title: "Home", Revision: "rev-home"}}, Page: 1, PerPage: 100},
		},
		wikiBySlug: map[string]gitcode.WikiPage{
			"Home": {ID: "wiki-home", Slug: "Home", Title: "Home", Body: "home body", Revision: "rev-home", CreatedAt: base, UpdatedAt: base},
		},
	}
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "wiki-revision-skip", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)

	first, err := svc.BulkSyncWiki(ctx, BulkSyncRequest{RepoID: "wiki-revision-skip", IdempotencyKey: "wiki-first", PerPage: 100})
	if err != nil {
		t.Fatalf("first BulkSyncWiki returned error: %v", err)
	}
	if first.SuccessCount != 1 || client.wikiCalls != 1 {
		t.Fatalf("first sync success/wikiCalls = %d/%d, want 1/1", first.SuccessCount, client.wikiCalls)
	}

	second, err := svc.BulkSyncWiki(ctx, BulkSyncRequest{RepoID: "wiki-revision-skip", IdempotencyKey: "wiki-second", PerPage: 100})
	if err != nil {
		t.Fatalf("second BulkSyncWiki returned error: %v", err)
	}
	if client.wikiCalls != 1 {
		t.Fatalf("wiki body fetches = %d, want 1 after unchanged second sync", client.wikiCalls)
	}
	if second.SuccessCount != 1 || len(second.Results) != 1 {
		t.Fatalf("second result count = %d/%d, want 1/1", second.SuccessCount, len(second.Results))
	}
	result := second.Results[0]
	if !result.ZeroDelta {
		t.Fatalf("second result ZeroDelta = false, want true")
	}
	if result.Counts.Listed != 1 || result.Counts.SkippedByRevision != 1 || result.Counts.FetchedDetail != 0 {
		t.Fatalf("second counts = %#v, want listed=1 skipped_by_revision=1 fetched_detail=0", result.Counts)
	}
	if result.Counts.Fetched != 1 || result.Counts.Skipped != 1 {
		t.Fatalf("compat counts = %#v, want fetched=1 skipped=1", result.Counts)
	}
}

func TestBulkSyncWikiFetchesChangedListRevision(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listWikiPages: []gitcode.Page[gitcode.WikiPage]{
			{Items: []gitcode.WikiPage{{Slug: "Home", Title: "Home", Revision: "rev-1"}}, Page: 1, PerPage: 100},
			{Items: []gitcode.WikiPage{{Slug: "Home", Title: "Home", Revision: "rev-2"}}, Page: 1, PerPage: 100},
		},
		wikiBySlug: map[string]gitcode.WikiPage{
			"Home": {ID: "wiki-home", Slug: "Home", Title: "Home", Body: "first body", Revision: "rev-1", CreatedAt: base, UpdatedAt: base},
		},
	}
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "wiki-revision-change", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)

	if _, err := svc.BulkSyncWiki(ctx, BulkSyncRequest{RepoID: "wiki-revision-change", IdempotencyKey: "wiki-first", PerPage: 100}); err != nil {
		t.Fatalf("first BulkSyncWiki returned error: %v", err)
	}
	client.wikiBySlug["Home"] = gitcode.WikiPage{ID: "wiki-home", Slug: "Home", Title: "Home", Body: "second body", Revision: "rev-2", CreatedAt: base, UpdatedAt: base.Add(time.Hour)}

	second, err := svc.BulkSyncWiki(ctx, BulkSyncRequest{RepoID: "wiki-revision-change", IdempotencyKey: "wiki-second", PerPage: 100})
	if err != nil {
		t.Fatalf("second BulkSyncWiki returned error: %v", err)
	}
	if client.wikiCalls != 2 {
		t.Fatalf("wiki body fetches = %d, want 2 after changed revision", client.wikiCalls)
	}
	if second.SuccessCount != 1 || len(second.Results) != 1 {
		t.Fatalf("second result count = %d/%d, want 1/1", second.SuccessCount, len(second.Results))
	}
	if second.Results[0].Counts.Listed != 1 || second.Results[0].Counts.FetchedDetail != 1 || second.Results[0].Counts.Updated != 1 {
		t.Fatalf("second counts = %#v, want listed=1 fetched_detail=1 updated=1", second.Results[0].Counts)
	}
	source, err := store.GetSourceScoped(ctx, "wiki-revision-change", "WIKI-HOME")
	if err != nil {
		t.Fatalf("WIKI-HOME missing: %v", err)
	}
	status, err := store.GetSyncStatusScoped(ctx, "wiki-revision-change", "WIKI-HOME")
	if err != nil {
		t.Fatalf("WIKI-HOME sync status missing: %v", err)
	}
	if source.Body != "second body" || status.RemoteRevision != "rev-2" {
		t.Fatalf("source body/revision = %q/%q, want second body/rev-2", source.Body, status.RemoteRevision)
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

func TestBulkSyncPullRequestsAndCommentsCreatesSearchableSources(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listPRPages: []gitcode.Page[gitcode.PullRequest]{
			{Items: []gitcode.PullRequest{{ID: "9001", Number: 7, Title: "Add live PR sync", Body: "PR body with search needle", State: "open", Labels: []string{"enhancement"}, Base: "main", Head: "topic", CreatedAt: base, UpdatedAt: base}}, Page: 1, PerPage: 100},
		},
		prCommentsByPR: map[int][]gitcode.PRComment{
			7: {{ID: "301", Body: "review comment needle", Author: "alice", DiscussionID: "D7", PRNumber: 7, CreatedAt: base, UpdatedAt: base}},
		},
	}
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "bulk-pr", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)
	prResult, err := svc.BulkSyncPullRequests(ctx, BulkSyncRequest{RepoID: "bulk-pr", IdempotencyKey: "bulk-pr", PerPage: 100})
	if err != nil {
		t.Fatalf("BulkSyncPullRequests returned error: %v", err)
	}
	if prResult.SuccessCount != 1 || prResult.Results[0].Record.ID != "PR-7" || prResult.Results[0].Record.Kind != "pull_request" {
		t.Fatalf("PR result=%+v", prResult)
	}
	if client.prCalls != 0 {
		t.Fatalf("bulk pull request sync GetPR calls = %d, want 0 because list payload is the current sync source", client.prCalls)
	}
	if prResult.Results[0].Counts.Listed != 1 || prResult.Results[0].Counts.FetchedDetail != 0 {
		t.Fatalf("PR counts=%#v, want listed=1 fetched_detail=0", prResult.Results[0].Counts)
	}
	commentResult, err := svc.BulkSyncPRComments(ctx, BulkSyncRequest{RepoID: "bulk-pr", IdempotencyKey: "bulk-pr-comments"})
	if err != nil {
		t.Fatalf("BulkSyncPRComments returned error: %v", err)
	}
	if commentResult.SuccessCount != 1 || commentResult.Results[0].Record.ID != "PRCOMMENT-7-301" || commentResult.Results[0].Record.Kind != "pr_comment" {
		t.Fatalf("comment result=%+v", commentResult)
	}
	if commentResult.Results[0].Counts.Listed != 1 || commentResult.Results[0].Counts.FetchedDetail != 0 {
		t.Fatalf("PR comment counts=%#v, want listed=1 fetched_detail=0", commentResult.Results[0].Counts)
	}
	pr, err := store.GetSourceScoped(ctx, "bulk-pr", "PR-7")
	if err != nil || pr.Kind != "pull_request" {
		t.Fatalf("PR source=%+v err=%v", pr, err)
	}
	comment, err := store.GetSourceScoped(ctx, "bulk-pr", "PRCOMMENT-7-301")
	if err != nil || comment.Kind != "pr_comment" {
		t.Fatalf("PR comment source=%+v err=%v", comment, err)
	}
	search, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "bulk-pr", Query: "needle", Kind: "pr_comment"})
	if err != nil {
		t.Fatalf("SearchSources returned error: %v", err)
	}
	if len(search.Results) != 1 || search.Results[0].ID != "PRCOMMENT-7-301" {
		t.Fatalf("search results=%+v", search.Results)
	}
}

func TestListPRDiscussionsGroupsRepliesAndFiltersUnresolved(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 24, 9, 0, 0, 0, time.UTC)
	resolvedFalse := false
	resolvedTrue := true
	client := &fakeGitCodeClient{
		listPRPages: []gitcode.Page[gitcode.PullRequest]{
			{Items: []gitcode.PullRequest{{ID: "9001", Number: 7, Title: "Review target", Body: "body", State: "open", CreatedAt: base, UpdatedAt: base}}},
		},
		prCommentsByPR: map[int][]gitcode.PRComment{
			7: {
				{ID: "301", Body: "inline root", Author: "alice", DiscussionID: "D7", ReviewKind: "inline", Path: "internal/service/service.go", Line: 42, Resolved: &resolvedFalse, ParentID: "", Positions: []gitcode.PRCommentPosition{{PositionKind: "current", PositionType: "text", BaseSHA: "base-sha", StartSHA: "base-sha", HeadSHA: "head-sha", OldPath: "internal/service/service.go", NewPath: "internal/service/service.go", NewLine: 42, LineCode: "line-code", PatchsetIID: 1, DiffID: 99, VersionSHA: "head-sha", Side: "new", IsOutdated: &resolvedFalse}}, PRNumber: 7, CreatedAt: base, UpdatedAt: base},
				{ID: "302", Body: "reply", Author: "bob", DiscussionID: "D7", ReviewKind: "inline", Path: "internal/service/service.go", Line: 42, Resolved: &resolvedFalse, ParentID: "301", PRNumber: 7, CreatedAt: base.Add(time.Minute), UpdatedAt: base.Add(time.Minute)},
				{ID: "303", Body: "general note", Author: "carol", ReviewKind: "general", Resolved: &resolvedTrue, PRNumber: 7, CreatedAt: base.Add(2 * time.Minute), UpdatedAt: base.Add(2 * time.Minute)},
			},
		},
	}
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "review-pr", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)
	if _, err := svc.BulkSyncPullRequests(ctx, BulkSyncRequest{RepoID: "review-pr"}); err != nil {
		t.Fatalf("BulkSyncPullRequests returned error: %v", err)
	}
	if _, err := svc.BulkSyncPRComments(ctx, BulkSyncRequest{RepoID: "review-pr"}); err != nil {
		t.Fatalf("BulkSyncPRComments returned error: %v", err)
	}
	all, err := svc.ListPRDiscussions(ctx, PRDiscussionRequest{RepoID: "review-pr", Number: 7})
	if err != nil {
		t.Fatalf("ListPRDiscussions returned error: %v", err)
	}
	if len(all.Discussions) != 2 {
		t.Fatalf("discussions=%+v, want 2 groups", all.Discussions)
	}
	inline := all.Discussions[0]
	if inline.ID != "D7" || inline.Kind != "inline" || inline.Path != "internal/service/service.go" || inline.Line != 42 || len(inline.Comments) != 2 {
		t.Fatalf("inline discussion=%+v", inline)
	}
	if inline.Comments[0].Author != "alice" || inline.Comments[1].ParentID != "301" || inline.Comments[1].Body != "reply" {
		t.Fatalf("inline comments=%+v", inline.Comments)
	}
	if inline.Position == nil || inline.Position.Kind != "current" || inline.Position.NewPath != "internal/service/service.go" || inline.Position.NewLine != 42 || inline.Position.LineCode != "line-code" || inline.Position.DiffID != 99 {
		t.Fatalf("inline position=%+v", inline.Position)
	}
	if len(inline.Comments[0].Positions) != 1 || inline.Comments[0].Positions[0].BaseSHA != "base-sha" {
		t.Fatalf("inline comment positions=%+v", inline.Comments[0].Positions)
	}
	general := all.Discussions[1]
	if general.Kind != "general" || len(general.Comments) != 1 || general.Comments[0].Author != "carol" {
		t.Fatalf("general discussion=%+v", general)
	}
	unresolved, err := svc.ListPRDiscussions(ctx, PRDiscussionRequest{RepoID: "review-pr", Number: 7, UnresolvedOnly: true})
	if err != nil {
		t.Fatalf("ListPRDiscussions unresolved returned error: %v", err)
	}
	if len(unresolved.Discussions) != 1 || unresolved.Discussions[0].ID != "D7" {
		t.Fatalf("unresolved discussions=%+v, want D7 only", unresolved.Discussions)
	}
	empty, err := svc.ListPRDiscussions(ctx, PRDiscussionRequest{RepoID: "review-pr", Number: 99})
	if err != nil {
		t.Fatalf("empty ListPRDiscussions returned error: %v", err)
	}
	if len(empty.Discussions) != 0 {
		t.Fatalf("empty discussions=%+v, want empty", empty.Discussions)
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
