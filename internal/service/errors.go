package service

import (
	"errors"
	"fmt"

	"gitcode-mcp/internal/cache"
)

type ErrNotFound struct {
	Kind string
	ID   string
}

func (e ErrNotFound) Error() string {
	return fmt.Sprintf("service: %s %q not found", e.Kind, e.ID)
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

func (e ErrInvalidQuery) Error() string {
	if e.Field == "" {
		return "service: invalid query: " + e.Message
	}
	return "service: invalid query " + e.Field + ": " + e.Message
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
