package servicectl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/observability"
	"gitcode-mcp/internal/service"
)

func jobObservationFixture(t *testing.T) *observability.Collector {
	t.Helper()
	c := observability.New(observability.Config{Directory: filepath.Join(t.TempDir(), "observation"), ManagedOutput: true})
	deadline := time.Now().Add(5 * time.Second)
	for c.Coverage().State == "initializing" {
		if time.Now().After(deadline) {
			t.Fatal("fixture collector did not initialize")
		}
		time.Sleep(time.Millisecond)
	}
	if c.Coverage().State != "ready" {
		t.Fatal("fixture collector unavailable")
	}
	t.Cleanup(func() { sealJobObservation(t, c) })
	return c
}

func sealJobObservation(t *testing.T, c *observability.Collector) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !c.Close(ctx, true) {
		t.Fatal("fixture collector did not flush")
	}
}

func expectedObservationRef(kind, id string) string {
	if id == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(kind + "\x00" + id))
	return kind + "-" + hex.EncodeToString(sum[:16])
}

func assertJobObservations(t *testing.T, c *observability.Collector, job Job, want map[observability.Code]int) []observability.Event {
	t.Helper()
	sealJobObservation(t, c)
	page, err := c.Query(observability.Filter{}, "", 100)
	if err != nil || page.HasMore {
		t.Fatal("bounded observation fixture query failed")
	}
	counts := map[observability.Code]int{}
	retained := 0
	for _, e := range page.Events {
		switch e.Code {
		case observability.JobAdmitted, observability.JobTerminal, observability.ProviderAttempt, observability.SyncCommitted, observability.SyncStageChanged:
		default:
			continue
		}
		counts[e.Code]++
		retained++
		if counts[e.Code] > want[e.Code] {
			t.Fatal("replay or unconfirmed boundary emitted execution evidence")
		}
		if job.ID != "" && (e.JobRef != expectedObservationRef("job", job.ID) || e.CacheRef != expectedObservationRef("cache", job.CacheUUID) || e.RepoRef != expectedObservationRef("repo", job.RepoID) || e.RegistrationRef != expectedObservationRef("registration", job.RegistrationID) || e.CorrelationRef != expectedObservationRef("correlation", c.BootID()+"\x00"+job.ID)) {
			t.Fatal("actual execution chain lost boot/job correlation")
		}
	}
	total := 0
	for _, n := range want {
		total += n
	}
	// TryLock admission is intentionally lossy: every actual boundary must be
	// either retained or concretely counted as dropped, never silently absent.
	if uint64(retained)+page.Coverage.Dropped != uint64(total) {
		t.Fatal("actual execution boundaries not conserved")
	}
	if page.Coverage.Dropped > 0 && page.Coverage.State != "partial" {
		t.Fatal("lost evidence presented as complete")
	}
	return page.Events
}

