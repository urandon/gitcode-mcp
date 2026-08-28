package adminhttp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const observationAPIVersion = "1"

type SnapshotProvider func(context.Context) (ObservationSnapshot, error)

type ObservationSnapshot struct {
	APIVersion   string                   `json:"api_version"`
	Revision     string                   `json:"revision"`
	GeneratedAt  time.Time                `json:"generated_at"`
	Service      ServiceObservation       `json:"service"`
	Attention    []AttentionItem          `json:"attention"`
	Caches       []CacheObservation       `json:"caches"`
	Jobs         []JobObservation         `json:"jobs"`
	JobRetention JobRetentionObservation  `json:"job_retention"`
	Maintenance  []MaintenanceObservation `json:"maintenance"`
	Diagnostics  []DiagnosticObservation  `json:"diagnostics"`
	Capabilities []CapabilityObservation  `json:"capabilities"`
}

type JobRetentionObservation struct {
	SuccessTTLSeconds    int64            `json:"success_ttl_seconds"`
	DiagnosticTTLSeconds int64            `json:"diagnostic_ttl_seconds"`
	MaxTerminalJobs      int              `json:"max_terminal_jobs"`
	MaxDiagnosticJobs    int              `json:"max_diagnostic_jobs"`
	MaxProgressEvents    int              `json:"max_progress_events"`
	Active               int              `json:"active"`
	Terminal             int              `json:"terminal"`
	RetainedByStatus     []JobStatusCount `json:"retained_by_status"`
	OldestRetainedAt     *time.Time       `json:"oldest_retained_at,omitempty"`
	LastPrunedAt         *time.Time       `json:"last_pruned_at,omitempty"`
	ExpiredTotal         int              `json:"expired_total"`
	TruncatedTotal       int              `json:"truncated_total"`
	LastExpired          int              `json:"last_expired"`
	LastTruncated        int              `json:"last_truncated"`
}

type JobStatusCount struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

