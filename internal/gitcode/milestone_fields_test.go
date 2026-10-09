package gitcode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMilestoneFieldValidationBeforeHTTP(t *testing.T) {
	for _, tc := range []struct {
		name   string
		create bool
		req    MilestoneWriteRequest
		field  string
	}{
		{"long-create", true, MilestoneWriteRequest{Description: strings.Repeat("a", 2001)}, "milestone.description"},
		{"long-update", false, MilestoneWriteRequest{Description: strings.Repeat("😀", 1001)}, "milestone.description"},
		{"invalid-utf8", true, MilestoneWriteRequest{Description: string([]byte{0xff})}, "milestone.description"},
		{"closed-create", true, MilestoneWriteRequest{State: "closed"}, "milestone.state"},
		{"missing-title", false, MilestoneWriteRequest{Title: " "}, "milestone.title"},
		{"missing-date", false, MilestoneWriteRequest{DueOn: " "}, "milestone.due_on"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(500) }))
			defer server.Close()
			req := tc.req
			req.Owner = "example-owner"
			req.Repo = "example-repo"
			req.ID = 1
			if req.Title == "" {
				req.Title = "Fixture"
			}
			if req.DueOn == "" {
				req.DueOn = "2026-12-31"
			}
			client := newTestClient(t, server.URL, Config{})
			var err error
			if tc.create {
				_, err = client.CreateMilestone(context.Background(), req, WriteOptions{IdempotencyKey: "key"})
			} else {
				_, err = client.UpdateMilestone(context.Background(), req, WriteOptions{IdempotencyKey: "key"})
			}
			var validation ErrValidationFailed
			if !errors.As(err, &validation) || validation.Field != tc.field || calls != 0 {
				t.Fatalf("field=%s calls=%d error=%v", validation.Field, calls, err)
			}
		})
	}
}

func TestMilestoneDescriptionUTF16Boundary(t *testing.T) {
	for _, body := range []string{strings.Repeat("a", 2000), strings.Repeat("界", 2000), strings.Repeat("😀", 1000)} {
		if err := validateCreateMilestone(MilestoneWriteRequest{Owner: "example-owner", Repo: "example-repo", Title: "Fixture", Description: body, DueOn: "2026-12-31"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMilestoneCanonicalConfirmationAndSingleAttempt(t *testing.T) {
	for _, create := range []bool{true, false} {
		for _, outcome := range []string{"success", "mismatch", "transport", "missing-identity", "partial-acknowledgement"} {
			t.Run(fmt.Sprintf("create=%t/%s", create, outcome), func(t *testing.T) {
				mutations, reads := 0, 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet {
						mutations++
						if outcome == "transport" {
							w.WriteHeader(503)
							return
						}
						var body map[string]any
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
							t.Error(err)
						}
						if body["description"] != " wanted " {
							t.Error("description bytes changed")
						}
						if create && body["state"] != nil {
							t.Error("create state sent on wire")
						}
						if outcome == "missing-identity" && create {
							fmt.Fprint(w, `{}`)
							return
						}
						if outcome == "partial-acknowledgement" && create {
							fmt.Fprint(w, `{"id":7}`)
							return
						}
						fmt.Fprint(w, `{"id":7,"title":"Fixture","description":" wanted ","state":"open","due_on":"2026-12-31"}`)
						return
					}
					reads++
					if r.URL.Path != "/api/v5/repos/example-owner/example-repo/milestones/7" {
						t.Error("wrong canonical readback route")
					}
					description := " wanted "
					if outcome == "mismatch" {
						description = "different"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"id": 7, "title": "Fixture", "description": description, "state": "open", "due_on": "2026-12-31"})
				}))
				defer server.Close()
				client := newTestClient(t, server.URL, Config{MaxRetries: 3})
				req := MilestoneWriteRequest{Owner: "example-owner", Repo: "example-repo", ID: 7, Title: "Fixture", Description: " wanted ", DueOn: "2026-12-31", State: "open"}
				var result WriteResult[Milestone]
				var err error
				if create {
					result, err = client.CreateMilestone(context.Background(), req, WriteOptions{IdempotencyKey: "key"})
				} else {
					result, err = client.UpdateMilestone(context.Background(), req, WriteOptions{IdempotencyKey: "key"})
				}
				if mutations != 1 {
					t.Fatalf("mutations=%d", mutations)
				}
				if outcome == "success" || (outcome == "missing-identity" || outcome == "partial-acknowledgement") && !create {
					if err != nil || !result.Confirmed || reads != 1 {
						t.Fatalf("confirmation=%+v reads=%d err=%v", result, reads, err)
					}
					return
				}
				var phase ErrWriteMutationPhase
				var identity ErrMilestoneWrite
				if !errors.As(err, &phase) || !phase.MutationAttempted || !errors.As(err, &identity) || result.Confirmed {
					t.Fatalf("missing ambiguous evidence: %v", err)
				}
				if (outcome == "mismatch" || outcome == "partial-acknowledgement") && identity.RemoteID != "7" {
					t.Fatal("canonical identity lost")
				}
				if strings.Contains(err.Error(), "different") {
					t.Fatal("raw provider value leaked")
				}
			})
		}
	}
}
