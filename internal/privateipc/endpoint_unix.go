//go:build darwin || linux

package privateipc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gitcode-mcp/internal/authority"
	"golang.org/x/sys/unix"
)

const socketName = "control.sock"
const ownerName = "runtime.owner"

type inodeIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

type endpointBinding struct {
	Schema    int           `json:"schema"`
	Directory inodeIdentity `json:"directory"`
	Socket    inodeIdentity `json:"socket"`
}

// Listener owns a qualified private directory and singleton lease. Close stops
// admission only; Release also releases the singleton, after all accepted
// connections have closed. Neither operation removes the durable owner file.
type Listener struct {
	mu          sync.Mutex
	listener    *net.UnixListener
	dir         *os.File
	owner       *os.File
	directory   string
	binding     endpointBinding
	ownerID     inodeIdentity
	closed      bool
	released    bool
	connections int
}

func denied() error      { return authority.Error{Code: authority.PolicyDenied} }
func conflict() error    { return authority.Error{Code: authority.Conflict} }
func unavailable() error { return authority.Error{Code: authority.Unavailable} }

func identity(stat unix.Stat_t) inodeIdentity {
	return inodeIdentity{Device: uint64(stat.Dev), Inode: uint64(stat.Ino)}
}

func privateStat(stat unix.Stat_t, kind uint32, mode uint32) bool {
	return uint32(stat.Mode)&unix.S_IFMT == kind && uint32(stat.Mode)&07777 == mode && stat.Uid == uint32(os.Geteuid()) && stat.Nlink >= 1 && (kind == unix.S_IFDIR || stat.Nlink == 1)
}

