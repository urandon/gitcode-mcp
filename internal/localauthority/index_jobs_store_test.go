package localauthority

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gitcode-mcp/internal/authority"
)

func indexQueueFixture(t *testing.T) (*indexQueue, *Store, string) {
	t.Helper()
	_, s, dir := projectionFixture(t)
	q, err := openIndexQueue(context.Background(), s)
	if err != nil {
		t.Fatal("prepared index queue fixture unavailable")
	}
	t.Cleanup(func() { _ = q.close() })
	return q, s, dir
}

func indexQueueEmpty(t *testing.T, q *indexQueue) indexJobSnapshot {
	t.Helper()
	state := indexJobSnapshot{Schema: 1, ProjectID: q.store.manifest.ID, TrackerInstance: q.store.instance, ManifestHash: manifestHash(q.store.manifest), Generation: 1, Jobs: []indexJobRecord{}}
	if err := q.publish(context.Background(), state); err != nil {
		t.Fatal("explicit index ledger initial publication failed")
	}
	return state
}

func indexQueueAdmitted(t *testing.T, q *indexQueue) indexJobSnapshot {
	t.Helper()
	r := indexJobModelFixture(t)
	r.Selection.ProjectID, r.Selection.TrackerInstance, r.Selection.ManifestHash = q.store.manifest.ID, q.store.instance, manifestHash(q.store.manifest)
	state := q.state
	state.Generation = 2
	state.Jobs = []indexJobRecord{r}
	if err := q.publish(context.Background(), state); err != nil {
		t.Fatal("explicit index intent publication failed")
	}
	return state
}

func TestIndexQueueMissingObservationAndUnownedRefusal(t *testing.T) {
	q, s, dir := indexQueueFixture(t)
	ctx := context.Background()
	if q.verifyExpected() != nil || q.state.Generation != 0 {
		t.Fatal("missing index ledger became initialized")
	}
	if _, err := os.Lstat(filepath.Join(dir, indexJobsDirectory)); !os.IsNotExist(err) {
		t.Fatal("index ledger observation provisioned its namespace")
	}
	ro, err := OpenReadOnly(ctx, dir, testProject)
	if err != nil {
		t.Fatal("read-only control unavailable")
	}
	defer ro.Close()
	if admitted, err := openIndexQueue(ctx, ro); err == nil {
		_ = admitted.close()
		t.Fatal("read-only authority obtained index intent ownership")
	}
	before := indexQueueEmpty(t, q)
	if err := q.close(); err != nil {
		t.Fatal("index intent owner did not close")
	}
	var phases []string
	next, err := openIndexQueueWithHooks(ctx, s, nil, func(phase string) error {
		phases = append(phases, phase)
		return nil
	})
	if err != nil {
		t.Fatal("verified index intent reopen failed")
	}
	defer next.close()
	if !reflect.DeepEqual(before, next.state) || !reflect.DeepEqual(phases, []string{"index_reopen_file", "index_reopen_directory", "index_reopen_root"}) {
		t.Fatal("index intent reopen changed identity or omitted qualification")
	}
	if next.verifyExpected() != nil || len(phases) != 3 {
		t.Fatal("ordinary ledger observation requalified or changed state")
	}
}

func TestIndexQueuePublicationFaultRetainsIntentAndDoesNotFenceSource(t *testing.T) {
	for _, phase := range []string{"index_stage_write", "index_stage_ready", "index_state_rename", "index_state_acknowledgement"} {
		t.Run(phase, func(t *testing.T) {
			q, s, _ := indexQueueFixture(t)
			ctx := context.Background()
			indexQueueEmpty(t, q)
			admitted := indexQueueAdmitted(t, q)
			created := createIssue(t, s, "synthetic-index-fault-source")
			next := admitted
			next.Generation++
			next.Jobs = append([]indexJobRecord{}, admitted.Jobs...)
			next.Jobs[0].State, next.Jobs[0].Generation, next.Jobs[0].Attempts = "running", 2, 1
			q.fault = func(at string) error {
				if at == phase {
					return context.Canceled
				}
				return nil
			}
			if err := q.publish(ctx, next); !errors.Is(err, context.Canceled) {
				t.Fatal("index state phase control did not fail")
			}
			postRename := phase == "index_state_rename" || phase == "index_state_acknowledgement"
			if postRename {
				if q.fence != IndexJobsUncertain {
					t.Fatal("ambiguous job state remained acknowledged")
				}
			} else if q.fence != "" || !reflect.DeepEqual(q.state, admitted) {
				t.Fatal("unpublished stage erased prior intent")
			}
			status, err := s.Status(ctx)
			read, e := s.Read(ctx, authority.ReadRequest{ProjectID: testProject, Kind: authority.Issue, EntityID: created.Citation.EntityID, Operation: "get"})
			replay := createIssue(t, s, "synthetic-index-fault-source")
			if err != nil || e != nil || status.Readiness != "ready" || status.AuthorityGeneration != 1 || len(read.Entities) != 1 || !replay.Replayed || replay.Citation != created.Citation {
				t.Fatal("index intent fault changed canonical authority")
			}
			if q.close() != nil {
				t.Fatal("faulted intent owner did not close")
			}
			loaded, err := openIndexQueue(ctx, s)
			if err != nil {
				t.Fatal("qualified job intent reopen failed")
			}
			defer loaded.close()
			want := admitted
			if postRename {
				want = next
			}
			if !reflect.DeepEqual(loaded.state, want) {
				t.Fatal("index reopen guessed stage or discarded exact intent")
			}
		})
	}
}

