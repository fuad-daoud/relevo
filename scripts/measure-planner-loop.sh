#!/bin/sh
# scripts/measure-planner-loop.sh -- the canonical planner loop, measured (#505, spec §7).
#
# Usage: measure-planner-loop.sh DIR [CANDIDATE] [--name NAME] [--plan FILE]
#        [--json] [--transcript FILE | --session ID] [--timeout DUR]
#   DIR          a scratch git working tree to run the loop in
#   CANDIDATE    the token bind takes; omit for the first ungated candidate
#   --name       binding name (default: baseline- plus DIR basename, <=24 chars)
#   --plan       the plan file to send (default: a generated probe plan)
#   --json       run the six steps in their agent form: json mode needs the
#                post-#505 binary and is measured on the after side only
#   --transcript a Claude Code session JSONL to sum driver tokens from
#   --session    an OpenCode session id to sum driver tokens from (sqlite3)
#   --timeout    wait --timeout (default 30m)
#
# Methodology
# -----------
# Six counted processes, one each, in order: bind, send, wait, show --report,
# show --diff --stat, done. cd DIR first. Each step's stdout goes to a temp file
# and is counted in bytes; each step is one CLI invocation. stderr goes to a
# second temp file and is NOT counted: it is never shown on success and only its
# last lines are printed for the step that stopped the loop. One uncounted
# `relevo version` probe fills the header and is excluded from the rows; the
# script's own report is not counted either. The script parses no relevo output:
# only exit codes and byte counts, so the same file is valid before and after
# the #505 change.
#
# Driver tokens come from --transcript (a Claude Code session JSONL: one usage
# object per assistant message id) or --session (OpenCode session_v2 through
# sqlite3); --transcript wins and neither is required. The source is read once
# before step 1 and again after the last step executed (completion or stop), and
# the delta is printed. An unavailable source never stops the loop and never
# changes the exit code.
#
# Exit: 0 when all six steps ran; otherwise the failing step's code, wait's
# protocol codes included (2 unmarked, 3 needs you, 4 gone, 5 halted, 6 not
# started, 124 timeout); 2 for usage and a refused precondition, before the first
# relevo process runs.
set -eu

prog=measure-planner-loop.sh
relevo_bin=${RELEVO_BIN:-relevo}
opencode_db=${RELEVO_OPENCODE_DB:-${XDG_DATA_HOME:-${HOME:-/nonexistent}/.local/share}/opencode/opencode.db}

opt_name=''
opt_plan=''
opt_json=''
opt_transcript=''
opt_session=''
opt_timeout=30m
dir=''
candidate=''
positional=0

usage() {
	printf '%s\n' \
		'usage: measure-planner-loop.sh DIR [CANDIDATE] [--name NAME] [--plan FILE] [--json] [--transcript FILE | --session ID] [--timeout DUR]' \
		'' \
		'  DIR          a scratch git working tree to run the loop in' \
		'  CANDIDATE    the token bind takes; omit for the first ungated candidate' \
		'  --name       binding name (default: baseline- plus DIR basename, <=24 chars)' \
		'  --plan       the plan file to send (default: a generated probe plan)' \
		'  --json       run the six steps in their agent form: json mode needs the' \
		'               post-#505 binary and is measured on the after side only' \
		'  --transcript a Claude Code session JSONL to sum driver tokens from' \
		'  --session    an OpenCode session id to sum driver tokens from (sqlite3)' \
		'  --timeout    wait --timeout (default 30m)' \
		'' \
		'Env: RELEVO_BIN (default relevo), RELEVO_OPENCODE_DB (default: the OpenCode' \
		'     store under the XDG data home, opencode/opencode.db).'
}

# refuse prints the reason and the usage on stderr and exits 2. Every refusal
# happens before the first relevo process, so a refused run proves nothing ran.
refuse() {
	printf '%s: %s\n' "$prog" "$1" >&2
	usage >&2
	exit 2
}

