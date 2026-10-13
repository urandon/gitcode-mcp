package localauthority

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gitcode-mcp/internal/authority"
)

func selectedRecoveryFixture(t *testing.T, mode, cut string, occurrence int) (*Store, *Registry, string, authority.DocumentMutationRequest, documentOperation) {
	t.Helper()
	return selectedRecoveryActorFixture(t, mode, cut, occurrence, testActor)
}

func selectedRecoveryActorFixture(t *testing.T, mode, cut string, occurrence int, actor Actor) (*Store, *Registry, string, authority.DocumentMutationRequest, documentOperation) {
	t.Helper()
	s, registry, directory := readyDocumentStore(t)
	ctx := context.Background()
	ref, citation := authority.EmptyKnowledgeRef, authority.Citation{}
	if mode != "create" {
		first, err := s.MutateDocument(ctx, documentRequest(t, "document.create", "synthetic-selected-parent", ref, citation, "# Original parent"), actor)
		if err != nil {
			t.Fatal("selected recovery parent unavailable")
		}
		ref, citation = first.Result.Locator.CommitOID, first.Receipt.Citation
	}
	req := documentRequest(t, "document."+mode, "synthetic-selected-original-key", ref, citation, "# selected-recovery-Git-only-sentinel")
	stop, count := errors.New("synthetic selected interruption"), 0
	s.fault = func(at string) error {
		if at == cut {
			count++
			if count == occurrence {
				return stop
			}
		}
		return nil
	}
	if _, err := s.MutateDocument(ctx, req, actor); !errors.Is(err, stop) {
		t.Fatal("selected recovery missed original barrier")
	}
	s.fault = nil
	o, err := loadDocumentOperation(ctx, s.db, testProject, hash([]byte(string(testProject)+"\x00"+req.Intent.IdempotencyKey)))
	if err != nil {
		t.Fatal("selected original journal unavailable")
	}
	if s.Close() != nil {
		t.Fatal("selected recovery owner did not close")
	}
	next, err := OpenWriter(ctx, registry, directory, testProject)
	if err != nil {
		t.Fatal("selected recovery owner could not reopen")
	}
	t.Cleanup(func() { _ = next.Close() })
	return next, registry, directory, req, o
}

func selectRecovery(o documentOperation) recoverySelection {
	return recoverySelection{project: testProject, operation: o.ID, intent: o.Intent, generation: o.Generation}
}

func knowledgeFootprint(t *testing.T, s *Store) map[string]string {
	t.Helper()
	root, out := filepath.Join(s.directory, knowledgeDirectoryName), map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("synthetic nonregular footprint")
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out[name] = hash(body)
		return nil
	})
	if err != nil {
		t.Fatal("synthetic knowledge footprint unavailable")
	}
	return out
}

func rejectRecoveryWrites(t *testing.T, s *Store) {
	t.Helper()
	s.fault = func(string) error {
		t.Fatal("proof/refusal performed publication or authority write")
		return Error{Corrupt}
	}
	// Clear before test cleanup or a later explicitly admitted action.
	t.Cleanup(func() { s.fault = nil })
}

func assertSelectedOriginal(t *testing.T, s *Store, o documentOperation, result authority.DocumentMutationResult) {
	t.Helper()
	if result.Validate() != nil || !result.Receipt.Replayed || result.Receipt.OperationID != o.ID || result.Receipt.Citation != o.Metadata.citation() || result.Receipt.IdempotencyKeyHash != o.Key || result.Receipt.IntentHash != o.Intent {
		t.Fatal("selected recovery substituted original identity/result")
	}
	saved, err := loadDocumentOperation(context.Background(), s.db, testProject, o.Key)
	if err != nil || saved.Phase != "settled" || saved.Generation != 3 || saved.Result == nil {
		t.Fatal("selected recovery did not settle original journal")
	}
	copy := result
	copy.Receipt.Replayed = false
	if !equalDocumentResult(copy, *saved.Result) || saved.commitOID() != o.commitOID() {
		t.Fatal("selected recovery changed original immutable binding")
	}
	for _, table := range []string{"receipts", "audit"} {
		var count int
		if s.db.QueryRow("SELECT count(*) FROM "+table+" WHERE operation_id=?", o.ID).Scan(&count) != nil || count != 1 {
			t.Fatal("selected recovery duplicated original acknowledgement")
		}
	}
	if s.verifyIdentity(context.Background(), testProject) != nil {
		t.Fatal("selected recovery broke canonical consistency")
	}
}

