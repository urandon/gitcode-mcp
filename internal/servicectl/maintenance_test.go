package servicectl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/adminhttp"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/service"
)

func TestMaintenanceRegistryEnrollReplayAndSanitize(t *testing.T) {
	ctx := context.Background()
	manager := newTestManager(t, "darwin")
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	jobs := NewJobManager(filepath.Join(dir, "jobs.json"))
	registryPath := filepath.Join(dir, "managed-caches.json")
	maintenance := NewMaintenanceManager(manager, jobs, registryPath)
	req := testMaintenanceEnrollRequest(cachePath, "enroll-1", MaintenancePolicy{RAGEnabled: false})
	first, err := maintenance.Enroll(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	changedConfig := testMaintenanceEnrollRequest(cachePath, "enroll-1", MaintenancePolicy{RAGEnabled: false})
	changedConfig.ConfigSnapshot.MaxRetries++
	changedConfig.ConfigHash = maintenanceHash(changedConfig.ConfigSnapshot)
	if _, err := maintenance.Enroll(ctx, changedConfig); err == nil {
		t.Fatal("idempotency key reuse with changed config succeeded")
	} else if coded, ok := err.(interface{ DiagnosticCode() string }); !ok || coded.DiagnosticCode() != "idempotency_conflict" {
		t.Fatalf("changed-config replay error=%T %v", err, err)
	}
	if _, err := maintenance.Enroll(ctx, testMaintenanceEnrollRequest(cachePath, "enroll-1", MaintenancePolicy{RAGEnabled: true})); err == nil {
		t.Fatal("idempotency key reuse with changed policy succeeded")
	} else if coded, ok := err.(interface{ DiagnosticCode() string }); !ok || coded.DiagnosticCode() != "idempotency_conflict" {
		t.Fatalf("changed-policy replay error=%T %v", err, err)
	}
	second, err := maintenance.Enroll(ctx, req)
	if err != nil || first.RegistrationID == "" || second.RegistrationID != first.RegistrationID || second.Generation != first.Generation {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
	updated, err := maintenance.Enroll(ctx, testMaintenanceEnrollRequest(cachePath, "enroll-2", MaintenancePolicy{RAGEnabled: true}))
	if err != nil || !updated.Policy.RAGEnabled || updated.Generation <= first.Generation {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	listed, err := maintenance.List(ctx)
	if err != nil || len(listed.Entries) != 1 {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	publicJSON, _ := json.Marshal(listed)
	if strings.Contains(string(publicJSON), cachePath) {
		t.Fatalf("public result leaked cache path: %s", publicJSON)
	}
	disk, err := os.ReadFile(registryPath)
	if err != nil || !strings.Contains(string(disk), cachePath) {
		t.Fatalf("registry must persist local path: %s err=%v", disk, err)
	}
	if strings.Contains(string(disk), "enroll-1") || strings.Contains(string(disk), "enroll-2") {
		t.Fatalf("registry leaked raw idempotency key: %s", disk)
	}
	info, err := os.Stat(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("registry mode=%v", info.Mode().Perm())
	}
	reloaded := NewMaintenanceManager(manager, jobs, registryPath)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	reloadedList, _ := reloaded.List(ctx)
	if len(reloadedList.Entries) != 1 || reloadedList.Entries[0].RegistrationID != first.RegistrationID {
		t.Fatalf("reloaded=%+v", reloadedList)
	}
}

func TestMaintenancePolicyChangeClearsInheritedSyncFailureAndIgnoresDeselectedFrontier(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues, cache.RepositoryScopeWiki}}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	jobs := NewJobManager(filepath.Join(dir, "jobs.json"))
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), jobs, filepath.Join(dir, "managed-caches.json"))
	before, err := maintenance.Enroll(ctx, testMaintenanceEnrollRequest(cachePath, "enroll-wiki", MaintenancePolicy{SyncEnabled: true, SyncMode: "head-and-backfill", Issues: true, Wiki: true}))
	if err != nil {
		t.Fatal(err)
	}
	failedAt := time.Date(2026, 8, 25, 16, 43, 37, 0, time.UTC)
	maintenance.entries[before.RegistrationID].SyncStage = MaintenanceStageState{Status: JobStatusFailed, LastErrorClass: "sync_failed", LastError: "remote cache maintenance failed", ConsecutiveFailures: 7, RetryAfter: failedAt.Add(time.Hour), UpdatedAt: failedAt}
	maintenance.entries[before.RegistrationID].Frontiers = []cache.MaintenanceFrontier{{RemoteType: "wiki", Lane: "tail", Status: "degraded", UpdatedAt: failedAt}}
	after, err := maintenance.Enroll(ctx, testMaintenanceEnrollRequest(cachePath, "enroll-no-wiki", MaintenancePolicy{SyncEnabled: true, SyncMode: "head-and-backfill", Issues: true}))
	if err != nil {
		t.Fatal(err)
	}
	if after.SyncStage.LastErrorClass != "" || !after.SyncStage.RetryAfter.IsZero() {
		t.Fatalf("inherited sync failure survived policy change: %+v", after.SyncStage)
	}
	after.LastErrorClass, after.LastError = maintenanceEntryError(after.Policy, after.SyncStage, after.RAGStage)
	if state := deriveMaintenanceEntryState(after); state != "ready" {
		t.Fatalf("deselected wiki frontier degraded active policy: state=%q entry=%+v", state, after)
	}
}

