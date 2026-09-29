# A builder killed by systemd-oomd is re-queued, not failed

Fixes GitHub issue #584. Commit message prefix: `fix(headless):`.

## Hard rules for this round (read first)

- No test may run `systemctl`, `opencode` or any other harness binary. The
  new runner method is exercised only through `fakeRunner`. Never run
  `pkill`, `killall`, `kill` on processes you did not start, and never
  `tmux kill-server`.
- If a step is impossible as written, or the code contradicts this plan,
  **stop and report**. Do not improvise a different design.
- Code style: CLAUDE.md. No issue numbers or history in code or test names.
  Comments say *why*. Functions at most 70 lines; non-test files at most
  600 lines. Never add a lint or file-size exclusion.
- Any new `store.Binding` field changes its JSON shape. The rule in
  `internal/store/format.go` applies: bump `BindingFormat` by one and
  regenerate the golden with
  `go test ./internal/store/ -run TestBindingShapeMatchesFormat -update -count=1`.
  Do not change `recordFormat`.

## 1. System overview

On 2026-09-26 the laptop ran out of memory with several local rounds
running. `systemd-oomd` killed four `relevo-round-*.scope` units
(`Failed with result 'oom-kill'`). The relevo daemon was still alive, so it
saw each builder exit with code `unknown` and a process it had watched. It
treated that as the candidate's failure: a counted switch, plus
`RoundExcluded` for the rest of the round.

A host out of memory is not the candidate's fault. After this change, when
a round's builder exits with code `unknown` and its systemd scope's
`Result` is `oom-kill`:
- the round is **re-queued on the same candidate**: not counted, not
  excluded, no gate;
- the old harness session is marked abandoned (the existing
  `abandonSession`), and the round restarts fresh;
- the new process's prompt says an out-of-memory kill interrupted it, and
  that the worktree may hold its partial work.

**Local rounds** (`b.Owner == ""`) have no builder cap. They are
re-admitted by a new daemon phase, one round per tick, once both hold:
1. at least `oomCooldown` (60 s) has passed since the kill;
2. fewer local headless rounds are running than were running when oomd
   struck. That count is recorded at the kill.

**Served rounds** (`b.Owner != ""`) re-queue exactly the way a builder lost
to a restart already does. The serve admit loop starts them under
`serve.max_builders`.

A round whose builder is oom-killed for the `oomMaxKills`-th time (3) is
halted with NEEDS YOU instead of re-queued, so memory pressure that never
ends cannot loop forever.

Scopes off (`rt.Scope == nil`), or a runner that cannot report a scope
result, means detection answers "not oom-killed", and behaviour is exactly
as today.

## 2. What this round changes, and what it does not

Nothing is deleted. The oom check is a new branch taken **before** the
existing stop-request, lost-to-restart, escape, limit, denial, nudge and
switch branches of the exited-without-report path. Every existing branch
keeps its behaviour for every exit that is not an oom-kill. No existing
test's expected value should change, apart from the binding-shape golden.
If one does, name it in the report with the reason.

## 3. File structure

```
internal/spawn/spawn.go          + ScopeResultProber interface (next to ScopeProber)
internal/proc/scope.go           + (*Runner).ScopeResult
internal/proc/scope_test.go      + parse test for the Result value (pure helper only)
internal/store/binding.go        + Binding.OOMRequeue, Binding.RoundOOMKills
internal/store/format.go         BindingFormat +1 (and the comment's list of unstamped fields)
internal/store/testdata/binding-shape.golden   regenerated
internal/relevo/oom.go           NEW: detection, requeue, admission rule, the daemon admit phase
internal/relevo/oom_test.go      NEW
internal/relevo/headless.go      one call in the exited-without-report path
internal/relevo/queue.go         Admit: the oom note, and clearing OOMRequeue
internal/relevo/reconcile.go     reset RoundOOMKills next to RoundSwitches = 0 (line ~528)
internal/relevo/send.go          reset RoundOOMKills next to RoundSwitches = 0 (line ~566)
internal/relevo/daemon.go        admit phase in Tick
internal/relevo/fake_test.go     fakeRunner.ScopeResult (map-scripted, records queries)
docs/plans/2026-09-26-oom-kill-requeue.md   this plan, verbatim (last step)
```

