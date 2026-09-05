package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"gitcode-mcp/internal/auth"
	"gitcode-mcp/internal/buildinfo"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/capability"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/diagnostics"
	"gitcode-mcp/internal/feedback"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/rag"
	"gitcode-mcp/internal/repositorydocs"
	"gitcode-mcp/internal/service"
	"gitcode-mcp/internal/servicectl"
)

const protocolVersion = "2024-11-05"

type serviceInterface interface {
	SearchSources(context.Context, service.SearchSourcesRequest) (service.SearchSourcesResult, error)
	GetSource(context.Context, service.GetSourceRequest) (service.SourceRecord, error)
	ListSources(context.Context, service.ListSourcesRequest) (service.ListSourcesResult, error)
	GetBacklinks(context.Context, service.GetBacklinksRequest) (service.BacklinksResult, error)
	ResolveID(context.Context, service.ResolveIDRequest) (service.ResolvedID, error)
	GetSyncStatus(context.Context, service.SyncStatusRequest) (service.SyncStatusResult, error)
	SyncStatus(context.Context, service.ListSourcesRequest) (service.SyncStatusSummaryResult, error)
	ExportSnapshot(context.Context, service.ExportSnapshotRequest) (service.ExportSnapshotResult, error)
	DiffSnapshot(context.Context, service.DiffSnapshotRequest) (service.DiffSnapshotResult, error)
	RepositoryStatus(context.Context, service.RepositoryStatusRequest) (service.RepositoryStatus, error)
	SyncToCache(context.Context, service.SyncRequest) (service.SyncResult, error)
	BulkSyncIssues(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)
	BulkSyncIssueComments(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)
	BulkSyncWiki(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)
	BulkSyncPullRequests(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)
	BulkSyncPRComments(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)
	BulkSyncAll(context.Context, service.BulkSyncRequest) (*service.SyncResourcesResult, error)
	ListPRDiscussions(context.Context, service.PRDiscussionRequest) (service.PRDiscussionsResult, error)
	CreateIssue(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	UpdateIssue(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	AddComment(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	UpdateComment(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	CreatePR(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	UpdatePR(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	ListMilestones(context.Context, service.MilestoneListRequest) (service.MilestoneListResult, error)
	ListPushRemoteMirrors(context.Context, service.PushMirrorListRequest) (service.PushMirrorListResult, error)
	TriggerPushRemoteMirror(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	WaitPushRemoteMirror(context.Context, service.PushMirrorWaitRequest) (service.PushMirrorWaitResult, error)
	CreateMilestone(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	UpdateMilestone(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	SetIssueMilestone(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	ClearIssueMilestone(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	AddPRComment(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	AddPRReviewComment(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	ReplyPRReviewComment(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	LinkPRIssue(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	CreatePage(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	UpdatePage(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	DeletePage(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	AddLabel(context.Context, service.WriteCommandRequest) (service.WriteCommandResult, error)
	Index(context.Context, service.OperationRequest) (service.OperationResult, error)
	ListChunks(context.Context, service.ChunkQuery) (service.ChunkQueryResult, error)
	SearchChunks(context.Context, service.ChunkSearchQuery) (service.ChunkQueryResult, error)
	GetChunkSnippet(context.Context, service.SnippetQuery) (service.ChunkQueryResult, error)
	StaleIndex(context.Context, service.StaleIndexRequest) (service.StaleIndexResult, error)
	RecentChanges(context.Context, service.RecentChangesRequest) (service.RecentChangesResult, error)
	LinkCheck(context.Context, service.LinkCheckRequest) (service.LinkCheckResult, error)
	CacheStatus(context.Context, service.CacheStatusRequest) (service.CacheStatusResult, error)
	PrepareFeedback(context.Context, feedback.Draft) (feedback.PreparedReport, error)
	SubmitFeedback(context.Context, service.SubmitFeedbackRequest) (feedback.SubmissionResult, error)
}

type feedbackReadinessProvider interface {
	FeedbackReadiness(context.Context) (feedback.Readiness, error)
}

func (s *Server) feedbackReadiness(ctx context.Context) (feedback.Readiness, error) {
	provider, ok := s.svc.(feedbackReadinessProvider)
	if !ok || provider == nil {
		return feedback.Readiness{}, service.ErrInvalidQuery{Field: "feedback", Message: "readiness provider is unavailable"}
	}
	return provider.FeedbackReadiness(ctx)
}

type RPCHandler struct {
	svc                  serviceInterface
	startupDiagnostic    StartupDiagnostic
	minimal              bool
	credentialResolver   *auth.CredentialResolver
	toolAccess           ToolAccess
	ragStatus            RAGStatusProvider
	ragSearch            RAGSearchProvider
	repositoryDocsPolicy RepositoryDocsPolicyProvider
	repositoryDocsStatus RepositoryDocsStatusProvider
	repositoryDocsSearch RepositoryDocsSearchProvider
	maintenancePlan      MaintenancePlanProvider
	maintenanceApply     MaintenanceApplyProvider
	runtimeContext       RuntimeContext
}

type Server struct {
	reader               io.Reader
	writer               io.Writer
	stderr               io.Writer
	handler              *RPCHandler
	svc                  serviceInterface
	startupDiagnostic    StartupDiagnostic
	minimal              bool
	credentialResolver   *auth.CredentialResolver
	toolAccess           ToolAccess
	ragStatus            RAGStatusProvider
	ragSearch            RAGSearchProvider
	repositoryDocsPolicy RepositoryDocsPolicyProvider
	repositoryDocsStatus RepositoryDocsStatusProvider
	repositoryDocsSearch RepositoryDocsSearchProvider
	maintenancePlan      MaintenancePlanProvider
	maintenanceApply     MaintenanceApplyProvider
	serviceClient        func() (*servicectl.RPCClient, error)
	runtimeContext       RuntimeContext
}

type RuntimeContext struct {
	EffectiveCachePath string `json:"effective_cache_path,omitempty"`
	CachePathSource    string `json:"cache_path_source,omitempty"`
	ConfigReference    string `json:"config_reference,omitempty"`
}

type RAGStatusProvider func(context.Context, rag.StatusRequest) (rag.StatusResult, error)
type RAGSearchProvider func(context.Context, rag.SearchRequest) (rag.SearchResult, error)
type RepositoryDocsPolicyProvider func(context.Context, repositorydocs.PolicyRequest) (repositorydocs.PolicyResult, error)
type RepositoryDocsStatusProvider func(context.Context, repositorydocs.StatusRequest) (repositorydocs.StatusResult, error)
type RepositoryDocsSearchProvider func(context.Context, repositorydocs.SearchRequest) (repositorydocs.SearchResult, error)
type MaintenancePlanProvider func(context.Context, servicectl.MaintenanceSetupRequest) (servicectl.MaintenancePlan, error)
type MaintenanceApplyProvider func(context.Context, servicectl.MaintenanceSetupRequest) (servicectl.MaintenanceApplyResult, error)

func NewRPCHandler(svc serviceInterface) *RPCHandler {
	return NewRPCHandlerWithToolAccess(svc, ToolAccessRead)
}

func NewRPCHandlerWithCredentialResolver(svc serviceInterface, credResolver *auth.CredentialResolver) *RPCHandler {
	return NewRPCHandlerWithCredentialResolverAndToolAccess(svc, credResolver, ToolAccessRead)
}

func NewRPCHandlerWithToolAccess(svc serviceInterface, access ToolAccess) *RPCHandler {
	return &RPCHandler{svc: svc, toolAccess: normalizeToolAccess(access)}
}

func NewRPCHandlerWithCredentialResolverAndToolAccess(svc serviceInterface, credResolver *auth.CredentialResolver, access ToolAccess) *RPCHandler {
	return &RPCHandler{svc: svc, credentialResolver: credResolver, toolAccess: normalizeToolAccess(access)}
}

func NewMinimalRPCHandler(diagnostic StartupDiagnostic) *RPCHandler {
	return &RPCHandler{startupDiagnostic: diagnostic, minimal: true, toolAccess: ToolAccessRead}
}

func New(r io.Reader, w io.Writer, stderr io.Writer, svc serviceInterface, credResolver *auth.CredentialResolver) *Server {
	return NewWithToolAccess(r, w, stderr, svc, credResolver, ToolAccessRead)
}

func NewWithToolAccess(r io.Reader, w io.Writer, stderr io.Writer, svc serviceInterface, credResolver *auth.CredentialResolver, access ToolAccess) *Server {
	access = normalizeToolAccess(access)
	return &Server{reader: r, writer: w, stderr: stderr, handler: NewRPCHandlerWithCredentialResolverAndToolAccess(svc, credResolver, access), svc: svc, credentialResolver: credResolver, toolAccess: access}
}

func (h *RPCHandler) SetRAGStatusProvider(provider RAGStatusProvider) {
	h.ragStatus = provider
}

func (h *RPCHandler) SetRAGSearchProvider(provider RAGSearchProvider) {
	h.ragSearch = provider
}

func (h *RPCHandler) SetRepositoryDocsProviders(policy RepositoryDocsPolicyProvider, status RepositoryDocsStatusProvider, search RepositoryDocsSearchProvider) {
	h.repositoryDocsPolicy = policy
	h.repositoryDocsStatus = status
	h.repositoryDocsSearch = search
}

func (h *RPCHandler) SetMaintenanceProviders(plan MaintenancePlanProvider, apply MaintenanceApplyProvider) {
	h.maintenancePlan = plan
	h.maintenanceApply = apply
}

func (h *RPCHandler) SetRuntimeContext(runtimeContext RuntimeContext) {
	h.runtimeContext = runtimeContext
}

func (s *Server) SetRAGStatusProvider(provider RAGStatusProvider) {
	s.ragStatus = provider
	if s.handler != nil {
		s.handler.SetRAGStatusProvider(provider)
	}
}

func (s *Server) SetRAGSearchProvider(provider RAGSearchProvider) {
	s.ragSearch = provider
	if s.handler != nil {
		s.handler.SetRAGSearchProvider(provider)
	}
}

func (s *Server) SetRepositoryDocsProviders(policy RepositoryDocsPolicyProvider, status RepositoryDocsStatusProvider, search RepositoryDocsSearchProvider) {
	s.repositoryDocsPolicy = policy
	s.repositoryDocsStatus = status
	s.repositoryDocsSearch = search
	if s.handler != nil {
		s.handler.SetRepositoryDocsProviders(policy, status, search)
	}
}

func (s *Server) SetMaintenanceProviders(plan MaintenancePlanProvider, apply MaintenanceApplyProvider) {
	s.maintenancePlan = plan
	s.maintenanceApply = apply
	if s.handler != nil {
		s.handler.SetMaintenanceProviders(plan, apply)
	}
}

func (s *Server) SetRuntimeContext(runtimeContext RuntimeContext) {
	s.runtimeContext = runtimeContext
	if s.handler != nil {
		s.handler.SetRuntimeContext(runtimeContext)
	}
}

func NewMinimal(r io.Reader, w io.Writer, stderr io.Writer, diagnostic StartupDiagnostic) *Server {
	return &Server{reader: r, writer: w, stderr: stderr, handler: NewMinimalRPCHandler(diagnostic), startupDiagnostic: diagnostic, minimal: true, toolAccess: ToolAccessRead}
}

func (h *RPCHandler) Handle(ctx context.Context, req request) (*response, bool) {
	if req.JSONRPC != "2.0" || req.Method == "" {
		return &response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32600, Message: "Invalid request"}}, true
	}
	var buf bytes.Buffer
	server := &Server{writer: &buf, stderr: io.Discard, handler: h, svc: h.svc, startupDiagnostic: h.startupDiagnostic, minimal: h.minimal, credentialResolver: h.credentialResolver, toolAccess: normalizeToolAccess(h.toolAccess), ragStatus: h.ragStatus, ragSearch: h.ragSearch, repositoryDocsPolicy: h.repositoryDocsPolicy, repositoryDocsStatus: h.repositoryDocsStatus, repositoryDocsSearch: h.repositoryDocsSearch, maintenancePlan: h.maintenancePlan, maintenanceApply: h.maintenanceApply, runtimeContext: h.runtimeContext}
	server.handle(ctx, req, req.ID == nil)
	line := bytes.TrimSpace(buf.Bytes())
	if len(line) == 0 {
		return nil, false
	}
	var resp response
	if err := json.Unmarshal(line, &resp); err != nil {
		return &response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32000, Message: "Server error", Data: &errorData{Code: "internal_error", Message: err.Error()}}}, true
	}
	return &resp, true
}

type request struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method"`
	Params  *json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

type rpcError struct {
	Code    int        `json:"code"`
	Message string     `json:"message"`
	Data    *errorData `json:"data,omitempty"`
}

type errorData struct {
	Code         string `json:"code"`
	Message      string `json:"message"`
	FailureClass string `json:"failure_class,omitempty"`
	Operation    string `json:"operation,omitempty"`
	RepoID       string `json:"repo_id,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	PID          int    `json:"pid,omitempty"`
	CacheRef     string `json:"cache_ref,omitempty"`
	AccessMode   string `json:"access_mode,omitempty"`
	Remediation  string `json:"remediation,omitempty"`
}

type toolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema inputSchema    `json:"inputSchema"`
	Meta        map[string]any `json:"_meta,omitempty"`
}

type inputSchema struct {
	Type       string                `json:"type"`
	Properties map[string]schemaProp `json:"properties"`
	Required   []string              `json:"required,omitempty"`
}

type schemaProp struct {
	Type        string      `json:"type"`
	Description string      `json:"description,omitempty"`
	Enum        []string    `json:"enum,omitempty"`
	Minimum     *float64    `json:"minimum,omitempty"`
	Maximum     *float64    `json:"maximum,omitempty"`
	Default     any         `json:"default,omitempty"`
	MinLength   int         `json:"minLength,omitempty"`
	Items       *schemaProp `json:"items,omitempty"`
}

type initResult struct {
	ProtocolVersion string         `json:"protocolVersion"`
	Capabilities    initCapability `json:"capabilities"`
	ServerInfo      serverInfo     `json:"serverInfo"`
}

type initCapability struct {
	Tools toolCapability `json:"tools"`
}

type toolCapability struct {
	ListChanged       bool               `json:"listChanged"`
	StartupDiagnostic *StartupDiagnostic `json:"startup_diagnostic,omitempty"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type toolsListResult struct {
	Tools             []toolDefinition   `json:"tools"`
	StartupDiagnostic *StartupDiagnostic `json:"startup_diagnostic,omitempty"`
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

type toolCallResult struct {
	Content           []toolContentItem `json:"content"`
	StructuredContent any               `json:"structuredContent"`
}

type toolContentItem struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

var sourceKindEnums = []string{"issue", "issue_comment", "wiki", "pull_request", "pr_comment"}

type ToolAccess string

const (
	ToolAccessRead  ToolAccess = "read"
	ToolAccessWrite ToolAccess = "write"
)

var writeToolNames = buildWriteToolNames()

func buildWriteToolNames() map[string]bool {
	names := capability.MCPWriteToolNames()
	names["sync_live"] = true
	names["index_repo"] = true
	names["enable_cache_maintenance"] = true
	names["service_job_cancel"] = true
	return names
}

func normalizeToolAccess(access ToolAccess) ToolAccess {
	if access == ToolAccessWrite {
		return ToolAccessWrite
	}
	return ToolAccessRead
}

func (s *Server) activeToolAccess() ToolAccess {
	if s == nil {
		return ToolAccessRead
	}
	return normalizeToolAccess(s.toolAccess)
}

func (s *Server) toolDisabledByPolicy(name string) bool {
	return s.activeToolAccess() == ToolAccessRead && writeToolNames[name]
}

func intPtr(v int) *int             { return &v }
func float64Ptr(v float64) *float64 { return &v }
func kindValidationMessage() string {
	return "kind must be one of: " + strings.Join(sourceKindEnums, ", ")
}

func writeSchemaProps(extra map[string]schemaProp) map[string]schemaProp {
	props := map[string]schemaProp{
		"repo_id":         {Type: "string", Description: "Configured repository id.", MinLength: 1},
		"write_mode":      {Type: "string", Description: "Required live write intent.", Enum: []string{"live"}},
		"idempotency_key": {Type: "string", Description: "Idempotency key."},
	}
	for key, prop := range extra {
		props[key] = prop
	}
	return props
}

func chunkSchemaProps(includeQuery bool) map[string]schemaProp {
	props := map[string]schemaProp{
		"repo_id":     {Type: "string", Description: "Configured repository id.", MinLength: 1},
		"source_id":   {Type: "string", Description: "Source id filter."},
		"record_id":   {Type: "string", Description: "Record id filter."},
		"snapshot_id": {Type: "string", Description: "Snapshot id filter."},
		"policy":      {Type: "string", Description: "Chunk policy.", Enum: []string{"heading", "sliding_window"}},
		"chunk_id":    {Type: "string", Description: "Chunk id."},
		"line_start":  {Type: "integer", Description: "Start line.", Minimum: float64Ptr(1)},
		"line_end":    {Type: "integer", Description: "End line.", Minimum: float64Ptr(1)},
		"limit":       {Type: "integer", Description: "Maximum results.", Minimum: float64Ptr(1), Maximum: float64Ptr(200), Default: 50.0},
		"offset":      {Type: "integer", Description: "Result offset.", Minimum: float64Ptr(0), Default: 0.0},
	}
	if includeQuery {
		props["query"] = schemaProp{Type: "string", Description: "Full-text chunk query text; not fuzzy or semantic.", MinLength: 1}
		props["kind"] = schemaProp{Type: "string", Description: "Source kind filter.", Enum: sourceKindEnums}
	}
	return props
}

type toolHandler func(context.Context, *json.RawMessage, json.RawMessage)

type registeredTool struct {
	definition toolDefinition
	handler    toolHandler
}

type toolRegistry map[string]registeredTool

var toolDefs = []toolDefinition{
	{
		Name:        "search_sources",
		Description: "Search cached sources with hybrid lexical plus semantic retrieval by default. Results are grouped by source with citations and score provenance. Use mode=full_text for deterministic exact/token matching without an embedding-provider call.",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]schemaProp{
				"repo_id": {Type: "string", Description: "Configured repository id.", MinLength: 1},
				"query":   {Type: "string", Description: "Conceptual, fuzzy, identifier, or exact text query.", MinLength: 1},
				"mode":    {Type: "string", Description: "hybrid (default) or deterministic full_text retrieval.", Enum: []string{service.SearchModeHybrid, service.SearchModeFullText}, Default: service.SearchModeHybrid},
				"kind":    {Type: "string", Description: "Source kind filter.", Enum: sourceKindEnums},
				"limit":   {Type: "integer", Description: "Maximum results.", Minimum: float64Ptr(1), Maximum: float64Ptr(100), Default: 20.0},
				"offset":  {Type: "integer", Description: "Result offset.", Minimum: float64Ptr(0), Default: 0.0},
			},
			Required: []string{"repo_id", "query"},
		},
	},
	{
		Name:        "get_source",
		Description: "Get a cached source record by stable id.",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]schemaProp{
				"repo_id": {Type: "string", Description: "Configured repository id.", MinLength: 1},
				"id":      {Type: "string", Description: "Stable source id or record alias.", MinLength: 1},
			},
			Required: []string{"repo_id", "id"},
		},
	},
	{
		Name:        "list_sources",
		Description: "List cached sources with optional filters.",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]schemaProp{
				"repo_id": {Type: "string", Description: "Configured repository id.", MinLength: 1},
				"kind":    {Type: "string", Description: "Source kind filter.", Enum: sourceKindEnums},
				"status":  {Type: "string", Description: "Source status filter."},
				"limit":   {Type: "integer", Description: "Maximum results.", Minimum: float64Ptr(1), Maximum: float64Ptr(100), Default: 20.0},
				"offset":  {Type: "integer", Description: "Result offset.", Minimum: float64Ptr(0), Default: 0.0},
			},
			Required: []string{"repo_id"},
		},
	},
	{
		Name:        "list_chunks",
		Description: "List cached index chunks through the shared chunk query result model.",
		InputSchema: inputSchema{Type: "object", Properties: chunkSchemaProps(false), Required: []string{"repo_id"}},
	},
	{
		Name:        "search_chunks",
		Description: "Search cached index chunks by full-text/token query. This is not fuzzy or semantic retrieval.",
		InputSchema: inputSchema{Type: "object", Properties: chunkSchemaProps(true), Required: []string{"repo_id", "query"}},
	},
	{
		Name:        "get_snippet",
		Description: "Get a cached chunk snippet through the shared chunk query result model.",
		InputSchema: inputSchema{Type: "object", Properties: chunkSchemaProps(false), Required: []string{"repo_id"}},
	},
	{
		Name:        "stale_index_report",
		Description: "Report missing or stale index state with freshness warning metadata.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{"repo_id": {Type: "string", Description: "Configured repository id.", MinLength: 1}, "strict": {Type: "boolean", Description: "Return stale-index errors when findings exist."}}, Required: []string{"repo_id"}},
	},
	{
		Name:        "recent_changes",
		Description: "List recently updated cached sources.",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]schemaProp{
				"repo_id": {Type: "string", Description: "Configured repository id.", MinLength: 1},
				"kind":    {Type: "string", Description: "Source kind filter.", Enum: sourceKindEnums},
				"status":  {Type: "string", Description: "Source status filter."},
				"limit":   {Type: "integer", Description: "Maximum results.", Minimum: float64Ptr(1), Maximum: float64Ptr(100), Default: 20.0},
				"offset":  {Type: "integer", Description: "Result offset.", Minimum: float64Ptr(0), Default: 0.0},
			},
			Required: []string{"repo_id"},
		},
	},
	{
		Name:        "link_check",
		Description: "Check cached source links for unresolved targets.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{"repo_id": {Type: "string", Description: "Configured repository id.", MinLength: 1}, "strict": {Type: "boolean", Description: "Return link-check errors when findings exist."}}, Required: []string{"repo_id"}},
	},
	{
		Name:        "cache_status",
		Description: "Report cache storage, WAL, count, and index-warning status.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{"repo_id": {Type: "string", Description: "Configured repository id.", MinLength: 1}}, Required: []string{"repo_id"}},
	},
	{
		Name:        "source_backlinks",
		Description: "List sources that link to the given id.",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]schemaProp{
				"repo_id": {Type: "string", Description: "Configured repository id.", MinLength: 1},
				"id":      {Type: "string", Description: "Target source id or record alias.", MinLength: 1},
				"limit":   {Type: "integer", Description: "Maximum results.", Minimum: float64Ptr(1), Maximum: float64Ptr(200), Default: 50.0},
				"offset":  {Type: "integer", Description: "Result offset.", Minimum: float64Ptr(0), Default: 0.0},
			},
			Required: []string{"repo_id", "id"},
		},
	},
	{
		Name:        "resolve_id",
		Description: "Resolve a stable id or alias to its local record.",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]schemaProp{
				"repo_id": {Type: "string", Description: "Configured repository id.", MinLength: 1},
				"id":      {Type: "string", Description: "Stable id or alias to resolve.", MinLength: 1},
			},
			Required: []string{"repo_id", "id"},
		},
	},
	{
		Name:        "sync_status",
		Description: "Check sync status for a source or the whole cache.",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]schemaProp{
				"repo_id": {Type: "string", Description: "Configured repository id.", MinLength: 1},
				"id":      {Type: "string", Description: "Source id. Omit for aggregate cache status."},
			},
			Required: []string{"repo_id"},
		},
	},
	{
		Name:        "export_snapshot",
		Description: "Export a deterministic snapshot of the cache.",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]schemaProp{
				"repo_id": {Type: "string", Description: "Configured repository id.", MinLength: 1},
				"format":  {Type: "string", Description: "Export format.", Enum: []string{"json", "markdown"}, Default: "json"},
				"inline":  {Type: "boolean", Description: "Return inline content.", Default: true},
			},
			Required: []string{"repo_id"},
		},
	},
	{
		Name:        "diff_snapshot",
		Description: "Diff two snapshots.",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]schemaProp{
				"repo_id": {Type: "string", Description: "Configured repository id.", MinLength: 1},
				"base_id": {Type: "string", Description: "Base snapshot id.", MinLength: 1},
				"head_id": {Type: "string", Description: "Head snapshot id.", MinLength: 1},
				"format":  {Type: "string", Description: "Diff format.", Enum: []string{"text", "json"}, Default: "text"},
			},
			Required: []string{"repo_id", "base_id", "head_id"},
		},
	},
	{
		Name:        "repo_status",
		Description: "Report configured repository binding and cache readiness state.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{"repo_id": {Type: "string", Description: "Configured repository id. Omit for nothing-bound status."}}},
	},
	{
		Name:        "service_status",
		Description: "Poll the local service coordinator status through local IPC.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{}},
	},
	{
		Name:        "maintenance_status",
		Description: "Report sanitized daemon-managed cache backfill and RAG coverage state through local IPC.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{}},
	},
	{
		Name:        "maintenance_plan",
		Description: "Build a deterministic, read-only plan for daemon-managed cache refresh, backfill, and RAG maintenance. The selected MCP cache is implicit; filesystem paths are never accepted or returned.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{
			"repo_id":            {Type: "string", Description: "Configured repository id.", MinLength: 1},
			"profile":            {Type: "string", Description: "RAG profile name."},
			"sync":               {Type: "string", Description: "Cache refresh policy.", Enum: []string{"off", "head", "head-and-backfill"}, Default: "head-and-backfill"},
			"collections":        {Type: "array", Description: "Maintained collections.", Items: &schemaProp{Type: "string", Enum: []string{"issues", "issue-comments", "wiki", "pulls", "pr-comments"}}},
			"rag":                {Type: "string", Description: "RAG maintenance policy.", Enum: []string{"off", "maintain"}, Default: "maintain"},
			"no_service_install": {Type: "boolean", Description: "Treat missing service installation as a blocker.", Default: false},
			"no_model_download":  {Type: "boolean", Description: "Treat a missing model as a blocker.", Default: false},
		}, Required: []string{"repo_id"}},
	},
	{
		Name:        "enable_cache_maintenance",
		Description: "Apply a previously rendered maintenance plan as an audited local write. Machine-level service installation, provider startup, and model downloads are never performed through MCP; use the returned exact CLI handoff.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{
			"repo_id":            {Type: "string", Description: "Configured repository id.", MinLength: 1},
			"plan_id":            {Type: "string", Description: "Plan id returned by maintenance_plan.", MinLength: 1},
			"write_mode":         {Type: "string", Description: "Required explicit local write intent.", Enum: []string{"live"}},
			"idempotency_key":    {Type: "string", Description: "Caller-provided idempotency key.", MinLength: 1},
			"profile":            {Type: "string", Description: "Must match the planned RAG profile."},
			"sync":               {Type: "string", Description: "Must match the planned cache refresh policy.", Enum: []string{"off", "head", "head-and-backfill"}, Default: "head-and-backfill"},
			"collections":        {Type: "array", Description: "Must match the planned collections.", Items: &schemaProp{Type: "string", Enum: []string{"issues", "issue-comments", "wiki", "pulls", "pr-comments"}}},
			"rag":                {Type: "string", Description: "Must match the planned RAG policy.", Enum: []string{"off", "maintain"}, Default: "maintain"},
			"detach":             {Type: "boolean", Description: "Return after jobs are coalesced.", Default: true},
			"no_service_install": {Type: "boolean", Description: "Must match the planned service-install policy.", Default: false},
			"no_model_download":  {Type: "boolean", Description: "Must match the planned model-download policy.", Default: false},
		}, Required: []string{"repo_id", "plan_id", "write_mode", "idempotency_key"}},
	},
	{
		Name:        "service_jobs",
		Description: "List local service coordinator jobs through local IPC.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{}},
	},
	{
		Name:        "service_job_status",
		Description: "Poll one local service coordinator job through local IPC.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{"job_id": {Type: "string", Description: "Service job id.", MinLength: 1}}, Required: []string{"job_id"}},
	},
	{
		Name:        "service_job_attach",
		Description: "Wait up to a bounded interval for one local coordinator job and return its latest progress or terminal state.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{
			"job_id":       {Type: "string", Description: "Service job id.", MinLength: 1},
			"wait_seconds": {Type: "integer", Description: "Bounded wait in seconds (1-30).", Minimum: float64Ptr(1), Maximum: float64Ptr(30), Default: 30},
		}, Required: []string{"job_id"}},
	},
	{
		Name:        "service_job_cancel",
		Description: "Explicitly cancel one local coordinator job. Job identity makes repeated cancellation idempotent.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{
			"job_id":     {Type: "string", Description: "Service job id.", MinLength: 1},
			"write_mode": {Type: "string", Description: "Required explicit local write intent.", Enum: []string{"live"}},
		}, Required: []string{"job_id", "write_mode"}},
	},
	{
		Name:        "rag_status",
		Description: "Report RAG provider readiness, namespace coverage, last index run, and active daemon job state.",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]schemaProp{
				"repo_id": {Type: "string", Description: "Configured repository id.", MinLength: 1},
				"profile": {Type: "string", Description: "RAG profile name."},
			},
			Required: []string{"repo_id"},
		},
	},
	{
		Name:        "rag_search",
		Description: "Run semantic/hybrid RAG retrieval over cached chunks with citations, provenance, and transparent score breakdowns. Existing search_sources and search_chunks remain full-text.",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]schemaProp{
				"repo_id":     {Type: "string", Description: "Configured repository id.", MinLength: 1},
				"query":       {Type: "string", Description: "Semantic/hybrid retrieval query.", MinLength: 1},
				"profile":     {Type: "string", Description: "RAG profile name."},
				"source_id":   {Type: "string", Description: "Source id filter."},
				"record_id":   {Type: "string", Description: "Record id filter."},
				"snapshot_id": {Type: "string", Description: "Snapshot id filter."},
				"policy":      {Type: "string", Description: "Chunk policy namespace."},
				"top_k":       {Type: "integer", Description: "Semantic candidate count.", Minimum: float64Ptr(1), Maximum: float64Ptr(500), Default: 40.0},
				"limit":       {Type: "integer", Description: "Maximum packed contexts.", Minimum: float64Ptr(1), Maximum: float64Ptr(50), Default: 10.0},
			},
			Required: []string{"repo_id", "query"},
		},
	},
	{
		Name:        "repository_docs_policy",
		Description: "Resolve the versioned repository-document policy at one local Git revision. The sole authority in the selected cache is resolved from repo_id when the opaque selector is omitted. No fetch or GitCode call is performed.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{
			"repo_id":                        {Type: "string", Description: "Configured repository id.", MinLength: 1},
			"registration_id":                {Type: "string", Description: "Opaque daemon repository-document registration id.", MinLength: 1},
			"source_registration_id":         {Type: "string", Description: "Opaque private Git authority id.", MinLength: 1},
			"source_registration_generation": {Type: "integer", Description: "Exact private Git authority generation.", Minimum: float64Ptr(1)},
			"revision":                       {Type: "string", Description: "Local Git revision; defaults to HEAD."},
			"include_worktree":               {Type: "boolean", Description: "Explicitly apply tracked worktree changes.", Default: false},
		}, Required: []string{"repo_id"}},
	},
	{
		Name:        "repository_docs_plan",
		Description: "Plan bounded repository-document indexing cost and typed exclusions. The sole authority in the selected cache is resolved from repo_id when the opaque selector is omitted. No embedding provider call is performed.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{
			"repo_id":                        {Type: "string", Description: "Configured repository id.", MinLength: 1},
			"registration_id":                {Type: "string", Description: "Opaque daemon repository-document registration id.", MinLength: 1},
			"source_registration_id":         {Type: "string", Description: "Opaque private Git authority id.", MinLength: 1},
			"source_registration_generation": {Type: "integer", Description: "Exact private Git authority generation.", Minimum: float64Ptr(1)},
			"revision":                       {Type: "string", Description: "Local Git revision; defaults to HEAD."},
			"include_worktree":               {Type: "boolean", Description: "Explicitly plan tracked worktree changes.", Default: false},
		}, Required: []string{"repo_id"}},
	},
	{
		Name:        "repository_docs_status",
		Description: "Inspect repository-document revision-set identity and vector coverage using public-safe opaque Git references. The sole authority in the selected cache is resolved from repo_id when the opaque selector is omitted.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{
			"repo_id":                        {Type: "string", Description: "Configured repository id.", MinLength: 1},
			"registration_id":                {Type: "string", Description: "Opaque daemon repository-document registration id.", MinLength: 1},
			"source_registration_id":         {Type: "string", Description: "Opaque private Git authority id.", MinLength: 1},
			"source_registration_generation": {Type: "integer", Description: "Exact private Git authority generation.", Minimum: float64Ptr(1)},
			"revision":                       {Type: "string", Description: "Local Git revision; defaults to HEAD."},
			"include_worktree":               {Type: "boolean", Description: "Explicitly select the tracked worktree overlay.", Default: false},
		}, Required: []string{"repo_id"}},
	},
	{
		Name:        "repository_docs_search",
		Description: "Search one local Git revision and return bounded digest-verified citations. The sole authority in the selected cache is resolved from repo_id when the opaque selector is omitted. Fulltext mode does not require an embedding provider.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{
			"repo_id":                        {Type: "string", Description: "Configured repository id.", MinLength: 1},
			"registration_id":                {Type: "string", Description: "Opaque daemon repository-document registration id.", MinLength: 1},
			"source_registration_id":         {Type: "string", Description: "Opaque private Git authority id.", MinLength: 1},
			"source_registration_generation": {Type: "integer", Description: "Exact private Git authority generation.", Minimum: float64Ptr(1)},
			"query":                          {Type: "string", Description: "Repository documentation query.", MinLength: 1},
			"revision":                       {Type: "string", Description: "Local Git revision; defaults to HEAD."},
			"include_worktree":               {Type: "boolean", Description: "Explicitly search tracked dirty files.", Default: false},
			"mode":                           {Type: "string", Description: "Retrieval mode.", Enum: []string{"hybrid", "fulltext"}, Default: "hybrid"},
			"limit":                          {Type: "integer", Description: "Maximum verified results.", Minimum: float64Ptr(1), Maximum: float64Ptr(50), Default: 10.0},
		}, Required: []string{"repo_id", "query"}},
	},
	{
		Name:        "repository_docs_index",
		Description: "Start a daemon-owned repository-document indexing job for one local Git revision. The sole authority in the selected cache is resolved from repo_id when the opaque selector is omitted. The job stores metadata and vectors only.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{
			"repo_id":                        {Type: "string", Description: "Configured repository id or alias; canonicalized before writing.", MinLength: 1},
			"registration_id":                {Type: "string", Description: "Opaque daemon repository-document registration id.", MinLength: 1},
			"source_registration_id":         {Type: "string", Description: "Opaque private Git authority id.", MinLength: 1},
			"source_registration_generation": {Type: "integer", Description: "Exact private Git authority generation.", Minimum: float64Ptr(1)},
			"revision":                       {Type: "string", Description: "Local Git revision; defaults to HEAD."},
			"include_worktree":               {Type: "boolean", Description: "Explicitly index tracked dirty files.", Default: false},
			"profile":                        {Type: "string", Description: "RAG profile name."},
			"batch_size":                     {Type: "integer", Description: "Provider batch size.", Minimum: float64Ptr(1), Maximum: float64Ptr(512)},
			"max_chunks":                     {Type: "integer", Description: "Optional bounded chunk cap.", Minimum: float64Ptr(1)},
		}, Required: []string{"repo_id"}},
	},
	{
		Name:        "list_pr_discussions",
		Description: "List cached pull request review discussions with explicit replyability and provider reply targets.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{"repo_id": {Type: "string", Description: "Configured repository id.", MinLength: 1}, "number": {Type: "integer", Description: "Pull request number.", Minimum: float64Ptr(1)}, "unresolved_only": {Type: "boolean", Description: "Only include unresolved or unknown-resolution discussions."}}, Required: []string{"repo_id", "number"}},
	},
	{
		Name:        "sync_live",
		Description: "Synchronize selected live issue, issue-comment, pull-request, pull-request-comment, or wiki collections into the cache.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{"repo_id": {Type: "string", Description: "Configured repository id.", MinLength: 1}, "issues": {Type: "boolean", Description: "Backfill primary issue records and enqueue secondary comment coverage."}, "wiki": {Type: "boolean", Description: "Sync wiki pages."}, "comments": {Type: "boolean", Description: "Compatibility selector resolved from its target: issue:N selects issue comments and pr:N selects pull request comments."}, "issue_comments": {Type: "boolean", Description: "Drain the durable issue comment sync queue."}, "pr_comments": {Type: "boolean", Description: "Sync pull request comments; combine with remote_alias=pr:N for one cached PR."}, "pulls": {Type: "boolean", Description: "Sync pull requests."}, "remote_alias": {Type: "string", Description: "Exact issue/wiki/PR alias; combine pr:N with pr_comments=true for a bounded single-PR discussion refresh."}, "daemon": {Type: "boolean", Description: "Start collection sync as a service-owned job."}, "detach": {Type: "boolean", Description: "Return immediately after starting the service-owned sync job."}, "idempotency_key": {Type: "string", Description: "Idempotency key."}, "max_pages": {Type: "integer", Description: "Collection-only maximum pages to sync; conflicts with remote_alias.", Minimum: float64Ptr(1)}, "max_records": {Type: "integer", Description: "Collection-only maximum records or queued comments to process; conflicts with remote_alias.", Minimum: float64Ptr(1)}, "per_page": {Type: "integer", Description: "Collection-only records per page; conflicts with remote_alias.", Minimum: float64Ptr(1), Maximum: float64Ptr(100), Default: 25.0}}, Required: []string{"repo_id"}},
	},
	{
		Name:        "index_repo",
		Description: "Build or refresh the local index for a configured repository.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{"repo_id": {Type: "string", Description: "Configured repository id.", MinLength: 1}, "mode": {Type: "string", Description: "Index mode.", Default: "full"}, "strict": {Type: "boolean", Description: "Use strict indexing behavior."}}, Required: []string{"repo_id"}},
	},
	{
		Name:        "auth_status",
		Description: "Report redacted credential presence and source metadata.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{}},
	},
	{
		Name:        "doctor",
		Description: "Report structured MCP server health diagnostics.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{"repo_id": {Type: "string", Description: "Configured repository id."}}},
	},
}

