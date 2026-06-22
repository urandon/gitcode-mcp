#!/usr/bin/env bash
set -euo pipefail

# run.sh — Validation script for task 014: Add SyncResources for partial-failure collection
# Runs the production tests in internal/service/ and adds strict external validation
# for Failures[0].SourceID that the existing test misses.

DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$DIR/../../.." && pwd)"

FAILED=0

echo "=== Scenario 1: Partial-failure test ==="
echo "Running: go test -run TestSyncResourcesPartialFailure -count=1 ./internal/service/"
if go test -run TestSyncResourcesPartialFailure -count=1 -v "$REPO_ROOT/internal/service/" 2>&1; then
  echo "PASS: TestSyncResourcesPartialFailure passed"
else
  echo "FAIL: TestSyncResourcesPartialFailure failed"
  FAILED=1
fi

echo ""
echo "=== Scenario 1: Extended strict validation (Failures[0].SourceID) ==="
cat > "$DIR"/verify_strict.go <<'GOEOF'
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/service"
)

type fakeStrictClient struct {
	issue    gitcode.Issue
	comments []gitcode.Comment
	wikiErr  error
}

func (f *fakeStrictClient) ListIssues(context.Context, gitcode.IssueListRequest) (gitcode.Page[gitcode.IssueSummary], error) {
	return gitcode.Page[gitcode.IssueSummary]{}, nil
}
func (f *fakeStrictClient) GetIssue(context.Context, gitcode.IssueRequest) (gitcode.Issue, error) {
	return f.issue, nil
}
func (f *fakeStrictClient) ListIssueComments(context.Context, gitcode.IssueRequest) (gitcode.Page[gitcode.Comment], error) {
	return gitcode.Page[gitcode.Comment]{Items: f.comments}, nil
}
func (f *fakeStrictClient) GetWikiPage(context.Context, gitcode.WikiPageRequest) (gitcode.WikiPage, error) {
	return gitcode.WikiPage{}, f.wikiErr
}
func (f *fakeStrictClient) ListWikiPages(context.Context, gitcode.WikiListRequest) (gitcode.Page[gitcode.WikiPage], error) {
	return gitcode.Page[gitcode.WikiPage]{}, nil
}
func (f *fakeStrictClient) Search(context.Context, gitcode.SearchRequest) (gitcode.Page[gitcode.SearchResult], error) {
	return gitcode.Page[gitcode.SearchResult]{}, nil
}
func (f *fakeStrictClient) ListIssueAttachments(context.Context, gitcode.IssueRequest) (gitcode.Page[gitcode.AttachmentSummary], error) {
	return gitcode.Page[gitcode.AttachmentSummary]{}, nil
}
func (f *fakeStrictClient) GetAttachment(context.Context, gitcode.AttachmentRequest) (gitcode.AttachmentBody, error) {
	return gitcode.AttachmentBody{}, errors.New("not implemented")
}
func (f *fakeStrictClient) CreateIssue(context.Context, gitcode.CreateIssueRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	return gitcode.WriteResult[gitcode.Issue]{}, errors.New("not implemented")
}
func (f *fakeStrictClient) UpdateIssue(context.Context, gitcode.UpdateIssueRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	return gitcode.WriteResult[gitcode.Issue]{}, errors.New("not implemented")
}
func (f *fakeStrictClient) CreateIssueComment(context.Context, gitcode.CreateIssueCommentRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Comment], error) {
	return gitcode.WriteResult[gitcode.Comment]{}, errors.New("not implemented")
}
func (f *fakeStrictClient) CreateWikiPage(context.Context, gitcode.CreateWikiPageRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.WikiPage], error) {
	return gitcode.WriteResult[gitcode.WikiPage]{}, errors.New("not implemented")
}
func (f *fakeStrictClient) UpdateWikiPage(context.Context, gitcode.UpdateWikiPageRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.WikiPage], error) {
	return gitcode.WriteResult[gitcode.WikiPage]{}, errors.New("not implemented")
}
func (f *fakeStrictClient) AddLabel(context.Context, gitcode.LabelRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	return gitcode.WriteResult[gitcode.Issue]{}, errors.New("not implemented")
}
func (f *fakeStrictClient) RemoveLabel(context.Context, gitcode.LabelRequest, gitcode.WriteOptions) (gitcode.WriteResult[gitcode.Issue], error) {
	return gitcode.WriteResult[gitcode.Issue]{}, errors.New("not implemented")
}

