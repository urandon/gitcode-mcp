package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
)

func TestBulkSyncIssuesBoundedCancelMidway_CancelAfterPage3Progress(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	base := time.Date(2026, 6, 22, 14, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{
			{Items: generateIssueSummaries(0, 10), Page: 1, PerPage: 10},
			{Items: generateIssueSummaries(10, 10), Page: 2, PerPage: 10},
			{Items: generateIssueSummaries(20, 10), Page: 3, PerPage: 10},
			{Items: generateIssueSummaries(30, 10), Page: 4, PerPage: 10},
			{Items: generateIssueSummaries(40, 10), Page: 5, PerPage: 10},
		},
		issuesByNumber: buildIssueMap(50, base),
	}
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "bounded-cancel", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)

	progressChan := make(chan ProgressEvent, 20)
	go func() {
		for ev := range progressChan {
			if ev.Page == 3 {
				cancel()
			}
		}
	}()

	result, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{
		RepoID:  "bounded-cancel",
		Page:    1,
		PerPage: 10,
		Bounds:  &SyncBounds{MaxRecords: 50, ProgressChan: progressChan},
	})
	close(progressChan)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var partial *PartialSyncError
	if !errors.As(err, &partial) {
		t.Fatalf("error = %T %v, want *PartialSyncError", err, err)
	}
	if partial.SuccessCount != 30 {
		t.Fatalf("success_count = %d, want 30", partial.SuccessCount)
	}
	if partial.Diagnostic != SyncDiagnosticCancelled {
		t.Fatalf("diagnostic = %q, want %q", partial.Diagnostic, SyncDiagnosticCancelled)
	}
	if partial.TotalRequested != 50 {
		t.Fatalf("total_requested = %d, want 50", partial.TotalRequested)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
}

func TestBulkSyncIssuesBoundedCancelMidway(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	base := time.Date(2026, 6, 22, 14, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{
			{Items: generateIssueSummaries(0, 10), Page: 1, PerPage: 10},
			{Items: generateIssueSummaries(10, 10), Page: 2, PerPage: 10},
			{Items: generateIssueSummaries(20, 10), Page: 3, PerPage: 10},
			{Items: generateIssueSummaries(30, 10), Page: 4, PerPage: 10},
			{Items: generateIssueSummaries(40, 10), Page: 5, PerPage: 10},
		},
		issuesByNumber: buildIssueMap(50, base),
	}
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "bounded-cancel-mid", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)

	progressChan := make(chan ProgressEvent, 20)
	go func() {
		for ev := range progressChan {
			if ev.Page == 3 {
				cancel()
			}
		}
	}()

	_, err = svc.BulkSyncIssues(ctx, BulkSyncRequest{
		RepoID:  "bounded-cancel-mid",
		Page:    1,
		PerPage: 10,
		Bounds:  &SyncBounds{MaxRecords: 50, ProgressChan: progressChan},
	})
	close(progressChan)

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var partial *PartialSyncError
	if !errors.As(err, &partial) {
		t.Fatalf("error = %T %v, want *PartialSyncError", err, err)
	}
	if partial.SuccessCount != 30 {
		t.Fatalf("success_count = %d, want 30", partial.SuccessCount)
	}
	if partial.Diagnostic != SyncDiagnosticCancelled {
		t.Fatalf("diagnostic = %q, want %q", partial.Diagnostic, SyncDiagnosticCancelled)
	}
	if partial.TotalRequested != 50 {
		t.Fatalf("total_requested = %d, want 50", partial.TotalRequested)
	}
}

