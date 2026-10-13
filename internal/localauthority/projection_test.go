package localauthority

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gitcode-mcp/internal/authority"
)

func projectionFixture(t *testing.T) (*projectionIndex, *Store, string) {
	t.Helper()
	prepared, _, dir := preparedFixture(t)
	s, err := prepared.OpenWriter(context.Background(), dir, testProject)
	if err != nil {
		t.Fatal("projection prepared writer fixture unavailable")
	}
	t.Cleanup(func() { _ = s.Close() })
	p, err := openProjection(context.Background(), s)
	if err != nil {
		t.Fatal("projection owner fixture unavailable")
	}
	t.Cleanup(func() { _ = p.close() })
	return p, s, dir
}

func projectionCandidate(t *testing.T, p *projectionIndex, sequence uint64) *projectionBuilder {
	t.Helper()
	b, err := p.newBuilder(context.Background(), sequence)
	if err != nil {
		t.Fatal("projection candidate intake failed")
	}
	for !b.done {
		if n, err := b.step(context.Background(), 1); err != nil || n > 1 {
			t.Fatal("bounded projection candidate failed")
		}
	}
	return b
}

func publishProjectionFixture(t *testing.T, p *projectionIndex, sequence uint64) projectionCatalog {
	t.Helper()
	if err := p.publish(context.Background(), projectionCandidate(t, p, sequence)); err != nil {
		t.Fatal("projection publication failed")
	}
	c, err := p.view(context.Background())
	if err != nil || c == nil || !c.valid() {
		t.Fatal("projection metadata view unavailable")
	}
	return *c
}

func requireProjectionCode(t *testing.T, err error, code Code) {
	t.Helper()
	var typed Error
	if !errors.As(err, &typed) || typed.Code != code {
		t.Fatal("projection did not return the expected fixed diagnostic")
	}
}

func TestProjectionObservationDoesNotProvisionAndUnownedRefuses(t *testing.T) {
	p, s, dir := projectionFixture(t)
	ctx := context.Background()
	if c, err := p.view(ctx); err != nil || c != nil {
		t.Fatal("missing projection became a ready catalog")
	}
	if _, err := os.Lstat(filepath.Join(dir, projectionDirectory)); !os.IsNotExist(err) {
		t.Fatal("opening or reading a missing projection provisioned it")
	}
	ro, err := OpenReadOnly(ctx, dir, testProject)
	if err != nil {
		t.Fatal("canonical read-only fixture unavailable")
	}
	defer ro.Close()
	if index, err := openProjection(ctx, ro); err == nil {
		_ = index.close()
		t.Fatal("offline read-only authority acquired a projection writer")
	}
	b, err := p.newBuilder(ctx, 0)
	if err != nil {
		t.Fatal("empty projection intent failed")
	}
	if err = p.publish(ctx, b); err == nil {
		t.Fatal("incomplete projection published")
	}
	if _, err := os.Lstat(filepath.Join(dir, projectionDirectory)); !os.IsNotExist(err) {
		t.Fatal("incomplete candidate provisioned a namespace")
	}
	c := publishProjectionFixture(t, p, 0)
	if c.Generation != 1 || len(c.Entries) != 0 || c.Sequence != 0 {
		t.Fatal("empty origin catalog has false coverage")
	}
	before, err := s.Status(ctx)
	if err != nil || before.AuthorityGeneration != 0 {
		t.Fatal("derived publication changed source sequence")
	}
	if p.close() != nil {
		t.Fatal("projection owner did not close")
	}
	next, err := openProjection(ctx, s)
	if err != nil {
		t.Fatal("projection owner did not reopen")
	}
	defer next.close()
	loaded, err := next.view(ctx)
	if err != nil || loaded == nil || !reflect.DeepEqual(c, *loaded) {
		t.Fatal("reopen changed complete catalog identity")
	}
	loaded.Entries = append(loaded.Entries, projectionEntry{})
	clean, err := next.view(ctx)
	if err != nil || len(clean.Entries) != 0 {
		t.Fatal("metadata view granted mutable catalog authority")
	}
}

