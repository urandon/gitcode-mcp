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
			return nil, ErrLockContention{Path: lockPath}
		}
		return nil, err
	}
	return &LockHandle{file: file, path: lockPath}, nil
}

func (s *SQLiteStore) ReleaseLock(ctx context.Context, handle *LockHandle) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	if handle == nil || handle.file == nil {
		return nil
	}
	if err := syscall.Flock(int(handle.file.Fd()), syscall.LOCK_UN); err != nil {
		return err
	}
	err := handle.file.Close()
	handle.file = nil
	return err
}
