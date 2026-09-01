package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/service"
)

const (
	JobStatusQueued      = "queued"
	JobStatusRunning     = "running"
	JobStatusCancelling  = "cancelling"
	JobStatusSucceeded   = "succeeded"
	JobStatusSuperseded  = "superseded"
	JobStatusFailed      = "failed"
	JobStatusCancelled   = "cancelled"
	JobStatusInterrupted = "interrupted"

	maxStoredTerminalJobs   = config.DefaultJobMaxTerminal
	maxStoredProgressEvents = config.DefaultJobMaxProgressEvents
)

type Job struct {
	ID                           string                  `json:"id"`
	Type                         string                  `json:"type"`
	RepoID                       string                  `json:"repo_id,omitempty"`
	ProfileID                    string                  `json:"profile_id,omitempty"`
	CacheUUID                    string                  `json:"cache_uuid,omitempty"`
	RegistrationID               string                  `json:"registration_id,omitempty"`
	NamespaceID                  string                  `json:"namespace_id,omitempty"`
	SourceRegistrationID         string                  `json:"source_registration_id,omitempty"`
	SourceRegistrationGeneration int64                   `json:"source_registration_generation,omitempty"`
	ExpectedRevisionSetID        string                  `json:"expected_revision_set_id,omitempty"`
	WorkKey                      string                  `json:"-"`
	WorkRef                      string                  `json:"work_ref,omitempty"`
	Status                       string                  `json:"status"`
	CreatedAt                    time.Time               `json:"created_at"`
	StartedAt                    *time.Time              `json:"started_at,omitempty"`
	UpdatedAt                    time.Time               `json:"updated_at"`
	FinishedAt                   *time.Time              `json:"finished_at,omitempty"`
	Steps                        int                     `json:"steps,omitempty"`
	Completed                    int                     `json:"completed,omitempty"`
	Error                        string                  `json:"error,omitempty"`
	ErrorClass                   string                  `json:"error_class,omitempty"`
	SyncStage                    *SyncStageView          `json:"sync_stage,omitempty"`
	Progress                     []service.ProgressEvent `json:"progress,omitempty"`
}

type JobManager struct {
	mu                                  sync.Mutex
	jobs                                map[string]*Job
	cancel                              map[string]context.CancelFunc
	cancelResolution                    map[string]*jobCancellationResolution
	nextID                              int
	snapshotPath                        string
	now                                 func() time.Time
	writeFile                           func(string, []byte, os.FileMode) error
	retention                           config.ServiceJobRetentionConfig
	expiredTotal                        int
	truncatedTotal                      int
	lastExpired                         int
	lastTruncated                       int
	registrationRedirects               map[string]string
	sourceRegistrationRedirects         map[string]string
	canonicalRepoByRegistration         map[string]string
	lastPrunedAt                        *time.Time
	onRepositoryDocsCancelled           func(Job) error
	repositoryDocsCancellationCommitted func(Job) bool
	cacheMutationFences                 map[string]bool
	inflightWorkers                     map[string]bool
	directCacheWriters                  map[string]string
}

type jobCancellationResolution struct {
	done  chan struct{}
	job   Job
	found bool
	err   error
}

type JobRetentionSnapshot struct {
	Policy         config.ServiceJobRetentionConfig
	Active         int
	Terminal       int
	ByStatus       map[string]int
	OldestRetained *time.Time
	LastPrunedAt   *time.Time
	ExpiredTotal   int
	TruncatedTotal int
	LastExpired    int
	LastTruncated  int
}

type ErrCacheWriterBusy struct {
	ActiveJobID string
	ActiveType  string
}

var ErrJobAdmissionPersistence = errors.New("service job admission could not be persisted")
var ErrJobSnapshotPersistence = errors.New("service job terminal snapshot could not be persisted")

type JobAdmissionPersistenceError struct{}

func (e JobAdmissionPersistenceError) Error() string { return ErrJobAdmissionPersistence.Error() }

func (e JobAdmissionPersistenceError) Unwrap() error { return ErrJobAdmissionPersistence }

func (e JobAdmissionPersistenceError) DiagnosticCode() string {
	return "job_admission_persistence_failed"
}

type JobSnapshotPersistenceError struct{}

func (e JobSnapshotPersistenceError) Error() string { return ErrJobSnapshotPersistence.Error() }

func (e JobSnapshotPersistenceError) Unwrap() error { return ErrJobSnapshotPersistence }

func (e JobSnapshotPersistenceError) DiagnosticCode() string {
	return "job_snapshot_persistence_failed"
}

func (e ErrCacheWriterBusy) Error() string {
	if e.ActiveJobID == "" {
		return "cache writer is busy"
	}
	return fmt.Sprintf("cache writer is busy (%s %s)", e.ActiveType, e.ActiveJobID)
}

func (e ErrCacheWriterBusy) DiagnosticCode() string { return "cache_writer_busy" }

type StartFakeJobRequest struct {
	Steps      int `json:"steps,omitempty"`
	IntervalMS int `json:"interval_ms,omitempty"`
}

