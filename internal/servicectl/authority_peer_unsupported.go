//go:build !darwin && !linux

package servicectl

import (
	"gitcode-mcp/internal/authority"
	"net"
)

func sameAuthorityUser(net.Conn) error      { return authority.Error{Code: authority.PolicyDenied} }
func authorityOwnedPath(string, bool) error { return authority.Error{Code: authority.PolicyDenied} }