type ServiceObservation struct {
	Version     string     `json:"version"`
	Protocol    string     `json:"protocol"`
	Running     bool       `json:"running"`
	Installed   bool       `json:"installed"`
	InstallKind string     `json:"install_kind,omitempty"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	AdminSecure bool       `json:"admin_secure"`
}

type AttentionItem struct {
	ID          string `json:"id"`
	Severity    string `json:"severity"`
	EntityType  string `json:"entity_type"`
	EntityID    string `json:"entity_id"`
	Code        string `json:"code"`
	Message     string `json:"message"`
	Remediation string `json:"remediation,omitempty"`
}

type CacheObservation struct {
	CacheRef        string                  `json:"cache_ref"`
	PathFingerprint string                  `json:"path_fingerprint,omitempty"`
	StorageMode     string                  `json:"storage_mode,omitempty"`
	Readiness       string                  `json:"readiness"`
	SchemaVersion   int                     `json:"schema_version,omitempty"`
	WALCapable      bool                    `json:"wal_capable"`
	JournalMode     string                  `json:"journal_mode,omitempty"`
	RecordCount     int                     `json:"record_count"`
	ChunkCount      int                     `json:"chunk_count"`
	RepositoryCount int                     `json:"repository_count"`
	Repositories    []RepositoryObservation `json:"repositories"`
}

type RepositoryObservation struct {
	RepoID        string                              `json:"repo_id"`
	DisplayName   string                              `json:"display_name,omitempty"`
	Aliases       []string                            `json:"aliases,omitempty"`
	Scopes        []string                            `json:"scopes,omitempty"`
	BindingState  string                              `json:"binding_state"`
	Counts        CollectionCounts                    `json:"counts"`
	Coverage      CoverageObservation                 `json:"coverage"`
	Execution     ExecutionObservation                `json:"execution"`
	Collections   []CollectionObservation             `json:"collections"`
	RecentSync    []SyncEventObservation              `json:"recent_sync_events"`
	Documentation *RepositoryDocumentationObservation `json:"documentation,omitempty"`
}

type RepositoryDocumentationObservation struct {
	State             string     `json:"state"`
	RevisionSetID     string     `json:"revision_set_id,omitempty"`
	CommitOID         string     `json:"commit_oid,omitempty"`
	RequestedRevision string     `json:"requested_revision,omitempty"`
	PolicySource      string     `json:"policy_source,omitempty"`
	PolicyHash        string     `json:"policy_hash,omitempty"`
	GitStoreRef       string     `json:"git_store_ref,omitempty"`
	WorktreeRef       string     `json:"worktree_ref,omitempty"`
	Overlay           bool       `json:"overlay"`
	NamespaceID       string     `json:"namespace_id,omitempty"`
	EligibleFiles     int        `json:"eligible_files"`
	EligibleChunks    int        `json:"eligible_chunks"`
	EmbeddedChunks    int        `json:"embedded_chunks"`
	ReusedChunks      int        `json:"reused_chunks"`
	FailedChunks      int        `json:"failed_chunks"`
	MissingObjects    int        `json:"missing_objects"`
	UpdatedAt         *time.Time `json:"updated_at,omitempty"`
	RevisionSetCount  int        `json:"revision_set_count"`
	SearchAvailable   bool       `json:"search_available"`
	SearchHandoff     string     `json:"search_handoff,omitempty"`
	IndexHandoff      string     `json:"index_handoff,omitempty"`
}

type CollectionObservation struct {
	Kind  string       `json:"kind"`
	Count int          `json:"count"`
	Head  CoverageLane `json:"head"`
	Tail  CoverageLane `json:"tail"`
}

type SyncEventObservation struct {
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Status      string    `json:"status"`
	CompletedAt time.Time `json:"completed_at"`
	ZeroDelta   bool      `json:"zero_delta"`
}

type CollectionCounts struct {
	Records   int            `json:"records"`
	Comments  int            `json:"comments"`
	Chunks    int            `json:"chunks"`
	ByKind    []KindCount    `json:"by_kind,omitempty"`
	Secondary SecondaryCount `json:"secondary"`
}

type KindCount struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

type SecondaryCount struct {
	Pending  int `json:"pending"`
	Deferred int `json:"deferred"`
	Complete int `json:"complete"`
	Total    int `json:"total"`
}

type CoverageObservation struct {
	Head       CoverageLane `json:"head"`
	Tail       CoverageLane `json:"tail"`
	Secondary  CoverageLane `json:"secondary"`
	Projection CoverageLane `json:"projection"`
	RAG        CoverageLane `json:"rag"`
}

type CoverageLane struct {
	State             string     `json:"state"`
	Status            string     `json:"status"`
	UpdatedAt         *time.Time `json:"updated_at,omitempty"`
	StopReason        string     `json:"stop_reason,omitempty"`
	PagesListed       int        `json:"pages_listed,omitempty"`
	RecordsListed     int        `json:"records_listed,omitempty"`
	CurrentGeneration int64      `json:"current_generation,omitempty"`
	CoveredGeneration int64      `json:"covered_generation,omitempty"`
	Eligible          int        `json:"eligible,omitempty"`
	Embedded          int        `json:"embedded,omitempty"`
	Missing           int        `json:"missing,omitempty"`
}

type ExecutionObservation struct {
	ActiveJobIDs   []string        `json:"active_job_ids,omitempty"`
	Contention     *Contention     `json:"contention,omitempty"`
	ScheduledRetry *ScheduledRetry `json:"scheduled_retry,omitempty"`
	LastErrors     []StageError    `json:"last_stage_errors,omitempty"`
}

type Contention struct {
	State     string `json:"state"`
	Operation string `json:"operation,omitempty"`
}

type ScheduledRetry struct {
	Stage string    `json:"stage"`
	At    time.Time `json:"at"`
}

type StageError struct {
	Stage        string     `json:"stage"`
	FailureClass string     `json:"failure_class"`
	Message      string     `json:"message"`
	ObservedAt   *time.Time `json:"observed_at,omitempty"`
}

type JobObservation struct {
	ID                  string                `json:"id"`
	Type                string                `json:"type"`
	CacheRef            string                `json:"cache_ref,omitempty"`
	RepoID              string                `json:"repo_id,omitempty"`
	ProfileID           string                `json:"profile_id,omitempty"`
	NamespaceID         string                `json:"namespace_id,omitempty"`
	RegistrationID      string                `json:"registration_id,omitempty"`
	Status              string                `json:"status"`
	CreatedAt           time.Time             `json:"created_at"`
	StartedAt           *time.Time            `json:"started_at,omitempty"`
	UpdatedAt           time.Time             `json:"updated_at"`
	FinishedAt          *time.Time            `json:"finished_at,omitempty"`
	Steps               int                   `json:"steps,omitempty"`
	Completed           int                   `json:"completed,omitempty"`
	FailureClass        string                `json:"failure_class,omitempty"`
	FailureMessage      string                `json:"failure_message,omitempty"`
	FailureCollection   string                `json:"failure_collection,omitempty"`
	RetryAfter          string                `json:"retry_after,omitempty"`
	InspectCommand      string                `json:"inspect_command,omitempty"`
	RemediationCommand  string                `json:"remediation_command,omitempty"`
	Progress            []ProgressObservation `json:"progress,omitempty"`
	WorkRef             string                `json:"work_ref,omitempty"`
	Cancellable         bool                  `json:"cancellable"`
	Retryable           bool                  `json:"retryable"`
	ActionReason        string                `json:"action_reason,omitempty"`
	ProgressRetained    int                   `json:"progress_retained"`
	ProgressLimit       int                   `json:"progress_limit"`
	ThroughputPerSecond float64               `json:"throughput_per_second,omitempty"`
	ETASeconds          int                   `json:"eta_seconds,omitempty"`
}

type ProgressObservation struct {
	Type            string `json:"type,omitempty"`
	Phase           string `json:"phase,omitempty"`
	Collection      string `json:"collection,omitempty"`
	Page            int    `json:"page,omitempty"`
	RecordsListed   int    `json:"records_listed,omitempty"`
	RecordsFetched  int    `json:"records_fetched,omitempty"`
	RecordsInserted int    `json:"records_inserted,omitempty"`
	RecordsUpdated  int    `json:"records_updated,omitempty"`
	RecordsSkipped  int    `json:"records_skipped,omitempty"`
	RecordsDeferred int    `json:"records_deferred,omitempty"`
	RecordsFailed   int    `json:"records_failed,omitempty"`
	RetryAfter      string `json:"retry_after,omitempty"`
	Attempt         int    `json:"attempt,omitempty"`
	RateLimitState  string `json:"rate_limit_state,omitempty"`
}

type MaintenanceObservation struct {
	RegistrationID  string                `json:"registration_id"`
	CacheRef        string                `json:"cache_ref"`
	RepoID          string                `json:"repo_id"`
	NamespaceID     string                `json:"namespace_id,omitempty"`
	Enabled         bool                  `json:"enabled"`
	State           string                `json:"state"`
	Generation      int64                 `json:"generation"`
	Policy          MaintenancePolicyView `json:"policy"`
	NextReconcileAt *time.Time            `json:"next_reconcile_at,omitempty"`
}

type MaintenancePolicyView struct {
	SyncEnabled         bool     `json:"sync_enabled"`
	SyncMode            string   `json:"sync_mode,omitempty"`
	RAGEnabled          bool     `json:"rag_enabled"`
	Collections         []string `json:"collections,omitempty"`
	HeadIntervalSeconds int      `json:"head_interval_seconds,omitempty"`
	RAGIntervalSeconds  int      `json:"rag_interval_seconds,omitempty"`
	HeadMaxPages        int      `json:"head_max_pages,omitempty"`
	TailSlicePages      int      `json:"tail_slice_pages,omitempty"`
	PerPage             int      `json:"per_page,omitempty"`
	Profile             string   `json:"profile,omitempty"`
}

type DiagnosticObservation struct {
	ID           string     `json:"id"`
	Severity     string     `json:"severity"`
	EntityType   string     `json:"entity_type"`
	EntityID     string     `json:"entity_id"`
	FailureClass string     `json:"failure_class"`
	Message      string     `json:"message"`
	Retryable    bool       `json:"retryable"`
	Current      bool       `json:"current"`
	ObservedAt   *time.Time `json:"observed_at,omitempty"`
	Remediation  string     `json:"remediation,omitempty"`
}

type CapabilityObservation struct {
	ID          string `json:"id"`
	Category    string `json:"category"`
	SafetyClass string `json:"safety_class"`
	Description string `json:"description"`
	UIEnabled   bool   `json:"ui_enabled"`
	UIReason    string `json:"ui_reason,omitempty"`
	CLIName     string `json:"cli_name,omitempty"`
	CLIEnabled  bool   `json:"cli_enabled"`
	MCPName     string `json:"mcp_name,omitempty"`
	MCPEnabled  bool   `json:"mcp_enabled"`
}

type Event struct {
	Cursor     string    `json:"cursor"`
	Kind       string    `json:"kind"`
	EntityType string    `json:"entity_type,omitempty"`
	EntityID   string    `json:"entity_id,omitempty"`
	Revision   string    `json:"revision,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
	sequence   uint64
}

