//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package repositorydocs

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// readWorktreeFileNoFollow resolves every path component relative to an open
// worktree directory descriptor. O_NOFOLLOW on each open closes both the
// parent-symlink escape and the final-component check/open race.
func readWorktreeFileNoFollow(ctx context.Context, root, repoPath string, maxBytes int64) ([]byte, int64, error) {
	return readWorktreeFileNoFollowAfterOpen(ctx, root, repoPath, maxBytes, nil)
}

// readWorktreeFileNoFollowAfterOpen is split out so tests can deterministically
// replace the pathname after the final descriptor has been opened. Production
// callers always pass a nil hook.
func readWorktreeFileNoFollowAfterOpen(ctx context.Context, root, repoPath string, maxBytes int64, afterOpen func()) ([]byte, int64, error) {
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	parts := strings.Split(repoPath, "/")
	if len(parts) == 0 {
		return nil, 0, os.ErrInvalid
	}
	dirFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = unix.Close(dirFD) }()
	for _, part := range parts[:len(parts)-1] {
		if part == "" || part == "." || part == ".." {
			return nil, 0, os.ErrInvalid
		}
		nextFD, openErr := unix.Openat(dirFD, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if openErr != nil {
			return nil, 0, openErr
		}
		if closeErr := unix.Close(dirFD); closeErr != nil {
			unix.Close(nextFD)
			return nil, 0, closeErr
		}
		dirFD = nextFD
	}
	name := parts[len(parts)-1]
	if name == "" || name == "." || name == ".." {
		return nil, 0, os.ErrInvalid
	}
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, 0, err
	}
	file := os.NewFile(uintptr(fd), "repository-document")
	if file == nil {
		unix.Close(fd)
		return nil, 0, errors.New("open tracked worktree file")
	}
	defer file.Close()
	if afterOpen != nil {
		afterOpen()
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, 0, os.ErrInvalid
	}
	actualBytes := info.Size()
	if actualBytes > maxBytes {
		return nil, actualBytes, ErrDocumentTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, actualBytes, err
	}
	if int64(len(data)) > maxBytes {
		return nil, int64(len(data)), ErrDocumentTooLarge
	}
	if err := ctx.Err(); err != nil {
		return nil, actualBytes, err
	}
	return data, actualBytes, nil
}
