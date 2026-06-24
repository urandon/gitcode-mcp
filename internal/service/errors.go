package service

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/gitcode"
)

type ErrSyncFailure struct {
	Mode           string
	Target         string
	Endpoint       string
	RetryAfter     time.Duration
	ExpectedBytes  int64
	GotBytes       int64
	LimitBytes     int64
	SizeBytes      int64
	PayloadSource  string
	Alias          string
	ExistingID     string
	NewID          string
	LocalPayload   []byte
	RemotePayload  []byte
	RecoveryAction string
	Cause          error
}

func (e ErrSyncFailure) DiagnosticCode() string { return e.Mode }

func (e ErrSyncFailure) Error() string {
	switch e.Mode {
	case "network_timeout":
		return fmt.Sprintf("sync: network timeout for record %s: retry with --timeout to increase deadline or check connectivity", e.Target)
	case "rate_limited":
		return fmt.Sprintf("sync: rate limited. Retry after %d seconds.", int(e.RetryAfter.Seconds()))
	case "partial_response":
		return fmt.Sprintf("sync: received partial response for %s: expected %d bytes, got %d bytes. Run sync again to resume.", e.Endpoint, e.ExpectedBytes, e.GotBytes)
	case "auth_expired":
		return "sync: authentication expired. Renew your GITCODE_TOKEN and try again."
	case "live_auth_failure":
		return "sync: live_auth_failure: live provider rejected credentials. Renew your GITCODE_TOKEN and try again."
	case "live_graph_invalid":
		if e.Cause != nil {
			return "sync: live_graph_invalid: " + e.Cause.Error()
		}
		return "sync: live_graph_invalid"
	case "remote_collision":
		return fmt.Sprintf("sync: remote id %s already maps to local id %s; cannot map to %s. Run link-check for guidance.", e.Alias, e.ExistingID, e.NewID)
	case "cache_corruption":
		return fmt.Sprintf("cache: integrity check failed at %s. Recover from backup or re-ingest with gitcode-mcp sync --full.", e.Endpoint)
	case "remote_not_found":
		return fmt.Sprintf("sync: remote record for alias %s not found. It may have been deleted or moved. Run link-check to find affected references.", e.Alias)
	case "payload_too_large":
		return fmt.Sprintf("sync: record %s exceeds maximum size %d bytes. Use --max-size to increase limit or skip with --skip-large.", e.Target, e.LimitBytes)
	case "conflict":
		return fmt.Sprintf("sync: conflict for record %s. Resolve local and remote payloads manually.", e.Target)
	default:
		if e.Cause != nil {
			return e.Cause.Error()
		}
		return "sync: failed"
	}
}

func (e ErrSyncFailure) Unwrap() error { return e.Cause }

func (e ErrSyncFailure) As(target any) bool {
	switch t := target.(type) {
	case *gitcode.ErrNetworkUnavailable:
		var v gitcode.ErrNetworkUnavailable
		if errors.As(e.Cause, &v) {
			*t = v
			return true
		}
	case *gitcode.ErrRateLimited:
		var v gitcode.ErrRateLimited
		if errors.As(e.Cause, &v) {
			*t = v
			return true
		}
	case *gitcode.ErrAuthExpired:
		var v gitcode.ErrAuthExpired
		if errors.As(e.Cause, &v) {
			*t = v
			return true
		}
	case *gitcode.ErrPartialResponse:
		var v gitcode.ErrPartialResponse
		if errors.As(e.Cause, &v) {
			*t = v
			return true
		}
	case *gitcode.ErrRemoteCollision:
		var v gitcode.ErrRemoteCollision
		if errors.As(e.Cause, &v) {
			*t = v
			return true
		}
	case *gitcode.ErrRemoteNotFound:
		var v gitcode.ErrRemoteNotFound
		if errors.As(e.Cause, &v) {
			*t = v
			return true
		}
	case *gitcode.ErrPayloadTooLarge:
		var v gitcode.ErrPayloadTooLarge
		if errors.As(e.Cause, &v) {
			*t = v
			return true
		}
	case *gitcode.ErrConflict:
		var v gitcode.ErrConflict
		if errors.As(e.Cause, &v) {
			*t = v
			return true
		}
	case *cache.ErrCacheCorruption:
		var v cache.ErrCacheCorruption
		if errors.As(e.Cause, &v) {
			*t = v
			return true
		}
	}
	return false
}

type ErrNotFound struct {
	Kind string
	ID   string
}

func (e ErrNotFound) Error() string {
	return fmt.Sprintf("service: %s %q not found", e.Kind, e.ID)
}

