package servicectl

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestLaunchAgentLiveBootstrap is deliberately opt-in because it registers a
// real per-session LaunchAgent. It uses a unique label and isolated runtime,
// verifies a real JSON-RPC call over the Unix socket, and always boots the job
// out again. Run on macOS with GITCODE_MCP_LIVE_LAUNCHD_TEST=1.
func TestLaunchAgentLiveBootstrap(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Getenv("GITCODE_MCP_LIVE_LAUNCHD_TEST") != "1" {
		t.Skip("set GITCODE_MCP_LIVE_LAUNCHD_TEST=1 on macOS to exercise launchd")
	}

	root, err := os.MkdirTemp("/private/tmp", "gitcode-mcp-launchd-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	binary := filepath.Join(root, "bin", ServiceName)
	if err := os.MkdirAll(filepath.Dir(binary), 0o700); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", binary, "./cmd/gitcode-mcp")
	build.Dir = repoRoot
	build.Env = append(os.Environ(), "GOCACHE=/private/tmp/gitcode-mcp-go-build")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build service binary: %v\n%s", err, output)
	}

	manager := Manager{
		Source: testSource{
			env:       map[string]string{},
			homeDir:   filepath.Join(root, "home"),
			configDir: filepath.Join(root, "config"),
			cacheDir:  filepath.Join(root, "cache"),
		},
		BinaryPath: binary,
		RuntimeDir: filepath.Join(root, "runtime"),
		GOOS:       "darwin",
	}
	status, err := manager.Install(false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(status.InstallPath)
	if err != nil {
		t.Fatal(err)
	}
	label := fmt.Sprintf("com.gitcode.gitcode-mcp.integration.%d.%d", os.Getpid(), time.Now().UnixNano())
	data = []byte(strings.Replace(string(data), "<string>com.gitcode.gitcode-mcp</string>", "<string>"+label+"</string>", 1))
	if err := os.WriteFile(status.InstallPath, data, 0o600); err != nil {
		t.Fatal(err)
	}

	domain := fmt.Sprintf("gui/%d", os.Getuid())
	target := domain + "/" + label
	t.Cleanup(func() {
		_ = exec.Command("launchctl", "bootout", target).Run()
	})
	if output, err := exec.Command("launchctl", "bootstrap", domain, status.InstallPath).CombinedOutput(); err != nil {
		t.Fatalf("launchctl bootstrap: %v: %s", err, strings.TrimSpace(string(output)))
	}
	if output, err := exec.Command("launchctl", "kickstart", "-k", target).CombinedOutput(); err != nil {
		t.Fatalf("launchctl kickstart: %v: %s", err, strings.TrimSpace(string(output)))
	}

	client, err := manager.Client()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	var live Status
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		err = client.Call(ctx, "Service.Status", nil, &live)
		cancel()
		if err == nil && live.Running && live.SocketPresent {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("LaunchAgent did not expose a usable control socket: status=%#v err=%v", live, err)
}
