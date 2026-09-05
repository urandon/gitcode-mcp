package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/service"
)

func TestRPCServiceStatusAndFakeJobLifecycle(t *testing.T) {
	manager := newTestManager(t, "darwin")
	manager.Commit = "test-commit"
	src := manager.Source.(testSource)
	src.env = map[string]string{"GITCODE_MCP_SERVICE_NETWORK": "mem", "GITCODE_MCP_SERVICE_ADDRESS": "test-ipc-lifecycle"}
	manager.Source = src
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Run(ctx)
	}()
	client := waitForTestClient(t, manager, errCh)

	var status Status
	if err := client.Call(context.Background(), "Service.Status", nil, &status); err != nil {
		t.Fatal(err)
	}
	if status.Status != StatusRunning || !status.Running || !status.SocketPresent {
		t.Fatalf("service status = %#v", status)
	}
	if status.BinaryVersion != manager.Version || status.BinaryCommit != manager.Commit || status.SchemaMin != cache.CurrentSchemaVersion() || status.SchemaMax != cache.CurrentSchemaVersion() {
		t.Fatalf("service status compatibility contract = %#v", status)
	}
	var health ServiceHealth
	if err := client.Call(context.Background(), "Service.Health", nil, &health); err != nil {
		t.Fatal(err)
	}
	if !health.Healthy || health.BinaryVersion != manager.Version || health.BinaryCommit != manager.Commit || health.SchemaMin != cache.CurrentSchemaVersion() || health.SchemaMax != cache.CurrentSchemaVersion() {
		t.Fatalf("service health compatibility contract = %#v", health)
	}
	var capabilities MaintenanceCapabilities
	if err := client.Call(context.Background(), "Maintenance.Capabilities", nil, &capabilities); err != nil {
		t.Fatal(err)
	}
	if capabilities.RegistryProtocol != maintenanceRegistrySchema || capabilities.BinaryVersion != manager.Version || len(capabilities.Methods) != 6 {
		t.Fatalf("maintenance capabilities = %#v", capabilities)
	}

	var job Job
	if err := client.Call(context.Background(), "Jobs.StartFake", StartFakeJobRequest{Steps: 20, IntervalMS: 25}, &job); err != nil {
		t.Fatal(err)
	}
	if job.ID == "" || job.Type != "fake" {
		t.Fatalf("started job = %#v", job)
	}

	var list JobListResult
	if err := client.Call(context.Background(), "Jobs.List", nil, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Jobs) != 1 || list.Jobs[0].ID != job.ID {
		t.Fatalf("job list = %#v", list)
	}

	cancelled := waitForJobStatus(t, client, job.ID, "cancel")
	if cancelled.Status != JobStatusCancelled || cancelled.FinishedAt == nil {
		t.Fatalf("cancelled job = %#v", cancelled)
	}
	data, err := json.Marshal(cancelled.Progress)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "records_fetched") && !strings.Contains(string(data), "cancelled") {
		t.Fatalf("progress serialization missing expected fields: %s", string(data))
	}

	cancel()
	if err := <-errCh; err != nil && err != context.Canceled {
		t.Fatalf("service run returned %v", err)
	}
}

