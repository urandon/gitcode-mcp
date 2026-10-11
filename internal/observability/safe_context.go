package observability

import (
	"context"
	"encoding/json"
	"time"
)

// SafeContext deliberately excludes broad Admin/cache/config projections,
// resource labels, source bodies and free-form errors. A selection is resolved
// only against evidence already returned to the caller, never an authority.
type SafeContext struct {
	Schema            int       `json:"schema"`
	Protocol          string    `json:"protocol"`
	BootID            string    `json:"boot_id,omitempty"`
	Generation        string    `json:"generation,omitempty"`
	MetricsBootID     string    `json:"metrics_boot_id,omitempty"`
	ObservedAt        time.Time `json:"observed_at"`
	ExecutionState    string    `json:"execution_state"`
	ExecutionCoverage string    `json:"execution_coverage"`
	ObservationState  string    `json:"observation_state"`
	LedgerState       string    `json:"ledger_state"`
	OutputState       string    `json:"output_state"`
	MetricsState      string    `json:"metrics_state"`
	DroppedEvents     uint64    `json:"dropped_events"`
	LossEpoch         uint64    `json:"loss_epoch"`
	DroppedMetrics    uint64    `json:"dropped_metrics"`
	Event             *Event    `json:"event,omitempty"`
	Incident          *Incident `json:"incident,omitempty"`
}
type ContextSelection struct {
	EventID     string `json:"event_id,omitempty"`
	IncidentRef string `json:"incident_ref,omitempty"`
}

func finiteOr(value, fallback string, allowed ...string) string {
	for _, item := range allowed {
		if value == item {
			return value
		}
	}
	return fallback
}

func BuildSafeContext(ctx context.Context, summary Summary, page *Page, selection ContextSelection) (SafeContext, error) {
	invalid := QueryError{Code: "invalid_query", Reason: "unsupported_context_selection"}
	if err := ctx.Err(); err != nil {
		return SafeContext{}, err
	}
	if selection.EventID != "" && selection.IncidentRef != "" || selection.EventID != "" && !hexID.MatchString(selection.EventID) || selection.IncidentRef != "" && !validRef(selection.IncidentRef, "correlation") {
		return SafeContext{}, invalid
	}
	for _, identity := range []string{summary.Collector.BootID, summary.Collector.Generation, summary.MetricsBootID} {
		if identity != "" && !hexID.MatchString(identity) {
			return SafeContext{}, invalid
		}
	}
	o := SafeContext{Schema: 1, Protocol: Protocol, BootID: summary.Collector.BootID, Generation: summary.Collector.Generation, MetricsBootID: summary.MetricsBootID, ObservedAt: summary.ObservedAt.UTC(),
		ExecutionState:    finiteOr(summary.ExecutionHealth.State, "unknown", "healthy", "degraded", "unknown"),
		ExecutionCoverage: finiteOr(summary.ExecutionHealth.Coverage, "unavailable", "retained", "partial", "unavailable"),
		ObservationState:  finiteOr(summary.ObservationHealth.State, "unavailable", "ready", "initializing", "partial", "unavailable"),
		LedgerState:       finiteOr(summary.ObservationHealth.LedgerState, "unavailable", "initializing", "ready", "partial", "unavailable"),
		OutputState:       finiteOr(summary.ObservationHealth.OutputState, "unavailable", "managed", "migration_required", "legacy_unbounded", "unsupported", "unavailable"),
		MetricsState:      finiteOr(summary.ObservationHealth.MetricsState, "unavailable", "initializing", "ready", "partial", "unavailable"),
		DroppedEvents:     summary.ObservationHealth.DroppedEvents, LossEpoch: summary.ObservationHealth.LossEpoch, DroppedMetrics: summary.ObservationHealth.DroppedMetrics}
	if selection.EventID != "" {
		if page == nil || len(page.Events) > MaxPageEvents {
			return SafeContext{}, invalid
		}
		for _, e := range page.Events {
			if err := ctx.Err(); err != nil {
				return SafeContext{}, err
			}
			if e.EventID == selection.EventID {
				if o.Event != nil || !e.valid() {
					return SafeContext{}, invalid
				}
				copy := e
				o.Event = &copy
			}
		}
		if o.Event == nil {
			return SafeContext{}, QueryError{Code: "observation_unavailable", Reason: "selection_not_retained"}
		}
	}
	if selection.IncidentRef != "" {
		if len(summary.ExecutionHealth.Incidents) > MaxIncidents {
			return SafeContext{}, invalid
		}
		for _, i := range summary.ExecutionHealth.Incidents {
			if err := ctx.Err(); err != nil {
				return SafeContext{}, err
			}
			if i.IncidentRef != selection.IncidentRef {
				continue
			}
			if o.Incident != nil || !validCopyIncident(i) {
				return SafeContext{}, invalid
			}
			copy := i
			if i.LastSuccessAt != nil {
				at := *i.LastSuccessAt
				copy.LastSuccessAt = &at
			}
			if i.RetryAfter != nil {
				at := *i.RetryAfter
				copy.RetryAfter = &at
			}
			o.Incident = &copy
		}
		if o.Incident == nil {
			return SafeContext{}, QueryError{Code: "observation_unavailable", Reason: "selection_not_retained"}
		}
	}
	if _, err := MarshalSafeContext(ctx, o); err != nil {
		return SafeContext{}, err
	}
	return o, nil
}
func validCopyIncident(i Incident) bool {
	if !validRef(i.IncidentRef, "correlation") || i.IncidentRef == "" || i.FirstObservedAt.IsZero() || i.LastObservedAt.Before(i.FirstObservedAt) || finiteOr(i.State, "", "active", "recovered", "retrying", "waiting_writer", "interrupted") == "" {
		return false
	}
	return validHealthEvidence(HealthEvidence{Cohort: Ref{i.IncidentRef}, Origin: i.Origin, Collection: i.Collection, Phase: i.Phase, Signal: "failed", Reason: i.Reason, Coverage: i.Coverage, At: i.FirstObservedAt, Refs: Context{Job: Ref{i.JobRef}, Registration: Ref{i.RegistrationRef}, Cache: Ref{i.CacheRef}, Repo: Ref{i.RepoRef}, Source: Ref{i.SourceRef}}})
}

