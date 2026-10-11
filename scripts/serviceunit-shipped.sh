#!/bin/sh
# serviceunit-shipped.sh -- record and verify the sha256 of every daemon unit
# source relevo has ever shipped.
#
# units/shipped.sha256 lets a later relevo recognise an installed unit as an
# unmodified copy of an older release, not a user edit, so the daemon may
# refresh it (internal/serviceunit/shipped.go, ShippedUnitBefore). The plist is
# indexed as its template, placeholders intact; an installed plist is
# canonicalised before it is hashed.
#
# Modes:
#   --write   regenerate internal/serviceunit/units/shipped.sha256 from the
#             full git history of both sources plus the working tree, unioned
#             with the lines already committed. A line, once written, is never
#             dropped by a later --write.
#   --check   verify the working-tree sources' hashes are all present in
#             shipped.sha256. It reads no git history.
#
# --root DIR points either mode at a checkout other than this script's parent
# (the test uses it to exercise a temp tree). Default: the script's parent.
set -eu

usage() {
	echo "usage: serviceunit-shipped.sh (--write | --check) [--root DIR]" >&2
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
			echo "serviceunit-shipped: --root needs a directory" >&2
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
		echo "serviceunit-shipped: unknown argument: $1" >&2
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
shipped=$units_dir/shipped.sha256
sources='relevo.service com.github.fuad-daoud.relevo.plist.in'

# hash_stdin prints the lowercase hex sha256 of stdin: sha256sum on Linux,
# shasum -a 256 (macOS) otherwise.
hash_stdin() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum | cut -d' ' -f1
	else
		shasum -a 256 | cut -d' ' -f1
	fi
}

# emit_history appends the sha of every blob the followed path $1 has been in
# git history, under the current basename $2, to file $3.
emit_history() { # relpath, base, out
	commit=
	git -C "$root" log --follow --format=%H --name-only -- "$1" |
		while IFS= read -r line; do
			[ -n "$line" ] || continue
			case $line in
			*[!0-9a-f]*)
				# A path at the commit seen just above, from --name-only.
				[ -n "$commit" ] || continue
				git -C "$root" cat-file -e "$commit:$line" 2>/dev/null || continue
				printf '%s  %s\n' "$(git -C "$root" show "$commit:$line" | hash_stdin)" "$2" >> "$3"
				;;
			*)
				if [ ${#line} -eq 40 ]; then
					commit=$line
				fi
				;;
			esac
		done
}

write_mode() {
	[ -d "$units_dir" ] || {
		echo "serviceunit-shipped: no such directory: $units_dir" >&2
		exit 1
	}
	command -v git >/dev/null 2>&1 || {
		echo "serviceunit-shipped: git is required for --write" >&2
		exit 1
	}

	union=$(mktemp "$units_dir/.shipped.XXXXXX")
	out=$(mktemp "$units_dir/.shipped.XXXXXX")
	trap 'rm -f "$union" "$out"' EXIT HUP INT TERM

	# Union with what is already recorded: append-only in spirit.
	if [ -f "$shipped" ]; then
		cat "$shipped" >> "$union"
	fi
	for base in $sources; do
		f=$units_dir/$base
		[ -f "$f" ] || continue
		printf '%s  %s\n' "$(hash_stdin < "$f")" "$base" >> "$union"
		emit_history "${f#"$root/"}" "$base" "$union"
		# Before the move into units/ the sources lived in dist/.
		emit_history "dist/$base" "$base" "$union"
	done

	LC_ALL=C sort -u "$union" > "$out"
	mv "$out" "$shipped"
	rm -f "$union"
	trap - EXIT HUP INT TERM
	echo "serviceunit-shipped: wrote $(wc -l < "$shipped" | tr -d ' ') hashes to $shipped"
}

check_mode() {
	if [ ! -f "$shipped" ]; then
		echo "serviceunit-shipped: missing $shipped; run sh scripts/serviceunit-shipped.sh --write" >&2
		exit 1
	fi

	fail=0
	for base in $sources; do
		f=$units_dir/$base
		[ -f "$f" ] || continue
		sha=$(hash_stdin < "$f")
		line="$sha  $base"
		grep -qxF "$line" "$shipped" && continue
		echo "serviceunit-shipped: $base: current sha $sha is not recorded in shipped.sha256; run sh scripts/serviceunit-shipped.sh --write" >&2
		fail=1
	done

	[ "$fail" -eq 0 ] || exit 1
	echo "serviceunit-shipped: ok"
}

case $mode in
write) write_mode ;;
check) check_mode ;;
esac
