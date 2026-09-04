package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
)

func TestMaintenanceConflictResolutionPlanApplyReplayAndRestart(t *testing.T) {
	ctx := context.Background()
	maintenance, registryPath, canonicalID := newMaintenanceIdentityConflictFixture(t)
	listed, _ := maintenance.List(ctx)
	conflict := listed.Entries[0]
	var selected MaintenanceIdentityCandidate
	for _, candidate := range conflict.IdentityConflict.Candidates {
		if candidate.RegistrationID == canonicalID {
			selected = candidate
		}
	}
	request := MaintenanceConflictResolutionRequest{RegistrationID: conflict.RegistrationID, CandidateRef: selected.CandidateRef, ExpectedGeneration: conflict.Generation}
	plan, err := maintenance.PlanConflictResolution(ctx, request)
	if err != nil || plan.Status != "ready" || plan.PlanID == "" || plan.CanonicalRegistrationID != canonicalID || plan.Selected.CandidateRef != selected.CandidateRef {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	stale := request
	stale.PlanID, stale.IdempotencyKey = "wrong-plan", "resolution-key-stale"
	if _, err := maintenance.ApplyConflictResolution(ctx, stale); err == nil {
		t.Fatal("stale plan applied")
	} else if coded, ok := err.(interface{ DiagnosticCode() string }); !ok || coded.DiagnosticCode() != "stale_plan" {
		t.Fatalf("stale error=%T %v", err, err)
	}
	request.PlanID, request.IdempotencyKey = plan.PlanID, "resolution-key-1"
	result, err := maintenance.ApplyConflictResolution(ctx, request)
	if err != nil || result.Outcome != "resolved" || result.Replayed || result.RegistrationID != canonicalID || result.ReceiptID == "" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	after, _ := maintenance.List(ctx)
	if len(after.Entries) != 1 || after.Entries[0].RegistrationID != canonicalID || after.Entries[0].IdentityConflict != nil || !after.Entries[0].Enabled || after.Entries[0].Policy.SyncMode != "head" {
		t.Fatalf("resolved state=%+v", after.Entries)
	}
	replayed, err := maintenance.ApplyConflictResolution(ctx, request)
	if err != nil || !replayed.Replayed || replayed.ReceiptID != result.ReceiptID {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	restarted := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	replayed, err = restarted.ApplyConflictResolution(ctx, request)
	if err != nil || !replayed.Replayed || replayed.ReceiptID != result.ReceiptID {
		t.Fatalf("restart replay=%+v err=%v", replayed, err)
	}
	durable, _ := os.ReadFile(registryPath)
	if !json.Valid(durable) || string(durable) == "" {
		t.Fatalf("invalid durable registry: %s", durable)
	}
}

func TestMaintenanceConflictResolutionPersistenceFailureRollsBackMutationAndReceipt(t *testing.T) {
	ctx := context.Background()
	maintenance, _, canonicalID := newMaintenanceIdentityConflictFixture(t)
	listed, _ := maintenance.List(ctx)
	conflict := listed.Entries[0]
	selected := conflict.IdentityConflict.Candidates[0]
	request := MaintenanceConflictResolutionRequest{RegistrationID: conflict.RegistrationID, CandidateRef: selected.CandidateRef, ExpectedGeneration: conflict.Generation}
	plan, err := maintenance.PlanConflictResolution(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	request.PlanID, request.IdempotencyKey = plan.PlanID, "atomic-resolution"
	injected := errors.New("injected atomic persistence failure")
	maintenance.writeFile = func(string, []byte, os.FileMode) error { return injected }
	if _, err := maintenance.ApplyConflictResolution(ctx, request); !errors.Is(err, injected) {
		t.Fatalf("apply error=%v", err)
	}
	after, _ := maintenance.List(ctx)
	if len(after.Entries) != 1 || after.Entries[0].IdentityConflict == nil || len(maintenance.resolutionReceipts) != 0 || maintenance.entries[canonicalID] == nil {
		t.Fatalf("rollback state=%+v receipts=%+v", after.Entries, maintenance.resolutionReceipts)
	}
	maintenance.writeFile = durableAtomicWriteFile
	if _, err := maintenance.ApplyConflictResolution(ctx, request); err != nil {
		t.Fatalf("retry after rollback=%v", err)
	}
}

func TestMaintenanceCloneResolutionRetiresUnselectedPath(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	firstPath := filepath.Join(dir, "first.db")
	secondPath := filepath.Join(dir, "second.db")
	store, err := cache.NewSQLiteStore(ctx, firstPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Aliases: []string{"legacy/repo"}}); err != nil {
		t.Fatal(err)
	}
	identity, _ := store.CacheIdentity(ctx)
	_ = store.Close()
	bytes, _ := os.ReadFile(firstPath)
	_ = os.WriteFile(secondPath, bytes, 0o600)
	canonicalID := maintenanceRegistrationID(identity.UUID, "owner/repo")
	legacyID := maintenanceRegistrationID(identity.UUID, "legacy/repo")
	cfg := config.Default()
	cfg.CachePath = firstPath
	secondCfg := cfg
	secondCfg.CachePath = secondPath
	policy := MaintenancePolicy{SyncMode: "off"}
	disk := maintenanceRegistryFile{SchemaVersion: legacyMaintenanceRegistrySchema, Entries: []maintenanceDiskEntry{
		{MaintenanceEntry: MaintenanceEntry{RegistrationID: canonicalID, CacheUUID: identity.UUID, RepoID: "owner/repo", Policy: policy, ConfigHash: maintenanceHash(cfg), Enabled: true}, CachePath: firstPath, ConfigSnapshot: cfg},
		{MaintenanceEntry: MaintenanceEntry{RegistrationID: legacyID, CacheUUID: identity.UUID, RepoID: "legacy/repo", Policy: policy, ConfigHash: maintenanceHash(secondCfg), Enabled: true}, CachePath: secondPath, ConfigSnapshot: secondCfg},
	}}
	data, _ := json.Marshal(disk)
	registryPath := filepath.Join(dir, "managed-caches.json")
	_ = os.WriteFile(registryPath, data, 0o600)
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := maintenance.Load(); err != nil {
		t.Fatal(err)
	}
	listed, _ := maintenance.List(ctx)
	conflict := listed.Entries[0]
	var selected MaintenanceIdentityCandidate
	for _, candidate := range conflict.IdentityConflict.Candidates {
		if candidate.PathFingerprint == pathFingerprint(maintenanceCanonicalPathKey(firstPath)) {
			selected = candidate
		}
	}
	request := MaintenanceConflictResolutionRequest{RegistrationID: conflict.RegistrationID, CandidateRef: selected.CandidateRef, ExpectedGeneration: conflict.Generation}
	plan, err := maintenance.PlanConflictResolution(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	request.PlanID, request.IdempotencyKey = plan.PlanID, "clone-resolution"
	result, err := maintenance.ApplyConflictResolution(ctx, request)
	if err != nil || result.RetiredClonePaths != 1 {
		t.Fatalf("clone result=%+v err=%v", result, err)
	}
	cloneEnroll := testMaintenanceEnrollRequest(secondPath, "retired-clone-enroll", policy)
	cloneEnroll.RepoID = "legacy/repo"
	if _, err := maintenance.Enroll(ctx, cloneEnroll); err == nil {
		t.Fatal("retired clone path re-enrolled")
	} else if coded, ok := err.(interface{ DiagnosticCode() string }); !ok || coded.DiagnosticCode() != "cache_clone_retired" {
		t.Fatalf("retired clone error=%T %v", err, err)
	}
	restarted := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Enroll(ctx, cloneEnroll); err == nil {
		t.Fatal("retired clone path re-enrolled after restart")
	}
}

func TestMaintenanceCloneResolutionRetiresInvalidSnapshotAuthorityAcrossRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	invalidPath, validPath := filepath.Join(dir, "invalid.db"), filepath.Join(dir, "valid.db")
	store, err := cache.NewSQLiteStore(ctx, invalidPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddRepository(ctx, cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo"}); err != nil {
		t.Fatal(err)
	}
	identity, _ := store.CacheIdentity(ctx)
	_ = store.Close()
	bytes, _ := os.ReadFile(invalidPath)
	if err := os.WriteFile(validPath, bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	invalidConfig := config.Default()
	invalidConfig.CachePath = validPath // Deliberately belongs to the other authority.
	validConfig := config.Default()
	validConfig.CachePath = validPath
	registrationID := maintenanceRegistrationID(identity.UUID, "owner/repo")
	policy := MaintenancePolicy{SyncMode: "off"}
	disk := maintenanceRegistryFile{SchemaVersion: legacyMaintenanceRegistrySchema, Entries: []maintenanceDiskEntry{
		{MaintenanceEntry: MaintenanceEntry{RegistrationID: registrationID, CacheUUID: identity.UUID, RepoID: "owner/repo", Policy: policy, ConfigHash: maintenanceHash(invalidConfig), Enabled: true}, CachePath: invalidPath, ConfigSnapshot: invalidConfig},
		{MaintenanceEntry: MaintenanceEntry{RegistrationID: registrationID, CacheUUID: identity.UUID, RepoID: "owner/repo", Policy: policy, ConfigHash: maintenanceHash(validConfig), Enabled: true}, CachePath: validPath, ConfigSnapshot: validConfig},
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
	if len(listed.Entries) != 1 || listed.Entries[0].IdentityConflict == nil || len(listed.Entries[0].IdentityConflict.Candidates) != 2 {
		t.Fatalf("mixed clone cohort=%+v", listed.Entries)
	}
	conflict := listed.Entries[0]
	invalidFingerprint := pathFingerprint(maintenanceCanonicalPathKey(invalidPath))
	validFingerprint := pathFingerprint(maintenanceCanonicalPathKey(validPath))
	var invalidCandidate MaintenanceIdentityCandidate
	for _, candidate := range conflict.IdentityConflict.Candidates {
		switch candidate.PathFingerprint {
		case invalidFingerprint:
			invalidCandidate = candidate
		case validFingerprint:
			// The valid selection is intentionally recovered after restart below.
		}
	}
	if invalidCandidate.CandidateRef == "" {
		t.Fatalf("missing invalid authority evidence: %+v", conflict.IdentityConflict.Candidates)
	}
	if _, err := maintenance.PlanConflictResolution(ctx, MaintenanceConflictResolutionRequest{RegistrationID: conflict.RegistrationID, CandidateRef: invalidCandidate.CandidateRef, ExpectedGeneration: conflict.Generation}); err == nil {
		t.Fatal("invalid snapshot authority was selectable")
	} else if coded, ok := err.(interface{ DiagnosticCode() string }); !ok || coded.DiagnosticCode() != "conflict_candidate_identity_changed" {
		t.Fatalf("invalid selection error=%T %v", err, err)
	}

	restarted := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	listed, _ = restarted.List(ctx)
	conflict = listed.Entries[0]
	var validCandidate MaintenanceIdentityCandidate
	for _, candidate := range conflict.IdentityConflict.Candidates {
		if candidate.PathFingerprint == validFingerprint {
			validCandidate = candidate
		}
	}
	if validCandidate.CandidateRef == "" || len(conflict.IdentityConflict.Candidates) != 2 {
		t.Fatalf("restarted mixed clone cohort=%+v", conflict.IdentityConflict.Candidates)
	}
	req := MaintenanceConflictResolutionRequest{RegistrationID: conflict.RegistrationID, CandidateRef: validCandidate.CandidateRef, ExpectedGeneration: conflict.Generation}
	plan, err := restarted.PlanConflictResolution(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.PlanID, req.IdempotencyKey = plan.PlanID, "resolve-valid-clone-authority"
	result, err := restarted.ApplyConflictResolution(ctx, req)
	if err != nil || result.RetiredClonePaths != 1 {
		t.Fatalf("resolve valid authority=%+v err=%v", result, err)
	}

	afterRestart := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := afterRestart.Load(); err != nil {
		t.Fatal(err)
	}
	repairedEnroll := testMaintenanceEnrollRequest(invalidPath, "repaired-invalid-clone", policy)
	if _, err := afterRestart.Enroll(ctx, repairedEnroll); err == nil {
		t.Fatal("repaired invalid clone authority resurrected after retirement")
	} else if coded, ok := err.(interface{ DiagnosticCode() string }); !ok || coded.DiagnosticCode() != "cache_clone_retired" {
		t.Fatalf("retired repaired clone error=%T %v", err, err)
	}
}

func TestMaintenanceCloneResolutionRetainsEveryRepositoryOnSelectedPath(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	firstPath, secondPath := filepath.Join(dir, "first.db"), filepath.Join(dir, "second.db")
	store, err := cache.NewSQLiteStore(ctx, firstPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, binding := range []cache.RepositoryBinding{
		{RepoID: "owner/first", Owner: "owner", Name: "first"},
		{RepoID: "owner/second", Owner: "owner", Name: "second"},
	} {
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
	legacyFirstID, legacySecondID := "maintenance-legacy-first-v0", "maintenance-legacy-second-v0"
	sourceIDs := map[string]string{
		firstID:  repositoryDocsSourceRegistrationID(firstID, "git-common-dir", "HEAD", "docs"),
		secondID: repositoryDocsSourceRegistrationID(secondID, "git-common-dir", "HEAD", "docs"),
	}
	entries := []maintenanceDiskEntry{}
	admissions := []repositoryDocsAdmissionIntent{}
	for _, cachePath := range []string{firstPath, secondPath} {
		cfg := config.Default()
		cfg.CachePath = cachePath
		fingerprint := pathFingerprint(maintenanceCanonicalPathKey(cachePath))
		firstSource := repositoryDocsDiskSource{State: RepositoryDocsMaintenanceState{SourceRegistrationID: sourceIDs[firstID], SourceRegistrationGeneration: 1, GitStoreRef: "git-common-dir", WorktreeRef: "HEAD", State: "ready"}, RepositoryPath: filepath.Join(dir, "repo-first"), Profile: "docs"}
		secondSource := repositoryDocsDiskSource{State: RepositoryDocsMaintenanceState{SourceRegistrationID: sourceIDs[secondID], SourceRegistrationGeneration: 1, GitStoreRef: "git-common-dir", WorktreeRef: "HEAD", State: "ready"}, RepositoryPath: filepath.Join(dir, "repo-second"), Profile: "docs"}
		entries = append(entries,
			maintenanceDiskEntry{MaintenanceEntry: MaintenanceEntry{RegistrationID: firstID, LegacyRegistrationIDs: []string{legacyFirstID}, CacheUUID: identity.UUID, RepoID: "owner/first", Policy: MaintenancePolicy{SyncEnabled: true, SyncMode: "head", Issues: true}, ConfigHash: maintenanceHash(cfg), Enabled: true}, CachePath: cachePath, ConfigSnapshot: cfg, RepositoryDocsSources: []repositoryDocsDiskSource{firstSource}},
			maintenanceDiskEntry{MaintenanceEntry: MaintenanceEntry{RegistrationID: secondID, LegacyRegistrationIDs: []string{legacySecondID}, CacheUUID: identity.UUID, RepoID: "owner/second", Policy: MaintenancePolicy{SyncMode: "off"}, ConfigHash: maintenanceHash(cfg), Enabled: false}, CachePath: cachePath, ConfigSnapshot: cfg, RepositoryDocsSources: []repositoryDocsDiskSource{secondSource}},
		)
		if cachePath == firstPath {
			admissions = append(admissions,
				repositoryDocsAdmissionIntent{RegistrationID: firstID, SourceRegistrationID: sourceIDs[firstID], SourceRegistrationGeneration: 1, AuthorityPathFingerprint: fingerprint, RepoID: "owner/first", WorkKey: "clone-first-work", ExpectedRevisionSetID: "clone-first-set", JobID: "job-000003", Disposition: repositoryDocsAdmissionCancelled, CreatedAt: time.Now().UTC()},
				repositoryDocsAdmissionIntent{RegistrationID: secondID, SourceRegistrationID: sourceIDs[secondID], SourceRegistrationGeneration: 1, AuthorityPathFingerprint: fingerprint, RepoID: "owner/second", WorkKey: "clone-second-work", ExpectedRevisionSetID: "clone-second-set", JobID: "job-000004", Disposition: repositoryDocsAdmissionCancelled, CreatedAt: time.Now().UTC()},
			)
		}
	}
	data, _ := json.Marshal(maintenanceRegistryFile{SchemaVersion: legacyMaintenanceRegistrySchema, Entries: entries, RepositoryDocsAdmissionQueue: admissions})
	registryPath := filepath.Join(dir, "managed-caches.json")
	_ = os.WriteFile(registryPath, data, 0o600)
	jobsPath := filepath.Join(dir, "jobs.json")
	now := time.Now().UTC()
	jobsData, _ := json.Marshal([]Job{
		{ID: "job-000001", Type: SyncJobType, RepoID: "owner/first", CacheUUID: identity.UUID, RegistrationID: firstID, Status: JobStatusSucceeded, CreatedAt: now, UpdatedAt: now},
		{ID: "job-000002", Type: SyncJobType, RepoID: "owner/second", CacheUUID: identity.UUID, RegistrationID: secondID, Status: JobStatusSucceeded, CreatedAt: now, UpdatedAt: now},
		{ID: "job-000003", Type: RepositoryDocsIndexJobType, RepoID: "owner/first", CacheUUID: identity.UUID, RegistrationID: firstID, SourceRegistrationID: sourceIDs[firstID], SourceRegistrationGeneration: 1, ExpectedRevisionSetID: "clone-first-set", WorkRef: publicWorkRef("clone-first-work"), Status: JobStatusCancelling, CreatedAt: now, UpdatedAt: now},
		{ID: "job-000004", Type: RepositoryDocsIndexJobType, RepoID: "owner/second", CacheUUID: identity.UUID, RegistrationID: secondID, SourceRegistrationID: sourceIDs[secondID], SourceRegistrationGeneration: 1, ExpectedRevisionSetID: "clone-second-set", WorkRef: publicWorkRef("clone-second-work"), Status: JobStatusCancelling, CreatedAt: now, UpdatedAt: now},
	})
	_ = os.WriteFile(jobsPath, jobsData, 0o600)
	jobs := NewJobManager(jobsPath)
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), jobs, registryPath)
	if err := maintenance.Load(); err != nil {
		t.Fatal(err)
	}
	if err := jobs.LoadAndMarkInterrupted(); err != nil {
		t.Fatal(err)
	}
	for id, wantRepo := range map[string]string{"job-000001": "owner/first", "job-000002": "owner/second"} {
		job, ok := jobs.Get(id)
		if !ok || job.RepoID != wantRepo || job.RegistrationID != maintenanceRegistrationID(identity.UUID, wantRepo) {
			t.Fatalf("clone conflict relabeled historical job %s: %+v", id, job)
		}
	}
	listed, _ := maintenance.List(ctx)
	if len(listed.Entries) != 1 || listed.Entries[0].IdentityConflict == nil {
		t.Fatalf("clone cohort=%+v", listed.Entries)
	}
	conflict := listed.Entries[0]
	if !stringSlicesEqual(conflict.LegacyRegistrationIDs, []string{firstID, secondID, legacyFirstID, legacySecondID}) {
		t.Fatalf("clone cohort lost transitive legacy ids: %v", conflict.LegacyRegistrationIDs)
	}
	for id, wantRegistration := range map[string]string{"job-000003": firstID, "job-000004": secondID} {
		job, ok := jobs.Get(id)
		if !ok || job.Status != JobStatusCancelled || job.RegistrationID != wantRegistration {
			t.Fatalf("clone cancellation recovery %s=%+v found=%t", id, job, ok)
		}
	}
	if len(conflict.IdentityConflict.Candidates) != 2 {
		t.Fatalf("path candidates=%+v", conflict.IdentityConflict.Candidates)
	}
	var selected MaintenanceIdentityCandidate
	for _, candidate := range conflict.IdentityConflict.Candidates {
		if candidate.PathFingerprint == pathFingerprint(maintenanceCanonicalPathKey(firstPath)) {
			selected = candidate
		}
	}
	if len(selected.Members) != 2 || len(selected.CohortRepoIDs) != 2 {
		t.Fatalf("selected path cohort=%+v", selected)
	}
	req := MaintenanceConflictResolutionRequest{RegistrationID: conflict.RegistrationID, CandidateRef: selected.CandidateRef, ExpectedGeneration: conflict.Generation}
	plan, err := maintenance.PlanConflictResolution(ctx, req)
	if err != nil || len(plan.ResultRegistrationIDs) != 2 || plan.CanonicalRegistrationID != "" {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	req.PlanID, req.IdempotencyKey = plan.PlanID, "multi-repo-clone-resolution"
	result, err := maintenance.ApplyConflictResolution(ctx, req)
	if err != nil || len(result.RegistrationIDs) != 2 || result.RetiredClonePaths != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	after, _ := maintenance.List(ctx)
	if len(after.Entries) != 2 {
		t.Fatalf("resolved entries=%+v", after.Entries)
	}
	byRepo := map[string]MaintenanceEntry{}
	for _, entry := range after.Entries {
		byRepo[entry.RepoID] = entry
	}
	if !byRepo["owner/first"].Enabled || byRepo["owner/first"].Policy.SyncMode != "head" || byRepo["owner/second"].Enabled || byRepo["owner/second"].Policy.SyncMode != "off" {
		t.Fatalf("per-repository state=%+v", byRepo)
	}
	for id, wantRepo := range map[string]string{"job-000001": "owner/first", "job-000002": "owner/second"} {
		job, _ := jobs.Get(id)
		if job.RepoID != wantRepo || job.RegistrationID != maintenanceRegistrationID(identity.UUID, wantRepo) {
			t.Fatalf("clone apply relabeled historical job %s: %+v", id, job)
		}
	}
	restartedJobs := NewJobManager(jobsPath)
	restarted := NewMaintenanceManager(newTestManager(t, "darwin"), restartedJobs, registryPath)
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	if err := restartedJobs.LoadAndMarkInterrupted(); err != nil {
		t.Fatal(err)
	}
	for id, wantRepo := range map[string]string{"job-000001": "owner/first", "job-000002": "owner/second"} {
		job, _ := restartedJobs.Get(id)
		if job.RepoID != wantRepo || job.RegistrationID != maintenanceRegistrationID(identity.UUID, wantRepo) {
			t.Fatalf("restart relabeled historical job %s: %+v", id, job)
		}
	}
	for id, wantRegistration := range map[string]string{"job-000003": firstID, "job-000004": secondID} {
		job, ok := restartedJobs.Get(id)
		if !ok || job.Status != JobStatusCancelled || job.RegistrationID != wantRegistration {
			t.Fatalf("restart clone cancellation %s=%+v found=%t", id, job, ok)
		}
	}
}

func TestMaintenanceCloneResolutionRestoresOnlySelectedPathAuthorities(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	firstPath, secondPath := filepath.Join(dir, "first.db"), filepath.Join(dir, "second.db")
	store, err := cache.NewSQLiteStore(ctx, firstPath)
	if err != nil {
		t.Fatal(err)
	}
	binding := cache.RepositoryBinding{RepoID: "owner/repo", Owner: "owner", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}
	if err := store.AddRepository(ctx, binding); err != nil {
		t.Fatal(err)
	}
	identity, _ := store.CacheIdentity(ctx)
	_ = store.Close()
	bytes, _ := os.ReadFile(firstPath)
	if err := os.WriteFile(secondPath, bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	registrationID := maintenanceRegistrationID(identity.UUID, "owner/repo")
	sourceID := "repository-docs-source-shared"
	firstConfig, secondConfig := config.Default(), config.Default()
	firstConfig.CachePath, secondConfig.CachePath = firstPath, secondPath
	firstPolicy, _ := normalizeMaintenancePolicy(MaintenancePolicy{SyncMode: "off"}, binding)
	secondPolicy, _ := normalizeMaintenancePolicy(MaintenancePolicy{SyncEnabled: true, SyncMode: "head", Issues: true}, binding)
	source := func(repositoryPath string) repositoryDocsDiskSource {
		return repositoryDocsDiskSource{State: RepositoryDocsMaintenanceState{SourceRegistrationID: sourceID, SourceRegistrationGeneration: 1, GitStoreRef: "git-common-dir", State: "registered"}, RepositoryPath: repositoryPath}
	}
	selectedAdmission := repositoryDocsAdmissionIntent{RegistrationID: registrationID, SourceRegistrationID: sourceID, SourceRegistrationGeneration: 1, AuthorityPathFingerprint: pathFingerprint(maintenanceCanonicalPathKey(firstPath)), RepoID: "owner/repo", WorkKey: "selected-work", ExpectedRevisionSetID: "selected-set", Disposition: repositoryDocsAdmissionPending, CreatedAt: time.Now().Add(-time.Minute).UTC()}
	rejectedAdmission := repositoryDocsAdmissionIntent{RegistrationID: registrationID, SourceRegistrationID: sourceID, SourceRegistrationGeneration: 1, AuthorityPathFingerprint: pathFingerprint(maintenanceCanonicalPathKey(secondPath)), RepoID: "owner/repo", WorkKey: "rejected-work", ExpectedRevisionSetID: "rejected-set", Disposition: repositoryDocsAdmissionCancelled, CreatedAt: time.Now().UTC()}
	legacyAdmission := repositoryDocsAdmissionIntent{RegistrationID: registrationID, SourceRegistrationID: sourceID, SourceRegistrationGeneration: 1, RepoID: "owner/repo", WorkKey: "legacy-work", ExpectedRevisionSetID: "legacy-set", Disposition: repositoryDocsAdmissionPending, CreatedAt: time.Now().Add(time.Minute).UTC()}
	selectedReceipt := maintenanceReceipt{KeyHash: maintenanceIdempotencyKeyHash("selected-receipt"), RegistrationID: registrationID, IntentHash: maintenanceEnrollmentIntentHash(registrationID, firstPolicy, maintenanceHash(firstConfig))}
	rejectedReceipt := maintenanceReceipt{KeyHash: maintenanceIdempotencyKeyHash("rejected-receipt"), RegistrationID: registrationID, IntentHash: maintenanceEnrollmentIntentHash(registrationID, secondPolicy, maintenanceHash(secondConfig))}
	disk := maintenanceRegistryFile{SchemaVersion: legacyMaintenanceRegistrySchema, Entries: []maintenanceDiskEntry{
		{MaintenanceEntry: MaintenanceEntry{RegistrationID: registrationID, CacheUUID: identity.UUID, RepoID: "owner/repo", Policy: firstPolicy, ConfigHash: maintenanceHash(firstConfig), Enabled: true}, CachePath: firstPath, ConfigSnapshot: firstConfig, RepositoryDocsSources: []repositoryDocsDiskSource{source(filepath.Join(dir, "repo-first"))}},
		{MaintenanceEntry: MaintenanceEntry{RegistrationID: registrationID, CacheUUID: identity.UUID, RepoID: "owner/repo", Policy: secondPolicy, ConfigHash: maintenanceHash(secondConfig), Enabled: true}, CachePath: secondPath, ConfigSnapshot: secondConfig, RepositoryDocsSources: []repositoryDocsDiskSource{source(filepath.Join(dir, "repo-second"))}},
	}, Receipts: []maintenanceReceipt{selectedReceipt, rejectedReceipt}, RepositoryDocsAdmissionQueue: []repositoryDocsAdmissionIntent{selectedAdmission, rejectedAdmission, legacyAdmission}}
	registryPath := filepath.Join(dir, "managed-caches.json")
	data, _ := json.Marshal(disk)
	if err := os.WriteFile(registryPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := maintenance.Load(); err != nil {
		t.Fatal(err)
	}
	listed, _ := maintenance.List(ctx)
	conflict := listed.Entries[0]
	var selected MaintenanceIdentityCandidate
	for _, candidate := range conflict.IdentityConflict.Candidates {
		if candidate.PathFingerprint == pathFingerprint(maintenanceCanonicalPathKey(firstPath)) {
			selected = candidate
		}
	}
	req := MaintenanceConflictResolutionRequest{RegistrationID: conflict.RegistrationID, CandidateRef: selected.CandidateRef, ExpectedGeneration: conflict.Generation}
	plan, err := maintenance.PlanConflictResolution(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.PlanID, req.IdempotencyKey = plan.PlanID, "restore-authorities"
	if _, err := maintenance.ApplyConflictResolution(ctx, req); err != nil {
		t.Fatal(err)
	}
	selectedSourceID := maintenance.entries[registrationID].RepositoryDocs.SourceRegistrationID
	current, ok := maintenance.repositoryDocsAdmission(registrationID, selectedSourceID)
	if !ok || current.WorkKey != selectedAdmission.WorkKey || current.ExpectedRevisionSetID != selectedAdmission.ExpectedRevisionSetID {
		t.Fatalf("selected admission=%+v ok=%t", current, ok)
	}
	selectedEnroll := testMaintenanceEnrollRequest(firstPath, "selected-receipt", firstPolicy)
	if _, err := maintenance.Enroll(ctx, selectedEnroll); err != nil {
		t.Fatalf("selected receipt replay=%v receipt=%+v entry=%+v request_hash=%s", err, maintenance.receipts[maintenanceIdempotencyKeyHash("selected-receipt")], maintenance.entries[registrationID], maintenanceHash(selectedEnroll.ConfigSnapshot))
	}
	rejectedEnroll := testMaintenanceEnrollRequest(firstPath, "rejected-receipt", firstPolicy)
	if _, err := maintenance.Enroll(ctx, rejectedEnroll); err == nil {
		t.Fatal("rejected clone receipt authorized selected path")
	}
	restarted := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := restarted.Load(); err != nil {
		t.Fatal(err)
	}
	selectedSourceID = restarted.entries[registrationID].RepositoryDocs.SourceRegistrationID
	current, ok = restarted.repositoryDocsAdmission(registrationID, selectedSourceID)
	if !ok || current.WorkKey != selectedAdmission.WorkKey {
		t.Fatalf("restarted selected admission=%+v ok=%t", current, ok)
	}
	if _, err := restarted.Enroll(ctx, selectedEnroll); err != nil {
		t.Fatalf("restarted selected receipt replay=%v", err)
	}
}

func TestCloneAuthorityRestorePrefersExactSourceOverAliasRepoMatch(t *testing.T) {
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), "")
	oldID, canonicalID, aliasID := "clone-conflict", "canonical-reg", "alias-reg"
	fingerprint := "sha256:selected"
	intent := repositoryDocsAdmissionIntent{RegistrationID: oldID, SourceRegistrationID: "source-alias", SourceRegistrationGeneration: 2, AuthorityPathFingerprint: fingerprint, RepoID: "owner/repo", WorkKey: "work", ExpectedRevisionSetID: "set", Disposition: repositoryDocsAdmissionPending, CreatedAt: time.Now().UTC()}
	maintenance.admissions[repositoryDocsAdmissionKey(oldID, intent.SourceRegistrationID, fingerprint)] = intent
	selected := []maintenanceIdentityConflictCandidate{
		{Entry: MaintenanceEntry{RegistrationID: canonicalID, RepoID: "owner/repo"}, RepositorySources: []repositoryDocsDiskSource{{State: RepositoryDocsMaintenanceState{SourceRegistrationID: "source-canonical", SourceRegistrationGeneration: 2}}}},
		{Entry: MaintenanceEntry{RegistrationID: aliasID, RepoID: "legacy/repo"}, RepositorySources: []repositoryDocsDiskSource{{State: RepositoryDocsMaintenanceState{SourceRegistrationID: "source-alias", SourceRegistrationGeneration: 2}}}},
	}
	bindings := []cache.RepositoryBinding{
		{RepoID: "owner/repo", Aliases: []string{"legacy/repo"}},
		{RepoID: "owner/repo", Aliases: []string{"legacy/repo"}},
	}
	maintenance.restoreSelectedCloneAuthoritiesLocked(oldID, fingerprint, selected, bindings)
	restored, ok := maintenance.admissions[repositoryDocsAdmissionKey(aliasID, intent.SourceRegistrationID, fingerprint)]
	if !ok || restored.RegistrationID != aliasID {
		t.Fatalf("source-specific alias admission=%+v ok=%t admissions=%+v", restored, ok, maintenance.admissions)
	}
}

func TestMaintenanceConflictResolutionBlocksActiveCacheWriter(t *testing.T) {
	ctx := context.Background()
	maintenance, _, _ := newMaintenanceIdentityConflictFixture(t)
	listed, _ := maintenance.List(ctx)
	conflict := listed.Entries[0]
	job, created, err := maintenance.jobs.createCoalescedJob(SyncJobType, conflict.RepoID, "", 0, "active-conflict-writer", conflict.CacheUUID, conflict.RegistrationID, "", func() {})
	if err != nil || !created {
		t.Fatalf("active job=%+v created=%t err=%v", job, created, err)
	}
	req := MaintenanceConflictResolutionRequest{RegistrationID: conflict.RegistrationID, CandidateRef: conflict.IdentityConflict.Candidates[0].CandidateRef, ExpectedGeneration: conflict.Generation}
	if _, err := maintenance.PlanConflictResolution(ctx, req); err == nil {
		t.Fatal("active writer did not block plan")
	} else if coded, ok := err.(interface{ DiagnosticCode() string }); !ok || coded.DiagnosticCode() != "conflict_jobs_active" {
		t.Fatalf("active writer error=%T %v", err, err)
	}
	maintenance.jobs.finishJob(job.ID, JobStatusSucceeded, "done")
	maintenance.jobs.markWorkerFinished(job.ID)
	plan, err := maintenance.PlanConflictResolution(ctx, req)
	if err != nil {
		t.Fatalf("plan after quiescence=%v", err)
	}
	late, created, err := maintenance.jobs.createCoalescedJob(RAGIndexJobType, conflict.RepoID, "profile", 0, "late-conflict-writer", conflict.CacheUUID, conflict.RegistrationID, "namespace", func() {})
	if err != nil || !created {
		t.Fatalf("late job=%+v created=%t err=%v", late, created, err)
	}
	req.PlanID, req.IdempotencyKey = plan.PlanID, "active-writer-apply"
	if _, err := maintenance.ApplyConflictResolution(ctx, req); err == nil {
		t.Fatal("writer admitted after plan did not block apply")
	} else if coded, ok := err.(interface{ DiagnosticCode() string }); !ok || coded.DiagnosticCode() != "conflict_jobs_active" {
		t.Fatalf("late writer apply error=%T %v", err, err)
	}
	if len(maintenance.resolutionReceipts) != 0 {
		t.Fatalf("blocked apply persisted receipts=%+v", maintenance.resolutionReceipts)
	}
	maintenance.jobs.finishJob(late.ID, JobStatusSucceeded, "done")
	maintenance.jobs.markWorkerFinished(late.ID)
	if _, err := maintenance.ApplyConflictResolution(ctx, req); err != nil {
		t.Fatalf("apply after worker quiescence=%v", err)
	}
}

func TestCacheMutationFenceWaitsForCancelledWorkerThatHasNotStarted(t *testing.T) {
	jobs := NewJobManager(filepath.Join(t.TempDir(), "jobs.json"))
	job, created, err := jobs.createCoalescedJob(SyncJobType, "owner/repo", "", 0, "late-start", "cache-1", "reg-1", "", func() {})
	if err != nil || !created {
		t.Fatalf("job=%+v created=%t err=%v", job, created, err)
	}
	jobs.finishJob(job.ID, JobStatusCancelled, "cancelled before scheduler start")
	release, blocked := jobs.BeginCacheMutationFence("cache-1")
	if len(blocked) != 1 || blocked[0] != job.ID {
		t.Fatalf("cancelled late worker was not fenced: %v", blocked)
	}
	release()
	jobs.markWorkerFinished(job.ID)
	release, blocked = jobs.BeginCacheMutationFence("cache-1")
	defer release()
	if len(blocked) != 0 {
		t.Fatalf("quiesced worker still blocked fence: %v", blocked)
	}
}

func TestCacheMutationFenceRejectsRepositoryDocsResume(t *testing.T) {
	jobs := NewJobManager(filepath.Join(t.TempDir(), "jobs.json"))
	job, created, err := jobs.createCoalescedJobWithIntent(RepositoryDocsIndexJobType, "owner/repo", "profile", 0, "docs-work", "cache-1", "reg-1", "namespace", JobRecoveryIntent{
		SourceRegistrationID: "source-1", SourceRegistrationGeneration: 3, ExpectedRevisionSetID: "set-1",
	}, func() {})
	if err != nil || !created {
		t.Fatalf("job=%+v created=%t err=%v", job, created, err)
	}
	jobs.finishJob(job.ID, JobStatusFailed, "retry")
	jobs.markWorkerFinished(job.ID)
	release, blocked := jobs.BeginCacheMutationFence("cache-1")
	if len(blocked) != 0 {
		t.Fatalf("terminal worker unexpectedly blocked initial fence: %v", blocked)
	}
	defer release()
	if _, resumed, err := jobs.ResumeRepositoryDocsAdmission(job.ID, "reg-1", "source-1", 3, "set-1", "docs-work", "", func() {}); err == nil || resumed {
		t.Fatalf("resume crossed fence: resumed=%t err=%v", resumed, err)
	} else if _, ok := err.(CacheMutationFenceError); !ok {
		t.Fatalf("resume fence error=%T %v", err, err)
	}
}

func TestRepositoryDocsResumeSharesAtomicWriterAdmission(t *testing.T) {
	jobs := NewJobManager(filepath.Join(t.TempDir(), "jobs.json"))
	docs, created, err := jobs.createCoalescedJobWithIntent(RepositoryDocsIndexJobType, "owner/repo", "profile", 0, "docs-resume", "cache-1", "reg-1", "namespace", JobRecoveryIntent{
		SourceRegistrationID: "source-1", SourceRegistrationGeneration: 3, ExpectedRevisionSetID: "set-1",
	}, func() {})
	if err != nil || !created {
		t.Fatalf("docs=%+v created=%t err=%v", docs, created, err)
	}
	jobs.finishJob(docs.ID, JobStatusFailed, "retry")
	if _, resumed, err := jobs.ResumeRepositoryDocsAdmission(docs.ID, "reg-1", "source-1", 3, "set-1", "docs-resume", "", func() {}); err == nil || resumed {
		t.Fatalf("resume overlapped unwinding worker: resumed=%t err=%v", resumed, err)
	}
	jobs.markWorkerFinished(docs.ID)
	syncJob, created, err := jobs.createCoalescedJob(SyncJobType, "owner/repo", "", 0, "sync-active", "cache-1", "reg-1", "", func() {})
	if err != nil || !created {
		t.Fatalf("sync=%+v created=%t err=%v", syncJob, created, err)
	}
	resumedJob, resumed, err := jobs.ResumeRepositoryDocsAdmission(docs.ID, "reg-1", "source-1", 3, "set-1", "docs-resume", "", func() {})
	if err != nil || !resumed || resumedJob.ID != docs.ID {
		t.Fatalf("provider-only sync reserved docs writer admission: job=%+v resumed=%t err=%v", resumedJob, resumed, err)
	}
	jobs.finishJob(docs.ID, JobStatusSucceeded, "done")
	jobs.markWorkerFinished(docs.ID)
	jobs.updateJob(docs.ID, func(job *Job, now time.Time) {
		job.Status = JobStatusFailed
		job.Error = "retry"
		job.UpdatedAt = now
		job.FinishedAt = &now
	})
	jobs.finishJob(syncJob.ID, JobStatusSucceeded, "done")
	jobs.markWorkerFinished(syncJob.ID)
	releaseDirect, err := jobs.BeginDirectCacheWriter("cache-1", "admin-binding-test")
	if err != nil {
		t.Fatal(err)
	}
	if _, resumed, err := jobs.ResumeRepositoryDocsAdmission(docs.ID, "reg-1", "source-1", 3, "set-1", "docs-resume", "", func() {}); err == nil || resumed {
		t.Fatalf("resume overlapped direct writer: resumed=%t err=%v", resumed, err)
	}
	releaseDirect()
	resumedJob, resumed, err = jobs.ResumeRepositoryDocsAdmission(docs.ID, "reg-1", "source-1", 3, "set-1", "docs-resume", "", func() {})
	if err != nil || !resumed || resumedJob.ID != docs.ID {
		t.Fatalf("resume after quiescence=%+v resumed=%t err=%v", resumedJob, resumed, err)
	}
	jobs.markWorkerFinished(docs.ID)
}

func TestRepositoryDocsResumePersistsActionIntentCorrelation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	jobs := NewJobManager(path)
	docs, created, err := jobs.createCoalescedJobWithIntent(RepositoryDocsIndexJobType, "owner/repo", "profile", 0, "docs-resume", "cache-1", "reg-1", "namespace", JobRecoveryIntent{
		SourceRegistrationID: "source-1", SourceRegistrationGeneration: 3, ExpectedRevisionSetID: "set-1",
	}, func() {})
	if err != nil || !created {
		t.Fatalf("docs=%+v created=%t err=%v", docs, created, err)
	}
	jobs.finishJob(docs.ID, JobStatusFailed, "retry")
	jobs.markWorkerFinished(docs.ID)
	resumed, ok, err := jobs.ResumeRepositoryDocsAdmission(docs.ID, "reg-1", "source-1", 3, "set-1", "docs-resume", "action-ref", func() {})
	if err != nil || !ok || resumed.Status != JobStatusQueued {
		t.Fatalf("resumed=%+v ok=%t err=%v", resumed, ok, err)
	}

	restarted := NewJobManager(path)
	if err := restarted.LoadAndMarkInterrupted(); err != nil {
		t.Fatal(err)
	}
	correlated, outcome, found := restarted.RetainedRetryIntentResult("action-ref")
	if !found || correlated.ID != docs.ID || correlated.Status != JobStatusInterrupted || outcome != "created" {
		t.Fatalf("correlated=%+v outcome=%q found=%t", correlated, outcome, found)
	}
}

func TestJobRedirectResolutionIsTransitiveAndCycleSafe(t *testing.T) {
	if got := resolveJobRedirect("legacy-a", map[string]string{"legacy-a": "legacy-b", "legacy-b": "canonical"}); got != "canonical" {
		t.Fatalf("transitive redirect=%q", got)
	}
	if got := resolveJobRedirect("legacy-a", map[string]string{"legacy-a": "legacy-b", "legacy-b": "legacy-a"}); got != "legacy-a" {
		t.Fatalf("cycle did not fail closed to original identity: %q", got)
	}
	cleaned := discardCyclicRedirects(map[string]string{"legacy-a": "legacy-b", "legacy-b": "legacy-a", "safe": "canonical"})
	if cleaned["legacy-a"] != "" || cleaned["legacy-b"] != "" || cleaned["safe"] != "canonical" {
		t.Fatalf("cycle cleanup=%v", cleaned)
	}
}

func TestMaintenanceConflictResolutionBindsIdempotencyToRequestedTarget(t *testing.T) {
	ctx := context.Background()
	maintenance, _, _ := newMaintenanceIdentityConflictFixture(t)
	listed, _ := maintenance.List(ctx)
	conflict := listed.Entries[0]
	req := MaintenanceConflictResolutionRequest{RegistrationID: conflict.RegistrationID, CandidateRef: conflict.IdentityConflict.Candidates[0].CandidateRef, ExpectedGeneration: conflict.Generation}
	plan, err := maintenance.PlanConflictResolution(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	req.PlanID, req.IdempotencyKey = plan.PlanID, "target-bound-key"
	if _, err := maintenance.ApplyConflictResolution(ctx, req); err != nil {
		t.Fatal(err)
	}
	req.RegistrationID = "different-registration"
	if _, err := maintenance.ApplyConflictResolution(ctx, req); err == nil {
		t.Fatal("idempotency replay crossed requested target")
	} else if _, ok := err.(MaintenanceIdempotencyConflictError); !ok {
		t.Fatalf("cross-target error=%T %v", err, err)
	}
}

func TestMaintenanceConflictResolutionRejectsConfigSnapshotTamper(t *testing.T) {
	ctx := context.Background()
	maintenance, _, _ := newMaintenanceIdentityConflictFixture(t)
	listed, _ := maintenance.List(ctx)
	conflict := listed.Entries[0]
	selected := conflict.IdentityConflict.Candidates[0]
	maintenance.mu.Lock()
	tampered := &maintenance.conflictCandidates[conflict.RegistrationID][0]
	tampered.ConfigSnapshot.CachePath = filepath.Join(t.TempDir(), "self-consistent-other-cache.db")
	tampered.Entry.ConfigHash = maintenanceHash(tampered.ConfigSnapshot)
	maintenance.mu.Unlock()
	_, err := maintenance.PlanConflictResolution(ctx, MaintenanceConflictResolutionRequest{RegistrationID: conflict.RegistrationID, CandidateRef: selected.CandidateRef, ExpectedGeneration: conflict.Generation})
	if err == nil {
		t.Fatal("tampered config snapshot produced a plan")
	}
	if coded, ok := err.(interface{ DiagnosticCode() string }); !ok || coded.DiagnosticCode() != "conflict_candidate_identity_changed" {
		t.Fatalf("tamper error=%T %v", err, err)
	}
}

func TestMaintenanceConflictResolutionRejectsLossyLegacyConflict(t *testing.T) {
	dir := t.TempDir()
	registryPath := filepath.Join(dir, "managed-caches.json")
	disk := maintenanceRegistryFile{SchemaVersion: maintenanceRegistrySchema, Entries: []maintenanceDiskEntry{{MaintenanceEntry: MaintenanceEntry{RegistrationID: "legacy-conflict", CacheUUID: "cache-uuid", RepoID: "owner/repo", Generation: 3, State: "identity_conflict", IdentityConflict: &MaintenanceIdentityConflict{Kind: "identity_conflict", CandidateRegistrationIDs: []string{"a", "b"}}}}}}
	data, _ := json.Marshal(disk)
	_ = os.WriteFile(registryPath, data, 0o600)
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := maintenance.Load(); err != nil {
		t.Fatal(err)
	}
	_, err := maintenance.PlanConflictResolution(context.Background(), MaintenanceConflictResolutionRequest{RegistrationID: "legacy-conflict", CandidateRef: "candidate", ExpectedGeneration: 3})
	if err == nil {
		t.Fatal("lossy legacy conflict produced a selectable plan")
	}
	if coded, ok := err.(interface{ DiagnosticCode() string }); !ok || coded.DiagnosticCode() != "conflict_details_unavailable" {
		t.Fatalf("lossy conflict error=%T %v", err, err)
	}
}

func TestMaintenanceConflictResolutionPlansAndReceiptsAreDeterministicAndBounded(t *testing.T) {
	ctx := context.Background()
	maintenance, registryPath, _ := newMaintenanceIdentityConflictFixture(t)
	listed, _ := maintenance.List(ctx)
	conflict := listed.Entries[0]
	request := MaintenanceConflictResolutionRequest{RegistrationID: conflict.RegistrationID, CandidateRef: conflict.IdentityConflict.Candidates[0].CandidateRef, ExpectedGeneration: conflict.Generation}
	first, err := maintenance.PlanConflictResolution(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := maintenance.PlanConflictResolution(ctx, request)
	if err != nil || second.PlanID != first.PlanID {
		t.Fatalf("plans first=%+v second=%+v err=%v", first, second, err)
	}
	maintenance.mu.Lock()
	for index := 0; index < maxMaintenanceConflictResolutionReceipts+7; index++ {
		key := maintenanceIdempotencyKeyHash(fmt.Sprintf("bounded-%03d", index))
		maintenance.resolutionReceipts[key] = maintenanceConflictResolutionReceipt{KeyHash: key, ReceiptID: fmt.Sprintf("receipt-%03d", index), AppliedAt: time.Unix(int64(index), 0).UTC()}
	}
	if err := maintenance.saveLocked(); err != nil {
		maintenance.mu.Unlock()
		t.Fatal(err)
	}
	retained := len(maintenance.resolutionReceipts)
	maintenance.mu.Unlock()
	if retained != maxMaintenanceConflictResolutionReceipts {
		t.Fatalf("in-memory receipts=%d", retained)
	}
	data, err := os.ReadFile(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	var disk maintenanceRegistryFile
	if err := json.Unmarshal(data, &disk); err != nil || len(disk.ConflictResolutionReceipts) != maxMaintenanceConflictResolutionReceipts {
		t.Fatalf("durable receipts=%d err=%v", len(disk.ConflictResolutionReceipts), err)
	}
}

func newMaintenanceIdentityConflictFixture(t *testing.T) (*MaintenanceManager, string, string) {
	t.Helper()
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
	disk := maintenanceRegistryFile{SchemaVersion: legacyMaintenanceRegistrySchema, Entries: []maintenanceDiskEntry{
		{MaintenanceEntry: MaintenanceEntry{RegistrationID: canonicalID, CacheUUID: identity.UUID, RepoID: "owner/repo", Policy: MaintenancePolicy{SyncEnabled: true, SyncMode: "head", Issues: true}, ConfigHash: maintenanceHash(cfg), Enabled: true, Generation: 4, ActiveJobs: []string{"job-a"}}, CachePath: cachePath, ConfigReference: "selected-config", ConfigSnapshot: cfg},
		{MaintenanceEntry: MaintenanceEntry{RegistrationID: legacyID, CacheUUID: identity.UUID, RepoID: "legacy/repo", Policy: MaintenancePolicy{SyncMode: "off"}, ConfigHash: maintenanceHash(cfg), Enabled: false, Generation: 2, ActiveJobs: []string{"job-b"}}, CachePath: cachePath, ConfigReference: "other-config", ConfigSnapshot: cfg},
	}}
	data, _ := json.Marshal(disk)
	registryPath := filepath.Join(dir, "managed-caches.json")
	if err := os.WriteFile(registryPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(filepath.Join(dir, "jobs.json")), registryPath)
	if err := maintenance.Load(); err != nil {
		t.Fatal(err)
	}
	return maintenance, registryPath, canonicalID
}
