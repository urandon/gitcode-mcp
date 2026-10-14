package servicectl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority"
)

const authorityHandlerLimit = 32

// AuthorityServer is a separate graph, NOT ordinary RPCServer/Manager.Run.
// No remote maintenance, cache, Admin, provider setup or credentials are held.
// Trusted bootstrap owns the listener and prepared coordinator; peers receive
// no filesystem/FD/config selectors. Native launch qualification remains L4.
type AuthorityServer struct{ core *localauthority.Coordinator }

func NewAuthorityServer(core *localauthority.Coordinator) (*AuthorityServer, error) {
	if !core.Available() {
		return nil, authority.Error{Code: authority.Unavailable}
	}
	return &AuthorityServer{core: core}, nil
}

func authorityRuntimeCapabilities() authority.Negotiation {
	return authority.Negotiation{Protocol: authority.Protocol, SchemaMin: 1, SchemaMax: 1, Authority: authority.Local, Available: true, Methods: []string{"AuthorityV1.Read", "AuthorityV1.Mutate", "AuthorityV1.MutateDocument", "AuthorityV1.KnowledgeStatus", "AuthorityV1.Search", "AuthorityV1.EnqueueRecovery", "AuthorityV1.RecoveryJob", "AuthorityV1.RecoveryJobs", "AuthorityV1.CancelRecovery", "AuthorityV1.RetryRecovery", "AuthorityV1.EnqueueIndex", "AuthorityV1.IndexJob", "AuthorityV1.IndexJobs", "AuthorityV1.CancelIndex", "AuthorityV1.RetryIndex", "AuthorityV1.PlanProjectionRepair", "AuthorityV1.ApplyProjectionRepair", "AuthorityV1.ProjectionRepair", "AuthorityV1.ProjectionRepairs", "AuthorityV1.RuntimeHealth"}}
}

type authorityProjectRequest struct {
	ProjectID authority.UUID `json:"project_id"`
}

// Serve requires an existing private Unix listener. It never selects a home,
// creates a directory, starts Admin, accepts TCP or initializes authority.
// It owns/join-closes this listener and its bounded handlers, not the core.
func (s *AuthorityServer) Serve(ctx context.Context, l *net.UnixListener) error {
	if s == nil || !s.core.Available() || l == nil {
		return authority.Error{Code: authority.Unavailable}
	}
	path := l.Addr().String()
	identity, err := privateAuthoritySocket(path)
	if err != nil {
		return err
	}
	serving, err := s.core.AdmitServing(ctx)
	if err != nil {
		return err
	}
	defer serving.Close()
	l.SetUnlinkOnClose(false)
	ctx, cancel := context.WithCancel(ctx)
	var handlers sync.WaitGroup
	slots := make(chan struct{}, authorityHandlerLimit)
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { serving.Seal(); _ = l.Close(); close(closed) })
	defer func() {
		cancel()
		if !stop() {
			<-closed
		}
		_ = l.Close()
		handlers.Wait()
		// Do not unlink a replacement left by another owner during shutdown.
		if current, e := os.Lstat(path); e == nil && os.SameFile(identity, current) {
			_ = os.Remove(path)
		}
	}()
	for {
		conn, err := l.AcceptUnix()
		if err != nil {
			serving.Seal()
			if ctx.Err() != nil {
				return nil
			}
			return authority.Error{Code: authority.Unavailable}
		}
		select {
		case slots <- struct{}{}:
			handlers.Add(1)
			go func() { defer handlers.Done(); defer func() { <-slots }(); s.handle(ctx, conn) }()
		default:
			_ = conn.Close()
		}
	}
}

func privateAuthoritySocket(path string) (os.FileInfo, error) {
	if !filepath.IsAbs(path) || len(path) > 100 {
		return nil, authority.Error{Code: authority.PolicyDenied}
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 || parent.Mode().Perm()&0077 != 0 || authorityOwnedPath(filepath.Dir(path), false) != nil {
		return nil, authority.Error{Code: authority.PolicyDenied}
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSocket == 0 || info.Mode().Perm()&0077 != 0 || authorityOwnedPath(path, true) != nil {
		return nil, authority.Error{Code: authority.PolicyDenied}
	}
	return info, nil
}

func (s *AuthorityServer) handle(ctx context.Context, conn *net.UnixConn) {
	defer conn.Close()
	if sameAuthorityUser(conn) != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, authorityCallBudget)
	defer cancel()
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = conn.Close(); close(closed) })
	defer func() {
		if !stop() {
			<-closed
		}
	}()
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		return
	}
	limited := &io.LimitedReader{R: conn, N: authority.MaxContractBytes + 1}
	decoder := json.NewDecoder(limited)
	var raw json.RawMessage
	if decoder.Decode(&raw) != nil || len(raw) > authority.MaxContractBytes || decoder.InputOffset() > authority.MaxContractBytes {
		return
	}
	// One request per connection: reject already buffered extra values rather
	// than retaining an unbounded pipelined stream/worker per peer.
	if tail, _ := io.ReadAll(decoder.Buffered()); len(bytes.TrimSpace(tail)) != 0 {
		return
	}
	response := RPCResponse{JSONRPC: jsonrpcVersion, Error: &RPCError{Code: -32602, Message: "invalid local authority contract", DiagnosticCode: string(authority.InvalidContract)}}
	responseLimit := authorityResponseBytes
	if strictAuthorityEnvelope(raw) == nil {
		var req RPCRequest
		if authority.DecodeJSON(raw, &req) == nil && req.JSONRPC == jsonrpcVersion {
			responseLimit = authorityResponseLimit(req.Method)
			response.ID = req.ID
			result, err := s.dispatch(ctx, req.Method, req.Params)
			if err != nil {
				response.Error = authorityRuntimeError(err)
			} else {
				response.Error = nil
				response.Result = result
			}
		}
	}
	body, err := json.Marshal(response)
	if err != nil || len(body) > responseLimit {
		return
	}
	_, _ = io.Copy(conn, bytes.NewReader(append(body, '\n')))
}

