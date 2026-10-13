package authority

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestRuntimeProtectedRequestsStrictAndBounded(t *testing.T) {
	project := UUID("11111111-1111-4111-8111-111111111111")
	request := AttributedMutationRequest{ActorID: "synthetic-agent", Request: MutationRequest{Intent: WriteIntent{ProjectID: project, WriteMode: "local", IdempotencyKey: "original-key"}, Operation: "issue.create", Payload: json.RawMessage(`{"title":"Synthetic","body":"","state":"open"}`)}}
	raw, _ := json.Marshal(request)
	if _, err := DecodeRuntimeRequest("MutateAttributed", raw); err != nil {
		t.Fatal("valid attributed mutation refused")
	}
	for _, actor := range []string{"", " padded", "line\nbreak", "nul\x00value", "del\x7fvalue", strings.Repeat("a", 129), string([]byte{0xff})} {
		candidate := request
		candidate.ActorID = actor
		if candidate.Validate() == nil {
			t.Fatal("invalid actor admitted")
		}
	}
	for _, raw := range []string{
		`{"request":{}}`, `{"actor_id":"synthetic","request":null}`, `{"actor_id":"synthetic","request":{},"path":"synthetic"}`,
		`{"actor_id":"synthetic","actor_id":"other","request":{}}`,
	} {
		if _, err := DecodeRuntimeRequest("MutateAttributed", []byte(raw)); err == nil {
			t.Fatal("malformed attribution envelope admitted")
		}
	}
	for _, method := range []string{"SetupKnowledge", "Migrate", "Rebind", "Search", "read", "AuthorityV1.Read"} {
		if _, err := DecodeRuntimeRequest(method, []byte(`{}`)); err == nil {
			t.Fatal("unknown or unimplemented dispatch admitted")
		}
	}
	legacy, _ := json.Marshal(request.Request)
	if _, err := DecodeRuntimeRequest("Mutate", legacy); err != nil {
		t.Fatal("legacy mutation shape changed")
	}
	request.Request.Operation = "document.create"
	request.Request.Payload = json.RawMessage(`{"title":"Synthetic","markdown":"Example"}`)
	if request.Validate() == nil {
		t.Fatal("generic attributed mutation admitted document effects")
	}
	for _, request := range []AuditRequest{{ProjectID: project, Limit: 0}, {ProjectID: project, Limit: 101}, {ProjectID: project, After: math.MaxUint64, Limit: 1}, {Limit: 1}} {
		if request.Validate() == nil {
			t.Fatal("unbounded audit request admitted")
		}
	}
}

func TestRuntimeOriginalKeysAndCleanupCannotRelaxIntent(t *testing.T) {
	project := UUID("11111111-1111-4111-8111-111111111111")
	r := OriginalOperationRequest{ProjectID: project, IdempotencyKey: "original-key"}
	first, err := r.KeyHash()
	if err != nil || !digestValid(first, 64) {
		t.Fatal("protected key digest invalid")
	}
	other := r
	other.IdempotencyKey = "another-key"
	second, _ := other.KeyHash()
	other = r
	other.ProjectID = "22222222-2222-4222-8222-222222222222"
	third, _ := other.KeyHash()
	if first == second || first == third || second == third {
		t.Fatal("key digest lost original project/key binding")
	}
	for _, key := range []string{"", " leading", "nul\x00value", strings.Repeat("k", 129)} {
		other := r
		other.IdempotencyKey = key
		if _, err := other.KeyHash(); err == nil {
			t.Fatal("invalid original key hashed as valid")
		}
	}
	for _, mode := range []string{"", "live", "plan"} {
		if (CleanupDocumentRequest{ProjectID: project, IdempotencyKey: r.IdempotencyKey, WriteMode: mode}).Validate() == nil {
			t.Fatal("cleanup accepted nonlocal write intent")
		}
	}
	cleanup := CleanupDocumentRequest{ProjectID: project, IdempotencyKey: r.IdempotencyKey, WriteMode: "local"}
	if cleanup.Validate() != nil || cleanup.Original() != r {
		t.Fatal("cleanup changed original intent")
	}
	for _, raw := range []string{`{"project_id":"11111111-1111-4111-8111-111111111111","idempotency_key":null}`, `{"project_id":"11111111-1111-4111-8111-111111111111","idempotency_key":"original-key","write_mode":"local"}`} {
		if _, err := DecodeRuntimeRequest("DocumentOperationStatus", []byte(raw)); err == nil {
			t.Fatal("status admitted missing key or write override")
		}
	}
}
