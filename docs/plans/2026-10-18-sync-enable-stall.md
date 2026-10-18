# Plan: #1241

MasterMind note: a live sandbox run on main (6d8af534) confirmed the traced cadence: after `db sync enable` on an idle machine with the daemon already running, attempts came at 12:47:21, 12:52:11, 12:57:13, 13:02:15 ... exactly every coldWindow (5 min), never 30 s. So the "stall" in the issue is the cold window; build the fix below as planned.


Base: origin/main 6d8af534. Builder works on a fresh branch off origin/main on server zen.

## What was proven, and how

I could not run a live daemon (the round is read-only), so the cause is proven by tracing every path that can start or withhold a second attempt, and the fix is pinned by a test that is red on main. The builder confirms the red first (step 1).

### Root cause

A daemon that finds sync on for the first time runs one attempt and then, on an idle machine, withholds the next one for `coldWindow` (5 min). The window is cold because nothing marks the machine as having just become active. This is the cadence the issue reports: one attempt, then 3+ minutes of silence, with nothing wrong anywhere. A restart does not help because it starts from the same cold state.

Code path, after `db sync enable` on a running daemon:

1. `Daemon.Run` ticks every 2 s (`cmd/relevo/daemon.go:40`, `internal/relevo/daemon.go:207-236`). Each `Tick` calls `idleSync` (`internal/relevo/daemon.go:287`).
2. While the mark is off, `idleSync` returns at `runner.On()` (`internal/relevo/sync_tick.go:89-91`; `internal/sync/steady.go:70-80`) and touches no state. `On()` reads the machine-local marker on every call, with no cache.
3. The enable writes the mark as its last step (`internal/sync/join.go:327` → `MarkEnabled`, `internal/sync/enable.go:192`). The verb then installs the worker with `setClient` (`internal/relevo/syncverb_join.go:314`).
4. The next tick sees `On()` true. `d.syncLast` is zero, so the window is "not closed". `idleSync` stamps `syncLast = now` and calls `queueSync` (`sync_tick.go:93-104`). This is the one attempt the issue sees.
5. Every later tick takes the `closed` branch: `now - syncLast < pullWindowLocked(now)` (`sync_tick.go:95`). `pullWindowLocked` (`internal/relevo/sync_liveness.go:118-123`) returns `hotWindow` (30 s) only when `live.appliedAt` is non-zero and younger than `hotSince`. Otherwise it returns `coldWindow` (5 min).
6. `appliedAt` is set only by `noteApplied`, whose only caller is `runSync` (`sync_tick.go:140`) when an attempt applied more than 0 rows (`sync_liveness.go:127-135`). The first attempt of a freshly enabled or joined machine applies nothing. The enable's own import happens inside the verb and never reaches `noteApplied`. On an idle machine the window therefore stays cold.
7. The only other trigger on the closed branch is `exportIfPending` (`sync_tick.go:101`, `sync_liveness.go:140-151`). It queues an attempt only when `HasOutboxEntries` or a document signal is pending. An idle machine has neither, so `queueSync` is never reached. The daemon sits idle with no TCP connections, and `last_attempt`, trouble, error, backoff and breaker all stay empty. That matches every symptom in the issue.
8. A restart zeroes `syncLast` and `appliedAt`, so it gives exactly one attempt and then goes cold again. This explains "a restart straight after enable did not reliably help". It also explains why a restart looks like it fixes things: the one attempt applies whatever a peer wrote meanwhile, which turns the window hot (30 s) and gives the 15-30 s ticking.

### Candidates ruled out (by reading, with the line that rules each out)

- **`syncInFlight` left true / missed wake-up.** Every site that sets it also clears it and broadcasts:
  - `syncSlot` has a `defer` with a `released` guard and panic recovery (`sync_liveness.go:75-114`).
  - `WaitSyncSlot` has a `defer` (`daemon.go:172-193`).
  - A stuck flag would stop all attempts forever, including after the 5-minute window. The issue's restart behaviour and the stated absence of a hung step contradict that.
