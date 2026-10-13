package localauthority

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"

	"gitcode-mcp/internal/authority"
)

// Registry is a trusted machine-global ownership boundary, not a project or
// daemon-profile setting. There is no exported constructor with an arbitrary
// root. Tests inject one isolated machine root through the private constructor.
type Registry struct {
	mu        sync.Mutex
	root      string
	uid       int
	physical  string
	directory *os.File
}

func MachineRegistry() (*Registry, error) {
	if os.Getuid() != os.Geteuid() {
		return nil, authority.Error{Code: authority.PolicyDenied}
	}
	u, err := user.Current()
	if err != nil || !filepath.IsAbs(u.HomeDir) {
		return nil, Error{DurabilityUnavailable}
	}
	root := filepath.Join(u.HomeDir, ".gitcode-mcp", "authority-owners")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, Error{DurabilityUnavailable}
	}
	return newRegistry(root)
}

func newRegistry(root string) (*Registry, error) {
	path, err := privateDirectory(root)
	if err != nil {
		return nil, err
	}
	f, err := openQualifiedDirectory(path)
	if err != nil {
		return nil, Error{DurabilityUnavailable}
	}
	physical, err := physicalIdentity(f)
	if err == nil {
		err = syncRegistryDirectory(f)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return &Registry{root: path, uid: os.Geteuid(), physical: physical, directory: f}, nil
}

// Close ends admission through this prepared capability. Existing leases own
// independent directory descriptors and retain their lock/durability boundary.
func (r *Registry) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.directory == nil {
		return nil
	}
	f := r.directory
	r.directory = nil
	if f.Close() != nil {
		return Error{DurabilityUnavailable}
	}
	return nil
}

