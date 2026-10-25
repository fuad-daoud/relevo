#!/bin/sh
# The plugin's SessionStart hook. Stay silent when relevo is missing:
# anything printed here lands in the session.
command -v relevo >/dev/null 2>&1 || exit 0
[ -n "$MUSE_PLUGIN_ID" ] && exec relevo mastermind init --hook muse
exec relevo mastermind init --hook claude
