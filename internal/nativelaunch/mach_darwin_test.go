//go:build darwin

package nativelaunch

import (
	"bytes"
	"context"
	_ "embed"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// This is only an explicit native test fixture, not a cgo production adapter,
// launcher, grant or adoption route. Default tests never invoke a compiler.
//
//go:embed testdata/mach_header_probe.c
var machHeaderProbe []byte

func runIsolatedMachCommand(ctx context.Context, cmd *exec.Cmd) error {
	cmd.Env = []string{}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	// The explicit native fixture/compiler group contains only our own
	// subprocesses. A timeout is refusal and kills any remaining descendants.
	if ctx.Err() != nil && cmd.Process != nil && cmd.Process.Pid > 0 {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return err
}

func TestNativeDarwinHeaderOnlyMachBounds(t *testing.T) {
	if os.Getenv("GITCODE_MCP_NATIVE_AUTHORITY_MACH") != "1" {
		t.Skip("explicit native Mach qualification not requested")
	}
	root := t.TempDir()
	source := filepath.Join(root, "probe.c")
	artifact := filepath.Join(root, "probe")
	if err := os.WriteFile(source, machHeaderProbe, 0600); err != nil {
		t.Fatal("isolated Mach fixture could not be prepared")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	compiler := exec.CommandContext(ctx, "/usr/bin/clang", "-Wall", "-Wextra", "-Werror", "-O2", source, "-o", artifact)
	compiler.Stdout, compiler.Stderr = io.Discard, io.Discard
	err := runIsolatedMachCommand(ctx, compiler)
	cancel()
	if err != nil {
		t.Fatal("isolated Mach compiler or SDK prerequisite failed")
	}
	for _, mode := range []string{"valid", "inline", "ool", "ports"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, artifact, mode)
			var result bytes.Buffer
			cmd.Stdout, cmd.Stderr = &result, io.Discard
			err := runIsolatedMachCommand(ctx, cmd)
			if err != nil || result.String() != "isolated header-only provenance qualification PASS\n" {
				t.Fatal("isolated Mach provenance or pre-copyout bound failed")
			}
		})
	}
}
