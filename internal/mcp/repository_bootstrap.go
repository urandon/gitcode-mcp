package mcp

import (
	"context"
	"encoding/json"
	"strings"

	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/service"
)

func (s *Server) bootstrapArgs(id *json.RawMessage, args json.RawMessage, allowed ...string) bool {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(args, &fields); err != nil || fields == nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be an object"})
		return false
	}
	for key := range fields {
		ok := false
		for _, field := range allowed {
			if key == field {
				ok = true
			}
		}
		if !ok {
			s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "unsupported repository bootstrap argument; description writes and arbitrary destinations are not supported"})
			return false
		}
	}
	return true
}

func (s *Server) callGetRepositoryMetadata(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	s.callBootstrapRead(ctx, id, args, true)
}
func (s *Server) callListRepositoryLabels(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	s.callBootstrapRead(ctx, id, args, false)
}
func (s *Server) callBootstrapRead(ctx context.Context, id *json.RawMessage, args json.RawMessage, metadata bool) {
	if !s.bootstrapArgs(id, args, "repo_id") {
		return
	}
	var a struct {
		RepoID string `json:"repo_id"`
	}
	if err := json.Unmarshal(args, &a); err != nil || strings.TrimSpace(a.RepoID) == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id is required"})
		return
	}
	var result any
	var err error
	operation := "list_repo_labels"
	if metadata {
		operation = "get_repo_metadata"
		result, err = s.svc.GetRepositoryMetadata(ctx, a.RepoID)
	} else {
		result, err = s.svc.ListRepositoryLabels(ctx, a.RepoID)
	}
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: operation, RepoID: a.RepoID})
		return
	}
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: operation + " explicit live read confirmed"}}, StructuredContent: result})
}
func (s *Server) callCreateRepositoryLabel(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	if !s.bootstrapArgs(id, args, "repo_id", "write_mode", "name", "color", "idempotency_key") {
		return
	}
	s.callValidatedWriteTool(ctx, id, args, s.svc.CreateRepositoryLabel, func(a writeToolArgs) service.WriteCommandRequest {
		req := writeRequestFromArgs(a)
		req.Label, req.Color = a.Name, a.Color
		return req
	}, func(a writeToolArgs) string {
		if strings.TrimSpace(a.IdempotencyKey) == "" {
			return "idempotency_key is required; preserve it for GET-only recovery"
		}
		if err := gitcode.ValidateRepositoryLabel(gitcode.RepositoryLabelRequest{Name: a.Name, Color: a.Color}); err != nil {
			return "name and #RRGGBB color must be valid; no description writes"
		}
		return ""
	})
}
