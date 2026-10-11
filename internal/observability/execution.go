package observability

// ExecutionDetails is a closed catalog contract, not free-form metadata. Every
// field is validated for both admission and replay. Owners may provide a finite
// attempt ordinal only when it is known from the actual execution policy loop.
// Zero means unspecified, never a fabricated first attempt.
type ExecutionDetails struct {
	Phase                string `json:"phase,omitempty"`
	Collection           string `json:"collection,omitempty"`
	WorkKind             string `json:"work_kind,omitempty"`
	Outcome              string `json:"outcome,omitempty"`
	FailureClass         string `json:"failure_class,omitempty"`
	Attempt              uint32 `json:"attempt,omitempty"`
	AdminRoute           string `json:"admin_route,omitempty"`
	MaintenanceScope     string `json:"maintenance_scope,omitempty"`
	PreviousState        string `json:"previous_state,omitempty"`
	PreviousFailureClass string `json:"previous_failure_class,omitempty"`
}

const MaxExecutionAttempt = 1024

func executionCode(code Code) bool {
	switch code {
	case JobAdmitted, JobTerminal, JobRecovered, ProviderAttempt, SyncCommitted, RetryAdmitted, WriterContended, SyncStageChanged, AdminRequestCompleted, MaintenanceStateChanged:
		return true
	default:
		return false
	}
}

func executionCollection(value string) bool {
	switch value {
	case "issues", "issue_comments", "wiki", "pulls", "pr_comments":
		return true
	default:
		return false
	}
}

func executionFailure(value string) bool {
	switch value {
	case "cancelled", "timeout", "rate_limited", "forbidden", "invalid", "operation_failed", "provider_failed", "interrupted":
		return true
	default:
		return false
	}
}

func (e Event) validExecution() bool {
	d := e.ExecutionDetails
	if e.SourceRef != "" && (e.Code != MaintenanceStateChanged || d.MaintenanceScope != "repository_docs") {
		return false
	}
	if e.Code != MaintenanceStateChanged && (d.MaintenanceScope != "" || d.PreviousState != "" || d.PreviousFailureClass != "") {
		return false
	}
	if e.Code != AdminRequestCompleted && d.AdminRoute != "" {
		return false
	}
	if e.Code != SyncStageChanged && e.StageRef != "" {
		return false
	}
	if !executionCode(e.Code) {
		// Old schema-1 lifecycle events remain valid with absent fields. Adding
		// arbitrary execution dimensions to one must not bypass the catalog.
		return d == (ExecutionDetails{})
	}
	if e.Legacy || e.CorrelationRef == "" || d.Attempt > MaxExecutionAttempt {
		return false
	}
	if e.Code != WriterContended && d.WorkKind != "" {
		return false
	}
	switch e.Code {
	case MaintenanceStateChanged:
		if e.JobRef != "" || e.RegistrationRef == "" || e.CacheRef == "" || e.RepoRef == "" || d.Phase != "maintenance" || d.Collection != "" || d.Attempt != 0 {
			return false
		}
		state := maintenanceState
		switch d.MaintenanceScope {
		case "registration":
		case "repository_docs":
			if e.SourceRef == "" {
				return false
			}
			state = maintenanceSourceState
		default:
			return false
		}
		return state(d.Outcome) && (state(d.PreviousState) || d.PreviousState == "absent" || d.PreviousState == "unknown") && maintenanceFailure(d.FailureClass) && maintenanceFailure(d.PreviousFailureClass) && (d.Outcome != d.PreviousState || d.FailureClass != d.PreviousFailureClass) && (d.PreviousState != "absent" || d.PreviousFailureClass == "")
	case AdminRequestCompleted:
		if e.JobRef != "" || e.CacheRef != "" || e.RegistrationRef != "" || e.RepoRef != "" || d.Phase != "admin" || d.Collection != "" || d.Attempt != 0 || d.FailureClass != "" {
			return false
		}
		switch d.AdminRoute {
		case "session", "read", "control", "search", "events", "assets", "unknown":
		default:
			return false
		}
		return d.Outcome == "succeeded" || d.Outcome == "failed" || d.Outcome == "rejected" || d.Outcome == "cancelled"
	case JobRecovered:
		if e.JobRef == "" || d.Phase != "recovery" || d.Collection != "" || d.Attempt != 0 {
			return false
		}
		switch d.Outcome {
		case "resumed", "reconciled":
			return d.FailureClass == ""
		case "rejected":
			return executionFailure(d.FailureClass)
		}
		return false
	case SyncStageChanged:
		if e.JobRef == "" || e.StageRef == "" || e.CacheRef == "" || e.RegistrationRef == "" || e.RepoRef == "" || d.Phase != "staging" || !executionCollection(d.Collection) {
			return false
		}
		switch d.Outcome {
		case "staged", "committing", "committed":
			return d.FailureClass == ""
		case "waiting_commit":
			return d.FailureClass == "writer_busy"
		case "superseded":
			return d.FailureClass == "superseded"
		case "rejected":
			return executionFailure(d.FailureClass) || d.FailureClass == "writer_busy"
		}
		return false
	case WriterContended:
		switch d.WorkKind {
		case "sync", "rag", "repository_docs", "other":
			return d.Phase == "writer" && d.Outcome == "denied" && d.FailureClass == "writer_busy" && d.Collection == "" && d.Attempt == 0
		}
		return false
	case JobAdmitted:
		return e.JobRef != "" && d.Phase == "admission" && d.Outcome == "queued" && d.Collection == "" && d.FailureClass == "" && d.Attempt == 0
	case JobTerminal:
		if e.JobRef == "" || d.Phase != "completion" || d.Collection != "" || d.Attempt != 0 {
			return false
		}
		switch d.Outcome {
		case "succeeded":
			return d.FailureClass == ""
		case "cancelled", "interrupted", "superseded":
			return d.FailureClass == d.Outcome
		case "failed":
			return executionFailure(d.FailureClass) && d.FailureClass != "cancelled" && d.FailureClass != "interrupted"
		}
	case ProviderAttempt:
		if (d.Phase != "fetch" && d.Phase != "embedding") || (d.Collection != "" && (!executionCollection(d.Collection) || d.Phase != "fetch")) {
			return false
		}
		switch d.Outcome {
		case "succeeded":
			return d.FailureClass == ""
		case "cancelled", "timeout", "rate_limited", "forbidden", "invalid":
			return d.FailureClass == d.Outcome
		case "failed":
			return d.FailureClass == "provider_failed"
		}
	case SyncCommitted:
		return e.JobRef != "" && d.Phase == "commit" && d.Outcome == "succeeded" && executionCollection(d.Collection) && d.FailureClass == "" && d.Attempt == 0
	case RetryAdmitted:
		return e.JobRef != "" && d.Phase == "retry" && d.Outcome == "admitted" && executionCollection(d.Collection) && d.FailureClass == "" && d.Attempt >= 2
	}
	return false
}

func maintenanceState(value string) bool {
	switch value {
	case "enrolled", "disabled", "ready", "degraded", "backfilling", "indexing", "refreshing", "cache_schema_blocked":
		return true
	}
	return false
}

func maintenanceFailure(value string) bool {
	switch value {
	case "", "cache", "identity", "content", "frontier", "embedding", "retry", "sync_schedule", "rag_schedule", "repository_docs", "cache_schema_blocked", "operation_failed":
		return true
	}
	return false
}

func maintenanceSourceState(value string) bool {
	switch value {
	case "registered", "not_indexed", "indexing", "disabled", "ready", "degraded":
		return true
	}
	return false
}
