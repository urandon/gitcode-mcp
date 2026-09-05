//go:build windows

package config

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func acquireFeedbackSetupLock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("feedback setup: trusted configuration directory is unavailable")
	}
	lock, err := os.OpenFile(path+".feedback-setup.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("feedback setup: trusted configuration lock is unavailable")
	}
	var overlapped windows.Overlapped
	const wholeFile = ^uint32(0)
	err = windows.LockFileEx(windows.Handle(lock.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, wholeFile, wholeFile, &overlapped)
	if err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("feedback setup: another configuration update is active")
	}
	return func() {
		var unlockOverlapped windows.Overlapped
		_ = windows.UnlockFileEx(windows.Handle(lock.Fd()), 0, wholeFile, wholeFile, &unlockOverlapped)
		_ = lock.Close()
	}, nil
}