func NewJobManager(snapshotPath string) *JobManager {
	return NewJobManagerWithRetention(snapshotPath, config.ServiceJobRetentionConfig{
		SuccessTTL: config.DefaultJobSuccessTTL, DiagnosticTTL: config.DefaultJobDiagnosticTTL,
		MaxTerminalJobs: config.DefaultJobMaxTerminal, MaxDiagnosticJobs: config.DefaultJobMaxDiagnostic,
		MaxProgressEvents: config.DefaultJobMaxProgressEvents,
	})
}

func NewJobManagerWithRetention(snapshotPath string, retention config.ServiceJobRetentionConfig) *JobManager {
	if err := config.ValidateServiceJobRetention(retention); err != nil {
		panic(err)
	}
	return &JobManager{
		jobs:                        map[string]*Job{},
		cancel:                      map[string]context.CancelFunc{},
		cancelResolution:            map[string]*jobCancellationResolution{},
		snapshotPath:                snapshotPath,
		now:                         func() time.Time { return time.Now().UTC() },
		writeFile:                   durableAtomicWriteFile,
		registrationRedirects:       map[string]string{},
		sourceRegistrationRedirects: map[string]string{},
		canonicalRepoByRegistration: map[string]string{},
		cacheMutationFences:         map[string]bool{},
		inflightWorkers:             map[string]bool{},
		directCacheWriters:          map[string]string{},
		retention:                   retention,
	}
}

func (m *JobManager) SetRegistrationRedirects(redirects, sourceRedirects map[string]string, repoIDs map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.registrationRedirects = map[string]string{}
	m.sourceRegistrationRedirects = map[string]string{}
	m.canonicalRepoByRegistration = map[string]string{}
	for from, to := range redirects {
		from, to = strings.TrimSpace(from), strings.TrimSpace(to)
		if from != "" && to != "" && from != to {
			m.registrationRedirects[from] = to
		}
	}
	for from, to := range sourceRedirects {
		from, to = strings.TrimSpace(from), strings.TrimSpace(to)
		if from != "" && to != "" && from != to {
			m.sourceRegistrationRedirects[from] = to
		}
	}
	for registrationID, repoID := range repoIDs {
		if registrationID, repoID = strings.TrimSpace(registrationID), strings.TrimSpace(repoID); registrationID != "" && repoID != "" {
			m.canonicalRepoByRegistration[registrationID] = repoID
		}
	}
	for _, job := range m.jobs {
		m.projectCanonicalRegistrationLocked(job)
	}
}

func (m *JobManager) projectCanonicalRegistrationLocked(job *Job) {
	if job == nil {
		return
	}
	job.RegistrationID = resolveJobRedirect(job.RegistrationID, m.registrationRedirects)
	if canonicalRepo := m.canonicalRepoByRegistration[job.RegistrationID]; canonicalRepo != "" {
		job.RepoID = canonicalRepo
	}
	job.SourceRegistrationID = resolveJobRedirect(job.SourceRegistrationID, m.sourceRegistrationRedirects)
}

func resolveJobRedirect(value string, redirects map[string]string) string {
	original := value
	seen := map[string]bool{}
	for redirects[value] != "" {
		if seen[value] {
			return original
		}
		seen[value] = true
		value = redirects[value]
	}
	return value
}

type CacheMutationFenceError struct{}

func (CacheMutationFenceError) Error() string          { return "service: cache authority is being changed" }
func (CacheMutationFenceError) DiagnosticCode() string { return "cache_authority_fenced" }

// BeginCacheMutationFence prevents a new writer admission and reports both
// public active jobs and workers that are still unwinding after cancellation.
// It never calls MaintenanceManager while holding the job lock.
func (m *JobManager) BeginCacheMutationFence(cacheUUID string) (func(), []string) {
	cacheUUID = strings.TrimSpace(cacheUUID)
	if cacheUUID == "" {
		return func() {}, []string{"unknown-cache-authority"}
	}
	m.mu.Lock()
	if m.cacheMutationFences[cacheUUID] {
		m.mu.Unlock()
		return func() {}, []string{"concurrent-conflict-resolution"}
	}
	m.cacheMutationFences[cacheUUID] = true
	blocked := []string{}
	for id, job := range m.jobs {
		if job != nil && job.CacheUUID == cacheUUID && (jobActiveStatus(job.Status) || m.inflightWorkers[id]) {
			blocked = append(blocked, id)
		}
	}
	if writerID := m.directCacheWriters[cacheUUID]; writerID != "" {
		blocked = append(blocked, writerID)
	}
	sort.Strings(blocked)
	m.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			delete(m.cacheMutationFences, cacheUUID)
			m.mu.Unlock()
		})
	}, blocked
}

