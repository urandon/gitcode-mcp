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

func TestG5ReviewInvalidQueryVectorCannotClaimHybrid(t *testing.T) {
	for _, bad := range []float64{0, 1e100} {
		t.Run(map[bool]string{true: "overflow", false: "zero"}[bad != 0], func(t *testing.T) {
			host, cfg, _ := localSearchFixture(t)
			want := searchFixtureMutation(t, host, localgit.Mutation{Operation: "create_issue", Title: "Question", Body: "needle current evidence", IdempotencyKey: "source"})
			var queryPhase atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/tags" {
					json.NewEncoder(w).Encode(map[string]any{"models": []any{map[string]any{"name": "fixture-embedding", "digest": "stable"}}})
					return
				}
				if r.URL.Path != "/api/embed" {
					t.Error("unexpected fixture operation")
					w.WriteHeader(500)
					return
				}
				var q struct {
					Input []string `json:"input"`
				}
				if json.NewDecoder(r.Body).Decode(&q) != nil {
					t.Error("invalid fixture request")
					w.WriteHeader(400)
					return
				}
				value := float64(1)
				if queryPhase.Load() {
					value = bad
				}
				vectors := make([][]float64, len(q.Input))
				for i := range vectors {
					vectors[i] = []float64{value, 0, 0, 0, 0, 0, 0, 0}
				}
				json.NewEncoder(w).Encode(map[string]any{"model": "fixture-embedding", "embeddings": vectors})
			}))
			defer server.Close()
			cfg.RAG.Providers["fixture"] = config.RAGProviderConfig{Type: "ollama", DataBoundary: "local_network", Endpoint: server.URL}
			host.WithSearchConfig(cfg)
			ready, err := host.Index(context.Background(), LocalProjectIndexRequest{Project: "lab", WriteMode: "local"})
			if err != nil || ready.State != "ready" {
				t.Fatal("stable valid document vector control failed")
			}
			queryPhase.Store(true)
			got, err := host.Search(context.Background(), LocalProjectSearchRequest{Project: "lab", Query: "needle", Mode: "hybrid"})
			if err != nil || got.EffectiveMode != "fulltext" || got.IndexState == "ready" || got.Coverage.State != "lexical-only" || len(got.Hits) != 1 || got.Hits[0].Citation != want.Citation {
				t.Fatal("invalid query vector was accepted as successful hybrid ranking")
			}
		})
	}
}
