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
	"sync"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/service"
)

const maintenanceRegistrySchema = "gitcode-mcp.managed-caches.v1"

type MaintenancePolicy struct {
	SyncEnabled         bool   `json:"sync_enabled"`
	RAGEnabled          bool   `json:"rag_enabled"`
	Issues              bool   `json:"issues,omitempty"`
	IssueComments       bool   `json:"issue_comments,omitempty"`
	Wiki                bool   `json:"wiki,omitempty"`
	Pulls               bool   `json:"pulls,omitempty"`
	PRComments          bool   `json:"pr_comments,omitempty"`
	HeadIntervalSeconds int    `json:"head_interval_seconds,omitempty"`
	RAGIntervalSeconds  int    `json:"rag_interval_seconds,omitempty"`
	HeadMaxPages        int    `json:"head_max_pages,omitempty"`
	TailSlicePages      int    `json:"tail_slice_pages,omitempty"`
	PerPage             int    `json:"per_page,omitempty"`
	Profile             string `json:"profile,omitempty"`
}

type MaintenanceStageState struct {
	LastJobID           string    `json:"last_job_id,omitempty"`
	Status              string    `json:"status,omitempty"`
	LastErrorClass      string    `json:"last_error_class,omitempty"`
	LastError           string    `json:"last_error,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures,omitempty"`
	RetryAfter          time.Time `json:"retry_after,omitempty"`
	UpdatedAt           time.Time `json:"updated_at,omitempty"`
	NamespaceID         string    `json:"namespace_id,omitempty"`
}

type MaintenanceEntry struct {
	RegistrationID    string                      `json:"registration_id"`
	CacheUUID         string                      `json:"cache_uuid"`
	PathFingerprint   string                      `json:"path_fingerprint"`
	RepoID            string                      `json:"repo_id"`
	NamespaceID       string                      `json:"namespace_id,omitempty"`
	Policy            MaintenancePolicy           `json:"policy"`
	Enabled           bool                        `json:"enabled"`
	Generation        int64                       `json:"generation"`
	State             string                      `json:"state"`
	ContentGeneration int64                       `json:"content_generation,omitempty"`
	CoveredGeneration int64                       `json:"covered_generation,omitempty"`
	RAGStatus         string                      `json:"rag_status,omitempty"`
	Frontiers         []cache.MaintenanceFrontier `json:"frontiers,omitempty"`
	ActiveJobs        []string                    `json:"active_jobs,omitempty"`
	LastErrorClass    string                      `json:"last_error_class,omitempty"`
	LastError         string                      `json:"last_error,omitempty"`
	SyncStage         MaintenanceStageState       `json:"sync_stage,omitempty"`
	RAGStage          MaintenanceStageState       `json:"rag_stage,omitempty"`
	LastSeenAt        time.Time                   `json:"last_seen_at"`
	LastReconciledAt  time.Time                   `json:"last_reconciled_at,omitempty"`
	NextReconcileAt   time.Time                   `json:"next_reconcile_at,omitempty"`
	cachePath         string
}

type maintenanceDiskEntry struct {
	MaintenanceEntry
	CachePath string `json:"cache_path"`
}

type maintenanceRegistryFile struct {
	SchemaVersion string                 `json:"schema_version"`
	Generation    int64                  `json:"generation"`
	Entries       []maintenanceDiskEntry `json:"entries"`
	Receipts      []maintenanceReceipt   `json:"idempotency_receipts,omitempty"`
}

type maintenanceReceipt struct {
	KeyHash        string `json:"key_hash"`
	RegistrationID string `json:"registration_id"`
}

type MaintenanceEnrollRequest struct {
	CachePath      string            `json:"cache_path"`
	RepoID         string            `json:"repo_id"`
	Policy         MaintenancePolicy `json:"policy"`
	IdempotencyKey string            `json:"idempotency_key"`
}

type MaintenanceRegistrationRequest struct {
	RegistrationID string `json:"registration_id"`
}

