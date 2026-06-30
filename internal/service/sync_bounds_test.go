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
	frontier, ok, err := store.GetSyncFrontier(context.Background(), "bounded-cancel", "issue", syncOrderingUpdatedAtDesc, syncFilterStateAll)
	if err != nil || !ok {
		t.Fatalf("GetSyncFrontier ok=%v err=%v", ok, err)
	}
	if frontier.Status != "cancelled" || frontier.PagesListed == 0 || frontier.RecordsListed == 0 {
		t.Fatalf("frontier = %#v, want persisted non-empty cancelled frontier", frontier)
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

func TestBulkSyncIssuesResumesTailAfterBoundedRun(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 30, 10, 0, 0, 0, time.UTC)
	page1 := issueSummariesWithUpdatedAt(0, 2, base)
	page2 := issueSummariesWithUpdatedAt(2, 2, base)
	page3 := issueSummariesWithUpdatedAt(4, 1, base)
	client := &fakeGitCodeClient{
		listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{
			{Items: page1, Page: 1, PerPage: 2},
			{Items: page1, Page: 1, PerPage: 2},
			{Items: page2, Page: 2, PerPage: 2},
			{Items: page3, Page: 3, PerPage: 2},
		},
		issuesByNumber: buildIssueMap(5, base),
	}
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "issues-resume-tail", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)

	first, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{RepoID: "issues-resume-tail", Page: 1, PerPage: 2, Bounds: &SyncBounds{MaxPages: 1}})
	if err != nil {
		t.Fatalf("first BulkSyncIssues returned error: %v", err)
	}
	if first.SuccessCount != 2 || first.TraversalStatus != "bounded" || first.StopReason != "max_pages" {
		t.Fatalf("first result success/traversal/stop = %d/%q/%q", first.SuccessCount, first.TraversalStatus, first.StopReason)
	}
	frontier, ok, err := store.GetSyncFrontier(ctx, "issues-resume-tail", "issue", syncOrderingUpdatedAtDesc, syncFilterStateAll)
	if err != nil || !ok {
		t.Fatalf("first GetSyncFrontier ok=%v err=%v", ok, err)
	}
	if frontier.Status != "bounded" || frontier.StopReason != "max_pages" {
		t.Fatalf("first frontier = %#v", frontier)
	}

	second, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{RepoID: "issues-resume-tail", Page: 1, PerPage: 2, Bounds: &SyncBounds{}})
	if err != nil {
		t.Fatalf("second BulkSyncIssues returned error: %v", err)
	}
	if len(client.listIssueRequests) != 4 {
		t.Fatalf("ListIssues calls = %d, want 4", len(client.listIssueRequests))
	}
	if second.SuccessCount != 5 || second.PagesListed != 3 || second.RecordsListed != 5 {
		t.Fatalf("second summary success/pages/records = %d/%d/%d", second.SuccessCount, second.PagesListed, second.RecordsListed)
	}
	if second.TraversalStatus != "complete" || second.StopReason != "end_of_collection" || second.WatermarkStatus != "disabled" || second.WatermarkReason != "previous_frontier_bounded" {
		t.Fatalf("second traversal/stop/watermark = %q/%q/%q/%q", second.TraversalStatus, second.StopReason, second.WatermarkStatus, second.WatermarkReason)
	}
	records, err := store.ListRecords(ctx, cache.RecordFilter{RepoID: "issues-resume-tail", Type: "issue"})
	if err != nil {
		t.Fatalf("ListRecords returned error: %v", err)
	}
	if len(records) != 5 {
		t.Fatalf("cached issue records = %d, want 5", len(records))
	}
	frontier, ok, err = store.GetSyncFrontier(ctx, "issues-resume-tail", "issue", syncOrderingUpdatedAtDesc, syncFilterStateAll)
	if err != nil || !ok {
		t.Fatalf("second GetSyncFrontier ok=%v err=%v", ok, err)
	}
	if frontier.Status != "complete" || frontier.StopReason != "end_of_collection" || frontier.RecordsListed != 5 {
		t.Fatalf("second frontier = %#v", frontier)
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

func TestBulkSyncIssuesBoundedDoesNotStopAtCachedWatermark(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatalf("NewInMemorySQLiteStore returned error: %v", err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "issues-watermark", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatalf("AddRepository returned error: %v", err)
	}
	if err := store.UpsertRecordGraph(ctx, cache.RecordGraph{Record: cache.Record{RepoID: "issues-watermark", ID: "ISSUE-2", Type: "issue", Path: "issues/2.md", Title: "Cached", Body: "cached", Status: "open", ContentHash: "cached", Provenance: cache.ProvenanceRemote, RemoteType: "issue", RemoteID: "2", RemoteRevision: "cached-rev", CreatedAt: base.Add(-time.Hour), UpdatedAt: base}}); err != nil {
		t.Fatalf("seed record returned error: %v", err)
	}
	client := &fakeGitCodeClient{
		listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{
			{Items: []gitcode.IssueSummary{
				{ID: "3", Number: 3, Title: "Fresh", State: "open", CreatedAt: base, UpdatedAt: base.Add(5 * time.Minute)},
				{ID: "2", Number: 2, Title: "Cached", State: "open", CreatedAt: base.Add(-time.Hour), UpdatedAt: base},
			}, Page: 1, PerPage: 2},
			{Items: []gitcode.IssueSummary{
				{ID: "1", Number: 1, Title: "Old", State: "open", CreatedAt: base.Add(-2 * time.Hour), UpdatedAt: base.Add(-time.Minute)},
			}, Page: 2, PerPage: 2},
			{Items: []gitcode.IssueSummary{{ID: "0", Number: 0, Title: "Should not list", State: "open", UpdatedAt: base.Add(-2 * time.Minute)}}, Page: 3, PerPage: 2},
		},
	}
	svc := NewWithClient(store, client)

	result, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{
		RepoID:  "issues-watermark",
		PerPage: 2,
		Bounds:  &SyncBounds{MaxPages: 10},
	})
	if err != nil {
		t.Fatalf("BulkSyncIssues returned error: %v", err)
	}
	if len(client.listIssueRequests) != 2 {
		t.Fatalf("ListIssues calls = %d, want 2", len(client.listIssueRequests))
	}
	firstReq := client.listIssueRequests[0]
	if firstReq.State != "all" || firstReq.OrderBy != "updated_at" || firstReq.Direction != "desc" {
		t.Fatalf("unexpected issue list request: %+v", firstReq)
	}
	if result.StopReason != "end_of_collection" || result.Ordering != "updated_at_desc" {
		t.Fatalf("stop/order = %q/%q, want end_of_collection/updated_at_desc", result.StopReason, result.Ordering)
	}
	if result.TraversalStatus != "complete" || result.WatermarkStatus != "disabled" || result.WatermarkReason == "" {
		t.Fatalf("traversal/watermark = %q/%q/%q, want complete/disabled/reason", result.TraversalStatus, result.WatermarkStatus, result.WatermarkReason)
	}
	if result.PagesListed != 2 || result.RecordsListed != 3 || result.SkippedByWatermark != 0 {
		t.Fatalf("summary pages/records/skipped = %d/%d/%d, want 2/3/0", result.PagesListed, result.RecordsListed, result.SkippedByWatermark)
	}
	if result.SuccessCount != 3 {
		t.Fatalf("SuccessCount = %d, want 3 staged records", result.SuccessCount)
	}
}

