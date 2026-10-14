//go:build darwin || linux

package localauthority

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
	"golang.org/x/sys/unix"
)

func launchPair(t *testing.T) (*net.UnixConn, *net.UnixConn) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal("isolated bootstrap socketpair failed")
	}
	var conns []*net.UnixConn
	for _, fd := range fds {
		f := os.NewFile(uintptr(fd), "synthetic-bootstrap-role")
		c, err := net.FileConn(f)
		_ = f.Close()
		if err != nil {
			t.Fatal("isolated bootstrap role conversion failed")
		}
		u, ok := c.(*net.UnixConn)
		if !ok {
			c.Close()
			t.Fatal("bootstrap role is not Unix")
		}
		conns = append(conns, u)
		t.Cleanup(func() { _ = u.Close() })
	}
	return conns[0], conns[1]
}

func launchFixture(t *testing.T) (*NativePreparation, *os.File, *Registry, string) {
	t.Helper()
	p, registry, dir := preparedFixture(t)
	launch, err := p.PrepareLaunch(context.Background(), []ProjectBinding{{testProject, dir}})
	if err != nil {
		t.Fatal("existing launch preparation failed")
	}
	t.Cleanup(func() { _ = launch.Close() })
	file, err := launch.Descriptor()
	if err != nil {
		t.Fatal("launch descriptor duplication failed")
	}
	t.Cleanup(func() { _ = file.Close() })
	return launch, file, registry, dir
}

func TestNativeLaunchPreparationTransfersFullExistingCohortAndReplays(t *testing.T) {
	launch, file, registry, dir := launchFixture(t)
	parent, child := launchPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sent := make(chan error, 1)
	go func() { sent <- launch.Send(ctx, parent) }()
	received, err := ReceiveLaunch(ctx, file, child)
	if err != nil {
		t.Fatalf("kernel-authenticated receive refused: %v", err)
	}
	t.Cleanup(func() { _ = received.Close(context.Background()) })
	// Receive only inspects. It must not take either writer lease.
	probe, err := OpenWriter(ctx, registry, dir, testProject)
	if err != nil {
		t.Fatal("bootstrap inspection acquired writer ownership")
	}
	_ = probe.Close()
	core, err := received.Activate(context.Background(), ctx)
	if err != nil {
		t.Fatal("validated launch activation failed")
	}
	if err := <-sent; err != nil {
		t.Fatal("parent did not receive bound ACTIVE")
	}
	if _, err := received.Activate(context.Background(), ctx); err == nil {
		t.Fatal("received launch reused")
	}
	if launch.Send(ctx, parent) == nil {
		t.Fatal("parent preparation reused")
	}
	if _, err := launch.Descriptor(); err == nil {
		t.Fatal("used preparation exported another descriptor")
	}
	first, err := core.Mutate(ctx, request(t, "issue.create", "synthetic-launched-key", authority.IssuePayload{Title: "Launched source", State: "open"}), testActor)
	if err != nil {
		t.Fatal("activated existing core did not mutate")
	}
	unknown := authority.UUID("22222222-2222-4222-8222-222222222222")
	if _, err := received.prepared.OpenWriter(ctx, dir, unknown); err == nil {
		t.Fatal("transferred cohort widened")
	}
	cancelled, stopClose := context.WithCancel(context.Background())
	stopClose()
	_ = received.Close(cancelled)
	if core.Available() {
		t.Fatal("cancelled-budget close failed to seal admission")
	}
	if _, err := core.Mutate(ctx, request(t, "issue.create", "synthetic-after-close", authority.IssuePayload{Title: "Must refuse", State: "open"}), testActor); err == nil {
		t.Fatal("expired close permitted fresh authority admission")
	}
	if err := received.Close(ctx); err != nil {
		t.Fatal("activated runtime did not join")
	}
	writer, err := OpenWriter(ctx, registry, dir, testProject)
	if err != nil {
		t.Fatal("joined launch retained writer ownership")
	}
	defer writer.Close()
	replayed, err := writer.Mutate(ctx, request(t, "issue.create", "synthetic-launched-key", authority.IssuePayload{Title: "Launched source", State: "open"}), testActor)
	if err != nil || !replayed.Replayed || replayed.Citation != first.Citation {
		t.Fatal("launch lifecycle lost exact confirmed intent")
	}
}

func TestNativeLaunchFrameMismatchRefusesBeforeWriter(t *testing.T) {
	for _, tamper := range []string{"challenge", "profile", "root", "database", "manifest", "instance", "physical", "duplicate", "observation"} {
		t.Run(tamper, func(t *testing.T) {
			launch, file, registry, dir := launchFixture(t)
			parent, child := launchPair(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				var message launchMessage
				if readLaunch(parent, &message) != nil {
					return
				}
				f := launch.frame
				f.Projects = append([]launchProject(nil), f.Projects...)
				f.Challenge = message.Challenge
				switch tamper {
				case "challenge":
					f.Challenge = testProject
				case "profile":
					f.Profile = "loopback_embeddings"
				case "root":
					f.Root = dir
				case "database":
					f.Projects[0].Database = hash([]byte("other database"))
				case "manifest":
					f.Projects[0].Manifest = hash([]byte("other manifest"))
				case "instance":
					f.Projects[0].Instance = testProject
				case "physical":
					f.Projects[0].Physical = hash([]byte("other directory"))
				case "duplicate":
					f.Projects = append(f.Projects, f.Projects[0])
				case "observation":
					f.Observation = "unsupported_observation_protocol"
				}
				_ = writeLaunch(parent, f)
			}()
			if r, err := ReceiveLaunch(ctx, file, child); err == nil {
				r.Close(ctx)
				t.Fatal("mismatched preparation admitted")
			}
			<-done
			writer, err := OpenWriter(ctx, registry, dir, testProject)
			if err != nil {
				t.Fatal("refused preparation changed ownership")
			}
			_ = writer.Close()
		})
	}
}