func TestIntegrationExecutionCorrelationActualProviderAndCommit(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := cache.NewSQLiteStore(ctx, filepath.Join(root, "cache.db"))
	if err != nil {
		t.Fatal("cache fixture unavailable")
	}
	defer store.Close()
	binding := cache.RepositoryBinding{RepoID: "fixture/repository", Owner: "fixture", Name: "repository", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}
	if store.AddRepository(ctx, binding) != nil {
		t.Fatal("binding fixture unavailable")
	}
	identity, _ := store.CacheIdentity(ctx)
	schema, _ := store.SchemaVersion(ctx)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Provider-Debug", "synthetic-provider-header")
		_, _ = w.Write([]byte(`[{"id":1,"number":1,"title":"Fixture","body":"synthetic-source-body","state":"open","comments":0}]`))
	}))
	defer server.Close()
	client, err := gitcode.NewHTTPClient(gitcode.Config{BaseURL: server.URL, Token: "synthetic-credential", Timeout: time.Second})
	if err != nil {
		t.Fatal("adapter fixture unavailable")
	}
	svc := service.NewWithClient(store, client)
	collector := jobObservationFixture(t)
	jobs := NewJobManager(filepath.Join(root, "jobs.json"))
	jobs.SetObservation(collector)
	job, created, err := jobs.createCoalescedJob(SyncJobType, binding.RepoID, "", 1, "fixture-work", identity.UUID, maintenanceRegistrationID(identity.UUID, binding.RepoID), "", func() {})
	if err != nil || !created {
		t.Fatal("fixture admission failed")
	}
	ctx = jobs.withJobObservation(ctx, job)
	jobs.updateJob(job.ID, func(j *Job, now time.Time) { j.Status, j.StartedAt = JobStatusRunning, &now })
	works := durableCollectionWorks(svc, service.BulkSyncRequest{RepoID: binding.RepoID, IdempotencyKey: "fixture-request", PerPage: 100, Bounds: &service.SyncBounds{MaxPages: 1, MaxRecords: 100}}, StartSyncJobRequest{Issues: true})
	manager := newTestManager(t, "darwin")
	manager.RuntimeDir = root
	result, _, err := jobs.runDurableCollection(ctx, manager, job.ID, StartSyncJobRequest{RepoID: binding.RepoID, CachePath: filepath.Join(root, "cache.db"), CacheUUID: identity.UUID, RegistrationID: job.RegistrationID}, schema, syncRepositoryBindingFingerprint(binding), works[0], nil, 1)
	if err != nil {
		t.Fatalf("actual offline execution failed: type=%T class=%s", err, jobObservationFailure(maintenanceJobErrorClass(err, "operation_failed")))
	}
	if result == nil || result.SuccessCount != 1 || calls.Load() != 1 {
		t.Fatalf("actual offline execution counts changed: nil=%t calls=%d", result == nil, calls.Load())
	}
	if _, err := store.GetSourceScoped(ctx, binding.RepoID, "ISSUE-1"); err != nil {
		t.Fatal("confirmed source missing")
	}
	jobs.finishJob(job.ID, JobStatusSucceeded, "")
	jobs.finishJob(job.ID, JobStatusSucceeded, "")
	events := assertJobObservations(t, collector, job, map[observability.Code]int{observability.JobAdmitted: 1, observability.ProviderAttempt: 1, observability.SyncCommitted: 1, observability.JobTerminal: 1, observability.SyncStageChanged: 3})
	for _, e := range events {
		if e.Code == observability.ProviderAttempt && (e.Phase != "fetch" || e.Outcome != "succeeded" || e.Attempt != 1) {
			t.Fatal("actual provider outcome changed")
		}
		if e.Code == observability.SyncCommitted && (e.Phase != "commit" || e.Collection != "issues" || e.Outcome != "succeeded") {
			t.Fatal("confirmed commit dimensions changed")
		}
	}
	data, _ := json.Marshal(events)
	for _, secret := range []string{"synthetic-source-body", "synthetic-provider-header", "synthetic-credential", server.URL, root, binding.RepoID, job.ID} {
		if strings.Contains(string(data), secret) {
			t.Fatal("execution evidence captured private input")
		}
	}
}

func TestJobObservationCoalescingPersistenceAndRecoveryNegativeControls(t *testing.T) {
	collector := jobObservationFixture(t)
	path := filepath.Join(t.TempDir(), "jobs.json")
	jobs := NewJobManager(path)
	jobs.SetObservation(collector)
	jobs.writeFile = func(string, []byte, os.FileMode) error { return errors.New("synthetic storage failure") }
	if _, _, err := jobs.createCoalescedJob(SyncJobType, "fixture/repository", "", 0, "fixture-work", "fixture-cache", "fixture-reg", "", func() {}); !errors.Is(err, ErrJobAdmissionPersistence) {
		t.Fatal("failed admission behavior changed")
	}
	jobs.writeFile = durableAtomicWriteFile
	job, _, err := jobs.createCoalescedJob(SyncJobType, "fixture/repository", "", 0, "fixture-work", "fixture-cache", "fixture-reg", "", func() {})
	if err != nil {
		t.Fatal("fixture admission failed")
	}
	if replay, created, err := jobs.createCoalescedJob(SyncJobType, "fixture/repository", "", 0, "fixture-work", "fixture-cache", "fixture-reg", "", func() {}); err != nil || created || replay.ID != job.ID {
		t.Fatal("coalescing changed")
	}
	jobs.writeFile = func(string, []byte, os.FileMode) error { return errors.New("synthetic storage failure") }
	terminal := func(j *Job, now time.Time) { j.Status, j.FinishedAt = JobStatusFailed, &now }
	if jobs.updateJobPersisted(job.ID, terminal) == nil {
		t.Fatal("failed terminal persistence accepted")
	}
	jobs.writeFile = durableAtomicWriteFile
	if jobs.updateJobPersisted(job.ID, terminal) != nil {
		t.Fatal("confirmed terminal persistence failed")
	}
	jobs.finishJob(job.ID, JobStatusFailed, "synthetic raw error")
	assertJobObservations(t, collector, job, map[observability.Code]int{observability.JobAdmitted: 1, observability.JobTerminal: 1})
	restartedCollector := jobObservationFixture(t)
	restarted := NewJobManager(path)
	restarted.SetObservation(restartedCollector)
	if restarted.LoadAndMarkInterrupted() != nil {
		t.Fatal("fixture reload failed")
	}
	restarted.finishJob(job.ID, JobStatusSucceeded, "")
	assertJobObservations(t, restartedCollector, job, map[observability.Code]int{})
}

