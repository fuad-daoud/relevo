#!/bin/sh
# /relevo:status. "$@" forwards the command's arguments.
command -v relevo >/dev/null 2>&1 || {
	echo 'relevo plugin: the relevo binary is not on PATH. Install it: https://github.com/fuad-daoud/relevo#install'
	exit 0
}
exec relevo status "$@"
