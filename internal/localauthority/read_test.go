package localauthority

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/cache"
)

func TestByteBoundedPagesPreserveSnapshotAndProgress(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	original := []authority.MutationReceipt{}
	for i := 0; i < 5; i++ {
		original = append(original, mustMutate(t, s, request(t, "issue.create", fmt.Sprintf("synthetic-page-%d", i), authority.IssuePayload{Title: "Fixture", Body: strings.Repeat("x", 128<<10), State: "open"})))
	}
	req := authority.ReadRequest{ProjectID: testProject, Kind: authority.Issue, Operation: "list", Limit: 100}
	page, err := s.Read(ctx, req)
	if err != nil || len(page.Entities) != 3 || page.NextCursor == "" {
		t.Fatal("byte budget did not produce progress-safe continuation")
	}
	if page.Match(req) != nil {
		t.Fatal("storage byte-bounded page violates shared cursor contract")
	}
	data, _ := json.Marshal(page)
	if len(data) > authority.MaxContractBytes {
		t.Fatal("encoded reply exceeds contract")
	}
	mustMutate(t, s, request(t, "issue.update", "synthetic-page-update", authority.IssuePayload{Title: "Later changed", Body: "later body", State: "closed"}, original[3]))
	createIssue(t, s, "synthetic-page-later-create")
	req.Cursor = page.NextCursor
	next, err := s.Read(ctx, req)
	if err != nil || len(next.Entities) != 2 || next.NextCursor != "" || next.Entities[0].Citation.Revision != 1 {
		t.Fatal("continuation lost immutable snapshot or skipped trailing entities")
	}
	if next.Match(req) != nil {
		t.Fatal("storage terminal page violates shared snapshot contract")
	}
	wrong := req
	wrong.Kind = authority.Label
	_, err = s.Read(ctx, wrong)
	var typed authority.Error
	if !errors.As(err, &typed) || typed.Code != authority.InvalidContract {
		t.Fatal("cursor scope mismatch accepted")
	}
	history := authority.ReadRequest{ProjectID: testProject, Kind: authority.Issue, EntityID: original[3].Citation.EntityID, Operation: "history", Limit: 1}
	first, err := s.Read(ctx, history)
	if err != nil || len(first.Entities) != 1 || first.NextCursor == "" {
		t.Fatal("history continuation missing")
	}
	if first.Match(history) != nil {
		t.Fatal("storage history cursor violates shared contract")
	}
	history.Cursor = first.NextCursor
	last, err := s.Read(ctx, history)
	if err != nil || len(last.Entities) != 1 || last.Entities[0].Citation.Revision != 2 {
		t.Fatal("history continuation incorrect")
	}
	if last.Match(history) != nil {
		t.Fatal("storage terminal history violates shared contract")
	}
}

func TestAuditIsContentFreeAndProjectionResetCannotDeleteAuthority(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	key := "synthetic-private-key-sentinel"
	actor := Actor{ID: "synthetic-private-actor-sentinel"}
	r, err := s.Mutate(ctx, request(t, "issue.create", key, authority.IssuePayload{Title: "synthetic-private-title-sentinel", Body: "synthetic-private-body-sentinel", State: "open"}), actor)
	if err != nil {
		t.Fatal("fixture mutation failed")
	}
	entries, err := s.Audit(ctx, 0, 100)
	if err != nil || len(entries) != 1 {
		t.Fatal("confirmed audit missing")
	}
	data, _ := json.Marshal(entries)
	for _, sentinel := range []string{key, actor.ID, "synthetic-private-title-sentinel", "synthetic-private-body-sentinel", s.directory} {
		if strings.Contains(string(data), sentinel) {
			t.Fatal("private content escaped into audit")
		}
	}
	projectionDir := privateTestDirectory(t)
	projectionPath := filepath.Join(projectionDir, "cache.db")
	projection, err := cache.NewSQLiteStore(ctx, projectionPath)
	if err != nil {
		t.Fatal("projection fixture creation failed")
	}
	if err = projection.AddRepository(ctx, cache.RepositoryBinding{RepoID: "synthetic-repo", Owner: "example-owner", Name: "example-repo"}); err != nil {
		t.Fatal("projection fixture binding failed")
	}
	if err = projection.UpsertRecordGraph(ctx, cache.RecordGraph{Record: cache.Record{RepoID: "synthetic-repo", ID: "ISSUE-1", Type: "issue", Title: "synthetic projection", Body: "derived synthetic body", Path: "issues/1.md", Status: "open", ContentHash: "synthetic", Provenance: cache.ProvenanceRemote, RemoteType: "issue", RemoteID: "1", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}}); err != nil {
		t.Fatal("projection fixture fill failed")
	}
	if err = projection.ResetLive(ctx, "synthetic-repo"); err != nil {
		t.Fatal("projection reset failed")
	}
	_ = projection.Close()
	if err = os.WriteFile(projectionPath, []byte("synthetic broken disposable projection"), 0600); err != nil {
		t.Fatal("projection corruption fixture failed")
	}
	view, err := s.Read(ctx, authority.ReadRequest{ProjectID: testProject, Kind: authority.Issue, EntityID: r.Citation.EntityID, Operation: "get"})
	if err != nil || len(view.Entities) != 1 {
		t.Fatal("projection failure deleted or blocked authority")
	}
	stored, err := s.Receipt(ctx, key)
	if err != nil || stored.OperationID != r.OperationID {
		t.Fatal("projection failure erased receipt")
	}
}

func TestNewerSchemaAndPhysicalReplacementFailClosed(t *testing.T) {
	s, registry, dir := testStore(t)
	if _, err := s.db.Exec("PRAGMA user_version=999"); err != nil {
		t.Fatal("future schema fixture failed")
	}
	_ = s.Close()
	path := filepath.Join(dir, "tracker.db")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("fixture read failed")
	}
	_, err = OpenWriter(context.Background(), registry, dir, testProject)
	var typed authority.Error
	if !errors.As(err, &typed) || typed.Code != authority.Unsupported {
		t.Fatal("future schema gained writer")
	}
	after, err := os.ReadFile(path)
	if err != nil || hash(before) != hash(after) {
		t.Fatal("future schema was migrated or rewritten")
	}
	// A same-path copied instance is not an implicit verified restore.
	other, otherRegistry, otherDir := testStore(t)
	_ = other.Close()
	otherPath := filepath.Join(otherDir, "tracker.db")
	body, err := os.ReadFile(otherPath)
	if err != nil {
		t.Fatal("replacement fixture read failed")
	}
	copyPath := filepath.Join(otherDir, "replacement.db")
	if os.WriteFile(copyPath, body, 0600) != nil || os.Rename(copyPath, otherPath) != nil {
		t.Fatal("replacement fixture publication failed")
	}
	_, err = OpenWriter(context.Background(), otherRegistry, otherDir, testProject)
	if !errors.As(err, &typed) || typed.Code != authority.Conflict {
		t.Fatal("physical replacement silently became authority")
	}
}