// BeginDirectCacheWriter reserves the same logical-cache writer lane used by
// daemon jobs for a bounded synchronous mutation such as Admin binding apply.
// The returned release must be called after the SQLite mutation completes.
func (m *JobManager) BeginDirectCacheWriter(cacheUUID, writerID string) (func(), error) {
	cacheUUID, writerID = strings.TrimSpace(cacheUUID), strings.TrimSpace(writerID)
	if cacheUUID == "" || writerID == "" {
		return func() {}, CacheWriterIdentityError{code: "cache_authority_unavailable"}
	}
	m.mu.Lock()
	if m.cacheMutationFences[cacheUUID] {
		m.mu.Unlock()
		return func() {}, CacheMutationFenceError{}
	}
	if active, ok := m.activeCacheWriterLocked(cacheUUID); ok {
		m.mu.Unlock()
		return func() {}, ErrCacheWriterBusy{ActiveJobID: active.ID, ActiveType: active.Type}
	}
	if activeID := m.directCacheWriters[cacheUUID]; activeID != "" {
		m.mu.Unlock()
		return func() {}, ErrCacheWriterBusy{ActiveJobID: activeID, ActiveType: "direct_cache_write"}
	}
	m.directCacheWriters[cacheUUID] = writerID
	m.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			if m.directCacheWriters[cacheUUID] == writerID {
				delete(m.directCacheWriters, cacheUUID)
			}
			m.mu.Unlock()
		})
	}, nil
}

func (m *JobManager) markWorkerStarted(jobID string) {
	m.mu.Lock()
	m.inflightWorkers[jobID] = true
	m.mu.Unlock()
}

func (m *JobManager) markWorkerFinished(jobID string) {
	m.mu.Lock()
	delete(m.inflightWorkers, jobID)
	m.mu.Unlock()
}

func (m *JobManager) LoadAndMarkInterrupted() error {
	if m.snapshotPath == "" {
		return nil
	}
	data, err := os.ReadFile(m.snapshotPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var jobs []Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		return err
	}
	now := m.now()
	maxID := 0
	for i := range jobs {
		job := jobs[i]
		m.mu.Lock()
		m.projectCanonicalRegistrationLocked(&job)
		cancellationCommitted := m.repositoryDocsCancellationCommitted
		m.mu.Unlock()
		if job.Status == JobStatusCancelling && cancellationCommitted != nil && cancellationCommitted(job) {
			job.Status = JobStatusCancelled
			job.UpdatedAt = now
			job.FinishedAt = &now
			job.ErrorClass = "cancelled"
			job.Error = publicMaintenanceJobError(job.Type, job.ErrorClass)
			job.Progress = append(job.Progress, service.ProgressEvent{Type: JobStatusCancelled, Phase: JobStatusCancelled, Collection: job.Type, Message: "durable cancellation recovered after service restart"})
		} else if jobActiveStatus(job.Status) {
			job.Status = JobStatusInterrupted
			job.UpdatedAt = now
			job.FinishedAt = &now
			job.Error = "service restarted before job completed"
			job.Progress = append(job.Progress, service.ProgressEvent{Type: "interrupted", Phase: "interrupted", Message: job.Error})
		}
		sanitizeStoredMaintenanceJob(&job)
		trimJobProgress(&job, m.retention.MaxProgressEvents)
		idNum := parseJobIDNumber(job.ID)
		if idNum > maxID {
			maxID = idNum
		}
		jobCopy := job
		m.mu.Lock()
		m.jobs[job.ID] = &jobCopy
		m.mu.Unlock()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if maxID > m.nextID {
		m.nextID = maxID
	}
	return m.saveLocked()
}

func sanitizeStoredMaintenanceJob(job *Job) {
	if job == nil || (job.Type != SyncJobType && job.Type != RAGIndexJobType && job.Type != RepositoryDocsIndexJobType) {
		return
	}
	if job.Error != "" {
		job.ErrorClass = sanitizeMaintenanceErrorClass(job.ErrorClass, job.Type+"_failed")
		job.Error = publicMaintenanceJobError(job.Type, job.ErrorClass)
	}
	for i := range job.Progress {
		job.Progress[i] = sanitizeMaintenanceProgress(job.Type, job.Progress[i])
	}
}

func (m *JobManager) StartFake(ctx context.Context, req StartFakeJobRequest) (Job, error) {
	steps := req.Steps
	if steps <= 0 {
		steps = 5
	}
	interval := time.Duration(req.IntervalMS) * time.Millisecond
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	ctx, cancel := context.WithCancel(ctx)
	job := m.createJob("fake", steps, cancel)
	go m.runFakeJob(ctx, job.ID, steps, interval)
	return job, nil
}

func (m *JobManager) ActiveJob(jobType, repoID string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, job := range m.jobs {
		if job.Type != jobType || job.RepoID != repoID {
			continue
		}
		if jobActiveStatus(job.Status) {
			return cloneJob(job), true
		}
	}
	return Job{}, false
}

func (m *JobManager) ActiveWork(workKey string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, job := range m.jobs {
		candidate := job.WorkKey
		if candidate == "" {
			candidate = job.Type + ":" + job.RepoID
		}
		if candidate != workKey {
			continue
		}
		if jobActiveStatus(job.Status) {
			return cloneJob(job), true
		}
	}
	return Job{}, false
}

// ActiveCacheRepo returns active maintenance work for one logical cache and
// repository. This prevents head/tail or sync/index jobs from overlapping in a
// cache without serializing an unrelated cache that happens to contain the
// same repository.
func (m *JobManager) ActiveCacheRepo(jobType, cacheUUID, repoID string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, job := range m.jobs {
		if job.Type != jobType || job.CacheUUID != cacheUUID || job.RepoID != repoID {
			continue
		}
		if jobActiveStatus(job.Status) {
			return cloneJob(job), true
		}
	}
	return Job{}, false
}

// ActiveCacheWriter returns the writer currently admitted for a logical cache.
// Sync and RAG indexing both mutate cache-owned state and therefore share this
// admission boundary even when they target different repository bindings.
func (m *JobManager) ActiveCacheWriter(cacheUUID string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeCacheWriterLocked(cacheUUID)
}

func (m *JobManager) activeCacheWriterLocked(cacheUUID string) (Job, bool) {
	cacheUUID = strings.TrimSpace(cacheUUID)
	if cacheUUID == "" {
		return Job{}, false
	}
	if writerID := m.directCacheWriters[cacheUUID]; writerID != "" {
		return Job{ID: writerID, Type: "direct_cache_write", CacheUUID: cacheUUID, Status: JobStatusRunning}, true
	}
	for id, job := range m.jobs {
		if job.CacheUUID != cacheUUID || !isCacheWriterJob(job.Type) {
			continue
		}
		if jobActiveStatus(job.Status) || m.inflightWorkers[id] {
			return cloneJob(job), true
		}
	}
	return Job{}, false
}

func isCacheWriterJob(jobType string) bool {
	return jobType == SyncJobType || jobType == RAGIndexJobType || jobType == RepositoryDocsIndexJobType
}

func (m *JobManager) LatestCacheRepo(jobType, cacheUUID, repoID string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest *Job
	for _, job := range m.jobs {
		if job.Type != jobType || job.CacheUUID != cacheUUID || job.RepoID != repoID {
			continue
		}
		if latest == nil || parseJobIDNumber(job.ID) > parseJobIDNumber(latest.ID) {
			latest = job
		}
	}
	if latest == nil {
		return Job{}, false
	}
	return cloneJob(latest), true
}

func (m *JobManager) ActiveRepositoryDocsSource(cacheUUID, repoID, sourceRegistrationID string, generation int64) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, job := range m.jobs {
		if job.Type == RepositoryDocsIndexJobType && job.CacheUUID == cacheUUID && job.RepoID == repoID &&
			job.SourceRegistrationID == sourceRegistrationID && job.SourceRegistrationGeneration == generation && jobActiveStatus(job.Status) {
			return cloneJob(job), true
		}
	}
	return Job{}, false
}

