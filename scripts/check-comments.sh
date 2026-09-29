#!/bin/sh
# scripts/check-comments.sh -- fails on a comment that cites history (#NNN or §).
#
# The rules are in CLAUDE.md's "Code style": a comment says why, and never
# where a change came from -- git and the issues hold that history. This is a
# text match over tracked .go files, not a Go parse. A line whose only // sits
# inside a string literal (a URL, say) is therefore a known false positive; the
# guard accepts it rather than growing a parser.
#
# Usage: check-comments.sh [--list]
#   --list  print the distinct failing paths, nothing else (seeds the allow-list)
set -eu

# The allow-list sits next to this script, at its real repo path.
# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
allow="$here/check-comments.allow"

list=0
case "${1:-}" in
	--list) list=1 ;;
	"") ;;
	*)
		echo "usage: check-comments.sh [--list]" >&2
		exit 2
		;;
esac

work=$(mktemp -d)
# The cleanup must not decide the verdict: in dash a failing EXIT trap's status
# replaces the script's own.
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

# Every tracked Go file, minus the allow-list. A listed path that no longer
# exists is skipped silently, so a round that deletes a file never has to touch
# the list.
git ls-files -- '*.go' > "$work/all"
: > "$work/skip"
if [ -f "$allow" ]; then
	grep -v '^[[:space:]]*$' "$allow" > "$work/skip" || :
fi
grep -vxF -f "$work/skip" "$work/all" > "$work/files" || :

# A violation is the text after the first // matching #NNN or a literal §.
: > "$work/hits"
while IFS= read -r f; do
	[ -f "$f" ] || continue
	grep -nE '//.*(#[0-9]+|§)' -- "$f" | while IFS=: read -r line _rest; do
		printf '%s:%s: comment cites history (#NNN or §)\n' "$f" "$line"
	done >> "$work/hits"
done < "$work/files"

if [ ! -s "$work/hits" ]; then
	echo "check-comments: ok"
	exit 0
fi

if [ "$list" -eq 1 ]; then
	cut -d: -f1 "$work/hits" | sort -u
else
	cat "$work/hits"
fi
exit 1
