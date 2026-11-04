#!/bin/sh
# Proves chatlab's scenarios can fail: each seeded fault goes into a clean
# worktree of HEAD, and its scenario must come out red (exit 1). Then a real
# judge must fail a wrong reply. The working tree is not touched, so commit
# first. It spends subscription limits.
set -u
cd "$(git rev-parse --show-toplevel)" || exit 1
base=$(mktemp -d)
tree="$base/tree"
cleanup() {
	git worktree remove --force "$tree" 2>/dev/null
	rm -rf "$base"
}
trap cleanup EXIT INT TERM
failed=0
while read -r patch scenario; do
	case "$patch" in '' | \#*) continue ;; esac
	git worktree add --quiet --detach "$tree" HEAD || exit 1
	if ! git -C "$tree" apply "$PWD/tools/chatlab/faults/$patch.patch"; then
		echo "FAULT $patch: the patch no longer applies"
		failed=1
	else
		(cd "$tree" && go build -o bin/ganglion . && go build -tags lab -o bin/chatlab ./tools/chatlab) || exit 1
		out=$(cd "$tree" && bin/chatlab run --repeat 1 "$scenario" 2>&1)
		code=$?
		printf '%s\n' "$out" | grep "^| $scenario"
		if [ "$code" -eq 1 ]; then
			echo "FAULT $patch: red, as it must be"
		else
			echo "FAULT $patch: exit $code, but $scenario must fail on it"
			failed=1
		fi
	fi
	git worktree remove --force "$tree"
done <tools/chatlab/faults/faults.txt
if GANGLION_LIVE_JUDGE=1 go test -count=1 -tags lab -run TestJudgeLive ./internal/chatlab/; then
	echo "JUDGE: fails a wrong reply, as it must"
else
	echo "JUDGE: did not hold"
	failed=1
fi
exit "$failed"
