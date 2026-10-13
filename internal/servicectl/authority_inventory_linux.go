//go:build linux

package servicectl

import (
	"os"
	"strconv"

	"gitcode-mcp/internal/authority"
	"golang.org/x/sys/unix"
)

func authorityDescriptorInventory() ([]int, error) {
	f, err := os.Open("/proc/self/fd")
	if err != nil {
		return nil, authority.Error{Code: authority.PolicyDenied}
	}
	defer f.Close()
	entries, err := f.ReadDir(129)
	if err != nil || len(entries) > 128 {
		return nil, authority.Error{Code: authority.PolicyDenied}
	}
	var out []int
	for _, e := range entries {
		fd, err := strconv.Atoi(e.Name())
		if err != nil {
			return nil, authority.Error{Code: authority.PolicyDenied}
		}
		if fd != int(f.Fd()) {
			out = append(out, fd)
		}
	}
	return out, nil
}

func authorityAccepting(fd int) (bool, error) {
	n, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ACCEPTCONN)
	return n != 0, err
}