type MaintenanceListResult struct {
	SchemaVersion string             `json:"schema_version"`
	Generation    int64              `json:"generation"`
	Entries       []MaintenanceEntry `json:"entries"`
}

type MaintenanceReconcileResult struct {
	Entries     []MaintenanceEntry `json:"entries"`
	JobsStarted []string           `json:"jobs_started,omitempty"`
	CheckedAt   time.Time          `json:"checked_at"`
}

type MaintenanceManager struct {
	mu          sync.Mutex
	reconcileMu sync.Mutex
	manager     Manager
	jobs        *JobManager
	path        string
	generation  int64
	entries     map[string]*MaintenanceEntry
	receipts    map[string]string
	now         func() time.Time
}

func NewMaintenanceManager(manager Manager, jobs *JobManager, path string) *MaintenanceManager {
	return &MaintenanceManager{manager: manager, jobs: jobs, path: path, entries: map[string]*MaintenanceEntry{}, receipts: map[string]string{}, now: func() time.Time { return time.Now().UTC() }}
}

func (m *MaintenanceManager) Load() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := os.ReadFile(m.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var disk maintenanceRegistryFile
	if err := json.Unmarshal(data, &disk); err != nil {
		return err
	}
	if disk.SchemaVersion != maintenanceRegistrySchema {
		return fmt.Errorf("maintenance: unsupported registry schema %q", disk.SchemaVersion)
	}
	m.generation = disk.Generation
	for _, stored := range disk.Entries {
		entry := stored.MaintenanceEntry
		if entry.Policy.RAGIntervalSeconds <= 0 {
			entry.Policy.RAGIntervalSeconds = 900
		}
		entry.cachePath = stored.CachePath
		m.entries[entry.RegistrationID] = &entry
	}
	for _, receipt := range disk.Receipts {
		m.receipts[receipt.KeyHash] = receipt.RegistrationID
	}
	return nil
}

func (m *MaintenanceManager) Run(ctx context.Context) {
	_, _ = m.Reconcile(ctx)
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = m.Reconcile(ctx)
		}
	}
}

