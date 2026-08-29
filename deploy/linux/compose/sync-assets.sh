#!/usr/bin/env bash
set -Eeuo pipefail

# This is the only fixed operator command that can use OSS credentials. The
# web/admin HTTP processes never receive the credentials and cannot choose an
# arbitrary source path or destination bucket.
env_file="${STONEAGE_ASSET_SYNC_ENV_FILE:-/host-project/.env}"

# Read only the two credential assignments from the deployment env file. Do
# not `source` it: .env is operator input and may contain values which are not
# shell syntax. The values are exported only to the short-lived uploader child
# and never become part of the long-running service-control environment.
env_value()
{
  key="$1"
  awk -v key="$key" '
    /^[[:space:]]*#/ || /^[[:space:]]*$/ { next }
    {
      line=$0
      sub(/^[[:space:]]*/, "", line)
      split(line, fields, "=")
      if (fields[1] != key) next
      value=substr(line, length(fields[1])+2)
      sub(/^[[:space:]]*/, "", value)
      sub(/[[:space:]]+#.*$/, "", value)
      if (value ~ /^".*"$/ || value ~ /^\047.*\047$/) value=substr(value, 2, length(value)-2)
      print value
      exit
    }
  ' "$env_file"
}

access_key="${ALIBABA_CLOUD_ACCESS_KEY_ID:-}"
access_secret="${ALIBABA_CLOUD_ACCESS_KEY_SECRET:-}"
if [[ -z "$access_key" && -f "$env_file" ]]; then
  access_key="$(env_value ALIBABA_CLOUD_ACCESS_KEY_ID || true)"
fi
if [[ -z "$access_secret" && -f "$env_file" ]]; then
  access_secret="$(env_value ALIBABA_CLOUD_ACCESS_KEY_SECRET || true)"
fi
if [[ -z "$access_key" || -z "$access_secret" ]]; then
  echo "OSS credentials are missing; configure ALIBABA_CLOUD_ACCESS_KEY_ID and ALIBABA_CLOUD_ACCESS_KEY_SECRET in $env_file." >&2
  exit 2
fi

export ALIBABA_CLOUD_ACCESS_KEY_ID="$access_key"
export ALIBABA_CLOUD_ACCESS_KEY_SECRET="$access_secret"
exec /opt/stoneage/bin/stoneage-assets-sync \
  -config /etc/stoneage/web.toml \
  -assets /opt/stoneage/web-assets \
  -client-data /game/client
