package testnet

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// ExactIssueAPI permits only one issue detail route and its comments. An
// accidental collection traversal or external write fails the owning test.
type ExactIssueAPI struct {
	*httptest.Server
	Details  atomic.Int32
	Comments atomic.Int32
}

func NewExactIssueAPI(t testing.TB, providerID, number int) *ExactIssueAPI {
	t.Helper()
	api := &ExactIssueAPI{}
	path := fmt.Sprintf("/api/v5/repos/owner/repo/issues/%d", number)
	api.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet {
			t.Errorf("unexpected mutation %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		switch r.URL.Path {
		case path:
			api.Details.Add(1)
			fmt.Fprintf(w, `{"id":%d,"number":%d,"title":"Provider issue","body":"References ISSUE-42 and WIKI-HOME are ordinary text.","state":"open","comments":1,"updated_at":"2026-10-08T12:00:00Z"}`, providerID, number)
		case path + "/comments":
			api.Comments.Add(1)
			fmt.Fprintf(w, `[{"id":420043,"issue_id":%d,"body":"Provider comment","updated_at":"2026-10-08T12:00:00Z"}]`, providerID)
		default:
			t.Errorf("unexpected collection/route %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(api.Close)
	return api
}
