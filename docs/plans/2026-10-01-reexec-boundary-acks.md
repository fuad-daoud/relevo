# Plan: #768 — the re-exec-under-load flake: bracket the tx ack, arm the helper, make the window deterministic

Issue: #768. Base `origin/main`. One builder round, **one commit, new commits
only**. Scope: `internal/db/wire/owner/` plus this plan. No production timeout
change (`drainDeadline`, `closeDrainTimeout`, `handshakeTimeout`), no new
exported API, no client change.

## The three seams

1. **A committed statement could be cut before its ack.** `(*conn).exec`
   (`internal/db/wire/owner/conn.go:310-337`) called `c.recordTransaction` —
   which clears `c.inTx` for COMMIT/END/ROLLBACK — before writing the Done
   frame. `Drain` (`owner.go:99-108`) returns as soon as `anyInTx()`
   (`owner.go:133-161`) sees no transaction, then `dropClients`
   (`owner.go:165-176`) closes the socket, so a client could lose a COMMIT that
   had already succeeded and surface it as `ErrConnLost`, which it never
   retries. BEGIN already recorded before its Done.
2. **Ready preceded the armed signal.** `runReexecHelper`
   (`reexec_test.go:104-141`) wrote the ready file before installing
   `signal.Notify`. A SIGUSR1 landing in that window had no watcher and was
   dropped.
3. **The window was guessed by sleeps.** `queueRequest` slept 100 ms before its
   dial and the test slept another 300 ms before the COMMIT. Under `-race` and
   sharded CI those sleeps are the race.

All transaction boundaries reach `exec`: the client's `Begin`/`Commit`/`Rollback`
all call `ExecContext` (`client/session.go:212-232`), never the query path.

## The four decisions, as built

