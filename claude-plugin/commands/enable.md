---
description: Make this session a relevo MasterMind
allowed-tools: Bash(${CLAUDE_PLUGIN_ROOT}/scripts/enable.sh:*)
---

If the relevo MCP tools are not available in this session, or the block below printed the install hint, tell the user that relevo runs only in Claude Code with the relevo binary installed, then stop.

```!
${CLAUDE_PLUGIN_ROOT}/scripts/enable.sh
```

The block above already ran: this session is now a relevo MasterMind. Confirm in
one line, and do not run `relevo mastermind enable` again.
