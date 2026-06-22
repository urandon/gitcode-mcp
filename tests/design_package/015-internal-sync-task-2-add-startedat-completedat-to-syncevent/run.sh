#!/usr/bin/env bash
set -euo pipefail

# run.sh — Validation script for task 015: Add StartedAt/CompletedAt to SyncEvent
# Runs the production tests in internal/service/ and internal/cache/.

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../../.." && pwd)"

FAILED=0

echo "=== Scenario 1: SyncEvent timestamps on successful SyncToCache ==="
echo "Running: go test -run TestSyncEventTimestamps -count=1 ./internal/service/"
if go test -run TestSyncEventTimestamps -count=1 -v "$REPO_ROOT/internal/service/" 2>&1; then
  echo "PASS: TestSyncEventTimestamps passed"
else
  echo "FAIL: TestSyncEventTimestamps failed"
  FAILED=1
fi

echo ""
echo "=== Scenario 1b: SyncEvent timestamps on failed sync ==="
echo "Running: go test -run TestSyncEventTimestampsFailure -count=1 ./internal/service/"
if go test -run TestSyncEventTimestampsFailure -count=1 -v "$REPO_ROOT/internal/service/" 2>&1; then
  echo "PASS: TestSyncEventTimestampsFailure passed"
else
  echo "FAIL: TestSyncEventTimestampsFailure failed"
  FAILED=1
fi

echo ""
echo "=== Scenario 2: SyncStatusSummary excludes zero-CompletedAt events ==="
echo "Running: go test -run TestSyncStatusSummaryCompletedAt -count=1 ./internal/service/"
if go test -run TestSyncStatusSummaryCompletedAt -count=1 -v "$REPO_ROOT/internal/service/" 2>&1; then
  echo "PASS: TestSyncStatusSummaryCompletedAt passed"
else
  echo "FAIL: TestSyncStatusSummaryCompletedAt failed"
  FAILED=1
fi

echo ""
echo "=== Scenario 3: Cache-level RecordSyncEvent timestamp roundtrip ==="
echo "Running: go test -run TestRecordSyncEventTimestamps -count=1 ./internal/cache/"
if go test -run TestRecordSyncEventTimestamps -count=1 -v "$REPO_ROOT/internal/cache/" 2>&1; then
  echo "PASS: TestRecordSyncEventTimestamps passed"
else
  echo "FAIL: TestRecordSyncEventTimestamps failed"
  FAILED=1
fi

echo ""
echo "=== Offline determinism check ==="
echo "Running: go test ./... (all packages, no live env vars)"
if go test "$REPO_ROOT/..." 2>&1; then
  echo "PASS: Full test suite passes offline"
else
  echo "FAIL: Full test suite failed"
  FAILED=1
fi

echo ""
echo "=== Extended strict validation: SyncResult.StartedAt/CompletedAt on replayed event ==="
cat > "$DIR"/verify_replay.go <<'GOEOF'
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/service"
)

type replayClient struct {
	issue    gitcode.Issue
	comments []gitcode.Comment
}

func (f *replayClient) ListIssues(context.Context, gitcode.IssueListRequest) (gitcode.Page[gitcode.IssueSummary], error) {
	return gitcode.Page[gitcode.IssueSummary]{}, nil
}
func (f *replayClient) GetIssue(context.Context, gitcode.IssueRequest) (gitcode.Issue, error) {
	return f.issue, nil
}
func (f *replayClient) ListIssueComments(context.Context, gitcode.IssueRequest) (gitcode.Page[gitcode.Comment], error) {
	return gitcode.Page[gitcode.Comment]{Items: f.comments}, nil
}
func (f *replayClient) GetWikiPage(context.Context, gitcode.WikiPageRequest) (gitcode.WikiPage, error) {
	return gitcode.WikiPage{}, gitcode.ErrNotFound{Endpoint: "/wiki", ID: "Home", Message: "not found"}
}
func (f *replayClient) ListWikiPages(context.Context, gitcode.WikiListRequest) (gitcode.Page[gitcode.WikiPage], error) {
	return gitcode.Page[gitcode.WikiPage]{}, nil
}
func (f *replayClient) Search(context.Context, gitcode.SearchRequest) (gitcode.Page[gitcode.SearchResult], error) {
	return gitcode.Page[gitcode.SearchResult]{}, nil
}
func (f *replayClient) ListIssueAttachments(context.Context, gitcode.IssueRequest) (gitcode.Page[gitcode.AttachmentSummary], error) {
	return gitcode.Page[gitcode.AttachmentSummary]{}, nil
}
func (f *replayClient) GetAttachment(context.Context, gitcode.AttachmentRequest) (gitcode.AttachmentBody, error) {
	return gitcode.AttachmentBody{}, fmt.Errorf("not implemented")
}
func (f *replayClient) CreateIssue(context.Context, gitcode.CreateIssueRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	return gitcode.WriteResult[gitcode.Issue]{}, fmt.Errorf("not implemented")
}
func (f *replayClient) UpdateIssue(context.Context, gitcode.UpdateIssueRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	return gitcode.WriteResult[gitcode.Issue]{}, fmt.Errorf("not implemented")
}
func (f *replayClient) CreateIssueComment(context.Context, gitcode.CreateIssueCommentRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Comment], error) {
	return gitcode.WriteResult[gitcode.Comment]{}, fmt.Errorf("not implemented")
}
func (f *replayClient) CreateWikiPage(context.Context, gitcode.CreateWikiPageRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.WikiPage], error) {
	return gitcode.WriteResult[gitcode.WikiPage]{}, fmt.Errorf("not implemented")
}
func (f *replayClient) UpdateWikiPage(context.Context, gitcode.UpdateWikiPageRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.WikiPage], error) {
	return gitcode.WriteResult[gitcode.WikiPage]{}, fmt.Errorf("not implemented")
}
func (f *replayClient) AddLabel(context.Context, gitcode.LabelRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	return gitcode.WriteResult[gitcode.Issue]{}, fmt.Errorf("not implemented")
}
func (f *replayClient) RemoveLabel(context.Context, gitcode.LabelRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	return gitcode.WriteResult[gitcode.Issue]{}, fmt.Errorf("not implemented")
}

