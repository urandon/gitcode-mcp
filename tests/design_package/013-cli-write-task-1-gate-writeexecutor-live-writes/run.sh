#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
WORKDIR="${TMPDIR:-/tmp}/gitcode-mcp-validation-013-$$"
cleanup() {
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

mkdir -p "$WORKDIR"
rsync -a --exclude '.git' --exclude 'ai/artifacts' "$ROOT/" "$WORKDIR/"

cat > "$WORKDIR/internal/service/write_design_validation_test.go" <<'GOEOF'
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

type validationWriteClient struct {
	createIssueCalls int
	updateIssueCalls int
	commentCalls     int
	createWikiCalls  int
	updateWikiCalls  int
	addLabelCalls    int
	err              error
	issueResult      gitcode.WriteResult[gitcode.Issue]
	commentResult    gitcode.WriteResult[gitcode.Comment]
	wikiResult       gitcode.WriteResult[gitcode.WikiPage]
}

func (c *validationWriteClient) ListIssues(context.Context, gitcode.IssueListRequest) (gitcode.Page[gitcode.IssueSummary], error) { return gitcode.Page[gitcode.IssueSummary]{}, nil }
func (c *validationWriteClient) GetIssue(context.Context, gitcode.IssueRequest) (gitcode.Issue, error) { return gitcode.Issue{}, nil }
func (c *validationWriteClient) ListIssueComments(context.Context, gitcode.IssueRequest) (gitcode.Page[gitcode.Comment], error) { return gitcode.Page[gitcode.Comment]{}, nil }
func (c *validationWriteClient) GetWikiPage(context.Context, gitcode.WikiPageRequest) (gitcode.WikiPage, error) { return gitcode.WikiPage{}, nil }
func (c *validationWriteClient) ListWikiPages(context.Context, gitcode.WikiListRequest) (gitcode.Page[gitcode.WikiPage], error) { return gitcode.Page[gitcode.WikiPage]{}, nil }
func (c *validationWriteClient) Search(context.Context, gitcode.SearchRequest) (gitcode.Page[gitcode.SearchResult], error) { return gitcode.Page[gitcode.SearchResult]{}, nil }
func (c *validationWriteClient) ListIssueAttachments(context.Context, gitcode.IssueRequest) (gitcode.Page[gitcode.AttachmentSummary], error) { return gitcode.Page[gitcode.AttachmentSummary]{}, nil }
func (c *validationWriteClient) GetAttachment(context.Context, gitcode.AttachmentRequest) (gitcode.AttachmentBody, error) { return gitcode.AttachmentBody{}, nil }
func (c *validationWriteClient) CreateIssue(context.Context, gitcode.CreateIssueRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	c.createIssueCalls++
	if c.err != nil { return gitcode.WriteResult[gitcode.Issue]{}, c.err }
	return c.issueResult, nil
}
func (c *validationWriteClient) UpdateIssue(context.Context, gitcode.UpdateIssueRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	c.updateIssueCalls++
	if c.err != nil { return gitcode.WriteResult[gitcode.Issue]{}, c.err }
	return c.issueResult, nil
}
func (c *validationWriteClient) CreateIssueComment(context.Context, gitcode.CreateIssueCommentRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Comment], error) {
	c.commentCalls++
	if c.err != nil { return gitcode.WriteResult[gitcode.Comment]{}, c.err }
	return c.commentResult, nil
}
func (c *validationWriteClient) CreateWikiPage(context.Context, gitcode.CreateWikiPageRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.WikiPage], error) {
	c.createWikiCalls++
	if c.err != nil { return gitcode.WriteResult[gitcode.WikiPage]{}, c.err }
	return c.wikiResult, nil
}
func (c *validationWriteClient) UpdateWikiPage(context.Context, gitcode.UpdateWikiPageRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.WikiPage], error) {
	c.updateWikiCalls++
	if c.err != nil { return gitcode.WriteResult[gitcode.WikiPage]{}, c.err }
	return c.wikiResult, nil
}
func (c *validationWriteClient) AddLabel(context.Context, gitcode.LabelRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) { c.addLabelCalls++; return gitcode.WriteResult[gitcode.Issue]{}, nil }
func (c *validationWriteClient) RemoveLabel(context.Context, gitcode.LabelRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) { return gitcode.WriteResult[gitcode.Issue]{}, nil }

