#!/bin/sh
# scripts/test-shard_test.sh -- tests for the shard assignment.
#
# Every assertion runs --dry-run, which needs only `go list` and the package
# sources: no test is compiled or run. The invariant under test: for any
# TOTAL, the shards are pairwise
# disjoint and their union is every whole package plus every test name of every
# split package.
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
repo=$(CDPATH= cd -- "$here/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

fail=0
status=0

# dry_run runs test-shard.sh --dry-run for INDEX/TOTAL from the repo root,
# leaving its exit status in $status and its output in $work/out. The script
# resolves packages relative to the cwd, as CI does.
dry_run() {
	status=0
	(cd "$repo" && sh "$here/test-shard.sh" --dry-run "$1" "$2") > "$work/out" 2>&1 || status=$?
}

# The bad-usage cases run without --dry-run, exactly as the usage text reads,
# and must exit 2 before running anything.
check_usage() {
	status=0
	(cd "$repo" && sh "$here/test-shard.sh" "$@") > "$work/out" 2>&1 || status=$?
	if [ "$status" -ne 2 ]; then
		echo "FAIL: 'test-shard.sh $*' exits $status, want 2"
		fail=1
	fi
}

# The TOTAL=1 shard is the whole assignment; every other TOTAL is a partition
# of it, so it is the reference the union is compared against.
dry_run 0 1
if [ "$status" -ne 0 ]; then
	echo "FAIL: TOTAL=1 dry run exits $status, want 0"
	fail=1
fi
LC_ALL=C sort -u "$work/out" > "$work/all.txt"

test_lines=$(grep -c '^test .*/internal/relevo ' "$work/all.txt" || :)
if [ "$test_lines" -le 100 ]; then
	echo "FAIL: TOTAL=1 lists $test_lines internal/relevo tests, want more than 100"
	fail=1
fi

pkg_lines=$(grep -c '^pkg ' "$work/all.txt" || :)
if [ "$pkg_lines" -lt 30 ]; then
	echo "FAIL: TOTAL=1 lists $pkg_lines packages, want at least 30"
	fail=1
fi

for total in 1 2 3 4; do
	: > "$work/union-$total.txt"
	index=0
	while [ "$index" -lt "$total" ]; do
		dry_run "$index" "$total"
		if [ "$status" -ne 0 ]; then
			echo "FAIL: TOTAL=$total INDEX=$index exits $status, want 0"
			fail=1
		fi
		cat "$work/out" >> "$work/union-$total.txt"
		index=$((index + 1))
	done

	# Disjoint: no line is produced by two shards.
	dupes=$(LC_ALL=C sort "$work/union-$total.txt" | uniq -d)
	if [ -n "$dupes" ]; then
		echo "FAIL: TOTAL=$total shards overlap:"
		printf '%s\n' "$dupes"
		fail=1
	fi

	# Union: the shards together are exactly the TOTAL=1 assignment.
	LC_ALL=C sort -u "$work/union-$total.txt" > "$work/union-$total.sorted"
	if ! cmp -s "$work/union-$total.sorted" "$work/all.txt"; then
		echo "FAIL: TOTAL=$total shards do not cover the TOTAL=1 assignment"
		fail=1
	fi
done

# Determinism: two dry runs of the same shard are byte-identical.
dry_run 0 3
cp "$work/out" "$work/det0-a.out"
dry_run 0 3
cp "$work/out" "$work/det0-b.out"
if ! cmp -s "$work/det0-a.out" "$work/det0-b.out"; then
	echo "FAIL: two dry runs of shard 0/3 are not identical"
	diff -u "$work/det0-a.out" "$work/det0-b.out" || :
	fail=1
fi

# Balance: with the shipped weights at TOTAL=3, compute each shard's load from
# its pkg lines (weights file, default 5) plus the split packages' weight/3, in
# the test with its own awk; assert max load minus min load is at most the
# largest single whole-package weight (the greedy bound).
for idx in 0 1 2; do
	dry_run "$idx" 3
	if [ "$status" -ne 0 ]; then
		echo "FAIL: balance check dry_run $idx 3 exits $status, want 0"
		fail=1
	fi
	cp "$work/out" "$work/shard-$idx.out"
done

awk -v mod="$(cd "$repo" && go list -m)" \
    -v weights_file="$here/test-shard.weights" \
    -v s0="$work/shard-0.out" -v s1="$work/shard-1.out" -v s2="$work/shard-2.out" '
	function strip_mod(p) {
		if (mod != "" && substr(p, 1, length(mod) + 1) == (mod "/")) {
			return substr(p, length(mod) + 2)
		}
		if (p == mod) return "."
		return p
	}
	BEGIN {
		while ((getline line < weights_file) > 0) {
			if (line ~ /^#/ || line ~ /^[ \t]*$/) continue
			split(line, parts)
			if (parts[1] != "") weights[parts[1]] = parts[2] + 0
		}
		close(weights_file)

		# Default split packages: internal/relevo, internal/db, cmd/relevo
		split_list[1] = "internal/relevo"
		split_list[2] = "internal/db"
		split_list[3] = "cmd/relevo"
		split_cost = 0
		for (i = 1; i <= 3; i++) {
			sp = split_list[i]
			w = (sp in weights) ? weights[sp] : 5
			split_cost += w / 3.0
		}

		shards[0] = s0
		shards[1] = s1
		shards[2] = s2
		max_single_weight = 0

		for (s = 0; s < 3; s++) {
			load[s] = split_cost
			f = shards[s]
			while ((getline line < f) > 0) {
				if (line ~ /^pkg /) {
					pkg = substr(line, 5)
					rel = strip_mod(pkg)
					w = (rel in weights) ? weights[rel] : 5
					load[s] += w
					if (w > max_single_weight) max_single_weight = w
				}
			}
			close(f)
		}

		max_load = load[0]
		min_load = load[0]
		for (s = 1; s < 3; s++) {
			if (load[s] > max_load) max_load = load[s]
			if (load[s] < min_load) min_load = load[s]
		}
		diff = max_load - min_load
		if (diff > max_single_weight) {
			printf "FAIL: balance check: diff=%.2f > max_single_weight=%d (max=%.2f, min=%.2f)\n",
				diff, max_single_weight, max_load, min_load > "/dev/stderr"
			exit 1
		}
	}
' </dev/null || {
	echo "FAIL: balance check exceeded the greedy bound"
	fail=1
}

# Longest-first: with a fixture weights file via SHARD_WEIGHTS giving the two
# heaviest real whole packages different shards.
cat > "$work/longest.weights" <<'EOF'
internal/sync 100
internal/serve 80
EOF
sync_shard=
serve_shard=
for idx in 0 1 2; do
	status=0
	(cd "$repo" && SHARD_WEIGHTS="$work/longest.weights" \
		sh "$here/test-shard.sh" --dry-run "$idx" 3) > "$work/out" 2>&1 || status=$?
	if [ "$status" -ne 0 ]; then
		echo "FAIL: longest-first dry-run $idx 3 exits $status, want 0"
		fail=1
	fi
	if grep -q "pkg .*/internal/sync$" "$work/out"; then
		sync_shard=$idx
	fi
	if grep -q "pkg .*/internal/serve$" "$work/out"; then
		serve_shard=$idx
	fi
done
if [ -z "$sync_shard" ] || [ -z "$serve_shard" ]; then
	echo "FAIL: longest-first: sync_shard=$sync_shard, serve_shard=$serve_shard, expected both assigned"
	fail=1
elif [ "$sync_shard" -eq "$serve_shard" ]; then
	echo "FAIL: longest-first: internal/sync and internal/serve assigned to same shard $sync_shard"
	fail=1
fi

# Unknown packages: SHARD_WEIGHTS pointing at an empty file puts every package
# at the default weight and the three shards' package counts differ by at most 1.
: > "$work/empty.weights"
count0=0
count1=0
count2=0
for idx in 0 1 2; do
	status=0
	(cd "$repo" && SHARD_WEIGHTS="$work/empty.weights" \
		sh "$here/test-shard.sh" --dry-run "$idx" 3) > "$work/out" 2>&1 || status=$?
	if [ "$status" -ne 0 ]; then
		echo "FAIL: empty weights dry-run $idx 3 exits $status, want 0"
		fail=1
	fi
	cnt=$(grep -c '^pkg ' "$work/out" || :)
	case $idx in
		0) count0=$cnt ;;
		1) count1=$cnt ;;
		2) count2=$cnt ;;
	esac
