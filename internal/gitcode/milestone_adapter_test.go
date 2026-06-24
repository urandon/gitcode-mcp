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

func TestMilestone001ListRouteContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/repos/test-owner/test-repo/milestones" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"id":1,"title":"v1.0","description":"First release","state":"open","due_on":"2026-12-31T00:00:00Z","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-06-01T00:00:00Z"}]`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	page, err := client.ListMilestones(context.Background(), MilestoneListRequest{Owner: "test-owner", Repo: "test-repo"})
	if err != nil {
		t.Fatalf("ListMilestones: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("expected 1 milestone, got %d", len(page.Items))
	}
	m := page.Items[0]
	if m.SourceID != "MILESTONE-1" {
		t.Fatalf("SourceID: got %q, want MILESTONE-1", m.SourceID)
	}
	if m.RemoteID != "1" {
		t.Fatalf("RemoteID: got %q, want 1", m.RemoteID)
	}
	if m.Title != "v1.0" {
		t.Fatalf("Title: got %q, want v1.0", m.Title)
	}
	if m.Body != "First release" {
		t.Fatalf("Body: got %q, want First release", m.Body)
	}
	if m.Status != "open" {
		t.Fatalf("Status: got %q, want open", m.Status)
	}
	if m.DueOn != "2026-12-31" {
		t.Fatalf("DueOn: got %q, want 2026-12-31", m.DueOn)
	}
	if m.CreatedAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("CreatedAt: got %q", m.CreatedAt)
	}
	if m.UpdatedAt != "2026-06-01T00:00:00Z" {
		t.Fatalf("UpdatedAt: got %q", m.UpdatedAt)
	}
}