func TestBulkSyncIssuesBoundedTimeout(t *testing.T) {
	base := time.Date(2026, 6, 22, 14, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{
			{Items: generateIssueSummaries(0, 10), Page: 1, PerPage: 10},
			{Items: generateIssueSummaries(10, 10), Page: 2, PerPage: 10},
		},
		issuesByNumber: buildIssueMap(20, base),
	}
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "bounded-timeout", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(2 * time.Millisecond)

	_, err = svc.BulkSyncIssues(ctx, BulkSyncRequest{
		RepoID:  "bounded-timeout",
		Page:    1,
		PerPage: 10,
		Bounds:  &SyncBounds{MaxRecords: 30},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var partial *PartialSyncError
	if errors.As(err, &partial) {
		if partial.Diagnostic != SyncDiagnosticTimeout {
			t.Fatalf("diagnostic = %q, want %q", partial.Diagnostic, SyncDiagnosticTimeout)
		}
	} else {
		t.Fatalf("error = %T %v, want *PartialSyncError", err, err)
	}
}

func TestBulkSyncIssuesBoundedProgressEvents(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 22, 14, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{
			{Items: generateIssueSummaries(0, 5), Page: 1, PerPage: 5},
			{Items: generateIssueSummaries(5, 5), Page: 2, PerPage: 5},
			{Items: generateIssueSummaries(10, 3), Page: 3, PerPage: 5},
		},
		issuesByNumber: buildIssueMap(13, base),
	}
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "bounded-progress", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)

	progressChan := make(chan ProgressEvent, 10)
	var events []ProgressEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range progressChan {
			events = append(events, ev)
		}
	}()
	result, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{
		RepoID:  "bounded-progress",
		Page:    1,
		PerPage: 5,
		Bounds:  &SyncBounds{ProgressChan: progressChan},
	})
	close(progressChan)
	<-done

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SuccessCount != 13 {
		t.Fatalf("success_count = %d, want 13", result.SuccessCount)
	}
	if len(events) < 3 {
		t.Fatalf("progress events = %d, want at least 3", len(events))
	}
	for i, ev := range events {
		if ev.Collection != "issues" {
			t.Fatalf("event[%d].Collection = %q, want issues", i, ev.Collection)
		}
		if ev.RecordsFetched < 1 {
			t.Fatalf("event[%d].RecordsFetched = %d, want >= 1", i, ev.RecordsFetched)
		}
	}
}

func TestBulkSyncIssuesBoundedMaxPages(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 22, 14, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{
			{Items: generateIssueSummaries(0, 10), Page: 1, PerPage: 10},
			{Items: generateIssueSummaries(10, 10), Page: 2, PerPage: 10},
			{Items: generateIssueSummaries(20, 10), Page: 3, PerPage: 10},
		},
		issuesByNumber: buildIssueMap(30, base),
	}
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "bounded-maxpages", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)

	result, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{
		RepoID:  "bounded-maxpages",
		Page:    1,
		PerPage: 10,
		Bounds:  &SyncBounds{MaxPages: 2},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SuccessCount != 20 {
		t.Fatalf("success_count = %d, want 20", result.SuccessCount)
	}
}

func TestBulkSyncIssuesBoundedMaxRecords(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 22, 14, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{
			{Items: generateIssueSummaries(0, 10), Page: 1, PerPage: 10},
			{Items: generateIssueSummaries(10, 10), Page: 2, PerPage: 10},
			{Items: generateIssueSummaries(20, 10), Page: 3, PerPage: 10},
			{Items: generateIssueSummaries(30, 10), Page: 4, PerPage: 10},
			{Items: generateIssueSummaries(40, 10), Page: 5, PerPage: 10},
		},
		issuesByNumber: buildIssueMap(50, base),
	}
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "bounded-maxrecords", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)

	result, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{
		RepoID:  "bounded-maxrecords",
		Page:    1,
		PerPage: 10,
		Bounds:  &SyncBounds{MaxRecords: 25},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SuccessCount != 25 {
		t.Fatalf("success_count = %d, want 25", result.SuccessCount)
	}
}

