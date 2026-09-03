package servicectl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gitcode-mcp/internal/adminhttp"
	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/capability"
)

type adminMaintenanceEntry struct {
	entry MaintenanceEntry
	path  string
}

func (m *MaintenanceManager) adminEntries() []adminMaintenanceEntry {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	entries := make([]adminMaintenanceEntry, 0, len(m.entries))
	for _, stored := range m.entries {
		entry := cloneMaintenanceEntry(stored)
		entries = append(entries, adminMaintenanceEntry{entry: entry, path: stored.cachePath})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].entry.RegistrationID < entries[j].entry.RegistrationID })
	return entries
}

func (m Manager) adminObservation(ctx context.Context, jobs *JobManager, maintenance *MaintenanceManager, startedAt time.Time) (adminhttp.ObservationSnapshot, error) {
	now := time.Now().UTC()
	schemaMin, schemaMax := m.schemaRange()
	snapshot := adminhttp.ObservationSnapshot{Service: adminhttp.ServiceObservation{
		Version: m.Version, Commit: m.Commit, SchemaMin: schemaMin, SchemaMax: schemaMax, Protocol: "admin.v1", Running: true, StartedAt: adminTimePointer(startedAt), AdminSecure: true,
	}}
	if status, err := m.Status(); err == nil {
		snapshot.Service.Installed = status.Installed
		snapshot.Service.InstallKind = status.InstallKind
	}
	entries := maintenance.adminEntries()
	jobList := jobs.List()
	vectorByteCeiling, ceilingErr := repositoryDocsVectorByteCeiling(m)
	if ceilingErr != nil {
		// Observation remains available for remediation even when the optional
		// machine-local override is malformed. Never report a misleading zero
		// byte policy that the indexing path would reject.
		vectorByteCeiling = DefaultRepositoryDocsVectorBytes
	}
	retention := jobs.RetentionSnapshot()
	snapshot.JobRetention = adminhttp.JobRetentionObservation{
		SuccessTTLSeconds: int64(retention.Policy.SuccessTTL.Seconds()), DiagnosticTTLSeconds: int64(retention.Policy.DiagnosticTTL.Seconds()),
		MaxTerminalJobs: retention.Policy.MaxTerminalJobs, MaxDiagnosticJobs: retention.Policy.MaxDiagnosticJobs,
		MaxProgressEvents: retention.Policy.MaxProgressEvents, Active: retention.Active, Terminal: retention.Terminal,
		OldestRetainedAt: retention.OldestRetained, LastPrunedAt: retention.LastPrunedAt,
		ExpiredTotal: retention.ExpiredTotal, TruncatedTotal: retention.TruncatedTotal,
		LastExpired: retention.LastExpired, LastTruncated: retention.LastTruncated,
	}
	for status, count := range retention.ByStatus {
		snapshot.JobRetention.RetainedByStatus = append(snapshot.JobRetention.RetainedByStatus, adminhttp.JobStatusCount{Status: status, Count: count})
	}
	cacheGroups := groupAdminCaches(m.AdminCachePath, entries)
	for _, group := range cacheGroups {
		cacheView, diagnostics := buildAdminCache(ctx, group, entries, jobList, vectorByteCeiling, m.Version, m.Commit, schemaMin, schemaMax)
		snapshot.Caches = append(snapshot.Caches, cacheView)
		snapshot.Diagnostics = append(snapshot.Diagnostics, diagnostics...)
	}
	for _, job := range jobList {
		snapshot.Jobs = append(snapshot.Jobs, adminJobObservation(job))
	}
	for _, item := range entries {
		snapshot.Maintenance = append(snapshot.Maintenance, adminMaintenanceObservation(item.entry))
		snapshot.Diagnostics = append(snapshot.Diagnostics, adminStageDiagnostics(item.entry)...)
	}
	for _, cap := range capability.Capabilities() {
		snapshot.Capabilities = append(snapshot.Capabilities, adminCapabilityObservation(cap))
	}
	for _, diagnostic := range snapshot.Diagnostics {
		if !diagnostic.Current || diagnostic.Severity == "info" {
			continue
		}
		snapshot.Attention = append(snapshot.Attention, adminhttp.AttentionItem{
			ID: diagnostic.ID, Severity: diagnostic.Severity, EntityType: diagnostic.EntityType,
			EntityID: diagnostic.EntityID, Code: diagnostic.FailureClass, Message: diagnostic.Message,
			Remediation: diagnostic.Remediation,
		})
	}
	return adminhttp.FinalizeSnapshot(snapshot, now), nil
}

type adminCacheGroup struct {
	path        string
	uuid        string
	fingerprint string
}

type adminCacheMigrationRecovery struct {
	SchemaVersion     string `json:"schema_version"`
	TargetSchema      int    `json:"target_schema"`
	Phase             string `json:"phase"`
	BackupVerified    bool   `json:"backup_verified"`
	IdentityPreserved bool   `json:"identity_preserved"`
}

const adminCacheMigrationRecoverySchema = "gitcode-mcp.cache-migration-recovery.v1"

type adminCacheMigrationReceipt struct {
	SchemaVersion       string    `json:"schema_version"`
	CacheUUID           string    `json:"cache_uuid"`
	TargetSchema        int       `json:"target_schema"`
	Phase               string    `json:"phase"`
	BackupVerified      bool      `json:"backup_verified"`
	IdentityPreserved   bool      `json:"identity_preserved"`
	TargetBinaryVersion string    `json:"target_binary_version"`
	TargetBinaryCommit  string    `json:"target_binary_commit"`
	TargetSchemaMin     int       `json:"target_schema_min"`
	TargetSchemaMax     int       `json:"target_schema_max"`
	CompletedAt         time.Time `json:"completed_at"`
}

