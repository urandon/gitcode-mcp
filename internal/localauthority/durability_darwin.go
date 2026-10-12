package localauthority

import (
	"golang.org/x/sys/unix"
	"os"
)

func qualifyFilesystem(path string) error {
	var stat unix.Statfs_t
	if unix.Statfs(path, &stat) != nil {
		return Error{DurabilityUnavailable}
	}
	var name []byte
	for _, c := range stat.Fstypename {
		if c == 0 {
			break
		}
		name = append(name, byte(c))
	}
	if string(name) != "apfs" && string(name) != "hfs" {
		return Error{DurabilityUnavailable}
	}
	return syncDirectory(path)
}

func syncFile(f *os.File) error {
	if err := f.Sync(); err != nil {
		return Error{DurabilityUnavailable}
	}
	if _, err := unix.FcntlInt(f.Fd(), unix.F_FULLFSYNC, 0); err != nil {
		return Error{DurabilityUnavailable}
	}
	return nil
}