- **Steady loop never started because its gate was read before the marker was written.** `On()` re-reads the marker on every tick (`steady.go:70-80`), and `Runner.Enabled` only needs a non-nil `Client` and `Local`. The placeholder supervisor is non-nil from startup (`internal/syncpipe/supervisor.go:48-53`), and `runSync` opens the stored remote through `EnsureTransport` (`sync_tick.go:127-134`). The loop does start; the issue's "one attempt" is evidence of that.
- **Timer reset only on some outcomes.** This is a real hazard but not what produced this report. `idleSync` stamps `syncLast` before `queueSync` decides anything (`sync_tick.go:94-98`). If a tick lands between `MarkEnabled` and the enable verb releasing the slot, `queueSync` marks the slot dirty and returns (`sync_tick.go:68-71`), and `WaitSyncSlot` then clears the dirty flag (`daemon.go:188`). That tick would use up the first window with no attempt at all. The reporter saw an attempt on both machines, so this is not proven as their cause and is **not fixed here**. The warm period below does restart the cadence 30 s after the lost window, so it is covered in effect.
- **Breaker not due.** `breakerDue` is checked in `queueSync` (`sync_tick.go:62`), but the issue shows breaker, backoff, error and attention all null.

## Fix

A daemon that observes sync turn on counts as active for `hotSince` (5 min), so the window is hot (30 s) during that period even if no attempt applied rows. After that it falls back to cold unless rows arrive. This is also what makes a restart of a synced machine behave: the process starts with a warm period too.

Cost: at most `hotSince / hotWindow` = 10 extra attempts per daemon start or enable, then the existing cold cadence. This is well under the "2,880 a day" ceiling the `hotWindow` comment already accepts.

### Design decisions (pre-answered)

- New field `onSince time.Time` on `syncLiveness` (`internal/relevo/sync_liveness.go:41-53`), guarded by `syncMu`. It is the time this daemon first saw sync on in the current on-period. Comment says why: a machine that just came on has a peer that is likely active and an empty history in this process, and `appliedAt` cannot say so until an attempt has applied something.
- `pullWindowLocked` returns `hotWindow` when `appliedAt` is within `hotSince` **or** `onSince` is non-zero and within `hotSince`. Keep it one small function; extract a tiny helper (for example `warmLocked(now)`) if it passes about 15 lines. Keep the existing doc comment accurate.
- `idleSync` (`sync_tick.go:87-105`):
  - When `runner.On()` is false, call a new `d.noteSyncOff()` before returning. It takes `syncMu` and zeroes `live.onSince` and `d.syncLast`. This makes a disable followed by a re-enable in the same process warm again, and makes the first window after any enable open immediately.
  - When on, inside the existing `syncMu` section, set `live.onSince = now` if it is zero, **before** computing `closed`. The first window therefore opens, and the window after it is hot.
  - `idleSync` stays under 70 lines. `noteSyncOff` and the warm helper go in `sync_liveness.go`; that file is 214 lines today, so there is no size problem.
- `daemon.go` is allow-listed over 600 lines. **Do not touch it**; the new field lives in `syncLiveness`, not `Daemon`.
- Do not change `queueSync`, `WaitSyncSlot`, `syncSlot`, the debounce, `exportIfPending`, the breaker, or any `internal/sync` file. The unproven dirty-drop hazard stays as it is.
- No comment mentions the issue, a PR, or history. Comments say why.

## Tests (all in `internal/relevo/sync_liveness_test.go`; no sleeps; fake clock `f.setNow`, injected via `d.syncNow`)

`sync_liveness_test.go` is 398 lines; the additions stay well under 600.

Fixture changes:

- `newLiveFixture` (line 70-93) must start the daemon **settled**, so the existing cold-window and debounce tests keep pinning a long-running machine. After constructing the daemon, set `f.d.live.onSince = baseTime.Add(-hotSince)` under `syncMu`. `idleSync` only stamps `onSince` when it is zero, so a preset is respected.
- Add `newOffFixture(t)`. It builds `newLiveFixture`, runs `f.baseline(t)` (needs the mark on to learn the per-attempt pull count), then `MarkEnabled(f.local, false, baseTime)` and zeroes `f.d.live.onSince` and `f.d.syncLast`. This is a daemon that has been running with sync off and is about to be enabled. The first `idleSync` while off clears these anyway; the explicit zeroing only keeps the setup honest.

New tests:

1. `TestSyncEnableWhileRunningFollowsWithASecondAttempt`
   - Off fixture. Call `idleSync` at `baseTime`, `baseTime+2s`, `baseTime+4s` and `waitSyncIdle` each time. Assert `attempts()` is unchanged (nothing while off).
   - `MarkEnabled(f.local, true, ...)` on the same daemon at `t0 = baseTime+10s`.
   - `idleSync` at `t0`: exactly 1 attempt.
   - `idleSync` at `t0+hotWindow-time.Second`: still 1.
   - `idleSync` at `t0+hotWindow`: exactly 2. The outbox is empty and no rows were applied (assert via `HasOutboxEntries` false and `live.appliedAt` zero), so only the warm period can account for the second attempt.
   - Red on main: the third check sees 1 attempt.
   - **Mutation:** in `pullWindowLocked`, drop the `onSince` term (`return coldWindow` regardless). The test fails at the final assertion.
