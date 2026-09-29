# OpenCode 2.0.14 TUI plugin probe (#393)

Round-1 spike answer to issue #393: can a relevo TUI plugin render in the
installed OpenCode, and what exactly can it do? Everything below is measured
from a throwaway probe plugin running in a private `opencode --standalone`
TUI (tmux 158x46, session `ses_f6e5046f0ffeBTguUw5PDdljfv`), driven by
`docs/specs/probes/2026-09-24-opencode-tui/run.sh`. Evidence lives in
`docs/specs/probes/2026-09-24-opencode-tui/out/`.

**Config isolation: `OPENCODE_CONFIG_DIR=<probe dir>` worked on the first
attempt** (`XDG_CONFIG_HOME` fallback never needed). The probe's config
directory holds a symlink to the user's `opencode.jsonc` (providers and
permissions, read-only use), the probe's own `cli.json`, and the probe plugin.

## 0. Correction to the plan: how 2.0.14 loads a file TUI plugin

The plan's §1 premise -- "file plugins are listed in `tui.json`/`tui.jsonc`
`"plugin": [...]`" -- is **wrong for the installed 2.0.14**. Measured facts:

* The binary contains the string `tui.json` (3x) and **never** `tui.jsonc`.
* `tui.json` is read exactly once, by the CLI-config migration:
  `message="migrated cli config" from="[\"<config>/tui.json\",\"<state>/kv.json\"]" to=<config>/cli.json`.
  Its `"plugin"` array is copied into `cli.json` as **`"plugins"`**.
* `cli.json`'s `plugins` array *is* shown by `opencode plugin list` (the probe
  appeared there as `local`), but the TUI never loads from it at runtime --
  verified: with the list populated, the TUI's plugin reconciliation still
  counted only the 12 builtins and never called the plugin.
* What actually loads: a **package directory under `<config>/plugins/<name>/`**
  whose TUI entrypoint (Node resolution of `exports["./tui"]`, or a bare
  `<dir>/tui.js|tui.ts|tui.tsx`) default-exports **`{ id, setup }`**:

  ```tsx
  /** @jsxImportSource @opentui/solid */
  export default {
    id: "relevo.probe",
    setup: async (api) => { api.ui.slot({ append: "sidebar.content", render: (props) => <text>hi</text> }); },
  };
  ```

  `run.sh` installs `relevo-probe.tsx` as
  `<config>/plugins/relevo-probe/tui.tsx` with
  `{"name":"relevo-probe","exports":{"./tui":"./tui.tsx"}}`.
* A plain file dropped in `plugins/` is treated as a **server** plugin and
  rejected: `Plugin must export a default definition with an id and an effect or
  setup function.`
* A package whose module exports the v1 shape `{id, tui}` is rejected with
  `Invalid V2 TUI plugin module: <path>` (the validator requires `id:string`
  plus `setup:function`).

`setup(api)` receives an api that shares almost nothing with the installed
`@opencode-ai/plugin` 1.18.25 types:

```
api:    app attention client data keymap location markdown options renderer storage theme themeMode ui
api.ui: dialog format panel router slot tabs toast
```

`loaded.json.api_map` (out/loaded.json) is the full depth-3 dump of that api,
including each method's arity and minified source. That dump is the reference
for everything below.

## Q1 load -- PARTIAL

A file TUI plugin does load on 2.0.14, but **not** from `tui.jsonc`, which is
never read. Verdict **PARTIAL** because the plan's documented mechanism does not
exist; the real mechanism does work.

* `api.app.version` = `"2.0.14"` (also `api.app.channel` = `"latest"`).
* `api.command` (legacy v1) is **gone**: `loaded.json.v1_api_present` is
  `false` for `command`, `slots`, `route`, `state`, `lifecycle`, `plugins`,
  `tuiConfig`, `kv`, `event`, `mode`, `keys`. `api.command` is not even a key.
* Evidence: `out/loaded.json`, `out/01-home.txt` (shows `2.0.14` in the footer),
  `out/13-plugins.txt`, `run.sh`.

## Q2 authoring -- WORKS (variant A)

* Variant A renders. A `.tsx` file with `/** @jsxImportSource @opentui/solid */`
  is transpiled at load; JSX intrinsics `<box>`, `<text>`, `<b>`, `<scrollbox>`,
  `<markdown>`, `<code>` all work.