## 4. Data structures

### `store.OOMRequeue` (internal/store/binding.go)
| field | type | json | meaning |
|---|---|---|---|
| `At` | time.Time | `at` | when relevo saw the oom-killed exit (UTC) |
| `Running` | int | `running` | local headless rounds that had a live process at that moment, **including** the killed one; always at least 1 |

### `store.Binding` new fields
- `OOMRequeue *OOMRequeue \`json:"oom_requeue,omitempty"\``. Non-nil only
  while the current round waits in the queue after an oom kill. `Admit`
  clears it.
- `RoundOOMKills int \`json:"round_oom_kills,omitempty"\``. The number of
  oom kills in the current round. It is reset to 0 wherever
  `RoundSwitches` is reset (reconcile.go ~528 and send.go ~566).

Update the `QueuedAt` field comment (binding.go ~120). It is no longer
server-only: a local round is queued after an oom kill.

### Constants (internal/relevo/oom.go)
- `oomCooldown = 60 * time.Second`
- `oomMaxKills = 3`
- `scopeResultOOM = "oom-kill"`, the systemd `Result` value.

## 5. Interfaces and contracts

### spawn (internal/spawn/spawn.go, after `ScopeProber`)
```
ScopeResultProber interface {
    // ScopeResult is the systemd Result of the scope unit <unit>.scope, e.g.
    // "success" or "oom-kill"; "" when the unit is unknown or systemctl is missing.
    ScopeResult(ctx context.Context, unit string) (string, error)
}
```
Callers type-assert `rt.Runner`, the same way they use `ScopeProber`. An
error means "could not tell" and is treated as not oom-killed.

### proc (internal/proc/scope.go)
`func (r *Runner) ScopeResult(ctx, unit) (string, error)`, modelled on
`ScopeActive` (line ~106):
- run `systemctl --user show --property=Result --value <ScopeUnitFileName(unit)>`
  with a 5 s timeout;
- a missing `systemctl` returns `("", nil)`;
- otherwise return the trimmed first line.

The trimming goes in a small pure helper with a unit test. The test does
not run systemctl.

### relevo (internal/relevo/oom.go)
- `func oomKilled(ctx context.Context, rt Runtime, b store.Binding) bool`
  - False when `rt.Scope == nil`, or when `rt.Runner` does not implement
    `spawn.ScopeResultProber`.
  - Otherwise asks `ScopeResult(ctx, scopeUnitName(b))`, and returns true
    exactly when the answer is `scopeResultOOM` with no error.
  - An error is logged at debug and returns false.
- `func localRunning(tx *store.Tx, self string) (int, error)` counts
  bindings from `tx.List()` that are local (`Owner == ""`), not remote
  (`!Builder.Remote()`), headless, `Builder.PID != 0`, with name `!= self`.
- `func requeueOOM(ctx context.Context, rt Runtime, tx *store.Tx, b store.Binding, now time.Time) (store.Binding, error)`
  - Precondition: the builder process is gone (the caller has not yet
    cleared PID; this function clears it).
  - Pseudocode in section 6.
- `func oomAdmissible(b store.Binding, running int, now time.Time) bool`, pure.
  - Requires all of: `b.Owner == ""`, `!b.QueuedAt.IsZero()`,
    `b.OOMRequeue != nil`, `b.State == store.StateActive`,
    `now.Sub(b.OOMRequeue.At) >= oomCooldown`, and
    `running < b.OOMRequeue.Running`.
- `func admitOOMQueued(ctx context.Context, rt Runtime, bindings []store.Binding)`
  - Runs in the daemon with no lock held.
  - Counts local running rounds with the same predicate as `localRunning`,
    over `bindings`.
  - Picks, among bindings where `oomAdmissible` is true, the one with the
    oldest `QueuedAt`.
  - Calls `Admit(ctx, rt, name)` for that one binding only.
  - Logs errors; returns nothing. `ErrNotQueued` is normal (another writer
    got there first) and is logged at debug.
