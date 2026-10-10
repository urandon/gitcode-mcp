package servicectl

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/observability"
)

func TestSourceInspectionLegacyFailureNotRecoveredByIndexing(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	for _, indexAt := range []time.Time{at.Add(-time.Minute), at, at.Add(time.Minute)} {
		jobs := NewJobManager("")
		m := NewMaintenanceManager(Manager{}, jobs, "")
		m.entries["reg"] = &MaintenanceEntry{RegistrationID: "reg", CacheUUID: "fixture-cache", RepoID: "fixture/repository", Enabled: true,
			RepositoryDocs: &RepositoryDocsMaintenanceState{SourceRegistrationID: "fixture-source", State: "degraded", LastErrorClass: "git_store_unavailable", UpdatedAt: at,
				Stage: MaintenanceStageState{Status: JobStatusSucceeded, UpdatedAt: indexAt}}}
		jobs.jobs["index-success"] = &Job{ID: "index-success", RegistrationID: "reg", CacheUUID: "fixture-cache", RepoID: "fixture/repository", SourceRegistrationID: "fixture-source", Type: RepositoryDocsIndexJobType, Status: JobStatusSucceeded, UpdatedAt: indexAt}
		got := m.ExecutionHealth(at.Add(time.Hour))
		if got.State != "degraded" || got.Active != 1 || got.Recovered != 0 || got.Coverage != "partial" {
			t.Fatal("indexing success hid legacy source inspection failure")
		}
	}
}

func TestSourceInspectionActualFailureRecoveryAndRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, path)
	if err != nil {
		t.Fatal("cache fixture unavailable")
	}
	defer store.Close()
	if store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}) != nil {
		t.Fatal("binding fixture unavailable")
	}
	identity, _ := store.CacheIdentity(ctx)
	repositoryPath := createRepositoryDocsRegistrationRepo(t, filepath.Join(root, "repository"), "synthetic-private-source")
	m := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), filepath.Join(root, "registry.json"))
	at := time.Unix(1700000000, 0).UTC()
	m.now = func() time.Time { return at }
	enrolled, err := m.Enroll(ctx, testMaintenanceEnrollRequest(path, "fixture-enroll", MaintenancePolicy{SyncMode: "off"}))
	if err != nil {
		t.Fatal("enrollment fixture unavailable")
	}
	registered, ok, err := m.RegisterRepositoryDocsSource(ctx, identity.UUID, enrolled.RepoID, repositoryPath, "")
	if err != nil || !ok {
		t.Fatal("source fixture unavailable")
	}
	reg, sourceID := enrolled.RegistrationID, registered.RepositoryDocs.SourceRegistrationID
	run := func() RepositoryDocsMaintenanceState {
		t.Helper()
		snapshot := cloneMaintenanceEntryPrivate(m.entries[reg])
		m.reconcileRepositoryDocsState(ctx, store, &snapshot, at)
		m.finishReconcileEntry(reg, snapshot, 0, 0, "", "", nil, Job{}, Job{}, Job{}, at)
		return m.sources[reg][sourceID].State
	}
	// Missing Git store fails before an indexing job can exist.
	parked := filepath.Join(root, "parked-repository")
	if os.Rename(repositoryPath, parked) != nil {
		t.Fatal("Git unavailability fixture failed")
	}
	failed := run()
	if failed.Inspection.Result != "failed" || failed.Inspection.LastFailureClass != "git_store" || !failed.Inspection.LastAttemptAt.Equal(at) || !failed.Inspection.FirstFailureAt.Equal(at) {
		t.Fatal("actual Git inspection result missing")
	}
	got := m.ExecutionHealth(at.Add(time.Hour))
	if got.State != "degraded" || got.Active != 1 || got.Coverage != "retained" || got.Incidents[0].Phase != "inspection" || got.Incidents[0].Reason != "source_unavailable" || !got.Incidents[0].LastObservedAt.Equal(at) {
		t.Fatal("actual inspection failure not projected independently")
	}
	// Successful actual inspection recovers only inspection, not indexing.
	if os.Rename(parked, repositoryPath) != nil {
		t.Fatal("Git recovery fixture failed")
	}
	at = at.Add(time.Minute)
	recovered := run()
	if recovered.Inspection.Result != "succeeded" || recovered.State != "not_indexed" || !recovered.Inspection.LastSuccessAt.Equal(at) {
		t.Fatal("inspection success claimed index readiness or lost timestamp")
	}
	before := m.ExecutionHealth(at.Add(time.Hour))
	if before.State != "healthy" || before.Recovered != 1 || before.Incidents[0].Phase != "inspection" || !before.Incidents[0].LastObservedAt.Equal(at) {
		t.Fatal("actual inspection recovery lost")
	}
	reloaded := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), m.path)
	if reloaded.Load() != nil {
		t.Fatal("source authority restart failed")
	}
	if after := reloaded.ExecutionHealth(at.Add(time.Hour)); !reflect.DeepEqual(before, after) {
		t.Fatal("restart changed retained inspection history")
	}
	// Snapshot/value copies cannot let consumers mutate private authority.
	copy := cloneMaintenanceEntry(m.entries[reg])
	copy.RepositoryDocs.Inspection.LastSuccessAt = time.Time{}
	copy.RepositoryDocsSources[0].Inspection.LastFailureClass = "synthetic-private-error"
	if !reflect.DeepEqual(m.ExecutionHealth(at.Add(time.Hour)), before) {
		t.Fatal("returned state mutated source authority")
	}
	data, _ := json.Marshal(before)
	for _, secret := range []string{root, repositoryPath, "owner/repo", reg, sourceID, "synthetic-private-source"} {
		if strings.Contains(string(data), secret) {
			t.Fatal("inspection health captured private authority")
		}
	}
	if len(m.jobs.jobs) != 0 {
		t.Fatal("observation or inspection admitted an indexing job")
	}
	// The revision-set lookup has its own actual failure boundary.
	if store.Close() != nil {
		t.Fatal("revision-state failure fixture unavailable")
	}
	at = at.Add(time.Minute)
	if state := run(); state.Inspection.LastFailureClass != "revision_set" || state.Inspection.Result != "failed" {
		t.Fatal("actual revision-state inspection failure lost")
	}
	if got := m.ExecutionHealth(at); got.Active != 1 || got.Incidents[0].Reason != "source_state_unavailable" {
		t.Fatal("revision-state inspection misclassified")
	}
	// Policy errors occur before the cache lookup and are not index failures.
	policyPath := filepath.Join(repositoryPath, ".gitcode", "gitcode-mcp.yaml")
	if os.MkdirAll(filepath.Dir(policyPath), 0o755) != nil || os.WriteFile(policyPath, []byte("repository_docs:\n  schema: 99\n"), 0o600) != nil {
		t.Fatal("policy failure fixture unavailable")
	}
	runGitForRepositoryDocsJobTest(t, repositoryPath, "add", ".gitcode/gitcode-mcp.yaml")
	runGitForRepositoryDocsJobTest(t, repositoryPath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "policy fixture")
	at = at.Add(time.Minute)
	if state := run(); state.Inspection.LastFailureClass != "policy" || state.Inspection.Result != "failed" {
		t.Fatal("actual policy inspection failure lost")
	}
	if os.WriteFile(policyPath, []byte("repository_docs:\n  enabled: false\n"), 0o600) != nil {
		t.Fatal("policy disable fixture unavailable")
	}
	runGitForRepositoryDocsJobTest(t, repositoryPath, "add", ".gitcode/gitcode-mcp.yaml")
	runGitForRepositoryDocsJobTest(t, repositoryPath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "disabled policy fixture")
	at = at.Add(time.Minute)
	if state := run(); state.State != "disabled" || state.Inspection.Result != "disabled" || state.Inspection.LastSuccessAt.Equal(at) {
		t.Fatal("policy disable fabricated successful inspection")
	}
	if got := m.ExecutionHealth(at); got.Active != 0 || got.State != "healthy" {
		t.Fatal("explicit disabled corpus projected old fault")
	}
}

