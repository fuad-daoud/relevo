# Probe round 2: hand a report to the chat, and free keys (#393)

A second **spike** round on binding `oc-tui-probe`, on top of round 1's commit
`4e3f0978` (`docs(specs): OpenCode 2.0.14 TUI plugin probe (#393)`). No Go
code changes. The deliverable is evidence: an extended probe, new captures, and
a new section in the findings doc.

## 1. System Overview

Round 1 established the real OpenCode 2.0.14 plugin surface (package directory
under `<config>/plugins/<name>/`, `exports["./tui"]` module exporting
`{ id, setup }`, `api.ui.slot`, `api.ui.router`, `api.ui.dialog.*`,
`api.ui.toast.show`, `api.keymap.layer` called from inside a slot render, and
`OPENCODE_CONFIG_DIR` isolation). Its findings are in
`docs/specs/2026-09-24-opencode-tui-probe.md` and the driver in
`docs/specs/probes/2026-09-24-opencode-tui/run.sh`.

Two questions remain before the #393 spec can be written:

1. **Handoff.** Can the plugin put text (a builder's report or question) into
   the planner's chat prompt as an **unsent draft** that the user reviews and
   submits? Candidates from round 1's `out/loaded.json` `api_map`:
   `api.data.session.input`, `api.data.session.prompt`,
   `api.client.session.synthetic`, `api.client.session.inbox`,
   `api.client.session.prompt`.
2. **Keys.** Are `ctrl+x r` and `ctrl+x n` (leader `ctrl+x`, then `r` / `n`)
   free in OpenCode's default keymap, and does a plugin binding on them fire?

Plus one survey item: what `api.ui.panel` and `api.ui.tabs` are (signature
and one rendered example), because the design wants a tabbed builder-detail
page.

## 2. File Structure

Only these paths may change:

```
docs/specs/2026-09-24-opencode-tui-probe.md             add a section "Round 2: handoff and keys" (append; do not rewrite round 1's text)
docs/specs/probes/2026-09-24-opencode-tui/
  run.sh                                                 add stage "r2" (see §6); keep round 1's stages runnable
  relevo-probe.tsx                                       add R10-R14 (see §4); keep R1-R9
  out/r2-*.txt / out/r2-*.ansi                           round 2 captures
  out/r2-handoff.json                                    see §3
  out/r2-keys.json                                       see §3
  out/r2-signatures.json                                 see §3
```

## 3. Data Structures

**`out/r2-signatures.json`** -- written at `setup`, before any call is made.
For each of these paths: `data.session.input`, `data.session.prompt`,
`data.session.get`, `client.session.synthetic`, `client.session.inbox`,
`client.session.prompt`, `client.session.create`, `client.session.remove`,
`keymap.shortcuts`, `keymap.dispatch`, `ui.panel`, `ui.tabs` ->
`{ exists: bool, arity: number|null, src: string|null }` where `src` is the
first 1500 characters of `Function.prototype.toString` of the function (or of
the object's own keys, joined, if it is an object and not a function).

**`out/r2-handoff.json`** -- an array, one entry per attempt H1-H4 (§4):
`{ id: "H1".."H4", api: string, args_shape: string, called: bool, ok: bool,
result: string (JSON, first 800 chars) | null, error: string | null,
prompt_draft_after: string | null, messages_added: number | null }`.
`prompt_draft_after` is what the TUI prompt shows in the capture taken right
after the attempt (copy it from the `.txt` capture). `messages_added` is the
count of rows in `opencode.db` table `message` for the throwaway session,
after minus before.

**`out/r2-keys.json`**:
- `leader` string: the leader key as OpenCode reports it
- `shortcuts` array: the full `keymap.shortcuts()` result (or whatever
  `keymap.shortcuts` returns; if it needs arguments, record the call you made)
- `taken`: for each of `ctrl+x r`, `ctrl+x n`, `<leader>r`, `<leader>n`,
  `ctrl+x R`, `ctrl+x shift+r` -> the command id already bound to it, or null
- `fired`: for each binding the probe registers (R12) -> bool, from the marker
  file the command's `run` writes

## 4. Component Contracts (probe additions)

All additions go in `relevo-probe.tsx`, each individually try/caught and
recorded as in round 1.

**The throwaway session (precondition for H1-H4).** `run.sh` stage `r2`
creates it before launching the TUI -- with `opencode session` subcommands if
one creates a session, else from the plugin via `api.client.session.create`
with title `relevo-probe r2 (delete me)` -- records its id in
`out/r2-handoff.json` (top-level key `session_id`, so make the file an object
`{ session_id, attempts: [...] }`), launches the TUI on it with `-s <id>`, and
removes it at cleanup (`api.client.session.remove` or `opencode session`
subcommand; record which). **H1-H4 act only on this session.** Never on
`ses_f6e5046f0ffeBTguUw5PDdljfv` or any other existing session.

| # | What | Rule |
|---|------|------|
| R10 | signatures | write `r2-signatures.json` at setup |
| H1 | `api.data.session.input` | read its `src` first. If it sets the draft input of a session's prompt, call it with the throwaway session id and the text `RELEVO-HANDOFF-H1 report from webshop r4`. Capture. |
| H2 | `api.data.session.prompt` | same procedure, text `RELEVO-HANDOFF-H2`. If its `src` shows it **submits** (sends to the model), do not call it: record `called:false` and why. |
| H3 | `api.client.session.synthetic` | read `src`. Call it only if the `src` or its arguments show a way to add a message **without** a model reply (e.g. a `noReply` / `synthetic` flag). Text `RELEVO-HANDOFF-H3`. Record `messages_added`. |
| H4 | `api.client.session.inbox` | read `src`. Record what it does. Call it only under the same no-model-reply rule as H3. Text `RELEVO-HANDOFF-H4`. |
| -- | `api.client.session.prompt` | **never called** in this round. Record its signature only. |
| R11 | keymap survey | at the first slot render (where `keymap.layer` works), call `keymap.shortcuts` and write `r2-keys.json` `leader`, `shortcuts`, `taken` |
| R12 | bindings | register, through `keymap.layer` from inside the slot render as round 1 does, two commands: `relevo.probe.leader_r` bound to leader+`r` and `relevo.probe.leader_n` bound to leader+`n` (use the binding syntax `shortcuts` shows for existing leader bindings, e.g. how `sidebar_toggle` is written). Each `run` writes `out/r2-fired-<r|n>.json` and shows a toast. |
| R13 | panel/tabs | render one `api.ui.tabs` (three tabs `plan` / `report` / `transcript`, each with one line of text) inside the round-1 plugin route page, and one `api.ui.panel` if its signature allows. Capture it. If either throws, record the error verbatim. |

## 5. Questions the new findings section must answer

For each: verdict (`WORKS` / `PARTIAL` / `FAILS` / `NOT TESTED`), evidence
files, the exact call that worked, errors verbatim.

- **Q13 draft handoff.** Can a plugin put text into a session's prompt as an unsent draft? Which API, with what arguments? Does the text appear in the prompt box (capture) and is **nothing** sent (`messages_added` = 0)?
- **Q14 message handoff.** Can a plugin add a message to a session without triggering a model reply (H3/H4)? What does the user see in the chat?
- **Q15 keys.** Are leader+`r` and leader+`n` free? What else is bound near them? Do the probe's bindings fire when pressed in tmux (`C-x` then `r`)?
- **Q16 tabs/panel.** What are `ui.tabs` and `ui.panel`, and do they render inside a plugin route?

Then update "Consequences for the #393 design" by **appending** bullets under
a sub-heading `After round 2`, 3-6 bullets, facts only.

## 6. Pseudocode: `run.sh` stage `r2`

```
reuse round 1's isolation (OPENCODE_CONFIG_DIR = the probe dir), tmux session name,
capture helper, C-u-before-typing rule and cleanup trap. Every launch keeps --standalone.

stage r2:
  create the throwaway session (§4); record its id
  count its message rows in ~/.local/share/opencode/opencode.db -> BEFORE
  launch: opencode --standalone -s <throwaway id>  (env RELEVO_PROBE_OUT, OPENCODE_CONFIG_DIR, RELEVO_PROBE_STAGE=r2)
  wait for r2-signatures.json (max 30 s: round 1 saw a >10 s plugin splash)
  capture r2-01-session
  trigger H1..H4 one at a time from a palette command `relevo.probe.h<N>`
    (register them in R12's layer; run each from the palette, not a slash command,
     so nothing is typed into the chat prompt), capture r2-0<N+1>-h<N> after each,
     then clear the draft with C-u before the next
  count message rows -> AFTER ; messages_added = AFTER - BEFORE (per attempt: count after each)
  press C-x then r ; capture r2-06-leader-r ; press C-x then n ; capture r2-07-leader-n
  open the route page, switch to the R13 tabs ; capture r2-08-tabs
  cleanup: remove the throwaway session; kill only tmux session relevo-probe
```

The H commands run from the palette so the probe never types text into the
chat prompt itself; anything that appears there came from the API under test.

## 7. Error Handling

As round 1: nothing escapes `setup`; a missing capture is recorded as
`NOT TESTED` with the reason. Only §8 stops the round.

## 8. Halt conditions -- stop and report, do not improvise

- Any step would send a message that triggers a model reply, in any session.
- Any step would write to a session other than the throwaway one.
- The throwaway session cannot be created or cannot be removed at the end
  (report its id so the planner can remove it).
- Anything under `~/.config/opencode/` would have to change.

Round 1 kept going past a contradicted premise and recorded it, which was the
right call there. In this round, the four conditions above are hard stops.

## 9. Working Efficiently

- Read once, in one batch: `docs/specs/2026-09-24-opencode-tui-probe.md`,
  `docs/specs/probes/2026-09-24-opencode-tui/relevo-probe.tsx`,
  `docs/specs/probes/2026-09-24-opencode-tui/run.sh`, and
  `docs/specs/probes/2026-09-24-opencode-tui/out/loaded.json` (`api_map`
  only). They hold everything round 1 learned; do not re-derive the loader.
- Make the `relevo-probe.tsx` additions in one edit and the `run.sh` stage in
  one edit.
- Iterate with `RELEVO_PROBE_STAGE=r2 bash docs/specs/probes/2026-09-24-opencode-tui/run.sh`
  (add the stage selector if `run.sh` has none, so round 1's stages do not
  rerun each time). Read `out/r2-*` after each run.
- Final check, once: `make check` and `git status --short` (only the paths in
  §2 may appear).

## 10. Ordered Implementation Steps

1. **Read** the four files in §9. *Done when:* you can name round 1's
   `keymap.layer` call site and its command shape.
2. **Extend the probe**: R10-R13 and the H1-H4 palette commands in
   `relevo-probe.tsx`; stage `r2` and a stage selector in `run.sh`.
   *Depends on 1.* *Done when:* `bash -n run.sh` passes.
3. **Run and iterate** until `r2-signatures.json`, `r2-handoff.json`,
   `r2-keys.json` and captures `r2-01`..`r2-08` exist or their absence is
   recorded. *Depends on 2.*
4. **Findings**: append "Round 2: handoff and keys" (Q13-Q16) and the
   `After round 2` consequences. *Depends on 3.*
5. **Verify and commit** on this branch:
   `docs(specs): OpenCode TUI probe round 2 -- handoff and keys (#393)`.
   *Depends on 4.*

## 11. Report

Include: the verdict line for Q13-Q16, the `After round 2` bullets verbatim,
the throwaway session id and proof it was removed (the `opencode session`
listing or the db row count), `messages_added` per attempt, and
`git diff --stat HEAD~1`.
