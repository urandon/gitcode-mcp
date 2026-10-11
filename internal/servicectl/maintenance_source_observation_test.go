package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/observability"
)

func TestMaintenanceSourceObservationActualRegisterReplayAndRebind(t *testing.T) {
	ctx := context.Background()
	m, path := maintenanceObservationFixture(t)
	entry, err := m.Enroll(ctx, testMaintenanceEnrollRequest(path, "fixture-enroll", MaintenancePolicy{SyncMode: "off"}))
	if err != nil {
		t.Fatal("enrollment unavailable")
	}
	repoA := createRepositoryDocsRegistrationRepo(t, filepath.Join(filepath.Dir(path), "repo-a"), "synthetic-private-source-a")
	repoB := createRepositoryDocsRegistrationRepo(t, filepath.Join(filepath.Dir(path), "repo-b"), "synthetic-private-source-b")
	c := maintenanceEventCollector(t)
	m.jobs.SetObservation(c)
	first, ok, err := m.RegisterRepositoryDocsSource(ctx, entry.CacheUUID, entry.RepoID, repoA, "")
	if err != nil || !ok || first.RepositoryDocs == nil {
		t.Fatal("source registration unavailable")
	}
	id := first.RepositoryDocs.SourceRegistrationID
	if _, ok, err := m.RegisterRepositoryDocsSource(ctx, entry.CacheUUID, entry.RepoID, repoA, ""); err != nil || !ok {
		t.Fatal("source replay changed")
	}
	// Confirmed failure uses the actual owner, not a reader's projection.
	owner, flush := beginMaintenanceObservation(ctx, m.jobs)
	m.updateRepositoryDocsFailure(owner, entry.RegistrationID, id, cloneMaintenanceEntryPrivate(m.entries[entry.RegistrationID]), "git_store_unavailable", "synthetic-private-error")
	flush()
	rebound, err := m.RebindRepositoryDocsSource(ctx, RepositoryDocsSourceRebindRequest{RegistrationID: entry.RegistrationID, SourceRegistrationID: id, ExpectedGeneration: 1, RepositoryPath: repoB})
	if err != nil || rebound.RepositoryDocs == nil || rebound.RepositoryDocs.SourceRegistrationID != id || rebound.RepositoryDocs.SourceRegistrationGeneration != 2 || rebound.RepositoryDocs.State != "registered" {
		t.Fatal("confirmed rebind authority changed")
	}
	// Generation-only reset of an already registered state is not a transition.
	if _, err := m.RebindRepositoryDocsSource(ctx, RepositoryDocsSourceRebindRequest{RegistrationID: entry.RegistrationID, SourceRegistrationID: id, ExpectedGeneration: 2, RepositoryPath: repoA}); err != nil {
		t.Fatal("generation-only rebind unavailable")
	}
	second, ok, err := m.RegisterRepositoryDocsSource(ctx, entry.CacheUUID, entry.RepoID, repoB, "")
	if err != nil || !ok || second.RepositoryDocs == nil || second.RepositoryDocs.SourceRegistrationID == id {
		t.Fatal("distinct second source unavailable")
	}
	events := maintenanceObservationPage(t, c, 4)
	seen := map[string]bool{}
	for _, event := range events {
		if event.MaintenanceScope != "repository_docs" || event.SourceRef == "" || event.RegistrationRef != expectedObservationRef("registration", entry.RegistrationID) || event.JobRef != "" || seen[event.CorrelationRef] {
			t.Fatal("source transition lost distinct authority")
		}
		seen[event.CorrelationRef] = true
		if event.Outcome == "degraded" && (event.PreviousState != "registered" || event.FailureClass != "repository_docs") {
			t.Fatal("source failure contract changed")
		}
		if event.Outcome == "registered" && event.PreviousState == "degraded" && (event.PreviousFailureClass != "repository_docs" || event.SourceRef != expectedObservationRef("source", id) || event.FailureClass != "") {
			t.Fatal("rebind reset lost source identity")
		}
	}
	data, _ := json.Marshal(events)
	for _, private := range []string{path, repoA, repoB, id, second.RepositoryDocs.SourceRegistrationID, "synthetic-private"} {
		if strings.Contains(string(data), private) {
			t.Fatal("private source authority entered diagnostics")
		}
	}
	restart := NewMaintenanceManager(m.manager, m.jobs, m.path)
	c2 := maintenanceEventCollector(t)
	m.jobs.SetObservation(c2)
	if restart.Load() != nil {
		t.Fatal("source restart unavailable")
	}
	if _, err := restart.List(ctx); err != nil {
		t.Fatal("source projection unavailable")
	}
	maintenanceObservationPage(t, c2, 0)
}

