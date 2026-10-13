//go:build linux

package privateipc

import (
	"gitcode-mcp/internal/authority"
	"golang.org/x/sys/unix"
)

func peerUID(fd uintptr) (uint32, error) {
	cred, err := unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	if err != nil || cred == nil || cred.Pid <= 0 {
		return 0, authority.Error{Code: authority.PolicyDenied}
	}
	return cred.Uid, nil
}
