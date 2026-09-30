# Plan: #715 — `TestListenerSurvivesARealReexecUnderLoad` flakes (hold-connection handshake times out)

One round on `relevo/reexec-flake`, one commit. The flake (1 of 3 runs on macOS, 5/5 green on Linux) is the hold connection's handshake: `reexec_test.go` opened the hold transaction *after* the fsync'd load loop was already running, and the owner's handshake path has no blocking path, so the production two-second `handshakeTimeout` was reached by scheduling starvation rather than by any real break.

## Problem, in the code

- Three client connections in the test process go through `client.openConn` (`internal/db/wire/client/client.go:67-79`), all bounded at `client.go:68` by the production `handshakeTimeout` (2 s, `client.go:21-23`): the load pool (`reexec_test.go:341`), the hold pool (`holdTransaction`), and the queued pool (`queueRequest`).
- The hold open ran *after* the load goroutine started and was issuing an fsync'd `INSERT` every 10 ms. The owner handshake path has no blocking path, so `hold conn: … i/o timeout` was scheduling starvation caused by the test's own load.
- `db.Dial`'s `dialTimeout` (`internal/db/dial.go:17`, used by `ProbeOwner` at `internal/db/ownerstatus.go:16`) is **not on this test's paths**: `reexec_test.go` only calls `sql.Open(client.DriverName, sock)` (openConn), and the helper daemon opens the file with `modernc.org/sqlite` directly plus `owner.New`. It is therefore untouched.

## Decisions (the MasterMind's six, in the two places they override the issue's "Fix direction")

