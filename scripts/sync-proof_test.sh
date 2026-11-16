#!/bin/sh
# scripts/sync-proof_test.sh -- tests for the sync-proof core and preflight.
#
# Every assertion drives scripts/sync-proof.sh with no network, no real relevo
# binary and no sandbox: a fake curl and a fake relevo earlier on PATH answer
# only fixture URLs from canned documents under the suite's temp dir, and the
# fake curl fails the suite if a secret reaches its argv or a non-fixture URL
# is called. The CLI runs under a temp HOME and a temp TMPDIR so the default
# env path and every state dir stay inside the suite; the pure functions run
# in a subshell that sources the script with SYNC_PROOF_SOURCED set, so a
# refusal's exit ends that subshell and not the suite. The fake env files carry
# sentinel secret values; a final sweep asserts no sentinel ever reached any
# output or any recorded argv, which is the pin that a refusal names variables
# and credentials travel only in mode-600 files.
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
script="$here/sync-proof.sh"
shbin=$(command -v sh)
tab=$(printf '\t')

work=$(mktemp -d)
home="$work/home"
mkdir -p "$home" "$work/tmp"
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

fail=0
all="$work/all-output"
: > "$all"

# sp runs the script as a command under the temp HOME, with the fake binaries
# first on PATH and state dirs under the suite's TMPDIR, capturing combined
# output and status. The temp HOME keeps the default env path off the
# developer's real ~/.config; the fake PATH keeps every remote call on
# fixtures, so no assertion depends on the machine or reaches a network.
sp() {
	out=$(HOME="$home" TMPDIR="$work/tmp" PATH="$fakebin:$PATH" sh "$script" "$@" 2>&1) && got=0 || got=$?
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

# srcf runs one function of the sourced core against the fake remote: the
# preflight env sourced, tools resolved and a fresh state dir (with the
# credential files) created by the core's own state_setup under the suite's
# TMPDIR. The function's exit ends the subshell, not the suite.
# shellcheck disable=SC2016 # the inner sh expands $1/$@, not this shell
srcf() {
	out=$(TMPDIR="$work/tmp" PATH="$fakebin:$PATH" SYNC_PROOF_SOURCED=1 sh -c '
		. "$1"; shift
		. "$1"; shift
		resolve_tools
		state_setup
		"$@"
	' sh "$script" "$work/preflight.env" "$@" 2>&1) && got=0 || got=$?
	printf '%s\n' "$out" >> "$all"
}

# srcfi is srcf with the function's stdin read from a file: the ownership and
# wipe functions read keys or table names on stdin.
# shellcheck disable=SC2016 # the inner sh expands $1/$@, not this shell
srcfi() {
	_si_in=$1
	shift
	out=$(TMPDIR="$work/tmp" PATH="$fakebin:$PATH" SYNC_PROOF_SOURCED=1 sh -c '
		. "$1"; shift
		. "$1"; shift
		resolve_tools
		state_setup
		"$@"
	' sh "$script" "$work/preflight.env" "$@" < "$_si_in" 2>&1) && got=0 || got=$?
	printf '%s\n' "$out" >> "$all"
}

# ext runs one JSON extractor function with json_tool forced, reading a fixed
# document: both extractors are pinned against the same verdict lines.
# shellcheck disable=SC2016 # the inner sh expands $1/$2/$3, not this shell
ext() {
	out=$(SYNC_PROOF_SOURCED=1 sh -c '. "$1"; json_tool=$2; "$3"' sh "$script" "$1" "$2" < "$3" 2>&1) && got=0 || got=$?
	printf '%s\n' "$out" >> "$all"
}

# exta is ext for an extractor function that takes one argument (the field name):
# the document still arrives on stdin, the argument after the function.
# shellcheck disable=SC2016 # the inner sh expands $1/$2/$3/$4, not this shell
exta() {
	out=$(SYNC_PROOF_SOURCED=1 sh -c '. "$1"; json_tool=$2; "$3" "$4"' sh "$script" "$1" "$2" "$3" < "$4" 2>&1) && got=0 || got=$?
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
# ordered asserts the given strings appear on distinct output lines in the
# given order: the dry-run verdict sequence is a contract, not a set.
ordered() {
	_od_name=$1
	shift
	_od_prev=0
	for _od_pat in "$@"; do
		_od_ln=$(printf '%s\n' "$out" | grep -nF -- "$_od_pat" | head -n1 | cut -d: -f1)
		if [ -z "$_od_ln" ] || [ "$_od_ln" -le "$_od_prev" ]; then
			echo "FAIL: $_od_name (\"$_od_pat\" missing or out of order in: $out)"
			fail=1
			return
		fi
		_od_prev=$_od_ln
	done
}
# log_has asserts a recorded argv line carries a fixed substring.
log_has() {
	if ! grep -qF -- "$2" "$1"; then
		echo "FAIL: $3 (no \"$2\" in $1)"
		fail=1
	fi
}

# Sentinel values: one per required variable. None may appear in any output the
# suite captures or in any argv the fake curl records; the sweeps at the end
# read them all off $all and the argv log.
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

# The preflight env: the same sentinels in URL shape, so the endpoint
# derivation yields the fixture URLs the fake curl answers.
cat > "$work/preflight.env" <<EOF
RELEVO_SCRATCH_SYNC_URL='libsql://$s_url.invalid'
RELEVO_SCRATCH_SYNC_TOKEN='$s_token'
RELEVO_SCRATCH_R2_ENDPOINT='https://$s_endpoint.invalid'
RELEVO_SCRATCH_R2_BUCKET='$s_bucket'
RELEVO_SCRATCH_R2_KEY_ID='$s_keyid'
RELEVO_SCRATCH_R2_SECRET='$s_secret'
EOF

turso_url="https://$s_url.invalid/v2/pipeline"
bucket_base="https://$s_endpoint.invalid/$s_bucket"
list_url="$bucket_base?list-type=2"

# The fake binaries and their fixtures. reset_fixtures restores the defaults
# between cases; the argv and failure logs accumulate for the final sweeps.
fakebin="$work/fakebin"
mkdir -p "$fakebin"

reset_fixtures() {
	printf '%s\n' '{"enabled":true,"account":{"month":"2026-11","total":1000000000,"machines":1,"own":1000000000,"quota":10000000000,"projected":2000000000,"over_warn":false,"pace_over":false}}' > "$work/status.json"
	printf '0\n' > "$work/relevo-exit"
	printf '%s\n' '{"baton":null,"results":[{"type":"ok","response":{"type":"execute","result":{"cols":[{"name":"name"}],"rows":[]}}},{"type":"ok","response":{"type":"close"}}]}' > "$work/turso-tables.json"
	printf '%s\n' '{"baton":null,"results":[{"type":"ok","response":{"type":"execute","result":{"cols":[{"name":"value"}],"rows":[]}}},{"type":"ok","response":{"type":"close"}}]}' > "$work/turso-marker.json"
	printf '%s\n' '{"results":[{"type":"error","error":{"message":"no such table: meta"}}]}' > "$work/turso-marker-no-meta.json"
	printf '200\n' > "$work/turso-http-code"
	: > "$work/bucket-keys"
	printf 'false\n' > "$work/bucket-truncated"
	printf '200\n' > "$work/bucket-http-code"
	printf '204\n' > "$work/delete-http-code"
	printf '%s\n' '  --aws-sigv4 <provider1[:prvdr2[:reg[:srv]]]>  AWS V4 signature auth' > "$work/curl-help"
	rm -f "$work/deleted-keys" "$work/stubborn-key" "$work/curl-fail" "$work/turso-marker-current" "$work/turso-marker-current.json" "$work/turso-write-err" "$work/turso-write-err.json"
}
reset_fixtures

# The fake curl: records every invocation's argv (tab-joined, one line each),
# fails the suite if a secret sentinel appears on argv or a URL is not a
# fixture URL, checks the credential files are mode 600 and carry the sentinel
# credentials, and answers canned bodies with the fixture HTTP codes. The
# bucket listing is regenerated from bucket-keys minus the keys deleted so
# far, so a wipe's re-list proof runs against a listing that really changed;
# stubborn-key stays listed even after its delete, to exercise the proof's
# refusal. A marker query answers the pipeline error when the tables fixture
# has no meta row, and the current run's recorded id when the marker-current
# control file is set. The write-err control file answers write bodies with
# the error-result fixture, so the real turso_exec can be driven to its
# refusal; the read probes keep their own fixtures either way.
cat > "$fakebin/curl" <<EOF
#!/bin/sh
work='$work'
printf '%s\t' "\$@" >> "\$work/curl-argv"
printf '\n' >> "\$work/curl-argv"
for a in "\$@"; do
	case \$a in
	*$s_token*|*$s_keyid*|*$s_secret*)
		printf 'secret on argv: %s\n' "\$a" >> "\$work/fake-fail"; exit 99 ;;
	esac
done
case " \$* " in
*' --help '*) cat "\$work/curl-help"; exit 0 ;;
esac
[ ! -f "\$work/curl-fail" ] || exit 7
url=''; method='GET'; kfile=''; hfile=''; sigv4=''; prev=''
for a in "\$@"; do
	case \$prev in
	-K) kfile=\$a; prev=''; continue ;;
	-X) method=\$a; prev=''; continue ;;
	-H) hfile=\$a; prev=''; continue ;;
	--aws-sigv4) sigv4=\$a; prev=''; continue ;;
	-w|--data-binary) prev=''; continue ;;
	esac
	case \$a in
	-K|-X|-H|--aws-sigv4|-w|--data-binary) prev=\$a ;;
	-sS|-s|-S) ;;
	-*) printf 'unexpected curl flag: %s\n' "\$a" >> "\$work/fake-fail"; exit 99 ;;
	*) url=\$a ;;
	esac
done
[ -n "\$url" ] || { printf 'no URL in argv\n' >> "\$work/fake-fail"; exit 99; }
check_kfile() {
	[ -n "\$kfile" ] || { printf 'R2 request without -K\n' >> "\$work/fake-fail"; exit 99; }
	[ -f "\$kfile" ] || { printf '-K file missing: %s\n' "\$kfile" >> "\$work/fake-fail"; exit 99; }
	[ "\$(stat -c '%a' "\$kfile")" = 600 ] || { printf '-K file not mode 600\n' >> "\$work/fake-fail"; exit 99; }
	grep -qxF 'user = "$s_keyid:$s_secret"' "\$kfile" || { printf '-K file lacks the key pair\n' >> "\$work/fake-fail"; exit 99; }
	[ "\$sigv4" = 'aws:amz:auto:s3' ] || { printf 'bad --aws-sigv4: %s\n' "\$sigv4" >> "\$work/fake-fail"; exit 99; }
}
case \$url in
'$turso_url')
	[ -n "\$hfile" ] || { printf 'pipeline request without -H\n' >> "\$work/fake-fail"; exit 99; }
	hf=\${hfile#@}
	[ -f "\$hf" ] || { printf '-H file missing\n' >> "\$work/fake-fail"; exit 99; }
	[ "\$(stat -c '%a' "\$hf")" = 600 ] || { printf '-H file not mode 600\n' >> "\$work/fake-fail"; exit 99; }
	grep -qxF 'Authorization: Bearer $s_token' "\$hf" || { printf '-H file lacks the bearer token\n' >> "\$work/fake-fail"; exit 99; }
	[ "\$method" = POST ] || { printf 'pipeline method not POST: %s\n' "\$method" >> "\$work/fake-fail"; exit 99; }
	body=\$(cat)
	printf '%s\n' "\$body" >> "\$work/curl-bodies"
	case \$body in
	*sqlite_master*) fixture="\$work/turso-tables.json" ;;
	*syncproof.run*)
		# No meta row in the tables fixture means no meta table: the real
		# endpoint rejects this query, so answer the pipeline error, not the
		# ok marker fixture.
		if ! grep -q '"value":"meta"' "\$work/turso-tables.json"; then
			fixture="\$work/turso-marker-no-meta.json"
		elif [ -f "\$work/turso-marker-current" ]; then
			# The control file selects the current run's own id as the marker.
			# The -H header file lives in this run's state dir, and the runs
			# file beside it holds the id state_setup recorded for the run.
			printf '{"results":[{"type":"ok","response":{"type":"execute","result":{"rows":[[{"type":"text","value":"%s"}]]}}}]}' "\$(cat "\${hf%/turso-header}/runs")" > "\$work/turso-marker-current.json"
			fixture="\$work/turso-marker-current.json"
		else
			fixture="\$work/turso-marker.json"
		fi
		;;
	*)
		if [ -f "\$work/turso-write-err" ]; then
			fixture="\$work/turso-write-err.json"
		else
			printf 'unexpected pipeline SQL: %s\n' "\$body" >> "\$work/fake-fail"; exit 99
		fi
		;;
	esac
	printf '%s\n%s' "\$(cat "\$fixture")" "\$(cat "\$work/turso-http-code")"
	;;
'$list_url')
	check_kfile
	[ "\$method" = GET ] || { printf 'list method not GET\n' >> "\$work/fake-fail"; exit 99; }
	{
		printf '<?xml version="1.0" encoding="UTF-8"?>\n<ListBucketResult>'
		printf '<IsTruncated>%s</IsTruncated>' "\$(cat "\$work/bucket-truncated")"
		while IFS= read -r k || [ -n "\$k" ]; do
			[ -n "\$k" ] || continue
			keep=1
			if [ -f "\$work/deleted-keys" ] && grep -qxF -- "\$k" "\$work/deleted-keys"; then keep=0; fi
			if [ -f "\$work/stubborn-key" ] && [ "\$k" = "\$(cat "\$work/stubborn-key")" ]; then keep=1; fi
			if [ "\$keep" = 1 ]; then printf '<Contents><Key>%s</Key><Size>1</Size></Contents>' "\$k"; fi
		done < "\$work/bucket-keys"
		printf '</ListBucketResult>'
	} > "\$work/list-body"
	printf '%s\n%s' "\$(cat "\$work/list-body")" "\$(cat "\$work/bucket-http-code")"
	;;
'$bucket_base/'*)
	check_kfile
	[ "\$method" = DELETE ] || { printf 'object method not DELETE: %s\n' "\$method" >> "\$work/fake-fail"; exit 99; }
	key=\${url#'$bucket_base/'}
	printf '%s\n' "\$key" >> "\$work/deleted-keys"
	printf '\n%s' "\$(cat "\$work/delete-http-code")"
	;;
*)
	printf 'non-fixture URL: %s\n' "\$url" >> "\$work/fake-fail"; exit 99 ;;
esac
EOF
chmod +x "$fakebin/curl"

# The fake relevo: answers only `db sync status --json` with the fixture
# document and the fixture exit; anything else fails the suite.
cat > "$fakebin/relevo" <<EOF
#!/bin/sh
work='$work'
printf '%s\t' "\$@" >> "\$work/relevo-argv"
printf '\n' >> "\$work/relevo-argv"
case " \$* " in
*' db sync status --json '*)
	cat "\$work/status.json"
	exit "\$(cat "\$work/relevo-exit")"
	;;
