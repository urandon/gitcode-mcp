package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/config"
)

type rejectUnexpectedSyncTransport struct{ calls int }

func (p *rejectUnexpectedSyncTransport) RoundTrip(*http.Request) (*http.Response, error) {
	p.calls++
	return nil, errors.New("fixture transport must not be entered")
}

func TestCLISyncPreDispatchCancellationHasNoHTTPAttempt(t *testing.T) {
	for _, expired := range []bool{false, true} {
		name := "cancelled"
		if expired {
			name = "expired"
		}
		t.Run(name, func(t *testing.T) {
			src := newTestSource(t)
			src.env[config.EnvToken] = "fixture-credential"
			cfg := filepath.Join(t.TempDir(), "config.json")
			src.env[config.EnvConfigPath] = cfg
			src.files[cfg] = []byte(`{"max_retries":0}`)
			cachePath := filepath.Join(t.TempDir(), "cache.db")
			addRepoForStartupTest(t, cachePath, "https://provider.invalid/api/v5")
			transport := &rejectUnexpectedSyncTransport{}
			previousTransport := http.DefaultTransport
			http.DefaultTransport = transport
			defer func() { http.DefaultTransport = previousTransport }()
			previousRoute := cliRoute
			defer func() { cliRoute = previousRoute }()
			cliRoute = func(ctx context.Context, args []string, stdout, stderr io.Writer, deps StartupDeps) int {
				var bounded context.Context
				var cancel context.CancelFunc
				if expired {
					bounded, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				} else {
					bounded, cancel = context.WithCancel(ctx)
					cancel()
				}
				defer cancel()
				return previousRoute(bounded, args, stdout, stderr, deps)
			}
			var stdout, stderr bytes.Buffer
			code := run([]string{"sync", "--live", "--issues", "--cache-path", cachePath, "--repo", "fixture-a"}, strings.NewReader(""), &stdout, &stderr, src)
			out := stdout.String() + stderr.String()
			if code != 1 || transport.calls != 0 {
				t.Fatal("pre-dispatch cancellation did not preserve zero transport entry")
			}
			if strings.Contains(out, "http_attempted: true") {
				t.Fatal("zero transport entry was reported as HTTP attempted")
			}
			if !strings.Contains(out, "http_attempted: false") {
				t.Fatal("missing explicit no-attempt diagnostic")
			}
			want := "operation_cancelled"
			if expired {
				want = "operation_timeout"
			}
			if !strings.Contains(out, "failure_class: "+want) {
				t.Fatal("pre-provider interruption must not be classified as a transport failure")
			}
		})
	}
}

func TestCLISyncKeepsEmptyListHTTPBeforeLaterCancellation(t *testing.T) {
	src := newTestSource(t)
	src.env[config.EnvToken] = "fixture-credential"
	cfg := filepath.Join(t.TempDir(), "config.json")
	src.env[config.EnvConfigPath] = cfg
	src.files[cfg] = []byte(`{"max_retries":0}`)
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	addRepoForStartupTest(t, cachePath, "https://provider.invalid")
	previousRoute := cliRoute
	t.Cleanup(func() { cliRoute = previousRoute })
	var cancel context.CancelFunc
	cliRoute = func(ctx context.Context, args []string, stdout, stderr io.Writer, deps StartupDeps) int {
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		return previousRoute(ctx, args, stdout, stderr, deps)
	}
	calls := 0
	withStartupTransport(t, startupRoundTripper(func(req *http.Request) (*http.Response, error) {
		calls++
		cancel()
		return &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: -1, Body: io.NopCloser(strings.NewReader(`[]`)), Request: req}, nil
	}))
	var stdout, stderr bytes.Buffer
	code := run([]string{"sync", "--live", "--issues", "--wiki", "--cache-path", cachePath, "--repo", "fixture-a"}, strings.NewReader(""), &stdout, &stderr, src)
	out := stdout.String() + stderr.String()
	if code != 1 || calls != 1 || !strings.Contains(out, "http_attempted: true") || !strings.Contains(out, "failure_class: operation_cancelled") {
		t.Fatal("late cancellation lost actual HTTP evidence or invented a transport failure")
	}
}

func TestCLISyncParentCancellationKeepsEnteredFailure(t *testing.T) {
	for _, collection := range []string{"--issues", "--wiki"} {
		t.Run(collection, func(t *testing.T) {
			src := newTestSource(t)
			src.env[config.EnvToken] = "fixture-credential"
			cfg := filepath.Join(t.TempDir(), "config.json")
			src.env[config.EnvConfigPath] = cfg
			src.files[cfg] = []byte(`{"max_retries":0}`)
			cachePath := filepath.Join(t.TempDir(), "cache.db")
			addRepoForStartupTest(t, cachePath, "https://provider.invalid")
			previousRoute := cliRoute
			t.Cleanup(func() { cliRoute = previousRoute })
			var cancel context.CancelFunc
			cliRoute = func(ctx context.Context, args []string, stdout, stderr io.Writer, deps StartupDeps) int {
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				return previousRoute(ctx, args, stdout, stderr, deps)
			}
			calls := 0
			withStartupTransport(t, startupRoundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				cancel()
				return nil, context.Canceled
			}))
			var stdout, stderr bytes.Buffer
			code := run([]string{"sync", "--live", collection, "--max-pages", "1", "--cache-path", cachePath, "--repo", "fixture-a"}, strings.NewReader(""), &stdout, &stderr, src)
			out := stdout.String() + stderr.String()
			if code != 1 || calls != 1 || !strings.Contains(out, "http_attempted: true") || !strings.Contains(out, "failure_class: live_transport_failure") {
				t.Fatal("parent cancellation discarded entered transport failure")
			}
		})
	}
}
