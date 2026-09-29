#!/bin/sh
# scripts/test-shard.sh -- run shard INDEX of TOTAL.
#
# Usage: test-shard.sh [--dry-run] INDEX TOTAL [OUTDIR]
#   INDEX    0 <= INDEX < TOTAL
#   TOTAL    >= 1
#   OUTDIR   default .shard; created if missing, and existing files in it are
#            overwritten
# Env: SPLIT_PKGS  space-separated import-path patterns whose tests are split
#                  across shards (default ./internal/relevo). Every package not
#                  matched by a pattern is assigned whole, in go list order.
# Exit: 0 when every go test it ran passed, 1 when any failed, 2 on bad usage.
#
# CI runs three shards of one leg in parallel, which is how internal/relevo's
# ~1,000 tests are split without splitting the package itself: whole package i
# goes to shard i mod TOTAL, and test j of a split package goes to shard
# j mod TOTAL, both over LC_ALL=C sorted lists. The assignment is
# deterministic, so the shards are always disjoint and cover everything.
#
# Test names come from the package sources rather than `go test -list`: reading
# a file is free, while -list would compile the package a second time.
set -eu

usage() {
	echo "usage: test-shard.sh [--dry-run] INDEX TOTAL [OUTDIR]" >&2
}

dry_run=0
case ${1:-} in
	--dry-run)
		dry_run=1
		shift
		;;
esac

case $# in
	2)
		index=$1
		total=$2
		outdir=.shard
		;;
	3)
		index=$1
		total=$2
		outdir=$3
		;;
	*)
		usage
		exit 2
		;;
esac

case $index in
	'' | *[!0-9]*)
		usage
		exit 2
		;;
esac
case $total in
	'' | *[!0-9]*)
		usage
		exit 2
		;;
esac
if [ "$total" -lt 1 ] || [ "$index" -ge "$total" ]; then
	usage
	exit 2
fi

split_pkgs=${SPLIT_PKGS:-./internal/relevo}

work=$(mktemp -d)
# The cleanup must not decide the verdict: in dash a failing EXIT trap's status
# replaces the script's own.
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

# Resolve the split patterns to import paths. go list prints one path per line
# and de-duplicates, so a pattern matching several packages is fine.
# shellcheck disable=SC2086 # word splitting is the point: one pattern per word
split=$(for pat in $split_pkgs; do go list "$pat"; done)

# The resolved paths reach awk through a file, never through -v: with two split
# packages the list holds a newline, and while GNU awk accepts a newline inside
# a -v assignment, the BSD awk on macOS rejects it. The file lives under $work,
# which the EXIT trap removes.
printf '%s\n' "$split" > "$work/split.txt"

# Whole packages: go list ./... minus the split ones, in go list order.
go list ./... | awk -v skip_file="$work/split.txt" -v idx="$index" -v total="$total" '
	BEGIN {
		while ((getline line < skip_file) > 0) {
			if (line != "") skip[line] = 1
		}
	}
	!($0 in skip) {
		if (k++ % total == idx) print
	}
' > "$work/pkgs.txt"

# Each split package's share of its test names, kept in numbered files so the
# dry run and the real run read the same assignment.
n=0
# shellcheck disable=SC2086 # word splitting is the point: one pattern per word
for s in $split; do
	dir=$(go list -f '{{.Dir}}' "$s")
	names=$(grep -hoE '^func (Test[A-Z0-9_][A-Za-z0-9_]*|Example[A-Za-z0-9_]*|Fuzz[A-Za-z0-9_]*)\(' \
		"$dir"/*_test.go 2>/dev/null \
		| sed -e 's/^func //' -e 's/($//' \
		| grep -v '^TestMain$' \
		| LC_ALL=C sort -u) || names=
	mine=$(printf '%s\n' "$names" | awk -v idx="$index" -v total="$total" 'NF && (j++ % total == idx)')
	[ -n "$mine" ] || continue
	n=$((n + 1))
	printf '%s\n' "$s" > "$work/s$n.pkg"
	printf '%s\n' "$mine" > "$work/s$n.names"
done

# A shard that got nothing still prints an empty assignment and exits 0; only
# bad usage is an error.
if [ "$dry_run" -eq 1 ]; then
	{
		if [ -s "$work/pkgs.txt" ]; then
			awk '{ print "pkg " $0 }' "$work/pkgs.txt"
		fi
		k=0
		while [ "$k" -lt "$n" ]; do
			k=$((k + 1))
			pkg=$(cat "$work/s$k.pkg")
			awk -v pkg="$pkg" 'NF { print "test " pkg " " $0 }' "$work/s$k.names"
		done
	} | LC_ALL=C sort
	exit 0
fi

mkdir -p "$outdir"
joblog="$work/jobs"
: > "$joblog"

# The whole-package test and each split package's test run concurrently; every
# job writes its output to OUTDIR and the log is replayed after all of them.
if [ -s "$work/pkgs.txt" ]; then
	# shellcheck disable=SC2046,SC2086 # word splitting is the point: one argument per package
	go test -race -count=1 -cover $(cat "$work/pkgs.txt") > "$outdir/whole.txt" 2>&1 &
	printf '%s\t%s\n' "$!" "$outdir/whole.txt" >> "$joblog"
else
	: > "$outdir/whole.txt"
fi

k=0
while [ "$k" -lt "$n" ]; do
	k=$((k + 1))
	pkg=$(cat "$work/s$k.pkg")
	base=${pkg##*/}
	run_re=$(awk 'NF { if (c++) printf "|"; printf "%s", $0 }' "$work/s$k.names")
	go test -race -count=1 -cover -coverprofile="$outdir/split-$base.out" \
		-run "^($run_re)$" "$pkg" > "$outdir/split-$base.txt" 2>&1 &
	printf '%s\t%s\n' "$!" "$outdir/split-$base.txt" >> "$joblog"
done

failed=0
while IFS="$(printf '\t')" read -r pid _file; do
	if ! wait "$pid"; then
		failed=1
	fi
done < "$joblog"

cat "$outdir/whole.txt"
k=0
while [ "$k" -lt "$n" ]; do
	k=$((k + 1))
	cat "$outdir/split-$(sed 's#.*/##' "$work/s$k.pkg").txt"
done

exit "$failed"
