package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/observability"
)

func testJobMetrics(t *testing.T, path string) (*JobManager, *observability.Metrics) {
	t.Helper()
	m := NewJobManager(path)
	metrics := observability.NewMetrics(strings.Repeat("a", 32), time.Now().UTC())
	m.SetMetrics(metrics)
	return m, metrics
}

func jobMetric(t *testing.T, metrics *observability.Metrics, name string, labels map[string]string) observability.MetricSeries {
	t.Helper()
	for _, series := range metrics.Snapshot().Series {
		if series.Name != name || len(series.Labels) != len(labels) {
			continue
		}
		match := true
		for key, value := range labels {
			match = match && series.Labels[key] == value
		}
		if match {
			return series
		}
	}
	t.Fatal("fixed metric series missing")
	return observability.MetricSeries{}
}

func requireJobCounter(t *testing.T, metrics *observability.Metrics, name string, labels map[string]string, want float64) {
	t.Helper()
	series := jobMetric(t, metrics, name, labels)
	if series.Value == nil || *series.Value != want {
		t.Fatal("job observation counter differs")
	}
}

func TestJobMetricsAdmissionCoalescingAndTerminalTransition(t *testing.T) {
	m, metrics := testJobMetrics(t, "")
	job, created, err := m.createCoalescedJob(SyncJobType, "fixture/repository", "", 1, "work-one", "cache-one", "reg-one", "", func() {})
	if err != nil || !created {
		t.Fatal("fixture admission failed")
	}
	replay, created, err := m.createCoalescedJob(SyncJobType, "fixture/repository", "", 1, "work-one", "cache-one", "reg-one", "", func() {})
	if err != nil || created || replay.ID != job.ID {
		t.Fatal("fixture coalescing changed")
	}
	requireJobCounter(t, metrics, "jobs_admitted", map[string]string{"kind": "sync"}, 1)
	start := time.Now().UTC()
	m.updateJob(job.ID, func(job *Job, _ time.Time) { job.Status, job.StartedAt = JobStatusRunning, &start })
	m.updateJob(job.ID, func(job *Job, _ time.Time) { job.Completed++ })
	finish := start.Add(3 * time.Second)
	m.updateJob(job.ID, func(job *Job, _ time.Time) { job.Status, job.FinishedAt = JobStatusSucceeded, &finish })
	m.updateJob(job.ID, func(job *Job, _ time.Time) { job.Completed++ })
	m.finishJob(job.ID, JobStatusSucceeded, "")
	requireJobCounter(t, metrics, "jobs_terminal", map[string]string{"kind": "sync", "outcome": "succeeded"}, 1)
	duration := jobMetric(t, metrics, "sync_duration", map[string]string{"outcome": "succeeded"})
	if duration.Count != 1 || duration.Sum != 3 {
		t.Fatal("duration counted progress or repeated terminalization")
	}
	// Throughput is unavailable until its separate confirmed-commit hook exists.
	if jobMetric(t, metrics, "sync_committed", map[string]string{"collection": "issues"}).Value != nil {
		t.Fatal("attaching job duration fabricated throughput availability")
	}
}

func TestJobMetricsPersistenceFailureAndRetry(t *testing.T) {
	m, metrics := testJobMetrics(t, "")
	failure := errors.New("synthetic unavailable storage")
	m.writeFile = func(string, []byte, os.FileMode) error { return failure }
	m.snapshotPath = filepath.Join(t.TempDir(), "jobs.json")
	if _, created, err := m.createCoalescedJob(SyncJobType, "fixture/repository", "", 1, "work", "cache", "reg", "", func() {}); created || !errors.Is(err, ErrJobAdmissionPersistence) {
		t.Fatal("admission failure behavior changed")
	}
	requireJobCounter(t, metrics, "jobs_admitted", map[string]string{"kind": "sync"}, 0)
	m.writeFile = durableAtomicWriteFile
	job, _, err := m.createCoalescedJob(SyncJobType, "fixture/repository", "", 1, "work", "cache", "reg", "", func() {})
	if err != nil {
		t.Fatal("fixture retry admission failed")
	}
	m.writeFile = func(string, []byte, os.FileMode) error { return failure }
	terminal := func(job *Job, now time.Time) { job.Status, job.FinishedAt = JobStatusFailed, &now }
	if err := m.updateJobPersisted(job.ID, terminal); !errors.Is(err, failure) {
		t.Fatal("terminal persistence failure behavior changed")
	}
	requireJobCounter(t, metrics, "jobs_terminal", map[string]string{"kind": "sync", "outcome": "failed"}, 0)
	if metrics.Snapshot().Dropped != 1 {
		t.Fatal("unconfirmed terminal observation loss hidden")
	}
	stored, _ := m.Get(job.ID)
	if stored.Status != JobStatusQueued {
		t.Fatal("observation altered rollback")
	}
	m.writeFile = durableAtomicWriteFile
	if err := m.updateJobPersisted(job.ID, terminal); err != nil {
		t.Fatal("fixture terminal persistence retry failed")
	}
	requireJobCounter(t, metrics, "jobs_terminal", map[string]string{"kind": "sync", "outcome": "failed"}, 1)
}