func TestMilestone002GetRouteContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/repos/test-owner/test-repo/milestones/1" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":1,"title":"v1.0","description":"First release","state":"closed","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-06-01T00:00:00Z"}`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	m, err := client.GetMilestone(context.Background(), MilestoneRequest{Owner: "test-owner", Repo: "test-repo", ID: 1})
	if err != nil {
		t.Fatalf("GetMilestone: %v", err)
	}
	if m.SourceID != "MILESTONE-1" {
		t.Fatalf("SourceID: got %q", m.SourceID)
	}
	if m.Status != "closed" {
		t.Fatalf("Status: got %q, want closed", m.Status)
	}
}

func TestMilestone003Pagination(t *testing.T) {
	pageCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/repos/test-owner/test-repo/milestones" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		pageCount++
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Page", "1")
		w.Header().Set("X-Per-Page", "1")
		if pageCount == 1 {
			w.Header().Set("X-Next-Page", "2")
			fmt.Fprint(w, `[{"id":1,"title":"v1.0","state":"open"}]`)
		} else {
			fmt.Fprint(w, `[{"id":2,"title":"v2.0","state":"open"}]`)
		}
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{Pagination: PaginationConfig{PerPage: 1}})
	page, err := client.ListMilestones(context.Background(), MilestoneListRequest{Owner: "test-owner", Repo: "test-repo"})
	if err != nil {
		t.Fatalf("ListMilestones paginated: %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("expected 2 milestones across pages, got %d", len(page.Items))
	}
	if page.Items[0].SourceID != "MILESTONE-1" {
		t.Fatalf("first milestone SourceID: got %q", page.Items[0].SourceID)
	}
	if page.Items[1].SourceID != "MILESTONE-2" {
		t.Fatalf("second milestone SourceID: got %q", page.Items[1].SourceID)
	}
}

func TestMilestone004MatrixGatingDeferred(t *testing.T) {
	matrix := RouteSchemaMatrix{
		specs: map[ProductArea]SurfaceSpec{
			ProductAreaIssues: {
				Area:     ProductAreaIssues,
				Status:   SupportStatusSupported,
				Route:    RouteFamilyAPIV5,
				Evidence: EvidenceClassOpenAPI,
			},
			ProductAreaLabels: {
				Area:     ProductAreaLabels,
				Status:   SupportStatusSupported,
				Route:    RouteFamilyAPIV5,
				Evidence: EvidenceClassOpenAPI,
			},
			ProductAreaMilestones: {
				Area:    ProductAreaMilestones,
				Status:  SupportStatusDeferred,
				Route:   RouteFamilyAPIV5,
				Evidence: EvidenceClassDeferred,
				Diagnostic: &UnsupportedDiagnostic{
					Code:          "unsupported_capability",
					CapabilityKey: "milestones_read",
					Message:       "Milestone reads are deferred",
				},
			},
			ProductAreaWiki: {
				Area:     ProductAreaWiki,
				Status:   SupportStatusSupported,
				Route:    RouteFamilyAPIV5,
				Evidence: EvidenceClassLiveProbe,
			},
			ProductAreaPullRequests: {
				Area:    ProductAreaPullRequests,
				Status:  SupportStatusDeferred,
				Route:   RouteFamilyAPIV5,
				Evidence: EvidenceClassDeferred,
				Diagnostic: &UnsupportedDiagnostic{
					Code:          "unsupported_capability",
					CapabilityKey: "pull_requests_read",
					Message:       "PR reads are deferred",
				},
			},
			ProductAreaComments: {
				Area:    ProductAreaComments,
				Status:  SupportStatusDeferred,
				Route:   RouteFamilyAPIV5,
				Evidence: EvidenceClassDeferred,
				Diagnostic: &UnsupportedDiagnostic{
					Code:          "unsupported_capability",
					CapabilityKey: "comments_read",
					Message:       "Comment reads are deferred",
				},
			},
		},
	}
	cfg := ProviderConfig{Mode: ProviderModeLive, LiveAllowed: true, Token: "token", BaseURL: "https://api.example.com"}
	old := newHTTPClientForProvider
	newHTTPClientForProvider = func(Config) (*HTTPClient, error) { return &HTTPClient{}, nil }
	defer func() { newHTTPClientForProvider = old }()
	provider, err := NewLiveProvider(cfg, WithRouteSchemaMatrix(matrix))
	if err != nil {
		t.Fatalf("NewLiveProvider: %v", err)
	}
	_, err = provider.ListMilestones(context.Background(), MilestoneListRequest{Owner: "o", Repo: "r"})
	if !IsUnsupportedCapability(err) {
		t.Fatalf("expected unsupported capability, got %T %v", err, err)
	}
	_, err = provider.GetMilestone(context.Background(), MilestoneRequest{Owner: "o", Repo: "r", ID: 1})
	if !IsUnsupportedCapability(err) {
		t.Fatalf("expected unsupported capability, got %T %v", err, err)
	}
}

func TestMilestone005TitleRequired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"id":1,"name":"v1.0-only","state":"open"}]`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	_, err := client.ListMilestones(context.Background(), MilestoneListRequest{Owner: "test-owner", Repo: "test-repo"})
	if err == nil {
		t.Fatal("expected error for missing title, got nil")
	}
	var schemaErr *ErrSchemaDecode
	if !errors.As(err, &schemaErr) {
		t.Fatalf("expected *ErrSchemaDecode, got %T: %v", err, err)
	}
	if schemaErr.Field != "milestone.title" {
		t.Fatalf("Field: got %q, want milestone.title", schemaErr.Field)
	}
	if schemaErr.Expected != "non-empty string" {
		t.Fatalf("Expected: got %q, want non-empty string", schemaErr.Expected)
	}
}