func (m *JobManager) LatestRepositoryDocsSource(cacheUUID, repoID, sourceRegistrationID string, generation int64) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest *Job
	for _, job := range m.jobs {
		if job.Type != RepositoryDocsIndexJobType || job.CacheUUID != cacheUUID || job.RepoID != repoID || job.SourceRegistrationID != sourceRegistrationID || job.SourceRegistrationGeneration != generation {
			continue
		}
		if latest == nil || parseJobIDNumber(job.ID) > parseJobIDNumber(latest.ID) {
			latest = job
		}
	}
	if latest == nil {
		return Job{}, false
	}
	return cloneJob(latest), true
}

// ResumeRepositoryDocsAdmission reuses the public identity already durably
// allocated for an interrupted or failed admission. Exact opaque intent
// matching fences unrelated or stale work from being revived; callers remain
// responsible for enforcing the durable retry window before invoking it.
func (m *JobManager) ResumeRepositoryDocsAdmission(jobID, registrationID, sourceRegistrationID string, generation int64, expectedSetID, workKey string, cancel context.CancelFunc) (Job, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[strings.TrimSpace(jobID)]
	if job == nil || job.Type != RepositoryDocsIndexJobType || !repositoryDocsAdmissionRecoverable(job.Status) ||
		job.RegistrationID != strings.TrimSpace(registrationID) || job.SourceRegistrationID != strings.TrimSpace(sourceRegistrationID) ||
		job.SourceRegistrationGeneration != generation || job.ExpectedRevisionSetID != strings.TrimSpace(expectedSetID) || job.WorkRef != publicWorkRef(workKey) {
		return Job{}, false, nil
	}
	if m.cacheMutationFences[strings.TrimSpace(job.CacheUUID)] {
		return Job{}, false, CacheMutationFenceError{}
	}
	if activeID := m.directCacheWriters[strings.TrimSpace(job.CacheUUID)]; activeID != "" {
		return Job{}, false, ErrCacheWriterBusy{ActiveJobID: activeID, ActiveType: "direct_cache_write"}
	}
	if active, ok := m.activeCacheWriterLocked(job.CacheUUID); ok {
		return Job{}, false, ErrCacheWriterBusy{ActiveJobID: active.ID, ActiveType: active.Type}
	}
	previous := cloneJob(job)
	job.WorkKey = workKey
	job.Status = JobStatusQueued
	job.StartedAt = nil
	job.FinishedAt = nil
	job.Error = ""
	job.ErrorClass = ""
	job.Steps = 0
	job.Completed = 0
	job.UpdatedAt = m.now()
	job.Progress = append(job.Progress, service.ProgressEvent{Type: "resumed", Phase: "queued", Collection: RepositoryDocsIndexJobType, Message: "durable repository documentation admission resumed"})
	m.cancel[job.ID] = cancel
	m.inflightWorkers[job.ID] = true
	if err := m.saveLocked(); err != nil {
		*job = previous
		delete(m.cancel, job.ID)
		delete(m.inflightWorkers, job.ID)
		return Job{}, false, JobAdmissionPersistenceError{}
	}
	return cloneJob(job), true, nil
}