func TestBulkSyncPullRequestsBoundedMaxPages(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 22, 17, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listPRPages: []gitcode.Page[gitcode.PullRequest]{
			{Items: generatePullRequests(0, 10, base), Page: 1, PerPage: 10},
			{Items: generatePullRequests(10, 10, base), Page: 2, PerPage: 10},
			{Items: generatePullRequests(20, 10, base), Page: 3, PerPage: 10},
		},
	}
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "pulls-maxpages", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)

	result, err := svc.BulkSyncPullRequests(ctx, BulkSyncRequest{
		RepoID:  "pulls-maxpages",
		Page:    1,
		PerPage: 10,
		Bounds:  &SyncBounds{MaxPages: 2},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SuccessCount != 20 {
		t.Fatalf("success_count = %d, want 20", result.SuccessCount)
	}
	if client.listPRCalls != 2 {
		t.Fatalf("ListPRs calls = %d, want 2", client.listPRCalls)
	}
}

func TestBulkSyncPullRequestsBoundedMaxRecords(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 22, 17, 30, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listPRPages: []gitcode.Page[gitcode.PullRequest]{
			{Items: generatePullRequests(0, 10, base), Page: 1, PerPage: 10},
			{Items: generatePullRequests(10, 10, base), Page: 2, PerPage: 10},
			{Items: generatePullRequests(20, 10, base), Page: 3, PerPage: 10},
		},
	}
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "pulls-maxrecords", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)

	result, err := svc.BulkSyncPullRequests(ctx, BulkSyncRequest{
		RepoID:  "pulls-maxrecords",
		Page:    1,
		PerPage: 10,
		Bounds:  &SyncBounds{MaxRecords: 15},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SuccessCount != 15 {
		t.Fatalf("success_count = %d, want 15", result.SuccessCount)
	}
	if client.listPRCalls != 2 {
		t.Fatalf("ListPRs calls = %d, want 2", client.listPRCalls)
	}
}

func TestBulkSyncPRCommentsBoundedMaxRecords(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 22, 18, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listPRPages: []gitcode.Page[gitcode.PullRequest]{
			{Items: generatePullRequests(0, 2, base), Page: 1, PerPage: 10},
		},
		prCommentsByPR: map[int][]gitcode.PRComment{
			1: generatePRComments(1, 3, base),
			2: generatePRComments(2, 3, base),
		},
	}
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "pr-comments-maxrecords", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)
	if _, err := svc.BulkSyncPullRequests(ctx, BulkSyncRequest{RepoID: "pr-comments-maxrecords", PerPage: 10}); err != nil {
		t.Fatalf("seed pull requests: %v", err)
	}

	result, err := svc.BulkSyncPRComments(ctx, BulkSyncRequest{
		RepoID: "pr-comments-maxrecords",
		Bounds: &SyncBounds{MaxRecords: 4},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SuccessCount != 4 {
		t.Fatalf("success_count = %d, want 4", result.SuccessCount)
	}
	if client.prCommentCalls != 2 {
		t.Fatalf("ListPRComments calls = %d, want 2", client.prCommentCalls)
	}
}

func TestBulkSyncWikiBoundedPreCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	base := time.Date(2026, 6, 22, 15, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listWikiPages: []gitcode.Page[gitcode.WikiPage]{
			{Items: generateWikiPages(1, 10), Page: 1, PerPage: 10},
		},
		wikiBySlug: buildWikiMap(10, base),
	}
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "wiki-precancel", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)

	_, err = svc.BulkSyncWiki(ctx, BulkSyncRequest{
		RepoID:  "wiki-precancel",
		Page:    1,
		PerPage: 10,
		Bounds:  &SyncBounds{MaxRecords: 20},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var partial *PartialSyncError
	if !errors.As(err, &partial) {
		t.Fatalf("error = %T %v, want *PartialSyncError", err, err)
	}
	if partial.Diagnostic != SyncDiagnosticCancelled {
		t.Fatalf("diagnostic = %q, want %q", partial.Diagnostic, SyncDiagnosticCancelled)
	}
	if partial.SuccessCount != 0 {
		t.Fatalf("success_count = %d, want 0", partial.SuccessCount)
	}
}

func TestBulkSyncWikiBoundedMaxRecords(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 22, 15, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listWikiPages: []gitcode.Page[gitcode.WikiPage]{
			{Items: generateWikiPages(1, 20), Page: 1, PerPage: 100},
		},
		wikiBySlug: buildWikiMap(20, base),
	}
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "wiki-maxrecords", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)

	result, err := svc.BulkSyncWiki(ctx, BulkSyncRequest{
		RepoID:  "wiki-maxrecords",
		Page:    1,
		PerPage: 100,
		Bounds:  &SyncBounds{MaxRecords: 5},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SuccessCount != 5 {
		t.Fatalf("success_count = %d, want 5", result.SuccessCount)
	}
}

func TestBulkSyncWikiBoundedMaxPages(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 22, 15, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listWikiPages: []gitcode.Page[gitcode.WikiPage]{
			{Items: generateWikiPages(1, 20), Page: 1, PerPage: 100},
		},
		wikiBySlug: buildWikiMap(20, base),
	}
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "wiki-maxpages", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)

	result, err := svc.BulkSyncWiki(ctx, BulkSyncRequest{
		RepoID:  "wiki-maxpages",
		Page:    1,
		PerPage: 3,
		Bounds:  &SyncBounds{MaxPages: 2},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SuccessCount != 6 {
		t.Fatalf("success_count = %d, want 6", result.SuccessCount)
	}
}

func TestBulkSyncIssuesUnboundedBackwardCompat(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 22, 14, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{
			{Items: []gitcode.IssueSummary{{ID: "10", Number: 10, Title: "Issue", State: "open"}}, Page: 1, PerPage: 100},
		},
		issuesByNumber: map[int]gitcode.Issue{10: {ID: "10", Number: 10, Title: "Issue", Body: "body", State: "open", CreatedAt: base, UpdatedAt: base}},
	}
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "unbounded-compat", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)

	result, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{RepoID: "unbounded-compat"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SuccessCount != 1 {
		t.Fatalf("success_count = %d, want 1", result.SuccessCount)
	}
}

