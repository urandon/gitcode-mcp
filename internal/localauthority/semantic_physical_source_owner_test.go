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

func TestSemanticPhysicalNamedOwnerReplacementFences(t *testing.T) {
	q, scope, expected := semanticPhysicalFixture(t)
	image, err := scanSemanticPhysical(context.Background(), scope, expected)
	if err != nil || q.observe(scope, image) != nil {
		t.Fatal("initial owner qualification failed")
	}
	owner := filepath.Join(scope.store.lease.registry, string(testProject)+".owner")
	body, err := readOwner(scope.store.lease)
	if err != nil || os.Rename(owner, owner+".saved") != nil || os.WriteFile(owner, body, 0600) != nil {
		t.Fatal("identical-body owner replacement fixture failed")
	}
	defer func() {
		if os.Remove(owner) != nil || os.Rename(owner+".saved", owner) != nil {
			t.Fatal("owner replacement cleanup failed")
		}
	}()
	image, err = scanSemanticPhysical(context.Background(), scope, expected)
	if err == nil || image.valid() || !q.uncertain.Load() {
		t.Fatal("named owner replacement bypassed full source guard")
	}
	if r, e := q.reserve(scope, semanticUsage{}); e == nil {
		r.joinedUncertain()
		t.Fatal("replaced named owner granted fresh growth")
	}
}

func TestSemanticPhysicalNamedOwnerFIFOIsBounded(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal("owner child executable unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, "-test.run=^TestSemanticPhysicalNamedOwnerFIFOChild$")
	cmd.Env = []string{"GCM_SEMANTIC_OWNER_FIFO_CHILD=1"}
	if cmd.Run() != nil || ctx.Err() != nil {
		t.Fatal("owner FIFO guard was not a bounded refusal")
	}
}

func TestSemanticPhysicalNamedOwnerFIFOChild(t *testing.T) {
	if os.Getenv("GCM_SEMANTIC_OWNER_FIFO_CHILD") == "" {
		return
	}
	q, scope, expected := semanticPhysicalFixture(t)
	owner := filepath.Join(scope.store.lease.registry, string(testProject)+".owner")
	if os.Rename(owner, owner+".saved") != nil || unix.Mkfifo(owner, 0600) != nil {
		t.Fatal("owner FIFO fixture failed")
	}
	defer func() {
		if os.Remove(owner) != nil || os.Rename(owner+".saved", owner) != nil {
			t.Fatal("owner FIFO cleanup failed")
		}
	}()
	image, err := scanSemanticPhysical(context.Background(), scope, expected)
	if err == nil || image.valid() || !q.uncertain.Load() || q.ioActive.Load() != 0 {
		t.Fatal("named owner FIFO was not rejected before qualification")
	}
}
