/* Filesystem work belongs exclusively to this worker. No game RNG calls,
 * network requests, shell commands, account data or player names. */
#include "stoneage_battle_log.h"
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <sys/statvfs.h>
#include <time.h>
#include <unistd.h>

#ifndef BATTLE_LOG_QUEUE_SIZE
#define BATTLE_LOG_QUEUE_SIZE 2048
#endif
#define BATTLE_PATH_MAX 2048
typedef struct {
    int slot, kind;
    unsigned long match, sequence;
    char json[BATTLE_LOG_JSON_MAX];
} BattleLogMessage;
typedef struct {
    unsigned long match, next_sequence;
    int broken, started;
    char directory[BATTLE_PATH_MAX];
} BattleLogFile;

static pthread_mutex_t lock = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t ready = PTHREAD_COND_INITIALIZER;
static pthread_t worker;
static BattleLogMessage queue[BATTLE_LOG_QUEUE_SIZE];
static unsigned int head, count;
static int initialized, running, stopping, slot_count;
static int offline;
static unsigned long submit_failures;
static BattleLogFile *files;
static char root[BATTLE_PATH_MAX], session[33];
static unsigned long written, io_errors, completed;
static int last_error;
static unsigned long long min_free = 1073741824ULL;
static time_t last_warning, last_space_check;
static int space_ok;

static void failure(int error)
{
    time_t now = time(NULL);
    last_error = error ? error : EIO;
    io_errors++;
    if (now - last_warning >= 60 || !last_warning) {
        fprintf(stderr, "Battle recorder: storage error %d; affected matches are incomplete\n", last_error);
        last_warning = now;
    }
}

static int directory(const char *path)
{
    struct stat st;
    if (mkdir(path, 0700) == 0) return 1;
    if (errno == EEXIST && lstat(path, &st) == 0 && S_ISDIR(st.st_mode)) return 1;
    failure(errno);
    return 0;
}

static int room(void)
{
    struct statvfs st;
    time_t now = time(NULL);
    if (now == last_space_check) return space_ok;
    last_space_check = now;
    space_ok = 0;
    if (statvfs(root, &st) != 0) failure(errno);
    else if ((unsigned long long)st.f_bavail * st.f_frsize < min_free) failure(ENOSPC);
    else space_ok = 1;
    return space_ok;
}

static int write_file(const char *path, const char *json, int append, int sync_file)
{
    int fd, ok = 1, saved;
    size_t left = strlen(json);
    const char *cursor = json;
    fd = open(path, O_WRONLY | O_CREAT | O_NOFOLLOW |
                    (append ? O_APPEND : O_EXCL), 0600);
    if (fd < 0) { failure(errno); return 0; }
    while (left) {
        ssize_t n = write(fd, cursor, left);
        if (n < 0 && errno == EINTR) continue;
        if (n <= 0) { failure(errno); ok = 0; break; }
        cursor += n;
        left -= (size_t)n;
    }
    if (ok && write(fd, "\n", 1) != 1) { failure(errno); ok = 0; }
    if (ok && sync_file && fsync(fd) != 0) { failure(errno); ok = 0; }
    saved = errno;
    if (close(fd) != 0) { failure(errno); ok = 0; }
    errno = saved;
    return ok;
}

static void process(const BattleLogMessage *m)
{
    BattleLogFile *f = &files[m->slot];
    char path[BATTLE_PATH_MAX + 64], day_path[BATTLE_PATH_MAX], day[16];
    char result[BATTLE_LOG_JSON_MAX + 128];
    time_t now;
    struct tm utc;
    int ok, fd;
    size_t len;
    if (m->kind == BATTLE_LOG_BEGIN) {
        memset(f, 0, sizeof(*f));
        f->match = m->match;
        now = time(NULL);
        gmtime_r(&now, &utc);
        strftime(day, sizeof(day), "%Y-%m-%d", &utc);
        snprintf(day_path, sizeof(day_path), "%s/%s", root, day);
        snprintf(f->directory, sizeof(f->directory), "%s/%s-%lu", day_path, session, m->match);
        if (!directory(root) || !room() || !directory(day_path) ||
            !directory(f->directory)) { f->broken = 1; return; }
        snprintf(path, sizeof(path), "%s/metadata.json", f->directory);
        f->started = write_file(path, m->json, 0, 1);
        f->broken = !f->started;
        f->next_sequence = m->sequence + 1;
        return;
    }
    if (f->match != m->match || !f->started) return;
    if (f->next_sequence != m->sequence) f->broken = 1;
    f->next_sequence = m->sequence + 1;
    if (!room()) { f->broken = 1; return; }
    if (m->kind == BATTLE_LOG_EVENT) {
        snprintf(path, sizeof(path), "%s/events.jsonl", f->directory);
        if (!write_file(path, m->json, 1, 0)) f->broken = 1;
        else written++;
        return;
    }
    if (m->kind != BATTLE_LOG_END) return;
    /* Result is the commit marker. Sync the entire trajectory first. */
    snprintf(path, sizeof(path), "%s/events.jsonl", f->directory);
    fd = open(path, O_RDONLY | O_NOFOLLOW);
    if (fd < 0) { failure(errno); f->broken = 1; }
    else { if (fsync(fd) != 0) { failure(errno); f->broken = 1; } close(fd); }
    len = strlen(m->json);
    if (!len || m->json[len-1] != '}') { f->broken = 1; return; }
    snprintf(result, sizeof(result), "%.*s,\"storage_complete\":%s}",
             (int)(len-1), m->json, f->broken ? "false" : "true");
    snprintf(path, sizeof(path), "%s/result.json.tmp", f->directory);
    ok = write_file(path, result, 0, 1);
    if (ok) {
        char final[BATTLE_PATH_MAX + 64];
        snprintf(final, sizeof(final), "%s/result.json", f->directory);
        if (rename(path, final) != 0) failure(errno);
        else {
            fd = open(f->directory, O_RDONLY | O_NOFOLLOW);
            if (fd >= 0) { if (fsync(fd) != 0) failure(errno); close(fd); }
            completed++;
        }
    }
    f->started = 0;
}

