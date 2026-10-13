//go:build darwin

package nativelaunch

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"gitcode-mcp/internal/authority"
	"golang.org/x/sys/unix"
)

func TestNativeDarwinImageABIRefusesBeforeKernel(t *testing.T) {
	token := syntheticImageToken()
	for _, c := range []struct {
		op   uint32
		size int
	}{{0, 0}, {0, 21}, {18, 20}, {18, 22}, {999, 21}} {
		requireImageCode(t, readDarwinCode(token, c.op, make([]byte, c.size)), authority.InvalidContract)
	}
	token[5] = 0
	requireImageCode(t, readDarwinCode(token, 0, make([]byte, 4)), authority.InvalidContract)
	_, err := readDarwinPeerToken(nil)
	requireImageCode(t, err, authority.InvalidContract)
}

func TestNativeDarwinImageHelper(t *testing.T) {
	args := os.Args
	if len(args) < 2 || args[len(args)-2] != "native-image-helper-v1" {
		t.Skip("isolated native image helper only")
	}
	mode := args[len(args)-1]
	if mode != "stay" && mode != "execsame" && mode != "execdifferent" && mode != "second" && mode != "refresh" && mode != "forward" {
		t.Fatal("invalid isolated helper role")
	}
	hardenImageFixture(t)
	if mode == "forward" {
		if _, err := unix.Write(3, []byte{'F'}); err != nil {
			t.Fatal("forwarded fixture send failed")
		}
		_, _ = os.Stdout.Write([]byte{'F'})
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	ready := byte('R')
	if mode == "second" {
		ready = 'N'
	}
	if _, err := unix.Write(3, []byte{ready}); err != nil {
		t.Fatal("isolated helper readiness failed")
	}
	var command [1]byte
	if _, err := unix.Read(3, command[:]); err != nil {
		t.Fatal("isolated helper control failed")
	}
	if mode == "refresh" && command[0] == 'D' {
		if _, err := unix.Write(3, []byte{'R'}); err != nil {
			t.Fatal("isolated owner refresh failed")
		}
		if _, err := unix.Read(3, command[:]); err != nil {
			t.Fatal("isolated owner refresh control failed")
		}
	}
	if command[0] == 'Q' {
		return
	}
	if command[0] != 'S' || mode == "stay" || mode == "second" {
		t.Fatal("unexpected isolated helper command")
	}
	if mode == "execdifferent" {
		if syscall.Exec("/bin/sleep", []string{"sleep", "5"}, []string{}) != nil {
			t.Fatal("isolated different-image exec failed")
		}
		return
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal("isolated own image coordinate unavailable")
	}
	// Only this test control keeps slot3 across deliberate re-exec. This is not
	// the production entry's mandatory post-exec CLOEXEC policy.
	if _, err := unix.FcntlInt(3, unix.F_SETFD, 0); err != nil {
		t.Fatal("isolated exec control unavailable")
	}
	if syscall.Exec(binary, []string{binary, "-test.run=^TestNativeDarwinImageHelper$", "-test.timeout=6s", "native-image-helper-v1", "second"}, []string{}) != nil {
		t.Fatal("isolated same-image exec failed")
	}
}

type imageChild struct {
	ctx     context.Context
	file    *os.File
	conn    *net.UnixConn
	cmd     *exec.Cmd
	joined  bool
	forward *os.File
}

func startImageChild(t *testing.T, mode string) *imageChild {
	t.Helper()
	binary, err := os.Executable()
	if err != nil {
		t.Fatal("isolated executing artifact unavailable")
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatal("isolated native pair unavailable")
	}
	unix.CloseOnExec(pair[0])
	unix.CloseOnExec(pair[1])
	left, right := os.NewFile(uintptr(pair[0]), "image-probe-parent"), os.NewFile(uintptr(pair[1]), "image-probe-child")
	connection, err := net.FileConn(left)
	if err != nil {
		_ = left.Close()
		_ = right.Close()
		t.Fatal("isolated native connection unavailable")
	}
	conn, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		_ = left.Close()
		_ = right.Close()
		t.Fatal("isolated native connection type mismatch")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	child := &imageChild{ctx: ctx, file: left, conn: conn}
	started := false
	t.Cleanup(func() {
		cancel()
		_ = conn.Close()
		_ = left.Close()
		_ = right.Close()
		if child.forward != nil {
			_ = child.forward.Close()
		}
		if started && !child.joined {
			_ = child.cmd.Wait()
			child.joined = true
		}
	})
	if mode == "refresh" {
		fd, err := unix.FcntlInt(right.Fd(), unix.F_DUPFD_CLOEXEC, 0)
		if err != nil {
			t.Fatal("isolated forwarded endpoint unavailable")
		}
		child.forward = os.NewFile(uintptr(fd), "isolated-forwarded-endpoint")
	}
	child.cmd = exec.CommandContext(ctx, binary, "-test.run=^TestNativeDarwinImageHelper$", "-test.timeout=6s", "native-image-helper-v1", mode)
	child.cmd.Env = []string{}
	child.cmd.ExtraFiles = []*os.File{right}
	child.cmd.Stdout, child.cmd.Stderr = io.Discard, io.Discard
	if child.cmd.Start() != nil {
		t.Fatal("isolated native child did not start")
	}
	started = true
	_ = right.Close()
	deadline, _ := ctx.Deadline()
	if conn.SetDeadline(deadline) != nil {
		t.Fatal("isolated native deadline failed")
	}
	child.read(t, 'R')
	return child
}

func (c *imageChild) read(t *testing.T, want byte) {
	t.Helper()
	var message [1]byte
	if n, err := c.conn.Read(message[:]); err != nil || n != 1 || message[0] != want {
		t.Fatal("isolated native child milestone missing")
	}
}

func (c *imageChild) send(t *testing.T, value byte) {
	t.Helper()
	if n, err := c.conn.Write([]byte{value}); err != nil || n != 1 {
		t.Fatal("isolated native child control failed")
	}
}

func (c *imageChild) finish(t *testing.T) {
	t.Helper()
	c.send(t, 'Q')
	err := c.cmd.Wait()
	c.joined = true
	if err != nil {
		t.Fatal("isolated native child did not join")
	}
}

func TestNativeDarwinImageOracle(t *testing.T) {
	if os.Getenv("GITCODE_MCP_NATIVE_AUTHORITY_IMAGE") != "1" {
		t.Skip("explicit native authority image qualification not requested")
	}
	hardenImageFixture(t)
	self, err := observeDarwinSelf(context.Background())
	if err != nil {
		var code authority.Error
		if errors.As(err, &code) {
			t.Fatalf("native self image oracle unavailable: %s", code.Code)
		}
		t.Fatal("native self image oracle unavailable")
	}
	for _, mode := range []string{"stay", "execsame", "execdifferent"} {
		t.Run(mode, func(t *testing.T) {
			child := startImageChild(t, mode)
			image, err := observeDarwinPeer(child.ctx, child.file, child.cmd.Process.Pid)
			if err != nil || image.hash != self.hash || image.hashType != self.hashType {
				t.Fatal("native child does not match own kernel artifact")
			}
			if revalidateDarwinPeer(child.ctx, child.file, image) != nil {
				t.Fatal("stable native child revalidation refused")
			}
			wrong := image.token
			wrong[7]++
			requireImageCode(t, readDarwinCode(wrong, darwinStatus, make([]byte, 4)), authority.Unavailable)
			if mode == "stay" {
				child.finish(t)
				if err := revalidateDarwinPeer(context.Background(), child.file, image); err == nil {
					t.Fatal("dead native child accepted")
				}
				return
			}
			child.send(t, 'S')
			if mode == "execsame" {
				child.read(t, 'N')
				current, err := observeDarwinPeer(child.ctx, child.file, child.cmd.Process.Pid)
				if err != nil || current.hash != image.hash || current.hashType != image.hashType || current.token[7] == image.token[7] {
					t.Fatal("same image re-exec did not change kernel incarnation")
				}
				requireImageCode(t, revalidateDarwinPeer(child.ctx, child.file, image), authority.Conflict)
				child.finish(t)
				return
			}
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				current, err := observeDarwinPeer(child.ctx, child.file, child.cmd.Process.Pid)
				if err == nil && current.hash != image.hash && current.token[7] != image.token[7] {
					requireImageCode(t, revalidateDarwinPeer(child.ctx, child.file, image), authority.Conflict)
					return // cleanup kills and joins the isolated sleep process.
				}
				time.Sleep(time.Millisecond)
			}
			t.Fatal("different image exec transition was not independently observed")
		})
	}
	// Strict ABI errors are finite; no kernel diagnostics/private body enters output.
	wrong := self.token
	wrong[7]++
	err = readDarwinCode(wrong, darwinStatus, make([]byte, 4))
	var typed authority.Error
	if !errors.As(err, &typed) || strings.Contains(err.Error(), "native-image") {
		t.Fatal("native failure was not finite")
	}
}

