#!/bin/sh
# scripts/relevo-sandbox_test.sh -- tests for the no-root env sandbox script.
#
# Every create runs with RELEVO_SANDBOX_ROOT pointed at a temp dir and
# --no-build, so the suite needs no toolchain, no network and never touches the
# developer's real ~/.local/share/relevo-sandboxes. What it pins is the safety
# that matters: the refusals, the 0700 layout, the marker, and the eight exports
# in env.sh -- specifically that every one of them resolves under the sandbox
# root and never under the caller's home.
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
script="$here/relevo-sandbox.sh"

root=$(mktemp -d)
home=$(mktemp -d)
trap 'rm -rf "$root" "$home" 2>/dev/null || :' EXIT

fail=0

# sb runs the script with a temp root and temp XDG_* and HOME, so every read
# lands in a temp dir: the developer's real ~/.claude is never copied, the
# real machine database is never read, and no assertion depends on the machine
# running the suite. It captures output and status.
sb() {
	out=$(RELEVO_SANDBOX_ROOT="$root" HOME="$home" XDG_STATE_HOME="$root/xdg-state" XDG_CONFIG_HOME="$root/xdg-config" XDG_DATA_HOME="$root/xdg-data" "$@" 2>&1) && got=0 || got=$?
}



# refuses asserts the invocation fails, with the description in its output.
refuses() {
	desc=$1
	want=$2
	shift 2
	sb "$@"
	if [ "$got" -eq 0 ]; then
		echo "FAIL: $desc (exit 0, want a refusal)"
		fail=1
		return
	fi
	if ! printf '%s' "$out" | grep -qF "$want"; then
		echo "FAIL: $desc (no \"$want\" in: $out)"
		fail=1
	fi
}

# allows asserts the invocation succeeds and its output carries a string.
allows() {
	desc=$1
	want=$2
	shift 2
	sb "$@"
	if [ "$got" -ne 0 ]; then
		echo "FAIL: $desc (exit $got, want 0): $out"
		fail=1
		return
	fi
	if [ -n "$want" ] && ! printf '%s' "$out" | grep -qF -- "$want"; then
		echo "FAIL: $desc (no \"$want\" in: $out)"
		fail=1
	fi
}

# carries asserts the output already in $out names a string.
carries() {
	desc=$1
	want=$2
	if ! printf '%s' "$out" | grep -qF -- "$want"; then
		echo "FAIL: $desc (no \"$want\" in: $out)"
		fail=1
	fi
}

# absent asserts the output does not name a string. It pins the three things the
# script must never do, read off a real create's output rather than the help.
absent() {
	desc=$1
	want=$2
	if printf '%s' "$out" | grep -qF -- "$want"; then
		echo "FAIL: $desc (\"$want\" present in: $out)"
		fail=1
	fi
}

nb='--no-build'

# A name outside ^[a-z][a-z0-9-]{0,12}$ is refused by name, not by a later
# filesystem error: the name is the only input that reaches the path arithmetic.
refuses 'an uppercase name is refused' 'invalid sandbox name' \
	sh "$script" create Demo "$nb"
refuses 'a leading digit is refused' 'invalid sandbox name' \
	sh "$script" create 9demo "$nb"
refuses 'a name with a slash is refused' 'invalid sandbox name' \
	sh "$script" create de/mo "$nb"
refuses 'a dot-dot name is refused' 'invalid sandbox name' \
	sh "$script" create .. "$nb"
refuses 'an over-long name is refused' 'invalid sandbox name' \
	sh "$script" create abcdefghijklmn "$nb"
refuses 'an empty name is refused' 'create needs a name' \
	sh "$script" create
# Ranges in a case pattern follow the locale's collating order, where a-z can
# also match uppercase: the refusal must hold under an explicit UTF-8 locale,
# which is the macOS-runner shape that once let Demo through.
for _loc in C C.utf8 en_US.utf8; do
	if LC_ALL=$_loc sh "$script" create Demo "$nb" >/dev/null 2>&1; then
		echo "FAIL: an uppercase name is accepted under LC_ALL=$_loc"
		fail=1
	fi
done
refuses 'an unknown subcommand is refused' 'unknown subcommand' \
	sh "$script" frobnicate

