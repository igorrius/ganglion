#!/bin/sh
# Checks a built node image before it is published (ADR-0086):
#   scripts/check-image.sh <image> <version> <arch> [<commit>]
# The binary reports the version, the architecture and, when given, the
# commit; Rust with the
# wasm32-wasip1 target and the claude CLI run as the node's user; and the
# node starts with the user guide's minimum configuration
# (docs/guide/getting-started/configure.md) on a fresh volume and gets as
# far as Telegram refusing the placeholder token. Needs docker and network
# access to api.telegram.org.
set -eu

image=$1
version=$2
arch=$3
commit=${4:-}

fail() {
	echo "check-image: $*" >&2
	exit 1
}

echo "== ganglion version"
out=$(docker run --rm --entrypoint ganglion "$image" version --json)
echo "$out"
echo "$out" | grep -q "\"version\": \"$version\"" || fail "version is not $version"
echo "$out" | grep -q "\"arch\": \"$arch\"" || fail "arch is not $arch"
[ -z "$commit" ] || echo "$out" | grep -q "\"commit\": \"$commit\"" || fail "commit is not $commit"

echo "== toolchain as uid 10001"
docker run --rm --user 10001 --entrypoint sh "$image" -c \
	'cargo --version && rustup target list --installed | grep -qx wasm32-wasip1 && echo wasm32-wasip1 && claude --version' ||
	fail "the toolchain does not run as the node's user"

echo "== start with the guide's configuration"
volume="ganglion-check-$$"
trap 'docker volume rm -f "$volume" >/dev/null 2>&1 || true' EXIT
docker volume create "$volume" >/dev/null
# The placeholders of the guide: no real token or id.
docker run --rm -i -v "$volume:/data" --entrypoint sh "$image" -c 'cat > /data/ganglion.yaml' <<'EOF'
telegram:
  token: env:TELEGRAM_BOT_TOKEN
  admin: telegram:user:123456
models:
  providers:
    - {name: claude, type: claude-cli}
  models:
    - {id: sonnet, provider: claude, name: sonnet, tools: true, context: 200000}
    - {id: haiku, provider: claude, name: haiku, tools: true, context: 200000}
  purposes:
    chat: [{model: sonnet, effort: medium}, {model: haiku}]
EOF
# Started as root, as the guide runs it: the entrypoint hands /data to uid
# 10001. The node stops when Telegram rejects the token.
log=$(timeout 120 docker run --rm -v "$volume:/data" -e TELEGRAM_BOT_TOKEN=123456:placeholder "$image" 2>&1) || true
echo "$log"
for want in 'msg="node: agent on"' 'msg="node started"' 'msg="telegram: token rejected"'; do
	echo "$log" | grep -qF "$want" || fail "the node's log has no $want"
done
owner=$(docker run --rm -v "$volume:/data" --entrypoint stat "$image" -c %u /data/ganglion.db)
[ "$owner" = 10001 ] || fail "/data/ganglion.db belongs to uid $owner, not 10001"
echo "check-image: ok"
