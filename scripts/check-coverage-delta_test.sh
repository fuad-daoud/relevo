#!/bin/sh
# scripts/check-coverage-delta_test.sh -- tests for scripts/check-coverage-delta.sh
set -eu

if ! command -v go >/dev/null 2>&1 || ! command -v git >/dev/null 2>&1; then
	echo "go or git not installed; skipping check-coverage-delta tests"
	exit 0
fi

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
trap 'chmod -R u+w "$work" 2>/dev/null || :; rm -rf "$work" 2>/dev/null || :' EXIT

export GIT_AUTHOR_NAME="Test Author" GIT_AUTHOR_EMAIL="test@example.com"
export GIT_COMMITTER_NAME="Test Committer" GIT_COMMITTER_EMAIL="test@example.com"
export GOTOOLCHAIN=local GOFLAGS='' GOPROXY=off
unset BASE RELEVO_BASE

fx="$work/fixture"
mkdir -p "$fx/scripts" "$fx/a"
cp "$here/affected-packages.sh" "$here/check-coverage-delta.sh" "$fx/scripts/"
# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
CDPATH= cd -- "$fx"
git init -q -b main 2>/dev/null || git init -q
printf 'module example.test/fx\n\ngo 1.22\n' > go.mod
cat > a/a.go << 'EOF'
package a

func A(n int) int {
	if n > 0 {
		return n
	}
	return -n
}
EOF
cat > a/a_test.go << 'EOF'
package a

import "testing"

func TestA(t *testing.T) {
	if A(2) != 2 || A(-3) != 3 {
		t.Fatal("A")
	}
}
EOF
git add -A && git commit -qm base
base=$(git rev-parse HEAD)

fail=0
# expect NAME WANT_EXIT WANT_TEXT runs the check over the tree's coverage.
expect() {
	go test -cover ./... > "$work/head.out" 2>&1 || :
	set +e
	out=$(BASE=$base sh scripts/check-coverage-delta.sh "$work/head.out" 2>&1)
	got=$?
	set -e
	if [ "$got" -ne "$2" ] || ! printf '%s\n' "$out" | grep -qF -- "$3"; then
		echo "FAIL $1: exit $got (want $2), output:"
		printf '%s\n' "$out"
		fail=1
	else
		echo "ok   $1"
	fi
}

expect "unchanged tree" 0 "no package's own files changed"

# Untested code in a changed package drops its coverage below the base's.
cat >> a/a.go << 'EOF'

func B(n int) int {
	if n > 10 {
		return n * 2
	}
	if n > 5 {
		return n * 3
	}
	return n
}
EOF
expect "untested code" 1 "is below its base's 100.0%"

cat >> a/a_test.go << 'EOF'

func TestB(t *testing.T) {
	if B(11) != 22 || B(6) != 18 || B(1) != 1 {
		t.Fatal("B")
	}
}
EOF
expect "tested code" 0 "check-coverage-delta: ok"

# A package the base does not have is new, so nothing can drop.
git checkout -q -- a
mkdir b
printf 'package b\n\nfunc C(n int) int {\n\tif n > 0 {\n\t\treturn 1\n\t}\n\treturn 0\n}\n' > b/b.go
printf 'package b\n\nimport "testing"\n\nfunc TestC(t *testing.T) { _ = C(1) }\n' > b/b_test.go
expect "new package" 0 "every changed package is new"

exit "$fail"
