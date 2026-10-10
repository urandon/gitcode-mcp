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
	if err := replacePrivate(dir, filepath.Base(path), data); err != nil {
		return errors.New("service definition replacement incomplete")
	}
	return dir.Sync()
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
func appendPrivate(dir *os.File, name string, data []byte) error {
	f, err := openPrivate(dir, name, unix.O_WRONLY|unix.O_APPEND|unix.O_CREAT)
	if err != nil {
		return err
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || info.Size()+int64(len(data)) > SegmentBytes {
		return errors.New("observation segment exceeds bound")
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	// Crash loss is generation-fenced. Clean shutdown syncs all streams.
	return nil
}
func missing(err error) bool { return errors.Is(err, os.ErrNotExist) }
func openForSync(dir *os.File, name string) (*os.File, error) {
	return openPrivate(dir, name, unix.O_RDONLY)
}