func TestIssue141BlockedCacheInspectionDoesNotBlockControlPlane(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	blockedPath := filepath.Join(root, "blocked.db")
	metadataBlockedPath := filepath.Join(root, "metadata-blocked.db")
	healthyPath := filepath.Join(root, "healthy.db")
	identities := map[string]cache.CacheIdentity{}
	canonicalPaths := map[string]string{}
	canonicalIdentities := map[string]cache.CacheIdentity{}
	schemas := map[string]int{}
	for _, path := range []string{blockedPath, metadataBlockedPath, healthyPath} {
		store, err := cache.NewSQLiteStore(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
			store.Close()
			t.Fatal(err)
		}
		identity, err := store.CacheIdentity(ctx)
		if err != nil {
			store.Close()
			t.Fatal(err)
		}
		identities[path] = identity
		canonicalPath, err := canonicalCachePath(path)
		if err != nil {
			store.Close()
			t.Fatal(err)
		}
		canonicalPaths[path] = canonicalPath
		canonicalIdentities[canonicalPath] = identity
		schema, err := store.SchemaVersion(ctx)
		if err != nil {
			store.Close()
			t.Fatal(err)
		}
		schemas[path] = schema
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}

	manager := newTestManager(t, "darwin")
	src := manager.Source.(testSource)
	src.env = map[string]string{"GITCODE_MCP_SERVICE_NETWORK": "mem", "GITCODE_MCP_SERVICE_ADDRESS": "issue-141-bounded-startup-" + filepath.Base(root)}
	manager.Source = src
	paths, err := manager.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.RegistryPath), 0o700); err != nil {
		t.Fatal(err)
	}
	disk := maintenanceRegistryFile{SchemaVersion: maintenanceRegistrySchema, Generation: 1}
	for _, path := range []string{blockedPath, metadataBlockedPath, healthyPath} {
		cfg := config.Default()
		cfg.CachePath = path
		disk.Entries = append(disk.Entries, maintenanceDiskEntry{
			MaintenanceEntry: MaintenanceEntry{
				RegistrationID: maintenanceRegistrationID(identities[path].UUID, "owner/repo"),
				CacheUUID:      identities[path].UUID,
				RepoID:         "owner/repo",
				Policy:         MaintenancePolicy{},
				ConfigHash:     maintenanceHash(cfg),
				Enabled:        true,
				State:          "ready",
				Generation:     1,
			},
			CachePath:      path,
			ConfigSnapshot: cfg,
		})
	}
	registryJSON, err := json.Marshal(disk)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.RegistryPath, registryJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	jobID := "job-000001"
	repositoryDocsJobID := "job-000002"
	repositoryDocsWorkKey := "issue-141-repository-docs-work"
	registrationID := maintenanceRegistrationID(identities[healthyPath].UUID, "owner/repo")
	seedJobs := NewJobManager(paths.JobsPath)
	seedJobs.jobs[jobID] = &Job{
		ID: jobID, Type: SyncJobType, RepoID: "owner/repo", CacheUUID: identities[healthyPath].UUID,
		RegistrationID: registrationID, Status: JobStatusRunning, CreatedAt: time.Now().Add(-time.Minute).UTC(), UpdatedAt: time.Now().UTC(),
	}
	seedJobs.jobs[repositoryDocsJobID] = &Job{
		ID: repositoryDocsJobID, Type: RepositoryDocsIndexJobType, RepoID: "owner/repo", CacheUUID: identities[healthyPath].UUID,
		RegistrationID: registrationID, SourceRegistrationID: "source-1", SourceRegistrationGeneration: 1,
		ExpectedRevisionSetID: "set-1", WorkRef: publicWorkRef(repositoryDocsWorkKey),
		Status: JobStatusRunning, CreatedAt: time.Now().Add(-time.Minute).UTC(), UpdatedAt: time.Now().UTC(),
	}
	seedJobs.nextID = 2
	if err := seedJobs.saveLocked(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewSyncStageJournal(paths.RuntimeDir, SyncStageLimits{}).Create(SyncStageEnvelope{
		JobID: jobID, CacheUUID: identities[healthyPath].UUID, CacheSchema: schemas[healthyPath], CachePath: healthyPath,
		RegistrationID: registrationID, RepoID: "owner/repo",
		BindingFingerprint: syncRepositoryBindingFingerprint(cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}),
		Collection:         "issues", IdempotencyKey: "issue-141-durable-recovery", Payload: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatal(err)
	}

	releaseBlockedOpen := make(chan struct{})
	releaseBlockedMetadata := make(chan struct{})
	releaseBlockedRecovery := make(chan struct{})
	recoveryStarted := make(chan struct{})
	defer close(releaseBlockedOpen)
	defer close(releaseBlockedMetadata)
	defer close(releaseBlockedRecovery)
	manager.maintenanceCacheInspectTimeout = 25 * time.Millisecond
	manager.maintenanceCacheCanonicalizer = func(path string) (string, error) {
		if path == metadataBlockedPath {
			<-releaseBlockedMetadata
		}
		return canonicalCachePath(path)
	}
	manager.maintenanceCacheInspector = func(inspectCtx context.Context, path, repoID string) (cache.CacheIdentity, cache.RepositoryBinding, error) {
		if path == canonicalPaths[blockedPath] {
			<-releaseBlockedOpen
		}
		if identity, ok := canonicalIdentities[path]; ok {
			return identity, cache.RepositoryBinding{RepoID: repoID, Owner: "owner", Name: "repo"}, nil
		}
		return inspectMaintenanceCache(inspectCtx, path, repoID)
	}
	manager.syncStageRecovery = func(_ context.Context, jobs *JobManager, _ Manager) error {
		stages, _, err := NewSyncStageJournal(paths.RuntimeDir, SyncStageLimits{}).ListForRecovery()
		if err != nil {
			return err
		}
		job, ok := jobs.Get(jobID)
		if len(stages) != 1 || !ok || job.Status != JobStatusInterrupted {
			return fmt.Errorf("durable recovery fixture missing: stages=%d job=%+v found=%t", len(stages), job, ok)
		}
		releaseWriter, writerErr := jobs.BeginDirectCacheWriter(identities[healthyPath].UUID, "issue-141-probe")
		if writerErr == nil {
			releaseWriter()
			return errors.New("durable recovery cache authority was not writer-fenced")
		}
		var fenceErr CacheRecoveryFenceError
		if !errors.As(writerErr, &fenceErr) {
			return fmt.Errorf("durable recovery writer fence error=%T %v", writerErr, writerErr)
		}
		_, resumed, resumeErr := jobs.ResumeRepositoryDocsAdmission(repositoryDocsJobID, registrationID, "source-1", 1, "set-1", repositoryDocsWorkKey, "", func() {})
		if resumed || !errors.As(resumeErr, &fenceErr) {
			return fmt.Errorf("repository-docs recovery bypassed sync fence: resumed=%t err=%T %v", resumed, resumeErr, resumeErr)
		}
		close(recoveryStarted)
		<-releaseBlockedRecovery
		return nil
	}
	runCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	startedAt := time.Now()
	go func() { errCh <- manager.Run(runCtx) }()
	client := waitForTestClient(t, manager, errCh)
	if elapsed := time.Since(startedAt); elapsed > 500*time.Millisecond {
		cancel()
		t.Fatalf("control plane publication took %s", elapsed)
	}
	select {
	case <-recoveryStarted:
	case <-time.After(500 * time.Millisecond):
		cancel()
		t.Fatal("durable sync-stage recovery did not start after control-plane publication")
	}

	var list MaintenanceListResult
	if err := client.Call(ctx, "Maintenance.List", nil, &list); err != nil {
		cancel()
		t.Fatal(err)
	}
	if len(list.Entries) != 3 {
		t.Fatalf("maintenance entries=%+v", list.Entries)
	}
	states := map[string]MaintenanceEntry{}
	for _, entry := range list.Entries {
		states[entry.CacheUUID] = entry
	}
	blocked := states[identities[blockedPath].UUID]
	if blocked.Enabled || blocked.State != "cache_inspection_timeout" || blocked.LastErrorClass != "cache_inspection_timeout" || !strings.Contains(blocked.LastError, "foreground service mode") {
		t.Fatalf("blocked registration=%+v", blocked)
	}
	metadataBlocked := states[identities[metadataBlockedPath].UUID]
	if metadataBlocked.Enabled || metadataBlocked.State != "cache_inspection_timeout" || metadataBlocked.LastErrorClass != "cache_inspection_timeout" {
		t.Fatalf("metadata-blocked registration=%+v", metadataBlocked)
	}
	healthy := states[identities[healthyPath].UUID]
	if !healthy.Enabled || healthy.LastErrorClass != "" {
		t.Fatalf("healthy registration=%+v", healthy)
	}
	publicJSON, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(publicJSON), blockedPath) || strings.Contains(string(publicJSON), metadataBlockedPath) || strings.Contains(string(publicJSON), healthyPath) {
		t.Fatalf("maintenance list leaked a cache path: %s", publicJSON)
	}
	var jobsList JobListResult
	if err := client.Call(ctx, "Jobs.List", nil, &jobsList); err != nil {
		t.Fatal(err)
	}
	if len(jobsList.Jobs) != 2 || jobsList.Jobs[0].Status != JobStatusInterrupted || jobsList.Jobs[1].Status != JobStatusInterrupted {
		t.Fatalf("durable recovery did not remain observable while blocked: %+v", jobsList.Jobs)
	}
	var reconciled MaintenanceReconcileResult
	if err := client.Call(ctx, "Maintenance.ReconcileRegistration", MaintenanceRegistrationRequest{RegistrationID: registrationID}, &reconciled); err != nil {
		t.Fatal(err)
	}
	if len(reconciled.Entries) != 1 || !reconciled.Entries[0].Enabled || reconciled.Entries[0].LastErrorClass != "" || len(reconciled.JobsStarted) != 0 {
		t.Fatalf("recovery-fenced reconciliation changed healthy state: %+v", reconciled)
	}

	cancel()
	if err := <-errCh; err != nil && err != context.Canceled {
		t.Fatalf("service run returned %v", err)
	}
	recovered := NewMaintenanceManager(managerWithoutInspectionSeam(manager), NewJobManager(""), paths.RegistryPath)
	if err := recovered.Load(); err != nil {
		t.Fatal(err)
	}
	recoveredList, err := recovered.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range recoveredList.Entries {
		wasBlocked := entry.CacheUUID == identities[blockedPath].UUID || entry.CacheUUID == identities[metadataBlockedPath].UUID
		if wasBlocked && (!entry.Enabled || entry.State != "enrolled" || entry.LastErrorClass != "") {
			t.Fatalf("recovered registration=%+v", entry)
		}
	}
}

