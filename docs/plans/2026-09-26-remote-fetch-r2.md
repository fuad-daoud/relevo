# #544 round 2 -- the round-close catch-up downloads and absorbs without the state lock

## 0. Rules for this round

- This tree is `relevo/remote-fetch` with round 1 committed on top of
  `ac74389`: `internal/relevo/remotefetch.go` (`remoteFetch`, `fetchRemote`,
  `matches`, `applyLogMirror`, ...), `applyRemote` / `applyRemoteErr` /
  `applyRemoteView` in `remote.go`, `reconcileWith` in `reconcile.go`,
  `prefetchRemote` in `daemon.go`, the `fakeRemote.beforeCall` hook, and
  `remotefetch_test.go`. Build on it. Do not fetch, merge or rebase. Check
  that `git log -2 --format=%s` shows the two round-1 commits; if not, stop
  and report.
- If a step is impossible as written, or the code contradicts this plan, **stop
  and report** -- do not improvise.
- **Run every check in the foreground and wait for it.** Never run `make check`
  or a test as a background task, and never end your turn to wait for one:
  this process exits when your turn ends and nothing will wake it. Your turn
  ends only after the report is written and the done marker exists.
- This is a **move** of where network and git I/O happen, not a behaviour
  change. Every existing test -- in particular the `TestCatchUp*`,
  `TestObserveRemoteIdle*` and `TestCatchUpOrderAndIdempotence` tests in
  `remote_test.go` (~3665-4960) -- must pass **unchanged**. If one fails, do
  not edit it: stop and report which and why.
- `catchUp` keeps its signature as a fetch-then-apply wrapper, as round 1 did
  for `observeRemote`, `mirrorLog` and `mirrorDriftOnce`.
- Comments: *why* only, no issue numbers, no `§`, no history. New and moved
  functions <= 70 lines. No `.golangci.yml` exclusion or allow-list entry.
- Touch only the files in §2.

## 1. System overview

Round 1 moved the every-tick remote reads (`GetBinding`, log mirror, drift)
out of the state lock. The round-close catch-up still runs under it:
`applyRemoteView` calls `catchUp(ctx, rt, tx, b, view)` (`remote.go`, `func
catchUp` ~1207-1460 on this branch) in two cases -- `RoundClosed` with
`view.ClosedRound >= b.Round`, and `RoundIdle` with `view.ClosedRound >=
b.Round` and no report entry yet for `b.Round` (the lost-ack recovery).

`catchUp` does, in order:

1. `RoundFile(report)` -> `writeTempAndRename(ReportPath)`; a 404 halts unless
   `view.Stopped != ""`; another error returns `b, nil` (retry next tick).
2. `RoundFile(diff)` -> read (cap `git.DefaultMaxPatchBytes`) -> `tx.PutRoundFile(DiffPath)`; 404 fine; other error -> `b, nil`.
3. `RoundFile(log)` -> legacy: `writeTempAndRename(logPath)`; else read (cap `maxMirrorBytes`) -> `tx.PutRoundFile`; 404 fine; other error -> `b, nil`.
4. `RoundFile(stream)` -> `writeTempAndRename(BuilderStreamPath)`; 404 fine; other error -> `b, nil`.
5. `RoundBundle(since LastKnown)` -> `rt.Transport.Absorb(b.Repo, ...)`, then for an
   adopted branch `rt.Git.RefSHA` + `rt.Git.UpdateRef`. "checked out" -> quiet
   retry (`checkedOutWarned`); other absorb/update failure ->
   `RemoteAbsorbFailures++`, halt at 10; RefSHA errors -> returned error.
6. `LastKnown = view.ResultCommit`, `RemoteAbsorbFailures = 0`.
7. `rt.Remote.Ack` (30 s); failure -> `b, nil`.
8. Diff entry, payload, `queueReport`, stop entry, mark idle.

Steps 1-5 are network, disk and git I/O, each up to 2 min x 4 tries. This
round moves steps 1-5 into the fetch half (no lock) and leaves 6-8 in the
apply half. **`Ack` stays in the apply half, under the lock**, so today's
order (absorb -> ack -> queue the report) and its retry behaviour are
unchanged; it is one bounded call per round.

Disk writes of the report, stream and legacy log go to **temp files** in the
fetch half and are **renamed into place in the apply half**, so a discarded
fetch never leaves a report at `NNN-report.md` (which `relevo send` treats
as an undelivered report). The diff and DB-log bodies are held in memory
(both are already read fully under their caps today).

Git side effects of step 5 (absorb, adopted-branch update) happen in the
fetch half. They are idempotent -- a discarded fetch leaves `LastKnown`
unchanged, so the next catch-up asks for the same bundle again.

## 2. File structure

