package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gitcode-mcp/internal/service"
)

const (
	JobStatusQueued      = "queued"
	JobStatusRunning     = "running"
	JobStatusSucceeded   = "succeeded"
	JobStatusCancelled   = "cancelled"
	JobStatusInterrupted = "interrupted"
)

type Job struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	Status     string                  `json:"status"`
	CreatedAt  time.Time               `json:"created_at"`
	StartedAt  *time.Time              `json:"started_at,omitempty"`
	UpdatedAt  time.Time               `json:"updated_at"`
	FinishedAt *time.Time              `json:"finished_at,omitempty"`
	Steps      int                     `json:"steps,omitempty"`
	Completed  int                     `json:"completed,omitempty"`
	Error      string                  `json:"error,omitempty"`
	Progress   []service.ProgressEvent `json:"progress,omitempty"`
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
	_ = m.saveLocked()
}

func (m *JobManager) saveLocked() error {
	if m.snapshotPath == "" {
		return nil
	}
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
