#include <mach/mach.h>
#include <mach/task_special_ports.h>
#include <spawn.h>
#include <sys/wait.h>
#include <signal.h>
#include <errno.h>
#include <unistd.h>
#include <fcntl.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

/* No grant or authority object. Header-only kernel provenance experiment. */
_Static_assert(sizeof(mach_msg_header_t) == 24, "header ABI");
_Static_assert(sizeof(mach_msg_audit_trailer_t) == 52, "audit ABI");
typedef union { uint64_t align; unsigned char bytes[76]; } envelope;

static int make_queue(mach_port_t *p) {
    mach_port_options_t opts = {0};
    opts.flags = MPO_QLIMIT | MPO_INSERT_SEND_RIGHT;
    opts.mpl.mpl_qlimit = 1;
    return mach_port_construct(mach_task_self(), &opts, 0, p) == KERN_SUCCESS ? 0 : 1;
}
static int close_queue(mach_port_t p) {
    return p == MACH_PORT_NULL || mach_port_destruct(mach_task_self(), p, -1, 0) == KERN_SUCCESS ? 0 : 1;
}
static int send_header(mach_port_t dest, mach_port_t reply, uint32_t word) {
    mach_msg_header_t h = {0};
    h.msgh_bits = MACH_MSGH_BITS(MACH_MSG_TYPE_COPY_SEND, reply ? MACH_MSG_TYPE_MAKE_SEND : 0);
    h.msgh_size = sizeof(h);
    h.msgh_remote_port = dest;
    h.msgh_local_port = reply;
    h.msgh_id = (mach_msg_id_t)word;
    return mach_msg(&h, MACH_SEND_MSG | MACH_SEND_TIMEOUT, sizeof(h), 0,
                    MACH_PORT_NULL, 1000, MACH_PORT_NULL) == MACH_MSG_SUCCESS ? 0 : 1;
}
static mach_msg_return_t receive_header(mach_port_t queue, envelope *b, int timeout) {
    memset(b, 0, sizeof(*b));
    return mach_msg((mach_msg_header_t *)b->bytes,
                    MACH_RCV_MSG | MACH_RCV_TIMEOUT |
                    MACH_RCV_TRAILER_TYPE(MACH_MSG_TRAILER_FORMAT_0) |
                    MACH_RCV_TRAILER_ELEMENTS(MACH_RCV_TRAILER_AUDIT),
                    0, sizeof(b->bytes), queue, timeout, MACH_PORT_NULL);
}
static int valid_header(envelope *b, mach_port_t queue, pid_t pid, unsigned seq, uint32_t word, int reply) {
    mach_msg_header_t *h = (mach_msg_header_t *)b->bytes;
    if (h->msgh_size != 24 || (uint32_t)h->msgh_id != word || h->msgh_local_port != queue ||
        h->msgh_voucher_port != MACH_PORT_NULL ||
        h->msgh_bits != MACH_MSGH_BITS(reply ? MACH_MSG_TYPE_PORT_SEND : 0, MACH_MSG_TYPE_PORT_SEND) ||
        (reply ? !MACH_PORT_VALID(h->msgh_remote_port) : h->msgh_remote_port != MACH_PORT_NULL)) return 1;
    mach_msg_audit_trailer_t *tr = (mach_msg_audit_trailer_t *)(b->bytes + 24);
    return tr->msgh_trailer_type != MACH_MSG_TRAILER_FORMAT_0 || tr->msgh_trailer_size != 52 ||
        tr->msgh_seqno != seq || tr->msgh_audit.val[5] != (uint32_t)pid ||
        tr->msgh_audit.val[1] != getuid() || tr->msgh_audit.val[3] != getuid() || tr->msgh_audit.val[7] == 0;
}
static int port_count(unsigned *count) {
    mach_port_name_array_t names = NULL;
    mach_port_type_array_t types = NULL;
    mach_msg_type_number_t n = 0, t = 0;
    if (mach_port_names(mach_task_self(), &names, &n, &types, &t) != KERN_SUCCESS) return 1;
    int result = n != t;
    *count = n;
    if (vm_deallocate(mach_task_self(), (vm_address_t)names, n * sizeof(*names)) != KERN_SUCCESS) result = 1;
    if (vm_deallocate(mach_task_self(), (vm_address_t)types, t * sizeof(*types)) != KERN_SUCCESS) result = 1;
    return result;
}
static int hostile(mach_port_t dest, const char *mode) {
    struct { mach_msg_header_t h; mach_msg_body_t b; mach_msg_ool_descriptor_t d; } memory = {0};
    struct { mach_msg_header_t h; mach_msg_body_t b; mach_msg_ool_ports_descriptor_t d; } ports = {0};
    struct { mach_msg_header_t h; uint32_t value; } inline_data = {0};
    mach_msg_header_t *h = NULL;
    void *data = NULL;
    mach_port_t object = MACH_PORT_NULL;
    mach_port_t array[8] = {0};
    int result = 1;
    if (!strcmp(mode, "inline")) {
        h = &inline_data.h;
        h->msgh_size = sizeof(inline_data);
    } else if (!strcmp(mode, "ool")) {
        data = calloc(1, 1024 * 1024);
        if (!data) goto done;
        h = &memory.h;
        h->msgh_size = sizeof(memory);
        memory.b.msgh_descriptor_count = 1;
        memory.d.address = data;
        memory.d.size = 1024 * 1024;
        memory.d.copy = MACH_MSG_VIRTUAL_COPY;
        memory.d.type = MACH_MSG_OOL_DESCRIPTOR;
    } else if (!strcmp(mode, "ports")) {
        if (make_queue(&object)) goto done;
        for (int i = 0; i < 8; i++) array[i] = object;
        h = &ports.h;
        h->msgh_size = sizeof(ports);
        ports.b.msgh_descriptor_count = 1;
        ports.d.address = array;
        ports.d.count = 8;
        ports.d.copy = MACH_MSG_VIRTUAL_COPY;
        ports.d.disposition = MACH_MSG_TYPE_COPY_SEND;
        ports.d.type = MACH_MSG_OOL_PORTS_DESCRIPTOR;
    } else goto done;
    h->msgh_bits = MACH_MSGH_BITS(MACH_MSG_TYPE_COPY_SEND, 0) |
        (!strcmp(mode, "inline") ? 0 : MACH_MSGH_BITS_COMPLEX);
    h->msgh_remote_port = dest;
    h->msgh_id = 179183;
    result = mach_msg(h, MACH_SEND_MSG | MACH_SEND_TIMEOUT, h->msgh_size, 0,
                    MACH_PORT_NULL, 1000, MACH_PORT_NULL) == MACH_MSG_SUCCESS ? 0 : 1;
done:
    if (close_queue(object)) result = 1;
    free(data);
    return result;
}
static int helper(const char *mode) {
    mach_port_t dest = MACH_PORT_NULL, reply = MACH_PORT_NULL;
    int result = 1;
    if (task_get_special_port(mach_task_self(), TASK_BOOTSTRAP_PORT, &dest) != KERN_SUCCESS) goto done;
    if (strcmp(mode, "valid")) { result = hostile(dest, mode); goto done; }
    if (make_queue(&reply)) goto done;
    for (unsigned i = 0; i < 8; i++) if (send_header(dest, i ? MACH_PORT_NULL : reply, 0x17918300u + i)) goto done;
    envelope b;
    if (receive_header(reply, &b, 1000) != MACH_MSG_SUCCESS) goto done;
    result = valid_header(&b, reply, getppid(), 0, 0x51, 0);
    mach_msg_destroy((mach_msg_header_t *)b.bytes);
done:
    if (dest && mach_port_deallocate(mach_task_self(), dest) != KERN_SUCCESS) result = 1;
    if (close_queue(reply)) result = 1;
    return result;
}
static int spawn_child(const char *self, mach_port_t port, const char *mode, pid_t *pid) {
    posix_spawnattr_t attrs;
    posix_spawn_file_actions_t actions;
    if (posix_spawnattr_init(&attrs)) return 1;
    if (posix_spawn_file_actions_init(&actions)) { posix_spawnattr_destroy(&attrs); return 1; }
    int err = posix_spawnattr_setflags(&attrs, POSIX_SPAWN_CLOEXEC_DEFAULT);
    if (!err) err = posix_spawnattr_setspecialport_np(&attrs, port, TASK_BOOTSTRAP_PORT);
    for (int i = 0; i < 3 && !err; i++) err = posix_spawn_file_actions_addopen(&actions, i, "/dev/null", i ? O_WRONLY : O_RDONLY, 0);
    char *argv[] = {(char *)self, "header-native-helper", (char *)mode, NULL};
    char *env[] = {NULL};
    if (!err) err = posix_spawn(pid, self, &actions, &attrs, argv, env);
    posix_spawn_file_actions_destroy(&actions);
    posix_spawnattr_destroy(&attrs);
    return err ? 1 : 0;
}
static int join(pid_t pid, int abort) {
    int status = 0;
    if (abort) kill(pid, SIGKILL);
    for (int i = 0; i < 200; i++) {
        pid_t got = waitpid(pid, &status, WNOHANG);
        if (got == pid) return !abort && WIFEXITED(status) && WEXITSTATUS(status) == 0 ? 0 : 1;
        if (got < 0 && errno == EINTR) continue;
        if (got < 0) {
            kill(pid, SIGKILL);
            while (waitpid(pid, &status, 0) < 0 && errno == EINTR) {}
            return 1;
        }
        usleep(10000);
    }
    kill(pid, SIGKILL);
    while (waitpid(pid, &status, 0) < 0 && errno == EINTR) {}
    return 1;
}
int main(int argc, char **argv) {
    /* The opt-in Go harness owns the whole isolated process-group deadline. */
    if (argc == 3 && !strcmp(argv[1], "header-native-helper")) return helper(argv[2]);
    if (argc != 2 || getuid() != geteuid() ||
        (strcmp(argv[1], "valid") && strcmp(argv[1], "inline") && strcmp(argv[1], "ool") && strcmp(argv[1], "ports"))) return 1;
    mach_port_t q = MACH_PORT_NULL, reply = MACH_PORT_NULL;
    pid_t pid = 0;
    int result = 1, spawned = 0;
    unsigned before = 0, after = 0;
    if (make_queue(&q) || port_count(&before) || spawn_child(argv[0], q, argv[1], &pid)) goto done;
    spawned = 1;
    if (!strcmp(argv[1], "valid")) {
        audit_token_t pinned = {{0}};
        for (unsigned i = 0; i < 8; i++) {
            envelope b;
            if (receive_header(q, &b, 1000) != MACH_MSG_SUCCESS) goto done;
            mach_msg_header_t *h = (mach_msg_header_t *)b.bytes;
            int bad = valid_header(&b, q, pid, i, 0x17918300u + i, i == 0);
            mach_msg_audit_trailer_t *tr = (mach_msg_audit_trailer_t *)(b.bytes + 24);
            if (!bad && i == 0) pinned = tr->msgh_audit;
            if (!bad && memcmp(&pinned, &tr->msgh_audit, sizeof(pinned))) bad = 1;
            if (!bad && i == 0) { reply = h->msgh_remote_port; h->msgh_remote_port = MACH_PORT_NULL; h->msgh_bits &= ~MACH_MSGH_BITS_REMOTE_MASK; }
            mach_msg_destroy(h);
            if (bad) goto done;
        }
        if (send_header(reply, MACH_PORT_NULL, 0x51)) goto done;
        if (mach_port_deallocate(mach_task_self(), reply) != KERN_SUCCESS) goto done;
        reply = MACH_PORT_NULL;
        if (port_count(&after) || after != before) goto done;
    } else {
        envelope b;
        if (receive_header(q, &b, 1000) != MACH_RCV_TOO_LARGE) goto done;
        /* This error is never origin evidence. Kernel already destroyed body/rights. */
        mach_msg_header_t *h = (mach_msg_header_t *)b.bytes;
        if (h->msgh_remote_port != MACH_PORT_NULL || h->msgh_voucher_port != MACH_PORT_NULL) goto done;
        if (port_count(&after) || after != before) goto done;
        if (receive_header(q, &b, 50) != MACH_RCV_TIMED_OUT) goto done;
    }
    result = 0;
done:
    if (spawned && join(pid, result != 0)) result = 1;
    if (reply && mach_port_deallocate(mach_task_self(), reply) != KERN_SUCCESS) result = 1;
    if (close_queue(q)) result = 1;
    puts(result ? "isolated header-only provenance qualification FAILED" : "isolated header-only provenance qualification PASS");
    return result;
}
