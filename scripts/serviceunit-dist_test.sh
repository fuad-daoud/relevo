#!/bin/sh
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
# The cleanup must not decide the verdict: in dash a failing EXIT trap's status
# replaces the script's own.
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

root=$work/repo
mkdir -p "$root/internal/serviceunit/units" "$root/dist"
cp "$here"/../internal/serviceunit/units/relevo.service "$here"/../internal/serviceunit/units/com.github.fuad-daoud.relevo.plist.in "$root/internal/serviceunit/units/"
cp "$here"/../dist/relevo.service "$here"/../dist/com.github.fuad-daoud.relevo.plist.in "$root/dist/"

fail=0
check() { # description, expected-exit, root
	if sh "$here/serviceunit-dist.sh" --check --root "$3" >/dev/null 2>&1; then
		got=0
	else
		got=1
	fi
	if [ "$got" -ne "$2" ]; then
		echo "FAIL: $1 (exit $got, want $2)"
		fail=1
	fi
}

check "matching copies pass" 0 "$root"

printf 'x' >> "$root/dist/relevo.service"
check "a doctored service copy fails" 1 "$root"

sh "$here/serviceunit-dist.sh" --write --root "$root" >/dev/null
check "--write repairs a doctored copy" 0 "$root"

printf 'x' >> "$root/dist/com.github.fuad-daoud.relevo.plist.in"
check "a doctored plist copy fails" 1 "$root"

rm "$root/dist/relevo.service"
check "a missing copy fails" 1 "$root"

check "the real working tree passes" 0 "$here/.."

if [ "$fail" -eq 0 ]; then
	echo "serviceunit-dist: ok"
fi
exit "$fail"
