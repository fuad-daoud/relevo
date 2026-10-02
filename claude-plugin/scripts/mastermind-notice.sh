#!/bin/sh
# The plugin's UserPromptSubmit hook, silent for the same reason as the
# SessionStart script.
command -v relevo >/dev/null 2>&1 || exit 0
exec relevo mastermind notice --hook claude
