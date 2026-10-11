package qualification

import (
	"bufio"
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

// One actual delayed attempt remains live across observation and the real
// Admin stream. The wait is provider-directed, not a rewritten checkpoint or
// test clock; all execution happens in the ordinary durable scheduler.
func TestO5ActualActiveRetryAndSSERecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root := t.TempDir()
	var calls atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ordinal := calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/api/v5/repos/fixture/repository/issues" {
			t.Error("unexpected active qualification provider operation")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if ordinal == 2 {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"message":"synthetic-private-provider-body"}`)
			return
		}
		_, _ = io.WriteString(w, `[{"id":1,"number":1,"title":"Fixture","body":"synthetic-private-source-body","state":"open","comments":0}]`)
	}))
	defer provider.Close()
	cachePath := filepath.Join(root, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal("active qualification cache unavailable")
	}
	defer store.Close()
	binding := cache.RepositoryBinding{RepoID: "fixture/repository", Owner: "fixture", Name: "repository", APIBaseURL: provider.URL, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}
	if store.AddRepository(ctx, binding) != nil {
		t.Fatal("active qualification binding unavailable")
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		t.Fatal("active qualification identity unavailable")
	}
	src := isolatedSource{root: root, env: map[string]string{config.EnvToken: "synthetic-private-credential"}}
	cfg, err := config.Load(src, config.Overrides{CachePath: cachePath, GitCodeBaseURL: provider.URL})
	if err != nil {
		t.Fatal("active isolated config unavailable")
	}
	cfg.MaxRetries, cfg.RateLimitRPS, cfg.RateLimitBurst = 0, 1000, 1000
	manager := servicectl.Manager{Source: src, RuntimeDir: root, EffectiveConfig: &cfg}
	jobs := servicectl.NewJobManager(filepath.Join(root, "jobs.json"))
	collector := qualifyCollector(t, "retained", filepath.Join(root, "events"))
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
	request := servicectl.StartSyncJobRequest{RepoID: binding.RepoID, CachePath: cachePath, Issues: true, MaxPages: 1, MaxRecords: 100, IdempotencyKey: "active-baseline"}
	first, err := jobs.StartSync(ctx, manager, request)
	if err != nil {
		t.Fatal("active baseline admission unavailable")
	}
	first = waitJob(t, jobs, first.ID, identity.UUID)
	if first.Status != servicectl.JobStatusSucceeded || calls.Load() != 1 || len(first.SyncCollections) != 1 || first.SyncCollections[0].LastSuccessAt == nil {
		t.Fatal("active baseline success evidence missing")
	}
	request.IdempotencyKey = "active-delayed"
	job, err := jobs.StartSync(ctx, manager, request)
	if err != nil {
		t.Fatal("active retry admission unavailable")
	}
	var active servicectl.Job
	eventually(t, func() bool {
		active, _ = jobs.Get(job.ID)
		return len(active.SyncCollections) == 1 && active.SyncCollections[0].Outcome == servicectl.SyncCollectionRetryScheduled
	})
	view := active.SyncCollections[0]
	if active.Status != servicectl.JobStatusRunning || active.FinishedAt != nil || calls.Load() != 2 || view.Attempt != 1 || view.RetryBudget != 4 || view.ErrorClass != "rate_limited" || view.RetryAfter == nil || time.Until(*view.RetryAfter) < 20*time.Second {
		t.Fatal("actual active retry lost its durable deadline or attempt budget")
	}
	maintenance := servicectl.NewMaintenanceManager(manager, jobs, "")
	reader := &observability.Reader{Collector: collector, Metrics: metrics, Health: maintenance.ExecutionHealthContext}
	refs := observability.ProjectEntityRefs(observability.Context{Job: observability.KnownRef("job", job.ID), Cache: observability.KnownRef("cache", identity.UUID), Repo: observability.KnownRef("repo", binding.RepoID)})
	if refs == nil {
		t.Fatal("active job observation identity missing")
	}
	private := []string{root, provider.URL, identity.UUID, binding.RepoID}
	assertActiveIncident(t, mustActiveSummary(t, ctx, reader), "retrying", refs.Job, view.RetryAfter, first.SyncCollections[0].LastSuccessAt)
	assertRetryAdmissionCount(t, ctx, reader, 0)

	// Real TCP IPC and authenticated Admin, with their owned pollers, remain
	// alive for both phases. This is not a route or EventSource mock.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("active qualification IPC unavailable")
	}
	ipcDone := make(chan error, 1)
	go func() { ipcDone <- (servicectl.RPCServer{Observability: reader}).Serve(ctx, listener) }()
	defer func() {
		cancel()
		_ = listener.Close()
		select {
		case <-ipcDone:
		case <-time.After(time.Second):
			t.Error("active IPC did not join")
		}
	}()
	ipc := &servicectl.RPCClient{Network: "tcp", Address: listener.Addr().String()}
	src.env = map[string]string{"GITCODE_MCP_SERVICE_NETWORK": "tcp", "GITCODE_MCP_SERVICE_ADDRESS": listener.Addr().String()}
	assets, err := fs.Sub(adminui.Files, "assets")
	if err != nil {
		t.Fatal("active embedded assets unavailable")
	}
	admin := adminhttp.New(adminhttp.Config{Observability: reader, Assets: assets})
	digest := sha256.Sum256([]byte("qualification-launch"))
	hash := base64.RawURLEncoding.EncodeToString(digest[:])
	opened, err := admin.Open(ctx, adminhttp.OpenRequest{LaunchTokenHash: hash})
	if err != nil {
		t.Fatal("active Admin unavailable")
	}
	defer func() {
		cancel()
		stopCtx, stop := context.WithTimeout(context.Background(), 4*time.Second)
		defer stop()
		if !admin.WaitStopped(stopCtx) {
			t.Error("active Admin did not join")
		}
	}()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 2 * time.Second}
	launch := bytes.NewBufferString(`{"launch_token":"qualification-launch"}`)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, opened.URL+"/api/admin/v1/session", launch)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", opened.URL)
	response, err := client.Do(req)
	if err != nil {
		t.Fatal("active session unavailable")
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatal("active session rejected")
	}
	probe := func(state string, retry, success *time.Time, expectedCalls int64) {
		before := calls.Load()
		assertActiveIncident(t, mustActiveSummary(t, ctx, reader), state, refs.Job, retry, success)
		var out, stderr bytes.Buffer
		data, err := retryObservationRead(ctx, func() ([]byte, error) {
			out.Reset()
			stderr.Reset()
			exit := cli.ExecuteWithSourceContext(ctx, []string{"service", "observe", "--format", "json"}, &out, &stderr, src)
			return out.Bytes(), cliObservationError(exit, out.Bytes(), stderr.Bytes())
		})
		if err != nil {
			t.Fatal("active CLI read unavailable")
		}
		assertActiveSummaryJSON(t, data, state, refs.Job, retry, success, private)
		for _, minimal := range []bool{false, true} {
			data, err := retryObservationRead(ctx, func() (json.RawMessage, error) {
				out.Reset()
				stderr.Reset()
				wire := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"service_observability","arguments":{}}}` + "\n"
				server := mcp.NewWithToolAccess(strings.NewReader(wire), &out, &stderr, nil, nil, mcp.ToolAccessRead)
				if minimal {
					server = mcp.NewMinimal(strings.NewReader(wire), &out, &stderr, mcp.StartupDiagnostic{})
				}
				server.SetServiceClient(func() (*servicectl.RPCClient, error) { return ipc, nil })
				server.SetToolBootstrap(func(context.Context, string, json.RawMessage) (*mcp.RPCHandler, error) {
					t.Error("active observation attempted bootstrap")
					return nil, errors.New("bootstrap forbidden")
				})
				if server.Serve() != nil {
					return nil, errors.New("active MCP unavailable")
				}
				return mcpObservationResult(out.Bytes(), stderr.Bytes())
			})
			if err != nil {
				t.Fatal("active MCP read unavailable")
			}
			assertActiveSummaryJSON(t, data, state, refs.Job, retry, success, private)
		}
		data, err = retryObservationRead(ctx, func() ([]byte, error) {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, opened.URL+"/api/admin/v1/observability", nil)
			resp, err := client.Do(req)
			if err != nil {
				return nil, errors.New("active Admin read unavailable")
			}
			data, err := io.ReadAll(io.LimitReader(resp.Body, observability.MaxSummaryBytes+1))
			_ = resp.Body.Close()
			if err != nil || len(data) > observability.MaxSummaryBytes {
				return nil, errors.New("active Admin body unavailable")
			}
			return data, adminObservationError(resp.StatusCode, resp.Header.Get("Cache-Control"), data)
		})
		if err != nil {
			t.Fatal("active Admin read failed")
		}
		assertActiveSummaryJSON(t, data, state, refs.Job, retry, success, private)
		if before != expectedCalls || calls.Load() != expectedCalls {
			t.Fatal("active observation contacted provider")
		}
	}
	probe("retrying", view.RetryAfter, first.SyncCollections[0].LastSuccessAt, 2)

	streamCtx, stopStream := context.WithCancel(ctx)
	streamReq, _ := http.NewRequestWithContext(streamCtx, http.MethodGet, opened.URL+"/api/admin/v1/events", nil)
	streamTransport := &http.Transport{ResponseHeaderTimeout: 2 * time.Second}
	defer streamTransport.CloseIdleConnections()
	streamClient := &http.Client{Jar: jar, Transport: streamTransport}
	stream, err := streamClient.Do(streamReq)
	if err != nil {
		stopStream()
		t.Fatal("actual Admin SSE unavailable")
	}
	if stream.StatusCode != http.StatusOK || stream.Header.Get("Content-Type") != "text/event-stream" {
		stopStream()
		_ = stream.Body.Close()
		t.Fatal("actual Admin SSE rejected")
	}
	signals := make(chan string, 16)
	streamDone := make(chan bool, 1)
	go func() {
		scanner := bufio.NewScanner(stream.Body)
		scanner.Buffer(make([]byte, 4096), 4096)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var event adminhttp.Event
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) != nil {
				streamDone <- false
				return
			}
			for _, value := range append(private, "synthetic-private") {
				if strings.Contains(line, value) {
					streamDone <- false
					return
				}
			}
			if event.Kind == "observability_changed" {
				if event.EntityType != "observability" || event.EntityID != "daemon" || event.Revision == "" {
					streamDone <- false
					return
				}
				select {
				case signals <- event.Revision:
				case <-streamCtx.Done():
					streamDone <- true
					return
				}
			}
		}
		streamDone <- streamCtx.Err() != nil
	}()
	defer func() {
		stopStream()
		_ = stream.Body.Close()
		select {
		case ok := <-streamDone:
			if !ok {
				t.Error("actual SSE failed its bounded public contract")
			}
		case <-time.After(time.Second):
			t.Error("actual SSE reader did not join")
		}
	}()
	var initialRevision string
	select {
	case initialRevision = <-signals:
	case <-time.After(3 * time.Second):
		t.Fatal("initial actual SSE invalidation missing")
	}
	if os.Getenv("GITCODE_MCP_O5_ACTIVE_BROWSER") == "1" {
		if _, err := admin.Open(ctx, adminhttp.OpenRequest{LaunchTokenHash: hash}); err != nil {
			t.Fatal("active browser launch unavailable")
		}
		ready, _ := json.Marshal(map[string]any{"url": opened.URL, "job_ref": refs.Job, "retry_after": view.RetryAfter})
		fmt.Printf("O5_ACTIVE %s\n", ready)
	}
	finished := waitJobWithin(t, jobs, job.ID, identity.UUID, 40*time.Second)
	if finished.Status != servicectl.JobStatusSucceeded || calls.Load() != 3 || len(finished.SyncCollections) != 1 || finished.SyncCollections[0].Attempt != 2 || finished.SyncCollections[0].RetryAfter != nil || finished.SyncCollections[0].LastSuccessAt == nil || len(jobs.List()) != 2 {
		t.Fatal("scheduled retry did not conserve exact execution/provider authority")
	}
	probe("recovered", nil, finished.SyncCollections[0].LastSuccessAt, 3)
	assertRetryAdmissionCount(t, ctx, reader, 1)
	page, err := retryObservationRead(ctx, func() (observability.Page, error) { return reader.Events(ctx, observability.EventsRequest{Limit: 50}) })
	if err != nil {
		t.Fatal("active causal events unavailable")
	}
	admissions := 0
	for _, event := range page.Events {
		if event.Code == observability.RetryAdmitted && event.JobRef == refs.Job {
			if event.Attempt != 2 || event.BootID != collector.BootID() || event.CacheRef != refs.Cache || event.RepoRef != refs.Repo || event.CorrelationRef == "" {
				t.Fatal("actual retry event lost causal attempt identity")
			}
			admissions++
		}
	}
	if admissions != 1 {
		t.Fatal("scheduled timer fabricated or lost retry admission")
	}
	finalRevision, err := retryObservationRead(ctx, func() (string, error) { return reader.Revision(ctx) })
	if err != nil || finalRevision == initialRevision {
		t.Fatal("actual recovery revision unavailable or unchanged")
	}
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case revision := <-signals:
			if revision == finalRevision {
				goto invalidated
			}
		case <-deadline.C:
			t.Fatal("confirmed recovery did not invalidate the actual SSE stream")
		}
	}
