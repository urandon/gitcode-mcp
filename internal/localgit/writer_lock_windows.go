package localgit

import (
	"context"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
)

func (s *Store) writerLock(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := filepath.Join(s.dir, "harness-writer.lock")
	if st, err := os.Lstat(path); err == nil && !st.Mode().IsRegular() {
		return nil, fail("unavailable", "writer_lock")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fail("unavailable", "writer_lock")
	}
	var overlap windows.Overlapped
	const wholeFile = ^uint32(0)
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, wholeFile, wholeFile, &overlap); err != nil {
		f.Close()
		if err == windows.ERROR_LOCK_VIOLATION || err == windows.ERROR_SHARING_VIOLATION {
			return nil, fail("writer_busy", "writer_lock")
		}
		return nil, fail("unavailable", "writer_lock")
	}
	return func() { windows.UnlockFileEx(windows.Handle(f.Fd()), 0, wholeFile, wholeFile, &overlap); f.Close() }, nil
}
