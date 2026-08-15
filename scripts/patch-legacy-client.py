#!/usr/bin/env python3
"""Create a local-server copy of the preserved 2.5 Windows client."""

from argparse import ArgumentParser
from hashlib import sha256
import json
import os
from pathlib import Path
import ipaddress
import struct
import sys
from urllib.error import HTTPError, URLError
from urllib.parse import urlparse
from urllib.request import Request, urlopen

try:
    import tomllib
except ModuleNotFoundError:  # Python < 3.11 can use the optional tomli package.
    try:
        import tomli as tomllib
    except ModuleNotFoundError:
        tomllib = None


DEFAULT_SOURCE = Path("runtime/legacy-client/sa_2903.exe")
DEFAULT_OUTPUT = Path("runtime/legacy-client/sa_2903-local.exe")
KNOWN_SOURCE_SHA256 = "9abb989b207d2db6eeb0a95fbc13cfb681eca8a20e9ddc169edb64a03497a3ee"
OLD_PORT = 9125
DEFAULT_HOST = "127.0.0.1"
DEFAULT_PORT = 9065

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
LOCAL_PHASE_HELPER_MIN_OFFSET = 0x100
GROUP_COUNT_VA = 0x2AB02F0
GROUP_RECORD_VA = 0x2F3A5A8
GAME_RECORD_VA = 0x2B02F88
CONNECTION_PHASE_VA = 0x2F3B588
GATEWAY_STATE_VA = 0x2B22D78
GROUP_RECORD_SIZE = 0x48
GAME_RECORD_SIZE = 0x100
GROUP_NAME_BYTES = 0x40
GAME_NAME_BYTES = 0x40
MAX_SERVER_GROUPS = 8
MAX_SERVER_LINES = 32
MAX_SERVER_LIST_BYTES = 64 * 1024


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


def legacy_text(value: object, label: str, capacity: int) -> bytes:
    if not isinstance(value, str) or not value.strip():
        raise SystemExit(f"{label} must be a non-empty string")
    try:
        encoded = value.encode("cp936")
    except UnicodeEncodeError as error:
        raise SystemExit(f"{label} contains characters not representable in CP936") from error
    if len(encoded) >= capacity:
        raise SystemExit(f"{label} is too long (maximum {capacity - 1} CP936 bytes)")
    return encoded + b"\0"


def parse_endpoint(raw: object, label: str) -> tuple[str, int]:
    if not isinstance(raw, dict):
        raise SystemExit(f"{label} must be an object")
    host_value = raw.get("host")
    if not isinstance(host_value, str):
        raise SystemExit(f"{label}.host must be an IPv4 address")
    try:
        host = str(ipaddress.IPv4Address(host_value))
    except ipaddress.AddressValueError as error:
        raise SystemExit(f"{label}.host must be an IPv4 address") from error
    port_value = raw.get("port")
    if isinstance(port_value, bool) or not isinstance(port_value, int):
        raise SystemExit(f"{label}.port must be an integer")
    if not 1 <= port_value <= 65535:
        raise SystemExit(f"{label}.port must be between 1 and 65535")
    return host, port_value


