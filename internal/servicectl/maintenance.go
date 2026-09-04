package servicectl

import (
	"bytes"
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
	"gitcode-mcp/internal/repositorydocs"
	"gitcode-mcp/internal/service"
)

const (
	legacyMaintenanceRegistrySchema = "gitcode-mcp.managed-caches.v1"
	maintenanceRegistrySchema       = "gitcode-mcp.managed-caches.v2"
)

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
	RegistrationID            string                           `json:"registration_id"`
	CacheUUID                 string                           `json:"cache_uuid"`
	PathFingerprint           string                           `json:"path_fingerprint"`
	RepoID                    string                           `json:"repo_id"`
	Aliases                   []string                         `json:"aliases,omitempty"`
	LegacyRegistrationIDs     []string                         `json:"legacy_registration_ids,omitempty"`
	IdentityConflict          *MaintenanceIdentityConflict     `json:"identity_conflict,omitempty"`
	NamespaceID               string                           `json:"namespace_id,omitempty"`
	Policy                    MaintenancePolicy                `json:"policy"`
	Enabled                   bool                             `json:"enabled"`
	Generation                int64                            `json:"generation"`
	State                     string                           `json:"state"`
	ContentGeneration         int64                            `json:"content_generation,omitempty"`
	CoveredGeneration         int64                            `json:"covered_generation,omitempty"`
	RAGStatus                 string                           `json:"rag_status,omitempty"`
	ConfigHash                string                           `json:"config_hash,omitempty"`
	Frontiers                 []cache.MaintenanceFrontier      `json:"frontiers,omitempty"`
	ActiveJobs                []string                         `json:"active_jobs,omitempty"`
	LastErrorClass            string                           `json:"last_error_class,omitempty"`
	LastError                 string                           `json:"last_error,omitempty"`
	DetectedSchemaVersion     int                              `json:"detected_schema_version,omitempty"`
	ExpectedSchemaVersion     int                              `json:"expected_schema_version,omitempty"`
	DaemonBinaryVersion       string                           `json:"daemon_binary_version,omitempty"`
	DaemonBinaryCommit        string                           `json:"daemon_binary_commit,omitempty"`
	QuiesceState              string                           `json:"quiesce_state,omitempty"`
	SyncStage                 MaintenanceStageState            `json:"sync_stage,omitempty"`
	RAGStage                  MaintenanceStageState            `json:"rag_stage,omitempty"`
	RepositoryDocs            *RepositoryDocsMaintenanceState  `json:"repository_docs,omitempty"`
	RepositoryDocsSources     []RepositoryDocsMaintenanceState `json:"repository_docs_sources,omitempty"`
	LastSeenAt                time.Time                        `json:"last_seen_at"`
	LastReconciledAt          time.Time                        `json:"last_reconciled_at,omitempty"`
	NextReconcileAt           time.Time                        `json:"next_reconcile_at,omitempty"`
	cachePath                 string
	configReference           string
	configSnapshot            config.Config
	repositoryPath            string
	repositoryProfile         string
	repositorySourceID        string
	identityBlockedWasEnabled bool
}

type MaintenanceIdentityConflict struct {
	Kind                     string                         `json:"kind,omitempty"`
	DetailsAvailable         bool                           `json:"details_available"`
	CandidateRegistrationIDs []string                       `json:"candidate_registration_ids"`
	PolicyHashes             []string                       `json:"policy_hashes"`
	ConfigHashes             []string                       `json:"config_hashes"`
	PathFingerprints         []string                       `json:"path_fingerprints,omitempty"`
	Candidates               []MaintenanceIdentityCandidate `json:"candidates,omitempty"`
}

type MaintenanceIdentityCandidate struct {
	CandidateRef          string                         `json:"candidate_ref"`
	SelectionKind         string                         `json:"selection_kind,omitempty"`
	RegistrationID        string                         `json:"registration_id"`
	RepoID                string                         `json:"repo_id"`
	Policy                MaintenancePolicy              `json:"policy"`
	PolicyHash            string                         `json:"policy_hash"`
	ConfigHash            string                         `json:"config_hash,omitempty"`
	PathFingerprint       string                         `json:"path_fingerprint"`
	SourceAuthorityHash   string                         `json:"source_authority_hash,omitempty"`
	SourceRefs            []string                       `json:"source_refs,omitempty"`
	WasEnabled            bool                           `json:"was_enabled"`
	CohortRegistrationIDs []string                       `json:"cohort_registration_ids,omitempty"`
	CohortRepoIDs         []string                       `json:"cohort_repo_ids,omitempty"`
	Members               []MaintenanceIdentityCandidate `json:"members,omitempty"`
}

type MaintenanceRegistrationRedirect struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type RepositoryDocsMaintenanceState struct {
	SourceRegistrationID         string                `json:"source_registration_id,omitempty"`
	SourceRegistrationGeneration int64                 `json:"source_registration_generation,omitempty"`
	GitStoreRef                  string                `json:"git_store_ref"`
	WorktreeRef                  string                `json:"worktree_ref,omitempty"`
	CommitOID                    string                `json:"commit_oid,omitempty"`
	PolicyHash                   string                `json:"policy_hash,omitempty"`
	RevisionSetID                string                `json:"revision_set_id,omitempty"`
	State                        string                `json:"state"`
	LastErrorClass               string                `json:"last_error_class,omitempty"`
	LastError                    string                `json:"last_error,omitempty"`
	Stage                        MaintenanceStageState `json:"stage,omitempty"`
	UpdatedAt                    time.Time             `json:"updated_at,omitempty"`
	NextPollAt                   time.Time             `json:"next_poll_at,omitempty"`
}

type maintenanceDiskEntry struct {
	MaintenanceEntry
	CachePath                  string                                 `json:"cache_path"`
	ConfigReference            string                                 `json:"config_reference,omitempty"`
	ConfigSnapshot             config.Config                          `json:"config_snapshot"`
	RepositoryPath             string                                 `json:"repository_path,omitempty"`
	RepositoryProfile          string                                 `json:"repository_profile,omitempty"`
	RepositoryDocsSources      []repositoryDocsDiskSource             `json:"repository_docs_sources,omitempty"`
	IdentityBlockedWasEnabled  bool                                   `json:"identity_blocked_was_enabled,omitempty"`
	IdentityConflictCandidates []maintenanceIdentityConflictCandidate `json:"identity_conflict_candidates,omitempty"`
}

// maintenanceIdentityConflictCandidate retains the exact private bundle for
// one pre-migration candidate. Public status receives only the paired,
// sanitized MaintenanceIdentityCandidate projection.
type maintenanceIdentityConflictCandidate struct {
	CandidateRef      string                     `json:"candidate_ref"`
	Entry             MaintenanceEntry           `json:"entry"`
	CachePath         string                     `json:"cache_path"`
	ConfigReference   string                     `json:"config_reference,omitempty"`
	ConfigSnapshot    config.Config              `json:"config_snapshot"`
	RepositoryPath    string                     `json:"repository_path,omitempty"`
	RepositoryProfile string                     `json:"repository_profile,omitempty"`
	RepositorySources []repositoryDocsDiskSource `json:"repository_docs_sources,omitempty"`
	WasEnabled        bool                       `json:"was_enabled"`
}

type repositoryDocsDiskSource struct {
	State          RepositoryDocsMaintenanceState `json:"state"`
	RepositoryPath string                         `json:"repository_path"`
	Profile        string                         `json:"profile,omitempty"`
}

type repositoryDocsRegisteredSource struct {
	State          RepositoryDocsMaintenanceState
	RepositoryPath string
	Profile        string
}

type maintenanceRegistryFile struct {
	SchemaVersion                string                                 `json:"schema_version"`
	Generation                   int64                                  `json:"generation"`
	Entries                      []maintenanceDiskEntry                 `json:"entries"`
	Receipts                     []maintenanceReceipt                   `json:"idempotency_receipts,omitempty"`
	RepositoryDocsAdmissionQueue []repositoryDocsAdmissionIntent        `json:"repository_docs_admission_queue,omitempty"`
	RegistrationRedirects        []MaintenanceRegistrationRedirect      `json:"registration_redirects,omitempty"`
	SourceRegistrationRedirects  []MaintenanceRegistrationRedirect      `json:"source_registration_redirects,omitempty"`
	ConflictResolutionReceipts   []maintenanceConflictResolutionReceipt `json:"conflict_resolution_receipts,omitempty"`
	RetiredClonePaths            []maintenanceRetiredClonePath          `json:"retired_clone_paths,omitempty"`
}

// repositoryDocsAdmissionIntent is the durable handoff between private source
// registration and the independently persisted jobs snapshot. It intentionally
// contains only opaque identities and semantic inputs; local paths remain in
// the source registration entry.
type repositoryDocsAdmissionIntent struct {
	RegistrationID               string    `json:"registration_id"`
	SourceRegistrationID         string    `json:"source_registration_id"`
	SourceRegistrationGeneration int64     `json:"source_registration_generation"`
	AuthorityPathFingerprint     string    `json:"authority_path_fingerprint,omitempty"`
	RepoID                       string    `json:"repo_id"`
	WorkKey                      string    `json:"work_key"`
	ExpectedRevisionSetID        string    `json:"expected_revision_set_id"`
	Revision                     string    `json:"revision,omitempty"`
	IncludeWorktree              bool      `json:"include_worktree,omitempty"`
	JobID                        string    `json:"job_id,omitempty"`
	Disposition                  string    `json:"disposition,omitempty"`
	FinishedAt                   time.Time `json:"finished_at,omitempty"`
	CreatedAt                    time.Time `json:"created_at"`
}

const (
	repositoryDocsAdmissionPending   = "pending"
	repositoryDocsAdmissionCancelled = "cancelled"
)

type repositoryDocsAdminSource struct {
	RegistrationID               string
	SourceRegistrationID         string
	SourceRegistrationGeneration int64
	CacheUUID                    string
	RepoID                       string
	RepositoryPath               string
	CachePath                    string
	Profile                      string
	Config                       config.Config
}