func main() {
	ctx := context.Background()
	base := time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC)

	client := &fakeStrictClient{
		issue:    gitcode.Issue{Number: 42, Title: "Test Issue", Body: "issue body", State: "open", CreatedAt: base, UpdatedAt: base},
		comments: []gitcode.Comment{{ID: "c1", Author: "author", Body: "comment", CreatedAt: base, UpdatedAt: base}},
		wikiErr:  gitcode.ErrNotFound{Endpoint: "/wiki", ID: "Home", Message: "not found"},
	}

	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		os.Exit(1)
	}
	defer store.Close()

	if err := store.AddRepository(ctx, cache.RepositoryBinding{
		RepoID: "strict-test", Owner: "o", Name: "r", APIBaseURL: "https://example.invalid/api",
		Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki},
	}); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: %v\n", err)
		os.Exit(1)
	}

	svc := service.NewWithClient(store, client)
	reqs := []service.SyncRequest{
		{RepoID: "strict-test", RemoteAlias: "issue:42", IdempotencyKey: "strict-issue"},
		{RepoID: "strict-test", RemoteAlias: "wiki:Home", IdempotencyKey: "strict-wiki"},
	}

	result, err := svc.SyncResources(ctx, reqs)
	if err == nil {
		fmt.Fprintln(os.Stderr, "FAIL: expected PartialSyncError, got nil")
		os.Exit(1)
	}

	var partial *service.PartialSyncError
	if !errors.As(err, &partial) {
		fmt.Fprintf(os.Stderr, "FAIL: error is not *PartialSyncError: %T %v\n", err, err)
		os.Exit(1)
	}

	failures := 0
	if result.SuccessCount != 1 {
		fmt.Fprintf(os.Stderr, "FAIL: SuccessCount = %d, want 1\n", result.SuccessCount)
		failures++
	}
	if result.FailureCount != 1 {
		fmt.Fprintf(os.Stderr, "FAIL: FailureCount = %d, want 1\n", result.FailureCount)
		failures++
	}
	if len(result.Failures) != 1 {
		fmt.Fprintf(os.Stderr, "FAIL: len(Failures) = %d, want 1\n", len(result.Failures))
		failures++
	}
	if len(result.Results) != 1 {
		fmt.Fprintf(os.Stderr, "FAIL: len(Results) = %d, want 1\n", len(result.Results))
		failures++
	}
	if result.Results[0].Counts.Fetched <= 0 {
		fmt.Fprintf(os.Stderr, "FAIL: Results[0].Counts.Fetched = %d, want > 0\n", result.Results[0].Counts.Fetched)
		failures++
	}

	// Strict check: Failures[0].SourceID must be non-empty and match the failed source
	if result.Failures[0].SourceID == "" {
		fmt.Fprintln(os.Stderr, "FAIL: Failures[0].SourceID is empty, must match the failed source")
		failures++
	} else if !strings.Contains(result.Failures[0].SourceID, "wiki") && !strings.Contains(result.Failures[0].SourceID, "Home") {
		fmt.Fprintf(os.Stderr, "FAIL: Failures[0].SourceID = %q, expected it to reference the wiki source\n", result.Failures[0].SourceID)
		failures++
	}

	// Check PartialSyncError error string contains success and failure counts
	if !strings.Contains(partial.Error(), "1 succeeded") || !strings.Contains(partial.Error(), "1 failed") {
		fmt.Fprintf(os.Stderr, "FAIL: PartialSyncError.Error() = %q, want contains success/failure counts\n", partial.Error())
		failures++
	}

	// Verify success committed to cache
	if _, err := store.GetSourceScoped(ctx, "strict-test", "ISSUE-42"); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: issue source not committed: %v\n", err)
		failures++
	}

	if failures > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: %d strict assertion failures\n", failures)
		os.Exit(1)
	}

	fmt.Println("PASS: All strict assertions verified")
}
GOEOF
cd "$REPO_ROOT"
SUPP_OUTPUT=$(go run "$DIR"/verify_strict.go 2>&1) || SUPP_EXIT=$?
rm -f "$DIR"/verify_strict.go
if [ "${SUPP_EXIT:-0}" -ne 0 ]; then
  echo "FAIL: Strict validations failed:"
  echo "$SUPP_OUTPUT"
  FAILED=1
else
  echo "PASS: Strict validations passed"
fi

echo ""
echo "=== Scenario 2: All-successful test ==="
echo "Running: go test -run TestSyncResourcesAllSuccess -count=1 ./internal/service/"
if go test -run TestSyncResourcesAllSuccess -count=1 -v "$REPO_ROOT/internal/service/" 2>&1; then
  echo "PASS: TestSyncResourcesAllSuccess passed"
else
  echo "FAIL: TestSyncResourcesAllSuccess failed"
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

if [ "$FAILED" -eq 0 ]; then
  echo ""
  echo "=== ALL VALIDATIONS PASSED ==="
  exit 0
else
  echo ""
  echo "=== SOME VALIDATIONS FAILED ==="
  exit 1
fi
