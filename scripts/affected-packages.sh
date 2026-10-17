#!/bin/sh
# scripts/affected-packages.sh -- list Go packages affected by changes against a base ref.
#
# Usage: affected-packages.sh [--base REF]
#   --base REF  git commit or ref to compare against (default: git merge-base HEAD origin/main)
# Env:   BASE        fallback override for base ref; the --base flag takes precedence
# Exit:  0 on success (including fallback to ./...), 2 on bad usage.
#
# Stdout is one import path per affected package, or './...' when all packages
# are affected, or empty when no package is affected. Stderr carries a one-line
# reason when falling back to './...'.
set -eu

# resolve_base returns the base git ref. If the ref cannot be resolved, it
# prints ./... to stdout, a reason to stderr, and exits 0.
resolve_base() {
	override="$1"
	if [ -n "$override" ]; then
		if ! git rev-parse --verify "$override^{commit}" >/dev/null 2>&1; then
			echo "base ref '$override' cannot be resolved; falling back to all packages" >&2
			echo "./..."
			exit 0
		fi
		echo "$override"
		return 0
	fi

	if ! git rev-parse --verify "origin/main^{commit}" >/dev/null 2>&1; then
		echo "origin/main ref is missing; falling back to all packages" >&2
		echo "./..."
		exit 0
	fi

	mb=$(git merge-base HEAD origin/main 2>/dev/null || :)
	if [ -z "$mb" ]; then
		echo "git merge-base HEAD origin/main failed; falling back to all packages" >&2
		echo "./..."
		exit 0
	fi
	echo "$mb"
}

# list_changed_paths writes repo-relative changed paths against target base to
# the output file, combining working tree diffs and untracked files.
list_changed_paths() {
	target_base="$1"
	out_file="$2"

	diff_out="$out_file.diff"
	untracked_out="$out_file.untracked"

	if ! git -c core.quotepath=off diff --name-only --no-renames "$target_base" > "$diff_out" 2>/dev/null; then
		echo "git diff against '$target_base' failed; falling back to all packages" >&2
		echo "./..."
		exit 0
	fi

	if ! git -c core.quotepath=off ls-files --others --exclude-standard > "$untracked_out" 2>/dev/null; then
		echo "git ls-files failed; falling back to all packages" >&2
		echo "./..."
		exit 0
	fi

	{
		cat "$diff_out"
		cat "$untracked_out"
	} | LC_ALL=C sort -u > "$out_file"
}

# map_paths_to_seeds maps changed paths to seeded module packages. Root build
# configs, deleted directories, or unmapped paths fall back to ./... and exit.
map_paths_to_seeds() {
	paths_file="$1"
	pkg_dirs_file="$2"
	seeds_file="$3"

	while IFS= read -r path || [ -n "$path" ]; do
		[ -n "$path" ] || continue

		case "$path" in
			go.mod|go.sum|Makefile|.golangci.yml)
				echo "build configuration changed ($path); falling back to all packages" >&2
				echo "./..."
				exit 0
				;;
			docs|docs/*|LICENSE)
				continue
				;;
			*/*)
				;;
			*.md)
				continue
				;;
		esac

		case "$path" in
			*/*) cur_dir="${path%/*}" ;;
			*)   cur_dir="." ;;
		esac

		if [ ! -d "$cur_dir" ]; then
			echo "directory no longer exists ($cur_dir); falling back to all packages" >&2
			echo "./..."
			exit 0
		fi

		while :; do
			pkg=$(awk -F'\t' -v d="$cur_dir" '$1 == d { print $2; exit }' "$pkg_dirs_file")
			if [ -n "$pkg" ]; then
				echo "$pkg" >> "$seeds_file"
				break
			fi

			has_go=0
			for f in "$cur_dir"/*.go; do
				if [ -f "$f" ]; then
					has_go=1
					break
				fi
			done
			if [ "$has_go" -eq 1 ]; then
				echo "directory holds unlisted .go files ($cur_dir); falling back to all packages" >&2
				echo "./..."
				exit 0
			fi

			if [ "$cur_dir" = "." ]; then
				echo "unmapped path reaching root ($path); falling back to all packages" >&2
				echo "./..."
				exit 0
			fi

			case "$cur_dir" in
				*/*) cur_dir="${cur_dir%/*}" ;;
				*)   cur_dir="." ;;
			esac
		done
	done < "$paths_file"
}

