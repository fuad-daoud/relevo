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

## The two ways a plain connection breaks a synced file

Both are the same mistake in different clothes: this tree opens the shared file
a second time, through turso's plain connector, and a plain connection is not
part of the sync engine. Evidence for both, against a scratch remote, is in
`internal/sync/scratch_pushpull_test.go`; each is gated behind
`RELEVO_SCRATCH_SYNC_URL` and `RELEVO_SCRATCH_SYNC_TOKEN` and skips without them.

**A push that reports success may have carried nothing.** Change capture is a
per-connection pragma, `PRAGMA capture_data_changes_conn`, and the sync engine
sets it on each connection *it* opens. A row written on a connection that has not
asked for it is not recorded in `turso_cdc`, and `push_changes` reads that table,
so the push finds no change set, sends nothing, and completes successfully. The
`db_sync_sent` field in a driver's `CheckpointResult` says whether the
*checkpoint* synchronised the database file to the remote; it is not a push
result, and reading it as one makes a failure look like a success.

**A plain truncate breaks the next pull.** The engine keeps a watermark in its own
metadata naming the last log frame it transferred into the revert database, and
the guard that notices a log restart only fires when the metadata also holds the
salt from when that watermark was set (`revert_since_wal_salt.is_some()`). An
apply persists the watermark without that salt, so a `PRAGMA
wal_checkpoint(TRUNCATE)` over any plain connection — which every close, vacuum
and compress pass here performs — resets the log, and the next pull's
`checkpoint_passive` refuses with `unable to checkpoint synced portion of WAL:
result=..., watermark=...` because it is asked for a frame the log no longer has.

Neither is a driver bug and neither has an upstream fix to wait for, so the tree
bounds both on its own side: `internal/db` asks for capture per connection on a
member file, retires the connections a pool opened before the membership existed,
and refuses to rewrite the log of a member.

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
- **Rows written before the capture was on.** The engine only captures what a
  connection recorded, so a row written before this file joined — or before the
  pool opened after it joined — is in the database and in no change set. Those
  rows are not recovered by anything in this tree; they are the rows a user has
  to push by whatever means the driver offers. Which of them exist on a given
  machine is a question about that machine's history, not something a marker can
  answer.
- **Whether a two-connection file is otherwise sound.** The fixes make the plain
  pool capture and stop it rewriting the log, which is what both symptoms needed.
  They do not make the file single-writer: the sync engine and this tree's pool
  still address one database at once, and whether the engine tolerates that for
  every shape of write is not established by the two symptoms fixed here.

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