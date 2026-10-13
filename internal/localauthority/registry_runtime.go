package localauthority

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"gitcode-mcp/internal/authority"
)

// PreparedRegistry is an opaque trusted-setup capability, not a caller path,
// FD assertion, serialized config or permission for a second project writer.
// Cross-process launch/protocol integration must preserve this origin boundary.
type PreparedRegistry struct {
	mu        sync.Mutex
	registry  *Registry
	directory *os.File
	identity  string
	writers   int
	closed    bool
	scope     map[authority.UUID]launchProject // non-nil only for transferred immutable cohort
}

type runtimeRegistry struct {
	directory *os.File
	identity  string
}

func (r runtimeRegistry) verify(root string) error {
	if r.directory == nil || r.identity == "" {
		return authority.Error{Code: authority.InitializationRequired}
	}
	retained, err := physicalIdentity(r.directory)
	if err != nil || retained != r.identity {
		return Error{DurabilityUnavailable}
	}
	current, err := openPrivateDirectory(root)
	if err != nil {
		return authority.Error{Code: authority.Conflict}
	}
	defer current.Close()
	identity, err := physicalIdentity(current)
	if err != nil || identity != retained {
		return authority.Error{Code: authority.Conflict}
	}
	return nil
}

// PrepareRuntime performs no initialization or account/environment discovery.
// Only the existing trusted Registry constructor can supply its private root.
func (r *Registry) PrepareRuntime() (*PreparedRegistry, error) {
	if r == nil || r.root == "" || r.runtime != nil {
		return nil, authority.Error{Code: authority.InitializationRequired}
	}
	root, err := privateDirectory(r.root)
	if err != nil || root != r.root {
		return nil, Error{DurabilityUnavailable}
	}
	if err = qualifyFilesystem(root); err != nil {
		return nil, err
	}
	directory, err := openPrivateDirectory(root)
	if err != nil {
		return nil, err
	}
	identity, err := physicalIdentity(directory)
	if err != nil {
		_ = directory.Close()
		return nil, err
	}
	p := &PreparedRegistry{registry: r, directory: directory, identity: identity}
	if err = (runtimeRegistry{directory: directory, identity: identity}).verify(root); err != nil {
		_ = directory.Close()
		return nil, err
	}
	return p, nil
}

// OpenWriter is trusted coordinator admission, never a public path/FD tool.
// It reuses only an existing full binding. No mkdir, owner-file creation,
// partial binding upgrade, account lookup, schema migration or init fallback.
func (p *PreparedRegistry) OpenWriter(ctx context.Context, directory string, project authority.UUID) (*Store, error) {
	if p == nil || !project.Valid() {
		return nil, authority.Error{Code: authority.InitializationRequired}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !p.mu.TryLock() {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, authority.Error{Code: authority.Conflict}
	}
	defer p.mu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if p.closed || p.registry == nil || p.directory == nil {
		return nil, authority.Error{Code: authority.InitializationRequired}
	}
	if p.scope != nil {
		expected, ok := p.scope[project]
		if !ok || expected.Directory != directory {
			return nil, authority.Error{Code: authority.NoAuthority}
		}
		actual, err := inspectLaunchProject(ctx, p.directory, ProjectBinding{project, directory})
		if err != nil {
			return nil, err
		}
		if actual != expected {
			return nil, authority.Error{Code: authority.Conflict}
		}
	}
	runtime := &runtimeRegistry{directory: p.directory, identity: p.identity}
	if err := runtime.verify(p.registry.root); err != nil {
		return nil, err
	}
	root, err := privateDirectory(directory)
	if err != nil {
		return nil, err
	}
	// Never flock tracker.db: on Darwin that conflicts with SQLite's own
	// locking. The private authority directory supplies an independent physical
	// lifetime guard while the owner record still fences the global UUID.
	guard, err := openPrivateDirectory(root)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = guard.Close()
		}
	}()
	if err = lockExclusive(guard); err != nil {
		return nil, err
	}
	directoryIdentity, err := physicalIdentity(guard)
	if err != nil {
		return nil, err
	}
	database, err := openExistingFile(filepath.Join(root, "tracker.db"))
	if err != nil {
		return nil, err
	}
	physical, err := physicalIdentity(database)
	_ = database.Close()
	if err != nil {
		return nil, err
	}
	if expected, scoped := p.scope[project]; scoped && (expected.Physical != directoryIdentity || expected.Database != physical) {
		return nil, authority.Error{Code: authority.Conflict}
	}
	registry := &Registry{root: p.registry.root, runtime: runtime}
	s, err := OpenWriter(ctx, registry, root, project)
	if err != nil {
		return nil, err
	}
	if expected, scoped := p.scope[project]; scoped && (expected.Instance != s.instance || expected.Manifest != manifestHash(s.manifest)) {
		_ = s.Close()
		return nil, authority.Error{Code: authority.Conflict}
	}
	verify := func() error {
		if err := runtime.verify(registry.root); err != nil {
			return err
		}
		currentDirectory, err := openPrivateDirectory(root)
		if err != nil {
			return err
		}
		identity, err := physicalIdentity(currentDirectory)
		_ = currentDirectory.Close()
		if err != nil || identity != directoryIdentity {
			return authority.Error{Code: authority.Conflict}
		}
		current, err := openExistingFile(filepath.Join(root, "tracker.db"))
		if err != nil {
			return err
		}
		defer current.Close()
		identity, err = physicalIdentity(current)
		if err != nil || identity != physical {
			return authority.Error{Code: authority.Conflict}
		}
		return nil
	}
	if err = verify(); err != nil {
		_ = s.Close()
		return nil, err
	}
	p.writers++
	s.lease.runtimeGuard = verify
	s.lease.onClose = func() error {
		var err error
		if guard.Close() != nil {
			err = Error{DurabilityUnavailable}
		}
		p.mu.Lock()
		p.writers--
		p.mu.Unlock()
		return err
	}
	ok = true
	return s, nil
}

// Close never drops the directory capability while a store still owns it.
// The coordinator must cancel/join work, close stores, then close preparation.
func (p *PreparedRegistry) Close() error {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	if p.writers != 0 {
		return authority.Error{Code: authority.Conflict}
	}
	p.closed = true
	if p.directory != nil && p.directory.Close() != nil {
		return Error{DurabilityUnavailable}
	}
	p.directory = nil
	return nil
}
