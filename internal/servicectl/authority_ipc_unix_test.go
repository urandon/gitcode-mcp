//go:build !windows

package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
)

func TestAuthorityActualPrivateSocketNegotiation(t *testing.T) {
	dir, err := shortWorkspaceTemp(t, "a1-")
	if err != nil {
		t.Fatal("short private fixture unavailable")
	}
	path := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal("private socket fixture unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- (RPCServer{}).Serve(ctx, listener) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("private IPC server not joined")
		}
	})
	c := RPCClient{Network: "unix", Address: path}
	var caps authority.Negotiation
	r := authority.NegotiationRequest{Protocol: authority.Protocol, Authority: authority.Local, SchemaMin: 1, SchemaMax: 1}
	if c.Call(context.Background(), "AuthorityV1.Negotiate", r, &caps) != nil || caps.Available {
		t.Fatal("private negotiation not honest")
	}
	requireAuthorityCode(t, c.Call(context.Background(), "AuthorityV1.Read", authority.ReadRequest{ProjectID: authorityExampleProject, Operation: "status"}, nil), authority.Unavailable)
}

func TestAuthoritySilentPrivateSocketCancellationJoins(t *testing.T) {
	dir, err := shortWorkspaceTemp(t, "a1-")
	if err != nil {
		t.Fatal("short private fixture unavailable")
	}
	path := filepath.Join(dir, "s")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal("private socket fixture unavailable")
	}
	entered, peerDone := make(chan struct{}), make(chan struct{})
	fixtureCtx, fixtureCancel := context.WithCancel(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		defer close(peerDone)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var req RPCRequest
		closed := make(chan struct{})
		stop := context.AfterFunc(fixtureCtx, func() { _ = conn.Close(); close(closed) })
		defer func() {
			if !stop() {
				<-closed
			}
		}()
		if json.NewDecoder(conn).Decode(&req) != nil {
			return
		}
		close(entered)
		_, _ = io.Copy(io.Discard, conn)
	}()
	joined := false
	t.Cleanup(func() {
		cancel()
		fixtureCancel()
		_ = listener.Close()
		if !joined {
			select {
			case <-result:
			case <-time.After(time.Second):
				t.Error("private caller not joined")
			}
		}
		select {
		case <-peerDone:
		case <-time.After(time.Second):
			t.Error("private peer not joined")
		}
	})
	c := RPCClient{Network: "unix", Address: path}
	req := authority.NegotiationRequest{Protocol: authority.Protocol, Authority: authority.Local, SchemaMin: 1, SchemaMax: 1}
	go func() { result <- c.Call(ctx, "AuthorityV1.Negotiate", req, &authority.Negotiation{}) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("private peer not entered")
	}
	cancel()
	select {
	case err := <-result:
		joined = true
		if !errors.Is(err, context.Canceled) {
			t.Error("private caller lost cancellation identity")
		}
	case <-time.After(time.Second):
		t.Error("private caller remained blocked")
	}
}

func TestAuthorityPrivateSocketRetainsDeclaredErrorIdentity(t *testing.T) {
	for _, code := range []authority.Code{authority.InvalidContract, authority.Unavailable, authority.Unsupported, authority.NoAuthority, authority.PolicyDenied, authority.Conflict, authority.RevisionConflict, authority.InitializationRequired,
		authority.ReadOnly, authority.Corrupt, authority.DurabilityUnavailable, authority.Capacity, authority.StorageUnavailable, authority.CommitUncertain,
		authority.InitializationPending, authority.RefConflict, authority.KnowledgeMigrationRequired, authority.KnowledgeSetupRequired,
		authority.KnowledgeInitializationPending, authority.DocumentPending, authority.DocumentRecoveryRequired, authority.KnowledgeCorrupt, "synthetic-private"} {
		t.Run(string(code), func(t *testing.T) {
			dir, err := shortWorkspaceTemp(t, "a1-")
			if err != nil {
				t.Fatal("short private fixture unavailable")
			}
			path := filepath.Join(dir, "s")
			listener, err := net.Listen("unix", path)
			if err != nil {
				t.Fatal("private error fixture unavailable")
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			done := make(chan struct{})
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				closed := make(chan struct{})
				stop := context.AfterFunc(ctx, func() { _ = conn.Close(); close(closed) })
				defer func() {
					if !stop() {
						<-closed
					}
				}()
				var req RPCRequest
				if json.NewDecoder(conn).Decode(&req) != nil {
					return
				}
				_ = json.NewEncoder(conn).Encode(RPCResponse{JSONRPC: jsonrpcVersion, ID: req.ID, Error: &RPCError{Code: -32000, Message: "synthetic-private-peer-body", DiagnosticCode: string(code)}})
			}()
			t.Cleanup(func() {
				cancel()
				_ = listener.Close()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("private error peer not joined")
				}
			})
			c := RPCClient{Network: "unix", Address: path}
			r := authority.NegotiationRequest{Protocol: authority.Protocol, Authority: authority.Local, SchemaMin: 1, SchemaMax: 1}
			want := code
			if code == "synthetic-private" {
				want = authority.Unsupported
			}
			requireAuthorityCode(t, c.Call(ctx, "AuthorityV1.Negotiate", r, &authority.Negotiation{}), want)
		})
	}
}
