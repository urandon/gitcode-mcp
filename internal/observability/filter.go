package observability

import (
	"errors"
	"sort"
	"time"
)

const MaxQuerySpan = 7 * 24 * time.Hour

// ValidateFilter is shared by transport decoders and the daemon reader. It
// validates only the fixed event catalog, never resolves a supplied identity.
func ValidateFilter(f Filter) (Filter, error) {
	f, _, err := normalizeFilter(f)
	return f, err
}

func validateFilter(f Filter) (Filter, error) {
	invalid := errors.New("invalid observation filter")
	if f.Stream != "" && f.Stream != "stdout" && f.Stream != "stderr" {
		return f, invalid
	}
	if f.Severity != "" && f.Severity != "info" && f.Severity != "warning" && f.Severity != "error" {
		return f, invalid
	}
	for _, r := range []struct{ value, kind string }{
		{f.JobRef, "job"}, {f.RegistrationRef, "registration"}, {f.CacheRef, "cache"}, {f.RepoRef, "repo"},
		{f.CorrelationRef, "correlation"}, {f.StageRef, "stage"}, {f.SourceRef, "source"}, {f.PreviousRegistrationRef, "registration"},
		{f.ProjectRef, "project"},
	} {
		if !validRef(r.value, r.kind) {
			return f, invalid
		}
	}
	if (f.Since == "") != (f.Until == "") {
		return f, invalid
	}
	if f.Since != "" {
		since, e1 := time.Parse(time.RFC3339Nano, f.Since)
		until, e2 := time.Parse(time.RFC3339Nano, f.Until)
		if e1 != nil || e2 != nil || since.IsZero() || until.IsZero() || f.Since[len(f.Since)-1] != 'Z' || f.Until[len(f.Until)-1] != 'Z' || until.Before(since) || until.Sub(since) > MaxQuerySpan {
			return f, invalid
		}
		f.Since, f.Until = since.UTC().Format(time.RFC3339Nano), until.UTC().Format(time.RFC3339Nano)
	}
	// Compatibility comes from representative catalog-valid shapes. Optional
	// entity references are checked by kind, not by arbitrary label lookup.
	for _, e := range filterCatalogShapes {
		if f.Stream != "" && f.Stream != e.Stream || f.Code != "" && f.Code != e.Code || f.Component != "" && f.Component != e.Component || f.Severity != "" && f.Severity != e.Severity ||
			f.Phase != "" && f.Phase != e.Phase || f.Collection != "" && f.Collection != e.Collection || f.Outcome != "" && f.Outcome != e.Outcome || f.FailureClass != "" && f.FailureClass != e.FailureClass ||
			f.SourceRef != "" && e.SourceRef == "" || f.StageRef != "" && e.StageRef == "" || f.PreviousRegistrationRef != "" && e.PreviousRegistrationRef == "" ||
			f.ProjectRef != "" && e.ProjectRef == "" ||
			localAuthorityCode(e.Code) && (f.JobRef != "" || f.CacheRef != "" || f.RepoRef != "" || f.RegistrationRef != "") ||
			f.JobRef != "" && (e.Code == MaintenanceStateChanged || e.Code == AdminRequestCompleted) ||
			(f.CacheRef != "" || f.RepoRef != "" || f.RegistrationRef != "") && e.Code == AdminRequestCompleted {
			continue
		}
		return f, nil
	}
	return f, invalid
}

func parseFilterTime(value string) time.Time { t, _ := time.Parse(time.RFC3339Nano, value); return t }

func (f Filter) matchSelectors(e Event) bool {
	return (f.Stream == "" || e.Stream == f.Stream) && (f.Severity == "" || e.Severity == f.Severity) && (f.Component == "" || e.Component == f.Component) && (f.Code == "" || e.Code == f.Code) &&
		(f.Phase == "" || e.Phase == f.Phase) && (f.Collection == "" || e.Collection == f.Collection) && (f.Outcome == "" || e.Outcome == f.Outcome) && (f.FailureClass == "" || e.FailureClass == f.FailureClass) &&
		(f.JobRef == "" || e.JobRef == f.JobRef) && (f.RegistrationRef == "" || e.RegistrationRef == f.RegistrationRef) && (f.CacheRef == "" || e.CacheRef == f.CacheRef) && (f.RepoRef == "" || e.RepoRef == f.RepoRef) &&
		(f.CorrelationRef == "" || e.CorrelationRef == f.CorrelationRef) && (f.StageRef == "" || e.StageRef == f.StageRef) && (f.SourceRef == "" || e.SourceRef == f.SourceRef) && (f.PreviousRegistrationRef == "" || e.PreviousRegistrationRef == f.PreviousRegistrationRef) && (f.ProjectRef == "" || e.ProjectRef == f.ProjectRef)
}

// These finite candidates are accepted only through the execution owner's
// validation. That keeps query compatibility in step with admission/replay,
// including neutral info-severity failures and legacy lifecycle records.
var filterCatalogShapes = buildFilterCatalogShapes()

// FilterValues exposes only finite catalog dimensions, never entity identities.
// The returned values describe individual membership; compatibility still
// requires ValidateFilter on the complete query.
func FilterValues() map[string][]string {
	values := map[string]map[string]bool{}
	for _, e := range filterCatalogShapes {
		for key, value := range map[string]string{"severity": e.Severity, "component": e.Component, "code": string(e.Code), "phase": e.Phase, "collection": e.Collection, "outcome": e.Outcome, "failure_class": e.FailureClass} {
			if value == "" {
				continue
			}
			if values[key] == nil {
				values[key] = map[string]bool{}
			}
			values[key][value] = true
		}
	}
	out := map[string][]string{"stream": {"stdout", "stderr"}}
	for key, set := range values {
		for value := range set {
			out[key] = append(out[key], value)
		}
		sort.Strings(out[key])
	}
	return out
}

