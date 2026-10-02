# `relay mcp`: the planner channel -- reports and NEEDS YOU pushed into a Claude Code session, and four verbs as tools (#124, #123)

Status: approved 2026-09-21. Round 1 (this spec's §3-§7) closes #124; round 2
(§8) closes #123. Follows the webhook sink (#4, `internal/hooks/webhook.go`)
and the persistence db (#247).

## 1. Problem

A planner running under Claude Code hands a builder a plan with `relay send`
and then has nothing to do but wait. Today it waits inside its own turn --
`relay wait <name> --timeout 9m` in a loop -- because the only way relay can
reach the planner is to type the report into its pane (`DeliverPending` ->
`herdr agent prompt`), and typing into a pane whose human may be mid-sentence
is the clobber the HELD state exists to avoid. So the turn stays open for
hours to keep the pane's input box empty.

Claude Code channels (research preview) let an MCP server push an event into
a running session; the event lands in the transcript as a
`<channel source=...>` block, starts a turn if the session is idle, and queues
in order if it is busy. There is no input box to clobber. That is the missing
delivery path.

Separately, every relay verb the planner runs today is a shell line composed
from prose, with `HERDR_PANE_ID=... ` prefixed by hand because the Bash tool's
shell does not inherit the pane id (the `claude` process itself does).

## 2. Goals and non-goals

Goals:

- A Claude Code planner with the relay plugin attached receives every
  planner-bound payload (report, consult answer, ask result, edge artifact)
  as a channel event, the moment the daemon has it, without the planner
  polling. HELD never happens for such a planner.
- NEEDS YOU, broken and orphaned are pushed once per episode.
- `status`, `send`, `answer`, `done` are MCP tools with schemas; each calls
  the same `internal/relay` function the CLI calls and returns the CLI's
  `--json` shape. The planner pane comes from the server's environment.
- A dead or absent `relay mcp` degrades to today's pane delivery within one
  daemon tick. Nothing is lost: the mailbox stays pending until confirmed.
- No new go.mod dependency. No Node/Bun runtime. The plugin runs `relay`
  from PATH.

Non-goals:

- A `wait` tool. The channel is the wait. (Amended: tools mode for Claude
  Code has a blocking `wait` tool; the channel still is the wait where there
  is one.)
- `diff`, `log`, `pull`, `unavailable/available`, `policy` as tools. Bash
  already shapes them. Later if wanted.
- Pushing stalled, stale, switched, round_started. Observations the next
  report explains; `relay status` shows them.
- opencode or agy planners. No claim from them -> pane delivery as today.
- Any Claude Code UI (anthropics/claude-code#52235).
- The `--pick` and `--allow-yolo` flags. Escalation stays on the CLI.
- Permission relay (`claude/channel/permission`). Not declared.

## 3. Mechanism

### 3.1 The mailbox has two consumers

`store` already keeps a per-binding queue of planner-bound entries
(`Tx.PendingForPlanner(name)` returns the oldest unconfirmed `to_planner`
entry; `Tx.ConfirmIndex(name, idx)` marks it delivered). The daemon is one
consumer. `relay mcp` is the second. Exactly one of them acts on a given
planner pane at a time, arbitrated by a **claim**.

### 3.2 The claim

`<state>/channels/<pane>.json`, where `<pane>` is the pane id with `:`
replaced by `_` (so `wG:pQ` -> `wG_pQ.json`):

```json
{"pane": "wG:pQ", "pid": 12345, "started_at": "...", "seen_at": "...", "cwd": "/home/fuad/projects/relay", "version": "0.6.0"}
```

- Written by `relay mcp` at start, `seen_at` refreshed every poll (1 s),
  removed on clean exit.
- **Live** means: the file parses, `pid` is alive, and `seen_at` is within
  `ClaimTTL` (10 s) of now. Anything else is stale and is treated as absent.
  A stale file is deleted by whoever reads it (daemon or a new `relay mcp`),
  so a crash leaves no residue past one tick.
- One claim per pane. A second `relay mcp` for the same pane (a restarted
  Claude Code whose predecessor is still exiting) overwrites the file only
  when the existing claim is stale; otherwise it refuses to start with
  `pane wG:pQ already has a live channel (pid N)` -- Claude Code shows the
  server as failed and the planner keeps pane delivery.

### 3.3 Daemon side: `DeliverPending` yields to a live claim

New first check in `DeliverPending`, before the planner status gate:

```
if claim := rt.Channels.Live(b.Planner.PaneID); claim != nil:
    return clearPlannerScreen(b), Delivery{Reason: "planner has a channel"}, nil
```

All-false `Delivery` means "not yet, try again next tick", which is the
existing contract. No HELD transition, no `Notify`, no `promptWithRetry`.
`deliverAndSettle` logs nothing for it (empty `Delivered`/`Held`).
Remote-owned bindings (`b.Owner != ""`) are already skipped before this
point and stay skipped.

### 3.4 Channel side: `relay mcp` drains its pane's mailbox

Every poll (1 s):

1. Refresh the claim.
2. `Store.List()`; keep bindings with `Planner.PaneID == myPane` and
   `Owner == ""`.
3. For each, under `Store.WithLock`: `PendingForPlanner`; if found, push
   the event (§3.5), then `ConfirmIndex`. Push-then-confirm, as the daemon
   does: a crash between them re-pushes that one entry after restart, never
   drops it. One entry per binding per poll; the next poll takes the next.
4. State edges (§3.6).

The store lock is per-operation (`flock`, short critical sections); the CLI
already coexists with the daemon under it, and so does this.

### 3.5 Event format

Payload events:

```
<channel source="relay" binding="judge" round="3" kind="report" path="/home/.../003-report.md" seq="7">
<the entry's Payload, verbatim -- already prefixed with the OriginLine>
</channel>
```

`meta` keys are `binding`, `round`, `kind`, `seq`, and `path` when the
entry has one. Keys are identifiers (letters, digits, underscore) as Claude
Code requires; values are strings.

State events (§3.6):

```
<channel source="relay" binding="judge" round="3" kind="state" state="needs_you" old_state="active">
judge round 3: NEEDS YOU -- <the binding's Display reason if any>
run relay status --name judge, then answer, unavailable, or stop.
</channel>
```

The `source` attribute is set by Claude Code from the server's configured
name; relay names the server `relay` in the plugin manifest.

### 3.6 State pushes

Kept in memory per binding: last-seen `State`. On each poll, for each
binding of mine, if `State` changed and the new state is one of
`needs_you`, `broken`, `orphaned`, push one state event. `orphaned` for the
pane I am on is contradictory (I am the planner) and is still pushed: it
means the store thinks otherwise, which the human should see. First
observation of a binding already in one of those states pushes once (a
restarted `relay mcp` re-announces; harmless and wanted). Transitions out
of those states push nothing.

### 3.7 Instructions to the model

The server's `instructions` string is the round protocol as it applies to
events, in this order: what a `<channel source="relay">` event is; that a
`kind="report"` event means the round closed and the planner must now run
the project's check command and compare the diff against the plan before
`done`; that a `kind="state" state="needs_you"` event needs a decision
(`answer`, `unavailable`, `stop`); that events for bindings the planner did
not send are still its own (same pane) and must not be ignored; that the
four tools exist and that every other verb is Bash. It is one constant in
`internal/mcp/instructions.go`, under 60 lines, so it is reviewable as
text.

## 4. Tools

Server name `relay`; Claude Code exposes each as
`mcp__plugin_relay_relay__<tool>`.

| tool | input schema | calls | result |
|---|---|---|---|
| `status` | `{name?: string, all?: bool}` | `relay.Status` | `Report` as `--json`; bindings filtered to `Planner.PaneID == myPane` unless `all`; `name` narrows further |
| `send` | `{name: string, file: string, tier?: string, verify?: bool, regate?: int, dry_run?: bool}` | `relay.Send` / `relay.SendDryRun` | `SendResult` / `DryRun` JSON |
| `answer` | `{name: string, text?: string, keys?: string, choice?: int}` -- exactly one of the three | `relay.Answer` | `{"ok": true, "text": AnswerText(name)}` |
| `done` | `{name: string}` | `relay.Done` | `DoneResult` JSON plus `"text": DoneText(name, r)` |

- `name` is required on `send`, `answer`, `done`. The CLI's "default to the
  binding for this cwd" rule is not offered: a tool call should name what
  it acts on.
- Errors: a `relay.*` error becomes an MCP tool result with `isError: true`
  and the error's message verbatim. Input validation failures (missing
  `name`, two of `text|keys|choice`) are JSON-RPC `-32602` invalid params.
- The `Runtime` is built by the same constructor the CLI uses
  (`newRuntime()` in `cmd/relay/main.go`) with `PlannerPane` resolved by §5.

## 5. Pane resolution

In order: `--pane <id>` flag; `$HERDR_PANE_ID` (inherited from the `claude`
process, verified present on 2026-09-21); else exit 2 with
`relay mcp: no planner pane (set HERDR_PANE_ID or pass --pane)`. No herdr
lookup, no cwd guessing: a channel that drains the wrong pane's mailbox is
worse than one that refuses to start.

## 6. Protocol

- JSON-RPC 2.0 over stdio, one message per line (the MCP stdio transport).
- `initialize`: reply with `protocolVersion` `2025-06-18` regardless of what
  the client offered (Claude Code refuses to register a channel that
  negotiates `2026-07-28`), `capabilities: {tools: {}, experimental: {"claude/channel": {}}}`,
  `serverInfo: {name: "relay", version: <relay version>}`, `instructions`.
- `notifications/initialized`: start the poll loop. Nothing is pushed
  before it.
- `tools/list`, `tools/call`, `ping`. Any other method: `-32601`.
- Pushes: `notifications/claude/channel` with `params: {content, meta}`.
- stdin EOF or SIGTERM: remove the claim, exit 0.
- Logging to stderr only (Claude Code captures it); never stdout.

Implemented in `internal/mcp` with the standard library. The surface is
five methods and one notification; a dependency for it is not worth the
`go mod tidy` and supply-chain cost.

## 7. Packaging and launch

```
.claude-plugin/marketplace.json           name "relay"; plugins: [{name: "relay", source: "./claude-plugin", version}]
claude-plugin/.claude-plugin/plugin.json  name "relay", version, mcpServers: {relay: {command: "relay", args: ["mcp"]}}
```

- `relay` from PATH, like the daemon service and `relay agent install`.
- `scripts/check-plugin-version.sh` also requires `claude-plugin/.claude-plugin/plugin.json`
  and `.claude-plugin/marketplace.json` to agree with `herdr-plugin.toml`.
- Install: `/plugin marketplace add fuad-daoud/relay`, `/plugin install relay@relay`.
- Launch: `claude --agent architect --model opus --dangerously-load-development-channels plugin:relay@relay`.
  `--channels` rejects any plugin not on Anthropic's allowlist during the
  research preview; the development flag asks for confirmation at start.
- **The delivery hole, and the guard against it.** Without either flag
  Claude Code still starts the MCP server and its tools work, but every
  push is dropped silently -- no error reaches the server. A claim written
  in that state would stop pane delivery and deliver nothing. So `relay
  mcp` decides its **mode** at startup from its parent process's argv
  (Claude Code spawns the server as a direct child; `/proc/<ppid>/cmdline`
  on Linux, `ps -o args= -p <ppid>` on macOS):
  - argv contains `--channels` or `--dangerously-load-development-channels`
    -> **channel mode**: claim + pushes + tools.
  - otherwise, or the parent cannot be read -> **tools-only mode**: no
    claim, no pushes, tools work, one stderr line saying why. Pane delivery
    continues as today.
  - `--mode channel|tools` overrides the detection (tests, unknown
    platforms). The manifest passes nothing.

## 8. Round 2 (#123): skill and commands

- `claude-plugin/skills/relay/SKILL.md`: the round protocol (the "Handing
  off" section of `architect.claude.md`, made harness-neutral), user- and
  model-invocable. The architect definition's section is replaced by one
  line pointing at the skill when the plugin is installed; it stays in the
  definition for planners without the plugin.
- Commands, thin: `/relay:status [name]`, `/relay:send <name> <file>`,
  `/relay:answer <name> ...`, `/relay:done <name>` -- each a ` ```! `
  preamble that runs the CLI with `--json` and a one-paragraph prompt that
  tells the model how to read it. They shell out; they do not call the
  tools, so a planner without channels enabled still has them.
- `relay doctor`: plugin installed and version matches; for each live
  claim, the mode `relay mcp` detected (§7).
- Tests: command files are rendered against a stubbed `relay` on PATH
  (a shell script printing fixture JSON); nothing reaches herdr.

## 9. Testing (round 1)

- `internal/mcp`: framing, `initialize` (offered `2026-07-28` -> answered
  `2025-06-18`; offered `2025-06-18` -> same), `tools/list` equals the four
  schemas, `tools/call` dispatch to a fake `Verbs` interface, unknown
  method -> `-32601`, invalid params -> `-32602`.
- `internal/relay/channel_claim_test.go`: live/stale/dead-pid/absent;
  a stale file is removed on read; a live claim refuses a second writer.
- `internal/relay/deliver_test.go` (extend): live claim -> entry stays
  pending, no `Prompt`, no `Notify`, state unchanged; stale claim ->
  delivered as before. Mutation check: remove the claim guard and the first
  test must fail on the `Prompt` call.
- `internal/relay/channel_drain_test.go`: one pending entry -> one push
  with the right meta, then confirmed; push error -> not confirmed, retried
  next poll; state edge tests for the three states and for "already in
  state at first sight".
- `make e2e` after the round (deliver path touched). The e2e suite has no
  claim, so its assertions are unchanged; it proves the guard is inert
  without a claim.
- By hand, once, after merge: launch with the development flag, `relay
  send` a trivial plan to a headless builder, end the turn, and see the
  report arrive as a `<channel>` event. Record the result on #124.

## 10. Risks

- Research preview: the flag name or the notification method may change.
  Everything Claude-Code-specific is in `internal/mcp/channel.go` and the
  manifest; the claim and the daemon guard do not know what a channel is.
- Mode detection reads the parent's argv (§7). If Claude Code ever spawns
  MCP servers through an intermediate process the detection fails closed
  (tools-only) and the planner notices reports still arriving in the pane;
  `--mode channel` is the escape hatch and `relay doctor` (round 2)
  reports the detected mode.
- The claim's pid check is not pid-reuse safe. Combined with the 10 s TTL
  the window is a reused pid within 10 s of a crash on a live heartbeat --
  accepted.
