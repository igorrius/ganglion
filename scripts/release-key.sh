#!/usr/bin/env bash
# Makes a key that signs the project's core releases (R-UPD-1, ADR-0081).
#
#   scripts/release-key.sh [name] [backup-file]
#   scripts/release-key.sh --key-only name private-key-file
#
# Writes the public key to update/keys/<name>.pem (commit it), creates the
# GitHub environment "release" that only core tags may deploy to, and sets
# the private key there as RELEASE_SIGNING_KEY. The private key never lands
# in the repository; it is removed from this machine unless a backup file is
# named (keep that one offline). With --key-only it only makes the key and
# writes the private one to the file, for the first step of replacing a key.
# Needs openssl, and gh logged in as a maintainer. Replacing a key:
# docs/releasing.md.
set -euo pipefail

repo="igorrius/ganglion"
key_only=false
if [ "${1:-}" = --key-only ]; then
  key_only=true
  shift
  [ $# -eq 2 ] || { echo "usage: $0 --key-only name private-key-file" >&2; exit 1; }
fi
name="${1:-release-$(date -u +%Y%m%d)}"
backup="${2:-}"
pub="update/keys/$name.pem"

cd "$(git rev-parse --show-toplevel)"
[[ "$name" =~ ^[a-z0-9][a-z0-9-]*$ ]] || { echo "a key name is lower-case letters, digits and dashes" >&2; exit 1; }
[ ! -e "$pub" ] || { echo "$pub exists" >&2; exit 1; }
[ -z "$backup" ] || [ ! -e "$backup" ] || { echo "$backup exists" >&2; exit 1; }

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
umask 077
openssl genpkey -algorithm ed25519 -out "$tmp/key.pem"
openssl pkey -in "$tmp/key.pem" -pubout -out "$pub"
chmod 0644 "$pub"

if $key_only; then
  cp "$tmp/key.pem" "$backup"
  echo "public key: $pub (commit it); private key: $backup (set it as the secret at step 2 of docs/releasing.md)"
  exit 0
fi

# The environment: deployments only from tags v*.*.*.
gh api -X PUT "repos/$repo/environments/release" --silent --input - <<'JSON'
{"deployment_branch_policy": {"protected_branches": false, "custom_branch_policies": true}}
JSON
if ! gh api "repos/$repo/environments/release/deployment-branch-policies" --jq '.branch_policies[] | select(.type=="tag") | .name' | grep -qx 'v*.*.*'; then
  gh api -X POST "repos/$repo/environments/release/deployment-branch-policies" --silent -f name='v*.*.*' -f type=tag
fi
gh secret set RELEASE_SIGNING_KEY --repo "$repo" --env release < "$tmp/key.pem"

if [ -n "$backup" ]; then
  cp "$tmp/key.pem" "$backup"
  echo "private key backup: $backup (keep it offline)"
fi
echo "public key: $pub (commit it); the private key is set as RELEASE_SIGNING_KEY in the release environment"
