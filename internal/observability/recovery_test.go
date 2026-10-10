//go:build darwin || linux

package observability

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
)

func TestCorruptTailPreservesEventsAcrossLaterRestarts(t *testing.T) {
	path := testDirectory(t)
	first := New(Config{Directory: path, ManagedOutput: true})
	ready(t, first)
	flush(t, first, true)
	f, err := os.OpenFile(filepath.Join(path, ledgerName(0)), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("open fixture tail")
	}
	_, err = f.WriteString("{")
	f.Close()
	if err != nil {
		t.Fatal("write fixture tail")
	}
	second := New(Config{Directory: path, ManagedOutput: true})
	ready(t, second)
	second.Emit(RecoveryFinished, Context{})
	flush(t, second, true)
	page, err := second.Query(Filter{Code: RecoveryFinished}, "", 200)
	if err != nil || len(page.Events) != 1 {
		t.Fatal("fixture event missing")
	}
	wanted := page.Events[0].EventID
	third := New(Config{Directory: path, ManagedOutput: true})
	ready(t, third)
	flush(t, third, true)
	page, err = third.Query(Filter{Code: RecoveryFinished}, "", 200)
	if err != nil || len(page.Events) != 1 || page.Events[0].EventID != wanted {
		t.Fatal("post-corruption evidence lost on later restart")
	}
	inventory(t, path)
}

func TestRestartResumesNewestManagedStream(t *testing.T) {
	path := testDirectory(t)
	c := New(Config{Directory: path, ManagedOutput: true})
	ready(t, c)
	for i := 0; i < 4300; i++ {
		for !c.Emit(RecoveryStarted, Context{}) {
		}
	}
	flush(t, c, true)
	s, _, _, err := openStorage(path)
	if err != nil {
		t.Fatal("fixture reopen")
	}
	defer s.close()
	if s.lengths[streamName("stdout", 1)] == 0 {
		t.Fatal("fixture did not rotate")
	}
	if s.outSlot != 1 {
		t.Fatal("older full stream selected over newest slot")
	}
}

func TestTransientAppendFailureRecoversWithoutErasingLoss(t *testing.T) {
	path := testDirectory(t)
	attempts := 0
	c := newCollector(Config{Directory: path, ManagedOutput: true}, func(path string) (*storage, []Event, bool, error) {
		s, e, u, err := openStorage(path)
		if err == nil {
			s.appendFault = func() error {
				attempts++
				if attempts == 1 {
					return syscall.ENOSPC
				}
				return nil
			}
		}
		return s, e, u, err
	})
	ready(t, c)
	c.Emit(RecoveryFinished, Context{})
	flush(t, c, true)
	if attempts < 2 || c.Coverage().State != "partial" || c.Coverage().LossEpoch == 0 {
		t.Fatal("transient storage recovery missing")
	}
	page, _ := c.Query(Filter{}, "", 200)
	epoch := c.Coverage().LossEpoch
	c2 := New(Config{Directory: path, ManagedOutput: true})
	ready(t, c2)
	flush(t, c2, true)
	if c2.Coverage().LossEpoch != epoch {
		t.Fatal("known partial history counted as new loss")
	}
	if _, err := c2.Query(Filter{}, page.Cursor, 200); err != nil {
		t.Fatal("clean restart invalidated known-partial cursor")
	}
	recovered, err := c2.Query(Filter{Code: RecoveryFinished}, "", 200)
	if err != nil || len(recovered.Events) != 1 {
		t.Fatal("post-fault event was not durable")
	}
}

func TestIncompleteShutdownImmediatelyMarksPartial(t *testing.T) {
	path := testDirectory(t)
	c := New(Config{Directory: path, ManagedOutput: true})
	ready(t, c)
	if !c.Close(context.Background(), false) {
		t.Fatal("flush failed")
	}
	if c.Coverage().State != "partial" || c.Coverage().LossEpoch == 0 {
		t.Fatal("incomplete teardown reported healthy")
	}
	s, _, _, err := openStorage(path)
	if err != nil {
		t.Fatal("read final metadata")
	}
	defer s.close()
	if !s.state.Partial {
		t.Fatal("incomplete teardown metadata omitted loss")
	}
}

