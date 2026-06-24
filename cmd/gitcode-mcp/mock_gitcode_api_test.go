package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type mockGitCodeAPIAuthMode string

type mockGitCodeAPIFailureMode string

const (
	mockGitCodeAPIAuthAccept    mockGitCodeAPIAuthMode = "accept"
	mockGitCodeAPIAuthReject401 mockGitCodeAPIAuthMode = "reject401"
	mockGitCodeAPIAuthReject403 mockGitCodeAPIAuthMode = "reject403"
)

const (
	mockGitCodeAPIFailureNone           mockGitCodeAPIFailureMode = ""
	mockGitCodeAPIFailure400            mockGitCodeAPIFailureMode = "400"
	mockGitCodeAPIFailure404            mockGitCodeAPIFailureMode = "404"
	mockGitCodeAPIFailure409            mockGitCodeAPIFailureMode = "409"
	mockGitCodeAPIFailure413            mockGitCodeAPIFailureMode = "413"
	mockGitCodeAPIFailure429            mockGitCodeAPIFailureMode = "429"
	mockGitCodeAPIFailureMalformedJSON  mockGitCodeAPIFailureMode = "malformed_json"
	mockGitCodeAPIFailureSchemaMismatch mockGitCodeAPIFailureMode = "schema_mismatch"
	mockGitCodeAPIFailurePartial        mockGitCodeAPIFailureMode = "partial_response"
	mockGitCodeAPIFailureTimeout        mockGitCodeAPIFailureMode = "timeout"
	mockGitCodeAPIFailure500            mockGitCodeAPIFailureMode = "500"
)

type MockGitCodeAPI struct {
	server        *httptest.Server
	expectedToken string
	authMode      mockGitCodeAPIAuthMode
	failureMode   mockGitCodeAPIFailureMode
	owner         string
	repo          string
	mu            sync.Mutex
	counts        MockGitCodeAPICounts
	requests      []MockGitCodeAPIRequest
}

type MockGitCodeAPICounts struct {
	ListIssues         int
	ListWikiPages      int
	ListComments       int
	CreateIssue        int
	AuthFailures       int
	UnexpectedRequests int
	TotalRequests      int
}

type MockGitCodeAPIRequest struct {
	Method          string
	Path            string
	Operation       string
	Status          int
	AuthorizationOK bool
	IdempotencyKey  string
}

type MockGitCodeAPIPair struct {
	Selected    *MockGitCodeAPI
	NonSelected *MockGitCodeAPI
}

func NewMockGitCodeAPI(t *testing.T, opts ...func(*MockGitCodeAPI)) *MockGitCodeAPI {
	t.Helper()
	api := &MockGitCodeAPI{expectedToken: "test-token", authMode: mockGitCodeAPIAuthAccept, owner: "owner-a", repo: "repo-a"}
	for _, opt := range opts {
		opt(api)
	}
	api.server = httptest.NewServer(http.HandlerFunc(api.serveHTTP))
	return api
}

func NewMockGitCodeAPIPair(t *testing.T) MockGitCodeAPIPair {
	t.Helper()
	return MockGitCodeAPIPair{Selected: NewMockGitCodeAPI(t), NonSelected: NewMockGitCodeAPI(t)}
}

func MockGitCodeAPIAuthMode(mode mockGitCodeAPIAuthMode) func(*MockGitCodeAPI) {
	return func(api *MockGitCodeAPI) { api.authMode = mode }
}

func MockGitCodeAPIFailureMode(mode mockGitCodeAPIFailureMode) func(*MockGitCodeAPI) {
	return func(api *MockGitCodeAPI) { api.failureMode = mode }
}

func (api *MockGitCodeAPI) BaseURL() string { return api.server.URL }

func (api *MockGitCodeAPI) Close() { api.server.Close() }

func (api *MockGitCodeAPI) Counts() MockGitCodeAPICounts {
	api.mu.Lock()
	defer api.mu.Unlock()
	return api.counts
}

func (api *MockGitCodeAPI) Requests() []MockGitCodeAPIRequest {
	api.mu.Lock()
	defer api.mu.Unlock()
	out := make([]MockGitCodeAPIRequest, len(api.requests))
	copy(out, api.requests)
	return out
}

func (api *MockGitCodeAPI) CapturedCreateRequests() []MockGitCodeAPIRequest {
	api.mu.Lock()
	defer api.mu.Unlock()
	var out []MockGitCodeAPIRequest
	for _, req := range api.requests {
		if req.Operation == "create_issue" {
			out = append(out, req)
		}
	}
	return out
}

