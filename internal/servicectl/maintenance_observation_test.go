package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/adminhttp"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/observability"
)

func maintenanceObservationFixture(t *testing.T) (*MaintenanceManager, string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal("private fixture root unavailable")
	}
	path := filepath.Join(root, "cache.db")
	store, err := cache.NewSQLiteStore(context.Background(), path)
	if err != nil {
		t.Fatal("cache fixture unavailable")
	}
	if store.AddRepository(context.Background(), cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}) != nil || store.Close() != nil {
		t.Fatal("cache fixture binding unavailable")
	}
	m := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(filepath.Join(root, "jobs.json")), filepath.Join(root, "registry.json"))
	return m, path
}

func maintenanceEventCollector(t *testing.T) *observability.Collector {
	t.Helper()
	c := jobObservationFixture(t)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := c.Query(observability.Filter{}, "", 200); err == nil {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatal("collector bootstrap query did not become ready")
		}
		time.Sleep(time.Millisecond)
	}
}

func maintenanceObservationPage(t *testing.T, collector *observability.Collector, expected int) []observability.Event {
	t.Helper()
	sealJobObservation(t, collector)
	page, err := collector.Query(observability.Filter{}, "", 200)
	if err != nil {
		t.Fatal("bounded maintenance query unavailable")
	}
	var events []observability.Event
	// Fixed-size fixtures may exceed one byte-bounded page; retain the real
	// cursor protocol rather than enlarging or weakening the production bound.
	for pages := 0; ; pages++ {
		for _, event := range page.Events {
			switch event.Code {
			case observability.MaintenanceStateChanged:
				events = append(events, event)
			case observability.JobAdmitted, observability.JobTerminal, observability.JobRecovered, observability.ProviderAttempt, observability.SyncCommitted:
				t.Fatal("maintenance observation invented execution counts")
			}
		}
		if !page.HasMore {
			break
		}
		if pages >= 3 {
			t.Fatal("bounded fixture unexpectedly exceeded four pages")
		}
		page, err = collector.Query(observability.Filter{}, page.Cursor, 200)
		if err != nil {
			t.Fatal("bounded maintenance continuation unavailable")
		}
	}
	if len(events) > expected || uint64(len(events))+page.Coverage.Dropped != uint64(expected) {
		t.Fatal("actual maintenance boundaries not conserved")
	}
	return events
}

