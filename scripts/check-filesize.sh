#!/bin/sh
# scripts/check-filesize.sh -- fails on a non-test .go file over 600 lines.
#
# The target in CLAUDE.md's "Code style" is around 500 lines; 600 is the
# enforced ceiling, with slack for one long table. Test files are not counted:
# tests are allowed to be as large as the code they pin.
#
# Usage: check-filesize.sh [--list]
#   --list  print the distinct failing paths, nothing else (seeds the allow-list)
set -eu

# The allow-list sits next to this script, at its real repo path.
# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
allow="$here/check-filesize.allow"

list=0
case "${1:-}" in
	--list) list=1 ;;
	"") ;;
	*)
		echo "usage: check-filesize.sh [--list]" >&2
		exit 2
		;;
esac

max=600

work=$(mktemp -d)
# The cleanup must not decide the verdict: in dash a failing EXIT trap's status
# replaces the script's own.
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

# Every tracked non-test Go file, minus the allow-list. A listed path that no
# longer exists is skipped silently, so a round that deletes a file never has to
# touch the list.
git ls-files -- '*.go' ':!:*_test.go' > "$work/all"
: > "$work/skip"
if [ -f "$allow" ]; then
	grep -v '^[[:space:]]*$' "$allow" > "$work/skip" || :
fi
grep -vxF -f "$work/skip" "$work/all" > "$work/files" || :

: > "$work/hits"
while IFS= read -r f; do
	[ -f "$f" ] || continue
	n=$(wc -l < "$f" | tr -d ' ')
	if [ "$n" -gt "$max" ]; then
		printf '%s: %s lines (max %s)\n' "$f" "$n" "$max"
	fi
done < "$work/files" > "$work/hits"

if [ ! -s "$work/hits" ]; then
	echo "check-filesize: ok"
	exit 0
fi

if [ "$list" -eq 1 ]; then
	cut -d: -f1 "$work/hits" | sort -u
else
	cat "$work/hits"
fi
exit 1
