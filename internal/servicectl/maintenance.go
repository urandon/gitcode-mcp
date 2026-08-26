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
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/service"
)

const maintenanceRegistrySchema = "gitcode-mcp.managed-caches.v1"

type MaintenancePolicy struct {
	SyncEnabled         bool   `json:"sync_enabled"`
	SyncMode            string `json:"sync_mode,omitempty"`
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
	ConfigHash        string                      `json:"config_hash,omitempty"`
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
	configReference   string
	configSnapshot    config.Config
}

type maintenanceDiskEntry struct {
	MaintenanceEntry
	CachePath       string        `json:"cache_path"`
	ConfigReference string        `json:"config_reference,omitempty"`
	ConfigSnapshot  config.Config `json:"config_snapshot"`
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
	IntentHash     string `json:"intent_hash,omitempty"`
}

type MaintenanceEnrollRequest struct {
	CachePath       string            `json:"cache_path"`
	RepoID          string            `json:"repo_id"`
	Policy          MaintenancePolicy `json:"policy"`
	IdempotencyKey  string            `json:"idempotency_key"`
	ConfigReference string            `json:"config_reference,omitempty"`
	ConfigHash      string            `json:"config_hash,omitempty"`
	ConfigSnapshot  config.Config     `json:"config_snapshot"`
}

type MaintenanceResolveConfigRequest struct {
	CachePath      string        `json:"cache_path"`
	Profile        string        `json:"profile,omitempty"`
	RAGEnabled     bool          `json:"rag_enabled"`
	ConfigSnapshot config.Config `json:"config_snapshot"`
}

