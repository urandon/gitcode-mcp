//go:build darwin || linux

package servicectl

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority"
	"gitcode-mcp/internal/privateipc"
)

func authorityLocalPeer(t *testing.T, respond func(RPCRequest) RPCResponse) *RPCClient {
	t.Helper()
	return authorityLocalRawPeer(t, func(r RPCRequest) any { return respond(r) })
}

func authorityLocalRawPeer(t *testing.T, respond func(RPCRequest) any) *RPCClient {
	t.Helper()
	path, err := os.MkdirTemp("", "lp-")
	if err != nil {
		t.Fatal("local peer directory unavailable")
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal("local peer canonical directory unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	listener, err := privateipc.Listen(ctx, path)
	if err != nil {
		cancel()
		t.Fatal("qualified local peer unavailable")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				closed := make(chan struct{})
				stop := context.AfterFunc(ctx, func() { _ = conn.Close(); close(closed) })
				defer func() {
					if !stop() {
						<-closed
					}
				}()
				_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
				var request RPCRequest
				if json.NewDecoder(conn).Decode(&request) != nil {
					return
				}
				_ = json.NewEncoder(conn).Encode(respond(request))
			}()
		}
	}()
	t.Cleanup(func() {
		cancel()
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("local peer did not join")
		}
		if listener.Release() != nil {
			t.Error("local peer lease did not release")
		}
	})
	client, err := NewLocalRPCClient(path)
	if err != nil {
		t.Fatal("local peer client constructor failed")
	}
	return client
}

func TestAuthorityLocalPeerExactErrorUnion(t *testing.T) {
	for _, variant := range []string{"success-null-error", "error-null-result", "error-null", "no-arm", "observation", "numeric-code", "message", "missing-code", "missing-message", "missing-diagnostic", "null-code", "null-message", "null-diagnostic"} {
		t.Run(variant, func(t *testing.T) {
			client := authorityLocalRawPeer(t, func(r RPCRequest) any {
				if r.Method == "AuthorityV1.Negotiate" {
					return RPCResponse{JSONRPC: jsonrpcVersion, ID: r.ID, Result: peerCapabilities("Read")}
				}
				arm := map[string]any{"code": -32000, "message": (authority.Error{Code: authority.Conflict}).Error(), "diagnostic_code": string(authority.Conflict)}
				response := map[string]any{"jsonrpc": jsonrpcVersion, "id": r.ID, "error": arm}
				switch variant {
				case "success-null-error":
					response["result"], response["error"] = peerStatus(), nil
				case "error-null-result":
					response["result"] = nil
				case "error-null":
					response["error"] = nil
				case "no-arm":
					delete(response, "error")
				case "observation":
					arm["observation"] = map[string]any{"code": "synthetic-private", "reason": "synthetic-private-peer-body"}
				case "numeric-code":
					arm["code"] = -32001
				case "message":
					arm["message"] = "synthetic-private-peer-body"
				case "missing-code":
					delete(arm, "code")
				case "missing-message":
					delete(arm, "message")
				case "missing-diagnostic":
					delete(arm, "diagnostic_code")
				case "null-code":
					arm["code"] = nil
				case "null-message":
					arm["message"] = nil
				case "null-diagnostic":
					arm["diagnostic_code"] = nil
				}
				return response
			})
			requireAuthorityCode(t, client.Call(context.Background(), "AuthorityV1.Read", authority.ReadRequest{ProjectID: authorityExampleProject, Operation: "status"}, nil), authority.InvalidContract)
		})
	}
}

func TestAuthorityLocalPeerFixedErrorArmRemainsUsable(t *testing.T) {
	for _, code := range []authority.Code{authority.Conflict, authority.PolicyDenied, authority.Capacity, authority.Unavailable} {
		client := authorityLocalPeer(t, func(r RPCRequest) RPCResponse {
			if r.Method == "AuthorityV1.Negotiate" {
				return RPCResponse{JSONRPC: jsonrpcVersion, ID: r.ID, Result: peerCapabilities("Read")}
			}
			return authorityFixedResponse(r.ID, code)
		})
		requireAuthorityCode(t, client.Call(context.Background(), "AuthorityV1.Read", authority.ReadRequest{ProjectID: authorityExampleProject, Operation: "status"}, nil), code)
	}
}

