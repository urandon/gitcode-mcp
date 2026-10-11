package servicectl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gitcode-mcp/internal/cache"
	"gitcode-mcp/internal/config"
	"gitcode-mcp/internal/observability"
)

// This adapter is test-only. Manager's fixed production targets MUST NOT reach
// the OS: a temporary Source alone does not isolate launchd/systemd commands.
type nativeObservationFixture struct {
	manager                 Manager
	paths                   Paths
	root, label, definition string
	env                     []string
	calls                   int
	linked, interrupt       bool
	trace                   []string // finite operation/outcome codes, never args or OS bodies
}

func (f *nativeObservationFixture) mapCommand(name string, args []string) ([]string, bool, error) {
	eq := func(want ...string) bool { return reflect.DeepEqual(args, want) }
	domain := fmt.Sprintf("gui/%d", os.Getuid())
	canonical := domain + "/com.gitcode.gitcode-mcp"
	if f.paths.InstallKind == "launchagent" && name == "launchctl" {
		switch {
		case eq("print", canonical), eq("bootout", canonical):
			return []string{args[0], domain + "/" + f.label}, false, nil
		case eq("kickstart", "-k", canonical):
			return []string{"kickstart", "-k", domain + "/" + f.label}, false, nil
		case eq("bootstrap", domain, f.paths.InstallPath):
			return []string{"bootstrap", domain, f.definition}, true, nil
		}
	}
	if f.paths.InstallKind == "systemd-user" && name == "systemctl" {
		switch {
		case eq("--user", "daemon-reload"):
			return args, true, nil
		case eq("--user", "start", "gitcode-mcp.service"), eq("--user", "stop", "gitcode-mcp.service"):
			return []string{"--user", args[1], f.label}, false, nil
		}
	}
	return nil, false, errors.New("native fixture refused unexpected platform command")
}

// Output is bounded and held in memory only. Neither OS bodies nor paths are
// emitted by the test, including on a prerequisite or platform failure.
type nativeOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *nativeOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := (64 << 10) - b.Len()
	if n > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}
func nativeCommand(ctx context.Context, name string, args ...string) (string, error) {
	// Do not permit a machine PATH wrapper to receive platform authority.
	var executable string
	switch name {
	case "launchctl":
		executable = "/bin/launchctl"
	case "systemctl":
		executable = "/usr/bin/systemctl"
	default:
		return "", errors.New("native fixture refused unexpected executable")
	}
	// launchd.plist(5) specifies a default 10-second respawn throttle. Start
	// performs bootstrap then kickstart -k, so an 8-second harness budget
	// necessarily expires before a valid native respawn. Keep that production
	// behavior and native throttle intact; bound the call at 15 seconds.
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.WaitDelay = time.Second
	var out nativeOutput
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if out.overflow {
		return "", errors.New("native fixture platform output exceeded bound")
	}
	return out.String(), err
}