const adminCacheMigrationReceiptSchema = "gitcode-mcp.cache-migration-receipt.v1"

func readAdminCacheMigrationRecovery(cachePath string) (adminCacheMigrationRecovery, bool, error) {
	data, err := os.ReadFile(cachePath + ".migration-recovery.json")
	if errors.Is(err, os.ErrNotExist) {
		return adminCacheMigrationRecovery{}, false, nil
	}
	if err != nil {
		return adminCacheMigrationRecovery{}, true, err
	}
	var recovery adminCacheMigrationRecovery
	if err := json.Unmarshal(data, &recovery); err != nil {
		return adminCacheMigrationRecovery{}, true, err
	}
	if recovery.SchemaVersion != adminCacheMigrationRecoverySchema || recovery.TargetSchema <= 0 {
		return adminCacheMigrationRecovery{}, true, errors.New("unsupported cache migration recovery intent")
	}
	return recovery, true, nil
}

func readAdminCacheMigrationReceipt(cachePath string) (adminCacheMigrationReceipt, bool, error) {
	data, err := os.ReadFile(cachePath + ".migration-receipt.json")
	if errors.Is(err, os.ErrNotExist) {
		return adminCacheMigrationReceipt{}, false, nil
	}
	if err != nil {
		return adminCacheMigrationReceipt{}, true, err
	}
	var receipt adminCacheMigrationReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return adminCacheMigrationReceipt{}, true, err
	}
	if receipt.SchemaVersion != adminCacheMigrationReceiptSchema || strings.TrimSpace(receipt.CacheUUID) == "" || receipt.TargetSchema <= 0 || receipt.Phase != "healthy" || !receipt.BackupVerified || !receipt.IdentityPreserved || strings.TrimSpace(receipt.TargetBinaryVersion) == "" || receipt.TargetSchemaMin <= 0 || receipt.TargetSchemaMax < receipt.TargetSchemaMin || receipt.TargetSchema < receipt.TargetSchemaMin || receipt.TargetSchema > receipt.TargetSchemaMax || receipt.CompletedAt.IsZero() {
		return adminCacheMigrationReceipt{}, true, errors.New("unsupported cache migration receipt")
	}
	return receipt, true, nil
}

func adminSchemaRecovery(binaryVersion, binaryCommit string, schemaMin, schemaMax, detected, expected int, recovery adminCacheMigrationRecovery, pending bool, recoveryErr error, receipt adminCacheMigrationReceipt, receiptPresent bool, receiptErr error, liveCacheUUID string) *adminhttp.SchemaRecoveryObservation {
	target := expected
	if recovery.TargetSchema > 0 {
		target = recovery.TargetSchema
	}
	view := &adminhttp.SchemaRecoveryObservation{
		State:               "migration_required",
		Phase:               "awaiting_confirmation",
		TargetSchemaVersion: target,
		TargetBinaryVersion: binaryVersion,
		TargetBinaryCommit:  binaryCommit,
		TargetSchemaMin:     schemaMin,
		TargetSchemaMax:     schemaMax,
		TargetCompatible:    target >= schemaMin && target <= schemaMax,
		BackupState:         "pending",
		MigrationState:      "pending",
		RestartState:        "pending",
		DataState:           "intact",
		IdentityState:       "retained",
		Remediation:         "Run gitcode-mcp migrate-cache --confirm with the compatible binary.",
	}
	if detected > target {
		view.State = "unsafe_refused"
		view.Phase = "refused"
		view.TargetCompatible = false
		view.BackupState = "not_started"
		view.MigrationState = "refused"
		view.RestartState = "not_started"
		view.Remediation = "Install a binary whose published schema range includes the detected cache schema; downgrade migration is refused."
		return view
	}
	if recoveryErr != nil {
		view.State = "interrupted_upgrade"
		view.Phase = "recovery_intent_unreadable"
		view.RestartState = "blocked"
		view.Remediation = "Inspect service diagnostics, then resume the confirmed cache migration handoff."
		return view
	}
	if pending {
		view.State = "interrupted_upgrade"
		view.Phase = recovery.Phase
		view.BackupState = "pending"
		view.MigrationState = "pending"
		view.RestartState = "pending"
		if recovery.BackupVerified {
			view.BackupState = "verified"
		}
		if recovery.IdentityPreserved {
			view.IdentityState = "preserved"
		}
		switch recovery.Phase {
		case "migration_committed", "migration_complete_service_install_failed", "migration_complete_service_restart_failed", "migration_complete_service_health_failed", "migration_complete_recovery_intent_failed", "compatible_service_installed":
			view.MigrationState = "complete"
		}
		switch recovery.Phase {
		case "migration_complete_service_install_failed", "migration_complete_service_restart_failed", "migration_complete_service_health_failed", "migration_complete_recovery_intent_failed":
			view.RestartState = "interrupted"
		}
		return view
	}
	if receiptErr != nil {
		view.State = "interrupted_upgrade"
		view.Phase = "recovery_receipt_invalid"
		view.BackupState = "unknown"
		view.MigrationState = "unknown"
		view.RestartState = "verification_required"
		view.Remediation = "Inspect service diagnostics before allowing cache writers to resume."
		return view
	}
	if receiptPresent && receipt.CacheUUID == liveCacheUUID && receipt.TargetSchema == detected && detected == expected && receipt.TargetBinaryVersion == binaryVersion && receipt.TargetBinaryCommit == binaryCommit && receipt.TargetSchemaMin == schemaMin && receipt.TargetSchemaMax == schemaMax && detected >= receipt.TargetSchemaMin && detected <= receipt.TargetSchemaMax {
		view.State = "compatible_restart"
		view.Phase = "healthy"
		view.TargetSchemaVersion = receipt.TargetSchema
		view.TargetBinaryVersion = receipt.TargetBinaryVersion
		view.TargetBinaryCommit = receipt.TargetBinaryCommit
		view.TargetSchemaMin = receipt.TargetSchemaMin
		view.TargetSchemaMax = receipt.TargetSchemaMax
		view.TargetCompatible = true
		view.BackupState = "verified"
		view.MigrationState = "complete"
		view.RestartState = "compatible"
		view.DataState = "available"
		view.IdentityState = "preserved"
		view.Remediation = ""
		return view
	}
	if receiptPresent {
		view.State = "interrupted_upgrade"
		view.Phase = "recovery_receipt_verification_failed"
		view.BackupState = "verified"
		view.MigrationState = "complete"
		view.RestartState = "verification_required"
		view.DataState = "available"
		view.IdentityState = "mismatch"
		view.Remediation = "The recovery receipt does not match the live cache or daemon identity; inspect service diagnostics before resuming writers."
		return view
	}
	return nil
}

