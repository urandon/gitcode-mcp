//go:build !darwin && !linux

package servicectl

import (
	"gitcode-mcp/internal/authority"
	"net"
	"os"
)

func authorityInheritedRoles() error { return authority.Error{Code: authority.Unsupported} }
func authorityBootstrapPair() (*net.UnixConn, *os.File, error) {
	return nil, nil, authority.Error{Code: authority.Unsupported}
}
