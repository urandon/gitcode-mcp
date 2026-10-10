package servicectl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
)

func inspectionBoundaryFixture(t *testing.T) (*MaintenanceManager, *cache.SQLiteStore, string, string, string) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatal("cache fixture unavailable")
	}
	t.Cleanup(func() { _ = store.Close() })
	if store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}) != nil {
		t.Fatal("binding fixture unavailable")
	}
	identity, _ := store.CacheIdentity(ctx)
	repo := createRepositoryDocsRegistrationRepo(t, filepath.Join(root, "repository"), "synthetic-source")
	manager := newTestManager(t, "darwin")
	cfg := adminFakeRAGConfig()
	cfg.CachePath, cfg.LockPath = path, path+".lock"
	provider := cfg.RAG.Providers["fake"]
	provider.DataBoundary = "unknown"
	cfg.RAG.Providers["fake"] = provider
	manager.EffectiveConfig = &cfg
	m := NewMaintenanceManager(manager, NewJobManager(""), filepath.Join(root, "registry.json"))
	enroll := testMaintenanceEnrollRequest(path, "fixture-enroll", MaintenancePolicy{SyncMode: "off"})
	enroll.ConfigSnapshot, enroll.ConfigHash = cfg, maintenanceHash(cfg)
	entry, err := m.Enroll(ctx, enroll)
	if err != nil {
		t.Fatal("enrollment fixture unavailable")
	}
	registered, ok, err := m.RegisterRepositoryDocsSource(ctx, identity.UUID, entry.RepoID, repo, "")
	if err != nil || !ok {
		t.Fatal("source fixture unavailable")
	}
	return m, store, repo, entry.RegistrationID, registered.RepositoryDocs.SourceRegistrationID
}

func TestSourceInspectionFailedPersistenceCannotClaimRetainedRecovery(t *testing.T) {
	m, store, repo, reg, source := inspectionBoundaryFixture(t)
	at := time.Unix(1700000000, 0).UTC()
	m.now = func() time.Time { return at }
	parked := repo + "-parked"
	if os.Rename(repo, parked) != nil {
		t.Fatal("Git failure fixture unavailable")
	}
	run := func() {
		snapshot := cloneMaintenanceEntryPrivate(m.entries[reg])
		m.reconcileRepositoryDocsState(context.Background(), store, &snapshot, at)
		m.finishReconcileEntry(context.Background(), reg, snapshot, 0, 0, "", "", nil, Job{}, Job{}, Job{}, at)
	}
	run()
	if m.sources[reg][source].State.Inspection.Result != "failed" {
		t.Fatal("actual inspection failure missing")
	}
	if os.Rename(parked, repo) != nil {
		t.Fatal("Git recovery fixture unavailable")
	}
	at = at.Add(time.Minute)
	m.writeFile = func(string, []byte, os.FileMode) error { return errors.New("synthetic storage failure") }
	run()
	reloaded := NewMaintenanceManager(m.manager, NewJobManager(""), m.path)
	if reloaded.Load() != nil {
		t.Fatal("durable source reload failed")
	}
	if reloaded.sources[reg][source].State.Inspection.Result != "failed" {
		t.Fatal("fixture did not preserve durable failure")
	}
	got := m.ExecutionHealth(at)
	if got.State != "degraded" || got.Coverage != "partial" || got.Active != 1 || got.Recovered != 0 || !m.sources[reg][source].State.Inspection.RetentionIncomplete {
		t.Fatal("failed registry save lost prior failure or fabricated retained recovery")
	}
	if !m.sources[reg][source].State.Inspection.LastSuccessAt.IsZero() {
		t.Fatal("unconfirmed success entered retained inspection episode")
	}
	// An unrelated registry save cannot turn the discarded attempt into proof.
	m.writeFile = nil
	if m.saveLocked() != nil {
		t.Fatal("unrelated registry save failed")
	}
	if reloaded.Load() != nil || reloaded.ExecutionHealth(at).Coverage != "partial" || reloaded.ExecutionHealth(at).Recovered != 0 {
		t.Fatal("unrelated save or restart confirmed the discarded attempt")
	}
	at = at.Add(time.Minute)
	run()
	if recovered := m.ExecutionHealth(at); recovered.State != "healthy" || recovered.Coverage != "retained" || recovered.Recovered != 1 {
		t.Fatal("new confirmed inspection failed to recover the episode")
	}
}