func TestAuthorityLocalPeerReadCursorBinding(t *testing.T) {
	entityID := authority.UUID("33333333-3333-4333-8333-333333333333")
	encode := func(fields map[string]any) string {
		raw, _ := json.Marshal(fields)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	for _, operation := range []string{"list", "history"} {
		for _, variant := range []string{"valid", "terminal", "malformed", "project", "kind", "operation", "entity", "snapshot", "backwards", "ahead", "missing", "null", "extra", "future-row", "duplicate-page", "empty"} {
			t.Run(operation+"/"+variant, func(t *testing.T) {
				fields := map[string]any{"project": authorityExampleProject, "kind": authority.Issue, "operation": operation, "entity": "", "snapshot": 5, "last": 1}
				request := authority.ReadRequest{ProjectID: authorityExampleProject, Kind: authority.Issue, Operation: operation, Limit: 1}
				if operation == "history" {
					fields["entity"], request.EntityID = entityID, entityID
				}
				request.Cursor = encode(fields)
				fields["last"] = 2
				view := authority.EntityView{Citation: authority.Citation{ProjectID: authorityExampleProject, EntityID: entityID, Kind: authority.Issue, Revision: 2}, DisplayAlias: "ISSUE-2", ProjectSequence: 2, Payload: json.RawMessage(`{"title":"Synthetic","body":"","state":"open"}`)}
				result := authority.ReadResult{ProjectID: authorityExampleProject, Entities: []authority.EntityView{view}}
				switch variant {
				case "project":
					fields["project"] = "22222222-2222-4222-8222-222222222222"
				case "kind":
					fields["kind"] = authority.Label
				case "operation":
					fields["operation"] = "get"
				case "entity":
					fields["entity"] = "44444444-4444-4444-8444-444444444444"
				case "snapshot":
					fields["snapshot"] = 6
				case "backwards":
					fields["last"] = 1
				case "ahead":
					fields["last"] = 3
				case "missing":
					delete(fields, "last")
				case "null":
					fields["snapshot"] = nil
				case "extra":
					fields["private"] = "synthetic-private"
				case "future-row":
					result.Entities[0].ProjectSequence = 6
				case "duplicate-page":
					result.Entities[0].Citation.Revision, result.Entities[0].DisplayAlias, fields["last"] = 1, "ISSUE-1", 1
				case "empty":
					result.Entities = []authority.EntityView{}
				}
				result.NextCursor = encode(fields)
				if variant == "malformed" {
					result.NextCursor = "synthetic-cursor"
				}
				if variant == "terminal" {
					result.NextCursor = ""
				}
				client := authorityLocalPeer(t, func(r RPCRequest) RPCResponse {
					if r.Method == "AuthorityV1.Negotiate" {
						return RPCResponse{JSONRPC: jsonrpcVersion, ID: r.ID, Result: peerCapabilities("Read")}
					}
					return RPCResponse{JSONRPC: jsonrpcVersion, ID: r.ID, Result: result}
				})
				err := client.Call(context.Background(), "AuthorityV1.Read", request, nil)
				if variant == "valid" || variant == "terminal" {
					if err != nil {
						t.Fatal("valid bound page refused")
					}
					return
				}
				requireAuthorityCode(t, err, authority.InvalidContract)
			})
		}
	}
}

func peerCapabilities(method string) authority.Negotiation {
	caps := authority.Negotiation{Protocol: authority.Protocol, Authority: authority.Local, SchemaMin: 1, SchemaMax: 1, Available: true, Methods: []string{method}}
	if method == "MutateDocument" || method == "KnowledgeStatus" || method == "DocumentOperationStatus" || method == "CleanupDocumentPin" {
		caps.DocumentSchemaMin, caps.DocumentSchemaMax = 1, 1
	}
	return caps
}

func peerStatus() authority.ProjectStatus {
	return authority.ProjectStatus{ProjectID: authorityExampleProject, Authority: authority.Local, Readiness: "ready", Generation: 1, NetworkState: "zero_network", WriterState: "owned"}
}

func TestAuthorityLocalPeerRejectsMalformedSuccessWithNilDestination(t *testing.T) {
	request := authority.ReadRequest{ProjectID: authorityExampleProject, Operation: "status"}
	base, _ := json.Marshal(peerStatus())
	for name, mutate := range map[string]func(map[string]any){
		"project":    func(m map[string]any) { m["project_id"] = "22222222-2222-4222-8222-222222222222" },
		"missing":    func(m map[string]any) { delete(m, "writer_state") },
		"null":       func(m map[string]any) { m["generation"] = nil },
		"extra":      func(m map[string]any) { m["private"] = "synthetic-private" },
		"enum":       func(m map[string]any) { m["readiness"] = "unknown" },
		"generation": func(m map[string]any) { m["indexed_generation"] = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			var fields map[string]any
			_ = json.Unmarshal(base, &fields)
			mutate(fields)
			body, _ := json.Marshal(fields)
			client := authorityLocalPeer(t, func(r RPCRequest) RPCResponse {
				if r.Method == "AuthorityV1.Negotiate" {
					return RPCResponse{JSONRPC: jsonrpcVersion, ID: r.ID, Result: peerCapabilities("Read")}
				}
				return RPCResponse{JSONRPC: jsonrpcVersion, ID: r.ID, Result: json.RawMessage(body)}
			})
			requireAuthorityCode(t, client.Call(context.Background(), "AuthorityV1.Read", request, nil), authority.InvalidContract)
		})
	}
}

