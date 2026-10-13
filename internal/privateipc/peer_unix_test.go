//go:build darwin || linux

package privateipc

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPeerActualUnixBothDirections(t *testing.T) {
	// A short, canonical OS temporary coordinate is fixture-only. No private
	// coordinate or raw kernel error is reflected in test diagnostics.
	dir, err := os.MkdirTemp("", "local-peer-")
	if err != nil {
		t.Fatal("private peer fixture unavailable")
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil || os.Chmod(path, 0600) != nil {
		if listener != nil {
			_ = listener.Close()
		}
		t.Fatal("private peer listener unavailable")
	}
	defer listener.Close()
	if listener.SetDeadline(time.Now().Add(2*time.Second)) != nil {
		t.Fatal("private listener deadline unavailable")
	}
	accepted := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			accepted <- err
			return
		}
		defer conn.Close()
		accepted <- VerifyPeer(conn)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", path)
	if err != nil {
		_ = listener.Close()
		<-accepted
		t.Fatal("private peer client unavailable")
	}
	clientErr := VerifyPeer(conn)
	_ = conn.Close()
	serverErr := <-accepted
	if clientErr != nil || serverErr != nil {
		t.Fatal("kernel same-user peer qualification failed")
	}
}
