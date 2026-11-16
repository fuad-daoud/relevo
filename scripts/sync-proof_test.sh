#!/bin/sh
# scripts/sync-proof_test.sh -- tests for the sync-proof pure core.
#
# Every assertion drives scripts/sync-proof.sh with no network, no relevo
# binary and no sandbox: the CLI runs under a temp HOME so the default env path
# resolves into a temp dir, and the pure functions run in a subshell that
# sources the script with SYNC_PROOF_SOURCED set, so a refusal's exit ends that
# subshell and not the suite. The fake env files carry sentinel secret values;
# a final sweep asserts no sentinel ever reached any output, which is the pin
# that a refusal names variables and never echoes a value.
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
script="$here/sync-proof.sh"

work=$(mktemp -d)
home="$work/home"
mkdir -p "$home"
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

fail=0
all="$work/all-output"
: > "$all"

# sp runs the script as a command under the temp HOME, capturing combined
# output and status. The temp HOME keeps the default env path off the
# developer's real ~/.config, so no assertion depends on the machine.
sp() {
	out=$(HOME="$home" sh "$script" "$@" 2>&1) && got=0 || got=$?
	printf '%s\n' "$out" >> "$all"
}

# src runs one function of the sourced pure core in a subshell. SYNC_PROOF_SOURCED
# keeps main from running; the subshell means a function's exit 2 (a refusal) or
# exit 1 (quota_allows refusing) ends the subshell, not the suite.
# shellcheck disable=SC2016 # the inner sh expands $1/$@, not this shell
src() {
	out=$(SYNC_PROOF_SOURCED=1 sh -c '. "$1"; shift; "$@"' sh "$script" "$@" 2>&1) && got=0 || got=$?
	printf '%s\n' "$out" >> "$all"
}

status() {
	if [ "$got" -ne "$2" ]; then
		echo "FAIL: $1 (exit $got, want $2): $out"
		fail=1
	fi
}
carries() {
	if ! printf '%s' "$out" | grep -qF -- "$2"; then
		echo "FAIL: $1 (no \"$2\" in: $out)"
		fail=1
	fi
}
absent() {
	if printf '%s' "$out" | grep -qF -- "$2"; then
		echo "FAIL: $1 (\"$2\" present in: $out)"
		fail=1
	fi
}
equals() {
	if [ "$out" != "$2" ]; then
		echo "FAIL: $1 (got \"$out\", want \"$2\")"
		fail=1
	fi
}

# Sentinel values: one per required variable. None may appear in any output the
# suite captures; the sweep at the end reads them all off $all.
s_url='SENTINEL-URL-7f3a9c'
s_token='SENTINEL-TOKEN-2b8e1d'
s_endpoint='SENTINEL-ENDPOINT-9c4f0a'
s_bucket='SENTINEL-BUCKET-5e7d2b'
s_keyid='SENTINEL-KEYID-1a6c8f'
s_secret='SENTINEL-SECRET-4f9b3e'

cat > "$work/good.env" <<EOF
RELEVO_SCRATCH_SYNC_URL='$s_url'
RELEVO_SCRATCH_SYNC_TOKEN='$s_token'
RELEVO_SCRATCH_R2_ENDPOINT='$s_endpoint'
RELEVO_SCRATCH_R2_BUCKET='$s_bucket'
RELEVO_SCRATCH_R2_KEY_ID='$s_keyid'
RELEVO_SCRATCH_R2_SECRET='$s_secret'
EOF

# The estimate arithmetic, written out rather than copied from output: three
# passes (seed write, migrate read, migrate write) over LOG_MB mebibytes.
est20=$((3 * 20 * 1024 * 1024))
est7=$((3 * 7 * 1024 * 1024))
est1=$((3 * 1 * 1024 * 1024))

# --help is the one invocation that exits 0.
sp --help
status '--help exits 0' 0
carries '--help prints usage' 'usage:'
carries '--help names --log-mb' '--log-mb'

# The sourcing guard: with SYNC_PROOF_SOURCED set, sourcing defines the
# functions and runs nothing, so the echo after the source still runs.
# shellcheck disable=SC2016 # the inner sh expands $1, not this shell
out=$(SYNC_PROOF_SOURCED=1 sh -c '. "$1"; echo sourced-ok' sh "$script" 2>&1) && got=0 || got=$?
printf '%s\n' "$out" >> "$all"
equals 'sourcing with SYNC_PROOF_SOURCED runs no main' 'sourced-ok'
status 'the guarded source exits 0' 0
# Without the variable, sourcing runs main: the default env file is absent
# under the temp HOME, so main refuses before the echo can run. This pins that
# the variable is what suppresses main, not the source itself.
# shellcheck disable=SC2016 # the inner sh expands $1, not this shell
out=$(HOME="$home" sh -c '. "$1"; echo sourced-ok' sh "$script" 2>&1) && got=0 || got=$?
printf '%s\n' "$out" >> "$all"
status 'sourcing without SYNC_PROOF_SOURCED runs main' 2
absent 'the unguarded source never reaches the echo' 'sourced-ok'

