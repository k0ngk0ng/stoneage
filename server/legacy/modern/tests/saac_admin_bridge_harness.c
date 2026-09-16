#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <errno.h>
#include <unistd.h>
#include <fcntl.h>
#include <dirent.h>
#include <sys/stat.h>
#include <sys/types.h>

char chardir[64] = "char";
int stoneage_admin_bridge_test_locked = 0;

static unsigned long bridge_test_mkdir_calls;
static unsigned long bridge_test_chmod_calls;
static unsigned long bridge_test_lstat_calls;
static unsigned long bridge_test_opendir_calls;

static int bridge_test_mkdir(const char *, mode_t);
static int bridge_test_chmod(const char *, mode_t);
static int bridge_test_lstat(const char *, struct stat *);
static DIR *bridge_test_opendir(const char *);

#define STONEAGE_ADMIN_BRIDGE_TEST
#define mkdir bridge_test_mkdir
#define chmod bridge_test_chmod
#define lstat bridge_test_lstat
#define opendir bridge_test_opendir
#include "../saac_admin_bridge.c"
#undef mkdir
#undef chmod
#undef lstat
#undef opendir

static int bridge_test_mkdir(const char *path, mode_t mode)
{
    bridge_test_mkdir_calls++;
    return mkdir(path, mode);
}

static int bridge_test_chmod(const char *path, mode_t mode)
{
    bridge_test_chmod_calls++;
    return chmod(path, mode);
}

static int bridge_test_lstat(const char *path, struct stat *info)
{
    bridge_test_lstat_calls++;
    return lstat(path, info);
}

static DIR *bridge_test_opendir(const char *path)
{
    bridge_test_opendir_calls++;
    return opendir(path);
}

static int make_directory(const char *path)
{
    if (mkdir(path, 0700) < 0 && errno != EEXIST) return -1;
    return 0;
}

static int write_all(int fd, const unsigned char *data, size_t length)
{
    size_t offset = 0;
    ssize_t written;
    while (offset < length) {
        written = write(fd, data + offset, length - offset);
        if (written <= 0) return -1;
        offset += (size_t)written;
    }
    return 0;
}

/* The production bridge deliberately scans at a bounded cadence. Keep the
 * harness calls on that same schedule instead of relying on filesystem work
 * to take long enough to advance the poll clock. */
static void poll_bridge(void)
{
    usleep(30000);
    stoneage_admin_bridge_poll();
}

static int write_request(const char *id, const char *text, size_t text_length,
                         const unsigned char *payload, size_t payload_length)
{
    char temporary[256];
    char final[256];
    int fd;

    snprintf(temporary, sizeof(temporary), "%s/.%s.req.tmp", "admin-bridge/requests", id);
    snprintf(final, sizeof(final), "%s/%s.req", "admin-bridge/requests", id);
    fd = open(temporary, O_WRONLY | O_CREAT | O_TRUNC, 0600);
    if (fd < 0) return -1;
    if (write_all(fd, (const unsigned char *)text, text_length) < 0 ||
        write_all(fd, payload, payload_length) < 0 || close(fd) < 0) {
        close(fd);
        return -1;
    }
    if (rename(temporary, final) < 0) return -1;
    return 0;
}

static int response_has(const char *id, const char *needle)
{
    char path[256];
    FILE *fp;
    char data[4096];
    size_t length;

    snprintf(path, sizeof(path), "%s/%s.resp", "admin-bridge/responses", id);
    fp = fopen(path, "rb");
    if (fp == NULL) return 0;
    length = fread(data, 1, sizeof(data) - 1, fp);
    fclose(fp);
    data[length] = 0;
    return strstr(data, needle) != NULL;
}

static int archive_path(char *out, int out_length, const char *account, int slot)
{
    int hash = 0;
    int i;
    for (i = 0; account[i] != 0; i++) hash += account[i];
    return snprintf(out, out_length, "char/0x%x/%s.%d.char", hash & 0xff,
                    account, slot) < out_length;
}

