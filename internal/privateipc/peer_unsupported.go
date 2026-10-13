//go:build !darwin && !linux

package privateipc

import "gitcode-mcp/internal/authority"

func peerUID(uintptr) (uint32, error) {
	return 0, authority.Error{Code: authority.Unsupported}
}
