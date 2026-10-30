#!/bin/sh
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
# The cleanup must not decide the verdict: in dash a failing EXIT trap's status
# replaces the script's own, so a teardown hiccup reads exactly like a test
# failure to `sh "$t" || exit 1` in the Makefile (#304).
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

agents=$here/../internal/harness/agents

# A temp copy of the tree layout agents-shipped.sh --check reads: a root holding
# internal/harness/agents/ with the definitions and the committed index. The
# temp root is not a git repository, which also proves --check reads no history.
root=$work/repo
mkdir -p "$root/internal/harness/agents" "$root/internal/shipped"
cp "$agents"/*.md "$agents"/*.toml "$root/internal/harness/agents/"
cp "$here/../internal/shipped/agents.sha256" "$root/internal/shipped/"

fail=0
check() { # description, expected-exit, root
	desc=$1
	want=$2
	r=$3
	if sh "$here/agents-shipped.sh" --check --root "$r" >/dev/null 2>&1; then
		got=0
	else
		got=1
	fi
	if [ "$got" -ne "$want" ]; then
		echo "FAIL: $desc (exit $got, want $want)"
		fail=1
	fi
}

check "the committed definitions pass" 0 "$root"

# One appended byte makes a definition's current hash unknown to the index, so
# --check must name it and fail (#371 round 3 §7 step 5).
printf 'x' >> "$root/internal/harness/agents/architect.claude.md"
check "an appended byte fails" 1 "$root"

# Any change to the working-tree bytes -- trailing whitespace included -- makes
# the index stale, because it records the file's raw hash as well.
printf '   \n\n' >> "$root/internal/harness/agents/architect.claude.md"
check "appended whitespace fails" 1 "$root"

# A missing index is a failure, not a silent pass.
cp "$agents/architect.claude.md" "$root/internal/harness/agents/architect.claude.md"
rm "$root/internal/shipped/agents.sha256"
check "a missing index fails" 1 "$root"

# No mode is a usage error.
if sh "$here/agents-shipped.sh" --root "$root" >/dev/null 2>&1; then
	echo "FAIL: no mode must exit non-zero"
	fail=1
fi

# The real tree still passes.
check "the real working tree passes" 0 "$here/.."

if [ "$fail" -eq 0 ]; then
	echo "agents-shipped: ok"
fi
exit "$fail"