func TestMilestone006EmptyTitle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"id":1,"title":"","state":"open"}]`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	_, err := client.ListMilestones(context.Background(), MilestoneListRequest{Owner: "test-owner", Repo: "test-repo"})
	if err == nil {
		t.Fatal("expected error for empty title, got nil")
	}
	var schemaErr *ErrSchemaDecode
	if !errors.As(err, &schemaErr) {
		t.Fatalf("expected *ErrSchemaDecode, got %T: %v", err, err)
	}
	if schemaErr.Field != "milestone.title" {
		t.Fatalf("Field: got %q, want milestone.title", schemaErr.Field)
	}
}

func TestMilestone007IDValidation(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
		field   string
	}{
		{"zero id", `{"id":0,"title":"test","state":"open"}`, true, "milestone.id"},
		{"negative id", `{"id":-1,"title":"test","state":"open"}`, true, "milestone.id"},
		{"nil id", `{"id":null,"title":"test","state":"open"}`, true, "milestone.id"},
		{"bool id", `{"id":true,"title":"test","state":"open"}`, true, "milestone.id"},
		{"object id", `{"id":{},"title":"test","state":"open"}`, true, "milestone.id"},
		{"array id", `{"id":[],"title":"test","state":"open"}`, true, "milestone.id"},
		{"fractional id", `{"id":1.5,"title":"test","state":"open"}`, true, "milestone.id"},
		{"string zero id", `{"id":"0","title":"test","state":"open"}`, true, "milestone.id"},
		{"empty string id", `{"id":"","title":"test","state":"open"}`, true, "milestone.id"},
		{"non-numeric string id", `{"id":"abc","title":"test","state":"open"}`, true, "milestone.id"},
		{"valid string id", `{"id":"7","title":"test","state":"open"}`, false, ""},
		{"valid numeric id", `{"id":7,"title":"test","state":"open"}`, false, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, "/milestones/") {
					fmt.Fprint(w, tt.body)
				} else {
					fmt.Fprintf(w, "[%s]", tt.body)
				}
			}))
			defer server.Close()
			client := newTestClient(t, server.URL, Config{})
			_, err := client.ListMilestones(context.Background(), MilestoneListRequest{Owner: "test-owner", Repo: "test-repo"})
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				var schemaErr *ErrSchemaDecode
				if !errors.As(err, &schemaErr) {
					t.Fatalf("expected *ErrSchemaDecode, got %T: %v", err, err)
				}
				if schemaErr.Field != tt.field {
					t.Fatalf("Field: got %q, want %q", schemaErr.Field, tt.field)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestMilestone008HTTP400NotTransportFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"message":"bad request"}`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	_, err := client.ListMilestones(context.Background(), MilestoneListRequest{Owner: "test-owner", Repo: "test-repo"})
	if err == nil {
		t.Fatal("expected error for HTTP 400, got nil")
	}
	var apiErr ErrAPIValidation
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected ErrAPIValidation, got %T: %v", err, err)
	}
	var netErr ErrNetworkUnavailable
	if errors.As(err, &netErr) {
		t.Fatalf("HTTP 400 incorrectly matches ErrNetworkUnavailable")
	}
	if apiErr.Status != http.StatusBadRequest {
		t.Fatalf("Status: got %d, want 400", apiErr.Status)
	}
}

func TestMilestone009HTTP404(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"message":"not found"}`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	_, err := client.GetMilestone(context.Background(), MilestoneRequest{Owner: "test-owner", Repo: "test-repo", ID: 999})
	if err == nil {
		t.Fatal("expected error for HTTP 404, got nil")
	}
	var notFoundErr ErrNotFound
	if !errors.As(err, &notFoundErr) {
		t.Fatalf("expected ErrNotFound, got %T: %v", err, err)
	}
	var netErr ErrNetworkUnavailable
	if errors.As(err, &netErr) {
		t.Fatalf("HTTP 404 incorrectly matches ErrNetworkUnavailable")
	}
}

func TestMilestone010MalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `not json`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	_, err := client.ListMilestones(context.Background(), MilestoneListRequest{Owner: "test-owner", Repo: "test-repo"})
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
	var partialErr ErrPartialResponse
	if !errors.As(err, &partialErr) {
		t.Fatalf("expected ErrPartialResponse for malformed JSON, got %T: %v", err, err)
	}
	var netErr ErrNetworkUnavailable
	if errors.As(err, &netErr) {
		t.Fatalf("malformed JSON incorrectly matches ErrNetworkUnavailable")
	}
}

func TestMilestone011StatusNormalization(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   string
	}{
		{"open unchanged", `"open"`, "open"},
		{"active becomes open", `"active"`, "open"},
		{"closed unchanged", `"closed"`, "closed"},
		{"absent defaults to open", ``, "open"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"id":1,"title":"test"}`, )
			if tt.status != `` {
				body = fmt.Sprintf(`{"id":1,"title":"test","state":%s}`, tt.status)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			client := newTestClient(t, server.URL, Config{})
			m, err := client.GetMilestone(context.Background(), MilestoneRequest{Owner: "test-owner", Repo: "test-repo", ID: 1})
			if err != nil {
				t.Fatalf("GetMilestone: %v", err)
			}
			if m.Status != tt.want {
				t.Fatalf("Status: got %q, want %q", m.Status, tt.want)
			}
		})
	}
}

