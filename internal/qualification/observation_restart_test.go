package qualification

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/observability"
	"gitcode-mcp/internal/servicectl"
)

// Private stdin carries only this test's temporary coordinates; neither it nor
// raw child output is copied to logs. The response is a bounded finite contract.
type restartInput struct {
	Root     string
	Provider string
}

type restartReady struct {
	Job       servicectl.Job
	Baseline  servicectl.Job
	Boot      string
	FirstPage observability.Page
}

func restartManager(t *testing.T, input restartInput) (servicectl.Manager, isolatedSource) {
	t.Helper()
	src := isolatedSource{root: input.Root, env: map[string]string{config.EnvToken: "synthetic-private-credential"}}
	cfg, err := config.Load(src, config.Overrides{CachePath: filepath.Join(input.Root, "cache.db"), GitCodeBaseURL: input.Provider})
	if err != nil {
		t.Fatal("restart fixture config unavailable")
	}
	cfg.MaxRetries, cfg.RateLimitRPS, cfg.RateLimitBurst = 0, 1000, 1000
	return servicectl.Manager{Source: src, RuntimeDir: input.Root, EffectiveConfig: &cfg}, src
}

// This helper is entered only by an owned test subprocess. Killing it is the
// crash boundary: cancelling its root context would instead persist a normal
// cancellation and remove the pending retry checkpoint.
func TestO5StoppedRetryWorker(t *testing.T) {
	if os.Getenv("GITCODE_MCP_O5_STOPPED_WORKER") != "1" {
		return
	}
	var input restartInput
	if json.NewDecoder(io.LimitReader(os.Stdin, 8192)).Decode(&input) != nil || input.Root == "" || input.Provider == "" {
		t.Fatal("restart fixture input rejected")
	}
	ctx := context.Background()
	manager, _ := restartManager(t, input)
	jobs := servicectl.NewJobManager(filepath.Join(input.Root, "jobs.json"))
	collector := qualifyCollector(t, "retained", filepath.Join(input.Root, "events"))
	jobs.SetObservation(collector)
	jobs.SetMetrics(observability.NewMetrics(collector.BootID(), time.Now().UTC()))
	request := servicectl.StartSyncJobRequest{RepoID: "fixture/repository", CachePath: filepath.Join(input.Root, "cache.db"), Issues: true, MaxPages: 1, MaxRecords: 100, IdempotencyKey: "restart-baseline"}
	baseline, err := jobs.StartSync(ctx, manager, request)
	if err != nil {
		t.Fatal("restart baseline admission unavailable")
	}
	baseline = waitJob(t, jobs, baseline.ID, baseline.CacheUUID)
	if baseline.Status != servicectl.JobStatusSucceeded {
		t.Fatal("restart baseline not confirmed")
	}
	request.IdempotencyKey, request.Pulls = "restart-delayed", true
	job, err := jobs.StartSync(ctx, manager, request)
	if err != nil {
		t.Fatal("restart delayed admission unavailable")
	}
	var active servicectl.Job
	eventually(t, func() bool {
		active, _ = jobs.Get(job.ID)
		issues, pulls := restartCollection(active, "issues"), restartCollection(active, "pulls")
		return issues.Outcome == servicectl.SyncCollectionRetryScheduled && issues.Attempt == 1 && pulls.Outcome == servicectl.SyncCollectionNoChange && pulls.Attempt == 1 && pulls.LastSuccessAt != nil
	})
	issues := restartCollection(active, "issues")
	// Read, never seed or rewrite, the scheduler's ordinary private journal.
	var journal struct {
		Schema      string `json:"schema"`
		Checkpoints []struct {
			JobID      string    `json:"job_id"`
			Collection string    `json:"collection"`
			Attempt    int       `json:"attempt"`
			RetryAt    time.Time `json:"retry_at"`
		} `json:"checkpoints"`
	}
	data, err := os.ReadFile(filepath.Join(input.Root, "sync-collection-retries.json"))
	if err != nil || len(data) > 1<<20 || json.Unmarshal(data, &journal) != nil || journal.Schema != "gitcode-mcp.sync-collection-retries.v1" || len(journal.Checkpoints) != 1 {
		t.Fatal("actual retry checkpoint missing")
	}
	checkpoint := journal.Checkpoints[0]
	if checkpoint.JobID != active.ID || checkpoint.Collection != "issues" || checkpoint.Attempt != 1 || issues.RetryAfter == nil || !checkpoint.RetryAt.Equal(*issues.RetryAfter) || time.Until(checkpoint.RetryAt) < 20*time.Second {
		t.Fatal("actual checkpoint differs from active authority")
	}
	var page observability.Page
	eventually(t, func() bool {
		page, err = collector.Query(observability.Filter{}, "", 1)
		return err == nil && len(page.Events) == 1 && page.HasMore && page.Cursor != ""
	})
	ready, err := json.Marshal(restartReady{active, baseline, collector.BootID(), page})
	if err != nil || len(ready) > 32<<10 {
		t.Fatal("restart fixture readiness exceeds bound")
	}
	if _, err := os.Stdout.Write(append(ready, '\n')); err != nil {
		t.Fatal("restart fixture readiness unavailable")
	}
	// Parent retains stdin until it kills and joins this specific child. EOF
	// without that crash is a failure, never graceful-recovery evidence.
	_, _ = io.Copy(io.Discard, os.Stdin)
	t.Fatal("restart worker was not stopped at the crash boundary")
}

