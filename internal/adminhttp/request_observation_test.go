//go:build darwin || linux

package adminhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"gitcode-mcp/internal/observability"
	"gitcode-mcp/internal/testnet"
)

func adminEventCollector(t *testing.T) *observability.Collector {
	return adminEventCollectorAt(t, adminEventDirectory(t))
}

func adminEventDirectory(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal("resolve synthetic observation root")
	}
	return filepath.Join(root, "observation")
}

func adminEventCollectorAt(t *testing.T, dir string) *observability.Collector {
	t.Helper()
	c := observability.New(observability.Config{Directory: dir, ManagedOutput: true})
	t.Cleanup(func() { closeAdminCollector(t, c) })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		page, err := c.Query(observability.Filter{}, "", 20)
		if err == nil && page.Coverage.State != "initializing" {
			if page.Coverage.Reason == "storage_unavailable" || page.Coverage.Reason == "storage_failed" {
				t.Fatal("synthetic observation fixture did not establish durable storage")
			}
			return c
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("observation fixture did not publish its startup snapshot")
	return c
}

func closeAdminCollector(t *testing.T, c *observability.Collector) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !c.Close(ctx, true) {
		t.Error("observation fixture did not finish")
	}
}

func adminCompletedEvents(t *testing.T, c *observability.Collector) []observability.Event {
	t.Helper()
	page, err := c.Query(observability.Filter{}, "", 200)
	if err != nil || page.HasMore {
		t.Fatal("small observation fixture did not fit one bounded page")
	}
	var events []observability.Event
	for _, e := range page.Events {
		if e.Code == observability.AdminRequestCompleted {
			events = append(events, e)
		}
	}
	return events
}

func TestAdminCompletedRequestCollectorIndependentOfMetrics(t *testing.T) {
	collector := adminEventCollector(t)
	for i := 0; i < 2; i++ {
		// Distinct controllers sharing a boot must not reuse request correlation.
		c := New(Config{Observation: collector})
		cookie := authorizeObservationController(c, time.Now().Add(time.Minute))
		r := httptest.NewRequest("GET", "http://127.0.0.1/api/admin/v1/readiness", nil)
		r.AddCookie(cookie)
		res := httptest.NewRecorder()
		c.handler().ServeHTTP(res, r)
		if res.Code != 200 {
			t.Fatal("observation changed a successful readiness request")
		}
	}
	closeAdminCollector(t, collector)
	events := adminCompletedEvents(t, collector)
	page, err := collector.Query(observability.Filter{}, "", 200)
	if err != nil || uint64(len(events))+page.Coverage.Dropped != 2 {
		t.Fatalf("completed requests retained=%d, lost=%d, want total 2", len(events), page.Coverage.Dropped)
	}
	seen := map[string]bool{}
	for _, e := range events {
		if e.CorrelationRef == "" || seen[e.CorrelationRef] || e.AdminRoute != "read" || e.Outcome != "succeeded" {
			t.Fatal("collector-owned completion/correlation was missing or reused")
		}
		seen[e.CorrelationRef] = true
	}
}

