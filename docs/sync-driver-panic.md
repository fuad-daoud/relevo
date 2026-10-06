# The sync driver's process-killing abort, and what this tree does about it

This is the evidence behind `internal/sync.DriverVersion` and the two transfer
thresholds on the seed open. It records what was found, what was changed, and
what is still unknown, so the next person to look at a driver bump does not have
to repeat the search.

## What was observed

```
thread '<unnamed>' panicked at core/storage/wal.rs:3638:9
tursogo.turso_sync_operation_resume / TursoSyncDb.driveOpUntilDone
TursoSyncDb.Pull (driver_sync.go:305)
internal/sync.(*Turso).Pull (internal/sync/turso.go:35)
internal/sync.(*Enabler).runSeed (internal/sync/enable.go:379)
VerbRunner.enable -> WaitSyncSlot
```

A `--seed-uploaded` enable, the existing-history seed case, against a remote
holding a just-imported 211MB seed, with 211MB of live local history. The first
attempt failed as `wire: connection lost ... EOF`; the retry reported
`already enabled` with the machine marked on and no handle.

The Go frames are the whole of what this side can see. `Pull` at driver_sync.go:305
is `driveOpUntilDone` over the apply step of a pull, and the panic is one C call
deeper.

## What the panic is

The line is a defensive assertion in turso's WAL. Readers resolve a page through
the WAL by watermark: `find_frame(page_id, frame_watermark)` answers "which WAL
frame, if any, holds this page at or below my snapshot position". Two guards on
that answer aborted the process rather than returning an error or a miss:

```rust
turso_assert!(
    frame_watermark.unwrap_or(0) <= self.max_frame.load(Ordering::Acquire),
    "frame_watermark must be <= than current WAL max_frame value"
);
turso_assert!(
    frame_watermark.is_none() || frame_watermark.unwrap() >= nbackfills,
    "frame_watermark must be >= than current WAL backfill amount", ...
);
```

The sibling assertion a few lines up only exists when the `conn_raw_api` feature
is off, so with that feature on the watermark argument is reachable and both
guards are.

A reader whose snapshot predates a passive checkpoint is a normal thing to exist:
it pinned its snapshot before the checkpoint ran, and the checkpoint advanced the
backfill floor above it. Upstream agrees — turso/turso#8195 is titled "WAL frame
lookup panics instead of returning not-found" and describes exactly that state as
routine rather than an invariant violation.

The consequence on this side is total. A panic inside the Rust library unwinds
across the C ABI and aborts the process, so no `recover` in this tree can catch
it, and the daemon that was mid-enable leaves a machine marked on with nothing
having moved.

## The upstream fix, and why the version does not move

turso/turso#8285 (commit `5ddcf8a2`, "core/storage/wal: return error on
out-of-range WAL watermark") replaces both assertions with
`Err(LimboError::InvalidArgument(...))` and closes #8195.

That fix is on `main` and in no release. Checked against the tag this tree builds:

```
$ curl -s https://api.github.com/repos/tursodatabase/turso/compare/5ddcf8a2...v0.8.1 | jq .status
"diverged"                      # ahead_by 11, behind_by 1
$ curl -s https://api.github.com/repos/tursodatabase/turso/compare/5ddcf8a2...v0.8.2-pre.2 | jq .status
"diverged"                      # ahead_by 16, behind_by 1
```

`behind_by: 1` is the fix commit itself. Confirmed by reading the file at each
tag rather than trusting the comparison:

```
$ for t in v0.8.0 v0.8.1 v0.8.2-pre.2 main; do
    printf '%s: ' "$t"
    curl -s "https://raw.githubusercontent.com/tursodatabase/turso/$t/core/storage/wal.rs" \
      | grep -c 'frame_watermark must be <='
  done
v0.8.0: 1
v0.8.1: 1
v0.8.2-pre.2: 1
main: 0
```

`turso.tech/database/tursogo` publishes v0.8.1 as `@latest`; the only newer tag
is v0.8.2-pre.2, a pre-release that still carries the assertion. So there is no
version to upgrade to. The module requirement stays at v0.8.1, `DriverVersion`
names it, and a test asserts the two agree — a bump now has to be deliberate and
has to say what it is claiming.

## What this tree changed instead

The enable's open is the only open in the tree that moves a whole database: the
first push after it carries every unpushed local change, and the first pull can
carry the remote's entire state. Both of those are now bounded by the driver's own
knobs:

- `PullBytesThreshold` (4MiB) splits the bootstrap download into multiple
  `/pull-updates` requests instead of one round trip.
- `PushOperationsThreshold` (4096) splits the push on transaction boundaries once
  a batch has grown that large.

Those two fields exist on `TursoSyncDbConfig` in v0.8.1 and are threaded through
`OpenConfig` to the constructor. Every later open leaves them at zero, because
every later open moves one round of changes.

**This is a bound on the work one driver call may be asked to do. It is not a
claim about where the driver breaks.** The abort is not reachable from this side
of the C ABI — it happens inside a page read during an apply, and the watermark
that trips it is one the sync engine computes, not one a caller passes. Bounding
the transfer is the strongest available shape change; whether it prevents this
abort is not established, and no test here can establish it.