func restartCollection(job servicectl.Job, name string) servicectl.SyncCollectionView {
	for _, collection := range job.SyncCollections {
		if collection.Collection == name {
			return collection
		}
	}
	return servicectl.SyncCollectionView{}
}

func TestO5ActualStoppedWorkerRetryRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	var calls, issueCalls, pullCalls, resumedAt atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet {
			t.Error("restart provider received a write")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v5/repos/fixture/repository/issues":
			ordinal := issueCalls.Add(1)
			if ordinal == 3 {
				resumedAt.Store(time.Now().UTC().UnixNano())
			}
			if ordinal == 2 {
				w.Header().Set("Retry-After", "30")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = io.WriteString(w, `{"message":"synthetic-private-provider-body"}`)
				return
			}
			_, _ = io.WriteString(w, `[{"id":1,"number":1,"title":"Fixture","body":"synthetic-private-source-body","state":"open","comments":0}]`)
		case "/api/v5/repos/fixture/repository/pulls":
			pullCalls.Add(1)
			_, _ = io.WriteString(w, `[]`)
		default:
			t.Error("restart provider received an unexpected operation")
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer provider.Close()
	store, err := cache.NewSQLiteStore(ctx, filepath.Join(root, "cache.db"))
	if err != nil {
		t.Fatal("restart cache unavailable")
	}
	defer store.Close()
	binding := cache.RepositoryBinding{RepoID: "fixture/repository", Owner: "fixture", Name: "repository", APIBaseURL: provider.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}
	if store.AddRepository(ctx, binding) != nil {
		t.Fatal("restart binding unavailable")
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		t.Fatal("restart identity unavailable")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("restart helper unavailable")
	}
	child := exec.Command(executable, "-test.run=^TestO5StoppedRetryWorker$", "-test.timeout=60s")
	child.Env = []string{"GITCODE_MCP_O5_STOPPED_WORKER=1"}
	child.Stderr = io.Discard
	input, err := child.StdinPipe()
	if err != nil {
		t.Fatal("restart private input unavailable")
	}
	output, err := child.StdoutPipe()
	if err != nil {
		_ = input.Close()
		t.Fatal("restart private output unavailable")
	}
	if child.Start() != nil {
		_ = input.Close()
		_ = output.Close()
		t.Fatal("restart helper did not start")
	}
	joined, killed := false, false
	childDone := make(chan error, 1)
	go func() { childDone <- child.Wait() }()
	stopChild := func() {
		if joined {
			return
		}
		killed = child.Process.Kill() == nil
		select {
		case <-childDone:
			joined = true
		case <-time.After(3 * time.Second):
			t.Error("owned restart helper did not join")
		}
		_ = input.Close()
		_ = output.Close()
	}
	defer stopChild()
	readyDone := make(chan []byte, 1)
	readyReaderDone := make(chan struct{})
	go func() {
		defer close(readyReaderDone)
		scanner := bufio.NewScanner(output)
		scanner.Buffer(make([]byte, 4096), 32<<10)
		var data []byte
		if scanner.Scan() {
			data = append([]byte(nil), scanner.Bytes()...)
		}
		readyDone <- data
	}()
	defer func() {
		stopChild()
		select {
		case <-readyReaderDone:
		case <-time.After(time.Second):
			t.Error("restart readiness reader did not join")
		}
	}()
	if json.NewEncoder(input).Encode(restartInput{root, provider.URL}) != nil {
		t.Fatal("restart private input rejected")
	}
	var ready restartReady
	select {
	case data := <-readyDone:
		if json.Unmarshal(data, &ready) != nil {
			t.Fatal("restart helper readiness rejected")
		}
	case <-time.After(10 * time.Second):
		stopChild()
		select {
		case <-readyDone:
		case <-time.After(time.Second):
			t.Error("restart readiness reader did not join")
		}
		t.Fatal("restart helper readiness timed out")
	}
	before := restartCollection(ready.Job, "issues")
	sibling := restartCollection(ready.Job, "pulls")
	baseline := restartCollection(ready.Baseline, "issues")
	if ready.Job.Status != servicectl.JobStatusRunning || ready.Job.CacheUUID != identity.UUID || before.RetryAfter == nil || before.FirstRetryAt == nil || before.LastRetryAt == nil || baseline.LastSuccessAt == nil || calls.Load() != 3 || issueCalls.Load() != 2 || pullCalls.Load() != 1 {
		t.Fatal("restart readiness lost execution/provider authority")
	}
	stopChild()
	if !joined || !killed || child.ProcessState == nil || child.ProcessState.Success() || time.Until(*before.RetryAfter) < 15*time.Second {
		t.Fatal("worker was not joined before its actual retry deadline")
	}
	manager, src := restartManager(t, restartInput{root, provider.URL})
	jobs := servicectl.NewJobManager(filepath.Join(root, "jobs.json"))
	if jobs.LoadAndMarkInterrupted() != nil {
		t.Fatal("fresh job owner could not recover ordinary snapshot")
	}
	interrupted, ok := jobs.Get(ready.Job.ID)
	if !ok || interrupted.Status != servicectl.JobStatusInterrupted || !reflect.DeepEqual(interrupted.SyncCollections, ready.Job.SyncCollections) {
		t.Fatal("actual crash changed retained collection authority")
	}
	collector := qualifyCollector(t, "retained", filepath.Join(root, "events"))
	if collector.BootID() == ready.Boot {
		t.Fatal("actual crash reused volatile boot identity")
	}
	metrics := observability.NewMetrics(collector.BootID(), time.Now().UTC())
	jobs.SetObservation(collector)
	jobs.SetMetrics(metrics)
	defer func() {
		cancel()
		eventually(t, func() bool {
			release, active := jobs.BeginCacheMutationFence(identity.UUID)
			release()
			return len(active) == 0
		})
	}()
	maintenance := servicectl.NewMaintenanceManager(manager, jobs, "")
	reader := &observability.Reader{Collector: collector, Metrics: metrics, Health: maintenance.ExecutionHealthContext}
	refs := observability.ProjectEntityRefs(observability.Context{Job: observability.KnownRef("job", ready.Job.ID), Cache: observability.KnownRef("cache", identity.UUID), Repo: observability.KnownRef("repo", binding.RepoID)})
	var page observability.Page
	eventually(t, func() bool {
		page, err = collector.Query(observability.Filter{}, "", 50)
		if err != nil {
			return false
		}
		for _, event := range page.Events {
			if event.Code == observability.PriorBootUnclean && event.BootID == collector.BootID() {
				return true
			}
		}
		return false
	})
	_, err = retryObservationRead(ctx, func() (observability.Page, error) {
		return collector.Query(observability.Filter{}, ready.FirstPage.Cursor, 50)
	})
	var cursorError observability.QueryError
	if !errors.As(err, &cursorError) || cursorError.Code != "snapshot_required" || cursorError.Reason != "generation_or_loss_changed" {
		t.Fatal("unclean restart lost its exact cursor discontinuity")
	}
	assertRestartIncident(t, mustActiveSummary(t, ctx, reader), "interrupted", "interrupted", refs.Job, nil, baseline.LastSuccessAt)
	assertFreshRetryOrigin(t, ctx, reader)
	parity := func(job servicectl.Job) {
		at := time.Now().UTC()
		reader.Now = func() time.Time { return at }
		assertTransportParity(t, reader, src, &calls, false, job, "", root, provider.URL, identity.UUID, binding.RepoID)
		reader.Now = nil
	}
	parity(interrupted)
	if jobs.RecoverSyncStages(ctx, manager) != nil {
		t.Fatal("ordinary pending retry recovery unavailable")
	}
	eventually(t, func() bool {
		page, err = collector.Query(observability.Filter{}, "", 50)
		if err != nil {
			return false
		}
		for _, event := range page.Events {
			if event.Code == observability.JobRecovered && event.JobRef == refs.Job && event.BootID == collector.BootID() {
				return event.Outcome == "resumed" && event.CacheRef == refs.Cache && event.RepoRef == refs.Repo && event.CorrelationRef != ""
			}
		}
		return false
	})
	// Recovery publication precedes scheduler entry. Attachment is best effort:
	// a lost attachment must stay unavailable, never become a saved-state zero.
	var metricBoundary string
	eventually(t, func() bool {
		snapshot, err := retryObservationRead(ctx, func() (observability.MetricsSnapshot, error) {
			return reader.MetricSnapshot(ctx, observability.MetricsRequest{Name: "retry_admissions", HistoryMinutes: 1})
		})
		if err != nil {
			return false
		}
		metricBoundary = restartRetryMetricBoundary(snapshot, 0, 0)
		return metricBoundary != ""
	})
	t.Logf("new-boot retry metrics: %s", metricBoundary)
	active, _ := jobs.Get(ready.Job.ID)
	if active.Status != servicectl.JobStatusRunning || active.FinishedAt != nil || !reflect.DeepEqual(restartCollection(active, "issues"), before) || !reflect.DeepEqual(restartCollection(active, "pulls"), sibling) || calls.Load() != 3 {
		t.Fatal("recovery replayed work or rewrote the provider-directed deadline")
	}
	assertRestartIncident(t, mustActiveSummary(t, ctx, reader), "retrying", "retry_wait", refs.Job, before.RetryAfter, baseline.LastSuccessAt)
	beforeAdmissionMetrics := assertRestartRetryMetric(t, ctx, reader, 0, 0)
	parity(active)
	finished := waitJobWithin(t, jobs, ready.Job.ID, identity.UUID, 40*time.Second)
	view := restartCollection(finished, "issues")
	if finished.Status != servicectl.JobStatusSucceeded || view.Attempt != 2 || view.RetryAfter != nil || view.LastSuccessAt == nil || !view.LastSuccessAt.After(*baseline.LastSuccessAt) || !reflect.DeepEqual(view.FirstRetryAt, before.FirstRetryAt) || !reflect.DeepEqual(view.LastRetryAt, before.LastRetryAt) || !reflect.DeepEqual(restartCollection(finished, "pulls"), sibling) || calls.Load() != 4 || issueCalls.Load() != 3 || pullCalls.Load() != 1 || resumedAt.Load() < before.RetryAfter.UnixNano() || len(jobs.List()) != 2 {
		t.Fatal("actual resumed retry lost conservation or replayed successful sibling")
	}
	assertRestartIncident(t, mustActiveSummary(t, ctx, reader), "recovered", "retry_wait", refs.Job, nil, view.LastSuccessAt)
	assertRestartRetryMetric(t, ctx, reader, 1, beforeAdmissionMetrics.Dropped)
	eventually(t, func() bool {
		page, err = collector.Query(observability.Filter{}, "", 50)
		if err != nil {
			return false
		}
		admitted, terminal := 0, 0
		for _, event := range page.Events {
			if event.JobRef != refs.Job || event.BootID != collector.BootID() {
				continue
			}
			if event.Code == observability.JobAdmitted {
				t.Fatal("saved state fabricated a fresh job admission")
			}
			if event.Code == observability.RetryAdmitted {
				if event.Collection != "issues" || event.Attempt != 2 || event.CacheRef != refs.Cache || event.RepoRef != refs.Repo || event.CorrelationRef == "" {
					t.Fatal("actual resumed attempt lost causal identity")
				}
				admitted++
			}
			if event.Code == observability.JobTerminal && event.Outcome == "succeeded" {
				if event.CacheRef != refs.Cache || event.RepoRef != refs.Repo || event.CorrelationRef == "" {
					t.Fatal("actual resumed terminal lost causal identity")
				}
				terminal++
			}
		}
		return admitted == 1 && terminal == 1
	})
	parity(finished)
	if jobs.RecoverSyncStages(ctx, manager) != nil || calls.Load() != 4 {
		t.Fatal("replaying terminal recovery contacted provider")
	}
	cached, err := store.GetSourceScoped(ctx, binding.RepoID, "ISSUE-1")
	if err != nil || cached.Body != "synthetic-private-source-body" {
		t.Fatal("actual stopped-worker recovery changed cached source truth")
	}
}