type ErrSnapshotConsistency struct {
	RepoID      string
	SnapshotID  string
	Expectation string
}

func (e ErrSnapshotConsistency) Error() string {
	return fmt.Sprintf("service: snapshot %q consistency error: %s", e.SnapshotID, e.Expectation)
}

type ErrCacheEmpty struct {
	Message string
}

func (e ErrCacheEmpty) Error() string {
	if e.Message == "" {
		return "service: cache is empty"
	}
	return "service: " + e.Message
}

type ErrInvalidQuery struct {
	Field   string
	Message string
}

type ErrRepoRequired struct {
	Operation string
}

func (e ErrRepoRequired) Error() string {
	if e.Operation == "" {
		return "service: repo_required: --repo is required"
	}
	return "service: repo_required: --repo is required for " + e.Operation
}

func (e ErrRepoRequired) DiagnosticCode() string { return "repo_required" }

type ErrAmbiguousAlias struct {
	Alias string
	Repos []string
}

func (e ErrAmbiguousAlias) Error() string {
	return fmt.Sprintf("service: ambiguous_alias %s is present in repositories %s", e.Alias, strings.Join(e.Repos, ","))
}

type ErrConflict struct {
	Kind    string
	ID      string
	Message string
}

func (e ErrConflict) Error() string {
	if e.Message != "" {
		return "service: conflict " + e.Kind + " " + e.ID + ": " + e.Message
	}
	return "service: conflict " + e.Kind + " " + e.ID
}

func (e ErrInvalidQuery) Error() string {
	if e.Field == "" {
		return "service: invalid query: " + e.Message
	}
	return "service: invalid query " + e.Field + ": " + e.Message
}

func (e ErrInvalidQuery) DiagnosticCode() string {
	if e.Field == "api_base_url" {
		return "invalid_api_base_url"
	}
	return "invalid_query"
}

type ErrRangeClamped struct {
	RequestedStart int
	RequestedEnd   int
	ActualStart    int
	ActualEnd      int
}

func (e ErrRangeClamped) Error() string {
	return fmt.Sprintf("service: range clamped from %d-%d to %d-%d", e.RequestedStart, e.RequestedEnd, e.ActualStart, e.ActualEnd)
}

type ErrStaleIndex struct {
	StaleCount int
}

func (e ErrStaleIndex) Error() string {
	return fmt.Sprintf("service: stale index contains %d item(s)", e.StaleCount)
}

type ErrLinkCheckFailed struct {
	BrokenCount int
}

func (e ErrLinkCheckFailed) Error() string {
	return fmt.Sprintf("service: link check found %d broken link(s)", e.BrokenCount)
}

type ErrWriteFailure struct {
	Code           string
	RepoID         string
	RemoteID       string
	IdempotencyKey string
	Cause          error
}

func (e ErrWriteFailure) Error() string {
	msg := "write: " + e.Code
	if e.RepoID != "" {
		msg += " repo_id=" + e.RepoID
	}
	if e.RemoteID != "" {
		msg += " remote_id=" + e.RemoteID
	}
	if e.IdempotencyKey != "" {
		msg += " idempotency_key=" + e.IdempotencyKey
	}
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return msg
}

func (e ErrWriteFailure) Unwrap() error { return e.Cause }

func (e ErrWriteFailure) DiagnosticCode() string { return e.Code }

type ErrSyncInProgress struct {
	EventID        string
	IdempotencyKey string
}

func (e ErrSyncInProgress) Error() string {
	return fmt.Sprintf("sync: idempotency key %s is already in progress as event %s", e.IdempotencyKey, e.EventID)
}

type ErrSyncStagingLimit struct {
	Limit int64
	Size  int64
}

func (e ErrSyncStagingLimit) Error() string {
	return fmt.Sprintf("sync: staged remote data exceeds maximum size %d bytes", e.Limit)
}

type ErrSyncNoRemoteAlias struct {
	Target string
}

func (e ErrSyncNoRemoteAlias) Error() string {
	return fmt.Sprintf("sync: no remote alias available for %s", e.Target)
}

func IsNotFound(err error) bool {
	var target ErrNotFound
	return errors.As(err, &target)
}

func IsCacheEmpty(err error) bool {
	var target ErrCacheEmpty
	return errors.As(err, &target)
}

func normalizeError(err error, kind, id string) error {
	if err == nil {
		return nil
	}
	if isCacheNotFound(err) {
		return ErrNotFound{Kind: kind, ID: id}
	}
	return err
}

func isCacheNotFound(err error) bool {
	return errors.Is(err, cache.ErrNotFound)
}
