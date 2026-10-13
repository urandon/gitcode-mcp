package localauthority

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
)

func recoveryQueueFixture(t *testing.T) (*recoveryQueue, documentOperation) {
	t.Helper()
	s, registry, dir, _, o := selectedRecoveryFixture(t, "update", "knowledge_document_bound", 1)
	if s.Close() != nil {
		t.Fatal("recovery queue original owner did not close")
	}
	p, err := registry.PrepareRuntime()
	if err != nil {
		t.Fatal("recovery queue preparation unavailable")
	}
	c, err := newStrictCoordinator(context.Background(), p, []ProjectBinding{{testProject, dir}}, false)
	if err != nil {
		t.Fatal("recovery queue coordinator unavailable")
	}
	q := c.projects[testProject].jobs
	if q == nil {
		_ = c.Close(context.Background())
		_ = p.Close()
		t.Fatal("recovery queue namespace unavailable")
	}
	t.Cleanup(func() { _ = q.close(); _ = c.Close(context.Background()); _ = p.Close() })
	return q, o
}

func TestRecoveryJobStoreNamespaceOwnershipAndStageCaps(t *testing.T) {
	q, o := recoveryQueueFixture(t)
	if other, err := newRecoveryQueue(context.Background(), q.store, nil); err == nil {
		_ = other.close()
		t.Fatal("second metadata writer admitted for one prepared store")
	}
	root := filepath.Join(q.store.directory, recoveryJobsDirectory)
	for i := 0; i < MaxRecoveryJobStages; i++ {
		id, err := authority.NewUUID()
		if err != nil || os.WriteFile(filepath.Join(root, "stage-"+string(id)+".json"), nil, 0600) != nil {
			t.Fatal("bounded stage fixture failed")
		}
	}
	count, total, err := q.stages(context.Background())
	if err != nil || count != MaxRecoveryJobStages || total != 0 {
		t.Fatal("stage accounting lost zero-byte evidence")
	}
	before, _, _, _ := q.readState()
	requireKnowledgeError(t, q.publish(context.Background(), recoveryQueuedState(t, q, o)), Capacity)
	after, _, _, err := q.readState()
	if err != nil || string(before) != string(after) {
		t.Fatal("stage capacity erased acknowledged state")
	}
	count, _, err = q.stages(context.Background())
	if err != nil || count != MaxRecoveryJobStages {
		t.Fatal("stage capacity deleted uncertain evidence")
	}
}

func TestRecoveryJobStoreUnsafeMetadataTypesPreserved(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "public-mode", "truncated", "oversized", "unknown-key"} {
		t.Run(kind, func(t *testing.T) {
			q, _ := recoveryQueueFixture(t)
			path := filepath.Join(q.store.directory, recoveryJobsDirectory, "state.json")
			if q.close() != nil {
				t.Fatal("unsafe metadata fixture did not close")
			}
			switch kind {
			case "symlink":
				preserved := filepath.Join(q.store.directory, "preserved-job-state")
				if os.Rename(path, preserved) != nil || os.Symlink(preserved, path) != nil {
					t.Fatal("synthetic symlink fixture failed")
				}
			case "hardlink":
				if os.Link(path, filepath.Join(q.store.directory, "preserved-job-state")) != nil {
					t.Fatal("synthetic hardlink fixture failed")
				}
			case "public-mode":
				if os.Chmod(path, 0644) != nil {
					t.Fatal("synthetic metadata mode fixture failed")
				}
			case "truncated":
				if os.WriteFile(path, []byte("{"), 0600) != nil {
					t.Fatal("synthetic truncated metadata failed")
				}
			case "oversized":
				if os.WriteFile(path, []byte(strings.Repeat("x", MaxRecoveryJobSnapshotBytes+1)), 0600) != nil {
					t.Fatal("synthetic oversized metadata failed")
				}
			case "unknown-key":
				if os.WriteFile(path, []byte(`{"untrusted":true}`), 0600) != nil {
					t.Fatal("synthetic unknown metadata failed")
				}
			}
			before, _ := os.ReadFile(path)
			if next, err := newRecoveryQueue(context.Background(), q.store, nil); err == nil {
				_ = next.close()
				t.Fatal("unsafe metadata was admitted")
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("unsafe metadata evidence was overwritten")
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatal("unsafe metadata was deleted")
			}
			createIssue(t, q.store, "synthetic-tracker-after-unsafe-job-type")
		})
	}
}

func recoveryQueuedState(t *testing.T, q *recoveryQueue, o documentOperation) recoveryJobSnapshot {
	t.Helper()
	next, err := q.nextState()
	if err != nil {
		t.Fatal("recovery queue next state unavailable")
	}
	stamp := time.Now().UTC().Format(time.RFC3339Nano)
	r := recoveryJobRecord{View: RecoveryJobView{Schema: 1, ProjectID: testProject, JobID: o.ID, Type: "document_recovery", State: "queued", Generation: 1, ObservedJournalGeneration: o.Generation, CreatedAt: stamp, UpdatedAt: stamp}, IntentHash: o.Intent, ActorHash: hash([]byte("synthetic job actor")), AdmittedGeneration: next.Generation}
	next.Jobs = append(next.Jobs, r)
	return next
}