// Metrics are not execution receipts. Complete counters are strict; a missing
// origin or undercount is accepted only with explicit, sufficient NEW aggregate
// loss. This is not attribution to a named counter. The
// actual job, provider, checkpoint and causal event assertions remain separate.
func restartRetryMetricBoundary(snapshot observability.MetricsSnapshot, want float64, beforeLoss uint64) string {
	if snapshot.Dropped < beforeLoss || snapshot.State != "initializing" && snapshot.State != "ready" && snapshot.State != "partial" || snapshot.Dropped > 0 && snapshot.State == "ready" {
		return ""
	}
	for _, series := range snapshot.Series {
		if series.Name != "retry_admissions" || series.Labels["collection"] != "issues" {
			continue
		}
		if series.Value == nil {
			required := uint64(want)
			if required == 0 {
				required = 1 // Initial attachment loss, not a fabricated saved zero.
			}
			if series.Reason == "origin_unavailable" && snapshot.Dropped-beforeLoss >= required {
				return "explicit_origin_loss"
			}
			return ""
		}
		value := *series.Value
		if series.Reason != "" || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > want || math.Trunc(value) != value {
			return ""
		}
		if value == want {
			return "observed_count"
		}
		if want-value <= float64(snapshot.Dropped-beforeLoss) {
			return "explicit_recording_loss"
		}
		return ""
	}
	return ""
}