func TestBulkSyncWikiUnboundedBackwardCompat(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 22, 15, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listWikiPages: []gitcode.Page[gitcode.WikiPage]{
			{Items: []gitcode.WikiPage{{Slug: "Home", Title: "Home"}}, Page: 1, PerPage: 100},
		},
		wikiBySlug: map[string]gitcode.WikiPage{"Home": {Slug: "Home", Title: "Home", Body: "body", Revision: "rev", CreatedAt: base, UpdatedAt: base}},
	}
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "wiki-unbounded-compat", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)

	result, err := svc.BulkSyncWiki(ctx, BulkSyncRequest{RepoID: "wiki-unbounded-compat"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SuccessCount != 1 {
		t.Fatalf("success_count = %d, want 1", result.SuccessCount)
	}
}

func TestBulkSyncAllBoundedAggregatesProgress(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 22, 16, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{
			{Items: generateIssueSummaries(0, 2), Page: 1, PerPage: 10},
		},
		listWikiPages: []gitcode.Page[gitcode.WikiPage]{
			{Items: generateWikiPages(1, 2), Page: 1, PerPage: 10},
		},
		issuesByNumber: buildIssueMap(2, base),
		wikiBySlug:     buildWikiMap(2, base),
	}
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "bounded-all", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)

	result, err := svc.BulkSyncAll(ctx, BulkSyncRequest{
		RepoID: "bounded-all",
		Bounds: &SyncBounds{MaxRecords: 4},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SuccessCount != 4 {
		t.Fatalf("success_count = %d, want 4", result.SuccessCount)
	}
}

func TestProgressEventNonBlockingSend(t *testing.T) {
	ch := make(chan ProgressEvent)
	emitProgress(ch, ProgressEvent{Collection: "test", Page: 1, RecordsFetched: 5})
	emitProgress(ch, ProgressEvent{Collection: "test", Page: 2, RecordsFetched: 10})
}

func TestBulkSyncWikiEmptyWikiDiagnosticUnbounded(t *testing.T) {
	ctx := context.Background()
	client := &fakeGitCodeClient{
		listWikiErrors: []error{&emptyWikiTestError{}},
	}
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "wiki-empty-unbounded", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)

	_, err = svc.BulkSyncWiki(ctx, BulkSyncRequest{RepoID: "wiki-empty-unbounded"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var partial *PartialSyncError
	if !errors.As(err, &partial) {
		t.Fatalf("error = %T %v, want *PartialSyncError", err, err)
	}
	if partial.Diagnostic != SyncDiagnosticEmptyWiki {
		t.Fatalf("partial.Diagnostic = %q, want %q", partial.Diagnostic, SyncDiagnosticEmptyWiki)
	}
	var chainHasEmptyWiki bool
	for _, re := range partial.Errors {
		if re.Err != nil && strings.Contains(re.Err.Error(), "empty") {
			chainHasEmptyWiki = true
			break
		}
	}
	if !chainHasEmptyWiki {
		t.Fatalf("error chain should mention empty wiki, got: %v", err)
	}
	if errorHasDiagnosticCode(err, "api_validation") {
		t.Fatal("should not be classified as api_validation")
	}
	if errorHasDiagnosticCode(err, "provider_failure") {
		t.Fatal("should not be classified as provider_failure")
	}
}

func TestBulkSyncWikiEmptyWikiDiagnosticBounded(t *testing.T) {
	ctx := context.Background()
	client := &fakeGitCodeClient{
		listWikiErrors: []error{&emptyWikiTestError{}},
	}
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "wiki-empty-bounded", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)

	result, err := svc.BulkSyncWiki(ctx, BulkSyncRequest{
		RepoID:  "wiki-empty-bounded",
		Page:    1,
		PerPage: 10,
		Bounds:  &SyncBounds{MaxPages: 5},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var partial *PartialSyncError
	if !errors.As(err, &partial) {
		t.Fatalf("error = %T %v, want *PartialSyncError", err, err)
	}
	if partial.Diagnostic != SyncDiagnosticEmptyWiki {
		t.Fatalf("partial.Diagnostic = %q, want %q", partial.Diagnostic, SyncDiagnosticEmptyWiki)
	}
	if result.SuccessCount != 0 {
		t.Fatalf("success_count = %d, want 0", result.SuccessCount)
	}
	if errorHasDiagnosticCode(err, "api_validation") {
		t.Fatal("should not be classified as api_validation")
	}
	if errorHasDiagnosticCode(err, "provider_failure") {
		t.Fatal("should not be classified as provider_failure")
	}
}

func TestNormalizeSyncFailureMapsEmptyWiki(t *testing.T) {
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := NewWithClient(store, &fakeGitCodeClient{})

	err = svc.normalizeSyncFailure(&emptyWikiTestError{}, SyncRequest{RepoID: "test"}, "wiki", "*")
	var sfErr ErrSyncFailure
	if !errors.As(err, &sfErr) {
		t.Fatalf("normalizeSyncFailure returned %T %v, want ErrSyncFailure", err, err)
	}
	if sfErr.Mode != "empty_wiki" {
		t.Fatalf("sfErr.Mode = %q, want %q", sfErr.Mode, "empty_wiki")
	}
	if !strings.Contains(sfErr.Error(), "gitcode-mcp wiki init") {
		t.Fatalf("error message missing remediation text, got: %v", sfErr.Error())
	}
}

func TestBulkSyncWikiBoundedCancelMidSync(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	base := time.Date(2026, 6, 22, 15, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listWikiPages: []gitcode.Page[gitcode.WikiPage]{
			{Items: generateWikiPages(1, 20), Page: 1, PerPage: 100},
		},
		wikiBySlug: buildWikiMap(20, base),
	}
	client.onWikiCall = func(call int) {
		if call == 2 {
			cancel()
		}
	}
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "wiki-cancel-mid", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)

	result, err := svc.BulkSyncWiki(ctx, BulkSyncRequest{
		RepoID:  "wiki-cancel-mid",
		Page:    1,
		PerPage: 100,
		Bounds:  &SyncBounds{MaxRecords: 20},
	})

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var partial *PartialSyncError
	if !errors.As(err, &partial) {
		t.Fatalf("error = %T %v, want *PartialSyncError", err, err)
	}
	if partial.Diagnostic != SyncDiagnosticCancelled {
		t.Fatalf("diagnostic = %q, want %q", partial.Diagnostic, SyncDiagnosticCancelled)
	}
	if partial.SuccessCount < 1 {
		t.Fatalf("success_count = %d, want >= 1", partial.SuccessCount)
	}
	if result == nil {
		t.Fatal("result is nil")
	}
	if result.SuccessCount < 1 {
		t.Fatalf("result.success_count = %d, want >= 1", result.SuccessCount)
	}
	totalItems := result.SuccessCount + result.FailureCount
	if totalItems != 20 {
		t.Fatalf("total items processed = %d, want 20", totalItems)
	}
}

func TestBulkSyncWikiBoundedSingleListWikiPagesCall(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 22, 15, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listWikiPages: []gitcode.Page[gitcode.WikiPage]{
			{Items: generateWikiPages(1, 20), Page: 1, PerPage: 100},
		},
		wikiBySlug: buildWikiMap(20, base),
	}
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "wiki-single-list", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)

	result, err := svc.BulkSyncWiki(ctx, BulkSyncRequest{
		RepoID:  "wiki-single-list",
		Page:    1,
		PerPage: 10,
		Bounds:  &SyncBounds{MaxRecords: 10},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SuccessCount != 10 {
		t.Fatalf("success_count = %d, want 10", result.SuccessCount)
	}
	if client.listWikiPagesCallCount != 1 {
		t.Fatalf("ListWikiPages call count = %d, want 1 (bounded wiki sync uses single call, no outer loop)", client.listWikiPagesCallCount)
	}
}

func TestErrorHasDiagnosticCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		code string
		want bool
	}{
		{"direct match", &emptyWikiTestError{}, "empty_wiki", true},
		{"direct mismatch", &emptyWikiTestError{}, "api_validation", false},
		{"wrapped match", fmt.Errorf("wrapped: %w", &emptyWikiTestError{}), "empty_wiki", true},
		{"wrapped mismatch", fmt.Errorf("wrapped: %w", &emptyWikiTestError{}), "other", false},
		{"nil error", nil, "empty_wiki", false},
		{"no DiagnosticCode", errors.New("plain error"), "empty_wiki", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errorHasDiagnosticCode(tt.err, tt.code); got != tt.want {
				t.Fatalf("errorHasDiagnosticCode(...) = %v, want %v", got, tt.want)
			}
		})
	}
}

