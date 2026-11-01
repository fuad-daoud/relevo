# Documentation standard

This standard states where each fact lives and how each page reads. One fact lives in one place; every other page links to it.

## What goes where

| Home | Holds | Stated in |
| --- | --- | --- |
| `README.md` | What relevo is, requirements, install, first run, quick start, the command surface, and the map of every `docs/` page | `README.md:8-20`, `## Requirements`, `## Install`, `## First run on a clean machine`, `## Quick start`, `## Command surface` |
| One `docs/` page | One topic: verifying and merging (`runbook.md`), the default sandbox (`env-sandbox.md`), the per-user sandbox (`dev-sandbox.md`), comment and size rules (`style.md`), the layer map (`architecture.md`) | Each page's first lines: `docs/runbook.md:1-7`, `docs/env-sandbox.md:1-11`, `docs/dev-sandbox.md:1-12`, `docs/style.md:1-3`, `docs/architecture.md:1-3` |
| `CLAUDE.md` | Agent instructions: working with builders, verifying, testing in isolation, conventions, code style, merging | `CLAUDE.md:7-96` |
| Code comments | Why-for-the-reader: the constraint, number source, ordering or hazard the code cannot state | `docs/style.md` "Comments" |
| `docs/plans/`, `docs/specs/`, `docs/superpowers/` | Local working files, gitignored, never committed | `CLAUDE.md:72-73` |

## Writing rules

Present tense, describing what is true now. No history: no issue or pull-request numbers, no "used to" stories, no dates of change. Release notes and git hold history.

One place per fact: link to it, never restate it. The sibling guides cite their source (`CLAUDE.md "Code style"`, `docs/style.md`) instead of copying it; this page does the same.

Every command runs as written: each fenced command executes verbatim from the repository root before the page lands. A command that needs flags or a different directory shows them.

Tables carry flags, fields and mappings; prose carries the rest. The layer map (`docs/architecture.md` "Layers") and the sandbox variables (`docs/env-sandbox.md:39-50`) are the shape to follow.

## Adding a page

One page holds one topic and links from the README map. It links outward to the one home of each fact it touches and restates none of them. It shows only commands that ran verbatim from the repository root.

Each check below runs as written from the repository root:

```
wc -l docs/docs-standard.md
git status --short
```

`wc` keeps the page near its budget (~250 lines); the `status` shows only the new page.
