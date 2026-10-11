package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/observability"
)

func observationTestPeer(t *testing.T, handler func(net.Conn, RPCRequest)) *RPCClient {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("local IPC fixture unavailable")
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	connections := map[net.Conn]bool{}
	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			connections[conn] = true
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				defer func() { mu.Lock(); delete(connections, conn); mu.Unlock() }()
				var req RPCRequest
				if json.NewDecoder(conn).Decode(&req) != nil {
					return
				}
				handler(conn, req)
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-acceptDone
		mu.Lock()
		for c := range connections {
			_ = c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return &RPCClient{Network: "tcp", Address: listener.Addr().String()}
}

func observationFixtureCapabilities() ObservationCapabilities {
	return ObservationCapabilities{observability.Protocol, []string{"Observability.Summary", "Observability.Metrics", "Observability.Events"}, observability.Limits()}
}
func writeObservationResponse(conn net.Conn, req RPCRequest, value any) {
	_ = json.NewEncoder(conn).Encode(RPCResponse{JSONRPC: jsonrpcVersion, ID: req.ID, Result: value})
}
func requireObservationDomain(t *testing.T, err error, code string) {
	t.Helper()
	var domain RPCDomainError
	if !errors.As(err, &domain) || domain.Code != code || strings.Contains(err.Error(), "synthetic-private") {
		t.Fatal("fixed observation domain error missing or unsafe")
	}
}

func TestObservationIPCStrictRequestsBeforeConnectAndReader(t *testing.T) {
	var calls atomic.Int64
	client := observationTestPeer(t, func(conn net.Conn, req RPCRequest) {
		calls.Add(1)
		writeObservationResponse(conn, req, observationFixtureCapabilities())
	})
	for _, params := range []any{map[string]any{"unknown": "synthetic-private"}, map[string]any{"limit": 0}, map[string]any{"source_ref": "synthetic-private"}, map[string]any{"outcome": "succeeded", "failure_class": "timeout"}} {
		if err := client.Call(context.Background(), "Observability.Events", params, &observability.Page{}); err == nil {
			t.Fatal("invalid query connected to daemon")
		}
	}
	readerCalls := 0
	s := RPCServer{Observability: &observability.Reader{Health: func(context.Context, time.Time) (observability.ExecutionHealth, error) {
		readerCalls++
		return observability.ExecutionHealth{}, nil
	}}}
	for _, raw := range []string{`{"limit":50,"limit":1}`, `{"limit":null}`, `{"source_ref":"synthetic-private"}`, `{} {}`} {
		if _, err := s.dispatch(context.Background(), "Observability.Events", []byte(raw)); err == nil {
			t.Fatal("invalid query accessed reader")
		}
	}
	if _, err := s.dispatch(context.Background(), "Observability.Summary", []byte(`{"path":"synthetic-private"}`)); err == nil {
		t.Fatal("summary accepted arbitrary lookup")
	}
	if calls.Load() != 0 || readerCalls != 0 {
		t.Fatal("validation did not precede connection and observation")
	}
}

func TestObservationIPCSameReaderParityAndCursorReasons(t *testing.T) {
	c := maintenanceEventCollector(t)
	sealJobObservation(t, c)
	at := time.Unix(1700000000, 0).UTC()
	m := observability.NewMetrics(c.BootID(), at)
	reader := &observability.Reader{Collector: c, Metrics: m, Now: func() time.Time { return at }}
	s := RPCServer{Observability: reader}
	client := observationTestPeer(t, func(conn net.Conn, req RPCRequest) {
		_ = json.NewEncoder(conn).Encode(s.handleRequest(context.Background(), req))
	})
	expected, err := reader.Summary(context.Background())
	if err != nil {
		t.Fatal("shared summary fixture unavailable")
	}
	var summary observability.Summary
	if err := client.Call(context.Background(), "Observability.Summary", nil, &summary); err != nil || !reflect.DeepEqual(summary, expected) {
		t.Fatal("IPC summary diverged from shared reader")
	}
	expectedMetrics, err := reader.MetricSnapshot(context.Background(), observability.MetricsRequest{HistoryMinutes: 3})
	if err != nil {
		t.Fatal("metric fixture unavailable")
	}
	var metrics observability.MetricsSnapshot
	if err := client.Call(context.Background(), "Observability.Metrics", observability.MetricsRequest{HistoryMinutes: 3}, &metrics); err != nil || !reflect.DeepEqual(metrics, expectedMetrics) {
		t.Fatal("IPC metrics diverged from shared reader")
	}
	var page observability.Page
	if err := client.Call(context.Background(), "Observability.Events", map[string]any{"limit": 1}, &page); err != nil || len(page.Events) != 1 {
		t.Fatal("IPC event fixture unavailable")
	}
	var query observability.QueryError
	err = client.Call(context.Background(), "Observability.Events", map[string]any{"cursor": page.Cursor, "component": "jobs"}, &page)
	if !errors.As(err, &query) || query.Code != "invalid_cursor" || query.Reason != "filter_changed" {
		t.Fatal("typed cursor reason lost")
	}
}

func TestObservationIPCSilentEstablishedPeerCancellation(t *testing.T) {
	var closed atomic.Int64
	client := observationTestPeer(t, func(conn net.Conn, req RPCRequest) { var b [1]byte; _, _ = conn.Read(b[:]); closed.Add(1) })
	for i := 0; i < 8; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		err := client.Call(ctx, "Observability.Summary", nil, &observability.Summary{})
		cancel()
		requireObservationDomain(t, err, "observation_timeout")
	}
	deadline := time.Now().Add(time.Second)
	for closed.Load() != 8 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if closed.Load() != 8 {
		t.Fatal("silent established observations retained sockets/workers")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	requireObservationDomain(t, client.Call(ctx, "Observability.Summary", nil, &observability.Summary{}), "observation_cancelled")
}

func TestObservationIPCSingleBudgetCoversNegotiationAndQuery(t *testing.T) {
	var queryReached atomic.Bool
	client := observationTestPeer(t, func(conn net.Conn, req RPCRequest) {
		if req.Method != "Observability.Capabilities" {
			queryReached.Store(true)
		}
		time.Sleep(1100 * time.Millisecond)
		if req.Method == "Observability.Capabilities" {
			writeObservationResponse(conn, req, observationFixtureCapabilities())
			return
		}
		writeObservationResponse(conn, req, observability.Summary{Protocol: observability.Protocol, Schema: 1})
	})
	start := time.Now()
	err := client.Call(context.Background(), "Observability.Summary", nil, &observability.Summary{})
	requireObservationDomain(t, err, "observation_timeout")
	if elapsed := time.Since(start); elapsed > observability.ReadBudget+500*time.Millisecond || elapsed < observability.ReadBudget-100*time.Millisecond {
		t.Fatal("negotiation/query did not share one bounded deadline")
	}
	// The query is already received before its reply delay; cleanup joins the
	// fixed peer worker rather than leaving a timed-out goroutine behind.
	if !queryReached.Load() {
		t.Fatal("fixture did not exercise query after negotiation")
	}
}

func TestObservationIPCOldMissingAndMalformedPeer(t *testing.T) {
	for _, mode := range []string{"old", "wrong_protocol", "wrong_id", "duplicate_key", "raw_error", "oversize", "unexpected_method"} {
		t.Run(mode, func(t *testing.T) {
			client := observationTestPeer(t, func(conn net.Conn, req RPCRequest) {
				switch mode {
				case "old":
					_ = json.NewEncoder(conn).Encode(RPCResponse{JSONRPC: jsonrpcVersion, ID: req.ID, Error: &RPCError{Code: -32000, Message: "synthetic-private-obsolete-method"}})
				case "wrong_protocol":
					cap := observationFixtureCapabilities()
					cap.Protocol = "old"
					writeObservationResponse(conn, req, cap)
				case "wrong_id":
					_ = json.NewEncoder(conn).Encode(RPCResponse{JSONRPC: jsonrpcVersion, ID: 9999, Result: observationFixtureCapabilities()})
				case "duplicate_key":
					_, _ = io.WriteString(conn, `{"jsonrpc":"2.0","jsonrpc":"2.0","id":1,"result":{}}`)
				case "raw_error":
					_ = json.NewEncoder(conn).Encode(RPCResponse{JSONRPC: jsonrpcVersion, ID: req.ID, Error: &RPCError{Code: -32000, Message: "synthetic-private-error", DiagnosticCode: "synthetic-private-code"}})
				case "oversize":
					_, _ = io.WriteString(conn, `{"jsonrpc":"2.0","id":1,"result":"`+strings.Repeat("x", 2*observationEnvelopeBytes)+`"}`)
				case "unexpected_method":
					cap := observationFixtureCapabilities()
					cap.Methods = append(cap.Methods, "Service.Start")
					writeObservationResponse(conn, req, cap)
				}
			})
			expected := "invalid_response"
			if mode == "old" || mode == "wrong_protocol" || mode == "unexpected_method" {
				expected = "unsupported_capability"
			}
			if mode == "oversize" {
				expected = "response_limit"
			}
			requireObservationDomain(t, client.Call(context.Background(), "Observability.Summary", nil, &observability.Summary{}), expected)
		})
	}
	client := &RPCClient{Network: "mem", Address: "synthetic-private-missing-daemon"}
	requireObservationDomain(t, client.Call(context.Background(), "Observability.Summary", nil, &observability.Summary{}), "service_unavailable")
}

func TestObservationIPCServerStrictEnvelopeBeforeReader(t *testing.T) {
	var reads atomic.Int64
	s := RPCServer{Observability: &observability.Reader{Health: func(context.Context, time.Time) (observability.ExecutionHealth, error) {
		reads.Add(1)
		return observability.ExecutionHealth{}, nil
	}}}
	for _, raw := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"Observability.Summary","method":"Observability.Summary","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"method":"Observability.Summary","unknown":"synthetic-private","params":{}}`,
		`{"jsonrpc":"2.0","id":{},"method":"Observability.Summary","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"method":"Observability.Summary","params":{"unknown":"` + strings.Repeat("x", observability.MaxReadRequestBytes) + `"}}`,
	} {
		client, server := net.Pipe()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { defer close(done); s.handleConn(ctx, server) }()
		_ = client.SetDeadline(time.Now().Add(time.Second))
		if _, err := io.WriteString(client, raw+"\n"); err != nil {
			t.Error("request fixture could not be sent")
		}
		var response RPCResponse
		if json.NewDecoder(client).Decode(&response) != nil || response.Error == nil || response.Error.DiagnosticCode != "invalid_query" {
			t.Error("invalid wire envelope reached the reader or lost its fixed error")
		}
		cancel()
		_ = client.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("strict envelope handler did not unwind")
		}
		if reads.Load() != 0 {
			t.Fatal("invalid wire request reached retained health reader")
		}
	}
}

