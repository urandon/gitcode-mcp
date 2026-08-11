package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"gitcode-mcp/internal/service"
)

const (
	JobStatusQueued      = "queued"
	JobStatusRunning     = "running"
	JobStatusSucceeded   = "succeeded"
	JobStatusFailed      = "failed"
	JobStatusCancelled   = "cancelled"
	JobStatusInterrupted = "interrupted"

	maxStoredTerminalJobs   = 128
	maxStoredProgressEvents = 256
)

type Job struct {
	ID             string                  `json:"id"`
	Type           string                  `json:"type"`
	RepoID         string                  `json:"repo_id,omitempty"`
	ProfileID      string                  `json:"profile_id,omitempty"`
	CacheUUID      string                  `json:"cache_uuid,omitempty"`
	RegistrationID string                  `json:"registration_id,omitempty"`
	NamespaceID    string                  `json:"namespace_id,omitempty"`
	WorkKey        string                  `json:"-"`
	Status         string                  `json:"status"`
	CreatedAt      time.Time               `json:"created_at"`
	StartedAt      *time.Time              `json:"started_at,omitempty"`
	UpdatedAt      time.Time               `json:"updated_at"`
	FinishedAt     *time.Time              `json:"finished_at,omitempty"`
	Steps          int                     `json:"steps,omitempty"`
	Completed      int                     `json:"completed,omitempty"`
	Error          string                  `json:"error,omitempty"`
	Progress       []service.ProgressEvent `json:"progress,omitempty"`
}

type JobManager struct {
	mu           sync.Mutex
	jobs         map[string]*Job
	cancel       map[string]context.CancelFunc
	nextID       int
	snapshotPath string
	now          func() time.Time
}

type StartFakeJobRequest struct {
	Steps      int `json:"steps,omitempty"`
	IntervalMS int `json:"interval_ms,omitempty"`
}

func NewJobManager(snapshotPath string) *JobManager {
	return &JobManager{
		jobs:         map[string]*Job{},
		cancel:       map[string]context.CancelFunc{},
		snapshotPath: snapshotPath,
		now:          func() time.Time { return time.Now().UTC() },
	}
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
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	maxID := 0
	for i := range jobs {
		job := jobs[i]
		if job.Status == JobStatusQueued || job.Status == JobStatusRunning {
			job.Status = JobStatusInterrupted
			job.UpdatedAt = now
			job.FinishedAt = &now
			job.Error = "service restarted before job completed"
			job.Progress = append(job.Progress, service.ProgressEvent{Type: "interrupted", Phase: "interrupted", Message: job.Error})
		}
		trimJobProgress(&job)
		idNum := parseJobIDNumber(job.ID)
		if idNum > maxID {
			maxID = idNum
		}
		jobCopy := job
		m.jobs[job.ID] = &jobCopy
	}
	if maxID > m.nextID {
		m.nextID = maxID
	}
	return m.saveLocked()
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
		if job.Status == JobStatusQueued || job.Status == JobStatusRunning {
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
		if job.Status == JobStatusQueued || job.Status == JobStatusRunning {
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
		if job.Status == JobStatusQueued || job.Status == JobStatusRunning {
			return cloneJob(job), true
		}
	}
	return Job{}, false
}

func (m *JobManager) SetWorkIdentity(id, workKey, cacheUUID, registrationID, namespaceID string) Job {
	m.updateJob(id, func(job *Job, now time.Time) {
		job.WorkKey = workKey
		job.CacheUUID = cacheUUID
		job.RegistrationID = registrationID
		job.NamespaceID = namespaceID
		job.UpdatedAt = now
	})
	return m.mustGet(id)
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

func (m *JobManager) Get(id string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job, ok := m.jobs[id]
	if !ok {
		return Job{}, false
	}
	return cloneJob(job), true
}

func (m *JobManager) Cancel(id string) (Job, bool) {
	m.mu.Lock()
	cancel, ok := m.cancel[id]
	job := m.jobs[id]
	m.mu.Unlock()
	if !ok || job == nil {
		if job == nil {
			return Job{}, false
		}
		return m.Get(id)
	}
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for {
		current, found := m.Get(id)
		if !found {
			return Job{}, false
		}
		if current.Status == JobStatusCancelled || current.Status == JobStatusSucceeded || current.Status == JobStatusInterrupted {
			return current, true
		}
		if time.Now().After(deadline) {
			return current, true
		}
		time.Sleep(5 * time.Millisecond)
	}
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
	trimJobProgress(job)
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
	if m.snapshotPath == "" {
		return nil
	}
	m.pruneLocked()
	if err := os.MkdirAll(filepath.Dir(m.snapshotPath), 0o700); err != nil {
		return err
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
	return os.WriteFile(m.snapshotPath, append(data, '\n'), 0o600)
}

func (m *JobManager) pruneLocked() {
	if maxStoredTerminalJobs <= 0 {
		return
	}
	terminal := make([]*Job, 0, len(m.jobs))
	for _, job := range m.jobs {
		if job == nil {
			continue
		}
		trimJobProgress(job)
		if jobTerminalStatus(job.Status) {
			terminal = append(terminal, job)
		}
	}
	if len(terminal) <= maxStoredTerminalJobs {
		return
	}
	sort.Slice(terminal, func(i, j int) bool {
		return terminalSortTime(terminal[i]).Before(terminalSortTime(terminal[j]))
	})
	for _, job := range terminal[:len(terminal)-maxStoredTerminalJobs] {
		delete(m.jobs, job.ID)
		delete(m.cancel, job.ID)
	}
}

func jobTerminalStatus(status string) bool {
	switch status {
	case JobStatusSucceeded, JobStatusFailed, JobStatusCancelled, JobStatusInterrupted:
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

func trimJobProgress(job *Job) {
	if job == nil || maxStoredProgressEvents <= 0 || len(job.Progress) <= maxStoredProgressEvents {
		return
	}
	job.Progress = append([]service.ProgressEvent(nil), job.Progress[len(job.Progress)-maxStoredProgressEvents:]...)
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