func (s *AuthorityServer) dispatch(ctx context.Context, method string, raw []byte) (any, error) {
	params, err := authorityParams(method, raw)
	if err != nil {
		return nil, err
	}
	if !s.core.Available() {
		return nil, authority.Error{Code: authority.Unavailable}
	}
	switch method {
	case "AuthorityV1.Negotiate":
		return authorityRuntimeCapabilities(), nil
	case "AuthorityV1.Read":
		req := params.(authority.ReadRequest)
		if req.Operation == "status" {
			return s.core.Status(ctx, req.ProjectID)
		}
		return s.core.Read(ctx, req)
	case "AuthorityV1.Mutate":
		return s.core.Mutate(ctx, params.(authority.MutationRequest), localauthority.Actor{ID: "local-user"})
	case "AuthorityV1.MutateDocument":
		return s.core.MutateDocument(ctx, params.(authority.DocumentMutationRequest), localauthority.Actor{ID: "local-user"})
	case "AuthorityV1.KnowledgeStatus":
		return s.core.KnowledgeStatus(ctx, params.(authorityProjectRequest).ProjectID)
	case "AuthorityV1.RuntimeHealth":
		return s.core.RuntimeHealth(ctx, params.(localauthority.RuntimeHealthRequest))
	case "AuthorityV1.Search":
		return s.core.Search(ctx, params.(localauthority.SearchRequest))
	case "AuthorityV1.EnqueueRecovery":
		return s.core.EnqueueRecovery(ctx, params.(localauthority.RecoveryJobRequest), localauthority.Actor{ID: "local-user"})
	case "AuthorityV1.RecoveryJob":
		req := params.(localauthority.RecoveryJobLookup)
		return s.core.RecoveryJob(ctx, req.ProjectID, req.JobID)
	case "AuthorityV1.RecoveryJobs":
		return s.core.RecoveryJobs(ctx, params.(localauthority.RecoveryJobsRequest))
	case "AuthorityV1.CancelRecovery":
		return s.core.CancelRecovery(ctx, params.(localauthority.RecoveryJobAction), localauthority.Actor{ID: "local-user"})
	case "AuthorityV1.RetryRecovery":
		return s.core.RetryRecovery(ctx, params.(localauthority.RecoveryJobAction), localauthority.Actor{ID: "local-user"})
	case "AuthorityV1.EnqueueIndex":
		return s.core.AdmitIndex(ctx, params.(localauthority.IndexJobRequest), localauthority.Actor{ID: "local-user"})
	case "AuthorityV1.IndexJob":
		req := params.(localauthority.IndexJobLookup)
		return s.core.IndexJob(ctx, req.ProjectID, req.JobID)
	case "AuthorityV1.IndexJobs":
		return s.core.IndexJobs(ctx, params.(localauthority.IndexJobsRequest))
	case "AuthorityV1.CancelIndex":
		return s.core.CancelIndex(ctx, params.(localauthority.IndexJobAction), localauthority.Actor{ID: "local-user"})
	case "AuthorityV1.RetryIndex":
		return s.core.RetryIndex(ctx, params.(localauthority.IndexJobAction), localauthority.Actor{ID: "local-user"})
	case "AuthorityV1.PlanProjectionRepair":
		return s.core.InspectProjectionRepair(ctx, params.(localauthority.ProjectionRepairPlanRequest))
	case "AuthorityV1.ApplyProjectionRepair":
		return s.core.ConfirmProjectionRepair(ctx, params.(localauthority.ProjectionRepairRequest), localauthority.Actor{ID: "local-user"})
	case "AuthorityV1.ProjectionRepair":
		req := params.(localauthority.ProjectionRepairLookup)
		return s.core.ProjectionRepair(ctx, req.ProjectID, req.RepairID)
	case "AuthorityV1.ProjectionRepairs":
		return s.core.ProjectionRepairs(ctx, params.(localauthority.ProjectionRepairPlanRequest))
	default:
		return nil, authority.Error{Code: authority.Unsupported}
	}
}

func authorityRuntimeError(err error) *RPCError {
	var domain authority.Error
	if errors.As(err, &domain) {
		return &RPCError{Code: -32000, Message: domain.Error(), DiagnosticCode: domain.DiagnosticCode()}
	}
	var storage localauthority.Error
	if errors.As(err, &storage) {
		return &RPCError{Code: -32000, Message: storage.Error(), DiagnosticCode: storage.DiagnosticCode()}
	}
	// Cancellation/raw driver/fault/OS errors never enter protected peer prose.
	fixed := authority.Error{Code: authority.Unavailable}
	return &RPCError{Code: -32000, Message: fixed.Error(), DiagnosticCode: fixed.DiagnosticCode()}
}