done
max_cnt=$count0
[ "$count1" -gt "$max_cnt" ] && max_cnt=$count1
[ "$count2" -gt "$max_cnt" ] && max_cnt=$count2
min_cnt=$count0
[ "$count1" -lt "$min_cnt" ] && min_cnt=$count1
[ "$count2" -lt "$min_cnt" ] && min_cnt=$count2
diff_cnt=$((max_cnt - min_cnt))
if [ "$diff_cnt" -gt 1 ]; then
	echo "FAIL: package counts with empty weights differ by $diff_cnt ($count0, $count1, $count2), want <= 1"
	fail=1
fi

# Weights file hygiene: every non-comment line is path integer, keys unique,
# and every key resolves to a package in go list ./...
(cd "$repo" && go list ./...) > "$work/all-pkgs.txt"
awk -v mod="$(cd "$repo" && go list -m)" -v pkgs_file="$work/all-pkgs.txt" '
	function strip_mod(p) {
		if (mod != "" && substr(p, 1, length(mod) + 1) == (mod "/")) {
			return substr(p, length(mod) + 2)
		}
		if (p == mod) return "."
		return p
	}
	BEGIN {
		while ((getline line < pkgs_file) > 0) {
			rel = strip_mod(line)
			real_pkgs[rel] = 1
		}
		close(pkgs_file)
		while ((getline line < "scripts/test-shard.weights") > 0) {
			if (line ~ /^#/ || line ~ /^[ \t]*$/) continue
			n = split(line, parts)
			if (n != 2) {
				printf "FAIL: weights hygiene: line %q has %d fields, want 2\n", line, n > "/dev/stderr"
				exit 1
			}
			path = parts[1]
			sec = parts[2]
			if (sec !~ /^[0-9]+$/) {
				printf "FAIL: weights hygiene: weight %q for %q is not integer\n", sec, path > "/dev/stderr"
				exit 1
			}
			if (path in seen) {
				printf "FAIL: weights hygiene: duplicate key %q\n", path > "/dev/stderr"
				exit 1
			}
			seen[path] = 1
			if (!(path in real_pkgs)) {
				printf "FAIL: weights hygiene: key %q does not exist in go list ./...\n", path > "/dev/stderr"
				exit 1
			}
		}
		close("scripts/test-shard.weights")
	}
' </dev/null || {
	echo "FAIL: weights file hygiene check failed"
	fail=1
}

