#!/bin/sh
# scripts/relevo-sandbox.sh -- a no-root dev sandbox: a directory under
# ~/.local/share/relevo-sandboxes whose XDG_*, CLAUDE_CONFIG_DIR and CODEX_HOME
# all point inside it, so a sandboxed session resolves every path under the
# sandbox instead of the developer's own home.
#
# Isolation is not built here; it falls out of the exports. Everything a
# process writes goes through one of those seven variables, so pointing them at
# <root>/<name> is the whole mechanism. This script only wraps it into one
# command and adds the refusals that make it hard to misuse.
#
# What it never does, deliberately: it never touches ~/.local/bin, never runs
# `make install` or `make service`, and never runs `relevo config agents`. The
# binary lands in <root>/<name>/bin, ahead of PATH, so the sandbox's own
# binary is the one a sandboxed shell finds; the install and the harness logins
# stay the developer's own work.
#
# Usage:
#   relevo-sandbox.sh create <name> [--no-build] [--dry-run]
#   relevo-sandbox.sh shell <name>
#   relevo-sandbox.sh destroy <name> [--force] [--dry-run]
#   relevo-sandbox.sh list [--dry-run]
#   any subcommand with --dry-run: print the actions, write nothing
set -eu

# The root holding every sandbox. RELEVO_SANDBOX_ROOT moves it wholesale, which
# is how the test suite points it at a temp dir and never touches the real one.
default_root="${HOME:-}/.local/share/relevo-sandboxes"

dry_run=0
force=0
no_build=0

die() { printf '%s: %s\n' "$prog" "$*" >&2; exit 1; }
note() { printf '%s\n' "$*"; }
plan() {
	if [ "$dry_run" -eq 1 ]; then printf '%s\n' "$*"; else "$@"; fi
}

# root resolves the sandbox root to an absolute path. It has to be absolute
# before any path check, because every safety check below compares a candidate
# path against this one as a prefix: a relative root would make the comparison
# meaningless.
root() {
	if [ -n "${RELEVO_SANDBOX_ROOT:-}" ]; then
		printf '%s' "$RELEVO_SANDBOX_ROOT"
	else
		printf '%s' "$default_root"
	fi
}

# valid_name enforces ^[a-z][a-z0-9-]{0,12}$: a short lowercase token, so the
# directory under the root cannot carry a slash or a dot-dot, and every path
# built from it is one component long.
valid_name() {
	case $1 in
	'' | *[!a-z0-9-]* | [0-9]* | -*) return 1 ;;
	esac
	[ "${#1}" -ge 1 ] && [ "${#1}" -le 13 ] || return 1
	return 0
}

