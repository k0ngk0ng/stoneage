/*
 * File-only bridge for the authenticated administrator service.
 *
 * Requests are atomically renamed into the dedicated player-admin directory
 * by the caller.  The
 * SAAC main loop consumes them, so an archive write observes isLocked() in
 * exactly the same serialized state as the normal SAAC callbacks.  There is
 * no listener, command execution, or caller-selected filesystem path here.
 */

#ifndef STONEAGE_ADMIN_BRIDGE_TEST
#include "main.h"
#include "char.h"
#include "util.h"
#else
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <errno.h>
#include <ctype.h>
#include <unistd.h>
#include <fcntl.h>
#include <dirent.h>
#include <sys/types.h>
#include <sys/stat.h>
#include <limits.h>

#define CHARDATASIZE 65536
#define MAXCHAR_PER_USER 2
#define STONEAGE_SAAC_ADMIN_BRIDGE_DIR "/run/stoneage/player-admin/saac"
extern char chardir[64];
extern int stoneage_admin_bridge_test_locked;
static int isLocked(char *id) { (void)id; return stoneage_admin_bridge_test_locked; }
static int getHash(char *id)
{
    int i;
    int h = 0;
    for (i = 0; id[i] != 0; i++) h += id[i];
    return h;
}
static void makeDirFilename(char *out, int outlen, char *base, int hash,
                            char *child)
{
    snprintf(out, outlen, "%s/0x%x/%s", base, hash & 0xff, child);
}
#define log(...) ((void)0)
#endif

#include "saac_admin_bridge.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <errno.h>
#include <ctype.h>
#include <unistd.h>
#include <fcntl.h>
#include <dirent.h>
#include <sys/types.h>
#include <sys/stat.h>

#ifndef PATH_MAX
#define PATH_MAX 4096
#endif

#define BRIDGE_ID_MAX 64
#define BRIDGE_ACCOUNT_MAX 31
#define BRIDGE_HEADER_MAX 4096
#define BRIDGE_ARCHIVE_MAX (CHARDATASIZE - 1)
#define BRIDGE_REQUEST_MAX (BRIDGE_HEADER_MAX + BRIDGE_ARCHIVE_MAX * 2)

struct bridge_request {
    char id[BRIDGE_ID_MAX + 1];
    char op[8];
    char account[BRIDGE_ACCOUNT_MAX + 1];
    int slot;
    int expected_length;
    int new_length;
    unsigned char *payload;
    unsigned char *allocation;
    size_t payload_length;
    int have_id;
    int have_op;
    int have_account;
    int have_slot;
    int have_expected_length;
    int have_new_length;
};

struct bridge_response {
    char op[8];
    char account[BRIDGE_ACCOUNT_MAX + 1];
    char code[32];
    int slot;
    int length;
    int count;
    int present[MAXCHAR_PER_USER];
    int slot_length[MAXCHAR_PER_USER];
    const unsigned char *payload;
    size_t payload_length;
};

static const char *bridge_directory(void)
{
    const char *configured = getenv("STONEAGE_PLAYER_ADMIN_DIR");
    size_t length;
    size_t i;

    /* This is process configuration, never a request field. Require an
       absolute path and reject traversal/control bytes before using it. */
    if (configured == NULL || configured[0] != '/')
        return STONEAGE_SAAC_ADMIN_BRIDGE_DIR;
    length = strlen(configured);
    if (length == 0 || length >= PATH_MAX) return STONEAGE_SAAC_ADMIN_BRIDGE_DIR;
    for (i = 0; i < length; i++) {
        if ((unsigned char)configured[i] < 0x20 || configured[i] == '\\')
            return STONEAGE_SAAC_ADMIN_BRIDGE_DIR;
    }
    if (strstr(configured, "/../") != NULL ||
        (length >= 3 && strcmp(configured + length - 3, "/..") == 0))
        return STONEAGE_SAAC_ADMIN_BRIDGE_DIR;
    return configured;
}

static int bridge_subdirectory(char *out, int out_length, const char *name)
{
    return snprintf(out, out_length, "%s/%s", bridge_directory(), name)
           < out_length;
}

