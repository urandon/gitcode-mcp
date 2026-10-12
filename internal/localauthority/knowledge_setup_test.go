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

	"gitcode-mcp/internal/authority"
)

// Test-only phase model: all SQL/filesystem writes are to testStore's isolated
// qualified temporary authority. There is no production setup mutator yet.
func reserveKnowledgeSetupFixture(t *testing.T, s *Store) knowledgeSetupRecord {
	t.Helper()
	instance, err := authority.NewUUID()
	if err != nil {
		t.Fatal("synthetic knowledge identity unavailable")
	}
	k := knowledgeSetupRecord{Project: s.manifest.ID, Tracker: s.instance, Instance: instance,
		Coordinate: hash([]byte(filepath.Join(s.directory, knowledgeDirectoryName))),
		Config:     hash([]byte(knowledgeConfig + knowledgeHEAD)), Algorithm: knowledgeAlgorithm,
		Key: hash([]byte(string(s.manifest.ID) + "\x00synthetic-original-setup")), Phase: "reserved", Generation: 1}
	k.Intent = k.intentHash()
	if _, err = s.db.Exec(`INSERT INTO knowledge_setup VALUES(1,?,?,?,?,?,?,?,?,?,?,?,?)`, k.Project, k.Tracker, k.Instance, k.Coordinate, k.Physical, k.Binding, k.Config, k.Algorithm, k.Key, k.Intent, k.Phase, k.Generation); err != nil || s.publishBarrier() != nil {
		t.Fatal("test-only setup reservation failed")
	}
	return k
}

func bindKnowledgeSetupFixture(t *testing.T, s *Store, k knowledgeSetupRecord) knowledgeSetupRecord {
	t.Helper()
	path := filepath.Join(s.directory, knowledgeDirectoryName)
	if os.Mkdir(path, 0700) != nil || syncDirectory(s.directory) != nil {
		t.Fatal("test-only owned directory creation failed")
	}
	physical, err := knowledgeDirectoryIdentity(path)
	if err != nil {
		t.Fatal("test-only physical identity unavailable")
	}
	k.Physical, k.Phase, k.Generation = physical, "bound", 2
	k.Binding = k.bindingHash()
	if _, err = s.db.Exec(`UPDATE knowledge_setup SET physical_hash=?,binding_hash=?,phase='bound',generation=2 WHERE phase='reserved' AND generation=1`, k.Physical, k.Binding); err != nil || s.publishBarrier() != nil {
		t.Fatal("test-only setup physical binding failed")
	}
	return k
}

func readyKnowledgeSetupFixture(t *testing.T, s *Store, k knowledgeSetupRecord) knowledgeSetupRecord {
	t.Helper()
	root := filepath.Join(s.directory, knowledgeDirectoryName)
	for _, directory := range []string{"objects", "refs", "refs/heads", "refs/pins"} {
		path := filepath.Join(root, directory)
		if os.Mkdir(path, 0700) != nil || syncDirectory(filepath.Dir(path)) != nil {
			t.Fatal("test-only Git directory publication failed")
		}
	}
	for name, body := range map[string]string{"config": knowledgeConfig, "HEAD": knowledgeHEAD} {
		path := filepath.Join(root, name)
		if os.WriteFile(path, []byte(body), 0600) != nil {
			t.Fatal("test-only Git metadata publication failed")
		}
		f, err := openExistingFile(path)
		if err != nil {
			t.Fatal("test-only Git metadata barrier unavailable")
		}
		err = syncFile(f)
		_ = f.Close()
		if err != nil {
			t.Fatal("test-only Git metadata barrier failed")
		}
	}
	if syncDirectory(root) != nil {
		t.Fatal("test-only Git root barrier failed")
	}
	if _, _, err := s.knowledgeRepository(k).verifyLayout(); err != nil {
		t.Fatal("test-only bound Git format rejected")
	}
	if _, err := s.db.Exec(`UPDATE knowledge_setup SET phase='ready',generation=3 WHERE phase='bound' AND generation=2`); err != nil || s.publishBarrier() != nil {
		t.Fatal("test-only ready settlement failed")
	}
	k.Phase, k.Generation = "ready", 3
	return k
}

