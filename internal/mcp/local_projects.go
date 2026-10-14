package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"gitcode-mcp/internal/capability"
	"gitcode-mcp/internal/localgit"
	"gitcode-mcp/internal/service"
	"gitcode-mcp/internal/servicectl"
)

func (h *RPCHandler) SetLocalProjects(client func() (*servicectl.RPCClient, error)) {
	h.localOnly = true
	h.serviceClient = client
	h.toolBootstrap = nil
	h.credentialResolver = nil
}
func (s *Server) SetLocalProjects(client func() (*servicectl.RPCClient, error)) {
	s.localOnly = true
	s.serviceClient = client
	s.toolBootstrap = nil
	s.credentialResolver = nil
	if s.handler != nil {
		s.handler.SetLocalProjects(client)
	}
}

func localProjectToolDefinition(name string) (toolDefinition, bool) {
	var cap capability.Capability
	for _, c := range capability.LocalProjectCapabilities() {
		if c.MCPName == name && c.MCP.Enabled {
			cap = c
		}
	}
	if cap.ID == "" {
		return toolDefinition{}, false
	}
	strict := false
	props := map[string]schemaProp{}
	required := []string{}
	if name != "local_project_status" {
		props["project"] = schemaProp{Type: "string", Description: "Exact opaque registered project selector; never a path or repo URL."}
		required = append(required, "project")
	}
	str := func(desc string) schemaProp { return schemaProp{Type: "string", Description: desc} }
	if name == "local_project_search" {
		props["query"] = str("Required UTF-8 query, maximum4096 bytes.")
		props["mode"] = schemaProp{Type: "string", Enum: []string{"fulltext", "hybrid"}, Default: "fulltext"}
		props["limit"] = schemaProp{Type: "integer", Minimum: float64Ptr(1), Maximum: float64Ptr(32), Default: 10}
		required = append(required, "query")
	} else if name == "local_project_index" {
		props["write_mode"] = schemaProp{Type: "string", Enum: []string{"local"}}
		props["max_chunks"] = schemaProp{Type: "integer", Minimum: float64Ptr(1), Maximum: float64Ptr(128), Default: 64}
		required = append(required, "write_mode")
	} else if name == "local_project_mutate" {
		mutation := map[string]schemaProp{}
		for _, key := range []string{"operation", "id", "parent_id", "title", "body", "state", "milestone", "expected_head", "expected_revision", "idempotency_key", "write_mode"} {
			mutation[key] = str("Typed local mutation field. Updates replace all editable metadata/body; preserve omitted data explicitly.")
		}
		mutation["operation"] = schemaProp{Type: "string", Enum: []string{"create_issue", "update_issue", "delete_issue", "add_comment", "update_comment", "delete_comment", "create_wiki", "update_wiki", "delete_wiki"}}
		mutation["write_mode"] = schemaProp{Type: "string", Enum: []string{"local"}}
		mutation["idempotency_key"] = str("Required caller key; reuse the same key and intent after timeout/ambiguity or restart. Never blind-retry another key.")
		mutation["expected_head"] = str("Full expected commit, required for creates. Get it from local_project_status.")
		mutation["expected_revision"] = str("Expected entity blob from local_project_get; required for update/delete unless expected_head is provided.")
		mutation["labels"] = schemaProp{Type: "array", Items: &schemaProp{Type: "string"}}
		mutation["links"] = schemaProp{Type: "array", Items: &schemaProp{Type: "object", Properties: map[string]schemaProp{"kind": {Type: "string", Enum: []string{"issue", "comment", "wiki"}}, "id": str("Project-scoped stable entity ID.")}, Required: []string{"kind", "id"}, AdditionalProperties: &strict}}
		props["mutation"] = schemaProp{Type: "object", Properties: mutation, Required: []string{"operation", "write_mode", "idempotency_key"}, AdditionalProperties: &strict}
		required = append(required, "mutation")
	} else if name != "local_project_status" {
		props["kind"] = schemaProp{Type: "string", Enum: []string{"issue", "comment", "wiki"}}
		required = append(required, "kind")
		if name != "local_project_list" {
			props["id"] = str("Stable ISSUE-n, COMMENT-n or DOC-n in this project.")
			required = append(required, "id")
		}
		if name == "local_project_list" {
			props["limit"] = schemaProp{Type: "integer", Minimum: float64Ptr(1), Maximum: float64Ptr(128), Default: 20}
			props["offset"] = schemaProp{Type: "integer", Minimum: float64Ptr(0), Default: 0}
		}
		if name == "local_project_history" {
			props["max_commits"] = schemaProp{Type: "integer", Minimum: float64Ptr(1), Maximum: float64Ptr(256), Default: 64}
		}
	}
	return toolDefinition{Name: name, Description: cap.Description, InputSchema: inputSchema{Type: "object", Properties: props, Required: required, AdditionalProperties: &strict}, Annotations: map[string]bool{"readOnlyHint": cap.Safety == capability.SafetyReadOnly, "openWorldHint": false}}, true
}

