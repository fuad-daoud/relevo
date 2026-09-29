# Probe: what an OpenCode 2.0.14 TUI plugin can do for relevo (#393)

A one-round **spike**. No Go code changes. The deliverable is evidence: a
throwaway probe plugin, the script that drives it, the screen captures it
produced, and a findings document that answers every question in §5 with a
verdict and the exact API shape that worked.

## 1. System Overview

Issue #393 wants a relevo TUI plugin for OpenCode: this planner's builders in
the session sidebar, toasts on NEEDS YOU / report delivered, and -- added by
the user -- an interactive view embedded in OpenCode (browse this planner's
bindings and rounds, read plans / reports / transcripts, act on NEEDS YOU).

Nothing is designed yet because four facts are unknown:

1. The installed plugin SDK types are `@opencode-ai/plugin` 1.18.25
   (`~/.config/opencode/node_modules/@opencode-ai/plugin/dist/tui.d.ts`),
   but the binary is `opencode v2.0.14`. The slot names
   (`sidebar_content`, `home_bottom`, ...) do not appear as plain strings in
   `/usr/bin/opencode`. Nobody has confirmed a TUI plugin that *renders*
   anything loads on 2.0.14. (herdr's plugin, `~/.config/opencode/herdr-tui-session.js`,
   renders nothing: it only polls `api.route.current`.)
