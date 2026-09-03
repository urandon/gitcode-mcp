package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
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
		{name: "last_server_status", err: gitcode.ErrNetworkUnavailable{Status: 599}, class: "network_unavailable", transient: true},
		{name: "outside_server_range", err: gitcode.ErrNetworkUnavailable{Status: 600}, class: "network_unavailable"},
		{name: "client", err: gitcode.ErrNetworkUnavailable{Status: 400}, class: "network_unavailable"},
		{name: "rate_limit", err: gitcode.ErrRateLimited{RetryAfter: 17 * time.Second}, class: "rate_limited", transient: true, retryAfter: 17 * time.Second},
		{name: "partial_transport", err: gitcode.ErrPartialResponse{Expected: 10, Got: 5, Cause: io.ErrUnexpectedEOF}, class: "partial_response", transient: true},
		{name: "partial_truncated_json", err: gitcode.ErrPartialResponse{Message: "truncated JSON"}, class: "partial_response", transient: true},
		{name: "partial_semantic", err: gitcode.ErrPartialResponse{Message: "wiki file metadata requires path and sha"}, class: "partial_response"},
		{name: "partial_invalid_content", err: gitcode.ErrPartialResponse{Message: "invalid base64 wiki content", Cause: errors.New("corrupt input")}, class: "partial_response"},
		{name: "wrapped_timeout", err: service.ErrSyncFailure{Mode: "network_timeout"}, class: "network_timeout", transient: true},
		{name: "auth", err: gitcode.ErrAuthExpired{}, class: "auth_expired"},
		{name: "permission", err: gitcode.ErrForbidden{}, class: "forbidden"},
		{name: "query", err: gitcode.ErrAPIValidation{}, class: "api_validation"},
		{name: "schema", err: gitcode.ErrSchemaDecode{}, class: "schema_decode"},
		{name: "validation", err: gitcode.ErrValidationFailed{}, class: "validation_failed"},
		{name: "wrapped_permission", err: fmt.Errorf("outer: %w", gitcode.ErrForbidden{}), class: "forbidden"},
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
	cappedA := syncCollectionRetryDelay("cache-a", "owner/repo", "wiki", "next_page:2", 99, 0)
	cappedB := syncCollectionRetryDelay("cache-b", "owner/repo", "wiki", "next_page:2", 99, 0)
	if cappedA == cappedB {
		t.Fatalf("capped retry jitter collapsed: %s", cappedA)
	}
	if got := syncCollectionRetryDelay("cache-a", "owner/repo", "wiki", "next_page:2", 99, 2*time.Minute); got != 2*time.Minute {
		t.Fatalf("Retry-After was not honored: %s", got)
	}
}

func TestAggregateSyncCollectionOutcomePreservesUsableProgress(t *testing.T) {
	tests := []struct {
		name string
		in   []SyncCollectionView
		want SyncAggregateHealth
	}{
		{name: "success", in: []SyncCollectionView{{Outcome: SyncCollectionSuccess}}, want: SyncHealthSucceeded},
		{name: "no_change", in: []SyncCollectionView{{Outcome: SyncCollectionNoChange}}, want: SyncHealthSucceeded},
		{name: "retrying", in: []SyncCollectionView{{Outcome: SyncCollectionSuccess, Committed: 2}, {Outcome: SyncCollectionRetryScheduled}}, want: SyncHealthPartialRetrying},
		{name: "usable_partial", in: []SyncCollectionView{{Outcome: SyncCollectionSuccess, Committed: 2}, {Outcome: SyncCollectionPermanentFailure}}, want: SyncHealthPartial},
		{name: "partial_batch", in: []SyncCollectionView{{Outcome: SyncCollectionPartial, Committed: 2}}, want: SyncHealthPartial},
		{name: "total_failure", in: []SyncCollectionView{{Outcome: SyncCollectionPermanentFailure}}, want: SyncHealthFailed},
		{name: "cancelled", in: []SyncCollectionView{{Outcome: SyncCollectionCancelled}}, want: SyncHealthCancelled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := aggregateSyncCollectionOutcome(tc.in); got != tc.want {
				t.Fatalf("aggregate=%q want=%q", got, tc.want)
			}
		})
	}
}

func TestSyncCollectionViewIsContentFreeAndOmitsUnsetTimes(t *testing.T) {
	privateFrontier := "https://token@example.invalid/private/path?cursor=secret"
	view := SyncCollectionView{
		Collection:  "wiki",
		Outcome:     SyncCollectionRetryScheduled,
		FrontierRef: publicSyncFrontierRef("cache-a", "owner/repo", "wiki", privateFrontier),
		UpdatedAt:   time.Unix(1_700_000_000, 0).UTC(),
	}
	body, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	serialized := string(body)
	for _, forbidden := range []string{privateFrontier, "token@", "private/path", "cursor=secret", "0001-01-01"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("public collection state leaked %q: %s", forbidden, serialized)
		}
	}
	if strings.Contains(serialized, "retry_after") || strings.Contains(serialized, "last_success_at") {
		t.Fatalf("unset timestamps were serialized: %s", serialized)
	}
}

func TestCloneJobDeepCopiesSyncCollections(t *testing.T) {
	original := &Job{SyncCollections: []SyncCollectionView{{Collection: "issues", Outcome: SyncCollectionSuccess}}}
	cloned := cloneJob(original)
	cloned.SyncCollections[0].Collection = "mutated"
	if original.SyncCollections[0].Collection != "issues" {
		t.Fatalf("clone shared sync collection backing storage: %+v", original.SyncCollections)
	}
}