func TestRecoveryJobStoreRoundTripAndNoSourceBytes(t *testing.T) {
	q, o := recoveryQueueFixture(t)
	next := recoveryQueuedState(t, q, o)
	if q.publish(context.Background(), next) != nil {
		t.Fatal("durable recovery job admission failed")
	}
	body, exists, _, err := q.readState()
	if err != nil || !exists {
		t.Fatal("durable recovery job missing")
	}
	for _, forbidden := range []string{"# selected-recovery-Git-only-sentinel", "synthetic-selected-original-key", q.store.directory} {
		if strings.Contains(string(body), forbidden) {
			t.Fatal("job metadata retained private source content/coordinate")
		}
	}
	if q.close() != nil {
		t.Fatal("recovery queue did not close")
	}
	reopened, err := newRecoveryQueue(context.Background(), q.store, nil)
	if err != nil {
		t.Fatal("verified job owner reopen unavailable")
	}
	defer reopened.close()
	if !reflect.DeepEqual(next, reopened.state) || reopened.fileHash != hash(body) || reopened.fileIdentity == "" {
		t.Fatal("reopen changed acknowledged job intent")
	}
	saved, err := loadDocumentOperation(context.Background(), q.store.db, testProject, o.Key)
	if err != nil || !reflect.DeepEqual(o, saved) {
		t.Fatal("metadata publication changed source authority")
	}
}

func TestRecoveryJobStorePublicationCutsAndReopen(t *testing.T) {
	for _, phase := range []string{"jobs_stage_write", "jobs_stage_file", "jobs_stage_directory", "jobs_state_rename", "jobs_state_directory", "jobs_state_acknowledgement"} {
		t.Run(phase, func(t *testing.T) {
			q, o := recoveryQueueFixture(t)
			before, exists, _, err := q.readState()
			if err != nil || !exists {
				t.Fatal("original job state unavailable")
			}
			next := recoveryQueuedState(t, q, o)
			stop := errors.New("synthetic job publication interruption")
			q.fault = func(at string) error {
				if at == phase {
					return stop
				}
				return nil
			}
			if !errors.Is(q.publish(context.Background(), next), stop) {
				t.Fatal("job publication cut not reached")
			}
			published := strings.HasPrefix(phase, "jobs_state_")
			if q.state.Generation != 1 || published != (q.fence == JobsUncertain) {
				t.Fatal("uncertain admission was acknowledged in memory")
			}
			body, _, _, err := q.readState()
			if err != nil || !published && string(body) != string(before) {
				t.Fatal("unpublished stage replaced canonical job intent")
			}
			q.fault = nil
			if q.close() != nil {
				t.Fatal("interrupted queue did not close")
			}
			reopened, err := newRecoveryQueue(context.Background(), q.store, nil)
			if err != nil {
				t.Fatal("job owner could not requalify publication")
			}
			defer reopened.close()
			want := uint64(1)
			if published {
				want = 2
			}
			if reopened.state.Generation != want {
				t.Fatal("reopen selected an unpublished stage by recency")
			}
			count, _, err := reopened.stages(context.Background())
			if err != nil || !published && count != 1 || published && count != 0 {
				t.Fatal("uncertain job staging evidence was removed")
			}
			saved, err := loadDocumentOperation(context.Background(), q.store.db, testProject, o.Key)
			if err != nil || !reflect.DeepEqual(o, saved) {
				t.Fatal("job fault mutated original journal")
			}
			createIssue(t, q.store, "synthetic-independent-tracker-after-job-fault")
		})
	}
}