static int write_archive(const char *account, int slot, const unsigned char *data,
                         size_t length)
{
    char path[256];
    char directory[128];
    int hash = 0;
    int i;
    int fd;

    for (i = 0; account[i] != 0; i++) hash += account[i];
    snprintf(directory, sizeof(directory), "char/0x%x", hash & 0xff);
    if (make_directory("char") < 0 || make_directory(directory) < 0) return -1;
    if (!archive_path(path, sizeof(path), account, slot)) return -1;
    fd = open(path, O_WRONLY | O_CREAT | O_TRUNC, 0600);
    if (fd < 0) return -1;
    if (write_all(fd, data, length) < 0 || close(fd) < 0) return -1;
    return 0;
}

static int read_archive(const char *account, int slot, unsigned char *data,
                        size_t capacity, size_t *length)
{
    char path[256];
    int fd;
    ssize_t got;
    size_t offset = 0;

    if (!archive_path(path, sizeof(path), account, slot)) return -1;
    fd = open(path, O_RDONLY);
    if (fd < 0) return -1;
    while (offset < capacity) {
        got = read(fd, data + offset, capacity - offset);
        if (got < 0) { close(fd); return -1; }
        if (got == 0) break;
        offset += (size_t)got;
    }
    close(fd);
    *length = offset;
    return 0;
}

static int expect(int condition, const char *message)
{
    if (condition) return 0;
    fprintf(stderr, "saac bridge harness: %s\n", message);
    return 1;
}

