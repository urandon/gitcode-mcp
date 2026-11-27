package feedback

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestPreparedDraftRoundTripIsSanitizedData(t *testing.T) {
	draft := validDraft()
	draft.Evidence = []string{"Authorization: Bearer synthetic-feedback-secret", "cache /Users/example/private/cache.db was used"}
	before := append([]string(nil), draft.Evidence...)
	cfg := DefaultConfig()
	cfg.Enabled = true
	prepared, err := Prepare(draft, testContext(), cfg, nil)
	if err != nil {
		t.Fatal("preparation failed")
	}
	data, err := json.Marshal(prepared)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"synthetic-feedback-secret", "/Users/example"} {
		if strings.Contains(string(data), private) {
			t.Fatal("portable draft exposed private input")
		}
	}
	if !reflect.DeepEqual(draft.Evidence, before) {
		t.Fatal("preparation mutated caller evidence")
	}
	// These are untrusted projections, not an accepted write body or destination.
	prepared.Body, prepared.RepoID, prepared.Fingerprint = "forged body", "example/forged", "forged fingerprint"
	data, _ = json.Marshal(prepared)
	decoded, err := DecodeDraftInput(data)
	if err != nil {
		t.Fatal("prepared output cannot be reused")
	}
	again, err := Prepare(decoded, testContext(), cfg, nil)
	if err != nil || again.Fingerprint == "forged fingerprint" || again.RepoID != cfg.RepoID || strings.Contains(again.Body, "forged body") || !reflect.DeepEqual(again.Draft, prepared.Draft) {
		t.Fatal("portable draft trusted projections or changed normalized input")
	}
}

func TestDraftInputRejectsMalformedOrAmbiguousEnvelopes(t *testing.T) {
	for _, input := range []string{
		`null`, `[]`, `{"draft":null}`, `{"draft":"report"}`,
		`{"draft":{},"summary":"mixed"}`, `{"draft":{"repo_id":"example/other"}}`,
		`{"draft":{},"unknown":true}`, `{"unknown":true}`, `{} {}`,
		strings.Repeat(" ", MaxDraftInputBytes+1),
	} {
		if _, err := DecodeDraftInput([]byte(input)); err == nil {
			t.Fatal("invalid draft input accepted")
		}
	}
}