- `oomNote(t time.Time) string`, next to `interruptedNote` in headless.go
  or in oom.go. Same shape as `interruptedNoteFormat`, but it says the
  round was interrupted at <t> because the host ran out of memory and
  systemd-oomd killed the builder, and that the worktree may already hold
  partial edits, which are its own work. It tells the builder to run
  `git status` and `git diff` first.

## 6. Pseudocode

### headless.go, exited-without-report path (starts at the `// Exited without a report.` comment, line ~757)
Insert right after the `suffix` computation and **before** the exit entry
is appended:
```
oom := codeText == "unknown" && oomKilled(ctx, rt, b)
if oom: suffix += "; killed by systemd-oomd (host out of memory)"
... existing AppendLog(exitEntry(...)) and slog line unchanged ...
... existing StopRequestedAt branch unchanged (a requested stop still wins) ...
if oom:
    return requeueOOM(ctx, rt, tx, b, now)
... everything after (lost, escape, gateOnLimit, denial, nudge, switch) unchanged ...
```

### requeueOOM
```
b.RoundOOMKills++
b = abandonSession(b)                       // the killed session must not be resumed by its harness
b.Builder.PID, b.Builder.StartedAt = 0, 0
b.Builder = clearProcess(b.Builder)
b.StalledSince = zero
if b.RoundOOMKills >= oomMaxKills:
    return haltBinding(ctx, rt, b, "<name>: builder killed by systemd-oomd (host out of memory) <n> times this round; free memory, then relevo send --name <name> --file <plan> again")
running := 1
if b.Owner == "":
    n, err := localRunning(tx, b.Name); if err: return b, err
    running = n + 1
b.QueuedAt = b.RoundStartedAt               // head of the queue, as the served lost path does
if b.QueuedAt.IsZero(): b.QueuedAt = now
b.RoundStartedAt = zero
b.OOMRequeue = &OOMRequeue{At: now, Running: running}
AppendLog KindQueue, DirToPlanner, Confirmed, Round b.Round,
    Note "re-queued (builder killed by systemd-oomd: host out of memory; <running> local round(s) were running)"
    (served: "re-queued (builder killed by systemd-oomd: host out of memory)")
slog.Info("headless builder re-queued after an oom kill", binding, round, running)
return b, nil
```
`haltBinding` is the existing function. Check its signature at its
definition and use it as the other call sites do.

### queue.go, Admit (line ~23)
Inside the lock, after `prompt := composePrompt(...)`:
```
if b.OOMRequeue != nil:
    prompt += "\n\n" + oomNote(b.OOMRequeue.At)
    b.OOMRequeue = nil
```
On the spawn-failure branch nothing changes: that branch already zeroes
`QueuedAt` and saves NEEDS YOU. `OOMRequeue` is nil by then, which is fine.

### daemon.go, Tick (line ~178, right after the `reap sessions` phase)
`d.safely("admit oom-queued", func() { admitOOMQueued(ctx, d.rt, fresh) })`

## 7. Error handling

- Detection failures (no scopes, no prober, a systemctl error) mean "not
  oom-killed". The exit then takes today's path unchanged.
- `requeueOOM` returns only errors from `tx` (`List`, `AppendLog`).
- `admitOOMQueued` never fails the tick. Admit errors are logged, and
  `Admit` itself already turns a spawn failure into NEEDS YOU.
- A requested stop still wins over the oom branch: the StopRequestedAt
  check stays before it.

## 8. Working efficiently

Each model step costs a round trip, so:
- First, read these in one batch of parallel reads:
  - `internal/relevo/headless.go` lines 80-115 and 740-860;
  - `internal/relevo/queue.go`;
  - `internal/relevo/daemon.go` lines 141-200;
  - `internal/relevo/abandon.go`;
  - `internal/relevo/fake_test.go` lines 600-740;
  - `internal/relevo/queue_test.go` (an Admit test to copy the setup from);
  - `internal/store/binding.go` lines 110-145;
  - `internal/store/format.go` lines 1-30;
  - `internal/spawn/spawn.go` lines 145-165;
  - `internal/proc/scope.go` lines 95-135;
  - `internal/relevo/reconcile.go` lines 515-535;
  - `internal/relevo/send.go` lines 550-570.

  Do not search for what this plan already located. Find `haltBinding`'s
  signature with one grep.
