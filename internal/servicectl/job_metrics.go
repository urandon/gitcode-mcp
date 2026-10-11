package servicectl

import (
	"context"
	"errors"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/observability"
)

// jobMetrics is a concrete, volatile sink, never a caller-supplied callback.
// It carries no execution authority or per-job deduplication map. Transition
// identity is decided while holding the job lock; recording happens after it.
type jobMetrics struct{ metrics *observability.Metrics }

type jobMetricState struct {
	kind, status      string
	started, finished time.Time
	identity          jobObservationIdentity
	errorClass        string
}

func metricState(job Job) jobMetricState {
	state := jobMetricState{kind: job.Type, status: job.Status, identity: observationIdentity(job), errorClass: job.ErrorClass}
	if job.StartedAt != nil {
		state.started = *job.StartedAt
	}
	if job.FinishedAt != nil {
		state.finished = *job.FinishedAt
	}
	return state
}

// SetMetrics attaches once during daemon construction, before work is exposed.
// The atomic pointer also makes teardown and deterministic fixture replacement
// safe without adding an observation lock to admission or terminalization.
func (m *JobManager) SetMetrics(metrics *observability.Metrics) {
	if metrics == nil {
		m.metrics.Store(nil)
		return
	}
	metrics.AttachOrigin("jobs")
	metrics.AttachOrigin("writer")
	m.metrics.Store(&jobMetrics{metrics: metrics})
}

// Only typed writer denial is evidence; a queue, fence, coalesced job or raw
// error string is not a contention attempt. Callers record after unlocking and
// releasing their commit turn, never from the cache adapter as well.
func (m *JobManager) observeWriterContention(kind string, err error) {
	m.observeWriterContentionAt(context.Background(), kind, err, jobObservationIdentity{})
}

func (m *JobManager) observeWriterContentionInContext(ctx context.Context, kind string, err error) {
	m.observeWriterContentionAt(ctx, kind, err, jobObservationIdentity{})
}

func (m *JobManager) observeWriterContentionForIdentity(kind string, err error, identity jobObservationIdentity) {
	m.observeWriterContentionAt(context.Background(), kind, err, identity)
}

func (m *JobManager) observeWriterContentionAt(ctx context.Context, kind string, err error, identity jobObservationIdentity) {
	var busy ErrCacheWriterBusy
	var lease cache.ErrLockContention
	if !errors.As(err, &busy) && !errors.As(err, &lease) {
		return
	}
	if sink := m.metrics.Load(); sink != nil {
		sink.metrics.Add("writer_contention", map[string]string{"kind": metricWorkKind(kind)}, 1)
	}
	details := observability.ExecutionDetails{Phase: "writer", Outcome: "denied", FailureClass: "writer_busy", WorkKind: metricWorkKind(kind)}
	if observability.HasObservation(ctx) {
		// Loss is recorded by the existing binding. Never retry a rejected event
		// through the manager or turn a worker boundary into another scope.
		observability.EmitFromContext(ctx, observability.WriterContended, details)
		return
	}
	if collector := m.observation.Load(); collector != nil {
		refs := identity.refs(collector)
		if identity.job == "" {
			// Pre-admission/direct writes have no job. This is a fixed boot/work-
			// kind scope, not a synthetic request identity or storage lookup.
			refs.Correlation = observability.KnownRef("correlation", collector.BootID()+"\x00writer\x00"+details.WorkKind)
		}
		refs.ExecutionDetails = details
		collector.Emit(observability.WriterContended, refs)
	}
}

func observeCollectionRetry(ctx context.Context, collection string, attempt int) {
	if attempt <= 1 || ctx.Err() != nil {
		return
	}
	metrics := observability.MetricsFromContext(ctx)
	knownCollection := true
	switch collection {
	case "issues", "issue_comments", "wiki", "pulls", "pr_comments":
	default:
		knownCollection = false
		collection = "other"
	}
	if metrics != nil {
		metrics.Add("retry_admissions", map[string]string{"collection": collection}, 1)
	}
	// A retry is actual scheduler entry, not a timer or durable checkpoint.
	// Its diagnostic event is independent of optional metric attachment.
	if knownCollection && attempt <= observability.MaxExecutionAttempt {
		observability.EmitFromContext(ctx, observability.RetryAdmitted, observability.ExecutionDetails{Phase: "retry", Collection: collection, Outcome: "admitted", Attempt: uint32(attempt)})
	} else {
		observability.OmitRetryFromContext(ctx)
	}
}

