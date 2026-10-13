//go:build darwin || linux

package localauthority

import (
	"os"
	"strings"

	"gitcode-mcp/internal/authority"
	"golang.org/x/sys/unix"
)

const projectionDirectory = "runtime-projection"

func projectionFileName(name string) bool {
	return name == "catalog.json" || strings.HasPrefix(name, "stage-") && strings.HasSuffix(name, ".json") && authority.UUID(strings.TrimSuffix(strings.TrimPrefix(name, "stage-"), ".json")).Valid()
}

func openProjectionNamespace(root *os.File, create bool) (*os.File, bool, error) {
	created := false
	fd, err := unix.Openat(int(root.Fd()), projectionDirectory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == unix.ENOENT {
		if !create {
			return nil, false, nil
		}
		if err = unix.Mkdirat(int(root.Fd()), projectionDirectory, 0700); err == nil {
			created = true
		} else if err != unix.EEXIST {
			return nil, false, Error{ProjectionCorrupt}
		}
		fd, err = unix.Openat(int(root.Fd()), projectionDirectory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return nil, false, Error{ProjectionCorrupt}
	}
	f := os.NewFile(uintptr(fd), "projection-directory")
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0077 != 0 {
		_ = f.Close()
		return nil, false, Error{ProjectionCorrupt}
	}
	return f, created, nil
}

func openProjectionFile(directory *os.File, name string, exclusive bool) (*os.File, bool, error) {
	if !projectionFileName(name) {
		return nil, false, Error{ProjectionCorrupt}
	}
	flags := unix.O_RDONLY | unix.O_NONBLOCK
	if exclusive {
		flags = unix.O_RDWR | unix.O_CREAT | unix.O_EXCL
	}
	fd, err := unix.Openat(int(directory.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err == unix.ENOENT && !exclusive {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, Error{ProjectionCorrupt}
	}
	f := os.NewFile(uintptr(fd), "projection-metadata")
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0077 != 0 {
		_ = f.Close()
		return nil, false, Error{ProjectionCorrupt}
	}
	return f, true, nil
}

func scanProjectionNamespace(directory *os.File) (*os.File, error) {
	fd, err := unix.Openat(int(directory.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, Error{ProjectionCorrupt}
	}
	return os.NewFile(uintptr(fd), "projection-inventory"), nil
}

func publishProjectionFile(directory *os.File, stage string) error {
	if stage == "catalog.json" || !projectionFileName(stage) {
		return Error{ProjectionCorrupt}
	}
	if unix.Renameat(int(directory.Fd()), stage, int(directory.Fd()), "catalog.json") != nil {
		return Error{ProjectionUncertain}
	}
	return nil
}