func TestSyncStageRecoveryRetainsFenceWhenFailureCannotBePersisted(t *testing.T) {
	jobs := NewJobManager(filepath.Join(t.TempDir(), "jobs.json"))
	cacheUUID := "cache-uuid"
	jobs.jobs["job-000001"] = &Job{ID: "job-000001", Type: SyncJobType, CacheUUID: cacheUUID, Status: JobStatusInterrupted, CreatedAt: time.Now().Add(-time.Minute).UTC(), UpdatedAt: time.Now().UTC()}
	if err := jobs.saveLocked(); err != nil {
		t.Fatal(err)
	}
	releaseFences := jobs.beginInterruptedSyncRecoveryFences()
	defer releaseFences()
	persistAttempted := make(chan struct{})
	var once sync.Once
	jobs.writeFile = func(string, []byte, os.FileMode) error {
		once.Do(func() { close(persistAttempted) })
		return errors.New("snapshot unavailable")
	}
	manager := newTestManager(t, "darwin")
	manager.syncStageRecovery = func(context.Context, *JobManager, Manager) error {
		return errors.New("recovery unavailable")
	}
	manager.startSyncStageRecovery(context.Background(), jobs, releaseFences)
	select {
	case <-persistAttempted:
	case <-time.After(time.Second):
		t.Fatal("recovery failure was not projected onto interrupted jobs")
	}
	if !jobs.CacheRecoveryPending(cacheUUID) {
		t.Fatal("cache recovery fence was released after terminal-state persistence failed")
	}
	job, ok := jobs.Get("job-000001")
	if !ok || job.Status != JobStatusInterrupted {
		t.Fatalf("interrupted job changed despite persistence failure: %+v found=%t", job, ok)
	}
}