while [ $# -gt 0 ]; do
	case $1 in
	--help | -h)
		usage
		exit 0
		;;
	--json)
		opt_json=1
		;;
	--name)
		[ $# -ge 2 ] || refuse "option --name needs a value"
		opt_name=$2
		shift
		;;
	--plan)
		[ $# -ge 2 ] || refuse "option --plan needs a value"
		opt_plan=$2
		shift
		;;
	--transcript)
		[ $# -ge 2 ] || refuse "option --transcript needs a value"
		opt_transcript=$2
		shift
		;;
	--session)
		[ $# -ge 2 ] || refuse "option --session needs a value"
		opt_session=$2
		shift
		;;
	--timeout)
		[ $# -ge 2 ] || refuse "option --timeout needs a value"
		opt_timeout=$2
		shift
		;;
	-*)
		refuse "unknown argument: $1"
		;;
	*)
		positional=$((positional + 1))
		if [ "$positional" -eq 1 ]; then
			dir=$1
		elif [ "$positional" -eq 2 ]; then
			candidate=$1
		else
			refuse "too many arguments: $1"
		fi
		;;
	esac
	shift
done

[ -n "$dir" ] || refuse "DIR is required"
[ -d "$dir" ] || refuse "not a directory: $dir"
git -C "$dir" rev-parse --git-dir >/dev/null 2>&1 ||
	refuse "not a git working tree: $dir"
if [ -n "$opt_transcript" ] && [ -n "$opt_session" ]; then
	refuse "--transcript and --session are exclusive"
fi

