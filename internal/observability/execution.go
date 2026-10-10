package observability

// ExecutionDetails is a closed catalog contract, not free-form metadata. Every
// field is validated for both admission and replay. Owners may provide a finite
// attempt ordinal only when it is known from the actual execution policy loop.
// Zero means unspecified, never a fabricated first attempt.
type ExecutionDetails struct {
	Phase        string `json:"phase,omitempty"`
	Collection   string `json:"collection,omitempty"`
	WorkKind     string `json:"work_kind,omitempty"`
	Outcome      string `json:"outcome,omitempty"`
	FailureClass string `json:"failure_class,omitempty"`
	Attempt      uint32 `json:"attempt,omitempty"`
}

const MaxExecutionAttempt = 1024

func executionCode(code Code) bool {
	switch code {
	case JobAdmitted, JobTerminal, ProviderAttempt, SyncCommitted, RetryAdmitted, WriterContended:
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
