package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	"gitcode-mcp/internal/capability"
)

var unsupportedCapabilityToolNames = map[string]bool{
	"add_comment": true,

	"update-issue": true,
	"create-page":  true,
	"update-page":  true,
}

func isUnsupportedCapabilityTool(name string) bool {
	if unsupportedCapabilityToolNames[name] {
		return true
	}
	cap, ok := capability.LookupByMCPName(name)
	return ok && !cap.MCP.Enabled
}

func (s *Server) unsupportedCapabilityHandler(_ context.Context, id *json.RawMessage, name string) {
	if cap, ok := capability.LookupByMCPName(name); ok && !cap.MCP.Enabled && cap.MCP.DisabledReason != "" {
		s.writeError(id, -32601, "Method not found", &errorData{Code: "unsupported_capability", Message: fmt.Sprintf("%q is not available: %s", name, cap.MCP.DisabledReason)})
		return
	}
	s.writeError(id, -32601, "Method not found", &errorData{Code: "unsupported_capability", Message: fmt.Sprintf("%q is not available: use CLI mutation commands for writes", name)})
}