static int bridge_safe_id(const char *value, int max)
{
    int i;
    int length;

    if (value == NULL) return 0;
    length = (int)strlen(value);
    if (length < 1 || length > max) return 0;
    for (i = 0; i < length; i++) {
        unsigned char c = (unsigned char)value[i];
        if (!(isalnum(c) || c == '_' || c == '-' || c == '.')) return 0;
    }
    if (strcmp(value, ".") == 0 || strcmp(value, "..") == 0) return 0;
    return 1;
}

static int bridge_safe_account(const char *value)
{
    int i;
    int length;

    if (value == NULL) return 0;
    length = (int)strlen(value);
    if (length < 1 || length > BRIDGE_ACCOUNT_MAX) return 0;
    for (i = 0; i < length; i++) {
        unsigned char c = (unsigned char)value[i];
        if (!(isalnum(c) || c == '_' || c == '-' || c == '.')) return 0;
    }
    return 1;
}

static int bridge_decimal(const char *value, int *out, int allow_zero)
{
    unsigned long number;
    char *end;

    if (value == NULL || value[0] == 0) return 0;
    if (value[0] == '-') return 0;
    errno = 0;
    number = strtoul(value, &end, 10);
    if (errno != 0 || *end != 0 || number > 65535UL) return 0;
    if (!allow_zero && number == 0) return 0;
    *out = (int)number;
    return 1;
}

static int bridge_copy_value(char *out, int outlen, const char *value)
{
    int length;

    if (value == NULL) return 0;
    length = (int)strlen(value);
    if (length < 1 || length >= outlen) return 0;
    memcpy(out, value, (size_t)length + 1);
    return 1;
}

static int bridge_line(const unsigned char *data, size_t length, size_t *offset,
                       char *line, int line_length)
{
    size_t start;
    size_t end;
    size_t copy_length;

    start = *offset;
    while (*offset < length && data[*offset] != '\n') (*offset)++;
    if (*offset >= length) return 0;
    end = *offset;
    (*offset)++;
    if (end > start && data[end - 1] == '\r') end--;
    copy_length = end - start;
    if (copy_length == 0 || copy_length >= (size_t)line_length) return 0;
    memcpy(line, data + start, copy_length);
    line[copy_length] = 0;
    return 1;
}

static int bridge_field(char *line, char **key, char **value)
{
    char *separator = strchr(line, '=');
    if (separator == NULL || separator == line || separator[1] == 0) return 0;
    *separator = 0;
    *key = line;
    *value = separator + 1;
    return 1;
}

static int bridge_read_file(const char *path, unsigned char **out,
                            int *length, int *present)
{
    int fd;
    struct stat info;
    unsigned char *data;
    ssize_t got;
    size_t offset;

    *out = NULL;
    *length = 0;
    *present = 0;
    fd = open(path, O_RDONLY | O_NOFOLLOW);
    if (fd < 0) {
        if (errno == ENOENT) return 0;
        return -1;
    }
    if (fstat(fd, &info) < 0 || !S_ISREG(info.st_mode)) {
        close(fd);
        return -1;
    }
    if (info.st_size <= 0 || info.st_size > BRIDGE_ARCHIVE_MAX) {
        close(fd);
        return -2;
    }
    data = (unsigned char *)malloc((size_t)info.st_size);
    if (data == NULL) {
        close(fd);
        return -1;
    }
    offset = 0;
    while (offset < (size_t)info.st_size) {
        got = read(fd, data + offset, (size_t)info.st_size - offset);
        if (got <= 0) {
            free(data);
            close(fd);
            return -1;
        }
        offset += (size_t)got;
    }
    close(fd);
    *out = data;
    *length = (int)info.st_size;
    *present = 1;
    return 0;
}

static int bridge_archive_path(const char *account, int slot, char *path,
                               int path_length)
{
    char body[BRIDGE_ACCOUNT_MAX + 32];

    if (!bridge_safe_account(account) || slot < 0 || slot >= MAXCHAR_PER_USER)
        return 0;
    snprintf(body, sizeof(body), "%s.%d.char", account, slot);
    makeDirFilename(path, path_length, chardir, getHash((char *)account), body);
    if (path[0] == 0 || strlen(path) >= (size_t)path_length) return 0;
    return 1;
}

static int bridge_load_request(const char *path, const char *filename,
                               struct bridge_request *request)
{
    int fd;
    struct stat info;
    unsigned char *data;
    size_t offset;
    ssize_t got;
    char line[BRIDGE_HEADER_MAX];
    char *key;
    char *value;
    int header_done;
    int seen[8];
    char header_id[BRIDGE_ID_MAX + 1];
    size_t filename_id_length;