func assertRestartRetryMetric(t *testing.T, ctx context.Context, reader *observability.Reader, want float64, beforeLoss uint64) observability.MetricsSnapshot {
	t.Helper()
	snapshot, err := retryObservationRead(ctx, func() (observability.MetricsSnapshot, error) {
		return reader.MetricSnapshot(ctx, observability.MetricsRequest{Name: "retry_admissions", HistoryMinutes: 1})
	})
	if err != nil || restartRetryMetricBoundary(snapshot, want, beforeLoss) == "" {
		t.Fatal("new-boot retry counter lost exact count or explicit loss boundary")
	}
	t.Logf("retry metric boundary: %s", restartRetryMetricBoundary(snapshot, want, beforeLoss))
	return snapshot
}

func TestO5RestartRetryMetricBoundaryRejectsInventedCounts(t *testing.T) {
	zero, one, two, negative, fraction, nan := float64(0), float64(1), float64(2), float64(-1), 0.5, math.NaN()
	for _, test := range []struct {
		name, state, reason, wantBoundary string
		dropped                           uint64
		value                             *float64
		want                              float64
	}{
		{"attached_zero", "initializing", "", "observed_count", 0, &zero, 0},
		{"admitted_one", "initializing", "", "observed_count", 0, &one, 1},
		{"lost_origin", "initializing", "origin_unavailable", "explicit_origin_loss", 1, nil, 0},
		{"lost_recording", "partial", "", "explicit_recording_loss", 1, &zero, 1},
		{"unreported_missing_origin", "initializing", "origin_unavailable", "", 0, nil, 0},
		{"unreported_undercount", "initializing", "", "", 0, &zero, 1},
		{"lost_wrong_origin", "partial", "probe_unavailable", "", 1, nil, 1},
		{"invented_admission", "partial", "", "", 1, &one, 0},
		{"duplicate_admission", "partial", "", "", 1, &two, 1},
		{"negative", "partial", "", "", 1, &negative, 1},
		{"fraction", "partial", "", "", 1, &fraction, 1},
		{"nan", "partial", "", "", 1, &nan, 1},
		{"ready_hides_loss", "ready", "origin_unavailable", "", 1, nil, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			snapshot := observability.MetricsSnapshot{State: test.state, Dropped: test.dropped, Series: []observability.MetricSeries{{MetricPoint: observability.MetricPoint{Name: "retry_admissions", Labels: map[string]string{"collection": "issues"}, Value: test.value, Reason: test.reason}}}}
			if restartRetryMetricBoundary(snapshot, test.want, 0) != test.wantBoundary {
				t.Fatal("retry metric boundary accepted invented count or hid explicit loss")
			}
		})
	}
	for _, test := range []struct {
		name          string
		value         *float64
		before, after uint64
		wantBoundary  string
	}{
		{"stale_loss_cannot_excuse_undercount", &zero, 1, 1, ""},
		{"stale_loss_cannot_excuse_missing_origin", nil, 1, 1, ""},
		{"new_aggregate_loss_with_undercount", &zero, 1, 2, "explicit_recording_loss"},
		{"new_aggregate_loss_with_missing_origin", nil, 1, 2, "explicit_origin_loss"},
		{"counter_exact_despite_old_loss", &one, 1, 1, "observed_count"},
		{"loss_regressed", &one, 2, 1, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			series := observability.MetricSeries{MetricPoint: observability.MetricPoint{Name: "retry_admissions", Labels: map[string]string{"collection": "issues"}, Value: test.value}}
			if test.value == nil {
				series.Reason = "origin_unavailable"
			}
			snapshot := observability.MetricsSnapshot{State: "partial", Dropped: test.after, Series: []observability.MetricSeries{series}}
			if restartRetryMetricBoundary(snapshot, 1, test.before) != test.wantBoundary {
				t.Fatal("old loss excused new admission deficit or aggregate loss boundary was erased")
			}
		})
	}
}

