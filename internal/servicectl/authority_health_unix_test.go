//go:build darwin || linux

package servicectl

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority"
)

func healthWireExample() (localauthority.RuntimeHealthRequest, localauthority.RuntimeHealthView) {
	req := localauthority.RuntimeHealthRequest{ProjectID: authorityExampleProject, RuntimeBinding: strings.Repeat("a", 64)}
	sequence := uint64(0)
	v := localauthority.RuntimeHealthView{Schema: 1, ProjectID: req.ProjectID, RuntimeBinding: req.RuntimeBinding, RetrievedAt: "2026-10-14T00:00:00Z", Phase: "serving",
		Tracker: localauthority.RuntimeTrackerHealth{State: "ready", Writer: "owned", SourceSequence: &sequence}, Knowledge: localauthority.RuntimeKnowledgeHealth{State: "uninitialized"}, Projection: localauthority.RuntimeProjectionHealth{State: "missing"},
		RecoveryJobs: localauthority.RuntimeJobsHealth{State: "ready", Counts: &localauthority.RuntimeJobCounts{}}, IndexJobs: localauthority.RuntimeJobsHealth{State: "missing"}, Repair: "missing"}
	return req, v
}

func TestAuthorityRuntimeHealthValidatesDiscardedResultsAndOldInventory(t *testing.T) {
	req, valid := healthWireExample()
	if localauthority.VerifyRuntimeHealth(req, valid) != nil {
		t.Fatal("synthetic closed health fixture invalid")
	}
	for _, field := range []string{"valid", "epoch", "project", "sequence", "projection", "count", "raw", "oversize"} {
		t.Run(field, func(t *testing.T) {
			body, _ := json.Marshal(valid)
			var v map[string]any
			_ = json.Unmarshal(body, &v)
			switch field {
			case "epoch":
				v["runtime_binding"] = strings.Repeat("b", 64)
			case "project":
				v["project_id"] = recoveryExampleJob
			case "sequence":
				v["tracker"].(map[string]any)["source_sequence"] = -1
			case "projection":
				v["projection"].(map[string]any)["state"] = "current"
			case "count":
				v["recovery_jobs"].(map[string]any)["counts"].(map[string]any)["running"] = 129
			case "raw":
				v["raw_error"] = "synthetic-private-provider-body"
			case "oversize":
				v["raw_error"] = strings.Repeat("x", localauthority.MaxRuntimeHealthBytes)
			}
			client, done := recoveryPeerFixture(t, authorityRuntimeCapabilities(), v, nil)
			err := client.Call(context.Background(), "AuthorityV1.RuntimeHealth", req, nil)
			<-done
			if (err == nil) != (field == "valid") {
				t.Fatal("discarded health result bypassed exact bounded request/response qualification")
			}
		})
	}
	for _, caps := range []authority.Negotiation{authority.ContractsOnly(), func() authority.Negotiation {
		v := authorityRuntimeCapabilities()
		v.Methods = append([]string{}, v.Methods[:len(v.Methods)-1]...)
		return v
	}()} {
		client, done := recoveryPeerFixture(t, caps, nil, nil)
		if client.Call(context.Background(), "AuthorityV1.RuntimeHealth", req, nil) == nil {
			t.Fatal("old or ordinary remote daemon invented current owner health")
		}
		<-done
	}
}

func TestAuthorityRuntimeHealthRequestRejectsSelectorsBeforeTransport(t *testing.T) {
	req, _ := healthWireExample()
	body, _ := json.Marshal(req)
	for _, field := range []string{"path", "repo_id", "profile", "endpoint", "start", "repair", "idempotency_key", "credential"} {
		var v map[string]any
		_ = json.Unmarshal(body, &v)
		v[field] = "synthetic-private-selector"
		bad, _ := json.Marshal(v)
		if _, err := authorityParams("AuthorityV1.RuntimeHealth", bad); err == nil {
			t.Fatal("health accepted an untrusted selection or effect field")
		}
	}
	remote := RPCServer{}
	if _, err := remote.dispatchAuthority(context.Background(), "AuthorityV1.RuntimeHealth", body); err == nil {
		t.Fatal("ordinary remote runtime admitted local health")
	}
}