func (f *nativeObservationFixture) derive() error {
	b, err := os.ReadFile(f.paths.InstallPath)
	if err != nil {
		return errors.New("native fixture logical definition unavailable")
	}
	text := string(b)
	legacy := legacyInstallFileContent(f.paths.InstallKind, f.manager.BinaryPath, f.paths)
	bounded := installFileContent(f.paths.InstallKind, f.manager.BinaryPath, f.paths)
	if text != legacy && text != bounded {
		return errors.New("native fixture refused unknown definition")
	}
	// env -i prevents user-manager environment or credential inheritance. The
	// resulting process is the actual compiled daemon, not a simulated owner.
	args := append([]string{"/usr/bin/env", "-i"}, f.env...)
	args = append(args, f.manager.BinaryPath, "service", "run", "--offline")
	if f.paths.InstallKind == "launchagent" {
		text = strings.Replace(text, "<string>com.gitcode.gitcode-mcp</string>", "<string>"+f.label+"</string>", 1)
		old := "<array><string>" + escapeXMLText(f.manager.BinaryPath) + "</string><string>service</string><string>run</string></array>"
		var program strings.Builder
		program.WriteString("<array>")
		for _, arg := range args {
			program.WriteString("<string>" + escapeXMLText(arg) + "</string>")
		}
		program.WriteString("</array>")
		text = strings.Replace(text, old, program.String(), 1)
		text = strings.Replace(text, "<key>RunAtLoad</key>", "<key>WorkingDirectory</key><string>"+escapeXMLText(filepath.Join(f.root, "work"))+"</string>\n<key>RunAtLoad</key>", 1)
	} else {
		quoted := make([]string, len(args))
		for i, arg := range args {
			quoted[i] = quoteSystemdArgument(arg)
		}
		text = strings.Replace(text, "ExecStart="+quoteSystemdArgument(f.manager.BinaryPath)+" service run", "ExecStart="+strings.Join(quoted, " "), 1)
		// Never claim or remove the canonical user's RuntimeDirectory.
		// WorkingDirectory is a path setting, not an ExecStart word. Quotes
		// become part of the path and make an absolute path invalid in systemd.
		work := strings.ReplaceAll(filepath.Join(f.root, "work"), "%", "%%")
		text = strings.Replace(text, "RuntimeDirectory=gitcode-mcp\n", "WorkingDirectory="+work+"\n", 1)
	}
	// Only a bounded logical definition passes the managed-output marker to
	// the daemon. A legacy instance must still report migration_required.
	if !boundedDefinition(f.paths.InstallKind, f.paths.InstallPath) {
		text = strings.ReplaceAll(text, "GITCODE_MCP_OBSERVATION_OUTPUT=bounded-v1", "GITCODE_MCP_OBSERVATION_OUTPUT=legacy-fixture")
	}
	return writePrivateFile(f.definition, []byte(text))
}

func (f *nativeObservationFixture) run(ctx context.Context, name string, args ...string) (string, error) {
	mapped, derive, err := f.mapCommand(name, args)
	if err != nil {
		return "", err
	}
	f.calls++
	if derive {
		if err := f.derive(); err != nil {
			return "", err
		}
		if f.paths.InstallKind == "systemd-user" && !f.linked {
			// A timed-out link can have committed. Cleanup owns every attempted
			// link and must prove absence before private files can be removed.
			f.linked = true
			if _, err := nativeCommand(ctx, "systemctl", "--user", "--runtime", "link", f.definition); err != nil {
				return "", errors.New("native fixture runtime link failed")
			}
		}
	}
	starting := name == "launchctl" && mapped[0] == "bootstrap" || name == "systemctl" && len(mapped) > 1 && mapped[1] == "start"
	if starting && f.interrupt && boundedDefinition(f.paths.InstallKind, f.paths.InstallPath) {
		f.interrupt = false
		return "", errors.New("native fixture interrupted before bounded owner restart")
	}
	out, err := nativeCommand(ctx, name, mapped...)
	op := mapped[0]
	if name == "systemctl" {
		op = mapped[1]
	}
	class := "ok"
	if err != nil {
		class = "unknown"
		var exited *exec.ExitError
		if errors.Is(err, context.DeadlineExceeded) {
			class = "deadline"
		} else if errors.As(err, &exited) {
			class = "exit-" + strconv.Itoa(exited.ExitCode())
		}
	}
	f.trace = append(f.trace, op+":"+class)
	if name == "launchctl" {
		// Translate only the fixture identity in the actual diagnostic, retaining
		// the native exit status for production's strict absence classifier.
		out = strings.ReplaceAll(out, f.label, "com.gitcode.gitcode-mcp")
	}
	return out, err
}

