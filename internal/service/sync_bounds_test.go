package service

import (
	"context"
	"errors"
	"fmt"
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

func TestBulkSyncWikiBoundedCancelMidPage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	base := time.Date(2026, 6, 22, 15, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listWikiPages: []gitcode.Page[gitcode.WikiPage]{
			{Items: generateWikiPages(1, 10), Page: 1, PerPage: 10},
			{Items: generateWikiPages(11, 10), Page: 2, PerPage: 10},
		},
		wikiBySlug: buildWikiMap(20, base),
	}
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "wiki-cancel", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)

	progressChan := make(chan ProgressEvent, 10)
	go func() {
		for ev := range progressChan {
			if ev.Page == 1 {
				cancel()
				return
			}
		}
	}()

	result, err := svc.BulkSyncWiki(ctx, BulkSyncRequest{
		RepoID:  "wiki-cancel",
		Page:    1,
		PerPage: 10,
		Bounds:  &SyncBounds{MaxRecords: 20, ProgressChan: progressChan},
	})
	close(progressChan)

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
	if result.SuccessCount != 10 {
		t.Fatalf("success_count = %d, want 10", result.SuccessCount)
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
