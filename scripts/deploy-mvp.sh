#!/usr/bin/env bash
# Compatibility entry point for existing automation.
set -Eeuo pipefail
script_dir="$(cd "$(dirname "$0")" && pwd)"
exec "$script_dir/deploy.sh" "$@"
