//go:build darwin || linux

package localauthority

import (
	"context"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestReviewSemanticPhysicalSourceFIFOIsBounded(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("child executable unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestReviewSemanticPhysicalSourceFIFOChild$")
	cmd.Env = []string{"GCM_REVIEW_SOURCE_FIFO_CHILD=1"}
	if cmd.Run() != nil || ctx.Err() != nil {
		t.Fatal("source FIFO physical guard blocked or child failed")
	}
}
func TestReviewSemanticPhysicalSourceFIFOChild(t *testing.T) {
	if os.Getenv("GCM_REVIEW_SOURCE_FIFO_CHILD") == "" {
		return
	}
	q, scope, expected := semanticPhysicalFixture(t)
	file := filepath.Join(scope.store.directory, "tracker.db")
	saved := file + ".review-saved"
	if os.Rename(file, saved) != nil || unix.Mkfifo(file, 0600) != nil {
		t.Fatal("source FIFO control unavailable")
	}
	defer func() {
		if os.Remove(file) != nil || os.Rename(saved, file) != nil {
			t.Fatal("source FIFO cleanup failed")
		}
	}()
	image, err := scanSemanticPhysical(context.Background(), scope, expected)
	if err == nil || image.valid() || !q.uncertain.Load() || q.ioActive.Load() != 0 {
		t.Fatal("source FIFO physical guard escaped bounded refusal")
	}
}