type eventLog struct {
	mu       sync.Mutex
	capacity int
	next     uint64
	events   []Event
	notify   chan struct{}
}

func newEventLog(capacity int) *eventLog {
	if capacity <= 0 {
		capacity = 256
	}
	return &eventLog{capacity: capacity, notify: make(chan struct{})}
}

func (l *eventLog) append(event Event) Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.next++
	event.sequence = l.next
	event.Cursor = encodeEventCursor(l.next)
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	l.events = append(l.events, event)
	if len(l.events) > l.capacity {
		l.events = append([]Event(nil), l.events[len(l.events)-l.capacity:]...)
	}
	close(l.notify)
	l.notify = make(chan struct{})
	return event
}

func (l *eventLog) replay(after uint64) (events []Event, expired bool, latest uint64, notify <-chan struct{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	latest = l.next
	if after > latest || (len(l.events) > 0 && after > 0 && after+1 < l.events[0].sequence) {
		expired = true
	} else {
		for _, event := range l.events {
			if event.sequence > after {
				events = append(events, event)
			}
		}
	}
	return events, expired, latest, l.notify
}

func FinalizeSnapshot(snapshot ObservationSnapshot, now time.Time) ObservationSnapshot {
	snapshot.APIVersion = observationAPIVersion
	snapshot.Revision = ""
	snapshot.GeneratedAt = time.Time{}
	normalizeSnapshotSlices(&snapshot)
	sortSnapshot(&snapshot)
	data, _ := json.Marshal(snapshot)
	hash := sha256.Sum256(data)
	snapshot.Revision = "snapshot-" + hex.EncodeToString(hash[:8])
	snapshot.GeneratedAt = now.UTC()
	return snapshot
}

func normalizeSnapshotSlices(snapshot *ObservationSnapshot) {
	if snapshot.Attention == nil {
		snapshot.Attention = []AttentionItem{}
	}
	if snapshot.Caches == nil {
		snapshot.Caches = []CacheObservation{}
	}
	if snapshot.Jobs == nil {
		snapshot.Jobs = []JobObservation{}
	}
	if snapshot.JobRetention.RetainedByStatus == nil {
		snapshot.JobRetention.RetainedByStatus = []JobStatusCount{}
	}
	if snapshot.Maintenance == nil {
		snapshot.Maintenance = []MaintenanceObservation{}
	}
	if snapshot.Diagnostics == nil {
		snapshot.Diagnostics = []DiagnosticObservation{}
	}
	if snapshot.Capabilities == nil {
		snapshot.Capabilities = []CapabilityObservation{}
	}
	for i := range snapshot.Caches {
		if snapshot.Caches[i].Repositories == nil {
			snapshot.Caches[i].Repositories = []RepositoryObservation{}
		}
	}
}

func sortSnapshot(snapshot *ObservationSnapshot) {
	sort.Slice(snapshot.Attention, func(i, j int) bool { return snapshot.Attention[i].ID < snapshot.Attention[j].ID })
	sort.Slice(snapshot.Caches, func(i, j int) bool { return snapshot.Caches[i].CacheRef < snapshot.Caches[j].CacheRef })
	for i := range snapshot.Caches {
		cache := &snapshot.Caches[i]
		sort.Slice(cache.Repositories, func(i, j int) bool { return cache.Repositories[i].RepoID < cache.Repositories[j].RepoID })
		for j := range cache.Repositories {
			repo := &cache.Repositories[j]
			if repo.Collections == nil {
				repo.Collections = []CollectionObservation{}
			}
			if repo.RecentSync == nil {
				repo.RecentSync = []SyncEventObservation{}
			}
			sort.Strings(repo.Aliases)
			sort.Strings(repo.Scopes)
			sort.Slice(repo.Counts.ByKind, func(i, j int) bool { return repo.Counts.ByKind[i].Kind < repo.Counts.ByKind[j].Kind })
			sort.Slice(repo.Collections, func(i, j int) bool { return repo.Collections[i].Kind < repo.Collections[j].Kind })
			sort.Slice(repo.RecentSync, func(i, j int) bool {
				if repo.RecentSync[i].CompletedAt.Equal(repo.RecentSync[j].CompletedAt) {
					return repo.RecentSync[i].ID > repo.RecentSync[j].ID
				}
				return repo.RecentSync[i].CompletedAt.After(repo.RecentSync[j].CompletedAt)
			})
			sort.Strings(repo.Execution.ActiveJobIDs)
			sort.Slice(repo.Execution.LastErrors, func(i, j int) bool { return repo.Execution.LastErrors[i].Stage < repo.Execution.LastErrors[j].Stage })
		}
	}
	sort.Slice(snapshot.Jobs, func(i, j int) bool { return snapshot.Jobs[i].ID < snapshot.Jobs[j].ID })
	sort.Slice(snapshot.JobRetention.RetainedByStatus, func(i, j int) bool {
		return snapshot.JobRetention.RetainedByStatus[i].Status < snapshot.JobRetention.RetainedByStatus[j].Status
	})
	sort.Slice(snapshot.Maintenance, func(i, j int) bool {
		return snapshot.Maintenance[i].RegistrationID < snapshot.Maintenance[j].RegistrationID
	})
	for i := range snapshot.Maintenance {
		sort.Strings(snapshot.Maintenance[i].Policy.Collections)
	}
	sort.Slice(snapshot.Diagnostics, func(i, j int) bool { return snapshot.Diagnostics[i].ID < snapshot.Diagnostics[j].ID })
	sort.Slice(snapshot.Capabilities, func(i, j int) bool { return snapshot.Capabilities[i].ID < snapshot.Capabilities[j].ID })
}

func encodeEventCursor(value uint64) string {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, value)
	return base64.RawURLEncoding.EncodeToString(buf)
}

