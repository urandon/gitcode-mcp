package localauthority

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/privateipc"
)

// RuntimeProject is trusted machine-local enrollment, never a public IPC DTO.
// Its directory is resolved by explicit CLI setup; runtime opens, not creates.
type RuntimeProject struct {
	Manifest     authority.Manifest     `json:"-"`
	Registration authority.Registration `json:"-"`
	Directory    string                 `json:"-"`
}

type RuntimeOptions struct {
	Registry     *Registry        `json:"-"`
	Directory    string           `json:"-"`
	NetworkState string           `json:"-"`
	Projects     []RuntimeProject `json:"-"`
}

type Runtime struct {
	mu         sync.RWMutex
	options    RuntimeOptions
	started    bool
	serving    bool
	runContext context.Context
	operations sync.WaitGroup
	calls      chan struct{}
	projects   map[authority.UUID]*runtimeProject
	registry   *Registry
}

// NewRuntime is pure admission validation, before environment/config/factories
// or writable opening. Neither a path nor a cache binding can select authority.
func NewRuntime(options RuntimeOptions) (*Runtime, error) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return nil, authority.Error{Code: authority.Unsupported}
	}
	if !filepath.IsAbs(options.Directory) || filepath.Clean(options.Directory) != options.Directory || len(options.Directory) > 4096 || len(options.Projects) < 1 || len(options.Projects) > 16 {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	// Loopback provider/Admin capabilities are not manufactured from policy.
	// Their actual factories are a following L4/L6 integration increment.
	if options.NetworkState != "zero_network" {
		return nil, authority.Error{Code: authority.Unsupported}
	}
	if options.Registry == nil {
		return nil, authority.Error{Code: authority.InitializationRequired}
	}
	if options.Registry.uid != os.Geteuid() || os.Getuid() != os.Geteuid() || options.Registry.root == "" || !validDigest(options.Registry.physical) {
		return nil, authority.Error{Code: authority.PolicyDenied}
	}
	if runtimePathOverlap(options.Directory, options.Registry.root) {
		return nil, authority.Error{Code: authority.Conflict}
	}
	profile := authority.RuntimeProfile{Protocol: authority.Protocol, SchemaMin: 1, SchemaMax: 1, Strict: true, PrivateIPC: true}
	ids := map[authority.UUID]bool{}
	refs := map[string]bool{}
	paths := map[string]bool{}
	copyOptions := options
	copyOptions.Projects = make([]RuntimeProject, len(options.Projects))
	for i, entry := range options.Projects {
		if !entry.Manifest.ID.Valid() || entry.Registration.Validate(entry.Manifest) != nil || !filepath.IsAbs(entry.Directory) || filepath.Clean(entry.Directory) != entry.Directory || len(entry.Directory) > 4096 {
			return nil, authority.Error{Code: authority.InvalidContract}
		}
		if err := authority.Preflight(entry.Manifest.Network, profile); err != nil {
			return nil, err
		}
		if ids[entry.Manifest.ID] || refs[entry.Registration.StorageRef] || paths[entry.Directory] {
			return nil, authority.Error{Code: authority.Conflict}
		}
		if runtimePathOverlap(options.Directory, entry.Directory) || runtimePathOverlap(options.Registry.root, entry.Directory) {
			return nil, authority.Error{Code: authority.Conflict}
		}
		for other := range paths {
			if runtimePathOverlap(other, entry.Directory) {
				return nil, authority.Error{Code: authority.Conflict}
			}
		}
		ids[entry.Manifest.ID] = true
		refs[entry.Registration.StorageRef] = true
		paths[entry.Directory] = true
		entry.Manifest.Entities = append([]authority.EntityKind(nil), entry.Manifest.Entities...)
		copyOptions.Projects[i] = entry
	}
	return &Runtime{options: copyOptions, registry: options.Registry, calls: make(chan struct{}, 32)}, nil
}

// Conservatively refuse nesting/case aliases before any filesystem effects.
// Authority/Git and global ownership namespaces must never host IPC files.
func runtimePathOverlap(left, right string) bool {
	left = strings.ToLower(filepath.Clean(left))
	right = strings.ToLower(filepath.Clean(right))
	return left == right || strings.HasPrefix(left, right+string(filepath.Separator)) || strings.HasPrefix(right, left+string(filepath.Separator))
}

type runtimeDirectory struct {
	path     string
	physical string
	file     *os.File
}

type runtimeNamespaces struct{ directories []runtimeDirectory }

func (n *runtimeNamespaces) close() {
	for _, entry := range n.directories {
		_ = entry.file.Close()
	}
}