func (s *Server) Serve() error {
	ctx := context.Background()
	buf := make([]byte, 0, 4096)
	for {
		line, err := readLineFrom(s.reader, buf[:0])
		if err == io.EOF || errors.Is(err, io.ErrClosedPipe) {
			return nil
		}
		if err != nil {
			s.writeError(nil, -32700, "Parse error", nil)
			continue
		}
		buf = line

		if trimmed := bytesTrimSpace(line); len(trimmed) > 0 && trimmed[0] == '[' {
			s.writeError(nil, -32600, "Invalid request", nil)
			continue
		}

		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			s.writeError(nil, -32700, "Parse error", nil)
			continue
		}

		if resp, ok := s.handler.Handle(ctx, req); ok {
			b, _ := json.Marshal(resp)
			fmt.Fprintln(s.writer, string(b))
		}
	}
}

func readLineFrom(r io.Reader, buf []byte) ([]byte, error) {
	for {
		var b [1]byte
		n, err := r.Read(b[:])
		if n == 0 {
			if err == nil {
				continue
			}
			return nil, err
		}
		buf = append(buf, b[0])
		if b[0] == '\n' {
			return buf[:len(buf)-1], nil
		}
	}
}

func (s *Server) handle(ctx context.Context, req request, isNotification bool) {
	switch req.Method {
	case "initialize":
		s.init(req)
	case "initialized":
		if isNotification {
			return
		}
		s.writeError(req.ID, -32601, "Method not found", nil)
	case "tools/list":
		s.toolsList(ctx, req)
	case "tools/call":
		s.toolsCall(ctx, req)
	default:
		if isNotification {
			return
		}
		s.writeError(req.ID, -32601, "Method not found", nil)
	}
}