func TestKnowledgeSetupReadinessHasNoHiddenSetupAndPreservesV1(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		var s *Store
		if legacy {
			s, _, _ = legacyAuthorityFixture(t)
		} else {
			s, _, _ = testStore(t)
		}
		before, version := authorityCatalogDigest(t, s)
		state, err := s.KnowledgeStatus(context.Background(), testProject)
		if err != nil || state.Validate() != nil || state.Scope != "knowledge_setup" {
			t.Fatal("finite setup readiness unavailable")
		}
		want := string(KnowledgeSetupRequired)
		if legacy {
			want = string(KnowledgeMigrationRequired)
		}
		if state.Code != want || state.Durability != "" || state.InstanceID != "" {
			t.Fatal("uninitialized knowledge advertised readiness or wrong remediation")
		}
		after, afterVersion := authorityCatalogDigest(t, s)
		if before != after || version != afterVersion {
			t.Fatal("readiness changed or migrated SQL catalog")
		}
		if _, err := os.Lstat(filepath.Join(s.directory, knowledgeDirectoryName)); !os.IsNotExist(err) {
			t.Fatal("readiness created Git storage")
		}
	}
}

func TestKnowledgeSetupSQLPhaseModelSurvivesReopen(t *testing.T) {
	s, registry, directory := testStore(t)
	k := reserveKnowledgeSetupFixture(t, s)
	for phase := 1; phase <= 3; phase++ {
		if phase == 2 {
			k = bindKnowledgeSetupFixture(t, s, k)
		}
		if phase == 3 {
			k = readyKnowledgeSetupFixture(t, s, k)
		}
		state, err := s.KnowledgeStatus(context.Background(), testProject)
		if err != nil || state.Validate() != nil || state.InstanceID != k.Instance || state.Phase != k.Phase {
			t.Fatal("original setup phase or identity changed")
		}
		if s.Close() != nil {
			t.Fatal("test-only store quiesce failed")
		}
		s, err = OpenWriter(context.Background(), registry, directory, testProject)
		if err != nil {
			t.Fatal("intact setup record blocked tracker reopen")
		}
		t.Cleanup(func() { _ = s.Close() })
		loaded, err := loadKnowledgeSetup(context.Background(), s.db, testProject, s.instance, s.directory)
		if err != nil || loaded != k {
			t.Fatal("original setup record did not survive reopen")
		}
		createIssue(t, s, fmt.Sprintf("synthetic-phase-%d-tracker", phase))
	}
}

func TestKnowledgeSetupSQLCannotDowngradeOrReplaceIdentity(t *testing.T) {
	s, _, _ := testStore(t)
	k := reserveKnowledgeSetupFixture(t, s)
	for _, query := range []string{
		`UPDATE knowledge_setup SET phase='ready',generation=3`,
		`UPDATE knowledge_setup SET knowledge_instance='12345678-1234-4234-8234-123456789abc'`,
		`DELETE FROM knowledge_setup`,
	} {
		if _, err := s.db.Exec(query); err == nil {
			t.Fatal("unbound setup skipped a phase or changed identity")
		}
	}
	k = bindKnowledgeSetupFixture(t, s, k)
	for _, query := range []string{
		`UPDATE knowledge_setup SET phase='reserved',generation=1,physical_hash='',binding_hash=''`,
		`UPDATE knowledge_setup SET physical_hash=printf('%064d',0)`,
		`UPDATE knowledge_setup SET binding_hash=printf('%064d',0)`,
	} {
		if _, err := s.db.Exec(query); err == nil {
			t.Fatal("bound physical evidence was replaced or downgraded")
		}
	}
	k = readyKnowledgeSetupFixture(t, s, k)
	if _, err := s.db.Exec(`UPDATE knowledge_setup SET phase='bound',generation=2`); err == nil {
		t.Fatal("ready setup was downgraded")
	}
	loaded, err := loadKnowledgeSetup(context.Background(), s.db, testProject, s.instance, s.directory)
	if err != nil || loaded != k {
		t.Fatal("rejected transition changed the original journal")
	}
}

