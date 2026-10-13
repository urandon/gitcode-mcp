// Package qualification joins real execution owners and public transports.
// All providers, credentials, stores and service endpoints are isolated fixtures.
package qualification

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/adminhttp"
	"gitcode-mcp/internal/adminui"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/cli"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/mcp"
	"gitcode-mcp/internal/observability"
	"gitcode-mcp/internal/servicectl"
)

type isolatedSource struct {
	root string
	env  map[string]string
}

func (s isolatedSource) Env(k string) string             { return s.env[k] }
func (s isolatedSource) UserHomeDir() (string, error)    { return s.root, nil }
func (s isolatedSource) UserConfigDir() (string, error)  { return s.root, nil }
func (s isolatedSource) UserCacheDir() (string, error)   { return s.root, nil }
func (s isolatedSource) WorkingDir() (string, error)     { return s.root, nil }
func (s isolatedSource) ReadFile(string) ([]byte, error) { return nil, os.ErrNotExist }

func eventually(t *testing.T, predicate func() bool) {
	t.Helper()
	eventuallyWithin(t, 5*time.Second, predicate)
}

func eventuallyWithin(t *testing.T, budget time.Duration, predicate func() bool) {
	t.Helper()
	deadline := time.NewTimer(budget)
	defer deadline.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for !predicate() {
		select {
		case <-tick.C:
		case <-deadline.C:
			t.Fatal("bounded qualification condition was not reached")
		}
	}
}

func closeCollector(t *testing.T, c *observability.Collector) {
	t.Helper()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !c.Close(ctx, true) {
		t.Fatal("qualification collector did not join")
	}
}

func qualifyCollector(t *testing.T, mode, dir string) *observability.Collector {
	t.Helper()
	if mode == "disabled" {
		return nil
	}
	if mode == "storage_unavailable" {
		if err := os.WriteFile(dir, []byte("fixture"), 0600); err != nil {
			t.Fatal("storage fault fixture unavailable")
		}
	}
	c := observability.NewQualificationCollector(observability.Config{Directory: dir, ManagedOutput: true})
	t.Cleanup(func() { closeCollector(t, c) })
	// Coverage is loaded before the fresh Boot append. A same-ledger restart
	// can therefore be ready/partial while startup still changes the snapshot.
	// Join the real serial writer's startup boundary before comparing DTOs;
	// this does not stop execution, fabricate events or retry their admission.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resume, err := c.PauseWriterForQualification(ctx)
	if err != nil || resume == nil || !resume() {
		t.Fatal("qualification collector startup did not reach its owned writer boundary")
	}
	if mode == "storage_unavailable" && c.Coverage().State != "partial" {
		t.Fatal("actual storage fault was not exercised")
	}
	if mode == "sealed" {
		closeCollector(t, c)
	}
	return c
}

func waitJob(t *testing.T, jobs *servicectl.JobManager, id, cacheID string) servicectl.Job {
	return waitJobWithin(t, jobs, id, cacheID, 5*time.Second)
}

func waitJobWithin(t *testing.T, jobs *servicectl.JobManager, id, cacheID string, budget time.Duration) servicectl.Job {
	t.Helper()
	var out servicectl.Job
	eventuallyWithin(t, budget, func() bool {
		var ok bool
		out, ok = jobs.Get(id)
		if !ok || out.FinishedAt == nil {
			return false
		}
		// A terminal snapshot alone is not worker exit. The owner's fence
		// also accounts for the worker's final unwind before durable reload.
		release, active := jobs.BeginCacheMutationFence(cacheID)
		release()
		return len(active) == 0
	})
	return out
}

