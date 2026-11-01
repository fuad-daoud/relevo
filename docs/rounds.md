# Headless builders

A builder is a process relevo runs, one fresh process per round; each
`relevo send` starts the harness's non-interactive form -- `agy -p …`,
`claude -p …`, `opencode run …` -- in the binding's tree with the round's
prompt, writes the harness's output -- stdout and stderr both -- to
`~/.local/state/relevo/<name>/NNN-runner.jsonl` beside the round's prompt and
report, and returns.
Those are the round's **open-round files**, in the binding's directory under
the state root. When the round closes, relevo seals them into the database in
the same transaction that closes the round -- the report text rides in the
MasterMind's payload -- and removes them, so the directory afterwards holds only
the files of a round still open.
The daemon renders the stream as it grows -- one line per
tool call (`● Bash go test ./...`), its result with the first line of what it printed (`  ⎿ ok: ok  github.com/… 0.4s`, `  ⎿ error: …`),
the builder's text, any denied permission, and the final answer -- so
`relevo ui`'s terminal tab and `relevo status` show
the round live, about two seconds behind. When the harness's stream carries
times, each rendered line opens with a plain-text stamp: the event's clock in
this machine's zone, then how long the step took (`12:41:03 +4.2s ● Bash go test ./...`). Between rounds the tab keeps
the last round's rendered output. The `.jsonl` is the raw record;
relevo never reads it for meaning, and `relevo show <name> --round N
--transcript` renders it for a human. A round from an older relevo version
keeps its `NNN-builder.log`, and the readers show that instead. The process
exits when it has written the report, or when
it fails; between rounds a headless binding has no process and is idle, not
broken. The completion marker is the contract: a process that wrote its report and
created `NNN-done`, then exited non-zero, has done its job. A process that
exits with a report but no marker closes the round too, flagged `unmarked`;
one that exits with neither is the "exited without a report" case below.

What this means in practice:

- **No dialogs.** The process runs with stdin closed. If a harness needs
  permission prompts answered, put its `--dangerously-skip-permissions`/`--auto`
  extra arg in the candidates section.
