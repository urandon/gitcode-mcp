package mcp

import (
	"io"
	"strings"
	"testing"

	"gitcode-mcp/internal/capability"
)

func TestEveryRegisteredMCPToolDeclaresSurfacePolicy(t *testing.T) {
	server := NewWithToolAccess(strings.NewReader(""), io.Discard, io.Discard, nil, nil, ToolAccessWrite)
	registered := server.toolRegistry()
	server.localOnly = true
	for name, tool := range server.toolRegistry() {
		registered[name] = tool
	}
	for name := range registered {
		cap, ok := capability.LookupByMCPName(name)
		if !ok || !cap.MCP.Enabled {
			t.Errorf("registered tool %q lacks an enabled capability declaration", name)
		}
	}
	for _, cap := range capability.Capabilities() {
		_, found := registered[cap.MCPName]
		if cap.MCP.Enabled && !found {
			t.Errorf("declared tool %q is not registered in either supported profile", cap.MCPName)
		}
		if !cap.MCP.Enabled && cap.MCPName != "" && found {
			t.Errorf("disabled tool %q is unexpectedly registered", cap.MCPName)
		}
	}
}

func TestSourceReadDiscoveryMatchesSharedFilters(t *testing.T) {
	server := New(strings.NewReader(""), io.Discard, io.Discard, nil, nil)
	for _, name := range []string{"search_sources", "list_sources"} {
		props := server.toolRegistry()[name].definition.InputSchema.Properties
		if props["provenance"].Type != "string" || len(props["kind"].Enum) != 0 {
			t.Fatalf("%s must expose provenance and extensible cache kind filters", name)
		}
		if props["limit"].Default != 20.0 || *props["limit"].Maximum != 100 {
			t.Fatalf("%s pagination schema differs from service contract", name)
		}
	}
}
