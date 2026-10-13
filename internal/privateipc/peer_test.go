package privateipc

import (
	"errors"
	"net"
	"strings"
	"testing"

	"gitcode-mcp/internal/authority"
)

func requireDenied(t *testing.T, err error) {
	t.Helper()
	var coded authority.Error
	if !errors.As(err, &coded) || coded.Code != authority.PolicyDenied || strings.Contains(err.Error(), "synthetic-private") {
		t.Fatal("peer refusal was not fixed policy denial")
	}
}

func TestPeerRefusesNonUnixAndForeignIdentity(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	requireDenied(t, VerifyPeer(left))
	requireDenied(t, VerifyPeer(nil))
	var uninitialized *net.UnixConn
	requireDenied(t, VerifyPeer(uninitialized))
	for _, pair := range [][2]int{{1001, 1002}, {0, 1001}, {1001, 0}, {0, -1}} {
		requireDenied(t, sameUser(uint32(pair[0]), pair[1]))
	}
	if sameUser(1001, 1001) != nil || sameUser(0, 0) != nil {
		t.Fatal("same effective UID was refused")
	}
}