// This self-only opt-in process change is not part of the read-only adapter
// and never runs in default tests. No other process/global policy is modified.
func hardenImageFixture(t *testing.T) {
	t.Helper()
	flags := uint32(0x1300) // HARD|KILL|ENFORCEMENT, XNU CS_OPS_SET_STATUS.
	_, _, errno := unix.Syscall6(unix.SYS_CSOPS, uintptr(os.Getpid()), 9, uintptr(unsafe.Pointer(&flags)), 4, 0, 0)
	if errno != 0 {
		t.Fatal("isolated image hardening prerequisite unavailable")
	}
}

func TestNativeDarwinPeerImageIsNotQueuedMessageSender(t *testing.T) {
	if os.Getenv("GITCODE_MCP_NATIVE_AUTHORITY_IMAGE") != "1" {
		t.Skip("explicit native authority image qualification not requested")
	}
	hardenImageFixture(t)
	child := startImageChild(t, "refresh")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal("forwarded artifact unavailable")
	}
	forward := exec.CommandContext(child.ctx, binary, "-test.run=^TestNativeDarwinImageHelper$", "-test.timeout=6s", "native-image-helper-v1", "forward")
	forward.Env = []string{}
	forward.ExtraFiles = []*os.File{child.forward}
	forward.Stderr = io.Discard
	input, err := forward.StdinPipe()
	if err != nil {
		t.Fatal("forwarded fixture input unavailable")
	}
	output, err := forward.StdoutPipe()
	if err != nil {
		_ = input.Close()
		t.Fatal("forwarded fixture output unavailable")
	}
	started, joined := false, false
	t.Cleanup(func() {
		_ = input.Close()
		if started && !joined {
			_ = forward.Wait()
		}
	})
	if forward.Start() != nil {
		t.Fatal("forwarded fixture did not start")
	}
	started = true
	var signal [1]byte
	if _, err := io.ReadFull(output, signal[:]); err != nil || signal[0] != 'F' {
		t.Fatal("forwarded queued datagram missing")
	}
	// F is still queued. Its actual sender is the forwarded child, not the
	// original child whose image/last-owner metadata we will refresh next.
	child.send(t, 'D')
	deadline := time.Now().Add(2 * time.Second)
	matched := false
	for time.Now().Before(deadline) {
		if _, err := observeDarwinPeer(child.ctx, child.file, child.cmd.Process.Pid); err == nil {
			matched = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !matched {
		t.Fatal("original peer metadata did not refresh")
	}
	child.read(t, 'F')
	image, err := observeDarwinPeer(child.ctx, child.file, child.cmd.Process.Pid)
	if err != nil || image.token[5] == uint32(forward.Process.Pid) {
		t.Fatal("last-owner counterexample not independently established")
	}
	// Measurement succeeded for the original peer while the consumed message
	// came from a different process. Never turn this into per-message admission.
	child.read(t, 'R')
	child.finish(t)
	_ = input.Close()
	_, _ = io.Copy(io.Discard, output)
	if forward.Wait() != nil {
		joined = true
		t.Fatal("forwarded fixture did not join")
	}
	joined = true
}
