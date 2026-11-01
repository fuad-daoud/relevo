#!/bin/sh
# scripts/dev-agents_test.sh -- tests for scripts/dev-agents.sh, against a fake
# relevo that records its calls.
set -eu

if ! command -v jq >/dev/null 2>&1; then
	echo "jq not installed; skipping dev-agents tests"
	exit 0
fi

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

fx="$work/fixture"
mkdir -p "$fx/scripts" "$fx/dev/agents" "$work/bin"
cp "$here/dev-agents.sh" "$fx/scripts/"
cp "$here/../dev/agents/"*.md "$fx/dev/agents/"

# The fake answers `config get actors.<role>.candidates` with a list naming the
# role, and records every call one argument per line, calls separated by ---.
cat > "$work/bin/relevo" << EOF
#!/bin/sh
for a in "\$@"; do printf '%s\n' "\$a"; done >> "$work/calls"
echo --- >> "$work/calls"
if [ "\$1 \$2" = "config get" ]; then
	role=\${3#actors.}
	role=\${role%.candidates}
	printf '[ "%s-a",\n  "%s-b" ]\n' "\$role" "\$role"
fi
EOF
chmod +x "$work/bin/relevo"
PATH="$work/bin:$PATH"
export PATH

fail=0
check() {
	if [ "$2" = "$3" ]; then
		echo "ok   $1"
	else
		echo "FAIL $1: got '$2', want '$3'"
		fail=1
	fi
}

# value NAME prints the argument after NAME in the recorded calls.
value() {
	awk -v k="$1" 'found { print; exit } $0 == k { found = 1 }' "$work/calls"
}

: > "$work/calls"
sh "$fx/scripts/dev-agents.sh" > /dev/null

for a in relevo-builder relevo-reviewer relevo-lite-planner relevo-planner; do
	got=$(value "agents.$a" | jq -r .source)
	want=$(cat "$fx/dev/agents/$a.md")
	check "$a source passed whole" "$got" "$want"
	check "$a rendered" "$(grep -A1 -x -- --agent "$work/calls" | grep -cx -- "$a")" "1"
done

check "builder takes builder's candidates" \
	"$(value actors.relevo-builder | jq -c .candidates)" '["builder-a","builder-b"]'
check "builder declares no outputs" "$(value actors.relevo-builder | jq -c .outputs)" "null"
check "reviewer takes reviewer's candidates" \
	"$(value actors.relevo-reviewer | jq -c .candidates)" '["reviewer-a","reviewer-b"]'
check "reviewer declares the verdict" \
	"$(value actors.relevo-reviewer | jq -c '.outputs.verdict["one-of"]')" '["pass","changes"]'
check "planner takes lite-planner's candidates" \
	"$(value actors.relevo-planner | jq -c .candidates)" '["lite-planner-a","lite-planner-b"]'
check "planner outputs a design" "$(value actors.relevo-planner | jq -c .outputs)" '{"design":"artifact"}'

: > "$work/calls"
sh "$fx/scripts/dev-agents.sh" --dry-run > "$work/dry"
check "dry run writes nothing" "$(grep -c '^set$' "$work/calls" || :)" "0"
check "dry run prints every write" "$(grep -c '^relevo config set' "$work/dry")" "8"

if sh "$fx/scripts/dev-agents.sh" --bogus > /dev/null 2>&1; then
	check "unknown flag refused" "exit 0" "exit 2"
else
	check "unknown flag refused" "refused" "refused"
fi

exit "$fail"