func managerWithoutInspectionSeam(manager Manager) Manager {
	manager.maintenanceCacheInspector = nil
	manager.maintenanceCacheCanonicalizer = nil
	manager.maintenanceCacheInspectTimeout = 0
	manager.syncStageRecovery = nil
	return manager
}

func TestRPCStatusHealthAndJobsExposeCacheSchemaBlocks(t *testing.T) {
	manager := newTestManager(t, "darwin")
	manager.Commit = "daemon-commit"
	jobs := NewJobManager("")
	maintenance := NewMaintenanceManager(manager, jobs, filepath.Join(t.TempDir(), "managed-caches.json"))
	maintenance.mu.Lock()
	maintenance.entries["maintenance-schema"] = &MaintenanceEntry{
		RegistrationID: "maintenance-schema", RepoID: "owner/repository", CacheUUID: "cache-public-id",
		State: "cache_schema_blocked", DetectedSchemaVersion: cache.CurrentSchemaVersion() + 1,
		ExpectedSchemaVersion: cache.CurrentSchemaVersion(), DaemonBinaryVersion: manager.Version,
		DaemonBinaryCommit: manager.Commit, QuiesceState: "required",
	}
	maintenance.mu.Unlock()
	server := RPCServer{Manager: manager, Jobs: jobs, Maintenance: maintenance}

	statusValue, err := server.dispatch(context.Background(), "Service.Status", nil)
	if err != nil {
		t.Fatal(err)
	}
	status := statusValue.(Status)
	if status.CacheReadiness != "cache_schema_blocked" || len(status.CacheSchemaBlocks) != 1 {
		t.Fatalf("status schema contract=%#v", status)
	}
	block := status.CacheSchemaBlocks[0]
	if block.DetectedVersion != cache.CurrentSchemaVersion()+1 || block.ExpectedVersion != cache.CurrentSchemaVersion() || block.DaemonBinaryVersion != manager.Version || block.DaemonBinaryCommit != manager.Commit || block.QuiesceState != "required" {
		t.Fatalf("status schema block=%#v", block)
	}

	healthValue, err := server.dispatch(context.Background(), "Service.Health", nil)
	if err != nil {
		t.Fatal(err)
	}
	health := healthValue.(ServiceHealth)
	if health.Healthy || health.CacheReadiness != "cache_schema_blocked" || len(health.CacheSchemaBlocks) != 1 {
		t.Fatalf("health schema contract=%#v", health)
	}

	jobsValue, err := server.dispatch(context.Background(), "Jobs.List", nil)
	if err != nil {
		t.Fatal(err)
	}
	jobList := jobsValue.(JobListResult)
	if jobList.CacheReadiness != "cache_schema_blocked" || len(jobList.CacheSchemaBlocks) != 1 {
		t.Fatalf("jobs schema contract=%#v", jobList)
	}
	raw, err := json.Marshal(jobList)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), string(filepath.Separator)+"private") {
		t.Fatalf("jobs schema contract leaked a path: %s", raw)
	}
}