type maintenanceReceipt struct {
	KeyHash                  string `json:"key_hash"`
	RegistrationID           string `json:"registration_id"`
	IntentHash               string `json:"intent_hash,omitempty"`
	AuthorityPathFingerprint string `json:"authority_path_fingerprint,omitempty"`
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

// RepositoryDocsSourceRebindRequest is the domain-level compare-and-swap
// operation for replacing a private Git authority. It is intentionally not an
// implicit side effect of ordinary indexing admission.
type RepositoryDocsSourceRebindRequest struct {
	RepoID               string `json:"repo_id,omitempty"`
	RegistrationID       string `json:"registration_id"`
	SourceRegistrationID string `json:"source_registration_id,omitempty"`
	ExpectedGeneration   int64  `json:"expected_generation"`
	RepositoryPath       string `json:"repository_path"`
	Profile              string `json:"profile,omitempty"`
}

type RepositoryDocsSourceSelector struct {
	RegistrationID               string `json:"registration_id"`
	SourceRegistrationID         string `json:"source_registration_id"`
	SourceRegistrationGeneration int64  `json:"source_registration_generation"`
}

type RepositoryDocsSourceConflictError struct{}

func (RepositoryDocsSourceConflictError) Error() string {
	return "repository docs: a different private source is already registered"
}

func (RepositoryDocsSourceConflictError) DiagnosticCode() string {
	return "repository_docs_source_conflict"
}

type RepositoryDocsSourceAmbiguousError struct{}

func (RepositoryDocsSourceAmbiguousError) Error() string {
	return "repository docs: more than one private source matches; select a source registration"
}

func (RepositoryDocsSourceAmbiguousError) DiagnosticCode() string {
	return "repository_docs_source_ambiguous"
}

type RepositoryDocsSourceGenerationConflictError struct{}

func (RepositoryDocsSourceGenerationConflictError) Error() string {
	return "repository docs: source registration generation changed"
}

func (RepositoryDocsSourceGenerationConflictError) DiagnosticCode() string {
	return "repository_docs_source_generation_conflict"
}

type RepositoryDocsSourceUnavailableError struct {
	code string
}

func (e RepositoryDocsSourceUnavailableError) Error() string {
	return "repository docs: private source registration is unavailable"
}

func (e RepositoryDocsSourceUnavailableError) DiagnosticCode() string {
	if strings.TrimSpace(e.code) != "" {
		return e.code
	}
	return "repository_docs_source_unavailable"
}

type MaintenanceIdempotencyConflictError struct{}

func (MaintenanceIdempotencyConflictError) Error() string {
	return "maintenance: idempotency key was already used for a different enrollment intent"
}

func (MaintenanceIdempotencyConflictError) DiagnosticCode() string { return "idempotency_conflict" }

type MaintenanceIdentityConflictError struct{ kind string }

func (e MaintenanceIdentityConflictError) Error() string {
	if e.kind == "cache_clone_conflict" {
		return "maintenance: cache identity is registered at multiple locations; resolve the clone conflict before enrollment"
	}
	return "maintenance: canonical repository identity has conflicting policies; resolve the conflict before enrollment"
}

func (e MaintenanceIdentityConflictError) DiagnosticCode() string {
	if strings.TrimSpace(e.kind) != "" {
		return e.kind
	}
	return "identity_conflict"
}

type maintenancePolicyScopeError struct {
	Scope cache.RepositoryScope
}

func (e maintenancePolicyScopeError) Error() string {
	return "maintenance: " + string(e.Scope) + " scope is not enabled for selected repository"
}

func (maintenancePolicyScopeError) DiagnosticCode() string { return "invalid_policy" }

type MaintenanceListResult struct {
	SchemaVersion string             `json:"schema_version"`
	Generation    int64              `json:"generation"`
	Entries       []MaintenanceEntry `json:"entries"`
}

type MaintenanceReconcileResult struct {
	Entries       []MaintenanceEntry `json:"entries"`
	JobsStarted   []string           `json:"jobs_started,omitempty"`
	JobsCoalesced []string           `json:"jobs_coalesced,omitempty"`
	CheckedAt     time.Time          `json:"checked_at"`
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
	mu                 sync.Mutex
	reconcileMu        sync.Mutex
	manager            Manager
	jobs               *JobManager
	path               string
	generation         int64
	entries            map[string]*MaintenanceEntry
	receipts           map[string]maintenanceReceipt
	admissions         map[string]repositoryDocsAdmissionIntent
	sources            map[string]map[string]*repositoryDocsRegisteredSource
	redirects          map[string]string
	sourceRedirects    map[string]string
	conflictCandidates map[string][]maintenanceIdentityConflictCandidate
	resolutionReceipts map[string]maintenanceConflictResolutionReceipt
	retiredClonePaths  map[string]map[string]bool
	now                func() time.Time
	writeFile          func(string, []byte, os.FileMode) error
}

func NewMaintenanceManager(manager Manager, jobs *JobManager, path string) *MaintenanceManager {
	maintenance := &MaintenanceManager{manager: manager, jobs: jobs, path: path, entries: map[string]*MaintenanceEntry{}, receipts: map[string]maintenanceReceipt{}, admissions: map[string]repositoryDocsAdmissionIntent{}, sources: map[string]map[string]*repositoryDocsRegisteredSource{}, redirects: map[string]string{}, sourceRedirects: map[string]string{}, conflictCandidates: map[string][]maintenanceIdentityConflictCandidate{}, resolutionReceipts: map[string]maintenanceConflictResolutionReceipt{}, retiredClonePaths: map[string]map[string]bool{}, now: func() time.Time { return time.Now().UTC() }, writeFile: durableAtomicWriteFile}
	if jobs != nil {
		jobs.onRepositoryDocsCancelled = maintenance.cancelRepositoryDocsAdmission
		jobs.repositoryDocsCancellationCommitted = maintenance.repositoryDocsCancellationCommitted
	}
	return maintenance
}

func (m *MaintenanceManager) Load() error {
	m.mu.Lock()
	err := m.loadLocked()
	redirects, sourceRedirects, repoIDs := m.jobProjectionRedirectsLocked(), cloneStringMap(m.sourceRedirects), m.canonicalRepoIDsLocked()
	m.mu.Unlock()
	if err == nil && m.jobs != nil {
		m.jobs.SetRegistrationRedirects(redirects, sourceRedirects, repoIDs)
	}
	return err
}

func (m *MaintenanceManager) loadLocked() error {
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
	if disk.SchemaVersion != maintenanceRegistrySchema && disk.SchemaVersion != legacyMaintenanceRegistrySchema {
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
		entry.repositoryPath = stored.RepositoryPath
		entry.repositoryProfile = stored.RepositoryProfile
		entry.identityBlockedWasEnabled = stored.IdentityBlockedWasEnabled
		if entry.RepositoryDocs != nil {
			if entry.RepositoryDocs.SourceRegistrationID == "" {
				entry.RepositoryDocs.SourceRegistrationID = repositoryDocsSourceRegistrationID(entry.RegistrationID, entry.RepositoryDocs.GitStoreRef, entry.RepositoryDocs.WorktreeRef, entry.repositoryProfile)
			}
			if entry.RepositoryDocs.SourceRegistrationGeneration <= 0 {
				entry.RepositoryDocs.SourceRegistrationGeneration = 1
			}
		}
		loadID := entry.RegistrationID
		if _, exists := m.entries[loadID]; exists {
			// Legacy registries can contain the same logical registration for
			// multiple physical clones. Keep both rows until UUID/path
			// canonicalization constructs the lossless clone cohort.
			loadID = maintenanceDuplicateLoadID(entry.RegistrationID, stored.CachePath)
			for suffix := 2; m.entries[loadID] != nil; suffix++ {
				loadID = fmt.Sprintf("%s-%d", maintenanceDuplicateLoadID(entry.RegistrationID, stored.CachePath), suffix)
			}
		}
		m.entries[loadID] = &entry
		registeredSources := map[string]*repositoryDocsRegisteredSource{}
		for _, diskSource := range stored.RepositoryDocsSources {
			state := diskSource.State
			if state.SourceRegistrationID == "" || state.SourceRegistrationGeneration <= 0 || strings.TrimSpace(diskSource.RepositoryPath) == "" {
				continue
			}
			registeredSources[state.SourceRegistrationID] = &repositoryDocsRegisteredSource{State: state, RepositoryPath: diskSource.RepositoryPath, Profile: diskSource.Profile}
		}
		// Backward compatibility: v1 registries stored one authority directly
		// on the maintenance entry.
		if len(registeredSources) == 0 && entry.RepositoryDocs != nil && strings.TrimSpace(entry.repositoryPath) != "" {
			state := *entry.RepositoryDocs
			registeredSources[state.SourceRegistrationID] = &repositoryDocsRegisteredSource{State: state, RepositoryPath: entry.repositoryPath, Profile: entry.repositoryProfile}
		}
		m.sources[loadID] = registeredSources
		m.refreshLegacyRepositoryDocsForKeyLocked(&entry, loadID)
		if len(stored.IdentityConflictCandidates) > 0 {
			candidates := cloneMaintenanceConflictCandidates(stored.IdentityConflictCandidates)
			for index := range candidates {
				candidates[index].CandidateRef = maintenanceConflictCandidateRef(candidates[index])
			}
			sort.Slice(candidates, func(i, j int) bool { return candidates[i].CandidateRef < candidates[j].CandidateRef })
			m.conflictCandidates[loadID] = candidates
			if entry.IdentityConflict != nil {
				entry.IdentityConflict.DetailsAvailable = true
				entry.IdentityConflict.Candidates = publicMaintenanceConflictSelections(entry.IdentityConflict.Kind, candidates)
			}
		}
	}
	for _, receipt := range disk.Receipts {
		m.receipts[receipt.KeyHash] = receipt
	}
	for _, redirect := range disk.RegistrationRedirects {
		if redirect.From != "" && redirect.To != "" && redirect.From != redirect.To {
			m.redirects[redirect.From] = redirect.To
		}
	}
	for _, redirect := range disk.SourceRegistrationRedirects {
		if redirect.From != "" && redirect.To != "" && redirect.From != redirect.To {
			m.sourceRedirects[redirect.From] = redirect.To
		}
	}
	m.redirects = discardCyclicRedirects(m.redirects)
	m.sourceRedirects = discardCyclicRedirects(m.sourceRedirects)
	for _, receipt := range disk.ConflictResolutionReceipts {
		if receipt.KeyHash != "" {
			m.resolutionReceipts[receipt.KeyHash] = receipt
		}
	}
	for _, retired := range disk.RetiredClonePaths {
		if retired.CacheUUID == "" || retired.PathFingerprint == "" {
			continue
		}
		if m.retiredClonePaths[retired.CacheUUID] == nil {
			m.retiredClonePaths[retired.CacheUUID] = map[string]bool{}
		}
		m.retiredClonePaths[retired.CacheUUID][retired.PathFingerprint] = true
	}
	for _, admission := range disk.RepositoryDocsAdmissionQueue {
		if strings.TrimSpace(admission.RegistrationID) == "" || strings.TrimSpace(admission.SourceRegistrationID) == "" ||
			admission.SourceRegistrationGeneration <= 0 || strings.TrimSpace(admission.ExpectedRevisionSetID) == "" || strings.TrimSpace(admission.WorkKey) == "" {
			continue
		}
		key := repositoryDocsAdmissionKey(admission.RegistrationID, admission.SourceRegistrationID, admission.AuthorityPathFingerprint)
		if _, exists := m.admissions[key]; exists {
			admission.AuthorityPathFingerprint = legacyAmbiguousAdmissionAuthority(admission)
			key = repositoryDocsAdmissionKey(admission.RegistrationID, admission.SourceRegistrationID, admission.AuthorityPathFingerprint)
		}
		m.admissions[key] = admission
	}
	migrated := m.canonicalizeLoadedEntriesLocked(context.Background())
	if disk.SchemaVersion == legacyMaintenanceRegistrySchema || migrated {
		m.generation++
		if err := m.saveLocked(); err != nil {
			return err
		}
	}
	return nil
}

type maintenanceCanonicalCandidate struct {
	id      string
	entry   *MaintenanceEntry
	repoID  string
	aliases []string
	pathKey string
}

func (m *MaintenanceManager) canonicalizeLoadedEntriesLocked(ctx context.Context) bool {
	changed := m.expandRecoveredUnresolvedCandidatesLocked(ctx)
	groups := map[string][]maintenanceCanonicalCandidate{}
	unresolved := map[string][]maintenanceCanonicalCandidate{}
	uuidPaths := map[string]map[string]bool{}
	uuidCandidates := map[string][]maintenanceCanonicalCandidate{}
	ids := make([]string, 0, len(m.entries))
	for id, entry := range m.entries {
		ids = append(ids, id)
		if entry.IdentityConflict != nil {
			continue
		}
		pathKey := maintenanceCanonicalPathKey(entry.cachePath)
		if uuidPaths[entry.CacheUUID] == nil {
			uuidPaths[entry.CacheUUID] = map[string]bool{}
		}
		uuidPaths[entry.CacheUUID][pathKey] = true
	}
	sort.Strings(ids)
	for _, id := range ids {
		entry := m.entries[id]
		// A collapsed cohort must remain blocked until the dedicated resolution
		// workflow selects one retained private candidate. Re-running ordinary
		// canonicalization must never silently treat the synthetic row as a new
		// single candidate and clear the conflict.
		if entry.IdentityConflict != nil {
			continue
		}
		pathKey := maintenanceCanonicalPathKey(entry.cachePath)
		item := maintenanceCanonicalCandidate{id: id, entry: entry, repoID: entry.RepoID, pathKey: pathKey}
		store, err := cache.NewSQLiteReadOnlyStore(ctx, entry.cachePath)
		if err != nil {
			unresolved[entry.CacheUUID+"\x00"+pathKey] = append(unresolved[entry.CacheUUID+"\x00"+pathKey], item)
			uuidCandidates[entry.CacheUUID] = append(uuidCandidates[entry.CacheUUID], item)
			continue
		}
		identity, identityErr := store.CacheIdentity(ctx)
		binding, bindingErr := store.ResolveRepositoryBinding(ctx, entry.RepoID)
		_ = store.Close()
		if identityErr != nil || bindingErr != nil || identity.UUID != entry.CacheUUID {
			unresolved[entry.CacheUUID+"\x00"+pathKey] = append(unresolved[entry.CacheUUID+"\x00"+pathKey], item)
			uuidCandidates[entry.CacheUUID] = append(uuidCandidates[entry.CacheUUID], item)
			continue
		}
		item.repoID = binding.RepoID
		item.aliases = append([]string(nil), binding.Aliases...)
		snapshotPath, snapshotErr := canonicalCachePath(entry.configSnapshot.CachePath)
		if entry.ConfigHash == "" || entry.ConfigHash != maintenanceHash(entry.configSnapshot) || snapshotErr != nil || snapshotPath != maintenanceCanonicalPathKey(entry.cachePath) {
			changed = blockMaintenanceIdentity(entry, "config_snapshot_invalid") || changed
			// The snapshot cannot authorize work, but the physical cache still
			// belongs to this UUID cohort. Preserve it as private conflict
			// evidence so selecting another path retires this authority and a
			// later snapshot repair cannot resurrect an untracked clone.
			uuidCandidates[entry.CacheUUID] = append(uuidCandidates[entry.CacheUUID], item)
			continue
		}
		fingerprint := pathFingerprint(pathKey)
		if entry.PathFingerprint != "" && entry.PathFingerprint != fingerprint {
			unresolved[entry.CacheUUID+"\x00"+pathKey] = append(unresolved[entry.CacheUUID+"\x00"+pathKey], item)
			uuidCandidates[entry.CacheUUID] = append(uuidCandidates[entry.CacheUUID], item)
			changed = blockMaintenanceIdentity(entry, "identity_unresolved") || changed
			continue
		}
		if entry.PathFingerprint == "" {
			entry.PathFingerprint = fingerprint
			changed = true
		}
		if entry.State == "identity_unresolved" || entry.State == "config_snapshot_invalid" {
			entry.Enabled = entry.identityBlockedWasEnabled
			entry.identityBlockedWasEnabled = false
			entry.State, entry.LastErrorClass, entry.LastError = "enrolled", "", ""
			changed = true
		}
		uuidCandidates[entry.CacheUUID] = append(uuidCandidates[entry.CacheUUID], item)
		canonicalID := maintenanceRegistrationID(identity.UUID, binding.RepoID)
		groups[canonicalID] = append(groups[canonicalID], item)
	}

	cloneCandidates := map[string]bool{}
	for cacheUUID, paths := range uuidPaths {
		if len(paths) <= 1 {
			continue
		}
		candidates := uuidCandidates[cacheUUID]
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].id < candidates[j].id })
		if len(candidates) == 0 {
			continue
		}
		for _, candidate := range candidates {
			cloneCandidates[candidate.id] = true
		}
		registrationID := maintenanceCloneConflictRegistrationID(cacheUUID)
		changed = m.installMaintenanceConflictLocked(registrationID, candidates, "cache_clone_conflict", sortedMapKeys(paths)) || changed
	}

	unresolvedKeys := make([]string, 0, len(unresolved))
	for key := range unresolved {
		unresolvedKeys = append(unresolvedKeys, key)
	}
	sort.Strings(unresolvedKeys)
	for _, key := range unresolvedKeys {
		candidates := unresolved[key]
		filtered := candidates[:0]
		for _, candidate := range candidates {
			if !cloneCandidates[candidate.id] {
				filtered = append(filtered, candidate)
			}
		}
		candidates = filtered
		if len(candidates) == 0 {
			continue
		}
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].id < candidates[j].id })
		if len(candidates) == 1 {
			changed = blockMaintenanceIdentity(candidates[0].entry, "identity_unresolved") || changed
			continue
		}
		registrationID := maintenanceUnresolvedRegistrationID(candidates[0].entry.CacheUUID, candidates[0].pathKey)
		changed = m.installMaintenanceConflictLocked(registrationID, candidates, "identity_unresolved", nil) || changed
	}

	canonicalIDs := make([]string, 0, len(groups))
	for id := range groups {
		canonicalIDs = append(canonicalIDs, id)
	}
	sort.Strings(canonicalIDs)
	for _, canonicalID := range canonicalIDs {
		candidates := groups[canonicalID]
		filtered := candidates[:0]
		for _, candidate := range candidates {
			if !cloneCandidates[candidate.id] {
				filtered = append(filtered, candidate)
			}
		}
		candidates = filtered
		if len(candidates) == 0 {
			continue
		}
		sort.Slice(candidates, func(i, j int) bool { return candidates[i].id < candidates[j].id })
		base := candidates[0]
		for _, item := range candidates {
			if item.id == canonicalID {
				base = item
				break
			}
		}
		compatible := true
		for _, item := range candidates {
			if item.entry.Policy != base.entry.Policy || item.entry.ConfigHash != base.entry.ConfigHash || !maintenanceSourceSetsCompatible(m.sources[base.id], m.sources[item.id]) {
				compatible = false
			}
		}
		if !compatible {
			changed = m.installMaintenanceConflictLocked(canonicalID, candidates, "identity_conflict", nil) || changed
			continue
		}
		merged := cloneMaintenanceEntryPrivate(base.entry)
		merged.RegistrationID, merged.RepoID = canonicalID, candidates[0].repoID
		merged.Aliases = sortedUniqueStrings(candidates[0].aliases)
		merged.IdentityConflict = nil
		merged.LegacyRegistrationIDs = nil
		maxGeneration := merged.Generation
		for _, item := range candidates {
			if item.entry.Generation > maxGeneration {
				maxGeneration = item.entry.Generation
			}
			merged.LastSeenAt = laterTime(merged.LastSeenAt, item.entry.LastSeenAt)
			merged.LastReconciledAt = laterTime(merged.LastReconciledAt, item.entry.LastReconciledAt)
			merged.SyncStage = conservativeMaintenanceStage(merged.SyncStage, item.entry.SyncStage)
			merged.RAGStage = conservativeMaintenanceStage(merged.RAGStage, item.entry.RAGStage)
			if item.id != canonicalID {
				merged.LegacyRegistrationIDs = append(merged.LegacyRegistrationIDs, item.id)
				m.redirects[item.id] = canonicalID
			}
			for _, legacyID := range item.entry.LegacyRegistrationIDs {
				if legacyID != "" && legacyID != canonicalID {
					merged.LegacyRegistrationIDs = append(merged.LegacyRegistrationIDs, legacyID)
					m.redirects[legacyID] = canonicalID
				}
			}
		}
		merged.LegacyRegistrationIDs = sortedUniqueStrings(merged.LegacyRegistrationIDs)
		mergedSources := m.mergeMaintenanceSourcesLocked(canonicalID, candidates)
		if len(candidates) > 1 {
			merged.Generation = maxGeneration + 1
			merged.ActiveJobs = nil
			changed = true
		}
		if merged.RepoID != base.entry.RepoID || !stringSlicesEqual(merged.Aliases, base.entry.Aliases) || canonicalID != base.id {
			changed = true
		}
		for _, item := range candidates {
			delete(m.entries, item.id)
			delete(m.sources, item.id)
			delete(m.conflictCandidates, item.id)
		}
		m.entries[canonicalID], m.sources[canonicalID] = &merged, mergedSources
		delete(m.conflictCandidates, canonicalID)
		m.refreshLegacyRepositoryDocsLocked(&merged)
	}
	changed = m.normalizeRedirectsLocked() || changed
	for key, receipt := range m.receipts {
		canonicalID := m.resolveRegistrationIDLocked(receipt.RegistrationID)
		if strings.TrimSpace(receipt.AuthorityPathFingerprint) == "" {
			paths := map[string]bool{}
			for _, candidates := range m.conflictCandidates {
				for _, candidate := range candidates {
					if m.resolveRegistrationIDLocked(candidate.Entry.RegistrationID) != canonicalID {
						continue
					}
					if receipt.IntentHash != "" && receipt.IntentHash != maintenanceEnrollmentIntentHash(candidate.Entry.RegistrationID, candidate.Entry.Policy, candidate.Entry.ConfigHash) {
						continue
					}
					paths[pathFingerprint(maintenanceCanonicalPathKey(candidate.CachePath))] = true
				}
			}
			switch len(paths) {
			case 1:
				for fingerprint := range paths {
					receipt.AuthorityPathFingerprint = fingerprint
				}
			case 0:
				if entry := m.entries[canonicalID]; entry != nil {
					receipt.AuthorityPathFingerprint = entry.PathFingerprint
				}
			default:
				receipt.AuthorityPathFingerprint = legacyAmbiguousReceiptAuthority(receipt)
			}
			changed = true
		}
		if canonicalID != receipt.RegistrationID {
			receipt.RegistrationID = canonicalID
			// Preserve the original intent hash. Redirecting an idempotency key
			// must never authorize a policy/config intent it did not create.
			changed = true
		}
		m.receipts[key] = receipt
	}
	changed = m.remapRepositoryDocsAdmissionsLocked() || changed
	return changed
}

