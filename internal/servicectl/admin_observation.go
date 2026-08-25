package servicectl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	snapshot := adminhttp.ObservationSnapshot{Service: adminhttp.ServiceObservation{
		Version: m.Version, Protocol: "admin.v1", Running: true, StartedAt: adminTimePointer(startedAt), AdminSecure: true,
	}}
	if status, err := m.Status(); err == nil {
		snapshot.Service.Installed = status.Installed
		snapshot.Service.InstallKind = status.InstallKind
	}
	entries := maintenance.adminEntries()
	jobList := jobs.List()
	cacheGroups := groupAdminCaches(m.AdminCachePath, entries)
	for _, group := range cacheGroups {
		cacheView, diagnostics := buildAdminCache(ctx, group, entries, jobList)
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

func groupAdminCaches(primaryPath string, entries []adminMaintenanceEntry) []adminCacheGroup {
	byPath := map[string]adminCacheGroup{}
	for _, item := range entries {
		path := strings.TrimSpace(item.path)
		if path == "" {
			continue
		}
		byPath[path] = adminCacheGroup{path: path, uuid: item.entry.CacheUUID, fingerprint: item.entry.PathFingerprint}
	}
	primaryPath = strings.TrimSpace(primaryPath)
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

func buildAdminCache(ctx context.Context, group adminCacheGroup, entries []adminMaintenanceEntry, jobs []Job) (adminhttp.CacheObservation, []adminhttp.DiagnosticObservation) {
	view := adminhttp.CacheObservation{
		CacheRef: publicCacheRef(group.uuid, group.path), PathFingerprint: group.fingerprint,
		StorageMode: "managed", Readiness: "unavailable",
	}
	var diagnostics []adminhttp.DiagnosticObservation
	store, err := cache.NewSQLiteReadOnlyStore(ctx, group.path)
	if err != nil {
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
		repoView := buildAdminRepository(ctx, store, repository, entry, jobs)
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

func buildAdminRepository(ctx context.Context, store *cache.SQLiteStore, repository cache.RepositoryBinding, entry *MaintenanceEntry, jobs []Job) adminhttp.RepositoryObservation {
	view := adminhttp.RepositoryObservation{
		RepoID: repository.RepoID, DisplayName: repository.DisplayName, Aliases: append([]string(nil), repository.Aliases...),
		BindingState: "bound",
	}
	for _, scope := range repository.Scopes {
		view.Scopes = append(view.Scopes, string(scope))
	}
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
	}
	if job.Error != "" {
		view.FailureMessage = "The job ended with a typed failure; use diagnostics for remediation."
	}
	for _, event := range job.Progress {
		view.Progress = append(view.Progress, adminhttp.ProgressObservation{
			Type: event.Type, Phase: event.Phase, Collection: event.Collection, Page: event.Page,
			RecordsListed: event.RecordsListed, RecordsFetched: event.RecordsFetched,
			RecordsInserted: event.RecordsInserted, RecordsUpdated: event.RecordsUpdated,
			RecordsSkipped: event.RecordsSkipped, RecordsDeferred: event.RecordsDeferred,
			RecordsFailed: event.RecordsFailed, RetryAfter: event.RetryAfter, Attempt: event.Attempt,
			RateLimitState: event.RateLimitState,
		})
	}
	return view
}

func adminMaintenanceObservation(entry MaintenanceEntry) adminhttp.MaintenanceObservation {
	collections := make([]string, 0, 5)
	for _, collection := range []struct {
		enabled bool
		name    string
	}{{entry.Policy.Issues, "issues"}, {entry.Policy.IssueComments, "issue_comments"}, {entry.Policy.Wiki, "wiki"}, {entry.Policy.Pulls, "pulls"}, {entry.Policy.PRComments, "pr_comments"}} {
		if collection.enabled {
			collections = append(collections, collection.name)
		}
	}
	return adminhttp.MaintenanceObservation{
		RegistrationID: entry.RegistrationID, CacheRef: publicCacheRef(entry.CacheUUID, ""), RepoID: entry.RepoID,
		NamespaceID: entry.NamespaceID, Enabled: entry.Enabled, State: entry.State, Generation: entry.Generation,
		NextReconcileAt: adminTimePointer(entry.NextReconcileAt),
		Policy: adminhttp.MaintenancePolicyView{
			SyncEnabled: entry.Policy.SyncEnabled, SyncMode: entry.Policy.SyncMode, RAGEnabled: entry.Policy.RAGEnabled,
			Collections: collections, HeadIntervalSeconds: entry.Policy.HeadIntervalSeconds,
			RAGIntervalSeconds: entry.Policy.RAGIntervalSeconds, HeadMaxPages: entry.Policy.HeadMaxPages,
			TailSlicePages: entry.Policy.TailSlicePages, PerPage: entry.Policy.PerPage, Profile: entry.Policy.Profile,
		},
	}
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
	return adminhttp.CapabilityObservation{
		ID: cap.ID, Category: string(cap.Category), SafetyClass: string(cap.Safety), Description: cap.Description,
		UIEnabled: false, UIReason: "Not exposed by the read-only observation slice.", CLIName: cap.CLIName,
		CLIEnabled: cap.CLI.Enabled, MCPName: cap.MCPName, MCPEnabled: cap.MCP.Enabled,
	}
}

func findAdminEntry(entries []adminMaintenanceEntry, path, repoID string) *MaintenanceEntry {
	for _, item := range entries {
		if item.path == path && item.entry.RepoID == repoID {
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