type failingWriteStore struct {
	cache.Store
	failAudit bool
	failNextRefresh bool
}

func (s *failingWriteStore) RecordAuditEvent(ctx context.Context, entry cache.AuditTrailEntry) error {
	if s.failAudit { return errors.New("injected audit failure") }
	return s.Store.RecordAuditEvent(ctx, entry)
}

func (s *failingWriteStore) UpsertRecordGraph(ctx context.Context, graph cache.RecordGraph) error {
	if s.failNextRefresh {
		s.failNextRefresh = false
		return errors.New("injected cache refresh failure")
	}
	return s.Store.UpsertRecordGraph(ctx, graph)
}

func validationStore(t *testing.T, ctx context.Context) *cache.SQLiteStore {
	t.Helper()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { _ = store.Close() })
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner-a", Name: "repo-a", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil { t.Fatal(err) }
	return store
}

func confirmedIssue(number int) gitcode.WriteResult[gitcode.Issue] {
	now := time.Date(2026, 6, 20, 12, 0, 0, 0, time.UTC)
	return gitcode.WriteResult[gitcode.Issue]{Record: gitcode.Issue{ID: fmt.Sprintf("remote-%d", number), Number: number, Title: "T", Body: "B", State: "open", CreatedAt: now, UpdatedAt: now}, Confirmed: true, Operation: "CreateIssue", RemoteID: fmt.Sprintf("%d", number), RemoteNumber: number, RemoteRevision: fmt.Sprintf("rev-%d", number), ConfirmedAt: now}
}

func TestDesign013DryRunNoMutation(t *testing.T) {
	ctx := context.Background()
	store := validationStore(t, ctx)
	client := &validationWriteClient{issueResult: confirmedIssue(42)}
	result, err := NewWithClient(store, client).CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeDryRun, Title: "T", Body: "B"})
	if err != nil { t.Fatal(err) }
	if result.Status != "dry_run_valid" || result.RepoID != "fixture-a" || result.IdempotencyKey == "" { t.Fatalf("bad dry-run result: %#v", result) }
	if client.createIssueCalls != 0 { t.Fatalf("provider calls=%d want 0", client.createIssueCalls) }
	counts, err := store.RecordCounts(ctx, "fixture-a")
	if err != nil { t.Fatal(err) }
	if counts.AuditRows != 0 || counts.Records != 0 { t.Fatalf("dry-run mutated cache: %#v", counts) }
}

func TestDesign013LiveSuccessAndIdempotentReplay(t *testing.T) {
	t.Setenv("GITCODE_TOKEN", "token")
	ctx := context.Background()
	store := validationStore(t, ctx)
	client := &validationWriteClient{issueResult: confirmedIssue(42)}
	svc := NewWithClient(store, client)
	req := WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "T", Body: "B", IdempotencyKey: "design-key-success"}
	result, err := svc.CreateIssue(ctx, req)
	if err != nil { t.Fatal(err) }
	if result.Status != "succeeded" || result.RemoteID != "42" || result.ID != "ISSUE-42" { t.Fatalf("bad live result: %#v", result) }
	counts, err := store.RecordCounts(ctx, "fixture-a")
	if err != nil { t.Fatal(err) }
	if counts.AuditRows != 1 || counts.Records != 1 { t.Fatalf("missing audit/cache state: %#v", counts) }
	replay, err := svc.CreateIssue(ctx, req)
	if err != nil { t.Fatal(err) }
	if !replay.Replayed || replay.Status != "succeeded" || client.createIssueCalls != 1 { t.Fatalf("bad replay=%#v calls=%d", replay, client.createIssueCalls) }
	countsAfter, err := store.RecordCounts(ctx, "fixture-a")
	if err != nil { t.Fatal(err) }
	if countsAfter != counts { t.Fatalf("replay changed cache/audit state before=%#v after=%#v", counts, countsAfter) }
}

