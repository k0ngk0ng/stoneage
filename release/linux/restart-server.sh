#!/usr/bin/env bash
set -euo pipefail

package_root="$(cd "$(dirname "$0")" && pwd)"

# This is the only restart operation exposed to the web console. Keep it a
# fixed, reviewable script so the operator socket never evaluates web input as
# a shell command.
"$package_root/stop-server.sh"
"$package_root/start-server.sh"
