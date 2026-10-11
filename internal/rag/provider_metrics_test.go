package rag

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/observability"
)

type embeddingMetricTransport func(*http.Request) (*http.Response, error)

func (f embeddingMetricTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func observedEmbeddingFixture(t *testing.T, transport embeddingMetricTransport) (*OllamaProvider, context.Context, *observability.Metrics) {
	t.Helper()
	profile := fakeIndexerProfile(2)
	profile.ProviderType, profile.Endpoint = "ollama", "https://example.invalid"
	provider, err := NewOllamaProvider(profile, ProviderOptions{HTTPClient: &http.Client{Transport: transport}})
	if err != nil {
		t.Fatal("provider fixture failed")
	}
	metrics := observability.NewMetrics(strings.Repeat("b", 32), time.Now().UTC())
	return provider, observability.WithMetrics(context.Background(), metrics), metrics
}

func embeddingMetricResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func requireEmbeddingAttempts(t *testing.T, metrics *observability.Metrics, outcome string, want uint64) {
	t.Helper()
	for _, series := range metrics.Snapshot().Series {
		if series.Labels["outcome"] != outcome {
			continue
		}
		if series.Name == "provider_outcomes" && (series.Value == nil || *series.Value != float64(want)) {
			t.Fatal("embedding HTTP attempt count differs")
		}
		if series.Name == "provider_duration" && series.Count != want {
			t.Fatal("embedding HTTP attempt duration count differs")
		}
	}
}

func TestEmbeddingMetricsCountNativeRetryAndLegacyRequests(t *testing.T) {
	for _, fallback := range []bool{false, true} {
		t.Run(map[bool]string{false: "retry", true: "legacy-fallback"}[fallback], func(t *testing.T) {
			calls := 0
			provider, ctx, metrics := observedEmbeddingFixture(t, func(req *http.Request) (*http.Response, error) {
				calls++
				switch req.URL.Path {
				case "/api/tags":
					if !fallback && calls == 1 {
						return embeddingMetricResponse(503, "synthetic-private-body"), nil
					}
					return embeddingMetricResponse(200, `{"models":[{"name":"fake-embedding","digest":"fixture-revision"}]}`), nil
				case "/api/embed":
					if fallback {
						return embeddingMetricResponse(404, "unsupported"), nil
					}
					return embeddingMetricResponse(200, `{"embeddings":[[1,0],[0,1]]}`), nil
				case "/api/embeddings":
					return embeddingMetricResponse(200, `{"embedding":[1,0]}`), nil
				default:
					t.Fatal("unexpected fixture operation")
					return nil, errors.New("invalid fixture")
				}
			})
			result, err := provider.Embed(ctx, EmbedRequest{Inputs: []string{"synthetic-private-input-a", "synthetic-private-input-b"}})
			if err != nil || len(result.Embeddings) != 2 {
				t.Fatal("observed provider changed embedding result")
			}
			wantSuccess := uint64(2)
			if fallback {
				wantSuccess = 3
			}
			if calls != int(wantSuccess)+1 {
				t.Fatal("retry/fallback attempt set differs")
			}
			requireEmbeddingAttempts(t, metrics, "succeeded", wantSuccess)
			requireEmbeddingAttempts(t, metrics, "failed", 1)
			bytes, err := json.Marshal(metrics.Snapshot())
			if err != nil || strings.Contains(string(bytes), "synthetic-private") || strings.Contains(string(bytes), "fake-embedding") || strings.Contains(string(bytes), "example.invalid") {
				t.Fatal("provider details entered metrics")
			}
		})
	}
}

type embeddingReadFailure struct{}

func (embeddingReadFailure) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (embeddingReadFailure) Close() error             { return nil }

type embeddingCloseCheck struct {
	io.ReadCloser
	t       *testing.T
	metrics *observability.Metrics
	closed  bool
}

func (b *embeddingCloseCheck) Close() error {
	requireEmbeddingAttempts(b.t, b.metrics, "succeeded", 0)
	b.closed = true
	return b.ReadCloser.Close()
}

func TestEmbeddingMetricsRecordAfterResponseReleaseAndFinishOnlyOnce(t *testing.T) {
	var body *embeddingCloseCheck
	provider, ctx, metrics := observedEmbeddingFixture(t, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
	})
	body = &embeddingCloseCheck{ReadCloser: io.NopCloser(strings.NewReader(`{}`)), t: t, metrics: metrics}
	if err := provider.doJSONOnce(ctx, http.MethodGet, "/api/tags", nil, nil); err != nil || !body.closed {
		t.Fatal("response release changed")
	}
	requireEmbeddingAttempts(t, metrics, "succeeded", 1)
	attempt := startEmbeddingMetricAttempt(ctx)
	completed := attempt.started.Add(time.Second)
	attempt.finish(200, nil, completed)
	attempt.finish(503, nil, completed)
	requireEmbeddingAttempts(t, metrics, "succeeded", 2)
	requireEmbeddingAttempts(t, metrics, "failed", 0)
}

func TestEmbeddingMetricsFixedTransportOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, outcome    string
		status           int
		err              error
		badRead, badJSON bool
	}{
		{name: "ok", outcome: "succeeded", status: 200},
		{name: "json_invalid_after_transfer", outcome: "succeeded", status: 200, badJSON: true},
		{name: "auth", outcome: "forbidden", status: 401},
		{name: "forbidden", outcome: "forbidden", status: 403},
		{name: "rate_limit", outcome: "rate_limited", status: 429},
		{name: "invalid_request", outcome: "invalid", status: 422},
		{name: "server", outcome: "failed", status: 503},
		{name: "transport", outcome: "failed", err: errors.New("synthetic-private-transport")},
		{name: "timeout", outcome: "timeout", err: context.DeadlineExceeded},
		{name: "cancelled_in_transport", outcome: "cancelled", err: context.Canceled},
		{name: "truncated", outcome: "invalid", status: 200, badRead: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			provider, ctx, metrics := observedEmbeddingFixture(t, func(*http.Request) (*http.Response, error) {
				calls++
				if tc.err != nil {
					return nil, tc.err
				}
				response := embeddingMetricResponse(tc.status, `{}`)
				if tc.badRead {
					response.Body = embeddingReadFailure{}
				}
				if tc.badJSON {
					response.Body = io.NopCloser(strings.NewReader(`{`))
				}
				return response, nil
			})
			var target any
			err := provider.doJSONOnce(ctx, http.MethodGet, "/synthetic-private-route", nil, &target)
			if calls != 1 || (err != nil) != (tc.status != 200 || tc.badRead || tc.badJSON || tc.err != nil) {
				t.Fatal("transport result changed")
			}
			requireEmbeddingAttempts(t, metrics, tc.outcome, 1)
		})
	}
}

func TestEmbeddingMetricsDoNotInventPreTransportAttempts(t *testing.T) {
	for _, mode := range []string{"cancelled", "encode", "empty_inputs", "fake", "foreground"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			provider, ctx, metrics := observedEmbeddingFixture(t, func(*http.Request) (*http.Response, error) {
				calls++
				return embeddingMetricResponse(200, `{}`), nil
			})
			switch mode {
			case "cancelled":
				cancelCtx, cancel := context.WithCancel(ctx)
				cancel()
				_ = provider.doJSONOnce(cancelCtx, http.MethodGet, "/api/tags", nil, nil)
			case "encode":
				_ = provider.doJSONOnce(ctx, http.MethodPost, "/api/embed", make(chan int), nil)
			case "empty_inputs":
				_, _ = provider.Embed(ctx, EmbedRequest{})
			case "fake":
				_, _ = mustFakeIndexerProvider(t, 2).Embed(ctx, EmbedRequest{Inputs: []string{"fixture"}})
			case "foreground":
				_ = provider.doJSONOnce(context.Background(), http.MethodGet, "/api/tags", nil, nil)
			}
			wantCalls := 0
			if mode == "foreground" {
				wantCalls = 1
			}
			if calls != wantCalls {
				t.Fatal("pre-transport work made a provider request")
			}
			for _, series := range metrics.Snapshot().Series {
				if series.Name == "provider_outcomes" && series.Value != nil {
					t.Fatal("unobserved/pre-transport work attached provider origin")
				}
			}
		})
	}
}