func TestJobMetricsRestartAndRecoveryAreNotExecution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	m, _ := testJobMetrics(t, path)
	job := m.createJob(SyncJobType, 1, func() {})
	m.updateJob(job.ID, func(job *Job, now time.Time) { job.Status, job.StartedAt = JobStatusRunning, &now })
	restarted, metrics := testJobMetrics(t, path)
	if err := restarted.LoadAndMarkInterrupted(); err != nil {
		t.Fatal("fixture restart failed")
	}
	if err := restarted.updateJobPersisted(job.ID, func(job *Job, now time.Time) { job.Status, job.FinishedAt = JobStatusSucceeded, &now }); err != nil {
		t.Fatal("fixture recovery projection failed")
	}
	requireJobCounter(t, metrics, "jobs_admitted", map[string]string{"kind": "sync"}, 0)
	for _, outcome := range []string{"interrupted", "succeeded"} {
		requireJobCounter(t, metrics, "jobs_terminal", map[string]string{"kind": "sync", "outcome": outcome}, 0)
	}
	if jobMetric(t, metrics, "sync_duration", map[string]string{"outcome": "succeeded"}).Count != 0 {
		t.Fatal("recovery fabricated execution duration")
	}
	probes := restarted.MetricProbes()
	if probes["jobs_queued"].Value == nil || *probes["jobs_queued"].Value != 0 || probes["jobs_active"].Value == nil || *probes["jobs_active"].Value != 0 {
		t.Fatal("durable terminal projection counted as active execution")
	}
}

func TestJobMetricsRepositoryDocsCancellationAndGenerationFence(t *testing.T) {
	m, metrics := testJobMetrics(t, "")
	cancelled := 0
	job, _, err := m.createCoalescedJobWithIntent(RepositoryDocsIndexJobType, "fixture/repository", "", 1, "docs-work", "cache-one", "reg-one", "", JobRecoveryIntent{SourceRegistrationID: "source-one", SourceRegistrationGeneration: 1}, func() { cancelled++ })
	if err != nil {
		t.Fatal("fixture documentation admission failed")
	}
	if _, found, err := m.Cancel(job.ID); !found || err != nil {
		t.Fatal("fixture durable cancellation failed")
	}
	if _, found, err := m.Cancel(job.ID); !found || err != nil || cancelled != 1 {
		t.Fatal("fixture cancellation replay changed")
	}
	requireJobCounter(t, metrics, "jobs_terminal", map[string]string{"kind": "repository_docs", "outcome": "cancelled"}, 1)
	m.markWorkerFinished(job.ID)
	_, _, err = m.createCoalescedJobWithIntent(RepositoryDocsIndexJobType, "fixture/repository", "", 1, "docs-next", "cache-one", "reg-one", "", JobRecoveryIntent{SourceRegistrationID: "source-one", SourceRegistrationGeneration: 2}, func() {})
	if err != nil {
		t.Fatal("fixture next generation admission failed")
	}
	m.FenceRepositoryDocsSourceGeneration("reg-one", "source-one", 2)
	m.FenceRepositoryDocsSourceGeneration("reg-one", "source-one", 2)
	requireJobCounter(t, metrics, "jobs_terminal", map[string]string{"kind": "repository_docs", "outcome": "superseded"}, 1)
}

func TestJobMetricsProbesAreNonblockingAndFinite(t *testing.T) {
	m, metrics := testJobMetrics(t, "")
	job := m.createJob("synthetic-private-kind", 1, func() {})
	m.mu.Lock()
	done := make(chan map[string]observability.ProbeValue, 1)
	go func() { done <- m.MetricProbes() }()
	select {
	case values := <-done:
		for _, v := range values {
			if v.Value != nil || v.Reason != "probe_unavailable" {
				t.Fatal("busy job authority returned fabricated probe")
			}
		}
	case <-time.After(time.Second):
		m.mu.Unlock()
		t.Fatal("sampler waited on execution lock")
	}
	m.syncCommitQueues["synthetic-private-cache"] = []syncCommitWaiter{{stageID: "a"}, {stageID: "b"}, {stageID: "c"}}
	m.mu.Unlock()
	values := m.MetricProbes()
	if *values["jobs_queued"].Value != 1 || *values["jobs_active"].Value != 0 || *values["writer_waiters"].Value != 2 {
		t.Fatal("queue gauge counted active turn as a waiter")
	}
	m.finishJob(job.ID, JobStatusCancelled, "synthetic-private-error")
	requireJobCounter(t, metrics, "jobs_terminal", map[string]string{"kind": "other", "outcome": "cancelled"}, 1)
	data, err := json.Marshal(metrics.Snapshot())
	if err != nil {
		t.Fatal("fixture metric serialization failed")
	}
	for _, private := range []string{"synthetic-private-kind", "synthetic-private-cache", "synthetic-private-error", job.ID} {
		if strings.Contains(string(data), private) {
			t.Fatal("job authority leaked into metric dimensions")
		}
	}
}

func TestJobMetricsConcurrentTerminalizationCountsOnce(t *testing.T) {
	m, metrics := testJobMetrics(t, "")
	job := m.createJob(RAGIndexJobType, 1, func() {})
	m.updateJob(job.ID, func(job *Job, now time.Time) { job.Status, job.StartedAt = JobStatusRunning, &now })
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); m.finishJob(job.ID, JobStatusSucceeded, "") }()
	}
	wg.Wait()
	requireJobCounter(t, metrics, "jobs_terminal", map[string]string{"kind": "rag", "outcome": "succeeded"}, 1)
	if jobMetric(t, metrics, "rag_duration", map[string]string{"outcome": "succeeded"}).Count != 1 {
		t.Fatal("concurrent terminal updates counted duplicate duration")
	}
}

func TestJobMetricsExecutionSinkIsExplicitAndPreservesContext(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	unobserved := NewJobManager("")
	if unobserved.withJobMetrics(parent) != parent {
		t.Fatal("unobserved manager changed execution context")
	}
	manager, metrics := testJobMetrics(t, "")
	ctx := manager.withJobMetrics(parent)
	if observability.MetricsFromContext(ctx) != metrics {
		t.Fatal("daemon execution lost its concrete metric sink")
	}
	cancel()
	if ctx.Err() != context.Canceled {
		t.Fatal("metric decoration detached execution cancellation")
	}
}
