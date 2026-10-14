package localauthority

import "golang.org/x/sys/unix"

func exclusiveRepairRename(from int, old string, to int, next string) error {
	return unix.Renameat2(from, old, to, next, unix.RENAME_NOREPLACE)
}