func groupAdminCaches(primaryPath string, entries []adminMaintenanceEntry) []adminCacheGroup {
	byPath := map[string]adminCacheGroup{}
	for _, item := range entries {
		path := canonicalAdminCachePath(item.path)
		if path == "" {
			continue
		}
		byPath[path] = adminCacheGroup{path: path, uuid: item.entry.CacheUUID, fingerprint: item.entry.PathFingerprint}
	}
	primaryPath = canonicalAdminCachePath(primaryPath)
	if primaryPath != "" && primaryPath != ":memory:" {
		if _, ok := byPath[primaryPath]; !ok {
			byPath[primaryPath] = adminCacheGroup{path: primaryPath, fingerprint: pathFingerprint(primaryPath)}
		}
	}
	groups := make([]adminCacheGroup, 0, len(byPath))
	for _, group := range byPath {
		groups = append(groups, group)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].fingerprint < groups[j].fingerprint })
	return groups
}

func canonicalAdminCachePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || path == ":memory:" {
		return path
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return path
}

func buildAdminCache(ctx context.Context, group adminCacheGroup, entries []adminMaintenanceEntry, jobs []Job, vectorByteCeiling int64, binaryVersion, binaryCommit string, schemaMin, schemaMax int) (adminhttp.CacheObservation, []adminhttp.DiagnosticObservation) {
	view := adminhttp.CacheObservation{
		CacheRef: publicCacheRef(group.uuid, group.path), PathFingerprint: group.fingerprint,
		StorageMode: "managed", Readiness: "unavailable", ExpectedSchemaVersion: cache.CurrentSchemaVersion(),
		DaemonBinaryVersion: binaryVersion, DaemonBinaryCommit: binaryCommit,
	}
	recovery, recoveryPending, recoveryErr := readAdminCacheMigrationRecovery(group.path)
	receipt, receiptPresent, receiptErr := readAdminCacheMigrationReceipt(group.path)
	var diagnostics []adminhttp.DiagnosticObservation
	store, err := cache.NewSQLiteReadOnlyStore(ctx, group.path)
	if err != nil {
		var schemaErr *cache.SchemaVersionError
		if errors.As(err, &schemaErr) {
			view.Readiness = "cache_schema_blocked"
			view.SchemaVersion = schemaErr.Compat.DetectedVersion
			view.ExpectedSchemaVersion = schemaErr.Compat.ExpectedVersion
			view.QuiesceState = "required"
			view.SchemaRecovery = adminSchemaRecovery(binaryVersion, binaryCommit, schemaMin, schemaMax, view.SchemaVersion, view.ExpectedSchemaVersion, recovery, recoveryPending, recoveryErr, receipt, receiptPresent, receiptErr, group.uuid)
			return view, []adminhttp.DiagnosticObservation{{
				ID: "cache-schema-" + view.CacheRef, Severity: "error", EntityType: "cache", EntityID: view.CacheRef,
				FailureClass: "cache_schema_blocked", Message: "The managed cache schema requires a compatible service binary before writers can resume.", Current: true,
				ObservedAt: adminTimePointer(time.Now().UTC()), Remediation: "Quiesce the service and run the confirmed cache migration workflow.",
			}}
		}
		return view, []adminhttp.DiagnosticObservation{{
			ID: "cache-unreadable-" + view.CacheRef, Severity: "error", EntityType: "cache", EntityID: view.CacheRef,
			FailureClass: "cache_unreadable", Message: "The managed cache cannot be opened read-only.", Current: true,
			ObservedAt: adminTimePointer(time.Now().UTC()), Remediation: "Run gitcode-mcp service doctor for this cache.",
		}}
	}
	defer store.Close()
	if identity, identityErr := store.CacheIdentity(ctx); identityErr == nil && identity.UUID != "" {
		group.uuid = identity.UUID
		view.CacheRef = publicCacheRef(identity.UUID, group.path)
		view.SchemaRecovery = adminSchemaRecovery(binaryVersion, binaryCommit, schemaMin, schemaMax, cache.CurrentSchemaVersion(), cache.CurrentSchemaVersion(), recovery, recoveryPending, recoveryErr, receipt, receiptPresent, receiptErr, identity.UUID)
	}
	view.Readiness = "ready"
	view.SchemaVersion, err = store.SchemaVersion(ctx)
	if err != nil || view.SchemaVersion != cache.CurrentSchemaVersion() {
		view.Readiness = "degraded"
		diagnostics = append(diagnostics, adminhttp.DiagnosticObservation{
			ID: "cache-schema-" + view.CacheRef, Severity: "warning", EntityType: "cache", EntityID: view.CacheRef,
			FailureClass: "cache_schema_blocked", Message: "The managed cache schema is not ready for current writes.", Current: true,
			ObservedAt: adminTimePointer(time.Now().UTC()), Remediation: "Run gitcode-mcp migrate-cache after confirming the selected cache.",
		})
	}
	view.WALCapable, view.JournalMode, err = store.WALCapable(ctx)
	if err != nil || !view.WALCapable {
		view.Readiness = "degraded"
		diagnostics = append(diagnostics, adminhttp.DiagnosticObservation{
			ID: "cache-journal-" + view.CacheRef, Severity: "warning", EntityType: "cache", EntityID: view.CacheRef,
			FailureClass: "cache_wal_unavailable", Message: "The managed cache is not ready for concurrent WAL access.", Current: true,
			ObservedAt: adminTimePointer(time.Now().UTC()), Remediation: "Run gitcode-mcp service doctor for this cache.",
		})
	}
	repositories, err := store.ListRepositories(ctx)
	if err != nil {
		return view, append(diagnostics, adminhttp.DiagnosticObservation{
			ID: "cache-repositories-" + view.CacheRef, Severity: "error", EntityType: "cache", EntityID: view.CacheRef,
			FailureClass: "cache_read_failed", Message: "Repository membership could not be read from the cache.", Current: true,
			ObservedAt: adminTimePointer(time.Now().UTC()), Remediation: "Run gitcode-mcp doctor.",
		})
	}
	for _, repository := range repositories {
		entry := findAdminEntry(entries, group.path, repository.RepoID)
		repoView := buildAdminRepository(ctx, store, repository, entry, jobs, vectorByteCeiling)
		view.Repositories = append(view.Repositories, repoView)
		view.RecordCount += repoView.Counts.Records
		view.ChunkCount += repoView.Counts.Chunks
	}
	for _, item := range entries {
		if item.path != group.path || hasAdminRepository(repositories, item.entry.RepoID) {
			continue
		}
		view.Repositories = append(view.Repositories, adminhttp.RepositoryObservation{
			RepoID: item.entry.RepoID, BindingState: "registration_without_binding",
			Coverage:  coverageFromEntry(item.entry, adminhttp.SecondaryCount{}, 0, 0),
			Execution: executionFromEntry(item.entry, jobs),
		})
	}
	view.RepositoryCount = len(view.Repositories)
	return view, diagnostics
}

