#!/bin/sh
# scripts/check-comments_test.sh -- tests for the history-comment guard.
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work" 2>/dev/null || :' EXIT
fixtures="$here/testdata/check-comments"

# stage starts an empty repository with the guard at its real path. The guard
# reads the index and the untracked, non-ignored files (git ls-files), so every
# case needs a real tree.
stage() {
	rm -rf "$work/repo"
	mkdir -p "$work/repo/scripts"
	cp "$here/check-comments.sh" "$work/repo/scripts/"
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

# put copies one fixture into the staged repo and tracks it.
put() {
	mkdir -p "$work/repo/fixtures"
	cp "$fixtures/$1" "$work/repo/fixtures/$1"
	(
		cd "$work/repo"
		git add -A
		git -c user.name=test -c user.email=test@example.com -c commit.gpgsign=false commit -q -m test
	)
}

# put_untracked copies one fixture into the staged repo without staging or
# committing it, so the guard must find it through the untracked list rather
# than the index.
put_untracked() {
	mkdir -p "$work/repo/fixtures"
	cp "$fixtures/$1" "$work/repo/fixtures/$1"
}

fail=0
# run executes the guard in the staged repo, leaving its exit status in $status
# and its output in $work/out.
status=0
run() {
	status=0
	(cd "$work/repo" && sh scripts/check-comments.sh "$@") > "$work/out" 2>&1 || status=$?
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

# A repository with no Go file at all lists nothing and passes.
stage
run
expect_exit "an empty file list passes" 0
expect_line "an empty file list prints ok" "check-comments: ok"

# A clean file passes and says so.
stage
put clean.go
run
expect_exit "a clean file passes" 0
expect_line "a clean file prints ok" "check-comments: ok"

# A comment citing an issue number fails, naming the path and line.
stage
put clean.go
put cites-issue.go
run
expect_exit "an issue number fails" 1
expect_line "the issue number is reported" "fixtures/cites-issue.go:4: comment cites history (#NNN or §)"

# A comment citing a spec section fails.
stage
put cites-section.go
run
expect_exit "a spec section fails" 1
expect_line "the spec section is reported" "fixtures/cites-section.go:4: comment cites history (#NNN or §)"

# The accepted false positive: the only // is inside a string literal.
stage
put in-string.go
run
expect_exit "a // inside a string followed by #NNN still fails" 1
expect_line "the in-string hit is reported" "fixtures/in-string.go:5: comment cites history (#NNN or §)"

# --list prints only the distinct failing paths.
stage
put clean.go
put cites-issue.go
put cites-section.go
run --list
expect_exit "--list fails when a file fails" 1
expect_line "--list names the issue fixture" "fixtures/cites-issue.go"
expect_line "--list names the section fixture" "fixtures/cites-section.go"
if grep -qF 'comment cites history' "$work/out"; then
	echo "FAIL: --list printed a message, not just paths"; fail=1
fi
if grep -qF 'clean.go' "$work/out"; then
	echo "FAIL: --list named a clean file"; fail=1
fi

# An allow-listed path is skipped, and one that no longer exists is harmless.
stage
put clean.go
put cites-issue.go
printf 'fixtures/cites-issue.go\nfixtures/gone.go\n' > "$work/repo/scripts/check-comments.allow"
run
expect_exit "an allow-listed path and a missing path pass" 0

# An untracked, non-ignored file citing an issue fails like a tracked one.
stage
put clean.go
put_untracked cites-issue.go
run
expect_exit "an untracked issue citation fails" 1
expect_line "the untracked issue number is reported" "fixtures/cites-issue.go:4: comment cites history (#NNN or §)"

# An untracked clean file passes and says so.
stage
put_untracked clean.go
run
expect_exit "an untracked clean file passes" 0
expect_line "an untracked clean file prints ok" "check-comments: ok"

# An untracked file a .gitignore covers is not scanned at all.
stage
printf 'fixtures/cites-issue.go\n' > "$work/repo/.gitignore"
(
	cd "$work/repo"
	git add -A
	git -c user.name=test -c user.email=test@example.com -c commit.gpgsign=false commit -q -m test
)
put_untracked cites-issue.go
run
expect_exit "an untracked ignored citing file passes" 0
expect_line "an untracked ignored file prints ok" "check-comments: ok"

# An untracked file the allow-list names is skipped like a tracked one.
stage
put clean.go
put_untracked cites-issue.go
printf 'fixtures/cites-issue.go\n' > "$work/repo/scripts/check-comments.allow"
run
expect_exit "an untracked allow-listed file passes" 0
expect_line "an untracked allow-listed file prints ok" "check-comments: ok"

[ "$fail" -eq 0 ] && echo "check-comments: ok"
exit "$fail"