func (m *MaintenanceManager) installMaintenanceConflictLocked(registrationID string, candidates []maintenanceCanonicalCandidate, kind string, allPathKeys []string) bool {
	if len(candidates) == 0 {
		return false
	}
	base := candidates[0]
	for _, candidate := range candidates {
		if candidate.id == registrationID {
			base = candidate
			break
		}
	}
	merged := cloneMaintenanceEntryPrivate(base.entry)
	merged.RegistrationID = registrationID
	if candidates[0].repoID != "" {
		merged.RepoID = candidates[0].repoID
	}
	merged.Aliases = sortedUniqueStrings(candidates[0].aliases)
	merged.LegacyRegistrationIDs = nil
	merged.ActiveJobs = nil
	merged.Enabled, merged.State = false, kind
	merged.LastErrorClass, merged.LastError = kind, publicMaintenanceError(kind)
	maxGeneration := merged.Generation
	privateCandidates := make([]maintenanceIdentityConflictCandidate, 0, len(candidates))
	candidateIDs, policyHashes, configHashes, pathFingerprints := []string{}, []string{}, []string{}, []string{}
	for _, pathKey := range allPathKeys {
		pathFingerprints = append(pathFingerprints, pathFingerprint(pathKey))
	}
	for _, item := range candidates {
		if item.entry.Generation > maxGeneration {
			maxGeneration = item.entry.Generation
		}
		merged.LastSeenAt = laterTime(merged.LastSeenAt, item.entry.LastSeenAt)
		merged.LastReconciledAt = laterTime(merged.LastReconciledAt, item.entry.LastReconciledAt)
		merged.SyncStage = conservativeMaintenanceStage(merged.SyncStage, item.entry.SyncStage)
		merged.RAGStage = conservativeMaintenanceStage(merged.RAGStage, item.entry.RAGStage)
		private := maintenanceConflictCandidate(item.entry, m.sources[item.id])
		privateCandidates = append(privateCandidates, private)
		candidateIDs = append(candidateIDs, item.entry.RegistrationID)
		policyHashes = append(policyHashes, maintenanceHash(item.entry.Policy))
		configHashes = append(configHashes, item.entry.ConfigHash)
		pathFingerprints = append(pathFingerprints, pathFingerprint(maintenanceCanonicalPathKey(item.entry.cachePath)))
		legacyIDs := append([]string{item.entry.RegistrationID}, item.entry.LegacyRegistrationIDs...)
		for _, legacyID := range legacyIDs {
			legacyID = strings.TrimSpace(legacyID)
			if legacyID == "" || legacyID == registrationID {
				continue
			}
			merged.LegacyRegistrationIDs = append(merged.LegacyRegistrationIDs, legacyID)
			m.redirects[legacyID] = registrationID
		}
	}
	merged.Generation = maxGeneration + 1
	merged.LegacyRegistrationIDs = sortedUniqueStrings(merged.LegacyRegistrationIDs)
	sort.Slice(privateCandidates, func(i, j int) bool { return privateCandidates[i].CandidateRef < privateCandidates[j].CandidateRef })
	publicCandidates := publicMaintenanceConflictSelections(kind, privateCandidates)
	merged.IdentityConflict = &MaintenanceIdentityConflict{Kind: kind, DetailsAvailable: true, CandidateRegistrationIDs: sortedUniqueStrings(candidateIDs), PolicyHashes: sortedUniqueStrings(policyHashes), ConfigHashes: sortedUniqueStrings(configHashes), PathFingerprints: sortedUniqueStrings(pathFingerprints), Candidates: publicCandidates}
	baseSources := cloneRepositoryDocsSources(m.sources[base.id])
	for _, item := range candidates {
		delete(m.entries, item.id)
		delete(m.sources, item.id)
		delete(m.conflictCandidates, item.id)
	}
	m.entries[registrationID] = &merged
	m.sources[registrationID] = baseSources
	m.conflictCandidates[registrationID] = privateCandidates
	m.refreshLegacyRepositoryDocsLocked(&merged)
	return true
}

func maintenanceConflictCandidate(entry *MaintenanceEntry, sources map[string]*repositoryDocsRegisteredSource) maintenanceIdentityConflictCandidate {
	privateEntry := cloneMaintenanceEntryPrivate(entry)
	privateEntry.IdentityConflict = nil
	private := maintenanceIdentityConflictCandidate{Entry: privateEntry, CachePath: entry.cachePath, ConfigReference: entry.configReference, ConfigSnapshot: entry.configSnapshot, RepositoryPath: entry.repositoryPath, RepositoryProfile: entry.repositoryProfile, WasEnabled: entry.Enabled}
	ids := make([]string, 0, len(sources))
	for id := range sources {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if source := sources[id]; source != nil {
			private.RepositorySources = append(private.RepositorySources, repositoryDocsDiskSource{State: source.State, RepositoryPath: source.RepositoryPath, Profile: source.Profile})
		}
	}
	private.CandidateRef = maintenanceConflictCandidateRef(private)
	return private
}

func publicMaintenanceConflictCandidate(private maintenanceIdentityConflictCandidate) MaintenanceIdentityCandidate {
	refs := make([]string, 0, len(private.RepositorySources))
	for _, source := range private.RepositorySources {
		refs = append(refs, source.State.SourceRegistrationID)
	}
	return MaintenanceIdentityCandidate{CandidateRef: private.CandidateRef, RegistrationID: private.Entry.RegistrationID, RepoID: private.Entry.RepoID, Policy: private.Entry.Policy, PolicyHash: maintenanceHash(private.Entry.Policy), ConfigHash: private.Entry.ConfigHash, PathFingerprint: pathFingerprint(maintenanceCanonicalPathKey(private.CachePath)), SourceAuthorityHash: maintenanceDiskSourceAuthorityHash(private.RepositorySources), SourceRefs: sortedUniqueStrings(refs), WasEnabled: private.WasEnabled}
}

func maintenanceConflictCandidateRef(private maintenanceIdentityConflictCandidate) string {
	payload := struct {
		RegistrationID      string `json:"registration_id"`
		RepoID              string `json:"repo_id"`
		PathFingerprint     string `json:"path_fingerprint"`
		PolicyHash          string `json:"policy_hash"`
		ConfigHash          string `json:"config_hash"`
		SourceAuthorityHash string `json:"source_authority_hash"`
		ConfigSnapshotHash  string `json:"config_snapshot_hash"`
	}{private.Entry.RegistrationID, private.Entry.RepoID, pathFingerprint(maintenanceCanonicalPathKey(private.CachePath)), maintenanceHash(private.Entry.Policy), private.Entry.ConfigHash, maintenanceDiskSourceAuthorityHash(private.RepositorySources), maintenanceHash(private.ConfigSnapshot)}
	hash := strings.TrimPrefix(maintenanceHash(payload), "sha256:")
	return "conflict-candidate-" + hash[:16]
}

func maintenanceDuplicateLoadID(registrationID, cachePath string) string {
	hash := strings.TrimPrefix(maintenanceHash(struct {
		RegistrationID  string `json:"registration_id"`
		PathFingerprint string `json:"path_fingerprint"`
	}{registrationID, pathFingerprint(maintenanceCanonicalPathKey(cachePath))}), "sha256:")
	return "maintenance-load-candidate-" + hash[:16]
}

func (m *MaintenanceManager) remapRepositoryDocsAdmissionsLocked() bool {
	if len(m.admissions) == 0 {
		return false
	}
	changed := false
	registrationRedirects := m.jobProjectionRedirectsLocked()
	next := make(map[string]repositoryDocsAdmissionIntent, len(m.admissions))
	keys := make([]string, 0, len(m.admissions))
	for key := range m.admissions {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		intent := m.admissions[key]
		registrationID := resolveMaintenanceRedirect(intent.RegistrationID, registrationRedirects)
		sourceID := m.resolveSourceRegistrationIDLocked(intent.SourceRegistrationID)
		if registrationID != intent.RegistrationID || sourceID != intent.SourceRegistrationID {
			intent.RegistrationID, intent.SourceRegistrationID = registrationID, sourceID
			changed = true
		}
		if strings.TrimSpace(intent.AuthorityPathFingerprint) == "" {
			paths := map[string]bool{}
			for _, candidates := range m.conflictCandidates {
				for _, candidate := range candidates {
					if resolveMaintenanceRedirect(candidate.Entry.RegistrationID, registrationRedirects) != intent.RegistrationID {
						continue
					}
					for _, source := range candidate.RepositorySources {
						if m.resolveSourceRegistrationIDLocked(source.State.SourceRegistrationID) == intent.SourceRegistrationID {
							paths[pathFingerprint(maintenanceCanonicalPathKey(candidate.CachePath))] = true
						}
					}
				}
			}
			if len(paths) == 1 {
				for fingerprint := range paths {
					intent.AuthorityPathFingerprint = fingerprint
				}
				changed = true
			} else if len(paths) > 1 {
				intent.AuthorityPathFingerprint = legacyAmbiguousAdmissionAuthority(intent)
				changed = true
			} else if entry := m.entries[intent.RegistrationID]; entry != nil && entry.PathFingerprint != "" {
				intent.AuthorityPathFingerprint = entry.PathFingerprint
				changed = true
			}
		}
		mappedKey := repositoryDocsAdmissionKey(intent.RegistrationID, intent.SourceRegistrationID, intent.AuthorityPathFingerprint)
		if previous, exists := next[mappedKey]; exists {
			// Cancellation is the stronger durable disposition. Otherwise keep
			// the newest exact admission deterministically.
			if previous.Disposition == repositoryDocsAdmissionCancelled && intent.Disposition != repositoryDocsAdmissionCancelled {
				continue
			}
			if previous.Disposition == intent.Disposition && !intent.CreatedAt.After(previous.CreatedAt) {
				continue
			}
			changed = true
		}
		next[mappedKey] = intent
	}
	m.admissions = next
	return changed
}

func legacyAmbiguousAdmissionAuthority(intent repositoryDocsAdmissionIntent) string {
	hash := strings.TrimPrefix(maintenanceHash(struct {
		WorkKey   string `json:"work_key"`
		SetID     string `json:"set_id"`
		JobID     string `json:"job_id"`
		CreatedAt string `json:"created_at"`
	}{intent.WorkKey, intent.ExpectedRevisionSetID, intent.JobID, intent.CreatedAt.UTC().Format(time.RFC3339Nano)}), "sha256:")
	return "legacy-ambiguous-" + hash[:16]
}

func legacyAmbiguousReceiptAuthority(receipt maintenanceReceipt) string {
	hash := strings.TrimPrefix(maintenanceHash(struct {
		KeyHash        string `json:"key_hash"`
		RegistrationID string `json:"registration_id"`
		IntentHash     string `json:"intent_hash"`
	}{receipt.KeyHash, receipt.RegistrationID, receipt.IntentHash}), "sha256:")
	return "legacy-ambiguous-" + hash[:16]
}

func (m *MaintenanceManager) expandRecoveredUnresolvedCandidatesLocked(ctx context.Context) bool {
	changed := false
	for registrationID, entry := range m.entries {
		candidates := m.conflictCandidates[registrationID]
		if entry == nil || entry.State != "identity_unresolved" || len(candidates) == 0 {
			continue
		}
		store, err := cache.NewSQLiteReadOnlyStore(ctx, candidates[0].CachePath)
		if err != nil {
			continue
		}
		_ = store.Close()
		delete(m.entries, registrationID)
		delete(m.sources, registrationID)
		delete(m.conflictCandidates, registrationID)
		for from, to := range m.redirects {
			if to == registrationID {
				delete(m.redirects, from)
			}
		}
		for _, candidate := range candidates {
			restored := cloneMaintenanceEntryPrivate(&candidate.Entry)
			restored.cachePath, restored.configReference, restored.configSnapshot = candidate.CachePath, candidate.ConfigReference, candidate.ConfigSnapshot
			restored.repositoryPath, restored.repositoryProfile = candidate.RepositoryPath, candidate.RepositoryProfile
			restored.IdentityConflict = nil
			m.entries[restored.RegistrationID] = &restored
			sources := map[string]*repositoryDocsRegisteredSource{}
			for _, diskSource := range candidate.RepositorySources {
				copy := diskSource
				sources[copy.State.SourceRegistrationID] = &repositoryDocsRegisteredSource{State: copy.State, RepositoryPath: copy.RepositoryPath, Profile: copy.Profile}
			}
			m.sources[restored.RegistrationID] = sources
		}
		changed = true
	}
	return changed
}

func blockMaintenanceIdentity(entry *MaintenanceEntry, state string) bool {
	if entry == nil {
		return false
	}
	changed := entry.State != state || entry.Enabled || entry.LastErrorClass != state
	if entry.State != state {
		entry.identityBlockedWasEnabled = entry.Enabled
	}
	entry.Enabled = false
	entry.State = state
	entry.LastErrorClass = state
	entry.LastError = publicMaintenanceError(state)
	return changed
}

func maintenanceSourceSetsCompatible(left, right map[string]*repositoryDocsRegisteredSource) bool {
	if len(left) == 0 || len(right) == 0 {
		return true
	}
	return maintenanceSourceAuthorityHash(left) == maintenanceSourceAuthorityHash(right)
}

func maintenanceSourceAuthorityHash(sources map[string]*repositoryDocsRegisteredSource) string {
	disk := make([]repositoryDocsDiskSource, 0, len(sources))
	for _, source := range sources {
		if source != nil {
			disk = append(disk, repositoryDocsDiskSource{State: source.State, RepositoryPath: source.RepositoryPath, Profile: source.Profile})
		}
	}
	return maintenanceDiskSourceAuthorityHash(disk)
}

func maintenanceDiskSourceAuthorityHash(sources []repositoryDocsDiskSource) string {
	type authority struct {
		GitStoreRef     string `json:"git_store_ref"`
		WorktreeRef     string `json:"worktree_ref"`
		Profile         string `json:"profile"`
		PathFingerprint string `json:"path_fingerprint"`
	}
	values := make([]authority, 0, len(sources))
	for _, source := range sources {
		values = append(values, authority{source.State.GitStoreRef, source.State.WorktreeRef, source.Profile, pathFingerprint(maintenanceCanonicalPathKey(source.RepositoryPath))})
	}
	sort.Slice(values, func(i, j int) bool { return maintenanceHash(values[i]) < maintenanceHash(values[j]) })
	return maintenanceHash(values)
}

func (m *MaintenanceManager) mergeMaintenanceSourcesLocked(canonicalID string, candidates []maintenanceCanonicalCandidate) map[string]*repositoryDocsRegisteredSource {
	byAuthority := map[string][]*repositoryDocsRegisteredSource{}
	for _, item := range candidates {
		for _, source := range m.sources[item.id] {
			if source != nil {
				key := maintenanceDiskSourceAuthorityHash([]repositoryDocsDiskSource{{State: source.State, RepositoryPath: source.RepositoryPath, Profile: source.Profile}})
				copy := *source
				byAuthority[key] = append(byAuthority[key], &copy)
			}
		}
	}
	out := map[string]*repositoryDocsRegisteredSource{}
	for _, sources := range byAuthority {
		sort.Slice(sources, func(i, j int) bool {
			return sources[i].State.SourceRegistrationID < sources[j].State.SourceRegistrationID
		})
		selected := *sources[0]
		maxGeneration := selected.State.SourceRegistrationGeneration
		for _, source := range sources[1:] {
			if source.State.UpdatedAt.After(selected.State.UpdatedAt) {
				selected = *source
			}
			selected.State.Stage = conservativeMaintenanceStage(selected.State.Stage, source.State.Stage)
			if source.State.SourceRegistrationGeneration > maxGeneration {
				maxGeneration = source.State.SourceRegistrationGeneration
			}
		}
		canonicalSourceID := repositoryDocsSourceRegistrationID(canonicalID, selected.State.GitStoreRef, selected.State.WorktreeRef, selected.Profile)
		for _, source := range sources {
			if source.State.SourceRegistrationID != canonicalSourceID {
				m.sourceRedirects[source.State.SourceRegistrationID] = canonicalSourceID
			}
		}
		if len(sources) > 1 {
			maxGeneration++
		}
		selected.State.SourceRegistrationID = canonicalSourceID
		selected.State.SourceRegistrationGeneration = maxGeneration
		out[canonicalSourceID] = &selected
	}
	return out
}

func maintenanceCanonicalPathKey(path string) string {
	if resolved, err := filepath.EvalSymlinks(strings.TrimSpace(path)); err == nil {
		if absolute, absErr := filepath.Abs(resolved); absErr == nil {
			return filepath.Clean(absolute)
		}
	}
	if absolute, err := filepath.Abs(strings.TrimSpace(path)); err == nil {
		return filepath.Clean(absolute)
	}
	return filepath.Clean(strings.TrimSpace(path))
}

func maintenanceUnresolvedRegistrationID(cacheUUID, pathKey string) string {
	sum := sha256.Sum256([]byte("unresolved\x00" + cacheUUID + "\x00" + pathKey))
	return "cache-unresolved-" + hex.EncodeToString(sum[:8])
}

func maintenanceCloneConflictRegistrationID(cacheUUID string) string {
	sum := sha256.Sum256([]byte("clone-conflict\x00" + cacheUUID))
	return "cache-clone-conflict-" + hex.EncodeToString(sum[:8])
}

func sortedMapKeys(values map[string]bool) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func conservativeMaintenanceStage(left, right MaintenanceStageState) MaintenanceStageState {
	selected := left
	if right.UpdatedAt.After(left.UpdatedAt) {
		selected = right
	}
	if maintenanceStageFailed(left) && maintenanceStageFailed(right) {
		if right.RetryAfter.After(selected.RetryAfter) {
			selected.RetryAfter = right.RetryAfter
		}
		if left.RetryAfter.After(selected.RetryAfter) {
			selected.RetryAfter = left.RetryAfter
		}
		if right.ConsecutiveFailures > selected.ConsecutiveFailures {
			selected.ConsecutiveFailures = right.ConsecutiveFailures
		}
		if left.ConsecutiveFailures > selected.ConsecutiveFailures {
			selected.ConsecutiveFailures = left.ConsecutiveFailures
		}
	}
	return selected
}

