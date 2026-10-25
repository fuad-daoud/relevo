#!/bin/sh
# The plugin's UserPromptSubmit hook, silent for the same reason as the
# SessionStart script.
command -v relevo >/dev/null 2>&1 || exit 0
[ -n "$MUSE_PLUGIN_ID" ] && exec relevo mastermind notice --hook muse
exec relevo mastermind notice --hook claude