- Make all changes to a file in one edit where you can.
- Focused loop:
  `go build ./... && go test ./internal/store/ ./internal/proc/ -count=1 && go test ./internal/relevo/ -run 'OOM|Oom|Admit|Queue|Lost|Restart|Switch|Stop' -count=1`.
  Fix every error before rerunning.
- Then run `go test ./internal/relevo/ ./internal/serve/ -count=1` once.
- At the end, run `make check` once. If a hook refuses `make check` on this
  machine, run `go vet ./...`, `go test -count=1 ./...`,
  `sh scripts/check-comments.sh`, `sh scripts/check-filesize.sh` and
  `gofmt -l .` instead, and say so in the report.

## 9. Ordered implementation steps

1. **spawn + proc.** Add `ScopeResultProber` and `(*Runner).ScopeResult`,
   with its pure trim helper and a test for that helper.

   Done when `go test ./internal/proc/ ./internal/spawn/` passes.
2. **store.** Add `OOMRequeue`, `Binding.OOMRequeue` and
   `Binding.RoundOOMKills`; update the `QueuedAt` comment; bump
   `BindingFormat` and regenerate the golden.

   Done when `go test ./internal/store/ -count=1` passes without `-update`.
3. **fakeRunner.** Add `scopeResults map[string]string` and
   `scopeResultQueries []string` to `fakeRunner`, and a `ScopeResult` method
   that records the unit and answers from the map.
4. **oom.go.** Add `oomKilled`, `localRunning`, `requeueOOM`,
   `oomAdmissible`, `admitOOMQueued`, `oomNote` and the constants; add the
   `RoundOOMKills` resets in reconcile.go and send.go.

   Tests in oom_test.go pin:
   - `oomKilled` is false with scopes off (and never queries), false for
     `"success"`, and true for `"oom-kill"`;
   - `oomAdmissible` holds only at or after the cooldown, only with fewer
     running than recorded, only local, and only while active and queued;
   - `admitOOMQueued` admits exactly one binding, the oldest queued, and
     none while running is not below the recorded count.
5. **headless.go and queue.go call sites.** Tests pin, driving
   `reconcile` with a fakeRunner whose builder exited with no trailer and
   whose scope result is `oom-kill`:
   - a local round becomes queued (`QueuedAt` set, `RoundStartedAt` zero,
     `OOMRequeue.Running` equal to the other live local rounds plus 1);
   - `RoundSwitches` is unchanged, `RoundExcluded` is empty, and
     `BuilderCandidate` is the same;
   - a queue log entry is written, and the exit entry's note says
     `killed by systemd-oomd`;
   - an opencode round's session is in `AbandonedSessions`;
   - a served round is queued the same way;
   - the third oom kill in a round halts with NEEDS YOU;
   - a requested stop still closes the round as stopped;
   - the same exit with scope result `"success"` still takes today's
     path. Assert on the existing switch.
   - `Admit` of an oom-queued round puts `oomNote` text in the spawned
     prompt, and clears `OOMRequeue`.
6. **daemon.go phase.** A test pins that `Tick` with one oom-queued local
   binding past the cooldown, and no other running rounds, starts its
   builder. Before the cooldown, it does not.
7. **Mutation checks.** First make `oomKilled` always return false and
   confirm a named test fails. Then drop the `running <
   b.OOMRequeue.Running` condition and confirm a named test fails. Restore
   both. Name both tests in the report.
8. **Full check.** Run it as in section 8. Never lower a coverage baseline.
9. **Ship the plan.** Copy this file verbatim to
   `docs/plans/2026-09-26-oom-kill-requeue.md`. Commit everything in one
   commit, subject
   `fix(headless): a builder killed by systemd-oomd is re-queued on the same candidate, not failed`,
   with `Fixes #584` in the body.

## 10. Report

Include:
- the files changed;
- any existing test whose expected value changed, with the reason;
- the mutation checks and their failing tests;
- the check results;
- the commit sha.
