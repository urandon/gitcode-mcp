//go:build darwin || linux

package localauthority

import (
	"os"
	"strings"

	"gitcode-mcp/internal/authority"
	"golang.org/x/sys/unix"
)

const recoveryJobsDirectory = "runtime-jobs"

func recoveryJobFileName(name string) bool {
	if name == "state.json" {
		return true
	}
	return strings.HasPrefix(name, "stage-") && strings.HasSuffix(name, ".json") && authority.UUID(strings.TrimSuffix(strings.TrimPrefix(name, "stage-"), ".json")).Valid()
}

func openRecoveryJobNamespace(root *os.File, create bool) (*os.File, bool, error) {
	created := false
	fd, err := unix.Openat(int(root.Fd()), recoveryJobsDirectory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == unix.ENOENT && create {
		if err = unix.Mkdirat(int(root.Fd()), recoveryJobsDirectory, 0700); err == nil {
			created = true
		} else if err != unix.EEXIST {
			return nil, false, Error{DurabilityUnavailable}
		}
		fd, err = unix.Openat(int(root.Fd()), recoveryJobsDirectory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return nil, false, Error{JobsCorrupt}
	}
	f := os.NewFile(uintptr(fd), "recovery-jobs-directory")
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0077 != 0 {
		_ = f.Close()
		return nil, false, Error{JobsCorrupt}
	}
	return f, created, nil
}

func openRecoveryJobFile(directory *os.File, name string, exclusive bool) (*os.File, bool, error) {
	if !recoveryJobFileName(name) {
		return nil, false, Error{JobsCorrupt}
	}
	// Prove the descriptor type without ever blocking on an attacker-replaced
	// FIFO. O_NONBLOCK is inert for the regular files admitted by fstat below.
	flags := unix.O_RDONLY | unix.O_NONBLOCK
	if exclusive {
		flags = unix.O_RDWR | unix.O_CREAT | unix.O_EXCL
	}
	fd, err := unix.Openat(int(directory.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err == unix.ENOENT && !exclusive {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, Error{JobsCorrupt}
	}
	f := os.NewFile(uintptr(fd), "recovery-job-metadata")
	var stat unix.Stat_t
	if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0077 != 0 {
		_ = f.Close()
		return nil, false, Error{JobsCorrupt}
	}
	return f, true, nil
}

func scanRecoveryJobDirectory(directory *os.File) (*os.File, error) {
	fd, err := unix.Openat(int(directory.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, Error{JobsCorrupt}
	}
	return os.NewFile(uintptr(fd), "recovery-job-scan"), nil
}

func publishRecoveryJobFile(directory *os.File, stage string) error {
	if !recoveryJobFileName(stage) || stage == "state.json" {
		return Error{JobsCorrupt}
	}
	if unix.Renameat(int(directory.Fd()), stage, int(directory.Fd()), "state.json") != nil {
		return Error{DurabilityUnavailable}
	}
	return nil
}
