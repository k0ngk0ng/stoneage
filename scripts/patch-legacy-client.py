#!/usr/bin/env python3
"""Create a local-server copy of the preserved 2.5 Windows client."""

from argparse import ArgumentParser
from hashlib import sha256
from pathlib import Path
import ipaddress
import struct


DEFAULT_SOURCE = Path("runtime/legacy-client/sa_2903.exe")
DEFAULT_OUTPUT = Path("runtime/legacy-client/sa_2903-local.exe")
KNOWN_SOURCE_SHA256 = "9abb989b207d2db6eeb0a95fbc13cfb681eca8a20e9ddc169edb64a03497a3ee"
OLD_PORT = 9125

# This Taiwan-style client deliberately mixes traditional UI wording with a
# mainland Windows code page.  The executable bytes prove that its ANSI code
# page is 936, but GBK can still encode the traditional glyphs used by this
# build.  Keep the byte width unchanged so the surrounding .data layout and
# every compiled pointer remain valid.
CONNECTION_ERROR_OFFSET = 0x7AEF4
CONNECTION_ERROR_ORIGINAL = "无法连接伺服器。".encode("cp936")
CONNECTION_ERROR_LOCAL = "無法連接伺服器。".encode("cp936")
# sa_2903.exe first talks to World Game System (WGS), a long-defunct
# commercial membership gateway, before it starts the ordinary StoneAge
# login flow.  The call below advances that gateway state machine once per
# frame.  Local mode replaces only this call with `mov eax, 1`, the function's
# documented success result, so no WGS socket is opened and all later GMSV
# protocol code remains untouched.
WGS_POLL_CALL_OFFSET = 0x11B14
WGS_POLL_CALL = bytes.fromhex("e81c890200")

# The selected game line enters a second WGS state machine.  A real GMSV
# starts with ``L\0`` while the retired WGS replied with ``A\0`` or ``B\0``.
# Local mode accepts L through the normal A path. The two calls just after
# this branch must remain intact: despite living in the WGS state machine,
# they unpack the login UI's protected account/password buffers before the
# old function-name ClientLogin sender uses them.
WGS_GAME_GREETING_OFFSET = 0x3ABA1
WGS_GAME_GREETING_ORIGINAL = bytes.fromhex("41")
WGS_GAME_GREETING_LOCAL = bytes.fromhex("4c")

# Once ClientLogin has been queued, the retired gateway path normally changes
# the shared connection phase to 3.  The main network loop uses that value to
# take ownership of the socket and feed newline-delimited packets to the
# named-LSSPROTO dispatcher.  Accepting GMSV's L greeting alone skips the WGS
# callback which performed this transition, so the login reply was previously
# read and discarded by the gateway state machine.  Replace the nearby
# ``mov eax, [gateway_state]`` (the value is overwritten immediately after)
# with the missing phase store while preserving the callback-state store that
# follows it.
GAME_PROTOCOL_PHASE_OFFSET = 0x3AC9D
GAME_PROTOCOL_PHASE_VA = 0x43AC9D
GAME_PROTOCOL_PHASE_ORIGINAL = bytes.fromhex("a1782db202")

# This release inserted an old launcher/watchdog block at the start of
# KeyboardReturn().  The block walks a Toolhelp process snapshot and calls
# TerminateProcess before reaching the ordinary IME, history and TK-send
# logic.  Its process assumptions no longer hold under Wine (and are unsafe
# on current Windows), so pressing Return in the chat field destroys the
# client before even updating chathis.dat.  Keep the function's three saved
# registers intact and jump only over that obsolete block.
CHAT_RETURN_WATCHDOG_OFFSET = 0xA65B
CHAT_RETURN_WATCHDOG_VA = 0x40A65B
CHAT_RETURN_WATCHDOG_ORIGINAL = bytes.fromhex("753df605d8")
CHAT_RETURN_CLEANUP_VA = 0x40A724