func TestJobManagerMarksRunningSnapshotInterrupted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.json")
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	running := []Job{{
		ID:        "job-000007",
		Type:      "fake",
		Status:    JobStatusRunning,
		CreatedAt: now,
		UpdatedAt: now,
		Steps:     10,
		Completed: 3,
	}}
	data, err := json.Marshal(running)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewJobManager(path)
	manager.now = func() time.Time { return now.Add(time.Minute) }
	if err := manager.LoadAndMarkInterrupted(); err != nil {
		t.Fatal(err)
	}
	job, ok := manager.Get("job-000007")
	if !ok {
		t.Fatal("job not loaded")
	}
	if job.Status != JobStatusInterrupted || job.FinishedAt == nil || !strings.Contains(job.Error, "restarted") {
		t.Fatalf("interrupted job = %#v", job)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	next, err := manager.StartFake(ctx, StartFakeJobRequest{Steps: 1, IntervalMS: 1})
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != "job-000008" {
		t.Fatalf("next job id = %q, want job-000008", next.ID)
	}
	waitForManagerJobTerminal(t, manager, next.ID)
}

func TestJobManagerPrunesStoredTerminalJobs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.json")
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	manager := NewJobManager(path)
	manager.now = func() time.Time { return now }
	manager.mu.Lock()
	for i := 1; i <= maxStoredTerminalJobs+2; i++ {
		finished := now.Add(time.Duration(i) * time.Minute)
		id := fmt.Sprintf("job-%06d", i)
		manager.jobs[id] = &Job{ID: id, Type: "fake", Status: JobStatusSucceeded, CreatedAt: now, UpdatedAt: finished, FinishedAt: &finished}
	}
	manager.jobs["job-active"] = &Job{ID: "job-active", Type: "fake", Status: JobStatusRunning, CreatedAt: now, UpdatedAt: now}
	if err := manager.saveLocked(); err != nil {
		manager.mu.Unlock()
		t.Fatal(err)
	}
	manager.mu.Unlock()

	if _, ok := manager.Get("job-000001"); ok {
		t.Fatal("oldest terminal job was not pruned")
	}
	if _, ok := manager.Get("job-000002"); ok {
		t.Fatal("second oldest terminal job was not pruned")
	}
	if _, ok := manager.Get("job-active"); !ok {
		t.Fatal("active job was pruned")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var jobs []Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		t.Fatal(err)
	}
	if len(jobs) != maxStoredTerminalJobs+1 {
		t.Fatalf("stored jobs = %d, want %d", len(jobs), maxStoredTerminalJobs+1)
	}
}

func TestJobManagerTrimsStoredProgressEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.json")
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	manager := NewJobManager(path)
	manager.now = func() time.Time { return now }
	progress := make([]service.ProgressEvent, 0, maxStoredProgressEvents+3)
	for i := 0; i < maxStoredProgressEvents+3; i++ {
		progress = append(progress, service.ProgressEvent{Type: "records", Page: i + 1})
	}
	manager.mu.Lock()
	manager.jobs["job-000001"] = &Job{ID: "job-000001", Type: "fake", Status: JobStatusSucceeded, CreatedAt: now, UpdatedAt: now, FinishedAt: &now, Progress: progress}
	if err := manager.saveLocked(); err != nil {
		manager.mu.Unlock()
		t.Fatal(err)
	}
	manager.mu.Unlock()

	job, ok := manager.Get("job-000001")
	if !ok {
		t.Fatal("job not stored")
	}
	if len(job.Progress) != maxStoredProgressEvents {
		t.Fatalf("progress events = %d, want %d", len(job.Progress), maxStoredProgressEvents)
	}
	if job.Progress[0].Page != 4 {
		t.Fatalf("first kept progress page = %d, want 4", job.Progress[0].Page)
	}
}

