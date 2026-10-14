package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"gitcode-mcp/internal/config"
)

func TestLocalProjectEmbeddingResponseBoundAndNoRedirect(t *testing.T) {
	large := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, strings.Repeat("x", (4<<20)+1)) }))
	defer large.Close()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, large.URL, nil)
	resp, err := (localProjectEmbeddingTransport{}).RoundTrip(req)
	if err != nil {
		t.Fatal("bounded response fixture unavailable")
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err == nil || len(data) > 4<<20 {
		t.Fatal("embedding response exceeded transfer cap")
	}
	if localProjectEmbeddingHTTP.Proxy != nil {
		t.Fatal("embedding transport uses ambient proxy routing")
	}
	var redirected atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1); w.WriteHeader(500) }))
	defer target.Close()
	from := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer from.Close()
	cfg := config.Config{RAG: config.RAGConfig{Profiles: map[string]config.RAGProfileConfig{"fixture": {Provider: "fixture", Model: "fixture-embedding", Dimensions: 8, BatchSize: 1}}, Providers: map[string]config.RAGProviderConfig{"fixture": {Type: "ollama", DataBoundary: "local_network", Endpoint: from.URL}}}}
	host := (&LocalProjects{}).WithSearchConfig(cfg)
	provider, err := host.localProjectProvider("fixture")
	if err != nil {
		t.Fatal("explicit fixture profile unavailable")
	}
	if _, err := provider.ModelInfo(context.Background()); err == nil || redirected.Load() != 0 {
		t.Fatal("embedding request followed redirect or invented readiness")
	}
	for _, selection := range []string{"", ":memory:", "file:fixture?mode=memory"} {
		if localProjectIndexPath(config.Config{CachePath: selection}) != "" {
			t.Fatal("unsupported cache selection became a filesystem destination")
		}
	}
}
