package mcp

import (
	"context"
	"encoding/json"
	"fmt"
)

var unsupportedCapabilityToolNames = map[string]bool{
	"create_issue": true,
	"update_issue": true,
	"add_comment":  true,
	"create_page":  true,
	"update_page":  true,

	"create-issue": true,
	"update-issue": true,
	"add-label":    true,
	"create-page":  true,
	"update-page":  true,
}

func isUnsupportedCapabilityTool(name string) bool {
	return unsupportedCapabilityToolNames[name]
}

func (s *Server) unsupportedCapabilityHandler(_ context.Context, id *json.RawMessage, name string) {
	s.writeError(id, -32601, "Method not found", &errorData{Code: "unsupported_capability", Message: fmt.Sprintf("%q is not available: use CLI mutation commands for writes", name)})
}
