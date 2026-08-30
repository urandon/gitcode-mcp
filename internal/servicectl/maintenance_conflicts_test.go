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
	policy := MaintenancePolicy{SyncMode: "off"}
	disk := maintenanceRegistryFile{SchemaVersion: legacyMaintenanceRegistrySchema, Entries: []maintenanceDiskEntry{
		{MaintenanceEntry: MaintenanceEntry{RegistrationID: canonicalID, CacheUUID: identity.UUID, RepoID: "owner/repo", Policy: policy, ConfigHash: maintenanceHash(cfg), Enabled: true}, CachePath: firstPath, ConfigSnapshot: cfg},
		{MaintenanceEntry: MaintenanceEntry{RegistrationID: legacyID, CacheUUID: identity.UUID, RepoID: "legacy/repo", Policy: policy, ConfigHash: maintenanceHash(cfg), Enabled: true}, CachePath: secondPath, ConfigSnapshot: cfg},
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
	cfg := config.Default()
	cfg.CachePath = firstPath
	firstID := maintenanceRegistrationID(identity.UUID, "owner/first")
	secondID := maintenanceRegistrationID(identity.UUID, "owner/second")
	entries := []maintenanceDiskEntry{}
	for _, cachePath := range []string{firstPath, secondPath} {
		entries = append(entries,
			maintenanceDiskEntry{MaintenanceEntry: MaintenanceEntry{RegistrationID: firstID, CacheUUID: identity.UUID, RepoID: "owner/first", Policy: MaintenancePolicy{SyncEnabled: true, SyncMode: "head", Issues: true}, ConfigHash: maintenanceHash(cfg), Enabled: true}, CachePath: cachePath, ConfigSnapshot: cfg},
			maintenanceDiskEntry{MaintenanceEntry: MaintenanceEntry{RegistrationID: secondID, CacheUUID: identity.UUID, RepoID: "owner/second", Policy: MaintenancePolicy{SyncMode: "off"}, ConfigHash: maintenanceHash(cfg), Enabled: false}, CachePath: cachePath, ConfigSnapshot: cfg},
		)
	}
	data, _ := json.Marshal(maintenanceRegistryFile{SchemaVersion: legacyMaintenanceRegistrySchema, Entries: entries})
	registryPath := filepath.Join(dir, "managed-caches.json")
	_ = os.WriteFile(registryPath, data, 0o600)
	maintenance := NewMaintenanceManager(newTestManager(t, "darwin"), NewJobManager(""), registryPath)
	if err := maintenance.Load(); err != nil {
		t.Fatal(err)
	}
	listed, _ := maintenance.List(ctx)
	if len(listed.Entries) != 1 || listed.Entries[0].IdentityConflict == nil {
		t.Fatalf("clone cohort=%+v", listed.Entries)
	}
	conflict := listed.Entries[0]
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
	if _, err := maintenance.ApplyConflictResolution(ctx, req); err != nil {
		t.Fatalf("apply after worker quiescence=%v", err)
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
	maintenance.conflictCandidates[conflict.RegistrationID][0].ConfigSnapshot.CachePath = "tampered.db"
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