func TestMilestone012StatusFromStatusField(t *testing.T) {
	body := `{"id":1,"title":"test","status":"closed"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	m, err := client.GetMilestone(context.Background(), MilestoneRequest{Owner: "test-owner", Repo: "test-repo", ID: 1})
	if err != nil {
		t.Fatalf("GetMilestone: %v", err)
	}
	if m.Status != "closed" {
		t.Fatalf("Status: got %q, want closed from status field", m.Status)
	}
}

func TestMilestone012bUnrecognizedStatusSchemaDecode(t *testing.T) {
	body := `{"id":1,"title":"test","state":"in_progress"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	_, err := client.GetMilestone(context.Background(), MilestoneRequest{Owner: "test-owner", Repo: "test-repo", ID: 1})
	if err == nil {
		t.Fatal("expected error for unrecognized status, got nil")
	}
	var schemaErr *ErrSchemaDecode
	if !errors.As(err, &schemaErr) {
		t.Fatalf("expected *ErrSchemaDecode, got %T: %v", err, err)
	}
	if schemaErr.Field != "milestone.state" {
		t.Fatalf("Field: got %q, want milestone.state", schemaErr.Field)
	}
}

func TestMilestone013DateParsing(t *testing.T) {
	tests := []struct {
		name   string
		dueOn  string
		want   string
		errMsg string
	}{
		{"YYYY-MM-DD", `"2026-12-31"`, "2026-12-31", ""},
		{"RFC3339", `"2026-12-31T00:00:00Z"`, "2026-12-31", ""},
		{"absent", ``, "", ""},
		{"unparseable", `"not-a-date"`, "", "due_on"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"id":1,"title":"test","state":"open"}`
			if tt.dueOn != `` {
				body = fmt.Sprintf(`{"id":1,"title":"test","state":"open","due_on":%s}`, tt.dueOn)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			client := newTestClient(t, server.URL, Config{})
			m, err := client.GetMilestone(context.Background(), MilestoneRequest{Owner: "test-owner", Repo: "test-repo", ID: 1})
			if tt.errMsg != "" {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				var schemaErr *ErrSchemaDecode
				if !errors.As(err, &schemaErr) {
					t.Fatalf("expected *ErrSchemaDecode, got %T: %v", err, err)
				}
				if schemaErr.Field != "milestone.due_on" {
					t.Fatalf("Field: got %q, want milestone.due_on", schemaErr.Field)
				}
				return
			}
			if err != nil {
				t.Fatalf("GetMilestone: %v", err)
			}
			if m.DueOn != tt.want {
				t.Fatalf("DueOn: got %q, want %q", m.DueOn, tt.want)
			}
		})
	}
}

func TestMilestone014DueDateFallback(t *testing.T) {
	body := `{"id":1,"title":"test","state":"open","due_date":"2026-06-15"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	m, err := client.GetMilestone(context.Background(), MilestoneRequest{Owner: "test-owner", Repo: "test-repo", ID: 1})
	if err != nil {
		t.Fatalf("GetMilestone: %v", err)
	}
	if m.DueOn != "2026-06-15" {
		t.Fatalf("DueOn from due_date: got %q, want 2026-06-15", m.DueOn)
	}
}

func TestMilestone015DueOnPreferredOverDueDate(t *testing.T) {
	body := `{"id":1,"title":"test","state":"open","due_on":"2026-12-25","due_date":"2026-06-15"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	m, err := client.GetMilestone(context.Background(), MilestoneRequest{Owner: "test-owner", Repo: "test-repo", ID: 1})
	if err != nil {
		t.Fatalf("GetMilestone: %v", err)
	}
	if m.DueOn != "2026-12-25" {
		t.Fatalf("DueOn: got %q, want 2026-12-25 (due_on preferred)", m.DueOn)
	}
}

func TestMilestone016IDMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/repos/test-owner/test-repo/milestones/1" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":99,"title":"v1.0","state":"open"}`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	_, err := client.GetMilestone(context.Background(), MilestoneRequest{Owner: "test-owner", Repo: "test-repo", ID: 1})
	if err == nil {
		t.Fatal("expected error for id mismatch, got nil")
	}
	var schemaErr *ErrSchemaDecode
	if !errors.As(err, &schemaErr) {
		t.Fatalf("expected *ErrSchemaDecode, got %T: %v", err, err)
	}
	if schemaErr.Field != "milestone.id" {
		t.Fatalf("Field: got %q, want milestone.id", schemaErr.Field)
	}
	if !strings.Contains(schemaErr.Error(), "99") {
		t.Fatalf("error should mention mismatched id 99: %v", err)
	}
}

func TestMilestone017MixedListFailure(t *testing.T) {
	var elementIndex int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elementIndex++
		w.Header().Set("Content-Type", "application/json")
		jsonOutput := fmt.Sprintf(
			`[{"id":1,"title":"valid","state":"open"},{"id":%d,"title":"","state":"open"}]`,
			elementIndex+1,
		)
		fmt.Fprint(w, jsonOutput)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	_, err := client.ListMilestones(context.Background(), MilestoneListRequest{Owner: "test-owner", Repo: "test-repo"})
	if err == nil {
		t.Fatal("expected error for mixed valid/invalid list, got nil")
	}
	var schemaErr *ErrSchemaDecode
	if !errors.As(err, &schemaErr) {
		t.Fatalf("expected *ErrSchemaDecode, got %T: %v", err, err)
	}
}

func TestMilestone018DescriptionPreferredOverBody(t *testing.T) {
	body := `{"id":1,"title":"test","description":"desc text","body":"body text","state":"open"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	m, err := client.GetMilestone(context.Background(), MilestoneRequest{Owner: "test-owner", Repo: "test-repo", ID: 1})
	if err != nil {
		t.Fatalf("GetMilestone: %v", err)
	}
	if m.Body != "desc text" {
		t.Fatalf("Body: got %q, want desc text (description preferred over body)", m.Body)
	}
}

func TestMilestone019BodyFallback(t *testing.T) {
	body := `{"id":1,"title":"test","body":"body text","state":"open"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	m, err := client.GetMilestone(context.Background(), MilestoneRequest{Owner: "test-owner", Repo: "test-repo", ID: 1})
	if err != nil {
		t.Fatalf("GetMilestone: %v", err)
	}
	if m.Body != "body text" {
		t.Fatalf("Body: got %q, want body text", m.Body)
	}
}

func TestMilestone020HTMLURL(t *testing.T) {
	body := `{"id":1,"title":"test","html_url":"https://gitcode.com/test-owner/test-repo/milestones/1","state":"open"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	m, err := client.GetMilestone(context.Background(), MilestoneRequest{Owner: "test-owner", Repo: "test-repo", ID: 1})
	if err != nil {
		t.Fatalf("GetMilestone: %v", err)
	}
	if m.HTMLURL != "https://gitcode.com/test-owner/test-repo/milestones/1" {
		t.Fatalf("HTMLURL: got %q", m.HTMLURL)
	}
}

func TestMilestone021TimestampValidation(t *testing.T) {
	tests := []struct {
		name      string
		createdAt string
		updatedAt string
		wantErr   bool
	}{
		{"valid timestamps", `"2026-01-01T00:00:00Z"`, `"2026-06-01T00:00:00Z"`, false},
		{"absent timestamps", ``, ``, false},
		{"invalid created_at", `"not-a-time"`, `"2026-06-01T00:00:00Z"`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"id":1,"title":"test","state":"open"}`
			if tt.createdAt != `` {
				body = fmt.Sprintf(`{"id":1,"title":"test","state":"open","created_at":%s}`, tt.createdAt)
				if tt.updatedAt != `` {
					body = fmt.Sprintf(`{"id":1,"title":"test","state":"open","created_at":%s,"updated_at":%s}`, tt.createdAt, tt.updatedAt)
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			client := newTestClient(t, server.URL, Config{})
			_, err := client.GetMilestone(context.Background(), MilestoneRequest{Owner: "test-owner", Repo: "test-repo", ID: 1})
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				var schemaErr *ErrSchemaDecode
				if !errors.As(err, &schemaErr) {
					t.Fatalf("expected *ErrSchemaDecode, got %T: %v", err, err)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			}
		})
	}
}

func TestMilestone022LiveProviderMatricesThroughCorrectly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v5/repos/test-owner/test-repo/milestones":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `[{"id":1,"title":"v1.0","state":"open"}]`)
		case "/api/v5/repos/test-owner/test-repo/milestones/1":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":1,"title":"v1.0","state":"open"}`)
		case "/api/v5/repos/test-owner/test-repo":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"1","owner":"test-owner","name":"test-repo","full_name":"test-owner/test-repo"}`)
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	old := newHTTPClientForProvider
	newHTTPClientForProvider = func(cfg Config) (*HTTPClient, error) {
		cfg.BaseURL = server.URL
		return NewHTTPClient(cfg)
	}
	defer func() { newHTTPClientForProvider = old }()

	provider, err := NewLiveProvider(ProviderConfig{
		Mode:        ProviderModeLive,
		LiveAllowed: true,
		Token:       "token",
		BaseURL:     server.URL,
	})
	if err != nil {
		t.Fatalf("NewLiveProvider: %v", err)
	}

	page, err := provider.ListMilestones(context.Background(), MilestoneListRequest{Owner: "test-owner", Repo: "test-repo"})
	if err != nil {
		t.Fatalf("ListMilestones via provider: %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].SourceID != "MILESTONE-1" {
		t.Fatalf("list result: %+v", page)
	}

	m, err := provider.GetMilestone(context.Background(), MilestoneRequest{Owner: "test-owner", Repo: "test-repo", ID: 1})
	if err != nil {
		t.Fatalf("GetMilestone via provider: %v", err)
	}
	if m.SourceID != "MILESTONE-1" {
		t.Fatalf("get result: %+v", m)
	}
}

func TestMilestone023ListMilestonesStateQuery(t *testing.T) {
	var capturedQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[]`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	_, err := client.ListMilestones(context.Background(), MilestoneListRequest{Owner: "test-owner", Repo: "test-repo", State: "closed"})
	if err != nil {
		t.Fatalf("ListMilestones: %v", err)
	}
	if !strings.Contains(capturedQuery, "state=closed") {
		t.Fatalf("query missing state=closed: %q", capturedQuery)
	}
}

func TestMilestone024MilestoneUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		want    Milestone
		wantErr bool
		errField string
	}{
		{
			name: "full milestone",
			json: `{"id":10,"title":"Sprint 1","description":"First sprint","state":"open","due_on":"2026-07-01","created_at":"2026-06-01T00:00:00Z","updated_at":"2026-06-15T00:00:00Z","html_url":"https://example.com/milestones/10"}`,
			want: Milestone{RemoteID: "10", SourceID: "MILESTONE-10", Title: "Sprint 1", Body: "First sprint", Status: "open", DueOn: "2026-07-01", CreatedAt: "2026-06-01T00:00:00Z", UpdatedAt: "2026-06-15T00:00:00Z", HTMLURL: "https://example.com/milestones/10"},
		},
		{
			name: "minimal milestone",
			json: `{"id":1,"title":"minimal"}`,
			want: Milestone{RemoteID: "1", SourceID: "MILESTONE-1", Title: "minimal", Status: "open"},
		},
		{
			name:    "missing id",
			json:    `{"title":"no id","state":"open"}`,
			wantErr: true,
			errField: "milestone.id",
		},
		{
			name:    "missing title with name only",
			json:    `{"id":1,"name":"only name","state":"open"}`,
			wantErr: true,
			errField: "milestone.title",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var m Milestone
			err := json.Unmarshal([]byte(tt.json), &m)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				var schemaErr *ErrSchemaDecode
				if !errors.As(err, &schemaErr) {
					t.Fatalf("expected *ErrSchemaDecode, got %T: %v", err, err)
				}
				if tt.errField != "" && schemaErr.Field != tt.errField {
					t.Fatalf("Field: got %q, want %q", schemaErr.Field, tt.errField)
				}
				return
			}
			if err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if m.RemoteID != tt.want.RemoteID {
				t.Fatalf("RemoteID: got %q, want %q", m.RemoteID, tt.want.RemoteID)
			}
			if m.SourceID != tt.want.SourceID {
				t.Fatalf("SourceID: got %q, want %q", m.SourceID, tt.want.SourceID)
			}
			if m.Title != tt.want.Title {
				t.Fatalf("Title: got %q, want %q", m.Title, tt.want.Title)
			}
			if m.Body != tt.want.Body {
				t.Fatalf("Body: got %q, want %q", m.Body, tt.want.Body)
			}
			if m.Status != tt.want.Status {
				t.Fatalf("Status: got %q, want %q", m.Status, tt.want.Status)
			}
			if m.DueOn != tt.want.DueOn {
				t.Fatalf("DueOn: got %q, want %q", m.DueOn, tt.want.DueOn)
			}
			if m.CreatedAt != tt.want.CreatedAt {
				t.Fatalf("CreatedAt: got %q, want %q", m.CreatedAt, tt.want.CreatedAt)
			}
			if m.UpdatedAt != tt.want.UpdatedAt {
				t.Fatalf("UpdatedAt: got %q, want %q", m.UpdatedAt, tt.want.UpdatedAt)
			}
			if m.HTMLURL != tt.want.HTMLURL {
				t.Fatalf("HTMLURL: got %q, want %q", m.HTMLURL, tt.want.HTMLURL)
			}
		})
	}
}