func (m *MaintenanceManager) Enroll(ctx context.Context, req MaintenanceEnrollRequest) (MaintenanceEntry, error) {
	if strings.TrimSpace(req.CachePath) == "" || strings.TrimSpace(req.RepoID) == "" || strings.TrimSpace(req.IdempotencyKey) == "" {
		return MaintenanceEntry{}, errors.New("maintenance: cache_path, repo_id, and idempotency_key are required")
	}
	path, err := canonicalCachePath(req.CachePath)
	if err != nil {
		return MaintenanceEntry{}, err
	}
	store, err := cache.NewSQLiteReadOnlyStore(ctx, path)
	if err != nil {
		return MaintenanceEntry{}, err
	}
	defer store.Close()
	version, err := store.SchemaVersion(ctx)
	if err != nil {
		return MaintenanceEntry{}, err
	}
	if version != cache.CurrentSchemaVersion() {
		return MaintenanceEntry{}, fmt.Errorf("maintenance: cache schema version %d is not current version %d", version, cache.CurrentSchemaVersion())
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil {
		return MaintenanceEntry{}, err
	}
	repos, err := store.ListRepositories(ctx)
	if err != nil {
		return MaintenanceEntry{}, err
	}
	var binding cache.RepositoryBinding
	for _, repo := range repos {
		if repo.RepoID == req.RepoID {
			binding = repo
			break
		}
	}
	if binding.RepoID == "" {
		return MaintenanceEntry{}, fmt.Errorf("maintenance: repository %q is not bound in selected cache", req.RepoID)
	}
	policy, err := normalizeMaintenancePolicy(req.Policy, binding)
	if err != nil {
		return MaintenanceEntry{}, err
	}
	registrationID := maintenanceRegistrationID(identity.UUID, req.RepoID)
	keyHash := maintenanceIdempotencyKeyHash(req.IdempotencyKey)
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if replayRegistrationID, ok := m.receipts[keyHash]; ok {
		if replayRegistrationID != registrationID {
			return MaintenanceEntry{}, errors.New("maintenance: idempotency key was already used for another registration")
		}
		if existing := m.entries[registrationID]; existing != nil {
			return cloneMaintenanceEntry(existing), nil
		}
	}
	for _, registered := range m.entries {
		if registered.CacheUUID == identity.UUID && registered.cachePath != path {
			return MaintenanceEntry{}, errors.New("maintenance: cache clone detected at another registered location")
		}
	}
	if existing := m.entries[registrationID]; existing != nil {
		if existing.cachePath != path {
			return MaintenanceEntry{}, errors.New("maintenance: cache clone detected at another registered location")
		}
		if existing.Policy == policy && existing.Enabled {
			previousSeen := existing.LastSeenAt
			existing.LastSeenAt = now
			m.receipts[keyHash] = registrationID
			m.generation++
			if err := m.saveLocked(); err != nil {
				existing.LastSeenAt = previousSeen
				delete(m.receipts, keyHash)
				m.generation--
				return MaintenanceEntry{}, err
			}
			return cloneMaintenanceEntry(existing), nil
		}
		previous := *existing
		profileChanged := existing.Policy.Profile != policy.Profile || existing.Policy.RAGEnabled != policy.RAGEnabled
		existing.Policy = policy
		if profileChanged {
			existing.NamespaceID = ""
			existing.RAGStage = MaintenanceStageState{}
		}
		existing.Enabled = true
		existing.Generation++
		existing.State = "enrolled"
		existing.LastSeenAt = now
		m.receipts[keyHash] = registrationID
		m.generation++
		if err := m.saveLocked(); err != nil {
			*existing = previous
			delete(m.receipts, keyHash)
			m.generation--
			return MaintenanceEntry{}, err
		}
		return cloneMaintenanceEntry(existing), nil
	}
	entry := &MaintenanceEntry{RegistrationID: registrationID, CacheUUID: identity.UUID, PathFingerprint: pathFingerprint(path), RepoID: req.RepoID, Policy: policy, Enabled: true, Generation: 1, State: "enrolled", LastSeenAt: now, cachePath: path}
	m.entries[registrationID] = entry
	m.receipts[keyHash] = registrationID
	m.generation++
	if err := m.saveLocked(); err != nil {
		delete(m.entries, registrationID)
		delete(m.receipts, keyHash)
		m.generation--
		return MaintenanceEntry{}, err
	}
	return cloneMaintenanceEntry(entry), nil
}

func (m *MaintenanceManager) List(ctx context.Context) (MaintenanceListResult, error) {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	entries := make([]MaintenanceEntry, 0, len(m.entries))
	for _, entry := range m.entries {
		entries = append(entries, cloneMaintenanceEntry(entry))
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].RegistrationID < entries[j].RegistrationID })
	return MaintenanceListResult{SchemaVersion: maintenanceRegistrySchema, Generation: m.generation, Entries: entries}, nil
}

func (m *MaintenanceManager) Disable(ctx context.Context, registrationID string) (MaintenanceEntry, error) {
	_ = ctx
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.entries[strings.TrimSpace(registrationID)]
	if entry == nil {
		return MaintenanceEntry{}, errors.New("maintenance: registration not found")
	}
	entry.Enabled = false
	entry.Generation++
	entry.State = "disabled"
	m.generation++
	if err := m.saveLocked(); err != nil {
		return MaintenanceEntry{}, err
	}
	return cloneMaintenanceEntry(entry), nil
}

