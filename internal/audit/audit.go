package audit

import (
	"context"
	"errors"
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

var ErrInvalidConfirmation = errors.New("audit: invalid live confirmation")

type ConfirmationInput struct {
	RepoID          string
	Key             string
	Command         string
	Mode            string
	RecordID        string
	RemoteType      string
	RemoteID        string
	PayloadHash     string
	Message         string
	RequestMetadata map[string]string
	CreatedAt       time.Time
}

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

func LiveCreateIssueConfirmation(input ConfirmationInput) (cache.AuditTrailEntry, error) {
	input.Command = normalizeCommand(input.Command)
	input.Mode = strings.TrimSpace(input.Mode)
	input.Key = strings.TrimSpace(input.Key)
	input.RemoteID = strings.TrimSpace(input.RemoteID)
	if input.Command != "create-issue" || input.Mode != "live" || input.Key == "" || input.RemoteID == "" {
		return cache.AuditTrailEntry{}, ErrInvalidConfirmation
	}
	confirmation := entry(input.RepoID, input.Key, input.Command, input.RecordID, input.RemoteType, input.RemoteID, StatusSucceeded, input.Message, input.PayloadHash, input.CreatedAt)
	confirmation.Command = input.Command
	confirmation.Mode = input.Mode
	confirmation.RequestMetadata = sanitizedMetadata(input.RequestMetadata)
	return confirmation, nil
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
	operation = normalizeCommand(operation)
	return cache.AuditTrailEntry{RepoID: repoID, ID: EntryID(key), Operation: operation, Command: operation, RecordID: recordID, RemoteType: remoteType, RemoteID: remoteID, IdempotencyKey: strings.TrimSpace(key), Status: status, Message: message, PayloadHash: payloadHash, CreatedAt: createdAt}
}

func normalizeCommand(command string) string {
	return strings.ToLower(strings.TrimSpace(command))
}

func sanitizedMetadata(metadata map[string]string) map[string]string {
	allowed := map[string]bool{
		"method":             true,
		"request_id":         true,
		"idempotency_key":    true,
		"remote_alias":       true,
		"remote_number":      true,
		"remote_type":        true,
		"provider":           true,
		"provider_mode":      true,
		"response_status":    true,
		"source_fingerprint": true,
	}
	out := map[string]string{}
	for key, value := range metadata {
		normalized := strings.ToLower(strings.TrimSpace(key))
		if !allowed[normalized] || strings.TrimSpace(value) == "" {
			continue
		}
		out[normalized] = strings.TrimSpace(value)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