func TestMilestone025FixtureProviderReturnsReadOnly(t *testing.T) {
	provider := mustFixtureProvider(t)
	_, err := provider.ListMilestones(context.Background(), MilestoneListRequest{Owner: "example-owner", Repo: "example-repo"})
	if !IsFixtureReadOnly(err) {
		t.Fatalf("expected fixture read-only for ListMilestones, got %T %v", err, err)
	}
	_, err = provider.GetMilestone(context.Background(), MilestoneRequest{Owner: "example-owner", Repo: "example-repo", ID: 1})
	if !IsFixtureReadOnly(err) {
		t.Fatalf("expected fixture read-only for GetMilestone, got %T %v", err, err)
	}
}

func TestMilestone026UnavailableProvider(t *testing.T) {
	provider := NewUnavailableProvider("milestones unavailable")
	_, err := provider.ListMilestones(context.Background(), MilestoneListRequest{Owner: "o", Repo: "r"})
	if !IsProviderUnavailable(err) {
		t.Fatalf("expected provider unavailable, got %T %v", err, err)
	}
	_, err = provider.GetMilestone(context.Background(), MilestoneRequest{Owner: "o", Repo: "r", ID: 1})
	if !IsProviderUnavailable(err) {
		t.Fatalf("expected provider unavailable, got %T %v", err, err)
	}
}

