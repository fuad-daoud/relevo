#!/bin/sh
# scripts/cover-merge.sh -- merge sharded coverage into one .coverage.txt.
#
# Usage: cover-merge.sh DIR
#   DIR   a directory searched recursively for whole.txt, split-*.txt and
#         split-*.out (the downloaded shard artifacts)
# Output: on stdout, a file scripts/check-coverage.sh parses unchanged:
#   - every line of every whole.txt, verbatim (the shards partition the whole
#     packages, so no package line repeats);
#   - for each split package, one synthesized line
#     "ok  <import path>  0.000s  coverage: <P>% of statements", where P is the
#     union of that package's shards' profiles.
# Exit: 0; 1 when DIR holds no whole.txt at all, or when a split-<base>.out set
#       is empty or unparsable.
#
# The synthesized percentage is the arithmetic `go test -cover` itself uses --
# a block counts as covered when any shard reports count > 0 for it -- so an
# unsharded run and a merged sharded run agree for that package.
set -eu

usage() {
	echo "usage: cover-merge.sh DIR" >&2
	exit 2
}

case $# in
	1) dir=$1 ;;
	*) usage ;;
esac

# Whole packages: copied verbatim. No whole.txt at all means the artifacts are
# missing, which must fail here rather than let the coverage guard pass
# vacuously.
wholes=$(find "$dir" -type f -name whole.txt | LC_ALL=C sort)
if [ -z "$wholes" ]; then
	echo "cover-merge: no whole.txt under $dir" >&2
	exit 1
fi

while IFS= read -r f; do
	[ -n "$f" ] || continue
	cat "$f"
done <<EOF
$wholes
EOF

# One <base> per split package. A shard that got none of a split package's
# tests writes neither of its files, so the .out set is what identifies the
# packages that need a synthesized line.
bases=$(find "$dir" -type f -name 'split-*.out' \
	| sed -e 's#.*/split-##' -e 's#\.out$##' \
	| LC_ALL=C sort -u)

while IFS= read -r base; do
	[ -n "$base" ] || continue

	# The import path is the $2 of the ok/FAIL/--- line carrying coverage: in
	# any of the package's shard outputs. A failing run prints no such line, so
	# fall back to the profile keys' file parts.
	imp=$(find "$dir" -type f -name "split-$base.txt" -exec awk '
		$1 == "ok" || $1 == "FAIL" || $1 ~ /^---/ {
			for (i = 1; i <= NF; i++) {
				if ($i == "coverage:") { print $2; exit }
			}
		}
	' {} +)

	# Union of the shards' profiles. total sums every distinct block's
	# statements once; total_cov sums the ones any shard reported as covered.
	merged=$(find "$dir" -type f -name "split-$base.out" -exec awk -v pkg="$imp" '
		/^mode:/ { next }
		NF >= 3 {
			key = $1
			n = $2 + 0
			c = $3 + 0
			if (pkg != "") {
				file = key
				sub(/:[0-9]+\.[0-9]+,[0-9]+\.[0-9]+$/, "", file)
				owner = file
				sub(/\/[^\/]*\.go$/, "", owner)
				if (owner != pkg) next
			}
			if (!(key in seen)) { seen[key] = 1; total += n }
			if (c > 0 && !(key in covered)) { covered[key] = 1; total_cov += n }
		}
		END {
			if (total == 0) exit 1
			printf "%.1f", 100 * total_cov / total
		}
	' {} +)

	if [ -z "$merged" ]; then
		echo "cover-merge: no coverage blocks for split package $base" >&2
		exit 1
	fi

	printf 'ok  %s  0.000s  coverage: %s%% of statements\n' "$imp" "$merged"
done <<EOF
$bases
EOF
