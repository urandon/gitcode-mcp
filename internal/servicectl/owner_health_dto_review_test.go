//go:build darwin || linux

package servicectl

import (
	"encoding/json"
	"gitcode-mcp/internal/localauthority"
	"testing"
)

func TestReviewRuntimeHealthRejectsImpossibleDerivedClaims(t *testing.T) {
	for _, cut := range []string{"pending_current", "unqualified_counts"} {
		t.Run(cut, func(t *testing.T) {
			req, v := healthWireExample()
			switch cut {
			case "pending_current":
				v.Repair = "pending"
				seq := uint64(0)
				v.Projection = localauthority.RuntimeProjectionHealth{State: "current", IndexedSequence: &seq}
			case "unqualified_counts":
				v.Tracker = localauthority.RuntimeTrackerHealth{State: "corrupt", Writer: "unavailable"}
				v.Knowledge = localauthority.RuntimeKnowledgeHealth{State: "unavailable"}
			}
			raw, err := json.Marshal(v)
			if err != nil {
				t.Fatal("closed result fixture unavailable")
			}
			if _, err := authorityRuntimeResult("AuthorityV1.RuntimeHealth", req, raw); err == nil {
				t.Fatal("protected result consumer accepted an impossible current-readiness combination")
			}
		})
	}
}