static void status(void)
{
    char path[BATTLE_PATH_MAX + 64], final[BATTLE_PATH_MAX + 64], json[512];
    int fd;
    if (!directory(root)) return;
    snprintf(path, sizeof(path), "%s/status.json.tmp", root);
    snprintf(final, sizeof(final), "%s/status.json", root);
    snprintf(json, sizeof(json), "{\"schema_version\":1,\"session\":\"%s\","
             "\"updated_at\":%ld,\"events_written\":%lu,\"matches_closed\":%lu,"
             "\"io_errors\":%lu,\"last_errno\":%d,\"space_ok\":%s,\"min_free_bytes\":%llu}",
             session, (long)time(NULL), written, completed, io_errors, last_error,
             room() ? "true" : "false", min_free);
    /* This fixed private health file is replaceable, unlike match files. */
    fd = open(path, O_WRONLY | O_CREAT | O_TRUNC | O_NOFOLLOW, 0600);
    if (fd < 0) { failure(errno); return; }
    if (write(fd, json, strlen(json)) != (ssize_t)strlen(json)) failure(errno);
    else if (rename(path, final) != 0) failure(errno);
    close(fd);
}

static void *writer(void *unused)
{
    BattleLogMessage m;
    time_t last_status = 0;
    (void)unused;
    for (;;) {
        struct timespec until;
        int have = 0, done;
        pthread_mutex_lock(&lock);
        if (!count && !stopping) {
            until.tv_sec = time(NULL) + 5;
            until.tv_nsec = 0;
            pthread_cond_timedwait(&ready, &lock, &until);
        }
        if (count) {
            m = queue[head];
            head = (head + 1) % BATTLE_LOG_QUEUE_SIZE;
            count--;
            pthread_cond_broadcast(&ready);
            have = 1;
        }
        done = stopping && !count;
        pthread_mutex_unlock(&lock);
        if (have) process(&m);
        if (time(NULL) - last_status >= 5 || done) {
            status();
            last_status = time(NULL);
        }
        if (done) break;
    }
    return NULL;
}

void StoneAge_BattleLogShutdown(void)
{
    if (!running) return;
    pthread_mutex_lock(&lock);
    stopping = 1;
    pthread_cond_signal(&ready);
    pthread_mutex_unlock(&lock);
    pthread_join(worker, NULL);
    running = 0;
    free(files);
    files = NULL;
}

int StoneAge_BattleLogInit(int slots)
{
    const char *path, *minimum;
    unsigned char random_bytes[16];
    int fd, i;
    if (initialized) return running;
    initialized = 1;
    path = getenv("STONEAGE_BATTLE_RECORD_DIR");
    if (!path || !*path) return 0;
    if (strlen(path) > sizeof(root) - 128 || slots <= 0 || slots > 100000) return 0;
    strcpy(root, path);
    minimum = getenv("STONEAGE_BATTLE_RECORD_MIN_FREE_BYTES");
    if (minimum && *minimum) {
        char *end;
        unsigned long long value;
        errno = 0;
        value = strtoull(minimum, &end, 10);
        if (!errno && !*end && minimum[0] != '-') min_free = value;
    }
    fd = open("/dev/urandom", O_RDONLY);
    if (fd < 0) return 0;
    i = (int)read(fd, random_bytes, sizeof(random_bytes));
    close(fd);
    if (i != sizeof(random_bytes)) return 0;
    for (i = 0; i < 16; i++) sprintf(session + i*2, "%02x", random_bytes[i]);
    files = (BattleLogFile *)calloc((size_t)slots, sizeof(BattleLogFile));
    if (!files) return 0;
    slot_count = slots;
    if (pthread_create(&worker, NULL, writer, NULL) != 0) { free(files); files = NULL; return 0; }
    running = 1;
    atexit(StoneAge_BattleLogShutdown);
    fprintf(stderr, "Battle recorder: asynchronous recording enabled (schema 1)\n");
    return 1;
}

const char *StoneAge_BattleLogSession(void) { return session; }
void StoneAge_BattleLogOffline(void) { if (!initialized) offline = 1; }
int StoneAge_BattleLogHealthy(void) { return initialized && session[0] && !io_errors && !submit_failures; }

int StoneAge_BattleLogSubmit(int slot, unsigned long match, unsigned long sequence,
                         int kind, const char *json)
{
    BattleLogMessage *m;
    if (!running || slot < 0 || slot >= slot_count || !json ||
        strlen(json) >= BATTLE_LOG_JSON_MAX) { submit_failures++; return 0; }
    /* The worker holds this mutex only while copying one bounded message,
     * never during I/O. Ordinary scheduling contention must not lose data. */
    pthread_mutex_lock(&lock);
    while (offline && count == BATTLE_LOG_QUEUE_SIZE && !stopping)
        pthread_cond_wait(&ready, &lock);
    if (count == BATTLE_LOG_QUEUE_SIZE || stopping) {
        submit_failures++;
        pthread_mutex_unlock(&lock);
        return 0;
    }
    m = &queue[(head + count) % BATTLE_LOG_QUEUE_SIZE];
    m->slot = slot;
    m->match = match;
    m->sequence = sequence;
    m->kind = kind;
    strcpy(m->json, json);
    count++;
    pthread_cond_signal(&ready);
    pthread_mutex_unlock(&lock);
    return 1;
}
