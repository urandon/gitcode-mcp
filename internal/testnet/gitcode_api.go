package testnet

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type GitCodeAPIAuthMode string

type GitCodeAPIFailureMode string

const (
	GitCodeAPIAuthAccept    GitCodeAPIAuthMode = "accept"
	GitCodeAPIAuthReject401 GitCodeAPIAuthMode = "reject401"
	GitCodeAPIAuthReject403 GitCodeAPIAuthMode = "reject403"
)

const (
	GitCodeAPIFailureNone           GitCodeAPIFailureMode = ""
	GitCodeAPIFailure400            GitCodeAPIFailureMode = "400"
	GitCodeAPIFailure404            GitCodeAPIFailureMode = "404"
	GitCodeAPIFailure409            GitCodeAPIFailureMode = "409"
	GitCodeAPIFailure413            GitCodeAPIFailureMode = "413"
	GitCodeAPIFailure429            GitCodeAPIFailureMode = "429"
	GitCodeAPIFailureMalformedJSON  GitCodeAPIFailureMode = "malformed_json"
	GitCodeAPIFailureSchemaMismatch GitCodeAPIFailureMode = "schema_mismatch"
	GitCodeAPIFailurePartial        GitCodeAPIFailureMode = "partial_response"
	GitCodeAPIFailureTimeout        GitCodeAPIFailureMode = "timeout"
	GitCodeAPIFailure500            GitCodeAPIFailureMode = "500"
)

type GitCodeAPIServer struct {
	server        *httptest.Server
	expectedToken string
	authMode      GitCodeAPIAuthMode
	failureMode   GitCodeAPIFailureMode
	owner         string
	repo          string
	mu            sync.Mutex
	counts        GitCodeAPICounts
	requests      []GitCodeAPIRequest
}

type GitCodeAPICounts struct {
	ListIssues         int
	ListWikiPages      int
	ListComments       int
	CreateIssue        int
	AuthFailures       int
	UnexpectedRequests int
	TotalRequests      int
}

type GitCodeAPIRequest struct {
	Method          string
	Path            string
	Operation       string
	Status          int
	AuthorizationOK bool
	IdempotencyKey  string
}

type GitCodeAPIPair struct {
	Selected    *GitCodeAPIServer
	NonSelected *GitCodeAPIServer
}

func NewGitCodeAPIServer(t *testing.T, opts ...func(*GitCodeAPIServer)) *GitCodeAPIServer {
	t.Helper()
	api := &GitCodeAPIServer{expectedToken: "test-token", authMode: GitCodeAPIAuthAccept, owner: "owner-a", repo: "repo-a"}
	for _, opt := range opts {
		opt(api)
	}
	api.server = httptest.NewServer(http.HandlerFunc(api.serveHTTP))
	return api
}

func NewGitCodeAPIPair(t *testing.T) GitCodeAPIPair {
	t.Helper()
	return GitCodeAPIPair{Selected: NewGitCodeAPIServer(t), NonSelected: NewGitCodeAPIServer(t)}
}

func WithGitCodeAPIAuthMode(mode GitCodeAPIAuthMode) func(*GitCodeAPIServer) {
	return func(api *GitCodeAPIServer) { api.authMode = mode }
}

func WithGitCodeAPIFailureMode(mode GitCodeAPIFailureMode) func(*GitCodeAPIServer) {
	return func(api *GitCodeAPIServer) { api.failureMode = mode }
}

func (api *GitCodeAPIServer) BaseURL() string { return api.server.URL }

func (api *GitCodeAPIServer) Close() { api.server.Close() }

func (api *GitCodeAPIServer) Counts() GitCodeAPICounts {
	api.mu.Lock()
	defer api.mu.Unlock()
	return api.counts
}

func (api *GitCodeAPIServer) Requests() []GitCodeAPIRequest {
	api.mu.Lock()
	defer api.mu.Unlock()
	out := make([]GitCodeAPIRequest, len(api.requests))
	copy(out, api.requests)
	return out
}

func (api *GitCodeAPIServer) CapturedCreateRequests() []GitCodeAPIRequest {
	api.mu.Lock()
	defer api.mu.Unlock()
	var out []GitCodeAPIRequest
	for _, req := range api.requests {
		if req.Operation == "create_issue" {
			out = append(out, req)
		}
	}
	return out
}

func (api *GitCodeAPIServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	operation := api.operation(r)
	authOK := r.Header.Get("Authorization") == "Bearer "+api.expectedToken
	status := http.StatusOK
	if api.authMode == GitCodeAPIAuthReject401 {
		status = http.StatusUnauthorized
	} else if api.authMode == GitCodeAPIAuthReject403 {
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

func (api *GitCodeAPIServer) writeFailure(w http.ResponseWriter, operation string) bool {
	if api.failureMode == GitCodeAPIFailureNone || operation != "list_issues" {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	switch api.failureMode {
	case GitCodeAPIFailure400:
		http.Error(w, "bad request", http.StatusBadRequest)
	case GitCodeAPIFailure404:
		http.Error(w, "not found", http.StatusNotFound)
	case GitCodeAPIFailure409:
		http.Error(w, "conflict", http.StatusConflict)
	case GitCodeAPIFailure413:
		http.Error(w, "too large", http.StatusRequestEntityTooLarge)
	case GitCodeAPIFailure429:
		w.Header().Set("Retry-After", "1")
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	case GitCodeAPIFailureMalformedJSON:
		fmt.Fprint(w, `[{"id":"MOCK-ISSUE-100","number":`)
	case GitCodeAPIFailureSchemaMismatch:
		fmt.Fprint(w, `[{"id":"MOCK-ISSUE-100","title":"missing number"}]`)
	case GitCodeAPIFailurePartial:
		w.Header().Set("Content-Length", "100")
		fmt.Fprint(w, `[{"id":"MOCK-ISSUE-100"`)
	case GitCodeAPIFailureTimeout:
		time.Sleep(100 * time.Millisecond)
		fmt.Fprint(w, `[]`)
	case GitCodeAPIFailure500:
		http.Error(w, "server error", http.StatusInternalServerError)
	default:
		return false
	}
	return true
}

func (api *GitCodeAPIServer) operation(r *http.Request) string {
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

func (api *GitCodeAPIServer) recordRequest(r *http.Request, operation string, authOK bool, status int) {
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
	api.requests = append(api.requests, GitCodeAPIRequest{Method: r.Method, Path: r.URL.Path, Operation: operation, Status: status, AuthorizationOK: authOK, IdempotencyKey: r.Header.Get("Idempotency-Key")})
}
