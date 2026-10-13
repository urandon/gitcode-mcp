// Package privateipc qualifies local control transport primitives. It does not
// select a runtime, enroll an authority or trust an arbitrary passed listener.
package privateipc

import (
	"net"
	"os"

	"gitcode-mcp/internal/authority"
)

// VerifyPeer authenticates the OS tenancy at both sides of a Unix stream.
// Actor attribution is separate and must never replace this kernel evidence.
// Filesystem/socket/singleton qualification remains the listener/dial owner's
// responsibility; this check alone is not production runtime admission.
func VerifyPeer(conn net.Conn) error {
	u, ok := conn.(*net.UnixConn)
	if !ok || u == nil || u.LocalAddr() == nil || u.RemoteAddr() == nil || u.LocalAddr().Network() != "unix" || u.RemoteAddr().Network() != "unix" {
		return authority.Error{Code: authority.PolicyDenied}
	}
	raw, err := u.SyscallConn()
	if err != nil {
		return authority.Error{Code: authority.PolicyDenied}
	}
	var uid uint32
	var peerErr error
	if err := raw.Control(func(fd uintptr) { uid, peerErr = peerUID(fd) }); err != nil {
		return authority.Error{Code: authority.PolicyDenied}
	}
	if peerErr != nil {
		return peerErr // platform adapters return fixed errors only.
	}
	return sameUser(uid, os.Geteuid())
}

func sameUser(peer uint32, effective int) error {
	if effective < 0 || uint64(effective) > uint64(^uint32(0)) || peer != uint32(effective) {
		return authority.Error{Code: authority.PolicyDenied}
	}
	return nil
}