func (m *MaintenanceManager) Reconcile(ctx context.Context) (MaintenanceReconcileResult, error) {
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()
	list, err := m.List(ctx)
	if err != nil {
		return MaintenanceReconcileResult{}, err
	}
	result := MaintenanceReconcileResult{CheckedAt: m.now()}
	for _, snapshot := range list.Entries {
		if !snapshot.Enabled {
			result.Entries = append(result.Entries, snapshot)
			continue
		}
		entry, started := m.reconcileEntry(ctx, snapshot.RegistrationID)
		result.Entries = append(result.Entries, entry)
		result.JobsStarted = append(result.JobsStarted, started...)
	}
	return result, nil
}

func (m *MaintenanceManager) reconcileEntry(ctx context.Context, registrationID string) (MaintenanceEntry, []string) {
	m.mu.Lock()
	entry := m.entries[registrationID]
	if entry == nil {
		m.mu.Unlock()
		return MaintenanceEntry{}, nil
	}
	snapshot := *entry
	path := entry.cachePath
	m.mu.Unlock()
	now := m.now()
	store, err := cache.NewSQLiteReadOnlyStore(ctx, path)
	if err != nil {
		return m.updateEntryFailure(registrationID, "cache_unreadable", err), nil
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil || identity.UUID != snapshot.CacheUUID {
		store.Close()
		if err == nil {
			err = errors.New("cache identity changed")
		}
		return m.updateEntryFailure(registrationID, "cache_replaced", err), nil
	}
	contentState, err := store.GetRepoContentState(ctx, snapshot.RepoID)
	if err != nil {
		store.Close()
		return m.updateEntryFailure(registrationID, "content_state_failed", err), nil
	}
	frontiers, _ := store.ListMaintenanceFrontiers(ctx, snapshot.RepoID)
	namespaces, _ := store.ListEmbeddingNamespaces(ctx, snapshot.RepoID)
	latestSync, _ := m.jobs.LatestCacheRepo(SyncJobType, snapshot.CacheUUID, snapshot.RepoID)
	latestRAG, _ := m.jobs.LatestCacheRepo(RAGIndexJobType, snapshot.CacheUUID, snapshot.RepoID)
	snapshot.SyncStage = observeMaintenanceStage(snapshot.SyncStage, latestSync, now)
	snapshot.RAGStage = observeMaintenanceStage(snapshot.RAGStage, latestRAG, now)
	if (latestRAG.Status == JobStatusSucceeded || latestRAG.Status == JobStatusSuperseded) && latestRAG.NamespaceID != "" {
		snapshot.NamespaceID = latestRAG.NamespaceID
	}
	namespaceID := selectMaintenanceNamespace(snapshot, namespaces)
	covered := int64(0)
	ragStatus := "missing"
	coverageUpdatedAt := time.Time{}
	if namespaceID != "" {
		state, ok, _ := store.GetRAGCoverageState(ctx, snapshot.RepoID, namespaceID)
		if ok {
			covered = state.CoveredGeneration
			ragStatus = state.Status
			coverageUpdatedAt = state.UpdatedAt
		}
	}
	store.Close()
	started := []string{}
	activeSync, _ := m.jobs.ActiveCacheRepo(SyncJobType, snapshot.CacheUUID, snapshot.RepoID)
	activeRAG, _ := m.jobs.ActiveCacheRepo(RAGIndexJobType, snapshot.CacheUUID, snapshot.RepoID)
	ragInterval := time.Duration(snapshot.Policy.RAGIntervalSeconds) * time.Second
	ragVerificationDue := maintenanceRAGVerificationDue(namespaceID, ragStatus, coverageUpdatedAt, ragInterval, now)
	needsRAGRepair := contentState.ContentGeneration > covered || (contentState.ContentGeneration > 0 && ragStatus != "ready") || ragVerificationDue
	if activeSync.ID == "" && activeRAG.ID == "" {
		lane, page, maxPages := nextMaintenanceSyncLane(snapshot, frontiers, now)
		syncReady := snapshot.SyncStage.RetryAfter.IsZero() || !now.Before(snapshot.SyncStage.RetryAfter)
		ragReady := snapshot.RAGStage.RetryAfter.IsZero() || !now.Before(snapshot.RAGStage.RetryAfter)
		stage := nextMaintenanceStage(snapshot.Policy, lane, needsRAGRepair, syncReady, ragReady)
		if stage == SyncJobType {
			req := StartSyncJobRequest{
				RepoID: snapshot.RepoID, ProviderMode: "live", CachePath: path,
				CacheUUID: snapshot.CacheUUID, RegistrationID: snapshot.RegistrationID, Lane: lane,
				Issues: snapshot.Policy.Issues, IssueComments: snapshot.Policy.IssueComments,
				Wiki: snapshot.Policy.Wiki, Pulls: snapshot.Policy.Pulls, PRComments: snapshot.Policy.PRComments,
				MaxPages: maxPages, PerPage: snapshot.Policy.PerPage,
				Page: page, IdempotencyKey: fmt.Sprintf("maintenance-%s-%s-%d-%d", snapshot.RegistrationID, lane, page, now.Unix()/60),
			}
			job, jobErr := m.jobs.StartSync(context.Background(), m.manager, req)
			if jobErr != nil {
				return m.updateEntryFailure(registrationID, "sync_schedule_failed", jobErr), nil
			}
			started = append(started, job.ID)
			activeSync = job
		} else if stage == RAGIndexJobType {
			job, jobErr := m.jobs.StartRAGIndex(context.Background(), m.manager, StartRAGIndexJobRequest{RepoID: snapshot.RepoID, Profile: snapshot.Policy.Profile, CachePath: path, CacheUUID: snapshot.CacheUUID, RegistrationID: snapshot.RegistrationID})
			if jobErr != nil {
				return m.updateEntryFailure(registrationID, "rag_schedule_failed", jobErr), started
			}
			started = append(started, job.ID)
			activeRAG = job
		}
	}
	m.mu.Lock()
	entry = m.entries[registrationID]
	entry.ContentGeneration = contentState.ContentGeneration
	entry.CoveredGeneration = covered
	entry.RAGStatus = ragStatus
	entry.SyncStage = snapshot.SyncStage
	entry.RAGStage = snapshot.RAGStage
	entry.NamespaceID = namespaceID
	if activeRAG.NamespaceID != "" {
		entry.NamespaceID = activeRAG.NamespaceID
	}
	entry.Frontiers = frontiers
	entry.ActiveJobs = activeMaintenanceJobIDs(activeSync, activeRAG)
	entry.LastErrorClass, entry.LastError = maintenanceEntryError(entry.Policy, entry.SyncStage, entry.RAGStage)
	entry.State = deriveMaintenanceEntryState(*entry)
	if activeSync.ID != "" {
		entry.State = "refreshing"
		if strings.Contains(activeSync.WorkKey, ":tail:") {
			entry.State = "backfilling"
		}
	} else if activeRAG.ID != "" {
		entry.State = "indexing"
	}
	entry.LastReconciledAt = now
	entry.NextReconcileAt = now.Add(time.Minute)
	_ = m.saveLocked()
	updated := cloneMaintenanceEntry(entry)
	m.mu.Unlock()
	return updated, started
}

func maintenanceRAGVerificationDue(namespaceID, status string, updatedAt time.Time, interval time.Duration, now time.Time) bool {
	return namespaceID != "" && status == "ready" && (updatedAt.IsZero() || !updatedAt.Add(interval).After(now))
}

func selectMaintenanceNamespace(entry MaintenanceEntry, namespaces []cache.EmbeddingNamespace) string {
	if entry.NamespaceID != "" {
		for _, namespace := range namespaces {
			if namespace.ID == entry.NamespaceID && (entry.Policy.Profile == "" || namespace.ProfileID == entry.Policy.Profile) {
				return namespace.ID
			}
		}
	}
	selected := cache.EmbeddingNamespace{}
	for _, namespace := range namespaces {
		if entry.Policy.Profile != "" && namespace.ProfileID != entry.Policy.Profile {
			continue
		}
		if selected.ID == "" || namespace.UpdatedAt.After(selected.UpdatedAt) {
			selected = namespace
		}
	}
	return selected.ID
}

func activeMaintenanceJobIDs(jobs ...Job) []string {
	ids := []string{}
	seen := map[string]bool{}
	for _, job := range jobs {
		if job.ID != "" && !seen[job.ID] {
			ids = append(ids, job.ID)
			seen[job.ID] = true
		}
	}
	return ids
}

func nextMaintenanceSyncLane(entry MaintenanceEntry, frontiers []cache.MaintenanceFrontier, now time.Time) (string, int, int) {
	expected := maintenanceRemoteTypes(entry.Policy)
	if len(expected) == 0 {
		return "", 0, 0
	}
	byKey := make(map[string]cache.MaintenanceFrontier, len(frontiers))
	for i := range frontiers {
		frontier := frontiers[i]
		byKey[frontier.RemoteType+"\x00"+frontier.Lane] = frontier
	}
	interval := time.Duration(entry.Policy.HeadIntervalSeconds) * time.Second
	for _, remoteType := range expected {
		head, ok := byKey[remoteType+"\x00head"]
		if !ok || head.UpdatedAt.Add(interval).Before(now) {
			return "head", 1, entry.Policy.HeadMaxPages
		}
	}
	nextPage := 0
	needsTail := false
	for _, remoteType := range expected {
		tail, ok := byKey[remoteType+"\x00tail"]
		if !ok || tail.Status != "complete" {
			needsTail = true
		}
		if !ok || tail.Status == "complete" {
			continue
		}
		page := 1
		_, _ = fmt.Sscanf(tail.Checkpoint, "next_page:%d", &page)
		if page <= 0 {
			page = 1
		}
		if nextPage == 0 || page < nextPage {
			nextPage = page
		}
	}
	if needsTail {
		if nextPage <= 0 {
			nextPage = 1
		}
		return "tail", nextPage, entry.Policy.TailSlicePages
	}
	return "", 0, 0
}

func maintenanceRemoteTypes(policy MaintenancePolicy) []string {
	remoteTypes := []string{}
	if policy.Issues {
		remoteTypes = append(remoteTypes, "issue")
	}
	if policy.IssueComments {
		remoteTypes = append(remoteTypes, "issue_comment")
	}
	if policy.Wiki {
		remoteTypes = append(remoteTypes, "wiki")
	}
	if policy.Pulls {
		remoteTypes = append(remoteTypes, "pull_request")
	}
	if policy.PRComments {
		remoteTypes = append(remoteTypes, "pr_comment")
	}
	return remoteTypes
}

func nextMaintenanceStage(policy MaintenancePolicy, lane string, needsRAGRepair, syncReady, ragReady bool) string {
	if policy.SyncEnabled && syncReady && lane == "head" {
		return SyncJobType
	}
	if policy.RAGEnabled && ragReady && needsRAGRepair {
		return RAGIndexJobType
	}
	if policy.SyncEnabled && syncReady && lane == "tail" {
		return SyncJobType
	}
	return ""
}

func observeMaintenanceStage(state MaintenanceStageState, job Job, now time.Time) MaintenanceStageState {
	if job.ID == "" || job.ID == state.LastJobID || !jobTerminalStatus(job.Status) {
		return state
	}
	state.LastJobID = job.ID
	state.Status = job.Status
	state.UpdatedAt = job.UpdatedAt
	if job.FinishedAt != nil {
		state.UpdatedAt = *job.FinishedAt
	}
	if job.NamespaceID != "" {
		state.NamespaceID = job.NamespaceID
	}
	switch job.Status {
	case JobStatusSucceeded, JobStatusSuperseded:
		state.ConsecutiveFailures = 0
		state.LastErrorClass = ""
		state.LastError = ""
		state.RetryAfter = time.Time{}
	default:
		state.ConsecutiveFailures++
		state.LastErrorClass = sanitizeMaintenanceErrorClass(job.ErrorClass, job.Type+"_failed")
		state.LastError = publicMaintenanceJobError(job.Type, state.LastErrorClass)
		state.RetryAfter = now.Add(maintenanceStageBackoff(state.ConsecutiveFailures))
	}
	return state
}

func maintenanceStageBackoff(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	shift := failures - 1
	if shift > 6 {
		shift = 6
	}
	return time.Minute * time.Duration(1<<shift)
}

func maintenanceEntryError(policy MaintenancePolicy, syncStage, ragStage MaintenanceStageState) (string, string) {
	selected := MaintenanceStageState{}
	stages := []MaintenanceStageState{}
	if policy.SyncEnabled {
		stages = append(stages, syncStage)
	}
	if policy.RAGEnabled {
		stages = append(stages, ragStage)
	}
	for _, stage := range stages {
		if stage.LastErrorClass != "" && (selected.LastErrorClass == "" || stage.UpdatedAt.After(selected.UpdatedAt)) {
			selected = stage
		}
	}
	return selected.LastErrorClass, selected.LastError
}

func maintenanceJobErrorClass(err error, fallback string) string {
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	for current := err; current != nil; current = errors.Unwrap(current) {
		if coded, ok := current.(interface{ DiagnosticCode() string }); ok && strings.TrimSpace(coded.DiagnosticCode()) != "" {
			return sanitizeMaintenanceErrorClass(coded.DiagnosticCode(), fallback)
		}
	}
	return sanitizeMaintenanceErrorClass(fallback, "maintenance_failed")
}

func sanitizeMaintenanceErrorClass(value, fallback string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" || len(value) > 64 {
		return fallback
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return fallback
		}
	}
	return value
}