func decodeEventCursor(value string) (uint64, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	buf, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(buf) != 8 {
		return 0, errors.New("invalid event cursor")
	}
	return binary.BigEndian.Uint64(buf), nil
}

func (c *Controller) snapshot(ctx context.Context) (ObservationSnapshot, error) {
	if c.cfg.Snapshot == nil {
		return ObservationSnapshot{}, errors.New("admin observation is unavailable")
	}
	snapshot, err := c.cfg.Snapshot(ctx)
	if err != nil {
		return ObservationSnapshot{}, err
	}
	if snapshot.APIVersion == "" || snapshot.Revision == "" {
		snapshot = FinalizeSnapshot(snapshot, time.Now())
	}
	return snapshot, nil
}

func (c *Controller) getSnapshot(w http.ResponseWriter, r *http.Request) {
	if !c.requireSession(w, r) {
		return
	}
	snapshot, err := c.snapshot(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "observation_unavailable", "Admin observation is unavailable.", "Run gitcode-mcp service doctor.")
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (c *Controller) getCaches(w http.ResponseWriter, r *http.Request) {
	if !c.requireSession(w, r) {
		return
	}
	snapshot, err := c.snapshot(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "observation_unavailable", "Cache observation is unavailable.", "Run gitcode-mcp service doctor.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_version": observationAPIVersion, "revision": snapshot.Revision, "caches": snapshot.Caches})
}