def normalize_server_list(
    raw: object,
) -> tuple[tuple[str, int], list[tuple[str, list[tuple[str, str, int]]]]]:
    if not isinstance(raw, dict) or not isinstance(raw.get("groups"), list):
        raise SystemExit("server list config must contain a groups array")
    if not raw["groups"]:
        raise SystemExit("server list config must contain at least one group")
    if len(raw["groups"]) > MAX_SERVER_GROUPS:
        raise SystemExit(f"server list config supports at most {MAX_SERVER_GROUPS} groups")

    # New configurations keep gateway addresses in one place and let each
    # visible line reference an endpoint by ID. The old per-line host/port
    # shape remains accepted when no gateways table is present.
    gateways: dict[str, tuple[str, int]] = {}
    gateways_raw = raw.get("gateways")
    if gateways_raw is not None:
        if not isinstance(gateways_raw, list) or not gateways_raw:
            raise SystemExit("server list config gateways must be a non-empty array")
        for gateway_index, gateway in enumerate(gateways_raw):
            if not isinstance(gateway, dict):
                raise SystemExit(f"gateways[{gateway_index}] must be an object")
            gateway_id = gateway.get("id")
            if not isinstance(gateway_id, str) or not gateway_id.strip():
                raise SystemExit(f"gateways[{gateway_index}].id must be a non-empty string")
            gateway_id = gateway_id.strip()
            if gateway_id in gateways:
                raise SystemExit(f"duplicate gateway id {gateway_id!r}")
            gateways[gateway_id] = parse_endpoint(gateway, f"gateways[{gateway_index}]")

    default_endpoint: tuple[str, int] | None = None
    if gateways:
        default_gateway = raw.get("default_gateway")
        if default_gateway is None:
            default_gateway = next(iter(gateways))
        if not isinstance(default_gateway, str) or default_gateway not in gateways:
            raise SystemExit("default_gateway must reference a configured gateway id")
        default_endpoint = gateways[default_gateway]

    groups: list[tuple[str, list[tuple[str, str, int]]]] = []
    line_count = 0
    for group_index, group in enumerate(raw["groups"], start=1):
        if not isinstance(group, dict):
            raise SystemExit(f"groups[{group_index - 1}] must be an object")
        group_name = group.get("name")
        legacy_text(group_name, f"groups[{group_index - 1}].name", GROUP_NAME_BYTES)
        lines = group.get("lines")
        if not isinstance(lines, list) or not lines:
            raise SystemExit(f"groups[{group_index - 1}].lines must be a non-empty array")

        normalized_lines: list[tuple[str, str, int]] = []
        for line_index, line in enumerate(lines, start=1):
            if not isinstance(line, dict):
                raise SystemExit(
                    f"groups[{group_index - 1}].lines[{line_index - 1}] must be an object"
                )
            line_name = line.get("name")
            legacy_text(
                line_name,
                f"groups[{group_index - 1}].lines[{line_index - 1}].name",
                GAME_NAME_BYTES,
            )
            if gateways:
                gateway_id = line.get("gateway")
                if not isinstance(gateway_id, str) or gateway_id not in gateways:
                    raise SystemExit(
                        f"groups[{group_index - 1}].lines[{line_index - 1}].gateway "
                        "must reference a configured gateway id"
                    )
                host, port_value = gateways[gateway_id]
            else:
                host, port_value = parse_endpoint(
                    line,
                    f"groups[{group_index - 1}].lines[{line_index - 1}]",
                )
                if default_endpoint is None:
                    default_endpoint = (host, port_value)
            normalized_lines.append((str(line_name), host, port_value))
            line_count += 1
        groups.append((str(group_name), normalized_lines))

    if line_count > MAX_SERVER_LINES:
        raise SystemExit(f"server list config supports at most {MAX_SERVER_LINES} lines")
    if default_endpoint is None:
        raise SystemExit("server list config could not determine a default gateway")
    return default_endpoint, groups


def parse_server_list_text(
    text: str, source: str, format_hint: str = ""
) -> tuple[tuple[str, int], list[tuple[str, list[tuple[str, str, int]]]]]:
    format_name = format_hint.lower()
    if format_name == "auto":
        errors: list[str] = []
        for candidate in ("json", "toml"):
            try:
                return parse_server_list_text(text, source, candidate)
            except SystemExit as error:
                errors.append(str(error))
        raise SystemExit(
            f"invalid server list config {source}; tried JSON and TOML: "
            + " | ".join(errors)
        )
    if not format_name:
        source_name = source.split("?", 1)[0].lower()
        format_name = "toml" if source_name.endswith(".toml") or ".toml." in source_name else "json"
    try:
        if format_name == "toml":
            if tomllib is None:
                raise SystemExit(
                    "TOML server lists require Python 3.11+ or the optional tomli package"
                )
            raw = tomllib.loads(text)
        elif format_name == "json":
            raw = json.loads(text)
        else:
            raise SystemExit(f"unsupported server list format {format_name!r}")
    except (json.JSONDecodeError, tomllib.TOMLDecodeError if tomllib else ValueError) as error:
        raise SystemExit(f"invalid server list config {source}: {error}") from error
    return normalize_server_list(raw)


