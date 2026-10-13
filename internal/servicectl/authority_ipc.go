package servicectl

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"reflect"
	"strconv"
	"strings"
	"time"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority"
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
	switch method {
	case "AuthorityV1.Negotiate":
		var r authority.NegotiationRequest
		if err := authority.DecodeJSON(raw, &r); err != nil {
			return nil, err
		}
		return r, r.Validate()
	case "AuthorityV1.Read":
		var r authority.ReadRequest
		if err := authority.DecodeJSON(raw, &r); err != nil {
			return nil, err
		}
		return r, r.Validate()
	case "AuthorityV1.Mutate":
		var r authority.MutationRequest
		if err := authority.DecodeJSON(raw, &r); err != nil {
			return nil, err
		}
		return r, r.Validate()
	case "AuthorityV1.MutateDocument":
		var r authority.DocumentMutationRequest
		if err := authority.DecodeJSON(raw, &r); err != nil {
			return nil, err
		}
		return r, r.Validate()
	case "AuthorityV1.KnowledgeStatus":
		var r authorityProjectRequest
		if err := authority.DecodeJSON(raw, &r); err != nil {
			return nil, err
		}
		if !r.ProjectID.Valid() {
			return nil, authority.Error{Code: authority.InvalidContract}
		}
		return r, nil
	case "AuthorityV1.Search":
		var r localauthority.SearchRequest
		if err := authority.DecodeJSON(raw, &r); err != nil {
			return nil, err
		}
		return r, r.Validate()
	case "AuthorityV1.EnqueueRecovery":
		var r localauthority.RecoveryJobRequest
		if err := authority.DecodeJSON(raw, &r); err != nil {
			return nil, err
		}
		return r, r.Validate()
	case "AuthorityV1.RecoveryJob":
		var r localauthority.RecoveryJobLookup
		if err := authority.DecodeJSON(raw, &r); err != nil {
			return nil, err
		}
		return r, r.Validate()
	case "AuthorityV1.RecoveryJobs":
		var r localauthority.RecoveryJobsRequest
		if err := authority.DecodeJSON(raw, &r); err != nil {
			return nil, err
		}
		return r, r.Validate()
	case "AuthorityV1.CancelRecovery", "AuthorityV1.RetryRecovery":
		var r localauthority.RecoveryJobAction
		if err := authority.DecodeJSON(raw, &r); err != nil {
			return nil, err
		}
		return r, r.Validate()
	case "AuthorityV1.EnqueueIndex":
		var r localauthority.IndexJobRequest
		if err := authority.DecodeJSON(raw, &r); err != nil {
			return nil, err
		}
		return r, r.Validate()
	case "AuthorityV1.IndexJob":
		var r localauthority.IndexJobLookup
		if err := authority.DecodeJSON(raw, &r); err != nil {
			return nil, err
		}
		return r, r.Validate()
	case "AuthorityV1.IndexJobs":
		var r localauthority.IndexJobsRequest
		if err := authority.DecodeJSON(raw, &r); err != nil {
			return nil, err
		}
		return r, r.Validate()
	case "AuthorityV1.CancelIndex", "AuthorityV1.RetryIndex":
		var r localauthority.IndexJobAction
		if err := authority.DecodeJSON(raw, &r); err != nil {
			return nil, err
		}
		return r, r.Validate()
	default:
		return nil, authority.Error{Code: authority.Unsupported}
	}
}

func (s RPCServer) dispatchAuthority(ctx context.Context, method string, raw []byte) (any, error) {
	if ctx.Err() != nil {
		return nil, authority.Error{Code: authority.Unavailable}
	}
	if _, err := authorityParams(method, raw); err != nil {
		return nil, err
	}
	if method == "AuthorityV1.Negotiate" {
		return authority.ContractsOnly(), nil
	}
	// L1 defines contracts but owns no authority registry/storage/runtime. Never
	// route a UUID through remote bindings or admit a job as an implementation.
	return nil, authority.Error{Code: authority.NoAuthority}
}

func strictAuthorityEnvelope(raw []byte) error {
	var r RPCRequest
	if err := authority.DecodeJSON(raw, &r); err != nil {
		return err
	}
	id, ok := r.ID.(float64)
	if !ok || id < 1 || id > 1<<53 || id != float64(int64(id)) {
		return authority.Error{Code: authority.InvalidContract}
	}
	return nil
}

