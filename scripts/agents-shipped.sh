#!/bin/sh
# agents-shipped.sh -- record and verify the sha256 of every agent definition
# relevo has ever shipped (#371 round 3).
#
# shipped.sha256 lets a later relevo recognise an on-disk definition as an
# unmodified copy of an older release -- not a user edit -- so an existing
# install auto-updates instead of being kept forever
# (internal/shipped, Before).
#
# Modes:
#   --write   regenerate internal/shipped/agents.sha256 from the full
#             git history of every definition plus the working tree, unioned
#             with the lines already committed. Append-only in spirit: a line,
#             once written, is never dropped by a later --write.
#   --check   verify the working-tree definitions' hashes are all present in
#             shipped.sha256. It reads no git history, so it works in any
#             checkout.
#
# --root DIR points either mode at a checkout other than this script's parent
# (the test uses it to exercise a temp tree). Default: the script's parent.
#
# Each file is hashed twice: its raw bytes, and its bytes with trailing ASCII
# whitespace trimmed and one newline appended. An installed copy written by a
# past relevo that normalised line endings therefore matches either way.
set -eu

usage() {
	echo "usage: agents-shipped.sh (--write | --check) [--root DIR]" >&2
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
			echo "agents-shipped: --root needs a directory" >&2
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
		echo "agents-shipped: unknown argument: $1" >&2
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
agents_dir=$root/internal/harness/agents
shipped=$root/internal/shipped/agents.sha256

# hash_stdin prints the lowercase hex sha256 of stdin: sha256sum on Linux,
# shasum -a 256 (macOS) otherwise.
hash_stdin() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum | cut -d' ' -f1
	else
		shasum -a 256 | cut -d' ' -f1
	fi
}

# normalise prints stdin with trailing ASCII whitespace trimmed from the whole
# stream and one newline appended.
normalise() {
	awk '
		{ line[n++] = $0 }
		END {
			s = ""
			for (i = 0; i < n; i++) s = s line[i] "\n"
			sub(/[ \t\r\n]+$/, "", s)
			printf "%s\n", s
		}'
}

# emit_file_hashes appends "<sha>  <base>" lines for the raw and, when it
# differs, the normalised form of file $1 to file $3.
emit_file_hashes() { # file, base, out
	raw=$(hash_stdin < "$1")
	norm=$(normalise < "$1" | hash_stdin)
	printf '%s  %s\n' "$raw" "$2" >> "$3"
	if [ "$norm" != "$raw" ]; then
		printf '%s  %s\n' "$norm" "$2" >> "$3"
	fi
}

# emit_blob_hashes appends the raw and normalised sha of blob $2 at commit $1,
# under the current basename $3, to file $4.
emit_blob_hashes() { # commit, path, base, out
	git -C "$root" cat-file -e "$1:$2" 2>/dev/null || return 0
	raw=$(git -C "$root" show "$1:$2" | hash_stdin)
	norm=$(git -C "$root" show "$1:$2" | normalise | hash_stdin)
	printf '%s  %s\n' "$raw" "$3" >> "$4"
	if [ "$norm" != "$raw" ]; then
		printf '%s  %s\n' "$norm" "$3" >> "$4"
	fi
}

# emit_history appends the hashes of every blob the followed path $1 has been
# in git history, under the current basename $2, to file $3.
emit_history() { # relpath, base, out
	commit=
	git -C "$root" log --follow --format=%H --name-only -- "$1" |
		while IFS= read -r line; do
			[ -n "$line" ] || continue
			case $line in
			*[!0-9a-f]*)
				# A path at the commit seen just above, from --name-only.
				[ -n "$commit" ] || continue
				case $line in
				internal/harness/agents/*) emit_blob_hashes "$commit" "$line" "$2" "$3" ;;
				esac
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
	[ -d "$agents_dir" ] || {
		echo "agents-shipped: no such directory: $agents_dir" >&2
		exit 1
	}
	command -v git >/dev/null 2>&1 || {
		echo "agents-shipped: git is required for --write" >&2
		exit 1
	}

	union=$(mktemp "$agents_dir/.shipped.XXXXXX")
	out=$(mktemp "$agents_dir/.shipped.XXXXXX")
	trap 'rm -f "$union" "$out"' EXIT HUP INT TERM

	# Union with what is already recorded: append-only in spirit (#371 §3).
	if [ -f "$shipped" ]; then
		cat "$shipped" >> "$union"
	fi
	for f in "$agents_dir"/*.md "$agents_dir"/*.toml; do
		[ -f "$f" ] || continue
		base=${f##*/}
		emit_file_hashes "$f" "$base" "$union"
		emit_history "${f#"$root/"}" "$base" "$union"
	done

	LC_ALL=C sort -u "$union" > "$out"
	mv "$out" "$shipped"
	rm -f "$union"
	trap - EXIT HUP INT TERM
	echo "agents-shipped: wrote $(wc -l < "$shipped" | tr -d ' ') hashes to $shipped"
}

check_mode() {
	if [ ! -f "$shipped" ]; then
		echo "agents-shipped: missing $shipped; run sh scripts/agents-shipped.sh --write" >&2
		exit 1
	fi

	current=$(mktemp)
	trap 'rm -f "$current"' EXIT HUP INT TERM

	for f in "$agents_dir"/*.md "$agents_dir"/*.toml; do
		[ -f "$f" ] || continue
		emit_file_hashes "$f" "${f##*/}" "$current"
	done

	fail=0
	while IFS= read -r line; do
		grep -qxF "$line" "$shipped" && continue
		sha=${line%%  *}
		echo "agents-shipped: ${line#*  }: current sha $sha is not recorded in shipped.sha256; run sh scripts/agents-shipped.sh --write" >&2
		fail=1
	done < "$current"

	rm -f "$current"
	trap - EXIT HUP INT TERM
	[ "$fail" -eq 0 ] || exit 1
	echo "agents-shipped: ok"
}

case $mode in
write) write_mode ;;
check) check_mode ;;
esac
