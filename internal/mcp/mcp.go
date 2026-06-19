package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"gitcode-mcp/internal/service"
)

const protocolVersion = "2024-11-05"
const serverVersion = "0.1.0"

type serviceInterface interface {
	SearchSources(context.Context, service.SearchSourcesRequest) ([]service.SearchSourceResult, error)
	GetSource(context.Context, service.GetSourceRequest) (service.SourceRecord, error)
	ListSources(context.Context, service.ListSourcesRequest) ([]service.SourceSummary, error)
	GetBacklinks(context.Context, service.GetBacklinksRequest) ([]service.BacklinkResult, error)
	ResolveID(context.Context, service.ResolveIDRequest) (service.ResolvedID, error)
	GetSyncStatus(context.Context, service.SyncStatusRequest) (service.SyncStatusResult, error)
	ExportSnapshot(context.Context, service.ExportSnapshotRequest) (service.ExportSnapshotResult, error)
	DiffSnapshot(context.Context, service.DiffSnapshotRequest) (service.DiffSnapshotResult, error)
}

type Server struct {
	reader io.Reader
	writer io.Writer
	stderr io.Writer
	svc    serviceInterface
}

func New(r io.Reader, w io.Writer, stderr io.Writer, svc serviceInterface) *Server {
	return &Server{reader: r, writer: w, stderr: stderr, svc: svc}
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
	Code    string `json:"code"`
	Message string `json:"message"`
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
	ListChanged bool `json:"listChanged"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type toolsListResult struct {
	Tools []toolDefinition `json:"tools"`
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

type aggregateSyncStatus struct {
	FreshCount int    `json:"fresh_count"`
	StaleCount int    `json:"stale_count"`
	LastSyncAt string `json:"last_sync_at"`
	CacheEmpty bool   `json:"cache_empty"`
}

func intPtr(v int) *int             { return &v }
func float64Ptr(v float64) *float64 { return &v }

var toolDefs = []toolDefinition{
	{
		Name:        "search_sources",
		Description: "Search cached sources by full-text query.",
		InputSchema: inputSchema{
			Type: "object",
			Properties: map[string]schemaProp{
				"repo_id": {Type: "string", Description: "Configured repository id.", MinLength: 1},
				"query":   {Type: "string", Description: "Search query text.", MinLength: 1},
				"kind":    {Type: "string", Description: "Source kind filter.", Enum: []string{"source", "task", "page", "decision", "handoff"}},
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
				"kind":    {Type: "string", Description: "Source kind filter.", Enum: []string{"source", "task", "page", "decision", "handoff"}},
				"status":  {Type: "string", Description: "Source status filter."},
				"limit":   {Type: "integer", Description: "Maximum results.", Minimum: float64Ptr(1), Maximum: float64Ptr(100), Default: 20.0},
				"offset":  {Type: "integer", Description: "Result offset.", Minimum: float64Ptr(0), Default: 0.0},
			},
			Required: []string{"repo_id"},
		},
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
}

func (s *Server) Serve() error {
	ctx := context.Background()
	buf := make([]byte, 0, 4096)
	for {
		line, err := readLineFrom(s.reader, buf[:0])
		if err == io.EOF {
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

		if req.JSONRPC != "2.0" || req.Method == "" {
			s.writeError(req.ID, -32600, "Invalid request", nil)
			continue
		}
		isNotification := req.ID == nil
		s.handle(ctx, req, isNotification)
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
	result := initResult{
		ProtocolVersion: protocolVersion,
		Capabilities:    initCapability{Tools: toolCapability{ListChanged: false}},
		ServerInfo:      serverInfo{Name: "gitcode-mcp", Version: serverVersion},
	}
	b, _ := json.Marshal(result)
	s.writeResponse(req.ID, b)
}

func (s *Server) toolsList(req request) {
	b, _ := json.Marshal(toolsListResult{Tools: toolDefs})
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

	args := params.Arguments
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}

	switch params.Name {
	case "search_sources":
		s.callSearchSources(ctx, req.ID, args)
	case "get_source":
		s.callGetSource(ctx, req.ID, args)
	case "list_sources":
		s.callListSources(ctx, req.ID, args)
	case "source_backlinks":
		s.callSourceBacklinks(ctx, req.ID, args)
	case "resolve_id":
		s.callResolveID(ctx, req.ID, args)
	case "sync_status":
		s.callSyncStatus(ctx, req.ID, args)
	case "export_snapshot":
		s.callExportSnapshot(ctx, req.ID, args)
	case "diff_snapshot":
		s.callDiffSnapshot(ctx, req.ID, args)
	default:
		s.writeError(req.ID, -32601, "Method not found", &errorData{Code: "unknown_tool", Message: fmt.Sprintf("unknown tool %q", params.Name)})
	}
}

type searchSourcesArgs struct {
	RepoID string `json:"repo_id"`
	Query  string `json:"query"`
	Kind   string `json:"kind,omitempty"`
	Limit  *int   `json:"limit,omitempty"`
	Offset *int   `json:"offset,omitempty"`
}
type searchSourcesSResult struct {
	Results []service.SearchSourceResult `json:"results"`
	Limit   int                          `json:"limit"`
	Offset  int                          `json:"offset"`
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
	if a.Kind != "" {
		valid := false
		for _, k := range []string{"source", "task", "page", "decision", "handoff"} {
			if a.Kind == k {
				valid = true
				break
			}
		}
		if !valid {
			s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "kind must be one of: source, task, page, decision, handoff"})
			return
		}
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

	all := results
	if len(all) > limit {
		all = all[:limit]
	}

	var text string
	for _, r := range all {
		text += fmt.Sprintf("%s:%s\n", r.Path, r.Snippet)
	}

	s.writeToolResult(id, toolCallResult{
		Content:           []toolContentItem{{Type: "text", Text: text}},
		StructuredContent: searchSourcesSResult{Results: all, Limit: limit, Offset: offset},
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
type listSourcesSResult struct {
	Results []service.SourceSummary `json:"results"`
	Limit   int                     `json:"limit"`
	Offset  int                     `json:"offset"`
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
	if a.Kind != "" {
		valid := false
		for _, k := range []string{"source", "task", "page", "decision", "handoff"} {
			if a.Kind == k {
				valid = true
				break
			}
		}
		if !valid {
			s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "kind must be one of: source, task, page, decision, handoff"})
			return
		}
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
	for _, r := range results {
		text += fmt.Sprintf("%s %s %s\n", r.ID, r.Path, r.Title)
	}

	s.writeToolResult(id, toolCallResult{
		Content:           []toolContentItem{{Type: "text", Text: text}},
		StructuredContent: listSourcesSResult{Results: results, Limit: limit, Offset: offset},
	})
}

type sourceBacklinksArgs struct {
	RepoID string `json:"repo_id"`
	ID     string `json:"id"`
	Limit  *int   `json:"limit,omitempty"`
	Offset *int   `json:"offset,omitempty"`
}
type sourceBacklinksSResult struct {
	ID        string                   `json:"id"`
	Backlinks []service.BacklinkResult `json:"backlinks"`
	Limit     int                      `json:"limit"`
	Offset    int                      `json:"offset"`
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

	results, err := s.svc.GetBacklinks(ctx, service.GetBacklinksRequest{RepoID: a.RepoID, ID: a.ID})
	if err != nil {
		s.writeDomainError(id, err)
		return
	}

	if offset > 0 && offset < len(results) {
		results = results[offset:]
	}
	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}

	var text string
	for _, r := range results {
		text += fmt.Sprintf("%s %s %s\n", r.ID, r.Path, r.Kind)
	}

	s.writeToolResult(id, toolCallResult{
		Content:           []toolContentItem{{Type: "text", Text: text}},
		StructuredContent: sourceBacklinksSResult{ID: a.ID, Backlinks: results, Limit: limit, Offset: offset},
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
		sources, err := s.svc.ListSources(ctx, service.ListSourcesRequest{RepoID: a.RepoID, Limit: 1})
		if err != nil {
			if service.IsCacheEmpty(err) {
				result := aggregateSyncStatus{CacheEmpty: true}
				s.writeToolResult(id, toolCallResult{
					Content:           []toolContentItem{{Type: "text", Text: "cache is empty"}},
					StructuredContent: result,
				})
				return
			}
			s.writeDomainError(id, err)
			return
		}
		result := aggregateSyncStatus{FreshCount: len(sources), CacheEmpty: len(sources) == 0}
		s.writeToolResult(id, toolCallResult{
			Content:           []toolContentItem{{Type: "text", Text: fmt.Sprintf("fresh=%d stale=%d cache_empty=%v", result.FreshCount, result.StaleCount, result.CacheEmpty)}},
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
		Content: []toolContentItem{{Type: "text", Text: text}},
		StructuredContent: map[string]any{
			"id":                status.SourceID,
			"fresh":             status.Status == "fresh",
			"stale":             status.Status != "fresh",
			"last_fetched_at":   status.LastFetchedAt,
			"remote_updated_at": status.LastFetchedAt,
			"reason":            status.Status,
		},
	})
}

type exportSnapshotArgs struct {
	RepoID string `json:"repo_id"`
	Format string `json:"format,omitempty"`
	Inline *bool  `json:"inline,omitempty"`
}
type exportSnapshotSResult struct {
	Format      string `json:"format"`
	Content     string `json:"content"`
	Path        string `json:"path,omitempty"`
	ContentHash string `json:"content_hash"`
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
		Format:      format,
		ContentHash: result.ContentHash,
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
	BaseID  string `json:"base_id"`
	HeadID  string `json:"head_id"`
	Format  string `json:"format"`
	Diff    string `json:"diff"`
	Changed bool   `json:"changed"`
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
	if a.BaseID == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "base_id is required"})
		return
	}
	if a.HeadID == "" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "head_id is required"})
		return
	}
	format := "text"
	if a.Format != "" {
		format = a.Format
	}
	if format != "text" && format != "json" {
		s.writeError(id, -32602, "Invalid params", &errorData{Code: "invalid_arguments", Message: "format must be text or json"})
		return
	}

	result, err := s.svc.DiffSnapshot(ctx, service.DiffSnapshotRequest{
		RepoID:         a.RepoID,
		BaseSnapshotID: a.BaseID,
		HeadSnapshotID: a.HeadID,
		Format:         format,
	})
	if err != nil {
		s.writeDomainError(id, err)
		return
	}

	s.writeToolResult(id, toolCallResult{
		Content: []toolContentItem{{Type: "text", Text: result.DiffText}},
		StructuredContent: diffSnapshotSResult{
			BaseID:  a.BaseID,
			HeadID:  a.HeadID,
			Format:  format,
			Diff:    result.DiffText,
			Changed: len(result.ChangedSourceIDs) > 0,
		},
	})
}

func (s *Server) writeToolResult(id *json.RawMessage, result toolCallResult) {
	b, _ := json.Marshal(result)
	s.writeResponse(id, b)
}

func (s *Server) writeDomainError(id *json.RawMessage, err error) {
	var data *errorData
	switch {
	case service.IsNotFound(err):
		data = &errorData{Code: "not_found", Message: err.Error()}
	case service.IsCacheEmpty(err):
		data = &errorData{Code: "cache_empty", Message: err.Error()}
	default:
		var staleErr service.ErrStaleIndex
		if errors.As(err, &staleErr) {
			data = &errorData{Code: "stale_cache", Message: err.Error()}
		} else {
			data = &errorData{Code: "sync_required", Message: err.Error()}
		}
	}

	s.writeError(id, -32000, "Server error", data)
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
