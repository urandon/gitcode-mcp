package observability

import (
	"context"
	"runtime"
	"time"
)

// Probe is a local runtime adapter. It runs exclusively on the sampler worker,
// never from an instrumentation call or read. It must respect cancellation;
// if it fails to unwind, Wait returns false rather than certify clean shutdown.
type MetricProbe func(context.Context) map[string]ProbeValue
type MetricSampler struct{ done chan struct{} }

func StartMetricSampler(ctx context.Context, m *Metrics, probe MetricProbe) *MetricSampler {
	return startMetricSampler(ctx, m, probe, MetricSampleInterval, time.Now)
}
func startMetricSampler(ctx context.Context, m *Metrics, probe MetricProbe, interval time.Duration, now func() time.Time) *MetricSampler {
	sampler := &MetricSampler{done: make(chan struct{})}
	go func() {
		defer close(sampler.done)
		sample := func() {
			at := now().UTC()
			values := RuntimeMetricProbes(m.resetAt, at)
			if probe != nil {
				for key, value := range probe(ctx) {
					values[key] = value
				}
			}
			if ctx.Err() == nil {
				m.SampleAt(at, values)
			}
		}
		sample()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sample()
			}
		}
	}()
	return sampler
}
func (s *MetricSampler) Wait(ctx context.Context) bool {
	select {
	case <-s.done:
		return true
	case <-ctx.Done():
		return false
	}
}
func RuntimeMetricProbes(resetAt, at time.Time) map[string]ProbeValue {
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	gc := Unavailable()
	if stats.NumGC > 0 {
		gc = Available(float64(stats.PauseNs[(stats.NumGC-1)%256]) / float64(time.Second))
	}
	return map[string]ProbeValue{"daemon_uptime": Available(at.Sub(resetAt).Seconds()), "runtime_heap": Available(float64(stats.HeapAlloc)), "runtime_goroutines": Available(float64(runtime.NumGoroutine())), "runtime_gc_pause": gc}
}
