package servicectl

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
	"gitcode-mcp/internal/observability"
	"gitcode-mcp/internal/service"
)

func TestCollectionRetryMetricsObserveExecutionNotScheduling(t *testing.T) {
	for _, mode := range []string{"success", "cancel_wait", "checkpoint_failure", "resumed", "permanent"} {
		t.Run(mode, func(t *testing.T) {
			_, metrics := testJobMetrics(t, "")
			clock := time.Unix(1700000000, 0).UTC()
			ctx := observability.WithMetrics(context.Background(), metrics)
			attempts := 0
			task := syncCollectionTask{Collection: "wiki", RemoteType: "wiki", PrivateFrontier: "synthetic-private-frontier"}
			if mode == "resumed" {
				task.AttemptStart, task.DueAt = 1, clock.Add(time.Second)
			}
			task.Attempt = func(attempt int) (*service.SyncResourcesResult, syncCollectionResult, error) {
				attempts++
				// Counter must change at entry, not after the operation succeeds.
				want := float64(0)
				if attempt > 1 {
					want = 1
				}
				requireJobCounter(t, metrics, "retry_admissions", map[string]string{"collection": "wiki"}, want)
				var err error
				if mode == "permanent" {
					err = gitcode.ErrForbidden{}
				} else if attempt == 1 {
					err = gitcode.ErrNetworkUnavailable{Status: 503}
				}
				return nil, syncCollectionResult{RemoteType: "wiki", Err: err}, err
			}
			hooks := &syncCollectionRetryHooks{Scheduled: func(syncCollectionTask, int, time.Time) error {
				requireJobCounter(t, metrics, "retry_admissions", map[string]string{"collection": "wiki"}, 0)
				if mode == "checkpoint_failure" {
					return errors.New("synthetic checkpoint failure")
				}
				return nil
			}}
			wait := func(_ context.Context, delay time.Duration) error {
				requireJobCounter(t, metrics, "retry_admissions", map[string]string{"collection": "wiki"}, 0)
				if mode == "cancel_wait" {
					return context.Canceled
				}
				clock = clock.Add(delay)
				return nil
			}
			runSyncCollectionSchedule(ctx, "synthetic-private-cache", "fixture/repository", []syncCollectionTask{task}, 4, nil, wait, func() time.Time { return clock }, hooks)
			want, wantAttempts := float64(0), 1
			if mode == "success" {
				want, wantAttempts = 1, 2
			}
			if mode == "resumed" {
				want = 1
			}
			requireJobCounter(t, metrics, "retry_admissions", map[string]string{"collection": "wiki"}, want)
			if attempts != wantAttempts {
				t.Fatal("observation changed scheduler attempts")
			}
			data, _ := json.Marshal(metrics.Snapshot())
			for _, private := range []string{task.PrivateFrontier, "synthetic-private-cache", "fixture/repository"} {
				if strings.Contains(string(data), private) {
					t.Fatal("retry metric leaked execution identity")
				}
			}
		})
	}
}

func TestCollectionRetryMetricsUnknownAndPreCancelled(t *testing.T) {
	_, metrics := testJobMetrics(t, "")
	metrics.AttachOrigin("maintenance")
	ctx := observability.WithMetrics(context.Background(), metrics)
	observeCollectionRetry(ctx, "synthetic-private-collection", 2)
	requireJobCounter(t, metrics, "retry_admissions", map[string]string{"collection": "other"}, 1)
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	observeCollectionRetry(ctx, "wiki", 2)
	observeCollectionRetry(context.Background(), "wiki", 2)
	observeCollectionRetry(ctx, "wiki", 1)
	requireJobCounter(t, metrics, "retry_admissions", map[string]string{"collection": "wiki"}, 0)
}

func TestWriterMetricsTypedBoundariesAndCoalescing(t *testing.T) {
	m, metrics := testJobMetrics(t, "")
	job, _, err := m.createCoalescedJob(RAGIndexJobType, "fixture/repository", "", 1, "rag-work", "cache", "reg", "", func() {})
	if err != nil {
		t.Fatal("fixture admission failed")
	}
	if _, created, err := m.createCoalescedJob(RAGIndexJobType, "fixture/repository", "", 1, "rag-work", "cache", "reg", "", func() {}); created || err != nil {
		t.Fatal("coalescing changed")
	}
	requireJobCounter(t, metrics, "writer_contention", map[string]string{"kind": "rag"}, 0)
	if _, _, err := m.createCoalescedJob(RAGIndexJobType, "fixture/repository", "", 1, "different-work", "cache", "reg", "", func() {}); err == nil {
		t.Fatal("writer exclusion changed")
	}
	requireJobCounter(t, metrics, "writer_contention", map[string]string{"kind": "rag"}, 1)
	if _, err := m.BeginDirectCacheWriter("cache", "synthetic-private-writer"); err == nil {
		t.Fatal("direct writer exclusion changed")
	}
	requireJobCounter(t, metrics, "writer_contention", map[string]string{"kind": "other"}, 1)
	for _, err := range []error{nil, context.Canceled, errors.New("cache_writer_busy"), CacheRecoveryFenceError{}, CacheMutationFenceError{}} {
		m.observeWriterContention(SyncJobType, err)
	}
	requireJobCounter(t, metrics, "writer_contention", map[string]string{"kind": "sync"}, 0)
	m.observeWriterContention(SyncJobType, fmt.Errorf("synthetic private wrapper: %w", cache.ErrLockContention{}))
	requireJobCounter(t, metrics, "writer_contention", map[string]string{"kind": "sync"}, 1)
	m.finishJob(job.ID, JobStatusSucceeded, "")
	m.markWorkerFinished(job.ID)
	release, err := m.BeginDirectCacheWriter("cache", "writer")
	if err != nil {
		t.Fatal("released writer remained blocked")
	}
	defer release()
	if _, _, err := m.createCoalescedJob(RepositoryDocsIndexJobType, "fixture/repository", "", 1, "docs-work", "cache", "reg", "", func() {}); err == nil {
		t.Fatal("direct writer was ignored")
	}
	requireJobCounter(t, metrics, "writer_contention", map[string]string{"kind": "repository_docs"}, 1)
}

func TestWriterMetricRecordingDoesNotWaitForJobAuthority(t *testing.T) {
	m, metrics := testJobMetrics(t, "")
	m.mu.Lock()
	done := make(chan struct{})
	go func() { m.observeWriterContention(SyncJobType, ErrCacheWriterBusy{}); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		m.mu.Unlock()
		t.Fatal("writer metric waited for execution authority")
	}
	m.mu.Unlock()
	requireJobCounter(t, metrics, "writer_contention", map[string]string{"kind": "sync"}, 1)
}