func TestMilestone027SchemaDecodeNotTransportOrConfigFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"id":1,"title":"","state":"open"}]`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	_, err := client.ListMilestones(context.Background(), MilestoneListRequest{Owner: "test-owner", Repo: "test-repo"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	assertNotTransportError(t, err)
	assertNotConfigCredentialError(t, err)

	var schemaErr *ErrSchemaDecode
	if !errors.As(err, &schemaErr) {
		t.Fatalf("expected *ErrSchemaDecode, got %T: %v", err, err)
	}
	if schemaErr.DiagnosticCode() != "schema_decode" {
		t.Fatalf("DiagnosticCode: got %q, want schema_decode", schemaErr.DiagnosticCode())
	}
}

func TestMilestone028HTTP400NotTransportOrConfigFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"message":"bad request"}`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	_, err := client.GetMilestone(context.Background(), MilestoneRequest{Owner: "test-owner", Repo: "test-repo", ID: 1})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	assertNotTransportError(t, err)
	assertNotConfigCredentialError(t, err)

	var apiErr ErrAPIValidation
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected ErrAPIValidation, got %T: %v", err, err)
	}
	if apiErr.DiagnosticCode() != "api_validation" {
		t.Fatalf("DiagnosticCode: got %q, want api_validation", apiErr.DiagnosticCode())
	}
}

