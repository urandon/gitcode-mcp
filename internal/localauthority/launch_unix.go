//go:build darwin || linux

package localauthority

import (
	"net"
	"os"

	"gitcode-mcp/internal/authority"
	"golang.org/x/sys/unix"
)

func validateLaunchDirectory(f *os.File) error {
	if f == nil {
		return authority.Error{Code: authority.InitializationRequired}
	}
	var stat unix.Stat_t
	if unix.Fstat(int(f.Fd()), &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0077 != 0 || stat.Uid != uint32(os.Geteuid()) {
		return authority.Error{Code: authority.PolicyDenied}
	}
	return nil
}

func duplicateLaunchDirectory(f *os.File) (*os.File, error) {
	if err := validateLaunchDirectory(f); err != nil {
		return nil, err
	}
	fd, err := unix.FcntlInt(f.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, Error{DurabilityUnavailable}
	}
	return os.NewFile(uintptr(fd), "authority-registry-role"), nil
}

func sameLaunchUser(conn *net.UnixConn) error {
	raw, err := conn.SyscallConn()
	if err != nil {
		return authority.Error{Code: authority.PolicyDenied}
	}
	var refused bool
	if raw.Control(func(fd uintptr) {
		kind, e := unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_TYPE)
		local, le := unix.Getsockname(int(fd))
		peer, pe := unix.Getpeername(int(fd))
		uid, ue := launchPeerUID(int(fd))
		_, localUnix := local.(*unix.SockaddrUnix)
		_, peerUnix := peer.(*unix.SockaddrUnix)
		refused = e != nil || le != nil || pe != nil || ue != nil || kind != unix.SOCK_STREAM || !localUnix || !peerUnix || uid != uint32(os.Geteuid())
	}) != nil || refused {
		return authority.Error{Code: authority.PolicyDenied}
	}
	return nil
}
