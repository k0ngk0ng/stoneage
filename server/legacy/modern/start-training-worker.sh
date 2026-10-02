#!/bin/sh
# Local training only: no account service, listener, or player credentials.
set -eu
if [ "$#" -ne 0 ]; then
    echo 'start-training-worker.sh accepts no arguments' >&2
    exit 2
fi
cd /opt/stoneage/defaults/gmsv
exec sh /opt/stoneage/bin/run-battle-environment.sh --config setup.cf
