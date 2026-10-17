#!/bin/sh
# scripts/test-shard-weights_test.sh -- tests for test-shard-weights.sh.
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

generator="$here/test-shard-weights.sh"
fail=0
status=0

run_stdin() {
	status=0
	sh "$generator" > "$work/out" 2>&1 || status=$?
}

run_args() {
	status=0
	sh "$generator" "$@" > "$work/out" 2>&1 || status=$?
}

cat > "$work/expected" <<'EOF'
# Shard weights for relevo packages, in integer seconds.
# Generated from one leg's logs (ubuntu/stable shards, not macOS).
# Regenerate with:
#   scripts/test-shard-weights.sh <shard-0.log> <shard-1.log> <shard-2.log> > scripts/test-shard.weights
internal/ansi 2
internal/db 177
internal/fast 1
internal/notest 0
internal/relevo 261
EOF

# Text format input:
# - timestamp prefix
# - split package listed three times (summed)
# - cached line skipped
# - FAIL line ignored
# - no-test-files line giving 0
# - sub-second ok giving 1
# - ANSI escape sequence tolerated
cat > "$work/input.txt" <<'EOF'
2026-10-17T18:00:00.0000000Z ok	github.com/fuad-daoud/relevo/internal/relevo	100.0s
2026-10-17T18:00:01.0000000Z ok	github.com/fuad-daoud/relevo/internal/relevo	100.0s
2026-10-17T18:00:02.0000000Z ok	github.com/fuad-daoud/relevo/internal/relevo	61.0s
ok	github.com/fuad-daoud/relevo/internal/db	177.2s
ok	github.com/fuad-daoud/relevo/internal/fast	0.002s
ok	github.com/fuad-daoud/relevo/internal/cached	(cached)
FAIL	github.com/fuad-daoud/relevo/internal/broken	15.0s
?	github.com/fuad-daoud/relevo/internal/notest	[no test files]
[0mok	github.com/fuad-daoud/relevo/internal/ansi	2.0s
EOF

# 1. Text input via stdin
run_stdin < "$work/input.txt"
if [ "$status" -ne 0 ]; then
	echo "FAIL: text input via stdin exits $status, want 0"
	fail=1
elif ! cmp -s "$work/out" "$work/expected"; then
	echo "FAIL: text input output does not match expected"
	diff -u "$work/expected" "$work/out" || :
	fail=1
fi

# 2. Text input via file argument
run_args "$work/input.txt"
if [ "$status" -ne 0 ]; then
	echo "FAIL: text input via file argument exits $status, want 0"
	fail=1
elif ! cmp -s "$work/out" "$work/expected"; then
	echo "FAIL: text input via file argument does not match expected"
	diff -u "$work/expected" "$work/out" || :
	fail=1
fi

# JSON format input giving the same table
cat > "$work/input.json" <<'EOF'
{"Time":"2026-10-17T18:00:00Z","Action":"pass","Package":"github.com/fuad-daoud/relevo/internal/relevo","Elapsed":100.0}
{"Time":"2026-10-17T18:00:01Z","Action":"pass","Package":"github.com/fuad-daoud/relevo/internal/relevo","Elapsed":100.0}
{"Time":"2026-10-17T18:00:02Z","Action":"pass","Package":"github.com/fuad-daoud/relevo/internal/relevo","Elapsed":61.0}
{"Time":"2026-10-17T18:00:00Z","Action":"pass","Package":"github.com/fuad-daoud/relevo/internal/db","Elapsed":177.2}
{"Time":"2026-10-17T18:00:00Z","Action":"pass","Package":"github.com/fuad-daoud/relevo/internal/fast","Elapsed":0.002}
{"Time":"2026-10-17T18:00:00Z","Action":"skip","Package":"github.com/fuad-daoud/relevo/internal/notest"}
{"Time":"2026-10-17T18:00:00Z","Action":"pass","Package":"github.com/fuad-daoud/relevo/internal/ansi","Elapsed":2.0}
{"Time":"2026-10-17T18:00:00Z","Action":"run","Package":"github.com/fuad-daoud/relevo/internal/db","Test":"TestIgnored"}
{"Time":"2026-10-17T18:00:00Z","Action":"pass","Package":"github.com/fuad-daoud/relevo/internal/db","Test":"TestIgnored","Elapsed":999.0}
EOF

run_stdin < "$work/input.json"
if [ "$status" -ne 0 ]; then
	echo "FAIL: JSON input via stdin exits $status, want 0"
	fail=1
elif ! cmp -s "$work/out" "$work/expected"; then
	echo "FAIL: JSON input output does not match expected"
	diff -u "$work/expected" "$work/out" || :
	fail=1
fi

# 3. Empty input exits 1
run_stdin < /dev/null
if [ "$status" -ne 1 ]; then
	echo "FAIL: empty stdin exits $status, want 1"
	fail=1
fi

# 4. Bad usage exits 2
run_args --bad-flag
if [ "$status" -ne 2 ]; then
	echo "FAIL: bad flag exits $status, want 2"
	fail=1
fi

run_args "$work/does-not-exist"
if [ "$status" -ne 2 ]; then
	echo "FAIL: nonexistent file exits $status, want 2"
	fail=1
fi

[ "$fail" -eq 0 ] && echo "test-shard-weights: ok"
exit "$fail"