- **No memory across rounds.** Every round is a fresh process. relevo plans
  already carry their own context (worktree table, conventions, "stop rather
  than improvise"); headless makes that a hard requirement.
- **`relevo status`** shows `builder  headless  <kind>  <idle|working|exited N>
  pid P since HH:MM` and the log's last three lines as `log` rows.
  `relevo ui`'s terminal tab shows the round's rendered output.
- **Exit without a report** is logged as an `exit` entry (exit code and the
  log's last 20 lines) and the daemon switches builders, up to `max_switches`
  (a switch caused by a rate-limit gate is not counted); then
  `NEEDS YOU`. The one exception is a builder whose
  supervisor died with the daemon itself (a systemd restart, `kill -9` of
  the process tree): relevo tells that apart from a real builder death and
  relaunches the same candidate on the same round instead, uncounted.
- **`done` and `unbind` stop the process** if a round is running. A stop that
  fails is reported, and the binding is still done or unbound. The round budget
  never kills anything: it flags `NEEDS YOU` and leaves the process alone.
- **`relevo gate`** on the provider mid-round kills the running process
  and starts the next candidate on the same round.
- **opencode 2.x** headless builders launch `run` with `--standalone` (#256):
  each headless round gets its own private server instead of the one
  `opencode serve --service` shared by every `opencode run` on that machine,
  so a kill, `relevo done`/`unbind`, a `relevo stop`, or a mid-round switch
  stops the agent for real -- before the fix, the client process died but the
  agent session kept running inside the shared service, still editing the
  worktree relevo had already switched away from. `relevo doctor` notes the shared
  service (and, when readable, its session count from opencode.db) whenever
  `~/.config/opencode/service.json` exists. They also pass `--thinking`, so
  the model's reasoning reaches the transcript.
- **A round's session is a runner, never a MasterMind.** Every process relevo
  starts for a round carries exactly one `RELEVO_RUNNER=<binding name>` in its
  environment, and relevo strips its own identity variables
  (`RELEVO_MASTERMIND`, `RELEVO_PLANNER`, and a stale `RELEVO_RUNNER` inherited
  from the daemon) from every spawned child. The MasterMind consent hooks read
  the marker and stay silent for it, so a builder or reader session is never
  asked the repository consent question, never briefed and never registered as
  a MasterMind: it is there to do the round's work. The marker reaches every
  round process -- local and served, a fresh spawn, a resume after a daemon
  restart, a mid-round switch, a repair round and the admit of a queued round.
  Every round and gate process also carries `RELEVO_BASE=<base commit>` when
  the binding has one.

**Scopes.** A local headless round runs in its own transient systemd scope
named `relevo-round-local-<binding>-<round>` (an owned remote binding uses its
owner's id where `local` sits). It is a *sibling* of `relevo.service`, not a
child: `systemctl --user restart relevo` -- what `make service` does -- leaves a
running round alone instead of killing it, so a restart no longer looks to
relevo like a builder that "exited without a report". With no `scope` block
configured this is the whole change: a local headless builder moves out of
`relevo.service`'s cgroup into its own scope, with no quota and no memory cap --
only its location, and with it restart survival. The gate, a consult and the
verify reviewer each get a scope from the same template too, with a unit name
that says what it is: `relevo-gate-local-<binding>-<round>`,
`relevo-consult-local-<binding>-<round>-<consult-id>` and
`relevo-verify-local-<binding>-<round>-<consult-id>` (an owned remote binding
uses its owner's id where `local` sits, exactly as for a round). The policy section's
top-level `scope` block configures the scope (`enabled`, `slice`, `cpu_weight`,
`cpu_quota`, `gate_cpu_quota`, `memory_max`, `tasks_max`); `gate_cpu_quota` is
the gate's own CPU ceiling, and defaults to `cpu_quota`. `serve.scope` replaces
that block entirely for served rounds, and `scope: {"enabled": false}` opts
out. `allowed_cpus` is a pool of cores (`"0-2"`), and each round is pinned to
one core from it: the gate runs on its round's core, while a consult and the
verify reviewer run on the whole pool. When every core is taken, a round runs
on the whole pool instead. Pinning needs `cpuset` delegated to your user
manager through a root drop-in on `user@.service`, and `relevo doctor` checks
this; if systemd refuses it, relevo logs one warning and runs unpinned. #314
measured a CPU-bound job pinned to one core using 10–18% less CPU time than the
same job left to float. Every scoped builder, gate, consult and verify reviewer
also gets `GOMAXPROCS` set to the CPUs its scope allows -- 1 for a pinned round,
`ceil(quota)` otherwise -- replacing any `GOMAXPROCS` the daemon inherited, such
as a shell-wide export; this affects Go processes only, and Go's `-p` and
`-parallel` follow it. On a host without a
usable systemd user manager relevo logs one warning and runs builders unscoped,
in `relevo.service`'s cgroup, exactly as before.

## Progress labels

relevo keeps a progress clock on every local binding with an open round and
labels what it sees. The labels are observations, never actions: relevo never
kills, nudges, switches or halts on them, and the round budget stays the only
automatic halt. Killing a stalled builder stays the human's decision -- `relevo
done`, `relevo unbind`, or `relevo stop`.

The signals are the working tree (its fingerprint is `HEAD` plus `git status
--porcelain`, hashed -- no diff, no snapshot) and the builder's output: the
builder's stream file (`NNN-runner.jsonl`) mtime. Each signal keeps the time it last changed, and the daemon samples at
most once every `progress_interval_ms` (default thirty seconds).

- **`stalled <age>`** -- no signal has moved for `stall_after_ms` (default
  fifteen minutes) while the round is open and the builder is not blocked. The
  label replaces `working` in `relevo status` and `relevo ui`; a stalled binding
  is still `ACTIVE` with `relevo wait` still waiting. The label clears when a
  signal moves again or the process exits, and the daemon fires one
  `builder_stalled` hook event per episode and none when it clears.
- **`exploring <age>`** -- the output or screen is changing but the tree has
  not for `explore_after_ms` (default twenty minutes), or for ten minutes after
  relevo resumed the round's session. A label only: some plans are read-heavy,
  so it fires no hook event and no notification.
- **`model silent for <N> min`** -- an opencode builder's stream has shown no
  event for `silent_after_ms` (default eight minutes) while its process tree
  burned almost no CPU, so a long foreground command never reads as silent.
  relevo aborts the step and resumes the same session once; a second silence or
  transport error in that session switches candidate.
- **`stale <age>`** -- a `NEEDS YOU` binding has sat unacted for
  `stale_after_ms` (default four hours). The age is measured from the halt, or
  from the newest log entry when the binding has none. The word follows the
  state word in `relevo status`, joins the row in `relevo ui`, and puts the row
  first inside its attention group; the daemon fires one `binding_stale` hook
  event per episode.

A binding with no readable signal at all -- no git and no stream -- is never
labelled.

When a round stops -- exit without the marker or the round budget -- relevo
scans the builder's last output for the harness's
rate-limit text and records a `rate_limited` gate (`source relevo`, the
matched line) on a match, parsing the line's own reset time when it names
one (`Resets in 2h48m52s`, `resets 7pm`) or using `limit_gate_default_ms`
otherwise, then switches uncounted toward `max_switches`. `relevo
gate` still overrides; `relevo gate --clear` undoes a false positive.

## Round budget

Each round carries a budget; past it, relevo flags the binding `NEEDS YOU` and
notifies once. It never kills anything — a builder working a real stage of a
plan runs for hours, so the budget is a runaway guard, not a progress estimate.
The default is 24 hours; `relevo bind --no-feature --timeout 2h` sets it per binding.

A headless builder that exits without writing its report file still closes the
round, and relevo delivers its report flagged `unmarked`. Nothing is scraped and
nothing is guessed.

## Running several builders at once

`relevo bind` gives the MasterMind one builder over the current tree. `relevo bind --worktree`
attaches more, each on its own git worktree, so they never contend for files:

```
relevo bind --no-feature --candidate claude/anthropic/sonnet --name api
relevo bind --worktree --no-feature --name frontend --candidate claude/anthropic/sonnet
relevo bind --worktree --no-feature --name backend  --candidate opencode/openrouter/z-ai/glm-5.3-flash

relevo send --name frontend --file ui_plan.md
relevo send --name backend  --file api_plan.md
```

Each peer is an ordinary binding: its own round counter, round log, captured
diffs and budget. `relevo status` lists them all, and every verb that acts on a
binding takes `--name`.

Every mutating verb (`bind`, `send`, `done`, `unbind`) ends by
listing, on stderr, every *other* binding that is waiting on a human — a halt,
a dead builder, a lost MasterMind — with how long and the verb that resolves it,
e.g. `waiting on you: api round 4 halted 23m -- relevo status --name api`. The
exit code is unchanged; it is a reminder, not a refusal.

Relevo does not sequence them and does not merge their trees. The MasterMind
decides how many builders it needs, which run in parallel and which wait, and
integrates the results — relevo only carries plans out and reports back.

Headless builders are the cheap way to run several: no terminal per builder,
no idle harness holding memory. `relevo bind --worktree --no-feature --name api` gives a peer its own
worktree and a fresh process per round.

## Chains

`relevo chain` runs a build, review and correct loop across a list of plans:
it starts a builder member per plan, reviews each round, and opens correction
rounds up to the policy cap before asking for a human. `relevo status --chains`
watches the chain and its steps instead of the members:

```
relevo chain --name shop --plan plan-a.md --plan plan-b.md --no-feature
relevo status --chains
```

A `--workflow` runs a saved workflow instead of the shipped
build-review-correct graph; `relevo config workflow` lists, adds and edits the
saved ones. `relevo help --json chain` lists every flag.

## Stopping a round

`relevo stop` ends an open round on purpose. The builder runs with no stdin, so
there is nothing to ask it: `relevo stop` kills the process and closes the round
without a report (`noreport stopped`).

```
relevo stop webshop               # end the round now
```

- **A stop is not a failure**, so it never charges a switch and never excludes
  a candidate, unlike an exit-without-report.
- **`stop` then `done`** is the sequence for parking a binding between
  rounds: `stop` ends the round, and `done` then releases the worktree.
- **Send and the round close clear the request**, so a stop never outlives
  the round it was made for. `stop` refuses a binding that is already `DONE`
  or `PAUSED`.

A remote binding's round is stopped on its server, and the binding stays: on
both sides only the round ends. A round still queued on the server is dropped
from the queue instead, since there is no process to kill. A server that
predates this refuses the request with a pointer to `relevo unbind webshop`,
which stops the round and drops the binding.

## Cleaning up finished bindings

A binding's live state is a record in the database, not a directory of files.
While a round is open that round's files sit in `$XDG_STATE_HOME/relevo/<name>/`
(defaulting to `~/.local/state/relevo/<name>/`) -- its prompt, report,
patch (`NNN-diff.patch`), captured dialog and builder stream -- and when the
round closes relevo seals them into the database and removes them.

`unread`/`●new` comes from the binding record's `viewed_at` stamp (#143):
`relevo show` (`--prompt`, `--diff`, `--log`) each stamps it after a successful
print of a live binding, and `status` compares the binding's newest report
against it. `relevo ui` never writes it directly -- it stamps through the same
call `show` uses, keeping `ui` itself read-only. `relevo done` stops relaying and, when the binding's worktree is clean and
no round is open, removes the worktree so its branch can be checked out
in the main repo (`removed worktree ... (branch relevo/x is free to check
out)`); a dirty tree or an open round is kept and `relevo unbind --done` retries
when it is clean. The binding directory itself is never removed by `done`
— the log is the record of what the MasterMind actually told the builder.
`relevo bind --resume <name>` puts a removed worktree back on the same
branch at the same path; a DONE binding may then be rebound with
`--rebind`, since the old builder cannot work in the recreated directory.

relevo deletes a branch in two places:
- `unbind --done` and `unbind --sweep`, for a `relevo/<name>` branch relevo created, only when its commit is on a remote-tracking ref;
- the existing `bind --server` rollback exception, which stays (a `relevo/<name>` branch created seconds earlier and undone because the server refused the binding).

A branch adopted with `--branch` is never deleted; `unbind`, `done` and a kept (dirty) worktree never delete anything.

```
relevo unbind ai              # delete the binding and its whole directory
relevo unbind ai --archive    # keep the binding's record, log and rounds in the database
relevo unbind --done          # archive this MasterMind's DONE bindings
relevo unbind --done --delete # remove them instead
```

Archiving is the default because every other destruction decision in relevo keeps
by default. An archived binding keeps every row it had -- its rounds, its events,
its artifacts and its transcripts -- so `relevo history`, `relevo show` and the
ui's `all` scope still read it months later, and the name frees for a fresh
binding.

`unbind --done` only touches bindings the MasterMind marked `DONE`. A `PAUSED` or a `BROKEN`
one is left alone: it still needs a
human, and clearing it would throw away the state that explains why it stopped,
while a paused binding is released but alive — `relevo bind --resume` brings it
back, and `relevo unbind` is how to clear it.

Snapshot tree objects created during round diff capture are written directly to
git's object database unreferenced. They never alter repository refs, branches,
or the working index, and they are reclaimed automatically by the repository's
own `git gc`.

**What a binding records.** Beyond its round history and live state, a fresh
`bind` fills in four more facts about the binding: which
repository it works in (the origin URL, normalised, and the git common
directory — best-effort, so a directory git can't read leaves this blank
rather than failing the command), the `--feature` label grouping it with
other bindings (or that it serves none), the `--ticket` issue it serves
(stored as `#N`, `owner/repo#N`, an issue key like `ABC-123`, or an http(s)
URL stored whole), and which
binding and round it was forked from. None of this
changes what you see day to day; it exists for `relevo history`, the ui's
dashboard and the database below.
