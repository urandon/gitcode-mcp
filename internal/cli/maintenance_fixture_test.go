package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitcode-mcp/internal/servicectl"
)

// This empty fixture's initial asynchronous reconciliation publishes jobs.json.
// The RPC listener alone is not a barrier for that filesystem write. Poll only
// a complete empty array; do not expose private paths or payloads in failures.
func maintenanceFixtureSnapshotPublished(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("fixture startup snapshot could not be read")
	}
	var jobs []json.RawMessage
	if err := json.Unmarshal(data, &jobs); err != nil {
		return false, nil
	}
	return jobs != nil && len(jobs) == 0, nil
}

func waitMaintenanceFixtureSnapshot(ctx context.Context, path string, done <-chan struct{}) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return errors.New("fixture daemon exited before startup snapshot readiness")
		default:
		}
		ready, err := maintenanceFixtureSnapshotPublished(path)
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			return errors.New("fixture daemon exited before startup snapshot readiness")
		case <-ticker.C:
		}
	}
}

func TestMaintenanceFixtureSnapshotBarrierRequiresCompleteEmptyArray(t *testing.T) {
	for _, tc := range []struct {
		name string
		data string
		want bool
	}{
		{name: "absent"},
		{name: "truncated", data: "["},
		{name: "null", data: "null"},
		{name: "nonempty", data: `[{}]`},
		{name: "complete", data: "[]\n", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "jobs.json")
			if tc.data != "" {
				if err := os.WriteFile(path, []byte(tc.data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := maintenanceFixtureSnapshotPublished(path)
			if err != nil || got != tc.want {
				t.Fatalf("ready=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestMaintenanceFixtureSnapshotBarrierFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitMaintenanceFixtureSnapshot(ctx, path, make(chan struct{})); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled readiness was accepted")
	}
	done := make(chan struct{})
	close(done)
	if err := waitMaintenanceFixtureSnapshot(context.Background(), path, done); err == nil {
		t.Fatal("exited daemon was accepted")
	}
}

func TestMaintenanceFixturePublishesStartupSnapshotBeforeReturn(t *testing.T) {
	for i := 0; i < 20; i++ {
		func() {
			src, _, _, stop := runningMaintenanceCLIFixture(t)
			defer stop()
			paths, err := (servicectl.Manager{Source: src}).ResolvePaths()
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(paths.JobsPath)
			if err != nil {
				t.Fatal("fixture returned before initial reconciliation snapshot publication")
			}
			var jobs []json.RawMessage
			if err := json.Unmarshal(data, &jobs); err != nil || jobs == nil || len(jobs) != 0 {
				t.Fatal("fixture initial job snapshot is not a complete empty array")
			}
		}()
	}
}
