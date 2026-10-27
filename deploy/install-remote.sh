#!/usr/bin/env bash
# Builds a release of this repo for Linux and installs it on a host set up as readme.md's
# "On a host of its own" describes (the groddle user, /opt/groddle-realtime, /etc/groddle/realtime.secret,
# the systemd unit). The build comes from a temporary worktree of the ref, never the working tree,
# and /healthz then reports the ref as its version. Refuses while players are online unless FORCE=1.
# Usage: deploy/install-remote.sh <user@host> <tag>     e.g. deploy/install-remote.sh root@<host> v0.2.0
set -euo pipefail
host=${1:?usage: deploy/install-remote.sh <user@host> <tag>}
ref=${2:?usage: deploy/install-remote.sh <user@host> <tag>}
repo=$(cd "$(dirname "$0")/.." && pwd)
commit=$(git -C "$repo" rev-parse --verify "$ref^{commit}")
version=$(git -C "$repo" describe --tags --always "$commit")
tmp=$(mktemp -d)
trap 'git -C "$repo" worktree remove --force "$tmp/src" >/dev/null 2>&1 || true; rm -rf "$tmp"' EXIT
git -C "$repo" worktree add --detach --quiet "$tmp/src" "$commit"
(cd "$tmp/src" && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$version" -o "$tmp/groddle-realtime" .)
players=$(ssh "$host" 'curl -s http://127.0.0.1:8793/healthz || true' | sed -n 's/.*"players":\([0-9]*\).*/\1/p')
if [ "${players:-0}" != 0 ] && [ "${FORCE:-}" != 1 ]; then
	echo "$players players online on $host: run again at 0, or with FORCE=1" >&2
	exit 1
fi
scp -q "$tmp/groddle-realtime" "$host:/opt/groddle-realtime/groddle-realtime.new"
scp -q "$tmp/src/deploy/groddle-realtime.service" "$host:/etc/systemd/system/groddle-realtime.service"
ssh "$host" 'chmod 0755 /opt/groddle-realtime/groddle-realtime.new \
	&& mv /opt/groddle-realtime/groddle-realtime.new /opt/groddle-realtime/groddle-realtime \
	&& systemctl daemon-reload && systemctl restart groddle-realtime && sleep 2 \
	&& systemctl is-active groddle-realtime && curl -s http://127.0.0.1:8793/healthz && echo'
echo "installed $version ($(git -C "$repo" rev-parse --short "$commit")) on $host"
