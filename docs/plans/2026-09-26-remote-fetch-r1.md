# #544 round 1 -- the daemon fetches a remote binding's state without the state lock, then applies it under a short one

## 0. Rules for this round

- The tree is already cut from current main (`ac743896`). Do not fetch or
  merge anything. Check `git log -1 --format=%h` shows `ac74389`; if it does
  not, stop and report.
- If a step is impossible as written, or the code contradicts this plan, **stop
  and report** -- do not improvise. A halt that surfaces a design error is
  worth more than a green suite that bent a test to fit.
- **Run every check in the foreground and wait for it.** Never run `make check`
  or a test as a background task, and never end your turn to wait for one:
  this process exits when your turn ends and nothing will wake it. Your turn
  ends only after the report is written and the done marker exists.
- This is a **move, not a behaviour change**, for everything except where the
  lock is held. Every existing test must pass **unchanged**. If one fails,
  do not edit its assertions: stop and report which test and why.
- Nothing is deleted from the behaviour. The code of `observeRemote` moves
  into the apply half; `observeRemote`, `mirrorLog` and `mirrorDriftOnce`
  keep their signatures (as fetch-then-apply wrappers) so their callers and
  tests still hold.
- Comments: *why* only, no issue numbers, no `§`, no history. No new
  `.golangci.yml` exclusion or allow-list entry. New and moved functions
  <= 70 lines (split where §4 says). Follow the surrounding tests'
  `t.Parallel()` convention in new tests.
- `cmd/relevo` tests must not spawn a harness or reach the network. This round
  adds no `cmd/relevo` test.
- Touch only the files in §2.

## 1. System overview

relevo's state lock is one exclusive flock over the state root. The daemon's
`tickOne` (`internal/relevo/daemon.go` ~266-320) takes it per binding and
runs `Reconcile` inside it. For a remote binding, `Reconcile`
(`internal/relevo/reconcile.go` ~127-182) calls `reconcileRemote`
(`remote.go` ~1021), which calls `observeRemote` (`remote.go` ~853-1016).
On every tick, that does HTTP while holding the lock:

- `rt.Remote.GetBinding` (up to 30 s);
- when the round is running, `mirrorLog` (`remote.go` ~637-770: one or two
  `RoundFileFrom` range reads, up to 2 min x 4 tries each) and
  `mirrorDriftOnce` (~772-803: one `RoundFile(drift)` per round).

