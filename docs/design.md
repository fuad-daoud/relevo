# Relevo — automated mastermind↔builder handoff across agent harnesses

Superseded in part by `docs/specs/2026-09-22-drop-herdr-design.md` (#303).

Date: 2026-09-04
Status: **historical record.** This is the design as approved before relevo was
built, kept because it explains *why* the pieces are shaped the way they are.
It is not maintained against the code — where the two disagree, the code and
the README are right. Its interface and state sections were refreshed to
today's command names and storage during the 2026-09 housekeeping; the rest is
the design as it was approved then.

## Problem

Work is split across two agents in two panes. A **mastermind** (architect role: `mastermind`,
`cmastermind`, `amastermind`) designs and verifies. A **builder** (`plan-executor` role:
`builder`, `cbuilder`, `abuilder`) implements. The human talks only to the mastermind.

Today the loop is driven by hand: copy the plan out of the mastermind pane, paste it into
the builder pane, wait, copy the report back, paste it into the mastermind, repeat until the
mastermind declares the work verified. Questions that need a human are answered by talking to
the mastermind, which is the desired behaviour and must survive automation.

The copy-paste is the only manual step, and it is pure mechanism. Relevo automates exactly
that and nothing else.

## Goals

- Move plans, reports, questions and answers between a bound mastermind and builder with no
  human copy-paste.
- Keep the human in conversation with the mastermind at all times; never steal or corrupt
  their input.
- Let the human choose the builder (`builder` / `cbuilder` / `abuilder`) per binding and
  switch mid-feature.
- Support multiple concurrent bindings, in one project or across projects.
- Everything visible: builders run in real panes the human can watch and scroll.

## Non-goals

- Relevo makes no judgements. It never summarises, rewrites, or decides whether work is
  done. All judgement stays in the mastermind.
- No headless execution. If it isn't in a pane, relevo doesn't run it.
- No modification of herdr. Relevo is built beside it, against its socket API.
- No auto-worktrees, no parallel builders on one tree, no scheduling.

## Context: what herdr already provides

herdr (0.8.2, AUR `herdr-bin`) is the substrate. Everything below already works:

| Need | herdr command |
| --- | --- |
| Enumerate agents, kinds, cwds, states | `herdr agent list` |
| Lifecycle state (`idle`/`working`/`blocked`/`done`/`unknown`) | `herdr agent get`, `wait` |
| Submit a prompt into an agent | `herdr agent prompt <t> <text> [--wait]` |
| Answer a dialog in a blocked agent | `herdr agent send-keys <t> <key>` |
| Read terminal output | `herdr agent read --source recent-unwrapped` |
| Create a visible pane beside the mastermind | `herdr pane split --current --direction right --cwd "$PWD" --no-focus` |
| Start a named agent in that pane | `herdr agent start <name> --kind <kind> --pane <id> -- <args>` |
| Surface a nudge to the human | `herdr notification show` |
| Caller's own location | `$HERDR_PANE_ID`, `$HERDR_TAB_ID`, `$HERDR_WORKSPACE_ID` |

Integrations already installed: **opencode** (`~/.config/opencode/plugins/herdr-agent-state.js`)
and **claude** (`~/.claude/hooks/herdr-agent-state.sh`). These push accurate lifecycle state
and session identity over the herdr socket.

Two herdr constraints shape the design:

1. **Terminal reads are unreliable.** Agent TUIs run on the alternate screen; completed
   responses scroll out of reach of `agent read` regardless of `--lines`. herdr's own
   prescribed workaround is to have the agent write its response to a file and reply with
   the path. Relevo therefore uses **file handoffs**, not screen scraping.
2. **`agent prompt` is rejected against a blocked agent** (`agent_blocked`). Answering a
   dialog must go through `send-keys`.

## Architecture

Three pieces, deliberately thin.

```
relevo CLI      Invoked by the MASTERMIND through its Bash tool. Harness-agnostic, so it
               works whether the mastermind is claude, opencode or agy.

                 relevo bind --candidate <candidate> [--name <n>]
                 relevo bind --resume <name>
                 relevo send --file <path>
                 relevo wait [<name>]
                 relevo done <name>
                 relevo unbind <name>
                 relevo status [--json] [--line]
                 relevo show <name> [--report|--diff|--log]
                 relevo ui [:view [args]]

relayd         One daemon per herdr session. Watches herdr agent state. Three jobs only:
                 1. builder -> idle    : deliver its report to the mastermind
                 2. builder -> blocked : deliver the dialog question to the mastermind
                 3. round accounting, timeouts, runaway cap

state          ~/.local/state/relevo/
                 relevo.db           0600: the record (bindings, round log,
                                     rounds, config, secrets; see the README)
                 .worktrees/<name>/  git worktrees (local builders)
                 <name>/NNN-*        a round's files while it is open
                 .daemon.lock        the daemon's lifetime flock
```

**Why the CLI and the daemon are separate.** The outbound leg (mastermind → builder) happens
while the mastermind is mid-turn, so the mastermind can just call `relevo send` synchronously. The
inbound leg (builder → mastermind) happens *after* the mastermind's turn has ended, when no model
is running to notice. That is the only reason a daemon exists.

## Data structures

### The binding record

Today this record is a row in `relevo.db`, not a `bind.json` file; the fields
are the ones designed here.

| Field | Type | Notes |
| --- | --- | --- |
| `name` | string | binding id, `[a-z][a-z0-9_-]{0,31}`, defaults from cwd basename |
| `cwd` | abs path | the working tree; **unique across active bindings** |
| `mastermind.pane_id` | string | e.g. `w2:p3` |
| `mastermind.session_id` | string | from the harness integration; survives pane id changes |
| `mastermind.kind` | enum | `claude` \| `opencode` \| `agy` |
| `runner.agent_name` | string | herdr agent name, e.g. `upjo-builder` |
| `runner.pane_id` | string | |
| `runner.kind` | enum | |
| `candidate` | string | harness/provider/model the builder was started from; empty when adopted |
| `round` | int | current round, starts at 1 |
| `state` | enum | `active` \| `held` \| `needs_you` \| `broken` \| `orphaned` \| `done` |
| `round_cap` | int | default 20 |
| `round_timeout_ms` | int | default 1_800_000 (30 min) |
| `created_at`, `updated_at` | iso8601 | |

#### Endpoint identity and refresh

An endpoint is identified by its `session_id` when one is recorded (exact match, no
fallback). When no session is recorded, identity falls back to `pane_id` plus agent
`kind` (or bare pane ID if no kind was recorded). Relevo treats endpoints as caches of
live agent identity rather than static records: whenever Reconcile locates an endpoint's
agent among live herdr agents, it refreshes the endpoint's `pane_id` (so a session-
identified endpoint survives a pane move) and backfills an empty `session_id` if the
agent reports one.

