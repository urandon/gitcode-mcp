package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/observability"
	"gitcode-mcp/internal/service"
)

func TestCollectionRetryObservationActualScheduler(t *testing.T) {
	for _, mode := range []string{"success", "cancel_wait", "checkpoint_failure", "resumed", "permanent", "without_metrics"} {
		t.Run(mode, func(t *testing.T) {
			collector := jobObservationFixture(t)
			jobs := NewJobManager("")
			jobs.SetObservation(collector)
			job := Job{ID: "fixture-job", CacheUUID: "synthetic-private-cache", RegistrationID: "fixture-registration", RepoID: "fixture/repository"}
			ctx := jobs.withJobObservation(context.Background(), job)
			if mode != "without_metrics" {
				_, metrics := testJobMetrics(t, "")
				ctx = observability.WithMetrics(ctx, metrics)
			}
			clock := time.Unix(1700000000, 0).UTC()
			attempts := 0
			task := syncCollectionTask{Collection: "wiki", RemoteType: "wiki", PrivateFrontier: "synthetic-private-frontier"}
			if mode == "resumed" {
				task.AttemptStart, task.DueAt = 1, clock.Add(time.Second)
			}
			task.Attempt = func(attempt int) (*service.SyncResourcesResult, syncCollectionResult, error) {
				attempts++
				var err error
				if mode == "permanent" {
					err = gitcode.ErrForbidden{}
				} else if attempt == 1 {
					err = gitcode.ErrNetworkUnavailable{Status: 503}
				}
				return nil, syncCollectionResult{RemoteType: "wiki", Err: err}, err
			}
			hooks := &syncCollectionRetryHooks{Scheduled: func(syncCollectionTask, int, time.Time) error {
				if mode == "checkpoint_failure" {
					return errors.New("synthetic-private-checkpoint-error")
				}
				return nil
			}}
			wait := func(_ context.Context, delay time.Duration) error {
				if mode == "cancel_wait" {
					return context.Canceled
				}
				clock = clock.Add(delay)
				return nil
			}
			runSyncCollectionSchedule(ctx, job.CacheUUID, job.RepoID, []syncCollectionTask{task}, 4, nil, wait, func() time.Time { return clock }, hooks)
			want, wantAttempts := 0, 1
			if mode == "success" || mode == "without_metrics" {
				want, wantAttempts = 1, 2
			} else if mode == "resumed" {
				want = 1
			}
			if attempts != wantAttempts {
				t.Fatal("observation changed actual scheduler attempts")
			}
			sealJobObservation(t, collector)
			page, err := collector.Query(observability.Filter{}, "", 100)
			if err != nil || page.HasMore {
				t.Fatal("bounded observation fixture query failed")
			}
			retained := 0
			for _, event := range page.Events {
				if event.Code != observability.Code("retry_admitted") {
					continue
				}
				retained++
				if event.Phase != "retry" || event.Collection != "wiki" || event.Outcome != "admitted" || event.Attempt != 2 || event.FailureClass != "" || event.JobRef != expectedObservationRef("job", job.ID) || event.CacheRef != expectedObservationRef("cache", job.CacheUUID) || event.RepoRef != expectedObservationRef("repo", job.RepoID) || event.RegistrationRef != expectedObservationRef("registration", job.RegistrationID) || event.CorrelationRef != expectedObservationRef("correlation", collector.BootID()+"\x00"+job.ID) {
					t.Fatal("actual retry lost its closed execution details or correlation")
				}
			}
			if retained > want || uint64(retained)+page.Coverage.Dropped != uint64(want) {
				t.Fatal("actual retry boundary was missing, duplicated or falsely scheduled")
			}
			if page.Coverage.Dropped > 0 && page.Coverage.State != "partial" {
				t.Fatal("lost retry evidence appeared complete")
			}
			data, _ := json.Marshal(page)
			for _, private := range []string{job.RepoID, job.CacheUUID, job.RegistrationID, task.PrivateFrontier, "synthetic-private-checkpoint-error"} {
				if strings.Contains(string(data), private) {
					t.Fatal("retry observation leaked execution input")
				}
			}
		})
	}
}

func TestCollectionRetryObservationRejectsUnknownAndNonAttempts(t *testing.T) {
	collector := jobObservationFixture(t)
	jobs := NewJobManager("")
	jobs.SetObservation(collector)
	ctx := jobs.withJobObservation(context.Background(), Job{ID: "fixture-job"})
	observeCollectionRetry(ctx, "synthetic-private-collection", 2)
	for _, attempt := range []int{-1, 0, 1, observability.MaxExecutionAttempt + 1} {
		observeCollectionRetry(ctx, "wiki", attempt)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	observeCollectionRetry(cancelled, "wiki", 2)
	observeCollectionRetry(context.Background(), "wiki", 2)
	sealJobObservation(t, collector)
	page, err := collector.Query(observability.Filter{}, "", 100)
	if err != nil || page.HasMore || page.Coverage.Dropped != 0 {
		t.Fatal("non-attempt fixture unexpectedly lost evidence")
	}
	for _, event := range page.Events {
		if event.Code == observability.Code("retry_admitted") {
			t.Fatal("non-attempt or private dimension entered the retry catalog")
		}
	}
}
