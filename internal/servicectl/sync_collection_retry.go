package servicectl

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"gitcode-mcp/internal/gitcode"
	"gitcode-mcp/internal/service"
)

const (
	defaultSyncCollectionRetryBudget = 4
	maxSyncCollectionRetryDelay      = time.Minute
)

type SyncCollectionOutcome string
type SyncAggregateHealth string

const (
	SyncCollectionSuccess          SyncCollectionOutcome = "success"
	SyncCollectionNoChange         SyncCollectionOutcome = "no_change"
	SyncCollectionPartial          SyncCollectionOutcome = "partial"
	SyncCollectionRetryScheduled   SyncCollectionOutcome = "retry_scheduled"
	SyncCollectionPermanentFailure SyncCollectionOutcome = "permanent_failure"
	SyncCollectionCancelled        SyncCollectionOutcome = "cancelled"

	SyncHealthSucceeded       SyncAggregateHealth = "succeeded"
	SyncHealthPartialRetrying SyncAggregateHealth = "partial/retrying"
	SyncHealthPartial         SyncAggregateHealth = "partial"
	SyncHealthFailed          SyncAggregateHealth = "failed"
	SyncHealthCancelled       SyncAggregateHealth = "cancelled"
)

// SyncCollectionView is the public, content-free health contract for one
// remote collection. FrontierRef is an opaque fingerprint, never a provider
// URL, cursor, local path, or source payload.
type SyncCollectionView struct {
	Collection    string                `json:"collection"`
	Outcome       SyncCollectionOutcome `json:"outcome"`
	FrontierRef   string                `json:"frontier_ref,omitempty"`
	RecordsListed int                   `json:"records_listed,omitempty"`
	Committed     int                   `json:"committed,omitempty"`
	Failed        int                   `json:"failed,omitempty"`
	ErrorClass    string                `json:"error_class,omitempty"`
	Attempt       int                   `json:"attempt,omitempty"`
	RetryBudget   int                   `json:"retry_budget,omitempty"`
	RetryAfter    *time.Time            `json:"retry_after,omitempty"`
	LastSuccessAt *time.Time            `json:"last_success_at,omitempty"`
	UpdatedAt     time.Time             `json:"updated_at"`
}

func aggregateSyncCollectionOutcome(collections []SyncCollectionView) SyncAggregateHealth {
	usable, failed, retrying, cancelled := false, false, false, false
	for _, collection := range collections {
		usable = usable || collection.Committed > 0 || collection.Outcome == SyncCollectionSuccess || collection.Outcome == SyncCollectionNoChange
		switch collection.Outcome {
		case SyncCollectionRetryScheduled:
			retrying = true
		case SyncCollectionPartial, SyncCollectionPermanentFailure:
			failed = true
		case SyncCollectionCancelled:
			cancelled = true
		}
	}
	switch {
	case retrying:
		return SyncHealthPartialRetrying
	case failed && usable:
		return SyncHealthPartial
	case failed:
		return SyncHealthFailed
	case cancelled:
		return SyncHealthCancelled
	default:
		return SyncHealthSucceeded
	}
}

type syncCollectionFailurePolicy struct {
	ErrorClass string
	Transient  bool
	RetryAfter time.Duration
}

type syncCollectionAttempt func() (*service.SyncResourcesResult, syncCollectionResult, error)
type syncCollectionObserver func(SyncCollectionView)
type syncCollectionWait func(context.Context, time.Duration) error

type syncCollectionTask struct {
	Collection      string
	RemoteType      string
	PrivateFrontier string
	AttemptStart    int
	DueAt           time.Time
	Attempt         func(int) (*service.SyncResourcesResult, syncCollectionResult, error)
}

type syncCollectionRetryHooks struct {
	Scheduled func(syncCollectionTask, int, time.Time) error
}

type syncCollectionExecution struct {
	Result     *service.SyncResourcesResult
	Collection syncCollectionResult
	Err        error
}

type pendingSyncCollection struct {
	task    syncCollectionTask
	attempt int
	dueAt   time.Time
	result  *service.SyncResourcesResult
}