func TestNativeLaunchExistingOnlyIntakeAndPhysicalChange(t *testing.T) {
	p, registry, dir := preparedFixture(t)
	for _, bindings := range [][]ProjectBinding{nil, {{testProject, dir}, {testProject, dir}}, {{testProject, privateTestDirectory(t)}}} {
		if launch, err := p.PrepareLaunch(context.Background(), bindings); err == nil {
			launch.Close()
			t.Fatal("invalid or empty authority prepared")
		}
	}
	launch, err := p.PrepareLaunch(context.Background(), []ProjectBinding{{testProject, dir}})
	if err != nil {
		t.Fatal("valid existing preparation failed")
	}
	defer launch.Close()
	file, err := launch.Descriptor()
	if err != nil {
		t.Fatal("valid existing role failed")
	}
	defer file.Close()
	parent, child := launchPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	sent := make(chan error, 1)
	go func() { sent <- launch.Send(ctx, parent) }()
	received, err := ReceiveLaunch(ctx, file, child)
	if err != nil {
		t.Fatal("valid bootstrap receive failed")
	}
	defer received.Close(context.Background())
	// Replace the directory around the very same database inode. The original
	// global record would otherwise still match; transferred physical binding
	// must reject this before opening the writer.
	old := dir + "-synthetic-displaced"
	if os.Rename(dir, old) != nil || os.Mkdir(dir, 0700) != nil {
		t.Fatal("physical replacement fixture failed")
	}
	entries, err := os.ReadDir(old)
	if err != nil {
		t.Fatal("physical replacement enumeration failed")
	}
	for _, entry := range entries {
		if os.Rename(filepath.Join(old, entry.Name()), filepath.Join(dir, entry.Name())) != nil {
			t.Fatal("physical replacement move failed")
		}
	}
	if core, err := received.Activate(context.Background(), ctx); err == nil {
		core.Close(ctx)
		t.Fatal("changed physical directory activated")
	}
	_ = child.Close()
	if <-sent == nil {
		t.Fatal("failed child claimed ACTIVE")
	}
	writer, err := OpenWriter(context.Background(), registry, dir, testProject)
	if err != nil {
		t.Fatal("refused activation changed full owner record")
	}
	_ = writer.Close()
}

func TestNativeLaunchWireClosedAndBounded(t *testing.T) {
	var m launchMessage
	for _, body := range [][]byte{[]byte(`{"schema":1,"schema":1}`), []byte(`{"unexpected":"value"}`), []byte(`{} {}`)} {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], uint32(len(body)))
		if readLaunch(bytes.NewReader(append(header[:], body...)), &m) == nil {
			t.Fatal("open or duplicate/suffix frame accepted")
		}
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], launchFrameLimit+1)
	if readLaunch(bytes.NewReader(header[:]), &m) == nil {
		t.Fatal("oversize frame accepted")
	}
	parent, child := launchPair(t)
	launch, _, _, _ := launchFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := launch.Send(ctx, parent); err == nil || time.Since(started) > time.Second {
		t.Fatal("silent handshake unbounded or succeeded")
	}
	_ = child.Close()
	if _, err := launch.Descriptor(); err == nil {
		t.Fatal("cancelled preparation was reusable")
	}
	var typed authority.Error
	if _, _, err := launchHandshake(context.Background(), nil); !errors.As(err, &typed) || typed.Code != authority.PolicyDenied {
		t.Fatal("missing native channel did not fail closed")
	}
}

func TestNativeLaunchCloseDuringReadyCannotAcknowledgeActive(t *testing.T) {
	launch, file, registry, dir := launchFixture(t)
	parent, child := launchPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ready := make(chan launchFrame, 1)
	proceed := make(chan struct{})
	sender := make(chan bool, 1)
	go func() {
		var challenge launchMessage
		if readLaunch(parent, &challenge) != nil {
			sender <- false
			return
		}
		f := launch.frame
		f.Challenge = challenge.Challenge
		if writeLaunch(parent, f) != nil || expectLaunch(parent, f, launchBinding(f), "ready") != nil {
			sender <- false
			return
		}
		ready <- f
		<-proceed
		_ = writeLaunch(parent, launchReply(f, launchBinding(f), "activate"))
		sender <- expectLaunch(parent, f, launchBinding(f), "active") == nil
	}()
	received, err := ReceiveLaunch(ctx, file, child)
	if err != nil {
		t.Fatal("close-during-READY fixture receive failed")
	}
	defer received.Close(context.Background())
	activated := make(chan error, 1)
	go func() { _, err := received.Activate(context.Background(), ctx); activated <- err }()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("actual READY transition was not reached")
	}
	expired, stop := context.WithCancel(context.Background())
	stop()
	_ = received.Close(expired)
	close(proceed)
	if <-sender {
		t.Fatal("closed runtime falsely acknowledged ACTIVE")
	}
	if <-activated == nil {
		t.Fatal("closed runtime falsely returned an active core")
	}
	if received.Close(ctx) != nil {
		t.Fatal("closed pending activation did not join")
	}
	writer, err := OpenWriter(ctx, registry, dir, testProject)
	if err != nil {
		t.Fatal("failed activation retained ownership after joined close")
	}
	_ = writer.Close()
}
