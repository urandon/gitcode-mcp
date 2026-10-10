package observability

import (
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const MetricHistoryMinutes = 60
const MetricSampleInterval = 15 * time.Second
const maxMetricSeries = 256

type ProbeValue struct {
	Value  *float64
	Reason string
}

func Available(value float64) ProbeValue { return ProbeValue{Value: &value} }
func Unavailable() ProbeValue            { return ProbeValue{Reason: "probe_unavailable"} }

type MetricPoint struct {
	Name         string            `json:"name"`
	Labels       map[string]string `json:"labels"`
	Value        *float64          `json:"value"`
	Reason       string            `json:"reason,omitempty"`
	Count        uint64            `json:"count,omitempty"`
	Sum          float64           `json:"sum,omitempty"`
	Distribution []uint64          `json:"distribution,omitempty"`
}
type MetricBucket struct {
	Start    time.Time `json:"start"`
	Count    uint64    `json:"count"`
	Sum      float64   `json:"sum"`
	Min      *float64  `json:"min"`
	Max      *float64  `json:"max"`
	Delta    *float64  `json:"delta"`
	Rate     *float64  `json:"rate"`
	Samples  int       `json:"samples"`
	Complete bool      `json:"complete"`
	Reason   string    `json:"reason,omitempty"`
	invalid  bool
}
type MetricSeries struct {
	MetricPoint
	History []MetricBucket `json:"history"`
}
type MetricsSnapshot struct {
	BootID    string             `json:"boot_id"`
	ResetAt   time.Time          `json:"reset_at"`
	SampledAt time.Time          `json:"sampled_at"`
	State     string             `json:"state"`
	Dropped   uint64             `json:"dropped"`
	Catalog   []MetricDefinition `json:"catalog"`
	Series    []MetricSeries     `json:"series"`
}
type metricCell struct {
	definition               MetricDefinition
	labels                   map[string]string
	value                    float64
	available                bool
	reason                   string
	count                    uint64
	sum                      float64
	distribution             []uint64
	history                  [MetricHistoryMinutes]MetricBucket
	lastAt                   time.Time
	lastValue                float64
	lastCount                uint64
	lastSum                  float64
	lastSlot                 int64
	hasLast                  bool
	intervalMin, intervalMax *float64
}

// Metrics is volatile per-boot evidence. Recording is TryLock-only, with no
// callback, disk or network operation under the mutex. Contention is explicit
// loss, never execution contention. Read DTOs contain independent deep copies.
type Metrics struct {
	mu                 sync.Mutex
	boot               string
	resetAt, sampledAt time.Time
	cells              map[string]*metricCell
	keys               []string
	origins            map[string]bool
	dropped            atomic.Uint64
	partial            bool
}

func NewMetrics(boot string, resetAt time.Time) *Metrics {
	if !hexID.MatchString(boot) {
		boot = randomID()
	}
	if resetAt.IsZero() {
		resetAt = time.Now().UTC()
	}
	m := &Metrics{boot: boot, resetAt: resetAt.UTC(), cells: map[string]*metricCell{}, origins: map[string]bool{}}
	for _, d := range MetricCatalog() {
		names := make([]string, 0, len(d.Dimensions))
		for k := range d.Dimensions {
			names = append(names, k)
		}
		sort.Strings(names)
		var expand func(int, map[string]string)
		expand = func(i int, labels map[string]string) {
			if i < len(names) {
				for _, v := range d.Dimensions[names[i]] {
					next := copyLabels(labels)
					next[names[i]] = v
					expand(i+1, next)
				}
				return
			}
			key, ok := metricKey(d, labels)
			if !ok || len(m.cells) >= maxMetricSeries {
				panic("invalid fixed metric catalog")
			}
			m.cells[key] = &metricCell{definition: d, labels: copyLabels(labels), reason: "origin_unavailable", distribution: make([]uint64, len(d.Bounds)+1)}
			m.keys = append(m.keys, key)
		}
		expand(0, map[string]string{})
	}
	sort.Strings(m.keys)
	return m
}
func copyLabels(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels))
	for k, v := range labels {
		out[k] = v
	}
	return out
}
func metricKey(d MetricDefinition, labels map[string]string) (string, bool) {
	if len(labels) != len(d.Dimensions) {
		return "", false
	}
	keys := make([]string, 0, len(labels))
	for k, v := range labels {
		allowed := false
		for _, candidate := range d.Dimensions[k] {
			if v == candidate {
				allowed = true
			}
		}
		if !allowed {
			return "", false
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := []string{d.Name}
	for _, k := range keys {
		parts = append(parts, k+"="+labels[k])
	}
	return strings.Join(parts, "|"), true
}
func finiteMetric(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0 && v <= 1e15 }
func (m *Metrics) loss()          { m.dropped.Add(1) }
func (m *Metrics) AttachOrigin(origin string) bool {
	if !m.mu.TryLock() {
		m.loss()
		return false
	}
	defer m.mu.Unlock()
	found := false
	for _, c := range m.cells {
		if c.definition.Origin == origin {
			found = true
			if c.definition.Type != "gauge" {
				c.available = true
				c.reason = ""
			}
		}
	}
	if found {
		m.origins[origin] = true
	}
	return found
}
func (m *Metrics) record(name string, labels map[string]string, value float64, histogram bool) bool {
	if !finiteMetric(value) {
		m.loss()
		return false
	}
	if !m.mu.TryLock() {
		m.loss()
		return false
	}
	defer m.mu.Unlock()
	// Definition is found from a finite prebuilt set; no caller-created series.
	var d MetricDefinition
	found := false
	for _, key := range m.keys {
		if m.cells[key].definition.Name == name {
			d = m.cells[key].definition
			found = true
			break
		}
	}
	if !found || (histogram && d.Type != "histogram") || (!histogram && d.Type != "counter") {
		m.loss()
		return false
	}
	key, ok := metricKey(d, labels)
	if !ok {
		m.loss()
		return false
	}
	c := m.cells[key]
	if !m.origins[d.Origin] {
		m.loss()
		return false
	}
	if histogram {
		if c.count >= 1e15 || c.sum+value > 1e15 {
			m.loss()
			return false
		}
		c.count++
		c.sum += value
		if c.intervalMin == nil || value < *c.intervalMin {
			v := value
			c.intervalMin = &v
		}
		if c.intervalMax == nil || value > *c.intervalMax {
			v := value
			c.intervalMax = &v
		}
		slot := sort.SearchFloat64s(d.Bounds, value)
		c.distribution[slot]++
		c.value = float64(c.count)
	} else {
		if c.value+value > 1e15 {
			m.loss()
			return false
		}
		c.value += value
	}
	return true
}
func (m *Metrics) Add(name string, labels map[string]string, value uint64) bool {
	return m.record(name, labels, float64(value), false)
}
func (m *Metrics) Observe(name string, labels map[string]string, seconds float64) bool {
	return m.record(name, labels, seconds, true)
}

// SampleAt is called only by the sampler, after probes finish outside all
// execution locks. Missing probes stay null; absence is never zero-filled.
func (m *Metrics) SampleAt(at time.Time, probes map[string]ProbeValue) bool {
	at = at.UTC()
	if !m.mu.TryLock() {
		m.loss()
		return false
	}
	defer m.mu.Unlock()
	if at.Before(m.resetAt) || at.Unix() < 0 {
		m.loss()
		return false
	}
	// Fixed UTC slots avoid treating ticker jitter as a fabricated extra sample.
	at = at.Truncate(MetricSampleInterval)
	if !m.sampledAt.IsZero() && !at.After(m.sampledAt) {
		m.loss()
		return false
	}
	gap := !m.sampledAt.IsZero() && at.Sub(m.sampledAt) > MetricSampleInterval
	if gap {
		m.partial = true
	}
	minute := at.Unix() / 60
	index := int(minute % MetricHistoryMinutes)
	for _, key := range m.keys {
		c := m.cells[key]
		d := c.definition
		if d.Type == "gauge" {
			probe, ok := probes[d.Name]
			if ok && probe.Value != nil && finiteMetric(*probe.Value) {
				c.value = *probe.Value
				c.available = true
				c.reason = ""
			} else {
				c.available = false
				c.reason = "probe_unavailable"
			}
		}
		bucket := &c.history[index]
		start := at.Truncate(time.Minute)
		if !bucket.Start.Equal(start) {
			*bucket = MetricBucket{Start: start, Reason: "sampling_in_progress"}
			if start.Before(m.resetAt) {
				bucket.invalid = true
				bucket.Reason = "boot_boundary"
			}
		}
		if gap {
			bucket.invalid = true
			bucket.Reason = "missing_samples"
		}
		if !c.available {
			bucket.Reason = c.reason
			bucket.invalid = true
			c.hasLast = false
			continue
		}
		bucket.Samples++
		value := c.value
		if d.Type != "histogram" {
			bucket.Count++
			bucket.Sum += value
		}
		if d.Type != "histogram" && (bucket.Min == nil || value < *bucket.Min) {
			v := value
			bucket.Min = &v
		}
		if d.Type != "histogram" && (bucket.Max == nil || value > *bucket.Max) {
			v := value
			bucket.Max = &v
		}
		if d.Type != "gauge" {
			// Counter and histogram history is based on observations, not replay.
			// Crossing a missing sample invalidates rate/complete coverage.
			if c.hasLast && at.Sub(c.lastAt) <= MetricSampleInterval {
				delta := value - c.lastValue
				if bucket.Delta == nil {
					v := float64(0)
					bucket.Delta = &v
				}
				*bucket.Delta += delta
				rate := delta / at.Sub(c.lastAt).Seconds()
				bucket.Rate = &rate
			} else {
				bucket.Delta = nil
				bucket.Rate = nil
				bucket.Reason = "baseline_missing"
				bucket.invalid = true
			}
			if d.Type == "histogram" {
				if !gap {
					bucket.Count += c.count - c.lastCount
					bucket.Sum += c.sum - c.lastSum
				}
				if !gap && c.intervalMin != nil && (bucket.Min == nil || *c.intervalMin < *bucket.Min) {
					bucket.Min = cloneFloat(c.intervalMin)
				}
				if !gap && c.intervalMax != nil && (bucket.Max == nil || *c.intervalMax > *bucket.Max) {
					bucket.Max = cloneFloat(c.intervalMax)
				}
				c.intervalMin = nil
				c.intervalMax = nil
				c.lastCount = c.count
				c.lastSum = c.sum
			}
		}
		c.hasLast = true
		c.lastAt = at
		c.lastValue = value
		c.lastSlot = minute
		bucket.Complete = bucket.Samples == 4 && !bucket.invalid && m.dropped.Load() == 0
		if bucket.Complete {
			bucket.Reason = ""
		}
	}
	m.sampledAt = at
	return true
}
func cloneFloat(v *float64) *float64 {
	if v == nil {
		return nil
	}
	copy := *v
	return &copy
}
func metricHistoryIndex(minute int64) int {
	n := minute % MetricHistoryMinutes
	if n < 0 {
		n += MetricHistoryMinutes
	}
	return int(n)
}
func (m *Metrics) Snapshot() MetricsSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := MetricsSnapshot{BootID: m.boot, ResetAt: m.resetAt, SampledAt: m.sampledAt, State: "ready", Dropped: m.dropped.Load(), Catalog: MetricCatalog(), Series: make([]MetricSeries, 0, len(m.keys))}
	if m.sampledAt.IsZero() {
		out.State = "initializing"
	} else if m.partial || out.Dropped > 0 {
		out.State = "partial"
	}
	for _, key := range m.keys {
		c := m.cells[key]
		point := MetricPoint{Name: c.definition.Name, Labels: copyLabels(c.labels), Reason: c.reason, Count: c.count, Sum: c.sum}
		if c.available && c.definition.Type != "histogram" {
			v := c.value
			point.Value = &v
		}
		if c.definition.Type == "histogram" {
			point.Distribution = append([]uint64(nil), c.distribution...)
		}
		series := MetricSeries{MetricPoint: point, History: make([]MetricBucket, 0, MetricHistoryMinutes)}
		if !m.sampledAt.IsZero() {
			latest := m.sampledAt.UTC().Truncate(time.Minute)
			for i := MetricHistoryMinutes - 1; i >= 0; i-- {
				start := latest.Add(-time.Duration(i) * time.Minute)
				bucket := c.history[metricHistoryIndex(start.Unix()/60)]
				if !bucket.Start.Equal(start) {
					reason := "missing_samples"
					if start.Before(m.resetAt) {
						reason = "not_retained_before_boot"
					}
					bucket = MetricBucket{Start: start, Reason: reason}
				}
				bucket.Min = cloneFloat(bucket.Min)
				bucket.Max = cloneFloat(bucket.Max)
				bucket.Delta = cloneFloat(bucket.Delta)
				bucket.Rate = cloneFloat(bucket.Rate)
				series.History = append(series.History, bucket)
			}
		}
		out.Series = append(out.Series, series)
	}
	return out
}
