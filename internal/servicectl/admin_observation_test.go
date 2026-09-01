package servicectl

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gitcode-mcp/internal/adminhttp"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/service"
)

func TestAdminObservationReadsCacheWithoutExposingItsPath(t *testing.T) {
	root, err := shortWorkspaceTemp(t, "admin-observation-")
	if err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(root, "private", "cache.db")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertRepo(ctx, cache.RepositoryBinding{RepoID: "example/repo", Owner: "example", Name: "repo", Scopes: []cache.RepositoryScope{cache.RepositoryScopeIssues}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	manager := newTestManager(t, "darwin")
	manager.AdminCachePath = cachePath
	jobs := NewJobManager("")
	maintenance := NewMaintenanceManager(manager, jobs, filepath.Join(root, "registry.json"))
	snapshot, err := manager.adminObservation(ctx, jobs, maintenance, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), root) || strings.Contains(string(data), "cache.db") {
		t.Fatalf("absolute cache path leaked: %s", data)
	}
	if strings.Contains(string(data), `"schema_recovery"`) || strings.Contains(string(data), "backup_path") {
		t.Fatalf("ordinary ready cache exposed a recovery lifecycle or path: %s", data)
	}
	if len(snapshot.Caches) != 1 || len(snapshot.Caches[0].Repositories) != 1 || snapshot.Caches[0].Repositories[0].RepoID != "example/repo" {
		t.Fatalf("cache topology=%+v", snapshot.Caches)
	}
	if snapshot.Caches[0].SchemaRecovery != nil {
		t.Fatalf("ordinary ready cache must not claim a completed migration: %+v", snapshot.Caches[0].SchemaRecovery)
	}
	documentation := snapshot.Caches[0].Repositories[0].Documentation
	if documentation == nil || documentation.State != "not_indexed" || documentation.IndexHandoff != "" || documentation.SearchAvailable {
		t.Fatalf("repository documentation observation=%+v", documentation)
	}
	if snapshot.JobRetention.SuccessTTLSeconds != int64(config.DefaultJobSuccessTTL.Seconds()) || snapshot.JobRetention.MaxTerminalJobs != config.DefaultJobMaxTerminal {
		t.Fatalf("job retention observation=%+v", snapshot.JobRetention)
	}
}

func TestAdminObservationPublishesSchemaBlockAsStructuredPublicState(t *testing.T) {
	root, err := shortWorkspaceTemp(t, "admin-schema-blocked-")
	if err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(root, "private", "cache.db")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	setMaintenanceTestSchemaVersion(t, cachePath, cache.CurrentSchemaVersion()+1)

	manager := newTestManager(t, "darwin")
	manager.Version = "0.3.0"
	manager.Commit = "compatible-commit"
	manager.AdminCachePath = cachePath
	jobs := NewJobManager("")
	maintenance := NewMaintenanceManager(manager, jobs, filepath.Join(root, "registry.json"))
	snapshot, err := manager.adminObservation(ctx, jobs, maintenance, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Service.Commit != manager.Commit || snapshot.Service.SchemaMin != cache.CurrentSchemaVersion() || snapshot.Service.SchemaMax != cache.CurrentSchemaVersion() {
		t.Fatalf("service compatibility contract=%+v", snapshot.Service)
	}
	if len(snapshot.Caches) != 1 {
		t.Fatalf("cache observations=%+v", snapshot.Caches)
	}
	view := snapshot.Caches[0]
	if view.Readiness != "cache_schema_blocked" || view.SchemaVersion != cache.CurrentSchemaVersion()+1 || view.ExpectedSchemaVersion != cache.CurrentSchemaVersion() || view.DaemonBinaryVersion != manager.Version || view.DaemonBinaryCommit != manager.Commit || view.QuiesceState != "required" {
		t.Fatalf("schema-blocked cache contract=%+v", view)
	}
	if view.SchemaRecovery == nil || view.SchemaRecovery.State != "unsafe_refused" || view.SchemaRecovery.MigrationState != "refused" || view.SchemaRecovery.TargetCompatible || view.SchemaRecovery.DataState != "intact" {
		t.Fatalf("unsafe schema refusal=%+v", view.SchemaRecovery)
	}
	if len(snapshot.Diagnostics) != 1 || snapshot.Diagnostics[0].FailureClass != "cache_schema_blocked" {
		t.Fatalf("schema-blocked diagnostics=%+v", snapshot.Diagnostics)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), root) || strings.Contains(string(data), "cache.db") {
		t.Fatalf("absolute cache path leaked: %s", data)
	}
}

func TestAdminObservationPublishesInterruptedSchemaLifecycleWithoutPaths(t *testing.T) {
	root, err := shortWorkspaceTemp(t, "admin-schema-interrupted-")
	if err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(root, "private", "cache.db")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	intent := `{"schema_version":"gitcode-mcp.cache-migration-recovery.v1","target_schema":` + fmt.Sprint(cache.CurrentSchemaVersion()) + `,"phase":"migration_complete_service_restart_failed","backup_verified":true,"identity_preserved":true}`
	if err := os.WriteFile(cachePath+".migration-recovery.json", []byte(intent), 0o600); err != nil {
		t.Fatal(err)
	}

	manager := newTestManager(t, "darwin")
	manager.Version = "0.4.0"
	manager.Commit = "compatible-target-commit"
	manager.AdminCachePath = cachePath
	jobs := NewJobManager("")
	maintenance := NewMaintenanceManager(manager, jobs, filepath.Join(root, "registry.json"))
	snapshot, err := manager.adminObservation(ctx, jobs, maintenance, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Caches) != 1 {
		t.Fatalf("cache observations=%+v", snapshot.Caches)
	}
	view := snapshot.Caches[0]
	recovery := view.SchemaRecovery
	if recovery == nil || recovery.State != "interrupted_upgrade" || recovery.Phase != "migration_complete_service_restart_failed" || recovery.BackupState != "verified" || recovery.MigrationState != "complete" || recovery.RestartState != "interrupted" || recovery.IdentityState != "preserved" || recovery.TargetBinaryVersion != manager.Version || recovery.TargetBinaryCommit != manager.Commit {
		t.Fatalf("interrupted lifecycle=%+v", recovery)
	}
	if view.CacheRef == "" || identity.UUID == "" {
		t.Fatal("cache identity was not observed")
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), root) || strings.Contains(string(data), "cache.db") || strings.Contains(string(data), ".backup-") {
		t.Fatalf("migration lifecycle leaked a filesystem path: %s", data)
	}
	for _, want := range []string{`"state":"interrupted_upgrade"`, `"backup_state":"verified"`, `"migration_state":"complete"`, `"restart_state":"interrupted"`, `"identity_state":"preserved"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("migration lifecycle JSON missing %s: %s", want, data)
		}
	}
}

func TestAdminObservationPublishesVerifiedCompatibleRestartReceiptWithoutPaths(t *testing.T) {
	root, err := shortWorkspaceTemp(t, "admin-schema-recovered-")
	if err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(root, "private", "cache.db")
	if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(t, "darwin")
	manager.Version = "0.4.0"
	manager.Commit = "compatible-target-commit"
	manager.AdminCachePath = cachePath
	receipt := fmt.Sprintf(`{"schema_version":"gitcode-mcp.cache-migration-receipt.v1","cache_uuid":%q,"target_schema":%d,"phase":"healthy","backup_verified":true,"identity_preserved":true,"target_binary_version":%q,"target_binary_commit":%q,"target_schema_min":%d,"target_schema_max":%d,"completed_at":%q}`,
		identity.UUID, cache.CurrentSchemaVersion(), manager.Version, manager.Commit, cache.CurrentSchemaVersion(), cache.CurrentSchemaVersion(), time.Now().UTC().Format(time.RFC3339Nano))
	if err := os.WriteFile(cachePath+".migration-receipt.json", []byte(receipt), 0o600); err != nil {
		t.Fatal(err)
	}
	jobs := NewJobManager("")
	maintenance := NewMaintenanceManager(manager, jobs, filepath.Join(root, "registry.json"))
	snapshot, err := manager.adminObservation(ctx, jobs, maintenance, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Caches) != 1 {
		t.Fatalf("cache observations=%+v", snapshot.Caches)
	}
	recovery := snapshot.Caches[0].SchemaRecovery
	if recovery == nil || recovery.State != "compatible_restart" || recovery.Phase != "healthy" || recovery.BackupState != "verified" || recovery.MigrationState != "complete" || recovery.RestartState != "compatible" || recovery.DataState != "available" || recovery.IdentityState != "preserved" || recovery.TargetBinaryVersion != manager.Version || recovery.TargetBinaryCommit != manager.Commit || !recovery.TargetCompatible {
		t.Fatalf("compatible restart lifecycle=%+v", recovery)
	}
	if identity.UUID == "" || snapshot.Caches[0].CacheRef == "" {
		t.Fatal("cache identity was not preserved")
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), root) || strings.Contains(string(data), "cache.db") || strings.Contains(string(data), ".backup-") {
		t.Fatalf("compatible restart leaked a filesystem path: %s", data)
	}
}

func TestAdminObservationRejectsStaleOrUnverifiedRecoveryReceipt(t *testing.T) {
	tests := []struct {
		name              string
		staleUUID         bool
		backupVerified    bool
		identityPreserved bool
		wantPhase         string
	}{
		{name: "stale cache UUID", staleUUID: true, backupVerified: true, identityPreserved: true, wantPhase: "recovery_receipt_verification_failed"},
		{name: "backup not verified", backupVerified: false, identityPreserved: true, wantPhase: "recovery_receipt_invalid"},
		{name: "identity not preserved", backupVerified: true, identityPreserved: false, wantPhase: "recovery_receipt_invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, err := shortWorkspaceTemp(t, "admin-schema-receipt-negative-")
			if err != nil {
				t.Fatal(err)
			}
			cachePath := filepath.Join(root, "private", "cache.db")
			if err := os.MkdirAll(filepath.Dir(cachePath), 0o700); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			store, err := cache.NewSQLiteStore(ctx, cachePath)
			if err != nil {
				t.Fatal(err)
			}
			identity, err := store.CacheIdentity(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			receiptUUID := identity.UUID
			if tt.staleUUID {
				receiptUUID = "stale-replaced-cache-uuid"
			}
			manager := newTestManager(t, "darwin")
			manager.Version = "0.4.0"
			manager.Commit = "compatible-target-commit"
			manager.AdminCachePath = cachePath
			receipt := fmt.Sprintf(`{"schema_version":"gitcode-mcp.cache-migration-receipt.v1","cache_uuid":%q,"target_schema":%d,"phase":"healthy","backup_verified":%t,"identity_preserved":%t,"target_binary_version":%q,"target_binary_commit":%q,"target_schema_min":%d,"target_schema_max":%d,"completed_at":%q}`,
				receiptUUID, cache.CurrentSchemaVersion(), tt.backupVerified, tt.identityPreserved, manager.Version, manager.Commit, cache.CurrentSchemaVersion(), cache.CurrentSchemaVersion(), time.Now().UTC().Format(time.RFC3339Nano))
			if err := os.WriteFile(cachePath+".migration-receipt.json", []byte(receipt), 0o600); err != nil {
				t.Fatal(err)
			}
			jobs := NewJobManager("")
			maintenance := NewMaintenanceManager(manager, jobs, filepath.Join(root, "registry.json"))
			snapshot, err := manager.adminObservation(ctx, jobs, maintenance, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Caches) != 1 || snapshot.Caches[0].SchemaRecovery == nil {
				t.Fatalf("missing typed receipt refusal: %+v", snapshot.Caches)
			}
			recovery := snapshot.Caches[0].SchemaRecovery
			if recovery.State != "interrupted_upgrade" || recovery.Phase != tt.wantPhase || recovery.RestartState != "verification_required" || recovery.State == "compatible_restart" {
				t.Fatalf("receipt was not rejected: %+v", recovery)
			}
			data, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(data), receiptUUID) || strings.Contains(string(data), identity.UUID) || strings.Contains(string(data), root) || strings.Contains(string(data), "cache.db") {
				t.Fatalf("receipt refusal leaked private identity or path: %s", data)
			}
		})
	}
}

func TestAdminObservationKeepsCurrentRAGSeparateFromLastErrorAndRetry(t *testing.T) {
	now := time.Now().UTC()
	entry := MaintenanceEntry{
		CacheUUID: "11111111-2222-3333-4444-555555555555", RepoID: "example/repo",
		Policy: MaintenancePolicy{RAGEnabled: true}, ContentGeneration: 8, CoveredGeneration: 8, RAGStatus: "ready",
		RAGStage: MaintenanceStageState{
			Status: "retry_scheduled", LastErrorClass: "cache_busy", LastError: "/private/cache.db is locked",
			RetryAfter: now.Add(time.Minute), UpdatedAt: now,
		},
	}
	coverage := coverageFromEntry(entry, adminhttp.SecondaryCount{Complete: 3, Total: 3}, 20, 20)
	execution := executionFromEntry(entry, nil)
	if coverage.RAG.State != "current" || coverage.RAG.Status != "ready" {
		t.Fatalf("current RAG truth was hidden by stage history: %+v", coverage.RAG)
	}
	if execution.Contention == nil || execution.ScheduledRetry == nil || len(execution.LastErrors) != 1 {
		t.Fatalf("execution context lost contention/retry/error: %+v", execution)
	}
	if strings.Contains(execution.LastErrors[0].Message, "/private/") {
		t.Fatalf("private path leaked in stage message: %q", execution.LastErrors[0].Message)
	}
}

func TestAdminObservationUsesOpaqueCacheReferences(t *testing.T) {
	path := "/Users/example/private/cache.db"
	ref := publicCacheRef("", path)
	if !strings.HasPrefix(ref, "cache-") || strings.Contains(ref, "Users") || strings.Contains(ref, "cache.db") {
		t.Fatalf("cache ref is not opaque: %q", ref)
	}
	if got := publicCacheRef("11111111-2222-3333-4444-555555555555", path); got != "cache-111111112222" {
		t.Fatalf("uuid cache ref=%q", got)
	}
	if got := publicCacheRef("", ""); got != "" {
		t.Fatalf("empty cache identity=%q", got)
	}
}

func TestAdminMaintenanceObservationExposesCanonicalAliasesAndSanitizedConflict(t *testing.T) {
	entry := MaintenanceEntry{
		RegistrationID: "cache-reg-canonical", CacheUUID: "11111111-2222-3333-4444-555555555555", RepoID: "owner/repo",
		Aliases: []string{"legacy/repo"}, LegacyRegistrationIDs: []string{"cache-reg-legacy"}, State: "identity_conflict",
		IdentityConflict: &MaintenanceIdentityConflict{
			DetailsAvailable:         true,
			CandidateRegistrationIDs: []string{"cache-reg-canonical", "cache-reg-legacy"},
			PolicyHashes:             []string{"sha256:policy-a", "sha256:policy-b"}, ConfigHashes: []string{"sha256:config"},
			Candidates: []MaintenanceIdentityCandidate{
				{CandidateRef: "candidate-a", SelectionKind: "physical_cache_authority", RegistrationID: "cache-reg-canonical", RepoID: "owner/repo", Policy: MaintenancePolicy{SyncEnabled: true, Issues: true}, PolicyHash: "sha256:policy-a", ConfigHash: "sha256:config", PathFingerprint: "sha256:path-a", WasEnabled: true, CohortRegistrationIDs: []string{"cache-reg-canonical", "cache-reg-member"}, CohortRepoIDs: []string{"owner/repo", "owner/member"}, Members: []MaintenanceIdentityCandidate{{CandidateRef: "member-a", RegistrationID: "cache-reg-member", RepoID: "owner/member", Policy: MaintenancePolicy{SyncMode: "off"}, PolicyHash: "sha256:member", PathFingerprint: "sha256:path-a"}}},
				{CandidateRef: "candidate-b", RegistrationID: "cache-reg-legacy", RepoID: "legacy/repo", Policy: MaintenancePolicy{SyncMode: "off"}, PolicyHash: "sha256:policy-b", ConfigHash: "sha256:config", PathFingerprint: "sha256:path-b"},
			},
		},
	}
	view := adminMaintenanceObservation(entry)
	data, _ := json.Marshal(view)
	if len(view.Aliases) != 1 || view.Aliases[0] != "legacy/repo" || len(view.LegacyRegistrationIDs) != 1 || view.IdentityConflict == nil || !view.IdentityConflict.DetailsAvailable || len(view.IdentityConflict.Candidates) != 2 || view.IdentityConflict.Candidates[0].CandidateRef != "candidate-a" {
		t.Fatalf("view=%+v", view)
	}
	clone := view.IdentityConflict.Candidates[0]
	if clone.SelectionKind != "physical_cache_authority" || len(clone.CohortRegistrationIDs) != 2 || len(clone.CohortRepoIDs) != 2 || len(clone.Members) != 1 || clone.Members[0].RepoID != "owner/member" {
		t.Fatalf("clone cohort DTO lost recursive authority data: %+v", clone)
	}
	if strings.Contains(string(data), "cache_path") || strings.Contains(string(data), "config_snapshot") {
		t.Fatalf("private migration state leaked: %s", data)
	}
}

func TestAdminJobObservationDropsRawProgressMessagesAndEndpoints(t *testing.T) {
	job := Job{ID: "job-000001", Type: "sync", RegistrationID: "reg-1", Status: JobStatusFailed, Error: "/private/cache.db failed", ErrorClass: "cache_busy"}
	job.Progress = append(job.Progress, service.ProgressEvent{Type: "page", Endpoint: "/private/api", Message: "raw log"}, service.ProgressEvent{Type: "failed", Collection: "wiki", RecordsFailed: 1, RetryAfter: "30s"})
	view := adminJobObservation(job)
	if strings.Contains(view.FailureMessage, "/private/") || len(view.Progress) != 2 || view.FailureCollection != "wiki" || view.RetryAfter != "30s" || view.InspectCommand != "gitcode-mcp service job job-000001 --format json" || view.RemediationCommand != "gitcode-mcp service maintenance --format json" {
		t.Fatalf("sanitized job=%+v", view)
	}
}

func TestAdminRepositoryDocsJobIsCancellableButNotGenericallyRetryable(t *testing.T) {
	now := time.Now().UTC()
	view := adminJobObservation(Job{ID: "job-repo-docs", Type: RepositoryDocsIndexJobType, RegistrationID: "reg-1", Status: JobStatusRunning, CreatedAt: now, UpdatedAt: now})
	if !view.Cancellable || view.Retryable {
		t.Fatalf("view=%+v", view)
	}
}

func TestAdminRepositoryDocsObservationExposesOpaqueAuthoritiesAndRetention(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	entry := &MaintenanceEntry{
		RegistrationID: "reg-docs",
		RepositoryDocsSources: []RepositoryDocsMaintenanceState{
			{SourceRegistrationID: "source-b", SourceRegistrationGeneration: 2, State: "registered", GitStoreRef: "git-store-b", WorktreeRef: "worktree-b"},
			{SourceRegistrationID: "source-a", SourceRegistrationGeneration: 4, State: "ready", GitStoreRef: "git-store-a", WorktreeRef: "worktree-a"},
		},
	}
	view := repositoryDocumentationObservation(ctx, store, "owner/repo", entry, 123456)
	if len(view.Sources) != 2 || view.Sources[0].SourceID != "source-a" || view.Sources[1].SourceID != "source-b" {
		t.Fatalf("sources=%+v", view.Sources)
	}
	if view.Retention.CommittedSetsPerIdentity != 8 || view.Retention.OverlayMaxAgeHours != 24 || view.Retention.TerminalMaxAgeHours != 168 || view.Retention.VectorByteCeiling != 123456 {
		t.Fatalf("retention=%+v", view.Retention)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	if bytes := string(encoded); strings.Contains(bytes, "/Users/") || strings.Contains(bytes, "repository_path") {
		t.Fatalf("private authority leaked: %s", bytes)
	}
}

func TestAdminRepositoryDocsObservationEnablesFullTextWithoutSemanticSet(t *testing.T) {
	ctx := context.Background()
	store, err := cache.NewSQLiteStore(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	state := RepositoryDocsMaintenanceState{
		SourceRegistrationID: "source-local", SourceRegistrationGeneration: 3,
		State: "registered", GitStoreRef: "git-store-local", CommitOID: "0123456789abcdef0123456789abcdef01234567",
	}
	entry := &MaintenanceEntry{
		RegistrationID: "reg-docs", RepositoryDocs: &state,
		RepositoryDocsSources: []RepositoryDocsMaintenanceState{state},
	}

	view := repositoryDocumentationObservation(ctx, store, "owner/repo", entry, 123456)
	if !view.Registered || !view.SearchAvailable || view.SemanticAvailable || view.State != "not_indexed" {
		t.Fatalf("registered lexical-only observation=%+v", view)
	}
	if view.SearchHandoff == "" || view.IndexHandoff == "" {
		t.Fatalf("registered handoffs=%+v", view)
	}
}

func TestAdminCoverageNeverPromotesBoundedTailToComplete(t *testing.T) {
	now := time.Now().UTC()
	entry := MaintenanceEntry{
		Policy: MaintenancePolicy{Issues: true, Wiki: true},
		Frontiers: []cache.MaintenanceFrontier{
			{RemoteType: "issue", Lane: "tail", Status: "complete", UpdatedAt: now},
			{RemoteType: "wiki", Lane: "tail", Status: "backfilling", StopReason: "max_pages", PagesListed: 3, UpdatedAt: now},
		},
	}
	coverage := coverageFromEntry(entry, adminhttp.SecondaryCount{}, 0, 0)
	if coverage.Tail.State != "partial" || coverage.Tail.Status == "complete" || coverage.Tail.StopReason != "max_pages" {
		t.Fatalf("bounded tail was promoted: %+v", coverage.Tail)
	}
}

func TestAdminCoverageRequiresEveryConfiguredCollectionForCurrentHead(t *testing.T) {
	now := time.Now().UTC()
	entry := MaintenanceEntry{
		Policy:    MaintenancePolicy{Issues: true, Wiki: true},
		Frontiers: []cache.MaintenanceFrontier{{RemoteType: "issue", Lane: "head", Status: "fresh", UpdatedAt: now}},
	}
	coverage := coverageFromEntry(entry, adminhttp.SecondaryCount{}, 0, 0)
	if coverage.Head.State != "partial" || coverage.Head.Status != "not_observed_for_all_collections" {
		t.Fatalf("missing collection head evidence=%+v", coverage.Head)
	}
}
