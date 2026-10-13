//go:build darwin

package servicectl

import "golang.org/x/sys/unix"

func authorityPeerUID(fd int) (uint32, error) {
	cred, err := unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	if err != nil {
		return 0, err
	}
	if cred.Version != 0 {
		return 0, unix.EINVAL
	}
	return cred.Uid, nil
}
