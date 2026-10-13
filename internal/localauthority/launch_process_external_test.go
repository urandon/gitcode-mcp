//go:build darwin || linux

package localauthority_test

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority"
	"gitcode-mcp/internal/servicectl"
	"golang.org/x/sys/unix"
)

// Native process fixtures execute the same binary and production fixed-role
// entry. This is not a stand-in claim for product-wide startup tracing or real
// foreign-user qualification, which remain separate native gates.
func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == servicectl.AuthorityRuntimeSelector {
		os.Exit(servicectl.RunInheritedAuthority(context.Background()))
	}
	os.Exit(m.Run())
}

func nativeLaunchFixture(t *testing.T) (*localauthority.PreparedRegistry, *localauthority.Registry, string) {
	t.Helper()
	registry, err := localauthority.NewFixtureRegistry(privateTemp(t))
	if err != nil {
		t.Fatal("native fixture registry unavailable")
	}
	dir := privateTemp(t)
	m := authority.Manifest{Schema: 1, ID: fixtureProject, Slug: "native-fixture", Authority: authority.Local, Entities: []authority.EntityKind{authority.Issue, authority.Comment, authority.Label, authority.Milestone, authority.Document, authority.Link}, Network: authority.NetworkPolicy{DataEgress: "deny", ControlTransport: "ipc_only"}, Search: authority.SearchIntent{Default: "hybrid", Fallback: "full_text"}}
	s, err := localauthority.Initialize(context.Background(), registry, dir, m)
	if err != nil {
		t.Fatal("native fixture authority init failed")
	}
	if _, err = s.SetupKnowledge(context.Background(), fixtureProject, "synthetic-launch-setup"); err != nil || s.Close() != nil {
		t.Fatal("native fixture knowledge setup failed")
	}
	p, err := registry.PrepareRuntime()
	if err != nil {
		t.Fatal("native fixture capability preparation failed")
	}
	t.Cleanup(func() {
		if p.Close() != nil {
			t.Error("native parent preparation leaked")
		}
	})
	return p, registry, dir
}

func nativeProcess(t *testing.T, p *localauthority.PreparedRegistry, dir string) *servicectl.AuthorityProcess {
	t.Helper()
	if runtime.GOOS == "darwin" && !nativeDarwinCgo() {
		t.Skip("native Darwin descriptor inventory requires cgo")
	}
	prepared, err := p.PrepareLaunch(context.Background(), []localauthority.ProjectBinding{{ProjectID: fixtureProject, Directory: dir}})
	if err != nil {
		t.Fatal("native full cohort preparation failed")
	}
	defer prepared.Close()
	path := filepath.Join(privateTemp(t), "s")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil || os.Chmod(path, 0600) != nil {
		t.Fatal("native launch listener failed")
	}
	assertNativeParentDescriptorHygiene(t)
	process, err := servicectl.StartAuthorityRuntime(context.Background(), prepared, l)
	if err != nil {
		t.Fatalf("native same-binary launch refused: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		if process.Close(ctx) != nil {
			t.Error("native child did not join cleanly")
		}
	})
	return process
}

// Diagnose ambient capabilities before exec, without exposing descriptor
// numbers, locators, file contents, or kernel diagnostic bodies. This is a
// bounded fixture assertion, not a substitute for child/native qualification.
func assertNativeParentDescriptorHygiene(t *testing.T) {
	t.Helper()
	classes := map[string]int{}
	for fd := 3; fd < 1024; fd++ {
		flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
		if err != nil || flags&unix.FD_CLOEXEC != 0 {
			continue
		}
		name := "other"
		var st unix.Stat_t
		if unix.Fstat(fd, &st) == nil {
			switch st.Mode & unix.S_IFMT {
			case unix.S_IFREG:
				name = "regular"
			case unix.S_IFDIR:
				name = "directory"
			case unix.S_IFSOCK:
				name = "socket"
			case unix.S_IFCHR:
				name = "character"
			case unix.S_IFIFO:
				name = "pipe"
			}
		}
		classes[name]++
	}
	if len(classes) != 0 {
		t.Fatalf("native launch parent has ambient non-close-on-exec resources: classes=%v", classes)
	}
}

func TestNativeAuthorityProcessLaunchReplayRestartAndReadOnlyCoexistence(t *testing.T) {
	p, registry, dir := nativeLaunchFixture(t)
	t.Setenv("HOME", privateTemp(t))
	t.Setenv("GITCODE_MCP_CONFIG", filepath.Join(privateTemp(t), "must-not-load"))
	t.Setenv("HTTP_PROXY", "http://synthetic.invalid:9")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
	process := nativeProcess(t, p, dir)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request := nativeMutation("synthetic-launched-replay", "Confirmed native source")
	var first authority.MutationReceipt
	if process.Client().Call(ctx, "AuthorityV1.Mutate", request, &first) != nil {
		t.Fatal("native admitted mutation failed")
	}
	reader, err := localauthority.OpenReadOnly(ctx, dir, fixtureProject)
	if err != nil {
		t.Fatal("native writer blocked canonical offline reads")
	}
	result, err := reader.Read(ctx, authority.ReadRequest{ProjectID: fixtureProject, Kind: authority.Issue, Operation: "get", EntityID: first.Citation.EntityID, Revision: 1})
	_ = reader.Close()
	if err != nil || len(result.Entities) != 1 || result.Entities[0].Citation != first.Citation {
		t.Fatal("native citation did not resolve offline")
	}
	if other, err := localauthority.OpenWriter(ctx, registry, dir, fixtureProject); err == nil {
		other.Close()
		t.Fatal("native process admitted a second writer")
	}
	if err = process.Close(ctx); err != nil {
		t.Fatal("native child did not release ownership after join")
	}
	next := nativeProcess(t, p, dir)
	var replay authority.MutationReceipt
	if next.Client().Call(ctx, "AuthorityV1.Mutate", request, &replay) != nil || !replay.Replayed || replay.Citation != first.Citation {
		t.Fatal("native restart changed confirmed result")
	}
}

func TestNativeAuthorityColdEntryRefusesWithoutPreparation(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("native executable unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, servicectl.AuthorityRuntimeSelector)
	cmd.Env = []string{}
	cmd.Dir = "/"
	err = cmd.Run()
	if ctx.Err() != nil {
		t.Fatal("cold native entry did not refuse promptly")
	}
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() == 0 {
		t.Fatal("cold native entry did not refuse missing fixed roles")
	}
}
