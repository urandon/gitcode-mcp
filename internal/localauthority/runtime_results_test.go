package localauthority

import (
	"encoding/json"
	"strings"
	"testing"

	"gitcode-mcp/internal/authority"
)

func TestRuntimeOriginalOperationResultStrictBinding(t *testing.T) {
	request := authority.OriginalOperationRequest{ProjectID: testProject, IdempotencyKey: "original-key"}
	digest, _ := request.KeyHash()
	state := DocumentOperationState{Schema: 1, ProjectID: testProject, OperationID: "22222222-2222-4222-8222-222222222222", EntityID: "33333333-3333-4333-8333-333333333333", Scope: "document_operation", Phase: "settled", PinState: "cleared", Remediation: "none"}
	result := DocumentOperationResult{Schema: 1, IdempotencyKeyHash: digest, State: state}
	raw, _ := json.Marshal(result)
	var decoded DocumentOperationResult
	if authority.DecodeJSON(raw, &decoded) != nil || decoded.Match(request) != nil || strings.Contains(string(raw), request.IdempotencyKey) {
		t.Fatal("protected original-key result failed")
	}
	other := request
	other.IdempotencyKey = "another-key"
	if decoded.Match(other) == nil {
		t.Fatal("cross-key swapped result admitted")
	}
	other = request
	other.ProjectID = state.OperationID
	if decoded.Match(other) == nil {
		t.Fatal("cross-project operation result admitted")
	}
	for _, mutate := range []func(map[string]any){
		func(m map[string]any) { delete(m, "idempotency_key_hash") },
		func(m map[string]any) { m["idempotency_key_hash"] = nil },
		func(m map[string]any) { m["state"] = nil },
		func(m map[string]any) { m["schema"] = 2 },
		func(m map[string]any) { m["idempotency_key"] = "original-key" },
	} {
		var fields map[string]any
		_ = json.Unmarshal(raw, &fields)
		mutate(fields)
		invalid, _ := json.Marshal(fields)
		if authority.DecodeJSON(invalid, &DocumentOperationResult{}) == nil {
			t.Fatal("malformed original-key result admitted")
		}
	}
}

func TestRuntimeAuditResponseStrictProjectAndPageBinding(t *testing.T) {
	entry := AuditEntry{Sequence: 1, OperationID: "22222222-2222-4222-8222-222222222222", EntityID: "33333333-3333-4333-8333-333333333333", Revision: 1, Operation: "issue.create", KeyHash: hash([]byte("key")), IntentHash: hash([]byte("intent")), ActorHash: hash([]byte("actor")), RecordedAt: "2026-01-01T00:00:00Z"}
	request := authority.AuditRequest{ProjectID: testProject, Limit: 1}
	result := AuditResult{ProjectID: testProject, Entries: []AuditEntry{entry}}
	raw, _ := json.Marshal(result)
	var decoded AuditResult
	if authority.DecodeJSON(raw, &decoded) != nil || decoded.Match(request) != nil {
		t.Fatal("valid protected audit page refused")
	}
	other := request
	other.ProjectID = entry.OperationID
	if decoded.Match(other) == nil {
		t.Fatal("swapped project audit admitted")
	}
	other = request
	other.After = 1
	if decoded.Match(other) == nil {
		t.Fatal("audit page ignored after barrier")
	}
	for _, mutate := range []func(*AuditResult){
		func(r *AuditResult) { r.Entries = nil },
		func(r *AuditResult) { r.Entries = append(r.Entries, r.Entries[0]) },
		func(r *AuditResult) { r.Entries[0].RecordedAt = "2026-01-01T01:00:00+01:00" },
		func(r *AuditResult) { r.Entries[0].Operation = "synthetic-unknown" },
		func(r *AuditResult) { r.Entries[0].Operation = "issue.update" },
		func(r *AuditResult) { r.Entries[0].Sequence = 2; r.Entries[0].Revision = 2 },
		func(r *AuditResult) { r.Entries[0].KeyHash = strings.ToUpper(r.Entries[0].KeyHash) },
	} {
		candidate := result
		candidate.Entries = append([]AuditEntry(nil), result.Entries...)
		mutate(&candidate)
		invalid, _ := json.Marshal(candidate)
		if authority.DecodeJSON(invalid, &AuditResult{}) == nil {
			t.Fatal("invalid audit entries admitted")
		}
	}
	second := entry
	second.Sequence = 2
	second.OperationID = "44444444-4444-4444-8444-444444444444"
	result.Entries = append(result.Entries, second)
	if result.Match(request) == nil {
		t.Fatal("over-limit requested audit page admitted")
	}
	result.Entries[0], result.Entries[1] = result.Entries[1], result.Entries[0]
	if result.Validate() == nil {
		t.Fatal("out-of-order audit admitted")
	}
	for _, raw := range []string{`{"project_id":"11111111-1111-4111-8111-111111111111","entries":null}`, `{"entries":[]}`, `{"project_id":"11111111-1111-4111-8111-111111111111","entries":[],"cursor":"unexpected"}`} {
		if authority.DecodeJSON([]byte(raw), &AuditResult{}) == nil {
			t.Fatal("null/missing/extra audit envelope admitted")
		}
	}
	if (AuditResult{ProjectID: testProject, Entries: []AuditEntry{}}).Match(request) != nil {
		t.Fatal("empty bounded audit page refused")
	}
}
