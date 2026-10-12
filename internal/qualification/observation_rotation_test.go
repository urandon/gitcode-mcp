package qualification

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/observability"
	"gitcode-mcp/internal/service"
	"gitcode-mcp/internal/servicectl"
)

func TestO5ActualJobDiagnosisSurvivesWholeFootprintRotation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ordinal := calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v5/repos/fixture/repository/issues" {
			t.Error("rotation fixture received an unexpected provider operation")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if ordinal == 2 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"synthetic-private-provider-body"}`)
			return
		}
		if ordinal == 3 {
			_, _ = io.WriteString(w, `[{"id":1,"number":1,"title":"Recovered rotation fixture","body":"synthetic-private-rotation-recovery-body","state":"open","comments":0}]`)
			return
		}
		_, _ = io.WriteString(w, `[{"id":1,"number":1,"title":"Fixture","body":"synthetic-private-source-body","state":"open","comments":0}]`)
	}))
	defer provider.Close()
	cachePath := filepath.Join(root, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal("rotation fixture cache unavailable")
	}
	defer store.Close()
	binding := cache.RepositoryBinding{RepoID: "fixture/repository", Owner: "fixture", Name: "repository", APIBaseURL: provider.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}
	if store.AddRepository(ctx, binding) != nil {
		t.Fatal("rotation fixture binding unavailable")
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		t.Fatal("rotation fixture identity unavailable")
	}
	manager, src := restartManager(t, restartInput{Root: root, Provider: provider.URL})
	jobs := servicectl.NewJobManager(filepath.Join(root, "jobs.json"))
	owner := jobs
	defer func() {
		cancel()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()
		for {
			unfence, active := owner.BeginCacheMutationFence(identity.UUID)
			unfence()
			if len(active) == 0 {
				return
			}
			select {
			case <-tick.C:
			case <-deadline.C:
				t.Error("rotation worker did not join before store teardown")
				return
			}
		}
	}()
	ledger := filepath.Join(root, "events")
	collector := qualifyCollector(t, "retained", ledger)
	metrics := observability.NewMetrics(collector.BootID(), time.Now().UTC())
	if !metrics.AttachCollector(collector) {
		t.Fatal("rotation concrete metric origin unavailable")
	}
	jobs.SetObservation(collector)
	jobs.SetMetrics(metrics)
	start := func(key string) servicectl.Job {
		t.Helper()
		job, err := jobs.StartSync(ctx, manager, servicectl.StartSyncJobRequest{RepoID: binding.RepoID, CachePath: cachePath, Issues: true, MaxPages: 1, MaxRecords: 100, IdempotencyKey: key})
		if err != nil {
			t.Fatal("actual rotation job admission unavailable")
		}
		return waitJob(t, jobs, job.ID, identity.UUID)
	}
	baseline := start("rotation-baseline")
	failed := start("rotation-forbidden")
	if baseline.Status != servicectl.JobStatusSucceeded || failed.Status != servicectl.JobStatusFailed || calls.Load() != 2 || len(baseline.SyncCollections) != 1 || baseline.SyncCollections[0].LastSuccessAt == nil {
		t.Fatal("actual rotation execution authority unavailable")
	}
	refs := observability.ProjectEntityRefs(observability.Context{Job: observability.KnownRef("job", failed.ID)})
	var before observability.Page
	eventually(t, func() bool {
		before, err = collector.Query(observability.Filter{}, "", 50)
		if err != nil || before.Cursor == "" {
			return false
		}
		for _, event := range before.Events {
			if event.JobRef == refs.Job && event.Code == observability.JobTerminal && event.Outcome == "failed" && event.FailureClass == "forbidden" {
				return true
			}
		}
		return false
	})
	maintenance := servicectl.NewMaintenanceManager(manager, jobs, "")
	reader := &observability.Reader{Collector: collector, Metrics: metrics, Health: maintenance.ExecutionHealthContext}
	assertRotationIncident(t, mustActiveSummary(t, ctx, reader), failed, *baseline.SyncCollections[0].LastSuccessAt, "active")

	// Catalog pressure is synthetic retained data, NOT an execution counter or
	// fabricated recovery of the actual failed job. Separate fixture refs keep
	// these rows out of that job's causal timeline.
	noise := observability.Context{Job: observability.KnownRef("job", "rotation-pressure"), Registration: observability.KnownRef("registration", "rotation-pressure"), Cache: observability.KnownRef("cache", "rotation-pressure"), Repo: observability.KnownRef("repo", "rotation-pressure"), Correlation: observability.KnownRef("correlation", "rotation-pressure")}
	deadline := time.Now().Add(60 * time.Second)
	var rejected uint64
	for i := 0; i < 10000; i++ {
		code := observability.RecoveryStarted
		if i%2 != 0 {
			code = observability.RecoveryFailed
		}
		for !collector.Emit(code, noise) {
			rejected++
			if time.Now().After(deadline) {
				t.Fatal("bounded catalog pressure admission exhausted")
			}
			time.Sleep(time.Millisecond)
		}
		if time.Now().After(deadline) {
			t.Fatal("bounded catalog pressure did not finish")
		}
	}
	closeCollector(t, collector) // concrete queue, storage and checkpoint joined
	assertRotationInventory(t, ledger, root, provider.URL, identity.UUID, binding.RepoID)
	page, err := collector.Query(observability.Filter{}, "", 50)
	if err != nil || len(page.Events) == 0 || page.Events[0].Sequence <= before.Events[len(before.Events)-1].Sequence || page.Coverage.Dropped != before.Coverage.Dropped+rejected || page.Coverage.LossEpoch != before.Coverage.LossEpoch+rejected {
		t.Fatal("actual rotation or concurrent loss conservation missing")
	}
	_, err = collector.Query(observability.Filter{}, before.Cursor, 50)
	var discontinuity observability.QueryError
	wantReason := "retention_expired"
	if rejected != 0 {
		wantReason = "generation_or_loss_changed"
	}
	if !errors.As(err, &discontinuity) || discontinuity.Code != "snapshot_required" || discontinuity.Reason != wantReason {
		t.Fatal("evicted timeline did not expose its exact typed discontinuity")
	}
	if len(rotationJobEvents(t, reader, refs.Job)) != 0 {
		t.Fatal("rotated job evidence was retained or fabricated")
	}
	at := time.Now().UTC()
	reader.Now = func() time.Time { return at }
	assertRotationIncident(t, mustActiveSummary(t, ctx, reader), failed, *baseline.SyncCollections[0].LastSuccessAt, "active")
	assertTransportParity(t, reader, src, &calls, false, failed, "forbidden", root, provider.URL, identity.UUID, binding.RepoID)

	fresh := observability.NewQualificationCollector(observability.Config{Directory: ledger, ManagedOutput: true})
	t.Cleanup(func() { closeCollector(t, fresh) })
	eventually(t, func() bool { return fresh.Coverage().State != "initializing" })
	freshMetrics := observability.NewMetrics(fresh.BootID(), time.Now().UTC())
	if !freshMetrics.AttachCollector(fresh) || dropMetric(t, freshMetrics) != 0 {
		t.Fatal("rotated ledger reload replayed admission-drop metrics")
	}
	if fresh.Coverage().Dropped != page.Coverage.Dropped || fresh.Coverage().LossEpoch != page.Coverage.LossEpoch {
		t.Fatal("rotated reload erased or recounted durable loss")
	}
	reloaded := servicectl.NewJobManager(filepath.Join(root, "jobs.json"))
	if reloaded.LoadAndMarkInterrupted() != nil {
		t.Fatal("rotation durable jobs reload unavailable")
	}
	owner = reloaded
	retained, ok := reloaded.Get(failed.ID)
	expected := failed
	expected.WorkKey = "" // deliberately json:"-", not persistent authority
	// Reload canonicalizes diagnostic prose, not execution authority. Assert the
	// exact public-safe message contract while preserving every other progress
	// field (including phase, timing, counts and structured failure class).
	expected.Progress = append([]service.ProgressEvent(nil), failed.Progress...)
	for i := range expected.Progress {
		event := &expected.Progress[i]
		if event.RecordsFailed > 0 || event.Type == "failed" || event.Type == "cancelled" || event.Type == "interrupted" || event.Phase == "failed" || event.Phase == "cancelled" || event.Phase == "interrupted" {
			event.Message = "remote cache maintenance failed"
		}
	}
	if !ok || !reflect.DeepEqual(retained, expected) {
		t.Fatal("rotation changed durable failure authority")
	}
	maintenance = servicectl.NewMaintenanceManager(manager, reloaded, "")
	reader.Collector, reader.Metrics, reader.Health = fresh, freshMetrics, maintenance.ExecutionHealthContext
	reloaded.SetObservation(fresh)
	reloaded.SetMetrics(freshMetrics)
	// Ready coverage precedes the asynchronous boot write. Query that concrete
	// marker before starting work, then fence loss to this recovery phase only.
	eventually(t, func() bool {
		page, err := scanRotationEventPage(reader, observability.Filter{Code: observability.Boot})
		if err != nil {
			var query observability.QueryError
			if errors.As(err, &query) && query.Code == "observation_busy" {
				return false
			}
			t.Fatal("rotation boot evidence unavailable")
		}
		for _, event := range page.Events {
			if event.BootID == fresh.BootID() {
				return true
			}
		}
		return false
	})
	assertRotationIncident(t, mustActiveSummary(t, ctx, reader), retained, *baseline.SyncCollections[0].LastSuccessAt, "active")
	assertTransportParity(t, reader, src, &calls, false, retained, "forbidden", root, provider.URL, identity.UUID, binding.RepoID)
	recoveryCoverage := fresh.Coverage()
	resumeWriter, err := fresh.PauseWriterForQualification(ctx)
	if err != nil {
		t.Fatal("rotation qualification writer boundary unavailable")
	}
	defer resumeWriter()
	// No collector reads occur until the real recovery worker has joined and
	// the writer resumes. Emit remains one-shot and uses the bounded real queue.
	release, err := reloaded.BeginDirectCacheWriter(identity.UUID, "synthetic-private-rotation-writer")
	if err != nil {
		t.Fatal("rotation recovery writer fixture unavailable")
	}
	defer release()
	job, err := reloaded.StartSync(ctx, manager, servicectl.StartSyncJobRequest{RepoID: binding.RepoID, CachePath: cachePath, Issues: true, MaxPages: 1, MaxRecords: 100, IdempotencyKey: "rotation-recovered"})
	if err != nil {
		t.Fatal("actual post-rotation recovery admission unavailable")
	}
	var waitingStage string
	eventually(t, func() bool {
		waiting, ok := reloaded.Get(job.ID)
		if ok && waiting.SyncStage != nil && waiting.SyncStage.Phase == servicectl.SyncStageWaitingCommit {
			waitingStage = waiting.SyncStage.StageRef
			return waitingStage != ""
		}
		return false
	})
	if calls.Load() != 3 {
		t.Fatal("rotation recovery staging changed provider traffic")
	}
	release()
	job = waitJob(t, reloaded, job.ID, identity.UUID)
	if job.Status != servicectl.JobStatusSucceeded || calls.Load() != 3 {
		t.Fatal("rotation recovery did not confirm new work exactly once")
	}
	if job.SyncStage == nil || job.SyncStage.StageRef != waitingStage || job.SyncStage.Collection != "issues" || job.SyncStage.Phase != servicectl.SyncStageCommitted || job.SyncStage.Committed != 1 || job.SyncStage.CommittedAt.IsZero() || !job.SyncStage.CommittedAt.After(*failed.FinishedAt) {
		t.Fatal("rotation recovery did not confirm the same durable staged commit")
	}
	if len(job.SyncCollections) != 1 || job.SyncCollections[0].Collection != "issues" || job.SyncCollections[0].Outcome != servicectl.SyncCollectionSuccess || job.SyncCollections[0].Committed != 1 || job.SyncCollections[0].LastSuccessAt == nil || !job.SyncCollections[0].LastSuccessAt.After(*failed.FinishedAt) {
		t.Fatal("rotation recovery lost durable collection commit evidence")
	}
	if !resumeWriter() {
		t.Fatal("rotation qualification window expired before worker completion")
	}
	closeCollector(t, fresh)
	assertRotationInventory(t, ledger, root, provider.URL, identity.UUID, binding.RepoID)
	at = *job.FinishedAt
	assertRotationIncident(t, mustActiveSummary(t, ctx, reader), retained, *job.SyncCollections[0].LastSuccessAt, "recovered")
	assertRotationRecoveryEvents(t, reader, recoveryCoverage, job)
	assertTransportParity(t, reader, src, &calls, false, job, "forbidden", root, provider.URL, identity.UUID, binding.RepoID)
	cached, err := store.GetSourceScoped(ctx, binding.RepoID, "ISSUE-1")
	if err != nil || cached.Body != "synthetic-private-rotation-recovery-body" || cached.Title != "Recovered rotation fixture" || calls.Load() != 3 {
		t.Fatal("rotation diagnosis changed durable cache or provider traffic")
	}
}

func assertRotationIncident(t *testing.T, summary observability.Summary, failed servicectl.Job, success time.Time, state string) {
	t.Helper()
	health := summary.ExecutionHealth
	if health.Source != "durable_jobs_and_maintenance" || health.Coverage != "retained" || state == "active" && (health.State != "degraded" || health.Active != 1) || state == "recovered" && (health.State != "healthy" || health.Active != 0 || health.Recovered != 1) {
		t.Fatal("rotated observation replaced retained execution health authority")
	}
	refs := observability.ProjectEntityRefs(observability.Context{Job: observability.KnownRef("job", failed.ID)})
	for _, incident := range health.Incidents {
		if incident.JobRef == refs.Job && incident.Collection == "issues" && incident.State == state && incident.Reason == "provider_forbidden" && incident.LastSuccessAt != nil && incident.LastSuccessAt.Equal(success) {
			return
		}
	}
	t.Fatal("retained rotation incident lost cause, prior success or exact identity")
}

func rotationJobEvents(t *testing.T, reader *observability.Reader, ref string) []observability.Event {
	t.Helper()
	return rotationEventPage(t, reader, observability.Filter{JobRef: ref}).Events
}

func rotationEventPage(t *testing.T, reader *observability.Reader, filter observability.Filter) observability.Page {
	t.Helper()
	page, err := scanRotationEventPage(reader, filter)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func scanRotationEventPage(reader *observability.Reader, filter observability.Filter) (observability.Page, error) {
	var events []observability.Event
	cursor := ""
	// A 4 MiB ledger and 64 KiB scan chunks need bounded sparse continuation.
	// Keep every selector unchanged; never assume an empty page ends the scan.
	for pages := 0; pages < 128; pages++ {
		page, err := reader.Events(context.Background(), observability.EventsRequest{Filter: filter, Cursor: cursor, Limit: 50})
		if err != nil {
			return observability.Page{}, err
		}
		for _, event := range page.Events {
			if filter.JobRef != "" && event.JobRef != filter.JobRef || filter.Code != "" && event.Code != filter.Code {
				return observability.Page{}, errors.New("rotated timeline admitted a different selector")
			}
		}
		events = append(events, page.Events...)
		if len(events) > observability.MaxPageEvents {
			return observability.Page{}, errors.New("fixture exceeded bounded causal evidence")
		}
		if !page.HasMore {
			page.Events = events
			return page, nil
		}
		if page.Cursor == "" || page.Cursor == cursor {
			return observability.Page{}, errors.New("sparse rotated scan did not make cursor progress")
		}
		cursor = page.Cursor
	}
	return observability.Page{}, errors.New("rotated timeline exceeded its fixed scan budget")
}

func assertRotationRecoveryEvents(t *testing.T, reader *observability.Reader, baseline observability.Coverage, job servicectl.Job) {
	t.Helper()
	refs := observability.ProjectEntityRefs(observability.Context{Job: observability.KnownRef("job", job.ID), Cache: observability.KnownRef("cache", job.CacheUUID), Repo: observability.KnownRef("repo", job.RepoID)})
	page := rotationEventPage(t, reader, observability.Filter{JobRef: refs.Job})
	if err := qualifyJobEvidence(page, baseline, reader.Collector.BootID(), *refs, servicectl.JobStatusSucceeded, ""); err != nil {
		t.Fatal(err)
	}
}

func assertRotationInventory(t *testing.T, dir string, private ...string) {
	t.Helper()
	caps := map[string]int64{"owner.lock": 0, "state-0.json": 4096, "state-1.json": 4096}
	for _, name := range []string{"ledger-0.jsonl", "ledger-1.jsonl", "ledger-2.jsonl", "ledger-3.jsonl", "stdout-0.jsonl", "stdout-1.jsonl", "stderr-0.jsonl", "stderr-1.jsonl"} {
		caps[name] = observability.SegmentBytes
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal("rotation inventory unavailable")
	}
	var total int64
	segments := 0
	for _, entry := range entries {
		cap, allowed := caps[entry.Name()]
		info, err := entry.Info()
		if !allowed || err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > cap {
			t.Fatal("whole diagnostic inventory violated its fixed file boundary")
		}
		total += info.Size()
		if strings.HasSuffix(entry.Name(), ".jsonl") {
			segments++
			if info.Size() == 0 {
				t.Fatal("rotation did not populate every retained segment")
			}
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil || strings.HasSuffix(entry.Name(), ".jsonl") && !strings.HasSuffix(string(data), "\n") {
			t.Fatal("rotated file has an incomplete retained boundary")
		}
		for _, sentinel := range append(private, "synthetic-private-source-body", "synthetic-private-rotation-recovery-body", "synthetic-private-provider-body", "synthetic-private-credential", "synthetic-private-rotation-writer") {
			if strings.Contains(string(data), sentinel) {
				t.Fatal("private source or provider content entered retained files")
			}
		}
	}
	if segments != 8 || total < 4*observability.SegmentBytes || total > observability.LedgerBytes+observability.StreamBytes+observability.MetadataBytes {
		t.Fatal("complete diagnostic footprint was not exercised or bounded")
	}
}
