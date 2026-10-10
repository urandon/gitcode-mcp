package observability

import "context"

type metricContextKey struct{}

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
