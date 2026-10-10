package gitcode

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

func observedProviderFixture(t *testing.T, cfg Config, transport roundTripFunc) (*HTTPClient, context.Context, *observability.Metrics) {
	t.Helper()
	cfg.BaseURL = "https://example.invalid"
	client, err := NewHTTPClient(cfg)
	if err != nil {
		t.Fatal("fixture client construction failed")
	}
	client.client.Transport = transport
	metrics := observability.NewMetrics(strings.Repeat("b", 32), time.Now().UTC())
	return client, observability.WithMetrics(context.Background(), metrics), metrics
}

func metricProviderSeries(t *testing.T, metrics *observability.Metrics, name, outcome string) observability.MetricSeries {
	t.Helper()
	for _, s := range metrics.Snapshot().Series {
		if s.Name == name && s.Labels["outcome"] == outcome {
			return s
		}
	}
	t.Fatal("provider fixed series missing")
	return observability.MetricSeries{}
}

func requireProviderAttempts(t *testing.T, metrics *observability.Metrics, outcome string, want float64) {
	t.Helper()
	s := metricProviderSeries(t, metrics, "provider_outcomes", outcome)
	if s.Value == nil || *s.Value != want {
		t.Fatal("provider attempt count differs")
	}
	h := metricProviderSeries(t, metrics, "provider_duration", outcome)
	if h.Count != uint64(want) {
		t.Fatal("provider duration counted a different attempt set")
	}
}

func providerFixtureResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), ContentLength: -1}
}

func TestProviderMetricsCountActualRetryInvocations(t *testing.T) {
	for _, noRetry := range []bool{false, true} {
		t.Run(map[bool]string{false: "read-retry", true: "guarded-write-single-attempt"}[noRetry], func(t *testing.T) {
			calls := 0
			client, ctx, metrics := observedProviderFixture(t, Config{MaxRetries: 2}, func(*http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return providerFixtureResponse(http.StatusServiceUnavailable, "synthetic-private-body"), nil
				}
				return providerFixtureResponse(http.StatusOK, "[]"), nil
			})
			_, _, err := client.bytesWithOptions(ctx, http.MethodGet, "/synthetic-private-route", nil, nil, requestOptions{noRetry: noRetry})
			if noRetry {
				if err == nil || calls != 1 {
					t.Fatal("observation changed guarded retry behavior")
				}
				requireProviderAttempts(t, metrics, "succeeded", 0)
			} else {
				if err != nil || calls != 2 {
					t.Fatal("observation changed read retry behavior")
				}
				requireProviderAttempts(t, metrics, "succeeded", 1)
			}
			requireProviderAttempts(t, metrics, "failed", 1)
		})
	}
}

func TestProviderMetricsPreTransportRejectionDoesNotCount(t *testing.T) {
	calls := 0
	client, ctx, metrics := observedProviderFixture(t, Config{}, func(*http.Request) (*http.Response, error) {
		calls++
		return providerFixtureResponse(http.StatusOK, "[]"), nil
	})
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := client.getBytes(cancelled, "/fixture", nil); err == nil {
		t.Fatal("fixture cancelled request accepted")
	}
	if _, _, err := client.getBytes(ctx, "https://example.invalid/disallowed", nil); err == nil {
		t.Fatal("fixture absolute endpoint accepted")
	}
	if calls != 0 || metricProviderSeries(t, metrics, "provider_outcomes", "succeeded").Value != nil || metrics.Snapshot().Dropped != 0 {
		t.Fatal("local rejection fabricated an attempted transport or observation loss")
	}
}

func TestProviderMetricsResponseClassesAndTransferFaults(t *testing.T) {
	for _, tc := range []struct {
		name, outcome string
		status        int
		transportErr  error
		shortBody     bool
	}{
		{name: "success", outcome: "succeeded", status: 200},
		{name: "forbidden", outcome: "forbidden", status: 403},
		{name: "unauthorized", outcome: "forbidden", status: 401},
		{name: "rate-limited", outcome: "rate_limited", status: 429},
		{name: "validation", outcome: "invalid", status: 422},
		{name: "not-found", outcome: "failed", status: 404},
		{name: "oversized-status", outcome: "invalid", status: 413},
		{name: "transport", outcome: "failed", transportErr: errors.New("synthetic-private-transport-error")},
		{name: "cancelled", outcome: "cancelled", transportErr: context.Canceled},
		{name: "timeout", outcome: "timeout", transportErr: context.DeadlineExceeded},
		{name: "truncated-transfer", outcome: "invalid", status: 200, shortBody: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, ctx, metrics := observedProviderFixture(t, Config{}, func(*http.Request) (*http.Response, error) {
				if tc.transportErr != nil {
					return nil, tc.transportErr
				}
				resp := providerFixtureResponse(tc.status, "[]")
				if tc.shortBody {
					resp.ContentLength = 100
				}
				return resp, nil
			})
			_, _, err := client.getBytes(ctx, "/fixture", nil)
			if tc.outcome == "succeeded" && err != nil || tc.outcome != "succeeded" && err == nil {
				t.Fatal("observation changed response outcome")
			}
			requireProviderAttempts(t, metrics, tc.outcome, 1)
			for _, other := range []string{"succeeded", "forbidden", "rate_limited", "failed", "invalid", "timeout", "cancelled"} {
				if other != tc.outcome {
					requireProviderAttempts(t, metrics, other, 0)
				}
			}
		})
	}
}

func TestProviderMetricsWikiDirectPathAndNoPrivatePayload(t *testing.T) {
	client, ctx, metrics := observedProviderFixture(t, Config{Token: "synthetic-private-token"}, func(*http.Request) (*http.Response, error) {
		return providerFixtureResponse(http.StatusOK, "[]"), nil
	})
	if _, err := client.listWikiEntries(ctx, "synthetic-private-owner", "synthetic-private-repository", "synthetic-private-directory"); err != nil {
		t.Fatal("fixture direct wiki transfer failed")
	}
	requireProviderAttempts(t, metrics, "succeeded", 1)
	data, err := json.Marshal(metrics.Snapshot())
	if err != nil || strings.Contains(string(data), "synthetic-private") || strings.Contains(string(data), "example.invalid") {
		t.Fatal("provider metrics leaked private request data")
	}
}

func TestProviderMetricsOmittedOutsideDaemonContext(t *testing.T) {
	client, _, metrics := observedProviderFixture(t, Config{}, func(*http.Request) (*http.Response, error) {
		return providerFixtureResponse(http.StatusOK, "[]"), nil
	})
	if _, _, err := client.getBytes(context.Background(), "/fixture", nil); err != nil {
		t.Fatal("fixture unobserved transfer failed")
	}
	if metricProviderSeries(t, metrics, "provider_outcomes", "succeeded").Value != nil {
		t.Fatal("foreground request acquired daemon telemetry implicitly")
	}
}

func TestProviderMetricsAttemptFinalizationIsOnce(t *testing.T) {
	metrics := observability.NewMetrics(strings.Repeat("d", 32), time.Now().UTC())
	attempt := newProviderMetricAttempt(observability.WithMetrics(context.Background(), metrics))
	attempt.finish(200, nil)
	if metricProviderSeries(t, metrics, "provider_outcomes", "succeeded").Value != nil {
		t.Fatal("unstarted attempt was counted")
	}
	attempt.start()
	attempt.finish(200, nil)
	attempt.finish(500, errors.New("synthetic-private-error"))
	requireProviderAttempts(t, metrics, "succeeded", 1)
	requireProviderAttempts(t, metrics, "failed", 0)
}
