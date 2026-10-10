package adminhttp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"gitcode-mcp/internal/observability"
)

func adminMetric(t *testing.T, metrics *observability.Metrics, name, route, outcome string) observability.MetricSeries {
	t.Helper()
	for _, series := range metrics.Snapshot().Series {
		if series.Name == name && series.Labels["route"] == route && series.Labels["outcome"] == outcome {
			return series
		}
	}
	t.Fatalf("missing fixed series %s/%s/%s", name, route, outcome)
	return observability.MetricSeries{}
}

func requireAdminCount(t *testing.T, metrics *observability.Metrics, route, outcome string, count float64) {
	t.Helper()
	series := adminMetric(t, metrics, "admin_requests", route, outcome)
	if series.Value == nil || *series.Value != count {
		t.Fatalf("%s/%s count does not match %v", route, outcome, count)
	}
}

func TestAdminMetricsUseActualMuxPatternsAndFiniteOutcomes(t *testing.T) {
	metrics := observability.NewMetrics("", time.Now())
	c := New(Config{Metrics: metrics, Assets: fstest.MapFS{"index.html": {Data: []byte("public fixture")}},
		Snapshot: func(ctx context.Context) (ObservationSnapshot, error) {
			if observability.MetricsFromContext(ctx) != metrics {
				t.Error("explicit handler execution lost its metric sink")
			}
			return ObservationSnapshot{}, errors.New("synthetic-private-error")
		}})
	cookie := authorizeObservationController(c, time.Now().Add(time.Minute))
	handler := c.handler()
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
		{"GET", "/api/admin/v1/synthetic-private-route", "unknown", "succeeded", 200, false, false},
		{"GET", "/synthetic-private-spa?query=synthetic-private-query", "assets", "succeeded", 200, false, false},
	} {
		req := httptest.NewRequest(tc.method, "http://127.0.0.1:8080"+tc.target, strings.NewReader("synthetic-private-body"))
		req.Header.Set("Authorization", "synthetic-private-header")
		if tc.auth {
			req.AddCookie(cookie)
		}
		if tc.cancel {
			ctx, cancel := context.WithCancel(req.Context())
			cancel()
			req = req.WithContext(ctx)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if res.Code != tc.status {
			t.Fatalf("fixed %s route status=%d, want %d", tc.route, res.Code, tc.status)
		}
		requireAdminCount(t, metrics, tc.route, tc.outcome, 1)
	}
	if got := adminMetric(t, metrics, "admin_duration", "read", ""); got.Count != 4 || got.Sum < 0 {
		t.Fatal("read duration did not count each completed handler")
	}
	encoded, err := json.Marshal(metrics.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "synthetic-private-") || strings.Contains(string(encoded), cookie.Value) {
		t.Fatal("request coordinates or session material entered metric evidence")
	}
}

// This writer deliberately has no Flusher; wrapping must not invent support.
type adminPlainWriter struct {
	header http.Header
	status int
	err    error
}

func (w *adminPlainWriter) Header() http.Header    { return w.header }
func (w *adminPlainWriter) WriteHeader(status int) { w.status = status }
func (w *adminPlainWriter) Write(body []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	return len(body), nil
}

func TestAdminMetricWrapperPreservesResponseAndPanicSemantics(t *testing.T) {
	for _, tc := range []struct{ name, outcome string }{
		{"informational", "succeeded"}, {"write-failure", "failed"}, {"panic", "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			metrics := observability.NewMetrics("", time.Now())
			c := New(Config{Metrics: metrics})
			writer := &adminPlainWriter{header: make(http.Header)}
			if tc.name == "write-failure" {
				writer.err = errors.New("synthetic write failure")
			}
			panicked := false
			func() {
				defer func() { panicked = recover() != nil }()
				c.observeRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if _, ok := w.(http.Flusher); ok {
						t.Error("wrapper invented streaming support")
					}
					if tc.name == "panic" {
						panic("synthetic panic")
					}
					w.WriteHeader(http.StatusEarlyHints)
					w.WriteHeader(http.StatusOK)
					_, err := w.Write([]byte("public fixture"))
					if err != writer.err {
						t.Error("writer error changed")
					}
				})).ServeHTTP(writer, httptest.NewRequest("GET", "http://127.0.0.1/", nil))
			}()
			if panicked != (tc.name == "panic") {
				t.Fatal("wrapper swallowed or invented a panic")
			}
			requireAdminCount(t, metrics, "unknown", tc.outcome, 1)
		})
	}
	plain := &adminPlainWriter{header: make(http.Header)}
	untouched := false
	New(Config{}).observeRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		untouched = w == plain && observability.MetricsFromContext(r.Context()) == nil
	})).ServeHTTP(plain, httptest.NewRequest("GET", "http://127.0.0.1/", nil))
	if !untouched {
		t.Fatal("disabled instrumentation changed transport")
	}
}

type adminErrorFlusher struct {
	adminPlainWriter
	flushErr error
}

func (w *adminErrorFlusher) Flush()            {}
func (w *adminErrorFlusher) FlushError() error { return w.flushErr }