# BSD awk rejects a newline inside a -v assignment, where GNU awk accepts it, so
# a shard script that hands awk its newline-separated list through -v passes on
# Linux and fails on macOS. No BSD awk exists here, so the shim below stands in
# for one: it rejects exactly that argument shape and passes the rest to the
# real awk. Every run in this block goes through it.
mkdir -p "$work/bin"
real_awk=$(command -v awk)
export real_awk
cat > "$work/bin/awk" <<'SH'
#!/bin/sh
# A stand-in for BSD awk: a newline inside a -v value is an error there.
nl='
'
prev=
for arg in "$@"; do
	if [ "$prev" = "-v" ]; then
		case $arg in
		*"$nl"*) echo "awk: newline in string $arg" >&2; exit 2 ;;
		esac
	fi
	case $arg in
	-v?*=*)
		case ${arg#-v} in
		*"$nl"*) echo "awk: newline in string $arg" >&2; exit 2 ;;
		esac
		;;
	esac
	prev=$arg
done
exec "$real_awk" "$@"
SH
chmod +x "$work/bin/awk"

# The shim must reject a newline in a -v value, or the cases below would pass
# under the very construct they exist to catch.
if PATH="$work/bin:$PATH" awk -v x="$(printf 'a\nb')" 'BEGIN { print 1 }' </dev/null >/dev/null 2>&1; then
	echo "FAIL: the BSD-awk shim accepted a newline in -v, so it pins nothing"
	fail=1