func TestNativeObservationFixtureCommandFence(t *testing.T) {
	for _, kind := range []string{"launchagent", "systemd-user"} {
		f := nativeObservationFixture{paths: Paths{InstallKind: kind, InstallPath: "logical"}, label: "unique-fixture", definition: "derived"}
		for _, bad := range []struct {
			name string
			args []string
		}{
			{"sh", []string{"-c", "anything"}}, {"launchctl", []string{"bootout", "gui/0/other"}},
			{"launchctl", []string{"bootstrap", "gui/0", "other"}}, {"systemctl", []string{"--user", "enable", "gitcode-mcp.service"}},
			{"systemctl", []string{"--user", "stop", "other.service"}}, {"systemctl", []string{"--user", "daemon-reload", "extra"}},
		} {
			if _, _, err := f.mapCommand(bad.name, bad.args); err == nil {
				t.Fatal("unsafe command admitted")
			}
		}
		domain := fmt.Sprintf("gui/%d", os.Getuid())
		name, args := "systemctl", []string{"--user", "stop", "gitcode-mcp.service"}
		if kind == "launchagent" {
			name, args = "launchctl", []string{"bootout", domain + "/com.gitcode.gitcode-mcp"}
		}
		got, _, err := f.mapCommand(name, args)
		if err != nil || !strings.Contains(strings.Join(got, " "), f.label) || strings.Contains(strings.Join(got, " "), "com.gitcode.gitcode-mcp") || strings.Contains(strings.Join(got, " "), "gitcode-mcp.service") {
			t.Fatal("canonical target crossed fixture boundary")
		}
	}
}

func TestNativeObservationFixtureDefinitionIsolation(t *testing.T) {
	for _, goos := range []string{"darwin", "linux"} {
		t.Run(goos, func(t *testing.T) {
			m, paths := legacyService(t, goos)
			root := filepath.Dir(m.BinaryPath)
			f := nativeObservationFixture{manager: m, paths: paths, root: root, label: "unique-fixture", definition: filepath.Join(root, "derived"), env: []string{"HOME=" + root, "GITCODE_MCP_OBSERVATION_OUTPUT=bounded-v1"}}
			for _, managed := range []bool{false, true} {
				if managed && writePrivateFile(paths.InstallPath, []byte(installFileContent(paths.InstallKind, m.BinaryPath, paths))) != nil {
					t.Fatal("native definition control unavailable")
				}
				if f.derive() != nil {
					t.Fatal("native fixture derivation failed")
				}
				b, err := os.ReadFile(f.definition)
				if err != nil {
					t.Fatal("derived definition unavailable")
				}
				text := string(b)
				if strings.Contains(text, "<string>com.gitcode.gitcode-mcp</string>") || strings.Contains(text, "RuntimeDirectory=gitcode-mcp\n") || !strings.Contains(text, "--offline") || !strings.Contains(text, "/usr/bin/env") || !strings.Contains(text, "-i") || !strings.Contains(text, "HOME=") || !strings.Contains(text, "WorkingDirectory") {
					t.Fatal("derived definition lost isolation fence")
				}
				if strings.Contains(text, "GITCODE_MCP_OBSERVATION_OUTPUT=bounded-v1") != managed {
					t.Fatal("derived marker does not match logical definition")
				}
				if goos == "linux" && !strings.Contains(text, "\nWorkingDirectory="+strings.ReplaceAll(filepath.Join(root, "work"), "%", "%%")+"\n") {
					t.Fatal("native systemd working directory is not an unquoted absolute path")
				}
			}
			if writePrivateFile(paths.InstallPath, []byte("unknown definition")) != nil || f.derive() == nil {
				t.Fatal("unknown logical definition admitted")
			}
		})
	}
}

func TestNativeObservationOutputBound(t *testing.T) {
	var b nativeOutput
	p := bytes.Repeat([]byte("x"), 128<<10)
	if n, err := b.Write(p); err != nil || n != len(p) || !b.overflow || b.Len() != 64<<10 {
		t.Fatal("native output cap not enforced")
	}
	if n, err := b.Write(p); err != nil || n != len(p) || b.Len() != 64<<10 {
		t.Fatal("native output grew beyond cap")
	}
}