func TestBulkSyncIssuesCompleteFrontierStopsAfterCachedTail(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	high := base.Add(5 * time.Minute)
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatalf("NewInMemorySQLiteStore returned error: %v", err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "issues-complete-frontier", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatalf("AddRepository returned error: %v", err)
	}
	cachedIssue := gitcode.Issue{ID: "3", Number: 3, Title: "Cached High", State: "open", CreatedAt: base, UpdatedAt: high}
	hash := contentHash(cachedIssue.Title, cachedIssue.Body, cachedIssue.State, cachedIssue.Labels)
	revision := issueRemoteRevision(cachedIssue, hash)
	if err := store.UpsertRecordGraph(ctx, cache.RecordGraph{
		Record:          cache.Record{RepoID: "issues-complete-frontier", ID: "ISSUE-3", Type: "issue", Path: "issues/3.md", Title: cachedIssue.Title, Body: cachedIssue.Body, Status: "open", ContentHash: hash, Provenance: cache.ProvenanceRemote, RemoteType: "issue", RemoteID: "3", RemoteRevision: revision, CreatedAt: base, UpdatedAt: high},
		RemoteRevisions: []cache.RemoteRevision{{RepoID: "issues-complete-frontier", RecordID: "ISSUE-3", RemoteType: "issue", RemoteID: "3", RemoteRevision: revision, Status: "fresh", LastFetchedAt: base}},
	}); err != nil {
		t.Fatalf("seed record returned error: %v", err)
	}
	if err := store.UpsertSyncFrontier(ctx, cache.SyncFrontier{RepoID: "issues-complete-frontier", RemoteType: "issue", Ordering: syncOrderingUpdatedAtDesc, FilterKey: syncFilterStateAll, Status: "complete", HighUpdatedAt: high, HighRemoteID: "3", HighNumber: 3, StopReason: "end_of_collection", UpdatedAt: base}); err != nil {
		t.Fatalf("seed frontier returned error: %v", err)
	}
	client := &fakeGitCodeClient{
		listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{
			{Items: []gitcode.IssueSummary{
				{ID: "4", Number: 4, Title: "New", State: "open", CreatedAt: base, UpdatedAt: high.Add(time.Minute)},
				{ID: "3", Number: 3, Title: "Cached High", State: "open", CreatedAt: base, UpdatedAt: high},
			}, Page: 1, PerPage: 2},
			{Items: []gitcode.IssueSummary{
				{ID: "2", Number: 2, Title: "Covered Tail", State: "open", CreatedAt: base.Add(-time.Hour), UpdatedAt: base},
			}, Page: 2, PerPage: 2},
			{Items: []gitcode.IssueSummary{{ID: "1", Number: 1, Title: "Should not list", State: "open", UpdatedAt: base.Add(-time.Minute)}}, Page: 3, PerPage: 2},
		},
	}
	svc := NewWithClient(store, client)

	result, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{RepoID: "issues-complete-frontier", PerPage: 2, Bounds: &SyncBounds{MaxPages: 10}})
	if err != nil {
		t.Fatalf("BulkSyncIssues returned error: %v", err)
	}
	if len(client.listIssueRequests) != 2 {
		t.Fatalf("ListIssues calls = %d, want 2", len(client.listIssueRequests))
	}
	if client.commentCalls != 1 {
		t.Fatalf("ListIssueComments calls = %d, want 1 for only new issue", client.commentCalls)
	}
	if result.StopReason != "watermark" || result.TraversalStatus != "complete" || result.WatermarkStatus != "used" {
		t.Fatalf("stop/traversal/watermark = %q/%q/%q", result.StopReason, result.TraversalStatus, result.WatermarkStatus)
	}
	if result.PagesListed != 2 || result.RecordsListed != 3 || result.SkippedByWatermark != 1 || result.SuccessCount != 2 {
		t.Fatalf("summary pages/records/skipped/success = %d/%d/%d/%d", result.PagesListed, result.RecordsListed, result.SkippedByWatermark, result.SuccessCount)
	}
	frontier, ok, err := store.GetSyncFrontier(ctx, "issues-complete-frontier", "issue", syncOrderingUpdatedAtDesc, syncFilterStateAll)
	if err != nil || !ok {
		t.Fatalf("GetSyncFrontier ok=%v err=%v", ok, err)
	}
	if frontier.Status != "complete" || frontier.StopReason != "watermark" || !frontier.HighUpdatedAt.Equal(high.Add(time.Minute)) {
		t.Fatalf("frontier = %#v", frontier)
	}
}

