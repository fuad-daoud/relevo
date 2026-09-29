# Round B1: the relevo OpenCode plugin package and its smoke test (#393)

Binding `oc-tui-a`, on top of commit `51b42c84` (round A: OpenCode sessions
resolve and register as planners; `relevo status --line --json`).
Spec: `docs/specs/2026-09-24-opencode-tui-plugin-design.md` §4, §5.3, §5.6 (in
your tree). Probe evidence and a working reference plugin:
`docs/specs/2026-09-24-opencode-tui-probe.md` and
`docs/specs/probes/2026-09-24-opencode-tui/` (`relevo-probe.tsx`,
`relevo-probe-server.ts`, `run.sh`).

**If a step is impossible as written or contradicts what you find, stop and
report. Do not improvise around it.** API-shape surprises (a name that differs
from this plan) are not a stop: use what the probe proved, and record the
difference in the report.

**Hard stops (halt immediately, report):**
- anything would start a model turn in any OpenCode session (never press Enter
  with text in the chat prompt or in the plugin's "tell the planner" box during
  the smoke run);
- anything would change `~/.config/opencode/`, the shared OpenCode service, or
  real relevo state (`~/.local/state/relevo`, relevo.db);
- anything would write a whole environment to a file or log.

B2 (next round) embeds this package in the relevo binary, installs it through
`relevo config agents`, and adds `relevo doctor` rows. This round does not touch
Go code.

## 1. System Overview

A plugin package that OpenCode 2.0.14 loads from
`<config>/plugins/relevo/`: a TUI half (sidebar rows, need-you badge, toasts,
`/relevo` fleet and binding pages, the NEEDS YOU dialog, palette/slash/key
commands) and a server half (a shell hook that marks every shell command
`RELEVO_HARNESS=opencode`). All relevo facts come from `relevo` subprocesses;
the plugin only lays them out. A smoke script loads the package into a private
`--standalone` OpenCode under tmux, with a **fake `relevo`** first on PATH that
serves fixtures and logs every call, and asserts on screen captures.

## 2. File Structure

```
internal/harness/opencodeplugin/          (B2 embeds this directory; no Go file here in B1)
  package.json                            name "relevo", type module, exports ./tui and ./server, version "0.0.0-dev"
  tui.tsx                                 the TUI half (§4.1)
  server.ts                               the server half (§4.2)
scripts/opencode-plugin-smoke.sh          the smoke driver (§5)
scripts/testdata/opencode-plugin/
  relevo                                  fake relevo (bash, executable) (§5.1)
  status-1.json, status-2.json            fixtures for `status --line --json` (first polls / later polls)
  history.json                            fixture for `history --json …`
  show-report.json, show-plan.json, show-diff.json, show-log.json, show-transcript.json
```

`package.json` `exports` uses exactly the shape round 1's `run.sh` wrote for
the probe package (read `run.sh` ~l.80-100), with `"./server"` added the way
round 3 did.

## 3. Data the plugin consumes (produced by round A and existing verbs)

- `relevo status --line --json` → `{ planner: {id,name}|null, now, rows: [
  {name, round, display, harness, candidate, role?, waiting, clock, tokens,
  last_kind, last_ts, route} ] }` (`internal/relevo/statusline.go`
  `StatusLineDoc`).
- `relevo planner init --kind opencode --session <ses_…>` → last stdout line
  `export RELEVO_PLANNER=<pl_…>`.
- `relevo history --json --binding <name> --limit 20` and
  `relevo history --json --planner <ses_…> --limit 8` → JSON array of RoundRow
  (keys include `BindingName`, `Number`, `StartedAt`, `ClosedAt`, `Outcome`,
  `BuilderCandidate`, `ReportOutcome`).
- `relevo show <name> --round <n> --json --plan|--report|--diff|--log|--transcript`
  → object with keys `Name, Round, Rounds, Section, Text, Events, Live, Missing,
  Archived, ArchivedAt`; the tab body is `Text`.
- Actions: `relevo send --name <n> --file <p>`, `relevo stop --name <n>`,
  `relevo done <n>`, `relevo gate <candidate> --reason <text>`.

Every spawn: `Bun.spawn` (fallback `node:child_process.execFile`), argv array
(never a shell string), env `{...process.env, RELEVO_PLANNER: <id>}` once the id
is known, 10 s timeout then kill, never awaited inside a render.

## 4. Component Contracts

### 4.1 `tui.tsx`

First line `/** @jsxImportSource @opentui/solid */`; static imports only
(dynamic `import()` of host packages fails, probe Q2). Default export
`{ id: "relevo", setup }`.