// Retry only the observation read's typed admission contention. No execution
// RPC, platform effect, cancellation or unknown error may be replayed here.
func nativeObservationCall(ctx context.Context, method string, call func() error) error {
	for attempt := 0; attempt < 5; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := call()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var query observability.QueryError
		if method != "Observability.Summary" || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
			!errors.As(err, &query) || query.Code != "observation_busy" || attempt == 4 {
			return err
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	panic("unreachable native observation read budget")
}

func TestNativeObservationReadRetryFence(t *testing.T) {
	busy := observability.QueryError{Code: "observation_busy", Reason: "ledger_busy"}
	for _, tc := range []struct {
		name, method string
		failure      error
		failures     int
		wantCalls    int
	}{
		{"success", "Observability.Summary", nil, 0, 1},
		{"busy_recovers", "Observability.Summary", busy, 2, 3},
		{"busy_exhausted", "Observability.Summary", busy, 9, 5},
		{"write_busy", "Jobs.StartFake", busy, 9, 1},
		{"other_read", "Jobs.List", busy, 9, 1},
		{"unavailable", "Observability.Summary", observability.QueryError{Code: "observation_unavailable"}, 9, 1},
		{"raw_text", "Observability.Summary", errors.New("observation_busy"), 9, 1},
		{"cancelled", "Observability.Summary", errors.Join(context.Canceled, busy), 9, 1},
		{"timeout", "Observability.Summary", errors.Join(context.DeadlineExceeded, busy), 9, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			err := nativeObservationCall(context.Background(), tc.method, func() error {
				calls++
				if calls <= tc.failures {
					return tc.failure
				}
				return nil
			})
			if calls != tc.wantCalls || (tc.failures < tc.wantCalls && err != nil) || (tc.failures >= tc.wantCalls && !errors.Is(err, tc.failure)) {
				t.Fatal("native read violated exact retry/result fence")
			}
		})
	}
	for _, duringRead := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if !duringRead {
			cancel()
		}
		calls := 0
		err := nativeObservationCall(ctx, "Observability.Summary", func() error {
			calls++
			cancel()
			return busy
		})
		cancel()
		want := 0
		if duringRead {
			want = 1
		}
		if calls != want || !errors.Is(err, context.Canceled) {
			t.Fatal("native read retried after cancellation")
		}
	}
}

type nativeHeldObservationRead struct {
	context.Context
	checks           atomic.Int32
	entered, release chan struct{}
}

func (c *nativeHeldObservationRead) Err() error {
	if c.checks.Add(1) == 2 {
		close(c.entered)
		select {
		case <-c.release:
		case <-c.Context.Done():
		}
	}
	return c.Context.Err()
}