func maintenanceStageFailed(state MaintenanceStageState) bool {
	return state.LastErrorClass != "" || state.Status == JobStatusFailed || state.Status == "retry_scheduled"
}

func laterTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}

func sortedUniqueStrings(values []string) []string {
	seen := map[string]bool{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			seen[value] = true
		}
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func stringSlicesEqual(left, right []string) bool {
	return strings.Join(sortedUniqueStrings(left), "\x00") == strings.Join(sortedUniqueStrings(right), "\x00")
}

func (m *MaintenanceManager) resolveRegistrationIDLocked(value string) string {
	return resolveMaintenanceRedirect(value, m.redirects)
}

func resolveMaintenanceRedirect(value string, redirects map[string]string) string {
	value = strings.TrimSpace(value)
	seen := map[string]bool{}
	for redirects[value] != "" && !seen[value] {
		seen[value] = true
		value = redirects[value]
	}
	return value
}

func discardCyclicRedirects(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for from, to := range in {
		seen := map[string]bool{}
		value := from
		cyclic := false
		for value != "" {
			if seen[value] {
				cyclic = true
				break
			}
			seen[value] = true
			value = in[value]
		}
		if !cyclic {
			out[from] = to
		}
	}
	return out
}

// normalizeRedirectsLocked flattens every public selector onto an authority
// that still exists after canonicalization. Redirects from a current authority
// are discarded, so a newly introduced old -> new -> old cycle converges on
// the retained row instead of surviving into durable state.
func (m *MaintenanceManager) normalizeRedirectsLocked() bool {
	registrationAuthorities := make(map[string]bool, len(m.entries))
	for registrationID := range m.entries {
		registrationAuthorities[registrationID] = true
	}
	sourceAuthorities := map[string]bool{}
	for _, sources := range m.sources {
		for sourceID := range sources {
			sourceAuthorities[sourceID] = true
		}
	}
	registrations := normalizeMaintenanceRedirects(m.redirects, registrationAuthorities)
	sources := normalizeMaintenanceRedirects(m.sourceRedirects, sourceAuthorities)
	changed := !stringMapsEqual(m.redirects, registrations) || !stringMapsEqual(m.sourceRedirects, sources)
	m.redirects, m.sourceRedirects = registrations, sources
	return changed
}

func normalizeMaintenanceRedirects(in map[string]string, authorities map[string]bool) map[string]string {
	out := map[string]string{}
	keys := make([]string, 0, len(in))
	for key := range in {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, from := range keys {
		from = strings.TrimSpace(from)
		if from == "" || authorities[from] {
			continue
		}
		value := from
		seen := map[string]bool{}
		for value != "" && !seen[value] && !authorities[value] {
			seen[value] = true
			value = strings.TrimSpace(in[value])
		}
		if authorities[value] && value != from {
			out[from] = value
		}
	}
	return out
}

func stringMapsEqual(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func (m *MaintenanceManager) resolveSourceRegistrationIDLocked(value string) string {
	return resolveMaintenanceRedirect(value, m.sourceRedirects)
}

func (m *MaintenanceManager) canonicalRepoIDsLocked() map[string]string {
	result := map[string]string{}
	for registrationID, entry := range m.entries {
		if entry != nil {
			result[registrationID] = entry.RepoID
		}
	}
	return result
}

// jobProjectionRedirectsLocked excludes synthetic clone-conflict rows. Those
// rows are operator-selection cohorts, not repository identities, and must
// never relabel durable historical jobs from distinct repositories.
func (m *MaintenanceManager) jobProjectionRedirectsLocked() map[string]string {
	result := map[string]string{}
	for from, to := range m.redirects {
		resolved := m.resolveRegistrationIDLocked(to)
		if entry := m.entries[resolved]; entry != nil && entry.IdentityConflict != nil && entry.IdentityConflict.Kind == "cache_clone_conflict" {
			continue
		}
		result[from] = to
	}
	return result
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
	if req.ConfigHash == "" || req.ConfigHash != maintenanceHash(req.ConfigSnapshot) {
		return MaintenanceEntry{}, errors.New("maintenance: config snapshot hash mismatch")
	}
	snapshotPath, snapshotErr := canonicalCachePath(req.ConfigSnapshot.CachePath)
	if snapshotErr != nil || snapshotPath != path {
		return MaintenanceEntry{}, errors.New("maintenance: config snapshot cache authority mismatch")
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
	binding, err := store.ResolveRepositoryBinding(ctx, req.RepoID)
	if err != nil {
		return MaintenanceEntry{}, fmt.Errorf("maintenance: repository %q is not bound in selected cache", req.RepoID)
	}
	req.RepoID = binding.RepoID
	policy, err := normalizeMaintenancePolicy(req.Policy, binding)
	if err != nil {
		return MaintenanceEntry{}, err
	}
	registrationID := maintenanceRegistrationID(identity.UUID, req.RepoID)
	keyHash := maintenanceIdempotencyKeyHash(req.IdempotencyKey)
	now := m.now()
	var redirectSnapshot, sourceRedirectSnapshot, repoIDSnapshot map[string]string
	m.mu.Lock()
	defer func() {
		m.mu.Unlock()
		if redirectSnapshot != nil && m.jobs != nil {
			m.jobs.SetRegistrationRedirects(redirectSnapshot, sourceRedirectSnapshot, repoIDSnapshot)
		}
	}()
	canonicalizationSnapshot := m.snapshotConflictMutationLocked()
	if m.canonicalizeLoadedEntriesLocked(ctx) {
		m.generation++
		if err := m.saveLocked(); err != nil {
			m.restoreConflictMutationLocked(canonicalizationSnapshot)
			return MaintenanceEntry{}, err
		}
		redirectSnapshot, sourceRedirectSnapshot, repoIDSnapshot = m.jobProjectionRedirectsLocked(), cloneStringMap(m.sourceRedirects), m.canonicalRepoIDsLocked()
	}
	if m.isRetiredClonePathLocked(identity.UUID, path) {
		return MaintenanceEntry{}, MaintenanceConflictResolutionError{code: "cache_clone_retired"}
	}
	registrationID = m.resolveRegistrationIDLocked(registrationID)
	intentHash := maintenanceEnrollmentIntentHash(registrationID, policy, req.ConfigHash)
	existing := m.entries[registrationID]
	if existing != nil && existing.IdentityConflict != nil {
		return MaintenanceEntry{}, MaintenanceIdentityConflictError{kind: existing.IdentityConflict.Kind}
	}
	if receipt, ok := m.receipts[keyHash]; ok {
		if receipt.RegistrationID != registrationID ||
			(receipt.AuthorityPathFingerprint != "" && receipt.AuthorityPathFingerprint != pathFingerprint(path)) ||
			(receipt.IntentHash != "" && !maintenanceReceiptAcceptsIntent(receipt, intentHash, existing)) {
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
			m.receipts[keyHash] = maintenanceReceipt{KeyHash: keyHash, RegistrationID: registrationID, IntentHash: intentHash, AuthorityPathFingerprint: pathFingerprint(path)}
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
		m.receipts[keyHash] = maintenanceReceipt{KeyHash: keyHash, RegistrationID: registrationID, IntentHash: intentHash, AuthorityPathFingerprint: pathFingerprint(path)}
		m.generation++
		if err := m.saveLocked(); err != nil {
			*existing = previous
			delete(m.receipts, keyHash)
			m.generation--
			return MaintenanceEntry{}, err
		}
		return cloneMaintenanceEntry(existing), nil
	}
	entry := &MaintenanceEntry{RegistrationID: registrationID, CacheUUID: identity.UUID, PathFingerprint: pathFingerprint(path), RepoID: req.RepoID, Aliases: append([]string(nil), binding.Aliases...), Policy: policy, ConfigHash: strings.TrimSpace(req.ConfigHash), Enabled: true, Generation: 1, State: "enrolled", LastSeenAt: now, cachePath: path, configReference: strings.TrimSpace(req.ConfigReference), configSnapshot: req.ConfigSnapshot}
	m.entries[registrationID] = entry
	m.receipts[keyHash] = maintenanceReceipt{KeyHash: keyHash, RegistrationID: registrationID, IntentHash: intentHash, AuthorityPathFingerprint: pathFingerprint(path)}
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
	entry := m.entries[m.resolveRegistrationIDLocked(registrationID)]
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
	registrationID = m.resolveRegistrationIDLocked(registrationID)
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

// RetrySyncCollection starts one explicit collection/frontier without
// advancing or replaying unrelated successful collections.
func (m *MaintenanceManager) RetrySyncCollection(ctx context.Context, registrationID, collection string) (MaintenanceReconcileResult, error) {
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()
	registrationID, collection = strings.TrimSpace(registrationID), strings.TrimSpace(collection)
	if registrationID == "" || collection == "" {
		return MaintenanceReconcileResult{}, errors.New("maintenance: registration_id and collection are required")
	}
	m.mu.Lock()
	registrationID = m.resolveRegistrationIDLocked(registrationID)
	registered := m.entries[registrationID]
	snapshot := cloneMaintenanceEntryPrivate(registered)
	m.mu.Unlock()
	if registered == nil {
		return MaintenanceReconcileResult{}, errors.New("maintenance: registration not found")
	}
	if !snapshot.Enabled || snapshot.IdentityConflict != nil {
		return MaintenanceReconcileResult{}, errors.New("maintenance: registration is not available for collection retry")
	}
	remoteType, selected, ok := maintenanceCollectionRetrySelection(snapshot.Policy, collection)
	if !ok {
		return MaintenanceReconcileResult{}, errors.New("maintenance: collection is not enabled by policy")
	}
	store, err := cache.NewSQLiteReadOnlyStore(ctx, snapshot.cachePath)
	if err != nil {
		return MaintenanceReconcileResult{}, err
	}
	frontiers, frontierErr := store.ListMaintenanceFrontiers(ctx, snapshot.RepoID)
	store.Close()
	if frontierErr != nil {
		return MaintenanceReconcileResult{}, frontierErr
	}
	page := 1
	for _, frontier := range frontiers {
		if frontier.RemoteType == remoteType && frontier.Lane == "head" {
			if checkpoint := checkpointPage(frontier.Checkpoint); checkpoint > 0 {
				page = checkpoint
			}
			break
		}
	}
	selected.RepoID, selected.ProviderMode, selected.CachePath = snapshot.RepoID, "live", snapshot.cachePath
	selected.CacheUUID, selected.RegistrationID, selected.Lane = snapshot.CacheUUID, snapshot.RegistrationID, "head"
	selected.MaxPages, selected.PerPage, selected.Page = 1, snapshot.Policy.PerPage, page
	selected.ActionIntentRef = jobActionIntentFromContext(ctx)
	selected.collectionPages = map[string]int{remoteType: page}
	selected.IdempotencyKey = fmt.Sprintf("maintenance-collection-retry-%s-%s-%d", snapshot.RegistrationID, remoteType, m.now().Unix()/60)
	jobManager := m.manager
	if snapshot.ConfigHash != "" {
		jobManager.EffectiveConfig = &snapshot.configSnapshot
	}
	job, created, err := m.jobs.startSync(context.Background(), jobManager, selected)
	if err != nil {
		return MaintenanceReconcileResult{}, err
	}
	result := MaintenanceReconcileResult{Entries: []MaintenanceEntry{cloneMaintenanceEntry(&snapshot)}, CheckedAt: m.now()}
	if created {
		result.JobsStarted = []string{job.ID}
	} else {
		result.JobsCoalesced = []string{job.ID}
	}
	return result, nil
}

func maintenanceCollectionRetrySelection(policy MaintenancePolicy, collection string) (string, StartSyncJobRequest, bool) {
	selection := StartSyncJobRequest{}
	switch collection {
	case "issues":
		selection.Issues = true
		return "issue", selection, policy.Issues
	case "issue_comments":
		selection.IssueComments = true
		return "issue_comment", selection, policy.IssueComments
	case "wiki":
		selection.Wiki = true
		return "wiki", selection, policy.Wiki
	case "pulls":
		selection.Pulls = true
		return "pull_request", selection, policy.Pulls
	case "pr_comments":
		selection.PRComments = true
		return "pr_comment", selection, policy.PRComments
	default:
		return "", selection, false
	}
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

// RegisterRepositoryDocsSource attaches a local Git worktree to an existing
// cache/repository maintenance registration. The path stays only in the 0600
// daemon registry; public state carries opaque Git identities.
func (m *MaintenanceManager) RegisterRepositoryDocsSource(ctx context.Context, cacheUUID, repoID, repositoryPath, profile string) (MaintenanceEntry, bool, error) {
	resolved, err := resolveRepositoryDocsSource(ctx, repoID, repositoryPath, profile, repositorydocs.PolicyRequest{RepoID: repoID})
	if err != nil {
		return MaintenanceEntry{}, false, err
	}
	return m.registerResolvedRepositoryDocsSource(cacheUUID, repoID, resolved)
}

type resolvedRepositoryDocsSource struct {
	repositoryPath string
	profile        string
	repo           *repositorydocs.Repository
	policy         repositorydocs.PolicyResult
}

func resolveRepositoryDocsSource(ctx context.Context, repoID, repositoryPath, profile string, policyRequest repositorydocs.PolicyRequest) (resolvedRepositoryDocsSource, error) {
	repositoryPath = strings.TrimSpace(repositoryPath)
	if repositoryPath == "" {
		return resolvedRepositoryDocsSource{}, RepositoryDocsSourceUnavailableError{}
	}
	absPath, err := filepath.Abs(repositoryPath)
	if err != nil {
		return resolvedRepositoryDocsSource{}, RepositoryDocsSourceUnavailableError{}
	}
	repo, err := repositorydocs.OpenRepository(ctx, absPath)
	if err != nil {
		return resolvedRepositoryDocsSource{}, RepositoryDocsSourceUnavailableError{}
	}
	policyRequest.RepoID = repoID
	policy, err := repositorydocs.InspectPolicy(ctx, repo, policyRequest)
	if err != nil {
		return resolvedRepositoryDocsSource{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_source_invalid"}
	}
	return resolvedRepositoryDocsSource{repositoryPath: absPath, profile: strings.TrimSpace(profile), repo: repo, policy: policy}, nil
}

func (m *MaintenanceManager) registerResolvedRepositoryDocsSource(cacheUUID, repoID string, resolved resolvedRepositoryDocsSource) (MaintenanceEntry, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.maintenanceEntryForCacheRepoLocked(cacheUUID, repoID)
	if entry == nil {
		return MaintenanceEntry{}, false, nil
	}
	previousEntry := cloneMaintenanceEntryPrivate(entry)
	previousSources := cloneRepositoryDocsSources(m.sources[entry.RegistrationID])
	previousManagerGeneration := m.generation
	source, registered, err := m.applyResolvedRepositoryDocsSourceLocked(entry, resolved)
	if err != nil || !registered {
		return MaintenanceEntry{}, registered, err
	}
	if err := m.saveLocked(); err != nil {
		restoreMaintenanceEntry(entry, previousEntry)
		m.sources[entry.RegistrationID] = previousSources
		m.generation = previousManagerGeneration
		return MaintenanceEntry{}, false, RepositoryDocsSourceUnavailableError{code: "repository_docs_registration_persist_failed"}
	}
	return cloneMaintenanceEntryForSource(entry, source), true, nil
}

func (m *MaintenanceManager) maintenanceEntryForCacheRepoLocked(cacheUUID, repoID string) *MaintenanceEntry {
	for _, entry := range m.entries {
		if entry.CacheUUID == cacheUUID && entry.RepoID == repoID {
			return entry
		}
	}
	return nil
}

func (m *MaintenanceManager) applyResolvedRepositoryDocsSourceLocked(entry *MaintenanceEntry, resolved resolvedRepositoryDocsSource) (*repositoryDocsRegisteredSource, bool, error) {
	if entry == nil {
		return nil, false, nil
	}
	if m.sources[entry.RegistrationID] == nil {
		m.sources[entry.RegistrationID] = map[string]*repositoryDocsRegisteredSource{}
	}
	sourceID := repositoryDocsSourceRegistrationID(entry.RegistrationID, resolved.policy.GitStoreRef, resolved.policy.WorktreeRef, resolved.profile)
	source := m.sources[entry.RegistrationID][sourceID]
	if source != nil {
		if source.Profile != resolved.profile || source.State.GitStoreRef != resolved.policy.GitStoreRef || source.State.WorktreeRef != resolved.policy.WorktreeRef {
			return nil, false, RepositoryDocsSourceConflictError{}
		}
		source.RepositoryPath = resolved.repositoryPath
		source.State.CommitOID = resolved.policy.CommitOID
		source.State.PolicyHash = resolved.policy.Policy.PolicyHash
		source.State.UpdatedAt = m.now()
		source.State.NextPollAt = m.now()
		m.refreshLegacyRepositoryDocsLocked(entry)
		return source, true, nil
	}
	source = &repositoryDocsRegisteredSource{
		RepositoryPath: resolved.repositoryPath,
		Profile:        resolved.profile,
		State: RepositoryDocsMaintenanceState{
			SourceRegistrationID: sourceID, SourceRegistrationGeneration: 1,
			GitStoreRef: resolved.policy.GitStoreRef, WorktreeRef: resolved.policy.WorktreeRef,
			CommitOID: resolved.policy.CommitOID, PolicyHash: resolved.policy.Policy.PolicyHash,
			State: "registered", UpdatedAt: m.now(), NextPollAt: m.now(),
		},
	}
	m.sources[entry.RegistrationID][sourceID] = source
	entry.Generation++
	m.generation++
	m.refreshLegacyRepositoryDocsLocked(entry)
	return source, true, nil
}

func (m *MaintenanceManager) registerAndRecordRepositoryDocsAdmission(prepared preparedRepositoryDocsIndex) (MaintenanceEntry, preparedRepositoryDocsIndex, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	resolved := resolvedRepositoryDocsSource{repositoryPath: prepared.request.RepositoryPath, profile: prepared.request.Profile, repo: prepared.repository, policy: prepared.policy}
	entry := m.maintenanceEntryForCacheRepoLocked(prepared.request.CacheUUID, prepared.request.RepoID)
	if entry == nil {
		return MaintenanceEntry{}, preparedRepositoryDocsIndex{}, false, nil
	}
	previousEntry := cloneMaintenanceEntryPrivate(entry)
	previousSources := cloneRepositoryDocsSources(m.sources[entry.RegistrationID])
	previousManagerGeneration := m.generation
	requestedSourceID := strings.TrimSpace(prepared.request.SourceRegistrationID)
	if requestedSourceID != "" {
		existing := m.sources[entry.RegistrationID][requestedSourceID]
		if existing == nil {
			return MaintenanceEntry{}, preparedRepositoryDocsIndex{}, false, RepositoryDocsSourceUnavailableError{code: "repository_docs_source_not_registered"}
		}
		if prepared.request.SourceRegistrationGeneration != existing.State.SourceRegistrationGeneration {
			return MaintenanceEntry{}, preparedRepositoryDocsIndex{}, false, RepositoryDocsSourceGenerationConflictError{}
		}
		resolvedID := repositoryDocsSourceRegistrationID(entry.RegistrationID, resolved.policy.GitStoreRef, resolved.policy.WorktreeRef, resolved.profile)
		if resolvedID != requestedSourceID {
			return MaintenanceEntry{}, preparedRepositoryDocsIndex{}, false, RepositoryDocsSourceConflictError{}
		}
	}
	source, registered, err := m.applyResolvedRepositoryDocsSourceLocked(entry, resolved)
	if err != nil || !registered {
		return MaintenanceEntry{}, preparedRepositoryDocsIndex{}, registered, err
	}
	prepared.request.RegistrationID = entry.RegistrationID
	prepared.request.SourceRegistrationID = source.State.SourceRegistrationID
	prepared.request.SourceRegistrationGeneration = source.State.SourceRegistrationGeneration
	intent, err := m.repositoryDocsAdmissionForPrepared(prepared)
	if err != nil {
		restoreMaintenanceEntry(entry, previousEntry)
		m.sources[entry.RegistrationID] = previousSources
		m.generation = previousManagerGeneration
		return MaintenanceEntry{}, preparedRepositoryDocsIndex{}, false, err
	}
	admissionKey := repositoryDocsAdmissionKey(entry.RegistrationID, source.State.SourceRegistrationID, intent.AuthorityPathFingerprint)
	previousAdmission, hadPreviousAdmission := m.admissions[admissionKey]
	m.admissions[admissionKey] = intent
	// An explicit fresh admission is the only operation that revives a
	// durably-cancelled intent. It also clears prior retry delay because the
	// operator has deliberately requested a new attempt.
	source.State.Stage.ConsecutiveFailures = 0
	source.State.Stage.RetryAfter = time.Time{}
	if err := m.saveLocked(); err != nil {
		restoreMaintenanceEntry(entry, previousEntry)
		m.sources[entry.RegistrationID] = previousSources
		m.generation = previousManagerGeneration
		if hadPreviousAdmission {
			m.admissions[admissionKey] = previousAdmission
		} else {
			delete(m.admissions, admissionKey)
		}
		code := "repository_docs_admission_persist_failed"
		if len(previousSources) == 0 {
			code = "repository_docs_registration_persist_failed"
		}
		return MaintenanceEntry{}, preparedRepositoryDocsIndex{}, false, RepositoryDocsSourceUnavailableError{code: code}
	}
	return cloneMaintenanceEntryForSource(entry, source), prepared, true, nil
}

func (m *MaintenanceManager) repositoryDocsAdmissionForPrepared(prepared preparedRepositoryDocsIndex) (repositoryDocsAdmissionIntent, error) {
	req := prepared.request
	intent := repositoryDocsAdmissionIntent{
		RegistrationID:               strings.TrimSpace(req.RegistrationID),
		SourceRegistrationID:         strings.TrimSpace(req.SourceRegistrationID),
		SourceRegistrationGeneration: req.SourceRegistrationGeneration,
		AuthorityPathFingerprint:     pathFingerprint(maintenanceCanonicalPathKey(req.CachePath)),
		RepoID:                       strings.TrimSpace(req.RepoID),
		WorkKey:                      repositoryDocsIndexWorkKey(req, prepared.repository, prepared.policy, prepared.namespaceID),
		ExpectedRevisionSetID:        repositoryDocsRevisionSetIdentity(req, prepared.repository, prepared.policy, prepared.namespaceID).ID(),
		Revision:                     strings.TrimSpace(req.Revision),
		IncludeWorktree:              req.IncludeWorktree,
		Disposition:                  repositoryDocsAdmissionPending,
		CreatedAt:                    m.now(),
	}
	if intent.RegistrationID == "" || intent.SourceRegistrationID == "" || intent.SourceRegistrationGeneration <= 0 || intent.ExpectedRevisionSetID == "" || intent.WorkKey == "" {
		return repositoryDocsAdmissionIntent{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_admission_invalid"}
	}
	return intent, nil
}

func (m *MaintenanceManager) cancelRepositoryDocsAdmission(job Job) error {
	if job.Type != RepositoryDocsIndexJobType || strings.TrimSpace(job.RegistrationID) == "" || strings.TrimSpace(job.SourceRegistrationID) == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key, intent, ok := m.repositoryDocsAdmissionMatchingLocked(job.RegistrationID, job.SourceRegistrationID, func(candidate repositoryDocsAdmissionIntent) bool {
		return candidate.SourceRegistrationGeneration == job.SourceRegistrationGeneration && candidate.ExpectedRevisionSetID == job.ExpectedRevisionSetID &&
			candidate.RepoID == job.RepoID && publicWorkRef(candidate.WorkKey) == job.WorkRef && (candidate.JobID == "" || candidate.JobID == job.ID)
	})
	if !ok {
		return RepositoryDocsSourceUnavailableError{code: "repository_docs_cancel_admission_missing"}
	}
	if intent.Disposition == repositoryDocsAdmissionCancelled && intent.JobID == job.ID {
		return nil
	}
	previous := intent
	intent.Disposition = repositoryDocsAdmissionCancelled
	intent.JobID = job.ID
	intent.FinishedAt = m.now()
	m.admissions[key] = intent
	if err := m.saveLocked(); err != nil {
		m.admissions[key] = previous
		return RepositoryDocsSourceUnavailableError{code: "repository_docs_cancel_persist_failed"}
	}
	return nil
}

func (m *MaintenanceManager) repositoryDocsCancellationCommitted(job Job) bool {
	if job.Type != RepositoryDocsIndexJobType || strings.TrimSpace(job.RegistrationID) == "" || strings.TrimSpace(job.SourceRegistrationID) == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, intent, ok := m.repositoryDocsAdmissionMatchingLocked(job.RegistrationID, job.SourceRegistrationID, func(candidate repositoryDocsAdmissionIntent) bool {
		return candidate.Disposition == repositoryDocsAdmissionCancelled && candidate.JobID == job.ID && candidate.RepoID == job.RepoID &&
			candidate.SourceRegistrationGeneration == job.SourceRegistrationGeneration && candidate.ExpectedRevisionSetID == job.ExpectedRevisionSetID && job.WorkRef == publicWorkRef(candidate.WorkKey)
	})
	return ok && intent.Disposition == repositoryDocsAdmissionCancelled && intent.JobID == job.ID &&
		intent.RepoID == job.RepoID && intent.SourceRegistrationGeneration == job.SourceRegistrationGeneration &&
		intent.ExpectedRevisionSetID == job.ExpectedRevisionSetID && job.WorkRef == publicWorkRef(intent.WorkKey)
}

func (m *MaintenanceManager) completeRepositoryDocsAdmission(registrationID, sourceRegistrationID string, sourceGeneration int64, expectedSetID, authorityPathFingerprint string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	registrationID = strings.TrimSpace(registrationID)
	key, intent, ok := m.repositoryDocsAdmissionMatchingLocked(registrationID, sourceRegistrationID, func(candidate repositoryDocsAdmissionIntent) bool {
		return candidate.SourceRegistrationGeneration == sourceGeneration && candidate.ExpectedRevisionSetID == strings.TrimSpace(expectedSetID) &&
			(strings.TrimSpace(authorityPathFingerprint) == "" || candidate.AuthorityPathFingerprint == strings.TrimSpace(authorityPathFingerprint))
	})
	if !ok {
		return nil
	}
	delete(m.admissions, key)
	if err := m.saveLocked(); err != nil {
		m.admissions[key] = intent
		return RepositoryDocsSourceUnavailableError{code: "repository_docs_admission_persist_failed"}
	}
	return nil
}

func (m *MaintenanceManager) repositoryDocsAdmission(registrationID string, sourceRegistrationIDs ...string) (repositoryDocsAdmissionIntent, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(sourceRegistrationIDs) > 0 && strings.TrimSpace(sourceRegistrationIDs[0]) != "" {
		_, intent, ok := m.repositoryDocsAdmissionForCurrentAuthorityLocked(registrationID, sourceRegistrationIDs[0])
		return intent, ok
	}
	var keys []string
	currentPath := m.currentAuthorityPathFingerprintLocked(registrationID)
	for key, intent := range m.admissions {
		if intent.RegistrationID == strings.TrimSpace(registrationID) && (intent.AuthorityPathFingerprint == currentPath || intent.AuthorityPathFingerprint == "") {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return repositoryDocsAdmissionIntent{}, false
	}
	sort.Strings(keys)
	return m.admissions[keys[0]], true
}

func (m *MaintenanceManager) bindRepositoryDocsAdmissionJob(registrationID, sourceRegistrationID, jobID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	key, intent, ok := m.repositoryDocsAdmissionForCurrentAuthorityLocked(registrationID, sourceRegistrationID)
	if !ok || intent.JobID == strings.TrimSpace(jobID) {
		return nil
	}
	previous := intent
	intent.JobID = strings.TrimSpace(jobID)
	m.admissions[key] = intent
	if err := m.saveLocked(); err != nil {
		m.admissions[key] = previous
		return RepositoryDocsSourceUnavailableError{code: "repository_docs_admission_persist_failed"}
	}
	return nil
}

func repositoryDocsAdmissionKey(registrationID, sourceRegistrationID string, authorityPathFingerprint ...string) string {
	path := ""
	if len(authorityPathFingerprint) > 0 {
		path = strings.TrimSpace(authorityPathFingerprint[0])
	}
	return strings.TrimSpace(registrationID) + "\x00" + strings.TrimSpace(sourceRegistrationID) + "\x00" + path
}

func (m *MaintenanceManager) currentAuthorityPathFingerprintLocked(registrationID string) string {
	registrationID = m.resolveRegistrationIDLocked(strings.TrimSpace(registrationID))
	if entry := m.entries[registrationID]; entry != nil {
		return entry.PathFingerprint
	}
	return ""
}

func (m *MaintenanceManager) repositoryDocsAdmissionMatchingLocked(registrationID, sourceRegistrationID string, match func(repositoryDocsAdmissionIntent) bool) (string, repositoryDocsAdmissionIntent, bool) {
	registrationID, sourceRegistrationID = strings.TrimSpace(registrationID), strings.TrimSpace(sourceRegistrationID)
	keys := make([]string, 0)
	for key, intent := range m.admissions {
		if intent.RegistrationID == registrationID && intent.SourceRegistrationID == sourceRegistrationID && (match == nil || match(intent)) {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return "", repositoryDocsAdmissionIntent{}, false
	}
	sort.Strings(keys)
	return keys[0], m.admissions[keys[0]], true
}

func (m *MaintenanceManager) repositoryDocsAdmissionForCurrentAuthorityLocked(registrationID, sourceRegistrationID string) (string, repositoryDocsAdmissionIntent, bool) {
	registrationID = m.resolveRegistrationIDLocked(strings.TrimSpace(registrationID))
	sourceRegistrationID = m.resolveSourceRegistrationIDLocked(strings.TrimSpace(sourceRegistrationID))
	path := m.currentAuthorityPathFingerprintLocked(registrationID)
	if intent, ok := m.admissions[repositoryDocsAdmissionKey(registrationID, sourceRegistrationID, path)]; ok {
		return repositoryDocsAdmissionKey(registrationID, sourceRegistrationID, path), intent, true
	}
	// A pre-v2 admission without authority is safe only when it was not marked
	// ambiguous during clone migration.
	if intent, ok := m.admissions[repositoryDocsAdmissionKey(registrationID, sourceRegistrationID)]; ok {
		return repositoryDocsAdmissionKey(registrationID, sourceRegistrationID), intent, true
	}
	return "", repositoryDocsAdmissionIntent{}, false
}

// RebindRepositoryDocsSource explicitly replaces a registered private Git
// authority. Ordinary registration never calls this path: callers must present
// the current source generation, so concurrent or stale rebinds fail closed.
func (m *MaintenanceManager) RebindRepositoryDocsSource(ctx context.Context, req RepositoryDocsSourceRebindRequest) (MaintenanceEntry, error) {
	registrationID := strings.TrimSpace(req.RegistrationID)
	if registrationID == "" || req.ExpectedGeneration <= 0 {
		return MaintenanceEntry{}, RepositoryDocsSourceGenerationConflictError{}
	}
	selectorGeneration := req.ExpectedGeneration
	if strings.TrimSpace(req.SourceRegistrationID) == "" {
		selectorGeneration = 0
	}
	selected, err := m.repositoryDocsSourceForSelector(RepositoryDocsSourceSelector{RegistrationID: registrationID, SourceRegistrationID: req.SourceRegistrationID, SourceRegistrationGeneration: selectorGeneration})
	if err != nil {
		return MaintenanceEntry{}, err
	}

	resolved, err := resolveRepositoryDocsSource(ctx, selected.RepoID, req.RepositoryPath, req.Profile, repositorydocs.PolicyRequest{RepoID: selected.RepoID})
	if err != nil {
		return MaintenanceEntry{}, err
	}
	m.mu.Lock()
	fenceGeneration := int64(0)
	defer func() {
		m.mu.Unlock()
		if fenceGeneration > 0 && m.jobs != nil {
			m.jobs.FenceRepositoryDocsSourceGeneration(registrationID, selected.SourceRegistrationID, fenceGeneration)
		}
	}()
	entry := m.entries[registrationID]
	source := m.sources[registrationID][selected.SourceRegistrationID]
	if entry == nil || source == nil {
		return MaintenanceEntry{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_source_not_registered"}
	}
	if source.State.SourceRegistrationGeneration != req.ExpectedGeneration {
		return MaintenanceEntry{}, RepositoryDocsSourceGenerationConflictError{}
	}
	if source.Profile == resolved.profile && source.State.GitStoreRef == resolved.policy.GitStoreRef && source.State.WorktreeRef == resolved.policy.WorktreeRef {
		return cloneMaintenanceEntryForSource(entry, source), nil
	}
	for id, candidate := range m.sources[registrationID] {
		if id != selected.SourceRegistrationID && candidate.Profile == resolved.profile && candidate.State.GitStoreRef == resolved.policy.GitStoreRef && candidate.State.WorktreeRef == resolved.policy.WorktreeRef {
			return MaintenanceEntry{}, RepositoryDocsSourceConflictError{}
		}
	}
	previousEntry := cloneMaintenanceEntryPrivate(entry)
	previousSources := cloneRepositoryDocsSources(m.sources[registrationID])
	previousManagerGeneration := m.generation
	admissionKey, previousAdmission, hadPreviousAdmission := m.repositoryDocsAdmissionForCurrentAuthorityLocked(registrationID, selected.SourceRegistrationID)
	legacyAdmission, hadLegacyAdmission := m.admissions[registrationID]
	oldGeneration := source.State.SourceRegistrationGeneration
	source.RepositoryPath = resolved.repositoryPath
	source.Profile = resolved.profile
	source.State = RepositoryDocsMaintenanceState{
		SourceRegistrationID:         selected.SourceRegistrationID,
		SourceRegistrationGeneration: oldGeneration + 1,
		GitStoreRef:                  resolved.policy.GitStoreRef,
		WorktreeRef:                  resolved.policy.WorktreeRef,
		CommitOID:                    resolved.policy.CommitOID,
		PolicyHash:                   resolved.policy.Policy.PolicyHash,
		State:                        "registered",
		UpdatedAt:                    m.now(),
		NextPollAt:                   m.now(),
	}
	entry.Generation++
	m.generation++
	delete(m.admissions, admissionKey)
	delete(m.admissions, registrationID)
	m.refreshLegacyRepositoryDocsLocked(entry)
	if err := m.saveLocked(); err != nil {
		restoreMaintenanceEntry(entry, previousEntry)
		m.sources[registrationID] = previousSources
		m.generation = previousManagerGeneration
		if hadPreviousAdmission {
			m.admissions[admissionKey] = previousAdmission
		}
		if hadLegacyAdmission {
			m.admissions[registrationID] = legacyAdmission
		}
		return MaintenanceEntry{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_registration_persist_failed"}
	}
	result := cloneMaintenanceEntryForSource(entry, source)
	fenceGeneration = oldGeneration
	return result, nil
}

// repositoryDocsSourceForAdmin resolves private local authority for an
// authenticated Admin control without copying it into public observations.
func (m *MaintenanceManager) repositoryDocsSourceForAdmin(registrationID string) (repositoryDocsAdminSource, error) {
	return m.repositoryDocsSourceForSelector(RepositoryDocsSourceSelector{RegistrationID: registrationID})
}

func (m *MaintenanceManager) repositoryDocsSourceForSelector(selector RepositoryDocsSourceSelector) (repositoryDocsAdminSource, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	selector.RegistrationID = strings.TrimSpace(selector.RegistrationID)
	selector.SourceRegistrationID = strings.TrimSpace(selector.SourceRegistrationID)
	if selector.RegistrationID == "" {
		return repositoryDocsAdminSource{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_source_selector_required"}
	}
	if (selector.SourceRegistrationID != "") != (selector.SourceRegistrationGeneration > 0) {
		return repositoryDocsAdminSource{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_source_selector_required"}
	}
	selector.RegistrationID = m.resolveRegistrationIDLocked(selector.RegistrationID)
	selector.SourceRegistrationID = m.resolveSourceRegistrationIDLocked(selector.SourceRegistrationID)
	entry := m.entries[selector.RegistrationID]
	if entry == nil {
		return repositoryDocsAdminSource{}, errors.New("maintenance: registration not found")
	}
	if !entry.Enabled {
		return repositoryDocsAdminSource{}, errors.New("maintenance: registration is disabled")
	}
	registered := m.sources[selector.RegistrationID]
	var source *repositoryDocsRegisteredSource
	if selector.SourceRegistrationID != "" {
		source = registered[selector.SourceRegistrationID]
		if source == nil {
			return repositoryDocsAdminSource{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_source_not_registered"}
		}
	} else {
		if len(registered) == 0 {
			return repositoryDocsAdminSource{}, RepositoryDocsSourceUnavailableError{code: "repository_docs_source_not_registered"}
		}
		if len(registered) > 1 {
			return repositoryDocsAdminSource{}, RepositoryDocsSourceAmbiguousError{}
		}
		for _, candidate := range registered {
			source = candidate
		}
	}
	if selector.SourceRegistrationGeneration > 0 && source.State.SourceRegistrationGeneration != selector.SourceRegistrationGeneration {
		return repositoryDocsAdminSource{}, RepositoryDocsSourceGenerationConflictError{}
	}
	cfg := entry.configSnapshot
	cfg.CachePath = entry.cachePath
	return repositoryDocsAdminSource{
		RegistrationID:               entry.RegistrationID,
		SourceRegistrationID:         source.State.SourceRegistrationID,
		SourceRegistrationGeneration: source.State.SourceRegistrationGeneration,
		CacheUUID:                    entry.CacheUUID,
		RepoID:                       entry.RepoID, RepositoryPath: source.RepositoryPath,
		CachePath: entry.cachePath, Profile: source.Profile, Config: cfg,
	}, nil
}

func (m *MaintenanceManager) selectRepositoryDocsSourceForReconcileLocked(registrationID string) (*repositoryDocsRegisteredSource, repositoryDocsAdmissionIntent, bool) {
	registered := m.sources[registrationID]
	ids := make([]string, 0, len(registered))
	for id := range registered {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, admission, ok := m.repositoryDocsAdmissionForCurrentAuthorityLocked(registrationID, id); ok && admission.Disposition != repositoryDocsAdmissionCancelled {
			copy := *registered[id]
			return &copy, admission, true
		}
	}
	for _, id := range ids {
		if _, admission, ok := m.repositoryDocsAdmissionForCurrentAuthorityLocked(registrationID, id); ok && admission.Disposition == repositoryDocsAdmissionCancelled {
			continue
		}
		state := registered[id].State
		if state.State != "ready" && (state.Stage.RetryAfter.IsZero() || !m.now().Before(state.Stage.RetryAfter)) {
			copy := *registered[id]
			return &copy, repositoryDocsAdmissionIntent{}, false
		}
	}
	for _, id := range ids {
		if _, admission, ok := m.repositoryDocsAdmissionForCurrentAuthorityLocked(registrationID, id); ok && admission.Disposition == repositoryDocsAdmissionCancelled {
			continue
		}
		state := registered[id].State
		if (state.NextPollAt.IsZero() || !m.now().Before(state.NextPollAt)) && (state.Stage.RetryAfter.IsZero() || !m.now().Before(state.Stage.RetryAfter)) {
			copy := *registered[id]
			return &copy, repositoryDocsAdmissionIntent{}, false
		}
	}
	for _, id := range ids {
		if _, admission, ok := m.repositoryDocsAdmissionForCurrentAuthorityLocked(registrationID, id); ok && admission.Disposition == repositoryDocsAdmissionCancelled {
			continue
		}
		copy := *registered[id]
		return &copy, repositoryDocsAdmissionIntent{}, false
	}
	return nil, repositoryDocsAdmissionIntent{}, false
}

func repositoryDocsSourceGeneration(entry MaintenanceEntry) int64 {
	if entry.RepositoryDocs == nil {
		return 0
	}
	return entry.RepositoryDocs.SourceRegistrationGeneration
}

func (m *MaintenanceManager) reconcileEntry(ctx context.Context, registrationID string) (MaintenanceEntry, []string) {
	actionIntentRef := jobActionIntentFromContext(ctx)
	m.mu.Lock()
	entry := m.entries[registrationID]
	if entry == nil {
		m.mu.Unlock()
		return MaintenanceEntry{}, nil
	}
	snapshot := *entry
	path := entry.cachePath
	snapshot.configSnapshot = entry.configSnapshot
	if entry.RepositoryDocs != nil {
		if source := m.sources[registrationID][entry.RepositoryDocs.SourceRegistrationID]; source != nil {
			source.State.Stage = entry.RepositoryDocs.Stage
		}
	}
	selectedSource, pendingRepositoryDocsAdmission, hasPendingRepositoryDocsAdmission := m.selectRepositoryDocsSourceForReconcileLocked(registrationID)
	if selectedSource != nil {
		state := selectedSource.State
		snapshot.RepositoryDocs = &state
		snapshot.repositoryPath = selectedSource.RepositoryPath
		snapshot.repositoryProfile = selectedSource.Profile
		snapshot.repositorySourceID = selectedSource.State.SourceRegistrationID
	} else {
		snapshot.RepositoryDocs = nil
		snapshot.repositoryPath = ""
		snapshot.repositoryProfile = ""
		snapshot.repositorySourceID = ""
	}
	m.mu.Unlock()
	now := m.now()
	activeSync, activeRAG, activeRepositoryDocs := m.activeJobsForSnapshot(snapshot)
	store, err := cache.NewSQLiteReadOnlyStore(ctx, path)
	if err != nil {
		recordJobActionIntentError(ctx, err)
		var schemaErr *cache.SchemaVersionError
		if errors.As(err, &schemaErr) {
			return m.updateEntrySchemaBlocked(registrationID, schemaErr.Compat, activeSync, activeRAG, activeRepositoryDocs), nil
		}
		return m.updateEntryFailureWithJobs(registrationID, "cache_unreadable", err, activeSync, activeRAG, activeRepositoryDocs), nil
	}
	identity, err := store.CacheIdentity(ctx)
	if err != nil || identity.UUID != snapshot.CacheUUID {
		store.Close()
		if err == nil {
			err = errors.New("cache identity changed")
		}
		recordJobActionIntentError(ctx, err)
		return m.updateEntryFailureWithJobs(registrationID, "cache_replaced", err, activeSync, activeRAG, activeRepositoryDocs), nil
	}
	contentState, err := store.GetRepoContentState(ctx, snapshot.RepoID)
	if err != nil {
		store.Close()
		recordJobActionIntentError(ctx, err)
		return m.updateEntryFailureWithJobs(registrationID, "content_state_failed", err, activeSync, activeRAG, activeRepositoryDocs), nil
	}
	frontiers, err := store.ListMaintenanceFrontiers(ctx, snapshot.RepoID)
	if err != nil {
		store.Close()
		recordJobActionIntentError(ctx, err)
		return m.updateEntryFailureWithJobs(registrationID, "maintenance_frontiers_read_failed", err, activeSync, activeRAG, activeRepositoryDocs), nil
	}
	namespaces, err := store.ListEmbeddingNamespaces(ctx, snapshot.RepoID)
	if err != nil {
		store.Close()
		recordJobActionIntentError(ctx, err)
		return m.updateEntryFailureWithJobs(registrationID, "embedding_namespaces_read_failed", err, activeSync, activeRAG, activeRepositoryDocs), nil
	}
	latestSync, _ := m.jobs.LatestCacheRepo(SyncJobType, snapshot.CacheUUID, snapshot.RepoID)
	latestRAG, _ := m.jobs.LatestCacheRepo(RAGIndexJobType, snapshot.CacheUUID, snapshot.RepoID)
	latestRepositoryDocs, _ := m.jobs.LatestRepositoryDocsSource(snapshot.CacheUUID, snapshot.RepoID, snapshot.repositorySourceID, repositoryDocsSourceGeneration(snapshot))
	if hasPendingRepositoryDocsAdmission && latestRepositoryDocs.Status == JobStatusSucceeded &&
		latestRepositoryDocs.RegistrationID == pendingRepositoryDocsAdmission.RegistrationID &&
		latestRepositoryDocs.SourceRegistrationID == pendingRepositoryDocsAdmission.SourceRegistrationID &&
		latestRepositoryDocs.SourceRegistrationGeneration == pendingRepositoryDocsAdmission.SourceRegistrationGeneration &&
		latestRepositoryDocs.ExpectedRevisionSetID == pendingRepositoryDocsAdmission.ExpectedRevisionSetID {
		if err := m.completeRepositoryDocsAdmission(registrationID, pendingRepositoryDocsAdmission.SourceRegistrationID, pendingRepositoryDocsAdmission.SourceRegistrationGeneration, pendingRepositoryDocsAdmission.ExpectedRevisionSetID, pendingRepositoryDocsAdmission.AuthorityPathFingerprint); err == nil {
			hasPendingRepositoryDocsAdmission = false
		}
	}
	snapshot.SyncStage = observeMaintenanceStage(snapshot.SyncStage, latestSync, now)
	snapshot.RAGStage = observeMaintenanceStage(snapshot.RAGStage, latestRAG, now)
	repositoryDocsNeedsIndex := false
	if snapshot.RepositoryDocs != nil && snapshot.repositoryPath != "" {
		snapshot.RepositoryDocs.Stage = observeMaintenanceStage(snapshot.RepositoryDocs.Stage, latestRepositoryDocs, now)
		repositoryDocsNeedsIndex = reconcileRepositoryDocsState(ctx, store, &snapshot, now)
	}
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
		state, ok, coverageErr := store.GetRAGCoverageState(ctx, snapshot.RepoID, namespaceID)
		if coverageErr != nil {
			store.Close()
			recordJobActionIntentError(ctx, coverageErr)
			return m.updateEntryFailureWithJobs(registrationID, "rag_coverage_read_failed", coverageErr, activeSync, activeRAG, activeRepositoryDocs), nil
		}
		if ok {
			covered = state.CoveredGeneration
			ragStatus = state.Status
			coverageUpdatedAt = state.UpdatedAt
		}
	}
	store.Close()
	started := []string{}
	activeSync, activeRAG, activeRepositoryDocs = m.activeJobsForSnapshot(snapshot)
	activeWriter, _ := m.jobs.ActiveCacheWriter(snapshot.CacheUUID)
	ragInterval := time.Duration(snapshot.Policy.RAGIntervalSeconds) * time.Second
	ragVerificationDue := maintenanceRAGVerificationDue(namespaceID, ragStatus, coverageUpdatedAt, ragInterval, now)
	needsRAGRepair := contentState.ContentGeneration > covered || (contentState.ContentGeneration > 0 && ragStatus != "ready") || ragVerificationDue
	// A durable admission preserves identity, not an exemption from retry
	// policy. Repeated failures remain bounded by the same backoff as other
	// daemon work and are resumed only when their retry window opens.
	repositoryDocsReady := repositoryDocsRetryReady(snapshot.RepositoryDocs, now)
	if actionIntentRef != "" && (activeSync.ID != "" || activeRAG.ID != "" || activeRepositoryDocs.ID != "" || activeWriter.ID != "") {
		lane, page, maxPages := nextMaintenanceSyncLane(snapshot, frontiers, now)
		syncReady := snapshot.SyncStage.RetryAfter.IsZero() || !now.Before(snapshot.SyncStage.RetryAfter)
		ragReady := snapshot.RAGStage.RetryAfter.IsZero() || !now.Before(snapshot.RAGStage.RetryAfter)
		stage := nextMaintenanceStage(snapshot.Policy, lane, needsRAGRepair, syncReady, ragReady)
		if hasPendingRepositoryDocsAdmission && repositoryDocsReady {
			stage = RepositoryDocsIndexJobType
		} else if repositoryDocsNeedsIndex && repositoryDocsReady && (stage == "" || stage == SyncJobType && lane == "tail") {
			stage = RepositoryDocsIndexJobType
		}
		candidate := Job{}
		switch stage {
		case SyncJobType:
			selection := maintenanceSyncSelection(snapshot.Policy, frontiers, lane, now)
			req := StartSyncJobRequest{
				RepoID: snapshot.RepoID, ProviderMode: "live", CachePath: path,
				CacheUUID: snapshot.CacheUUID, RegistrationID: snapshot.RegistrationID, Lane: lane,
				Issues: selection.Issues, IssueComments: selection.IssueComments,
				Wiki: selection.Wiki, Pulls: selection.Pulls, PRComments: selection.PRComments,
				MaxPages: maxPages, PerPage: snapshot.Policy.PerPage, Page: page,
				collectionPages: selection.collectionPages,
			}
			if exact, found := m.jobs.ActiveWork(syncWorkKey(req)); found {
				candidate = exact
			}
		case RAGIndexJobType:
			req := StartRAGIndexJobRequest{RepoID: snapshot.RepoID, Profile: effectiveProfile, CachePath: path, CacheUUID: snapshot.CacheUUID, RegistrationID: snapshot.RegistrationID}
			if exact, found := m.jobs.ActiveWork(ragIndexWorkKey(req)); found {
				candidate = exact
			}
		case RepositoryDocsIndexJobType:
			if hasPendingRepositoryDocsAdmission {
				if exact, found := m.jobs.ActiveWork(pendingRepositoryDocsAdmission.WorkKey); found && exact.ExpectedRevisionSetID == pendingRepositoryDocsAdmission.ExpectedRevisionSetID {
					candidate = exact
				}
			}
		}
		if candidate.ID != "" {
			correlated, attached, err := m.jobs.AttachActionIntent(candidate.ID, actionIntentRef, "coalesced")
			if err != nil {
				recordJobActionIntentError(ctx, err)
				return m.updateEntryFailure(registrationID, "retry_correlation_persist_failed", err), nil
			}
			if attached {
				switch correlated.Type {
				case SyncJobType:
					activeSync = correlated
				case RAGIndexJobType:
					activeRAG = correlated
				case RepositoryDocsIndexJobType:
					activeRepositoryDocs = correlated
				}
				return m.finishReconcileEntry(registrationID, snapshot, contentState.ContentGeneration, covered, ragStatus, namespaceID, frontiers, activeSync, activeRAG, activeRepositoryDocs, now), nil
			}
			activeSync, activeRAG, activeRepositoryDocs = m.activeJobsForSnapshot(snapshot)
			activeWriter, _ = m.jobs.ActiveCacheWriter(snapshot.CacheUUID)
		}
	}
	if actionIntentRef != "" && (activeSync.ID != "" || activeRAG.ID != "" || activeRepositoryDocs.ID != "" || activeWriter.ID != "") {
		blocker := activeWriter
		if blocker.ID == "" {
			for _, active := range []Job{activeSync, activeRAG, activeRepositoryDocs} {
				if active.ID != "" {
					blocker = active
					break
				}
			}
		}
		recordJobActionIntentError(ctx, ErrCacheWriterBusy{ActiveJobID: blocker.ID, ActiveType: blocker.Type})
		return m.finishReconcileEntry(registrationID, snapshot, contentState.ContentGeneration, covered, ragStatus, namespaceID, frontiers, activeSync, activeRAG, activeRepositoryDocs, now), nil
	}
	if activeSync.ID == "" && activeRAG.ID == "" && activeRepositoryDocs.ID == "" && activeWriter.ID == "" {
		lane, page, maxPages := nextMaintenanceSyncLane(snapshot, frontiers, now)
		syncReady := snapshot.SyncStage.RetryAfter.IsZero() || !now.Before(snapshot.SyncStage.RetryAfter)
		ragReady := snapshot.RAGStage.RetryAfter.IsZero() || !now.Before(snapshot.RAGStage.RetryAfter)
		stage := nextMaintenanceStage(snapshot.Policy, lane, needsRAGRepair, syncReady, ragReady)
		// Preserve remote head freshness and ordinary RAG repair priority, but do
		// not let historical tail backfill starve a changed local Git HEAD.
		if hasPendingRepositoryDocsAdmission && repositoryDocsReady {
			stage = RepositoryDocsIndexJobType
		} else if repositoryDocsNeedsIndex && repositoryDocsReady && (stage == "" || stage == SyncJobType && lane == "tail") {
			stage = RepositoryDocsIndexJobType
		}
		if stage == SyncJobType {
			selection := maintenanceSyncSelection(snapshot.Policy, frontiers, lane, now)
			req := StartSyncJobRequest{
				RepoID: snapshot.RepoID, ProviderMode: "live", CachePath: path,
				CacheUUID: snapshot.CacheUUID, RegistrationID: snapshot.RegistrationID, Lane: lane,
				Issues: selection.Issues, IssueComments: selection.IssueComments,
				Wiki: selection.Wiki, Pulls: selection.Pulls, PRComments: selection.PRComments,
				MaxPages: maxPages, PerPage: snapshot.Policy.PerPage,
				Page: page, IdempotencyKey: fmt.Sprintf("maintenance-%s-%s-%d-%d", snapshot.RegistrationID, lane, page, now.Unix()/60),
				ActionIntentRef: actionIntentRef,
				collectionPages: selection.collectionPages,
			}
			job, jobErr := m.jobs.StartSync(context.Background(), jobManager, req)
			if jobErr != nil {
				var busy ErrCacheWriterBusy
				if errors.As(jobErr, &busy) {
					recordJobActionIntentError(ctx, jobErr)
					return m.finishReconcileEntry(registrationID, snapshot, contentState.ContentGeneration, covered, ragStatus, namespaceID, frontiers, activeSync, activeRAG, activeRepositoryDocs, now), nil
				}
				recordJobActionIntentError(ctx, jobErr)
				return m.updateEntryFailure(registrationID, "sync_schedule_failed", jobErr), nil
			}
			started = append(started, job.ID)
			activeSync = job
		} else if stage == RAGIndexJobType {
			job, jobErr := m.jobs.StartRAGIndex(context.Background(), jobManager, StartRAGIndexJobRequest{RepoID: snapshot.RepoID, Profile: effectiveProfile, CachePath: path, CacheUUID: snapshot.CacheUUID, RegistrationID: snapshot.RegistrationID, ActionIntentRef: actionIntentRef})
			if jobErr != nil {
				var busy ErrCacheWriterBusy
				if errors.As(jobErr, &busy) {
					recordJobActionIntentError(ctx, jobErr)
					return m.finishReconcileEntry(registrationID, snapshot, contentState.ContentGeneration, covered, ragStatus, namespaceID, frontiers, activeSync, activeRAG, activeRepositoryDocs, now), started
				}
				recordJobActionIntentError(ctx, jobErr)
				return m.updateEntryFailure(registrationID, "rag_schedule_failed", jobErr), started
			}
			started = append(started, job.ID)
			activeRAG = job
		} else if stage == RepositoryDocsIndexJobType {
			revision := ""
			includeWorktree := false
			recoveryExpectedSetID := ""
			recoveryExpectedWorkKey := ""
			recoveryJobID := ""
			if hasPendingRepositoryDocsAdmission {
				revision = pendingRepositoryDocsAdmission.Revision
				includeWorktree = pendingRepositoryDocsAdmission.IncludeWorktree
				recoveryExpectedSetID = pendingRepositoryDocsAdmission.ExpectedRevisionSetID
				recoveryExpectedWorkKey = pendingRepositoryDocsAdmission.WorkKey
				recoveryJobID = pendingRepositoryDocsAdmission.JobID
				if recoveryJobID == "" {
					if recoverable, ok := m.jobs.RecoverableRepositoryDocsAdmission(registrationID, pendingRepositoryDocsAdmission.SourceRegistrationID, pendingRepositoryDocsAdmission.SourceRegistrationGeneration, pendingRepositoryDocsAdmission.ExpectedRevisionSetID, pendingRepositoryDocsAdmission.WorkKey); ok {
						recoveryJobID = recoverable.ID
					}
				}
			}
			job, jobErr := m.jobs.StartRepositoryDocsIndex(context.Background(), jobManager, StartRepositoryDocsIndexJobRequest{
				RepoID: snapshot.RepoID, RepositoryPath: snapshot.repositoryPath, Profile: snapshot.repositoryProfile,
				CachePath: path, CacheUUID: snapshot.CacheUUID, RegistrationID: snapshot.RegistrationID,
				SourceRegistrationID:          snapshot.RepositoryDocs.SourceRegistrationID,
				SourceRegistrationGeneration:  snapshot.RepositoryDocs.SourceRegistrationGeneration,
				ActionIntentRef:               actionIntentRef,
				Revision:                      revision,
				IncludeWorktree:               includeWorktree,
				recoveryExpectedRevisionSetID: recoveryExpectedSetID,
				recoveryExpectedWorkKey:       recoveryExpectedWorkKey,
				recoveryJobID:                 recoveryJobID,
			})
			if jobErr != nil {
				var staleAdmission RepositoryDocsAdmissionStaleError
				if hasPendingRepositoryDocsAdmission && errors.As(jobErr, &staleAdmission) {
					_ = m.completeRepositoryDocsAdmission(registrationID, pendingRepositoryDocsAdmission.SourceRegistrationID, pendingRepositoryDocsAdmission.SourceRegistrationGeneration, pendingRepositoryDocsAdmission.ExpectedRevisionSetID, pendingRepositoryDocsAdmission.AuthorityPathFingerprint)
					recordJobActionIntentError(ctx, jobErr)
					return m.updateRepositoryDocsFailure(registrationID, snapshot.repositorySourceID, staleAdmission.DiagnosticCode(), "queued repository documentation work was superseded before it started"), started
				}
				var busy ErrCacheWriterBusy
				if errors.As(jobErr, &busy) {
					recordJobActionIntentError(ctx, jobErr)
					return m.finishReconcileEntry(registrationID, snapshot, contentState.ContentGeneration, covered, ragStatus, namespaceID, frontiers, activeSync, activeRAG, activeRepositoryDocs, now), started
				}
				recordJobActionIntentError(ctx, jobErr)
				class := "repository_docs_schedule_failed"
				message := "repository documentation indexing could not be scheduled"
				var coded interface{ DiagnosticCode() string }
				if errors.As(jobErr, &coded) && strings.TrimSpace(coded.DiagnosticCode()) != "" {
					class = strings.TrimSpace(coded.DiagnosticCode())
				}
				if class == "repository_docs_provider_boundary_blocked" {
					message = "repository documentation indexing requires an effective local_process or local_network provider boundary"
				}
				return m.updateRepositoryDocsFailure(registrationID, snapshot.repositorySourceID, class, message), started
			}
			started = append(started, job.ID)
			activeRepositoryDocs = job
		}
	}
	return m.finishReconcileEntry(registrationID, snapshot, contentState.ContentGeneration, covered, ragStatus, namespaceID, frontiers, activeSync, activeRAG, activeRepositoryDocs, now), started
}

func repositoryDocsRetryReady(state *RepositoryDocsMaintenanceState, now time.Time) bool {
	return state == nil || state.Stage.RetryAfter.IsZero() || !now.Before(state.Stage.RetryAfter)
}

func (m *MaintenanceManager) finishReconcileEntry(registrationID string, snapshot MaintenanceEntry, contentGeneration, covered int64, ragStatus, namespaceID string, frontiers []cache.MaintenanceFrontier, activeSync, activeRAG, activeRepositoryDocs Job, now time.Time) MaintenanceEntry {
	m.mu.Lock()
	entry := m.entries[registrationID]
	entry.ContentGeneration = contentGeneration
	entry.CoveredGeneration = covered
	entry.RAGStatus = ragStatus
	entry.SyncStage = snapshot.SyncStage
	entry.RAGStage = snapshot.RAGStage
	if source := m.sources[registrationID][snapshot.repositorySourceID]; source != nil && snapshot.RepositoryDocs != nil {
		source.State = *snapshot.RepositoryDocs
	}
	m.refreshLegacyRepositoryDocsLocked(entry)
	entry.NamespaceID = namespaceID
	if activeRAG.NamespaceID != "" {
		entry.NamespaceID = activeRAG.NamespaceID
	}
	entry.Frontiers = frontiers
	entry.ActiveJobs = activeMaintenanceJobIDs(activeSync, activeRAG, activeRepositoryDocs)
	entry.LastErrorClass, entry.LastError = maintenanceEntryError(entry.Policy, entry.SyncStage, entry.RAGStage)
	entry.DetectedSchemaVersion = 0
	entry.ExpectedSchemaVersion = 0
	entry.DaemonBinaryVersion = ""
	entry.DaemonBinaryCommit = ""
	entry.QuiesceState = ""
	entry.State = deriveMaintenanceEntryState(*entry)
	if activeSync.ID != "" {
		entry.State = "refreshing"
		if strings.Contains(activeSync.WorkKey, ":tail:") {
			entry.State = "backfilling"
		}
	} else if activeRAG.ID != "" {
		entry.State = "indexing"
	} else if activeRepositoryDocs.ID != "" {
		entry.State = "indexing"
		if source := m.sources[registrationID][snapshot.repositorySourceID]; source != nil {
			source.State.State = "indexing"
			m.refreshLegacyRepositoryDocsLocked(entry)
		}
	}
	entry.LastReconciledAt = now
	entry.NextReconcileAt = now.Add(time.Minute)
	_ = m.saveLocked()
	updated := cloneMaintenanceEntry(entry)
	m.mu.Unlock()
	return updated
}

func reconcileRepositoryDocsState(ctx context.Context, store *cache.SQLiteStore, entry *MaintenanceEntry, now time.Time) bool {
	state := entry.RepositoryDocs
	if state == nil || entry.repositoryPath == "" {
		return false
	}
	state.UpdatedAt = now
	state.NextPollAt = now.Add(time.Minute)
	repo, err := repositorydocs.OpenRepository(ctx, entry.repositoryPath)
	if err != nil {
		state.State = "degraded"
		state.LastErrorClass = maintenanceJobErrorClass(err, "git_store_unavailable")
		state.LastError = "registered Git store is unavailable"
		return false
	}
	policy, err := repositorydocs.InspectPolicy(ctx, repo, repositorydocs.PolicyRequest{RepoID: entry.RepoID})
	if err != nil {
		state.State = "degraded"
		state.LastErrorClass = maintenanceJobErrorClass(err, "repository_docs_policy_invalid")
		state.LastError = "repository documentation policy cannot be resolved"
		return false
	}
	state.GitStoreRef = policy.GitStoreRef
	state.WorktreeRef = policy.WorktreeRef
	state.CommitOID = policy.CommitOID
	state.PolicyHash = policy.Policy.PolicyHash
	state.LastErrorClass = state.Stage.LastErrorClass
	state.LastError = state.Stage.LastError
	if !policy.Policy.Policy.Enabled {
		state.State = "disabled"
		state.RevisionSetID = ""
		return false
	}
	sets, err := store.ListRepositoryDocRevisionSets(ctx, cache.RepositoryDocRevisionSetFilter{
		RepoID: entry.RepoID, SourceRegistrationID: state.SourceRegistrationID, SourceRegistrationGeneration: state.SourceRegistrationGeneration,
		GitStoreRef: policy.GitStoreRef, CommitOID: policy.CommitOID,
		PolicyHash: policy.Policy.PolicyHash, ExactOverlay: true,
		ChunkPolicyID: repositorydocs.DefaultChunkPolicyID, Limit: 20,
	})
	if err != nil {
		state.State = "degraded"
		state.LastErrorClass = "repository_docs_status_failed"
		state.LastError = "repository documentation index state is unavailable"
		return false
	}
	for _, set := range sets {
		if set.State == cache.RepoDocSetReady {
			state.State = "ready"
			state.RevisionSetID = set.ID
			return false
		}
	}
	state.State = "not_indexed"
	state.RevisionSetID = ""
	return true
}

func (m *MaintenanceManager) updateRepositoryDocsFailure(id, sourceID, class, message string) MaintenanceEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.entries[id]
	if entry == nil {
		return MaintenanceEntry{}
	}
	source := m.sources[id][strings.TrimSpace(sourceID)]
	if source == nil {
		return cloneMaintenanceEntry(entry)
	}
	source.State.State = "degraded"
	source.State.LastErrorClass = class
	source.State.LastError = message
	source.State.UpdatedAt = m.now()
	source.State.NextPollAt = m.now().Add(time.Minute)
	m.refreshLegacyRepositoryDocsLocked(entry)
	entry.LastReconciledAt = m.now()
	entry.NextReconcileAt = entry.LastReconciledAt.Add(time.Minute)
	_ = m.saveLocked()
	return cloneMaintenanceEntry(entry)
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

func (m *MaintenanceManager) activeJobsForSnapshot(snapshot MaintenanceEntry) (Job, Job, Job) {
	activeSync, _ := m.jobs.ActiveCacheRepo(SyncJobType, snapshot.CacheUUID, snapshot.RepoID)
	activeRAG, _ := m.jobs.ActiveCacheRepo(RAGIndexJobType, snapshot.CacheUUID, snapshot.RepoID)
	activeRepositoryDocs, _ := m.jobs.ActiveRepositoryDocsSource(snapshot.CacheUUID, snapshot.RepoID, snapshot.repositorySourceID, repositoryDocsSourceGeneration(snapshot))
	return activeSync, activeRAG, activeRepositoryDocs
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

// maintenanceSyncSelection keeps continuation units collection-local. Wiki
// uses exact record offsets while issues and pull requests use provider page
// numbers; comparing those values is useful only to choose a wake-up, never as
// the request cursor for every selected collection.
func maintenanceSyncSelection(policy MaintenancePolicy, frontiers []cache.MaintenanceFrontier, lane string, now time.Time) StartSyncJobRequest {
	selection := StartSyncJobRequest{collectionPages: map[string]int{}}
	byKey := make(map[string]cache.MaintenanceFrontier, len(frontiers))
	for _, frontier := range frontiers {
		byKey[frontier.RemoteType+"\x00"+frontier.Lane] = frontier
	}
	interval := time.Duration(policy.HeadIntervalSeconds) * time.Second
	for _, remoteType := range maintenanceRemoteTypes(policy) {
		frontier, ok := byKey[remoteType+"\x00"+lane]
		needed := false
		switch lane {
		case "head":
			needed = !ok || frontier.Status != "fresh" || frontier.UpdatedAt.Add(interval).Before(now)
		case "tail":
			needed = !ok || frontier.Status != "complete"
		}
		if !needed {
			continue
		}
		page := 1
		if ok {
			page = checkpointPage(frontier.Checkpoint)
			if page <= 0 {
				page = 1
			}
		}
		selection.collectionPages[remoteType] = page
		switch remoteType {
		case "issue":
			selection.Issues = true
		case "issue_comment":
			selection.IssueComments = true
		case "wiki":
			selection.Wiki = true
		case "pull_request":
			selection.Pulls = true
		case "pr_comment":
			selection.PRComments = true
		}
	}
	return selection
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
	if job.ID == "" || !jobTerminalStatus(job.Status) {
		return state
	}
	observedAt := job.UpdatedAt
	if job.FinishedAt != nil {
		observedAt = *job.FinishedAt
	}
	// Durable retries intentionally retain one public job id. Deduplicate one
	// exact terminal observation, while still accepting a later terminal
	// transition of the same job after it has been resumed.
	if job.ID == state.LastJobID && job.Status == state.Status && !observedAt.After(state.UpdatedAt) {
		return state
	}
	state.LastJobID = job.ID
	state.Status = job.Status
	state.UpdatedAt = observedAt
	if job.NamespaceID != "" {
		state.NamespaceID = job.NamespaceID
	}
	switch job.Status {
	case JobStatusSucceeded, JobStatusSuperseded:
		if job.Type == SyncJobType && (job.SyncHealth == SyncHealthPartial || job.SyncHealth == SyncHealthFailed) {
			state.ConsecutiveFailures++
			state.LastErrorClass = sanitizeMaintenanceErrorClass(job.ErrorClass, "sync_partial")
			state.LastError = publicMaintenanceJobError(job.Type, state.LastErrorClass)
			state.RetryAfter = now.Add(maintenanceStageBackoff(state.ConsecutiveFailures))
		} else {
			state.ConsecutiveFailures = 0
			state.LastErrorClass = ""
			state.LastError = ""
			state.RetryAfter = time.Time{}
		}
	case JobStatusInterrupted:
		// A daemon restart is not a failed attempt. Durable work resumes with the
		// same public identity immediately after recovery.
		state.LastErrorClass = sanitizeMaintenanceErrorClass(job.ErrorClass, job.Type+"_interrupted")
		state.LastError = publicMaintenanceJobError(job.Type, state.LastErrorClass)
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
	if jobType == RepositoryDocsIndexJobType {
		return "repository documentation indexing failed"
	}
	return "remote cache maintenance failed"
}

func (m *MaintenanceManager) updateEntryFailure(id, class string, _ error) MaintenanceEntry {
	return m.updateEntryFailureWithJobs(id, class, nil)
}

func (m *MaintenanceManager) updateEntryFailureWithJobs(id, class string, _ error, jobs ...Job) MaintenanceEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.entries[id]
	if entry == nil {
		return MaintenanceEntry{}
	}
	entry.State = "degraded"
	entry.LastErrorClass = class
	entry.LastError = publicMaintenanceError(class)
	entry.ActiveJobs = activeMaintenanceJobIDs(jobs...)
	entry.LastReconciledAt = m.now()
	entry.NextReconcileAt = entry.LastReconciledAt.Add(time.Minute)
	_ = m.saveLocked()
	return cloneMaintenanceEntry(entry)
}

func (m *MaintenanceManager) updateEntrySchemaBlocked(id string, compat cache.VersionCompatibility, jobs ...Job) MaintenanceEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.entries[id]
	if entry == nil {
		return MaintenanceEntry{}
	}
	entry.State = "cache_schema_blocked"
	entry.LastErrorClass = "cache_schema_blocked"
	entry.LastError = publicMaintenanceError("cache_schema_blocked")
	entry.DetectedSchemaVersion = compat.DetectedVersion
	entry.ExpectedSchemaVersion = compat.ExpectedVersion
	entry.DaemonBinaryVersion = m.manager.Version
	entry.DaemonBinaryCommit = m.manager.Commit
	entry.QuiesceState = "required"
	entry.ActiveJobs = activeMaintenanceJobIDs(jobs...)
	entry.LastReconciledAt = m.now()
	entry.NextReconcileAt = entry.LastReconciledAt.Add(time.Minute)
	_ = m.saveLocked()
	return cloneMaintenanceEntry(entry)
}

func (m *MaintenanceManager) saveLocked() error {
	disk := maintenanceRegistryFile{SchemaVersion: maintenanceRegistrySchema, Generation: m.generation}
	for _, entry := range m.entries {
		stored := maintenanceDiskEntry{MaintenanceEntry: cloneMaintenanceEntry(entry), CachePath: entry.cachePath, ConfigReference: entry.configReference, ConfigSnapshot: entry.configSnapshot, RepositoryPath: entry.repositoryPath, RepositoryProfile: entry.repositoryProfile, IdentityBlockedWasEnabled: entry.identityBlockedWasEnabled, IdentityConflictCandidates: cloneMaintenanceConflictCandidates(m.conflictCandidates[entry.RegistrationID])}
		ids := make([]string, 0, len(m.sources[entry.RegistrationID]))
		for id := range m.sources[entry.RegistrationID] {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			source := m.sources[entry.RegistrationID][id]
			if source != nil {
				stored.RepositoryDocsSources = append(stored.RepositoryDocsSources, repositoryDocsDiskSource{State: source.State, RepositoryPath: source.RepositoryPath, Profile: source.Profile})
			}
		}
		disk.Entries = append(disk.Entries, stored)
	}
	sort.Slice(disk.Entries, func(i, j int) bool { return disk.Entries[i].RegistrationID < disk.Entries[j].RegistrationID })
	for _, receipt := range m.receipts {
		disk.Receipts = append(disk.Receipts, receipt)
	}
	sort.Slice(disk.Receipts, func(i, j int) bool { return disk.Receipts[i].KeyHash < disk.Receipts[j].KeyHash })
	for from, to := range m.redirects {
		disk.RegistrationRedirects = append(disk.RegistrationRedirects, MaintenanceRegistrationRedirect{From: from, To: to})
	}
	sort.Slice(disk.RegistrationRedirects, func(i, j int) bool { return disk.RegistrationRedirects[i].From < disk.RegistrationRedirects[j].From })
	for from, to := range m.sourceRedirects {
		disk.SourceRegistrationRedirects = append(disk.SourceRegistrationRedirects, MaintenanceRegistrationRedirect{From: from, To: to})
	}
	sort.Slice(disk.SourceRegistrationRedirects, func(i, j int) bool {
		return disk.SourceRegistrationRedirects[i].From < disk.SourceRegistrationRedirects[j].From
	})
	for _, receipt := range m.resolutionReceipts {
		disk.ConflictResolutionReceipts = append(disk.ConflictResolutionReceipts, receipt)
	}
	sort.Slice(disk.ConflictResolutionReceipts, func(i, j int) bool {
		if disk.ConflictResolutionReceipts[i].AppliedAt.Equal(disk.ConflictResolutionReceipts[j].AppliedAt) {
			return disk.ConflictResolutionReceipts[i].KeyHash < disk.ConflictResolutionReceipts[j].KeyHash
		}
		return disk.ConflictResolutionReceipts[i].AppliedAt.Before(disk.ConflictResolutionReceipts[j].AppliedAt)
	})
	if len(disk.ConflictResolutionReceipts) > maxMaintenanceConflictResolutionReceipts {
		disk.ConflictResolutionReceipts = append([]maintenanceConflictResolutionReceipt(nil), disk.ConflictResolutionReceipts[len(disk.ConflictResolutionReceipts)-maxMaintenanceConflictResolutionReceipts:]...)
	}
	for cacheUUID, fingerprints := range m.retiredClonePaths {
		for fingerprint := range fingerprints {
			disk.RetiredClonePaths = append(disk.RetiredClonePaths, maintenanceRetiredClonePath{CacheUUID: cacheUUID, PathFingerprint: fingerprint})
		}
	}
	sort.Slice(disk.RetiredClonePaths, func(i, j int) bool {
		if disk.RetiredClonePaths[i].CacheUUID != disk.RetiredClonePaths[j].CacheUUID {
			return disk.RetiredClonePaths[i].CacheUUID < disk.RetiredClonePaths[j].CacheUUID
		}
		return disk.RetiredClonePaths[i].PathFingerprint < disk.RetiredClonePaths[j].PathFingerprint
	})
	for _, admission := range m.admissions {
		disk.RepositoryDocsAdmissionQueue = append(disk.RepositoryDocsAdmissionQueue, admission)
	}
	sort.Slice(disk.RepositoryDocsAdmissionQueue, func(i, j int) bool {
		left, right := disk.RepositoryDocsAdmissionQueue[i], disk.RepositoryDocsAdmissionQueue[j]
		if left.RegistrationID != right.RegistrationID {
			return left.RegistrationID < right.RegistrationID
		}
		return left.SourceRegistrationID < right.SourceRegistrationID
	})
	data, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return err
	}
	writeFile := m.writeFile
	if writeFile == nil {
		writeFile = durableAtomicWriteFile
	}
	payload := append(data, '\n')
	if err := writeFile(m.path, payload, 0o600); err != nil {
		// Atomic replace can report a directory-fsync error after rename has
		// already committed the exact payload. Confirm that boundary before a
		// caller rolls memory back; otherwise disk and the live manager split.
		persisted, readErr := os.ReadFile(m.path)
		if readErr != nil || !bytes.Equal(persisted, payload) {
			return err
		}
	}
	m.pruneConflictResolutionReceiptsLocked()
	return nil
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
		return MaintenancePolicy{}, maintenancePolicyScopeError{Scope: cache.RepositoryScopeIssues}
	}
	if policy.Wiki && !bindingHasScope(binding, cache.RepositoryScopeWiki) {
		return MaintenancePolicy{}, maintenancePolicyScopeError{Scope: cache.RepositoryScopeWiki}
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
	case "identity_unresolved":
		return "managed cache repository identity is unavailable"
	case "identity_conflict":
		return "managed cache repository identity has conflicting policies"
	case "cache_clone_conflict":
		return "managed cache identity is registered at multiple locations"
	case "config_snapshot_invalid":
		return "managed cache configuration does not match its cache authority"
	case "cache_unreadable":
		return "managed cache is unavailable"
	case "cache_schema_blocked":
		return "managed cache schema requires a compatible service binary"
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

func repositoryDocsSourceRegistrationID(maintenanceRegistrationID, gitStoreRef, worktreeRef, profile string) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{"repository-docs-source", maintenanceRegistrationID, gitStoreRef, worktreeRef, strings.TrimSpace(profile)}, "\x00")))
	return "repo-doc-source-" + hex.EncodeToString(sum[:8])
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

func maintenanceReceiptAcceptsIntent(receipt maintenanceReceipt, canonicalIntentHash string, entry *MaintenanceEntry) bool {
	if receipt.IntentHash == canonicalIntentHash {
		return true
	}
	if entry == nil {
		return false
	}
	// A compatible alias migration changes only the registration component of
	// the intent. Preserve the original hash as audit evidence and accept the
	// canonical replay only when the exact current policy/config recomputes that
	// historical hash. A rejected policy/config candidate therefore cannot gain
	// authority through an alias redirect.
	for _, legacyID := range entry.LegacyRegistrationIDs {
		if receipt.IntentHash == maintenanceEnrollmentIntentHash(legacyID, entry.Policy, entry.ConfigHash) {
			return true
		}
	}
	return false
}

func deriveMaintenanceEntryState(entry MaintenanceEntry) string {
	if !entry.Enabled {
		return "disabled"
	}
	if entry.LastErrorClass != "" {
		return "degraded"
	}
	if entry.RepositoryDocs != nil && entry.RepositoryDocs.State == "degraded" {
		return "degraded"
	}
	for _, source := range entry.RepositoryDocsSources {
		if source.State == "degraded" {
			return "degraded"
		}
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
	if entry.RepositoryDocs != nil && (entry.RepositoryDocs.State == "registered" || entry.RepositoryDocs.State == "not_indexed" || entry.RepositoryDocs.State == "indexing") {
		return "indexing"
	}
	for _, source := range entry.RepositoryDocsSources {
		if source.State == "registered" || source.State == "not_indexed" || source.State == "indexing" {
			return "indexing"
		}
	}
	return "ready"
}

func cloneMaintenanceEntry(entry *MaintenanceEntry) MaintenanceEntry {
	if entry == nil {
		return MaintenanceEntry{}
	}
	copy := *entry
	copy.Aliases = append([]string(nil), entry.Aliases...)
	copy.LegacyRegistrationIDs = append([]string(nil), entry.LegacyRegistrationIDs...)
	if entry.IdentityConflict != nil {
		conflict := *entry.IdentityConflict
		conflict.CandidateRegistrationIDs = append([]string(nil), entry.IdentityConflict.CandidateRegistrationIDs...)
		conflict.PolicyHashes = append([]string(nil), entry.IdentityConflict.PolicyHashes...)
		conflict.ConfigHashes = append([]string(nil), entry.IdentityConflict.ConfigHashes...)
		conflict.PathFingerprints = append([]string(nil), entry.IdentityConflict.PathFingerprints...)
		conflict.Candidates = cloneMaintenanceIdentityCandidates(entry.IdentityConflict.Candidates)
		copy.IdentityConflict = &conflict
	}
	copy.Frontiers = append([]cache.MaintenanceFrontier(nil), entry.Frontiers...)
	copy.ActiveJobs = append([]string(nil), entry.ActiveJobs...)
	copy.RepositoryDocsSources = append([]RepositoryDocsMaintenanceState(nil), entry.RepositoryDocsSources...)
	if entry.RepositoryDocs != nil {
		repositoryDocs := *entry.RepositoryDocs
		copy.RepositoryDocs = &repositoryDocs
	}
	copy.cachePath = ""
	copy.repositoryPath = ""
	copy.repositoryProfile = ""
	copy.repositorySourceID = ""
	return copy
}

func cloneMaintenanceEntryForSource(entry *MaintenanceEntry, source *repositoryDocsRegisteredSource) MaintenanceEntry {
	copy := cloneMaintenanceEntry(entry)
	if source == nil {
		copy.RepositoryDocs = nil
		return copy
	}
	state := source.State
	copy.RepositoryDocs = &state
	return copy
}

func cloneRepositoryDocsSources(in map[string]*repositoryDocsRegisteredSource) map[string]*repositoryDocsRegisteredSource {
	out := make(map[string]*repositoryDocsRegisteredSource, len(in))
	for id, source := range in {
		if source == nil {
			continue
		}
		copy := *source
		out[id] = &copy
	}
	return out
}

func cloneMaintenanceConflictCandidates(in []maintenanceIdentityConflictCandidate) []maintenanceIdentityConflictCandidate {
	out := make([]maintenanceIdentityConflictCandidate, 0, len(in))
	for _, candidate := range in {
		copy := candidate
		copy.Entry = cloneMaintenanceEntryPrivate(&candidate.Entry)
		copy.Entry.IdentityConflict = nil
		copy.RepositorySources = append([]repositoryDocsDiskSource(nil), candidate.RepositorySources...)
		out = append(out, copy)
	}
	return out
}

func cloneMaintenanceIdentityCandidates(in []MaintenanceIdentityCandidate) []MaintenanceIdentityCandidate {
	out := make([]MaintenanceIdentityCandidate, len(in))
	for i, candidate := range in {
		out[i] = candidate
		out[i].SourceRefs = append([]string(nil), candidate.SourceRefs...)
		out[i].CohortRegistrationIDs = append([]string(nil), candidate.CohortRegistrationIDs...)
		out[i].CohortRepoIDs = append([]string(nil), candidate.CohortRepoIDs...)
		out[i].Members = cloneMaintenanceIdentityCandidates(candidate.Members)
	}
	return out
}

func (m *MaintenanceManager) refreshLegacyRepositoryDocsLocked(entry *MaintenanceEntry) {
	if entry == nil {
		return
	}
	m.refreshLegacyRepositoryDocsForKeyLocked(entry, entry.RegistrationID)
}

func (m *MaintenanceManager) refreshLegacyRepositoryDocsForKeyLocked(entry *MaintenanceEntry, registrationKey string) {
	if entry == nil {
		return
	}
	registered := m.sources[registrationKey]
	ids := make([]string, 0, len(registered))
	for id := range registered {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		entry.RepositoryDocs = nil
		entry.RepositoryDocsSources = nil
		entry.repositoryPath = ""
		entry.repositoryProfile = ""
		entry.repositorySourceID = ""
		return
	}
	source := registered[ids[0]]
	entry.RepositoryDocsSources = entry.RepositoryDocsSources[:0]
	for _, id := range ids {
		entry.RepositoryDocsSources = append(entry.RepositoryDocsSources, registered[id].State)
	}
	state := source.State
	entry.RepositoryDocs = &state
	entry.repositoryPath = source.RepositoryPath
	entry.repositoryProfile = source.Profile
	entry.repositorySourceID = source.State.SourceRegistrationID
}

func cloneMaintenanceEntryPrivate(entry *MaintenanceEntry) MaintenanceEntry {
	if entry == nil {
		return MaintenanceEntry{}
	}
	copy := *entry
	copy.Aliases = append([]string(nil), entry.Aliases...)
	copy.LegacyRegistrationIDs = append([]string(nil), entry.LegacyRegistrationIDs...)
	if entry.IdentityConflict != nil {
		conflict := *entry.IdentityConflict
		conflict.CandidateRegistrationIDs = append([]string(nil), entry.IdentityConflict.CandidateRegistrationIDs...)
		conflict.PolicyHashes = append([]string(nil), entry.IdentityConflict.PolicyHashes...)
		conflict.ConfigHashes = append([]string(nil), entry.IdentityConflict.ConfigHashes...)
		conflict.PathFingerprints = append([]string(nil), entry.IdentityConflict.PathFingerprints...)
		conflict.Candidates = cloneMaintenanceIdentityCandidates(entry.IdentityConflict.Candidates)
		copy.IdentityConflict = &conflict
	}
	copy.Frontiers = append([]cache.MaintenanceFrontier(nil), entry.Frontiers...)
	copy.ActiveJobs = append([]string(nil), entry.ActiveJobs...)
	copy.RepositoryDocsSources = append([]RepositoryDocsMaintenanceState(nil), entry.RepositoryDocsSources...)
	if entry.RepositoryDocs != nil {
		repositoryDocs := *entry.RepositoryDocs
		copy.RepositoryDocs = &repositoryDocs
	}
	return copy
}

func restoreMaintenanceEntry(target *MaintenanceEntry, previous MaintenanceEntry) {
	if target == nil {
		return
	}
	*target = cloneMaintenanceEntryPrivate(&previous)
}
