#!/bin/sh
# /relevo:enable-repo. A command's ! block shows stdout in chat, so the missing
# binary reads as an install hint instead of a shell error.
command -v relevo >/dev/null 2>&1 || {
	echo 'relevo plugin: the relevo binary is not on PATH. Install it: https://github.com/fuad-daoud/relevo#install'
	exit 0
}
exec relevo mastermind enable --repo