fi

# The coverage toggle is read off a `go` stand-in: it records each `go test`
# invocation and exits without compiling anything, and hands every other
# subcommand to the real go so the assignment keeps resolving.
real_go=$(command -v go)
export real_go
export SHIM_LOG="$work/go-argv"
cat > "$work/bin/go" <<'SH'
#!/bin/sh
# A stand-in for go: `go test` is recorded and skipped, so the coverage flags can
# be read off the argv without running anything; everything else is the real go.
if [ "${1:-}" = "test" ]; then
	printf '%s\n' "$*" >> "$SHIM_LOG"
	exit 0
fi
exec "$real_go" "$@"
SH
chmod +x "$work/bin/go"

# record_shard runs one shard from the repo root under the stand-in, leaving the
# recorded argv lines in $1; $2 is SHARD_COVER's value (empty means unset).
record_shard() {
	status=0
	(cd "$repo" && PATH="$work/bin:$PATH" SHIM_LOG="$1" SHARD_COVER="$2" \
		sh "$here/test-shard.sh" 0 3 "$work/shard") > "$work/out" 2>&1 || status=$?
}

# Default: coverage is on, so every `go test` carries -cover and race, and at
# least one split job carries its profile.
record_shard "$work/argv-cover" ""
if [ "$status" -ne 0 ]; then
	echo "FAIL: a shard with coverage on exits $status, want 0"
	fail=1
fi
if [ ! -s "$work/argv-cover" ]; then
	echo "FAIL: the go stand-in recorded no go test invocation with coverage on"
	fail=1
else
	while IFS= read -r line; do
		case $line in
		*"-race -count=1"*) ;;
		*) echo "FAIL: with coverage on, '$line' lacks -race -count=1"; fail=1 ;;
		esac
		case $line in
		*-cover*) ;;
		*) echo "FAIL: with coverage on, '$line' lacks -cover"; fail=1 ;;
		esac
	done < "$work/argv-cover"
	if ! grep -q -- '-coverprofile=' "$work/argv-cover"; then
		echo "FAIL: with coverage on, no go test carries -coverprofile="
		fail=1
	fi
	# The split base names are distinct for the default SPLIT_PKGS
	awk '
		/-coverprofile=/ {
			for (i = 1; i <= NF; i++) {
				if ($i ~ /^-coverprofile=/) {
					prof = substr($i, 15)
					sub(/.*\//, "", prof)
					profs[prof]++
				}
			}
		}
		END {
			for (p in profs) {
				if (profs[p] > 1) {
					printf "FAIL: duplicate cover profile: %s\n", p > "/dev/stderr"
					exit 1
				}
			}
			if (!("split-cmd-relevo.out" in profs)) {
				printf "FAIL: missing split-cmd-relevo.out\n" > "/dev/stderr"
				exit 1
			}
			if (!("split-internal-relevo.out" in profs)) {
				printf "FAIL: missing split-internal-relevo.out\n" > "/dev/stderr"
				exit 1
			}
		}
	' "$work/argv-cover" || {
		echo "FAIL: split cover profiles check failed"
		fail=1
	}
fi

# SHARD_COVER=0 drops both coverage flags and keeps race on every job.
record_shard "$work/argv-nocover" 0
if [ "$status" -ne 0 ]; then
	echo "FAIL: a shard with SHARD_COVER=0 exits $status, want 0"
	fail=1
