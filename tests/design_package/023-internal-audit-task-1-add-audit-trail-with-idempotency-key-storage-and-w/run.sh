#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/gitcode-mcp-023-validation.XXXXXX")"
cleanup() {
  rm -rf "$WORKDIR"
}
trap cleanup EXIT

cp -R "$ROOT/." "$WORKDIR/"
rm -rf "$WORKDIR/.git" "$WORKDIR/ai/artifacts" 2>/dev/null || true

cat >> "$WORKDIR/internal/service/service_test.go" <<'GOEOF'

func TestDesignPackage023AuditSuccessReplayAndFailureRetry(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedStore(t, ctx, store)
	client := &fakeGitCodeClient{createIssueResult: gitcode.WriteResult[gitcode.Issue]{Record: gitcode.Issue{ID: "remote-2301", Number: 2301, Title: "Audit validation", Body: "success body", State: "open"}, Confirmed: true, Operation: "CreateIssue", RemoteID: "2301", RemoteNumber: 2301, ConfirmedAt: time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)}}
	svc := NewWithClient(store, client)
	t.Setenv("GITCODE_TOKEN", "offline-validation-token")

	req := WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "Audit validation", Body: "success body", IdempotencyKey: "scenario-023-success-key"}
	result, err := svc.CreateIssue(ctx, req)
	if err != nil {
		t.Fatalf("scenario-1 CreateIssue returned error: %v", err)
	}
	if result.Status != "succeeded" || result.RemoteID != "2301" || result.ID == "" || result.Replayed {
		t.Fatalf("scenario-1 unexpected result: %#v", result)
	}
	entry, err := store.GetAuditEventByKey(ctx, "fixture-a", "scenario-023-success-key")
	if err != nil {
		t.Fatalf("scenario-1 audit lookup error: %v", err)
	}
	if entry == nil || entry.Status != "succeeded" || entry.Operation != "create-issue" || entry.RecordID == "" || entry.RemoteType != "issue" || entry.RemoteID != "2301" || entry.PayloadHash == "" {
		t.Fatalf("scenario-1 audit entry missing success outcome fields: %#v", entry)
	}
	counts, err := store.RecordCounts(ctx, "fixture-a")
	if err != nil {
		t.Fatalf("scenario-1 count error: %v", err)
	}
	if counts.AuditRows != 1 || client.createIssueCalls != 1 {
		t.Fatalf("scenario-1 audit rows=%d adapter calls=%d, want 1 and 1", counts.AuditRows, client.createIssueCalls)
	}

	replay, err := svc.CreateIssue(ctx, req)
	if err != nil {
		t.Fatalf("scenario-2 replay returned error: %v", err)
	}
	if replay.Status != "already_applied" || !replay.Replayed || replay.RemoteID != "2301" {
		t.Fatalf("scenario-2 expected already_applied replay, got %#v", replay)
	}
	counts, err = store.RecordCounts(ctx, "fixture-a")
	if err != nil {
		t.Fatalf("scenario-2 count error: %v", err)
	}
	if counts.AuditRows != 1 || client.createIssueCalls != 1 {
		t.Fatalf("scenario-2 duplicate detected: audit rows=%d adapter calls=%d", counts.AuditRows, client.createIssueCalls)
	}

	failureStore, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer failureStore.Close()
	seedStore(t, ctx, failureStore)
	failureClient := &fakeGitCodeClient{errors: []error{gitcode.ErrNetworkUnavailable{Endpoint: "/issues", Attempts: 1}}, createIssueResult: gitcode.WriteResult[gitcode.Issue]{Record: gitcode.Issue{ID: "remote-2302", Number: 2302, Title: "Retry validation", Body: "retry body", State: "open"}, Confirmed: true, Operation: "CreateIssue", RemoteID: "2302", RemoteNumber: 2302, ConfirmedAt: time.Date(2026, 6, 22, 12, 1, 0, 0, time.UTC)}}
	failureSvc := NewWithClient(failureStore, failureClient)
	retryReq := WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "Retry validation", Body: "retry body", IdempotencyKey: "scenario-023-retry-key"}
	_, err = failureSvc.CreateIssue(ctx, retryReq)
	var writeErr ErrWriteFailure
	if !errors.As(err, &writeErr) || writeErr.Code != "write_network_unavailable" {
		t.Fatalf("scenario-3 first write err=%v, want write_network_unavailable", err)
	}
	failedEntry, err := failureStore.GetAuditEventByKey(ctx, "fixture-a", "scenario-023-retry-key")
	if err != nil {
		t.Fatalf("scenario-3 failure audit lookup error: %v", err)
	}
	if failedEntry == nil || failedEntry.Status != "failed" || failedEntry.Message != "write_network_unavailable" || failedEntry.RemoteID != "" || failedEntry.PayloadHash == "" {
		t.Fatalf("scenario-3 failure audit entry not retry-safe: %#v", failedEntry)
	}
	retry, err := failureSvc.CreateIssue(ctx, retryReq)
	if err != nil {
		t.Fatalf("scenario-3 retry should be allowed, got error: %v", err)
	}
	if retry.Status != "succeeded" || retry.RemoteID != "2302" || failureClient.createIssueCalls != 2 {
		t.Fatalf("scenario-3 retry result=%#v adapter calls=%d, want success and 2 calls", retry, failureClient.createIssueCalls)
	}
	finalEntry, err := failureStore.GetAuditEventByKey(ctx, "fixture-a", "scenario-023-retry-key")
	if err != nil {
		t.Fatalf("scenario-3 final audit lookup error: %v", err)
	}
	if finalEntry == nil || finalEntry.Status != "succeeded" || finalEntry.RemoteID != "2302" {
		t.Fatalf("scenario-3 retry did not update audit to success: %#v", finalEntry)
	}
}

