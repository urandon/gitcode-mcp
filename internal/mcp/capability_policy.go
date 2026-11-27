package mcp

import (
	"context"
	"encoding/json"
	"gitcode-mcp/internal/capability"
)

func (h *RPCHandler) SetCapabilityPolicy(p *capability.Policy) { h.policy = p }
func (s *Server) SetCapabilityPolicy(p *capability.Policy) {
	s.policy = p
	if s.handler != nil {
		s.handler.SetCapabilityPolicy(p)
	}
}
func (s *Server) callCapabilityPolicy(_ context.Context, id *json.RawMessage, _ json.RawMessage) {
	v := s.policy.View()
	b, _ := json.Marshal(v)
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: string(b)}}, StructuredContent: v})
}