The regression test drives the shape, not the abort: an abort before a write is
not reproducible in-process, and a test that tried would kill the test binary
rather than fail an assertion. What is pinned instead is the arguments and order
of every driver call in the seed path — see
`TestTheSeedOpenAsksTheDriverForBoundedCalls` and
`TestEveryDriverCallInTheSeedPathIsBounded` in `internal/sync/enable_test.go`.
Dropping either threshold back to the driver's unbounded default fails them.

## Why per-connection capture wedged a lived-in machine

Asking the capture pragma on every connection of every writable pool over a
member file was reverted, because a build doing it took a healthy machine down
within minutes: new connections piled up inside the connect path, reads and
ingest failed `busy`, and a revert to the pre-capture binary restored service.

The trigger is that the pragma is a **write**.
`PRAGMA capture_data_changes_conn('full,turso_cdc')` installs the connection's
capture state and creates the capture tables if they are absent, so it queues
for the database's single write slot. Put on a path every connection takes, it
turns connection setup into a write transaction competing with whatever the
daemon is already writing. While that slot is not free, a connect waits out the
whole `busy_timeout` and then fails `busy` — and the failure names the pragma,
not the read that asked for the connection.

Evidence is in `internal/db/capture_pragma_test.go`, over scratch files only:

- `TestCapturePragmaIsAWrite` holds a write transaction open on one connection
  and asks a second connection for a configuring pragma and then for the
  capture pragma. The first returns immediately; the second waits the full
  five seconds and fails `database is busy: database is locked`.
- `TestCapturePragmaOnThePoolOpenPathFailsReadsUnderWriteLockSaturation` runs one
  scenario twelve ways, crossing the pragma on and off the open path, whether the
  capture tables are already present, and three writer shapes. With the pragma
  off, every read is immediate whatever the writer is doing. With it on and the
  write slot held, every read spends the whole busy timeout and fails `busy`. The
  capture tables being present or absent changes nothing, so CDC state left by an
  earlier open is not the trigger.
- `TestCapturePragmaBlockTracksTheWriteSlotNotTheWal` builds the log a lived-in
  file has and a fixture does not — a reader pinning its snapshot,
  `wal_autocheckpoint = 1`, and a 1.9MB log that cannot be backfilled — and asks
  for the pragma over it twice. Free write slot: immediate. Held write slot: the
  full busy timeout, then `busy`. The block tracks the write slot, not the log,
  so the un-backfillable frames left by an earlier checkpoint failure are not the
  trigger either.

What made it a wedge rather than a slowdown is the pool around it. `openPool`
sets no `SetMaxOpenConns`, so nothing bounds how many connections can be opening
at once, and the reverted revision dropped idle retention on a member's pool,
which spends a fresh connect — and so a fresh capture pragma — on every read
instead of once per connection. A read that is fast on its own therefore costs a
write transaction's worth of contention, and the contention it creates is paid
back by the tick writer, which then fails `busy` in turn. That is the loop the
field report saw: reads failing `busy` while writes that were already succeeding
stop succeeding.

Two consequences for any future attempt at this. A pragma that writes does not
belong on a path every connection takes, however cheap it looks on a quiet file.
And a pool that opens connections in a hot path wants a bound on how many can be
opening, whatever it opens them for.

## What is still unknown

- **The exact trigger.** Whether the failing case is the 211MB download, the
  211MB push, the page/WAL shape a fresh import leaves, or a race between the
  sync engine's own passive checkpoint and a reader it holds, is not determined.
  The panic's own issue reports the below-floor case; the observed line is the
  above-max-frame guard, which is the mirror image and is a different interleaving.
- **Whether the bounds prevent it.** Nothing here can run the failing shape: it
  needs a remote holding a real imported seed, which is exactly what a test must
  not reach for.
- **Whether other guards abort the same way.** Two assertions were replaced
  upstream; a driver full of `turso_assert!` is a driver whose panics are
  unrecoverable, and this tree has no way to enumerate which are reachable from a
  sync call.

The remedy is upstream and released. When a release carries #8285, the right move
is to bump, watch whether the bounds are still wanted, and delete this file's
remaining uncertainty — not to keep the bounds as a permanent substitute.

## The two symptoms that were ours to fix

The abort is the driver's. The state it left behind was not, and both parts are
fixed here with tests:

- **The wedged machine.** The enable writes its mark before the first push, so a
  process that dies inside one leaves a machine marked on, no handle, and no verb
  that would accept it: `push`/`pull` refuse for want of a handle, `enable`
  refused as already-enabled, and `disable` paid a dial to a remote it could not
  use to push a file nothing had joined. Now `internal/sync.KeySeeding` marks the
  window between the mark and the end of the first round, `disable` completes with
  no handle and no network (it opens nothing for a file the driver never joined),
  and an enable that finds the window open re-runs rather than refusing.
- **The wrong database.** `turso db import <seed>` creates a *new* cloud database
  rather than filling the one `--url` names, so the remote to sync with is not the
  one the section holds. The contradiction refusal now applies while a machine is
  on; an off machine takes the remote it is named. The refusal for the enabled
  case is unchanged.