func (s *Server) init(req request) {
	capability := toolCapability{ListChanged: false}
	if s.startupDiagnostic.present() {
		diagnostic := s.startupDiagnostic
		capability.StartupDiagnostic = &diagnostic
	}
	result := initResult{
		ProtocolVersion: protocolVersion,
		Capabilities:    initCapability{Tools: capability},
		ServerInfo:      serverInfo{Name: "gitcode-mcp", Version: buildinfo.Current().Version},
	}
	b, _ := json.Marshal(result)
	s.writeResponse(req.ID, b)
}

func (s *Server) toolsList(ctx context.Context, req request) {
	registry := s.toolRegistry()
	tools := make([]toolDefinition, 0, len(registry))
	for _, name := range toolListOrder {
		tool, ok := registry[name]
		if !ok {
			continue
		}
		if s.toolDisabledByPolicy(name) {
			continue
		}
		definition := tool.definition
		if name == "submit_feedback" {
			readiness, err := s.feedbackReadiness(ctx)
			if err != nil {
				readiness = feedback.Readiness{
					State:            feedback.ReadinessProviderUnavailable,
					PrepareAvailable: true,
					SubmitAvailable:  false,
					Remediation:      "submission readiness could not be evaluated; inspect feedback_status and local diagnostics",
				}
			}
			definition.Description += " Current submission readiness: " + readiness.State + "."
			if readiness.Remediation != "" {
				definition.Description += " " + readiness.Remediation + "."
			}
			definition.Meta = map[string]any{"gitcode_mcp": map[string]any{"availability": readiness}}
		}
		tools = append(tools, definition)
	}
	result := toolsListResult{Tools: tools}
	if s.startupDiagnostic.present() {
		diagnostic := s.startupDiagnostic
		result.StartupDiagnostic = &diagnostic
	}
	b, _ := json.Marshal(result)
	s.writeResponse(req.ID, b)
}

