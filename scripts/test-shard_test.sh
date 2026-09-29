#!/bin/sh
# scripts/test-shard_test.sh -- tests for the shard assignment.
#
# Every assertion runs --dry-run, which needs only `go list` and the package
# sources: no test is compiled or run. The invariant under test: for any
# TOTAL, the shards are pairwise
# disjoint and their union is every whole package plus every test name of every
# split package.
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
repo=$(CDPATH= cd -- "$here/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

fail=0
status=0

# dry_run runs test-shard.sh --dry-run for INDEX/TOTAL from the repo root,
# leaving its exit status in $status and its output in $work/out. The script
# resolves packages relative to the cwd, as CI does.
dry_run() {
	status=0
	(cd "$repo" && sh "$here/test-shard.sh" --dry-run "$1" "$2") > "$work/out" 2>&1 || status=$?
}

# The bad-usage cases run without --dry-run, exactly as the usage text reads,
# and must exit 2 before running anything.
check_usage() {
	status=0
	(cd "$repo" && sh "$here/test-shard.sh" "$@") > "$work/out" 2>&1 || status=$?
	if [ "$status" -ne 2 ]; then
		echo "FAIL: 'test-shard.sh $*' exits $status, want 2"
		fail=1
	fi
}

# The TOTAL=1 shard is the whole assignment; every other TOTAL is a partition
# of it, so it is the reference the union is compared against.
dry_run 0 1
if [ "$status" -ne 0 ]; then
	echo "FAIL: TOTAL=1 dry run exits $status, want 0"
	fail=1
fi
LC_ALL=C sort -u "$work/out" > "$work/all.txt"

test_lines=$(grep -c '^test .*/internal/relevo ' "$work/all.txt" || :)
if [ "$test_lines" -le 100 ]; then
	echo "FAIL: TOTAL=1 lists $test_lines internal/relevo tests, want more than 100"
	fail=1
fi

pkg_lines=$(grep -c '^pkg ' "$work/all.txt" || :)
if [ "$pkg_lines" -lt 30 ]; then
	echo "FAIL: TOTAL=1 lists $pkg_lines packages, want at least 30"
	fail=1
fi

for total in 1 2 3 4; do
	: > "$work/union-$total.txt"
	index=0
	while [ "$index" -lt "$total" ]; do
		dry_run "$index" "$total"
		if [ "$status" -ne 0 ]; then
			echo "FAIL: TOTAL=$total INDEX=$index exits $status, want 0"
			fail=1
		fi
		cat "$work/out" >> "$work/union-$total.txt"
		index=$((index + 1))
	done

	# Disjoint: no line is produced by two shards.
	dupes=$(LC_ALL=C sort "$work/union-$total.txt" | uniq -d)
	if [ -n "$dupes" ]; then
		echo "FAIL: TOTAL=$total shards overlap:"
		printf '%s\n' "$dupes"
		fail=1
	fi

	# Union: the shards together are exactly the TOTAL=1 assignment.
	LC_ALL=C sort -u "$work/union-$total.txt" > "$work/union-$total.sorted"
	if ! cmp -s "$work/union-$total.sorted" "$work/all.txt"; then
		echo "FAIL: TOTAL=$total shards do not cover the TOTAL=1 assignment"
		fail=1
	fi
done

# BSD awk rejects a newline inside a -v assignment, where GNU awk accepts it, so
# a shard script that hands awk its newline-separated list through -v passes on
# Linux and fails on macOS. No BSD awk exists here, so the shim below stands in
# for one: it rejects exactly that argument shape and passes the rest to the
# real awk. Every run in this block goes through it.
mkdir -p "$work/bin"
real_awk=$(command -v awk)
export real_awk
cat > "$work/bin/awk" <<'SH'
#!/bin/sh
# A stand-in for BSD awk: a newline inside a -v value is an error there.
nl='
'
prev=
for arg in "$@"; do
	if [ "$prev" = "-v" ]; then
		case $arg in
		*"$nl"*) echo "awk: newline in string $arg" >&2; exit 2 ;;
		esac
	fi
	case $arg in
	-v?*=*)
		case ${arg#-v} in
		*"$nl"*) echo "awk: newline in string $arg" >&2; exit 2 ;;
		esac
		;;
	esac
	prev=$arg
done
exec "$real_awk" "$@"
SH
chmod +x "$work/bin/awk"

# The shim must reject a newline in a -v value, or the cases below would pass
# under the very construct they exist to catch.
if PATH="$work/bin:$PATH" awk -v x="$(printf 'a\nb')" 'BEGIN { print 1 }' </dev/null >/dev/null 2>&1; then
	echo "FAIL: the BSD-awk shim accepted a newline in -v, so it pins nothing"
	fail=1
fi

# SPLIT_PKGS naming two packages is the shape the default list takes once a
# second package joins it. dry_run_split runs --dry-run with both split, under
# the shim.
dry_run_split() {
	status=0
	(cd "$repo" && PATH="$work/bin:$PATH" \
		SPLIT_PKGS="./internal/relevo ./internal/delivery" \
		sh "$here/test-shard.sh" --dry-run "$1" "$2") > "$work/out" 2>&1 || status=$?
}

# TOTAL=1 is the two-package assignment the shards below are compared against.
dry_run_split 0 1
if [ "$status" -ne 0 ]; then
	echo "FAIL: SPLIT_PKGS naming two packages, TOTAL=1 exits $status, want 0"
	fail=1
else
	LC_ALL=C sort -u "$work/out" > "$work/two-all.txt"
	# Each split package must contribute `test` lines rather than a whole `pkg`
	# line: the exit status alone would also hold for a run that skipped nothing.
	for dir in internal/relevo internal/delivery; do
		if grep -q "^pkg .*/$dir\$" "$work/two-all.txt"; then
			echo "FAIL: split package $dir is assigned whole, not split"
			fail=1
		fi
		if ! grep -q "^test .*/$dir " "$work/two-all.txt"; then
			echo "FAIL: split package $dir contributes no tests"
			fail=1
		fi
	done
fi

# TOTAL=3 is the shape CI runs, and two split packages make it the first case
# with more than one split file.
: > "$work/two-union.txt"
bad=0
index=0
while [ "$index" -lt 3 ]; do
	dry_run_split "$index" 3
	if [ "$status" -ne 0 ]; then
		echo "FAIL: SPLIT_PKGS naming two packages, TOTAL=3 INDEX=$index exits $status, want 0"
		fail=1
		bad=1
	else
		cat "$work/out" >> "$work/two-union.txt"
	fi
	index=$((index + 1))
done

# A shard that never reached awk says nothing about the assignment, so the
# partition checks are meaningless when one failed above.
if [ "$bad" -eq 0 ]; then
	dupes=$(LC_ALL=C sort "$work/two-union.txt" | uniq -d)
	if [ -n "$dupes" ]; then
		echo "FAIL: SPLIT_PKGS naming two packages, TOTAL=3 shards overlap:"
		printf '%s\n' "$dupes"
		fail=1
	fi

	LC_ALL=C sort -u "$work/two-union.txt" > "$work/two-union.sorted"
	if ! cmp -s "$work/two-union.sorted" "$work/two-all.txt"; then
		echo "FAIL: SPLIT_PKGS naming two packages, TOTAL=3 shards do not cover the TOTAL=1 assignment"
		fail=1
	fi
fi

# Bad usage: no arguments, INDEX == TOTAL, and a negative INDEX.
check_usage
check_usage 3 3
check_usage -1 2

[ "$fail" -eq 0 ] && echo "test-shard: ok"
exit "$fail"
