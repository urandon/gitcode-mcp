package service

import (
	"context"
	"encoding/json"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/localgit"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestG5ReviewModelRevisionDriftCannotUseOldNamespace(t *testing.T) {
	for _, phase := range []string{"query", "index"} {
		t.Run(phase, func(t *testing.T) {
			host, cfg, _ := localSearchFixture(t)
			searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_issue", Title: "Question", Body: "needle authoritative evidence", IdempotencyKey: "issue"})
			var drifting atomic.Bool
			var tags atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/tags":
					ordinal := tags.Add(1)
					rev := "old-revision"
					if drifting.Load() && ordinal >= 2 {
						rev = "new-revision"
					}
					json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]any{"name": "fixture-embedding", "digest": rev}}})
				case "/api/embed":
					var req struct {
						Input []string `json:"input"`
					}
					if json.NewDecoder(r.Body).Decode(&req) != nil {
						t.Error("invalid fixture request")
						w.WriteHeader(400)
						return
					}
					values := make([][]float64, len(req.Input))
					for i := range values {
						values[i] = []float64{1, 0, 0, 0, 0, 0, 0, 0}
					}
					json.NewEncoder(w).Encode(map[string]any{"model": "fixture-embedding", "embeddings": values})
				default:
					t.Error("unexpected fixture operation")
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			cfg.RAG.Providers["fixture"] = config.RAGProviderConfig{Type: "ollama", DataBoundary: "local_network", Endpoint: server.URL}
			host.WithSearchConfig(cfg)
			ctx := context.Background()
			if phase == "query" {
				made, err := host.Index(ctx, LocalProjectIndexRequest{Project: "lab", WriteMode: "local"})
				if err != nil || made.State != "ready" {
					t.Fatal("stable model ready control unavailable")
				}
			}
			tags.Store(0)
			drifting.Store(true)
			if phase == "query" {
				got, err := host.Search(ctx, LocalProjectSearchRequest{Project: "lab", Query: "needle", Mode: "hybrid"})
				if tags.Load() < 2 {
					t.Fatal("query did not exercise revision drift")
				}
				if err != nil || got.EffectiveMode != "fulltext" || got.IndexState == "ready" || len(got.Hits) != 1 {
					t.Fatal("query vector from changed model used the old ready namespace")
				}
			} else {
				made, err := host.Index(ctx, LocalProjectIndexRequest{Project: "lab", WriteMode: "local"})
				if tags.Load() < 2 {
					t.Fatal("index did not exercise revision drift")
				}
				if err == nil && made.State == "ready" {
					t.Fatal("changed-model vectors published under old namespace")
				}
			}
		})
	}
}
