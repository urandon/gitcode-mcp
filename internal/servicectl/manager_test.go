package servicectl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type testSource struct {
	homeDir   string
	configDir string
	cacheDir  string
}

func (s testSource) Env(string) string                    { return "" }
func (s testSource) UserHomeDir() (string, error)         { return s.homeDir, nil }
func (s testSource) UserConfigDir() (string, error)       { return s.configDir, nil }
func (s testSource) UserCacheDir() (string, error)        { return s.cacheDir, nil }
func (s testSource) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func newTestManager(t *testing.T, goos string) Manager {
	t.Helper()
	root := t.TempDir()
	return Manager{
		Source: testSource{
			homeDir:   filepath.Join(root, "home"),
			configDir: filepath.Join(root, "config"),
			cacheDir:  filepath.Join(root, "cache"),
		},
		BinaryPath: "/tmp/gitcode-mcp-test",
		Version:    "test-version",
		GOOS:       goos,
	}
}

func TestResolvePathsUsesPlatformUserLocations(t *testing.T) {
	darwin := newTestManager(t, "darwin")
	darwinPaths, err := darwin.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if darwinPaths.InstallKind != "launchagent" || !strings.HasSuffix(darwinPaths.InstallPath, filepath.Join("Library", "LaunchAgents", "com.gitcode.gitcode-mcp.plist")) {
		t.Fatalf("darwin install path = %#v", darwinPaths)
	}
	if !strings.HasSuffix(darwinPaths.SocketPath, filepath.Join("gitcode-mcp", "runtime", "control.sock")) {
		t.Fatalf("darwin socket path = %q", darwinPaths.SocketPath)
	}

	linux := newTestManager(t, "linux")
	linuxPaths, err := linux.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if linuxPaths.InstallKind != "systemd-user" || !strings.HasSuffix(linuxPaths.InstallPath, filepath.Join("systemd", "user", "gitcode-mcp.service")) {
		t.Fatalf("linux install path = %#v", linuxPaths)
	}
}

func TestInstallWritesSameBinaryServiceRunDefinition(t *testing.T) {
	manager := newTestManager(t, "darwin")
	status, err := manager.Install(false)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != StatusInstalledStopped || !status.Installed {
		t.Fatalf("install status = %#v", status)
	}
	data, err := os.ReadFile(status.InstallPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/tmp/gitcode-mcp-test", "<string>service</string>", "<string>run</string>"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("install definition missing %q:\n%s", want, string(data))
		}
	}
	if _, err := manager.Install(false); err == nil {
		t.Fatal("second install without overwrite succeeded")
	}
	if _, err := manager.Install(true); err != nil {
		t.Fatalf("install overwrite: %v", err)
	}
}

func TestStartStopUsePlatformRunner(t *testing.T) {
	var commands []string
	manager := newTestManager(t, "linux")
	manager.Runner = func(_ context.Context, name string, args ...string) error {
		commands = append(commands, strings.Join(append([]string{name}, args...), " "))
		return nil
	}
	if _, err := manager.Install(false); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Message != "service start command submitted to systemd-user" {
		t.Fatalf("start status = %#v", status)
	}
	status, err = manager.Stop(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Message != "service stop command submitted to systemd-user" {
		t.Fatalf("stop status = %#v", status)
	}
	want := []string{
		"systemctl --user daemon-reload",
		"systemctl --user start gitcode-mcp.service",
		"systemctl --user stop gitcode-mcp.service",
	}
	if strings.Join(commands, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands = %#v, want %#v", commands, want)
	}
}

func TestStatusDistinguishesRuntimeStates(t *testing.T) {
	manager := newTestManager(t, "darwin")
	status, err := manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != StatusNotInstalled || status.Installed || status.Running {
		t.Fatalf("initial status = %#v", status)
	}

	status, err = manager.Install(false)
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != StatusInstalledStopped || !status.Installed || status.Running {
		t.Fatalf("installed status = %#v", status)
	}

	paths, err := manager.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := writeState(paths, State{PID: os.Getpid(), SocketPath: paths.SocketPath, StartedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	status, err = manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != StatusStaleSocket || !status.PIDAlive || status.SocketPresent || status.Running {
		t.Fatalf("stale socket status = %#v", status)
	}

	if err := os.WriteFile(paths.SocketPath, []byte("socket placeholder"), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err = manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != StatusRunning || !status.Running || !status.SocketPresent {
		t.Fatalf("running status = %#v", status)
	}

	if err := writeState(paths, State{PID: -1, SocketPath: paths.SocketPath, StartedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	status, err = manager.Status()
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != StatusStalePID || status.Running || status.PIDAlive {
		t.Fatalf("stale pid status = %#v", status)
	}
}
