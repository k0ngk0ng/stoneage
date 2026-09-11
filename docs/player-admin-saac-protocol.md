# SAAC player-admin file bridge

The administrator never writes a character archive directly. It writes a
request into the dedicated player-admin control volume and waits for the SAAC
main loop to publish the matching response. The bridge has no TCP listener and
does not execute request content as a command.

The default SAAC queue is `/run/stoneage/player-admin/saac`, with requests in
`requests/` and responses in `responses/`. The SAAC process
may be configured with `STONEAGE_PLAYER_ADMIN_DIR` for a deployment-specific
absolute directory. The value is process configuration; it is not accepted in
a request. The module rejects relative paths, traversal components and control
characters. The queue must be owned by the SAAC process and have mode `0700`.
Request and response files are mode `0600`.

## Request files

The caller chooses a fresh request ID of 1–64 ASCII letters, digits, `_`, `-`
or `.` (the exact IDs `.` and `..` are invalid), then atomically renames a
temporary file to `requests/<id>.req`. Reusing an ID while its response exists
is not allowed. A request is an ASCII metadata header followed by `---\n`; write
requests append the exact binary payload after that delimiter:

```text
version=1
id=<id>
op=list|read|write
account=<account>
slot=<0 or 1>                  # read/write only
expected-length=<0..65535>    # write only
new-length=<1..65535>         # write only
---
```

The account is restricted to at most 31 ASCII letters, digits, `_` or `-`. A
role slot is exactly 0 or 1, matching `MAXCHAR_PER_USER`. `list` has no
slot and returns metadata for both slots. `read` returns the complete original
archive bytes for one slot. `write` carries `expected-length` bytes followed
by `new-length` bytes. The expected bytes must be the complete archive that
SAAC currently has; a missing archive matches only an expected length of zero.
An empty new archive is rejected.

The archive lookup is the existing SAAC rule, with no caller path component:

```text
<chardir>/0x<getHash(account) & 0xff>/<account>.<slot>.char
```

## Response files

SAAC atomically publishes `responses/<id>.resp` after consuming the request and then
removes the request. The response always echoes the request ID and includes a
machine-readable code:

```text
version=1
id=<id>
status=ok|error
op=list|read|write|unknown
account=<account>              # when parsed
code=ok|bad_request|invalid_account|invalid_path|invalid_slot|not_found|online|conflict|range|io
---
```

Successful `read` and `write` responses add `slot=<slot>` and
`length=<bytes>`. A successful read appends exactly `length` raw bytes after
the delimiter. A successful list adds `count=<number of present archives>`
and, for each slot in order, `slot=<slot>`, `present=0|1`, and `length=<bytes>`.
Errors have no payload.

For `write`, SAAC checks `isLocked(account)` in this same main-loop turn before
reading or replacing the archive. `online` means the account is currently in
use and the archive was not touched. `conflict` means the complete expected
archive no longer matches. A successful replacement is written to a same
directory temporary file, `fsync`ed, and renamed over the target; the original
bytes are never decoded or reserialized by this bridge.

The focused harness is run through:

```sh
python3 server/legacy/modern/tests/test-saac-admin-bridge.py
```

It compiles the C harness locally and checks online rejection, CAS conflict,
successful atomic replacement, read/list behavior, and slot/account/path and
length rejection.