* Variant B (`.js`, no JSX) was **not written**: the plan says only if A fails,
  and A does not fail. (`run.sh` accepts `PROBE_VARIANT=relevo-probe.js`.)
* `<markdown>` and `<code>` take **props, not children**. A raw string child
  throws (verbatim):
  `Orphan text error: "# heading\n\n**bold** and `code`" must have a <text> as a parent: markdown-1 above text-node-679`
  Working form: `<markdown content={"# heading"} width="100%" />` and
  `<code content={"const x = 1;"} filetype="ts" width="100%" />`
  (see `out/05-page.txt`, `out/12-palette.txt`, and `loaded.json.errors` in an
  earlier iteration).
* Dynamic `import()` of `@opentui/solid`, `solid-js`, `@opentui/core`,
  `@opencode-ai/plugin/tui`, `@opencode/plugin/tui` all fail with
  `ResolveMessage: Cannot find package '<spec>' imported from <plugin file>`.
  Imports only work because the plugin loader installs OpenTUI Solid runtime
  plugin support and **statically rewrites** those specifiers -- so plugin code
  must use static imports / the JSX pragma, never `await import()`.
* No build step: ship the `.tsx`.

## Q3 slots -- PARTIAL

* There is no `sidebar_content` / `home_bottom` slot **name**. Slots are dotted
  **paths** claimed with exactly one placement key
  (`prepend|append|before|after|replace`):
  `api.ui.slot({ append: "sidebar.content", render: (props) => <box>...</box> })`.
  Registering with a name (as the v1 SDK does) is impossible: the only API is
  this claim call, and the empty placement key set throws
  `Slot claim requires exactly one placement key`.
* Slot paths present in 2.0.14 (strings in the binary):
  `sidebar.content`, `sidebar.footer`, `home.footer`, `home.footer.status`,
  `prompt.footer.status`, `app`. There is **no** `sidebar.title`,
  `home_bottom`/`home.bottom`, `app_bottom`, `home_prompt*` or `session_prompt*`.
  The closest thing to R2 is `home.footer`; it renders (`relevo-probe home`,
  `out/01-home.txt` line 45).
* Sidebar geometry at a 158-column terminal: the sidebar box occupies columns
  ~120-158 with 2-column padding, so the **usable text width is 37 columns**.
  A 60-character line **wraps** (no truncation, no clip): the probe's line of 60
  `x` occupies 37 columns on one row and 23 on the next
  (`out/04-session-tick.txt` rows 13-14: `n_x=37` then `n_x=23`). The
  `relevo-probe sidebar <session id>` line wraps the same way.
* Evidence: `out/03-session.txt`, `out/04-session-tick.txt`, `out/loaded.json`
  (`attempts` shows the successful claims and the v1 attempt
  `api.slots is undefined`).

## Q4 reactivity -- WORKS

The tick advances with no user input: `tick 200` in `out/03-session.txt` and
`tick 203` in `out/04-session-tick.txt` (3 s apart), and it kept climbing
(`tick 162/165` in an earlier run). The signal used was
`api.storage.store("tick", { initial: { n: 0 } })` mutated by a 1-second
`setInterval` -- a v2 storage store is reactive and re-renders the slot.

Caveat: v2 exposes **no dispose hook** (`api.lifecycle` does not exist;
`loaded.json.tick_dispose_hook` = `"none exposed by the v2 api"`), so the
interval leaks until the TUI exits. `api.attention.dispose()` is the only
dispose-like method in the api.

## Q5 styling -- PARTIAL

* Theme colour and bold render. `out/03-session.ansi` contains, for the
  `NEEDS YOU` line, `^[[1m^[[38;2;245;167;66mNEEDS YOU` -- bold plus an RGB
  colour taken from `api.theme.text.feedback.warning.base`. So the v2 token is
  `api.theme.text.feedback.warning.base` (an RGBA object), not the v1
  `theme.current.warning`.
* Raw ANSI inside `<text>` renders as **garbage, not colour**:
  `"\x1b[33mraw-ansi\x1b[0m"` appears in the capture as the literal text
  `[33mraw-ansi [` (the ESC byte is dropped). Evidence: `out/03-session.txt`
  row 15 and `out/03-session.ansi` (`   [33mraw-ansi`). A plugin cannot emit
  escape sequences; it must use theme tokens.

## Q6 toast -- WORKS, with a boot-timing caveat

