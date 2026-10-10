package servicectl

import (
	"context"

	"gitcode-mcp/internal/observability"
)

// Copy only primitive identity under the existing authority lock. Opaque refs
// and correlation are derived after unlocking; no source/error bytes are read.
type jobObservationIdentity struct{ job, cache, registration, repo string }

func observationIdentity(job Job) jobObservationIdentity {
	return jobObservationIdentity{job.ID, job.CacheUUID, job.RegistrationID, job.RepoID}
}

func (i jobObservationIdentity) refs(collector *observability.Collector) observability.Context {
	return observability.Context{
		Job: observability.KnownRef("job", i.job), Cache: observability.KnownRef("cache", i.cache),
		Registration: observability.KnownRef("registration", i.registration), Repo: observability.KnownRef("repo", i.repo),
		Correlation: observability.KnownRef("correlation", collector.BootID()+"\x00"+i.job),
	}
}

// SetObservation is called before daemon publication. An atomic pointer keeps
// the actual admission/terminal paths free of another execution-lock read.
func (m *JobManager) SetObservation(collector *observability.Collector) {
	m.observation.Store(collector)
}

func (m *JobManager) withJobObservation(ctx context.Context, job Job) context.Context {
	if collector := m.observation.Load(); collector != nil && job.ID != "" {
		return observability.WithObservation(ctx, collector, observationIdentity(job).refs(collector))
	}
	return ctx
}

func (m *JobManager) emitJobObservation(identity jobObservationIdentity, code observability.Code, details observability.ExecutionDetails) {
	if collector := m.observation.Load(); collector != nil && identity.job != "" {
		refs := identity.refs(collector)
		refs.ExecutionDetails = details
		collector.Emit(code, refs)
	}
}

func jobObservationFailure(class string) string {
	// Only a closed class mapping crosses the diagnostic boundary. Unknown
	// typed execution classes remain operation_failed, never free-form text.
	switch class {
	case "timeout", "provider_timeout":
		return "timeout"
	case "rate_limited", "provider_rate_limited":
		return "rate_limited"
	case "forbidden", "provider_forbidden", "provider_unauthorized":
		return "forbidden"
	case "invalid", "provider_invalid_response", "provider_unsupported_response":
		return "invalid"
	case "provider_failed", "provider_fetch_failed", "network_unavailable":
		return "provider_failed"
	default:
		return "operation_failed"
	}
}