// Each operation is relative to the retained directory, never the pathname.
// Holding mu also prevents Close from recycling its descriptor during a syscall.
func (r *Registry) withDirectory(operation func(*os.File) error) error {
	if r == nil {
		return authority.Error{Code: authority.PolicyDenied}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.directory == nil || r.uid != os.Geteuid() || os.Getuid() != os.Geteuid() {
		return authority.Error{Code: authority.PolicyDenied}
	}
	if qualifyDirectoryFD(r.directory) != nil {
		return Error{Corrupt}
	}
	return operation(r.directory)
}

func (r *Registry) exists(name string) (exists bool, err error) {
	err = r.withDirectory(func(f *os.File) error { var e error; exists, e = registryExists(f, name); return e })
	return
}

func (r *Registry) open(name string, create, exclusive bool, maxLinks uint64) (file *os.File, err error) {
	err = r.withDirectory(func(f *os.File) error {
		var e error
		file, e = registryOpen(f, name, create, exclusive, maxLinks)
		return e
	})
	return
}

func (r *Registry) link(old, next string) error {
	return r.withDirectory(func(f *os.File) error { return registryLink(f, old, next) })
}

func (r *Registry) unlink(name string) error {
	return r.withDirectory(func(f *os.File) error { return registryUnlink(f, name) })
}

func (r *Registry) sync() error { return r.withDirectory(syncRegistryDirectory) }

// checkCapability performs only local qualified filesystem inspection. It
// neither resolves an account/home nor creates a registry or chooses a root.
func (r *Registry) checkCapability() error {
	if r == nil || r.root == "" || r.uid != os.Geteuid() || os.Getuid() != os.Geteuid() || !validDigest(r.physical) {
		return authority.Error{Code: authority.PolicyDenied}
	}
	return r.withDirectory(func(held *os.File) error {
		if qualifyDirectoryFD(held) != nil {
			return Error{Corrupt}
		}
		identity, err := physicalIdentity(held)
		if err != nil || identity != r.physical {
			return Error{Corrupt}
		}
		// Path revalidation is only an admission fence. It never supplies the
		// descriptor used for owner or initialization writes.
		path, err := privateDirectory(r.root)
		if err != nil || path != r.root {
			return Error{Corrupt}
		}
		f, err := openQualifiedDirectory(path)
		if err != nil {
			return Error{Corrupt}
		}
		defer f.Close()
		physical, err := physicalIdentity(f)
		if err != nil || physical != r.physical {
			return Error{Corrupt}
		}
		return nil
	})
}

type lease struct {
	file     *os.File
	binding  string
	registry *os.File
}

// The binding is never removed on release. A clone or relocated authority
// requires an explicit identity-preserving restore/rebind contract (L7).
func (r *Registry) acquire(ctx context.Context, id, instance authority.UUID, directory string) (*lease, error) {
	if r == nil || !id.Valid() || !instance.Valid() {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	l, err := r.lockProject(ctx, id)
	if err != nil {
		return nil, err
	}
	f := l.file
	success := false
	defer func() {
		if !success {
			_ = l.close()
		}
	}()
	if exists, err := registryExists(l.registry, initName(id)); err != nil {
		return nil, err
	} else if exists {
		return nil, Error{InitializationPending}
	}
	digest := sha256.Sum256([]byte(directory))
	want := string(id) + "\n" + hex.EncodeToString(digest[:]) + "\n" + string(instance) + "\n"
	l.binding = want
	info, err := f.Stat()
	if err != nil || info.Size() > 256 {
		return nil, Error{Corrupt}
	}
	if info.Size() != 0 {
		body := make([]byte, info.Size())
		if _, err := f.ReadAt(body, 0); err != nil || string(body) != want && !(strings.HasPrefix(string(body), want) && len(body) == len(want)+65 && body[len(body)-1] == '\n') {
			return nil, authority.Error{Code: authority.Conflict}
		}
	} else {
		if _, err = f.WriteAt([]byte(want), 0); err != nil {
			return nil, Error{DurabilityUnavailable}
		}
		if err = syncFile(f); err != nil {
			return nil, err
		}
		if err = syncRegistryDirectory(l.registry); err != nil {
			return nil, err
		}
	}
	success = true
	return l, nil
}

// An unpublished lock is not an identity reservation. Only the initialization
// publication protocol may turn it into a durable final binding.
func (r *Registry) lockProject(ctx context.Context, id authority.UUID) (*lease, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if r == nil || !id.Valid() {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	var directory *os.File
	err := r.withDirectory(func(f *os.File) error { var e error; directory, e = duplicateRegistryDirectory(f); return e })
	if err != nil {
		return nil, err
	}
	f, err := registryOpen(directory, string(id)+".owner", true, false, 1)
	if err != nil {
		_ = directory.Close()
		return nil, err
	}
	if err = lockExclusive(f); err != nil {
		_ = f.Close()
		_ = directory.Close()
		return nil, err
	}
	return &lease{file: f, registry: directory}, nil
}

func (l *lease) bindPhysical(path string) error {
	f, err := openExistingFile(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fingerprint, err := physicalIdentity(f)
	if err != nil {
		return err
	}
	want := l.binding + fingerprint + "\n"
	info, err := l.file.Stat()
	if err != nil || info.Size() > 256 {
		return Error{Corrupt}
	}
	body := make([]byte, info.Size())
	if _, err = l.file.ReadAt(body, 0); err != nil {
		return Error{Corrupt}
	}
	if string(body) == want {
		return nil
	}
	if string(body) != l.binding {
		return authority.Error{Code: authority.Conflict}
	}
	if _, err = l.file.WriteAt([]byte(want), 0); err != nil {
		return Error{DurabilityUnavailable}
	}
	if err = syncFile(l.file); err != nil {
		return err
	}
	return syncRegistryDirectory(l.registry)
}

func (l *lease) close() error {
	if l == nil || l.file == nil {
		return nil
	}
	f := l.file
	l.file = nil
	// Closing releases the kernel lease. Never truncate, unlink, or replace its
	// identity while another process could be waiting on the same inode.
	err := f.Close()
	if l.registry != nil {
		if e := l.registry.Close(); e != nil {
			err = e
		}
		l.registry = nil
	}
	if err != nil {
		return Error{DurabilityUnavailable}
	}
	return nil
}

func privateDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return "", authority.Error{Code: authority.InvalidContract}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return "", Error{DurabilityUnavailable}
	}
	physical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", Error{DurabilityUnavailable}
	}
	if err := directoryOwner(physical); err != nil {
		return "", err
	}
	return physical, nil
}

func syncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return Error{DurabilityUnavailable}
	}
	defer f.Close()
	if err = f.Sync(); err != nil {
		return Error{DurabilityUnavailable}
	}
	return nil
}