func TestCleanManifestDetectsMissingLedgerAndStream(t *testing.T) {
	for _, removed := range []string{ledgerName(0), streamName("stdout", 0)} {
		t.Run(removed, func(t *testing.T) {
			path := testDirectory(t)
			c := New(Config{Directory: path, ManagedOutput: true})
			ready(t, c)
			flush(t, c, true)
			page, _ := c.Query(Filter{}, "", 200)
			if err := os.Remove(filepath.Join(path, removed)); err != nil {
				t.Fatal("remove fixture segment")
			}
			c2 := New(Config{Directory: path, ManagedOutput: true})
			ready(t, c2)
			flush(t, c2, true)
			if c2.Coverage().State != "partial" {
				t.Fatal("missing segment reported healthy")
			}
			_, err := c2.Query(Filter{}, page.Cursor, 200)
			var q QueryError
			if !errors.As(err, &q) || q.Code != "snapshot_required" {
				t.Fatal("missing segment did not fence cursor")
			}
		})
	}
}

func TestShortAppendSuffixIsTrimmedBeforeNextWrite(t *testing.T) {
	path := testDirectory(t)
	s, _, _, err := openStorage(path)
	if err != nil {
		t.Fatal("open fixture storage")
	}
	defer s.close()
	name := ledgerName(0)
	if err := replacePrivate(s.dir, name, []byte("{torn")); err != nil {
		t.Fatal("write short suffix")
	}
	if err := appendPrivate(s.dir, name, []byte("complete\n"), 0); err != nil {
		t.Fatal("bounded append recovery")
	}
	b, err := readPrivate(s.dir, name, SegmentBytes)
	if err != nil || string(b) != "complete\n" {
		t.Fatal("failed append suffix retained")
	}
}

func TestAtomicReplacementFailurePreservesCommittedBytes(t *testing.T) {
	if os.Getenv("GITCODE_TEST_REPLACEMENT_CHILD") != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal("test executable unavailable")
		}
		cmd := exec.Command(executable, "-test.run=^TestAtomicReplacementFailurePreservesCommittedBytes$")
		cmd.Env = append(os.Environ(), "GITCODE_TEST_REPLACEMENT_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated replacement regression: %s", output)
		}
		return
	}
	path := testDirectory(t)
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal("fixture directory")
	}
	dir, err := openDirectory(path, false)
	if err != nil {
		t.Fatal("fixture directory handle")
	}
	defer dir.Close()
	before := bytes.Repeat([]byte("a"), 512)
	for _, name := range []string{"definition", "state-0.json"} {
		if err := replacePrivate(dir, name, before); err != nil {
			t.Fatal("fixture committed bytes")
		}
	}
	var limit syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
		t.Fatal("get fixture limit")
	}
	limit.Cur = 128
	signal.Ignore(syscall.SIGXFSZ)
	if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
		t.Fatal("set fixture limit")
	}
	if err := WriteDefinition(filepath.Join(path, "definition"), bytes.Repeat([]byte("b"), 2048)); err == nil {
		t.Fatal("replacement unexpectedly passed")
	}
	if err := replaceAtomic(dir, "state-0.json", "state-1.json", bytes.Repeat([]byte("b"), 2048), 4096); err == nil {
		t.Fatal("metadata replacement unexpectedly passed")
	}
	for _, name := range []string{"definition", "state-0.json"} {
		after, err := readPrivate(dir, name, 4096)
		if err != nil || !bytes.Equal(before, after) {
			t.Fatal("failed replacement destroyed committed bytes")
		}
	}
}

func TestLegacyStreamIdentityAndUnavailableOutputArePartial(t *testing.T) {
	root := filepath.Dir(testDirectory(t))
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal("private fixture")
	}
	if err := os.WriteFile(filepath.Join(root, "service.out.log"), []byte(catalog[RecoveryFailed].message+"\nunknown\n"), 0600); err != nil {
		t.Fatal("legacy fixture")
	}
	c := New(Config{Directory: filepath.Join(root, "observation"), LegacyDirectory: root})
	ready(t, c)
	flush(t, c, true)
	page, err := c.Query(Filter{Code: RecoveryFailed, Stream: "stdout"}, "", 200)
	if err != nil || len(page.Events) != 1 || !page.Events[0].Legacy {
		t.Fatal("legacy source stream misattributed")
	}
	if c.Coverage().State != "partial" || c.Coverage().OutputState != "migration_required" {
		t.Fatal("legacy output reported fully managed")
	}
}
