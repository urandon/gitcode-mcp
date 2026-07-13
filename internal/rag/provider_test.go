package rag

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestOllamaProviderUsesNativeBatchAndPreservesOrdering(t *testing.T) {
	var embedCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []ollamaModel{{Name: "qwen3-embedding:0.6b", Digest: "sha256:test"}}})
		case "/api/embed":
			atomic.AddInt32(&embedCalls, 1)
			var body struct {
				Model string   `json:"model"`
				Input []string `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode native batch: %v", err)
			}
			if body.Model != "qwen3-embedding:0.6b" || len(body.Input) == 0 || len(body.Input) > 2 {
				t.Fatalf("native batch body=%#v", body)
			}
			embeddings := make([][]float64, len(body.Input))
			for i, input := range body.Input {
				var value float64
				if _, err := fmt.Sscanf(input, "input-%f", &value); err != nil {
					t.Fatalf("parse input %q: %v", input, err)
				}
				embeddings[i] = []float64{value, 0, 0}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": embeddings})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := newTestOllamaProvider(t, server.URL, 3)
	inputs := []string{"input-1", "input-2", "input-3", "input-4", "input-5"}
	result, err := provider.Embed(context.Background(), EmbedRequest{Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&embedCalls) != 3 || len(result.Embeddings) != len(inputs) {
		t.Fatalf("calls=%d result=%#v", embedCalls, result)
	}
	for i, embedding := range result.Embeddings {
		if embedding[0] != float32(i+1) {
			t.Fatalf("embedding[%d]=%v, want ordered value %d", i, embedding, i+1)
		}
	}
}

func TestOllamaProviderRejectsMalformedNativeBatch(t *testing.T) {
	tests := []struct {
		name       string
		response   string
		wantClass  string
		inputCount int
	}{
		{name: "partial cardinality", response: `{"embeddings":[[1,0,0]]}`, wantClass: ProviderFailureUnsupportedResponse, inputCount: 2},
		{name: "malformed json", response: `{"embeddings":`, wantClass: ProviderFailureUnsupportedResponse, inputCount: 2},
		{name: "dimension mismatch", response: `{"embeddings":[[1,0],[2,0,0]]}`, wantClass: ProviderFailureDimensionMismatch, inputCount: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/tags" {
					_ = json.NewEncoder(w).Encode(map[string]any{"models": []ollamaModel{{Name: "qwen3-embedding:0.6b", Digest: "sha256:test"}}})
					return
				}
				if r.URL.Path != "/api/embed" {
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write([]byte(tt.response))
			}))
			defer server.Close()
			provider := newTestOllamaProvider(t, server.URL, 3)
			inputs := make([]string, tt.inputCount)
			for i := range inputs {
				inputs[i] = fmt.Sprintf("input-%d", i)
			}
			_, err := provider.Embed(context.Background(), EmbedRequest{Inputs: inputs})
			assertProviderClass(t, err, tt.wantClass)
		})
	}
}

func TestOllamaProviderRetriesNativeBatchWithoutLegacyFallback(t *testing.T) {
	var nativeCalls int32
	var legacyCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []ollamaModel{{Name: "qwen3-embedding:0.6b", Digest: "sha256:test"}}})
		case "/api/embed":
			if atomic.AddInt32(&nativeCalls, 1) == 1 {
				http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float64{{1, 0, 0}, {2, 0, 0}}})
		case "/api/embeddings":
			atomic.AddInt32(&legacyCalls, 1)
			http.Error(w, "legacy should not be called", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	provider := newTestOllamaProvider(t, server.URL, 3)
	result, err := provider.Embed(context.Background(), EmbedRequest{Inputs: []string{"first", "second"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Embeddings) != 2 || nativeCalls != 2 || legacyCalls != 0 {
		t.Fatalf("result=%#v native=%d legacy=%d", result, nativeCalls, legacyCalls)
	}
}

func TestOllamaProviderFallsBackToLegacyOnceAndPreservesOrdering(t *testing.T) {
	var nativeCalls int32
	var legacyCalls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			_ = json.NewEncoder(w).Encode(map[string]any{"models": []ollamaModel{{Name: "qwen3-embedding:0.6b", Digest: "sha256:test"}}})
		case "/api/embed":
			atomic.AddInt32(&nativeCalls, 1)
			http.NotFound(w, r)
		case "/api/embeddings":
			atomic.AddInt32(&legacyCalls, 1)
			var body struct {
				Prompt string `json:"prompt"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode legacy request: %v", err)
			}
			value := float64(len(body.Prompt))
			_ = json.NewEncoder(w).Encode(map[string]any{"embedding": []float64{value, 0, 0}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	provider := newTestOllamaProvider(t, server.URL, 3)
	for _, inputs := range [][]string{{"a", "bbbb"}, {"cc", "dddddd"}} {
		result, err := provider.Embed(context.Background(), EmbedRequest{Inputs: inputs})
		if err != nil {
			t.Fatal(err)
		}
		if result.Embeddings[0][0] != float32(len(inputs[0])) || result.Embeddings[1][0] != float32(len(inputs[1])) {
			t.Fatalf("inputs=%v result=%#v", inputs, result)
		}
	}
	if nativeCalls != 1 || legacyCalls != 4 {
		t.Fatalf("native=%d legacy=%d, want 1 and 4", nativeCalls, legacyCalls)
	}
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
		case "/api/embed":
			if behavior.embedding == nil {
				behavior.embedding = []float64{0.1, 0.2, 0.3}
			}
			var body struct {
				Input []string `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode batch request: %v", err)
			}
			embeddings := make([][]float64, len(body.Input))
			for i := range embeddings {
				embeddings[i] = behavior.embedding
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": embeddings})
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
