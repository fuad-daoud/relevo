#!/bin/sh
# scripts/relevo-dev-user.sh -- create, destroy and list the Unix accounts that
# serve as isolated relevo development sandboxes.
#
# A sandbox is a real Unix user, not a directory under the developer's own
# home. That is what makes it isolated: the clone, the state root, the config,
# the systemd user unit and every credential belong to that account, so two
# sandboxes cannot collide on a branch, a socket, a port or a login.
#
# The script never touches the invoking user's state and never touches a user
# it did not create: destroy refuses anything but an rv- account, and it stops
# that account's units before deleting it.
#
# Usage:
#   relevo-dev-user.sh create <name> [--port N] [--repo URL] [--ref REF]
#   relevo-dev-user.sh destroy <name>
#   relevo-dev-user.sh list
#   any subcommand with --dry-run: print the commands, run nothing, need no root
set -eu

# The sun_path limit is what caps the sandbox name's length: the socket path is
# <home>/.local/state/relevo/relevo.sock, and a path over the limit can never
# bind, so a name whose path would be too long is refused at create rather than
# after the account exists.
sun_path_limit=104

# The default `relevo serve` port. A sandbox may never take it: the production
# serve on this host already owns it, and the doctor port row warns when a
# sandbox sits there.
default_serve_port=7777

# The first port handed out when no --port is given. Each sandbox gets its own
# number, so 7801 and up.
first_serve_port=7801

dry_run=0
port=
repo=
ref=

die() { printf '%s: %s\n' "$prog" "$*" >&2; exit 1; }
note() { printf '%s\n' "$*"; }
plan() {
	if [ "$dry_run" -eq 1 ]; then printf '%s\n' "$*"; else "$@"; fi
}

# run_as_user runs a command as the sandbox account under an environment built
# from scratch. An inherited environment would hand the child the parent's
# XDG_*, RELEVO_* and CLAUDE* values, which is exactly the inheritance the
# sandbox exists to avoid: the child would resolve its state root under the
# parent's home and write there.
run_as_user() {
	_user=$1
	_uid=$2
	shift 2
	plan env -i \
		HOME="/home/$_user" \
		USER="$_user" \
		LOGNAME="$_user" \
		PATH=/usr/local/bin:/usr/bin:/bin \
		XDG_RUNTIME_DIR="/run/user/$_uid" \
		su -s /bin/sh -c "$*" "$_user"
}

# stop_user_unit stops one user unit as the sandbox account on its own bus.
# Root has no user manager, so a bare `systemctl --user` run as root cannot
# connect; the runtime dir addresses the account's bus instead. A missing unit
# is not an error: terminate-user ends anything left, and destroy's contract
# is that the account goes away.
stop_user_unit() {
	_user=$1
	_uid=$2
	_unit=$3
	plan env XDG_RUNTIME_DIR="/run/user/$_uid" su -s /bin/sh -c "systemctl --user disable --now $_unit || true" "$_user"
}

# valid_name enforces the name rule: a short lowercase token, so the user is
# rv-<name>, the home is /home/rv-<name>, and the socket path stays inside the
# sun_path limit.
valid_name() {
	case $1 in
	'' | *[!a-z0-9-]* | [0-9]* | -*) return 1 ;;
	esac
	[ "${#1}" -ge 1 ] && [ "${#1}" -le 12 ] || return 1
	return 0
}

# socket_path is the owner socket the sandbox's daemon would bind. create
# refuses a name whose path does not fit, naming the path and the limit, for
# the same reason owner.SocketPath refuses it: the daemon could never start.
socket_path() {
	printf '%s' "/home/rv-$1/.local/state/relevo/relevo.sock"
}

# check_socket_length refuses a name whose socket path would not fit sun_path.
check_socket_length() {
	_path=$(socket_path "$1")
	if [ "${#_path}" -ge "$sun_path_limit" ]; then
		die "the socket path for $1 would be ${#_path} bytes, over the $sun_path_limit-byte sun_path limit; pick a shorter name"
	fi
}