**Module state** (module-level, so a second `setup` call reuses it; there is no
dispose hook): `started` flag, `plannerBySession: Map<ses, {id,name}|"pending"|"error">`,
`doc` (last StatusLineDoc) + `docAt` (ms), `prevRows` (by name), `failures`,
`inFlight` flag, `timer`. Reactive state for renders through
`api.storage.store("relevo", { initial: {...} })` (probe R1 shape: returns
`[get, set]`).

**Session → planner.** On a `sidebar.content` render for session S: if the
session has a parent (sub-agent; check `api.data.session.get(S)` for a parent
id field -- read round 1's `out/loaded.json` api_map or the probe's H-code for
the field name) skip; if `plannerBySession` has no entry for S, mark "pending"
and spawn `relevo planner init --kind opencode --session S`; parse
`export RELEVO_PLANNER=(pl_[a-z0-9]+)`; store `{id, name}` (name from the first
line `planner <name> (<id>) …`), else "error".

**Poller.** Start once (`started`). Every 5000 ms: if a relevo slot rendered in
the last 10 s or a relevo route is current, and not `inFlight`, spawn
`relevo status --line --json` with the current session's planner env; on
success store `doc`, `docAt`, reset `failures`, run toasts; on failure
`failures++`; after 3 consecutive failures the interval becomes 30 000 ms until
the next success.

**Sidebar** (`api.ui.slot({ append: "sidebar.content", render })`), width 37
columns, every line cut to 37 with `…`:
```
relevo · <planner name>                    (bold "relevo", muted rest)
● <name>                          NEEDS YOU   (● and word: warning colour + bold)
  r<round> · <harness> · <waiting> · <clock>   (muted)
○ <name>                             ACTIVE   (○ muted; word: success colour)
  …
/relevo · ctrl+x o                         (muted)
(stale 40s)                                (only when now-docAt > 15 s)
```
Planner "pending" → `relevo · registering…`; "error" or `doc.planner` null →
`relevo: not a planner (see relevo doctor)`; spawn ENOENT → `relevo not found`.
Colours via `api.theme.*` tokens (probe Q5 used
`api.theme.text.feedback.warning.base`; find the success/muted equivalents the
same way). Never raw ANSI.

**Badge** (`api.ui.slot({ append: "prompt.footer.status", render })`):
`● relevo N need you` (warning colour) when N > 0 rows have display NEEDS YOU;
nothing otherwise.

**Toasts** (`api.ui.toast.show`), comparing each successful poll with
`prevRows` (skipped on the first successful poll after start):
- display changed to `NEEDS YOU` → `{variant:"warning", title:"relevo", message:"<name> needs you: <waiting>", duration: 8000}`
- `last_ts` changed and `last_kind == "report"` → `{variant:"info", title:"relevo", message:"<name> r<round> report in" + (route=="deliverer" ? ", delivered to chat" : ""), duration: 6000}`

