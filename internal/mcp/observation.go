package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"gitcode-mcp/internal/observability"
	"gitcode-mcp/internal/servicectl"
)

func decodeObservationCall(raw []byte, dst *toolCallParams) error {
	invalid := observability.QueryError{Code: "invalid_query", Reason: "unsupported_request"}
	if len(raw) > observability.MaxReadRequestBytes+1024 {
		return invalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return invalid
	}
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		name, ok := key.(string)
		if err != nil || !ok || seen[name] || (name != "name" && name != "arguments") {
			return invalid
		}
		seen[name] = true
		var value json.RawMessage
		if d.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return invalid
		}
	}
	if end, err := d.Token(); err != nil || end != json.Delim('}') {
		return invalid
	}
	if _, err := d.Token(); err != io.EOF {
		return invalid
	}
	if json.Unmarshal(raw, dst) != nil || dst.Name == "" {
		return invalid
	}
	return nil
}

// ToolBootstrap supplies dependencies to one request only. Discovery and local
// observation never call it or contend with its cache/credential initialization.
type ToolBootstrap func(context.Context, string, json.RawMessage) (*RPCHandler, error)

// The bootstrap's store lease lasts through the handler, not just construction.
func (h *RPCHandler) SetToolRelease(release func()) { h.toolRelease = release }

type BootstrapError struct{ Diagnostic StartupDiagnostic }

func (e BootstrapError) Error() string { return e.Diagnostic.Message }
func (h *RPCHandler) SetBootstrapDiagnostic(provider func() StartupDiagnostic) {
	h.bootstrapDiagnostic = provider
}
func (s *Server) SetBootstrapDiagnostic(provider func() StartupDiagnostic) {
	s.bootstrapDiagnostic = provider
	if s.handler != nil {
		s.handler.SetBootstrapDiagnostic(provider)
	}
}

func (h *RPCHandler) SetToolBootstrap(bootstrap ToolBootstrap) { h.toolBootstrap = bootstrap }
func (s *Server) SetToolBootstrap(bootstrap ToolBootstrap) {
	s.toolBootstrap = bootstrap
	if s.handler != nil {
		s.handler.SetToolBootstrap(bootstrap)
	}
}
func (h *RPCHandler) SetServiceClient(provider func() (*servicectl.RPCClient, error)) {
	h.serviceClient = provider
}
func (s *Server) SetServiceClient(provider func() (*servicectl.RPCClient, error)) {
	s.serviceClient = provider
	if s.handler != nil {
		s.handler.SetServiceClient(provider)
	}
}

func toolNeedsBootstrap(name string) bool {
	switch name {
	case "service_observability", "service_metrics", "service_events", "service_status", "maintenance_status", "maintenance_plan", "enable_cache_maintenance", "service_jobs", "service_job_status", "service_job_attach", "service_job_cancel", "repository_docs_sources", "repository_docs_plan", "repository_docs_index", "auth_status":
		return false
	}
	return true
}

func observationToolDefinition(name string) (toolDefinition, bool) {
	props := map[string]schemaProp{}
	description := "Read a bounded daemon-owned summary with separate execution and observation health and nullable unsampled resources. Never starts the daemon, opens/migrates a cache, resolves credentials, or repairs work."
	switch name {
	case "service_observability":
	case "service_metrics":
		description = "Read the fixed-name bounded daemon metric snapshot and retained minute history. Unsampled values remain unavailable/null. No cache bootstrap, provider setup, sampling, or daemon start."
		props["name"] = schemaProp{Type: "string", Description: "Optional fixed metric name."}
		name := props["name"]
		for _, d := range observability.MetricCatalog() {
			name.Enum = append(name.Enum, d.Name)
		}
		props["name"] = name
		props["history_minutes"] = schemaProp{Type: "integer", Minimum: float64Ptr(1), Maximum: float64Ptr(60), Default: 60}
	case "service_events":
		description = "Read a bounded retained execution ledger page. Severity is not outcome; filter explicit outcome/failure_class and use opaque refs from listed resources/summary incidents. Cursor binds all selectors, boot generation and loss; on snapshot_required restart without cursor. No live sync, cache bootstrap, credential resolution, or daemon start."
		for _, key := range []string{"stream", "severity", "component", "code", "phase", "collection", "outcome", "failure_class", "job_ref", "registration_ref", "cache_ref", "repo_ref", "correlation_ref", "stage_ref", "source_ref", "previous_registration_ref", "since", "until", "cursor"} {
			props[key] = schemaProp{Type: "string", Description: "Opaque ref from already returned local entity/event/incident evidence; never a repository id, filesystem path, or private coordinate."}
		}
		for key, values := range observability.FilterValues() {
			props[key] = schemaProp{Type: "string", Description: "Finite event dimension; incompatible selector combinations are rejected. Severity is not outcome.", Enum: values}
		}
		for _, key := range []string{"since", "until"} {
			props[key] = schemaProp{Type: "string", Description: "Paired ordered RFC3339Nano UTC (Z) range, at most seven days. Omit both for retained evidence only."}
		}
		props["cursor"] = schemaProp{Type: "string", Description: "Opaque signed cursor from the preceding page; reuse all identical selectors. Refresh without it on snapshot_required."}
		props["limit"] = schemaProp{Type: "integer", Minimum: float64Ptr(1), Maximum: float64Ptr(200), Default: 50}
	default:
		return toolDefinition{}, false
	}
	strict := false
	return toolDefinition{Name: name, Description: description, Annotations: map[string]bool{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}, InputSchema: inputSchema{Type: "object", Properties: props, AdditionalProperties: &strict}, Meta: map[string]any{"gitcode_mcp": map[string]any{"read_only": true, "protocol": observability.Protocol, "limits": observability.Limits()}}}, true
}

