//go:build darwin

package nativelaunch

import (
	"context"
	"os"
	"runtime"
	"unsafe"

	"gitcode-mcp/internal/authority"
	"golang.org/x/sys/unix"
)

// Read-only ABI from Apple's XNU codesign.h/cs_blobs.h/kern_proc.c. Unlike the
// path-based libproc query, the audited calls bind the pidversion and text vnode.
// They do not prove that a LOCAL_PEERTOKEN belongs to one specific message.
func readDarwinCode(token [8]uint32, op uint32, out []byte) error {
	if token[5] == 0 || token[5] > (1<<31)-1 || token[7] == 0 || op != darwinStatus && op != darwinHashInfo || op == darwinStatus && len(out) != 4 || op == darwinHashInfo && len(out) != 21 {
		return authority.Error{Code: authority.InvalidContract}
	}
	_, _, errno := unix.Syscall6(unix.SYS_CSOPS_AUDITTOKEN, uintptr(token[5]), uintptr(op), uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), uintptr(unsafe.Pointer(&token)), 0)
	runtime.KeepAlive(out)
	runtime.KeepAlive(&token)
	if errno != 0 {
		return authority.Error{Code: authority.Unavailable}
	}
	return nil
}

// Caller retains exclusive ownership of file throughout measurement. No bare
// descriptor or token becomes an enrollment capability by calling this helper.
func readDarwinPeerToken(file *os.File) ([8]uint32, error) {
	var token [8]uint32
	if file == nil {
		return token, authority.Error{Code: authority.InvalidContract}
	}
	length := uint32(32)
	_, _, errno := unix.Syscall6(unix.SYS_GETSOCKOPT, file.Fd(), unix.SOL_LOCAL, unix.LOCAL_PEERTOKEN, uintptr(unsafe.Pointer(&token)), uintptr(unsafe.Pointer(&length)), 0)
	runtime.KeepAlive(&token)
	runtime.KeepAlive(&length)
	runtime.KeepAlive(file)
	if errno != 0 || length != 32 {
		return [8]uint32{}, authority.Error{Code: authority.Unavailable}
	}
	return token, nil
}

func observeDarwinPeer(ctx context.Context, file *os.File, pid int) (darwinImage, error) {
	if os.Getuid() != os.Geteuid() {
		return darwinImage{}, authority.Error{Code: authority.PolicyDenied}
	}
	return sampleDarwinImage(ctx, pid, os.Geteuid(), func() ([8]uint32, error) { return readDarwinPeerToken(file) }, readDarwinCode)
}

func revalidateDarwinPeer(ctx context.Context, file *os.File, pinned darwinImage) error {
	current, err := observeDarwinPeer(ctx, file, int(pinned.token[5]))
	if err != nil {
		return err
	}
	if current != pinned {
		return authority.Error{Code: authority.Conflict}
	}
	return nil
}

// The independent self reference comes from a newly created owned pair, not a
// caller's socket/token, PATH or argv. It is still only a measurement primitive.
func observeDarwinSelf(ctx context.Context) (darwinImage, error) {
	if ctx.Err() != nil {
		return darwinImage{}, ctx.Err()
	}
	if os.Getuid() != os.Geteuid() {
		return darwinImage{}, authority.Error{Code: authority.PolicyDenied}
	}
	pair, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err != nil {
		return darwinImage{}, authority.Error{Code: authority.Unavailable}
	}
	unix.CloseOnExec(pair[0])
	unix.CloseOnExec(pair[1])
	left, right := os.NewFile(uintptr(pair[0]), "native-self-left"), os.NewFile(uintptr(pair[1]), "native-self-right")
	defer left.Close()
	defer right.Close()
	// Socket last-owner metadata need not be initialized by creation alone.
	// Both ends are freshly owned here; one bounded self datagram establishes
	// the current self token without trusting external message coordinates.
	if n, err := unix.Write(pair[1], []byte{1}); err != nil || n != 1 {
		return darwinImage{}, authority.Error{Code: authority.Unavailable}
	}
	var data [1]byte
	if n, err := unix.Read(pair[0], data[:]); err != nil || n != 1 || data[0] != 1 {
		return darwinImage{}, authority.Error{Code: authority.Unavailable}
	}
	return observeDarwinPeer(ctx, left, os.Getpid())
}