func inspectionFixtureState(at time.Time) RepositoryDocsMaintenanceState {
	return RepositoryDocsMaintenanceState{SourceRegistrationID: "fixture-source", State: "degraded", UpdatedAt: at,
		Inspection: RepositoryDocsInspectionState{}.completed("failed", "git_store", at)}
}

func TestSourceInspectionRetainedTimestampsAndIndexingAreIndependent(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	for _, indexAt := range []time.Time{at.Add(-time.Minute), at, at.Add(time.Minute)} {
		jobs := NewJobManager("")
		m := NewMaintenanceManager(Manager{}, jobs, "")
		state := inspectionFixtureState(at)
		state.Stage = MaintenanceStageState{Status: JobStatusSucceeded, UpdatedAt: indexAt}
		m.entries["reg"] = &MaintenanceEntry{CacheUUID: "cache", RepoID: "fixture/repository", Enabled: true, RepositoryDocs: &state}
		jobs.jobs["index"] = &Job{ID: "index", Type: RepositoryDocsIndexJobType, Status: JobStatusSucceeded, CacheUUID: "cache", RepoID: "fixture/repository", RegistrationID: "reg", SourceRegistrationID: state.SourceRegistrationID, UpdatedAt: indexAt}
		if got := m.ExecutionHealth(at); got.Active != 1 || got.Recovered != 0 || got.Incidents[0].Phase != "inspection" {
			t.Fatal("indexing timestamp recovered inspection")
		}
		state.Inspection = state.Inspection.completed("succeeded", "", at)
		if got := m.ExecutionHealth(at); got.Active != 1 || got.Recovered != 0 {
			t.Fatal("equal timestamp fabricated ordered inspection recovery")
		}
		state.Inspection = state.Inspection.completed("succeeded", "", at.Add(time.Second))
		if got := m.ExecutionHealth(at); got.Active != 0 || got.Recovered != 1 {
			t.Fatal("later actual inspection did not recover")
		}
		// A new failed episode retains its own first/last timestamps.
		state.Inspection = state.Inspection.completed("failed", "policy", at.Add(2*time.Second))
		state.Inspection = state.Inspection.completed("failed", "policy", at.Add(3*time.Second))
		got := m.ExecutionHealth(at)
		if got.Active != 1 || !got.Incidents[0].FirstObservedAt.Equal(at.Add(2*time.Second)) || !got.Incidents[0].LastObservedAt.Equal(at.Add(3*time.Second)) || got.Incidents[0].Reason != "source_policy_invalid" {
			t.Fatal("retained episode timestamps lost")
		}
	}
}

