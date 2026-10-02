#!/bin/sh
# scripts/check-gofmt_test.sh -- tests the gofmt gate in the Makefile.
#
# The gate is a Makefile recipe, not a script, so this test extracts that recipe
# line and runs it verbatim: a hard-coded copy would keep passing after the
# listing it copies had drifted back to the tracked-only form.
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
root=$(CDPATH= cd -- "$here/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

# gate is the check-static gofmt recipe with make's quoting undone: the leading
# tab and @ stripped, $$ collapsed to $, so the shell sees the line make runs.
gate=$(sed -n '/^check-static:/,/^[^	]/p' "$root/Makefile" |
	awk '/gofmt -l/ && !done { sub(/^	@?/, ""); gsub(/\$\$/, "$"); print; done = 1 }')

if [ -z "$gate" ]; then
	echo "FAIL: no gofmt recipe found in check-static"
	exit 1
fi

# A badly formatted file gofmt must name: the spacing and alignment gofmt
# rewrites are enough to make it a hit, so the fixture stays readable.
write_dirty() {
	printf 'package fixture\n\nfunc  F( ) {\nx:=1\n_ = x\n}\n'
}

write_clean() {
	printf 'package fixture\n\n// limit bounds the loop.\nvar limit = 5\n'
}

stage() {
	rm -rf "$work/repo"
	mkdir -p "$work/repo/fixtures"
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

fail=0
# run executes the extracted gate in the staged repo, leaving its exit status in
# $status and its output in $work/out.
status=0
run() {
	status=0
	(cd "$work/repo" && sh -c "$gate") > "$work/out" 2>&1 </dev/null || status=$?
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

# A repository with no Go file lists nothing and stays green.
stage
run
expect_exit "an empty file list passes" 0

# A tracked, formatted file passes.
stage
write_clean > "$work/repo/fixtures/clean.go"
commit
run
expect_exit "a tracked formatted file passes" 0

# A tracked, badly formatted file is named and fails.
stage
write_dirty > "$work/repo/fixtures/dirty.go"
commit
run
expect_exit "a tracked badly formatted file fails" 1
expect_line "the tracked dirty file is named" "fixtures/dirty.go"

# An untracked, non-ignored file is named and fails: it is code someone will
# read and commit, so the gate formats it like a tracked one.
stage
write_clean > "$work/repo/fixtures/clean.go"
commit
write_dirty > "$work/repo/fixtures/dirty.go"
run
expect_exit "an untracked badly formatted file fails" 1
expect_line "the untracked dirty file is named" "fixtures/dirty.go"

# An untracked, formatted file passes.
stage
write_clean > "$work/repo/fixtures/clean.go"
run
expect_exit "an untracked formatted file passes" 0

# An untracked, badly formatted file a .gitignore covers is not listed: nobody
# will commit it, so it must not decide the gate.
stage
printf 'fixtures/\n' > "$work/repo/.gitignore"
commit
write_dirty > "$work/repo/fixtures/dirty.go"
run
expect_exit "an untracked ignored badly formatted file passes" 0

[ "$fail" -eq 0 ] && echo "check-gofmt: ok"
exit "$fail"