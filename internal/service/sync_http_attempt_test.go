package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/diagnostics"
	"gitcode-mcp/internal/gitcode"
)

type syncAttemptTransport func(*http.Request) (*http.Response, error)

func (f syncAttemptTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestSyncOwnersRetainPreProviderInterruption(t *testing.T) {
	store, err := cache.NewInMemorySQLiteStore(context.Background())
	if err != nil {
		t.Fatal("cache setup failed")
	}
	defer store.Close()
	svc := New(store)
	for _, expired := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		want := diagnostics.CodeOperationCancelled
		if expired {
			ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			defer cancel()
			want = diagnostics.CodeOperationTimeout
		}
		req := BulkSyncRequest{RepoID: "fixture-a", Bounds: &SyncBounds{MaxPages: 1}}
		for _, fn := range []func(context.Context, BulkSyncRequest) (*SyncResourcesResult, error){svc.BulkSyncIssues, svc.BulkSyncIssueComments, svc.BulkSyncWiki, svc.BulkSyncPullRequests, svc.BulkSyncPRComments, svc.BulkSyncAll} {
			_, err := fn(ctx, req)
			d := diagnostics.Classify(err, diagnostics.CommandContext{ProviderMode: "live-http", HTTPAttempted: true, TransportFailure: true})
			if err == nil || d.Code != want || d.HTTPAttempted {
				t.Fatal("sync owner inferred HTTP from pre-provider interruption")
			}
		}
		_, err := svc.SyncToCache(ctx, SyncRequest{RepoID: "fixture-a", RemoteAlias: "issue:1"})
		if !errors.Is(err, ctx.Err()) {
			t.Fatal("targeted sync lost cause")
		}
		_, err = svc.SyncResources(ctx, []SyncRequest{{RepoID: "fixture-a", RemoteAlias: "issue:1"}})
		if attempted, known := diagnostics.HTTPAttemptEvidence(err); !known || attempted {
			t.Fatal("multi-target sync lost known negative evidence")
		}
	}
}

func TestSyncRetainsEmptyListHTTPBeforeLaterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal("cache setup failed")
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "example-owner", Name: "example-repo", APIBaseURL: "https://provider.invalid", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal("binding setup failed")
	}
	client, _ := gitcode.NewHTTPClient(gitcode.Config{BaseURL: "https://provider.invalid"})
	svc := NewWithClient(store, client)
	previous := http.DefaultTransport
	defer func() { http.DefaultTransport = previous }()
	calls := 0
	http.DefaultTransport = syncAttemptTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		cancel()
		return &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: -1, Body: io.NopCloser(strings.NewReader(`[]`)), Request: req}, nil
	})
	_, err = svc.BulkSyncAll(ctx, BulkSyncRequest{RepoID: "fixture-a", Bounds: &SyncBounds{MaxPages: 1}})
	if attempted, known := diagnostics.HTTPAttemptEvidence(err); err == nil || !known || !attempted || calls != 1 {
		t.Fatal("empty list HTTP lost after later cancellation")
	}
}

func TestSyncPartialAggregatePreservesAttemptAndDiagnostic(t *testing.T) {
	_, retain := beginSyncHTTPAttempt(context.Background())
	negative := retain(&PartialSyncError{Diagnostic: SyncDiagnosticCancelled})
	ctx, witness := diagnostics.WithHTTPAttemptWitness(context.Background())
	diagnostics.RecordHTTPAttempt(ctx)
	_, retainPositive := beginSyncHTTPAttempt(ctx)
	positive := retainPositive(&PartialSyncError{SuccessCount: 1, Diagnostic: SyncDiagnosticTimeout})
	if !witness.Attempted() {
		t.Fatal("positive witness absent")
	}
	merged := &PartialSyncError{SuccessCount: 1}
	merged.PreserveHTTPAttemptEvidence(negative, positive)
	if attempted, known := merged.HTTPAttemptEvidence(); !known || !attempted || merged.Diagnostic != SyncDiagnosticCancelled {
		t.Fatal("aggregate lost first diagnostic or positive attempt")
	}
	if _, ok := positive.(*PartialSyncError); !ok {
		t.Fatal("partial error pointer contract changed")
	}
}

func TestSyncCollectionsPriorHTTPThenLocalRateCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := cache.NewInMemorySQLiteStore(ctx)
	if err != nil {
		t.Fatal("cache setup failed")
	}
	defer store.Close()
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "fixture-a", Owner: "example-owner", Name: "example-repo", APIBaseURL: "https://provider.invalid", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal("binding setup failed")
	}
	svc, err := NewWithMode(store, gitcode.ProviderModeLive, "fixture-credential", ServiceConfig{BaseURL: "https://provider.invalid", RateLimitRPS: 0.001, RateLimitBurst: 1})
	if err != nil {
		t.Fatal("provider setup failed")
	}
	calls := 0
	previous := http.DefaultTransport
	defer func() { http.DefaultTransport = previous }()
	http.DefaultTransport = syncAttemptTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{}, ContentLength: -1, Body: io.NopCloser(strings.NewReader(`[]`)), Request: req}, nil
	})
	ctx, _ = diagnostics.WithHTTPAttemptWitness(ctx)
	_, err = svc.BulkSyncIssues(ctx, BulkSyncRequest{RepoID: "fixture-a", Bounds: &SyncBounds{MaxPages: 1}})
	if err != nil || calls != 1 {
		t.Fatal("first collection must succeed with exactly one HTTP request")
	}
	ctx = gitcode.WithRateLimitObserver(ctx, func(event gitcode.RateLimitEvent) {
		if event.Type == gitcode.RateLimitEventThrottleWaitStarted {
			cancel()
		}
	})
	_, err = svc.BulkSyncPullRequests(ctx, BulkSyncRequest{RepoID: "fixture-a", Bounds: &SyncBounds{MaxPages: 1}})
	var network gitcode.ErrNetworkUnavailable
	if !errors.As(err, &network) || network.Attempts != 0 || calls != 1 || !errors.Is(err, context.Canceled) {
		t.Fatal("later collection must stop locally before another HTTP request")
	}
	d := diagnostics.Classify(err, diagnostics.CommandContext{ProviderMode: "live-http", TransportFailure: true})
	if d.Code != diagnostics.CodeOperationCancelled || !d.HTTPAttempted {
		t.Fatalf("shared owner evidence mismatch: class=%s attempted=%t", d.Code, d.HTTPAttempted)
	}
}
