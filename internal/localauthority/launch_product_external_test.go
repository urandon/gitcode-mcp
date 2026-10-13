//go:build darwin || linux

package localauthority_test

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority"
	"gitcode-mcp/internal/servicectl"
	"golang.org/x/sys/unix"
)

func buildNativeProduct(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("native product source boundary unavailable")
	}
	executable := filepath.Join(privateTemp(t), "authority-product")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", executable, "./cmd/gitcode-mcp")
	cmd.Dir = filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	// Build infrastructure is separate from the strict child. Reuse already
	// installed module/compiler caches; network/toolchain acquisition is denied.
	cmd.Env = []string{"GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "PATH=/usr/bin:/bin"}
	for _, key := range []string{"HOME", "GOPATH", "GOCACHE", "GOROOT", "TMPDIR", "CGO_ENABLED"} {
		if value := os.Getenv(key); value != "" {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	if cmd.Run() != nil {
		t.Fatal("offline native product build failed")
	}
	return executable
}

type productChild struct {
	cmd    *exec.Cmd
	done   chan struct{}
	client *servicectl.RPCClient
}

func spawnProductChild(t *testing.T, executable string, p *localauthority.PreparedRegistry, dir, misuse string) productChild {
	t.Helper()
	prepared, err := p.PrepareLaunch(context.Background(), []localauthority.ProjectBinding{{ProjectID: fixtureProject, Directory: dir}})
	if err != nil {
		t.Fatal("native product preparation failed")
	}
	defer prepared.Close()
	registry, err := prepared.Descriptor()
	if err != nil {
		t.Fatal("native product registry role failed")
	}
	defer registry.Close()
	path := filepath.Join(privateTemp(t), "s")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil || os.Chmod(path, 0600) != nil {
		t.Fatal("native product listener fixture failed")
	}
	listener.SetUnlinkOnClose(false)
	defer listener.Close()
	identity, err := os.Lstat(path)
	if err != nil {
		t.Fatal("native product socket identity unavailable")
	}
	listenFile, err := listener.File()
	if err != nil {
		t.Fatal("native product listener role duplication failed")
	}
	defer listenFile.Close()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal("native product bootstrap fixture failed")
	}
	for _, fd := range fds {
		unix.CloseOnExec(fd)
	}
	parentFile := os.NewFile(uintptr(fds[0]), "synthetic-parent-role")
	childFile := os.NewFile(uintptr(fds[1]), "synthetic-child-role")
	defer childFile.Close()
	connection, err := net.FileConn(parentFile)
	_ = parentFile.Close()
	if err != nil {
		t.Fatal("native product bootstrap conversion failed")
	}
	parent := connection.(*net.UnixConn)
	defer parent.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, executable, servicectl.AuthorityRuntimeSelector)
	unused := privateTemp(t)
	cmd.Env = []string{"HOME=" + unused, "GITCODE_MCP_CONFIG=" + filepath.Join(unused, "missing"), "GITCODE_MCP_CACHE_PATH=" + filepath.Join(unused, "must-not-create"), "HTTP_PROXY=http://synthetic.invalid:9", "HTTPS_PROXY=http://127.0.0.1:9"}
	cmd.Dir = unused
	cmd.ExtraFiles = []*os.File{registry, listenFile, childFile}
	switch misuse {
	case "regular-root":
		wrong, err := os.Open(filepath.Join(dir, "tracker.db"))
		if err != nil {
			t.Fatal("native wrong root fixture failed")
		}
		defer wrong.Close()
		cmd.ExtraFiles[0] = wrong
	case "aliased-socket":
		cmd.ExtraFiles[2] = listenFile
	case "unrelated-file":
		extra, err := os.Open(filepath.Join(dir, "tracker.db"))
		if err != nil {
			t.Fatal("native unrelated descriptor fixture failed")
		}
		defer extra.Close()
		cmd.ExtraFiles = append(cmd.ExtraFiles, extra)
	case "inet-stdio":
		fd, err := unix.Socket(unix.AF_INET, unix.SOCK_STREAM, 0)
		if err != nil {
			t.Fatal("native INET role control unavailable")
		}
		unix.CloseOnExec(fd)
		socket := os.NewFile(uintptr(fd), "synthetic-inert-stdio-control")
		defer socket.Close()
		cmd.Stdout = socket
	}
	assertNativeParentDescriptorHygiene(t)
	if cmd.Start() != nil {
		cancel()
		t.Fatal("native product execution failed")
	}
	_ = childFile.Close()
	_ = registry.Close()
	_ = listenFile.Close()
	child := productChild{cmd, make(chan struct{}), &servicectl.RPCClient{Network: "unix", Address: path}}
	go func() { _ = cmd.Wait(); _ = parent.Close(); close(child.done) }()
	t.Cleanup(func() {
		cancel()
		_ = cmd.Process.Kill()
		select {
		case <-child.done:
		case <-time.After(5 * time.Second):
			t.Error("native product child was not joined")
		}
		if current, err := os.Lstat(path); err == nil && os.SameFile(identity, current) {
			_ = os.Remove(path)
		}
	})
	if misuse != "" {
		select {
		case <-child.done:
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("native role control did not refuse before bootstrap")
		}
		expected := map[string]int{"regular-root": 90, "aliased-socket": 96, "unrelated-file": 87, "inet-stdio": 89}[misuse]
		if cmd.ProcessState.ExitCode() != expected {
			t.Fatal("native role control did not exercise its specific admission refusal")
		}
	} else {
		startup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if prepared.Send(startup, parent) != nil {
			t.Fatal("real product early entry did not activate existing authority")
		}
	}
	entries, err := os.ReadDir(unused)
	if err != nil || len(entries) != 0 {
		t.Fatal("strict product startup used inherited config/cache/home setup")
	}
	return child
}

func TestNativeProductAuthorityActualEntryAbruptRestartAndRoleRefusal(t *testing.T) {
	if runtime.GOOS == "darwin" && !nativeDarwinCgo() {
		t.Skip("native Darwin descriptor inventory requires cgo")
	}
	executable := buildNativeProduct(t)
	p, registry, dir := nativeLaunchFixture(t)
	for _, misuse := range []string{"regular-root", "aliased-socket", "unrelated-file", "inet-stdio"} {
		t.Run(misuse, func(t *testing.T) {
			spawnProductChild(t, executable, p, dir, misuse)
			writer, err := localauthority.OpenWriter(context.Background(), registry, dir, fixtureProject)
			if err != nil {
				t.Fatal("native refusal changed or retained authority ownership")
			}
			_ = writer.Close()
		})
	}
	first := spawnProductChild(t, executable, p, dir, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request := nativeMutation("synthetic-product-crash-replay", "Exact product authority")
	var committed authority.MutationReceipt
	if first.client.Call(ctx, "AuthorityV1.Mutate", request, &committed) != nil {
		t.Fatal("real product authority write failed")
	}
	if first.cmd.Process.Kill() != nil {
		t.Fatal("native abrupt exit control failed")
	}
	select {
	case <-first.done:
	case <-ctx.Done():
		t.Fatal("actual abrupt child was not joined")
	}
	next := spawnProductChild(t, executable, p, dir, "")
	var replay authority.MutationReceipt
	if next.client.Call(ctx, "AuthorityV1.Mutate", request, &replay) != nil || !replay.Replayed || replay.Citation != committed.Citation {
		t.Fatal("actual product restart lost exact committed identity")
	}
}