func TestObservationIPCServerPipeliningAndShutdownJoin(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("IPC fixture unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- (RPCServer{}).Serve(ctx, listener) }()
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal("IPC fixture connection unavailable")
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))
	request := `{"jsonrpc":"2.0","id":1,"method":"Observability.Capabilities","params":{}}` + "\n"
	if _, err := io.WriteString(conn, request+request+request); err != nil {
		t.Fatal("pipeline fixture send failed")
	}
	d := json.NewDecoder(conn)
	for i := 0; i < 3; i++ {
		var response RPCResponse
		if d.Decode(&response) != nil || response.Error != nil || response.Result == nil {
			t.Fatal("persistent decoder lost a pipelined request")
		}
	}
	// Leave an accepted socket blocked in its next decode. Serve must join that
	// handler on cancellation, not just close the listener and return.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("cancelled server returned an operational failure")
		}
	case <-time.After(time.Second):
		t.Fatal("IPC cancellation failed to join established handler")
	}
	var b [1]byte
	if _, err := conn.Read(b[:]); err == nil {
		t.Fatal("joined server retained established socket")
	}
}

func TestObservationIPCFullSampledMetricHistoryExceedsLegacyLimit(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	m := observability.NewMetrics("0123456789abcdef0123456789abcdef", at.Add(-time.Hour))
	for i := 0; i < observability.MetricHistoryMinutes; i++ {
		if !m.SampleAt(at.Add(time.Duration(i-59)*time.Minute), map[string]observability.ProbeValue{"runtime_heap": observability.Available(float64(i + 1))}) {
			t.Fatal("metric history fixture failed")
		}
	}
	r := &observability.Reader{Metrics: m, Now: func() time.Time { return at }}
	want, err := r.MetricSnapshot(context.Background(), observability.MetricsRequest{HistoryMinutes: 60})
	if err != nil {
		t.Fatal("full history fixture unavailable")
	}
	b, err := json.Marshal(want)
	if err != nil || len(b) <= 64<<10 || len(b) > observability.MaxMetricsBytes {
		t.Fatal("full history fixture does not exercise bounded large response")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal("large response fixture unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- (RPCServer{Observability: r}).Serve(ctx, listener) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("large response server did not join")
		}
	}()
	client := &RPCClient{Network: "tcp", Address: listener.Addr().String()}
	var got observability.MetricsSnapshot
	if err := client.Call(context.Background(), "Observability.Metrics", observability.MetricsRequest{HistoryMinutes: 60}, &got); err != nil {
		t.Fatalf("actual IPC full history failed: %v", err)
	}
	// Internal aggregate bookkeeping is not part of the wire DTO. Compare all
	// serialized fields, including every bucket and unavailable value.
	gotJSON, _ := json.Marshal(got)
	if string(gotJSON) != string(b) {
		t.Fatal("actual IPC truncated or changed serialized full history")
	}
}
