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
	go func() { defer close(done); c.pollObservability(ctx) }()
	// Wait for the real publication boundary, rather than assuming a goroutine
	// has been scheduled. Stack data is inspected locally and never reported.
	deadline := time.Now().Add(3 * time.Second)
	for {
		buf := make([]byte, 1<<20)
		n := runtime.Stack(buf, true)
		blocked := false
		for _, stack := range strings.Split(string(buf[:n]), "\n\n") {
			if strings.Contains(stack, ".pollObservability(") && strings.Contains(stack, "(*eventLog).append(") {
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
	// Let a fixed-period tick accumulate while the first revision is waiting.
	time.Sleep(1100 * time.Millisecond)
	if !metrics.SampleAt(at, map[string]observability.ProbeValue{"runtime_heap": observability.Available(42)}) {
		t.Fatal("changed revision fixture unavailable")
	}
	c.events.mu.Unlock()
	locked = false
	deadline = time.Now().Add(3 * time.Second)
	for {
		events, _, _, _ := c.events.replay(0)
		if len(events) >= 2 {
			if events[1].OccurredAt.Sub(events[0].OccurredAt) < time.Second {
				t.Fatal("observation invalidations exceeded the actual one-per-second emission bound")
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("changed revision was not eventually published")
		}
		time.Sleep(time.Millisecond)
	}
}
