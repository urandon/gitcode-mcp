package audit

import (
	"context"
	"strings"
	"time"

	"gitcode-mcp/internal/cache"
)

const (
	StatusSucceeded                         = "succeeded"
	StatusFailed                            = "failed"
	StatusRemoteConfirmedCacheRefreshFailed = "remote_confirmed_cache_refresh_failed"
	StatusRemoteConfirmedAuditFailed        = "remote_confirmed_audit_failed"
)

type Store interface {
	RecordAuditEvent(context.Context, cache.AuditTrailEntry) error
	GetAuditEventByKey(context.Context, string, string) (*cache.AuditTrailEntry, error)
}

type Lookup struct {
	Entry    *cache.AuditTrailEntry
	Conflict bool
	Replay   bool
	Retry    bool
	Partial  bool
}

func EntryID(key string) string {
	return "write-" + strings.TrimSpace(key)
}

func LookupIdempotency(ctx context.Context, store Store, repoID, key, payloadHash string) (Lookup, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return Lookup{}, nil
	}
	entry, err := store.GetAuditEventByKey(ctx, repoID, key)
	if err != nil || entry == nil {
		return Lookup{Entry: entry}, err
	}
	lookup := Lookup{Entry: entry}
	if entry.PayloadHash != "" && entry.PayloadHash != payloadHash {
		lookup.Conflict = true
		return lookup, nil
	}
	switch entry.Status {
	case StatusSucceeded:
		lookup.Replay = true
	case StatusRemoteConfirmedCacheRefreshFailed:
		lookup.Partial = true
	case StatusFailed:
		lookup.Retry = true
	}
	return lookup, nil
}

func Success(repoID, key, operation, recordID, remoteType, remoteID, payloadHash, message string, createdAt time.Time) cache.AuditTrailEntry {
	return entry(repoID, key, operation, recordID, remoteType, remoteID, StatusSucceeded, message, payloadHash, createdAt)
}

func Failure(repoID, key, operation, payloadHash, message string, createdAt time.Time) cache.AuditTrailEntry {
	return entry(repoID, key, operation, "", "", "", StatusFailed, message, payloadHash, createdAt)
}

func RemoteConfirmedCacheRefreshFailed(repoID, key, operation, recordID, remoteType, remoteID, payloadHash, message string, createdAt time.Time) cache.AuditTrailEntry {
	return entry(repoID, key, operation, recordID, remoteType, remoteID, StatusRemoteConfirmedCacheRefreshFailed, message, payloadHash, createdAt)
}

func RemoteConfirmedAuditFailed(repoID, key, operation, recordID, remoteType, remoteID, payloadHash, message string, createdAt time.Time) cache.AuditTrailEntry {
	return entry(repoID, key, operation, recordID, remoteType, remoteID, StatusRemoteConfirmedAuditFailed, message, payloadHash, createdAt)
}

func entry(repoID, key, operation, recordID, remoteType, remoteID, status, message, payloadHash string, createdAt time.Time) cache.AuditTrailEntry {
	return cache.AuditTrailEntry{RepoID: repoID, ID: EntryID(key), Operation: operation, RecordID: recordID, RemoteType: remoteType, RemoteID: remoteID, IdempotencyKey: strings.TrimSpace(key), Status: status, Message: message, PayloadHash: payloadHash, CreatedAt: createdAt}
}
