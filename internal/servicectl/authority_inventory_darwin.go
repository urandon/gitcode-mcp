//go:build darwin && cgo

package servicectl

/*
#include <libproc.h>
#include <sys/proc_info.h>
#include <unistd.h>
#include <sys/socket.h>

static int authority_inventory(int *out) {
    struct proc_fdinfo records[129];
    int bytes = proc_pidinfo(getpid(), PROC_PIDLISTFDS, 0, records, sizeof(records));
    if (bytes <= 0 || bytes % sizeof(records[0]) != 0) return -1;
    int count = bytes / sizeof(records[0]);
    if (count > 128) return -1;
    for (int i = 0; i < count; i++) out[i] = records[i].proc_fd;
    return count;
}

// XNU does not implement SO_ACCEPTCONN in getsockopt. Native socket fdinfo
// exposes the listener option; do not infer it from a caller pathname.
static int authority_accepting(int fd) {
    struct socket_fdinfo info;
    int bytes = proc_pidfdinfo(getpid(), fd, PROC_PIDFDSOCKETINFO, &info, sizeof(info));
    if (bytes != sizeof(info) || info.psi.soi_family != AF_UNIX || info.psi.soi_type != SOCK_STREAM) return -1;
    return (info.psi.soi_options & SO_ACCEPTCONN) != 0;
}
*/
import "C"

import "gitcode-mcp/internal/authority"

func authorityDescriptorInventory() ([]int, error) {
	var fds [128]C.int
	n := int(C.authority_inventory(&fds[0]))
	if n < 0 {
		return nil, authority.Error{Code: authority.PolicyDenied}
	}
	out := make([]int, n)
	for i := range out {
		out[i] = int(fds[i])
	}
	return out, nil
}

func authorityAccepting(fd int) (bool, error) {
	n := int(C.authority_accepting(C.int(fd)))
	if n < 0 {
		return false, authority.Error{Code: authority.PolicyDenied}
	}
	return n != 0, nil
}