func sanitizeMaintenanceProgress(jobType string, event service.ProgressEvent) service.ProgressEvent {
	if event.RecordsFailed > 0 || event.Type == JobStatusFailed || event.Type == JobStatusCancelled || event.Type == JobStatusInterrupted || event.Phase == JobStatusFailed || event.Phase == JobStatusCancelled || event.Phase == JobStatusInterrupted {
		event.Message = publicMaintenanceJobError(jobType, "progress_failed")
	}
	return event
}

func publicMaintenanceJobError(jobType, class string) string {
	if class == "cancelled" {
		return "maintenance job was cancelled"
	}
	if jobType == RAGIndexJobType {
		return "RAG maintenance failed"
	}
	return "remote cache maintenance failed"
}

func (m *MaintenanceManager) updateEntryFailure(id, class string, _ error) MaintenanceEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.entries[id]
	if entry == nil {
		return MaintenanceEntry{}
	}
	entry.State = "degraded"
	entry.LastErrorClass = class
	entry.LastError = publicMaintenanceError(class)
	entry.LastReconciledAt = m.now()
	entry.NextReconcileAt = entry.LastReconciledAt.Add(time.Minute)
	_ = m.saveLocked()
	return cloneMaintenanceEntry(entry)
}

func (m *MaintenanceManager) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return err
	}
	disk := maintenanceRegistryFile{SchemaVersion: maintenanceRegistrySchema, Generation: m.generation}
	for _, entry := range m.entries {
		disk.Entries = append(disk.Entries, maintenanceDiskEntry{MaintenanceEntry: cloneMaintenanceEntry(entry), CachePath: entry.cachePath})
	}
	sort.Slice(disk.Entries, func(i, j int) bool { return disk.Entries[i].RegistrationID < disk.Entries[j].RegistrationID })
	for keyHash, registrationID := range m.receipts {
		disk.Receipts = append(disk.Receipts, maintenanceReceipt{KeyHash: keyHash, RegistrationID: registrationID})
	}
	sort.Slice(disk.Receipts, func(i, j int) bool { return disk.Receipts[i].KeyHash < disk.Receipts[j].KeyHash })
	data, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, m.path)
}