type MaintenanceResolveConfigResult struct {
	ConfigHash string `json:"config_hash"`
	Profile    string `json:"profile,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
}

type MaintenanceRegistrationRequest struct {
	RegistrationID string `json:"registration_id"`
}

type MaintenanceIdempotencyConflictError struct{}

func (MaintenanceIdempotencyConflictError) Error() string {
	return "maintenance: idempotency key was already used for a different enrollment intent"
}

func (MaintenanceIdempotencyConflictError) DiagnosticCode() string { return "idempotency_conflict" }

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

type MaintenanceCapabilities struct {
	RegistryProtocol string   `json:"registry_protocol"`
	Methods          []string `json:"methods"`
	BinaryVersion    string   `json:"binary_version,omitempty"`
}

func maintenanceCapabilities(version string) MaintenanceCapabilities {
	return MaintenanceCapabilities{RegistryProtocol: maintenanceRegistrySchema, BinaryVersion: version, Methods: []string{"Maintenance.Enroll", "Maintenance.List", "Maintenance.Reconcile", "Maintenance.ReconcileRegistration", "Maintenance.ResolveConfig", "Maintenance.Disable"}}
}

type MaintenanceManager struct {
	mu          sync.Mutex
	reconcileMu sync.Mutex
	manager     Manager
	jobs        *JobManager
	path        string
	generation  int64
	entries     map[string]*MaintenanceEntry
	receipts    map[string]maintenanceReceipt
	now         func() time.Time
}

func NewMaintenanceManager(manager Manager, jobs *JobManager, path string) *MaintenanceManager {
	return &MaintenanceManager{manager: manager, jobs: jobs, path: path, entries: map[string]*MaintenanceEntry{}, receipts: map[string]maintenanceReceipt{}, now: func() time.Time { return time.Now().UTC() }}
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
		entry.configReference = stored.ConfigReference
		entry.configSnapshot = stored.ConfigSnapshot
		m.entries[entry.RegistrationID] = &entry
	}
	for _, receipt := range disk.Receipts {
		m.receipts[receipt.KeyHash] = receipt
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
	intentHash := maintenanceEnrollmentIntentHash(registrationID, policy, req.ConfigHash)
	if req.ConfigHash == "" || req.ConfigHash != maintenanceHash(req.ConfigSnapshot) {
		return MaintenanceEntry{}, errors.New("maintenance: config snapshot hash mismatch")
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if receipt, ok := m.receipts[keyHash]; ok {
		if receipt.RegistrationID != registrationID || (receipt.IntentHash != "" && receipt.IntentHash != intentHash) {
			return MaintenanceEntry{}, MaintenanceIdempotencyConflictError{}
		}
		if existing := m.entries[registrationID]; existing != nil {
			if receipt.IntentHash == "" && (existing.Policy != policy || existing.ConfigHash != strings.TrimSpace(req.ConfigHash)) {
				return MaintenanceEntry{}, MaintenanceIdempotencyConflictError{}
			}
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
		if existing.Policy == policy && existing.ConfigHash == strings.TrimSpace(req.ConfigHash) && existing.Enabled {
			previousSeen := existing.LastSeenAt
			existing.LastSeenAt = now
			m.receipts[keyHash] = maintenanceReceipt{KeyHash: keyHash, RegistrationID: registrationID, IntentHash: intentHash}
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
		syncChanged := maintenanceSyncPolicyChanged(existing.Policy, policy)
		existing.Policy = policy
		existing.ConfigHash = strings.TrimSpace(req.ConfigHash)
		existing.configReference = strings.TrimSpace(req.ConfigReference)
		existing.configSnapshot = req.ConfigSnapshot
		if profileChanged {
			existing.NamespaceID = ""
			existing.RAGStage = MaintenanceStageState{}
		}
		if syncChanged {
			existing.SyncStage = MaintenanceStageState{}
		}
		existing.Enabled = true
		existing.Generation++
		existing.State = "enrolled"
		existing.LastSeenAt = now
		m.receipts[keyHash] = maintenanceReceipt{KeyHash: keyHash, RegistrationID: registrationID, IntentHash: intentHash}
		m.generation++
		if err := m.saveLocked(); err != nil {
			*existing = previous
			delete(m.receipts, keyHash)
			m.generation--
			return MaintenanceEntry{}, err
		}
		return cloneMaintenanceEntry(existing), nil
	}
	entry := &MaintenanceEntry{RegistrationID: registrationID, CacheUUID: identity.UUID, PathFingerprint: pathFingerprint(path), RepoID: req.RepoID, Policy: policy, ConfigHash: strings.TrimSpace(req.ConfigHash), Enabled: true, Generation: 1, State: "enrolled", LastSeenAt: now, cachePath: path, configReference: strings.TrimSpace(req.ConfigReference), configSnapshot: req.ConfigSnapshot}
	m.entries[registrationID] = entry
	m.receipts[keyHash] = maintenanceReceipt{KeyHash: keyHash, RegistrationID: registrationID, IntentHash: intentHash}
	m.generation++
	if err := m.saveLocked(); err != nil {
		delete(m.entries, registrationID)
		delete(m.receipts, keyHash)
		m.generation--
		return MaintenanceEntry{}, err
	}
	return cloneMaintenanceEntry(entry), nil
}

func maintenanceSyncPolicyChanged(before, after MaintenancePolicy) bool {
	return before.SyncEnabled != after.SyncEnabled ||
		before.SyncMode != after.SyncMode ||
		before.Issues != after.Issues ||
		before.IssueComments != after.IssueComments ||
		before.Wiki != after.Wiki ||
		before.Pulls != after.Pulls ||
		before.PRComments != after.PRComments ||
		before.HeadIntervalSeconds != after.HeadIntervalSeconds ||
		before.HeadMaxPages != after.HeadMaxPages ||
		before.TailSlicePages != after.TailSlicePages ||
		before.PerPage != after.PerPage
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
	if err := m.jobs.Prune(); err != nil {
		return MaintenanceReconcileResult{}, fmt.Errorf("maintenance: prune job history: %w", err)
	}
	list, err := m.List(ctx)
	if err != nil {
		return MaintenanceReconcileResult{}, err
	}
	result := MaintenanceReconcileResult{CheckedAt: m.now()}
	schedule := maintenanceReconcileOrder(list.Entries, m.jobs.List())
	updated := make(map[string]MaintenanceEntry, len(schedule))
	for _, snapshot := range schedule {
		if !snapshot.Enabled {
			updated[snapshot.RegistrationID] = snapshot
			continue
		}
		entry, started := m.reconcileEntry(ctx, snapshot.RegistrationID)
		updated[snapshot.RegistrationID] = entry
		result.JobsStarted = append(result.JobsStarted, started...)
	}
	for _, snapshot := range list.Entries {
		result.Entries = append(result.Entries, updated[snapshot.RegistrationID])
	}
	return result, nil
}

func maintenanceReconcileOrder(entries []MaintenanceEntry, jobs []Job) []MaintenanceEntry {
	ordered := append([]MaintenanceEntry(nil), entries...)
	lastWriter := map[string]time.Time{}
	for _, job := range jobs {
		if !isCacheWriterJob(job.Type) || job.RegistrationID == "" {
			continue
		}
		if lastWriter[job.RegistrationID].Before(job.UpdatedAt) {
			lastWriter[job.RegistrationID] = job.UpdatedAt
		}
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].CacheUUID != ordered[j].CacheUUID {
			return ordered[i].CacheUUID < ordered[j].CacheUUID
		}
		left, right := lastWriter[ordered[i].RegistrationID], lastWriter[ordered[j].RegistrationID]
		if left.Equal(right) {
			return ordered[i].RegistrationID < ordered[j].RegistrationID
		}
		return left.Before(right)
	})
	return ordered
}

func (m *MaintenanceManager) ReconcileRegistration(ctx context.Context, registrationID string) (MaintenanceReconcileResult, error) {
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()
	registrationID = strings.TrimSpace(registrationID)
	if registrationID == "" {
		return MaintenanceReconcileResult{}, errors.New("maintenance: registration_id is required")
	}
	m.mu.Lock()
	registered := m.entries[registrationID]
	var snapshot MaintenanceEntry
	if registered != nil {
		snapshot = cloneMaintenanceEntry(registered)
	}
	m.mu.Unlock()
	if registered == nil {
		return MaintenanceReconcileResult{}, errors.New("maintenance: registration not found")
	}
	if !snapshot.Enabled {
		return MaintenanceReconcileResult{Entries: []MaintenanceEntry{snapshot}, CheckedAt: m.now()}, nil
	}
	entry, started := m.reconcileEntry(ctx, registrationID)
	return MaintenanceReconcileResult{Entries: []MaintenanceEntry{entry}, JobsStarted: started, CheckedAt: m.now()}, nil
}

func (m *MaintenanceManager) ResolveConfig(req MaintenanceResolveConfigRequest) (MaintenanceResolveConfigResult, error) {
	if strings.TrimSpace(req.CachePath) == "" {
		return MaintenanceResolveConfigResult{}, errors.New("maintenance: cache_path is required")
	}
	cfg := req.ConfigSnapshot
	cfg.CachePath = req.CachePath
	result := MaintenanceResolveConfigResult{ConfigHash: maintenanceHash(cfg)}
	if !req.RAGEnabled {
		return result, nil
	}
	profile := strings.TrimSpace(req.Profile)
	if profile == "" {
		profile = strings.TrimSpace(cfg.RAG.DefaultProfile)
	}
	if profile == "" {
		profile = config.DefaultRAGProfile
	}
	profileConfig, ok := cfg.RAG.Profiles[profile]
	if !ok {
		return MaintenanceResolveConfigResult{}, fmt.Errorf("maintenance: RAG profile %q is not configured", profile)
	}
	provider := strings.TrimSpace(profileConfig.Provider)
	if _, ok := cfg.RAG.Providers[provider]; !ok {
		return MaintenanceResolveConfigResult{}, fmt.Errorf("maintenance: RAG provider %q is not configured", provider)
	}
	result.Profile, result.Provider, result.Model = profile, provider, profileConfig.Model
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
	snapshot.configSnapshot = entry.configSnapshot
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
	jobManager := m.manager
	if snapshot.ConfigHash != "" {
		jobManager.EffectiveConfig = &snapshot.configSnapshot
	}
	effectiveProfile := maintenanceEffectiveProfile(jobManager, path, snapshot.Policy.Profile)
	namespaceID := selectMaintenanceNamespace(snapshot, namespaces, effectiveProfile)
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
	activeWriter, _ := m.jobs.ActiveCacheWriter(snapshot.CacheUUID)
	ragInterval := time.Duration(snapshot.Policy.RAGIntervalSeconds) * time.Second
	ragVerificationDue := maintenanceRAGVerificationDue(namespaceID, ragStatus, coverageUpdatedAt, ragInterval, now)
	needsRAGRepair := contentState.ContentGeneration > covered || (contentState.ContentGeneration > 0 && ragStatus != "ready") || ragVerificationDue
	if activeSync.ID == "" && activeRAG.ID == "" && activeWriter.ID == "" {
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
			job, jobErr := m.jobs.StartSync(context.Background(), jobManager, req)
			if jobErr != nil {
				var busy ErrCacheWriterBusy
				if errors.As(jobErr, &busy) {
					return m.finishReconcileEntry(registrationID, snapshot, contentState.ContentGeneration, covered, ragStatus, namespaceID, frontiers, activeSync, activeRAG, now), nil
				}
				return m.updateEntryFailure(registrationID, "sync_schedule_failed", jobErr), nil
			}
			started = append(started, job.ID)
			activeSync = job
		} else if stage == RAGIndexJobType {
			job, jobErr := m.jobs.StartRAGIndex(context.Background(), jobManager, StartRAGIndexJobRequest{RepoID: snapshot.RepoID, Profile: effectiveProfile, CachePath: path, CacheUUID: snapshot.CacheUUID, RegistrationID: snapshot.RegistrationID})
			if jobErr != nil {
				var busy ErrCacheWriterBusy
				if errors.As(jobErr, &busy) {
					return m.finishReconcileEntry(registrationID, snapshot, contentState.ContentGeneration, covered, ragStatus, namespaceID, frontiers, activeSync, activeRAG, now), started
				}
				return m.updateEntryFailure(registrationID, "rag_schedule_failed", jobErr), started
			}
			started = append(started, job.ID)
			activeRAG = job
		}
	}
	return m.finishReconcileEntry(registrationID, snapshot, contentState.ContentGeneration, covered, ragStatus, namespaceID, frontiers, activeSync, activeRAG, now), started
}

func (m *MaintenanceManager) finishReconcileEntry(registrationID string, snapshot MaintenanceEntry, contentGeneration, covered int64, ragStatus, namespaceID string, frontiers []cache.MaintenanceFrontier, activeSync, activeRAG Job, now time.Time) MaintenanceEntry {
	m.mu.Lock()
	entry := m.entries[registrationID]
	entry.ContentGeneration = contentGeneration
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
	return updated
}

func maintenanceRAGVerificationDue(namespaceID, status string, updatedAt time.Time, interval time.Duration, now time.Time) bool {
	return namespaceID != "" && status == "ready" && (updatedAt.IsZero() || !updatedAt.Add(interval).After(now))
}

func maintenanceEffectiveProfile(manager Manager, cachePath, requested string) string {
	if profile := strings.TrimSpace(requested); profile != "" {
		return profile
	}
	effective, err := effectiveJobConfig(manager, cachePath)
	if err == nil {
		if profile := strings.TrimSpace(effective.Config.RAG.DefaultProfile); profile != "" {
			return profile
		}
	}
	return config.DefaultRAGProfile
}

func selectMaintenanceNamespace(entry MaintenanceEntry, namespaces []cache.EmbeddingNamespace, effectiveProfile string) string {
	if entry.NamespaceID == "" || strings.TrimSpace(effectiveProfile) == "" {
		return ""
	}
	for _, namespace := range namespaces {
		if namespace.ID == entry.NamespaceID && namespace.ProfileID == effectiveProfile {
			return namespace.ID
		}
	}
	return ""
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
	headPage := 0
	for _, remoteType := range expected {
		head, ok := byKey[remoteType+"\x00head"]
		if !ok || head.Status == "fresh" && head.UpdatedAt.Add(interval).Before(now) {
			return "head", 1, entry.Policy.HeadMaxPages
		}
		if head.Status != "fresh" {
			page := checkpointPage(head.Checkpoint)
			if page <= 0 {
				page = 1
			}
			if headPage == 0 || page < headPage {
				headPage = page
			}
		}
	}
	if headPage > 0 {
		return "head", headPage, entry.Policy.HeadMaxPages
	}
	if entry.Policy.SyncMode == "head" {
		return "", 0, 0
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
		disk.Entries = append(disk.Entries, maintenanceDiskEntry{MaintenanceEntry: cloneMaintenanceEntry(entry), CachePath: entry.cachePath, ConfigReference: entry.configReference, ConfigSnapshot: entry.configSnapshot})
	}
	sort.Slice(disk.Entries, func(i, j int) bool { return disk.Entries[i].RegistrationID < disk.Entries[j].RegistrationID })
	for _, receipt := range m.receipts {
		disk.Receipts = append(disk.Receipts, receipt)
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
	if policy.SyncEnabled && policy.SyncMode == "" {
		policy.SyncMode = "head-and-backfill"
	}
	if !policy.SyncEnabled && policy.SyncMode == "" {
		policy.SyncMode = "off"
	}
	if policy.SyncMode != "off" && policy.SyncMode != "head" && policy.SyncMode != "head-and-backfill" {
		return MaintenancePolicy{}, errors.New("maintenance: sync_mode must be off, head, or head-and-backfill")
	}
	if policy.SyncEnabled != (policy.SyncMode != "off") {
		return MaintenancePolicy{}, errors.New("maintenance: sync_enabled and sync_mode disagree")
	}
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

func maintenanceEnrollmentIntentHash(registrationID string, policy MaintenancePolicy, configHash string) string {
	payload, _ := json.Marshal(struct {
		RegistrationID string            `json:"registration_id"`
		Policy         MaintenancePolicy `json:"policy"`
		ConfigHash     string            `json:"config_hash,omitempty"`
	}{registrationID, policy, strings.TrimSpace(configHash)})
	sum := sha256.Sum256(payload)
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
		selected := map[string]bool{}
		for _, remoteType := range maintenanceRemoteTypes(entry.Policy) {
			selected[remoteType] = true
		}
		for _, frontier := range entry.Frontiers {
			if selected[frontier.RemoteType] && frontier.Lane == "tail" && frontier.Status != "complete" {
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
