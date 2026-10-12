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
	return openSecureFileLinks(path, flags, 1)
}

// Only the initialization protocol validates a known two-name publication.
func openInitializationFile(path string) (*os.File, error) {
	return openSecureFileLinks(path, unix.O_RDONLY, 2)
}

func openSecureFileLinks(path string, flags int, maxLinks uint64) (*os.File, error) {
	fd, err := unix.Open(path, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, Error{DurabilityUnavailable}
	}
	f := os.NewFile(uintptr(fd), "authority-owner")
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink < 1 || uint64(stat.Nlink) > maxLinks || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0077 != 0 {
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