# The same 2002 process-enumeration watchdog was inlined into three other
# client paths.  The most visible one is MenuProc(): opening either the pet
# or item panel can therefore terminate the whole client on modern Wine.
# The other copies sit in action cleanup and a protected file-loading path.
# They all compare against the same obsolete launcher process name, open the
# matched PID with PROCESS_ALL_ACCESS, and call TerminateProcess.  Skip only
# those exact, hash-checked blocks.  A fifth TerminateProcess import call in
# the Microsoft runtime's fatal-error path is intentionally left untouched.
LEGACY_PROCESS_WATCHDOGS = (
    # This block already has a conditional jump immediately after the seven
    # bytes below. ANDing the detected PID with zero both clears it and sets
    # ZF, so the existing branch takes the normal continuation.
    (
        0x011AB,
        0x4011AB,
        bytes.fromhex("833d880cab0200"),
        bytes.fromhex("8325880cab0200"),
    ),
    # EBP is the function's established zero register here. Clear the stale
    # detected PID and replace its conditional branch with the normal jump.
    (
        0x1012D,
        0x41012D,
        bytes.fromhex("392d880cab020f8484000000"),
        bytes.fromhex("892d880cab02e98500000090"),
    ),
    # This copy pushes ESI and EDI before branching. Preserve those pushes so
    # the function's epilogue remains balanced, while clearing with EBX=0.
    (
        0x4B4E0,
        0x44B4E0,
        bytes.fromhex("391d880cab0256570f8484000000"),
        bytes.fromhex("891d880cab025657e98500000090"),
    ),
)

# The tail of .text is alignment padding in this exact executable.  Local
# mode uses it for a tiny initializer which recreates the server-list data
# WGS used to download.  Addresses below are reverse-engineered globals in
# sa_2903.exe; the source hash and original-byte checks make this deliberately
# specific to that preserved binary.
LOCAL_INIT_FILE_OFFSET = 0x62D60
LOCAL_INIT_VA = 0x462D60
LOCAL_INIT_CAPACITY = 0x2A0
LOCAL_PHASE_HELPER_OFFSET = 0x100
LOCAL_PHASE_HELPER_VA = LOCAL_INIT_VA + LOCAL_PHASE_HELPER_OFFSET
GROUP_COUNT_VA = 0x2AB02F0
GROUP_RECORD_VA = 0x2F3A5A8
GAME_RECORD_VA = 0x2B02F88
CONNECTION_PHASE_VA = 0x2F3B588
GATEWAY_STATE_VA = 0x2B22D78


def mov_bytes(address: int, value: bytes) -> bytes:
    """Encode absolute x86 stores for a short byte string."""
    result = bytearray()
    offset = 0
    while len(value) - offset >= 4:
        result += b"\xc7\x05" + struct.pack("<II", address + offset, struct.unpack_from("<I", value, offset)[0])
        offset += 4
    if len(value) - offset >= 2:
        result += b"\x66\xc7\x05" + struct.pack("<I", address + offset) + value[offset : offset + 2]
        offset += 2
    if len(value) - offset:
        result += b"\xc6\x05" + struct.pack("<I", address + offset) + value[offset : offset + 1]
    return bytes(result)


def build_local_initializer(host: str, port: int) -> bytes:
    # Outer record (72 bytes): enabled, child count, first child index, name.
    # Game record (256 bytes): enabled flag, host, port and display name at
    # offsets 0, 1, 0x80 and 0xc0 respectively.
    group_name = "本機".encode("cp936") + b"\0"
    game_name = "本機一線".encode("cp936") + b"\0"
    code = bytearray()
    code += b"\xc7\x05" + struct.pack("<II", GROUP_COUNT_VA, 1)
    code += mov_bytes(GROUP_RECORD_VA, b"\x01\x01\x00\x00\x00\x00\x00\x00")
    code += mov_bytes(GROUP_RECORD_VA + 8, group_name)
    code += mov_bytes(GAME_RECORD_VA, b"1")
    code += mov_bytes(GAME_RECORD_VA + 1, host.encode("ascii") + b"\0")
    code += mov_bytes(GAME_RECORD_VA + 0x80, str(port).encode("ascii") + b"\0")
    code += mov_bytes(GAME_RECORD_VA + 0xC0, game_name)
    code += b"\xb8\x01\x00\x00\x00\xc3"  # mov eax, 1; ret
    if len(code) > LOCAL_PHASE_HELPER_OFFSET:
        raise SystemExit("internal error: local initializer overlaps phase helper")
    code += b"\0" * (LOCAL_PHASE_HELPER_OFFSET - len(code))
    # Restore the side effect of the WGS callback bypassed in local mode, then
    # execute the original instruction from the patched call site.
    code += b"\xc7\x05" + struct.pack("<II", CONNECTION_PHASE_VA, 3)
    code += b"\xa1" + struct.pack("<I", GATEWAY_STATE_VA)
    code += b"\xc3"
    if len(code) > LOCAL_INIT_CAPACITY:
        raise SystemExit("internal error: local initializer exceeds the code cave")
    return bytes(code)