// runSyncCollectionSchedule walks every due collection before sleeping for a
// delayed retry. A timeout in one frontier therefore cannot hold up unrelated
// collections that are ready now.
func runSyncCollectionSchedule(
	ctx context.Context,
	cacheUUID, repoID string,
	tasks []syncCollectionTask,
	budget int,
	observe syncCollectionObserver,
	wait syncCollectionWait,
	now func() time.Time,
	hooks *syncCollectionRetryHooks,
) []syncCollectionExecution {
	if budget <= 0 {
		budget = defaultSyncCollectionRetryBudget
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if wait == nil {
		wait = waitForSyncCollectionRetry
	}
	pending := make([]pendingSyncCollection, 0, len(tasks))
	for _, task := range tasks {
		pending = append(pending, pendingSyncCollection{task: task, attempt: task.AttemptStart, dueAt: task.DueAt})
	}
	executions := make([]syncCollectionExecution, 0, len(tasks))
	for len(pending) > 0 {
		current := -1
		observedAt := now()
		for i := range pending {
			if pending[i].dueAt.IsZero() || !pending[i].dueAt.After(observedAt) {
				current = i
				break
			}
		}
		if current < 0 {
			current = 0
			for i := 1; i < len(pending); i++ {
				if pending[i].dueAt.Before(pending[current].dueAt) {
					current = i
				}
			}
		}
		item := pending[current]
		pending = append(pending[:current], pending[current+1:]...)
		if delay := item.dueAt.Sub(now()); !item.dueAt.IsZero() && delay > 0 {
			if err := wait(ctx, delay); err != nil {
				view := syncCollectionViewForAttempt(item.task.Collection, publicSyncFrontierRef(cacheUUID, repoID, item.task.Collection, item.task.PrivateFrontier), item.attempt, budget, nil, err, now())
				if observe != nil {
					observe(view)
				}
				executions = append(executions, syncCollectionExecution{Result: item.result, Collection: syncCollectionResult{RemoteType: item.task.RemoteType, Result: item.result, Err: err}, Err: err})
				continue
			}
		}
		item.attempt++
		result, collectionResult, err := item.task.Attempt(item.attempt)
		if result != nil {
			// Attempts describe the same frontier. Keep the newest observation
			// instead of double-counting replayed idempotent commits.
			item.result = result
		}
		frontierRef := publicSyncFrontierRef(cacheUUID, repoID, item.task.Collection, item.task.PrivateFrontier)
		view := syncCollectionViewForAttempt(item.task.Collection, frontierRef, item.attempt, budget, item.result, err, now())
		policy := classifySyncCollectionFailure(err)
		if err != nil && policy.Transient && item.attempt < budget && ctx.Err() == nil {
			delay := syncCollectionRetryDelay(cacheUUID, repoID, item.task.Collection, frontierRef, item.attempt, policy.RetryAfter)
			retryAt := now().Add(delay)
			view.Outcome, view.RetryAfter = SyncCollectionRetryScheduled, &retryAt
			if hooks != nil && hooks.Scheduled != nil {
				if persistErr := hooks.Scheduled(item.task, item.attempt, retryAt); persistErr != nil {
					view.Outcome, view.RetryAfter, view.ErrorClass = SyncCollectionPermanentFailure, nil, "retry_checkpoint_persist_failed"
					if observe != nil {
						observe(view)
					}
					collectionResult.Err = persistErr
					executions = append(executions, syncCollectionExecution{Result: item.result, Collection: collectionResult, Err: persistErr})
					continue
				}
			}
			if observe != nil {
				observe(view)
			}
			item.dueAt = retryAt
			pending = append(pending, item)
			continue
		}
		if observe != nil {
			observe(view)
		}
		collectionResult.Result = item.result
		executions = append(executions, syncCollectionExecution{Result: item.result, Collection: collectionResult, Err: err})
	}
	return executions
}

func runSyncCollectionWithRetry(
	ctx context.Context,
	cacheUUID, repoID, collection, privateFrontier string,
	budget int,
	attempt syncCollectionAttempt,
	observe syncCollectionObserver,
	wait syncCollectionWait,
	now func() time.Time,
) (*service.SyncResourcesResult, syncCollectionResult, error) {
	if budget <= 0 {
		budget = defaultSyncCollectionRetryBudget
	}
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	if wait == nil {
		wait = waitForSyncCollectionRetry
	}
	frontierRef := publicSyncFrontierRef(cacheUUID, repoID, collection, privateFrontier)
	for number := 1; number <= budget; number++ {
		result, collectionResult, err := attempt()
		view := syncCollectionViewForAttempt(collection, frontierRef, number, budget, result, err, now())
		if err == nil {
			if observe != nil {
				observe(view)
			}
			return result, collectionResult, nil
		}
		policy := classifySyncCollectionFailure(err)
		if policy.Transient && number < budget && ctx.Err() == nil {
			delay := syncCollectionRetryDelay(cacheUUID, repoID, collection, frontierRef, number, policy.RetryAfter)
			retryAt := now().Add(delay)
			view.Outcome = SyncCollectionRetryScheduled
			view.RetryAfter = &retryAt
			if observe != nil {
				observe(view)
			}
			if waitErr := wait(ctx, delay); waitErr != nil {
				cancelled := syncCollectionViewForAttempt(collection, frontierRef, number, budget, result, waitErr, now())
				if observe != nil {
					observe(cancelled)
				}
				collectionResult.Err = waitErr
				return result, collectionResult, waitErr
			}
			continue
		}
		if observe != nil {
			observe(view)
		}
		return result, collectionResult, err
	}
	panic("unreachable sync collection retry loop")
}

func syncCollectionViewForAttempt(collection, frontierRef string, attempt, budget int, result *service.SyncResourcesResult, err error, now time.Time) SyncCollectionView {
	view := SyncCollectionView{
		Collection: collection, FrontierRef: frontierRef, Attempt: attempt, RetryBudget: budget, UpdatedAt: now.UTC(),
	}
	if result != nil {
		view.RecordsListed = result.RecordsListed
		view.Committed = result.SuccessCount
		view.Failed = result.FailureCount
	}
	if err == nil {
		view.Outcome = SyncCollectionSuccess
		if result == nil || result.RecordsListed == 0 {
			view.Outcome = SyncCollectionNoChange
		}
		succeededAt := now.UTC()
		view.LastSuccessAt = &succeededAt
		return view
	}
	policy := classifySyncCollectionFailure(err)
	view.ErrorClass = policy.ErrorClass
	switch {
	case errors.Is(err, context.Canceled):
		view.Outcome = SyncCollectionCancelled
	case result != nil && result.SuccessCount > 0:
		view.Outcome = SyncCollectionPartial
	default:
		view.Outcome = SyncCollectionPermanentFailure
	}
	return view
}

func waitForSyncCollectionRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(max(delay, 0))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// classifySyncCollectionFailure is deliberately allow-list based. Unknown,
// authentication, permission, query, schema, and validation errors must not be
// hidden behind a network retry loop.
func classifySyncCollectionFailure(err error) syncCollectionFailurePolicy {
	if err == nil {
		return syncCollectionFailurePolicy{}
	}
	if errors.Is(err, context.Canceled) {
		return syncCollectionFailurePolicy{ErrorClass: "cancelled"}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return syncCollectionFailurePolicy{ErrorClass: "network_timeout", Transient: true}
	}
	var rateLimited gitcode.ErrRateLimited
	if errors.As(err, &rateLimited) {
		return syncCollectionFailurePolicy{ErrorClass: "rate_limited", Transient: true, RetryAfter: max(rateLimited.RetryAfter, 0)}
	}
	var unavailable gitcode.ErrNetworkUnavailable
	if errors.As(err, &unavailable) {
		class := "network_unavailable"
		if errors.Is(unavailable.Cause, context.DeadlineExceeded) {
			class = "network_timeout"
		}
		return syncCollectionFailurePolicy{ErrorClass: class, Transient: unavailable.Status == 0 || unavailable.Status >= 500 && unavailable.Status <= 599}
	}
	var partial gitcode.ErrPartialResponse
	if errors.As(err, &partial) {
		return syncCollectionFailurePolicy{ErrorClass: "partial_response", Transient: transientPartialResponse(partial)}
	}
	var syncFailure service.ErrSyncFailure
	if errors.As(err, &syncFailure) {
		class := sanitizeMaintenanceErrorClass(syncFailure.Mode, "sync_failed")
		switch class {
		case "network_timeout", "network_unavailable", "partial_response":
			return syncCollectionFailurePolicy{ErrorClass: class, Transient: true}
		case "rate_limited":
			return syncCollectionFailurePolicy{ErrorClass: class, Transient: true, RetryAfter: max(syncFailure.RetryAfter, 0)}
		default:
			return syncCollectionFailurePolicy{ErrorClass: class}
		}
	}
	class := "sync_failed"
	var coded interface{ DiagnosticCode() string }
	if errors.As(err, &coded) {
		class = sanitizeMaintenanceErrorClass(coded.DiagnosticCode(), class)
	}
	return syncCollectionFailurePolicy{ErrorClass: class}
}

func transientPartialResponse(partial gitcode.ErrPartialResponse) bool {
	if partial.Expected > 0 && partial.Got >= 0 && partial.Got < partial.Expected {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(partial.Message), "truncated JSON") {
		return true
	}
	if partial.Cause == nil {
		return false
	}
	if errors.Is(partial.Cause, io.EOF) || errors.Is(partial.Cause, io.ErrUnexpectedEOF) || errors.Is(partial.Cause, context.DeadlineExceeded) {
		return true
	}
	var unavailable gitcode.ErrNetworkUnavailable
	return errors.As(partial.Cause, &unavailable) && (unavailable.Status == 0 || unavailable.Status >= 500 && unavailable.Status <= 599)
}

func publicSyncFrontierRef(cacheUUID, repoID, collection, privateFrontier string) string {
	digest := sha256.Sum256([]byte(strings.Join([]string{cacheUUID, repoID, collection, privateFrontier}, "\x00")))
	return "frontier-" + hex.EncodeToString(digest[:12])
}

func syncCollectionRetryDelay(cacheUUID, repoID, collection, frontier string, attempt int, retryAfter time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	shift := min(attempt-1, 6)
	base := time.Second * time.Duration(1<<shift)
	jitterWindow := max(base/4, time.Millisecond)
	if base+jitterWindow > maxSyncCollectionRetryDelay {
		jitterWindow = max(maxSyncCollectionRetryDelay/4, time.Millisecond)
		base = maxSyncCollectionRetryDelay - jitterWindow
	}
	seed := strings.Join([]string{cacheUUID, repoID, collection, frontier, strconv.Itoa(attempt)}, "\x00")
	digest := sha256.Sum256([]byte(seed))
	jitter := time.Duration(binary.BigEndian.Uint64(digest[:8]) % uint64(jitterWindow))
	return max(base+jitter, retryAfter)
}
