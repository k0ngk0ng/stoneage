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

# The administrator character bridge is a file-only module consumed by the
# SAAC main loop. Keep its source in modern/ so the historical source tree
# remains untouched, while the integration patch stays reviewable.
cp /modern/saac_admin_bridge.c /src/saac/saac_admin_bridge.c
cp /modern/saac_admin_bridge.h /src/saac/include/saac_admin_bridge.h
if ! grep -q 'saac_admin_bridge.h' /src/saac/main.c; then
  patch -d /src/saac -p1 < /modern/patches/0009-saac-admin-character-bridge.patch
fi

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

# The private gateway multiplexes browser sessions behind one source IPv4.
# Keep direct-client same-IP protection, with an explicit exact-host exception.
if ! grep -q 'STONEAGE_TRUSTED_GATEWAY_SAME_IP' /src/gmsv/net.c; then
  patch -d /src/gmsv -p1 < /modern/patches/0005-trusted-gateway-same-ip.patch
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

# The legacy no-_RIDELEVEL fallback returned from an unconditional block after
# every successful level check, so that build could never reach ride mapping.
# Keep the historical +5 rule, but scope its message/return to the rejection
# condition without changing the archived CP936 source in the repository.
if ! grep -q 'STONEAGE_SAFE_RIDELEVEL_GUARD' /src/gmsv/char/family.c; then
  patch -d /src/gmsv -p1 < /modern/patches/0010-safe-ride-level-guard.patch
fi

# The 8.5 pet table includes Perusha (100872), but the archived C ride table
# has no corresponding 104025..104036 character sprites. Add that small
# mapping after the common level, loyalty, and AI checks.
if ! grep -q 'STONEAGE_WHITE_TIGER_RIDE' /src/gmsv/char/family.c; then
  patch -d /src/gmsv -p1 < /modern/patches/0011-white-tiger-ride-mapping.patch
fi

# Encounter tables can reference a group or enemy row which was not loaded.
# ENEMY_getEnemy must reject those references before any legacy array accessor;
# keep the data gap explicit instead of allowing an invalid -1 index or
# inventing a replacement enemy.
if ! grep -q 'STONEAGE_SAFE_ENEMY_GROUP_BOUNDS' /src/gmsv/char/enemy.c; then
  patch -d /src/gmsv -p1 < /modern/patches/0014-safe-enemy-group-bounds.patch
fi

# The authenticated web console writes one atomic, fixed-path notice file.
# Let the GMSV consume it in its normal main loop and deliver it to online
# players through the same red system-message path as the built-in announce
# command. The file is never interpreted as a shell command.
if ! grep -q 'STONEAGE_ADMIN_NOTICE' /src/gmsv/main.c; then
  patch -d /src/gmsv -p1 < /modern/patches/0007-admin-notification.patch
fi

# The player administration queue is a private local file bridge. Keep its
# modern module in ASCII/UTF-8 and apply only the small main-loop/makefile
# integration patch to the CP936 archive.
if ! grep -q 'STONEAGE_PlayerAdminProcess' /src/gmsv/main.c; then
  cp /modern/stoneage_player_admin.c /src/gmsv/stoneage_player_admin.c
  cp /modern/stoneage_player_admin.h /src/gmsv/stoneage_player_admin.h
  patch -d /src/gmsv -p1 < /modern/patches/0009-player-admin-bridge.patch
fi

# AI funding is a server capability matched to the live account and save-file
# slot. Policy files are read-only; charge and finite external-transfer audit
# records are written to the separately configured ledger path. Keep the
# modern implementation outside the CP936 archive and apply only integration
# edits to the historical sources.
cp /modern/stoneage_ai_funding.c /src/gmsv/stoneage_ai_funding.c
cp /modern/stoneage_ai_funding.h /src/gmsv/include/stoneage_ai_funding.h
if ! grep -q 'StoneAge_AIFundingRefresh' /src/gmsv/main.c; then
  patch -d /src/gmsv -p1 < /modern/patches/0012-ai-funding.patch
fi

# The AI game bridge requests S("AI") through the normal authenticated
# status path.  Keep its response read-only and character-scoped; the module
# is copied from modern/ and only the small integration patch touches the
# archived CP936 source tree.
cp /modern/stoneage_ai_observation.c /src/gmsv/stoneage_ai_observation.c
cp /modern/stoneage_ai_observation.h /src/gmsv/include/stoneage_ai_observation.h
if ! grep -q 'StoneAge_AIObservationMake' /src/gmsv/callfromcli.c; then
  patch -d /src/gmsv -p1 < /modern/patches/0013-ai-observation.patch
fi

# Prepare shop inventory before charging, and publish it only after funding
# commits. These source edits follow the original funding integration patch.
if ! grep -q 'STONEAGE_AI_PURCHASE_COMMIT' /src/gmsv/npc/npc_itemshop.c; then
  patch -d /src/gmsv -p1 < /modern/patches/0015-ai-funding-transactions.patch
