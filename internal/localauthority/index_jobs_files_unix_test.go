//go:build darwin || linux

package localauthority

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestIndexJobFilesRefuseUnsafeStateWithoutSourceDamage(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "namespace", "unknown", "malformed", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			q, s, dir := indexQueueFixture(t)
			createIssue(t, s, "synthetic-index-file-source")
			indexQueueEmpty(t, q)
			indexQueueAdmitted(t, q)
			if q.close() != nil {
				t.Fatal("index file owner did not close")
			}
			ns := filepath.Join(dir, indexJobsDirectory)
			file := filepath.Join(ns, "state.json")
			switch kind {
			case "symlink":
				if os.Rename(file, filepath.Join(ns, "saved.json")) != nil || os.Symlink("saved.json", file) != nil {
					t.Fatal("index symlink control unavailable")
				}
			case "hardlink":
				if os.Link(file, filepath.Join(dir, "saved-index-intent.json")) != nil {
					t.Fatal("index hardlink control unavailable")
				}
			case "namespace":
				if os.Rename(ns, filepath.Join(dir, "saved-index-intents")) != nil || os.Symlink("saved-index-intents", ns) != nil {
					t.Fatal("index namespace control unavailable")
				}
			case "unknown":
				if os.WriteFile(filepath.Join(ns, "unknown.json"), []byte(`{}`), 0600) != nil {
					t.Fatal("unknown index inventory control unavailable")
				}
			case "malformed":
				if os.WriteFile(file, []byte(`{"state":{},"digest":"synthetic-error"}`), 0600) != nil {
					t.Fatal("malformed index state control unavailable")
				}
			case "oversize":
				if os.WriteFile(file, []byte(strings.Repeat("x", maxIndexSnapshotBytes+1)), 0600) != nil {
					t.Fatal("oversized index state control unavailable")
				}
			}
			loaded, err := openIndexQueue(context.Background(), s)
			if loaded != nil {
				_ = loaded.close()
				t.Fatal("unsafe index state was admitted")
			}
			requireProjectionCode(t, err, IndexJobsCorrupt)
			status, err := s.Status(context.Background())
			if err != nil || status.Readiness != "ready" || status.AuthorityGeneration != 1 {
				t.Fatal("invalid index sidecar fenced canonical source")
			}
		})
	}
}

func TestIndexJobFIFORefusalInActualBoundedChild(t *testing.T) {
	for _, target := range []string{"final", "stage"} {
		t.Run(target, func(t *testing.T) {
			exe, err := os.Executable()
			if err != nil {
				t.Fatal("index FIFO child executable unavailable")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestIndexJobFIFOChild$")
			cmd.Env = []string{"GCM_INDEX_FIFO_CHILD=" + target}
			if err = cmd.Run(); err != nil || ctx.Err() != nil {
				t.Fatal("index FIFO child failed bounded nonblocking refusal")
			}
		})
	}
}

func TestIndexJobFIFOChild(t *testing.T) {
	target := os.Getenv("GCM_INDEX_FIFO_CHILD")
	if target == "" {
		return
	}
	if target != "final" && target != "stage" {
		t.Fatal("invalid bounded index FIFO selector")
	}
	q, s, dir := indexQueueFixture(t)
	if q.close() != nil {
		t.Fatal("index FIFO owner did not close")
	}
	ns := filepath.Join(dir, indexJobsDirectory)
	if os.Mkdir(ns, 0700) != nil {
		t.Fatal("index FIFO namespace unavailable")
	}
	name := "state.json"
	if target == "stage" {
		name = "stage-22222222-2222-4222-8222-222222222222.json"
	}
	if unix.Mkfifo(filepath.Join(ns, name), 0600) != nil {
		t.Fatal("index FIFO control unavailable")
	}
	_, err := openIndexQueue(context.Background(), s)
	requireProjectionCode(t, err, IndexJobsCorrupt)
}

func TestIndexJobPublicationRefusesPhysicalReplacement(t *testing.T) {
	for _, target := range []string{"state", "stage", "namespace"} {
		t.Run(target, func(t *testing.T) {
			q, s, dir := indexQueueFixture(t)
			indexQueueEmpty(t, q)
			admitted := indexQueueAdmitted(t, q)
			next := cloneIndexSnapshot(admitted)
			next.Generation++
			next.Jobs[0].Generation, next.Jobs[0].Attempts, next.Jobs[0].State = 2, 1, "running"
			q.fault = func(phase string) error {
				if phase != "index_stage_ready" {
					return nil
				}
				ns := filepath.Join(dir, indexJobsDirectory)
				switch target {
				case "state":
					if os.WriteFile(filepath.Join(ns, "state.json"), []byte(`{}`), 0600) != nil {
						t.Fatal("index final replacement unavailable")
					}
				case "stage":
					entries, err := os.ReadDir(ns)
					if err != nil {
						t.Fatal("index stage inventory unavailable")
					}
					for _, e := range entries {
						if !strings.HasPrefix(e.Name(), "stage-") {
							continue
						}
						file := filepath.Join(ns, e.Name())
						body, err := os.ReadFile(file)
						if err != nil || os.Rename(file, filepath.Join(ns, "saved-stage")) != nil || os.WriteFile(file, body, 0600) != nil {
							t.Fatal("index identical stage inode replacement unavailable")
						}
					}
				case "namespace":
					if os.Rename(ns, filepath.Join(dir, "saved-intent-namespace")) != nil || os.Mkdir(ns, 0700) != nil {
						t.Fatal("index namespace inode replacement unavailable")
					}
				}
				return nil
			}
			requireProjectionCode(t, q.publish(context.Background(), next), IndexJobsCorrupt)
			status, err := s.Status(context.Background())
			if err != nil || status.AuthorityGeneration != 0 || status.Readiness != "ready" {
				t.Fatal("index replacement affected canonical source")
			}
		})
	}
}