func TestProjectionContentReuseStaleMissingAndArchive(t *testing.T) {
	p, s, _ := projectionFixture(t)
	ctx := context.Background()
	payload := authority.IssuePayload{Title: "Synthetic catalog title", Body: "Synthetic catalog content", State: "open"}
	issue := mustMutate(t, s, request(t, "issue.create", "projection-first", payload))
	label := mustMutate(t, s, request(t, "label.create", "projection-label", authority.LabelPayload{Name: "synthetic-label", Color: "#112233", Archived: false}))
	first := publishProjectionFixture(t, p, 2)
	noop := mustMutate(t, s, request(t, "issue.update", "projection-noop", payload, issue))
	page, err := s.Snapshot(ctx, SnapshotRequest{ProjectID: testProject, Limit: 100})
	if err != nil {
		t.Fatal("projection comparison source unavailable")
	}
	comparison, err := compareProjection(first, page)
	if err != nil || comparison.Reusable != 2 || comparison.Stale != 0 || comparison.Missing != 0 || comparison.Partial {
		t.Fatal("metadata-only revision invalidated identical content")
	}
	second := publishProjectionFixture(t, p, 3)
	if second.Generation != 2 || second.Epoch != first.Epoch || second.BuildID == first.BuildID || second.Sequence != 3 {
		t.Fatal("new citation did not publish in the original catalog epoch")
	}
	found := false
	for _, e := range second.Entries {
		if e.Citation.EntityID == noop.Citation.EntityID {
			found = e.Citation == noop.Citation
		}
	}
	if !found {
		t.Fatal("content reuse retained an obsolete citation")
	}
	payload.Body = "Synthetic changed catalog content"
	mustMutate(t, s, request(t, "issue.update", "projection-edit", payload, noop))
	createIssue(t, s, "projection-new-entity")
	mustMutate(t, s, request(t, "label.update", "projection-archive", authority.LabelPayload{Name: "synthetic-label", Color: "#112233", Archived: true}, label))
	page, err = s.Snapshot(ctx, SnapshotRequest{ProjectID: testProject, Limit: 100})
	if err != nil {
		t.Fatal("updated projection comparison source unavailable")
	}
	comparison, err = compareProjection(second, page)
	if err != nil || comparison.Stale != 1 || comparison.Missing != 1 || comparison.Inactive != 1 || comparison.Reusable != 0 {
		t.Fatal("real content/new/archive changes did not invalidate active coverage")
	}
	third := publishProjectionFixture(t, p, 6)
	comparison, err = compareProjection(third, page)
	if err != nil || comparison.Reusable != 2 || comparison.Inactive != 1 || comparison.Stale != 0 || comparison.Missing != 0 {
		t.Fatal("repair did not publish actual updated source identity")
	}
	if _, err = p.newBuilder(ctx, 5); err == nil {
		t.Fatal("projection intake permitted generation rollback")
	}
}

