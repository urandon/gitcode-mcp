package testnet

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// CreationAPI models the observed sparse PR POST and canonical detail GET.
// Only the selected primary creation and its exact readback are permitted.
type CreationAPI struct {
	*httptest.Server
	Posts atomic.Int32
	Reads atomic.Int32
}

func NewCreationAPI(t testing.TB, kind, title, body string) *CreationAPI {
	t.Helper()
	api := &CreationAPI{}
	collection := "/api/v5/repos/owner/repo/issues"
	if kind == "pull_request" {
		collection = "/api/v5/repos/owner/repo/pulls"
	}
	canonical := map[string]any{"id": 9001, "number": 7, "title": title, "body": body, "state": "open", "head": map[string]string{"ref": "topic"}, "base": map[string]string{"ref": "main"}}
	api.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == collection:
			api.Posts.Add(1)
			var sent map[string]any
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil || sent["title"] != title || sent["body"] != body {
				t.Error("creation request did not preserve the expected fields")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			if kind == "pull_request" {
				_ = json.NewEncoder(w).Encode(map[string]any{"id": 9001, "number": 7, "title": title, "state": "opened"})
			} else {
				_ = json.NewEncoder(w).Encode(canonical)
			}
		case r.Method == http.MethodGet && r.URL.Path == collection+"/7":
			api.Reads.Add(1)
			_ = json.NewEncoder(w).Encode(canonical)
		default:
			t.Errorf("unexpected creation route %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(api.Close)
	return api
}
