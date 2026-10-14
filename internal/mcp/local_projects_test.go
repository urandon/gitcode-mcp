package mcp

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"gitcode-mcp/internal/servicectl"
)

func TestLocalProjectDiscoveryPolicyAndClosedArgumentsBeforeIPC(t *testing.T) {
	for _, access := range []ToolAccess{ToolAccessRead, ToolAccessWrite} {
		h := NewRPCHandlerWithToolAccess(nil, access)
		h.SetToolBootstrap(func(context.Context, string, json.RawMessage) (*RPCHandler, error) {
			panic("local tool bootstrapped remote dependencies")
		})
		h.SetLocalProjects(func() (*servicectl.RPCClient, error) { panic("invalid/local discovery entered IPC") })
		id := json.RawMessage(`1`)
		resp, ok := h.Handle(context.Background(), request{JSONRPC: "2.0", ID: &id, Method: "tools/list"})
		if !ok || resp.Error != nil {
			t.Fatal("local discovery failed")
		}
		var list toolsListResult
		if json.Unmarshal(resp.Result, &list) != nil {
			t.Fatal("invalid local tool list")
		}
		want := 4
		if access == ToolAccessWrite {
			want = 5
		}
		if len(list.Tools) != want {
			t.Fatal("local discovery exposed remote or hid required tools")
		}
		names := map[string]bool{}
		for _, tool := range list.Tools {
			if names[tool.Name] {
				t.Fatal("duplicate local tool")
			}
			names[tool.Name] = true
			if !strings.HasPrefix(tool.Name, "local_project_") || tool.InputSchema.AdditionalProperties == nil || *tool.InputSchema.AdditionalProperties {
				t.Fatal("nonlocal or open schema in local discovery")
			}
		}
		for _, name := range []string{"local_project_status", "local_project_get", "local_project_list", "local_project_history"} {
			if !names[name] {
				t.Fatal("required local read tool missing")
			}
		}
		if names["local_project_mutate"] != (access == ToolAccessWrite) {
			t.Fatal("local mutation discovery policy changed")
		}
		for _, test := range []struct{ name, args, code string }{
			{"local_project_get", `{"project":"lab","kind":"issue","id":"ISSUE-1","path":"private-coordinate"}`, "local_git_invalid_argument"},
			{"local_project_get", `{"project":"lab","project":"other","kind":"issue","id":"ISSUE-1"}`, "local_git_invalid_argument"},
			{"local_project_get", `{"project":"lab","kind":"issue","id":"ISSUE-1","max_commits":1}`, "local_git_invalid_argument"},
			{"local_project_get", `{"project":null,"kind":"issue","id":"ISSUE-1"}`, "local_git_invalid_argument"},
			{"local_project_get", `{"kind":"issue","id":"ISSUE-1"}`, "local_git_invalid_argument"},
			{"local_project_list", `{"project":"lab","kind":"issue","limit":129}`, "local_git_invalid_argument"},
			{"local_project_mutate", `{"project":"lab","mutation":{"operation":"create_issue","write_mode":"live","idempotency_key":"key"}}`, "local_git_invalid_argument"},
			{"local_project_mutate", `{"project":"lab","mutation":{"operation":"create_issue","write_mode":"local","idempotency_key":"key","body":"` + string([]byte{0xff}) + `"}}`, "local_git_invalid_argument"},
			{"create_issue", `{}`, "local_git_unsupported_capability"},
		} {
			if access == ToolAccessRead && test.name == "local_project_mutate" {
				test.code = "tool_disabled_by_policy"
			}
			resp := mcpObservationCall(t, h, test.name, test.args)
			if resp.Error == nil || resp.Error.Data.Code != test.code {
				t.Fatal("local boundary did not reject before IPC")
			}
			encoded, _ := json.Marshal(resp)
			if strings.Contains(string(encoded), "private-coordinate") {
				t.Fatal("local error reflected input")
			}
		}
	}
}

func TestLocalProjectStdioDiscoveryWithNoDaemon(t *testing.T) {
	input := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}` + "\n"
	s := NewWithToolAccess(strings.NewReader(input), io.Discard, io.Discard, nil, nil, ToolAccessRead)
	s.SetLocalProjects(func() (*servicectl.RPCClient, error) { panic("discovery connected to daemon") })
	if s.Serve() != nil {
		t.Fatal("local stdio discovery failed")
	}
}

func TestLocalProjectStdioFrameBoundDoesNotEnterIPC(t *testing.T) {
	s := NewWithToolAccess(strings.NewReader(strings.Repeat("x", (1<<20)+1)+"\n"), io.Discard, io.Discard, nil, nil, ToolAccessWrite)
	s.SetLocalProjects(func() (*servicectl.RPCClient, error) { panic("oversized frame entered IPC") })
	if s.Serve() == nil {
		t.Fatal("oversized local frame not bounded")
	}
}
