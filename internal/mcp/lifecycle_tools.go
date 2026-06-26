package mcp

import (
	"context"
	"encoding/json"
	"errors"
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
	MaxPages       int    `json:"max_pages,omitempty"`
	MaxRecords     int    `json:"max_records,omitempty"`
	PerPage        int    `json:"per_page,omitempty"`
}

type syncLiveResult struct {
	RepoID       string                  `json:"repo_id"`
	Collections  []string                `json:"collections"`
	FreshCount   int                     `json:"fresh_count"`
	SuccessCount int                     `json:"success_count"`
	FailureCount int                     `json:"failure_count"`
	Results      []service.SyncResult    `json:"results,omitempty"`
	Failures     []service.ResourceError `json:"failures,omitempty"`
	Diagnostics  []lifecycleDiagnostic   `json:"diagnostics,omitempty"`
	GeneratedAt  time.Time               `json:"generated_at"`
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
	if a.MaxPages < 0 || a.MaxRecords < 0 || a.PerPage < 0 {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "bounds must be non-negative"})
		return
	}
	selected := syncLiveCollections(a)
	result := syncLiveResult{RepoID: a.RepoID, Collections: selected, GeneratedAt: time.Now().UTC()}
	if strings.TrimSpace(a.RemoteAlias) != "" {
		key := strings.TrimSpace(a.IdempotencyKey)
		if key == "" {
			key = fmt.Sprintf("mcp-sync-live-remote-%d", time.Now().UTC().UnixNano())
		}
		syncResult, err := s.svc.SyncToCache(ctx, service.SyncRequest{RepoID: a.RepoID, RemoteAlias: strings.TrimSpace(a.RemoteAlias), IdempotencyKey: key})
		if err != nil {
			s.writeDomainError(id, err)
			return
		}
		result.Results = append(result.Results, syncResult)
		result.SuccessCount = 1
		if syncResult.Freshness == service.FreshnessFresh || syncResult.Status == "succeeded" || syncResult.Status == "ok" {
			result.FreshCount++
		}
		text := fmt.Sprintf("fresh_count=%d collections=remote_alias", result.FreshCount)
		s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
		return
	}

	if len(selected) == 0 {
		selected = []string{"issues", "wiki"}
		result.Collections = selected
	}
	req := syncLiveBulkRequest(a)
	var syncErr error
	runBulk := func(collection string, fn func(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)) {
		part, err := fn(ctx, req)
		appendBulkSyncResult(&result, part)
		if err != nil {
			syncErr = mergeLifecycleSyncError(syncErr, &result, err)
			if partial, ok := extractLifecyclePartial(err); ok && partial.Diagnostic != "" {
				result.Diagnostics = append(result.Diagnostics, lifecycleDiagnostic{Code: string(partial.Diagnostic), Message: collection + " sync returned a diagnostic"})
			}
		}
	}
	switch {
	case a.Issues && a.Wiki && !a.Pulls && !a.Comments:
		runBulk("all", s.svc.BulkSyncAll)
	default:
		if a.Issues || len(syncLiveCollections(a)) == 0 {
			runBulk("issues", s.svc.BulkSyncIssues)
		}
		if a.Wiki || len(syncLiveCollections(a)) == 0 {
			runBulk("wiki", s.svc.BulkSyncWiki)
		}
		if a.Pulls {
			runBulk("pulls", s.svc.BulkSyncPullRequests)
		}
		if a.Comments {
			if a.Issues && !a.Pulls {
				result.Diagnostics = append(result.Diagnostics, lifecycleDiagnostic{Code: "unsupported_comments_parent", Message: "sync_live comments currently syncs pull request comments only", Remediation: "sync pull requests first, then call sync_live with comments=true"})
			}
			runBulk("comments", s.svc.BulkSyncPRComments)
		}
	}
	if syncErr != nil {
		if partial, ok := extractLifecyclePartial(syncErr); ok && partial.Diagnostic != "" {
			result.Diagnostics = append(result.Diagnostics, lifecycleDiagnostic{Code: string(partial.Diagnostic), Message: "sync_live completed with a diagnostic"})
		} else {
			result.Diagnostics = append(result.Diagnostics, lifecycleDiagnostic{Code: "partial_sync", Message: syncErr.Error()})
		}
	}
	text := fmt.Sprintf("fresh_count=%d collections=%s", result.FreshCount, strings.Join(result.Collections, ","))
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}

func syncLiveBulkRequest(a syncLiveArgs) service.BulkSyncRequest {
	perPage := a.PerPage
	if perPage <= 0 {
		perPage = 25
	}
	req := service.BulkSyncRequest{RepoID: a.RepoID, IdempotencyKey: strings.TrimSpace(a.IdempotencyKey), PerPage: perPage}
	if a.MaxPages > 0 || a.MaxRecords > 0 {
		req.Bounds = &service.SyncBounds{MaxPages: a.MaxPages, MaxRecords: a.MaxRecords}
	}
	return req
}

