//go:build !windows

package servicectl

import (
	"context"
	"encoding/json"
	"testing"

	"gitcode-mcp/internal/authority"
)

func TestAuthorityRepairAuthenticatedForgedResponsesRejectedWhenDiscarded(t *testing.T) {
	for method, req := range repairIPCRequests() {
		for _, change := range []string{"project", "raw", "omitted"} {
			body, _ := json.Marshal(repairExampleResult(method))
			var bad map[string]any
			_ = json.Unmarshal(body, &bad)
			switch change {
			case "project":
				target := bad
				if v, ok := bad["plan"].(map[string]any); ok {
					target = v
				}
				if v, ok := bad["repair"].(map[string]any); ok {
					target = v
				}
				target["project_id"] = "55555555-5555-4555-8555-555555555555"
			case "raw":
				bad["path"] = "synthetic-private-peer-body"
			case "omitted":
				delete(bad, "schema")
			}
			client, done := recoveryPeerFixture(t, authorityRuntimeCapabilities(), bad, nil)
			requireAuthorityCode(t, client.Call(context.Background(), method, req, nil), authority.InvalidContract)
			<-done
			if client.nextID.Load() != 2 {
				t.Fatal("repair client retried a refused response")
			}
		}
	}
}

func TestAuthorityRepairPriorInventoryNeverReceivesWrite(t *testing.T) {
	for _, count := range []int{5, 10, 15, 18} {
		caps := authorityRuntimeCapabilities()
		caps.Methods = append([]string{}, caps.Methods[:count]...)
		client, done := recoveryPeerFixture(t, caps, nil, nil)
		requireAuthorityCode(t, client.Call(context.Background(), "AuthorityV1.ApplyProjectionRepair", repairIPCRequests()["AuthorityV1.ApplyProjectionRepair"], nil), authority.Unsupported)
		<-done
		if client.nextID.Load() != 1 {
			t.Fatal("old runtime inventory received repair execution")
		}
	}
}
