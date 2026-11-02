#!/bin/sh
# internal/burst/testdata/fake-provider.sh -- a stand-in for `burst report`
# behind CommandProvider's tests: the request arrives on stdin, the reply
# leaves on stdout, and failures are the exit code and stderr, exactly as
# the real provider exchanges.
#
# Usage: fake-provider.sh MODE   (one exchange per run; no network, no state)
#   ok      read the request; unless it carries "version":2 refuse it with
#           exit 2 (what the real burst does for any other version), then
#           print the fixed v2 response.
#   exit1   print a refusal on stderr and exit 1: the provider refusing to
#           answer at all.
#   sleep   read nothing and run two 30 s sleeps, one backgrounded so it
#           outlives the script's own process and holds the output pipes:
#           the exchange is past the head's 10 s timeout either way.
#   badjson print bytes that are not JSON on stdout and exit 0.
#   v1      print a v1 response on stdout and exit 0: the reply shape
#           relevo never shipped and must refuse.
set -eu

case "${1:-}" in
ok)
	request=$(cat)
	case $request in
	*'"version":2'*) ;;
	*)
		echo 'fake-provider: version: must be 2' >&2
		exit 2
		;;
	esac
	printf '%s\n' '{"version":2,"nodes":[{"id":"n7","name":"b7 cx32@hel1","url":"https://10.0.0.7:9443","fingerprint":"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","cap":2,"state":"ready"}],"preparing":null,"denied":null,"report_every":"45s"}'
	;;
exit1)
	echo 'fake-provider: no burst credentials configured' >&2
	exit 1
	;;
sleep)
	sleep 30 &
	sleep 30
	;;
badjson)
	printf 'not json at all\n'
	;;
v1)
	printf '{"version":1,"nodes":[]}\n'
	;;
*)
	echo "fake-provider: unknown mode: ${1:-}" >&2
	exit 3
	;;
esac