fi

if ! grep -q 'STONEAGE_AI_PET_FUNDING_COMMIT' /src/gmsv/npc/npc_petshop.c; then
  patch -d /src/gmsv -p1 < /modern/patches/0016-ai-pet-funding.patch
fi

# NPC taxes are part of an already delivered service, not player deposits.
# Preserve this origin on failed AC replies even after funding is revoked.
if ! grep -q 'STONEAGE_NPC_TAX_NO_REFUND' /src/gmsv/callfromac.c; then
  patch -d /src/gmsv -p1 < /modern/patches/0017-npc-tax-refund.patch
fi

# Publish both trade-quota entries together before the native exchange.
if ! grep -q 'STONEAGE_AI_TRADE_PAIR_COMMIT' /src/gmsv/char/trade.c; then
  patch -d /src/gmsv -p1 < /modern/patches/0018-ai-trade-pair-funding.patch
fi

# Commit drop quota before changing the purse or publishing ground gold.
if ! grep -q 'STONEAGE_AI_DROP_COMMIT' /src/gmsv/char/char_item.c; then
  patch -d /src/gmsv -p1 < /modern/patches/0019-ai-drop-funding.patch
fi

# Prepare the reviewed adult-ceremony reward before replacing its ingredients.
cp /modern/stoneage_adult_exchange.c /src/gmsv/stoneage_adult_exchange.c
cp /modern/stoneage_adult_exchange.h /src/gmsv/include/stoneage_adult_exchange.h
if ! grep -q 'STONEAGE_ADULT_ITEM_EXCHANGE' /src/gmsv/npc/npc_exchangeman.c; then
  patch -d /src/gmsv -p1 < /modern/patches/0020-adult-item-exchange.patch
fi

# AI character initialization needs deterministic pet levels. Keep the
# historical random-level entry point intact and expose a checked helper for
# the private player-admin bridge.
if ! grep -q 'ENEMY_createPetFromEnemyIndexAtLevel' /src/gmsv/char/enemy.c; then
  patch -d /src/gmsv -p1 < /modern/patches/0021-ai-initialize.patch
fi

# Dedicated immutable player IDs are persisted as a named char field. This
# does not reuse pet ucode or account slots and does not expose unsaved IDs.
cp /modern/stoneage_character_identity.c /src/gmsv/stoneage_character_identity.c
cp /modern/stoneage_character_identity.h /src/gmsv/include/stoneage_character_identity.h
if ! grep -q 'STONEAGE_CHARACTER_IDENTITY' /src/gmsv/include/char_base.h; then
  patch -d /src/gmsv -p1 < /modern/patches/0022-character-identity.patch
fi

# Only a successfully loaded archive establishes externally visible identity.
if ! grep -q 'STONEAGE_LOADED_CHARACTER_ID' /src/gmsv/include/char_base.h; then
  patch -d /src/gmsv -p1 < /modern/patches/0023-loaded-character-identity.patch
fi

# Pair loaded player identity with the exact chat packet at send time.
cp /modern/stoneage_chat_identity.c /src/gmsv/stoneage_chat_identity.c
cp /modern/stoneage_chat_identity.h /src/gmsv/include/stoneage_chat_identity.h
if ! grep -q 'StoneAge_ChatIdentitySend' /src/gmsv/char/char_talk.c; then
  patch -d /src/gmsv -p1 < /modern/patches/0024-chat-identity.patch
fi

# Pair each visible player/party status record with its loaded persistent
# identity. The helper emits companions through the ordinary S path and does
# not alter the original C/N payload.
cp /modern/stoneage_person_identity.c /src/gmsv/stoneage_person_identity.c
cp /modern/stoneage_person_identity.h /src/gmsv/include/stoneage_person_identity.h
if ! grep -q 'StoneAge_PersonIdentitySendC' /src/gmsv/lssproto_serv.c; then
  patch -d /src/gmsv -p1 < /modern/patches/0025-person-identity.patch
fi

# netloop_faster() polls one slot at a time with a zero timeout and only leaves
# the tick once the whole Onelooptime budget has been burned, so an empty or
# idle server holds a core at 100% while doing nothing. Wait for the slot for
# its share of what is left of the tick instead; the round-robin order, the
# per-visit read limit and the write path are unchanged.
if ! grep -q 'STONEAGE_IDLE_NETLOOP_WAIT' /src/gmsv/net.c; then
  patch -d /src/gmsv -p1 < /modern/patches/0026-idle-netloop-wait.patch
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

# NPC names occupy up to 63 bytes, but object broadcasts copied them into a
# 32-byte VIP display buffer. Long arena signboard names tripped Fortify and
# terminated GMSV as soon as a player came into view.
python3 /modernize-object-cstring.py /src/gmsv/char/char.c

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
