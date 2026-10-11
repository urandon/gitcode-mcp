package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/observability"
	"gitcode-mcp/internal/servicectl"
)

func assertMinimalObservationTools(t *testing.T, tools []toolDefinition) {
	t.Helper()
	want := map[string]bool{"doctor": true, "service_observability": true, "service_metrics": true, "service_events": true}
	if len(tools) != len(want) {
		t.Fatal("minimal discovery lost its fixed four-tool contract")
	}
	for _, tool := range tools {
		if !want[tool.Name] {
			t.Fatal("unexpected minimal tool")
		}
		delete(want, tool.Name)
	}
	if len(want) != 0 {
		t.Fatal("minimal discovery contains duplicate tools")
	}
}

func observationMCPFixture(t *testing.T) (*servicectl.RPCClient, *observability.Reader) {
	t.Helper()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	c := observability.New(observability.Config{Directory: t.TempDir(), ManagedOutput: true})
	if !c.Close(context.Background(), true) {
		t.Fatal("fixture collector did not close")
	}
	m := observability.NewMetrics(c.BootID(), at)
	if !m.SampleAt(at, map[string]observability.ProbeValue{"runtime_heap": observability.Available(42)}) {
		t.Fatal("actual metric sample failed")
	}
	r := &observability.Reader{Collector: c, Metrics: m, Now: func() time.Time { return at }}
	ctx, cancel := context.WithCancel(context.Background())
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- (servicectl.RPCServer{Observability: r}).Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("observation fixture worker did not unwind")
		}
	})
	return &servicectl.RPCClient{Network: "tcp", Address: listener.Addr().String()}, r
}

