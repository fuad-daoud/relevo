# relevo inside the OpenCode TUI (#393)

Status: draft for review, 2026-09-24. Mockup: https://claude.ai/artifact/G6ztshdkqx4aVSRzvQ678b.
Evidence: `docs/specs/2026-09-24-opencode-tui-probe.md` and
`docs/specs/probes/2026-09-24-opencode-tui/` (three probe rounds on binding
`oc-tui-probe`, OpenCode 2.0.14).

**Amended 2026-09-28 by `docs/plans/2026-09-27-round-vocabulary.md`:** a
round's input is now its prompt; the current words are `prompt sent` and
`no prompt yet`, and the log kind is `prompt`. The sections below stay as
written.
## 1. Problem

A Claude Code planner sees its builders under the prompt (`relevo status
--line` through Claude Code's `statusLine`), and its session is a relevo planner
from the first turn (the SessionStart hook runs `relevo planner init` and exports
`RELEVO_PLANNER`). An OpenCode planner has neither:

- OpenCode has no status-line setting, so the planner learns that a builder
  reported, stalled or needs a decision only by running `relevo status`.
- Nothing registers an OpenCode session as a planner. `planner.Detect`
  (`internal/planner/ident.go`) recognises Claude and agy only; OpenCode's tool
  shell exports no session id. So the agent's own `relevo bind` fails with "not
  in a detectable planner session" unless it passes `--planner`, and
  `OpencodeDeliverer` (`internal/relevo/deliver_opencode.go`, wired at
  `cmd/relevo/main.go:534`), which already pushes reports into an OpenCode
  planner's chat, never has a planner to deliver to.

## 2. Goals

1. An OpenCode session is a relevo planner without a manual step, and its
   agent's `relevo` commands resolve to it. Reports then reach the chat through
   the existing deliverer, as they do for Claude Code.
2. The planner sees **its own** builders in the session sidebar and gets a toast
   when one needs it.
3. A `/relevo` page, a slice of the relevo cockpit limited to this planner:
   the fleet, and per builder its rounds with plan, report, diff, log and
   transcript.
4. On NEEDS YOU, one dialog lets the user tell the planner agent what to do in
   free text (posted into the chat), with send-plan, stop, done and gate as
   actions beside it.
5. `relevo config agents` installs it; `relevo doctor` checks it.

Non-goals: other planners' bindings (never shown); changing the Claude Code
status line; agy/codex; a relevo-authored plan (the plugin never writes a plan).

## 3. Facts the design rests on (from the probes)

| Fact | Evidence |
|---|---|
| A plugin is a package directory `<config>/plugins/<name>/`; its `exports["./tui"]` module exports `{ id, setup }`. `tui.json`/`tui.jsonc` `plugin` arrays load nothing on 2.0.14. | Q1 |
| One `.tsx` file works with no build step (JSX via `/** @jsxImportSource @opentui/solid */`); dynamic `import()` of host packages fails, static imports work. | Q2 |
| The installed `@opencode-ai/plugin` 1.18.25 types are stale: `api.slots`, `api.route`, `api.state`, `api.lifecycle`, `api.command` do not exist. The live surface is `out/loaded.json` `api_map`. | Q1, Q2 |
| Slots: `api.ui.slot({ append: "sidebar.content", render })`; also `sidebar.footer`, `home.footer`, `home.footer.status`, `prompt.footer.status`, `app`. The sidebar gives **37 columns** and wraps. | Q3 |
| Theme colour via `api.theme.*` + `<b>` renders; raw ANSI prints as garbage. | Q5 |
| `api.storage.store` + `setInterval` re-renders; there is no dispose hook. | Q4 |
| `api.ui.toast.show({ variant, title, message, duration })`; `attention.notify` is off by default. | Q6, Q11 |
| Commands: `api.keymap.layer(...)` called from inside a slot render; command `{ id, title, group, palette, slash: { name }, bind, run }`; palette and slash both work. | Q7 |
| Pages: `api.ui.router.register({ name, render })` + `navigate({ type: "plugin", name })`; the plugin binds its own Escape; a scrollable page needs a `focusable focused` `<scrollbox>`; `<markdown content=…>` and `<code>` render. | Q8 |
| Dialogs are promises: `api.ui.dialog.select/prompt/confirm/alert`; `prompt` takes `value` (pre-fill) and `description`. | Q9, r2 signatures |
| `Bun.spawn` and `node:child_process.execFile` work from the TUI. | Q10 |
| No API sets an unsent prompt draft. | Q13 |
| Leader is `ctrl+x`. `<leader>n` = `session.new`, `<leader>r` = `session.redo`; `d f h j k o p v z` are unused by the default table. A plugin binding on a taken key shadows the builtin. | Q15, binary strings |
| `api.ui.tabs` / `api.ui.panel` are controllers, not components; tabs are built from `<box>`/`<text>`. | Q16 |
| A plugin that fails to load is listed as failed under `/plugins`; the TUI still starts. | Q12 |
| `relevo status --line` takes 0.9 s and `status --json` 2.7 s on the dev laptop (a builder under load measured 20 s). | measured 2026-09-24 |
| A server plugin loads from the same package via `exports["./server"]`, default export `{ id, setup }` (or `{ id, effect }`); one package with `./tui` and `./server` loads both halves. | Q17, Q21 |
| The shell hook is `api.shell.hook("create.before", (spec) => { spec.env.X = "y" })`; it mutates `spec.env` in place. Its input is `{ command, cwd, timeout, shell, env }`: **no session id, no call id**. | Q18 |
| The hook reaches the TUI's `!` shell mode, `api.client.session.shell`, and (by code reading) the agent's bash tool, all through `Shell.create`. | Q19 |
| Time spent in the hook delays the command 1:1; a subprocess in the hook costs ~5 ms. | Q20 |
| OpenCode sets no variable of its own in a tool shell (the round 3 env dump has no `OPENCODE_*` key). | r3 hook calls |

## 4. Architecture

```
~/.config/opencode/plugins/relevo/        written by `relevo config agents` (kind opencode)
  package.json                            exports ./tui and ./server; "version" = relevo version
  tui.tsx                                 sidebar, toasts, /relevo pages, dialogs, commands
  server.ts                               shell hook: marks every shell RELEVO_HARNESS=opencode   [§5.6]

TUI plugin ──spawn──► relevo planner init --kind opencode --session S
            ──spawn──► relevo status --line --json          (poll)
            ──spawn──► relevo history --json --planner S     (fleet feed)
            ──spawn──► relevo show NAME --round N --json [--plan|--report|--diff|--log|--transcript]
            ──spawn──► relevo send / done / stop / gate      (actions)

server plugin ──shell hook──► every shell command gets RELEVO_HARNESS=opencode
agent's shell: relevo bind/send ──► Detect: opencode ──► session = match(cwd, opencode.db) ──► planner   [§5.7]

relevo daemon ──OpencodeDeliverer──► POST /api/session/S/prompt   (reports reach the chat: unchanged)
NEEDS YOU dialog ──api.client.session.prompt──► the user's typed instruction, into the same chat
```

The plugin holds no relevo logic. Every fact and every word of row text comes
from a `relevo` subprocess; the plugin lays it out with the OpenCode theme.

## 5. Components

### 5.1 `relevo status --line --json` (Go)

Today `--line` refuses `--json` (`cmd/relevo/main.go` status flag handling).
Allow the pair; everything else about `--line` is unchanged.

Output, one JSON document:

```
{
  "planner": { "id": "pl_…", "name": "architect-7" },   // null when no planner resolves
  "now": "2026-09-24T20:14:03Z",
  "rows": [ StatusLineRow … ]
}
StatusLineRow {
  name        string   binding name
  round       int
  display     string   ACTIVE | NEEDS YOU   (DONE rows are excluded, as today)
  harness     string   harnessSegment(builder_candidate), "@server" for a remote builder
  candidate   string   builder_candidate
  role        string   omitted for builder
  waiting     string   waiting(b): "plan sent", "report in (halted)", "question in", …
  clock       string   roundClock(b, now): the round's running time, frozen at the report; "--"
  tokens      string   roundTokens(b): "539k tok"; "" if none
  last_kind   string   plan | report | question | answer | ""  (LastPayload.Kind)
  last_ts     string   RFC 3339, "" when none
  route       string   planner_route: deliverer | channel | pull
}
```

Built by a new pure function `StatusLineRows(Report, now) []StatusLineRow` in
`internal/relevo/statusline.go`, sharing `waiting`, `roundClock`, `roundTokens` and `harnessSegment` (as reshaped by #430)
with `RenderStatusLine` so both front ends use the same words. Store-only, like
`PlannerStatus`. Planner resolution is `plannerFilter` unchanged, so it honours
`RELEVO_PLANNER`.

### 5.2 Idle OpenCode records are pruned (Go)

An OpenCode planner record is an explicit registration (no host): one TUI
process shows many sessions, and the registry refuses two records on one live
host (`ErrHostTaken`), so the TUI cannot be the host. `Prune` never forgets an
explicit record. `planner.PruneIdle` does, for kind `opencode` only: a record
unseen for `OpencodeIdleTTL` (7 days) that no binding names. `seen_at` is
refreshed by every Resolve hit, and the TUI plugin resolves every 5 s while it
is open, so a session in use never goes idle. The daemon runs `PruneIdle`
right after `Prune`.

### 5.3 The TUI plugin (`tui.tsx`)

**Session → planner.** On the first `sidebar.content` render for a top-level
session (skip sessions with a parent), spawn
`relevo planner init --kind opencode --session <id>` once per
session per TUI process, parse `export RELEVO_PLANNER=(pl_\w+)`, and cache it.
Every later spawn for that session sets `RELEVO_PLANNER` in its environment.

**Poller.** One module-level poller (guarded so a re-run `setup` never starts a
second: there is no dispose hook). Every 5 s while a relevo slot or page is on
screen, spawn `relevo status --line --json` without awaiting it in the render
path; skip a tick if the previous spawn is still running; after 3 consecutive
failures back off to 30 s. Results go into `api.storage.store`; slots and pages
read the store.

**Sidebar** (`sidebar.content`, append). Header `relevo · <planner name>`, then
two lines per row, each cut to 37 columns with `…`:
`● <name> … <display>` (dot and display in `theme…warning` + bold for NEEDS YOU,
success colour for ACTIVE) and `  r<round> · <harness> · <waiting> · <clock>`.
Last line `/relevo · ctrl+x o`. When the data is older than 3 poll intervals:
`(stale 40s)`. When `relevo` is not on PATH: one line `relevo not found`.

**Badge** (`prompt.footer.status`): `● relevo N need you` when N > 0, else nothing.

**Toasts.** Compare each poll with the previous one, per binding: a row whose
`display` became `NEEDS YOU` → warning toast `<name> needs you` with `waiting`
as the message; a new `last_ts` with `last_kind` `report` → info toast
`<name> r<round> report in` (plus `, delivered to chat` when `route` is
`deliverer`). No toast on the first poll after start.

**Pages** (`ui.router`), each binding Escape to go back:
- `relevo` (fleet): the cockpit's fleet columns without PLANNER/REPO: NAME,
  ACTOR (role or `builder`), ON (model from `candidate`), RND, STATE, NOW
  (`waiting · clock`), TOKENS (`tokens`). Below it, `── recent`: the newest 8
  `relevo history --json --planner <session id> --limit 8` rows. Keys:
  ↑↓, enter (open), a (act), esc (back to chat).
- `relevo/binding` (params: name): header `name · actor on model · round N ·
  state`; tabs plan · report · diff · log · transcript built from `<box>`/`<text>`
  with the selected tab in `theme…interactive`; `[` `]` step rounds (from
  `relevo history --json --binding <name>`); body in a `focusable focused`
  `<scrollbox>`; plan/report through `<markdown content=…>`, diff through
  `<code>`. Each tab body is one `relevo show <name> --round N --json --<tab>`
  spawn, cached per (name, round, tab); the open round's log refreshes with
  `--log --after <seq>` on the poll tick.

**The NEEDS YOU dialog** (`a` on either page, `relevo.next`, or enter on a
NEEDS YOU row). NEEDS YOU means the builder stopped and the next move is the
planner's: a report is ready, the round halted, or the builder asked a question.
The report or question is already in the chat (delivered by relevo), so the
dialog's first job is to let the user tell the planner agent what to do.

```
 <name> needs you · r<round> · <waiting> · <age>
 <the question text, or the report's first line>

 ┌ tell the planner ───────────────────────────────┐
 │ <free text>▌                                     │
 └──────────────────────────────────────────────────┘
   enter sends to this chat as your message
 ─ or act yourself (tab) ─
   Send plan file…   Stop the round   Mark done   Gate the provider…      esc close
```

- **Enter** posts the text into this session as the user's message through
  `api.client.session.prompt`, prefixed with one context line:
  `[relevo · <name> r<round> · <waiting>]`. The agent acts on it (writes and
  sends the next plan, stops, answers). Empty text does nothing.
- **Actions**, for acting without the agent: Tab moves focus from the text
  box to the action row, ←/→ pick, Enter runs. (No ctrl-key shortcuts: inside
  OpenCode `ctrl+x` is the leader and the text box owns ordinary keys.)
  - Send plan file… → `ui.dialog.prompt` (value: the newest
    `docs/plans/*.md` in the binding's repo, if any) → `relevo send --name <name> --file <path>`
  - Stop the round → `ui.dialog.confirm` → `relevo stop --name <name>`
  - Mark done → `ui.dialog.confirm` → `relevo done <name>`
  - Gate the provider… → `ui.dialog.prompt` (reason) → `relevo gate <provider-token> --reason <text>`
- Built with `api.ui.dialog.show` and a custom body holding a text input. The
  probe confirmed `dialog.show` takes a custom render but did not test a text
  input inside it; the implementation plan's first plugin step checks that. If
  it fails, the fallback is a `ui.dialog.select` whose first option, "Tell the
  planner…", opens the confirmed `ui.dialog.prompt`, followed by the four
  actions as options.
- Answering the builder directly is out of scope (§9): the text always goes to
  the planner.

Each action's stdout/stderr first line becomes a toast (success or error),
and the poller runs once immediately after.

**Commands** (one `api.keymap.layer`, registered from an `app` slot render):

| id | title | slash | bind |
|---|---|---|---|
| `relevo.open` | Open relevo | `relevo` | `<leader>o` |
| `relevo.next` | Next builder that needs you | `relevo-next` | `<leader>j` |
| `relevo.refresh` | Refresh relevo | -- | -- |

All in group `relevo`, `palette: true`. `relevo.next` opens the binding page of
the oldest NEEDS YOU row, or toasts `nothing needs you`.

### 5.4 Install (`relevo config agents`, Go)

The package's three files are embedded in the binary next to the agent
definitions (`internal/harness/agents/`, `go:embed`) and installed by
`harness.Install` for kind `opencode` to `~/.config/opencode/plugins/relevo/`,
with the same rules as an agent definition: write when absent; overwrite when
the installed file's sha is in the manifest or `ShippedBefore`; otherwise
report `modified` and leave it, unless `--force`; `--dry-run` writes nothing.
The plugin is opt-in: only `relevo config agents` writes it when absent; once
installed, every install (daemon start, `relevo config init`) refreshes an
unmodified copy and keeps an edited one. It installs under
`~/.config/opencode/plugins/relevo/` like the agent definitions under
`~/.config/opencode/agents/`.

### 5.5 `relevo doctor` (Go)

New rows in the `opencode` group, only when `opencode` is on PATH:
- `plugin` -- OK when the three files are present and match the shipped
  shas; OK `not installed -- relevo config agents installs the OpenCode plugin`;
  WARN `modified` or `from relevo vX (running vY)`.
- `plugin keys` -- WARN when the user's OpenCode config binds `<leader>o` or
  `<leader>j` to anything else (read the `keybinds` of `opencode.jsonc` /
  `cli.json`; unreadable config is not a failure).

### 5.6 The server half (`server.ts`)

Default export `{ id: "relevo", setup }`. In `setup`, register one hook:

```
api.shell.hook("create.before", spec =>
  if spec.env.RELEVO_HARNESS is unset: spec.env.RELEVO_HARNESS = "opencode")
```

It never overrides a variable the user set, never spawns anything, never
awaits, never logs, and never reads the rest of `spec.env` (round 3's probe
logged the whole environment and committed live credentials; the shipped hook
touches one key). The hook marks the shell; relevo works out the session.

### 5.7 Planner resolution in an OpenCode shell (Go)

**Detect** (`internal/planner/ident.go`), after the Claude and agy checks:
`RELEVO_HARNESS == "opencode"` gives `Ident{Kind: "opencode", SessionID: "",
HostPID: 0}`. Detect stays pure; the session id is filled in by Resolve.

**Resolve** (`internal/planner/resolve.go`): `ResolveInput` gains
`OpencodeSession func(cwd string, now time.Time) (string, error)`. When the
detected kind is `opencode` and its SessionID is empty, Resolve calls it and
continues with the session step (`BySession("opencode", id)`) as for any other
kind. The order stays: `--planner` flag, `RELEVO_PLANNER`, host, session.

**Session match** (new pure function, `internal/planner/opencode_match.go`):

```
MatchOpencodeSession(cwd string, sessions []OpencodeSession, now time.Time) (id string, err error)
OpencodeSession { ID, Directory, ParentID, Title string; Updated time.Time; Archived bool }
```

1. Keep top-level (`ParentID == ""`), unarchived sessions whose `Directory` is
   `cwd` or an ancestor of it; keep only those with the **longest** matching
   directory.
2. None → `ErrNoOpencodeSession`.
3. Sort by `Updated`, newest first. If the second newest was updated within 60 s
   of `now` as well as the newest → `ErrAmbiguousOpencodeSession` naming both
   titles ("two OpenCode sessions are active in <dir>: <a>, <b>; pass --planner").
4. Otherwise the newest.

The caller reads `sessions` from `$XDG_DATA_HOME/opencode/opencode.db`
(`select id, directory, parent_id, title, time_updated, time_archived from
session`) through the same sqlite3 shell-out `OpencodeDeliverer` uses
(`usage.Exec`). A read failure is `ErrNoOpencodeSession` with the reason, never
a guess.

**Unregistered session.** When the match succeeds but no planner record names
the session (the TUI has not opened it, e.g. a headless `opencode run`
planner), the verbs that need a planner (`bind`, `add`, `fork`, `ask`, through
`resolveVerbPlanner` in `internal/relevo/bind.go`) register it on the spot,
as `relevo planner init --kind opencode --session <id>` does today: a hostless,
explicit record. Read-only commands (`status`) do not register.

## 6. Data flow: a report lands

1. The builder's round closes; `relevo daemon` delivers the report to the
   planner session through `OpencodeDeliverer` (unchanged).
2. Within 5 s the TUI poller sees `last_kind: report` with a new `last_ts` and
   `route: deliverer`; it toasts `ledger r3 report in, delivered to chat`, and
   the sidebar row turns NEEDS YOU if the status does.
3. The agent reads the report in the chat and writes the next plan; or the user
   opens `/relevo`, reads the report tab, and sends, marks done or stops from the
   action dialog.

## 7. Error handling

- The plugin never throws out of `setup` or a render; every spawn and API call
  is caught. A failure shows in the plugin's own UI (the sidebar's `stale` or
  `not found` line, an error toast for an action), never as a crash.
- A broken plugin is contained by OpenCode itself (listed as failed, Q12).
- A spawn that exceeds 10 s is killed; the poll counts it as a failure.
- `planner init` failing leaves the session unregistered; the sidebar shows
  `relevo: not a planner (see relevo doctor)` and retries on the next session
  switch.

## 8. Testing

- Go, pure functions in `internal/relevo` and `internal/planner` (CI has no
  harness binary and no network, so no `cmd/relevo` test spawns one):
  `StatusLineRows` table test; `PruneIdle` table test;
  `MatchOpencodeSession` (exact dir, ancestor, longest-match, child sessions
  ignored, archived ignored, the 60 s ambiguity, none); Detect with
  `RELEVO_HARNESS=opencode` below `CLAUDECODE` and agy; Resolve calling
  `OpencodeSession` only for that kind;
  install of the plugin files under `harness.Install` (absent / shipped /
  modified / force / dry-run); doctor rows.
- `cmd/relevo`: flag-parsing tests only (`--line --json` accepted; `--line
  --name` still refused).
- The plugin: the probe driver (`docs/specs/probes/2026-09-24-opencode-tui/run.sh`)
  becomes `scripts/opencode-plugin-smoke.sh`: a private `--standalone` OpenCode
  under tmux with `OPENCODE_CONFIG_DIR`, a throwaway session, captures of the
  sidebar, fleet, binding page and action dialog. Run by hand on a machine with
  OpenCode, not in CI. The script records only named variables, never a whole
  environment.

## 9. Out of scope

- Thoughts in the transcript tab: `opencode run --format json` emits no
  reasoning parts (they live in `opencode.db`), Claude's stream carries thinking
  blocks, codex has reasoning items; `relevo show --transcript` drops all of
  them today. Separate issue.
- The artifacts tab and answering a builder's question: both come from the
  cockpit work (nothing writes the `question` kind yet).
- Desktop notifications (`attention` is off by default in OpenCode).
- Making `relevo status --line` itself faster than 0.9 s.