func (api *MockGitCodeAPI) serveHTTP(w http.ResponseWriter, r *http.Request) {
	operation := api.operation(r)
	authOK := r.Header.Get("Authorization") == "Bearer "+api.expectedToken
	status := http.StatusOK
	if api.authMode == mockGitCodeAPIAuthReject401 {
		status = http.StatusUnauthorized
	} else if api.authMode == mockGitCodeAPIAuthReject403 {
		status = http.StatusForbidden
	} else if !authOK {
		status = http.StatusUnauthorized
	}
	api.recordRequest(r, operation, authOK, status)
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		http.Error(w, "live auth failure", status)
		return
	}
	if api.writeFailure(w, operation) {
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch operation {
	case "list_issues":
		fmt.Fprint(w, `[{"id":"MOCK-ISSUE-100","number":100,"title":"Mock Live Issue","state":"open","body":"mock live issue body","created_at":"2026-06-22T00:00:00Z","updated_at":"2026-06-22T00:00:00Z"}]`)
	case "get_issue":
		fmt.Fprint(w, `{"id":"MOCK-ISSUE-100","number":100,"title":"Mock Live Issue","state":"open","body":"mock live issue body","created_at":"2026-06-22T00:00:00Z","updated_at":"2026-06-22T00:00:00Z"}`)
	case "list_comments":
		fmt.Fprint(w, `[{"id":"MOCK-COMMENT-1","author":"mock-user","body":"mock live comment","created_at":"2026-06-22T00:00:00Z","updated_at":"2026-06-22T00:00:00Z"}]`)
	case "list_wiki":
		fmt.Fprint(w, `[{"path":"LiveGuide.md","type":"file","sha":"rev-live-1"}]`)
	case "get_wiki":
		fmt.Fprint(w, `{"path":"LiveGuide.md","type":"file","sha":"rev-live-1"}`)
	case "raw_wiki":
		w.Header().Set("Content-Type", "text/markdown")
		fmt.Fprint(w, `mock live wiki body`)
	case "create_issue":
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":"MOCK-CREATED-ISSUE","number":101,"title":"Mock Created","state":"open","body":"created by mock keychain","created_at":"2026-06-22T00:00:00Z","updated_at":"2026-06-22T00:00:00Z"}`)
	default:
		http.NotFound(w, r)
	}
}

func (api *MockGitCodeAPI) writeFailure(w http.ResponseWriter, operation string) bool {
	if api.failureMode == mockGitCodeAPIFailureNone || operation != "list_issues" {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	switch api.failureMode {
	case mockGitCodeAPIFailure400:
		http.Error(w, "bad request", http.StatusBadRequest)
	case mockGitCodeAPIFailure404:
		http.Error(w, "not found", http.StatusNotFound)
	case mockGitCodeAPIFailure409:
		http.Error(w, "conflict", http.StatusConflict)
	case mockGitCodeAPIFailure413:
		http.Error(w, "too large", http.StatusRequestEntityTooLarge)
	case mockGitCodeAPIFailure429:
		w.Header().Set("Retry-After", "1")
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	case mockGitCodeAPIFailureMalformedJSON:
		fmt.Fprint(w, `[{"id":"MOCK-ISSUE-100","number":`)
	case mockGitCodeAPIFailureSchemaMismatch:
		fmt.Fprint(w, `[{"id":"MOCK-ISSUE-100","title":"missing number"}]`)
	case mockGitCodeAPIFailurePartial:
		w.Header().Set("Content-Length", "100")
		fmt.Fprint(w, `[{"id":"MOCK-ISSUE-100"`)
	case mockGitCodeAPIFailureTimeout:
		time.Sleep(100 * time.Millisecond)
		fmt.Fprint(w, `[]`)
	case mockGitCodeAPIFailure500:
		http.Error(w, "server error", http.StatusInternalServerError)
	default:
		return false
	}
	return true
}

func (api *MockGitCodeAPI) operation(r *http.Request) string {
	base := "/api/v5/repos/" + api.owner + "/" + api.repo
	path := r.URL.Path
	if path == base+"/issues" && r.Method == http.MethodGet {
		return "list_issues"
	}
	if path == base+"/issues" && r.Method == http.MethodPost {
		return "create_issue"
	}
	if path == base+"/issues/100" && r.Method == http.MethodGet {
		return "get_issue"
	}
	if path == base+"/issues/100/comments" && r.Method == http.MethodGet {
		return "list_comments"
	}
	wikiBase := "/api/v5/repos/" + api.owner + "/" + api.repo + ".wiki"
	if path == wikiBase+"/contents" && r.Method == http.MethodGet {
		return "list_wiki"
	}
	if path == wikiBase+"/contents/LiveGuide.md" && r.Method == http.MethodGet {
		return "get_wiki"
	}
	if path == wikiBase+"/raw/LiveGuide.md" && r.Method == http.MethodGet {
		return "raw_wiki"
	}
	if strings.HasPrefix(path, base+"/issues/") && strings.HasSuffix(path, "/comments") && r.Method == http.MethodGet {
		return "list_comments"
	}
	return "unexpected"
}

func (api *MockGitCodeAPI) recordRequest(r *http.Request, operation string, authOK bool, status int) {
	api.mu.Lock()
	defer api.mu.Unlock()
	api.counts.TotalRequests++
	switch operation {
	case "list_issues":
		api.counts.ListIssues++
	case "list_wiki":
		api.counts.ListWikiPages++
	case "list_comments":
		api.counts.ListComments++
	case "create_issue":
		api.counts.CreateIssue++
	case "unexpected":
		api.counts.UnexpectedRequests++
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		api.counts.AuthFailures++
	}
	api.requests = append(api.requests, MockGitCodeAPIRequest{Method: r.Method, Path: r.URL.Path, Operation: operation, Status: status, AuthorizationOK: authOK, IdempotencyKey: r.Header.Get("Idempotency-Key")})
}
