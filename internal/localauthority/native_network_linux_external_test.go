//go:build linux

package localauthority_test

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/localauthority"
	"golang.org/x/sys/unix"
)

// This probe exists only in the augmented test binary. TCP and UDP/DNS use
// fixture-owned literal loopback destinations; no external resolver is called.
func runNativeNetworkProbe() int {
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 71
	}
	defer l.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := l.Accept()
		if err == nil {
			_ = conn.Close()
		}
	}()
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp4", l.Addr().String())
	if err != nil {
		_ = l.Close()
		<-done
		return 72
	}
	_ = conn.Close()
	<-done
	dns, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		return 73
	}
	defer dns.Close()
	dnsDone := make(chan struct{})
	go func() {
		defer close(dnsDone)
		var buf [4096]byte
		for {
			_ = dns.SetReadDeadline(time.Now().Add(time.Second))
			n, peer, err := dns.ReadFrom(buf[:])
			if err != nil {
				return
			}
			if n < 12 {
				continue
			}
			// Fixture-owned NXDOMAIN response echoes the actual question only.
			buf[2], buf[3] = buf[2]|0x80, 0x83
			for i := 6; i < 12; i++ {
				buf[i] = 0
			}
			_, _ = dns.WriteTo(buf[:n], peer)
		}
	}()
	r := net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "udp4", dns.LocalAddr().String())
	}}
	_, err = r.LookupHost(ctx, "qualification.invalid.")
	_ = dns.Close()
	<-dnsDone
	var de *net.DNSError
	if !errors.As(err, &de) || !de.IsNotFound {
		return 74
	}
	return 0
}

// A real escaped-session descendant is a positive control for EXITKILL, not
// product behavior. The finite fixture has a fallback expiry even on failure.
func runNativeEscapeProbe(selector string) int {
	if selector == "__authority_network_escape_child" {
		session, err := unix.Getsid(0)
		if err != nil || session != os.Getpid() {
			return 75
		}
		var ready [8]byte
		binary.BigEndian.PutUint64(ready[:], uint64(os.Getpid()))
		if _, err := os.Stdout.Write(ready[:]); err != nil {
			return 76
		}
		time.Sleep(10 * time.Second)
		return 77
	}
	executable, err := os.Executable()
	if err != nil {
		return 75
	}
	cmd := exec.Command(executable, "__authority_network_escape_child")
	cmd.Env = []string{}
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	cmd.Stdout = os.Stdout
	if cmd.Run() != nil {
		return 76
	}
	return 77
}

type nativeTraceTarget struct {
	mu sync.Mutex
	fd int
}

func (p *nativeTraceTarget) pin(pid int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	fd, err := unix.PidfdOpen(pid, 0)
	if err != nil || p.fd >= 0 {
		if fd >= 0 {
			_ = unix.Close(fd)
		}
		return errors.New("native observation target is unavailable")
	}
	p.fd = fd
	return nil
}

func (p *nativeTraceTarget) signal(sig unix.Signal) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fd < 0 {
		return errors.New("native observation target is unavailable")
	}
	if unix.PidfdSendSignal(p.fd, sig, nil, 0) != nil {
		return errors.New("native observation target is unavailable")
	}
	return nil
}

func (p *nativeTraceTarget) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.fd >= 0 {
		_ = unix.Close(p.fd)
		p.fd = -1
	}
}

// The normal tracer remains a joined child of this fixture. A pidfd pins the
// actual observed tracee for graceful SIGTERM, never a caller-supplied PID.
// EXITKILL covers every followed descendant, even if it escapes the group.
// Cancellation kills only the owned tracer os.Process, never a numeric group;
// kernel EXITKILL and a real escaped-descendant calibration qualify cleanup.
func instrumentNativeTrace(o *nativeNetworkTrace) func(*exec.Cmd) {
	return func(cmd *exec.Cmd) {
		args := append([]string{}, cmd.Args...)
		cmd.Path = "/usr/bin/strace"
		cmd.Args = append([]string{cmd.Path, "--kill-on-exit", "-q", "-f", "-o", "/proc/self/fd/2", "-s", "0", "-e", "trace=network,execve,execveat,clone,clone3,fork,vfork"}, args...)
		cmd.Stderr = o
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		cmd.WaitDelay = 2 * time.Second
		cmd.Cancel = func() error { return cmd.Process.Kill() }
	}
}

