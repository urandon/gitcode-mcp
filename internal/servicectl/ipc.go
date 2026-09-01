package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gitcode-mcp/internal/adminhttp"
	"gitcode-mcp/internal/cache"
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
	Code           int    `json:"code"`
	Message        string `json:"message"`
	DiagnosticCode string `json:"diagnostic_code,omitempty"`
}

type RPCDomainError struct {
	Message string
	Code    string
}

func (e RPCDomainError) Error() string          { return e.Message }
func (e RPCDomainError) DiagnosticCode() string { return e.Code }

type JobListResult struct {
	Jobs              []Job              `json:"jobs"`
	CacheReadiness    string             `json:"cache_readiness,omitempty"`
	CacheSchemaBlocks []CacheSchemaBlock `json:"cache_schema_blocks,omitempty"`
}

type CacheSchemaBlock struct {
	RegistrationID      string `json:"registration_id,omitempty"`
	RepoID              string `json:"repo_id,omitempty"`
	CacheUUID           string `json:"cache_uuid,omitempty"`
	DetectedVersion     int    `json:"detected_schema_version"`
	ExpectedVersion     int    `json:"expected_schema_version"`
	DaemonBinaryVersion string `json:"daemon_binary_version,omitempty"`
	DaemonBinaryCommit  string `json:"daemon_binary_commit,omitempty"`
	DaemonSchemaMin     int    `json:"daemon_schema_min,omitempty"`
	DaemonSchemaMax     int    `json:"daemon_schema_max,omitempty"`
	QuiesceState        string `json:"quiesce_state,omitempty"`
}

type ServiceHealth struct {
	Status            string             `json:"status"`
	Healthy           bool               `json:"healthy"`
	CheckedAt         time.Time          `json:"checked_at"`
	Message           string             `json:"message,omitempty"`
	BinaryVersion     string             `json:"binary_version,omitempty"`
	BinaryCommit      string             `json:"binary_commit,omitempty"`
	SchemaMin         int                `json:"schema_min"`
	SchemaMax         int                `json:"schema_max"`
	CacheReadiness    string             `json:"cache_readiness,omitempty"`
	CacheSchemaBlocks []CacheSchemaBlock `json:"cache_schema_blocks,omitempty"`
}

type RPCServer struct {
	Manager     Manager
	Jobs        *JobManager
	Maintenance *MaintenanceManager
	Admin       *adminhttp.Controller
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
		var coded interface{ DiagnosticCode() string }
		if errors.As(err, &coded) {
			resp.Error.DiagnosticCode = coded.DiagnosticCode()
		}
		return resp
	}
	resp.Result = result
	return resp
}