func buildAdminRepository(ctx context.Context, store *cache.SQLiteStore, repository cache.RepositoryBinding, entry *MaintenanceEntry, jobs []Job, vectorByteCeiling int64) adminhttp.RepositoryObservation {
	view := adminhttp.RepositoryObservation{
		RepoID: repository.RepoID, DisplayName: repository.DisplayName, Aliases: append([]string(nil), repository.Aliases...),
		BindingState: "bound",
	}
	for _, scope := range repository.Scopes {
		view.Scopes = append(view.Scopes, string(scope))
	}
	documentation := repositoryDocumentationObservation(ctx, store, repository.RepoID, entry, vectorByteCeiling)
	view.Documentation = &documentation
	if counts, err := store.RecordCounts(ctx, repository.RepoID); err == nil {
		view.Counts.Records = counts.Records
		view.Counts.Comments = counts.Comments
		view.Counts.Chunks = counts.Chunks
	}
	if counts, err := store.SourceKindCounts(ctx, repository.RepoID); err == nil {
		for _, count := range counts {
			view.Counts.ByKind = append(view.Counts.ByKind, adminhttp.KindCount{Kind: count.Kind, Count: count.Count})
		}
	}
	if summary, err := store.IssueCommentSyncSummary(ctx, repository.RepoID); err == nil {
		view.Counts.Secondary = adminhttp.SecondaryCount{Pending: summary.Pending, Deferred: summary.Deferred, Complete: summary.Complete, Total: summary.Total}
	}
	contentGeneration := int64(0)
	if state, err := store.GetRepoContentState(ctx, repository.RepoID); err == nil {
		contentGeneration = state.ContentGeneration
	}
	embedded := 0
	if counts, err := store.RecordCounts(ctx, repository.RepoID); err == nil {
		embedded = counts.RAGEmbeddings
	}
	if entry == nil {
		entry = &MaintenanceEntry{RepoID: repository.RepoID, ContentGeneration: contentGeneration}
	} else if entry.ContentGeneration == 0 {
		copy := *entry
		copy.ContentGeneration = contentGeneration
		entry = &copy
	}
	if frontiers, err := store.ListMaintenanceFrontiers(ctx, repository.RepoID); err == nil {
		copy := *entry
		copy.Frontiers = frontiers
		entry = &copy
	}
	view.Coverage = coverageFromEntry(*entry, view.Counts.Secondary, view.Counts.Chunks, embedded)
	view.Execution = executionFromEntry(*entry, jobs)
	view.Collections = collectionObservations(*entry, view.Counts.ByKind)
	if events, err := store.RecentSyncEventSummaries(ctx, repository.RepoID, 12); err == nil {
		for _, event := range events {
			view.RecentSync = append(view.RecentSync, adminhttp.SyncEventObservation{
				ID: event.ID, Kind: event.RemoteType, Status: event.Status,
				CompletedAt: event.CompletedAt, ZeroDelta: event.ZeroDelta,
			})
		}
	}
	return view
}

