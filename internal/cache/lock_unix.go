package cache

import (
	"context"
	"syscall"
)

func (s *SQLiteStore) AcquireLock(ctx context.Context, lockPath string) (*LockHandle, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	file, err := openLockFile(lockPath)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return nil, ErrLockContention{Path: lockPath, HolderHint: "another process holds the cache lock"}
		}
		return nil, err
	}
	return &LockHandle{file: file, path: lockPath}, nil
}

func (s *SQLiteStore) ReleaseLock(ctx context.Context, handle *LockHandle) error {
	_ = ctx
	if handle == nil || handle.file == nil {
		return nil
	}
	file := handle.file
	handle.file = nil
	unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	closeErr := file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr

}
