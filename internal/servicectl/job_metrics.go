package servicectl

import (
	"context"
	"time"

	"gitcode-mcp/internal/observability"
)

// jobMetrics is a concrete, volatile sink, never a caller-supplied callback.
// It carries no execution authority or per-job deduplication map. Transition
// identity is decided while holding the job lock; recording happens after it.
type jobMetrics struct{ metrics *observability.Metrics }

type jobMetricState struct {
	kind, status      string
	started, finished time.Time
}

func metricState(job Job) jobMetricState {
	state := jobMetricState{kind: job.Type, status: job.Status}
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
	m.metrics.Store(&jobMetrics{metrics: metrics})
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

func (m *JobManager) observeJobAdmission(kind string) {
	if sink := m.metrics.Load(); sink != nil {
		sink.metrics.Add("jobs_admitted", map[string]string{"kind": metricWorkKind(kind)}, 1)
	}
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
	if sink == nil {
		return
	}
	if !persisted {
		sink.metrics.LostObservation()
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

// MetricProbes never waits for the execution lock. A busy authority is an
// unavailable observation, not a fabricated zero or an execution delay.
func (m *JobManager) MetricProbes() map[string]observability.ProbeValue {
	values := map[string]observability.ProbeValue{
		"jobs_queued":    observability.Unavailable(),
		"jobs_active":    observability.Unavailable(),
		"writer_waiters": observability.Unavailable(),
	}
	if !m.mu.TryLock() {
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
