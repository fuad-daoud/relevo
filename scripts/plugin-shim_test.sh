#!/bin/sh
# scripts/plugin-shim_test.sh -- pins the plugin's PATH shims.
#
# Every entrypoint under claude-plugin/scripts is a tiny POSIX shim that checks
# for the relevo binary first: with it the shim execs relevo and passes stdio
# and arguments through untouched; without it the hooks stay silent, the MCP
# server prints one hint on stderr, and the commands print the same hint on
# stdout. This test needs no real relevo: a stub on PATH records the argv each
# shim hands it.
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
plug="$here/../claude-plugin"

work=$(mktemp -d)
# The cleanup must not decide the verdict: in dash a failing EXIT trap's status
# replaces the script's own.
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

fail=0
bad() { echo "FAIL: $*" >&2; fail=1; }

hint='relevo plugin: the relevo binary is not on PATH. Install it: https://github.com/fuad-daoud/relevo#install'
printf '%s\n' "$hint" > "$work/hintline"

# --- 1. every shim is executable -------------------------------------------

for f in "$plug"/scripts/*.sh; do
	if [ ! -f "$f" ]; then
		bad "no scripts under $plug/scripts"
		break
	fi
	if [ ! -x "$f" ]; then
		bad "$(basename "$f") is not executable"
	fi
done

# --- 2. no command substitution, backticks or parameter expansion ----------
# A shim uses no $(, backtick or ${. Its only variable is the accepted "$@"
# hold in show.sh and status.sh, used once, in the exec line; the doc comment
# above that line names the variable too, so count only non-comment lines.

for f in "$plug"/scripts/*.sh; do
	if [ ! -f "$f" ]; then
		break
	fi
	base=$(basename "$f")
	if grep -F -q "\$(" "$f"; then
		bad "$base contains a command substitution"
	fi
	if grep -F -q '`' "$f"; then
		bad "$base contains a backtick"
	fi
	if grep -F -q "\${" "$f"; then
		bad "$base contains a parameter expansion"
	fi
	uses=$(grep -v '^[[:space:]]*#' "$f" | grep -F -c '"$@"' || true)
	case $base in
	show.sh | status.sh)
		[ "$uses" -eq 1 ] || bad "$base uses \"\$@\" $uses time(s), want 1"
		;;
	*)
		[ "$uses" -eq 0 ] || bad "$base uses \"\$@\" $uses time(s), want 0"
		;;
	esac
done

# --- 3. the manifests and every command file point at the scripts ----------

hooks="$plug/hooks/hooks.json"
pjson="$plug/.claude-plugin/plugin.json"

n=$(grep -F -o "\${CLAUDE_PLUGIN_ROOT}/scripts/" "$hooks" | wc -l | tr -d ' ')
[ "$n" -eq 2 ] || bad "hooks.json names $n script command(s), want exactly 2"
grep -F -q "\"command\": \"\\\"\${CLAUDE_PLUGIN_ROOT}/scripts/mastermind-init.sh\\\"\"" "$hooks" ||
	bad "hooks.json SessionStart does not run mastermind-init.sh"
grep -F -q "\"command\": \"\\\"\${CLAUDE_PLUGIN_ROOT}/scripts/mastermind-notice.sh\\\"\"" "$hooks" ||
	bad "hooks.json UserPromptSubmit does not run mastermind-notice.sh"
if grep -F -q '"command": "relevo"' "$hooks"; then
	bad "hooks.json still runs the bare relevo command"
fi

grep -F -q "\"relevo\": {\"command\": \"\${CLAUDE_PLUGIN_ROOT}/scripts/mcp.sh\"}" "$pjson" ||
	bad "plugin.json mcpServers.relevo.command is not the mcp.sh script"
if grep -F -q '"command": "relevo"' "$pjson"; then
	bad "plugin.json still runs the bare relevo command"
fi

for f in "$plug"/commands/*.md; do
	if [ ! -f "$f" ]; then
		bad "no command files under $plug/commands"
		break
	fi
	base=$(basename "$f" .md)
	script="\${CLAUDE_PLUGIN_ROOT}/scripts/$base.sh"
	case $base in
	show | status) line="$script \$ARGUMENTS" ;;
	*) line="$script" ;;
	esac
	n=$(grep -F -x -c "$line" "$f" || true)
	[ "$n" -eq 1 ] || bad "$base.md has $n script line(s), want 1"
	if ! grep -F -q "allowed-tools: Bash($script:*)" "$f"; then
		bad "$base.md allowed-tools does not name $base.sh"
	fi
done

# first_body_line prints the first non-empty line after a leading `---` pair.
first_body_line() {
	awk '
		/^---[[:space:]]*$/ { seen++; next }
		seen >= 2 && NF { print; exit }
	' "$1"
}

guard="relevo MCP tools are not available"
for f in "$plug"/commands/*.md "$plug"/skills/*/SKILL.md; do
	if [ ! -f "$f" ]; then
		bad "no command files or skills to guard"
		break
	fi
	got=$(first_body_line "$f")
	case $got in
	*"$guard"*) ;;
	*) bad "$(basename "$f"): the first body line is not the guard: $got" ;;
	esac
