#!/bin/sh
# scripts/relevo-dev-user_test.sh -- tests for the dev-sandbox user script.
#
# Every case runs with --dry-run, so the suite needs no root, creates no
# account and touches no real state. What it pins is the plan the script
# prints: the refusals, the order of the create steps, the clean environment
# the clone runs under, and the order of the destroy steps.
set -eu

# shellcheck disable=SC1007 # CDPATH= scopes an empty CDPATH to this one command
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
script="$here/relevo-dev-user.sh"
work=$(mktemp -d)
trap 'rm -rf "$work" 2>/dev/null || :' EXIT

fail=0

# run captures the script's output and status, so a case can assert on both the
# words and the exit code.
run() {
	out=$("$@" 2>&1) && got=0 || got=$?
}

# refuses asserts the invocation fails, with the description in its output.
refuses() {
	desc=$1
	want=$2
	shift 2
	run "$@"
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
	run "$@"
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

# before asserts the first string appears before the second, which is how the
# step order is pinned.
before() {
	desc=$1
	first=$2
	second=$3
	a=$(printf '%s\n' "$out" | grep -nF -- "$first" | head -1 | cut -d: -f1)
	b=$(printf '%s\n' "$out" | grep -nF -- "$second" | head -1 | cut -d: -f1)
	if [ -z "$a" ] || [ -z "$b" ]; then
		echo "FAIL: $desc (missing \"$first\" or \"$second\" in: $out)"
		fail=1
		return
	fi
	if [ "$a" -ge "$b" ]; then
		echo "FAIL: $desc (\"$first\" at line $a, \"$second\" at line $b; want the first before the second)"
		fail=1
	fi
}

# carries asserts the plan already in $out contains a string. It runs no
# command, so a case reads as "the create plan names X".
carries() {
	desc=$1
	want=$2
	if ! printf '%s' "$out" | grep -qF -- "$want"; then
		echo "FAIL: $desc (no \"$want\" in: $out)"
		fail=1
	fi
}

# absent asserts the output does not carry a string.
absent() {
	desc=$1
	want=$2
	if printf '%s' "$out" | grep -qF -- "$want"; then
		echo "FAIL: $desc (\"$want\" present in: $out)"
		fail=1
	fi
}

dry='--dry-run'
repo='--repo https://example.com/relevo.git'

# plan_create runs the create subcommand the way most cases below want it, and
# leaves its plan in $out for the assertions to read.
plan_create() {
	run sh "$script" create demo --port 7801 "$repo" "$dry"
}

# plan_destroy runs the destroy subcommand the same way.
plan_destroy() {
	run sh "$script" destroy demo "$dry"
}

# A name that is not a short lowercase token is refused, and named.
refuses 'an uppercase name is refused' 'invalid sandbox name' \
	sh "$script" create Demo --port 7801 "$repo" "$dry"
refuses 'a leading digit is refused' 'invalid sandbox name' \
	sh "$script" create 9demo "$repo" "$dry"
refuses 'a name with a slash is refused' 'invalid sandbox name' \
	sh "$script" create de/mo "$repo" "$dry"

# The default serve port belongs to the production serve on this host.
refuses 'port 7777 is refused' 'port 7777 is the default' \
	sh "$script" create demo --port 7777 "$repo" "$dry"

# A name whose socket path would not fit sun_path can never bind, so it is
# refused before the account is created.
long=$(printf 'a%.0s' $(seq 1 70))
refuses 'an over-long socket path is refused' 'sun_path limit' \
	sh "$script" create "$long" "$repo" "$dry"

# The create plan runs the clone and the build as the target user, under env -i
# with no XDG_ or RELEVO_ passthrough: an inherited environment is exactly what
# would resolve the sandbox's state under the parent's home.
plan_create
if [ "$got" -ne 0 ]; then
	echo "FAIL: create --dry-run exits $got: $out"
	fail=1
fi
if ! printf '%s' "$out" | grep -qF 'env -i HOME=/home/rv-demo'; then
	echo "FAIL: the create plan does not run the clone under env -i as the user ($out)"
	fail=1
fi
if ! printf '%s' "$out" | grep -qF 'XDG_RUNTIME_DIR=/run/user/'; then
	echo "FAIL: the clean environment names no XDG_RUNTIME_DIR ($out)"
	fail=1
fi
absent 'the clean environment leaks XDG_STATE_HOME' 'XDG_STATE_HOME='
absent 'the clean environment leaks XDG_CONFIG_HOME' 'XDG_CONFIG_HOME='
absent 'the clean environment leaks RELEVO_ state' 'RELEVO_'
absent 'the clean environment leaks CLAUDE_ state' 'CLAUDE'
if printf '%s' "$out" | grep -qF 'su -s /bin/sh -c'; then :; else
	echo "FAIL: the create plan does not run anything as the target user ($out)"
	fail=1
fi
plan_create
carries 'the clone names the --repo URL' 'https://example.com/relevo.git'
carries 'the checkout names a ref' 'git checkout'
run sh "$script" create demo --port 7801 "$repo" --ref release/1.0 "$dry"
if ! printf '%s' "$out" | grep -qF "git checkout 'release/1.0'"; then
	echo "FAIL: --ref is not honoured ($out)"
	fail=1
fi

# Lingering must come before the service is installed: without it, the user
# manager that would run the unit does not outlive the session installing it.
before 'linger precedes make service' \
	'loginctl enable-linger rv-demo' 'make service'
before 'the account is created before linger' \
	'useradd --create-home' 'loginctl enable-linger rv-demo'
before 'the build follows the clone' \
	'git clone' 'make service'

# The marker is written after the service, and hands the file to the user.
plan_create
carries 'the marker plan carries the name and port' \
	'write_marker /home/rv-demo/.local/state/relevo/sandbox demo 7801 rv-demo'
before 'the marker follows the build' 'make service' 'write_marker'

# With --port, the serve unit gets that port as a drop-in, and the start line
# is printed for the operator.
carries 'the serve drop-in names the port' 'install_serve_dropin rv-demo 7801'
carries 'the serve start line names the port' 'relevo serve --listen :7801'

# The credential steps are printed rather than attempted: they need the
# operator's own logins.
carries 'the login step is printed' 'sudo -iu rv-demo'
carries 'the agents step is printed' 'relevo config agents'
carries 'the doctor step is printed' 'relevo doctor'
carries 'the allowlist names the sandbox state root' '/home/rv-demo/.local/state/relevo'

# destroy order: the units stop, then the sessions, then lingering, then the
# account and its home go last.
allows 'destroy plans' 'userdel -r rv-demo' \
	sh "$script" destroy demo "$dry"
before 'the daemon unit stops before the account goes' \
	'systemctl --user disable --now relevo.service' 'userdel -r rv-demo'
before 'the serve unit stops before the account goes' \
	'systemctl --user disable --now relevo-serve.service' 'userdel -r rv-demo'
before 'the sessions are terminated before lingering is disabled' \
	'loginctl terminate-user rv-demo' 'loginctl disable-linger rv-demo'
before 'lingering is disabled before the account goes' \
	'loginctl disable-linger rv-demo' 'userdel -r rv-demo'
before 'the account goes last' 'loginctl terminate-user rv-demo' 'userdel -r rv-demo'

# destroy refuses a name that is not a sandbox token, so it can never be aimed
# at an ordinary account.
refuses 'destroy refuses an invalid name' 'invalid sandbox name' \
	sh "$script" destroy ../root "$dry"

# Every account destroy touches is the rv- form of the name it was given, so
# an ordinary account is never a target. The refusal of the invoking account
# itself needs a real account database and is exercised by a real destroy.
plan_destroy
me=$(id -un)
if printf '%s\n' "$out" | grep -E '(userdel|loginctl|systemctl)[^\n]*[^v ]\b'"$me"'\b' | grep -v "rv-$me" | grep -q .; then
	echo "FAIL: the destroy plan names the invoking account $me unprefixed: $out"
	fail=1
fi
carries 'the destroy plan targets the rv- account' "rv-demo"
absent 'the destroy plan does not touch an unprefixed account' "userdel -r demo"

# list needs no root and no arguments.
run sh "$script" list "$dry"
if [ "$got" -ne 0 ]; then
	echo "FAIL: list exits $got: $out"
	fail=1
fi
carries 'list prints a header' 'USER'
carries 'list reports every sandbox' 'LINGER'

# Without --dry-run, create refuses to run as a non-root caller. Skipped when
# the suite happens to run as root, where the refusal cannot be established.
if [ "$(id -u)" -ne 0 ]; then
	refuses 'a non-root create without --dry-run refuses' 'must run as root' \
		sh "$script" create demo --port 7801 "$repo"
else
	echo "skip: the non-root refusal (running as root)"
fi

[ "$fail" -eq 0 ] && echo "relevo-dev-user: ok"
exit "$fail"