func TestProjectionImmutableIntakeAndContendedGeneration(t *testing.T) {
	p, s, _ := projectionFixture(t)
	ctx := context.Background()
	for i := 0; i < 4; i++ {
		createIssue(t, s, fmt.Sprintf("projection-asof-%d", i))
	}
	b, err := p.newBuilder(ctx, 4)
	if err != nil {
		t.Fatal("old-sequence builder intake failed")
	}
	if _, err = b.step(ctx, 1); err != nil || b.done {
		t.Fatal("first bounded projection slice lost continuation")
	}
	before, err := s.Snapshot(ctx, SnapshotRequest{ProjectID: testProject, Limit: 100})
	if err != nil {
		t.Fatal("immutable control snapshot unavailable")
	}
	old := before.Entities[2]
	var payload authority.IssuePayload
	if authority.DecodeJSON(old.Entity.Payload, &payload) != nil {
		t.Fatal("old snapshot payload control unavailable")
	}
	payload.Body = "Changed after index job intake"
	mustMutate(t, s, request(t, "issue.update", "projection-asof-update", payload, authority.MutationReceipt{Citation: old.Entity.Citation}))
	createIssue(t, s, "projection-asof-later-create")
	for !b.done {
		if _, err = b.step(ctx, 1); err != nil {
			t.Fatal("immutable builder continuation failed")
		}
	}
	if len(b.catalog.Entries) != 4 || b.catalog.Sequence != 4 {
		t.Fatal("later-created entity entered the recorded snapshot")
	}
	for _, e := range b.catalog.Entries {
		if e.Citation.EntityID == old.Entity.Citation.EntityID && (e.Citation != old.Entity.Citation || e.Digest != old.ContentDigest) {
			t.Fatal("later edit entered the old projection generation")
		}
	}
	other := projectionCandidate(t, p, 6)
	if err = p.publish(ctx, other); err != nil {
		t.Fatal("newer complete candidate did not publish")
	}
	if err = p.publish(ctx, b); err == nil {
		t.Fatal("stale candidate overwrote a newer projection generation")
	}
	loaded, err := p.view(ctx)
	if err != nil || loaded.Sequence != 6 {
		t.Fatal("failed stale repair changed published metadata")
	}
}

func TestProjectionGitBytesStayCanonicalAcrossNoopAndTombstone(t *testing.T) {
	p, s, dir := projectionFixture(t)
	ctx := context.Background()
	if _, err := s.SetupKnowledge(ctx, testProject, "projection-knowledge-setup"); err != nil {
		t.Fatal("explicit Git fixture setup unavailable")
	}
	const sentinel = "# Synthetic projection Git-only-content sentinel\n"
	first, err := s.MutateDocument(ctx, documentRequest(t, "document.create", "projection-doc-first", authority.EmptyKnowledgeRef, authority.Citation{}, sentinel), testActor)
	if err != nil {
		t.Fatal("projection document control unavailable")
	}
	catalog := publishProjectionFixture(t, p, 1)
	noop, err := s.MutateDocument(ctx, documentRequest(t, "document.update", "projection-doc-noop", first.Result.Locator.CommitOID, first.Receipt.Citation, sentinel), testActor)
	if err != nil || noop.Result.Locator.CommitOID == first.Result.Locator.CommitOID {
		t.Fatal("document no-op revision did not retain distinct immutable provenance")
	}
	page, err := s.Snapshot(ctx, SnapshotRequest{ProjectID: testProject, Limit: 100})
	if err != nil {
		t.Fatal("document comparison unavailable")
	}
	c, err := compareProjection(catalog, page)
	if err != nil || c.Reusable != 1 || c.Stale != 0 {
		t.Fatal("new Git locator invalidated identical document content")
	}
	publishProjectionFixture(t, p, 2)
	removed, err := s.MutateDocument(ctx, documentRequest(t, "document.tombstone", "projection-doc-remove", noop.Result.Locator.CommitOID, noop.Receipt.Citation, ""), testActor)
	if err != nil {
		t.Fatal("document tombstone control failed")
	}
	last := publishProjectionFixture(t, p, 3)
	if len(last.Entries) != 1 || last.Entries[0].State != "tombstone" || last.Entries[0].Citation != removed.Receipt.Citation {
		t.Fatal("tombstone did not remove active projection identity")
	}
	historical, err := s.Read(ctx, authority.ReadRequest{ProjectID: testProject, Kind: authority.Document, EntityID: first.Receipt.Citation.EntityID, Operation: "get", Revision: 1})
	if err != nil || len(historical.Entities) != 1 || !bytes.Contains(historical.Entities[0].Payload, []byte("Git-only-content sentinel")) {
		t.Fatal("derived invalidation deleted historical Git bytes")
	}
	entries, err := os.ReadDir(filepath.Join(dir, projectionDirectory))
	if err != nil {
		t.Fatal("projection fixture inventory unavailable")
	}
	for _, e := range entries {
		body, err := os.ReadFile(filepath.Join(dir, projectionDirectory, e.Name()))
		if err != nil || bytes.Contains(body, []byte("Git-only-content sentinel")) || bytes.Contains(body, []byte("Example document")) || bytes.Contains(body, []byte("projection-doc")) {
			t.Fatal("projection persisted content/title/caller-key text")
		}
	}
}

