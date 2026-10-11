package gitcode

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/diagnostics"
)

type attemptRoundTripper func(*http.Request) (*http.Response, error)

func (f attemptRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type legacyAttemptTransport struct{ cancelled *http.Request }

func (t *legacyAttemptTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, context.Canceled
}
func (t *legacyAttemptTransport) CancelRequest(req *http.Request) { t.cancelled = req }

func TestHTTPAttemptWrapperPreservesLegacyCancellation(t *testing.T) {
	inner := &legacyAttemptTransport{}
	req, _ := http.NewRequest(http.MethodGet, "https://provider.invalid/fixture", nil)
	wrapper := httpAttemptTransport{inner: inner}
	wrapper.CancelRequest(req)
	if inner.cancelled != req {
		t.Fatal("legacy client cancellation hook lost")
	}
}

func TestHTTPAttemptBoundaryBeforeAndInsideTransport(t *testing.T) {
	for _, tc := range []struct {
		name   string
		before bool
		cause  error
	}{
		{"cancelled_before", true, context.Canceled},
		{"deadline_before", true, context.DeadlineExceeded},
		{"cancelled_inside", false, context.Canceled},
		{"deadline_inside", false, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, err := NewHTTPClient(Config{BaseURL: "https://provider.invalid", MaxRetries: 0})
			if err != nil {
				t.Fatal("client setup failed")
			}
			var calls atomic.Int32
			client.client.Transport = attemptRoundTripper(func(*http.Request) (*http.Response, error) { calls.Add(1); return nil, tc.cause })
			ctx := context.Background()
			if tc.before {
				if tc.cause == context.Canceled {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				} else {
					var cancel context.CancelFunc
					ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
					defer cancel()
				}
			}
			ctx, witness := diagnostics.WithHTTPAttemptWitness(ctx)
			_, err = client.GetIssue(ctx, IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 1})
			var network ErrNetworkUnavailable
			if !errors.As(err, &network) || !errors.Is(err, tc.cause) {
				t.Fatal("network wrapper lost interruption cause")
			}
			want := 1
			if tc.before {
				want = 0
			}
			if int(calls.Load()) != want || network.Attempts != want || witness.Attempted() != (want != 0) {
				t.Fatal("HTTP count must match actual transport entry independently of telemetry")
			}
		})
	}
}

func TestHTTPAttemptBoundaryRequestConstructionAndRateWait(t *testing.T) {
	client, _ := NewHTTPClient(Config{BaseURL: "https://provider.invalid", RateLimitRPS: 0.001, RateLimitBurst: 1})
	var calls atomic.Int32
	client.client.Transport = attemptRoundTripper(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("unexpected transport")
	})
	ctx, witness := diagnostics.WithHTTPAttemptWitness(context.Background())
	observation := newProviderMetricAttempt(ctx)
	_, err := client.do(ctx, "invalid method", "/fixture", nil, nil, requestOptions{metricAttempt: observation})
	if err == nil || witness.Attempted() || observation.httpEntered.Load() || calls.Load() != 0 {
		t.Fatal("request construction is not an HTTP attempt")
	}
	// Consume the initial token locally; cancel deterministically from the
	// throttle callback before entering transport, without a timing sleep.
	client.rateLimiter.reserve(time.Now())
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx = WithRateLimitObserver(ctx, func(event RateLimitEvent) {
		if event.Type == RateLimitEventThrottleWaitStarted {
			cancel()
		}
	})
	_, err = client.GetIssue(ctx, IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 1})
	var network ErrNetworkUnavailable
	if !errors.As(err, &network) || network.Attempts != 0 || witness.Attempted() || calls.Load() != 0 {
		t.Fatal("rate wait is not an HTTP attempt")
	}
}

func TestHTTPAttemptWrapperPreservesClientRedirectPolicy(t *testing.T) {
	client, _ := NewHTTPClient(Config{BaseURL: "https://provider.invalid", Timeout: time.Second})
	ctx, witness := diagnostics.WithHTTPAttemptWitness(context.Background())
	var calls, redirects int
	transport := attemptRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls++
		if _, ok := req.Context().Deadline(); !ok {
			t.Error("configured client timeout lost")
		}
		if calls == 1 {
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"/redirect"}}, Body: io.NopCloser(strings.NewReader("")), Request: req}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: -1, Body: io.NopCloser(strings.NewReader(`{"id":1,"number":1}`)), Request: req}, nil
	})
	client.client.Transport = transport
	client.client.CheckRedirect = func(req *http.Request, via []*http.Request) error { redirects++; return nil }
	original := client.client
	_, _, err := client.getBytes(ctx, "/fixture", nil)
	if err != nil || calls != 2 || redirects != 1 || !witness.Attempted() || client.client != original {
		t.Fatalf("client behavior changed: error type=%T calls=%d redirects=%d attempted=%t", err, calls, redirects, witness.Attempted())
	}
}

func TestHTTPAttemptPriorSuccessThenLocalRateCancellation(t *testing.T) {
	client, _ := NewHTTPClient(Config{BaseURL: "https://provider.invalid", MaxRetries: 0, RateLimitRPS: 0.001, RateLimitBurst: 1})
	var calls atomic.Int32
	client.client.Transport = attemptRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: -1, Body: io.NopCloser(strings.NewReader(`{"id":1,"number":1}`)), Request: req}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, witness := diagnostics.WithHTTPAttemptWitness(ctx)
	if _, _, err := client.getBytes(ctx, "/fixture", nil); err != nil {
		t.Fatal("initial request failed")
	}
	ctx = WithRateLimitObserver(ctx, func(event RateLimitEvent) {
		if event.Type == RateLimitEventThrottleWaitStarted {
			cancel()
		}
	})
	_, err := client.GetIssue(ctx, IssueRequest{Owner: "example-owner", Repo: "example-repo", Number: 2})
	var network ErrNetworkUnavailable
	if !errors.As(err, &network) || network.Attempts != 0 || !errors.Is(err, context.Canceled) || calls.Load() != 1 || !witness.Attempted() {
		t.Fatal("later request must stop before transport without erasing prior HTTP")
	}
	d := diagnostics.Classify(diagnostics.RetainHTTPAttempt(err, witness.Attempted()), diagnostics.CommandContext{ProviderMode: "live-http", TransportFailure: true})
	if d.Code != diagnostics.CodeOperationCancelled || !d.HTTPAttempted {
		t.Fatalf("prior successful HTTP is not a transport failure: class=%s attempted=%t", d.Code, d.HTTPAttempted)
	}
}