func TestAdminCompletedRequestRealMuxPrivacyAndOutcomes(t *testing.T) {
	collector := adminEventCollector(t)
	metrics := observability.NewMetrics(collector.BootID(), time.Now())
	c := New(Config{Observation: collector, Metrics: metrics,
		Assets: fstest.MapFS{"index.html": {Data: []byte("public fixture")}},
		Snapshot: func(ctx context.Context) (ObservationSnapshot, error) {
			if observability.MetricsFromContext(ctx) != metrics {
				t.Error("handler lost its concrete metric sink")
			}
			return ObservationSnapshot{}, errors.New("synthetic-private-error")
		}})
	cookie := authorizeObservationController(c, time.Now().Add(time.Minute))
	handler := c.handler()
	want := map[string]int{}
	for _, tc := range []struct {
		method, target, route, outcome string
		status                         int
		auth, cancel                   bool
	}{
		{"GET", "/api/admin/v1/readiness", "read", "succeeded", 200, true, false},
		{"GET", "/api/admin/v1/jobs/synthetic-private-job?token=synthetic-private-query", "read", "rejected", 401, false, false},
		{"GET", "/api/admin/v1/snapshot", "read", "failed", 503, true, false},
		{"GET", "/api/admin/v1/readiness", "read", "cancelled", 200, true, true},
		{"POST", "/api/admin/v1/maintenance/synthetic-private-registration/reconcile", "control", "rejected", 401, false, false},
		{"POST", "/api/admin/v1/repository-docs/synthetic-private-registration/search", "search", "rejected", 401, false, false},
		{"GET", "/api/admin/v1/session", "session", "rejected", 401, false, false},
		// These paths are not registered telemetry endpoints yet; they must
		// not be silently excluded based on the user's raw path.
		{"GET", "/api/admin/v1/observability/metrics", "unknown", "succeeded", 200, false, false},
		{"GET", "/synthetic-private-spa?token=synthetic-private-query", "assets", "succeeded", 200, false, false},
	} {
		r := httptest.NewRequest(tc.method, "http://127.0.0.1:8080"+tc.target, strings.NewReader("synthetic-private-body"))
		r.Header.Set("Authorization", "synthetic-private-header")
		r.Header.Set("X-Request-Id", "synthetic-private-request")
		if tc.auth {
			r.AddCookie(cookie)
		}
		if tc.cancel {
			ctx, cancel := context.WithCancel(r.Context())
			cancel()
			r = r.WithContext(ctx)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, r)
		if res.Code != tc.status {
			t.Fatal("request observation changed HTTP status")
		}
		requireAdminCount(t, metrics, tc.route, tc.outcome, 1)
		want[tc.route+"/"+tc.outcome]++
	}
	closeAdminCollector(t, collector)
	events := adminCompletedEvents(t, collector)
	page, err := collector.Query(observability.Filter{}, "", 200)
	if err != nil || uint64(len(events))+page.Coverage.Dropped != 9 {
		t.Fatal("completed HTTP boundaries were duplicated or silently lost")
	}
	seen := map[string]bool{}
	for _, e := range events {
		key := e.AdminRoute + "/" + e.Outcome
		if want[key] == 0 || e.Phase != "admin" || e.CorrelationRef == "" || seen[e.CorrelationRef] || e.JobRef != "" || e.RepoRef != "" || e.CacheRef != "" || e.RegistrationRef != "" || e.StageRef != "" {
			t.Fatal("request event invented an outcome, entity or correlation")
		}
		want[key]--
		seen[e.CorrelationRef] = true
	}
	data, err := json.Marshal(page)
	if err != nil || strings.Contains(string(data), "synthetic-private-") || strings.Contains(string(data), cookie.Value) {
		t.Fatal("request/session coordinates entered event evidence")
	}
}

func TestAdminCompletedRequestExcludesOnlyRegisteredObservationReads(t *testing.T) {
	collector := adminEventCollector(t)
	metrics := observability.NewMetrics(collector.BootID(), time.Now())
	c := New(Config{Observation: collector, Metrics: metrics})
	mux := http.NewServeMux()
	for _, pattern := range []string{"GET /api/admin/v1/observability", "GET /api/admin/v1/observability/metrics", "GET /api/admin/v1/observability/events", "GET /api/admin/v1/observability/private-fixture"} {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	}
	handler := c.observeRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observeAdminRoute(mux, r)
		mux.ServeHTTP(w, r)
	}))
	for _, path := range []string{"/api/admin/v1/observability", "/api/admin/v1/observability/metrics", "/api/admin/v1/observability/events"} {
		for i := 0; i < 3; i++ {
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://127.0.0.1"+path, nil))
		}
	}
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://127.0.0.1/api/admin/v1/observability/private-fixture", nil))
	closeAdminCollector(t, collector)
	page, err := collector.Query(observability.Filter{}, "", 200)
	if err != nil || uint64(len(adminCompletedEvents(t, collector)))+page.Coverage.Dropped != 1 {
		t.Fatal("telemetry polling logged itself or prefix matching suppressed another route")
	}
	requireAdminCount(t, metrics, "read", "succeeded", 9)
	requireAdminCount(t, metrics, "unknown", "succeeded", 1)
}

