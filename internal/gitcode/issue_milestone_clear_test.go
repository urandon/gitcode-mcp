package gitcode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUpdateIssueClearUsesNumericProviderSentinel(t *testing.T) {
	for _, stateChange := range []bool{false, true} {
		t.Run(map[bool]string{false: "clear only", true: "state and clear"}[stateChange], func(t *testing.T) {
			state := "open"
			var milestone any = map[string]any{"id": 7, "title": "Fixture release", "state": "open"}
			gets, patches := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path != "/api/v5/repos/example-owner/example-repo/issues/42" {
					t.Error("unexpected issue route")
					w.WriteHeader(http.StatusNotFound)
					return
				}
				switch r.Method {
				case http.MethodGet:
					gets++
				case http.MethodPatch:
					patches++
					var payload map[string]json.RawMessage
					if json.NewDecoder(r.Body).Decode(&payload) != nil {
						t.Error("invalid fixture request")
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					if string(payload["state"]) == `"close"` {
						state = "closed"
					}
					// Current provider leaves null/omission unchanged. Numeric
					// zero is the clear intent used by its milestone picker.
					if string(payload["milestone"]) == "0" {
						milestone = nil
					}
				default:
					t.Error("unexpected method")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": "42", "number": 42, "title": "Fixture issue",
					"body": "Original fixture body", "state": state,
					"labels": []any{}, "milestone": milestone,
				})
			}))
			defer server.Close()
			request := UpdateIssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 42, Milestone: EncodeIssueMilestone("null")}
			wantState := "open"
			if stateChange {
				request.State, wantState = "closed", "closed"
			}
			result, err := newTestClient(t, server.URL, Config{}).UpdateIssue(context.Background(), request, WriteOptions{IdempotencyKey: "fixture-explicit-clear"})
			if err != nil {
				t.Fatalf("clear must be canonically confirmed: %v", err)
			}
			if result.Record.State != wantState || result.Record.Milestone != nil || result.Record.Title != "Fixture issue" || result.Record.Body != "Original fixture body" {
				t.Fatal("update did not preserve omitted fields and confirm clear")
			}
			if patches != 1 || gets != 2 {
				t.Fatalf("requests: PATCH=%d GET=%d, want one mutation plus preimage/readback", patches, gets)
			}
		})
	}
}

func TestUpdateIssueMilestonePayloadKeepsOmissionAndAssignmentDistinct(t *testing.T) {
	for _, tc := range []struct{ name, logical, wire string }{
		{"omitted", "", ""}, {"assigned", "7", "7"}, {"cleared", "null", "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := UpdateIssueRequest{Milestone: EncodeIssueMilestone(tc.logical)}
			body, err := json.Marshal(updateIssuePayload(req))
			if err != nil {
				t.Fatal(err)
			}
			var payload map[string]json.RawMessage
			if json.Unmarshal(body, &payload) != nil || string(payload["milestone"]) != tc.wire {
				t.Fatal("incorrect wire intent")
			}
			if string(req.Milestone) != tc.logical {
				t.Fatal("wire translation mutated logical readback intent")
			}
		})
	}
}
