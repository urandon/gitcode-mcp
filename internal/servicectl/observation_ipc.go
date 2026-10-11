package servicectl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"reflect"
	"strconv"
	"strings"

	"gitcode-mcp/internal/observability"
)

const observationEnvelopeBytes = 4 << 10
const maxRPCRequestBytes = 1 << 20

// Existing JSON-stream RPC framing is retained; each decoded value is capped.
// Observation envelopes additionally use the smaller published request budget.
func strictObservationEnvelope(raw []byte) error {
	var req RPCRequest
	if err := strictObservationResponse(raw, &req); err != nil {
		return err
	}
	switch req.ID.(type) {
	case string, float64:
	default:
		return observationDomain("invalid_query")
	}
	return nil
}

type ObservationCapabilities struct {
	Protocol string                   `json:"protocol"`
	Methods  []string                 `json:"methods"`
	Limits   observability.ReadLimits `json:"limits"`
}

func observationMethod(method string) bool {
	switch method {
	case "Observability.Capabilities", "Observability.Summary", "Observability.Metrics", "Observability.Events":
		return true
	}
	return false
}

func observationParams(method string, raw []byte) (any, error) {
	switch method {
	case "Observability.Capabilities", "Observability.Summary":
		err := observability.DecodeReadRequest(raw, &struct{}{})
		return struct{}{}, err
	case "Observability.Metrics":
		return observability.DecodeMetricsRequest(raw)
	case "Observability.Events":
		return observability.DecodeEventsRequest(raw)
	}
	return nil, observationDomain("unsupported_capability")
}

func (s RPCServer) dispatchObservation(ctx context.Context, method string, raw []byte) (any, error) {
	params, err := observationParams(method, raw)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, observability.ReadBudget)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, observationTransportError(ctx, err)
	}
	if method == "Observability.Capabilities" {
		return ObservationCapabilities{observability.Protocol, []string{"Observability.Summary", "Observability.Metrics", "Observability.Events"}, observability.Limits()}, nil
	}
	reader := s.Observability
	if reader == nil {
		reader = &observability.Reader{Collector: s.Manager.observation, Metrics: s.Manager.metrics, Health: s.Maintenance.ExecutionHealthContext}
	}
	var result any
	switch method {
	case "Observability.Summary":
		result, err = reader.Summary(ctx)
	case "Observability.Metrics":
		result, err = reader.MetricSnapshot(ctx, params.(observability.MetricsRequest))
	case "Observability.Events":
		result, err = reader.Events(ctx, params.(observability.EventsRequest))
	}
	if err != nil && ctx.Err() != nil {
		return nil, observationTransportError(ctx, err)
	}
	return result, err
}

// callObservation owns one deadline covering capability negotiation and the
// query. It never starts a daemon, discovers cache contents, or resolves a
// credential; even established silent sockets are cancelled and bounded.
func (c *RPCClient) callObservation(ctx context.Context, method string, params any, result any) error {
	var raw []byte
	if params != nil {
		var err error
		raw, err = json.Marshal(params)
		if err != nil {
			return observationDomain("invalid_query")
		}
	}
	normalized, err := observationParams(method, raw)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, observability.ReadBudget)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return observationTransportError(ctx, err)
	}
	if method != "Observability.Capabilities" {
		var capabilities ObservationCapabilities
		if err := c.observationRoundTrip(ctx, "Observability.Capabilities", struct{}{}, &capabilities, observationEnvelopeBytes); err != nil {
			return err
		}
		if capabilities.Protocol != observability.Protocol || capabilities.Limits != observability.Limits() {
			return observationDomain("unsupported_capability")
		}
		found := false
		seen := map[string]bool{}
		if len(capabilities.Methods) != 3 {
			return observationDomain("unsupported_capability")
		}
		for _, m := range capabilities.Methods {
			if m == method {
				found = true
			}
			if !observationMethod(m) || m == "Observability.Capabilities" || seen[m] {
				return observationDomain("unsupported_capability")
			}
			seen[m] = true
		}
		if !found {
			return observationDomain("unsupported_capability")
		}
	}
	limit := observability.MaxSummaryBytes
	if method == "Observability.Metrics" {
		limit = observability.MaxMetricsBytes
	}
	if method == "Observability.Events" {
		limit = observability.MaxPageBytes
	}
	if method == "Observability.Capabilities" {
		limit = observationEnvelopeBytes
	}
	return c.observationRoundTrip(ctx, method, normalized, result, limit+observationEnvelopeBytes)
}

type observationRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *RPCError       `json:"error"`
}

func (c *RPCClient) observationRoundTrip(ctx context.Context, method string, params any, result any, limit int) error {
	if err := ctx.Err(); err != nil {
		return observationTransportError(ctx, err)
	}
	network := c.Network
	if network == "" {
		network = "unix"
	}
	address := c.Address
	if address == "" {
		address = c.SocketPath
	}
	if address == "" {
		return observationDomain("service_unavailable")
	}
	id := c.nextID.Add(1)
	body, err := json.Marshal(params)
	if err != nil || len(body) > observability.MaxReadRequestBytes {
		return observationDomain("invalid_query")
	}
	req := RPCRequest{JSONRPC: jsonrpcVersion, ID: id, Method: method, Params: body}
	var data []byte
	if network == "mem" {
		if !memoryRPC.TryLock() {
			return observationDomain("observation_busy")
		}
		server, ok := memoryRPC.servers[address]
		memoryRPC.Unlock()
		if !ok {
			return observationDomain("service_unavailable")
		}
		resp := server.handleRequest(ctx, req)
		data, err = json.Marshal(resp)
	} else {
		var dialer net.Dialer
		conn, dialErr := dialer.DialContext(ctx, network, address)
		if dialErr != nil {
			return observationTransportError(ctx, dialErr)
		}
		defer conn.Close()
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stop()
		deadline, _ := ctx.Deadline()
		if err := conn.SetDeadline(deadline); err != nil {
			return observationTransportError(ctx, err)
		}
		encoded, err := json.Marshal(req)
		if err != nil || len(encoded) > observability.MaxReadRequestBytes+observationEnvelopeBytes {
			return observationDomain("invalid_query")
		}
		if _, err := io.Copy(conn, bytes.NewReader(append(encoded, '\n'))); err != nil {
			return observationTransportError(ctx, err)
		}
		bounded := &io.LimitedReader{R: conn, N: int64(limit) + 1}
		d := json.NewDecoder(bounded)
		var raw json.RawMessage
		if err := d.Decode(&raw); err != nil {
			if bounded.N == 0 {
				return observationDomain("response_limit")
			}
			return observationTransportError(ctx, err)
		}
		if d.InputOffset() > int64(limit) {
			return observationDomain("response_limit")
		}
		tail, _ := io.ReadAll(d.Buffered())
		if len(bytes.TrimSpace(tail)) != 0 {
			return observationDomain("invalid_response")
		}
		data = raw
	}
	if err != nil || len(data) > limit {
		return observationDomain("response_limit")
	}
	if err := ctx.Err(); err != nil {
		return observationTransportError(ctx, err)
	}
	var response observationRPCResponse
	if strictObservationResponse(data, &response) != nil || response.JSONRPC != jsonrpcVersion || string(response.ID) != strconv.FormatInt(id, 10) || response.Error != nil && len(response.Result) > 0 {
		return observationDomain("invalid_response")
	}
	if response.Error != nil {
		if method == "Observability.Capabilities" && response.Error.DiagnosticCode == "" {
			return observationDomain("unsupported_capability")
		}
		if q := response.Error.Observation; q != nil && q.Code == response.Error.DiagnosticCode && validObservationQueryError(*q) {
			return *q
		}
		return observationDomain(response.Error.DiagnosticCode)
	}
	if len(response.Result) == 0 || bytes.Equal(response.Result, []byte("null")) {
		return observationDomain("invalid_response")
	}
	if result != nil && strictObservationResponse(response.Result, result) != nil {
		return observationDomain("invalid_response")
	}
	return nil
}

func strictObservationResponse(data []byte, dst any) error {
	if err := uniqueObservationJSON(data, reflect.TypeOf(dst)); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	var tail any
	if err := d.Decode(&tail); err != io.EOF {
		return errors.New("invalid observation response")
	}
	return nil
}

