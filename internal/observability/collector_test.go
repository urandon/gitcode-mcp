//go:build darwin || linux

package observability

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func testDirectory(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal("resolve fixture root")
	}
	return filepath.Join(root, "observation")
}
func await(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !fn() {
		if time.Now().After(deadline) {
			t.Fatal("observation barrier timed out")
		}
		time.Sleep(time.Millisecond)
	}
}
func ready(t *testing.T, c *Collector) {
	t.Helper()
	await(t, func() bool { return c.Coverage().State != "initializing" })
}
func flush(t *testing.T, c *Collector, clean bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !c.Close(ctx, clean) {
		t.Fatal("observation flush timed out")
	}
}
func inventory(t *testing.T, path string) int64 {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal("read fixture inventory")
	}
	var bytes int64
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			t.Fatal("stat fixture inventory")
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			t.Fatal("unsafe retained file")
		}
		cap := int64(SegmentBytes)
		if strings.HasPrefix(entry.Name(), "state-") {
			cap = 4096
		}
		if entry.Name() == "owner.lock" {
			cap = 0
		}
		if info.Size() > cap {
			t.Fatal("retained file exceeds bound")
		}
		bytes += info.Size()
	}
	if bytes > LedgerBytes+StreamBytes+MetadataBytes {
		t.Fatal("complete footprint exceeds bound")
	}
	return bytes
}

