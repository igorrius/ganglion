#!/usr/bin/env bash
# Sets each tracked file's modification time from its content, for CI.
#
# go test keeps a test's result only while the files it read keep their size and
# time, and it does not read their content. A fresh checkout gives every file a
# new time, so such a test would always run again. A fixed time would be wrong the
# other way: an edit that keeps the size would reuse the old result. A time taken
# from the file's blob changes exactly when its content does.
set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

git ls-files -s -z | while IFS= read -r -d '' entry; do
	meta=${entry%%$'\t'*}
	file=${entry#*$'\t'}
	blob=${meta#* }
	blob=${blob%% *}
	[ -e "$file" ] || [ -L "$file" ] || continue # deleted in the working tree
	touch -h -d "@$((1000000000 + 0x${blob:0:7}))" -- "$file"
done

# A directory's time comes from the names in it, so a new or removed file shows
# in a test that only stats the directory (internal/testinputs).
find . -type d -not -path './.git' -not -path './.git/*' -print0 | while IFS= read -r -d '' dir; do
	names=$(find "$dir" -mindepth 1 -maxdepth 1 -not -name .git -printf '%f\n' | LC_ALL=C sort | sha256sum)
	touch -d "@$((1000000000 + 0x${names:0:7}))" -- "$dir"
done