func (m *JobManager) RecoverableRepositoryDocsAdmission(registrationID, sourceRegistrationID string, generation int64, expectedSetID, workKey string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest *Job
	for _, job := range m.jobs {
		if job.Type != RepositoryDocsIndexJobType || !repositoryDocsAdmissionRecoverable(job.Status) || job.RegistrationID != strings.TrimSpace(registrationID) ||
			job.SourceRegistrationID != strings.TrimSpace(sourceRegistrationID) || job.SourceRegistrationGeneration != generation ||
			job.ExpectedRevisionSetID != strings.TrimSpace(expectedSetID) || job.WorkRef != publicWorkRef(workKey) {
			continue
		}
		if latest == nil || parseJobIDNumber(job.ID) > parseJobIDNumber(latest.ID) {
			latest = job
		}
	}
	if latest == nil {
		return Job{}, false
	}
	return cloneJob(latest), true
}

func repositoryDocsAdmissionRecoverable(status string) bool {
	return status == JobStatusInterrupted || status == JobStatusFailed
}

func (m *JobManager) SetWorkIdentity(id, workKey, cacheUUID, registrationID, namespaceID string) Job {
	m.updateJob(id, func(job *Job, now time.Time) {
		job.WorkKey = workKey
		job.WorkRef = publicWorkRef(workKey)
		job.CacheUUID = cacheUUID
		job.RegistrationID = registrationID
		job.NamespaceID = namespaceID
		job.UpdatedAt = now
	})
	return m.mustGet(id)
}

func (m *JobManager) createCoalescedJob(jobType, repoID, profileID string, steps int, workKey, cacheUUID, registrationID, namespaceID string, cancel context.CancelFunc) (Job, bool, error) {
	return m.createCoalescedJobWithIntent(jobType, repoID, profileID, steps, workKey, cacheUUID, registrationID, namespaceID, JobRecoveryIntent{}, cancel)
}

type JobRecoveryIntent struct {
	SourceRegistrationID         string
	SourceRegistrationGeneration int64
	ExpectedRevisionSetID        string
}

func (m *JobManager) createCoalescedJobWithIntent(jobType, repoID, profileID string, steps int, workKey, cacheUUID, registrationID, namespaceID string, intent JobRecoveryIntent, cancel context.CancelFunc) (Job, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cacheUUID = strings.TrimSpace(cacheUUID); cacheUUID != "" && m.cacheMutationFences[cacheUUID] {
		return Job{}, false, CacheMutationFenceError{}
	}
	for _, job := range m.jobs {
		if job.WorkKey == workKey && jobActiveStatus(job.Status) {
			return cloneJob(job), false, nil
		}
	}
	writer := isCacheWriterJob(jobType)
	if writer {
		if activeID := m.directCacheWriters[cacheUUID]; activeID != "" {
			return Job{}, false, ErrCacheWriterBusy{ActiveJobID: activeID, ActiveType: "direct_cache_write"}
		}
		if active, ok := m.activeCacheWriterLocked(cacheUUID); ok {
			return Job{}, false, ErrCacheWriterBusy{ActiveJobID: active.ID, ActiveType: active.Type}
		}
	}
	m.nextID++
	now := m.now()
	id := fmt.Sprintf("job-%06d", m.nextID)
	job := &Job{
		ID: id, Type: jobType, RepoID: repoID, ProfileID: profileID,
		CacheUUID: cacheUUID, RegistrationID: registrationID, NamespaceID: namespaceID, WorkKey: workKey,
		SourceRegistrationID: intent.SourceRegistrationID, SourceRegistrationGeneration: intent.SourceRegistrationGeneration, ExpectedRevisionSetID: intent.ExpectedRevisionSetID,
		Status: JobStatusQueued, CreatedAt: now, UpdatedAt: now, Steps: steps, WorkRef: publicWorkRef(workKey),
	}
	m.jobs[id] = job
	m.cancel[id] = cancel
	if writer {
		// Reserve the worker while admission is still atomic. A cancellation can
		// publish a terminal status before the goroutine is scheduled, but the
		// cache-authority fence must still wait for that possible late starter.
		m.inflightWorkers[id] = true
	}
	if err := m.saveLocked(); err != nil {
		delete(m.jobs, id)
		delete(m.cancel, id)
		delete(m.inflightWorkers, id)
		m.nextID--
		return Job{}, false, JobAdmissionPersistenceError{}
	}
	return cloneJob(job), true, nil
}

func (m *JobManager) List() []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		out = append(out, cloneJob(job))
	}
	sortJobs(out)
	return out
}

func (m *JobManager) Prune() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveLocked()
}

func (m *JobManager) RetentionSnapshot() JobRetentionSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	snapshot := JobRetentionSnapshot{
		Policy: m.retention, ByStatus: map[string]int{},
		ExpiredTotal: m.expiredTotal, TruncatedTotal: m.truncatedTotal,
		LastExpired: m.lastExpired, LastTruncated: m.lastTruncated,
	}
	if m.lastPrunedAt != nil {
		value := *m.lastPrunedAt
		snapshot.LastPrunedAt = &value
	}
	for _, job := range m.jobs {
		if job == nil {
			continue
		}
		snapshot.ByStatus[job.Status]++
		if jobTerminalStatus(job.Status) {
			snapshot.Terminal++
		} else {
			snapshot.Active++
		}
		when := job.CreatedAt
		if snapshot.OldestRetained == nil || when.Before(*snapshot.OldestRetained) {
			value := when
			snapshot.OldestRetained = &value
		}
	}
	return snapshot
}

