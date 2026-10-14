package servicectl

import (
	"context"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority"
)

// AuthorityRuntimeSelector is a fixed internal child entry, not a command with
// root/FD/profile/config selectors. Normal public workflow belongs to L5.
const AuthorityRuntimeSelector = "__local_authority_runtime"

type AuthorityProcess struct {
	cmd    *exec.Cmd
	cancel context.CancelFunc
	done   chan struct{}
	exit   error
	client *RPCClient
}

// StartAuthorityRuntime is explicit trusted launch, never invoked by a read.
// It consumes the existing private listener, including on startup refusal.
// Setup/preparation remain separate; child receives exactly three fixed roles.
func StartAuthorityRuntime(ctx context.Context, preparation *localauthority.NativePreparation, listener *net.UnixListener) (*AuthorityProcess, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, authority.Error{Code: authority.Unavailable}
	}
	return startAuthorityRuntime(ctx, preparation, listener, executable)
}

func startAuthorityRuntime(ctx context.Context, preparation *localauthority.NativePreparation, listener *net.UnixListener, executable string) (*AuthorityProcess, error) {
	if listener == nil || preparation == nil || ctx.Err() != nil {
		if listener != nil {
			_ = listener.Close()
		}
		return nil, authority.Error{Code: authority.InitializationRequired}
	}
	path := listener.Addr().String()
	identity, err := privateAuthoritySocket(path)
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	listener.SetUnlinkOnClose(false)
	defer listener.Close()
	cleanup := func() {
		if current, e := os.Lstat(path); e == nil && os.SameFile(identity, current) {
			_ = os.Remove(path)
		}
	}
	ok := false
	defer func() {
		if !ok {
			cleanup()
		}
	}()
	registry, err := preparation.Descriptor()
	if err != nil {
		return nil, err
	}
	defer registry.Close()
	listenFile, err := listener.File()
	if err != nil {
		return nil, authority.Error{Code: authority.Unavailable}
	}
	defer listenFile.Close()
	parent, child, err := authorityBootstrapPair()
	if err != nil {
		return nil, err
	}
	keepBootstrap := false
	defer func() {
		if !keepBootstrap {
			_ = parent.Close()
		}
	}()
	defer child.Close()
	// Empty environment and inert stdio are deliberate. Nothing about the
	// inherited shell/proxy/provider settings grants authority or setup rights.
	cmd := exec.Command(executable, AuthorityRuntimeSelector)
	cmd.Env = []string{}
	cmd.Dir = "/"
	cmd.ExtraFiles = []*os.File{registry, listenFile, child}
	lifetime, cancel := context.WithCancel(ctx)
	p := &AuthorityProcess{cmd: cmd, cancel: cancel, done: make(chan struct{}), client: &RPCClient{Network: "unix", Address: path}}
	if cmd.Start() != nil {
		cancel()
		return nil, authority.Error{Code: authority.Unavailable}
	}
	// The parent must drop its copy of the child's endpoint immediately: keeping
	// it through the handshake hides a refused/exited child as a silent peer.
	_ = child.Close()
	_ = registry.Close()
	_ = listenFile.Close()
	observationDone := make(chan struct{})
	observationReady := make(chan struct{})
	// Only one goroutine calls Wait. A cancelled Close never abandons ownership.
	go func() {
		if cmd.Wait() != nil {
			p.exit = authorityLaunchRefusal{phase: authorityExitPhase(cmd.ProcessState.ExitCode())}
		}
		<-observationReady // deadline handoff cannot race a startup reset
		_ = parent.SetReadDeadline(time.Now().Add(time.Second))
		<-observationDone
		_ = parent.Close()
		cleanup()
		close(p.done)
	}()
	go func() {
		select {
		case <-p.done:
			cancel()
			return
		case <-lifetime.Done():
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		budget := time.NewTimer(localauthority.CoordinatorCallBudget)
		defer budget.Stop()
		select {
		case <-p.done:
			return
		case <-budget.C:
			_ = cmd.Process.Kill()
			<-p.done
		}
	}()
	observation, err := preparation.SendObserved(ctx, parent)
	if err != nil {
		close(observationDone)
		close(observationReady)
		cancel()
		_ = cmd.Process.Kill()
		<-p.done
		if p.exit != nil {
			return nil, p.exit
		}
		return nil, err
	}
	if observation != nil {
		keepBootstrap = true
		go func() {
			defer close(observationDone)
			observation.Receive()
		}()
	} else {
		close(observationDone)
	}
	close(observationReady)
	select {
	case <-p.done:
		cancel()
		return nil, authority.Error{Code: authority.Unavailable}
	default:
	}
	ok = true
	return p, nil
}

// Client is the protected internal control handle. It is not serializable
// public endpoint/configuration data and does not start or repair the process.
func (p *AuthorityProcess) Client() *RPCClient {
	if p == nil || p.done == nil {
		return nil
	}
	return p.client
}

func (p *AuthorityProcess) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if p.cancel == nil || p.done == nil {
		return authority.Error{Code: authority.Unavailable}
	}
	p.cancel()
	select {
	case <-p.done:
		return p.exit
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *AuthorityProcess) Wait(ctx context.Context) error {
	if p == nil || p.done == nil {
		return authority.Error{Code: authority.Unavailable}
	}
	select {
	case <-p.done:
		return p.exit
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RunInheritedAuthority is the early native entry. It never calls ordinary
// startup/config, MachineRegistry, credential discovery, remote Manager or Admin.
// Fixed descriptors: 3 registry directory, 4 Unix listener, 5 Unix bootstrap.
func RunInheritedAuthority(ctx context.Context) (status int) {
	if err := authorityInheritedRoles(); err != nil {
		if role, ok := err.(authorityRoleRefusal); ok {
			return int(role)
		}
		return 78
	}
	registry := os.NewFile(3, "authority-registry-role")
	listenFile := os.NewFile(4, "authority-listener-role")
	bootFile := os.NewFile(5, "authority-bootstrap-role")
	defer registry.Close()
	defer listenFile.Close()
	defer bootFile.Close()
	l, err := net.FileListener(listenFile)
	if err != nil {
		return 79
	}
	defer l.Close()
	listener, ok := l.(*net.UnixListener)
	if !ok {
		return 79
	}
	listener.SetUnlinkOnClose(false)
	if _, err = privateAuthoritySocket(listener.Addr().String()); err != nil {
		return 80
	}
	c, err := net.FileConn(bootFile)
	if err != nil {
		return 81
	}
	defer c.Close()
	bootstrap, ok := c.(*net.UnixConn)
	if !ok || sameAuthorityUser(bootstrap) != nil {
		return 81
	}
	lifetime, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer cancel()
	startup, stop := context.WithTimeout(lifetime, localauthority.CoordinatorCallBudget)
	defer stop()
	received, err := localauthority.ReceiveLaunch(startup, registry, bootstrap)
	if err != nil {
		return 82
	}
	defer func() {
		cancel()
		join, release := context.WithTimeout(context.Background(), localauthority.CoordinatorCallBudget)
		defer release()
		status = authorityJoinStatus(status, received.Close(join))
	}()
	core, err := received.Activate(lifetime, startup)
	if err != nil {
		return 83
	}
	server, err := NewAuthorityServer(core)
	if err != nil {
		return 84
	}
	observed, err := received.StartObservations()
	if err != nil {
		return 84
	}
	if !observed {
		_ = bootstrap.Close()
	}
	stop()
	if server.Serve(lifetime, listener) != nil {
		return 85
	}
	return 0
}

type authorityLaunchRefusal struct{ phase string }

type authorityRoleRefusal int

func (e authorityRoleRefusal) Error() string { return "local authority inherited role refused" }
func (e authorityRoleRefusal) Unwrap() error { return authority.Error{Code: authority.PolicyDenied} }

func (e authorityLaunchRefusal) Error() string {
	return "local authority runtime refused during " + e.phase
}
func (e authorityLaunchRefusal) Unwrap() error { return authority.Error{Code: authority.Unavailable} }
func authorityExitPhase(code int) string {
	switch code {
	case 78:
		return "inherited-role admission"
	case 79:
		return "listener admission"
	case 80:
		return "private socket validation"
	case 81:
		return "bootstrap peer admission"
	case 82:
		return "preparation validation"
	case 83:
		return "writer activation"
	case 84:
		return "runtime capability admission"
	case 85:
		return "runtime serving"
	case 86:
		return "descriptor inventory"
	case 87:
		return "unexpected inherited descriptor"
	case 88:
		return "descriptor type inspection"
	case 89:
		return "inert stdio admission"
	case 90:
		return "registry role admission"
	case 91:
		return "socket role admission"
	case 92:
		return "Unix socket family/type admission"
	case 93:
		return "kernel peer admission"
	case 94:
		return "Unix socket address admission"
	case 95:
		return "stream socket type admission"
	case 96:
		return "listener/connected role admission"
	case 98:
		return "owned shutdown join"
	default:
		return "process lifetime"
	}
}

func authorityJoinStatus(status int, err error) int {
	if status == 0 && err != nil {
		return 98
	}
	return status
}
