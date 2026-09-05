package servicectl

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"gitcode-mcp/internal/adminhttp"
	"gitcode-mcp/internal/adminui"
	"gitcode-mcp/internal/cache"
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
	RuntimeDir   string `json:"runtime_dir"`
	LogDir       string `json:"log_dir"`
	StatePath    string `json:"state_path"`
	PIDPath      string `json:"pid_path"`
	SocketPath   string `json:"socket_path"`
	JobsPath     string `json:"jobs_path"`
	RegistryPath string `json:"registry_path"`
	Network      string `json:"network"`
	Address      string `json:"address"`
	InstallPath  string `json:"install_path"`
	InstallKind  string `json:"install_kind"`
}

type State struct {
	PID        int       `json:"pid"`
	SocketPath string    `json:"socket_path"`
	StartedAt  time.Time `json:"started_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	Version    string    `json:"version,omitempty"`
	Commit     string    `json:"commit,omitempty"`
	SchemaMin  int       `json:"schema_min,omitempty"`
	SchemaMax  int       `json:"schema_max,omitempty"`
}

type Status struct {
	Status            string             `json:"status"`
	Installed         bool               `json:"installed"`
	Running           bool               `json:"running"`
	PIDAlive          bool               `json:"pid_alive"`
	SocketPresent     bool               `json:"socket_present"`
	PID               int                `json:"pid,omitempty"`
	SocketPath        string             `json:"socket_path"`
	RuntimeDir        string             `json:"runtime_dir"`
	LogDir            string             `json:"log_dir"`
	StatePath         string             `json:"state_path"`
	InstallPath       string             `json:"install_path"`
	InstallKind       string             `json:"install_kind"`
	RAG               *rag.SetupResult   `json:"rag,omitempty"`
	UpdatedAt         *time.Time         `json:"updated_at,omitempty"`
	Message           string             `json:"message,omitempty"`
	BinaryVersion     string             `json:"binary_version,omitempty"`
	BinaryCommit      string             `json:"binary_commit,omitempty"`
	SchemaMin         int                `json:"schema_min,omitempty"`
	SchemaMax         int                `json:"schema_max,omitempty"`
	CacheReadiness    string             `json:"cache_readiness,omitempty"`
	CacheSchemaBlocks []CacheSchemaBlock `json:"cache_schema_blocks,omitempty"`
}

type Manager struct {
	Source             config.Source
	CredentialReporter config.CredentialStatusReporter
	BinaryPath         string
	Version            string
	Commit             string
	// SchemaMin/SchemaMax describe the cache contract published by this daemon
	// binary. Zero values select the schema compiled into the current binary.
	// Explicit values are useful to launch compatibility fixtures as real daemon
	// processes instead of faking their state files.
	SchemaMin             int
	SchemaMax             int
	RuntimeDir            string
	AdminBind             string
	AdminAutoStart        bool
	AdminAllowNonLoopback bool
	AdminSessionTTL       time.Duration
	AdminCachePath        string
	// Offline suppresses live-provider feedback readiness for an explicitly
	// fixture-only daemon. The zero value keeps normal installed daemons live-capable.
	Offline bool
	// JobRetention is the daemon-owned terminal job history policy. Keep it
	// separate from EffectiveConfig: daemon jobs deliberately resolve their
	// cache-scoped provider configuration at execution time.
	JobRetention *config.ServiceJobRetentionConfig
	// EffectiveConfig is an in-memory, non-secret configuration snapshot used by
	// daemon-managed jobs. It is never rendered by service status APIs.
	EffectiveConfig *config.Config
	GOOS            string
	Runner          CommandRunner
	OutputRunner    CommandOutputRunner
	StartupTimeout  time.Duration
	StartupInterval time.Duration
	// maintenanceCacheInspector and its timeout are package-private startup
	// seams. Production uses the canonical read-only cache inspection; tests can
	// deterministically model an OS-level open that ignores cancellation.
	maintenanceCacheInspector      maintenanceCacheInspector
	maintenanceCacheInspectTimeout time.Duration
}

type CommandRunner func(context.Context, string, ...string) error
type CommandOutputRunner func(context.Context, string, ...string) (string, error)

func (m Manager) schemaRange() (int, int) {
	minimum, maximum := m.SchemaMin, m.SchemaMax
	if minimum <= 0 {
		minimum = cache.CurrentSchemaVersion()
	}
	if maximum <= 0 {
		maximum = cache.CurrentSchemaVersion()
	}
	return minimum, maximum
}

func effectiveJobConfig(manager Manager, cachePath string) (config.EffectiveConfig, error) {
	if manager.EffectiveConfig != nil {
		cfg := *manager.EffectiveConfig
		cfg.CachePath = cachePath
		cfg.LockPath = cachePath + ".lock"
		return config.EffectiveConfig{Config: cfg, CachePathSource: "daemon-registry-snapshot"}, nil
	}
	src := manager.Source
	if src == nil {
		src = config.OSSource{}
	}
	return config.LoadEffective(src, config.Overrides{CachePath: cachePath})
}

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
	if configured := strings.TrimSpace(m.RuntimeDir); configured != "" {
		runtimeDir = configured
	}
	goos := m.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	paths := Paths{
		RuntimeDir:   runtimeDir,
		LogDir:       filepath.Join(cacheDir, ServiceName, "logs"),
		StatePath:    filepath.Join(runtimeDir, "state.json"),
		PIDPath:      filepath.Join(runtimeDir, "service.pid"),
		SocketPath:   filepath.Join(runtimeDir, "control.sock"),
		JobsPath:     filepath.Join(runtimeDir, "jobs.json"),
		RegistryPath: filepath.Join(runtimeDir, "managed-caches.json"),
		Network:      "unix",
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
	binary, err := resolveInstallExecutable(m.BinaryPath)
	if err != nil {
		return Status{}, err
	}
	content := installFileContent(paths.InstallKind, binary, paths)
	if err := writeInstallDefinition(paths.InstallPath, []byte(content)); err != nil {
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
	socketPresent := paths.Network != "unix" || unixSocketExists(paths.SocketPath)
	pidAlive := stateOK && processAlive(state.PID)
	schemaMin, schemaMax := m.schemaRange()
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
		BinaryVersion: m.Version,
		BinaryCommit:  m.Commit,
		SchemaMin:     schemaMin,
		SchemaMax:     schemaMax,
	}
	if stateOK {
		status.PID = state.PID
		status.UpdatedAt = &state.UpdatedAt
		// Once a daemon state file exists it is the authority. Zero-valued
		// compatibility fields mean an older daemon did not publish that
		// contract; substituting the inspecting CLI's range would be unsafe.
		status.BinaryVersion = state.Version
		status.BinaryCommit = state.Commit
		status.SchemaMin = state.SchemaMin
		status.SchemaMax = state.SchemaMax
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
	if status.Installed {
		if err := validateInstalledDefinition(status.InstallKind, status.InstallPath); err != nil {
			status.Status = StatusUnhealthy
			status.Running = false
			status.Message = "installed service executable is not usable; run gitcode-mcp service repair"
		}
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
	return m.waitForHealthy(ctx, paths)
}

// Repair replaces both the on-disk service definition and the platform
// manager's loaded copy. Merely overwriting a plist is insufficient on macOS:
// launchd keeps executing the definition it bootstrapped earlier.
func (m Manager) Repair(ctx context.Context) (Status, error) {
	paths, err := m.ResolvePaths()
	if err != nil {
		return Status{}, err
	}
	if _, err := resolveInstallExecutable(m.BinaryPath); err != nil {
		return Status{}, fmt.Errorf("service: repair cannot use the current executable: %w", err)
	}
	if err := m.unloadForRepair(ctx, paths); err != nil {
		return Status{}, err
	}
	if _, err := m.Install(true); err != nil {
		return Status{}, err
	}
	return m.Start(ctx)
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

// QuiesceForCacheMigration unloads the installed coordinator and waits until
// both its process and control socket are gone. A foreground coordinator that
// is not owned by the installed service is refused: the migrator cannot prove
// that it controls that writer lifecycle.
func (m Manager) QuiesceForCacheMigration(ctx context.Context) (Status, error) {
	status, err := m.Status()
	if err != nil {
		return Status{}, err
	}
	if !status.Installed {
		if status.Running || status.PIDAlive {
			return status, RPCDomainError{Code: "cache_schema_coordination_required", Message: "cache migration requires the running coordinator to be installed so it can be quiesced safely"}
		}
		status.Message = "no installed coordinator requires quiescing"
		return status, nil
	}
	paths, err := m.ResolvePaths()
	if err != nil {
		return Status{}, err
	}
	if err := m.unloadForRepair(ctx, paths); err != nil {
		return status, RPCDomainError{Code: "cache_schema_quiesce_failed", Message: "cache migration could not quiesce the installed coordinator: " + err.Error()}
	}
	return m.waitForStopped(ctx, paths)
}

func (m Manager) waitForStopped(ctx context.Context, paths Paths) (Status, error) {
	timeout := m.StartupTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	interval := m.StartupInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		status, err := m.Status()
		if err != nil {
			return Status{}, err
		}
		if !status.PIDAlive && (paths.Network != "unix" || !status.SocketPresent) {
			status.Running = false
			status.Message = "service is quiesced for cache migration"
			return status, nil
		}
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-timer.C:
			return status, RPCDomainError{Code: "cache_schema_quiesce_timeout", Message: fmt.Sprintf("coordinator did not quiesce within %s", timeout)}
		case <-ticker.C:
		}
	}
}

func (m Manager) Run(ctx context.Context) error {
	paths, err := m.ResolvePaths()
	if err != nil {
		return err
	}
	if err := ensurePathDirs(paths); err != nil {
		return err
	}
	if paths.Network == "unix" {
		// Remove a socket left by a prior process before publishing this PID. A
		// new live PID plus an old socket inode must never look ready.
		_ = os.Remove(paths.SocketPath)
	}
	now := time.Now().UTC()
	schemaMin, schemaMax := m.schemaRange()
	state := State{PID: os.Getpid(), SocketPath: paths.SocketPath, StartedAt: now, UpdatedAt: now, Version: m.Version, Commit: m.Commit, SchemaMin: schemaMin, SchemaMax: schemaMax}
	if err := writeState(paths, state); err != nil {
		return err
	}
	retention := config.Default().Service.JobRetention
	if m.JobRetention != nil {
		retention = *m.JobRetention
	}
	jobs := NewJobManagerWithRetention(paths.JobsPath, retention)
	maintenance := NewMaintenanceManager(m, jobs, paths.RegistryPath)
	if err := maintenance.Load(); err != nil {
		return err
	}
	// Load the durable admission registry before recovering jobs so a persisted
	// repository-document cancellation tombstone can distinguish a committed
	// cancellation from a crash that happened before the tombstone write.
	if err := jobs.LoadAndMarkInterrupted(); err != nil {
		return err
	}
	if err := jobs.RecoverSyncStages(ctx, m); err != nil {
		return err
	}
	jobActions := NewJobActionManager(paths.JobsPath+".actions", jobs, maintenance)
	if err := jobActions.Load(); err != nil {
		return err
	}
	controlReceipts := NewAdminControlReceiptManager(paths.JobsPath + ".controls")
	if err := controlReceipts.Load(); err != nil {
		return err
	}
	adminControls := NewAdminControlManager(m, maintenance, jobs, controlReceipts)
	assets, err := fs.Sub(adminui.Files, "assets")
	if err != nil {
		return err
	}
	admin := adminhttp.New(adminhttp.Config{
		Bind: m.AdminBind, AllowNonLoopback: m.AdminAllowNonLoopback, SessionTTL: m.AdminSessionTTL,
		Assets: assets, Readiness: m.adminReadiness,
		Snapshot: func(snapshotContext context.Context) (adminhttp.ObservationSnapshot, error) {
			return m.adminObservation(snapshotContext, jobs, maintenance, now)
		},
		CancelJob:                          jobActions.Cancel,
		RetryJob:                           jobActions.Retry,
		PlanMaintenance:                    adminControls.PlanMaintenance,
		ApplyMaintenance:                   adminControls.ApplyMaintenance,
		DisableMaintenance:                 adminControls.DisableMaintenance,
		ReconcileMaintenance:               adminControls.ReconcileMaintenance,
		PlanMaintenanceConflictResolution:  adminControls.PlanMaintenanceConflictResolution,
		ApplyMaintenanceConflictResolution: adminControls.ApplyMaintenanceConflictResolution,
		PlanBinding:                        adminControls.PlanBinding,
		ApplyBinding:                       adminControls.ApplyBinding,
		PlanFeedbackSetup:                  adminControls.PlanFeedbackSetup,
		ApplyFeedbackSetup:                 adminControls.ApplyFeedbackSetup,
		CompareSearch:                      adminControls.CompareSearch,
		SearchRepositoryDocs:               adminControls.SearchRepositoryDocs,
		PlanRepositoryDocs:                 adminControls.PlanRepositoryDocs,
		IndexRepositoryDocs:                adminControls.IndexRepositoryDocs,
		SmokeProvider:                      adminControls.SmokeProvider,
		PlanRAGRepair:                      adminControls.PlanRAGRepair,
		ApplyRAGRepair:                     adminControls.ApplyRAGRepair,
	})
	if m.AdminAutoStart {
		if _, err := admin.Start(ctx); err != nil {
			return err
		}
	}
	server := RPCServer{Manager: m, Jobs: jobs, Maintenance: maintenance, Admin: admin}
	go maintenance.Run(ctx)
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

func (m Manager) adminReadiness(ctx context.Context) adminhttp.Readiness {
	result := adminhttp.Readiness{Version: m.Version}
	cachePath := m.AdminCachePath
	if cachePath != "" && cachePath != ":memory:" {
		if info, err := os.Stat(cachePath); err == nil && !info.IsDir() {
			if store, err := cache.NewSQLiteReadOnlyStore(ctx, cachePath); err == nil {
				defer store.Close()
				if identity, err := store.CacheIdentity(ctx); err == nil {
					result.CacheReference = publicCacheRef(identity.UUID, cachePath)
				} else {
					result.CacheReference = publicCacheRef("", cachePath)
				}
				if version, err := store.SchemaVersion(ctx); err == nil {
					result.CacheConnected = true
					result.SchemaVersion = version
				}
			}
		}
	}
	return result
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
  <key>EnvironmentVariables</key>
  <dict><key>GITCODE_MCP_SERVICE_RUNTIME_DIR</key><string>%s</string></dict>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, escapeXMLText(binary), escapeXMLText(paths.RuntimeDir), escapeXMLText(filepath.Join(paths.LogDir, "service.out.log")), escapeXMLText(filepath.Join(paths.LogDir, "service.err.log")))
	case "systemd-user":
		return fmt.Sprintf(`[Unit]
Description=gitcode-mcp local service

[Service]
ExecStart=%s service run
Environment=%s
Restart=on-failure
RuntimeDirectory=gitcode-mcp

[Install]
WantedBy=default.target
`, quoteSystemdArgument(binary), quoteSystemdArgument("GITCODE_MCP_SERVICE_RUNTIME_DIR="+paths.RuntimeDir))
	default:
		data, _ := json.MarshalIndent(map[string]string{"binary": binary, "kind": kind}, "", "  ")
		return string(data) + "\n"
	}
}

func writeInstallDefinition(path string, content []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(path), ".gitcode-mcp-service-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(content); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func resolveInstallExecutable(raw string) (string, error) {
	binary := strings.TrimSpace(raw)
	if binary == "" {
		return "", fmt.Errorf("service: executable path is empty; invoke service install through the installed gitcode-mcp binary")
	}
	if strings.ContainsAny(binary, "\x00\r\n") {
		return "", fmt.Errorf("service: executable path contains unsupported control characters")
	}
	if !filepath.IsAbs(binary) {
		if strings.ContainsRune(binary, filepath.Separator) {
			return "", fmt.Errorf("service: executable path must be absolute or a command resolvable through PATH")
		}
		resolved, err := exec.LookPath(binary)
		if err != nil {
			return "", fmt.Errorf("service: executable %q is not resolvable through PATH; invoke service install through an absolute binary path", binary)
		}
		binary, err = filepath.Abs(resolved)
		if err != nil {
			return "", fmt.Errorf("service: resolve executable path: %w", err)
		}
	}
	binary = filepath.Clean(binary)
	if err := validateExecutable(binary); err != nil {
		return "", err
	}
	return binary, nil
}

func validateExecutable(binary string) error {
	if !filepath.IsAbs(binary) {
		return fmt.Errorf("service: installed executable path must be absolute")
	}
	info, err := os.Stat(binary)
	if err != nil {
		return fmt.Errorf("service: installed executable is unavailable; recovery: invoke service install through an existing absolute executable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("service: installed executable is not an executable regular file; recovery: invoke service install through an existing absolute executable")
	}
	return nil
}

func validateInstalledDefinition(kind, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var binary string
	switch kind {
	case "launchagent":
		binary, err = launchAgentProgram(data)
	case "systemd-user":
		binary, err = systemdProgram(data)
	default:
		return fmt.Errorf("unsupported service definition kind %q", kind)
	}
	if err != nil {
		return err
	}
	return validateExecutable(binary)
}

func launchAgentProgram(data []byte) (string, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	wantArguments := false
	inArguments := false
	for {
		token, err := decoder.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", err
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "key":
				var key string
				if err := decoder.DecodeElement(&key, &value); err != nil {
					return "", err
				}
				wantArguments = key == "ProgramArguments"
			case "array":
				if wantArguments {
					inArguments = true
					wantArguments = false
				}
			case "string":
				if inArguments {
					var program string
					if err := decoder.DecodeElement(&program, &value); err != nil {
						return "", err
					}
					if strings.TrimSpace(program) == "" {
						return "", errors.New("empty ProgramArguments[0]")
					}
					return program, nil
				}
			}
		case xml.EndElement:
			if value.Name.Local == "array" && inArguments {
				inArguments = false
			}
		}
	}
	return "", errors.New("ProgramArguments[0] is missing")
}

func systemdProgram(data []byte) (string, error) {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ExecStart=") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "ExecStart="))
		if value == "" {
			return "", errors.New("ExecStart executable is empty")
		}
		if value[0] != '"' {
			return strings.Fields(value)[0], nil
		}
		var builder strings.Builder
		escaped := false
		for i := 1; i < len(value); i++ {
			ch := value[i]
			if escaped {
				builder.WriteByte(ch)
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				return strings.ReplaceAll(strings.ReplaceAll(builder.String(), "$$", "$"), "%%", "%"), nil
			}
			builder.WriteByte(ch)
		}
		return "", errors.New("unterminated ExecStart executable")
	}
	return "", errors.New("ExecStart is missing")
}

func escapeXMLText(value string) string {
	var buffer bytes.Buffer
	_ = xml.EscapeText(&buffer, []byte(value))
	return buffer.String()
}

func quoteSystemdArgument(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\"", "\\\"")
	value = strings.ReplaceAll(value, "$", "$$")
	value = strings.ReplaceAll(value, "%", "%%")
	return "\"" + value + "\""
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
		target := domain + "/com.gitcode.gitcode-mcp"
		if _, err := m.runCommandOutput(ctx, "launchctl", "print", target); err != nil {
			if err := m.runCommand(ctx, "launchctl", "bootstrap", domain, paths.InstallPath); err != nil {
				if _, printErr := m.runCommandOutput(ctx, "launchctl", "print", target); printErr != nil {
					return fmt.Errorf("service: launchagent could not be loaded (%s); recovery: gitcode-mcp service repair", commandFailureReason(err))
				}
			}
		}
		if err := m.runCommand(ctx, "launchctl", "kickstart", "-k", target); err != nil {
			return fmt.Errorf("service: launchagent could not be started (%s); recovery: gitcode-mcp service doctor", commandFailureReason(err))
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

func (m Manager) unloadForRepair(ctx context.Context, paths Paths) error {
	switch paths.InstallKind {
	case "launchagent":
		domain := fmt.Sprintf("gui/%d", os.Getuid())
		target := domain + "/com.gitcode.gitcode-mcp"
		if _, err := m.runCommandOutput(ctx, "launchctl", "print", target); err != nil {
			return nil
		}
		if err := m.runCommand(ctx, "launchctl", "bootout", target); err != nil {
			return fmt.Errorf("service: could not unload the existing launchagent (%s); recovery: gitcode-mcp service doctor", commandFailureReason(err))
		}
		return nil
	case "systemd-user":
		if err := m.runCommand(ctx, "systemctl", "--user", "stop", "gitcode-mcp.service"); err != nil {
			return fmt.Errorf("service: systemctl stop before repair failed: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("service: repair is not supported for install kind %q", paths.InstallKind)
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

func (m Manager) runCommandOutput(ctx context.Context, name string, args ...string) (string, error) {
	if m.OutputRunner != nil {
		return m.OutputRunner(ctx, name, args...)
	}
	if m.Runner != nil {
		return "", m.Runner(ctx, name, args...)
	}
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(output), err
}

func (m Manager) waitForHealthy(ctx context.Context, paths Paths) (Status, error) {
	timeout := m.StartupTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	interval := m.StartupInterval
	if interval <= 0 {
		interval = 100 * time.Millisecond
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		status, err := m.Status()
		if err != nil {
			return Status{}, err
		}
		if status.Running {
			status.Message = "service is running under " + paths.InstallKind
			return status, nil
		}
		select {
		case <-ctx.Done():
			return status, ctx.Err()
		case <-timer.C:
			reason := m.startupFailureReason(ctx, paths, status)
			status.Status = StatusUnhealthy
			status.Running = false
			status.Message = fmt.Sprintf("service did not become healthy within %s (%s); recovery: gitcode-mcp service doctor", timeout, reason)
			return status, errors.New(status.Message)
		case <-ticker.C:
		}
	}
}

func (m Manager) startupFailureReason(ctx context.Context, paths Paths, status Status) string {
	if err := validateInstalledDefinition(paths.InstallKind, paths.InstallPath); err != nil {
		return "installed executable is unusable"
	}
	if paths.InstallKind == "launchagent" {
		target := fmt.Sprintf("gui/%d/com.gitcode.gitcode-mcp", os.Getuid())
		if output, err := m.runCommandOutput(ctx, "launchctl", "print", target); err == nil {
			if summary := launchctlStateSummary(output); summary != "" {
				return summary
			}
		}
	}
	if strings.TrimSpace(status.Message) != "" {
		return status.Message
	}
	return "status=" + status.Status
}

func launchctlStateSummary(output string) string {
	parts := make([]string, 0, 2)
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "state =") || strings.HasPrefix(line, "last exit code =") {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, ", ")
}

func commandFailureReason(err error) string {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if exitErr.ExitCode() == 5 {
			return "launchd rejected the definition or the job changed state while loading"
		}
		return fmt.Sprintf("process exited with status %d", exitErr.ExitCode())
	}
	message := strings.TrimSpace(err.Error())
	if strings.Contains(message, "exit status 5") {
		return "launchd rejected the definition or the job changed state while loading"
	}
	if message == "" {
		return "command failed"
	}
	return message
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func unixSocketExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSocket != 0
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
