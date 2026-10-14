package localauthority

import "golang.org/x/sys/unix"

func exclusiveRepairRename(from int, old string, to int, next string) error {
	return unix.RenameatxNp(from, old, to, next, unix.RENAME_EXCL)
}