func nativeTraceCancellationCalibration(t *testing.T) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("native cancellation calibration executable unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "__authority_network_escape_probe")
	cmd.Env = []string{}
	cmd.Dir = "/"
	var observed nativeNetworkTrace
	instrumentNativeTrace(&observed)(cmd)
	// This control intentionally delegates to a new session. It qualifies
	// kernel teardown, not strict graph acceptance; raw trace is discarded.
	cmd.Stderr = io.Discard
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal("native cancellation calibration channel unavailable")
	}
	defer out.Close()
	if cmd.Start() != nil {
		t.Fatal("native cancellation calibration could not start")
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("native cancellation tracer did not join")
		}
	})
	if f, ok := out.(*os.File); !ok || f.SetReadDeadline(time.Now().Add(3*time.Second)) != nil {
		t.Fatal("native cancellation calibration deadline unavailable")
	}
	var ready [8]byte
	if _, err := io.ReadFull(out, ready[:]); err != nil {
		t.Fatal("native escaped-session control did not become ready")
	}
	pid := binary.BigEndian.Uint64(ready[:])
	if pid < 1 || pid > 1<<31-1 {
		t.Fatal("native cancellation calibration identity invalid")
	}
	fd, err := unix.PidfdOpen(int(pid), 0)
	if err != nil {
		t.Fatal("native cancellation calibration target unavailable")
	}
	defer unix.Close(fd)
	defer unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0) // failure cleanup owns this pinned fixture child
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("native cancellation tracer did not join")
	}
	wait := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	if n, err := unix.Poll(wait, 3000); err != nil || n != 1 || wait[0].Revents&unix.POLLIN == 0 {
		t.Fatal("EXITKILL failed to terminate escaped-session descendant")
	}
}

func nativeTraceUnfollowedBirthCalibration(t *testing.T) {
	t.Helper()
	// C owns this fork control: running Go runtime code after a raw clone would
	// not be a sound fixture. The child makes one inert INET socket attempt and
	// exits immediately; it neither resolves DNS nor contacts a destination.
	const source = `#define _GNU_SOURCE
#include <sched.h>
#include <signal.h>
#include <sys/socket.h>
#include <sys/syscall.h>
#include <sys/wait.h>
#include <unistd.h>
int main(void) {
  long child = syscall(SYS_clone, SIGCHLD | CLONE_UNTRACED, 0, 0, 0, 0);
  if (child < 0) return 71;
  if (child == 0) {
    int fd = socket(AF_INET, SOCK_DGRAM, 0);
    if (fd >= 0) close(fd);
    _exit(0);
  }
  int status;
  if (waitpid(child, &status, 0) != child) return 72;
  return WIFEXITED(status) && WEXITSTATUS(status) == 0 ? 0 : 73;
}`
	executable := filepath.Join(t.TempDir(), "unfollowed-birth-control")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	compile := exec.CommandContext(ctx, "/usr/bin/cc", "-x", "c", "-", "-o", executable)
	compile.Stdin = strings.NewReader(source)
	if compile.Run() != nil {
		t.Fatal("native unfollowed-birth control compiler unavailable")
	}
	// Prove the actual bounded fixture creates/joins its child, not just that
	// an unavailable syscall or malformed executable produces an error.
	if exec.CommandContext(ctx, executable).Run() != nil {
		t.Fatal("native unfollowed-birth positive control unavailable")
	}
	cmd := exec.CommandContext(ctx, executable)
	cmd.Env = []string{}
	cmd.Dir = "/"
	var observed nativeNetworkTrace
	instrumentNativeTrace(&observed)(cmd)
	_ = cmd.Run() // parser refusal may stop the tracer; never treat that as zero
	e := observed.snapshot(true)
	if !e.Partial || e.ForbiddenBirth != 1 {
		t.Fatal("actual CLONE_UNTRACED delegation escaped strict observation")
	}
}

func nativeTraceCalibration(t *testing.T) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("native observer calibration executable unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "__authority_network_probe")
	cmd.Env = []string{}
	cmd.Dir = "/"
	var observed nativeNetworkTrace
	instrumentNativeTrace(&observed)(cmd)
	if cmd.Run() != nil {
		t.Fatal("native observer positive calibration failed")
	}
	e := observed.snapshot(true)
	if e.Partial || e.INETSocket < 3 || e.INETOperation < 1 || e.Listen < 1 || e.Connect < 1 || e.DatagramSocket < 1 || e.Send < 1 {
		t.Fatal("native observer missed actual TCP/UDP/DNS calibration")
	}
}

func nativeTraceProduct(t *testing.T) string {
	t.Helper()
	// An external artifact requires an explicit byte identity. This identifies
	// separately built bytes, not their source: only the recorded build recipe
	// (the fixed CI runner builds this checkout) proves current-source evidence.
	if name := os.Getenv("GITCODE_MCP_NATIVE_AUTHORITY_FIXTURE_BINARY"); name != "" {
		digest := os.Getenv("GITCODE_MCP_NATIVE_AUTHORITY_FIXTURE_SHA256")
		if !nativeArtifactMatches(name, digest) {
			t.Fatal("separately built native observation artifact identity unavailable")
		}
		t.Log("observing separately built artifact sha256:" + digest + "; source qualification requires its recorded build recipe")
		return name
	}
	return buildNativeProduct(t)
}

