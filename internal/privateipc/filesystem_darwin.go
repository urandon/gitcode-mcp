//go:build darwin

package privateipc

import "golang.org/x/sys/unix"

func endpointFilesystem(fd int) bool {
	var stat unix.Statfs_t
	if unix.Fstatfs(fd, &stat) != nil {
		return false
	}
	var name []byte
	for _, c := range stat.Fstypename {
		if c == 0 {
			break
		}
		name = append(name, byte(c))
	}
	return string(name) == "apfs" || string(name) == "hfs"
}