fi
if [ ! -s "$work/argv-nocover" ]; then
	echo "FAIL: the go stand-in recorded no go test invocation with coverage off"
	fail=1
else
	while IFS= read -r line; do
		case $line in
		*"-race -count=1"*) ;;
		*) echo "FAIL: with SHARD_COVER=0, '$line' lacks -race -count=1"; fail=1 ;;
		esac
		case $line in
		*-cover*) echo "FAIL: with SHARD_COVER=0, '$line' still carries coverage"; fail=1 ;;
		esac
	done < "$work/argv-nocover"
fi

# SPLIT_PKGS naming two packages is the shape the default list takes once a
# second package joins it. dry_run_split runs --dry-run with both split, under
# the shim.
dry_run_split() {
	status=0
	(cd "$repo" && PATH="$work/bin:$PATH" \
		SPLIT_PKGS="./internal/relevo ./internal/delivery" \
		sh "$here/test-shard.sh" --dry-run "$1" "$2") > "$work/out" 2>&1 || status=$?
}

# TOTAL=1 is the two-package assignment the shards below are compared against.
dry_run_split 0 1
if [ "$status" -ne 0 ]; then
	echo "FAIL: SPLIT_PKGS naming two packages, TOTAL=1 exits $status, want 0"
	fail=1
else
	LC_ALL=C sort -u "$work/out" > "$work/two-all.txt"
	# Each split package must contribute `test` lines rather than a whole `pkg`
	# line: the exit status alone would also hold for a run that skipped nothing.
	for dir in internal/relevo internal/delivery; do
		if grep -q "^pkg .*/$dir\$" "$work/two-all.txt"; then
			echo "FAIL: split package $dir is assigned whole, not split"
			fail=1
		fi
		if ! grep -q "^test .*/$dir " "$work/two-all.txt"; then
			echo "FAIL: split package $dir contributes no tests"
			fail=1
		fi
	done
fi

# TOTAL=3 is the shape CI runs, and two split packages make it the first case
# with more than one split file.
: > "$work/two-union.txt"
bad=0
index=0
while [ "$index" -lt 3 ]; do
	dry_run_split "$index" 3
	if [ "$status" -ne 0 ]; then
		echo "FAIL: SPLIT_PKGS naming two packages, TOTAL=3 INDEX=$index exits $status, want 0"
		fail=1
		bad=1
	else
		cat "$work/out" >> "$work/two-union.txt"
	fi
	index=$((index + 1))
done

# A shard that never reached awk says nothing about the assignment, so the
# partition checks are meaningless when one failed above.
if [ "$bad" -eq 0 ]; then
	dupes=$(LC_ALL=C sort "$work/two-union.txt" | uniq -d)
	if [ -n "$dupes" ]; then
		echo "FAIL: SPLIT_PKGS naming two packages, TOTAL=3 shards overlap:"
		printf '%s\n' "$dupes"
		fail=1
	fi

	LC_ALL=C sort -u "$work/two-union.txt" > "$work/two-union.sorted"
	if ! cmp -s "$work/two-union.sorted" "$work/two-all.txt"; then
		echo "FAIL: SPLIT_PKGS naming two packages, TOTAL=3 shards do not cover the TOTAL=1 assignment"
		fail=1
	fi
fi

# Bad usage: no arguments, INDEX == TOTAL, negative INDEX, nonexistent SHARD_WEIGHTS.
check_usage
check_usage 3 3
check_usage -1 2

status=0
(cd "$repo" && SHARD_WEIGHTS="$work/nonexistent" sh "$here/test-shard.sh" 0 3) > "$work/out" 2>&1 || status=$?
if [ "$status" -ne 2 ]; then
	echo "FAIL: nonexistent SHARD_WEIGHTS exits $status, want 2"
	fail=1
fi

[ "$fail" -eq 0 ] && echo "test-shard: ok"
exit "$fail"
