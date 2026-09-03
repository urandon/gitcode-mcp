package servicectl

import (
	"context"
	"errors"
	"testing"
	"time"

	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/service"
)

func TestSyncCollectionFailurePolicyIsAllowListBased(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		class      string
		transient  bool
		retryAfter time.Duration
	}{
		{name: "deadline", err: context.DeadlineExceeded, class: "network_timeout", transient: true},
		{name: "transport", err: gitcode.ErrNetworkUnavailable{Status: 0}, class: "network_unavailable", transient: true},
		{name: "server", err: gitcode.ErrNetworkUnavailable{Status: 503}, class: "network_unavailable", transient: true},
		{name: "client", err: gitcode.ErrNetworkUnavailable{Status: 400}, class: "network_unavailable"},
		{name: "rate_limit", err: gitcode.ErrRateLimited{RetryAfter: 17 * time.Second}, class: "rate_limited", transient: true, retryAfter: 17 * time.Second},
		{name: "partial_transport", err: gitcode.ErrPartialResponse{}, class: "partial_response", transient: true},
		{name: "wrapped_timeout", err: service.ErrSyncFailure{Mode: "network_timeout"}, class: "network_timeout", transient: true},
		{name: "auth", err: gitcode.ErrAuthExpired{}, class: "auth_expired"},
		{name: "permission", err: gitcode.ErrForbidden{}, class: "forbidden"},
		{name: "query", err: gitcode.ErrAPIValidation{}, class: "api_validation"},
		{name: "schema", err: gitcode.ErrSchemaDecode{}, class: "schema_decode"},
		{name: "validation", err: gitcode.ErrValidationFailed{}, class: "validation_failed"},
		{name: "cancelled", err: context.Canceled, class: "cancelled"},
		{name: "unknown", err: errors.New("private provider detail"), class: "sync_failed"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifySyncCollectionFailure(tc.err)
			if got.ErrorClass != tc.class || got.Transient != tc.transient || got.RetryAfter != tc.retryAfter {
				t.Fatalf("policy=%+v want class=%q transient=%t retry_after=%s", got, tc.class, tc.transient, tc.retryAfter)
			}
		})
	}
}

func TestSyncCollectionRetryDelayIsDeterministicBoundedAndHonorsRetryAfter(t *testing.T) {
	first := syncCollectionRetryDelay("cache-a", "owner/repo", "wiki", "next_page:2", 3, 0)
	second := syncCollectionRetryDelay("cache-a", "owner/repo", "wiki", "next_page:2", 3, 0)
	if first != second || first < 4*time.Second || first >= 5*time.Second {
		t.Fatalf("deterministic delay=%s second=%s", first, second)
	}
	if other := syncCollectionRetryDelay("cache-a", "owner/repo", "issues", "next_page:2", 3, 0); other == first {
		t.Fatalf("collection identity did not affect stable jitter: %s", other)
	}
	if got := syncCollectionRetryDelay("cache-a", "owner/repo", "wiki", "next_page:2", 99, 0); got > maxSyncCollectionRetryDelay {
		t.Fatalf("retry delay exceeded cap: %s", got)
	}
	if got := syncCollectionRetryDelay("cache-a", "owner/repo", "wiki", "next_page:2", 99, 2*time.Minute); got != 2*time.Minute {
		t.Fatalf("Retry-After was not honored: %s", got)
	}
}
