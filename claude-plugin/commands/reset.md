---
description: Ask the relevo consent question again in this repository and this session
allowed-tools: Bash(${CLAUDE_PLUGIN_ROOT}/scripts/reset.sh:*)
---

If the relevo MCP tools are not available in this session, or the block below printed the install hint, tell the user that relevo runs only in Claude Code with the relevo binary installed, then stop.

```!
${CLAUDE_PLUGIN_ROOT}/scripts/reset.sh
```

The block above already ran: the consent question is asked again in this
repository. Do not run `relevo mastermind reset` again.
