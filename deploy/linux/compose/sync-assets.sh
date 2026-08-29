#!/usr/bin/env bash
set -Eeuo pipefail

# This is the only fixed operator command that can use object-storage credentials. The
# web/admin HTTP processes never receive the credentials and cannot choose an
# arbitrary source path or destination bucket. Docker secrets are preferred;
# environment variables remain a compatibility fallback for direct CI use.
read_secret()
{
  filename="$1"
  [[ -f "$filename" ]] || return 0
  value="$(cat "$filename")"
  # A secret is one logical value. Docker's file secrets often end in a
  # newline, so trim line terminators before handing it to the Go uploader.
  value="${value//$'\r'/}"
  value="${value//$'\n'/}"
  printf '%s' "$value"
}

access_key="${STONEAGE_ASSET_SYNC_ACCESS_KEY:-${CLOUDFLARE_R2_ACCESS_KEY_ID:-${AWS_ACCESS_KEY_ID:-${ALIBABA_CLOUD_ACCESS_KEY_ID:-}}}}"
access_secret="${STONEAGE_ASSET_SYNC_ACCESS_SECRET:-${CLOUDFLARE_R2_SECRET_ACCESS_KEY:-${AWS_SECRET_ACCESS_KEY:-${ALIBABA_CLOUD_ACCESS_KEY_SECRET:-}}}}"
if [[ -z "$access_key" ]]; then
  access_key="$(read_secret "${STONEAGE_ASSET_SYNC_ACCESS_KEY_FILE:-${CLOUDFLARE_R2_ACCESS_KEY_ID_FILE:-${AWS_ACCESS_KEY_ID_FILE:-${ALIBABA_CLOUD_ACCESS_KEY_ID_FILE:-/run/secrets/stoneage-oss-access-key-id}}}}")"
fi
if [[ -z "$access_secret" ]]; then
  access_secret="$(read_secret "${STONEAGE_ASSET_SYNC_ACCESS_SECRET_FILE:-${CLOUDFLARE_R2_SECRET_ACCESS_KEY_FILE:-${AWS_SECRET_ACCESS_KEY_FILE:-${ALIBABA_CLOUD_ACCESS_KEY_SECRET_FILE:-/run/secrets/stoneage-oss-access-key-secret}}}}")"
fi
if [[ -z "$access_key" || -z "$access_secret" ]]; then
  echo "object-storage credentials are missing; provide the two Docker secret files or provider-compatible access-key variables." >&2
  exit 2
fi

# Pass only file locations to the Go child when secrets are file-backed; the
# secret values stay in the shell's non-exported variables. If a CI caller
# deliberately supplied legacy environment values without files, leave the
# file variables unset so the Go fallback remains usable.
if [[ -n "${STONEAGE_ASSET_SYNC_ACCESS_KEY_FILE:-}" ]]; then
  export STONEAGE_ASSET_SYNC_ACCESS_KEY_FILE
elif [[ -z "${STONEAGE_ASSET_SYNC_ACCESS_KEY:-}" && -z "${CLOUDFLARE_R2_ACCESS_KEY_ID:-}" && -z "${AWS_ACCESS_KEY_ID:-}" && -z "${ALIBABA_CLOUD_ACCESS_KEY_ID:-}" ]]; then
  export STONEAGE_ASSET_SYNC_ACCESS_KEY_FILE=/run/secrets/stoneage-oss-access-key-id
fi
if [[ -n "${STONEAGE_ASSET_SYNC_ACCESS_SECRET_FILE:-}" ]]; then
  export STONEAGE_ASSET_SYNC_ACCESS_SECRET_FILE
elif [[ -z "${STONEAGE_ASSET_SYNC_ACCESS_SECRET:-}" && -z "${CLOUDFLARE_R2_SECRET_ACCESS_KEY:-}" && -z "${AWS_SECRET_ACCESS_KEY:-}" && -z "${ALIBABA_CLOUD_ACCESS_KEY_SECRET:-}" ]]; then
  export STONEAGE_ASSET_SYNC_ACCESS_SECRET_FILE=/run/secrets/stoneage-oss-access-key-secret
fi
exec /opt/stoneage/bin/stoneage-assets-sync \
  -config /etc/stoneage/web.toml \
  -assets /opt/stoneage/web-assets \
  -client-data /game/client
