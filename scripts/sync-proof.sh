#!/bin/sh
# scripts/sync-proof.sh -- prove a scratch remote can carry a relevo sync run
# without spending the operator's real quota: seed a log of a chosen size,
# migrate it, run the traffic phases and print the measured-traffic table
# beside the design's predictions, read against the remote's quota.
#
# The pure core (argument and env parsing, the quota arithmetic, the run's own
# traffic estimate, the measured-traffic table) plus the read-only preflight:
# the account quota read from the developer's installed relevo, the scratch
# Turso ownership check and the bucket ownership check. Sourcing it with
# SYNC_PROOF_SOURCED set defines the functions and runs nothing, so the test
# script drives each one with no network, no sandbox and no credentials.
#
# The run phases (sandbox setup, the format-5 seed, the migrate, the traffic
# measurement) execute only under SYNC_PROOF_RUN_PHASES=1; without it a real run
# stops after the preflight with exit 2, so no test creates a sandbox or reaches
# a network. --dry-run prints the verdicts and the full ordered command plan
# and creates nothing.
#
# No env value is ever printed: a refusal names the missing variables, never
# their contents, so a secret cannot reach a terminal or a log through here.
# Credentials reach curl only through mode-600 files under the run's state dir
# (-K for the R2 key pair, -H @ for the Turso bearer header), never on argv
# where ps shows them to every user; the files are removed on exit.
#
# Usage:
#   sync-proof.sh [--env FILE] [--log-mb N] [--wipe] [--dry-run] [--help]
set -eu

prog=sync-proof.sh

# The env file a run reads unless --env moves it. It must hold the six
# variables naming the scratch Turso remote and the R2 bucket the log lives in.
default_env_file="${HOME:-}/.config/relevo-r2log-scratch.env"

usage() {
	cat <<EOF
usage: $prog [--env FILE] [--log-mb N] [--wipe] [--dry-run] [--help]

Prove a scratch remote can carry a relevo sync run within its quota.

  --env FILE    env file holding the scratch remote's credentials
                (default: $default_env_file)
  --log-mb N    size of the seeded log in mebibytes, a positive integer
                (default: 20)
  --wipe        delete this script's own bucket objects (origins recorded in
                the run's origins file) before seeding; bucket-only: it never
                waives the Turso ownership refusal and never deletes objects
                under foreign origins
  --dry-run     print the env file path, the estimate and the quota, Turso and
                bucket ownership verdicts, and the full ordered command plan
                (creates, enables with secret sources named, seed batch count,
                migrate, traffic phases); create nothing
  --help        this help

The env file must set RELEVO_SCRATCH_SYNC_URL, RELEVO_SCRATCH_SYNC_TOKEN,
RELEVO_SCRATCH_R2_ENDPOINT, RELEVO_SCRATCH_R2_BUCKET, RELEVO_SCRATCH_R2_KEY_ID
and RELEVO_SCRATCH_R2_SECRET. A refusal names the missing variables only;
no value is ever printed.

A measured run prints its table to stdout and saves it as table.txt in the
run's state dir.

The run phases (sandbox setup, format-5 seed, migrate, traffic measurement)
run only when SYNC_PROOF_RUN_PHASES=1; otherwise the script stops after the
preflight.
EOF
}

# refuse reports a usage error and exits 2: the message names the problem and
# usage goes to stderr, so a calling shell's stdout stays clean.
refuse() {
	printf '%s: %s\n' "$prog" "$*" >&2
	usage >&2
	exit 2
}

# parse_args reads the flags into env_file, log_mb, wipe and dry_run. No
# `local` in POSIX sh, so the temporaries carry a _pa_ prefix nothing else
# here uses.
parse_args() {
	env_file=$default_env_file
	log_mb=20
	wipe=0
	dry_run=0
	while [ $# -gt 0 ]; do
		case $1 in
		--env)
			[ $# -ge 2 ] || refuse '--env needs a file'
			case $2 in --*) refuse '--env needs a file' ;; esac
			env_file=$2
			shift 2
			;;
		--log-mb)
			[ $# -ge 2 ] || refuse '--log-mb needs a value'
			case $2 in --*) refuse '--log-mb needs a value' ;; esac
			_pa_n=$2
			case $_pa_n in
			'' | *[!0-9]*) refuse "--log-mb needs a positive integer, got: $_pa_n" ;;
			esac
			[ "$_pa_n" -gt 0 ] || refuse "--log-mb needs a positive integer, got: $_pa_n"
			# $((...)) reads a leading zero as octal, so strip leading zeros
			# before any arithmetic sees the value: 020 must mean twenty, not
			# sixteen.
			while :; do
				case $_pa_n in
				0?*) _pa_n=${_pa_n#0} ;;
				*) break ;;
				esac
			done
			log_mb=$_pa_n
			shift 2
			;;
		--wipe)
			wipe=1
			shift
			;;
		--dry-run)
			dry_run=1
			shift
			;;
		--help)
			usage
			exit 0
			;;
		--*)
			refuse "unknown flag: $1"
			;;
		*)
			refuse "unexpected argument: $1"
			;;
		esac
	done
}

# load_env sources FILE and requires the six scratch variables. A refusal
# names the missing variables only -- never a value -- and happens before
# anything is created. The checks are explicit rather than an eval over the
# names, so no indirect expansion can echo a secret.
load_env() {
	_le_file=$1
	if [ ! -f "$_le_file" ]; then
		printf '%s: env file not found: %s\n' "$prog" "$_le_file" >&2
		printf '%s: missing env variables: %s\n' "$prog" \
			'RELEVO_SCRATCH_SYNC_URL RELEVO_SCRATCH_SYNC_TOKEN RELEVO_SCRATCH_R2_ENDPOINT RELEVO_SCRATCH_R2_BUCKET RELEVO_SCRATCH_R2_KEY_ID RELEVO_SCRATCH_R2_SECRET' >&2
		exit 2
	fi
	# shellcheck disable=SC1090,SC1091 # the path is supplied at run time (--env or the
	# default under $HOME), so shellcheck cannot follow it
	. "$_le_file"
	_le_missing=''
	[ -n "${RELEVO_SCRATCH_SYNC_URL:-}" ] || _le_missing="$_le_missing RELEVO_SCRATCH_SYNC_URL"
	[ -n "${RELEVO_SCRATCH_SYNC_TOKEN:-}" ] || _le_missing="$_le_missing RELEVO_SCRATCH_SYNC_TOKEN"
	[ -n "${RELEVO_SCRATCH_R2_ENDPOINT:-}" ] || _le_missing="$_le_missing RELEVO_SCRATCH_R2_ENDPOINT"
	[ -n "${RELEVO_SCRATCH_R2_BUCKET:-}" ] || _le_missing="$_le_missing RELEVO_SCRATCH_R2_BUCKET"
	[ -n "${RELEVO_SCRATCH_R2_KEY_ID:-}" ] || _le_missing="$_le_missing RELEVO_SCRATCH_R2_KEY_ID"
	[ -n "${RELEVO_SCRATCH_R2_SECRET:-}" ] || _le_missing="$_le_missing RELEVO_SCRATCH_R2_SECRET"
	if [ -n "$_le_missing" ]; then
		printf '%s: missing env variables:%s\n' "$prog" "$_le_missing" >&2
		exit 2
	fi
}

# quota_allows PROJECTED QUOTA ESTIMATE succeeds (exit 0) when the projected
# total plus this run's estimate stays within 95% of the quota, and refuses
# (exit 1) otherwise. Cross-multiply instead of dividing: integer division
# would floor 95% of the quota, and the boundary must stay exact -- exactly
# 95% allows, one byte over refuses.
quota_allows() {
	[ $(( ($1 + $3) * 100 )) -le $(( $2 * 95 )) ]
}

# run_estimate LOG_MB prints the run's own Turso traffic in bytes. The run
# seeds the log once (a write of LOG_MB mebibytes), then migrate reads every
# seeded row and writes it back (a read plus a write of the same bytes): three
# passes over the seed dominate the run's traffic. The fixed schema and
# round-row writes are kilobytes beside a multi-MiB log, well inside the 5%
# quota margin, so they stay out of the estimate. A mebibyte is 1024*1024
# bytes: log sizes are binary, and the estimate must not shrink the seed.
run_estimate() {
	printf '%s\n' "$(( 3 * $1 * 1024 * 1024 ))"
}

# flag_ratio MEASURED PREDICTED prints FLAG when the measured traffic is more
# than twice the prediction or less than half of it, and nothing otherwise.
# Cross-multiply to keep the comparison exact in integer math: m > 2p and
# 2m < p. A zero prediction with zero measured is agreement (nothing), while
# any measured traffic against a zero prediction is an unbounded ratio (FLAG).
flag_ratio() {
	if [ "$1" -gt $(( 2 * $2 )) ] || [ $(( 2 * $1 )) -lt "$2" ]; then
		printf 'FLAG\n'
	fi
}

# row_flag MEASURED PREDICTED prints one table row's flag. An n/a figure -- a
# count the bucket keeps no counter for, or a prediction the design gives no
# figure for -- has no ratio to flag, so the column stays empty rather than
# guessing one; every numeric pair goes through flag_ratio unchanged.
row_flag() {
	case $1 in n/a*) return 0 ;; esac
	case $2 in n/a*) return 0 ;; esac
	flag_ratio "$1" "$2"
}

# print_table reads name<TAB>measured<TAB>predicted<TAB>unit rows on stdin and
# prints the measured-traffic table, its flag column driven by row_flag
# (flag_ratio for a numeric pair). The tab IFS splits each row on tabs only, so
# a name carrying spaces stays one field. `|| [ -n "$_pt_name" ]` reads a final
# row that has no trailing newline.
print_table() {
	_pt_tab=$(printf '\t')
	printf '%-16s %14s %14s %-6s %s\n' name measured predicted unit flag
	while IFS="$_pt_tab" read -r _pt_name _pt_measured _pt_predicted _pt_unit || [ -n "$_pt_name" ]; do
		[ -n "$_pt_name" ] || continue
		_pt_flag=$(row_flag "$_pt_measured" "$_pt_predicted")
		printf '%-16s %14s %14s %-6s %s\n' "$_pt_name" "$_pt_measured" "$_pt_predicted" "$_pt_unit" "$_pt_flag"
	done
}