# compute_closure finds the transitive reverse closure of seeded packages
# across module package dependencies, printing sorted import paths.
compute_closure() {
	graph_file="$1"
	seeds_file="$2"

	[ -s "$seeds_file" ] || return 0

	awk -v seeds_file="$seeds_file" '
	BEGIN { FS = "\t" }
	FILENAME != seeds_file {
		pkg = $1
		if (pkg == "") next
		is_module_pkg[pkg] = 1
		n = split($2, deps, ",")
		for (i = 1; i <= n; i++) {
			d = deps[i]
			if (d != "" && d != pkg) {
				edge_key = d SUBSEP pkg
				if (!seen_edge[edge_key]) {
					seen_edge[edge_key] = 1
					edge_count[d]++
					importers[d, edge_count[d]] = pkg
				}
			}
		}
		next
	}
	FILENAME == seeds_file {
		seed = $0
		if (seed != "" && is_module_pkg[seed]) {
			if (!affected[seed]) {
				affected[seed] = 1
				queue[++qtail] = seed
			}
		}
		next
	}
	END {
		if (qtail == 0) exit
		qhead = 0
		while (qhead < qtail) {
			curr = queue[++qhead]
			count = edge_count[curr]
			for (i = 1; i <= count; i++) {
				imp = importers[curr, i]
				if (is_module_pkg[imp] && !affected[imp]) {
					affected[imp] = 1
					queue[++qtail] = imp
				}
			}
		}
		for (pkg in affected) print pkg
	}
	' "$graph_file" "$seeds_file" | LC_ALL=C sort -u
}

usage() {
	echo "usage: affected-packages.sh [--base REF]" >&2
}

base_arg=""
if [ -n "${BASE:-}" ]; then
	base_arg="$BASE"
fi

while [ $# -gt 0 ]; do
	case "$1" in
		--base)
			if [ $# -lt 2 ]; then
				usage
				exit 2
			fi
			base_arg="$2"
			shift 2
			;;
		--base=*)
			base_arg="${1#*=}"
			shift
			;;
		*)
			usage
			exit 2
			;;
	esac
done

toplevel=$(git rev-parse --show-toplevel 2>/dev/null || :)
if [ -z "$toplevel" ]; then
	echo "not in a git repository; falling back to all packages" >&2
	echo "./..."
	exit 0
fi

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
CDPATH= cd -- "$toplevel"

if [ ! -f "go.mod" ]; then
	echo "no go.mod at repository root; falling back to all packages" >&2
	echo "./..."
	exit 0
fi

base=$(resolve_base "$base_arg")

work=$(mktemp -d)
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

changed_paths="$work/changed_paths.txt"
list_changed_paths "$base" "$changed_paths"

if [ ! -s "$changed_paths" ]; then
	exit 0
fi

modpath=$(go list -m 2>/dev/null || :)
if [ -z "$modpath" ]; then
	echo "go list -m failed; falling back to all packages" >&2
	echo "./..."
	exit 0
fi

graph="$work/graph.txt"
if ! go list -e -f '{{.ImportPath}}{{"\t"}}{{join .Imports ","}},{{join .TestImports ","}},{{join .XTestImports ","}}' ./... > "$graph" 2>/dev/null; then
	echo "go list ./... failed; falling back to all packages" >&2
	echo "./..."
	exit 0
fi

pkg_dirs="$work/pkg_dirs.txt"
awk -F'\t' -v mod="$modpath" '
{
	pkg = $1
	if (pkg == mod) {
		dir = "."
	} else if (index(pkg, mod "/") == 1) {
		dir = substr(pkg, length(mod) + 2)
	} else {
		next
	}
	print dir "\t" pkg
}
' "$graph" > "$pkg_dirs"

seeds="$work/seeds.txt"
: > "$seeds"
map_paths_to_seeds "$changed_paths" "$pkg_dirs" "$seeds"

compute_closure "$graph" "$seeds"
