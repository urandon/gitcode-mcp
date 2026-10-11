package observability

import (
	"encoding/json"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func metricsTime() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

func TestRetryOriginAttachmentLossDoesNotInventCounter(t *testing.T) {
	m := NewMetrics(strings.Repeat("a", 32), metricsTime())
	m.mu.Lock()
	attached := m.AttachOrigin("maintenance")
	m.mu.Unlock()
	if attached {
		t.Fatal("contended origin attachment was admitted")
	}
	labels := map[string]string{"collection": "issues"}
	for wantLoss := uint64(1); wantLoss <= 2; wantLoss++ {
		snapshot := m.Snapshot()
		series := metricSeries(t, snapshot, "retry_admissions", labels)
		if snapshot.Dropped != wantLoss || snapshot.State != "initializing" || series.Value != nil || series.Reason != "origin_unavailable" {
			t.Fatal("lost origin fabricated a current counter or erased its loss")
		}
		if wantLoss == 1 && m.Add("retry_admissions", labels, 1) {
			t.Fatal("unattached origin recorded an invented admission")
		}
	}
}
func metricSeries(t *testing.T, s MetricsSnapshot, name string, labels map[string]string) MetricSeries {
	t.Helper()
	for _, series := range s.Series {
		if series.Name != name || len(series.Labels) != len(labels) {
			continue
		}
		match := true
		for k, v := range labels {
			if series.Labels[k] != v {
				match = false
			}
		}
		if match {
			return series
		}
	}
	t.Fatal("missing fixed series", name)
	return MetricSeries{}
}
func TestMetricCatalogCardinalityAndIsolation(t *testing.T) {
	m := NewMetrics(strings.Repeat("a", 32), metricsTime())
	s := m.Snapshot()
	if len(s.Series) == 0 || len(s.Series) > maxMetricSeries {
		t.Fatal("cardinality bound")
	}
	defs := MetricCatalog()
	for _, d := range defs {
		if d.Type == "histogram" {
			d.Bounds[0] = 999
		}
		for _, values := range d.Dimensions {
			values[0] = "private-sentinel"
		}
	}
	for _, origin := range []string{"jobs", "provider", "sync", "rag", "writer", "admin", "maintenance"} {
		if !m.AttachOrigin(origin) {
			t.Fatal(origin)
		}
	}
	if m.AttachOrigin("collector") || m.Add("event_drops", nil, 1) || metricSeries(t, m.Snapshot(), "event_drops", nil).Value != nil {
		t.Fatal("generic origin invented concrete collector availability")
	}
	if m.Add("jobs_admitted", map[string]string{"kind": "private-sentinel"}, 1) || m.Add("jobs_admitted", map[string]string{"repo_id": "private-sentinel"}, 1) || m.Add("private-sentinel", nil, 1) || m.Observe("provider_duration", map[string]string{"outcome": "succeeded"}, math.Inf(1)) {
		t.Fatal("invalid dimensions accepted")
	}
	if !m.Add("jobs_admitted", map[string]string{"kind": "sync"}, 2) {
		t.Fatal("known dimension rejected")
	}
	b, err := json.Marshal(m.Snapshot())
	if err != nil || strings.Contains(string(b), "private-sentinel") {
		t.Fatal("unsafe/malformed DTO")
	}
	if len(m.Snapshot().Series) != len(s.Series) {
		t.Fatal("caller added a series")
	}
}
func TestMetricNullMissingBootAndRateBoundaries(t *testing.T) {
	start := metricsTime()
	m := NewMetrics(strings.Repeat("a", 32), start)
	m.AttachOrigin("jobs")
	m.SampleAt(start, map[string]ProbeValue{"runtime_heap": Available(10)})
	m.Add("jobs_admitted", map[string]string{"kind": "sync"}, 3)
	m.SampleAt(start.Add(15*time.Second), nil)
	s := m.Snapshot()
	heap := metricSeries(t, s, "runtime_heap", nil)
	if heap.Value != nil || heap.Reason != "probe_unavailable" {
		t.Fatal("null probe was zero filled")
	}
	admitted := metricSeries(t, s, "jobs_admitted", map[string]string{"kind": "sync"})
	bucket := admitted.History[59]
	if bucket.Rate == nil || *bucket.Rate != .2 {
		t.Fatal("same boot rate missing", bucket)
	}
	m.SampleAt(start.Add(2*time.Minute), nil)
	bucket = metricSeries(t, m.Snapshot(), "jobs_admitted", map[string]string{"kind": "sync"}).History[59]
	if bucket.Rate != nil || bucket.Complete {
		t.Fatal("rate crossed missing samples")
	}
	history := metricSeries(t, m.Snapshot(), "runtime_heap", nil).History
	if len(history) != 60 || history[58].Reason != "missing_samples" || history[0].Reason != "not_retained_before_boot" {
		t.Fatal("missing history invented")
	}
	restarted := NewMetrics(strings.Repeat("b", 32), start.Add(3*time.Minute))
	restarted.AttachOrigin("jobs")
	restarted.SampleAt(start.Add(3*time.Minute), nil)
	fresh := metricSeries(t, restarted.Snapshot(), "jobs_admitted", map[string]string{"kind": "sync"})
	if fresh.Value == nil || *fresh.Value != 0 || fresh.History[59].Rate != nil {
		t.Fatal("counter/rate crossed boot")
	}
	if m.SampleAt(start.Add(time.Minute), nil) || m.SampleAt(start.Add(2*time.Minute+time.Second), nil) {
		t.Fatal("rollback/duplicate slot accepted")
	}
}
func TestMetricHistogramHistoryAccumulatesIntervals(t *testing.T) {
	start := metricsTime()
	m := NewMetrics(strings.Repeat("a", 32), start)
	m.AttachOrigin("provider")
	labels := map[string]string{"outcome": "succeeded"}
	m.SampleAt(start, nil)
	m.Observe("provider_duration", labels, .05)
	m.Observe("provider_duration", labels, 2)
	m.SampleAt(start.Add(15*time.Second), nil)
	m.Observe("provider_duration", labels, 4)
	m.SampleAt(start.Add(30*time.Second), nil)
	series := metricSeries(t, m.Snapshot(), "provider_duration", labels)
	bucket := series.History[59]
	if series.Value != nil || series.Count != 3 || series.Sum != 6.05 || bucket.Count != 3 || bucket.Sum != 6.05 || bucket.Min == nil || *bucket.Min != .05 || bucket.Max == nil || *bucket.Max != 4 {
		t.Fatal("histogram/history counts", series)
	}
	var total uint64
	for _, v := range series.Distribution {
		total += v
	}
	if total != 3 {
		t.Fatal("fixed histogram buckets")
	}
	series.Distribution[0] = 99
	series.Labels["outcome"] = "private-sentinel"
	*bucket.Min = 99
	fresh := metricSeries(t, m.Snapshot(), "provider_duration", labels)
	if *fresh.History[59].Min != .05 {
		t.Fatal("snapshot aliases registry")
	}
}
func TestMetricHistoryUsesFourDistinctSlotsAndRetention(t *testing.T) {
	start := metricsTime()
	m := NewMetrics(strings.Repeat("a", 32), start)
	for slot := 0; slot < 4; slot++ {
		m.SampleAt(start.Add(time.Duration(slot)*15*time.Second+3*time.Millisecond), map[string]ProbeValue{"runtime_heap": Available(float64(slot + 1))})
	}
	bucket := metricSeries(t, m.Snapshot(), "runtime_heap", nil).History[59]
	if !bucket.Complete || bucket.Count != 4 || bucket.Sum != 10 || *bucket.Min != 1 || *bucket.Max != 4 {
		t.Fatal("four slots", bucket)
	}
	for slot := 4; slot < 4*70; slot++ {
		m.SampleAt(start.Add(time.Duration(slot)*15*time.Second), map[string]ProbeValue{"runtime_heap": Available(7)})
	}
	s := m.Snapshot()
	history := metricSeries(t, s, "runtime_heap", nil).History
	if len(history) != 60 || history[0].Start.Before(start.Add(10*time.Minute)) {
		t.Fatal("retention escaped ring")
	}
	b, err := json.Marshal(s)
	if err != nil || len(b) > 3<<20 {
		t.Fatal("bounded serialized footprint", len(b))
	}
}
func TestMetricRecordingNeverWaitsForSnapshotLock(t *testing.T) {
	m := NewMetrics(strings.Repeat("a", 32), metricsTime())
	m.AttachOrigin("jobs")
	m.mu.Lock()
	done := make(chan bool, 1)
	go func() { done <- m.Add("jobs_admitted", map[string]string{"kind": "sync"}, 1) }()
	select {
	case accepted := <-done:
		if accepted {
			t.Fatal("contended recording accepted")
		}
	case <-time.After(time.Second):
		t.Fatal("recording blocked")
	}
	m.mu.Unlock()
	m.SampleAt(metricsTime(), nil)
	s := m.Snapshot()
	if s.State != "partial" || s.Dropped != 1 {
		t.Fatal("contention lost silently")
	}
}
func TestMetricConcurrentSamplingRecordingAndSnapshots(t *testing.T) {
	m := NewMetrics(strings.Repeat("a", 32), metricsTime())
	m.AttachOrigin("provider")
	var wg sync.WaitGroup
	for worker := 0; worker < 5; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				m.Observe("provider_duration", map[string]string{"outcome": "succeeded"}, 1)
				_ = m.Snapshot()
			}
		}()
	}
	for i := 0; i < 100; i++ {
		m.SampleAt(metricsTime().Add(time.Duration(i)*15*time.Second), nil)
	}
	wg.Wait()
	s := m.Snapshot()
	series := metricSeries(t, s, "provider_duration", map[string]string{"outcome": "succeeded"})
	if series.Count+s.Dropped < 500 {
		t.Fatal("concurrent samples disappeared")
	}
}