func normalizeMaintenancePolicy(policy MaintenancePolicy, binding cache.RepositoryBinding) (MaintenancePolicy, error) {
	if policy.HeadIntervalSeconds <= 0 {
		policy.HeadIntervalSeconds = 900
	}
	if policy.RAGIntervalSeconds <= 0 {
		policy.RAGIntervalSeconds = 900
	}
	if policy.HeadMaxPages <= 0 {
		policy.HeadMaxPages = 3
	}
	if policy.TailSlicePages <= 0 {
		policy.TailSlicePages = 10
	}
	if policy.PerPage <= 0 {
		policy.PerPage = 100
	}
	if policy.SyncEnabled && !policy.Issues && !policy.IssueComments && !policy.Wiki && !policy.Pulls && !policy.PRComments {
		if bindingHasScope(binding, cache.RepositoryScopeIssues) {
			policy.Issues, policy.IssueComments, policy.Pulls, policy.PRComments = true, true, true, true
		}
		if bindingHasScope(binding, cache.RepositoryScopeWiki) {
			policy.Wiki = true
		}
	}
	if (policy.Issues || policy.IssueComments || policy.Pulls || policy.PRComments) && !bindingHasScope(binding, cache.RepositoryScopeIssues) {
		return MaintenancePolicy{}, errors.New("maintenance: issues scope is not enabled for selected repository")
	}
	if policy.Wiki && !bindingHasScope(binding, cache.RepositoryScopeWiki) {
		return MaintenancePolicy{}, errors.New("maintenance: wiki scope is not enabled for selected repository")
	}
	return policy, nil
}