func TestAdminMetricWrapperPreservesResponseControllerFlushError(t *testing.T) {
	metrics := observability.NewMetrics("", time.Now())
	c := New(Config{Metrics: metrics})
	want := errors.New("synthetic flush failure")
	writer := &adminErrorFlusher{adminPlainWriter: adminPlainWriter{header: make(http.Header)}, flushErr: want}
	c.observeRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := http.NewResponseController(w).Flush(); err != want {
			t.Error("ResponseController lost the underlying flush error")
		}
	})).ServeHTTP(writer, httptest.NewRequest("GET", "http://127.0.0.1/", nil))
	requireAdminCount(t, metrics, "unknown", "failed", 1)
}

func requireSSECount(t *testing.T, c *Controller, want float64) {
	t.Helper()
	value := c.SSEConnectionsProbe()
	if value.Value == nil || *value.Value != want {
		t.Fatalf("active SSE count does not match %v", want)
	}
}

func TestSSEMetricTracksOnlyValidatedStreamsAndUnwinds(t *testing.T) {
	metrics := observability.NewMetrics("", time.Now())
	c := New(Config{Metrics: metrics})
	cookie := authorizeObservationController(c, time.Now().Add(time.Minute))
	handler := c.handler()
	if res := authorizedRequest(t, handler, cookie, "/api/admin/v1/events?after=invalid"); res.Code != 400 {
		t.Fatal("invalid cursor was accepted")
	}
	requireSSECount(t, c, 0)
	req := httptest.NewRequest("GET", "http://127.0.0.1:8080/api/admin/v1/events", nil)
	req.AddCookie(cookie)
	plain := &adminPlainWriter{header: make(http.Header)}
	handler.ServeHTTP(plain, req)
	if plain.status != 500 {
		t.Fatal("non-streaming writer support changed")
	}
	requireSSECount(t, c, 0)
	unauthorized := request(t, handler, "GET", "/api/admin/v1/events", nil, nil)
	if unauthorized.Code != 401 {
		t.Fatal("unauthenticated stream accepted")
	}
	requireSSECount(t, c, 0)
	c.events.append(Event{Kind: "snapshot_changed", EntityType: "snapshot", EntityID: "overview"})
	ctx, cancel := context.WithCancel(context.Background())
	req = req.WithContext(ctx)
	writer := newFlushRecorder()
	done := make(chan struct{})
	go func() { defer close(done); handler.ServeHTTP(writer, req) }()
	select {
	case <-writer.flushed:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("metric wrapper prevented SSE flushing")
	}
	requireSSECount(t, c, 1)
	// Duration and request count represent a completed stream, not its handshake.
	requireAdminCount(t, metrics, "events", "cancelled", 0)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled stream did not unwind")
	}
	requireSSECount(t, c, 0)
	requireAdminCount(t, metrics, "events", "cancelled", 1)
	requireAdminCount(t, metrics, "events", "rejected", 2)
	requireAdminCount(t, metrics, "events", "failed", 1)
	if got := adminMetric(t, metrics, "admin_duration", "events", ""); got.Count != 4 {
		t.Fatal("stream lifetime duration missing")
	}
	// A valid but unavailable replay cursor is a short successful stream;
	// even this early post-admission return must release the gauge.
	res := authorizedRequest(t, handler, cookie, "/api/admin/v1/events?after="+encodeEventCursor(999))
	if res.Code != 200 || !strings.Contains(res.Body.String(), "event: snapshot_required") {
		t.Fatal("metric wrapper changed cursor recovery")
	}
	requireSSECount(t, c, 0)
	requireAdminCount(t, metrics, "events", "succeeded", 1)
	var missing *Controller
	if got := missing.SSEConnectionsProbe(); got.Value != nil || got.Reason != "probe_unavailable" {
		t.Fatal("missing controller reported a healthy zero")
	}
}

func TestConcurrentAdminMetricProbesDoNotChangeRequests(t *testing.T) {
	metrics := observability.NewMetrics("", time.Now())
	c := New(Config{Metrics: metrics})
	cookie := authorizeObservationController(c, time.Now().Add(time.Minute))
	c.events.append(Event{Kind: "snapshot_changed", EntityType: "snapshot", EntityID: "overview"})
	handler := c.handler()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		writer := newFlushRecorder()
		req := httptest.NewRequest("GET", "http://127.0.0.1:8080/api/admin/v1/events", nil).WithContext(ctx)
		req.AddCookie(cookie)
		workers.Add(1)
		go func() { defer workers.Done(); handler.ServeHTTP(writer, req) }()
		select {
		case <-writer.flushed:
		case <-time.After(time.Second):
			t.Fatal("concurrent stream did not flush")
		}
	}
	requireSSECount(t, c, 8)
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for j := 0; j < 25; j++ {
				metrics.SampleAt(time.Now(), map[string]observability.ProbeValue{"sse_connections": c.SSEConnectionsProbe()})
				_ = metrics.Snapshot()
				req := httptest.NewRequest("GET", "http://127.0.0.1:8080/api/admin/v1/readiness", nil)
				req.AddCookie(cookie)
				res := httptest.NewRecorder()
				handler.ServeHTTP(res, req)
				if res.Code != 200 {
					t.Error("telemetry contention changed a read request")
				}
			}
		}()
	}
	cancel()
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent metrics or streams blocked execution")
	}
	requireSSECount(t, c, 0)
}