2. `TestSyncWarmPeriodEndsAfterHotSince`
   - Same enable setup. Open the first window at `t0`, then call `idleSync` at `t0+hotSince+hotWindow` and assert that no further attempt starts.
   - This pins that a quiet machine returns to the cold cadence.
   - **Mutation:** remove the age bound on `onSince` (treat any non-zero `onSince` as hot). The test fails.
3. `TestSyncReEnableWarmsAgain`
   - Enable and run as in test 1, then `MarkEnabled(false)` and `idleSync` at a later tick, which clears the state.
   - Then `MarkEnabled(true)` and `idleSync` at `t1`: an attempt starts. At `t1+hotWindow` a second attempt starts.
   - **Mutation:** make `noteSyncOff` a no-op. The `idleSync` at `t1` sees `closed` and fails the first assertion (or the second, depending on spacing).
4. Extend `TestSyncHotWindowCoolsAfterHotSince` (line 195-216) with rows for `onSince` alone: `hotWindow` at `hotSince-1s`, `coldWindow` at `hotSince`. Keep the existing `appliedAt` rows.

All other existing liveness tests must pass unchanged. In particular `TestSyncPullWindowIsHotAfterAppliedRowsAndColdOtherwise` and `TestSyncExportDebounce` stay correct only because of the settled preset above. If either fails, fix the fixture, never the assertion.

Package placement: the whole rule is in `internal/relevo`; nothing is added to `cmd/relevo`, so no CI harness constraint applies.

## Ordered steps

1. Add the fixture changes and tests 1-4 to `internal/relevo/sync_liveness_test.go`. Run `go test ./internal/relevo/ -run 'TestSync(EnableWhileRunning|WarmPeriod|ReEnable|HotWindowCools)' -count=1`. Expect the new enable test to be red, with the second attempt missing. Record this in the report; it is the evidence that the test reproduces the stall. Tests 2-3 may fail to compile or pass vacuously until the field exists; that is fine, only test 1's red matters.
2. In `sync_liveness.go`: add `onSince` to `syncLiveness`, extend `pullWindowLocked`, add `noteSyncOff`. Done when the package compiles.
3. In `sync_tick.go`: call `noteSyncOff` on the off path and stamp `onSince` in `idleSync`. Done when the focused command from step 1 is green.
4. Mutation pass. Break each of the three conditions named in the tests, one at a time, and confirm the named test fails. Restore each. Put the results in the report.
5. Run `go test ./internal/relevo/ -count=1`. Then run `make check-affected` once at the end and fix anything it reports.
6. Save this plan to `docs/plans/2026-10-18-sync-enable-stall.md` and commit it **in the same commit series as the code** (plans ship with their implementation). New commits only; nothing is amended.

## Deleted behaviour

1. The idle machine's guarantee that the first window after sync turns on is followed by a `coldWindow` (5 min) gap. This replaces it with a `hotSince`-long warm period. No code is removed; only the window decision changes.

## Scope (files `git diff --stat` may show)

- `internal/relevo/sync_liveness.go`
- `internal/relevo/sync_tick.go`
- `internal/relevo/sync_liveness_test.go`
- `docs/plans/2026-10-18-sync-enable-stall.md`

Anything else in the diff is out of scope. In particular, no change to `daemon.go`, `internal/sync/*`, `cmd/relevo/*` or `testdata/coverage-baseline.txt`; the coverage baseline should not need regenerating since no code moves between packages.

## Report must include

- The red output of the enable test on unmodified main, then green after the fix.
- The three mutations (one per test) with the failing test name each produced.
- The exact `go test` and `make check-affected` outputs' pass/fail summary.
- `git diff --stat`, compared against the scope above.
- A statement that no live reproduction was done and that the cause rests on the traced code path. Also say whether the builder found any contradiction in the trace above. If the builder finds that `idleSync` is not the path (for example the hook is not installed so `d.rt.Sync` is used), halt and report rather than fix something else.
- The unproven dirty-drop hazard in `idleSync`/`WaitSyncSlot` is untouched. Mention it so the owner can file it separately if the stall persists after the fix.
