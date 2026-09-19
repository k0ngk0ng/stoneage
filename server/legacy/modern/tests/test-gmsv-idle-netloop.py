#!/usr/bin/env python3
"""Show that the patched netloop wait sleeps instead of burning the tick.

netloop_faster() walked the connection table one slot at a time with a zero
timeout and only left the tick after the whole Onelooptime budget had been
burned, so an unattended server held a core at 100%. The patch is applied to a
copy of the archived source, and the wait lines are then lifted out of the
result and run inside a loop with the same shape, so the arithmetic under test
is the production one: an empty table has to sleep the tick away, and a
populated one has to block in select() instead of returning at once.

Two things are easy to lose again later, so both are pinned here.

The historic read select also asked for writability. A connected socket is
writable almost always, so leaving wfds in that set turns the wait back into
the original spin -- the control case below measures that.

The pass also has to end on an absolute deadline rather than one Onelooptime
after the pass happened to start, or a sleep that returns late stretches the
tick. Walking and NPC movement advance once per pass, so a stretched tick is a
game that runs slow, which is why the schedule is checked against a scripted
clock here instead of being left to the host's timers.
"""

from pathlib import Path
import subprocess
import tempfile


ROOT = Path(__file__).resolve().parents[4]
SOURCE = ROOT / "server/legacy/source/2.5/gmsv/net.c"
PATCHES = [
    ROOT / "server/legacy/modern/patches/0026-idle-netloop-wait.patch",
    ROOT / "server/legacy/modern/patches/0027-netloop-pass-slice.patch",
    ROOT / "server/legacy/modern/patches/0028-netloop-line-buffer.patch",
]
COMMON = ROOT / "server/legacy/source/2.5/gmsv/include/common.h"

MARKER = "/* STONEAGE_IDLE_NETLOOP_WAIT"


def patched_source(directory):
    """Apply the reviewed patches to a copy of the archived net.c."""
    path = Path(directory) / "net.c"
    path.write_bytes(SOURCE.read_bytes())
    for patch in PATCHES:
        subprocess.run(
            ["patch", "-p1", "--quiet", "-i", str(patch)],
            cwd=directory,
            check=True,
        )
    return path.read_bytes().decode("latin-1")


def slice_from(source, start_marker, end_marker, offset=0):
    start = source.index(start_marker, offset)
    end = source.index(end_marker, start)
    return source[start:end], end


def slice_to_next_marker(source, offset=0):
    start = source.index(MARKER, offset)
    end = source.index(MARKER, start + len(MARKER))
    return source[start:end], end


def production_pieces(source):
    if source.count(MARKER) != 6:
        raise RuntimeError(
            f"expected 6 {MARKER} blocks in net.c, found {source.count(MARKER)}"
        )

    counter, cursor = slice_to_next_marker(source)
    schedule, cursor = slice_from(
        source, MARKER, "SINGLETHREAD BOOL netloop_faster", cursor
    )
    empty, cursor = slice_from(source, MARKER, "\n#ifdef _AC_PIORITY", cursor)
    wait, _ = slice_from(source, MARKER, "    /* read select */", cursor)

    reader, _ = slice_from(source, "SINGLETHREAD BOOL GetOneLine_fix",
                           "\nANYTHREAD BOOL initConnectOne", 0)

    common = COMMON.read_bytes().decode("latin-1")
    time_macro = next(
        entry
        for entry in common.splitlines()
        if entry.startswith("#define time_diff_us")
    )
    return counter, schedule, empty, wait, time_macro, reader