# check_port_free refuses the default port and any port another sandbox's marker
# already records, so two sandboxes cannot be created onto one port.
check_port_free() {
	_want=$1
	if [ "$_want" -eq "$default_serve_port" ]; then
		die "port $_want is the default $default_serve_port, which the production serve on this host owns; pass --port with another number"
	fi
	for _marker in /home/rv-*/.local/state/relevo/sandbox; do
		[ -f "$_marker" ] || continue
		_have=$(sed -n 's/^port=//p' "$_marker" 2>/dev/null | head -1)
		[ -n "$_have" ] || continue
		if [ "$_have" -eq "$_want" ]; then
			die "port $_want is already recorded by the sandbox holding $_marker; pass --port with another number"
		fi
	done
}

# require_root refuses to run the real thing without root: every step below
# creates an account, moves a login session or removes one. --dry-run needs no
# root because it runs nothing.
require_root() {
	[ "$dry_run" -eq 1 ] && return 0
	if [ "$(id -u)" -ne 0 ]; then
		die "must run as root (try again with sudo, or pass --dry-run to print the commands)"
	fi
}

cmd_create() {
	_name=$1
	shift
	while [ $# -gt 0 ]; do
		case $1 in
		--port)
			port=$2
			shift 2
			;;
		--repo)
			repo=$2
			shift 2
			;;
		--ref)
			ref=$2
			shift 2
			;;
		*)
			die "create: unknown option $1"
			;;
		esac
	done

	# The socket length is checked first: it is the more specific refusal, and a
	# name too long for it never reaches the pattern below.
	check_socket_length "$_name"
	valid_name "$_name" || die "invalid sandbox name '$_name': use ^[a-z][a-z0-9-]{0,11}\$ (a short lowercase token, no leading digit or dash)"

	# The account-exists check reads the live account database, which --dry-run
	# must not depend on: it prints the command a real run would consult.
	_user="rv-$_name"
	if [ "$dry_run" -eq 1 ]; then
		plan id "$_user"
	elif id "$_user" >/dev/null 2>&1; then
		die "the user $_user already exists; destroy it first or pick another name"
	fi

	# The default port is the one just above the first port handed out, so an
	# unflagged create still lands somewhere unique. A --port is checked
	# against the defaults and every recorded marker.
	if [ -z "$port" ]; then
		port=$first_serve_port
		while grep -qs "^port=$port\$" /home/rv-*/.local/state/relevo/sandbox; do
			port=$((port + 1))
		done
	fi
	case $port in
	'' | *[!0-9]*) die "--port must be a number, got '$port'" ;;
	esac
	check_port_free "$port"

	# The checkout to clone from defaults to the invoking checkout's origin,
	# and the branch to origin/main: a sandbox starts from what main holds, not
	# from whatever the caller has checked out.
	if [ -z "$repo" ]; then
		repo=$(git -C "$(pwd)" remote get-url origin 2>/dev/null || true)
		[ -n "$repo" ] || die "no --repo given and this directory has no origin remote; pass --repo URL"
	fi
	[ -n "$ref" ] || ref=origin/main

	require_root

	note "creating sandbox $_name: user $_user, home /home/$_user, serve port $port"

	# Step 1: the account and its home.
	plan useradd --create-home --shell /bin/bash "$_user"

	# Step 2: lingering, so the user manager survives logout. This must come
	# before the service is installed, because without it the manager that
	# would run the unit does not outlive the session that installs it.
	plan loginctl enable-linger "$_user"

	# Step 3: wait for the runtime directory the user's systemd manager needs
	# and its units live under. The uid is only known once useradd has run, so
	# under --dry-run it stands in as the literal <uid> the printed command
	# carries.
	if [ "$dry_run" -eq 1 ]; then
		uid='<uid>'
		note "id -u $_user"
		note "wait for /run/user/<uid> to appear"
	else
		uid=$(id -u "$_user")
		wait_runtime_dir "$uid"
	fi

	# Step 4: the clone, the build and the service, all as the user and under a
	# clean environment. The checkout is never shared with the caller: a shared
	# work tree means a shared index, so a sandbox's checkout would fight the
	# developer's over branches, fetch refusals and hook execution.
	run_as_user "$_user" "$uid" "git clone '$repo' /home/$_user/src/relevo"
	run_as_user "$_user" "$uid" "cd /home/$_user/src/relevo && git checkout '$ref' && make service"

	# Step 5: the marker, so doctor can tell this account is a sandbox. It is
	# written by root and handed to the user, so the sandbox's own doctor reads
	# a file it owns.
	marker="/home/$_user/.local/state/relevo/sandbox"
	plan mkdir -p "/home/$_user/.local/state/relevo"
	plan write_marker "$marker" "$_name" "$port" "$_user"

	# Step 6: with --port, the serve unit gets that port as a drop-in rather
	# than an edit to the shipped template, so a later `make service` cannot
	# silently put 7777 back.
	if [ -n "$port" ]; then
		plan install_serve_dropin "$_user" "$port"
	fi

	note ""
	note "sandbox $_name is ready. These steps need your own logins and cannot be done for you:"
	note "  sudo -iu $_user"
	note "  # then, as $_user, log the harnesses in and point opencode at this state root:"
	note "  #   opencode's permission.external_directory allowlist must include:"
	note "  #     /home/$_user/.local/state/relevo"
	note "  # and configure the harnesses:"
	note "  #   relevo config agents"
	note "  # then check the install:"
	note "  #   relevo doctor"
	note ""
	if [ -n "$port" ]; then
		note "start the sandbox's serve with:"
		note "  sudo -iu $_user 'relevo serve --listen :$port'"
	fi
}