func TestKnowledgeSetupGitDamageIsDocumentOnly(t *testing.T) {
	for _, fault := range []string{"missing", "replaced", "config"} {
		t.Run(fault, func(t *testing.T) {
			s, registry, directory := testStore(t)
			k := readyKnowledgeSetupFixture(t, s, bindKnowledgeSetupFixture(t, s, reserveKnowledgeSetupFixture(t, s)))
			issue := createIssue(t, s, "synthetic-before-Git-damage")
			root := filepath.Join(s.directory, knowledgeDirectoryName)
			if fault == "config" {
				if os.WriteFile(filepath.Join(root, "config"), []byte("synthetic foreign config"), 0600) != nil {
					t.Fatal("foreign config fixture failed")
				}
			} else {
				if os.Rename(root, root+".preserved") != nil {
					t.Fatal("test-only Git evidence preservation failed")
				}
				if fault == "replaced" && os.Mkdir(root, 0700) != nil {
					t.Fatal("physical replacement fixture failed")
				}
			}
			state, err := s.KnowledgeStatus(context.Background(), testProject)
			if err != nil || state.Validate() != nil || state.Code != string(KnowledgeCorrupt) || state.InstanceID != k.Instance || s.fenced {
				t.Fatal("Git-only damage lost identity or fenced global SQL")
			}
			if s.Close() != nil {
				t.Fatal("test-only damaged store quiesce failed")
			}
			opened, err := OpenWriter(context.Background(), registry, directory, testProject)
			if err != nil {
				t.Fatal("Git-only damage blocked intact tracker admission")
			}
			defer opened.Close()
			view, err := opened.Read(context.Background(), authority.ReadRequest{ProjectID: testProject, Kind: authority.Issue, EntityID: issue.Citation.EntityID, Operation: "get"})
			if err != nil || len(view.Entities) != 1 {
				t.Fatal("Git-only damage blocked an unrelated tracker read")
			}
			createIssue(t, opened, "synthetic-after-Git-damage")
		})
	}
}