* `api.ui.toast.show({ variant, title, message, duration })` works
  (`loaded.json.attempts`). It renders as a box in the **top-right, above the
  sidebar**.
* R3's load-time toast with `duration: 10000` is **not visible in the home
  captures**: the TUI spends more than 10 s behind its `… Loading plugins…`
  splash while plugins resolve, and the toast expires on that screen
  (`out/01-home.txt`, `out/02-home-later.txt` show no toast of ours).
* A toast fired when the sidebar first renders *is* captured: title
  `relevo-probe`, message `toast from sidebar`, visible in `out/03-session.txt`,
  `out/04-session-tick.txt` and `out/05-page.txt`, and gone by
  `out/06b-page-arrow.txt` -- i.e. visible for the configured 10 s
  (03 -> 05 spans ~9 s, 06b is ~2 s later). `loaded.json.sidebar_toast_fired`
  records that the second toast fired.
* Also visible on the home screen: the *failure* toast
  `Plugin failed: <target>` with an `Open plugins` action
  (`out/01-home.txt` line 45, `out/02-home-later.txt`).

## Q7 commands -- WORKS

* All three commands run: `/relevo-probe` opens the page (`out/05-page.txt`),
  `/relevo-probe-select` and `/relevo-probe-prompt` open their dialogs.
* The palette lists all three under the group `relevo`
  (`out/12-palette.txt`):
  `relevo-probe page  open the relevo probe page  relevo`, etc.