func (c *Controller) getCache(w http.ResponseWriter, r *http.Request) {
	if !c.requireSession(w, r) {
		return
	}
	snapshot, err := c.snapshot(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "observation_unavailable", "Cache observation is unavailable.", "Run gitcode-mcp service doctor.")
		return
	}
	cacheRef := strings.TrimSpace(r.PathValue("cache_ref"))
	for _, cache := range snapshot.Caches {
		if cache.CacheRef == cacheRef {
			writeJSON(w, http.StatusOK, map[string]any{"api_version": observationAPIVersion, "revision": snapshot.Revision, "cache": cache})
			return
		}
	}
	writeAPIError(w, http.StatusNotFound, "cache_not_found", "The selected cache is not managed by this daemon.", "Refresh the snapshot and select a listed cache_ref.")
}

func (c *Controller) getRepository(w http.ResponseWriter, r *http.Request) {
	if !c.requireSession(w, r) {
		return
	}
	snapshot, err := c.snapshot(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "observation_unavailable", "Repository observation is unavailable.", "Run gitcode-mcp service doctor.")
		return
	}
	cacheRef := strings.TrimSpace(r.PathValue("cache_ref"))
	repoID := strings.Trim(strings.TrimSpace(r.PathValue("repo_id")), "/")
	for _, cache := range snapshot.Caches {
		if cache.CacheRef != cacheRef {
			continue
		}
		for _, repo := range cache.Repositories {
			if repo.RepoID == repoID {
				writeJSON(w, http.StatusOK, map[string]any{"api_version": observationAPIVersion, "revision": snapshot.Revision, "repository": repo})
				return
			}
		}
	}
	writeAPIError(w, http.StatusNotFound, "repository_not_found", "The selected repository is not present in this cache.", "Refresh the snapshot and select a listed repository.")
}

