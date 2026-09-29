---
name: capturing-tui-screens
description: Use when a change touches a terminal UI (bubbletea/lipgloss, relevo's cockpit, any full-screen TUI) and you need to see what it really draws, when goldens pass but the user reports the real screen looks wrong, stays on "loading…", or when you are about to ask the user to run the TUI and describe or screenshot it
---

# Capturing TUI screens

## Overview

You can run a full-screen TUI yourself: start it in a detached `tmux` pane of a fixed size, send
it keys, and capture the screen as text, ANSI or a PNG you can look at with Read. Golden tests
render fixtures. This renders **the real program on real data**, which is where layout, colour and
timing bugs show up. Don't hand the walk to the user until you have done it yourself.

## Quick reference

`T=~/.claude/skills/capturing-tui-screens/tui.sh`

| step | command |
|---|---|
| start at a known size (truecolor) | `$T start s 132x34 <binary> [args]` |
| wait for the first frame | `$T wait s 'fleet' 10` (a regex; fails with the screen dumped) |
| type / press keys | `$T keys s / text:ref-cleanup Enter Enter` (tmux names: `Escape`, `Down`, `Tab`, `C-c`) |
| wait for content, not time | `$T gone s 'loading…' 8` |
| read it | `$T text s` (layout), `$T ansi s` (escapes) |
| see it | `$T png s /path/shot.png`, then Read the PNG |
| clean up | `$T stop s` |

## Recipe for a TUI change

1. Build the branch's binary somewhere outside PATH, e.g. `go build -o ~/.cache/relevo-verify/relevo-x ./cmd/relevo`.
2. Capture at the widths that matter: 132x34 (the design size), 100x30, and 80x24.
3. For each screen the change touches: `start`, `wait`, `keys`, `wait`/`gone`, then `png` and Read it.
   Compare it with the design board side by side.
4. Wait on screen content (`wait`/`gone`), never on fixed `sleep`. Wait for something **unique to the
   target screen** (`wait s 'round [0-9]+ of'`), not for the old screen to vanish: headers are shared. A screen that never reaches the state
   you want is a finding: `gone 'loading…'` failing is exactly how the lost-reply race showed up.
5. When the screen is wrong and the code looks right, instrument: append timestamped lines to a file
   from the suspect paths (fetch issued/done, message received, view drawn). Run it in tmux and
   read the file. Restore the files afterwards.
6. Show the user the PNGs. Ask them to look only after your own pass.

## relevo specifics

- The cockpit reads live `relevo.db`. It is safe to navigate (`/`, `enter`, `esc`, `tab`, `[` `]`, `.`, `:`, `?`).
- **Never press action keys on real bindings:** `x D u g s E r b o`, or `y` in a confirm. To see a confirm, open it
  and cancel with `n`.
- Opening a binding: `keys s / text:<name> Enter Enter`. The first Enter applies the filter, the second opens.
- `enter` on a row marks its report viewed (the unread stamp). Prefer your own bindings.
- The installed `relevo` is the baseline. To tell a regression from an old bug, run the same steps on it.

## Common mistakes

- Missing `COLORTERM`: without truecolor, lipgloss downsamples and the PNG lies about colours. `start` sets it.
- Capturing before the first status arrives: `wait` for real content first.
- Leaving sessions running: always `stop`. `tmux ls` shows strays.
- Treating `text` as proof of colour or weight: only `ansi` or `png` show style.
