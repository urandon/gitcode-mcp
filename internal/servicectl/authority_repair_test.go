package servicectl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority"
)

func repairIPCRequests() map[string]any {
	return map[string]any{
		"AuthorityV1.PlanProjectionRepair":  localauthority.ProjectionRepairPlanRequest{ProjectID: authorityExampleProject},
		"AuthorityV1.ApplyProjectionRepair": localauthority.ProjectionRepairRequest{ProjectID: authorityExampleProject, PlanID: strings.Repeat("a", 64), IdempotencyKey: "synthetic-private-repair-key", WriteMode: "local"},
		"AuthorityV1.ProjectionRepair":      localauthority.ProjectionRepairLookup{ProjectID: authorityExampleProject, RepairID: recoveryExampleJob},
		"AuthorityV1.ProjectionRepairs":     localauthority.ProjectionRepairPlanRequest{ProjectID: authorityExampleProject},
	}
}

func repairWireDigest(domain string, request, response any) string {
	body, _ := json.Marshal(struct {
		Request  any `json:"request"`
		Response any `json:"response"`
	}{request, response})
	digest := sha256.Sum256(append([]byte("authority.v1/projection_repair/"+domain+"\x00"), body...))
	return hex.EncodeToString(digest[:])
}

func repairExampleResult(method string) any {
	v := localauthority.ProjectionRepairView{Schema: 1, ProjectID: authorityExampleProject, RepairID: recoveryExampleJob, PlanID: strings.Repeat("a", 64), Sequence: 7, State: "confirmed", Generation: 3, Files: 1, Bytes: 1, RestartRequired: true}
	switch method {
	case "AuthorityV1.PlanProjectionRepair":
		p := localauthority.ProjectionRepairPlan{Schema: 1, ProjectID: v.ProjectID, PlanID: v.PlanID, Sequence: 7, LedgerState: "missing", State: "ready", Action: "quarantine", Files: 1, Bytes: 1, RestartRequired: true}
		return localauthority.ProjectionRepairPlanResult{Schema: 1, Digest: repairWireDigest("plan", repairIPCRequests()[method], p), Plan: p}
	case "AuthorityV1.ApplyProjectionRepair":
		return localauthority.ProjectionRepairConfirmation{Schema: 1, Digest: repairWireDigest("apply", repairIPCRequests()[method], v), Repair: v}
	case "AuthorityV1.ProjectionRepairs":
		h := localauthority.ProjectionRepairHistory{Schema: 1, ProjectID: v.ProjectID, State: "ready", Generation: 3, Retained: 1, Repairs: []localauthority.ProjectionRepairView{v}}
		body, _ := json.Marshal(h)
		digest := sha256.Sum256(append([]byte("authority.v1/projection_repair/history\x00"), body...))
		h.SnapshotDigest = hex.EncodeToString(digest[:])
		return h
	}
	return v
}

