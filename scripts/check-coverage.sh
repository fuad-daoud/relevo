#!/bin/sh
# scripts/check-coverage.sh -- guards per-package statement coverage against
# testdata/coverage-baseline.txt, failing a package that drops more than one
# point below its recorded baseline.
#
# Usage: check-coverage.sh [--write] [FILE]
#   --write  rewrite testdata/coverage-baseline.txt from FILE, instead of
#            checking against it
#   FILE     a `go test -cover` output file (default .coverage.txt)
# Env:   COVERAGE_SCOPE  a file of import paths, one per line: only these
#        packages fail on a drop (a line `./...` means every package); the rest
#        are reported and pass. Unset checks every package.
set -eu

# The baseline sits next to this script's real repo path, not the caller's cwd.
# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
baseline="$here/../testdata/coverage-baseline.txt"

# Statement counting differs across Go minor versions, and some packages have
# platform-only code paths -- a baseline is only comparable to a run from the
# same toolchain minor version and GOOS/GOARCH it was measured on.
goversion=$(go env GOVERSION | grep -oE 'go[0-9]+\.[0-9]+')
current_header="$goversion $(go env GOOS)/$(go env GOARCH)"

write=0
if [ "${1:-}" = "--write" ]; then
	write=1
	shift
fi

case $# in
	0) file=.coverage.txt ;;
	1) file=$1 ;;
	*)
		echo "usage: check-coverage.sh [--write] [FILE]" >&2
		exit 2
		;;
esac

if [ ! -f "$file" ]; then
	echo "check-coverage: no coverage output (run make check)"
	exit 1
fi

work=$(mktemp -d)
# The cleanup must not decide the verdict: in dash a failing EXIT trap's status
# replaces the script's own.
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

# Pull "<pkg> <pct>" out of every `ok  <pkg>  <time>  coverage: <pct>% of
# statements` line. A package built with no test files, or with test files
# but no statements to cover, prints "coverage: [no test files]" or
# "coverage: [no statements]" instead of a percentage and is skipped, which is
# what keeps it out of the baseline.
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
' "$file" | sort -k1,1 > "$work/current"

if [ "$write" -eq 1 ]; then
	mkdir -p "$(dirname -- "$baseline")"
	n=$(wc -l < "$work/current" | tr -d ' ')
	{
		printf '# measured with %s\n' "$current_header"
		cat "$work/current"
	} > "$baseline"
	echo "check-coverage: wrote $n packages to $baseline"
	exit 0
fi

if [ ! -f "$baseline" ]; then
	echo "check-coverage: no baseline at $baseline (run with --write first)"
	exit 1
fi

first_line=$(head -n 1 "$baseline")
case $first_line in
	"# measured with "*)
		baseline_header=${first_line#"# measured with "}
		;;
	*)
		echo "check-coverage: baseline has no header (regenerate it with --write)"
		exit 1
		;;
esac

# Only CI's coverage job compares. The header names the Go minor version, but
# two builds of one minor (a patch apart, or a distribution's build) can count
# a package's statements differently by several points, so a developer machine
# or a server whose header matches would still fail on code nobody touched.
# RELEVO_REQUIRE_COVERAGE=1 is set by that job, and opts a local run in.
if [ "${RELEVO_REQUIRE_COVERAGE:-}" != "1" ]; then
	echo "check-coverage: skipped (the baseline, measured with $baseline_header, is enforced by CI's coverage job; RELEVO_REQUIRE_COVERAGE=1 checks it here)"
	exit 0
fi

if [ "$baseline_header" != "$current_header" ]; then
	echo "check-coverage: baseline was measured with $baseline_header, this is $current_header; regenerate it with --write on the header's env or the CI leg"
	exit 1
fi

# The header line is not a package line.
tail -n +2 "$baseline" > "$work/baseline"

# A package whose tests take timing-dependent paths measures a point or two
# apart run to run, so a change is held only to the packages it touched.
scope=${COVERAGE_SCOPE:-}
if [ -n "$scope" ] && [ ! -f "$scope" ]; then
	echo "check-coverage: COVERAGE_SCOPE names $scope, which does not exist"
	exit 1
fi

in_scope() {
	[ -z "$scope" ] && return 0
	grep -qxF -e "$1" -e "./..." "$scope"
}

fail=0

# Every baseline package: a drop of more than one point fails; a package the
# run no longer reports is noted (it may have been deleted or merged) but does
# not fail -- the coverage guard is not the deletion guard.
while IFS=' ' read -r pkg base; do
	[ -n "$pkg" ] || continue
	pct=$(awk -v p="$pkg" '$1 == p { print $2; found = 1 } END { if (!found) exit 1 }' "$work/current") || pct=""
	if [ -z "$pct" ]; then
		echo "$pkg: not in this run (may have been deleted or merged)"
		continue
	fi
	if awk -v pct="$pct" -v base="$base" 'BEGIN { exit !(pct < base - 1.0) }'; then
		if in_scope "$pkg"; then
			echo "$pkg: coverage $pct% is below baseline $base% (-1.0 allowed)"
			fail=1
		else
			echo "$pkg: coverage $pct% is below baseline $base% (not changed here, not enforced)"
		fi
	fi
done < "$work/baseline"

# Every package the run reports that the baseline has never seen: new, not a
# regression, so it is noted but never fails.
while IFS=' ' read -r pkg _pct; do
	[ -n "$pkg" ] || continue
	if ! awk -v p="$pkg" '$1 == p { found = 1 } END { exit !found }' "$work/baseline"; then
		echo "$pkg: not in baseline (new package?)"
	fi
done < "$work/current"

if [ "$fail" -eq 0 ]; then
	echo "check-coverage: ok"
fi
exit "$fail"
