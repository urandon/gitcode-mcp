package service

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/feedback"
	"gitcode-mcp/internal/gitcode"
)

func feedbackDraft() feedback.Draft {
	return feedback.Draft{Summary: "Exact issue sync required after bulk failure", Category: "bug", Surface: "sync", ReporterType: "agent", Observed: "bulk sync returned malformed JSON", Expected: "bulk sync completes", Impact: "agent used a narrower fallback", ToolName: "sync_live", FailureClass: "partial_response"}
}

func feedbackService(t *testing.T, client gitcode.Client) (*Service, *cache.SQLiteStore) {
	t.Helper()
	ctx := context.Background()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "feedback-repo", Owner: "example", Name: "feedback", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITCODE_TOKEN", "test-token")
	svc := NewWithClient(store, client)
	svc.ConfigureFeedback(feedback.Config{Enabled: true, Sink: feedback.SinkGitCodeIssues, RepoID: "feedback-repo", Labels: []string{"feedback", "dogfood"}})
	return svc, store
}

func TestSubmitFeedbackUsesAuditedIssueWriteAndReplays(t *testing.T) {
	client := &fakeGitCodeClient{createIssueResult: gitcode.WriteResult[gitcode.Issue]{Record: gitcode.Issue{ID: "remote-91", Number: 91, State: "open"}, Confirmed: true, Operation: "CreateIssue", RemoteID: "91", RemoteNumber: 91, ConfirmedAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)}}
	svc, store := feedbackService(t, client)
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	req := SubmitFeedbackRequest{Draft: feedbackDraft(), Mode: WriteModeLive, IdempotencyKey: "feedback-submit-1"}
	result, err := svc.SubmitFeedback(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "submitted" || result.TicketNumber != 91 || client.createIssueCalls != 1 || !strings.Contains(client.lastCreateIssueRequest.Body, "gitcode-mcp-feedback:") {
		t.Fatalf("result=%#v calls=%d body=%q", result, client.createIssueCalls, client.lastCreateIssueRequest.Body)
	}
	if string(client.lastCreateIssueRequest.Labels) != `"feedback,dogfood"` || client.lastWriteOptions.IdempotencyKey != "feedback-submit-1" {
		t.Fatalf("request=%#v options=%#v", client.lastCreateIssueRequest, client.lastWriteOptions)
	}
	entry, err := store.GetAuditEventByKey(context.Background(), "feedback-repo", "feedback-submit-1")
	if err != nil || entry == nil || entry.Status != "succeeded" {
		t.Fatalf("audit entry=%#v err=%v", entry, err)
	}
	now = now.Add(10 * time.Minute)
	replay, err := svc.SubmitFeedback(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || client.createIssueCalls != 1 {
		t.Fatalf("replay=%#v calls=%d", replay, client.createIssueCalls)
	}
}

func TestSubmitFeedbackRequiresExplicitLiveModeAndConfiguration(t *testing.T) {
	svc, _ := feedbackService(t, &fakeGitCodeClient{onCreateIssue: func(gitcode.CreateIssueRequest, gitcode.WriteOptions) { t.Fatal("provider must not be called") }})
	if _, err := svc.SubmitFeedback(context.Background(), SubmitFeedbackRequest{Draft: feedbackDraft(), Mode: WriteModeDryRun, IdempotencyKey: "k"}); err == nil {
		t.Fatal("dry-run submission unexpectedly succeeded")
	}
	svc.ConfigureFeedback(feedback.DefaultConfig())
	result, err := svc.SubmitFeedback(context.Background(), SubmitFeedbackRequest{Draft: feedbackDraft(), Mode: WriteModeLive, IdempotencyKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "configuration_required" || result.Remediation == "" {
		t.Fatalf("result=%#v", result)
	}
}

func TestSubmitFeedbackReturnExistingPolicyDoesNotWriteLikelyDuplicate(t *testing.T) {
	client := &fakeGitCodeClient{onCreateIssue: func(gitcode.CreateIssueRequest, gitcode.WriteOptions) { t.Fatal("provider must not be called") }}
	svc, store := feedbackService(t, client)
	if err := store.UpsertSourceGraph(context.Background(), cache.SourceGraph{Source: cache.Source{RepoID: "feedback-repo", ID: "ISSUE-42", Kind: "issue", Path: "issues/42.md", Title: "[Feedback/bug][sync] Exact issue sync required after bulk provider failure", Body: "manual feedback", Status: "open", ContentHash: "feedback-42", Provenance: cache.ProvenanceLive}}); err != nil {
		t.Fatal(err)
	}
	svc.ConfigureFeedback(feedback.Config{Enabled: true, Sink: feedback.SinkGitCodeIssues, RepoID: "feedback-repo", DuplicatePolicy: feedback.DuplicatePolicyReturn})
	result, err := svc.SubmitFeedback(context.Background(), SubmitFeedbackRequest{Draft: feedbackDraft(), Mode: WriteModeLive, IdempotencyKey: "return-existing"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "duplicate" || result.DedupeDecision != "likely_match" || result.TicketNumber != 42 || client.createIssueCalls != 0 {
		t.Fatalf("result=%#v calls=%d", result, client.createIssueCalls)
	}
}

func TestCreateIssueClaimsIdempotencyBeforeProviderCall(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	client := &fakeGitCodeClient{createIssueResult: gitcode.WriteResult[gitcode.Issue]{Record: gitcode.Issue{ID: "remote-92", Number: 92, Title: "Concurrent", Body: "body", State: "open"}, Confirmed: true, Operation: "CreateIssue", RemoteID: "92", RemoteNumber: 92, ConfirmedAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)}}
	client.onCreateIssue = func(gitcode.CreateIssueRequest, gitcode.WriteOptions) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
	}
	svc, _ := feedbackService(t, client)
	req := WriteCommandRequest{RepoID: "feedback-repo", Mode: WriteModeLive, Title: "Concurrent", Body: "body", IdempotencyKey: "concurrent-create"}
	firstDone := make(chan error, 1)
	go func() {
		_, err := svc.CreateIssue(context.Background(), req)
		firstDone <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first provider call did not start")
	}
	_, secondErr := svc.CreateIssue(context.Background(), req)
	var writeErr ErrWriteFailure
	if !errors.As(secondErr, &writeErr) || writeErr.Code != "write_idempotency_in_progress" {
		t.Fatalf("second error=%v", secondErr)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first call: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls=%d, want 1", calls.Load())
	}
}