type emptyWikiTestError struct{}

func (e *emptyWikiTestError) Error() string          { return "wiki is empty/uninitialized" }
func (e *emptyWikiTestError) DiagnosticCode() string { return "empty_wiki" }

func buildIssueMap(count int, base time.Time) map[int]gitcode.Issue {
	m := make(map[int]gitcode.Issue, count)
	for i := 1; i <= count; i++ {
		m[i] = gitcode.Issue{ID: fmt.Sprintf("%d", i), Number: i, Title: fmt.Sprintf("Issue %d", i), Body: "body", State: "open", CreatedAt: base, UpdatedAt: base}
	}
	return m
}

func buildWikiMap(count int, base time.Time) map[string]gitcode.WikiPage {
	m := make(map[string]gitcode.WikiPage, count)
	for i := 1; i <= count; i++ {
		slug := fmt.Sprintf("Page%d", i)
		m[slug] = gitcode.WikiPage{Slug: slug, Title: slug, Body: "body", Revision: "rev", CreatedAt: base, UpdatedAt: base}
	}
	return m
}

func generateIssueSummaries(offset, count int) []gitcode.IssueSummary {
	out := make([]gitcode.IssueSummary, count)
	for i := 0; i < count; i++ {
		num := offset + i + 1
		out[i] = gitcode.IssueSummary{ID: fmt.Sprintf("%d", num), Number: num, Title: fmt.Sprintf("Issue %d", num), State: "open"}
	}
	return out
}