    memset(request, 0, sizeof(*request));
    request->slot = -1;
    request->expected_length = -1;
    request->new_length = -1;
    filename_id_length = strlen(filename);
    if (filename_id_length <= 4 ||
        strcmp(filename + filename_id_length - 4, ".req") != 0)
        return -1;
    filename_id_length -= 4;
    if (filename_id_length > BRIDGE_ID_MAX) return -1;
    memcpy(request->id, filename, filename_id_length);
    request->id[filename_id_length] = 0;
    if (!bridge_safe_id(request->id, BRIDGE_ID_MAX)) return -1;

    fd = open(path, O_RDONLY | O_NOFOLLOW);
    if (fd < 0) return -1;
    if (fstat(fd, &info) < 0 || !S_ISREG(info.st_mode) ||
        info.st_size < 1 || info.st_size > BRIDGE_REQUEST_MAX) {
        close(fd);
        return -1;
    }
    data = (unsigned char *)malloc((size_t)info.st_size);
    if (data == NULL) {
        close(fd);
        return -1;
    }
    offset = 0;
    while (offset < (size_t)info.st_size) {
        got = read(fd, data + offset, (size_t)info.st_size - offset);
        if (got <= 0) {
            free(data);
            close(fd);
            return -1;
        }
        offset += (size_t)got;
    }
    close(fd);

    memset(seen, 0, sizeof(seen));
    header_id[0] = 0;
    offset = 0;
    header_done = 0;
    while (offset < (size_t)info.st_size) {
        if (!bridge_line(data, (size_t)info.st_size, &offset, line,
                         sizeof(line))) {
            free(data);
            return -1;
        }
        if (strcmp(line, "---") == 0) {
            header_done = 1;
            break;
        }
        if (!bridge_field(line, &key, &value)) {
            free(data);
            return -1;
        }
        if (strcmp(key, "version") == 0) {
            if (seen[0]++ || strcmp(value, "1") != 0) { free(data); return -1; }
        } else if (strcmp(key, "id") == 0) {
            if (seen[1]++ || !bridge_copy_value(header_id, sizeof(header_id), value) ||
                !bridge_safe_id(value, BRIDGE_ID_MAX)) { free(data); return -1; }
            request->have_id = 1;
        } else if (strcmp(key, "op") == 0) {
            if (seen[2]++ || !bridge_copy_value(request->op, sizeof(request->op), value)) {
                free(data); return -1;
            }
            request->have_op = 1;
        } else if (strcmp(key, "account") == 0) {
            if (seen[3]++ || !bridge_copy_value(request->account, sizeof(request->account), value) ||
                !bridge_safe_account(value)) { free(data); return -1; }
            request->have_account = 1;
        } else if (strcmp(key, "slot") == 0) {
            if (seen[4]++ || !bridge_decimal(value, &request->slot, 1)) {
                free(data); return -1;
            }
            request->have_slot = 1;
        } else if (strcmp(key, "expected-length") == 0) {
            if (seen[5]++ || !bridge_decimal(value, &request->expected_length, 1)) {
                free(data); return -1;
            }
            request->have_expected_length = 1;
        } else if (strcmp(key, "new-length") == 0) {
            if (seen[6]++ || !bridge_decimal(value, &request->new_length, 0)) {
                free(data); return -1;
            }
            request->have_new_length = 1;
        } else {
            free(data);
            return -1;
        }
    }
    if (!header_done || !seen[0] || !request->have_id || !request->have_op ||
        !request->have_account || strcmp(header_id, request->id) != 0) {
        free(data);
        return -1;
    }
    if (offset > (size_t)info.st_size) {
        free(data);
        return -1;
    }
    request->allocation = data;
    request->payload = data + offset;
    request->payload_length = (size_t)info.st_size - offset;
    if (strcmp(request->op, "list") == 0) {
        if (request->have_slot || request->have_expected_length || request->have_new_length ||
            request->payload_length != 0) { free(data); return -1; }
    } else if (strcmp(request->op, "read") == 0) {
        if (!request->have_slot || request->slot < 0 || request->slot >= MAXCHAR_PER_USER ||
            request->have_expected_length || request->have_new_length ||
            request->payload_length != 0) { free(data); return -1; }
    } else if (strcmp(request->op, "write") == 0) {
        if (!request->have_slot || request->slot < 0 || request->slot >= MAXCHAR_PER_USER ||
            !request->have_expected_length || !request->have_new_length ||
            request->expected_length > BRIDGE_ARCHIVE_MAX ||
            request->new_length > BRIDGE_ARCHIVE_MAX ||
            request->payload_length != (size_t)request->expected_length +
                                         (size_t)request->new_length) {
            free(data);
            return -1;
        }
    } else {
        free(data);
        return -1;
    }
    return 0;
}

