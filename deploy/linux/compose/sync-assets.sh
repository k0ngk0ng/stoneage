#!/usr/bin/env bash
set -Eeuo pipefail

# This is the only fixed operator command that can use OSS credentials. The
# web/admin HTTP processes never receive the credentials and cannot choose an
# arbitrary source path or destination bucket.
exec /opt/stoneage/bin/stoneage-assets-sync \
  -config /etc/stoneage/web.toml \
  -assets /opt/stoneage/web-assets \
  -client-data /game/client