func TestJobManagerRetentionTTLsAndDiagnosticCohort(t *testing.T) {
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	manager := NewJobManagerWithRetention(filepath.Join(t.TempDir(), "jobs.json"), config.ServiceJobRetentionConfig{
		SuccessTTL: 2 * time.Hour, DiagnosticTTL: 24 * time.Hour,
		MaxTerminalJobs: 10, MaxDiagnosticJobs: 2, MaxProgressEvents: 5,
	})
	manager.now = func() time.Time { return now }
	add := func(id, status, registration string, age time.Duration) {
		finished := now.Add(-age)
		manager.jobs[id] = &Job{ID: id, Type: "sync", RegistrationID: registration, Status: status, CreatedAt: finished.Add(-time.Minute), UpdatedAt: finished, FinishedAt: &finished}
	}
	manager.mu.Lock()
	add("job-success-expired", JobStatusSucceeded, "reg-a", 2*time.Hour)
	add("job-success-recent", JobStatusSucceeded, "reg-a", time.Hour)
	add("job-failure-old", JobStatusFailed, "reg-a", 72*time.Hour)
	add("job-failure-latest", JobStatusFailed, "reg-a", 48*time.Hour)
	add("job-cancelled-recent", JobStatusCancelled, "reg-b", time.Hour)
	manager.jobs["job-active"] = &Job{ID: "job-active", Type: "sync", Status: JobStatusRunning, CreatedAt: now.Add(-365 * 24 * time.Hour), UpdatedAt: now.Add(-time.Hour)}
	manager.mu.Unlock()

	if err := manager.Prune(); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"job-success-expired", "job-failure-old"} {
		if _, ok := manager.Get(id); ok {
			t.Fatalf("%s was not expired", id)
		}
	}
	for _, id := range []string{"job-success-recent", "job-failure-latest", "job-cancelled-recent", "job-active"} {
		if _, ok := manager.Get(id); !ok {
			t.Fatalf("%s was unexpectedly pruned", id)
		}
	}
	snapshot := manager.RetentionSnapshot()
	if snapshot.ExpiredTotal != 2 || snapshot.LastExpired != 2 || snapshot.Active != 1 || snapshot.Terminal != 3 {
		t.Fatalf("retention snapshot=%+v", snapshot)
	}
}

func TestJobManagerPrunesOnLoadAndPreservesInterruptedJob(t *testing.T) {
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "jobs.json")
	old := now.Add(-3 * time.Hour)
	jobs := []Job{
		{ID: "job-000001", Type: "sync", Status: JobStatusSucceeded, CreatedAt: old, UpdatedAt: old, FinishedAt: &old},
		{ID: "job-000002", Type: "sync", RegistrationID: "reg-a", Status: JobStatusRunning, CreatedAt: old, UpdatedAt: old},
	}
	data, err := json.Marshal(jobs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	manager := NewJobManagerWithRetention(path, config.ServiceJobRetentionConfig{
		SuccessTTL: time.Hour, DiagnosticTTL: 24 * time.Hour,
		MaxTerminalJobs: 8, MaxDiagnosticJobs: 2, MaxProgressEvents: 8,
	})
	manager.now = func() time.Time { return now }
	if err := manager.LoadAndMarkInterrupted(); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.Get("job-000001"); ok {
		t.Fatal("expired success survived load pruning")
	}
	interrupted, ok := manager.Get("job-000002")
	if !ok || interrupted.Status != JobStatusInterrupted {
		t.Fatalf("restart job=%+v, retained=%t", interrupted, ok)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("jobs.json mode=%#o, want 0600", got)
	}
}