func TestCatalogRejectsRawTextAndUncheckedRefs(t *testing.T) {
	path := testDirectory(t)
	c := New(Config{Directory: path, ManagedOutput: true})
	ready(t, c)
	sentinel := "synthetic-private-content-marker"
	if c.Emit(Code(sentinel), Context{}) || c.Emit(RecoveryFailed, Context{Repo: Ref{sentinel}}) {
		t.Fatal("unchecked content accepted")
	}
	if !c.Emit(RecoveryFailed, Context{Repo: KnownRef("repo", sentinel), Job: KnownRef("job", sentinel)}) {
		t.Fatal("resolved opaque references rejected")
	}
	flush(t, c, true)
	p, err := c.Query(Filter{}, "", 200)
	if err != nil {
		t.Fatal("query failed")
	}
	b, _ := json.Marshal(p)
	if strings.Contains(string(b), sentinel) {
		t.Fatal("source sentinel leaked to DTO")
	}
	entries, _ := os.ReadDir(path)
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(path, entry.Name()))
		if err != nil {
			t.Fatal("read retained fixture")
		}
		if strings.Contains(string(data), sentinel) {
			t.Fatal("source sentinel persisted")
		}
	}
	inventory(t, path)
}
func TestCleanRestartPreservesOrderAndCursor(t *testing.T) {
	path := testDirectory(t)
	c := New(Config{Directory: path, ManagedOutput: true})
	ready(t, c)
	c.Emit(RecoveryFinished, Context{})
	flush(t, c, true)
	p, err := c.Query(Filter{}, "", 200)
	if err != nil {
		t.Fatal("initial page")
	}
	last := p.Events[len(p.Events)-1].Sequence
	c2 := New(Config{Directory: path, ManagedOutput: true})
	ready(t, c2)
	await(t, func() bool { c2.mu.Lock(); defer c2.mu.Unlock(); return len(c2.events) > len(p.Events) })
	next, err := c2.Query(Filter{}, p.Cursor, 200)
	if err != nil || len(next.Events) == 0 || next.Events[0].Sequence <= last {
		t.Fatal("cross-boot scan did not advance")
	}
	if c2.Coverage().ObservedBoots != 2 {
		t.Fatal("observed boot count")
	}
	flush(t, c2, true)
	inventory(t, path)
}
func TestUncleanCorruptTailAndLossInvalidateCursor(t *testing.T) {
	path := testDirectory(t)
	c := New(Config{Directory: path, ManagedOutput: true})
	ready(t, c)
	flush(t, c, false)
	p, _ := c.Query(Filter{}, "", 200)
	f, err := os.OpenFile(filepath.Join(path, "ledger-0.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("open fixture tail")
	}
	_, err = f.WriteString("{\"schema\":99,\"message\":\"synthetic-hostile-tail\"")
	f.Close()
	if err != nil {
		t.Fatal("write fixture tail")
	}
	c2 := New(Config{Directory: path, ManagedOutput: true})
	ready(t, c2)
	if c2.Coverage().State != "partial" {
		t.Fatal("corruption incorrectly healthy")
	}
	_, err = c2.Query(Filter{}, p.Cursor, 200)
	var q QueryError
	if !errors.As(err, &q) || q.Code != "snapshot_required" {
		t.Fatal("unclean cursor accepted")
	}
	fresh, err := c2.Query(Filter{}, "", 200)
	if err != nil || len(fresh.Events) < len(p.Events) {
		t.Fatal("validated prefix was lost")
	}
	data, _ := json.Marshal(fresh)
	if strings.Contains(string(data), "synthetic-hostile-tail") {
		t.Fatal("corrupt source bytes leaked")
	}
	flush(t, c2, false)
}
func TestQueueIsNonblockingDuringStorageStall(t *testing.T) {
	blocked := make(chan struct{})
	entered := make(chan struct{})
	c := newCollector(Config{Directory: testDirectory(t)}, func(string) (*storage, []Event, bool, error) {
		close(entered)
		<-blocked
		return nil, nil, false, syscall.ENOSPC
	})
	<-entered
	var accepted int
	for i := 0; i < MaxEvents+20; i++ {
		if c.Emit(RecoveryStarted, Context{}) {
			accepted++
		}
	}
	if accepted != MaxEvents {
		t.Fatal("queue event bound")
	}
	c.mu.Lock()
	bytes, count := c.queueBytes, c.queued
	c.mu.Unlock()
	if bytes > MaxQueueBytes || count > MaxEvents {
		t.Fatal("queue exceeds bound")
	}
	if c.Coverage().Dropped != 20 {
		t.Fatal("missing queue loss evidence")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if c.Close(ctx, true) {
		t.Fatal("stalled storage reported flushed")
	}
	close(blocked)
	flush(t, c, true)
	p, err := c.Query(Filter{}, "", 200)
	if err != nil || p.Coverage.State != "partial" || p.Coverage.Dropped != 20 {
		t.Fatal("loss not readable")
	}
	encoded, _ := json.Marshal(p)
	if len(encoded) > MaxPageBytes || len(p.Events) > 200 {
		t.Fatal("page exceeds bound")
	}
}
func TestENOSPCNeverStopsAdmissionAndSurvivesRestart(t *testing.T) {
	path := testDirectory(t)
	c := newCollector(Config{Directory: path, ManagedOutput: true}, func(path string) (*storage, []Event, bool, error) {
		s, e, u, err := openStorage(path)
		if err == nil {
			s.appendFault = func() error { return syscall.ENOSPC }
		}
		return s, e, u, err
	})
	ready(t, c)
	for i := 0; i < 20; i++ {
		if !c.Emit(RecoveryFinished, Context{}) {
			t.Fatal("disk failure blocked bounded admission")
		}
	}
	flush(t, c, true)
	if c.Coverage().State != "partial" {
		t.Fatal("disk failure incorrectly healthy")
	}
	c2 := New(Config{Directory: path, ManagedOutput: true})
	ready(t, c2)
	if c2.Coverage().State != "partial" {
		t.Fatal("disk loss erased on restart")
	}
	flush(t, c2, true)
}
func TestSparseFiltersAdvanceAndBindCursor(t *testing.T) {
	c := New(Config{Directory: testDirectory(t), ManagedOutput: true})
	ready(t, c)
	for i := 0; i < 400; i++ {
		for !c.Emit(RecoveryStarted, Context{}) {
			time.Sleep(time.Millisecond)
		}
	}
	flush(t, c, true)
	filter := Filter{Severity: "error"}
	p, err := c.Query(filter, "", 200)
	if err != nil || len(p.Events) != 0 || !p.HasMore {
		t.Fatal("empty sparse scan lost continuation")
	}
	previous := p.Cursor
	for p.HasMore {
		p, err = c.Query(filter, p.Cursor, 200)
		if err != nil || p.Cursor == previous {
			t.Fatal("empty scan did not advance")
		}
		previous = p.Cursor
	}
	_, err = c.Query(Filter{}, p.Cursor, 200)
	var q QueryError
	if !errors.As(err, &q) || q.Code != "invalid_cursor" {
		t.Fatal("changed filter accepted")
	}
	_, err = c.Query(filter, "not-a-cursor", 200)
	if !errors.As(err, &q) || q.Code != "invalid_cursor" {
		t.Fatal("malformed cursor accepted")
	}
}
func TestSingleWriterSymlinkHardlinkAndPermissions(t *testing.T) {
	path := testDirectory(t)
	c := New(Config{Directory: path})
	ready(t, c)
	other := New(Config{Directory: path})
	ready(t, other)
	if other.Coverage().State != "partial" {
		t.Fatal("second writer admitted")
	}
	flush(t, other, true)
	flush(t, c, true)
	for _, kind := range []string{"symlink", "hardlink", "permissions", "directory"} {
		t.Run(kind, func(t *testing.T) {
			path := testDirectory(t)
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal("mkdir fixture")
			}
			name := filepath.Join(path, "ledger-0.jsonl")
			target := filepath.Join(filepath.Dir(path), "external-fixture")
			if err := os.WriteFile(target, []byte("synthetic-external-marker"), 0600); err != nil {
				t.Fatal("create target fixture")
			}
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(target, name)
			case "hardlink":
				err = os.Link(target, name)
			case "permissions":
				err = os.WriteFile(name, nil, 0644)
			case "directory":
				err = os.Mkdir(name, 0700)
			}
			if err != nil {
				t.Fatal("create hostile fixture")
			}
			c := New(Config{Directory: path})
			ready(t, c)
			if c.Coverage().State != "partial" {
				t.Fatal("unsafe storage admitted")
			}
			flush(t, c, true)
			b, _ := os.ReadFile(target)
			if string(b) != "synthetic-external-marker" {
				t.Fatal("external file modified")
			}
		})
	}
}
func TestConcurrentRotationWholeFootprintAndReopen(t *testing.T) {
	path := testDirectory(t)
	c := New(Config{Directory: path, ManagedOutput: true})
	ready(t, c)
	// Larger opaque contexts exercise all segments without accepting raw text.
	refs := Context{Job: KnownRef("job", "fixture"), Registration: KnownRef("registration", "fixture"), Cache: KnownRef("cache", "fixture"), Repo: KnownRef("repo", "fixture"), Correlation: KnownRef("correlation", "fixture")}
	var wg sync.WaitGroup
	for writer := 0; writer < 4; writer++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 2100; i++ {
				code := RecoveryStarted
				if i%2 == 0 {
					code = RecoveryFailed
				}
				for !c.Emit(code, refs) {
					time.Sleep(time.Millisecond)
				}
			}
		}()
	}
	wg.Wait()
	flush(t, c, true)
	if inventory(t, path) < 4*SegmentBytes {
		t.Fatal("fixture did not exercise whole rotation footprint")
	}
	p, _ := c.Query(Filter{}, "", 200)
	if len(p.Events) == 0 || p.Events[0].Sequence == 1 {
		t.Fatal("ledger did not rotate")
	}
	c2 := New(Config{Directory: path, ManagedOutput: true})
	ready(t, c2)
	c2.Emit(RecoveryFinished, refs)
	flush(t, c2, true)
	inventory(t, path)
	entries, _ := os.ReadDir(path)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".jsonl") {
			b, _ := os.ReadFile(filepath.Join(path, entry.Name()))
			if len(b) > 0 && !strings.HasSuffix(string(b), "\n") {
				t.Fatal("writer did not reopen safely")
			}
		}
	}
}
func TestLegacyOnlyRecognizesTemplatesAndNoRawLines(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal("resolve fixture")
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal("make legacy fixture private")
	}
	sentinel := "synthetic-secret-body"
	data := catalog[Boot].message + "\n" + strings.Repeat(sentinel, 1000) + "\n" + catalog[RecoveryFailed].message + "\n"
	if err := os.WriteFile(filepath.Join(root, "service.out.log"), []byte(data), 0600); err != nil {
		t.Fatal("write legacy fixture")
	}
	sample, err := ReadLegacy(root, "stdout")
	if err != nil || sample.Bytes > MaxPageBytes || !sample.Partial || sample.Codes[0] != Boot {
		t.Fatal("bounded legacy template read", sample, err)
	}
	b, _ := json.Marshal(sample)
	if strings.Contains(string(b), sentinel) {
		t.Fatal("legacy raw text leaked")
	}
	files, err := LegacyInventory(root)
	if err != nil || !files[0].Present {
		t.Fatal("legacy inventory")
	}
	if err := RemoveLegacy(root, files); err != nil {
		t.Fatal("fixed cleanup")
	}
	if _, err := os.Stat(filepath.Join(root, "service.out.log")); !os.IsNotExist(err) {
		t.Fatal("legacy file remains")
	}
}

