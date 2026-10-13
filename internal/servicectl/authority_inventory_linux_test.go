//go:build linux

package servicectl

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func nativeOpenDescriptorCount() int {
	count := 0
	for fd := 0; fd < 1024; fd++ {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err == nil {
			count++
		}
	}
	return count
}

// A fresh executable is required: a normal test process may already own a
// poller and hide accidental initialization by the census implementation.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == "__authority_inventory_fixture" {
		before := nativeOpenDescriptorCount()
		inventory, err := authorityDescriptorInventory()
		after := nativeOpenDescriptorCount()
		var pipe [2]int
		positive := unix.Pipe2(pipe[:], unix.O_CLOEXEC) == nil
		control := nativeOpenDescriptorCount()
		visible, visibilityErr := authorityDescriptorInventory()
		seen := [2]bool{}
		for _, fd := range visible {
			if fd == pipe[0] {
				seen[0] = true
			}
			if fd == pipe[1] {
				seen[1] = true
			}
		}
		if positive {
			_ = unix.Close(pipe[0])
			_ = unix.Close(pipe[1])
		}
		result := struct {
			Before  int  `json:"before"`
			After   int  `json:"after"`
			Control int  `json:"control"`
			Valid   bool `json:"valid"`
		}{before, after, control, err == nil && positive && len(inventory) == before && visibilityErr == nil && len(visible) == control && seen[0] && seen[1]}
		body, _ := json.Marshal(result)
		_, _ = os.Stdout.Write(body)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestAuthorityNativeCensusDoesNotInitializeGoPoller(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("native census executable unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "__authority_inventory_fixture")
	cmd.Env = []string{}
	cmd.Dir = "/"
	body, err := cmd.Output()
	if err != nil || len(body) > 256 {
		t.Fatal("native census fixture did not terminate with bounded evidence")
	}
	var result struct {
		Before  int  `json:"before"`
		After   int  `json:"after"`
		Control int  `json:"control"`
		Valid   bool `json:"valid"`
	}
	if json.Unmarshal(body, &result) != nil || !result.Valid || result.Before != result.After || result.Control != result.After+2 {
		t.Fatal("native census changed process descriptors or failed its calibrated control")
	}
}