func TestMaintenanceObservationActualEnrollmentReconcileDisable(t *testing.T) {
	ctx := context.Background()
	m, path := maintenanceObservationFixture(t)
	c := maintenanceEventCollector(t)
	m.jobs.SetObservation(c)
	req := testMaintenanceEnrollRequest(path, "synthetic-private-key", MaintenancePolicy{SyncMode: "off"})
	entry, err := m.Enroll(ctx, req)
	if err != nil {
		t.Fatal("enrollment fixture failed")
	}
	if _, err := m.Enroll(ctx, req); err != nil {
		t.Fatal("receipt replay failed")
	}
	for i := 0; i < 3; i++ {
		result, err := m.ReconcileRegistration(ctx, entry.RegistrationID)
		if err != nil || len(result.JobsStarted) != 0 || result.Entries[0].State != "ready" {
			t.Fatal("read-only reconcile behavior changed")
		}
		if _, err := m.List(ctx); err != nil {
			t.Fatal("projection fixture failed")
		}
	}
	controls := &AdminControlManager{maintenance: m, receipts: NewAdminControlReceiptManager(filepath.Join(filepath.Dir(path), "controls.json"))}
	control := adminhttp.RegistrationControlRequest{RegistrationID: entry.RegistrationID, IdempotencyKey: "synthetic-private-control-key"}
	if _, err := controls.DisableMaintenance(ctx, control); err != nil {
		t.Fatal("receipt-wrapped disable failed")
	}
	if _, err := controls.DisableMaintenance(ctx, control); err != nil {
		t.Fatal("receipt-wrapped disable replay failed")
	}
	if _, err := m.Disable(ctx, entry.RegistrationID); err != nil {
		t.Fatal("unchanged direct disable failed")
	}
	if _, err := m.Reconcile(ctx); err != nil {
		t.Fatal("disabled registry reconcile failed")
	}
	events := maintenanceObservationPage(t, c, 3)
	seen := map[string]bool{}
	allowed := map[string]string{"enrolled": "absent", "ready": "enrolled", "disabled": "ready"}
	for _, event := range events {
		if allowed[event.Outcome] != event.PreviousState || event.FailureClass != "" || event.PreviousFailureClass != "" || event.JobRef != "" || event.StageRef != "" || event.RegistrationRef != expectedObservationRef("registration", entry.RegistrationID) || event.CacheRef != expectedObservationRef("cache", entry.CacheUUID) || event.RepoRef != expectedObservationRef("repo", entry.RepoID) || seen[event.CorrelationRef] || event.CorrelationRef == "" {
			t.Fatal("owner transition lost finite identity or correlation")
		}
		seen[event.CorrelationRef] = true
	}
	data, _ := json.Marshal(events)
	for _, private := range []string{path, entry.RegistrationID, entry.CacheUUID, entry.RepoID, req.IdempotencyKey, control.IdempotencyKey} {
		if strings.Contains(string(data), private) {
			t.Fatal("private owner coordinates entered diagnostics")
		}
	}
	// Loading registry authority is not a fresh owner transition.
	restartedCollector := maintenanceEventCollector(t)
	m.jobs.SetObservation(restartedCollector)
	restarted := NewMaintenanceManager(m.manager, m.jobs, m.path)
	if restarted.Load() != nil {
		t.Fatal("registry restart failed")
	}
	if _, err := restarted.Reconcile(ctx); err != nil {
		t.Fatal("disabled restart reconcile failed")
	}
	maintenanceObservationPage(t, restartedCollector, 0)
}

func TestMaintenanceObservationActualSaveConfirmationAndLoss(t *testing.T) {
	for _, mode := range []string{"retained_disable_failure", "reconcile_rename_confirmed", "reconcile_save_failed", "enroll_rollback"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			m, path := maintenanceObservationFixture(t)
			req := testMaintenanceEnrollRequest(path, "synthetic-enroll", MaintenancePolicy{SyncMode: "off"})
			var entry MaintenanceEntry
			if mode != "enroll_rollback" {
				var err error
				entry, err = m.Enroll(ctx, req)
				if err != nil {
					t.Fatal("initial enrollment failed")
				}
			}
			c := maintenanceEventCollector(t)
			m.jobs.SetObservation(c)
			writes := 0
			m.writeFile = func(name string, data []byte, permissions os.FileMode) error {
				writes++
				if mode == "reconcile_rename_confirmed" {
					if durableAtomicWriteFile(name, data, permissions) != nil {
						t.Fatal("ambiguous-write fixture unavailable")
					}
				}
				return errors.New("synthetic-private-storage-failure")
			}
			switch mode {
			case "enroll_rollback":
				if _, err := m.Enroll(ctx, req); err == nil || len(m.entries) != 0 {
					t.Fatal("failed enrollment rollback changed")
				}
				maintenanceObservationPage(t, c, 0)
			case "retained_disable_failure":
				if _, err := m.Disable(ctx, entry.RegistrationID); err == nil || m.entries[entry.RegistrationID].Enabled {
					t.Fatal("retained disable failure behavior changed")
				}
				m.writeFile = durableAtomicWriteFile
				if _, err := m.Disable(ctx, entry.RegistrationID); err != nil {
					t.Fatal("unchanged later persistence failed")
				}
				if len(maintenanceObservationPage(t, c, 1)) != 0 || c.Coverage().Reason != "execution_boundary_unconfirmed" {
					t.Fatal("unconfirmed retained mutation invented a confirmed transition")
				}
			default:
				result, err := m.ReconcileRegistration(ctx, entry.RegistrationID)
				if err != nil || result.Entries[0].State != "ready" {
					t.Fatal("diagnostic save result altered reconcile behavior")
				}
				events := maintenanceObservationPage(t, c, 1)
				if mode == "reconcile_save_failed" && len(events) != 0 {
					t.Fatal("failed save invented persisted state")
				}
			}
			if writes != 1 {
				t.Fatal("observation introduced save retries")
			}
		})
	}
}

