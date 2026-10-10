package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/observability"
	"gitcode-mcp/internal/service"
)

func TestStageObservationActualDurableTransitions(t *testing.T) {
	root := t.TempDir()
	store, err := cache.NewSQLiteStore(context.Background(), filepath.Join(root, "cache.db"))
	if err != nil {
		t.Fatal("stage cache fixture failed")
	}
	binding := cache.RepositoryBinding{RepoID: "fixture/repository", Owner: "fixture", Name: "repository", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}
	if store.AddRepository(context.Background(), binding) != nil {
		t.Fatal("stage binding fixture failed")
	}
	identity, _ := store.CacheIdentity(context.Background())
	schema, _ := store.SchemaVersion(context.Background())
	if store.Close() != nil {
		t.Fatal("stage cache close failed")
	}
	jobs := NewJobManager(filepath.Join(root, "jobs.json"))
	job, _, err := jobs.createCoalescedJob(SyncJobType, binding.RepoID, "", 0, "stage-work", identity.UUID, maintenanceRegistrationID(identity.UUID, binding.RepoID), "", func() {})
	if err != nil {
		t.Fatal("stage job fixture failed")
	}
	c := jobObservationFixture(t)
	jobs.SetObservation(c)
	ctx := jobs.withJobObservation(context.Background(), job)
	manager := newTestManager(t, "darwin")
	manager.RuntimeDir = root
	calls := 0
	_, _, err = jobs.runDurableCollection(ctx, manager, job.ID, StartSyncJobRequest{RepoID: job.RepoID, CachePath: filepath.Join(root, "cache.db"), CacheUUID: job.CacheUUID, RegistrationID: job.RegistrationID}, schema, syncRepositoryBindingFingerprint(binding), durableCollectionWork{
		collection: "issues", remoteType: "issue",
		fetch: func(context.Context) (durableCollectionBatch, error) {
			return durableCollectionBatch{payload: json.RawMessage(`{"items":[]}`), idempotencyKey: "stage-fixture", fetchedAt: time.Now().UTC()}, nil
		},
		commit: func(context.Context, SyncStageEnvelope) (*service.SyncResourcesResult, error) {
			calls++
			return &service.SyncResourcesResult{}, nil
		},
	}, nil, 1)
	if err != nil || calls != 1 {
		t.Fatal("actual stage fixture execution failed")
	}
	sealJobObservation(t, c)
	page, err := c.Query(observability.Filter{}, "", 100)
	if err != nil || page.HasMore {
		t.Fatal("stage observation query failed")
	}
	retained := 0
	for _, e := range page.Events {
		if e.Code == observability.SyncStageChanged {
			retained++
			if e.StageRef == "" || e.JobRef != expectedObservationRef("job", job.ID) || e.CorrelationRef != expectedObservationRef("correlation", c.BootID()+"\x00"+job.ID) || e.Phase != "staging" || e.Collection != "issues" {
				t.Fatal("actual journal transition lost execution identity")
			}
		} else if e.Code == observability.SyncCommitted {
			t.Fatal("a test callback fabricated an actual shared cache commit")
		}
	}
	if uint64(retained)+page.Coverage.Dropped != 3 {
		t.Fatal("actual retained/committing/committed journal boundaries are absent")
	}
}

