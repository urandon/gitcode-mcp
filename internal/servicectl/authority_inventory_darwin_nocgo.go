//go:build darwin && !cgo

package servicectl

import "gitcode-mcp/internal/authority"

func authorityDescriptorInventory() ([]int, error) {
	return nil, authority.Error{Code: authority.Unsupported}
}

func authorityAccepting(int) (bool, error) {
	return false, authority.Error{Code: authority.Unsupported}
}