*)
	printf 'unexpected relevo args: %s\n' "\$*" >> "\$work/fake-fail"; exit 99 ;;
esac
EOF
chmod +x "$fakebin/relevo"

# Minimal PATH dirs for the tool-absence refusals: each holds only what the
# script needs to reach the refusal (cat for usage), plus the fakes the
# earlier probes must find.
mkdir -p "$work/min-norelevo" "$work/min-nojson" "$work/min-nocurl"
ln -s "$(command -v cat)" "$work/min-norelevo/cat"
ln -s "$(command -v cat)" "$work/min-nojson/cat"
cp "$fakebin/relevo" "$work/min-nojson/relevo"
ln -s "$(command -v cat)" "$work/min-nocurl/cat"
cp "$fakebin/relevo" "$work/min-nocurl/relevo"
ln -s "$(command -v jq)" "$work/min-nocurl/jq"

# A python3-only PATH (no jq) to drive the whole preflight through the
# fallback extractor: every external the script and the fakes need, symlinked,
# plus the fake curl/relevo and python3, but no jq.
pybin="$work/pybin"
mkdir -p "$pybin"
for tool in date mkdir chmod rm grep sed sort cat stat python3; do
	ln -s "$(command -v "$tool")" "$pybin/$tool"
done
cp "$fakebin/curl" "$pybin/curl"
cp "$fakebin/relevo" "$pybin/relevo"

# The estimate arithmetic, written out rather than copied from output: three
# passes (seed write, migrate read, migrate write) over LOG_MB mebibytes.
est20=$((3 * 20 * 1024 * 1024))
est7=$((3 * 7 * 1024 * 1024))
est1=$((3 * 1 * 1024 * 1024))

# --help is the one invocation that exits 0 without a preflight.
sp --help
status '--help exits 0' 0
carries '--help prints usage' 'usage:'
carries '--help names --log-mb' '--log-mb'
carries '--help says --wipe never waives the Turso refusal' 'waives the Turso'
carries '--help says --wipe never deletes foreign origins' 'foreign origins'

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
# before and after, for both a refused load and a preflight-and-stop main.
mkdir -p "$work/empty-cwd"
before=$(find "$work/empty-cwd" -mindepth 1 | sort)
(cd "$work/empty-cwd" && HOME="$home" TMPDIR="$work/tmp" PATH="$fakebin:$PATH" sh "$script" --env "$work/absent.env" >/dev/null 2>&1) || :
(cd "$work/empty-cwd" && HOME="$home" TMPDIR="$work/tmp" PATH="$fakebin:$PATH" sh "$script" --env "$work/preflight.env" >/dev/null 2>&1) || :
after=$(find "$work/empty-cwd" -mindepth 1 | sort)
if [ "$before" != "$after" ]; then
	echo "FAIL: a run created files in an empty cwd: [$before] -> [$after]"
	fail=1
fi

# main: parse, load, run the read-only preflight against the fakes, then stop
# with exit 2 (the seed/migrate/measure phases are unimplemented).
sp --env "$work/preflight.env" --log-mb 20
status 'a valid preflight run stops at exit 2' 2
carries 'main prints the estimate for 20 MiB' "estimated run traffic: $est20 bytes"
carries 'main says the phases are not implemented' 'not implemented'
sp --env "$work/preflight.env" --log-mb 7
status 'a valid run with --log-mb 7 stops at exit 2' 2
carries 'main prints the estimate for 7 MiB' "estimated run traffic: $est7 bytes"

# The default env path is ~/.config/relevo-r2log-scratch.env and the default
# log size is 20: a no-args run under the temp HOME reads that file.
mkdir -p "$home/.config"
cp "$work/preflight.env" "$home/.config/relevo-r2log-scratch.env"
sp
status 'a no-args run reads the default env file and stops at exit 2' 2
carries 'the default log size is 20 MiB' "estimated run traffic: $est20 bytes"
rm -f "$home/.config/relevo-r2log-scratch.env"

# --wipe on an empty bucket deletes nothing and is reported in the stop notice.
sp --env "$work/preflight.env" --log-mb 20 --wipe
status '--wipe on an empty bucket stops at exit 2' 2
carries '--wipe prints the same estimate' "estimated run traffic: $est20 bytes"
carries 'the notice reports wipe parsed' 'wipe=1'
carries 'the notice reports dry-run unset' 'dry-run=0'
if [ -f "$work/deleted-keys" ]; then
	echo "FAIL: --wipe on an empty bucket deleted something: $(cat "$work/deleted-keys")"
	fail=1
fi
sp --env "$work/preflight.env" --log-mb 20
carries 'the plain notice reports wipe unset' 'wipe=0'

# A leading zero is decimal, not octal: 020 is twenty, so its estimate matches
# the estimate for 20.
sp --env "$work/preflight.env" --log-mb 020
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

# --- the measurement primitives: flags, counters, deltas, listing deltas ---

# row_flag: an n/a measured or predicted figure has no ratio, so the flag
# column stays empty; numeric pairs keep flag_ratio's verdicts.
src row_flag 21 10
equals 'row_flag: a numeric pair keeps flag_ratio (flag)' 'FLAG'
src row_flag 10 10
equals 'row_flag: a numeric pair keeps flag_ratio (agreement)' ''
src row_flag 'n/a (no counter)' 0
equals 'row_flag: a count with no counter does not flag' ''
src row_flag 5 n/a
equals 'row_flag: a prediction with no design figure does not flag' ''
src row_flag n/a n/a
equals 'row_flag: two n/a figures do not flag' ''

# print_table with n/a figures: the rows render with an empty flag column and
# the numeric rows keep their flags; the rendering is pinned byte for byte.
printf 'gets\tn/a (no counter)\tn/a\tcount\nzero\t0\t0\tbytes\nflagged\t300\t100\tbytes\n' > "$work/table-na.in"
{
	printf '%-16s %14s %14s %-6s %s\n' name measured predicted unit flag
	printf '%-16s %14s %14s %-6s %s\n' gets 'n/a (no counter)' n/a count ''
	printf '%-16s %14s %14s %-6s %s\n' zero 0 0 bytes ''
	printf '%-16s %14s %14s %-6s %s\n' flagged 300 100 bytes FLAG
} > "$work/table-na.expected"
# shellcheck disable=SC2016 # the inner sh expands $1, not this shell
SYNC_PROOF_SOURCED=1 sh -c '. "$1"; print_table' sh "$script" < "$work/table-na.in" > "$work/table-na.actual" 2> "$work/table-na.err" && got=0 || got=$?
cat "$work/table-na.actual" "$work/table-na.err" >> "$all"
status 'the n/a table renders without error' 0
if ! cmp -s "$work/table-na.expected" "$work/table-na.actual"; then
	echo "FAIL: print_table mishandles n/a figures:"
	diff "$work/table-na.expected" "$work/table-na.actual" || :
	fail=1
fi
if [ -s "$work/table-na.err" ]; then
	echo "FAIL: print_table wrote to stderr on n/a figures: $(cat "$work/table-na.err")"
	fail=1
fi

# bytes_field: both extractors read the month's counters from a status
# document; an absent bytes object or field is zero, not missing.
printf '%s\n' '{"enabled":true,"bytes":{"turso_push":11,"turso_pull":22,"r2_put":33,"r2_get":44}}' > "$work/bf-full.json"
printf '%s\n' '{"enabled":false}' > "$work/bf-none.json"
printf '%s\n' '{"enabled":true,"bytes":{"turso_push":11}}' > "$work/bf-partial.json"
for tool in jq python3; do
	command -v "$tool" >/dev/null 2>&1 || continue
	exta "$tool" bytes_field turso_push "$work/bf-full.json"
	equals "bytes_field ($tool): turso_push" '11'
	exta "$tool" bytes_field turso_pull "$work/bf-full.json"
	equals "bytes_field ($tool): turso_pull" '22'
	exta "$tool" bytes_field r2_put "$work/bf-full.json"
	equals "bytes_field ($tool): r2_put" '33'
	exta "$tool" bytes_field r2_get "$work/bf-full.json"
	equals "bytes_field ($tool): r2_get" '44'
	exta "$tool" bytes_field turso_pull "$work/bf-none.json"
	equals "bytes_field ($tool): an absent bytes object is zero" '0'
	exta "$tool" bytes_field r2_get "$work/bf-partial.json"
	equals "bytes_field ($tool): an absent field is zero" '0'
done

# counter_delta: the delta arithmetic, including the worker-restart reset
# where the after reading is itself the delta.
src counter_delta 100 150
equals 'counter_delta: a rise is the difference' '50'
src counter_delta 150 100
equals 'counter_delta: a fall is a reset, the after value is the delta' '100'
src counter_delta 100 100
equals 'counter_delta: equal readings moved nothing' '0'
src counter_delta 0 0
equals 'counter_delta: two zeros moved nothing' '0'
src counter_delta 0 5
equals 'counter_delta: a first reading counts from zero' '5'

# status_deltas: the four deltas in ByteCounters' order from canned status
# documents, the reset note on stderr, and the refusals for an empty snapshot
# or a non-counter value.
printf '%s\n' '{"bytes":{"turso_push":100,"turso_pull":200,"r2_put":300,"r2_get":400}}' > "$work/sd-before.json"
printf '%s\n' '{"bytes":{"turso_push":150,"turso_pull":260,"r2_put":300,"r2_get":490}}' > "$work/sd-after.json"
srcf status_deltas sp-x "$work/sd-before.json" "$work/sd-after.json"
status 'status_deltas succeeds' 0
equals 'status_deltas prints the four deltas in order' "$(printf '50\n60\n0\n90')"
printf '%s\n' '{"bytes":{"turso_push":150,"turso_pull":50,"r2_put":300,"r2_get":490}}' > "$work/sd-reset.json"
export SD_BEFORE="$work/sd-before.json" SD_RESET="$work/sd-reset.json" SD_ERR="$work/sd-reset.err"
srcf eval 'status_deltas sp-x "$SD_BEFORE" "$SD_RESET" 2>"$SD_ERR"'
status 'status_deltas succeeds across a reset' 0
equals 'the reset delta is the after value' "$(printf '50\n50\n0\n90')"
log_has "$SD_ERR" 'sp-x turso_pull counter reset (worker restart)' 'the reset is noted on stderr'
printf '%s\n' '{"enabled":false}' > "$work/sd-nobytes.json"
srcf status_deltas sp-x "$work/sd-nobytes.json" "$work/sd-after.json"
equals 'an absent bytes object reads as a zero baseline' "$(printf '150\n260\n300\n490')"
: > "$work/sd-empty.json"
srcf status_deltas sp-x "$work/sd-empty.json" "$work/sd-after.json"
status 'an empty before snapshot is refused' 2
carries 'the empty-snapshot refusal names the before side' 'empty (before)'
srcf status_deltas sp-x "$work/sd-before.json" "$work/sd-empty.json"
status 'an empty after snapshot is refused' 2
carries 'the empty-snapshot refusal names the after side' 'empty (after)'
printf '%s\n' '{"bytes":{"turso_push":"abc"}}' > "$work/sd-bad.json"
srcf status_deltas sp-x "$work/sd-bad.json" "$work/sd-after.json"
status 'a non-counter status value is refused' 2
carries 'the non-counter refusal names the field' 'not counters: turso_push'

# new_segment_keys: the listing delta under one origin, segment keys only.
printf 'origin-a/log/000000000001\norigin-a/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\norigin-b/log/000000000001\n' > "$work/nsk-before"
printf 'origin-a/log/000000000001\norigin-a/log/000000000002\norigin-a/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\norigin-b/log/000000000001\norigin-b/log/000000000002\n' > "$work/nsk-after"
srcf new_segment_keys "$work/nsk-before" "$work/nsk-after" origin-a
status 'new_segment_keys succeeds' 0
equals 'new_segment_keys prints only the new segment keys of the origin' 'origin-a/log/000000000002'
srcf new_segment_keys "$work/nsk-before" "$work/nsk-after" origin-b
equals 'new_segment_keys scopes to the requested origin' 'origin-b/log/000000000002'
srcf new_segment_keys "$work/nsk-after" "$work/nsk-after" origin-a
equals 'an unchanged listing has no new segments' ''
: > "$work/nsk-empty"
srcf new_segment_keys "$work/nsk-empty" "$work/nsk-after" origin-a
equals 'an empty before listing counts every segment as new' "$(printf 'origin-a/log/000000000001\norigin-a/log/000000000002')"
srcf new_segment_keys "$work/nsk-before" "$work/nsk-after" origin-c
equals 'an origin with no objects has no new segments' ''
# A suffix origin must not pass for the requested one: the match is a prefix.
printf 'xorigin-a/log/000000000009\n' > "$work/nsk-suffix"
srcf new_segment_keys "$work/nsk-empty" "$work/nsk-suffix" origin-a
equals 'an origin that is a suffix of another is not the requested one' ''
src count_lines ''
equals 'count_lines of the empty list is zero' '0'
src count_lines "$(printf 'a\nb')"
equals 'count_lines counts the non-empty lines' '2'

# day_entry_names: one line per entry, a 64-digit id and a varied name of the
# modelled length; the last burst's partial count is honoured.
src day_entry_names 1 3
status 'day_entry_names succeeds' 0
if [ "$(printf '%s\n' "$out" | wc -l)" -ne 3 ]; then
	echo "FAIL: day_entry_names printed the wrong line count: $out"
	fail=1
fi
carries 'day_entry_names labels entry 1' 'sync-proof day entry 1 '
carries 'day_entry_names labels entry 3' 'sync-proof day entry 3 '
if ! printf '%s\n' "$out" | awk '{
	id = $1
	if (length(id) != 64 || id ~ /[^0-9]/) { print "bad id: " id; bad = 1 }
	name = substr($0, 66)
	if (length(name) < 200) { print "short name: " length(name); bad = 1 }
	seen[name]++
} END {
	for (n in seen) if (seen[n] > 1) { print "repeated name"; bad = 1 }
	if (bad) exit 1
}'; then
	echo "FAIL: day_entry_names ids or names break the model: $out"
	fail=1
fi
src day_entry_names 4984 17
if [ "$(printf '%s\n' "$out" | wc -l)" -ne 17 ]; then
	echo "FAIL: the last burst printed the wrong line count: $(printf '%s\n' "$out" | wc -l) (want 17)"
	fail=1
fi
carries 'the last burst starts at entry 4984' 'sync-proof day entry 4984 '
carries 'the last burst ends at entry 5000' 'sync-proof day entry 5000 '

# --- preflight: tool resolution ---