func TestProjectionPublicationFaultsPreserveAuthorityAndLastCompleteCatalog(t *testing.T) {
	for _, phase := range []string{"projection_stage_write", "projection_stage_file", "projection_stage_directory", "projection_catalog_rename", "projection_catalog_directory", "projection_catalog_acknowledgement"} {
		t.Run(phase, func(t *testing.T) {
			p, s, _ := projectionFixture(t)
			ctx := context.Background()
			createIssue(t, s, "projection-fault-baseline")
			before := publishProjectionFixture(t, p, 1)
			created := createIssue(t, s, "projection-fault-new")
			b := projectionCandidate(t, p, 2)
			fail, cancel := context.WithCancel(ctx)
			defer cancel()
			p.fault = func(at string) error {
				if at == phase {
					cancel()
				}
				return nil
			}
			if err := p.publish(fail, b); !errors.Is(err, context.Canceled) {
				t.Fatal("publication did not preserve phase cancellation")
			}
			postRename := strings.HasPrefix(phase, "projection_catalog_")
			if postRename {
				_, err := p.view(ctx)
				requireProjectionCode(t, err, ProjectionUncertain)
			} else {
				old, err := p.view(ctx)
				if err != nil || old == nil || !reflect.DeepEqual(before, *old) {
					t.Fatal("failed candidate replaced prior complete catalog")
				}
			}
			read, err := s.Read(ctx, authority.ReadRequest{ProjectID: testProject, Kind: authority.Issue, EntityID: created.Citation.EntityID, Operation: "get"})
			status, e := s.Status(ctx)
			if err != nil || e != nil || len(read.Entities) != 1 || status.AuthorityGeneration != 2 || status.Readiness != "ready" {
				t.Fatal("projection fault fenced or changed canonical authority")
			}
			replay := createIssue(t, s, "projection-fault-new")
			if !replay.Replayed || replay.Citation != created.Citation {
				t.Fatal("projection fault changed original mutation receipt")
			}
			if p.close() != nil {
				t.Fatal("faulted projection owner did not close")
			}
			var barriers []string
			next, err := openProjectionWithBarrierFault(ctx, s, func(phase string) error {
				barriers = append(barriers, phase)
				return nil
			})
			if err != nil {
				t.Fatal("verified projection reopen failed")
			}
			defer next.close()
			loaded, err := next.view(ctx)
			generation := uint64(1)
			if postRename {
				generation = 2
			}
			if err != nil || loaded == nil || loaded.Generation != generation {
				t.Fatal("reopen guessed an unpublished stage instead of actual final metadata")
			}
			if !reflect.DeepEqual(barriers, []string{"projection_reopen_file", "projection_reopen_directory", "projection_reopen_root"}) {
				t.Fatal("reopen cleared uncertainty without all durability barriers")
			}
			if _, err := next.view(ctx); err != nil || len(barriers) != 3 {
				t.Fatal("ordinary metadata observation repeated writer admission barriers")
			}
		})
	}
}

