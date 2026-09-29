#!/bin/sh
# scripts/check-filesize_test.sh -- tests for the file-size guard.
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work" 2>/dev/null || :' EXIT
fixtures="$here/testdata/check-filesize"

# stage starts an empty repository with the guard at its real path. The guard
# reads the index (git ls-files), so every case needs a real, tracked tree.
stage() {
	rm -rf "$work/repo"
	mkdir -p "$work/repo/scripts"
	cp "$here/check-filesize.sh" "$work/repo/scripts/"
	(
		cd "$work/repo"
		git init -q
		git config user.name test
		git config user.email test@example.com
		git config commit.gpgsign false
		# A detached git maintenance/gc would write under .git while the EXIT
		# trap removes the tree.
		git config maintenance.auto false
		git config gc.auto 0
	)
}

commit() {
	(
		cd "$work/repo"
		git add -A
		git -c user.name=test -c user.email=test@example.com -c commit.gpgsign=false commit -q -m test
	)
}

# put copies one fixture into the staged repo and tracks it.
put() {
	mkdir -p "$work/repo/fixtures"
	cp "$fixtures/$1" "$work/repo/fixtures/$1"
	commit
}

fail=0
# run executes the guard in the staged repo, leaving its exit status in $status
# and its output in $work/out.
status=0
run() {
	status=0
	(cd "$work/repo" && sh scripts/check-filesize.sh "$@") > "$work/out" 2>&1 || status=$?
}

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

# A small file passes and says so.
stage
put small.go
run
expect_exit "a small file passes" 0
expect_line "a small file prints ok" "check-filesize: ok"

# 601 lines fails, naming the path and the count.
stage
put big.go
run
expect_exit "601 lines fail" 1
expect_line "the over-size file is reported" "fixtures/big.go: 601 lines (max 600)"

# Exactly 600 lines passes, and a long test file is never counted.
stage
mkdir -p "$work/repo/fixtures"
{
	printf 'package edge\n\n'
	i=0
	while [ "$i" -lt 598 ]; do
		printf 'var v%04d = %d\n' "$i" "$i"
		i=$((i + 1))
	done
} > "$work/repo/fixtures/edge.go"
cp "$fixtures/big.go" "$work/repo/fixtures/big_test.go"
commit
run
expect_exit "600 lines and a long test file pass" 0

# --list prints only the distinct failing paths.
stage
put small.go
put big.go
run --list
expect_exit "--list fails when a file fails" 1
expect_line "--list names the over-size fixture" "fixtures/big.go"
if grep -qF 'lines (max' "$work/out"; then
	echo "FAIL: --list printed a count, not just a path"; fail=1
fi
if grep -qF 'small.go' "$work/out"; then
	echo "FAIL: --list named a small file"; fail=1
fi

# An allow-listed path is skipped, and one that no longer exists is harmless.
stage
put big.go
printf 'fixtures/big.go\nfixtures/gone.go\n' > "$work/repo/scripts/check-filesize.allow"
run
expect_exit "an allow-listed path and a missing path pass" 0

[ "$fail" -eq 0 ] && echo "check-filesize: ok"
exit "$fail"
