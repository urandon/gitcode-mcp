package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

const wholeFileLockRange = ^uint32(0)

func (s *SQLiteStore) AcquireLock(ctx context.Context, lockPath string) (*LockHandle, error) {
	return s.acquireLock(ctx, lockPath, WriterOwner{Operation: "legacy", StartedAt: time.Now().UTC(), PID: os.Getpid(), CachePath: s.cachePath})
}

func (s *SQLiteStore) AcquireWriter(ctx context.Context, req WriterRequest) (*WriterLease, error) {
	operation := strings.TrimSpace(req.Operation)
	if operation == "" {
		operation = "writer"
	}
	lockPath := strings.TrimSpace(req.LockPath)
	if lockPath == "" {
		lockPath = s.lockPath
	}
	owner := WriterOwner{Operation: operation, RepoID: strings.TrimSpace(req.RepoID), StartedAt: time.Now().UTC(), PID: os.Getpid(), CachePath: s.cachePath}
	lock, err := s.acquireLock(ctx, lockPath, owner)
	if err != nil {
		return nil, err
	}
	return &WriterLease{lock: lock, Owner: owner}, nil
}

func (s *SQLiteStore) ReleaseWriter(ctx context.Context, lease *WriterLease) error {
	if lease == nil {
		return nil
	}
	return s.ReleaseLock(ctx, lease.lock)
}

func (s *SQLiteStore) acquireLock(ctx context.Context, lockPath string, owner WriterOwner) (*LockHandle, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	file, err := openLockFile(lockPath)
	if err != nil {
		return nil, err
	}
	if err := lockFileExclusive(file); err != nil {
		_ = file.Close()
		if isWindowsLockContention(err) {
			held := readLockOwner(lockPath)
			return nil, ErrLockContention{Path: lockPath, HolderHint: ownerHint(held), Operation: held.Operation, RepoID: held.RepoID, StartedAt: held.StartedAt, PID: held.PID, CachePath: held.CachePath}
		}
		return nil, err
	}
	if err := writeLockOwner(file, owner); err != nil {
		_ = unlockFile(file)
		_ = file.Close()
		return nil, err
	}
	return &LockHandle{file: file, path: lockPath, owner: owner}, nil
}

func (s *SQLiteStore) ReleaseLock(ctx context.Context, handle *LockHandle) error {
	_ = ctx
	if handle == nil || handle.file == nil {
		return nil
	}
	file := handle.file
	handle.file = nil
	_ = file.Truncate(0)
	_, _ = file.Seek(0, 0)
	unlockErr := unlockFile(file)
	closeErr := file.Close()
	if unlockErr != nil {
		return unlockErr
	}
	return closeErr
}

func lockFileExclusive(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.LockFileEx(
		windows.Handle(file.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0,
		wholeFileLockRange,
		wholeFileLockRange,
		&overlapped,
	)
}

func unlockFile(file *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(
		windows.Handle(file.Fd()),
		0,
		wholeFileLockRange,
		wholeFileLockRange,
		&overlapped,
	)
}

func isWindowsLockContention(err error) bool {
	return err == windows.ERROR_LOCK_VIOLATION || err == windows.ERROR_SHARING_VIOLATION
}

func writeLockOwner(file *os.File, owner WriterOwner) error {
	if owner.StartedAt.IsZero() {
		owner.StartedAt = time.Now().UTC()
	}
	if owner.PID == 0 {
		owner.PID = os.Getpid()
	}
	body, err := json.Marshal(owner)
	if err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	if _, err := file.Write(append(body, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func readLockOwner(lockPath string) WriterOwner {
	body, err := os.ReadFile(lockPath)
	if err != nil {
		return WriterOwner{}
	}
	var owner WriterOwner
	_ = json.Unmarshal(body, &owner)
	return owner
}

func ownerHint(owner WriterOwner) string {
	if owner.Operation == "" {
		return "another process holds the cache lock"
	}
	if owner.StartedAt.IsZero() {
		return fmt.Sprintf("writer %s holds the cache lock", owner.Operation)
	}
	return fmt.Sprintf("writer %s holds the cache lock since %s", owner.Operation, owner.StartedAt.Format(time.RFC3339Nano))
}
