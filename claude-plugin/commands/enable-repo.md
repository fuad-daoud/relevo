---
description: Make relevo this repository's MasterMind from now on
allowed-tools: Bash(${CLAUDE_PLUGIN_ROOT}/scripts/enable-repo.sh:*)
---

If the relevo MCP tools are not available in this session, or the block below printed the install hint, tell the user that relevo runs only in Claude Code with the relevo binary installed, then stop.

```!
${CLAUDE_PLUGIN_ROOT}/scripts/enable-repo.sh
```

The block above already ran: relevo is now this repository's MasterMind. Confirm
in one line, and do not run `relevo mastermind enable --repo` again.