# join_lines reads lines on stdin and prints them as one space-separated line:
# the form a refusal or a verdict names a list of tables or origins in.
join_lines() {
	_jl_out=''
	while IFS= read -r _jl_line || [ -n "$_jl_line" ]; do
		[ -n "$_jl_line" ] || continue
		_jl_out="$_jl_out $_jl_line"
	done
	printf '%s\n' "${_jl_out# }"
}

# resolve_tools finds the three readers the preflight needs, before anything
# is created. relevo comes from the caller's PATH -- the developer's installed
# binary, never a sandbox one, since no sandbox exists at preflight time; its
# `db sync status` reads local marks only, so the quota read never writes.
# jq is the preferred JSON extractor: a dedicated reader with no interpreter
# startup; python3 is the fallback because it ships on machines without jq.
# Both print the same verdict lines, so the choice cannot change a refusal.
# curl must carry --aws-sigv4 (added in curl 7.75): the bucket requests are
# signed by curl itself, never by a hand-rolled SigV4.
resolve_tools() {
	relevo_bin=$(command -v relevo) || refuse "quota preflight needs the developer's relevo on PATH (db sync status --json); none found"
	if command -v jq >/dev/null 2>&1; then
		json_tool=jq
	elif command -v python3 >/dev/null 2>&1; then
		json_tool=python3
	else
		refuse 'JSON extraction needs jq or python3; neither is on PATH'
	fi
	command -v curl >/dev/null 2>&1 || refuse 'bucket reads need curl; none is on PATH'
	curl --help all 2>/dev/null | grep -q -- '--aws-sigv4' || refuse 'bucket reads need curl with --aws-sigv4 support (curl 7.75+); this curl lacks it'
}

# quota_fields reads a `relevo db sync status --json` document on stdin and
# prints one verdict line: `ok PROJECTED QUOTA`, or which field is missing.
# account is optional in the document, projected is omitempty (absent while
# the month is too young to project), and a zero quota means no allowance is
# set: each is a refusal, never a guessed projection.
quota_fields() {
	case $json_tool in
	jq)
		jq -r 'if .account == null then "missing account"
			elif (.account.quota == null or .account.quota == 0) then "missing quota"
			elif .account.projected == null then "missing projected"
			else "ok \(.account.projected) \(.account.quota)" end'
		;;
	python3)
		python3 -c 'import json, sys
d = json.load(sys.stdin)
a = d.get("account")
if a is None:
    print("missing account")
elif not a.get("quota"):
    print("missing quota")
elif a.get("projected") is None:
    print("missing projected")
else:
    print("ok %s %s" % (a["projected"], a["quota"]))'
		;;
	esac
}

# quota_read runs the developer's status verb and sets projected and quota
# from its account reading. A failed verb, an unreadable document or a missing
# field exits 2 naming the problem: the preflight refuses rather than guessing.
quota_read() {
	_qr_json=$("$relevo_bin" db sync status --json) || refuse 'quota preflight: relevo db sync status --json failed'
	[ -n "$_qr_json" ] || refuse 'quota preflight: relevo db sync status --json printed nothing'
	if ! _qr_fields=$(printf '%s\n' "$_qr_json" | quota_fields); then
		refuse 'quota preflight: the status JSON is unreadable'
	fi
	case $_qr_fields in
	'missing account') refuse 'quota preflight: account missing from relevo db sync status --json' ;;
	'missing quota') refuse 'quota preflight: account.quota missing or zero in relevo db sync status --json' ;;
	'missing projected') refuse 'quota preflight: account.projected missing from relevo db sync status --json; not guessing a projection' ;;
	esac
	projected=${_qr_fields#ok }
	projected=${projected%% *}
	quota=${_qr_fields##* }
}

# turso_endpoint URL prints the libsql HTTP pipeline endpoint for a sync URL:
# the sync engine reads libsql:// and turso:// as https:// (tursogo's
# normalizeUrl) and posts statements to /v2/pipeline, the endpoint the
# transport's own errors name. The script speaks the same endpoint so its
# reads land where the engine's would; any other scheme passes through and
# the probe refuses it if it is not a working HTTP endpoint.
turso_endpoint() {
	case $1 in
	libsql://*) _te_base=https://${1#libsql://} ;;
	turso://*) _te_base=https://${1#turso://} ;;
	*) _te_base=$1 ;;
	esac
	printf '%s/v2/pipeline\n' "${_te_base%/}"
}

# state_setup creates the run's state dir under TMPDIR and the two credential
# files curl reads: the R2 key pair as a -K config line and the Turso bearer
# token as a -H header line. umask 077 makes both mode 600 at creation, so no
# other user can read them while they exist; cleanup removes them on exit.
# The runs file records this run's id (the Turso marker is checked against
# it), the origins file starts empty (a fresh run owns no origin yet), and the
# sandboxes file starts empty: it records only the sandboxes this run creates,
# which is the whole of what cleanup may destroy. The table files name the
# measured rows and the printed table; measure_setup starts the rows file empty.
state_setup() {
	run_id=$(date -u +%Y%m%dT%H%M%SZ)-$$
	state_dir=${TMPDIR:-/tmp}/sync-proof-$run_id
	r2_cred_file=$state_dir/r2-credentials
	turso_header_file=$state_dir/turso-header
	runs_file=$state_dir/runs
	origins_file=$state_dir/origins
	sandboxes_file=$state_dir/sandboxes
	migrate_file=$state_dir/migrate.json
	table_rows_file=$state_dir/table-rows
	table_file=$state_dir/table.txt
	pipeline_url=$(turso_endpoint "$RELEVO_SCRATCH_SYNC_URL")
	trap cleanup EXIT
	mkdir -p "$state_dir"
	chmod 700 "$state_dir"
	(umask 077; printf 'user = "%s:%s"\n' "$RELEVO_SCRATCH_R2_KEY_ID" "$RELEVO_SCRATCH_R2_SECRET" > "$r2_cred_file")
	(umask 077; printf 'Authorization: Bearer %s\n' "$RELEVO_SCRATCH_SYNC_TOKEN" > "$turso_header_file")
	printf '%s\n' "$run_id" > "$runs_file"
	: > "$origins_file"
	: > "$sandboxes_file"
}

# cleanup removes the credential files so they never outlive the run; a
# dry-run removes its whole state dir, since a preview must leave nothing
# behind. On a non-zero exit it also destroys the sandboxes this run created,
# leaving the scratch remote and bucket as found except the seeded log. A failed
# removal or destroy is ignored: the run's own verdict and exit status stand,
# and the files are unreachable once the process exits anyway.
cleanup() {
	_cl_status=$?
	rm -f "$r2_cred_file" "$turso_header_file" 2>/dev/null || :
	if [ "${dry_run:-0}" = 1 ]; then
		rm -rf "$state_dir" 2>/dev/null || :
	elif [ "$_cl_status" -ne 0 ]; then
		destroy_sandboxes
	fi
}

# turso_request SQL prints the pipeline request body: one execute and a close,
# so the connection does not linger on the server. The JSON tool builds it so
# the SQL is escaped as JSON, not pasted into a template.
turso_request() {
	case $json_tool in
	jq)
		jq -c -n --arg sql "$1" '{requests: [{type: "execute", stmt: {sql: $sql}}, {type: "close"}]}'
		;;
	python3)
		python3 -c 'import json, sys
print(json.dumps({"requests": [{"type": "execute", "stmt": {"sql": sys.argv[1]}}, {"type": "close"}]}, separators=(",", ":")))' "$1"
		;;
	esac
}

# turso_rows reads a pipeline response on stdin and prints `ok` followed by
# the first column of each row, or `error MESSAGE` for an error result. The
# verdict line keeps a failed statement from reading as an empty result.
turso_rows() {
	case $json_tool in
	jq)
		jq -r 'if .results[0].type == "ok" then "ok", (.results[0].response.result.rows[][0].value)
			else "error \(.results[0].error.message // "pipeline error result")" end'
		;;
	python3)
		python3 -c 'import json, sys
d = json.load(sys.stdin)
r = (d.get("results") or [{}])[0]
if r.get("type") == "ok":
    print("ok")
    for row in r["response"]["result"]["rows"]:
        print(row[0]["value"])
else:
    print("error " + str(r.get("error", {}).get("message", "pipeline error result")))'
		;;
	esac
}

# turso_query SQL runs one read-only statement against the scratch pipeline
# endpoint and prints its first-column rows. The bearer token travels in the
# mode-600 header file (-H @), never on argv. Any failure -- curl, a non-200
# status, an error result, an unreadable body -- exits 2: a failed probe is
# never an empty database.
turso_query() {
	_tq_sql=$1
	_tq_req=$(turso_request "$_tq_sql")
	# A newline cannot survive $( ): command substitution strips trailing
	# newlines, so the body/status separator is a quoted literal.
	_tq_nl='
'
	if ! _tq_resp=$(printf '%s\n' "$_tq_req" | curl -sS -X POST -H @"$turso_header_file" --data-binary @- -w '\n%{http_code}' "$pipeline_url"); then
		refuse 'scratch Turso probe failed: curl could not reach the pipeline endpoint'
	fi
	_tq_code=${_tq_resp##*"$_tq_nl"}
	_tq_body=${_tq_resp%"$_tq_nl"*}
	[ "$_tq_code" = 200 ] || refuse "scratch Turso probe failed: HTTP $_tq_code"
	if ! _tq_out=$(printf '%s\n' "$_tq_body" | turso_rows); then
		refuse 'scratch Turso probe failed: unreadable pipeline response'
	fi
	_tq_first=${_tq_out%%"$_tq_nl"*}
	if [ "$_tq_first" != ok ]; then
		refuse "scratch Turso probe failed: ${_tq_first#error }"
	fi
	if [ "$_tq_out" != ok ]; then
		printf '%s\n' "${_tq_out#*"$_tq_nl"}"
	fi
}

# scratch_turso_owned MARKER RUNS_FILE reads table names on stdin and succeeds
# (0) when the scratch database is provably this script's: no user tables at
# all, or the meta marker holds a run id recorded in RUNS_FILE. Anything else
# is 1: a database with tables and no provable marker may hold another
# writer's data. sqlite_* names are SQLite's own internals, not user tables;
# an empty MARKER means no meta table or no marker row.
scratch_turso_owned() {
	_st_marker=$1
	_st_runs=$2
	_st_user_tables=0
	while IFS= read -r _st_name || [ -n "$_st_name" ]; do
		case $_st_name in
		'' | sqlite_*) continue ;;
		esac
		_st_user_tables=1
	done
	if [ "$_st_user_tables" = 0 ]; then
		return 0
	fi
	if [ -z "$_st_marker" ] || [ ! -f "$_st_runs" ]; then
		return 1
	fi
	while IFS= read -r _st_id || [ -n "$_st_id" ]; do
		if [ "$_st_id" = "$_st_marker" ]; then
			return 0
		fi
	done < "$_st_runs"
	return 1
}

