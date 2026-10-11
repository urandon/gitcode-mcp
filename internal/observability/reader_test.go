//go:build darwin || linux

package observability

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestReadRequestStrictBeforeAnyObservation(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `1`, `"x"`, `{"unknown":1}`, `{"limit":50,"limit":20}`, `{"limit":0}`, `{"limit":201}`, `{"limit":1.5}`, `{"limit":"50"}`, `{"limit":null}`, `{"limit":[]}`, `{"filter":{}}`, `{"stream":true}`, `{"cursor":null}`, `{} {}`, `{"source_ref":"synthetic-private-coordinate"}`} {
		_, err := DecodeEventsRequest([]byte(raw))
		var q QueryError
		if !errors.As(err, &q) || strings.Contains(err.Error(), "synthetic-private") {
			t.Fatal("invalid event query accepted or echoed")
		}
	}
	r, err := DecodeEventsRequest([]byte(`{}`))
	if err != nil || r.Limit != 50 {
		t.Fatal("omitted event limit lost documented default")
	}
	for _, raw := range []string{`{"name":"unknown"}`, `{"history_minutes":0}`, `{"history_minutes":61}`, `{"history_minutes":true}`, `{"history_minutes":1,"history_minutes":2}`, `{"labels":{}}`} {
		if _, err := DecodeMetricsRequest([]byte(raw)); err == nil {
			t.Fatal("invalid metric query accepted")
		}
	}
	metrics, err := DecodeMetricsRequest(nil)
	if err != nil || metrics.HistoryMinutes != 60 {
		t.Fatal("metric history default changed")
	}
	for _, raw := range []string{`null`, `{"path":"synthetic-private"}`, `{"a":1,"a":2}`, `{}[]`} {
		if DecodeReadRequest([]byte(raw), &struct{}{}) == nil {
			t.Fatal("summary accepted unvalidated fields")
		}
	}
	if DecodeReadRequest([]byte(strings.Repeat(" ", MaxReadRequestBytes+1)), &struct{}{}) == nil {
		t.Fatal("request cap ignored")
	}
	var absent *Reader
	if _, err := absent.Events(context.Background(), EventsRequest{Limit: 0}); err == nil || err.Error() != "invalid_query: unsupported_filter_or_limit" {
		t.Fatal("invalid event request accessed absent reader")
	}
	if _, err := absent.MetricSnapshot(context.Background(), MetricsRequest{Name: "unknown", HistoryMinutes: 60}); err == nil || err.Error() != "invalid_query: unsupported_metric_or_window" {
		t.Fatal("invalid metric request accessed absent reader")
	}
}

func TestSharedReaderSampledResourceAndMetricsParity(t *testing.T) {
	c := New(Config{Directory: testDirectory(t), ManagedOutput: true})
	ready(t, c)
	flush(t, c, true)
	at := metricsTime()
	m := NewMetrics(c.BootID(), at)
	m.AttachOrigin("jobs")
	if !m.SampleAt(at, map[string]ProbeValue{"runtime_heap": Available(123)}) {
		t.Fatal("actual sampler unavailable")
	}
	before := m.Snapshot()
	health := ExecutionHealth{Schema: 1, ObservedAt: at, State: "degraded", Coverage: "retained", Reason: "durable_retention_boundary", Incidents: []Incident{}}
	r := &Reader{Collector: c, Metrics: m, Now: func() time.Time { return at }, Health: func(ctx context.Context, when time.Time) (ExecutionHealth, error) {
		if when != at || ctx.Err() != nil {
			t.Fatal("retained authority timestamp/budget changed")
		}
		return health, nil
	}}
	summary, err := r.Summary(context.Background())
	if err != nil || summary.Schema != 1 || summary.Protocol != Protocol || summary.ExecutionHealth.State != "degraded" || !reflect.DeepEqual(summary.ObservationHealth, ProjectObservationHealth(c.Coverage(), before)) || summary.Collector.BootID != m.boot || summary.Limits != Limits() {
		t.Fatal("shared summary lost independent health or identity")
	}
	if len(summary.Resources) != 9 {
		t.Fatal("resource catalog changed")
	}
	for _, p := range summary.Resources {
		if p.Name == "runtime_heap" {
			if p.Value == nil || *p.Value != 123 {
				t.Fatal("sampled resource lost")
			}
		} else if p.Value != nil {
			t.Fatal("unsampled resource fabricated zero")
		}
	}
	full, err := r.MetricSnapshot(context.Background(), MetricsRequest{HistoryMinutes: 60})
	if err != nil || !reflect.DeepEqual(full, before) {
		t.Fatal("shared metric snapshot diverged from fixed registry")
	}
	subset, err := r.MetricSnapshot(context.Background(), MetricsRequest{Name: "runtime_heap", HistoryMinutes: 3})
	if err != nil || len(subset.Catalog) != 1 || len(subset.Series) != 1 || len(subset.Series[0].History) != 3 || subset.BootID != before.BootID || subset.ResetAt != before.ResetAt {
		t.Fatal("metric projection expanded query or lost boot boundary")
	}
	subset.Series[0].Labels["synthetic-private-label"] = "synthetic-private-value"
	if !reflect.DeepEqual(before, m.Snapshot()) {
		t.Fatal("read mutated fixed metric registry")
	}
	if err := checkReadSize(context.Background(), summary, MaxSummaryBytes); err != nil {
		t.Fatal("summary exceeded published cap")
	}
}