func TestBulkSyncPullRequestsBoundedDoesNotStopAtCachedWatermark(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatalf("NewInMemorySQLiteStore returned error: %v", err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "pulls-watermark", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatalf("AddRepository returned error: %v", err)
	}
	if err := store.UpsertRecordGraph(ctx, cache.RecordGraph{Record: cache.Record{RepoID: "pulls-watermark", ID: "PR-8", Type: "pull_request", Path: "pulls/8.md", Title: "Cached", Body: "cached", Status: "open", ContentHash: "cached", Provenance: cache.ProvenanceRemote, RemoteType: "pull_request", RemoteID: "8", RemoteRevision: "cached-rev", CreatedAt: base.Add(-time.Hour), UpdatedAt: base}}); err != nil {
		t.Fatalf("seed record returned error: %v", err)
	}
	client := &fakeGitCodeClient{
		listPRPages: []gitcode.Page[gitcode.PullRequest]{
			{Items: []gitcode.PullRequest{
				{ID: "9", Number: 9, Title: "Fresh", State: "open", CreatedAt: base, UpdatedAt: base.Add(5 * time.Minute)},
				{ID: "8", Number: 8, Title: "Cached", State: "open", CreatedAt: base.Add(-time.Hour), UpdatedAt: base},
			}, Page: 1, PerPage: 2},
			{Items: []gitcode.PullRequest{
				{ID: "7", Number: 7, Title: "Old", State: "open", CreatedAt: base.Add(-2 * time.Hour), UpdatedAt: base.Add(-time.Minute)},
			}, Page: 2, PerPage: 2},
		},
	}
	svc := NewWithClient(store, client)

	result, err := svc.BulkSyncPullRequests(ctx, BulkSyncRequest{
		RepoID:  "pulls-watermark",
		PerPage: 2,
		Bounds:  &SyncBounds{MaxPages: 10},
	})
	if err != nil {
		t.Fatalf("BulkSyncPullRequests returned error: %v", err)
	}
	if len(client.listPRRequests) != 2 {
		t.Fatalf("ListPRs calls = %d, want 2", len(client.listPRRequests))
	}
	firstReq := client.listPRRequests[0]
	if firstReq.State != "all" || firstReq.OrderBy != "updated_at" || firstReq.Direction != "desc" {
		t.Fatalf("unexpected PR list request: %+v", firstReq)
	}
	if result.StopReason != "end_of_collection" || result.PagesListed != 2 || result.RecordsListed != 3 || result.SkippedByWatermark != 0 {
		t.Fatalf("summary stop/pages/records/skipped = %q/%d/%d/%d", result.StopReason, result.PagesListed, result.RecordsListed, result.SkippedByWatermark)
	}
	if result.TraversalStatus != "complete" || result.WatermarkStatus != "disabled" || result.WatermarkReason == "" {
		t.Fatalf("traversal/watermark = %q/%q/%q, want complete/disabled/reason", result.TraversalStatus, result.WatermarkStatus, result.WatermarkReason)
	}
	if result.SuccessCount != 3 {
		t.Fatalf("SuccessCount = %d, want 3 staged records", result.SuccessCount)
	}
}

