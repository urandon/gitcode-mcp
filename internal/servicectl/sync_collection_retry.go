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
