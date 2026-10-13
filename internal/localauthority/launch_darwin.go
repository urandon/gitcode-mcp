//go:build darwin

package localauthority

import "golang.org/x/sys/unix"

func launchPeerUID(fd int) (uint32, error) {
	c, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return 0, err
	}
	if c.Version != 0 {
		return 0, unix.EINVAL
	}
	return c.Uid, nil
}