func TestBulkSyncPullRequestsIgnoresLiveCacheWatermarkWithoutCompleteFrontier(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatalf("NewInMemorySQLiteStore returned error: %v", err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "pulls-live-watermark", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatalf("AddRepository returned error: %v", err)
	}
	if err := store.UpsertRecordGraph(ctx, cache.RecordGraph{Record: cache.Record{RepoID: "pulls-live-watermark", ID: "PR-8", Type: "pull_request", Path: "pulls/8.md", Title: "Cached", Body: "cached", Status: "open", ContentHash: "cached", Provenance: cache.ProvenanceLive, RemoteType: "pull_request", RemoteID: "8", RemoteRevision: "cached-rev", CreatedAt: base.Add(-time.Hour), UpdatedAt: base}}); err != nil {
		t.Fatalf("seed record returned error: %v", err)
	}
	client := &fakeGitCodeClient{
		listPRPages: []gitcode.Page[gitcode.PullRequest]{
			{Items: []gitcode.PullRequest{
				{ID: "8", Number: 8, Title: "Cached", State: "open", CreatedAt: base.Add(-time.Hour), UpdatedAt: base},
				{ID: "7", Number: 7, Title: "Old", State: "open", CreatedAt: base.Add(-2 * time.Hour), UpdatedAt: base.Add(-time.Minute)},
			}, Page: 1, PerPage: 2},
			{Items: []gitcode.PullRequest{{ID: "6", Number: 6, Title: "Should not list", State: "open", UpdatedAt: base.Add(-2 * time.Minute)}}, Page: 2, PerPage: 2},
		},
	}
	svc := NewWithClient(store, client)

	result, err := svc.BulkSyncPullRequests(ctx, BulkSyncRequest{
		RepoID:  "pulls-live-watermark",
		PerPage: 2,
		Bounds:  &SyncBounds{MaxPages: 10},
	})
	if err != nil {
		t.Fatalf("BulkSyncPullRequests returned error: %v", err)
	}
	if len(client.listPRRequests) != 2 {
		t.Fatalf("ListPRs calls = %d, want 2", len(client.listPRRequests))
	}
	if result.StopReason != "end_of_collection" || result.PagesListed != 2 || result.RecordsListed != 3 || result.SkippedByWatermark != 0 {
		t.Fatalf("summary stop/pages/records/skipped = %q/%d/%d/%d", result.StopReason, result.PagesListed, result.RecordsListed, result.SkippedByWatermark)
	}
	if result.SuccessCount != 3 {
		t.Fatalf("SuccessCount = %d, want 3 staged records", result.SuccessCount)
	}
}

