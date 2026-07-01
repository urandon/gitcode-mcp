package servicectl

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	next, err := manager.StartFake(context.Background(), StartFakeJobRequest{Steps: 1, IntervalMS: 1})
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != "job-000008" {
		t.Fatalf("next job id = %q, want job-000008", next.ID)
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
		if job.Status == JobStatusCancelled || job.Status == JobStatusSucceeded || job.Status == JobStatusInterrupted {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not reach terminal status: %#v", job)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
