# Chains: relevo drives build, review and correct across a list of plans

**Issues:** reshapes #388 (a chain spans bindings, not one binding) and
delivers #389 with the reviewer as the judge; #360's detached client is this
spec's server mode (§8). Unblocks #391, which waits on rounds that advance
without a live MasterMind.

## 1. What a chain is

The MasterMind hands relevo an ordered list of round plans. relevo sends each
plan to a builder, hands the result to a reviewer, and, when the reviewer asks
for changes, has a planner write a correction plan that the builder runs next.
A plan advances only when the reviewer passes it. After the last plan an
optional security phase scans the whole branch, and its findings go through the
same build, review and correct loop. The MasterMind hears from relevo once: when
the chain finishes or halts, with a trace of every step.

relevo still judges nothing. Every decision between rounds is a runner's
structured verdict or the project's check; relevo only routes. When a verdict is
missing, a limit runs out or a runner halts, the chain stops with NEEDS YOU; it
never skips a step.

### 1.1 What stays outside

- **Writing the round plans (phase 0).** The MasterMind uses today's verbs: it
  binds a `lite-planner` or `planner`, seeds it with the task, and has it write
  `round-1.md` … `round-N.md` into its artifact dir. The MasterMind reviews and
  corrects them, then starts the chain with those paths.
- **Opening the PR and merging.** When the chain finishes, the MasterMind runs
  the repo's check, compares the diff with the plans, and follows the repo's
  conventions.
- **A context refresh.** Every round of every binding starts a fresh harness
  process; relevo resumes a session only to recover a round a daemon restart
  lost. Nothing accumulates across rounds, so a chain never rebinds to shed
  context.
- **Out of scope:** attaching an existing builder binding, several builders in
  parallel, editing the plan list of a running chain, a cockpit chain view, and
  placing one chain's members on several servers.

## 2. Members

A chain is one `chains` row plus ordinary bindings, its **members**. Each is a
normal binding: `status`, `show`, gating, candidate switching and limits work
on it unchanged.

| member | binding name | actor (setting) | shape | tree |
|---|---|---|---|---|
| builder | `<n>` | the `builder` actor | writer | its own worktree and branch, cut from `--base` |
| reviewer | `<n>-rev` | `chain.reviewer_actor` | reader | the builder's tree |
| planner | `<n>-plan` | `chain.planner_actor` | reader | the builder's tree |
| security | `<n>-sec` | `chain.security_actor` | reader | the builder's tree; only when the security phase is on |

- Member names must pass `store.ValidName`; with the longest suffix (`-plan`),
  a chain name is at most 27 characters. An invalid or taken member name refuses
  the chain before anything is created; names are never truncated.
- The reviewer, planner and security actors must be reader actors, and the
  builder actor a writer; anything else refuses at start.
- The builder's check and `regate` budget are the builder actor's, exactly as
  for a lone binding. The chain does not own them.

## 3. The verb

```
relevo chain --name <n> --plan <file> [--plan <file> ...]
             --feature <label> | --no-feature  [--ticket <ref>]
             [--server <s>] [--base <ref>]
             [--security | --no-security] [--max-corrections K]
             [--reviewer-actor A] [--planner-actor A] [--security-actor A]
relevo chain --resume --name <n> [--security | --no-security]
             [--max-corrections K] [--reviewer-actor A] [--planner-actor A]
             [--security-actor A]
```

- `--plan` is repeatable and ordered; at least one is required. Each file is
  copied into the chain's store when the chain starts, so a later edit of the
  source changes nothing. An empty or unreadable plan refuses the start.
- `--feature`/`--no-feature` and `--ticket` label every member, as on `bind`.
- `--server` puts the whole chain on that server (§8). Without it the chain
  lives on this machine and each member follows its actor's placement (§7).
- The settings flags override `policy.chain` (§5).
- `--resume` continues a halted or stopped chain (§6.3). Flags given with it
  replace the stored values; flags not given keep them.

Existing verbs accept a chain name and act on the chain:

| verb | on a chain |
|---|---|
| `status` | one chain row: status, phase, step, plan i of N, correction count, the active member; the members listed under it |
| `show <n> --trace` | the ordered trace (§9.1); `--json` prints one document |
| `wait --name <n>` | returns once, when the chain finishes (exit 0) or halts or is stopped (exit 3), with the end delivery (§9.2) as its output |
| `stop <n>` | stops the active member's open round and marks the chain `stopped` |
| `done <n>` | releases every member, as `done` does per binding; refused while the chain is `running` |