# parse_args refusals: each exits 2 with usage on stderr.
sp --bogus
status 'an unknown flag is refused' 2
carries 'the unknown-flag refusal names the flag' '--bogus'
carries 'the unknown-flag refusal prints usage' 'usage:'
sp stray
status 'a positional argument is refused' 2
carries 'the positional refusal names it' 'unexpected argument'
sp --env
status '--env with no value is refused' 2
carries '--env missing-value refusal names --env' '--env needs a file'
carries '--env missing-value refusal prints usage' 'usage:'
sp --env --wipe
status '--env followed by a flag is a missing value' 2
carries '--env-before-flag refusal names --env' '--env needs a file'
sp --log-mb
status '--log-mb with no value is refused' 2
carries '--log-mb missing-value refusal names --log-mb' '--log-mb needs a value'
sp --log-mb --wipe
status '--log-mb followed by a flag is a missing value' 2
carries '--log-mb-before-flag refusal names --log-mb' '--log-mb needs a value'
sp --log-mb abc
status 'a non-numeric --log-mb is refused' 2
carries 'non-numeric --log-mb refusal names the rule' 'positive integer'
sp --log-mb 0
status 'a zero --log-mb is refused' 2
carries 'zero --log-mb refusal names the rule' 'positive integer'
sp --log-mb -5
status 'a negative --log-mb is refused' 2
carries 'negative --log-mb refusal names the rule' 'positive integer'
sp --log-mb 2.5
status 'a fractional --log-mb is refused' 2
carries 'fractional --log-mb refusal names the rule' 'positive integer'
sp --log-mb ''
status 'an empty --log-mb is refused' 2
carries 'empty --log-mb refusal names the rule' 'positive integer'

# load_env: a missing file names all six variables and the path, exit 2.
sp --env "$work/absent.env"
status 'a missing env file is refused' 2
carries 'the missing-file refusal names the path' 'env file not found'
for name in RELEVO_SCRATCH_SYNC_URL RELEVO_SCRATCH_SYNC_TOKEN RELEVO_SCRATCH_R2_ENDPOINT RELEVO_SCRATCH_R2_BUCKET RELEVO_SCRATCH_R2_KEY_ID RELEVO_SCRATCH_R2_SECRET; do
	carries "the missing-file refusal names $name" "$name"
done

# An empty file leaves all six unset.
: > "$work/empty.env"
sp --env "$work/empty.env"
status 'an empty env file is refused' 2
carries 'the empty-file refusal names the variables' 'missing env variables'
for name in RELEVO_SCRATCH_SYNC_URL RELEVO_SCRATCH_SYNC_TOKEN RELEVO_SCRATCH_R2_ENDPOINT RELEVO_SCRATCH_R2_BUCKET RELEVO_SCRATCH_R2_KEY_ID RELEVO_SCRATCH_R2_SECRET; do
	carries "the empty-file refusal names $name" "$name"
done

# Each variable absent in turn: the refusal names exactly the missing one, not
# a present one, and echoes none of the sentinel values the file still holds.
for name in RELEVO_SCRATCH_SYNC_URL RELEVO_SCRATCH_SYNC_TOKEN RELEVO_SCRATCH_R2_ENDPOINT RELEVO_SCRATCH_R2_BUCKET RELEVO_SCRATCH_R2_KEY_ID RELEVO_SCRATCH_R2_SECRET; do
	grep -v "^$name=" "$work/good.env" > "$work/minus.env"
	sp --env "$work/minus.env"
	status "load_env refuses when $name is absent" 2
	carries "the refusal names the absent $name" "$name"
	if [ "$name" = RELEVO_SCRATCH_SYNC_TOKEN ]; then
		witness=RELEVO_SCRATCH_SYNC_URL
	else
		witness=RELEVO_SCRATCH_SYNC_TOKEN
	fi
	absent "the refusal does not name the present $witness" "$witness"
	absent "the refusal echoes no sentinel token" "$s_token"
	absent "the refusal echoes no sentinel secret" "$s_secret"
done

# An empty value is missing too: blank the token, keep the rest.
sed "s/^RELEVO_SCRATCH_SYNC_TOKEN=.*/RELEVO_SCRATCH_SYNC_TOKEN=''/" "$work/good.env" > "$work/blank.env"
sp --env "$work/blank.env"
status 'an empty variable value is refused' 2
carries 'the empty-value refusal names the token' 'RELEVO_SCRATCH_SYNC_TOKEN'
absent 'the empty-value refusal names no present variable' 'RELEVO_SCRATCH_SYNC_URL'
absent 'the empty-value refusal echoes no sentinel' "$s_secret"