func TestMaintenanceSourceObservationActualInspectionFailureRecovery(t *testing.T) {
	m, store, repo, reg, id := inspectionBoundaryFixture(t)
	c := maintenanceEventCollector(t)
	m.jobs.SetObservation(c)
	at := time.Unix(1700000000, 0).UTC()
	m.now = func() time.Time { return at }
	run := func(active Job) {
		t.Helper()
		ctx, flush := beginMaintenanceObservation(context.Background(), m.jobs)
		defer flush()
		snapshot := cloneMaintenanceEntryPrivate(m.entries[reg])
		m.reconcileRepositoryDocsState(ctx, store, &snapshot, at)
		m.finishReconcileEntry(ctx, reg, snapshot, 0, 0, "", "", nil, Job{}, Job{}, active, at)
	}
	parked := repo + "-parked"
	if os.Rename(repo, parked) != nil {
		t.Fatal("private Git failure fixture unavailable")
	}
	run(Job{})
	if os.Rename(parked, repo) != nil {
		t.Fatal("private Git recovery fixture unavailable")
	}
	at = at.Add(time.Minute)
	run(Job{})
	run(Job{}) // repeated successful inspection has no finite pair change
	run(Job{ID: "fixture-active-source", Type: RepositoryDocsIndexJobType, Status: JobStatusQueued,
		CacheUUID: m.entries[reg].CacheUUID, RepoID: m.entries[reg].RepoID, RegistrationID: reg,
		SourceRegistrationID: id, SourceRegistrationGeneration: m.sources[reg][id].State.SourceRegistrationGeneration})
	run(Job{})
	if m.sources[reg][id].State.State != "not_indexed" || m.sources[reg][id].State.Inspection.Result != "succeeded" {
		t.Fatal("actual inspection/indexing owner changed authority")
	}
	// Parent degraded->indexing is distinct from source readiness: inspection
	// recovery does not make a not-indexed source ready. Two parent and four
	// source transitions; separately confirmed health is not duplicated.
	for _, event := range maintenanceObservationPage(t, c, 6) {
		if event.MaintenanceScope == "repository_docs" {
			if event.SourceRef != expectedObservationRef("source", id) {
				t.Fatal("inspection state transition lost source authority")
			}
			if event.Outcome == "not_indexed" && event.PreviousState == "degraded" && (event.PreviousFailureClass != "repository_docs" || event.FailureClass != "") {
				t.Fatal("actual inspection recovery class changed")
			}
		}
	}
}