func TestRecoveryJobStoreForeignFinalAndStagePreserved(t *testing.T) {
	for _, mode := range []string{"final-content", "final-same-byte-replacement", "stage-content", "stage-replacement"} {
		t.Run(mode, func(t *testing.T) {
			q, o := recoveryQueueFixture(t)
			root := filepath.Join(q.store.directory, recoveryJobsDirectory)
			final := filepath.Join(root, "state.json")
			original, _ := os.ReadFile(final)
			fired := false
			q.fault = func(at string) error {
				if at != "jobs_stage_directory" {
					return nil
				}
				fired = true
				path := final
				if strings.HasPrefix(mode, "stage-") {
					entries, err := os.ReadDir(root)
					if err != nil {
						t.Fatal("synthetic stage inspection failed")
					}
					for _, e := range entries {
						if strings.HasPrefix(e.Name(), "stage-") {
							path = filepath.Join(root, e.Name())
						}
					}
					if path == final {
						t.Fatal("synthetic candidate not found")
					}
				}
				if strings.HasSuffix(mode, "replacement") {
					if os.Rename(path, filepath.Join(q.store.directory, "preserved-job-file")) != nil || os.WriteFile(path, original, 0600) != nil {
						t.Fatal("synthetic physical replacement failed")
					}
				} else if os.WriteFile(path, []byte("foreign"), 0600) != nil {
					t.Fatal("synthetic foreign metadata failed")
				}
				return nil
			}
			err := q.publish(context.Background(), recoveryQueuedState(t, q, o))
			requireKnowledgeError(t, err, JobsCorrupt)
			if !fired || q.state.Generation != 1 {
				t.Fatal("foreign-state refusal missed publication boundary")
			}
			q.fault = nil
			body, _ := os.ReadFile(final)
			want := string(original)
			if mode == "final-content" {
				want = "foreign"
			}
			if string(body) != want {
				t.Fatal("foreign final was overwritten")
			}
			createIssue(t, q.store, "synthetic-tracker-after-foreign-job-state")
		})
	}
}

func TestRecoveryJobStoreRefusesUnpreparedAndMissingExistingState(t *testing.T) {
	s, _, dir := readyDocumentStore(t)
	if _, err := newRecoveryQueue(context.Background(), s, nil); err == nil {
		t.Fatal("ordinary writer became prepared job owner")
	}
	if _, err := os.Lstat(filepath.Join(dir, recoveryJobsDirectory)); !os.IsNotExist(err) {
		t.Fatal("unprepared refusal created job state")
	}
	q, _ := recoveryQueueFixture(t)
	final := filepath.Join(q.store.directory, recoveryJobsDirectory, "state.json")
	if q.close() != nil || os.Rename(final, filepath.Join(q.store.directory, "preserved-original-jobs")) != nil {
		t.Fatal("synthetic missing-final fixture failed")
	}
	_, err := newRecoveryQueue(context.Background(), q.store, nil)
	requireKnowledgeError(t, err, JobsUncertain)
	if _, err = os.Lstat(final); !os.IsNotExist(err) {
		t.Fatal("missing existing job history was reinitialized")
	}
}

func TestRecoveryJobStoreLifecycleAndIntentErasureRefusal(t *testing.T) {
	q, o := recoveryQueueFixture(t)
	if q.publish(context.Background(), recoveryQueuedState(t, q, o)) != nil {
		t.Fatal("lifecycle fixture admission failed")
	}
	for _, change := range []string{"erase", "intent", "actor", "attempt", "generation", "success-without-proof"} {
		next, err := q.nextState()
		if err != nil {
			t.Fatal("lifecycle fixture state unavailable")
		}
		switch change {
		case "erase":
			next.Jobs = []recoveryJobRecord{}
		case "intent":
			next.Jobs[0].IntentHash = strings.Repeat("a", 64)
		case "actor":
			next.Jobs[0].ActorHash = strings.Repeat("a", 64)
		case "attempt":
			next.Jobs[0].View.Attempts = 2
		case "generation":
			next.Jobs[0].View.Generation++
		case "success-without-proof":
			next.Jobs[0].View.State = "succeeded"
			next.Jobs[0].View.Generation++
		}
		requireKnowledgeError(t, q.publish(context.Background(), next), JobsCorrupt)
		if q.state.Generation != 2 || len(q.state.Jobs) != 1 {
			t.Fatal("unresolved intent was erased/changed")
		}
	}
	next, _ := q.nextState()
	next.Jobs[0].View.State = "running"
	next.Jobs[0].View.Generation++
	next.Jobs[0].View.Attempts++
	if q.publish(context.Background(), next) != nil {
		t.Fatal("durable actual attempt rejected")
	}
	next, _ = q.nextState()
	r := &next.Jobs[0]
	r.CancelFrom = r.View.Generation
	r.ActionActorHash = hash([]byte("synthetic cancellation actor"))
	r.View.Generation++
	r.View.State = "cancelling"
	if q.publish(context.Background(), next) != nil {
		t.Fatal("durable cancellation rejected")
	}
	next, _ = q.nextState()
	r = &next.Jobs[0]
	r.View.Generation++
	r.View.State = "cancelled"
	r.View.Code = "cancelled"
	if q.publish(context.Background(), next) != nil {
		t.Fatal("joined pending cancellation rejected")
	}
	next, _ = q.nextState()
	next.Jobs = []recoveryJobRecord{}
	requireKnowledgeError(t, q.publish(context.Background(), next), JobsCorrupt)
	next, _ = q.nextState()
	r = &next.Jobs[0]
	r.RetryFrom = r.View.Generation
	r.View.Generation++
	r.CancelFrom = 0
	r.View.State = "queued"
	r.View.Code = ""
	if q.publish(context.Background(), next) != nil {
		t.Fatal("explicit generation-fenced retry rejected")
	}
}