func TestAuthorityLocalPeerRejectsWrongOriginalKeyAndAuditProject(t *testing.T) {
	original := authority.OriginalOperationRequest{ProjectID: authorityExampleProject, IdempotencyKey: "synthetic-original-key"}
	wrong := original
	wrong.IdempotencyKey = "synthetic-wrong-key"
	digest, _ := wrong.KeyHash()
	state := localauthority.DocumentOperationResult{Schema: 1, IdempotencyKeyHash: digest, State: localauthority.DocumentOperationState{Schema: 1, ProjectID: authorityExampleProject, OperationID: "22222222-2222-4222-8222-222222222222", EntityID: "33333333-3333-4333-8333-333333333333", Scope: "document_operation", Phase: "settled", PinState: "cleared", Remediation: "none"}}
	for _, method := range []string{"DocumentOperationStatus", "CleanupDocumentPin"} {
		t.Run(method, func(t *testing.T) {
			client := authorityLocalPeer(t, func(r RPCRequest) RPCResponse {
				if r.Method == "AuthorityV1.Negotiate" {
					return RPCResponse{JSONRPC: jsonrpcVersion, ID: r.ID, Result: peerCapabilities(method)}
				}
				return RPCResponse{JSONRPC: jsonrpcVersion, ID: r.ID, Result: state}
			})
			var request any = original
			if method == "CleanupDocumentPin" {
				request = authority.CleanupDocumentRequest{ProjectID: original.ProjectID, IdempotencyKey: original.IdempotencyKey, WriteMode: "local"}
			}
			requireAuthorityCode(t, client.Call(context.Background(), "AuthorityV1."+method, request, nil), authority.InvalidContract)
		})
	}
	client := authorityLocalPeer(t, func(r RPCRequest) RPCResponse {
		if r.Method == "AuthorityV1.Negotiate" {
			return RPCResponse{JSONRPC: jsonrpcVersion, ID: r.ID, Result: peerCapabilities("Audit")}
		}
		return RPCResponse{JSONRPC: jsonrpcVersion, ID: r.ID, Result: localauthority.AuditResult{ProjectID: "22222222-2222-4222-8222-222222222222", Entries: []localauthority.AuditEntry{}}}
	})
	requireAuthorityCode(t, client.Call(context.Background(), "AuthorityV1.Audit", authority.AuditRequest{ProjectID: authorityExampleProject, Limit: 1}, nil), authority.InvalidContract)
}