func assertNotTransportError(t *testing.T, err error) {
	t.Helper()
	var netErr ErrNetworkUnavailable
	if errors.As(err, &netErr) {
		t.Fatalf("error incorrectly matches ErrNetworkUnavailable: %v", err)
	}
}

func assertNotConfigCredentialError(t *testing.T, err error) {
	t.Helper()
	var authErr ErrAuthExpired
	if errors.As(err, &authErr) {
		t.Fatalf("error incorrectly matches ErrAuthExpired: %v", err)
	}
	var providerErr ErrProviderUnavailable
	if errors.As(err, &providerErr) {
		t.Fatalf("error incorrectly matches ErrProviderUnavailable: %v", err)
	}
}

func TestMilestone029ValidationFailedForInvalidRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not reach server")
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})

	_, err := client.ListMilestones(context.Background(), MilestoneListRequest{Owner: "", Repo: "repo"})
	if err == nil {
		t.Fatal("expected validation error for empty owner")
	}
	var valErr ErrValidationFailed
	if !errors.As(err, &valErr) {
		t.Fatalf("expected ErrValidationFailed, got %T: %v", err, err)
	}

	_, err = client.GetMilestone(context.Background(), MilestoneRequest{Owner: "owner", Repo: "repo", ID: 0})
	if err == nil {
		t.Fatal("expected validation error for zero id")
	}
	var valErr2 ErrValidationFailed
	if !errors.As(err, &valErr2) {
		t.Fatalf("expected ErrValidationFailed, got %T: %v", err, err)
	}
	if valErr2.Field != "milestone.id" {
		t.Fatalf("Field: got %q, want milestone.id", valErr2.Field)
	}
}

