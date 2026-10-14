package servicectl

import (
	"context"
	"errors"
	"net"
	"os"
	"sync/atomic"
	"time"

	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/localgit"
	"gitcode-mcp/internal/service"
)

func localHostError(code string) error { return &localgit.Error{Code: code, Field: "service"} }

// This is the same Manager lifecycle and private RPC implementation, with an
// early startup branch that never constructs maintenance/jobs/providers/Admin.
func (m Manager) runLocalProjects(ctx context.Context) error {
	cfg := m.EffectiveConfig
	if cfg == nil || m.AdminAutoStart || m.AdminBind != "" || m.AdminAllowNonLoopback {
		return config.LocalProjectConfigError{}
	}
	paths, err := m.ResolvePaths()
	if err != nil {
		return localHostError("unavailable")
	}
	if paths.Network != "unix" && paths.Network != "mem" {
		return localHostError("private_ipc_required")
	}
	if paths.Network == "unix" && paths.Address != paths.SocketPath {
		return localHostError("private_ipc_required")
	}
	if state, found, err := readState(paths.StatePath); err != nil {
		return localHostError("unavailable")
	} else if found && processAlive(state.PID) {
		return localHostError("service_busy")
	}
	host, err := service.NewLocalProjects(ctx, cfg.LocalProjects, func(ref string) (string, error) { return config.LocalProjectStorePath(m.Source, ref) })
	if err != nil {
		return err
	}
	if err := ensurePathDirs(paths); err != nil {
		return localHostError("unavailable")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	now := time.Now().UTC()
	min, max := m.schemaRange()
	if err := writeState(paths, State{PID: os.Getpid(), SocketPath: paths.SocketPath, StartedAt: now, UpdatedAt: now, Version: m.Version, Commit: m.Commit, SchemaMin: min, SchemaMax: max}); err != nil {
		return localHostError("unavailable")
	}
	defer os.Remove(paths.PIDPath)
	defer os.Remove(paths.StatePath)
	var handlers atomic.Int64
	server := RPCServer{Manager: m, LocalProjects: host, LocalOnly: true, runtimeContext: ctx, observationHandlers: &handlers}
	if paths.Network == "mem" {
		err := serveMemoryRPC(ctx, paths.Address, server, nil)
		// Removal from memoryRPC closes admission under its existing mutex.
		// Every previously admitted local call owns this counter and a context
		// linked to ctx; returning means its Git process and handler have joined.
		for handlers.Load() > 0 {
			time.Sleep(time.Millisecond)
		}
		return err
	}
	if err := os.Remove(paths.SocketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return localHostError("unavailable")
	}
	listener, err := net.Listen("unix", paths.Address)
	if err != nil {
		return localHostError("unavailable")
	}
	defer listener.Close()
	defer os.Remove(paths.SocketPath)
	if err := os.Chmod(paths.SocketPath, 0600); err != nil {
		return localHostError("unavailable")
	}
	if err := server.Serve(ctx, listener); err != nil {
		return localHostError("unavailable")
	}
	return ctx.Err()
}