# wait_runtime_dir blocks until systemd has created the user's runtime
# directory. Without it the user manager has nowhere to put its units, and
# `systemctl --user` fails with a bare "Failed to connect to bus".
wait_runtime_dir() {
	_uid=$1
	_i=0
	while [ ! -d "/run/user/$_uid" ]; do
		[ "$_i" -ge 30 ] && die "/run/user/$_uid never appeared; the user manager did not start"
		sleep 1
		_i=$((_i + 1))
	done
	note "/run/user/$_uid is present"
}

# write_marker writes the sandbox marker and hands it to the user, so the
# sandbox's own doctor reads a file it owns.
write_marker() {
	_path=$1
	_name=$2
	_port=$3
	_user=$4
	printf 'name=%s\nport=%s\n' "$_name" "$_port" > "$_path"
	chown "$_user" "$_path"
}

# install_serve_dropin writes a systemd drop-in naming the sandbox's port, so
# the shipped unit template stays untouched and `make service` cannot put the
# default back.
install_serve_dropin() {
	_user=$1
	_port=$2
	dir="/home/$_user/.config/systemd/user/relevo-serve.service.d"
	mkdir -p "$dir"
	{
		printf '[Service]\n'
		printf 'ExecStart=\n'
		printf 'ExecStart=/home/%s/.local/bin/relevo serve --listen :%s\n' "$_user" "$_port"
	} > "$dir/10-port.conf"
	chown -R "$_user" "$dir"
}