2. Slots, routes and dialogs render Solid/OpenTUI JSX
   (`/** @jsxImportSource @opentui/solid */` in upstream's example). We do
   not know whether OpenCode transpiles a `.tsx` **file** plugin at load
   time, or whether a plain `.js` plugin can import `@opentui/solid` /
   `solid-js` (they are NOT in `~/.config/opencode/node_modules`).
3. Whether the plugin can run a subprocess (`relevo status --json`) from the
   TUI process, and with what environment.
4. How much interactivity works in practice: full-screen routes, dialogs,
   slash commands / palette commands, scrollable long text.

The probe answers these by loading a probe plugin into a **private**
OpenCode TUI running inside a detached tmux session, and capturing the
screen.

Upstream reference (read-only, for API shapes): the SDK types above, and
upstream's spec `packages/opencode/specs/tui-plugins.md`, which says:
file plugins are listed in `tui.json`/`tui.jsonc` `"plugin": [...]`
(relative paths resolve against the config file); a file module must
`export default { id: "<non-empty>", tui }` and must not export `server`;
`api.keymap.registerLayer({ commands: [{ name, title, category, namespace:
"palette", slashName, run() }], bindings: [{ key, cmd, desc }] })`;
`api.route.register([{ name, render: () => <box><text>..</text></box> }])`
then `api.route.navigate(name)`; `api.slots.register({ order, slots: {
home_footer() { return <View/> } } })`. The session sidebar is a
42-column box with 2-column padding each side, and `sidebar_content` sits in
a scrollbox under `sidebar_title`.

## 2. File Structure

Everything new lives under one probe directory plus one findings doc. No
other path in the repo changes.

```
docs/specs/2026-09-24-opencode-tui-probe.md        findings: one section per question in §5
docs/specs/probes/2026-09-24-opencode-tui/
  README.md                                          how to re-run the probe (one paragraph)
  run.sh                                             the driver: temp config, tmux, keys, captures, cleanup
  relevo-probe.tsx                                   probe plugin, variant A (JSX file plugin)
  relevo-probe.js                                    probe plugin, variant B (no JSX) -- only if A fails to render
  tui.jsonc                                          the probe's TUI config (lists exactly one variant)
  out/                                               everything the run produced, committed
    loaded.json                                      written by the plugin at load (see §3)
    spawn.json                                       subprocess results (see §3)
    select.json / prompt.json                        dialog results (see §3)
    NN-<stage>.txt                                   plain captures   (tmux capture-pane -p)
    NN-<stage>.ansi                                  coloured captures (tmux capture-pane -p -e)
    opencode.log                                     the TUI's own log, if --print-logs / --log-level gives one
```

## 3. Data Structures

These are the files the probe plugin writes into `$RELEVO_PROBE_OUT`
(an env var `run.sh` sets to the absolute `out/` path). JSON, pretty-printed.

**`loaded.json`** -- written once at the top of `tui(api, options, meta)`,
before anything else can throw:
- `app_version` string: `api.app.version`
- `api_keys` string[]: `Object.keys(api)` sorted
- `ui_keys`, `route_keys`, `keymap_keys`, `slots_keys`, `state_keys` string[]: `Object.keys` of each
- `has_command_legacy` bool: `api.command !== undefined`
- `route_current` object: `api.route.current` at load
- `meta` object: the `meta` argument
- `runtime` object: `{ bun: typeof Bun !== "undefined" ? Bun.version : null, node: process.versions.node ?? null }`
- `env` object: for each of `RELEVO_PLANNER`, `OPENCODE_TERMINAL`, `OPENCODE_CLIENT`, `OPENCODE_CONFIG_DIR`, `XDG_CONFIG_HOME`, `PATH` -> its value or null
- `imports` object: for each specifier in `["@opentui/solid", "solid-js", "@opentui/core", "@opencode-ai/plugin/tui"]` -> `{ ok: bool, keys: string[] (first 60 export names, sorted) | null, error: string | null }` via dynamic `import()`
- `errors` string[]: any exception message caught later in the plugin is appended and the file rewritten

**`spawn.json`** -- one entry per attempt, in order:
- `method` string: `"Bun.spawn"` | `"node:child_process.execFile"`
- `argv` string[]: `["relevo","version"]` then `["relevo","status","--json"]`
- `ok` bool, `exit_code` number | null, `ms` number (wall time), `stdout_head` string (first 400 chars), `stderr_head` string (first 400 chars), `error` string | null

**`select.json`** -- `{ "selected": <option value> }` written by the DialogSelect `onSelect`.
**`prompt.json`** -- `{ "value": <string> }` written by the DialogPrompt `onConfirm`.

## 4. Component Contracts (what the probe plugin registers)

Plugin id: `relevo.probe`. Everything below is registered inside `tui()`.
Wrap each registration in its own try/catch that appends to `loaded.json`
`errors` with a label, so one failing API does not hide the others.

| # | API | Registration | Visible evidence |
|---|-----|--------------|------------------|
| R1 | `api.slots.register` | slot `sidebar_content(ctx, props)`: two lines. Line 1: `relevo-probe sidebar <props.session_id>`. Line 2: `tick <N>`, where N is a Solid signal incremented every 1000 ms by a `setInterval` cleared via `api.lifecycle.onDispose`. Line 3: the text `NEEDS YOU` in bold, coloured `ctx.theme.current.warning` (or `api.theme.current.warning`). Line 4: a 60-character string of `x` (tests wrapping/truncation in a 38-col slot). | sidebar shows all four lines; tick advances between two captures |
| R2 | `api.slots.register` | slot `home_bottom()`: one line `relevo-probe home` | home screen shows it |
| R3 | `api.ui.toast` | at load: `{ variant: "warning", title: "relevo-probe", message: "toast ok", duration: 10000 }` | toast visible in first capture |
| R4 | `api.keymap.registerLayer` | commands `relevo.probe.page` (slashName `relevo-probe`), `relevo.probe.select` (slashName `relevo-probe-select`), `relevo.probe.prompt` (slashName `relevo-probe-prompt`); all `category: "relevo"`, `namespace: "palette"` | slash commands run; palette lists them |
| R5 | `api.route.register` + `navigate` | route `relevo-probe`: a full-screen page with a header line `relevo-probe page`, then a `<scrollbox>` containing 200 lines `line 001` ... `line 200`. `relevo.probe.page` navigates to it. Also try, inside the page, one `<markdown>` element (content `# heading\n\n**bold** and \`code\``) and one `<code>` element; record in `loaded.json.errors` if either intrinsic is unknown | page shows header + first lines; scrolling moves the lines |
| R6 | `api.ui.DialogSelect` via `api.ui.dialog.replace` | `relevo.probe.select` opens a DialogSelect titled `relevo-probe select`, options `alpha`/`beta`/`gamma` (value = title); `onSelect` writes `select.json` then `api.ui.dialog.clear()` | dialog visible; `select.json` holds the chosen value |
| R7 | `api.ui.DialogPrompt` via `api.ui.dialog.replace` | `relevo.probe.prompt` opens a DialogPrompt titled `relevo-probe prompt`; `onConfirm(value)` writes `prompt.json` then clears | dialog visible; `prompt.json` holds the typed text |
| R8 | subprocess | at load, after R1-R7 are registered, run the two argv of §3 `spawn.json` with each method (Bun.spawn first, then node `child_process.execFile`), each with a 5 s timeout, writing `spawn.json` | `spawn.json` |
| R9 | `api.attention.notify` | at load: `{ title: "relevo-probe", message: "attention ok", notification: false, sound: false }`; append the resolved result object to `loaded.json` under key `attention` | `loaded.json.attention` |

## 5. Questions the findings doc must answer

For each: **verdict** (`WORKS` / `PARTIAL` / `FAILS` / `NOT TESTED`), the
evidence file(s), and the exact API shape or import that worked. Quote error
text verbatim.

- **Q1 load.** Does a file TUI plugin listed in `tui.jsonc` load on 2.0.14? What `api.app.version` does it report? Is `api.command` (legacy) still present?
- **Q2 authoring.** Does variant A (`.tsx`, `/** @jsxImportSource @opentui/solid */`) render? If not, what error, and does variant B work -- a `.js` file that renders without JSX using whatever `@opentui/solid` / `solid-js` export (`createComponent`, `createElement`, `insert`, `h`, ...) the `imports` probe shows? State which one relevo should ship and whether it needs a build step.
- **Q3 slots.** Do `sidebar_content` and `home_bottom` render? Measure the usable width of `sidebar_content` in columns from the capture, and say what happens to the 60-char line (wrap, truncate, clip).
- **Q4 reactivity.** Does the tick advance without user input (a signal set from `setInterval` re-renders the slot)?
- **Q5 styling.** Does theme colour + bold render in the `.ansi` capture? Does a raw ANSI escape inside `<text>` render as colour or as garbage? (Add a fifth sidebar line containing `"\x1b[33mraw-ansi\x1b[0m"` to answer this.)
- **Q6 toast.** Does R3 show? Where on screen, for how long?
- **Q7 commands.** Do the three slash commands run? Does the command palette (default `ctrl+p`, or `ctrl+k` in the user's global config -- check which applies in the probe's config) list them under `relevo`?
- **Q8 routes.** Does R5 navigate to a full-screen page? Does the scrollbox scroll (PageDown / arrow keys / mouse wheel -- test keys only)? How does the user get back (Esc? a keybind?) -- record what worked. Do `<markdown>` and `<code>` exist?
- **Q9 dialogs.** Do R6 and R7 open, take keyboard input, and call back (`select.json` / `prompt.json` present with the right values)?
- **Q10 subprocess.** Which spawn method works, how long do `relevo version` and `relevo status --json` take, and is `RELEVO_PLANNER` set in the TUI process (expected: no)?
- **Q11 attention.** What does `attention.notify` return with the default (unconfigured) attention settings?
- **Q12 failure mode.** When a plugin throws at load or imports a missing module, does the TUI still start (and is there a visible warning)? Answer from whatever happened during Q2; do not engineer an extra failure if Q2 already produced one.

End the doc with a **"Consequences for the #393 design"** section: 5-10
bullets stating, as facts from this probe, what the real plugin can and
cannot rely on (e.g. "ship a single .js file, no build step", or "needs a
bundling step because ..."). No design proposals beyond that.

## 6. Pseudocode: `run.sh`

```
set -euo pipefail
PROBE_DIR = directory of this script (absolute)
OUT = $PROBE_DIR/out ; recreate it empty
TMP = mktemp -d under $PROBE_DIR/.tmp (gitignored? no -- delete it at the end)
SESSION_TMUX = "relevo-probe"          # the ONLY tmux session this script may create or kill

isolate config:
  CONFIG = $TMP/opencode ; mkdir
  copy $PROBE_DIR/tui.jsonc -> $CONFIG/tui.jsonc, rewriting the plugin path to the absolute probe file
  symlink ~/.config/opencode/opencode.jsonc -> $CONFIG/opencode.jsonc   (providers/permissions; read-only use)
  first attempt:  env OPENCODE_CONFIG_DIR=$CONFIG
  if loaded.json never appears within 20 s: second attempt with XDG_CONFIG_HOME=$TMP instead
  if neither loads the plugin: HALT (see §8) -- do NOT fall back to editing ~/.config/opencode

pick a session for the sidebar:
  the most recently updated TOP-LEVEL session (no parent) from `opencode session list`
  (or sqlite3 ~/.local/share/opencode/opencode.db, table session, parent_id NULL, newest time_updated)
  record its id in the findings doc

stage "home":
  tmux new-session -d -s $SESSION_TMUX -x 160 -y 45 "<env> RELEVO_PROBE_OUT=$OUT opencode --standalone 2> $OUT/opencode.log"
  wait until loaded.json exists (poll 0.5 s, max 20 s)
  sleep 2 ; capture 01-home ; sleep 2 ; capture 02-home-later
stage "session":
  kill the tmux session ; start again with `opencode --standalone -s <session id>`
  wait for loaded.json refresh ; sleep 2 ; capture 03-session ; sleep 3 ; capture 04-session-tick
  if the sidebar is not visible at 160 cols: try the sidebar toggle keybind, capture 04b
stage "commands":
  type "/relevo-probe" + Enter ; sleep 1 ; capture 05-page
  send PageDown twice ; capture 06-page-scrolled
  send Escape ; capture 07-back
  type "/relevo-probe-select" + Enter ; capture 08-select ; send Down, Enter ; capture 09-selected
  type "/relevo-probe-prompt" + Enter ; capture 10-prompt ; type "hello probe" + Enter ; capture 11-prompted
  open the command palette ; type "relevo" ; capture 12-palette ; Escape
cleanup (trap on EXIT):
  tmux kill-session -t $SESSION_TMUX (ignore if absent)
  rm -rf $TMP
  never kill any other opencode process, the shared opencode service, or any other tmux session

capture NAME:
  tmux capture-pane -t $SESSION_TMUX -p    > $OUT/NAME.txt
  tmux capture-pane -t $SESSION_TMUX -p -e > $OUT/NAME.ansi
```

`--standalone` is mandatory on every launch: it gives the probe a private
server, so the user's shared OpenCode service (`~/.config/opencode/service.json`)
and their live sessions are untouched. Never type a chat prompt into the
probe's TUI -- only slash commands -- so no model is ever called.

The key sequences above are the intent; if a key does not do what it says
(e.g. Escape does not leave the route), try the obvious alternative once,
record both in the findings, and continue.

## 7. Error Handling

- The plugin never lets an exception escape `tui()`: every registration and
  every probe step is individually caught and recorded in `loaded.json.errors`.
- `run.sh` treats a missing capture as data, not failure: record `NOT TESTED`
  with the reason and continue to the next stage.
- Only §8's halt conditions stop the round.

## 8. Halt conditions -- stop and report, do not improvise

- The probe cannot load a plugin without editing any file under `~/.config/opencode/`.
- `opencode --standalone` will not start inside tmux, or needs a login/auth flow.
- Loading the plugin tries to reach the network and fails (record the error).
- Anything in this plan contradicts what you find (a flag that does not
  exist, a file that is not where it says). Report what you found.

Do not modify any Go file, `Makefile`, `go.mod`, the user's OpenCode
config, or anything outside `docs/specs/2026-09-24-opencode-tui-probe.md`
and `docs/specs/probes/2026-09-24-opencode-tui/`.

## 9. Working Efficiently

Every model step costs a round trip. So:

- Batch independent reads in one step. The files to read are already named:
  `~/.config/opencode/node_modules/@opencode-ai/plugin/dist/tui.d.ts`
  (all 510 lines -- the API), `~/.config/opencode/herdr-tui-session.js`
  (a working, non-rendering plugin), `~/.config/opencode/tui.jsonc`. Do not
  search the repo -- nothing in it is relevant beyond this plan.
- Write `relevo-probe.tsx`, `tui.jsonc`, `run.sh` and `README.md` in one
  step, then run `bash docs/specs/probes/2026-09-24-opencode-tui/run.sh`.
- Iterate on the probe with that one command. Read the `.txt` captures and
  `out/*.json` after each run, fix the plugin, rerun. Budget: if variant A has
  not rendered after three runs, switch to variant B.
- Write the findings doc once, at the end, from the final run's captures.
- Final check, once: `make check` (it must still pass -- this round changes
  no Go code) and `git status --short` (only the two allowed paths may
  appear).

## 10. Ordered Implementation Steps

1. **Read** the three files named in §9 in one batch. *Done when:* you can
   name the `TuiPluginModule` shape and the `registerLayer` command fields.
2. **Write the probe** (`relevo-probe.tsx` implementing R1-R9 + the Q5 raw
   ANSI line, `tui.jsonc`, `run.sh` per §6, `README.md`). *Depends on 1.*
   *Done when:* `bash -n run.sh` passes.
3. **Run and iterate** until every stage of §6 has produced a capture or a
   recorded reason it could not. *Depends on 2.* *Done when:* `out/` holds
   `loaded.json`, `spawn.json` and captures 01-12 (or the findings say why
   one is missing).
4. **Variant B**, only if Q2 variant A failed to render. *Depends on 3.*
   *Done when:* B renders R1 or the findings record why neither can.
5. **Findings doc** `docs/specs/2026-09-24-opencode-tui-probe.md`: Q1-Q12 with
   verdicts and evidence, then "Consequences for the #393 design". *Depends
   on 3-4.* *Done when:* no question lacks a verdict.
6. **Verify** with `make check` and `git status --short`; commit the two
   paths on this binding's branch with message
   `docs(specs): OpenCode 2.0.14 TUI plugin probe (#393)`. *Depends on 5.*

## 11. Report

Your report must include: the verdict line for each of Q1-Q12, the
"Consequences" bullets verbatim, which config-isolation method worked, the
session id used, and `git diff --stat` against the base.
