//go:build darwin || linux

package localauthority_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority"
	"gitcode-mcp/internal/observability"
	"gitcode-mcp/internal/servicectl"
)

func TestRuntimeObservationActualBorrowedListenerAndRejectedAdmission(t *testing.T) {
	prepared, _, directory := nativeLaunchFixture(t)
	relay := observability.NewLocalProjectRelay([]string{string(fixtureProject)})
	ctx := observability.WithLocalProjectRelay(context.Background(), relay)
	core, err := localauthority.NewStrictCoordinator(ctx, prepared, []localauthority.ProjectBinding{{ProjectID: fixtureProject, Directory: directory}})
	if err != nil {
		t.Fatal("borrowed observed core unavailable")
	}
	t.Cleanup(func() { _ = core.Close(context.Background()); relay.Seal() })
	server, err := servicectl.NewAuthorityServer(core)
	if err != nil {
		t.Fatal("borrowed observed server unavailable")
	}
	path := filepath.Join(privateTemp(t), "s")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal("borrowed listener unavailable")
	}
	defer listener.Close()
	if os.Chmod(path, 0666) != nil || server.Serve(ctx, listener) == nil {
		t.Fatal("unsafe listener was not refused before admission")
	}
	select {
	case <-relay.Events():
		t.Fatal("rejected listener invented a runtime event")
	default:
	}
	if os.Chmod(path, 0600) != nil {
		t.Fatal("private listener mode unavailable")
	}
	serving, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- server.Serve(serving, listener) }()
	joined := false
	t.Cleanup(func() {
		stop()
		if !joined {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("borrowed observed server did not join")
			}
		}
	})
	call, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := &servicectl.RPCClient{Network: "unix", Address: path}
	var caps authority.Negotiation
	if client.Call(call, "AuthorityV1.Negotiate", authority.NegotiationRequest{Protocol: authority.Protocol, Authority: authority.Local, SchemaMin: 1, SchemaMax: 1}, &caps) != nil {
		t.Fatal("actual private listener readiness unavailable")
	}
	stop()
	select {
	case err := <-done:
		joined = true
		if err != nil {
			t.Fatal("actual borrowed listener did not stop")
		}
	case <-call.Done():
		t.Fatal("actual borrowed listener failed to join")
	}
	if !core.Available() {
		t.Fatal("borrowed listener stop incorrectly closed writer")
	}
	select {
	case event := <-relay.Events():
		if event.Code != observability.LocalAuthorityPhaseChanged || event.Phase != "runtime" || event.Outcome != "serving" {
			t.Fatal("actual serving boundary changed")
		}
	default:
		t.Fatal("actual serving admission lost historical proof")
	}
	select {
	case <-relay.Events():
		t.Fatal("borrowed listener stop invented writer shutdown")
	default:
	}
	if core.Close(call) != nil {
		t.Fatal("explicit writer close failed")
	}
	for _, want := range []string{"closing", "stopped"} {
		select {
		case event := <-relay.Events():
			if event.Phase != "runtime" || event.Outcome != want {
				t.Fatal("explicit writer shutdown changed historical proof")
			}
		default:
			t.Fatal("explicit joined writer shutdown lost proof")
		}
	}
}
