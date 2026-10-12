package authority

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFullReplacementRequiresZeroValuedPayloadFields(t *testing.T) {
	for _, test := range []struct{ operation, payload string }{
		{"issue.create", `{"title":"Synthetic","state":"open"}`},
		{"label.create", `{"name":"Synthetic","color":"#abcdef"}`},
		{"milestone.create", `{"title":"Synthetic"}`},
		{"milestone.create", `{"title":"Synthetic","description":"","due_date":""}`},
	} {
		r := MutationRequest{Intent: WriteIntent{ProjectID: exampleID, WriteMode: "local", IdempotencyKey: "synthetic-key"}, Operation: test.operation, Payload: json.RawMessage(test.payload)}
		requireError(t, r.Validate(), InvalidContract)
	}
	valid := MutationRequest{Intent: WriteIntent{ProjectID: exampleID, WriteMode: "local", IdempotencyKey: "synthetic-key"}, Operation: "issue.create", Payload: json.RawMessage(`{"title":"Synthetic","body":"","state":"open"}`)}
	if valid.Validate() != nil {
		t.Fatal("explicit empty full replacement body rejected")
	}
}

func TestResultsRequireFieldsAndSemanticAuthorityEvidence(t *testing.T) {
	for _, target := range []any{&ReadResult{}, &MutationReceipt{}, &EntityView{}, &ProjectStatus{}, &DocumentLocator{}} {
		requireError(t, DecodeJSON([]byte(`{}`), target), InvalidContract)
	}
	v := EntityView{Citation: Citation{exampleID, exampleEntity, Issue, 1}, DisplayAlias: "ISSUE-1", ProjectSequence: 1, Payload: json.RawMessage(`{"title":"Synthetic","body":"","state":"open"}`)}
	r := ReadResult{ProjectID: exampleID, Entities: []EntityView{v}}
	data, _ := json.Marshal(r)
	var decoded ReadResult
	if DecodeJSON(data, &decoded) != nil {
		t.Fatal("valid typed read result rejected")
	}
	for _, bad := range []ReadResult{
		{ProjectID: exampleID, Entities: nil}, {ProjectID: exampleEntity, Entities: []EntityView{v}}, {ProjectID: exampleID, Entities: []EntityView{v, v}}, {ProjectID: exampleID, Entities: []EntityView{}, NextCursor: "synthetic-cursor"},
	} {
		data, _ := json.Marshal(bad)
		requireError(t, DecodeJSON(data, &ReadResult{}), InvalidContract)
	}
	for _, payload := range []string{`{}`, `{"title":"Synthetic","state":"open"}`, `{"title":"Synthetic","body":"","state":"invalid"}`, `{"title":"Synthetic","body":"","state":"open","private":"synthetic-private"}`} {
		bad := v
		bad.Payload = json.RawMessage(payload)
		data, _ := json.Marshal(bad)
		requireError(t, DecodeJSON(data, &EntityView{}), InvalidContract)
	}
	receipt := MutationReceipt{ProjectID: exampleID, OperationID: exampleEntity, Citation: v.Citation, ProjectSequence: 1, IdempotencyKeyHash: strings.Repeat("a", 64), IntentHash: strings.Repeat("b", 64), Evidence: "local_commit_confirmed", ProjectionState: "pending"}
	data, _ = json.Marshal(receipt)
	if DecodeJSON(data, &MutationReceipt{}) != nil {
		t.Fatal("synthetic semantic receipt rejected")
	}
	for i := 0; i < 5; i++ {
		bad := receipt
		switch i {
		case 0:
			bad.Evidence = "provider-confirmed"
		case 1:
			bad.ProjectSequence = 0
		case 2:
			bad.ProjectID = exampleEntity
		case 3:
			bad.ProjectionState = "guessed"
		case 4:
			bad.IntentHash = "synthetic-private"
		}
		data, _ := json.Marshal(bad)
		requireError(t, DecodeJSON(data, &MutationReceipt{}), InvalidContract)
	}
	var fields map[string]json.RawMessage
	data, _ = json.Marshal(receipt)
	_ = json.Unmarshal(data, &fields)
	delete(fields, "replayed")
	data, _ = json.Marshal(fields)
	requireError(t, DecodeJSON(data, &MutationReceipt{}), InvalidContract)
}

func TestNonStrictPoliciesRequireMatchingIsolatedCapabilities(t *testing.T) {
	base := RuntimeProfile{Protocol: Protocol, SchemaMin: 1, SchemaMax: 1, PrivateIPC: true}
	admin := base
	admin.TCP = true
	admin.LoopbackTCPOnly = true
	admin.Admin = true
	provider := base
	provider.TCP = true
	provider.LoopbackTCPOnly = true
	provider.EmbeddingProvider = true
	provider.PreProvisioned = true
	for _, test := range []struct {
		policy  NetworkPolicy
		profile RuntimeProfile
	}{
		{NetworkPolicy{"deny", "loopback_admin"}, admin}, {NetworkPolicy{"loopback_embeddings", "ipc_only"}, provider},
	} {
		if Preflight(test.policy, test.profile) != nil {
			t.Fatal("matching loopback profile rejected")
		}
		for i := 0; i < 10; i++ {
			bad := test.profile
			switch i {
			case 0:
				bad.PrivateIPC = false
			case 1:
				bad.TCP = false
			case 2:
				bad.LoopbackTCPOnly = false
			case 3:
				bad.UDP = true
			case 4:
				bad.DNS = true
			case 5:
				bad.ExternalNetwork = true
			case 6:
				bad.CredentialResolver = true
			case 7:
				bad.ProviderSetup = true
			case 8:
				bad.Admin = !bad.Admin
			case 9:
				bad.EmbeddingProvider = !bad.EmbeddingProvider
			}
			requireError(t, Preflight(test.policy, bad), PolicyDenied)
		}
	}
	requireError(t, Preflight(NetworkPolicy{"deny", "loopback_admin"}, base), PolicyDenied)
	bad := admin
	bad.UDP = true
	bad.DNS = true
	requireError(t, Preflight(NetworkPolicy{"deny", "loopback_admin"}, bad), PolicyDenied)
	requireError(t, Preflight(NetworkPolicy{"loopback_embeddings", "ipc_only"}, base), PolicyDenied)
	bad = provider
	bad.PreProvisioned = false
	requireError(t, Preflight(NetworkPolicy{"loopback_embeddings", "ipc_only"}, bad), PolicyDenied)
}
