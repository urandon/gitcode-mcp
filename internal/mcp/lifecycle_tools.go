package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/service"
)

type repoStatusArgs struct {
	RepoID string `json:"repo_id"`
}

type repoStatusResult struct {
	RepoID       string                    `json:"repo_id,omitempty"`
	BindingState string                    `json:"binding_state"`
	Status       *service.RepositoryStatus `json:"status,omitempty"`
	Diagnostics  []lifecycleDiagnostic     `json:"diagnostics,omitempty"`
}

type lifecycleDiagnostic struct {
	Code        string `json:"code"`
	ErrorClass  string `json:"error_class,omitempty"`
	Message     string `json:"message"`
	Remediation string `json:"remediation,omitempty"`
}

func (s *Server) callRepoStatus(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a repoStatusArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if strings.TrimSpace(a.RepoID) == "" {
		result := repoStatusResult{BindingState: "nothing_bound", Diagnostics: []lifecycleDiagnostic{{Code: "nothing_bound", Message: "no repository binding requested"}}}
		s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: "binding_state=nothing_bound"}}, StructuredContent: result})
		return
	}
	status, err := s.svc.RepositoryStatus(ctx, service.RepositoryStatusRequest{RepoID: a.RepoID})
	if err != nil {
		if service.IsNotFound(err) {
			result := repoStatusResult{RepoID: a.RepoID, BindingState: "nothing_bound", Diagnostics: []lifecycleDiagnostic{{Code: "nothing_bound", Message: err.Error(), Remediation: "add a repository binding before using live sync"}}}
			s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: "binding_state=nothing_bound"}}, StructuredContent: result})
			return
		}
		s.writeDomainError(id, err)
		return
	}
	result := repoStatusResult{RepoID: status.RepoID, BindingState: status.BindingState, Status: &status}
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: fmt.Sprintf("binding_state=%s", status.BindingState)}}, StructuredContent: result})
}

type syncLiveArgs struct {
	RepoID         string `json:"repo_id"`
	Issues         bool   `json:"issues,omitempty"`
	Wiki           bool   `json:"wiki,omitempty"`
	Comments       bool   `json:"comments,omitempty"`
	Pulls          bool   `json:"pulls,omitempty"`
	RemoteAlias    string `json:"remote_alias,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type syncLiveResult struct {
	RepoID      string                `json:"repo_id"`
	Collections []string              `json:"collections"`
	FreshCount  int                   `json:"fresh_count"`
	Results     []service.SyncResult  `json:"results,omitempty"`
	Diagnostics []lifecycleDiagnostic `json:"diagnostics,omitempty"`
	GeneratedAt time.Time             `json:"generated_at"`
}

func (s *Server) callSyncLive(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a syncLiveArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if strings.TrimSpace(a.RepoID) == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id is required"})
		return
	}
	selected := syncLiveCollections(a)
	if len(selected) == 0 {
		selected = []string{"issues"}
	}
	result := syncLiveResult{RepoID: a.RepoID, Collections: selected, GeneratedAt: time.Now().UTC()}
	for _, collection := range selected {
		alias := strings.TrimSpace(a.RemoteAlias)
		if alias == "" {
			switch collection {
			case "issues":
				alias = "issue:*"
			case "wiki":
				alias = "wiki:*"
			default:
				result.Diagnostics = append(result.Diagnostics, lifecycleDiagnostic{Code: "unsupported_selector", Message: collection + " sync is not available in the current service surface"})
				continue
			}
		}
		key := strings.TrimSpace(a.IdempotencyKey)
		if key == "" {
			key = fmt.Sprintf("mcp-sync-live-%s-%d", collection, time.Now().UTC().UnixNano())
		}
		syncResult, err := s.svc.SyncToCache(ctx, service.SyncRequest{RepoID: a.RepoID, RemoteAlias: alias, IdempotencyKey: key})
		if err != nil {
			s.writeDomainError(id, err)
			return
		}
		result.Results = append(result.Results, syncResult)
		if syncResult.Freshness == service.FreshnessFresh || syncResult.Status == "succeeded" || syncResult.Status == "ok" {
			result.FreshCount++
		}
	}
	text := fmt.Sprintf("fresh_count=%d collections=%s", result.FreshCount, strings.Join(result.Collections, ","))
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}

func syncLiveCollections(a syncLiveArgs) []string {
	var selected []string
	if a.Issues {
		selected = append(selected, "issues")
	}
	if a.Wiki {
		selected = append(selected, "wiki")
	}
	if a.Comments {
		selected = append(selected, "comments")
	}
	if a.Pulls {
		selected = append(selected, "pulls")
	}
	return selected
}

type indexRepoArgs struct {
	RepoID string `json:"repo_id"`
	Mode   string `json:"mode,omitempty"`
	Strict bool   `json:"strict,omitempty"`
}

func (s *Server) callIndexRepo(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a indexRepoArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if strings.TrimSpace(a.RepoID) == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id is required"})
		return
	}
	mode := strings.TrimSpace(a.Mode)
	if mode == "" {
		mode = "full"
	}
	result, err := s.svc.Index(ctx, service.OperationRequest{RepoID: a.RepoID, Mode: mode, Strict: a.Strict})
	if err != nil {
		s.writeDomainError(id, err)
		return
	}
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: fmt.Sprintf("index status=%s processed=%d", result.Status, result.ProcessedCount)}}, StructuredContent: result})
}

type authStatusResult struct {
	Source    string `json:"source"`
	Present   bool   `json:"present"`
	StoreMode string `json:"store_mode,omitempty"`
}

func (s *Server) callAuthStatus(_ context.Context, id *json.RawMessage, args json.RawMessage) {
	var a map[string]any
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	present := strings.TrimSpace(os.Getenv(config.EnvToken)) != ""
	source := "missing"
	if present {
		source = "env:" + config.EnvToken
	}
	result := authStatusResult{Source: source, Present: present, StoreMode: "env"}
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: fmt.Sprintf("credential_present=%t source=%s", result.Present, result.Source)}}, StructuredContent: result})
}

type doctorArgs struct {
	RepoID string `json:"repo_id,omitempty"`
}

type doctorResult struct {
	Status      string                `json:"status"`
	RepoID      string                `json:"repo_id,omitempty"`
	Diagnostics []lifecycleDiagnostic `json:"diagnostics"`
	GeneratedAt time.Time             `json:"generated_at"`
}

func (s *Server) callDoctor(_ context.Context, id *json.RawMessage, args json.RawMessage) {
	var a doctorArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	result := doctorResult{Status: "ok", RepoID: a.RepoID, Diagnostics: []lifecycleDiagnostic{}, GeneratedAt: time.Now().UTC()}
	text := "doctor status=ok"
	if s.startupDiagnostic.present() {
		result.Status = "degraded"
		result.Diagnostics = append(result.Diagnostics, lifecycleDiagnostic{Code: s.startupDiagnostic.ErrorClass, ErrorClass: s.startupDiagnostic.ErrorClass, Message: s.startupDiagnostic.Message, Remediation: s.startupDiagnostic.Remediation})
		text = "doctor status=degraded"
	}
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}
