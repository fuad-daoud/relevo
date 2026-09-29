#!/bin/sh
# scripts/ci-code-changed_test.sh -- tests for scripts/ci-code-changed.sh
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
script="$here/ci-code-changed.sh"

fail=0
check() { # description, expected_stdout, input_string
	desc=$1; want=$2; input=$3
	if ! got=$(printf '%s' "$input" | sh "$script"); then
		echo "FAIL: $desc (script exited with non-zero status $?)"
		fail=1
		return
	fi
	if [ "$got" != "$want" ]; then
		echo "FAIL: $desc (got '$got', want '$want')"
		fail=1
	fi
}

# 1. empty input -> true
check "empty input" "true" ""

# 2. only blank lines -> true
check "only blank lines" "true" "
   
	
"

# 3. docs/plans/a.md -> false
check "single docs/plans file" "false" "docs/plans/a.md
"

# 4. docs/plans/a.md, docs/specs/b.md, docs/superpowers/plans/c.md -> false
check "docs/plans, docs/specs, docs/superpowers" "false" "docs/plans/a.md
docs/specs/b.md
docs/superpowers/plans/c.md
"

# 5. docs/plans/a.md, cmd/relevo/main.go -> true
check "docs file and code file" "true" "docs/plans/a.md
cmd/relevo/main.go
"

# 6. README.md -> true
check "README.md" "true" "README.md
"

# 7. docs/design.md -> true
check "docs/design.md" "true" "docs/design.md
"

# 8. internal/harness/agents/architect.claude.md -> true
check "internal/harness/agents/architect.claude.md" "true" "internal/harness/agents/architect.claude.md
"

# 9. .github/workflows/ci.yml -> true
check ".github/workflows/ci.yml" "true" ".github/workflows/ci.yml
"

# 10. x/docs/plans/a.md -> true (prefix match only)
check "x/docs/plans/a.md (prefix match only)" "true" "x/docs/plans/a.md
"

# 11. docs/plansx/a.md -> true (the trailing slash is part of the prefix)
check "docs/plansx/a.md (trailing slash required)" "true" "docs/plansx/a.md
"

[ "$fail" -eq 0 ] && echo "ci-code-changed: ok"
exit "$fail"