func (s RPCServer) dispatch(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case "Service.Status":
		return s.serviceStatus(ctx)
	case "Service.Health":
		return s.health(ctx)
	case "Service.Doctor":
		return s.Manager.Doctor()
	case "Admin.Status":
		if s.Admin == nil {
			return adminhttp.Status{}, errors.New("admin controller is unavailable")
		}
		return s.Admin.Status(), nil
	case "Admin.Open":
		if s.Admin == nil {
			return adminhttp.OpenResult{}, errors.New("admin controller is unavailable")
		}
		var request adminhttp.OpenRequest
		if err := json.Unmarshal(params, &request); err != nil {
			return adminhttp.OpenResult{}, err
		}
		return s.Admin.Open(ctx, request)
	case "Jobs.StartFake":
		var req StartFakeJobRequest
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, err
			}
		}
		return s.Jobs.StartFake(context.Background(), req)
	case "Jobs.StartRAGIndex":
		var req StartRAGIndexJobRequest
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, err
			}
		}
		return s.Jobs.StartRAGIndex(context.Background(), s.Manager, req)
	case "Jobs.StartRepositoryDocsIndex":
		var req StartRepositoryDocsIndexJobRequest
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, err
			}
		}
		if s.Maintenance == nil {
			return nil, RepositoryDocsSourceUnavailableError{code: "repository_docs_registration_unavailable"}
		}
		if strings.TrimSpace(req.RegistrationID) != "" && strings.TrimSpace(req.RepositoryPath) == "" {
			source, err := s.Maintenance.repositoryDocsSourceForSelector(RepositoryDocsSourceSelector{
				RegistrationID: req.RegistrationID, SourceRegistrationID: req.SourceRegistrationID,
				SourceRegistrationGeneration: req.SourceRegistrationGeneration,
			})
			if err != nil {
				return nil, err
			}
			if !repositoryDocsSourceMatchesRepo(ctx, source, req.RepoID) {
				return nil, RepositoryDocsSourceUnavailableError{code: "repository_docs_source_repo_conflict"}
			}
			req.RepoID, req.RepositoryPath, req.Profile = source.RepoID, source.RepositoryPath, source.Profile
			req.CachePath, req.CacheUUID = source.CachePath, source.CacheUUID
		}
		prepared, err := prepareRepositoryDocsIndex(ctx, s.Manager, req)
		if err != nil {
			return nil, err
		}
		entry, prepared, registered, registerErr := s.Maintenance.registerAndRecordRepositoryDocsAdmission(prepared)
		if registerErr != nil {
			return nil, registerErr
		}
		if !registered || entry.RepositoryDocs == nil || entry.RepositoryDocs.SourceRegistrationID == "" || entry.RepositoryDocs.SourceRegistrationGeneration <= 0 {
			return nil, RepositoryDocsSourceUnavailableError{code: "repository_docs_registration_unavailable"}
		}
		job, err := s.Jobs.startPreparedRepositoryDocsIndex(context.Background(), s.Manager, prepared)
		if err != nil {
			// The durable admission remains queued in the maintenance registry;
			// reconciliation can retry it after a transient writer or snapshot
			// persistence failure without re-accepting private authority.
			return nil, err
		}
		if err := s.Maintenance.bindRepositoryDocsAdmissionJob(prepared.request.RegistrationID, prepared.request.SourceRegistrationID, job.ID); err != nil {
			return nil, err
		}
		// Keep the durable handoff until reconciliation observes a successful
		// terminal job. It is the exact recovery source if the daemon exits
		// after queueing or while the job is running.
		return job, nil
	case "RepositoryDocs.RegisterSource":
		var req RegisterRepositoryDocsSourceRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return s.registerRepositoryDocsSource(ctx, req)
	case "RepositoryDocs.RebindSource":
		if s.Maintenance == nil {
			return nil, RepositoryDocsSourceUnavailableError{code: "repository_docs_registration_unavailable"}
		}
		var req RepositoryDocsSourceRebindRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		selectorGeneration := req.ExpectedGeneration
		if strings.TrimSpace(req.SourceRegistrationID) == "" {
			selectorGeneration = 0
		}
		source, err := s.Maintenance.repositoryDocsSourceForSelector(RepositoryDocsSourceSelector{RegistrationID: req.RegistrationID, SourceRegistrationID: req.SourceRegistrationID, SourceRegistrationGeneration: selectorGeneration})
		if err != nil {
			return nil, err
		}
		if !repositoryDocsSourceMatchesRepo(ctx, source, req.RepoID) {
			return nil, RepositoryDocsSourceUnavailableError{code: "repository_docs_source_repo_conflict"}
		}
		return s.Maintenance.RebindRepositoryDocsSource(ctx, req)
	case "RepositoryDocs.Policy", "RepositoryDocs.Plan", "RepositoryDocs.Status", "RepositoryDocs.Search":
		var req RepositoryDocsQueryRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		switch method {
		case "RepositoryDocs.Policy":
			return s.repositoryDocsPolicy(ctx, req)
		case "RepositoryDocs.Plan":
			return s.repositoryDocsPlan(ctx, req)
		case "RepositoryDocs.Status":
			return s.repositoryDocsStatus(ctx, req)
		default:
			return s.repositoryDocsSearch(ctx, req)
		}
	case "Jobs.StartSync":
		var req StartSyncJobRequest
		if len(params) > 0 {
			if err := json.Unmarshal(params, &req); err != nil {
				return nil, err
			}
		}
		return s.Jobs.StartSync(context.Background(), s.Manager, req)
	case "Jobs.List":
		blocks, err := s.cacheSchemaBlocks(ctx)
		if err != nil {
			return nil, err
		}
		result := JobListResult{Jobs: s.Jobs.List(), CacheSchemaBlocks: blocks}
		if len(blocks) > 0 {
			result.CacheReadiness = "cache_schema_blocked"
		}
		return result, nil
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
		job, ok, cancelErr := s.Jobs.Cancel(id)
		if cancelErr != nil {
			return nil, cancelErr
		}
		if !ok {
			return nil, fmt.Errorf("job not found: %s", id)
		}
		return job, nil
	case "Maintenance.Enroll":
		var req MaintenanceEnrollRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return s.Maintenance.Enroll(ctx, req)
	case "Maintenance.Capabilities":
		return maintenanceCapabilities(s.Manager.Version), nil
	case "Maintenance.List":
		return s.Maintenance.List(ctx)
	case "Maintenance.Reconcile":
		return s.Maintenance.Reconcile(ctx)
	case "Maintenance.ReconcileRegistration":
		var req MaintenanceRegistrationRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return s.Maintenance.ReconcileRegistration(ctx, req.RegistrationID)
	case "Maintenance.ResolveConfig":
		var req MaintenanceResolveConfigRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return s.Maintenance.ResolveConfig(req)
	case "Maintenance.Disable":
		var req MaintenanceRegistrationRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		return s.Maintenance.Disable(ctx, req.RegistrationID)
	default:
		return nil, fmt.Errorf("unknown method: %s", method)
	}
}