* Keybind check: the probe's `cli.json` sets no `keybinds.command_list`, so the
  **default `ctrl+p`** applies (not the user's global `ctrl+k`); `ctrl+p` opened
  the palette in every run. `ctrl+k` was not tested.
* Operational detail: a slash command typed in the main prompt needs **Enter
  twice** (the first Enter selects the autocomplete entry, the second runs it);
  the same command run from the palette needs **Enter once**.

## Q8 routes -- WORKS

* `api.ui.router.register({ name: "relevo-probe", render: (props) => <box>... })`
  plus `api.ui.router.navigate({ type: "plugin", name: "relevo-probe" })` shows a
  full-screen page with the header `relevo-probe page` (`out/05-page.txt`).
* Scrolling works **only once the scrollbox is focusable**: with
  `<scrollbox height={20} focusable focused>` PageDown scrolled to
  `line 021` (`out/06-page-scrolled.txt`) and three Down-arrows to `line 033`
  (`out/06b-page-arrow.txt`). Without those props (first iteration) PageDown did
  nothing -- the plugin route owns no message-list keybindings.
* Getting back: **Escape alone does not leave a plugin route.** It works as soon
  as the plugin registers its own command with `bind: "escape"`
  (`out/07-back.txt` shows the session route). `q` was not needed.
* `<markdown>` and `<code>` both exist (see Q2); they render on the page
  (`out/05-page.txt` line 22 `relevo-probe extras`).

## Q9 dialogs -- WORKS

* `api.ui.dialog.select({ title, placeholder, options: [{title, value}] })`
  returns a promise resolving the chosen value; after Down + Enter
  `out/select.json` holds `{"selected":"beta"}` and `out/08-select.txt` shows
  the dialog with `alpha`/`beta`/`gamma`.
* `api.ui.dialog.prompt({ title, placeholder })` returns a promise resolving
  the typed string; `out/prompt.json` holds `{"value":"hello probe"}` and
  `out/10-prompt.txt` shows the dialog with its `type here` placeholder.
* `api.ui.dialog.alert` and `api.ui.dialog.confirm` exist too (same
  promise shape). The v1 `api.ui.dialog.replace` is gone; the low-level call is
  `api.ui.dialog.show(render, onClose)`.

## Q10 subprocess -- WORKS, but the command is too slow to use live

Both methods work from inside the TUI process (`out/spawn.json`):

| method | argv | result |
|--------|------|--------|
| `Bun.spawn` | `relevo version` | ok, exit 0, **12 ms**, `relevo v0.13.0-6-g559dcf9` |
| `Bun.spawn` | `relevo status --json` | killed at the 5 s cap, exit 143, **5001 ms** |
| `node:child_process.execFile` | `relevo version` | ok, exit 0, **5 ms** |
| `node:child_process.execFile` | `relevo status --json` | timeout at 5002 ms (`Error: Command failed: relevo status --json`) |

Measured outside the TUI, `relevo status --json` takes **19.9 s** wall clock, so
neither method can return it within 5 s; a plugin must cache it or use a faster
command.

`RELEVO_PLANNER` **is** set in the TUI process (`loaded.json.env.RELEVO_PLANNER`
= `"pl_n2ggkahruttr"`) -- contrary to the plan's expectation. The probe inherited
it from the launching shell via tmux, so this says nothing about a normal user
launch; it does mean a plugin cannot detect "launched by relevo" by its absence.
Also recorded: `OPENCODE_TERMINAL=1`, `OPENCODE_CLIENT=null`,
`XDG_CONFIG_HOME=/home/fuad/.config`, runtime `bun 1.4.2` / `node 26.3.0`.

Spawning at load must be **fire and forget**: awaiting the subprocess probe kept
the TUI on `Loading plugins…` for the whole timeout and blanked every capture.

## Q11 attention -- WORKS

`api.attention.notify({ title, message, notification: false, sound: false })`
resolved to (verbatim JSON):

```json
{"ok":false,"notification":false,"sound":false,"skipped":"attention_disabled"}
```

Default (unconfigured) attention settings therefore make `notify` a no-op: no
sound, no system notification, and `ok:false`. `api.attention.dispose()` is also
present. Evidence: `out/loaded.json.attention`.

## Q12 failure mode -- WORKS (the TUI still starts)

The driver installs a second package, `relevo-probe-v1`, exporting the v1 shape
`{id, tui}`. Measured behaviour:

* The TUI starts normally; the plugin never runs.
* `/plugins` lists it under **TUI** as `failed` (`out/13-plugins.txt`).
* Pressing Enter on the failed entry opens the error view showing, verbatim,
  `Invalid V2 TUI plugin module: <path>/plugins/relevo-probe-v1`
  (`out/13-plugins.txt`, `out/13b-plugin-error.txt`).
* The footer also shows `⊙ 1 plugin failed /plugins` and a home-screen toast
  `Plugin failed: <target>` with an `Open plugins` action
  (`out/01-home.txt`).

A plugin that throws at load therefore degrades to a visible, inspectable
warning; it does not take the TUI down.

## Deviations from the plan (recorded, not hidden)

1. **Plugin installation mechanism.** The plan's `tui.jsonc` ``"plugin"`` list
   does not load anything on 2.0.14 (Q1). The probe is installed as a package
   directory under `<config>/plugins/`, and `tui.jsonc` is kept in the tree only
   as the record of what the plan expected.
2. **Plugin module shape.** `{ id, setup }`, not the v1 `{ id, tui }`.
3. **Registration APIs.** R1-R9 are written against the real v2 api
   (`ui.slot`, `ui.router`, `ui.dialog.select/prompt`, `ui.toast.show`,
   `keymap.layer`, `attention.notify`) because the api the plan's §4 targets
   does not exist in 2.0.14. Every v1 call is still attempted and its failure
   recorded in `loaded.json.attempts` (`api.slots is undefined`,
   `api.route is undefined`, `api.keymap.registerLayer is not a function`,
   `api.ui.dialog.replace is not a function`,
   `api.ui.toast is not a function`).
4. **R1's tick is a storage store, not a Solid signal** -- dynamic
   `import("solid-js")` fails; the store gives the same reactive behaviour and
   is what the builtin sidebar plugins use.
5. **R2 uses `home.footer`** -- `home_bottom` is not a slot path in 2.0.14.
6. **R8 is fire-and-forget** rather than awaited at load (Q10).
7. **Two additions for evidence:** a second toast fired on the sidebar's first
   render (so R3's toast appears in a capture at all -- Q6), a `bind:"escape"`
   command (Q8), a deliberately failing v1 plugin package (Q12), and two extra
   captures (`06b-page-arrow`, `13b-plugin-error`, `14-final`).
8. **`out/opencode.log` is 0 bytes**: without `--print-logs` the TUI writes
   nothing to stderr, and the plan's §6 launch line does not pass it.

## Consequences for the #393 design

* Ship a **single `.tsx` file, no build step**: a package directory under
  `<config>/plugins/<name>/` whose `exports["./tui"]` module exports
  `{ id, setup }`; JSX compiles at load from the `@opentui/solid` pragma.
* `tui.jsonc` / `tui.json` are dead weight for plugins on 2.0.14 -- a relevo
  installer must write into `<config>/plugins/`, not into a `plugin` array.
* The installed `@opencode-ai/plugin` 1.18.25 TUI types describe an api that no
  longer exists (`api.slots`, `api.route`, `api.state`, `api.lifecycle`,
  `api.command`); the design must target the probe's `loaded.json.api_map`
  surface, and relevo's own type shim should be written from it.
* Slots are dotted paths claimed with exactly one of
  `prepend|append|before|after|replace`; the session sidebar is
  `sidebar.content` and the only bottom slot it provides is `sidebar.footer`
  (`home.footer`, `home.footer.status`, `prompt.footer.status`, `app` complete
  the set). There is no title slot to replace.
* The sidebar gives **37 usable columns** and wraps rather than truncates, so
  planner/binding lines must be written short and pre-elided by relevo.
* Style through `api.theme.*` tokens (`api.theme.text.feedback.warning.base`)
  and `<b>`; raw ANSI is not interpreted, so no hand-rolled colour.
* Reactivity works from `api.storage.store`/`memory` plus `setInterval`, but
  v2 exposes no dispose hook -- a polling plugin leaks its timer until the TUI
  exits.
* Use promise dialogs (`ui.dialog.select/prompt/confirm`) for "act on NEEDS
  YOU"; do not rebuild dialog state by hand.
* Commands must be registered from inside a slot render via
  `api.keymap.layer(...)` (calling it from `setup` throws
  `Keymap.Provider is missing`); commands take
  `{ id, title, group, palette, slash: { name }, bind, run }`, and both slash
  and palette entry points work.
* Plugin routes work (`ui.router.register` + `navigate({type:"plugin",name})`)
  but the plugin must bind its own Escape; a scrollable page needs an
  explicitly `focusable focused` scrollbox.
* `relevo status --json` takes ~20 s here: any sidebar or toast refresh needs a
  cached/smaller payload, and subprocesses must be spawned fire-and-forget
  (`Bun.spawn` and `node:child_process.execFile` both work).
* `api.attention.notify` is a no-op with default settings
  (`skipped:"attention_disabled"`), so relevo's signal to the user must be the
  in-TUI toast (`api.ui.toast.show`), not notifications or sound.
* A broken plugin cannot brick the TUI: it is marked `failed` under `/plugins`
  with its error text, plus a failure toast -- relevo can ship the plugin
  without a kill switch.

## Round 2: handoff and keys (Q13-Q16)

Round 2 is the same probe plugin driven by stage `r2` of the same driver:

```sh
RELEVO_PROBE_STAGE=r2 bash docs/specs/probes/2026-09-24-opencode-tui/run.sh
```

Stage `r2` keeps round 1's committed captures and writes only `out/r2-*` (and
the plugin writes `r2-loaded.json` / `r2-spawn.json` instead of the round-1
names for the same reason). New probe code: R10 signatures, H1-H4 handoff
attempts, R11 keymap survey, R12 leader bindings, R13 panel/tabs; round 1's
R1-R9 still run unchanged.

**The throwaway session.** `opencode session` on 2.0.14 has no `create`
subcommand (`list | delete | export | import`), so the driver launches the TUI
once *without* `-s` and the plugin creates the session in `setup`:

```
api.client.session.create({ title: "relevo-probe r2 (delete me)" })
```

-> `ses_f2b876309ffe8S0gpwmJxeOVEl`. The driver reads the id from
`out/r2-handoff.json`, launches phase B with `-s <id>`, and removes it at
cleanup with `opencode session delete --standalone <id>` -- `r2-notes.txt` has
`Session ses_f2b876309ffe8S0gpwmJxeOVEl deleted`, then "verified absent from
`opencode session list`"; `message` rows for it were 0 before and 0 after.
H1-H4 only ever act on that session, and each attempt runs from a palette
command (`relevo.probe.h1`..`h4`), so the probe never types text into the chat
prompt itself.

### Q13 draft handoff -- FAILS

None of the round-1 candidate apis can put an unsent draft into a session's
prompt. Measured:

* `api.data.session.input` is **not** a draft setter: it is `{ list, has }`,
  `list(sessionID) -> string[]` and `has(sessionID, inputID) -> bool`, both read
  `session.pending` --
  `list(I){return(p.session.pending[I]??[]).flatMap((W)=>W.type==="compaction"?[]:[W.id])}`.
  H1 is therefore recorded `called:false` (`out/r2-handoff.json`), src in
  `out/r2-signatures.json`.
* `api.data.session.prompt` **submits**: its src enqueues
  `{id, sessionID, type:"user", delivery:F.delivery??"steer", payload:{text,...}}`
  and then calls the client's session prompt. H2 is `called:false` with the src
  quoted verbatim in `out/r2-handoff.json`.
* `api.client.session.prompt` is that same submit path over HTTP and is never
  called in this round (R14, signature only).
* Nothing reached the prompt box: `grep -c RELEVO-HANDOFF out/r2-0*.txt` is 0
  in every capture and the prompt box in `out/r2-02-h1.txt` / `out/r2-03-h2.txt`
  is empty, so `prompt_draft_after` is `null` for all four attempts.

A handoff the user reviews before sending therefore cannot be built on these
apis; the only prompt-writing paths found submit to the model.

### Q14 message handoff -- PARTIAL

`api.client.session.synthetic({ sessionID, text, resume: false })` works and is
the only H attempt that was called (H3). It returned a durable inbox item and
**no model reply happened**:

```json
{"id":"msg_0d478f063001EP34kcL6FV4YcU","sessionID":"ses_f2b876309ffe8S0gpwmJxeOVEl",
 "time":{"created":1790271090788},"type":"synthetic","payload":{"text":"RELEVO-HANDOFF-H3"},
 "delivery":"steer"}
```

* `messages_added` = 0 (rows in `opencode.db` table `message` for that session,
  after minus before; 0 before and after every attempt).
* `resume: false` is the no-model-reply lever -- the server only wakes the
  session when `resume !== false`.
* What the user sees: **nothing in the chat.** `out/r2-04-h3.txt` shows an empty
  session and an empty prompt box. The item is pending in the session's inbox
  instead: H4's `api.client.session.inbox.list({sessionID})` returns it first
  (`msg_0d478f063...`, `out/r2-handoff.json` H4 `inbox_list`), and `inbox.update`
  (PATCH `{delivery}`) is the only way to change it.
* The item carries `delivery:"steer"`, so it is fed to the model the next time
  the session runs: a synthetic handoff does not stay unsent forever.

H4 `api.client.session.inbox` is a request namespace
(`list` / `cancel` / `update` over `/api/session/:id/inbox`), so it cannot add a
message and is recorded `called:false`.

### Q15 keys -- PARTIAL

* The leader is `ctrl+x` as the live api reports it: `keymap.shortcuts("session.new")`
  returns `["ctrl+x n"]` (`out/r2-keys.json`), and the probe's palette entry
  renders as `relevo-probe leader r ... relevo · ctrl+x r`
  (`out/r2-05b-palette.txt`).
* **Not free.** `ctrl+x n` is taken by `session.new` -- the only live hit in the
  survey (`out/r2-keys.json.taken`). `ctrl+x r` has no live binding at survey
  time, but 2.0.14's default keybind table (strings in the binary) binds
  `session.redo` to `<leader>r` and `session.new` to `<leader>n`; nothing is
  bound to `ctrl+x R` or `ctrl+x shift+r`.
* The probe's bindings **do fire**: registered as `<leader>r` / `<leader>n`
  through `keymap.layer` from inside the app-slot render (the syntax
  `keymap.shortcuts` shows: `session.sidebar.toggle` is written `<leader>b`),
  pressing `C-x` then `r` / `n` in tmux wrote `out/r2-fired-r.json` /
  `out/r2-fired-n.json` and showed the `leader r fired` / `leader n fired`
  toasts (`out/r2-06-leader-r.txt`, `out/r2-07-leader-n.txt`).
* The plugin's layer wins while it is registered: pressing `C-x n` did not
  create a session (session list identical before and after, 100 entries each;
  `out/r2-session-ids-new.txt` is empty), so the builtin `session.new` is
  shadowed rather than also firing.

### Q16 tabs/panel -- FAILS

`api.ui.tabs` and `api.ui.panel` are controller objects, not renderables.
Calling either as a component inside the round-1 plugin route throws (verbatim
first line; the full text is in `out/r2-loaded.json.r13`):

```
Error: api.ui.tabs is not a function. (In 'api.ui.tabs({
```

`api.ui.tabs` is `{ enabled, list, open, focus, move, close }` -- session-tab
control (`open(sessionID)`, `focus(sessionID)`, `move(sessionID, index)`,
`close(sessionID)`) -- and `api.ui.panel` is `{ open, close, current }`, where
`panel.open(name, {presentation})` returns `false` unless the current route is a
session route and the plugin is active. Neither renders inside a plugin route;
the errors are visible in the capture `out/r2-08-tabs.txt`.

### After round 2

Appended to *Consequences for the #393 design*:

* No tested api can put an **unsent draft** in the planner's chat prompt: the
  candidates are read-only (`data.session.input` is `{list,has}` over a
  session's pending input ids) or they submit (`data.session.prompt`,
  `client.session.prompt`). A "review before send" handoff needs a different
  mechanism, e.g. a plugin-owned dialog that submits on confirm.
* `api.client.session.synthetic({sessionID, text, resume:false})` is the only
  found way to add a message with no model reply: it returns a durable inbox
  item, writes no `message` row and shows nothing in the chat; it is
  `delivery:"steer"`, so it is delivered to the model on the session's next run.
* `api.client.session.inbox` is `list` / `cancel` / `update` only -- it can
  inspect and cancel pending items, never add one.
* `ctrl+x` is the leader and both `ctrl+x r` and `ctrl+x n` are taken
  (`session.redo`, `session.new`); a plugin command bound to them registers and
  fires, and shadows the builtin while the plugin is loaded, so relevo must not
  silently take over "redo" and "new session".
* `api.ui.tabs` / `api.ui.panel` are session-tab and panel controllers, not
  components: a tabbed builder-detail page cannot use them and must be built
  from `<box>` / `<text>` plus the plugin's own state.
* `opencode session` has no `create`, so a session can only be created from the
  plugin (`api.client.session.create`); `opencode session delete --standalone <id>`
  removes it cleanly.

## Round 3: server plugin and shell env (Q17-Q21)

Round 3 explores whether an OpenCode **server** plugin can export session
identity to shell commands, on top of round 2's commit `d577e7d1`. Measured
facts from stage `r3` of `docs/specs/probes/2026-09-24-opencode-tui/run.sh`
running in a throwaway session (`ses_f2b5153a9ffeCSVKkXXNlb88OV`), with
evidence under `docs/specs/probes/2026-09-24-opencode-tui/out/r3-*`.

### Q17 load -- WORKS

A server plugin loads from the same `<config>/plugins/<name>/` package directory
via an `exports["./server"]` entry (or `<dir>/server.js|ts`).

* **Resolution**: In the binary, the package loader `Lm(r)` resolves
  `server: n(["server", ""])`. It checks `[r.name, "server"]` (matching
  `exports["./server"]`) or `s.resolve(r.directory, "server")`.
* **Module shape**: `vD = o({ default: G([ o({ id: t, effect: Jr((e) => typeof e === "function") }), o({ id: t, setup: Jr((e) => typeof e === "function") }) ]) })`.
  The module default export must be `{ id: string, setup: function }` or
  `{ id: string, effect: function }`.
* **Failure mode (verbatim error)**: Exporting the wrong shape (such as
  `{ id, server }`) throws `PluginModule.LoadError`:
  ```
  PluginModule.LoadError: Plugin must export a default definition with an id and an effect or setup function. (cause: SchemaError(Missing key
    at ["default"]["effect"]
  Missing key
    at ["default"]["setup"]))
  ```
* **API passed to `setup(api)`**: 25 server namespaces (`agent, aisdk, app,
  command, event, experimental, generate, integration, location, mcp, model,
  options, permission, plugin, provider, reference, rpc, session, shell, skill,
  storage, tool, vcs, websearch, worktree`).
* Evidence: `out/r3-server-loaded.json`, `out/r3-opencode.log`.

### Q18 hook -- FAILS (name & session id), WORKS (env mutation)

The premise name `"shell.env"` is dead in 2.0.14, and the hook input carries no
session id. However, the real hook does allow mutating environment variables for
shell commands:

* **Real hook name**: `api.shell.hook("create.before", callback)`. OpenCode's
  `PluginHooks` registry prefixes namespaces as `${namespace}.${hook}`, so this
  registers for `shell.create.before`. The name `"shell.env"` does not exist and
  is never triggered.
* **Input payload**: `Shell.create` constructs:
  ```ts
  sH = {
    command: rH.command,
    cwd: rH.cwd ?? e.directory,
    timeout: rH.timeout ?? 0,
    shell: rH.shell,
    env: { ...FH ?? process.env, TERM: "xterm-256color", OPENCODE_TERMINAL: "1" }
  };
  ```
  The input carries **`command`, `cwd`, `timeout`, `shell`, and `env`**.
  It does **NOT** carry `sessionID`, `callID`, or `metadata` (session id lives on
  the outer `rH.metadata.sessionID`, which `Shell.create` does not copy into `sH`).
* **Setting variables**: `PluginHooks.trigger` does not read the callback return
  value (`yield* h.callback(f); return f;`); the callback must mutate
  `input.env` in place. The mutated `sH.env` is passed directly to
  `i.spawner.spawn`.
* In our run, the hook set `RELEVO_PROBE_SESSION="none"` and
  `RELEVO_PROBE_HOOK="1"`, successfully recorded in all 16 hook invocations.
* Evidence: `out/r3-hook-calls.jsonl`, `out/r3-server-loaded.json`.

### Q19 reach -- REACHES ALL THREE

All three shell execution paths funnel into the single `Shell.create` service:

* **P1: TUI prompt shell mode (`!` prefix)**: **REACHED**. In `runPromptTurn`,
  when `mode === "shell"`, it calls `Te` which directly invokes
  `api.client.session.shell` without a model turn. Captured in
  `out/r3-02-p1-shell.txt` showing `RELEVO_PROBE_HOOK=1` and in
  `out/r3-hook-calls.jsonl` (call 1).
* **P2: `api.client.session.shell` (TUI palette command `relevo.probe.shell`)**:
  **REACHED**. Invokes `POST /api/session/:sessionID/shell` without a model turn.
  Server handler calls `SessionShell.start` -> `Shell.create` -> triggers
  `shell.create.before`. Captured in `out/r3-03-p2-api-shell.txt` showing
  `RELEVO_PROBE_SESSION=none` and `RELEVO_PROBE_HOOK=1`.
* **P3: The agent's bash tool (`opencode.tool.shell`)**: **CONFIRMED FROM
  SOURCE; NOT TESTED (needs a model)**. In the binary, `ShellTool.Plugin`'s
  `execute` calls `d.create` (`Bc.create` / `Shell.create`), which triggers
  the exact same `shell.create.before` hook before spawning.
* Evidence: `out/r3-reach.json`, `out/r3-hook-calls.jsonl`, decompiled binary handlers.

### Q20 cost -- WORKS, but hook execution directly stalls commands

Subprocess execution inside the hook works, but hook latency blocks command
spawning 1:1:

* `relevo version` spawned once from inside `shell.create.before` with
  `Bun.spawn` / `execFile` completed in **5 ms** (`out/r3-cost.json`).
* Because `s.trigger("shell", "create.before", sH)` is awaited synchronously
  before `spawner.spawn`, sleeping inside the hook delays process spawning:
  * Sleep 0 ms: hook median **0.0 ms**, command round-trip median **34.0 ms**.
  * Sleep 200 ms: hook median **200.0 ms**, command round-trip median **237.0 ms**.
  * Sleep 1000 ms: hook median **1001.0 ms**, command round-trip median **1038.0 ms**.
* Evidence: `out/r3-cost.json`.

### Q21 coexistence -- WORKS

A single package directory under `<config>/plugins/relevo-probe/` with:
```json
{
  "name": "relevo-probe",
  "version": "1.0.0",
  "type": "module",
  "exports": {
    "./tui": "./tui.tsx",
    "./server": "./server.ts"
  }
}
```
loads both halves cleanly. The TUI process boots `./tui` (`out/r3-loaded.json`),
and the server background process boots `./server` (`out/r3-server-loaded.json`).

### After round 3

* A server plugin loads via `exports["./server"]` exporting `{ id, setup }` or
  `{ id, effect }`, coexisting with `./tui` in a single package.
* The shell environment hook is `api.shell.hook("create.before", (spec) => { spec.env.X = "y" })`
  (v1 `"shell.env"` does not exist); the hook must mutate `spec.env` in place.
* The hook payload carries only `{ command, cwd, timeout, shell, env }` -- it
  does **not** carry `sessionID` or `callID`, so the hook alone cannot identify
  which planner or session triggered the command without external correlation.
* The hook reaches all three shell paths: TUI `!` shell mode,
  `api.client.session.shell`, and the agent's bash tool (`opencode.tool.shell`),
  because all funnel through `Shell.create`.
* Subprocess execution like `relevo version` works inside the hook (~5 ms), but
  any delay in the hook stalls process spawning 1:1.
