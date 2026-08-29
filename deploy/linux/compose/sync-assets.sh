#!/usr/bin/env bash
set -Eeuo pipefail

# This is the only fixed operator command that can use OSS credentials. The
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

access_key="${ALIBABA_CLOUD_ACCESS_KEY_ID:-}"
access_secret="${ALIBABA_CLOUD_ACCESS_KEY_SECRET:-}"
if [[ -z "$access_key" ]]; then
  access_key="$(read_secret "${ALIBABA_CLOUD_ACCESS_KEY_ID_FILE:-/run/secrets/stoneage-oss-access-key-id}")"
fi
if [[ -z "$access_secret" ]]; then
  access_secret="$(read_secret "${ALIBABA_CLOUD_ACCESS_KEY_SECRET_FILE:-/run/secrets/stoneage-oss-access-key-secret}")"
fi
if [[ -z "$access_key" || -z "$access_secret" ]]; then
  echo "OSS credentials are missing; provide the two Docker secret files or ALIBABA_CLOUD_ACCESS_KEY_ID(_FILE) and ALIBABA_CLOUD_ACCESS_KEY_SECRET(_FILE)." >&2
  exit 2
fi

# Pass only file locations to the Go child when secrets are file-backed; the
# secret values stay in the shell's non-exported variables. If a CI caller
# deliberately supplied legacy environment values without files, leave the
# file variables unset so the Go fallback remains usable.
if [[ -n "${ALIBABA_CLOUD_ACCESS_KEY_ID_FILE:-}" ]]; then
  export ALIBABA_CLOUD_ACCESS_KEY_ID_FILE
elif [[ -z "${ALIBABA_CLOUD_ACCESS_KEY_ID:-}" ]]; then
  export ALIBABA_CLOUD_ACCESS_KEY_ID_FILE=/run/secrets/stoneage-oss-access-key-id
fi
if [[ -n "${ALIBABA_CLOUD_ACCESS_KEY_SECRET_FILE:-}" ]]; then
  export ALIBABA_CLOUD_ACCESS_KEY_SECRET_FILE
elif [[ -z "${ALIBABA_CLOUD_ACCESS_KEY_SECRET:-}" ]]; then
  export ALIBABA_CLOUD_ACCESS_KEY_SECRET_FILE=/run/secrets/stoneage-oss-access-key-secret
fi
exec /opt/stoneage/bin/stoneage-assets-sync \
  -config /etc/stoneage/web.toml \
  -assets /opt/stoneage/web-assets \
  -client-data /game/client
