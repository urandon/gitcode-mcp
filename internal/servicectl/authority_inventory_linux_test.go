//go:build linux

package servicectl

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
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
	if len(os.Args) == 2 && os.Args[1] == "__authority_roles_fixture" {
		if err := authorityInheritedRoles(); err != nil {
			if role, ok := err.(authorityRoleRefusal); ok {
				os.Exit(int(role))
			}
			os.Exit(78)
		}
		os.Exit(0)
	}
	if len(os.Args) == 2 && os.Args[1] == "__authority_inventory_fixture" {
		before := nativeOpenDescriptorCount()
		classes := map[string]int{}
		for fd := 3; fd < 1024; fd++ {
			var st unix.Stat_t
			if unix.Fstat(fd, &st) != nil {
				continue
			}
			name := "other"
			var buf [128]byte
			n, err := unix.Readlink("/proc/self/fd/"+strconv.Itoa(fd), buf[:])
			if err == nil {
				switch string(buf[:n]) {
				case "anon_inode:[eventpoll]":
					name = "eventpoll"
				case "anon_inode:[eventfd]":
					name = "eventfd"
				}
			}
			classes[name]++
		}
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
			Before  int            `json:"before"`
			After   int            `json:"after"`
			Control int            `json:"control"`
			Valid   bool           `json:"valid"`
			Classes map[string]int `json:"classes"`
		}{before, after, control, err == nil && positive && len(inventory) == before && visibilityErr == nil && len(visible) == control && seen[0] && seen[1], classes}
		body, _ := json.Marshal(result)
		_, _ = os.Stdout.Write(body)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// This checks the production census with actual inherited roles before any
// bootstrap work, without depending on a qualified authority filesystem.
func TestAuthorityNativeFixedRolesAdmission(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("native role executable unavailable")
	}
	dir := t.TempDir()
	if os.Chmod(dir, 0700) != nil {
		t.Fatal("native private directory fixture unavailable")
	}
	root, err := os.Open(dir)
	if err != nil {
		t.Fatal("native directory role unavailable")
	}
	defer root.Close()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, "s"), Net: "unix"})
	if err != nil {
		t.Fatal("native listening role unavailable")
	}
	defer listener.Close()
	if os.Chmod(listener.Addr().String(), 0600) != nil {
		t.Fatal("native private listener fixture unavailable")
	}
	file, err := listener.File()
	if err != nil {
		t.Fatal("native listener duplication unavailable")
	}
	defer file.Close()
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal("native connected role unavailable")
	}
	parent := os.NewFile(uintptr(pair[0]), "synthetic-parent-role")
	defer parent.Close()
	child := os.NewFile(uintptr(pair[1]), "synthetic-role")
	defer child.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "__authority_roles_fixture")
	expected := 0
	if product := os.Getenv("GITCODE_MCP_NATIVE_ROLE_FIXTURE_BINARY"); product != "" {
		cmd = exec.CommandContext(ctx, product, AuthorityRuntimeSelector)
		expected = 82 // valid roles followed by deliberately absent preparation
	}
	cmd.Env = []string{}
	cmd.Dir = "/"
	cmd.ExtraFiles = []*os.File{root, file, child}
	if cmd.Start() != nil {
		t.Fatal("native role execution unavailable")
	}
	_ = child.Close()
	_ = parent.Close()
	_ = cmd.Wait()
	if cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != expected {
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		t.Fatalf("native fixed role admission refused: phase %d", code)
	}
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
		Before  int            `json:"before"`
		After   int            `json:"after"`
		Control int            `json:"control"`
		Valid   bool           `json:"valid"`
		Classes map[string]int `json:"classes"`
	}
	if json.Unmarshal(body, &result) != nil || !result.Valid || result.Before != result.After || result.Control != result.After+2 {
		t.Fatal("native census changed process descriptors or failed its calibrated control")
	}
	if result.Before != 3 {
		t.Fatalf("native startup baseline has unexpected resources: count=%d classes=%v", result.Before, result.Classes)
	}
}