func nativeArtifactMatches(name, digest string) bool {
	expected, err := hex.DecodeString(digest)
	if err != nil || len(expected) != sha256.Size || digest != strings.ToLower(digest) || !filepath.IsAbs(name) {
		return false
	}
	info, err := os.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Size() < 1 || info.Size() > 128<<20 {
		return false
	}
	fd, err := unix.Open(name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return false
	}
	f := os.NewFile(uintptr(fd), "native-observer-artifact")
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return false
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(f, (128<<20)+1))
	return err == nil && n == info.Size() && hex.EncodeToString(h.Sum(nil)) == digest
}

func TestNativeNetworkArtifactIdentityIsExplicit(t *testing.T) {
	name := filepath.Join(t.TempDir(), "synthetic-product")
	input := []byte("synthetic public-safe executable fixture")
	if os.WriteFile(name, input, 0700) != nil {
		t.Fatal("artifact identity fixture unavailable")
	}
	d := sha256.Sum256(input)
	digest := hex.EncodeToString(d[:])
	if !nativeArtifactMatches(name, digest) {
		t.Fatal("identified artifact refused")
	}
	for _, wrong := range []string{"", "synthetic", strings.Repeat("0", 64), strings.ToUpper(digest)} {
		if nativeArtifactMatches(name, wrong) {
			t.Fatal("unidentified artifact accepted")
		}
	}
	link := filepath.Join(t.TempDir(), "link")
	if os.Symlink(name, link) != nil || nativeArtifactMatches(link, digest) {
		t.Fatal("artifact identity followed a symlink")
	}
	if os.WriteFile(name, []byte("changed synthetic fixture"), 0700) != nil || nativeArtifactMatches(name, digest) {
		t.Fatal("changed artifact retained its old byte identity")
	}
}

func TestNativeAuthorityStrictProcessNetworkIsolation(t *testing.T) {
	if os.Getenv("GITCODE_MCP_NATIVE_AUTHORITY_TRACE") != "1" {
		t.Skip("explicit Linux native syscall observer qualification not selected")
	}
	if info, err := os.Stat("/usr/bin/strace"); err != nil || !info.Mode().IsRegular() {
		t.Fatal("required native syscall observer unavailable")
	}
	nativeTraceCalibration(t)
	nativeTraceCancellationCalibration(t)
	nativeTraceUnfollowedBirthCalibration(t)
	executable := nativeTraceProduct(t)
	p, registry, dir := nativeLaunchFixture(t)
	target := &nativeTraceTarget{fd: -1}
	t.Cleanup(target.close)
	observed := &nativeNetworkTrace{onExec: target.pin}
	child := spawnProductChild(t, executable, p, dir, "", instrumentNativeTrace(observed))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request := nativeMutation("synthetic-observed-native-source", "Observed canonical issue")
	var receipt authority.MutationReceipt
	if child.client.Call(ctx, "AuthorityV1.Mutate", request, &receipt) != nil || receipt.Validate() != nil {
		t.Fatal("observed actual product mutation failed")
	}
	var read authority.ReadResult
	if child.client.Call(ctx, "AuthorityV1.Read", authority.ReadRequest{ProjectID: fixtureProject, Kind: authority.Issue, Operation: "get", EntityID: receipt.Citation.EntityID}, &read) != nil || len(read.Entities) != 1 || read.Entities[0].Citation != receipt.Citation {
		t.Fatal("observed actual product citation readback failed")
	}
	var search localauthority.SearchResult
	if child.client.Call(ctx, "AuthorityV1.Search", localauthority.SearchRequest{ProjectID: fixtureProject, Query: "canonical issue", Mode: "hybrid", Limit: 8}, &search) != nil || search.FallbackReason != "policy_denied" || search.EffectiveMode != "full_text" || len(search.Hits) != 1 {
		t.Fatal("observed strict product search fabricated semantic readiness")
	}
	if target.signal(unix.SIGTERM) != nil {
		t.Fatal("observed actual child could not receive owned stop")
	}
	select {
	case <-child.done:
	case <-ctx.Done():
		t.Fatal("observed product/tracer/stream did not join")
	}
	if child.cmd.ProcessState == nil || !child.cmd.ProcessState.Success() {
		t.Fatal("observed product did not exit cleanly after joined shutdown")
	}
	e := observed.snapshot(true)
	if e.Partial || e.RoleRead == 0 || e.INETSocket != 0 || e.INETOperation != 0 || e.UnixConnect != 0 {
		t.Fatal("strict product network observation is incomplete or reports an attempt")
	}
	writer, err := localauthority.OpenWriter(ctx, registry, dir, fixtureProject)
	if err != nil {
		t.Fatal("observed native teardown retained canonical writer ownership")
	}
	defer writer.Close()
	result, err := writer.Read(ctx, authority.ReadRequest{ProjectID: fixtureProject, Kind: authority.Issue, Operation: "get", EntityID: receipt.Citation.EntityID})
	if err != nil || len(result.Entities) != 1 || result.Entities[0].Citation != receipt.Citation {
		t.Fatal("observed native teardown changed confirmed canonical state")
	}
}
