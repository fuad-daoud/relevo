# Reader actors

A **reader** actor is a binding, exactly as a writer is, but its round reads
and reports: relevo runs one headless process, it never edits the shared tree,
and its output lands in the round's artifact directory. That output is
`NNN-<actor>/<label>.md` -- the reader's final message -- named after the
actor's resolved output label (`plan.md` for the architect, `findings.md` for
the reviewer, `notes.md` for the researcher), plus any files it produced. A
round from before the rename answers `summary.md`, its old fallback name.

The MasterMind runs, from its own session:

```
relevo bind --no-feature --actor reviewer --name webshop
relevo send --name webshop --file q.md
```

relevo stages `q.md` as the round's prompt and runs the reader headless; its
**final message** becomes the round's output at `NNN-<actor>/<label>.md` and is
queued to the MasterMind like any report. `relevo show <name> --output` prints
the output file, `relevo show <name> --artifacts` lists the round's other files,
and the cockpit's artifacts tab shows both.

A bound reader round is the only way to get a reader's answer: the old
one-shot `relevo ask` consult is gone. `relevo show <name> --findings <id>`
still reads the findings of a consult recorded before that removal, and it is
how a verify reviewer's findings are read.

The process is the only thing relevo can observe: relevo kills one that
outlives its deadline, and a process that exits without a final message is
reported with its exit code and the stream to read. The resolved tier still
gates the pick: at `read`, claude and agy can run (claude `--permission-mode
plan`, agy `--mode plan`), while opencode and codex cannot honour `read` and
are refused.

While a verify reviewer is running, `relevo status` appends ` +Nc` to the
binding's row — only when non-zero, so a healthy binding looks no different. A
finished reviewer's record is dropped once its findings have been queued.
Terminal consults are not work in flight, so the count does not include them.

`relevo config agents` writes the reviewer definition with the other
agents; to install just this one:

```
relevo config agents --agent reviewer
```

`relevo doctor` reports whether the definition landed, on every kind.

Read-only is a property of the agent definition — the definition pins a
read-only tool set and the candidate's `tree` decides where it runs — not
something relevo enforces. On agy the definition's `tools:` allowlist makes it
a property the harness enforces: a write tool that is not listed is not
offered. The list is also load-bearing the other way: a definition with no
`tools:` gets no write or shell tool at all, and one naming a tool agy does
not have does not start -- so every agy definition relevo ships carries an
explicit, verified list, and `relevo doctor` warns when the installed agy
copy differs from it (on claude and opencode the copy is yours to edit, and
doctor leaves it alone). relevo cannot observe writes; it reports what is in a tree
and no more. Note also that `reviewer` is deliberately not the `researcher`
agent: `researcher` is dispatched by a builder's own plan-executor and returns
findings in-band to it, while a reviewer runs as its own relevo consult and
hands back a file path.

### Planner actors

A planner actor is a reader binding too: bind it, send it the task, and read
its plan back from the round's output.

```
relevo bind --no-feature --actor lite-planner --name plan-x
relevo send --name plan-x --file task.md
relevo show plan-x --output                                   # or the cockpit's artifacts tab
relevo send --name <builder> --file <the output path>
```

The plan is the reader round's output, `NNN-lite-planner/plan.md`. Review
it with `relevo show plan-x --output` (or the cockpit's artifacts tab), then
hand that output path to a builder with `relevo send --name <builder> --file
<the output path>`. Reviewing is a human (or MasterMind) step: relevo
sends nothing automatically.

### Round usage

At every round close relevo records what the round consumed on the
round's `report` event (and a consult's on its `findings`
entry): harness, provider, model, duration, tokens (`in`, `cache_read`,
`cache_write`, `out` -- `out` includes thinking), and a cost with its
provenance:

| `cost.basis` | meaning |
|---|---|
| `measured` | the harness reported dollars itself (claude's `total_cost_usd`, opencode's `cost`) |
| `estimated` | relevo multiplied the harness's token counts by the prices section |
| `unknown` | no record, no price row, or no way to read; `note` says which |

`unknown` is an answer, not a failure. Where each figure comes from:

| harness | headless |
|---|---|
| claude | the round's stream (`measured`) |
| agy | the round's stream (`estimated`) |
| opencode | the round's stream (`measured`) |

A binding on `--cwd` shares the MasterMind's directory, so its rounds
are `unknown` (`shared cwd`) rather than counting the MasterMind's spend.

The `prices` section is `{"as_of": "YYYY-MM-DD", "source": "...", "models":
{"<provider>/<model>": {"in": …, "cache_read": …, "cache_write": …,
"out": …}}}` in USD per million tokens, overlaid on the table relevo ships;
a model with no row is `unknown`, never `$0`. Mark a subscription lane
with `"plan": true` on its candidate: its rounds record `cost.plan` and
are shown as a quota draw, never as free.

