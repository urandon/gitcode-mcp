//go:build linux

package servicectl

import "golang.org/x/sys/unix"

func authorityPeerUID(fd int) (uint32, error) {
	cred, err := unix.GetsockoptUcred(fd, unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil {
		return 0, err
	}
	if cred.Pid <= 0 {
		return 0, unix.EINVAL
	}
	return cred.Uid, nil
}
