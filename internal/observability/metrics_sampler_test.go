package observability

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestMetricSamplerCancellationAndLocalRuntimeProbes(t *testing.T) {
	start := metricsTime()
	m := NewMetrics(strings.Repeat("a", 32), start)
	ctx, cancel := context.WithCancel(context.Background())
	sampled := make(chan struct{}, 1)
	sampler := startMetricSampler(ctx, m, func(context.Context) map[string]ProbeValue {
		select {
		case sampled <- struct{}{}:
		default:
		}
		return map[string]ProbeValue{"jobs_queued": Available(4)}
	}, time.Hour, func() time.Time { return start })
	select {
	case <-sampled:
	case <-time.After(time.Second):
		t.Fatal("sampler did not probe")
	}
	cancel()
	wait, c := context.WithTimeout(context.Background(), time.Second)
	defer c()
	if !sampler.Wait(wait) {
		t.Fatal("sampler did not unwind")
	}
	// Cancellation can precede publication; explicitly inspect probe DTOs instead
	// of making the test depend on goroutine scheduling of the final sample.
	probes := RuntimeMetricProbes(start, start.Add(time.Second))
	for _, name := range []string{"daemon_uptime", "runtime_heap", "runtime_goroutines"} {
		if probes[name].Value == nil || *probes[name].Value < 0 {
			t.Fatal("runtime gauge unavailable", name)
		}
	}
}
func TestMetricStalledProbeDoesNotBlockRecordingOrClaimShutdown(t *testing.T) {
	m := NewMetrics(strings.Repeat("a", 32), metricsTime())
	m.AttachOrigin("jobs")
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	release := make(chan struct{})
	sampler := startMetricSampler(ctx, m, func(context.Context) map[string]ProbeValue { close(entered); <-release; return nil }, time.Hour, metricsTime)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no probe")
	}
	if !m.Add("jobs_admitted", map[string]string{"kind": "sync"}, 1) {
		t.Fatal("probe acquired recording lock")
	}
	_ = m.Snapshot()
	cancel()
	expired, c := context.WithCancel(context.Background())
	c()
	if sampler.Wait(expired) {
		t.Fatal("stalled sampler marked clean")
	}
	close(release)
	wait, c := context.WithTimeout(context.Background(), time.Second)
	defer c()
	if !sampler.Wait(wait) {
		t.Fatal("released sampler not joined")
	}
}
