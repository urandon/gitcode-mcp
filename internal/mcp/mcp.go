package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/diagnostics"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/service"
)

const protocolVersion = "2024-11-05"
const serverVersion = "0.1.0"

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
	Index(context.Context, service.OperationRequest) (service.OperationResult, error)
	ListChunks(context.Context, service.ChunkQuery) (service.ChunkQueryResult, error)
	SearchChunks(context.Context, service.ChunkSearchQuery) (service.ChunkQueryResult, error)
	GetChunkSnippet(context.Context, service.SnippetQuery) (service.ChunkQueryResult, error)
	StaleIndex(context.Context, service.StaleIndexRequest) (service.StaleIndexResult, error)
	RecentChanges(context.Context, service.RecentChangesRequest) (service.RecentChangesResult, error)
	LinkCheck(context.Context, service.LinkCheckRequest) (service.LinkCheckResult, error)
	CacheStatus(context.Context, service.CacheStatusRequest) (service.CacheStatusResult, error)
}

type RPCHandler struct {
	svc               serviceInterface
	startupDiagnostic StartupDiagnostic
	minimal           bool
}

type Server struct {
	reader            io.Reader
	writer            io.Writer
	stderr            io.Writer
	handler           *RPCHandler
	svc               serviceInterface
	startupDiagnostic StartupDiagnostic
	minimal           bool
}

func NewRPCHandler(svc serviceInterface) *RPCHandler {
	return &RPCHandler{svc: svc}
}

func NewMinimalRPCHandler(diagnostic StartupDiagnostic) *RPCHandler {
	return &RPCHandler{startupDiagnostic: diagnostic, minimal: true}
}

func New(r io.Reader, w io.Writer, stderr io.Writer, svc serviceInterface) *Server {
	return &Server{reader: r, writer: w, stderr: stderr, handler: NewRPCHandler(svc), svc: svc}
}

func NewMinimal(r io.Reader, w io.Writer, stderr io.Writer, diagnostic StartupDiagnostic) *Server {
	return &Server{reader: r, writer: w, stderr: stderr, handler: NewMinimalRPCHandler(diagnostic), startupDiagnostic: diagnostic, minimal: true}
}

func (h *RPCHandler) Handle(ctx context.Context, req request) (*response, bool) {
	if req.JSONRPC != "2.0" || req.Method == "" {
		return &response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32600, Message: "Invalid request"}}, true
	}
	var buf bytes.Buffer
	server := &Server{writer: &buf, stderr: io.Discard, handler: h, svc: h.svc, startupDiagnostic: h.startupDiagnostic, minimal: h.minimal}
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
	CachePath    string `json:"cache_path,omitempty"`
}

type toolDefinition struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema inputSchema `json:"inputSchema"`
}

type inputSchema struct {
	Type       string                `json:"type"`
	Properties map[string]schemaProp `json:"properties"`
	Required   []string              `json:"required,omitempty"`
}

type schemaProp struct {
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Enum        []string `json:"enum,omitempty"`
	Minimum     *float64 `json:"minimum,omitempty"`
	Maximum     *float64 `json:"maximum,omitempty"`
	Default     any      `json:"default,omitempty"`
	MinLength   int      `json:"minLength,omitempty"`
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

var sourceKindEnums = []string{"issue", "wiki"}

func intPtr(v int) *int             { return &v }
func float64Ptr(v float64) *float64 { return &v }
func kindValidationMessage() string {
	return "kind must be one of: " + strings.Join(sourceKindEnums, ", ")
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
		props["query"] = schemaProp{Type: "string", Description: "Normalized chunk query text.", MinLength: 1}
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
		Description: "Search cached sources by full-text query.",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]schemaProp{
				"repo_id": {Type: "string", Description: "Configured repository id.", MinLength: 1},
				"query":   {Type: "string", Description: "Search query text.", MinLength: 1},
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
		Description: "Search cached index chunks through the shared chunk query result model.",
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
		Name:        "sync_live",
		Description: "Synchronize selected live collection records into the cache.",
		InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{"repo_id": {Type: "string", Description: "Configured repository id.", MinLength: 1}, "issues": {Type: "boolean", Description: "Sync issues."}, "wiki": {Type: "boolean", Description: "Sync wiki pages."}, "comments": {Type: "boolean", Description: "Sync comments."}, "pulls": {Type: "boolean", Description: "Sync pull requests."}, "remote_alias": {Type: "string", Description: "Specific remote alias for current sync surface."}, "idempotency_key": {Type: "string", Description: "Idempotency key."}}, Required: []string{"repo_id"}},
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
		s.toolsList(req)
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
		ServerInfo:      serverInfo{Name: "gitcode-mcp", Version: serverVersion},
	}
	b, _ := json.Marshal(result)
	s.writeResponse(req.ID, b)
}

