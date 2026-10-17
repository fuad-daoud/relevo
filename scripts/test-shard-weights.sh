#!/bin/sh
# scripts/test-shard-weights.sh -- generate shard weights from test logs.
#
# Usage: test-shard-weights.sh [FILE...]
# Reads go test output (text or -json) from files or stdin, sums per-package
# test durations across logs, and emits package weights in seconds on stdout.
#
# The inputs should come from one leg's logs (the three ubuntu/stable shards,
# not also macOS), because mixing legs double counts package durations.
#
# Exit: 0 on success, 1 when input yields no packages, 2 on bad usage.
set -eu

usage() {
	echo "usage: test-shard-weights.sh [FILE...]" >&2
	exit 2
}

for arg in "$@"; do
	case $arg in
	--)
		break
		;;
	-*)
		if [ "$arg" != "-" ]; then
			usage
		fi
		;;
	esac
done

for arg in "$@"; do
	if [ "$arg" != "-" ] && [ ! -r "$arg" ]; then
		echo "test-shard-weights: cannot read $arg" >&2
		exit 2
	fi
done

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
repo=$(CDPATH= cd -- "$here/.." && pwd)
mod=$(cd "$repo" && go list -m 2>/dev/null || :)

work=$(mktemp -d)
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

awk -v mod="$mod" '
function json_val(json, key,    p, rest, val) {
	p = index(json, "\"" key "\"")
	if (p == 0) return ""
	rest = substr(json, p + length(key) + 2)
	sub(/^[ \t]*:[ \t]*/, "", rest)
	if (substr(rest, 1, 1) == "\"") {
		rest = substr(rest, 2)
		val = substr(rest, 1, index(rest, "\"") - 1)
		return val
	} else {
		match(rest, /^[-+0-9.eE]+/)
		if (RSTART > 0) {
			return substr(rest, RSTART, RLENGTH)
		}
		return ""
	}
}

function strip_mod(p) {
	if (mod != "" && substr(p, 1, length(mod) + 1) == (mod "/")) {
		return substr(p, length(mod) + 2)
	}
	if (p == mod) {
		return "."
	}
	return p
}

{
	line = $0
	sub(/^[ \t]+/, "", line)
	if (substr(line, 1, 1) == "{") {
		# go test -json output
		if (index(line, "\"Test\"") > 0) next
		action = json_val(line, "Action")
		pkg = json_val(line, "Package")
		if (pkg == "") next
		pkg = strip_mod(pkg)
		if (action == "pass" || action == "fail") {
			el = json_val(line, "Elapsed") + 0
			secs[pkg] += el
			ok[pkg] = 1
			seen[pkg] = 1
		} else if (action == "skip") {
			seen[pkg] = 1
		}
		next
	}

	# Text output: strip ANSI escapes
	gsub(/\033\[[0-9;]*[a-zA-Z]/, "")
	if ($0 ~ /(^|[ \t])FAIL([ \t]|$)/) next

	if ($0 ~ /\[no test files\]/) {
		for (i = 1; i < NF; i++) {
			if ($i == "?") {
				p = strip_mod($(i + 1))
				seen[p] = 1
				next
			}
		}
	}

	for (i = 1; i <= NF; i++) {
		if ($i == "ok") {
			if (i + 1 > NF) next
			p = strip_mod($(i + 1))
			if (i + 2 <= NF && $(i + 2) == "(cached)") next
			if (i + 3 <= NF && $(i + 3) == "(cached)") next
			if ($0 ~ /\(cached\)/) next
			if (i + 2 <= NF) {
				t = $(i + 2)
				sub(/s$/, "", t)
				el = t + 0
				secs[p] += el
				ok[p] = 1
				seen[p] = 1
			}
			next
		}
	}
}

END {
	count = 0
	for (p in seen) {
		count++
		s = secs[p] + 0
		r = int(s + 0.5)
		if (ok[p] && r < 1) r = 1
		print p " " r
	}
	if (count == 0) exit 1
}
' "$@" > "$work/table" || exit 1

if [ ! -s "$work/table" ]; then
	exit 1
fi

cat <<'EOF'
# Shard weights for relevo packages, in integer seconds.
# Generated from one leg's logs (ubuntu/stable shards, not macOS).
# Regenerate with:
#   scripts/test-shard-weights.sh <shard-0.log> <shard-1.log> <shard-2.log> > scripts/test-shard.weights
EOF
LC_ALL=C sort -k1,1 "$work/table"