func TestAuthorityLocalPeerMutationIntentAndRevisionMatching(t *testing.T) {
	payload := json.RawMessage(`{"title":"Synthetic","body":"Evidence","state":"open"}`)
	request := authority.AttributedMutationRequest{ActorID: "synthetic-agent", Request: authority.MutationRequest{Intent: authority.WriteIntent{ProjectID: authorityExampleProject, WriteMode: "local", IdempotencyKey: "synthetic-original-write"}, Operation: "issue.create", Payload: payload}}
	intent, _ := localauthority.MutationIntentHash(request)
	key, _ := (authority.OriginalOperationRequest{ProjectID: authorityExampleProject, IdempotencyKey: request.Request.Intent.IdempotencyKey}).KeyHash()
	base := authority.MutationReceipt{ProjectID: authorityExampleProject, OperationID: "22222222-2222-4222-8222-222222222222", Citation: authority.Citation{ProjectID: authorityExampleProject, EntityID: "33333333-3333-4333-8333-333333333333", Kind: authority.Issue, Revision: 1}, ProjectSequence: 1, IdempotencyKeyHash: key, IntentHash: intent, Evidence: "local_commit_confirmed", ProjectionState: "pending"}
	for name, mutate := range map[string]func(*authority.MutationReceipt){
		"intent":   func(r *authority.MutationReceipt) { r.IntentHash = strings.Repeat("a", 64) },
		"key":      func(r *authority.MutationReceipt) { r.IdempotencyKeyHash = strings.Repeat("b", 64) },
		"revision": func(r *authority.MutationReceipt) { r.Citation.Revision = 2 },
		"kind":     func(r *authority.MutationReceipt) { r.Citation.Kind = authority.Label },
	} {
		t.Run(name, func(t *testing.T) {
			bad := base
			mutate(&bad)
			client := authorityLocalPeer(t, func(r RPCRequest) RPCResponse {
				if r.Method == "AuthorityV1.Negotiate" {
					return RPCResponse{JSONRPC: jsonrpcVersion, ID: r.ID, Result: peerCapabilities("MutateAttributed")}
				}
				return RPCResponse{JSONRPC: jsonrpcVersion, ID: r.ID, Result: bad}
			})
			requireAuthorityCode(t, client.Call(context.Background(), "AuthorityV1.MutateAttributed", request, nil), authority.InvalidContract)
		})
	}
}

func TestAuthorityLocalPeerCapabilitiesStopBeforeOperation(t *testing.T) {
	for _, kind := range []string{"unsupported-method", "unavailable", "legacy-client"} {
		t.Run(kind, func(t *testing.T) {
			var operations atomic.Int32
			client := authorityLocalPeer(t, func(r RPCRequest) RPCResponse {
				if r.Method != "AuthorityV1.Negotiate" {
					operations.Add(1)
					return RPCResponse{JSONRPC: jsonrpcVersion, ID: r.ID, Result: peerStatus()}
				}
				caps := peerCapabilities("Read")
				if kind == "unsupported-method" {
					caps = peerCapabilities("Audit")
				}
				if kind == "unavailable" {
					caps = authority.ContractsOnly()
				}
				return RPCResponse{JSONRPC: jsonrpcVersion, ID: r.ID, Result: caps}
			})
			want := authority.Unsupported
			if kind == "legacy-client" {
				client.Address = filepath.Join(client.localDirectory, "control.sock")
				client.Network = "unix"
				client.localDirectory = ""
			}
			if kind == "unavailable" {
				want = authority.Unavailable
			}
			requireAuthorityCode(t, client.Call(context.Background(), "AuthorityV1.Read", authority.ReadRequest{ProjectID: authorityExampleProject, Operation: "status"}, nil), want)
			if operations.Load() != 0 {
				t.Fatal("incompatible peer admitted operation")
			}
		})
	}
}

func TestAuthorityLocalPeerResponseBudgetUnionAndID(t *testing.T) {
	for _, kind := range []string{"oversize", "union", "id", "null", "extra"} {
		t.Run(kind, func(t *testing.T) {
			client := authorityLocalPeer(t, func(r RPCRequest) RPCResponse {
				if r.Method == "AuthorityV1.Negotiate" {
					return RPCResponse{JSONRPC: jsonrpcVersion, ID: r.ID, Result: peerCapabilities("Read")}
				}
				reply := RPCResponse{JSONRPC: jsonrpcVersion, ID: r.ID, Result: peerStatus()}
				switch kind {
				case "oversize":
					reply.Error = &RPCError{Code: -32000, Message: strings.Repeat("x", authority.MaxContractBytes+1), DiagnosticCode: string(authority.InvalidContract)}
					reply.Result = nil
				case "union":
					reply.Error = &RPCError{Code: -32000, Message: "synthetic-private-peer", DiagnosticCode: string(authority.InvalidContract)}
				case "id":
					reply.ID = 999
				case "null":
					reply.Result = json.RawMessage(`null`)
				case "extra":
					reply.Result = json.RawMessage(`{"project_id":"12345678-1234-4234-8234-123456789abc","extra":"synthetic-private"}`)
				}
				return reply
			})
			requireAuthorityCode(t, client.Call(context.Background(), "AuthorityV1.Read", authority.ReadRequest{ProjectID: authorityExampleProject, Operation: "status"}, nil), authority.InvalidContract)
		})
	}
}

type refusedAuthorityListener struct{ touched bool }

