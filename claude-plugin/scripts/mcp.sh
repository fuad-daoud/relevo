#!/bin/sh
# The plugin's MCP server. exec leaves stdio (JSON-RPC) untouched; with no
# relevo on PATH the session must learn why its tools are gone.
command -v relevo >/dev/null 2>&1 || {
	echo 'relevo plugin: the relevo binary is not on PATH. Install it: https://github.com/fuad-daoud/relevo#install' >&2
	exit 1
}
exec relevo mcp