func validObservationQueryError(q observability.QueryError) bool {
	switch q.Code + ":" + q.Reason {
	case "invalid_query:unsupported_request", "invalid_query:unsupported_filter_or_limit", "invalid_query:unsupported_metric_or_window",
		"invalid_cursor:malformed", "invalid_cursor:filter_changed", "invalid_cursor:foreign_or_modified", "invalid_cursor:future_position",
		"snapshot_required:initializing", "snapshot_required:generation_or_loss_changed", "snapshot_required:retention_expired",
		"observation_busy:ledger_busy", "observation_busy:metrics_busy",
		"observation_unavailable:reader_unavailable", "observation_unavailable:ledger_unavailable", "observation_unavailable:metrics_unavailable", "observation_unavailable:response_limit":
		return true
	}
	return false
}

func uniqueObservationJSON(data []byte, schema reflect.Type) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	var value func(int, reflect.Type) error
	invalid := errors.New("invalid observation JSON")
	fieldsByType := map[reflect.Type]map[string]reflect.Type{}
	var fields func(reflect.Type) map[string]reflect.Type
	fields = func(typ reflect.Type) map[string]reflect.Type {
		if known, ok := fieldsByType[typ]; ok {
			return known
		}
		out := map[string]reflect.Type{}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if f.PkgPath != "" {
				continue
			}
			name := strings.SplitN(f.Tag.Get("json"), ",", 2)[0]
			if name == "-" {
				continue
			}
			if f.Anonymous && name == "" && f.Type.Kind() == reflect.Struct {
				for key, child := range fields(f.Type) {
					out[key] = child
				}
				continue
			}
			if name == "" {
				name = f.Name
			}
			out[name] = f.Type
		}
		fieldsByType[typ] = out
		return out
	}
	value = func(depth int, typ reflect.Type) error {
		if depth > 16 {
			return invalid
		}
		for typ != nil && typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		// Raw JSON envelopes are checked for duplicate keys/depth here, then
		// validated against their actual DTO in the next decode boundary.
		if typ == reflect.TypeOf(json.RawMessage{}) {
			typ = nil
		}
		t, err := d.Token()
		if err != nil {
			return invalid
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			var allowed map[string]reflect.Type
			if typ != nil && typ.Kind() == reflect.Struct {
				allowed = fields(typ)
			}
			seen := map[string]bool{}
			for d.More() {
				token, err := d.Token()
				key, ok := token.(string)
				if err != nil || !ok || seen[key] {
					return invalid
				}
				seen[key] = true
				var child reflect.Type
				if allowed != nil {
					var ok bool
					child, ok = allowed[key]
					if !ok {
						return invalid
					}
				} else if typ != nil && typ.Kind() == reflect.Map {
					child = typ.Elem()
				}
				if err := value(depth+1, child); err != nil {
					return err
				}
			}
			if end, err := d.Token(); err != nil || end != json.Delim('}') {
				return invalid
			}
		case '[':
			var child reflect.Type
			if typ != nil && (typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array) {
				child = typ.Elem()
			}
			for d.More() {
				if err := value(depth+1, child); err != nil {
					return err
				}
			}
			if end, err := d.Token(); err != nil || end != json.Delim(']') {
				return invalid
			}
		default:
			return invalid
		}
		return nil
	}
	if err := value(0, schema); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return invalid
	}
	return nil
}

func observationTransportError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) {
		return observationDomain("observation_cancelled")
	}
	var timeout net.Error
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
		return observationDomain("observation_timeout")
	}
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "dial" {
		return observationDomain("service_unavailable")
	}
	return observationDomain("invalid_response")
}

func observationDomain(code string) RPCDomainError {
	message := "local observation response is unavailable"
	switch code {
	case "unsupported_capability":
		message = "daemon lacks observability.v1; explicitly update the daemon before retrying"
	case "service_unavailable":
		message = "local daemon is unavailable; explicitly start it before retrying"
	case "invalid_query", "invalid_cursor":
		message = "observation request is invalid"
	case "snapshot_required":
		message = "observation snapshot is required; refresh without a cursor"
	case "observation_busy":
		message = "local observation snapshot is busy; retry the read"
	case "observation_timeout":
		message = "local observation read timed out"
	case "observation_cancelled":
		message = "local observation read was cancelled"
	case "response_limit", "observation_unavailable", "invalid_response":
	default:
		code = "invalid_response"
	}
	return RPCDomainError{Code: code, Message: strings.TrimSpace(message)}
}
