package servicectl

import "time"

// RepositoryDocsInspectionState is a fixed-size retained episode on the
// existing source registration. The zero Result denotes legacy/unobserved
// authority, not success. Values contain no raw error, path or source bytes.
// Value semantics preserve snapshot isolation without shared mutable pointers.
type RepositoryDocsInspectionState struct {
	Result           string    `json:"result,omitempty"`
	LastAttemptAt    time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt    time.Time `json:"last_success_at,omitempty"`
	FirstFailureAt   time.Time `json:"first_failure_at,omitempty"`
	LastFailureAt    time.Time `json:"last_failure_at,omitempty"`
	LastFailureClass string    `json:"last_failure_class,omitempty"`
}

func (s RepositoryDocsInspectionState) completed(result, failure string, at time.Time) RepositoryDocsInspectionState {
	s.Result, s.LastAttemptAt = result, at
	if result == "succeeded" {
		s.LastSuccessAt = at
	} else if result == "failed" {
		if s.FirstFailureAt.IsZero() || s.LastSuccessAt.After(s.LastFailureAt) {
			s.FirstFailureAt = at
		}
		s.LastFailureAt, s.LastFailureClass = at, failure
	}
	return s
}

func inspectionHealthReason(class string) string {
	switch class {
	case "git_store":
		return "source_unavailable"
	case "policy":
		return "source_policy_invalid"
	case "revision_set":
		return "source_state_unavailable"
	}
	return ""
}

func legacyInspectionFailure(class string) string {
	switch class {
	case "git_store_unavailable":
		return "git_store"
	case "repository_docs_policy_invalid":
		return "policy"
	case "repository_docs_status_failed":
		return "revision_set"
	}
	return ""
}

// Public status never returns arbitrary enum text from a malformed retained
// registry. Private authority remains intact so health can mark it unknown.
func (s RepositoryDocsInspectionState) public() RepositoryDocsInspectionState {
	switch s.Result {
	case "", "failed", "succeeded", "disabled":
	default:
		s.Result = "unknown"
	}
	if s.LastFailureClass != "" && inspectionHealthReason(s.LastFailureClass) == "" {
		s.LastFailureClass = ""
	}
	return s
}

func (s RepositoryDocsInspectionState) valid() bool {
	if s.LastAttemptAt.IsZero() || s.LastSuccessAt.After(s.LastAttemptAt) || s.LastFailureAt.After(s.LastAttemptAt) {
		return false
	}
	if s.LastFailureAt.IsZero() != s.FirstFailureAt.IsZero() || s.FirstFailureAt.After(s.LastFailureAt) {
		return false
	}
	if s.LastFailureAt.IsZero() {
		if s.LastFailureClass != "" {
			return false
		}
	} else if inspectionHealthReason(s.LastFailureClass) == "" {
		return false
	}
	switch s.Result {
	case "failed":
		return s.LastFailureAt.Equal(s.LastAttemptAt) && !s.LastSuccessAt.After(s.LastFailureAt)
	case "succeeded":
		return s.LastSuccessAt.Equal(s.LastAttemptAt)
	case "disabled":
		return true
	}
	return false
}
