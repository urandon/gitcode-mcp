package localauthority

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gitcode-mcp/internal/authority"
)

// This digest freezes the exact catalog delivered by #181, not whichever
// schema fresh initialization happens to select in a subsequent build.
const legacyAuthoritySchemaDigest = "61856938d281f218f2d6896cc745f606335f60966a9bcf5b66c2049b96b5060a"

// Build an isolated old-format authority through its original SQL contract.
// This helper never opens a real cache, copies a live store or invokes migration.
func legacyAuthorityFixture(t *testing.T) (*Store, *Registry, string) {
	t.Helper()
	if hash([]byte(schemaSQL)) != legacyAuthoritySchemaDigest {
		t.Fatal("original authority v1 DDL changed")
	}
	r, directory := initializationFixture(t)
	ctx := context.Background()
	directory, err := privateDirectory(directory)
	if err != nil || qualifyFilesystem(directory) != nil {
		t.Fatal("isolated legacy coordinate qualification failed")
	}
	path := filepath.Join(directory, "tracker.db")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil || f.Close() != nil {
		t.Fatal("isolated legacy database file unavailable")
	}
	db, err := openDB(ctx, path, false)
	if err != nil {
		t.Fatal("isolated legacy SQL connection unavailable")
	}
	defer db.Close()
	var mode string
	if db.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode) != nil || mode != "wal" || verifyWriter(ctx, db) != nil {
		t.Fatal("legacy SQL fixture is not WAL FULL")
	}
	instance, err := authority.NewUUID()
	if err != nil {
		t.Fatal("legacy fixture identity unavailable")
	}
	m := testManifest()
	manifest, _ := json.Marshal(m)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal("legacy fixture transaction unavailable")
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, schemaSQL); err != nil {
		t.Fatal("frozen legacy DDL rejected")
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO project VALUES(1,?,?,?,1,0)", m.ID, instance, string(manifest)); err != nil {
		t.Fatal("legacy identity insertion failed")
	}
	for _, kind := range m.Entities {
		if _, err = tx.ExecContext(ctx, "INSERT INTO aliases VALUES(?,1)", kind); err != nil {
			t.Fatal("legacy alias insertion failed")
		}
	}
	if _, err = tx.ExecContext(ctx, "PRAGMA application_id=1195593025; PRAGMA user_version=1"); err != nil || tx.Commit() != nil {
		t.Fatal("legacy header transaction failed")
	}
	var busy, pages, done int
	if db.QueryRowContext(ctx, "PRAGMA wal_checkpoint(FULL)").Scan(&busy, &pages, &done) != nil || busy != 0 || pages != done || db.Close() != nil {
		t.Fatal("legacy fixture checkpoint failed")
	}
	owner, err := r.acquire(ctx, m.ID, instance, directory)
	if err != nil {
		t.Fatal("isolated legacy writer lease unavailable")
	}
	if owner.bindPhysical(path) != nil || owner.close() != nil {
		t.Fatal("isolated legacy physical binding failed")
	}
	s, err := OpenWriter(ctx, r, directory, m.ID)
	if err != nil {
		t.Fatal("current binary refused intact legacy authority")
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, r, directory
}

func authorityCatalogDigest(t *testing.T, s *Store) (string, int) {
	t.Helper()
	var version int
	if s.db.QueryRow("PRAGMA user_version").Scan(&version) != nil {
		t.Fatal("fixture schema version unavailable")
	}
	rows, err := s.db.Query("SELECT type,name,coalesce(sql,'') FROM sqlite_schema ORDER BY name LIMIT 65")
	if err != nil {
		t.Fatal("fixture schema catalog unavailable")
	}
	defer rows.Close()
	var catalog [][3]string
	for rows.Next() {
		var entry [3]string
		if rows.Scan(&entry[0], &entry[1], &entry[2]) != nil || len(catalog) >= 64 {
			t.Fatal("fixture catalog bound exceeded")
		}
		catalog = append(catalog, entry)
	}
	if rows.Err() != nil {
		t.Fatal("fixture catalog scan failed")
	}
	body, _ := json.Marshal(catalog)
	return hash(body), version
}

