//go:build darwin || linux

package localauthority_test

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
)

const nativeTraceByteLimit = 2 << 20
const nativeTraceLineLimit = 16 << 10
const nativeTraceProcessLimit = 128

type nativePendingCall struct {
	call                 string
	inet, datagram, unix bool
}

type nativeTraceEvidence struct {
	Exec, Exit, Partial                             bool
	INETSocket, INETOperation, UnixConnect          int
	Listen, Connect, DatagramSocket, Send, RoleRead int
	ForbiddenBirth                                  int
}

// Test-only bounded streaming evidence. Raw lines are transient parser input,
// never retained diagnostic data, errors, fixtures, stdout or disk artifacts.
// Counts represent attempts, including syscalls that failed in the kernel.
type nativeNetworkTrace struct {
	mu        sync.Mutex
	partial   []byte
	bytes     int
	root      int
	evidence  nativeTraceEvidence
	onExec    func(int) error
	pending   map[int]nativePendingCall
	processes map[int]bool // finite observed IDs; true only after exit evidence
}

func (o *nativeNetworkTrace) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.evidence.Partial || len(p) > nativeTraceByteLimit-o.bytes {
		o.evidence.Partial = true
		return 0, errors.New("native observation exceeds its finite boundary")
	}
	o.bytes += len(p)
	for _, b := range p {
		if b != '\n' {
			if len(o.partial) >= nativeTraceLineLimit {
				o.evidence.Partial = true
				o.partial = nil
				return 0, errors.New("native observation exceeds its finite boundary")
			}
			o.partial = append(o.partial, b)
			continue
		}
		o.line(o.partial)
		clear(o.partial)
		o.partial = o.partial[:0]
	}
	return len(p), nil
}

func (o *nativeNetworkTrace) line(raw []byte) {
	s := strings.TrimSpace(string(raw))
	space := strings.IndexByte(s, ' ')
	if space < 1 {
		o.evidence.Partial = true
		return
	}
	pid, err := strconv.Atoi(s[:space])
	if err != nil || pid < 1 {
		o.evidence.Partial = true
		return
	}
	body := strings.TrimSpace(s[space+1:])
	if o.processes == nil {
		o.processes = make(map[int]bool)
		o.pending = make(map[int]nativePendingCall)
	}
	if _, exists := o.processes[pid]; !exists {
		if len(o.processes) >= nativeTraceProcessLimit {
			o.evidence.Partial = true
			return
		}
		o.processes[pid] = false
	} else if o.processes[pid] {
		o.evidence.Partial = true
		return
	}
	if strings.HasPrefix(body, "+++") {
		if _, waiting := o.pending[pid]; waiting || body != "+++ exited with 0 +++" {
			o.evidence.Partial = true
		}
		o.processes[pid] = true
		if pid == o.root && body == "+++ exited with 0 +++" {
			o.evidence.Exit = true
		}
		return
	}
	if strings.HasPrefix(body, "---") {
		return // signal notification, not a syscall entry
	}
	if strings.HasPrefix(body, "<...") {
		end := strings.Index(body, " resumed>")
		pending, waiting := o.pending[pid]
		if end < 5 || !waiting || body[5:end] != pending.call || !strings.Contains(body[end+9:], " = ") {
			o.evidence.Partial = true
			return
		}
		o.classify(&pending, body[end+9:])
		delete(o.pending, pid)
		return
	}
	open := strings.IndexByte(body, '(')
	if open < 1 {
		o.evidence.Partial = true
		return
	}
	call, args := body[:open], body[open+1:]
	if _, waiting := o.pending[pid]; waiting {
		o.evidence.Partial = true
		return
	}
	if call == "execve" || call == "execveat" {
		if !o.evidence.Exec {
			o.root, o.evidence.Exec = pid, true
			if o.onExec != nil && o.onExec(pid) != nil {
				o.evidence.Partial = true
			}
		} else {
			// The exact strict entry does not delegate its graph to another
			// executable; unknown extra exec is not silently out of scope.
			o.evidence.Partial = true
		}
	} else if !o.evidence.Exec {
		o.evidence.Partial = true
		return
	}
	switch call {
	case "clone", "clone3":
		// A strict Go runtime may create threads, not delegate an unobserved
		// process graph. UNTRACED and non-thread creation are refused even if
		// the syscall itself fails; births can never disappear from evidence.
		if !nativeThreadFlagsQualified(args) {
			o.evidence.ForbiddenBirth++
			o.evidence.Partial = true
		}
	case "fork", "vfork":
		o.evidence.ForbiddenBirth++
		o.evidence.Partial = true
	case "socket", "socketpair", "execve", "execveat":
	case "connect":
		o.evidence.Connect++
	case "bind", "sendto", "sendmsg", "sendmmsg", "recvfrom", "recvmsg", "recvmmsg":
		if strings.HasPrefix(call, "send") {
			o.evidence.Send++
		}
	case "listen":
		o.evidence.Listen++
	case "getsockname", "getpeername", "getsockopt":
		o.evidence.RoleRead++
	case "accept", "accept4", "shutdown", "setsockopt":
	default:
		o.evidence.Partial = true // changed observer grammar is unavailable
	}
	pending := nativePendingCall{call: call}
	o.classify(&pending, args)
	if strings.HasSuffix(body, "<unfinished ...>") {
		o.pending[pid] = pending
	} else if !strings.Contains(body, " = ") {
		o.evidence.Partial = true
	}
}