func TestKnowledgeSelectedRecoveryExactPinBindingRefAndReceipt(t *testing.T) {
	for _, mode := range []string{"create", "update", "tombstone"} {
		for _, cut := range []struct {
			name string
			n    int
		}{
			{"knowledge_ref_directory", 1}, // published pin, SQL still reserved
			{"knowledge_document_bound", 1},
			{"knowledge_ref_file", 2},   // owned head lock before rename
			{"knowledge_ref_rename", 2}, // candidate head, directory barrier pending
			{"knowledge_document_settled", 1},
			{"knowledge_document_acknowledgement", 1},
		} {
			t.Run(mode+"-"+cut.name, func(t *testing.T) {
				s, _, _, req, o := selectedRecoveryFixture(t, mode, cut.name, cut.n)
				selection := selectRecovery(o)
				var beforeRef os.FileInfo
				if cut.name == "knowledge_ref_rename" {
					beforeRef, _ = os.Stat(filepath.Join(s.directory, knowledgeDirectoryName, "refs", "heads", "knowledge"))
					barriers := 0
					s.fault = func(at string) error {
						if at == "knowledge_ref_rename" {
							t.Fatal("already-candidate recovery rewrote a ref")
						}
						if at == "knowledge_ref_directory" {
							barriers++
						}
						return nil
					}
					defer func() {
						if barriers < 2 {
							t.Error("candidate ref durability was not requalified")
						}
					}()
				}
				if o.Phase == "settled" {
					rejectRecoveryWrites(t, s)
				}
				result, err := s.recoverSelectedDocument(context.Background(), selection)
				if err != nil {
					t.Fatalf("exact selected recovery failed: %T", err)
				}
				if beforeRef != nil {
					after, _ := os.Stat(filepath.Join(s.directory, knowledgeDirectoryName, "refs", "heads", "knowledge"))
					if after == nil || !os.SameFile(beforeRef, after) {
						t.Fatal("candidate ref identity changed")
					}
				}
				s.fault = nil
				assertSelectedOriginal(t, s, o, result)
				rejectRecoveryWrites(t, s)
				before := knowledgeFootprint(t, s)
				replayed, err := s.recoverSelectedDocument(context.Background(), selection)
				if err != nil || !equalDocumentResult(replayed, result) || !reflect.DeepEqual(before, knowledgeFootprint(t, s)) {
					t.Fatal("settled selected replay mutated/substituted authority")
				}
				s.fault = nil
				original, err := s.MutateDocument(context.Background(), req, testActor)
				if err != nil || !equalDocumentResult(original, result) {
					t.Fatal("original caller lost exact replay")
				}
				for _, filename := range []string{"tracker.db", "tracker.db-wal"} {
					data, err := os.ReadFile(filepath.Join(s.directory, filename))
					if os.IsNotExist(err) && filename == "tracker.db-wal" {
						continue
					}
					if err != nil || bytes.Contains(data, []byte("selected-recovery-Git-only-sentinel")) || bytes.Contains(data, []byte(req.Intent.IdempotencyKey)) {
						t.Fatal("selected recovery persisted source bytes/raw caller key")
					}
				}
			})
		}
	}
}

func TestKnowledgeSelectedRecoveryNoPinAndLockOnlyRequireOriginalIntent(t *testing.T) {
	for _, cut := range []string{"knowledge_document_reserved", "knowledge_ref_file"} {
		t.Run(cut, func(t *testing.T) {
			s, _, _, req, o := selectedRecoveryFixture(t, "create", cut, 1)
			before := knowledgeFootprint(t, s)
			rejectRecoveryWrites(t, s)
			_, err := s.recoverSelectedDocument(context.Background(), selectRecovery(o))
			requireKnowledgeError(t, err, DocumentRecoveryRequired)
			if !reflect.DeepEqual(before, knowledgeFootprint(t, s)) {
				t.Fatal("unprepared recovery changed Git evidence")
			}
			saved, err := loadDocumentOperation(context.Background(), s.db, testProject, o.Key)
			if err != nil || !reflect.DeepEqual(o, saved) {
				t.Fatal("unprepared recovery changed original reservation")
			}
			s.fault = nil
			createIssue(t, s, "synthetic-tracker-during-unprepared-recovery")
			result, err := s.MutateDocument(context.Background(), req, testActor)
			if err != nil {
				t.Fatal("no-pin refusal lost original-intent recovery")
			}
			assertSelectedOriginal(t, s, o, result)
		})
	}
}

