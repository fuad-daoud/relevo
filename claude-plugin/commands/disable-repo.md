---
description: Never let relevo register this repository's sessions
allowed-tools: Bash(${CLAUDE_PLUGIN_ROOT}/scripts/disable-repo.sh:*)
---

If the relevo MCP tools are not available in this session, or the block below printed the install hint, tell the user that relevo runs only in Claude Code with the relevo binary installed, then stop.

```!
${CLAUDE_PLUGIN_ROOT}/scripts/disable-repo.sh
```

The block above already ran: relevo will never register this repository's
sessions. Do not run `relevo mastermind disable --repo` again.