```
internal/relevo/remotefetch.go       catchUpFetch type; fetchCatchUp; applyCatchUp; (*remoteFetch).release;
                                     fetchRemote fills f.CatchUp; prefetchRemote comment fix (§4)
internal/relevo/remote.go            catchUp -> wrapper; applyRemoteView passes f.CatchUp; the step 1-5
                                     code moves out; reconcileRemote and SyncRemote release a prefetch
internal/relevo/daemon.go            tickOne releases the prefetch; prefetchRemote comment fix
internal/relevo/remote_test.go       fakeRemote: beforeCall also in RoundBundle; fakeTransport: an
                                     optional beforeAbsorb func() hook
internal/relevo/remotefetch_test.go  the tests in §6
docs/plans/2026-09-26-remote-fetch-r2.md   this plan (last step)
```

## 3. Data structures

```
// catchUpFetch is the round-close download a fetch made without the lock.
type catchUpFetch struct {
    Round         int     // view.ClosedRound
    Abort         bool    // a non-404 read failed: the apply returns b unchanged (retry next tick)
    ReportMissing bool    // RoundFile(report) answered 404
    ReportTemp    string  // temp file holding the report, "" if none
    Diff          []byte  // nil: none (404, or over cap -- logged at fetch as today)
    Log           []byte  // DB-form log body; nil: none
    LogTemp       string  // legacy-form log in a temp file, "" if none
    StreamTemp    string  // temp file holding the stream, "" if none
    CheckedOut    bool    // absorb or adopted-branch update hit "checked out": quiet retry
    AbsorbErr     error   // other absorb / UpdateRef failure: apply counts it (halt at 10)
    Fatal         error   // RefSHA failure after absorb: apply returns it as today
}
remoteFetch gains:  CatchUp *catchUpFetch   // nil: no catch-up was fetched
```

Temp files are created with `os.CreateTemp(filepath.Dir(final), filepath.Base(final)+".fetch.*")`,
chmod 0644 as `writeTempAndRename` does, so the rename in apply is atomic on
the same filesystem.

## 4. Contracts

```
fetchCatchUp(ctx, rt Runtime, b store.Binding, view remote.BindingView) *catchUpFetch
  Steps 1-5 of §1 with the same calls, caps, 404 rules and log lines, writing
  to temp files / memory instead of final paths and tx. The first non-404
  read error sets Abort, removes temps made so far, and returns (no further
  calls -- today the function returns there too). A report 404 sets
  ReportMissing and continues only when view.Stopped != ""; when it is "",
  return with ReportMissing set and nothing else fetched (the apply halts).
  Absorb and the adopted-branch update run here; their outcome is recorded in
  CheckedOut / AbsorbErr / Fatal (and CheckedOut stops before later steps, as
  today's early return does).

applyCatchUp(ctx, rt, tx, b store.Binding, view remote.BindingView, cf *catchUpFetch) (store.Binding, error)
  Under tx. In order, mirroring today's catchUp exactly:
   cf.Abort                               -> return b, nil
   cf.ReportMissing && view.Stopped == "" -> haltBinding (same message)
   rename ReportTemp -> ReportPath; PutRoundFile Diff; Log (PutRoundFile) or LogTemp (rename);
   rename StreamTemp -> BuilderStreamPath  -- a failure here logs as today and returns b, nil
   cf.CheckedOut                          -> return b, nil
   cf.AbsorbErr != nil                    -> RemoteAbsorbFailures++ ; >= 10 -> haltBinding (same messages)
   cf.Fatal != nil                        -> return b, cf.Fatal
   then today's steps 6-8 unchanged (LastKnown, Ack, diff entry, queueReport, stop entry, idle).
  Split into helpers to stay <= 70 lines (e.g. applyCatchUpFiles, applyCatchUpAbsorb).

catchUp(ctx, rt, tx, b, view) (store.Binding, error)                     // kept: inline wrapper
  cf := fetchCatchUp(ctx, rt, b, view); defer cf.release(); return applyCatchUp(ctx, rt, tx, b, view, cf)

fetchRemote (round 1) additionally, after GetBinding succeeds:
  RoundClosed && view.ClosedRound >= b.Round                       -> f.CatchUp = fetchCatchUp(...)
  RoundIdle   && view.ClosedRound >= b.Round && no report entry for b.Round
              (rt.Store.ReadLog(b.Name), an unlocked read; on error, no catch-up) -> f.CatchUp = fetchCatchUp(...)
applyRemoteView: where it calls catchUp today, call
  applyCatchUp(ctx, rt, tx, b, view, f.CatchUp) when f.CatchUp != nil && f.CatchUp.Round == view.ClosedRound,
  else catchUp(...) inline as today (covers the Idle case whose unlocked check disagreed with the locked one).

(*catchUpFetch).release()  -- removes ReportTemp, LogTemp, StreamTemp if they still exist; nil-safe; idempotent.
(*remoteFetch).release()   -- nil-safe; releases f.CatchUp.
Callers: tickOne `defer pre.release()` right after prefetchRemote; SyncRemote `defer`-equivalent per binding
(call f.release() after its WithLock returns); reconcileRemote's discard branch needs nothing extra (the
caller releases). A rename in apply leaves nothing for release to remove.

prefetchRemote's doc comment: replace "returns nil ... for any read that fails" with the truth -- nil when the
binding cannot be loaded or is not a live remote binding; a failed server read travels in the fetch's Err and is
classified by the apply half.
```