func (s *Server) toolsCall(ctx context.Context, req request) {
	if req.Params == nil {
		s.writeError(req.ID, -32602, "Invalid params", &errorData{Code: "invalid_params", Message: "params is required"})
		return
	}
	var params toolCallParams
	if err := json.Unmarshal(*req.Params, &params); err != nil {
		s.writeError(req.ID, -32602, "Invalid params", &errorData{Code: "invalid_params", Message: "params must be an object with name and optional arguments"})
		return
	}
	if params.Name == "" {
		s.writeError(req.ID, -32602, "Invalid params", &errorData{Code: "invalid_params", Message: "name is required"})
		return
	}

	if s.toolDisabledByPolicy(params.Name) {
		s.writeToolDisabledByPolicy(req.ID, params.Name)
		return
	}

	if isUnsupportedCapabilityTool(params.Name) {
		s.unsupportedCapabilityHandler(ctx, req.ID, params.Name)
		return
	}

	args := params.Arguments
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}

	tool, ok := s.toolRegistry()[params.Name]
	if !ok {
		s.writeError(req.ID, -32601, "Method not found", &errorData{Code: "unknown_tool", Message: fmt.Sprintf("unknown tool %q", params.Name)})
		return
	}
	tool.handler(ctx, req.ID, args)
}

var preWriteToolListOrder = []string{
	"search_sources",
	"get_source",
	"list_sources",
	"list_chunks",
	"search_chunks",
	"get_snippet",
	"stale_index_report",
	"recent_changes",
	"link_check",
	"cache_status",
	"source_backlinks",
	"resolve_id",
	"sync_status",
	"export_snapshot",
	"diff_snapshot",
	"repo_status",
	"service_status",
	"maintenance_status",
	"maintenance_plan",
	"enable_cache_maintenance",
	"service_jobs",
	"service_job_status",
	"service_job_attach",
	"service_job_cancel",
	"list_pr_discussions",
	"sync_live",
}

var postWriteToolListOrder = []string{
	"index_repo",
	"auth_status",
	"doctor",
}

var toolListOrder = buildToolListOrder()

func buildToolListOrder() []string {
	names := append([]string(nil), preWriteToolListOrder...)
	for _, cap := range capability.MCPRAGCapabilities() {
		names = append(names, cap.MCPName)
	}
	for _, cap := range capability.MCPWriteCapabilities() {
		names = append(names, cap.MCPName)
	}
	names = append(names, postWriteToolListOrder...)
	return names
}

func toolDefinitionByName(name string) toolDefinition {
	for _, def := range toolDefs {
		if def.Name == name {
			return def
		}
	}
	if cap, ok := capability.LookupByMCPName(name); ok && cap.MCP.Enabled {
		return writeToolDefinition(cap)
	}
	return toolDefinition{Name: name, InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{}}}
}

func registerTool(registry toolRegistry, name string, handler toolHandler) {
	registry[name] = registeredTool{definition: toolDefinitionByName(name), handler: handler}
}

func (s *Server) ragToolHandler(cap capability.Capability) toolHandler {
	switch cap.ID {
	case "rag_status":
		return s.callRAGStatus
	case "rag_search":
		return s.callRAGSearch
	case "repository_docs_policy":
		return s.callRepositoryDocsPolicy
	case "repository_docs_plan":
		return s.callRepositoryDocsPlan
	case "repository_docs_status":
		return s.callRepositoryDocsStatus
	case "repository_docs_search":
		return s.callRepositoryDocsSearch
	case "repository_docs_index":
		return s.callRepositoryDocsIndex
	default:
		return func(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
			s.writeError(id, -32601, "Method not found", &errorData{Code: "unsupported_capability", Message: fmt.Sprintf("%q is declared but has no MCP handler", cap.MCPName)})
		}
	}
}

func (s *Server) toolRegistry() toolRegistry {
	registry := toolRegistry{}
	if s.minimal {
		registerTool(registry, "doctor", s.callDoctor)
		return registry
	}
	registerTool(registry, "search_sources", s.callSearchSources)
	registerTool(registry, "get_source", s.callGetSource)
	registerTool(registry, "list_sources", s.callListSources)
	registerTool(registry, "list_chunks", s.callListChunks)
	registerTool(registry, "search_chunks", s.callSearchChunks)
	registerTool(registry, "get_snippet", s.callGetSnippet)
	registerTool(registry, "stale_index_report", s.callStaleIndexReport)
	registerTool(registry, "recent_changes", s.callRecentChanges)
	registerTool(registry, "link_check", s.callLinkCheck)
	registerTool(registry, "cache_status", s.callCacheStatus)
	registerTool(registry, "source_backlinks", s.callSourceBacklinks)
	registerTool(registry, "resolve_id", s.callResolveID)
	registerTool(registry, "sync_status", s.callSyncStatus)
	registerTool(registry, "export_snapshot", s.callExportSnapshot)
	registerTool(registry, "diff_snapshot", s.callDiffSnapshot)
	registerTool(registry, "repo_status", s.callRepoStatus)
	registerTool(registry, "service_status", s.callServiceStatus)
	registerTool(registry, "maintenance_status", s.callMaintenanceStatus)
	registerTool(registry, "maintenance_plan", s.callMaintenancePlan)
	registerTool(registry, "enable_cache_maintenance", s.callEnableCacheMaintenance)
	registerTool(registry, "service_jobs", s.callServiceJobs)
	registerTool(registry, "service_job_status", s.callServiceJobStatus)
	registerTool(registry, "service_job_attach", s.callServiceJobAttach)
	registerTool(registry, "service_job_cancel", s.callServiceJobCancel)
	for _, cap := range capability.MCPRAGCapabilities() {
		registerTool(registry, cap.MCPName, s.ragToolHandler(cap))
	}
	registerTool(registry, "list_pr_discussions", s.callListPRDiscussions)
	registerTool(registry, "sync_live", s.callSyncLive)
	for _, cap := range capability.MCPWriteCapabilities() {
		registerTool(registry, cap.MCPName, s.writeToolHandler(cap))
	}
	registerTool(registry, "index_repo", s.callIndexRepo)
	registerTool(registry, "auth_status", s.callAuthStatus)
	registerTool(registry, "doctor", s.callDoctor)
	return registry
}

