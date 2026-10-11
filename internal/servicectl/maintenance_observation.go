package servicectl

import (
	"context"

	"gitcode-mcp/internal/observability"
)

const maxMaintenanceObservations = 128

type maintenanceObservationKey struct{}
type maintenanceObservationState struct{ state, failure string }
type maintenanceObservationCapture struct {
	identity             jobObservationIdentity
	scope, source        string
	previousRegistration string
	resolution           int
	before, after        maintenanceObservationState
}

// Operation-local bounded capture only. All writers are synchronous owners on
// the calling goroutine; no spawned job retains this diagnostic context.
type maintenanceObservationBuffer struct {
	collector   *observability.Collector
	metrics     *observability.Metrics
	captures    [maxMaintenanceObservations]maintenanceObservationCapture
	count       int
	lost        uint64
	resolutions int
}

// Establish this before acquiring any outer authority lock. Inner owners share
// the boundary; only the outermost caller flushes after all its locks unwind.
func beginMaintenanceObservation(ctx context.Context, jobs *JobManager) (context.Context, func()) {
	if ctx.Value(maintenanceObservationKey{}) != nil || jobs == nil {
		return ctx, func() {}
	}
	collector := jobs.observation.Load()
	var metrics *observability.Metrics
	if sink := jobs.metrics.Load(); sink != nil {
		metrics = sink.metrics
	}
	if collector == nil && metrics == nil {
		return ctx, func() {}
	}
	buffer := &maintenanceObservationBuffer{collector: collector, metrics: metrics}
	return context.WithValue(ctx, maintenanceObservationKey{}, buffer), buffer.flush
}

func maintenanceObservedState(entry *MaintenanceEntry) maintenanceObservationState {
	if entry == nil {
		return maintenanceObservationState{state: "absent"}
	}
	state := entry.State
	switch state {
	case "enrolled", "disabled", "ready", "degraded", "backfilling", "indexing", "refreshing", "cache_schema_blocked":
	default:
		state = "unknown"
	}
	return maintenanceObservationState{state: state, failure: maintenanceObservedFailure(entry.LastErrorClass)}
}

func maintenanceObservedSourceState(source *repositoryDocsRegisteredSource) maintenanceObservationState {
	if source == nil {
		return maintenanceObservationState{state: "absent"}
	}
	state := source.State.State
	switch state {
	case "registered", "not_indexed", "indexing", "disabled", "ready", "degraded":
	default:
		state = "unknown"
	}
	return maintenanceObservationState{state: state, failure: maintenanceObservedFailure(source.State.LastErrorClass)}
}

func maintenanceObservedFailure(class string) string {
	switch class {
	case "":
		return ""
	case "cache_unreadable", "cache_replaced", "cache_inspection_timeout":
		return "cache"
	case "identity_unresolved", "identity_conflict", "cache_clone_conflict":
		return "identity"
	case "content_state_failed":
		return "content"
	case "maintenance_frontiers_read_failed":
		return "frontier"
	case "embedding_namespaces_read_failed", "rag_coverage_read_failed":
		return "embedding"
	case "retry_correlation_persist_failed":
		return "retry"
	case "sync_schedule_failed":
		return "sync_schedule"
	case "rag_schedule_failed":
		return "rag_schedule"
	case "repository_docs_status_failed", "repository_docs_policy_invalid", "git_store_unavailable", "repository_docs_schedule_failed":
		return "repository_docs"
	case "cache_schema_blocked":
		return "cache_schema_blocked"
	default:
		return "operation_failed"
	}
}

// Capture happens under the existing registry lock after the existing save.
// It hashes nothing, invokes no sink/callback, and never performs telemetry I/O.
func captureMaintenanceObservation(ctx context.Context, before maintenanceObservationState, entry *MaintenanceEntry, persisted bool) {
	if entry == nil {
		return
	}
	captureMaintenanceTransition(ctx, before, maintenanceObservedState(entry), entry, "registration", "", persisted)
}

// Source confirmation requires both the existing save and current originating
// authority. Retained stale-owner changes are count-only loss, not certification.
func captureMaintenanceSourceObservation(ctx context.Context, before maintenanceObservationState, entry *MaintenanceEntry, source *repositoryDocsRegisteredSource, confirmed bool) {
	if source == nil {
		return
	}
	captureMaintenanceTransition(ctx, before, maintenanceObservedSourceState(source), entry, "repository_docs", source.State.SourceRegistrationID, confirmed)
}

func captureMaintenanceTransition(ctx context.Context, before, after maintenanceObservationState, entry *MaintenanceEntry, scope, source string, confirmed bool) {
	buffer, _ := ctx.Value(maintenanceObservationKey{}).(*maintenanceObservationBuffer)
	if buffer == nil || entry == nil {
		return
	}
	if before == after {
		return
	}
	if !confirmed {
		buffer.lost++
		return
	}
	// A disabled event sink has no admission buffer to overflow. Independent
	// metrics still account for an actual unconfirmed execution boundary.
	if buffer.collector == nil {
		return
	}
	if after.state == "unknown" || entry.CacheUUID == "" || len(entry.CacheUUID) > 4096 || entry.RegistrationID == "" || len(entry.RegistrationID) > 4096 || entry.RepoID == "" || len(entry.RepoID) > 4096 || (scope == "repository_docs" && (source == "" || len(source) > 4096)) || buffer.count == len(buffer.captures) {
		buffer.lost++
		return
	}
	buffer.captures[buffer.count] = maintenanceObservationCapture{
		identity: jobObservationIdentity{cache: entry.CacheUUID, registration: entry.RegistrationID, repo: entry.RepoID}, scope: scope, source: source, before: before, after: after,
	}
	buffer.count++
}

func (b *maintenanceObservationBuffer) flush() {
	if b.lost > 0 {
		if b.collector != nil {
			b.collector.UnconfirmedExecutions(b.lost)
		}
		if b.metrics != nil {
			b.metrics.LostObservations(b.lost)
		}
	}
	if b.collector == nil {
		return
	}
	var resolution int
	var resolutionCorrelation observability.Ref
	for i := 0; i < b.count; i++ {
		capture := b.captures[i]
		var correlation observability.Ref
		if capture.resolution > 0 {
			if capture.resolution != resolution {
				resolution, resolutionCorrelation = capture.resolution, b.collector.MaintenanceTransitionCorrelation()
			}
			correlation = resolutionCorrelation
		} else {
			correlation = b.collector.MaintenanceTransitionCorrelation()
		}
		b.collector.Emit(observability.MaintenanceStateChanged, observability.Context{
			Cache:                observability.KnownRef("cache", capture.identity.cache),
			Registration:         observability.KnownRef("registration", capture.identity.registration),
			Repo:                 observability.KnownRef("repo", capture.identity.repo),
			Correlation:          correlation,
			Source:               observability.KnownRef("source", capture.source),
			PreviousRegistration: observability.KnownRef("registration", capture.previousRegistration),
			ExecutionDetails:     observability.ExecutionDetails{Phase: "maintenance", MaintenanceScope: capture.scope, PreviousState: capture.before.state, PreviousFailureClass: capture.before.failure, Outcome: capture.after.state, FailureClass: capture.after.failure},
		})
	}
}