1. The queued connection is **not** pre-opened: pre-opening would move its connect out of the drain gap, which is the one thing the test exists to exercise (`queueRequest:263` sleeps 100 ms then the goroutine's `Exec` lands inside the window).
2. The knob follows `SetDialer` (`dialer.go:10-16`), not an internal-only change: `maxConns` in `internal/db/wire/owner/owner.go:22` is unexported and manipulable only from its own package (`owner_test.go:202-204`), while this knob must be set from `package owner`'s test (`reexec_test.go:24` already imports `client`). That forces an exported setter with a `!unix` stub.
3. `Info` stays on the caller's ctx: it never reads the knob.
4. `db.dialTimeout`, the owner's drain, `closeDrainTimeout` and the production 2 s bound all stay.
5. Nothing is deleted (see below).
6. The issue's follow-up (helper stderr + goroutine dump on timeout) stays out of scope unless the flake recurs.

## The reorder (test change)

`holder := holdTransaction(t, sock)` moved from just after the load goroutine started to just before it, directly after `t.Cleanup(func() { _ = loadDB.Close() })`. Nothing else moves: `signalAt`, `SIGUSR1`, `queueRequest`, the 300 ms sleep, `COMMIT`, `awaitAdoption`, `awaitQueued`, the `os.SameFile` check, `close(stop)`, `assertLoadFailuresWithin` and `assertServing` keep their order and content.

Why it removes the observed failure: the hold's dial, handshake and `BEGIN` now complete while the owner is idle — before the test creates the fsync'd load loop it deliberately runs. The timeout happened precisely because the hold competed with the load loop; after the move that competition cannot exist, and the test still exercises everything it is about (signal, drain, a queued connect landing inside the gap, commit, adoption, same inode, failures only inside the window).

## The knob (client change)

- New unix file `internal/db/wire/client/timeout.go`: `var handshakeBudget = handshakeTimeout` (process-wide, mirroring `dialer`) and `func SetHandshakeTimeout(d time.Duration)`. A non-positive `d` restores `handshakeTimeout`; it affects connections opened after the call only. The comment says why: a test that deliberately starves the owner raises its own client budget, and no production caller does.
- `client.go:68` reads `handshakeBudget` instead of the const — the single production call site changed. Everything else keeps its timeout: the `handshakeTimeout` const keeps its 2 s and its comment, `Info` (`client.go:46-57`) keeps the caller's ctx deadline and never reads the knob, `closeDrainTimeout` (`session.go:33`), the owner's drain and `db.dialTimeout` are untouched.
- `unsupported.go` gets the no-op stub `func SetHandshakeTimeout(time.Duration) {}` (+ `time` import), next to the existing `SetDialer` stub, because CI cross-compiles `windows/amd64`.
- Restore: both call sites call `SetHandshakeTimeout(0)` in `t.Cleanup`. In the owner test the restore is registered first, so it runs last (LIFO), after every pool Close.

Owner test usage: `client.SetHandshakeTimeout(10 * time.Second)` at the top of `TestListenerSurvivesARealReexecUnderLoad`, after `reexecRoots`. Ten seconds is several times any observed stall, stays below the package's test timeout, and stays above the test's own watchdogs (`awaitAdoption` 5 s, `awaitQueued` 5 s, `waitForFile` 10 s), so a real break still surfaces. The helper child process is the server and opens no client connection; the knob is a Go var, not an env var, so it cannot reach the re-exec'd helper. The queued request's connect stays inside the drain window: the knob — not a pre-open — is its headroom.

## Tests for the knob (`internal/db/wire/client/timeout_test.go`, package `client_test`)

- `TestHandshakeTimeoutBoundsPooledHandshake`: a `SetDialer` hook records the deadline carried by the ctx it receives. A never-answering listener (`shortListener`) leaves the pooled connection blocked in the welcome read. With `SetHandshakeTimeout(50 * time.Millisecond)`, a pooled `sql.Open`+Exec must fail and the recorded deadline must be within the tiny budget — a deterministic check, not wall-clock elapsed. Then `SetHandshakeTimeout(0)` and a second open (hook records and refuses immediately) must see a ~2 s deadline: the default is back.
- `TestInfoKeepsTheCallersDeadline`: with the knob raised to 5 s, `client.Info(ctx, sock)` with a 100 ms caller ctx must still dial with a ~100 ms deadline — pins that `Info` is not on the knob.
- No `t.Parallel` is added in either package (none exists under `internal/db/wire`).

## Mutation results

Each mutation was applied alone, the focused tests run, and the mutation reverted. All three are pinned by `TestHandshakeTimeoutBoundsPooledHandshake` / `TestInfoKeepsTheCallersDeadline`:

1. `client.go:68` reads the const `handshakeTimeout` again — `TestHandshakeTimeoutBoundsPooledHandshake` fails: `the pooled handshake deadline was 1.999990798s, want it within the 50ms budget`.
2. `SetHandshakeTimeout` treats `0` as a real budget (the `d <= 0` restore removed) — `TestHandshakeTimeoutBoundsPooledHandshake` fails: `the restored pooled handshake deadline was -17.144µs, want the default 2s`.
3. `Info` reads `handshakeBudget` (its dial ctx derived from `context.Background()` with the knob) — `TestInfoKeepsTheCallersDeadline` fails: `Info dialled with a 4.999997991s deadline, want the caller's ~100ms`.

The reorder is deliberately **not** mutation-pinned, and this was checked rather than assumed: with the hold moved back after the load goroutine started, `go test -race -count=1 ./internal/db/wire/owner/ -run TestListenerSurvivesARealReexecUnderLoad` and `go test -race -count=1 ./internal/db/wire/client/` both stay green. No test distinguishes the order, and none is invented.

## Focused commands

- `go test -race -count=1 -v ./internal/db/wire/client/ -run 'TestHandshakeTimeoutBoundsPooledHandshake|TestInfoKeepsTheCallersDeadline'` — both PASS.
- `go test -race -count=5 ./internal/db/wire/owner/ -run 'TestListenerSurvivesARealReexecUnderLoad'` — ok (3.4 s).
- `go build ./...` and `CGO_ENABLED=0 GOOS=windows go build ./...` — green.
- `git diff` on `internal/db/wire/owner/reexec_test.go` shows only the hold line moved plus the two knob lines; no assertion, helper or `t.Parallel` change.
- `make check` at the end: see the round's report.

## Risks

- Does the longer test budget hide a real regression? Per assertion: same-inode (`os.SameFile`) has no timeout on its path — unaffected. Queued request served — `awaitQueued`'s 5 s watchdog still fires first; a 10 s handshake budget cannot make a never-served request pass. Failures only inside the window — a longer budget can turn a starving-but-working setup into a pass (the point) and can delay a genuine failure to up to 10 s, but that failure lands after the window and is still reported; it cannot create a false pass. Adoption and `assertServing` — own watchdogs/errors, only slower on a real break. Net: slower failure, never a hidden one.
- Does the knob leak between tests? Owner and client tests are separate binaries; within each, tests are sequential; both call sites restore in `t.Cleanup` and the owner test's passing path synchronizes (`wg.Wait`, queued receive) before cleanup. The helper process never sees the value. Residual plain-var shape matches the existing `SetDialer` tests; if `-race` ever flags it on a fatal path, make the read atomic.
- Moving the hold before the load loop also moves its `t.Cleanup` registrations earlier, so on teardown the load pool now closes before the hold pool (LIFO). No assertion depends on teardown order.
- The macOS flake cannot be reproduced here (1 of 3 on macOS; 5/5 green on Linux). The honest claim is "removed the known contention and gave every client setup headroom", confirmed only by later macOS CI runs.

## Files this round touches (closed)

1. `internal/db/wire/client/timeout.go` (new)
2. `internal/db/wire/client/client.go` (one line at `:68`)
3. `internal/db/wire/client/unsupported.go` (stub + `time` import)
4. `internal/db/wire/client/timeout_test.go` (new)
5. `internal/db/wire/owner/reexec_test.go` (reorder + knob set/restore)
6. `docs/plans/2026-09-30-reexec-under-load-flake.md` (new)

Plus `testdata/coverage-baseline.txt` only if the guard moves (not expected).

## Deletions

Nothing. The round removes no behaviour: no assertion, helper, env var, timeout const or production call site is deleted; the hold line is moved, not dropped; no `t.Parallel`; no `.golangci.yml`/allow-list entry; no baseline line lowered. The owner package's serving path and `docs/specs` are unchanged, and the production 2 s bound still holds.
