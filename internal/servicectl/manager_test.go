package servicectl

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startTestUnixListener(t *testing.T, path string) net.Listener {
	t.Helper()
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.Remove(path)
	})
	return listener
}

type testSource struct {
	env       map[string]string
	homeDir   string
	configDir string
	cacheDir  string
}

func (s testSource) Env(key string) string                { return s.env[key] }
func (s testSource) UserHomeDir() (string, error)         { return s.homeDir, nil }
func (s testSource) UserConfigDir() (string, error)       { return s.configDir, nil }
func (s testSource) UserCacheDir() (string, error)        { return s.cacheDir, nil }
func (s testSource) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

func newTestManager(t *testing.T, goos string) Manager {
	t.Helper()
	root, err := shortWorkspaceTemp(t, "svc-")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "gitcode-mcp-test")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return Manager{
		Source: testSource{
			env:       map[string]string{},
			homeDir:   filepath.Join(root, "h"),
			configDir: filepath.Join(root, "f"),
			cacheDir:  filepath.Join(root, "c"),
		},
		BinaryPath: binary,
		Version:    "test-version",
		GOOS:       goos,
	}
}

func shortWorkspaceTemp(t *testing.T, pattern string) (string, error) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	root := cwd
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			return "", os.ErrNotExist
		}
		root = parent
	}
	base := filepath.Join(root, ".t")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp(base, pattern)
	if err != nil {
		return "", err
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir, nil
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
	for _, want := range []string{manager.BinaryPath, "<string>service</string>", "<string>run</string>"} {
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

func TestInstallResolvesBareExecutableToAbsolutePath(t *testing.T) {
	manager := newTestManager(t, "darwin")
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(binDir, ServiceName)
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	manager.BinaryPath = ServiceName

	status, err := manager.Install(false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(status.InstallPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "<string>"+binary+"</string>") {
		t.Fatalf("install definition did not resolve bare executable:\n%s", data)
	}
}

func TestDoctorRejectsBrokenRelativeInstalledExecutable(t *testing.T) {
	manager := newTestManager(t, "darwin")
	paths, err := manager.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := ensurePathDirs(paths); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.InstallPath, []byte(installFileContent(paths.InstallKind, ServiceName, paths)), 0o600); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Doctor()
	if err != nil {
		t.Fatal(err)
	}
	if status.Status != StatusUnhealthy || status.Message != "installed service executable is not usable; run gitcode-mcp service repair" {
		t.Fatalf("doctor status=%#v", status)
	}
}

func TestStartStopUsePlatformRunner(t *testing.T) {
	var commands []string
	manager := newTestManager(t, "linux")
	manager.StartupTimeout = time.Second
	manager.StartupInterval = time.Millisecond
	paths, err := manager.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	manager.Runner = func(_ context.Context, name string, args ...string) error {
		commands = append(commands, strings.Join(append([]string{name}, args...), " "))
		if name == "systemctl" && len(args) >= 2 && args[1] == "start" {
			now := time.Now().UTC()
			if err := writeState(paths, State{PID: os.Getpid(), SocketPath: paths.SocketPath, StartedAt: now, UpdatedAt: now}); err != nil {
				return err
			}
			startTestUnixListener(t, paths.SocketPath)
		}
		return nil
	}
	if _, err := manager.Install(false); err != nil {
		t.Fatal(err)
	}
	status, err := manager.Start(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Message != "service is running under systemd-user" {
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

func TestInstallRejectsUnsafeExecutablePaths(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "empty", path: ""},
		{name: "relative path", path: filepath.Join("bin", ServiceName)},
		{name: "missing absolute", path: filepath.Join(t.TempDir(), "missing")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager := newTestManager(t, "darwin")
			manager.BinaryPath = tt.path
			if _, err := manager.Install(false); err == nil {
				t.Fatal("Install succeeded")
			}
		})
	}
	manager := newTestManager(t, "darwin")
	nonExecutable := filepath.Join(t.TempDir(), ServiceName)
	if err := os.WriteFile(nonExecutable, []byte("binary"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager.BinaryPath = nonExecutable
	if _, err := manager.Install(false); err == nil || !strings.Contains(err.Error(), "recovery:") {
		t.Fatal("Install accepted non-executable file")
	}
}

func TestInstallDefinitionCarriesRuntimeDirectory(t *testing.T) {
	manager := newTestManager(t, "darwin")
	status, err := manager.Install(false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(status.InstallPath)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := manager.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<key>EnvironmentVariables</key>", "<key>GITCODE_MCP_SERVICE_RUNTIME_DIR</key>", escapeXMLText(paths.RuntimeDir)} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("LaunchAgent definition missing %q:\n%s", want, data)
		}
	}
}

func TestInstallEscapesLaunchAgentAndSystemdExecutable(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "git&code mcp")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	darwin := newTestManager(t, "darwin")
	darwin.BinaryPath = binary
	status, err := darwin.Install(false)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(status.InstallPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "git&amp;code mcp") {
		t.Fatalf("LaunchAgent path is not XML escaped:\n%s", data)
	}
	if err := validateInstalledDefinition("launchagent", status.InstallPath); err != nil {
		t.Fatalf("escaped LaunchAgent is not readable: %v", err)
	}

	linux := newTestManager(t, "linux")
	linux.BinaryPath = binary
	status, err = linux.Install(false)
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(status.InstallPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "ExecStart=\"") {
		t.Fatalf("systemd executable is not quoted:\n%s", data)
	}
	if err := validateInstalledDefinition("systemd-user", status.InstallPath); err != nil {
		t.Fatalf("quoted systemd definition is not readable: %v", err)
	}
}

func TestLaunchAgentStartHandlesAlreadyLoadedAndWaitsForHealth(t *testing.T) {
	manager := newTestManager(t, "darwin")
	manager.StartupTimeout = time.Second
	manager.StartupInterval = time.Millisecond
	if _, err := manager.Install(false); err != nil {
		t.Fatal(err)
	}
	paths, err := manager.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	var commands []string
	manager.OutputRunner = func(_ context.Context, name string, args ...string) (string, error) {
		commands = append(commands, strings.Join(append([]string{name}, args...), " "))
		return "state = exited", nil
	}
	manager.Runner = func(_ context.Context, name string, args ...string) error {
		commands = append(commands, strings.Join(append([]string{name}, args...), " "))
		if name == "launchctl" && len(args) > 0 && args[0] == "kickstart" {
			now := time.Now().UTC()
			if err := writeState(paths, State{PID: os.Getpid(), SocketPath: paths.SocketPath, StartedAt: now, UpdatedAt: now}); err != nil {
				return err
			}
			startTestUnixListener(t, paths.SocketPath)
			return nil
		}
		return nil
	}
	status, err := manager.Start(context.Background())
	if err != nil || !status.Running {
		t.Fatalf("Start status=%#v err=%v", status, err)
	}
	joined := strings.Join(commands, "\n")
	if strings.Contains(joined, " bootstrap ") || !strings.Contains(joined, "launchctl kickstart -k") {
		t.Fatalf("commands:\n%s", joined)
	}
}

func TestLaunchAgentStartReportsBoundedFailureState(t *testing.T) {
	manager := newTestManager(t, "darwin")
	manager.StartupTimeout = 10 * time.Millisecond
	manager.StartupInterval = time.Millisecond
	if _, err := manager.Install(false); err != nil {
		t.Fatal(err)
	}
	manager.OutputRunner = func(_ context.Context, _ string, _ ...string) (string, error) {
		return "state = spawn scheduled\nlast exit code = 78: EX_CONFIG\n", nil
	}
	manager.Runner = func(context.Context, string, ...string) error { return nil }
	status, err := manager.Start(context.Background())
	if err == nil || status.Status != StatusUnhealthy || !strings.Contains(err.Error(), "last exit code = 78: EX_CONFIG") || !strings.Contains(err.Error(), "gitcode-mcp service doctor") {
		t.Fatalf("Start status=%#v err=%v", status, err)
	}
}

func TestLaunchAgentBootstrapExitFiveIsTranslated(t *testing.T) {
	manager := newTestManager(t, "darwin")
	if _, err := manager.Install(false); err != nil {
		t.Fatal(err)
	}
	manager.OutputRunner = func(context.Context, string, ...string) (string, error) { return "", errors.New("not loaded") }
	manager.Runner = func(_ context.Context, _ string, args ...string) error {
		if len(args) > 0 && args[0] == "bootstrap" {
			return errors.New("exit status 5")
		}
		return nil
	}
	_, err := manager.Start(context.Background())
	if err == nil || strings.Contains(err.Error(), "exit status 5") || !strings.Contains(err.Error(), "service repair") {
		t.Fatalf("error=%v", err)
	}
}

func TestLaunchAgentBootstrapExitFiveContinuesWhenJobBecameLoaded(t *testing.T) {
	manager := newTestManager(t, "darwin")
	manager.StartupTimeout = time.Second
	manager.StartupInterval = time.Millisecond
	if _, err := manager.Install(false); err != nil {
		t.Fatal(err)
	}
	paths, err := manager.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	prints := 0
	manager.OutputRunner = func(context.Context, string, ...string) (string, error) {
		prints++
		if prints == 1 {
			return "", errors.New("not loaded")
		}
		return "state = exited", nil
	}
	manager.Runner = func(_ context.Context, _ string, args ...string) error {
		if len(args) > 0 && args[0] == "bootstrap" {
			return errors.New("exit status 5")
		}
		if len(args) > 0 && args[0] == "kickstart" {
			now := time.Now().UTC()
			if err := writeState(paths, State{PID: os.Getpid(), SocketPath: paths.SocketPath, StartedAt: now, UpdatedAt: now}); err != nil {
				return err
			}
			startTestUnixListener(t, paths.SocketPath)
			return nil
		}
		return nil
	}
	status, err := manager.Start(context.Background())
	if err != nil || !status.Running || prints != 2 {
		t.Fatalf("Start status=%#v err=%v prints=%d", status, err, prints)
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
	if status.Status != StatusStaleSocket || status.Running || status.SocketPresent {
		t.Fatalf("regular socket placeholder was accepted = %#v", status)
	}

	startTestUnixListener(t, paths.SocketPath)
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

func TestRepairReloadsLoadedBrokenLaunchAgentAndWaitsForUnixSocket(t *testing.T) {
	manager := newTestManager(t, "darwin")
	manager.StartupTimeout = time.Second
	manager.StartupInterval = time.Millisecond
	paths, err := manager.ResolvePaths()
	if err != nil {
		t.Fatal(err)
	}
	if err := ensurePathDirs(paths); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.InstallPath, []byte(installFileContent(paths.InstallKind, ServiceName, paths)), 0o600); err != nil {
		t.Fatal(err)
	}

	loaded := true
	var commands []string
	manager.OutputRunner = func(_ context.Context, name string, args ...string) (string, error) {
		commands = append(commands, strings.Join(append([]string{name}, args...), " "))
		if loaded {
			return "state = exited", nil
		}
		return "", errors.New("not loaded")
	}
	manager.Runner = func(_ context.Context, name string, args ...string) error {
		commands = append(commands, strings.Join(append([]string{name}, args...), " "))
		if name == "launchctl" && len(args) > 0 && args[0] == "bootout" {
			loaded = false
		}
		if name == "launchctl" && len(args) > 0 && args[0] == "bootstrap" {
			loaded = true
		}
		if name == "launchctl" && len(args) > 0 && args[0] == "kickstart" {
			now := time.Now().UTC()
			if err := writeState(paths, State{PID: os.Getpid(), SocketPath: paths.SocketPath, StartedAt: now, UpdatedAt: now}); err != nil {
				return err
			}
			startTestUnixListener(t, paths.SocketPath)
		}
		return nil
	}

	status, err := manager.Repair(context.Background())
	if err != nil || !status.Running {
		t.Fatalf("Repair status=%#v err=%v", status, err)
	}
	data, err := os.ReadFile(paths.InstallPath)
	if err != nil {
		t.Fatal(err)
	}
	program, err := launchAgentProgram(data)
	if err != nil {
		t.Fatal(err)
	}
	if program != manager.BinaryPath {
		t.Fatalf("repaired program=%q want %q", program, manager.BinaryPath)
	}
	joined := strings.Join(commands, "\n")
	bootout := strings.Index(joined, "launchctl bootout")
	bootstrap := strings.Index(joined, "launchctl bootstrap")
	if bootout < 0 || bootstrap < 0 || bootout > bootstrap {
		t.Fatalf("repair did not unload before bootstrap:\n%s", joined)
	}
}
