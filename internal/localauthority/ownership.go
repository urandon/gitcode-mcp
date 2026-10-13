package localauthority

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/user"
	"path/filepath"
	"strings"

	"gitcode-mcp/internal/authority"
)

// Registry is a trusted machine-global ownership boundary, not a project or
// daemon-profile setting. There is no exported constructor with an arbitrary
// root. Tests inject one isolated machine root through the private constructor.
type Registry struct {
	root     string
	uid      int
	physical string
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
	if err := qualifyFilesystem(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, Error{DurabilityUnavailable}
	}
	physical, err := physicalIdentity(f)
	_ = f.Close()
	if err != nil {
		return nil, err
	}
	return &Registry{root: path, uid: os.Geteuid(), physical: physical}, nil
}

// checkCapability performs only local qualified filesystem inspection. It
// neither resolves an account/home nor creates a registry or chooses a root.
func (r *Registry) checkCapability() error {
	if r == nil || r.root == "" || r.uid != os.Geteuid() || os.Getuid() != os.Geteuid() || !validDigest(r.physical) {
		return authority.Error{Code: authority.PolicyDenied}
	}
	path, err := privateDirectory(r.root)
	if err != nil || path != r.root || qualifyFilesystem(path) != nil {
		return Error{Corrupt}
	}
	f, err := os.Open(path)
	if err != nil {
		return Error{Corrupt}
	}
	physical, err := physicalIdentity(f)
	_ = f.Close()
	if err != nil || physical != r.physical {
		return Error{Corrupt}
	}
	return nil
}

type lease struct {
	file     *os.File
	binding  string
	registry string
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
	if _, err := os.Lstat(filepath.Join(r.root, string(id)+".init")); err == nil {
		return nil, Error{InitializationPending}
	} else if !os.IsNotExist(err) {
		return nil, Error{Corrupt}
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
		if err = syncDirectory(r.root); err != nil {
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
	f, err := openPrivateFile(filepath.Join(r.root, string(id)+".owner"))
	if err != nil {
		return nil, err
	}
	if err = lockExclusive(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return &lease{file: f, registry: r.root}, nil
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
	return syncDirectory(l.registry)
}

func (l *lease) close() error {
	if l == nil || l.file == nil {
		return nil
	}
	f := l.file
	l.file = nil
	// Closing releases the kernel lease. Never truncate, unlink, or replace its
	// identity while another process could be waiting on the same inode.
	if err := f.Close(); err != nil {
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