func TestProjectionFailedNamespaceBarrierIsRetriedBeforeAcknowledgement(t *testing.T) {
	p, s, dir := projectionFixture(t)
	ctx := context.Background()
	created := createIssue(t, s, "projection-parent-barrier")
	b := projectionCandidate(t, p, 1)
	attempts := 0
	p.barrierFault = func(phase string) error {
		if phase == "projection_namespace_root" {
			attempts++
			if attempts == 1 {
				return Error{DurabilityUnavailable}
			}
		}
		return nil
	}
	requireProjectionCode(t, p.publish(ctx, b), DurabilityUnavailable)
	if p.directory == nil {
		t.Fatal("parent failure control did not retain the created namespace")
	}
	if _, err := os.Lstat(filepath.Join(dir, projectionDirectory, "catalog.json")); !os.IsNotExist(err) {
		t.Fatal("parent failure acknowledged an unqualified catalog")
	}
	if err := p.publish(ctx, b); err != nil || attempts != 2 {
		t.Fatal("publication retry skipped the failed namespace parent barrier")
	}
	if c, err := p.view(ctx); err != nil || c == nil || c.Generation != 1 {
		t.Fatal("qualified retry did not publish the complete generation")
	}
	status, err := s.Status(ctx)
	replay := createIssue(t, s, "projection-parent-barrier")
	if err != nil || status.Readiness != "ready" || status.AuthorityGeneration != 1 || !replay.Replayed || replay.Citation != created.Citation {
		t.Fatal("derived parent barrier failure changed canonical state or receipt")
	}
}

func TestProjectionReopenRequiresEveryBarrierAndPreservesCanonicalState(t *testing.T) {
	for _, phase := range []string{"projection_reopen_file", "projection_reopen_directory", "projection_reopen_root"} {
		for _, cancellation := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cancel-%t", phase, cancellation), func(t *testing.T) {
				p, s, dir := projectionFixture(t)
				ctx := context.Background()
				created := createIssue(t, s, "projection-reopen-barrier")
				publishProjectionFixture(t, p, 1)
				createIssue(t, s, "projection-reopen-new")
				p.fault = func(at string) error {
					if at == "projection_catalog_rename" {
						return context.Canceled
					}
					return nil
				}
				if err := p.publish(ctx, projectionCandidate(t, p, 2)); !errors.Is(err, context.Canceled) {
					t.Fatal("post-rename uncertainty fixture failed")
				}
				if p.close() != nil {
					t.Fatal("uncertain owner did not close")
				}
				final := filepath.Join(dir, projectionDirectory, "catalog.json")
				before, err := os.ReadFile(final)
				if err != nil {
					t.Fatal("retained metadata unavailable")
				}
				admission, cancel := context.WithCancel(ctx)
				defer cancel()
				seen := false
				index, err := openProjectionWithBarrierFault(admission, s, func(at string) error {
					if at == phase {
						seen = true
						if cancellation {
							cancel()
							return nil
						}
						return Error{DurabilityUnavailable}
					}
					return nil
				})
				if index != nil || !seen {
					t.Fatal("failed reopen admitted an unqualified projection owner")
				}
				if cancellation {
					if !errors.Is(err, context.Canceled) {
						t.Fatal("reopen barrier did not preserve cancellation")
					}
				} else {
					requireProjectionCode(t, err, DurabilityUnavailable)
				}
				after, err := os.ReadFile(final)
				status, e := s.Status(ctx)
				read, r := s.Read(ctx, authority.ReadRequest{ProjectID: testProject, Kind: authority.Issue, EntityID: created.Citation.EntityID, Operation: "get"})
				if err != nil || e != nil || r != nil || !bytes.Equal(before, after) || status.Readiness != "ready" || status.AuthorityGeneration != 2 || len(read.Entities) != 1 {
					t.Fatal("failed derived admission changed metadata bytes or canonical authority")
				}
				next, err := openProjection(ctx, s)
				if err != nil {
					t.Fatal("fully qualified reopen did not recover derived metadata")
				}
				defer next.close()
				if c, err := next.view(ctx); err != nil || c == nil || c.Generation != 2 {
					t.Fatal("qualified reopen did not retain the actual final generation")
				}
			})
		}
	}
}