func (s *Server) callServiceObservability(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	if err := observability.DecodeReadRequest(args, &struct{}{}); err != nil {
		s.writeObservationError(id, err)
		return
	}
	var result observability.Summary
	s.callObservation(ctx, id, "Observability.Summary", struct{}{}, &result)
}
func (s *Server) callServiceMetrics(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	params, err := observability.DecodeMetricsRequest(args)
	if err != nil {
		s.writeObservationError(id, err)
		return
	}
	var result observability.MetricsSnapshot
	s.callObservation(ctx, id, "Observability.Metrics", params, &result)
}
func (s *Server) callServiceEvents(ctx context.Context, id *json.RawMessage, args json.RawMessage) {
	params, err := observability.DecodeEventsRequest(args)
	if err != nil {
		s.writeObservationError(id, err)
		return
	}
	var result observability.Page
	s.callObservation(ctx, id, "Observability.Events", params, &result)
}
func (s *Server) callObservation(ctx context.Context, id *json.RawMessage, method string, params any, result any) {
	ctx, cancel := context.WithTimeout(ctx, observability.ReadBudget)
	defer cancel()
	client, err := s.localServiceClient()
	if err != nil {
		s.writeObservationError(id, servicectl.RPCDomainError{Code: "service_unavailable", Message: "local daemon is unavailable; explicitly start it before retrying"})
		return
	}
	if err := client.Call(ctx, method, params, result); err != nil {
		s.writeObservationError(id, err)
		return
	}
	s.writeToolResult(id, toolCallResult{Content: []toolContentItem{{Type: "text", Text: "Bounded read-only daemon observation; inspect structuredContent for coverage and unavailable values."}}, StructuredContent: result})
}
func (s *Server) writeObservationError(id *json.RawMessage, err error) {
	var query observability.QueryError
	if errors.As(err, &query) {
		code := -32000
		if query.Code == "invalid_query" || query.Code == "invalid_cursor" {
			code = -32602
		}
		s.writeError(id, code, "Observation unavailable", &errorData{Code: query.Code, Message: "bounded observation request could not be completed", Observation: &query})
		return
	}
	var domain servicectl.RPCDomainError
	if errors.As(err, &domain) {
		data := &errorData{Code: domain.Code, Operation: "service_observation", Message: "bounded local observation is unavailable"}
		switch domain.Code {
		case "unsupported_capability":
			data.Message = "daemon does not support observability.v1"
			data.Remediation = "explicitly update the daemon, then retry; this read never updates it"
		case "service_unavailable":
			data.Message = "local daemon is unavailable"
			data.Remediation = "explicitly start the daemon, then retry; this read never starts it"
		case "observation_timeout":
			data.Message = "local observation read timed out"
			data.Remediation = "retry the bounded read only if still needed"
		case "observation_cancelled":
			data.Message = "local observation read was cancelled"
			data.Remediation = "retry only if still needed"
		case "observation_busy":
			data.Message = "local observation snapshot is busy"
			data.Remediation = "retry the read without changing execution state"
		case "response_limit", "observation_unavailable", "invalid_response":
			data.Remediation = "inspect daemon compatibility and retry a bounded read"
		default:
			data.Code = "invalid_response"
			data.Remediation = "inspect daemon compatibility before retrying"
		}
		s.writeError(id, -32000, "Observation unavailable", data)
		return
	}
	s.writeOperationalError(id, err, domainErrorContext{Operation: "service_observation", Subsystem: "service"})
}