func TestKnowledgeSelectedRecoveryMonotonicRetryAfterBinding(t *testing.T) {
	s, _, _, _, o := selectedRecoveryFixture(t, "create", "knowledge_ref_directory", 1)
	selection := selectRecovery(o)
	stop := errors.New("synthetic recovery binding interruption")
	s.fault = func(at string) error {
		if at == "knowledge_document_bound" {
			return stop
		}
		return nil
	}
	if _, err := s.recoverSelectedDocument(context.Background(), selection); !errors.Is(err, stop) {
		t.Fatal("selected recovery binding fault not reached")
	}
	s.fault = nil
	bound, err := loadDocumentOperation(context.Background(), s.db, testProject, o.Key)
	if err != nil || bound.Phase != "bound" || bound.Generation != 2 {
		t.Fatal("selected binding did not persist original generation")
	}
	result, err := s.recoverSelectedDocument(context.Background(), selection)
	if err != nil {
		t.Fatal("monotonic original selection could not resume")
	}
	assertSelectedOriginal(t, s, o, result)
}

func TestKnowledgeSelectedRecoveryValidationReadOnlyAndContendedAdmission(t *testing.T) {
	s, _, directory, _, o := selectedRecoveryFixture(t, "create", "knowledge_document_bound", 1)
	before := knowledgeFootprint(t, s)
	for _, field := range []string{"project", "operation", "intent", "generation", "invalid"} {
		selection := selectRecovery(o)
		switch field {
		case "project":
			selection.project = "22222222-2222-4222-8222-222222222222"
		case "operation":
			selection.operation = "22222222-2222-4222-8222-222222222222"
		case "intent":
			selection.intent = strings.Repeat("a", 64)
		case "generation":
			selection.generation = 3
		case "invalid":
			selection.generation = 0
		}
		rejectRecoveryWrites(t, s)
		if _, err := s.recoverSelectedDocument(context.Background(), selection); err == nil {
			t.Fatal("foreign/future selection admitted")
		}
		s.fault = nil
	}
	ro, err := OpenReadOnly(context.Background(), directory, testProject)
	if err != nil {
		t.Fatal("selected recovery readonly fixture unavailable")
	}
	defer ro.Close()
	_, err = ro.recoverSelectedDocument(context.Background(), selectRecovery(o))
	requireKnowledgeError(t, err, ReadOnly)
	s.mu.Lock()
	_, err = s.recoverSelectedDocument(context.Background(), selectRecovery(o))
	s.mu.Unlock()
	var typed authority.Error
	if !errors.As(err, &typed) || typed.Code != authority.Conflict {
		t.Fatal("selected recovery did not refuse held writer turn")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.recoverSelectedDocument(ctx, selectRecovery(o)); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled selected recovery admitted")
	}
	if !reflect.DeepEqual(before, knowledgeFootprint(t, s)) {
		t.Fatal("refused selected recovery changed evidence")
	}
}

func TestKnowledgeSelectedRecoveryLateEvidenceAndLayoutCancellation(t *testing.T) {
	for _, control := range []struct {
		phase, cut string
		n          int
	}{
		{"knowledge_layout_object", "knowledge_document_bound", 1},
		{"knowledge_evidence_blob", "knowledge_document_bound", 1},
		{"knowledge_evidence_blob", "knowledge_ref_file", 2},
	} {
		t.Run(control.phase+"-"+control.cut, func(t *testing.T) {
			s, _, _, _, o := selectedRecoveryFixture(t, "update", control.cut, control.n)
			before := knowledgeFootprint(t, s)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			selection, count := selectRecovery(o), 0
			selection.readCheckpoint = func(at string) {
				if at == control.phase {
					count++
					if count == 2 {
						cancel()
					}
				}
			}
			rejectRecoveryWrites(t, s)
			_, err := s.recoverSelectedDocument(ctx, selection)
			if !errors.Is(err, context.Canceled) || count != 2 {
				t.Fatal("late actual traversal cancellation was ignored")
			}
			if !reflect.DeepEqual(before, knowledgeFootprint(t, s)) {
				t.Fatal("cancelled proof changed Git evidence")
			}
			s.fault = nil
		})
	}
}

