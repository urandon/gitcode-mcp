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
	refs.Stage = Ref{}                // stage identity belongs to one confirmed event, not a worker
	refs.Source = Ref{}               // source transition identity also belongs to one event
	refs.PreviousRegistration = Ref{} // a resolution predecessor is event-owned
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

// OmitRetryFromContext accounts for an actual scheduler admission that cannot
// fit the closed retry catalog. Only an already qualified worker scope may
// affect coverage; no ordinal or unknown collection text is retained. This
// neither admits a fabricated event nor changes the executing retry.
func OmitRetryFromContext(ctx context.Context) {
	binding, ok := ctx.Value(observationContextKey{}).(observationBinding)
	if !ok {
		return
	}
	refs := binding.refs
	// Use fixed catalog details only to validate the inherited scope, never to
	// emit or clamp the actual retry. Malformed references remain unobserved.
	e := Event{Code: RetryAdmitted, JobRef: refs.Job.value, CacheRef: refs.Cache.value,
		RegistrationRef: refs.Registration.value, RepoRef: refs.Repo.value, CorrelationRef: refs.Correlation.value,
		StageRef: refs.Stage.value, SourceRef: refs.Source.value, PreviousRegistrationRef: refs.PreviousRegistration.value,
		ExecutionDetails: ExecutionDetails{Phase: "retry", Collection: "wiki", Outcome: "admitted", Attempt: 2}}
	if !e.validExecution() {
		return
	}
	for _, ref := range [...]struct{ value, kind string }{
		{e.JobRef, "job"}, {e.CacheRef, "cache"}, {e.RegistrationRef, "registration"},
		{e.RepoRef, "repo"}, {e.CorrelationRef, "correlation"},
	} {
		if !validRef(ref.value, ref.kind) {
			return
		}
	}
	binding.collector.UnconfirmedExecution()
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