1. **Arm the SIGUSR1 watcher before serving; write ready only after.** In
   `runReexecHelper`, `signal.Notify` now runs before `go srv.Serve(ln)`, and the
   ready file is written last, so a signal seen once ready exists is never
   dropped into the gap between the write and the handler. Correct by ordering
   alone: nothing in-process can force a signal into that runtime-internal
   interval, and the child cannot be instrumented there, so this decision has no
   test. (This is "Decision 1" in the seed's Risks.)
2. **Expire the listener deadline before writing the draining marker.** On
   SIGUSR1 the helper asserts `interface{ SetDeadline(time.Time) error }` on the
   listener and expires it, then writes the marker, then calls `Drain`. The old
   image therefore accepts nothing after the marker, so a dial made once the
   marker exists can only land in the kernel backlog for the next image, and the
   drain's start no longer races the test's COMMIT. The discarded `Serve` error
   is fine because nothing here closes the listener — the fd is inherited by the
   next image. (This is "Decision 2" in the seed's Risks.)
3. **Bracket the transaction membership around the ack.** In `conn.exec`, a
   BEGIN records its membership before the Done is sent; a COMMIT, END or
   ROLLBACK clears it only after the Done is written; a non-boundary statement
   changes nothing; the failed `ExecContext` path returns before any membership
   change. The flag is therefore never wrong while an answer is on the wire.
   `query`'s single `recordTransaction` is deliberately unchanged, and the
   `recordTransaction` comment now records why the order matters. This is pinned
   by the COMMIT test, with the mutation recorded below. (This is "Decision 3"
   in the seed's Risks.)
4. **Wait on the marker instead of sleeping.** `queueRequest` loses its 100 ms
   sleep and the test loses its 300 ms sleep; the test instead waits for the
   helper's draining marker with `waitForFile`, then dials the queued request,
   then sends the COMMIT. What is verified: the test now depends on the marker
   (drop the write and the wait fails), and the queued dial can no longer be
   accepted by the old image. The sleeps were never observed losing on Linux —
   the old test is green under `-race` — so no test distinguishes them from the
   marker, and none was invented to claim it.

## The white-box tests (`internal/db/wire/owner/txack_test.go`, new)

Both build a real `conn` with `newConn` (`owner.go:247-249`) and `c.pin`
(`conn.go:284-308`) over a test `net.Conn` whose `Write` reports that it was
entered and then blocks until released, so the transaction state can be read
while a Done frame is mid-write.

- `TestACommitStaysInTransactionUntilItsAckIsWritten`: a BEGIN through `exec`;
  the gate is armed; the COMMIT runs in a goroutine; while its Done write is
  blocked the connection must report `transactionOpen() == true`; after the
  release it must report `false`.
- `TestABeginIsInTransactionBeforeItsAckIsWritten`: the same gate on BEGIN;
  `true` while blocked. A regression guard: it passes before and after.

## Mutation and the tests' output

Step 1, on today's order (BEGIN passes, COMMIT fails):

```
=== RUN   TestACommitStaysInTransactionUntilItsAckIsWritten
    txack_test.go:119: a COMMIT left the connection out of its transaction before its ack was written
--- FAIL: TestACommitStaysInTransactionUntilItsAckIsWritten (0.01s)
=== RUN   TestABeginIsInTransactionBeforeItsAckIsWritten
--- PASS: TestABeginIsInTransactionBeforeItsAckIsWritten (0.00s)
FAIL
FAIL	github.com/fuad-daoud/relevo/internal/db/wire/owner	0.020s
FAIL
```

After the `exec` change, both pass:

```
=== RUN   TestACommitStaysInTransactionUntilItsAckIsWritten
--- PASS: TestACommitStaysInTransactionUntilItsAckIsWritten (0.01s)
=== RUN   TestABeginIsInTransactionBeforeItsAckIsWritten
--- PASS: TestABeginIsInTransactionBeforeItsAckIsWritten (0.00s)
PASS
ok  	github.com/fuad-daoud/relevo/internal/db/wire/owner	1.022s
```

Mutation — the clear moved back before the send, then restored; the COMMIT test
fails with the exact message above:

```
=== RUN   TestACommitStaysInTransactionUntilItsAckIsWritten
    txack_test.go:119: a COMMIT left the connection out of its transaction before its ack was written
--- FAIL: TestACommitStaysInTransactionUntilItsAckIsWritten (0.01s)
=== RUN   TestABeginIsInTransactionBeforeItsAckIsWritten
--- PASS: TestABeginIsInTransactionBeforeItsAckIsWritten (0.00s)
FAIL
FAIL	github.com/fuad-daoud/relevo/internal/db/wire/owner	0.020s
FAIL
```

## Commands and results

- `go test -race -count=1 -v ./internal/db/wire/owner/ -run 'TestACommitStaysInTransactionUntilItsAckIsWritten|TestABeginIsInTransactionBeforeItsAckIsWritten'` — BEGIN PASS, COMMIT FAIL before the `exec` change; both PASS after.
- `go test -race -count=1 ./internal/db/wire/owner/` — ok.
- `go test -race -count=1 ./internal/db/wire/owner/ -run TestListenerSurvivesARealReexecUnderLoad` — ok.
- `go test -race -count=50 ./internal/db/wire/owner/ -run TestListenerSurvivesARealReexecUnderLoad` — ok (6.087s).
- `make check` — green. `internal/db/wire/owner` report line: `ok  	github.com/fuad-daoud/relevo/internal/db/wire/owner	4.543s	coverage: 68.0% of statements` — above the 67.9 baseline, not regenerated or lowered.

## Files this round touches (closed)

1. `internal/db/wire/owner/conn.go`
2. `internal/db/wire/owner/reexec_test.go`
3. `internal/db/wire/owner/txack_test.go` (new)
4. `docs/plans/2026-10-01-reexec-boundary-acks.md` (new)

Nothing else moves: no `internal/db/wire/client/`, no const, no `.golangci.yml`,
no allow-list and no coverage baseline line changes.

## Deletions (closed list)

1. `reexec_test.go` — `time.Sleep(100 * time.Millisecond)` in `queueRequest`.
2. `reexec_test.go` — `time.Sleep(300 * time.Millisecond)` in `TestListenerSurvivesARealReexecUnderLoad`.
3. `conn.go` — the clear-before-ack ordering for COMMIT/END/ROLLBACK; the
   membership change moves after the Done.
4. Nothing else is deleted: no assertion, watchdog, env const, helper, timeout,
   exported symbol, lint/coverage exclusion or baseline line is removed. The
   ready write and the SIGQUIT dump move/stay rather than disappear.

## Risks

- **Decision 1 has no test.** The lost-signal window is a runtime-internal
  interval between the ready write and `signal.Notify`; nothing in-process can
  force a signal into it, and the child cannot be instrumented there. It is
  correct by ordering alone.
- **Decision 2 has no named failing test.** The sleeps were never observed
  losing on Linux (the old test is green under `-race`), so no test
  distinguishes them from the marker; saying otherwise would be bending a test.
  What is verified: the test now depends on the marker (drop the write and the
  wait fails), and the queued dial can no longer be accepted by the old image.
- **Decision 3 is pinned** by the COMMIT test, with the mutation recorded. The
  BEGIN half is a regression guard, honestly labelled as passing before and
  after.
- **The macOS errno is not reproduced locally.** Local green plus `make check`
  is not proof; macOS CI on the PR is the acceptance.
- Removing the sleeps does not weaken `assertLoadFailuresWithin`: its window
  still starts at `signalAt`, before the marker.