func TestLegacyCursorHandlesPartialContinuationRotationAndTruncation(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal("resolve fixture")
	}
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal("private fixture")
	}
	name := filepath.Join(root, "service.out.log")
	if err := os.WriteFile(name, []byte(strings.Repeat("x", MaxPageBytes+MaxEventBytes)), 0600); err != nil {
		t.Fatal("write bounded scan fixture")
	}
	first, err := ReadLegacy(root, "stdout")
	if err != nil || first.Bytes != MaxPageBytes || !first.HasMore || !first.Cursor.SkipPartial {
		t.Fatal("legacy byte-bound continuation")
	}
	f, err := os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal("append fixture")
	}
	_, err = f.WriteString(catalog[Boot].message + "\n" + catalog[RecoveryFinished].message + "\n")
	f.Close()
	if err != nil {
		t.Fatal("append fixture")
	}
	second, err := ReadLegacyFrom(root, "stdout", first.Cursor)
	if err != nil || second.HasMore {
		t.Fatal("resume scan")
	}
	for _, code := range second.Codes {
		if code == Boot {
			t.Fatal("partial continuation treated as a complete template")
		}
	}
	if err := os.Truncate(name, 0); err != nil {
		t.Fatal("truncate fixture")
	}
	truncated, err := ReadLegacyFrom(root, "stdout", second.Cursor)
	if err != nil || truncated.State != "truncated" || truncated.Cursor.Offset != 0 {
		t.Fatal("truncation not explicit")
	}
	if err := os.Rename(name, filepath.Join(root, "old-fixture")); err != nil {
		t.Fatal("rotate fixture")
	}
	if err := os.WriteFile(name, []byte(catalog[Boot].message+"\n"), 0600); err != nil {
		t.Fatal("new rotated fixture")
	}
	rotated, err := ReadLegacyFrom(root, "stdout", truncated.Cursor)
	if err != nil || rotated.State != "rotated" || len(rotated.Codes) != 1 || rotated.Codes[0] != Boot {
		t.Fatal("rotation not explicit")
	}
}
func TestForeignCursorIsInvalidNotAnExpiredGeneration(t *testing.T) {
	c := New(Config{Directory: testDirectory(t), ManagedOutput: true})
	ready(t, c)
	flush(t, c, true)
	p, _ := c.Query(Filter{}, "", 200)
	other := New(Config{Directory: testDirectory(t), ManagedOutput: true})
	ready(t, other)
	flush(t, other, true)
	_, err := other.Query(Filter{}, p.Cursor, 200)
	var q QueryError
	if !errors.As(err, &q) || q.Code != "invalid_cursor" || q.Reason != "foreign_or_modified" {
		t.Fatal("foreign cursor incorrectly treated as known history")
	}
}
