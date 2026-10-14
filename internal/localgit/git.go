package localgit

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

const (
	MaxFileBytes     = 1 << 20
	MaxMetadataBytes = 16 << 10
	MaxTreeBytes     = 4 << 20
	MaxEntries       = 10000
	MaxHistory       = 256
	MaxReadBytes     = 8 << 20
	commandTimeout   = 10 * time.Second
)

type Limits struct{ FileBytes, MetadataBytes, TreeBytes, Entries, History, ReadBytes int }

func normalizeLimits(l Limits) (Limits, error) {
	items := []struct {
		value *int
		max   int
	}{{&l.FileBytes, MaxFileBytes}, {&l.MetadataBytes, MaxMetadataBytes}, {&l.TreeBytes, MaxTreeBytes}, {&l.Entries, MaxEntries}, {&l.History, MaxHistory}, {&l.ReadBytes, MaxReadBytes}}
	for _, p := range items {
		if *p.value == 0 {
			*p.value = p.max
		}
		if *p.value < 1 || *p.value > p.max {
			return l, fail("invalid_argument", "limits")
		}
	}
	return l, nil
}

// Store is machine-local; its filesystem identity is never a public DTO.
type Store struct {
	dir, git string
	limits   Limits
}

func Open(ctx context.Context, path string, limits Limits) (*Store, error) {
	l, err := normalizeLimits(limits)
	if err != nil {
		return nil, err
	}
	dir, err := filepath.Abs(path)
	if err != nil || path == "" {
		return nil, fail("invalid_argument", "store")
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fail("unavailable", "store")
	}
	if st, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		if !st.IsDir() {
			return nil, fail("unsupported_store", "git_directory")
		}
		dir = filepath.Join(dir, ".git")
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		return nil, fail("unavailable", "store")
	}
	// Linked worktrees and alternates redirect authority outside the selected store.
	for _, p := range []string{"commondir", "objects/info/alternates"} {
		if _, err := os.Lstat(filepath.Join(dir, p)); err == nil {
			return nil, fail("unsupported_store", "store_redirection")
		} else if !os.IsNotExist(err) {
			return nil, fail("unavailable", "store")
		}
	}
	objects, err := filepath.EvalSymlinks(filepath.Join(dir, "objects"))
	if err != nil || objects != filepath.Join(dir, "objects") {
		return nil, fail("unsupported_store", "objects")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return nil, fail("unavailable", "git")
	}
	s := &Store{dir: dir, git: git, limits: l}
	if _, err = s.run(ctx, 128, "rev-parse", "--is-bare-repository"); err != nil {
		return nil, err
	}
	return s, nil
}

type boundedOutput struct {
	buffer   bytes.Buffer
	limit    int
	cancel   context.CancelFunc
	exceeded bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.buffer.Len() {
		b.exceeded = true
		b.cancel()
		return 0, fail("limit_exceeded", "git_output")
	}
	return b.buffer.Write(p)
}

// run is deliberately private: public callers cannot supply Git subcommands.
// The same store-bound environment can be used by G2's typed plumbing writes.
func (s *Store) run(ctx context.Context, maxBytes int, args ...string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	child, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	prefix := []string{"--no-pager", "--no-replace-objects", "--git-dir=" + s.dir, "-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "credential.helper=", "-c", "core.askPass=", "-c", "core.attributesFile=" + os.DevNull, "-c", "commit.gpgsign=false", "-c", "tag.gpgsign=false", "-c", "gc.auto=0", "-c", "maintenance.auto=false"}
	cmd := exec.CommandContext(child, s.git, append(prefix, args...)...)
	cmd.Dir = s.dir
	cmd.Env = []string{"PATH=" + filepath.Dir(s.git), "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=" + os.DevNull, "GIT_CONFIG_GLOBAL=" + os.DevNull, "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=", "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_COMMON_DIR=" + s.dir, "GIT_OBJECT_DIRECTORY=" + filepath.Join(s.dir, "objects")}
	out := &boundedOutput{limit: maxBytes, cancel: cancel}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if out.exceeded {
		return nil, fail("limit_exceeded", "git_output")
	}
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fail("unavailable", "git_object")
	}
	return out.buffer.Bytes(), nil
}
