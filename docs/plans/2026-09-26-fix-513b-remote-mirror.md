# Fix #513 (part B) -- the remote log mirror writes only new bytes' worth of change; read verbs leave remote sync to a running daemon

## 0. Rules for this round

- Before anything else: `git fetch origin && git merge --ff-only origin/main`.
  If the fast-forward fails, stop and report.
- If a step is impossible as written, or the code contradicts this plan, **stop
  and report** -- do not improvise.
- **Run every check in the foreground and wait for it.** Never run `make check`
  or a test as a background task, and never end your turn to wait for one:
  this process exits when your turn ends and nothing will wake it. Your turn
  ends only after the report is written and the done marker exists.
- Comments: *why* only, no issue numbers, no `§`, no history. No new
  `.golangci.yml` exclusion or allow-list entry. Functions <= 70 lines.
- Touch only the files in §2. Parallel rounds own `internal/store`,
  `internal/db`, `internal/relevo/send.go`, `internal/relevo/headless.go`.
- `cmd/relevo` tests must not spawn a harness or reach the network: this
  round tests the new rule as a function in `internal/relevo`, not through the
  CLI.

## 1. System overview

A remote binding's builder log is mirrored from the server into the local
database by `mirrorLog` (`internal/relevo/remote.go` ~637-768). In its
database branch (after the `legacyLog` branch), it range-reads from the local
length and then **rewrites the whole log** with `tx.PutRoundFile(name, round,
path, append(cur, body...))` -- up to 16 MiB -- even when `body` is empty,
i.e. on every sync of every running remote binding, whether or not the log
grew. `mirrorLog` runs from `observeRemote`, which runs:

- in the daemon's reconcile of each remote binding, every tick;
- in `SyncRemote` (`remote.go` ~1036-1077), which `relevo status`
  (`cmd/relevo/status.go` ~93) and every poll of `relevo wait`
  (`internal/relevo/wait.go` ~190-196) call -- each under the global state
  lock, with network calls inside it.

With three remote bindings, a planner waiting on each, and the daemon
running, that is a full-log rewrite per binding many times a minute (the
~400 MB WAL), and read verbs holding the state lock across HTTP calls that
the daemon is already making.

Changes:
- A: `mirrorLog`'s appending case writes nothing when the range read
  returned no new bytes.
- B: `relevo status` and `relevo wait` skip `SyncRemote` when a daemon is
  running (`Store.DaemonRunning`, `internal/store/daemonlock.go` ~48); the
  daemon keeps remote bindings current. With no daemon they sync as today.

Out of scope: moving the daemon's own network I/O out of the state lock
(`observeRemote`/`catchUp` split) -- a follow-up issue; storing the log as
appended chunks instead of one blob.

## 2. File structure

```
internal/relevo/remote.go       mirrorLog: skip the no-op write; new SyncRemoteUnlessDaemon
internal/relevo/wait.go         the poll calls SyncRemoteUnlessDaemon
cmd/relevo/status.go            relevo status calls SyncRemoteUnlessDaemon (~93)
internal/relevo/remote_test.go  tests next to TestMirrorLogWritesARow (~2519) and the SyncRemote tests
docs/plans/2026-09-26-fix-513b-remote-mirror.md   this plan (last step)
```

No other file changes.

## 3. Contracts

```
// internal/relevo/remote.go, next to SyncRemote
SyncRemoteUnlessDaemon(ctx context.Context, rt Runtime) (synced int, skipped bool, err error)
  rt.Remote == nil            -> (0, false, nil)
  rt.Store.DaemonRunning():
     error                    -> fall through and sync (a failed probe must not stop collection)
     true                     -> (0, true, nil), no network, no lock
     false                    -> synced, err := SyncRemote(ctx, rt); return synced, false, err
```

`mirrorLog`, database branch, case `fr.Honored && fr.From == local &&
fr.Size >= local`: after `readBounded(rc)`, if `len(body) == 0`, return
without calling `PutRoundFile`. The other two cases (server shrank; server
ignored the range) are full replacements and stay as they are. The legacy
file branch appends to a file with `O_APPEND` and is unchanged.

Before relying on the daemon for `wait`, confirm by reading
`reconcile.go` (~170-180) and `reconcileRemote` (`remote.go` ~1015) that the
daemon's tick observes every remote binding that `SyncRemote` would (same
state filter: not `StateDone`). If the daemon skips some state that
`SyncRemote` covers, stop and report.

## 4. Pseudocode

