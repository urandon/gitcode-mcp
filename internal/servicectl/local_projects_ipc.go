package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"strings"
	"time"

	"gitcode-mcp/internal/localgit"
	"gitcode-mcp/internal/service"
)

const localProjectBudget = 10 * time.Second
const localProjectResponseBytes = 64 << 20 // bounded escaped JSON over the G1 8 MiB source cap.

func localProjectMethod(method string) bool { return strings.HasPrefix(method, "LocalProjects.") }

func (s RPCServer) dispatchLocalProject(ctx context.Context, method string, raw []byte) (any, error) {
	if s.LocalProjects == nil {
		return nil, localHostError("unavailable")
	}
	if method == "Service.Status" || method == "Service.Health" || method == "LocalProjects.Status" {
		if len(raw) > 0 && strictObservationResponse(raw, &struct{}{}) != nil {
			return nil, localHostError("invalid_argument")
		}
		status, err := s.LocalProjects.Status(ctx)
		if err != nil {
			return nil, err
		}
		if method == "Service.Health" {
			healthy := true
			for _, p := range status.Projects {
				healthy = healthy && p.Readable
			}
			min, max := s.Manager.schemaRange()
			return ServiceHealth{Status: StatusRunning, Healthy: healthy, CheckedAt: time.Now().UTC(), BinaryVersion: s.Manager.Version, BinaryCommit: s.Manager.Commit, SchemaMin: min, SchemaMax: max}, nil
		}
		if method == "Service.Status" {
			return struct {
				Status   string                       `json:"status"`
				Running  bool                         `json:"running"`
				Mode     string                       `json:"mode"`
				Projects []service.LocalProjectStatus `json:"projects"`
			}{StatusRunning, true, "local", status.Projects}, nil
		}
		return status, nil
	}
	switch method {
	case "LocalProjects.Get", "LocalProjects.List", "LocalProjects.History":
		var q service.LocalProjectQuery
		if strictObservationResponse(raw, &q) != nil {
			return nil, localHostError("invalid_argument")
		}
		switch method {
		case "LocalProjects.Get":
			return s.LocalProjects.Get(ctx, q)
		case "LocalProjects.List":
			return s.LocalProjects.List(ctx, q)
		default:
			return s.LocalProjects.History(ctx, q)
		}
	case "LocalProjects.Mutate":
		var q service.LocalProjectMutation
		if strictObservationResponse(raw, &q) != nil {
			return nil, localHostError("invalid_argument")
		}
		return s.LocalProjects.Mutate(ctx, q)
	default:
		return nil, localHostError("unsupported_capability")
	}
}