def load_server_list(
    path: Path,
) -> tuple[tuple[str, int], list[tuple[str, list[tuple[str, str, int]]]]]:
    try:
        text = path.read_text(encoding="utf-8-sig")
    except OSError as error:
        raise SystemExit(f"cannot read server list config {path}: {error}") from error
    return parse_server_list_text(text, str(path))


def load_server_list_url(
    url: str,
) -> tuple[tuple[str, int], list[tuple[str, list[tuple[str, str, int]]]]]:
    parsed = urlparse(url)
    if parsed.scheme not in {"http", "https"} or not parsed.netloc:
        raise SystemExit("server list URL must use http:// or https://")
    request = Request(url, headers={"Accept": "text/plain, application/toml, application/json"})
    try:
        with urlopen(request, timeout=10) as response:
            payload = response.read(MAX_SERVER_LIST_BYTES + 1)
            content_type = response.headers.get_content_type()
    except (HTTPError, URLError, TimeoutError, OSError) as error:
        raise SystemExit(f"cannot fetch server list {url}: {error}") from error
    if len(payload) > MAX_SERVER_LIST_BYTES:
        raise SystemExit(f"server list response exceeds {MAX_SERVER_LIST_BYTES} bytes")
    try:
        text = payload.decode("utf-8-sig")
    except UnicodeDecodeError as error:
        raise SystemExit(f"server list response is not UTF-8: {error}") from error
    if "toml" in content_type or parsed.path.lower().endswith(".toml"):
        format_hint = "toml"
    elif "json" in content_type or parsed.path.lower().endswith(".json"):
        format_hint = "json"
    else:
        # APIs often expose `/servers` without a filename or a useful
        # Content-Type. Try JSON first and then TOML for those endpoints.
        format_hint = "auto"
    return parse_server_list_text(text, url, format_hint)