Where you see it: `relevo show --log` prints the round line under each report
(`⎿ opencode/cline-pass/glm-5.3-flash  14m  in 2k  cache 166k (91%)  write 14k  out 12k  $0.41`);
`relevo status` adds a `usage` row (newest round) and a `spend` row
(the binding's total: `4 rounds +2c · $1.23 · ~$0.40 · 2 unknown · 2.1M tok`), both
on `--json` as `last_usage` and `spend`; `relevo ui` shows the total in
the fleet's SPEND column and in the round pane's `spend` row.

A remote builder's round is measured on the server, from the builder's
own stream there, and shipped with the round: the client keeps the
server's figure verbatim instead of reading a record it does not have. A
server built before this ships no figure, and its rounds print
`unknown · remote: server sent no usage`.

While a round is running, `relevo status` and `relevo ui` show a `live`
figure read from the harness's record on each refresh, and
`relevo status --line` appends `live $0.02 · 41k tok` to the row. On
`status --json` it is carried as `live_usage`. The live figure is
estimated (`~$`) unless the harness reports dollars per step (opencode).
It is never recorded and never added to `spend`. Across bindings:

```
relevo history --tab [--since 7d|24h|2026-09-01] [--by binding|model|provider|owner] [--json]
```

sums every round relevo has recorded, including bindings `unbind --done` has
archived, one row per group and a total, with the four token columns
`in`, `cache`, `write`, `out` (a sum across models has no meaningful
ratio, so `cache` carries no percentage). Measured and estimated dollars
never share a column; `plan` and `unknown` are counts of rounds. `--tab`
is the second exception to the #114 verb freeze, taken because its
sums exist regardless (they are on `status --json`) and a cross-binding
view has no other home.

### Usage stats

```
relevo history --stats [--since 7d] [--json]
```

`relevo history --stats` answers the questions `--tab`'s money sums do not. It reads
the same records as `relevo history --tab`, live and archived, locally only:
nothing leaves the machine. `--since` cuts on a round's start, so a round that
started before the cut is not counted.

Each counted round has one builder -- `harness/provider/model`, taken from the
report's usage record when it has one, else from the round's pick, else
`unknown` -- and one outcome:

| outcome | meaning |
|---|---|
| `done` | the report's relevo block said `done` |
| `halted` | the report's block said `halted` |
| `blocked` | the report's block said `blocked` |
| `deferred` | the report's block said `deferred` |
| `unstructured` | a report arrived without a usable block |
| `noreport` | the report was noted `noreport` |
| `stopped` | no report; the round was stopped |
| `exited` | no report; the builder exited |
| `open` | no report, no stop and no exit: the round is still open |

`needs-you` and `stalled` are not outcomes because they are live state, not a
property of a finished round: relevo records neither in the round log, so there
is nothing to count after the fact. They stay where they are visible, on
`relevo status` and in `relevo ui`.

`switches` counts the rounds that changed builder mid-round, attributes each to
the provider it left with a reason (`rate-limited`, `exited`, `gated`, `remote`
or `other`), and says how many of the rounds that was. `gate` counts pass, fail,
timeout and error over the rounds that ran a gate. `consults` counts consults
per actor with the findings' models beside them, plus the verifies that were
skipped.

`blocked` comes from the gate history in the database, which keeps 30 days: rate-limit and
spawn-failure events in that window, and the gates a human cleared with
`relevo gate --clear`. Only a gate cleared by hand has a recorded length -- an
expired gate keeps no expiry time, so it counts as an event with no duration.

### Reviewer candidates

The verify reviewer resolves the reviewer actor and picks from its `candidates`
list, by the same rule as `--candidate`.

In the actors section:

```json
"reviewer": { "agent": "reviewer", "candidates": ["opus"] }
```

Until the reviewer actor lists a candidate, the verify consult fails with
`no configured candidate serves actor "reviewer"`.

## The MasterMind: architect

`architect` is the planner actor's contract and a session persona. As the
planner actor's contract, `planner` and `lite-planner` run it as a reader round:
a seed goes in, and the plan comes out as the final message, saved as `plan.md`.
The contract is minimal -- the plan says the behaviour, the cases, the seams and
one-line ordered steps, and the planner never edits and never builds; a strong
builder owns the how.

As a persona, the same architect is the definition relevo ships for the session
you drive. The MasterMind is the session you drive -- the one you run `relevo
bind` and `relevo send` from -- and relevo does not pick its harness or start
it. What relevo provides is the definition, so the same architect runs on any
kind:

```
relevo config agents --agent architect
```

(`relevo config agents` with no flags writes it too.)

Then start the MasterMind with the harness's own `--agent` flag, for example:

```
claude   --agent architect --model opus
opencode --agent architect -m openrouter/deepseek/deepseek-v4-pro
```

Its agy copy pins `model: inherit` and a read-only tool set; the claude and
opencode copies leave `model:` unset so the launch line's flag decides. The
`relevo mastermind init` SessionStart hook injects the relevo guide, so a
MasterMind session on any agent receives it.

**Wait for the report after every send.** In Claude Code the MasterMind calls
the blocking `wait` tool for that binding and ends its turn: the call returns
with the report already in its output, and the `relevo mcp` send result
points at that tool, carrying the round's budget. Other
harnesses run `relevo wait <name> --timeout 9m` in a loop while it exits 124; the
wait prints the report, and they end their turn only when no binding has a round in
flight.

`architect` is not a relevo actor: it is absent from the actors section, so
`relevo bind --actor architect` is refused, and
`relevo doctor` does not check for it -- doctor reports only the role table's
definitions.