func (m *JobManager) Get(id string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return Job{}, false
	}
	return cloneJob(job), true
}

func (m *JobManager) Cancel(id string) (Job, bool, error) {
	m.mu.Lock()
	cancel, ok := m.cancel[id]
	stored := m.jobs[id]
	job := cloneJob(stored)
	if !ok || job.ID == "" {
		m.mu.Unlock()
		if job.ID == "" {
			return Job{}, false, nil
		}
		current, found := m.Get(id)
		return current, found, nil
	}
	if job.Status == JobStatusCancelling {
		resolution := m.cancelResolution[id]
		m.mu.Unlock()
		if resolution == nil {
			return job, true, JobSnapshotPersistenceError{}
		}
		<-resolution.done
		return resolution.job, resolution.found, resolution.err
	}
	if job.Type != RepositoryDocsIndexJobType {
		m.mu.Unlock()
		cancel()
		return m.waitForTerminalJob(id)
	}
	onRepositoryDocsCancelled := m.onRepositoryDocsCancelled
	previous := job
	resolution := &jobCancellationResolution{done: make(chan struct{})}
	stored.Status = JobStatusCancelling
	stored.UpdatedAt = m.now()
	stored.Progress = append(stored.Progress, service.ProgressEvent{Type: JobStatusCancelling, Phase: JobStatusCancelling, Collection: stored.Type, Message: "durable cancellation is being committed"})
	m.cancelResolution[id] = resolution
	if err := m.saveLocked(); err != nil {
		*stored = previous
		resolution.job, resolution.found, resolution.err = previous, true, JobAdmissionPersistenceError{}
		delete(m.cancelResolution, id)
		close(resolution.done)
		m.mu.Unlock()
		return resolution.job, resolution.found, resolution.err
	}
	job = cloneJob(stored)
	m.mu.Unlock()

	// Persist the cancellation disposition before publishing a terminal job or
	// signalling the worker. Repository-document terminalization observes the
	// cancelling fence and cannot race this write-ahead decision.
	if onRepositoryDocsCancelled != nil {
		if err := onRepositoryDocsCancelled(job); err != nil {
			m.mu.Lock()
			if current := m.jobs[id]; current != nil && current.Status == JobStatusCancelling {
				*current = previous
				_ = m.saveLocked()
			}
			current := cloneJob(m.jobs[id])
			resolution.job, resolution.found, resolution.err = current, current.ID != "", err
			delete(m.cancelResolution, id)
			close(resolution.done)
			m.mu.Unlock()
			return resolution.job, resolution.found, resolution.err
		}
	}
	m.mu.Lock()
	current := m.jobs[id]
	if current != nil && current.Status == JobStatusCancelling {
		now := m.now()
		current.Status = JobStatusCancelled
		current.UpdatedAt = now
		current.FinishedAt = &now
		current.ErrorClass = "cancelled"
		current.Error = publicMaintenanceJobError(current.Type, current.ErrorClass)
		current.Progress = append(current.Progress, service.ProgressEvent{Type: JobStatusCancelled, Phase: JobStatusCancelled, Collection: current.Type, Message: current.Error})
		delete(m.cancel, id)
	}
	saveErr := m.saveLocked()
	currentJob := cloneJob(current)
	resolution.job, resolution.found = currentJob, currentJob.ID != ""
	if saveErr != nil {
		resolution.err = JobSnapshotPersistenceError{}
	}
	// Signal the worker before publishing the shared resolution. Concurrent
	// callers may return as soon as done closes, so the public result must not
	// claim a worker signal that is still pending.
	cancel()
	delete(m.cancelResolution, id)
	close(resolution.done)
	m.mu.Unlock()
	return resolution.job, resolution.found, resolution.err
}

