package observability

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

const ReadBudget = 2 * time.Second
const MaxSummaryBytes = 128 << 10
const MaxMetricsBytes = 4 << 20
const MaxSafeCopyBytes = 16 << 10
const Protocol = "observability.v1"

type ReadLimits struct {
	BudgetMilliseconds int `json:"budget_milliseconds"`
	Events             int `json:"events"`
	EventBytes         int `json:"event_bytes"`
	SummaryBytes       int `json:"summary_bytes"`
	MetricsBytes       int `json:"metrics_bytes"`
	SafeCopyBytes      int `json:"safe_copy_bytes"`
	HistoryMinutes     int `json:"history_minutes"`
	TimeSpanSeconds    int `json:"time_span_seconds"`
}

func Limits() ReadLimits {
	return ReadLimits{int(ReadBudget / time.Millisecond), MaxPageEvents, MaxPageBytes, MaxSummaryBytes, MaxMetricsBytes, MaxSafeCopyBytes, MetricHistoryMinutes, int(MaxQuerySpan / time.Second)}
}

type CollectorSnapshot struct {
	BootID         string   `json:"boot_id"`
	Generation     string   `json:"generation"`
	LatestSequence uint64   `json:"latest_sequence"`
	Coverage       Coverage `json:"coverage"`
}

func (c *Collector) SnapshotContext(ctx context.Context) (CollectorSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return CollectorSnapshot{}, err
	}
	if !c.mu.TryLock() {
		return CollectorSnapshot{}, QueryError{Code: "observation_busy", Reason: "ledger_busy"}
	}
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return CollectorSnapshot{}, err
	}
	c.absorbLossLocked()
	latest := uint64(0)
	if len(c.events) > 0 {
		latest = c.events[len(c.events)-1].Sequence
	}
	return CollectorSnapshot{c.boot, c.generation, latest, c.coverage}, nil
}

type Summary struct {
	CopyContext       *SafeContext      `json:"copy_context"`
	Schema            int               `json:"schema"`
	Protocol          string            `json:"protocol"`
	ObservedAt        time.Time         `json:"observed_at"`
	Collector         CollectorSnapshot `json:"collector"`
	MetricsBootID     string            `json:"metrics_boot_id"`
	MetricsResetAt    time.Time         `json:"metrics_reset_at"`
	MetricsSampledAt  time.Time         `json:"metrics_sampled_at"`
	ObservationHealth ObservationHealth `json:"observation_health"`
	ExecutionHealth   ExecutionHealth   `json:"execution_health"`
	Resources         []MetricPoint     `json:"resources"`
	Limits            ReadLimits        `json:"limits"`
}

// Reader composes concrete bounded local observations, not a general snapshot
// callback. Health must be the retained authority projection, never a provider
// probe, credential resolver, cache bootstrap, or broad Admin snapshot.
type Reader struct {
	Collector *Collector
	Metrics   *Metrics
	Health    func(context.Context, time.Time) (ExecutionHealth, error)
	Now       func() time.Time
}

func (r *Reader) Summary(ctx context.Context) (Summary, error) {
	ctx, cancel := context.WithTimeout(ctx, ReadBudget)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Summary{}, err
	}
	if r == nil {
		return Summary{}, QueryError{Code: "observation_unavailable", Reason: "reader_unavailable"}
	}
	at := time.Now().UTC()
	if r.Now != nil {
		at = r.Now().UTC()
	}
	out := Summary{Schema: 1, Protocol: Protocol, ObservedAt: at, Limits: Limits(), Resources: []MetricPoint{}, ExecutionHealth: UnavailableExecutionHealth(at, "authority_missing")}
	out.Collector.Coverage = Coverage{State: "unavailable", OutputState: "unsupported"}
	if r.Collector != nil {
		var err error
		out.Collector, err = r.Collector.SnapshotContext(ctx)
		if err != nil {
			return Summary{}, err
		}
	}
	metrics := MetricsSnapshot{State: "unavailable"}
	if r.Metrics != nil {
		var err error
		metrics, err = r.Metrics.CurrentSnapshotContext(ctx)
		if err != nil {
			return Summary{}, err
		}
	}
	out.MetricsBootID, out.MetricsResetAt, out.MetricsSampledAt = metrics.BootID, metrics.ResetAt, metrics.SampledAt
	out.ObservationHealth = ProjectObservationHealth(out.Collector.Coverage, metrics)
	for _, s := range metrics.Series {
		for _, d := range metrics.Catalog {
			if s.Name == d.Name && d.Type == "gauge" {
				out.Resources = append(out.Resources, s.MetricPoint)
				break
			}
		}
	}
	if r.Health != nil {
		var err error
		out.ExecutionHealth, err = r.Health(ctx, at)
		if err != nil {
			return Summary{}, err
		}
	}
	if err := checkReadSize(ctx, out, MaxSummaryBytes); err != nil {
		return Summary{}, err
	}
	copyContext, err := BuildSafeContext(ctx, out, nil, ContextSelection{})
	if err != nil {
		return Summary{}, err
	}
	out.CopyContext = &copyContext
	if err := checkReadSize(ctx, out, MaxSummaryBytes); err != nil {
		return Summary{}, err
	}
	return out, nil
}

