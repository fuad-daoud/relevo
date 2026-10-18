#!/bin/sh
# scripts/affected-packages_test.sh -- tests for scripts/affected-packages.sh
set -eu

if ! command -v go >/dev/null 2>&1 || ! command -v git >/dev/null 2>&1; then
	echo "go or git not installed; skipping affected-packages tests"
	exit 0
fi

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
script="$here/affected-packages.sh"
work=$(mktemp -d)
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

fx="$work/fixture"
mkdir -p "$fx"

export GIT_AUTHOR_NAME="Test Author"
export GIT_AUTHOR_EMAIL="test@example.com"
export GIT_AUTHOR_DATE="2026-01-01T00:00:00Z"
export GIT_COMMITTER_NAME="Test Committer"
export GIT_COMMITTER_EMAIL="test@example.com"
export GIT_COMMITTER_DATE="2026-01-01T00:00:00Z"

export GOTOOLCHAIN=local
export GOFLAGS=
export GOPROXY=off

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
CDPATH= cd -- "$fx"

git init -b main >/dev/null 2>&1 || git init >/dev/null 2>&1
git config user.name "$GIT_AUTHOR_NAME"
git config user.email "$GIT_AUTHOR_EMAIL"

cat << 'EOF' > go.mod
module example.test/fx

go 1.22
EOF

mkdir -p a/sub a/testdata b c d e f

cat << 'EOF' > a/a.go
package a

func A() string { return "a" }
EOF

cat << 'EOF' > a/extra.go
package a

func Extra() string { return "extra" }
EOF

cat << 'EOF' > a/sub/sub.go
package sub

func Sub() string { return "sub" }
EOF

echo "fixture testdata" > a/testdata/x.txt

cat << 'EOF' > b/b.go
package b

import "example.test/fx/a"

func B() string { return a.A() }
EOF

cat << 'EOF' > c/c.go
package c

import "example.test/fx/b"

func C() string { return b.B() }
EOF

cat << 'EOF' > d/d.go
package d

func D() string { return "d" }
EOF

cat << 'EOF' > d/d_test.go
package d

import (
	"testing"

	"example.test/fx/a"
)

func TestD(t *testing.T) {
	_ = a.A()
}
EOF

cat << 'EOF' > e/e.go
package e

func E() string { return "e" }
EOF

cat << 'EOF' > e/e_test.go
package e_test

import (
	"testing"

	"example.test/fx/a"
)

func TestE(t *testing.T) {
	_ = a.A()
}
EOF

echo "embedded content" > f/embed.txt

cat << 'EOF' > f/f.go
package f

import _ "embed"

//go:embed embed.txt
var S string

func F() string { return S }
EOF

git add -A
git commit -m "initial commit" >/dev/null
git update-ref refs/remotes/origin/main HEAD

fail=0

reset_fixture() {
	git reset --hard origin/main >/dev/null 2>&1
	git clean -fdx >/dev/null 2>&1
}

assert_stdout() {
	case_name="$1"
	want="$2"
	shift 2

	out="$work/stdout"
	err="$work/stderr"
	status=0
	(sh "$script" "$@") > "$out" 2> "$err" || status=$?

	if [ "$status" -ne 0 ]; then
		echo "FAIL: $case_name: script exited $status, want 0"
		fail=1
		return
	fi

	got=$(cat "$out")
	if [ "$got" != "$want" ]; then
		echo "FAIL: $case_name"
		echo "  got:  $got"
		echo "  want: $want"
		fail=1
	fi
}

want_a="example.test/fx/a
example.test/fx/b
example.test/fx/c
example.test/fx/d
example.test/fx/e"

# 1. edit in a gives a b c d e
reset_fixture
echo "// edit" >> a/a.go
assert_stdout "edit in a gives a b c d e" "$want_a"

# 2. edit in f gives only f
reset_fixture
echo "// edit" >> f/f.go
assert_stdout "edit in f gives only f" "example.test/fx/f"