func TestStageObservationJournalConfirmationIdempotencyAndFaults(t *testing.T) {
	c := jobObservationFixture(t)
	m := NewJobManager("")
	m.SetObservation(c)
	envelope := testSyncStageEnvelope()
	// Long sentinels distinguish private authority from the public opaque
	// prefixes (a short job-1 can occur in a legitimate job-<hex> reference).
	envelope.JobID = "synthetic-private-stage-job"
	envelope.CacheUUID = "synthetic-private-stage-cache"
	envelope.RegistrationID = "synthetic-private-stage-registration"
	ctx := m.withJobObservation(context.Background(), Job{ID: envelope.JobID, CacheUUID: envelope.CacheUUID, RegistrationID: envelope.RegistrationID, RepoID: envelope.RepoID})
	journal := NewSyncStageJournal(t.TempDir(), SyncStageLimits{})
	// Instrumentation must not reread job authority; the existing journal owns
	// the validated envelope and emits after releasing its capacity lock.
	m.mu.Lock()
	stage, err := journal.createObserved(ctx, envelope)
	m.mu.Unlock()
	if err != nil {
		t.Fatal("observed retention failed")
	}
	originalWrite := journal.writeFile
	journal.writeFile = func(string, []byte, os.FileMode) error { return errors.New("synthetic-private-write-error") }
	failed := envelope
	failed.IdempotencyKey = "different-stage"
	if _, err := journal.createObserved(ctx, failed); err == nil {
		t.Fatal("journal create fault was not reached")
	}
	state := stage.State
	state.Phase = SyncStageCommitting
	turnCtx, turn := observability.DeferSyncStage(ctx)
	if _, err := journal.updateStateObserved(turnCtx, stage.StageID, state); err == nil {
		t.Fatal("journal transition fault was not reached")
	}
	turn.Flush() // failed write leaves no confirmed intent
	journal.writeFile = originalWrite
	if _, err := journal.createObserved(ctx, envelope); err != nil {
		t.Fatal("idempotent create changed behavior")
	}
	if _, err := journal.updateStateObserved(ctx, stage.StageID, stage.State); err != nil {
		t.Fatal("timestamp-only journal update changed behavior")
	}
	for i := 0; i < 2; i++ {
		state, _ = nextSyncCommitRetry(stage.StageID, stage.State, time.Now().UTC())
		stage, err = journal.updateStateObserved(ctx, stage.StageID, state)
		if err != nil {
			t.Fatal("actual waiting/attempt transition failed")
		}
	}
	state = stage.State
	state.Phase = SyncStageCommitting
	turnCtx, turn = observability.DeferSyncStage(ctx)
	stage, err = journal.updateStateObserved(turnCtx, stage.StageID, state)
	if err != nil {
		t.Fatal("confirmed committing transition failed")
	}
	turn.Flush()
	turn.Flush()
	stage = rejectCancelledSyncStageObserved(ctx, journal, stage)
	if stage.State.Phase != SyncStageRejected || stage.State.TerminalReason != "cancelled" {
		t.Fatal("observed cancellation changed durable state")
	}
	if _, err := journal.updateStateObserved(ctx, stage.StageID, stage.State); err != nil {
		t.Fatal("repeated cancellation changed behavior")
	}
	restarted := NewSyncStageJournal(filepath.Dir(journal.dir), SyncStageLimits{})
	if _, err := restarted.Load(stage.StageID); err != nil {
		t.Fatal("stage restart fixture failed")
	}
	if _, err := restarted.createObserved(ctx, envelope); err != nil {
		t.Fatal("restart idempotent retention changed behavior")
	}
	sealJobObservation(t, c)
	page, err := c.Query(observability.Filter{}, "", 100)
	if err != nil || page.HasMore {
		t.Fatal("stage journal observation query failed")
	}
	counts := map[string]int{}
	for _, e := range page.Events {
		if e.Code != observability.SyncStageChanged {
			continue
		}
		counts[e.Outcome]++
		if e.StageRef != expectedObservationRef("stage", stage.StageID) || e.Attempt > 2 || (e.Outcome == "waiting_commit" && e.FailureClass != "writer_busy") || (e.Outcome == "rejected" && e.FailureClass != "cancelled") {
			t.Fatal("stage observation lost finite state or validated identity")
		}
	}
	if counts["staged"] > 1 || counts["waiting_commit"] > 2 || counts["committing"] > 1 || counts["rejected"] > 1 || uint64(counts["staged"]+counts["waiting_commit"]+counts["committing"]+counts["rejected"])+page.Coverage.Dropped != 5 {
		t.Fatal("journal replay, timestamp or failed persistence invented a confirmed boundary")
	}
	data, _ := json.Marshal(page.Events)
	for _, private := range []string{envelope.CachePath, envelope.JobID, envelope.CacheUUID, envelope.RegistrationID, envelope.RepoID, stage.StageID, "private source body", "synthetic-private-write-error", envelope.IdempotencyKey} {
		if strings.Contains(string(data), private) {
			t.Fatal("private envelope data escaped into stage evidence")
		}
	}
}

func TestStageObservationUnboundOrMismatchedOwnerStillPersists(t *testing.T) {
	for _, bound := range []bool{false, true} {
		c := jobObservationFixture(t)
		ctx := context.Background()
		if bound {
			ctx = observability.WithObservation(ctx, c, observability.Context{Job: observability.KnownRef("job", "other-job"), Correlation: observability.KnownRef("correlation", "other-correlation")})
		}
		journal := NewSyncStageJournal(t.TempDir(), SyncStageLimits{})
		stage, err := journal.createObserved(ctx, testSyncStageEnvelope())
		if err != nil {
			t.Fatal("observation scope changed journal retention")
		}
		state := stage.State
		state.Phase = SyncStageCommitting
		if _, err := journal.updateStateObserved(ctx, stage.StageID, state); err != nil {
			t.Fatal("observation scope changed journal transition")
		}
		sealJobObservation(t, c)
		page, err := c.Query(observability.Filter{Code: observability.SyncStageChanged}, "", 10)
		if err != nil || len(page.Events) != 0 || page.Coverage.Dropped != 0 {
			t.Fatal("unbound or mismatched stage scope manufactured execution evidence")
		}
	}
}

