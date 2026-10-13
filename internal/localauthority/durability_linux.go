package localauthority

import (
	"golang.org/x/sys/unix"
	"os"
)

func qualifyFilesystem(path string) error {
	f, err := openQualifiedDirectory(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return syncRegistryDirectory(f)
}

func qualifyFilesystemFD(f *os.File) error {
	var stat unix.Statfs_t
	if unix.Fstatfs(int(f.Fd()), &stat) != nil {
		return Error{DurabilityUnavailable}
	}
	switch stat.Type {
	case unix.EXT4_SUPER_MAGIC, unix.XFS_SUPER_MAGIC, unix.BTRFS_SUPER_MAGIC:
	default:
		return Error{DurabilityUnavailable}
	}
	return nil
}

func syncFile(f *os.File) error {
	if f.Sync() != nil {
		return Error{DurabilityUnavailable}
	}
	return nil
}
