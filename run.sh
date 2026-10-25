#!/bin/sh
# Builds and runs the realtime server for the local Groddle site (Cove routes https://groddle.localhost/ws here).
set -e
cd "$(dirname "$0")"
go build -o bin/groddle-realtime .
exec bin/groddle-realtime -addr "${GRODDLE_REALTIME_ADDR:-127.0.0.1:8793}" -secret-file "${GRODDLE_REALTIME_SECRET_FILE:-$HOME/Cove/Sites/groddle.localhost/realtime.secret}" "$@"
