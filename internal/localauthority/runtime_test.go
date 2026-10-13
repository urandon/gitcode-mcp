package localauthority

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"gitcode-mcp/internal/authority"
	"gitcode-mcp/internal/privateipc"
)

func runtimeFixture(t *testing.T) (*Runtime, *Registry, string, string) {
	t.Helper()
	s, registry, directory := testStore(t)
	if s.Close() != nil {
		t.Fatal("runtime authority fixture did not close")
	}
	control, err := os.MkdirTemp("", "r-")
	if err != nil {
		t.Fatal("runtime control fixture unavailable")
	}
	t.Cleanup(func() { _ = os.RemoveAll(control) })
	control, err = filepath.EvalSymlinks(control)
	if err != nil {
		t.Fatal("runtime control fixture cannot canonicalize")
	}
	registration := authority.Registration{StorageRef: "test-store", Writer: "coordinator", Backup: authority.BackupPolicy{Retain: 7, MaxBytes: 1 << 20}, Indexing: authority.IndexPolicy{IntervalSeconds: 900, MaxChunks: 128, EmbeddingProfile: "disabled"}}
	r, err := NewRuntime(RuntimeOptions{Registry: registry, Directory: control, NetworkState: "zero_network", Projects: []RuntimeProject{{Manifest: testManifest(), Registration: registration, Directory: directory}}})
	if err != nil {
		t.Fatal("runtime intent fixture invalid")
	}
	return r, registry, directory, control
}

func runtimeClient(t *testing.T, ctx context.Context, control string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := privateipc.Dial(ctx, control)
		if err == nil {
			return conn
		}
		if ctx.Err() != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("runtime private endpoint not ready")
	return nil
}

func TestRuntimeOwnsGlobalWriterUntilHandlersJoin(t *testing.T) {
	r, registry, directory, control := runtimeFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	entered, release := make(chan struct{}), make(chan struct{})
	var released sync.Once
	unblock := func() { released.Do(func() { close(release) }) }
	done := make(chan error, 1)
	go func() { done <- r.Serve(ctx, func(context.Context, net.Conn) { close(entered); <-release }) }()
	t.Cleanup(func() { cancel(); unblock() })
	client := runtimeClient(t, ctx, control)
	defer client.Close()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("runtime handler not admitted")
	}
	if second, err := OpenWriter(context.Background(), registry, directory, testProject); err == nil {
		_ = second.Close()
		t.Fatal("runtime did not own global project writer")
	}
	cancel()
	select {
	case <-done:
		t.Fatal("runtime released ownership while handler still running")
	case <-time.After(20 * time.Millisecond):
	}
	if second, err := OpenWriter(context.Background(), registry, directory, testProject); err == nil {
		_ = second.Close()
		t.Fatal("shutdown released active handler writer")
	}
	unblock()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("runtime cancellation identity missing")
		}
	case <-time.After(time.Second):
		t.Fatal("runtime shutdown did not join")
	}
	writer, err := OpenWriter(context.Background(), registry, directory, testProject)
	if err != nil {
		t.Fatal("joined runtime retained writer lease")
	}
	_ = writer.Close()
	if _, err := os.Lstat(filepath.Join(control, "control.sock")); !os.IsNotExist(err) {
		t.Fatal("joined runtime retained owned socket")
	}
}

func TestRuntimeEnrollmentFailureNeverPublishesSocket(t *testing.T) {
	r, registry, directory, control := runtimeFixture(t)
	held, err := OpenWriter(context.Background(), registry, directory, testProject)
	if err != nil {
		t.Fatal("contention fixture cannot own writer")
	}
	defer held.Close()
	err = r.Serve(context.Background(), func(context.Context, net.Conn) { t.Error("failed enrollment admitted handler") })
	var code authority.Error
	if !errors.As(err, &code) || code.Code != authority.Conflict {
		t.Fatal("writer contention not refused")
	}
	files, err := os.ReadDir(control)
	if err != nil || len(files) != 0 {
		t.Fatal("failed enrollment published socket/owner state")
	}
}

func TestRuntimeLaterEnrollmentFailureReleasesEarlierWriter(t *testing.T) {
	r, registry, directory, control := runtimeFixture(t)
	options := r.options
	second := options.Projects[0]
	second.Manifest.ID = "22222222-2222-4222-8222-222222222222"
	second.Registration.StorageRef = "second-store"
	second.Directory = privateTestDirectory(t) // no tracker; never implicit init.
	options.Projects = append(append([]RuntimeProject(nil), options.Projects...), second)
	next, err := NewRuntime(options)
	if err != nil {
		t.Fatal("multi-project intent fixture invalid")
	}
	if err := next.Serve(context.Background(), func(context.Context, net.Conn) { t.Error("partial enrollment admitted handler") }); err == nil {
		t.Fatal("missing later authority silently initialized")
	}
	files, _ := os.ReadDir(control)
	if len(files) != 0 {
		t.Fatal("partial enrollment published socket")
	}
	files, _ = os.ReadDir(second.Directory)
	if len(files) != 0 {
		t.Fatal("missing authority was initialized")
	}
	first, err := OpenWriter(context.Background(), registry, directory, testProject)
	if err != nil {
		t.Fatal("partial failure retained earlier writer")
	}
	_ = first.Close()
}

