package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/observability"
	"gitcode-mcp/internal/service"
)

func TestWriterObservationActualPreAdmissionAndDirectDenial(t *testing.T) {
	m := NewJobManager("")
	job, _, err := m.createCoalescedJob(RAGIndexJobType, "fixture/repository", "", 1, "work", "fixture-cache", "fixture-registration", "", func() {})
	if err != nil {
		t.Fatal("fixture admission failed")
	}
	collector := jobObservationFixture(t)
	m.SetObservation(collector)
	if _, created, err := m.createCoalescedJob(RAGIndexJobType, job.RepoID, "", 1, "work", job.CacheUUID, job.RegistrationID, "", func() {}); created || err != nil {
		t.Fatal("coalescing changed execution")
	}
	if _, _, err := m.createCoalescedJob(RepositoryDocsIndexJobType, job.RepoID, "", 1, "denied-work", job.CacheUUID, job.RegistrationID, "", func() {}); err == nil {
		t.Fatal("actual writer denial not reached")
	}
	if _, err := m.BeginDirectCacheWriter(job.CacheUUID, "synthetic-private-writer-id"); err == nil {
		t.Fatal("actual direct writer denial not reached")
	}
	sealJobObservation(t, collector)
	page, err := collector.Query(observability.Filter{}, "", 100)
	if err != nil || page.HasMore {
		t.Fatal("bounded writer observation query failed")
	}
	retained := 0
	for _, event := range page.Events {
		if event.Code != observability.WriterContended {
			continue
		}
		retained++
		if event.JobRef != "" || event.CacheRef != expectedObservationRef("cache", job.CacheUUID) || event.CorrelationRef == "" || event.Phase != "writer" || event.Outcome != "denied" || event.FailureClass != "writer_busy" {
			t.Fatal("pre-admission denial invented a job or lost its resolved cache scope")
		}
		if event.WorkKind != "repository_docs" && event.WorkKind != "other" {
			t.Fatal("writer denial lost its finite work kind")
		}
		if event.CorrelationRef != expectedObservationRef("correlation", collector.BootID()+"\x00writer\x00"+event.WorkKind) {
			t.Fatal("pre-admission correlation is not the fixed boot/work-kind scope")
		}
		if event.WorkKind == "repository_docs" && (event.RepoRef != expectedObservationRef("repo", job.RepoID) || event.RegistrationRef != expectedObservationRef("registration", job.RegistrationID)) {
			t.Fatal("validated pre-admission scope was lost")
		}
		if event.WorkKind == "other" && (event.RepoRef != "" || event.RegistrationRef != "") {
			t.Fatal("direct writer invented repository authority")
		}
	}
	if retained > 2 || uint64(retained)+page.Coverage.Dropped != 2 {
		t.Fatal("actual typed writer denials were lost or coalescing emitted a denial")
	}
	if page.Coverage.Dropped > 0 && page.Coverage.State != "partial" {
		t.Fatal("writer observation loss appeared complete")
	}
	data, _ := json.Marshal(page)
	for _, private := range []string{job.RepoID, job.CacheUUID, job.RegistrationID, "synthetic-private-writer-id", "denied-work"} {
		if strings.Contains(string(data), private) {
			t.Fatal("writer observation leaked resolved authority or request input")
		}
	}
}

func writerObservationPage(t *testing.T, c *observability.Collector, boundaries int, stageBoundaries ...int) []observability.Event {
	t.Helper()
	sealJobObservation(t, c)
	page, err := c.Query(observability.Filter{}, "", 100)
	if err != nil || page.HasMore {
		t.Fatal("writer observation query failed")
	}
	var events []observability.Event
	stages, expectedStages := 0, 0
	for _, n := range stageBoundaries {
		expectedStages += n
	}
	for _, event := range page.Events {
		if event.Code == observability.SyncStageChanged {
			stages++
		}
		if event.Code == observability.WriterContended {
			events = append(events, event)
		}
	}
	if len(events) > boundaries || stages > expectedStages || uint64(len(events)+stages)+page.Coverage.Dropped != uint64(boundaries+expectedStages) {
		t.Fatal("writer boundary retention/loss was not conserved")
	}
	if page.Coverage.Dropped > 0 && page.Coverage.State != "partial" {
		t.Fatal("lost writer evidence appeared complete")
	}
	return events
}

