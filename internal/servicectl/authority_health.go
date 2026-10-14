package servicectl

import (
	"context"
	"os"
	"path/filepath"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority"
)

// Optional pins belong only to the trusted native handle, never to JSON or
// configuration. They reject accidental endpoint replacement, not hostile
// same-UID filesystem races or protocol impersonation.
func (c *RPCClient) verifyAuthorityEndpoint(address string) error {
	if c.authoritySocketIdentity == nil && c.authorityParentIdentity == nil {
		return nil
	}
	invalid := authority.Error{Code: authority.Unavailable}
	if c.authoritySocketIdentity == nil || c.authorityParentIdentity == nil {
		return invalid
	}
	socket, err := privateAuthoritySocket(address)
	if err != nil || !os.SameFile(c.authoritySocketIdentity, socket) {
		return invalid
	}
	parent, err := os.Lstat(filepath.Dir(address))
	if err != nil || !os.SameFile(c.authorityParentIdentity, parent) {
		return invalid
	}
	return nil
}

// RuntimeHealth uses the original private endpoint and independently retained
// launch epoch. The generic Client handle cannot retarget this health handle.
// Reads neither start a process nor derive an expected epoch from its response.
func (p *AuthorityProcess) RuntimeHealth(ctx context.Context, project authority.UUID) (localauthority.RuntimeHealthView, error) {
	var zero localauthority.RuntimeHealthView
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if p == nil || p.done == nil || p.healthClient == nil || p.healthBinding == "" {
		return zero, authority.Error{Code: authority.Unavailable}
	}
	select {
	case <-p.done:
		return zero, authority.Error{Code: authority.Unavailable}
	default:
	}
	req := localauthority.RuntimeHealthRequest{ProjectID: project, RuntimeBinding: p.healthBinding}
	var view localauthority.RuntimeHealthView
	if err := p.healthClient.Call(ctx, "AuthorityV1.RuntimeHealth", req, &view); err != nil {
		return zero, err
	}
	select {
	case <-p.done:
		return zero, authority.Error{Code: authority.Unavailable}
	default:
	}
	if ctx.Err() != nil {
		return zero, ctx.Err()
	}
	if err := p.healthClient.verifyAuthorityEndpoint(p.healthClient.Address); err != nil {
		return zero, err
	}
	if localauthority.VerifyRuntimeHealth(req, view) != nil {
		return zero, authority.Error{Code: authority.InvalidContract}
	}
	return view, nil
}
