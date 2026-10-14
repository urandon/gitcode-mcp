//go:build darwin || linux

package localauthority_test

import (
	"context"
	"errors"
	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority"
	"gitcode-mcp/internal/servicectl"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReviewStoppedServerCannotRemainCurrentServing(t *testing.T) {
	prepared, _, directory := nativeLaunchFixture(t)
	core, err := localauthority.NewStrictCoordinator(context.Background(), prepared, []localauthority.ProjectBinding{{ProjectID: fixtureProject, Directory: directory}})
	if err != nil {
		t.Fatal("borrowed core unavailable")
	}
	t.Cleanup(func() {
		if core.Close(context.Background()) != nil {
			t.Error("borrowed core did not join")
		}
	})
	path := filepath.Join(privateTemp(t), "s")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil || os.Chmod(path, 0600) != nil {
		t.Fatal("private listener unavailable")
	}
	defer listener.Close()
	server, err := servicectl.NewAuthorityServer(core)
	if err != nil {
		t.Fatal("server unavailable")
	}
	serveCtx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- server.Serve(serveCtx, listener) }()
	joined := false
	t.Cleanup(func() {
		stop()
		if !joined {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("server did not join")
			}
		}
	})
	client := &servicectl.RPCClient{Network: "unix", Address: path}
	call, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var caps authority.Negotiation
	if client.Call(call, "AuthorityV1.Negotiate", authority.NegotiationRequest{Protocol: authority.Protocol, Authority: authority.Local, SchemaMin: 1, SchemaMax: 1}, &caps) != nil {
		t.Fatal("server readiness boundary unavailable")
	}
	req := localauthority.RuntimeHealthRequest{ProjectID: fixtureProject, RuntimeBinding: core.RuntimeBinding()}
	read := func() localauthority.RuntimeHealthView {
		for {
			v, e := core.RuntimeHealth(call, req)
			if e == nil {
				return v
			}
			var domain authority.Error
			if !errors.As(e, &domain) || domain.Code != authority.Conflict {
				t.Fatal("qualified core health unavailable")
			}
			select {
			case <-time.After(time.Millisecond):
			case <-call.Done():
				t.Fatal("core remained busy")
			}
		}
	}
	if read().Phase != "serving" {
		t.Fatal("actual admitted server was not serving")
	}
	stop()
	select {
	case e := <-done:
		joined = true
		if e != nil {
			t.Fatal("server stop failed")
		}
	case <-call.Done():
		t.Fatal("server did not join")
	}
	if !core.Available() {
		t.Fatal("server incorrectly owned borrowed core lifetime")
	}
	if read().Phase == "serving" {
		t.Fatal("stopped listener left a false current serving proof")
	}
}