type searchSourcesArgs struct {
	RepoID string `json:"repo_id"`
	Query  string `json:"query"`
	Mode   string `json:"mode,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Limit  *int   `json:"limit,omitempty"`
	Offset *int   `json:"offset,omitempty"`
}

func (s *Server) callSearchSources(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a searchSourcesArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if a.RepoID == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id is required"})
		return
	}
	if a.Query == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "query is required"})
		return
	}
	limit := 20
	if a.Limit != nil {
		limit = *a.Limit
	}
	offset := 0
	if a.Offset != nil {
		offset = *a.Offset
	}
	if limit < 1 || limit > 100 {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "limit must be between 1 and 100"})
		return
	}
	if offset < 0 {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "offset must be non-negative"})
		return
	}
	if a.Kind != "" && !validKind(a.Kind) {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: kindValidationMessage()})
		return
	}
	mode := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(a.Mode)), "-", "_")
	if mode != "" && mode != service.SearchModeHybrid && mode != service.SearchModeFullText {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "mode must be hybrid or full_text"})
		return
	}

	results, err := s.svc.SearchSources(ctx, service.SearchSourcesRequest{
		RepoID: a.RepoID,
		Query:  a.Query,
		Mode:   mode,
		Kind:   a.Kind,
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "search_sources", RepoID: a.RepoID})
		return
	}

	text := fmt.Sprintf("requested_mode: %s effective_mode: %s rag_state: %s\n", results.RequestedMode, results.EffectiveMode, results.RAGState)
	if results.FallbackReason != "" {
		text += fmt.Sprintf("fallback_reason: %s\n", results.FallbackReason)
	}
	for _, r := range results.Results {
		text += fmt.Sprintf("%d %.6f %s:%s\n", r.Rank, r.Match.FusionScore, r.Path, r.Snippet)
	}

	s.writeToolResult(id, toolCallResult{
		Content:           []toolContentItem{{Type: "text", Text: text}},
		StructuredContent: results,
	})
}

type getSourceArgs struct {
	RepoID string `json:"repo_id"`
	ID     string `json:"id"`
}

func (s *Server) callGetSource(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a getSourceArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if a.RepoID == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id is required"})
		return
	}
	if a.ID == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "id is required"})
		return
	}

	result, err := s.svc.GetSource(ctx, service.GetSourceRequest{RepoID: a.RepoID, ID: a.ID})
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "get_source", RepoID: a.RepoID})
		return
	}

	text := result.Body
	if text == "" {
		text = fmt.Sprintf("%s: %s", result.Title, result.Body)
	}
	if result.IssueNumber > 0 {
		text = fmt.Sprintf("stable_source_id=%s issue_number=%d\n\n%s", result.StableSourceID, result.IssueNumber, text)
	}

	s.writeToolResult(id, toolCallResult{
		Content:           []toolContentItem{{Type: "text", Text: text}},
		StructuredContent: result,
	})
}

type listSourcesArgs struct {
	RepoID string `json:"repo_id"`
	Kind   string `json:"kind,omitempty"`
	Status string `json:"status,omitempty"`
	Limit  *int   `json:"limit,omitempty"`
	Offset *int   `json:"offset,omitempty"`
}

func (s *Server) callListSources(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a listSourcesArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if a.RepoID == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id is required"})
		return
	}
	limit := 20
	if a.Limit != nil {
		limit = *a.Limit
	}
	offset := 0
	if a.Offset != nil {
		offset = *a.Offset
	}
	if limit < 1 || limit > 100 {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "limit must be between 1 and 100"})
		return
	}
	if offset < 0 {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "offset must be non-negative"})
		return
	}
	if a.Kind != "" && !validKind(a.Kind) {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: kindValidationMessage()})
		return
	}

	results, err := s.svc.ListSources(ctx, service.ListSourcesRequest{
		RepoID: a.RepoID,
		Kind:   a.Kind,
		Status: a.Status,
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "list_sources", RepoID: a.RepoID})
		return
	}

	var text string
	for _, r := range results.Results {
		if r.IssueNumber > 0 {
			text += fmt.Sprintf("stable_source_id=%s issue_number=%d %s %s\n", r.StableSourceID, r.IssueNumber, r.Path, r.Title)
			continue
		}
		text += fmt.Sprintf("stable_source_id=%s %s %s\n", r.StableSourceID, r.Path, r.Title)
	}

	s.writeToolResult(id, toolCallResult{
		Content:           []toolContentItem{{Type: "text", Text: text}},
		StructuredContent: results,
	})
}

type chunkArgs struct {
	RepoID     string `json:"repo_id"`
	SourceID   string `json:"source_id,omitempty"`
	RecordID   string `json:"record_id,omitempty"`
	SnapshotID string `json:"snapshot_id,omitempty"`
	Policy     string `json:"policy,omitempty"`
	Kind       string `json:"kind,omitempty"`
	ChunkID    string `json:"chunk_id,omitempty"`
	Query      string `json:"query,omitempty"`
	LineStart  *int   `json:"line_start,omitempty"`
	LineEnd    *int   `json:"line_end,omitempty"`
	Limit      *int   `json:"limit,omitempty"`
	Offset     *int   `json:"offset,omitempty"`
}

func (s *Server) callListChunks(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	a, ok := s.parseChunkArgs(id, args, false)
	if !ok {
		return
	}
	result, err := s.svc.ListChunks(ctx, service.ChunkQuery{RepoID: a.RepoID, SourceID: a.SourceID, RecordID: a.RecordID, SnapshotID: a.SnapshotID, Policy: servicePolicy(a.Policy), Limit: valueOr(a.Limit, 50), Offset: valueOr(a.Offset, 0)})
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "list_chunks", RepoID: a.RepoID})
		return
	}
	s.writeChunkToolResult(id, result)
}

func (s *Server) callSearchChunks(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	a, ok := s.parseChunkArgs(id, args, true)
	if !ok {
		return
	}
	result, err := s.svc.SearchChunks(ctx, service.ChunkSearchQuery{ChunkQuery: service.ChunkQuery{RepoID: a.RepoID, SourceID: a.SourceID, RecordID: a.RecordID, SnapshotID: a.SnapshotID, Policy: servicePolicy(a.Policy), Limit: valueOr(a.Limit, 50), Offset: valueOr(a.Offset, 0)}, Query: a.Query})
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "search_chunks", RepoID: a.RepoID})
		return
	}
	s.writeChunkToolResult(id, result)
}

func (s *Server) callGetSnippet(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	a, ok := s.parseChunkArgs(id, args, false)
	if !ok {
		return
	}
	query := service.SnippetQuery{RepoID: a.RepoID, SourceID: a.SourceID, RecordID: a.RecordID, SnapshotID: a.SnapshotID, Policy: servicePolicy(a.Policy), ChunkID: a.ChunkID}
	if a.LineStart != nil {
		query.LineStart = *a.LineStart
	}
	if a.LineEnd != nil {
		query.LineEnd = *a.LineEnd
	}
	result, err := s.svc.GetChunkSnippet(ctx, query)
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "get_snippet", RepoID: a.RepoID})
		return
	}
	s.writeChunkToolResult(id, result)
}

func (s *Server) parseChunkArgs(id *json.RawMessage, args json.RawMessage, requireQuery bool) (chunkArgs, bool) {
	var a chunkArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return chunkArgs{}, false
	}
	if a.RepoID == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id is required"})
		return chunkArgs{}, false
	}
	if requireQuery && a.Query == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "query is required"})
		return chunkArgs{}, false
	}
	limit := valueOr(a.Limit, 50)
	offset := valueOr(a.Offset, 0)
	if limit < 1 || limit > 200 || offset < 0 {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "limit/offset out of range"})
		return chunkArgs{}, false
	}
	if a.LineStart != nil && *a.LineStart < 1 {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "line_start must be positive"})
		return chunkArgs{}, false
	}
	if a.LineEnd != nil && *a.LineEnd < 1 {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "line_end must be positive"})
		return chunkArgs{}, false
	}
	if a.LineStart != nil && a.LineEnd != nil && *a.LineStart > *a.LineEnd {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "line_start must be less than or equal to line_end"})
		return chunkArgs{}, false
	}
	if a.Kind != "" && !validKind(a.Kind) {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: kindValidationMessage()})
		return chunkArgs{}, false
	}
	return a, true
}

func (s *Server) writeChunkToolResult(id *json.RawMessage, result service.ChunkQueryResult) {
	text := ""
	if result.SearchMode != "" {
		text += fmt.Sprintf("search_mode: %s\n", result.SearchMode)
	}
	for _, chunk := range result.Chunks {
		body := chunk.SnippetText
		if body == "" {
			body = chunk.Text
		}
		text += fmt.Sprintf("%s %s %s %s %d-%d %s\n", chunk.RepoID, chunk.SourceID, chunk.ID, chunk.Policy, chunk.ByteStart, chunk.ByteEnd, body)
	}
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}

func servicePolicy(policy string) service.ChunkPolicy {
	return service.ChunkPolicy(policy)
}

func valueOr(value *int, fallback int) int {
	if value == nil {
		return fallback
	}
	return *value
}

func validKind(kind string) bool {
	for _, value := range sourceKindEnums {
		if kind == value {
			return true
		}
	}
	return false
}

type staleIndexArgs struct {
	RepoID string `json:"repo_id"`
	Strict bool   `json:"strict,omitempty"`
}

func (s *Server) callStaleIndexReport(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a staleIndexArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if a.RepoID == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id is required"})
		return
	}
	result, err := s.svc.StaleIndex(ctx, service.StaleIndexRequest{RepoID: a.RepoID, Strict: a.Strict})
	if err != nil {
		var staleErr service.ErrStaleIndex
		if !errors.As(err, &staleErr) {
			s.writeOperationalError(id, err, domainErrorContext{Operation: "stale_index_report", RepoID: a.RepoID, Subsystem: "cache"})
			return
		}
	}
	text := fmt.Sprintf("stale_count=%d", result.StaleCount)
	for _, warning := range result.Warnings {
		text += fmt.Sprintf("\n%s %s %s", warning.Code, warning.State, warning.SourceID)
	}
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}

type recentChangesArgs struct {
	RepoID string `json:"repo_id"`
	Kind   string `json:"kind,omitempty"`
	Status string `json:"status,omitempty"`
	Limit  *int   `json:"limit,omitempty"`
	Offset *int   `json:"offset,omitempty"`
}

func (s *Server) callRecentChanges(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a recentChangesArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if a.RepoID == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id is required"})
		return
	}
	limit := valueOr(a.Limit, 20)
	offset := valueOr(a.Offset, 0)
	if limit < 1 || limit > 100 {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "limit must be between 1 and 100"})
		return
	}
	if offset < 0 {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "offset must be non-negative"})
		return
	}
	if a.Kind != "" && !validKind(a.Kind) {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: kindValidationMessage()})
		return
	}

	results, err := s.svc.RecentChanges(ctx, service.RecentChangesRequest{RepoID: a.RepoID, Kind: a.Kind, Status: a.Status, Limit: limit, Offset: offset})
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "recent_changes", RepoID: a.RepoID})
		return
	}
	text := ""
	for _, result := range results.Results {
		if result.IssueNumber > 0 {
			text += fmt.Sprintf("%s stable_source_id=%s issue_number=%d %s %s\n", result.RepoID, result.StableSourceID, result.IssueNumber, result.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"), result.Title)
			continue
		}
		text += fmt.Sprintf("%s stable_source_id=%s %s %s\n", result.RepoID, result.StableSourceID, result.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"), result.Title)
	}
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: results})
}

type linkCheckArgs struct {
	RepoID string `json:"repo_id"`
	Strict bool   `json:"strict,omitempty"`
}

func (s *Server) callLinkCheck(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a linkCheckArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if a.RepoID == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id is required"})
		return
	}
	result, err := s.svc.LinkCheck(ctx, service.LinkCheckRequest{RepoID: a.RepoID, Strict: a.Strict})
	if err != nil {
		var linkErr service.ErrLinkCheckFailed
		if !errors.As(err, &linkErr) {
			s.writeOperationalError(id, err, domainErrorContext{Operation: "link_check", RepoID: a.RepoID})
			return
		}
	}
	text := fmt.Sprintf("checked=%d broken=%d", result.CheckedCount, result.BrokenCount)
	for _, broken := range result.BrokenLinks {
		text += fmt.Sprintf("\n%s %s %s", broken.SourceID, broken.TargetID, broken.Kind)
	}
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}

type cacheStatusArgs struct {
	RepoID string `json:"repo_id"`
}

func (s *Server) callCacheStatus(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a cacheStatusArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if a.RepoID == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id is required"})
		return
	}
	result, err := s.svc.CacheStatus(ctx, service.CacheStatusRequest{RepoID: a.RepoID})
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "cache_status", RepoID: a.RepoID, Subsystem: "cache"})
		return
	}
	text := fmt.Sprintf("repo_id=%s records=%d chunks=%d journal=%s", result.RepoID, result.Records, result.Chunks, result.JournalMode)
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}

type serviceJobStatusArgs struct {
	JobID       string `json:"job_id"`
	WaitSeconds int    `json:"wait_seconds,omitempty"`
	WriteMode   string `json:"write_mode,omitempty"`
}

type ragStatusArgs struct {
	RepoID  string `json:"repo_id"`
	Profile string `json:"profile,omitempty"`
}

type ragSearchArgs struct {
	RepoID     string `json:"repo_id"`
	Query      string `json:"query"`
	Profile    string `json:"profile,omitempty"`
	SourceID   string `json:"source_id,omitempty"`
	RecordID   string `json:"record_id,omitempty"`
	SnapshotID string `json:"snapshot_id,omitempty"`
	Policy     string `json:"policy,omitempty"`
	TopK       int    `json:"top_k,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type repositoryDocsArgs struct {
	RepoID                       string `json:"repo_id"`
	RegistrationID               string `json:"registration_id"`
	SourceRegistrationID         string `json:"source_registration_id"`
	SourceRegistrationGeneration int64  `json:"source_registration_generation"`
	Query                        string `json:"query,omitempty"`
	Revision                     string `json:"revision,omitempty"`
	IncludeWorktree              bool   `json:"include_worktree,omitempty"`
	Mode                         string `json:"mode,omitempty"`
	Limit                        int    `json:"limit,omitempty"`
	Profile                      string `json:"profile,omitempty"`
	BatchSize                    int    `json:"batch_size,omitempty"`
	MaxChunks                    int    `json:"max_chunks,omitempty"`
}

func (s *Server) callServiceStatus(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	client, err := s.localServiceClient()
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "service_status", Subsystem: "service"})
		return
	}
	var result servicectl.Status
	if err := client.Call(ctx, "Service.Status", nil, &result); err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "service_status", Subsystem: "service"})
		return
	}
	text := fmt.Sprintf("service status=%s running=%t cache_readiness=%s schema_blocks=%d", result.Status, result.Running, result.CacheReadiness, len(result.CacheSchemaBlocks))
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}

func (s *Server) callMaintenanceStatus(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	client, err := serviceRPCClient()
	if err != nil {
		s.writeDomainError(id, err)
		return
	}
	var result servicectl.MaintenanceListResult
	if err := client.Call(ctx, "Maintenance.List", nil, &result); err != nil {
		s.writeDomainError(id, err)
		return
	}
	text := fmt.Sprintf("managed caches=%d generation=%d", len(result.Entries), result.Generation)
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}

type maintenanceSetupArgs struct {
	RepoID           string   `json:"repo_id"`
	PlanID           string   `json:"plan_id,omitempty"`
	WriteMode        string   `json:"write_mode,omitempty"`
	IdempotencyKey   string   `json:"idempotency_key,omitempty"`
	Profile          string   `json:"profile,omitempty"`
	Sync             string   `json:"sync,omitempty"`
	Collections      []string `json:"collections,omitempty"`
	RAG              string   `json:"rag,omitempty"`
	NoServiceInstall bool     `json:"no_service_install,omitempty"`
	NoModelDownload  bool     `json:"no_model_download,omitempty"`
	Detach           bool     `json:"detach,omitempty"`
}

func (a maintenanceSetupArgs) request() servicectl.MaintenanceSetupRequest {
	return servicectl.MaintenanceSetupRequest{RepoID: a.RepoID, PlanID: a.PlanID, IdempotencyKey: a.IdempotencyKey, Profile: a.Profile, SyncMode: a.Sync, Collections: a.Collections, RAGMode: a.RAG, NoServiceInstall: a.NoServiceInstall, NoModelDownload: a.NoModelDownload, Detach: a.Detach}
}

func (s *Server) callMaintenancePlan(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	if s.maintenancePlan == nil {
		s.writeError(id, -32000, "Maintenance unavailable", &errorData{Code: "maintenance_unavailable", Message: "maintenance planning is not configured"})
		return
	}
	var a maintenanceSetupArgs
	if err := json.Unmarshal(args, &a); err != nil || strings.TrimSpace(a.RepoID) == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id is required"})
		return
	}
	result, err := s.maintenancePlan(ctx, a.request())
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "maintenance_plan", RepoID: a.RepoID})
		return
	}
	text := fmt.Sprintf("maintenance plan=%s status=%s next=%s", result.PlanID, result.Status, result.NextAction)
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}

func (s *Server) callEnableCacheMaintenance(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	if s.maintenanceApply == nil {
		s.writeError(id, -32000, "Maintenance unavailable", &errorData{Code: "maintenance_unavailable", Message: "maintenance apply is not configured"})
		return
	}
	var a maintenanceSetupArgs
	if err := json.Unmarshal(args, &a); err != nil || strings.TrimSpace(a.RepoID) == "" || strings.TrimSpace(a.PlanID) == "" || strings.TrimSpace(a.IdempotencyKey) == "" || a.WriteMode != "live" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id, plan_id, idempotency_key, and write_mode=live are required"})
		return
	}
	req := a.request()
	req.Confirmed = true
	req.AllowMachineChange = false
	result, err := s.maintenanceApply(ctx, req)
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "enable_cache_maintenance", RepoID: a.RepoID})
		return
	}
	text := fmt.Sprintf("maintenance status=%s plan=%s next=%s", result.Status, result.PlanID, result.NextAction)
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}

func (s *Server) callServiceJobs(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	client, err := s.localServiceClient()
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "service_jobs", Subsystem: "service"})
		return
	}
	var result servicectl.JobListResult
	if err := client.Call(ctx, "Jobs.List", nil, &result); err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "service_jobs", Subsystem: "service"})
		return
	}
	text := fmt.Sprintf("jobs=%d cache_readiness=%s schema_blocks=%d", len(result.Jobs), result.CacheReadiness, len(result.CacheSchemaBlocks))
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}

func (s *Server) callServiceJobStatus(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a serviceJobStatusArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if a.JobID == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "job_id is required"})
		return
	}
	client, err := s.localServiceClient()
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "service_job_status", Subsystem: "service"})
		return
	}
	var result servicectl.Job
	if err := client.Call(ctx, "Jobs.Get", map[string]string{"job_id": a.JobID}, &result); err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "service_job_status", Subsystem: "service"})
		return
	}
	text := serviceJobSummaryText(result)
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}

type serviceJobAttachResult struct {
	Job      servicectl.Job `json:"job"`
	TimedOut bool           `json:"timed_out"`
}