func TestJobManagerPrunesOnCompletion(t *testing.T) {
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	manager := NewJobManagerWithRetention(filepath.Join(t.TempDir(), "jobs.json"), config.ServiceJobRetentionConfig{
		SuccessTTL: time.Hour, DiagnosticTTL: 24 * time.Hour,
		MaxTerminalJobs: 8, MaxDiagnosticJobs: 2, MaxProgressEvents: 8,
	})
	manager.now = func() time.Time { return now }
	old := now.Add(-2 * time.Hour)
	manager.jobs["job-old"] = &Job{ID: "job-old", Type: "sync", Status: JobStatusSucceeded, CreatedAt: old, UpdatedAt: old, FinishedAt: &old}
	manager.jobs["job-current"] = &Job{ID: "job-current", Type: "fake", Status: JobStatusRunning, CreatedAt: now, UpdatedAt: now}
	manager.finishJob("job-current", JobStatusSucceeded, "")
	if _, ok := manager.Get("job-old"); ok {
		t.Fatal("completion did not prune expired history")
	}
	if current, ok := manager.Get("job-current"); !ok || current.Status != JobStatusSucceeded {
		t.Fatalf("completed job=%+v, retained=%t", current, ok)
	}
}

func TestJobManagerPruneRollsBackWholeLiveStateWhenSnapshotWriteFails(t *testing.T) {
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	manager := NewJobManagerWithRetention(filepath.Join(t.TempDir(), "jobs.json"), config.ServiceJobRetentionConfig{
		SuccessTTL: time.Hour, DiagnosticTTL: 24 * time.Hour,
		MaxTerminalJobs: 8, MaxDiagnosticJobs: 2, MaxProgressEvents: 1,
	})
	manager.now = func() time.Time { return now }
	old := now.Add(-2 * time.Hour)
	manager.jobs["job-expired"] = &Job{
		ID: "job-expired", Type: SyncJobType, Status: JobStatusSucceeded, CreatedAt: old, UpdatedAt: old, FinishedAt: &old,
		Progress: []service.ProgressEvent{{Type: "one"}, {Type: "two"}},
	}
	manager.cancel["job-expired"] = func() {}
	manager.writeFile = func(string, []byte, os.FileMode) error { return fmt.Errorf("snapshot unavailable") }

	if err := manager.Prune(); err == nil {
		t.Fatal("expected snapshot write failure")
	}
	retained, ok := manager.Get("job-expired")
	if !ok || len(retained.Progress) != 2 {
		t.Fatalf("failed prune changed live job state: retained=%t job=%+v", ok, retained)
	}
	if _, ok := manager.cancel["job-expired"]; !ok {
		t.Fatal("failed prune removed live cancellation state")
	}
	snapshot := manager.RetentionSnapshot()
	if snapshot.ExpiredTotal != 0 || snapshot.TruncatedTotal != 0 || snapshot.LastPrunedAt != nil {
		t.Fatalf("failed prune advanced retention counters: %+v", snapshot)
	}

	manager.writeFile = durableAtomicWriteFile
	if err := manager.Prune(); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.Get("job-expired"); ok {
		t.Fatal("successful retry did not prune expired job")
	}
}

func TestJobManagerRetentionPinsUnsettledActionIntent(t *testing.T) {
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	manager := NewJobManagerWithRetention(filepath.Join(t.TempDir(), "jobs.json"), config.ServiceJobRetentionConfig{
		SuccessTTL: time.Hour, DiagnosticTTL: time.Hour,
		MaxTerminalJobs: 1, MaxDiagnosticJobs: 1, MaxProgressEvents: 8,
	})
	manager.now = func() time.Time { return now }
	old := now.Add(-2 * time.Hour)
	manager.jobs["job-pinned"] = &Job{ID: "job-pinned", Type: SyncJobType, Status: JobStatusSucceeded, CreatedAt: old, UpdatedAt: old, FinishedAt: &old, ActionIntentRefs: []string{"intent-ref"}}
	manager.jobs["job-recent"] = &Job{ID: "job-recent", Type: SyncJobType, Status: JobStatusSucceeded, CreatedAt: now, UpdatedAt: now, FinishedAt: &now}
	if err := manager.Prune(); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.Get("job-pinned"); !ok {
		t.Fatal("unsettled action correlation was pruned")
	}
	if err := manager.ReleaseActionIntent("intent-ref"); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.Get("job-pinned"); ok {
		t.Fatal("settled action correlation did not release expired job")
	}
}