int main(int argc, char **argv)
{
    const unsigned char original[] = {
        'n', 'a', 'm', 'e', '=', 'o', 'r', 'i', 'g', 'i', 'n', 'a', 'l',
        '|', 'r', 'a', 'w', '=', 0x00, 0xff
    };
    const unsigned char replacement[] = {
        'n', 'a', 'm', 'e', '=', 'r', 'e', 'p', 'l', 'a', 'c', 'e', 'm', 'e', 'n', 't',
        '|', 'r', 'a', 'w', '=', 0x00, 0xff
    };
    const unsigned char dotted_original[] = {
        'n', 'a', 'm', 'e', '=', 'd', 'o', 't', 't', 'e', 'd',
        '|', 'r', 'a', 'w', '=', 0x00, 0xfe
    };
    const unsigned char dotted_replacement[] = {
        'n', 'a', 'm', 'e', '=', 'd', 'o', 't', 't', 'e', 'd', '-', 'n', 'e', 'w',
        '|', 'r', 'a', 'w', '=', 0x00, 0xfd
    };
    const unsigned char wrong[] = {'n', 'a', 'm', 'e', '=', 'w', 'r', 'o', 'n', 'g'};
    unsigned char online_payload[128];
    unsigned char conflict_payload[128];
    unsigned char dotted_payload[128];
    size_t online_payload_length;
    size_t conflict_payload_length;
    size_t dotted_payload_length;
    char request[512];
    unsigned char archive[256];
    size_t archive_length;
    char bridge_path[256];
    char archive_temp[256];
    char read_length[64];
    int errors = 0;

    if (argc != 2 || chdir(argv[1]) < 0) return 2;
    snprintf(bridge_path, sizeof(bridge_path), "%s/admin-bridge", argv[1]);
    if (setenv("STONEAGE_PLAYER_ADMIN_DIR", bridge_path, 1) < 0) return 2;
    if (make_directory("admin-bridge") < 0 ||
        make_directory("admin-bridge/requests") < 0 ||
        make_directory("admin-bridge/responses") < 0 ||
        write_archive("alice", 0,
        original, sizeof(original) - 1) < 0 ||
        write_archive("qa.slot2", 1,
        dotted_original, sizeof(dotted_original) - 1) < 0) return 2;
    if (stoneage_admin_bridge_init() < 0) return 2;
    {
        unsigned long mkdir_calls = bridge_test_mkdir_calls;
        unsigned long chmod_calls = bridge_test_chmod_calls;
        unsigned long lstat_calls = bridge_test_lstat_calls;
        unsigned long opendir_calls;
        int i;
        /* The first poll is allowed to inspect the empty queue. Calls made
         * during the same 25 ms window must return before opening it again. */
        stoneage_admin_bridge_poll();
        opendir_calls = bridge_test_opendir_calls;
        for (i = 0; i < 8; i++) stoneage_admin_bridge_poll();
        errors += expect(bridge_test_mkdir_calls == mkdir_calls,
                         "dense polls repeated mkdir");
        errors += expect(bridge_test_chmod_calls == chmod_calls,
                         "dense polls repeated chmod");
        errors += expect(bridge_test_lstat_calls == lstat_calls,
                         "dense polls repeated lstat");
        errors += expect(bridge_test_opendir_calls == opendir_calls,
                         "dense polls repeated opendir");
    }
    online_payload_length = sizeof(original) - 1 + sizeof(replacement) - 1;
    memcpy(online_payload, original, sizeof(original) - 1);
    memcpy(online_payload + sizeof(original) - 1, replacement,
           sizeof(replacement) - 1);
    conflict_payload_length = sizeof(wrong) - 1 + sizeof(replacement) - 1;
    memcpy(conflict_payload, wrong, sizeof(wrong) - 1);
    memcpy(conflict_payload + sizeof(wrong) - 1, replacement,
           sizeof(replacement) - 1);
    memcpy(dotted_payload, dotted_original, sizeof(dotted_original) - 1);
    memcpy(dotted_payload + sizeof(dotted_original) - 1, dotted_replacement,
           sizeof(dotted_replacement) - 1);
    dotted_payload_length = sizeof(dotted_original) - 1 +
                           sizeof(dotted_replacement) - 1;

    snprintf(request, sizeof(request),
             "version=1\nid=online\nop=write\naccount=alice\nslot=0\n"
             "expected-length=%lu\nnew-length=%lu\n---\n",
             (unsigned long)(sizeof(original) - 1),
             (unsigned long)(sizeof(replacement) - 1));
    stoneage_admin_bridge_test_locked = 1;
    errors += expect(write_request("online", request, strlen(request),
                                   online_payload, online_payload_length) == 0,
                     "write online request");
    if (!errors) poll_bridge();
    errors += expect(response_has("online", "code=online\n"), "online write rejected");
    errors += expect(read_archive("alice", 0, archive, sizeof(archive),
                                  &archive_length) == 0 && archive_length == sizeof(original) - 1 &&
                    memcmp(archive, original, archive_length) == 0,
                    "online write changed archive");

    snprintf(request, sizeof(request),
             "version=1\nid=conflict\nop=write\naccount=alice\nslot=0\n"
             "expected-length=%lu\nnew-length=%lu\n---\n",
             (unsigned long)(sizeof(wrong) - 1),
             (unsigned long)(sizeof(replacement) - 1));
    stoneage_admin_bridge_test_locked = 0;
    errors += expect(write_request("conflict", request, strlen(request),
                                   conflict_payload, conflict_payload_length) == 0,
                     "write conflict request");
    poll_bridge();
    errors += expect(response_has("conflict", "code=conflict\n"), "CAS conflict rejected");

    snprintf(request, sizeof(request),
             "version=1\nid=save\nop=write\naccount=alice\nslot=0\n"
             "expected-length=%lu\nnew-length=%lu\n---\n",
             (unsigned long)(sizeof(original) - 1),
             (unsigned long)(sizeof(replacement) - 1));
    errors += expect(write_request("save", request, strlen(request),
                                   online_payload, online_payload_length) == 0,
                     "write save request");
    poll_bridge();
    errors += expect(response_has("save", "code=ok\n"), "atomic save failed");
    errors += expect(read_archive("alice", 0, archive, sizeof(archive),
                                  &archive_length) == 0 && archive_length == sizeof(replacement) - 1 &&
                    memcmp(archive, replacement, archive_length) == 0,
                    "atomic save bytes changed");
    if (archive_path(archive_temp, sizeof(archive_temp), "alice", 0)) {
        snprintf(archive_temp + strlen(archive_temp),
                 sizeof(archive_temp) - strlen(archive_temp),
                 ".admin-save.tmp");
        errors += expect(access(archive_temp, F_OK) < 0,
                         "atomic save left temporary archive");
    }

    snprintf(request, sizeof(request),
             "version=1\nid=list\nop=list\naccount=alice\n---\n");
    errors += expect(write_request("list", request, strlen(request), NULL, 0) == 0,
                     "list request");
    poll_bridge();
    errors += expect(response_has("list", "code=ok\n") &&
                     response_has("list", "present=1\n"), "list failed");

    snprintf(request, sizeof(request),
             "version=1\nid=read\nop=read\naccount=alice\nslot=0\n---\n");
    errors += expect(write_request("read", request, strlen(request), NULL, 0) == 0,
                     "read request");
    poll_bridge();
    snprintf(read_length, sizeof(read_length), "length=%lu\n",
             (unsigned long)(sizeof(replacement) - 1));
    errors += expect(response_has("read", "code=ok\n") &&
                     response_has("read", read_length), "read failed");

    snprintf(request, sizeof(request),
             "version=1\nid=dotted-read\nop=read\naccount=qa.slot2\nslot=1\n---\n");
    errors += expect(write_request("dotted-read", request, strlen(request), NULL, 0) == 0,
                     "dotted account read request");
    poll_bridge();
    snprintf(read_length, sizeof(read_length), "length=%lu\n",
             (unsigned long)(sizeof(dotted_original) - 1));
    errors += expect(response_has("dotted-read", "code=ok\n") &&
                     response_has("dotted-read", read_length),
                     "dotted account read rejected");

    snprintf(request, sizeof(request),
             "version=1\nid=dotted-write\nop=write\naccount=qa.slot2\nslot=1\n"
             "expected-length=%lu\nnew-length=%lu\n---\n",
             (unsigned long)(sizeof(dotted_original) - 1),
             (unsigned long)(sizeof(dotted_replacement) - 1));
    errors += expect(write_request("dotted-write", request, strlen(request),
                                   dotted_payload,
                                   dotted_payload_length) == 0,
                     "dotted account write request");
    poll_bridge();
    errors += expect(response_has("dotted-write", "code=ok\n"),
                     "dotted account write rejected");
    errors += expect(read_archive("qa.slot2", 1, archive, sizeof(archive),
                                  &archive_length) == 0 &&
                    archive_length == sizeof(dotted_replacement) - 1 &&
                    memcmp(archive, dotted_replacement, archive_length) == 0,
                    "dotted account write bytes changed");

    snprintf(request, sizeof(request),
             "version=1\nid=path\nop=read\naccount=../escape\nslot=0\n---\n");
    errors += expect(write_request("path", request, strlen(request), NULL, 0) == 0,
                     "path request");
    poll_bridge();
    errors += expect(response_has("path", "code=bad_request\n"), "path rejected");

    snprintf(request, sizeof(request),
             "version=1\nid=slot\nop=read\naccount=alice\nslot=2\n---\n");
    errors += expect(write_request("slot", request, strlen(request), NULL, 0) == 0,
                     "slot request");
    poll_bridge();
    errors += expect(response_has("slot", "code=bad_request\n"), "slot rejected");

    snprintf(request, sizeof(request),
             "version=1\nid=range\nop=write\naccount=alice\nslot=0\n"
             "expected-length=0\nnew-length=65536\n---\n");
    errors += expect(write_request("range", request, strlen(request), NULL, 0) == 0,
                     "range request");
    poll_bridge();
    errors += expect(response_has("range", "code=bad_request\n"), "range rejected");

    return errors == 0 ? 0 : 1;
}
