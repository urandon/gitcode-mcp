package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
)

func TestDurableOfflineFixtureIssueBatchCommits(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc, err := NewWithMode(store, gitcode.ProviderModeFixture, "", ServiceConfig{})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := svc.FetchIssueSyncBatch(ctx, BulkSyncRequest{RepoID: "fixture-a", PerPage: 100, Bounds: &SyncBounds{MaxPages: 1}})
	if err != nil {
		t.Fatalf("FetchIssueSyncBatch: %v", err)
	}
	result, err := svc.CommitIssueSyncBatch(ctx, batch, nil)
	if err != nil {
		t.Fatalf("CommitIssueSyncBatch: %T %v; batch=%+v result=%+v", err, err, batch, result)
	}
	if result.SuccessCount != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestDurableIssueBatchFetchDoesNotHoldTargetWriterAndCommitDoesNotRefetch(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{{
		Items: []gitcode.IssueSummary{{ID: "provider-42", Number: 42, Title: "Fetched once", Body: "durable body", State: "open", CreatedAt: base, UpdatedAt: base}},
		Page:  1, PerPage: 100,
	}}}
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "durable-issues", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)
	held, err := store.AcquireWriter(ctx, cache.WriterRequest{Operation: "external-writer", RepoID: "durable-issues", LockPath: svc.lockPath})
	if err != nil {
		t.Fatalf("AcquireWriter: %v", err)
	}
	batch, err := svc.FetchIssueSyncBatch(ctx, BulkSyncRequest{RepoID: "durable-issues", PerPage: 100, Bounds: &SyncBounds{MaxPages: 1}})
	if err != nil {
		t.Fatalf("FetchIssueSyncBatch while target writer held: %v", err)
	}
	if len(client.listIssueRequests) != 1 || batch.RecordCount() != 1 || batch.TraversalStatus != "complete" {
		t.Fatalf("fetch calls=%d batch=%+v", len(client.listIssueRequests), batch)
	}
	encoded, err := json.Marshal(batch)
	if err != nil {
		t.Fatalf("Marshal batch: %v", err)
	}
	var replay DurableIssueSyncBatch
	if err := json.Unmarshal(encoded, &replay); err != nil {
		t.Fatalf("Unmarshal batch: %v", err)
	}
	if _, err := svc.CommitIssueSyncBatch(ctx, replay, nil); err == nil {
		t.Fatal("commit succeeded while target writer was held")
	} else {
		var contention cache.ErrLockContention
		if !errors.As(err, &contention) {
			t.Fatalf("commit error = %T %v, want cache contention", err, err)
		}
	}
	if len(client.listIssueRequests) != 1 {
		t.Fatalf("contended commit refetched provider: calls=%d", len(client.listIssueRequests))
	}
	if err := store.ReleaseWriter(ctx, held); err != nil {
		t.Fatalf("ReleaseWriter: %v", err)
	}
	result, err := svc.CommitIssueSyncBatch(ctx, replay, nil)
	if err != nil {
		t.Fatalf("CommitIssueSyncBatch retry: %v", err)
	}
	if result.SuccessCount != 1 || len(client.listIssueRequests) != 1 {
		t.Fatalf("result=%+v provider calls=%d", result, len(client.listIssueRequests))
	}
	stored, err := store.GetSourceScoped(ctx, "durable-issues", "ISSUE-42")
	if err != nil {
		t.Fatalf("GetSourceScoped: %v", err)
	}
	if stored.Body != "durable body" {
		t.Fatalf("stored body = %q", stored.Body)
	}
	frontier, ok, err := store.GetSyncFrontier(ctx, "durable-issues", "issue", syncOrderingUpdatedAtDesc, syncFilterStateAll)
	if err != nil || !ok || frontier.Status != "complete" || frontier.RecordsListed != 1 {
		t.Fatalf("frontier=%+v ok=%v err=%v", frontier, ok, err)
	}
}

func TestDurableIssueBatchValidatesWholeBatchBeforePublishing(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "durable-atomic-validation", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	batch := DurableIssueSyncBatch{Version: DurableSyncBatchVersion, RepoID: "durable-atomic-validation", Collection: "issues", IdempotencyKey: "atomic-validation", Items: []DurableIssueItem{{Number: 1, Title: "valid first"}, {Number: 0, Title: "invalid second"}}, PagesListed: 1, RecordsListed: 2, StopReason: "end_of_collection", TraversalStatus: "complete"}
	if _, err := NewWithClient(store, &fakeGitCodeClient{}).CommitIssueSyncBatch(ctx, batch, nil); err == nil {
		t.Fatal("CommitIssueSyncBatch accepted invalid staged item")
	}
	if _, err := store.GetSourceScoped(ctx, batch.RepoID, "ISSUE-1"); err == nil {
		t.Fatal("valid first item was published before full-batch validation")
	}
	if _, ok, err := store.GetSyncFrontier(ctx, batch.RepoID, "issue", syncOrderingUpdatedAtDesc, syncFilterStateAll); err != nil || ok {
		t.Fatalf("frontier ok=%v err=%v after rejected batch", ok, err)
	}
}