invalidated:
	cached, err := store.GetSource(ctx, "ISSUE-1")
	if err != nil || cached.Body != "synthetic-private-source-body" {
		t.Fatal("active observation changed cache truth")
	}
	if os.Getenv("GITCODE_MCP_O5_ACTIVE_BROWSER") == "1" {
		fmt.Println("O5_ACTIVE_DONE")
		continued, inputDone := make(chan bool, 1), make(chan struct{})
		go func() {
			defer close(inputDone)
			var command string
			_, err := fmt.Fscanln(os.Stdin, &command)
			continued <- err == nil && command == "continue"
		}()
		select {
		case ok := <-continued:
			if !ok {
				t.Fatal("active browser continuation failed")
			}
		case <-time.After(30 * time.Second):
			_ = os.Stdin.Close()
			select {
			case <-inputDone:
			case <-time.After(time.Second):
				t.Error("active browser input reader did not join")
			}
			t.Fatal("active browser continuation timed out")
		}
		<-inputDone
		if calls.Load() != 3 {
			t.Fatal("active browser contacted provider")
		}
	}
}

func mustActiveSummary(t *testing.T, ctx context.Context, reader *observability.Reader) observability.Summary {
	t.Helper()
	value, err := retryObservationRead(ctx, func() (observability.Summary, error) { return reader.Summary(ctx) })
	if err != nil {
		t.Fatal("active summary unavailable")
	}
	return value
}