func nativeThreadFlagsQualified(args string) bool {
	start := strings.Index(args, "flags=")
	if start < 0 {
		return false
	}
	flags := args[start+6:]
	if end := strings.IndexAny(flags, ",} )"); end >= 0 {
		flags = flags[:end]
	}
	thread := false
	for _, flag := range strings.Split(flags, "|") {
		switch flag {
		case "CLONE_THREAD":
			thread = true
		case "CLONE_VM", "CLONE_FS", "CLONE_FILES", "CLONE_SIGHAND", "CLONE_SYSVSEM", "CLONE_SETTLS", "CLONE_PARENT_SETTID", "CLONE_CHILD_SETTID", "CLONE_CHILD_CLEARTID":
		default:
			return false
		}
	}
	return thread
}

func (o *nativeNetworkTrace) classify(p *nativePendingCall, args string) {
	inet := strings.Contains(args, "AF_INET")
	if (p.call == "socket" || p.call == "socketpair") && inet {
		if !p.inet {
			o.evidence.INETSocket++
			p.inet = true
		}
		if strings.Contains(args, "SOCK_DGRAM") && !p.datagram {
			o.evidence.DatagramSocket++
			p.datagram = true
		}
	}
	switch p.call {
	case "connect", "bind", "sendto", "sendmsg", "sendmmsg", "recvfrom", "recvmsg", "recvmmsg", "accept", "accept4", "getsockname", "getpeername":
		if inet && !p.inet {
			o.evidence.INETOperation++
			p.inet = true
		}
	}
	if p.call == "connect" && !p.unix && (strings.Contains(args, "AF_UNIX") || strings.Contains(args, "AF_LOCAL")) {
		o.evidence.UnixConnect++
		p.unix = true
	}
}

func (o *nativeNetworkTrace) snapshot(final bool) nativeTraceEvidence {
	o.mu.Lock()
	defer o.mu.Unlock()
	e := o.evidence
	e.Partial = e.Partial || final && (len(o.partial) != 0 || !e.Exec || !e.Exit)
	if final {
		e.Partial = e.Partial || len(o.pending) != 0
		for _, exited := range o.processes {
			e.Partial = e.Partial || !exited
		}
	}
	return e
}