func TestSourceInspectionDoesNotRecoverIndexingFailureOrIgnoreNewFailureAfterOldCancellation(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	m := NewMaintenanceManager(Manager{}, NewJobManager(""), "")
	state := inspectionFixtureState(at)
	state.Inspection = state.Inspection.completed("succeeded", "", at.Add(time.Minute))
	state.Stage = MaintenanceStageState{Status: JobStatusFailed, ConsecutiveFailures: 1, LastErrorClass: "embedding_failed", UpdatedAt: at}
	m.entries["reg"] = &MaintenanceEntry{Enabled: true, CacheUUID: "cache", RepoID: "fixture/repository", RepositoryDocs: &state}
	got := m.ExecutionHealth(at)
	if got.Active != 1 || got.Recovered != 1 {
		t.Fatal("inspection success recovered indexing failure")
	}
	if got.Incidents[0].Phase != "indexing" || got.Incidents[1].Phase != "inspection" {
		t.Fatal("inspection and indexing phases not explicit")
	}
	state.Stage.Status = JobStatusCancelled
	state.Inspection = state.Inspection.completed("failed", "git_store", at.Add(2*time.Minute))
	if got := m.ExecutionHealth(at); got.Active != 1 || got.Incidents[0].Phase != "inspection" {
		t.Fatal("old cancelled indexing stage hid new inspection failure")
	}
}

func TestSourceInspectionLegacyAndInvalidEvidenceIsUnknown(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	cases := []RepositoryDocsMaintenanceState{
		{State: "registered"},
		{State: "ready", Stage: MaintenanceStageState{Status: JobStatusSucceeded, UpdatedAt: at}},
		{State: "degraded", LastErrorClass: "git_store_unavailable"},
		{State: "degraded", Inspection: RepositoryDocsInspectionState{Result: "failed"}},
		{State: "indexing", Inspection: RepositoryDocsInspectionState{Result: "disabled"}},
	}
	bad := inspectionFixtureState(at)
	bad.Inspection.LastSuccessAt = at.Add(time.Minute)
	cases = append(cases, bad)
	bad = inspectionFixtureState(at)
	bad.Inspection.LastFailureClass = "synthetic-private-error"
	cases = append(cases, bad)
	for _, state := range cases {
		m := NewMaintenanceManager(Manager{}, NewJobManager(""), "")
		m.entries["reg"] = &MaintenanceEntry{Enabled: true, CacheUUID: "cache", RepoID: "fixture/repository", RepositoryDocs: &state}
		if got := m.ExecutionHealth(at); got.State != "unknown" || got.Coverage != "partial" {
			t.Fatal("unavailable inspection history fabricated healthy state")
		}
	}
}