# Several missing at once: only URL set, the other five named.
grep '^RELEVO_SCRATCH_SYNC_URL=' "$work/good.env" > "$work/one.env"
sp --env "$work/one.env"
status 'a file with one variable is refused' 2
for name in RELEVO_SCRATCH_SYNC_TOKEN RELEVO_SCRATCH_R2_ENDPOINT RELEVO_SCRATCH_R2_BUCKET RELEVO_SCRATCH_R2_KEY_ID RELEVO_SCRATCH_R2_SECRET; do
	carries "the one-variable refusal names $name" "$name"
done
absent 'the one-variable refusal does not name the present URL' 'RELEVO_SCRATCH_SYNC_URL'

# A refusal creates nothing: run in an empty cwd and compare the file set
# before and after, for both a refused load and the estimate-and-stop main.
mkdir -p "$work/empty-cwd"
before=$(find "$work/empty-cwd" -mindepth 1 | sort)
(cd "$work/empty-cwd" && HOME="$home" sh "$script" --env "$work/absent.env" >/dev/null 2>&1) || :
(cd "$work/empty-cwd" && HOME="$home" sh "$script" --env "$work/good.env" >/dev/null 2>&1) || :
after=$(find "$work/empty-cwd" -mindepth 1 | sort)
if [ "$before" != "$after" ]; then
	echo "FAIL: a run created files in an empty cwd: [$before] -> [$after]"
	fail=1
fi

# main: parse, load, print the estimate, stop with exit 2 (phases unimplemented).
sp --env "$work/good.env" --log-mb 20
status 'a valid run stops at exit 2' 2
carries 'main prints the estimate for 20 MiB' "estimated run traffic: $est20 bytes"
carries 'main says the phases are not implemented' 'not implemented'
sp --env "$work/good.env" --log-mb 7
status 'a valid run with --log-mb 7 stops at exit 2' 2
carries 'main prints the estimate for 7 MiB' "estimated run traffic: $est7 bytes"

# The default env path is ~/.config/relevo-r2log-scratch.env and the default
# log size is 20: a no-args run under the temp HOME reads that file.
mkdir -p "$home/.config"
cp "$work/good.env" "$home/.config/relevo-r2log-scratch.env"
sp
status 'a no-args run reads the default env file and stops at exit 2' 2
carries 'the default log size is 20 MiB' "estimated run traffic: $est20 bytes"
rm -f "$home/.config/relevo-r2log-scratch.env"

# --wipe and --dry-run are parsed but make no behavioral distinction yet: the
# estimate and exit are identical, and the notice reports their parsed state.
sp --env "$work/good.env" --log-mb 20 --wipe --dry-run
status '--wipe --dry-run still stops at exit 2' 2
carries '--wipe --dry-run prints the same estimate' "estimated run traffic: $est20 bytes"
carries '--wipe --dry-run still says not implemented' 'not implemented'
carries 'the notice reports wipe parsed' 'wipe=1'
carries 'the notice reports dry-run parsed' 'dry-run=1'
sp --env "$work/good.env" --log-mb 20
carries 'the plain notice reports wipe unset' 'wipe=0'
carries 'the plain notice reports dry-run unset' 'dry-run=0'

# A leading zero is decimal, not octal: 020 is twenty, so its estimate matches
# the estimate for 20.
sp --env "$work/good.env" --log-mb 020
status '--log-mb 020 stops at exit 2' 2
carries '--log-mb 020 is twenty, not octal sixteen' "estimated run traffic: $est20 bytes"

# quota_allows: exactly 95% allows, one byte over refuses, and the floor of a
# non-divisible quota stays exact (101 * 95% = 95.95, so 95 allows, 96 refuses).
src quota_allows 90 100 5
status 'quota: exactly 95% allows' 0
src quota_allows 90 100 6
status 'quota: one byte over 95% refuses' 1
src quota_allows 95 100 0
status 'quota: 95 of 100 with no estimate allows' 0
src quota_allows 96 100 0
status 'quota: 96 of 100 refuses' 1
src quota_allows 90 101 5
status 'quota: a floored 95% boundary allows' 0
src quota_allows 90 101 6
status 'quota: one byte over a floored 95% refuses' 1

# run_estimate: the formula's arithmetic, pinned for 20 and two other values.
src run_estimate 20
equals 'run_estimate 20 is three passes over 20 MiB' "$est20"
src run_estimate 7
equals 'run_estimate 7 is three passes over 7 MiB' "$est7"
src run_estimate 1
equals 'run_estimate 1 is three passes over 1 MiB' "$est1"