func (c *RPCClient) callAuthority(ctx context.Context, method string, params any, result any) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return authority.Error{Code: authority.InvalidContract}
	}
	normalized, err := authorityParams(method, raw)
	if err != nil {
		return err
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
		if reflect.DeepEqual(caps, authority.ContractsOnly()) {
			return authority.Error{Code: authority.Unavailable}
		}
		if !reflect.DeepEqual(caps, authorityRuntimeCapabilities()) {
			return authority.Error{Code: authority.Unsupported}
		}
		var raw json.RawMessage
		if err := c.authorityRoundTrip(ctx, method, normalized, &raw); err != nil {
			return err
		}
		validated, err := authorityRuntimeResult(method, normalized, raw)
		if err != nil {
			return err
		}
		if result != nil {
			body, _ := json.Marshal(validated)
			return replaceAuthorityResult(body, result)
		}
		return nil
	}
	var caps authority.Negotiation
	if err := c.authorityRoundTrip(ctx, method, normalized, &caps); err != nil {
		return err
	}
	if !reflect.DeepEqual(caps, authority.ContractsOnly()) && !reflect.DeepEqual(caps, authorityRuntimeCapabilities()) {
		return authority.Error{Code: authority.Unsupported}
	}
	if result != nil {
		data, _ := json.Marshal(caps)
		if err := replaceAuthorityResult(data, result); err != nil {
			return err
		}
	}
	return nil
}

func (c *RPCClient) authorityRoundTrip(ctx context.Context, method string, params any, result any) error {
	address := c.Address
	if address == "" {
		address = c.SocketPath
	}
	if address == "" {
		return authority.Error{Code: authority.Unavailable}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	id := c.nextID.Add(1)
	body, err := json.Marshal(params)
	if err != nil {
		return authority.Error{Code: authority.InvalidContract}
	}
	req := RPCRequest{JSONRPC: jsonrpcVersion, ID: id, Method: method, Params: body}
	encoded, err := json.Marshal(req)
	if err != nil || len(encoded) > authority.MaxContractBytes {
		return authority.Error{Code: authority.InvalidContract}
	}
	var data []byte
	if c.Network == "mem" {
		if !memoryRPC.TryLock() {
			return authority.Error{Code: authority.Unavailable}
		}
		s, ok := memoryRPC.servers[address]
		memoryRPC.Unlock()
		if !ok {
			return authority.Error{Code: authority.Unavailable}
		}
		data, err = json.Marshal(s.handleRequest(ctx, req))
	} else {
		var dialer net.Dialer
		conn, dialErr := dialer.DialContext(ctx, "unix", address)
		if dialErr != nil {
			return authoritySocketError(ctx, dialErr)
		}
		defer conn.Close()
		if err := sameAuthorityUser(conn); err != nil {
			return err
		}
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
		switch code {
		case authority.InvalidContract, authority.Unavailable, authority.Unsupported, authority.NoAuthority, authority.PolicyDenied, authority.Conflict, authority.RevisionConflict, authority.InitializationRequired:
			return authority.Error{Code: code}
		default:
			switch storage := localauthority.Code(reply.Error.DiagnosticCode); storage {
			case localauthority.ReadOnly, localauthority.Corrupt, localauthority.DurabilityUnavailable, localauthority.Capacity, localauthority.StorageUnavailable, localauthority.CommitUncertain, localauthority.InitializationPending, localauthority.RefConflict, localauthority.KnowledgeMigrationRequired, localauthority.KnowledgeSetupRequired, localauthority.KnowledgeInitializationPending, localauthority.DocumentPending, localauthority.DocumentRecoveryRequired, localauthority.KnowledgeCorrupt:
				return localauthority.Error{Code: storage}
			case localauthority.JobsCorrupt, localauthority.JobsUncertain, localauthority.JobNotFound, localauthority.JobRetryLimit:
				return localauthority.Error{Code: storage}
			case localauthority.ProjectionCorrupt, localauthority.ProjectionUncertain, localauthority.IndexJobsCorrupt, localauthority.IndexJobsUncertain, localauthority.IndexJobNotFound:
				return localauthority.Error{Code: storage}
			default:
				return authority.Error{Code: authority.Unsupported}
			}
		}
	}
	return replaceAuthorityResult(reply.Result, result)
}

// Responses are complete snapshots, not merge patches into a caller's prior
// result. Decode/validate a fresh value before replacing the destination: an
// omitted optional code/cursor/receipt must not survive an earlier response.
func replaceAuthorityResult(body []byte, result any) error {
	v := reflect.ValueOf(result)
	if !v.IsValid() || v.Kind() != reflect.Pointer || v.IsNil() {
		return authority.Error{Code: authority.InvalidContract}
	}
	fresh := reflect.New(v.Elem().Type())
	if err := authority.DecodeJSON(body, fresh.Interface()); err != nil {
		return err
	}
	v.Elem().Set(fresh.Elem())
	return nil
}

func authoritySocketError(ctx context.Context, err error) error {
	if cause := rpcCallerError(ctx, err); cause == context.Canceled || cause == context.DeadlineExceeded {
		return cause
	}
	return authority.Error{Code: authority.Unavailable}
}
