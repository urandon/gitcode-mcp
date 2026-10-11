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

func TestCollectionRetryObservationRejectsNonAttempts(t *testing.T) {
	collector := jobObservationFixture(t)
	jobs := NewJobManager("")
	jobs.SetObservation(collector)
	ctx := jobs.withJobObservation(context.Background(), Job{ID: "fixture-job"})
	for _, attempt := range []int{-1, 0, 1} {
		observeCollectionRetry(ctx, "wiki", attempt)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	observeCollectionRetry(cancelled, "wiki", 2)
	observeCollectionRetry(cancelled, "wiki", observability.MaxExecutionAttempt+1)
	observeCollectionRetry(cancelled, "synthetic-private-collection", 2)
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

func TestCollectionRetryObservationRetainedOrdinalCoverage(t *testing.T) {
	for _, tc := range []struct {
		name       string
		collection string
		prior      int
		omitted    bool
	}{
		{"ordinary", "wiki", 1, false},
		{"last_representable", "wiki", observability.MaxExecutionAttempt - 1, false},
		{"over_catalog", "wiki", observability.MaxExecutionAttempt, true},
		{"unknown_collection", "synthetic-private-collection", 1, true},
	} {
		for _, sinks := range []string{"both", "observation_only", "metrics_only", "none"} {
			t.Run(tc.name+"/"+sinks, func(t *testing.T) {
				collector := jobObservationFixture(t)
				jobs := NewJobManager("")
				jobs.SetObservation(collector)
				job := Job{ID: "fixture-job", CacheUUID: "synthetic-private-cache", RegistrationID: "fixture-registration", RepoID: "fixture/repository"}
				ctx := context.Background()
				if sinks == "both" || sinks == "observation_only" {
					ctx = jobs.withJobObservation(ctx, job)
				}
				metrics := observability.NewMetrics(collector.BootID(), time.Now().UTC())
				if !metrics.AttachCollector(collector) {
					t.Fatal("fixture collector metric attachment failed")
				}
				if sinks == "both" || sinks == "metrics_only" {
					ctx = observability.WithMetrics(ctx, metrics)
				}
				clock := time.Unix(1700000000, 0).UTC()
				runtimeDir := t.TempDir()
				checkpoint := syncCollectionRetryCheckpoint{JobID: job.ID, Collection: tc.collection, RemoteType: "wiki", Attempt: tc.prior, RetryAt: clock,
					Request: syncCollectionRetryRequest{CacheUUID: job.CacheUUID, RegistrationID: job.RegistrationID, RepoID: job.RepoID, CachePath: "synthetic-private-cache-path", Wiki: true}}
				if err := newSyncCollectionRetryJournal(runtimeDir).Upsert(checkpoint); err != nil {
					t.Fatal("fixture checkpoint persistence failed")
				}
				loaded, err := newSyncCollectionRetryJournal(runtimeDir).List()
				if err != nil || len(loaded) != 1 || loaded[0].Attempt != tc.prior {
					t.Fatal("durable reload changed accepted retry authority")
				}
				// Recovery supplies the retained ordinal to the actual scheduler;
				// the observer must not sanitize execution authority to fit itself.
				calls, actual := 0, 0
				expectedResult := &service.SyncResourcesResult{}
				task := syncCollectionTask{Collection: loaded[0].Collection, RemoteType: loaded[0].RemoteType, AttemptStart: loaded[0].Attempt, DueAt: loaded[0].RetryAt,
					Attempt: func(attempt int) (*service.SyncResourcesResult, syncCollectionResult, error) {
						calls++
						actual = attempt
						if sinks == "both" || sinks == "metrics_only" {
							collection := "wiki"
							if tc.collection != "wiki" {
								collection = "other"
							}
							requireJobCounter(t, metrics, "retry_admissions", map[string]string{"collection": collection}, 1)
						}
						return expectedResult, syncCollectionResult{RemoteType: "wiki", Result: expectedResult}, nil
					}}
				results := runSyncCollectionSchedule(ctx, job.CacheUUID, job.RepoID, []syncCollectionTask{task}, 4, nil, nil, func() time.Time { return clock }, nil)
				if calls != 1 || actual != tc.prior+1 || len(results) != 1 || results[0].Err != nil || results[0].Result != expectedResult {
					t.Fatal("observation changed retained retry execution or result")
				}
				sealJobObservation(t, collector)
				page, err := collector.Query(observability.Filter{}, "", 100)
				if err != nil || page.HasMore {
					t.Fatal("retry evidence query failed")
				}
				retained := 0
				for _, event := range page.Events {
					if event.Code == observability.RetryAdmitted {
						retained++
						if tc.omitted || event.Attempt != uint32(actual) || event.Collection != "wiki" {
							t.Fatal("catalog fabricated or clamped the retained ordinal")
						}
					}
				}
				want := uint64(1)
				if sinks == "metrics_only" || sinks == "none" {
					want = 0
				}
				if uint64(retained)+page.Coverage.Dropped != want || (tc.omitted && retained != 0) {
					t.Fatal("actual retry admission lost, duplicated or invented evidence")
				}
				if tc.omitted && want != 0 && (page.Coverage.Dropped != 1 || page.Coverage.State != "partial") {
					t.Fatal("unrepresentable actual retry appeared fully covered")
				}
				if tc.omitted || sinks == "metrics_only" || sinks == "none" {
					requireJobCounter(t, metrics, "event_drops", nil, 0)
				}
				data, _ := json.Marshal(page)
				for _, private := range []string{job.RepoID, job.CacheUUID, job.RegistrationID, checkpoint.Request.CachePath, "synthetic-private-collection", runtimeDir} {
					if strings.Contains(string(data), private) {
						t.Fatal("retry omission leaked retained input")
					}
				}
			})
		}
	}
}