# The steps run cd'd into DIR, so every path handed to relevo is made absolute
# first: a relative --plan would otherwise resolve against DIR.
abspath() {
	case $1 in
	/*) printf '%s\n' "$1" ;;
	*) printf '%s\n' "$PWD/$1" ;;
	esac
}
if [ -n "$opt_plan" ]; then
	opt_plan=$(abspath "$opt_plan")
	[ -f "$opt_plan" ] || refuse "--plan is not a file: $opt_plan"
fi
if [ -n "$opt_transcript" ]; then
	opt_transcript=$(abspath "$opt_transcript")
	[ -f "$opt_transcript" ] || refuse "--transcript is not a file: $opt_transcript"
fi

# RELEVO_BIN must resolve before anything runs, and is resolved to an absolute
# path because the steps cd away from the caller's directory.
if ! command -v "$relevo_bin" >/dev/null 2>&1; then
	refuse "RELEVO_BIN does not resolve: $relevo_bin"
fi
relevo_path=$(command -v "$relevo_bin")
case $relevo_path in
/*) ;;
*) relevo_path=$PWD/$relevo_path ;;
esac

if ! abs_dir=$(cd "$dir" 2>/dev/null && pwd); then
	refuse "cannot enter DIR: $dir"
fi

# NAME: baseline- plus the DIR basename, lowercased, every non-[a-z0-9_-] byte
# turned into '-', the trailing run of '-' trimmed, at most 24 characters (the
# store caps a name at 32 and bind appends -builder, so 24 keeps the pair legal).
if [ -n "$opt_name" ]; then
	name=$opt_name
else
	slug=$(printf '%s' "baseline-$(basename "$dir")" | tr '[:upper:]' '[:lower:]')
	slug=$(printf '%s' "$slug" | tr -c 'a-z0-9_-' '-')
	slug=$(printf '%s' "$slug" | sed 's/-\{1,\}$//')
	name=$(printf '%s' "$slug" | cut -c1-24)
fi

if [ -n "$opt_json" ]; then
	mode=json
else
	mode=human
fi

work=$(mktemp -d)
# The cleanup must not decide the verdict: in dash a failing EXIT trap's status
# replaces the script's own.
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

# PLAN: a generated temp plan whose builder creates PROBE-<name>.md in the
# working tree, so show --diff --stat has a body. --plan overrides it.
if [ -n "$opt_plan" ]; then
	plan=$opt_plan
else
	plan=$work/plan.md
	{
		printf '# measure-planner-loop probe\n\n'
		printf 'Create a new file named PROBE-%s.md in the root of this working tree.\n' "$name"
		printf 'Write exactly this one line in it:\n\n'
		printf '    measure-planner-loop probe %s\n\n' "$name"
		printf 'Change nothing else. This is a scratch probe.\n'
	} >"$plan"
fi

if [ -n "$candidate" ]; then
	candidate_display=$candidate
else
	candidate_display='(default: first ungated candidate)'
fi

# --- token sources ----------------------------------------------------------

# token_read sets token_value to a count and token_status to '' on success, or
# leaves token_value empty and token_status to the reason it is unavailable.
token_read() {
	token_value=''
	token_status=''
	if [ -n "$opt_transcript" ]; then
		if [ ! -r "$opt_transcript" ]; then
			token_status="transcript $opt_transcript unreadable"
			return 0
		fi
		if token_value=$(awk '
			{
				line = $0
				sub(/\r$/, "", line)
				gsub(/[ \t]/, "", line)
				if (line !~ /"type":"assistant"/) next
				if (!match(line, /"message":\{/)) next
				msg = substr(line, RSTART)
				if (!match(msg, /"id":"[^"]*"/)) next
				id = substr(msg, RSTART + 6, RLENGTH - 7)
				if (id in seen) next
				seen[id] = 1
				if (!match(msg, /"usage":\{[^}]*\}/)) next
				us = substr(msg, RSTART, RLENGTH)
				n = split("input_tokens cache_creation_input_tokens cache_read_input_tokens output_tokens", keys, " ")
				for (i = 1; i <= n; i++) {
					if (match(us, "\"" keys[i] "\":[0-9]+")) {
						v = substr(us, RSTART, RLENGTH)
						sub(/^"[^"]*":/, "", v)
						total += v + 0
						found = 1
					}
				}
			}
			END {
				if (!found) exit 3
				printf "%d", total
			}
		' "$opt_transcript"); then
			token_desc="claude transcript $opt_transcript: input+cache_creation+cache_read+output, one per message id"
			return 0
		fi
		token_value=''
		token_status="no usage records in transcript $opt_transcript"
		return 0
	fi
	if [ -n "$opt_session" ]; then
		if ! command -v sqlite3 >/dev/null 2>&1; then
			token_status="sqlite3 not on PATH"
			return 0
		fi
		if [ ! -r "$opencode_db" ]; then
			token_status="no OpenCode db at $opencode_db"
			return 0
		fi
		sqlerr=$work/sqlite.err
		sql="SELECT tokens_input+tokens_output+tokens_reasoning+tokens_cache_read+tokens_cache_write FROM session_v2 WHERE id = '$opt_session'"
		if token_value=$(sqlite3 -readonly "$opencode_db" "$sql" 2>"$sqlerr"); then
			if [ -z "$token_value" ]; then
				token_status="session $opt_session not in session_v2"
			else
				token_desc="opencode session $opt_session: the five token columns summed"
			fi
			return 0
		fi
		token_value=''
		token_status="sqlite3 failed: $(sed -n '1p' "$sqlerr")"
		return 0
	fi
	token_status='no --transcript or --session given'
	return 0
}

# --- counting ---------------------------------------------------------------

ran=0
total_inv=0
total_bytes=0
stop_label=''
stop_code=0
stop_err=''
before_value=0
before_status=''
after_value=0
after_status=''
token_desc=''
token_value=''
token_status=''

# finish_stop reads the token source after the last step executed, prints the
# partial report and exits with the failing step's code.
finish_stop() {
	token_read
	after_value=$token_value
	after_status=$token_status
	print_report
	exit "$stop_code"
}

# run_step LABEL ARGV... runs ARGV once, counts its stdout bytes and records its
# row and its argv exactly as run. It never returns non-zero: a failing step
# prints the partial report from finish_stop and exits.
run_step() {
	step_label=$1
	shift
	ran=$((ran + 1))
	step_out=$work/out.$ran
	step_err=$work/err.$ran
	code=0
	if "$@" >"$step_out" 2>"$step_err"; then
		code=0
	else
		code=$?
	fi
	bytes=$(wc -c <"$step_out" | tr -d ' ')
	total_inv=$((total_inv + 1))
	total_bytes=$((total_bytes + bytes))
	printf '%s\t1\t%s\n' "$step_label" "$bytes" >>"$work/rows"
	printf '  %d %s\n' "$ran" "$*" >>"$work/cmds"
	if [ "$code" -ne 0 ]; then
		stop_label=$step_label
		stop_code=$code
		stop_err=$step_err
		finish_stop
	fi
	return 0
}

print_report() {
	printf 'measure-planner-loop: the canonical planner loop (spec §7)\n'
	printf 'date: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
	printf 'mode: %s\n' "$mode"
	printf 'relevo: %s\n' "$relevo_version"
	printf 'dir: %s\n' "$abs_dir"
	printf 'binding: %s\n' "$name"
	printf 'candidate: %s\n' "$candidate_display"
	printf 'timeout: %s\n' "$opt_timeout"
	printf 'plan: %s\n' "$plan"
	printf 'stderr is not counted\n'
	printf '\ncommands (exactly as run):\n'
	cat "$work/cmds"
	tab=$(printf '\t')
	printf '\n%-18s %11s %12s\n' step invocations stdout_bytes
	while IFS="$tab" read -r row_label row_inv row_bytes; do
		printf '%-18s %11s %12s\n' "$row_label" "$row_inv" "$row_bytes"
	done <"$work/rows"
	printf '%-18s %11s %12s\n' total "$total_inv" "$total_bytes"
	printf '\n'
	if [ -z "$before_status" ] && [ -z "$after_status" ]; then
		printf 'driver tokens: %+d (%s)\n' "$((after_value - before_value))" "$token_desc"
	else
		reason=$before_status
		[ -n "$reason" ] || reason=$after_status
		printf 'driver tokens: unavailable (%s)\n' "$reason"
	fi
	if [ -n "$stop_label" ]; then
		printf 'stopped at %s (exit %s)\n' "$stop_label" "$stop_code"
		if [ -s "$stop_err" ]; then
			printf 'stderr (last 10 lines of %s):\n' "$stop_label"
			tail -n 10 "$stop_err"
		else
			printf 'stderr (last 10 lines of %s): (empty)\n' "$stop_label"
		fi
	fi
}

# --- the loop ---------------------------------------------------------------

# The header's version probe is the first relevo process, uncounted, and it runs
# before the first token read so its own effect on a bumping driver is inside
# the baseline value, not the delta.
version_out=$work/version.out
version_err=$work/version.err
version_code=0
if "$relevo_path" version >"$version_out" 2>"$version_err"; then
	version_code=0
else
	version_code=$?
fi
if [ "$version_code" -eq 0 ]; then
	relevo_version=$(sed -n '1p' "$version_out")
	[ -n "$relevo_version" ] || relevo_version='(no version line)'
else
	relevo_version="(relevo version exited $version_code)"
fi

token_read
before_value=$token_value
before_status=$token_status

cd "$abs_dir"

# 1 bind -- the candidate is passed only when one was given, and --json is the
# one addition the agent form makes.
set -- bind --name "$name" --no-feature --no-gate
if [ -n "$candidate" ]; then
	set -- "$@" --candidate "$candidate"
fi
if [ -n "$opt_json" ]; then
	set -- "$@" --json
fi
run_step bind "$relevo_path" "$@"

# 2 send
set -- send --name "$name" --file "$plan" --no-verify
if [ -n "$opt_json" ]; then
	set -- "$@" --json
fi
run_step send "$relevo_path" "$@"

# 3 wait
set -- wait --name "$name" --timeout "$opt_timeout"
if [ -n "$opt_json" ]; then
	set -- "$@" --json
fi
run_step wait "$relevo_path" "$@"

# 4 show --report
set -- show "$name" --report
if [ -n "$opt_json" ]; then
	set -- "$@" --json
fi
run_step 'show --report' "$relevo_path" "$@"

# 5 show --diff --stat (--stat is a human truncation; the --json ShowResult is
# the agent's answer, so it is dropped in json mode)
set -- show "$name" --diff
if [ -n "$opt_json" ]; then
	set -- "$@" --json
else
	set -- "$@" --stat
fi
run_step 'show --diff --stat' "$relevo_path" "$@"

# 6 done
set -- 'done' "$name"
if [ -n "$opt_json" ]; then
	set -- "$@" --json
fi
run_step 'done' "$relevo_path" "$@"

token_read
after_value=$token_value
after_status=$token_status
print_report
