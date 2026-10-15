//go:build !windows

package localgit

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
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
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, fail("writer_busy", "writer_lock")
		}
		return nil, fail("unavailable", "writer_lock")
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
