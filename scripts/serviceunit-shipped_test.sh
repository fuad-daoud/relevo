#!/bin/sh
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
# The cleanup must not decide the verdict: in dash a failing EXIT trap's status
# replaces the script's own.
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

units=$here/../internal/serviceunit/units

# A temp copy of the layout --check reads. The temp root is not a git
# repository, which also proves --check reads no history.
root=$work/repo
mkdir -p "$root/internal/serviceunit/units"
cp "$units"/relevo.service "$units"/com.github.fuad-daoud.relevo.plist.in "$units"/shipped.sha256 "$root/internal/serviceunit/units/"

fail=0
check() { # description, expected-exit, root
	desc=$1
	want=$2
	r=$3
	if sh "$here/serviceunit-shipped.sh" --check --root "$r" >/dev/null 2>&1; then
		got=0
	else
		got=1
	fi
	if [ "$got" -ne "$want" ]; then
		echo "FAIL: $desc (exit $got, want $want)"
		fail=1
	fi
}

check "the committed units pass" 0 "$root"

# One appended byte makes a unit's current hash unknown to the index.
printf 'x' >> "$root/internal/serviceunit/units/relevo.service"
check "an appended byte fails" 1 "$root"

# The plist template is checked too.
cp "$units/relevo.service" "$root/internal/serviceunit/units/relevo.service"
printf 'x' >> "$root/internal/serviceunit/units/com.github.fuad-daoud.relevo.plist.in"
check "an edited plist template fails" 1 "$root"

# A missing index is a failure, not a silent pass.
cp "$units/com.github.fuad-daoud.relevo.plist.in" "$root/internal/serviceunit/units/com.github.fuad-daoud.relevo.plist.in"
rm "$root/internal/serviceunit/units/shipped.sha256"
check "a missing index fails" 1 "$root"

# No mode is a usage error.
if sh "$here/serviceunit-shipped.sh" --root "$root" >/dev/null 2>&1; then
	echo "FAIL: no mode must exit non-zero"
	fail=1
fi

# The real tree still passes.
check "the real working tree passes" 0 "$here/.."

if [ "$fail" -eq 0 ]; then
	echo "serviceunit-shipped: ok"
fi
exit "$fail"
