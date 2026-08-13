# Legacy server compatibility layer

The original server source is imported locally from the archive and is not
committed. Files in this directory are the small, reviewable compatibility
layer required to build that source with a current Linux compiler.

`modern/build.sh` is mounted read-only into the build container by
`scripts/build-legacy-server.sh`.

The active runtime is the 2.5 server, not the older 2.0 fallback. It keeps
SAAC's file storage for now. The archived SQL patch is deliberately disabled:
if persistence is modernized, the supported target is MySQL 8.0 with
`utf8mb4`, parameterized queries, password hashing, and migrations. See
`docs/database.md`.