// Qualify every namespace before acquiring even the first writer. Lexical
// validation alone cannot see ancestor symlinks, case aliases or bind mounts.
func (r *Runtime) qualifyNamespaces() (*runtimeNamespaces, error) {
	n := &runtimeNamespaces{}
	success := false
	defer func() {
		if !success {
			n.close()
		}
	}()
	paths := []string{r.options.Directory, r.registry.root}
	for _, entry := range r.options.Projects {
		paths = append(paths, entry.Directory)
	}
	for _, path := range paths {
		canonical, err := privateDirectory(path)
		if err != nil {
			return nil, err
		}
		f, err := openQualifiedDirectory(canonical)
		if err != nil {
			return nil, err
		}
		physical, err := physicalIdentity(f)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
		for _, other := range n.directories {
			if physical == other.physical || runtimePathOverlap(canonical, other.path) {
				_ = f.Close()
				return nil, authority.Error{Code: authority.Conflict}
			}
		}
		n.directories = append(n.directories, runtimeDirectory{path: canonical, physical: physical, file: f})
	}
	if n.directories[1].physical != r.registry.physical {
		return nil, Error{Corrupt}
	}
	success = true
	return n, nil
}

func (n *runtimeNamespaces) revalidate() error {
	for _, entry := range n.directories {
		if qualifyDirectoryFD(entry.file) != nil {
			return Error{Corrupt}
		}
		f, err := openQualifiedDirectory(entry.path)
		if err != nil {
			return Error{Corrupt}
		}
		physical, err := physicalIdentity(f)
		_ = f.Close()
		if err != nil || physical != entry.physical {
			return Error{Corrupt}
		}
	}
	return nil
}

// Serve owns private IPC and project writers together. Handler receives only
// qualified same-user connections, not a listener/factory it can substitute.
// Cancellation joins every handler before closing stores or singleton leases.
// No implicit tracker/Git setup, migration, remote sync or provider work occurs.
func (r *Runtime) Serve(ctx context.Context, handler func(context.Context, net.Conn)) (result error) {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if r == nil || handler == nil {
		return authority.Error{Code: authority.InvalidContract}
	}
	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return authority.Error{Code: authority.Conflict}
	}
	r.started = true
	r.mu.Unlock()
	registry := r.registry
	if registry == nil {
		return authority.Error{Code: authority.InitializationRequired}
	}
	if err := registry.checkCapability(); err != nil {
		return err
	}
	namespaces, err := r.qualifyNamespaces()
	if err != nil {
		return err
	}
	defer namespaces.close()
	projects := make(map[authority.UUID]*Store, len(r.options.Projects))
	defer func() {
		for _, store := range projects {
			if err := store.Close(); err != nil {
				result = errors.Join(result, err)
			}
		}
	}()
	for i, entry := range r.options.Projects {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := namespaces.revalidate(); err != nil {
			return err
		}
		store, err := OpenWriter(ctx, registry, namespaces.directories[i+2].path, entry.Manifest.ID)
		if err != nil {
			return err
		}
		projects[entry.Manifest.ID] = store
		if manifestHash(store.manifest) != manifestHash(entry.Manifest) {
			return authority.Error{Code: authority.Conflict}
		}
	}
	if err := registry.checkCapability(); err != nil {
		return err
	}
	if err := namespaces.revalidate(); err != nil {
		return err
	}
	listener, err := privateipc.Listen(ctx, namespaces.directories[0].path)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		if err := listener.Release(); err != nil {
			result = errors.Join(result, err)
		}
	}()
	runCtx, cancel := context.WithCancel(ctx)
	var handlers sync.WaitGroup
	r.mu.Lock()
	r.projects = make(map[authority.UUID]*runtimeProject, len(projects))
	for id, store := range projects {
		lane := make(chan struct{}, 1)
		lane <- struct{}{}
		r.projects[id] = &runtimeProject{store: store, lane: lane}
	}
	r.serving = true
	r.runContext = runCtx
	r.mu.Unlock()
	stopDone := make(chan struct{})
	stop := context.AfterFunc(runCtx, func() { _ = listener.Close(); close(stopDone) })
	defer func() {
		cancel()
		_ = listener.Close()
		if !stop() {
			<-stopDone
		}
		r.mu.Lock()
		r.serving = false
		r.mu.Unlock()
		handlers.Wait()
		r.operations.Wait()
		r.mu.Lock()
		r.projects = nil
		r.runContext = nil
		r.mu.Unlock()
	}()
	slots := make(chan struct{}, 32)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if runCtx.Err() != nil {
				return runCtx.Err()
			}
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			_ = conn.Close()
			continue
		}
		handlers.Add(1)
		go func() {
			defer handlers.Done()
			defer func() { <-slots }()
			defer conn.Close()
			closed := make(chan struct{})
			stopConn := context.AfterFunc(runCtx, func() { _ = conn.Close(); close(closed) })
			defer func() {
				if !stopConn() {
					<-closed
				}
			}()
			handler(runCtx, conn)
		}()
	}
}
