//go:build darwin || linux

package adminhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/observability"
)

func TestObservabilityAdminSharedReaderAndPollIsolation(t *testing.T) {
	collector := adminEventCollector(t)
	closeAdminCollector(t, collector)
	at := time.Unix(1700000000, 0).UTC()
	metrics := observability.NewMetrics(collector.BootID(), at)
	var broadCalls atomic.Int64
	reader := &observability.Reader{Collector: collector, Metrics: metrics, Now: func() time.Time { return at }}
	c := New(Config{Observation: collector, Metrics: metrics, Observability: reader, Snapshot: func(context.Context) (ObservationSnapshot, error) {
		broadCalls.Add(1)
		return ObservationSnapshot{}, errors.New("synthetic-private-snapshot-failure")
	}})
	cookie := authorizeObservationController(c, time.Now().Add(time.Hour))
	handler := c.handler()
	expected, err := reader.Summary(context.Background())
	if err != nil {
		t.Fatal("summary fixture unavailable")
	}
	for i := 0; i < 12; i++ {
		response := authorizedRequest(t, handler, cookie, "/api/admin/v1/observability")
		var got observability.Summary
		if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &got) != nil || !reflect.DeepEqual(got, expected) || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("Admin summary diverged from shared reader")
		}
		var browser struct {
			Version   string                                             `json:"admin_observation_version"`
			Filters   map[string][]string                                `json:"event_filters"`
			Templates map[observability.Code]observability.EventTemplate `json:"event_templates"`
			Details   map[string][]string                                `json:"event_detail_values"`
		}
		if len(response.Body.Bytes()) > observability.MaxSummaryBytes || json.Unmarshal(response.Body.Bytes(), &browser) != nil || browser.Version != "1" || !reflect.DeepEqual(browser.Filters, observability.FilterValues()) || !reflect.DeepEqual(browser.Templates, observability.EventTemplates()) || !reflect.DeepEqual(browser.Details, observability.EventDetailValues()) {
			t.Fatal("bounded browser catalog contract unavailable")
		}
	}
	// Older O3 IPC clients disallow unknown fields. Browser metadata must never
	// leak into the shared DTO used by IPC, CLI or MCP.
	shared, _ := json.Marshal(expected)
	decoder := json.NewDecoder(bytes.NewReader(shared))
	decoder.DisallowUnknownFields()
	var legacy observability.Summary
	if decoder.Decode(&legacy) != nil || bytes.Contains(shared, []byte("event_filters")) || bytes.Contains(shared, []byte("event_templates")) || bytes.Contains(shared, []byte("event_detail_values")) || bytes.Contains(shared, []byte("admin_observation_version")) {
		t.Fatal("browser metadata changed the strict shared protocol")
	}
	response := authorizedRequest(t, handler, cookie, "/api/admin/v1/observability/metrics?name=runtime_heap&history_minutes=3")
	var gotMetrics observability.MetricsSnapshot
	expectedMetrics, err := reader.MetricSnapshot(context.Background(), observability.MetricsRequest{Name: "runtime_heap", HistoryMinutes: 3})
	if err != nil || response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &gotMetrics) != nil || !reflect.DeepEqual(gotMetrics, expectedMetrics) {
		t.Fatal("Admin metrics diverged from shared reader")
	}
	response = authorizedRequest(t, handler, cookie, "/api/admin/v1/observability/events?limit=1")
	var gotPage observability.Page
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &gotPage) != nil || len(gotPage.Events) != 1 {
		t.Fatal("bounded Admin events unavailable")
	}
	if broadCalls.Load() != 0 || len(adminCompletedEvents(t, collector)) != 0 || metrics.Snapshot().SampledAt != (time.Time{}) {
		t.Fatal("observation polls called broad bootstrap, recursively emitted events, or sampled")
	}
}

func TestAdminObservationSummaryCeilingIncludesMetadata(t *testing.T) {
	if _, err := boundedAdminObservationSummary(map[string]string{"catalog": strings.Repeat("x", observability.MaxSummaryBytes)}); err == nil {
		t.Fatal("browser metadata escaped the total summary byte ceiling")
	}
	first := observability.EventTemplates()
	for code := range first {
		delete(first, code)
	}
	if len(observability.EventTemplates()) == 0 {
		t.Fatal("caller mutated the reviewed event catalog")
	}
}