func TestDurablePullAndWikiBatchesCommitWithoutProviderReplay(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listPRPages:   []gitcode.Page[gitcode.PullRequest]{{Items: []gitcode.PullRequest{{ID: "pr-7", Number: 7, Title: "Durable PR", Body: "pull body", State: "open", CreatedAt: base, UpdatedAt: base}}, Page: 1, PerPage: 100}},
		listWikiPages: []gitcode.Page[gitcode.WikiPage]{{Items: []gitcode.WikiPage{{ID: "wiki-home", Slug: "Home", Title: "Home", Body: "wiki body", Revision: "rev-1", CreatedAt: base, UpdatedAt: base}}, Page: 1, PerPage: 100}},
	}
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "durable-mixed", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)
	held, err := store.AcquireWriter(ctx, cache.WriterRequest{Operation: "external-writer", RepoID: "durable-mixed", LockPath: svc.lockPath})
	if err != nil {
		t.Fatal(err)
	}
	pulls, err := svc.FetchPullSyncBatch(ctx, BulkSyncRequest{RepoID: "durable-mixed", PerPage: 100, Bounds: &SyncBounds{MaxPages: 1}})
	if err != nil {
		t.Fatalf("FetchPullSyncBatch: %v", err)
	}
	wiki, err := svc.FetchWikiSyncBatch(ctx, BulkSyncRequest{RepoID: "durable-mixed", PerPage: 100, Bounds: &SyncBounds{MaxPages: 1}})
	if err != nil {
		t.Fatalf("FetchWikiSyncBatch: %v", err)
	}
	if _, err := svc.CommitPullSyncBatch(ctx, pulls, nil); err == nil {
		t.Fatal("pull commit succeeded under external writer")
	}
	if _, err := svc.CommitWikiSyncBatch(ctx, wiki, nil); err == nil {
		t.Fatal("wiki commit succeeded under external writer")
	}
	if client.listPRCalls != 1 || client.listWikiPagesCallCount != 1 {
		t.Fatalf("provider calls after contention: pulls=%d wiki=%d", client.listPRCalls, client.listWikiPagesCallCount)
	}
	if err := store.ReleaseWriter(ctx, held); err != nil {
		t.Fatal(err)
	}
	if result, err := svc.CommitPullSyncBatch(ctx, pulls, nil); err != nil || result.SuccessCount != 1 {
		t.Fatalf("CommitPullSyncBatch result=%+v err=%v", result, err)
	}
	if result, err := svc.CommitWikiSyncBatch(ctx, wiki, nil); err != nil || result.SuccessCount != 1 {
		t.Fatalf("CommitWikiSyncBatch result=%+v err=%v", result, err)
	}
	if client.listPRCalls != 1 || client.listWikiPagesCallCount != 1 || client.wikiCalls != 0 {
		t.Fatalf("commit replay called provider: pulls=%d wiki-list=%d wiki-detail=%d", client.listPRCalls, client.listWikiPagesCallCount, client.wikiCalls)
	}
}

type aggregateIssueCommentClient struct {
	*fakeGitCodeClient
	pages    map[int]gitcode.Page[gitcode.Comment]
	err      error
	requests []gitcode.RepositoryIssueCommentListRequest
}

