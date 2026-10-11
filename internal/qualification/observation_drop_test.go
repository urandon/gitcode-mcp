package qualification

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/observability"
	"gitcode-mcp/internal/servicectl"
)

// QueryContext checks cancellation before and after its nonblocking lock
// admission. Hold the second check to model a stalled reader without exposing
// a production fault hook or manufacturing a dropped event.
type heldLedgerRead struct {
	context.Context
	checks  atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (c *heldLedgerRead) Err() error {
	if c.checks.Add(1) == 2 {
		close(c.entered)
		select {
		case <-c.release:
		case <-c.Context.Done():
		}
	}
	return c.Context.Err()
}

func TestO5ActualAdmissionDropPreservesExecutionTruth(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v5/repos/fixture/repository/issues" {
			t.Error("drop fixture received an unexpected provider operation")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"id":1,"number":1,"title":"Fixture","body":"synthetic-private-source-body","state":"open","comments":0}]`)
	}))
	defer provider.Close()
	store, err := cache.NewSQLiteStore(ctx, filepath.Join(root, "cache.db"))
	if err != nil {
		t.Fatal("drop fixture cache unavailable")
	}
	defer store.Close()
	binding := cache.RepositoryBinding{RepoID: "fixture/repository", Owner: "fixture", Name: "repository", APIBaseURL: provider.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}
	if store.AddRepository(ctx, binding) != nil {
		t.Fatal("drop fixture binding unavailable")
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		t.Fatal("drop fixture cache identity unavailable")
	}
	manager, src := restartManager(t, restartInput{Root: root, Provider: provider.URL})
	jobs := servicectl.NewJobManager(filepath.Join(root, "jobs.json"))
	collector := qualifyCollector(t, "retained", filepath.Join(root, "events"))
	metrics := observability.NewMetrics(collector.BootID(), time.Now().UTC())
	if !metrics.AttachCollector(collector) {
		t.Fatal("concrete drop origin unavailable")
	}
	jobs.SetObservation(collector)
	jobs.SetMetrics(metrics)
	maintenance := servicectl.NewMaintenanceManager(manager, jobs, "")
	reader := &observability.Reader{Collector: collector, Metrics: metrics, Health: maintenance.ExecutionHealthContext}
	var before observability.Page
	eventually(t, func() bool {
		before, err = collector.Query(observability.Filter{}, "", 1)
		return err == nil && len(before.Events) == 1 && before.Cursor != ""
	})
	if before.Coverage.Dropped != 0 || dropMetric(t, metrics) != 0 {
		t.Fatal("fresh collector fabricated loss")
	}

	held := &heldLedgerRead{Context: ctx, entered: make(chan struct{}), release: make(chan struct{})}
	readDone := make(chan error, 1)
	go func() { _, err := collector.QueryContext(held, observability.Filter{}, "", 1); readDone <- err }()
	var once sync.Once
	released, joined := false, false
	release := func() {
		once.Do(func() { close(held.release); released = true })
		if joined {
			return
		}
		select {
		case err := <-readDone:
			joined = true
			if err != nil {
				t.Error("held ledger reader did not finish its bounded read")
			}
		case <-time.After(time.Second):
			t.Error("held ledger reader did not join")
		}
	}
	defer release()
	select {
	case <-held.entered:
	case <-time.After(time.Second):
		t.Fatal("real ledger read did not reach its owned lock barrier")
	}
	// Rejected untrusted input must not be charged as execution evidence.
	if collector.Emit(observability.Code("synthetic-untrusted-code"), observability.Context{}) || dropMetric(t, metrics) != 0 {
		t.Fatal("untrusted emission manufactured admission loss")
	}
	job, err := jobs.StartSync(ctx, manager, servicectl.StartSyncJobRequest{RepoID: binding.RepoID, CachePath: filepath.Join(root, "cache.db"), Issues: true, MaxPages: 1, MaxRecords: 100, IdempotencyKey: "actual-admission-drop"})
	if err != nil {
		t.Fatal("real sync could not be admitted under diagnostic contention")
	}
	defer func() {
		// Failure paths also unwind the concrete worker before store teardown.
		release()
		cancel()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			unfence, active := jobs.BeginCacheMutationFence(identity.UUID)
			unfence()
			if len(active) == 0 {
				return
			}
			select {
			case <-tick.C:
			case <-deadline.C:
				t.Error("drop fixture worker did not join during cleanup")
				return
			}
		}
	}()
	job = waitJob(t, jobs, job.ID, identity.UUID)
	if released || job.Status != servicectl.JobStatusSucceeded || calls.Load() != 1 {
		t.Fatal("actual worker depended on releasing the diagnostic reader")
	}
	cached, err := store.GetSourceScoped(ctx, binding.RepoID, "ISSUE-1")
	if err != nil || cached.Body != "synthetic-private-source-body" {
		t.Fatal("diagnostic admission loss changed cached source truth")
	}
	for _, read := range []func() error{
		func() error { _, err := reader.Summary(ctx); return err },
		func() error { _, err := reader.Events(ctx, observability.EventsRequest{Limit: 50}); return err },
	} {
		var busy observability.QueryError
		if !errors.As(read(), &busy) || busy.Code != "observation_busy" || busy.Reason != "ledger_busy" {
			t.Fatal("held ledger read hid typed busy or manufactured execution failure")
		}
	}
	drops := dropMetric(t, metrics)
	if drops == 0 {
		t.Fatal("real execution did not encounter validated admission rejection")
	}
	release()
	if !joined {
		t.Fatal("drop reader release was not joined")
	}
	coverage := collector.Coverage()
	if coverage.State != "partial" || coverage.Dropped != drops || coverage.LossEpoch != before.Coverage.LossEpoch+drops {
		t.Fatal("actual admission loss did not conserve coverage and cursor epoch")
	}
	_, err = collector.Query(observability.Filter{}, before.Cursor, 50)
	var discontinuity observability.QueryError
	if !errors.As(err, &discontinuity) || discontinuity.Code != "snapshot_required" || discontinuity.Reason != "generation_or_loss_changed" {
		t.Fatal("pre-loss cursor did not expose the actual missing evidence")
	}
	page, err := collector.Query(observability.Filter{}, "", 50)
	if err != nil {
		t.Fatal("post-loss timeline unavailable")
	}
	refs := observability.ProjectEntityRefs(observability.Context{Job: observability.KnownRef("job", job.ID)})
	for _, event := range page.Events {
		if event.JobRef == refs.Job {
			t.Fatal("rejected execution event was retroactively fabricated")
		}
	}
	at := time.Now().UTC()
	reader.Now = func() time.Time { return at }
	summary := mustActiveSummary(t, ctx, reader)
	if summary.ExecutionHealth.State != "healthy" || summary.ExecutionHealth.Source != "durable_jobs_and_maintenance" || summary.ObservationHealth.DroppedEvents != drops || summary.ObservationHealth.State != "partial" || summary.ObservationHealth.Reason != "ledger_loss" {
		t.Fatal("partial observation replaced confirmed execution authority")
	}
	assertTransportParity(t, reader, src, &calls, false, job, "", root, provider.URL, identity.UUID, binding.RepoID)
	// The actual owner has already exited; clean close checkpoints loss.
	closeCollector(t, collector)
	fresh := qualifyCollector(t, "retained", filepath.Join(root, "events"))
	if fresh.BootID() == collector.BootID() || fresh.Coverage().Dropped != drops || fresh.Coverage().LossEpoch != coverage.LossEpoch {
		t.Fatal("ledger reload erased or recounted durable admission loss")
	}
	_, err = fresh.Query(observability.Filter{}, page.Cursor, 50)
	if err != nil {
		t.Fatal("clean restart invalidated the already-known partial snapshot")
	}
	_, err = fresh.Query(observability.Filter{}, before.Cursor, 50)
	if !errors.As(err, &discontinuity) || discontinuity.Code != "snapshot_required" || discontinuity.Reason != "generation_or_loss_changed" {
		t.Fatal("clean restart erased the pre-loss cursor discontinuity")
	}
	freshMetrics := observability.NewMetrics(fresh.BootID(), time.Now().UTC())
	if !freshMetrics.AttachCollector(fresh) || dropMetric(t, freshMetrics) != 0 {
		t.Fatal("fresh boot replayed historical admission drops into its metric")
	}
	reloaded := servicectl.NewJobManager(filepath.Join(root, "jobs.json"))
	if reloaded.LoadAndMarkInterrupted() != nil {
		t.Fatal("drop execution reload unavailable")
	}
	retained, ok := reloaded.Get(job.ID)
	// WorkKey is deliberately process-local (json:"-"); all persisted
	// execution authority, including progress and collection history, must match.
	expected := job
	expected.WorkKey = ""
	if !ok || !reflect.DeepEqual(retained, expected) {
		t.Fatal("event loss changed durable terminal job")
	}
	maintenance = servicectl.NewMaintenanceManager(manager, reloaded, "")
	reader.Collector, reader.Metrics, reader.Health = fresh, freshMetrics, maintenance.ExecutionHealthContext
	assertTransportParity(t, reader, src, &calls, false, retained, "", root, provider.URL, identity.UUID, binding.RepoID)
	if calls.Load() != 1 {
		t.Fatal("drop diagnosis or reload contacted provider")
	}
}

func dropMetric(t *testing.T, metrics *observability.Metrics) uint64 {
	t.Helper()
	for _, series := range metrics.Snapshot().Series {
		if series.Name == "event_drops" {
			if series.Value == nil || *series.Value < 0 || float64(uint64(*series.Value)) != *series.Value {
				t.Fatal("concrete admission-drop counter unavailable or invalid")
			}
			return uint64(*series.Value)
		}
	}
	t.Fatal("concrete admission-drop series missing")
	return 0
}