func (s RPCServer) handleLocalProjectConn(ctx context.Context, conn net.Conn, raw []byte) {
	var req RPCRequest
	valid := strictObservationResponse(raw, &req) == nil
	id, ok := req.ID.(float64)
	if !valid || !ok || id < 1 || id > 9007199254740991 || math.Trunc(id) != id {
		_ = json.NewEncoder(conn).Encode(RPCResponse{JSONRPC: jsonrpcVersion, Error: &RPCError{Code: -32602, Message: "invalid local project request", DiagnosticCode: "local_git_invalid_argument"}})
		return
	}
	requestCtx, cancel := context.WithTimeout(ctx, localProjectBudget)
	defer cancel()
	deadline, _ := requestCtx.Deadline()
	_ = conn.SetDeadline(deadline)
	// Generic RPCClient closes the connection on caller cancellation. Observe
	// EOF while the typed operation runs, then close/join this one reader.
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer cancel()
		var buffer [256]byte
		for remaining := 4096; remaining > 0; {
			n, err := conn.Read(buffer[:])
			remaining -= n
			for _, b := range buffer[:n] {
				if b != ' ' && b != '\n' && b != '\r' && b != '\t' {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() { _ = conn.Close(); <-done }()
	resp := s.handleRequest(requestCtx, req)
	encoded, err := json.Marshal(resp)
	if err != nil || len(encoded) > localProjectResponseBytes {
		encoded, _ = json.Marshal(RPCResponse{JSONRPC: jsonrpcVersion, ID: req.ID, Error: &RPCError{Code: -32000, Message: "local project response exceeds limit", DiagnosticCode: "local_git_limit_exceeded"}})
	}
	_, _ = conn.Write(append(encoded, '\n'))
}

// Local calls reuse the existing private endpoint/framing, but have their own
// source-size budget and sanitize transport errors instead of exposing paths.
func (c *RPCClient) callLocalProject(ctx context.Context, method string, params any, result any) error {
	ctx, cancel := context.WithTimeout(ctx, localProjectBudget)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	network, address := c.Network, c.Address
	if network == "" {
		network = "unix"
	}
	if address == "" {
		address = c.SocketPath
	}
	if network != "unix" && network != "mem" {
		return localHostError("private_ipc_required")
	}
	body, err := json.Marshal(params)
	if err != nil {
		return localHostError("invalid_argument")
	}
	if string(body) == "null" {
		body = []byte("{}")
	}
	id := c.nextID.Add(1)
	req := RPCRequest{JSONRPC: jsonrpcVersion, ID: id, Method: method, Params: body}
	encoded, err := json.Marshal(req)
	if err != nil || len(encoded) > maxRPCRequestBytes {
		return localHostError("limit_exceeded")
	}
	var response []byte
	sent := false
	failure := func() error {
		if method == "LocalProjects.Mutate" && sent {
			if ctx.Err() != nil {
				return errors.Join(localHostError("publication_ambiguous"), ctx.Err())
			}
			return localHostError("publication_ambiguous")
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return localHostError("unavailable")
	}
	if network == "mem" {
		memoryRPC.Lock()
		server, ok := memoryRPC.servers[address]
		memoryRPC.Unlock()
		if !ok {
			return localHostError("unavailable")
		}
		sent = true
		response, err = json.Marshal(server.handleRequest(ctx, req))
	} else {
		var dialer net.Dialer
		conn, dialErr := dialer.DialContext(ctx, "unix", address)
		if dialErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return localHostError("unavailable")
		}
		defer conn.Close()
		stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
		defer stop()
		deadline, _ := ctx.Deadline()
		_ = conn.SetDeadline(deadline)
		sent = true
		if _, err = conn.Write(append(encoded, '\n')); err == nil {
			response, err = io.ReadAll(io.LimitReader(conn, localProjectResponseBytes+1))
		}
	}
	if err != nil {
		return failure()
	}
	if ctx.Err() != nil {
		return failure()
	}
	if len(response) > localProjectResponseBytes {
		return localHostError("limit_exceeded")
	}
	var resp observationRPCResponse
	if strictObservationResponse(response, &resp) != nil || resp.JSONRPC != jsonrpcVersion || string(resp.ID) != string(mustLocalID(id)) {
		if method == "LocalProjects.Mutate" {
			return failure()
		}
		return localHostError("invalid_response")
	}
	if resp.Error != nil {
		if !strings.HasPrefix(resp.Error.DiagnosticCode, "local_git_") {
			return failure()
		}
		code := strings.TrimPrefix(resp.Error.DiagnosticCode, "local_git_")
		switch code {
		case "invalid_argument", "invalid_configuration", "invalid_schema", "unsupported_schema", "unavailable", "not_found", "unsupported_store", "limit_exceeded", "history_unavailable", "write_conflict", "writer_busy", "idempotency_conflict", "publication_ambiguous", "publication_rejected", "unknown_project", "read_only", "private_ipc_required", "service_busy", "unsupported_capability", "invalid_response":
		default:
			return failure()
		}
		return &localgit.Error{Code: code, Field: "service"} // never reflect peer error body.
	}
	if len(resp.Result) == 0 || string(resp.Result) == "null" {
		if method == "LocalProjects.Mutate" {
			return failure()
		}
		return localHostError("invalid_response")
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(resp.Result, result); err != nil {
		if method == "LocalProjects.Mutate" {
			return failure()
		}
		return localHostError("invalid_response")
	}
	return nil
}
func mustLocalID(id int64) []byte { b, _ := json.Marshal(id); return b }
