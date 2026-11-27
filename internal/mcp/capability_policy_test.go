package mcp

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"gitcode-mcp/internal/capability"
)

func testInstancePolicy(t *testing.T, allow ...string) *capability.Policy {
	t.Helper()
	p, err := capability.CompilePolicy(false, capability.PolicyLayer{Source: "machine", Specification: &capability.Specification{Ceiling: capability.Rule{Allow: allow}}})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCapabilityPolicyHidesAndRejectsBeforeBootstrap(t *testing.T) {
	for _, local := range []bool{false, true} {
		h := NewRPCHandlerWithToolAccess(nil, ToolAccessWrite)
		h.SetCapabilityPolicy(testInstancePolicy(t, "get_source"))
		if local {
			h.SetLocalProjects(nil)
		}
		// No service or daemon exists. Denied calls must not reach either.
		for _, name := range []string{"merge_pr", "sync_live", "local_project_mutate"} {
			resp := mcpObservationCall(t, h, name, `"not-an-argument-object"`)
			if resp.Error == nil || resp.Error.Data.Code != "tool_disabled_by_policy" {
				t.Fatalf("%s did not fail before argument/bootstrap: %+v", name, resp.Error)
			}
		}
		resp := mcpObservationCall(t, h, "capability_policy_status", `{}`)
		if resp.Error != nil {
			t.Fatal(resp.Error)
		}
		b, _ := json.Marshal(resp)
		if !strings.Contains(string(b), h.policy.Fingerprint()) {
			t.Fatal("handler clone lost policy")
		}
		var out strings.Builder
		s := NewWithToolAccess(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`+"\n"), &out, io.Discard, nil, nil, ToolAccessWrite)
		s.SetCapabilityPolicy(h.policy)
		if local {
			s.SetLocalProjects(nil)
		}
		if err := s.Serve(); err != nil {
			t.Fatal(err)
		}
		var wire struct {
			Result toolsListResult `json:"result"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(out.String())), &wire); err != nil {
			t.Fatal(err)
		}
		for _, tool := range wire.Result.Tools {
			if tool.Name != "get_source" && tool.Name != "capability_policy_status" {
				t.Fatalf("hidden tool discovered: %s", tool.Name)
			}
		}
	}
}

func TestCapabilityPolicyConcurrentInstancesDoNotShareRole(t *testing.T) {
	for _, allow := range [][]string{{"get_source"}, {"add_issue_comment", "repo_status"}} {
		h := NewRPCHandlerWithToolAccess(nil, ToolAccessWrite)
		h.SetCapabilityPolicy(testInstancePolicy(t, allow...))
		// Direct in-memory HTTP handler uses a fresh Server per request.
		var req request
		if err := json.Unmarshal([]byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`), &req); err != nil {
			t.Fatal(err)
		}
		out, ok := h.Handle(context.Background(), req)
		if !ok {
			t.Fatal("no response")
		}
		encoded, err := json.Marshal(out)
		if err != nil {
			t.Fatal(err)
		}
		var wire struct {
			Result toolsListResult `json:"result"`
		}
		if err := json.Unmarshal(encoded, &wire); err != nil {
			t.Fatal(err)
		}
		for _, tool := range wire.Result.Tools {
			if tool.Name != allow[0] && tool.Name != "capability_policy_status" && tool.Name != "repo_status" {
				t.Fatal("cross-instance authority")
			}
		}
	}
}