static void bridge_release_request(struct bridge_request *request)
{
    if (request->allocation != NULL) {
        free(request->allocation);
        request->allocation = NULL;
        request->payload = NULL;
    }
}

static void bridge_response_init(struct bridge_response *response,
                                 struct bridge_request *request)
{
    int i;
    memset(response, 0, sizeof(*response));
    snprintf(response->op, sizeof(response->op), "%s", request->op);
    snprintf(response->account, sizeof(response->account), "%s", request->account);
    snprintf(response->code, sizeof(response->code), "bad_request");
    response->slot = request->slot;
    response->length = 0;
    response->count = 0;
    for (i = 0; i < MAXCHAR_PER_USER; i++) {
        response->present[i] = 0;
        response->slot_length[i] = 0;
    }
}

static int bridge_write_all(int fd, const unsigned char *data, size_t length)
{
    ssize_t written;
    size_t offset = 0;
    while (offset < length) {
        written = write(fd, data + offset, length - offset);
        if (written <= 0) return -1;
        offset += (size_t)written;
    }
    return 0;
}

static int bridge_response_path(char *out, int out_length,
                                const char *directory, const char *id,
                                int temporary)
{
    const char *prefix = temporary ? "/." : "/";
    const char *suffix = temporary ? ".resp.tmp" : ".resp";
    size_t directory_length;
    size_t prefix_length;
    size_t id_length;
    size_t suffix_length;
    size_t length;
    size_t offset;
    size_t capacity;

    if (out == NULL || out_length <= 0 || directory == NULL || id == NULL)
        return 0;
    capacity = (size_t)out_length;
    directory_length = strlen(directory);
    prefix_length = strlen(prefix);
    id_length = strlen(id);
    suffix_length = strlen(suffix);
    if (directory_length >= capacity) return 0;
    length = directory_length;
    if (prefix_length > capacity - length - 1) return 0;
    length += prefix_length;
    if (id_length > capacity - length - 1) return 0;
    length += id_length;
    if (suffix_length > capacity - length - 1) return 0;
    length += suffix_length;

    offset = 0;
    memcpy(out + offset, directory, directory_length);
    offset += directory_length;
    memcpy(out + offset, prefix, prefix_length);
    offset += prefix_length;
    memcpy(out + offset, id, id_length);
    offset += id_length;
    memcpy(out + offset, suffix, suffix_length);
    out[length] = 0;
    return 1;
}