# flag_ratio: more than double flags, less than half flags, the exact
# boundaries do not, and a zero prediction flags any positive measurement but
# agrees with zero.
src flag_ratio 21 10
equals 'flag_ratio: more than double flags' 'FLAG'
src flag_ratio 20 10
equals 'flag_ratio: exactly double does not flag' ''
src flag_ratio 4 10
equals 'flag_ratio: less than half flags' 'FLAG'
src flag_ratio 5 10
equals 'flag_ratio: exactly half does not flag' ''
src flag_ratio 10 10
equals 'flag_ratio: agreement does not flag' ''
src flag_ratio 0 0
equals 'flag_ratio: zero predicted and zero measured is empty' ''
src flag_ratio 1 0
equals 'flag_ratio: zero predicted with positive measured flags' 'FLAG'
src flag_ratio 0 10
equals 'flag_ratio: zero measured against positive predicted flags' 'FLAG'

# print_table: the exact header and rows, the flag column driven by flag_ratio.
# The format is the rendering contract; the flag values below are literals, so
# a broken flag wiring changes the rendered bytes and fails the cmp.
printf 'seed\t100\t100\tbytes\nmigrate\t300\t100\tbytes\nwipe\t40\t100\tbytes\n' > "$work/table.in"
{
	printf '%-16s %14s %14s %-6s %s\n' name measured predicted unit flag
	printf '%-16s %14s %14s %-6s %s\n' seed 100 100 bytes ''
	printf '%-16s %14s %14s %-6s %s\n' migrate 300 100 bytes FLAG
	printf '%-16s %14s %14s %-6s %s\n' wipe 40 100 bytes FLAG
} > "$work/table.expected"
# shellcheck disable=SC2016 # the inner sh expands $1, not this shell
SYNC_PROOF_SOURCED=1 sh -c '. "$1"; print_table' sh "$script" < "$work/table.in" > "$work/table.actual" 2> "$work/table.err"
cat "$work/table.actual" "$work/table.err" >> "$all"
if ! cmp -s "$work/table.expected" "$work/table.actual"; then
	echo "FAIL: print_table output differs from the pinned table:"
	diff "$work/table.expected" "$work/table.actual" || :
	fail=1
fi
if [ -s "$work/table.err" ]; then
	echo "FAIL: print_table wrote to stderr: $(cat "$work/table.err")"
	fail=1
fi

# A blank line is skipped, and a final row with no trailing newline is read.
printf 'seed\t100\t100\tbytes\n\nmigrate\t300\t100\tbytes' > "$work/table2.in"
{
	printf '%-16s %14s %14s %-6s %s\n' name measured predicted unit flag
	printf '%-16s %14s %14s %-6s %s\n' seed 100 100 bytes ''
	printf '%-16s %14s %14s %-6s %s\n' migrate 300 100 bytes FLAG
} > "$work/table2.expected"
# shellcheck disable=SC2016 # the inner sh expands $1, not this shell
SYNC_PROOF_SOURCED=1 sh -c '. "$1"; print_table' sh "$script" < "$work/table2.in" > "$work/table2.actual" 2>&1
cat "$work/table2.actual" >> "$all"
if ! cmp -s "$work/table2.expected" "$work/table2.actual"; then
	echo "FAIL: print_table mishandles a blank line or an unterminated final row:"
	diff "$work/table2.expected" "$work/table2.actual" || :
	fail=1
fi

# A name carrying spaces stays one field under the tab IFS.
printf 'my log\t100\t100\tbytes\n' > "$work/table3.in"
# shellcheck disable=SC2016 # the inner sh expands $1, not this shell
SYNC_PROOF_SOURCED=1 sh -c '. "$1"; print_table' sh "$script" < "$work/table3.in" > "$work/table3.actual" 2>&1
cat "$work/table3.actual" >> "$all"
if ! grep -qF 'my log' "$work/table3.actual"; then
	echo "FAIL: print_table split a name carrying spaces: $(cat "$work/table3.actual")"
	fail=1
fi

# The sentinel-leak sweep: no sentinel value reached any output the suite
# captured. This is the pin that load_env names variables and never echoes a
# value, across every refusal and every run above.
for s in "$s_url" "$s_token" "$s_endpoint" "$s_bucket" "$s_keyid" "$s_secret"; do
	if grep -qF -- "$s" "$all"; then
		echo "FAIL: sentinel-leak sweep: \"$s\" appeared in suite output"
		fail=1
	fi
done

[ "$fail" -eq 0 ] && echo "sync-proof: ok"
exit "$fail"