```
cmd/relevo/status.go (cmdStatus), replacing the SyncRemote call:
  if rt.Remote != nil {
      if _, _, serr := relevo.SyncRemoteUnlessDaemon(ctx, rt); serr != nil {
          fmt.Fprintf(os.Stderr, "relevo: sync remote bindings: %v\n", serr)   // unchanged text
      }
  }

wait.go poll loop:
  _, _, _ = SyncRemoteUnlessDaemon(ctx, rt)    // advisory, as before
```

Update the comment above the wait.go call so it still says *why*: with no
daemon, the poll is the only thing collecting a remote round.

## 5. Tests (`internal/relevo/remote_test.go`, using `fakeRemote` ~135)

1. `TestMirrorLogUnchangedLogWritesNothing`: a database-branch mirror (model
   on `TestMirrorLogWritesARow`) where the local row already holds N bytes and
   the fake server reports `Honored, From: N, Size: N` with an empty body.
   Want: the stored row is byte-identical and was not rewritten -- assert via
   whatever the round-file row records that changes on every put (e.g. an
   updated-at or a write counter); if nothing observable changes on a
   rewrite of identical bytes, wrap the store's put in the test's fake or
   count it, and if neither is possible without touching `internal/store`,
   stop and report.
2. `TestMirrorLogAppendsNewBytes` (or confirm `TestMirrorLogAppends` /
   `TestMirrorLogWritesARow` already pins): N bytes local, server returns M
   new bytes -> row is N+M bytes.
3. `TestSyncRemoteUnlessDaemonSkipsWhileDaemonRuns`: take the daemon lock
   with `rt.Store.AcquireDaemonLock()` in the test (release in cleanup); call
   `SyncRemoteUnlessDaemon` with one running remote binding. Want:
   `skipped == true`, `fakeRemote.calls` empty.
4. `TestSyncRemoteUnlessDaemonSyncsWithoutDaemon`: same binding, no daemon
   lock. Want: `skipped == false`, the fake saw `GetBinding` for it.

Mutation checks: (a) remove the empty-body early return -- test 1 must fail;
(b) make `SyncRemoteUnlessDaemon` ignore `DaemonRunning` -- test 3 must fail.
Restore after each.

## 6. Error handling

- `DaemonRunning` failing is not fatal: sync as today (a missed skip only
  costs contention; a missed sync could leave `wait` blind with no daemon).
- `mirrorLog` keeps logging and swallowing its own errors, unchanged.

## 7. Working efficiently

- Read in one batch: `remote.go` 600-780, 800-1080; `wait.go` 170-220;
  `cmd/relevo/status.go` 70-110; `reconcile.go` 160-185;
  `internal/store/daemonlock.go`; `remote_test.go` 100-200 and 2340-2720.
- One edit call per file.
- Focused loop: `go build ./... && go test ./internal/relevo -run 'Mirror|SyncRemote|Wait' -count=1`.
- Then `go test ./cmd/relevo -count=1` once (the status contract goldens).
- Full check once, at the end, in the foreground: `make check`.

## 8. Ordered steps

1. Fast-forward to origin/main (§0).
2. A: the `mirrorLog` early return, tests 1-2. Focused tests pass.
3. B: `SyncRemoteUnlessDaemon`, the two call sites, tests 3-4. Focused tests
   pass; `cmd/relevo` tests pass with no golden change (if a golden changes,
   stop and report).
4. Mutation checks (§5). Report each failing test name.
5. `make check` in the foreground. `git diff --stat` shows only §2 files.
   Commit: `fix(remote): mirror writes only when the log grew; status and wait leave remote sync to a running daemon (#513)`.
6. **Save the plan.** Copy this plan to
   `docs/plans/2026-09-26-fix-513b-remote-mirror.md` and commit it.

Report: the diff stat, how test 1 observes "not rewritten", the mutation
checks' failing test names, and the `make check` result.

## Round 2

### Fix #513 (part B), round 2 -- align SyncRemote's state filter with the daemon, then finish change B

## 0. Rules

- This tree is `relevo/fix-513b` with **uncommitted round-1 work**: change A
  (the `mirrorLog` empty-body early return in `internal/relevo/remote.go`) and
  its test `TestMirrorLogUnchangedLogWritesNothing` in `remote_test.go`. Keep
  it; do not reset or stash it.
- First: `git fetch origin`, then commit the round-1 work as a WIP commit
  (`git add -A && git commit -m wip`), `git rebase origin/main`, then
  `git reset --soft HEAD~1` to un-commit it again. If the rebase conflicts,
  stop and report.
- If a step is impossible as written, or the code contradicts this plan, **stop
  and report** -- do not improvise.
