#!/bin/sh
# scripts/check-layers.sh -- holds every package to the import rule in
# docs/architecture.md: each package sits in one band of the Layers table,
# imports only packages in its own band or a lower one, and a Foundations
# package imports no relevo package. Test files may import anything.
#
# The table is the source: a new package fails here until it is placed in a
# band, which keeps the document and the tree from drifting apart.
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
CDPATH= cd -- "$here/.."
doc=docs/architecture.md

# The packages that serve tests only, named under the table; production code
# must not import them.
test_only="db/dbtest e2e"

work=$(mktemp -d)
# The cleanup must not decide the verdict: in dash a failing EXIT trap's status
# replaces the script's own.
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

# "<package> <rank>" for every backquoted name in a band row, rank 0 at the
# bottom.
awk -F'|' '
	{
		name = $2
		gsub(/^ +| +$/, "", name)
		rank = -1
		if (name == "Foundations") rank = 0
		if (name == "Records") rank = 1
		if (name == "Services") rank = 2
		if (name == "Engine") rank = 3
		if (name == "Surfaces") rank = 4
		if (rank < 0) next
		pkgs = $3
		while (match(pkgs, /`[^`]+`/)) {
			print substr(pkgs, RSTART + 1, RLENGTH - 2), rank
			pkgs = substr(pkgs, RSTART + RLENGTH)
		}
	}
' "$doc" > "$work/bands"
if [ ! -s "$work/bands" ]; then
	echo "check-layers: no band table in $doc" >&2
	exit 1
fi
for p in $test_only; do
	echo "$p test"
done >> "$work/bands"

mod=$(go list -m)
go list -f '{{.ImportPath}} {{join .Imports " "}}' ./... > "$work/imports"

awk -v mod="$mod/" -v bandfile="$work/bands" '
	function short(p) {
		p = substr(p, length(mod) + 1)
		sub(/^internal\//, "", p)
		return p
	}
	BEGIN {
		split("Foundations Records Services Engine Surfaces", label, " ")
		while ((getline line < bandfile) > 0) {
			split(line, f, " ")
			band[f[1]] = f[2]
		}
	}
	{
		p = short($1)
		if (!(p in band)) {
			print p ": not in a band of the Layers table in docs/architecture.md"
			bad = 1
			next
		}
		if (band[p] == "test") next
		for (i = 2; i <= NF; i++) {
			if (index($i, mod) != 1) continue
			q = short($i)
			# An unlisted import fails on its own line.
			if (!(q in band)) continue
			if (band[q] == "test") {
				print p ": imports " q ", which serves tests only"
				bad = 1
			} else if (band[p] == 0) {
				print p ": a Foundations package imports " q "; foundations import no relevo package"
				bad = 1
			} else if (band[q] > band[p]) {
				print p " (" label[band[p] + 1] ") imports " q " (" label[band[q] + 1] "), a higher band"
				bad = 1
			}
		}
	}
	END { exit bad }
' "$work/imports" || {
	echo "check-layers: a package imports only its own band or lower (docs/architecture.md)"
	exit 1
}
echo "check-layers: ok"