Both harnesses report a session to herdr, but at different times. Claude's session is
available almost immediately after spawn, making a missing session a brief startup race
resolved by the next tick's backfill. Agy reports a session only once the agent has begun
a conversation (`source: herdr:antigravity_cli`), leaving its session absent when freshly
spawned and idle. Until the session appears, identity is pane plus kind; once it appears,
the backfill records it and identity becomes exact.

Accepted risk: a session-less endpoint whose pane exits and is reissued to a new agent
of the same kind is adopted as the original builder. This exposure lasts until a session
is recorded — brief for claude, and for agy lasting until the agent takes its first turn.

Moving a builder's pane between workspaces breaks its binding if the move happens before
a session has been recorded (bound but not yet started), because herdr issues a new pane
ID and `FindAgent` cannot match the agent without a session ID; the binding transitions
to `broken` and `refreshEndpoint` never runs to learn the new pane ID. Once a session has
been recorded (nearly immediately for claude, and after the first turn for agy), the
session survives the move and `refreshEndpoint` updates the pane ID normally.

### Candidates (config)

Candidates are the `candidates` section of `relevo.db`, edited with
`relevo config`; see [Candidates](../README.md#candidates) and the design spec (`docs/specs/2026-09-11-candidates-design.md`) for configuration format and semantics. Relevo renders harness launch arguments per kind:

| kind | args |
| --- | --- |
| `claude` | `--model <model> --agent <role.Definition>` |
| `opencode` | `--agent <role.Definition> -m <provider>/<model>` |
| `agy` | `--model <model> --agent <role.Definition>` |

then `extra_args` are appended.

Which candidate an omitted token resolves to is decided by the `policy`
section (`order[role]`) together with the
availability ledger: the first ungated candidate in the order, refusing
when nothing ungated serves the role or when several serve it and
nothing is ordered. Each resolution is a `pick` entry in the binding's
round log. See `docs/specs/2026-09-11-policy-order-design.md`.

### The round log entry

Today each entry is an `event` row in `relevo.db`, not a line of `log.jsonl`.

`{ ts, round, direction: "to_runner"|"to_planner", kind: "plan"|"report"|"question"|"answer"|"pick"|"switch",
   path, delivered_at, confirmed: bool, note }`

A `pick` entry is relevo -> log only: which candidate a spawn resolved to and why.
A `switch` entry is relevo -> log only: the builder was replaced mid-round, and why.

## Message protocol

### MasterMind → builder

```
mastermind writes ./plan.md, then runs:  relevo send --file ./plan.md

relevo: assign round N, copy to <state>/NNN-prompt.md
       herdr agent prompt <builder> "
         Round N from the mastermind.
         Read:  <state>/NNN-prompt.md
         When you are done, write your report to: <state>/NNN-report.md
         Then, as the very last thing you do -- after every edit, test and
         commit -- create this empty file: <state>/NNN-done
         Reply here with only the report path."
       append log entry, return immediately
```

### Builder → mastermind

```
relayd, every tick while the round is open (pane or headless):
  if <state>/NNN-done exists:
      if NNN-report.md exists: payload = "Builder finished round N. Report: <path>"
      else:                    payload = "...marker but no report at <path>."   note noreport
      close the round

relayd observes builder -> idle|done, no marker:
  nudge once: "You went idle without finishing. Write your report to <path>
               if you have not, then create the empty file <done> as your
               last action, and reply with only the report path."
  wait for the screen to stay still for the nudge grace
  if NNN-report.md exists:
      payload = "Builder finished round N but never confirmed completion
                 (no NNN-done). Report: <path>. The diff may be premature."  note unmarked
  else:
      scrape = herdr agent read --source recent-unwrapped --lines 200
      write to NNN-report.md marked SCRAPED (may be truncated)
      payload = report + explicit unreliability warning                       note scraped

headless: a process that exited with a report but no marker closes unmarked;
          with neither, "exited without a report" (see headless spec).

  deliver(payload) -> mastermind        # see delivery rule below
```

### Builder blocked

```
relayd observes builder -> blocked
  dialog = herdr agent read <builder> --source detection
  write <state>/NNN-question.md
  deliver("Builder is blocked at a dialog. Question: <path>.
           Answer with: relevo answer --keys <key> | --choice <n> | --text <s>")

mastermind reads the actual dialog, then:  relevo answer --keys enter
relevo: herdr agent send-keys <builder> enter     # NOT agent prompt; that is rejected
```

### Delivery rule (the anti-clobber rule)

`herdr agent prompt` types text and presses Enter. If the human is mid-sentence in the
mastermind pane, their draft and the payload merge and submit as garbage. herdr exposes pane
focus but cannot see the input buffer, so:

```
deliver(payload):
    if mastermind is not idle:            queue, retry on next idle
    else if mastermind pane is focused:   queue, state = held
                                       herdr notification show "<name>: report ready"
                                       # four ways out of held:
                                       #   human focuses another pane -> inject as normal
                                       #   mastermind's input box reads empty (claude) -> inject at once
                                       #   screen unchanged for --held-grace -> inject anyway (appends
                                       #     to an abandoned draft, accepted on purpose)
                                       #   human says "go" -> mastermind runs `relevo pull`, which PRINTS
                                       #     the payload to stdout as tool output. No injection at all,
                                       #     so it cannot collide and cannot be rejected mid-turn.
    else:                              herdr agent prompt <mastermind> payload
                                       mark confirmed in log
```

### Termination

The mastermind calls `relevo done` once it has verified the work. `round_cap` (default 20) is a
runaway guard only: on reaching it relevo stops relaying, sets `needs_you`, and notifies.

## Observability

```
$ relevo status
relayd  running   pid 48213   herdr session default   up 2h14m

webshop    /home/dev/projects/webshop     w2   round 3   ACTIVE
  mastermind  cmastermind        w2:p3  claude    working
  builder  upjo-builder    w2:p4  opencode  working    `builder` -> glm-5.3-flash
  last     14:22:07  plan 003 -> builder
  pending  --

career  /home/dev/projects/api               w4   round 1   NEEDS YOU
  mastermind  amastermind        w4:pA  agy       idle
  builder  career-builder  w4:pB  claude    blocked    <- approval dialog
  last     14:19:51  question 001 -> mastermind
  pending  mastermind to answer

money   /home/dev/money/ai                      wF   round 7   HELD
  mastermind  cmastermind        wF:p1  claude    idle       (focused -- holding)
  builder  money-builder   wF:p2  opencode  idle
  pending  report round 7 -> mastermind, held: quiet 45s of 1m0s
```

Three display states cover everything: **ACTIVE** (someone is working), **NEEDS YOU**
(stalled on a human), **HELD** (ready, but the human is in the pane).

- `relevo wait [<name>]` — blocks until a round closes or needs the mastermind, then prints
  the pending report.
- `relevo show <name> --log` — every relayed message: round, direction, file, timestamp. The
  audit trail for "what did the mastermind actually tell the builder".
- `relevo ui [:view [args]]` — the cockpit: the `:fleet` table, round detail and the `:rounds` grid.
- `relevo status --line` — one row per binding this mastermind owns, for Claude Code's `statusLine` setting; store-only, never probes the harness (spec `docs/specs/2026-09-13-statusline-design.md`).

All agent rows are derived live from herdr on each call. Relevo holds no truth herdr already
has, except bindings and the round log, so `status` cannot disagree with reality.

## Failure handling

| Failure | Behaviour |
| --- | --- |
| Builder pane closed/killed | Binding -> `broken`, relaying stops, pending plan kept. Rebinding resumes at the same round with a short context rebuild. Relevo closes a pane only in `relevo reap` (a consult pane it spawned) and in a mid-round builder switch (the replaced builder's pane). |
| Builder wedged (`working` forever) | `round_timeout_ms` elapses -> `needs_you` + notification. Nothing killed. |
| herdr reports `unknown` | Treated as "keep waiting", **never** as done (herdr documents that `unknown` does not prove completion). After a grace period -> `needs_you`. Most likely with `abuilder`; see prerequisites. |
| `agent_prompt_stalled` | Retry once, then stop and flag. Never blind-refire — a double-submitted plan means two builders' worth of edits. |
| MasterMind session ends (`/clear`, compaction, pane closed) | Binding -> `orphaned`, reports queue on disk. `relevo bind --resume <name>` adopts it into a new mastermind and hands over the round log. |
| relayd restart | Rebuilds from `bind.json` + `log.jsonl` + live herdr state. A pending record is written *before* a prompt is sent and cleared on confirmation; on restart, re-deliver only if the target is idle **and** the log shows no confirmation. Bias toward under-delivering. |
| Second bind on the same cwd | **Refused**, naming the binding that owns it. For genuine parallelism, `herdr worktree create` yields a different cwd and the check passes with no special code path. |
| Human camps in the mastermind pane | Delivery stays `held`; notification escalates. Human says "go" and the mastermind runs `relevo pull`, receiving the payload as tool output rather than as injected keystrokes. After `--held-grace` of screen quiet the daemon injects anyway. |
| Builder gone for 30s, or its provider gated mid-round | The daemon switches to the next ungated candidate in `policy.json` order, bounded by `max_switches`; see `docs/specs/2026-09-11-builder-switching-design.md`. |

## Decisions and rationale

| Fork | Chosen | Why |
| --- | --- | --- |
| Who drives the loop | MasterMind delegates and ends its turn; daemon relays | Keeps judgement in the model the human already talks to, keeps the human able to interject, keeps the daemon dumb enough to trust |
| Builder session lifetime | Persistent per binding | Matches the manual workflow; follow-ups stay short because the builder remembers what it wrote. Reset comes free via rebinding to a new pane |
| Handoff channel | Files | herdr documents that alternate-screen output is unrecoverable by `agent read`; scraping is a labelled last resort only |
| Delivery while focused | Hold + notify, inject when unfocused | The only rule that cannot eat a half-typed message; full autonomy resumes the moment the human looks away |
| Builder selection | Human, in plain English to the mastermind | Preserves existing cost/model control; the token names exactly what starts, so the log and `status` show it without a lookup |
| Concurrent loops on one tree | Refuse the second bind | The one failure mode that destroys work rather than stalling |

relevo enforces one writer per working tree only for bindings: `Bind` refuses a
second binding on a tree another one drives. An agent relevo did not start is
outside that guarantee entirely, and relevo cannot prevent one -- it does not
own the harness.

What it can do is say so. `relevo status` reports, per binding, every live agent
whose cwd is inside that binding's working tree and which no binding accounts
for, as `foreign` rows. The rule is occupancy, not authorship: `herdr agent
list` reports a kind, a status, a cwd and a title, and nothing about writes, so
a sanctioned read-only researcher and a rogue implementer look identical from
here. relevo reports that something is there and shows its title; the reader
draws the conclusion. Rows are never filtered by title, which would mean relevo
trusting a string any agent can set.

How much that covers depends on the builder's harness: claude runs sub-agents
in their own panes, which herdr lists; agy and opencode do not, and herdr
lists nothing extra. For those bindings `relevo status` prints a `coverage` row
after the foreign rows saying that no foreign rows does not mean the tree is
clear. The per-harness record is `harness.Harness.SubAgents`.

An agent is foreign when no binding references it, not merely when it is not
this binding's builder -- a second binding's mastermind may legitimately share a
tree, and relevo knows about it. Agents in subdirectories of the tree count;
agents in parent directories do not.

Known limit: paths are compared literally. A tree reached through a symlink
under one name and reported by herdr under another will not match, and the
agent goes unreported.

### What the diff trail covers

A round's diff covers exactly **send -> report**. That was always true; it was
never written down, and a reader reasonably assumed otherwise.

The window between a report and the next send is now captured separately, as
drift. A round diff is recorded when a round closes; drift is recorded when the
next one opens, and is keyed to that opening round.

**A commit is not drift.** relevo snapshots working-tree *content* (`add -A` into
a temporary index), so a mastermind committing, amending, or rebasing the builder's
work changes nothing relevo can see. A merge that brings in new content, a
checkout, or a builder that kept editing after it reported all do register.

This narrowness is a property to rely on, and a reader who does not know it will
misread a quiet send.

**relevo reports drift; it does not attribute it.** Drift never changes a
binding's state, never notifies, and never fires a hook. relevo cannot observe
*who* wrote -- `herdr agent list` can say which agents were in the tree (that is
the foreign-agent feature), and joining the two is the mastermind's judgement, not
relevo's.

One honest gap: a round that produced no baseline also records no drift origin,
so the next send is silent. It self-heals after one round.

## Prerequisites

1. `herdr integration install antigravity-cli` — only **opencode** and **claude**
   integrations are currently installed. Without it, `abuilder` panes rely on heuristic
   screen detection and will frequently report `unknown`.
2. herdr >= 0.8.2 (for `agent start --kind agy`, `agent wait --until`, `notification show`).

## Out of scope (YAGNI)

- Auto-worktree creation for parallel loops.
- Relevo-side summarisation or context compaction.
- Any mastermind-side intelligence in the daemon.
- Cross-machine relaying (herdr `--remote` exists; not needed yet).
- A TUI in the original scope: `relevo status` / `relevo watch` were enough. Superseded by
  [`docs/specs/2026-09-08-relevo-tui-design.md`](specs/2026-09-08-relevo-tui-design.md), which
  designs `relevo ui` as the cockpit, read-only until B2 of the cockpit plan
  (docs/specs/2026-09-24-cockpit-design.md). `relevo watch` (status on a ticker) was dropped in
  favour of `watch -n2 relevo status` once `ui` existed (#114).
