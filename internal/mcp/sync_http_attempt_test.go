package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/diagnostics"
	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/service"
)

type syncAttemptRoundTripper func(*http.Request) (*http.Response, error)

func (f syncAttemptRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestMCPSyncLiveHTTPAttemptParity(t *testing.T) {
	for _, tc := range []struct {
		name                                           string
		before, expired, empty, parentCancel, wikiOnly bool
		wantClass                                      string
		wantCalls                                      int
	}{
		{name: "before_cancel", before: true, wantClass: "operation_cancelled"},
		{name: "before_deadline", before: true, expired: true, wantClass: "operation_timeout"},
		{name: "inside_cancel", wantClass: "live_transport_failure", wantCalls: 1},
		{name: "inside_deadline", expired: true, wantClass: "live_transport_failure", wantCalls: 1},
		{name: "after_empty_list", empty: true, wantClass: "operation_cancelled", wantCalls: 1},
		{name: "issues_parent_cancel", parentCancel: true, wantClass: "live_transport_failure", wantCalls: 1},
		{name: "wiki_parent_cancel", parentCancel: true, wikiOnly: true, wantClass: "live_transport_failure", wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, err := cache.NewInMemorySQLiteStore(context.Background())
			if err != nil {
				t.Fatal("cache setup failed")
			}
			defer store.Close()
			if err := store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "fixture-a", Owner: "example-owner", Name: "example-repo", APIBaseURL: "https://provider.invalid", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
				t.Fatal("binding setup failed")
			}
			svc, err := service.NewWithMode(store, gitcode.ProviderModeLive, "fixture-credential", service.ServiceConfig{BaseURL: "https://provider.invalid"})
			if err != nil {
				t.Fatal("provider setup failed")
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.before {
				cancel()
				if tc.expired {
					ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
					defer cancel()
				}
			}
			calls := 0
			previous := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = previous })
			http.DefaultTransport = syncAttemptRoundTripper(func(req *http.Request) (*http.Response, error) {
				calls++
				if tc.parentCancel {
					cancel()
				}
				if tc.empty {
					cancel()
					return &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: -1, Body: io.NopCloser(strings.NewReader(`[]`)), Request: req}, nil
				}
				if tc.expired {
					return nil, context.DeadlineExceeded
				}
				return nil, context.Canceled
			})
			var out bytes.Buffer
			srv := NewWithToolAccess(nil, &out, io.Discard, svc, nil, ToolAccessWrite)
			id := json.RawMessage(`"attempt-parity"`)
			args, _ := json.Marshal(map[string]any{"repo_id": "fixture-a", "issues": !tc.wikiOnly, "wiki": tc.empty || tc.wikiOnly, "max_pages": 1})
			srv.callSyncLive(ctx, &id, args)
			var envelope struct {
				Result struct {
					StructuredContent syncLiveResult `json:"structuredContent"`
				} `json:"result"`
			}
			if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
				t.Fatal("response decode failed")
			}
			ds := envelope.Result.StructuredContent.Diagnostics
			if tc.empty {
				coverage := envelope.Result.StructuredContent
				if coverage.TraversalStatus == "complete" || coverage.Continuation == "" {
					t.Fatal("nil-result cancelled sibling falsely claimed complete coverage")
				}
			}
			if calls != tc.wantCalls || len(ds) == 0 {
				t.Fatal("missing diagnostic or unexpected provider traffic")
			}
			last := ds[len(ds)-1]
			if last.HTTPAttempted == nil || *last.HTTPAttempted != (calls != 0) || last.ErrorClass != tc.wantClass {
				t.Fatalf("MCP evidence mismatch: class=%s attempted_present=%t calls=%d", last.ErrorClass, last.HTTPAttempted != nil, calls)
			}
		})
	}
}

func TestMCPSyncLaterPreDispatchInterruptionKeepsPriorHTTP(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		for _, entered := range []int{0, 1} {
			network := gitcode.ErrNetworkUnavailable{Attempts: entered, Cause: cause}
			failure := service.ErrSyncFailure{Mode: "network_timeout", Cause: network}
			partial := &service.PartialSyncError{Errors: []service.ResourceError{{Err: failure, FailureClass: "network_timeout"}}, SuccessCount: 1, FailureCount: 1}
			partial.Errors = append(partial.Errors, service.ResourceError{Err: diagnostics.RetainPreDispatchInterruption(cause)})
			partial.FailureCount++
			partial.PreserveHTTPAttemptEvidence(diagnostics.RetainHTTPAttempt(failure, true))
			d, ok := mcpDiagnostic(partial)
			want := diagnostics.CodeOperationCancelled
			if cause == context.DeadlineExceeded {
				want = diagnostics.CodeOperationTimeout
			}
			if entered != 0 {
				want = diagnostics.CodeLiveTransportFailure
			}
			if !ok || d.Code != want || !d.HTTPAttempted {
				t.Fatalf("partial interruption mismatch: entered=%d class=%s attempted=%t", entered, d.Code, d.HTTPAttempted)
			}
		}
	}
}

func TestMCPOperationalErrorIncludesKnownNoHTTPAttempt(t *testing.T) {
	var out bytes.Buffer
	srv := &Server{writer: &out, stderr: io.Discard}
	id := json.RawMessage(`"attempt-error"`)
	srv.writeOperationalError(&id, gitcode.ErrNetworkUnavailable{Attempts: 0, Cause: context.Canceled}, domainErrorContext{Operation: "sync_live"})
	var resp response
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil || resp.Error == nil || resp.Error.Data == nil {
		t.Fatal("missing operational error")
	}
	d := resp.Error.Data
	if d.FailureClass != "operation_cancelled" || d.HTTPAttempted == nil || *d.HTTPAttempted {
		t.Fatal("operational envelope contradicted zero transport entry")
	}
}
