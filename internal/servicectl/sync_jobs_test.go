package servicectl

import (
	"context"
	"errors"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/service"
)

type admissionSyncService struct {
	calls []string
	err   error
}

func (s *admissionSyncService) result(name string) (*service.SyncResourcesResult, error) {
	s.calls = append(s.calls, name)
	if len(s.calls) == 1 {
		return nil, s.err
	}
	return &service.SyncResourcesResult{SuccessCount: 1}, nil
}

func (s *admissionSyncService) BulkSyncIssues(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error) {
	return s.result("issues")
}
func (s *admissionSyncService) BulkSyncIssueComments(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error) {
	return s.result("issue_comments")
}
func (s *admissionSyncService) BulkSyncWiki(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error) {
	return s.result("wiki")
}
func (s *admissionSyncService) BulkSyncPullRequests(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error) {
	return s.result("pulls")
}
func (s *admissionSyncService) BulkSyncPRComments(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error) {
	return s.result("pr_comments")
}
func (s *admissionSyncService) BulkSyncAll(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error) {
	return s.result("all")
}

func TestRunSyncSelectionsStopsAfterWriterAdmissionContention(t *testing.T) {
	holder := cache.ErrLockContention{
		Path:      "cache.db.lock",
		Operation: "bulk-sync-issues",
		RepoID:    "owner/repo",
		PID:       42,
		StartedAt: time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC),
	}
	svc := &admissionSyncService{err: holder}
	result, collections, err := runSyncSelections(context.Background(), svc, service.BulkSyncRequest{RepoID: "owner/repo"}, StartSyncJobRequest{Issues: true, IssueComments: true, Wiki: true, Pulls: true, PRComments: true})

	var contention cache.ErrLockContention
	if !errors.As(err, &contention) {
		t.Fatalf("runSyncSelections error = %T %[1]v, want ErrLockContention", err)
	}
	if contention.Operation != holder.Operation || contention.RepoID != holder.RepoID || contention.PID != holder.PID || !contention.StartedAt.Equal(holder.StartedAt) {
		t.Fatalf("contention = %#v, want preserved holder metadata %#v", contention, holder)
	}
	if len(svc.calls) != 1 || svc.calls[0] != "issues" {
		t.Fatalf("calls = %v, want only first selected collection", svc.calls)
	}
	if len(collections) != 1 || collections[0].RemoteType != "issue" {
		t.Fatalf("collections = %#v, want only issue attempt", collections)
	}
	if result == nil || result.SuccessCount != 0 || result.FailureCount != 0 {
		t.Fatalf("result = %#v, want empty aggregate without synthetic partial failure", result)
	}
	var partial *service.PartialSyncError
	if errors.As(err, &partial) {
		t.Fatalf("error = %T %[1]v, must preserve direct contention", err)
	}
}

func TestFailedSyncCollectionProgressNamesEachFailedCollection(t *testing.T) {
	events := failedSyncCollectionProgress([]syncCollectionResult{
		{RemoteType: "wiki", Err: errors.New("empty repository")},
		{RemoteType: "pr_comment", Result: &service.SyncResourcesResult{FailureCount: 2}},
		{RemoteType: "issue", Result: &service.SyncResourcesResult{SuccessCount: 1}},
	})
	if len(events) != 2 || events[0].Collection != "wiki" || events[0].RecordsFailed != 1 || events[1].Collection != "pr_comment" || events[1].RecordsFailed != 2 {
		t.Fatalf("events=%+v", events)
	}
}