func repositoryDocumentationObservation(ctx context.Context, store *cache.SQLiteStore, repoID string, entry *MaintenanceEntry, vectorByteCeiling int64) adminhttp.RepositoryDocumentationObservation {
	view := adminhttp.RepositoryDocumentationObservation{
		State: "not_indexed", Retention: adminhttp.RepositoryDocumentationRetention{CommittedSetsPerIdentity: 8, OverlayMaxAgeHours: 24, TerminalMaxAgeHours: 24 * 7, VectorByteCeiling: vectorByteCeiling},
	}
	filter := cache.RepositoryDocRevisionSetFilter{RepoID: repoID}
	if entry != nil {
		for _, source := range entry.RepositoryDocsSources {
			view.Sources = append(view.Sources, adminhttp.RepositoryDocumentationSource{SourceID: source.SourceRegistrationID, SourceGeneration: source.SourceRegistrationGeneration, State: source.State, GitStoreRef: source.GitStoreRef, WorktreeRef: source.WorktreeRef, CommitOID: source.CommitOID, PolicyHash: source.PolicyHash})
		}
		sort.Slice(view.Sources, func(i, j int) bool { return view.Sources[i].SourceID < view.Sources[j].SourceID })
	}
	if entry != nil && entry.RepositoryDocs != nil {
		state := entry.RepositoryDocs
		view.Registered = true
		view.RegistrationID = entry.RegistrationID
		view.SourceID = state.SourceRegistrationID
		view.SourceGeneration = state.SourceRegistrationGeneration
		view.ReconcileState = state.State
		view.TargetCommitOID = state.CommitOID
		view.NextPollAt = adminTimePointer(state.NextPollAt)
		view.LastErrorClass = state.LastErrorClass
		view.LastError = state.LastError
		view.GitStoreRef = state.GitStoreRef
		view.WorktreeRef = state.WorktreeRef
		filter.SourceRegistrationID = state.SourceRegistrationID
		filter.SourceRegistrationGeneration = state.SourceRegistrationGeneration
		selector := fmt.Sprintf("--registration-id %s --source-registration-id %s --source-registration-generation %d", entry.RegistrationID, state.SourceRegistrationID, state.SourceRegistrationGeneration)
		view.IndexHandoff = fmt.Sprintf("gitcode-mcp repo-docs index --repo %s %s", repoID, selector)
		view.SearchHandoff = fmt.Sprintf("gitcode-mcp repo-docs search --repo %s %s QUERY", repoID, selector)
		// Full-text search hydrates directly from the registered Git authority
		// and does not depend on semantic derived state.
		view.SearchAvailable = true
	}
	sets, err := store.ListRepositoryDocRevisionSets(ctx, filter)
	if err != nil || len(sets) == 0 {
		return view
	}
	var set *cache.RepositoryDocRevisionSet
	for index := range sets {
		candidate := &sets[index]
		if candidate.State == cache.RepoDocSetBuilding || candidate.State == cache.RepoDocSetPartial {
			if view.ActiveSetID == "" {
				view.ActiveSetID = candidate.ID
				view.ActiveState = candidate.State
			}
		}
		if view.LastFailureClass == "" && candidate.LastErrorClass != "" {
			view.LastFailureClass = candidate.LastErrorClass
		}
		if set == nil && candidate.State == cache.RepoDocSetReady {
			set = candidate
		}
	}
	view.RevisionSetCount = len(sets)
	if set == nil {
		view.State = sets[0].State
		return view
	}
	view.State = set.State
	view.RevisionSetID = set.ID
	view.CommitOID = set.CommitOID
	view.RequestedRevision = set.RequestedRevision
	view.PolicySource = set.PolicySource
	view.PolicyHash = set.PolicyHash
	view.GitStoreRef = set.GitStoreRef
	view.WorktreeRef = set.WorktreeRef
	view.Overlay = set.OverlayDigest != ""
	view.NamespaceID = set.NamespaceID
	view.EligibleFiles = set.EligibleFiles
	view.EligibleChunks = set.EligibleChunks
	view.EmbeddedChunks = set.EmbeddedChunks
	view.ReusedChunks = set.ReusedChunks
	view.FailedChunks = set.FailedChunks
	view.MissingObjects = set.MissingObjects
	view.ExcludedFiles = set.ExcludedFiles
	view.UpdatedAt = adminTimePointer(set.UpdatedAt)
	if exclusions, exclusionErr := store.ListRepositoryDocExclusions(ctx, repoID, set.ID); exclusionErr == nil {
		counts := map[string]int{}
		for _, exclusion := range exclusions {
			counts[exclusion.ReasonCode]++
		}
		for reason, count := range counts {
			view.Exclusions = append(view.Exclusions, adminhttp.RepositoryDocumentationExclusionCount{Reason: reason, Count: count})
		}
		sort.Slice(view.Exclusions, func(i, j int) bool { return view.Exclusions[i].Reason < view.Exclusions[j].Reason })
	}
	// Semantic ranking is a separate capability of the exact published set.
	// Hybrid requests may still fall back to the always-local full-text path.
	view.SemanticAvailable = true
	return view
}