// Open only an explicitly prepared runtime directory. It is never inferred
// from a cache, an environment override, a socket supplied by an IPC caller,
// or an inherited/passed listener. CLI setup owns creation of this directory.
func openDirectory(path string) (*os.File, inodeIdentity, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || len(path) > 4096 {
		return nil, inodeIdentity{}, denied()
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil || canonical != path {
		return nil, inodeIdentity{}, denied()
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, inodeIdentity{}, denied()
	}
	f := os.NewFile(uintptr(fd), "private-runtime-directory")
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || !privateStat(stat, unix.S_IFDIR, 0700) || !endpointFilesystem(fd) {
		_ = f.Close()
		return nil, inodeIdentity{}, denied()
	}
	return f, identity(stat), nil
}

func statAt(dir *os.File, name string, kind uint32, mode uint32) (inodeIdentity, error) {
	var stat unix.Stat_t
	if unix.Fstatat(int(dir.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW) != nil || !privateStat(stat, kind, mode) {
		return inodeIdentity{}, denied()
	}
	return identity(stat), nil
}

func openOwner(dir *os.File, write bool) (*os.File, inodeIdentity, error) {
	flags := unix.O_RDONLY
	if write {
		flags = unix.O_RDWR | unix.O_CREAT
	}
	fd, err := unix.Openat(int(dir.Fd()), ownerName, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, inodeIdentity{}, denied()
	}
	f := os.NewFile(uintptr(fd), "private-runtime-owner")
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || !privateStat(stat, unix.S_IFREG, 0600) || stat.Size < 0 || stat.Size > 256 {
		_ = f.Close()
		return nil, inodeIdentity{}, denied()
	}
	return f, identity(stat), nil
}

func readBinding(owner *os.File) (endpointBinding, bool, error) {
	var zero endpointBinding
	info, err := owner.Stat()
	if err != nil || info.Size() < 0 || info.Size() > 256 {
		return zero, false, denied()
	}
	if info.Size() == 0 {
		return zero, false, nil
	}
	body := make([]byte, info.Size())
	if _, err := owner.ReadAt(body, 0); err != nil || authority.DecodeJSON(body, &zero) != nil || zero.Schema != 1 || zero.Directory.Inode == 0 || zero.Socket.Inode == 0 {
		return endpointBinding{}, false, denied()
	}
	return zero, true, nil
}

func directoryMatches(path string, dir *os.File, want inodeIdentity) error {
	other, actual, err := openDirectory(path)
	if err != nil {
		return err
	}
	_ = other.Close()
	var held unix.Stat_t
	if actual != want || unix.Fstat(int(dir.Fd()), &held) != nil || identity(held) != want || !privateStat(held, unix.S_IFDIR, 0700) {
		return conflict()
	}
	return nil
}

// Listen never adopts or replaces an unbound socket. A stale socket requires
// the exact retained directory/socket binding and an ECONNREFUSED probe after
// acquiring the singleton. Unknown, live, replaced or insecure bytes survive.
func Listen(ctx context.Context, directory string) (*Listener, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	dir, dirID, err := openDirectory(directory)
	if err != nil {
		return nil, err
	}
	owner, ownerID, err := openOwner(dir, true)
	if err != nil {
		_ = dir.Close()
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = owner.Close()
			_ = dir.Close()
		}
	}()
	if err := unix.Flock(int(owner.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, conflict()
		}
		return nil, unavailable()
	}
	prior, exists, err := readBinding(owner)
	if err != nil {
		return nil, err
	}
	if exists && prior.Directory != dirID {
		return nil, conflict()
	}
	path := filepath.Join(directory, socketName)
	var stat unix.Stat_t
	statErr := unix.Fstatat(int(dir.Fd()), socketName, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if statErr == nil {
		if !exists || !privateStat(stat, unix.S_IFSOCK, 0600) || identity(stat) != prior.Socket {
			return nil, conflict()
		}
		probeCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		var dialer net.Dialer
		conn, probeErr := dialer.DialContext(probeCtx, "unix", path)
		cancel()
		if conn != nil {
			_ = conn.Close()
			return nil, conflict()
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !errors.Is(probeErr, unix.ECONNREFUSED) {
			return nil, conflict()
		}
		if directoryMatches(directory, dir, dirID) != nil {
			return nil, conflict()
		}
		current, err := statAt(dir, socketName, unix.S_IFSOCK, 0600)
		if err != nil || current != prior.Socket {
			return nil, conflict()
		}
		if currentOwner, err := statAt(dir, ownerName, unix.S_IFREG, 0600); err != nil || currentOwner != ownerID {
			return nil, conflict()
		}
		if unix.Unlinkat(int(dir.Fd()), socketName, 0) != nil {
			return nil, unavailable()
		}
	} else if !errors.Is(statErr, unix.ENOENT) {
		return nil, denied()
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if directoryMatches(directory, dir, dirID) != nil {
		return nil, conflict()
	}
	if currentOwner, err := statAt(dir, ownerName, unix.S_IFREG, 0600); err != nil || currentOwner != ownerID {
		return nil, conflict()
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, unavailable()
	}
	l.SetUnlinkOnClose(false)
	// If qualification fails, preserve the new socket as unbound evidence. A
	// subsequent call must not mistake its name or shape for prior ownership.
	defer func() {
		if !ok {
			_ = l.Close()
		}
	}()
	if os.Chmod(path, 0600) != nil || directoryMatches(directory, dir, dirID) != nil {
		return nil, denied()
	}
	socketID, err := statAt(dir, socketName, unix.S_IFSOCK, 0600)
	if err != nil {
		return nil, err
	}
	binding := endpointBinding{Schema: 1, Directory: dirID, Socket: socketID}
	body, err := json.Marshal(binding)
	if err != nil || len(body) > 256 {
		return nil, unavailable()
	}
	if currentOwner, err := statAt(dir, ownerName, unix.S_IFREG, 0600); err != nil || currentOwner != ownerID {
		return nil, conflict()
	}
	if _, err = owner.WriteAt(body, 0); err != nil {
		return nil, unavailable()
	}
	if owner.Truncate(int64(len(body))) != nil || owner.Sync() != nil || dir.Sync() != nil {
		return nil, unavailable()
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	ok = true
	return &Listener{listener: l, dir: dir, owner: owner, directory: directory, binding: binding, ownerID: ownerID}, nil
}

func (l *Listener) qualify() error {
	if directoryMatches(l.directory, l.dir, l.binding.Directory) != nil {
		return conflict()
	}
	if ownerID, err := statAt(l.dir, ownerName, unix.S_IFREG, 0600); err != nil || ownerID != l.ownerID {
		return conflict()
	}
	if socketID, err := statAt(l.dir, socketName, unix.S_IFSOCK, 0600); err != nil || socketID != l.binding.Socket {
		return conflict()
	}
	bound, exists, err := readBinding(l.owner)
	if err != nil || !exists || bound != l.binding {
		return conflict()
	}
	return nil
}

func (l *Listener) Accept() (net.Conn, error) {
	for {
		conn, err := l.listener.AcceptUnix()
		if err != nil {
			return nil, unavailable()
		}
		l.mu.Lock()
		if l.closed || l.released || l.qualify() != nil {
			l.mu.Unlock()
			_ = conn.Close()
			return nil, conflict()
		}
		if VerifyPeer(conn) != nil {
			l.mu.Unlock()
			_ = conn.Close()
			continue
		}
		l.connections++
		l.mu.Unlock()
		return &ownedConn{Conn: conn, owner: l}, nil
	}
}

type ownedConn struct {
	net.Conn
	owner *Listener
	once  sync.Once
}

func (c *ownedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.owner.mu.Lock(); c.owner.connections--; c.owner.mu.Unlock() })
	return err
}

func (l *Listener) Addr() net.Addr { return l.listener.Addr() }

func (l *Listener) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.mu.Unlock()
	if l.listener.Close() != nil {
		return unavailable()
	}
	return nil
}

func (l *Listener) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	if !l.closed || l.connections != 0 {
		return conflict()
	}
	// Even shutdown cannot unlink a replacement or clear unknown ownership.
	// After all connections join, release only our retained descriptors even
	// when namespace qualification failed; preserve suspicious current names.
	var result error
	if l.qualify() != nil {
		result = conflict()
	} else if unix.Unlinkat(int(l.dir.Fd()), socketName, 0) != nil || l.dir.Sync() != nil {
		result = unavailable()
	}
	ownerErr := l.owner.Close()
	if l.dir.Close() != nil || ownerErr != nil {
		result = unavailable()
	}
	l.released = true
	return result
}

// Dial does no filesystem mutation, startup, repair, DNS or TCP. Its trusted
// machine coordinate must still match the listener's retained inode binding.
func Dial(ctx context.Context, directory string) (net.Conn, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	dir, dirID, err := openDirectory(directory)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	owner, ownerID, err := openOwner(dir, false)
	if err != nil {
		return nil, err
	}
	defer owner.Close()
	bound, exists, err := readBinding(owner)
	if err != nil || !exists || bound.Directory != dirID {
		return nil, denied()
	}
	socketID, err := statAt(dir, socketName, unix.S_IFSOCK, 0600)
	if err != nil || socketID != bound.Socket {
		return nil, denied()
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", filepath.Join(directory, socketName))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, unavailable()
	}
	ok := false
	defer func() {
		if !ok {
			_ = conn.Close()
		}
	}()
	if VerifyPeer(conn) != nil || directoryMatches(directory, dir, dirID) != nil {
		return nil, denied()
	}
	if actual, err := statAt(dir, socketName, unix.S_IFSOCK, 0600); err != nil || actual != socketID {
		return nil, conflict()
	}
	if actual, err := statAt(dir, ownerName, unix.S_IFREG, 0600); err != nil || actual != ownerID {
		return nil, conflict()
	}
	final, exists, err := readBinding(owner)
	left, _ := json.Marshal(bound)
	right, _ := json.Marshal(final)
	if err != nil || !exists || !bytes.Equal(left, right) {
		return nil, conflict()
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	ok = true
	return conn, nil
}