def build_local_initializer(
    groups: list[tuple[str, list[tuple[str, str, int]]]]
) -> tuple[bytes, int]:
    # Outer record (72 bytes): enabled, child count, first child index, name.
    # Game record (256 bytes): enabled flag, host, port and display name at
    # offsets 0, 1, 0x80 and 0xc0 respectively.  The old WGS data is copied
    # into these globals before the normal list renderer runs.
    code = bytearray()
    code += b"\xc7\x05" + struct.pack("<II", GROUP_COUNT_VA, len(groups))
    line_index = 0
    for group_index, (group_name, lines) in enumerate(groups):
        group_address = GROUP_RECORD_VA + group_index * GROUP_RECORD_SIZE
        group_header = b"\x01" + bytes((len(lines),)) + b"\0\0" + struct.pack("<I", line_index)
        code += mov_bytes(group_address, group_header)
        code += mov_bytes(group_address + 8, legacy_text(group_name, "group name", GROUP_NAME_BYTES))
        for line_name, host, port in lines:
            game_address = GAME_RECORD_VA + line_index * GAME_RECORD_SIZE
            code += mov_bytes(game_address, b"1")
            code += mov_bytes(game_address + 1, host.encode("ascii") + b"\0")
            code += mov_bytes(game_address + 0x80, str(port).encode("ascii") + b"\0")
            code += mov_bytes(
                game_address + 0xC0,
                legacy_text(line_name, "line name", GAME_NAME_BYTES),
            )
            line_index += 1
    code += b"\xb8\x01\x00\x00\x00\xc3"  # mov eax, 1; ret
    phase_helper_offset = max(
        LOCAL_PHASE_HELPER_MIN_OFFSET, (len(code) + 0x0F) & ~0x0F
    )
    if phase_helper_offset + 16 > LOCAL_INIT_CAPACITY:
        raise SystemExit(
            "server list is too large for the legacy client patch; reduce groups or lines"
        )
    code += b"\0" * (phase_helper_offset - len(code))
    # Restore the side effect of the WGS callback bypassed in local mode, then
    # execute the original instruction from the patched call site.
    code += b"\xc7\x05" + struct.pack("<II", CONNECTION_PHASE_VA, 3)
    code += b"\xa1" + struct.pack("<I", GATEWAY_STATE_VA)
    code += b"\xc3"
    return bytes(code), phase_helper_offset


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
    parser.add_argument(
        "--host",
        help="legacy one-line fallback IPv4 (prefer a gateway in --servers-file)",
    )
    parser.add_argument(
        "--port",
        type=int,
        help="legacy one-line fallback port (prefer a gateway in --servers-file)",
    )
    parser.add_argument(
        "--servers-file",
        type=Path,
        help="UTF-8 TOML (or JSON) file containing gateways, groups and game lines",
    )
    parser.add_argument(
        "--servers-url",
        help="HTTP(S) URL returning a UTF-8 TOML or JSON server list",
    )
    parser.add_argument(
        "--bypass-wgs",
        action="store_true",
        help="skip the defunct WGS membership gateway and continue locally",
    )
    args = parser.parse_args()

    if (args.servers_file or args.servers_url) and not args.bypass_wgs:
        raise SystemExit("--servers-file/--servers-url require --bypass-wgs")
    if (args.servers_file or args.servers_url) and (args.host is not None or args.port is not None):
        raise SystemExit(
            "put gateway host/port in the server-list config; "
            "do not combine it with --host/--port"
        )
    if (args.host is None) != (args.port is None):
        raise SystemExit("--host and --port must be provided together")

    # The archived client is an immutable input.  Catch both the obvious
    # same-path case and an existing hardlink/symlink to the source before any
    # output bytes are written; a launcher must always produce a separate
    # client copy.
    try:
        source_path = args.source.resolve(strict=True)
    except OSError as error:
        raise SystemExit(f"cannot read source client {args.source}: {error}") from error
    output_path = args.output.resolve(strict=False)
    if source_path == output_path:
        raise SystemExit("output client must be different from the original source client")
    try:
        if args.output.exists() and os.path.samefile(source_path, args.output):
            raise SystemExit("output client must not be a hardlink or symlink to the source client")
    except OSError:
        # A missing output (or a parent that has not been created yet) is fine;
        # the normal write path below creates it.
        pass

    if args.servers_url:
        try:
            endpoint, groups = load_server_list_url(args.servers_url)
        except SystemExit as error:
            if args.servers_file is None:
                raise
            print(f"warning: {error}; using local server list fallback", file=sys.stderr)
            endpoint, groups = load_server_list(args.servers_file)
    elif args.servers_file is not None:
        endpoint, groups = load_server_list(args.servers_file)
    else:
        endpoint = parse_endpoint(
            {
                "host": args.host if args.host is not None else DEFAULT_HOST,
                "port": args.port if args.port is not None else DEFAULT_PORT,
            },
            "default gateway",
        )
        groups = [
            (
                "本機",
                [("本機一線", endpoint[0], endpoint[1])],
            )
        ]

    # The legacy binary has a fixed-size IPv4 string slot. Restrict this tool
    # to numeric IPv4 so a replacement can never overwrite adjacent bytes.
    endpoint_host, endpoint_port = endpoint
    host = str(ipaddress.IPv4Address(endpoint_host)).encode("ascii")
    if not 1 <= endpoint_port <= 65535:
        raise SystemExit("port must be between 1 and 65535")

    source = source_path.read_bytes()
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
    struct.pack_into("<H", patched, port_offset, endpoint_port)

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
        initializer, phase_helper_offset = build_local_initializer(groups)
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
        phase_relative_call = LOCAL_INIT_VA + phase_helper_offset - (
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

    output_path.parent.mkdir(parents=True, exist_ok=True)
    output_path.write_bytes(bytes(patched))
    output_path.chmod(source_path.stat().st_mode)
    print(f"source_sha256={source_hash}")
    print(f"output={output_path}")
    print(f"output_sha256={digest(patched)}")
    print(f"endpoint={endpoint_host}:{endpoint_port}")
    print(f"wgs={'bypassed' if args.bypass_wgs else 'enabled'}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
