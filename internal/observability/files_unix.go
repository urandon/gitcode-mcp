//go:build darwin || linux

package observability

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"
)

// Walk every component without following symlinks. Only the final directory is
// created. A trusted caller supplies this fixed product path, never an RPC path.
func openDirectory(path string, create bool) (*os.File, error) {
	return openOwnedDirectory(path, create, true)
}
func openOwnedDirectory(path string, create, private bool) (*os.File, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	// Only exact OS-owned macOS aliases are canonicalized. Caller-owned
	// symlinks elsewhere in the walk still fail closed.
	if runtime.GOOS == "darwin" {
		for _, alias := range []string{"/tmp", "/var"} {
			if path == alias || strings.HasPrefix(path, alias+"/") {
				target, e := os.Readlink(alias)
				if e == nil && (target == "/private"+alias || target == "private"+alias) {
					path = "/private" + path
				}
				break
			}
		}
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(filepath.Clean(path), "/"), "/")
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			unix.Close(fd)
			return nil, errors.New("unsafe observation directory")
		}
		last := i == len(parts)-1
		if last && create {
			if err := unix.Mkdirat(fd, part, 0700); err != nil && err != unix.EEXIST {
				unix.Close(fd)
				return nil, err
			}
		}
		next, err := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
	}
	f := os.NewFile(uintptr(fd), "observation-directory")
	var st unix.Stat_t
	mask := uint32(0022)
	if private {
		mask = 0077
	}
	if err := unix.Fstat(fd, &st); err != nil || st.Uid != uint32(os.Getuid()) || uint32(st.Mode)&mask != 0 {
		f.Close()
		return nil, errors.New("observation directory must be private and owned")
	}
	return f, nil
}
func openPrivate(dir *os.File, name string, flags int) (*os.File, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "observation-file")
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Uid != uint32(os.Getuid()) || st.Mode&0077 != 0 || st.Nlink != 1 {
		f.Close()
		return nil, errors.New("observation file must be private, regular and singly linked")
	}
	return f, nil
}
func lockDirectory(dir *os.File) (*os.File, error) {
	f, err := openPrivate(dir, "owner.lock", unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("observation writer already active")
	}
	if info, err := f.Stat(); err != nil || info.Size() != 0 {
		f.Close()
		return nil, errors.New("invalid observation lock")
	}
	return f, nil
}
func ReadDefinition(path string) ([]byte, error) {
	dir, err := openOwnedDirectory(filepath.Dir(path), false, false)
	if err != nil {
		return nil, errors.New("unsafe service definition directory")
	}
	defer dir.Close()
	b, err := readPrivate(dir, filepath.Base(path), 1<<20)
	if err != nil {
		return nil, errors.New("unsafe or unavailable service definition")
	}
	return b, nil
}
func WriteDefinition(path string, data []byte) error {
	if len(data) > 1<<20 {
		return errors.New("service definition exceeds bound")
	}
	dir, err := openOwnedDirectory(filepath.Dir(path), false, false)
	if err != nil {
		return errors.New("unsafe service definition directory")
	}
	defer dir.Close()
	if err := replaceAtomic(dir, filepath.Base(path), ".gitcode-mcp-definition.pending", data, 1<<20); err != nil {
		return errors.New("service definition replacement incomplete")
	}
	return dir.Sync()
}

// The fixed staging leaf is also the writer lock. A failed/short write never
// truncates the current leaf, and a crash leaves only one bounded staging leaf.
func replaceAtomic(dir *os.File, name, staging string, data []byte, cap int) error {
	if len(data) > cap {
		return errors.New("replacement exceeds bound")
	}
	old, err := openPrivate(dir, name, unix.O_RDONLY)
	if err != nil && !missing(err) {
		return err
	}
	if old != nil {
		old.Close()
	}
	f, err := openPrivate(dir, staging, unix.O_RDWR|unix.O_CREAT)
	if err != nil {
		return err
	}
	defer f.Close()
	return publishReplacement(dir, name, staging, f, data, cap)
}

func publishReplacement(dir *os.File, name, staging string, f *os.File, data []byte, cap int) error {
	if len(data) > cap {
		return errors.New("replacement exceeds bound")
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return errors.New("replacement writer active")
	}
	// A contender can acquire a descriptor before staging was renamed into
	// committed state, then lock it after publication. Never truncate it.
	var held, named unix.Stat_t
	if unix.Fstat(int(f.Fd()), &held) != nil || unix.Fstatat(int(dir.Fd()), staging, &named, unix.AT_SYMLINK_NOFOLLOW) != nil || held.Dev != named.Dev || held.Ino != named.Ino || named.Mode&unix.S_IFMT != unix.S_IFREG || held.Nlink != 1 {
		return errors.New("replacement staging identity changed")
	}
	if info, err := f.Stat(); err != nil || info.Size() > int64(cap) {
		return errors.New("unsafe replacement staging file")
	}
	if err := f.Truncate(0); err != nil {
		return err
	}
	if n, err := f.WriteAt(data, 0); err != nil {
		return err
	} else if n != len(data) {
		return io.ErrShortWrite
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := unix.Renameat(int(dir.Fd()), staging, int(dir.Fd()), name); err != nil {
		return err
	}
	return dir.Sync()
}

func trimPrivate(dir *os.File, name string, bytes int) error {
	f, err := openPrivate(dir, name, unix.O_WRONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || bytes < 0 || info.Size() < int64(bytes) || info.Size() > SegmentBytes {
		return errors.New("invalid observation prefix")
	}
	if err := f.Truncate(int64(bytes)); err != nil {
		return err
	}
	return f.Sync()
}
func readPrivate(dir *os.File, name string, cap int64) ([]byte, error) {
	f, err := openPrivate(dir, name, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, cap+1))
	if int64(len(b)) > cap {
		return nil, errors.New("observation file exceeds bound")
	}
	return b, err
}
func replacePrivate(dir *os.File, name string, data []byte) error {
	f, err := openPrivate(dir, name, unix.O_WRONLY|unix.O_CREAT)
	if err != nil {
		return err
	}
	defer f.Close()
	// Do not truncate before validating ownership/type/link count.
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.WriteAt(data, 0); err != nil {
		return err
	}
	return f.Sync()
}
func appendPrivate(dir *os.File, name string, data []byte, expected int) error {
	f, err := openPrivate(dir, name, unix.O_WRONLY|unix.O_APPEND|unix.O_CREAT)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() < int64(expected) || info.Size() > SegmentBytes || expected+len(data) > SegmentBytes {
		return errors.New("observation segment exceeds bound")
	}
	// A failed append may leave a short suffix. Trim only bytes beyond the
	// last successful write before a later bounded attempt; do not replay it.
	if info.Size() != int64(expected) {
		if err := f.Truncate(int64(expected)); err != nil {
			return err
		}
	}
	if n, err := f.Write(data); err != nil {
		return err
	} else if n != len(data) {
		return io.ErrShortWrite
	}
	// Crash loss is generation-fenced. Clean shutdown syncs all streams.
	return nil
}
func missing(err error) bool { return errors.Is(err, os.ErrNotExist) }
func openForSync(dir *os.File, name string) (*os.File, error) {
	return openPrivate(dir, name, unix.O_RDONLY)
}