func TestMaintenanceObservationNestedBoundaryAndFixedOverflow(t *testing.T) {
	m, path := maintenanceObservationFixture(t)
	entry, err := m.Enroll(context.Background(), testMaintenanceEnrollRequest(path, "fixture-enroll", MaintenancePolicy{SyncMode: "off"}))
	if err != nil {
		t.Fatal("initial enrollment failed")
	}
	c := maintenanceEventCollector(t)
	m.jobs.SetObservation(c)
	ctx, flush := beginMaintenanceObservation(context.Background(), m.jobs)
	// Model the same outer receipt boundary used by Admin/job-action owners.
	m.reconcileMu.Lock()
	if _, err := m.Disable(ctx, entry.RegistrationID); err != nil {
		m.reconcileMu.Unlock()
		flush()
		t.Fatal("nested owner failed")
	}
	page, err := c.Query(observability.Filter{Code: observability.MaintenanceStateChanged}, "", 10)
	if err != nil || len(page.Events) != 0 || page.Coverage.Dropped != 0 {
		m.reconcileMu.Unlock()
		flush()
		t.Fatal("inner owner emitted before outer authority unlocked")
	}
	m.reconcileMu.Unlock()
	flush()
	maintenanceObservationPage(t, c, 1)

	// Fixed primitive capture accounts for every overflow without storing it.
	c = maintenanceEventCollector(t)
	m.jobs.SetObservation(c)
	ctx, flush = beginMaintenanceObservation(context.Background(), m.jobs)
	const total = maxMaintenanceObservations + 9
	m.mu.Lock()
	for i := 0; i < total; i++ {
		captureMaintenanceObservation(ctx, maintenanceObservationState{state: "ready"}, m.entries[entry.RegistrationID], true)
	}
	buffer := ctx.Value(maintenanceObservationKey{}).(*maintenanceObservationBuffer)
	if buffer.count != maxMaintenanceObservations || buffer.lost != 9 {
		m.mu.Unlock()
		flush()
		t.Fatal("maintenance capture exceeded fixed capacity or hid overflow")
	}
	m.mu.Unlock()
	flush()
	maintenanceObservationPage(t, c, total)
}

func TestMaintenanceObservationActualReconcileSaveBarrierAndSchema(t *testing.T) {
	m, path := maintenanceObservationFixture(t)
	entry, err := m.Enroll(context.Background(), testMaintenanceEnrollRequest(path, "fixture-enroll", MaintenancePolicy{SyncMode: "off"}))
	if err != nil {
		t.Fatal("initial enrollment failed")
	}
	setMaintenanceTestSchemaVersion(t, path, cache.CurrentSchemaVersion()+1)
	c := maintenanceEventCollector(t)
	m.jobs.SetObservation(c)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	m.writeFile = func(name string, payload []byte, mode os.FileMode) error {
		close(entered)
		<-release
		return durableAtomicWriteFile(name, payload, mode)
	}
	go func() { _, err := m.ReconcileRegistration(context.Background(), entry.RegistrationID); done <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("save fixture failed to unwind after release")
		}
		t.Fatal("actual save barrier did not enter")
	}
	page, queryErr := c.Query(observability.Filter{Code: observability.MaintenanceStateChanged}, "", 10)
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("schema reconcile failed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("actual save owner did not unwind")
	}
	if queryErr != nil || len(page.Events) != 0 || page.Coverage.Dropped != 0 {
		t.Fatal("maintenance event crossed an unconfirmed locked save")
	}
	m.writeFile = durableAtomicWriteFile
	setMaintenanceTestSchemaVersion(t, path, cache.CurrentSchemaVersion())
	if _, err := m.ReconcileRegistration(context.Background(), entry.RegistrationID); err != nil {
		t.Fatal("schema recovery failed")
	}
	for _, event := range maintenanceObservationPage(t, c, 2) {
		if event.Outcome == "cache_schema_blocked" && event.FailureClass != "cache_schema_blocked" {
			t.Fatal("schema block lost finite failure class")
		}
		if event.Outcome == "ready" && (event.PreviousState != "cache_schema_blocked" || event.PreviousFailureClass != "cache_schema_blocked" || event.FailureClass != "") {
			t.Fatal("schema recovery transition is not actual")
		}
	}
}