func TestKnowledgeSetupReadinessIsFiniteAndContentFree(t *testing.T) {
	s, _, _ := testStore(t)
	k := readyKnowledgeSetupFixture(t, s, bindKnowledgeSetupFixture(t, s, reserveKnowledgeSetupFixture(t, s)))
	state, err := s.KnowledgeStatus(context.Background(), testProject)
	if err != nil || state.Validate() != nil || state.Durability != authority.OrderedOSBarriersV1 {
		t.Fatal("ready finite state rejected")
	}
	body, _ := json.Marshal(state)
	for _, private := range []string{s.directory, k.Key, k.Intent, k.Physical, "synthetic-original-setup"} {
		if strings.Contains(string(body), private) {
			t.Fatal("private setup coordinates or key escaped status")
		}
	}
	state.Remediation = "arbitrary private text"
	if state.Validate() == nil {
		t.Fatal("arbitrary readiness remediation accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.KnowledgeStatus(ctx, testProject)
	if !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation misclassified as storage corruption")
	}
}

func TestKnowledgeSetupSQLCorruptionFencesAuthority(t *testing.T) {
	for _, column := range []string{"physical_hash", "binding_hash", "intent_hash", "coordinate_hash", "config_hash", "knowledge_instance", "algorithm", "generation"} {
		t.Run(column, func(t *testing.T) {
			s, registry, directory := testStore(t)
			readyKnowledgeSetupFixture(t, s, bindKnowledgeSetupFixture(t, s, reserveKnowledgeSetupFixture(t, s)))
			original := request(t, "issue.create", "synthetic-before-setup-SQL-corruption", authority.IssuePayload{Title: "Original", State: "open"})
			mustMutate(t, s, original)
			for _, trigger := range []string{"immutable_knowledge_setup_identity", "monotonic_knowledge_setup"} {
				corruptExec(t, s.db, "DROP TRIGGER "+trigger)
			}
			corruptExec(t, s.db, "PRAGMA ignore_check_constraints=ON")
			value := strings.Repeat("a", 64)
			if column == "knowledge_instance" {
				value = string(knowledgeTestEntity)
			}
			if column == "algorithm" || column == "generation" {
				value = strings.Repeat("synthetic-private-scalar", 65536)
			}
			corruptExec(t, s.db, "UPDATE knowledge_setup SET "+column+"=?", value)
			corruptExec(t, s.db, "PRAGMA ignore_check_constraints=OFF")
			for _, statement := range strings.Split(knowledgeSchemaSQL, ";\n") {
				if strings.HasPrefix(strings.TrimSpace(statement), "CREATE TRIGGER immutable_knowledge_setup_identity ") || strings.HasPrefix(strings.TrimSpace(statement), "CREATE TRIGGER monotonic_knowledge_setup ") {
					corruptExec(t, s.db, strings.TrimSpace(statement))
				}
			}
			_, err := s.KnowledgeStatus(context.Background(), testProject)
			requireKnowledgeError(t, err, Corrupt)
			if !s.fenced || !s.logicalFenced || strings.Contains(err.Error(), "synthetic-private-scalar") {
				t.Fatal("SQL corruption did not fence writes or escaped diagnostics")
			}
			_, err = s.Mutate(context.Background(), original, testActor)
			requireKnowledgeError(t, err, Corrupt)
			if !s.fenced || !s.logicalFenced {
				t.Fatal("old receipt replay downgraded a logical corruption fence")
			}
			_, err = s.Mutate(context.Background(), request(t, "issue.create", "synthetic-after-setup-SQL-corruption", authority.IssuePayload{Title: "Example", State: "open"}), testActor)
			requireKnowledgeError(t, err, Corrupt)
			if s.Close() != nil {
				t.Fatal("test-only corrupted authority quiesce failed")
			}
			for _, writer := range []bool{false, true} {
				var opened *Store
				if writer {
					opened, err = OpenWriter(context.Background(), registry, directory, testProject)
				} else {
					opened, err = OpenReadOnly(context.Background(), directory, testProject)
				}
				if opened != nil {
					_ = opened.Close()
				}
				requireKnowledgeError(t, err, Corrupt)
			}
		})
	}
}

func TestKnowledgeSetupCapacityCarriesFiniteCategory(t *testing.T) {
	s, _, _ := testStore(t)
	readyKnowledgeSetupFixture(t, s, bindKnowledgeSetupFixture(t, s, reserveKnowledgeSetupFixture(t, s)))
	for i := 0; i <= maxRetainedKnowledgePins; i++ {
		name := fmt.Sprintf("%08x-1234-4234-8234-123456789abc", i)
		if os.WriteFile(filepath.Join(s.directory, knowledgeDirectoryName, "refs", "pins", name), []byte(strings.Repeat("a", 64)+"\n"), 0600) != nil {
			t.Fatal("bounded pin capacity fixture failed")
		}
	}
	state, err := s.KnowledgeStatus(context.Background(), testProject)
	if err != nil || state.Validate() != nil || state.Code != string(Capacity) || state.Capacity != "retained_pins" || state.Scope != "knowledge_setup" || s.fenced {
		t.Fatal("knowledge-only capacity lost finite category or fenced SQL")
	}
	createIssue(t, s, "synthetic-tracker-under-knowledge-capacity")
}

func TestKnowledgeSetupOriginalKeyCannotBecomeTrackerMutation(t *testing.T) {
	s, _, _ := testStore(t)
	k := reserveKnowledgeSetupFixture(t, s)
	_, err := s.Mutate(context.Background(), request(t, "issue.create", "synthetic-original-setup", authority.IssuePayload{Title: "Other intent", State: "open"}), testActor)
	var conflict authority.Error
	if !errors.As(err, &conflict) || conflict.Code != authority.Conflict {
		t.Fatal("setup key admitted a different tracker intent")
	}
	loaded, err := loadKnowledgeSetup(context.Background(), s.db, testProject, s.instance, s.directory)
	if err != nil || loaded != k {
		t.Fatal("key conflict changed original setup reservation")
	}
	status, err := s.Status(context.Background())
	if err != nil || status.AuthorityGeneration != 0 {
		t.Fatal("key conflict produced a tracker effect")
	}
	createIssue(t, s, "synthetic-unrelated-key-after-conflict")
}

func TestKnowledgeSetupUnboundDirectoryIsNotPromotedByRead(t *testing.T) {
	s, _, _ := testStore(t)
	k := reserveKnowledgeSetupFixture(t, s)
	root := filepath.Join(s.directory, knowledgeDirectoryName)
	if os.Mkdir(root, 0700) != nil || os.WriteFile(filepath.Join(root, "foreign.txt"), []byte("synthetic preserved foreign state"), 0600) != nil {
		t.Fatal("unbound foreign directory fixture failed")
	}
	state, err := s.KnowledgeStatus(context.Background(), testProject)
	if err != nil || state.Validate() != nil || state.Phase != "reserved" || state.Code != string(KnowledgeInitializationPending) {
		t.Fatal("unbound directory was promoted or readiness was guessed")
	}
	loaded, err := loadKnowledgeSetup(context.Background(), s.db, testProject, s.instance, s.directory)
	if err != nil || loaded != k || loaded.Physical != "" || loaded.Binding != "" {
		t.Fatal("read filled missing physical ownership proof")
	}
	body, err := os.ReadFile(filepath.Join(root, "foreign.txt"))
	if err != nil || string(body) != "synthetic preserved foreign state" {
		t.Fatal("read changed unbound evidence")
	}
}