func coverageFromEntry(entry MaintenanceEntry, secondary adminhttp.SecondaryCount, chunks, embedded int) adminhttp.CoverageObservation {
	coverage := adminhttp.CoverageObservation{
		Head:       aggregateFrontierLane(entry.Frontiers, "head", frontierCollectionKinds(entry)),
		Tail:       aggregateFrontierLane(entry.Frontiers, "tail", frontierCollectionKinds(entry)),
		Secondary:  adminhttp.CoverageLane{State: "unknown", Status: "not_observed"},
		Projection: adminhttp.CoverageLane{State: "unknown", Status: "not_observed"},
		RAG:        adminhttp.CoverageLane{State: "unconfigured", Status: "not_configured"},
	}
	coverage.Secondary = adminhttp.CoverageLane{State: "current", Status: "complete"}
	if secondary.Total == 0 {
		coverage.Secondary = adminhttp.CoverageLane{State: "unknown", Status: "not_observed"}
	} else if secondary.Deferred > 0 {
		coverage.Secondary = adminhttp.CoverageLane{State: "partial", Status: "deferred", Missing: secondary.Pending + secondary.Deferred}
	} else if secondary.Pending > 0 {
		coverage.Secondary = adminhttp.CoverageLane{State: "partial", Status: "pending", Missing: secondary.Pending}
	}
	coverage.Projection = adminhttp.CoverageLane{State: "current", Status: "current", CurrentGeneration: entry.ContentGeneration, CoveredGeneration: entry.ContentGeneration}
	if entry.ContentGeneration == 0 {
		coverage.Projection = adminhttp.CoverageLane{State: "unknown", Status: "not_observed"}
	}
	if entry.Policy.RAGEnabled || entry.NamespaceID != "" {
		coverage.RAG = adminhttp.CoverageLane{State: "partial", Status: firstNonEmpty(entry.RAGStatus, "missing"), CurrentGeneration: entry.ContentGeneration, CoveredGeneration: entry.CoveredGeneration, Eligible: chunks, Embedded: embedded, Missing: maxInt(chunks-embedded, 0)}
		if entry.RAGStatus == "ready" && entry.CoveredGeneration >= entry.ContentGeneration {
			coverage.RAG.State = "current"
		}
	}
	return coverage
}

func collectionObservations(entry MaintenanceEntry, counts []adminhttp.KindCount) []adminhttp.CollectionObservation {
	countByKind := map[string]int{}
	kinds := map[string]bool{}
	for _, count := range counts {
		countByKind[count.Kind] = count.Count
		kinds[count.Kind] = true
	}
	for _, kind := range frontierCollectionKinds(entry) {
		kinds[kind] = true
	}
	ordered := make([]string, 0, len(kinds))
	for kind := range kinds {
		ordered = append(ordered, kind)
	}
	sort.Strings(ordered)
	out := make([]adminhttp.CollectionObservation, 0, len(ordered))
	for _, kind := range ordered {
		out = append(out, adminhttp.CollectionObservation{
			Kind: kind, Count: countByKind[kind],
			Head: aggregateFrontierLane(entry.Frontiers, "head", []string{kind}),
			Tail: aggregateFrontierLane(entry.Frontiers, "tail", []string{kind}),
		})
	}
	return out
}

func frontierCollectionKinds(entry MaintenanceEntry) []string {
	kinds := map[string]bool{}
	for _, frontier := range entry.Frontiers {
		if frontier.RemoteType != "" {
			kinds[frontier.RemoteType] = true
		}
	}
	for _, item := range []struct {
		enabled bool
		kind    string
	}{
		{entry.Policy.Issues, "issue"},
		{entry.Policy.Wiki, "wiki"},
		{entry.Policy.Pulls, "pull_request"},
	} {
		if item.enabled {
			kinds[item.kind] = true
		}
	}
	out := make([]string, 0, len(kinds))
	for kind := range kinds {
		out = append(out, kind)
	}
	sort.Strings(out)
	return out
}

func aggregateFrontierLane(frontiers []cache.MaintenanceFrontier, lane string, kinds []string) adminhttp.CoverageLane {
	result := adminhttp.CoverageLane{State: "unknown", Status: "not_observed"}
	if len(kinds) == 0 {
		return result
	}
	byKind := map[string]cache.MaintenanceFrontier{}
	for _, frontier := range frontiers {
		if frontier.Lane == lane {
			byKind[frontier.RemoteType] = frontier
		}
	}
	observed := 0
	current := 0
	partial := 0
	stopReasons := map[string]bool{}
	for _, kind := range kinds {
		frontier, ok := byKind[kind]
		if !ok {
			continue
		}
		observed++
		result.PagesListed += frontier.PagesListed
		result.RecordsListed += frontier.RecordsListed
		if result.UpdatedAt == nil || frontier.UpdatedAt.After(*result.UpdatedAt) {
			result.UpdatedAt = adminTimePointer(frontier.UpdatedAt)
		}
		if frontier.StopReason != "" {
			stopReasons[frontier.StopReason] = true
		}
		if frontierState(frontier.Status) == "current" {
			current++
		} else {
			partial++
		}
	}
	if observed == 0 {
		return result
	}
	if current == len(kinds) {
		result.State = "current"
		if lane == "tail" {
			result.Status = "complete"
		} else {
			result.Status = "fresh"
		}
	} else {
		result.State = "partial"
		result.Status = "partial"
		if partial == 0 && observed < len(kinds) {
			result.Status = "not_observed_for_all_collections"
		}
	}
	if len(stopReasons) == 1 {
		for reason := range stopReasons {
			result.StopReason = reason
		}
	} else if len(stopReasons) > 1 {
		result.StopReason = "mixed"
	}
	return result
}

func frontierState(status string) string {
	switch status {
	case "complete", "current", "fresh", "ready":
		return "current"
	default:
		return "partial"
	}
}

