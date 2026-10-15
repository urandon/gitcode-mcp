package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/service"
)

func TestIssuePartialReadbackCLI(t *testing.T) {
	err := service.ErrWriteFailure{Code: "write_ambiguous_remote", Cause: gitcode.ErrWriteMutationPhase{
		Phase: "readback", MutationAttempted: true,
		Cause:         gitcode.ErrValidationFailed{Field: "milestone", Message: "issue update readback does not match the requested milestone"},
		IssueReadback: &gitcode.IssueUpdateReadback{State: "closed", MilestonePresent: true}}}
	for _, format := range []string{"json", "text"} {
		var out bytes.Buffer
		if writeCommandError(&out, format, startupPlan{ProviderMode: "live-http"}, err) == 0 {
			t.Fatal("partial write exited successfully")
		}
		if strings.Contains(out.String(), "opaque-provider-sentinel") || !strings.Contains(out.String(), "closed") {
			t.Fatalf("unsafe or missing readback evidence: %s", out.String())
		}
		if format == "json" {
			var payload struct {
				IssueReadback *gitcode.IssueUpdateReadback `json:"issue_readback"`
			}
			if json.Unmarshal(out.Bytes(), &payload) != nil || payload.IssueReadback == nil || !payload.IssueReadback.MilestonePresent || payload.IssueReadback.MilestoneMatches {
				t.Fatal("missing structured partial readback")
			}
		}
	}
}
