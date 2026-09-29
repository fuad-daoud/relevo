#!/bin/sh
# scripts/check-name_test.sh -- tests for the relay-name guard (#292 §4).
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

# stage starts an empty repository with the guard at its real path. The guard
# uses git grep, so every case needs a real, tracked tree, and it exempts
# scripts/check-name.sh -- the one file that must spell the pattern itself.
stage() {
	rm -rf "$work/repo"
	mkdir -p "$work/repo/scripts"
	cp "$here/check-name.sh" "$work/repo/scripts/"
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
	)
}

# write <path> writes stdin below the staged repo, creating parents.
write() {
	mkdir -p "$work/repo/$(dirname -- "$1")"
	cat > "$work/repo/$1"
}

commit() {
	(
		cd "$work/repo"
		git add -A
		git -c user.name=test -c user.email=test@example.com -c commit.gpgsign=false commit -q -m test
	)
}

fail=0
check() { # description, expected exit
	desc=$1; want=$2
	if (cd "$work/repo" && sh scripts/check-name.sh >/dev/null 2>&1); then got=0; else got=1; fi
	if [ "$got" -ne "$want" ]; then
		echo "FAIL: $desc (exit $got, want $want)"; fail=1
	fi
}

# A Go file with a bare old marker fails.
stage
write main.go <<'EOF'
package main

var s = "relay-exit:"
EOF
commit
check "a relay token in a Go file fails" 1

# The same line carrying the marker passes.
stage
write main.go <<'EOF'
package main

var s = "relay-exit:" // name-guard: legacy
EOF
commit
check "a marked line passes" 0

# The verb forms are not the old name and must not match.
stage
write ok.go <<'EOF'
package main

var a = "relayd"
var b = "relaying"
var c = "Relayed"
EOF
commit
check "relayd, relaying and Relayed pass" 0

# The site hostname is kept (spec §1 item 4).
stage
write README.md <<'EOF'
# relevo

Site: https://relay-site.fuad-daoud.com
EOF
commit
check "the site hostname passes" 0

# Everything between the Markdown markers is allowed ...
stage
write README.md <<'EOF'
# relevo

<!-- name-guard: off -->
relay was renamed relevo in v0.12.0.
<!-- name-guard: on -->
EOF
commit
check "a Markdown off/on block passes" 0

# ... and the same text outside the block is not.
stage
write README.md <<'EOF'
# relevo

relay was renamed relevo in v0.12.0.
EOF
commit
check "the same text outside the block fails" 1

[ "$fail" -eq 0 ] && echo "check-name: ok"
exit "$fail"