func (s *Server) toolsList(req request) {
	registry := s.toolRegistry()
	tools := make([]toolDefinition, 0, len(registry))
	for _, name := range toolListOrder {
		tool, ok := registry[name]
		if !ok {
			continue
		}
		tools = append(tools, tool.definition)
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

var toolListOrder = []string{
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
	"sync_live",
	"index_repo",
	"auth_status",
	"doctor",
}

func toolDefinitionByName(name string) toolDefinition {
	for _, def := range toolDefs {
		if def.Name == name {
			return def
		}
	}
	return toolDefinition{Name: name, InputSchema: inputSchema{Type: "object", Properties: map[string]schemaProp{}}}
}

func registerTool(registry toolRegistry, name string, handler toolHandler) {
	registry[name] = registeredTool{definition: toolDefinitionByName(name), handler: handler}
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
	registerTool(registry, "sync_live", s.callSyncLive)
	registerTool(registry, "index_repo", s.callIndexRepo)
	registerTool(registry, "auth_status", s.callAuthStatus)
	registerTool(registry, "doctor", s.callDoctor)
	return registry
}

type searchSourcesArgs struct {
	RepoID string `json:"repo_id"`
	Query  string `json:"query"`
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

	results, err := s.svc.SearchSources(ctx, service.SearchSourcesRequest{
		RepoID: a.RepoID,
		Query:  a.Query,
		Kind:   a.Kind,
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		s.writeDomainError(id, err)
		return
	}

	var text string
	for _, r := range results.Results {
		text += fmt.Sprintf("%s:%s\n", r.Path, r.Snippet)
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
		s.writeDomainError(id, err)
		return
	}

	text := result.Body
	if text == "" {
		text = fmt.Sprintf("%s: %s", result.Title, result.Body)
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
		s.writeDomainError(id, err)
		return
	}

	var text string
	for _, r := range results.Results {
		text += fmt.Sprintf("%s %s %s\n", r.ID, r.Path, r.Title)
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
		s.writeDomainError(id, err)
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
		s.writeDomainError(id, err)
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
		s.writeDomainError(id, err)
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
			s.writeDomainError(id, err)
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
		s.writeDomainError(id, err)
		return
	}
	text := ""
	for _, result := range results.Results {
		text += fmt.Sprintf("%s %s %s %s\n", result.RepoID, result.ID, result.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"), result.Title)
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
			s.writeDomainError(id, err)
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
		s.writeDomainError(id, err)
		return
	}
	text := fmt.Sprintf("repo_id=%s records=%d chunks=%d journal=%s", result.RepoID, result.Records, result.Chunks, result.JournalMode)
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
		s.writeDomainError(id, err)
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
		s.writeDomainError(id, err)
		return
	}

	text := fmt.Sprintf("%s %s %s", result.ID, result.Path, result.RemoteAlias)

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
			s.writeDomainError(id, err)
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
		s.writeDomainError(id, err)
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
		s.writeDomainError(id, err)
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
		s.writeDomainError(id, err)
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

func mcpDiagnostic(err error) (diagnostics.Diagnostic, bool) {
	ctx := diagnostics.CommandContext{ProviderMode: "live-http"}
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
	if errors.As(err, &syncErr) {
		ctx.HTTPAttempted = syncErr.Mode == "live_auth_failure" || syncErr.Mode == "network_timeout" || syncErr.Mode == "rate_limited" || syncErr.Mode == "partial_response" || syncErr.Mode == "live_graph_invalid" || syncErr.Mode == "payload_too_large" || syncErr.Mode == "remote_not_found" || syncErr.Mode == "conflict" || syncErr.Mode == "remote_collision"
		ctx.FailureSource = syncErr.PayloadSource
		ctx.LocalPayloadTooLarge = syncErr.Mode == "payload_too_large" && syncErr.PayloadSource == "local_body_limit"
		ctx.SchemaDecodeFailure = syncErr.Mode == "partial_response" || syncErr.Mode == "schema_decode"
		if syncErr.Mode == "partial_response" {
			ctx.FailureSource = "partial_response"
		}
		return diagnostics.Classify(err, ctx), true
	}
	if errors.As(err, &writeErr) {
		ctx.HTTPAttempted = writeErr.Code == "write_unauthorized" || writeErr.Code == "write_network_unavailable" || writeErr.Code == "write_provider_error" || writeErr.Code == "write_conflict"
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

func (s *Server) writeDomainError(id *json.RawMessage, err error) {
	var data *errorData
	diagnostic, hasDiagnostic := mcpDiagnostic(err)
	switch {
	case service.IsNotFound(err):
		data = &errorData{Code: "not_found", Message: err.Error()}
	case service.IsCacheEmpty(err):
		data = &errorData{Code: "cache_empty", Message: err.Error()}
	default:
		var invalid service.ErrInvalidQuery
		var repoRequired service.ErrRepoRequired
		var staleErr service.ErrStaleIndex
		var linkErr service.ErrLinkCheckFailed
		var lockErr cache.ErrLockContention
		switch {
		case errors.As(err, &invalid):
			data = &errorData{Code: "invalid_query", Message: err.Error()}
		case errors.As(err, &repoRequired):
			data = &errorData{Code: "repo_required", Message: err.Error()}
		case errors.As(err, &staleErr):
			data = &errorData{Code: "stale_index", Message: err.Error()}
		case errors.As(err, &linkErr):
			data = &errorData{Code: "link_check_failed", Message: err.Error()}
		case errors.As(err, &lockErr):
			data = cacheLockErrorData(lockErr, err.Error())
		default:
			data = &errorData{Code: "sync_required", Message: err.Error()}
		}
	}
	if hasDiagnostic && data != nil {
		data.FailureClass = string(diagnostic.Code)
	}

	s.writeError(id, -32000, "Server error", data)
}

func LockContentionReadiness(err cache.ErrLockContention) Readiness {
	data := cacheLockErrorData(err, err.Error())
	return Readiness{Ready: false, Code: data.Code, Message: data.Message, ErrorData: data}
}

func cacheLockErrorData(err cache.ErrLockContention, message string) *errorData {
	data := &errorData{Code: cacheLockErrorCode(err), Message: message, Operation: strings.TrimSpace(err.Operation), RepoID: strings.TrimSpace(err.RepoID), PID: err.PID}
	if !err.StartedAt.IsZero() {
		data.StartedAt = err.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	if strings.HasPrefix(err.CachePath, ":memory:") || strings.HasPrefix(err.CachePath, "file:") {
		data.CachePath = err.CachePath
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
		return "busy"
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