func (c *aggregateIssueCommentClient) ListRepositoryIssueComments(_ context.Context, req gitcode.RepositoryIssueCommentListRequest) (gitcode.Page[gitcode.Comment], error) {
	c.requests = append(c.requests, req)
	if c.err != nil {
		return gitcode.Page[gitcode.Comment]{}, c.err
	}
	return c.pages[req.Page], nil
}

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
	if client.commentCalls != 0 {
		t.Fatalf("first parent backfill ListIssueComments calls = %d, want 0", client.commentCalls)
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
		if result.Counts.Listed != 1 || result.Counts.FetchedDetail != 0 {
			t.Fatalf("first result %d counts = %#v, want parent-only listed=1 fetched_detail=0", i, result.Counts)
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
	if first.IssueComments == nil || first.IssueComments.Pending != 1 || first.IssueComments.Complete != 1 {
		t.Fatalf("first issue comment queue = %#v, want pending=1 complete=1", first.IssueComments)
	}
	status, err := svc.GetSyncStatus(ctx, SyncStatusRequest{RepoID: "bulk-issues", ID: "ISSUE-1"})
	if err != nil {
		t.Fatalf("GetSyncStatus ISSUE-1 returned error: %v", err)
	}
	if status.Status != "fresh" || status.IssueComments == nil || status.IssueComments.Status != "pending" || status.IssueComments.ExpectedCount != 1 {
		t.Fatalf("ISSUE-1 sync status = %#v, want fresh parent with pending comment coverage", status)
	}
	summary, err := svc.SyncStatus(ctx, ListSourcesRequest{RepoID: "bulk-issues"})
	if err != nil {
		t.Fatalf("SyncStatus returned error: %v", err)
	}
	if summary.IssueComments == nil || summary.IssueComments.Pending != 1 || summary.IssueComments.Complete != 1 {
		t.Fatalf("aggregate issue comment status = %#v", summary.IssueComments)
	}
	drained, err := svc.BulkSyncIssueComments(ctx, BulkSyncRequest{RepoID: "bulk-issues", PerPage: 100})
	if err != nil {
		t.Fatalf("BulkSyncIssueComments returned error: %v", err)
	}
	if drained.IssueComments == nil || drained.IssueComments.Pending != 0 || drained.IssueComments.Complete != 2 || client.commentCalls != 1 {
		t.Fatalf("drained queue=%#v comment_calls=%d", drained.IssueComments, client.commentCalls)
	}
	second, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{RepoID: "bulk-issues", IdempotencyKey: "bulk-issues-second", PerPage: 100})
	if err != nil {
		t.Fatalf("BulkSyncIssues second returned error: %v", err)
	}
	if second.SuccessCount != 2 || second.FailureCount != 0 {
		t.Fatalf("second counts = success %d failure %d, want 2/0", second.SuccessCount, second.FailureCount)
	}
	if client.commentCalls != 1 {
		t.Fatalf("parent resync ListIssueComments calls = %d, want drain-only call count 1", client.commentCalls)
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

func TestBulkSyncIssueCommentsUsesRepositoryAggregateAndReportsAvoidedRequests(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 7, 13, 14, 0, 0, 0, time.UTC)
	parents := []gitcode.IssueSummary{
		{ID: "7001", Number: 7, Title: "First", State: "open", Comments: 1, CreatedAt: base, UpdatedAt: base.Add(3 * time.Minute)},
		{ID: "7002", Number: 8, Title: "Second", State: "open", Comments: 1, CreatedAt: base, UpdatedAt: base.Add(2 * time.Minute)},
		{ID: "7003", Number: 9, Title: "Third", State: "open", Comments: 1, CreatedAt: base, UpdatedAt: base.Add(time.Minute)},
	}
	client := &aggregateIssueCommentClient{
		fakeGitCodeClient: &fakeGitCodeClient{listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{{Items: parents}, {Items: nil}}},
		pages: map[int]gitcode.Page[gitcode.Comment]{
			1: {Items: []gitcode.Comment{{ID: "c7", IssueID: "7001", IssueNumber: 7, Body: "issue-comment-seven-unique", CreatedAt: base, UpdatedAt: base}, {ID: "c8", IssueID: "7002", IssueNumber: 8, Body: "issue-comment-eight-unique", CreatedAt: base, UpdatedAt: base}}, Page: 1, PerPage: 2, TotalCount: 3, NextPage: 2},
			2: {Items: []gitcode.Comment{{ID: "c9", IssueID: "7003", IssueNumber: 9, Body: "issue-comment-nine-unique", CreatedAt: base, UpdatedAt: base}}, Page: 2, PerPage: 2, TotalCount: 3},
		},
	}
	store := newBulkIssueCommentStore(t, ctx, "aggregate-comments")
	defer store.Close()
	svc := NewWithClient(store, client)
	if _, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{RepoID: "aggregate-comments", PerPage: 3, Bounds: &SyncBounds{MaxPages: 10}}); err != nil {
		t.Fatalf("BulkSyncIssues returned error: %v", err)
	}
	if err := store.UpsertRecordComments(ctx, "aggregate-comments", "ISSUE-7", []cache.RecordComment{{CommentID: "stale", Body: "remove me", CreatedAt: base, UpdatedAt: base}}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSyncGraph(ctx, cache.SyncGraph{
		RepoID:     "aggregate-comments",
		Provenance: cache.ProvenanceLive,
		Record:     cache.Record{ID: "ISSUECOMMENT-7-stale", Type: "issue_comment", Path: "issues/7/comments/stale.md", Title: "Stale comment", Body: "issue-comment-stale-unique", Status: "current", ContentHash: "stale", RemoteType: "issue_comment", RemoteID: "7:stale", CreatedAt: base, UpdatedAt: base},
		Links:      []cache.Link{{SourceID: "ISSUECOMMENT-7-stale", TargetID: "ISSUE-7", Kind: "parent", Text: "issue"}},
	}); err != nil {
		t.Fatal(err)
	}
	progress := make(chan ProgressEvent, 8)
	result, err := svc.BulkSyncIssueComments(ctx, BulkSyncRequest{RepoID: "aggregate-comments", PerPage: 2, Bounds: &SyncBounds{ProgressChan: progress}, ProgressChan: progress})
	if err != nil {
		t.Fatalf("BulkSyncIssueComments returned error: %v", err)
	}
	if client.commentCalls != 0 || len(client.requests) != 2 {
		t.Fatalf("per_issue=%d aggregate=%d", client.commentCalls, len(client.requests))
	}
	if result.SuccessCount != 3 || result.PagesListed != 2 || result.RecordsListed != 3 || result.IssueComments == nil || result.IssueComments.Strategy != "repository_aggregate" || result.IssueComments.ParentRequestsAvoided != 1 || result.IssueComments.Unreconciled != 0 {
		t.Fatalf("result=%#v", result)
	}
	for number, commentID := range map[int]string{7: "c7", 8: "c8", 9: "c9"} {
		record, err := store.GetRecord(ctx, "aggregate-comments", fmt.Sprintf("ISSUE-%d", number))
		if err != nil || len(record.Comments) != 1 || record.Comments[0].CommentID != commentID {
			t.Fatalf("number=%d record=%#v err=%v", number, record, err)
		}
		sourceID := fmt.Sprintf("ISSUECOMMENT-%d-%s", number, commentID)
		source, err := svc.GetSource(ctx, GetSourceRequest{RepoID: "aggregate-comments", ID: sourceID})
		if err != nil || source.Kind != "issue_comment" || source.Body == "" || len(source.Links) != 1 || source.Links[0].TargetID != fmt.Sprintf("ISSUE-%d", number) || source.Links[0].Kind != "parent" {
			t.Fatalf("number=%d source=%#v err=%v", number, source, err)
		}
	}
	listed, err := svc.ListSources(ctx, ListSourcesRequest{RepoID: "aggregate-comments", Kind: "issue_comment"})
	if err != nil || len(listed.Results) != 3 {
		t.Fatalf("listed=%#v err=%v", listed, err)
	}
	found, err := svc.SearchSources(ctx, SearchSourcesRequest{RepoID: "aggregate-comments", Query: "issue-comment-seven-unique", Kind: "issue_comment"})
	if err != nil || len(found.Results) != 1 || found.Results[0].ID != "ISSUECOMMENT-7-c7" {
		t.Fatalf("found=%#v err=%v", found, err)
	}
	if _, err := store.GetSourceScoped(ctx, "aggregate-comments", "ISSUECOMMENT-7-stale"); !errors.Is(err, cache.ErrNotFound) {
		t.Fatalf("stale source error = %v, want ErrNotFound", err)
	}
	stale, err := store.SearchSources(ctx, cache.SearchQuery{RepoID: "aggregate-comments", Query: "issue-comment-stale-unique", Kind: "issue_comment", Limit: 10})
	if err != nil || len(stale) != 0 {
		t.Fatalf("stale search=%#v err=%v", stale, err)
	}
	second, err := svc.BulkSyncIssueComments(ctx, BulkSyncRequest{RepoID: "aggregate-comments", PerPage: 2, Bounds: &SyncBounds{}})
	if err != nil || second.StopReason != "queue_empty" || len(client.requests) != 2 {
		t.Fatalf("second=%#v requests=%d err=%v", second, len(client.requests), err)
	}
}

func TestBulkSyncIssueCommentsAggregateBoundedRunRestartsSafely(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 7, 13, 15, 0, 0, 0, time.UTC)
	parents := []gitcode.IssueSummary{{ID: "7101", Number: 11, Title: "One", State: "open", Comments: 2, CreatedAt: base, UpdatedAt: base.Add(time.Minute)}}
	client := &aggregateIssueCommentClient{
		fakeGitCodeClient: &fakeGitCodeClient{listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{{Items: parents}, {Items: nil}}},
		pages: map[int]gitcode.Page[gitcode.Comment]{
			1: {Items: []gitcode.Comment{{ID: "c11a", IssueID: "7101", IssueNumber: 11, Body: "first"}}, Page: 1, PerPage: 1, NextPage: 2},
			2: {Items: []gitcode.Comment{{ID: "c11b", IssueID: "7101", IssueNumber: 11, Body: "second"}}, Page: 2, PerPage: 1},
		},
	}
	store := newBulkIssueCommentStore(t, ctx, "aggregate-resume")
	defer store.Close()
	svc := NewWithClient(store, client)
	if _, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{RepoID: "aggregate-resume", PerPage: 1, Bounds: &SyncBounds{MaxPages: 10}}); err != nil {
		t.Fatal(err)
	}
	first, err := svc.BulkSyncIssueComments(ctx, BulkSyncRequest{RepoID: "aggregate-resume", Page: 7, PerPage: 1, Bounds: &SyncBounds{MaxPages: 1}})
	if err != nil || first.TraversalStatus != "bounded" || first.StopReason != "max_pages" || first.SuccessCount != 0 || first.IssueComments.Pending != 1 {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	second, err := svc.BulkSyncIssueComments(ctx, BulkSyncRequest{RepoID: "aggregate-resume", PerPage: 1, Bounds: &SyncBounds{}})
	if err != nil || second.SuccessCount != 1 || second.IssueComments.Complete != 1 {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	if len(client.requests) != 3 || client.requests[0].Page != 1 || client.requests[1].Page != 1 || client.requests[2].Page != 2 {
		t.Fatalf("requests=%#v", client.requests)
	}
	record, err := store.GetRecord(ctx, "aggregate-resume", "ISSUE-11")
	if err != nil || len(record.Comments) != 2 {
		t.Fatalf("record=%#v err=%v", record, err)
	}
}

func TestBulkSyncIssueCommentsIncrementalQueueDrainsBoundedWork(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 8, 11, 14, 0, 0, 0, time.UTC)
	parents := []gitcode.IssueSummary{
		{ID: "8101", Number: 11, Title: "One", State: "open", Comments: 1, CreatedAt: base, UpdatedAt: base},
		{ID: "8102", Number: 12, Title: "Two", State: "open", Comments: 1, CreatedAt: base, UpdatedAt: base.Add(time.Minute)},
	}
	client := &fakeGitCodeClient{
		listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{{Items: parents, Page: 1, PerPage: 100}},
		commentsByIssue: map[int][]gitcode.Comment{
			11: {{ID: "c11", IssueID: "8101", IssueNumber: 11, Body: "one", CreatedAt: base, UpdatedAt: base}},
			12: {{ID: "c12", IssueID: "8102", IssueNumber: 12, Body: "two", CreatedAt: base, UpdatedAt: base}},
		},
	}
	store := newBulkIssueCommentStore(t, ctx, "incremental-comments")
	defer store.Close()
	svc := NewWithClient(store, client)
	if _, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{RepoID: "incremental-comments", PerPage: 100, Bounds: &SyncBounds{MaxPages: 10}}); err != nil {
		t.Fatal(err)
	}
	request := BulkSyncRequest{RepoID: "incremental-comments", IncrementalQueue: true, Bounds: &SyncBounds{MaxPages: 1}}
	first, err := svc.BulkSyncIssueComments(ctx, request)
	if err != nil || first.SuccessCount != 1 || first.PagesListed != 1 || first.TraversalStatus != "bounded" || first.IssueComments.Pending != 1 {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	second, err := svc.BulkSyncIssueComments(ctx, request)
	if err != nil || second.SuccessCount != 1 || second.PagesListed != 1 || second.TraversalStatus != "complete" || second.IssueComments.Complete != 2 {
		t.Fatalf("second=%#v err=%v", second, err)
	}
}