func TestWriterObservationTypedContextAndHeldJobAuthority(t *testing.T) {
	m, metrics := testJobMetrics(t, "")
	c := jobObservationFixture(t)
	m.SetObservation(c)
	job := Job{ID: "fixture-job", CacheUUID: "fixture-cache", RepoID: "fixture/repository", RegistrationID: "fixture-registration"}
	ctx := m.withJobObservation(context.Background(), job)
	for _, err := range []error{nil, context.Canceled, CacheMutationFenceError{}, CacheRecoveryFenceError{}, errors.New("cache_writer_busy")} {
		m.observeWriterContentionInContext(ctx, SyncJobType, err)
	}
	m.mu.Lock()
	done := make(chan struct{})
	go func() {
		m.observeWriterContentionInContext(ctx, SyncJobType, fmt.Errorf("synthetic-private-error: %w", cache.ErrLockContention{Path: "synthetic-private-path", HolderHint: "synthetic-private-holder", RepoID: "synthetic-private-repo", Operation: "synthetic-private-operation"}))
		m.observeWriterContentionInContext(ctx, "synthetic-private-kind", ErrCacheWriterBusy{ActiveJobID: "synthetic-private-blocker"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		m.mu.Unlock()
		t.Fatal("writer observation read held job authority")
	}
	m.mu.Unlock()
	requireJobCounter(t, metrics, "writer_contention", map[string]string{"kind": "sync"}, 1)
	requireJobCounter(t, metrics, "writer_contention", map[string]string{"kind": "other"}, 1)
	events := writerObservationPage(t, c, 2)
	for _, event := range events {
		if event.Code != observability.WriterContended || event.JobRef != expectedObservationRef("job", job.ID) || event.CorrelationRef != expectedObservationRef("correlation", c.BootID()+"\x00"+job.ID) || event.CacheRef != expectedObservationRef("cache", job.CacheUUID) || event.RepoRef != expectedObservationRef("repo", job.RepoID) {
			t.Fatal("worker denial lost actual boot/job context")
		}
	}
	data, _ := json.Marshal(events)
	if strings.Contains(string(data), "synthetic-private") {
		t.Fatal("writer typed error or kind bytes reached event output")
	}
}

func TestWriterObservationActualResumeUsesRetainedJobIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	m := NewJobManager(path)
	job, created, err := m.createCoalescedJobWithIntent(RepositoryDocsIndexJobType, "fixture/repository", "", 0, "docs-work", "fixture-cache", "fixture-registration", "", JobRecoveryIntent{SourceRegistrationID: "fixture-source", SourceRegistrationGeneration: 3, ExpectedRevisionSetID: "fixture-set"}, func() {})
	if err != nil || !created {
		t.Fatal("resume fixture admission failed")
	}
	m.finishJob(job.ID, JobStatusFailed, "fixture retry")
	m.markWorkerFinished(job.ID)
	m = NewJobManager(path)
	if m.LoadAndMarkInterrupted() != nil {
		t.Fatal("resume fixture reload failed")
	}
	release, err := m.BeginDirectCacheWriter(job.CacheUUID, "synthetic-private-direct-writer")
	if err != nil {
		t.Fatal("resume direct writer fixture failed")
	}
	defer release()
	c := jobObservationFixture(t)
	m.SetObservation(c)
	// A stale/nonexistent identity must not acquire a writer event scope.
	for _, id := range []string{"synthetic-private-missing", job.ID} {
		if _, resumed, err := m.ResumeRepositoryDocsAdmission(id, job.RegistrationID, "fixture-source", 4, "fixture-set", "docs-work", "", func() {}); err != nil || resumed {
			t.Fatal("stale resume behavior changed")
		}
	}
	_, resumed, err := m.ResumeRepositoryDocsAdmission(job.ID, job.RegistrationID, "fixture-source", 3, "fixture-set", "docs-work", "", func() {})
	var busy ErrCacheWriterBusy
	if resumed || !errors.As(err, &busy) {
		t.Fatal("actual resume writer denial was not reached")
	}
	for _, event := range writerObservationPage(t, c, 1) {
		if event.Code != observability.WriterContended || event.WorkKind != "repository_docs" || event.JobRef != expectedObservationRef("job", job.ID) || event.CorrelationRef != expectedObservationRef("correlation", c.BootID()+"\x00"+job.ID) || event.RegistrationRef != expectedObservationRef("registration", job.RegistrationID) {
			t.Fatal("resumed denial did not use validated retained job authority")
		}
	}
	retained, ok := m.Get(job.ID)
	if !ok || retained.Status != JobStatusFailed {
		t.Fatal("writer observation revived denied work")
	}
}

func TestWriterObservationActualDurableCommitDenial(t *testing.T) {
	root := t.TempDir()
	store, err := cache.NewSQLiteStore(context.Background(), filepath.Join(root, "cache.db"))
	if err != nil {
		t.Fatal("writer cache fixture failed")
	}
	binding := cache.RepositoryBinding{RepoID: "fixture/repository", Owner: "fixture", Name: "repository", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}
	if store.AddRepository(context.Background(), binding) != nil {
		t.Fatal("writer binding fixture failed")
	}
	identity, _ := store.CacheIdentity(context.Background())
	schema, _ := store.SchemaVersion(context.Background())
	if store.Close() != nil {
		t.Fatal("writer fixture close failed")
	}
	m := NewJobManager(filepath.Join(root, "jobs.json"))
	job, _, err := m.createCoalescedJob(SyncJobType, binding.RepoID, "", 0, "sync-work", identity.UUID, maintenanceRegistrationID(identity.UUID, binding.RepoID), "", func() {})
	if err != nil {
		t.Fatal("writer job fixture failed")
	}
	c := jobObservationFixture(t)
	m.SetObservation(c)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx = m.withJobObservation(ctx, job)
	manager := newTestManager(t, "darwin")
	manager.RuntimeDir = root
	calls := 0
	_, _, err = m.runDurableCollection(ctx, manager, job.ID, StartSyncJobRequest{RepoID: job.RepoID, CachePath: filepath.Join(root, "cache.db"), CacheUUID: identity.UUID, RegistrationID: job.RegistrationID}, schema, syncRepositoryBindingFingerprint(binding), durableCollectionWork{
		collection: "issues", remoteType: "issue",
		fetch: func(context.Context) (durableCollectionBatch, error) {
			return durableCollectionBatch{payload: json.RawMessage(`{"items":[]}`), idempotencyKey: "writer-fixture-fetch", fetchedAt: time.Now().UTC()}, nil
		},
		commit: func(context.Context, SyncStageEnvelope) (*service.SyncResourcesResult, error) {
			calls++
			cancel() // stop the existing retry wait, not the confirmed denial
			return nil, cache.ErrLockContention{HolderHint: "synthetic-private-holder"}
		},
	}, nil, 1)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("writer observation changed bounded commit/retry behavior: calls=%d error_type=%T class=%s", calls, err, jobObservationFailure(maintenanceJobErrorClass(err, "operation_failed")))
	}
	for _, event := range writerObservationPage(t, c, 1, 4) {
		if event.Code != observability.WriterContended || event.WorkKind != "sync" || event.JobRef != expectedObservationRef("job", job.ID) || event.CorrelationRef != expectedObservationRef("correlation", c.BootID()+"\x00"+job.ID) {
			t.Fatal("actual durable commit writer denial lost worker context")
		}
	}
}

func TestWriterObservationRejectedContextNeverFallsBackToManager(t *testing.T) {
	m := NewJobManager("")
	bound := jobObservationFixture(t)
	m.SetObservation(bound)
	ctx := m.withJobObservation(context.Background(), Job{ID: "fixture-job"})
	sealJobObservation(t, bound)
	replacement := jobObservationFixture(t)
	m.SetObservation(replacement)
	// A closed/rejected binding is still the boundary's original authority.
	// A replacement collector must not turn rejection into another admission.
	m.observeWriterContentionInContext(ctx, SyncJobType, cache.ErrLockContention{})
	writerObservationPage(t, replacement, 0)
}
