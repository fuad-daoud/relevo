---
description: Stop being relevo's MasterMind in this session
allowed-tools: Bash(${CLAUDE_PLUGIN_ROOT}/scripts/disable.sh:*)
---

If the relevo MCP tools are not available in this session, or the block below printed the install hint, tell the user that relevo runs only in Claude Code with the relevo binary installed, then stop.

```!
${CLAUDE_PLUGIN_ROOT}/scripts/disable.sh
```

The block above already ran: this session is no longer a relevo MasterMind. Do
not run `relevo mastermind disable` again.