func TestSourceInspectionRetainRejectsChangedAuthority(t *testing.T) {
	for _, change := range []string{"registration", "physical_cache", "source_generation", "source_path"} {
		t.Run(change, func(t *testing.T) {
			m, _, _, reg, source := inspectionBoundaryFixture(t)
			snapshot := cloneMaintenanceEntryPrivate(m.entries[reg])
			snapshot.RepositoryDocs.Inspection = RepositoryDocsInspectionState{}.completed("succeeded", "", time.Unix(1700000000, 0).UTC())
			before := m.sources[reg][source].State
			switch change {
			case "registration":
				m.entries[reg].Generation++
			case "physical_cache":
				m.entries[reg].PathFingerprint = "changed-physical-cache"
			case "source_generation":
				m.sources[reg][source].State.SourceRegistrationGeneration++
				before = m.sources[reg][source].State
			case "source_path":
				m.sources[reg][source].RepositoryPath += "-replacement"
			}
			writes := 0
			m.writeFile = func(string, []byte, os.FileMode) error { writes++; return errors.New("fixture write not expected") }
			m.retainRepositoryDocsInspection(&snapshot)
			if writes != 0 || m.sources[reg][source].State != before || !snapshot.RepositoryDocs.Inspection.RetentionIncomplete {
				t.Fatal("obsolete inspection mutated current source authority")
			}
			m.finishReconcileEntry(context.Background(), reg, snapshot, 0, 0, "", "", nil, Job{}, Job{}, Job{}, time.Unix(1700000000, 0).UTC())
			if m.sources[reg][source].State != before {
				t.Fatal("late finish republished obsolete source inspection")
			}
		})
	}
}

func TestSourceInspectionCompletionSurvivesDownstreamScheduleFailure(t *testing.T) {
	m, _, _, reg, source := inspectionBoundaryFixture(t)
	at := time.Unix(1700000000, 0).UTC()
	m.now = func() time.Time { return at }
	_, started := m.reconcileEntry(context.Background(), reg)
	state := m.sources[reg][source].State
	if len(started) != 0 || state.LastErrorClass != "repository_docs_provider_boundary_blocked" {
		t.Fatal("deterministic indexing schedule failure not reached")
	}
	if state.Inspection.Result != "succeeded" || !state.Inspection.LastAttemptAt.Equal(at) {
		t.Fatal("completed inspection lost behind independent schedule failure")
	}
	reloaded := NewMaintenanceManager(m.manager, NewJobManager(""), m.path)
	if reloaded.Load() != nil {
		t.Fatal("source authority reload failed")
	}
	if reloaded.sources[reg][source].State.Inspection.Result != "succeeded" {
		t.Fatal("completed inspection was not durable")
	}
}

func TestSourceInspectionAdmissionFaultNotSuppressedAsInspectionDuplicate(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	m := NewMaintenanceManager(Manager{}, NewJobManager(""), "")
	state := RepositoryDocsMaintenanceState{SourceRegistrationID: "fixture-source", State: "degraded", LastErrorClass: "repository_docs_provider_boundary_blocked", UpdatedAt: at,
		Inspection: RepositoryDocsInspectionState{}.completed("succeeded", "", at)}
	m.entries["reg"] = &MaintenanceEntry{Enabled: true, CacheUUID: "fixture-cache", RepoID: "fixture/repository", State: "degraded", LastReconciledAt: at, RepositoryDocs: &state}
	got := m.ExecutionHealth(at)
	if got.State == "healthy" || got.Active == 0 {
		t.Fatal("independent source admission failure erased as duplicate inspection fault")
	}
}

func TestSourceInspectionActualAdmissionFaultAfterObservedInspection(t *testing.T) {
	m, _, _, reg, source := inspectionBoundaryFixture(t)
	at := time.Unix(1700000000, 0).UTC()
	m.now = func() time.Time { return at }
	m.entries[reg].RepositoryDocs.Stage.RetryAfter = at.Add(time.Hour)
	m.reconcileEntry(context.Background(), reg)
	if m.sources[reg][source].State.Inspection.Result != "succeeded" {
		t.Fatal("prior inspection not observed")
	}
	m.entries[reg].RepositoryDocs.Stage.RetryAfter = time.Time{}
	m.sources[reg][source].State.Stage.RetryAfter = time.Time{}
	at = at.Add(time.Minute)
	_, started := m.reconcileEntry(context.Background(), reg)
	if len(started) != 0 || m.sources[reg][source].State.LastErrorClass != "repository_docs_provider_boundary_blocked" {
		t.Fatal("schedule fault not reached")
	}
	got := m.ExecutionHealth(at)
	if got.State == "healthy" || got.Active == 0 {
		t.Fatal("actual source admission fault erased as inspection duplicate")
	}
}