func TestKnowledgeSelectedRecoveryCorruptEvidencePreservedAndTrackerIndependent(t *testing.T) {
	for _, damage := range []string{"missing-pin", "foreign-pin", "foreign-head", "missing-blob", "foreign-head-lock"} {
		t.Run(damage, func(t *testing.T) {
			s, _, _, _, o := selectedRecoveryFixture(t, "update", "knowledge_document_bound", 1)
			root := filepath.Join(s.directory, knowledgeDirectoryName)
			switch damage {
			case "missing-pin":
				if os.Rename(filepath.Join(root, "refs", "pins", string(o.ID)), filepath.Join(s.directory, "preserved-pin")) != nil {
					t.Fatal("synthetic pin relocation failed")
				}
			case "foreign-pin":
				if os.WriteFile(filepath.Join(root, "refs", "pins", string(o.ID)), []byte(strings.Repeat("a", 64)+"\n"), 0600) != nil {
					t.Fatal("synthetic foreign pin failed")
				}
			case "foreign-head":
				if os.WriteFile(filepath.Join(root, "refs", "heads", "knowledge"), []byte(strings.Repeat("a", 64)+"\n"), 0600) != nil {
					t.Fatal("synthetic foreign head failed")
				}
			case "missing-blob":
				oid := o.Metadata.Result.Locator.BlobOID
				if os.Rename(filepath.Join(root, "objects", oid[:2], oid[2:]), filepath.Join(s.directory, "preserved-blob")) != nil {
					t.Fatal("synthetic blob relocation failed")
				}
			case "foreign-head-lock":
				if os.WriteFile(filepath.Join(root, "refs", "heads", "knowledge.lock"), []byte(strings.Repeat("a", 64)+"\n"), 0600) != nil {
					t.Fatal("synthetic foreign lock failed")
				}
			}
			before := knowledgeFootprint(t, s)
			rejectRecoveryWrites(t, s)
			if _, err := s.recoverSelectedDocument(context.Background(), selectRecovery(o)); err == nil {
				t.Fatal("foreign/missing candidate was recovered")
			}
			if !reflect.DeepEqual(before, knowledgeFootprint(t, s)) {
				t.Fatal("foreign evidence was rewritten/removed")
			}
			s.fault = nil
			saved, err := loadDocumentOperation(context.Background(), s.db, testProject, o.Key)
			if err != nil || !reflect.DeepEqual(o, saved) {
				t.Fatal("corrupt evidence changed original reservation")
			}
			createIssue(t, s, "synthetic-tracker-after-refused-recovery")
		})
	}
}

func TestKnowledgeSelectedRecoverySettledProofCannotAcknowledgeUncertainBarrier(t *testing.T) {
	s, registry, directory := readyDocumentStore(t)
	ctx := context.Background()
	req := documentRequest(t, "document.create", "synthetic-selected-uncertain-key", authority.EmptyKnowledgeRef, authority.Citation{}, "# Exact uncertain original")
	stop, count := errors.New("synthetic settled barrier interruption"), 0
	s.fault = func(at string) error {
		if at == "database_sync" {
			count++
			if count == 3 {
				return stop
			}
		}
		return nil
	}
	if _, err := s.MutateDocument(ctx, req, testActor); !errors.Is(err, stop) || !s.fenced {
		t.Fatal("uncertain settled barrier was not retained")
	}
	s.fault = nil
	o, err := loadDocumentOperation(ctx, s.db, testProject, hash([]byte(string(testProject)+"\x00"+req.Intent.IdempotencyKey)))
	if err != nil || o.Phase != "settled" {
		t.Fatal("uncertain original SQL receipt unavailable")
	}
	rejectRecoveryWrites(t, s)
	_, err = s.recoverSelectedDocument(ctx, selectRecovery(o))
	requireKnowledgeError(t, err, CommitUncertain)
	if !s.fenced {
		t.Fatal("proof-only replay lifted uncertain writer fence")
	}
	s.fault = nil
	if s.Close() != nil {
		t.Fatal("uncertain owner did not close")
	}
	next, err := OpenWriter(ctx, registry, directory, testProject)
	if err != nil {
		t.Fatal("verified owner reopen unavailable")
	}
	defer next.Close()
	rejectRecoveryWrites(t, next)
	result, err := next.recoverSelectedDocument(ctx, selectRecovery(o))
	if err != nil {
		t.Fatal("verified reopened receipt could not replay")
	}
	assertSelectedOriginal(t, next, o, result)
}