# A create whose name is already taken is refused: reusing a live sandbox's name
# would write into a directory someone is already running a daemon in.
allows 'a first create succeeds' 'is ready' \
	sh "$script" create demo "$nb"
refuses 'creating the same name twice is refused' 'already exists' \
	sh "$script" create demo "$nb"

# The three things the script never does, read off that create's own output.
# Each is a separate hazard: writing ~/.local/bin would replace the developer's
# binary with a sandbox build, `make service` would install a systemd unit
# outside the sandbox, and `relevo config agents` would write harness
# definitions into the caller's own config root.
absent 'create does not write to ~/.local/bin' '/.local/bin'
absent 'create does not run make install' 'make install'
absent 'create does not run make service' 'make service'
absent 'create does not run config agents' 'config agents'

sbx="$root/demo"
for d in bin state config data cache claude codex; do
	if [ ! -d "$sbx/$d" ]; then
		echo "FAIL: create left no $sbx/$d"
		fail=1
		continue
	fi
	# 0700 on every directory: the sandbox holds a database and harness
	# credentials, and nothing under it needs another uid's read.
	mode=$(stat -c '%a' "$sbx/$d" 2>/dev/null || stat -f '%Lp' "$sbx/$d" 2>/dev/null || printf '')
	if [ "$mode" != "700" ]; then
		echo "FAIL: $sbx/$d is mode $mode, want 700"
		fail=1
	fi
done

# The marker is what makes this a sandbox and what destroy later requires.
# The sandbox directory itself is 0700 too: it is the parent of the
# credentials, so a mode that lets another uid list it leaks what is inside.
mode=$(stat -c '%a' "$sbx" 2>/dev/null || stat -f '%Lp' "$sbx" 2>/dev/null || printf '')
if [ "$mode" != "700" ]; then
	echo "FAIL: $sbx is mode $mode, want 700"
	fail=1
fi

if [ ! -f "$sbx/sandbox" ]; then
	echo "FAIL: create wrote no sandbox marker at $sbx/sandbox"
	fail=1
else
	if ! grep -q '^name=demo$' "$sbx/sandbox"; then
		echo "FAIL: the marker records no name=demo: $(cat "$sbx/sandbox")"
		fail=1
	fi
	if ! grep -q '^created=' "$sbx/sandbox"; then
		echo "FAIL: the marker records no created= line: $(cat "$sbx/sandbox")"
		fail=1
	fi
fi

# env.sh is the sandbox. All eight exports must name a path under the sandbox
# root: this is what makes store.DefaultRoot resolve into the sandbox rather than
# the developer's ~/.local/state.
if [ ! -f "$sbx/env.sh" ]; then
	echo "FAIL: create wrote no env.sh at $sbx/env.sh"
	fail=1
else
	for pair in \
		"SB=$sbx" \
		"XDG_STATE_HOME=$sbx/state" \
		"XDG_CONFIG_HOME=$sbx/config" \
		"XDG_DATA_HOME=$sbx/data" \
		"XDG_CACHE_HOME=$sbx/cache" \
		"CLAUDE_CONFIG_DIR=$sbx/claude" \
		"CODEX_HOME=$sbx/codex" \
		"PATH=$sbx/bin:\$PATH"; do
		want="export $pair"
		if ! grep -qF -- "$want" "$sbx/env.sh"; then
			echo "FAIL: env.sh lacks \"$want\""
			fail=1
		fi
	done
	# The one failure mode that matters: an export that leaks the caller's home
	# is a sandbox that writes to the developer's real state.
	if grep -qE '^(export )?XDG_STATE_HOME="?\$?HOME' "$sbx/env.sh"; then
		echo "FAIL: env.sh resolves XDG_STATE_HOME from HOME, so the sandbox would write to the caller state"
		fail=1
	fi
	if grep -qF -- "$home/" "$sbx/env.sh"; then
		echo "FAIL: env.sh names the caller home $home, so the sandbox is not isolated"
		fail=1
	fi

	# Sourcing env.sh and asking where the state root resolves must land inside
	# the sandbox: store.DefaultRoot returns $XDG_STATE_HOME/relevo, so the
	# export alone decides it. This is the resolution the script exists for.
	resolved=$(RELEVO_SANDBOX_ROOT="$root" HOME="$home" sh -c '. "$1/env.sh"; printf "%s" "$XDG_STATE_HOME/relevo"' sh "$sbx")
	if [ "$resolved" != "$sbx/state/relevo" ]; then
		echo "FAIL: sourcing env.sh resolves the state root to $resolved, want $sbx/state/relevo"
		fail=1
	fi