# No relevo on PATH: the quota read refuses before anything is created.
out=$(HOME="$home" TMPDIR="$work/tmp" PATH="$work/min-norelevo" "$shbin" "$script" --env "$work/preflight.env" 2>&1) && got=0 || got=$?
printf '%s\n' "$out" >> "$all"
status 'no relevo on PATH is refused' 2
carries 'the no-relevo refusal names relevo' 'relevo'
carries 'the no-relevo refusal names the status verb' 'db sync status --json'

# Neither jq nor python3: the refusal names both.
out=$(HOME="$home" TMPDIR="$work/tmp" PATH="$work/min-nojson" "$shbin" "$script" --env "$work/preflight.env" 2>&1) && got=0 || got=$?
printf '%s\n' "$out" >> "$all"
status 'no JSON extractor is refused' 2
carries 'the no-extractor refusal names jq' 'jq'
carries 'the no-extractor refusal names python3' 'python3'

# No curl: the bucket reads refuse.
out=$(HOME="$home" TMPDIR="$work/tmp" PATH="$work/min-nocurl" "$shbin" "$script" --env "$work/preflight.env" 2>&1) && got=0 || got=$?
printf '%s\n' "$out" >> "$all"
status 'no curl is refused' 2
carries 'the no-curl refusal names curl' 'curl'

# A curl without --aws-sigv4 (pre-7.75): the refusal names the need.
printf '%s\n' '  --user <user:password>  Server user and password' > "$work/curl-help"
sp --env "$work/preflight.env"
status 'a curl without --aws-sigv4 is refused' 2
carries 'the no-sigv4 refusal names --aws-sigv4' '--aws-sigv4'
carries 'the no-sigv4 refusal names the version' '7.75'
reset_fixtures

# --- preflight: the quota read ---

# Each missing field exits 2 naming the field; no projection is guessed.
printf '%s\n' '{"enabled":true}' > "$work/status.json"
sp --env "$work/preflight.env"
status 'a missing account is refused' 2
carries 'the missing-account refusal names account' 'account missing'
printf '%s\n' '{"account":{"projected":5}}' > "$work/status.json"
sp --env "$work/preflight.env"
status 'a missing quota is refused' 2
carries 'the missing-quota refusal names quota' 'account.quota missing'
printf '%s\n' '{"account":{"quota":0,"projected":5}}' > "$work/status.json"
sp --env "$work/preflight.env"
status 'a zero quota is refused' 2
carries 'the zero-quota refusal names quota' 'account.quota missing'
printf '%s\n' '{"account":{"quota":100}}' > "$work/status.json"
sp --env "$work/preflight.env"
status 'a missing projected is refused' 2
carries 'the missing-projected refusal names projected' 'account.projected missing'
carries 'the missing-projected refusal says it will not guess' 'not guessing'
reset_fixtures

# The boundary through main: with --log-mb 1 the estimate is 3145728, and a
# quota of 3311300 has its 95% line at exactly 3145735. Projected 7 lands on
# the line (pass); projected 8 is one byte over (refuse).
printf '%s\n' '{"account":{"quota":3311300,"projected":8}}' > "$work/status.json"
sp --env "$work/preflight.env" --log-mb 1
status 'one byte over the 95% line is refused' 2
carries 'the over-line refusal says quota refusal' 'quota refusal'
carries 'the over-line refusal names the projection' 'projected 8'
carries 'the over-line refusal names the estimate' "estimate $est1"
carries 'the over-line refusal names the quota' 'quota 3311300'
carries 'the over-line refusal names the 95% line' 'line 3145735'
ordered 'the estimate is printed before the refusal' 'estimated run traffic' 'quota refusal'
printf '%s\n' '{"account":{"quota":3311300,"projected":7}}' > "$work/status.json"
sp --env "$work/preflight.env" --log-mb 1
status 'exactly on the 95% line passes the quota check' 2
absent 'the at-line run has no quota refusal' 'quota refusal'
carries 'the at-line run reaches the unimplemented phases' 'not implemented'
reset_fixtures

# A failed status verb and unreadable documents refuse; nothing is guessed.
printf '1\n' > "$work/relevo-exit"
sp --env "$work/preflight.env"
status 'a failed status verb is refused' 2
carries 'the failed-verb refusal names the verb' 'relevo db sync status --json failed'
printf '0\n' > "$work/relevo-exit"
printf '%s\n' '{not json' > "$work/status.json"
sp --env "$work/preflight.env"
status 'an unreadable status document is refused' 2
carries 'the unreadable-document refusal says unreadable' 'unreadable'
: > "$work/status.json"
sp --env "$work/preflight.env"
status 'an empty status output is refused' 2
carries 'the empty-output refusal says printed nothing' 'printed nothing'
reset_fixtures

# quota_fields: both extractors print the same verdict lines for the same
# fixed documents.
printf '%s\n' '{"account":{"quota":3311300,"projected":7}}' > "$work/doc-ok.json"
printf '%s\n' '{"enabled":true}' > "$work/doc-noaccount.json"
printf '%s\n' '{"account":{"projected":5}}' > "$work/doc-noquota.json"
printf '%s\n' '{"account":{"quota":0,"projected":5}}' > "$work/doc-zeroquota.json"
printf '%s\n' '{"account":{"quota":100}}' > "$work/doc-noprojected.json"
for tool in jq python3; do
	command -v "$tool" >/dev/null 2>&1 || continue
	ext "$tool" quota_fields "$work/doc-ok.json"
	equals "quota_fields ($tool): the ok document" 'ok 7 3311300'
	ext "$tool" quota_fields "$work/doc-noaccount.json"
	equals "quota_fields ($tool): no account" 'missing account'
	ext "$tool" quota_fields "$work/doc-noquota.json"
	equals "quota_fields ($tool): no quota" 'missing quota'
	ext "$tool" quota_fields "$work/doc-zeroquota.json"
	equals "quota_fields ($tool): zero quota" 'missing quota'
	ext "$tool" quota_fields "$work/doc-noprojected.json"
	equals "quota_fields ($tool): no projected" 'missing projected'
done

# The whole preflight through the python3 fallback (no jq on PATH): the
# request builder, the quota read and the pipeline rows all run under python3.
if command -v python3 >/dev/null 2>&1; then
	out=$(HOME="$home" TMPDIR="$work/tmp" PATH="$pybin" "$shbin" "$script" --env "$work/preflight.env" 2>&1) && got=0 || got=$?
	printf '%s\n' "$out" >> "$all"
	status 'the python3-only preflight reaches the phases' 2
	carries 'the python3-only run prints the estimate' "estimated run traffic: $est20 bytes"
	carries 'the python3-only run says not implemented' 'not implemented'
	log_has "$work/curl-bodies" '"type":"close"' 'the python3 request body is compact JSON'
	# A non-empty Turso under python3: the rows extract and the refusal names them.
	printf '%s\n' '{"results":[{"type":"ok","response":{"type":"execute","result":{"rows":[[{"type":"text","value":"entries"}]]}}}]}' > "$work/turso-tables.json"
	out=$(HOME="$home" TMPDIR="$work/tmp" PATH="$pybin" "$shbin" "$script" --env "$work/preflight.env" 2>&1) && got=0 || got=$?
	printf '%s\n' "$out" >> "$all"
	status 'the python3-only Turso refusal exits 2' 2
	carries 'the python3-only Turso refusal names the table' 'entries'
	reset_fixtures
fi

# --- preflight: the scratch Turso ---

# The endpoint derivation mirrors the sync engine's: libsql:// and turso://
# become https://, and the pipeline path is appended once.
src turso_endpoint 'libsql://db-org.turso.io'
equals 'turso_endpoint: libsql becomes https pipeline' 'https://db-org.turso.io/v2/pipeline'
src turso_endpoint 'turso://db.turso.io'
equals 'turso_endpoint: turso becomes https pipeline' 'https://db.turso.io/v2/pipeline'
src turso_endpoint 'https://db.turso.io/'
equals 'turso_endpoint: a trailing slash is not doubled' 'https://db.turso.io/v2/pipeline'

# turso_rows: both extractors read the same pipeline documents, verdict first.
printf '%s\n' '{"results":[{"type":"ok","response":{"type":"execute","result":{"rows":[[{"type":"text","value":"meta"}],[{"type":"text","value":"entries"}]]}}}]}' > "$work/pipe-ok.json"
printf '%s\n' '{"results":[{"type":"error","error":{"message":"no such table: x"}}]}' > "$work/pipe-err.json"
for tool in jq python3; do
	command -v "$tool" >/dev/null 2>&1 || continue
	ext "$tool" turso_rows "$work/pipe-ok.json"
	equals "turso_rows ($tool): ok rows after the verdict" "$(printf 'ok\nmeta\nentries')"
	ext "$tool" turso_rows "$work/pipe-err.json"
	equals "turso_rows ($tool): an error result names the message" 'error no such table: x'
done

# scratch_turso_owned: empty (or sqlite-internal-only) is owned; tables need a
# marker matching a recorded run id.
printf '20260101T000000Z-111\n20260102T000000Z-222\n' > "$work/runs-seeded"
: > "$work/tables-empty"
printf 'sqlite_sequence\nsqlite_stat1\n' > "$work/tables-sqlite"
printf 'meta\nentries\n' > "$work/tables-user"
printf 'sqlite_sequence\nmeta\nentries\n' > "$work/tables-mixed"
srcfi "$work/tables-empty" scratch_turso_owned '' "$work/runs-seeded"
status 'scratch_turso_owned: no tables is owned' 0
srcfi "$work/tables-sqlite" scratch_turso_owned '' "$work/runs-seeded"
status 'scratch_turso_owned: sqlite internals are not user tables' 0
srcfi "$work/tables-user" scratch_turso_owned '' "$work/runs-seeded"
status 'scratch_turso_owned: tables without a marker are not owned' 1
srcfi "$work/tables-user" scratch_turso_owned '20260101T000000Z-111' "$work/runs-seeded"
status 'scratch_turso_owned: a marker matching a recorded run id is owned' 0
srcfi "$work/tables-user" scratch_turso_owned '20260102T000000Z-222' "$work/runs-seeded"
status 'scratch_turso_owned: a marker matching the second recorded id is owned' 0
srcfi "$work/tables-user" scratch_turso_owned '20260103T000000Z-333' "$work/runs-seeded"
status 'scratch_turso_owned: an unrecorded marker is not owned' 1
srcfi "$work/tables-user" scratch_turso_owned '20260101T000000Z-111' "$work/runs-absent"
status 'scratch_turso_owned: no runs file is not owned' 1
srcfi "$work/tables-mixed" scratch_turso_owned '20260101T000000Z-111' "$work/runs-seeded"
status 'scratch_turso_owned: internals beside user tables still check the marker' 0

# The runs record the marker check reads, through the srcf path (the core's
# own state_setup): exactly the current run id, as the sole line of the runs
# file. The inner shell expands the state_setup variables, so the assertion
# compares the record against the id this very run was given.
# shellcheck disable=SC2016 # the inner shell expands $runs_file/$run_id, not this shell
srcf eval '[ "$(cat "$runs_file")" = "$run_id" ] && [ "$(wc -l < "$runs_file")" -eq 1 ] && printf runs-record-ok'
status 'the runs-record check succeeds' 0
equals 'state_setup writes exactly the current run id as the sole runs line' 'runs-record-ok'

# Through main: unowned tables refuse naming them, and --wipe does not waive.
printf '%s\n' '{"results":[{"type":"ok","response":{"type":"execute","result":{"rows":[[{"type":"text","value":"meta"}],[{"type":"text","value":"entries"}]]}}}]}' > "$work/turso-tables.json"
sp --env "$work/preflight.env"
status 'unowned Turso tables are refused' 2
carries 'the Turso refusal names the meta table' 'meta'
carries 'the Turso refusal names the entries table' 'entries'
carries 'the Turso refusal says wipe does not waive' 'does not waive'
absent 'the Turso refusal stops before the phases' 'not implemented'
sp --env "$work/preflight.env" --wipe
status '--wipe does not waive the Turso refusal' 2
carries 'the wiped Turso refusal still names the tables' 'entries'
reset_fixtures

# A marker holding this run's own id owns the tables: the tables fixture lists
# meta plus a user table, the bucket is empty, and the marker-current control
# makes the fake answer the marker query with the id this run's state_setup
# just recorded, so main passes the Turso check through the entry point.
printf '%s\n' '{"results":[{"type":"ok","response":{"type":"execute","result":{"rows":[[{"type":"text","value":"meta"}],[{"type":"text","value":"entries"}]]}}}]}' > "$work/turso-tables.json"
touch "$work/turso-marker-current"
sp --env "$work/preflight.env"
status 'a marker-owned Turso passes the preflight' 2
carries 'the marker-owned run reaches the phases' 'not implemented'
absent 'the marker-owned run has no Turso refusal' 'not this script'
reset_fixtures

# A failed probe is exit 2, never an empty database: a non-200 status, a curl
# failure and an error result each refuse before any ownership verdict.
printf '401\n' > "$work/turso-http-code"
sp --env "$work/preflight.env"
status 'a non-200 pipeline status is refused' 2
carries 'the non-200 refusal names the status' 'HTTP 401'
absent 'the non-200 refusal never reaches the phases' 'not implemented'
printf '200\n' > "$work/turso-http-code"
touch "$work/curl-fail"
sp --env "$work/preflight.env"
status 'a curl failure on the pipeline is refused' 2
carries 'the curl-failure refusal names the probe' 'probe failed'
absent 'the curl-failure refusal never reaches the phases' 'not implemented'
rm -f "$work/curl-fail"
printf '%s\n' '{"results":[{"type":"error","error":{"message":"authorization required"}}]}' > "$work/turso-tables.json"
sp --env "$work/preflight.env"
status 'an error result is refused' 2
carries 'the error-result refusal names the server message' 'authorization required'
absent 'the error-result refusal never reaches the phases' 'not implemented'
reset_fixtures

# The pipeline request the fake received: POST, the bearer header file, and
# both statements' bodies recorded.
log_has "$work/curl-argv" "-X${tab}POST" 'the pipeline request is a POST'
log_has "$work/curl-argv" "$turso_url" 'the pipeline request addresses the derived endpoint'
log_has "$work/curl-argv" '-H' 'the pipeline request carries -H'
log_has "$work/curl-bodies" 'sqlite_master' 'the probe statement reads sqlite_master'
log_has "$work/curl-bodies" '"type":"close"' 'the pipeline request closes the connection'

# --- preflight: the bucket ---

# bucket_list: the request shape the fake received, and the keys it answers.
printf 'own1/log/000000000001\nown1/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\nforeign/log/000000000001\n' > "$work/bucket-keys"
srcf bucket_list
status 'bucket_list succeeds against the fake' 0
equals 'bucket_list prints the fixture keys' "$(printf 'own1/log/000000000001\nown1/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\nforeign/log/000000000001')"
log_has "$work/curl-argv" "-X${tab}GET" 'the listing is a GET'
log_has "$work/curl-argv" "$list_url" 'the listing URL carries list-type=2'
log_has "$work/curl-argv" '--aws-sigv4' 'the listing carries --aws-sigv4'
log_has "$work/curl-argv" 'aws:amz:auto:s3' 'the sigv4 scope is aws:amz:auto:s3'
log_has "$work/curl-argv" '-K' 'the listing carries -K'
reset_fixtures

