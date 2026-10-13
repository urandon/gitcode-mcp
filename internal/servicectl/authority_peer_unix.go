//go:build darwin || linux

package servicectl

import (
	"net"
	"os"

	"gitcode-mcp/internal/authority"
	"golang.org/x/sys/unix"
)

func sameAuthorityUser(conn net.Conn) error {
	u, ok := conn.(*net.UnixConn)
	if !ok {
		return authority.Error{Code: authority.PolicyDenied}
	}
	raw, err := u.SyscallConn()
	if err != nil {
		return authority.Error{Code: authority.PolicyDenied}
	}
	uid := uint32(0)
	var peerErr error
	if err = raw.Control(func(fd uintptr) { uid, peerErr = authorityPeerUID(int(fd)) }); err != nil || peerErr != nil || uid != uint32(os.Geteuid()) {
		return authority.Error{Code: authority.PolicyDenied}
	}
	return nil
}

func authorityOwnedPath(path string, socket bool) error {
	var stat unix.Stat_t
	if unix.Lstat(path, &stat) != nil || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0077 != 0 {
		return authority.Error{Code: authority.PolicyDenied}
	}
	want := uint32(unix.S_IFDIR)
	if socket {
		want = unix.S_IFSOCK
	}
	if uint32(stat.Mode)&unix.S_IFMT != want {
		return authority.Error{Code: authority.PolicyDenied}
	}
	return nil
}