def harness_source(pieces):
    counter, schedule, empty, wait, time_macro, reader = pieces
    return f"""\
#define _DEFAULT_SOURCE
#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <errno.h>
#include <unistd.h>
#include <sys/select.h>
#include <sys/time.h>
#include <sys/socket.h>
#include <time.h>

#define BOOL int
#define TRUE 1
#define FALSE 0
#define SINGLETHREAD
{time_macro}

static struct {{
  int use;
  char *rb;
  int rbuse;
  int check_rb_oneline_b;
  int check_rb_time;
}} Connect[4];
static int ConnectLen = 4;
static int acfd = 3;
#define AC_RBSIZE 4096
#define logRBuseErr fixture_log_rbuse_err
static int fixture_log_rbuse_err = 0;
static void LogAcMess(int fd, const char *tag, const char *text)
{{ (void)fd; (void)tag; (void)text; }}
static void shiftRB(int fd, int count)
{{
  memmove(Connect[fd].rb, Connect[fd].rb + count, Connect[fd].rbuse - count);
  Connect[fd].rbuse -= count;
}}

/* The schedule reads the wall clock, so the tick it computes is checked
   against a scripted one. Everything else keeps the real clock. */
static int scripted_clock = 0;
static struct timeval scripted_now;
static int fixture_gettimeofday(struct timeval *tv, void *tz)
{{
  struct timespec ts;

  (void)tz;
  if (scripted_clock) {{
    if (tv) *tv = scripted_now;
    return 0;
  }}
  clock_gettime(CLOCK_REALTIME, &ts);
  if (tv) {{ tv->tv_sec = ts.tv_sec; tv->tv_usec = ts.tv_nsec / 1000; }}
  return 0;
}}
#define gettimeofday fixture_gettimeofday

{counter}

{schedule}

{reader}

static double wall_now(void)
{{
  struct timespec ts;
  clock_gettime(CLOCK_MONOTONIC, &ts);
  return ts.tv_sec + ts.tv_nsec / 1e9;
}}

/* One netloop_faster pass, reduced to the parts under test: the round-robin
   slot walk keeps its shape, the table is fixed, and the slot itself is an
   unconnected socket, which is never readable but is always writable. */
static double run_tick(int connect_fd, unsigned int looptime_us,
                       int wait_for_write, long *visits)
{{
  int active_fds, remain_us = 0, wait_us = 0, ret;
  struct timeval st, et, tmv, idle;
  static struct timeval tick_deadline;
  fd_set rfds, wfds;
  clock_t cpu_start;
  double wall_start, wall_end;
  long loops = 0;

  CONNECT_advanceTickDeadline(&tick_deadline, &st, looptime_us);
  active_fds = CONNECT_countActiveFds();
  wall_start = wall_now();
  cpu_start = clock();

  while (1) {{
    gettimeofday(&et, NULL);
    if (time_diff_us(et, st) >= looptime_us) break;

{empty}

    if (active_fds > 0) {{
{wait}
    }}

    FD_ZERO(&rfds);
    FD_ZERO(&wfds);
    FD_SET(connect_fd, &rfds);
    if (wait_for_write) FD_SET(connect_fd, &wfds);
    tmv.tv_sec = 0;
    tmv.tv_usec = wait_us;
    ret = select(connect_fd + 1, &rfds,
                 wait_for_write ? &wfds : (fd_set *)NULL, (fd_set *)NULL, &tmv);
    if (ret < 0 && errno != EINTR) abort();
    loops++;
  }}

  wall_end = wall_now();
  *visits = loops;
  return (double)(clock() - cpu_start) / CLOCKS_PER_SEC / (wall_end - wall_start);
}}

static void set_scripted(long seconds, long microseconds)
{{
  scripted_clock = 1;
  scripted_now.tv_sec = seconds;
  scripted_now.tv_usec = microseconds;
}}

/* The per-pass buffer is no longer zeroed, which is only safe while the
   reader terminates what it hands back and leaves the buffer alone when it
   reports that no line is ready. */
static void check_line_reader(void)
{{
  static char ring[16];
  static char buffer[32];
  int i;

  Connect[0].rb = ring;
  Connect[0].rbuse = 0;
  memset(buffer, 0xAA, sizeof(buffer));
  assert(GetOneLine_fix(0, buffer, sizeof(buffer)) == FALSE);
  for (i = 0; i < (int)sizeof(buffer); i++) assert((unsigned char)buffer[i] == 0xAA);

  /* A complete line is copied and terminated, with nothing of the caller's
     buffer left inside the string. */
  memcpy(ring, "hello\\n", 6);
  Connect[0].rbuse = 6;
  Connect[0].check_rb_oneline_b = 0;
  memset(buffer, 0xAA, sizeof(buffer));
  assert(GetOneLine_fix(0, buffer, sizeof(buffer)) == TRUE);
  assert(strcmp(buffer, "hello\\n") == 0);
  assert(Connect[0].rbuse == 0);

  /* A line without its newline yet is not handed back, and the buffer keeps
     whatever the caller put there. */
  memcpy(ring, "partial", 7);
  Connect[0].rbuse = 7;
  Connect[0].check_rb_oneline_b = 0;
  memset(buffer, 0xAA, sizeof(buffer));
  assert(GetOneLine_fix(0, buffer, sizeof(buffer)) == FALSE);
  for (i = 0; i < (int)sizeof(buffer); i++) assert((unsigned char)buffer[i] == 0xAA);

  /* The longest line the buffer can hold still terminates inside it. */
  for (i = 0; i < (int)sizeof(buffer) - 1; i++) buffer[i] = 'x';
  buffer[sizeof(buffer) - 1] = 'y';
  Connect[0].rb = buffer;
  Connect[0].rbuse = sizeof(buffer) - 1;
  Connect[0].check_rb_oneline_b = 0;
  assert(GetOneLine_fix(0, ring, 16) == FALSE);
}}

static void check_schedule(void)
{{
  const unsigned int period = 5000;
  struct timeval deadline, start;
  int i;

  /* First pass: the window starts at the clock and the deadline is one
     period later. */
  memset(&deadline, 0, sizeof(deadline));
  set_scripted(1000, 0);
  CONNECT_advanceTickDeadline(&deadline, &start, period);
  assert(deadline.tv_sec == 1000 && deadline.tv_usec == 5000);
  assert(start.tv_sec == 1000 && start.tv_usec == 0);

  /* On-time passes move the deadline exactly one period each. */
  for (i = 1; i <= 3; i++) {{
    set_scripted(1000, 5000 * i);
    CONNECT_advanceTickDeadline(&deadline, &start, period);
    assert(deadline.tv_sec == 1000 && deadline.tv_usec == 5000 * (i + 1));
    assert(deadline.tv_sec * 1000000 + deadline.tv_usec
           - (start.tv_sec * 1000000 + start.tv_usec) == (long)period);
  }}

  /* The period carries into the next second. */
  memset(&deadline, 0, sizeof(deadline));
  set_scripted(1000, 995000);
  CONNECT_advanceTickDeadline(&deadline, &start, period);
  assert(deadline.tv_sec == 1001 && deadline.tv_usec == 0);
  assert(start.tv_sec == 1000 && start.tv_usec == 995000);
  set_scripted(1001, 1000);
  CONNECT_advanceTickDeadline(&deadline, &start, period);
  assert(deadline.tv_sec == 1001 && deadline.tv_usec == 5000);
  assert(start.tv_sec == 1001 && start.tv_usec == 0);

  /* An overshoot shorter than a period is compensated rather than
     rescheduled: the next deadline stays on the original schedule, which is
     what keeps the average period at Onelooptime. */
  memset(&deadline, 0, sizeof(deadline));
  set_scripted(2000, 0);
  CONNECT_advanceTickDeadline(&deadline, &start, period);
  assert(deadline.tv_sec == 2000 && deadline.tv_usec == 5000);
  set_scripted(2000, 6200);
  CONNECT_advanceTickDeadline(&deadline, &start, period);
  assert(deadline.tv_sec == 2000 && deadline.tv_usec == 10000);
  assert(start.tv_sec == 2000 && start.tv_usec == 5000);

  /* A pass a whole period behind is not handed a backlog to clear: its
     successor gets a full period from now instead of one period from the
     missed deadline. */
  set_scripted(1009, 0);
  CONNECT_advanceTickDeadline(&deadline, &start, period);
  assert(deadline.tv_sec == 1009 && deadline.tv_usec == 5000);
  assert(start.tv_sec == 1009 && start.tv_usec == 0);

  /* A clock that stepped backwards starts a fresh period rather than holding
     the deadline out of reach for the length of the jump. */
  set_scripted(1000, 0);
  CONNECT_advanceTickDeadline(&deadline, &start, period);
  assert(deadline.tv_sec == 1000 && deadline.tv_usec == 5000);
  assert(start.tv_sec == 1000 && start.tv_usec == 0);

  scripted_clock = 0;
}}

int main(void)
{{
  const unsigned int tick_us = 5000;
  const int ticks = 40;
  int pair[2];
  int fd, i;
  long visits = 0, total_visits = 0;
  double ratio, cpu_ratio, period_ms;
  double a, b;

  check_schedule();
  check_line_reader();

  /* A connected socket with nothing sent on it: never readable, always
     writable, which is the combination the wait has to survive. */
  assert(socketpair(AF_UNIX, SOCK_STREAM, 0, pair) == 0);
  fd = pair[0];

  /* Every used slot counted, empty ones ignored. */
  memset(Connect, 0, sizeof(Connect));
  assert(CONNECT_countActiveFds() == 0);
  Connect[1].use = 1;
  assert(CONNECT_countActiveFds() == 1);
  Connect[3].use = 1;
  assert(CONNECT_countActiveFds() == 2);

  /* Nobody connected: the tick is slept out, not spun through. */
  memset(Connect, 0, sizeof(Connect));
  a = wall_now();
  ratio = run_tick(fd, tick_us, 0, &visits);
  b = wall_now();
  printf("empty table: cpu/wall %.3f, %.0f slot visits, %.3f ms\\n",
         ratio, (double)visits, (b - a) * 1000);
  assert(visits == 0);
  assert(ratio < 0.25);
  assert(b - a >= 0.004 && b - a < 0.02);

  /* Two sessions and no input: the tick keeps its period and stays asleep.
     The period is asserted over the whole run rather than per pass, because a
     pass that gives the CPU back late is followed by a correspondingly
     shorter wait. */
  Connect[1].use = 1;
  Connect[3].use = 1;
  cpu_ratio = 0;
  a = wall_now();
  for (i = 0; i < ticks; i++) {{
    cpu_ratio += run_tick(fd, tick_us, 0, &visits);
    total_visits += visits;
    /* One slice per slot: two slots wake about once each per tick, not the
       hundreds of thousands of visits a poll loop makes, and not the dozen a
       share-of-the-remainder wait takes as the remainder halves. */
    assert(visits <= 4);
  }}
  b = wall_now();
  period_ms = (b - a) * 1000 / ticks;
  printf("two sessions: %.3f ms/tick over %d ticks, mean cpu/wall %.3f,"
         " %.1f slot visits/tick\\n",
         period_ms, ticks, cpu_ratio / ticks, (double)total_visits / ticks);
  assert(cpu_ratio / ticks < 0.25);
  assert(period_ms > tick_us / 1000.0 * 0.96);
  assert(period_ms < tick_us / 1000.0 * 1.04);
  assert(total_visits <= (long)ticks * 4);

  /* The control: waiting for writability ends the wait immediately, which is
     the zero-timeout spin this patch replaces. */
  a = wall_now();
  ratio = run_tick(fd, tick_us, 1, &visits);
  b = wall_now();
  printf("writability in the set: cpu/wall %.3f, %.0f slot visits\\n",
         ratio, (double)visits);
  assert(visits > 1000);
  assert(ratio > 0.75);

  close(pair[0]);
  close(pair[1]);
  puts("idle netloop wait: ok");
  return 0;
}}
"""


def main():
    with tempfile.TemporaryDirectory() as directory:
        source = patched_source(directory)
        path = Path(directory) / "idle_netloop.c"
        path.write_text(harness_source(production_pieces(source)))
        binary = Path(directory) / "idle_netloop"
        subprocess.run(
            ["cc", "-O2", "-std=gnu89", "-w", "-o", str(binary), str(path)],
            check=True,
        )
        result = subprocess.run(
            [str(binary)], check=True, capture_output=True, text=True
        )
        print(result.stdout, end="")


if __name__ == "__main__":
    main()