func TestKnowledgeCompatibilityLegacyTrackerPreservesExactSchema(t *testing.T) {
	s, registry, directory := legacyAuthorityFixture(t)
	ctx := context.Background()
	catalog, version := authorityCatalogDigest(t, s)
	if version != 1 || s.manifest.Schema != authority.Schema {
		t.Fatal("legacy database or manifest version changed")
	}
	request := request(t, "issue.create", "synthetic-legacy-original", authority.IssuePayload{Title: "Original", Body: "Legacy fixture", State: "open"})
	original, err := s.Mutate(ctx, request, testActor)
	if err != nil || s.Close() != nil {
		t.Fatal("legacy confirmed mutation failed")
	}
	path := filepath.Join(directory, "tracker.db")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal("legacy snapshot unavailable")
	}
	ro, err := OpenReadOnly(ctx, directory, testProject)
	if err != nil {
		t.Fatal("legacy read-only reopen failed")
	}
	stored, err := ro.Receipt(ctx, request.Intent.IdempotencyKey)
	if err != nil || stored != original {
		t.Fatal("legacy receipt changed after read-only reopen")
	}
	read, err := ro.Read(ctx, authority.ReadRequest{ProjectID: testProject, Kind: authority.Issue, EntityID: original.Citation.EntityID, Operation: "get"})
	if err != nil || len(read.Entities) != 1 || read.Entities[0].Citation != original.Citation {
		t.Fatal("legacy cached authority read changed")
	}
	if ro.Close() != nil {
		t.Fatal("legacy reader close failed")
	}
	after, err := os.ReadFile(path)
	if err != nil || hash(before) != hash(after) {
		t.Fatal("read-only legacy reopen rewrote authority bytes")
	}
	writable, err := OpenWriter(ctx, registry, directory, testProject)
	if err != nil {
		t.Fatal("legacy writer reopen failed")
	}
	defer writable.Close()
	replay, err := writable.Mutate(ctx, request, testActor)
	if err != nil || !replay.Replayed || replay.OperationID != original.OperationID || replay.Citation != original.Citation {
		t.Fatal("legacy original key did not replay unchanged operation")
	}
	update := requestFixtureLegacyUpdate(t, original)
	changed, err := writable.Mutate(ctx, update, testActor)
	if err != nil || changed.Citation.Revision != 2 {
		t.Fatal("legacy tracker update became unavailable")
	}
	audit, err := writable.Audit(ctx, 0, 100)
	if err != nil || len(audit) != 2 {
		t.Fatal("legacy tracker audit semantics changed")
	}
	currentCatalog, currentVersion := authorityCatalogDigest(t, writable)
	if currentCatalog != catalog || currentVersion != 1 {
		t.Fatal("legacy tracker operations migrated or altered DDL")
	}
	var knowledgeTables int
	if writable.db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name IN('knowledge_setup','document_operations')").Scan(&knowledgeTables) != nil || knowledgeTables != 0 {
		t.Fatal("legacy open or mutation created knowledge tables")
	}
	if _, err := os.Lstat(filepath.Join(directory, "knowledge.git")); !os.IsNotExist(err) {
		t.Fatal("legacy tracker operation created knowledge storage")
	}
}

func requestFixtureLegacyUpdate(t *testing.T, original authority.MutationReceipt) authority.MutationRequest {
	t.Helper()
	return request(t, "issue.update", "synthetic-legacy-update", authority.IssuePayload{Title: "Changed", Body: "Explicit local mutation", State: "closed"}, original)
}

func TestKnowledgeCompatibilityFreshVersionIsIndependent(t *testing.T) {
	s, _, directory := testStore(t)
	_, version := authorityCatalogDigest(t, s)
	if version != 2 || s.manifest.Schema != 1 || authority.Schema != 1 || authority.DocumentContractSchema != 1 {
		t.Fatal("fresh database version was not explicit and independent of wire/manifest versions")
	}
	var tables int
	if s.db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name IN('knowledge_setup','document_operations') AND type='table'").Scan(&tables) != nil || tables != 2 {
		t.Fatal("fresh database lacks the dedicated content-free knowledge schema")
	}
	if authority.ContractsOnly().Available || len(authority.ContractsOnly().Methods) != 0 {
		t.Fatal("fresh schema advertised unfinished document workflows")
	}
	if _, err := os.Lstat(filepath.Join(directory, "knowledge.git")); !os.IsNotExist(err) {
		t.Fatal("tracker initialization performed hidden knowledge setup")
	}
	createIssue(t, s, "synthetic-fresh-tracker")
	status, err := s.Status(context.Background())
	if err != nil || status.Readiness != "ready" {
		t.Fatal("uninitialized knowledge blocked intact tracker")
	}
}

func TestKnowledgeDocumentAdmissionLegacyStopsWithoutMigrationEffects(t *testing.T) {
	s, _, directory := legacyAuthorityFixture(t)
	before, version := authorityCatalogDigest(t, s)
	if version != 1 {
		t.Fatal("test legacy version unavailable")
	}
	request := documentRequest(t, "document.create", "synthetic-legacy-doc", authority.EmptyKnowledgeRef, authority.Citation{}, "# No implicit migration")
	_, err := s.MutateDocument(context.Background(), request, testActor)
	requireKnowledgeError(t, err, KnowledgeMigrationRequired)
	after, currentVersion := authorityCatalogDigest(t, s)
	if before != after || currentVersion != 1 {
		t.Fatal("document admission silently changed legacy catalog")
	}
	if _, err := os.Lstat(filepath.Join(directory, knowledgeDirectoryName)); !os.IsNotExist(err) {
		t.Fatal("legacy document admission created knowledge storage")
	}
	createIssue(t, s, "synthetic-legacy-tracker-after-doc-refusal")
}