fi

# shell refuses a directory that is not a sandbox: a marker is required, so the
# subcommand cannot be aimed at a bare directory under the root.
mkdir -p "$root/plain"
refuses 'shell refuses a directory with no marker' 'not a sandbox' \
	sh "$script" shell plain
refuses 'shell refuses an invalid name' 'invalid sandbox name' \
	sh "$script" shell ../root
refuses 'shell refuses an unknown sandbox' 'not a sandbox' \
	sh "$script" shell absent

# destroy refuses a directory with no marker. This is the refusal that keeps
# the rm -rf away from anything create did not make.
refuses 'destroy refuses a directory with no marker' 'sandbox marker' \
	sh "$script" destroy plain
if [ ! -d "$root/plain" ]; then
	echo "FAIL: destroy removed $root/plain, so the marker refusal did not hold"
	fail=1
fi
refuses 'destroy refuses an invalid name' 'invalid sandbox name' \
	sh "$script" destroy ../root
refuses 'destroy refuses a name that does not exist' 'sandbox marker' \
	sh "$script" destroy absent

# A held state lock means a foreground daemon is still writing into the directory
# about to be removed. flock -n fails on a held lock, which is how the check is
# driven without a daemon: a second lock holder stands in for one.
if command -v flock >/dev/null 2>&1; then
	exec 9>"$sbx/state/.daemon.lock"
	flock -x 9
	refuses 'destroy refuses a held daemon lock' 'is held' \
		sh "$script" destroy demo
	refuses 'destroy refuses a held daemon lock without --force' 'pass --force' \
		sh "$script" destroy demo
	if [ ! -d "$sbx" ]; then
		echo "FAIL: destroy removed a sandbox with a held lock"
		fail=1
	fi
	# --force is the operator saying they know, and it proceeds.
	allows 'destroy --force proceeds past a held lock' 'is gone' \
		sh "$script" destroy demo --force
	exec 9>&-
	if [ -d "$sbx" ]; then
		echo "FAIL: destroy --force left $sbx behind"
		fail=1
	fi
else
	echo "skip: the held-lock refusal (no flock on this machine)"
fi

# list names the sandboxes present under the root, and reports the root when
# there are none.
allows 'list on an empty root succeeds' 'no sandboxes' \
	sh "$script" list
allows 'create a second sandbox' 'is ready' \
	sh "$script" create other "$nb"
allows 'create a third sandbox' 'is ready' \
	sh "$script" create extra "$nb"
sb "$script" list
if [ "$got" -ne 0 ]; then
	echo "FAIL: list exits $got: $out"
	fail=1
fi
carries 'list names one sandbox' 'other'
carries 'list names the other sandbox' 'extra'
# list reports what the marker records, so a sandbox is listed by name and not
# by the directory it happens to sit in.
created=$(sed -n 's/^created=//p' "$root/other/sandbox" | head -1)
carries 'list reports the creation time the marker records' "$created"
# A bare directory under the root is not a sandbox, so it is not listed.
absent 'list does not name the markerless directory' 'plain'

# --dry-run writes nothing: a plan that created the sandbox would make the
# refusals above pass for the wrong reason. The XDG_* overrides keep the
# host-side reads hermetic: without them the seed would read the machine
# running the suite instead of nothing.
rm -rf "$root/dry"
before=$(find "$root" -mindepth 1 -maxdepth 1 | sort | tr '\n' ' ')
RELEVO_SANDBOX_ROOT="$root" HOME="$home" XDG_STATE_HOME="$root/xdg-state" XDG_CONFIG_HOME="$root/xdg-config" XDG_DATA_HOME="$root/xdg-data" sh "$script" create dry --dry-run >/dev/null
after=$(find "$root" -mindepth 1 -maxdepth 1 | sort | tr '\n' ' ')
if [ "$before" != "$after" ]; then
	echo "FAIL: create --dry-run changed the root from [$before] to [$after]"
	fail=1
fi
if [ -d "$root/dry" ]; then
	echo "FAIL: create --dry-run made $root/dry"
	fail=1
