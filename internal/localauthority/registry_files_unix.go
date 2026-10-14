//go:build darwin || linux

package localauthority

import (
	"gitcode-mcp/internal/authority"
	"golang.org/x/sys/unix"
	"os"
)

func openPrivateDirectory(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, Error{DurabilityUnavailable}
	}
	f := os.NewFile(uintptr(fd), "authority-registry")
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0077 != 0 {
		_ = f.Close()
		return nil, Error{DurabilityUnavailable}
	}
	return f, nil
}

func openExistingOwnerAt(directory *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == unix.ENOENT {
		return nil, authority.Error{Code: authority.InitializationRequired}
	}
	if err != nil {
		return nil, Error{DurabilityUnavailable}
	}
	f := os.NewFile(uintptr(fd), "authority-owner")
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0077 != 0 {
		_ = f.Close()
		return nil, Error{DurabilityUnavailable}
	}
	return f, nil
}

func accountHomeOwner(path string) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return Error{DurabilityUnavailable}
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) {
		return Error{DurabilityUnavailable}
	}
	return nil
}
