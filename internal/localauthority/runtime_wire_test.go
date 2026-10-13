//go:build darwin || linux

package localauthority_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority"
	"gitcode-mcp/internal/servicectl"
)

const wireProject authority.UUID = "11111111-1111-4111-8111-111111111111"

func wireDirectory(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("", "lw-")
	if err != nil {
		t.Fatal("private wire fixture unavailable")
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	canonical, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal("wire directory qualification unavailable")
	}
	return canonical
}

func wireFixture(t *testing.T) (*servicectl.RPCClient, string) {
	t.Helper()
	root, storeDirectory, control := wireDirectory(t), wireDirectory(t), wireDirectory(t)
	registry, err := localauthority.TestRegistry(root)
	if err != nil {
		t.Fatal("test-only wire registry unavailable")
	}
	t.Cleanup(func() { _ = registry.Close() })
	manifest := authority.Manifest{Schema: 1, ID: wireProject, Slug: "synthetic-wire-research", Authority: authority.Local, Entities: []authority.EntityKind{authority.Issue, authority.Comment, authority.Label, authority.Milestone, authority.Document, authority.Link}, Network: authority.NetworkPolicy{DataEgress: "deny", ControlTransport: "ipc_only"}, Search: authority.SearchIntent{Default: "hybrid", Fallback: "full_text"}}
	store, err := localauthority.Initialize(context.Background(), registry, storeDirectory, manifest)
	if err != nil {
		t.Fatal("explicit wire authority fixture initialization failed")
	}
	_, err = store.SetupKnowledge(context.Background(), wireProject, "synthetic-wire-setup")
	_ = store.Close()
	if err != nil {
		t.Fatal("explicit wire knowledge fixture setup failed")
	}
	registration := authority.Registration{StorageRef: "wire-fixture", Writer: "coordinator", Backup: authority.BackupPolicy{Retain: 7, MaxBytes: 1 << 20}, Indexing: authority.IndexPolicy{IntervalSeconds: 900, MaxChunks: 128, EmbeddingProfile: "disabled"}}
	runtime, err := localauthority.NewRuntime(localauthority.RuntimeOptions{Registry: registry, Directory: control, NetworkState: "zero_network", Projects: []localauthority.RuntimeProject{{Manifest: manifest, Registration: registration, Directory: storeDirectory}}})
	if err != nil {
		t.Fatal("wire runtime fixture rejected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- (servicectl.Manager{Local: runtime}).Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("wire manager did not join")
		}
	})
	client, err := servicectl.NewLocalRPCClient(control)
	if err != nil {
		t.Fatal("trusted wire client fixture invalid")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var caps authority.Negotiation
		err = client.Call(context.Background(), "AuthorityV1.Negotiate", authority.NegotiationRequest{Protocol: authority.Protocol, Authority: authority.Local, SchemaMin: 1, SchemaMax: 1}, &caps)
		if err == nil && caps.Available {
			return client, control
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("actual qualified wire runtime did not become available")
	return nil, ""
}

func wireCall(t *testing.T, client *servicectl.RPCClient, method string, request, result any) {
	t.Helper()
	if err := client.Call(context.Background(), "AuthorityV1."+method, request, result); err != nil {
		t.Fatalf("actual wire %s failed: %T", method, err)
	}
}

func TestRuntimeWireQualifiedResearchWorkflowAndLegacyRefusal(t *testing.T) {
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "GITCODE_MCP_SERVICE_ADDRESS", "GITCODE_TOKEN"} {
		t.Setenv(name, "synthetic-inherited-setting")
	}
	client, control := wireFixture(t)
	payload, _ := json.Marshal(authority.IssuePayload{Title: "Synthetic wire task", Body: "Protected evidence", State: "open"})
	request := authority.AttributedMutationRequest{ActorID: "synthetic-wire-agent", Request: authority.MutationRequest{Intent: authority.WriteIntent{ProjectID: wireProject, WriteMode: "local", IdempotencyKey: "wire-original-issue"}, Operation: "issue.create", Payload: payload}}
	var first authority.MutationReceipt
	wireCall(t, client, "MutateAttributed", request, &first)
	if first.Validate() != nil || first.Replayed {
		t.Fatal("actual wire receipt unconfirmed")
	}
	var replay authority.MutationReceipt
	wireCall(t, client, "MutateAttributed", request, &replay)
	if !replay.Replayed || replay.OperationID != first.OperationID {
		t.Fatal("wire original-key replay duplicated mutation")
	}
	changed := request
	changed.ActorID = "another-actor"
	var code authority.Error
	if err := client.Call(context.Background(), "AuthorityV1.MutateAttributed", changed, nil); !errors.As(err, &code) || code.Code != authority.Conflict {
		t.Fatal("wire changed actor did not conflict")
	}
	var read authority.ReadResult
	wireCall(t, client, "Read", authority.ReadRequest{ProjectID: wireProject, Kind: authority.Issue, EntityID: first.Citation.EntityID, Operation: "get"}, &read)
	if len(read.Entities) != 1 || read.Entities[0].Citation != first.Citation {
		t.Fatal("wire read citation changed")
	}
	var metadata localauthority.RevisionMetadata
	wireCall(t, client, "RevisionMetadata", first.Citation, &metadata)
	if metadata.ActorID != request.ActorID {
		t.Fatal("wire protected asserted actor lost")
	}
	var audit localauthority.AuditResult
	wireCall(t, client, "Audit", authority.AuditRequest{ProjectID: wireProject, Limit: 100}, &audit)
	if len(audit.Entries) != 1 || audit.ProjectID != wireProject {
		t.Fatal("wire audit binding or original receipt invalid")
	}
	var knowledge localauthority.KnowledgeState
	wireCall(t, client, "KnowledgeStatus", authority.ProjectRequest{ProjectID: wireProject}, &knowledge)
	if knowledge.Phase != "ready" {
		t.Fatal("explicit setup lost on wire startup")
	}
	docPayload, _ := json.Marshal(authority.DocumentPayload{Title: "Synthetic document", Markdown: "# Exact wire Git-only bytes"})
	document := authority.DocumentMutationCall{ActorID: request.ActorID, Request: authority.DocumentMutationRequest{Schema: 1, Operation: "document.create", Intent: authority.DocumentWriteIntent{ProjectID: wireProject, WriteMode: "local", IdempotencyKey: "wire-original-doc", ExpectedRefOID: knowledge.CurrentRefOID}, Payload: docPayload}}
	var written authority.DocumentMutationResult
	wireCall(t, client, "MutateDocument", document, &written)
	if written.Validate() != nil || written.Result.State != "live" {
		t.Fatal("wire exact document receipt invalid")
	}
	var state localauthority.DocumentOperationResult
	original := authority.OriginalOperationRequest{ProjectID: wireProject, IdempotencyKey: document.Request.Intent.IdempotencyKey}
	wireCall(t, client, "DocumentOperationStatus", original, &state)
	if state.Match(original) != nil || state.State.Phase != "settled" {
		t.Fatal("wire original document state binding invalid")
	}
	wireCall(t, client, "CleanupDocumentPin", authority.CleanupDocumentRequest{ProjectID: wireProject, IdempotencyKey: original.IdempotencyKey, WriteMode: "local"}, &state)
	if state.State.PinState != "cleared" {
		t.Fatal("wire settled pin cleanup did not complete")
	}
	wireCall(t, client, "Read", authority.ReadRequest{ProjectID: wireProject, Kind: authority.Document, EntityID: written.Receipt.Citation.EntityID, Revision: 1, Operation: "get"}, &read)
	var doc authority.DocumentPayload
	if authority.DecodeJSON(read.Entities[0].Payload, &doc) != nil || doc.Markdown != "# Exact wire Git-only bytes" {
		t.Fatal("wire historical document bytes substituted")
	}
	wireCall(t, client, "Read", authority.ReadRequest{ProjectID: wireProject, Operation: "status"}, nil)
	legacy := servicectl.RPCClient{Network: "unix", Address: filepath.Join(control, "control.sock")}
	if err := legacy.Call(context.Background(), "AuthorityV1.Read", authority.ReadRequest{ProjectID: wireProject, Operation: "status"}, nil); !errors.As(err, &code) || code.Code != authority.Unsupported {
		t.Fatal("legacy client admitted available authority work")
	}
	if err := client.Call(context.Background(), "Service.Status", nil, nil); !errors.As(err, &code) || code.Code != authority.PolicyDenied {
		t.Fatal("local client reached legacy service")
	}
	if err := client.Call(context.Background(), "AuthorityV1.Read", authority.ReadRequest{ProjectID: "22222222-2222-4222-8222-222222222222", Operation: "status"}, nil); !errors.As(err, &code) || code.Code != authority.NoAuthority {
		t.Fatal("unknown wire project fell back")
	}
	client.Network = "mem"
	if err := client.Call(context.Background(), "AuthorityV1.Read", authority.ReadRequest{ProjectID: wireProject, Operation: "status"}, nil); !errors.As(err, &code) || code.Code != authority.PolicyDenied {
		t.Fatal("mixed local transport override admitted")
	}
}
