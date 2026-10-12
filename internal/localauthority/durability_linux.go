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
	switch stat.Type {
	case unix.EXT4_SUPER_MAGIC, unix.XFS_SUPER_MAGIC, unix.BTRFS_SUPER_MAGIC:
	default:
		return Error{DurabilityUnavailable}
	}
	return syncDirectory(path)
}

func syncFile(f *os.File) error {
	if f.Sync() != nil {
		return Error{DurabilityUnavailable}
	}
	return nil
}
