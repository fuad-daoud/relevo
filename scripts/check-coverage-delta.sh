#!/bin/sh
# scripts/check-coverage-delta.sh -- fails a package whose own files changed
# when its statement coverage drops more than one point below the same package
# at the base commit, both measured on this machine.
#
# Usage: check-coverage-delta.sh FILE
#   FILE  `go test -cover` output of the changed tree
# Env:   BASE, RELEVO_BASE  pick the base commit as affected-packages.sh does
#
# testdata/coverage-baseline.txt is held only by CI, because two builds of one
# Go minor can count a package's statements points apart. Measuring the base
# again here, with the same build, compares like with like, so a gate on any
# machine can hold a change to its own packages' coverage.
set -eu

if [ $# -ne 1 ]; then
	echo "usage: check-coverage-delta.sh FILE" >&2
	exit 2
fi
head_file=$1
if [ ! -f "$head_file" ]; then
	echo "check-coverage-delta: no coverage output at $head_file" >&2
	exit 2
fi

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

base=$(sh "$here/affected-packages.sh" --print-base)
if [ "$base" = "./..." ]; then
	echo "check-coverage-delta: skipped (no base commit to compare against)"
	exit 0
fi
pkgs=$(sh "$here/affected-packages.sh" --base "$base" --direct)
if [ -z "$pkgs" ]; then
	echo "check-coverage-delta: ok (no package's own files changed)"
	exit 0
fi
if [ "$pkgs" = "./..." ]; then
	echo "check-coverage-delta: skipped (the build configuration changed; CI's coverage job holds every package)"
	exit 0
fi

work=$(mktemp -d)
# The cleanup must not decide the verdict: in dash a failing EXIT trap's status
# replaces the script's own.
trap 'chmod -R u+w "$work" 2>/dev/null || :; rm -rf "$work" 2>/dev/null || :' EXIT

# pcts prints "<pkg> <pct>" for every package a `go test -cover` output file
# reports as ok with a percentage.
pcts() {
	awk '
		$1 == "ok" {
			for (i = 1; i <= NF; i++) {
				if ($i == "coverage:" && (i + 1) <= NF) {
					pct = $(i + 1)
					if (pct ~ /^[0-9]+\.[0-9]+%$/) {
						sub(/%$/, "", pct)
						print $2, pct
					}
				}
			}
		}
	' "$1" | LC_ALL=C sort -k1,1
}
pcts "$head_file" > "$work/head"

# The base is a plain extract, not a worktree, so a killed run leaves nothing
# registered in the repository.
modpath=$(go list -m)
mkdir "$work/src"
git archive "$base" | tar -x -C "$work/src"

# A package the base does not have is new: there is nothing to drop from.
: > "$work/measure"
for pkg in $pkgs; do
	grep -q "^$pkg " "$work/head" || continue
	if [ "$pkg" = "$modpath" ]; then
		dir=.
	else
		dir=${pkg#"$modpath"/}
	fi
	for f in "$work/src/$dir"/*.go; do
		if [ -f "$f" ]; then
			echo "./$dir" >> "$work/measure"
			break
		fi
	done
done
if [ ! -s "$work/measure" ]; then
	echo "check-coverage-delta: ok (every changed package is new)"
	exit 0
fi

# A failing base test still prints ok lines for the packages that passed, and
# a package with no ok line is not compared: the base's own failures are not
# this change's.
# shellcheck disable=SC1007,SC2046 # CDPATH= scopes an empty CDPATH to cd; one argument per package directory
(CDPATH= cd -- "$work/src" && go test -cover $(cat "$work/measure")) > "$work/base.out" 2>&1 || :
pcts "$work/base.out" > "$work/base"

fail=0
while IFS=' ' read -r pkg base_pct; do
	head_pct=$(awk -v p="$pkg" '$1 == p { print $2 }' "$work/head")
	[ -n "$head_pct" ] || continue
	if awk -v h="$head_pct" -v b="$base_pct" 'BEGIN { exit !(h < b - 1.0) }'; then
		echo "$pkg: coverage $head_pct% is below its base's $base_pct% (-1.0 allowed); test the new code"
		fail=1
	else
		echo "$pkg: coverage $head_pct% (base $base_pct%)"
	fi
done < "$work/base"

if [ "$fail" -eq 0 ]; then
	echo "check-coverage-delta: ok"
fi
exit "$fail"