func TestKnowledgeSelectedRecoveryTombstoneCannotHideLostParentBytes(t *testing.T) {
	s, _, _, req, o := selectedRecoveryFixture(t, "tombstone", "knowledge_document_bound", 1)
	k, err := loadKnowledgeSetup(context.Background(), s.db, testProject, s.instance, s.directory)
	if err != nil {
		t.Fatal("original parent setup unavailable")
	}
	g := s.knowledgeRepository(k)
	entries, err := g.treeAt(o.Metadata.ExpectedRefOID)
	if err != nil {
		t.Fatal("original parent tree unavailable")
	}
	oid := entries[o.Entity]
	if os.Rename(filepath.Join(g.directory, "objects", oid[:2], oid[2:]), filepath.Join(s.directory, "preserved-parent-blob")) != nil {
		t.Fatal("synthetic parent blob relocation failed")
	}
	before := knowledgeFootprint(t, s)
	rejectRecoveryWrites(t, s)
	_, err = s.recoverSelectedDocument(context.Background(), selectRecovery(o))
	requireKnowledgeError(t, err, KnowledgeCorrupt)
	if !reflect.DeepEqual(before, knowledgeFootprint(t, s)) {
		t.Fatal("lost prior history was hidden by recovery")
	}
	s.fault = nil
	_, err = s.MutateDocument(context.Background(), req, testActor)
	requireKnowledgeError(t, err, KnowledgeCorrupt)
	if !reflect.DeepEqual(before, knowledgeFootprint(t, s)) {
		t.Fatal("lost prior history was hidden by original retry")
	}
	saved, err := loadDocumentOperation(context.Background(), s.db, testProject, o.Key)
	if err != nil || !reflect.DeepEqual(o, saved) {
		t.Fatal("lost prior history changed original journal")
	}
	createIssue(t, s, "synthetic-tracker-after-lost-parent-recovery")
}

func TestKnowledgeRecoveryRefCheckCancellationClassification(t *testing.T) {
	for _, phase := range []string{
		"knowledge_layout_head_pair", "knowledge_layout_pin_pair", "knowledge_inventory_pin",
		"knowledge_ref_reuse", "knowledge_head_pin_admission", "knowledge_replay_pin",
		"knowledge_inventory_bound_pin", "knowledge_bound_pin_admission",
	} {
		t.Run(phase, func(t *testing.T) {
			cut, occurrence := "knowledge_document_bound", 1
			switch phase {
			case "knowledge_layout_head_pair":
				cut, occurrence = "knowledge_ref_file", 2
			case "knowledge_ref_reuse":
				cut = "knowledge_ref_file"
			case "knowledge_replay_pin":
				cut = "knowledge_document_acknowledgement"
			}
			s, _, _, _, o := selectedRecoveryFixture(t, "update", cut, occurrence)
			k, err := loadKnowledgeSetup(context.Background(), s.db, testProject, s.instance, s.directory)
			if err != nil {
				t.Fatal("ref interruption setup unavailable")
			}
			g := s.knowledgeRepository(k)
			if phase == "knowledge_layout_pin_pair" {
				if os.WriteFile(filepath.Join(g.directory, "refs", "pins", string(o.ID)+".lock"), []byte(o.commitOID()+"\n"), 0600) != nil {
					t.Fatal("synthetic exact pin pair unavailable")
				}
			}
			before := knowledgeFootprint(t, s)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			g = g.withContext(ctx)
			seen := 0
			g.readCheckpoint = func(at string) {
				if at == phase {
					seen++
					cancel()
				}
			}
			rejectRecoveryWrites(t, s)
			g.fault = s.fault
			switch phase {
			case "knowledge_layout_head_pair", "knowledge_layout_pin_pair":
				_, _, err = g.verifyJournalLayout(&o)
			case "knowledge_inventory_pin", "knowledge_inventory_bound_pin":
				err = s.verifyDocumentInventory(ctx, g)
			case "knowledge_ref_reuse":
				err = g.publishRef(filepath.Join("pins", string(o.ID)), authority.EmptyKnowledgeRef, o.commitOID())
			case "knowledge_head_pin_admission":
				err = g.publishJournalRef(o, true)
			case "knowledge_replay_pin":
				_, err = s.replayDocument(ctx, g, o)
			case "knowledge_bound_pin_admission":
				_, err = s.completeBoundDocument(ctx, g, o)
			}
			if !errors.Is(err, context.Canceled) || seen != 1 {
				t.Fatal("named ref interruption was classified as corruption/recovery")
			}
			if !reflect.DeepEqual(before, knowledgeFootprint(t, s)) {
				t.Fatal("interrupted ref proof changed evidence")
			}
			s.fault = nil
		})
	}
}

