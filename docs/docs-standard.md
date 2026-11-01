# Documentation standard

How relevo's documentation is organised and written. Code comments follow
[Code style](style.md#comments); this page covers everything else.

## What goes where

| Home | Holds |
| --- | --- |
| `README.md` | What relevo is, requirements, install, first run, a quick start, and a map of the `docs/` pages. A newcomer reads it top to bottom. |
| A page in `docs/` | One topic, complete: a reference (commands, the cockpit, candidates), a guide (style, architecture), or a runbook. |
| `CLAUDE.md` | Instructions for an agent working on this repository: how to dispatch and verify builders here, the rules it must not break. It links to the guides rather than restating them. |
| `internal/mastermind/guide.md` | The protocol relevo injects into a MasterMind's session. It ships in the binary, so it describes relevo, not this repository. |
| Code comments | Why the code is the way it is, for someone reading it. |
| `docs/plans/`, `docs/specs/`, `docs/superpowers/` | Local working files. They are gitignored and never committed. |

## Writing rules

- **Present tense, true now.** A page describes what relevo does today. No
  history: no issue or pull-request numbers, no "used to" or "was renamed
  from", no dates of change. Release notes and git hold history; a renamed
  command or a migration step belongs in the release notes of the version
  that changed it.
- **One place per fact.** Each fact lives on one page. Every other page links
  to it instead of restating it, so a change is made once.
- **Commands run as written.** Every command in a code block runs exactly as
  shown, from the directory the page says. Use `<placeholders>` for values
  the reader supplies, and say what they are.
- **Tables for flags and fields.** A flag, a config key or a document field
  goes in a table with its meaning and default; prose explains how they fit
  together.
- **Plain words.** Name a thing the way the CLI and the cockpit name it.
  Define a term once, where it first appears on its page.

## Adding a page

1. Check the map in `README.md` first: the topic may already have a page
   that the new material belongs in.
2. Create `docs/<topic>.md`, with one `#` heading naming the topic and an
   opening paragraph saying what the page covers.
3. Add one line for it to the map in `README.md`.
4. Move, don't copy: when the new page takes over material from another page,
   delete it there and leave a link.
5. Check every relative link and anchor on the pages you touched resolves.
