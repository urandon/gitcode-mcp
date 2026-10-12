//go:build darwin || linux

package localauthority

import (
	"fmt"
	"gitcode-mcp/internal/authority"
	"golang.org/x/sys/unix"
	"os"
)

func physicalIdentity(f *os.File) (string, error) {
	var stat unix.Stat_t
	if unix.Fstat(int(f.Fd()), &stat) != nil {
		return "", Error{DurabilityUnavailable}
	}
	return hash([]byte(fmt.Sprintf("%d:%d", stat.Dev, stat.Ino))), nil
}

func directoryOwner(path string) error {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return Error{DurabilityUnavailable}
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0077 != 0 {
		return Error{DurabilityUnavailable}
	}
	return nil
}

func openPrivateFile(path string) (*os.File, error) {
	return openSecureFile(path, unix.O_CREAT|unix.O_RDWR)
}

func openExistingFile(path string) (*os.File, error) { return openSecureFile(path, unix.O_RDONLY) }

func openSecureFile(path string, flags int) (*os.File, error) {
	fd, err := unix.Open(path, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, Error{DurabilityUnavailable}
	}
	f := os.NewFile(uintptr(fd), "authority-owner")
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0077 != 0 {
		_ = f.Close()
		return nil, Error{DurabilityUnavailable}
	}
	return f, nil
}

func lockExclusive(f *os.File) error {
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
			return authority.Error{Code: authority.Conflict}
		}
		return Error{DurabilityUnavailable}
	}
	return nil
}
