package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
)

const authorityExampleProject authority.UUID = "12345678-1234-4234-8234-123456789abc"

func requireAuthorityCode(t *testing.T, err error, code authority.Code) {
	t.Helper()
	var e authority.Error
	if !errors.As(err, &e) || e.Code != code || strings.Contains(err.Error(), "synthetic-private") {
		t.Fatal("authority error missing or unsafe")
	}
}

func TestAuthorityIPCDeniesBeforeConnectionOrAdmission(t *testing.T) {
	r := authority.ReadRequest{ProjectID: authorityExampleProject, Operation: "status"}
	c := RPCClient{Network: "tcp", Address: "synthetic-private.invalid:1234"}
	requireAuthorityCode(t, c.Call(context.Background(), "AuthorityV1.Read", r, nil), authority.PolicyDenied)
	if c.nextID.Load() != 0 {
		t.Fatal("TCP control request was admitted")
	}
	c = RPCClient{Network: "mem", Address: "missing-synthetic-authority"}
	for _, params := range []any{map[string]any{"cache_path": "synthetic-private"}, map[string]any{"repo_id": "synthetic-private"}, map[string]any{"project_id": authorityExampleProject, "operation": "status", "credential": "synthetic-private"}} {
		requireAuthorityCode(t, c.Call(context.Background(), "AuthorityV1.Read", params, nil), authority.InvalidContract)
	}
	if c.nextID.Load() != 0 {
		t.Fatal("invalid DTO reached transport")
	}
	requireAuthorityCode(t, c.Call(context.Background(), "AuthorityV1.Read", r, nil), authority.Unavailable)
	// A zero-value server has no job manager, credential/provider or storage.
	// Reserved methods must fail without touching any legacy dispatch branch.
	s := RPCServer{}
	raw, _ := json.Marshal(r)
	_, err := s.dispatch(context.Background(), "AuthorityV1.Read", raw)
	requireAuthorityCode(t, err, authority.NoAuthority)
	_, err = s.dispatch(context.Background(), "AuthorityV1.synthetic-private", nil)
	requireAuthorityCode(t, err, authority.Unsupported)
}

func TestAuthorityIPCContractsOnlyNegotiationAndNoWriteDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	published := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- serveMemoryRPC(ctx, "authority-contract-fixture", RPCServer{}, func() { close(published) })
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("memory authority fixture not joined")
		}
	})
	<-published
	c := RPCClient{Network: "mem", Address: "authority-contract-fixture"}
	var caps authority.Negotiation
	req := authority.NegotiationRequest{Protocol: authority.Protocol, Authority: authority.Local, SchemaMin: 1, SchemaMax: 1}
	if c.Call(context.Background(), "AuthorityV1.Negotiate", req, &caps) != nil || caps.Available || len(caps.Methods) != 0 || caps.Reason != authority.Unavailable {
		t.Fatal("negotiation advertised unimplemented authority")
	}
	before := c.nextID.Load()
	payload, _ := json.Marshal(authority.IssuePayload{Title: "Synthetic", State: "open"})
	write := authority.MutationRequest{Intent: authority.WriteIntent{ProjectID: authorityExampleProject, WriteMode: "local", IdempotencyKey: "synthetic-key"}, Operation: "issue.create", Payload: payload}
	requireAuthorityCode(t, c.Call(context.Background(), "AuthorityV1.Mutate", write, nil), authority.Unavailable)
	if c.nextID.Load() != before+1 {
		t.Fatal("unavailable negotiation dispatched or retried mutation")
	}
	req.SchemaMin = 2
	req.SchemaMax = 2
	requireAuthorityCode(t, c.Call(context.Background(), "AuthorityV1.Negotiate", req, &caps), authority.Unsupported)
}

func TestAuthorityEnvelopeStrictAndPrivate(t *testing.T) {
	for _, raw := range []string{`{"jsonrpc":"2.0","id":{"private":"synthetic-private"},"method":"AuthorityV1.Negotiate"}`, `{"jsonrpc":"2.0","id":1,"id":2,"method":"AuthorityV1.Negotiate"}`, `{"jsonrpc":"2.0","ID":1,"method":"AuthorityV1.Negotiate"}`, `{"jsonrpc":"2.0","id":"synthetic-private","method":"AuthorityV1.Negotiate"}`} {
		requireAuthorityCode(t, strictAuthorityEnvelope([]byte(raw)), authority.InvalidContract)
	}
}
