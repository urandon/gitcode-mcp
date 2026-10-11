package observability

import "context"

type metricContextKey struct{}

type observationContextKey struct{}
type observationBinding struct {
	collector *Collector
	refs      Context
}

// WithObservation copies only already resolved opaque refs into a private
// process-local carrier. It accepts a concrete collector, never a callback.
// Per-event execution details are supplied separately at the actual boundary.
func WithObservation(ctx context.Context, collector *Collector, refs Context) context.Context {
	if collector == nil {
		return ctx
	}
	refs.ExecutionDetails = ExecutionDetails{}
	refs.Stage = Ref{}  // stage identity belongs to one confirmed event, not a worker
	refs.Source = Ref{} // source transition identity also belongs to one event
	return context.WithValue(ctx, observationContextKey{}, observationBinding{collector, refs})
}

// EmitFromContext inherits correlation but cannot change execution, retry,
// deadlines or cancellation. Rejected observations are not retried.
func EmitFromContext(ctx context.Context, code Code, details ExecutionDetails) bool {
	binding, ok := ctx.Value(observationContextKey{}).(observationBinding)
	if !ok {
		return false
	}
	refs := binding.refs
	refs.ExecutionDetails = details
	return binding.collector.Emit(code, refs)
}

// HasObservation checks only the private process-local carrier, never storage.
func HasObservation(ctx context.Context) bool {
	_, ok := ctx.Value(observationContextKey{}).(observationBinding)
	return ok
}

// WithMetrics carries a trusted concrete sink through daemon-owned execution.
// It does not install an arbitrary callback or alter cancellation/deadlines.
// Foreground callers without this private process-local value are unobserved.
func WithMetrics(ctx context.Context, metrics *Metrics) context.Context {
	if metrics == nil {
		return ctx
	}
	return context.WithValue(ctx, metricContextKey{}, metrics)
}

func MetricsFromContext(ctx context.Context) *Metrics {
	metrics, _ := ctx.Value(metricContextKey{}).(*Metrics)
	return metrics
}