# 3. testdata file under a gives the same as a
reset_fixture
echo "change" >> a/testdata/x.txt
assert_stdout "testdata file under a gives the same as a" "$want_a"

# Embedded file under f gives only f
reset_fixture
echo "change" >> f/embed.txt
assert_stdout "embedded file in f gives only f" "example.test/fx/f"

# 4. docs-only change gives empty output
reset_fixture
mkdir -p docs
echo "docs" > docs/x.md
echo "readme" > README.md
assert_stdout "docs-only change (docs/x.md, root README.md) gives empty output" ""

# 5. go.mod / Makefile / .golangci.yml / .github/x.yml / unknown top dir gives ./...
reset_fixture
echo "// mod" >> go.mod
assert_stdout "go.mod gives all" "./..."

reset_fixture
touch Makefile
assert_stdout "Makefile gives all" "./..."

reset_fixture
touch .golangci.yml
assert_stdout ".golangci.yml gives all" "./..."

reset_fixture
mkdir -p .github
touch .github/x.yml
assert_stdout ".github/x.yml gives all" "./..."

reset_fixture
mkdir -p unknown
touch unknown/x.txt
assert_stdout "unknown top dir gives all" "./..."

# 6. untracked new .go file in f is included
reset_fixture
cat << 'EOF' > f/new.go
package f

func NewF() {}
EOF
assert_stdout "untracked new .go file in f is included" "example.test/fx/f"

# 7. staged-only and unstaged-only edits are both included
reset_fixture
echo "// staged" >> a/a.go
git add a/a.go
echo "// unstaged" >> f/f.go
want_staged_unstaged="example.test/fx/a
example.test/fx/b
example.test/fx/c
example.test/fx/d
example.test/fx/e
example.test/fx/f"
assert_stdout "staged-only and unstaged-only edits are both included" "$want_staged_unstaged"

# 8. base override restricts diff to changes since that ref
reset_fixture
echo "// committed edit" >> a/a.go
git commit -am "edit a" >/dev/null
assert_stdout "base override flag --base HEAD gives empty" "" --base HEAD
BASE=HEAD
export BASE
assert_stdout "base override BASE=HEAD gives empty" ""
unset BASE

assert_stdout "base override --base HEAD~1 gives a b c d e" "$want_a" --base HEAD~1
BASE=HEAD~1
export BASE
assert_stdout "base override BASE=HEAD~1 gives a b c d e" "$want_a"
unset BASE

# 9. missing origin/main gives ./... plus a stderr note
reset_fixture
git update-ref -d refs/remotes/origin/main
out="$work/stdout"
err="$work/stderr"
status=0
(sh "$script") > "$out" 2> "$err" || status=$?
if [ "$status" -ne 0 ]; then
	echo "FAIL: missing origin/main: exit $status, want 0"
	fail=1
fi
if [ "$(cat "$out")" != "./..." ]; then
	echo "FAIL: missing origin/main: got '$(cat "$out")', want './...'"
	fail=1
fi
if [ ! -s "$err" ]; then
	echo "FAIL: missing origin/main: stderr is empty, want note"
	fail=1
fi
git update-ref refs/remotes/origin/main HEAD

# 9b. RELEVO_BASE: used when no --base/BASE is given, ahead of origin/main
reset_fixture
initial=$(git rev-parse HEAD)
echo "// committed edit" >> a/a.go
git commit -am "edit a" >/dev/null
git update-ref -d refs/remotes/origin/main
RELEVO_BASE="$initial"
export RELEVO_BASE
assert_stdout "RELEVO_BASE with no origin/main gives a b c d e" "$want_a"
assert_stdout "--direct with RELEVO_BASE and no origin/main gives only a" "example.test/fx/a" --direct
assert_stdout "--base HEAD beats RELEVO_BASE" "" --base HEAD
BASE=HEAD
export BASE
assert_stdout "BASE=HEAD beats RELEVO_BASE" ""
unset BASE
RELEVO_BASE=""
assert_stdout "empty RELEVO_BASE is ignored" "./..."
unset RELEVO_BASE
git update-ref refs/remotes/origin/main "$initial"

