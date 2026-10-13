//go:build !darwin && !linux

package localauthority

import (
	"gitcode-mcp/internal/authority"
	"net"
	"os"
)

func validateLaunchDirectory(*os.File) error { return authority.Error{Code: authority.Unsupported} }
func duplicateLaunchDirectory(*os.File) (*os.File, error) {
	return nil, authority.Error{Code: authority.Unsupported}
}
func sameLaunchUser(*net.UnixConn) error { return authority.Error{Code: authority.Unsupported} }
