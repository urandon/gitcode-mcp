package rag

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
)

func TestFakeProviderDeterministicMultilingualEmbeddings(t *testing.T) {
	provider, err := NewEmbeddingProviderFromConfig(fakeProviderConfig(), "", ProviderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	fixture := loadMultilingualEvalFixture(t)
	var inputs []string
	for _, tc := range fixture.Cases {
		inputs = append(inputs, tc.Query)
		for _, chunk := range tc.Chunks {
			inputs = append(inputs, chunk.Text)
		}
	}
	first, err := provider.Embed(context.Background(), EmbedRequest{Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	second, err := provider.Embed(context.Background(), EmbedRequest{Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if first.Dimensions != 8 || len(first.Embeddings) != len(inputs) {
		t.Fatalf("first=%#v", first)
	}
	for i := range first.Embeddings {
		for j := range first.Embeddings[i] {
			if first.Embeddings[i][j] != second.Embeddings[i][j] {
				t.Fatalf("embedding[%d][%d] not deterministic: %f != %f", i, j, first.Embeddings[i][j], second.Embeddings[i][j])
			}
		}
	}
}

func TestEnsureEmbeddingNamespaceFromProviderProfile(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "owner", Name: "repo", APIBaseURL: "https://example.invalid/api", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	provider, err := NewEmbeddingProviderFromConfig(fakeProviderConfig(), "", ProviderOptions{})
	if err != nil {
		t.Fatal(err)
	}
	req := NamespaceRequest{RepoID: "fixture-a", ChunkPolicyID: "tokens-512-overlap-64", LanguagePolicyID: "ru-zh-en-v1"}
	first, err := EnsureEmbeddingNamespace(ctx, store, provider, req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := EnsureEmbeddingNamespace(ctx, store, provider, req)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == "" || second.ID != first.ID || first.ModelRevision != "fake-deterministic-v1" {
		t.Fatalf("namespaces first=%#v second=%#v", first, second)
	}
}

func TestEmbeddingProviderConfigValidation(t *testing.T) {
	cfg := fakeProviderConfig()
	profile := cfg.RAG.Profiles["fake"]
	profile.Dimensions = 0
	cfg.RAG.Profiles["fake"] = profile
	if _, err := NewEmbeddingProviderFromConfig(cfg, "", ProviderOptions{}); err == nil || !strings.Contains(err.Error(), "dimensions") {
		t.Fatalf("err=%v, want dimensions validation", err)
	}
}

func TestOllamaProviderEmbedsAndClassifiesFailures(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		server := newOllamaTestServer(t, ollamaServerBehavior{embedding: []float64{0.1, 0.2, 0.3}})
		provider := newTestOllamaProvider(t, server.URL, 3)
		result, err := provider.Embed(context.Background(), EmbedRequest{Inputs: []string{"русский 中文 English"}})
		if err != nil {
			t.Fatal(err)
		}
		if result.Model != "qwen3-embedding:0.6b" || result.Revision != "sha256:test" || result.Dimensions != 3 || len(result.Embeddings) != 1 {
			t.Fatalf("result=%#v", result)
		}
	})

	t.Run("missing model", func(t *testing.T) {
		server := newOllamaTestServer(t, ollamaServerBehavior{models: []ollamaModel{{Name: "other", Digest: "sha256:other"}}})
		provider := newTestOllamaProvider(t, server.URL, 3)
		_, err := provider.Embed(context.Background(), EmbedRequest{Inputs: []string{"query"}})
		assertProviderClass(t, err, ProviderFailureModelMissing)
	})

	t.Run("dimension mismatch", func(t *testing.T) {
		server := newOllamaTestServer(t, ollamaServerBehavior{embedding: []float64{0.1, 0.2}})
		provider := newTestOllamaProvider(t, server.URL, 3)
		_, err := provider.Embed(context.Background(), EmbedRequest{Inputs: []string{"query"}})
		assertProviderClass(t, err, ProviderFailureDimensionMismatch)
	})

	t.Run("unsupported response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("{"))
		}))
		defer server.Close()
		provider := newTestOllamaProvider(t, server.URL, 3)
		_, err := provider.ModelInfo(context.Background())
		assertProviderClass(t, err, ProviderFailureUnsupportedResponse)
	})

	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(50 * time.Millisecond)
		}))
		defer server.Close()
		provider := newTestOllamaProvider(t, server.URL, 3)
		provider.httpClient.Timeout = time.Millisecond
		_, err := provider.ModelInfo(context.Background())
		assertProviderClass(t, err, ProviderFailureTimeout)
	})

	t.Run("retries transient unavailable", func(t *testing.T) {
		var tagsCalls int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/tags" {
				http.NotFound(w, r)
				return
			}
			if atomic.AddInt32(&tagsCalls, 1) == 1 {
				http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []ollamaModel{{Name: "qwen3-embedding:0.6b", Digest: "sha256:test"}}})
		}))
		defer server.Close()
		provider := newTestOllamaProvider(t, server.URL, 3)
		info, err := provider.ModelInfo(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if info.Revision != "sha256:test" || atomic.LoadInt32(&tagsCalls) != 2 {
			t.Fatalf("info=%#v tagsCalls=%d", info, atomic.LoadInt32(&tagsCalls))
		}
	})
}

func fakeProviderConfig() config.Config {
	cfg := config.Default()
	cfg.RAG.DefaultProfile = "fake"
	cfg.RAG.Providers["fake"] = config.RAGProviderConfig{Type: "fake", Timeout: time.Second}
	cfg.RAG.Profiles["fake"] = config.RAGProfileConfig{Provider: "fake", Model: "fake-embed", Dimensions: 8, BatchSize: 2, MaxInputTokens: 512}
	cfg.RAG.Indexing.Profile = "fake"
	cfg.RAG.Search.Profile = "fake"
	return cfg
}

type ollamaServerBehavior struct {
	models    []ollamaModel
	embedding []float64
}

func newOllamaTestServer(t *testing.T, behavior ollamaServerBehavior) *httptest.Server {
	t.Helper()
	if behavior.models == nil {
		behavior.models = []ollamaModel{{Name: "qwen3-embedding:0.6b", Digest: "sha256:test"}}
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": behavior.models})
		case "/api/embeddings":
			if behavior.embedding == nil {
				behavior.embedding = []float64{0.1, 0.2, 0.3}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"embedding": behavior.embedding})
		default:
			http.NotFound(w, r)
		}
	}))
}

func newTestOllamaProvider(t *testing.T, endpoint string, dimensions int) *OllamaProvider {
	t.Helper()
	provider, err := NewOllamaProvider(EmbeddingProviderProfile{
		ProfileID:    "default",
		ProviderID:   "ollama",
		ProviderType: "ollama",
		Endpoint:     endpoint,
		Model:        "qwen3-embedding:0.6b",
		Dimensions:   dimensions,
		BatchSize:    2,
		Timeout:      time.Second,
	}, ProviderOptions{MaxRetries: 0})
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func assertProviderClass(t *testing.T, err error, class string) {
	t.Helper()
	var providerErr *ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("err=%T %v, want ProviderError", err, err)
	}
	if providerErr.Class != class {
		t.Fatalf("class=%q, want %q; err=%v", providerErr.Class, class, err)
	}
	if strings.TrimSpace(providerErr.Error()) == "" {
		t.Fatalf("empty provider error")
	}
}
