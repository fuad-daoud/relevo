#!/bin/sh
# scripts/sync-proof.sh -- prove a scratch remote can carry a relevo sync run
# without spending the operator's real quota: seed a log of a chosen size,
# migrate it, and measure the traffic against the remote's quota.
#
# This file is the pure core: argument and env parsing, the quota arithmetic,
# the run's own traffic estimate and the measured-traffic table. Sourcing it
# with SYNC_PROOF_SOURCED set defines the functions and runs nothing, so the
# test script drives each one with no network, no sandbox and no credentials.
# The run phases are not implemented; main parses, loads the env, prints the
# estimate and stops with exit 2.
#
# No env value is ever printed: a refusal names the missing variables, never
# their contents, so a secret cannot reach a terminal or a log through here.
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
  --wipe        remove the scratch remote's log before seeding
  --dry-run     print the actions and write nothing
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

main() {
	parse_args "$@"
	load_env "$env_file"
	estimate=$(run_estimate "$log_mb")
	printf 'estimated run traffic: %s bytes (log %s MiB)\n' "$estimate" "$log_mb"
	# --wipe and --dry-run are parsed, but the phases that would honor them are
	# not implemented, so every invocation stops here with the same exit; the
	# notice only reports their parsed state.
	printf '%s: run phases are not implemented (wipe=%s dry-run=%s)\n' "$prog" "$wipe" "$dry_run" >&2
	exit 2
}

# Sourcing with SYNC_PROOF_SOURCED set defines the functions and runs nothing,
# so the test drives the pure core without a run; executing the script leaves
# it unset and main runs.
if [ -z "${SYNC_PROOF_SOURCED:-}" ]; then
	main "$@"
fi
