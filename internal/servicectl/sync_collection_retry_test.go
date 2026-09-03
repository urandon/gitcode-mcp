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

func TestMaintenanceCollectionRetrySelectionIsExactAndPolicyGated(t *testing.T) {
	policy := MaintenancePolicy{Issues: true, IssueComments: true, Wiki: true, Pulls: true, PRComments: true}
	tests := []struct {
		collection string
		remoteType string
		selected   func(StartSyncJobRequest) bool
	}{
		{"issues", "issue", func(req StartSyncJobRequest) bool { return req.Issues }},
		{"issue_comments", "issue_comment", func(req StartSyncJobRequest) bool { return req.IssueComments }},
		{"wiki", "wiki", func(req StartSyncJobRequest) bool { return req.Wiki }},
		{"pulls", "pull_request", func(req StartSyncJobRequest) bool { return req.Pulls }},
		{"pr_comments", "pr_comment", func(req StartSyncJobRequest) bool { return req.PRComments }},
	}
	for _, test := range tests {
		remoteType, req, ok := maintenanceCollectionRetrySelection(policy, test.collection)
		if !ok || remoteType != test.remoteType || !test.selected(req) {
			t.Fatalf("collection=%s remote=%s selected=%+v ok=%t", test.collection, remoteType, req, ok)
		}
		selected := 0
		for _, value := range []bool{req.Issues, req.IssueComments, req.Wiki, req.Pulls, req.PRComments} {
			if value {
				selected++
			}
		}
		if selected != 1 {
			t.Fatalf("collection=%s selected %d cohorts: %+v", test.collection, selected, req)
		}
	}
	if _, _, ok := maintenanceCollectionRetrySelection(MaintenancePolicy{}, "wiki"); ok {
		t.Fatal("disabled collection was retryable")
	}
	if _, _, ok := maintenanceCollectionRetrySelection(policy, "unknown"); ok {
		t.Fatal("unknown collection was retryable")
	}
}

func TestRunSyncCollectionWithRetryIsolatedAndBounded(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0).UTC()
	attempts := 0
	observed := []SyncCollectionView{}
	waits := []time.Duration{}
	result, collection, err := runSyncCollectionWithRetry(
		context.Background(), "cache-a", "owner/repo", "issues", "page:1", 4,
		func() (*service.SyncResourcesResult, syncCollectionResult, error) {
			attempts++
			if attempts == 1 {
				err := gitcode.ErrNetworkUnavailable{Status: 503}
				return nil, syncCollectionResult{RemoteType: "issue", Err: err}, err
			}
			result := &service.SyncResourcesResult{RecordsListed: 2, SuccessCount: 2}
			return result, syncCollectionResult{RemoteType: "issue", Result: result}, nil
		},
		func(view SyncCollectionView) { observed = append(observed, view) },
		func(_ context.Context, delay time.Duration) error {
			waits = append(waits, delay)
			clock = clock.Add(delay)
			return nil
		},
		func() time.Time { return clock },
	)
	if err != nil || result == nil || result.SuccessCount != 2 || collection.Err != nil {
		t.Fatalf("result=%+v collection=%+v err=%v", result, collection, err)
	}
	if attempts != 2 || len(waits) != 1 || len(observed) != 2 {
		t.Fatalf("attempts=%d waits=%v observed=%+v", attempts, waits, observed)
	}
	if observed[0].Outcome != SyncCollectionRetryScheduled || observed[0].RetryAfter == nil || observed[0].ErrorClass != "network_unavailable" {
		t.Fatalf("retry observation=%+v", observed[0])
	}
	if observed[1].Outcome != SyncCollectionSuccess || observed[1].LastSuccessAt == nil || observed[1].Attempt != 2 {
		t.Fatalf("success observation=%+v", observed[1])
	}
}

func TestRunSyncCollectionWithRetryDoesNotRetryPermanentFailure(t *testing.T) {
	attempts := 0
	_, _, err := runSyncCollectionWithRetry(
		context.Background(), "cache-a", "owner/repo", "wiki", "page:1", 4,
		func() (*service.SyncResourcesResult, syncCollectionResult, error) {
			attempts++
			err := gitcode.ErrPartialResponse{Message: "wiki file metadata requires path and sha"}
			return nil, syncCollectionResult{RemoteType: "wiki", Err: err}, err
		}, nil,
		func(context.Context, time.Duration) error {
			t.Fatal("permanent failure must not wait for a retry")
			return nil
		}, nil,
	)
	if err == nil || attempts != 1 {
		t.Fatalf("err=%v attempts=%d", err, attempts)
	}
}

func TestRunSyncCollectionScheduleDoesNotBlockOrRepeatDueCollections(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0).UTC()
	order := []string{}
	observed := []SyncCollectionView{}
	issueAttempts := 0
	waitedAfter := ""
	tasks := []syncCollectionTask{
		{
			Collection: "issues", RemoteType: "issue", PrivateFrontier: "page:1",
			Attempt: func(attempt int) (*service.SyncResourcesResult, syncCollectionResult, error) {
				issueAttempts++
				order = append(order, fmt.Sprintf("issues:%d", attempt))
				if attempt == 1 {
					err := gitcode.ErrNetworkUnavailable{Status: 503}
					result := &service.SyncResourcesResult{RecordsListed: 1, SuccessCount: 1}
					return result, syncCollectionResult{RemoteType: "issue", Result: result, Err: err}, err
				}
				result := &service.SyncResourcesResult{RecordsListed: 1, SuccessCount: 1}
				return result, syncCollectionResult{RemoteType: "issue", Result: result}, nil
			},
		},
		{
			Collection: "wiki", RemoteType: "wiki", PrivateFrontier: "page:1",
			Attempt: func(attempt int) (*service.SyncResourcesResult, syncCollectionResult, error) {
				order = append(order, fmt.Sprintf("wiki:%d", attempt))
				result := &service.SyncResourcesResult{RecordsListed: 2, SuccessCount: 2}
				return result, syncCollectionResult{RemoteType: "wiki", Result: result}, nil
			},
		},
	}
	executions := runSyncCollectionSchedule(
		context.Background(), "cache-a", "owner/repo", tasks, 4, func(view SyncCollectionView) { observed = append(observed, view) },
		func(_ context.Context, delay time.Duration) error {
			waitedAfter = order[len(order)-1]
			clock = clock.Add(delay)
			return nil
		},
		func() time.Time { return clock }, nil,
	)
	if got, want := strings.Join(order, ","), "issues:1,wiki:1,issues:2"; got != want {
		t.Fatalf("attempt order=%q want=%q", got, want)
	}
	if waitedAfter != "wiki:1" {
		t.Fatalf("backoff waited before another due collection: waited_after=%q", waitedAfter)
	}
	if issueAttempts != 2 || len(executions) != 2 {
		t.Fatalf("issue_attempts=%d executions=%+v", issueAttempts, executions)
	}
	if len(observed) != 3 || observed[0].Collection != "issues" || observed[0].Outcome != SyncCollectionRetryScheduled || observed[0].Committed != 1 {
		t.Fatalf("partial committed progress was not retained while retrying: %+v", observed)
	}
	for _, execution := range executions {
		if execution.Err != nil {
			t.Fatalf("terminal execution failed: %+v", execution)
		}
		if execution.Collection.RemoteType == "issue" && (execution.Result == nil || execution.Result.SuccessCount != 1) {
			t.Fatalf("retry replay counts were summed: %+v", execution)
		}
	}
}
