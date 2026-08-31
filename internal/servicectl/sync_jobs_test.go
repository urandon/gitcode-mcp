package servicectl

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/service"
)

type admissionSyncService struct {
	calls []string
	err   error
}

func TestDirectCacheWritersNormalizeIdentityAndRespectAuthorityFence(t *testing.T) {
	ctx := context.Background()
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	identity, _ := store.CacheIdentity(ctx)
	_ = store.Close()
	manager := newTestManager(t, "darwin")
	cfg := config.Default()
	manager.EffectiveConfig = &cfg
	jobs := NewJobManager("")
	release, blockers := jobs.BeginCacheMutationFence(identity.UUID)
	defer release()
	if len(blockers) != 0 {
		t.Fatalf("unexpected blockers=%v", blockers)
	}
	_, err = jobs.StartSync(ctx, manager, StartSyncJobRequest{RepoID: "owner/repo", CachePath: cachePath, Issues: true})
	var fenced CacheMutationFenceError
	if !errors.As(err, &fenced) {
		t.Fatalf("missing-uuid writer error=%T %v", err, err)
	}
	_, err = jobs.StartRAGIndex(ctx, manager, StartRAGIndexJobRequest{RepoID: "owner/repo", CachePath: cachePath, CacheUUID: "wrong-uuid"})
	if err == nil || err.Error() != "service: cache uuid does not match the selected cache authority" {
		t.Fatalf("wrong-uuid writer error=%T %v", err, err)
	}
}

func TestNormalizeCacheWriterIdentityDoesNotExposePrivateCachePath(t *testing.T) {
	manager := newTestManager(t, "darwin")
	cfg := config.Default()
	manager.EffectiveConfig = &cfg
	privatePath := "/private/sentinel-user/cache-authority/missing.db"
	cachePath, cacheUUID, registrationID, repoID := privatePath, "", "", "owner/repo"
	err := normalizeCacheWriterIdentity(context.Background(), manager, &cachePath, &cacheUUID, &registrationID, &repoID)
	if err == nil || strings.Contains(err.Error(), privatePath) || strings.Contains(err.Error(), "sentinel-user") {
		t.Fatalf("public error leaked cache authority: %T %v", err, err)
	}
	coded, ok := err.(interface{ DiagnosticCode() string })
	if !ok || coded.DiagnosticCode() != "cache_authority_unavailable" {
		t.Fatalf("diagnostic=%T %v", err, err)
	}
}

func TestDirectCacheWriterBlocksConflictFenceAndDaemonAdmission(t *testing.T) {
	ctx := context.Background()
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	identity, _ := store.CacheIdentity(ctx)
	_ = store.Close()
	jobs := NewJobManager("")
	releaseWriter, err := jobs.BeginDirectCacheWriter(identity.UUID, "admin-binding-test")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseWriter()
	releaseFence, blocked := jobs.BeginCacheMutationFence(identity.UUID)
	if len(blocked) != 1 || blocked[0] != "admin-binding-test" {
		t.Fatalf("direct writer did not block conflict fence: %v", blocked)
	}
	releaseFence()
	manager := newTestManager(t, "darwin")
	cfg := config.Default()
	manager.EffectiveConfig = &cfg
	_, err = jobs.StartSync(ctx, manager, StartSyncJobRequest{RepoID: "owner/repo", CachePath: cachePath, Issues: true})
	var busy ErrCacheWriterBusy
	if !errors.As(err, &busy) || busy.ActiveType != "direct_cache_write" {
		t.Fatalf("daemon writer crossed direct reservation: %T %v", err, err)
	}
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
