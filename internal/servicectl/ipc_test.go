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

	"gitcode-mcp/internal/service"
)

func TestRPCServiceStatusAndFakeJobLifecycle(t *testing.T) {
	manager := newTestManager(t, "darwin")
	src := manager.Source.(testSource)
	src.env = map[string]string{"GITCODE_MCP_SERVICE_NETWORK": "mem", "GITCODE_MCP_SERVICE_ADDRESS": "test-ipc-lifecycle"}
	manager.Source = src
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- manager.Run(ctx)
	}()
	client := waitForTestClient(t, manager, errCh)

	var status Status
	if err := client.Call(context.Background(), "Service.Status", nil, &status); err != nil {
		t.Fatal(err)
	}
	if status.Status != StatusRunning || !status.Running || !status.SocketPresent {
		t.Fatalf("service status = %#v", status)
	}
	var capabilities MaintenanceCapabilities
	if err := client.Call(context.Background(), "Maintenance.Capabilities", nil, &capabilities); err != nil {
		t.Fatal(err)
	}
	if capabilities.RegistryProtocol != maintenanceRegistrySchema || capabilities.BinaryVersion != manager.Version || len(capabilities.Methods) != 6 {
		t.Fatalf("maintenance capabilities = %#v", capabilities)
	}

	var job Job
	if err := client.Call(context.Background(), "Jobs.StartFake", StartFakeJobRequest{Steps: 20, IntervalMS: 25}, &job); err != nil {
		t.Fatal(err)
	}
	if job.ID == "" || job.Type != "fake" {
		t.Fatalf("started job = %#v", job)
	}

	var list JobListResult
	if err := client.Call(context.Background(), "Jobs.List", nil, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Jobs) != 1 || list.Jobs[0].ID != job.ID {
		t.Fatalf("job list = %#v", list)
	}

	cancelled := waitForJobStatus(t, client, job.ID, "cancel")
	if cancelled.Status != JobStatusCancelled || cancelled.FinishedAt == nil {
		t.Fatalf("cancelled job = %#v", cancelled)
	}
	data, err := json.Marshal(cancelled.Progress)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "records_fetched") && !strings.Contains(string(data), "cancelled") {
		t.Fatalf("progress serialization missing expected fields: %s", string(data))
	}

	cancel()
	if err := <-errCh; err != nil && err != context.Canceled {
		t.Fatalf("service run returned %v", err)
	}
}

func TestJobManagerMarksRunningSnapshotInterrupted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.json")
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	running := []Job{{
		ID:        "job-000007",
		Type:      "fake",
		Status:    JobStatusRunning,
		CreatedAt: now,
		UpdatedAt: now,
		Steps:     10,
		Completed: 3,
	}}
	data, err := json.Marshal(running)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	manager := NewJobManager(path)
	manager.now = func() time.Time { return now.Add(time.Minute) }
	if err := manager.LoadAndMarkInterrupted(); err != nil {
		t.Fatal(err)
	}
	job, ok := manager.Get("job-000007")
	if !ok {
		t.Fatal("job not loaded")
	}
	if job.Status != JobStatusInterrupted || job.FinishedAt == nil || !strings.Contains(job.Error, "restarted") {
		t.Fatalf("interrupted job = %#v", job)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	next, err := manager.StartFake(ctx, StartFakeJobRequest{Steps: 1, IntervalMS: 1})
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != "job-000008" {
		t.Fatalf("next job id = %q, want job-000008", next.ID)
	}
	waitForManagerJobTerminal(t, manager, next.ID)
}

func TestJobManagerPrunesStoredTerminalJobs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.json")
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	manager := NewJobManager(path)
	manager.mu.Lock()
	for i := 1; i <= maxStoredTerminalJobs+2; i++ {
		finished := now.Add(time.Duration(i) * time.Minute)
		id := fmt.Sprintf("job-%06d", i)
		manager.jobs[id] = &Job{ID: id, Type: "fake", Status: JobStatusSucceeded, CreatedAt: now, UpdatedAt: finished, FinishedAt: &finished}
	}
	manager.jobs["job-active"] = &Job{ID: "job-active", Type: "fake", Status: JobStatusRunning, CreatedAt: now, UpdatedAt: now}
	if err := manager.saveLocked(); err != nil {
		manager.mu.Unlock()
		t.Fatal(err)
	}
	manager.mu.Unlock()

	if _, ok := manager.Get("job-000001"); ok {
		t.Fatal("oldest terminal job was not pruned")
	}
	if _, ok := manager.Get("job-000002"); ok {
		t.Fatal("second oldest terminal job was not pruned")
	}
	if _, ok := manager.Get("job-active"); !ok {
		t.Fatal("active job was pruned")
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var jobs []Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		t.Fatal(err)
	}
	if len(jobs) != maxStoredTerminalJobs+1 {
		t.Fatalf("stored jobs = %d, want %d", len(jobs), maxStoredTerminalJobs+1)
	}
}

func TestJobManagerTrimsStoredProgressEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "jobs.json")
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	manager := NewJobManager(path)
	progress := make([]service.ProgressEvent, 0, maxStoredProgressEvents+3)
	for i := 0; i < maxStoredProgressEvents+3; i++ {
		progress = append(progress, service.ProgressEvent{Type: "records", Page: i + 1})
	}
	manager.mu.Lock()
	manager.jobs["job-000001"] = &Job{ID: "job-000001", Type: "fake", Status: JobStatusSucceeded, CreatedAt: now, UpdatedAt: now, FinishedAt: &now, Progress: progress}
	if err := manager.saveLocked(); err != nil {
		manager.mu.Unlock()
		t.Fatal(err)
	}
	manager.mu.Unlock()

	job, ok := manager.Get("job-000001")
	if !ok {
		t.Fatal("job not stored")
	}
	if len(job.Progress) != maxStoredProgressEvents {
		t.Fatalf("progress events = %d, want %d", len(job.Progress), maxStoredProgressEvents)
	}
	if job.Progress[0].Page != 4 {
		t.Fatalf("first kept progress page = %d, want 4", job.Progress[0].Page)
	}
}

func waitForManagerJobTerminal(t *testing.T, manager *JobManager, id string) Job {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		job, ok := manager.Get(id)
		if !ok {
			t.Fatalf("job %s not found", id)
		}
		switch job.Status {
		case JobStatusSucceeded, JobStatusSuperseded, JobStatusFailed, JobStatusCancelled, JobStatusInterrupted:
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s did not finish before cleanup: %#v", id, job)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func waitForTestClient(t *testing.T, manager Manager, errCh <-chan error) *RPCClient {
	t.Helper()
	client, err := manager.Client()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var status Status
		err := client.Call(context.Background(), "Service.Status", nil, &status)
		if err == nil {
			return client
		}
		select {
		case runErr := <-errCh:
			t.Fatalf("service run exited before socket became ready: %v", runErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("service socket did not become ready: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForJobStatus(t *testing.T, client *RPCClient, id string, action string) Job {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var job Job
		var err error
		if action == "cancel" {
			err = client.Call(context.Background(), "Jobs.Cancel", map[string]string{"job_id": id}, &job)
		} else {
			err = client.Call(context.Background(), "Jobs.Get", map[string]string{"job_id": id}, &job)
		}
		if err != nil {
			t.Fatal(err)
		}
		if jobTerminalStatus(job.Status) {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not reach terminal status: %#v", job)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