func TestMilestone030UnknownFieldsIgnored(t *testing.T) {
	body := `{"id":1,"title":"test","unknown_field":"should be ignored","state":"open"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	m, err := client.GetMilestone(context.Background(), MilestoneRequest{Owner: "test-owner", Repo: "test-repo", ID: 1})
	if err != nil {
		t.Fatalf("GetMilestone with unknown field: %v", err)
	}
	if m.Title != "test" {
		t.Fatalf("Title: got %q, want test", m.Title)
	}
}

func TestMilestone031MultiMilestoneList(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"id":1,"title":"v1.0","state":"open"},{"id":2,"title":"v2.0","state":"closed","due_on":"2026-12-31"},{"id":3,"title":"v3.0","description":"Next","state":"active","due_date":"2027-01-15"}]`)
	}))
	defer server.Close()
	client := newTestClient(t, server.URL, Config{})
	page, err := client.ListMilestones(context.Background(), MilestoneListRequest{Owner: "test-owner", Repo: "test-repo"})
	if err != nil {
		t.Fatalf("ListMilestones: %v", err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("expected 3 milestones, got %d", len(page.Items))
	}
	if page.Items[0].SourceID != "MILESTONE-1" || page.Items[0].Title != "v1.0" || page.Items[0].Status != "open" {
		t.Fatalf("item 0: %+v", page.Items[0])
	}
	if page.Items[1].SourceID != "MILESTONE-2" || page.Items[1].Title != "v2.0" || page.Items[1].Status != "closed" || page.Items[1].DueOn != "2026-12-31" {
		t.Fatalf("item 1: %+v", page.Items[1])
	}
	if page.Items[2].SourceID != "MILESTONE-3" || page.Items[2].Title != "v3.0" || page.Items[2].Status != "open" || page.Items[2].Body != "Next" || page.Items[2].DueOn != "2027-01-15" {
		t.Fatalf("item 2: %+v", page.Items[2])
	}
}
