//go:build darwin || linux

package observability

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func collectorMetricFixture(t *testing.T) (*Collector, *Metrics) {
	t.Helper()
	c := New(Config{Directory: testDirectory(t), ManagedOutput: true})
	t.Cleanup(func() { flush(t, c, true) })
	ready(t, c)
	await(t, func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.events) > 0
	})
	m := NewMetrics(c.BootID(), metricsTime())
	if !m.AttachCollector(c) {
		t.Fatal("concrete collector origin unavailable")
	}
	return c, m
}

func requireCollectorCounter(t *testing.T, m *Metrics, want uint64) MetricSeries {
	t.Helper()
	series := metricSeries(t, m.Snapshot(), "event_drops", nil)
	if series.Value == nil || *series.Value != float64(want) || len(series.Labels) != 0 {
		t.Fatal("collector admission loss counter differs")
	}
	return series
}

func TestCollectorMetricActualLossClosedBoundaryAndRestart(t *testing.T) {
	c, m := collectorMetricFixture(t)
	requireCollectorCounter(t, m, 0)
	c.mu.Lock()
	for i := 0; i < 7; i++ {
		if c.Emit(RecoveryFinished, Context{}) {
			c.mu.Unlock()
			t.Fatal("contended admission unexpectedly succeeded")
		}
	}
	if c.Emit(Code("unchecked-code"), Context{}) || c.Emit(RecoveryFailed, Context{Repo: Ref{"unchecked-ref"}}) {
		c.mu.Unlock()
		t.Fatal("unchecked event admitted")
	}
	// A bounded barrier also fails safely if a regression takes that mutex.
	snapshots := make(chan MetricsSnapshot, 1)
	go func() { snapshots <- m.Snapshot() }()
	var heldSnapshot MetricsSnapshot
	select {
	case heldSnapshot = <-snapshots:
		c.mu.Unlock()
	case <-time.After(time.Second):
		c.mu.Unlock()
		t.Fatal("counter snapshot waited for the collector mutex")
	}
	point := metricSeries(t, heldSnapshot, "event_drops", nil)
	if point.Value == nil || *point.Value != 7 {
		t.Fatal("held-mutex admission loss did not reach the concrete origin")
	}
	c.mu.Lock()
	c.queueBytes = MaxQueueBytes
	c.mu.Unlock()
	if c.Emit(RecoveryFinished, Context{}) {
		t.Fatal("full queue byte budget admitted an event")
	}
	if c.Coverage().Reason != "queue_overflow" {
		t.Fatal("fixture did not exercise the queue boundary")
	}
	c.mu.Lock()
	c.queueBytes = 0
	c.mu.Unlock()
	requireCollectorCounter(t, m, 8)
	flush(t, c, true)
	c.mu.Lock()
	accepted := c.Emit(RecoveryFinished, Context{})
	c.mu.Unlock()
	if accepted {
		t.Fatal("closed collector admitted an event")
	}
	c.pendingLoss.Add(1) // preempted post-seal Add does not own a drop
	requireCollectorCounter(t, m, 8)
	if m.Add("event_drops", nil, 99) {
		t.Fatal("additive caller replaced the concrete counter origin")
	}
	requireCollectorCounter(t, m, 8)
	restarted := New(Config{Directory: c.config.Directory, ManagedOutput: true})
	defer flush(t, restarted, true)
	ready(t, restarted)
	fresh := NewMetrics(restarted.BootID(), metricsTime().Add(time.Hour))
	if !fresh.AttachCollector(restarted) || fresh.AttachCollector(c) {
		t.Fatal("counter attachment ignored the boot boundary")
	}
	requireCollectorCounter(t, fresh, 0)
	if restarted.Coverage().Dropped != 8 {
		t.Fatal("durable old loss boundary was erased")
	}
}

func TestCollectorMetricMissingForeignOriginAndSampledRate(t *testing.T) {
	c, m := collectorMetricFixture(t)
	unattached := NewMetrics(c.BootID(), metricsTime())
	if unattached.AttachCollector(nil) || metricSeries(t, unattached.Snapshot(), "event_drops", nil).Value != nil {
		t.Fatal("missing collector fabricated a zero origin")
	}
	foreign := NewMetrics("", metricsTime())
	if foreign.AttachCollector(c) || metricSeries(t, foreign.Snapshot(), "event_drops", nil).Value != nil {
		t.Fatal("foreign boot was treated as this metric origin")
	}
	if !m.SampleAt(metricsTime(), nil) {
		t.Fatal("initial counter sample unavailable")
	}
	c.mu.Lock()
	c.Emit(RecoveryFinished, Context{})
	c.Emit(RecoveryFinished, Context{})
	c.mu.Unlock()
	if !m.SampleAt(metricsTime().Add(15*time.Second), nil) {
		t.Fatal("adjacent counter sample unavailable")
	}
	series := requireCollectorCounter(t, m, 2)
	bucket := series.History[MetricHistoryMinutes-1]
	if bucket.Delta == nil || *bucket.Delta != 2 || bucket.Rate == nil || *bucket.Rate != 2.0/15 {
		t.Fatal("per-boot admission total did not produce an adjacent-sample rate")
	}
}

func TestCollectorMetricConcurrentReadersDoNotChangeAdmission(t *testing.T) {
	c, m := collectorMetricFixture(t)
	var workers sync.WaitGroup
	var dropped atomic.Uint64
	for i := 0; i < 4; i++ {
		workers.Add(2)
		go func() {
			defer workers.Done()
			for j := 0; j < 100; j++ {
				if !c.Emit(RecoveryFinished, Context{}) {
					dropped.Add(1)
				}
			}
		}()
		go func() {
			defer workers.Done()
			for j := 0; j < 100; j++ {
				_ = m.Snapshot()
				_ = c.Coverage()
			}
		}()
	}
	workers.Wait()
	flush(t, c, true)
	requireCollectorCounter(t, m, dropped.Load())
	if c.Coverage().Dropped != dropped.Load() {
		t.Fatal("per-boot metric disagreed with actual validated admission loss")
	}
}
