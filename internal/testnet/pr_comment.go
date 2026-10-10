package testnet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
)

// NewPRCommentAPI exposes only one PR's comment POST/read, with public-safe data.
func NewPRCommentAPI(t testing.TB, body string) *CreationAPI {
	t.Helper()
	api := &CreationAPI{}
	api.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		comment := map[string]any{"note_id": 301, "body": body, "created_at": "2026-10-10T12:00:00Z", "updated_at": "2026-10-10T12:00:00Z"}
		if r.Method == http.MethodGet && r.URL.Path == "/api/v5/repos/owner/repo/pulls/7/comments" {
			api.Reads.Add(1)
			_ = json.NewEncoder(w).Encode([]any{comment})
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/api/v4/projects/owner/repo/merge_requests/7/discussions" {
			_ = json.NewEncoder(w).Encode([]any{})
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/v5/repos/owner/repo/pulls/7/comments" {
			t.Errorf("unexpected comment route %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		api.Posts.Add(1)
		var sent map[string]any
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil || sent["body"] != body {
			t.Error("comment body was not preserved")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(comment)
	}))
	t.Cleanup(api.Close)
	return api
}

func SeedPRCommentParent(t testing.TB, store cache.Store, repoID string) {
	t.Helper()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	if err := store.UpsertRecordGraph(context.Background(), cache.RecordGraph{SourceProvenance: cache.ProvenanceLive, Record: cache.Record{RepoID: repoID, ID: "PR-7", Type: "pull_request", Path: "pulls/7.md", Title: "parent", ContentHash: "parent", Provenance: cache.ProvenanceRemote, RemoteType: "pull_request", RemoteID: "7", CreatedAt: now, UpdatedAt: now}, Identities: []cache.Identity{{RepoID: repoID, SourceID: "PR-7", AliasType: "pull_request", Alias: "7", Remote: cache.RemoteAlias{Type: "pull_request", ID: "7"}}}}); err != nil {
		t.Fatal(err)
	}
}