fi
# The plan still names every action a real create performs. With no host
# database there is nothing to copy, so the plan says so and the seed steps
# stay out of it.
plan_out=$(RELEVO_SANDBOX_ROOT="$root" HOME="$home" XDG_STATE_HOME="$root/xdg-state" XDG_CONFIG_HOME="$root/xdg-config" XDG_DATA_HOME="$root/xdg-data" sh "$script" create dry --dry-run 2>&1)
for want in "$root/dry/state" 'chmod 0700' "$root/dry/env.sh" "$root/dry/sandbox" 'no host database' 'bin/relevo doctor'; do
	if ! printf '%s' "$plan_out" | grep -qF -- "$want"; then
		echo "FAIL: the create plan names no \"$want\": $plan_out"
		fail=1
	fi
done
# --dry-run prints the plan and writes nothing on destroy either.
dry_out=$(RELEVO_SANDBOX_ROOT="$root" HOME="$home" sh "$script" destroy other --dry-run 2>&1)
if ! printf '%s' "$dry_out" | grep -qF -- "rm -rf $root/other"; then
	echo "FAIL: the destroy plan names no rm -rf of the sandbox: $dry_out"
	fail=1
fi
if [ ! -d "$root/other" ]; then
	echo "FAIL: destroy --dry-run removed $root/other"
	fail=1
fi

# Without a build there is no binary to seed or check with, so --no-build
# skips both and says so.
sb "$script" create skipsb "$nb"
carries 'a --no-build create skips the seed and the doctor' 'no config seed and no doctor run'

# A create from inside a sandbox shell is refused: the host-side reads would
# resolve into that sandbox, so the seed would copy the sandbox into itself.
nest_out=$(RELEVO_SANDBOX_ROOT="$root" HOME="$home" XDG_STATE_HOME="$root/relevo-sandboxes/nested/state" sh "$script" create nested --dry-run 2>&1) && nest_got=0 || nest_got=$?
if [ "$nest_got" -eq 0 ]; then
	echo "FAIL: create from a sandbox shell succeeds (exit 0): $nest_out"
	fail=1
fi
if ! printf '%s' "$nest_out" | grep -qF 'not a sandbox shell'; then
	echo "FAIL: the nested-shell refusal names nothing useful: $nest_out"
	fail=1
fi

# A stub host relevo answering one section proves the copy path: the value it
# returns must reach the sandbox-side set verbatim, the other sections print
# skip notes, and a seeded sandbox skips the init fallback. The empty database
# file stands in for a host database whose sections are all unset: reads fail
# against it, so the loop is exercised without a real machine database.
mkdir -p "$root/stubbin" "$root/xdg-state/relevo"
cat > "$root/stubbin/relevo" <<'EOF'
#!/bin/sh
# stub host relevo for the seed loop: answers one section, accepts every set
# (the sandbox-side binary is exercised by the dry-run plan, not executed).
if [ "$1 $2" = "config get" ]; then
	case $3 in
	candidates) printf '[{"stub":1}]' ;;
	servers) printf '{"s":2}' ;;
	*) exit 1 ;;
	esac
elif [ "$1 $2" = "config set" ]; then
	exit 0
else
	exit 1
fi
EOF
chmod +x "$root/stubbin/relevo"
: > "$root/xdg-state/relevo/relevo.db"
stub_out=$(RELEVO_SANDBOX_ROOT="$root" HOME="$home" XDG_STATE_HOME="$root/xdg-state" XDG_CONFIG_HOME="$root/xdg-config" XDG_DATA_HOME="$root/xdg-data" PATH="$root/stubbin:$PATH" sh "$script" create stubbed --dry-run 2>&1)
if ! printf '%s' "$stub_out" | grep -qF 'config set candidates [{"stub":1}]'; then
	echo "FAIL: the seed does not carry the host value verbatim: $stub_out"
	fail=1
fi
if ! printf '%s' "$stub_out" | grep -qF 'config set servers {"s":2}'; then
	echo "FAIL: the seed drops the servers section: $stub_out"
	fail=1
fi
if ! printf '%s' "$stub_out" | grep -qF 'no host policy to copy; skipping'; then
	echo "FAIL: unset sections print no skip note: $stub_out"
	fail=1
