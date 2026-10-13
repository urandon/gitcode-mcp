package servicectl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/privateipc"
)

const authorityResponseBytes = 512 << 10
const authorityCallBudget = 10 * time.Second

type authorityRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

func authorityMethod(method string) bool { return strings.HasPrefix(method, "AuthorityV1.") }

func authorityParams(method string, raw []byte) (any, error) {
	if !authorityMethod(method) {
		return nil, authority.Error{Code: authority.Unsupported}
	}
	return authority.DecodeRuntimeRequest(strings.TrimPrefix(method, "AuthorityV1."), raw)
}

func (s RPCServer) dispatchAuthority(ctx context.Context, method string, raw []byte) (any, error) {
	if ctx.Err() != nil {
		return nil, authority.Error{Code: authority.Unavailable}
	}
	if _, err := authorityParams(method, raw); err != nil {
		return nil, err
	}
	if s.Local != nil {
		if !s.qualifiedLocal {
			return nil, authority.Error{Code: authority.PolicyDenied}
		}
		return s.Local.Dispatch(ctx, strings.TrimPrefix(method, "AuthorityV1."), raw)
	}
	if method == "AuthorityV1.Negotiate" {
		return authority.ContractsOnly(), nil
	}
	// L1 defines contracts but owns no authority registry/storage/runtime. Never
	// route a UUID through remote bindings or admit a job as an implementation.
	return nil, authority.Error{Code: authority.NoAuthority}
}

