//go:build darwin || linux

package servicectl

import (
	"net"
	"os"

	"gitcode-mcp/internal/authority"
	"golang.org/x/sys/unix"
)

func authorityBootstrapPair() (*net.UnixConn, *os.File, error) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, nil, authority.Error{Code: authority.Unavailable}
	}
	for _, fd := range fds {
		unix.CloseOnExec(fd)
	}
	parent := os.NewFile(uintptr(fds[0]), "authority-parent-bootstrap")
	child := os.NewFile(uintptr(fds[1]), "authority-child-bootstrap")
	conn, err := net.FileConn(parent)
	_ = parent.Close()
	if err != nil {
		child.Close()
		return nil, nil, authority.Error{Code: authority.Unavailable}
	}
	u, ok := conn.(*net.UnixConn)
	if !ok {
		conn.Close()
		child.Close()
		return nil, nil, authority.Error{Code: authority.Unavailable}
	}
	return u, child, nil
}

func authorityInheritedRoles() error {
	fds, err := authorityDescriptorInventory()
	if err != nil {
		return authorityRoleRefusal(86)
	}
	seen := [6]bool{}
	for _, fd := range fds {
		if fd < 0 || fd > 5 || seen[fd] {
			return authorityRoleRefusal(87)
		}
		seen[fd] = true
		var stat unix.Stat_t
		if unix.Fstat(fd, &stat) != nil {
			return authorityRoleRefusal(88)
		}
		kind := stat.Mode & unix.S_IFMT
		switch {
		case fd < 3:
			// Never write a startup diagnostic to inherited INET/Unix sockets.
			if kind != unix.S_IFREG && kind != unix.S_IFIFO && kind != unix.S_IFCHR {
				return authorityRoleRefusal(89)
			}
		case fd == 3:
			if kind != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0077 != 0 {
				return authorityRoleRefusal(90)
			}
		case fd == 4 || fd == 5:
			if kind != unix.S_IFSOCK {
				return authorityRoleRefusal(91)
			}
			address, e := unix.Getsockname(fd)
			_, isUnix := address.(*unix.SockaddrUnix)
			typeID, te := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
			accept, ae := authorityAccepting(fd)
			if e != nil || !isUnix {
				return authorityRoleRefusal(94)
			}
			if te != nil || typeID != unix.SOCK_STREAM {
				return authorityRoleRefusal(95)
			}
			if ae != nil || fd == 4 && !accept || fd == 5 && accept {
				return authorityRoleRefusal(96)
			}
			if fd == 5 {
				peer, pe := unix.Getpeername(fd)
				_, peerUnix := peer.(*unix.SockaddrUnix)
				uid, ue := authorityPeerUID(fd)
				if pe != nil || !peerUnix || ue != nil || uid != uint32(os.Geteuid()) {
					return authorityRoleRefusal(93)
				}
			}
		}
	}
	for _, ok := range seen {
		if !ok {
			return authority.Error{Code: authority.InitializationRequired}
		}
	}
	return nil
}
