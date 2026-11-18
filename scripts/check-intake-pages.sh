#!/usr/bin/env bash
# Checks that a published intake page never changes (spec R-INT-3, ADR-0098).
#
#   scripts/check-intake-pages.sh [base]
#
# Every version in intake/published.sha256 must have both copies, the one the
# release embeds (intake/page/<v>/index.html) and the one the site publishes
# (docs/guide/intake/<v>/index.html), with exactly that hash. Every page under
# docs/guide/intake/ must be listed, since the site publishes it. With a base
# commit, every version listed there must still be listed with the same hash:
# a line is never edited or removed.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"
list=intake/published.sha256
fail=0
err() { echo "::error::$*"; fail=1; }

entries() { grep -vE '^(#|$)' "$@"; }

while read -r sum ver; do
	for f in "intake/page/$ver/index.html" "docs/guide/intake/$ver/index.html"; do
		if [ ! -f "$f" ]; then
			err "$f is missing: version $ver is published"
		elif [ "$(sha256sum "$f" | cut -d' ' -f1)" != "$sum" ]; then
			err "$f differs from the published $ver: a published version never changes; make a new version"
		fi
	done
done < <(entries "$list")

for f in docs/guide/intake/*/index.html; do
	[ -e "$f" ] || continue
	ver=$(basename "$(dirname "$f")")
	entries "$list" | awk -v v="$ver" '$2 == v { found = 1 } END { exit !found }' ||
		err "$f is published by the site but $ver is not in $list"
done
for d in docs/guide/intake/*/ intake/page/*/; do
	[ -e "$d" ] || continue
	[ -f "$d/index.html" ] || err "$d holds no index.html"
done

if [ -n "${1:-}" ] && git cat-file -e "$1:$list" 2>/dev/null; then
	while read -r line; do
		entries "$list" | grep -qxF -- "$line" || err "the published line '$line' was edited or removed from $list"
	done < <(git show "$1:$list" | grep -vE '^(#|$)')
fi

[ "$fail" -eq 0 ] && echo "intake pages: every published version is unchanged"
exit "$fail"