done

# --- 4. with a stub relevo on PATH, each shim execs it argv-and-stdio intact

mkdir -p "$work/bin"
cat > "$work/bin/relevo" <<'STUB'
#!/bin/sh
printf '%s\n' "$*" >> "$RELEVO_STUB_LOG"
cat > "$RELEVO_STUB_STDIN"
exit 0
STUB
chmod +x "$work/bin/relevo"

stub_case() { # want-argv input script [args...]
	want=$1
	input=$2
	script=$3
	shift 3
	rm -f "$work/argv" "$work/stdin" "$work/out" "$work/err"
	: > "$work/argv"
	: > "$work/stdin"
	printf '%s' "$input" > "$work/in"
	code=0
	printf '%s' "$input" |
		env PATH="$work/bin:$PATH" RELEVO_STUB_LOG="$work/argv" RELEVO_STUB_STDIN="$work/stdin" \
			"$script" "$@" > "$work/out" 2> "$work/err" || code=$?
	[ "$code" -eq 0 ] || bad "$script: exit $code with the stub, want 0"
	got=$(cat "$work/argv")
	[ "$got" = "$want" ] || bad "$script: stub argv '$got', want '$want'"
	if ! cmp -s "$work/in" "$work/stdin"; then
		bad "$script: stdin was not passed through byte-for-byte"
	fi
}

stub_case 'mastermind init --hook claude' 'session-start-json' "$plug/scripts/mastermind-init.sh"
stub_case 'mastermind notice --hook claude' 'prompt-json' "$plug/scripts/mastermind-notice.sh"
stub_case 'mcp' 'rpc-json' "$plug/scripts/mcp.sh"
stub_case 'mastermind disable' '' "$plug/scripts/disable.sh"
stub_case 'mastermind disable --repo' '' "$plug/scripts/disable-repo.sh"
stub_case 'mastermind enable' '' "$plug/scripts/enable.sh"
stub_case 'mastermind enable --repo' '' "$plug/scripts/enable-repo.sh"
stub_case 'mastermind reset' '' "$plug/scripts/reset.sh"
stub_case 'show' '' "$plug/scripts/show.sh"
stub_case 'show --round 3 --report' '' "$plug/scripts/show.sh" --round 3 --report
stub_case 'status' '' "$plug/scripts/status.sh"
stub_case 'status --round 3 --report' '' "$plug/scripts/status.sh" --round 3 --report

# --- 5. with no relevo on PATH, the hooks go silent, the rest print the hint

mkdir -p "$work/empty"

nopath() { # script [args...] -- run with an empty PATH, capturing both streams
	script=$1
	shift
	code=0
	env PATH="$work/empty" "$script" "$@" > "$work/out" 2> "$work/err" || code=$?
}

silent_case() { # script -- exit 0, nothing on either stream
	nopath "$1"
	[ "$code" -eq 0 ] || bad "$1: exit $code without PATH, want 0"
	[ ! -s "$work/out" ] || bad "$1: stdout is not empty without PATH"
	[ ! -s "$work/err" ] || bad "$1: stderr is not empty without PATH"
}

hint_case() { # want-exit stream script [args...]
	want=$1
	stream=$2
	script=$3
	shift 3
	nopath "$script" "$@"
	[ "$code" -eq "$want" ] || bad "$script: exit $code without PATH, want $want"
	if [ "$stream" = out ]; then
		other="$work/err"
		target="$work/out"
	else
		other="$work/out"
		target="$work/err"
	fi
	[ ! -s "$other" ] || bad "$script: the other stream is not empty without PATH"
	if ! cmp -s "$work/hintline" "$target"; then
		bad "$script: the printed hint is not byte-identical to the literal"
	fi
}

silent_case "$plug/scripts/mastermind-init.sh"
silent_case "$plug/scripts/mastermind-notice.sh"
hint_case 1 err "$plug/scripts/mcp.sh"
hint_case 0 out "$plug/scripts/disable.sh"
hint_case 0 out "$plug/scripts/disable-repo.sh"
hint_case 0 out "$plug/scripts/enable.sh"
hint_case 0 out "$plug/scripts/enable-repo.sh"
hint_case 0 out "$plug/scripts/reset.sh"
hint_case 0 out "$plug/scripts/show.sh" --round 3 --report
hint_case 0 out "$plug/scripts/status.sh" --round 3 --report

if [ "$fail" -eq 0 ]; then
	echo "plugin-shim: ok"
fi
exit "$fail"