func TestRuntimeRegistryReplacementRefusesBeforeOpening(t *testing.T) {
	r, registry, _, control := runtimeFixture(t)
	old := registry.root + ".retained"
	if os.Rename(registry.root, old) != nil || os.Mkdir(registry.root, 0700) != nil {
		t.Fatal("registry replacement fixture unavailable")
	}
	t.Cleanup(func() { _ = os.RemoveAll(old) })
	err := r.Serve(context.Background(), func(context.Context, net.Conn) { t.Error("replacement registry admitted handler") })
	var code Error
	if !errors.As(err, &code) || code.Code != Corrupt {
		t.Fatal("registry replacement not fenced")
	}
	files, _ := os.ReadDir(control)
	if len(files) != 0 {
		t.Fatal("replacement registry startup published socket")
	}
}

func TestRuntimePhysicalAliasesRefuseBeforeAnyWriterEffects(t *testing.T) {
	for _, cohort := range []string{"endpoint-project", "registry-project", "project-project"} {
		t.Run(cohort, func(t *testing.T) {
			r, registry, directory, control := runtimeFixture(t)
			options := r.options
			options.Projects = append([]RuntimeProject(nil), options.Projects...)
			// An ancestor symlink is accepted by privateDirectory, unlike a leaf
			// symlink. It is invisible to lexical overlap checks in NewRuntime.
			parent := privateTestDirectory(t)
			alias := filepath.Join(parent, "alias")
			target := filepath.Dir(directory)
			switch cohort {
			case "endpoint-project":
				options.Directory = directory
				options.Projects[0].Directory = filepath.Join(alias, filepath.Base(directory))
			case "registry-project":
				target = filepath.Dir(registry.root)
				options.Projects[0].Directory = filepath.Join(alias, filepath.Base(registry.root))
			case "project-project":
				second := options.Projects[0]
				second.Manifest.ID = "22222222-2222-4222-8222-222222222222"
				second.Registration.StorageRef = "second-store"
				second.Directory = filepath.Join(alias, filepath.Base(directory))
				options.Projects = append(options.Projects, second)
			}
			if os.Symlink(target, alias) != nil {
				t.Fatal("physical alias fixture unavailable")
			}
			next, err := NewRuntime(options)
			if err != nil {
				t.Fatal("synthetic lexical intent unexpectedly refused")
			}
			// The first owner record is deliberately removed by this isolated
			// fixture after releasing its lease. A writer admission would recreate
			// it even if a later project then failed to open.
			owner := filepath.Join(registry.root, string(testProject)+".owner")
			if os.Remove(owner) != nil {
				t.Fatal("no-effects fixture unavailable")
			}
			before, err := os.ReadFile(filepath.Join(directory, "tracker.db"))
			if err != nil {
				t.Fatal("authority fixture unavailable")
			}
			err = next.Serve(context.Background(), func(context.Context, net.Conn) { t.Error("physical alias admitted handler") })
			var code authority.Error
			if !errors.As(err, &code) || code.Code != authority.Conflict {
				t.Fatal("physical namespace overlap not refused")
			}
			if _, err := os.Lstat(owner); !os.IsNotExist(err) {
				t.Fatal("alias refusal happened after writer ownership effects")
			}
			after, err := os.ReadFile(filepath.Join(directory, "tracker.db"))
			if err != nil || string(before) != string(after) {
				t.Fatal("alias admission modified authority")
			}
			for _, root := range []string{control, directory, registry.root} {
				for _, name := range []string{"runtime.owner", "control.sock"} {
					if _, err := os.Lstat(filepath.Join(root, name)); !os.IsNotExist(err) {
						t.Fatal("alias admission published endpoint state")
					}
				}
			}
		})
	}
}

func TestRuntimePureAdmissionAndCopiedIntent(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("runtime persistence unqualified")
	}
	r, registry, _, _ := runtimeFixture(t)
	base := r.options
	for _, field := range []string{"registry", "network", "duplicate", "zero-id", "remote", "storage-ref", "directory", "authority overlap", "registry overlap"} {
		t.Run(field, func(t *testing.T) {
			candidate := base
			candidate.Projects = append([]RuntimeProject(nil), base.Projects...)
			switch field {
			case "registry":
				candidate.Registry = nil
			case "network":
				candidate.NetworkState = "loopback_control_only"
			case "duplicate":
				candidate.Projects = append(candidate.Projects, candidate.Projects[0])
			case "zero-id":
				candidate.Projects[0].Manifest.ID = ""
			case "remote":
				candidate.Projects[0].Manifest.Authority = authority.Remote
			case "storage-ref":
				candidate.Projects[0].Registration.StorageRef = "synthetic/private"
			case "directory":
				candidate.Directory = "relative"
			case "authority overlap":
				candidate.Directory = filepath.Join(candidate.Projects[0].Directory, "runtime")
			case "registry overlap":
				candidate.Directory = candidate.Registry.root
			}
			if _, err := NewRuntime(candidate); err == nil {
				t.Fatal("invalid isolated runtime intent admitted")
			}
		})
	}
	candidate := base
	candidate.Registry = registry
	candidate.Projects = append([]RuntimeProject(nil), base.Projects...)
	candidate.Projects[0].Manifest.Entities = append([]authority.EntityKind(nil), candidate.Projects[0].Manifest.Entities...)
	next, err := NewRuntime(candidate)
	if err != nil {
		t.Fatal("valid copied intent refused")
	}
	candidate.Projects[0].Manifest.Entities[0] = authority.Document
	if next.options.Projects[0].Manifest.Entities[0] != authority.Issue {
		t.Fatal("runtime retained mutable caller intent")
	}
}