# An empty bucket passes through main; a foreign bucket refuses naming the
# foreign origins, and --wipe waives the refusal but deletes nothing owned
# (a fresh run records no origin yet).
sp --env "$work/preflight.env"
status 'an empty bucket passes the preflight' 2
carries 'the empty-bucket run reaches the phases' 'not implemented'
absent 'the empty-bucket run has no foreign refusal' 'foreign origins'
printf 'foreign/log/000000000001\n' > "$work/bucket-keys"
sp --env "$work/preflight.env"
status 'a foreign bucket is refused' 2
carries 'the foreign refusal names foreign origins' 'foreign origins'
carries 'the foreign refusal names the origin' 'foreign'
absent 'the foreign refusal stops before the phases' 'not implemented'
find "$work/tmp" -mindepth 1 -maxdepth 1 | sort > "$work/wipe-dirs-before"
sp --env "$work/preflight.env" --wipe
status '--wipe waives the foreign refusal' 2
carries 'the wiped foreign run reaches the phases' 'not implemented'
if [ -f "$work/deleted-keys" ]; then
	echo "FAIL: --wipe with no recorded origins deleted something: $(cat "$work/deleted-keys")"
	fail=1
fi
# The wipe run's own state dir is the one new entry under the suite TMPDIR.
# Its origins record must not name the foreign origin the wipe left behind:
# the record is the whole of what this script may touch, so a foreign line
# in it would license deleting the foreign objects.
find "$work/tmp" -mindepth 1 -maxdepth 1 | sort > "$work/wipe-dirs-after"
grep -vxF -f "$work/wipe-dirs-before" "$work/wipe-dirs-after" > "$work/wipe-new-dirs" || :
if [ "$(wc -l < "$work/wipe-new-dirs")" -ne 1 ]; then
	echo "FAIL: the wiped foreign run did not leave exactly one new state dir: $(cat "$work/wipe-new-dirs")"
	fail=1
else
	_wipe_state=$(cat "$work/wipe-new-dirs")
	if [ ! -f "$_wipe_state/origins" ]; then
		echo "FAIL: the wiped foreign run left no origins file in $_wipe_state"
		fail=1
	elif grep -qxF foreign "$_wipe_state/origins"; then
		echo "FAIL: the wiped foreign run recorded the foreign origin as its own: $(cat "$_wipe_state/origins")"
		fail=1
	fi
fi
reset_fixtures

# A truncated listing cannot prove ownership: exit 2, never a partial pass.
printf 'own1/log/000000000001\n' > "$work/bucket-keys"
printf 'true\n' > "$work/bucket-truncated"
sp --env "$work/preflight.env"
status 'a truncated listing is refused' 2
carries 'the truncated refusal names truncation' 'truncated'
reset_fixtures

# A non-200 listing refuses.
printf '403\n' > "$work/bucket-http-code"
sp --env "$work/preflight.env"
status 'a non-200 listing is refused' 2
carries 'the non-200 listing refusal names the status' 'HTTP 403'
reset_fixtures

# key_origins and the whole-line origin match: a prefix of a recorded origin
# is still foreign.
printf 'own/log/000000000001\nownership/log/000000000002\nown/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n' > "$work/keys-mixed"
printf 'own\n' > "$work/origins-seeded"
srcfi "$work/keys-mixed" key_origins
equals 'key_origins prints distinct first segments' "$(printf 'own\nownership')"
srcfi "$work/keys-mixed" bucket_foreign "$work/origins-seeded"
equals 'bucket_foreign: a prefix of a recorded origin is foreign' 'ownership'
# The collision's other direction: a recorded longer origin must not pass a
# shorter lookup -- the match is whole-line, not substring.
printf 'ownership\n' > "$work/origins-long"
printf 'own/log/000000000001\n' > "$work/keys-short"
srcfi "$work/keys-short" bucket_foreign "$work/origins-long"
equals 'bucket_foreign: a shorter origin is not the recorded longer one' 'own'
printf 'own/log/000000000001\n' > "$work/keys-owned"
srcfi "$work/keys-owned" bucket_foreign "$work/origins-seeded"
equals 'bucket_foreign: a recorded origin is owned' ''
srcfi "$work/keys-owned" bucket_foreign "$work/origins-absent"
equals 'bucket_foreign: no record means every origin is foreign' 'own'

# bucket_wipe deletes only owned-origin keys, exactly, and re-lists to prove
# no owned object remains; foreign keys are never deleted.
printf 'own1/log/000000000001\nown1/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\nforeign/log/000000000001\n' > "$work/bucket-keys"
cp "$work/bucket-keys" "$work/keys-wipe"
printf 'own1\n' > "$work/origins-seeded"
srcfi "$work/keys-wipe" bucket_wipe "$work/origins-seeded"
status 'bucket_wipe succeeds' 0
equals 'bucket_wipe prints the remaining foreign keys' 'foreign/log/000000000001'
printf 'own1/log/000000000001\nown1/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef\n' > "$work/deletes-expected"
if ! cmp -s "$work/deletes-expected" "$work/deleted-keys"; then
	echo "FAIL: bucket_wipe deleted the wrong keys:"
	diff "$work/deletes-expected" "$work/deleted-keys" || :
	fail=1
fi
log_has "$work/curl-argv" "-X${tab}DELETE" 'the wipe deletes with DELETE'
log_has "$work/curl-argv" "$bucket_base/own1/log/000000000001" 'the wipe deletes the owned segment key'
log_has "$work/curl-argv" "$bucket_base/own1/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" 'the wipe deletes the owned body key'
if grep -qF "$bucket_base/foreign/" "$work/curl-argv"; then
	echo "FAIL: bucket_wipe addressed a foreign key"
	fail=1
fi
reset_fixtures

# The re-list proof: an owned key that survives its delete refuses the wipe.
printf 'own1/log/000000000001\nforeign/log/000000000001\n' > "$work/bucket-keys"
cp "$work/bucket-keys" "$work/keys-wipe"
printf 'own1/log/000000000001\n' > "$work/stubborn-key"
srcfi "$work/keys-wipe" bucket_wipe "$work/origins-seeded"
status 'a wipe that leaves an owned object behind is refused' 2
carries 'the stubborn-key refusal names the object' 'left an owned object behind'
reset_fixtures

# A delete's 404 is already-gone (the re-list is the proof); a 500 refuses.
printf 'own1/log/000000000001\n' > "$work/bucket-keys"
cp "$work/bucket-keys" "$work/keys-wipe"
printf '404\n' > "$work/delete-http-code"
srcfi "$work/keys-wipe" bucket_wipe "$work/origins-seeded"
status 'a 404 delete still wipes' 0
printf '500\n' > "$work/delete-http-code"
srcfi "$work/keys-wipe" bucket_wipe "$work/origins-seeded"
status 'a 500 delete is refused' 2
carries 'the 500-delete refusal names the status' 'HTTP 500'
reset_fixtures

# An empty record deletes nothing: every key is foreign to the wipe.
printf 'own1/log/000000000001\n' > "$work/bucket-keys"
cp "$work/bucket-keys" "$work/keys-wipe"
: > "$work/origins-empty"
srcfi "$work/keys-wipe" bucket_wipe "$work/origins-empty"
status 'a wipe with an empty record succeeds' 0
equals 'a wipe with an empty record keeps every key' 'own1/log/000000000001'
if [ -f "$work/deleted-keys" ]; then
	echo "FAIL: a wipe with an empty record deleted something"
	fail=1
fi
reset_fixtures

# --- dry-run: verdicts in order, nothing created ---

# The all-owned dry run: env path, estimate, quota, Turso, bucket, in order,
# exit 0, and no state dir or credential file left behind.
before=$(find "$work/tmp" -mindepth 1 -maxdepth 1 | sort)
sp --env "$work/preflight.env" --dry-run
status 'an all-owned dry run exits 0' 0
ordered 'the dry-run verdicts print in order' 'env file:' 'estimated run traffic:' 'quota: allow' 'turso: owned' 'bucket: owned'
carries 'the dry run prints the env file path' "$work/preflight.env"
carries 'the dry run prints the estimate' "estimated run traffic: $est20 bytes"
after=$(find "$work/tmp" -mindepth 1 -maxdepth 1 | sort)
if [ "$before" != "$after" ]; then
	echo "FAIL: a dry run left a state dir: [$before] -> [$after]"
	fail=1
fi

# The all-foreign dry run reports every negative verdict, still exits 0, and
# deletes nothing even with --wipe.
printf '%s\n' '{"account":{"quota":3311300,"projected":8}}' > "$work/status.json"
printf '%s\n' '{"results":[{"type":"ok","response":{"type":"execute","result":{"rows":[[{"type":"text","value":"entries"}]]}}}]}' > "$work/turso-tables.json"
printf 'foreign/log/000000000001\n' > "$work/bucket-keys"
sp --env "$work/preflight.env" --log-mb 1 --wipe --dry-run
status 'an all-foreign dry run exits 0' 0
ordered 'the negative dry-run verdicts print in order' 'env file:' 'estimated run traffic:' 'quota: refuse' 'turso: not owned' 'bucket: not owned'
carries 'the dry-run quota verdict names the projection' 'projected 8'
carries 'the dry-run Turso verdict names the tables' 'entries'
carries 'the dry-run bucket verdict names the foreign origin' 'foreign'
if [ -f "$work/deleted-keys" ]; then
	echo "FAIL: a dry run deleted something"
	fail=1
fi
after=$(find "$work/tmp" -mindepth 1 -maxdepth 1 | sort)
if [ "$before" != "$after" ]; then
	echo "FAIL: a foreign dry run left a state dir: [$before] -> [$after]"
	fail=1
fi
reset_fixtures

# A dry run still refuses a hard error: a missing account has no verdict.
printf '%s\n' '{"enabled":true}' > "$work/status.json"
sp --env "$work/preflight.env" --dry-run
status 'a dry run with a missing account exits 2' 2
carries 'the dry-run hard error names account' 'account missing'
reset_fixtures

# --- the run phases: dry-run plan, seed model, sandbox lifecycle, cleanup ---

# The exact guard line every in-sandbox snippet starts with (docs/env-sandbox.md).
guard_line='test -n "$RELEVO_SANDBOX" || { echo '\''not in a sandbox'\'' >&2; exit 1; }'

# The dry-run command plan: the ordered verb sequence a real run executes, with
# secret sources named but no value present. The verdicts still print first.
before=$(find "$work/tmp" -mindepth 1 -maxdepth 1 | sort)
sp --env "$work/preflight.env" --log-mb 20 --dry-run
status 'the plan dry run exits 0' 0
ordered 'the plan prints creates, enables, pushes, seed, migrate and traffic phases in order' \
	'scripts/relevo-sandbox.sh create sp-a' \
	'scripts/relevo-sandbox.sh create sp-b' \
	'scripts/relevo-sandbox.sh create sp-none' \
	'plan: enable sp-a' \
	'relevo db sync enable --url' \
	'plan: unlock sp-a --new' \
	'relevo db sync unlock --new --passphrase-stdin' \
	'relevo db sync push --json' \
	'plan: enable sp-b' \
	'plan: push sp-b' \
	'plan: enable sp-none' \
	'--r2-key-id "$RELEVO_SCRATCH_R2_KEY_ID" --history none' \
	'plan: push sp-none' \
	'plan: seed format-5' \
	'plan: migrate from sp-a' \
	'relevo db sync migrate --confirm-all-upgraded --json' \
	'plan: next pull on sp-b' \
	'plan: fresh join sp-d' \
	'scripts/relevo-sandbox.sh create sp-d' \
	'plan: push sp-d' \
	'plan: idle poll on sp-a' \
	'plan: compressed day on sp-a' \
	'relevo config set repos.' \
	'plan: day pulls' \
	'plan: measured table'
carries 'the plan carries the guard line' "$guard_line"
carries 'the plan names the token stdin source' 'token: stdin'
carries 'the plan names the R2 secret env source' 'r2.secret: RELEVO_R2_SECRET'
carries 'the plan names the joiner passphrase env source' 'passphrase: RELEVO_SYNC_PASSPHRASE'
carries 'the plan pins the seed batch count for 20 MiB' '41 batches of 500 rows (--log-mb 20)'
carries 'the plan migrate line carries the confirm flag' 'relevo db sync migrate --confirm-all-upgraded --json'
carries 'the plan none enable appends --history none last' '--r2-key-id "$RELEVO_SCRATCH_R2_KEY_ID" --history none'
carries 'the plan pins the idle poll call count' '60 consecutive pulls, no writes between'
carries 'the plan pins the day burst model' '5000 entries in bursts of 33 (one config set per entry, one push per burst)'
carries 'the plan names the table destination' '<state-dir>/table.txt'
absent 'the plan echoes no sentinel token' "$s_token"
absent 'the plan echoes no sentinel secret' "$s_secret"
absent 'the plan echoes no sentinel key id' "$s_keyid"
# The plan is a preview: it creates nothing and leaves no state dir.
after=$(find "$work/tmp" -mindepth 1 -maxdepth 1 | sort)
if [ "$before" != "$after" ]; then
	echo "FAIL: the plan dry run left a state dir: [$before] -> [$after]"
	fail=1
fi
reset_fixtures

# --- the real enable snippets, captured through a recording sb_sh ---

# The enables are pinned as the exact snippets the real functions hand to
# sb_sh: a recording stub stores each invocation's stdin as one numbered
# record (in call order) plus the sandbox name it was addressed to, and
# executes nothing. The preflight env is in force with its sentinel values,
# so a snippet that inlined a secret instead of naming its variable would
# carry a sentinel into the record and fail the absence assertions below.
mkdir -p "$work/enable-capture"
export CAP_DIR="$work/enable-capture"
srcf eval '
sb_sh() {
	_cap_n=$((_cap_n + 1))
	printf "%s\n" "$1" >> "$CAP_DIR/names"
	cat > "$CAP_DIR/snippet-$_cap_n"
}
_cap_n=0
sync_passphrase=capture-passphrase-7e2d
enable_first
enable_joiner sp-b ""
enable_joiner sp-none " --history none"
'
status 'the real enables run against the recording sb_sh' 0
if [ ! -f "$CAP_DIR/snippet-7" ] || [ -f "$CAP_DIR/snippet-8" ]; then
	echo "FAIL: the enables produce exactly seven snippets"
	fail=1