func TestObservabilityAdminGuardsBeforeAnyRead(t *testing.T) {
	var reads atomic.Int64
	c := New(Config{Observability: &observability.Reader{Health: func(context.Context, time.Time) (observability.ExecutionHealth, error) {
		reads.Add(1)
		return observability.ExecutionHealth{}, nil
	}}})
	cookie := authorizeObservationController(c, time.Now().Add(time.Hour))
	for _, test := range []struct {
		path, method, host, origin, site, body string
		authorized                             bool
		want                                   int
	}{
		{"/api/admin/v1/observability", "GET", "", "", "", "", false, 401},
		{"/api/admin/v1/observability", "GET", "evil.invalid", "", "", "", true, 400},
		{"/api/admin/v1/observability", "GET", "", "http://evil.invalid", "", "", true, 403},
		{"/api/admin/v1/observability", "GET", "", "", "cross-site", "", true, 403},
		{"/api/admin/v1/observability", "HEAD", "", "", "", "", true, 405},
		{"/api/admin/v1/observability?path=synthetic-private", "GET", "", "", "", "", true, 400},
		{"/api/admin/v1/observability", "GET", "", "", "", "{}", true, 400},
		{"/api/admin/v1/observability/events?limit=50&limit=20", "GET", "", "", "", "", true, 400},
		{"/api/admin/v1/observability/events?stream=%ZZ", "GET", "", "", "", "", true, 400},
		{"/api/admin/v1/observability/events?limit=00", "GET", "", "", "", "", true, 400},
		{"/api/admin/v1/observability/events?source_ref=synthetic-private", "GET", "", "", "", "", true, 400},
		{"/api/admin/v1/observability/events?outcome=succeeded&failure_class=timeout", "GET", "", "", "", "", true, 400},
		{"/api/admin/v1/observability/metrics?history_minutes=61", "GET", "", "", "", "", true, 400},
		{"/api/admin/v1/observability/events?LIMIT=1", "GET", "", "", "", "", true, 400},
		{"/api/admin/v1/observability/events?limit=0&LIMIT=1", "GET", "", "", "", "", true, 400},
		{"/api/admin/v1/observability/metrics?NAME=runtime_heap", "GET", "", "", "", "", true, 400},
		{"/api/admin/v1/observability/events?stream=stderr&code=job_terminal", "GET", "", "", "", "", true, 400},
	} {
		r := httptest.NewRequest(test.method, "http://127.0.0.1:8080"+test.path, bytes.NewBufferString(test.body))
		if test.authorized {
			r.AddCookie(cookie)
		}
		if test.host != "" {
			r.Host = test.host
		}
		if test.origin != "" {
			r.Header.Set("Origin", test.origin)
		}
		if test.site != "" {
			r.Header.Set("Sec-Fetch-Site", test.site)
		}
		response := httptest.NewRecorder()
		c.handler().ServeHTTP(response, r)
		if response.Code != test.want || strings.Contains(response.Body.String(), "synthetic-private") {
			t.Fatal("Admin read guard failed or echoed untrusted selector")
		}
		if test.want != 400 || test.host == "" {
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("observation error lost no-store")
			}
		}
	}
	if reads.Load() != 0 {
		t.Fatal("invalid or unauthenticated requests read retained authority")
	}
}

func TestObservabilityRevisionExcludesPollAndLiveRequestCounters(t *testing.T) {
	collector := adminEventCollector(t)
	closeAdminCollector(t, collector)
	at := time.Unix(1700000000, 0).UTC()
	metrics := observability.NewMetrics(collector.BootID(), at)
	reader := &observability.Reader{Collector: collector, Metrics: metrics, Now: func() time.Time { return time.Now().UTC() }}
	c := New(Config{Observability: reader, Observation: collector, Metrics: metrics})
	before, err := reader.Revision(context.Background())
	if err != nil {
		t.Fatal("stable revision unavailable")
	}
	cookie := authorizeObservationController(c, time.Now().Add(time.Hour))
	for i := 0; i < 20; i++ {
		if authorizedRequest(t, c.handler(), cookie, "/api/admin/v1/observability").Code != http.StatusOK {
			t.Fatal("summary poll unavailable")
		}
	}
	after, err := reader.Revision(context.Background())
	if err != nil || before != after {
		t.Fatal("polls self-invalidated observation")
	}
	if !metrics.SampleAt(at, map[string]observability.ProbeValue{"daemon_uptime": observability.Available(20)}) {
		t.Fatal("actual sample boundary unavailable")
	}
	sampled, err := reader.Revision(context.Background())
	if err != nil || sampled == before {
		t.Fatal("actual metric sample failed to invalidate")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.pollObservability(ctx) }()
	deadline := time.Now().Add(time.Second)
	for {
		c.events.mu.Lock()
		n := len(c.events.events)
		c.events.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("initial invalidation missing")
		}
		time.Sleep(time.Millisecond)
	}
	for i := 0; i < 10; i++ {
		authorizedRequest(t, c.handler(), cookie, "/api/admin/v1/observability")
	}
	// Even an immediately changed metric sample must wait for the one-second
	// poll boundary; no synchronous request callback broadcasts a ledger body.
	metrics.SampleAt(at.Add(15*time.Second), nil)
	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done
	c.events.mu.Lock()
	defer c.events.mu.Unlock()
	if len(c.events.events) != 1 || c.events.events[0].Kind != "observability_changed" || c.events.events[0].EntityType != "observability" {
		t.Fatal("coalesced compact invalidation contract changed")
	}
	data, _ := json.Marshal(c.events.events[0])
	for _, private := range []string{"events\":", "series\":", "synthetic-private", "repo_id"} {
		if strings.Contains(string(data), private) {
			t.Fatal("SSE invalidation carried observation body")
		}
	}
}