func TestAdminCompletedRequestOccursOnlyAfterHandlerUnwinds(t *testing.T) {
	collector := adminEventCollector(t)
	c := New(Config{Observation: collector})
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	releaseHandler := sync.OnceFunc(func() { close(release) })
	defer func() {
		releaseHandler()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("handler cleanup did not finish")
		}
	}()
	handler := c.observeRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(200)
	}))
	go func() {
		defer close(done)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://127.0.0.1/", nil))
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not enter")
	}
	before := len(adminCompletedEvents(t, collector))
	releaseHandler()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not unwind")
	}
	closeAdminCollector(t, collector)
	page, err := collector.Query(observability.Filter{}, "", 200)
	if before != 0 || err != nil || uint64(len(adminCompletedEvents(t, collector)))+page.Coverage.Dropped != 1 {
		t.Fatal("active handler was falsely completed or completion was silently lost")
	}
}

type adminShortWriter struct{ adminPlainWriter }

func (w *adminShortWriter) Write(body []byte) (int, error) { return len(body) - 1, nil }

func TestAdminCompletedRequestPreservesTransportFailures(t *testing.T) {
	for _, tc := range []struct{ name, outcome string }{
		{"informational", "succeeded"}, {"write-failure", "failed"}, {"short-write", "failed"},
		{"flush-failure", "failed"}, {"panic", "failed"}, {"failure-and-cancel", "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			collector := adminEventCollector(t)
			c := New(Config{Observation: collector})
			plain := &adminPlainWriter{header: make(http.Header)}
			var writer http.ResponseWriter = plain
			switch tc.name {
			case "write-failure", "failure-and-cancel":
				plain.err = errors.New("synthetic-private-write")
			case "short-write":
				writer = &adminShortWriter{*plain}
			case "flush-failure":
				writer = &adminErrorFlusher{adminPlainWriter: *plain, flushErr: errors.New("synthetic-private-flush")}
			}
			r := httptest.NewRequest("GET", "http://127.0.0.1/", nil)
			if tc.name == "failure-and-cancel" {
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
			}
			panicked := false
			func() {
				defer func() { panicked = recover() != nil }()
				c.observeRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					_, flushable := w.(http.Flusher)
					if flushable != (tc.name == "flush-failure") {
						t.Error("observation changed streaming support")
					}
					if tc.name == "panic" {
						panic("synthetic-private-panic")
					}
					if tc.name == "flush-failure" {
						if http.NewResponseController(w).Flush() == nil {
							t.Error("flush error was swallowed")
						}
						return
					}
					w.WriteHeader(http.StatusEarlyHints)
					w.WriteHeader(http.StatusOK)
					n, err := w.Write([]byte("public fixture"))
					if err != plain.err || (tc.name == "short-write" && n != len("public fixture")-1) {
						t.Error("observation changed response result")
					}
				})).ServeHTTP(writer, r)
			}()
			if panicked != (tc.name == "panic") {
				t.Fatal("observation swallowed or invented a panic")
			}
			closeAdminCollector(t, collector)
			events := adminCompletedEvents(t, collector)
			page, err := collector.Query(observability.Filter{}, "", 200)
			if err != nil || uint64(len(events))+page.Coverage.Dropped != 1 {
				t.Fatal("transport boundary duplicated or silently lost")
			}
			for _, e := range events {
				if e.AdminRoute != "unknown" || e.Outcome != tc.outcome {
					t.Fatal("transport failure precedence changed")
				}
			}
		})
	}
}

func TestAdminCompletedRequestSSEActualFlushFailure(t *testing.T) {
	collector := adminEventCollector(t)
	c := New(Config{Observation: collector})
	cookie := authorizeObservationController(c, time.Now().Add(time.Minute))
	completed := make(chan bool, 1)
	handler := c.handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		control := http.NewResponseController(w)
		if control.SetWriteDeadline(time.Unix(1, 0)) != nil {
			completed <- false
			return
		}
		handler.ServeHTTP(w, r)
		completed <- control.Flush() != nil
	}))
	defer server.Close()
	r, err := http.NewRequest("GET", server.URL+"/api/admin/v1/events?after="+encodeEventCursor(999), nil)
	if err != nil {
		t.Fatal("synthetic SSE request construction failed")
	}
	r.AddCookie(cookie)
	client := testnet.NoExternalNetwork(t)
	client.Timeout = 2 * time.Second
	res, _ := client.Do(r)
	if res != nil {
		res.Body.Close()
	}
	select {
	case failed := <-completed:
		if !failed {
			t.Fatal("actual SSE flush failure was not established")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SSE handler did not finish")
	}
	requireSSECount(t, c, 0)
	closeAdminCollector(t, collector)
	page, err := collector.Query(observability.Filter{}, "", 200)
	events := adminCompletedEvents(t, collector)
	if err != nil || uint64(len(events))+page.Coverage.Dropped != 1 {
		t.Fatal("actual SSE completion duplicated or silently lost")
	}
	for _, e := range events {
		if e.AdminRoute != "events" || e.Outcome != "failed" {
			t.Fatal("SSE flush failure lost precedence over cancellation")
		}
	}
}

