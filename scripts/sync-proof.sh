#!/bin/sh
# scripts/sync-proof.sh -- prove a scratch remote can carry a relevo sync run
# without spending the operator's real quota: seed a log of a chosen size,
# migrate it, and measure the traffic against the remote's quota.
#
# The pure core (argument and env parsing, the quota arithmetic, the run's own
# traffic estimate, the measured-traffic table) plus the read-only preflight:
# the account quota read from the developer's installed relevo, the scratch
# Turso ownership check and the bucket ownership check. Sourcing it with
# SYNC_PROOF_SOURCED set defines the functions and runs nothing, so the test
# script drives each one with no network, no sandbox and no credentials. The
# seed/migrate/measure phases are not implemented; a real run stops after the
# preflight with exit 2, and --dry-run prints the verdicts and creates nothing.
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
                bucket ownership verdicts; create nothing
  --help        this help

The env file must set RELEVO_SCRATCH_SYNC_URL, RELEVO_SCRATCH_SYNC_TOKEN,
RELEVO_SCRATCH_R2_ENDPOINT, RELEVO_SCRATCH_R2_BUCKET, RELEVO_SCRATCH_R2_KEY_ID
and RELEVO_SCRATCH_R2_SECRET. A refusal names the missing variables only;
no value is ever printed.
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

# print_table reads name<TAB>measured<TAB>predicted<TAB>unit rows on stdin and
# prints the measured-traffic table, its flag column driven by flag_ratio. The
# tab IFS splits each row on tabs only, so a name carrying spaces stays one
# field. `|| [ -n "$_pt_name" ]` reads a final row that has no trailing newline.
print_table() {
	_pt_tab=$(printf '\t')
	printf '%-16s %14s %14s %-6s %s\n' name measured predicted unit flag
	while IFS="$_pt_tab" read -r _pt_name _pt_measured _pt_predicted _pt_unit || [ -n "$_pt_name" ]; do
		[ -n "$_pt_name" ] || continue
		_pt_flag=$(flag_ratio "$_pt_measured" "$_pt_predicted")
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
# it) and the origins file starts empty: a fresh run owns no origin yet.
state_setup() {
	run_id=$(date -u +%Y%m%dT%H%M%SZ)-$$
	state_dir=${TMPDIR:-/tmp}/sync-proof-$run_id
	r2_cred_file=$state_dir/r2-credentials
	turso_header_file=$state_dir/turso-header
	runs_file=$state_dir/runs
	origins_file=$state_dir/origins
	pipeline_url=$(turso_endpoint "$RELEVO_SCRATCH_SYNC_URL")
	trap cleanup EXIT
	mkdir -p "$state_dir"
	chmod 700 "$state_dir"
	(umask 077; printf 'user = "%s:%s"\n' "$RELEVO_SCRATCH_R2_KEY_ID" "$RELEVO_SCRATCH_R2_SECRET" > "$r2_cred_file")
	(umask 077; printf 'Authorization: Bearer %s\n' "$RELEVO_SCRATCH_SYNC_TOKEN" > "$turso_header_file")
	printf '%s\n' "$run_id" > "$runs_file"
	: > "$origins_file"
}

# cleanup removes the credential files so they never outlive the run; a
# dry-run removes its whole state dir, since a preview must leave nothing
# behind. A failed removal is ignored: the run's own verdict and exit status
# stand, and the files are unreachable once the process exits anyway.
cleanup() {
	rm -f "$r2_cred_file" "$turso_header_file" 2>/dev/null || :
	if [ "${dry_run:-0}" = 1 ]; then
		rm -rf "$state_dir" 2>/dev/null || :
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
	# --wipe and --dry-run are honored by the preflight; the seed/migrate/measure
	# phases are not implemented, so a real run stops here after the reads (and
	# any owned-origin deletes) with the same exit.
	printf '%s: run phases are not implemented (wipe=%s dry-run=%s)\n' "$prog" "$wipe" "$dry_run" >&2
	exit 2
}

# Sourcing with SYNC_PROOF_SOURCED set defines the functions and runs nothing,
# so the test drives the pure core without a run; executing the script leaves
# it unset and main runs.
if [ -z "${SYNC_PROOF_SOURCED:-}" ]; then
	main "$@"
fi