func TestJobObservationCopiedIdentityNeverReadsHeldAuthority(t *testing.T) {
	collector := jobObservationFixture(t)
	jobs := NewJobManager("")
	jobs.SetObservation(collector)
	job := Job{ID: "fixture-job", CacheUUID: "fixture-cache", RepoID: "fixture/repository", RegistrationID: "fixture-reg", Status: JobStatusSucceeded}
	state := metricState(job)
	jobs.mu.Lock()
	done := make(chan struct{})
	go func() {
		ctx := jobs.withJobObservation(context.Background(), job)
		if !observability.HasObservation(ctx) {
			t.Error("execution context missing")
		}
		jobs.observeJobTerminal(JobStatusRunning, state, true)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		jobs.mu.Unlock()
		t.Fatal("observation waited on job authority")
	}
	jobs.mu.Unlock()
	assertJobObservations(t, collector, job, map[observability.Code]int{observability.JobTerminal: 1})
}

func TestJobObservationUnconfirmedTerminalSnapshotLoss(t *testing.T) {
	for _, unrelated := range []bool{false, true} {
		name := "repeated_completion"
		if unrelated {
			name = "unrelated_snapshot"
		}
		t.Run(name, func(t *testing.T) {
			collector := jobObservationFixture(t)
			path := filepath.Join(t.TempDir(), "jobs.json")
			jobs := NewJobManager(path)
			jobs.SetObservation(collector)
			job, created, err := jobs.createCoalescedJob(SyncJobType, "fixture/repository", "", 0, "fixture-work", "fixture-cache", "fixture-reg", "", func() {})
			if err != nil || !created {
				t.Fatal("fixture admission failed")
			}
			jobs.writeFile = func(string, []byte, os.FileMode) error { return errors.New("synthetic storage failure") }
			jobs.finishJob(job.ID, JobStatusSucceeded, "")
			jobs.writeFile = durableAtomicWriteFile
			if unrelated {
				other := jobs.createJob("fixture", 0, func() {})
				if other.ID == "" {
					t.Fatal("unrelated snapshot fixture failed")
				}
			} else {
				jobs.finishJob(job.ID, JobStatusSucceeded, "")
			}
			loaded := NewJobManager(path)
			if loaded.LoadAndMarkInterrupted() != nil {
				t.Fatal("confirmed snapshot reload failed")
			}
			persisted, ok := loaded.Get(job.ID)
			if !ok || persisted.Status != JobStatusSucceeded {
				t.Fatal("terminal snapshot not durable")
			}
			// A retained failed-save mutation may subsequently become durable,
			// but its unconfirmed observation boundary must remain explicit loss.
			sealJobObservation(t, collector)
			page, err := collector.Query(observability.Filter{}, "", 100)
			if err != nil || page.Coverage.State != "partial" || page.Coverage.Dropped == 0 {
				t.Fatal("unconfirmed terminal boundary silently omitted")
			}
			for _, event := range page.Events {
				if event.Code == observability.JobTerminal {
					t.Fatal("unconfirmed terminal emitted confirmed evidence")
				}
			}
			admissions := 1
			if unrelated {
				admissions++
			}
			assertJobObservations(t, collector, Job{}, map[observability.Code]int{observability.JobAdmitted: admissions, observability.JobTerminal: 1})
		})
	}
}