func TestNativeNetworkTraceStreamingBoundsAndAttemptEvidence(t *testing.T) {
	if !nativeThreadFlagsQualified("child_stack=NULL, flags=CLONE_VM|CLONE_THREAD|CLONE_SETTLS, tls=NULL") {
		t.Fatal("known runtime thread flags refused")
	}
	for _, flags := range []string{"flags=CLONE_THREAD|CLONE_UNTRACED", "flags=CLONE_THREAD|0x100", "flags=CLONE_THREAD|CLONE_NEWNET", "flags=CLONE_VM"} {
		if nativeThreadFlagsQualified(flags) {
			t.Fatal("unqualified process creation flags accepted")
		}
	}
	// Synthetic public-safe syscall grammar, not captured provider data. The
	// same streaming parser consumes the real observer in opt-in qualification.
	input := "101 execve(\"synthetic-program\", [], []) = 0\n" +
		"101 socket(AF_INET6, SOCK_DGRAM, 0) = -1 EPERM\n" +
		"102 connect(7, {sa_family=AF_UNIX}, 8 <unfinished ...>\n" +
		"102 <... connect resumed>) = 0\n" +
		"101 listen(7, 4) = 0\n" +
		"101 sendto(7, \"\", 0, 0, {sa_family=AF_INET}, 16) = 0\n" +
		"101 getsockopt(4, SOL_SOCKET, SO_ACCEPTCONN, [1], [4]) = 0\n" +
		"102 +++ exited with 0 +++\n" +
		"101 +++ exited with 0 +++\n"
	var o nativeNetworkTrace
	for _, b := range []byte(input) {
		if _, err := o.Write([]byte{b}); err != nil {
			t.Fatal("finite native trace fragment refused")
		}
	}
	for _, entry := range []string{
		"101 recvfrom(7, <unfinished ...>\n101 <... recvfrom resumed>\"\", 0, 0, {sa_family=AF_INET}, [16]) = 0\n",
		"101 socketpair(AF_INET, SOCK_STREAM, 0, [7, 8]) = -1 EPERM\n",
	} {
		var attempted nativeNetworkTrace
		_, _ = attempted.Write([]byte("101 execve(\"synthetic-program\", [], []) = 0\n" + entry + "101 +++ exited with 0 +++\n"))
		e := attempted.snapshot(true)
		if e.Partial || e.INETOperation+e.INETSocket != 1 {
			t.Fatal("native continuation or socketpair attempt was lost")
		}
	}
	for _, entry := range []string{
		"101 <... connect resumed>) = 0\n",
		"101 connect(7, <unfinished ...>\n101 <... bind resumed>) = 0\n",
		"102 socket(AF_UNIX, SOCK_STREAM, 0) = 7\n",
		"102 +++ killed by SIGKILL +++\n",
		"101 execveat(7, \"synthetic-program\", [], [], 0) = 0\n",
		"101 clone(flags=CLONE_VM|CLONE_UNTRACED) = 102\n",
		"101 clone3({flags=CLONE_VM}, 88) = -1 ENOSYS\n",
		"101 fork() = 102\n",
	} {
		var incomplete nativeNetworkTrace
		_, _ = incomplete.Write([]byte("101 execve(\"synthetic-program\", [], []) = 0\n" + entry + "101 +++ exited with 0 +++\n"))
		if !incomplete.snapshot(true).Partial {
			t.Fatal("unmatched continuation or incomplete process tree became a pass")
		}
	}
	e := o.snapshot(true)
	if e.Partial || !e.Exec || !e.Exit || e.INETSocket != 1 || e.DatagramSocket != 1 || e.UnixConnect != 1 || e.Connect != 1 || e.Listen != 1 || e.Send != 1 || e.INETOperation != 1 || e.RoleRead != 1 {
		t.Fatal("native trace fabricated, lost or duplicated syscall attempts")
	}
	if bytes.Contains(o.partial, []byte("synthetic-program")) {
		t.Fatal("native trace retained consumed raw input")
	}
	for _, malformed := range []string{"", "101 execve(\"synthetic-program\", [], []) = 0", "unexpected-native-body\n", "101 unknown_call() = 0\n"} {
		var bad nativeNetworkTrace
		_, _ = bad.Write([]byte(malformed))
		if !bad.snapshot(true).Partial {
			t.Fatal("missing/truncated/unknown observation became a zero-network pass")
		}
	}
	var overflow nativeNetworkTrace
	_, err := overflow.Write(bytes.Repeat([]byte{'x'}, nativeTraceLineLimit+1))
	if err == nil || !overflow.snapshot(true).Partial || strings.Contains(err.Error(), "xxxx") {
		t.Fatal("oversize native trace was not bounded and content-free")
	}
}