func (c *Controller) getJobs(w http.ResponseWriter, r *http.Request) {
	if !c.requireSession(w, r) {
		return
	}
	snapshot, err := c.snapshot(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "observation_unavailable", "Job observation is unavailable.", "Run gitcode-mcp service doctor.")
		return
	}
	jobs := make([]JobObservation, 0, len(snapshot.Jobs))
	for _, job := range snapshot.Jobs {
		if value := r.URL.Query().Get("state"); value != "" && job.Status != value {
			continue
		}
		if value := r.URL.Query().Get("type"); value != "" && job.Type != value {
			continue
		}
		if value := r.URL.Query().Get("cache_ref"); value != "" && job.CacheRef != value {
			continue
		}
		if value := r.URL.Query().Get("repo_id"); value != "" && job.RepoID != value {
			continue
		}
		if value := r.URL.Query().Get("failure_class"); value != "" && job.FailureClass != value {
			continue
		}
		if cursor := r.URL.Query().Get("after"); cursor != "" && job.ID <= cursor {
			continue
		}
		jobs = append(jobs, job)
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 100 {
			writeAPIError(w, http.StatusBadRequest, "invalid_query", "limit must be between 1 and 100.", "Use a bounded positive job page size.")
			return
		}
		limit = parsed
	}
	next := ""
	if len(jobs) > limit {
		next = jobs[limit-1].ID
		jobs = jobs[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_version": observationAPIVersion, "revision": snapshot.Revision, "jobs": jobs, "next_cursor": next})
}

func (c *Controller) getJob(w http.ResponseWriter, r *http.Request) {
	if !c.requireSession(w, r) {
		return
	}
	snapshot, err := c.snapshot(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "observation_unavailable", "Job observation is unavailable.", "Run gitcode-mcp service doctor.")
		return
	}
	jobID := strings.TrimSpace(r.PathValue("job_id"))
	for _, job := range snapshot.Jobs {
		if job.ID == jobID {
			writeJSON(w, http.StatusOK, map[string]any{"api_version": observationAPIVersion, "revision": snapshot.Revision, "job": job})
			return
		}
	}
	writeAPIError(w, http.StatusNotFound, "job_not_retained", "The selected job has expired or is not retained by this daemon.", "Return to the retained job list; cached repository data and maintenance state are unaffected.")
}

