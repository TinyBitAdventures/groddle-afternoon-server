#!/usr/bin/env bash
# Builds a clean checkout of this repo for Linux and installs it on a host set up as readme.md's
# "On a host of its own" describes (the groddle user, /opt/groddle-realtime, /etc/groddle/realtime.secret,
# the systemd unit). Refuses while players are online unless FORCE=1.
# Usage: deploy/install-remote.sh root@45.55.43.61 [<clean checkout>]
set -euo pipefail
host=${1:?usage: deploy/install-remote.sh <user@host> [<clean checkout>]}
src=${2:-$(cd "$(dirname "$0")/.." && pwd)}
cd "$src"
if [ -n "$(git status --porcelain -- '*.go' data go.mod go.sum deploy)" ]; then
	echo "$src has uncommitted server changes: build from a clean checkout" >&2
	exit 1
fi
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$tmp/groddle-realtime" .
players=$(ssh "$host" 'curl -s http://127.0.0.1:8793/healthz || true' | sed -n 's/.*"players":\([0-9]*\).*/\1/p')
if [ "${players:-0}" != 0 ] && [ "${FORCE:-}" != 1 ]; then
	echo "$players players online on $host: run again at 0, or with FORCE=1" >&2
	exit 1
fi
scp -q "$tmp/groddle-realtime" "$host:/opt/groddle-realtime/groddle-realtime.new"
scp -q deploy/groddle-realtime.service "$host:/etc/systemd/system/groddle-realtime.service"
ssh "$host" 'chmod 0755 /opt/groddle-realtime/groddle-realtime.new \
	&& mv /opt/groddle-realtime/groddle-realtime.new /opt/groddle-realtime/groddle-realtime \
	&& systemctl daemon-reload && systemctl restart groddle-realtime && sleep 2 \
	&& systemctl is-active groddle-realtime && curl -s http://127.0.0.1:8793/healthz && echo'
echo "installed $(git rev-parse --short HEAD) on $host"