func TestSourceInspectionCanonicalMultipleSourcesCancellationAndBounds(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	m := NewMaintenanceManager(Manager{}, NewJobManager(""), "")
	entry := &MaintenanceEntry{Enabled: true, CacheUUID: "cache", RepoID: "fixture/repository", PathFingerprint: "fixture-path"}
	m.entries["reg"] = entry
	m.sources["reg"] = map[string]*repositoryDocsRegisteredSource{}
	for i := 0; i < observability.MaxIncidents+9; i++ {
		state := inspectionFixtureState(at)
		state.SourceRegistrationID = fmt.Sprint(i)
		if i == 0 {
			state.Inspection = state.Inspection.completed("succeeded", "", at.Add(time.Second))
		}
		m.sources["reg"][state.SourceRegistrationID] = &repositoryDocsRegisteredSource{State: state}
	}
	// The legacy singleton must not add duplicate/contradictory evidence.
	legacy := inspectionFixtureState(at)
	legacy.SourceRegistrationID = "legacy"
	entry.RepositoryDocs = &legacy
	got := m.ExecutionHealth(at)
	if got.Active != observability.MaxIncidents+8 || got.Recovered != 1 || len(got.Incidents) != observability.MaxIncidents || got.Omitted != 9 || got.Coverage != "partial" {
		t.Fatal("source projection lost active count or exceeded incident limit")
	}
	for _, incident := range got.Incidents {
		if incident.State == "recovered" {
			t.Fatal("recovered source displaced active fault at the limit")
		}
	}
	m.sources["reg"] = map[string]*repositoryDocsRegisteredSource{"fixture-source": {State: inspectionFixtureState(at)}}
	entry.RepositoryDocs = nil
	m.admissions[repositoryDocsAdmissionKey("reg", "fixture-source")] = repositoryDocsAdmissionIntent{Disposition: repositoryDocsAdmissionCancelled}
	if got := m.ExecutionHealth(at); got.State != "healthy" || got.Active != 0 {
		t.Fatal("current cancellation tombstone did not suppress inactive source")
	}
	// Exact physical authority wins over a legacy cancellation record.
	m.admissions[repositoryDocsAdmissionKey("reg", "fixture-source", entry.PathFingerprint)] = repositoryDocsAdmissionIntent{Disposition: repositoryDocsAdmissionPending}
	if got := m.ExecutionHealth(at); got.State != "degraded" || got.Active != 1 {
		t.Fatal("legacy tombstone suppressed another physical authority")
	}
	entry.Enabled = false
	if got := m.ExecutionHealth(at); got.State != "healthy" || got.Active != 0 {
		t.Fatal("disabled registration projected an active source fault")
	}
	entry.Enabled = true
	m.sources["reg"]["fixture-source"].State.State = "disabled"
	if got := m.ExecutionHealth(at); got.State != "healthy" || got.Active != 0 {
		t.Fatal("explicit policy disable became a source fault")
	}
}

func TestSourceInspectionConcurrentProjectionAndHeldLocks(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	m := NewMaintenanceManager(Manager{}, NewJobManager(""), "")
	m.entries["reg"] = &MaintenanceEntry{Enabled: true, CacheUUID: "cache", RepoID: "fixture/repository"}
	m.sources["reg"] = map[string]*repositoryDocsRegisteredSource{"fixture-source": {State: inspectionFixtureState(at)}}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			m.mu.Lock()
			m.sources["reg"]["fixture-source"].State.Inspection = RepositoryDocsInspectionState{}.completed("failed", "git_store", at.Add(time.Duration(i)*time.Second))
			m.mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			m.ExecutionHealth(at)
		}
	}()
	wg.Wait()
	m.mu.Lock()
	done := make(chan observability.ExecutionHealth, 1)
	go func() { done <- m.ExecutionHealth(at) }()
	select {
	case got := <-done:
		if got.State != "unknown" || got.Reason != "authority_busy" {
			t.Error("busy source authority fabricated health")
		}
	case <-time.After(time.Second):
		m.mu.Unlock()
		t.Fatal("health read waited on source authority")
	}
	m.mu.Unlock()
}

func TestSourceInspectionMalformedEnumsNeverEnterPublicStatus(t *testing.T) {
	state := inspectionFixtureState(time.Unix(1700000000, 0).UTC())
	state.Inspection.Result = "synthetic-private-result"
	state.Inspection.LastFailureClass = "synthetic-private-error"
	entry := &MaintenanceEntry{RepositoryDocs: &state, RepositoryDocsSources: []RepositoryDocsMaintenanceState{state}}
	for _, public := range []MaintenanceEntry{cloneMaintenanceEntry(entry), cloneMaintenanceEntryForSource(entry, &repositoryDocsRegisteredSource{State: state})} {
		data, _ := json.Marshal(public)
		if strings.Contains(string(data), "synthetic-private-") {
			t.Fatal("malformed inspection enum entered public status")
		}
	}
	if state.Inspection.LastFailureClass != "synthetic-private-error" {
		t.Fatal("public sanitization mutated execution authority")
	}
}

func TestSourceInspectionCompletionClockDoesNotChangePollingSchedule(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	completedAt := at.Add(30 * time.Second)
	m := NewMaintenanceManager(Manager{}, NewJobManager(""), "")
	m.now = func() time.Time { return completedAt }
	entry := &MaintenanceEntry{RepositoryDocs: &RepositoryDocsMaintenanceState{}, repositoryPath: filepath.Join(t.TempDir(), "missing")}
	m.reconcileRepositoryDocsState(context.Background(), nil, entry, at)
	state := entry.RepositoryDocs
	if !state.UpdatedAt.Equal(at) || !state.NextPollAt.Equal(at.Add(time.Minute)) || !state.Inspection.LastAttemptAt.Equal(completedAt) {
		t.Fatal("inspection used polling time as completion or changed execution schedule")
	}
}