- **Run every check in the foreground and wait for it.** Never run `make check`
  or a test as a background task, and never end your turn to wait for one:
  this process exits when your turn ends and nothing will wake it.
- Comments: *why* only, no issue numbers, no `§`, no history. Functions <= 70 lines.
- Touch only: `internal/relevo/remote.go`, `internal/relevo/remote_test.go`,
  `internal/relevo/wait.go`, `cmd/relevo/status.go`, and the plan file (§4).
- `cmd/relevo` tests must not spawn a harness or reach the network: the new
  rule is tested in `internal/relevo`.

## 1. Context

Remote bindings' builder logs are mirrored into the local database by
`mirrorLog` from `observeRemote`, which runs from the daemon's reconcile and
from `SyncRemote` (`internal/relevo/remote.go` ~1036). `relevo status`
(`cmd/relevo/status.go` ~93) and every poll of `relevo wait`
(`internal/relevo/wait.go` ~190-196) call `SyncRemote`, each under the global
state lock with HTTP calls inside it, while the daemon is already doing the
same work. Round 1 made `mirrorLog` skip rewriting an unchanged log (change A,
done, uncommitted). Change B -- read verbs skip `SyncRemote` when a daemon is
running -- was halted because the filters differ:

- `Reconcile` (`internal/relevo/reconcile.go` ~172) returns early for
  `StateDone` **and `StatePaused`**, so the daemon never observes a paused
  remote binding.
- `SyncRemote` (`remote.go` ~1049) skips only `StateDone`.

Decision: `StatePaused` is a legacy state (no verb sets it any more; only old
data carries it) and a paused binding is by definition not being relayed. So
`SyncRemote` skips `StatePaused` too, matching the daemon. Then the daemon
covers every binding `SyncRemote` does, and change B is safe.

## 2. Changes

```
remote.go SyncRemote loop filter:
  if !b.Builder.Remote() || b.State == store.StateDone || b.State == store.StatePaused { continue }
  (one short why-comment: a paused binding is not relayed, the daemon skips it too)

remote.go, next to SyncRemote:
SyncRemoteUnlessDaemon(ctx context.Context, rt Runtime) (synced int, skipped bool, err error)
  rt.Remote == nil                    -> (0, false, nil)
  running, perr := rt.Store.DaemonRunning()   (internal/store/daemonlock.go ~48)
  perr == nil && running              -> (0, true, nil)   no network, no lock
  otherwise                           -> synced, err := SyncRemote(ctx, rt); return synced, false, err
  (a failed probe syncs: a missed skip costs contention, a missed sync could
   leave wait blind with no daemon)

cmd/relevo/status.go ~93: replace the relevo.SyncRemote call with
  relevo.SyncRemoteUnlessDaemon; the stderr message text stays identical.
internal/relevo/wait.go ~195: `_, _, _ = SyncRemoteUnlessDaemon(ctx, rt)`;
  keep the comment's why (with no daemon the poll is the only collector).
```

## 3. Tests (`internal/relevo/remote_test.go`, using `fakeRemote` ~135)

1. `TestSyncRemoteSkipsPausedBinding`: a remote binding in `StatePaused`;
   `SyncRemote` makes no `GetBinding` call for it.
2. `TestSyncRemoteUnlessDaemonSkipsWhileDaemonRuns`: hold
   `rt.Store.AcquireDaemonLock()` (release in `t.Cleanup`); one running remote
   binding. Want `skipped == true`, `fakeRemote.calls` empty.
3. `TestSyncRemoteUnlessDaemonSyncsWithoutDaemon`: same, no daemon lock. Want
   `skipped == false` and a `GetBinding` call for it.

Mutation checks: (a) remove the empty-body early return -- 
`TestMirrorLogUnchangedLogWritesNothing` fails; (b) ignore `DaemonRunning` --
test 2 fails; (c) drop the `StatePaused` skip -- test 1 fails. Restore after each.

## 4. Steps

1. Rebase with the round-1 work preserved (§0). `go build ./...` passes.
2. §2 and §3. Focused: `go build ./... && go test ./internal/relevo -run 'Mirror|SyncRemote|Wait' -count=1`,
   then `go test ./cmd/relevo -count=1` once (no golden may change; if one
   does, stop and report).
3. Mutation checks (§3).
4. `make check` in the foreground. `git diff --stat` shows only the §0 files.
   Commit: `fix(remote): mirror writes only when the log grew; status and wait leave remote sync to a running daemon (#513)`.
5. Save this plan to `docs/plans/2026-09-26-fix-513b-remote-mirror.md` and commit it.

Report: the diff stat, the mutation checks' failing test names, and the
`make check` result.
