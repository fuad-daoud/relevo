---
description: Show a round's prompt, report, diff, drift, log or transcript
argument-hint: "[<binding>] [--round N] [--diff|--drift|--log|--report|--prompt|--transcript]"
allowed-tools: Bash(relevo:*)
---

```!
relevo show $ARGUMENTS
```

The block above is the round's content, already fetched. Say what happened
or anything that needs a decision. Do not run `relevo show` again -- you
already have it.
