package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"gitcode-mcp/internal/service"
	"strings"
	"testing"
)

func TestWikiWriteErrorExposesSafeReconciliation(t *testing.T) {
	attempted := true
	err := service.ErrWriteFailure{Code: "write_ambiguous_remote", RemoteID: "Home.md", IdempotencyKey: "wiki-key", WritePhase: "readback", MutationAttempted: &attempted, ProviderFailureClass: "schema_decode", Reconciliation: "reuse same key for GET-only reconciliation", Cause: errors.New("opaque-private-provider-body")}
	for _, format := range []string{"json", "text"} {
		var out bytes.Buffer
		if writeCommandError(&out, format, startupPlan{ProviderMode: "live-http"}, err) == 0 {
			t.Fatal("success exit for ambiguity")
		}
		if strings.Contains(out.String(), "opaque-private") || !strings.Contains(out.String(), "wiki-key") || !strings.Contains(out.String(), "GET-only") {
			t.Fatalf("unsafe or incomplete output: %s", out.String())
		}
		if format == "json" {
			var payload map[string]any
			if e := json.Unmarshal(out.Bytes(), &payload); e != nil {
				t.Fatal(e)
			}
			if payload["mutation_attempted"] != true || payload["write_phase"] != "readback" || payload["remote_path"] != "Home.md" || payload["provider_failure_class"] != "schema_decode" {
				t.Fatalf("missing evidence: %+v", payload)
			}
		}
	}
}