func TestMaintenanceSourceObservationPersistenceAndSharedBuffer(t *testing.T) {
	for _, mode := range []string{"register_rollback", "admission_rollback", "retained_failure", "rename_readback"} {
		t.Run(mode, func(t *testing.T) {
			m, _, repo, reg, id := inspectionBoundaryFixture(t)
			c := maintenanceEventCollector(t)
			m.jobs.SetObservation(c)
			writes := 0
			m.writeFile = func(name string, data []byte, permissions os.FileMode) error {
				writes++
				if mode == "rename_readback" && durableAtomicWriteFile(name, data, permissions) != nil {
					t.Fatal("confirmed write fixture unavailable")
				}
				return errors.New("synthetic-private-storage-failure")
			}
			ctx, flush := beginMaintenanceObservation(context.Background(), m.jobs)
			switch mode {
			case "register_rollback":
				entry := m.entries[reg]
				if _, _, err := m.RegisterRepositoryDocsSource(ctx, entry.CacheUUID, entry.RepoID, repo, "different-profile"); err == nil || len(m.sources[reg]) != 1 {
					t.Fatal("source registration rollback changed")
				}
			case "admission_rollback":
				cfg := adminFakeRAGConfig()
				cfg.CachePath, cfg.LockPath = m.entries[reg].cachePath, m.entries[reg].cachePath+".lock"
				m.manager.EffectiveConfig = &cfg
				prepared, err := prepareRepositoryDocsIndex(ctx, m.manager, StartRepositoryDocsIndexJobRequest{RepoID: m.entries[reg].RepoID, RepositoryPath: repo, CachePath: cfg.CachePath})
				if err != nil {
					t.Fatal("offline admission rollback preparation unavailable")
				}
				if _, _, ok, err := m.registerAndRecordRepositoryDocsAdmission(ctx, prepared); err == nil || ok || len(m.sources[reg]) != 1 || len(m.admissions) != 0 {
					t.Fatal("source admission rollback changed")
				}
			default:
				m.updateRepositoryDocsFailure(ctx, reg, id, cloneMaintenanceEntryPrivate(m.entries[reg]), "synthetic_private_safe_class", "synthetic-private-body")
				if m.sources[reg][id].State.State != "degraded" {
					t.Fatal("retained source failure behavior changed")
				}
			}
			if writes != 1 {
				t.Fatal("observation added registry I/O")
			}
			buffer := ctx.Value(maintenanceObservationKey{}).(*maintenanceObservationBuffer)
			expected, lost := 0, uint64(0)
			if mode == "rename_readback" {
				expected = 1
			}
			if mode == "retained_failure" {
				lost = 1
			}
			if buffer.count != expected || buffer.lost != lost {
				t.Fatal("source persistence boundary not conserved")
			}
			// Actual unchanged later confirmation does not resurrect a missing
			// transition; scope shares the existing registration capture boundary.
			m.writeFile = nil
			if mode == "retained_failure" || mode == "rename_readback" {
				m.updateRepositoryDocsFailure(ctx, reg, id, cloneMaintenanceEntryPrivate(m.entries[reg]), "synthetic_private_safe_class", "changed-private-body")
			}
			flush()
			events := maintenanceObservationPage(t, c, expected+int(lost))
			for _, event := range events {
				if event.MaintenanceScope != "repository_docs" || event.FailureClass != "operation_failed" {
					t.Fatal("source persisted raw safe-looking failure class")
				}
			}
		})
	}
}

