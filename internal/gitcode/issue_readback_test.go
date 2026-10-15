package gitcode

import (
	"fmt"
	"testing"
)

func TestIssueReadbackFromErrorIsBounded(t *testing.T) {
	phase := ErrWriteMutationPhase{Phase: "readback", MutationAttempted: true,
		Cause:         ErrValidationFailed{Field: "milestone", Message: "mismatch"},
		IssueReadback: &IssueUpdateReadback{State: "closed", MilestonePresent: true}}
	got := IssueReadbackFromError(fmt.Errorf("wrapped: %w", phase))
	if got == nil || got.State != "closed" || !got.MilestonePresent || got.MilestoneMatches {
		t.Fatal("missing bounded canonical mismatch")
	}
	got.State = "changed copy"
	if phase.IssueReadback.State != "closed" {
		t.Fatal("projection aliases mutable error evidence")
	}
	phase.IssueReadback.State = "opaque-provider-state"
	if got := IssueReadbackFromError(phase); got == nil || got.State != "unknown" {
		t.Fatal("unbounded provider state exposed")
	}
	phase.Phase = "patch"
	if IssueReadbackFromError(phase) != nil {
		t.Fatal("patch failure fabricated a canonical readback")
	}
	phase.Phase = "readback"
	phase.Cause = ErrNetworkUnavailable{}
	if IssueReadbackFromError(phase) != nil {
		t.Fatal("transport failure fabricated a canonical mismatch")
	}
}