func assertActiveIncident(t *testing.T, summary observability.Summary, state, jobRef string, retry, success *time.Time) {
	t.Helper()
	wantHealth := "degraded"
	if state == "recovered" {
		wantHealth = "healthy"
	}
	if summary.ExecutionHealth.State != wantHealth || summary.ExecutionHealth.Coverage != "retained" || summary.ExecutionHealth.Source != "durable_jobs_and_maintenance" {
		t.Fatal("active health lost execution authority")
	}
	for _, incident := range summary.ExecutionHealth.Incidents {
		if incident.Collection != "issues" {
			continue
		}
		if incident.JobRef != jobRef || incident.State != state || incident.Reason != "retry_wait" || incident.LastSuccessAt == nil || success == nil || !incident.LastSuccessAt.Equal(*success) || (retry == nil) != (incident.RetryAfter == nil) || retry != nil && !incident.RetryAfter.Equal(*retry) {
			t.Fatal("active incident lost exact job/retry/last-success evidence")
		}
		return
	}
	t.Fatal("active incident missing")
}

func assertActiveSummaryJSON(t *testing.T, data []byte, state, jobRef string, retry, success *time.Time, private []string) {
	t.Helper()
	for _, value := range append(private, "synthetic-private") {
		if strings.Contains(string(data), value) {
			t.Fatal("active observation leaked private input")
		}
	}
	var summary observability.Summary
	if json.Unmarshal(data, &summary) != nil {
		t.Fatal("active summary is not structured JSON")
	}
	assertActiveIncident(t, summary, state, jobRef, retry, success)
}

func assertRetryAdmissionCount(t *testing.T, ctx context.Context, reader *observability.Reader, want float64) {
	t.Helper()
	metrics, err := retryObservationRead(ctx, func() (observability.MetricsSnapshot, error) {
		return reader.MetricSnapshot(ctx, observability.MetricsRequest{Name: "retry_admissions", HistoryMinutes: 1})
	})
	if err != nil {
		t.Fatal("active retry metrics unavailable")
	}
	for _, series := range metrics.Series {
		if series.Name == "retry_admissions" && series.Labels["collection"] == "issues" {
			if series.Value == nil || *series.Value != want {
				t.Fatal("retry metric counted a scheduled timer rather than actual attempt")
			}
			return
		}
	}
	t.Fatal("actual retry metric missing")
}
