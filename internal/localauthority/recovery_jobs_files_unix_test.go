//go:build darwin || linux

package localauthority

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRecoveryJobFIFOProcessHelper(t *testing.T) {
	if os.Getenv("GCM_RECOVERY_FIFO_CHILD") != "1" {
		return
	}
	d, err := openPrivateDirectory(os.Getenv("GCM_RECOVERY_FIFO_DIRECTORY"))
	if err != nil {
		t.Fatal("synthetic FIFO directory unavailable")
	}
	defer d.Close()
	f, _, err := openRecoveryJobFile(d, os.Getenv("GCM_RECOVERY_FIFO_NAME"), false)
	if f != nil {
		_ = f.Close()
	}
	requireKnowledgeError(t, err, JobsCorrupt)
}

func TestRecoveryJobFilesRejectFIFOWithoutBlocking(t *testing.T) {
	for _, name := range []string{"state.json", "stage-22222222-2222-4222-8222-222222222222.json"} {
		t.Run(name, func(t *testing.T) {
			directory := privateTestDirectory(t)
			path := filepath.Join(directory, name)
			if unix.Mkfifo(path, 0600) != nil {
				t.Fatal("synthetic FIFO creation failed")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRecoveryJobFIFOProcessHelper$")
			cmd.Env = append(os.Environ(), "GCM_RECOVERY_FIFO_CHILD=1", "GCM_RECOVERY_FIFO_DIRECTORY="+directory, "GCM_RECOVERY_FIFO_NAME="+name)
			if cmd.Run() != nil || ctx.Err() != nil {
				t.Fatal("FIFO metadata rejection blocked or failed")
			}
			info, err := os.Lstat(path)
			if err != nil || info.Mode()&os.ModeNamedPipe == 0 {
				t.Fatal("FIFO evidence was replaced or deleted")
			}
		})
	}
}