**Commands** (one `api.keymap.layer`, registered from inside an
`api.ui.slot({ append: "app", render })` render, exactly as probe R4 does;
calling it from `setup` throws):
| id | title | slash | bind | run |
|---|---|---|---|---|
| `relevo.open` | Open relevo | `relevo` | `<leader>o` | navigate to route `relevo` |
| `relevo.next` | Next builder that needs you | `relevo-next` | `<leader>j` | open route `relevo.binding` for the NEEDS YOU row with the oldest `last_ts`; none → toast "nothing needs you" |
| `relevo.refresh` | Refresh relevo | -- | -- | poll now |
| `relevo.back` | (hidden) | -- | `escape` | only while a relevo route is current: back to the previous session route (probe R4's escape command) |
All `group: "relevo"`, `palette: true` except `relevo.back`.

**Routes** (`api.ui.router.register`; navigate with
`api.ui.router.navigate({ type: "plugin", name })`):
- `relevo` (fleet): header `relevo › fleet` + `● N need you · planner <name>`;
  a line `<n> bindings · <k> need you · this planner only`; column header
  `NAME ACTOR ON RND STATE NOW TOKENS` (ACTOR = role or "builder"; ON = the
  model part of `candidate`, i.e. text after the second `/`, `#…` dropped;
  NOW = `waiting · clock`); one row per doc row, selectable (↑/↓, enter opens
  the binding page, `a` opens the NEEDS YOU dialog for the selected row);
  then `── recent` with up to 8 rows from `history --json --planner <S> --limit 8`
  formatted `HH:MM  <BindingName>  r<Number>  <Outcome>`.
- `relevo.binding` (params `{name, round?}`): header `relevo › fleet › <name> › r<round>`;
  a line `<name> · <actor> on <model> · <display>`; a tab row
  `plan  report  diff  log  transcript` built from `<box>`/`<text>` (selected
  tab in the theme's interactive/primary colour; `tab`/`shift+tab` switch);
  a round row `[ ] round  r1 r2 …` from `history --json --binding <name>`
  (`[`/`]` step); the body `Text` of `show <name> --round <n> --json --<tab>` in a
  `<scrollbox focusable focused>`; plan and report through
  `<markdown content={Text}>`, diff through `<code>` (probe Q8: both exist;
  `markdown` needs the `content` prop, never a text child). Cache bodies per
  (name, round, tab); refresh the open round's `log` tab on each poll.
  `a` opens the NEEDS YOU dialog for this binding.

**The NEEDS YOU dialog** (`api.ui.dialog.show(render)` with a custom body):
```
<name> needs you · r<round> · <waiting> · <clock>                  esc close
<first non-empty line of show --report --json Text, if the round has a report>

tell the planner
┌──────────────────────────────────────────────────────┐
│ <text input>                                          │
└──────────────────────────────────────────────────────┘
enter sends to this chat as your message, starting [relevo · <name> r<round> · <waiting>]

or act yourself (tab)
Send plan file…   Stop the round   Mark done   Gate the provider…
```
- Enter in the text box, non-empty text → `api.client.session.prompt` for the
  current session with the text `"[relevo · <name> r<round> · <waiting>]\n" + text`
  (read the call's argument shape from round 2's `out/r2-signatures.json`
  `client.session.prompt` src), then close the dialog and toast "sent to the
  planner". **The smoke run never triggers this path.**
- Tab moves focus to the action row; ←/→ select; Enter runs:
  - Send plan file… → `api.ui.dialog.prompt({title:"Send plan to <name>", placeholder:"path to the plan file"})` → `relevo send --name <name> --file <path>`
  - Stop the round → `api.ui.dialog.confirm({title:"Stop <name> round <n>?", message:"The builder is killed; the worktree and branch are kept."})` → `relevo stop --name <name>`
  - Mark done → `api.ui.dialog.confirm({title:"Mark <name> done?", …})` → `relevo done <name>`
  - Gate the provider… → `api.ui.dialog.prompt({title:"Gate <candidate>", placeholder:"reason"})` → `relevo gate <candidate> --reason <reason>`
  Each: toast the first line of stdout (success) or stderr (error), then poll now.
- **Fallback** if a text input does not work inside `dialog.show` (verify in
  step 3 before building the rest): `api.ui.dialog.select` titled
  `<name> needs you` with options `Tell the planner…` (→ `api.ui.dialog.prompt`,
  then the same post), and the four actions. Record which one shipped.

### 4.2 `server.ts`

Default export `{ id: "relevo-server", setup }` (a TUI and a server module in one
package must not share an id if OpenCode rejects duplicates -- probe Q21 loaded
both; use the ids the probe used as the pattern). In `setup`:
`api.shell.hook("create.before", spec => { if (spec?.env && spec.env.RELEVO_HARNESS === undefined) spec.env.RELEVO_HARNESS = "opencode" })`.
Nothing else: no spawn, no await, no logging, no other key read.

## 5. The smoke test

### 5.1 Fake `relevo` (`scripts/testdata/opencode-plugin/relevo`)

Bash. Appends one line per call to `$RELEVO_FAKE_LOG`: the argv joined by
spaces (never the environment). Serves:
- `planner init --kind opencode --session S` → prints
  `planner oc-smoke (pl_smoke000001) created` and `export RELEVO_PLANNER=pl_smoke000001`
- `status --line --json` → `status-1.json` for the first 2 calls, then
  `status-2.json` (count calls in `$RELEVO_FAKE_STATE/status.count`)
- `history --json …` → `history.json`
- `show <name> … --json --<tab>` → `show-<tab>.json` (`--report` when no tab flag)
- `send|stop|done|gate …` → prints `ok (fake)` and exits 0
- anything else → exit 2 with `fake relevo: unhandled: <argv>` on stderr

Fixtures (write them to match §3's shapes exactly; generate the keys by
reading one real `relevo status --line --json` / `history --json --limit 1` /
`show <any> --json --report` output, then write synthetic values -- never copy
real output into the repo):
- `status-1.json`: planner `{id: "pl_smoke000001", name: "oc-smoke"}`; rows
  `webshop` (NEEDS YOU, waiting "question in", last_kind "question"),
  `ledger` (ACTIVE, waiting "plan sent", last_kind "plan"),
  `landing` (ACTIVE, role "designer").
- `status-2.json`: same, but `ledger` is NEEDS YOU, waiting "report in",
  last_kind "report", a newer last_ts, route "deliverer" -- the smoke expects
  the report toast from this change.

### 5.2 Driver (`scripts/opencode-plugin-smoke.sh`)

Reuse `run.sh`'s machinery (tmux session name `relevo-plugin-smoke`, 160×45,
`OPENCODE_CONFIG_DIR` isolation, `--standalone`, capture helper, `C-u` before
typing, cleanup trap that kills only its own tmux session). Differences:
- `OPENCODE_CONFIG_DIR=$TMP/config`; copy (not symlink) `internal/harness/opencodeplugin/`
  to `$TMP/config/plugins/relevo/`.
- `PATH=$REPO/scripts/testdata/opencode-plugin:$PATH`, `RELEVO_FAKE_LOG`,
  `RELEVO_FAKE_STATE`, and `XDG_STATE_HOME=$TMP/state` (so even a real relevo
  that slipped through touches nothing real) in the TUI's launch environment.
- Session: the newest top-level session from
  `sqlite3 -readonly ~/.local/share/opencode/opencode.db` (as round 1 did);
  opened with `-s`, read-only. Nothing is typed into its chat prompt.
- Captures to `$OUT` (default `$TMP/out`, printed at the end; nothing is
  written into the repo): `01-session`, `02-after-toast` (≥ 12 s later, after
  the 3rd poll), `03-fleet` (`C-x o`), `04-binding` (enter on `webshop`),
  `05-binding-report` (tab to report), `06-dialog` (`a`), `07-dialog-actions`
  (tab), `08-confirm-done` (→ to Mark done, enter), then enter on confirm,
  `09-after-done`, `10-palette` (`ctrl+p`, type `relevo`).
- Assertions (grep on the plain captures and the fake log; print PASS/FAIL per
  line, exit non-zero if any FAIL):
  1. log has `planner init --kind opencode --session <that session id>`
  2. `01-session` shows `relevo · oc-smoke`, `webshop`, `NEEDS YOU`
  3. `01-session` shows `relevo 1 need you`
  4. `02-after-toast` shows `ledger r` … `report in, delivered to chat` (the toast) and `relevo 2 need you`
  5. `03-fleet` shows `relevo › fleet` and the header `NAME` … `TOKENS`
  6. `04-binding` shows `relevo › fleet › webshop` and the tab row
  7. log has `show webshop` … `--report` after step 05
  8. `06-dialog` shows `tell the planner` (or, with the fallback, `Tell the planner…`)
  9. log has `done webshop` after step 08
  10. `10-palette` lists `Open relevo`
  11. no line in the log starts with anything but a relevo verb (sanity)

## 6. Working Efficiently

- One batch of reads: this plan, the spec sections named above, the probe's
  `relevo-probe.tsx` (all of it: it is the API reference), `relevo-probe-server.ts`,
  `run.sh`, `out/loaded.json` (api_map only), `out/r2-signatures.json`.
- Write `package.json`, `server.ts`, the fake relevo and fixtures in one step;
  then `tui.tsx`; then the smoke driver.
- Iterate with `bash scripts/opencode-plugin-smoke.sh`; read the captures and
  the fake log after each run. Budget 8 runs; if the text input in `dialog.show`
  has not worked after 3 runs, ship the fallback.
- Final check: `make check` must still pass (no Go change is expected; if the
  laptop blocks it, run `go vet ./...` and `go test ./...`). `git status --short`
  lists only §2's paths.

## 7. Ordered Steps

1. **Read** (§6). *Done when* you can name the probe's slot, keymap.layer,
   router, dialog, toast and storage call shapes.
2. **Package skeleton + server half + fake relevo + fixtures.** *Done when*
   `bash -n` passes on the scripts and `scripts/testdata/opencode-plugin/relevo status --line --json`
   prints `status-1.json`.
3. **Dialog text-input check first**: a minimal `tui.tsx` that registers the
   commands and opens a `dialog.show` with a text input from `relevo.open`;
   one smoke run with only that capture. *Done when* you know whether the text
   input works; record it.
4. **The full `tui.tsx`** (§4.1). *Done when* the smoke's assertions 1-3 pass.
5. **Smoke driver complete** (§5.2), iterate until all 11 assertions pass.
6. **Verify and commit**:
   `feat(opencode): relevo plugin package for OpenCode 2.0.14 -- sidebar, fleet, NEEDS YOU dialog, shell marker; smoke script (#393)`.

## Report

Per step status; which dialog variant shipped; the smoke script's PASS/FAIL
output verbatim; every API name that differed from this plan; `git diff --stat HEAD~1`.
