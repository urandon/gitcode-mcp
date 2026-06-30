package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"gitcode-mcp/internal/capability"
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
	CommentID      string   `json:"comment_id,omitempty"`
	Path           string   `json:"path,omitempty"`
	Line           int      `json:"line,omitempty"`
	StartLine      int      `json:"start_line,omitempty"`
	EndLine        int      `json:"end_line,omitempty"`
	Position       int      `json:"position,omitempty"`
	Title          string   `json:"title,omitempty"`
	Body           string   `json:"body,omitempty"`
	Head           string   `json:"head,omitempty"`
	Base           string   `json:"base,omitempty"`
	State          string   `json:"state,omitempty"`
	Labels         []string `json:"labels,omitempty"`
	Strategy       string   `json:"strategy,omitempty"`
}

func writeToolDefinition(cap capability.Capability) toolDefinition {
	return toolDefinition{
		Name:        cap.MCPName,
		Description: cap.Description,
		InputSchema: writeToolInputSchema(cap.ID),
	}
}

func writeToolInputSchema(id string) inputSchema {
	switch id {
	case "create_issue":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"title": {Type: "string", Description: "Issue title.", MinLength: 1}, "body": {Type: "string", Description: "Issue body."}, "labels": {Type: "array", Description: "Issue labels."}}), Required: []string{"repo_id", "write_mode", "title"}}
	case "add_issue_comment":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"number": {Type: "integer", Description: "Issue number.", Minimum: float64Ptr(1)}, "body": {Type: "string", Description: "Comment body.", MinLength: 1}}), Required: []string{"repo_id", "write_mode", "number", "body"}}
	case "update_issue_comment":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"comment_id": {Type: "string", Description: "Issue comment id.", MinLength: 1}, "number": {Type: "integer", Description: "Optional issue number hint for cache parent resolution.", Minimum: float64Ptr(1)}, "body": {Type: "string", Description: "Updated comment body.", MinLength: 1}}), Required: []string{"repo_id", "write_mode", "comment_id", "body"}}
	case "update_issue":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"number": {Type: "integer", Description: "Issue number.", Minimum: float64Ptr(1)}, "title": {Type: "string", Description: "Issue title."}, "body": {Type: "string", Description: "Issue body."}, "state": {Type: "string", Description: "Issue state."}, "labels": {Type: "array", Description: "Issue labels."}}), Required: []string{"repo_id", "write_mode", "number"}}
	case "create_pr":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"title": {Type: "string", Description: "Pull request title.", MinLength: 1}, "body": {Type: "string", Description: "Pull request body."}, "head": {Type: "string", Description: "Source branch.", MinLength: 1}, "base": {Type: "string", Description: "Target branch.", MinLength: 1}}), Required: []string{"repo_id", "write_mode", "title", "head", "base"}}
	case "update_pr":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"number": {Type: "integer", Description: "Pull request number.", Minimum: float64Ptr(1)}, "title": {Type: "string", Description: "Pull request title."}, "body": {Type: "string", Description: "Pull request body."}, "state": {Type: "string", Description: "Pull request state."}}), Required: []string{"repo_id", "write_mode", "number"}}
	case "add_pr_comment":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"number": {Type: "integer", Description: "Pull request number.", Minimum: float64Ptr(1)}, "body": {Type: "string", Description: "Comment body.", MinLength: 1}}), Required: []string{"repo_id", "write_mode", "number", "body"}}
	case "add_pr_review_comment":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"number": {Type: "integer", Description: "Pull request number.", Minimum: float64Ptr(1)}, "body": {Type: "string", Description: "Comment body.", MinLength: 1}, "path": {Type: "string", Description: "Changed file path.", MinLength: 1}, "line": {Type: "integer", Description: "File line number.", Minimum: float64Ptr(1)}, "position": {Type: "integer", Description: "Diff position.", Minimum: float64Ptr(1)}, "start_line": {Type: "integer", Description: "Optional range start line.", Minimum: float64Ptr(1)}, "end_line": {Type: "integer", Description: "Optional range end line.", Minimum: float64Ptr(1)}}), Required: []string{"repo_id", "write_mode", "number", "body", "path"}}
	case "link_pr_issue":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"pr_number": {Type: "integer", Description: "Pull request number.", Minimum: float64Ptr(1)}, "issue_number": {Type: "integer", Description: "Issue number.", Minimum: float64Ptr(1)}, "strategy": {Type: "string", Description: "Link strategy.", Enum: []string{"auto", "description_fallback"}, Default: "auto"}}), Required: []string{"repo_id", "write_mode", "pr_number", "issue_number"}}
	default:
		return inputSchema{Type: "object", Properties: writeSchemaProps(nil), Required: []string{"repo_id", "write_mode"}}
	}
}

func (s *Server) writeToolHandler(cap capability.Capability) toolHandler {
	switch cap.ID {
	case "create_issue":
		return s.callCreateIssue
	case "add_issue_comment":
		return s.callAddIssueComment
	case "update_issue_comment":
		return s.callUpdateIssueComment
	case "update_issue":
		return s.callUpdateIssue
	case "create_pr":
		return s.callCreatePR
	case "update_pr":
		return s.callUpdatePR
	case "add_pr_comment":
		return s.callAddPRComment
	case "add_pr_review_comment":
		return s.callAddPRReviewComment
	case "link_pr_issue":
		return s.callLinkPRIssue
	default:
		return func(_ context.Context, id *json.RawMessage, _ json.RawMessage) {
			s.writeError(id, -32601, "Method not found", &errorData{Code: "unsupported_capability", Message: fmt.Sprintf("%q is declared but has no MCP handler", cap.MCPName)})
		}
	}
}

func (s *Server) callCreateIssue(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	s.callWriteTool(ctx, id, args, s.svc.CreateIssue, func(a writeToolArgs) service.WriteCommandRequest {
		req := writeRequestFromArgs(a)
		req.Title = a.Title
		req.Body = a.Body
		req.Labels = a.Labels
		return req
	})
}

func (s *Server) callAddIssueComment(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	s.callWriteTool(ctx, id, args, s.svc.AddComment, func(a writeToolArgs) service.WriteCommandRequest {
		req := writeRequestFromArgs(a)
		req.Number = a.Number
		req.Body = a.Body
		return req
	})
}

func (s *Server) callUpdateIssueComment(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	s.callWriteTool(ctx, id, args, s.svc.UpdateComment, func(a writeToolArgs) service.WriteCommandRequest {
		req := writeRequestFromArgs(a)
		req.Number = a.Number
		req.CommentID = a.CommentID
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

func (s *Server) callAddPRReviewComment(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	s.callWriteTool(ctx, id, args, s.svc.AddPRReviewComment, func(a writeToolArgs) service.WriteCommandRequest {
		req := writeRequestFromArgs(a)
		req.Number = a.Number
		req.Body = a.Body
		req.Path = a.Path
		req.Line = a.Line
		req.StartLine = a.StartLine
		req.EndLine = a.EndLine
		req.Position = a.Position
		return req
	})
}

func (s *Server) callLinkPRIssue(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	s.callWriteTool(ctx, id, args, s.svc.LinkPRIssue, func(a writeToolArgs) service.WriteCommandRequest {
		req := writeRequestFromArgs(a)
		req.Number = a.PRNumber
		req.IssueNumber = a.IssueNumber
		req.Strategy = strings.TrimSpace(a.Strategy)
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
	strategy := strings.TrimSpace(a.Strategy)
	if strategy != "" && strategy != "auto" && strategy != "description_fallback" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "strategy must be auto or description_fallback"})
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