func TestBulkSyncIssueCommentsAggregateParentFailureIsExplicitAndRetryable(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 7, 13, 16, 0, 0, 0, time.UTC)
	client := &aggregateIssueCommentClient{
		fakeGitCodeClient: &fakeGitCodeClient{listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{
			{Items: []gitcode.IssueSummary{{ID: "7201", Number: 21, Title: "Known", State: "open", Comments: 1, CreatedAt: base, UpdatedAt: base}}},
			{Items: nil},
		}},
		pages: map[int]gitcode.Page[gitcode.Comment]{1: {Items: []gitcode.Comment{{ID: "orphan", IssueID: "9999", IssueNumber: 99, Body: "orphan"}, {ID: "known", IssueID: "7201", IssueNumber: 21, Body: "known"}}, Page: 1, PerPage: 100}},
	}
	store := newBulkIssueCommentStore(t, ctx, "aggregate-reconcile")
	defer store.Close()
	svc := NewWithClient(store, client)
	if _, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{RepoID: "aggregate-reconcile", PerPage: 1, Bounds: &SyncBounds{MaxPages: 10}}); err != nil {
		t.Fatal(err)
	}
	result, err := svc.BulkSyncIssueComments(ctx, BulkSyncRequest{RepoID: "aggregate-reconcile", PerPage: 100, Bounds: &SyncBounds{}})
	var partial *PartialSyncError
	if !errors.As(err, &partial) || result.TraversalStatus != "partial" || result.StopReason != "reconciliation_failed" || len(result.Failures) != 1 || result.Failures[0].RemoteType != "issue_comment_reconciliation" || result.Failures[0].RecoveryAction == "" || result.IssueComments.Unreconciled != 1 {
		t.Fatalf("result=%#v err=%T %v", result, err, err)
	}
	queue, ok, queueErr := store.GetIssueCommentSync(ctx, "aggregate-reconcile", "ISSUE-21")
	if queueErr != nil || !ok || queue.Status != "pending" {
		t.Fatalf("queue=%#v ok=%t err=%v", queue, ok, queueErr)
	}
}

