#!/bin/sh
# scripts/check-coverage_test.sh -- tests for the per-package coverage guard.
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work" 2>/dev/null || :' EXIT
fixtures="$here/testdata/check-coverage"

# The header check compares against the toolchain and platform actually
# running this test, so a baseline meant to match must carry that header --
# computed the same way check-coverage.sh computes it.
current_header="$(go env GOVERSION | grep -oE 'go[0-9]+\.[0-9]+') $(go env GOOS)/$(go env GOARCH)"

# stage puts the guard at its real relative path in a scratch tree with the
# fixture baseline, header-stamped for the running environment, as
# testdata/coverage-baseline.txt, matching where the script expects to find
# it (next to its own scripts/ directory).
stage() {
	rm -rf "$work/repo"
	mkdir -p "$work/repo/scripts" "$work/repo/testdata"
	cp "$here/check-coverage.sh" "$work/repo/scripts/"
	{
		printf '# measured with %s\n' "$current_header"
		cat "$fixtures/baseline.txt"
	} > "$work/repo/testdata/coverage-baseline.txt"
}

# stage_baseline is stage, but with $1 copied verbatim as the baseline --
# for fixtures whose header (or lack of one) is the point of the test.
stage_baseline() {
	rm -rf "$work/repo"
	mkdir -p "$work/repo/scripts" "$work/repo/testdata"
	cp "$here/check-coverage.sh" "$work/repo/scripts/"
	cp "$1" "$work/repo/testdata/coverage-baseline.txt"
}

fail=0
status=0
# run executes the guard in the staged repo, leaving its exit status in
# $status and its output in $work/out.
run() {
	status=0
	(cd "$work/repo" && sh scripts/check-coverage.sh "$@") > "$work/out" 2>&1 || status=$?
}

# run_ci is run as CI's coverage job runs it: with RELEVO_REQUIRE_COVERAGE=1,
# the only setting under which the baseline is compared.
run_ci() {
	status=0
	(cd "$work/repo" && RELEVO_REQUIRE_COVERAGE=1 sh scripts/check-coverage.sh "$@") > "$work/out" 2>&1 || status=$?
}

# run_scoped is run_ci on coverage.txt with COVERAGE_SCOPE set to $1. The
# assignment sits inside the subshell: a prefix assignment on a function call
# outlives the call in some shells.
run_scoped() {
	status=0
	(cd "$work/repo" && RELEVO_REQUIRE_COVERAGE=1 COVERAGE_SCOPE="$1" sh scripts/check-coverage.sh coverage.txt) > "$work/out" 2>&1 || status=$?
}

unset RELEVO_REQUIRE_COVERAGE COVERAGE_SCOPE 2>/dev/null || :

expect_exit() { # description, expected exit
	if [ "$status" -ne "$2" ]; then
		echo "FAIL: $1 (exit $status, want $2)"; fail=1
	fi
}

expect_line() { # description, expected output line
	if ! grep -qF -- "$2" "$work/out"; then
		echo "FAIL: $1 (missing: $2)"; fail=1
	fi
}

# A passing output -- every package at or above its baseline, header matching
# the running environment -- exits 0 and says so.
stage
cp "$fixtures/passing.txt" "$work/repo/coverage.txt"
run_ci coverage.txt
expect_exit "a passing output exits 0" 0
expect_line "a passing output prints ok" "check-coverage: ok"

# A regression of 1.5 points, more than the one point allowed, fails and names
# the package and both percentages.
stage
cp "$fixtures/regression.txt" "$work/repo/coverage.txt"
run_ci coverage.txt
expect_exit "a 1.5-point regression fails" 1
expect_line "the regressed package is named" \
	"example.com/pkg/b: coverage 48.5% is below baseline 50.0% (-1.0 allowed)"

# COVERAGE_SCOPE holds a drop only against the packages it lists: a drop in a
# listed package fails, a drop elsewhere is reported and passes.
stage
cp "$fixtures/regression.txt" "$work/repo/coverage.txt"
echo "example.com/pkg/b" > "$work/scope"
run_scoped "$work/scope"
expect_exit "a drop in a scoped package fails" 1
expect_line "the scoped drop is named" \
	"example.com/pkg/b: coverage 48.5% is below baseline 50.0% (-1.0 allowed)"

echo "example.com/pkg/a" > "$work/scope"
run_scoped "$work/scope"
expect_exit "a drop outside the scope passes" 0
expect_line "the unscoped drop is still reported" \
	"example.com/pkg/b: coverage 48.5% is below baseline 50.0% (not changed here, not enforced)"

: > "$work/scope"
run_scoped "$work/scope"
expect_exit "an empty scope passes" 0

echo "./..." > "$work/scope"
run_scoped "$work/scope"
expect_exit "a ./... scope holds every package" 1

run_scoped "$work/no-such-scope"
expect_exit "a missing scope file fails" 1

# A package the baseline has never seen is noted but does not fail the run.
stage
cp "$fixtures/newpkg.txt" "$work/repo/coverage.txt"
run_ci coverage.txt
expect_exit "a new package does not fail" 0
expect_line "the new package is named" "example.com/pkg/c: not in baseline (new package?)"

# A missing FILE fails with the documented message, telling the human to run
# make check first.
stage
run coverage.txt
expect_exit "a missing coverage file fails" 1
expect_line "the missing-file message is printed" \
	"check-coverage: no coverage output (run make check)"

# Without RELEVO_REQUIRE_COVERAGE the baseline is never compared, even when the
# header matches this toolchain: a regression that would fail on CI is
# announced as skipped and exits 0, because another build of the same Go minor
# can measure the same code several points apart.
stage
cp "$fixtures/regression.txt" "$work/repo/coverage.txt"
run coverage.txt
expect_exit "a matching header without the env exits 0 on a regression" 0
expect_line "a matching header without the env is announced as skipped" \
	"check-coverage: skipped (the baseline, measured with $current_header, is enforced by CI's coverage job; RELEVO_REQUIRE_COVERAGE=1 checks it here)"

# A header from a different toolchain/platform without the env is skipped the
# same way.
stage_baseline "$fixtures/baseline-foreign-header.txt"
cp "$fixtures/passing.txt" "$work/repo/coverage.txt"
run coverage.txt
expect_exit "a header mismatch without the env exits 0" 0
expect_line "a header mismatch without the env is announced as skipped" \
	"check-coverage: skipped (the baseline, measured with go0.1 plan9/386, is enforced by CI's coverage job; RELEVO_REQUIRE_COVERAGE=1 checks it here)"

# The same mismatch with RELEVO_REQUIRE_COVERAGE=1 (CI's coverage job) fails
# instead of skipping silently.
stage_baseline "$fixtures/baseline-foreign-header.txt"
cp "$fixtures/passing.txt" "$work/repo/coverage.txt"
run_ci coverage.txt
expect_exit "a header mismatch with the env fails" 1
expect_line "a header mismatch with the env names both headers" \
	"check-coverage: baseline was measured with go0.1 plan9/386, this is $current_header; regenerate it with --write on the header's env or the CI leg"

# A baseline with no header at all -- the pre-header format -- is an error
# telling the human to regenerate it, not a silent skip.
stage_baseline "$fixtures/baseline.txt"
cp "$fixtures/passing.txt" "$work/repo/coverage.txt"
run coverage.txt
expect_exit "a baseline with no header fails" 1
expect_line "the no-header message is printed" \
	"check-coverage: baseline has no header (regenerate it with --write)"

[ "$fail" -eq 0 ] && echo "check-coverage: ok"
exit "$fail"