func TestProjectionMissingFinalPreservesStagesAndCapacity(t *testing.T) {
	p, s, dir := projectionFixture(t)
	ctx := context.Background()
	createIssue(t, s, "projection-stage-capacity")
	b := projectionCandidate(t, p, 1)
	p.fault = func(phase string) error {
		if phase == "projection_stage_write" {
			return context.Canceled
		}
		return nil
	}
	for i := 0; i < MaxProjectionStages; i++ {
		if err := p.publish(ctx, b); !errors.Is(err, context.Canceled) {
			t.Fatal("bounded stage control failed")
		}
	}
	before, err := os.ReadDir(filepath.Join(dir, projectionDirectory))
	if err != nil || len(before) != MaxProjectionStages {
		t.Fatal("failed stage evidence was not retained")
	}
	p.fault = nil
	requireProjectionCode(t, p.publish(ctx, b), Capacity)
	if p.close() != nil {
		t.Fatal("stage owner did not close")
	}
	next, err := openProjection(ctx, s)
	if err != nil {
		t.Fatal("missing derived final falsely became source corruption")
	}
	defer next.close()
	if c, err := next.view(ctx); err != nil || c != nil {
		t.Fatal("orphan candidate became published authority")
	}
	after, err := os.ReadDir(filepath.Join(dir, projectionDirectory))
	if err != nil || len(after) != len(before) {
		t.Fatal("reopen silently cleaned uncertain candidates")
	}
}

func TestProjectionStoredBoundDoesNotRelaxTransportGrammar(t *testing.T) {
	p, _, _ := projectionFixture(t)
	b, err := p.newBuilder(context.Background(), 0)
	if err != nil {
		t.Fatal("catalog schema control unavailable")
	}
	catalog := b.catalog
	catalog.Sequence = MaxProjectionEntries
	for i := 1; i <= MaxProjectionEntries; i++ {
		id := authority.UUID(fmt.Sprintf("%08x-1111-4111-8111-%012x", i, i))
		catalog.Entries = append(catalog.Entries, projectionEntry{Citation: authority.Citation{ProjectID: testProject, EntityID: id, Kind: authority.Issue, Revision: 1}, Sequence: uint64(i), Digest: strings.Repeat("a", 64), State: "active"})
	}
	state, _ := json.Marshal(catalog)
	body, _ := json.Marshal(projectionEnvelope{catalog, hash(state)})
	if len(body) <= authority.MaxContractBytes || len(body) > MaxProjectionBytes {
		t.Fatal("schema fixture did not straddle storage versus transport bounds")
	}
	var decoded projectionEnvelope
	if authority.DecodeJSON(body, &decoded) == nil || authority.DecodeStoredJSON(body, &decoded, MaxProjectionBytes) != nil {
		t.Fatal("stored metadata bound weakened transport or rejected admitted capacity")
	}
	for _, limit := range []int{0, -1, 5 << 20, len(body) - 1} {
		if authority.DecodeStoredJSON(body, &decoded, limit) == nil {
			t.Fatal("invalid stored-byte bound accepted")
		}
	}
	for _, corrupt := range []string{`{"catalog":null,"digest":"x"}`, `{"catalog":{},"digest":"x","digest":"x"}`, `{"catalog":{},"digest":"x","payload":"synthetic-private-body"}`} {
		if authority.DecodeStoredJSON([]byte(corrupt), &decoded, MaxProjectionBytes) == nil {
			t.Fatal("stored grammar accepted null/duplicate/unknown catalog data")
		}
	}
}

func TestSnapshotExplicitAsOfRejectsFutureAndChangedCursorScope(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	createIssue(t, s, "snapshot-asof-first")
	createIssue(t, s, "snapshot-asof-second")
	zero := uint64(0)
	page, err := s.Snapshot(ctx, SnapshotRequest{ProjectID: testProject, Limit: 1, AsOf: &zero})
	if err != nil || page.Sequence != 0 || len(page.Entities) != 0 || page.NextCursor == "" || page.Scanned != 1 {
		t.Fatal("explicit empty-origin snapshot did not advance a sparse page")
	}
	two, future, oversized := uint64(2), uint64(3), uint64(math.MaxInt64)+1
	for _, request := range []SnapshotRequest{{ProjectID: testProject, Limit: 100, AsOf: &future}, {ProjectID: testProject, Limit: 100, AsOf: &oversized}, {ProjectID: testProject, Limit: 100, Cursor: page.NextCursor, AsOf: &two}} {
		if _, err := s.Snapshot(ctx, request); err == nil {
			t.Fatal("as-of intake accepted future or changed continuation scope")
		}
	}
	last, err := s.Snapshot(ctx, SnapshotRequest{ProjectID: testProject, Limit: 100, Cursor: page.NextCursor, AsOf: &zero})
	if err != nil || len(last.Entities) != 0 || last.NextCursor != "" || last.Sequence != 0 {
		t.Fatal("same empty-origin continuation failed")
	}
}

