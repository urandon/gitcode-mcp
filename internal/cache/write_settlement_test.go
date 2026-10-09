package cache

import (
	"context"
	"testing"
	"time"
)

func TestWriteGraphSettlementFencesReceiptAndRollsBack(t *testing.T) {
	ctx := context.Background()
	for _, scenario := range []string{"metadata-fence", "rollback", "settled-fence"} {
		t.Run(scenario, func(t *testing.T) {
			store := newTestStore(t, ctx)
			defer store.Close()
			now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
			pending := AuditTrailEntry{RepoID: "fixture-a", ID: "write-wiki-key", RecordID: "WIKI-HOME.MD", RemoteType: "wiki", RemoteID: "Home.md", IdempotencyKey: "wiki-key", PayloadHash: "intent", Status: "remote_confirmed_cache_refresh_pending", RequestMetadata: map[string]string{"wiki_revision": "old"}, CreatedAt: now}
			if err := store.RecordAuditEvent(ctx, pending); err != nil {
				t.Fatal(err)
			}
			complete := pending
			complete.Status = "succeeded"
			graph := RecordGraph{Record: Record{RepoID: "fixture-a", ID: "WIKI-HOME.MD", Type: "wiki", Path: "wiki/Home.md", Body: "wanted", RemoteRevision: "old", CreatedAt: now, UpdatedAt: now}}
			if scenario == "metadata-fence" {
				newer := pending
				newer.RequestMetadata = map[string]string{"wiki_revision": "new"}
				if err := store.RecordAuditEvent(ctx, newer); err != nil {
					t.Fatal(err)
				}
			} else if scenario == "rollback" {
				graph.Links = []Link{{RepoID: "fixture-a", SourceID: "WIKI-HOME.MD", TargetID: "MISSING", Kind: "references"}}
			} else {
				settled, err := store.SettleWriteGraphGeneration(ctx, graph, complete, pending, now)
				if err != nil || !settled {
					t.Fatalf("initial settlement %t %v", settled, err)
				}
				graph.Record.Body = "stale caller"
			}
			settled, err := store.SettleWriteGraphGeneration(ctx, graph, complete, pending, now)
			if settled || (scenario == "rollback" && err == nil) || (scenario != "rollback" && err != nil) {
				t.Fatalf("unfenced settlement: %t %v", settled, err)
			}
			entry, auditErr := store.GetAuditEventByKey(ctx, "fixture-a", "wiki-key")
			if auditErr != nil || entry == nil {
				t.Fatal("missing receipt")
			}
			record, recordErr := store.GetRecord(ctx, "fixture-a", "WIKI-HOME.MD")
			if scenario == "settled-fence" {
				if entry.Status != "succeeded" || recordErr != nil || record.Body != "wanted" {
					t.Fatal("late cache publication changed settled state")
				}
			} else if entry.Status != pending.Status || recordErr == nil {
				t.Fatal("transaction left a partial publication")
			}
		})
	}
}

func TestWriteGraphStagingFencesObservedReceipt(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t, ctx)
	defer store.Close()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	observed := AuditTrailEntry{RepoID: "fixture-a", ID: "write-key", IdempotencyKey: "key", PayloadHash: "intent", Status: "remote_confirmed_cache_refresh_pending", RequestMetadata: map[string]string{"wiki_revision": "old"}, CreatedAt: now}
	if err := store.RecordAuditEvent(ctx, observed); err != nil {
		t.Fatal(err)
	}
	newer := observed
	newer.RequestMetadata = map[string]string{"wiki_revision": "newer"}
	staged, err := store.StageWriteGraphGeneration(ctx, newer, observed, now)
	if err != nil || !staged {
		t.Fatalf("new staging failed: %t %v", staged, err)
	}
	staged, err = store.StageWriteGraphGeneration(ctx, observed, observed, now)
	if err != nil || staged {
		t.Fatalf("stale same-generation staging accepted: %t %v", staged, err)
	}
	entry, err := store.GetAuditEventByKey(ctx, "fixture-a", "key")
	if err != nil || entry == nil || entry.RequestMetadata["wiki_revision"] != "newer" {
		t.Fatalf("new stage was overwritten: %+v %v", entry, err)
	}
}
