#!/bin/sh
# scripts/check-board-theme.sh -- the board page-source guard (#795, S3).
#
# The page palette has exactly one home: the Go table in internal/board/theme.go.
# The wrapper under board/src must therefore carry no colour of its own and no
# runtime network of its own, and the Mermaid recolour must stay present. Three
# rules over the tracked sources:
#
#   a) no hex colour literal -- the palette is Go's, not the page's;
#   b) no https://            -- no new runtime network;
#   c) recolor() carries at least four strokeColor and four backgroundColor
#      assignments, so deleting the recolour fails the check rather than
#      passing silently.
#
# Rule c applies only once board/src/mermaid.js exists (S3.2); the S3.1 tree
# has no such file yet, and the guard must pass on the tree that introduces it.
set -eu

# The paths sit next to this script's real repo path, not the caller's cwd.
# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
# shellcheck disable=SC1007
root=$(CDPATH= cd -- "$here/.." && pwd)

work=$(mktemp -d)
# The cleanup must not decide the verdict: in dash a failing EXIT trap's status
# replaces the script's own.
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

files=$(git -C "$root" ls-files -- 'board/src/**')
if [ -z "$files" ]; then
	echo "check-board-theme: no tracked files under board/src" >&2
	exit 1
fi

: > "$work/hex"
: > "$work/net"
cd "$root"
printf '%s\n' "$files" | while IFS= read -r f; do
	grep -nE '#[0-9a-fA-F]{3,8}\b' "$f" >> "$work/hex" || :
	grep -n 'https://' "$f" >> "$work/net" || :
done

fail=0

# a) no hex colour literal.
if [ -s "$work/hex" ]; then
	cat "$work/hex"
	echo "check-board-theme: rule a: hex colour literal in board/src (the palette is Go's)"
	fail=1
fi

# b) no runtime network.
if [ -s "$work/net" ]; then
	cat "$work/net"
	echo "check-board-theme: rule b: https:// in board/src (no new runtime network)"
	fail=1
fi

# c) the recolour stays. The region is the body of the recolor function, from
#    its definition line to the next line that is exactly a closing brace at
#    column zero; every body line is indented, so the first such line is the
#    function's own end.
mermaid="$root/board/src/mermaid.js"
if [ -f "$mermaid" ]; then
	awk '/^(export )?function recolor/ { inbody = 1 }
	     inbody { print }
	     inbody && /^}/ { inbody = 0 }' "$mermaid" > "$work/recolor"
	stroke=$(grep -c 'strokeColor' "$work/recolor" || :)
	bg=$(grep -c 'backgroundColor' "$work/recolor" || :)
	if [ "$stroke" -lt 4 ] || [ "$bg" -lt 4 ]; then
		echo "check-board-theme: rule c: recolor() carries $stroke strokeColor and $bg backgroundColor assignments (need 4 and 4)"
		fail=1
	fi
fi

if [ "$fail" -ne 0 ]; then
	exit 1
fi
echo "check-board-theme: ok"