func TestKnowledgeRecoveryMalformedBoundPinRetainsRecoveryClassification(t *testing.T) {
	for _, owner := range []string{"inventory", "bound-completion"} {
		t.Run(owner, func(t *testing.T) {
			s, _, _, _, o := selectedRecoveryFixture(t, "update", "knowledge_document_bound", 1)
			k, err := loadKnowledgeSetup(context.Background(), s.db, testProject, s.instance, s.directory)
			if err != nil {
				t.Fatal("malformed pin setup unavailable")
			}
			g := s.knowledgeRepository(k)
			if os.WriteFile(filepath.Join(g.directory, "refs", "pins", string(o.ID)), []byte("partial"), 0600) != nil {
				t.Fatal("synthetic malformed pin unavailable")
			}
			before := knowledgeFootprint(t, s)
			rejectRecoveryWrites(t, s)
			g.fault = s.fault
			if owner == "inventory" {
				err = s.verifyDocumentInventory(context.Background(), g)
			} else {
				_, err = s.completeBoundDocument(context.Background(), g, o)
			}
			requireKnowledgeError(t, err, DocumentRecoveryRequired)
			if !reflect.DeepEqual(before, knowledgeFootprint(t, s)) {
				t.Fatal("malformed bound pin was rewritten")
			}
			s.fault = nil
		})
	}
}

func TestKnowledgeRecoveryRefErrorPreservesInterruptionAndForeignClassification(t *testing.T) {
	for _, interrupted := range []error{context.Canceled, context.DeadlineExceeded, errors.Join(Error{KnowledgeCorrupt}, context.DeadlineExceeded)} {
		if recoveryRefError(interrupted) != interrupted {
			t.Fatal("recovery ref boundary hid an interruption")
		}
	}
	for _, foreign := range []error{Error{KnowledgeCorrupt}, Error{DurabilityUnavailable}} {
		requireKnowledgeError(t, recoveryRefError(foreign), DocumentRecoveryRequired)
	}
}

func TestKnowledgeRecoveryRefPublicationRecheckRetainsForeignEvidence(t *testing.T) {
	for _, phase := range []string{"knowledge_ref_file", "knowledge_ref_rename", "knowledge_owned_ref_file"} {
		t.Run(phase, func(t *testing.T) {
			s, _, _, _, o := selectedRecoveryFixture(t, "update", "knowledge_ref_file", 2)
			k, err := loadKnowledgeSetup(context.Background(), s.db, testProject, s.instance, s.directory)
			if err != nil {
				t.Fatal("ref recheck setup unavailable")
			}
			g := s.knowledgeRepository(k)
			head := filepath.Join(g.directory, "refs", "heads", "knowledge")
			if phase != "knowledge_owned_ref_file" {
				// Keep the exact lock but make the destination unborn, so the
				// primitive's original lock-reuse path reaches both barriers.
				if os.Rename(head, filepath.Join(s.directory, "preserved-parent-ref")) != nil {
					t.Fatal("synthetic ref relocation unavailable")
				}
			}
			fired := 0
			g.fault = func(at string) error {
				if at == phase {
					fired++
					if os.WriteFile(head, []byte("partial"), 0600) != nil {
						t.Fatal("synthetic ref interference unavailable")
					}
				}
				return nil
			}
			if phase == "knowledge_owned_ref_file" {
				err = g.publishJournalRef(o, true)
			} else {
				err = g.publishRef("heads/knowledge", authority.EmptyKnowledgeRef, o.commitOID())
			}
			requireKnowledgeError(t, err, DocumentRecoveryRequired)
			body, readErr := os.ReadFile(head)
			if fired != 1 || readErr != nil || string(body) != "partial" {
				t.Fatal("ref publication overwrote foreign evidence")
			}
			s.fault = nil
		})
	}
}