func TestMaintenanceReconcileReadsGenerationWithoutStartingDisabledJobs(t *testing.T) {
	ctx := context.Background()
	manager := newTestManager(t, "darwin")
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertSource(ctx, cache.Source{RepoID: "owner/repo", ID: "ISSUE-1", Kind: "issue", Path: "issues/1.md", Title: "one", ContentHash: "hash-1"}); err != nil {
		t.Fatal(err)
	}
	wantState, _ := store.GetRepoContentState(ctx, "owner/repo")
	store.Close()
	jobs := NewJobManager(filepath.Join(dir, "jobs.json"))
	maintenance := NewMaintenanceManager(manager, jobs, filepath.Join(dir, "managed-caches.json"))
	entry, err := maintenance.Enroll(ctx, testMaintenanceEnrollRequest(cachePath, "enroll-1", MaintenancePolicy{}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := maintenance.Reconcile(ctx)
	if err != nil || len(result.Entries) != 1 || len(result.JobsStarted) != 0 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	got := result.Entries[0]
	if got.RegistrationID != entry.RegistrationID || got.ContentGeneration != wantState.ContentGeneration || got.State != "ready" {
		t.Fatalf("got=%+v want generation=%d", got, wantState.ContentGeneration)
	}
}

func TestMaintenancePollsRegisteredRepositoryDocsSource(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()

	repoPath := filepath.Join(dir, "repo")
	if err := os.MkdirAll(filepath.Join(repoPath, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoPath, "docs", "guide.md"), []byte("# Guide\n\nfirst committed documentation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForRepositoryDocsJobTest(t, repoPath, "init")
	runGitForRepositoryDocsJobTest(t, repoPath, "add", "docs/guide.md")
	runGitForRepositoryDocsJobTest(t, repoPath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "first")

	cfg := adminFakeRAGConfig()
	cfg.CachePath = cachePath
	cfg.LockPath = cachePath + ".lock"
	manager := Manager{EffectiveConfig: &cfg}
	jobs := NewJobManager(filepath.Join(dir, "jobs.json"))
	registryPath := filepath.Join(dir, "managed-caches.json")
	maintenance := NewMaintenanceManager(manager, jobs, registryPath)
	enroll := testMaintenanceEnrollRequest(cachePath, "repo-docs-enroll", MaintenancePolicy{})
	enroll.ConfigSnapshot = cfg
	enroll.ConfigHash = maintenanceHash(cfg)
	entry, err := maintenance.Enroll(ctx, enroll)
	if err != nil {
		t.Fatal(err)
	}
	registered, ok, err := maintenance.RegisterRepositoryDocsSource(ctx, identity.UUID, "owner/repo", repoPath, "")
	if err != nil || !ok || registered.RepositoryDocs == nil || registered.RepositoryDocs.GitStoreRef == "" {
		t.Fatalf("registration=%+v ok=%v err=%v", registered, ok, err)
	}
	replayed, ok, err := maintenance.RegisterRepositoryDocsSource(ctx, identity.UUID, "owner/repo", repoPath, "")
	if err != nil || !ok || replayed.Generation != registered.Generation {
		t.Fatalf("replayed registration=%+v ok=%v err=%v", replayed, ok, err)
	}
	publicJSON, _ := json.Marshal(registered)
	if strings.Contains(string(publicJSON), repoPath) {
		t.Fatalf("public registration leaked repository path: %s", publicJSON)
	}
	disk, err := os.ReadFile(registryPath)
	if err != nil || !strings.Contains(string(disk), repoPath) {
		t.Fatalf("private registry must retain repository path: %s err=%v", disk, err)
	}
	maintenance.entries[entry.RegistrationID].RepositoryDocs.Stage.RetryAfter = time.Now().Add(time.Hour)
	backedOff, err := maintenance.ReconcileRegistration(ctx, entry.RegistrationID)
	if err != nil || len(backedOff.JobsStarted) != 0 {
		t.Fatalf("backed-off reconcile=%+v err=%v", backedOff, err)
	}
	maintenance.entries[entry.RegistrationID].RepositoryDocs.Stage.RetryAfter = time.Time{}

	result, err := maintenance.ReconcileRegistration(ctx, entry.RegistrationID)
	if err != nil || len(result.JobsStarted) != 1 {
		t.Fatalf("initial reconcile=%+v err=%v", result, err)
	}
	waitForTerminalJob(t, jobs, result.JobsStarted[0])
	result, err = maintenance.ReconcileRegistration(ctx, entry.RegistrationID)
	if err != nil || len(result.JobsStarted) != 0 || len(result.Entries) != 1 || result.Entries[0].RepositoryDocs == nil || result.Entries[0].RepositoryDocs.State != "ready" {
		t.Fatalf("ready reconcile=%+v err=%v", result, err)
	}
	firstSet := result.Entries[0].RepositoryDocs.RevisionSetID

	if err := os.WriteFile(filepath.Join(repoPath, "docs", "guide.md"), []byte("# Guide\n\nsecond committed documentation\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitForRepositoryDocsJobTest(t, repoPath, "add", "docs/guide.md")
	runGitForRepositoryDocsJobTest(t, repoPath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "second")
	result, err = maintenance.ReconcileRegistration(ctx, entry.RegistrationID)
	if err != nil || len(result.JobsStarted) != 1 {
		t.Fatalf("changed HEAD reconcile=%+v err=%v", result, err)
	}
	waitForTerminalJob(t, jobs, result.JobsStarted[0])
	result, err = maintenance.ReconcileRegistration(ctx, entry.RegistrationID)
	if err != nil || result.Entries[0].RepositoryDocs.State != "ready" || result.Entries[0].RepositoryDocs.RevisionSetID == firstSet {
		t.Fatalf("updated ready reconcile=%+v err=%v", result, err)
	}

	reloaded := NewMaintenanceManager(manager, jobs, registryPath)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	reloadedResult, err := reloaded.ReconcileRegistration(ctx, entry.RegistrationID)
	if err != nil || len(reloadedResult.JobsStarted) != 0 || reloadedResult.Entries[0].RepositoryDocs.State != "ready" {
		t.Fatalf("restart reconcile=%+v err=%v", reloadedResult, err)
	}
}

func TestRepositoryDocsSourceRegistrationSupportsMultipleAuthorities(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	repoA := createRepositoryDocsRegistrationRepo(t, filepath.Join(dir, "repo-a"), "alpha")
	repoB := createRepositoryDocsRegistrationRepo(t, filepath.Join(dir, "repo-b"), "beta")
	registryPath := filepath.Join(dir, "managed-caches.json")
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	entry, err := maintenance.Enroll(ctx, testMaintenanceEnrollRequest(cachePath, "enroll-source-conflict", MaintenancePolicy{}))
	if err != nil {
		t.Fatal(err)
	}
	registered, ok, err := maintenance.RegisterRepositoryDocsSource(ctx, identity.UUID, entry.RepoID, repoA, "profile-a")
	if err != nil || !ok || registered.RepositoryDocs == nil || registered.RepositoryDocs.SourceRegistrationID == "" || registered.RepositoryDocs.SourceRegistrationGeneration < 1 {
		t.Fatalf("registered=%+v ok=%v err=%v", registered, ok, err)
	}
	sole, err := maintenance.repositoryDocsSourceForAdmin(entry.RegistrationID)
	if err != nil || sole.SourceRegistrationID != registered.RepositoryDocs.SourceRegistrationID {
		t.Fatalf("sole no-selector source=%+v err=%v", sole, err)
	}

	second, ok, err := maintenance.RegisterRepositoryDocsSource(ctx, identity.UUID, entry.RepoID, repoB, "profile-a")
	if err != nil || !ok || second.RepositoryDocs == nil || second.RepositoryDocs.SourceRegistrationID == registered.RepositoryDocs.SourceRegistrationID {
		t.Fatalf("second=%+v ok=%v err=%v", second, ok, err)
	}
	replayed, ok, err := maintenance.RegisterRepositoryDocsSource(ctx, identity.UUID, entry.RepoID, repoB, "profile-a")
	if err != nil || !ok || replayed.RepositoryDocs.SourceRegistrationID != second.RepositoryDocs.SourceRegistrationID || replayed.RepositoryDocs.SourceRegistrationGeneration != second.RepositoryDocs.SourceRegistrationGeneration {
		t.Fatalf("replayed=%+v ok=%v err=%v", replayed, ok, err)
	}
	_, err = maintenance.repositoryDocsSourceForAdmin(entry.RegistrationID)
	var ambiguous RepositoryDocsSourceAmbiguousError
	if !errors.As(err, &ambiguous) || ambiguous.DiagnosticCode() != "repository_docs_source_ambiguous" {
		t.Fatalf("no-selector error=%T %v", err, err)
	}
	selected, err := maintenance.repositoryDocsSourceForSelector(RepositoryDocsSourceSelector{RegistrationID: entry.RegistrationID, SourceRegistrationID: registered.RepositoryDocs.SourceRegistrationID, SourceRegistrationGeneration: registered.RepositoryDocs.SourceRegistrationGeneration})
	if err != nil || selected.SourceRegistrationID != registered.RepositoryDocs.SourceRegistrationID {
		t.Fatalf("selected=%+v err=%v", selected, err)
	}
	listed, err := maintenance.List(ctx)
	if err != nil || len(listed.Entries) != 1 || listed.Entries[0].RepositoryDocs == nil || len(listed.Entries[0].RepositoryDocsSources) != 2 {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	if got := listed.Entries[0]; got.Generation <= registered.Generation {
		t.Fatalf("second authority did not advance registration generation: before=%+v after=%+v", registered, got)
	}
	disk, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(disk, []byte(repoA)) || !bytes.Contains(disk, []byte(repoB)) {
		t.Fatalf("registry did not retain both private bindings: %s", disk)
	}
	reloaded := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := reloaded.Load(); err != nil {
		t.Fatal(err)
	}
	replayedAfterRestart, ok, err := reloaded.RegisterRepositoryDocsSource(ctx, identity.UUID, entry.RepoID, repoB, "profile-a")
	if err != nil || !ok || replayedAfterRestart.RepositoryDocs.SourceRegistrationID != second.RepositoryDocs.SourceRegistrationID || replayedAfterRestart.RepositoryDocs.SourceRegistrationGeneration != second.RepositoryDocs.SourceRegistrationGeneration {
		t.Fatalf("restart replay=%+v ok=%v err=%v", replayedAfterRestart, ok, err)
	}
}

func TestRepositoryDocsSourceRebindUsesGenerationCAS(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	store.Close()
	repoA := createRepositoryDocsRegistrationRepo(t, filepath.Join(dir, "repo-a"), "alpha")
	repoB := createRepositoryDocsRegistrationRepo(t, filepath.Join(dir, "repo-b"), "beta")
	jobs := NewJobManager(filepath.Join(dir, "jobs.json"))
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), jobs, filepath.Join(dir, "managed-caches.json"))
	entry, err := maintenance.Enroll(ctx, testMaintenanceEnrollRequest(cachePath, "enroll-rebind", MaintenancePolicy{}))
	if err != nil {
		t.Fatal(err)
	}
	registered, _, err := maintenance.RegisterRepositoryDocsSource(ctx, identity.UUID, entry.RepoID, repoA, "profile-a")
	if err != nil {
		t.Fatal(err)
	}
	_, created, err := jobs.createCoalescedJobWithIntent(
		RepositoryDocsIndexJobType, registered.RepoID, "profile-a", 0, "old-source-work", registered.CacheUUID, registered.RegistrationID, "namespace-old",
		JobRecoveryIntent{SourceRegistrationID: registered.RepositoryDocs.SourceRegistrationID, SourceRegistrationGeneration: registered.RepositoryDocs.SourceRegistrationGeneration, ExpectedRevisionSetID: "set-old"},
		func() {},
	)
	if err != nil || !created {
		t.Fatalf("old generation job created=%v err=%v", created, err)
	}
	maintenance.mu.Lock()
	maintenance.admissions[registered.RegistrationID] = repositoryDocsAdmissionIntent{
		RegistrationID: registered.RegistrationID, SourceRegistrationID: registered.RepositoryDocs.SourceRegistrationID,
		SourceRegistrationGeneration: registered.RepositoryDocs.SourceRegistrationGeneration,
		RepoID:                       registered.RepoID, WorkKey: "old-source-pending-work", ExpectedRevisionSetID: "set-old-pending", CreatedAt: time.Now().UTC(),
	}
	if err := maintenance.saveLocked(); err != nil {
		maintenance.mu.Unlock()
		t.Fatal(err)
	}
	maintenance.mu.Unlock()

	_, err = maintenance.RebindRepositoryDocsSource(ctx, RepositoryDocsSourceRebindRequest{
		RegistrationID: registered.RegistrationID, ExpectedGeneration: registered.RepositoryDocs.SourceRegistrationGeneration - 1,
		RepositoryPath: repoB, Profile: "profile-b",
	})
	var generationConflict RepositoryDocsSourceGenerationConflictError
	if !errors.As(err, &generationConflict) || generationConflict.DiagnosticCode() != "repository_docs_source_generation_conflict" {
		t.Fatalf("stale rebind error=%T %v", err, err)
	}
	rebound, err := maintenance.RebindRepositoryDocsSource(ctx, RepositoryDocsSourceRebindRequest{
		RegistrationID: registered.RegistrationID, ExpectedGeneration: registered.RepositoryDocs.SourceRegistrationGeneration,
		RepositoryPath: repoB, Profile: "profile-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rebound.RepositoryDocs.SourceRegistrationID != registered.RepositoryDocs.SourceRegistrationID || rebound.RepositoryDocs.SourceRegistrationGeneration != registered.RepositoryDocs.SourceRegistrationGeneration+1 || rebound.RepositoryDocs.GitStoreRef == registered.RepositoryDocs.GitStoreRef {
		t.Fatalf("rebound=%+v registered=%+v", rebound, registered)
	}
	oldJob, ok := jobs.ActiveWork("old-source-work")
	if ok {
		t.Fatalf("old source generation remained active after rebind: %+v", oldJob)
	}
	listedJobs := jobs.List()
	if len(listedJobs) != 1 || listedJobs[0].Status != JobStatusSuperseded || listedJobs[0].ErrorClass != "repository_docs_source_generation_superseded" {
		t.Fatalf("old source generation was not durably fenced: %+v", listedJobs)
	}
	if pending, ok := maintenance.repositoryDocsAdmission(registered.RegistrationID); ok {
		t.Fatalf("old source generation admission survived rebind: %+v", pending)
	}
	registryBytes, err := os.ReadFile(maintenance.path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(registryBytes, []byte("set-old-pending")) {
		t.Fatalf("old source generation admission remained durable after rebind: %s", registryBytes)
	}
	replayed, err := maintenance.RebindRepositoryDocsSource(ctx, RepositoryDocsSourceRebindRequest{
		RegistrationID: rebound.RegistrationID, ExpectedGeneration: rebound.RepositoryDocs.SourceRegistrationGeneration,
		RepositoryPath: repoB, Profile: "profile-b",
	})
	if err != nil || replayed.RepositoryDocs.SourceRegistrationGeneration != rebound.RepositoryDocs.SourceRegistrationGeneration {
		t.Fatalf("idempotent rebind=%+v err=%v", replayed, err)
	}
	repoC := createRepositoryDocsRegistrationRepo(t, filepath.Join(dir, "repo-c"), "gamma")
	repoD := createRepositoryDocsRegistrationRepo(t, filepath.Join(dir, "repo-d"), "delta")
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, repositoryPath := range []string{repoC, repoD} {
		repositoryPath := repositoryPath
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, rebindErr := maintenance.RebindRepositoryDocsSource(ctx, RepositoryDocsSourceRebindRequest{
				RegistrationID: rebound.RegistrationID, ExpectedGeneration: rebound.RepositoryDocs.SourceRegistrationGeneration,
				RepositoryPath: repositoryPath, Profile: "profile-b",
			})
			errs <- rebindErr
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	succeeded, conflicted := 0, 0
	for err := range errs {
		if err == nil {
			succeeded++
			continue
		}
		var stale RepositoryDocsSourceGenerationConflictError
		if errors.As(err, &stale) {
			conflicted++
			continue
		}
		t.Fatalf("concurrent rebind error=%T %v", err, err)
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent rebind outcomes succeeded=%d conflicted=%d", succeeded, conflicted)
	}
}

func TestRepositoryDocsPendingAdmissionReplaysAfterRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	repoPath := createRepositoryDocsRegistrationRepo(t, filepath.Join(dir, "repo"), "restart replay")
	cfg := adminFakeRAGConfig()
	cfg.CachePath = cachePath
	cfg.LockPath = cachePath + ".lock"
	manager := Manager{EffectiveConfig: &cfg}
	registryPath := filepath.Join(dir, "managed-caches.json")
	jobsPath := filepath.Join(dir, "jobs.json")
	beforeJobs := NewJobManager(jobsPath)
	before := NewMaintenanceManager(manager, beforeJobs, registryPath)
	enroll := testMaintenanceEnrollRequest(cachePath, "pending-restart", MaintenancePolicy{})
	enroll.ConfigSnapshot = cfg
	enroll.ConfigHash = maintenanceHash(cfg)
	if _, err := before.Enroll(ctx, enroll); err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareRepositoryDocsIndex(ctx, manager, StartRepositoryDocsIndexJobRequest{RepoID: "owner/repo", RepositoryPath: repoPath, CachePath: cachePath})
	if err != nil {
		t.Fatal(err)
	}
	entry, prepared, registered, err := before.registerAndRecordRepositoryDocsAdmission(prepared)
	if err != nil || !registered || entry.RepositoryDocs == nil {
		t.Fatalf("registration=%+v registered=%v err=%v", entry, registered, err)
	}
	wantSetID := repositoryDocsRevisionSetIdentity(prepared.request, prepared.repository, prepared.policy, prepared.namespaceID).ID()
	wantWorkKey := repositoryDocsIndexWorkKey(prepared.request, prepared.repository, prepared.policy, prepared.namespaceID)
	queuedBeforeRestart, created, err := beforeJobs.createCoalescedJobWithIntent(
		RepositoryDocsIndexJobType, prepared.request.RepoID, prepared.request.Profile, 0, wantWorkKey, prepared.request.CacheUUID, prepared.request.RegistrationID, prepared.namespaceID,
		JobRecoveryIntent{SourceRegistrationID: prepared.request.SourceRegistrationID, SourceRegistrationGeneration: prepared.request.SourceRegistrationGeneration, ExpectedRevisionSetID: wantSetID}, func() {},
	)
	if err != nil || !created {
		t.Fatalf("durable queued job created=%v err=%v", created, err)
	}

	afterJobs := NewJobManager(jobsPath)
	if err := afterJobs.LoadAndMarkInterrupted(); err != nil {
		t.Fatal(err)
	}
	interrupted, ok := afterJobs.Get(queuedBeforeRestart.ID)
	if !ok || interrupted.Status != JobStatusInterrupted || interrupted.WorkRef != queuedBeforeRestart.WorkRef {
		t.Fatalf("restart did not retain interrupted durable work identity: before=%+v after=%+v", queuedBeforeRestart, interrupted)
	}
	after := NewMaintenanceManager(manager, afterJobs, registryPath)
	if err := after.Load(); err != nil {
		t.Fatal(err)
	}
	result, err := after.ReconcileRegistration(ctx, entry.RegistrationID)
	if err != nil || len(result.JobsStarted) != 1 {
		t.Fatalf("reconcile=%+v err=%v", result, err)
	}
	var job Job
	deadline := time.Now().Add(5 * time.Second)
	for {
		job, _ = afterJobs.Get(result.JobsStarted[0])
		if jobTerminalStatus(job.Status) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("replayed job did not terminate: %+v", job)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if job.ID != queuedBeforeRestart.ID || job.WorkRef != queuedBeforeRestart.WorkRef || job.ExpectedRevisionSetID != wantSetID || job.SourceRegistrationID != entry.RepositoryDocs.SourceRegistrationID || job.SourceRegistrationGeneration != entry.RepositoryDocs.SourceRegistrationGeneration {
		t.Fatalf("replayed job=%+v", job)
	}
	settled, err := after.ReconcileRegistration(ctx, entry.RegistrationID)
	if err != nil || len(settled.JobsStarted) != 0 {
		t.Fatalf("settled reconcile=%+v err=%v", settled, err)
	}
	if pending, ok := after.repositoryDocsAdmission(entry.RegistrationID); ok {
		t.Fatalf("completed durable handoff remained pending: %+v", pending)
	}
	jobsInfo, err := os.Stat(jobsPath)
	if err != nil {
		t.Fatal(err)
	}
	if jobsInfo.Mode().Perm() != 0o600 {
		t.Fatalf("jobs snapshot mode=%#o", jobsInfo.Mode().Perm())
	}
}

func TestRepositoryDocsPendingAdmissionRejectsChangedSnapshotBeforeRelaunch(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	repoPath := createRepositoryDocsRegistrationRepo(t, filepath.Join(dir, "repo"), "before crash")
	cfg := adminFakeRAGConfig()
	cfg.CachePath = cachePath
	cfg.LockPath = cachePath + ".lock"
	manager := Manager{EffectiveConfig: &cfg}
	registryPath := filepath.Join(dir, "managed-caches.json")
	before := NewMaintenanceManager(manager, NewJobManager(filepath.Join(dir, "jobs-before.json")), registryPath)
	enroll := testMaintenanceEnrollRequest(cachePath, "pending-stale", MaintenancePolicy{})
	enroll.ConfigSnapshot = cfg
	enroll.ConfigHash = maintenanceHash(cfg)
	if _, err := before.Enroll(ctx, enroll); err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareRepositoryDocsIndex(ctx, manager, StartRepositoryDocsIndexJobRequest{RepoID: "owner/repo", RepositoryPath: repoPath, CachePath: cachePath})
	if err != nil {
		t.Fatal(err)
	}
	entry, prepared, registered, err := before.registerAndRecordRepositoryDocsAdmission(prepared)
	if err != nil || !registered {
		t.Fatalf("registration=%+v registered=%v err=%v", entry, registered, err)
	}
	wantSetID := repositoryDocsRevisionSetIdentity(prepared.request, prepared.repository, prepared.policy, prepared.namespaceID).ID()
	if err := os.WriteFile(filepath.Join(repoPath, "docs", "guide.md"), []byte("# Guide\n\nafter crash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitForRepositoryDocsJobTest(t, repoPath, "add", "docs/guide.md")
	runGitForRepositoryDocsJobTest(t, repoPath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "changed while daemon down")

	afterJobs := NewJobManager(filepath.Join(dir, "jobs-after.json"))
	after := NewMaintenanceManager(manager, afterJobs, registryPath)
	if err := after.Load(); err != nil {
		t.Fatal(err)
	}
	result, err := after.ReconcileRegistration(ctx, entry.RegistrationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.JobsStarted) != 0 || len(afterJobs.List()) != 0 {
		t.Fatalf("changed durable intent relaunched work: result=%+v jobs=%+v", result, afterJobs.List())
	}
	if len(result.Entries) != 1 || result.Entries[0].RepositoryDocs == nil || result.Entries[0].RepositoryDocs.LastErrorClass != "repository_docs_admission_stale" {
		t.Fatalf("stale admission was not terminalized: %+v", result)
	}
	if pending, ok := after.repositoryDocsAdmission(entry.RegistrationID); ok {
		t.Fatalf("stale admission remained queued: %+v", pending)
	}
	registryBytes, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(registryBytes, []byte(wantSetID)) {
		t.Fatalf("stale expected set remained durable: %s", registryBytes)
	}
}

func TestRepositoryDocsCancellationIsDurableAndSuppressesReconcileSelection(t *testing.T) {
	dir := t.TempDir()
	jobs := NewJobManager(filepath.Join(dir, "jobs.json"))
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), jobs, filepath.Join(dir, "registry.json"))
	registrationID, sourceID := "reg-cancel", "source-cancel"
	maintenance.entries[registrationID] = &MaintenanceEntry{RegistrationID: registrationID, RepoID: "owner/repo"}
	maintenance.sources[registrationID] = map[string]*repositoryDocsRegisteredSource{sourceID: &repositoryDocsRegisteredSource{State: RepositoryDocsMaintenanceState{SourceRegistrationID: sourceID, SourceRegistrationGeneration: 1, State: "indexing"}}}
	maintenance.admissions[repositoryDocsAdmissionKey(registrationID, sourceID)] = repositoryDocsAdmissionIntent{RegistrationID: registrationID, SourceRegistrationID: sourceID, SourceRegistrationGeneration: 1, RepoID: "owner/repo", WorkKey: "work", ExpectedRevisionSetID: "set", JobID: "", Disposition: repositoryDocsAdmissionPending, CreatedAt: time.Now().UTC()}
	jobCtx, cancel := context.WithCancel(context.Background())
	job, created, err := jobs.createCoalescedJobWithIntent(RepositoryDocsIndexJobType, "owner/repo", "profile", 0, "work", "cache", registrationID, "namespace", JobRecoveryIntent{SourceRegistrationID: sourceID, SourceRegistrationGeneration: 1, ExpectedRevisionSetID: "set"}, cancel)
	if err != nil || !created {
		t.Fatalf("job=%+v created=%t err=%v", job, created, err)
	}
	jobs.updateJob(job.ID, func(stored *Job, now time.Time) { stored.Status = JobStatusRunning })
	go func() {
		<-jobCtx.Done()
		jobs.finishJob(job.ID, JobStatusCancelled, "cancelled")
	}()
	if cancelled, ok, cancelErr := jobs.Cancel(job.ID); cancelErr != nil || !ok || cancelled.Status != JobStatusCancelled {
		t.Fatalf("cancelled=%+v ok=%t err=%v", cancelled, ok, cancelErr)
	}
	intent, ok := maintenance.repositoryDocsAdmission(registrationID, sourceID)
	if !ok || intent.Disposition != repositoryDocsAdmissionCancelled || intent.JobID != job.ID || intent.FinishedAt.IsZero() {
		t.Fatalf("durable cancellation=%+v ok=%t", intent, ok)
	}
	maintenance.mu.Lock()
	selected, _, pending := maintenance.selectRepositoryDocsSourceForReconcileLocked(registrationID)
	maintenance.mu.Unlock()
	if selected != nil || pending {
		t.Fatalf("cancelled source selected=%+v pending=%t", selected, pending)
	}
	restarted := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(filepath.Join(dir, "jobs-restarted.json")), filepath.Join(dir, "registry.json"))
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	restarted.mu.Lock()
	selected, _, pending = restarted.selectRepositoryDocsSourceForReconcileLocked(registrationID)
	restarted.mu.Unlock()
	if selected != nil || pending {
		t.Fatalf("restart revived cancelled source selected=%+v pending=%t", selected, pending)
	}
}

func TestRepositoryDocsCancellationFailsClosedWhenTombstoneCannotPersist(t *testing.T) {
	dir := t.TempDir()
	jobs := NewJobManager(filepath.Join(dir, "jobs.json"))
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), jobs, filepath.Join(dir, "registry.json"))
	registrationID, sourceID := "reg-cancel-fail", "source-cancel-fail"
	maintenance.entries[registrationID] = &MaintenanceEntry{RegistrationID: registrationID, RepoID: "owner/repo"}
	maintenance.sources[registrationID] = map[string]*repositoryDocsRegisteredSource{sourceID: {State: RepositoryDocsMaintenanceState{SourceRegistrationID: sourceID, SourceRegistrationGeneration: 1, State: "indexing"}}}
	maintenance.admissions[repositoryDocsAdmissionKey(registrationID, sourceID)] = repositoryDocsAdmissionIntent{RegistrationID: registrationID, SourceRegistrationID: sourceID, SourceRegistrationGeneration: 1, RepoID: "owner/repo", WorkKey: "work", ExpectedRevisionSetID: "set", Disposition: repositoryDocsAdmissionPending, CreatedAt: time.Now().UTC()}
	jobCtx, cancel := context.WithCancel(context.Background())
	job, created, err := jobs.createCoalescedJobWithIntent(RepositoryDocsIndexJobType, "owner/repo", "profile", 0, "work", "cache", registrationID, "namespace", JobRecoveryIntent{SourceRegistrationID: sourceID, SourceRegistrationGeneration: 1, ExpectedRevisionSetID: "set"}, cancel)
	if err != nil || !created {
		t.Fatalf("job=%+v created=%t err=%v", job, created, err)
	}
	jobs.updateJob(job.ID, func(stored *Job, now time.Time) { stored.Status = JobStatusRunning })
	done := make(chan struct{})
	go func() {
		<-jobCtx.Done()
		jobs.finishJob(job.ID, JobStatusCancelled, "cancelled")
		close(done)
	}()

	maintenance.writeFile = func(string, []byte, os.FileMode) error { return errors.New("injected durable write failure") }
	actionsPath := filepath.Join(dir, "actions.json")
	_, actionErr := NewJobActionManager(actionsPath, jobs, nil).Cancel(context.Background(), adminhttp.JobActionRequest{JobID: job.ID, IdempotencyKey: "cancel-persist-failure"})
	var publicErr adminhttp.JobActionError
	if !errors.As(actionErr, &publicErr) || publicErr.Status != 503 || publicErr.Code != "repository_docs_cancel_persist_failed" {
		t.Fatalf("public cancel error=%T %v", actionErr, actionErr)
	}
	current, found := jobs.Get(job.ID)
	if !found || current.Status != JobStatusRunning {
		t.Fatalf("failed-closed cancel current=%+v found=%t", current, found)
	}
	if _, err := os.Stat(actionsPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed cancellation persisted a success receipt: %v", err)
	}
	select {
	case <-done:
		t.Fatal("worker was cancelled before its durable tombstone was committed")
	default:
	}
	intent, ok := maintenance.repositoryDocsAdmission(registrationID, sourceID)
	if !ok || intent.Disposition != repositoryDocsAdmissionPending {
		t.Fatalf("failed tombstone mutated admission=%+v ok=%t", intent, ok)
	}

	maintenance.writeFile = durableAtomicWriteFile
	cancelled, found, cancelErr := jobs.Cancel(job.ID)
	if cancelErr != nil || !found || cancelled.Status != JobStatusCancelled {
		t.Fatalf("retry cancel=%+v found=%t err=%v", cancelled, found, cancelErr)
	}
	<-done
	intent, ok = maintenance.repositoryDocsAdmission(registrationID, sourceID)
	if !ok || intent.Disposition != repositoryDocsAdmissionCancelled || intent.JobID != job.ID {
		t.Fatalf("durable retry tombstone=%+v ok=%t", intent, ok)
	}
}

func TestRepositoryDocsPendingAdmissionRespectsFailureBackoff(t *testing.T) {
	now := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	state := RepositoryDocsMaintenanceState{}
	first := Job{ID: "job-1", Type: RepositoryDocsIndexJobType, Status: JobStatusFailed, ErrorClass: "provider_timeout", UpdatedAt: now}
	state.Stage = observeMaintenanceStage(state.Stage, first, now)
	if state.Stage.ConsecutiveFailures != 1 || repositoryDocsRetryReady(&state, now) {
		t.Fatalf("first failure stage=%+v", state.Stage)
	}
	secondAt := state.Stage.RetryAfter
	second := Job{ID: "job-2", Type: RepositoryDocsIndexJobType, Status: JobStatusFailed, ErrorClass: "provider_timeout", UpdatedAt: secondAt}
	state.Stage = observeMaintenanceStage(state.Stage, second, secondAt)
	if state.Stage.ConsecutiveFailures != 2 || !state.Stage.RetryAfter.Equal(secondAt.Add(2*time.Minute)) || repositoryDocsRetryReady(&state, secondAt) {
		t.Fatalf("second failure stage=%+v", state.Stage)
	}
	if !repositoryDocsRetryReady(&state, state.Stage.RetryAfter) {
		t.Fatalf("retry was not released at %s", state.Stage.RetryAfter)
	}
}

func TestRepositoryDocsFailedAdmissionResumesAfterBackoffWithSameJobID(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	repoPath := createRepositoryDocsRegistrationRepo(t, filepath.Join(dir, "repo"), "failed admission retry")
	cfg := adminFakeRAGConfig()
	cfg.CachePath = cachePath
	cfg.LockPath = cachePath + ".lock"
	manager := Manager{EffectiveConfig: &cfg}
	jobs := NewJobManager(filepath.Join(dir, "jobs.json"))
	maintenance := NewMaintenanceManager(manager, jobs, filepath.Join(dir, "managed-caches.json"))
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	maintenance.now = func() time.Time { return now }
	enroll := testMaintenanceEnrollRequest(cachePath, "failed-retry", MaintenancePolicy{})
	enroll.ConfigSnapshot = cfg
	enroll.ConfigHash = maintenanceHash(cfg)
	if _, err := maintenance.Enroll(ctx, enroll); err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareRepositoryDocsIndex(ctx, manager, StartRepositoryDocsIndexJobRequest{RepoID: "owner/repo", RepositoryPath: repoPath, CachePath: cachePath})
	if err != nil {
		t.Fatal(err)
	}
	entry, prepared, registered, err := maintenance.registerAndRecordRepositoryDocsAdmission(prepared)
	if err != nil || !registered || entry.RepositoryDocs == nil {
		t.Fatalf("registration=%+v registered=%t err=%v", entry, registered, err)
	}
	wantSetID := repositoryDocsRevisionSetIdentity(prepared.request, prepared.repository, prepared.policy, prepared.namespaceID).ID()
	wantWorkKey := repositoryDocsIndexWorkKey(prepared.request, prepared.repository, prepared.policy, prepared.namespaceID)
	failed, created, err := jobs.createCoalescedJobWithIntent(RepositoryDocsIndexJobType, prepared.request.RepoID, prepared.request.Profile, 0, wantWorkKey, prepared.request.CacheUUID, prepared.request.RegistrationID, prepared.namespaceID, JobRecoveryIntent{SourceRegistrationID: prepared.request.SourceRegistrationID, SourceRegistrationGeneration: prepared.request.SourceRegistrationGeneration, ExpectedRevisionSetID: wantSetID}, func() {})
	if err != nil || !created {
		t.Fatalf("failed job created=%t err=%v", created, err)
	}
	jobs.updateJob(failed.ID, func(job *Job, observed time.Time) {
		job.Status = JobStatusFailed
		job.UpdatedAt = observed
		job.FinishedAt = &observed
		job.ErrorClass = "provider_timeout"
		delete(jobs.cancel, job.ID)
	})
	if err := maintenance.bindRepositoryDocsAdmissionJob(prepared.request.RegistrationID, prepared.request.SourceRegistrationID, failed.ID); err != nil {
		t.Fatal(err)
	}

	blocked, err := maintenance.ReconcileRegistration(ctx, entry.RegistrationID)
	if err != nil || len(blocked.JobsStarted) != 0 || len(blocked.Entries) != 1 || blocked.Entries[0].RepositoryDocs == nil {
		t.Fatalf("backoff reconcile=%+v err=%v", blocked, err)
	}
	retryAt := blocked.Entries[0].RepositoryDocs.Stage.RetryAfter
	if retryAt.IsZero() || !retryAt.After(now) {
		t.Fatalf("retry window=%s stage=%+v", retryAt, blocked.Entries[0].RepositoryDocs.Stage)
	}
	now = retryAt
	resumed, err := maintenance.ReconcileRegistration(ctx, entry.RegistrationID)
	if err != nil || len(resumed.JobsStarted) != 1 || resumed.JobsStarted[0] != failed.ID {
		t.Fatalf("resume reconcile=%+v err=%v", resumed, err)
	}
	terminal := waitForTerminalJob(t, jobs, failed.ID)
	if terminal.Status != JobStatusSucceeded || terminal.ExpectedRevisionSetID != wantSetID {
		t.Fatalf("resumed terminal=%+v", terminal)
	}
	now = now.Add(time.Second)
	settled, err := maintenance.ReconcileRegistration(ctx, entry.RegistrationID)
	if err != nil || len(settled.JobsStarted) != 0 || len(settled.Entries) != 1 || settled.Entries[0].RepositoryDocs == nil {
		t.Fatalf("settled reconcile=%+v err=%v", settled, err)
	}
	stage := settled.Entries[0].RepositoryDocs.Stage
	if stage.Status != JobStatusSucceeded || stage.ConsecutiveFailures != 0 || !stage.RetryAfter.IsZero() {
		t.Fatalf("same-id success was not observed: %+v", stage)
	}
	if _, ok := maintenance.repositoryDocsAdmission(entry.RegistrationID, prepared.request.SourceRegistrationID); ok {
		t.Fatal("successful resumed admission remained durable")
	}
}

func createRepositoryDocsRegistrationRepo(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "docs", "guide.md"), []byte("# Guide\n\n"+body+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitForRepositoryDocsJobTest(t, path, "init")
	runGitForRepositoryDocsJobTest(t, path, "add", "docs/guide.md")
	runGitForRepositoryDocsJobTest(t, path, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "docs")
	return path
}

func waitForTerminalJob(t *testing.T, jobs *JobManager, id string) Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		job, ok := jobs.Get(id)
		if ok && jobTerminalStatus(job.Status) {
			if job.Status != JobStatusSucceeded {
				t.Fatalf("job %s finished as %s: %+v", id, job.Status, job)
			}
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s did not finish", id)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestMaintenanceReconcileRegistrationDoesNotTouchOtherCaches(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	manager := newTestManager(t, "darwin")
	maintenance := NewMaintenanceManager(manager, NewJobManager(filepath.Join(dir, "jobs.json")), filepath.Join(dir, "managed-caches.json"))
	var entries []MaintenanceEntry
	for _, name := range []string{"left.db", "right.db"} {
		cachePath := filepath.Join(dir, name)
		store, err := cache.NewSQLiteStore(ctx, cachePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
			t.Fatal(err)
		}
		store.Close()
		entry, err := maintenance.Enroll(ctx, testMaintenanceEnrollRequest(cachePath, "enroll-"+name, MaintenancePolicy{}))
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	result, err := maintenance.ReconcileRegistration(ctx, entries[0].RegistrationID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 || result.Entries[0].RegistrationID != entries[0].RegistrationID || len(result.JobsStarted) != 0 {
		t.Fatalf("scoped reconcile=%+v", result)
	}
	listed, _ := maintenance.List(ctx)
	for _, entry := range listed.Entries {
		if entry.RegistrationID == entries[1].RegistrationID && !entry.LastReconciledAt.IsZero() {
			t.Fatalf("unrelated registration was reconciled: %+v", entry)
		}
	}
}

func TestMaintenanceWorkKeysSeparateSameRepoAcrossCaches(t *testing.T) {
	left := ragIndexWorkKey(StartRAGIndexJobRequest{RepoID: "owner/repo", CacheUUID: "cache-left", NamespaceID: "ns", ChunkPolicy: "heading"})
	right := ragIndexWorkKey(StartRAGIndexJobRequest{RepoID: "owner/repo", CacheUUID: "cache-right", NamespaceID: "ns", ChunkPolicy: "heading"})
	if left == right {
		t.Fatalf("RAG work keys collapsed: %q", left)
	}
	if ragIndexWorkKey(StartRAGIndexJobRequest{RepoID: "owner/repo", CacheUUID: "cache-left", Profile: "old"}) == ragIndexWorkKey(StartRAGIndexJobRequest{RepoID: "owner/repo", CacheUUID: "cache-left", Profile: "current"}) {
		t.Fatal("RAG work keys collapsed distinct profiles")
	}
	if ragIndexWorkKey(StartRAGIndexJobRequest{RepoID: "owner/repo", CacheUUID: "cache-left", MaxChunks: 10}) == ragIndexWorkKey(StartRAGIndexJobRequest{RepoID: "owner/repo", CacheUUID: "cache-left", MaxChunks: 1000}) {
		t.Fatal("RAG work keys collapsed distinct repair bounds")
	}
	head := syncWorkKey(StartSyncJobRequest{RepoID: "owner/repo", CacheUUID: "cache-left", Lane: "head", Issues: true})
	tail := syncWorkKey(StartSyncJobRequest{RepoID: "owner/repo", CacheUUID: "cache-left", Lane: "tail", Issues: true})
	if head == tail {
		t.Fatalf("head and tail work keys collapsed: %q", head)
	}
}

func TestActiveCacheRepoDoesNotSerializeAnotherCache(t *testing.T) {
	jobs := NewJobManager(filepath.Join(t.TempDir(), "jobs.json"))
	_, cancel := context.WithCancel(context.Background())
	job := jobs.createJobWithMetadata(SyncJobType, "owner/repo", "", 0, cancel)
	jobs.SetWorkIdentity(job.ID, "sync:cache-left:owner/repo:head", "cache-left", "reg-left", "")
	if _, ok := jobs.ActiveCacheRepo(SyncJobType, "cache-left", "owner/repo"); !ok {
		t.Fatal("active work was not found for its cache")
	}
	if other, ok := jobs.ActiveCacheRepo(SyncJobType, "cache-right", "owner/repo"); ok {
		t.Fatalf("unrelated cache was serialized by %+v", other)
	}
}

func TestMaintenanceSyncAggregationNeverPromotesBoundedTail(t *testing.T) {
	aggregate := &service.SyncResourcesResult{}
	mergeSyncResources(aggregate, &service.SyncResourcesResult{TraversalStatus: "complete", StopReason: "end_of_collection"})
	mergeSyncResources(aggregate, &service.SyncResourcesResult{TraversalStatus: "bounded", StopReason: "max_pages"})
	if aggregate.TraversalStatus != "bounded" || aggregate.StopReason != "max_pages" {
		t.Fatalf("aggregate=%+v", aggregate)
	}
}

func TestMaintenanceTailUsesConstantWindowFromCheckpoint(t *testing.T) {
	now := time.Now().UTC()
	lane, page, maxPages := nextMaintenanceSyncLane(MaintenanceEntry{Policy: MaintenancePolicy{Issues: true, HeadIntervalSeconds: 900, TailSlicePages: 10}}, []cache.MaintenanceFrontier{
		{RemoteType: "issue", Lane: "head", Status: "fresh", UpdatedAt: now},
		{RemoteType: "issue", Lane: "tail", Status: "backfilling", PagesListed: 50, Checkpoint: "next_page:41", UpdatedAt: now},
	}, now)
	if lane != "tail" || page != 41 || maxPages != 10 {
		t.Fatalf("lane=%q page=%d max_pages=%d", lane, page, maxPages)
	}
}

func TestMaintenancePartialHeadContinuesBeforeCompletedTail(t *testing.T) {
	now := time.Now().UTC()
	lane, page, maxPages := nextMaintenanceSyncLane(MaintenanceEntry{Policy: MaintenancePolicy{Issues: true, HeadIntervalSeconds: 900, HeadMaxPages: 3, TailSlicePages: 10}}, []cache.MaintenanceFrontier{
		{RemoteType: "issue", Lane: "head", Status: "partial", Checkpoint: "next_page:3", UpdatedAt: now},
		{RemoteType: "issue", Lane: "tail", Status: "complete", UpdatedAt: now},
	}, now)
	if lane != "head" || page != 3 || maxPages != 3 {
		t.Fatalf("lane=%q page=%d max_pages=%d", lane, page, maxPages)
	}
}

func TestMaintenancePolicyDefaultsFromRepositoryScopes(t *testing.T) {
	issuesOnly := cache.RepositoryBinding{RepoID: "owner/repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}
	policy, err := normalizeMaintenancePolicy(MaintenancePolicy{SyncEnabled: true}, issuesOnly)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.Issues || !policy.IssueComments || !policy.Pulls || !policy.PRComments || policy.Wiki {
		t.Fatalf("scope-derived policy=%+v", policy)
	}
	if _, err := normalizeMaintenancePolicy(MaintenancePolicy{SyncEnabled: true, Wiki: true}, issuesOnly); err == nil {
		t.Fatal("explicit wiki selection without wiki scope returned nil error")
	}
}

func TestMaintenanceFailureDoesNotExposeCachePath(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "private-cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(filepath.Join(dir, "jobs.json")), filepath.Join(dir, "managed-caches.json"))
	if _, err := maintenance.Enroll(ctx, testMaintenanceEnrollRequest(cachePath, "private-path-test", MaintenancePolicy{})); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cachePath); err != nil {
		t.Fatal(err)
	}
	result, err := maintenance.Reconcile(ctx)
	if err != nil || len(result.Entries) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), cachePath) || strings.Contains(result.Entries[0].LastError, dir) {
		t.Fatalf("maintenance result leaked private path: %s", data)
	}
	if result.Entries[0].LastError != "managed cache is unavailable" {
		t.Fatalf("last_error=%q", result.Entries[0].LastError)
	}
}

func TestCreateCoalescedJobIsAtomic(t *testing.T) {
	jobs := NewJobManager(filepath.Join(t.TempDir(), "jobs.json"))
	const callers = 32
	var wg sync.WaitGroup
	var mu sync.Mutex
	created := 0
	ids := map[string]int{}
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, cancel := context.WithCancel(context.Background())
			job, wasCreated, err := jobs.createCoalescedJob(SyncJobType, "owner/repo", "", 0, "same-work", "cache-1", "reg-1", "", cancel)
			if err != nil {
				t.Errorf("create job: %v", err)
				return
			}
			if !wasCreated {
				cancel()
			}
			mu.Lock()
			defer mu.Unlock()
			ids[job.ID]++
			if wasCreated {
				created++
			}
		}()
	}
	wg.Wait()
	if created != 1 || len(ids) != 1 {
		t.Fatalf("created=%d ids=%v", created, ids)
	}
}

func TestCacheWriterAdmissionSerializesIndependentWork(t *testing.T) {
	jobs := NewJobManager(filepath.Join(t.TempDir(), "jobs.json"))
	first, created, err := jobs.createCoalescedJob(SyncJobType, "owner/left", "", 0, "sync:cache-1:owner/left:head", "cache-1", "reg-left", "", func() {})
	if err != nil || !created {
		t.Fatalf("first=%+v created=%t err=%v", first, created, err)
	}
	coalesced, created, err := jobs.createCoalescedJob(SyncJobType, "owner/left", "", 0, "sync:cache-1:owner/left:head", "cache-1", "reg-left", "", func() {})
	if err != nil || created || coalesced.ID != first.ID {
		t.Fatalf("coalesced=%+v created=%t err=%v", coalesced, created, err)
	}
	_, created, err = jobs.createCoalescedJob(RAGIndexJobType, "owner/right", "profile", 0, "rag:cache-1:owner/right", "cache-1", "reg-right", "ns", func() {})
	var busy ErrCacheWriterBusy
	if created || !errors.As(err, &busy) || busy.ActiveJobID != first.ID || busy.DiagnosticCode() != "cache_writer_busy" {
		t.Fatalf("created=%t err=%T %v busy=%+v", created, err, err, busy)
	}
	other, created, err := jobs.createCoalescedJob(RAGIndexJobType, "owner/right", "profile", 0, "rag:cache-2:owner/right", "cache-2", "reg-right", "ns", func() {})
	if err != nil || !created || other.CacheUUID != "cache-2" {
		t.Fatalf("other=%+v created=%t err=%v", other, created, err)
	}
}

func TestMaintenanceReconcileOrderPrefersOldestWriterIntentPerCache(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	entries := []MaintenanceEntry{
		{RegistrationID: "reg-newer", CacheUUID: "cache-1"},
		{RegistrationID: "reg-never", CacheUUID: "cache-1"},
		{RegistrationID: "reg-older", CacheUUID: "cache-1"},
	}
	jobs := []Job{
		{Type: SyncJobType, RegistrationID: "reg-newer", UpdatedAt: now},
		{Type: RAGIndexJobType, RegistrationID: "reg-older", UpdatedAt: now.Add(-time.Hour)},
	}
	ordered := maintenanceReconcileOrder(entries, jobs)
	want := []string{"reg-never", "reg-older", "reg-newer"}
	for i := range want {
		if ordered[i].RegistrationID != want[i] {
			t.Fatalf("order=%+v want=%v", ordered, want)
		}
	}
}

func TestMaintenanceFrontiersPreservePerCollectionResults(t *testing.T) {
	ctx := context.Background()
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertMaintenanceFrontier(ctx, cache.MaintenanceFrontier{RepoID: "owner/repo", RemoteType: "wiki", Ordering: "updated_at_desc", FilterKey: "all", Lane: "tail", Status: "backfilling", Checkpoint: "next_page:21", UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	req := StartSyncJobRequest{RepoID: "owner/repo", CachePath: cachePath, Lane: "tail", Page: 1, MaxPages: 10}
	err = recordMaintenanceSyncFrontiers(ctx, req, []syncCollectionResult{
		{RemoteType: "issue", Result: &service.SyncResourcesResult{TraversalStatus: "complete", StopReason: "end_of_collection", PagesListed: 1, RecordsListed: 5}},
		{RemoteType: "wiki", Result: &service.SyncResourcesResult{TraversalStatus: "bounded", StopReason: "max_pages", PagesListed: 10, RecordsListed: 100}},
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err = cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	frontiers, err := store.ListMaintenanceFrontiers(ctx, "owner/repo")
	if err != nil || len(frontiers) != 2 {
		t.Fatalf("frontiers=%+v err=%v", frontiers, err)
	}
	byType := map[string]cache.MaintenanceFrontier{}
	for _, frontier := range frontiers {
		byType[frontier.RemoteType] = frontier
	}
	if byType["issue"].Status != "complete" || byType["issue"].PagesListed != 1 {
		t.Fatalf("issue frontier=%+v", byType["issue"])
	}
	if byType["wiki"].Status != "backfilling" || byType["wiki"].PagesListed != 10 {
		t.Fatalf("wiki frontier=%+v", byType["wiki"])
	}
	if byType["wiki"].Checkpoint != "next_page:21" {
		t.Fatalf("wiki checkpoint regressed: %+v", byType["wiki"])
	}
}

func TestMaintenanceStageInterleavesRAGBeforeTail(t *testing.T) {
	policy := MaintenancePolicy{SyncEnabled: true, RAGEnabled: true}
	if got := nextMaintenanceStage(policy, "head", true, true, true); got != SyncJobType {
		t.Fatalf("head stage=%q", got)
	}
	if got := nextMaintenanceStage(policy, "tail", true, true, true); got != RAGIndexJobType {
		t.Fatalf("stale RAG stage=%q", got)
	}
	if got := nextMaintenanceStage(policy, "tail", false, true, true); got != SyncJobType {
		t.Fatalf("tail stage=%q", got)
	}
	if got := nextMaintenanceStage(policy, "tail", true, true, false); got != SyncJobType {
		t.Fatalf("RAG backoff blocked healthy tail stage: %q", got)
	}
	if got := nextMaintenanceStage(policy, "head", true, false, true); got != RAGIndexJobType {
		t.Fatalf("sync backoff blocked healthy RAG stage: %q", got)
	}
}

func TestMaintenanceReconcileDoesNotOverlapActiveRAGAndSync(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	jobs := NewJobManager(filepath.Join(dir, "jobs.json"))
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), jobs, filepath.Join(dir, "managed-caches.json"))
	entry, err := maintenance.Enroll(ctx, testMaintenanceEnrollRequest(cachePath, "cross-type-arbitration", MaintenancePolicy{SyncEnabled: true}))
	if err != nil {
		t.Fatal(err)
	}
	_, cancel := context.WithCancel(ctx)
	defer cancel()
	active, created, err := jobs.createCoalescedJob(RAGIndexJobType, entry.RepoID, "fake-rag", 0, "rag-index:"+entry.CacheUUID+":"+entry.RepoID, entry.CacheUUID, entry.RegistrationID, "", cancel)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("active RAG test job was not created")
	}
	result, err := maintenance.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.JobsStarted) != 0 || len(result.Entries) != 1 || len(result.Entries[0].ActiveJobs) != 1 || result.Entries[0].ActiveJobs[0] != active.ID {
		t.Fatalf("result=%+v active=%+v", result, active)
	}
}

func TestMaintenanceLaneTracksEveryCollectionFrontier(t *testing.T) {
	now := time.Now().UTC()
	entry := MaintenanceEntry{Policy: MaintenancePolicy{Issues: true, Wiki: true, HeadIntervalSeconds: 900, HeadMaxPages: 3, TailSlicePages: 10}}
	frontiers := []cache.MaintenanceFrontier{
		{RemoteType: "issue", Lane: "head", Status: "fresh", UpdatedAt: now},
		{RemoteType: "issue", Lane: "tail", Status: "complete", UpdatedAt: now},
		{RemoteType: "wiki", Lane: "tail", Status: "backfilling", Checkpoint: "next_page:21", UpdatedAt: now},
	}
	if lane, page, pages := nextMaintenanceSyncLane(entry, frontiers, now); lane != "head" || page != 1 || pages != 3 {
		t.Fatalf("missing wiki head lane=%q page=%d pages=%d", lane, page, pages)
	}
	frontiers = append(frontiers, cache.MaintenanceFrontier{RemoteType: "wiki", Lane: "head", Status: "fresh", UpdatedAt: now})
	if lane, page, pages := nextMaintenanceSyncLane(entry, frontiers, now); lane != "tail" || page != 21 || pages != 10 {
		t.Fatalf("wiki tail lane=%q page=%d pages=%d", lane, page, pages)
	}
}

func TestMaintenanceCheckpointAdvancesWithOnePageOverlap(t *testing.T) {
	req := StartSyncJobRequest{Lane: "tail", Page: 21, MaxPages: 10}
	collection := syncCollectionResult{Result: &service.SyncResourcesResult{TraversalStatus: "bounded", PagesListed: 10}}
	if got := nextMaintenanceCheckpoint(req, collection); got != "next_page:30" {
		t.Fatalf("checkpoint=%q", got)
	}
	collection.Result.TraversalStatus = "complete"
	if got := nextMaintenanceCheckpoint(req, collection); got != "" {
		t.Fatalf("complete checkpoint=%q", got)
	}
}

func TestMaintenanceHeadCheckpointAdvancesUntilTraversalCompletes(t *testing.T) {
	req := StartSyncJobRequest{Lane: "head", Page: 1, MaxPages: 3}
	collection := syncCollectionResult{Result: &service.SyncResourcesResult{TraversalStatus: "bounded", PagesListed: 3}}
	if got := nextMaintenanceCheckpoint(req, collection); got != "next_page:3" {
		t.Fatalf("checkpoint=%q", got)
	}
	req.Page = 3
	collection.Result.TraversalStatus = "complete"
	if got := nextMaintenanceCheckpoint(req, collection); got != "" {
		t.Fatalf("complete checkpoint=%q", got)
	}
}

func TestMaintenanceCompletedHeadPublishesAggregatedHighWatermark(t *testing.T) {
	ctx := context.Background()
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 11, 10, 0, 0, 0, time.UTC)
	if err := store.UpsertMaintenanceFrontier(ctx, cache.MaintenanceFrontier{RepoID: "owner/repo", RemoteType: "issue", Ordering: "updated_at_desc", FilterKey: "all", Lane: "head", Status: "partial", HighUpdatedAt: base.Add(time.Hour), HighRemoteID: "issue:2", Checkpoint: "next_page:3", UpdatedAt: base}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	result := &service.SyncResourcesResult{TraversalStatus: "complete", StopReason: "watermark", PagesListed: 1, Results: []service.SyncResult{{Record: service.SourceSummary{ID: "ISSUE-3", RemoteAlias: "issue:3", UpdatedAt: base.Add(2 * time.Hour)}}}}
	if err := recordMaintenanceSyncFrontiers(ctx, StartSyncJobRequest{RepoID: "owner/repo", CachePath: cachePath, Lane: "head", Page: 3, MaxPages: 3}, []syncCollectionResult{{RemoteType: "issue", Result: result}}); err != nil {
		t.Fatal(err)
	}
	store, err = cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	frontier, ok, err := store.GetSyncFrontier(ctx, "owner/repo", "issue", "updated_at_desc", "state=all")
	if err != nil || !ok || frontier.Status != "complete" || !frontier.HighUpdatedAt.Equal(base.Add(2*time.Hour)) {
		t.Fatalf("frontier=%+v ok=%t err=%v", frontier, ok, err)
	}
}

func TestMaintenanceHeadBoundedTraversalIsNotFresh(t *testing.T) {
	ctx := context.Background()
	cachePath := filepath.Join(t.TempDir(), "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	store.Close()
	err = recordMaintenanceSyncFrontiers(ctx, StartSyncJobRequest{RepoID: "owner/repo", CachePath: cachePath, Lane: "head", MaxPages: 3}, []syncCollectionResult{{RemoteType: "issue", Result: &service.SyncResourcesResult{TraversalStatus: "bounded", StopReason: "max_pages", PagesListed: 3}}})
	if err != nil {
		t.Fatal(err)
	}
	store, err = cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	frontiers, err := store.ListMaintenanceFrontiers(ctx, "owner/repo")
	if err != nil || len(frontiers) != 1 || frontiers[0].Status != "partial" || frontiers[0].Checkpoint != "next_page:3" {
		t.Fatalf("frontiers=%+v err=%v", frontiers, err)
	}
}

func TestMaintenanceStageFailureBackoffPersistsUntilSuccess(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	failedAt := now.Add(-time.Second)
	failed := Job{ID: "job-000001", Type: RAGIndexJobType, Status: JobStatusFailed, ErrorClass: "provider_unavailable", UpdatedAt: failedAt, FinishedAt: &failedAt}
	state := observeMaintenanceStage(MaintenanceStageState{}, failed, now)
	if state.ConsecutiveFailures != 1 || !state.RetryAfter.Equal(now.Add(time.Minute)) || state.LastErrorClass != "provider_unavailable" {
		t.Fatalf("first failure state=%+v", state)
	}
	if repeated := observeMaintenanceStage(state, failed, now.Add(30*time.Second)); repeated.ConsecutiveFailures != 1 || !repeated.RetryAfter.Equal(state.RetryAfter) {
		t.Fatalf("same job counted twice: %+v", repeated)
	}
	secondAt := now.Add(time.Minute)
	second := Job{ID: "job-000002", Type: RAGIndexJobType, Status: JobStatusFailed, ErrorClass: "provider_unavailable", UpdatedAt: secondAt, FinishedAt: &secondAt}
	state = observeMaintenanceStage(state, second, secondAt)
	if state.ConsecutiveFailures != 2 || !state.RetryAfter.Equal(secondAt.Add(2*time.Minute)) {
		t.Fatalf("second failure state=%+v", state)
	}
	succeededAt := secondAt.Add(3 * time.Minute)
	succeeded := Job{ID: "job-000003", Type: RAGIndexJobType, Status: JobStatusSucceeded, NamespaceID: "namespace-new", UpdatedAt: succeededAt, FinishedAt: &succeededAt}
	state = observeMaintenanceStage(state, succeeded, succeededAt)
	if state.ConsecutiveFailures != 0 || !state.RetryAfter.IsZero() || state.LastErrorClass != "" || state.NamespaceID != "namespace-new" {
		t.Fatalf("success did not clear failure state: %+v", state)
	}
}

func TestMaintenanceJobDiagnosticsArePublicSafe(t *testing.T) {
	secret := "/Users/private/cache.db?token=secret"
	event := sanitizeMaintenanceProgress(RAGIndexJobType, service.ProgressEvent{Type: "records", Phase: "running", RecordsFailed: 1, Message: secret})
	if strings.Contains(event.Message, secret) || event.Message != "RAG maintenance failed" {
		t.Fatalf("progress message=%q", event.Message)
	}
	if got := sanitizeMaintenanceErrorClass("../../private", "rag_failed"); got != "rag_failed" {
		t.Fatalf("error class=%q", got)
	}
}

func TestSelectMaintenanceNamespaceRequiresRecordedEffectiveProfile(t *testing.T) {
	now := time.Now().UTC()
	namespaces := []cache.EmbeddingNamespace{
		{ID: "old", EmbeddingNamespaceIdentity: cache.EmbeddingNamespaceIdentity{ProfileID: "profile-old"}, UpdatedAt: now.Add(time.Minute)},
		{ID: "current", EmbeddingNamespaceIdentity: cache.EmbeddingNamespaceIdentity{ProfileID: "profile-current"}, UpdatedAt: now},
	}
	entry := MaintenanceEntry{NamespaceID: "old", Policy: MaintenancePolicy{Profile: "profile-current"}}
	if got := selectMaintenanceNamespace(entry, namespaces, "profile-current"); got != "" {
		t.Fatalf("unverified namespace=%q", got)
	}
	entry.NamespaceID = "current"
	if got := selectMaintenanceNamespace(entry, namespaces, "profile-current"); got != "current" {
		t.Fatalf("namespace=%q", got)
	}
}

func TestMaintenanceEffectiveDefaultProfileRejectsOtherNamespace(t *testing.T) {
	manager := newTestManager(t, "darwin")
	source := manager.Source.(testSource)
	source.env[config.EnvRAGProfile] = "profile-default"
	profile := maintenanceEffectiveProfile(Manager{Source: source}, filepath.Join(t.TempDir(), "cache.db"), "")
	if profile != "profile-default" {
		t.Fatalf("effective profile=%q", profile)
	}
	entry := MaintenanceEntry{NamespaceID: "manual"}
	namespaces := []cache.EmbeddingNamespace{
		{ID: "manual", EmbeddingNamespaceIdentity: cache.EmbeddingNamespaceIdentity{ProfileID: "profile-manual"}},
		{ID: "default", EmbeddingNamespaceIdentity: cache.EmbeddingNamespaceIdentity{ProfileID: "profile-default"}},
	}
	if got := selectMaintenanceNamespace(entry, namespaces, profile); got != "" {
		t.Fatalf("selected non-default namespace=%q", got)
	}
}

func TestMaintenancePeriodicallyVerifiesReadyRAGNamespace(t *testing.T) {
	now := time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	if !maintenanceRAGVerificationDue("namespace", "ready", now.Add(-15*time.Minute), 15*time.Minute, now) {
		t.Fatal("ready namespace did not become due for provider/model verification")
	}
	if maintenanceRAGVerificationDue("namespace", "ready", now.Add(-14*time.Minute), 15*time.Minute, now) {
		t.Fatal("recently verified namespace became due early")
	}
}

func testMaintenanceEnrollRequest(cachePath, key string, policy MaintenancePolicy) MaintenanceEnrollRequest {
	cfg := config.Default()
	cfg.CachePath = cachePath
	return MaintenanceEnrollRequest{
		CachePath: cachePath, RepoID: "owner/repo", IdempotencyKey: key, Policy: policy,
		ConfigHash: maintenanceHash(cfg), ConfigSnapshot: cfg,
	}
}
