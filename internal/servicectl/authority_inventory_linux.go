//go:build linux

package servicectl

import (
	"strconv"

	"gitcode-mcp/internal/authority"
	"golang.org/x/sys/unix"
)

func authorityDescriptorInventory() ([]int, error) {
	// Native census must not initialize Go's netpoller itself. os.Open can
	// initialize epoll/break descriptors before learning a directory is not
	// pollable, turning our own inspection into unexpected startup resources.
	fd, err := unix.Open("/proc/self/fd", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, authority.Error{Code: authority.PolicyDenied}
	}
	defer unix.Close(fd)
	buffer := make([]byte, 16<<10)
	var out []int
	for page := 0; page < 16; page++ {
		n, err := unix.ReadDirent(fd, buffer)
		if err != nil {
			return nil, authority.Error{Code: authority.PolicyDenied}
		}
		if n == 0 {
			return out, nil
		}
		consumed, _, names := unix.ParseDirent(buffer[:n], -1, nil)
		if consumed != n {
			return nil, authority.Error{Code: authority.PolicyDenied}
		}
		for _, name := range names {
			entry, err := strconv.Atoi(name)
			if err != nil {
				return nil, authority.Error{Code: authority.PolicyDenied}
			}
			if entry != fd {
				out = append(out, entry)
			}
			if len(out) > 128 {
				return nil, authority.Error{Code: authority.PolicyDenied}
			}
		}
	}
	return nil, authority.Error{Code: authority.PolicyDenied}
}

func authorityAccepting(fd int) (bool, error) {
	n, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_ACCEPTCONN)
	return n != 0, err
}
