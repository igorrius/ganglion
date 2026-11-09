#!/bin/sh
# Starts the node as the unprivileged user ganglion (uid 10001). A platform
# that mounts a volume owned by root (Railway) leaves /data unwritable for that
# user, so when this runs as root it hands /data over first, then drops
# privileges. The node's command sandbox and builder refuse to run as root.
set -eu
if [ "$(id -u)" = 0 ]; then
	if [ "$(stat -c %u /data)" != 10001 ]; then
		chown -R 10001:10001 /data
	fi
	export HOME=/home/ganglion
	exec setpriv --reuid=10001 --regid=10001 --clear-groups "$@"
fi
exec "$@"
