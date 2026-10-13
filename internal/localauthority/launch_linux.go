//go:build linux

package localauthority

import "golang.org/x/sys/unix"

func launchPeerUID(fd int) (uint32, error) {
	c, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, err
	}
	if c.Pid <= 0 {
		return 0, unix.EINVAL
	}
	return c.Uid, nil
}