func TestMaintenanceSourceObservationActualAdmissionAndIndexCompletion(t *testing.T) {
	ctx := context.Background()
	m, path := maintenanceObservationFixture(t)
	cfg := adminFakeRAGConfig()
	cfg.CachePath, cfg.LockPath = path, path+".lock"
	m.manager = Manager{EffectiveConfig: &cfg}
	req := testMaintenanceEnrollRequest(path, "fixture-enroll", MaintenancePolicy{SyncMode: "off"})
	req.ConfigSnapshot, req.ConfigHash = cfg, maintenanceHash(cfg)
	entry, err := m.Enroll(ctx, req)
	if err != nil {
		t.Fatal("fake-provider enrollment unavailable")
	}
	repo := createRepositoryDocsRegistrationRepo(t, filepath.Join(filepath.Dir(path), "repo"), "synthetic-private-index-source")
	c := maintenanceEventCollector(t)
	m.jobs.SetObservation(c)
	ctx, flush := beginMaintenanceObservation(ctx, m.jobs)
	// Retain the actual operation-local maintenance sink while disabling the
	// independent job sink. This keeps loss conservation specific to this
	// cohort; unrelated provider/job admission loss must not count as a missing
	// maintenance transition. Workers must not inherit this private carrier.
	m.jobs.SetObservation(nil)
	prepared, err := prepareRepositoryDocsIndex(ctx, m.manager, StartRepositoryDocsIndexJobRequest{RepoID: entry.RepoID, RepositoryPath: repo, CachePath: path})
	if err != nil {
		t.Fatal("offline source admission preparation unavailable")
	}
	registered, prepared, ok, err := m.registerAndRecordRepositoryDocsAdmission(ctx, prepared)
	if err != nil || !ok || registered.RepositoryDocs == nil {
		t.Fatal("durable source admission unavailable")
	}
	id := registered.RepositoryDocs.SourceRegistrationID
	if _, _, ok, err := m.registerAndRecordRepositoryDocsAdmission(ctx, prepared); err != nil || !ok {
		t.Fatal("durable admission replay changed")
	}
	// Exercise the actual scheduler, fake embedding worker and confirmed cache
	// revision set, not a fabricated ready projection or terminal status.
	result, err := m.ReconcileRegistration(ctx, entry.RegistrationID)
	if err != nil || len(result.JobsStarted) != 1 {
		t.Fatal("source admission did not start actual indexing")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		job, exists := m.jobs.Get(result.JobsStarted[0])
		if exists && jobTerminalStatus(job.Status) {
			if job.Status != JobStatusSucceeded {
				t.Fatal("actual offline source index failed")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("actual offline source index did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	result, err = m.ReconcileRegistration(ctx, entry.RegistrationID)
	if err != nil || len(result.JobsStarted) != 0 || len(result.Entries) != 1 || result.Entries[0].RepositoryDocs.State != "ready" {
		t.Fatal("confirmed source index completion not reconciled")
	}
	if _, err := m.ReconcileRegistration(ctx, entry.RegistrationID); err != nil {
		t.Fatal("ready source replay unavailable")
	}
	buffer := ctx.Value(maintenanceObservationKey{}).(*maintenanceObservationBuffer)
	if buffer.count != 5 || buffer.lost != 0 {
		t.Fatal("actual source pipeline captures not conserved")
	}
	flush()
	sealJobObservation(t, c)
	page, err := c.Query(observability.Filter{Code: observability.MaintenanceStateChanged}, "", 100)
	if err != nil || page.HasMore {
		t.Fatal("bounded source completion query unavailable")
	}
	sourceEvents := 0
	for _, event := range page.Events {
		if event.MaintenanceScope == "repository_docs" {
			sourceEvents++
			if event.SourceRef != expectedObservationRef("source", id) || event.FailureClass != "" {
				t.Fatal("completed source cohort lost safe correlation")
			}
			switch event.Outcome {
			case "registered":
				if event.PreviousState != "absent" {
					t.Fatal("admission registration repeated")
				}
			case "indexing":
				if event.PreviousState != "registered" {
					t.Fatal("actual indexing transition changed")
				}
			case "ready":
				if event.PreviousState != "indexing" {
					t.Fatal("confirmed readiness transition changed")
				}
			default:
				t.Fatal("source pipeline produced invented finite state")
			}
		}
	}
	// Two parent transitions and three source transitions. Optional job telemetry
	// stayed disabled without changing real indexing or cache confirmation.
	if len(page.Events) > 5 || uint64(len(page.Events))+page.Coverage.Dropped != 5 || sourceEvents > 3 {
		t.Fatal("actual source pipeline boundaries not conserved")
	}
}

func TestMaintenanceSourceObservationStaleSnapshotAndSharedOverflow(t *testing.T) {
	m, _, _, reg, id := inspectionBoundaryFixture(t)
	c := maintenanceEventCollector(t)
	m.jobs.SetObservation(c)
	ctx, flush := beginMaintenanceObservation(context.Background(), m.jobs)
	snapshot := cloneMaintenanceEntryPrivate(m.entries[reg])
	snapshot.RepositoryDocs.State = "ready"
	snapshot.RepositoryDocs.SourceRegistrationGeneration--
	before := m.sources[reg][id].State
	m.finishReconcileEntry(ctx, reg, snapshot, 0, 0, "", "", nil, Job{}, Job{}, Job{}, time.Now())
	if m.sources[reg][id].State != before {
		t.Fatal("stale completion changed current source authority")
	}
	buffer := ctx.Value(maintenanceObservationKey{}).(*maintenanceObservationBuffer)
	if buffer.count != 0 || buffer.lost != 0 {
		t.Fatal("rejected stale source callback manufactured an event or loss")
	}
	// A qualified current finish supplies the real enrolled->indexing parent
	// transition; obsolete work can no longer mutate even the parent state.
	m.finishReconcileEntry(ctx, reg, cloneMaintenanceEntryPrivate(m.entries[reg]), 0, 0, "", "", nil, Job{}, Job{}, Job{}, time.Now().UTC())
	if buffer.count != 1 || buffer.captures[0].scope != "registration" {
		t.Fatal("current completion lost parent observation")
	}
	entry, source := m.entries[reg], m.sources[reg][id]
	for i := 0; i < maxMaintenanceObservations+5; i++ {
		captureMaintenanceSourceObservation(ctx, maintenanceObservationState{state: "absent"}, entry, source, true)
	}
	if buffer.count != maxMaintenanceObservations || buffer.lost != 6 {
		t.Fatal("source and registration did not share bounded capture")
	}
	flush()
	maintenanceObservationPage(t, c, maxMaintenanceObservations+6)
}

func TestMaintenanceSourceObservationActualObsoleteJobAndCurrentControl(t *testing.T) {
	for _, stale := range []bool{false, true} {
		name := "current"
		if stale {
			name = "obsolete"
		}
		t.Run(name, func(t *testing.T) {
			m, _, repo, reg, id := inspectionBoundaryFixture(t)
			m.jobs.snapshotPath = filepath.Join(filepath.Dir(m.path), "jobs.json")
			snapshot := cloneMaintenanceEntryPrivate(m.entries[reg])
			active, created, err := m.jobs.createCoalescedJobWithIntent(RepositoryDocsIndexJobType, snapshot.RepoID, "", 0,
				"fixture-old-source", snapshot.CacheUUID, reg, "fixture-namespace",
				JobRecoveryIntent{SourceRegistrationID: id, SourceRegistrationGeneration: 1, ExpectedRevisionSetID: "fixture-old-set"}, func() {})
			if err != nil || !created || active.Status != JobStatusQueued {
				t.Fatal("actual queued source intent unavailable")
			}
			replacement := createRepositoryDocsRegistrationRepo(t, repo+"-replacement", "fixture-replacement")
			if _, err := m.RebindRepositoryDocsSource(context.Background(), RepositoryDocsSourceRebindRequest{RegistrationID: reg, SourceRegistrationID: id, ExpectedGeneration: 1, RepositoryPath: replacement}); err != nil {
				t.Fatal("actual source rebind unavailable")
			}
			fenced, ok := m.jobs.Get(active.ID)
			if !ok || fenced.Status != JobStatusSuperseded || m.sources[reg][id].State.SourceRegistrationGeneration != 2 || m.sources[reg][id].State.State != "registered" {
				t.Fatal("actual old-generation fence unavailable")
			}
			// Acknowledge the cancelled starter's existing writer reservation;
			// retain the actual pre-fence queued copy for the obsolete callback.
			m.jobs.markWorkerFinished(active.ID)
			if !stale {
				snapshot = cloneMaintenanceEntryPrivate(m.entries[reg])
				active, created, err = m.jobs.createCoalescedJobWithIntent(RepositoryDocsIndexJobType, snapshot.RepoID, "", 0,
					"fixture-current-source", snapshot.CacheUUID, reg, "fixture-namespace",
					JobRecoveryIntent{SourceRegistrationID: id, SourceRegistrationGeneration: 2, ExpectedRevisionSetID: "fixture-current-set"}, func() {})
				if err != nil || !created || active.Status != JobStatusQueued {
					t.Fatal("actual current-generation intent unavailable")
				}
			}
			c := maintenanceEventCollector(t)
			m.jobs.SetObservation(c)
			before := cloneMaintenanceEntryPrivate(m.entries[reg])
			beforeSource := *m.sources[reg][id]
			writes := 0
			m.writeFile = func(name string, data []byte, permissions os.FileMode) error {
				writes++
				return durableAtomicWriteFile(name, data, permissions)
			}
			ctx, flush := beginMaintenanceObservation(context.Background(), m.jobs)
			defer flush()
			updated := m.finishReconcileEntry(ctx, reg, snapshot, 0, 0, "", "", nil, Job{}, Job{}, active, time.Now().UTC())
			if stale {
				if !reflect.DeepEqual(updated, cloneMaintenanceEntry(&before)) {
					t.Fatal("obsolete completion returned stale state")
				}
				assertObsoleteSourceCallbackUnchanged(t, m, reg, id, before, beforeSource, writes)
			} else if updated.State != "indexing" || m.sources[reg][id].State.State != "indexing" || m.sources[reg][id].State.SourceRegistrationGeneration != 2 || writes == 0 {
				t.Fatal("current source completion did not apply")
			}
			assertMaintenanceSourceAuthorityCaptures(t, ctx, stale)
			// Unchanged replay must not retroactively certify or recount the pair.
			m.finishReconcileEntry(ctx, reg, snapshot, 0, 0, "", "", nil, Job{}, Job{}, active, time.Now().UTC())
			assertMaintenanceSourceAuthorityCaptures(t, ctx, stale)
			if stale {
				assertObsoleteSourceCallbackUnchanged(t, m, reg, id, before, beforeSource, writes)
			}
			assertSourceAuthorityRestart(t, m, reg, id, *m.sources[reg][id])
		})
	}
}

func TestMaintenanceSourceObservationActualLateFailureAndCurrentControl(t *testing.T) {
	for _, stale := range []bool{false, true} {
		name := "current"
		if stale {
			name = "obsolete"
		}
		t.Run(name, func(t *testing.T) {
			m, _, repo, reg, id := inspectionBoundaryFixture(t)
			c := maintenanceEventCollector(t)
			m.jobs.SetObservation(c)
			entered, release := make(chan struct{}), make(chan struct{})
			var first atomic.Bool
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			// The existing clock boundary follows reconcile's snapshot/unlock
			// and precedes actual local-only source scheduling.
			m.now = func() time.Time {
				if first.CompareAndSwap(false, true) {
					close(entered)
					<-release
				}
				return time.Now().UTC()
			}
			ctx, flush := beginMaintenanceObservation(context.Background(), m.jobs)
			defer flush()
			done := make(chan MaintenanceEntry, 1)
			go func() { entry, _ := m.reconcileEntry(ctx, reg); done <- entry }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("actual reconcile snapshot barrier unavailable")
			}
			if stale {
				replacement := createRepositoryDocsRegistrationRepo(t, repo+"-replacement", "fixture-replacement")
				if _, err := m.RebindRepositoryDocsSource(context.Background(), RepositoryDocsSourceRebindRequest{RegistrationID: reg, SourceRegistrationID: id, ExpectedGeneration: 1, RepositoryPath: replacement}); err != nil {
					t.Fatal("actual source rebind unavailable")
				}
			}
			before := cloneMaintenanceEntryPrivate(m.entries[reg])
			beforeSource := *m.sources[reg][id]
			var writes atomic.Int64
			m.writeFile = func(name string, data []byte, permissions os.FileMode) error {
				writes.Add(1)
				return durableAtomicWriteFile(name, data, permissions)
			}
			unblock()
			select {
			case updated := <-done:
				if stale {
					if !reflect.DeepEqual(updated, cloneMaintenanceEntry(&before)) {
						t.Fatal("obsolete failure returned stale state")
					}
				} else if updated.RepositoryDocs == nil || updated.RepositoryDocs.State != "degraded" {
					t.Fatal("current source failure did not apply")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("actual source scheduling failure did not unwind")
			}
			if stale {
				assertObsoleteSourceCallbackUnchanged(t, m, reg, id, before, beforeSource, int(writes.Load()))
			} else if m.sources[reg][id].State.State != "degraded" || m.sources[reg][id].State.LastErrorClass != "repository_docs_provider_boundary_blocked" || writes.Load() == 0 {
				t.Fatal("actual local-only scheduling boundary unavailable")
			}
			assertMaintenanceSourceAuthorityCaptures(t, ctx, stale)
			assertSourceAuthorityRestart(t, m, reg, id, *m.sources[reg][id])
		})
	}
}

func assertMaintenanceSourceAuthorityCaptures(t *testing.T, ctx context.Context, stale bool) {
	t.Helper()
	buffer := ctx.Value(maintenanceObservationKey{}).(*maintenanceObservationBuffer)
	sourceCaptures := 0
	for i := 0; i < buffer.count; i++ {
		if buffer.captures[i].scope == "repository_docs" {
			sourceCaptures++
		}
	}
	expected, lost := 1, uint64(0)
	if stale {
		expected = 0
	}
	if sourceCaptures != expected || buffer.lost != lost {
		t.Fatal("source confirmation or unchanged obsolete callback accounting incorrect")
	}
}

func assertObsoleteSourceCallbackUnchanged(t *testing.T, m *MaintenanceManager, reg, id string, before MaintenanceEntry, beforeSource repositoryDocsRegisteredSource, writes int) {
	t.Helper()
	if writes != 0 || !reflect.DeepEqual(cloneMaintenanceEntryPrivate(m.entries[reg]), before) || *m.sources[reg][id] != beforeSource {
		t.Fatal("obsolete callback changed replacement authority or persisted state")
	}
	list, err := m.List(context.Background())
	if err != nil || len(list.Entries) != 1 || !reflect.DeepEqual(list.Entries[0], cloneMaintenanceEntry(&before)) {
		t.Fatal("public maintenance projection changed after obsolete callback")
	}
}

func assertSourceAuthorityRestart(t *testing.T, m *MaintenanceManager, reg, id string, want repositoryDocsRegisteredSource) {
	t.Helper()
	reloaded := NewMaintenanceManager(m.manager, NewJobManager(""), m.path)
	if reloaded.Load() != nil {
		t.Fatal("durable source authority changed across restart")
	}
	// Existing registry canonicalization may redirect a rebound source's
	// original id to the id of its replacement Git/worktree/profile authority.
	// Require that exact redirect, not an arbitrary substitute source.
	canonicalID := reloaded.resolveSourceRegistrationIDLocked(id)
	if canonicalID != repositoryDocsSourceRegistrationID(reg, want.State.GitStoreRef, want.State.WorktreeRef, want.Profile) || reloaded.sources[reg][canonicalID] == nil {
		t.Fatal("durable source redirect does not match replacement authority")
	}
	want.State.SourceRegistrationID = canonicalID
	got := *reloaded.sources[reg][canonicalID]
	if got != want {
		changed := []string{}
		if got.RepositoryPath != want.RepositoryPath {
			changed = append(changed, "path")
		}
		if got.Profile != want.Profile {
			changed = append(changed, "profile")
		}
		a, b := reflect.ValueOf(got.State), reflect.ValueOf(want.State)
		for i := 0; i < a.NumField(); i++ {
			if !reflect.DeepEqual(a.Field(i).Interface(), b.Field(i).Interface()) {
				changed = append(changed, a.Type().Field(i).Name)
			}
		}
		t.Fatalf("durable source authority changed across restart: fields %s", strings.Join(changed, ","))
	}
}

func TestMaintenanceSourceObservationFlushAfterRebindFence(t *testing.T) {
	m, _, repo, reg, id := inspectionBoundaryFixture(t)
	owner, flush := beginMaintenanceObservation(context.Background(), m.jobs)
	m.updateRepositoryDocsFailure(owner, reg, id, cloneMaintenanceEntryPrivate(m.entries[reg]), "git_store_unavailable", "fixture-error")
	flush()
	replacement := createRepositoryDocsRegistrationRepo(t, repo+"-replacement", "synthetic-private-replacement")
	c := maintenanceEventCollector(t)
	m.jobs.SetObservation(c)
	written := make(chan struct{})
	var notified sync.Once
	m.writeFile = func(name string, data []byte, permissions os.FileMode) error {
		err := durableAtomicWriteFile(name, data, permissions)
		notified.Do(func() { close(written) })
		return err
	}
	// Hold the actual source-generation fence's job authority mutex. The
	// registry save/unlock may complete, but diagnostics must wait for that
	// enclosing execution boundary to unwind as well.
	m.jobs.mu.Lock()
	var released sync.Once
	release := func() { released.Do(m.jobs.mu.Unlock) }
	defer release()
	done := make(chan error, 1)
	go func() {
		_, err := m.RebindRepositoryDocsSource(context.Background(), RepositoryDocsSourceRebindRequest{RegistrationID: reg, SourceRegistrationID: id, ExpectedGeneration: 1, RepositoryPath: replacement})
		done <- err
	}()
	finish := func() {
		t.Helper()
		release()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal("actual rebind failed")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("actual rebind fence did not unwind")
		}
	}
	select {
	case <-written:
	case <-time.After(5 * time.Second):
		finish()
		t.Fatal("actual rebind save did not enter")
	}
	deadline := time.Now().Add(5 * time.Second)
	for !m.mu.TryLock() {
		if time.Now().After(deadline) {
			finish()
			t.Fatal("actual rebind registry lock did not unwind")
		}
		time.Sleep(time.Millisecond)
	}
	state := m.sources[reg][id].State.State
	m.mu.Unlock()
	// Establish that the FIFO collector has consumed everything admitted before
	// this marker. A single empty query alone could merely race its async writer.
	marker := observability.KnownRef("correlation", "fixture-held-generation-fence")
	if !c.Emit(observability.RecoveryFinished, observability.Context{Correlation: marker}) {
		finish()
		t.Fatal("collector fence marker not admitted")
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		markers, err := c.Query(observability.Filter{Code: observability.RecoveryFinished}, "", 10)
		if err != nil {
			finish()
			t.Fatal("collector fence marker query unavailable")
		}
		if len(markers.Events) == 1 {
			break
		}
		if time.Now().After(deadline) {
			finish()
			t.Fatal("collector fence marker not persisted")
		}
		time.Sleep(time.Millisecond)
	}
	page, err := c.Query(observability.Filter{Code: observability.MaintenanceStateChanged}, "", 10)
	finish()
	if state != "registered" || err != nil || len(page.Events) != 0 || page.Coverage.Dropped != 0 {
		t.Fatal("source observation crossed held generation fence")
	}
	maintenanceObservationPage(t, c, 1)
}