func generateWikiPages(offset, count int) []gitcode.WikiPage {
	out := make([]gitcode.WikiPage, count)
	for i := 0; i < count; i++ {
		slug := fmt.Sprintf("Page%d", offset+i+1)
		out[i] = gitcode.WikiPage{Slug: slug, Title: slug}
	}
	return out
}

func generatePullRequests(offset, count int, base time.Time) []gitcode.PullRequest {
	out := make([]gitcode.PullRequest, count)
	for i := 0; i < count; i++ {
		num := offset + i + 1
		out[i] = gitcode.PullRequest{
			ID:        fmt.Sprintf("%d", num),
			Number:    num,
			Title:     fmt.Sprintf("PR %d", num),
			Body:      "body",
			State:     "open",
			Base:      "main",
			Head:      fmt.Sprintf("topic-%d", num),
			CreatedAt: base,
			UpdatedAt: base,
		}
	}
	return out
}

func generatePRComments(prNumber, count int, base time.Time) []gitcode.PRComment {
	out := make([]gitcode.PRComment, count)
	for i := 0; i < count; i++ {
		num := i + 1
		out[i] = gitcode.PRComment{
			ID:        fmt.Sprintf("%d-%d", prNumber, num),
			Author:    "fixture-user",
			Body:      fmt.Sprintf("comment %d on pr %d", num, prNumber),
			CreatedAt: base,
			UpdatedAt: base,
		}
	}
	return out
}
