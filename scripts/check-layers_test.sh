#!/bin/sh
# scripts/check-layers_test.sh -- tests for scripts/check-layers.sh
set -eu

if ! command -v go >/dev/null 2>&1; then
	echo "go not installed; skipping check-layers tests"
	exit 0
fi

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work" 2>/dev/null || :' EXIT
export GOTOOLCHAIN=local GOFLAGS='' GOPROXY=off

fx="$work/fixture"
mkdir -p "$fx/scripts" "$fx/docs" "$fx/internal/text" "$fx/internal/store" "$fx/internal/config" "$fx/internal/e2e"
cp "$here/check-layers.sh" "$fx/scripts/"
# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
CDPATH= cd -- "$fx"
printf 'module example.test/fx\n\ngo 1.22\n' > go.mod
cat > docs/architecture.md << 'EOF'
## Layers

| Band | Packages | What they own |
| --- | --- | --- |
| Foundations | `text` | helpers |
| Records | `store` | records |
| Services | `config` | services |
| Engine | | |
| Surfaces | | |
EOF
printf 'package text\n\nfunc T() string { return "t" }\n' > internal/text/text.go
printf 'package e2e\n\nfunc E() {}\n' > internal/e2e/e2e.go
cat > internal/store/store.go << 'EOF'
package store

import "example.test/fx/internal/text"

func S() string { return text.T() }
EOF
cat > internal/config/config.go << 'EOF'
package config

import "example.test/fx/internal/store"

func C() string { return store.S() }
EOF

fail=0
# expect NAME WANT_EXIT WANT_TEXT runs the check over the fixture as it stands.
expect() {
	set +e
	out=$(sh scripts/check-layers.sh 2>&1)
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

expect "imports downward" 0 "check-layers: ok"

# A test file may reach upward: only production imports are held.
cat > internal/store/store_test.go << 'EOF'
package store_test

import (
	"testing"

	"example.test/fx/internal/config"
)

func TestS(t *testing.T) { _ = config.C() }
EOF
expect "test file imports upward" 0 "check-layers: ok"
rm internal/store/store_test.go

cp internal/store/store.go "$work/store.go"
cat > internal/store/store.go << 'EOF'
package store

import "example.test/fx/internal/config"

func S() string { return config.C() }
EOF
# config imports store, so this is also a cycle; give config its own body.
printf 'package config\n\nfunc C() string { return "c" }\n' > internal/config/config.go
expect "imports upward" 1 "store (Records) imports config (Services), a higher band"
cp "$work/store.go" internal/store/store.go

cat > internal/text/text.go << 'EOF'
package text

import "example.test/fx/internal/e2e"

func T() string { e2e.E(); return "t" }
EOF
expect "imports a test-only package" 1 "text: imports e2e, which serves tests only"

cat > internal/text/text.go << 'EOF'
package text

import "example.test/fx/internal/config"

func T() string { return config.C() }
EOF
expect "foundation imports relevo" 1 "text: a Foundations package imports config"
printf 'package text\n\nfunc T() string { return "t" }\n' > internal/text/text.go

mkdir internal/fresh
printf 'package fresh\n\nfunc F() {}\n' > internal/fresh/fresh.go
expect "package in no band" 1 "fresh: not in a band of the Layers table"

exit "$fail"
