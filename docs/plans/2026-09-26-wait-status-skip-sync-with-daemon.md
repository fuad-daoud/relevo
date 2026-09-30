# relevo wait and relevo status: skip the remote sync while the daemon runs (#535, step 1) (2026-09-26)

## 1. Overview

`SyncRemote` (`internal/relevo/remote.go`, `func SyncRemote`, ~line 1036) observes every remote binding inside
`rt.Store.WithLock`. `observeRemote` does network I/O to the server: `GetBinding`, log and drift mirroring, and the
catch-up downloads. So the machine-wide state lock is held across HTTP round trips.

Two callers run `SyncRemote` on their own:
- `Wait` (`internal/relevo/wait.go`, in its poll loop, ~line 195: `_, _ = SyncRemote(ctx, rt)`), on **every poll**;
- `cmdStatus` (`cmd/relevo/status.go`, ~lines 92-96).

With several waits running, the state lock is almost never free, and `relevo status` and the cockpit hang.

The daemon already reconciles every remote binding each tick (`reconcileRemote`). `SyncRemote` exists so that a
closed remote round is collected **when no daemon runs**, as its doc comment and the comment in `Wait` both say.

**This round:** both callers run the sync only when no daemon is running. Step 2 of #535 (moving the daemon's own
network calls out of the lock) is **not** part of this round.

## 2. Files

```
internal/relevo/remote.go        + SyncRemoteIfNoDaemon
internal/relevo/wait.go          the poll loop calls SyncRemoteIfNoDaemon
cmd/relevo/status.go             cmdStatus calls SyncRemoteIfNoDaemon
internal/relevo/remote_test.go   (or a new sync_daemon_test.go) + the tests of §4 step 3
docs/plans/2026-09-26-wait-status-skip-sync-with-daemon.md
```

## 3. Contract

```
// SyncRemoteIfNoDaemon runs SyncRemote only when no daemon holds the daemon lock: a running daemon already
// observes every remote binding each tick, and a second observer only adds network I/O under the state lock.
func SyncRemoteIfNoDaemon(ctx context.Context, rt Runtime) (ran bool, synced int, err error)
```
- **When it runs:** `rt.Remote == nil` → `(false, 0, nil)`. Otherwise it asks `rt.Store.DaemonRunning()`
  (`internal/store/daemonlock.go:48`).
  - `true` → `(false, 0, nil)`: no `GetBinding`, no lock taken.
  - `false` → `ran = true`, and `synced, err` from `SyncRemote(ctx, rt)`.
  - An error from `DaemonRunning` is treated as "no daemon": sync, as before, so a lock-file problem never hides a
    closed round.
- **Callers:**
  - `Wait` replaces `_, _ = SyncRemote(ctx, rt)` with `_, _, _ = SyncRemoteIfNoDaemon(ctx, rt)`. Update the comment
    above it to say the daemon observes when it runs.
  - `cmdStatus` replaces its `relevo.SyncRemote` call with `relevo.SyncRemoteIfNoDaemon`, and keeps printing `serr`
    the same way when it is non-nil.
- **No other caller changes.** In particular, `stop.go`'s direct `observeRemote` after a remote stop stays.

## 4. Steps

### 0. Working efficiently

**How to work:**
- In one batch, read:
  - `internal/relevo/remote.go:1000-1080`, `internal/relevo/wait.go:150-245`, `cmd/relevo/status.go:80-100`;
  - `internal/store/daemonlock.go`;
  - `internal/relevo/remote_test.go:40-140` (`fakeRemote`);
  - one existing test that runs `Wait` against a remote binding (search `remote_test.go` / `wait_test.go` for
    `fakeRemote` and `Wait(`).
- Make each file's change in one edit.

**Commands:**
- First run `mkdir -p $HOME/.cache/go-tmp`, then `export TMPDIR=$HOME/.cache/go-tmp GOTMPDIR=$HOME/.cache/go-tmp`.
  Contabo's `/tmp` is a small tmpfs.
- Focused: `go test ./internal/relevo/ -run 'SyncRemote|Wait' -count=1 -race`
- Final:
  - `go test ./internal/relevo/ ./cmd/relevo/ ./internal/store/ -count=1 -race`
  - `go vet ./internal/relevo/ ./cmd/relevo/`
  - `test -z "$(gofmt -l $(git ls-files '*.go'))"`
  - `sh scripts/check-comments.sh`
  - `sh scripts/check-filesize.sh`
- If `git ls-files` fails in this worktree, run `gofmt -l` and `check-comments.sh` on the touched files by hand, and
  say so.
- Skip `make check`. **No test in `cmd/relevo`**: CI runners have no harness binary and no network. The rule is
  tested in `internal/relevo`.
- Comments say *why*, with no issue numbers, `§` or plan references. Test names say what they pin.

### 1. `SyncRemoteIfNoDaemon` (§3), in `remote.go` below `SyncRemote`

### 2. The two callers (§3)

### 3. Tests (`internal/relevo`)

- **a. `TestSyncRemoteSkippedWhileDaemonRuns`:**
  - Use a runtime with a `fakeRemote` and one remote binding, as the existing remote tests build one.
  - Hold the daemon lock with `rt.Store.AcquireDaemonLock()` and release it in `t.Cleanup`.
  - `SyncRemoteIfNoDaemon` returns `ran == false`, and the fake records **zero** `GetBinding` calls. Add a call
    counter to `fakeRemote` if it has none.
- **b. `TestSyncRemoteRunsWithoutDaemon`:** the same, without the lock. `ran == true`, and the fake records at least
  one `GetBinding` call.
- **c. `TestWaitDoesNotPollTheServerWhileDaemonRuns`:**
  - Set up a remote binding with an open round, as an existing remote `Wait` test does.
  - Hold the daemon lock, and run `Wait` with a short timeout and a short interval, so it polls several times and
    times out.
  - The result is `WaitTimeout`, and `GetBinding` was called zero times.

**Required mutation:** make `SyncRemoteIfNoDaemon` ignore `DaemonRunning` and always sync. Tests a and c must fail.
Report the failing lines, then revert.

### 4. Checks, the plan, the commit

1. Run the final commands listed in step 0.
2. Copy this plan to `docs/plans/2026-09-26-wait-status-skip-sync-with-daemon.md`.
3. `git add -A && git commit -m "fix(relevo): wait and status leave remote observation to a running daemon (#535)"`.
   A new commit.

## 5. Deletions

None. `SyncRemote` itself is unchanged.

## 6. Stop rather than improvise

Halt and report if any of these happens:
- `Wait` or `cmdStatus` call `SyncRemote` somewhere other than where §1 says;
- `DaemonRunning` would itself block. It must stay a non-blocking try-lock, as it is today.
- an existing `Wait` remote test relied on `Wait` syncing while a daemon lock was held.