// EventDetailValues exposes finite execution detail domains for browser
// projection, not additional query selectors. State/failure membership uses
// the same validators as admission and replay, rather than UI-owned guesses.
func EventDetailValues() map[string][]string {
	filters := FilterValues()
	out := map[string][]string{
		"work_kind":         append([]string(nil), workKinds...),
		"admin_route":       append([]string(nil), adminRoutes...),
		"maintenance_scope": {"registration", "repository_docs", "identity_resolution"},
	}
	for _, value := range append(filters["outcome"], "absent", "unknown") {
		if maintenanceState(value) || maintenanceSourceState(value) || maintenanceIdentityPredecessor(value) || value == "absent" || value == "unknown" {
			out["previous_state"] = append(out["previous_state"], value)
		}
	}
	for _, value := range filters["failure_class"] {
		if maintenanceFailure(value) {
			out["previous_failure_class"] = append(out["previous_failure_class"], value)
		}
	}
	for _, values := range out {
		sort.Strings(values)
	}
	return out
}

func buildFilterCatalogShapes() []Event {
	var shapes []Event
	for code, t := range catalog {
		base := Event{Code: code, Component: t.component, Severity: t.severity, Stream: t.stream}
		if !executionCode(code) {
			// Only legacy lifecycle evidence can retain either stream. Actual
			// execution codes cannot be legacy and keep their admitted stream.
			base.Stream = "stdout"
			shapes = append(shapes, base)
			base.Stream = "stderr"
			shapes = append(shapes, base)
			continue
		}
		base.CorrelationRef = KnownRef("correlation", "catalog").value
		if localAuthorityCode(code) {
			base.ProjectRef = KnownRef("project", "catalog").value
		}
		switch code {
		case JobAdmitted, JobTerminal, JobRecovered, SyncCommitted, RetryAdmitted, SyncStageChanged:
			base.JobRef = KnownRef("job", "catalog").value
		}
		if code == SyncStageChanged || code == MaintenanceStateChanged {
			base.CacheRef = KnownRef("cache", "catalog").value
			base.RepoRef = KnownRef("repo", "catalog").value
			base.RegistrationRef = KnownRef("registration", "catalog").value
		}
		if code == SyncStageChanged {
			base.StageRef = KnownRef("stage", "catalog").value
		}
		if code == WriterContended {
			base.WorkKind = "other"
		}
		if code == AdminRequestCompleted {
			base.AdminRoute = "read"
		}
		if code == RetryAdmitted {
			base.Attempt = 2
		}
		phases := map[Code][]string{
			JobAdmitted: {"admission"}, JobTerminal: {"completion"}, JobRecovered: {"recovery"},
			ProviderAttempt: {"fetch", "embedding"}, SyncCommitted: {"commit"}, RetryAdmitted: {"retry"},
			WriterContended: {"writer"}, SyncStageChanged: {"staging"}, AdminRequestCompleted: {"admin"}, MaintenanceStateChanged: {"maintenance"},
			LocalAuthorityCommitted:    {"authority_commit", "document_commit"},
			LocalAuthorityPhaseChanged: {"index_job", "knowledge_recovery", "projection_repair", "runtime"},
		}[code]
		collections := []string{""}
		if code == ProviderAttempt || code == SyncCommitted || code == RetryAdmitted || code == SyncStageChanged {
			collections = []string{"", "issues", "issue_comments", "wiki", "pulls", "pr_comments"}
		}
		scopes := []string{""}
		if code == MaintenanceStateChanged {
			scopes = []string{"registration", "repository_docs", "identity_resolution"}
		}
		for _, scope := range scopes {
			e := base
			e.MaintenanceScope = scope
			if scope != "" {
				e.PreviousState = "unknown"
			}
			if scope == "repository_docs" {
				e.SourceRef = KnownRef("source", "catalog").value
			}
			if scope == "identity_resolution" {
				e.PreviousState = "identity_unresolved"
				e.PreviousRegistrationRef = KnownRef("registration", "previous").value
			}
			for _, phase := range phases {
				e.Phase = phase
				for _, collection := range collections {
					e.Collection = collection
					for _, outcome := range []string{"queued", "succeeded", "cancelled", "interrupted", "superseded", "failed", "resumed", "reconciled", "rejected", "timeout", "rate_limited", "forbidden", "invalid", "admitted", "denied", "staged", "committing", "committed", "waiting_commit", "enrolled", "disabled", "ready", "degraded", "backfilling", "indexing", "refreshing", "cache_schema_blocked", "registered", "not_indexed", "identity_conflict", "cache_clone_conflict", "identity_unresolved", "config_snapshot_invalid", "cache_inspection_timeout", "running", "cancelling", "prepared", "quarantined", "confirmed", "serving", "closing", "stopped"} {
						e.Outcome = outcome
						for _, failure := range []string{"", "cancelled", "timeout", "rate_limited", "forbidden", "invalid", "operation_failed", "provider_failed", "interrupted", "writer_busy", "superseded", "cache", "identity", "content", "frontier", "embedding", "retry", "sync_schedule", "rag_schedule", "repository_docs", "cache_schema_blocked"} {
							e.FailureClass = failure
							// Include same-state identity outcomes via a different valid predecessor.
							if scope == "identity_resolution" && outcome == "identity_unresolved" {
								e.PreviousState = "identity_conflict"
							} else if scope == "identity_resolution" {
								e.PreviousState = "identity_unresolved"
							}
							if e.validExecution() {
								shapes = append(shapes, e)
							}
						}
					}
				}
			}
		}
	}
	return shapes
}