static int bridge_write_response(const char *id, struct bridge_response *response)
{
    char response_directory[PATH_MAX];
    char final_path[PATH_MAX];
    char temp_path[PATH_MAX];
    char header[BRIDGE_HEADER_MAX];
    int fd;
    int length;
    int i;
    int offset;
    int directory_fd;

    if (!bridge_safe_id(id, BRIDGE_ID_MAX)) return -1;
    if (!bridge_subdirectory(response_directory, sizeof(response_directory),
                             "responses")) return -1;
    if (!bridge_response_path(final_path, sizeof(final_path),
                              response_directory, id, 0)) return -1;
    if (access(final_path, F_OK) == 0) return 0;
    if (!bridge_response_path(temp_path, sizeof(temp_path),
                              response_directory, id, 1)) return -1;
    unlink(temp_path);
    fd = open(temp_path, O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW, 0600);
    if (fd < 0) return -1;
    offset = snprintf(header, sizeof(header),
                      "version=1\nid=%s\nstatus=%s\nop=%s\n",
                      id, strcmp(response->code, "ok") == 0 ? "ok" : "error",
                      response->op[0] == 0 ? "unknown" : response->op);
    if (response->account[0] != 0)
        offset += snprintf(header + offset, sizeof(header) - (size_t)offset,
                           "account=%s\n", response->account);
    offset += snprintf(header + offset, sizeof(header) - (size_t)offset,
                       "code=%s\n", response->code);
    if (strcmp(response->op, "list") == 0 && strcmp(response->code, "ok") == 0) {
        offset += snprintf(header + offset, sizeof(header) - (size_t)offset,
                           "count=%d\n", response->count);
        for (i = 0; i < MAXCHAR_PER_USER; i++) {
            offset += snprintf(header + offset, sizeof(header) - (size_t)offset,
                               "slot=%d\npresent=%d\nlength=%d\n", i,
                               response->present[i], response->slot_length[i]);
        }
    } else if (strcmp(response->code, "ok") == 0) {
        offset += snprintf(header + offset, sizeof(header) - (size_t)offset,
                           "slot=%d\nlength=%d\n", response->slot,
                           response->length);
    }
    offset += snprintf(header + offset, sizeof(header) - (size_t)offset, "---\n");
    if (offset < 0 || offset >= (int)sizeof(header)) {
        close(fd);
        unlink(temp_path);
        return -1;
    }
    length = offset;
    if (bridge_write_all(fd, (unsigned char *)header, (size_t)length) < 0 ||
        (response->payload_length > 0 &&
         bridge_write_all(fd, response->payload, response->payload_length) < 0) ||
        fsync(fd) < 0) {
        close(fd);
        unlink(temp_path);
        return -1;
    }
    if (close(fd) < 0 || rename(temp_path, final_path) < 0) {
        unlink(temp_path);
        return -1;
    }
    directory_fd = open(response_directory, O_RDONLY);
    if (directory_fd >= 0) {
        fsync(directory_fd);
        close(directory_fd);
    }
    return 0;
}

static int bridge_atomic_archive_write(const char *path, const char *id,
                                       const unsigned char *data, int length)
{
    char temp_path[PATH_MAX];
    int fd;

    if (snprintf(temp_path, sizeof(temp_path), "%s.admin-%s.tmp", path, id)
        >= (int)sizeof(temp_path)) return -1;
    unlink(temp_path);
    fd = open(temp_path, O_WRONLY | O_CREAT | O_EXCL | O_NOFOLLOW, 0600);
    if (fd < 0) return -1;
    if (bridge_write_all(fd, data, (size_t)length) < 0 || fsync(fd) < 0) {
        close(fd);
        unlink(temp_path);
        return -1;
    }
    if (close(fd) < 0 || rename(temp_path, path) < 0) {
        unlink(temp_path);
        return -1;
    }
    return 0;
}

static void bridge_process_request(const char *path, const char *filename)
{
    struct bridge_request request;
    struct bridge_response response;
    char archive_path[PATH_MAX];
    unsigned char *current;
    int current_length;
    int current_present;
    int result;
    int i;

    if (bridge_load_request(path, filename, &request) < 0) {
        memset(&request, 0, sizeof(request));
        if (strlen(filename) > 4 && strlen(filename) - 4 <= BRIDGE_ID_MAX) {
            memcpy(request.id, filename, strlen(filename) - 4);
            request.id[strlen(filename) - 4] = 0;
        }
        request.op[0] = 0;
        if (bridge_safe_id(request.id, BRIDGE_ID_MAX)) {
            memset(&response, 0, sizeof(response));
            snprintf(response.code, sizeof(response.code), "bad_request");
            snprintf(response.op, sizeof(response.op), "unknown");
            bridge_write_response(request.id, &response);
        }
        unlink(path);
        return;
    }

    bridge_response_init(&response, &request);
    if (!bridge_safe_account(request.account)) {
        snprintf(response.code, sizeof(response.code), "invalid_account");
    } else if (strcmp(request.op, "list") == 0) {
        response.count = 0;
        response.code[0] = 0;
        snprintf(response.code, sizeof(response.code), "ok");
        for (i = 0; i < MAXCHAR_PER_USER; i++) {
            if (!bridge_archive_path(request.account, i, archive_path,
                                     sizeof(archive_path))) {
                snprintf(response.code, sizeof(response.code), "invalid_path");
                break;
            }
            result = bridge_read_file(archive_path, &current, &current_length,
                                      &current_present);
            if (result < 0) {
                snprintf(response.code, sizeof(response.code),
                         result == -2 ? "range" : "io");
                break;
            }
            response.present[i] = current_present;
            response.slot_length[i] = current_present ? current_length : 0;
            if (current_present) response.count++;
            free(current);
        }
    } else if (!bridge_archive_path(request.account, request.slot, archive_path,
                                    sizeof(archive_path))) {
        snprintf(response.code, sizeof(response.code), "invalid_path");
    } else if (strcmp(request.op, "read") == 0) {
        result = bridge_read_file(archive_path, &current, &current_length,
                                  &current_present);
        if (result == 0 && current_present) {
            response.code[0] = 0;
            snprintf(response.code, sizeof(response.code), "ok");
            response.length = current_length;
            response.payload = current;
            response.payload_length = (size_t)current_length;
            bridge_write_response(request.id, &response);
            free(current);
            bridge_release_request(&request);
            unlink(path);
            return;
        }
        snprintf(response.code, sizeof(response.code),
                 result == -2 ? "range" : (result < 0 ? "io" : "not_found"));
    } else if (strcmp(request.op, "write") == 0) {
        if (isLocked(request.account)) {
            snprintf(response.code, sizeof(response.code), "online");
        } else {
            result = bridge_read_file(archive_path, &current, &current_length,
                                      &current_present);
            if (result == -2) {
                snprintf(response.code, sizeof(response.code), "range");
            } else if (result < 0) {
                snprintf(response.code, sizeof(response.code), "io");
            } else if ((!current_present && request.expected_length != 0) ||
                       (current_present &&
                        (current_length != request.expected_length ||
                         memcmp(current, request.payload,
                                (size_t)request.expected_length) != 0))) {
                snprintf(response.code, sizeof(response.code), "conflict");
            } else if (bridge_atomic_archive_write(
                           archive_path, request.id,
                           request.payload + request.expected_length,
                           request.new_length) < 0) {
                snprintf(response.code, sizeof(response.code), "io");
            } else {
                snprintf(response.code, sizeof(response.code), "ok");
                response.length = request.new_length;
            }
            free(current);
        }
    }
    bridge_write_response(request.id, &response);
    bridge_release_request(&request);
    unlink(path);
}