func TestDesignPackage023AuditConflictPartialAndRepoScope(t *testing.T) {
	ctx := context.Background()

	conflictStore, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conflictStore.Close()
	seedStore(t, ctx, conflictStore)
	conflictClient := &fakeGitCodeClient{createIssueResult: gitcode.WriteResult[gitcode.Issue]{Record: gitcode.Issue{ID: "remote-2303", Number: 2303, Title: "Original", Body: "body", State: "open"}, Confirmed: true, Operation: "CreateIssue", RemoteID: "2303", RemoteNumber: 2303, ConfirmedAt: time.Date(2026, 6, 22, 12, 2, 0, 0, time.UTC)}}
	conflictSvc := NewWithClient(conflictStore, conflictClient)
	t.Setenv("GITCODE_TOKEN", "offline-validation-token")
	if _, err := conflictSvc.CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "Original", Body: "body", IdempotencyKey: "scenario-023-conflict-key"}); err != nil {
		t.Fatalf("S023-audit-conflict initial write returned error: %v", err)
	}
	_, err = conflictSvc.CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "Changed", Body: "body", IdempotencyKey: "scenario-023-conflict-key"})
	var conflictErr ErrWriteFailure
	if !errors.As(err, &conflictErr) || conflictErr.Code != "write_idempotency_conflict" || conflictClient.createIssueCalls != 1 {
		t.Fatalf("S023-audit-conflict err=%v calls=%d, want conflict without second adapter call", err, conflictClient.createIssueCalls)
	}

	partialStore, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer partialStore.Close()
	seedStore(t, ctx, partialStore)
	wrapped := &writeRefreshFailStore{Store: partialStore, failNextRefresh: true}
	partialClient := &fakeGitCodeClient{createIssueResult: gitcode.WriteResult[gitcode.Issue]{Record: gitcode.Issue{ID: "remote-2304", Number: 2304, Title: "Partial", Body: "body", State: "open"}, Confirmed: true, Operation: "CreateIssue", RemoteID: "2304", RemoteNumber: 2304, RemoteRevision: "rev-2304", ConfirmedAt: time.Date(2026, 6, 22, 12, 3, 0, 0, time.UTC)}}
	partialSvc := NewWithClient(wrapped, partialClient)
	partialReq := WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "Partial", Body: "body", IdempotencyKey: "scenario-023-partial-key"}
	_, err = partialSvc.CreateIssue(ctx, partialReq)
	var partialErr ErrWriteFailure
	if !errors.As(err, &partialErr) || partialErr.Code != "write_partial_cache_refresh_failed" {
		t.Fatalf("S023-audit-partial-refresh first err=%v, want partial refresh failure", err)
	}
	partialResult, err := partialSvc.CreateIssue(ctx, partialReq)
	if err != nil || partialResult.Status != "succeeded" || !partialResult.Replayed || partialClient.createIssueCalls != 1 {
		t.Fatalf("S023-audit-partial-refresh result=%#v err=%v calls=%d", partialResult, err, partialClient.createIssueCalls)
	}

	scopeStore, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer scopeStore.Close()
	seedStore(t, ctx, scopeStore)
	if err := scopeStore.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-b", Owner: "owner-b", Name: "repo-b", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}, DisplayName: "Fixture B", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	scopeClient := &fakeGitCodeClient{createIssueResults: []gitcode.WriteResult[gitcode.Issue]{
		{Record: gitcode.Issue{ID: "remote-2305", Number: 2305, Title: "Scoped", Body: "body", State: "open"}, Confirmed: true, Operation: "CreateIssue", RemoteID: "2305", RemoteNumber: 2305, ConfirmedAt: time.Date(2026, 6, 22, 12, 4, 0, 0, time.UTC)},
		{Record: gitcode.Issue{ID: "remote-2306", Number: 2306, Title: "Scoped", Body: "body", State: "open"}, Confirmed: true, Operation: "CreateIssue", RemoteID: "2306", RemoteNumber: 2306, ConfirmedAt: time.Date(2026, 6, 22, 12, 5, 0, 0, time.UTC)},
	}}
	scopeSvc := NewWithClient(scopeStore, scopeClient)
	if _, err := scopeSvc.CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-a", Mode: WriteModeLive, Title: "Scoped", Body: "body", IdempotencyKey: "scenario-023-shared-key"}); err != nil {
		t.Fatalf("S023-audit-repo-scope repo A error: %v", err)
	}
	if _, err := scopeSvc.CreateIssue(ctx, WriteCommandRequest{RepoID: "fixture-b", Mode: WriteModeLive, Title: "Scoped", Body: "body", IdempotencyKey: "scenario-023-shared-key"}); err != nil {
		t.Fatalf("S023-audit-repo-scope repo B error: %v", err)
	}
	if scopeClient.createIssueCalls != 2 {
		t.Fatalf("S023-audit-repo-scope calls=%d want 2", scopeClient.createIssueCalls)
	}
}
GOEOF

(
  cd "$WORKDIR"
  go test ./internal/service -run 'TestDesignPackage023' -count=1
  go test ./internal/audit ./internal/cache ./internal/service -count=1
)
