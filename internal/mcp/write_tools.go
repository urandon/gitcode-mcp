package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gitcode-mcp/internal/service"
)

type writeToolArgs struct {
	RepoID         string   `json:"repo_id"`
	WriteMode      string   `json:"write_mode"`
	IdempotencyKey string   `json:"idempotency_key,omitempty"`
	ID             string   `json:"id,omitempty"`
	Number         int      `json:"number,omitempty"`
	PRNumber       int      `json:"pr_number,omitempty"`
	IssueNumber    int      `json:"issue_number,omitempty"`
	Title          string   `json:"title,omitempty"`
	Body           string   `json:"body,omitempty"`
	Head           string   `json:"head,omitempty"`
	Base           string   `json:"base,omitempty"`
	State          string   `json:"state,omitempty"`
	Labels         []string `json:"labels,omitempty"`
	Strategy       string   `json:"strategy,omitempty"`
}

func (s *Server) callAddIssueComment(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	s.callWriteTool(ctx, id, args, s.svc.AddComment, func(a writeToolArgs) service.WriteCommandRequest {
		req := writeRequestFromArgs(a)
		req.Number = a.Number
		req.Body = a.Body
		return req
	})
}

func (s *Server) callUpdateIssue(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	s.callWriteTool(ctx, id, args, s.svc.UpdateIssue, func(a writeToolArgs) service.WriteCommandRequest {
		req := writeRequestFromArgs(a)
		req.Number = a.Number
		req.Title = a.Title
		req.Body = a.Body
		req.State = a.State
		req.Labels = a.Labels
		return req
	})
}

func (s *Server) callCreatePR(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	s.callWriteTool(ctx, id, args, s.svc.CreatePR, func(a writeToolArgs) service.WriteCommandRequest {
		req := writeRequestFromArgs(a)
		req.Title = a.Title
		req.Body = a.Body
		req.Head = a.Head
		req.Base = a.Base
		return req
	})
}

func (s *Server) callUpdatePR(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	s.callWriteTool(ctx, id, args, s.svc.UpdatePR, func(a writeToolArgs) service.WriteCommandRequest {
		req := writeRequestFromArgs(a)
		req.Number = a.Number
		req.Title = a.Title
		req.Body = a.Body
		req.State = a.State
		return req
	})
}

func (s *Server) callAddPRComment(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	s.callWriteTool(ctx, id, args, s.svc.AddPRComment, func(a writeToolArgs) service.WriteCommandRequest {
		req := writeRequestFromArgs(a)
		req.Number = a.Number
		req.Body = a.Body
		return req
	})
}

func (s *Server) callLinkPRIssue(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	s.callWriteTool(ctx, id, args, s.svc.LinkPRIssue, func(a writeToolArgs) service.WriteCommandRequest {
		req := writeRequestFromArgs(a)
		req.Number = a.PRNumber
		req.IssueNumber = a.IssueNumber
		return req
	})
}

func (s *Server) callWriteTool(ctx context.Context, id *json.RawMessage, args json.RawMessage, handler func(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error), build func(writeToolArgs) service.WriteCommandRequest) {
	var a writeToolArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if strings.TrimSpace(a.RepoID) == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id is required"})
		return
	}
	if strings.TrimSpace(a.WriteMode) != string(service.WriteModeLive) {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "write_mode must be live"})
		return
	}
	if strings.TrimSpace(a.Strategy) != "" && strings.TrimSpace(a.Strategy) != "description_fallback" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "strategy must be description_fallback"})
		return
	}
	result, err := handler(ctx, build(a))
	if err != nil {
		s.writeDomainError(id, err)
		return
	}
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: fmt.Sprintf("status=%s command=%s", result.Status, result.Command)}}, StructuredContent: result})
}

func writeRequestFromArgs(a writeToolArgs) service.WriteCommandRequest {
	return service.WriteCommandRequest{RepoID: a.RepoID, Repo: a.RepoID, Mode: service.WriteModeLive, ID: a.ID, IdempotencyKey: strings.TrimSpace(a.IdempotencyKey)}
}
