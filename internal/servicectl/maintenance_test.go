package servicectl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	wantAuthority := pathFingerprint(maintenanceCanonicalPathKey(cachePath))
	for _, key := range []string{"enroll-1", "enroll-2"} {
		receipt := maintenance.receipts[maintenanceIdempotencyKeyHash(key)]
		if receipt.AuthorityPathFingerprint != wantAuthority {
			t.Fatalf("receipt %q authority=%q want=%q", key, receipt.AuthorityPathFingerprint, wantAuthority)
		}
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

func TestMaintenanceRejectsConfigSnapshotFromAnotherCacheAuthority(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	firstPath, secondPath := filepath.Join(dir, "first.db"), filepath.Join(dir, "second.db")
	for _, path := range []string{firstPath, secondPath} {
		store, err := cache.NewSQLiteStore(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
			t.Fatal(err)
		}
		_ = store.Close()
	}
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), filepath.Join(dir, "managed-caches.json"))
	req := testMaintenanceEnrollRequest(firstPath, "wrong-config-authority", MaintenancePolicy{})
	req.ConfigSnapshot.CachePath = secondPath
	req.ConfigHash = maintenanceHash(req.ConfigSnapshot)
	_, err := maintenance.Enroll(ctx, req)
	if err == nil || strings.Contains(err.Error(), firstPath) || strings.Contains(err.Error(), secondPath) || !strings.Contains(err.Error(), "config snapshot cache authority mismatch") {
		t.Fatalf("config authority error=%T %v", err, err)
	}
	if listed, _ := maintenance.List(ctx); len(listed.Entries) != 0 {
		t.Fatalf("rejected enrollment mutated registry: %+v", listed.Entries)
	}
}

func TestMaintenanceLoadBlocksConfigSnapshotFromAnotherCacheAuthority(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	firstPath, secondPath := filepath.Join(dir, "first.db"), filepath.Join(dir, "second.db")
	firstStore, err := cache.NewSQLiteStore(ctx, firstPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstStore.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	identity, _ := firstStore.CacheIdentity(ctx)
	_ = firstStore.Close()
	secondStore, err := cache.NewSQLiteStore(ctx, secondPath)
	if err != nil {
		t.Fatal(err)
	}
	_ = secondStore.Close()
	wrongConfig := config.Default()
	wrongConfig.CachePath = secondPath
	registrationID := maintenanceRegistrationID(identity.UUID, "owner/repo")
	disk := maintenanceRegistryFile{SchemaVersion: maintenanceRegistrySchema, Entries: []maintenanceDiskEntry{{
		MaintenanceEntry: MaintenanceEntry{RegistrationID: registrationID, CacheUUID: identity.UUID, RepoID: "owner/repo", Policy: MaintenancePolicy{SyncMode: "off"}, ConfigHash: maintenanceHash(wrongConfig), Enabled: true, State: "enrolled"},
		CachePath:        firstPath, ConfigSnapshot: wrongConfig,
	}}}
	registryPath := filepath.Join(dir, "managed-caches.json")
	data, _ := json.Marshal(disk)
	if err := os.WriteFile(registryPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	jobs := NewJobManager(filepath.Join(dir, "jobs.json"))
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), jobs, registryPath)
	if err := maintenance.Load(); err != nil {
		t.Fatal(err)
	}
	listed, _ := maintenance.List(ctx)
	if len(listed.Entries) != 1 || listed.Entries[0].Enabled || listed.Entries[0].State != "config_snapshot_invalid" || listed.Entries[0].LastErrorClass != "config_snapshot_invalid" {
		t.Fatalf("mismatched loaded config was not blocked: %+v", listed.Entries)
	}
	result, err := maintenance.ReconcileRegistration(ctx, registrationID)
	if err != nil || len(result.JobsStarted) != 0 || len(jobs.List()) != 0 {
		t.Fatalf("blocked config scheduled work: result=%+v jobs=%+v err=%v", result, jobs.List(), err)
	}
	publicJSON, _ := json.Marshal(listed)
	if strings.Contains(string(publicJSON), firstPath) || strings.Contains(string(publicJSON), secondPath) {
		t.Fatalf("blocked config observation leaked authority: %s", publicJSON)
	}
	persistedData, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted maintenanceRegistryFile
	if err := json.Unmarshal(persistedData, &persisted); err != nil || len(persisted.Entries) != 1 {
		t.Fatalf("persisted blocked registry=%+v err=%v", persisted, err)
	}
	repairedConfig := wrongConfig
	repairedConfig.CachePath = firstPath
	persisted.Entries[0].ConfigSnapshot = repairedConfig
	persisted.Entries[0].ConfigHash = maintenanceHash(repairedConfig)
	repairedData, _ := json.Marshal(persisted)
	if err := os.WriteFile(registryPath, repairedData, 0o600); err != nil {
		t.Fatal(err)
	}
	repaired := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := repaired.Load(); err != nil {
		t.Fatal(err)
	}
	repairedList, _ := repaired.List(ctx)
	if len(repairedList.Entries) != 1 || !repairedList.Entries[0].Enabled || repairedList.Entries[0].State != "enrolled" || repairedList.Entries[0].LastErrorClass != "" {
		t.Fatalf("repaired config did not restore prior intent: %+v", repairedList.Entries)
	}
}

func TestMaintenanceEnrollCanonicalizesAliasBeforeIdentityAndReplay(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Aliases: []string{"legacy/repo"}, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(filepath.Join(dir, "jobs.json")), filepath.Join(dir, "registry.json"))
	canonicalReq := testMaintenanceEnrollRequest(cachePath, "shared-replay", MaintenancePolicy{})
	canonical, err := maintenance.Enroll(ctx, canonicalReq)
	if err != nil {
		t.Fatal(err)
	}
	aliasReq := canonicalReq
	aliasReq.RepoID = "legacy/repo"
	alias, err := maintenance.Enroll(ctx, aliasReq)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errCh := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := testMaintenanceEnrollRequest(cachePath, fmt.Sprintf("concurrent-%d", i), MaintenancePolicy{})
			if i%2 == 1 {
				req.RepoID = "legacy/repo"
			}
			entry, enrollErr := maintenance.Enroll(ctx, req)
			if enrollErr != nil {
				errCh <- enrollErr
				return
			}
			if entry.RegistrationID != canonical.RegistrationID || entry.RepoID != "owner/repo" {
				errCh <- fmt.Errorf("non-canonical concurrent entry: %+v", entry)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	listed, err := maintenance.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if alias.RegistrationID != canonical.RegistrationID || alias.RepoID != "owner/repo" || len(listed.Entries) != 1 || len(canonical.Aliases) != 1 || canonical.Aliases[0] != "legacy/repo" {
		t.Fatalf("canonical=%+v alias=%+v listed=%+v", canonical, alias, listed)
	}
}