func (s RPCServer) health(ctx context.Context) (ServiceHealth, error) {
	status, err := s.serviceStatus(ctx)
	if err != nil {
		return ServiceHealth{}, err
	}
	health := ServiceHealth{Status: status.Status, Healthy: status.Status == StatusRunning && status.CacheReadiness != "cache_schema_blocked", CheckedAt: time.Now().UTC(), BinaryVersion: s.Manager.Version, BinaryCommit: s.Manager.Commit, SchemaMin: cache.CurrentSchemaVersion(), SchemaMax: cache.CurrentSchemaVersion(), CacheReadiness: status.CacheReadiness, CacheSchemaBlocks: status.CacheSchemaBlocks}
	if !health.Healthy {
		health.Message = status.Message
	}
	return health, nil
}

func (s RPCServer) serviceStatus(ctx context.Context) (Status, error) {
	status, err := s.Manager.Status()
	if err != nil {
		return Status{}, err
	}
	blocks, err := s.cacheSchemaBlocks(ctx)
	if err != nil {
		return Status{}, err
	}
	status.CacheSchemaBlocks = blocks
	if len(blocks) > 0 {
		status.CacheReadiness = "cache_schema_blocked"
		status.Message = "one or more managed caches require a compatible service binary before writers can resume"
	}
	return status, nil
}

func (s RPCServer) cacheSchemaBlocks(ctx context.Context) ([]CacheSchemaBlock, error) {
	if s.Maintenance == nil {
		return nil, nil
	}
	result, err := s.Maintenance.List(ctx)
	if err != nil {
		return nil, err
	}
	blocks := make([]CacheSchemaBlock, 0)
	for _, entry := range result.Entries {
		if entry.State != "cache_schema_blocked" {
			continue
		}
		blocks = append(blocks, CacheSchemaBlock{
			RegistrationID: entry.RegistrationID, RepoID: entry.RepoID, CacheUUID: entry.CacheUUID,
			DetectedVersion: entry.DetectedSchemaVersion, ExpectedVersion: entry.ExpectedSchemaVersion,
			DaemonBinaryVersion: entry.DaemonBinaryVersion, DaemonBinaryCommit: entry.DaemonBinaryCommit,
			DaemonSchemaMin: cache.CurrentSchemaVersion(), DaemonSchemaMax: cache.CurrentSchemaVersion(), QuiesceState: entry.QuiesceState,
		})
	}
	return blocks, nil
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
		if resp.Error.DiagnosticCode != "" {
			return RPCDomainError{Message: resp.Error.Message, Code: resp.Error.DiagnosticCode}
		}
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
		if resp.Error.DiagnosticCode != "" {
			return RPCDomainError{Message: resp.Error.Message, Code: resp.Error.DiagnosticCode}
		}
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