# origin_recorded FILE ORIGIN succeeds when ORIGIN is a whole line of FILE,
# the record of origins this script owns. The match is fixed-string and
# whole-line, so an origin that is a prefix of another does not pass for it.
origin_recorded() {
	[ -f "$1" ] && grep -qxF -- "$2" "$1"
}

# key_origins reads bucket keys on stdin and prints each distinct first path
# segment: keys are <origin>/log/<first> and <origin>/<digest>, so the origin
# is everything before the first slash.
key_origins() {
	while IFS= read -r _ko_key || [ -n "$_ko_key" ]; do
		[ -n "$_ko_key" ] || continue
		printf '%s\n' "${_ko_key%%/*}"
	done | sort -u
}

# bucket_foreign ORIGINS_FILE reads keys on stdin and prints the origins not
# in the record: the foreign ones. No output means every listed origin is
# owned, or the bucket is empty.
bucket_foreign() {
	while IFS= read -r _bf_key || [ -n "$_bf_key" ]; do
		[ -n "$_bf_key" ] || continue
		_bf_origin=${_bf_key%%/*}
		if ! origin_recorded "$1" "$_bf_origin"; then
			printf '%s\n' "$_bf_origin"
		fi
	done | sort -u
}

# bucket_list prints every key in the scratch bucket, one per line, from a
# ListObjectsV2 GET signed by curl's own --aws-sigv4 (region auto, service s3,
# as R2 reads them); the key pair travels in the mode-600 -K file, never on
# argv. The key space carries no XML-special characters, so <Key> extraction
# needs no entity decoding. A truncated listing exits 2: one page cannot
# prove ownership of a bucket it does not fully show, and a scratch bucket
# holds one run's log, far below the 1000-key page size.
bucket_list() {
	_bl_url="${RELEVO_SCRATCH_R2_ENDPOINT%/}/$RELEVO_SCRATCH_R2_BUCKET?list-type=2"
	# A newline cannot survive $( ): command substitution strips trailing
	# newlines, so the body/status separator is a quoted literal.
	_bl_nl='
'
	if ! _bl_resp=$(curl -sS --aws-sigv4 'aws:amz:auto:s3' -K "$r2_cred_file" -X GET -w '\n%{http_code}' "$_bl_url"); then
		refuse 'bucket listing failed: curl could not reach the endpoint'
	fi
	_bl_code=${_bl_resp##*"$_bl_nl"}
	_bl_body=${_bl_resp%"$_bl_nl"*}
	[ "$_bl_code" = 200 ] || refuse "bucket listing failed: HTTP $_bl_code"
	if printf '%s\n' "$_bl_body" | grep -o '<IsTruncated>[^<]*</IsTruncated>' | grep -q true; then
		refuse 'bucket listing is truncated: too many objects to prove ownership in one page'
	fi
	printf '%s\n' "$_bl_body" | grep -o '<Key>[^<]*</Key>' | sed 's/^<Key>//; s/<\/Key>$//'
}

# r2_delete KEY deletes one object with a signed DELETE. A 404 is already-gone,
# not a failure: the wipe's re-list is the proof, not the delete's answer.
r2_delete() {
	_rd_url="${RELEVO_SCRATCH_R2_ENDPOINT%/}/$RELEVO_SCRATCH_R2_BUCKET/$1"
	# A newline cannot survive $( ): command substitution strips trailing
	# newlines, so the body/status separator is a quoted literal.
	_rd_nl='
'
	if ! _rd_resp=$(curl -sS --aws-sigv4 'aws:amz:auto:s3' -K "$r2_cred_file" -X DELETE -w '\n%{http_code}' "$_rd_url"); then
		refuse 'bucket delete failed: curl could not reach the endpoint'
	fi
	_rd_code=${_rd_resp##*"$_rd_nl"}
	case $_rd_code in
	2?? | 404) ;;
	*) refuse "bucket delete of $1 failed: HTTP $_rd_code" ;;
	esac
}

# bucket_wipe ORIGINS_FILE reads keys on stdin, deletes only the keys whose
# origin is in the record, then re-lists and refuses if any owned object
# remains. Foreign origins are never deleted: the record is the whole of what
# this script may touch. Prints the remaining keys.
bucket_wipe() {
	_bw_file=$1
	while IFS= read -r _bw_key || [ -n "$_bw_key" ]; do
		[ -n "$_bw_key" ] || continue
		if origin_recorded "$_bw_file" "${_bw_key%%/*}"; then
			r2_delete "$_bw_key"
		fi
	done
	_bw_after=$(bucket_list)
	while IFS= read -r _bw_key || [ -n "$_bw_key" ]; do
		[ -n "$_bw_key" ] || continue
		if origin_recorded "$_bw_file" "${_bw_key%%/*}"; then
			refuse "bucket wipe left an owned object behind: $_bw_key"
		fi
	done <<EOF
$_bw_after
EOF
	printf '%s\n' "$_bw_after"
}

# --- the run phases: sandbox setup, format-5 seed, migrate, measurement ---
#
# The phases run only when SYNC_PROOF_RUN_PHASES=1. The test harness and any
# casual invocation leave it unset, so main stops after the preflight and no
# test creates a sandbox or reaches a network; the measured run sets it.
# --dry-run always previews the ordered command plan instead of running it.
# The setup verbs' output goes to stderr and the measurement verbs' output to
# state-dir files: stdout belongs to the measured table, the run's deliverable.

# The sandboxes this run creates, in enable order: sp-a is the first machine (it
# creates the key and is the migrator), sp-b joins reading full history, sp-none
# joins reading none. The names match relevo-sandbox.sh's name rule.
sandbox_names='sp-a sp-b sp-none'

# create builds ./cmd/relevo from the invoking checkout, so it runs from this
# checkout's root and the sandbox script is addressed relative to that root; the
# test overrides sandbox_script with a stub. The root mirrors relevo-sandbox.sh.
sandbox_script=scripts/relevo-sandbox.sh
sandbox_root=${RELEVO_SANDBOX_ROOT:-${HOME:-}/.local/share/relevo-sandboxes}

# The exact guard every in-sandbox snippet starts with (docs/env-sandbox.md):
# RELEVO_SANDBOX is set iff the sandbox exports are in force, so a command never
# runs against the developer's own state root when env.sh was not sourced.
sandbox_guard='test -n "$RELEVO_SANDBOX" || { echo '\''not in a sandbox'\'' >&2; exit 1; }'

# The seed's batch model: one HTTP request carries seed_batch_rows rows (the
# >=500-row batch floor), bodies average seed_body_avg bytes. seed_batches
# predicts the count from this model for the dry run; the real seed reads the
# stored size back and tops up, so the prediction is never the measured figure.
seed_batch_rows=500
seed_body_avg=1024
seed_body_min=512
seed_body_spread=1024

# The migrate's wall-clock bound in seconds: the worker's 30-minute migration
# budget that dbSyncMigrateBound (cmd/relevo/db_sync_migrate.go) clears plus dial
# slack, so the CLI never gives up before the worker does.
migrate_bound_s=1800

# The format-5 log and marks DDL are copied verbatim from the shapes the sync
# worker's own tests seed an older remote with (internal/syncworker/turso_test.go:
# logWithMACAndAfter and marksTableStatement); the table set log,marks,meta is
# olderLogTables[5] in internal/syncworker/format.go. Copying them keeps the
# seeded remote exactly what the migrator classifies as format 5, not a lookalike.
seed_log_ddl='CREATE TABLE log (origin TEXT NOT NULL, seq INTEGER NOT NULL, batch INTEGER NOT NULL, tbl TEXT NOT NULL, pk TEXT NOT NULL, op TEXT NOT NULL, schema_version INTEGER NOT NULL, body TEXT, at TEXT NOT NULL, mac BLOB, after_origin TEXT, after_seq INTEGER, PRIMARY KEY (origin, seq))'
seed_marks_ddl='CREATE TABLE marks (reader TEXT NOT NULL, origin TEXT NOT NULL, seq INTEGER NOT NULL, at TEXT NOT NULL, PRIMARY KEY (reader, origin))'
seed_meta_ddl='CREATE TABLE meta (key TEXT NOT NULL PRIMARY KEY, value TEXT NOT NULL)'

# seed_batches LOG_MB prints the predicted batch count: target bytes over the
# bytes one batch carries, rounded up. Integer ceil keeps a partial batch counted
# as one whole request.
seed_batches() {
	_sb_target=$(( $1 * 1024 * 1024 ))
	_sb_bpb=$(( seed_batch_rows * seed_body_avg ))
	printf '%s\n' "$(( (_sb_target + _sb_bpb - 1) / _sb_bpb ))"
}

# seed_body_len N prints row N's body length: a fixed minimum plus N mod the
# spread, so lengths vary across rows and the compression a later phase measures
# is honest rather than one repeated string.
seed_body_len() {
	printf '%s\n' "$(( seed_body_min + ($1 % seed_body_spread) ))"
}

# seed_statements prints the format-5 schema and marker rows, one statement per
# line: exactly log, marks and meta (no heads or shared table), meta.format='5',
# and the syncproof.run marker holding this run's id. The drops that clear what a
# prior enable left are seed_reset_statements, kept apart so this list is only
# what a format-5 log is.
seed_statements() {
	printf '%s\n' "$seed_log_ddl"
	printf '%s\n' "$seed_marks_ddl"
	printf '%s\n' "$seed_meta_ddl"
	printf "INSERT INTO meta (key, value) VALUES ('format', '5')\n"
	printf "INSERT INTO meta (key, value) VALUES ('syncproof.run', '%s')\n" "$run_id"
}

# seed_reset_statements prints the drops that clear whatever the remote holds
# (the enables create heads/meta/shared at format 6) so the seed leaves exactly
# log, marks, meta. IF EXISTS keeps a fresh or partly-seeded remote safe.
seed_reset_statements() {
	printf '%s\n' 'DROP TABLE IF EXISTS heads'
	printf '%s\n' 'DROP TABLE IF EXISTS shared'
	printf '%s\n' 'DROP TABLE IF EXISTS log'
	printf '%s\n' 'DROP TABLE IF EXISTS marks'
	printf '%s\n' 'DROP TABLE IF EXISTS meta'
}

# seed_batch_sql ORIGIN START COUNT prints one INSERT carrying COUNT log rows for
# ORIGIN, sequences START..START+COUNT-1. Each body is a per-row pseudo-random
# word (a Lehmer generator seeded from the row number) repeated to the row's
# length, so content and length both vary. awk builds the batch in one pass; the
# single quote travels as a variable so the SQL literals stay readable.
seed_batch_sql() {
	awk -v q="'" -v origin="$1" -v start="$2" -v count="$3" \
		-v bmin="$seed_body_min" -v bspread="$seed_body_spread" '
	BEGIN {
		gsub(q, q q, origin)
		printf "INSERT INTO log (origin, seq, batch, tbl, pk, op, schema_version, body, at, mac, after_origin, after_seq) VALUES "
		for (i = 0; i < count; i++) {
			n = start + i
			L = bmin + (n % bspread)
			x = (n * 48271) % 2147483647
			w = ""
			for (c = 0; c < 16; c++) { x = (x * 48271) % 2147483647; w = w sprintf("%c", 97 + (x % 26)) }
			body = ""
			while (length(body) < L) body = body w
			body = substr(body, 1, L)
			pk = q "[\"" n "\"]" q
			t = q origin q ", " n ", 1, " q "round" q ", " pk ", " q "upsert" q ", 1, " q body q ", " q "2026-01-01T00:00:00Z" q ", NULL, NULL, NULL"
			if (i > 0) printf ","
			printf "(%s)", t
		}
		printf "\n"
	}'
}

# turso_multi_request reads statements on stdin (one per line) and prints a
# pipeline body with one execute per statement and a final close. The JSON tool
# escapes each statement, so SQL is never pasted into a template.
turso_multi_request() {
	case $json_tool in
	jq)
		jq -c -R -s '{requests: ([split("\n")[] | select(length > 0) | {type: "execute", stmt: {sql: .}}] + [{type: "close"}])}'
		;;
	python3)
		python3 -c 'import json, sys
stmts = [l for l in sys.stdin.read().split("\n") if l]
reqs = [{"type": "execute", "stmt": {"sql": s}} for s in stmts] + [{"type": "close"}]
print(json.dumps({"requests": reqs}, separators=(",", ":")))'
		;;
	esac
}

# turso_exec_verdict reads a pipeline response and prints ok when no result is an
# error, else error MESSAGE for the first one. A write batch landed only when
# every statement did; an empty response is an error, never a silent success.
turso_exec_verdict() {
	case $json_tool in
	jq)
		jq -r 'if ((.results // []) | length) == 0 then "error empty pipeline response"
			else ([.results[] | select(.type == "error")]) as $err
			| if ($err | length) == 0 then "ok" else "error " + ($err[0].error.message // "pipeline error result") end end'
		;;
	python3)
		python3 -c 'import json, sys
d = json.load(sys.stdin)
res = d.get("results") or []
if not res:
    print("error empty pipeline response")
else:
    errs = [r for r in res if r.get("type") == "error"]
    if not errs:
        print("ok")
    else:
        print("error " + str(errs[0].get("error", {}).get("message", "pipeline error result")))'
		;;
	esac
}

# turso_exec reads statements on stdin and runs them against the scratch
# pipeline, refusing (exit 2) on a curl failure, a non-200 status or any error
# result: a refused batched insert halts the seed rather than falling back to a
# smaller unmeasured one. The bearer token travels in the mode-600 header file.
turso_exec() {
	_te_req=$(turso_multi_request)
	# A newline cannot survive $( ): command substitution strips trailing
	# newlines, so the body/status separator is a quoted literal.
	_te_nl='
'
	if ! _te_resp=$(printf '%s\n' "$_te_req" | curl -sS -X POST -H @"$turso_header_file" --data-binary @- -w '\n%{http_code}' "$pipeline_url"); then
		refuse 'scratch Turso write failed: curl could not reach the pipeline endpoint'
	fi
	_te_code=${_te_resp##*"$_te_nl"}
	_te_body=${_te_resp%"$_te_nl"*}
	[ "$_te_code" = 200 ] || refuse "scratch Turso write failed: HTTP $_te_code"
	if ! _te_verdict=$(printf '%s\n' "$_te_body" | turso_exec_verdict); then
		refuse 'scratch Turso write failed: unreadable pipeline response'
	fi
	[ "$_te_verdict" = ok ] || refuse "scratch Turso write failed: ${_te_verdict#error }"
}

# turso_size prints "ROWS BYTES" read back from the log: the count and the sum of
# body lengths the server stores, never a client-side estimate. coalesce reads an
# empty log's sum as 0.
turso_size() {
	turso_query "SELECT count(*) || ' ' || coalesce(sum(length(body)), 0) FROM log"
}

# seed_log ORIGIN LOG_MB resets the remote to the format-5 schema and fills log
# with synthetic rows for ORIGIN until the stored body bytes are within 5% of
# LOG_MB mebibytes, reading the size back after every batch and topping up. A
# batch the endpoint refuses, or a size that cannot be brought into the band,
# halts (exit 2): never a smaller unmeasured seed.
seed_log() {
	_sl_origin=$1
	_sl_target=$(( $2 * 1024 * 1024 ))
	_sl_low=$(( _sl_target * 95 / 100 ))
	_sl_high=$(( _sl_target * 105 / 100 ))
	seed_reset_statements | turso_exec
	seed_statements | turso_exec
	_sl_rows=0
	_sl_bytes=0
	while [ "$_sl_bytes" -lt "$_sl_low" ]; do
		_sl_start=$(( _sl_rows + 1 ))
		seed_batch_sql "$_sl_origin" "$_sl_start" "$seed_batch_rows" | turso_exec
		_sl_size=$(turso_size)
		_sl_rows=${_sl_size%% *}
		_sl_bytes=${_sl_size##* }
		case $_sl_rows in '' | *[!0-9]*) refuse 'seed: the log size read back is not a number' ;; esac
		case $_sl_bytes in '' | *[!0-9]*) refuse 'seed: the log size read back is not a number' ;; esac
	done
	if [ "$_sl_bytes" -gt "$_sl_high" ]; then
		refuse "seed: log overshot the 5% band: $_sl_bytes bytes against target $_sl_target"
	fi
	printf '%s: seeded %s rows, %s body bytes (target %s)\n' "$prog" "$_sl_rows" "$_sl_bytes" "$_sl_target" >&2
}

# sb_env NAME prints the env.sh path of a sandbox under the root.
sb_env() {
	printf '%s/%s/env.sh\n' "$sandbox_root" "$1"
}

# sb_sh NAME reads a shell snippet on stdin and runs it inside sandbox NAME: a
# subshell sources the sandbox's env.sh (the recipe that enters it), then evals
# the snippet in that same subshell so the scratch variables the caller holds
# stay in scope. Every snippet starts with the guard line; env.sh puts the
# sandbox's own bin first on PATH, so relevo here is the sandbox's build, never
# the caller's.
sb_sh() {
	_ss_env=$(sb_env "$1")
	_ss_snippet=$(cat)
	(
		# shellcheck disable=SC1090 # the path is built at run time from the sandbox root
		. "$_ss_env"
		# env.sh always exports RELEVO_SANDBOX as the sandbox's own name
		# (relevo-sandbox.sh's write_env_sh writes `export RELEVO_SANDBOX=%s`
		# with the name), so a marker naming another sandbox means the
		# snippet was delivered to the wrong one. The in-snippet guard only
		# tests that the marker is non-empty and can never catch that; this
		# name check is the only form of the guard that can refuse here.
		if [ "${RELEVO_SANDBOX:-}" != "$1" ]; then
			echo 'not in a sandbox' >&2
			exit 1
		fi
		eval "$_ss_snippet"
	)
}

# record_new_origins lists the bucket and appends every first-segment not already
# in the origins record, printing the newly recorded origins. After one
# sandbox's first push the new origin is that sandbox's; the record stays the
# whole of what this run may delete.
record_new_origins() {
	_rn_keys=$(bucket_list)
	_rn_new=$(printf '%s\n' "$_rn_keys" | bucket_foreign "$origins_file")
	if [ -n "$_rn_new" ]; then
		printf '%s\n' "$_rn_new" >> "$origins_file"
	fi
	printf '%s\n' "$_rn_new"
}

# create_sandboxes refuses to reuse any existing sandbox of the run's names
# before creating any, then creates each from this checkout's root and records
# it. The record is what cleanup destroys: only this run's creations.
create_sandboxes() {
	# shellcheck disable=SC2086 # the names are a space-separated list to iterate
	for _cs_name in $sandbox_names; do
		if [ -e "$sandbox_root/$_cs_name" ]; then
			refuse "sandbox $_cs_name already exists; refusing to reuse it (destroy it first)"
		fi
	done
	# shellcheck disable=SC2086 # the names are a space-separated list to iterate
	for _cs_name in $sandbox_names; do
		"$sandbox_script" create "$_cs_name" || refuse "sandbox create failed for $_cs_name"
		printf '%s\n' "$_cs_name" >> "$sandboxes_file"
	done
}

# gen_passphrase prints a fresh sync passphrase for the scratch key, never echoed
# elsewhere. The scratch remote holds no key before this run, so the passphrase
# is this run's own; it reaches the verbs only via stdin or the documented env
# name, never argv.
gen_passphrase() {
	od -An -tx1 -N24 /dev/urandom | tr -d ' \n'
}

# enable_first enables sp-a on the still-empty remote, then creates the key with
# unlock --new and drives the first push. a is enabled before the seed so it
# exists as an origin the seed can fill: its installation id is learned from the
# bucket after this push, and the seed then resets the remote to format 5 around
# that origin. The enable carries no passphrase (an enable never creates key
# material), so a joins locked and unlock --new sends the history it owes.
enable_first() {
	sb_sh sp-a >&2 <<EOF
$sandbox_guard
unset RELEVO_SYNC_PASSPHRASE
export RELEVO_R2_SECRET="\$RELEVO_SCRATCH_R2_SECRET"
printf '%s' "\$RELEVO_SCRATCH_SYNC_TOKEN" | relevo db sync enable --url "\$RELEVO_SCRATCH_SYNC_URL" --token-stdin --r2-endpoint "\$RELEVO_SCRATCH_R2_ENDPOINT" --r2-bucket "\$RELEVO_SCRATCH_R2_BUCKET" --r2-key-id "\$RELEVO_SCRATCH_R2_KEY_ID"
EOF
	sb_sh sp-a >&2 <<EOF
$sandbox_guard
printf '%s' "\$sync_passphrase" | relevo db sync unlock --new --passphrase-stdin
EOF
	sb_sh sp-a >&2 <<EOF
$sandbox_guard
relevo db sync push --json
EOF
}

# enable_joiner NAME HISTORY enables a joining machine with the same scratch
# remote and bucket. HISTORY is empty for full or " --history none" for a none
# machine (refused beside --as, which is never passed). The passphrase rides
# RELEVO_SYNC_PASSPHRASE so the enable verifies it against the key sp-a created;
# the token is the one stdin secret, the R2 secret the env one.
enable_joiner() {
	_ej_name=$1
	_ej_hist=$2
	sb_sh "$_ej_name" >&2 <<EOF
$sandbox_guard
export RELEVO_R2_SECRET="\$RELEVO_SCRATCH_R2_SECRET"
export RELEVO_SYNC_PASSPHRASE="\$sync_passphrase"
printf '%s' "\$RELEVO_SCRATCH_SYNC_TOKEN" | relevo db sync enable --url "\$RELEVO_SCRATCH_SYNC_URL" --token-stdin --r2-endpoint "\$RELEVO_SCRATCH_R2_ENDPOINT" --r2-bucket "\$RELEVO_SCRATCH_R2_BUCKET" --r2-key-id "\$RELEVO_SCRATCH_R2_KEY_ID"$_ej_hist
EOF
	sb_sh "$_ej_name" >&2 <<EOF
$sandbox_guard
relevo db sync push --json
EOF
}

# migrate_field FIELD reads a migrate JSON document on stdin and prints the
# field's value, empty when absent. Booleans render lowercase in both tools so
# already_migrated reads the same whichever extractor is on PATH.
migrate_field() {
	case $json_tool in
	jq)
		jq -r --arg f "$1" 'if has($f) then .[$f] else empty end'
		;;
	python3)
		python3 -c 'import json, sys
d = json.load(sys.stdin)
v = d.get(sys.argv[1])
if v is None:
    print("")
elif isinstance(v, bool):
    print(str(v).lower())
else:
    print(v)' "$1"
		;;
	esac
}

# migrate_add_duration SECONDS records the run's wall-clock duration in the
# saved migrate document as duration_s: the verb's own document carries no
# duration field (DbSyncMigrateDoc has none), and the table's duration row
# reads the record alone, not the run's memory.
migrate_add_duration() {
	case $json_tool in
	jq)
		jq --argjson d "$1" '. + {duration_s: $d}' "$migrate_file" > "$migrate_file.tmp" ||
			refuse 'migrate: could not record the duration'
		;;
	python3)
		python3 -c 'import json, sys
p = sys.argv[1]
with open(p) as f:
    d = json.load(f)
d["duration_s"] = int(sys.argv[2])
with open(p + ".tmp", "w") as f:
    json.dump(d, f)' "$migrate_file" "$1" ||
			refuse 'migrate: could not record the duration'
		;;
	esac
	mv "$migrate_file.tmp" "$migrate_file" || refuse 'migrate: could not record the duration'
}

# migrate_run times the migrate from sp-a, saves its JSON, and refuses loudly if
# already_migrated is true (the seed did not land). It records sent, received and
# the duration against the 30-minute bound. The DROP TABLE log push is inside the
# commit transaction (internal/syncworker/migrate_commit.go finishTxMigration)
# and shares the call's one turso_sent_bytes delta (internal/syncworker/migrate.go
# migrateResult): no separate counter exists, so it is never split out by estimate.
migrate_run() {
	_mr_start=$(date +%s)
	if ! sb_sh sp-a > "$migrate_file" <<EOF
$sandbox_guard
relevo db sync migrate --confirm-all-upgraded --json
EOF
	then
		refuse 'migrate failed: relevo db sync migrate returned non-zero'
	fi
	_mr_end=$(date +%s)
	_mr_dur=$(( _mr_end - _mr_start ))
	migrate_add_duration "$_mr_dur"
	_mr_already=$(migrate_field already_migrated < "$migrate_file")
	if [ "$_mr_already" = true ]; then
		refuse 'migrate reported already_migrated=true: the format-5 seed did not land'
	fi
	_mr_sent=$(migrate_field turso_sent_bytes < "$migrate_file")
	_mr_recv=$(migrate_field turso_received_bytes < "$migrate_file")
	_mr_rows=$(migrate_field rows < "$migrate_file")
	_mr_objects=$(migrate_field objects_written < "$migrate_file")
	_mr_r2put=$(migrate_field r2_put_bytes < "$migrate_file")
	_mr_r2get=$(migrate_field r2_get_bytes < "$migrate_file")
	printf '%s: migrate rows=%s objects_written=%s turso_sent_bytes=%s turso_received_bytes=%s r2_put_bytes=%s r2_get_bytes=%s duration=%ss bound=%ss\n' \
		"$prog" "$_mr_rows" "$_mr_objects" "$_mr_sent" "$_mr_recv" "$_mr_r2put" "$_mr_r2get" "$_mr_dur" "$migrate_bound_s" >&2
	if [ "$_mr_dur" -gt "$migrate_bound_s" ]; then
		printf '%s: migrate duration %ss exceeded the %ss bound\n' "$prog" "$_mr_dur" "$migrate_bound_s" >&2
	fi
}

# --- the traffic measurement: snapshots, deltas, the measured table ---
#
# Every phase snapshots the acting machine's own status counters and the bucket
# listing before and after its act, and the row is the delta. The counters are
# cumulative per machine and month, read inside the sandbox so they are that
# machine's; the listing delta is the PUT count, because the bucket keeps no
# operation counter. GET and LIST counts have no counter at all: their rows say
# n/a (no counter) and the bytes rows carry the measured figure -- a count is
# never estimated from bytes.

# The compressed day's model: day_entries entries written on sp-a in bursts of
# day_burst, one push per burst. One config set writes one shared config-document
# key (the repos section is per-item, internal/config/localkeys.go), and a push
# exports the burst's changed keys as one append -- one segment
# (internal/synclog/docexport.go, internal/syncworker/append.go). A burst over
# SegmentMaxEntries (256) new keys would be refused by the worker, so the burst
# stays under the cap; 33-key bursts predict ceil(5000/33) = 152 segments against
# the design's ~150.
day_entries=5000
day_burst=33
# The entry body's filler length: a small config entry's size, so the day's
# bodies are neither empty rows nor seed-sized pages.
day_body_len=200
# The design's day figures: ~150 segments for 5,000 entries (floor 20, ceiling
# 308), and a full-client day total of 1.6-3.0 MB whose midpoint 2.3 MB is the
# predicted value -- providers bill in powers of 1000, so 2.3 MB is 2300000 bytes.
day_predicted_segments=150
day_segment_floor=20
day_segment_ceiling=308
day_full_client_predicted=2300000
# The idle poll's call count: consecutive pulls with no writes between.
idle_poll_calls=60

# bytes_field FIELD reads a `relevo db sync status --json` document on stdin and
# prints bytes.FIELD. The bytes object is omitempty -- absent until something
# moved this month -- so an absent object or field reads as 0: a machine that
# moved nothing has zero counters, not missing ones.
bytes_field() {
	case $json_tool in
	jq)
		jq -r --arg f "$1" '.bytes[$f] // 0'
		;;
	python3)
		python3 -c 'import json, sys
d = json.load(sys.stdin)
b = d.get("bytes") or {}
print(b.get(sys.argv[1], 0))' "$1"
		;;
	esac
}

# counter_delta PREV CUR prints what a cumulative counter moved between two
# readings. A reading lower than the last means the worker restarted and counts
# from zero again, so the new reading is itself the delta -- subtracting would
# be negative and would lose everything the new worker moved (the rule
# internal/sync/bytes.go counterDelta keeps for the month's own counters).
counter_delta() {
	if [ "$2" -lt "$1" ]; then
		printf '%s\n' "$2"
	else
		printf '%s\n' "$(( $2 - $1 ))"
	fi
}

# snap NAME FILE saves sandbox NAME's own status document to FILE. A failed verb
# exits non-zero and halts the run: an empty snapshot measured as zero traffic
# would be a quiet phase that never happened.
snap() {
	sb_sh "$1" > "$2" <<EOF
$sandbox_guard
relevo db sync status --json
EOF
}

# status_deltas LABEL BEFORE AFTER prints the four counter deltas between two
# status documents, one per line in ByteCounters' order (turso_push, turso_pull,
# r2_put, r2_get). A counter that fell is a worker restart: the after value is
# the delta, and the reset is noted on stderr so a reset reading is never
# mistaken for a quiet phase.
status_deltas() {
	_sd_label=$1
	_sd_before=$2
	_sd_after=$3
	[ -s "$_sd_before" ] || refuse "status snapshot for $_sd_label is empty (before)"
	[ -s "$_sd_after" ] || refuse "status snapshot for $_sd_label is empty (after)"
	for _sd_f in turso_push turso_pull r2_put r2_get; do
		_sd_b=$(bytes_field "$_sd_f" < "$_sd_before")
		_sd_a=$(bytes_field "$_sd_f" < "$_sd_after")
		case $_sd_b in '' | *[!0-9]*) refuse "status bytes for $_sd_label are not counters: $_sd_f" ;; esac
		case $_sd_a in '' | *[!0-9]*) refuse "status bytes for $_sd_label are not counters: $_sd_f" ;; esac
		if [ "$_sd_a" -lt "$_sd_b" ]; then
			printf '%s: %s %s counter reset (worker restart): after %s, using the after value as the delta\n' \
				"$prog" "$_sd_label" "$_sd_f" "$_sd_a" >&2
		fi
		counter_delta "$_sd_b" "$_sd_a"
	done
}

# read_deltas FILE sets d_push, d_pull, d_put and d_get from the four lines
# status_deltas printed, in that order.
read_deltas() {
	{ IFS= read -r d_push; IFS= read -r d_pull; IFS= read -r d_put; IFS= read -r d_get; } < "$1"
}

# new_segment_keys BEFORE AFTER ORIGIN prints the segment keys (<origin>/log/…)
# ORIGIN holds in AFTER that BEFORE lacks: the objects a phase's pushes put.
# awk matches the prefix exactly, so an origin that is a suffix of another
# cannot pass for it; body keys (<origin>/<digest>) are not segments and stay
# out. The listing delta is the PUT count because the bucket keeps no counter.
new_segment_keys() {
	awk -v p="$3/log/" 'index($0, p) == 1' "$2" | grep -vxF -f "$1" || :
}

# count_lines TEXT prints how many non-empty lines TEXT holds: the count of a
# key list, where the empty list (one empty line from printf) is zero.
count_lines() {
	printf '%s\n' "$1" | grep -c . || :
}

# table_row NAME MEASURED PREDICTED UNIT appends one row to the run's table
# input: the tab-separated shape print_table reads, where a name carrying
# spaces stays one field.
table_row() {
	printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$4" >> "$table_rows_file"
}

# measure_setup starts the measured table: the rows file every phase appends
# to, named by state_setup beside the run's other files. The table file the run
# prints and saves lives in the same state dir, which a dry run removes whole
# and a real run keeps for the operator.
measure_setup() {
	: > "$table_rows_file"
}

# create_joiner NAME creates one more sandbox mid-run (the fresh join's sp-d),
# refusing an existing one before creating, and records it: the record stays
# the whole of what cleanup may destroy.
create_joiner() {
	if [ -e "$sandbox_root/$1" ]; then
		refuse "sandbox $1 already exists; refusing to reuse it (destroy it first)"
	fi
	"$sandbox_script" create "$1" >&2 || refuse "sandbox create failed for $1"
	printf '%s\n' "$1" >> "$sandboxes_file"
}

# day_entry_names START COUNT prints COUNT lines, each one entry's repos key and
# name: a 64-digit zero-padded id (the hex shape a repos identity carries) and a
# label plus a per-entry pseudo-random word (the seed's Lehmer generator)
# repeated to day_body_len bytes, so content and length both vary and the
# compression the day measures is honest rather than one repeated string.
day_entry_names() {
	awk -v start="$1" -v count="$2" -v len="$day_body_len" 'BEGIN {
		for (i = 0; i < count; i++) {
			n = start + i
			x = (n * 48271) % 2147483647
			w = ""
			for (c = 0; c < 16; c++) { x = (x * 48271) % 2147483647; w = w sprintf("%c", 97 + (x % 26)) }
			body = ""
			while (length(body) < len) body = body w
			printf "%064d sync-proof day entry %d %s\n", n, n, substr(body, 1, len)
		}
	}'
}

# gen_day_entries writes the day's entries on sp-a: bursts of day_burst config
# sets, one shared repos key each, then one push exporting the burst. The repos
# entry is name-only -- valid on every reader (internal/config/repos.go
# ParseRepos accepts it) and naming no real repository, so the day applies
# everywhere without touching a binding. db query cannot seed these rows (it is
# read-only), and reconcile walks the row tables, not the config document's
# keys: push per burst is the public path that appends the entries.
gen_day_entries() {
	_gd_n=0
	while [ "$_gd_n" -lt "$day_entries" ]; do
		_gd_left=$(( day_entries - _gd_n ))
		_gd_burst=$day_burst
		[ "$_gd_left" -ge "$_gd_burst" ] || _gd_burst=$_gd_left
		day_entry_names "$(( _gd_n + 1 ))" "$_gd_burst" > "$state_dir/day-names"
		while read -r _gd_id _gd_name; do
			_gd_n=$(( _gd_n + 1 ))
			sb_sh sp-a >> "$state_dir/cd-sets.log" <<EOF
$sandbox_guard
relevo config set repos.$_gd_id '{"name":"$_gd_name"}'
EOF
		done < "$state_dir/day-names"
		sb_sh sp-a >> "$state_dir/cd-pushes.jsonl" <<EOF
$sandbox_guard
relevo db sync push --json
EOF
	done
}

# phase_migrate_rows feeds the migrate's recorded figures into the table: the
# Turso bytes and the duration from migrate.json, the object count as the PUT
# count. The DROP TABLE log push shares the migrate's one turso_sent_bytes
# delta -- no separate counter exists -- so its row carries n/a and the
# footnote carries the wording, never a split figure.
phase_migrate_rows() {
	_pm_sent=$(migrate_field turso_sent_bytes < "$migrate_file")
	_pm_recv=$(migrate_field turso_received_bytes < "$migrate_file")
	_pm_dur=$(migrate_field duration_s < "$migrate_file")
	_pm_objs=$(migrate_field objects_written < "$migrate_file")
	for _pm_v in "$_pm_sent" "$_pm_recv" "$_pm_dur" "$_pm_objs"; do
		case $_pm_v in '' | *[!0-9]*) refuse 'migrate record carries a non-numeric figure' ;; esac
	done
	table_row 'migrate sent' "$_pm_sent" n/a bytes
	table_row 'migrate received' "$_pm_recv" n/a bytes
	table_row 'drop-push' n/a n/a bytes
	table_row 'migrate duration' "$_pm_dur" "$migrate_bound_s" s
	table_row 'migrate puts' "$_pm_objs" n/a count
}

# phase_next_pull measures b's first pull after the migrate: one pull, the
# machine's Turso pull and R2 get bytes for that call, and the listing delta's
# PUT count (a pull writes no segments).
phase_next_pull() {
	snap sp-b "$state_dir/np-before.json"
	bucket_list > "$state_dir/np-keys-before"
	sb_sh sp-b > "$state_dir/np-pull.json" <<EOF
$sandbox_guard
relevo db sync pull --json
EOF
	snap sp-b "$state_dir/np-after.json"
	bucket_list > "$state_dir/np-keys-after"
	status_deltas sp-b "$state_dir/np-before.json" "$state_dir/np-after.json" > "$state_dir/np-deltas"
	read_deltas "$state_dir/np-deltas"
	table_row 'next pull turso' "$d_pull" n/a bytes
	table_row 'next pull r2get' "$d_get" n/a bytes
	table_row 'next pull gets' 'n/a (no counter)' n/a count
	table_row 'next pull lists' 'n/a (no counter)' n/a count
	_np_puts=$(new_segment_keys "$state_dir/np-keys-before" "$state_dir/np-keys-after" "$origin_b")
	table_row 'next pull puts' "$(count_lines "$_np_puts")" n/a count
}

# phase_fresh_join creates sp-d after the migrate and enables it full: the
# join's total Turso pull and R2 get bytes and its elapsed time. The verdict row
# asks whether the dropped log table's pages shipped: they would arrive as Turso
# pull bytes, so a join pulling materially less than the seeded log (under half,
# the table's own half threshold) shows they did not ship -- the history travels
# as R2 segments instead. The design predicts they do not ship (0).
phase_fresh_join() {
	create_joiner sp-d
	snap sp-d "$state_dir/fj-before.json"
	bucket_list > "$state_dir/fj-keys-before"
	_fj_start=$(date +%s)
	enable_joiner sp-d ''
	_fj_end=$(date +%s)
	snap sp-d "$state_dir/fj-after.json"
	bucket_list > "$state_dir/fj-keys-after"
	origin_d=$(record_new_origins | head -n1)
	[ -n "$origin_d" ] || refuse 'could not learn sp-d origin from the bucket after its join'
	status_deltas sp-d "$state_dir/fj-before.json" "$state_dir/fj-after.json" > "$state_dir/fj-deltas"
	read_deltas "$state_dir/fj-deltas"
	table_row 'fresh join turso' "$d_pull" n/a bytes
	table_row 'fresh join r2get' "$d_get" n/a bytes
	table_row 'fresh join time' "$(( _fj_end - _fj_start ))" n/a s
	table_row 'fresh join gets' 'n/a (no counter)' n/a count
	table_row 'fresh join lists' 'n/a (no counter)' n/a count
	_fj_puts=$(new_segment_keys "$state_dir/fj-keys-before" "$state_dir/fj-keys-after" "$origin_d")
	table_row 'fresh join puts' "$(count_lines "$_fj_puts")" n/a count
	_fj_seed=$(( log_mb * 1024 * 1024 ))
	_fj_ship=1
	if [ "$d_pull" -lt $(( _fj_seed / 2 )) ]; then
		_fj_ship=0
	fi
	table_row 'join log shipped' "$_fj_ship" 0 bool
}

# phase_idle_poll measures the idle shape: idle_poll_calls consecutive pulls on
# sp-a with no writes between. Any R2 byte movement flags (the design predicts
# no R2 operation at all); the operation count has no counter, so its row says
# so and the bytes row carries the flag. The per-call cost is the total Turso
# pull over the call count, integer division flooring the remainder.
phase_idle_poll() {
	snap sp-a "$state_dir/ip-before.json"
	bucket_list > "$state_dir/ip-keys-before"
	_ip_i=0
	while [ "$_ip_i" -lt "$idle_poll_calls" ]; do
		sb_sh sp-a >> "$state_dir/ip-pulls.jsonl" <<EOF
$sandbox_guard
relevo db sync pull --json
EOF
		_ip_i=$(( _ip_i + 1 ))
	done
	snap sp-a "$state_dir/ip-after.json"
	bucket_list > "$state_dir/ip-keys-after"
	status_deltas sp-a "$state_dir/ip-before.json" "$state_dir/ip-after.json" > "$state_dir/ip-deltas"
	read_deltas "$state_dir/ip-deltas"
	table_row 'idle poll turso' "$d_pull" n/a bytes
	table_row 'idle poll r2' "$(( d_put + d_get ))" 0 bytes
	table_row 'idle poll r2 ops' 'n/a (no counter)' 0 ops
	table_row 'idle poll/call' "$(( d_pull / idle_poll_calls ))" n/a bytes
	table_row 'idle poll gets' 'n/a (no counter)' n/a count
	table_row 'idle poll lists' 'n/a (no counter)' n/a count
	_ip_puts=$(new_segment_keys "$state_dir/ip-keys-before" "$state_dir/ip-keys-after" "$migrator_origin")
	table_row 'idle poll puts' "$(count_lines "$_ip_puts")" n/a count
}

# phase_day_machine ORIGIN LABEL emits one machine's compressed-day rows from
# the deltas the caller just read (d_push, d_pull, d_put, d_get): the four
# counters, the GET/LIST counts with no counter, and the PUT count from the
# listing delta under the machine's origin.
phase_day_machine() {
	table_row "day $2 push" "$d_push" n/a bytes
	table_row "day $2 pull" "$d_pull" n/a bytes
	table_row "day $2 r2put" "$d_put" n/a bytes
	table_row "day $2 r2get" "$d_get" n/a bytes
	table_row "day $2 gets" 'n/a (no counter)' n/a count
	table_row "day $2 lists" 'n/a (no counter)' n/a count
	_dm_puts=$(new_segment_keys "$state_dir/cd-keys-before" "$state_dir/cd-keys-after" "$1")
	table_row "day $2 puts" "$(count_lines "$_dm_puts")" n/a count
}

# phase_compressed_day generates the day's entries on sp-a, then b and sp-none
# pull. The segment count is the listing delta under sp-a's origin (the PUT
# count), against the design's ~150. sp-none reads the shared mirror only
# (internal/syncworker/pull.go), so its R2 get bytes and new-object reads must
# be zero: the get-bytes row asserts 0 and flags any movement; a read count has
# no counter, and any new-object read would move get bytes, so the bytes row is
# the zero proof for both. d_get holds sp-none's delta when the zero rows read
# it, sp-none being the last machine measured.
phase_compressed_day() {
	snap sp-a "$state_dir/cd-a-before.json"
	snap sp-b "$state_dir/cd-b-before.json"
	snap sp-none "$state_dir/cd-n-before.json"
	bucket_list > "$state_dir/cd-keys-before"
	gen_day_entries
	sb_sh sp-b > "$state_dir/cd-b-pull.json" <<EOF
$sandbox_guard
relevo db sync pull --json
EOF
	sb_sh sp-none > "$state_dir/cd-n-pull.json" <<EOF
$sandbox_guard
relevo db sync pull --json
EOF
	snap sp-a "$state_dir/cd-a-after.json"
	snap sp-b "$state_dir/cd-b-after.json"
	snap sp-none "$state_dir/cd-n-after.json"
	bucket_list > "$state_dir/cd-keys-after"
	_cd_segments=$(new_segment_keys "$state_dir/cd-keys-before" "$state_dir/cd-keys-after" "$migrator_origin")
	table_row 'day segments' "$(count_lines "$_cd_segments")" "$day_predicted_segments" segments
	status_deltas sp-a "$state_dir/cd-a-before.json" "$state_dir/cd-a-after.json" > "$state_dir/cd-a-deltas"
	read_deltas "$state_dir/cd-a-deltas"
	phase_day_machine "$migrator_origin" a
	status_deltas sp-b "$state_dir/cd-b-before.json" "$state_dir/cd-b-after.json" > "$state_dir/cd-b-deltas"
	read_deltas "$state_dir/cd-b-deltas"
	phase_day_machine "$origin_b" b
	table_row 'day b total' "$(( d_pull + d_get ))" "$day_full_client_predicted" bytes
	status_deltas sp-none "$state_dir/cd-n-before.json" "$state_dir/cd-n-after.json" > "$state_dir/cd-n-deltas"
	read_deltas "$state_dir/cd-n-deltas"
	phase_day_machine "$origin_none" none
	table_row 'none zero get' "$d_get" 0 bytes
	table_row 'none zero reads' 'n/a (no counter)' 0 count
}

# print_footnotes prints the table's reading notes: the figures with no counter
# or no design number, the predicted values' sources, and the verdict encodings.
# They travel with the table (stdout and table.txt) because a row's n/a or 0/1
# is unreadable without them.
print_footnotes() {
	cat <<EOF
notes:
- gets/lists/reads counts: n/a (no counter) -- the bucket keeps no GET or LIST
  counter; the bytes rows are the measured figure, and counts are never
  estimated from bytes.
- drop-push: the DROP TABLE log push is inside the commit transaction and
  shares the call's one turso_sent_bytes delta; no separate counter exists, so
  it is never split out by estimate.
- day b total predicted $day_full_client_predicted bytes = 2.3 MB, the midpoint
  of the design's 1.6-3.0 MB full-client day range (powers of 1000).
- day segments predicted ~$day_predicted_segments for $day_entries entries; the
  design's floor is $day_segment_floor and ceiling $day_segment_ceiling. FLAG
  marks a row more than twice or less than half its prediction.
- join log shipped: 1 when the fresh join's Turso pull reached half the seeded
  log bytes, 0 otherwise; the design predicts the dropped log's pages do not
  ship (0). The history travels as R2 segments instead (fresh join r2get).
- idle poll/call: the total Turso pull over $idle_poll_calls calls, integer
  division flooring the remainder; idle poll r2 predicted 0 -- any R2 byte
  movement flags.
- none zero get/reads: a history:none machine reads the shared mirror only, so
  its R2 get bytes and new-object reads are 0; any get-byte movement flags, and
  a read with no counter is witnessed by the bytes row.
- rows with n/a predicted carry no design figure (the join-after-year figures
  are not applicable); their flag column stays empty.
- a counter reset (worker restart) uses the after value as the delta and is
  noted on stderr.
EOF
}

# measure_phases runs the traffic phases in order after the migrate and prints
# the measured table: every row through print_table, to stdout and to the run's
# table.txt -- the deliverable the operator collects.
measure_phases() {
	measure_setup
	phase_migrate_rows
	phase_next_pull
	phase_fresh_join
	phase_idle_poll
	phase_compressed_day
	print_table < "$table_rows_file" > "$table_file"
	print_footnotes >> "$table_file"
	cat "$table_file"
}

# destroy_sandboxes destroys only the sandboxes this run recorded creating. A
# failed destroy is ignored with a note: cleanup must not replace the run's own
# exit status, and a sandbox left behind is the operator's to remove.
destroy_sandboxes() {
	[ -n "${sandboxes_file:-}" ] && [ -f "$sandboxes_file" ] || return 0
	while IFS= read -r _ds_name || [ -n "$_ds_name" ]; do
		[ -n "$_ds_name" ] || continue
		if ! "$sandbox_script" destroy "$_ds_name"; then
			printf '%s: cleanup could not destroy sandbox %s\n' "$prog" "$_ds_name" >&2
		fi
	done < "$sandboxes_file"
}

# run_phases drives the whole measured run: create the sandboxes, enable them
# (sp-a first, creating the key), learn each origin from its first push, seed the
# format-5 log around sp-a's origin, migrate from sp-a, then measure the traffic
# phases and print the table. Each origin is kept because the listing deltas
# count segments per origin.
run_phases() {
	sync_passphrase=$(gen_passphrase)
	create_sandboxes
	enable_first
	migrator_origin=$(record_new_origins | head -n1)
	[ -n "$migrator_origin" ] || refuse 'could not learn sp-a origin from the bucket after its first push'
	enable_joiner sp-b ''
	origin_b=$(record_new_origins | head -n1)
	[ -n "$origin_b" ] || refuse 'could not learn sp-b origin from the bucket after its first push'
	enable_joiner sp-none ' --history none'
	origin_none=$(record_new_origins | head -n1)
	[ -n "$origin_none" ] || refuse 'could not learn sp-none origin from the bucket after its first push'
	seed_log "$migrator_origin" "$log_mb"
	migrate_run
	measure_phases
	printf '%s: run complete (migrate recorded in %s, table in %s)\n' "$prog" "$migrate_file" "$table_file" >&2
}

# print_plan prints the ordered command plan a real run executes, for --dry-run.
# Secret sources are named but no value appears: the token and passphrase travel
# by stdin, the R2 secret by RELEVO_R2_SECRET, the passphrase for the joiners by
# RELEVO_SYNC_PASSPHRASE, and the URL/endpoint/bucket/key-id by the scratch env
# variables. Each in-sandbox snippet starts with the guard line. This plan and
# the enable/migrate functions and the traffic phases above must agree on the
# verb sequence.
print_plan() {
	printf 'plan: create sandboxes (from this checkout root)\n'
	# shellcheck disable=SC2086 # the names are a space-separated list to iterate
	for _pp_name in $sandbox_names; do
		printf '%s create %s\n' "$sandbox_script" "$_pp_name"
	done
	printf 'plan: enable sp-a (first machine; token: stdin, r2.secret: RELEVO_R2_SECRET, no passphrase)\n'
	printf '. %s/sp-a/env.sh\n' "$sandbox_root"
	printf '%s\n' "$sandbox_guard"
	printf 'relevo db sync enable --url "$RELEVO_SCRATCH_SYNC_URL" --token-stdin --r2-endpoint "$RELEVO_SCRATCH_R2_ENDPOINT" --r2-bucket "$RELEVO_SCRATCH_R2_BUCKET" --r2-key-id "$RELEVO_SCRATCH_R2_KEY_ID"\n'
	printf 'plan: unlock sp-a --new (passphrase: stdin, generated per run)\n'
	printf '%s\n' "$sandbox_guard"
	printf 'relevo db sync unlock --new --passphrase-stdin\n'
	printf 'plan: push sp-a (first push)\n'
	printf '%s\n' "$sandbox_guard"
	printf 'relevo db sync push --json\n'
	printf 'plan: enable sp-b (joiner; token: stdin, r2.secret: RELEVO_R2_SECRET, passphrase: RELEVO_SYNC_PASSPHRASE)\n'
	printf '. %s/sp-b/env.sh\n' "$sandbox_root"
	printf '%s\n' "$sandbox_guard"
	printf 'relevo db sync enable --url "$RELEVO_SCRATCH_SYNC_URL" --token-stdin --r2-endpoint "$RELEVO_SCRATCH_R2_ENDPOINT" --r2-bucket "$RELEVO_SCRATCH_R2_BUCKET" --r2-key-id "$RELEVO_SCRATCH_R2_KEY_ID"\n'
	printf 'plan: push sp-b (first push)\n'
	printf '%s\n' "$sandbox_guard"
	printf 'relevo db sync push --json\n'
	printf 'plan: enable sp-none (joiner, --history none; same secret sources as sp-b)\n'
	printf '. %s/sp-none/env.sh\n' "$sandbox_root"
	printf '%s\n' "$sandbox_guard"
	printf 'relevo db sync enable --url "$RELEVO_SCRATCH_SYNC_URL" --token-stdin --r2-endpoint "$RELEVO_SCRATCH_R2_ENDPOINT" --r2-bucket "$RELEVO_SCRATCH_R2_BUCKET" --r2-key-id "$RELEVO_SCRATCH_R2_KEY_ID" --history none\n'
	printf 'plan: push sp-none (first push)\n'
	printf '%s\n' "$sandbox_guard"
	printf 'relevo db sync push --json\n'
	printf 'plan: learn each origin from the bucket after its first push; append to origins\n'
	printf 'plan: seed format-5 log for the sp-a origin, %s batches of %s rows (--log-mb %s)\n' "$(seed_batches "$log_mb")" "$seed_batch_rows" "$log_mb"
	printf 'plan: migrate from sp-a\n'
	printf '. %s/sp-a/env.sh\n' "$sandbox_root"
	printf '%s\n' "$sandbox_guard"
	printf 'relevo db sync migrate --confirm-all-upgraded --json\n'
	printf 'plan: next pull on sp-b (one pull after the migrate)\n'
	printf '. %s/sp-b/env.sh\n' "$sandbox_root"
	printf '%s\n' "$sandbox_guard"
	printf 'relevo db sync pull --json\n'
	printf 'plan: fresh join sp-d (create after the migrate; enable full, first push)\n'
	printf '%s create sp-d\n' "$sandbox_script"
	printf '. %s/sp-d/env.sh\n' "$sandbox_root"
	printf '%s\n' "$sandbox_guard"
	printf 'relevo db sync enable --url "$RELEVO_SCRATCH_SYNC_URL" --token-stdin --r2-endpoint "$RELEVO_SCRATCH_R2_ENDPOINT" --r2-bucket "$RELEVO_SCRATCH_R2_BUCKET" --r2-key-id "$RELEVO_SCRATCH_R2_KEY_ID"\n'
	printf 'plan: push sp-d (first push)\n'
	printf '%s\n' "$sandbox_guard"
	printf 'relevo db sync push --json\n'
	printf 'plan: idle poll on sp-a: %s consecutive pulls, no writes between\n' "$idle_poll_calls"
	printf '. %s/sp-a/env.sh\n' "$sandbox_root"
	printf '%s\n' "$sandbox_guard"
	printf 'relevo db sync pull --json\n'
	printf 'plan: compressed day on sp-a: %s entries in bursts of %s (one config set per entry, one push per burst)\n' "$day_entries" "$day_burst"
	printf '. %s/sp-a/env.sh\n' "$sandbox_root"
	printf '%s\n' "$sandbox_guard"
	printf 'relevo config set repos.<64-hex-id> '\''{"name":"sync-proof day entry <n> <varied word>"}'\''\n'
	printf '%s\n' "$sandbox_guard"
	printf 'relevo db sync push --json\n'
	printf 'plan: day pulls (sp-b, then sp-none)\n'
	printf '. %s/sp-b/env.sh\n' "$sandbox_root"
	printf '%s\n' "$sandbox_guard"
	printf 'relevo db sync pull --json\n'
	printf '. %s/sp-none/env.sh\n' "$sandbox_root"
	printf '%s\n' "$sandbox_guard"
	printf 'relevo db sync pull --json\n'
	printf 'plan: measured table: status snapshots and bucket listings around every phase; rows through print_table to stdout and <state-dir>/table.txt\n'
}

main() {
	parse_args "$@"
	load_env "$env_file"
	resolve_tools
	estimate=$(run_estimate "$log_mb")
	if [ "$dry_run" = 1 ]; then
		printf 'env file: %s\n' "$env_file"
	fi
	printf 'estimated run traffic: %s bytes (log %s MiB)\n' "$estimate" "$log_mb"
	quota_read
	# The estimate is printed before the refusal check; the 95% line is
	# floored to whole bytes, which is exact for the integer comparison
	# quota_allows makes.
	_m_line=$(( quota * 95 / 100 ))
	if quota_allows "$projected" "$quota" "$estimate"; then
		_m_quota_verdict=allow
	else
		_m_quota_verdict=refuse
	fi
	if [ "$dry_run" = 1 ]; then
		printf 'quota: %s (projected %s + estimate %s, 95%% line %s of quota %s)\n' \
			"$_m_quota_verdict" "$projected" "$estimate" "$_m_line" "$quota"
	elif [ "$_m_quota_verdict" = refuse ]; then
		refuse "quota refusal: projected $projected + estimate $estimate exceeds the 95% line $_m_line of quota $quota"
	fi
	state_setup
	_m_tables=$(turso_query "SELECT name FROM sqlite_master WHERE type='table'")
	_m_marker=''
	if printf '%s\n' "$_m_tables" | grep -qxF meta; then
		_m_marker=$(turso_query "SELECT value FROM meta WHERE key='syncproof.run'")
	fi
	if printf '%s\n' "$_m_tables" | scratch_turso_owned "$_m_marker" "$runs_file"; then
		_m_turso_verdict=owned
	else
		_m_turso_verdict='not owned'
	fi
	if [ "$dry_run" = 1 ]; then
		if [ "$_m_turso_verdict" = owned ]; then
			printf 'turso: owned\n'
		else
			printf 'turso: not owned (tables: %s)\n' "$(printf '%s\n' "$_m_tables" | join_lines)"
		fi
	elif [ "$_m_turso_verdict" = 'not owned' ]; then
		refuse "scratch Turso is not this script's: tables found: $(printf '%s\n' "$_m_tables" | join_lines) (--wipe does not waive the Turso refusal)"
	fi
	_m_keys=$(bucket_list)
	_m_foreign=$(printf '%s\n' "$_m_keys" | bucket_foreign "$origins_file")
	if [ "$dry_run" = 1 ]; then
		if [ -z "$_m_foreign" ]; then
			printf 'bucket: owned\n'
		else
			printf 'bucket: not owned (foreign origins: %s)\n' "$(printf '%s\n' "$_m_foreign" | join_lines)"
		fi
		print_plan
		exit 0
	fi
	if [ -n "$_m_foreign" ]; then
		if [ "$wipe" = 1 ]; then
			_m_keys=$(printf '%s\n' "$_m_keys" | bucket_wipe "$origins_file")
		else
			refuse "bucket holds foreign origins: $(printf '%s\n' "$_m_foreign" | join_lines)"
		fi
	fi
	# The origins record stays as state_setup seeded it: recording the
	# post-wipe listing would adopt the foreign origins the wipe left
	# behind, and the record is the whole of what this script may delete.
	# The run phases execute only under SYNC_PROOF_RUN_PHASES=1 (the measured
	# run); without it the script stops here after the reads and any
	# owned-origin deletes, so a test or casual invocation never creates a
	# sandbox or reaches a network.
	if [ "${SYNC_PROOF_RUN_PHASES:-0}" = 1 ]; then
		run_phases
		exit 0
	fi
	printf '%s: run phases are not implemented for this invocation (set SYNC_PROOF_RUN_PHASES=1 to run them; wipe=%s dry-run=%s)\n' "$prog" "$wipe" "$dry_run" >&2
	exit 2
}

# Sourcing with SYNC_PROOF_SOURCED set defines the functions and runs nothing,
# so the test drives the pure core without a run; executing the script leaves
# it unset and main runs.
if [ -z "${SYNC_PROOF_SOURCED:-}" ]; then
	main "$@"
fi