func metricWorkKind(kind string) string {
	switch kind {
	case SyncJobType:
		return "sync"
	case RAGIndexJobType:
		return "rag"
	case RepositoryDocsIndexJobType:
		return "repository_docs"
	default:
		return "other"
	}
}

func (m *JobManager) observeJobAdmission(state jobMetricState) {
	if sink := m.metrics.Load(); sink != nil {
		sink.metrics.Add("jobs_admitted", map[string]string{"kind": metricWorkKind(state.kind)}, 1)
	}
	m.emitJobObservation(state.identity, observability.JobAdmitted, observability.ExecutionDetails{Phase: "admission", Outcome: "queued"})
}

func (m *JobManager) withJobMetrics(ctx context.Context) context.Context {
	if sink := m.metrics.Load(); sink != nil {
		return observability.WithMetrics(ctx, sink.metrics)
	}
	return ctx
}

func (m *JobManager) observeJobTerminal(previousStatus string, state jobMetricState, persisted bool) {
	// Loading, recovery of an interrupted snapshot, coalescing, and repeated
	// progress/terminal writes are not new execution outcomes.
	if !jobActiveStatus(previousStatus) || !jobTerminalStatus(state.status) {
		return
	}
	sink := m.metrics.Load()
	if !persisted {
		if collector := m.observation.Load(); collector != nil {
			collector.UnconfirmedExecution()
		}
		if sink != nil {
			sink.metrics.LostObservation()
		}
		return
	}
	details := observability.ExecutionDetails{Phase: "completion", Outcome: state.status}
	switch state.status {
	case JobStatusCancelled, JobStatusInterrupted, JobStatusSuperseded:
		details.FailureClass = state.status
	case JobStatusFailed:
		details.FailureClass = jobObservationFailure(state.errorClass)
	}
	m.emitJobObservation(state.identity, observability.JobTerminal, details)
	if sink == nil {
		return
	}
	kind := metricWorkKind(state.kind)
	sink.metrics.Add("jobs_terminal", map[string]string{"kind": kind, "outcome": state.status}, 1)
	if state.started.IsZero() || state.finished.IsZero() || state.finished.Before(state.started) {
		return
	}
	metric := ""
	switch kind {
	case "sync":
		metric = "sync_duration"
	case "rag":
		metric = "rag_duration"
	}
	if metric != "" {
		sink.metrics.Observe(metric, map[string]string{"outcome": state.status}, state.finished.Sub(state.started).Seconds())
	}
}

const maxJobMetricRows = 32768

// MetricProbes never waits for the execution lock. A busy or oversized authority
// is unavailable, not a partial total, fabricated zero or unbounded lock hold.
func (m *JobManager) MetricProbes() map[string]observability.ProbeValue {
	values := map[string]observability.ProbeValue{
		"jobs_queued":    observability.Unavailable(),
		"jobs_active":    observability.Unavailable(),
		"writer_waiters": observability.Unavailable(),
	}
	if !m.mu.TryLock() {
		return values
	}
	// Map lengths are O(1). Count all rows, including nil and terminal jobs;
	// only queue headers are visited because each queue length is also O(1).
	// Subtraction avoids overflow from summing arbitrary authority sizes.
	if len(m.jobs) > maxJobMetricRows || len(m.syncCommitQueues) > maxJobMetricRows-len(m.jobs) {
		m.mu.Unlock()
		return values
	}
	var queued, active, waiters int
	for _, job := range m.jobs {
		if job == nil {
			continue
		}
		switch job.Status {
		case JobStatusQueued:
			queued++
		case JobStatusRunning, JobStatusCancelling:
			active++
		}
	}
	for _, queue := range m.syncCommitQueues {
		// The first entry owns the turn. Only following entries await it.
		waiters += max(0, len(queue)-1)
	}
	m.mu.Unlock()
	values["jobs_queued"] = observability.Available(float64(queued))
	values["jobs_active"] = observability.Available(float64(active))
	values["writer_waiters"] = observability.Available(float64(waiters))
	return values
}