func executionFromEntry(entry MaintenanceEntry, jobs []Job) adminhttp.ExecutionObservation {
	view := adminhttp.ExecutionObservation{ActiveJobIDs: append([]string(nil), entry.ActiveJobs...)}
	for _, job := range jobs {
		if job.CacheUUID == entry.CacheUUID && job.RepoID == entry.RepoID && (job.Status == JobStatusQueued || job.Status == JobStatusRunning) {
			view.ActiveJobIDs = append(view.ActiveJobIDs, job.ID)
		}
	}
	stages := []struct {
		name  string
		state MaintenanceStageState
	}{{"sync", entry.SyncStage}, {"rag", entry.RAGStage}}
	for _, stage := range stages {
		if stage.state.LastErrorClass != "" {
			view.LastErrors = append(view.LastErrors, adminhttp.StageError{Stage: stage.name, FailureClass: stage.state.LastErrorClass, Message: publicStageMessage(stage.name, stage.state.LastErrorClass), ObservedAt: adminTimePointer(stage.state.UpdatedAt)})
		}
		if !stage.state.RetryAfter.IsZero() && stage.state.RetryAfter.After(time.Now()) {
			if view.ScheduledRetry == nil || stage.state.RetryAfter.Before(view.ScheduledRetry.At) {
				view.ScheduledRetry = &adminhttp.ScheduledRetry{Stage: stage.name, At: stage.state.RetryAfter}
			}
		}
		if strings.Contains(stage.state.LastErrorClass, "cache_busy") || strings.Contains(stage.state.LastErrorClass, "contention") {
			view.Contention = &adminhttp.Contention{State: "waiting", Operation: stage.name}
		}
	}
	view.ActiveJobIDs = uniqueStrings(view.ActiveJobIDs)
	return view
}

func adminJobObservation(job Job) adminhttp.JobObservation {
	view := adminhttp.JobObservation{
		ID: job.ID, Type: job.Type, CacheRef: publicCacheRef(job.CacheUUID, ""), RepoID: job.RepoID,
		ProfileID: job.ProfileID, NamespaceID: job.NamespaceID, RegistrationID: job.RegistrationID,
		Status: job.Status, CreatedAt: job.CreatedAt, StartedAt: job.StartedAt, UpdatedAt: job.UpdatedAt,
		FinishedAt: job.FinishedAt, Steps: job.Steps, Completed: job.Completed, FailureClass: job.ErrorClass,
		WorkRef: firstNonEmpty(job.WorkRef, publicWorkRef(job.WorkKey)), ProgressRetained: len(job.Progress), ProgressLimit: maxStoredProgressEvents,
	}
	active := job.Status == JobStatusQueued || job.Status == JobStatusRunning
	terminal := jobTerminalStatus(job.Status)
	view.Cancellable = active && (job.Type == SyncJobType || job.Type == RAGIndexJobType || job.Type == RepositoryDocsIndexJobType)
	view.Retryable = terminal && job.RegistrationID != "" && (job.Type == SyncJobType || job.Type == RAGIndexJobType)
	if !view.Cancellable && !view.Retryable {
		view.ActionReason = "No safe admin action is available for the current job type and state."
	}
	if job.StartedAt != nil && job.Completed > 0 {
		elapsed := job.UpdatedAt.Sub(*job.StartedAt).Seconds()
		if elapsed > 0 {
			view.ThroughputPerSecond = float64(job.Completed) / elapsed
			if active && job.Steps > job.Completed {
				view.ETASeconds = int(float64(job.Steps-job.Completed) / view.ThroughputPerSecond)
			}
		}
	}
	if job.Error != "" {
		view.InspectCommand = "gitcode-mcp service job " + job.ID + " --format json"
		if job.RegistrationID != "" {
			view.RemediationCommand = "gitcode-mcp service maintenance --format json"
		} else {
			view.RemediationCommand = "gitcode-mcp service doctor"
		}
	}
	for _, event := range job.Progress {
		if event.Collection != "" && event.Collection != SyncJobType && event.RecordsFailed > 0 {
			view.FailureCollection = event.Collection
		}
		if event.RetryAfter != "" {
			view.RetryAfter = event.RetryAfter
		}
		view.Progress = append(view.Progress, adminhttp.ProgressObservation{
			Type: event.Type, Phase: event.Phase, Collection: event.Collection, Page: event.Page,
			RecordsListed: event.RecordsListed, RecordsFetched: event.RecordsFetched,
			RecordsInserted: event.RecordsInserted, RecordsUpdated: event.RecordsUpdated,
			RecordsSkipped: event.RecordsSkipped, RecordsDeferred: event.RecordsDeferred,
			RecordsFailed: event.RecordsFailed, RetryAfter: event.RetryAfter, Attempt: event.Attempt,
			RateLimitState: event.RateLimitState,
		})
	}
	if job.Error != "" {
		if view.FailureCollection != "" {
			view.FailureMessage = strings.ReplaceAll(view.FailureCollection, "_", " ") + " collection failed; inspect the retained timeline before retrying."
		} else {
			view.FailureMessage = "The job ended with " + firstNonEmpty(job.ErrorClass, "a typed failure") + "; inspect the retained timeline before retrying."
		}
	}
	return view
}

func publicWorkRef(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	hash := sha256.Sum256([]byte(value))
	return "work-" + hex.EncodeToString(hash[:8])
}

func adminMaintenanceObservation(entry MaintenanceEntry) adminhttp.MaintenanceObservation {
	view := adminhttp.MaintenanceObservation{
		RegistrationID: entry.RegistrationID, CacheRef: publicCacheRef(entry.CacheUUID, ""), RepoID: entry.RepoID,
		Aliases: append([]string(nil), entry.Aliases...), LegacyRegistrationIDs: append([]string(nil), entry.LegacyRegistrationIDs...),
		NamespaceID: entry.NamespaceID, Enabled: entry.Enabled, State: entry.State, Generation: entry.Generation,
		NextReconcileAt: adminTimePointer(entry.NextReconcileAt),
		Policy:          adminMaintenancePolicyObservation(entry.Policy),
	}
	if entry.IdentityConflict != nil {
		view.IdentityConflict = &adminhttp.MaintenanceIdentityConflictObservation{
			Kind:                     entry.IdentityConflict.Kind,
			DetailsAvailable:         entry.IdentityConflict.DetailsAvailable,
			CandidateRegistrationIDs: append([]string(nil), entry.IdentityConflict.CandidateRegistrationIDs...),
			PolicyHashes:             append([]string(nil), entry.IdentityConflict.PolicyHashes...),
			ConfigHashes:             append([]string(nil), entry.IdentityConflict.ConfigHashes...),
			PathFingerprints:         append([]string(nil), entry.IdentityConflict.PathFingerprints...),
		}
		for _, candidate := range entry.IdentityConflict.Candidates {
			view.IdentityConflict.Candidates = append(view.IdentityConflict.Candidates, adminMaintenanceCandidateObservation(candidate))
		}
	}
	return view
}