func TestBulkSyncIssueCommentsAggregateRejectsIncompleteReportedTotal(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 7, 13, 16, 30, 0, 0, time.UTC)
	client := &aggregateIssueCommentClient{
		fakeGitCodeClient: &fakeGitCodeClient{listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{
			{Items: []gitcode.IssueSummary{{ID: "7251", Number: 25, Title: "Known", State: "open", Comments: 1, CreatedAt: base, UpdatedAt: base}}},
			{Items: nil},
		}},
		pages: map[int]gitcode.Page[gitcode.Comment]{1: {Items: []gitcode.Comment{{ID: "known", IssueID: "7251", IssueNumber: 25, Body: "known"}}, Page: 1, PerPage: 100, TotalCount: 2}},
	}
	store := newBulkIssueCommentStore(t, ctx, "aggregate-total")
	defer store.Close()
	svc := NewWithClient(store, client)
	if _, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{RepoID: "aggregate-total", PerPage: 1, Bounds: &SyncBounds{MaxPages: 10}}); err != nil {
		t.Fatal(err)
	}
	result, err := svc.BulkSyncIssueComments(ctx, BulkSyncRequest{RepoID: "aggregate-total", PerPage: 100, Bounds: &SyncBounds{}})
	var partial *PartialSyncError
	if !errors.As(err, &partial) || result.FailureCount != 1 || result.Failures[0].RemoteType != "issue_comment_reconciliation" || !strings.Contains(result.Failures[0].Message, "expected total_count 2") {
		t.Fatalf("result=%#v err=%T %v", result, err, err)
	}
}

