package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gitcode-mcp/internal/capability"
	"gitcode-mcp/internal/service"
)

type writeToolArgs struct {
	RepoID         string   `json:"repo_id"`
	WriteMode      string   `json:"write_mode"`
	IdempotencyKey string   `json:"idempotency_key,omitempty"`
	ID             string   `json:"id,omitempty"`
	MirrorID       string   `json:"mirror_id,omitempty"`
	After          string   `json:"after,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
	Number         int      `json:"number,omitempty"`
	PRNumber       int      `json:"pr_number,omitempty"`
	IssueNumber    int      `json:"issue_number,omitempty"`
	CommentID      string   `json:"comment_id,omitempty"`
	DiscussionID   string   `json:"discussion_id,omitempty"`
	ParentID       string   `json:"parent_comment_id,omitempty"`
	Slug           string   `json:"slug,omitempty"`
	Path           string   `json:"path,omitempty"`
	Sha            string   `json:"sha,omitempty"`
	Line           int      `json:"line,omitempty"`
	StartLine      int      `json:"start_line,omitempty"`
	EndLine        int      `json:"end_line,omitempty"`
	Position       int      `json:"position,omitempty"`
	Title          string   `json:"title,omitempty"`
	Body           string   `json:"body,omitempty"`
	Description    string   `json:"description,omitempty"`
	DueOn          string   `json:"due_on,omitempty"`
	Milestone      string   `json:"milestone,omitempty"`
	ClearMilestone bool     `json:"clear_milestone,omitempty"`
	Head           string   `json:"head,omitempty"`
	Base           string   `json:"base,omitempty"`
	State          string   `json:"state,omitempty"`
	PerPage        int      `json:"per_page,omitempty"`
	Label          string   `json:"label,omitempty"`
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
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"title": {Type: "string", Description: "Issue title.", MinLength: 1}, "body": {Type: "string", Description: "Issue body."}, "labels": {Type: "array", Description: "Issue labels."}, "milestone": {Type: "string", Description: "Milestone remote id, stable MILESTONE-id, or exact title.", MinLength: 1}}), Required: []string{"repo_id", "write_mode", "title"}}
	case "add_issue_comment":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"number": {Type: "integer", Description: "Issue number.", Minimum: float64Ptr(1)}, "body": {Type: "string", Description: "Comment body.", MinLength: 1}}), Required: []string{"repo_id", "write_mode", "number", "body"}}
	case "update_issue_comment":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"comment_id": {Type: "string", Description: "Issue comment id.", MinLength: 1}, "number": {Type: "integer", Description: "Optional issue number hint for cache parent resolution.", Minimum: float64Ptr(1)}, "body": {Type: "string", Description: "Updated comment body.", MinLength: 1}}), Required: []string{"repo_id", "write_mode", "comment_id", "body"}}
	case "update_issue":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"number": {Type: "integer", Description: "Issue number.", Minimum: float64Ptr(1)}, "title": {Type: "string", Description: "Issue title."}, "body": {Type: "string", Description: "Issue body."}, "state": {Type: "string", Description: "Issue state.", Enum: []string{"open", "closed"}}, "labels": {Type: "array", Description: "Issue labels."}, "milestone": {Type: "string", Description: "Milestone remote id, stable MILESTONE-id, or exact title.", MinLength: 1}, "clear_milestone": {Type: "boolean", Description: "Clear the issue milestone; conflicts with milestone.", Default: false}}), Required: []string{"repo_id", "write_mode", "number"}}
	case "create_pr":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"title": {Type: "string", Description: "Pull request title.", MinLength: 1}, "body": {Type: "string", Description: "Pull request body."}, "head": {Type: "string", Description: "Source branch.", MinLength: 1}, "base": {Type: "string", Description: "Target branch.", MinLength: 1}}), Required: []string{"repo_id", "write_mode", "title", "head", "base"}}
	case "update_pr":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"number": {Type: "integer", Description: "Pull request number.", Minimum: float64Ptr(1)}, "title": {Type: "string", Description: "Pull request title."}, "body": {Type: "string", Description: "Pull request body."}, "state": {Type: "string", Description: "Pull request state."}}), Required: []string{"repo_id", "write_mode", "number"}}
	case "list_milestones":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"state": {Type: "string", Description: "Milestone state filter.", Enum: []string{"open", "closed"}}, "per_page": {Type: "integer", Description: "Records per page.", Minimum: float64Ptr(1), Maximum: float64Ptr(100)}}), Required: []string{"repo_id"}}
	case "list_push_remote_mirrors":
		return inputSchema{Type: "object", Properties: writeSchemaProps(nil), Required: []string{"repo_id"}}
	case "trigger_push_remote_mirror":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"mirror_id": {Type: "string", Description: "Configured push mirror id; optional only when exactly one mirror exists."}}), Required: []string{"repo_id", "write_mode", "idempotency_key"}}
	case "wait_push_remote_mirror":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"mirror_id": {Type: "string", Description: "Configured push mirror id; optional only when exactly one mirror exists."}, "after": {Type: "string", Description: "RFC3339 freshness barrier for terminal status."}, "timeout_seconds": {Type: "integer", Description: "Polling timeout in seconds.", Minimum: float64Ptr(1), Maximum: float64Ptr(600)}}), Required: []string{"repo_id"}}
	case "create_milestone":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"title": {Type: "string", Description: "Milestone title.", MinLength: 1}, "description": {Type: "string", Description: "Milestone description."}, "due_on": {Type: "string", Description: "Due date YYYY-MM-DD.", MinLength: 1}, "state": {Type: "string", Description: "Milestone state.", Enum: []string{"open", "closed"}}}), Required: []string{"repo_id", "write_mode", "title", "due_on"}}
	case "update_milestone":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"milestone": {Type: "string", Description: "Milestone id or exact title.", MinLength: 1}, "title": {Type: "string", Description: "Updated milestone title."}, "description": {Type: "string", Description: "Updated milestone description."}, "due_on": {Type: "string", Description: "Updated due date YYYY-MM-DD."}, "state": {Type: "string", Description: "Updated milestone state.", Enum: []string{"open", "closed"}}}), Required: []string{"repo_id", "write_mode", "milestone"}}
	case "set_issue_milestone":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"number": {Type: "integer", Description: "Issue number.", Minimum: float64Ptr(1)}, "milestone": {Type: "string", Description: "Milestone id or exact title.", MinLength: 1}}), Required: []string{"repo_id", "write_mode", "number", "milestone"}}
	case "clear_issue_milestone":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"number": {Type: "integer", Description: "Issue number.", Minimum: float64Ptr(1)}}), Required: []string{"repo_id", "write_mode", "number"}}
	case "add_pr_comment":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"number": {Type: "integer", Description: "Pull request number.", Minimum: float64Ptr(1)}, "body": {Type: "string", Description: "Comment body.", MinLength: 1}}), Required: []string{"repo_id", "write_mode", "number", "body"}}
	case "add_pr_review_comment":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"number": {Type: "integer", Description: "Pull request number.", Minimum: float64Ptr(1)}, "body": {Type: "string", Description: "Comment body.", MinLength: 1}, "path": {Type: "string", Description: "Changed file path.", MinLength: 1}, "line": {Type: "integer", Description: "Required 1-based current-side file line; the adapter derives GitCode provider coordinates.", Minimum: float64Ptr(1)}, "position": {Type: "integer", Description: "Deprecated file-line alias, not a diff-hunk offset; omit it or make it equal line.", Minimum: float64Ptr(1)}, "start_line": {Type: "integer", Description: "Optional range start line; must be at or before line.", Minimum: float64Ptr(1)}, "end_line": {Type: "integer", Description: "Optional range end line; must equal the anchor line.", Minimum: float64Ptr(1)}}), Required: []string{"repo_id", "write_mode", "number", "body", "path", "line"}}
	case "reply_pr_review_comment":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"number": {Type: "integer", Description: "Pull request number.", Minimum: float64Ptr(1)}, "discussion_id": {Type: "string", Description: "Use reply_discussion_id returned by list_pr_discussions; a synthetic comment:<root-id> is resolved by live refresh or rejected without posting.", MinLength: 1}, "parent_comment_id": {Type: "string", Description: "Parent/root review comment id.", MinLength: 1}, "body": {Type: "string", Description: "Reply body.", MinLength: 1}}), Required: []string{"repo_id", "write_mode", "number", "discussion_id", "parent_comment_id", "body"}}
	case "link_pr_issue":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"pr_number": {Type: "integer", Description: "Pull request number.", Minimum: float64Ptr(1)}, "issue_number": {Type: "integer", Description: "Issue number.", Minimum: float64Ptr(1)}, "strategy": {Type: "string", Description: "Link strategy.", Enum: []string{"auto", "description_fallback"}, Default: "auto"}}), Required: []string{"repo_id", "write_mode", "pr_number", "issue_number"}}
	case "create_page":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"path": {Type: "string", Description: "Wiki page path."}, "slug": {Type: "string", Description: "Wiki page slug."}, "title": {Type: "string", Description: "Wiki page title."}, "body": {Type: "string", Description: "Wiki page body.", MinLength: 1}}), Required: []string{"repo_id", "write_mode", "body"}}
	case "update_page":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"id": {Type: "string", Description: "Wiki page id."}, "path": {Type: "string", Description: "Wiki page path."}, "slug": {Type: "string", Description: "Wiki page slug."}, "title": {Type: "string", Description: "Updated wiki page title."}, "body": {Type: "string", Description: "Updated wiki page body."}, "sha": {Type: "string", Description: "Expected wiki page sha/revision."}}), Required: []string{"repo_id", "write_mode"}}
	case "delete_page":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"id": {Type: "string", Description: "Wiki page id."}, "path": {Type: "string", Description: "Wiki page path."}, "slug": {Type: "string", Description: "Wiki page slug."}, "sha": {Type: "string", Description: "Expected wiki page sha/revision."}}), Required: []string{"repo_id", "write_mode"}}
	case "add_label":
		return inputSchema{Type: "object", Properties: writeSchemaProps(map[string]schemaProp{"number": {Type: "integer", Description: "Issue number.", Minimum: float64Ptr(1)}, "id": {Type: "string", Description: "Issue id."}, "label": {Type: "string", Description: "Label to add.", MinLength: 1}}), Required: []string{"repo_id", "write_mode", "label"}}
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
	case "list_milestones":
		return s.callListMilestones
	case "list_push_remote_mirrors":
		return s.callListPushRemoteMirrors
	case "trigger_push_remote_mirror":
		return s.callTriggerPushRemoteMirror
	case "wait_push_remote_mirror":
		return s.callWaitPushRemoteMirror
	case "create_milestone":
		return s.callCreateMilestone
	case "update_milestone":
		return s.callUpdateMilestone
	case "set_issue_milestone":
		return s.callSetIssueMilestone
	case "clear_issue_milestone":
		return s.callClearIssueMilestone
	case "add_pr_comment":
		return s.callAddPRComment
	case "add_pr_review_comment":
		return s.callAddPRReviewComment
	case "reply_pr_review_comment":
		return s.callReplyPRReviewComment
	case "link_pr_issue":
		return s.callLinkPRIssue
	case "create_page":
		return s.callCreatePage
	case "update_page":
		return s.callUpdatePage
	case "delete_page":
		return s.callDeletePage
	case "add_label":
		return s.callAddLabel
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
		req.Milestone = a.Milestone
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
		req.Milestone = a.Milestone
		req.ClearMilestone = a.ClearMilestone
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

func (s *Server) callListMilestones(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a writeToolArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if strings.TrimSpace(a.RepoID) == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id is required"})
		return
	}
	result, err := s.svc.ListMilestones(ctx, service.MilestoneListRequest{RepoID: a.RepoID, Repo: a.RepoID, State: a.State, PerPage: a.PerPage})
	if err != nil {
		s.writeDomainError(id, err)
		return
	}
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: fmt.Sprintf("repo_id=%s milestones=%d", result.RepoID, result.Count)}}, StructuredContent: result})
}

func (s *Server) callListPushRemoteMirrors(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a writeToolArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if strings.TrimSpace(a.RepoID) == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id is required"})
		return
	}
	result, err := s.svc.ListPushRemoteMirrors(ctx, service.PushMirrorListRequest{RepoID: a.RepoID, Repo: a.RepoID})
	if err != nil {
		s.writeDomainError(id, err)
		return
	}
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: fmt.Sprintf("repo_id=%s push_mirrors=%d", result.RepoID, result.Count)}}, StructuredContent: result})
}

func (s *Server) callTriggerPushRemoteMirror(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	s.callWriteTool(ctx, id, args, s.svc.TriggerPushRemoteMirror, func(a writeToolArgs) service.WriteCommandRequest {
		req := writeRequestFromArgs(a)
		req.ID = strings.TrimSpace(a.MirrorID)
		return req
	})
}

func (s *Server) callWaitPushRemoteMirror(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a writeToolArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if strings.TrimSpace(a.RepoID) == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id is required"})
		return
	}
	var after time.Time
	if strings.TrimSpace(a.After) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(a.After))
		if err != nil {
			s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "after must be an RFC3339 timestamp"})
			return
		}
		after = parsed.UTC()
	}
	result, err := s.svc.WaitPushRemoteMirror(ctx, service.PushMirrorWaitRequest{RepoID: a.RepoID, Repo: a.RepoID, MirrorID: a.MirrorID, After: after, TimeoutSeconds: a.TimeoutSeconds})
	if err != nil {
		s.writeDomainError(id, err)
		return
	}
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: fmt.Sprintf("repo_id=%s mirror_id=%s status=%s", result.RepoID, result.MirrorID, result.Status)}}, StructuredContent: result})
}

func (s *Server) callCreateMilestone(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	s.callWriteTool(ctx, id, args, s.svc.CreateMilestone, func(a writeToolArgs) service.WriteCommandRequest {
		req := writeRequestFromArgs(a)
		req.Title = a.Title
		req.Description = a.Description
		req.DueOn = a.DueOn
		req.State = a.State
		return req
	})
}

func (s *Server) callUpdateMilestone(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	s.callWriteTool(ctx, id, args, s.svc.UpdateMilestone, func(a writeToolArgs) service.WriteCommandRequest {
		req := writeRequestFromArgs(a)
		req.Milestone = a.Milestone
		req.Title = a.Title
		req.Description = a.Description
		req.DueOn = a.DueOn
		req.State = a.State
		return req
	})
}

func (s *Server) callSetIssueMilestone(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	s.callWriteTool(ctx, id, args, s.svc.SetIssueMilestone, func(a writeToolArgs) service.WriteCommandRequest {
		req := writeRequestFromArgs(a)
		req.Number = a.Number
		req.Milestone = a.Milestone
		return req
	})
}

func (s *Server) callClearIssueMilestone(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	s.callWriteTool(ctx, id, args, s.svc.ClearIssueMilestone, func(a writeToolArgs) service.WriteCommandRequest {
		req := writeRequestFromArgs(a)
		req.Number = a.Number
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
	handler := func(ctx context.Context, req service.WriteCommandRequest) (service.WriteCommandResult, error) {
		if req.Line <= 0 {
			return service.WriteCommandResult{}, service.ErrInvalidQuery{Field: "line", Message: "line is required; position is not a diff-hunk offset"}
		}
		return s.svc.AddPRReviewComment(ctx, req)
	}
	s.callWriteTool(ctx, id, args, handler, func(a writeToolArgs) service.WriteCommandRequest {
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

func (s *Server) callReplyPRReviewComment(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	s.callWriteTool(ctx, id, args, s.svc.ReplyPRReviewComment, func(a writeToolArgs) service.WriteCommandRequest {
		req := writeRequestFromArgs(a)
		req.Number = a.Number
		req.DiscussionID = a.DiscussionID
		req.ParentID = a.ParentID
		req.Body = a.Body
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

func (s *Server) callCreatePage(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	s.callWriteTool(ctx, id, args, s.svc.CreatePage, func(a writeToolArgs) service.WriteCommandRequest {
		req := writeRequestFromArgs(a)
		req.Slug = a.Slug
		req.Path = a.Path
		req.Title = a.Title
		req.Body = a.Body
		return req
	})
}

func (s *Server) callUpdatePage(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	s.callWriteTool(ctx, id, args, s.svc.UpdatePage, func(a writeToolArgs) service.WriteCommandRequest {
		req := writeRequestFromArgs(a)
		req.Slug = a.Slug
		req.Path = a.Path
		req.Sha = a.Sha
		req.Title = a.Title
		req.Body = a.Body
		return req
	})
}

func (s *Server) callDeletePage(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	s.callWriteTool(ctx, id, args, s.svc.DeletePage, func(a writeToolArgs) service.WriteCommandRequest {
		req := writeRequestFromArgs(a)
		req.Slug = a.Slug
		req.Path = a.Path
		req.Sha = a.Sha
		return req
	})
}

func (s *Server) callAddLabel(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	s.callWriteTool(ctx, id, args, s.svc.AddLabel, func(a writeToolArgs) service.WriteCommandRequest {
		req := writeRequestFromArgs(a)
		req.Number = a.Number
		req.Label = a.Label
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
	text := fmt.Sprintf("status=%s command=%s", result.Status, result.Command)
	if result.Milestone != nil {
		text += fmt.Sprintf(" milestone_id=%s milestone_remote_id=%s milestone_cleared=%t", result.Milestone.ID, result.Milestone.RemoteID, result.Milestone.Cleared)
	}
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}

func writeRequestFromArgs(a writeToolArgs) service.WriteCommandRequest {
	return service.WriteCommandRequest{RepoID: a.RepoID, Repo: a.RepoID, Mode: service.WriteModeLive, ID: a.ID, IdempotencyKey: strings.TrimSpace(a.IdempotencyKey)}
}
