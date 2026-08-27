#!/bin/sh
set -eu

apk add --no-cache build-base file python3

prepare_makefile() {
  target="$1"
  sed -i '1s/^\xef\xbb\xbf//' "$target"
  sed -i 's/^CFLAGS=.*/CFLAGS=-O2 -g3 -fno-omit-frame-pointer -std=gnu89 -fcommon -w $(INCFLAGS)/' "$target"
}

prepare_makefile /src/saac/makefile
prepare_makefile /src/gmsv/makefile

for child_makefile in \
  /src/gmsv/battle/makefile \
  /src/gmsv/char/makefile \
  /src/gmsv/item/makefile \
  /src/gmsv/magic/makefile \
  /src/gmsv/map/makefile \
  /src/gmsv/npc/makefile; do
  prepare_makefile "$child_makefile"
done

# The bundled MySQL patch interpolates account data into SQL and stores plain
# passwords. Keep local bring-up on the original file-backed account path.
sed -i 's/^#define _SASQL/\/\/#define _SASQL/' /src/saac/include/version.h
sed -i 's/^#define _SQL_REGISTER/\/\/#define _SQL_REGISTER/' /src/saac/include/version.h
sed -i 's/^MYSQL=.*/MYSQL=/' /src/saac/makefile
sed -i 's/ sasql.c / /' /src/saac/makefile

# A historic whitespace trim copies a line of up to 512 bytes through a
# 256-byte stack buffer. Modern fortified libc correctly stops the overflow.
# Apply the reviewable compatibility patch once to trim the line in place.
if ! grep -q 'STONEAGE_SAFE_LEADING_SPACE_TRIM' /src/gmsv/item/item.c; then
  patch -d /src/gmsv -p1 < /modern/patches/0001-safe-item-leading-space-trim.patch
fi

# NPC argument files are nested below data/npc. The original 32-byte path
# buffer is smaller than valid paths already present in the archive.
if ! grep -q 'STONEAGE_SAFE_NPC_ARGUMENT_PATH' /src/gmsv/npc/npcutil.c; then
  patch -d /src/gmsv -p1 < /modern/patches/0002-safe-npc-argument-path.patch
fi

# The original same-IP scan omitted the loop braces. It tested ConnectLen
# after the loop, reading one element past the connection table and randomly
# rejecting future local clients after the first disconnect.
if ! grep -q 'STONEAGE_SAFE_SAME_IP_SCAN' /src/gmsv/net.c; then
  patch -d /src/gmsv -p1 < /modern/patches/0004-safe-same-ip-scan.patch
fi

# fgetc() returns int, but the archive stored it in a plain char. On ARM64,
# EOF consequently became 255 and character loading wrote past its 64 KiB
# buffer until SAAC crashed. Preserve EOF and reject oversized save files.
python3 /modernize-character-file-read.py /src/saac/char.c

# Replace hard-coded XFei/private-server sales adverts with a short local
# welcome. char.c is GBK rather than UTF-8, so the checked rewriter preserves
# every unrelated byte and emits traditional wording in code page 936.
python3 /modernize-login-announcement.py /src/gmsv/char/char.c

# The historic movement-packet NU budget is never replenished in this build,
# so it eventually disconnects ordinary players. Keep the check available for
# compatibility, but make it an explicit setup.cf option that defaults off.
python3 /modernize-nu-flow-control.py /src/gmsv

# Reject invalid character/pet references so one disconnect can never delete
# another online player through a stale legacy array index. The connection's
# use flag intentionally remains the close-path re-entry guard in net.c.
if ! grep -q 'STONEAGE_SAFE_DISCONNECT_CLEANUP' /src/gmsv/char/char.c; then
  patch -d /src/gmsv -p1 < /modern/patches/0006-safe-disconnect-cleanup.patch
fi

# A clean client EOF is the 2.5 in-place logout path. GMSV historically
# closed that socket before SAAC acknowledged the asynchronous character save,
# allowing an immediate relogin to load the previous coordinates. Keep the fd
# slot until the existing WHILELOGOUTSAVE callback has completed; no newer
# CharLogout field is added to the 2.5 wire protocol.
if ! grep -q 'STONEAGE_DURABLE_EOF_LOGOUT' /src/gmsv/net.c; then
  patch -d /src/gmsv -p1 < /modern/patches/0008-durable-eof-logout.patch
fi

# The authenticated web console writes one atomic, fixed-path notice file.
# Let the GMSV consume it in its normal main loop and deliver it to online
# players through the same red system-message path as the built-in announce
# command. The file is never interpreted as a shell command.
if ! grep -q 'STONEAGE_ADMIN_NOTICE' /src/gmsv/main.c; then
  patch -d /src/gmsv -p1 < /modern/patches/0007-admin-notification.patch
fi

# Debug output in the historic login, delete, shutdown, and configuration
# paths exposes player passwords, the GMSV-to-SAAC shared secret, and the GM
# command password. The source files are GBK, so use checked, byte-preserving
# line replacements rather than transcoding them as UTF-8.
python3 /redact-legacy-password-logs.py /src/gmsv /src/saac

# Quiz question selection returned a pointer to a dead stack VLA, while live
# player state pointers were truncated into 32-bit NPC work integers. The
# archive is not UTF-8, so this uses byte-preserving, checked substitutions.
python3 /modernize-quiz-state.py /src/gmsv/npc/npc_quiz.c

# The generated RPC dispatcher always references this optional callback.
# With the unsafe SQL lock backend disabled it is intentionally a development
# no-op. Internet deployment must replace this account layer first.
if ! grep -q 'STONEAGE_DEV_LOCKLOGIN_STUB' /src/saac/recv.c; then
  cat >> /src/saac/recv.c <<'EOF'

/* STONEAGE_DEV_LOCKLOGIN_STUB: local-only compatibility callback. */
void saacproto_LockLogin_recv(int fd, char *id, char *ip, int flag)
{
    (void)fd;
    (void)id;
    (void)ip;
    (void)flag;
}
EOF
fi

make -C /src/saac clean >/dev/null 2>&1 || true
make -C /src/saac -j2

make -C /src/gmsv clean >/dev/null 2>&1 || true
make -C /src/gmsv -j2

file /src/saac/saacjt.exe /src/gmsv/gmsvjt.exe