func TestO5ActualFailureWriterWaitRecoveryAcrossTransports(t *testing.T) {
	for _, tc := range []struct {
		name, mode, class, reason, terminal string
		status, attempts                    int
	}{
		{"retained", "retained", "live_auth_failure", "provider_forbidden", "forbidden", http.StatusForbidden, 1},
		{"unauthorized", "retained", "live_auth_failure", "provider_forbidden", "forbidden", http.StatusUnauthorized, 1},
		{"disabled", "disabled", "live_auth_failure", "provider_forbidden", "forbidden", http.StatusForbidden, 1},
		{"storage_unavailable", "storage_unavailable", "live_auth_failure", "provider_forbidden", "forbidden", http.StatusForbidden, 1},
		{"sealed", "sealed", "live_auth_failure", "provider_forbidden", "forbidden", http.StatusForbidden, 1},
		{"timeout", "retained", "network_timeout", "provider_timeout", "timeout", 0, 4},
		{"partial", "retained", "partial_response", "provider_invalid", "invalid", http.StatusOK, 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mode := tc.mode
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			root := t.TempDir()
			var calls atomic.Int64
			var forbidden atomic.Bool
			var recoveryBody atomic.Bool
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "GET" || r.URL.Path != "/api/v5/repos/fixture/repository/issues" {
					t.Error("unexpected provider operation")
					w.WriteHeader(400)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if forbidden.Load() {
					if tc.name == "timeout" {
						<-r.Context().Done()
						return
					}
					if tc.name == "partial" {
						_, _ = io.WriteString(w, `[{`)
						return
					}
					w.WriteHeader(tc.status)
					_, _ = io.WriteString(w, `{"message":"synthetic-private-provider-body"}`)
					return
				}
				if recoveryBody.Load() {
					_, _ = io.WriteString(w, `[{"id":1,"number":1,"title":"Recovered fixture","body":"synthetic-private-recovery-body","state":"open","comments":0}]`)
				} else {
					_, _ = io.WriteString(w, `[{"id":1,"number":1,"title":"Fixture","body":"synthetic-private-source-body","state":"open","comments":0}]`)
				}
			}))
			defer provider.Close()
			cachePath := filepath.Join(root, "cache.db")
			store, err := cache.NewSQLiteStore(ctx, cachePath)
			if err != nil {
				t.Fatal("qualification cache unavailable")
			}
			defer store.Close()
			binding := cache.RepositoryBinding{RepoID: "fixture/repository", Owner: "fixture", Name: "repository", APIBaseURL: provider.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}
			if store.AddRepository(ctx, binding) != nil {
				t.Fatal("qualification binding unavailable")
			}
			identity, err := store.CacheIdentity(ctx)
			if err != nil {
				t.Fatal("qualification cache identity unavailable")
			}
			src := isolatedSource{root: root, env: map[string]string{config.EnvToken: "synthetic-private-credential"}}
			cfg, err := config.Load(src, config.Overrides{CachePath: cachePath, GitCodeBaseURL: provider.URL})
			if err != nil {
				t.Fatal("isolated config unavailable")
			}
			cfg.MaxRetries, cfg.RateLimitRPS, cfg.RateLimitBurst = 0, 1000, 1000
			if tc.name == "timeout" {
				cfg.DefaultTimeout = 250 * time.Millisecond
			}
			manager := servicectl.Manager{Source: src, RuntimeDir: root, EffectiveConfig: &cfg}
			jobsPath := filepath.Join(root, "jobs.json")
			jobs := servicectl.NewJobManager(jobsPath)
			collector := qualifyCollector(t, mode, filepath.Join(root, "events"))
			jobs.SetObservation(collector)
			var metrics *observability.Metrics
			if collector != nil {
				metrics = observability.NewMetrics(collector.BootID(), time.Now().UTC())
				jobs.SetMetrics(metrics)
			}
			request := servicectl.StartSyncJobRequest{RepoID: binding.RepoID, CachePath: cachePath, Issues: true, MaxPages: 1, MaxRecords: 100}
			start := func(key string) servicectl.Job {
				request.IdempotencyKey = key
				job, err := jobs.StartSync(ctx, manager, request)
				if err != nil {
					t.Fatal("actual sync admission failed")
				}
				return job
			}
			first := start("qualification-first")
			first = waitJob(t, jobs, first.ID, identity.UUID)
			if first.Status != servicectl.JobStatusSucceeded || calls.Load() != 1 {
				t.Fatal("baseline confirmed sync failed")
			}
			var evidenceBaseline observability.Coverage
			if collector != nil {
				evidenceBaseline = collector.Coverage()
			}
			forbidden.Store(true)
			failed := start("qualification-failed")
			budget := 5 * time.Second
			if tc.attempts > 1 {
				// Real durable retries wait 1+2+4 seconds plus bounded jitter;
				// only this cohort needs a longer terminal join deadline.
				budget = 20 * time.Second
			}
			failed = waitJobWithin(t, jobs, failed.ID, identity.UUID, budget)
			failedCalls := int64(1 + tc.attempts)
			if failed.Status != servicectl.JobStatusFailed || failed.ErrorClass != tc.class || calls.Load() != failedCalls {
				t.Fatalf("actual provider failure was not isolated: status=%s class=%s calls=%d", failed.Status, failed.ErrorClass, calls.Load())
			}
			if tc.attempts > 1 {
				// HTTP call count alone could mistake adapter retries for the
				// durable scheduler. Check the real persisted attempt/budget and
				// each intermediate scheduled deadline as positive controls.
				settled := false
				for _, collection := range failed.SyncCollections {
					if collection.Collection == "issues" && collection.Attempt == tc.attempts && collection.RetryBudget == tc.attempts && collection.Outcome == servicectl.SyncCollectionPermanentFailure && collection.ErrorClass == tc.class && collection.RetryAfter == nil {
						settled = true
					}
				}
				retries := map[int]time.Time{}
				for _, progress := range failed.Progress {
					if progress.Type != "retry_scheduled" || progress.Collection != "issues" {
						continue
					}
					due, err := time.Parse(time.RFC3339Nano, progress.RetryAfter)
					if err != nil || progress.Attempt < 1 || progress.Attempt >= tc.attempts || !due.After(*first.FinishedAt) {
						t.Fatal("durable retry lost its bounded deadline or attempt identity")
					}
					if _, duplicate := retries[progress.Attempt]; duplicate {
						t.Fatal("one durable retry was counted twice")
					}
					retries[progress.Attempt] = due
				}
				if !settled || len(retries) != tc.attempts-1 {
					t.Fatal("actual durable retry budget or scheduled history missing")
				}
				for attempt := 2; attempt < tc.attempts; attempt++ {
					if !retries[attempt].After(retries[attempt-1]) {
						t.Fatal("durable retry deadlines did not advance")
					}
				}
			}
			closeCollector(t, collector)
			var previousPage observability.Page
			var previousBoot string
			if mode == "retained" {
				previousBoot = collector.BootID()
				previousPage, err = collector.Query(observability.Filter{}, "", 1)
				if err != nil || len(previousPage.Events) != 1 || !previousPage.HasMore {
					t.Fatal("retained pre-restart cursor unavailable")
				}
			}
			maintenance := servicectl.NewMaintenanceManager(manager, jobs, "")
			at := *failed.FinishedAt
			reader := &observability.Reader{Collector: collector, Metrics: metrics, Health: maintenance.ExecutionHealthContext, Now: func() time.Time { return at }}
			health := maintenance.ExecutionHealth(at)
			if health.State != "degraded" || health.Active == 0 {
				t.Fatal("actual provider failure became healthy")
			}
			found := false
			for _, incident := range health.Incidents {
				if incident.Collection == "issues" && incident.Reason == tc.reason && incident.LastSuccessAt != nil {
					found = true
				}
			}
			if !found {
				t.Fatal("failure diagnosis lost provider cause or prior confirmed collection success")
			}
			assertTransportParitySince(t, reader, src, &calls, mode == "retained", evidenceBaseline, failed, tc.terminal, root, provider.URL, identity.UUID, binding.RepoID)
			// New job owner reloads the durable snapshot, not event-derived state.
			jobs = servicectl.NewJobManager(jobsPath)
			if jobs.LoadAndMarkInterrupted() != nil {
				t.Fatal("durable job reload failed")
			}
			maintenance = servicectl.NewMaintenanceManager(manager, jobs, "")
			if !reflect.DeepEqual(health, maintenance.ExecutionHealth(at)) {
				t.Fatal("restart changed retained failure authority")
			}
			reader.Health = maintenance.ExecutionHealthContext
			assertTransportParitySince(t, reader, src, &calls, mode == "retained", evidenceBaseline, failed, tc.terminal, root, provider.URL, identity.UUID, binding.RepoID)
			collector = qualifyCollector(t, mode, filepath.Join(root, "events"))
			if mode == "retained" {
				if collector.BootID() == previousBoot {
					t.Fatal("ledger restart reused volatile boot identity")
				}
				page, err := collector.Query(observability.Filter{}, "", 50)
				if err != nil || len(page.Events) == 0 || page.Events[0].EventID != previousPage.Events[0].EventID {
					t.Fatal("ledger restart discarded retained history")
				}
				continued, err := collector.Query(observability.Filter{}, previousPage.Cursor, 50)
				if err != nil || len(continued.Events) == 0 || continued.Events[0].Sequence <= previousPage.Events[0].Sequence {
					t.Fatal("unchanged ledger boundary did not resume exact cursor")
				}
			}
			jobs.SetObservation(collector)
			if collector != nil {
				evidenceBaseline = collector.Coverage()
				metrics = observability.NewMetrics(collector.BootID(), time.Now().UTC())
				jobs.SetMetrics(metrics)
			}
			forbidden.Store(false)
			recoveryBody.Store(true)
			release, err := jobs.BeginDirectCacheWriter(identity.UUID, "synthetic-private-writer")
			if err != nil {
				t.Fatal("writer contention fixture unavailable")
			}
			defer release()
			recovering := start("qualification-recovered")
			var waitingStage string
			eventually(t, func() bool {
				j, ok := jobs.Get(recovering.ID)
				if ok && j.SyncStage != nil && j.SyncStage.Phase == servicectl.SyncStageWaitingCommit {
					waitingStage = j.SyncStage.StageRef
					return waitingStage != ""
				}
				return false
			})
			waiting := maintenance.ExecutionHealth(time.Now().UTC())
			found = false
			for _, incident := range waiting.Incidents {
				if incident.Collection == "issues" && incident.State == "waiting_writer" && incident.RetryAfter != nil {
					found = true
				}
			}
			if !found || calls.Load() != failedCalls+1 {
				t.Fatal("staged writer wait lost causal evidence or refetched")
			}
			release()
			recovering = waitJob(t, jobs, recovering.ID, identity.UUID)
			if recovering.Status != servicectl.JobStatusSucceeded || calls.Load() != failedCalls+1 {
				t.Fatal("writer retry changed confirmed execution or amplified provider traffic")
			}
			if recovering.SyncStage == nil || recovering.SyncStage.StageRef != waitingStage || recovering.SyncStage.Collection != "issues" || recovering.SyncStage.Phase != servicectl.SyncStageCommitted || recovering.SyncStage.Committed != 1 || recovering.SyncStage.CommittedAt.IsZero() || !recovering.SyncStage.CommittedAt.After(*failed.FinishedAt) {
				t.Fatal("recovery did not durably confirm the same staged issues commit")
			}
			if len(recovering.SyncCollections) != 1 || recovering.SyncCollections[0].Collection != "issues" || recovering.SyncCollections[0].Outcome != servicectl.SyncCollectionSuccess || recovering.SyncCollections[0].Committed != 1 || recovering.SyncCollections[0].LastSuccessAt == nil || !recovering.SyncCollections[0].LastSuccessAt.After(*failed.FinishedAt) {
				t.Fatal("recovery lost its durable collection commit evidence")
			}
			closeCollector(t, collector)
			at = *recovering.FinishedAt
			reader.Collector = collector
			reader.Metrics = metrics
			health = maintenance.ExecutionHealth(at)
			if health.State != "healthy" || health.Active != 0 || health.Recovered == 0 {
				t.Fatal("confirmed recovery did not recover retained incident")
			}
			found = false
			for _, incident := range health.Incidents {
				if incident.Collection == "issues" && incident.State == "recovered" && incident.Reason == tc.reason && incident.LastSuccessAt != nil && incident.LastSuccessAt.After(*failed.FinishedAt) {
					found = true
				}
			}
			if !found {
				t.Fatal("confirmed recovery erased the prior provider cause")
			}
			if mode == "storage_unavailable" && collector.Coverage().State != "partial" {
				t.Fatal("storage failure disappeared into execution success")
			}
			assertTransportParitySince(t, reader, src, &calls, mode == "retained", evidenceBaseline, recovering, tc.terminal, root, provider.URL, identity.UUID, binding.RepoID)
			cached, err := store.GetSourceScoped(ctx, binding.RepoID, "ISSUE-1")
			if err != nil || cached.Body != "synthetic-private-recovery-body" || cached.Title != "Recovered fixture" {
				t.Fatal("telemetry mode changed durable cache truth")
			}
		})
	}
}

