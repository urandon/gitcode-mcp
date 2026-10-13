//go:build darwin

package privateipc

import (
	"gitcode-mcp/internal/authority"
	"golang.org/x/sys/unix"
)

func peerUID(fd uintptr) (uint32, error) {
	cred, err := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
	// Darwin's xucred version is 0. Unknown future formats are not proof.
	if err != nil || cred == nil || cred.Version != 0 {
		return 0, authority.Error{Code: authority.PolicyDenied}
	}
	return cred.Uid, nil
}
