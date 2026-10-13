package localauthority

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitcode-mcp/internal/authority"
)

func TestSnapshotSequenceFenceAndContentIdentity(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		createIssue(t, s, fmt.Sprintf("snapshot-create-%d", i))
	}
	all, err := s.Snapshot(ctx, SnapshotRequest{ProjectID: testProject, Limit: 100})
	if err != nil || len(all.Entities) != 3 || all.Sequence != 3 || all.NextCursor != "" {
		t.Fatal("initial snapshot failed")
	}
	first, err := s.Snapshot(ctx, SnapshotRequest{ProjectID: testProject, Limit: 1})
	if err != nil || len(first.Entities) != 1 || first.NextCursor == "" {
		t.Fatal("bounded snapshot did not preserve continuation")
	}
	old := all.Entities[1]
	var payload authority.IssuePayload
	if authority.DecodeJSON(old.Entity.Payload, &payload) != nil {
		t.Fatal("fixture payload failed")
	}
	prior := authority.MutationReceipt{Citation: old.Entity.Citation}
	changed, err := s.Mutate(ctx, request(t, "issue.update", "snapshot-noop", payload, prior), Actor{ID: "synthetic-other-actor"})
	if err != nil {
		t.Fatal("metadata-only content fixture failed")
	}
	current, err := s.Snapshot(ctx, SnapshotRequest{ProjectID: testProject, Limit: 100})
	if err != nil || current.Sequence != 4 || current.Entities[1].Entity.Citation != changed.Citation || current.Entities[1].ContentDigest != old.ContentDigest {
		t.Fatal("no-op content changed digest or hid new citation")
	}
	payload.Body = "Changed content"
	mustMutate(t, s, request(t, "issue.update", "snapshot-edit", payload, changed))
	createIssue(t, s, "snapshot-later-create")
	last, err := s.Snapshot(ctx, SnapshotRequest{ProjectID: testProject, Limit: 100, Cursor: first.NextCursor})
	if err != nil || last.Sequence != 3 || len(last.Entities) != 2 || last.Entities[0].Entity.Citation != old.Entity.Citation || last.Entities[0].ContentDigest != old.ContentDigest {
		t.Fatal("later write contaminated immutable continuation")
	}
	current, err = s.Snapshot(ctx, SnapshotRequest{ProjectID: testProject, Limit: 100})
	if err != nil {
		t.Fatal("updated snapshot failed")
	}
	found := false
	for _, entry := range current.Entities {
		if entry.Entity.Citation.EntityID == old.Entity.Citation.EntityID {
			found = entry.ContentDigest != old.ContentDigest
		}
	}
	if !found {
		t.Fatal("content update failed to invalidate identity")
	}
}

func TestSnapshotByteBoundsAndInvalidRequests(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		mustMutate(t, s, request(t, "issue.create", fmt.Sprintf("snapshot-large-%d", i), authority.IssuePayload{Title: "Example", Body: strings.Repeat("x", 128<<10), State: "open"}))
	}
	req := SnapshotRequest{ProjectID: testProject, Limit: 100}
	seen := map[authority.UUID]bool{}
	for {
		page, err := s.Snapshot(ctx, req)
		if err != nil || len(page.Entities) == 0 {
			t.Fatal("byte-bounded page lost progress")
		}
		data, _ := json.Marshal(page)
		if len(data) > authority.MaxContractBytes {
			t.Fatal("snapshot exceeded encoded byte bound")
		}
		for _, entry := range page.Entities {
			if seen[entry.Entity.Citation.EntityID] {
				t.Fatal("snapshot repeated an entity")
			}
			seen[entry.Entity.Citation.EntityID] = true
		}
		if page.NextCursor == "" {
			break
		}
		req.Cursor = page.NextCursor
	}
	if len(seen) != 5 {
		t.Fatal("snapshot dropped trailing entities")
	}
	for _, req := range []SnapshotRequest{{ProjectID: testProject}, {ProjectID: testProject, Limit: 101}, {ProjectID: testProject, Limit: 1, Cursor: "not-a-cursor"}, {Limit: 1}} {
		_, err := s.Snapshot(ctx, req)
		var typed authority.Error
		if !errors.As(err, &typed) || typed.Code != authority.InvalidContract {
			t.Fatal("invalid snapshot request was not refused")
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Snapshot(cancelled, SnapshotRequest{ProjectID: testProject, Limit: 1}); !errors.Is(err, context.Canceled) {
		t.Fatal("snapshot did not preserve cancellation")
	}
}

func TestSnapshotGitBytesAndTombstonesRemainAuthorityOwned(t *testing.T) {
	s, _, _ := readyDocumentStore(t)
	ctx := context.Background()
	const markdown = "# Synthetic snapshot Git-only sentinel\n"
	created, err := s.MutateDocument(ctx, documentRequest(t, "document.create", "snapshot-doc-create", authority.EmptyKnowledgeRef, authority.Citation{}, markdown), testActor)
	if err != nil {
		t.Fatal("document fixture create failed")
	}
	page, err := s.Snapshot(ctx, SnapshotRequest{ProjectID: testProject, Limit: 100})
	if err != nil || len(page.Entities) != 1 || page.Entities[0].Entity.Citation != created.Receipt.Citation {
		t.Fatal("document snapshot did not verify exact citation")
	}
	var payload authority.DocumentPayload
	if authority.DecodeJSON(page.Entities[0].Entity.Payload, &payload) != nil || payload.Markdown != markdown {
		t.Fatal("document snapshot did not hydrate Git bytes")
	}
	oldDigest := page.Entities[0].ContentDigest
	_, err = s.MutateDocument(ctx, documentRequest(t, "document.tombstone", "snapshot-doc-remove", created.Result.Locator.CommitOID, created.Receipt.Citation, ""), testActor)
	if err != nil {
		t.Fatal("document tombstone failed")
	}
	page, err = s.Snapshot(ctx, SnapshotRequest{ProjectID: testProject, Limit: 100})
	if err != nil || len(page.Entities) != 1 || !page.Entities[0].Entity.Tombstone || page.Entities[0].ContentDigest == oldDigest {
		t.Fatal("snapshot hid tombstone invalidation")
	}
	for _, filename := range []string{"tracker.db", "tracker.db-wal"} {
		data, err := os.ReadFile(filepath.Join(s.directory, filename))
		if os.IsNotExist(err) && filename == "tracker.db-wal" {
			continue
		}
		if err != nil || bytes.Contains(data, []byte(markdown)) {
			t.Fatal("snapshot copied document bytes into SQLite")
		}
	}
}