int stoneage_admin_bridge_init(void)
{
    char requests[PATH_MAX];
    char responses[PATH_MAX];
    struct stat info;

    if (mkdir(bridge_directory(), 0700) < 0 && errno != EEXIST) {
        log("admin bridge mkdir failed: %s\n", strerror(errno));
        return -1;
    }
    if (chmod(bridge_directory(), 0700) < 0 ||
        lstat(bridge_directory(), &info) < 0 ||
        !S_ISDIR(info.st_mode) || (info.st_mode & 0077) != 0 ||
        info.st_uid != geteuid()) {
        log("admin bridge directory is not private\n");
        return -1;
    }
    if (!bridge_subdirectory(requests, sizeof(requests), "requests") ||
        !bridge_subdirectory(responses, sizeof(responses), "responses") ||
        (mkdir(requests, 0700) < 0 && errno != EEXIST) ||
        (mkdir(responses, 0700) < 0 && errno != EEXIST) ||
        chmod(requests, 0700) < 0 || chmod(responses, 0700) < 0 ||
        lstat(requests, &info) < 0 || !S_ISDIR(info.st_mode) ||
        (info.st_mode & 0077) != 0 || info.st_uid != geteuid() ||
        lstat(responses, &info) < 0 || !S_ISDIR(info.st_mode) ||
        (info.st_mode & 0077) != 0 || info.st_uid != geteuid()) {
        log("admin bridge queue directories are unavailable\n");
        return -1;
    }
    return 0;
}

void stoneage_admin_bridge_poll(void)
{
    DIR *directory;
    struct dirent *entry;
    char path[PATH_MAX];
    char request_directory[PATH_MAX];
    struct stat info;
    size_t name_length;

    if (stoneage_admin_bridge_init() < 0) return;
    if (!bridge_subdirectory(request_directory, sizeof(request_directory),
                             "requests")) return;
    directory = opendir(request_directory);
    if (directory == NULL) return;
    while ((entry = readdir(directory)) != NULL) {
        name_length = strlen(entry->d_name);
        if (name_length <= 4 ||
            strcmp(entry->d_name + name_length - 4, ".req") != 0)
            continue;
        if (snprintf(path, sizeof(path), "%s/%s",
                     request_directory, entry->d_name)
            >= (int)sizeof(path))
            continue;
        if (lstat(path, &info) < 0 || !S_ISREG(info.st_mode)) continue;
        bridge_process_request(path, entry->d_name);
    }
    closedir(directory);
}