func (r *Reader) Events(ctx context.Context, req EventsRequest) (Page, error) {
	req, err := req.Validate()
	if err != nil {
		return Page{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, ReadBudget)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	if r == nil || r.Collector == nil {
		return Page{}, QueryError{Code: "observation_unavailable", Reason: "ledger_unavailable"}
	}
	out, err := r.Collector.QueryContext(ctx, req.Filter, req.Cursor, req.Limit)
	if err != nil {
		return Page{}, err
	}
	if err := checkReadSize(ctx, out, MaxPageBytes); err != nil {
		return Page{}, err
	}
	return out, nil
}

func (r *Reader) MetricSnapshot(ctx context.Context, req MetricsRequest) (MetricsSnapshot, error) {
	req, err := req.Validate()
	if err != nil {
		return MetricsSnapshot{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, ReadBudget)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return MetricsSnapshot{}, err
	}
	if r == nil || r.Metrics == nil {
		return MetricsSnapshot{}, QueryError{Code: "observation_unavailable", Reason: "metrics_unavailable"}
	}
	out, err := r.Metrics.SnapshotContext(ctx)
	if err != nil {
		return MetricsSnapshot{}, err
	}
	series := out.Series[:0]
	for _, s := range out.Series {
		if req.Name == "" || s.Name == req.Name {
			if len(s.History) > req.HistoryMinutes {
				s.History = s.History[len(s.History)-req.HistoryMinutes:]
			}
			series = append(series, s)
		}
	}
	out.Series = series
	if req.Name != "" {
		for _, d := range out.Catalog {
			if d.Name == req.Name {
				out.Catalog = []MetricDefinition{d}
				break
			}
		}
	}
	if err := checkReadSize(ctx, out, MaxMetricsBytes); err != nil {
		return MetricsSnapshot{}, err
	}
	return out, nil
}

func checkReadSize(ctx context.Context, value any, limit int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b, err := json.Marshal(value)
	if err != nil || len(b) > limit {
		return QueryError{Code: "observation_unavailable", Reason: "response_limit"}
	}
	return ctx.Err()
}

// Revision deliberately excludes poll timestamps, uptime and live request
// counters. It reads existing scalar identities only: polling it cannot create
// the next invalidation, invoke authority/provider probes or start a sampler.
func (r *Reader) Revision(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if r == nil {
		return "", QueryError{Code: "observation_unavailable", Reason: "reader_unavailable"}
	}
	var identity struct {
		Collector   CollectorSnapshot
		MetricsBoot string
		SampledAt   time.Time
		Dropped     uint64
		Partial     bool
	}
	if r.Collector != nil {
		var err error
		identity.Collector, err = r.Collector.SnapshotContext(ctx)
		if err != nil {
			return "", err
		}
	}
	if m := r.Metrics; m != nil {
		if !m.mu.TryLock() {
			return "", QueryError{Code: "observation_busy", Reason: "metrics_busy"}
		}
		identity.MetricsBoot, identity.SampledAt, identity.Dropped, identity.Partial = m.boot, m.sampledAt, m.dropped.Load(), m.partial
		m.mu.Unlock()
	}
	b, err := json.Marshal(identity)
	if err != nil {
		return "", QueryError{Code: "observation_unavailable", Reason: "response_limit"}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "observation-" + hex.EncodeToString(sum[:16]), nil
}