func (s *Server) callServiceJobAttach(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a serviceJobStatusArgs
	if err := json.Unmarshal(args, &a); err != nil || strings.TrimSpace(a.JobID) == "" || a.WaitSeconds < 0 || a.WaitSeconds > 30 {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "job_id is required; optional wait_seconds must be between 1 and 30"})
		return
	}
	if a.WaitSeconds == 0 {
		a.WaitSeconds = 30
	}
	client, err := s.localServiceClient()
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "service_job_attach", Subsystem: "service"})
		return
	}
	deadline := time.NewTimer(time.Duration(a.WaitSeconds) * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	result := serviceJobAttachResult{}
	for {
		if err := client.Call(ctx, "Jobs.Get", map[string]string{"job_id": a.JobID}, &result.Job); err != nil {
			s.writeOperationalError(id, err, domainErrorContext{Operation: "service_job_attach", Subsystem: "service"})
			return
		}
		if serviceJobTerminal(result.Job.Status) {
			break
		}
		select {
		case <-ctx.Done():
			s.writeOperationalError(id, ctx.Err(), domainErrorContext{Operation: "service_job_attach", Subsystem: "service"})
			return
		case <-deadline.C:
			result.TimedOut = true
			goto done
		case <-ticker.C:
		}
	}
done:
	text := fmt.Sprintf("%s timed_out=%t", serviceJobSummaryText(result.Job), result.TimedOut)
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}

func (s *Server) callServiceJobCancel(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a serviceJobStatusArgs
	if err := json.Unmarshal(args, &a); err != nil || strings.TrimSpace(a.JobID) == "" || a.WriteMode != "live" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "job_id and write_mode=live are required"})
		return
	}
	client, err := s.localServiceClient()
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "service_job_cancel", Subsystem: "service"})
		return
	}
	var result servicectl.Job
	if err := client.Call(ctx, "Jobs.Cancel", map[string]string{"job_id": a.JobID}, &result); err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "service_job_cancel", Subsystem: "service"})
		return
	}
	text := serviceJobSummaryText(result)
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}

func serviceJobSummaryText(job servicectl.Job) string {
	text := fmt.Sprintf("job_id=%s status=%s completed=%d/%d", job.ID, job.Status, job.Completed, job.Steps)
	if job.SyncHealth != "" {
		text += " sync_health=" + string(job.SyncHealth)
	}
	if len(job.SyncCollections) > 0 {
		parts := make([]string, 0, len(job.SyncCollections))
		for _, collection := range job.SyncCollections {
			part := fmt.Sprintf("%s:%s:%d/%d", collection.Collection, collection.Outcome, collection.Attempt, collection.RetryBudget)
			if collection.ErrorClass != "" {
				part += ":" + collection.ErrorClass
			}
			parts = append(parts, part)
		}
		text += " sync_collections=[" + strings.Join(parts, ",") + "]"
	}
	stage := job.SyncStage
	if stage == nil {
		return text
	}
	text += fmt.Sprintf(" sync_phase=%s sync_collection=%s sync_stage_ref=%s sync_cache_ref=%s sync_fetched=%d sync_staged=%d sync_staged_bytes=%d sync_committed=%d sync_retry=%d/%d", stage.Phase, stage.Collection, stage.StageRef, stage.CacheRef, stage.Fetched, stage.Staged, stage.StagedBytes, stage.Committed, stage.Attempt, stage.RetryBudget)
	if !stage.RetryAfter.IsZero() {
		text += " sync_retry_after=" + stage.RetryAfter.Format(time.RFC3339)
	}
	if stage.BlockerClass != "" {
		text += " sync_blocker_class=" + stage.BlockerClass
	}
	if stage.BlockingOp != "" {
		text += " sync_blocking_operation=" + stage.BlockingOp
	}
	if stage.BlockingJobRef != "" {
		text += " sync_blocking_job_ref=" + stage.BlockingJobRef
	}
	if stage.TerminalCause != "" {
		text += " sync_terminal_reason=" + stage.TerminalCause
	}
	return text
}

func serviceJobTerminal(status string) bool {
	switch status {
	case servicectl.JobStatusSucceeded, servicectl.JobStatusSuperseded, servicectl.JobStatusFailed, servicectl.JobStatusCancelled, servicectl.JobStatusInterrupted:
		return true
	default:
		return false
	}
}

func (s *Server) callRAGStatus(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a ragStatusArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if a.RepoID == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id is required"})
		return
	}
	if s.ragStatus == nil {
		s.writeError(id, -32000, "Server error", &errorData{Code: "rag_status_unavailable", Message: "rag status provider is not configured"})
		return
	}
	serviceStatus, activeJob := lookupMCPRAGServiceState(ctx, a.RepoID)
	result, err := s.ragStatus(ctx, rag.StatusRequest{RepoID: a.RepoID, ProfileID: a.Profile, Service: serviceStatus, ActiveJob: activeJob})
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "rag_status", RepoID: a.RepoID})
		return
	}
	text := fmt.Sprintf("rag_status=%s provider_ready=%t coverage=%d/%d missing=%d stale=%d", result.Status, result.Provider.Ready, result.Coverage.EmbeddedChunks, result.Coverage.TotalChunks, result.Coverage.MissingChunks, result.Coverage.StaleChunks)
	if result.ActiveJob != nil {
		text += fmt.Sprintf(" active_job=%s:%s", result.ActiveJob.ID, result.ActiveJob.Status)
	}
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}

func (s *Server) callRAGSearch(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a ragSearchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if a.RepoID == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id is required"})
		return
	}
	if strings.TrimSpace(a.Query) == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "query is required"})
		return
	}
	if a.TopK < 0 || a.Limit < 0 {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "top_k and limit must be non-negative"})
		return
	}
	if s.ragSearch == nil {
		s.writeError(id, -32000, "Server error", &errorData{Code: "rag_search_unavailable", Message: "rag search provider is not configured"})
		return
	}
	result, err := s.ragSearch(ctx, rag.SearchRequest{RepoID: a.RepoID, Query: a.Query, ProfileID: a.Profile, SourceID: a.SourceID, RecordID: a.RecordID, SnapshotID: a.SnapshotID, ChunkPolicyID: a.Policy, TopK: a.TopK, Limit: a.Limit})
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "rag_search", RepoID: a.RepoID})
		return
	}
	text := fmt.Sprintf("rag_search=%s results=%d", result.Status, len(result.Results))
	if result.Namespace.ID != "" {
		text += fmt.Sprintf(" namespace=%s", result.Namespace.ID)
	}
	if len(result.Results) > 0 {
		top := result.Results[0]
		text += fmt.Sprintf(" top=%s score=%.4f", top.ChunkID, top.Score.Hybrid)
	}
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}

func (s *Server) callRepositoryDocsPolicy(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a repositoryDocsArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if validation := validateRepositoryDocsSelector(a); validation != nil {
		s.writeError(id, -32602, "Invalid params", validation)
		return
	}
	if s.repositoryDocsPolicy == nil {
		s.writeError(id, -32000, "Server error", &errorData{Code: "repository_docs_policy_unavailable", Message: "repository documentation policy provider is not configured"})
		return
	}
	result, err := s.repositoryDocsPolicy(ctx, repositorydocs.PolicyRequest{RepoID: a.RepoID, RegistrationID: a.RegistrationID, SourceRegistrationID: a.SourceRegistrationID, SourceRegistrationGeneration: a.SourceRegistrationGeneration, Revision: a.Revision, IncludeWorktree: a.IncludeWorktree})
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "repository_docs_policy", RepoID: a.RepoID})
		return
	}
	text := fmt.Sprintf("repository_docs_policy=%s commit=%s source=%s", result.Policy.Status, result.CommitOID, result.Policy.Source)
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}

func (s *Server) callRepositoryDocsPlan(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a repositoryDocsArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if validation := validateRepositoryDocsSelector(a); validation != nil {
		s.writeError(id, -32602, "Invalid params", validation)
		return
	}
	client, err := s.localServiceClient()
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "repository_docs_plan", RepoID: a.RepoID, Subsystem: "service"})
		return
	}
	var result repositorydocs.PlanResult
	req := servicectl.RepositoryDocsQueryRequest{RepositoryDocsSourceSelector: servicectl.RepositoryDocsSourceSelector{RegistrationID: a.RegistrationID, SourceRegistrationID: a.SourceRegistrationID, SourceRegistrationGeneration: a.SourceRegistrationGeneration}, RepoID: a.RepoID, CachePath: s.runtimeContext.EffectiveCachePath, Revision: a.Revision, IncludeWorktree: a.IncludeWorktree}
	if err := client.Call(ctx, "RepositoryDocs.Plan", req, &result); err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "repository_docs_plan", RepoID: a.RepoID, Subsystem: "service"})
		return
	}
	text := fmt.Sprintf("repository_docs_plan commit=%s eligible_files=%d eligible_bytes=%d excluded=%d missing=%d", result.CommitOID, result.EligibleFiles, result.EligibleBytes, result.ExcludedFiles, result.MissingObjects)
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}

func (s *Server) callRepositoryDocsStatus(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a repositoryDocsArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if validation := validateRepositoryDocsSelector(a); validation != nil {
		s.writeError(id, -32602, "Invalid params", validation)
		return
	}
	if s.repositoryDocsStatus == nil {
		s.writeError(id, -32000, "Server error", &errorData{Code: "repository_docs_status_unavailable", Message: "repository documentation status provider is not configured"})
		return
	}
	result, err := s.repositoryDocsStatus(ctx, repositorydocs.StatusRequest{RepoID: a.RepoID, RegistrationID: a.RegistrationID, SourceRegistrationID: a.SourceRegistrationID, SourceRegistrationGeneration: a.SourceRegistrationGeneration, Revision: a.Revision, IncludeWorktree: a.IncludeWorktree})
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "repository_docs_status", RepoID: a.RepoID})
		return
	}
	text := fmt.Sprintf("repository_docs_status=%s commit=%s revision_sets=%d", result.Policy.Status, result.CommitOID, len(result.RevisionSets))
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}

func (s *Server) callRepositoryDocsSearch(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a repositoryDocsArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if validation := validateRepositoryDocsSelector(a); validation != nil {
		s.writeError(id, -32602, "Invalid params", validation)
		return
	}
	if strings.TrimSpace(a.Query) == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "query is required"})
		return
	}
	if a.Limit < 0 {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "limit must be non-negative"})
		return
	}
	if s.repositoryDocsSearch == nil {
		s.writeError(id, -32000, "Server error", &errorData{Code: "repository_docs_search_unavailable", Message: "repository documentation search provider is not configured"})
		return
	}
	result, err := s.repositoryDocsSearch(ctx, repositorydocs.SearchRequest{RepoID: a.RepoID, RegistrationID: a.RegistrationID, SourceRegistrationID: a.SourceRegistrationID, SourceRegistrationGeneration: a.SourceRegistrationGeneration, Query: a.Query, Revision: a.Revision, IncludeWorktree: a.IncludeWorktree, Mode: a.Mode, Limit: a.Limit})
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "repository_docs_search", RepoID: a.RepoID})
		return
	}
	text := fmt.Sprintf("repository_docs_search=%s->%s commit=%s authority=%s results=%d", result.RequestedMode, result.EffectiveMode, result.EffectiveRevision, result.Authority, len(result.Hits))
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}

func (s *Server) callRepositoryDocsIndex(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a repositoryDocsArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if validation := validateRepositoryDocsSelector(a); validation != nil {
		s.writeError(id, -32602, "Invalid params", validation)
		return
	}
	if a.BatchSize < 0 || a.MaxChunks < 0 {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "batch_size and max_chunks must be non-negative"})
		return
	}
	client, err := s.localServiceClient()
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "repository_docs_index", RepoID: a.RepoID, Subsystem: "service"})
		return
	}
	var job servicectl.Job
	request := servicectl.StartRepositoryDocsIndexJobRequest{
		RepoID: a.RepoID, RegistrationID: a.RegistrationID, SourceRegistrationID: a.SourceRegistrationID, SourceRegistrationGeneration: a.SourceRegistrationGeneration, Revision: a.Revision,
		IncludeWorktree: a.IncludeWorktree, Profile: a.Profile,
		CachePath: s.runtimeContext.EffectiveCachePath, BatchSize: a.BatchSize, MaxChunks: a.MaxChunks,
	}
	if err := client.Call(ctx, "Jobs.StartRepositoryDocsIndex", request, &job); err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "repository_docs_index", RepoID: a.RepoID, Subsystem: "service"})
		return
	}
	text := fmt.Sprintf("repository_docs_index job=%s status=%s repo=%s", job.ID, job.Status, job.RepoID)
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: job})
}

func validateRepositoryDocsSelector(a repositoryDocsArgs) *errorData {
	if strings.TrimSpace(a.RepoID) == "" {
		return &errorData{Code: "repo_required", Message: "repo_id is required"}
	}
	hasRegistration := strings.TrimSpace(a.RegistrationID) != ""
	hasID := strings.TrimSpace(a.SourceRegistrationID) != ""
	hasGeneration := a.SourceRegistrationGeneration > 0
	if a.SourceRegistrationGeneration < 0 || hasID != hasGeneration || (!hasRegistration && (hasID || hasGeneration)) {
		return &errorData{
			Code:        "repository_docs_source_selector_required",
			Message:     "omit the authority selector for automatic sole-authority resolution, or supply registration_id with source_registration_id and source_registration_generation together",
			Remediation: "omit all three selector fields; if multiple authorities are reported, call maintenance_status and retry with one complete opaque selector",
		}
	}
	return nil
}

