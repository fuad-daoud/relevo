---
description: Show relevo's bindings for this MasterMind
argument-hint: "[--name <binding>] [--all]"
allowed-tools: Bash(${CLAUDE_PLUGIN_ROOT}/scripts/status.sh:*)
---

If the relevo MCP tools are not available in this session, or the block below printed the install hint, tell the user that relevo runs only in Claude Code with the relevo binary installed, then stop.

```!
${CLAUDE_PLUGIN_ROOT}/scripts/status.sh $ARGUMENTS
```

The block above is relevo's live state for this MasterMind, already fetched.
Say only what changed or what needs a decision. Do not call the relevo
status tool or run `relevo status` again -- you already have the answer.