func appendBulkSyncResult(dst *syncLiveResult, src *service.SyncResourcesResult) {
	if dst == nil || src == nil {
		return
	}
	dst.Results = append(dst.Results, src.Results...)
	dst.Failures = append(dst.Failures, src.Failures...)
	dst.SuccessCount = len(dst.Results)
	dst.FailureCount = len(dst.Failures)
	for _, syncResult := range src.Results {
		if syncResult.Freshness == service.FreshnessFresh || syncResult.Status == "succeeded" || syncResult.Status == "ok" {
			dst.FreshCount++
		}
	}
}

func mergeLifecycleSyncError(existing error, result *syncLiveResult, err error) error {
	if err == nil {
		return existing
	}
	if existing == nil {
		return err
	}
	if result == nil {
		return existing
	}
	return &service.PartialSyncError{Errors: result.Failures, SuccessCount: result.SuccessCount, FailureCount: result.FailureCount}
}

func extractLifecyclePartial(err error) (*service.PartialSyncError, bool) {
	var partial *service.PartialSyncError
	if err != nil && errors.As(err, &partial) {
		return partial, true
	}
	return nil, false
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
	Source      string `json:"source"`
	Present     bool   `json:"present"`
	StoreMode   string `json:"store_mode,omitempty"`
	ErrorClass  string `json:"error_class,omitempty"`
	Remediation string `json:"remediation,omitempty"`
}

func (s *Server) callAuthStatus(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a map[string]any
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	_ = a

	result := s.authStatus(ctx)
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: fmt.Sprintf("credential_present=%t source=%s", result.Present, result.Source)}}, StructuredContent: result})
}

func (s *Server) authStatus(ctx context.Context) authStatusResult {
	if s.credentialResolver != nil {
		result := s.credentialResolver.Status(ctx, config.EffectiveConfig{})
		return authStatusResult{Source: result.Source, Present: result.Present, StoreMode: result.StoreMode, ErrorClass: result.ErrorClass, Remediation: result.Remediation}
	}
	present := strings.TrimSpace(os.Getenv(config.EnvToken)) != ""
	source := "missing"
	if present {
		source = "env:" + config.EnvToken
	}
	return authStatusResult{Source: source, Present: present, StoreMode: "env"}
}

type doctorArgs struct {
	RepoID string `json:"repo_id,omitempty"`
}

type doctorResult struct {
	Status       string                           `json:"status"`
	RepoID       string                           `json:"repo_id,omitempty"`
	Cache        *service.CacheStatusResult       `json:"cache,omitempty"`
	Repo         *service.RepositoryStatus        `json:"repo,omitempty"`
	Auth         *authStatusResult                `json:"auth,omitempty"`
	Sync         *service.SyncStatusSummaryResult `json:"sync,omitempty"`
	Index        *service.StaleIndexResult        `json:"index,omitempty"`
	LiveProvider map[string]string                `json:"live_provider,omitempty"`
	Diagnostics  []lifecycleDiagnostic            `json:"diagnostics"`
	GeneratedAt  time.Time                        `json:"generated_at"`
}

func (s *Server) callDoctor(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
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
	auth := s.authStatus(ctx)
	result.Auth = &auth
	if auth.Present {
		result.LiveProvider = map[string]string{"status": "credential_configured", "source": auth.Source}
	} else {
		result.LiveProvider = map[string]string{"status": "not_configured", "source": auth.Source}
	}
	if s.svc != nil && strings.TrimSpace(a.RepoID) != "" {
		if repo, err := s.svc.RepositoryStatus(ctx, service.RepositoryStatusRequest{RepoID: a.RepoID}); err == nil {
			result.Repo = &repo
		} else {
			result.addDoctorDiagnostic("repo_status", err, "check repository binding")
		}
		if cacheStatus, err := s.svc.CacheStatus(ctx, service.CacheStatusRequest{RepoID: a.RepoID}); err == nil {
			result.Cache = &cacheStatus
		} else {
			result.addDoctorDiagnostic("cache_status", err, "check cache path and schema")
		}
		if syncStatus, err := s.svc.SyncStatus(ctx, service.ListSourcesRequest{RepoID: a.RepoID}); err == nil {
			result.Sync = &syncStatus
		} else {
			result.addDoctorDiagnostic("sync_status", err, "run sync_live for this repository")
		}
		if indexStatus, err := s.svc.StaleIndex(ctx, service.StaleIndexRequest{RepoID: a.RepoID}); err == nil {
			result.Index = &indexStatus
		} else {
			result.addDoctorDiagnostic("index_status", err, "run index_repo after syncing")
		}
	}
	if len(result.Diagnostics) > 0 {
		result.Status = "degraded"
		text = "doctor status=degraded"
	}
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}

func (r *doctorResult) addDoctorDiagnostic(code string, err error, remediation string) {
	if err == nil {
		return
	}
	r.Diagnostics = append(r.Diagnostics, lifecycleDiagnostic{Code: code, Message: err.Error(), Remediation: remediation})
}