func TestAdminCompletedRequestActualControlFailure(t *testing.T) {
	collector := adminEventCollector(t)
	metrics := observability.NewMetrics(collector.BootID(), time.Now())
	calls := 0
	c := New(Config{Observation: collector, Metrics: metrics, ReconcileMaintenance: func(ctx context.Context, req RegistrationControlRequest) (any, error) {
		calls++
		if req.RegistrationID != "synthetic-private-registration" || observability.MetricsFromContext(ctx) != metrics {
			t.Error("explicit control changed its identity or metric sink")
		}
		return nil, errors.New("synthetic-private-provider")
	}})
	cookie := authorizeObservationController(c, time.Now().Add(time.Minute))
	r := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/admin/v1/maintenance/synthetic-private-registration/reconcile", strings.NewReader(`{"idempotency_key":"synthetic-private-key"}`))
	r.AddCookie(cookie)
	r.Header.Set("Origin", "http://127.0.0.1:8080")
	r.Header.Set("Sec-Fetch-Site", "same-origin")
	r.Header.Set("X-CSRF-Token", "csrf")
	res := httptest.NewRecorder()
	c.handler().ServeHTTP(res, r)
	if calls != 1 || res.Code < 500 {
		t.Fatal("control failure changed execution or HTTP outcome")
	}
	closeAdminCollector(t, collector)
	page, err := collector.Query(observability.Filter{}, "", 200)
	events := adminCompletedEvents(t, collector)
	if err != nil || uint64(len(events))+page.Coverage.Dropped != 1 {
		t.Fatal("actual control completion duplicated or silently lost")
	}
	for _, e := range events {
		if e.AdminRoute != "control" || e.Outcome != "failed" {
			t.Fatal("control failure classification changed")
		}
	}
	data, _ := json.Marshal(page)
	if strings.Contains(string(data), "synthetic-private-") || strings.Contains(string(data), cookie.Value) {
		t.Fatal("explicit control/session material entered events")
	}
	requireAdminCount(t, metrics, "control", "failed", 1)
}

func TestAdminCompletedRequestRestartDoesNotReplayExecution(t *testing.T) {
	dir := adminEventDirectory(t)
	first := adminEventCollectorAt(t, dir)
	c := New(Config{Observation: first})
	request := httptest.NewRequest("GET", "http://127.0.0.1/api/admin/v1/readiness", nil)
	request.AddCookie(authorizeObservationController(c, time.Now().Add(time.Minute)))
	c.handler().ServeHTTP(httptest.NewRecorder(), request)
	closeAdminCollector(t, first)
	original := adminCompletedEvents(t, first)
	page, err := first.Query(observability.Filter{}, "", 200)
	if err != nil || uint64(len(original))+page.Coverage.Dropped != 1 {
		t.Fatal("original completion silently lost")
	}
	restarted := adminEventCollectorAt(t, dir)
	closeAdminCollector(t, restarted)
	retained := adminCompletedEvents(t, restarted)
	if len(retained) != len(original) {
		current, _ := restarted.Query(observability.Filter{}, "", 200)
		t.Fatalf("restart retained %d original %d; before=%s/%s after=%s/%s", len(retained), len(original), page.Coverage.State, page.Coverage.Reason, current.Coverage.State, current.Coverage.Reason)
	}
	for i, e := range retained {
		if e != original[i] || e.BootID != first.BootID() {
			t.Fatal("restart rewrote original request correlation")
		}
	}
	for _, code := range []observability.Code{observability.JobAdmitted, observability.JobTerminal, observability.JobRecovered, observability.ProviderAttempt, observability.SyncCommitted} {
		page, err := restarted.Query(observability.Filter{Code: code}, "", 20)
		if err != nil || len(page.Events) != 0 {
			t.Fatal("HTTP replay invented non-HTTP execution")
		}
	}
}