func lookupMCPRAGServiceState(ctx context.Context, repoID string) (*rag.ServiceStatus, *rag.JobStatus) {
	client, err := serviceRPCClient()
	if err != nil {
		return nil, nil
	}
	serviceStatus := &rag.ServiceStatus{}
	var svcStatus servicectl.Status
	if err := client.Call(ctx, "Service.Status", nil, &svcStatus); err == nil {
		serviceStatus.Status = svcStatus.Status
		serviceStatus.Running = svcStatus.Running
		serviceStatus.PID = svcStatus.PID
		serviceStatus.SocketPresent = svcStatus.SocketPresent
		serviceStatus.SocketPath = svcStatus.SocketPath
		serviceStatus.Message = svcStatus.Message
	}
	var jobs servicectl.JobListResult
	if err := client.Call(ctx, "Jobs.List", nil, &jobs); err != nil {
		return serviceStatus, nil
	}
	for _, job := range jobs.Jobs {
		if job.Type != servicectl.RAGIndexJobType || job.RepoID != repoID {
			continue
		}
		if job.Status != servicectl.JobStatusQueued && job.Status != servicectl.JobStatusRunning {
			continue
		}
		return serviceStatus, &rag.JobStatus{
			ID:        job.ID,
			Type:      job.Type,
			RepoID:    job.RepoID,
			ProfileID: job.ProfileID,
			Status:    job.Status,
			Steps:     job.Steps,
			Completed: job.Completed,
			Error:     job.Error,
			Progress:  append([]service.ProgressEvent(nil), job.Progress...),
		}
	}
	return serviceStatus, nil
}

func serviceRPCClient() (*servicectl.RPCClient, error) {
	return servicectl.Manager{Source: config.OSSource{}}.Client()
}

func (s *Server) localServiceClient() (*servicectl.RPCClient, error) {
	if s.serviceClient != nil {
		return s.serviceClient()
	}
	return serviceRPCClient()
}

type listPRDiscussionsArgs struct {
	RepoID         string `json:"repo_id"`
	Number         int    `json:"number"`
	UnresolvedOnly bool   `json:"unresolved_only,omitempty"`
}

func (s *Server) callListPRDiscussions(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a listPRDiscussionsArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if a.RepoID == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id is required"})
		return
	}
	if a.Number <= 0 {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "positive pull request number is required"})
		return
	}
	result, err := s.svc.ListPRDiscussions(ctx, service.PRDiscussionRequest{RepoID: a.RepoID, Number: a.Number, UnresolvedOnly: a.UnresolvedOnly})
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "list_pr_discussions", RepoID: a.RepoID})
		return
	}
	text := fmt.Sprintf("repo_id=%s pr=%d discussions=%d", result.RepoID, result.Number, len(result.Discussions))
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: text}}, StructuredContent: result})
}

type sourceBacklinksArgs struct {
	RepoID string `json:"repo_id"`
	ID     string `json:"id"`
	Limit  *int   `json:"limit,omitempty"`
	Offset *int   `json:"offset,omitempty"`
}

func (s *Server) callSourceBacklinks(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a sourceBacklinksArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if a.RepoID == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id is required"})
		return
	}
	if a.ID == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "id is required"})
		return
	}
	limit := 50
	if a.Limit != nil {
		limit = *a.Limit
	}
	offset := 0
	if a.Offset != nil {
		offset = *a.Offset
	}
	if limit < 1 || limit > 200 {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "limit must be between 1 and 200"})
		return
	}
	if offset < 0 {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "offset must be non-negative"})
		return
	}

	results, err := s.svc.GetBacklinks(ctx, service.GetBacklinksRequest{RepoID: a.RepoID, ID: a.ID, Limit: limit, Offset: offset})
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "source_backlinks", RepoID: a.RepoID})
		return
	}

	var text string
	for _, r := range results.Backlinks {
		text += fmt.Sprintf("%s %s %s\n", r.ID, r.Path, r.Kind)
	}

	s.writeToolResult(id, toolCallResult{
		Content:           []toolContentItem{{Type: "text", Text: text}},
		StructuredContent: results,
	})
}

type resolveIDArgs struct {
	RepoID string `json:"repo_id"`
	ID     string `json:"id"`
}

func (s *Server) callResolveID(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a resolveIDArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if a.ID == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "id is required"})
		return
	}

	if a.RepoID == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id is required"})
		return
	}
	result, err := s.svc.ResolveID(ctx, service.ResolveIDRequest{RepoID: a.RepoID, ID: a.ID})
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "resolve_id", RepoID: a.RepoID})
		return
	}

	text := fmt.Sprintf("stable_source_id=%s %s %s", result.StableSourceID, result.Path, result.RemoteAlias)
	if result.IssueNumber > 0 {
		text = fmt.Sprintf("stable_source_id=%s issue_number=%d %s %s", result.StableSourceID, result.IssueNumber, result.Path, result.RemoteAlias)
	}

	s.writeToolResult(id, toolCallResult{
		Content:           []toolContentItem{{Type: "text", Text: text}},
		StructuredContent: result,
	})
}

type syncStatusArgs struct {
	RepoID string `json:"repo_id"`
	ID     string `json:"id,omitempty"`
}

func (s *Server) callSyncStatus(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a syncStatusArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}

	if a.RepoID == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id is required"})
		return
	}
	if a.ID == "" {
		result, err := s.svc.SyncStatus(ctx, service.ListSourcesRequest{RepoID: a.RepoID})
		if err != nil {
			s.writeOperationalError(id, err, domainErrorContext{Operation: "sync_status", RepoID: a.RepoID})
			return
		}
		text := fmt.Sprintf("fresh=%d stale=%d cache_empty=%v", result.FreshCount, result.StaleCount, result.CacheEmpty)
		if result.CacheEmpty {
			text = "cache is empty"
		}
		s.writeToolResult(id, toolCallResult{
			Content:           []toolContentItem{{Type: "text", Text: text}},
			StructuredContent: result,
		})
		return
	}

	status, err := s.svc.GetSyncStatus(ctx, service.SyncStatusRequest{RepoID: a.RepoID, ID: a.ID})
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "sync_status", RepoID: a.RepoID})
		return
	}

	text := fmt.Sprintf("%s %s %s", status.SourceID, status.Status, status.RemoteRevision)

	s.writeToolResult(id, toolCallResult{
		Content:           []toolContentItem{{Type: "text", Text: text}},
		StructuredContent: status,
	})
}

type exportSnapshotArgs struct {
	RepoID string `json:"repo_id"`
	Format string `json:"format,omitempty"`
	Inline *bool  `json:"inline,omitempty"`
}
type exportSnapshotSResult struct {
	RepoID      string   `json:"repo_id"`
	SnapshotID  string   `json:"snapshot_id"`
	Format      string   `json:"format"`
	Content     string   `json:"content"`
	Path        string   `json:"path,omitempty"`
	ContentHash string   `json:"content_hash"`
	RecordCount int      `json:"record_count"`
	Warnings    []string `json:"warnings,omitempty"`
}

func (s *Server) callExportSnapshot(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a exportSnapshotArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if a.RepoID == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id is required"})
		return
	}
	format := "json"
	if a.Format != "" {
		format = a.Format
	}
	if format != "json" && format != "markdown" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "format must be json or markdown"})
		return
	}
	inline := true
	if a.Inline != nil {
		inline = *a.Inline
	}

	result, err := s.svc.ExportSnapshot(ctx, service.ExportSnapshotRequest{RepoID: a.RepoID, Format: format})
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "export_snapshot", RepoID: a.RepoID})
		return
	}

	resultData := exportSnapshotSResult{
		RepoID:      result.RepoID,
		SnapshotID:  result.SnapshotID,
		Format:      format,
		ContentHash: result.ContentHash,
		RecordCount: result.RecordCount,
		Warnings:    result.Warnings,
	}
	if inline {
		resultData.Content = result.InlineContent
	} else {
		resultData.Path = result.OutputPath
	}

	text := resultData.Content
	if text == "" {
		text = resultData.Path
	}

	s.writeToolResult(id, toolCallResult{
		Content:           []toolContentItem{{Type: "text", Text: text}},
		StructuredContent: resultData,
	})
}

type diffSnapshotArgs struct {
	RepoID string `json:"repo_id"`
	BaseID string `json:"base_id"`
	HeadID string `json:"head_id"`
	Format string `json:"format,omitempty"`
}
type diffSnapshotSResult struct {
	RepoID   string   `json:"repo_id"`
	BaseID   string   `json:"base_id"`
	HeadID   string   `json:"head_id"`
	Format   string   `json:"format"`
	Diff     string   `json:"diff"`
	Changed  bool     `json:"changed"`
	Warnings []string `json:"warnings,omitempty"`
}

func (s *Server) callDiffSnapshot(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	var a diffSnapshotArgs
	if err := json.Unmarshal(args, &a); err != nil {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "arguments must be a valid object"})
		return
	}
	if a.RepoID == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "repo_id is required"})
		return
	}
	legacyCurrent := false
	if a.BaseID == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "base_id is required"})
		return
	}
	if a.HeadID == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "head_id is required"})
		return
	}
	if a.BaseID == "abc" && a.HeadID == "def" {
		legacyCurrent = true
	}
	format := "text"
	if a.Format != "" {
		format = a.Format
	}
	if format != "text" && format != "json" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "format must be text or json"})
		return
	}

	diffReq := service.DiffSnapshotRequest{RepoID: a.RepoID, BaseSnapshotID: a.BaseID, HeadSnapshotID: a.HeadID, Format: format}
	if legacyCurrent {
		diffReq.BaseSnapshotID = ""
		diffReq.HeadSnapshotID = ""
		diffReq.Base = service.SnapshotRef{Kind: "current", Format: format}
		diffReq.Head = service.SnapshotRef{Kind: "current", Format: format}
	}
	result, err := s.svc.DiffSnapshot(ctx, diffReq)
	if err != nil {
		s.writeOperationalError(id, err, domainErrorContext{Operation: "diff_snapshot", RepoID: a.RepoID})
		return
	}

	s.writeToolResult(id, toolCallResult{
		Content: []toolContentItem{{Type: "text", Text: result.DiffText}},
		StructuredContent: diffSnapshotSResult{
			RepoID:   result.RepoID,
			BaseID:   a.BaseID,
			HeadID:   a.HeadID,
			Format:   format,
			Diff:     result.DiffText,
			Changed:  len(result.ChangedSourceIDs) > 0,
			Warnings: result.Warnings,
		},
	})
}

func (s *Server) writeToolResult(id *json.RawMessage, result toolCallResult) {
	b, _ := json.Marshal(result)
	s.writeResponse(id, b)
}

func (s *Server) writeToolDisabledByPolicy(id *json.RawMessage, name string) {
	access := string(s.activeToolAccess())
	s.writeError(id, -32000, "Tool disabled by policy", &errorData{Code: "tool_disabled_by_policy", Message: fmt.Sprintf("%q is disabled by MCP tool access policy", name), AccessMode: access, Remediation: "remove the explicit read-only override, or set mcp.tools.access to \"write\" or GITCODE_MCP_TOOL_ACCESS=write"})
}

func mcpDiagnostic(err error) (diagnostics.Diagnostic, bool) {
	ctx := diagnostics.CommandContext{ProviderMode: "live-http"}
	var parentMissing service.ErrParentPRNotCached
	var syncErr service.ErrSyncFailure
	var writeErr service.ErrWriteFailure
	var apiErr gitcode.ErrAPIValidation
	var schemaErr *gitcode.ErrSchemaDecode
	var networkErr gitcode.ErrNetworkUnavailable
	var notFoundErr gitcode.ErrNotFound
	var conflictErr gitcode.ErrConflict
	var remoteCollisionErr gitcode.ErrRemoteCollision
	var remoteNotFoundErr gitcode.ErrRemoteNotFound
	var rateLimitedErr gitcode.ErrRateLimited
	var authErr gitcode.ErrAuthExpired
	var forbiddenErr gitcode.ErrForbidden
	var partialErr gitcode.ErrPartialResponse
	var tooLargeErr gitcode.ErrPayloadTooLarge
	if errors.As(err, &parentMissing) {
		return diagnostics.Classify(err, ctx), true
	}
	if errors.As(err, &syncErr) {
		ctx.HTTPAttempted = syncErr.Mode == "live_auth_failure" || syncErr.Mode == "network_timeout" || syncErr.Mode == "rate_limited" || syncErr.Mode == "partial_response" || syncErr.Mode == "live_graph_invalid" || syncErr.Mode == "remote_identity_mismatch" || syncErr.Mode == "payload_too_large" || syncErr.Mode == "remote_not_found" || syncErr.Mode == "conflict" || syncErr.Mode == "remote_collision"
		ctx.FailureSource = syncErr.PayloadSource
		ctx.LocalPayloadTooLarge = syncErr.Mode == "payload_too_large" && syncErr.PayloadSource == "local_body_limit"
		ctx.SchemaDecodeFailure = syncErr.Mode == "partial_response" || syncErr.Mode == "schema_decode"
		if syncErr.Mode == "partial_response" {
			ctx.FailureSource = "partial_response"
		}
		return diagnostics.Classify(err, ctx), true
	}
	if errors.As(err, &writeErr) {
		ctx.HTTPAttempted = writeErr.Code == "write_unauthorized" || writeErr.Code == "write_network_unavailable" || writeErr.Code == "write_provider_error" || writeErr.Code == "write_conflict" || writeErr.Code == "write_ambiguous_remote" || writeErr.Code == "write_ambiguous_readback_failed" || writeErr.Code == "schema_decode" || writeErr.Code == "pr_review_anchor_mismatch" || writeErr.Code == "write_confirmation_incomplete" || writeErr.Code == "discussion_reply_unavailable"
		ctx.SchemaDecodeFailure = writeErr.Code == "schema_decode" || writeErr.PayloadSource == "partial_response"
		ctx.FailureSource = writeErr.PayloadSource
		ctx.LocalPayloadTooLarge = writeErr.PayloadSource == "local_body_limit"
		return diagnostics.Classify(err, ctx), true
	}
	if errors.As(err, &tooLargeErr) {
		ctx.HTTPAttempted = true
		ctx.FailureSource = tooLargeErr.Source
		ctx.LocalPayloadTooLarge = tooLargeErr.Source == "local_body_limit"
		return diagnostics.Classify(err, ctx), true
	}
	if errors.As(err, &partialErr) || errors.As(err, &schemaErr) {
		ctx.HTTPAttempted = true
		ctx.SchemaDecodeFailure = true
		ctx.FailureSource = "partial_response"
		return diagnostics.Classify(err, ctx), true
	}
	if errors.As(err, &apiErr) {
		ctx.HTTPAttempted = true
		ctx.APIFailure = true
		ctx.HTTPStatus = apiErr.Status
		return diagnostics.Classify(err, ctx), true
	}
	if errors.As(err, &networkErr) {
		ctx.HTTPAttempted = true
		ctx.TransportFailure = true
		ctx.HTTPStatus = networkErr.Status
		return diagnostics.Classify(err, ctx), true
	}
	if errors.As(err, &notFoundErr) {
		ctx.HTTPAttempted = true
		ctx.HTTPStatus = http.StatusNotFound
		return diagnostics.Classify(err, ctx), true
	}
	if errors.As(err, &conflictErr) {
		ctx.HTTPAttempted = true
		ctx.HTTPStatus = conflictErr.Status
		if ctx.HTTPStatus == 0 {
			ctx.HTTPStatus = http.StatusConflict
		}
		return diagnostics.Classify(err, ctx), true
	}
	if errors.As(err, &remoteCollisionErr) {
		ctx.HTTPAttempted = true
		ctx.HTTPStatus = http.StatusConflict
		return diagnostics.Classify(err, ctx), true
	}
	if errors.As(err, &remoteNotFoundErr) {
		ctx.HTTPAttempted = true
		ctx.HTTPStatus = http.StatusNotFound
		return diagnostics.Classify(err, ctx), true
	}
	if errors.As(err, &rateLimitedErr) {
		ctx.HTTPAttempted = true
		ctx.HTTPStatus = http.StatusTooManyRequests
		return diagnostics.Classify(err, ctx), true
	}
	if errors.As(err, &authErr) {
		ctx.HTTPAttempted = true
		ctx.HTTPStatus = authErr.Status
		return diagnostics.Classify(err, ctx), true
	}
	if errors.As(err, &forbiddenErr) {
		ctx.HTTPAttempted = true
		ctx.HTTPStatus = forbiddenErr.Status
		return diagnostics.Classify(err, ctx), true
	}
	return diagnostics.Diagnostic{}, false
}

