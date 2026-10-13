//go:build !darwin && !linux

package privateipc

import (
	"context"
	"net"

	"gitcode-mcp/internal/authority"
)

type Listener struct{}

func Listen(context.Context, string) (*Listener, error) {
	return nil, authority.Error{Code: authority.Unsupported}
}
func Dial(context.Context, string) (net.Conn, error) {
	return nil, authority.Error{Code: authority.Unsupported}
}
func (*Listener) Accept() (net.Conn, error) { return nil, authority.Error{Code: authority.Unsupported} }
func (*Listener) Addr() net.Addr            { return nil }
func (*Listener) Close() error              { return authority.Error{Code: authority.Unsupported} }
func (*Listener) Release() error            { return authority.Error{Code: authority.Unsupported} }