func TestObservabilityInvalidationRateAfterDelayedPublication(t *testing.T) {
	collector := adminEventCollector(t)
	closeAdminCollector(t, collector)
	at := time.Unix(1700000000, 0).UTC()
	metrics := observability.NewMetrics(collector.BootID(), at)
	c := New(Config{Observability: &observability.Reader{Collector: collector, Metrics: metrics}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	c.events.mu.Lock()
	locked := true
	waitEntered := make(chan time.Duration, 1)
	releaseWait := make(chan struct{})
	wait := func(ctx context.Context, interval time.Duration) bool {
		select {
		case waitEntered <- interval:
		case <-ctx.Done():
			return false
		}
		select {
		case <-releaseWait:
			return true
		case <-ctx.Done():
			return false
		}
	}
	defer func() {
		if locked {
			c.events.mu.Unlock()
		}
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("observation poller did not join")
		}
	}()
	go func() { defer close(done); c.pollObservabilityWithWait(ctx, wait) }()
	// Wait for the real publication boundary, rather than assuming a goroutine
	// has been scheduled. Stack data is inspected locally and never reported.
	deadline := time.Now().Add(3 * time.Second)
	for {
		select {
		case <-waitEntered:
			t.Fatal("invalidation wait began before held publication completed")
		default:
		}
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		blocked := false
		for _, stack := range strings.Split(string(buf[:n]), "\n\n") {
			if strings.Contains(stack, ".pollObservabilityWithWait(") && strings.Contains(stack, "(*eventLog).append(") {
				blocked = true
			}
		}
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("initial invalidation did not reach the held publication boundary")
		}
		time.Sleep(time.Millisecond)
	}
	// The held append has not reached the wait. A changed revision must not
	// bypass the one explicitly released post-publication interval.
	if !metrics.SampleAt(at, map[string]observability.ProbeValue{"runtime_heap": observability.Available(42)}) {
		t.Fatal("changed revision fixture unavailable")
	}
	c.events.mu.Unlock()
	locked = false
	assertWait := func() {
		t.Helper()
		select {
		case interval := <-waitEntered:
			if interval != time.Second {
				t.Fatal("invalidation did not request the exact one-second wait")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("published invalidation did not enter its bounded wait")
		}
	}
	assertWait()
	first, _, _, _ := c.events.replay(0)
	if len(first) != 1 || first[0].Kind != "observability_changed" || first[0].EntityType != "observability" || first[0].OccurredAt.Location() != time.UTC {
		t.Fatal("first compact UTC invalidation was not published before waiting")
	}
	select {
	case releaseWait <- struct{}{}:
	case <-time.After(3 * time.Second):
		t.Fatal("invalidation wait was not controllable")
	}
	assertWait()
	second, _, _, _ := c.events.replay(0)
	if len(second) != 2 || second[1].Kind != "observability_changed" || second[1].EntityType != "observability" || second[1].OccurredAt.Location() != time.UTC || second[1].Revision == first[0].Revision {
		t.Fatal("one released wait did not publish exactly the changed compact revision")
	}
}

func TestObservabilityDefaultWaitHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if waitObservabilityInterval(ctx, time.Second) {
		t.Fatal("cancelled observation wait resumed publication")
	}
}

func TestObservabilityDefaultWaitUsesTimer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if !waitObservabilityInterval(ctx, 0) {
		t.Fatal("elapsed observation timer did not resume polling")
	}
}