fi
printf 'sp-a\nsp-a\nsp-a\nsp-b\nsp-b\nsp-none\nsp-none\n' > "$work/enable-names-want"
if ! cmp -s "$work/enable-names-want" "$CAP_DIR/names"; then
	echo "FAIL: the enables address sp-a three times, then sp-b and sp-none twice each:"
	diff "$work/enable-names-want" "$CAP_DIR/names" || :
	fail=1
fi

# The expected snippets, written out byte for byte: the guard first, then the
# secret routing (token on stdin, R2 secret via RELEVO_R2_SECRET, passphrase
# unset for the first machine and via RELEVO_SYNC_PASSPHRASE for the joiners)
# and the verb with its flags. sp-none's want is sp-b's with --history none
# appended last, the exact shape the real enable_joiner builds.
cat > "$work/enable-want-1" <<EOF
$guard_line
unset RELEVO_SYNC_PASSPHRASE
export RELEVO_R2_SECRET="\$RELEVO_SCRATCH_R2_SECRET"
printf '%s' "\$RELEVO_SCRATCH_SYNC_TOKEN" | relevo db sync enable --url "\$RELEVO_SCRATCH_SYNC_URL" --token-stdin --r2-endpoint "\$RELEVO_SCRATCH_R2_ENDPOINT" --r2-bucket "\$RELEVO_SCRATCH_R2_BUCKET" --r2-key-id "\$RELEVO_SCRATCH_R2_KEY_ID"
EOF
cat > "$work/enable-want-2" <<EOF
$guard_line
printf '%s' "\$sync_passphrase" | relevo db sync unlock --new --passphrase-stdin
EOF
cat > "$work/enable-want-3" <<EOF
$guard_line
relevo db sync push --json
EOF
cat > "$work/enable-want-4" <<EOF
$guard_line
export RELEVO_R2_SECRET="\$RELEVO_SCRATCH_R2_SECRET"
export RELEVO_SYNC_PASSPHRASE="\$sync_passphrase"
printf '%s' "\$RELEVO_SCRATCH_SYNC_TOKEN" | relevo db sync enable --url "\$RELEVO_SCRATCH_SYNC_URL" --token-stdin --r2-endpoint "\$RELEVO_SCRATCH_R2_ENDPOINT" --r2-bucket "\$RELEVO_SCRATCH_R2_BUCKET" --r2-key-id "\$RELEVO_SCRATCH_R2_KEY_ID"
EOF
sed '$ s/$/ --history none/' "$work/enable-want-4" > "$work/enable-want-6"
cp "$work/enable-want-3" "$work/enable-want-5"
cp "$work/enable-want-3" "$work/enable-want-7"
snippet_is() {
	if ! cmp -s "$work/enable-want-$1" "$CAP_DIR/snippet-$1"; then
		echo "FAIL: $2:"
		diff "$work/enable-want-$1" "$CAP_DIR/snippet-$1" || :
		fail=1
	fi
}
snippet_is 1 'enable_first enables sp-a with the token on stdin, the R2 secret exported and the passphrase unset'
snippet_is 2 'enable_first creates the key with unlock --new'
snippet_is 3 'enable_first drives the first push'
snippet_is 4 'enable_joiner sp-b enables with full history'
snippet_is 5 'enable_joiner sp-b drives its first push'
snippet_is 6 'enable_joiner sp-none appends --history none last'
snippet_is 7 'enable_joiner sp-none drives its first push'
_g_bad=''
for i in 1 2 3 4 5 6 7; do
	[ "$(head -n1 "$CAP_DIR/snippet-$i")" = "$guard_line" ] || _g_bad="$_g_bad $i"
done
if [ -n "$_g_bad" ]; then
	echo "FAIL: every enable snippet starts with the exact guard line (offenders:$_g_bad)"
	fail=1
fi
for i in 1 2 3 4 5 7; do
	if grep -qF -- '--history none' "$CAP_DIR/snippet-$i"; then
		echo "FAIL: --history none appears only in the sp-none enable (found in snippet $i)"
		fail=1
	fi
done
cat "$CAP_DIR"/snippet-* >> "$all"
for s in "$s_url" "$s_token" "$s_endpoint" "$s_bucket" "$s_keyid" "$s_secret"; do
	if grep -qF -- "$s" "$CAP_DIR"/snippet-*; then
		echo "FAIL: the enable snippets carry no sentinel secret value (\"$s\" present)"
		fail=1
	fi
done
if grep -qF -- 'capture-passphrase-7e2d' "$CAP_DIR"/snippet-*; then
	echo "FAIL: the enable snippets never inline the passphrase"
	fail=1
fi

# --- the dry-run plan agrees with the real enable snippets ---

# The plan follows the real run, never the reverse: the enable/unlock/push
# verb lines of the plan's setup section (through the migrate verb) must
# equal, in order, the verbs the real setup enables hand to sb_sh (the
# records above). The fresh join's enable has its own agreement below and
# the day's pushes are pinned by the gen_day_entries capture; the migrate
# verb is pinned by the plan-order assertion.
sp --env "$work/preflight.env" --log-mb 20 --dry-run
status 'the agreement dry run exits 0' 0
printf '%s\n' "$out" > "$work/plan-out"
sed -n '1,/^relevo db sync migrate/p' "$work/plan-out" | grep -E '^relevo db sync (enable|unlock|push) ' > "$work/plan-verbs" || :
: > "$work/real-verbs"
for i in 1 2 3 4 5 6 7; do
	grep -o 'relevo db sync .*' "$CAP_DIR/snippet-$i" >> "$work/real-verbs" || :
done
if ! cmp -s "$work/real-verbs" "$work/plan-verbs"; then
	echo "FAIL: the dry-run plan and the real enable snippets agree on the verb sequence:"
	diff "$work/real-verbs" "$work/plan-verbs" || :
	fail=1
fi
reset_fixtures

# --- run_phases drives the phases in the real order ---

# The real run_phases with every phase stubbed to log its name and arguments:
# the logged order is the run's contract (create, enable sp-a and learn its
# origin, enable the joiners, seed around that origin, migrate, measure). The
# record_new_origins stub answers the origin the seed must receive, so the
# log also pins that the seed's origin is the one learned from the bucket.
export PHASE_LOG="$work/phase-log"
export PHASE_PASS="$work/phase-pass"
: > "$PHASE_LOG"
srcf eval '
gen_passphrase() { printf "gen_passphrase\n" >> "$PHASE_LOG"; printf "phase-passphrase"; }
create_sandboxes() { printf "create_sandboxes\n" >> "$PHASE_LOG"; }
enable_first() { printf "enable_first\n" >> "$PHASE_LOG"; }
enable_joiner() { printf "enable_joiner %s [%s]\n" "$1" "$2" >> "$PHASE_LOG"; }
record_new_origins() { printf "record_new_origins\n" >> "$PHASE_LOG"; printf "origin-from-record\n"; }
seed_log() { printf "seed_log %s %s\n" "$1" "$2" >> "$PHASE_LOG"; }
migrate_run() { printf "migrate_run\n" >> "$PHASE_LOG"; }
measure_phases() { printf "measure_phases\n" >> "$PHASE_LOG"; }
log_mb=20
run_phases
printf "%s" "$sync_passphrase" > "$PHASE_PASS"
'
status 'run_phases drives the phases' 0
carries 'run_phases reports the run complete' 'run complete'
cat > "$work/phase-log-want" <<'EOF'
gen_passphrase
create_sandboxes
enable_first
record_new_origins
enable_joiner sp-b []
record_new_origins
enable_joiner sp-none [ --history none]
record_new_origins
seed_log origin-from-record 20
migrate_run
measure_phases
EOF
if ! cmp -s "$work/phase-log-want" "$PHASE_LOG"; then
	echo "FAIL: run_phases orders create, enables, seed and migrate:"
	diff "$work/phase-log-want" "$PHASE_LOG" || :
	fail=1
fi
if [ "$(cat "$PHASE_PASS" 2>/dev/null)" != phase-passphrase ]; then
	echo "FAIL: run_phases takes the passphrase from gen_passphrase"
	fail=1
fi

# seed_batches: the predicted batch count is the target over the bytes a batch
# carries, rounded up; pinned for 20 and two other sizes.
src seed_batches 20
equals 'seed_batches 20 is 41' '41'
src seed_batches 7
equals 'seed_batches 7 is 15' '15'
src seed_batches 1
equals 'seed_batches 1 is 3' '3'

# seed_body_len: the varying length model, minimum plus row mod spread.
src seed_body_len 1
equals 'seed_body_len 1 is 513' '513'
src seed_body_len 500
equals 'seed_body_len 500 is 1012' '1012'
src seed_body_len 1024
equals 'seed_body_len 1024 wraps to the minimum' '512'

# seed_statements: exactly the format-5 tables (log, marks, meta), the format row
# at 5 and the run marker; no heads or shared table is seeded.
srcf seed_statements
status 'seed_statements succeeds' 0
carries 'seed_statements sets format 5' "VALUES ('format', '5')"
carries 'seed_statements writes the run marker' "VALUES ('syncproof.run',"
carries 'seed_statements creates the log table' 'CREATE TABLE log ('
carries 'seed_statements creates the marks table' 'CREATE TABLE marks ('
carries 'seed_statements creates the meta table' 'CREATE TABLE meta ('
absent 'seed_statements seeds no heads table' 'CREATE TABLE heads'
absent 'seed_statements seeds no shared table' 'CREATE TABLE shared'
# Exactly three CREATE TABLE statements: log, marks, meta.
if [ "$(printf '%s\n' "$out" | grep -c '^CREATE TABLE ')" -ne 3 ]; then
	echo "FAIL: seed_statements did not create exactly three tables: $out"
	fail=1
fi

# seed_batch_sql: one INSERT whose rows match the length model and whose bodies
# are distinct, proven against a real SQLite with no network. The byte total is
# the sum of seed_body_len over the rows, so the generator and the model agree.
if command -v python3 >/dev/null 2>&1 && command -v awk >/dev/null 2>&1; then
	src seed_batch_sql testorigin 1 500
	status 'seed_batch_sql succeeds' 0
	printf '%s\n' "$out" > "$work/batch.sql"
	pyout=$(python3 - "$work/batch.sql" <<'PY'
import sqlite3, sys
sql = open(sys.argv[1]).read()
ddl = "CREATE TABLE log (origin TEXT NOT NULL, seq INTEGER NOT NULL, batch INTEGER NOT NULL, tbl TEXT NOT NULL, pk TEXT NOT NULL, op TEXT NOT NULL, schema_version INTEGER NOT NULL, body TEXT, at TEXT NOT NULL, mac BLOB, after_origin TEXT, after_seq INTEGER, PRIMARY KEY (origin, seq))"
c = sqlite3.connect(":memory:")
c.execute(ddl)
c.execute(sql)
n, s = c.execute("SELECT count(*), sum(length(body)) FROM log").fetchone()
d = c.execute("SELECT count(DISTINCT body) FROM log").fetchone()[0]
expected = sum(512 + (i % 1024) for i in range(1, 501))
print("ok" if (n == 500 and s == expected and d == 500) else "bad rows=%s bytes=%s distinct=%s" % (n, s, d))
PY
	) && pygot=0 || pygot=$?
	printf '%s\n' "$pyout" >> "$all"
	if [ "$pygot" -ne 0 ] || [ "$pyout" != ok ]; then
		echo "FAIL: seed_batch_sql rows/bytes/distinct do not match the model: $pyout"
		fail=1
	fi
fi

# record_new_origins: learns the new first-segment after a push and records it,
# so a second listing of the same bucket learns nothing new.
printf 'neworigin/log/000000000001\n' > "$work/bucket-keys"
srcf eval 'first=$(record_new_origins); second=$(record_new_origins); printf "%s|%s" "$first" "$second"'
status 'record_new_origins succeeds' 0
equals 'record_new_origins learns then remembers' 'neworigin|'
reset_fixtures

# A stub sandbox script records every create/destroy call; the lifecycle and
# cleanup tests drive it instead of the real script (no build, no network).
cat > "$work/fake-sandbox.sh" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$FAKE_SB_LOG"
exit "${FAKE_SB_EXIT:-0}"
EOF
chmod +x "$work/fake-sandbox.sh"
export FAKE_SB="$work/fake-sandbox.sh"
export FAKE_SB_LOG="$work/fake-sandbox-calls"

# create_sandboxes refuses to reuse an existing sandbox of the run's names,
# before creating any.
mkdir -p "$work/reuse-root/sp-a"
export FAKE_ROOT="$work/reuse-root"
: > "$FAKE_SB_LOG"
srcf eval 'sandbox_root=$FAKE_ROOT; sandbox_script=$FAKE_SB; create_sandboxes'
status 'create refuses to reuse an existing sandbox' 2
carries 'the reuse refusal names the sandbox' 'sp-a'
if [ -s "$FAKE_SB_LOG" ]; then
	echo "FAIL: create called the sandbox script despite the reuse refusal: $(cat "$FAKE_SB_LOG")"
	fail=1
fi

# create_sandboxes creates each name from an empty root and records exactly those
# names in the sandboxes file (the whole of what cleanup may destroy).
mkdir -p "$work/fresh-root"
export FAKE_ROOT="$work/fresh-root"
: > "$FAKE_SB_LOG"
srcf eval 'sandbox_root=$FAKE_ROOT; sandbox_script=$FAKE_SB; create_sandboxes; cat "$sandboxes_file"'
status 'create_sandboxes succeeds from an empty root' 0
equals 'create_sandboxes records the three names' "$(printf 'sp-a\nsp-b\nsp-none')"
log_has "$FAKE_SB_LOG" 'create sp-a' 'create_sandboxes creates sp-a'
log_has "$FAKE_SB_LOG" 'create sp-b' 'create_sandboxes creates sp-b'
log_has "$FAKE_SB_LOG" 'create sp-none' 'create_sandboxes creates sp-none'

# Cleanup on failure: a non-zero exit after sandboxes are recorded destroys each
# recorded sandbox. The failing stub verb is the non-zero exit; the trap fires.
export FAKE_ROOT="$work/fresh-root"
: > "$FAKE_SB_LOG"
srcf eval 'sandbox_script=$FAKE_SB; printf "sp-a\nsp-b\nsp-none\n" > "$sandboxes_file"; exit 3'
status 'a failing run exits non-zero' 3
log_has "$FAKE_SB_LOG" 'destroy sp-a' 'cleanup destroys sp-a on failure'
log_has "$FAKE_SB_LOG" 'destroy sp-b' 'cleanup destroys sp-b on failure'
log_has "$FAKE_SB_LOG" 'destroy sp-none' 'cleanup destroys sp-none on failure'

# A successful exit destroys nothing: cleanup only fires the destroys on failure.
: > "$FAKE_SB_LOG"
srcf eval 'sandbox_script=$FAKE_SB; printf "sp-a\n" > "$sandboxes_file"; exit 0'
status 'a successful run exits 0' 0
if [ -s "$FAKE_SB_LOG" ]; then
	echo "FAIL: a successful run destroyed sandboxes: $(cat "$FAKE_SB_LOG")"
	fail=1
