# Legacy server compatibility layer

The complete StoneAge 2.5 GMSV and SAAC source lives in
`source/2.5/gmsv` and `source/2.5/saac`. It is part of this repository, so a
release build does not depend on a private archive or a developer's machine.
Generated objects, executables, logs, and live character/account data remain
ignored; the compatibility files in `modern/` are applied during each build.

`modern/build.sh` is copied read-only into the build container by
`scripts/build-legacy-server.sh` and by `deploy/linux/legacy-runtime.Dockerfile`.

The active runtime is the 2.5 server, not the older 2.0 fallback. It keeps
SAAC's file storage for now. The archived SQL patch is deliberately disabled:
if persistence is modernized, the supported target is MySQL 8.0 with
`utf8mb4`, parameterized queries, password hashing, and migrations. See
`docs/database.md`.