func TestAuthorityRepairParamsFailBeforeTransportOrRemoteDispatch(t *testing.T) {
	for method, req := range repairIPCRequests() {
		t.Run(method, func(t *testing.T) {
			body, _ := json.Marshal(req)
			if _, err := authorityParams(method, body); err != nil {
				t.Fatal("valid repair request refused")
			}
			for _, field := range []string{"path", "repo_id", "actor", "provider", "markdown", "profile", "credential", "destination", "namespace", "cursor", "limit"} {
				var bad map[string]any
				_ = json.Unmarshal(body, &bad)
				bad[field] = "synthetic-private-selector"
				client := RPCClient{Network: "mem", Address: "missing-repair-fixture"}
				requireAuthorityCode(t, client.Call(context.Background(), method, bad, nil), authority.InvalidContract)
				if client.nextID.Load() != 0 {
					t.Fatal("invalid repair request reached transport")
				}
			}
			_, err := (RPCServer{}).dispatch(context.Background(), method, body)
			requireAuthorityCode(t, err, authority.NoAuthority)
			client := RPCClient{Network: "tcp", Address: "synthetic-private.invalid:1"}
			requireAuthorityCode(t, client.Call(context.Background(), method, req, nil), authority.PolicyDenied)
			if client.nextID.Load() != 0 {
				t.Fatal("repair control entered TCP transport")
			}
			for _, bad := range [][]byte{[]byte(`{}`), []byte(`null`), append(body, body...), append([]byte(`{"project_id":"12345678-1234-4234-8234-123456789abc",`), body[1:]...)} {
				_, err := authorityParams(method, bad)
				requireAuthorityCode(t, err, authority.InvalidContract)
			}
		})
	}
	for _, field := range []string{"mode", "key", "plan"} {
		req := repairIPCRequests()["AuthorityV1.ApplyProjectionRepair"].(localauthority.ProjectionRepairRequest)
		switch field {
		case "mode":
			req.WriteMode = "live"
		case "key":
			req.IdempotencyKey = ""
		case "plan":
			req.PlanID = "synthetic-private-invalid-plan"
		}
		body, _ := json.Marshal(req)
		_, err := authorityParams("AuthorityV1.ApplyProjectionRepair", body)
		requireAuthorityCode(t, err, authority.InvalidContract)
	}
}

func TestAuthorityRepairProtectedResultBindingAndRequiredFields(t *testing.T) {
	for method, req := range repairIPCRequests() {
		body, _ := json.Marshal(repairExampleResult(method))
		if _, err := authorityRuntimeResult(method, req, body); err != nil {
			t.Fatal("valid protected repair result refused")
		}
		for _, raw := range [][]byte{[]byte(`{}`), []byte(`null`), append(body, body...), []byte(strings.Repeat("x", authority.MaxProtectedResultBytes+1))} {
			_, err := authorityRuntimeResult(method, req, raw)
			requireAuthorityCode(t, err, authority.InvalidContract)
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(body, &fields)
		for field := range fields {
			var bad map[string]json.RawMessage
			_ = json.Unmarshal(body, &bad)
			delete(bad, field)
			raw, _ := json.Marshal(bad)
			_, err := authorityRuntimeResult(method, req, raw)
			requireAuthorityCode(t, err, authority.InvalidContract)
		}
		for _, nested := range []string{"plan", "repair"} {
			if fields[nested] == nil {
				continue
			}
			var members map[string]json.RawMessage
			_ = json.Unmarshal(fields[nested], &members)
			for field := range members {
				var bad map[string]json.RawMessage
				_ = json.Unmarshal(fields[nested], &bad)
				delete(bad, field)
				next, _ := json.Marshal(bad)
				copyFields := map[string]json.RawMessage{}
				for k, v := range fields {
					copyFields[k] = v
				}
				copyFields[nested] = next
				raw, _ := json.Marshal(copyFields)
				_, err := authorityRuntimeResult(method, req, raw)
				requireAuthorityCode(t, err, authority.InvalidContract)
			}
		}
	}
	req := repairIPCRequests()["AuthorityV1.ApplyProjectionRepair"].(localauthority.ProjectionRepairRequest)
	confirmation := repairExampleResult("AuthorityV1.ApplyProjectionRepair").(localauthority.ProjectionRepairConfirmation)
	for _, changed := range []string{"key", "plan", "project", "pending", "digest"} {
		bad, result := req, confirmation
		switch changed {
		case "key":
			bad.IdempotencyKey = "synthetic-other-key"
		case "plan":
			bad.PlanID = strings.Repeat("b", 64)
		case "project":
			bad.ProjectID = "55555555-5555-4555-8555-555555555555"
		case "pending":
			result.Repair.State, result.Repair.Generation = "quarantined", 2
			result.Digest = repairWireDigest("apply", req, result.Repair)
		case "digest":
			result.Digest = strings.Repeat("b", 64)
		}
		body, _ := json.Marshal(result)
		_, err := authorityRuntimeResult("AuthorityV1.ApplyProjectionRepair", bad, body)
		requireAuthorityCode(t, err, authority.InvalidContract)
	}
}