func (c *Controller) getMaintenance(w http.ResponseWriter, r *http.Request) {
	c.writeSnapshotCollection(w, r, "maintenance")
}

func (c *Controller) getDiagnostics(w http.ResponseWriter, r *http.Request) {
	c.writeSnapshotCollection(w, r, "diagnostics")
}

func (c *Controller) getCapabilities(w http.ResponseWriter, r *http.Request) {
	c.writeSnapshotCollection(w, r, "capabilities")
}

func (c *Controller) writeSnapshotCollection(w http.ResponseWriter, r *http.Request, name string) {
	if !c.requireSession(w, r) {
		return
	}
	snapshot, err := c.snapshot(r.Context())
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "observation_unavailable", "Admin observation is unavailable.", "Run gitcode-mcp service doctor.")
		return
	}
	value := map[string]any{"api_version": observationAPIVersion, "revision": snapshot.Revision}
	switch name {
	case "maintenance":
		value[name] = snapshot.Maintenance
	case "diagnostics":
		value[name] = snapshot.Diagnostics
	case "capabilities":
		value[name] = snapshot.Capabilities
	}
	writeJSON(w, http.StatusOK, value)
}

func (c *Controller) getEvents(w http.ResponseWriter, r *http.Request) {
	session, authenticated := c.authenticate(r)
	if !authenticated {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "Admin session required.", "Run gitcode-mcp admin open.")
		return
	}
	afterValue := r.URL.Query().Get("after")
	if afterValue == "" {
		afterValue = r.Header.Get("Last-Event-ID")
	}
	after, err := decodeEventCursor(afterValue)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_cursor", "The event cursor is invalid.", "Refresh the snapshot and reconnect without a cursor.")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeAPIError(w, http.StatusInternalServerError, "stream_unavailable", "Event streaming is unavailable.", "Use the snapshot endpoint.")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	expires := time.NewTimer(time.Until(session.Expires))
	defer expires.Stop()
	for {
		events, expired, latest, notify := c.events.replay(after)
		if expired {
			event := Event{Cursor: encodeEventCursor(latest), Kind: "snapshot_required", OccurredAt: time.Now().UTC()}
			writeSSE(w, event)
			flusher.Flush()
			return
		}
		for _, event := range events {
			writeSSE(w, event)
			after = event.sequence
		}
		if len(events) > 0 {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-expires.C:
			return
		case <-notify:
		case <-keepalive.C:
			_, _ = fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func (c *Controller) requireSession(w http.ResponseWriter, r *http.Request) bool {
	if _, ok := c.authenticate(r); !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized", "Admin session required.", "Run gitcode-mcp admin open.")
		return false
	}
	return true
}

func writeSSE(w http.ResponseWriter, event Event) {
	data, _ := json.Marshal(event)
	_, _ = fmt.Fprintf(w, "id: %s\nevent: %s\ndata: %s\n\n", event.Cursor, event.Kind, data)
}

func writeAPIError(w http.ResponseWriter, status int, code, message, remediation string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message, "remediation": remediation}})
}
