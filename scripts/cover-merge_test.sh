#!/bin/sh
# scripts/cover-merge_test.sh -- tests for the sharded-coverage merger.
#
# The fixtures are two hand-written shard output dirs over the same four
# blocks: shard A covers blocks 1-2, shard B covers blocks 2-3, and block 4 is
# covered by neither, so the expected percentage needs the union of both
# profiles.
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

fixtures="$here/testdata/cover-merge"
merge="$here/cover-merge.sh"

fail=0
status=0

# run executes the merger, leaving its exit status in $status and its output in
# $work/out.
run() {
	status=0
	sh "$merge" "$@" > "$work/out" 2>&1 || status=$?
}

expect_exit() { # description, expected exit
	if [ "$status" -ne "$2" ]; then
		echo "FAIL: $1 (exit $status, want $2)"
		fail=1
	fi
}

# Both whole.txt lines verbatim, then one synthesized internal/relevo line. The
# four blocks carry 3+5+7+11 statements; the two shards between them cover
# 3+5+7, so the merged percentage is 100 x 15 / 26 = 57.7% -- neither shard's
# own partial percentage (30.8%, 45.5%) is copied.
cat > "$work/expected" <<'EOF'
ok  example.com/pkg/a  0.123s  coverage: 88.8% of statements
ok  example.com/pkg/b  0.003s  coverage: 50.0% of statements
ok  example.com/example/internal/relevo  0.000s  coverage: 57.7% of statements
EOF

run "$fixtures"
expect_exit "merging the shard fixtures exits 0" 0
if ! cmp -s "$work/out" "$work/expected"; then
	echo "FAIL: merged output does not match the expected lines"
	diff "$work/expected" "$work/out" || :
	fail=1
fi

# A directory with no whole.txt at all -- a missing artifact download -- must
# fail instead of printing a partial file with exit 0.
mkdir -p "$work/empty"
run "$work/empty"
expect_exit "an empty dir exits 1" 1

[ "$fail" -eq 0 ] && echo "cover-merge: ok"
exit "$fail"