# RELEVO_BASE beats origin/main
RELEVO_BASE=HEAD
export RELEVO_BASE
assert_stdout "RELEVO_BASE beats origin/main" ""
unset RELEVO_BASE

# an unresolvable RELEVO_BASE falls through to origin/main with a note
RELEVO_BASE=0000000000000000000000000000000000000000
export RELEVO_BASE
out="$work/stdout"
err="$work/stderr"
status=0
(sh "$script") > "$out" 2> "$err" || status=$?
unset RELEVO_BASE
if [ "$status" -ne 0 ]; then
	echo "FAIL: unresolvable RELEVO_BASE: exit $status, want 0"
	fail=1
fi
if [ "$(cat "$out")" != "$want_a" ]; then
	echo "FAIL: unresolvable RELEVO_BASE: got '$(cat "$out")', want a b c d e"
	fail=1
fi
if [ ! -s "$err" ]; then
	echo "FAIL: unresolvable RELEVO_BASE: stderr is empty, want note"
	fail=1
fi
git update-ref refs/remotes/origin/main "$initial"

# 10. a deleted whole package directory gives ./...
reset_fixture
rm -rf a/sub
out="$work/stdout"
err="$work/stderr"
status=0
(sh "$script") > "$out" 2> "$err" || status=$?
if [ "$status" -ne 0 ]; then
	echo "FAIL: deleted package directory: exit $status, want 0"
	fail=1
fi
if [ "$(cat "$out")" != "./..." ]; then
	echo "FAIL: deleted package directory: got '$(cat "$out")', want './...'"
	fail=1
fi
if [ ! -s "$err" ]; then
	echo "FAIL: deleted package directory: stderr is empty, want note"
	fail=1
fi

# 11. a file renamed out of a maps both ends
reset_fixture
git mv a/extra.go f/extra.go
want_rename="example.test/fx/a
example.test/fx/b
example.test/fx/c
example.test/fx/d
example.test/fx/e
example.test/fx/f"
assert_stdout "a file renamed out of a maps both ends" "$want_rename"

# 12. a path with a space in its name maps
reset_fixture
mkdir -p "a/sub dir"
touch "a/sub dir/my file.txt"
assert_stdout "a path with a space in its name maps" "$want_a"

# --direct lists the changed packages without their importers
reset_fixture
echo "// edit" >> a/a.go
echo "// edit" >> f/f.go
assert_stdout "--direct on edits in a and f gives a and f" "example.test/fx/a
example.test/fx/f" --direct

reset_fixture
mkdir -p docs
echo "docs" > docs/x.md
assert_stdout "--direct on a docs-only change gives empty" "" --direct

reset_fixture
echo "// edit" >> go.mod
assert_stdout "--direct on a go.mod change gives ./..." "./..." --direct

reset_fixture
mkdir -p .github/workflows
echo "on: push" > .github/workflows/ci.yml
assert_stdout "a file in no package gives ./..." "./..."
assert_stdout "--direct on a file in no package gives empty" "" --direct

reset_fixture
rm -rf a/sub
assert_stdout "--direct on a deleted package directory gives its parent" "example.test/fx/a" --direct

reset_fixture
echo "package fixture" > a/testdata/fixture.go
assert_stdout "--direct on a .go file under testdata gives its package" "example.test/fx/a" --direct

# 13. bad flag gives exit 2
reset_fixture
status=0
(sh "$script" --invalid-flag) >/dev/null 2>&1 || status=$?
if [ "$status" -ne 2 ]; then
	echo "FAIL: bad flag exits $status, want 2"
	fail=1
fi

status=0
(sh "$script" --base) >/dev/null 2>&1 || status=$?
if [ "$status" -ne 2 ]; then
	echo "FAIL: missing base argument exits $status, want 2"
	fail=1
fi

if [ "$fail" -ne 0 ]; then
	exit 1
fi
