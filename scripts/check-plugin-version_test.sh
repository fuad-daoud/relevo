#!/bin/sh
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
# The cleanup must not decide the verdict: in dash (Ubuntu's /bin/sh) a
# failing EXIT trap's status replaces the script's own, so a teardown hiccup
# reads exactly like a test failure to `sh "$t" || exit 1` in the Makefile
# (#304).
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

# Build a fake repo root with both manifests at the given versions.
# $2 (marketplace.json) defaults to $1 so every one-argument caller keeps
# agreeing across both manifests.
stage() {
	v2=${2:-$1}
	rm -rf "$work/repo"
	mkdir -p "$work/repo/claude-plugin/.claude-plugin" "$work/repo/.claude-plugin"
	printf '{"name": "relevo", "version": "%s"}\n' "$1" > "$work/repo/claude-plugin/.claude-plugin/plugin.json"
	printf '{"name": "relevo", "plugins": [{"name": "relevo", "version": "%s"}]}\n' "$v2" > "$work/repo/.claude-plugin/marketplace.json"
	cp "$here/check-plugin-version.sh" "$work/repo/"
}

fail=0
check() { # description, expected-exit, args...
	desc=$1; want=$2; shift 2
	if (cd "$work/repo" && sh check-plugin-version.sh "$@" >/dev/null 2>&1); then got=0; else got=1; fi
	if [ "$got" -ne "$want" ]; then
		echo "FAIL: $desc (exit $got, want $want)"; fail=1
	fi
}

stage 1.2.3 1.2.3
check "agreeing manifests, no tag" 0
check "agreeing manifests, matching tag" 0 v1.2.3
check "agreeing manifests, mismatched tag" 1 v9.9.9

stage 1.2.3 4.5.6
check "disagreeing manifests, no tag" 1
check "disagreeing manifests, matching tag" 1 v1.2.3

stage 1.2.3 9.9.9
check "marketplace.json version disagrees" 1

stage 1.2.3 1.2.3
(cd "$work/repo" && rm .claude-plugin/marketplace.json)
check "missing marketplace manifest" 1

[ "$fail" -eq 0 ] && echo "check-plugin-version: ok"
exit "$fail"