# sandbox_dir resolves <root>/<name> and refuses anything that escapes the root.
# The name rule already excludes a slash and a dot-dot, so this is the second
# lock on the same door: the resolved path must still be under the root, and
# must not be the root itself. Every write in this script goes through here.
sandbox_dir() {
	_name=$1
	valid_name "$_name" ||
		die "invalid sandbox name '$_name': use ^[a-z][a-z0-9-]{0,12}\$ (a short lowercase token, no leading digit or dash)"
	_root=$(root)
	case "$_root" in
	/*) ;;
	*) die "the sandbox root '$_root' is not an absolute path; set RELEVO_SANDBOX_ROOT to an absolute path" ;;
	esac
	_dir="$_root/$_name"
	case "$_dir" in
	"$_root"/*) ;;
	*) die "refusing '$_dir': it does not resolve under the sandbox root '$_root'" ;;
	esac
	printf '%s' "$_dir"
}

# repo_root returns the invoking checkout's root, or dies. create builds the
# binary out of the checkout the caller is standing in, so a build from some
# other tree would silently produce a sandbox that is not this branch.
repo_root() {
	_dir=$(pwd)
	if [ -d "$_dir/.git" ] || [ -f "$_dir/.git" ]; then
		_top=$(git -C "$_dir" rev-parse --show-toplevel 2>/dev/null || printf '%s' "$_dir")
		printf '%s' "$_top"
		return 0
	fi
	die "create builds ./cmd/relevo from the invoking checkout, but this directory is not inside a git checkout; run it from a clone of the relevo repo, or pass --no-build"
}

# daemon_running reports whether a foreground daemon holds the sandbox's state
# lock. The lock is a real flock, so the answer needs flock: a shell cannot read
# it otherwise. Without flock the answer is unknown, and destroy treats unknown
# as "not held" only because --force is still there for the operator who knows.
daemon_running() {
	_lock=$1
	[ -e "$_lock" ] || return 1
	if command -v flock >/dev/null 2>&1; then
		flock -n "$_lock" true 2>/dev/null && return 1
		return 0
	fi
	return 1
}

cmd_create() {
	_name=$1
	shift
	while [ $# -gt 0 ]; do
		case $1 in
		--no-build)
			no_build=1
			shift
			;;
		*)
			die "create: unknown option $1"
			;;
		esac
	done

	_sb=$(sandbox_dir "$_name")
	if [ "$dry_run" -eq 1 ]; then
		plan test -e "$_sb"
	elif [ -e "$_sb" ]; then
		die "$_sb already exists; destroy it first or pick another name"
	fi

	# The checkout is resolved before anything is written, so a create run from
	# the wrong directory fails without leaving a half-made sandbox behind.
	_src=
	if [ "$no_build" -eq 0 ]; then
		_src=$(repo_root)
		[ -d "$_src/cmd/relevo" ] || die "$_src has no cmd/relevo, so it is not a relevo checkout; pass --no-build to skip the build"
	fi

	note "creating sandbox $_name at $_sb"

	for _d in bin state config data cache claude codex; do
		plan mkdir -p "$_sb/$_d"
	done
	# 0700 on every directory: the sandbox holds harness credentials and a
	# database, and nothing under it needs to be readable by another uid.
	if [ "$dry_run" -eq 1 ]; then
		note "chmod 0700 $_sb $_sb/bin $_sb/state $_sb/config $_sb/data $_sb/cache $_sb/claude $_sb/codex"
	else
		for _d in '' bin state config data cache claude codex; do
			chmod 0700 "$_sb/$_d"
		done
	fi

	# The claude credentials are copied when present and warned about when not:
	# the copy is what lets a sandbox reuse your existing login instead of
	# asking for a new one, but it is a copy of a rotating secret, so it can be
	# stale by the time the sandbox runs.
	_creds="$HOME/.claude/.credentials.json"
	if [ -f "$_creds" ]; then
		plan cp "$_creds" "$_sb/claude/.credentials.json"
	else
		note "no $_creds to copy; log the claude harness in inside the sandbox before using it"
	fi

	if [ "$no_build" -eq 1 ]; then
		note "skipping the build (--no-build): $_sb/bin stays empty"
	else
		plan mkdir -p "$_sb/bin"
		note "go build -o $_sb/bin/relevo ./cmd/relevo   (in $_src)"
		if [ "$dry_run" -eq 0 ]; then
			(cd "$_src" && go build -o "$_sb/bin/relevo" ./cmd/relevo) ||
				die "the build failed; nothing is usable in $_sb yet"
			chmod 0700 "$_sb/bin/relevo"
		fi
	fi

	# The marker is what makes this directory a sandbox. destroy refuses any
	# directory without it, so it can never be aimed at an ordinary directory
	# that happens to sit under the root.
	note "printf 'name=$_name\\ncreated=%s\\n' \"\$(date -u +%Y-%m-%dT%H:%M:%SZ)\" > $_sb/sandbox"
	if [ "$dry_run" -eq 0 ]; then
		printf 'name=%s\ncreated=%s\n' "$_name" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$_sb/sandbox"
		chmod 0600 "$_sb/sandbox"
	fi

	write_env_sh "$_sb"

	note ""
	note "sandbox $_name is ready. Enter it with:"
	note "  $prog shell $_name"
	note "and run 'relevo doctor' inside it: a fresh XDG_CONFIG_HOME starts every harness logged out."
}

# write_env_sh writes the export recipe that is the sandbox. Seven exports, all
# under the sandbox root: XDG_STATE_HOME is what store.DefaultRoot reads, the
# other three XDG_ roots follow the same base, CLAUDE_CONFIG_DIR and CODEX_HOME
# are the two per-process homes, and PATH puts the sandbox's own binary first so
# a sandboxed shell cannot reach the developer's ~/.local/bin/relevo by accident.
write_env_sh() {
	_sb=$1
	note "write $_sb/env.sh"
	if [ "$dry_run" -eq 0 ]; then
		{
			printf '# %s/env.sh -- sourced by %s shell. Every path a sandboxed process\n' "$_sb" "$prog"
			printf '# resolves lands under the sandbox root, never the developer home.\n'
			printf 'export XDG_STATE_HOME=%s/state\n' "$_sb"
			printf 'export XDG_CONFIG_HOME=%s/config\n' "$_sb"
			printf 'export XDG_DATA_HOME=%s/data\n' "$_sb"
			printf 'export XDG_CACHE_HOME=%s/cache\n' "$_sb"
			printf 'export CLAUDE_CONFIG_DIR=%s/claude\n' "$_sb"
			printf 'export CODEX_HOME=%s/codex\n' "$_sb"
			# The literal $PATH is the point: the sandbox prepends its own bin to
			# the caller's PATH rather than replacing it, so go, git and the
			# harnesses still resolve. It must reach env.sh unexpanded.
			# shellcheck disable=SC2016
			printf 'export PATH=%s/bin:$PATH\n' "$_sb"
		} > "$_sb/env.sh"
		chmod 0600 "$_sb/env.sh"
	fi
}

cmd_shell() {
	_name=$1
	shift
	if [ $# -gt 0 ]; then
		die "shell: unknown option $1"
	fi

	_sb=$(sandbox_dir "$_name")
	[ -f "$_sb/sandbox" ] || die "$_sb has no sandbox marker, so it is not a sandbox; create it first"
	[ -f "$_sb/env.sh" ] || die "$_sb has no env.sh, so there is no environment to enter; create it again"

	_shell=${SHELL:-/bin/sh}
	# The whole session inherits the sandbox, not just this command: sourcing
	# here and exec'ing means the login shell, the daemon it starts and every
	# harness child it spawns all resolve under the same root.
	note "entering sandbox $_name in a $_shell"
	set -a
	# shellcheck disable=SC1090,SC1091 # the path is built at run time and written by
	# create, so shellcheck cannot read it
	. "$_sb/env.sh"
	set +a
	exec "$_shell"
}

cmd_destroy() {
	_name=$1
	shift
	while [ $# -gt 0 ]; do
		case $1 in
		--force)
			force=1
			shift
			;;
		*)
			die "destroy: unknown option $1"
			;;
		esac
	done

	_sb=$(sandbox_dir "$_name")

	# The marker is the whole refusal: no marker means this directory was not
	# made by create, so destroying it is destroying something the operator did
	# not ask this script to touch.
	if [ "$dry_run" -eq 1 ]; then
		plan test -f "$_sb/sandbox"
	else
		[ -f "$_sb/sandbox" ] ||
			die "refusing to remove $_sb: it carries no sandbox marker, so this script did not create it"
	fi

	# A held daemon lock means a foreground daemon is still writing into the
	# state root about to be removed. --force is the operator saying they know.
	if daemon_running "$_sb/state/.daemon.lock"; then
		if [ "$force" -eq 0 ]; then
			die "$_sb/state/.daemon.lock is held, so a daemon is still running in this sandbox; stop it, or pass --force"
		fi
		note "warning: a daemon still holds $_sb/state/.daemon.lock (--force)"
	fi

	note "destroying sandbox $_name at $_sb"
	# rm -rf on the sandbox directory is the entire rollback: its state, config,
	# data, cache and harness homes all live under it.
	plan rm -rf "$_sb"
	if [ "$dry_run" -eq 1 ]; then
		plan test ! -e "$_sb"
	else
		[ -e "$_sb" ] && die "rm -rf left $_sb behind"
		note "sandbox $_name is gone"
		return 0
	fi
}

cmd_list() {
	_root=$(root)
	note "SANDBOX  CREATED  PATH"
	_found=0
	for _d in "$_root"/*; do
		[ -d "$_d" ] || continue
		[ -f "$_d/sandbox" ] || continue
		_found=1
		_created=$(sed -n 's/^created=//p' "$_d/sandbox" 2>/dev/null | head -1)
		[ -n "$_created" ] || _created='-'
		printf '%-7s %-9s %s\n' "$(basename "$_d")" "$_created" "$_d"
	done
	if [ "$_found" -eq 0 ]; then
		note "(no sandboxes under $_root)"
	fi
}

usage() {
	cat <<EOF
usage: $prog {create <name> [--no-build] | shell <name> | destroy <name> [--force] | list} [--dry-run]

A no-root dev sandbox: a directory under ${RELEVO_SANDBOX_ROOT:-$HOME/.local/share/relevo-sandboxes}
whose XDG_STATE_HOME, XDG_CONFIG_HOME, XDG_DATA_HOME, XDG_CACHE_HOME,
CLAUDE_CONFIG_DIR and CODEX_HOME all point inside it. The isolation is the
exports; this script wraps them into one command.

  create <name>   make <root>/<name> with bin/ state/ config/ data/ cache/
                  claude/ codex/ env.sh, a sandbox marker, and a build of
                  ./cmd/relevo from the invoking checkout. Copies
                  ~/.claude/.credentials.json when present.
                  --no-build skips the build (no toolchain needed).
  shell <name>    exec \$SHELL with env.sh sourced, so the session, its daemon
                  and its harness children all resolve under the sandbox.
  destroy <name>  rm -rf the sandbox directory. Refuses a directory with no
                  marker, and refuses a held daemon lock unless --force.
  list            the sandboxes under the root.

What this script never does: it never touches ~/.local/bin, never runs
'make install' or 'make service', and never runs 'relevo config agents'. The
binary lands in <root>/<name>/bin ahead of PATH, so the sandbox's own binary is
the one a sandboxed shell finds. Installing, and logging the harnesses in
('relevo config agents', then 'relevo doctor' inside the sandbox), are your own
work -- agy has no per-process home selector, so ~/.gemini stays shared.

--dry-run prints the actions and writes nothing; it may appear anywhere.
Set RELEVO_SANDBOX_ROOT to move the root.
EOF
}

prog=relevo-sandbox.sh
[ "${0##*/}" = "$prog" ] || prog=${0##*/}

# Pull --dry-run out of the argument list before the subcommand reads it, so it
# can appear before or after the subcommand and after its arguments.
_dry=0
_args=""
# A hand-rolled pass keeps the remaining arguments in order without depending
# on "$@" word splitting rules that differ between shells.
for _a in "$@"; do
	case $_a in
	--dry-run) _dry=1 ;;
	*) _args="$_args $_a" ;;
	esac
done
dry_run=$_dry
# shellcheck disable=SC2086 # the split is the point: _args is a list to become
# the script's positional parameters again, so the subcommand reads them with
# shift. A quoted "set -- $_args" would make the whole list one argument.
# shellcheck disable=SC2086
set -- $_args

if [ $# -eq 0 ]; then
	usage >&2
	exit 2
fi

_sub=$1
shift
case $_sub in
create)
	[ $# -ge 1 ] || die "create needs a name"
	cmd_create "$@"
	;;
shell)
	[ $# -ge 1 ] || die "shell needs a name"
	cmd_shell "$@"
	;;
destroy)
	[ $# -ge 1 ] || die "destroy needs a name"
	cmd_destroy "$@"
	;;
list)
	cmd_list
	;;
-h | --help | help)
	usage
	;;
*)
	die "unknown subcommand $_sub"
	;;
esac