fi

# A failed destroy is ignored: the run's own exit status stands, with a note.
export FAKE_SB_EXIT=1
: > "$FAKE_SB_LOG"
srcf eval 'sandbox_script=$FAKE_SB; printf "sp-a\n" > "$sandboxes_file"; exit 3'
status 'a failed destroy does not replace the run status' 3
carries 'the failed destroy is noted' 'cleanup could not destroy sandbox sp-a'
unset FAKE_SB_EXIT

# sb_sh sources the sandbox env.sh then runs the guard-prefixed snippet: with the
# marker set the snippet runs; without it the guard refuses before any command.
mkdir -p "$work/sb-root/sp-x/bin"
printf 'export RELEVO_SANDBOX=sp-x\n' > "$work/sb-root/sp-x/env.sh"
printf '%s\n' "$guard_line" 'echo inside-ok' > "$work/snippet-ok"
out=$(SYNC_PROOF_SOURCED=1 sh -c '. "$1"; sandbox_root=$2; sb_sh sp-x' sh "$script" "$work/sb-root" < "$work/snippet-ok" 2>&1) && got=0 || got=$?
printf '%s\n' "$out" >> "$all"
status 'sb_sh runs the snippet inside the sandbox' 0
carries 'sb_sh reaches the guarded command' 'inside-ok'
# Outside a sandbox the marker is empty (defined, not unset): the guard refuses.
printf 'export RELEVO_SANDBOX=\n' > "$work/sb-root/sp-x/env.sh"
out=$(SYNC_PROOF_SOURCED=1 sh -c '. "$1"; sandbox_root=$2; sb_sh sp-x' sh "$script" "$work/sb-root" < "$work/snippet-ok" 2>&1) && got=0 || got=$?
printf '%s\n' "$out" >> "$all"
status 'sb_sh refuses without the sandbox marker' 1
absent 'the unguarded sandbox never runs the command' 'inside-ok'
carries 'the guard refusal names the sandbox' 'not in a sandbox'
# A marker naming another sandbox: env.sh always sets RELEVO_SANDBOX to the
# sandbox's own name, so a non-empty marker naming a different sandbox means
# the snippet was delivered to the wrong one -- the in-snippet guard alone
# cannot see the mismatch, and sb_sh must refuse before the eval.
printf 'export RELEVO_SANDBOX=other-name\n' > "$work/sb-root/sp-x/env.sh"
out=$(SYNC_PROOF_SOURCED=1 sh -c '. "$1"; sandbox_root=$2; sb_sh sp-x' sh "$script" "$work/sb-root" < "$work/snippet-ok" 2>&1) && got=0 || got=$?
printf '%s\n' "$out" >> "$all"
status 'sb_sh refuses a marker naming another sandbox' 1
absent 'the wrong-name sandbox never runs the command' 'inside-ok'
carries 'the wrong-name refusal keeps the guard wording' 'not in a sandbox'

# migrate_field: both extractors read the same migrate document, booleans render
# lowercase so already_migrated reads the same whichever tool is on PATH.
printf '%s\n' '{"already_migrated":false,"rows":41,"turso_sent_bytes":123,"objects_written":7}' > "$work/migrate-doc.json"
for tool in jq python3; do
	command -v "$tool" >/dev/null 2>&1 || continue
	exta "$tool" migrate_field already_migrated "$work/migrate-doc.json"
	equals "migrate_field ($tool): false renders lowercase" 'false'
	exta "$tool" migrate_field rows "$work/migrate-doc.json"
	equals "migrate_field ($tool): rows" '41'
	exta "$tool" migrate_field turso_sent_bytes "$work/migrate-doc.json"
	equals "migrate_field ($tool): sent bytes" '123'
	exta "$tool" migrate_field absent "$work/migrate-doc.json"
	equals "migrate_field ($tool): an absent field is empty" ''
done
printf '%s\n' '{"already_migrated":true}' > "$work/migrate-true.json"
for tool in jq python3; do
	command -v "$tool" >/dev/null 2>&1 || continue
	exta "$tool" migrate_field already_migrated "$work/migrate-true.json"
	equals "migrate_field ($tool): true renders lowercase" 'true'
done

# turso_exec_verdict: ok only when no result is an error; the first error names
# its message; an empty response is an error, never a silent success.
printf '%s\n' '{"results":[{"type":"ok","response":{"type":"execute","result":{}}},{"type":"ok","response":{"type":"close"}}]}' > "$work/exec-ok.json"
printf '%s\n' '{"results":[{"type":"ok","response":{}},{"type":"error","error":{"message":"boom"}}]}' > "$work/exec-err.json"
printf '%s\n' '{"results":[]}' > "$work/exec-empty.json"
for tool in jq python3; do
	command -v "$tool" >/dev/null 2>&1 || continue
	ext "$tool" turso_exec_verdict "$work/exec-ok.json"
	equals "turso_exec_verdict ($tool): all ok" 'ok'
	ext "$tool" turso_exec_verdict "$work/exec-err.json"
	equals "turso_exec_verdict ($tool): the first error names its message" 'error boom'
	ext "$tool" turso_exec_verdict "$work/exec-empty.json"
	equals "turso_exec_verdict ($tool): an empty response is an error" 'error empty pipeline response'
done

# The real turso_exec against the fake curl, not a stub: the write-err control
# answers the write body with the exec-err shape (one ok result, then an error
# result), and the batch must refuse with exit 2 -- a refused insert halts the
# seed rather than falling back to a smaller unmeasured one. The seed_log
# tests below stub turso_exec out; this one drives the real pipeline path.
cp "$work/exec-err.json" "$work/turso-write-err.json"
touch "$work/turso-write-err"
printf 'INSERT INTO x VALUES (1)\n' > "$work/exec-write.sql"
srcfi "$work/exec-write.sql" turso_exec
status 'turso_exec refuses an error result from the real pipeline path' 2
carries 'the turso_exec refusal names the scratch write' 'scratch Turso write failed:'
carries 'the turso_exec refusal names the server message' 'boom'
log_has "$work/curl-bodies" 'INSERT INTO x VALUES (1)' 'turso_exec sends the statement to the pipeline'
rm -f "$work/turso-write-err"
reset_fixtures

# turso_multi_request: one execute per statement plus a close, SQL preserved.
printf 'CREATE TABLE x (a)\nINSERT INTO x VALUES (1)\n' > "$work/stmts.txt"
for tool in jq python3; do
	command -v "$tool" >/dev/null 2>&1 || continue
	ext "$tool" turso_multi_request "$work/stmts.txt"
	status "turso_multi_request ($tool) succeeds" 0
	carries "turso_multi_request ($tool) carries the first statement" 'CREATE TABLE x (a)'
	carries "turso_multi_request ($tool) carries the second statement" 'INSERT INTO x VALUES (1)'
	carries "turso_multi_request ($tool) closes the connection" '"type":"close"'
done

# seed_log's top-up loop, with the write and the read-back stubbed (no network):
# it stops inside the 5% band, halts on an overshoot, and halts on a read-back
# that is not a number. The write stub drains stdin like the real turso_exec, so
# the batch generator never blocks on a full pipe.
srcf eval 'turso_exec() { cat >/dev/null; }; turso_size() { _n=0; [ -f "$state_dir/sz" ] && _n=$(cat "$state_dir/sz"); _n=$((_n+500000)); printf "%s" "$_n" > "$state_dir/sz"; printf "500 %s" "$_n"; }; seed_log testorigin 1'
status 'seed_log tops up into the band' 0
carries 'seed_log reports the measured bytes' '1000000 body bytes'
srcf eval 'turso_exec() { cat >/dev/null; }; turso_size() { printf "500 2000000"; }; seed_log testorigin 1'
status 'seed_log halts on an overshoot' 2
carries 'the overshoot refusal names the band' 'overshot the 5% band'
srcf eval 'turso_exec() { cat >/dev/null; }; turso_size() { printf "abc def"; }; seed_log testorigin 1'
status 'seed_log halts on a non-number read-back' 2
carries 'the non-number refusal names the read-back' 'not a number'

# migrate_run, with sb_sh stubbed to emit a fixture document (no sandbox, no
# network): already_migrated=true fails loudly, a failed verb refuses, and a
# fresh migration records its bytes and the duration bound.
export MR_DOC='{"already_migrated":true,"rows":0}'
srcf eval 'sb_sh() { printf "%s" "$MR_DOC"; }; migrate_run'
status 'migrate refuses already_migrated=true' 2
carries 'the already-migrated refusal names the seed' 'already_migrated=true'
srcf eval 'sb_sh() { return 1; }; migrate_run'
status 'migrate refuses a failed verb' 2
carries 'the failed-migrate refusal names the verb' 'migrate failed'
export MR_DOC='{"already_migrated":false,"rows":41,"turso_sent_bytes":100,"turso_received_bytes":200,"objects_written":7,"r2_put_bytes":300,"r2_get_bytes":50}'
srcf eval 'sb_sh() { printf "%s" "$MR_DOC"; }; migrate_run'
status 'migrate records a fresh migration' 0
carries 'migrate reports the sent bytes' 'turso_sent_bytes=100'
carries 'migrate reports the received bytes' 'turso_received_bytes=200'
carries 'migrate reports the duration bound' 'bound=1800s'
unset MR_DOC

# --- the traffic phases: duration record, joiner create, day generation ---

# migrate_run records the duration in the migrate document: the verb's own
# document has no duration field, and the table's duration row reads the
# record alone. Both extractors write it; a record that cannot be written
# refuses rather than leaving the table a missing figure.
export MR_DOC='{"already_migrated":false,"rows":41,"turso_sent_bytes":100,"turso_received_bytes":200,"objects_written":7,"r2_put_bytes":300,"r2_get_bytes":50}'
srcf eval 'sb_sh() { printf "%s" "$MR_DOC"; }; migrate_run; cat "$migrate_file"'
status 'migrate_run with the duration record succeeds' 0
carries 'the migrate record carries duration_s' '"duration_s":'
srcf eval 'json_tool=python3; sb_sh() { printf "%s" "$MR_DOC"; }; migrate_run; cat "$migrate_file"'
status 'migrate_run records the duration through python3 too' 0
carries 'the python3 migrate record carries duration_s' '"duration_s":'
unset MR_DOC
srcf eval 'rm -f "$migrate_file"; migrate_add_duration 7'
status 'a duration record that cannot be written is refused' 2
carries 'the duration refusal names the record' 'could not record the duration'

# create_joiner: the mid-run creation refuses reuse before creating, and
# records what it creates (the whole of what cleanup may destroy).
mkdir -p "$work/reuse-root-d/sp-d"
export FAKE_ROOT="$work/reuse-root-d"
: > "$FAKE_SB_LOG"
srcf eval 'sandbox_root=$FAKE_ROOT; sandbox_script=$FAKE_SB; create_joiner sp-d'
status 'create_joiner refuses to reuse an existing sandbox' 2
carries 'the joiner reuse refusal names the sandbox' 'sp-d'
if [ -s "$FAKE_SB_LOG" ]; then
	echo "FAIL: create_joiner called the sandbox script despite the reuse refusal: $(cat "$FAKE_SB_LOG")"
	fail=1
fi
mkdir -p "$work/fresh-root-d"
export FAKE_ROOT="$work/fresh-root-d"
: > "$FAKE_SB_LOG"
srcf eval 'sandbox_root=$FAKE_ROOT; sandbox_script=$FAKE_SB; create_joiner sp-d; cat "$sandboxes_file"'
status 'create_joiner succeeds from an empty root' 0
equals 'create_joiner records the joiner sandbox' 'sp-d'
log_has "$FAKE_SB_LOG" 'create sp-d' 'create_joiner creates sp-d'
# A failed create refuses before anything is recorded: the record append
# follows the create, so a refusal never licenses destroying a sandbox this
# run did not create.
export FAKE_ROOT="$work/fresh-root-d"
export FAKE_SB_EXIT=1
srcf eval 'sandbox_root=$FAKE_ROOT; sandbox_script=$FAKE_SB; create_joiner sp-d2'
status 'a failed joiner create is refused' 2
carries 'the failed-create refusal names the sandbox' 'sandbox create failed for sp-d2'
unset FAKE_SB_EXIT

# The real gen_day_entries against a recording sb_sh: one record per call, in
# call order. The day is 5000 config sets (one shared repos key each) in bursts
# of 33, one push per burst -- 152 pushes, the last burst carrying 17 sets.
mkdir -p "$work/day-capture"
export DAY_CAP="$work/day-capture"
srcf eval '
sb_sh() {
	_dc_n=$((_dc_n + 1))
	IFS= read -r _dc_guard
	IFS= read -r _dc_line
	printf "%s\n%s\n" "$_dc_guard" "$_dc_line" > "$DAY_CAP/snippet-$_dc_n"
	case $_dc_line in
	"relevo config set repos."*)
		printf "SET %s\n" "${_dc_line#relevo config set repos.}" >> "$DAY_CAP/log"
		;;
	"relevo db sync push --json")
		printf "PUSH\n" >> "$DAY_CAP/log"
		;;
	*)
		printf "OTHER\n" >> "$DAY_CAP/log"
		;;
	esac
}
_dc_n=0
gen_day_entries
printf "%s" "$_dc_n" > "$DAY_CAP/count"
'
status 'gen_day_entries runs against the recording sb_sh' 0
if [ "$(cat "$DAY_CAP/count")" -ne 5152 ]; then
	echo "FAIL: gen_day_entries made the wrong number of calls: $(cat "$DAY_CAP/count") (want 5152 = 5000 sets + 152 pushes)"
	fail=1
fi
if [ "$(grep -c '^SET ' "$DAY_CAP/log")" -ne 5000 ]; then
	echo "FAIL: gen_day_entries made the wrong number of config sets: $(grep -c '^SET ' "$DAY_CAP/log") (want 5000)"
	fail=1
fi
if [ "$(grep -c '^PUSH$' "$DAY_CAP/log")" -ne 152 ]; then
	echo "FAIL: gen_day_entries made the wrong number of pushes: $(grep -c '^PUSH$' "$DAY_CAP/log") (want 152)"
	fail=1
fi
if grep -q '^OTHER' "$DAY_CAP/log"; then
	echo "FAIL: gen_day_entries emitted an unexpected verb"
	fail=1
fi
# The burst order: 33 sets then a push, 151 times, then 17 sets and a push.
if ! awk '
/^SET / { run++; next }
/^PUSH$/ {
	if (bursts < 151 && run != 33) { print "burst " bursts + 1 " carried " run " sets"; bad = 1 }
	if (bursts == 151 && run != 17) { print "last burst carried " run " sets"; bad = 1 }
	bursts++; run = 0; next
}
END { if (bursts != 152 || run != 0) bad = 1; if (bad) exit 1 }
' "$DAY_CAP/log"; then
	echo "FAIL: gen_day_entries burst order breaks the model"
	fail=1