func TestMaintenanceEnrollRollsBackLiveCanonicalizationWhenPersistenceFails(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	binding := cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Aliases: []string{"legacy/repo"}, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}
	if err := store.AddRepository(ctx, binding); err != nil {
		t.Fatal(err)
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.CachePath = cachePath
	canonicalPath, err := canonicalCachePath(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := normalizeMaintenancePolicy(MaintenancePolicy{}, binding)
	if err != nil {
		t.Fatal(err)
	}
	legacyID := maintenanceRegistrationID(identity.UUID, "legacy/repo")
	canonicalID := maintenanceRegistrationID(identity.UUID, "owner/repo")
	legacySourceID := repositoryDocsSourceRegistrationID(legacyID, "git-common-dir", "HEAD", "docs")
	registrationFingerprint := pathFingerprint(canonicalPath)
	registryPath := filepath.Join(dir, "managed-caches.json")
	jobs := NewJobManager(filepath.Join(dir, "jobs.json"))
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), jobs, registryPath)
	maintenance.generation = 7
	maintenance.entries[legacyID] = &MaintenanceEntry{
		RegistrationID: legacyID, CacheUUID: identity.UUID, PathFingerprint: registrationFingerprint,
		RepoID: "legacy/repo", Policy: policy, ConfigHash: maintenanceHash(cfg), Enabled: true,
		Generation: 2, State: "enrolled", cachePath: canonicalPath, configSnapshot: cfg,
	}
	maintenance.receipts["legacy-receipt"] = maintenanceReceipt{KeyHash: "legacy-receipt", RegistrationID: legacyID, AuthorityPathFingerprint: registrationFingerprint}
	maintenance.sources[legacyID] = map[string]*repositoryDocsRegisteredSource{
		legacySourceID: {State: RepositoryDocsMaintenanceState{SourceRegistrationID: legacySourceID, SourceRegistrationGeneration: 1, GitStoreRef: "git-common-dir", WorktreeRef: "HEAD", State: "ready"}, RepositoryPath: filepath.Join(dir, "repository"), Profile: "docs"},
	}
	maintenance.admissions[repositoryDocsAdmissionKey(legacyID, legacySourceID)] = repositoryDocsAdmissionIntent{RegistrationID: legacyID, SourceRegistrationID: legacySourceID, SourceRegistrationGeneration: 1, AuthorityPathFingerprint: registrationFingerprint, RepoID: "legacy/repo", WorkKey: "legacy-work", ExpectedRevisionSetID: "legacy-set", Disposition: repositoryDocsAdmissionPending, CreatedAt: time.Now().UTC()}
	if err := maintenance.saveLocked(); err != nil {
		t.Fatal(err)
	}
	jobs.SetRegistrationRedirects(maintenance.jobProjectionRedirectsLocked(), cloneStringMap(maintenance.sourceRedirects), maintenance.canonicalRepoIDsLocked())

	before := maintenance.snapshotConflictMutationLocked()
	beforeDisk, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	jobs.mu.Lock()
	beforeJobRedirects := cloneStringMap(jobs.registrationRedirects)
	beforeJobSourceRedirects := cloneStringMap(jobs.sourceRegistrationRedirects)
	beforeJobRepoIDs := cloneStringMap(jobs.canonicalRepoByRegistration)
	jobs.mu.Unlock()

	injected := errors.New("injected live canonicalization persistence failure")
	maintenance.writeFile = func(string, []byte, os.FileMode) error { return injected }
	if _, err := maintenance.Enroll(ctx, testMaintenanceEnrollRequest(cachePath, "canonicalize-fails", MaintenancePolicy{})); !errors.Is(err, injected) {
		t.Fatalf("enroll error=%v", err)
	}
	after := maintenance.snapshotConflictMutationLocked()
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("failed canonicalization mutated manager state:\nbefore=%+v\nafter=%+v", before, after)
	}
	afterDisk, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterDisk, beforeDisk) {
		t.Fatalf("failed canonicalization changed durable registry: before=%s after=%s", beforeDisk, afterDisk)
	}
	jobs.mu.Lock()
	if !reflect.DeepEqual(jobs.registrationRedirects, beforeJobRedirects) || !reflect.DeepEqual(jobs.sourceRegistrationRedirects, beforeJobSourceRedirects) || !reflect.DeepEqual(jobs.canonicalRepoByRegistration, beforeJobRepoIDs) {
		t.Fatalf("failed canonicalization changed job projection: registrations=%v sources=%v repos=%v", jobs.registrationRedirects, jobs.sourceRegistrationRedirects, jobs.canonicalRepoByRegistration)
	}
	jobs.mu.Unlock()

	postCommit := errors.New("injected post-rename durability uncertainty")
	maintenance.writeFile = func(path string, data []byte, mode os.FileMode) error {
		if err := durableAtomicWriteFile(path, data, mode); err != nil {
			return err
		}
		return postCommit
	}
	entry, err := maintenance.Enroll(ctx, testMaintenanceEnrollRequest(cachePath, "canonicalize-retry", MaintenancePolicy{}))
	if err != nil {
		t.Fatalf("commit-confirmed retry returned post-rename error: %v", err)
	}
	if entry.RegistrationID != canonicalID || entry.RepoID != "owner/repo" || maintenance.redirects[legacyID] != canonicalID {
		t.Fatalf("retry did not converge canonical state: entry=%+v redirects=%v", entry, maintenance.redirects)
	}
	jobs.mu.Lock()
	if jobs.registrationRedirects[legacyID] != canonicalID || jobs.canonicalRepoByRegistration[canonicalID] != "owner/repo" {
		t.Fatalf("retry did not publish canonical job projection: registrations=%v repos=%v", jobs.registrationRedirects, jobs.canonicalRepoByRegistration)
	}
	jobs.mu.Unlock()
	maintenance.writeFile = durableAtomicWriteFile

	restartedJobs := NewJobManager(filepath.Join(dir, "jobs-restarted.json"))
	restarted := NewMaintenanceManager(newTestManager(t, "darwin"), restartedJobs, registryPath)
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	listed, err := restarted.List(ctx)
	if err != nil || len(listed.Entries) != 1 || listed.Entries[0].RegistrationID != canonicalID || restarted.redirects[legacyID] != canonicalID {
		t.Fatalf("restart did not retain canonical state: listed=%+v redirects=%v err=%v", listed, restarted.redirects, err)
	}
}

func TestMaintenanceLoadMigratesCompatibleAliasDuplicatesAndJobLinks(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Aliases: []string{"legacy/repo"}, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.CachePath = cachePath
	configHash := maintenanceHash(cfg)
	policy := MaintenancePolicy{SyncEnabled: true, SyncMode: "head", Issues: true, HeadIntervalSeconds: 900, RAGIntervalSeconds: 900, HeadMaxPages: 3, TailSlicePages: 10, PerPage: 100}
	canonicalID := maintenanceRegistrationID(identity.UUID, "owner/repo")
	legacyID := maintenanceRegistrationID(identity.UUID, "legacy/repo")
	now := time.Date(2026, 8, 30, 7, 0, 0, 0, time.UTC)
	legacyRetry := now.Add(45 * time.Minute)
	registryPath := filepath.Join(dir, "managed-caches.json")
	disk := maintenanceRegistryFile{
		SchemaVersion: legacyMaintenanceRegistrySchema,
		Generation:    7,
		Entries: []maintenanceDiskEntry{
			{MaintenanceEntry: MaintenanceEntry{RegistrationID: canonicalID, CacheUUID: identity.UUID, RepoID: "owner/repo", Policy: policy, ConfigHash: configHash, Enabled: true, Generation: 2, State: "ready", LastSeenAt: now}, CachePath: cachePath, ConfigSnapshot: cfg},
			{MaintenanceEntry: MaintenanceEntry{RegistrationID: legacyID, CacheUUID: identity.UUID, RepoID: "legacy/repo", Policy: policy, ConfigHash: configHash, Enabled: true, Generation: 4, State: "degraded", SyncStage: MaintenanceStageState{Status: JobStatusFailed, ConsecutiveFailures: 3, RetryAfter: legacyRetry, UpdatedAt: now.Add(time.Minute)}, LastSeenAt: now.Add(time.Minute)}, CachePath: cachePath, ConfigSnapshot: cfg},
		},
		Receipts: []maintenanceReceipt{{KeyHash: maintenanceIdempotencyKeyHash("legacy-enroll"), RegistrationID: legacyID, IntentHash: maintenanceEnrollmentIntentHash(legacyID, policy, configHash)}},
	}
	data, _ := json.Marshal(disk)
	if err := os.WriteFile(registryPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	jobsPath := filepath.Join(dir, "jobs.json")
	jobsData, _ := json.Marshal([]Job{{ID: "job-000001", Type: SyncJobType, RepoID: "legacy/repo", CacheUUID: identity.UUID, RegistrationID: legacyID, Status: JobStatusSucceeded, CreatedAt: now, UpdatedAt: now}})
	if err := os.WriteFile(jobsPath, jobsData, 0o600); err != nil {
		t.Fatal(err)
	}
	jobs := NewJobManager(jobsPath)
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), jobs, registryPath)
	if err := maintenance.Load(); err != nil {
		t.Fatal(err)
	}
	if err := jobs.LoadAndMarkInterrupted(); err != nil {
		t.Fatal(err)
	}
	listed, err := maintenance.List(ctx)
	if err != nil || len(listed.Entries) != 1 {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	entry := listed.Entries[0]
	if entry.RegistrationID != canonicalID || entry.RepoID != "owner/repo" || len(entry.Aliases) != 1 || entry.Aliases[0] != "legacy/repo" || len(entry.LegacyRegistrationIDs) != 1 || entry.LegacyRegistrationIDs[0] != legacyID || !entry.SyncStage.RetryAfter.Equal(legacyRetry) || entry.Generation != 5 {
		t.Fatalf("migrated entry=%+v", entry)
	}
	job, ok := jobs.Get("job-000001")
	if !ok || job.RegistrationID != canonicalID || job.RepoID != "owner/repo" || job.ID != "job-000001" {
		t.Fatalf("migrated job=%+v ok=%v", job, ok)
	}
	replayed, err := maintenance.Enroll(ctx, MaintenanceEnrollRequest{
		CachePath: cachePath, RepoID: "legacy/repo", Policy: policy, IdempotencyKey: "legacy-enroll",
		ConfigHash: configHash, ConfigSnapshot: cfg,
	})
	if err != nil || replayed.RegistrationID != canonicalID {
		t.Fatalf("legacy receipt canonical replay=%+v err=%v", replayed, err)
	}
	disabled, err := maintenance.Disable(ctx, legacyID)
	if err != nil || disabled.RegistrationID != canonicalID || disabled.Enabled {
		t.Fatalf("redirect disable=%+v err=%v", disabled, err)
	}
	persisted, err := os.ReadFile(registryPath)
	if err != nil || !strings.Contains(string(persisted), maintenanceRegistrySchema) || !strings.Contains(string(persisted), legacyID) {
		t.Fatalf("persisted=%s err=%v", persisted, err)
	}
}