## 5. Pseudocode (fetch side, step 1 as the pattern)

```
rc, err := rt.Remote.RoundFile(ctx, server, name, n, "report")
switch {
case is404(err):        cf.ReportMissing = true; if view.Stopped == "" { return cf }
case err != nil:        slog.Warn("fetch report failed", ...); cf.Abort = true; cf.release(); return cf
default:                cf.ReportTemp, err = downloadTemp(rt.Store.ReportPath(name, n), rc)   // closes rc
                        if err != nil { slog.Warn("write report failed", ...); cf.Abort = true; cf.release(); return cf }
}
```

`downloadTemp(final string, r io.ReadCloser) (string, error)` is
`writeTempAndRename` without the rename (MkdirAll, CreateTemp, copy, close,
chmod 0644), returning the temp path. `writeTempAndRename` may be
reimplemented as `downloadTemp` + `os.Rename` if its other callers stay green.

## 6. Tests (`remotefetch_test.go`)

Hooks: `fakeRemote.beforeCall` is also called at the top of `RoundBundle`;
`fakeTransport` gains `beforeAbsorb func()` called at the top of `Absorb`.
Build the closed-round fixtures the way `TestCatchUpOrderAndIdempotence`
(~3807) and `TestCatchUpStoppedWithReport` (~3717) do, driven through a
`Daemon.Tick` like round 1's `TestTickFetchesRemoteWithoutTheStateLock`.

1. `TestTickCatchesUpWithoutTheStateLock`: `RoundClosed`, `ClosedRound = 1`.
   `lockFreeWithin(st, 2s)` holds inside `beforeCall` for the report, diff,
   log and stream `RoundFile` calls and `RoundBundle`, and inside
   `beforeAbsorb`. After `Tick`: round 1's report entry is queued, `Round ==
   2`, `LastKnown == view.ResultCommit`, `Ack` was called once, the report is
   at `ReportPath(1)`, no `*.fetch.*` file remains in the binding dir.
2. `TestCatchUpDiscardedWhenDoneMidFetch`: `beforeAbsorb` marks the binding
   DONE via `st.WithLock`. After `Tick`: state DONE, no report entry, no
   `Ack` call, no file at `ReportPath(1)`, no `*.fetch.*` left.
3. `TestCatchUpFetchAbortLeavesNothing`: `RoundFile(diff)` returns a non-404
   error. After `Tick`: no report entry, no `Ack`, `RoundBundle` never
   called, no file at `ReportPath(1)`, no `*.fetch.*` left.
4. `TestTickCatchUpMissingReportHalts`: report 404, not stopped -> the
   binding halts with today's message, through the tick path.

Mutation checks (report each failing test name; restore after each):
(a) `fetchRemote` never sets `CatchUp` -> test 1 fails (catch-up ran under the lock);
(b) `release` removes nothing -> test 2 or 3 fails on a leftover temp;
(c) rename the report in the fetch half instead of apply -> test 2 fails (a report at `ReportPath(1)`).

## 7. Error handling

Identical outcomes to today for every server answer: same halts, same
retries, same log lines. New failure modes are only temp-file ones
(CreateTemp / rename), which log and retry next tick exactly like the
`writeTempAndRename` failures they replace.

## 8. Working efficiently

- Read in one batch: `remote.go` (`applyRemoteView`, `catchUp`, `writeTempAndRename`,
  `SyncRemote`, `reconcileRemote`), `remotefetch.go`, `daemon.go` 260-350,
  `remote_test.go` 60-260 and 3660-3960, `remotefetch_test.go`.
- Focused loop: `go build ./... && go test ./internal/relevo -run 'CatchUp|Remote|Mirror|Tick|SyncRemote|Stop|Fetch|ObserveRemote' -count=1`.
- Then `go test ./internal/relevo -count=1 -race` once.
- Full check once, at the end, in the foreground: `make check`.

## 9. Ordered steps

1. Check the base (§0).
2. `catchUpFetch`, `downloadTemp`, `fetchCatchUp`, `applyCatchUp`, `release`; `catchUp` as the wrapper.
   Build passes; **every existing test passes unchanged**.
3. `fetchRemote` fills `CatchUp`; `applyRemoteView` uses it; `tickOne` and `SyncRemote` release;
   `prefetchRemote` comment fix. Build and existing tests pass.
4. Hooks and tests (§6). Focused loop passes.
5. Mutation checks (§6).
6. `make check` in the foreground. `git diff --stat HEAD` shows only §2 files.
   Commit: `fix(remote): the round-close catch-up downloads and absorbs without the state lock (#544)`.
7. **Save the plan.** Copy this plan to `docs/plans/2026-09-26-remote-fetch-r2.md` and commit it.

Report: the diff stat, each mutation's failing test name, any existing test
that needed attention (there should be none), and the `make check` result.