func TestSharedReaderCancellationBusyAndUnavailable(t *testing.T) {
	at := metricsTime()
	m := NewMetrics(strings.Repeat("a", 32), at)
	r := &Reader{Metrics: m, Now: func() time.Time { return at }}
	initial, err := r.Summary(context.Background())
	if err != nil || initial.ObservationHealth.State != "unavailable" || initial.ExecutionHealth.State != "unknown" || len(initial.Resources) != 9 {
		t.Fatal("missing collector/authority claimed availability")
	}
	c := New(Config{Directory: testDirectory(t), ManagedOutput: true})
	ready(t, c)
	flush(t, c, true)
	r.Collector = c
	initial, err = r.Summary(context.Background())
	if err != nil || initial.ObservationHealth.State == "unavailable" || initial.ObservationHealth.MetricsState != "initializing" || !reflect.DeepEqual(initial.ObservationHealth, ProjectObservationHealth(c.Coverage(), m.Snapshot())) {
		t.Fatal("unsampled metrics mistaken for absent metrics")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for i := 0; i < 64; i++ {
		if _, err := r.Summary(ctx); !errors.Is(err, context.Canceled) {
			t.Fatal("summary ignored cancellation")
		}
		if _, err := r.Events(ctx, EventsRequest{Limit: 50}); !errors.Is(err, context.Canceled) {
			t.Fatal("events ignored cancellation")
		}
		if _, err := r.MetricSnapshot(ctx, MetricsRequest{HistoryMinutes: 60}); !errors.Is(err, context.Canceled) {
			t.Fatal("metrics ignored cancellation")
		}
	}
	m.mu.Lock()
	_, err = r.Summary(context.Background())
	m.mu.Unlock()
	var q QueryError
	if !errors.As(err, &q) || q.Code != "observation_busy" {
		t.Fatal("shared summary hid snapshot contention")
	}
	if m.dropped.Load() != 0 || !m.sampledAt.IsZero() {
		t.Fatal("read invented loss or sampled data")
	}
	if err := checkReadSize(context.Background(), strings.Repeat("x", 16), 8); !errors.As(err, &q) || q.Reason != "response_limit" {
		t.Fatal("oversized response not explicit")
	}
}

func TestSharedReaderSparseEventPageAdvancesWithinByteCap(t *testing.T) {
	c := New(Config{Directory: testDirectory(t), ManagedOutput: true})
	ready(t, c)
	for i := 0; i < 300; i++ {
		admitFixture(t, c, RecoveryFinished, Context{Job: KnownRef("job", strings.Repeat("x", i+1))})
	}
	flush(t, c, true)
	r := &Reader{Collector: c}
	req := EventsRequest{Filter: Filter{JobRef: KnownRef("job", "absent").value}, Limit: 50}
	visited := map[string]bool{}
	pages := 0
	for {
		p, err := r.Events(context.Background(), req)
		if err != nil || len(p.Events) != 0 || visited[p.Cursor] {
			t.Fatal("sparse bounded page stalled or changed match")
		}
		data, _ := json.Marshal(p)
		if len(data) > MaxPageBytes {
			t.Fatal("events exceeded output cap")
		}
		visited[p.Cursor] = true
		pages++
		if !p.HasMore {
			break
		}
		if pages > 5 {
			t.Fatal("sparse traversal exceeded retained fixture bound")
		}
		req.Cursor = p.Cursor
	}
	if pages < 2 {
		t.Fatal("fixture did not exercise scan budget")
	}
}