A name that is both a chain and its builder member (`<n>`) resolves to the chain
for these verbs. `show <n> --round R` without `--trace` still reads the builder
binding's rounds.

## 4. The state machine

A new package, `internal/chain`, owns the chain's state and a pure transition
function. It does no I/O.

### 4.1 State

- `status`: `running | halted | stopped | done`, plus `reason` when halted.
- `phase`: `build | security | finished`.
- `step`: `building | reviewing | correcting | scanning | planning-fixes`.
- `plan`: the index of the plan in progress. In the security phase it
  identifies the fix plan.
- `corrections`: correction rounds spent on the current plan.
- `awaiting`: the (member, round) whose close the chain waits for.
- `settings`: the resolved values of §5, stored at start.

### 4.2 Events and actions

`Next(state, event) → (state, action)`.

Events come from a member's round close:
- a builder close, carrying the report tail's outcome and the gate result
  (`green`, or `red` after the `regate` budget is spent);
- a reviewer close, carrying its verdict (`pass`, `changes`, or none);
- a planner close, carrying whether its plan artifact is present and non-empty;
- a security close, carrying its finding count, or none;
- a member going NEEDS YOU, with its reason;
- a member's round stopped.

Actions are `Send{member, seed}`, `Halt{reason}`, `Stop` and `Finish`.

An event whose (member, round) does not match `awaiting` is ignored and
returns the state unchanged. A daemon restart that replays a close therefore
cannot advance the chain twice.

### 4.3 Transitions

| step | event | action, next step |
|---|---|---|
| building | builder closed: outcome done, gate green or red after regate | seed the reviewer (§4.4) → reviewing |
| building | builder outcome halted, blocked or unstructured | Halt: "builder halted on plan i: <note>" |
| reviewing | verdict pass, plans left | send plan i+1 to the builder; corrections = 0 → building |
| reviewing | verdict pass, last plan, security on, phase build | seed security → scanning, phase security |
| reviewing | verdict pass, last plan, security off; or phase security | Finish |
| reviewing | verdict changes, corrections < K | seed the planner → correcting |
| reviewing | verdict changes, corrections = K | Halt: "reviewer still wants changes after K corrections" |
| reviewing | no verdict, or one that does not parse | Halt: "reviewer gave no verdict" |
| correcting | plan artifact present | send it to the builder; corrections++ → building |
| correcting | plan artifact missing or empty | Halt: "planner wrote no plan" |
| scanning | findings = 0 | Finish |
| scanning | findings > 0 | seed the planner with the findings → planning-fixes |
| scanning | no finding count | Halt: "security gave no finding count" |
| planning-fixes | plan artifact present | send it to the builder; corrections = 0 → building (phase stays security) |
| planning-fixes | plan artifact missing or empty | Halt: "planner wrote no plan" |
| any | a member goes NEEDS YOU | Halt with the member's reason |
| any | a member's round was stopped | Stop: status `stopped` |

The security phase scans once. After its fix plan passes review, the chain
finishes; there is no rescan.

A red gate reaches the reviewer rather than halting: the reviewer sees the
check output, and its corrections spend the same budget K.

### 4.4 Seeds

relevo writes each seed as an ordinary prompt file from templates embedded in
`internal/chain`. A seed names paths; it does not inline content. The diff is
written to a file first.

| seed | carries |
|---|---|
| reviewer | the plan, the builder's report, the round's diff, the gate result and its output, and the verdict block it must end with |
| planner, correction | the reviewer's output (corrections), the plan, the report, the diff, and the gate result |
| security | the branch diff against the chain's base, and the finding-count block it must end with |
| planner, fixes | the security output and the branch diff |

The rounds these seeds start are ordinary member rounds with their own
artifacts. Those artifacts are what the trace points at.

### 4.5 Verdicts

A reviewer's output ends with the fenced `relevo` block builders already end
their reports with (`internal/reporttail`):

````
```relevo
verdict: pass        # or: changes
```
````

The security actor's output ends with `findings: <n>` in the same block. The
reader reads its own member's output file. A missing block, a missing key or an
unknown value is "no verdict". It is never a pass.

## 5. Settings

`policy.chain`, a new group in `internal/policy`, is edited with
`relevo config set` and shown on the cockpit settings screen beside `gate`.

| key | default | meaning |
|---|---|---|
| `chain.max_corrections` | 3 | correction rounds allowed per plan before NEEDS YOU; 0 halts on the first `changes` |
| `chain.reviewer_actor` | `reviewer` | the reviewer member's actor |
| `chain.planner_actor` | `lite-planner` | the actor that writes correction and fix plans |
| `chain.security_actor` | `security` | the security member's actor |
| `chain.security` | `false` | whether the security phase runs |