fi
# The ids run 1..5000 in order, each a 64-digit zero-padded number.
if ! grep '^SET ' "$DAY_CAP/log" | awk '{
	n++
	id = $2
	if (length(id) != 64 || id ~ /[^0-9]/ || id + 0 != n) { print "bad id at " n ": " id; bad = 1 }
} END { if (n != 5000 || bad) exit 1 }'; then
	echo "FAIL: gen_day_entries ids are not the sequential 1..5000"
	fail=1
fi
# Every day snippet starts with the exact guard line.
if ! awk -v g="$guard_line" 'FNR == 1 && $0 != g { print FILENAME; bad = 1 } END { if (bad) exit 1 }' "$DAY_CAP"/snippet-* > "$work/day-guard-bad"; then
	echo "FAIL: every day snippet starts with the exact guard line: $(cat "$work/day-guard-bad")"
	fail=1
fi
# One set snippet, byte for byte: the guard, then the config set with the
# entry's id and name-only repos value the generator printed.
src day_entry_names 1 1
_dn_id=${out%% *}
_dn_name=${out#* }
{
	printf '%s\n' "$guard_line"
	printf 'relevo config set repos.%s '\''{"name":"%s"}'\''\n' "$_dn_id" "$_dn_name"
} > "$work/day-want-1"
if ! cmp -s "$work/day-want-1" "$DAY_CAP/snippet-1"; then
	echo "FAIL: the first day set snippet matches the generator's entry byte for byte:"
	diff "$work/day-want-1" "$DAY_CAP/snippet-1" || :
	fail=1
fi
# The burst's push snippet is the guard and the push verb, nothing else.
if ! cmp -s "$work/enable-want-3" "$DAY_CAP/snippet-34"; then
	echo "FAIL: the day push snippet is the guard and the push verb:"
	diff "$work/enable-want-3" "$DAY_CAP/snippet-34" || :
	fail=1
fi
cat "$DAY_CAP"/snippet-* >> "$all"
for s in "$s_url" "$s_token" "$s_endpoint" "$s_bucket" "$s_keyid" "$s_secret"; do
	if grep -qF -- "$s" "$DAY_CAP"/snippet-*; then
		echo "FAIL: the day snippets carry no sentinel secret value (\"$s\" present)"
		fail=1
	fi
done

# --- the fresh join's enable, captured and agreed with the plan ---

# The fresh join enables sp-d full: the same snippet shape as sp-b's enable
# (no --history), then the first push. The plan's fresh-join section must
# agree with these records verb for verb.
mkdir -p "$work/join-capture"
export JOIN_CAP="$work/join-capture"
srcf eval '
sb_sh() {
	_jc_n=$((_jc_n + 1))
	printf "%s\n" "$1" >> "$JOIN_CAP/names"
	cat > "$JOIN_CAP/snippet-$_jc_n"
}
_jc_n=0
sync_passphrase=capture-passphrase-7e2d
enable_joiner sp-d ""
'
status 'the fresh join enable runs against the recording sb_sh' 0
if [ ! -f "$JOIN_CAP/snippet-2" ] || [ -f "$JOIN_CAP/snippet-3" ]; then
	echo "FAIL: the fresh join enable produces exactly two snippets"
	fail=1
fi
printf 'sp-d\nsp-d\n' > "$work/join-names-want"
if ! cmp -s "$work/join-names-want" "$JOIN_CAP/names"; then
	echo "FAIL: the fresh join enable addresses sp-d twice:"
	diff "$work/join-names-want" "$JOIN_CAP/names" || :
	fail=1
fi
cp "$work/enable-want-4" "$work/join-want-1"
cp "$work/enable-want-3" "$work/join-want-2"
if ! cmp -s "$work/join-want-1" "$JOIN_CAP/snippet-1"; then
	echo "FAIL: the fresh join enables sp-d with full history:"
	diff "$work/join-want-1" "$JOIN_CAP/snippet-1" || :
	fail=1
fi
if ! cmp -s "$work/join-want-2" "$JOIN_CAP/snippet-2"; then
	echo "FAIL: the fresh join drives its first push:"
	diff "$work/join-want-2" "$JOIN_CAP/snippet-2" || :
	fail=1
fi
cat "$JOIN_CAP"/snippet-* >> "$all"
for s in "$s_url" "$s_token" "$s_endpoint" "$s_bucket" "$s_keyid" "$s_secret"; do
	if grep -qF -- "$s" "$JOIN_CAP"/snippet-*; then
		echo "FAIL: the fresh join snippets carry no sentinel secret value (\"$s\" present)"
		fail=1
	fi
done
if grep -qF -- 'capture-passphrase-7e2d' "$JOIN_CAP"/snippet-*; then
	echo "FAIL: the fresh join snippets never inline the passphrase"
	fail=1
fi
sp --env "$work/preflight.env" --log-mb 20 --dry-run
status 'the fresh-join agreement dry run exits 0' 0
printf '%s\n' "$out" | sed -n '/^plan: fresh join sp-d/,/^plan: idle poll/p' | grep -E '^relevo db sync (enable|unlock|push) ' > "$work/join-plan-verbs" || :
: > "$work/join-real-verbs"
for i in 1 2; do
	grep -o 'relevo db sync .*' "$JOIN_CAP/snippet-$i" >> "$work/join-real-verbs" || :
done
if ! cmp -s "$work/join-real-verbs" "$work/join-plan-verbs"; then
	echo "FAIL: the dry-run plan and the real fresh join enable agree on the verb sequence:"
	diff "$work/join-real-verbs" "$work/join-plan-verbs" || :
	fail=1
fi
reset_fixtures

# An origin the bucket never names refuses the run: the listing deltas count
# segments per origin, so an unknown origin would measure nothing silently.
# The call counter lives in a file: record_new_origins runs in a command
# substitution, so a shell variable would reset on every call.
export EO_EMPTY_AT=2
export EO_COUNT="$work/eo-count"
rm -f "$EO_COUNT"
srcf eval '
gen_passphrase() { printf "p"; }
create_sandboxes() { :; }
enable_first() { :; }
enable_joiner() { :; }
seed_log() { :; }
migrate_run() { :; }
measure_phases() { :; }
record_new_origins() {
	_eo_n=$(cat "$EO_COUNT" 2>/dev/null || printf 0)
	_eo_n=$((_eo_n + 1))
	printf "%s" "$_eo_n" > "$EO_COUNT"
	if [ "$_eo_n" = "$EO_EMPTY_AT" ]; then return 0; fi
	printf "origin-%s\n" "$_eo_n"
}
log_mb=20
run_phases
'
status 'an empty sp-b origin refuses the run' 2
carries 'the sp-b origin refusal names the machine' 'could not learn sp-b origin'
export EO_EMPTY_AT=3
rm -f "$EO_COUNT"
srcf eval '
gen_passphrase() { printf "p"; }
create_sandboxes() { :; }
enable_first() { :; }
enable_joiner() { :; }
seed_log() { :; }
migrate_run() { :; }
measure_phases() { :; }
record_new_origins() {
	_eo_n=$(cat "$EO_COUNT" 2>/dev/null || printf 0)
	_eo_n=$((_eo_n + 1))
	printf "%s" "$_eo_n" > "$EO_COUNT"
	if [ "$_eo_n" = "$EO_EMPTY_AT" ]; then return 0; fi
	printf "origin-%s\n" "$_eo_n"
}
log_mb=20
run_phases
'
status 'an empty sp-none origin refuses the run' 2
carries 'the sp-none origin refusal names the machine' 'could not learn sp-none origin'
srcf eval '
create_joiner() { :; }
snap() { :; }
bucket_list() { :; }
enable_joiner() { :; }
record_new_origins() { :; }
log_mb=20
phase_fresh_join
'
status 'an empty sp-d origin refuses the fresh join' 2
carries 'the sp-d origin refusal names the join' 'could not learn sp-d origin'

# A migrate record without the duration (or with a non-numeric figure) refuses
# the table rather than printing a row the flag arithmetic cannot read.
srcf eval 'measure_setup; printf "%s" "{\"rows\":1}" > "$migrate_file"; phase_migrate_rows'
status 'a migrate record without a duration is refused' 2
carries 'the broken-record refusal names the figure' 'non-numeric figure'

# --- the measured run: the full phase list against canned verbs ---

# The real run_phases and measure_phases with the setup phases stubbed and
# sb_sh answering canned verbs: status snapshots come from per-machine queues
# (one document per snap, in call order), a push lands one new segment under
# sp-a's origin, pulls and config sets are logged. The table is the
# deliverable: every required row, the flag verdicts, the reset note, the call
# counts, and table.txt agreeing with stdout. Every snippet is two lines (the
# guard and the verb), so the stub reads them without a subprocess and checks
# the guard itself.
export MEAS_DIR="$work/meas"
export BUCKET_KEYS="$work/bucket-keys"
export GUARD_LINE="$guard_line"
meas_body='
gen_passphrase() { printf "meas-pass"; }
create_sandboxes() { printf "create_sandboxes\n" >> "$MEAS_DIR/phases"; }
create_joiner() { printf "create_joiner %s\n" "$1" >> "$MEAS_DIR/phases"; }
enable_first() { printf "enable_first\n" >> "$MEAS_DIR/phases"; }
enable_joiner() { printf "enable_joiner %s [%s]\n" "$1" "$2" >> "$MEAS_DIR/phases"; }
seed_log() { printf "seed_log %s %s\n" "$1" "$2" >> "$MEAS_DIR/phases"; }
migrate_run() { printf "migrate_run\n" >> "$MEAS_DIR/phases"; printf "%s" "$MIG_DOC" > "$migrate_file"; }
record_new_origins() {
	_mn=$(cat "$MEAS_DIR/rn-n" 2>/dev/null || printf 0)
	_mn=$((_mn + 1))
	printf "%s" "$_mn" > "$MEAS_DIR/rn-n"
	printf "record_new_origins\n" >> "$MEAS_DIR/phases"
	case $_mn in
	1) printf "origin-a\n" ;;
	2) printf "origin-b\n" ;;
	3) printf "origin-none\n" ;;
	4) printf "origin-d\n" ;;
	esac
}
sb_sh() {
	_ms_name=$1
	IFS= read -r _ms_guard
	IFS= read -r _ms_line
	if [ "$_ms_guard" != "$GUARD_LINE" ]; then
		printf "bad guard for %s\n" "$_ms_name" >> "$MEAS_DIR/fail"
		return 1
	fi
	printf "%s\n%s\n" "$_ms_guard" "$_ms_line" >> "$MEAS_DIR/snippets"
	case $_ms_line in
	"relevo db sync status --json")
		_mq=$(cat "$MEAS_DIR/$_ms_name-n" 2>/dev/null || printf 0)
		_mq=$((_mq + 1))
		printf "%s" "$_mq" > "$MEAS_DIR/$_ms_name-n"
		cat "$MEAS_DIR/$_ms_name-status-$_mq"
		;;
	"relevo db sync pull --json")
		printf "PULL %s\n" "$_ms_name" >> "$MEAS_DIR/verbs"
		printf "{\"ok\":true}\n"
		;;
	"relevo db sync push --json")
		_mp=$(cat "$MEAS_DIR/push-n" 2>/dev/null || printf 0)
		_mp=$((_mp + 1))
		printf "%s" "$_mp" > "$MEAS_DIR/push-n"
		printf "origin-a/log/%012d\n" "$_mp" >> "$BUCKET_KEYS"
		printf "PUSH %s\n" "$_ms_name" >> "$MEAS_DIR/verbs"
		printf "{\"ok\":true}\n"
		;;
	"relevo config set repos."*)
		printf "SET %s\n" "$_ms_name" >> "$MEAS_DIR/verbs"
		;;
	*)
		printf "unexpected snippet for %s\n" "$_ms_name" >> "$MEAS_DIR/fail"
		return 1
		;;
	esac
}
log_mb=20
run_phases
'
# meas_row PATTERN sets out to the first table row whose name matches, so the
# flag and value assertions read one row, not the whole table.
meas_row() {
	out=$(printf '%s\n' "$_meas_out" | grep -E "^$1" | head -n1)
}

# Scenario A: a 3x day total flags, a 1.5x duration does not, the idle poll
# resets its pull counter (the after value is the delta, noted), the none
# machine moves no R2 byte, and the fresh join pulls far under half the seed
# (the dropped log did not ship, agreeing with the prediction).
reset_fixtures
rm -rf "$MEAS_DIR"
mkdir -p "$MEAS_DIR"
export MIG_DOC='{"already_migrated":false,"rows":20480,"turso_sent_bytes":21000000,"turso_received_bytes":21000000,"objects_written":80,"r2_put_bytes":20000000,"r2_get_bytes":1000,"duration_s":2700}'
printf '%s\n' '{"enabled":true,"bytes":{"turso_push":2000000,"turso_pull":5000,"r2_put":800000,"r2_get":100}}' > "$MEAS_DIR/sp-a-status-1"
printf '%s\n' '{"enabled":true,"bytes":{"turso_push":2000000,"turso_pull":2000,"r2_put":800000,"r2_get":100}}' > "$MEAS_DIR/sp-a-status-2"
printf '%s\n' '{"enabled":true,"bytes":{"turso_push":2000000,"turso_pull":2000,"r2_put":800000,"r2_get":100}}' > "$MEAS_DIR/sp-a-status-3"
printf '%s\n' '{"enabled":true,"bytes":{"turso_push":5000000,"turso_pull":2000,"r2_put":2400000,"r2_get":100}}' > "$MEAS_DIR/sp-a-status-4"
printf '%s\n' '{"enabled":true,"bytes":{"turso_push":100,"turso_pull":1000,"r2_put":0,"r2_get":500}}' > "$MEAS_DIR/sp-b-status-1"
printf '%s\n' '{"enabled":true,"bytes":{"turso_push":100,"turso_pull":1500,"r2_put":0,"r2_get":900}}' > "$MEAS_DIR/sp-b-status-2"
printf '%s\n' '{"enabled":true,"bytes":{"turso_push":100,"turso_pull":1500,"r2_put":0,"r2_get":900}}' > "$MEAS_DIR/sp-b-status-3"
printf '%s\n' '{"enabled":true,"bytes":{"turso_push":100,"turso_pull":1001500,"r2_put":0,"r2_get":5900900}}' > "$MEAS_DIR/sp-b-status-4"
printf '%s\n' '{"enabled":true,"bytes":{"turso_push":700,"turso_pull":3000,"r2_put":0,"r2_get":0}}' > "$MEAS_DIR/sp-none-status-1"
printf '%s\n' '{"enabled":true,"bytes":{"turso_push":700,"turso_pull":300300,"r2_put":0,"r2_get":0}}' > "$MEAS_DIR/sp-none-status-2"
printf '%s\n' '{"enabled":false}' > "$MEAS_DIR/sp-d-status-1"
printf '%s\n' '{"enabled":true,"bytes":{"turso_push":5000,"turso_pull":100000,"r2_put":30000,"r2_get":20000000}}' > "$MEAS_DIR/sp-d-status-2"
find "$work/tmp" -mindepth 1 -maxdepth 1 | sort > "$work/meas-dirs-before"
srcf eval "$meas_body"
status 'the measured run exits 0' 0
carries 'the measured run reports the run complete' 'run complete'
_meas_out=$out
if [ -s "$MEAS_DIR/fail" ]; then
	echo "FAIL: the measured run's stub rejected a snippet: $(cat "$MEAS_DIR/fail")"
	fail=1