func main() {
	ctx := context.Background()
	base := time.Date(2026, 6, 22, 14, 0, 0, 0, time.UTC)

	client := &replayClient{
		issue:    gitcode.Issue{Number: 42, Title: "Replay Issue", Body: "body", State: "open", CreatedAt: base, UpdatedAt: base},
		comments: []gitcode.Comment{{ID: "c1", Author: "author", Body: "comment", CreatedAt: base, UpdatedAt: base}},
	}

	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	if err := store.AddRepository(ctx, cache.RepositoryBinding{
		RepoID: "replay-test", Owner: "o", Name: "r", APIBaseURL: "https://example.invalid/api",
		Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues},
	}); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		os.Exit(1)
	}

	svc := service.NewWithClient(store, client)

	result1, err := svc.SyncToCache(ctx, service.SyncRequest{RepoID: "replay-test", RemoteAlias: "issue:42", IdempotencyKey: "replay-key"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: first SyncToCache returned error: %v\n", err)
		os.Exit(1)
	}
	if result1.StartedAt.IsZero() {
		fmt.Fprintln(os.Stderr, "FAIL: first sync StartedAt is zero")
		os.Exit(1)
	}
	if result1.CompletedAt.IsZero() {
		fmt.Fprintln(os.Stderr, "FAIL: first sync CompletedAt is zero")
		os.Exit(1)
	}
	if !result1.CompletedAt.After(result1.StartedAt) {
		fmt.Fprintf(os.Stderr, "FAIL: first sync CompletedAt %s not after StartedAt %s\n", result1.CompletedAt, result1.StartedAt)
		os.Exit(1)
	}

	result2, err := svc.SyncToCache(ctx, service.SyncRequest{RepoID: "replay-test", RemoteAlias: "issue:42", IdempotencyKey: "replay-key"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: second (replay) SyncToCache returned error: %v\n", err)
		os.Exit(1)
	}
	if !result2.Replayed {
		fmt.Fprintln(os.Stderr, "FAIL: second sync Replayed is not true")
		os.Exit(1)
	}
	if result2.StartedAt.IsZero() {
		fmt.Fprintln(os.Stderr, "FAIL: replayed sync StartedAt is zero")
		os.Exit(1)
	}
	if result2.CompletedAt.IsZero() {
		fmt.Fprintln(os.Stderr, "FAIL: replayed sync CompletedAt is zero")
		os.Exit(1)
	}

	fmt.Println("PASS: All replay strict assertions verified")
}
GOEOF
cd "$REPO_ROOT"
REPLAY_OUTPUT=$(go run "$DIR"/verify_replay.go 2>&1) || REPLAY_EXIT=$?
rm -f "$DIR"/verify_replay.go
if [ "${REPLAY_EXIT:-0}" -ne 0 ]; then
  echo "FAIL: Replay strict validations failed:"
  echo "$REPLAY_OUTPUT"
  FAILED=1
else
  echo "PASS: Replay strict validations passed"
fi

if [ "$FAILED" -eq 0 ]; then
  echo ""
  echo "=== ALL VALIDATIONS PASSED ==="
  exit 0
else
  echo ""
  echo "=== SOME VALIDATIONS FAILED ==="
  exit 1
fi
