package servicectl

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
)

// durableAtomicWriteFile replaces path only after the complete payload and its
// permissions have reached the filesystem. Syncing the containing directory
// makes the rename durable across a process or machine restart on filesystems
// which implement directory fsync. Some platforms reject directory fsync; the
// file and atomic rename guarantees still apply there.
func durableAtomicWriteFile(path string, data []byte, mode os.FileMode) (err error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if err = tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmpPath, path); err != nil {
		return err
	}
	if err = syncDirectory(dir); err != nil {
		return err
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	err = dir.Sync()
	if err == nil || directorySyncUnsupported(err) {
		return nil
	}
	return err
}

func directorySyncUnsupported(err error) bool {
	if runtime.GOOS == "windows" {
		return true
	}
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EBADF)
}