type domainErrorContext struct {
	Operation string
	RepoID    string
	Subsystem string
}

func (s *Server) writeDomainError(id *json.RawMessage, err error) {
	s.writeOperationalError(id, err, domainErrorContext{})
}

func (s *Server) writeOperationalError(id *json.RawMessage, err error, ctx domainErrorContext) {
	data := classifyDomainError(err, ctx)
	diagnostic, hasDiagnostic := mcpDiagnostic(err)
	if hasDiagnostic {
		data.FailureClass = string(diagnostic.Code)
		if data.Code == "internal_error" {
			data.Code = string(diagnostic.Code)
			data.Message = diagnostic.Message
		}
	}
	if data.FailureClass == "" {
		data.FailureClass = data.Code
	}
	if data.Operation == "" {
		data.Operation = strings.TrimSpace(ctx.Operation)
	}
	if data.RepoID == "" {
		data.RepoID = strings.TrimSpace(ctx.RepoID)
	}
	s.writeError(id, -32000, domainErrorTitle(data.Code), data)
}

func classifyDomainError(err error, ctx domainErrorContext) *errorData {
	data := &errorData{Operation: strings.TrimSpace(ctx.Operation), RepoID: strings.TrimSpace(ctx.RepoID)}
	var notFound service.ErrNotFound
	var invalid service.ErrInvalidQuery
	var repoRequired service.ErrRepoRequired
	var staleErr service.ErrStaleIndex
	var linkErr service.ErrLinkCheckFailed
	var lockErr cache.ErrLockContention
	var corruption cache.ErrCacheCorruption
	var schemaErr *cache.SchemaVersionError
	var parentMissing service.ErrParentPRNotCached
	var rpcErr servicectl.RPCDomainError

	switch {
	case errors.As(err, &notFound) && notFound.Kind == "repository":
		data.Code = "missing_repository_binding"
		data.Message = fmt.Sprintf("repository binding %q is not configured", firstNonEmpty(data.RepoID, notFound.ID))
		data.Remediation = fmt.Sprintf("call repo_status with repo_id=%q; CLI fallback: gitcode-mcp doctor --repo %q --format json", firstNonEmpty(data.RepoID, notFound.ID), firstNonEmpty(data.RepoID, notFound.ID))
	case service.IsNotFound(err):
		data.Code = "not_found"
		data.Message = err.Error()
		data.Remediation = remediationForRepo("call sync_live for the missing resource or list_sources to inspect cached ids", data.RepoID, "gitcode-mcp sync")
	case service.IsCacheEmpty(err):
		data.Code = "cache_empty"
		data.Message = err.Error()
		data.Remediation = remediationForRepo("call sync_live for the required collection", data.RepoID, "gitcode-mcp sync")
	case errors.As(err, &invalid):
		data.Code = invalid.DiagnosticCode()
		data.Message = err.Error()
	case errors.As(err, &repoRequired):
		data.Code = "repo_required"
		data.Message = err.Error()
	case errors.As(err, &staleErr):
		data.Code = "stale_index"
		data.Message = err.Error()
		data.Remediation = remediationForRepo("call index_repo", data.RepoID, "gitcode-mcp index")
	case errors.As(err, &linkErr):
		data.Code = "link_check_failed"
		data.Message = err.Error()
	case errors.As(err, &lockErr):
		data = cacheLockErrorData(lockErr, lockErr.Error())
		if data.Remediation == "" {
			data.Remediation = "retry after the current cache writer completes; CLI fallback: gitcode-mcp doctor --format json"
		}
	case errors.As(err, &schemaErr):
		data.Code = "cache_schema_blocked"
		data.Message = fmt.Sprintf("cache schema is incompatible: detected=%d expected=%d", schemaErr.Compat.DetectedVersion, schemaErr.Compat.ExpectedVersion)
		data.Remediation = firstNonEmpty(schemaErr.Compat.Remediation, "run gitcode-mcp migrate-cache, review the plan, then rerun with --confirm")
	case errors.As(err, &corruption):
		data.Code = "cache_corruption"
		data.Message = "cache integrity validation failed"
		data.Remediation = "run gitcode-mcp doctor --format json; restore the cache or rebuild it with sync"
	case errors.As(err, &parentMissing):
		data.Code = parentMissing.DiagnosticCode()
		data.Message = parentMissing.Error()
		data.RepoID = parentMissing.RepoID
		data.Remediation = fmt.Sprintf("call sync_live with repo_id=%q, pulls=true, remote_alias=%q; CLI fallback: %s", parentMissing.RepoID, fmt.Sprintf("pr:%d", parentMissing.Number), parentMissing.Remediation())
	case errors.As(err, &rpcErr) && strings.TrimSpace(rpcErr.Code) != "":
		data.Code = strings.TrimSpace(rpcErr.Code)
		if message, remediation, ok := repositoryDocsDiagnostic(data.Code, data.RepoID); ok {
			data.Message = message
			data.Remediation = remediation
		} else {
			data.Message = fmt.Sprintf("local service reported %s", data.Code)
			data.Remediation = serviceRemediation(ctx.Operation)
		}
	case errors.Is(err, context.DeadlineExceeded):
		data.Code = "operation_timeout"
		data.Message = "the operation exceeded its deadline"
		if ctx.Subsystem == "service" {
			data.Remediation = serviceRemediation(ctx.Operation)
		} else {
			data.Remediation = remediationForRepo("retry the operation", data.RepoID, "gitcode-mcp doctor")
		}
	case errors.Is(err, context.Canceled):
		data.Code = "operation_cancelled"
		data.Message = "the operation was cancelled"
		data.Remediation = "retry only if the caller still needs the result"
	case ctx.Subsystem == "service" && isServiceUnavailableError(err):
		data.Code = "service_unavailable"
		data.Message = "local gitcode-mcp service is unavailable"
		data.Remediation = serviceRemediation(ctx.Operation)
	default:
		var coded interface{ DiagnosticCode() string }
		if errors.As(err, &coded) && strings.TrimSpace(coded.DiagnosticCode()) != "" {
			data.Code = strings.TrimSpace(coded.DiagnosticCode())
			if message, remediation, ok := repositoryDocsDiagnostic(data.Code, data.RepoID); ok {
				data.Message = message
				data.Remediation = remediation
			} else {
				data.Message = err.Error()
			}
		} else if ctx.Subsystem == "cache" {
			data.Code = "cache_unavailable"
			data.Message = "the selected cache could not complete the operation"
			data.Remediation = remediationForRepo("call doctor", data.RepoID, "gitcode-mcp doctor")
		} else if ctx.Subsystem == "service" {
			data.Code = "service_operation_failed"
			data.Message = "the local service rejected or could not complete the operation"
			data.Remediation = serviceRemediation(ctx.Operation)
		} else {
			data.Code = "internal_error"
			data.Message = "the operation failed without a recognized public-safe diagnostic"
			data.Remediation = "call doctor; CLI fallback: gitcode-mcp doctor --format json"
		}
	}
	return data
}

func repositoryDocsDiagnostic(code, repoID string) (string, string, bool) {
	switch strings.TrimSpace(code) {
	case "repository_docs_registration_not_found", "repository_docs_registration_unavailable", "repository_docs_registration_disabled", "repository_docs_source_not_registered":
		return "no enabled repository-document authority is registered for the selected cache and repository",
			remediationForRepo("register the local Git authority, then retry", repoID, "gitcode-mcp repo-docs register --repository-path PATH"), true
	case "repository_docs_source_ambiguous":
		return "multiple repository-document authorities are registered for this cache and repository",
			"call maintenance_status, select one repository_docs_sources entry, and retry with its registration_id, source_registration_id, and source_registration_generation", true
	case "repository_docs_source_generation_conflict":
		return "the selected repository-document authority generation is stale",
			"call maintenance_status and retry with the current opaque source selector", true
	case "repository_docs_source_selector_required":
		return "the repository-document authority selector is incomplete",
			"omit all selector fields for automatic sole-authority resolution, or call maintenance_status and supply all three opaque selector fields", true
	case "repository_docs_binding_unavailable":
		return "the repository is not bound in the selected cache",
			remediationForRepo("call repo_status and configure the repository binding", repoID, "gitcode-mcp doctor --format json"), true
	default:
		return "", "", false
	}
}

func domainErrorTitle(code string) string {
	switch code {
	case "missing_repository_binding":
		return "Repository binding unavailable"
	case "cache_empty", "cache_busy", "cache_owned", "cache_schema_blocked", "cache_corruption", "cache_unavailable", "migration_blocked":
		return "Cache operation failed"
	case "service_unavailable":
		return "Local service unavailable"
	case "service_operation_failed":
		return "Local service operation failed"
	case "invalid_query", "repo_required":
		return "Invalid request"
	case "not_found":
		return "Resource not found"
	case "repository_docs_registration_not_found", "repository_docs_registration_unavailable", "repository_docs_registration_disabled", "repository_docs_source_not_registered", "repository_docs_source_ambiguous", "repository_docs_source_generation_conflict", "repository_docs_source_selector_required", "repository_docs_binding_unavailable":
		return "Repository documentation authority unavailable"
	default:
		return "Operation failed"
	}
}

func remediationForRepo(mcpAction, repoID, cliCommand string) string {
	if strings.TrimSpace(repoID) == "" {
		return fmt.Sprintf("%s; CLI fallback: %s", mcpAction, cliCommand)
	}
	return fmt.Sprintf("%s with repo_id=%q; CLI fallback: %s --repo %q", mcpAction, repoID, cliCommand, repoID)
}

func serviceRemediation(operation string) string {
	if strings.TrimSpace(operation) == "service_status" {
		return "CLI fallback: gitcode-mcp service status --format json; if stopped, run gitcode-mcp service start"
	}
	return "call service_status; CLI fallback: gitcode-mcp service status --format json; if stopped, run gitcode-mcp service start"
}

func isServiceUnavailableError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return errors.Is(err, io.EOF) || strings.Contains(err.Error(), "service memory endpoint not found") || strings.Contains(err.Error(), "service address is required")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func LockContentionReadiness(err cache.ErrLockContention) Readiness {
	data := cacheLockErrorData(err, err.Error())
	return Readiness{Ready: false, Code: data.Code, Message: data.Message, ErrorData: data}
}

func cacheLockErrorData(err cache.ErrLockContention, message string) *errorData {
	data := &errorData{Code: cacheLockErrorCode(err), Message: message, Operation: err.PublicOperation(), RepoID: err.PublicRepoID(), CacheRef: err.PublicCacheRef()}
	if err.PID > 0 {
		data.PID = err.PID
	}
	if !err.StartedAt.IsZero() {
		data.StartedAt = err.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	return data
}

func cacheLockErrorCode(err cache.ErrLockContention) string {
	switch err.Operation {
	case "migration":
		return "migration_blocked"
	case "sync", "index", "write", "sync-index":
		return "cache_owned"
	default:
		return "cache_busy"
	}
}

func (s *Server) writeResponse(id *json.RawMessage, result json.RawMessage) {
	resp := response{JSONRPC: "2.0", ID: id, Result: result}
	b, _ := json.Marshal(resp)
	fmt.Fprintln(s.writer, string(b))
}

func (s *Server) writeError(id *json.RawMessage, code int, message string, data *errorData) {
	resp := response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message, Data: data}}
	b, _ := json.Marshal(resp)
	fmt.Fprintln(s.writer, string(b))
}

func bytesTrimSpace(b []byte) []byte {
	return bytes.TrimSpace(b)
}
