#!/bin/sh
# Runs the ganglion binary in a local container over a data directory, so
# `ganglion mcp` can be reached through `docker exec -i` as in production
# (R-OBS-7, plan M3c task 6). No image is built: a static binary is
# mounted into busybox.
#
# Usage: scripts/diag-container.sh <data dir>   (holds ganglion.db and logs/)
#        scripts/diag-container.sh stop
set -eu
name=ganglion-diag
if [ "${1:-}" = stop ]; then
	docker rm -f "$name" >/dev/null
	exit 0
fi
[ -n "${1:-}" ] || { echo "usage: $0 <data dir> | stop" >&2; exit 2; }
data=$(cd "$1" && pwd)
mkdir -p bin
CGO_ENABLED=0 go build -o bin/ganglion-static .
docker rm -f "$name" >/dev/null 2>&1 || true
docker run -d --name "$name" -v "$data:/data" -v "$PWD/bin/ganglion-static:/usr/local/bin/ganglion:ro" \
	busybox:1.37.0 sleep 86400 >/dev/null
cat <<JSON
Container $name is up. MCP server config for Claude Code (.mcp.json):
{"mcpServers": {"ganglion": {"command": "docker",
  "args": ["exec", "-i", "$name", "ganglion", "mcp", "--db", "/data/ganglion.db"]}}}
Stop it with: $0 stop
JSON