fi
# Every required row name is present in the printed table.
for _mr_name in 'migrate sent' 'migrate received' 'drop-push' 'migrate duration' 'migrate puts' \
	'next pull turso' 'next pull r2get' 'next pull gets' 'next pull lists' 'next pull puts' \
	'fresh join turso' 'fresh join r2get' 'fresh join time' 'fresh join gets' 'fresh join lists' 'fresh join puts' \
	'join log shipped' \
	'idle poll turso' 'idle poll r2' 'idle poll r2 ops' 'idle poll/call' 'idle poll gets' 'idle poll lists' 'idle poll puts' \
	'day segments' \
	'day a push' 'day a pull' 'day a r2put' 'day a r2get' 'day a gets' 'day a lists' 'day a puts' \
	'day b push' 'day b pull' 'day b r2put' 'day b r2get' 'day b gets' 'day b lists' 'day b puts' 'day b total' \
	'day none push' 'day none pull' 'day none r2put' 'day none r2get' 'day none gets' 'day none lists' 'day none puts' \
	'none zero get' 'none zero reads'; do
	if ! printf '%s\n' "$_meas_out" | grep -q "^$_mr_name"; then
		echo "FAIL: the measured table lacks the row: $_mr_name"
		fail=1
	fi
done
out=$_meas_out
ordered 'the measured table rows follow the phase order' \
	'migrate sent' 'next pull turso' 'fresh join turso' 'idle poll turso' 'day segments' 'day a push' 'day b total' 'none zero get'
# The DROP-push wording travels with the table.
carries 'the table carries the DROP-push wording' 'the DROP TABLE log push is inside the commit transaction'
carries 'the DROP-push wording names the shared counter' 'shares the call'\''s one turso_sent_bytes delta'
carries 'the DROP-push wording says no separate counter exists' 'no separate counter exists'
# The flag verdicts: 3x flags, 1.5x does not, zeros agree.
meas_row 'day b total'
carries 'the 3x day total flags' 'FLAG'
carries 'the 3x day total measures 6900000' '6900000'
meas_row 'migrate duration'
absent 'the 1.5x migrate duration does not flag' 'FLAG'
carries 'the migrate duration measures 2700' '2700'
carries 'the migrate duration predicts the 30-minute bound' '1800'
meas_row 'day segments'
absent 'the ~1x segment count does not flag' 'FLAG'
carries 'the segment count is the listing delta (152 pushes)' '152'
meas_row 'none zero get'
absent 'the zero none-get does not flag' 'FLAG'
meas_row 'idle poll r2 +[0-9]'
absent 'the idle R2 bytes at zero do not flag' 'FLAG'
meas_row 'join log shipped'
absent 'the does-not-ship verdict agrees with the prediction' 'FLAG'
carries 'the verdict measures 0 (did not ship)' ' 0 '
# The measured values and the reset note.
meas_row 'next pull turso'
carries 'the next pull measures the turso delta 500' '500'
meas_row 'next pull r2get'
carries 'the next pull measures the r2 get delta 400' '400'
meas_row 'fresh join r2get'
carries 'the fresh join history ships as R2 get bytes' '20000000'
meas_row 'idle poll turso'
carries 'the reset idle total is the after value' '2000'
meas_row 'idle poll/call'
carries 'the per-call cost floors the total over 60' '33'
out=$_meas_out
carries 'the reset is noted for sp-a turso_pull' 'sp-a turso_pull counter reset (worker restart)'
# The call counts: 60 idle pulls, 5000 sets, 152 pushes, b pulls twice (next
# pull and day), sp-none once (day).
if [ "$(grep -c '^PULL sp-a$' "$MEAS_DIR/verbs")" -ne 60 ]; then
	echo "FAIL: the idle poll made the wrong number of pulls: $(grep -c '^PULL sp-a$' "$MEAS_DIR/verbs") (want 60)"
	fail=1
fi
if [ "$(grep -c '^SET sp-a$' "$MEAS_DIR/verbs")" -ne 5000 ]; then
	echo "FAIL: the day made the wrong number of config sets: $(grep -c '^SET sp-a$' "$MEAS_DIR/verbs") (want 5000)"
	fail=1
fi
if [ "$(grep -c '^PUSH sp-a$' "$MEAS_DIR/verbs")" -ne 152 ]; then
	echo "FAIL: the day made the wrong number of pushes: $(grep -c '^PUSH sp-a$' "$MEAS_DIR/verbs") (want 152)"
	fail=1
fi
if [ "$(grep -c '^PULL sp-b$' "$MEAS_DIR/verbs")" -ne 2 ]; then
	echo "FAIL: sp-b pulled the wrong number of times: $(grep -c '^PULL sp-b$' "$MEAS_DIR/verbs") (want 2)"
	fail=1
fi
if [ "$(grep -c '^PULL sp-none$' "$MEAS_DIR/verbs")" -ne 1 ]; then
	echo "FAIL: sp-none pulled the wrong number of times: $(grep -c '^PULL sp-none$' "$MEAS_DIR/verbs") (want 1)"
	fail=1
fi
# table.txt agrees with the printed table (stdout minus the run's stderr
# notices), and no sentinel reached either.
find "$work/tmp" -mindepth 1 -maxdepth 1 | sort > "$work/meas-dirs-after"
grep -vxF -f "$work/meas-dirs-before" "$work/meas-dirs-after" > "$work/meas-new-dirs" || :
if [ "$(wc -l < "$work/meas-new-dirs")" -ne 1 ]; then
	echo "FAIL: the measured run did not leave exactly one new state dir: $(cat "$work/meas-new-dirs")"
	fail=1
else
	_meas_state=$(cat "$work/meas-new-dirs")
	if [ ! -f "$_meas_state/table.txt" ]; then
		echo "FAIL: the measured run left no table.txt in $_meas_state"
		fail=1
	else
		printf '%s\n' "$_meas_out" | grep -v '^sync-proof.sh: ' > "$work/meas-stdout-table"
		if ! cmp -s "$work/meas-stdout-table" "$_meas_state/table.txt"; then
			echo "FAIL: table.txt differs from the printed table:"
			diff "$work/meas-stdout-table" "$_meas_state/table.txt" || :
			fail=1
		fi
		for s in "$s_url" "$s_token" "$s_endpoint" "$s_bucket" "$s_keyid" "$s_secret"; do
			if grep -qF -- "$s" "$_meas_state/table.txt"; then
				echo "FAIL: a sentinel secret reached table.txt: \"$s\""
				fail=1
			fi
		done
	fi
fi
out=$_meas_out
for s in "$s_url" "$s_token" "$s_endpoint" "$s_bucket" "$s_keyid" "$s_secret"; do
	absent "the measured stdout carries no sentinel secret ($s)" "$s"
done
cat "$MEAS_DIR/snippets" >> "$all"
mkdir -p "$work/meas-a-queues"
cp "$MEAS_DIR"/sp-*-status-* "$work/meas-a-queues/"

# Scenario B: the flag directions -- any idle R2 movement flags, a non-zero
# none-get flags, a join pulling exactly half the seed ships (the boundary is
# under-half, not half-or-under) and flags against the does-not-ship
# prediction, a 3x duration flags while a 1.5x day total does not.
reset_fixtures
rm -rf "$MEAS_DIR"
mkdir -p "$MEAS_DIR"
export MIG_DOC='{"already_migrated":false,"rows":20480,"turso_sent_bytes":21000000,"turso_received_bytes":21000000,"objects_written":80,"r2_put_bytes":20000000,"r2_get_bytes":1000,"duration_s":5400}'
printf '%s\n' '{"enabled":true,"bytes":{"turso_push":2000000,"turso_pull":5000,"r2_put":800000,"r2_get":100}}' > "$MEAS_DIR/sp-a-status-1"
printf '%s\n' '{"enabled":true,"bytes":{"turso_push":2000000,"turso_pull":6000,"r2_put":800000,"r2_get":105}}' > "$MEAS_DIR/sp-a-status-2"
printf '%s\n' '{"enabled":true,"bytes":{"turso_push":2000000,"turso_pull":6000,"r2_put":800000,"r2_get":105}}' > "$MEAS_DIR/sp-a-status-3"
printf '%s\n' '{"enabled":true,"bytes":{"turso_push":5000000,"turso_pull":6000,"r2_put":2400000,"r2_get":105}}' > "$MEAS_DIR/sp-a-status-4"
printf '%s\n' '{"enabled":true,"bytes":{"turso_push":100,"turso_pull":1000,"r2_put":0,"r2_get":500}}' > "$MEAS_DIR/sp-b-status-1"
printf '%s\n' '{"enabled":true,"bytes":{"turso_push":100,"turso_pull":1500,"r2_put":0,"r2_get":900}}' > "$MEAS_DIR/sp-b-status-2"
printf '%s\n' '{"enabled":true,"bytes":{"turso_push":100,"turso_pull":1500,"r2_put":0,"r2_get":900}}' > "$MEAS_DIR/sp-b-status-3"
printf '%s\n' '{"enabled":true,"bytes":{"turso_push":100,"turso_pull":1001500,"r2_put":0,"r2_get":2450900}}' > "$MEAS_DIR/sp-b-status-4"
printf '%s\n' '{"enabled":true,"bytes":{"turso_push":700,"turso_pull":3000,"r2_put":0,"r2_get":0}}' > "$MEAS_DIR/sp-none-status-1"
printf '%s\n' '{"enabled":true,"bytes":{"turso_push":700,"turso_pull":300300,"r2_put":0,"r2_get":1}}' > "$MEAS_DIR/sp-none-status-2"
printf '%s\n' '{"enabled":false}' > "$MEAS_DIR/sp-d-status-1"
printf '%s\n' '{"enabled":true,"bytes":{"turso_push":5000,"turso_pull":10485760,"r2_put":30000,"r2_get":20000000}}' > "$MEAS_DIR/sp-d-status-2"
srcf eval "$meas_body"
status 'the flag-scenario measured run exits 0' 0
_meas_out=$out
meas_row 'idle poll r2 +[0-9]'
carries 'any idle R2 byte movement flags' 'FLAG'
carries 'the idle R2 movement measures 5 bytes' ' 5 '
meas_row 'none zero get'
carries 'a non-zero none-get flags' 'FLAG'
meas_row 'join log shipped'
carries 'a shipped log flags against the does-not-ship prediction' 'FLAG'
carries 'the half-seed boundary ships' ' 1 '
meas_row 'migrate duration'
carries 'the 3x duration flags' 'FLAG'
meas_row 'day b total'
absent 'the 1.5x day total does not flag' 'FLAG'
carries 'the 1.5x day total measures 3450000' '3450000'
cat "$MEAS_DIR/snippets" >> "$all"
reset_fixtures

# Scenario C: the whole measured run through the python3 fallback extractor,
# on scenario A's canned queues: the same verdicts and the full 49-row table.
if command -v python3 >/dev/null 2>&1; then
	rm -rf "$MEAS_DIR"
	mkdir -p "$MEAS_DIR"
	cp "$work/meas-a-queues"/sp-*-status-* "$MEAS_DIR/"
	export MIG_DOC='{"already_migrated":false,"rows":20480,"turso_sent_bytes":21000000,"turso_received_bytes":21000000,"objects_written":80,"r2_put_bytes":20000000,"r2_get_bytes":1000,"duration_s":2700}'
	meas_body_py="json_tool=python3
$meas_body"
	find "$work/tmp" -mindepth 1 -maxdepth 1 | sort > "$work/meas-py-dirs-before"
	srcf eval "$meas_body_py"
	status 'the python3 measured run exits 0' 0
	_meas_out=$out
	meas_row 'day b total'
	carries 'the 3x day total flags (python3)' 'FLAG'
	meas_row 'migrate duration'
	absent 'the 1.5x migrate duration does not flag (python3)' 'FLAG'
	out=$_meas_out
	carries 'the reset is noted (python3)' 'sp-a turso_pull counter reset (worker restart)'
	find "$work/tmp" -mindepth 1 -maxdepth 1 | sort > "$work/meas-py-dirs-after"
	grep -vxF -f "$work/meas-py-dirs-before" "$work/meas-py-dirs-after" > "$work/meas-py-new-dirs" || :
	if [ "$(wc -l < "$work/meas-py-new-dirs")" -ne 1 ]; then
		echo "FAIL: the python3 measured run did not leave exactly one new state dir: $(cat "$work/meas-py-new-dirs")"
		fail=1
	else
		_meas_py_state=$(cat "$work/meas-py-new-dirs")
		if [ "$(wc -l < "$_meas_py_state/table-rows")" -ne 49 ]; then
			echo "FAIL: the python3 table has the wrong row count: $(wc -l < "$_meas_py_state/table-rows") (want 49)"
			fail=1
		fi
	fi
	cat "$MEAS_DIR/snippets" >> "$all"
	reset_fixtures
fi
unset MIG_DOC BUCKET_KEYS GUARD_LINE EO_EMPTY_AT EO_COUNT SD_BEFORE SD_RESET SD_ERR

# --- hygiene sweeps ---

# No credential file outlives any run above: cleanup removed them all.
leftover=$(find "$work/tmp" -type f \( -name 'r2-credentials' -o -name 'turso-header' \) 2>/dev/null)
if [ -n "$leftover" ]; then
	echo "FAIL: credential files outlived their runs: $leftover"
	fail=1
fi

# No secret sentinel reached any recorded curl argv (the fake also refuses
# such a call on the spot; this is the sweep across every invocation).
if [ -f "$work/curl-argv" ]; then
	for s in "$s_token" "$s_keyid" "$s_secret"; do
		if grep -qF -- "$s" "$work/curl-argv"; then
			echo "FAIL: sentinel secret on curl argv: \"$s\""
			fail=1
		fi
	done
fi

# The fake never rejected a call: every URL was a fixture URL, every argv
# secret-free, every credential file mode 600 with the right content.
if [ -s "$work/fake-fail" ]; then
	echo "FAIL: the fake curl/relevo rejected a call:"
	cat "$work/fake-fail"
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
