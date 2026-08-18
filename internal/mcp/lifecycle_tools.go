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
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/service"
	"gitcode-mcp/internal/servicectl"
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
	text := fmt.Sprintf(
		"binding_state=%s binary_version=%s cache_schema=%d/%d issue_records=%d issue_comments=%d",
		status.BindingState,
		status.BinaryVersion,
		status.CacheSchemaVersion,
		status.ExpectedCacheSchemaVersion,
		status.IssueRecords,
		status.IssueComments,
	)
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}

type syncLiveArgs struct {
	RepoID         string `json:"repo_id"`
	Issues         bool   `json:"issues,omitempty"`
	Wiki           bool   `json:"wiki,omitempty"`
	Comments       bool   `json:"comments,omitempty"`
	IssueComments  bool   `json:"issue_comments,omitempty"`
	PRComments     bool   `json:"pr_comments,omitempty"`
	Pulls          bool   `json:"pulls,omitempty"`
	RemoteAlias    string `json:"remote_alias,omitempty"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	MaxPages       int    `json:"max_pages,omitempty"`
	MaxRecords     int    `json:"max_records,omitempty"`
	PerPage        int    `json:"per_page,omitempty"`
	Daemon         bool   `json:"daemon,omitempty"`
	Detach         bool   `json:"detach,omitempty"`
}

type syncLiveCommentSelection struct {
	Issue bool
	PR    bool
}

type syncLiveResult struct {
	RepoID        string                            `json:"repo_id"`
	Collections   []string                          `json:"collections"`
	FreshCount    int                               `json:"fresh_count"`
	SuccessCount  int                               `json:"success_count"`
	FailureCount  int                               `json:"failure_count"`
	Results       []service.SyncResult              `json:"results,omitempty"`
	Failures      []service.ResourceError           `json:"failures,omitempty"`
	IssueComments *service.IssueCommentQueueSummary `json:"issue_comments,omitempty"`
	Job           *servicectl.Job                   `json:"job,omitempty"`
	Diagnostics   []lifecycleDiagnostic             `json:"diagnostics,omitempty"`
	GeneratedAt   time.Time                         `json:"generated_at"`
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
	commentSelection, message := resolveSyncLiveCommentSelection(a)
	if message != "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: message})
		return
	}
	if message := syncLiveCommentSurfaceError(a); message != "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: message})
		return
	}
	if message := syncLiveTargetError(a); message != "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: message})
		return
	}
	selected := syncLiveCollections(a, commentSelection)
	result := syncLiveResult{RepoID: a.RepoID, Collections: selected, GeneratedAt: time.Now().UTC()}
	if a.Daemon || a.Detach {
		if strings.TrimSpace(a.RemoteAlias) != "" {
			s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "daemon sync_live supports collection selectors only; omit remote_alias"})
			return
		}
		if len(result.Collections) == 0 {
			result.Collections = []string{"issues", "wiki"}
		}
		client, err := serviceRPCClient()
		if err != nil {
			s.writeDomainError(id, err)
			return
		}
		var job servicectl.Job
		if err := client.Call(ctx, "Jobs.StartSync", syncLiveJobRequest(a, commentSelection), &job); err != nil {
			s.writeDomainError(id, err)
			return
		}
		result.Job = &job
		text := fmt.Sprintf("job=%s status=%s collections=%s", job.ID, job.Status, strings.Join(result.Collections, ","))
		s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
		return
	}
	if mode, ok := lifecycleProviderMode(s.svc); ok && mode != gitcode.ProviderModeLive {
		if len(result.Collections) == 0 {
			result.Collections = []string{"issues", "wiki"}
		}
		result.Diagnostics = append(result.Diagnostics, lifecycleDiagnostic{
			Code:        "mcp_live_not_configured",
			ErrorClass:  "configuration_error",
			Message:     "sync_live requires a live GitCode provider, but this MCP server is using " + string(mode),
			Remediation: "restart the MCP server with live credentials available or pass --live explicitly",
		})
		text := fmt.Sprintf("fresh_count=0 collections=%s", strings.Join(result.Collections, ","))
		s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
		return
	}
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

	defaultCollections := len(selected) == 0
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
	case a.Issues && a.Wiki && !a.Pulls && !commentSelection.Issue && !commentSelection.PR:
		runBulk("all", s.svc.BulkSyncAll)
	default:
		if a.Issues || defaultCollections {
			runBulk("issues", s.svc.BulkSyncIssues)
		}
		if commentSelection.Issue {
			runBulk("issue_comments", s.svc.BulkSyncIssueComments)
		}
		if a.Wiki || defaultCollections {
			runBulk("wiki", s.svc.BulkSyncWiki)
		}
		if a.Pulls {
			runBulk("pulls", s.svc.BulkSyncPullRequests)
		}
		if commentSelection.PR {
			runBulk("pr_comments", s.svc.BulkSyncPRComments)
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

func syncLiveJobRequest(a syncLiveArgs, comments syncLiveCommentSelection) servicectl.StartSyncJobRequest {
	return servicectl.StartSyncJobRequest{
		RepoID:         strings.TrimSpace(a.RepoID),
		ProviderMode:   string(gitcode.ProviderModeLive),
		Issues:         a.Issues,
		Wiki:           a.Wiki,
		Pulls:          a.Pulls,
		IssueComments:  comments.Issue,
		PRComments:     comments.PR,
		IdempotencyKey: strings.TrimSpace(a.IdempotencyKey),
		MaxPages:       a.MaxPages,
		MaxRecords:     a.MaxRecords,
		PerPage:        a.PerPage,
	}
}

func appendBulkSyncResult(dst *syncLiveResult, src *service.SyncResourcesResult) {
	if dst == nil || src == nil {
		return
	}
	dst.Results = append(dst.Results, src.Results...)
	dst.Failures = append(dst.Failures, src.Failures...)
	dst.SuccessCount = len(dst.Results)
	dst.FailureCount = len(dst.Failures)
	if src.IssueComments != nil {
		queue := *src.IssueComments
		dst.IssueComments = &queue
	}
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

func syncLiveCollections(a syncLiveArgs, comments syncLiveCommentSelection) []string {
	var selected []string
	if a.Issues {
		selected = append(selected, "issues")
	}
	if comments.Issue {
		selected = append(selected, "issue_comments")
	}
	if a.Wiki {
		selected = append(selected, "wiki")
	}
	if a.Pulls {
		selected = append(selected, "pulls")
	}
	if comments.PR {
		selected = append(selected, "pr_comments")
	}
	return selected
}

func resolveSyncLiveCommentSelection(a syncLiveArgs) (syncLiveCommentSelection, string) {
	selection := syncLiveCommentSelection{Issue: a.IssueComments, PR: a.PRComments}
	if !a.Comments {
		return selection, ""
	}
	if a.IssueComments || a.PRComments {
		return syncLiveCommentSelection{}, "comments cannot be combined with issue_comments or pr_comments; use the explicit selectors only"
	}
	if strings.TrimSpace(a.RemoteAlias) != "" {
		if syncLiveRemoteAliasSurface(a.RemoteAlias) == "issue" {
			selection.Issue = true
		}
		return selection, ""
	}
	if a.Issues && a.Pulls {
		return syncLiveCommentSelection{}, "comments is ambiguous when both issues and pulls are selected; use issue_comments=true and/or pr_comments=true"
	}
	if a.Issues {
		selection.Issue = true
		return selection, ""
	}
	selection.PR = true
	return selection, ""
}

func syncLiveCommentSurfaceError(a syncLiveArgs) string {
	if strings.TrimSpace(a.RemoteAlias) == "" || !(a.Comments || a.IssueComments || a.PRComments) {
		return ""
	}
	if a.Issues || a.Wiki || a.Pulls {
		return "comment sync with remote_alias cannot be combined with collection selectors; use remote_alias issue:N alone or run a collection sync without remote_alias"
	}
	switch syncLiveRemoteAliasSurface(a.RemoteAlias) {
	case "issue":
		if a.PRComments {
			return "pr_comments cannot target issue aliases; use issue_comments=true or comments=true with issue:N"
		}
		return ""
	case "pull_request":
		return "targeted pull request comment sync is not supported; sync pull requests first, then call sync_live with pr_comments=true and no remote_alias"
	default:
		return "comment sync with remote_alias supports issue:N only; use pr_comments=true without remote_alias for pull request comments"
	}
}

func syncLiveTargetError(a syncLiveArgs) string {
	if strings.TrimSpace(a.RemoteAlias) == "" {
		return ""
	}
	if a.MaxPages > 0 || a.MaxRecords > 0 || a.PerPage > 0 {
		return "max_pages, max_records, and per_page apply to collection sync only; omit them for an exact remote_alias sync"
	}
	selected := ""
	selectedCount := 0
	if a.Issues {
		selected, selectedCount = "issue", selectedCount+1
	}
	if a.Wiki {
		selected, selectedCount = "wiki", selectedCount+1
	}
	if a.Pulls {
		selected, selectedCount = "pull_request", selectedCount+1
	}
	if selectedCount == 0 {
		return ""
	}
	if selectedCount > 1 {
		return "an exact remote_alias target can have at most one matching collection selector"
	}
	surface := syncLiveRemoteAliasSurface(a.RemoteAlias)
	if surface == "" {
		return "collection selectors require a matching issue:N, wiki:SLUG, or pr:N remote_alias"
	}
	if selected != surface {
		return fmt.Sprintf("remote_alias targets %s but the selected collection is %s", surface, selected)
	}
	return ""
}

func syncLiveRemoteAliasSurface(alias string) string {
	remoteType, _, ok := strings.Cut(strings.TrimSpace(alias), ":")
	if !ok {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(remoteType)) {
	case "issue", "issues":
		return "issue"
	case "wiki", "page", "remote":
		return "wiki"
	case "pull_request", "pull", "pulls", "pr":
		return "pull_request"
	default:
		return ""
	}
}

func lifecycleProviderMode(svc serviceInterface) (gitcode.ProviderMode, bool) {
	if svc == nil {
		return "", false
	}
	provider, ok := svc.(interface {
		ProviderMode() gitcode.ProviderMode
	})
	if !ok {
		return "", false
	}
	return provider.ProviderMode(), true
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
	ToolAccess   string                           `json:"tool_access"`
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
	result := doctorResult{Status: "ok", RepoID: a.RepoID, ToolAccess: string(s.activeToolAccess()), Diagnostics: []lifecycleDiagnostic{}, GeneratedAt: time.Now().UTC()}
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