func TestIndexQueueParentRetryAndEveryReopenBarrier(t *testing.T) {
	t.Run("namespace_retry", func(t *testing.T) {
		q, _, _ := indexQueueFixture(t)
		state := indexJobSnapshot{Schema: 1, ProjectID: q.store.manifest.ID, TrackerInstance: q.store.instance, ManifestHash: manifestHash(q.store.manifest), Generation: 1, Jobs: []indexJobRecord{}}
		attempts := 0
		q.barrierFault = func(at string) error {
			if at == "index_namespace_root" {
				attempts++
				if attempts == 1 {
					return Error{DurabilityUnavailable}
				}
			}
			return nil
		}
		requireProjectionCode(t, q.publish(context.Background(), state), DurabilityUnavailable)
		if q.directory == nil || q.state.Generation != 0 {
			t.Fatal("failed parent control changed durable intent")
		}
		if err := q.publish(context.Background(), state); err != nil || attempts != 2 {
			t.Fatal("retained index namespace skipped its parent barrier")
		}
	})
	for _, phase := range []string{"index_reopen_file", "index_reopen_directory", "index_reopen_root"} {
		t.Run(phase, func(t *testing.T) {
			q, s, dir := indexQueueFixture(t)
			indexQueueEmpty(t, q)
			indexQueueAdmitted(t, q)
			if q.close() != nil {
				t.Fatal("index intent fixture did not close")
			}
			final := filepath.Join(dir, indexJobsDirectory, "state.json")
			before, err := os.ReadFile(final)
			if err != nil {
				t.Fatal("final intent control unavailable")
			}
			for _, cancelled := range []bool{false, true} {
				ctx, cancel := context.WithCancel(context.Background())
				seen := false
				next, err := openIndexQueueWithHooks(ctx, s, nil, func(at string) error {
					if at == phase {
						seen = true
						if cancelled {
							cancel()
							return nil
						}
						return Error{DurabilityUnavailable}
					}
					return nil
				})
				cancel()
				if next != nil || !seen {
					t.Fatal("index owner admitted despite failed reopen barrier")
				}
				if cancelled {
					if !errors.Is(err, context.Canceled) {
						t.Fatal("index admission lost cancellation")
					}
				} else {
					requireProjectionCode(t, err, DurabilityUnavailable)
				}
			}
			after, err := os.ReadFile(final)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("failed reopen erased retained caller/cancellation intent")
			}
		})
	}
}

func TestIndexQueueMissingFinalAndOrphanCapacityAreNotFirstBoot(t *testing.T) {
	q, s, dir := indexQueueFixture(t)
	ctx := context.Background()
	state := indexJobSnapshot{Schema: 1, ProjectID: s.manifest.ID, TrackerInstance: s.instance, ManifestHash: manifestHash(s.manifest), Generation: 1, Jobs: []indexJobRecord{}}
	q.fault = func(at string) error {
		if at == "index_stage_write" {
			return context.Canceled
		}
		return nil
	}
	for i := 0; i < maxIndexStages; i++ {
		if err := q.publish(ctx, state); !errors.Is(err, context.Canceled) {
			t.Fatal("bounded orphan index control failed")
		}
	}
	q.fault = nil
	requireProjectionCode(t, q.publish(ctx, state), Capacity)
	before, err := os.ReadDir(filepath.Join(dir, indexJobsDirectory))
	if err != nil || len(before) != maxIndexStages || q.close() != nil {
		t.Fatal("index orphan evidence control unavailable")
	}
	loaded, err := openIndexQueue(ctx, s)
	if loaded != nil {
		_ = loaded.close()
		t.Fatal("missing final index intent silently initialized")
	}
	requireProjectionCode(t, err, IndexJobsUncertain)
	after, err := os.ReadDir(filepath.Join(dir, indexJobsDirectory))
	status, e := s.Status(ctx)
	if err != nil || e != nil || len(before) != len(after) || status.Readiness != "ready" {
		t.Fatal("uncertain index ledger erased evidence or blocked canonical state")
	}
}
