package observability

import "time"

// ObservationHealth describes evidence delivery, not execution outcomes. It is
// intentionally a different DTO from ExecutionHealth and cannot rewrite it.
type ObservationHealth struct {
	State            string     `json:"state"`
	Reason           string     `json:"reason,omitempty"`
	LedgerState      string     `json:"ledger_state"`
	OutputState      string     `json:"output_state"`
	MetricsState     string     `json:"metrics_state"`
	DroppedEvents    uint64     `json:"dropped_events"`
	LossEpoch        uint64     `json:"loss_epoch"`
	DroppedMetrics   uint64     `json:"dropped_metrics"`
	MetricsSampledAt *time.Time `json:"metrics_sampled_at,omitempty"`
}

func ProjectObservationHealth(coverage Coverage, metrics MetricsSnapshot) ObservationHealth {
	out := ObservationHealth{State: "ready", LedgerState: "unavailable", OutputState: "unavailable", MetricsState: "unavailable", DroppedEvents: coverage.Dropped, LossEpoch: coverage.LossEpoch, DroppedMetrics: metrics.Dropped}
	switch coverage.State {
	case "initializing", "ready", "partial":
		out.LedgerState = coverage.State
	}
	switch coverage.OutputState {
	case "managed", "migration_required", "legacy_unbounded", "unsupported":
		out.OutputState = coverage.OutputState
	}
	switch metrics.State {
	case "initializing", "ready", "partial":
		out.MetricsState = metrics.State
	}
	if !metrics.SampledAt.IsZero() {
		at := metrics.SampledAt.UTC()
		out.MetricsSampledAt = &at
	}
	switch {
	case out.LedgerState == "unavailable":
		out.State, out.Reason = "unavailable", "ledger_unavailable"
	case out.MetricsState == "unavailable":
		out.State, out.Reason = "unavailable", "metrics_unavailable"
	case coverage.Dropped > 0 || coverage.LossEpoch > 0 || out.LedgerState == "partial":
		out.State, out.Reason = "partial", "ledger_loss"
	case metrics.Dropped > 0 || out.MetricsState == "partial":
		out.State, out.Reason = "partial", "metric_loss"
	case out.OutputState != "managed":
		out.State, out.Reason = "partial", "output_not_managed"
	case out.LedgerState == "initializing" || out.MetricsSampledAt == nil:
		out.State, out.Reason = "initializing", "initializing"
	}
	return out
}