def find_legacy_endpoint(source: bytes):
    """Find the unique IPv4 string followed by the archived client port."""
    legacy_port = struct.pack("<H", OLD_PORT)
    matches = []
    string_start = 0
    while string_start < len(source):
        nul_offset = source.find(b"\0", string_start)
        if nul_offset < 0:
            break
        candidate = source[string_start:nul_offset]
        if candidate:
            try:
                candidate_text = candidate.decode("ascii")
                ipaddress.IPv4Address(candidate_text)
            except (UnicodeDecodeError, ipaddress.AddressValueError):
                pass
            else:
                if source[nul_offset + 1 : nul_offset + 3] == legacy_port:
                    matches.append((string_start, candidate))
        string_start = nul_offset + 1

    if len(matches) != 1:
        raise SystemExit(f"expected one legacy IPv4 endpoint, found {len(matches)}")
    return matches[0]


def digest(data: bytes) -> str:
    return sha256(data).hexdigest()


def main() -> int:
    parser = ArgumentParser()
    parser.add_argument("--source", type=Path, default=DEFAULT_SOURCE)
    parser.add_argument("--output", type=Path, default=DEFAULT_OUTPUT)
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=9065)
    parser.add_argument(
        "--bypass-wgs",
        action="store_true",
        help="skip the defunct WGS membership gateway and continue locally",
    )
    args = parser.parse_args()

    # The legacy binary has a fixed-size IPv4 string slot. Restrict this tool
    # to numeric IPv4 so a replacement can never overwrite adjacent bytes.
    host = str(ipaddress.IPv4Address(args.host)).encode("ascii")
    if not 1 <= args.port <= 65535:
        raise SystemExit("port must be between 1 and 65535")

    source = args.source.read_bytes()
    source_hash = digest(source)
    if args.source == DEFAULT_SOURCE and source_hash != KNOWN_SOURCE_SHA256:
        raise SystemExit(
            "refusing to patch an unknown sa_2903.exe: "
            f"expected {KNOWN_SOURCE_SHA256}, got {source_hash}"
        )
    endpoint_offset, legacy_endpoint = find_legacy_endpoint(source)
    if len(host) > len(legacy_endpoint):
        raise SystemExit(f"IPv4 address is too long for the {len(legacy_endpoint)}-byte slot")
    port_offset = endpoint_offset + len(legacy_endpoint) + 1
    old_port = struct.unpack_from("<H", source, port_offset)[0]
    if old_port != OLD_PORT:
        raise SystemExit(
            f"expected legacy port {OLD_PORT} after endpoint, found {old_port}"
        )

    replacement = host + b"\0" * (len(legacy_endpoint) - len(host))
    patched = bytearray(source)
    patched[endpoint_offset : endpoint_offset + len(legacy_endpoint)] = replacement
    struct.pack_into("<H", patched, port_offset, args.port)

    actual_connection_error = bytes(
        patched[
            CONNECTION_ERROR_OFFSET :
            CONNECTION_ERROR_OFFSET + len(CONNECTION_ERROR_ORIGINAL)
        ]
    )
    if actual_connection_error != CONNECTION_ERROR_ORIGINAL:
        raise SystemExit(
            "refusing to patch an unknown connection-error string: "
            f"expected {CONNECTION_ERROR_ORIGINAL.hex()}, "
            f"got {actual_connection_error.hex()}"
        )
    if len(CONNECTION_ERROR_LOCAL) != len(CONNECTION_ERROR_ORIGINAL):
        raise SystemExit("internal error: localized connection error changed size")
    patched[
        CONNECTION_ERROR_OFFSET :
        CONNECTION_ERROR_OFFSET + len(CONNECTION_ERROR_LOCAL)
    ] = CONNECTION_ERROR_LOCAL

    if args.bypass_wgs:
        actual_call = bytes(
            patched[WGS_POLL_CALL_OFFSET : WGS_POLL_CALL_OFFSET + len(WGS_POLL_CALL)]
        )
        if actual_call != WGS_POLL_CALL:
            raise SystemExit(
                "refusing to patch an unknown WGS call site: "
                f"expected {WGS_POLL_CALL.hex()}, got {actual_call.hex()}"
            )
        initializer = build_local_initializer(args.host, args.port)
        cave = bytes(
            patched[
                LOCAL_INIT_FILE_OFFSET : LOCAL_INIT_FILE_OFFSET + LOCAL_INIT_CAPACITY
            ]
        )
        if cave != b"\0" * LOCAL_INIT_CAPACITY:
            raise SystemExit("refusing to overwrite a non-empty local initializer cave")
        relative_call = LOCAL_INIT_VA - (0x411B14 + len(WGS_POLL_CALL))
        patched[
            WGS_POLL_CALL_OFFSET : WGS_POLL_CALL_OFFSET + len(WGS_POLL_CALL)
        ] = b"\xe8" + struct.pack("<i", relative_call)
        patched[
            LOCAL_INIT_FILE_OFFSET : LOCAL_INIT_FILE_OFFSET + len(initializer)
        ] = initializer

        actual_greeting = bytes(
            patched[
                WGS_GAME_GREETING_OFFSET :
                WGS_GAME_GREETING_OFFSET + len(WGS_GAME_GREETING_ORIGINAL)
            ]
        )
        if actual_greeting != WGS_GAME_GREETING_ORIGINAL:
            raise SystemExit(
                "refusing to patch an unknown game-gateway greeting branch: "
                f"expected {WGS_GAME_GREETING_ORIGINAL.hex()}, got {actual_greeting.hex()}"
            )
        patched[
            WGS_GAME_GREETING_OFFSET :
            WGS_GAME_GREETING_OFFSET + len(WGS_GAME_GREETING_LOCAL)
        ] = WGS_GAME_GREETING_LOCAL

        actual_phase = bytes(
            patched[
                GAME_PROTOCOL_PHASE_OFFSET :
                GAME_PROTOCOL_PHASE_OFFSET + len(GAME_PROTOCOL_PHASE_ORIGINAL)
            ]
        )
        if actual_phase != GAME_PROTOCOL_PHASE_ORIGINAL:
            raise SystemExit(
                "refusing to patch an unknown game-protocol phase site: "
                f"expected {GAME_PROTOCOL_PHASE_ORIGINAL.hex()}, "
                f"got {actual_phase.hex()}"
            )
        phase_relative_call = LOCAL_PHASE_HELPER_VA - (
            GAME_PROTOCOL_PHASE_VA + len(GAME_PROTOCOL_PHASE_ORIGINAL)
        )
        patched[
            GAME_PROTOCOL_PHASE_OFFSET :
            GAME_PROTOCOL_PHASE_OFFSET + len(GAME_PROTOCOL_PHASE_ORIGINAL)
        ] = b"\xe8" + struct.pack("<i", phase_relative_call)

        actual_chat_watchdog = bytes(
            patched[
                CHAT_RETURN_WATCHDOG_OFFSET :
                CHAT_RETURN_WATCHDOG_OFFSET + len(CHAT_RETURN_WATCHDOG_ORIGINAL)
            ]
        )
        if actual_chat_watchdog != CHAT_RETURN_WATCHDOG_ORIGINAL:
            raise SystemExit(
                "refusing to patch an unknown chat watchdog site: "
                f"expected {CHAT_RETURN_WATCHDOG_ORIGINAL.hex()}, "
                f"got {actual_chat_watchdog.hex()}"
            )
        chat_body_relative_jump = CHAT_RETURN_CLEANUP_VA - (
            CHAT_RETURN_WATCHDOG_VA + len(CHAT_RETURN_WATCHDOG_ORIGINAL)
        )
        patched[
            CHAT_RETURN_WATCHDOG_OFFSET :
            CHAT_RETURN_WATCHDOG_OFFSET + len(CHAT_RETURN_WATCHDOG_ORIGINAL)
        ] = b"\xe9" + struct.pack("<i", chat_body_relative_jump)

        for watchdog_offset, watchdog_va, watchdog_original, watchdog_replacement in (
            LEGACY_PROCESS_WATCHDOGS
        ):
            actual_watchdog = bytes(
                patched[
                    watchdog_offset : watchdog_offset + len(watchdog_original)
                ]
            )
            if actual_watchdog != watchdog_original:
                raise SystemExit(
                    "refusing to patch an unknown process watchdog site at "
                    f"0x{watchdog_va:08x}: expected {watchdog_original.hex()}, "
                    f"got {actual_watchdog.hex()}"
                )
            patched[
                watchdog_offset : watchdog_offset + len(watchdog_original)
            ] = watchdog_replacement

    if len(patched) != len(source):
        raise SystemExit("internal error: patched executable changed size")

    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_bytes(bytes(patched))
    args.output.chmod(args.source.stat().st_mode)
    print(f"source_sha256={source_hash}")
    print(f"output={args.output}")
    print(f"output_sha256={digest(patched)}")
    print(f"endpoint={args.host}:{args.port}")
    print(f"wgs={'bypassed' if args.bypass_wgs else 'enabled'}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
