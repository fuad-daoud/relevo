# Serve the owner before the startup passes

Fixes #1105.

## The failure

`cmdDaemon` bound the owner socket early and served it only after the one-time
passes had finished: the mirror dedupe, the history compression, the change-
capture drop, and the split plus origin backfill. A client that connected during
that window met a bound socket with nobody accepting on it. It spent its whole
`ownerStartWait` -- fifteen seconds -- and was then told the daemon was starting
or unresponsive, even though the daemon was running and its database was
healthy. On a large database the passes are open-ended, so the window was
open-ended too, and a plain read -- which needs no write lock and would have
succeeded against a serving owner -- failed anyway.

## The choice

Serve the socket first and run the passes after it, under the owner's normal
write path.

- No protocol change. `Hello`/`Welcome`/`Refusal` and the client handshake are
  untouched, so a client in a mixed-version window reads every answer exactly as
  it did before.
- The owner already answers handshakes and queues work under load: each
  connection gets its own goroutine, a handshake never waits on the pinned
  connection cap, and a pass and a client write serialize on the one shared pool
  with busy mapping and begin-retry.
- The one thing serve-first costs is the in-place compaction each pass wanted.
  `Vacuum` closes the pool and reopens it, which would strand every client the
  owner is answering, so it refuses a served handle. That refusal is right for a
  vacuum a person asked for and wrong for a pass whose deletes and conversions
  have already committed: each pass now skips the reclaim and says so in one
  line naming itself and the reason.

## Ordering

The order a start runs in is the whole of the fix, so it lives in one place.

1. `serveOwner` over the one handle, before anything else.
2. The one-time passes, in the order they have always run in: dedupe, then the
   history compression that runs on the smaller database the dedupe left, then
   the change-capture drop, then the installation row with the split and the
   origin backfill behind it.
3. The sync verb executor, installed only once both of those last two passes
   recorded themselves.

A verb is the one request that pushes rows off this machine, and the split and
the backfill are the passes that must finish before any row may leave it. A verb
arriving while they run is refused with the owner's existing no-executor code,
which a client of any version already reads; the executor is installed once they
are done, and a database whose split failed keeps refusing it rather than
exporting the very rows the split exists to move.

## What each pass costs a concurrent client

- **split.** A machine-local row is written through the routing every local
  surface binds, so it lands in the local file while the pass is still reading
  the shared one. The pass sees a local row that disagrees with the shared copy,
  keeps the local row -- it is what the running system is already using --
  converges the stray away and counts the disagreement. A row written before the
  pass reads its table is moved; a row written after is a stray the next start
  converges, which is what the marker-present branch is for.
- **backfill.** It stamps only rows carrying no origin, so nothing is stamped
  twice, and its marker is believed only over counts that are empty, so a row
  that arrived after the pass is not skipped: the next pass runs and stamps it.
- **dedupe.** Every planned row is deleted inside one transaction, so a reader
  alongside it sees the row or its absence, never a partial one.
- **compress.** Each batch updates a row's value and its codec in one statement,
  so a reader can never meet bytes the codec beside them does not describe.

## Deleted behaviour

- In-place `VACUUM` file compaction during a startup pass while the owner serves.
  Deletes and conversions still happen at startup; only the reclaim is deferred,
  and the freed pages stay on the file's free list for the engine to reuse.
- The bound-but-silent startup window. A starting daemon no longer holds an
  unserved socket through its passes.

## Cases

- A startup pass stalled past `ownerStartWait` -> a client dial and read still
  succeed, inside one dial budget, with no waiting line.
- A client write racing a pass -> queued under begin-retry and succeeds; never a
  silent failure at the start wait.
- A sync verb arriving before the split and backfill complete -> refused with the
  existing no-executor code; served normally after.
- Reclaim on a served handle -> skipped, no `VacuumErr` recorded; a direct,
  unserved handle still compacts for real.
- A genuinely wedged owner, bound and never serving, still fails at the start
  wait. That is the limit the wait was built for and it is unchanged.

## Tests

- `TestStalledStartupPassStillAnswersAClient` kills "serve moved back after the
  passes": the client then waits the whole start wait and is refused.
- `TestSyncVerbIsRefusedUntilTheStartupPassesFinish` kills "install the executor
  with the serve", and its second half kills "never install it".
- `TestSyncVerbStaysOffWhenTheEnablePathDidNotFinish` kills "install it whatever
  the passes reported".
- `TestSplitKeepsAWriteThatRacesTheMove` kills the conflict rule that keeps a
  racing local row.
- `TestSplitMovesAWriteThatRacesAMovedTable` pins the other side of the move.
- `TestBackfillNeverSkipsAndNeverStampsTwice` kills both a lost `origin = ''`
  predicate and a marker trusted without the gate.
- `TestCompressLeavesEveryRowReadableWhileItRuns` and
  `TestDedupeMirrorOnceKeepsReadersCorrectDuringTheDelete` pin the reader side.
- `TestDropTursoCdcSkipsTheReclaimOnAServedHandle`,
  `TestReclaimVacuumsAnUnservedHandle` and
  `TestReclaimRecordsNothingOnAServedHandle` pin the skip and the compaction it
  replaced.

The stall test binds a real unix socket and serves a real handle, reads through
a real dial, and starts no subprocess and opens no network connection.