cmd_destroy() {
	_name=$1
	valid_name "$_name" || die "invalid sandbox name '$_name': use ^[a-z][a-z0-9-]{0,11}\$ (a short lowercase token, no leading digit or dash)"

	_user="rv-$_name"
	# The two refusals below read the live account database. Under --dry-run
	# they are printed as the commands a real run would consult, so the plan
	# prints without depending on an account existing.
	if [ "$dry_run" -eq 1 ]; then
		plan id "$_user"
	else
		if ! id "$_user" >/dev/null 2>&1; then
			die "there is no user $_user, so there is no sandbox $_name to destroy"
		fi
		if [ "$_user" = "$(id -un)" ]; then
			die "refusing to destroy $_user: that is the account running this script"
		fi
	fi

	require_root

	note "destroying sandbox $_name (user $_user)"

	# Stop the units before the account goes: a user manager that outlives its
	# user would keep a daemon running with no home to resolve. The uid is
	# only known once the account exists, so under --dry-run it stands in as
	# the literal <uid> the printed command carries.
	if [ "$dry_run" -eq 1 ]; then
		_duid='<uid>'
		note "id -u $_user"
	else
		_duid=$(id -u "$_user")
	fi
	stop_user_unit "$_user" "$_duid" relevo.service
	stop_user_unit "$_user" "$_duid" relevo-serve.service
	# Terminate the sessions first, so the user manager -- and the units above
	# -- are gone before the account is removed. A dormant sandbox has no
	# sessions to terminate and no running manager, so both steps are
	# best-effort: userdel below is the backstop that fails loudly if a
	# process is really left behind.
	plan loginctl terminate-user "$_user" || true
	plan loginctl disable-linger "$_user"
	# -r removes the home with the account. This is the whole rollback: a
	# sandbox's state, its clone and its credentials all live under that home.
	# userdel fails when the account never received mail, after removing it;
	# only a surviving account is a real failure.
	plan userdel -r "$_user" || true
	if [ "$dry_run" -eq 0 ] && id "$_user" >/dev/null 2>&1; then
		die "userdel failed and $_user still exists"
	fi
}

cmd_list() {
	note 'USER        PORT  LINGER  RELEVO'
	_found=0
	for _home in /home/rv-*; do
		[ -d "$_home" ] || continue
		_user=$(basename "$_home")
		_found=1
		_marker="$_home/.local/state/relevo/sandbox"
		if [ -f "$_marker" ]; then
			_port=$(sed -n 's/^port=//p' "$_marker" 2>/dev/null | head -1)
			[ -n "$_port" ] || _port='-'
		else
			_port='-'
		fi
		if [ -f "/var/lib/systemd/linger/$_user" ]; then
			_linger=yes
		else
			_linger=no
		fi
		if [ -f "$_home/.config/systemd/user/relevo.service" ]; then
			_unit=installed
		else
			_unit=none
		fi
		printf '%-11s %-5s %-7s %s\n' "$_user" "$_port" "$_linger" "$_unit"
	done
	if [ "$_found" -eq 0 ]; then
		note '(no rv-* sandboxes)'
	fi
}

prog=relevo-dev-user.sh
[ "${0##*/}" = "$prog" ] || prog=${0##*/}

# Pull --dry-run out of the argument list before the subcommand reads it, so it
# can appear before or after the subcommand and after its arguments.
_dry=0
_args=""
# A hand-rolled pass keeps the remaining arguments in order without depending
# on "$@" word splitting rules that differ between shells.
for _a in "$@"; do
	if [ "$_a" = "--dry-run" ]; then
		_dry=1
	else
		_args="$_args $_a"
	fi
done
dry_run=$_dry
# shellcheck disable=SC2086 # the split is the point: _args is a list to become
# the script's positional parameters again, so the subcommand reads them with
# shift. A quoted "set -- $_args" would make the whole list one argument.
# shellcheck disable=SC2086
set -- $_args

if [ $# -eq 0 ]; then
	die "usage: $prog {create <name> [--port N] [--repo URL] [--ref REF] | destroy <name> | list} [--dry-run]"
fi

_sub=$1
shift
case $_sub in
create)
	[ $# -ge 1 ] || die "create needs a name"
	cmd_create "$@"
	;;
destroy)
	[ $# -ge 1 ] || die "destroy needs a name"
	cmd_destroy "$@"
	;;
list)
	cmd_list
	;;
-h | --help)
	note "usage: $prog {create <name> [--port N] [--repo URL] [--ref REF] | destroy <name> | list} [--dry-run]"
	;;
*)
	die "unknown subcommand $_sub"
	;;
esac