func (m *JobManager) waitForTerminalJob(id string) (Job, bool, error) {
	deadline := time.Now().Add(2 * time.Second)
	for {
		current, found := m.Get(id)
		if !found {
			return Job{}, false, nil
		}
		if jobTerminalStatus(current.Status) {
			return current, true, nil
		}
		if time.Now().After(deadline) {
			return current, true, nil
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (m *JobManager) updateRepositoryDocsTerminalJob(id string, fn func(*Job, time.Time)) {
	for {
		m.mu.Lock()
		job := m.jobs[id]
		if job == nil || jobTerminalStatus(job.Status) {
			m.mu.Unlock()
			return
		}
		if job.Status == JobStatusCancelling {
			resolution := m.cancelResolution[id]
			m.mu.Unlock()
			if resolution != nil {
				<-resolution.done
				continue
			}
			time.Sleep(time.Millisecond)
			continue
		}
		now := m.now()
		fn(job, now)
		trimJobProgress(job, m.retention.MaxProgressEvents)
		_ = m.saveLocked()
		m.mu.Unlock()
		return
	}
}

// FenceRepositoryDocsSourceGeneration makes work admitted under a replaced
// private source generation terminal before cancelling its worker. The source
// generation remains part of the durable job intent, so a restarted daemon
// cannot mistake the old work for the current authority.
func (m *JobManager) FenceRepositoryDocsSourceGeneration(registrationID, sourceRegistrationID string, generation int64) {
	registrationID = strings.TrimSpace(registrationID)
	sourceRegistrationID = strings.TrimSpace(sourceRegistrationID)
	if registrationID == "" || sourceRegistrationID == "" || generation <= 0 {
		return
	}
	m.mu.Lock()
	now := m.now()
	var cancels []context.CancelFunc
	for id, job := range m.jobs {
		if job == nil || job.Type != RepositoryDocsIndexJobType || job.RegistrationID != registrationID ||
			job.SourceRegistrationID != sourceRegistrationID || job.SourceRegistrationGeneration != generation ||
			!jobActiveStatus(job.Status) {
			continue
		}
		job.Status = JobStatusSuperseded
		job.UpdatedAt = now
		job.FinishedAt = &now
		job.ErrorClass = "repository_docs_source_generation_superseded"
		job.Error = publicMaintenanceJobError(job.Type, job.ErrorClass)
		job.Progress = append(job.Progress, service.ProgressEvent{Type: JobStatusSuperseded, Phase: JobStatusSuperseded, Collection: job.Type, Message: job.Error})
		if cancel := m.cancel[id]; cancel != nil {
			cancels = append(cancels, cancel)
		}
		delete(m.cancel, id)
	}
	_ = m.saveLocked()
	m.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func jobActiveStatus(status string) bool {
	return status == JobStatusQueued || status == JobStatusRunning || status == JobStatusCancelling
}

func (m *JobManager) beginRepositoryDocsJob(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[id]
	if job == nil || job.Status != JobStatusQueued {
		return false
	}
	now := m.now()
	job.Status = JobStatusRunning
	job.StartedAt = &now
	job.UpdatedAt = now
	job.Progress = append(job.Progress, service.ProgressEvent{Type: "started", Phase: "indexing", Collection: RepositoryDocsIndexJobType, Message: "repository documentation indexing started"})
	_ = m.saveLocked()
	return true
}

func (m *JobManager) createJob(jobType string, steps int, cancel context.CancelFunc) Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	now := m.now()
	id := fmt.Sprintf("job-%06d", m.nextID)
	job := &Job{ID: id, Type: jobType, Status: JobStatusQueued, CreatedAt: now, UpdatedAt: now, Steps: steps}
	m.jobs[id] = job
	m.cancel[id] = cancel
	_ = m.saveLocked()
	return cloneJob(job)
}

func (m *JobManager) createJobWithMetadata(jobType, repoID, profileID string, steps int, cancel context.CancelFunc) Job {
	job := m.createJob(jobType, steps, cancel)
	m.updateJob(job.ID, func(stored *Job, now time.Time) {
		stored.RepoID = repoID
		stored.ProfileID = profileID
		stored.UpdatedAt = now
	})
	return m.mustGet(job.ID)
}

func (m *JobManager) runFakeJob(ctx context.Context, id string, steps int, interval time.Duration) {
	m.updateJob(id, func(job *Job, now time.Time) {
		job.Status = JobStatusRunning
		job.StartedAt = &now
		job.UpdatedAt = now
		job.Progress = append(job.Progress, service.ProgressEvent{Type: "started", Phase: "running", Collection: "fake", Message: "fake job started"})
	})
	for step := 1; step <= steps; step++ {
		select {
		case <-ctx.Done():
			m.finishJob(id, JobStatusCancelled, "job cancelled")
			return
		case <-time.After(interval):
		}
		m.updateJob(id, func(job *Job, now time.Time) {
			job.Completed = step
			job.UpdatedAt = now
			job.Progress = append(job.Progress, service.ProgressEvent{Type: "records", Phase: "running", Collection: "fake", Page: step, RecordsFetched: step, Message: fmt.Sprintf("fake step %d/%d", step, steps)})
		})
	}
	m.finishJob(id, JobStatusSucceeded, "")
}

func (m *JobManager) finishJob(id, status, message string) {
	m.updateJob(id, func(job *Job, now time.Time) {
		job.Status = status
		job.UpdatedAt = now
		job.FinishedAt = &now
		if message != "" {
			job.Error = message
		}
		eventType := status
		if status == JobStatusSucceeded {
			eventType = "finished"
		}
		job.Progress = append(job.Progress, service.ProgressEvent{Type: eventType, Phase: status, Collection: job.Type, Message: firstNonEmpty(message, "job finished")})
		delete(m.cancel, id)
	})
}

func (m *JobManager) updateJob(id string, fn func(*Job, time.Time)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := m.jobs[id]
	if job == nil {
		return
	}
	fn(job, m.now())
	trimJobProgress(job, m.retention.MaxProgressEvents)
	_ = m.saveLocked()
}

func (m *JobManager) mustGet(id string) Job {
	job, ok := m.Get(id)
	if !ok {
		return Job{}
	}
	return job
}

func (m *JobManager) saveLocked() error {
	m.pruneLocked()
	if m.snapshotPath == "" {
		return nil
	}
	jobs := make([]Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		jobs = append(jobs, cloneJob(job))
	}
	sortJobs(jobs)
	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}
	return m.writeFile(m.snapshotPath, append(data, '\n'), 0o600)
}

func (m *JobManager) pruneLocked() {
	now := m.now()
	terminal := make([]*Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		if job == nil {
			continue
		}
		trimJobProgress(job, m.retention.MaxProgressEvents)
		if jobTerminalStatus(job.Status) {
			terminal = append(terminal, job)
		}
	}

	protected := m.protectedDiagnosticJobs(terminal)
	expired := 0
	for _, job := range terminal {
		ttl := m.retention.SuccessTTL
		if diagnosticJobStatus(job.Status) {
			ttl = m.retention.DiagnosticTTL
		}
		if now.Sub(terminalSortTime(job)) >= ttl && !protected[job.ID] {
			delete(m.jobs, job.ID)
			delete(m.cancel, job.ID)
			expired++
		}
	}

	terminal = terminal[:0]
	for _, job := range m.jobs {
		if job != nil && jobTerminalStatus(job.Status) {
			terminal = append(terminal, job)
		}
	}
	sort.Slice(terminal, func(i, j int) bool { return terminalJobLess(terminal[i], terminal[j]) })
	truncated := 0
	for len(terminal) > m.retention.MaxTerminalJobs {
		index := 0
		for i, job := range terminal {
			if !protected[job.ID] {
				index = i
				break
			}
		}
		job := terminal[index]
		delete(m.jobs, job.ID)
		delete(m.cancel, job.ID)
		terminal = append(terminal[:index], terminal[index+1:]...)
		truncated++
	}
	if expired > 0 || truncated > 0 {
		prunedAt := now
		m.lastPrunedAt = &prunedAt
	}
	if expired > 0 || truncated > 0 {
		m.lastExpired, m.lastTruncated = expired, truncated
		m.expiredTotal += expired
		m.truncatedTotal += truncated
	}
}

func (m *JobManager) protectedDiagnosticJobs(terminal []*Job) map[string]bool {
	latest := map[string]*Job{}
	for _, job := range terminal {
		if !diagnosticJobStatus(job.Status) {
			continue
		}
		key := diagnosticJobKey(job)
		if current := latest[key]; current == nil || terminalJobLess(current, job) {
			latest[key] = job
		}
	}
	candidates := make([]*Job, 0, len(latest))
	for _, job := range latest {
		candidates = append(candidates, job)
	}
	sort.Slice(candidates, func(i, j int) bool { return terminalJobLess(candidates[j], candidates[i]) })
	protected := map[string]bool{}
	for i, job := range candidates {
		if i >= m.retention.MaxDiagnosticJobs {
			break
		}
		protected[job.ID] = true
	}
	return protected
}

func diagnosticJobStatus(status string) bool {
	switch status {
	case JobStatusFailed, JobStatusInterrupted, JobStatusCancelled:
		return true
	default:
		return false
	}
}

func diagnosticJobKey(job *Job) string {
	if job == nil {
		return "unknown"
	}
	return firstNonEmpty(job.RegistrationID, job.WorkKey, job.WorkRef, strings.Join([]string{job.Type, job.CacheUUID, job.RepoID}, "\x00"))
}

func terminalJobLess(left, right *Job) bool {
	leftTime, rightTime := terminalSortTime(left), terminalSortTime(right)
	if leftTime.Equal(rightTime) {
		return left.ID < right.ID
	}
	return leftTime.Before(rightTime)
}

func jobTerminalStatus(status string) bool {
	switch status {
	case JobStatusSucceeded, JobStatusSuperseded, JobStatusFailed, JobStatusCancelled, JobStatusInterrupted:
		return true
	default:
		return false
	}
}

func terminalSortTime(job *Job) time.Time {
	if job == nil {
		return time.Time{}
	}
	if job.FinishedAt != nil {
		return *job.FinishedAt
	}
	if !job.UpdatedAt.IsZero() {
		return job.UpdatedAt
	}
	return job.CreatedAt
}

func trimJobProgress(job *Job, limit int) {
	if job == nil || limit <= 0 || len(job.Progress) <= limit {
		return
	}
	job.Progress = append([]service.ProgressEvent(nil), job.Progress[len(job.Progress)-limit:]...)
}

func cloneJob(job *Job) Job {
	if job == nil {
		return Job{}
	}
	out := *job
	if job.StartedAt != nil {
		started := *job.StartedAt
		out.StartedAt = &started
	}
	if job.FinishedAt != nil {
		finished := *job.FinishedAt
		out.FinishedAt = &finished
	}
	out.Progress = append([]service.ProgressEvent(nil), job.Progress...)
	return out
}

func parseJobIDNumber(id string) int {
	var n int
	if _, err := fmt.Sscanf(id, "job-%d", &n); err != nil {
		return 0
	}
	return n
}

func sortJobs(jobs []Job) {
	for i := 1; i < len(jobs); i++ {
		for j := i; j > 0 && jobs[j-1].ID > jobs[j].ID; j-- {
			jobs[j-1], jobs[j] = jobs[j], jobs[j-1]
		}
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