// MarshalSafeContext is also a defensive ceiling for frontend/CLI copy helpers.
// Callers construct the DTO through BuildSafeContext, not arbitrary input JSON.
func MarshalSafeContext(ctx context.Context, value SafeContext) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	invalid := QueryError{Code: "invalid_query", Reason: "unsupported_context_selection"}
	if value.Schema != 1 || value.Protocol != Protocol || value.Event != nil && value.Incident != nil || value.Event != nil && !value.Event.valid() || value.Incident != nil && !validCopyIncident(*value.Incident) {
		return nil, invalid
	}
	for _, identity := range []string{value.BootID, value.Generation, value.MetricsBootID} {
		if identity != "" && !hexID.MatchString(identity) {
			return nil, invalid
		}
	}
	for _, state := range []struct {
		value   string
		allowed []string
	}{
		{value.ExecutionState, []string{"healthy", "degraded", "unknown"}},
		{value.ExecutionCoverage, []string{"retained", "partial", "unavailable"}},
		{value.ObservationState, []string{"ready", "initializing", "partial", "unavailable"}},
		{value.LedgerState, []string{"initializing", "ready", "partial", "unavailable"}},
		{value.OutputState, []string{"managed", "migration_required", "legacy_unbounded", "unsupported", "unavailable"}},
		{value.MetricsState, []string{"initializing", "ready", "partial", "unavailable"}},
	} {
		if finiteOr(state.value, "", state.allowed...) == "" {
			return nil, invalid
		}
	}
	b, err := json.Marshal(value)
	if err != nil || len(b) > MaxSafeCopyBytes {
		return nil, QueryError{Code: "response_limit", Reason: "safe_context_limit"}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return b, nil
}