Every writer (`send`, `done`, `gate`, `wait` confirming a delivery, other
bindings' ticks) waits behind it and fails after 90 s.

This round splits that every-tick path into:

1. **fetch** -- no lock: `GetBinding`, the log range read and the drift read,
   into an in-memory `remoteFetch`;
2. **apply** -- under the lock, with the binding reloaded: validate that the
   binding is still the one fetched for (same name, server, round; still
   remote; not DONE or PAUSED). If not, discard and change nothing. Otherwise
   run exactly the decisions and writes `observeRemote` makes today, reading
   the fetched values instead of calling the network.

The daemon prefetches before taking the lock; `SyncRemote` (the no-daemon
path) does the same. `stop.go` keeps calling `observeRemote` inline under its
own lock (a user command, rare) -- unchanged.

**Out of scope (round 2):** `catchUp` (`remote.go` ~1265-1535) -- the
round-close downloads, bundle, absorb and `Ack` -- still runs inline from the
apply half, under the lock, exactly as today. So does the legacy (pre-DB,
file-backed) branch of `mirrorLog`, which only old rounds take.

## 2. File structure

```
internal/relevo/remotefetch.go       NEW: remoteFetch, logMirror, fetchRemote, fetchLogMirror,
                                     fetchDrift, (remoteFetch).matches, applyLogMirror, applyDrift
internal/relevo/remote.go            observeRemote -> applyRemote (+ its two halves); observeRemote,
                                     mirrorLog, mirrorDriftOnce become fetch-then-apply wrappers;
                                     reconcileRemote takes the prefetch; SyncRemote prefetches
internal/relevo/reconcile.go         Reconcile delegates to reconcileWith(..., pre *remoteFetch)
internal/relevo/daemon.go            tickOne prefetches a remote binding before WithLock
internal/relevo/remote_test.go       fakeRemote: one optional hook field, called from three methods
internal/relevo/remotefetch_test.go  NEW: the tests in §6
docs/plans/2026-09-26-remote-fetch-r1.md   this plan (last step)
```

No other file changes.

## 3. Data structures (`remotefetch.go`)

```
// remoteFetch is what the fetch half read from the server for one binding,
// without the state lock, for the apply half to act on under it.
type remoteFetch struct {
    Name   string              // b.Name at snapshot
    Server string              // b.Builder.Server at snapshot
    Round  int                 // b.Round at snapshot
    View   remote.BindingView  // GetBinding's result; zero when Err != nil
    Err    error               // GetBinding's error, classified by the apply half exactly as today
    Log    *logMirror          // nil: nothing fetched to write (not running, legacy log, error, no growth)
    Legacy bool                // the round's log is the legacy file form: apply runs the legacy mirror inline
    Drift  []byte              // nil: nothing fetched
}

// logMirror is one fetched update to the round's mirrored builder log row.
type logMirror struct {
    Path    string  // rt.Store.BuilderLogPath(name, round)
    Base    int64   // local length the range read started from; the apply half writes only if the
                    // row still has exactly this length. -1 means Body is a full replacement.
    Body    []byte  // bytes to append (Base >= 0) or the whole log (Base == -1); never empty when Log != nil
}
```

Constraints: `Log.Body` is non-empty; `len(cur)+len(Body) <= maxMirrorBytes`
is checked at fetch and again at apply. `Drift` is only set when the fetch
read it successfully.

## 4. Contracts

```
fetchRemote(ctx context.Context, rt Runtime, b store.Binding) remoteFetch
  Pre: b is remote. Takes no lock; reads local state only through unlocked
  reads (rt.Store.ReadFile). Makes GetBinding; if the view is RoundRunning,
  also the log (fetchLogMirror) and, once per round, the drift (fetchDrift).
  Never returns an error: a failed read leaves that field empty and logs as
  mirrorLog / mirrorDriftOnce log today (same messages, same levels).

fetchLogMirror(ctx, rt, server, name string, round int) (*logMirror, bool /*legacy*/)
  The network half of today's DB-branch mirrorLog: read cur via
  rt.Store.ReadFile, range-read from len(cur), and map today's three cases:
    honored && from==local && size>=local  -> append (Base=len(cur)); nil if body empty
    honored && size<local                  -> full refetch from 0, Base=-1
    otherwise                              -> full body, Base=-1
  The over-cap checks stay where they are today (fetch side). legacyLog(rt,
  name, round) true -> (nil, true) with no network.

fetchDrift(ctx, rt, server, name string, round int) []byte
  The network half of mirrorDriftOnce, keeping its guards (already stored ->
  nil; mirrorDriftAttempts LoadOrStore -> nil on the second attempt).

(f remoteFetch) matches(b store.Binding) bool
  b.Name == f.Name && b.Builder.Remote() && b.Builder.Server == f.Server &&
  b.Round == f.Round && b.State != StateDone && b.State != StatePaused &&
  store.KnownState(b.State)

applyLogMirror(rt Runtime, tx *store.Tx, name string, round int, m *logMirror)
  Under tx. m nil -> nothing. Base == -1 -> PutRoundFile(Body).
  Base >= 0 -> re-read cur; if int64(len(cur)) != Base -> skip (the row moved
  since the fetch; the next tick re-reads) at Debug; else PutRoundFile(cur+Body)
  unless over maxMirrorBytes (same Warn as today). Errors are logged and
  swallowed, as mirrorLog does today.

applyDrift(rt Runtime, tx *store.Tx, name string, round int, data []byte)
  data nil -> nothing; else PutRoundFile(DriftPath, data), same Warn on failure.

applyRemote(ctx, rt, tx, b store.Binding, f remoteFetch) (store.Binding, bool, error)
  Today's observeRemote body, with:
    GetBinding call        -> f.View / f.Err
    mirrorLog(...)         -> if f.Legacy { mirrorLog(ctx, rt, tx, server, name, b.Round) } else { applyLogMirror(...) }
    mirrorDriftOnce(...)   -> applyDrift(...)
    catchUp(...)           -> unchanged (inline, under tx)
  Split to fit 70 lines:
    applyRemoteErr(ctx, rt, tx, b, err, now) (store.Binding, bool, error)  -- the whole `if err != nil` block
    applyRemoteView(ctx, rt, tx, b, f, now) (store.Binding, bool, error)   -- the success path and the state switch
  Return values and every write are identical to today's observeRemote.

observeRemote(ctx, rt, tx, b) (store.Binding, bool, error)
  = applyRemote(ctx, rt, tx, b, fetchRemote(ctx, rt, b))     // kept for stop.go; inline under the caller's lock

mirrorLog(ctx, rt, tx, server, name, round)
  legacy -> today's legacy branch, unchanged
  else   -> m, _ := fetchLogMirror(...); applyLogMirror(rt, tx, name, round, m)
mirrorDriftOnce(ctx, rt, tx, server, name, round)
  = applyDrift(rt, tx, name, round, fetchDrift(...))

reconcileRemote(ctx, rt, tx, b, pre *remoteFetch) (store.Binding, error)
  pre == nil          -> next, deliver, err := observeRemote(...)       (today's path)
  !pre.matches(b)     -> return b, nil  (discard: no write, no network, no delivery;
                                          log at Debug "remote fetch discarded: binding changed")
  otherwise           -> next, deliver, err := applyRemote(ctx, rt, tx, b, *pre)
  then as today: if err != nil || !deliver return; deliverAndSettle(...)

reconcile.go:
  Reconcile(ctx, rt, tx, b) = reconcileWith(ctx, rt, tx, b, nil)     // exported signature unchanged
  reconcileWith(ctx, rt, tx, b, pre *remoteFetch): today's Reconcile body, passing pre to reconcileRemote.
  (Not named `reconcile`: the tests already have a helper by that name.)
```

## 5. Pseudocode

```
daemon.go tickOne(ctx, name):
  pre := d.prefetchRemote(ctx, name)          // NEW, before WithLock
  WithLock:
     ... unchanged: load, newer-format guard, sealRounds, backfillPlannerID ...
     next, err := reconcileWith(ctx, d.rt, tx, fresh, pre)     // was Reconcile(ctx, d.rt, tx, fresh)
     ... unchanged save ...

(d *Daemon) prefetchRemote(ctx, name) *remoteFetch:
  if d.rt.Remote == nil: return nil
  b, err := d.rt.Store.Load(name)             // an unlocked read
  if err != nil || !b.Builder.Remote() || b.State in {Done, Paused} || !store.KnownState(b.State)
     || b.Format > store.BindingFormat: return nil
  f := fetchRemote(ctx, d.rt, b); return &f
  (Keep the panic containment: prefetch runs inside tickOne after its deferred
   recover is installed, i.e. place the call after the defer.)

SyncRemote(ctx, rt), per remote relaying binding b from the (unlocked) List:
  f := fetchRemote(ctx, rt, b)                 // NEW: before the lock
  WithLock:
     fresh := tx.Load(name)   (ErrNotFound -> nil, as today)
     if !f.matches(fresh): return nil          // discarded; not counted as synced
     next, _, err := applyRemote(ctx, rt, tx, fresh, f)
     ... unchanged SameBinding / synced++ / Save ...
```

A discarded fetch is not an error and is not retried within the tick.

## 6. Tests (`internal/relevo/remotefetch_test.go`)

Add to `fakeRemote` (`remote_test.go` ~60-135) one field:
`beforeCall func(call string)`; call it (when non-nil) at the top of
`GetBinding`, `RoundFileFrom` and `RoundFile`, with the same call string the
method appends to `calls`. Nothing else in the fake changes.

Build daemons and bindings the way `daemon_test.go` (`TestTickReconcilesAndPersists`
~24) and `remote_test.go` (`TestReconcileRemoteRunningMirrorsLog` ~2352,
`remoteBinding`) do.

Helper (test file): `lockFreeWithin(t, st *store.Store, d time.Duration) bool`
-- runs `st.WithLock(func(*store.Tx) error { return nil })` in a goroutine and
reports whether it returned within d.

1. `TestTickFetchesRemoteWithoutTheStateLock`: a running remote binding; the
   fake returns a log body. `beforeCall` asserts `lockFreeWithin(st, 2s)` on
   `GetBinding` and on `RoundFileFrom`. After `Tick`: the log row holds the
   body, `RemoteStatus == "running"`, and the fake saw exactly one
   `GetBinding`.
2. `TestTickDiscardsFetchWhenBindingIsDoneMidFetch`: `beforeCall` on
   `GetBinding` marks the binding DONE through `st.WithLock` (load, set
   `State = StateDone`, save). After `Tick`: state is DONE, no log row was
   written, one `GetBinding` call in total.
3. `TestTickDiscardsFetchWhenRoundAdvancedMidFetch`: `beforeCall` bumps
   `Round` to 2 and saves. After `Tick`: `Round == 2`, `RemoteStatus`
   unchanged from what the hook saved, no round-1 log row written, one
   `GetBinding` call (the discarded fetch is not redone inline).
4. `TestSyncRemoteFetchesWithoutTheStateLock`: as test 1, through
   `SyncRemote` with no daemon.
5. `TestApplyLogMirrorSkipsWhenTheRowMoved`: `applyLogMirror` with
   `Base = 5` against a row that now holds 8 bytes writes nothing; with the
   row at 5 bytes it writes `cur+Body`; with `Base = -1` it replaces.
6. `TestFetchMatchesOnlyTheSameRoundAndServer`: table test of `matches`
   (same -> true; round, server, name changed, DONE, PAUSED, not remote -> false).

Mutation checks (report each failing test name, restore after each):
(a) `prefetchRemote` returns nil -> test 1 fails (lock held during the fetch);
(b) `matches` returns true -> test 3 fails;
(c) `applyLogMirror` ignores `Base` -> test 5 fails;
(d) `SyncRemote` fetches inside `WithLock` -> test 4 fails.

## 7. Error handling

- Fetch never fails the tick. Network errors travel in `remoteFetch.Err` and
  are classified in the apply half exactly as today (401 revoked / other 401
  grace / 404 / unreachable / cert changed / other).
- A discarded fetch is silent apart from a Debug line: the binding changed,
  so the next tick fetches for its new state.
- A panic in the prefetch is contained by `tickOne`'s existing recover.

## 8. Working efficiently

- Read in one batch: `remote.go` 600-1110, `daemon.go` 130-330,
  `reconcile.go` 100-185, `stop.go` 100-140, `remote_test.go` 1-200 and
  2340-2460, `daemon_test.go` 1-80, `internal/remote/client/client.go`
  270-290.
- Make the `remote.go` change in one edit pass: cut `observeRemote`'s body
  into `applyRemoteErr` / `applyRemoteView`, then add the wrappers.
- Focused loop: `go build ./... && go test ./internal/relevo -run 'Remote|Mirror|Tick|SyncRemote|Stop|Fetch' -count=1`.
- Then `go test ./internal/relevo -count=1 -race` once.
- Full check once, at the end, in the foreground: `make check`.

## 9. Ordered steps

1. Check the base commit (§0).
2. `remotefetch.go` types and the fetch/apply helpers (§3, §4). Build passes.
3. `remote.go`: `applyRemote` (+ halves), wrappers, `reconcileRemote(pre)`,
   `SyncRemote` prefetch. `reconcile.go`: `reconcileWith`. `daemon.go`:
   `prefetchRemote` + call site. Build passes; **every existing
   `internal/relevo` test passes unchanged** (§0).
4. Tests (§6) and the `fakeRemote` hook. Focused loop passes.
5. Mutation checks (§6).
6. `make check` in the foreground. `git diff --stat` shows only §2 files.
   Commit: `fix(remote): the daemon fetches a remote binding without the state lock, then applies under it (#544)`.
7. **Save the plan.** Copy this plan to `docs/plans/2026-09-26-remote-fetch-r1.md`
   and commit it.

Report: the diff stat, each mutation's failing test name, any existing test
that needed attention (there should be none), and the `make check` result.
