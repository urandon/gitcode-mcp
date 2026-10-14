//go:build darwin || linux

package localauthority

import (
	"gitcode-mcp/internal/authority"
	"golang.org/x/sys/unix"
	"os"
	"strings"
)

const repairDirectory = "runtime-projection-repair"

func repairMetadataName(name string) bool {
	return name == "state.json" || strings.HasPrefix(name, "stage-") && strings.HasSuffix(name, ".json") && authority.UUID(strings.TrimSuffix(strings.TrimPrefix(name, "stage-"), ".json")).Valid()
}
func repairQuarantineName(id authority.UUID) string { return "quarantine-" + string(id) }
func repairQuarantineID(name string) authority.UUID {
	if !strings.HasPrefix(name, "quarantine-") {
		return ""
	}
	id := authority.UUID(strings.TrimPrefix(name, "quarantine-"))
	if !id.Valid() {
		return ""
	}
	return id
}

func openRepairSubdirectory(parent *os.File, name string, create bool) (*os.File, bool, error) {
	if name != repairDirectory && !repairQuarantineID(name).Valid() {
		return nil, false, Error{RepairCorrupt}
	}
	created := false
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err == unix.ENOENT {
		if !create {
			return nil, false, nil
		}
		if name != repairDirectory {
			return nil, false, Error{RepairCorrupt}
		}
		if err = unix.Mkdirat(int(parent.Fd()), name, 0700); err == nil {
			created = true
		} else if err != unix.EEXIST {
			return nil, false, Error{RepairCorrupt}
		}
		fd, err = unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return nil, false, Error{RepairCorrupt}
	}
	f := os.NewFile(uintptr(fd), "repair-directory")
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Uid != uint32(os.Geteuid()) || st.Mode&0077 != 0 {
		_ = f.Close()
		return nil, false, Error{RepairCorrupt}
	}
	return f, created, nil
}

func openRepairMetadata(parent *os.File, name string, exclusive bool) (*os.File, bool, error) {
	if !repairMetadataName(name) {
		return nil, false, Error{RepairCorrupt}
	}
	flags := unix.O_RDONLY | unix.O_NONBLOCK
	if exclusive {
		flags = unix.O_RDWR | unix.O_CREAT | unix.O_EXCL
	}
	fd, err := unix.Openat(int(parent.Fd()), name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err == unix.ENOENT && !exclusive {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, Error{RepairCorrupt}
	}
	f := os.NewFile(uintptr(fd), "repair-metadata")
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFREG || st.Nlink != 1 || st.Uid != uint32(os.Geteuid()) || st.Mode&0077 != 0 {
		_ = f.Close()
		return nil, false, Error{RepairCorrupt}
	}
	return f, true, nil
}

func publishRepairMetadata(directory *os.File, stage string) error {
	if stage == "state.json" || !repairMetadataName(stage) || unix.Renameat(int(directory.Fd()), stage, int(directory.Fd()), "state.json") != nil {
		return Error{RepairUncertain}
	}
	return nil
}
func moveProjectionToQuarantine(root, repair *os.File, id authority.UUID) error {
	if !id.Valid() {
		return Error{RepairCorrupt}
	}
	if exclusiveRepairRename(int(root.Fd()), projectionDirectory, int(repair.Fd()), repairQuarantineName(id)) != nil {
		return Error{RepairUncertain}
	}
	return nil
}