fi
if printf '%s' "$stub_out" | grep -qF 'config init --no-agents'; then
	echo "FAIL: a seeded sandbox still plans the init fallback: $stub_out"
	fail=1
fi

# A host database with no set sections falls back to the starter: the empty
# file reads as unset for every section, so the init line returns.
sb "$script" create emptydb --dry-run
carries 'an empty host database plans the init fallback' 'config init --no-agents'

# A non-absolute RELEVO_SANDBOX_ROOT is refused: every safety check below
# compares a path against the root as a prefix, and a relative root makes that
# comparison meaningless.
rel_out=$(RELEVO_SANDBOX_ROOT=relative/root HOME="$home" sh "$script" create demo2 "$nb" 2>&1) && rel_got=0 || rel_got=$?
if [ "$rel_got" -eq 0 ]; then
	echo "FAIL: a relative sandbox root is accepted (exit 0): $rel_out"
	fail=1
fi
if ! printf '%s' "$rel_out" | grep -qF 'not an absolute path'; then
	echo "FAIL: the relative-root refusal names nothing useful: $rel_out"
	fail=1
fi

# Without --no-build, create builds out of the invoking checkout, so running it
# from a directory that is not one is refused rather than silently building
# something else. The refusal happens before any write, so the sandbox is not
# left half-made.
outside=$(mktemp -d)
out_out=$(cd "$outside" && RELEVO_SANDBOX_ROOT="$root" HOME="$home" sh "$script" create norepo 2>&1) && out_got=0 || out_got=$?
if [ "$out_got" -eq 0 ]; then
	echo "FAIL: create outside a checkout succeeds (exit 0): $out_out"
	fail=1
fi
if ! printf '%s' "$out_out" | grep -qF 'not inside a git checkout'; then
	echo "FAIL: the out-of-checkout refusal names no reason: $out_out"
	fail=1
fi
if [ -e "$root/norepo" ]; then
	echo "FAIL: the out-of-checkout refusal left $root/norepo behind"
	fail=1
fi
rm -rf "$outside"

# A create with no credentials to copy warns instead of failing: the copy is a
# convenience, and a sandbox without it is still a working sandbox.
sb "$script" create nologin "$nb"
carries 'a create with no credentials to copy warns' 'log the claude harness in'
if [ -f "$root/nologin/claude/.credentials.json" ]; then
	echo "FAIL: create copied credentials the temp HOME does not have"
	fail=1
fi

# With logins present, create copies the login and config files plus the agent
# definitions, and leaves every data directory behind: transcripts must land
# in the sandbox, never leak out of it through a copied directory.
mkdir -p "$home/.claude/agents" "$home/.claude/projects/sess1" "$home/.codex"
printf '{"token":"x"}' > "$home/.claude/.credentials.json"
printf '{}' > "$home/.claude/settings.json"
printf '{}' > "$home/.claude/settings.shared.json"
printf 'hook' > "$home/.claude/agents/executor.md"
printf 'transcript' > "$home/.claude/projects/sess1/chat.jsonl"
printf 'history' > "$home/.claude/history.jsonl"
printf '{"t":"y"}' > "$home/.codex/auth.json"
printf 'cfg' > "$home/.codex/config.toml"
printf 'data' > "$home/.codex/goals_1.sqlite"
sb "$script" create logins "$nb"
for want in \
	"$root/logins/claude/.credentials.json" \
	"$root/logins/claude/settings.json" \
	"$root/logins/claude/settings.shared.json" \
	"$root/logins/claude/agents/executor.md" \
	"$root/logins/codex/auth.json" \
	"$root/logins/codex/config.toml"; do
	if [ ! -f "$want" ]; then
		echo "FAIL: create copied no $want"
		fail=1
	fi
done
for want in \
	"$root/logins/claude/projects" \
	"$root/logins/claude/history.jsonl" \
	"$root/logins/codex/goals_1.sqlite"; do
	if [ -e "$want" ]; then
		echo "FAIL: create copied session data $want"
		fail=1
	fi
done

# The help text states the three things the script never does, so the guarantee
# is readable without reading the code.
sb "$script" --help
carries 'help names the install refusal' 'never touches ~/.local/bin'
carries 'help names the make install refusal' 'make install'
carries 'help names the config agents refusal' 'config agents'

[ "$fail" -eq 0 ] && echo "relevo-sandbox: ok"
exit "$fail"