func TestJobManagerReleaseActionIntentKeepsPinWhenPersistenceFails(t *testing.T) {
	manager := NewJobManager(filepath.Join(t.TempDir(), "jobs.json"))
	manager.jobs["job-pinned"] = &Job{ID: "job-pinned", Type: SyncJobType, Status: JobStatusFailed, ActionIntentRefs: []string{"intent-ref"}}
	manager.writeFile = func(string, []byte, os.FileMode) error { return fmt.Errorf("disk unavailable") }

	if err := manager.ReleaseActionIntent("intent-ref"); err == nil {
		t.Fatal("expected persistence failure")
	}
	job, ok := manager.Get("job-pinned")
	if !ok || len(job.ActionIntentRefs) != 1 || job.ActionIntentRefs[0] != "intent-ref" {
		t.Fatalf("failed release must retain correlation pin: %+v, retained=%t", job, ok)
	}
}

func TestJobActionIntentCorrelationIsPrivateSnapshotState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	manager := NewJobManager(path)
	manager.jobs["job-pinned"] = &Job{ID: "job-pinned", Type: SyncJobType, Status: JobStatusFailed, ActionIntentRefs: []string{"intent-ref"}, ActionIntentOutcomes: map[string]string{"intent-ref": "created"}}
	if err := manager.Prune(); err != nil {
		t.Fatal(err)
	}
	publicJSON, err := json.Marshal(manager.mustGet("job-pinned"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(publicJSON), "action_intent") || strings.Contains(string(publicJSON), "intent-ref") {
		t.Fatalf("public job JSON leaked retry correlation: %s", publicJSON)
	}
	privateJSON, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(privateJSON), "action_intent_refs") || !strings.Contains(string(privateJSON), "action_intent_outcomes") {
		t.Fatalf("private snapshot omitted durable retry correlation: %s", privateJSON)
	}
	restarted := NewJobManager(path)
	if err := restarted.LoadAndMarkInterrupted(); err != nil {
		t.Fatal(err)
	}
	_, outcome, found := restarted.RetainedRetryIntentResult("intent-ref")
	if !found || outcome != "created" {
		t.Fatalf("private correlation was not recovered: outcome=%q found=%t", outcome, found)
	}
}

func TestJobManagerIdleReconcilePrunesExpiredHistory(t *testing.T) {
	now := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	jobs := NewJobManagerWithRetention(filepath.Join(t.TempDir(), "jobs.json"), config.ServiceJobRetentionConfig{
		SuccessTTL: time.Hour, DiagnosticTTL: 2 * time.Hour,
		MaxTerminalJobs: 8, MaxDiagnosticJobs: 2, MaxProgressEvents: 8,
	})
	jobs.now = func() time.Time { return now }
	finished := now.Add(-2 * time.Hour)
	jobs.mu.Lock()
	jobs.jobs["job-expired"] = &Job{ID: "job-expired", Type: "sync", Status: JobStatusSucceeded, CreatedAt: finished, UpdatedAt: finished, FinishedAt: &finished}
	jobs.mu.Unlock()
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), jobs, filepath.Join(t.TempDir(), "managed-caches.json"))
	maintenance.now = func() time.Time { return now }
	if _, err := maintenance.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := jobs.Get("job-expired"); ok {
		t.Fatal("idle reconcile did not prune expired job")
	}
}

func waitForManagerJobTerminal(t *testing.T, manager *JobManager, id string) Job {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		job, ok := manager.Get(id)
		if !ok {
			t.Fatalf("job %s not found", id)
		}
		switch job.Status {
		case JobStatusSucceeded, JobStatusSuperseded, JobStatusFailed, JobStatusCancelled, JobStatusInterrupted:
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s did not finish before cleanup: %#v", id, job)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitForTestClient(t *testing.T, manager Manager, errCh <-chan error) *RPCClient {
	t.Helper()
	client, err := manager.Client()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var status Status
		err := client.Call(context.Background(), "Service.Status", nil, &status)
		if err == nil {
			return client
		}
		select {
		case runErr := <-errCh:
			t.Fatalf("service run exited before socket became ready: %v", runErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("service socket did not become ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForJobStatus(t *testing.T, client *RPCClient, id string, action string) Job {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var job Job
		var err error
		if action == "cancel" {
			err = client.Call(context.Background(), "Jobs.Cancel", map[string]string{"job_id": id}, &job)
		} else {
			err = client.Call(context.Background(), "Jobs.Get", map[string]string{"job_id": id}, &job)
		}
		if err != nil {
			t.Fatal(err)
		}
		if jobTerminalStatus(job.Status) {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not reach terminal status: %#v", job)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
