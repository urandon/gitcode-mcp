package audit

import (
	"context"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
)

type memoryStore struct {
	entry *cache.AuditTrailEntry
}

func (s *memoryStore) RecordAuditEvent(context.Context, cache.AuditTrailEntry) error { return nil }

func (s *memoryStore) GetAuditEventByKey(context.Context, string, string) (*cache.AuditTrailEntry, error) {
	return s.entry, nil
}

func TestLookupIdempotencyClassifiesReplayConflictRetryAndPartial(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC)

	store := &memoryStore{entry: &cache.AuditTrailEntry{Status: StatusSucceeded, PayloadHash: "hash", CreatedAt: now}}
	lookup, err := LookupIdempotency(ctx, store, "repo", "key", "hash")
	if err != nil || !lookup.Replay || lookup.Conflict || lookup.Retry || lookup.Partial {
		t.Fatalf("succeeded lookup=%#v err=%v", lookup, err)
	}

	lookup, err = LookupIdempotency(ctx, store, "repo", "key", "different")
	if err != nil || !lookup.Conflict || lookup.Replay {
		t.Fatalf("conflict lookup=%#v err=%v", lookup, err)
	}

	store.entry = &cache.AuditTrailEntry{Status: StatusFailed, PayloadHash: "hash", CreatedAt: now}
	lookup, err = LookupIdempotency(ctx, store, "repo", "key", "hash")
	if err != nil || !lookup.Retry || lookup.Replay || lookup.Conflict {
		t.Fatalf("failed lookup=%#v err=%v", lookup, err)
	}

	store.entry = &cache.AuditTrailEntry{Status: StatusRemoteConfirmedCacheRefreshFailed, PayloadHash: "hash", CreatedAt: now}
	lookup, err = LookupIdempotency(ctx, store, "repo", "key", "hash")
	if err != nil || !lookup.Partial || lookup.Replay || lookup.Conflict {
		t.Fatalf("partial lookup=%#v err=%v", lookup, err)
	}
}

func TestEntryHelpersAvoidRawPayloadStorage(t *testing.T) {
	now := time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC)
	entry := Success("repo", "key", "create-issue", "ISSUE-1", "issue", "1", "payload-hash", "created", now)
	if entry.ID != "write-key" || entry.Status != StatusSucceeded || entry.PayloadHash != "payload-hash" || entry.Message != "created" || entry.CreatedAt != now {
		t.Fatalf("entry=%#v", entry)
	}
	failure := Failure("repo", "key", "create-issue", "payload-hash", "write_network_unavailable", now)
	if failure.RemoteID != "" || failure.RecordID != "" || failure.Status != StatusFailed || failure.Message != "write_network_unavailable" {
		t.Fatalf("failure=%#v", failure)
	}
}
