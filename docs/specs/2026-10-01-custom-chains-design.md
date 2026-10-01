# Custom chains: a chain runs a workflow the user defines

**Issues:** #792 (custom chains), designed with #754 (sub-chains). This builds on
the chains spec (`2026-09-30-chains-design.md`, slices 1–3 shipped in #767–#790)
and replaces its fixed state machine with a workflow engine. The sketch beside
this file (`2026-10-01-custom-chains-sketch.svg`, editable as `.excalidraw`)
draws sections 2–5.

## 1. What changes and why

A chain today is one fixed workflow hard-coded in `internal/chain.Next`:

- plans are built, checked (with a regate) and reviewed;
- a `changes` verdict runs a correction plan;
- once, at the end, an optional security scan runs a fix plan.

The only knobs are which actor fills each of the four fixed parts, the gate, and
the correction budget.

relevo users already define their own agents, candidates and actors. A workflow
is the next thing they own. A user writes a chain of their own and keeps it
private (never shipped with relevo) or runs it once from a file. For example:

- a triage actor whose only output is `answer: yes | no`, gating whether a
  builder runs at all;
- two reviewers in a row;
- a chain with no builder.

The design aims to be **flexible yet stable**:

- **Flexible:** the user's imagination lives in their actors, and in the edges
  that wire one actor's outcome to the next step.
- **Stable:** relevo owns a small fixed set of step kinds, validates a workflow
  completely before anything starts, copies it into the chain at start, and runs
  every workflow — shipped or private — on one pure engine. The trace, the
  single delivery, `--resume`, placement and `--server` therefore work the same
  for all of them.

relevo still judges nothing. Every branch a chain takes is an actor's declared
outcome or a check's result. relevo only routes.

### 1.1 Decisions this spec rests on

These were made with the owner on 2026-10-01 and are recorded on #792.

1. A chain is a graph. Steps run actors, and edges match outcomes.
2. An actor **declares** the outcomes it emits. One declaration generates the
   prompt footer, the parse at the round's close, and the check at start.
3. Outcomes and artifacts are different things. Outcomes are small values a
   chain branches on; artifacts are files a later step reads.
4. Inputs are **named references** (`{{correct.plan}}`), checked at start.
5. Plans are not fixed in relevo. A chain may have no builder, so plans are an
   optional input list that a `for-each` step walks.
6. The check is its own step kind. Today's regate becomes an ordinary loop in the
   graph. The step kinds are `run`, `check`, `for-each`, `fork`, and `when` (a
   static branch on a param, needed so that `--no-security` stays a param of the
   default workflow). `done` and `halt` are targets, not steps.
7. A workflow resolves from a one-shot file first, then a saved private
   workflow, then a shipped one.
8. A chain runs one binding per actor. A chain has one tree, and its writer steps
   take turns on it.
9. Saved workflows live in `relevo.db`. A workflow file is YAML or JSON; JSON is
   what is stored.
10. One engine. Existing chain rows migrate to it, and the old `Next` is deleted.
11. The engine is an interpreted graph: a pure `Next` over data. Compiling a
    workflow into today's `Next`, or embedding a scripting language, were both
    rejected: the first cannot express custom shapes, and the second cannot be
    validated before it runs or resumed safely.

### 1.2 Out of scope

- A visual workflow editor.
- Expressions beyond equality and count comparisons.
- Steps that run arbitrary local code other than a `check` command.
- Sharing workflows between users or machines, other than through
  `relevo config export` / `import` and the `--server` create request.

## 2. Actors declare their outputs

An actor record gains `outputs`, beside its agent, shape, candidates and
placement.

```yaml
actor: yes-no
agent: yes-no
shape: reader
candidates: [haiku]
outputs:
  answer: { one-of: [yes, no] }      # an outcome: a value to branch on
```

An output is one of:

- **`one-of: [v…]`:** an enumerated outcome. An edge matches it with `key=value`.
- **`count`:** a non-negative integer outcome. An edge matches it with `key=0`
  or `key>0`.
- **`artifact`:** a file the runner writes. Its path is the reader's
  `<name>.md`, the reader-output label convention that exists today. A later
  step references it as `{{step.name}}`. Branching on an artifact is not allowed.

The one declaration drives three things.

1. **The prompt footer.** relevo appends the block the runner must end with,
   generated from the declaration:
   `answer: yes   # or: no`, or `findings: 0   # a count`. This replaces
   today's hard-coded verdict and findings footers.
2. **The parse at close.** Each declared outcome key is read from the round's
   relevo blocks: any block in the final message, then the stream's assistant
   messages, newest first. This is `chainReaderVerdict` generalized; a recap
   after the block stays harmless (#784).
   - A missing key, or a value outside `one-of`, closes the step with
     `status: halted`.
   - A declared artifact the runner did not write, or left empty, also closes
     the step halted (today's "planner wrote no plan").
   - The reason names the key or artifact, and what was found.
3. **The check at start.** Every edge must match a declared value, and every
   declared value needs an edge or an `else` (section 3.3).

The shipped actors gain declarations:

| Actor | Declared outputs |
|---|---|
| `reviewer` | `verdict: { one-of: [pass, changes] }`, `findings: artifact` |
| `security` | `findings: count`, `report: artifact` |
| `planner` / `lite-planner` | `plan: artifact` |
| `builder` (a writer) | none; a writer's outcomes are fixed (section 4.1) |

A writer may also declare outcomes. They are parsed from its report's relevo
block the same way.

## 3. The workflow format

### 3.1 The shipped default

Today's chain, as the shipped `default` workflow:

```yaml
name: default
description: plans built, checked and reviewed, then one security scan
inputs:
  plans: required          # none | optional | required
  task: none
params:                    # the only values a CLI flag changes
  builder: builder
  reviewer: reviewer
  planner: lite-planner
  security: security
  scan: true
  gate: make check
  regate: 1
  max_corrections: 3
start: plans
steps:
  plans:      { for-each: plans, on: { next: build, empty: scan-gate } }
  build:      { run: "{{params.builder}}", seed: "{{plans.current}}", on: { done: check } }
  check:      { check: "{{params.gate}}", on: { green: review, red: repair } }
  repair:     { run: "{{params.builder}}", seed: shipped:repair,
                budget: { max: "{{params.regate}}", per: [build, build-fix], then: review },
                on: { done: check } }
  review:     { run: "{{params.reviewer}}", seed: shipped:review,
                on: { verdict=pass: plans, verdict=changes: correct } }
  correct:    { run: "{{params.planner}}", seed: shipped:correct,
                budget: { max: "{{params.max_corrections}}", per: plans,
                          then: { halt: "reviewer still wants changes after {{params.max_corrections}} corrections" } },
                on: { done: build-fix } }
  build-fix:  { run: "{{params.builder}}", seed: "{{correct.plan}}", on: { done: check } }
  scan-gate:  { when: "{{params.scan}}", on: { true: scan, false: done } }
  scan:       { run: "{{params.security}}", seed: shipped:scan,
                on: { findings=0: done, findings>0: fix-plan } }
  fix-plan:   { run: "{{params.planner}}", seed: shipped:fix, on: { done: fix-build } }
  fix-build:  { run: "{{params.builder}}", seed: "{{fix-plan.plan}}", on: { done: fix-check } }
  fix-check:  { check: "{{params.gate}}", on: { green: fix-review, red: fix-repair } }
  fix-repair: { run: "{{params.builder}}", seed: shipped:repair,
                budget: { max: "{{params.regate}}", per: [fix-build, fix-rebuild], then: fix-review },
                on: { done: fix-check } }
  fix-review: { run: "{{params.reviewer}}", seed: shipped:review,
                on: { verdict=pass: done, verdict=changes: fix-correct } }
  fix-correct: { run: "{{params.planner}}", seed: shipped:correct,
                 budget: { max: "{{params.max_corrections}}", per: fix-plan,
                           then: { halt: "reviewer still wants changes after {{params.max_corrections}} corrections" } },
                 on: { done: fix-rebuild } }
  fix-rebuild: { run: "{{params.builder}}", seed: "{{fix-correct.plan}}", on: { done: fix-check } }
```

`when` is the one static branch, used only on params (section 3.4). The fix path
mirrors today's: security runs once, with no rescan, and a `changes` verdict on
the fixes runs the same correction loop with its own budget. The repair budget
resets on every builder round, as the regate budget does today.

### 3.2 A user's workflow

```yaml
name: triage-first
inputs: { task: required }
start: triage
steps:
  triage: { run: yes-no, seed: "Should we build this? {{task}}",
            on: { answer=yes: build, answer=no: { halt: "triage said no" } } }
  build:  { run: builder, seed: "{{task}}", on: { done: check } }
  check:  { check: make check, on: { green: done, red: { halt: "check red" } } }
```

### 3.3 Rules of the format

- **`name`** follows the actor name rules.
- **`inputs.plans`** and **`inputs.task`** say what `relevo chain` must be given:
  - `required`: refused without it;
  - `optional`: may be absent;
  - `none`: refused if given.
- **`params`** are named slots with defaults. Seeds, actors, check commands,
  budgets and `when` read them as `{{params.x}}`. Nothing else is
  parameterizable.
- **`start`** names the first step.
- **A step** has exactly one kind key (`run`, `check`, `for-each`, `fork`,
  `when`) and an `on` map.
- **`on` maps a match to a target.**
  - Matches:
    - `key=value`;
    - `key=0` and `key>0` for a count;
    - the bare shorthand of a kind's main outcome: `done` (`status=done`),
      `green`, `red`, `next`, `empty`, `joined`, `conflict`, `true`, `false`;
    - `else`.
  - A target is a step id, `done`, or `{ halt: "<reason>" }`.
  - The first match wins, in this order: an exact key match, then a count
    comparison, then `else`.
- **`status` on a `run` step.** It is always emitted (`done | halted | blocked |
  deferred`, from the round's report tail). A status other than `done` that the
  step does not match halts the chain with the runner's own reason, which is
  today's behaviour made explicit.
- **`budget: { max, per, then }`** counts visits to the step.
  - `per` is a step id, a list of step ids, or `chain`. The count resets
    whenever one of those steps is entered; for a `for-each`, that means each
    time it advances. Corrections reset per plan (`per: plans`), and repairs
    reset per builder round (`per: [build, build-fix]`), as they do today.
    `chain` never resets.
  - On the visit past `max`, the step does not run and the chain goes to `then`.
  - `max` may be a param.
- **`seed`** is one of:
  - an inline template;
  - `file:<relative path>`, resolved against the workflow file and embedded into
    the stored definition at save time;
  - `shipped:<name>`, relevo's own templates (`repair`, `review`, `correct`,
    `scan`, `fix`), resolved from the running binary.

  A writer's seed is its prompt. A seed that is exactly one reference to a file,
  such as `{{plans.current}}` or `{{correct.plan}}`, hands over that file's
  bytes, as a chain hands the builder its plan today.

### 3.4 References

`{{…}}` resolves to a **path** a runner can open. Content is never pasted in;
every path goes through `chainSeedInput`, so a sealed key is copied out first.

| Reference | What it resolves to |
|---|---|
| `{{task}}` | the task text (the only reference rendered inline) |
| `{{params.x}}` | the param's value (inline) |
| `{{plans.current}}` | the `for-each` step's current item |
| `{{plans.all}}` | a file listing every plan copy |
| `{{<step>.<artifact>}}` | that step's latest declared artifact |
| `{{<step>.report}}` | a writer's latest report |
| `{{<step>.diff}}` | a writer's latest round diff |
| `{{<step>.output}}` | a reader's latest final message |
| `{{<check>.log}}` | a check step's latest log |
| `{{<fork>.conflict}}` | the conflicted paths of the latest join |
| `{{chain.diff}}` | the chain base → the newest closed writer tree (the #787 whole diff) |
| `{{chain.base}}`, `{{chain.branch}}` | the chain's base commit and branch name (inline) |

The current item of a `for-each` named `plans` is `{{plans.current}}`; in
general it is `{{<for-each step>.current}}`. `when` takes `{{params.x}}` only,
so a branch decided at start can never depend on a runtime value.

### 3.5 Validation

A workflow is validated in full when it is saved, when a one-shot file is loaded,
and again when a chain starts, against the actors present at that moment. Every
failure names the step and the rule. A chain never starts on an invalid
workflow.

1. `start` and every target exist, and every step is reachable from `start`.
2. A step has exactly one kind. A `run` names an existing actor (after params),
   and a `for-each` names an input or a list artifact.
3. Every `on` match is a declared outcome of that step's actor or kind, and
   every declared enumerated value and both count arms (`=0`, `>0`) are
   covered, or there is an `else`.
4. Every reference resolves to a step that can run before the referencing step
   on every path from `start` (dominance on the step graph), or to a chain
   input the workflow requires.
5. Every cycle contains a `run` or a `check` step, and has a `budget` on at least
   one of its steps. No unbounded loop, and no control-only spin.
   A budget's `per` names existing steps, and its `then` is a valid target.
6. `inputs` agree with what the chain was started with.
7. A `fork` child's workflow resolves and validates under the same rules, with
   the child's inputs.
8. `when` reads only `{{params.x}}`, and that param is a boolean.

## 4. Step kinds

### 4.1 `run`: one round of an actor

- **A writer** works on the chain's one tree, on the chain's branch.
- **A reader** works in a throwaway copy of that tree, as readers do today.
- The rendered seed is the prompt. Verify is off for chain members, as today.
- **Outcomes:** the actor's declared outcomes, plus `status`.
- **Artifacts:**
  - for a writer: `report`, `diff` and `commits`;
  - for a reader: `output`, plus its declared artifacts.

### 4.2 `check`: a command on the chain's tree

- It runs through the existing gate runner: the `relevo-gate` scope, the
  timeout, and the lint `PATH`.
- It runs where the tree lives. For a writer placed on a server, that is the
  server (section 6.3).
- **Outcome:** `result: green | red`.
- **Artifact:** `log`.
- A chain with no writer checks its base tree.

Chain members no longer run a gate of their own. The chain runs every check as a
step, so today's builder gate and repair round become the `check` and `repair`
steps in the default workflow.

### 4.3 `for-each`: walks a list

- **The source** is an input list (`plans`), or a step's list artifact. For
  example, a planner that writes `round-1.md … round-N.md`, which the next step
  walks with `for-each: "{{phase0.rounds}}"`.
- **Outcomes:** `next` (exposing `{{<step>.current}}`) and `empty`.
- It needs no I/O and resolves inside `Next` (section 5).

### 4.4 `when`: a static branch

- It evaluates a boolean param.
- **Outcomes:** `true` and `false`.
- It resolves inside `Next`.

### 4.5 `fork`: sub-chains (#754)

```yaml
split: { fork: { each: plans.current-stage, workflow: default },     # one child per item
         on: { joined: check-merge, conflict: merge, halted: { halt: "a child halted" } } }
split2: { fork: { children: [ { workflow: default, plans: [r2.md] },
                              { workflow: review-only, task: "audit the API" } ] },
          on: { joined: next-step, conflict: merge } }
```

- **Each child is a full chain.** It has its own definition and inputs, its own
  branch cut from the parent's current tip, its own per-actor bindings
  (`<parent>.<child>-<actor>`), and its actors' own placement. Forks nest.
- **The step waits** until every child has ended. A child that halts lets its
  siblings run to their end.
- **Then relevo merges the children's branches** into the parent's branch,
  mechanically, in child order.
- **Outcomes:**
  - `joined`: every child finished, and the merge was clean;
  - `conflict`: the merge stopped, the tree holds the conflict, and
    `{{<fork>.conflict}}` lists the paths;
  - `halted`: a child halted. The reason is the first halted child's.
- **The workflow wires what follows.**
  - `joined` → a `check`.
  - `conflict` → a `run` of the builder whose seed names `{{split.conflict}}`,
    then check and review.

  relevo judges nothing. A check after a merge is a `check` step the author put
  there.
- **Resume.** A halted child is resumed by name (`relevo chain --resume --name
  <child>`). The parent's fork step picks the join back up when that child ends.
  `relevo stop` on the parent stops every running child.
- **Delivery.** Only the top-level chain delivers to the MasterMind. The parent
  consumes a child's end, as members' outputs are consumed today.

## 5. The engine

`internal/workflow` is a pure package with no I/O, table-tested like
`internal/chain`:

```go
func Validate(def Definition, env Env) []Problem
func Next(def Definition, s State, e Event) (State, []Action)
```

`Env` is what validation needs from the outside: the actor registry (each
actor's shape and declared outputs), the inputs given, and the shipped seed
names.

**State** is stored as JSON on the chain row:

| Field | Meaning |
|---|---|
| `Status`, `Reason` | running, halted, stopped or done |
| `At` | the step the chain is on |
| `Awaiting` | `{step, member, round}` for a `run`, `{step, check run}` for a `check`, or `{step, children}` for a `fork` |
| `Visits[step]` | the budget counters, each with the scope instance it was counted in |
| `Iter[for-each]` | `{index, items}`; `{{x.current}}` reads it |
| `Results[step]` | the latest `{round, outcomes, artifacts}` per step; references resolve from here |

**Events:**

- `step_closed`: step, member, round, the parsed outcomes, the artifact keys and
  the status.
- `check_closed`: step, run id, result and log key.
- `child_ended`: fork step, child, status and reason.
- `needs_you` and `stopped`, as today.

**Actions:**

- `send`: step, actor and seed. The caller renders the seed and fills in the
  round.
- `run_check`: step and command.
- `fork`: step and the child specs.
- `merge`: the fork step.
- `finish` and `halt`.

**Rules:**

- An event that does not match `Awaiting` changes nothing, so a replayed close
  can never advance a chain twice. The same holds for a terminal chain.
- `for-each` and `when` resolve inside `Next`. It walks control steps until it
  reaches a `run`, `check` or `fork`, or a target. Validation rule 5 guarantees
  the walk ends; a hard cap on steps per transition guards a bug anyway.
- A `budget` is checked on entering a step. A visit past `max` goes to `then`
  without running the step.
- **Resume.** It re-enters the halted step, and that step's budget scope resets.
  `--from <step>` re-enters elsewhere. If the halted `run` step's member has a
  newer closed round (a manual send), resume treats that round as the step's
  `step_closed` (today's "resume reviews the newer manual round", generalized).
  A stopped round is re-sent with its own staged prompt (#788).
- **The trace** keeps one row per transition, with a new `step` column, e.g.
  `review r2  verdict=changes → correct`.

## 6. Running a workflow

### 6.1 Members

- One binding per actor the workflow uses.
- The builder actor keeps today's name `<chain>`. Every other actor is
  `<chain>-<actor>`.
- All member bindings are created at start, all-or-none, because the graph is
  known. A fork's children get theirs when the fork runs.
- The longest actor name sets the chain-name length cap.
- A chain has one tree. Every writer member of a chain works on it, taking turns
  because a chain runs one step at a time. So chain members are exempt from the
  one-binding-per-tree refusal (`ErrCWDTaken`) among themselves, and only among
  themselves.

### 6.2 The daemon

`chainApply` stays the one driver under the state lock, generalized:

1. A member's round closes. Its outcomes are parsed against that actor's
   declared outputs, its artifacts are keyed, and a `step_closed` goes to `Next`.
2. Each resulting action runs in the same critical section.
   - **`send`** renders the seed (every reference becomes a path through
     `chainSeedInput`), then calls `sendChainRound`. A served member queues for
     `admit` (slice 3).
   - **`run_check`** starts a check run through the gate runner on the writer's
     tree, records the run per chain, step and visit, and stores its log as a
     round file. Its end becomes `check_closed`.
   - **`fork`** creates the child chains (rows plus members), all-or-none, and
     sends each child's first step.
   - **`merge`** merges the children's branches into the parent's branch,
     mechanically, and produces `joined` or `conflict`.
   - **`finish` and `halt`** are today's `chainTerminal`.

The sweep, the pending-send step for remote members (slice 2), the server-chain
guards and the pull (slice 3) carry over. They key on the chain row, not on the
fixed parts.

### 6.3 Remote writers, and `--server`

- **A check on a placed writer.** When a writer's tree is on a server (slice 2
  placement), its check runs there.
  - This needs `POST /v1/bindings/{n}/checks`, behind a new server feature,
    `check`.
  - The client refuses to start a placed chain that has a `check` step if the
    server lacks `check`. The refusal names the server and the feature.
- **A `--server` chain** carries the definition in `CreateChainRequest`, in
  place of `settings`.
  - The server validates it against its own actors and resolves `shipped:`
    seeds from its own binary.
  - New server feature: `workflow`.
  - For one release, a request carrying the old `settings` is still accepted and
    mapped onto the default workflow, so a client that has not upgraded can
    still start a server chain.

### 6.4 Surfaces

- `status` shows `chain  running  review r2 · plans 2/4`.
- The statusline collapses a fork's children under the parent.
- `show --trace` prints the step on every row.
- `relevo show <chain> --workflow` prints the definition the chain was started
  with.

## 7. CLI and configuration

`relevo chain` is unchanged for today's uses:

- `--plan`, `--reviewer-actor`, `--planner-actor`, `--security-actor`,
  `--security`, `--no-security`, `--gate`, `--no-gate`, `--regate` and
  `--max-corrections` set the params of the shipped `default` workflow.
- A flag the chosen workflow has no param for is a usage error that lists the
  params the workflow does take.

New flags:

| Flag | Meaning |
|---|---|
| `--workflow <name\|path>` | a saved or shipped name, or a YAML/JSON file (one-shot) |
| `--param key=value` | fills a workflow param; repeatable |
| `--task <text>` / `--task-file <path>` | the task input |
| `--dry-run` | loads and validates the workflow, then prints the resolved graph, each step's actor and placement, and every reference; starts nothing |
| `--resume --from <step>` | re-enters a halted chain at another step |

Configuration:

- `relevo config workflow add <file>` validates and stores a workflow, and
  `relevo config workflow rm <name>` removes it.
- `relevo config` lists workflows as `shipped` or `saved`.
- `relevo config export` and `import` carry a new `workflows` section.
- Actor `outputs` travel in the `actors` section. A saved workflow cannot shadow
  a shipped name unless it is added with `--force`, as actors do.

### 7.1 Updating a saved workflow

- `relevo config workflow edit <name>` opens the stored source in `$EDITOR`.
  - Saving validates it.
  - An invalid workflow reopens with its problems listed at the top as comments
    (`# step review: undeclared match verdict=maybe`), until it is valid or the
    user quits without changes.
- `relevo config workflow add <file>` refuses an existing name unless
  `--replace` is given, so a workflow is never overwritten by accident.
- `relevo config workflow show <name>` prints the source the user wrote,
  comments kept. `--json` prints the stored form.
- relevo stores the source text next to the parsed JSON, so `edit` and `show`
  round-trip the user's own file, not a re-encoding.
- A running chain keeps the copy it took at start. An edit applies to the next
  chain started with that name, and `relevo show <chain> --workflow` shows the
  copy a chain runs.
- The database keeps no version history. A user who wants history keeps the
  source in git and runs `add --replace`.
- The cockpit's Workflows settings section (#823) is built on these verbs.

## 8. Migration

One additive database migration.

- `chains` gains `workflow` (the stored definition), `state` (JSON) and `parent`.
- `chain_event` gains `step`.

Every existing row gets the shipped `default` definition, with its params taken
from its stored settings JSON. Its fixed columns are converted into `state`:

| Old `step` | New `At` |
|---|---|
| `building` | `build`, or `build-fix` / `fix-build` / `fix-rebuild` when the builder's staged prompt is the planner's plan (by phase and corrections) |
| `reviewing` | `review` |
| `correcting` | `correct` |
| `scanning` | `scan` |
| `planning-fixes` | `fix-plan` |

- `plan` becomes `Iter[plans].index`, and `corrections` becomes `Visits[correct]`
  in the current plan's scope.
- The awaited member is carried over, so a chain that is running when the daemon
  re-execs onto the new binary carries on without noticing.
- Halted and done rows keep their traces; old trace rows show no step.
- The fixed columns stay in the schema, unread, until a later migration drops
  them.

## 9. Testing

CI has neither a harness binary nor network access (CLAUDE.md).

- **`internal/workflow`:**
  - one table case per validation rule (section 3.5);
  - one case per kind and outcome for `Next`;
  - budget scopes resetting per `for-each` advance;
  - the replayed-close guard;
  - the control-walk cap;
  - parsing of YAML and JSON to the same definition.
- **Equivalence, before the old engine is deleted.** Every case in today's
  `chain.Next` table is replayed through `workflow.Next` with the shipped
  `default` definition, through a translation:
  - today's single `builder_closed`, which carries the gate result after the
    regate, becomes the new events: the build step's `step_closed`, its
    `check_closed`, and any repair rounds in between;
  - each other member close maps to its step's `step_closed`.

  The sequence of sends to each member, and the terminal status, must match.
  Halt reasons may be reworded, and the test lists every rewording. This is the
  proof that the default workflow is today's chain.
- **Outputs.** The prompt footer generated from each output kind, and the parse
  of each kind: present, missing, out of range, and a recap after the block.
- **Migration.** Old rows at every step convert and then continue. A row that is
  running when the daemon re-execs keeps its awaited round.
- **`make e2e`:**
  - `TestChainE2E` and the slice 2 and 3 e2es pass unchanged, on the default
    workflow;
  - a triage workflow with a fake yes/no reader takes both edges;
  - a fork with two children: one clean join, and one conflict wired to a merge
    round;
  - a `--server` chain running a custom workflow.
- **Mutation checks.** Each round's plan names its mutations and the test each
  must fail, as the chains rounds have.

## 10. Rollout

Each slice is built **by a relevo chain** (owner, 2026-10-01):

1. the `planner` (opus) writes the round plans;
2. the MasterMind reviews them;
3. the rounds run as `relevo chain --server zen`;
4. the MasterMind verifies the result;
5. one PR per slice.

Build chains run as server chains, because W2 onwards changes the chain engine
itself. A chain driven by zen's daemon is never upgraded under itself, and the
servers are redeployed only between chains.

| Slice | Contents | Risk |
|---|---|---|
| **W1: engine** | `internal/workflow`: parse (YAML/JSON), validate, `Next`; the shipped `default` definition; the equivalence test. Nothing wired. | none |
| **W2: wiring** | Actors declare outputs; the daemon runs workflows; `check` is a step; seeds use references; `--workflow`, `--param`, `--task`, `--dry-run`, `--from`; the `config workflows` section; the migration; the old `Next` deleted. | high: replaces the engine; the equivalence and e2e suites gate it |
| **W3: fork** | #754 sub-chains: `fork`, child chains, the merge, the parent and child surfaces. | medium |
| **W4: remote** | Checks on a placed writer (feature `check`); `--server` chains carry the workflow (feature `workflow`, with the old-client mapping). Server redeploy. | medium |

This spec ships in W1's PR, with the sketch.

## 11. Clarifications from W1 planning

The W1 planning round found these gaps. They are settled as follows.

1. **YAML is read as YAML 1.2** (`go.yaml.in/yaml/v3`). Under YAML 1.1 the key
   `on` and the values `yes`/`no` would decode as booleans. YAML is decoded,
   its map keys are made strings, and it goes through the same strict JSON
   decoder as a `.json` file.
2. **Rule 5 (budgets).** A cycle that passes through a `for-each` is bounded by
   the list's length. A budget whose `per` resets inside the same cycle does not
   bound that cycle. A cycle made only of control steps is always rejected.
3. **Halt reasons may read params**, and only params, like `when`.
4. **Artifacts are lists of keys.** A single file is a list of one. A `for-each`
   may walk any declared `artifact` output.
5. **`stop` is an action.** It mirrors the `stopped` event.
6. **A `check` whose command renders empty** routes as `green` inside `Next`,
   with no action and no log. This is how `--no-gate` works on the default
   workflow.
7. **The shipped seeds** are named `repair`, `review`, `correct`, `scan` and
   `fix`. The templates move from `internal/chain/seeds` in W2.
8. **Until W3**, entering a `fork` step halts the chain with "fork steps are not
   run by this engine". W1 parses and validates forks, and W3 adds the merge
   event and their execution.
9. **The migration mapping** (section 8) is by phase and corrections.
   - `building` maps to `build` / `build-fix` in the build phase, and to
     `fix-build` / `fix-rebuild` in the security phase.
   - `reviewing` and `correcting` in the security phase map to `fix-review` and
     `fix-correct`.
   - `Visits[correct]` is `corrections + 1` while the chain is at `correct`.
10. **`Visits` are plain counts.** Entering a step resets the count of every
    step whose `per` names it.
11. **Step-matching order on a `run` close:**
    1. A status other than `done` matches only `status=<s>` edges, or halts.
    2. Then declared exact matches, in sorted key order.
    3. Then count arms.
    4. Then `done`.
    5. Then `else`.
12. **`internal/workflow` owns `ValidName`** (the actor-name pattern).