func assertRestartIncident(t *testing.T, summary observability.Summary, state, reason, job string, retry, success *time.Time) {
	t.Helper()
	want := "degraded"
	if state == "recovered" {
		want = "healthy"
	}
	if summary.ExecutionHealth.State != want || summary.ExecutionHealth.Source != "durable_jobs_and_maintenance" || summary.MetricsBootID != summary.Collector.BootID {
		t.Fatal("restart diagnosis replaced execution or boot authority")
	}
	for _, incident := range summary.ExecutionHealth.Incidents {
		if incident.Collection == "issues" && incident.JobRef == job {
			if incident.State != state || incident.Reason != reason || incident.LastSuccessAt == nil || success == nil || !incident.LastSuccessAt.Equal(*success) || (retry == nil) != (incident.RetryAfter == nil) || retry != nil && !incident.RetryAfter.Equal(*retry) {
				t.Fatal("restart diagnosis lost interrupted/retry/last-success authority")
			}
			return
		}
	}
	t.Fatal("restart incident missing")
}

func assertFreshRetryOrigin(t *testing.T, ctx context.Context, reader *observability.Reader) {
	t.Helper()
	metrics, err := retryObservationRead(ctx, func() (observability.MetricsSnapshot, error) {
		return reader.MetricSnapshot(ctx, observability.MetricsRequest{Name: "retry_admissions", HistoryMinutes: 1})
	})
	if err != nil {
		t.Fatal("fresh retry origin unavailable")
	}
	for _, series := range metrics.Series {
		if series.Name == "retry_admissions" && series.Labels["collection"] == "issues" {
			if series.Value != nil || series.Reason != "origin_unavailable" {
				t.Fatal("saved retry state fabricated an attached origin or counter")
			}
			return
		}
	}
	t.Fatal("fresh retry catalog missing")
}