- A flag beats the setting, and the setting beats the default.
- The resolved values are stored on the chain row at start. Editing the
  settings never changes a running, halted or stopped chain; `--resume` flags
  do.
- What is stored is which actor fills each part, not the actor's contents. An
  edit to an actor's candidates or agent applies from that member's next round,
  as for any binding.

## 6. Running a chain

### 6.1 Start

1. Resolve the settings. Validate the plans, the member names and the actor
   shapes. Any failure refuses the start with nothing created.
2. Create the chain row and the members in one step. The builder is cut from
   `--base` as `bind --worktree` does.
3. Send plan 1 to the builder: `step = building`, `awaiting = (builder, 1)`.

### 6.2 Advancing

The daemon's reconcile already closes each member's round. Once a member of a
running chain closes a round, reconcile maps the close to an event, calls
`Next` and performs the action through the normal `Send`. It then appends the
transition to the trace (§9.1). The event, the new state and the trace row
are written in one transaction.

A member round's output is consumed by the chain, not delivered to the
MasterMind (§9.2).

### 6.3 Halt, stop and resume

- **Halted:** the chain stops at a Halt action with its reason. The member that
  was active keeps its round as it closed.
- **Stopped:** `relevo stop <n>` ends the active member's open round the way
  `relevo stop` does today. The chain becomes `stopped`. The stopped round stays
  in the trace and does not spend the correction budget.
- **Manual sends:** a `send` to a member of a `running` chain is refused with
  `conflict`: "belongs to running chain <n>; relevo stop <n> first". Once the
  chain is halted or stopped, manual sends are allowed.
- **`--resume`** applies any flags, then:
  - if the builder has a closed round newer than the one the chain was
    waiting on or last reviewed (a manual round sent after the halt), it
    reviews that round: seeds the reviewer, `step = reviewing`;
  - otherwise it re-runs the step that halted or was stopped, sending the
    same seed or plan again. A stopped builder round may have left edits in
    the tree; the re-sent plan runs on the tree as it is, as any round does;
  - it resets `corrections` for the current plan to 0, because a human has
    looked at it.

  `--resume` on a `running` or `done` chain is refused.
- **A member lost** (unbound by hand, or its record gone) halts the chain with
  "member <name> gone".

## 7. Where a chain runs: this machine

Without `--server`, the chain row lives in this machine's `relevo.db` and the
local daemon drives it. The MasterMind session can end; the machine and its
daemon must stay up.

Each member binds where its actor's placement puts it, the way `bind --actor`
does. A builder can therefore run on zen while the reviewer and planner run
locally. The local daemon sees a remote member's close through its existing
remote sync, and that close is the chain's event. Sending a seed to a remote
member is an ordinary remote `send`.

## 8. Where a chain runs: a server

`relevo chain --server <s>` hands the whole chain to one server, and the client
can shut down after the server accepts it.

- **Feature check.** The client refuses before creating anything unless the
  server's `WhoAmI.Features` includes `chain` (new, `remote.FeatureChain`) and
  `readers`. The error names the server and the missing feature.
- **One request.** `POST /v1/chains` carries the chain name, the plan texts in
  order, the resolved settings, the feature and ticket labels, and the base
  bundle `bind --server` ships today. The server validates the request against
  its own actors, then creates the chain row and every member under the
  caller's owner id all-or-none, and starts plan 1.
- **Driving.** The server's daemon reconciles owned bindings with the same
  `internal/relevo` code, so the same `Next` drives the chain there. Closed
  member rounds wait unacked; the server already allows several closed,
  unacked rounds, because acks are cumulative and only a running round blocks
  the next send.
- **Placement.** Every member of a server chain stays on that server. Actor
  placement lists are ignored for its members.
- **The client's view.** `status`, `wait` and `show --trace` read
  `GET /v1/chains/{name}`. `stop`, `--resume` and `done` post to
  `/v1/chains/{name}/stop|resume|done`.
- **Pulling back.** When the client syncs, it pulls every member's closed
  rounds in round order, fetches the builder branch's commits into
  `relevo/<n>`, and acks. The end delivery is queued on the client once the
  chain is finished or halted and its rounds are pulled.
- **Remote readers on the builder's tree.** Locally, a reader may share a
  writer's tree. That a remote reader member can do the same is verified when
  this mode is built; a gap is fixed separately and picked up.

## 9. What the MasterMind sees

### 9.1 The trace

A new append-only table, `chain_event`, holds one row per transition:

