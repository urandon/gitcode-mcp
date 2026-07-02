package servicectl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/rag"
)

const (
	ServiceName = "gitcode-mcp"

	StatusNotInstalled     = "not-installed"
	StatusInstalledStopped = "installed-stopped"
	StatusRunning          = "running"
	StatusStalePID         = "stale-pid"
	StatusStaleSocket      = "stale-socket"
	StatusUnhealthy        = "unhealthy"
)

type Paths struct {
	RuntimeDir  string `json:"runtime_dir"`
	LogDir      string `json:"log_dir"`
	StatePath   string `json:"state_path"`
	PIDPath     string `json:"pid_path"`
	SocketPath  string `json:"socket_path"`
	JobsPath    string `json:"jobs_path"`
	Network     string `json:"network"`
	Address     string `json:"address"`
	InstallPath string `json:"install_path"`
	InstallKind string `json:"install_kind"`
}

type State struct {
	PID        int       `json:"pid"`
	SocketPath string    `json:"socket_path"`
	StartedAt  time.Time `json:"started_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Version    string    `json:"version,omitempty"`
}

type Status struct {
	Status        string           `json:"status"`
	Installed     bool             `json:"installed"`
	Running       bool             `json:"running"`
	PIDAlive      bool             `json:"pid_alive"`
	SocketPresent bool             `json:"socket_present"`
	PID           int              `json:"pid,omitempty"`
	SocketPath    string           `json:"socket_path"`
	RuntimeDir    string           `json:"runtime_dir"`
	LogDir        string           `json:"log_dir"`
	StatePath     string           `json:"state_path"`
	InstallPath   string           `json:"install_path"`
	InstallKind   string           `json:"install_kind"`
	RAG           *rag.SetupResult `json:"rag,omitempty"`
	UpdatedAt     *time.Time       `json:"updated_at,omitempty"`
	Message       string           `json:"message,omitempty"`
}

type Manager struct {
	Source     config.Source
	BinaryPath string
	Version    string
	GOOS       string
	Runner     CommandRunner
}

type CommandRunner func(context.Context, string, ...string) error

func (m Manager) ResolvePaths() (Paths, error) {
	src := m.Source
	if src == nil {
		src = config.OSSource{}
	}
	cacheDir, err := src.UserCacheDir()
	if err != nil {
		return Paths{}, err
	}
	configDir, err := src.UserConfigDir()
	if err != nil {
		return Paths{}, err
	}
	homeDir, err := src.UserHomeDir()
	if err != nil {
		return Paths{}, err
	}
	runtimeDir := filepath.Join(cacheDir, ServiceName, "runtime")
	goos := m.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	paths := Paths{
		RuntimeDir: runtimeDir,
		LogDir:     filepath.Join(cacheDir, ServiceName, "logs"),
		StatePath:  filepath.Join(runtimeDir, "state.json"),
		PIDPath:    filepath.Join(runtimeDir, "service.pid"),
		SocketPath: filepath.Join(runtimeDir, "control.sock"),
		JobsPath:   filepath.Join(runtimeDir, "jobs.json"),
		Network:    "unix",
	}
	if network := strings.TrimSpace(src.Env("GITCODE_MCP_SERVICE_NETWORK")); network != "" {
		paths.Network = network
	}
	paths.Address = paths.SocketPath
	if address := strings.TrimSpace(src.Env("GITCODE_MCP_SERVICE_ADDRESS")); address != "" {
		paths.Address = address
	}
	switch goos {
	case "darwin":
		paths.InstallKind = "launchagent"
		paths.InstallPath = filepath.Join(homeDir, "Library", "LaunchAgents", "com.gitcode.gitcode-mcp.plist")
	case "linux":
		paths.InstallKind = "systemd-user"
		paths.InstallPath = filepath.Join(configDir, "systemd", "user", "gitcode-mcp.service")
	default:
		paths.InstallKind = "unsupported"
		paths.InstallPath = filepath.Join(configDir, ServiceName, "service-install.json")
	}
	return paths, nil
}

func (m Manager) Install(overwrite bool) (Status, error) {
	paths, err := m.ResolvePaths()
	if err != nil {
		return Status{}, err
	}
	if err := ensurePathDirs(paths); err != nil {
		return Status{}, err
	}
	if _, err := os.Stat(paths.InstallPath); err == nil && !overwrite {
		return Status{}, fmt.Errorf("service: install target already exists: %s", paths.InstallPath)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Status{}, err
	}
	binary := strings.TrimSpace(m.BinaryPath)
	if binary == "" {
		binary = ServiceName
	}
	content := installFileContent(paths.InstallKind, binary, paths)
	if err := os.WriteFile(paths.InstallPath, []byte(content), 0o600); err != nil {
		return Status{}, err
	}
	return m.Status()
}

func (m Manager) Uninstall() (Status, error) {
	paths, err := m.ResolvePaths()
	if err != nil {
		return Status{}, err
	}
	if err := os.Remove(paths.InstallPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Status{}, err
	}
	return m.Status()
}

func (m Manager) Status() (Status, error) {
	paths, err := m.ResolvePaths()
	if err != nil {
		return Status{}, err
	}
	installed := fileExists(paths.InstallPath)
	state, stateOK, err := readState(paths.StatePath)
	if err != nil {
		return Status{}, err
	}
	socketPresent := paths.Network != "unix" || fileExists(paths.SocketPath)
	pidAlive := stateOK && processAlive(state.PID)
	status := Status{
		Status:        StatusNotInstalled,
		Installed:     installed,
		PIDAlive:      pidAlive,
		SocketPresent: socketPresent,
		SocketPath:    paths.SocketPath,
		RuntimeDir:    paths.RuntimeDir,
		LogDir:        paths.LogDir,
		StatePath:     paths.StatePath,
		InstallPath:   paths.InstallPath,
		InstallKind:   paths.InstallKind,
	}
	if stateOK {
		status.PID = state.PID
		status.UpdatedAt = &state.UpdatedAt
	}
	switch {
	case pidAlive && socketPresent:
		status.Status = StatusRunning
		status.Running = true
	case pidAlive && !socketPresent:
		status.Status = StatusStaleSocket
		status.Message = "state pid is alive but control socket is missing"
	case stateOK && !pidAlive:
		status.Status = StatusStalePID
		status.Message = "runtime state references a non-running pid"
	case installed:
		status.Status = StatusInstalledStopped
	default:
		status.Status = StatusNotInstalled
	}
	if paths.InstallKind == "unsupported" && installed {
		status.Status = StatusUnhealthy
		status.Message = "service install target is not supported on this platform"
	}
	return status, nil
}

func (m Manager) Doctor() (Status, error) {
	status, err := m.Status()
	if err != nil {
		return Status{}, err
	}
	src := m.Source
	if src == nil {
		src = config.OSSource{}
	}
	eff, err := config.LoadEffective(src, config.Overrides{})
	if err != nil {
		result := rag.SetupResult{Status: "config_error", Diagnostics: []string{err.Error()}}
		status.RAG = &result
		return status, nil
	}
	result, err := rag.Setup(context.Background(), rag.SetupRequest{Config: eff.Config, DryRun: true})
	if err != nil {
		result = rag.SetupResult{Status: "config_error", Diagnostics: []string{err.Error()}}
	}
	status.RAG = &result
	return status, nil
}

func (m Manager) Start(ctx context.Context) (Status, error) {
	paths, err := m.ResolvePaths()
	if err != nil {
		return Status{}, err
	}
	if !fileExists(paths.InstallPath) {
		status, statusErr := m.Status()
		if statusErr != nil {
			return Status{}, statusErr
		}
		status.Message = "service is not installed"
		return status, nil
	}
	if err := m.runStartCommand(ctx, paths); err != nil {
		return Status{}, err
	}
	status, err := m.Status()
	if err != nil {
		return Status{}, err
	}
	status.Message = "service start command submitted to " + paths.InstallKind
	return status, nil
}

func (m Manager) Stop(ctx context.Context) (Status, error) {
	paths, err := m.ResolvePaths()
	if err != nil {
		return Status{}, err
	}
	if !fileExists(paths.InstallPath) {
		status, statusErr := m.Status()
		if statusErr != nil {
			return Status{}, statusErr
		}
		status.Message = "service is not installed"
		return status, nil
	}
	if err := m.runStopCommand(ctx, paths); err != nil {
		return Status{}, err
	}
	status, err := m.Status()
	if err != nil {
		return Status{}, err
	}
	status.Message = "service stop command submitted to " + paths.InstallKind
	return status, nil
}

func (m Manager) Run(ctx context.Context) error {
	paths, err := m.ResolvePaths()
	if err != nil {
		return err
	}
	if err := ensurePathDirs(paths); err != nil {
		return err
	}
	now := time.Now().UTC()
	state := State{PID: os.Getpid(), SocketPath: paths.SocketPath, StartedAt: now, UpdatedAt: now, Version: m.Version}
	if err := writeState(paths, state); err != nil {
		return err
	}
	jobs := NewJobManager(paths.JobsPath)
	if err := jobs.LoadAndMarkInterrupted(); err != nil {
		return err
	}
	server := RPCServer{Manager: m, Jobs: jobs}
	if paths.Network == "mem" {
		return serveMemoryRPC(ctx, paths.Address, server)
	}
	if paths.Network == "unix" {
		_ = os.Remove(paths.SocketPath)
	}
	listener, err := net.Listen(paths.Network, paths.Address)
	if err != nil {
		return err
	}
	defer listener.Close()
	if paths.Network == "unix" {
		defer os.Remove(paths.SocketPath)
	}
	if err := server.Serve(ctx, listener); err != nil {
		return err
	}
	return ctx.Err()
}

func (m Manager) Client() (*RPCClient, error) {
	paths, err := m.ResolvePaths()
	if err != nil {
		return nil, err
	}
	return &RPCClient{Network: paths.Network, Address: paths.Address, SocketPath: paths.SocketPath}, nil
}

func ensurePathDirs(paths Paths) error {
	for _, dir := range []string{paths.RuntimeDir, paths.LogDir, filepath.Dir(paths.InstallPath)} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func installFileContent(kind, binary string, paths Paths) string {
	switch kind {
	case "launchagent":
		return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "https://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.gitcode.gitcode-mcp</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>service</string><string>run</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, binary, filepath.Join(paths.LogDir, "service.out.log"), filepath.Join(paths.LogDir, "service.err.log"))
	case "systemd-user":
		return fmt.Sprintf(`[Unit]
Description=gitcode-mcp local service

[Service]
ExecStart=%s service run
Restart=on-failure
RuntimeDirectory=gitcode-mcp

[Install]
WantedBy=default.target
`, binary)
	default:
		data, _ := json.MarshalIndent(map[string]string{"binary": binary, "kind": kind}, "", "  ")
		return string(data) + "\n"
	}
}

func readState(path string) (State, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, false, err
	}
	return state, true, nil
}

func writeState(paths Paths, state State) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(paths.StatePath, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.WriteFile(paths.PIDPath, []byte(fmt.Sprintf("%d\n", state.PID)), 0o600)
}

func (m Manager) runStartCommand(ctx context.Context, paths Paths) error {
	switch paths.InstallKind {
	case "launchagent":
		domain := fmt.Sprintf("gui/%d", os.Getuid())
		if err := m.runCommand(ctx, "launchctl", "bootstrap", domain, paths.InstallPath); err != nil {
			return fmt.Errorf("service: launchctl bootstrap failed: %w", err)
		}
		if err := m.runCommand(ctx, "launchctl", "kickstart", "-k", domain+"/com.gitcode.gitcode-mcp"); err != nil {
			return fmt.Errorf("service: launchctl kickstart failed: %w", err)
		}
		return nil
	case "systemd-user":
		if err := m.runCommand(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return fmt.Errorf("service: systemctl daemon-reload failed: %w", err)
		}
		if err := m.runCommand(ctx, "systemctl", "--user", "start", "gitcode-mcp.service"); err != nil {
			return fmt.Errorf("service: systemctl start failed: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("service: start is not supported for install kind %q", paths.InstallKind)
	}
}

func (m Manager) runStopCommand(ctx context.Context, paths Paths) error {
	switch paths.InstallKind {
	case "launchagent":
		domain := fmt.Sprintf("gui/%d", os.Getuid())
		if err := m.runCommand(ctx, "launchctl", "bootout", domain+"/com.gitcode.gitcode-mcp"); err != nil {
			return fmt.Errorf("service: launchctl bootout failed: %w", err)
		}
		return nil
	case "systemd-user":
		if err := m.runCommand(ctx, "systemctl", "--user", "stop", "gitcode-mcp.service"); err != nil {
			return fmt.Errorf("service: systemctl stop failed: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("service: stop is not supported for install kind %q", paths.InstallKind)
	}
}

func (m Manager) runCommand(ctx context.Context, name string, args ...string) error {
	if m.Runner != nil {
		return m.Runner(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...).Run()
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func processAlive(pid int) bool {
	if pid <= 0 || runtime.GOOS == "windows" {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(syscall.Signal(0)) == nil
}
