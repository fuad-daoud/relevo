#!/bin/sh
# serviceunit-dist.sh -- keep dist/ in step with the unit sources.
#
# internal/serviceunit/units/ is the source of truth because go:embed cannot
# reach dist/; dist/ holds committed copies for readers without the binary.
#
# Modes:
#   --write   copy the two sources into dist/.
#   --check   fail unless dist/ holds byte-identical copies.
#
# --root DIR points either mode at a checkout other than this script's parent.
set -eu

usage() {
	echo "usage: serviceunit-dist.sh (--write | --check) [--root DIR]" >&2
}

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
root=$here/..

mode=
while [ $# -gt 0 ]; do
	case $1 in
	--write)
		mode='write'
		;;
	--check)
		mode='check'
		;;
	--root)
		shift
		if [ $# -eq 0 ]; then
			echo "serviceunit-dist: --root needs a directory" >&2
			exit 1
		fi
		root=$1
		;;
	--root=*)
		root=${1#--root=}
		;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "serviceunit-dist: unknown argument: $1" >&2
		usage
		exit 1
		;;
	esac
	shift
done

if [ -z "$mode" ]; then
	usage
	exit 1
fi

root=${root%/}
units_dir=$root/internal/serviceunit/units
dist_dir=$root/dist

fail=0
for base in relevo.service com.github.fuad-daoud.relevo.plist.in; do
	if [ "$mode" = 'write' ]; then
		mkdir -p "$dist_dir"
		cp "$units_dir/$base" "$dist_dir/$base"
	elif ! cmp -s "$units_dir/$base" "$dist_dir/$base"; then
		echo "serviceunit-dist: dist/$base differs from internal/serviceunit/units/$base; run sh scripts/serviceunit-dist.sh --write" >&2
		fail=1
	fi
done

[ "$fail" -eq 0 ] || exit 1
echo "serviceunit-dist: ok"