| column | meaning |
|---|---|
| chain | the chain's id |
| seq | the order within the chain |
| ts | when the transition was applied |
| phase, step | the state before the event |
| member, round | the round whose close was the event |
| event | the event kind and its data (verdict, gate result, finding count) |
| action | what relevo did: the member and round sent, or halt, stop or finish |
| reason | the halt reason, when there is one |

`relevo show <n> --trace` renders it in order, one line per transition, and
names the round each line refers to. For example:

```
plan 2/4  build    x r3       check red after regate
plan 2/4  review   x-rev r2   changes
plan 2/4  correct  x-plan r2  correction plan
plan 2/4  build    x r4       check green
plan 2/4  review   x-rev r3   pass
```

Plans, reports, diffs, reviews and correction plans stay the members' round
artifacts. The trace holds references, not copies.

### 9.2 Delivery

- A member round closed while its chain is running records a
  `consumed by chain <n>` entry in place of a MasterMind-bound payload. Members
  of a running chain are never pending and never NEEDS YOU on the MasterMind's
  surfaces; a consumed payload stays clean after the chain settles, showing its
  phase word rather than REPORT IN and carrying the note as reason.
- When the chain finishes, halts or is stopped, relevo queues exactly one
  MasterMind-bound payload for the chain, delivered through today's routes
  (`wait`, push, `show`). It carries:
  - the status and phase;
  - plans passed of N, the total correction rounds, and the security findings;
  - on a halt, the reason and the `next:` command
    (`relevo chain --resume --name <n>`, or the member round to read);
  - the builder's branch;
  - the `show <n> --trace` command.

### 9.3 Surfaces

- `relevo status`: the chain row (§3), with its members listed under it.
- The statusline: one entry per chain, for example
  `chain x · plan 2/4 · reviewing · 1 correction`, in place of its members'
  rows. A halted chain shows NEEDS YOU there; a settled chain shows DONE
  while its members are present and conjures no row once its members are released.
- The cockpit shows members as ordinary bindings. A chain view is later work.

## 10. Storage

- **Migration 016** adds `chains` (the state of §4.1, the settings, the plan
  list, and the server name for a server chain) and `chain_event` (§9.1).
  Add-only, per `internal/db/migrations/README.md`.
- Membership lives only in `chains`: the row names each member binding and its
  part (builder, reviewer, planner, security), with an index on the member
  name. Reconcile finds a close's chain by the closing binding's name. The
  binding record does not change, so there is no binding-format bump.
- The plan copies live in the chain's store directory. The seeds are the
  members' ordinary prompt files.

## 11. Slices

1. **Local chain, local members.** `internal/chain` (state, `Next`, seeds,
   verdict parsing), migration 016, `policy.chain` and its settings-screen
   section, the `chain` verb and `--resume`, reconcile wiring, the refusal of
   manual sends, `status`/`show --trace`/`wait`/`stop`/`done` on a chain,
   consumed member output and the single delivery, and the statusline entry.
2. **Local chain, placed members.** Members follow their actors' placement;
   a builder on zen. The plan confirms that remote `send` and sync need no
   change for a member and adds what they do need.
3. **Server chain.** `FeatureChain`, `/v1/chains` endpoints, all-or-none
   create, server-side driving, pull-back and the client's view.

## 12. Testing

CI has neither a harness binary nor network access (CLAUDE.md).

- **`internal/chain`:** one table case per row of §4.3, plus:
  - an event for a round other than `awaiting` leaves the state unchanged;
  - `--resume` picks the newer manual builder round;
  - `--resume` resets `corrections`;
  - the verdict and finding-count parsers reject a missing block, key or value.
- **Mutation checks, each with a named failing test:**
  - make `changes` advance to the next plan;
  - make `awaiting` match any round;
  - count a missing verdict as a pass.
- **`internal/relevo`:** reconcile with faked round closes advances a chain,
  writes the trace row with the state in one transaction, records member
  output as consumed, and queues exactly one delivery. A manual send to a
  running chain's member is refused. No `cmd/relevo` test runs `relevo chain`
  against a harness.
- **`make e2e`:** a two-plan chain with the fake harness, where plan 1 gets one
  `changes` verdict and one correction round, and the security phase finds one
  finding and fixes it. The chain finishes, the trace has every step, and the
  MasterMind receives one delivery.
- **Slice 3:** a served e2e in which the client's daemon stops after the chain
  is accepted, the chain finishes on the server, and a later sync pulls every
  member round in order with the builder's commits. A server without `chain`
  refuses, and the error names the server and the feature.