func TestDesign013MissingTokenAndProviderErrors(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct{ name string; err error; code string }{
		{"unavailable", gitcode.ErrProviderUnavailable{Reason: "stub unavailable"}, "write_provider_error"},
		{"conflict", gitcode.ErrConflict{Endpoint: "/issues", Message: "conflict"}, "write_conflict"},
		{"unauthorized", gitcode.ErrAuthExpired{Endpoint: "/issues"}, "write_unauthorized"},
		{"rate-limited", gitcode.ErrRateLimited{Endpoint: "/issues", Attempts: 1, RetryAfter: time.Second}, "write_rate_limited"},
		{"network", gitcode.ErrNetworkUnavailable{Endpoint: "/issues", Attempts: 1}, "write_network_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GITCODE_TOKEN", "token")
			store := validationStore(t, ctx)
			client := &validationWriteClient{err: tc.err}
			_, err := NewWithClient(store, client).CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "T", Body: "B", IdempotencyKey: "key-" + tc.name})
			var writeErr ErrWriteFailure
			if !errors.As(err, &writeErr) || writeErr.Code != tc.code { t.Fatalf("err=%v want code %s", err, tc.code) }
			counts, err := store.RecordCounts(ctx, "fixture-a")
			if err != nil { t.Fatal(err) }
			if counts.AuditRows != 0 || counts.Records != 0 { t.Fatalf("provider failure persisted success state: %#v", counts) }
		})
	}
	store := validationStore(t, ctx)
	client := &validationWriteClient{issueResult: confirmedIssue(43)}
	_, err := NewWithClient(store, client).CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "T", Body: "B", IdempotencyKey: "missing-token"})
	var writeErr ErrWriteFailure
	if !errors.As(err, &writeErr) || writeErr.Code != "write_missing_credential" { t.Fatalf("missing token err=%v", err) }
	if client.createIssueCalls != 0 { t.Fatalf("missing token called provider %d time(s)", client.createIssueCalls) }
}

func TestDesign013PartialAuditFailure(t *testing.T) {
	t.Setenv("GITCODE_TOKEN", "token")
	ctx := context.Background()
	base := validationStore(t, ctx)
	store := &failingWriteStore{Store: base, failAudit: true}
	client := &validationWriteClient{issueResult: confirmedIssue(44)}
	_, err := NewWithClient(store, client).CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "T", Body: "B", IdempotencyKey: "partial-audit"})
	var writeErr ErrWriteFailure
	if !errors.As(err, &writeErr) || writeErr.Code != "write_partial_remote_confirmed_audit_failed" || writeErr.RemoteID != "44" || writeErr.IdempotencyKey != "partial-audit" { t.Fatalf("partial audit err=%v", err) }
	counts, err := base.RecordCounts(ctx, "fixture-a")
	if err != nil { t.Fatal(err) }
	if counts.AuditRows != 0 || counts.Records != 0 { t.Fatalf("audit failure wrote success state: %#v", counts) }
	_, retryErr := NewWithClient(store, client).CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "T", Body: "B", IdempotencyKey: "partial-audit"})
	if !errors.As(retryErr, &writeErr) || writeErr.Code != "write_partial_remote_confirmed_audit_failed" { t.Fatalf("retry did not stay on partial reconciliation path: %v", retryErr) }
}

func TestDesign013PartialCacheRefreshRetryDoesNotMutateAgain(t *testing.T) {
	t.Setenv("GITCODE_TOKEN", "token")
	ctx := context.Background()
	base := validationStore(t, ctx)
	store := &failingWriteStore{Store: base, failNextRefresh: true}
	client := &validationWriteClient{issueResult: confirmedIssue(45)}
	svc := NewWithClient(store, client)
	req := WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "T", Body: "B", IdempotencyKey: "partial-cache"}
	_, err := svc.CreateIssue(ctx, req)
	var writeErr ErrWriteFailure
	if !errors.As(err, &writeErr) || writeErr.Code != "write_partial_cache_refresh_failed" { t.Fatalf("partial cache err=%v", err) }
	entry, err := base.GetAuditEventByKey(ctx, "fixture-a", "partial-cache")
	if err != nil { t.Fatal(err) }
	if entry == nil || entry.Status != "remote_confirmed_cache_refresh_failed" { t.Fatalf("missing partial cache audit entry: %#v", entry) }
	result, err := svc.CreateIssue(ctx, req)
	if err != nil { t.Fatalf("retry should refresh cache without second mutation: %v", err) }
	if result.Status != "succeeded" || client.createIssueCalls != 1 { t.Fatalf("retry mutated again or did not succeed: result=%#v calls=%d", result, client.createIssueCalls) }
	counts, err := base.RecordCounts(ctx, "fixture-a")
	if err != nil { t.Fatal(err) }
	if counts.Records != 1 { t.Fatalf("retry did not refresh record: %#v", counts) }
}