func bindingHasScope(binding cache.RepositoryBinding, scope cache.RepositoryScope) bool {
	for _, configured := range binding.Scopes {
		if configured == scope {
			return true
		}
	}
	return false
}

func publicMaintenanceError(class string) string {
	switch class {
	case "cache_unreadable":
		return "managed cache is unavailable"
	case "cache_replaced":
		return "managed cache identity changed"
	case "content_state_failed":
		return "managed cache content state is unavailable"
	case "sync_schedule_failed":
		return "remote cache refresh could not be scheduled"
	case "rag_schedule_failed":
		return "RAG repair could not be scheduled"
	default:
		return "maintenance operation failed"
	}
}

func canonicalCachePath(path string) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("maintenance: cache path is not a regular file")
	}
	return resolved, nil
}

func maintenanceRegistrationID(cacheUUID, repoID string) string {
	sum := sha256.Sum256([]byte(cacheUUID + "\x00" + repoID))
	return "cache-reg-" + hex.EncodeToString(sum[:8])
}

func pathFingerprint(path string) string {
	sum := sha256.Sum256([]byte(path))
	return "sha256:" + hex.EncodeToString(sum[:8])
}

func maintenanceIdempotencyKeyHash(key string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return "sha256:" + hex.EncodeToString(sum[:16])
}

func deriveMaintenanceEntryState(entry MaintenanceEntry) string {
	if !entry.Enabled {
		return "disabled"
	}
	if entry.LastErrorClass != "" {
		return "degraded"
	}
	if entry.Policy.SyncEnabled {
		for _, frontier := range entry.Frontiers {
			if frontier.Lane == "tail" && frontier.Status != "complete" {
				return "backfilling"
			}
		}
	}
	if entry.Policy.RAGEnabled && (entry.CoveredGeneration < entry.ContentGeneration || (entry.ContentGeneration > 0 && entry.RAGStatus != "ready")) {
		return "indexing"
	}
	return "ready"
}

func cloneMaintenanceEntry(entry *MaintenanceEntry) MaintenanceEntry {
	if entry == nil {
		return MaintenanceEntry{}
	}
	copy := *entry
	copy.Frontiers = append([]cache.MaintenanceFrontier(nil), entry.Frontiers...)
	copy.ActiveJobs = append([]string(nil), entry.ActiveJobs...)
	copy.cachePath = ""
	return copy
}
