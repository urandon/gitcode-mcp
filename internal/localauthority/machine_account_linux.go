//go:build linux

package localauthority

import (
	"golang.org/x/sys/unix"
	"os"
)

func resolveAccountHome() (string, error) {
	fd, err := unix.Open("/etc/passwd", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", Error{DurabilityUnavailable}
	}
	f := os.NewFile(uintptr(fd), "account-metadata")
	defer f.Close()
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != 0 || stat.Mode&0022 != 0 {
		return "", Error{DurabilityUnavailable}
	}
	return homeFromPasswd(f, os.Geteuid())
}
