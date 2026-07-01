package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

const jsonrpcVersion = "2.0"

type RPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type RPCResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *RPCError `json:"error,omitempty"`
}

type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type JobListResult struct {
	Jobs []Job `json:"jobs"`
}

type ServiceHealth struct {
	Status    string    `json:"status"`
	Healthy   bool      `json:"healthy"`
	CheckedAt time.Time `json:"checked_at"`
	Message   string    `json:"message,omitempty"`
}

type RPCServer struct {
	Manager Manager
	Jobs    *JobManager
}

type RPCClient struct {
	Network    string
	Address    string
	SocketPath string
	nextID     atomic.Int64
}

var memoryRPC = struct {
	sync.Mutex
	servers map[string]RPCServer
}{servers: map[string]RPCServer{}}

func serveMemoryRPC(ctx context.Context, address string, server RPCServer) error {
	if address == "" {
		return errors.New("memory service address is required")
	}
	memoryRPC.Lock()
	memoryRPC.servers[address] = server
	memoryRPC.Unlock()
	<-ctx.Done()
	memoryRPC.Lock()
	delete(memoryRPC.servers, address)
	memoryRPC.Unlock()
	return ctx.Err()
}

func (s RPCServer) Serve(ctx context.Context, listener net.Listener) error {
	errCh := make(chan error, 1)
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
					errCh <- nil
				default:
					errCh <- err
				}
				return
			}
			go s.handleConn(ctx, conn)
		}
	}()
	return <-errCh
}

func (s RPCServer) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)
	for {
		var req RPCRequest
		if err := dec.Decode(&req); err != nil {
			return
		}
		resp := s.handleRequest(ctx, req)
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}

func (s RPCServer) handleRequest(ctx context.Context, req RPCRequest) RPCResponse {
	resp := RPCResponse{JSONRPC: jsonrpcVersion, ID: req.ID}
	if req.JSONRPC != jsonrpcVersion {
		resp.Error = &RPCError{Code: -32600, Message: "invalid jsonrpc version"}
		return resp
	}
	result, err := s.dispatch(ctx, req.Method, req.Params)
	if err != nil {
		resp.Error = &RPCError{Code: -32000, Message: err.Error()}
		return resp
	}
	resp.Result = result
	return resp
}

func (s RPCServer) dispatch(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "Service.Status":
		return s.Manager.Status()
	case "Service.Health":
		return s.health(ctx)
	case "Service.Doctor":
		return s.Manager.Doctor()
	case "Jobs.StartFake":
		var req StartFakeJobRequest
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, err
			}
		}
		return s.Jobs.StartFake(context.Background(), req)
	case "Jobs.List":
		return JobListResult{Jobs: s.Jobs.List()}, nil
	case "Jobs.Get":
		id, err := decodeJobID(params)
		if err != nil {
			return nil, err
		}
		job, ok := s.Jobs.Get(id)
		if !ok {
			return nil, fmt.Errorf("job not found: %s", id)
		}
		return job, nil
	case "Jobs.Cancel":
		id, err := decodeJobID(params)
		if err != nil {
			return nil, err
		}
		job, ok := s.Jobs.Cancel(id)
		if !ok {
			return nil, fmt.Errorf("job not found: %s", id)
		}
		return job, nil
	default:
		return nil, fmt.Errorf("unknown method: %s", method)
	}
}

func (s RPCServer) health(ctx context.Context) (ServiceHealth, error) {
	status, err := s.Manager.Status()
	if err != nil {
		return ServiceHealth{}, err
	}
	health := ServiceHealth{Status: status.Status, Healthy: status.Status == StatusRunning, CheckedAt: time.Now().UTC()}
	if !health.Healthy {
		health.Message = status.Message
	}
	return health, nil
}

func decodeJobID(params json.RawMessage) (string, error) {
	var req struct {
		JobID string `json:"job_id"`
		ID    string `json:"id"`
	}
	if len(params) == 0 {
		return "", errors.New("job_id is required")
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return "", err
	}
	id := firstNonEmpty(req.JobID, req.ID)
	if id == "" {
		return "", errors.New("job_id is required")
	}
	return id, nil
}

func (c *RPCClient) Call(ctx context.Context, method string, params any, result any) error {
	network := c.Network
	if network == "" {
		network = "unix"
	}
	address := c.Address
	if address == "" {
		address = c.SocketPath
	}
	if address == "" {
		return errors.New("service address is required")
	}
	if network == "mem" {
		return c.callMemory(ctx, address, method, params, result)
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, network, address)
	if err != nil {
		return err
	}
	defer conn.Close()
	id := c.nextID.Add(1)
	req := RPCRequest{JSONRPC: jsonrpcVersion, ID: id, Method: method}
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req.Params = data
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return err
	}
	var resp RPCResponse
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return err
	}
	if resp.Error != nil {
		return errors.New(resp.Error.Message)
	}
	if result == nil {
		return nil
	}
	data, err := json.Marshal(resp.Result)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, result)
}

func (c *RPCClient) callMemory(ctx context.Context, address, method string, params any, result any) error {
	memoryRPC.Lock()
	server, ok := memoryRPC.servers[address]
	memoryRPC.Unlock()
	if !ok {
		return fmt.Errorf("service memory endpoint not found: %s", address)
	}
	id := c.nextID.Add(1)
	req := RPCRequest{JSONRPC: jsonrpcVersion, ID: id, Method: method}
	if params != nil {
		data, err := json.Marshal(params)
		if err != nil {
			return err
		}
		req.Params = data
	}
	resp := server.handleRequest(ctx, req)
	if resp.Error != nil {
		return errors.New(resp.Error.Message)
	}
	if result == nil {
		return nil
	}
	data, err := json.Marshal(resp.Result)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, result)
}