func TestBulkSyncIssueCommentsFallsBackWhenAggregateEndpointUnsupported(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 7, 13, 17, 0, 0, 0, time.UTC)
	client := &aggregateIssueCommentClient{
		fakeGitCodeClient: &fakeGitCodeClient{
			listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{
				{Items: []gitcode.IssueSummary{{ID: "7301", Number: 31, Title: "Fallback", State: "open", Comments: 1, CreatedAt: base, UpdatedAt: base}}},
				{Items: nil},
			},
			commentsByIssue: map[int][]gitcode.Comment{31: {{ID: "fallback-comment", Body: "fallback"}}},
		},
		err: gitcode.ErrUnsupportedCapability{CapabilityKey: "repository_issue_comments", Message: "unsupported fixture"},
	}
	store := newBulkIssueCommentStore(t, ctx, "aggregate-fallback")
	defer store.Close()
	svc := NewWithClient(store, client)
	if _, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{RepoID: "aggregate-fallback", PerPage: 1, Bounds: &SyncBounds{MaxPages: 10}}); err != nil {
		t.Fatal(err)
	}
	result, err := svc.BulkSyncIssueComments(ctx, BulkSyncRequest{RepoID: "aggregate-fallback", Bounds: &SyncBounds{}})
	if err != nil || len(client.requests) != 1 || client.commentCalls != 1 || result.IssueComments.Strategy != "per_issue_fallback" || result.IssueComments.FallbackReason != "aggregate_endpoint_unsupported" || result.SuccessCount != 1 {
		t.Fatalf("result=%#v aggregate=%d per_issue=%d err=%v", result, len(client.requests), client.commentCalls, err)
	}
}

func newBulkIssueCommentStore(t *testing.T, ctx context.Context, repoID string) *cache.SQLiteStore {
	t.Helper()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: repoID, Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		store.Close()
		t.Fatal(err)
	}
	return store
}

func TestBulkSyncIssuesIgnoresDeferredIssueCommentsRead(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 26, 16, 30, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{
			{Items: []gitcode.IssueSummary{{ID: "4119847", Number: 16, Title: "Live issue", Body: "live body", State: "open", Comments: 1, CreatedAt: base, UpdatedAt: base}}, Page: 1, PerPage: 1},
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
	if len(result.Results) != 1 || result.Results[0].Counts.Deferred != 1 || client.commentCalls != 0 {
		t.Fatalf("parent result = %+v calls=%d, want queued comments without child read", result.Results, client.commentCalls)
	}
	source, err := store.GetSourceScoped(ctx, "bulk-issues-comments-deferred", "ISSUE-16")
	if err != nil {
		t.Fatalf("ISSUE-16 missing: %v", err)
	}
	if source.Title != "Live issue" || source.Body != "live body" {
		t.Fatalf("source=%+v", source)
	}
	drained, err := svc.BulkSyncIssueComments(ctx, BulkSyncRequest{RepoID: "bulk-issues-comments-deferred", PerPage: 1})
	if err != nil {
		t.Fatalf("BulkSyncIssueComments returned error: %v", err)
	}
	if drained.TraversalStatus != "deferred" || drained.StopReason != "comments_read_unsupported" || drained.IssueComments == nil || drained.IssueComments.Deferred != 1 || client.commentCalls != 1 {
		t.Fatalf("drained=%+v calls=%d", drained, client.commentCalls)
	}
}

func TestBulkSyncIssuesDefersRateLimitedIssueCommentsRead(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{
			{Items: []gitcode.IssueSummary{{ID: "11985", Number: 11985, Title: "Runtime issue", Body: "primary issue body", State: "open", Comments: 3, CreatedAt: base, UpdatedAt: base}}, Page: 1, PerPage: 1},
			{Items: []gitcode.IssueSummary{{ID: "11985", Number: 11985, Title: "Runtime issue", Body: "primary issue body", State: "open", Comments: 3, CreatedAt: base, UpdatedAt: base}}, Page: 1, PerPage: 1},
		},
		listIssueCommentsErr: gitcode.ErrRateLimited{
			Endpoint:      "/api/v5/repos/owner/repo/issues/11985/comments",
			RetryAfter:    2 * time.Second,
			RawRetryAfter: "2",
			Attempts:      3,
		},
	}
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "bulk-issues-comments-rate-limited", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)
	result, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{RepoID: "bulk-issues-comments-rate-limited", PerPage: 1})
	if err != nil {
		t.Fatalf("BulkSyncIssues returned error: %v", err)
	}
	if result.SuccessCount != 1 || result.FailureCount != 0 {
		t.Fatalf("counts = success %d failure %d, want 1/0", result.SuccessCount, result.FailureCount)
	}
	if len(result.Results) != 1 || result.Results[0].Counts.Deferred != 1 || result.Results[0].Counts.FetchedDetail != 0 || client.commentCalls != 0 {
		t.Fatalf("result counts = %+v calls=%d, want queued comment work without child reads", result.Results, client.commentCalls)
	}
	source, err := store.GetSourceScoped(ctx, "bulk-issues-comments-rate-limited", "ISSUE-11985")
	if err != nil {
		t.Fatalf("ISSUE-11985 missing: %v", err)
	}
	if source.Title != "Runtime issue" || source.Body != "primary issue body" {
		t.Fatalf("source=%+v", source)
	}
	record, err := store.GetRecord(ctx, "bulk-issues-comments-rate-limited", "ISSUE-11985")
	if err != nil {
		t.Fatalf("record missing: %v", err)
	}
	if len(record.Comments) != 0 {
		t.Fatalf("comments=%+v, want comments deferred", record.Comments)
	}
	status, err := store.GetSyncStatusScoped(ctx, "bulk-issues-comments-rate-limited", "ISSUE-11985")
	if err != nil {
		t.Fatalf("sync status missing: %v", err)
	}
	if status.Status != "fresh" {
		t.Fatalf("primary sync status = %q, want fresh despite queued child coverage", status.Status)
	}
	deferred, err := svc.BulkSyncIssueComments(ctx, BulkSyncRequest{RepoID: "bulk-issues-comments-rate-limited", PerPage: 1})
	if err != nil {
		t.Fatalf("BulkSyncIssueComments deferred returned error: %v", err)
	}
	if deferred.TraversalStatus != "deferred" || deferred.StopReason != "rate_limited" || deferred.IssueComments == nil || deferred.IssueComments.Deferred != 1 {
		t.Fatalf("deferred drain=%+v", deferred)
	}

	client.listIssueCommentsErr = nil
	client.commentsByIssue = map[int][]gitcode.Comment{
		11985: {{ID: "comment-1", Author: "reviewer", Body: "late comment", CreatedAt: base.Add(time.Minute), UpdatedAt: base.Add(time.Minute)}},
	}
	second, err := svc.BulkSyncIssueComments(ctx, BulkSyncRequest{RepoID: "bulk-issues-comments-rate-limited", IdempotencyKey: "retry-comments", PerPage: 1})
	if err != nil {
		t.Fatalf("second BulkSyncIssues returned error: %v", err)
	}
	if len(second.Results) != 1 || second.Results[0].Counts.FetchedDetail != 1 || second.Results[0].Counts.Deferred != 0 || second.IssueComments == nil || second.IssueComments.Complete != 1 {
		t.Fatalf("second counts = %+v queue=%#v, want completed queued comment retry", second.Results, second.IssueComments)
	}
	record, err = store.GetRecord(ctx, "bulk-issues-comments-rate-limited", "ISSUE-11985")
	if err != nil {
		t.Fatalf("record missing after retry: %v", err)
	}
	if len(record.Comments) != 1 || record.Comments[0].CommentID != "comment-1" {
		t.Fatalf("comments=%+v, want retry comment", record.Comments)
	}
}