func TestBulkSyncPullRequestsCompleteFrontierStopsAfterCachedTail(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC)
	high := base.Add(5 * time.Minute)
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatalf("NewInMemorySQLiteStore returned error: %v", err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "pulls-complete-frontier", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatalf("AddRepository returned error: %v", err)
	}
	if err := store.UpsertSyncFrontier(ctx, cache.SyncFrontier{RepoID: "pulls-complete-frontier", RemoteType: "pull_request", Ordering: syncOrderingUpdatedAtDesc, FilterKey: syncFilterStateAll, Status: "complete", HighUpdatedAt: high, HighRemoteID: "8", HighNumber: 8, StopReason: "end_of_collection", UpdatedAt: base}); err != nil {
		t.Fatalf("seed frontier returned error: %v", err)
	}
	client := &fakeGitCodeClient{
		listPRPages: []gitcode.Page[gitcode.PullRequest]{
			{Items: []gitcode.PullRequest{
				{ID: "9", Number: 9, Title: "New", State: "open", CreatedAt: base, UpdatedAt: high.Add(time.Minute)},
				{ID: "8", Number: 8, Title: "Cached High", State: "open", CreatedAt: base, UpdatedAt: high},
			}, Page: 1, PerPage: 2},
			{Items: []gitcode.PullRequest{
				{ID: "7", Number: 7, Title: "Covered Tail", State: "open", CreatedAt: base.Add(-time.Hour), UpdatedAt: base},
			}, Page: 2, PerPage: 2},
		},
	}
	svc := NewWithClient(store, client)

	result, err := svc.BulkSyncPullRequests(ctx, BulkSyncRequest{RepoID: "pulls-complete-frontier", PerPage: 2, Bounds: &SyncBounds{MaxPages: 10}})
	if err != nil {
		t.Fatalf("BulkSyncPullRequests returned error: %v", err)
	}
	if len(client.listPRRequests) != 2 {
		t.Fatalf("ListPRs calls = %d, want 2", len(client.listPRRequests))
	}
	if result.StopReason != "watermark" || result.TraversalStatus != "complete" || result.WatermarkStatus != "used" {
		t.Fatalf("stop/traversal/watermark = %q/%q/%q", result.StopReason, result.TraversalStatus, result.WatermarkStatus)
	}
	if result.PagesListed != 2 || result.RecordsListed != 3 || result.SkippedByWatermark != 1 || result.SuccessCount != 2 {
		t.Fatalf("summary pages/records/skipped/success = %d/%d/%d/%d", result.PagesListed, result.RecordsListed, result.SkippedByWatermark, result.SuccessCount)
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

func TestBulkSyncWikiRouteFailureClassifiesAPIValidation(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatalf("NewInMemorySQLiteStore returned error: %v", err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "wiki-route-400", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeWiki}}); err != nil {
		t.Fatalf("AddRepository returned error: %v", err)
	}
	client := &fakeGitCodeClient{
		listWikiErrors: []error{gitcode.ErrAPIValidation{Endpoint: "/api/v5/repos/owner/repo.wiki/contents", Status: 400, Message: "bad request"}},
	}
	svc := NewWithClient(store, client)

	result, err := svc.BulkSyncWiki(ctx, BulkSyncRequest{RepoID: "wiki-route-400"})
	if err == nil {
		t.Fatal("BulkSyncWiki returned nil error, want PartialSyncError")
	}
	var partial *PartialSyncError
	if !errors.As(err, &partial) {
		t.Fatalf("BulkSyncWiki error = %T %v, want *PartialSyncError", err, err)
	}
	if result == nil || len(result.Failures) != 1 {
		t.Fatalf("result failures = %#v, want one failure", result)
	}
	failure := result.Failures[0]
	if failure.FailureClass != "api_validation" || failure.Endpoint != "/api/v5/repos/owner/repo.wiki/contents" || failure.StatusCode != 400 {
		t.Fatalf("failure metadata = %#v", failure)
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

func issueSummariesWithUpdatedAt(offset, count int, base time.Time) []gitcode.IssueSummary {
	out := generateIssueSummaries(offset, count)
	for i := range out {
		out[i].CreatedAt = base.Add(-time.Duration(out[i].Number) * time.Minute)
		out[i].UpdatedAt = base.Add(-time.Duration(out[i].Number) * time.Minute)
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