func TestNativeObservationActualRPCBusyRecovery(t *testing.T) {
	collector := maintenanceEventCollector(t)
	server := RPCServer{Observability: &observability.Reader{Collector: collector}}
	client := observationTestPeer(t, func(conn net.Conn, request RPCRequest) {
		_ = json.NewEncoder(conn).Encode(server.handleRequest(context.Background(), request))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var summary observability.Summary
	if err := client.Call(ctx, "Observability.Summary", nil, &summary); err != nil {
		t.Fatal("native healthy RPC control unavailable")
	}
	held := &nativeHeldObservationRead{Context: ctx, entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() { _, err := collector.QueryContext(held, observability.Filter{}, "", 1); done <- err }()
	released, joined := false, false
	defer func() {
		if !released {
			close(held.release)
		}
		if !joined {
			select {
			case err := <-done:
				if err != nil {
					t.Error("native held reader join failed")
				}
			case <-time.After(time.Second):
				t.Error("native held reader did not join")
			}
		}
	}()
	select {
	case <-held.entered:
	case <-ctx.Done():
		t.Fatal("native held reader did not enter")
	}
	calls := 0
	err := nativeObservationCall(ctx, "Observability.Summary", func() error {
		calls++
		err := client.Call(ctx, "Observability.Summary", nil, &summary)
		if calls == 1 {
			var query observability.QueryError
			if !errors.As(err, &query) || query.Code != "observation_busy" {
				t.Fatal("actual held reader did not return typed busy")
			}
			close(held.release)
			released = true
			select {
			case joinErr := <-done:
				joined = true
				if joinErr != nil {
					t.Fatal("native released reader failed")
				}
			case <-ctx.Done():
				t.Fatal("native released reader did not join")
			}
		}
		return err
	})
	if err != nil || calls != 2 || !joined {
		t.Fatal("native actual typed RPC contention did not recover within read budget")
	}
}

type nativeFixtureSource struct {
	root, home, cache, config string
	env                       map[string]string
}

func (s nativeFixtureSource) Env(k string) string            { return s.env[k] }
func (s nativeFixtureSource) UserHomeDir() (string, error)   { return s.home, nil }
func (s nativeFixtureSource) UserCacheDir() (string, error)  { return s.cache, nil }
func (s nativeFixtureSource) UserConfigDir() (string, error) { return s.config, nil }
func (s nativeFixtureSource) WorkingDir() (string, error)    { return filepath.Join(s.root, "work"), nil }
func (s nativeFixtureSource) ReadFile(p string) ([]byte, error) {
	rel, err := filepath.Rel(s.root, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, os.ErrNotExist
	}
	return os.ReadFile(p)
}

// Explicit opt-in is required on a suitable native host. Default Go tests never
// register a service; requested qualification fails (not skips) without its bus.
func TestNativeObservationUpgradeRecovery(t *testing.T) {
	if os.Getenv("GITCODE_MCP_NATIVE_OBSERVATION_UPGRADE") != "1" {
		t.Skip("native owner qualification is explicit opt-in")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Fatal("native owner platform unsupported")
	}
	// daemon-reload is manager-wide even when all units are uniquely named.
	// Never issue it on an ordinary operator's user manager. The CI VM sets
	// this explicit disposable-manager attestation; other hosts must provide
	// an isolated test user manager before opting in.
	if runtime.GOOS == "linux" && os.Getenv("GITCODE_MCP_NATIVE_SYSTEMD_ISOLATED") != "1" {
		t.Fatal("native user systemd qualification requires an isolated disposable manager")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	// The later unique-owner absence probe also proves native bus access.
	// Do not inspect the whole GUI domain or unrelated manager environment.
	root, err := os.MkdirTemp("/tmp", "gcm-native-")
	if err != nil {
		t.Fatal("native fixture directory unavailable")
	}
	// A short explicit temporary root also keeps Unix socket names below the
	// Darwin limit. It is retained if native teardown cannot prove quiescence.
	f := &nativeObservationFixture{root: root}
	cleanupOK := true // no native owner exists before the cleanup hook is armed
	defer func() {
		if cleanupOK {
			_ = os.RemoveAll(root)
		}
	}()
	s := nativeFixtureSource{root: root, home: filepath.Join(root, "home"), env: map[string]string{}}
	s.cache, s.config = filepath.Join(s.home, ".cache"), filepath.Join(s.home, ".config")
	if runtime.GOOS == "darwin" {
		s.cache, s.config = filepath.Join(s.home, "Library", "Caches"), filepath.Join(s.home, "Library", "Application Support")
	}
	for _, dir := range []string{s.home, s.cache, s.config, filepath.Join(root, "work")} {
		if os.MkdirAll(dir, 0700) != nil {
			t.Fatal("native fixture directories unavailable")
		}
	}
	configPath, cachePath := filepath.Join(root, "config.json"), filepath.Join(root, "cache.db")
	cfgBytes, _ := json.Marshal(map[string]any{"cache_path": cachePath, "lock_path": cachePath + ".lock", "credential": map[string]string{"store": "env"}, "rag": map[string]any{"providers": map[string]any{"ollama": map[string]any{"autostart": false}}, "search": map[string]any{"hybrid": false}}, "feedback": map[string]any{"enabled": false}})
	if os.WriteFile(configPath, cfgBytes, 0600) != nil {
		t.Fatal("native fixture config unavailable")
	}
	s.env[config.EnvMCPConfigPath] = configPath
	effective, err := config.LoadEffective(s, config.Overrides{})
	if err != nil || effective.CredentialPolicy.Store != "env" || effective.Config.CachePath != cachePath || effective.Config.RAG.Providers["ollama"].Autostart {
		t.Fatal("native fixture effective isolation config invalid")
	}
	f.manager = Manager{Source: s, BinaryPath: filepath.Join(root, "daemon"), RuntimeDir: filepath.Join(root, "runtime"), GOOS: runtime.GOOS, Offline: true, StartupTimeout: 15 * time.Second}
	f.paths, err = f.manager.ResolvePaths()
	if err != nil {
		t.Fatal("native fixture paths unavailable")
	}
	f.label = "com.gitcode.gitcode-mcp.o5." + filepath.Base(root)
	if runtime.GOOS == "linux" {
		f.label = "gitcode-mcp-o5-" + filepath.Base(root) + ".service"
	}
	f.definition = filepath.Join(root, f.label+".plist")
	if runtime.GOOS == "linux" {
		f.definition = filepath.Join(root, f.label)
	}
	f.env = []string{"HOME=" + s.home, "XDG_CACHE_HOME=" + s.cache, "XDG_CONFIG_HOME=" + s.config, "PATH=/usr/bin:/bin", "GITCODE_MCP_CONFIG=" + configPath, "GITCODE_MCP_SERVICE_RUNTIME_DIR=" + f.paths.RuntimeDir, "GITCODE_MCP_OBSERVATION_OUTPUT=bounded-v1"}
	// Compile from the exact checkout BEFORE invoking this opt-in test. No
	// nested compiler process/pipe may outlive private native-fixture cleanup.
	input, err := os.Open(os.Getenv("GITCODE_MCP_NATIVE_FIXTURE_BINARY"))
	if err != nil {
		t.Fatal("native fixture requires an explicitly prebuilt daemon")
	}
	info, statErr := input.Stat()
	if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Size() > 128<<20 {
		_ = input.Close()
		t.Fatal("native prebuilt daemon invalid")
	}
	binaryBytes, readErr := io.ReadAll(io.LimitReader(input, 128<<20+1))
	closeInputErr := input.Close()
	if readErr != nil || closeInputErr != nil || len(binaryBytes) > 128<<20 || os.WriteFile(f.manager.BinaryPath, binaryBytes, 0700) != nil {
		t.Fatal("native prebuilt daemon copy failed")
	}
	if ensurePathDirs(f.paths) != nil || writePrivateFile(f.paths.InstallPath, []byte(legacyInstallFileContent(f.paths.InstallKind, f.manager.BinaryPath, f.paths))) != nil {
		t.Fatal("native legacy definition unavailable")
	}
	for _, file := range []string{"service.out.log", "service.err.log", "unrelated.txt"} {
		if os.WriteFile(filepath.Join(f.paths.LogDir, file), []byte("synthetic-private-stream"), 0600) != nil {
			t.Fatal("native fixture stream unavailable")
		}
	}
	store, err := cache.NewSQLiteStore(ctx, cachePath)
	if err != nil {
		t.Fatal("native fixture cache unavailable")
	}
	identity, err := store.CacheIdentity(ctx)
	closeErr := store.Close()
	if err != nil || closeErr != nil {
		t.Fatal("native fixture cache identity unavailable")
	}
	f.manager.Runner = func(ctx context.Context, name string, args ...string) error {
		_, err := f.run(ctx, name, args...)
		return err
	}
	f.manager.OutputRunner = f.run
	// Positive absence is checked before this fixture may claim an owner. A
	// random name is useful, but is not itself evidence of native absence.
	if runtime.GOOS == "darwin" {
		out, err := nativeCommand(ctx, "launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), f.label))
		var exited *exec.ExitError
		if !errors.As(err, &exited) || exited.ExitCode() != 113 || !strings.Contains(out, `Could not find service "`+f.label+`"`) {
			t.Fatal("native unique launchd owner absence not confirmed")
		}
	} else {
		out, err := nativeCommand(ctx, "systemctl", "--user", "show", "--property=LoadState", "--value", f.label)
		if err != nil || strings.TrimSpace(out) != "not-found" {
			t.Fatal("native unique systemd owner absence not confirmed")
		}
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer stopCancel()
		if err := f.manager.stopObservationOwner(stopCtx, f.paths); err != nil {
			t.Error("native fixture owner cleanup failed")
			return
		}
		if _, err := f.manager.waitForStopped(stopCtx, f.paths); err != nil {
			t.Error("native fixture process did not join")
			return
		}
		if runtime.GOOS == "linux" && f.linked {
			if _, err := nativeCommand(stopCtx, "systemctl", "--user", "--runtime", "disable", f.label); err != nil {
				t.Error("native fixture unit unlink failed")
				return
			}
			if _, err := nativeCommand(stopCtx, "systemctl", "--user", "daemon-reload"); err != nil {
				t.Error("native fixture unlink reload failed")
				return
			}
			out, err := nativeCommand(stopCtx, "systemctl", "--user", "show", "--property=LoadState", "--value", f.label)
			if err != nil || strings.TrimSpace(out) != "not-found" {
				t.Error("native fixture unit absence not confirmed")
				return
			}
		}
		cleanupOK = true
	}()
	cleanupOK = false
	before, err := f.manager.Start(ctx)
	if err != nil || !before.Running {
		t.Fatalf("native legacy owner startup failed; finite platform outcomes: %v", f.trace)
	}
	client, err := f.manager.Client()
	if err != nil {
		t.Fatal("native fixture IPC unavailable")
	}
	rpc := func(method string, params, out any) {
		t.Helper()
		callCtx, done := context.WithTimeout(ctx, 3*time.Second)
		defer done()
		if nativeObservationCall(callCtx, method, func() error { return client.Call(callCtx, method, params, out) }) != nil {
			t.Fatal("native fixture RPC failed: " + method)
		}
	}
	summary := func(want string) observability.Summary {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			var result observability.Summary
			rpc("Observability.Summary", nil, &result)
			if result.ObservationHealth.OutputState == want {
				return result
			}
			if time.Now().After(deadline) {
				t.Fatal("native observation readiness mismatch")
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	oldSummary := summary("migration_required")
	assertPID := func(want int, managed bool) {
		t.Helper()
		var out string
		var err error
		if runtime.GOOS == "darwin" {
			out, err = nativeCommand(ctx, "launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), f.label))
			if managed {
				stdout, stderr := false, false
				for _, line := range strings.Split(out, "\n") {
					switch strings.TrimSpace(line) {
					case "stdout path = /dev/null":
						stdout = true
					case "stderr path = /dev/null":
						stderr = true
					}
				}
				if !stdout || !stderr {
					t.Fatal("native loaded launchd routing is not bounded")
				}
			}
			found := false
			for _, line := range strings.Split(out, "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "pid = ") {
					out = strings.TrimPrefix(strings.TrimSpace(line), "pid = ")
					found = true
					break
				}
			}
			if !found {
				t.Fatal("native owner PID unavailable")
			}
		} else {
			out, err = nativeCommand(ctx, "systemctl", "--user", "show", "--property=MainPID", "--value", f.label)
			if managed {
				routing, routingErr := nativeCommand(ctx, "systemctl", "--user", "show", "--property=StandardOutput", "--property=StandardError", "--value", f.label)
				if routingErr != nil || strings.TrimSpace(routing) != "null\nnull" {
					t.Fatal("native loaded systemd routing is not bounded")
				}
			}
		}
		pid, parseErr := strconv.Atoi(strings.TrimSpace(out))
		if err != nil || parseErr != nil || pid != want || pid <= 0 {
			t.Fatal("native platform and daemon PID disagree")
		}
	}
	assertPID(before.PID, false)
	var job Job
	rpc("Jobs.StartFake", StartFakeJobRequest{Steps: 1, IntervalMS: 1}, &job)
	deadline := time.Now().Add(5 * time.Second)
	var completed Job
	for {
		var jobs JobListResult
		rpc("Jobs.List", nil, &jobs)
		for _, j := range jobs.Jobs {
			if j.ID == job.ID && j.FinishedAt != nil {
				completed = j
			}
		}
		if completed.Status == JobStatusSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native fixture job did not complete")
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls := f.calls
	plan, err := f.manager.PlanObservationUpgrade()
	if err != nil || f.calls != calls || len(plan.Legacy) != 2 {
		t.Fatal("native upgrade plan is not read-only")
	}
	public, _ := json.Marshal(plan)
	if strings.Contains(string(public), root) || strings.Contains(string(public), "synthetic-private") {
		t.Fatal("native plan leaked private fixture data")
	}
	if _, err := f.manager.ApplyObservationUpgrade(ctx, plan.PlanID+"-stale"); err == nil || f.calls != calls {
		t.Fatal("native stale plan reached owner")
	}
	f.interrupt = true
	_, err = f.manager.ApplyObservationUpgrade(ctx, plan.PlanID)
	var domainErr RPCDomainError
	if !errors.As(err, &domainErr) || domainErr.Code != "observation_restart_incomplete" || f.interrupt {
		t.Fatal("native interrupted restart boundary not reached")
	}
	stopped, err := f.manager.Status()
	if err != nil || stopped.PIDAlive || stopped.SocketPresent || !boundedDefinition(f.paths.InstallKind, f.paths.InstallPath) {
		t.Fatal("native interrupted owner is not quiescent and replaced")
	}
	for _, file := range []string{"service.out.log", "service.err.log"} {
		if _, err := os.Stat(filepath.Join(f.paths.LogDir, file)); !os.IsNotExist(err) {
			t.Fatal("native fixed legacy cleanup incomplete")
		}
	}
	fresh, err := f.manager.PlanObservationUpgrade()
	present := false
	for _, stream := range fresh.Legacy {
		present = present || stream.Present
	}
	if err != nil || fresh.PlanID == plan.PlanID || present {
		t.Fatal("native remaining-effects plan unavailable")
	}
	applied, err := f.manager.ApplyObservationUpgrade(ctx, fresh.PlanID)
	if err != nil || applied.State != "applied" {
		t.Fatal("native fresh-plan recovery failed")
	}
	var after Status
	rpc("Service.Status", nil, &after)
	if !after.Running || after.PID == before.PID {
		t.Fatal("native upgraded owner did not restart")
	}
	assertPID(after.PID, true)
	newSummary := summary("managed")
	if newSummary.Collector.BootID == oldSummary.Collector.BootID {
		t.Fatal("native observation boot identity did not change")
	}
	var jobs JobListResult
	rpc("Jobs.List", nil, &jobs)
	retained := false
	for _, j := range jobs.Jobs {
		if j.ID == completed.ID && j.Status == completed.Status && j.FinishedAt != nil && j.FinishedAt.Equal(*completed.FinishedAt) {
			retained = true
		}
	}
	if !retained {
		t.Fatal("native restart lost durable completed job")
	}
	reopened, err := cache.NewSQLiteReadOnlyStore(ctx, cachePath)
	if err != nil {
		t.Fatal("native retained cache unavailable")
	}
	afterIdentity, identityErr := reopened.CacheIdentity(ctx)
	closeErr = reopened.Close()
	if identityErr != nil || closeErr != nil || afterIdentity.UUID != identity.UUID {
		t.Fatal("native restart changed durable cache identity")
	}
	b, err := os.ReadFile(filepath.Join(f.paths.LogDir, "unrelated.txt"))
	if err != nil || string(b) != "synthetic-private-stream" {
		t.Fatal("native cleanup changed unrelated local file")
	}
	for _, file := range []string{"service.out.log", "service.err.log"} {
		if _, err := os.Stat(filepath.Join(f.paths.LogDir, file)); !os.IsNotExist(err) {
			t.Fatal("native restart recreated legacy streams")
		}
	}
}
