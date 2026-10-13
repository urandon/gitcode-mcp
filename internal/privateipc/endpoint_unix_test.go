//go:build darwin || linux

package privateipc

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
)

func endpointDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "p-")
	if err != nil {
		t.Fatal("private runtime fixture unavailable")
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	canonical, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal("private runtime fixture cannot canonicalize")
	}
	return canonical
}

func requireCode(t *testing.T, err error, code authority.Code) {
	t.Helper()
	var coded authority.Error
	if !errors.As(err, &coded) || coded.Code != code {
		t.Fatal("fixed runtime error identity missing")
	}
}

func releaseFixture(t *testing.T, l *Listener) {
	t.Helper()
	if l.Close() != nil || l.Release() != nil {
		t.Fatal("private listener did not release cleanly")
	}
}

func TestEndpointPrivateRoundTripAndJoinedLease(t *testing.T) {
	dir := endpointDir(t)
	l, err := Listen(context.Background(), dir)
	if err != nil {
		t.Fatal("qualified endpoint unavailable")
	}
	t.Cleanup(func() { _ = l.Close(); _ = l.Release() })
	if socketInfo, err := os.Lstat(filepath.Join(dir, socketName)); err != nil || socketInfo.Mode().Perm() != 0600 {
		t.Fatal("socket was not private")
	}
	if _, err := Listen(context.Background(), dir); err == nil {
		t.Fatal("second singleton admitted")
	} else {
		requireCode(t, err, authority.Conflict)
	}
	accepted := make(chan net.Conn, 1)
	failed := make(chan error, 1)
	go func() {
		conn, err := l.Accept()
		if err != nil {
			failed <- err
			return
		}
		accepted <- conn
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client, err := Dial(ctx, dir)
	if err != nil {
		_ = l.Close()
		select {
		case <-failed:
		case <-time.After(time.Second):
			t.Error("accept fixture not joined")
		}
		t.Fatal("qualified client unavailable")
	}
	defer client.Close()
	var server net.Conn
	select {
	case server = <-accepted:
	case <-failed:
		t.Fatal("private accept failed")
	case <-ctx.Done():
		t.Fatal("private accept did not finish")
	}
	if l.Close() != nil {
		t.Fatal("admission did not close")
	}
	requireCode(t, l.Release(), authority.Conflict)
	if _, err := Listen(context.Background(), dir); err == nil {
		t.Fatal("lease released before accepted connection joined")
	} else {
		requireCode(t, err, authority.Conflict)
	}
	_ = server.Close()
	_ = server.Close() // accounting is one-shot, not a negative count.
	if l.Release() != nil {
		t.Fatal("joined endpoint lease not released")
	}
	if _, err := os.Lstat(filepath.Join(dir, ownerName)); err != nil {
		t.Fatal("durable owner binding was removed")
	}
	if _, err := os.Lstat(filepath.Join(dir, socketName)); !os.IsNotExist(err) {
		t.Fatal("owned socket not removed")
	}
	next, err := Listen(context.Background(), dir)
	if err != nil {
		t.Fatal("clean restart refused")
	}
	releaseFixture(t, next)
}

func TestEndpointMissingClientNeverBootstraps(t *testing.T) {
	dir := endpointDir(t)
	if _, err := Dial(context.Background(), dir); err == nil {
		t.Fatal("unregistered client connected")
	}
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 0 {
		t.Fatal("read client created ownership or socket state")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Listen(ctx, dir); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled listen lost caller identity")
	}
	files, _ = os.ReadDir(dir)
	if len(files) != 0 {
		t.Fatal("cancelled listen created ownership")
	}
}

func TestEndpointUnknownStaleSocketNeverAdopted(t *testing.T) {
	dir := endpointDir(t)
	path := filepath.Join(dir, socketName)
	foreign, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal("unknown socket fixture unavailable")
	}
	foreign.SetUnlinkOnClose(false)
	if os.Chmod(path, 0600) != nil {
		t.Fatal("unknown socket fixture not private")
	}
	_ = foreign.Close()
	before, err := os.Lstat(path)
	if err != nil {
		t.Fatal("unknown socket missing")
	}
	if _, err := Listen(context.Background(), dir); err == nil {
		t.Fatal("unknown stale socket adopted")
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("unknown stale socket was replaced")
	}
}

func TestEndpointOwnedStaleSocketRecovery(t *testing.T) {
	dir := endpointDir(t)
	l, err := Listen(context.Background(), dir)
	if err != nil {
		t.Fatal("stale endpoint fixture unavailable")
	}
	if l.Close() != nil {
		t.Fatal("stale fixture stop failed")
	}
	// Model process loss of descriptors without normal namespace cleanup.
	_ = l.owner.Close()
	_ = l.dir.Close()
	next, err := Listen(context.Background(), dir)
	if err != nil {
		t.Fatal("exact retained stale endpoint did not recover")
	}
	releaseFixture(t, next)
}

func TestEndpointNamespaceReplacementPreserved(t *testing.T) {
	for _, item := range []string{socketName, ownerName} {
		t.Run(item, func(t *testing.T) {
			dir := endpointDir(t)
			l, err := Listen(context.Background(), dir)
			if err != nil {
				t.Fatal("replacement fixture unavailable")
			}
			if l.Close() != nil {
				t.Fatal("replacement fixture stop failed")
			}
			path := filepath.Join(dir, item)
			if os.Rename(path, path+".retained") != nil || os.WriteFile(path, []byte("synthetic-foreign-marker"), 0600) != nil {
				t.Fatal("replacement fixture not built")
			}
			requireCode(t, l.Release(), authority.Conflict)
			body, err := os.ReadFile(path)
			if err != nil || string(body) != "synthetic-foreign-marker" {
				t.Fatal("replacement bytes removed or changed")
			}
			if _, err := Listen(context.Background(), dir); err == nil {
				t.Fatal("replacement owner/socket admitted")
			}
		})
	}
}

func TestEndpointPrivateModesAndSymlinksRefused(t *testing.T) {
	for _, item := range []string{"directory", ownerName, socketName} {
		t.Run(item, func(t *testing.T) {
			dir := endpointDir(t)
			if item == "directory" {
				if os.Chmod(dir, 0755) != nil {
					t.Fatal("mode fixture unavailable")
				}
			} else {
				if os.Symlink("synthetic-missing", filepath.Join(dir, item)) != nil {
					t.Fatal("symlink fixture unavailable")
				}
			}
			if _, err := Listen(context.Background(), dir); err == nil {
				t.Fatal("insecure namespace admitted")
			}
			if _, err := Dial(context.Background(), dir); err == nil {
				t.Fatal("insecure namespace dialed")
			}
		})
	}
}

func TestEndpointLiveSocketNotReplacedEvenWithRetainedBinding(t *testing.T) {
	dir := endpointDir(t)
	l, err := Listen(context.Background(), dir)
	if err != nil {
		t.Fatal("live endpoint fixture unavailable")
	}
	// Simulate an uncooperative same-user listener retaining its live socket
	// after releasing singleton ownership. The probe must still refuse it.
	_ = l.owner.Close()
	defer l.listener.Close()
	defer l.dir.Close()
	before, _ := os.Lstat(filepath.Join(dir, socketName))
	if _, err := Listen(context.Background(), dir); err == nil {
		t.Fatal("live socket replaced")
	}
	after, err := os.Lstat(filepath.Join(dir, socketName))
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("live socket inode replaced")
	}
}
