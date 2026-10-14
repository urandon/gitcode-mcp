//go:build darwin || linux

package localauthority_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority"
	"gitcode-mcp/internal/servicectl"
)

func nativeCurrentHealth(t *testing.T, p *servicectl.AuthorityProcess) localauthority.RuntimeHealthView {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		v, err := p.RuntimeHealth(ctx, fixtureProject)
		if err == nil {
			if v.Validate() != nil || v.Phase != "serving" {
				t.Fatal("actual native health did not prove current serving owner")
			}
			return v
		}
		var domain authority.Error
		if !errors.As(err, &domain) || domain.Code != authority.Conflict {
			t.Fatal("native current health failed outside finite busy admission")
		}
		select {
		case <-tick.C:
		case <-ctx.Done():
			t.Fatal("native owner never completed a qualified health turn")
		}
	}
}

func TestNativeRuntimeHealthEpochRetainedIndependentlyAcrossRestart(t *testing.T) {
	p, _, dir := nativeLaunchFixture(t)
	first := nativeProcess(t, p, dir)
	view := nativeCurrentHealth(t, first)
	if first.Close(context.Background()) != nil {
		t.Fatal("first health owner did not join")
	}
	if _, err := first.RuntimeHealth(context.Background(), fixtureProject); err == nil {
		t.Fatal("known exited owner returned current health")
	}
	next := nativeProcess(t, p, dir)
	current := nativeCurrentHealth(t, next)
	if view.RuntimeBinding == current.RuntimeBinding {
		t.Fatal("restart reused an owner epoch")
	}
	old := localauthority.RuntimeHealthRequest{ProjectID: fixtureProject, RuntimeBinding: view.RuntimeBinding}
	if next.Client().Call(context.Background(), "AuthorityV1.RuntimeHealth", old, nil) == nil {
		t.Fatal("new owner accepted a previous epoch learned from request")
	}
}

func TestNativeRuntimeHealthRefusesLiveEndpointEchoReplacement(t *testing.T) {
	p, _, dir := nativeLaunchFixture(t)
	process := nativeProcess(t, p, dir)
	view := nativeCurrentHealth(t, process)
	path := process.Client().Address
	var caps authority.Negotiation
	if process.Client().Call(context.Background(), "AuthorityV1.Negotiate", authority.NegotiationRequest{Protocol: authority.Protocol, Authority: authority.Local, SchemaMin: 1, SchemaMax: 1}, &caps) != nil {
		t.Fatal("actual capability fixture unavailable")
	}
	// Generic caller mutation must not retarget the independently retained
	// health handle or cause a TCP/DNS attempt.
	process.Client().Network, process.Client().Address = "tcp", "synthetic.invalid:9"
	if nativeCurrentHealth(t, process).RuntimeBinding != view.RuntimeBinding {
		t.Fatal("generic client mutation retargeted health")
	}
	if os.Rename(path, filepath.Join(filepath.Dir(path), "retained-original")) != nil {
		t.Fatal("test endpoint preservation failed")
	}
	echo, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil || os.Chmod(path, 0600) != nil {
		t.Fatal("echo replacement unavailable")
	}
	echo.SetUnlinkOnClose(false)
	identity, err := os.Lstat(path)
	if err != nil {
		t.Fatal("echo identity unavailable")
	}
	var contacted atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := echo.AcceptUnix()
			if err != nil {
				return
			}
			contacted.Add(1)
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			var req servicectl.RPCRequest
			if json.NewDecoder(io.LimitReader(conn, localauthority.MaxRuntimeHealthBytes)).Decode(&req) == nil {
				var result any = caps
				if req.Method == "AuthorityV1.RuntimeHealth" {
					var selector localauthority.RuntimeHealthRequest
					_ = json.Unmarshal(req.Params, &selector)
					forged := view
					forged.RuntimeBinding = selector.RuntimeBinding
					result = forged
				}
				_ = json.NewEncoder(conn).Encode(servicectl.RPCResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
			}
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { _ = echo.Close(); <-done })
	if _, err := process.RuntimeHealth(context.Background(), fixtureProject); err == nil || contacted.Load() != 0 {
		t.Fatal("pinned health accepted or contacted a replacement echo responder")
	}
	if process.Close(context.Background()) != nil {
		t.Fatal("replaced-endpoint original owner did not join")
	}
	retained, err := os.Lstat(path)
	if err != nil || !os.SameFile(identity, retained) {
		t.Fatal("original owner cleanup deleted a replacement endpoint")
	}
}
