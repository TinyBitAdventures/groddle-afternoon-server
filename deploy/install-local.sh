#!/bin/sh
# Runs the realtime server as a macOS launchd agent for a local Cove site, so it starts at
# login and restarts if it stops. Builds from SRC (default: this checkout; pass a clean
# worktree of the commit you mean to run), installs the binary beside the site and
# (re)loads the agent. Usage: deploy/install-local.sh [SRC]
# Stop it for good: launchctl bootout gui/$(id -u)/com.groddleafternoon.realtime
set -e
SRC=${1:-$(cd "$(dirname "$0")/.." && pwd)}
SITE=${GRODDLE_SITE_DIR:-$HOME/Cove/Sites/groddle.localhost}
ADDR=${GRODDLE_REALTIME_ADDR:-127.0.0.1:8793}
LABEL=com.groddleafternoon.realtime
PLIST=$HOME/Library/LaunchAgents/$LABEL.plist
mkdir -p "$SITE/realtime"
(cd "$SRC" && go build -o "$SITE/realtime/groddle-realtime.new" .)
mv "$SITE/realtime/groddle-realtime.new" "$SITE/realtime/groddle-realtime"
sed -e "s#__BIN__#$SITE/realtime/groddle-realtime#" -e "s#__ADDR__#$ADDR#" \
    -e "s#__SECRET__#$SITE/realtime.secret#" -e "s#__LOG__#$SITE/realtime/realtime.log#" \
    -e "s#__LABEL__#$LABEL#" "$(dirname "$0")/launchd.plist" > "$PLIST"
launchctl bootout "gui/$(id -u)/$LABEL" 2>/dev/null || true
launchctl bootstrap "gui/$(id -u)" "$PLIST"
i=0
until curl -fsS "http://$ADDR/healthz" >/dev/null 2>&1; do
  i=$((i + 1)); [ $i -gt 40 ] && { echo "not answering on $ADDR; see $SITE/realtime/realtime.log"; exit 1; }
  sleep 0.25
done
echo "$LABEL running on $ADDR ($(curl -fsS "http://$ADDR/healthz"))"