func TestMaintenanceObservationFiniteMappingAndIndependentMetrics(t *testing.T) {
	var absentActions *JobActionManager
	if absentActions.Load() != nil {
		t.Fatal("optional job-action load behavior changed")
	}
	for _, fixture := range []struct{ input, expected string }{
		{"", ""}, {"cache_unreadable", "cache"}, {"identity_conflict", "identity"}, {"content_state_failed", "content"},
		{"maintenance_frontiers_read_failed", "frontier"}, {"rag_coverage_read_failed", "embedding"}, {"retry_correlation_persist_failed", "retry"},
		{"sync_schedule_failed", "sync_schedule"}, {"rag_schedule_failed", "rag_schedule"}, {"git_store_unavailable", "repository_docs"}, {"cache_schema_blocked", "cache_schema_blocked"},
		{"synthetic_private_safe_class", "operation_failed"}, {"CACHE_UNREADABLE", "operation_failed"}, {"cache_unreadable_private_suffix", "operation_failed"},
	} {
		if maintenanceObservedFailure(fixture.input) != fixture.expected {
			t.Fatal("maintenance failure mapping is not exact and closed")
		}
	}
	m, path := maintenanceObservationFixture(t)
	entry, err := m.Enroll(context.Background(), testMaintenanceEnrollRequest(path, "fixture-enroll", MaintenancePolicy{SyncMode: "off"}))
	if err != nil {
		t.Fatal("initial enrollment failed")
	}
	metrics := observability.NewMetrics("", time.Time{})
	m.jobs.SetMetrics(metrics)
	m.writeFile = func(string, []byte, os.FileMode) error { return errors.New("synthetic-private-failure") }
	if _, err := m.Disable(context.Background(), entry.RegistrationID); err == nil {
		t.Fatal("failed disable returned success")
	}
	if snapshot := metrics.Snapshot(); snapshot.Dropped != 1 || snapshot.State != "initializing" {
		t.Fatal("pre-sample loss count or readiness changed")
	}
	metrics.SampleAt(time.Now(), nil)
	if snapshot := metrics.Snapshot(); snapshot.Dropped != 1 || snapshot.State != "partial" {
		t.Fatal("metrics-only mode hid actual unconfirmed mutation")
	}
	m.writeFile = durableAtomicWriteFile
	if _, err := m.Disable(context.Background(), entry.RegistrationID); err != nil || metrics.Snapshot().Dropped != 1 {
		t.Fatal("metrics-only replay fabricated another loss")
	}
	ctx, flush := beginMaintenanceObservation(context.Background(), m.jobs)
	for i := 0; i < maxMaintenanceObservations+9; i++ {
		captureMaintenanceObservation(ctx, maintenanceObservationState{state: "ready"}, m.entries[entry.RegistrationID], true)
	}
	flush()
	if metrics.Snapshot().Dropped != 1 {
		t.Fatal("disabled event sink fabricated queue overflow")
	}
}
