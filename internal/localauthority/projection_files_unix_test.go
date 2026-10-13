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

func TestProjectionFileTypeAndNamespaceRefusal(t *testing.T) {
	for _, kind := range []string{"final-symlink", "final-hardlink", "namespace-symlink", "unknown-name", "bad-envelope", "large-file"} {
		t.Run(kind, func(t *testing.T) {
			p, s, dir := projectionFixture(t)
			createIssue(t, s, "projection-type-source")
			publishProjectionFixture(t, p, 1)
			if p.close() != nil {
				t.Fatal("projection type owner did not close")
			}
			ns := filepath.Join(dir, projectionDirectory)
			file := filepath.Join(ns, "catalog.json")
			switch kind {
			case "final-symlink":
				if os.Rename(file, filepath.Join(ns, "saved.json")) != nil || os.Symlink("saved.json", file) != nil {
					t.Fatal("symlink control unavailable")
				}
			case "final-hardlink":
				if os.Link(file, filepath.Join(dir, "saved-index.json")) != nil {
					t.Fatal("hardlink control unavailable")
				}
			case "namespace-symlink":
				if os.Rename(ns, filepath.Join(dir, "saved-projection")) != nil || os.Symlink("saved-projection", ns) != nil {
					t.Fatal("namespace symlink control unavailable")
				}
			case "unknown-name":
				if os.WriteFile(filepath.Join(ns, "unknown.json"), []byte(`{}`), 0600) != nil {
					t.Fatal("unknown inventory control unavailable")
				}
			case "bad-envelope":
				if os.WriteFile(file, []byte(`{"catalog":{},"digest":"synthetic-private-error"}`), 0600) != nil {
					t.Fatal("corrupt envelope control unavailable")
				}
			case "large-file":
				if os.WriteFile(file, []byte(strings.Repeat("x", MaxProjectionBytes+1)), 0600) != nil {
					t.Fatal("oversize control unavailable")
				}
			}
			if index, err := openProjection(context.Background(), s); err == nil {
				_ = index.close()
				t.Fatal("unsafe projection file or inventory was admitted")
			}
			status, err := s.Status(context.Background())
			if err != nil || status.Readiness != "ready" || status.AuthorityGeneration != 1 {
				t.Fatal("unsafe derived bytes became canonical authority damage")
			}
		})
	}
}

func TestProjectionFIFORefusalIsBoundedInActualChild(t *testing.T) {
	for _, target := range []string{"final", "stage"} {
		t.Run(target, func(t *testing.T) {
			exe, err := os.Executable()
			if err != nil {
				t.Fatal("projection FIFO child executable unavailable")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, exe, "-test.run=^TestProjectionFIFOChild$")
			cmd.Env = []string{"GCM_PROJECTION_FIFO_CHILD=" + target}
			if err = cmd.Run(); err != nil || ctx.Err() != nil {
				t.Fatal("projection FIFO child failed bounded type refusal")
			}
		})
	}
}

func TestProjectionFIFOChild(t *testing.T) {
	target := os.Getenv("GCM_PROJECTION_FIFO_CHILD")
	if target == "" {
		return
	}
	if target != "final" && target != "stage" {
		t.Fatal("invalid bounded FIFO helper selector")
	}
	p, s, dir := projectionFixture(t)
	if p.close() != nil {
		t.Fatal("FIFO control owner did not close")
	}
	ns := filepath.Join(dir, projectionDirectory)
	if os.Mkdir(ns, 0700) != nil {
		t.Fatal("FIFO control namespace unavailable")
	}
	name := "catalog.json"
	if target == "stage" {
		name = "stage-22222222-2222-4222-8222-222222222222.json"
	}
	if unix.Mkfifo(filepath.Join(ns, name), 0600) != nil {
		t.Fatal("FIFO control unavailable")
	}
	_, err := openProjection(context.Background(), s)
	requireProjectionCode(t, err, ProjectionCorrupt)
}

func TestProjectionPhysicalChangesDuringPublicationFailClosed(t *testing.T) {
	for _, target := range []string{"final-content", "stage-inode", "namespace-inode"} {
		t.Run(target, func(t *testing.T) {
			p, s, dir := projectionFixture(t)
			createIssue(t, s, "projection-physical-baseline")
			publishProjectionFixture(t, p, 1)
			createIssue(t, s, "projection-physical-new")
			candidate := projectionCandidate(t, p, 2)
			p.fault = func(phase string) error {
				if phase != "projection_stage_directory" {
					return nil
				}
				ns := filepath.Join(dir, projectionDirectory)
				switch target {
				case "final-content":
					if os.WriteFile(filepath.Join(ns, "catalog.json"), []byte(`{}`), 0600) != nil {
						t.Fatal("final content swap unavailable")
					}
				case "stage-inode":
					entries, err := os.ReadDir(ns)
					if err != nil {
						t.Fatal("stage inode inventory unavailable")
					}
					for _, e := range entries {
						if strings.HasPrefix(e.Name(), "stage-") {
							path := filepath.Join(ns, e.Name())
							data, err := os.ReadFile(path)
							if err != nil || os.Rename(path, filepath.Join(ns, "saved-stage")) != nil || os.WriteFile(path, data, 0600) != nil {
								t.Fatal("same-content stage inode replacement unavailable")
							}
						}
					}
				case "namespace-inode":
					if os.Rename(ns, filepath.Join(dir, "saved-index-namespace")) != nil || os.Mkdir(ns, 0700) != nil {
						t.Fatal("namespace identity swap unavailable")
					}
				}
				return nil
			}
			requireProjectionCode(t, p.publish(context.Background(), candidate), ProjectionCorrupt)
			status, err := s.Status(context.Background())
			if err != nil || status.AuthorityGeneration != 2 || status.Readiness != "ready" {
				t.Fatal("derived physical conflict changed canonical source")
			}
		})
	}
}
