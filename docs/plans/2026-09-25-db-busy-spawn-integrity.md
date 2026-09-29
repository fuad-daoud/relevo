# A busy database no longer orphans builders or leaves branches behind (#436, #437, #445)

## 1. System Overview

Several processes share `relevo.db`: the daemon, every planner's CLI and MCP
calls, hooks. SQLite allows one writer at a time. Today three things go
wrong when a writer waits longer than `busy_timeout(5000)`:

1. **The daemon holds the write lock across subprocesses.**
   `ingest.Ingest` (`internal/ingest/ingest.go:158`) opens one `BEGIN
   IMMEDIATE` transaction per binding. Inside it, it calls `repoRefFromGit`,
   which runs `git`, and `deps.Sessions`, which searches the disk for a
   planner transcript. The daemon runs this back to back over every live
   binding, outside the store's flock. Other writers starve and get
   `db: tx begin: busy`.
2. **`relevo send` spawns before it records (#436).** In `Send`
   (`internal/relevo/send.go`), `startRound` starts the builder process
   *before* the plan log entry and the `tx.Save` that stores its pid. A busy
   error in those writes returns an error with the builder already running.
   It is untracked, and nothing kills it.
3. **A retried send collides with the orphan (#445).** The round's systemd
   scope unit name is fixed per round (`scopeUnitName`,
   `internal/relevo/headless.go:77`). The retry's `systemd-run` refuses the
   duplicate unit name and exits at once. Its pid is the one recorded. The
   daemon then halts the binding with "builder exited without a report"
   while the orphan works on.
4. **`relevo bind` does not roll back its branch (#437).** `Add`'s
   `rollback` (`internal/relevo/add.go:268-272`) removes the worktree it cut
   but keeps the `relevo/<name>` branch it created. The retry then fails
   with "branch already exists".

This round makes four changes:
- (A) `db.Tx` retries `BEGIN IMMEDIATE` on busy, up to a bounded deadline.
- (B) Ingest does its git and session I/O before it opens the transaction.
- (C) `Send` kills a builder it spawned when the rest of the send fails, and
  refuses to spawn when the round's scope unit is already running.
- (D) `Add`'s rollback deletes the branch it created.

This round deletes no behaviour.

## 2. File Structure

```
internal/db/db.go                 MODIFIED  busy_timeout from a package var; Tx retries BEGIN IMMEDIATE on ErrBusy until beginRetryFor
internal/db/db_test.go            MODIFIED  two tests for the BEGIN retry
internal/ingest/ingest.go         MODIFIED  repo facts + planner locator resolved before d.Tx
internal/ingest/ingest_test.go    MODIFIED  one test: GitFacts and Sessions run with no write lock held
internal/relevo/runner.go         MODIFIED  new optional interface ScopeProber
internal/relevo/headless.go       MODIFIED  new sentinel ErrScopeActive
internal/relevo/send.go           MODIFIED  scope guard before startRound; kill-on-failure after it; test seam
internal/relevo/send_test.go      MODIFIED  two tests
internal/relevo/fake_test.go      MODIFIED  fakeRunner implements ScopeProber
internal/relevo/add.go            MODIFIED  rollback deletes the branch Add created
internal/relevo/add_test.go       MODIFIED  one test
internal/proc/scope.go            MODIFIED  (*Runner).ScopeActive via systemctl --user show
docs/plans/2026-09-25-db-busy-spawn-integrity.md   NEW  this plan (last step)
```

Nothing else changes. If you need to touch another file, halt and report.

## 3. Data Structures & Type Definitions

**`internal/db/db.go`: two unexported package vars** (tests override them):
- `busyTimeoutMS int = 5000`. `Open`'s DSN (line 55) is built from it, not
  from a literal `5000`.
- `beginRetryFor time.Duration = 30 * time.Second`. This is the total time
  `Tx` keeps retrying a busy `BEGIN IMMEDIATE`.

**`internal/relevo/headless.go`: a new sentinel next to `ErrBuilderBusy`
(line 28):**
- `ErrScopeActive = errors.New("this round's builder scope is still running")`

**`internal/relevo/send.go`: a test seam, unexported:**
- `var sendAfterSpawn func(name string) error`. It is nil in production. When
  non-nil, `Send` calls it right after `startRound` succeeds, and a non-nil
  return fails the send from that point. It exists only so a test can inject
  a post-spawn failure. Its doc comment says so.

**`internal/relevo/fake_test.go` `fakeRunner` (line 620): two new fields:**
- `scopeActive map[string]bool`: the answer per unit base name. A missing
  key is false.
- `scopeQueries []string`: the unit names asked for, in order.

## 4. Interface Definitions & Component Contracts

**`relevo.ScopeProber`** (new, `internal/relevo/runner.go`, next to
`Runner`). Its single responsibility is to say whether a systemd scope unit
is still occupying its name.
```
type ScopeProber interface {
    // ScopeActive reports whether the scope unit <unit>.scope is loaded and not
    // yet gone: ActiveState is active, activating, deactivating or reloading.
    // unit is the base name scopeUnitName returns (no ".scope").
    // An error means "could not tell"; callers treat it as not active.
    ScopeActive(ctx context.Context, unit string) (bool, error)
}
```
The `Runner` interface itself is **not** changed. Callers type-assert
`rt.Runner.(ScopeProber)`.

**`(*proc.Runner).ScopeActive`** (`internal/proc/scope.go`, unix build like
`ScopeArgv`'s callers; scope.go has no build tag, so it compiles
everywhere):
- Runs `systemctl --user show --property=ActiveState --value
  <ScopeUnitFileName(unit)>` with a 5 s context timeout.
- `systemctl` missing (`exec.ErrNotFound`) → `(false, nil)`.
- Any other command error → `(false, err)`.
- Trimmed output is `active`, `activating`, `deactivating` or `reloading` →
  `true`. Anything else (`inactive`, `failed`, empty) → `false`.
- Add `var _ relevo.ScopeProber = (*Runner)(nil)` in `internal/proc/proc.go`
  next to the existing `var _ relevo.Runner = (*Runner)(nil)`. That file is
  unix-only. If `proc_other.go`'s `Runner` then fails to build on !unix,
  because scope.go's method is on the shared type name, keep the method in
  scope.go only. It is a plain method on `*Runner`, and both builds define
  `Runner`. Verify with `GOOS=windows go vet ./internal/proc/`.

**`fakeRunner.ScopeActive`** (`internal/relevo/fake_test.go`): appends `unit`
to `scopeQueries` and returns `scopeActive[unit], nil`.

**`db.(*DB).Tx`**: the contract is unchanged, with one addition. When `BEGIN
IMMEDIATE` fails with `ErrBusy`, it is retried until `beginRetryFor` has
elapsed since the first attempt. Between attempts it sleeps a random 25–100
ms. `fn` is never run until `BEGIN` succeeds, so it still runs at most once.
After the deadline, the error is exactly today's:
`fmt.Errorf("db: tx begin: %w", ErrBusy)`. A non-busy BEGIN error returns at
once, unretried. COMMIT is not retried. `migrate.go`'s own `BEGIN IMMEDIATE`
(line 143) is untouched.

## 5. High-Level Pseudocode

**A. `db.Tx`** (`internal/db/db.go`, lines 216-240):
```
conn := sqlDB.Conn(ctx)
start := now
loop:
    err := conn.Exec("BEGIN IMMEDIATE")
    if err == nil: break
    mapped := mapBusy(err)
    if not errors.Is(mapped, ErrBusy) or now - start >= beginRetryFor:
        return "db: tx begin: %w", mapped
    sleep random 25..100ms
... unchanged fn / ROLLBACK / COMMIT
```

**B. `ingest.Ingest`** (`internal/ingest/ingest.go`):
```
before d.Tx (after `kind, _ := src.Origin()`, line 155):
    ref := b.RepoRef                                  // moved verbatim from lines 167-173
    if ref == nil && deps.Git != nil { ...repoRefFromGit(CWD), then Repo... }
    plannerLocator := b.Planner.TranscriptLocator     // moved from lines 436-441
    if plannerLocator == "" && deps.Sessions != nil && b.Planner.SessionID != "" && kind == "live":
        if p, ok := deps.Sessions(b.Planner.Kind, b.Planner.SessionID); ok: plannerLocator = p
inside d.Tx:
    repo section uses the precomputed ref (no git call)
    planner transcript section uses plannerLocator (no Sessions call);
    the `os.Stat(locator)` and everything after it stay where they are
```
The guard on the planner transcript section (`plannerID != nil && kind ==
"live"`) is unchanged. `plannerID != nil` holds exactly when
`b.Planner.SessionID != ""`, which is why the precompute tests `SessionID`.

**C. `Send`** (`internal/relevo/send.go`, inside the `WithLock` closure and
after it):
```
declare before WithLock:  var spawned *ProcHandle
inside the closure, in `if !deferred {` and BEFORE startRound (currently ~line 390):
    if rt.Scope != nil:                                 // scopes on; else no unit can exist
        if p, ok := rt.Runner.(ScopeProber); ok:
            unit := scopeUnitName(b)
            active, perr := p.ScopeActive(ctx, unit)
            if perr != nil: slog.Debug("scope probe", "unit", unit, "err", perr)
            if active:
                return fmt.Errorf("binding %q round %d: scope %s.scope is still running -- a builder for this round is already alive (an earlier send may have started it); inspect it with systemctl --user status %s.scope, and relevo stop %s ends it: %w",
                                  name, b.Round, unit, unit, name, ErrScopeActive)
                // nothing written: the plan file staged above is removed first (os.Remove(planPath)),
                // exactly as the ErrTierUnsupported branch does
    started, err := startRound(...)          // unchanged, including its error branch
    ...
    b = started
    h := handleOf(started.Builder); spawned = &h
    if sendAfterSpawn != nil:
        if err := sendAfterSpawn(name); err != nil: return err
after WithLock:
    if err != nil:
        if spawned != nil:
            kerr := rt.Runner.Kill(context.WithoutCancel(ctx), *spawned)
            appendLogMarker(rt.Store.BuilderLogPath(name, round-of-spawn), rt.Now(),
                            "send failed after spawn; builder stopped: " + err.Error())
            if kerr != nil:
                return SendResult{}, fmt.Errorf("%w; and stopping the builder it started (pid %d) failed: %v", err, spawned.PID, kerr)
            return SendResult{}, fmt.Errorf("%w; the builder it started (pid %d) was stopped", err, spawned.PID)
        return SendResult{}, err                      // today's path
```
For the round number in the marker, capture `spawnRound := b.Round` when you
set `spawned`. Use `appendLogMarker`, which already exists and is used in
Send at the builder-changed marker. Keep the `%w` on `err`, so
`errors.Is(err, db.ErrBusy)` still holds for callers.

**D. `Add`'s rollback** (`internal/relevo/add.go`, lines 268-272):
```
createdBranch := (the else-branch at ~line 244, which set branch = "relevo/"+name and ran AddWorktree successfully)
rollback := func() {
    if worktree != "" && rt.Git != nil { RemoveWorktree(...) }       // unchanged
    if createdBranch && rt.Git != nil { _ = rt.Git.DeleteBranch(ctx, opts.Repo, branch) }
}
```
Set `createdBranch = true` only after `AddWorktree` returns nil in that
else-branch. The `--branch` path (`existingBranch = true`) and the `--cwd`
path never set it, so an existing branch is never deleted.
`DeleteBranch` is idempotent on a missing branch
(`internal/git/client.go:467`).

## 6. Error Handling Strategy

- `ErrBusy` is recoverable at BEGIN only, through the bounded retry. After
  the deadline it propagates exactly as today.
- A post-spawn send failure is not recoverable in-process. The builder is
  killed, the log says so, and the returned error names the pid and both
  failures. The binding is not saved, because the closure's writes failed.
  The previous state stands and the planner may resend.
- `ErrScopeActive` means nothing was spawned and nothing was saved. Its text
  tells the human how to inspect the unit and how to stop it. It is **not** a
  spawn failure: it never reaches `recordSpawnFailureLocked` and never sets
  NEEDS YOU, because it returns before `startRound`.
- A `ScopeActive` probe error is logged at Debug and treated as not active.
- A branch-delete failure during rollback is ignored, like today's
  worktree-removal failure. The original error is what is returned.

## 7. Working Efficiently

Each model step costs a round trip, so:
- Read these in one step, as parallel reads: `internal/db/db.go` 40-60 and
  200-245; `internal/db/db_test.go` 1-60 and 236-290;
  `internal/ingest/ingest.go` 120-200 and 425-460;
  `internal/ingest/ingest_test.go` 640-735; `internal/relevo/send.go`
  296-525; `internal/relevo/fake_test.go` 600-720;
  `internal/relevo/send_test.go` 1-60 and 391-490;
  `internal/relevo/add.go` 240-360; `internal/relevo/add_test.go` 1-45 and
  299-320; `internal/relevo/runner.go` 120-150; `internal/proc/scope.go`.
  Do not search for anything this plan already locates.
- Make every change to one file in a single edit call.
- Focused checks:
  - `go test ./internal/db/ -run 'TestTx' -count=1`
  - `go test ./internal/ingest/ -count=1`
  - `go test ./internal/relevo/ -run 'TestSend|TestAdd' -count=1`
  - `go build ./... && GOOS=darwin go vet ./internal/proc/ && GOOS=windows go vet ./internal/proc/`
- Full check, once, at the end: `make check`. If this machine blocks heavy
  commands, use `dev run make check`. If `dev run` fails because `dist/` or
  `.git` is missing on the mirror, say so in the report and rely on the PR's
  CI.
- CI runners have neither a harness binary nor systemd user sessions nor
  network. No test may run `systemctl` or `systemd-run`. The send tests use
  `fakeRunner`, and `(*proc.Runner).ScopeActive` gets no test of its own.

If any step is impossible as written or contradicts the code, stop and
report. Do not bend the plan or a test to fit.

## 8. Ordered Implementation Steps

**Step 0: sync.** `git fetch origin && git merge --ff-only origin/main`.
Confirm these anchors still hold, and halt if any is gone:
- `db.go` has the DSN literal `busy_timeout(5000)` in `Open`, and
  `"BEGIN IMMEDIATE"` in `Tx`.
- `ingest.go` calls `repoRefFromGit` and `deps.Sessions(` inside
  `d.Tx(func`.
- `send.go` has `started, err := startRound(ctx, rt, tx, b, text)` inside
  `rt.Store.WithLock`.
- `add.go` has a `rollback := func()` that only calls `RemoveWorktree`.

**Step 1: db retry (A).** Tests first, in `internal/db/db_test.go`:
- `TestTxRetriesABusyBegin`: set `busyTimeoutMS = 20` and `beginRetryFor = 3s`
  (restore both with `t.Cleanup`). Open the same file twice as `d1` and `d2`.
  In a goroutine, `d1.Tx` holds its fn for 300 ms (signal on a channel once
  inside). After the signal, `d2.Tx` with a counting fn must return nil, and
  the fn must have run exactly once.
- `TestTxGivesUpOnABusyBeginAfterTheDeadline`: `busyTimeoutMS = 20`,
  `beginRetryFor = 200ms`. `d1.Tx` holds until the test releases it (after
  `d2` returns). `d2.Tx` must return an error with `errors.Is(err, ErrBusy)`
  and `"db: tx begin"` in its text, and its fn must never have run.

Run them: `TestTxRetriesABusyBegin` must FAIL before the change (record the
failure line). Then implement section 5A. Verification: both pass, and
`go test ./internal/db/ -count=1` is green.

**Step 2: ingest (B).** Test first, in `internal/ingest/ingest_test.go`:
`TestIngestResolvesGitAndSessionsOutsideTheTransaction`. Model it on
`TestIngestResolvesRepoWhenMissing` (line 668) and
`TestIngestPlannerTranscript` (line 686) for the fixture shape: a live source
with no RepoRef and a planner SessionID but no TranscriptLocator. Give it a
GitFacts fake and a Sessions func. Each one, when called, runs
`d.Tx(func(*db.Tx) error { return nil })` on the same `*db.DB`, records the
error, and then returns the normal fake answer. Assert both recorded errors
are nil and both fakes were called. Before the fix, the inner `Tx` hits busy.
It takes about `beginRetryFor` to fail, which is acceptable once. Run it
once before the fix with `-timeout 120s` and record the failure. Then
implement section 5B. Verification: `go test ./internal/ingest/ -count=1` is
green, including the existing `TestIngestResolvesRepoWhenMissing`,
`TestIngestPlannerTranscript` and `TestIngestRepoFromSourceCheckoutWhenCWDGone`.

**Step 3: ScopeProber + proc (C, part 1).** Add `ScopeProber` to
`runner.go`, `ErrScopeActive` to `headless.go`, `(*Runner).ScopeActive` to
`proc/scope.go`, the `var _` assertion, and `fakeRunner.ScopeActive` plus its
two fields. Verification: `go build ./... && GOOS=darwin go vet
./internal/proc/ && GOOS=windows go vet ./internal/proc/`.

**Step 4: Send (C, part 2).** Tests first, in `internal/relevo/send_test.go`,
using the setup of `TestSendHeadlessTierYoloOverrideAndRoundClose` (line
391) for a headless binding with a `fakeRunner`:
- `TestSendKillsTheBuilderWhenTheSendFailsAfterSpawn`: set `sendAfterSpawn`
  to return `errors.New("injected")` (restore with `t.Cleanup`). `Send` must
  return an error containing both `injected` and `was stopped`. `fakeRunner`'s
  `kills` must hold exactly the handle `Start` returned. The reloaded binding
  must have `Builder.PID` unchanged from before the send, and its log must
  have no plan entry for the new round. The round's builder log must contain
  `send failed after spawn; builder stopped`.
- `TestSendRefusesWhileTheRoundsScopeIsActive`: set `rt.Scope` to a non-nil
  `&ScopeSpec{}`. Set `fakeRunner.scopeActive[scopeUnitName(<binding at the
  round Send will open>)] = true`. `Send` must return an error with
  `errors.Is(err, ErrScopeActive)`. `fakeRunner.specs` must be empty (no
  Start). The binding's State must not be NEEDS YOU, and the plan file for
  that round must not exist.
- Also assert, in the second test or a third small one, that with
  `rt.Scope == nil` no scope query is made (`scopeQueries` empty) and the
  send proceeds.

Run them: both must FAIL before the change. Then implement section 5C.
Verification: `go test ./internal/relevo/ -run 'TestSend' -count=1` is green.
Existing Send tests must pass unchanged. If one breaks, halt and report
rather than edit it.

**Step 5: Add rollback (D).** Test first, in `internal/relevo/add_test.go`:
`TestAddRollbackDeletesTheBranchItCreated`. Use `newForkRuntime` and
`fakeGit` as `TestAddRefusesATreeAnotherBindingDrives` (line 299) does, but
seed the incumbent binding with `CWD: rt.Store.WorktreePath("frontend")` and
call `Add` **without** `CWD`, so it cuts a worktree and then hits
`ErrCWDTaken`. Assert `errors.Is(err, store.ErrCWDTaken)`, one
`removeWorktreeCalls` entry, and one `deleteBranchCalls` entry with
`Branch == "relevo/frontend"`. Also assert that
`TestAddRefusesATreeAnotherBindingDrives` (the `--cwd` path) records **no**
`deleteBranchCalls`: add that one assertion to the existing test. That is a
strengthening, not a change to what it checks. Run: the new test FAILS
first. Then implement section 5D. Verification:
`go test ./internal/relevo/ -run 'TestAdd' -count=1` is green.

**Step 6: mutation check.** Revert each fix one at a time in the working
tree, run its focused test, and confirm the named test fails:
- 5A → `TestTxRetriesABusyBegin`
- 5C kill → `TestSendKillsTheBuilderWhenTheSendFailsAfterSpawn`
- 5C guard → `TestSendRefusesWhileTheRoundsScopeIsActive`
- 5D → `TestAddRollbackDeletesTheBranchItCreated`

Restore each fix after checking it. Skip 5B here; step 2 already showed its
failure. Record the four results.

**Step 7: full check.** `make check` (see section 7). It must pass.

**Step 8: ship.** Copy the plan file you were given (its path is in your
prompt) to `docs/plans/2026-09-25-db-busy-spawn-integrity.md`. Commit
everything as one commit:
`fix: a busy db no longer orphans a spawned builder or leaves a bind's branch behind (#436, #437, #445)`
with `Fixes #436`, `Fixes #437` and `Fixes #445` in the body. Push, and open
a PR against `main` whose body lists the four changes (A-D) and the three
`Fixes` lines. Don't wait for CI and don't merge.

The report states:
- the pre-fix failure line for each new test (steps 1, 2, 4, 5);
- the step 6 mutation results;
- the `make check` result;
- the PR number;
- `git diff --stat`.