func (l *refusedAuthorityListener) Accept() (net.Conn, error) {
	l.touched = true
	return nil, errors.New("unqualified listener touched")
}
func (l *refusedAuthorityListener) Close() error { l.touched = true; return nil }
func (*refusedAuthorityListener) Addr() net.Addr {
	return &net.UnixAddr{Name: "synthetic", Net: "unix"}
}

func TestAuthorityLocalServerCannotAdoptPassedOrMemoryListener(t *testing.T) {
	server := RPCServer{Local: &localauthority.Runtime{}}
	listener := &refusedAuthorityListener{}
	requireAuthorityCode(t, server.Serve(context.Background(), listener), authority.PolicyDenied)
	if listener.touched {
		t.Fatal("local server touched unqualified listener")
	}
	requireAuthorityCode(t, serveMemoryRPC(context.Background(), "synthetic-local-memory", server, func() { t.Error("local memory runtime published") }), authority.PolicyDenied)
}

func TestAuthorityNumericIDsAndActualResponseEnvelopeBound(t *testing.T) {
	for _, id := range []string{`9007199254740993`, `9007199254740992`, `1e0`, `1.0`, `0`, `-1`, `"1"`, `null`} {
		raw := []byte(`{"jsonrpc":"2.0","id":` + id + `,"method":"AuthorityV1.Negotiate","params":{}}`)
		requireAuthorityCode(t, strictAuthorityEnvelope(raw), authority.InvalidContract)
	}
	response := boundedAuthorityResponse(RPCResponse{JSONRPC: jsonrpcVersion, ID: 1, Result: strings.Repeat("x", authority.MaxContractBytes)})
	if response.Result != nil || response.Error == nil || response.Error.DiagnosticCode != string(authority.Capacity) {
		t.Fatal("actual oversized response not replaced with finite capacity")
	}
	body, _ := json.Marshal(response)
	if len(body)+1 > authority.MaxContractBytes {
		t.Fatal("capacity response exceeded envelope limit")
	}
}

func TestAuthorityLocalDestinationAndInvalidUTFRefuseBeforeConnection(t *testing.T) {
	client, err := NewLocalRPCClient(filepath.Join(t.TempDir(), "missing"))
	if err != nil {
		t.Fatal("pure missing-runtime enrollment invalid")
	}
	request := authority.ReadRequest{ProjectID: authorityExampleProject, Operation: "status"}
	var nilStatus *authority.ProjectStatus
	var nilRequest *authority.ReadRequest
	requireAuthorityCode(t, client.Call(context.Background(), "AuthorityV1.Read", nilRequest, nil), authority.InvalidContract)
	for _, result := range []any{nilStatus, &authority.ReadResult{}, "not-a-pointer"} {
		requireAuthorityCode(t, client.Call(context.Background(), "AuthorityV1.Read", request, result), authority.InvalidContract)
	}
	payload := json.RawMessage(`{"title":"Synthetic","body":"","state":"open"}`)
	invalid := authority.AttributedMutationRequest{ActorID: string([]byte{0xff}), Request: authority.MutationRequest{Intent: authority.WriteIntent{ProjectID: authorityExampleProject, WriteMode: "local", IdempotencyKey: "synthetic-key"}, Operation: "issue.create", Payload: payload}}
	requireAuthorityCode(t, client.Call(context.Background(), "AuthorityV1.MutateAttributed", invalid, nil), authority.InvalidContract)
	if client.nextID.Load() != 0 {
		t.Fatal("invalid typed destination or UTF attribution reached connection")
	}
}

