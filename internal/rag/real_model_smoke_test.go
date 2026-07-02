package rag

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestOptionalOllamaRealModelSmoke(t *testing.T) {
	if os.Getenv("GITCODE_MCP_RAG_REAL_SMOKE") != "1" {
		t.Skip("set GITCODE_MCP_RAG_REAL_SMOKE=1 to run local Ollama real-model smoke")
	}
	fixture := loadMultilingualEvalFixture(t)
	endpoint := firstNonEmpty(os.Getenv("GITCODE_MCP_RAG_PROVIDER_ENDPOINT"), "http://127.0.0.1:11434")
	model := firstNonEmpty(os.Getenv("GITCODE_MCP_RAG_REAL_MODEL"), "qwen3-embedding:0.6b")
	dimensions := 1024
	if raw := os.Getenv("GITCODE_MCP_RAG_REAL_DIMENSIONS"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			t.Fatalf("invalid GITCODE_MCP_RAG_REAL_DIMENSIONS=%q", raw)
		}
		dimensions = parsed
	}
	provider, err := NewOllamaProvider(EmbeddingProviderProfile{
		ProfileID:      "real-smoke",
		ProviderID:     "ollama",
		ProviderType:   "ollama",
		Endpoint:       endpoint,
		Model:          model,
		Dimensions:     dimensions,
		BatchSize:      4,
		MaxInputTokens: 512,
		Timeout:        5 * time.Second,
	}, ProviderOptions{HTTPClient: &http.Client{Timeout: 5 * time.Second}, MaxRetries: 0})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := provider.ModelInfo(ctx); err != nil {
		skipIfProviderUnavailable(t, err)
		t.Fatalf("ModelInfo returned error: %v", err)
	}
	inputs := make([]string, 0, len(fixture.Cases))
	for _, tc := range fixture.Cases {
		inputs = append(inputs, tc.Query)
	}
	result, err := provider.Embed(ctx, EmbedRequest{Inputs: inputs})
	if err != nil {
		skipIfProviderUnavailable(t, err)
		t.Fatalf("Embed returned error: %v", err)
	}
	if result.Model != model || result.Dimensions != dimensions || len(result.Embeddings) != len(inputs) {
		t.Fatalf("result=%#v, want model=%q dimensions=%d embeddings=%d", result, model, dimensions, len(inputs))
	}
}

func skipIfProviderUnavailable(t *testing.T, err error) {
	t.Helper()
	var providerErr *ProviderError
	if errors.As(err, &providerErr) {
		switch providerErr.Class {
		case ProviderFailureUnavailable, ProviderFailureModelMissing, ProviderFailureTimeout:
			t.Skipf("local embedding provider is not ready: %v", err)
		}
	}
}