func adminMaintenancePolicyObservation(policy MaintenancePolicy) adminhttp.MaintenancePolicyView {
	collections := make([]string, 0, 5)
	for _, collection := range []struct {
		name    string
		enabled bool
	}{{"issues", policy.Issues}, {"issue_comments", policy.IssueComments}, {"wiki", policy.Wiki}, {"pulls", policy.Pulls}, {"pr_comments", policy.PRComments}} {
		if collection.enabled {
			collections = append(collections, collection.name)
		}
	}
	return adminhttp.MaintenancePolicyView{
		SyncEnabled: policy.SyncEnabled, SyncMode: policy.SyncMode, RAGEnabled: policy.RAGEnabled,
		Collections: collections, HeadIntervalSeconds: policy.HeadIntervalSeconds,
		RAGIntervalSeconds: policy.RAGIntervalSeconds, HeadMaxPages: policy.HeadMaxPages,
		TailSlicePages: policy.TailSlicePages, PerPage: policy.PerPage, Profile: policy.Profile,
	}
}

func adminMaintenanceCandidateObservation(candidate MaintenanceIdentityCandidate) adminhttp.MaintenanceIdentityCandidateObservation {
	view := adminhttp.MaintenanceIdentityCandidateObservation{
		CandidateRef: candidate.CandidateRef, SelectionKind: candidate.SelectionKind,
		RegistrationID: candidate.RegistrationID, RepoID: candidate.RepoID,
		Policy: adminMaintenancePolicyObservation(candidate.Policy), PolicyHash: candidate.PolicyHash,
		ConfigHash: candidate.ConfigHash, PathFingerprint: candidate.PathFingerprint,
		SourceAuthorityHash: candidate.SourceAuthorityHash, SourceRefs: append([]string(nil), candidate.SourceRefs...),
		WasEnabled:            candidate.WasEnabled,
		CohortRegistrationIDs: append([]string(nil), candidate.CohortRegistrationIDs...),
		CohortRepoIDs:         append([]string(nil), candidate.CohortRepoIDs...),
	}
	for _, member := range candidate.Members {
		view.Members = append(view.Members, adminMaintenanceCandidateObservation(member))
	}
	return view
}

func adminStageDiagnostics(entry MaintenanceEntry) []adminhttp.DiagnosticObservation {
	var out []adminhttp.DiagnosticObservation
	for _, stage := range []struct {
		name  string
		state MaintenanceStageState
	}{{"sync", entry.SyncStage}, {"rag", entry.RAGStage}} {
		if stage.state.LastErrorClass == "" {
			continue
		}
		current := stage.state.Status == "failed" || (!stage.state.RetryAfter.IsZero() && stage.state.RetryAfter.After(time.Now()))
		out = append(out, adminhttp.DiagnosticObservation{
			ID: "maintenance-" + entry.RegistrationID + "-" + stage.name, Severity: "warning",
			EntityType: "maintenance", EntityID: entry.RegistrationID, FailureClass: stage.state.LastErrorClass,
			Message: publicStageMessage(stage.name, stage.state.LastErrorClass), Retryable: true, Current: current,
			ObservedAt: adminTimePointer(stage.state.UpdatedAt), Remediation: "Inspect the registration and retry only after the scheduled backoff.",
		})
	}
	return out
}

func adminCapabilityObservation(cap capability.Capability) adminhttp.CapabilityObservation {
	uiReason := cap.UI.DisabledReason
	if !cap.UI.Enabled && uiReason == "" {
		uiReason = "Not exposed by the local admin console."
	}
	return adminhttp.CapabilityObservation{
		ID: cap.ID, Category: string(cap.Category), SafetyClass: string(cap.Safety), Description: cap.Description,
		UIEnabled: cap.UI.Enabled, UIReason: uiReason, CLIName: cap.CLIName,
		CLIEnabled: cap.CLI.Enabled, MCPName: cap.MCPName, MCPEnabled: cap.MCP.Enabled,
	}
}

func findAdminEntry(entries []adminMaintenanceEntry, path, repoID string) *MaintenanceEntry {
	for _, item := range entries {
		if canonicalAdminCachePath(item.path) == canonicalAdminCachePath(path) && item.entry.RepoID == repoID {
			copy := item.entry
			return &copy
		}
	}
	return nil
}

func hasAdminRepository(repositories []cache.RepositoryBinding, repoID string) bool {
	for _, repository := range repositories {
		if repository.RepoID == repoID {
			return true
		}
	}
	return false
}

func publicCacheRef(uuid, path string) string {
	value := strings.TrimSpace(uuid)
	if value == "" && strings.TrimSpace(path) == "" {
		return ""
	}
	if value == "" {
		hash := sha256.Sum256([]byte(strings.TrimSpace(path)))
		value = hex.EncodeToString(hash[:])
	}
	value = strings.ReplaceAll(value, "-", "")
	if len(value) > 12 {
		value = value[:12]
	}
	if value == "" {
		return ""
	}
	return "cache-" + value
}

func publicStageMessage(stage, failureClass string) string {
	if failureClass == "" {
		return ""
	}
	return strings.ToUpper(stage[:1]) + stage[1:] + " maintenance recorded " + failureClass + "."
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func adminTimePointer(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	copy := value.UTC()
	return &copy
}