func TestAuthorityLocalPeerDocumentLocatorMatchesOriginalBytes(t *testing.T) {
	payload := json.RawMessage(`{"title":"Synthetic","markdown":"Exact bytes"}`)
	request := authority.DocumentMutationCall{ActorID: "synthetic-agent", Request: authority.DocumentMutationRequest{Schema: 1, Operation: "document.create", Intent: authority.DocumentWriteIntent{ProjectID: authorityExampleProject, WriteMode: "local", IdempotencyKey: "synthetic-original-doc", ExpectedRefOID: authority.EmptyKnowledgeRef}, Payload: payload}}
	intent, _ := localauthority.DocumentIntentHash(request)
	key, _ := (authority.OriginalOperationRequest{ProjectID: authorityExampleProject, IdempotencyKey: request.Request.Intent.IdempotencyKey}).KeyHash()
	citation := authority.Citation{ProjectID: authorityExampleProject, EntityID: "33333333-3333-4333-8333-333333333333", Kind: authority.Document, Revision: 1}
	for _, kind := range []string{"digest", "length", "intent"} {
		t.Run(kind, func(t *testing.T) {
			result := authority.DocumentMutationResult{Schema: 1, Durability: authority.OrderedOSBarriersV1, Receipt: authority.MutationReceipt{ProjectID: authorityExampleProject, OperationID: "22222222-2222-4222-8222-222222222222", Citation: citation, ProjectSequence: 1, IdempotencyKeyHash: key, IntentHash: intent, Evidence: "local_commit_confirmed", ProjectionState: "pending"}, Result: authority.DocumentOutcome{State: "live", Locator: &authority.DocumentLocator{Citation: citation, CommitOID: strings.Repeat("a", 64), BlobOID: strings.Repeat("b", 64), ByteLength: 11}}}
			// Derive the correct source digest with the same transparent local hash
			// as the receiver, then change one field of an otherwise valid result.
			var content authority.DocumentPayload
			_ = authority.DecodeJSON(payload, &content)
			digest := sha256.Sum256([]byte(content.Markdown))
			result.Result.Locator.SHA256 = hex.EncodeToString(digest[:])
			switch kind {
			case "digest":
				result.Result.Locator.SHA256 = strings.Repeat("c", 64)
			case "length":
				result.Result.Locator.ByteLength = 12
			case "intent":
				result.Receipt.IntentHash = strings.Repeat("d", 64)
			}
			client := authorityLocalPeer(t, func(r RPCRequest) RPCResponse {
				if r.Method == "AuthorityV1.Negotiate" {
					return RPCResponse{JSONRPC: jsonrpcVersion, ID: r.ID, Result: peerCapabilities("MutateDocument")}
				}
				return RPCResponse{JSONRPC: jsonrpcVersion, ID: r.ID, Result: result}
			})
			requireAuthorityCode(t, client.Call(context.Background(), "AuthorityV1.MutateDocument", request, nil), authority.InvalidContract)
		})
	}
}

func TestAuthorityLocalPeerSilentCallCancellationJoins(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	client := authorityLocalPeer(t, func(r RPCRequest) RPCResponse {
		close(entered)
		<-release
		return RPCResponse{JSONRPC: jsonrpcVersion, ID: r.ID, Result: peerCapabilities("Read")}
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- client.Call(ctx, "AuthorityV1.Negotiate", authority.NegotiationRequest{Protocol: authority.Protocol, Authority: authority.Local, SchemaMin: 1, SchemaMax: 1}, nil)
	}()
	joined := false
	t.Cleanup(func() {
		cancel()
		unblock()
		if !joined {
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Error("local cancelled client did not join")
			}
		}
	})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("qualified silent peer not entered")
	}
	cancel()
	select {
	case err := <-done:
		joined = true
		if !errors.Is(err, context.Canceled) {
			t.Fatal("qualified client lost caller cancellation")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("qualified silent call did not cancel/join")
	}
	unblock()
}

func TestAuthorityLocalLegacyDenialNeverReflectsUntrustedID(t *testing.T) {
	for _, method := range []string{"Service.Status", "ObservabilityV1.Summary", "synthetic-unknown"} {
		t.Run(method, func(t *testing.T) {
			server := RPCServer{Local: &localauthority.Runtime{}}
			request := RPCRequest{JSONRPC: jsonrpcVersion, ID: map[string]string{"private": "synthetic-private-sentinel"}, Method: method, Params: json.RawMessage(`{}`)}
			response := server.handleRequest(context.Background(), request)
			body, _ := json.Marshal(response)
			if response.ID != nil || response.Error == nil || response.Error.DiagnosticCode != string(authority.PolicyDenied) || strings.Contains(string(body), "synthetic-private-sentinel") {
				t.Fatal("local legacy denial reflected private ID")
			}
			left, right := net.Pipe()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			done := make(chan struct{})
			go func() { defer close(done); server.handleConn(ctx, left) }()
			_ = json.NewEncoder(right).Encode(request)
			var wire json.RawMessage
			if json.NewDecoder(right).Decode(&wire) != nil || strings.Contains(string(wire), "synthetic-private-sentinel") {
				t.Error("local wire denial reflected private ID")
			}
			_ = right.Close()
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("local denial handler did not join")
			}
		})
	}
}