func assertTransportParity(t *testing.T, reader *observability.Reader, src isolatedSource, calls *atomic.Int64, requireEvents bool, expectedJob servicectl.Job, failureClass string, private ...string) {
	t.Helper()
	assertTransportParitySince(t, reader, src, calls, requireEvents, observability.Coverage{}, expectedJob, failureClass, private...)
}

func assertTransportParitySince(t *testing.T, reader *observability.Reader, src isolatedSource, calls *atomic.Int64, requireEvents bool, baseline observability.Coverage, expectedJob servicectl.Job, failureClass string, private ...string) {
	t.Helper()
	// The caller establishes a stable execution/ledger phase and observation
	// time (joined terminal owner, or a retained provider-directed wait), so
	// exact parity does not race an ongoing transition or sampler.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("qualification IPC unavailable")
	}
	done := make(chan error, 1)
	go func() { done <- (servicectl.RPCServer{Observability: reader}).Serve(ctx, listener) }()
	defer func() {
		cancel()
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("qualification IPC did not join")
		}
	}()
	ipc := &servicectl.RPCClient{Network: "tcp", Address: listener.Addr().String()}
	src.env = map[string]string{"GITCODE_MCP_SERVICE_NETWORK": "tcp", "GITCODE_MCP_SERVICE_ADDRESS": listener.Addr().String()}
	assets, err := fs.Sub(adminui.Files, "assets")
	if err != nil {
		t.Fatal("embedded Admin fixture unavailable")
	}
	admin := adminhttp.New(adminhttp.Config{Observability: reader, Assets: assets})
	// Synthetic launch authority is confined to this temporary test server.
	// It is not a machine credential and permits the semantic browser cohort
	// to exchange its own real session without copying cookies into output.
	token := "qualification-launch"
	digest := sha256.Sum256([]byte(token))
	hash := base64.RawURLEncoding.EncodeToString(digest[:])
	opened, err := admin.Open(ctx, adminhttp.OpenRequest{LaunchTokenHash: hash})
	if err != nil {
		t.Fatal("local Admin fixture unavailable")
	}
	defer func() {
		cancel()
		stopCtx, stop := context.WithTimeout(context.Background(), 4*time.Second)
		defer stop()
		if !admin.WaitStopped(stopCtx) {
			t.Error("qualification Admin did not join")
		}
	}()
	jar, _ := cookiejar.New(nil)
	httpClient := &http.Client{Jar: jar, Timeout: 2 * time.Second}
	launch, _ := json.Marshal(map[string]string{"launch_token": token})
	req, _ := http.NewRequest("POST", opened.URL+"/api/admin/v1/session", bytes.NewReader(launch))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", opened.URL)
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatal("local Admin session exchange failed")
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatal("local Admin session rejected")
	}
	before := calls.Load()
	expectedRefs := observability.ProjectEntityRefs(observability.Context{Job: observability.KnownRef("job", expectedJob.ID), Cache: observability.KnownRef("cache", expectedJob.CacheUUID), Repo: observability.KnownRef("repo", expectedJob.RepoID)})
	if expectedRefs == nil || expectedRefs.Job == "" {
		t.Fatal("actual admitted job has no observation identity")
	}
	summary, err := retryObservationRead(ctx, func() (observability.Summary, error) { return reader.Summary(ctx) })
	if err != nil {
		t.Fatal("retained summary unavailable")
	}
	if !summary.MetricsSampledAt.IsZero() {
		t.Fatal("qualification fabricated a resource sample")
	}
	for _, resource := range summary.Resources {
		if resource.Value != nil {
			t.Fatal("unsampled resource gauge became a number")
		}
	}
	// Without a collector the event endpoint intentionally reports unavailable;
	// summary parity must still preserve execution authority.
	tests := []struct {
		args                []string
		tool, params, route string
		want                any
	}{
		{[]string{"service", "observe", "--format", "json"}, "service_observability", `{}`, "/api/admin/v1/observability", summary},
	}
	if reader.Metrics != nil {
		metrics, err := retryObservationRead(ctx, func() (observability.MetricsSnapshot, error) {
			return reader.MetricSnapshot(ctx, observability.MetricsRequest{HistoryMinutes: 3})
		})
		if err != nil {
			t.Fatal("retained metrics unavailable")
		}
		for _, series := range metrics.Series {
			for _, bucket := range series.History {
				if bucket.Samples != 0 || bucket.Rate != nil || bucket.Delta != nil {
					t.Fatal("unsampled history fabricated samples or rates")
				}
			}
		}
		tests = append(tests, struct {
			args                []string
			tool, params, route string
			want                any
		}{[]string{"service", "metrics", "--history-minutes", "3", "--format", "json"}, "service_metrics", `{"history_minutes":3}`, "/api/admin/v1/observability/metrics?history_minutes=3", metrics})
	}
	if reader.Collector != nil {
		page, err := retryObservationRead(ctx, func() (observability.Page, error) {
			return reader.Events(ctx, observability.EventsRequest{Limit: 50})
		})
		if err != nil {
			t.Fatal("retained event page unavailable")
		}
		if requireEvents {
			var incident observability.Incident
			for _, item := range summary.ExecutionHealth.Incidents {
				if item.Collection == "issues" {
					incident = item
				}
			}
			if incident.JobRef == "" {
				t.Fatal("actual incident lost its opaque job identity")
			}
			// A recovered incident retains the original failed job ref. Bind
			// the recovery chain to the actual StartSync result, never a sibling
			// succeeded terminal from retained history.
			if err := qualifyJobEvidence(page, baseline, reader.Collector.BootID(), *expectedRefs, expectedJob.Status, failureClass); err != nil {
				t.Fatal(err)
			}
		}
		tests = append(tests, struct {
			args                []string
			tool, params, route string
			want                any
		}{[]string{"service", "events", "--format", "json"}, "service_events", `{}`, "/api/admin/v1/observability/events?limit=50", page})
	}
	for _, tc := range tests {
		var out, stderr bytes.Buffer
		data, err := retryObservationRead(ctx, func() ([]byte, error) {
			out.Reset()
			stderr.Reset()
			exit := cli.ExecuteWithSourceContext(ctx, tc.args, &out, &stderr, src)
			return out.Bytes(), cliObservationError(exit, out.Bytes(), stderr.Bytes())
		})
		if err != nil {
			t.Fatal("actual CLI observation failed")
		}
		assertJSON(t, data, tc.want, false, private)
		for _, minimal := range []bool{false, true} {
			wire := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"` + tc.tool + `","arguments":` + tc.params + `}}` + "\n"
			data, err := retryObservationRead(ctx, func() (json.RawMessage, error) {
				out.Reset()
				stderr.Reset()
				server := mcp.NewWithToolAccess(strings.NewReader(wire), &out, &stderr, nil, nil, mcp.ToolAccessRead)
				if minimal {
					server = mcp.NewMinimal(strings.NewReader(wire), &out, &stderr, mcp.StartupDiagnostic{})
				}
				server.SetServiceClient(func() (*servicectl.RPCClient, error) { return ipc, nil })
				server.SetToolBootstrap(func(context.Context, string, json.RawMessage) (*mcp.RPCHandler, error) {
					t.Error("observation attempted bootstrap")
					return nil, errors.New("observation must not bootstrap")
				})
				if server.Serve() != nil {
					return nil, errors.New("actual MCP stdio observation failed")
				}
				return mcpObservationResult(out.Bytes(), stderr.Bytes())
			})
			if err != nil {
				t.Fatal("MCP observation returned failure")
			}
			assertJSON(t, data, tc.want, false, private)
		}
		data, err = retryObservationRead(ctx, func() ([]byte, error) {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, opened.URL+tc.route, nil)
			if err != nil {
				return nil, errors.New("Admin observation request unavailable")
			}
			resp, err := httpClient.Do(req)
			if err != nil {
				return nil, errors.New("actual authenticated Admin observation failed")
			}
			data, readErr := io.ReadAll(io.LimitReader(resp.Body, observability.MaxMetricsBytes+1))
			_ = resp.Body.Close()
			if readErr != nil {
				return nil, errors.New("Admin observation body unavailable")
			}
			return data, adminObservationError(resp.StatusCode, resp.Header.Get("Cache-Control"), data)
		})
		if err != nil {
			t.Fatal("actual authenticated Admin observation failed")
		}
		assertJSON(t, data, tc.want, tc.tool == "service_observability", private)
	}
	if calls.Load() != before {
		t.Fatal("observation contacted provider")
	}
	if os.Getenv("GITCODE_MCP_O5_BROWSER") == "1" {
		// Playwright starts only this isolated test binary, receives the local
		// origin (no cookies, paths or payloads), then drives the real Admin UI.
		if _, err := admin.Open(ctx, adminhttp.OpenRequest{LaunchTokenHash: hash}); err != nil {
			t.Fatal("browser launch admission failed")
		}
		ready, _ := json.Marshal(map[string]string{"url": opened.URL, "state": summary.ExecutionHealth.State, "job_ref": expectedRefs.Job})
		fmt.Printf("O5_BROWSER %s\n", ready)
		continued := make(chan bool, 1)
		inputDone := make(chan struct{})
		go func() {
			defer close(inputDone)
			var command string
			_, err := fmt.Fscanln(os.Stdin, &command)
			continued <- err == nil && command == "continue"
		}()
		select {
		case ok := <-continued:
			if !ok {
				t.Fatal("browser fixture handoff failed")
			}
		case <-time.After(30 * time.Second):
			// This is the dedicated browser fixture process's input pipe.
			// Close it to unblock and join its reader before failing the test.
			_ = os.Stdin.Close()
			select {
			case <-inputDone:
			case <-time.After(time.Second):
				t.Fatal("browser fixture input did not join")
			}
			t.Fatal("browser fixture handoff timed out")
		}
		<-inputDone
		if calls.Load() != before {
			t.Fatal("browser observation contacted provider")
		}
	}
}

func assertJSON(t *testing.T, data []byte, want any, browserSummary bool, private []string) {
	t.Helper()
	for _, sentinel := range append(private, "synthetic-private-source-body", "synthetic-private-recovery-body", "synthetic-private-provider-body", "synthetic-private-credential", "synthetic-private-writer") {
		if strings.Contains(string(data), sentinel) {
			t.Fatal("qualification observation leaked private input")
		}
	}
	var got, expected any
	encoded, _ := json.Marshal(want)
	if json.Unmarshal(data, &got) != nil || json.Unmarshal(encoded, &expected) != nil {
		t.Fatal("qualification observation was not JSON")
	}
	if browserSummary {
		for _, key := range []string{"admin_observation_version", "event_templates", "event_detail_values", "event_filters"} {
			delete(got.(map[string]any), key)
		}
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatal("actual execution observation diverged across transports")
	}
}