func TestProjectionFailedCapacityCandidateNeverPublishesPrefix(t *testing.T) {
	p, s, dir := projectionFixture(t)
	for i := 0; i < 3; i++ {
		createIssue(t, s, fmt.Sprintf("projection-candidate-cap-%d", i))
	}
	b, err := p.newBuilder(context.Background(), 3)
	if err != nil || b.maxEntries != MaxProjectionEntries {
		t.Fatal("production capacity intake is not the fixed advertised limit")
	}
	b.maxEntries = 2 // private deterministic limit; actual canonical source remains three
	_, err = b.step(context.Background(), 3)
	requireProjectionCode(t, err, Capacity)
	if !b.failed || b.done || len(b.catalog.Entries) != 2 {
		t.Fatal("candidate silently truncated or lost failure state")
	}
	requireProjectionCode(t, p.publish(context.Background(), b), ProjectionCorrupt)
	if _, err = os.Lstat(filepath.Join(dir, projectionDirectory)); !os.IsNotExist(err) {
		t.Fatal("failed capacity candidate provisioned/published partial coverage")
	}
	status, err := s.Status(context.Background())
	if err != nil || status.AuthorityGeneration != 3 || status.Readiness != "ready" {
		t.Fatal("derived capacity failure changed authoritative source")
	}
}

func TestProjectionFailedGitHydrationPreservesCatalogAndTrackerReads(t *testing.T) {
	p, s, dir := projectionFixture(t)
	ctx := context.Background()
	issue := createIssue(t, s, "projection-intact-tracker")
	if _, err := s.SetupKnowledge(ctx, testProject, "projection-damaged-git-setup"); err != nil {
		t.Fatal("explicit damaged-Git fixture setup failed")
	}
	doc, err := s.MutateDocument(ctx, documentRequest(t, "document.create", "projection-damaged-git-doc", authority.EmptyKnowledgeRef, authority.Citation{}, "# Original private synthetic index source\n"), testActor)
	if err != nil {
		t.Fatal("damaged-Git document fixture unavailable")
	}
	before := publishProjectionFixture(t, p, 2)
	oid := doc.Result.Locator.BlobOID
	path := filepath.Join(dir, knowledgeDirectoryName, "objects", oid[:2], oid[2:])
	if os.WriteFile(path, []byte("synthetic-corrupt-object"), 0600) != nil {
		t.Fatal("damaged Git control unavailable")
	}
	b, err := p.newBuilder(ctx, 2)
	if err != nil {
		t.Fatal("derived candidate intake unexpectedly repaired Git")
	}
	_, err = b.step(ctx, 100)
	if err == nil || !b.failed || b.done {
		t.Fatal("failed Git hydration became a successful projection prefix")
	}
	requireProjectionCode(t, p.publish(ctx, b), ProjectionCorrupt)
	loaded, err := p.view(ctx)
	if err != nil || loaded == nil || !reflect.DeepEqual(before, *loaded) {
		t.Fatal("failed source hydration changed complete derived catalog")
	}
	read, err := s.Read(ctx, authority.ReadRequest{ProjectID: testProject, Kind: authority.Issue, EntityID: issue.Citation.EntityID, Operation: "get"})
	if err != nil || len(read.Entities) != 1 || read.Entities[0].Citation != issue.Citation {
		t.Fatal("unrelated intact tracker read depended on failed index/Git")
	}
}
