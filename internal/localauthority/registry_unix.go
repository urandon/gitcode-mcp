//go:build darwin || linux

package localauthority

import (
	"os"
	"strings"

	"gitcode-mcp/internal/authority"
	"golang.org/x/sys/unix"
)

func openQualifiedDirectory(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, Error{DurabilityUnavailable}
	}
	f := os.NewFile(uintptr(fd), "authority-directory")
	if err := qualifyDirectoryFD(f); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func qualifyDirectoryFD(f *os.File) error {
	var stat unix.Stat_t
	if f == nil || unix.Fstat(int(f.Fd()), &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0077 != 0 || stat.Mode&07000 != 0 {
		return Error{DurabilityUnavailable}
	}
	return qualifyFilesystemFD(f)
}

func duplicateRegistryDirectory(f *os.File) (*os.File, error) {
	if err := qualifyDirectoryFD(f); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(f.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, Error{DurabilityUnavailable}
	}
	copy := os.NewFile(uintptr(fd), "authority-registry-lease")
	if err := qualifyDirectoryFD(copy); err != nil {
		_ = copy.Close()
		return nil, err
	}
	return copy, nil
}

func registryName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 256 && !strings.ContainsAny(name, "/\\\x00")
}

func registryExists(f *os.File, name string) (bool, error) {
	if !registryName(name) {
		return false, authority.Error{Code: authority.InvalidContract}
	}
	var stat unix.Stat_t
	err := unix.Fstatat(int(f.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW)
	if err == unix.ENOENT {
		return false, nil
	}
	if err != nil {
		return false, Error{Corrupt}
	}
	return true, nil
}

func registryOpen(directory *os.File, name string, create, exclusive bool, maxLinks uint64) (*os.File, error) {
	if !registryName(name) {
		return nil, authority.Error{Code: authority.InvalidContract}
	}
	flags := unix.O_RDONLY
	if create {
		flags = unix.O_RDWR | unix.O_CREAT
	}
	if exclusive {
		flags |= unix.O_EXCL
	}
	fd, err := unix.Openat(int(directory.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, Error{DurabilityUnavailable}
	}
	f := os.NewFile(uintptr(fd), "authority-registry-record")
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink < 1 || uint64(stat.Nlink) > maxLinks || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0077 != 0 || stat.Mode&07000 != 0 {
		_ = f.Close()
		return nil, Error{DurabilityUnavailable}
	}
	return f, nil
}

func registryLink(f *os.File, old, next string) error {
	if !registryName(old) || !registryName(next) {
		return authority.Error{Code: authority.InvalidContract}
	}
	if unix.Linkat(int(f.Fd()), old, int(f.Fd()), next, 0) != nil {
		return authority.Error{Code: authority.Conflict}
	}
	return nil
}

func registryUnlink(f *os.File, name string) error {
	if !registryName(name) {
		return authority.Error{Code: authority.InvalidContract}
	}
	if unix.Unlinkat(int(f.Fd()), name, 0) != nil {
		return Error{DurabilityUnavailable}
	}
	return nil
}

func syncRegistryDirectory(f *os.File) error {
	if qualifyDirectoryFD(f) != nil || f.Sync() != nil {
		return Error{DurabilityUnavailable}
	}
	return nil
}