func TestMaintenanceCanonicalIdentityKeepsDistinctRepositoriesSeparate(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, binding := range []cache.RepositoryBinding{
		{RepoID: "owner/first", Owner: "owner", Name: "first", Aliases: []string{"legacy/first"}},
		{RepoID: "owner/second", Owner: "owner", Name: "second", Aliases: []string{"legacy/second"}},
	} {
		if err := store.AddRepository(ctx, binding); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(filepath.Join(dir, "jobs.json")), filepath.Join(dir, "registry.json"))
	firstRequest := testMaintenanceEnrollRequest(cachePath, "first", MaintenancePolicy{})
	firstRequest.RepoID = "legacy/first"
	first, err := maintenance.Enroll(ctx, firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	secondRequest := testMaintenanceEnrollRequest(cachePath, "second", MaintenancePolicy{})
	secondRequest.RepoID = "owner/second"
	second, err := maintenance.Enroll(ctx, secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := maintenance.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.RepoID != "owner/first" || second.RepoID != "owner/second" || first.RegistrationID == second.RegistrationID || len(listed.Entries) != 2 {
		t.Fatalf("first=%+v second=%+v listed=%+v", first, second, listed)
	}
}

func TestConservativeMaintenanceStageUsesLatestSuccessToClearOlderFailure(t *testing.T) {
	failedAt := time.Date(2026, 8, 30, 8, 0, 0, 0, time.UTC)
	succeededAt := failedAt.Add(time.Minute)
	failed := MaintenanceStageState{Status: JobStatusFailed, LastErrorClass: "sync_failed", ConsecutiveFailures: 3, RetryAfter: failedAt.Add(time.Hour), UpdatedAt: failedAt}
	succeeded := MaintenanceStageState{Status: JobStatusSucceeded, UpdatedAt: succeededAt}
	merged := conservativeMaintenanceStage(failed, succeeded)
	if merged.Status != JobStatusSucceeded || merged.LastErrorClass != "" || merged.ConsecutiveFailures != 0 || !merged.RetryAfter.IsZero() {
		t.Fatalf("latest success did not clear older failure: %+v", merged)
	}
}

func TestMaintenanceRegistryMigrationFailureLeavesV1RecoverableAndRetryIsIdempotent(t *testing.T) {
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
	cfg := config.Default()
	cfg.CachePath = cachePath
	registrationID := maintenanceRegistrationID(identity.UUID, "owner/repo")
	disk := maintenanceRegistryFile{SchemaVersion: legacyMaintenanceRegistrySchema, Generation: 4, Entries: []maintenanceDiskEntry{{
		MaintenanceEntry: MaintenanceEntry{RegistrationID: registrationID, CacheUUID: identity.UUID, RepoID: "owner/repo", Policy: MaintenancePolicy{}, ConfigHash: maintenanceHash(cfg), Enabled: true, Generation: 1, State: "ready"},
		CachePath:        cachePath, ConfigSnapshot: cfg,
	}}}
	original, _ := json.Marshal(disk)
	registryPath := filepath.Join(dir, "managed-caches.json")
	if err := os.WriteFile(registryPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected migration persistence failure")
	failing := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	failing.writeFile = func(string, []byte, os.FileMode) error { return injected }
	if err := failing.Load(); !errors.Is(err, injected) {
		t.Fatalf("migration error=%v", err)
	}
	afterFailure, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(afterFailure, original) {
		t.Fatalf("failed migration changed durable v1 registry: before=%s after=%s", original, afterFailure)
	}

	first := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := first.Load(); err != nil {
		t.Fatal(err)
	}
	firstList, err := first.List(ctx)
	if err != nil || firstList.SchemaVersion != maintenanceRegistrySchema || firstList.Generation != 5 || len(firstList.Entries) != 1 {
		t.Fatalf("first migration=%+v err=%v", firstList, err)
	}
	second := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := second.Load(); err != nil {
		t.Fatal(err)
	}
	secondList, err := second.List(ctx)
	if err != nil || secondList.Generation != firstList.Generation || len(secondList.Entries) != 1 || secondList.Entries[0].RegistrationID != registrationID {
		t.Fatalf("repeated migration=%+v err=%v", secondList, err)
	}
}

func TestMaintenanceLoadBlocksAliasPolicyConflictWithoutScheduling(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Aliases: []string{"legacy/repo"}, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	identity, _ := store.CacheIdentity(ctx)
	_ = store.Close()
	cfg := config.Default()
	cfg.CachePath = cachePath
	configHash := maintenanceHash(cfg)
	canonicalID := maintenanceRegistrationID(identity.UUID, "owner/repo")
	legacyID := maintenanceRegistrationID(identity.UUID, "legacy/repo")
	disk := maintenanceRegistryFile{SchemaVersion: legacyMaintenanceRegistrySchema, Entries: []maintenanceDiskEntry{
		{MaintenanceEntry: MaintenanceEntry{RegistrationID: canonicalID, CacheUUID: identity.UUID, RepoID: "owner/repo", Policy: MaintenancePolicy{SyncEnabled: true, SyncMode: "head", Issues: true}, ConfigHash: configHash, Enabled: true, Generation: 1}, CachePath: cachePath, ConfigSnapshot: cfg},
		{MaintenanceEntry: MaintenanceEntry{RegistrationID: legacyID, CacheUUID: identity.UUID, RepoID: "legacy/repo", Policy: MaintenancePolicy{SyncEnabled: false, SyncMode: "off"}, ConfigHash: configHash, Enabled: true, Generation: 1}, CachePath: cachePath, ConfigSnapshot: cfg},
	}}
	data, _ := json.Marshal(disk)
	registryPath := filepath.Join(dir, "managed-caches.json")
	if err := os.WriteFile(registryPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	jobs := NewJobManager(filepath.Join(dir, "jobs.json"))
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), jobs, registryPath)
	if err := maintenance.Load(); err != nil {
		t.Fatal(err)
	}
	listed, _ := maintenance.List(ctx)
	if len(listed.Entries) != 1 || listed.Entries[0].Enabled || listed.Entries[0].State != "identity_conflict" || listed.Entries[0].IdentityConflict == nil || len(listed.Entries[0].IdentityConflict.CandidateRegistrationIDs) != 2 {
		t.Fatalf("conflict list=%+v", listed)
	}
	reconciled, err := maintenance.Reconcile(ctx)
	if err != nil || len(reconciled.JobsStarted) != 0 || len(jobs.List()) != 0 {
		t.Fatalf("reconcile=%+v jobs=%+v err=%v", reconciled, jobs.List(), err)
	}
	request := testMaintenanceEnrollRequest(cachePath, "must-not-resolve-silently", MaintenancePolicy{SyncEnabled: true, SyncMode: "head", Issues: true})
	if _, err := maintenance.Enroll(ctx, request); err == nil {
		t.Fatal("ordinary enrollment silently resolved identity conflict")
	} else if coded, ok := err.(interface{ DiagnosticCode() string }); !ok || coded.DiagnosticCode() != "identity_conflict" {
		t.Fatalf("conflict enrollment error=%T %v", err, err)
	}
}

func TestMaintenanceLoadBlocksCacheCloneConflict(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first.db")
	secondPath := filepath.Join(dir, "second.db")
	store, err := cache.NewSQLiteStore(ctx, firstPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Aliases: []string{"legacy/repo"}, Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.CachePath = firstPath
	secondCfg := cfg
	secondCfg.CachePath = secondPath
	configHash := maintenanceHash(cfg)
	policy := MaintenancePolicy{SyncEnabled: true, SyncMode: "head", Issues: true}
	canonicalID := maintenanceRegistrationID(identity.UUID, "owner/repo")
	legacyID := maintenanceRegistrationID(identity.UUID, "legacy/repo")
	disk := maintenanceRegistryFile{SchemaVersion: legacyMaintenanceRegistrySchema, Entries: []maintenanceDiskEntry{
		{MaintenanceEntry: MaintenanceEntry{RegistrationID: canonicalID, CacheUUID: identity.UUID, RepoID: "owner/repo", Policy: policy, ConfigHash: configHash, Enabled: true}, CachePath: firstPath, ConfigSnapshot: cfg},
		{MaintenanceEntry: MaintenanceEntry{RegistrationID: legacyID, CacheUUID: identity.UUID, RepoID: "legacy/repo", Policy: policy, ConfigHash: maintenanceHash(secondCfg), Enabled: true}, CachePath: secondPath, ConfigSnapshot: secondCfg},
	}}
	data, _ := json.Marshal(disk)
	registryPath := filepath.Join(dir, "managed-caches.json")
	if err := os.WriteFile(registryPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	jobs := NewJobManager(filepath.Join(dir, "jobs.json"))
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), jobs, registryPath)
	if err := maintenance.Load(); err != nil {
		t.Fatal(err)
	}
	listed, err := maintenance.List(ctx)
	if err != nil || len(listed.Entries) != 1 {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	entry := listed.Entries[0]
	if entry.Enabled || entry.State != "cache_clone_conflict" || entry.LastErrorClass != "cache_clone_conflict" || entry.IdentityConflict == nil || entry.IdentityConflict.Kind != "cache_clone_conflict" || len(entry.IdentityConflict.PathFingerprints) != 2 {
		t.Fatalf("clone conflict=%+v", entry)
	}
	request := testMaintenanceEnrollRequest(firstPath, "clone-must-not-resolve", policy)
	if _, err := maintenance.Enroll(ctx, request); err == nil {
		t.Fatal("ordinary enrollment silently resolved cache clone conflict")
	} else if coded, ok := err.(interface{ DiagnosticCode() string }); !ok || coded.DiagnosticCode() != "cache_clone_conflict" {
		t.Fatalf("clone enrollment error=%T %v", err, err)
	}
	result, err := maintenance.Reconcile(ctx)
	if err != nil || len(result.JobsStarted) != 0 || len(jobs.List()) != 0 {
		t.Fatalf("reconcile=%+v jobs=%+v err=%v", result, jobs.List(), err)
	}
}

func TestMaintenanceLoadCanonicalizesEquivalentRepositoryDocsSourceAuthorities(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	repositoryPath := filepath.Join(dir, "repository")
	if err := os.MkdirAll(repositoryPath, 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Aliases: []string{"legacy/repo"}}); err != nil {
		t.Fatal(err)
	}
	identity, _ := store.CacheIdentity(ctx)
	_ = store.Close()
	canonicalID := maintenanceRegistrationID(identity.UUID, "owner/repo")
	legacyID := maintenanceRegistrationID(identity.UUID, "legacy/repo")
	canonicalSourceID := repositoryDocsSourceRegistrationID(canonicalID, "refs/docs", "HEAD", "docs")
	legacySourceID := repositoryDocsSourceRegistrationID(legacyID, "refs/docs", "HEAD", "docs")
	cfg := config.Default()
	cfg.CachePath = cachePath
	policy := MaintenancePolicy{SyncMode: "off"}
	source := func(id string) repositoryDocsDiskSource {
		return repositoryDocsDiskSource{State: RepositoryDocsMaintenanceState{SourceRegistrationID: id, SourceRegistrationGeneration: 1, GitStoreRef: "refs/docs", WorktreeRef: "HEAD", State: "ready"}, RepositoryPath: repositoryPath, Profile: "docs"}
	}
	admissionWorkKey := "repository-docs-index:durable-migration"
	admissionSetID := "repo-doc-set-durable-migration"
	disk := maintenanceRegistryFile{SchemaVersion: legacyMaintenanceRegistrySchema, Entries: []maintenanceDiskEntry{
		{MaintenanceEntry: MaintenanceEntry{RegistrationID: canonicalID, CacheUUID: identity.UUID, RepoID: "owner/repo", Policy: policy, ConfigHash: maintenanceHash(cfg), Enabled: true}, CachePath: cachePath, ConfigSnapshot: cfg, RepositoryDocsSources: []repositoryDocsDiskSource{source(canonicalSourceID)}},
		{MaintenanceEntry: MaintenanceEntry{RegistrationID: legacyID, CacheUUID: identity.UUID, RepoID: "legacy/repo", Policy: policy, ConfigHash: maintenanceHash(cfg), Enabled: true}, CachePath: cachePath, ConfigSnapshot: cfg, RepositoryDocsSources: []repositoryDocsDiskSource{source(legacySourceID)}},
	}, RepositoryDocsAdmissionQueue: []repositoryDocsAdmissionIntent{{RegistrationID: legacyID, SourceRegistrationID: legacySourceID, SourceRegistrationGeneration: 1, RepoID: "owner/repo", WorkKey: admissionWorkKey, ExpectedRevisionSetID: admissionSetID, JobID: "job-cancelling", Disposition: repositoryDocsAdmissionCancelled, CreatedAt: time.Now().UTC()}}}
	data, _ := json.Marshal(disk)
	registryPath := filepath.Join(dir, "managed-caches.json")
	if err := os.WriteFile(registryPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	jobs := NewJobManager(filepath.Join(dir, "jobs.json"))
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), jobs, registryPath)
	if err := maintenance.Load(); err != nil {
		t.Fatal(err)
	}
	listed, _ := maintenance.List(ctx)
	if len(listed.Entries) != 1 || listed.Entries[0].RegistrationID != canonicalID || listed.Entries[0].IdentityConflict != nil {
		t.Fatalf("canonicalized entries=%+v", listed.Entries)
	}
	registered := maintenance.sources[canonicalID]
	if len(registered) != 1 || registered[canonicalSourceID] == nil || registered[canonicalSourceID].State.SourceRegistrationGeneration != 2 {
		t.Fatalf("canonicalized sources=%+v", registered)
	}
	if maintenance.sourceRedirects[legacySourceID] != canonicalSourceID {
		t.Fatalf("source redirects=%+v", maintenance.sourceRedirects)
	}
	maintenance.mu.Lock()
	_, admission, ok := maintenance.repositoryDocsAdmissionForCurrentAuthorityLocked(canonicalID, canonicalSourceID)
	maintenance.mu.Unlock()
	if !ok || admission.Disposition != repositoryDocsAdmissionCancelled || admission.JobID != "job-cancelling" {
		t.Fatalf("migrated admission=%+v ok=%t", admission, ok)
	}
	if !maintenance.repositoryDocsCancellationCommitted(Job{ID: "job-cancelling", Type: RepositoryDocsIndexJobType, RegistrationID: canonicalID, SourceRegistrationID: canonicalSourceID, SourceRegistrationGeneration: 1, RepoID: "owner/repo", ExpectedRevisionSetID: admissionSetID, WorkRef: publicWorkRef(admissionWorkKey)}) {
		t.Fatal("durable cancellation tombstone was not preserved")
	}
	selected, err := maintenance.repositoryDocsSourceForSelector(RepositoryDocsSourceSelector{RegistrationID: legacyID, SourceRegistrationID: legacySourceID, SourceRegistrationGeneration: 2})
	if err != nil || selected.RegistrationID != canonicalID || selected.SourceRegistrationID != canonicalSourceID {
		t.Fatalf("legacy selector=%+v err=%v", selected, err)
	}
	job := Job{RegistrationID: legacyID, RepoID: "legacy/repo", SourceRegistrationID: legacySourceID}
	jobs.mu.Lock()
	jobs.projectCanonicalRegistrationLocked(&job)
	jobs.mu.Unlock()
	if job.RegistrationID != canonicalID || job.RepoID != "owner/repo" || job.SourceRegistrationID != canonicalSourceID {
		t.Fatalf("projected job=%+v", job)
	}
}

func TestMaintenanceLoadNormalizesRedirectCyclesIntroducedByCanonicalization(t *testing.T) {
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
	identity, _ := store.CacheIdentity(ctx)
	_ = store.Close()
	registrationID := maintenanceRegistrationID(identity.UUID, "owner/repo")
	legacyRegistrationID := "maintenance-future-alias"
	canonicalSourceID := repositoryDocsSourceRegistrationID(registrationID, "git-common-dir", "HEAD", "docs")
	legacySourceID := "repository-docs-future-alias"
	cfg := config.Default()
	cfg.CachePath = cachePath
	source := repositoryDocsDiskSource{State: RepositoryDocsMaintenanceState{SourceRegistrationID: legacySourceID, SourceRegistrationGeneration: 1, GitStoreRef: "git-common-dir", WorktreeRef: "HEAD", State: "ready"}, RepositoryPath: filepath.Join(dir, "repository"), Profile: "docs"}
	disk := maintenanceRegistryFile{
		SchemaVersion: legacyMaintenanceRegistrySchema,
		Entries: []maintenanceDiskEntry{{
			MaintenanceEntry: MaintenanceEntry{RegistrationID: registrationID, LegacyRegistrationIDs: []string{legacyRegistrationID}, CacheUUID: identity.UUID, RepoID: "owner/repo", Policy: MaintenancePolicy{SyncMode: "off"}, ConfigHash: maintenanceHash(cfg), Enabled: true},
			CachePath:        cachePath, ConfigSnapshot: cfg, RepositoryDocsSources: []repositoryDocsDiskSource{source},
		}},
		RegistrationRedirects:       []MaintenanceRegistrationRedirect{{From: registrationID, To: legacyRegistrationID}},
		SourceRegistrationRedirects: []MaintenanceRegistrationRedirect{{From: canonicalSourceID, To: legacySourceID}},
	}
	registryPath := filepath.Join(dir, "managed-caches.json")
	data, _ := json.Marshal(disk)
	if err := os.WriteFile(registryPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	assertNormalized := func(stage string, maintenance *MaintenanceManager) {
		t.Helper()
		if got := maintenance.redirects[legacyRegistrationID]; got != registrationID || maintenance.redirects[registrationID] != "" {
			t.Fatalf("%s registration redirects=%v", stage, maintenance.redirects)
		}
		if got := maintenance.sourceRedirects[legacySourceID]; got != canonicalSourceID || maintenance.sourceRedirects[canonicalSourceID] != "" {
			t.Fatalf("%s source redirects=%v", stage, maintenance.sourceRedirects)
		}
	}
	first := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := first.Load(); err != nil {
		t.Fatal(err)
	}
	assertNormalized("first load", first)
	second := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := second.Load(); err != nil {
		t.Fatal(err)
	}
	assertNormalized("restart", second)
}

func TestMaintenanceLoadBlocksDifferentRepositoryDocsSourceAuthorities(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Aliases: []string{"legacy/repo"}}); err != nil {
		t.Fatal(err)
	}
	identity, _ := store.CacheIdentity(ctx)
	_ = store.Close()
	canonicalID := maintenanceRegistrationID(identity.UUID, "owner/repo")
	legacyID := maintenanceRegistrationID(identity.UUID, "legacy/repo")
	cfg := config.Default()
	cfg.CachePath = cachePath
	policy := MaintenancePolicy{SyncMode: "off"}
	source := func(registrationID, gitStoreRef, repositoryPath string) repositoryDocsDiskSource {
		return repositoryDocsDiskSource{State: RepositoryDocsMaintenanceState{SourceRegistrationID: repositoryDocsSourceRegistrationID(registrationID, gitStoreRef, "HEAD", "docs"), SourceRegistrationGeneration: 1, GitStoreRef: gitStoreRef, WorktreeRef: "HEAD", State: "ready"}, RepositoryPath: repositoryPath, Profile: "docs"}
	}
	disk := maintenanceRegistryFile{SchemaVersion: legacyMaintenanceRegistrySchema, Entries: []maintenanceDiskEntry{
		{MaintenanceEntry: MaintenanceEntry{RegistrationID: canonicalID, CacheUUID: identity.UUID, RepoID: "owner/repo", Policy: policy, ConfigHash: maintenanceHash(cfg), Enabled: true}, CachePath: cachePath, ConfigSnapshot: cfg, RepositoryDocsSources: []repositoryDocsDiskSource{source(canonicalID, "refs/docs", filepath.Join(dir, "canonical-repository"))}},
		{MaintenanceEntry: MaintenanceEntry{RegistrationID: legacyID, CacheUUID: identity.UUID, RepoID: "legacy/repo", Policy: policy, ConfigHash: maintenanceHash(cfg), Enabled: true}, CachePath: cachePath, ConfigSnapshot: cfg, RepositoryDocsSources: []repositoryDocsDiskSource{source(legacyID, "refs/other-docs", filepath.Join(dir, "legacy-repository"))}},
	}}
	data, _ := json.Marshal(disk)
	registryPath := filepath.Join(dir, "managed-caches.json")
	if err := os.WriteFile(registryPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := maintenance.Load(); err != nil {
		t.Fatal(err)
	}
	listed, _ := maintenance.List(ctx)
	if len(listed.Entries) != 1 || listed.Entries[0].IdentityConflict == nil || listed.Entries[0].IdentityConflict.Kind != "identity_conflict" || len(listed.Entries[0].IdentityConflict.Candidates) != 2 {
		t.Fatalf("different source authorities did not fail closed: %+v", listed.Entries)
	}
	left, right := listed.Entries[0].IdentityConflict.Candidates[0], listed.Entries[0].IdentityConflict.Candidates[1]
	if left.SourceAuthorityHash == right.SourceAuthorityHash || left.SourceAuthorityHash == "" || right.SourceAuthorityHash == "" {
		t.Fatalf("source authority evidence=%+v", listed.Entries[0].IdentityConflict.Candidates)
	}
}

func TestMaintenanceIdentityConflictRetainsPairedPrivateCandidatesAcrossRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Aliases: []string{"legacy/repo"}}); err != nil {
		t.Fatal(err)
	}
	identity, _ := store.CacheIdentity(ctx)
	_ = store.Close()
	canonicalID := maintenanceRegistrationID(identity.UUID, "owner/repo")
	legacyID := maintenanceRegistrationID(identity.UUID, "legacy/repo")
	cfgA := config.Default()
	cfgA.CachePath = cachePath
	cfgB := cfgA
	cfgB.MaxRetries++
	disk := maintenanceRegistryFile{SchemaVersion: legacyMaintenanceRegistrySchema, Entries: []maintenanceDiskEntry{
		{MaintenanceEntry: MaintenanceEntry{RegistrationID: canonicalID, CacheUUID: identity.UUID, RepoID: "owner/repo", Policy: MaintenancePolicy{SyncEnabled: true, SyncMode: "head"}, ConfigHash: maintenanceHash(cfgA), Enabled: true}, CachePath: cachePath, ConfigReference: "config-a", ConfigSnapshot: cfgA},
		{MaintenanceEntry: MaintenanceEntry{RegistrationID: legacyID, CacheUUID: identity.UUID, RepoID: "legacy/repo", Policy: MaintenancePolicy{SyncMode: "off"}, ConfigHash: maintenanceHash(cfgB), Enabled: false}, CachePath: cachePath, ConfigReference: "config-b", ConfigSnapshot: cfgB},
	}}
	data, _ := json.Marshal(disk)
	registryPath := filepath.Join(dir, "managed-caches.json")
	if err := os.WriteFile(registryPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	first := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := first.Load(); err != nil {
		t.Fatal(err)
	}
	firstList, _ := first.List(ctx)
	if len(firstList.Entries) != 1 || firstList.Entries[0].IdentityConflict == nil || !firstList.Entries[0].IdentityConflict.DetailsAvailable || len(firstList.Entries[0].IdentityConflict.Candidates) != 2 || len(first.conflictCandidates[canonicalID]) != 2 {
		t.Fatalf("first conflict=%+v private=%+v", firstList.Entries, first.conflictCandidates)
	}
	public, _ := json.Marshal(firstList)
	if strings.Contains(string(public), cachePath) || strings.Contains(string(public), "config-a") || strings.Contains(string(public), "config-b") {
		t.Fatalf("public conflict leaked private authority: %s", public)
	}
	second := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := second.Load(); err != nil {
		t.Fatal(err)
	}
	secondList, _ := second.List(ctx)
	if len(secondList.Entries) != 1 || secondList.Entries[0].IdentityConflict == nil || len(second.conflictCandidates[canonicalID]) != 2 {
		t.Fatalf("reloaded conflict=%+v private=%+v", secondList.Entries, second.conflictCandidates)
	}
	want := map[string]bool{"config-a": true, "config-b": true}
	for _, candidate := range second.conflictCandidates[canonicalID] {
		delete(want, candidate.ConfigReference)
		if candidate.CandidateRef == "" || candidate.Entry.IdentityConflict != nil {
			t.Fatalf("invalid retained candidate=%+v", candidate)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing retained config bundles=%+v", want)
	}
}

func TestMaintenanceCloneConflictGroupsWholeUUIDAcrossRepositoryBindings(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first.db")
	secondPath := filepath.Join(dir, "second.db")
	store, err := cache.NewSQLiteStore(ctx, firstPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, binding := range []cache.RepositoryBinding{{RepoID: "owner/first", Owner: "owner", Name: "first"}, {RepoID: "owner/second", Owner: "owner", Name: "second"}} {
		if err := store.AddRepository(ctx, binding); err != nil {
			t.Fatal(err)
		}
	}
	identity, _ := store.CacheIdentity(ctx)
	_ = store.Close()
	bytes, _ := os.ReadFile(firstPath)
	if err := os.WriteFile(secondPath, bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	firstID := maintenanceRegistrationID(identity.UUID, "owner/first")
	secondID := maintenanceRegistrationID(identity.UUID, "owner/second")
	cfg := config.Default()
	cfg.CachePath = firstPath
	secondCfg := cfg
	secondCfg.CachePath = secondPath
	disk := maintenanceRegistryFile{SchemaVersion: legacyMaintenanceRegistrySchema, Entries: []maintenanceDiskEntry{
		{MaintenanceEntry: MaintenanceEntry{RegistrationID: firstID, CacheUUID: identity.UUID, RepoID: "owner/first", Policy: MaintenancePolicy{SyncMode: "off"}, ConfigHash: maintenanceHash(cfg), Enabled: true}, CachePath: firstPath, ConfigSnapshot: cfg},
		{MaintenanceEntry: MaintenanceEntry{RegistrationID: secondID, CacheUUID: identity.UUID, RepoID: "owner/second", Policy: MaintenancePolicy{SyncMode: "off"}, ConfigHash: maintenanceHash(secondCfg), Enabled: true}, CachePath: secondPath, ConfigSnapshot: secondCfg},
	}}
	data, _ := json.Marshal(disk)
	registryPath := filepath.Join(dir, "managed-caches.json")
	_ = os.WriteFile(registryPath, data, 0o600)
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := maintenance.Load(); err != nil {
		t.Fatal(err)
	}
	listed, _ := maintenance.List(ctx)
	wantID := maintenanceCloneConflictRegistrationID(identity.UUID)
	if len(listed.Entries) != 1 || listed.Entries[0].RegistrationID != wantID || listed.Entries[0].IdentityConflict == nil || len(listed.Entries[0].IdentityConflict.Candidates) != 2 || len(listed.Entries[0].IdentityConflict.PathFingerprints) != 2 {
		t.Fatalf("whole UUID conflict=%+v", listed.Entries)
	}
	if maintenance.redirects[firstID] != wantID || maintenance.redirects[secondID] != wantID {
		t.Fatalf("clone redirects=%+v", maintenance.redirects)
	}
}

func TestMaintenanceUnavailableAliasDuplicatesCollapseThenExpandWhenCacheReturns(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	seedPath := filepath.Join(dir, "seed.db")
	missingPath := filepath.Join(dir, "returned.db")
	store, err := cache.NewSQLiteStore(ctx, seedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Aliases: []string{"legacy/repo"}}); err != nil {
		t.Fatal(err)
	}
	identity, _ := store.CacheIdentity(ctx)
	_ = store.Close()
	seedBytes, _ := os.ReadFile(seedPath)
	canonicalID := maintenanceRegistrationID(identity.UUID, "owner/repo")
	legacyID := maintenanceRegistrationID(identity.UUID, "legacy/repo")
	cfg := config.Default()
	cfg.CachePath = missingPath
	policy := MaintenancePolicy{SyncMode: "off"}
	disk := maintenanceRegistryFile{SchemaVersion: legacyMaintenanceRegistrySchema, Entries: []maintenanceDiskEntry{
		{MaintenanceEntry: MaintenanceEntry{RegistrationID: canonicalID, CacheUUID: identity.UUID, RepoID: "owner/repo", Policy: policy, ConfigHash: maintenanceHash(cfg), Enabled: true}, CachePath: missingPath, ConfigSnapshot: cfg},
		{MaintenanceEntry: MaintenanceEntry{RegistrationID: legacyID, CacheUUID: identity.UUID, RepoID: "legacy/repo", Policy: policy, ConfigHash: maintenanceHash(cfg), Enabled: true}, CachePath: missingPath, ConfigSnapshot: cfg},
	}}
	data, _ := json.Marshal(disk)
	registryPath := filepath.Join(dir, "managed-caches.json")
	_ = os.WriteFile(registryPath, data, 0o600)
	first := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := first.Load(); err != nil {
		t.Fatal(err)
	}
	firstList, _ := first.List(ctx)
	if len(firstList.Entries) != 1 || firstList.Entries[0].State != "identity_unresolved" || firstList.Entries[0].IdentityConflict == nil || len(firstList.Entries[0].IdentityConflict.Candidates) != 2 {
		t.Fatalf("collapsed unavailable aliases=%+v", firstList.Entries)
	}
	if err := os.WriteFile(missingPath, seedBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	second := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := second.Load(); err != nil {
		t.Fatal(err)
	}
	secondList, _ := second.List(ctx)
	if len(secondList.Entries) != 1 || secondList.Entries[0].RegistrationID != canonicalID || !secondList.Entries[0].Enabled || secondList.Entries[0].IdentityConflict != nil {
		t.Fatalf("expanded returned cache=%+v", secondList.Entries)
	}
}

func TestMaintenanceLegacyLossyConflictReportsDetailsUnavailable(t *testing.T) {
	dir := t.TempDir()
	registryPath := filepath.Join(dir, "managed-caches.json")
	disk := maintenanceRegistryFile{SchemaVersion: maintenanceRegistrySchema, Entries: []maintenanceDiskEntry{{MaintenanceEntry: MaintenanceEntry{RegistrationID: "legacy-conflict", CacheUUID: "cache-uuid", RepoID: "owner/repo", Enabled: false, State: "identity_conflict", IdentityConflict: &MaintenanceIdentityConflict{Kind: "identity_conflict", CandidateRegistrationIDs: []string{"a", "b"}}}}}}
	data, _ := json.Marshal(disk)
	_ = os.WriteFile(registryPath, data, 0o600)
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := maintenance.Load(); err != nil {
		t.Fatal(err)
	}
	listed, _ := maintenance.List(context.Background())
	if len(listed.Entries) != 1 || listed.Entries[0].IdentityConflict == nil || listed.Entries[0].IdentityConflict.DetailsAvailable {
		t.Fatalf("legacy conflict details=%+v", listed.Entries)
	}
}

func TestMaintenanceIdentityUnresolvedRestoresPriorEnabledStateAfterCacheReturns(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "cache.db")
	unavailablePath := filepath.Join(dir, "cache.unavailable")
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
	if err := os.Rename(cachePath, unavailablePath); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.CachePath = cachePath
	registrationID := maintenanceRegistrationID(identity.UUID, "owner/repo")
	disk := maintenanceRegistryFile{SchemaVersion: maintenanceRegistrySchema, Generation: 1, Entries: []maintenanceDiskEntry{{MaintenanceEntry: MaintenanceEntry{RegistrationID: registrationID, CacheUUID: identity.UUID, RepoID: "owner/repo", Policy: MaintenancePolicy{}, ConfigHash: maintenanceHash(cfg), Enabled: true, State: "ready"}, CachePath: cachePath, ConfigSnapshot: cfg}}}
	data, _ := json.Marshal(disk)
	registryPath := filepath.Join(dir, "managed-caches.json")
	if err := os.WriteFile(registryPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	blocked := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := blocked.Load(); err != nil {
		t.Fatal(err)
	}
	blockedList, _ := blocked.List(ctx)
	if len(blockedList.Entries) != 1 || blockedList.Entries[0].Enabled || blockedList.Entries[0].State != "identity_unresolved" || blockedList.Entries[0].LastErrorClass != "identity_unresolved" {
		t.Fatalf("blocked=%+v", blockedList)
	}
	if err := os.Rename(unavailablePath, cachePath); err != nil {
		t.Fatal(err)
	}
	recovered := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := recovered.Load(); err != nil {
		t.Fatal(err)
	}
	recoveredList, _ := recovered.List(ctx)
	if len(recoveredList.Entries) != 1 || !recoveredList.Entries[0].Enabled || recoveredList.Entries[0].State != "enrolled" || recoveredList.Entries[0].LastErrorClass != "" {
		t.Fatalf("recovered=%+v", recoveredList)
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
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		<-jobCtx.Done()
		jobs.finishJob(job.ID, JobStatusCancelled, "cancelled")
	}()
	if cancelled, ok, cancelErr := jobs.Cancel(job.ID); cancelErr != nil || !ok || cancelled.Status != JobStatusCancelled {
		t.Fatalf("cancelled=%+v ok=%t err=%v", cancelled, ok, cancelErr)
	}
	select {
	case <-workerDone:
	case <-time.After(time.Second):
		t.Fatal("cancelled worker did not finish before test cleanup")
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

func TestRepositoryDocsCancellationFenceWinsConcurrentWorkerSuccess(t *testing.T) {
	jobs := NewJobManager("")
	jobCtx, cancel := context.WithCancel(context.Background())
	job, created, err := jobs.createCoalescedJobWithIntent(
		RepositoryDocsIndexJobType, "owner/repo", "profile", 0, "work", "cache", "registration", "namespace",
		JobRecoveryIntent{SourceRegistrationID: "source", SourceRegistrationGeneration: 1, ExpectedRevisionSetID: "set"}, cancel,
	)
	if err != nil || !created {
		t.Fatalf("job=%+v created=%t err=%v", job, created, err)
	}
	jobs.updateJob(job.ID, func(stored *Job, now time.Time) { stored.Status = JobStatusRunning })

	tombstoneEntered := make(chan struct{})
	releaseTombstone := make(chan struct{})
	jobs.onRepositoryDocsCancelled = func(Job) error {
		close(tombstoneEntered)
		<-releaseTombstone
		return nil
	}
	cancelResult := make(chan Job, 1)
	cancelErr := make(chan error, 1)
	go func() {
		cancelled, _, err := jobs.Cancel(job.ID)
		cancelResult <- cancelled
		cancelErr <- err
	}()
	<-tombstoneEntered
	coalescedCancelResult := make(chan Job, 1)
	coalescedCancelErr := make(chan error, 1)
	go func() {
		cancelled, _, err := jobs.Cancel(job.ID)
		coalescedCancelResult <- cancelled
		coalescedCancelErr <- err
	}()

	workerFinished := make(chan struct{})
	go func() {
		jobs.updateRepositoryDocsTerminalJob(job.ID, func(stored *Job, now time.Time) {
			stored.Status = JobStatusSucceeded
			stored.UpdatedAt = now
			stored.FinishedAt = &now
			delete(jobs.cancel, job.ID)
		})
		close(workerFinished)
	}()
	select {
	case <-workerFinished:
		t.Fatal("worker published success while durable cancellation was unresolved")
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseTombstone)
	if err := <-cancelErr; err != nil {
		t.Fatal(err)
	}
	cancelled := <-cancelResult
	if err := <-coalescedCancelErr; err != nil {
		t.Fatal(err)
	}
	coalescedCancelled := <-coalescedCancelResult
	<-workerFinished
	if cancelled.Status != JobStatusCancelled || coalescedCancelled.Status != JobStatusCancelled {
		t.Fatalf("cancel results primary=%+v coalesced=%+v", cancelled, coalescedCancelled)
	}
	current, ok := jobs.Get(job.ID)
	if !ok || current.Status != JobStatusCancelled {
		t.Fatalf("concurrent terminal state=%+v found=%t", current, ok)
	}
	select {
	case <-jobCtx.Done():
	default:
		t.Fatal("worker cancellation was not signalled after durable tombstone")
	}
}

func TestRepositoryDocsConcurrentCancellationSharesDurableFailure(t *testing.T) {
	jobs := NewJobManager("")
	jobCtx, cancel := context.WithCancel(context.Background())
	job, created, err := jobs.createCoalescedJobWithIntent(
		RepositoryDocsIndexJobType, "owner/repo", "profile", 0, "work", "cache", "registration", "namespace",
		JobRecoveryIntent{SourceRegistrationID: "source", SourceRegistrationGeneration: 1, ExpectedRevisionSetID: "set"}, cancel,
	)
	if err != nil || !created {
		t.Fatalf("job=%+v created=%t err=%v", job, created, err)
	}
	jobs.updateJob(job.ID, func(stored *Job, now time.Time) { stored.Status = JobStatusRunning })

	tombstoneEntered := make(chan struct{})
	releaseTombstone := make(chan struct{})
	wantErr := errors.New("injected cancellation tombstone failure")
	jobs.onRepositoryDocsCancelled = func(Job) error {
		close(tombstoneEntered)
		<-releaseTombstone
		return wantErr
	}
	type cancelOutcome struct {
		job   Job
		found bool
		err   error
	}
	primary := make(chan cancelOutcome, 1)
	duplicate := make(chan cancelOutcome, 1)
	go func() {
		cancelled, found, cancelErr := jobs.Cancel(job.ID)
		primary <- cancelOutcome{job: cancelled, found: found, err: cancelErr}
	}()
	<-tombstoneEntered
	go func() {
		cancelled, found, cancelErr := jobs.Cancel(job.ID)
		duplicate <- cancelOutcome{job: cancelled, found: found, err: cancelErr}
	}()
	select {
	case result := <-duplicate:
		t.Fatalf("duplicate returned before durable resolution: %+v", result)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseTombstone)
	for name, outcomes := range map[string]<-chan cancelOutcome{"primary": primary, "duplicate": duplicate} {
		select {
		case result := <-outcomes:
			if !result.found || result.job.Status != JobStatusRunning || !errors.Is(result.err, wantErr) {
				t.Fatalf("%s outcome=%+v", name, result)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatalf("%s did not receive the shared cancellation resolution", name)
		}
	}
	select {
	case <-jobCtx.Done():
		t.Fatal("failed durable cancellation signalled the worker")
	default:
	}
}

func TestRepositoryDocsCancellationRecoversCommittedTombstoneAfterSnapshotFailure(t *testing.T) {
	dir := t.TempDir()
	jobsPath := filepath.Join(dir, "jobs.json")
	registryPath := filepath.Join(dir, "registry.json")
	jobs := NewJobManager(jobsPath)
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), jobs, registryPath)
	registrationID, sourceID := "reg-cancel-recover", "source-cancel-recover"
	maintenance.entries[registrationID] = &MaintenanceEntry{RegistrationID: registrationID, RepoID: "owner/repo"}
	maintenance.sources[registrationID] = map[string]*repositoryDocsRegisteredSource{sourceID: {
		State:          RepositoryDocsMaintenanceState{SourceRegistrationID: sourceID, SourceRegistrationGeneration: 1, State: "indexing"},
		RepositoryPath: filepath.Join(dir, "repo"),
	}}
	maintenance.admissions[repositoryDocsAdmissionKey(registrationID, sourceID)] = repositoryDocsAdmissionIntent{
		RegistrationID: registrationID, SourceRegistrationID: sourceID, SourceRegistrationGeneration: 1,
		RepoID: "owner/repo", WorkKey: "work", ExpectedRevisionSetID: "set", Disposition: repositoryDocsAdmissionPending, CreatedAt: time.Now().UTC(),
	}
	jobCtx, cancel := context.WithCancel(context.Background())
	job, created, err := jobs.createCoalescedJobWithIntent(
		RepositoryDocsIndexJobType, "owner/repo", "profile", 0, "work", "cache", registrationID, "namespace",
		JobRecoveryIntent{SourceRegistrationID: sourceID, SourceRegistrationGeneration: 1, ExpectedRevisionSetID: "set"}, cancel,
	)
	if err != nil || !created {
		t.Fatalf("job=%+v created=%t err=%v", job, created, err)
	}
	jobs.updateJob(job.ID, func(stored *Job, now time.Time) { stored.Status = JobStatusRunning })

	writes := 0
	jobs.writeFile = func(path string, data []byte, mode os.FileMode) error {
		writes++
		if writes == 2 {
			return errors.New("injected terminal snapshot failure")
		}
		return durableAtomicWriteFile(path, data, mode)
	}
	_, actionErr := NewJobActionManager("", jobs, nil).Cancel(context.Background(), adminhttp.JobActionRequest{JobID: job.ID, IdempotencyKey: "cancel-snapshot-failure"})
	var publicErr adminhttp.JobActionError
	if !errors.As(actionErr, &publicErr) || publicErr.Code != "repository_docs_cancel_snapshot_failed" || !strings.Contains(publicErr.Message, "Cancellation is durable") {
		t.Fatalf("public snapshot error=%T %+v", actionErr, publicErr)
	}
	current, found := jobs.Get(job.ID)
	if !found || current.Status != JobStatusCancelled {
		t.Fatalf("in-memory terminal cancellation=%+v found=%t", current, found)
	}
	select {
	case <-jobCtx.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("worker was not signalled after the durable tombstone")
	}

	restartedJobs := NewJobManager(jobsPath)
	restartedMaintenance := NewMaintenanceManager(newTestManager(t, "darwin"), restartedJobs, registryPath)
	if err := restartedMaintenance.Load(); err != nil {
		t.Fatal(err)
	}
	if err := restartedJobs.LoadAndMarkInterrupted(); err != nil {
		t.Fatal(err)
	}
	recovered, found := restartedJobs.Get(job.ID)
	if !found || recovered.Status != JobStatusCancelled || recovered.ErrorClass != "cancelled" {
		t.Fatalf("recovered cancellation=%+v found=%t", recovered, found)
	}
	intent, ok := restartedMaintenance.repositoryDocsAdmission(registrationID, sourceID)
	if !ok || intent.Disposition != repositoryDocsAdmissionCancelled || intent.JobID != job.ID {
		t.Fatalf("recovered tombstone=%+v ok=%t", intent, ok)
	}
}

func TestRepositoryDocsCancellationRecoveryRequiresExactWorkIdentity(t *testing.T) {
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), nil, "")
	intent := repositoryDocsAdmissionIntent{
		RegistrationID: "registration", SourceRegistrationID: "source", SourceRegistrationGeneration: 3,
		RepoID: "owner/repo", WorkKey: "private-work-key", ExpectedRevisionSetID: "set", JobID: "job-1",
		Disposition: repositoryDocsAdmissionCancelled,
	}
	maintenance.admissions[repositoryDocsAdmissionKey(intent.RegistrationID, intent.SourceRegistrationID)] = intent
	matching := Job{
		ID: "job-1", Type: RepositoryDocsIndexJobType, RepoID: "owner/repo", RegistrationID: "registration",
		SourceRegistrationID: "source", SourceRegistrationGeneration: 3, ExpectedRevisionSetID: "set",
		WorkRef: publicWorkRef(intent.WorkKey), Status: JobStatusCancelling,
	}
	if !maintenance.repositoryDocsCancellationCommitted(matching) {
		t.Fatal("exact durable cancellation identity was not recognized")
	}
	wrongRepo := matching
	wrongRepo.RepoID = "other/repo"
	if maintenance.repositoryDocsCancellationCommitted(wrongRepo) {
		t.Fatal("repository mismatch accepted a cancellation tombstone")
	}
	wrongWork := matching
	wrongWork.WorkRef = publicWorkRef("different-private-work-key")
	if maintenance.repositoryDocsCancellationCommitted(wrongWork) {
		t.Fatal("work identity mismatch accepted a cancellation tombstone")
	}
}

func TestRepositoryDocsCancellingWithoutCommittedTombstoneRecoversInterrupted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	before := NewJobManager(path)
	now := time.Now().UTC()
	before.jobs["job-1"] = &Job{
		ID: "job-1", Type: RepositoryDocsIndexJobType, RepoID: "owner/repo", RegistrationID: "registration",
		SourceRegistrationID: "source", SourceRegistrationGeneration: 1, ExpectedRevisionSetID: "set",
		WorkRef: publicWorkRef("work"), Status: JobStatusCancelling, CreatedAt: now, UpdatedAt: now,
	}
	if err := before.saveLocked(); err != nil {
		t.Fatal(err)
	}
	after := NewJobManager(path)
	after.repositoryDocsCancellationCommitted = func(Job) bool { return false }
	if err := after.LoadAndMarkInterrupted(); err != nil {
		t.Fatal(err)
	}
	recovered, found := after.Get("job-1")
	if !found || recovered.Status != JobStatusInterrupted {
		t.Fatalf("uncommitted cancellation recovery=%+v found=%t", recovered, found)
	}
}

func TestRepositoryDocsGenerationFenceSuppressesLateCancellationPersistence(t *testing.T) {
	jobs := NewJobManager("")
	now := time.Now().UTC()
	jobs.jobs["job-fenced"] = &Job{
		ID: "job-fenced", Type: RepositoryDocsIndexJobType, Status: JobStatusSuperseded,
		RegistrationID: "registration", SourceRegistrationID: "source", SourceRegistrationGeneration: 1,
		ExpectedRevisionSetID: "set", UpdatedAt: now, FinishedAt: &now,
		ErrorClass: "repository_docs_source_generation_superseded",
	}
	called := false
	jobs.onRepositoryDocsCancelled = func(Job) error {
		called = true
		return errors.New("late cancellation persistence")
	}

	jobs.persistRepositoryDocsTerminalCancellation("job-fenced")
	if called {
		t.Fatal("superseded generation attempted late cancellation persistence")
	}
	current, ok := jobs.Get("job-fenced")
	if !ok || current.Status != JobStatusSuperseded || current.ErrorClass != "repository_docs_source_generation_superseded" {
		t.Fatalf("generation fence was overwritten: %+v found=%t", current, ok)
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
	jobs.markWorkerFinished(failed.ID)
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

func TestCacheWriterAdmissionRetainsIndependentIntentAcrossRepeatedContention(t *testing.T) {
	jobs := NewJobManager(filepath.Join(t.TempDir(), "jobs.json"))
	first, created, err := jobs.createCoalescedJob(SyncJobType, "owner/left", "", 0, "sync:cache-1:owner/left:head", "cache-1", "reg-left", "", func() {})
	if err != nil || !created {
		t.Fatalf("first=%+v created=%t err=%v", first, created, err)
	}

	const independentWork = "rag:cache-1:owner/right:profile"
	for attempt := 0; attempt < 3; attempt++ {
		_, created, err = jobs.createCoalescedJob(RAGIndexJobType, "owner/right", "profile", 0, independentWork, "cache-1", "reg-right", "ns", func() {})
		var busy ErrCacheWriterBusy
		if created || !errors.As(err, &busy) || busy.ActiveJobID != first.ID {
			t.Fatalf("attempt=%d created=%t err=%T %v busy=%+v", attempt, created, err, err, busy)
		}
		if listed := jobs.List(); len(listed) != 1 || listed[0].WorkKey != first.WorkKey {
			t.Fatalf("contention changed durable intent: %+v", listed)
		}
	}

	// Publishing a terminal state is not enough to admit a possible late
	// starter. The cache remains fenced until the worker has actually unwound.
	jobs.updateJob(first.ID, func(job *Job, now time.Time) {
		job.Status = JobStatusSucceeded
		job.UpdatedAt = now
		job.FinishedAt = &now
	})
	_, created, err = jobs.createCoalescedJob(RAGIndexJobType, "owner/right", "profile", 0, independentWork, "cache-1", "reg-right", "ns", func() {})
	var busy ErrCacheWriterBusy
	if created || !errors.As(err, &busy) || busy.ActiveJobID != first.ID {
		t.Fatalf("terminal worker still in flight: created=%t err=%T %v busy=%+v", created, err, err, busy)
	}

	jobs.markWorkerFinished(first.ID)
	next, created, err := jobs.createCoalescedJob(RAGIndexJobType, "owner/right", "profile", 0, independentWork, "cache-1", "reg-right", "ns", func() {})
	if err != nil || !created || next.WorkKey != independentWork || next.RepoID != "owner/right" || next.Type != RAGIndexJobType {
		t.Fatalf("independent intent after release=%+v created=%t err=%v", next, created, err)
	}
}

func TestCacheWriterAdmissionRestartReleasesInterruptedWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	jobs := NewJobManager(path)
	first, created, err := jobs.createCoalescedJob(SyncJobType, "owner/left", "", 0, "sync:cache-1:owner/left:head", "cache-1", "reg-left", "", func() {})
	if err != nil || !created {
		t.Fatalf("first=%+v created=%t err=%v", first, created, err)
	}

	restarted := NewJobManager(path)
	if err := restarted.LoadAndMarkInterrupted(); err != nil {
		t.Fatal(err)
	}
	interrupted, ok := restarted.Get(first.ID)
	if !ok || interrupted.Status != JobStatusInterrupted || interrupted.FinishedAt == nil {
		t.Fatalf("interrupted=%+v found=%t", interrupted, ok)
	}

	next, created, err := restarted.createCoalescedJob(RAGIndexJobType, "owner/right", "profile", 0, "rag:cache-1:owner/right:profile", "cache-1", "reg-right", "ns", func() {})
	if err != nil || !created || next.ID == first.ID || next.CacheUUID != first.CacheUUID {
		t.Fatalf("post-restart writer=%+v created=%t err=%v", next, created, err)
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
