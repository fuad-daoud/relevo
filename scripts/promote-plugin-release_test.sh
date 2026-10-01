#!/bin/sh
# scripts/promote-plugin-release_test.sh -- tests for the plugin-release move.
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
# The cleanup must not decide the verdict: in dash (Ubuntu's /bin/sh) a
# failing EXIT trap's status replaces the script's own, so a teardown hiccup
# reads exactly like a test failure to `sh "$t" || exit 1` in the Makefile
# (#304).
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

# stage builds a bare origin and a clone carrying the annotated tags v1.0.0
# and v2.0.0. Annotated tags are the shape `make release-tag` creates, so
# pushing the tag object instead of its commit would fail here. The clone
# keeps full history, the same shape release.yml's fetch-depth: 0 gives.
stage() {
	rm -rf "$work/origin.git" "$work/repo"
	mkdir -p "$work/repo"
	(
		cd "$work/repo"
		git init -q
		git config user.name test
		git config user.email test@example.com
		git config commit.gpgsign false
		# A detached `git maintenance`/`gc` would write under .git while the
		# EXIT trap removes the tree (#304).
		git config maintenance.auto false
		git config gc.auto 0
		: > f
		git add f
		git commit -q -m one
		git tag -a v1.0.0 -m v1.0.0
		printf 'two\n' > f
		git commit -q -am two
		git tag -a v2.0.0 -m v2.0.0
	)
	git init -q --bare "$work/origin.git"
	(
		cd "$work/repo"
		git remote add origin "$work/origin.git"
		git push -q origin v1.0.0 v2.0.0
	)
}

# sha prints a revision's commit id from the clone.
sha() (
	cd "$work/repo"
	git rev-parse --verify "$1^{commit}"
)

# bare_set points a ref in the bare origin at a commit.
bare_set() (
	cd "$work/origin.git"
	git update-ref "$1" "$2"
)

# remote_sha prints the bare origin's plugin-release tip, or nothing.
remote_sha() {
	git -C "$work/origin.git" rev-parse --verify \
		refs/heads/plugin-release 2>/dev/null || :
}

# run executes the script from the clone, stderr collected in $work/err.
run() {
	(cd "$work/repo" && sh "$here/promote-plugin-release.sh" "$@") 2>"$work/err"
}

fail=0
run_ok() {
	if run "$@" >/dev/null; then :; else
		echo "FAIL: expected success, got exit $? : $*"
		fail=1
	fi
}
run_exit() { # expected-exit, args...
	want=$1; shift
	if run "$@" >/dev/null; then got=0; else got=$?; fi
	if [ "$got" -ne "$want" ]; then
		echo "FAIL: exit $got, want $want : $*"
		fail=1
	fi
}

# create: plugin-release absent on the remote.
stage
run_ok v2.0.0 origin
if [ "$(remote_sha)" != "$(sha v2.0.0)" ]; then
	echo "FAIL: create did not set plugin-release to v2^{commit}"
	fail=1
fi

# fast-forward: the tip is an ancestor of the tag's commit.
stage
bare_set refs/heads/plugin-release "$(sha v1.0.0)"
run_ok v2.0.0 origin
if [ "$(remote_sha)" != "$(sha v2.0.0)" ]; then
	echo "FAIL: fast-forward did not move plugin-release to v2^{commit}"
	fail=1
fi

# re-run: already there is a no-op success.
run_ok v2.0.0 origin
if [ "$(remote_sha)" != "$(sha v2.0.0)" ]; then
	echo "FAIL: re-run changed plugin-release"
	fail=1
fi

# refuse: a tag cut from an older commit while the branch sits at v2.
stage
bare_set refs/heads/plugin-release "$(sha v2.0.0)"
run_exit 1 v1.0.0 origin
if [ "$(remote_sha)" != "$(sha v2.0.0)" ]; then
	echo "FAIL: refusal moved plugin-release"
	fail=1
fi
if ! grep -q "by hand" "$work/err"; then
	echo "FAIL: refusal stderr does not mention the by-hand move"
	fail=1
fi

# unresolvable tag: exit 1 before any remote read.
stage
run_exit 1 v9.9.9 origin

# usage: no tag, and extra args.
stage
run_exit 2
run_exit 2 v1.0.0 origin extra

[ "$fail" -eq 0 ] && echo "promote-plugin-release: ok"
exit "$fail"