func TestDesign013AddLabelUnsupported(t *testing.T) {
	t.Setenv("GITCODE_TOKEN", "token")
	ctx := context.Background()
	store := validationStore(t, ctx)
	client := &validationWriteClient{issueResult: confirmedIssue(46)}
	_, err := NewWithClient(store, client).AddLabel(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Number: 1, Label: "bug"})
	var writeErr ErrWriteFailure
	if !errors.As(err, &writeErr) || writeErr.Code != "write_unsupported_deferred" { t.Fatalf("add-label err=%v", err) }
	if client.addLabelCalls != 0 { t.Fatalf("AddLabel provider calls=%d want 0", client.addLabelCalls) }
}
GOEOF

cat > "$WORKDIR/internal/cli/write_design_validation_test.go" <<'GOEOF'
package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestDesign013WriteValidationGatesBeforeDispatch(t *testing.T) {
	commands := [][]string{
		{"update-issue", "--number", "1", "--live", "--format", "json"},
		{"add-comment", "--number", "1", "--body", "B", "--live", "--format", "json"},
		{"create-page", "--title", "T", "--body", "B", "--live", "--format", "json"},
		{"update-page", "--slug", "Home", "--body", "B", "--live", "--format", "json"},
	}
	for _, args := range commands {
		spy := &spyService{}
		factory := func(context.Context, string) (queryService, func() error, error) { return spy, nil, nil }
		var stdout, stderr bytes.Buffer
		if code := executeWithFactory(args, &stdout, &stderr, factory); code == 0 || !strings.Contains(stderr.String(), "repo_required") {
			t.Fatalf("missing repo args=%v code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
		if len(spy.calls) != 0 { t.Fatalf("missing repo dispatched service for %v: %#v", args, spy.calls) }
	}
	for _, args := range [][]string{
		{"update-issue", "--repo", "fixture-a", "--number", "1", "--format", "json"},
		{"add-comment", "--repo", "fixture-a", "--number", "1", "--body", "B", "--format", "json"},
		{"create-page", "--repo", "fixture-a", "--title", "T", "--body", "B", "--format", "json"},
		{"update-page", "--repo", "fixture-a", "--slug", "Home", "--body", "B", "--format", "json"},
		{"update-issue", "--repo", "fixture-a", "--number", "1", "--dry-run", "--live", "--format", "json"},
		{"add-comment", "--repo", "fixture-a", "--number", "1", "--body", "B", "--dry-run", "--live", "--format", "json"},
		{"create-page", "--repo", "fixture-a", "--title", "T", "--body", "B", "--dry-run", "--live", "--format", "json"},
		{"update-page", "--repo", "fixture-a", "--slug", "Home", "--body", "B", "--dry-run", "--live", "--format", "json"},
		{"update-issue", "--repo", "fixture-a", "--owner", "owner", "--name", "repo", "--number", "1", "--live", "--format", "json"},
		{"add-comment", "--repo", "fixture-a", "--owner", "owner", "--name", "repo", "--number", "1", "--body", "B", "--live", "--format", "json"},
		{"create-page", "--repo", "fixture-a", "--owner", "owner", "--name", "repo", "--title", "T", "--body", "B", "--live", "--format", "json"},
		{"update-page", "--repo", "fixture-a", "--owner", "owner", "--name", "repo", "--slug", "Home", "--body", "B", "--live", "--format", "json"},
	} {
		spy := &spyService{}
		factory := func(context.Context, string) (queryService, func() error, error) { return spy, nil, nil }
		var stdout, stderr bytes.Buffer
		if code := executeWithFactory(args, &stdout, &stderr, factory); code == 0 || !strings.Contains(stderr.String(), "invalid_query") {
			t.Fatalf("validation args=%v code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
		if len(spy.calls) != 0 { t.Fatalf("validation dispatched service for %v: %#v", args, spy.calls) }
	}
}
GOEOF

cd "$WORKDIR"
go test ./internal/service ./internal/cli -run 'TestDesign013' -count=1