func TestBulkSyncIssuesFetchesCommentsWhenListRevisionChanges(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 6, 27, 11, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listIssuesPages: []gitcode.Page[gitcode.IssueSummary]{
			{Items: []gitcode.IssueSummary{{ID: "11", Number: 11, Title: "Issue", Body: "body", State: "open", Comments: 0, CreatedAt: base, UpdatedAt: base}}, Page: 1, PerPage: 100},
			{Items: []gitcode.IssueSummary{{ID: "11", Number: 11, Title: "Issue", Body: "body", State: "open", Comments: 1, CreatedAt: base, UpdatedAt: base.Add(time.Minute)}}, Page: 1, PerPage: 100},
			{Items: []gitcode.IssueSummary{{ID: "11", Number: 11, Title: "Issue", Body: "body", State: "open", Comments: 0, CreatedAt: base, UpdatedAt: base.Add(2 * time.Minute)}}, Page: 1, PerPage: 100},
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
	if client.commentCalls != 0 {
		t.Fatalf("parent ListIssueComments calls = %d, want 0 after changed list revision", client.commentCalls)
	}
	if second.SuccessCount != 1 || len(second.Results) != 1 {
		t.Fatalf("second result count = %d/%d, want 1/1", second.SuccessCount, len(second.Results))
	}
	if second.Results[0].Counts.FetchedDetail != 0 || second.Results[0].Counts.SkippedByRevision != 0 || second.Results[0].Counts.Deferred != 1 {
		t.Fatalf("second counts = %#v, want changed parent plus queued comments", second.Results[0].Counts)
	}
	if _, err := svc.BulkSyncIssueComments(ctx, BulkSyncRequest{RepoID: "bulk-issues-comment-revision", PerPage: 100}); err != nil {
		t.Fatalf("BulkSyncIssueComments returned error: %v", err)
	}
	if client.commentCalls != 1 {
		t.Fatalf("ListIssueComments calls after explicit drain = %d, want 1", client.commentCalls)
	}
	record, err := store.GetRecord(ctx, "bulk-issues-comment-revision", "ISSUE-11")
	if err != nil {
		t.Fatalf("ISSUE-11 record missing: %v", err)
	}
	if len(record.Comments) != 1 || record.Comments[0].CommentID != "c11" {
		t.Fatalf("comments=%+v, want c11", record.Comments)
	}
	if _, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{RepoID: "bulk-issues-comment-revision", IdempotencyKey: "issues-third", PerPage: 100}); err != nil {
		t.Fatalf("third BulkSyncIssues returned error: %v", err)
	}
	if client.commentCalls != 1 {
		t.Fatalf("ListIssueComments calls after zero-comment parent revision = %d, want 1", client.commentCalls)
	}
	record, err = store.GetRecord(ctx, "bulk-issues-comment-revision", "ISSUE-11")
	if err != nil {
		t.Fatalf("ISSUE-11 record missing after zero-comment revision: %v", err)
	}
	if len(record.Comments) != 0 {
		t.Fatalf("comments=%+v, want exact empty coverage after list count reached zero", record.Comments)
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
	if inline.ID != "D7" || !inline.Replyable || inline.ReplyDiscussionID != "D7" || inline.ReplyUnavailableReason != "" || inline.Kind != "inline" || inline.Path != "internal/service/service.go" || inline.Line != 42 || len(inline.Comments) != 2 {
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
	if general.ID != "comment:303" || general.Replyable || general.ReplyUnavailableReason == "" || general.Kind != "general" || len(general.Comments) != 1 || general.Comments[0].Author != "carol" {
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

func TestListPRDiscussionsKeepsV5OnlyNestedRepliesInOneSyntheticThread(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	client := &fakeGitCodeClient{
		listPRPages: []gitcode.Page[gitcode.PullRequest]{{Items: []gitcode.PullRequest{{ID: "9001", Number: 7, Title: "Review target", State: "open", CreatedAt: base, UpdatedAt: base}}}},
		prCommentsByPR: map[int][]gitcode.PRComment{7: {
			{ID: "301", Body: "root", ReviewKind: "inline", Path: "x.go", Line: 7, PRNumber: 7, CreatedAt: base, UpdatedAt: base},
			{ID: "302", Body: "nested", ReviewKind: "inline", ParentID: "301", Path: "x.go", Line: 7, PRNumber: 7, CreatedAt: base.Add(time.Minute), UpdatedAt: base.Add(time.Minute)},
		}},
	}
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "v5-only", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)
	if _, err := svc.BulkSyncPullRequests(ctx, BulkSyncRequest{RepoID: "v5-only"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.BulkSyncPRComments(ctx, BulkSyncRequest{RepoID: "v5-only"}); err != nil {
		t.Fatal(err)
	}
	got, err := svc.ListPRDiscussions(ctx, PRDiscussionRequest{RepoID: "v5-only", Number: 7})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Discussions) != 1 || got.Discussions[0].ID != "comment:301" || got.Discussions[0].Replyable || len(got.Discussions[0].Comments) != 2 {
		t.Fatalf("discussions=%+v, want one non-replyable synthetic thread", got.Discussions)
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
	if result.PagesListed != 0 || result.RecordsListed != 0 {
		t.Fatalf("list stats = pages %d records %d, want 0/0", result.PagesListed, result.RecordsListed)
	}
	var failure ErrSyncFailure
	if !errors.As(result.Failures[0].Err, &failure) || failure.Mode != "rate_limited" {
		t.Fatalf("failure error = %T %v, want rate_limited ErrSyncFailure", result.Failures[0].Err, result.Failures[0].Err)
	}
}

func TestBulkSyncIssuesCachesRecoverablePrefixAndKeepsPartialFrontier(t *testing.T) {
	ctx := context.Background()
	base := time.Date(2026, 8, 18, 13, 0, 0, 0, time.UTC)
	decodeErr := gitcode.ErrPartialResponse{Endpoint: "/issues", Got: 512, Offset: 511, Attempts: 2, Message: "truncated JSON"}
	client := &fakeGitCodeClient{
		listIssuesErrors: []error{decodeErr},
		listIssuesErrorPages: []gitcode.Page[gitcode.IssueSummary]{{Items: []gitcode.IssueSummary{
			{ID: "1", Number: 1, Title: "First recovered", Body: "first", State: "open", CreatedAt: base, UpdatedAt: base},
			{ID: "2", Number: 2, Title: "Second recovered", Body: "second", State: "open", CreatedAt: base.Add(-time.Minute), UpdatedAt: base.Add(-time.Minute)},
		}, Page: 1, PerPage: 10}},
	}
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	const repoID = "bulk-partial-prefix"
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: repoID, Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	svc := NewWithClient(store, client)
	result, err := svc.BulkSyncIssues(ctx, BulkSyncRequest{RepoID: repoID, PerPage: 10, Bounds: &SyncBounds{MaxPages: 5}})
	var partial *PartialSyncError
	if !errors.As(err, &partial) {
		t.Fatalf("result=%+v err=%T %v, want typed partial", result, err, err)
	}
	if result.SuccessCount != 2 || result.FailureCount != 1 || len(result.Results) != 2 || len(result.Failures) != 1 {
		t.Fatalf("result=%+v, want 2 successes plus collection failure", result)
	}
	if result.Failures[0].FailureClass != "partial_response" || result.Failures[0].ResponseBytes != 512 || result.Failures[0].DecodeOffset != 511 || result.Failures[0].Attempts != 2 {
		t.Fatalf("failure=%+v", result.Failures[0])
	}
	for _, id := range []string{"ISSUE-1", "ISSUE-2"} {
		if _, err := store.GetSourceScoped(ctx, repoID, id); err != nil {
			t.Fatalf("recovered %s missing: %v", id, err)
		}
	}
	frontier, ok, err := store.GetSyncFrontier(ctx, repoID, "issue", syncOrderingUpdatedAtDesc, syncFilterStateAll)
	if err != nil || !ok || frontier.Status == "complete" {
		t.Fatalf("frontier=%+v ok=%t err=%v, want non-complete retryable frontier", frontier, ok, err)
	}
}