func mcpObservationCall(t *testing.T, h *RPCHandler, name, args string) *response {
	t.Helper()
	params := json.RawMessage(`{"name":` + strconvJSON(name) + `,"arguments":` + args + `}`)
	id := json.RawMessage(`1`)
	resp, ok := h.Handle(context.Background(), request{JSONRPC: "2.0", ID: &id, Method: "tools/call", Params: &params})
	if !ok || resp == nil {
		t.Fatal("missing observation response")
	}
	return resp
}
func strconvJSON(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestMCPObservationStrictBeforeClientOrBootstrap(t *testing.T) {
	var clientCalls, bootCalls atomic.Int64
	h := NewRPCHandler(nil)
	h.SetToolBootstrap(func(context.Context, string, json.RawMessage) (*RPCHandler, error) { bootCalls.Add(1); return nil, nil })
	h.SetServiceClient(func() (*servicectl.RPCClient, error) {
		clientCalls.Add(1)
		return &servicectl.RPCClient{Network: "mem", Address: "absent"}, nil
	})
	for _, test := range []struct{ name, args string }{
		{"service_observability", `{"repo_id":"synthetic-private"}`},
		{"service_observability", `null`},
		{"service_metrics", `{"name":"synthetic-private"}`},
		{"service_metrics", `{"history_minutes":0}`},
		{"service_metrics", `{"history_minutes":1,"history_minutes":2}`},
		{"service_events", `{"limit":0}`},
		{"service_events", `{"limit":1.5}`},
		{"service_events", `{"source_ref":"synthetic-private"}`},
		{"service_events", `{"since":"2026-01-01T00:00:00Z"}`},
		{"service_events", `{"limit":50,"limit":10}`},
		{"service_events", `{"write_mode":"live"}`},
		{"service_events", `{"cursor":null}`},
	} {
		resp := mcpObservationCall(t, h, test.name, test.args)
		if resp.Error == nil || resp.Error.Code != -32602 || resp.Error.Data.Code != "invalid_query" {
			t.Fatal("invalid observation query accepted")
		}
		b, _ := json.Marshal(resp)
		if strings.Contains(string(b), "synthetic-private") {
			t.Fatal("invalid query echoed private input")
		}
	}
	for _, raw := range []string{`{"name":"service_events","name":"service_events","arguments":{}}`, `{"name":"service_events","extra":1}`, `{"name":"service_events","arguments":{},"arguments":{}}`} {
		params := json.RawMessage(raw)
		id := json.RawMessage(`1`)
		resp, ok := h.Handle(context.Background(), request{JSONRPC: "2.0", ID: &id, Method: "tools/call", Params: &params})
		if !ok || resp.Error == nil || resp.Error.Code != -32602 {
			t.Fatal("invalid observation envelope accepted")
		}
	}
	if clientCalls.Load() != 0 || bootCalls.Load() != 0 {
		t.Fatal("invalid arguments reached local client or bootstrap")
	}
}

func assertObservationMCPParity(t *testing.T, r *observability.Reader, call func(string, string) *response) {
	t.Helper()
	wantSummary, err := r.Summary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantMetrics, err := r.MetricSnapshot(context.Background(), observability.MetricsRequest{Name: "runtime_heap", HistoryMinutes: 3})
	if err != nil {
		t.Fatal(err)
	}
	wantEvents, err := r.Events(context.Background(), observability.EventsRequest{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, args string
		want       any
	}{
		{"service_observability", `{}`, wantSummary},
		{"service_metrics", `{"name":"runtime_heap","history_minutes":3}`, wantMetrics},
		{"service_events", `{}`, wantEvents},
	} {
		resp := call(test.name, test.args)
		if resp.Error != nil {
			t.Fatalf("%s failed: %s", test.name, resp.Error.Data.Code)
		}
		var result struct {
			Structured json.RawMessage `json:"structuredContent"`
		}
		if json.Unmarshal(resp.Result, &result) != nil {
			t.Fatal("invalid tool result")
		}
		var got, want any
		_ = json.Unmarshal(result.Structured, &got)
		b, _ := json.Marshal(test.want)
		_ = json.Unmarshal(b, &want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s differs from shared reader", test.name)
		}
	}
}

func TestMCPObservationNormalMinimalAndStdioParity(t *testing.T) {
	client, r := observationMCPFixture(t)
	for _, minimal := range []bool{false, true} {
		h := NewRPCHandler(nil)
		if minimal {
			h = NewMinimalRPCHandler(StartupDiagnostic{ErrorClass: "schema_incompatible", Message: "cache is incompatible", Remediation: "explicitly migrate cache"})
		}
		h.SetServiceClient(func() (*servicectl.RPCClient, error) { return client, nil })
		h.SetToolBootstrap(func(context.Context, string, json.RawMessage) (*RPCHandler, error) {
			t.Error("observation invoked bootstrap")
			return nil, nil
		})
		assertObservationMCPParity(t, r, func(name, args string) *response { return mcpObservationCall(t, h, name, args) })
		var input strings.Builder
		for _, name := range []string{"service_observability", "service_metrics", "service_events"} {
			input.WriteString(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":` + strconvJSON(name) + `,"arguments":{}}}` + "\n")
		}
		var output bytes.Buffer
		s := NewWithToolAccess(strings.NewReader(input.String()), &output, io.Discard, nil, nil, ToolAccessRead)
		if minimal {
			s = NewMinimal(strings.NewReader(input.String()), &output, io.Discard, StartupDiagnostic{ErrorClass: "cache_path_unwritable"})
		}
		s.SetServiceClient(func() (*servicectl.RPCClient, error) { return client, nil })
		if s.Serve() != nil {
			t.Fatal("stdio observation serve failed")
		}
		lines := strings.Split(strings.TrimSpace(output.String()), "\n")
		if len(lines) != 3 {
			t.Fatal("stdio observation lost responses")
		}
		for _, line := range lines {
			var resp response
			if json.Unmarshal([]byte(line), &resp) != nil || resp.Error != nil {
				t.Fatal("stdio observation failed")
			}
		}
	}
}

func TestMCPObservationHTTPSSEParityDuringCacheBootstrap(t *testing.T) {
	client, r := observationMCPFixture(t)
	h := NewRPCHandler(nil)
	h.SetServiceClient(func() (*servicectl.RPCClient, error) { return client, nil })
	started, release := make(chan struct{}), make(chan struct{})
	h.SetToolBootstrap(func(context.Context, string, json.RawMessage) (*RPCHandler, error) {
		close(started)
		<-release
		return nil, context.Canceled
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		id := json.RawMessage(`1`)
		params := json.RawMessage(`{"name":"cache_status","arguments":{}}`)
		_, _ = h.Handle(context.Background(), request{JSONRPC: "2.0", ID: &id, Method: "tools/call", Params: &params})
	}()
	defer func() {
		close(release)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("bootstrap fixture worker did not unwind")
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("bootstrap fixture did not start")
	}
	transport := NewHTTPSSETransport(h, ServerConfig{SessionID: func() string { return "observation-session" }})
	server := httptest.NewServer(transport.Handler())
	defer server.Close()
	sse, endpoint, events := openSSE(t, server.URL+"/sse")
	defer sse.Body.Close()
	assertObservationMCPParity(t, r, func(name, args string) *response {
		postJSON(t, server.URL+endpoint, map[string]any{"jsonrpc": "2.0", "id": name, "method": "tools/call", "params": map[string]any{"name": name, "arguments": json.RawMessage(args)}})
		response := readSSEMessage(t, events)
		return &response
	})
}

func TestMCPObservationDiscoveryFiniteReadOnlySchemas(t *testing.T) {
	h := NewRPCHandler(nil)
	h.SetToolBootstrap(func(context.Context, string, json.RawMessage) (*RPCHandler, error) {
		t.Fatal("discovery bootstrapped dependencies")
		return nil, nil
	})
	id := json.RawMessage(`1`)
	resp, ok := h.Handle(context.Background(), request{JSONRPC: "2.0", ID: &id, Method: "tools/list"})
	if !ok || resp.Error != nil {
		t.Fatal("discovery unavailable")
	}
	var listed struct {
		Tools []toolDefinition `json:"tools"`
	}
	if json.Unmarshal(resp.Result, &listed) != nil {
		t.Fatal("discovery result malformed")
	}
	found := 0
	for _, d := range listed.Tools {
		if _, ok := observationToolDefinition(d.Name); !ok {
			continue
		}
		found++
		if d.InputSchema.AdditionalProperties == nil || *d.InputSchema.AdditionalProperties || !d.Annotations["readOnlyHint"] || d.Annotations["destructiveHint"] || !d.Annotations["idempotentHint"] || d.Annotations["openWorldHint"] {
			t.Fatal("observation discovery lost strict read-only hints")
		}
		if d.Name == "service_metrics" && len(d.InputSchema.Properties["name"].Enum) != len(observability.MetricCatalog()) {
			t.Fatal("metric discovery lost finite catalog")
		}
		if d.Name == "service_events" {
			for key, values := range observability.FilterValues() {
				if !reflect.DeepEqual(d.InputSchema.Properties[key].Enum, values) {
					t.Fatal("event discovery diverged from admitted filter catalog")
				}
			}
		}
	}
	if found != 3 {
		t.Fatal("normal discovery lost observation tools")
	}
}

func TestMCPObservationHTTPSSEMinimalParity(t *testing.T) {
	client, reader := observationMCPFixture(t)
	h := NewMinimalRPCHandler(StartupDiagnostic{ErrorClass: "schema_incompatible"})
	h.SetServiceClient(func() (*servicectl.RPCClient, error) { return client, nil })
	transport := NewHTTPSSETransport(h, ServerConfig{SessionID: func() string { return "minimal-observation" }})
	server := httptest.NewServer(transport.Handler())
	defer server.Close()
	sse, endpoint, messages := openSSE(t, server.URL+"/sse")
	defer sse.Body.Close()
	postJSON(t, server.URL+endpoint, map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
	listed := readSSEMessage(t, messages)
	var result struct {
		Tools []toolDefinition `json:"tools"`
	}
	if listed.Error != nil || json.Unmarshal(listed.Result, &result) != nil {
		t.Fatal("minimal HTTP discovery failed")
	}
	assertMinimalObservationTools(t, result.Tools)
	assertObservationMCPParity(t, reader, func(name, args string) *response {
		postJSON(t, server.URL+endpoint, map[string]any{"jsonrpc": "2.0", "id": name, "method": "tools/call", "params": map[string]any{"name": name, "arguments": json.RawMessage(args)}})
		resp := readSSEMessage(t, messages)
		return &resp
	})
}

func TestMCPObservationMissingAndOldDaemonExplicitHandoffs(t *testing.T) {
	for _, mode := range []string{"missing", "old"} {
		h := NewRPCHandler(nil)
		if mode == "missing" {
			h.SetServiceClient(func() (*servicectl.RPCClient, error) {
				return &servicectl.RPCClient{Network: "mem", Address: "absent-observation-daemon"}, nil
			})
		} else {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal("old daemon fixture unavailable")
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(time.Second))
				var req servicectl.RPCRequest
				if json.NewDecoder(conn).Decode(&req) == nil {
					_ = json.NewEncoder(conn).Encode(servicectl.RPCResponse{JSONRPC: "2.0", ID: req.ID, Error: &servicectl.RPCError{Code: -32000, Message: "synthetic-private-old-method"}})
				}
			}()
			t.Cleanup(func() {
				_ = listener.Close()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("old daemon fixture did not join")
				}
			})
			h.SetServiceClient(func() (*servicectl.RPCClient, error) {
				return &servicectl.RPCClient{Network: "tcp", Address: listener.Addr().String()}, nil
			})
		}
		resp := mcpObservationCall(t, h, "service_observability", `{}`)
		wantCode, action := "service_unavailable", "explicitly start"
		if mode == "old" {
			wantCode, action = "unsupported_capability", "explicitly update"
		}
		if resp.Error == nil || resp.Error.Data.Code != wantCode || !strings.Contains(resp.Error.Data.Remediation, action) {
			t.Fatal("observation failure lost explicit non-mutating handoff")
		}
		data, _ := json.Marshal(resp)
		if strings.Contains(string(data), "synthetic-private") {
			t.Fatal("observation echoed old daemon body")
		}
	}
}