func TestStageObservationActualRecoveredCacheCommit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		attempt int
	}{
		{"representable", 0}, {"negative", -1}, {"above_catalog", observability.MaxExecutionAttempt + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			cachePath := filepath.Join(root, "cache.db")
			store, err := cache.NewSQLiteStore(context.Background(), cachePath)
			if err != nil {
				t.Fatal("recovered cache fixture failed")
			}
			binding := cache.RepositoryBinding{RepoID: "fixture/repository", Owner: "fixture", Name: "repository", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}
			if store.AddRepository(context.Background(), binding) != nil {
				t.Fatal("recovered binding fixture failed")
			}
			identity, _ := store.CacheIdentity(context.Background())
			schema, _ := store.SchemaVersion(context.Background())
			if store.Close() != nil {
				t.Fatal("recovered cache close failed")
			}
			jobs := NewJobManager(filepath.Join(root, "jobs.json"))
			job, _, err := jobs.createCoalescedJob(SyncJobType, binding.RepoID, "", 0, "recovered-stage-work", identity.UUID, maintenanceRegistrationID(identity.UUID, binding.RepoID), "", func() {})
			if err != nil {
				t.Fatal("recovered job fixture failed")
			}
			batch := service.DurableIssueSyncBatch{Version: service.DurableSyncBatchVersion, RepoID: binding.RepoID, Collection: "issues", IdempotencyKey: "recovered-stage-fixture", FetchedAt: time.Now().UTC(), RecordsListed: 1, Items: []service.DurableIssueItem{{Number: 1, Title: "Fixture", Body: "synthetic-private-recovered-body", State: "open"}}}
			payload, err := json.Marshal(batch)
			if err != nil {
				t.Fatal("recovered batch fixture failed")
			}
			journal := NewSyncStageJournal(root, SyncStageLimits{})
			stage, err := journal.Create(SyncStageEnvelope{JobID: job.ID, CacheUUID: job.CacheUUID, RegistrationID: job.RegistrationID, RepoID: job.RepoID, CacheSchema: schema, CachePath: cachePath, BindingFingerprint: syncRepositoryBindingFingerprint(binding), Collection: "issues", IdempotencyKey: batch.IdempotencyKey, RecordCount: 1, Payload: payload})
			if err != nil {
				t.Fatal("recovered stage retention failed")
			}
			state := stage.State
			state.Attempt = tc.attempt
			if _, err := journal.UpdateState(stage.StageID, state); err != nil {
				t.Fatal("durable ordinal fixture failed")
			}
			stage, err = journal.Load(stage.StageID)
			if err != nil || stage.State.Attempt != tc.attempt {
				t.Fatal("diagnostic range changed journal authority")
			}
			c := jobObservationFixture(t)
			jobs.SetObservation(c)
			manager := newTestManager(t, "darwin")
			manager.RuntimeDir = root
			cfg := config.Default()
			manager.EffectiveConfig = &cfg
			result, stage, err := jobs.commitRecoveredSyncStage(context.Background(), manager, journal, stage)
			if err != nil || result == nil || result.SuccessCount != 1 || stage.State.Phase != SyncStageCommitted {
				t.Fatal("actual recovered shared cache commit failed")
			}
			if stage.State.Attempt != tc.attempt {
				t.Fatal("observation clamped the durable attempt")
			}
			if tc.attempt == 0 {
				assertJobObservations(t, c, job, map[observability.Code]int{observability.SyncStageChanged: 2, observability.SyncCommitted: 1})
				return
			}
			sealJobObservation(t, c)
			page, err := c.Query(observability.Filter{}, "", 20)
			if err != nil || page.HasMore {
				t.Fatal("unrepresentable stage query failed")
			}
			commits := 0
			for _, e := range page.Events {
				if e.Code == observability.SyncStageChanged {
					t.Fatal("unrepresentable ordinal fabricated a stage event")
				}
				if e.Code == observability.SyncCommitted {
					commits++
				}
			}
			if page.Coverage.State != "partial" || page.Coverage.Dropped < 2 || uint64(commits)+page.Coverage.Dropped != 3 {
				t.Fatal("actual successful commit hid omitted journal boundaries")
			}
		})
	}
}
