#!/bin/sh
# scripts/test-shard.sh -- run shard INDEX of TOTAL.
#
# Usage: test-shard.sh [--dry-run] INDEX TOTAL [OUTDIR]
#   INDEX    0 <= INDEX < TOTAL
#   TOTAL    >= 1
#   OUTDIR   default .shard; created if missing, and existing files in it are
#            overwritten
# Env: SPLIT_PKGS    space-separated import-path patterns whose tests are split
#                    across shards (default ./internal/relevo ./internal/db ./cmd/relevo).
#                    Every package not matched by a pattern is assigned whole.
#      SHARD_WEIGHTS path to the weights file (default scripts/test-shard.weights).
#      SHARD_COVER   0 drops coverage instrumentation from every job; unset or any
#                    other value keeps it (default on).
# Exit: 0 when every go test it ran passed, 1 when any failed, 2 on bad usage.
#
# CI runs three shards of one leg in parallel to balance package test durations:
# whole packages are placed greedily onto the currently least-loaded shard,
# longest first (ties broken by package path in LC_ALL=C order, ties to lowest
# index), according to SHARD_WEIGHTS (default scripts/test-shard.weights). The
# split packages contribute an equal preload (their raw weight) to every shard,
# and their individual tests are distributed round-robin across shards. The
# assignment is deterministic, so the shards are always disjoint and cover
# everything.
#
# Test names come from the package sources rather than `go test -list`: reading
# a file is free, while -list would compile the package a second time.
set -eu

usage() {
	echo "usage: test-shard.sh [--dry-run] INDEX TOTAL [OUTDIR]" >&2
	echo "  SHARD_WEIGHTS points to package weights (default scripts/test-shard.weights)" >&2
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

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
repo=$(CDPATH= cd -- "$here/.." && pwd)
mod=$(cd "$repo" && go list -m 2>/dev/null) || mod=

weights_file=${SHARD_WEIGHTS:-$here/test-shard.weights}
if [ ! -r "$weights_file" ]; then
	echo "test-shard: weights file not found or not readable: $weights_file" >&2
	exit 2
fi

split_pkgs=${SPLIT_PKGS:-./internal/relevo ./internal/db ./cmd/relevo}

# SHARD_COVER=0 turns coverage off for every job. darwin's coverage report
# intermittently fails after a passing run, so the macOS legs set it.
cover=1
case ${SHARD_COVER:-} in
	0) cover=0 ;;
esac

work=$(mktemp -d)
# The cleanup must not decide the verdict: in dash a failing EXIT trap's status
# replaces the script's own.
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

# Resolve the split patterns to import paths. go list prints one path per line
# and de-duplicates, so a pattern matching several packages is fine.
# shellcheck disable=SC2086 # word splitting is the point: one pattern per word
split=$(for pat in $split_pkgs; do go list "$pat"; done)

# The resolved paths reach awk through a file, never through -v: with several split
# packages the list holds newlines, and while GNU awk accepts a newline inside
# a -v assignment, the BSD awk on macOS rejects it. The file lives under $work,
# which the EXIT trap removes.
printf '%s\n' "$split" > "$work/split.txt"

# Whole packages: lookup weights, default 5 for unknown packages.
go list ./... | awk -v weights_file="$weights_file" -v split_file="$work/split.txt" -v mod="$mod" '
	function strip_mod(p) {
		if (mod != "" && substr(p, 1, length(mod) + 1) == (mod "/")) {
			return substr(p, length(mod) + 2)
		}
		if (p == mod) return "."
		return p
	}
	BEGIN {
		while ((getline line < split_file) > 0) {
			if (line != "") skip[line] = 1
		}
		close(split_file)
		while ((getline line < weights_file) > 0) {
			if (line ~ /^#/ || line ~ /^[ \t]*$/) continue
			split(line, parts)
			if (parts[1] != "") weights[parts[1]] = parts[2] + 0
		}
		close(weights_file)
	}
	!($0 in skip) {
		rel = strip_mod($0)
		w = (rel in weights) ? weights[rel] : 5
		print w " " $0
	}
' > "$work/whole_weights.raw"

LC_ALL=C sort -k1,1nr -k2,2 "$work/whole_weights.raw" > "$work/whole_weights.sorted"

# Greedy assignment: longest whole package first onto the currently least-loaded shard,
# ties broken by lowest index. Split packages preload each shard by their raw weight.
awk -v weights_file="$weights_file" -v split_file="$work/split.txt" -v mod="$mod" \
    -v idx="$index" -v total="$total" '
	function strip_mod(p) {
		if (mod != "" && substr(p, 1, length(mod) + 1) == (mod "/")) {
			return substr(p, length(mod) + 2)
		}
		if (p == mod) return "."
		return p
	}
	BEGIN {
		while ((getline line < weights_file) > 0) {
			if (line ~ /^#/ || line ~ /^[ \t]*$/) continue
			split(line, parts)
			if (parts[1] != "") weights[parts[1]] = parts[2] + 0
		}
		close(weights_file)
		split_preload = 0
		while ((getline line < split_file) > 0) {
			if (line == "") continue
			rel = strip_mod(line)
			w = (rel in weights) ? weights[rel] : 5
			split_preload += w
		}
		close(split_file)
		for (i = 0; i < total; i++) {
			load[i] = split_preload
		}
	}
	NF >= 2 {
		w = $1 + 0
		pkg = substr($0, index($0, " ") + 1)
		min_idx = 0
		min_load = load[0]
		for (i = 1; i < total; i++) {
			if (load[i] < min_load) {
				min_load = load[i]
				min_idx = i
			}
		}
		if (min_idx == idx) {
			print pkg
		}
		load[min_idx] += w * total
	}
' "$work/whole_weights.sorted" > "$work/pkgs.txt"

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
	rel=$s
	case $rel in
	"$mod"/*) rel=${rel#"$mod"/} ;;
	esac
	base=$(printf '%s\n' "$rel" | tr '/' '-')
	printf '%s\n' "$s" > "$work/s$n.pkg"
	printf '%s\n' "$base" > "$work/s$n.base"
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

# The coverage flags are one unit: -coverprofile implies coverage, so both come
# and go with the toggle.
cover_flag=
if [ "$cover" -eq 1 ]; then
	cover_flag=-cover
fi

# The whole-package test and each split package's test run concurrently; every
# job writes its output to OUTDIR and the log is replayed after all of them.
if [ -s "$work/pkgs.txt" ]; then
	# shellcheck disable=SC2046,SC2086 # word splitting is the point: one argument per package, and the toggle's flag
	go test -race -count=1 $cover_flag $(cat "$work/pkgs.txt") > "$outdir/whole.txt" 2>&1 &
	printf '%s\t%s\n' "$!" "$outdir/whole.txt" >> "$joblog"
else
	: > "$outdir/whole.txt"
fi

k=0
while [ "$k" -lt "$n" ]; do
	k=$((k + 1))
	pkg=$(cat "$work/s$k.pkg")
	base=$(cat "$work/s$k.base")
	run_re=$(awk 'NF { if (c++) printf "|"; printf "%s", $0 }' "$work/s$k.names")
	coverprofile_flag=
	if [ "$cover" -eq 1 ]; then
		coverprofile_flag="-coverprofile=$outdir/split-$base.out"
	fi
	# shellcheck disable=SC2086 # word splitting is the point: the toggle's flags
	go test -race -count=1 $cover_flag $coverprofile_flag \
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
	base=$(cat "$work/s$k.base")
	cat "$outdir/split-$base.txt"
done

exit "$failed"