func (s *Server) localToolsCall(ctx context.Context, req request, params toolCallParams) {
	tool, ok := s.toolRegistry()[params.Name]
	if !ok {
		s.localProjectError(req.ID, &localgit.Error{Code: "unsupported_capability", Field: "tool"})
		return
	}
	if s.toolDisabledByPolicy(params.Name) {
		s.writeToolDisabledByPolicy(req.ID, params.Name)
		return
	}
	if servicectl.DecodeLocalProjectJSON(*req.Params, &params) != nil {
		s.localProjectError(req.ID, &localgit.Error{Code: "invalid_argument", Field: "request"})
		return
	}
	args := params.Arguments
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	tool.handler(ctx, req.ID, args)
}

func (s *Server) callLocalProject(ctx context.Context, id *json.RawMessage, name string, args json.RawMessage) {
	def, _ := localProjectToolDefinition(name)
	if !localArgumentShape(args, def.InputSchema.Properties, def.InputSchema.Required) {
		s.localProjectError(id, &localgit.Error{Code: "invalid_argument", Field: "arguments"})
		return
	}
	var method string
	var params, result any
	if name == "local_project_status" {
		method = "Status"
		params = &struct{}{}
		result = &service.LocalProjectsStatus{}
	} else if name == "local_project_search" {
		method = "Search"
		params = &service.LocalProjectSearchRequest{}
		result = &service.LocalProjectSearchResult{}
	} else if name == "local_project_index" {
		method = "Index"
		params = &service.LocalProjectIndexRequest{}
		result = &service.LocalProjectIndexResult{}
	} else if name == "local_project_mutate" {
		method = "Mutate"
		params = &service.LocalProjectMutation{}
		result = &localgit.MutationResult{}
	} else {
		q := &service.LocalProjectQuery{}
		switch name {
		case "local_project_get":
			method = "Get"
			result = &localgit.Entity{}
		case "local_project_list":
			method = "List"
			q.Limit = 20
			result = &[]localgit.Entity{}
		case "local_project_history":
			method = "History"
			q.MaxCommits = 64
			result = &[]localgit.Citation{}
		}
		params = q
	}
	if method == "" || servicectl.DecodeLocalProjectJSON(args, params) != nil {
		s.localProjectError(id, &localgit.Error{Code: "invalid_argument", Field: "arguments"})
		return
	}
	// Project registration and operation validation are shared daemon behavior.
	client, err := s.localServiceClient()
	if err != nil {
		s.localProjectError(id, &localgit.Error{Code: "unavailable", Field: "service"})
		return
	}
	if err = client.Call(ctx, "LocalProjects."+method, params, result); err != nil {
		s.localProjectError(id, err)
		return
	}
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: "Local Git project result; exact revisions and replay evidence are in structuredContent."}}, StructuredContent: result})
}
func (s *Server) localProjectError(id *json.RawMessage, err error) {
	code := "local_git_unavailable"
	var coded interface{ DiagnosticCode() string }
	if errors.As(err, &coded) && strings.HasPrefix(coded.DiagnosticCode(), "local_git_") {
		code = coded.DiagnosticCode()
	}
	if !errors.As(err, &coded) && errors.Is(err, context.Canceled) {
		code = "operation_cancelled"
	}
	if !errors.As(err, &coded) && errors.Is(err, context.DeadlineExceeded) {
		code = "operation_timeout"
	}
	// Fixed text only: private socket/store paths and peer messages stay private.
	s.writeError(id, -32000, "Local project operation unavailable", &errorData{Code: code, Message: "local Git project operation could not be completed", Remediation: "inspect local_project_status and the selected configuration; explicitly start the local daemon if unavailable; after an uncertain mutation preserve the same caller key and intent"})
}

// Validate the closed tool shape before touching IPC; the daemon still owns all
// domain/revision/receipt validation. This is not another execution protocol.
func localArgumentShape(raw []byte, props map[string]schemaProp, required []string) bool {
	var fields map[string]json.RawMessage
	if servicectl.DecodeLocalProjectJSON(raw, &fields) != nil || fields == nil {
		return false
	}
	for _, key := range required {
		if _, ok := fields[key]; !ok {
			return false
		}
	}
	for key, value := range fields {
		p, ok := props[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
		switch p.Type {
		case "string":
			var s string
			if json.Unmarshal(value, &s) != nil {
				return false
			}
			if len(p.Enum) > 0 {
				valid := false
				for _, v := range p.Enum {
					valid = valid || s == v
				}
				if !valid {
					return false
				}
			}
		case "integer":
			var n int
			if json.Unmarshal(value, &n) != nil {
				return false
			}
			if p.Minimum != nil && float64(n) < *p.Minimum || p.Maximum != nil && float64(n) > *p.Maximum {
				return false
			}
		case "object":
			if !localArgumentShape(value, p.Properties, p.Required) {
				return false
			}
		case "array":
			var items []json.RawMessage
			if json.Unmarshal(value, &items) != nil || items == nil {
				return false
			}
			if len(items) > 128 {
				return false
			}
			for _, item := range items {
				if p.Items.Type == "object" {
					if !localArgumentShape(item, p.Items.Properties, p.Items.Required) {
						return false
					}
				} else {
					var s string
					if bytes.Equal(bytes.TrimSpace(item), []byte("null")) || json.Unmarshal(item, &s) != nil {
						return false
					}
				}
			}
		default:
			return false
		}
	}
	return true
}