func strictAuthorityEnvelope(raw []byte) error {
	var r struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params,omitempty"`
	}
	if err := authority.DecodeJSON(raw, &r); err != nil {
		return err
	}
	id, err := strconv.ParseUint(string(r.ID), 10, 64)
	if err != nil || id < 1 || id > (1<<53)-1 || strconv.FormatUint(id, 10) != string(r.ID) || r.JSONRPC != jsonrpcVersion || !authorityMethod(r.Method) {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

// NewLocalRPCClient takes a trusted machine enrollment coordinate, not a tool
// argument or an environment override. Dial requalifies the owned endpoint and
// same-UID peer every time; this never creates, repairs or starts a runtime.
func NewLocalRPCClient(directory string) (*RPCClient, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || len(directory) > 4096 {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	return &RPCClient{localDirectory: directory}, nil
}

func (c *RPCClient) callAuthority(ctx context.Context, method string, params any, result any) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	local := c.localDirectory != ""
	if local && (c.Network != "" || c.Address != "" || c.SocketPath != "") {
		return authority.Error{Code: authority.PolicyDenied}
	}
	if params == nil || reflect.ValueOf(params).Kind() == reflect.Pointer && reflect.ValueOf(params).IsNil() {
		return authority.Error{Code: authority.InvalidContract}
	}
	if validated, ok := params.(interface{ Validate() error }); ok {
		if err := validated.Validate(); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return authority.Error{Code: authority.InvalidContract}
	}
	normalized, err := authorityParams(method, raw)
	if err != nil {
		return err
	}
	if local && method == "AuthorityV1.Mutate" {
		return authority.Error{Code: authority.Unsupported}
	}
	if local || method == "AuthorityV1.Negotiate" {
		if err := authorityDestination(method, normalized, result); err != nil {
			return err
		}
	}
	network := c.Network
	if network == "" {
		network = "unix"
	}
	// Authority control never uses TCP even when a separate loopback Admin or
	// embedding policy is allowed. mem is a deterministic fixture transport.
	if network != "unix" && network != "mem" {
		return authority.Error{Code: authority.PolicyDenied}
	}
	ctx, cancel := context.WithTimeout(ctx, authorityCallBudget)
	defer cancel()
	if method != "AuthorityV1.Negotiate" {
		var caps authority.Negotiation
		err := c.authorityRoundTrip(ctx, "AuthorityV1.Negotiate", authority.NegotiationRequest{Protocol: authority.Protocol, Authority: authority.Local, SchemaMin: 1, SchemaMax: 1}, &caps)
		if err != nil {
			return err
		}
		if !caps.Available {
			if !reflect.DeepEqual(caps, authority.ContractsOnly()) {
				return authority.Error{Code: authority.Unsupported}
			}
			return authority.Error{Code: authority.Unavailable}
		}
		if !local || !caps.Supports(strings.TrimPrefix(method, "AuthorityV1.")) {
			return authority.Error{Code: authority.Unsupported}
		}
		return c.authorityRoundTrip(ctx, method, normalized, result)
	}
	var caps authority.Negotiation
	if err := c.authorityRoundTrip(ctx, method, normalized, &caps); err != nil {
		return err
	}
	if !local && !reflect.DeepEqual(caps, authority.ContractsOnly()) {
		return authority.Error{Code: authority.Unsupported}
	}
	if result != nil {
		data, _ := json.Marshal(caps)
		if err := authority.DecodeJSON(data, result); err != nil {
			return err
		}
	}
	return nil
}

func (c *RPCClient) authorityRoundTrip(ctx context.Context, method string, params any, result any) error {
	address := c.Address
	if address == "" && c.localDirectory == "" {
		address = c.SocketPath
	}
	if address == "" && c.localDirectory == "" {
		return authority.Error{Code: authority.Unavailable}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	id := c.nextID.Add(1)
	if id < 1 || id > (1<<53)-1 {
		return authority.Error{Code: authority.Capacity}
	}
	body, err := json.Marshal(params)
	if err != nil {
		return authority.Error{Code: authority.InvalidContract}
	}
	req := RPCRequest{JSONRPC: jsonrpcVersion, ID: id, Method: method, Params: body}
	encoded, err := json.Marshal(req)
	if err != nil || len(encoded)+1 > authority.MaxContractBytes {
		return authority.Error{Code: authority.InvalidContract}
	}
	var data []byte
	if c.Network == "mem" {
		if c.localDirectory != "" {
			return authority.Error{Code: authority.PolicyDenied}
		}
		if !memoryRPC.TryLock() {
			return authority.Error{Code: authority.Unavailable}
		}
		s, ok := memoryRPC.servers[address]
		memoryRPC.Unlock()
		if !ok {
			return authority.Error{Code: authority.Unavailable}
		}
		if s.Local != nil {
			return authority.Error{Code: authority.PolicyDenied}
		}
		data, err = json.Marshal(s.handleRequest(ctx, req))
	} else {
		var conn net.Conn
		var dialErr error
		if c.localDirectory != "" {
			conn, dialErr = privateipc.Dial(ctx, c.localDirectory)
		} else {
			var dialer net.Dialer
			conn, dialErr = dialer.DialContext(ctx, "unix", address)
		}
		if dialErr != nil {
			var code authority.Error
			if errors.As(dialErr, &code) {
				return code
			}
			return authoritySocketError(ctx, dialErr)
		}
		defer conn.Close()
		done := make(chan struct{})
		stop := context.AfterFunc(ctx, func() { _ = conn.Close(); close(done) })
		defer func() {
			if !stop() {
				<-done
			}
		}()
		deadline, _ := ctx.Deadline()
		if err := conn.SetDeadline(deadline); err != nil {
			return authoritySocketError(ctx, err)
		}
		if _, err := io.Copy(conn, bytes.NewReader(append(encoded, '\n'))); err != nil {
			return authoritySocketError(ctx, err)
		}
		limited := &io.LimitedReader{R: conn, N: authorityResponseBytes + 1}
		decoder := json.NewDecoder(limited)
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			if limited.N == 0 {
				return authority.Error{Code: authority.InvalidContract}
			}
			return authoritySocketError(ctx, err)
		}
		if decoder.InputOffset() > authorityResponseBytes {
			return authority.Error{Code: authority.InvalidContract}
		}
		tail, _ := io.ReadAll(decoder.Buffered())
		if len(bytes.TrimSpace(tail)) != 0 {
			return authority.Error{Code: authority.InvalidContract}
		}
		data = raw
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || len(data) > authorityResponseBytes {
		return authority.Error{Code: authority.InvalidContract}
	}
	var reply authorityRPCResponse
	if authority.DecodeJSON(data, &reply) != nil || reply.JSONRPC != jsonrpcVersion || string(reply.ID) != strconv.FormatInt(id, 10) || reply.Error != nil && len(reply.Result) > 0 {
		return authority.Error{Code: authority.InvalidContract}
	}
	if reply.Error != nil {
		// Old daemons or unknown codes are unsupported, never raw peer errors.
		code := authority.Code(reply.Error.DiagnosticCode)
		if authority.KnownCode(code) {
			return authority.Error{Code: code}
		}
		return authority.Error{Code: authority.Unsupported}
	}
	if _, err := checkedAuthorityResult(method, params, reply.Result); err != nil {
		return err
	}
	return authorityResultCopy(reply.Result, result)
}

func authoritySocketError(ctx context.Context, err error) error {
	if cause := rpcCallerError(ctx, err); cause == context.Canceled || cause == context.DeadlineExceeded {
		return cause
	}
	return authority.Error{Code: authority.Unavailable}
}
