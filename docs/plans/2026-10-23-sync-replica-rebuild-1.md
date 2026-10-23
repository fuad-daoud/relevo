# Plan: rebuild a corrupt sync replica by itself -- member 1, the automatic rebuild

Member 1 of two. This member owns the whole mechanism (`internal/sync`): the
classifier, the aside folder, the record, the cap, and the latch, plus the
daemon branch that drives it, the status line and the `:sync` view line. It is
complete on its own: after it lands, a machine whose replica goes corrupt
recovers by itself and `relevo db sync status` says what happened.

Member 2 is `relevo db sync retry --rebuild`: one flag, one wire field, one call
into this member's exported `(*sync.Runner).RebuildReplica`. The dependency is
real: member 2 calls this member's seam, so member 2 lands after member 1, on
the same branch -- this chain is one branch and one PR per member, in order.
Everything else about the two members is independent, and they touch disjoint
files apart from the one seam member 2 calls.

## Verified facts this plan rests on

- **Rebuilding loses no data.** The outbox is cleared only after the append is
  confirmed: `internal/synclog/export.go` (`ExportBatch` calls
  `DeleteDrainedOutbox` after `appendTo` returned nil, and the comment says a
  refused batch is re-exported next pass), and the worker pushes before it
  answers (`internal/syncworker/turso.go`, `AppendSigned`). A fresh replica
  re-reads the whole remote log, and the marks that say how far this machine
  has *applied* live in `relevo.db`, not the replica (`synclog.Importer` holds
  `*db.DB`), so the importer resumes exactly where it stopped. Nothing is
  re-applied and nothing is dropped.
- **The replica is opened only through `newSyncDb`**
  (`internal/syncworker/driver_turso.go`), a package variable a test can
  replace. No CI test may reach it for real.
- **`ReplicaFiles` already lists exactly the replica's files**
  (`internal/sync/disable.go`: the replica, plus every driver file named after
  it by the `-`/`.` prefix rule), and `deleteReplica` in the same file already
  moves the main file *after* its siblings. The move-aside reuses both rules.
- **The daemon owns the path and the worker owns the handle.**
  `VerbRunner.ReplicaPath` is `syncpipe.ReplicaPath(d.Path())`
  (`cmd/relevo/syncverb_hook.go`, `internal/syncpipe/wiring.go`), and
  `runner.Runner.ReplicaPath` is set from it, which is the runner the tick
  drives (`internal/relevo/sync_tick.go`). The worker is a child process;
  `syncpipe.Client.kill` kills and **reaps** it, so once the transport is
  closed the file is free.
- **A corrupt read arrives as a plain refusal, not a death.** The driver's text
  is wrapped by `refusalError` under `ErrRefused` with no code
  (`internal/syncpipe/client.go`), `markTransferRefusal`
  (`internal/syncworker/format.go`) only marks `failed to execute sql`, so
  `Supervisor.settle` takes the `ErrRefused` branch: the death count is
  unchanged and **the worker is kept** -- which is why the rebuild path stops
  it explicitly before renaming anything.
- **The two corrupt texts, and where they come from.** The first failure was
  `syncworker: read an entry: sql: Scan error on column index 2, name "batch":
  converting NULL to int is unsupported` -- `scanEntry`
  (`internal/syncworker/turso.go`) reading the `log` table, whose `batch`
  column is declared `INTEGER NOT NULL`
  (`internal/syncworker/format.go`, and `batch` is present from the first
  format, so no upgrade can leave a legitimate NULL). A NULL there is a read
  the schema forbids. Every later failure was `turso: database is corrupt:
  range 25445.. out of bounds for buffer size 4096`, surfacing through
  `readFloors` -> `ReadMarks` (`internal/synclog/import.go`).
  **Decision: the NULL-batch read counts as replica-corrupt**, because the
  schema says that row cannot exist and it came from the same file that then
  reported a torn page.
- **`relevo db sync retry` today** clears the breaker and cancels the worker
  (`internal/sync/retry.go`) and the next tick reopens the same file.
- **`Reconcile` already chunks at 256** (`internal/synclog/reconcile.go`,
  `defaultReconcileChunk`). Out of scope here.
- **The latch is the attention marker** (`internal/sync/sync.go`,
  `SetAttention`), read back by `Breaker.LatchCause`
  (`internal/sync/breaker.go`) and by `ReadState`, so the statusline shows it
  with no new plumbing.
- **The status verb is a pure read of the local file**
  (`cmd/relevo/db_sync_status.go`, `cmd/relevo/db_sync_r2.go`), so a new local
  record is enough for it; the `:sync` view already carries the whole
  `relevosync.State` (`internal/ui/actions_sync.go`).

## Non-goals (closed)

- The driver root cause (tursogo v0.8.1), reconcile chunking, #1194, #1118.
- No new wire refusal code, and no remapping of a corrupt replica in
  `dbSyncClassify`. After this member a corrupt replica no longer reaches the
  user as an internal failure on the steady path -- the tick handles it and the
  latch names the fix. A one-shot `push` that meets one still reports internal;
  giving it its own code is a change to a closed set and is left alone
  deliberately.
- `relevo db sync disable` is **not** changed. It deletes the replica's own
  files; the aside folder is a copy relevo kept as evidence and `db sync
  status` names it, so a user can remove it. Stated here so no builder
  "tidies" it.
- The aside folder is never written into the log or synced: it is a local file
  under the state directory, and nothing in `internal/db` reads it.

## Behaviour and cases

1. **A corrupt replica is rebuilt without a human.** When a steady attempt
   fails with an error the replica itself produced (a torn page, a malformed
   image, a NULL in a `NOT NULL` log column), the daemon stops the worker,
   moves the replica and every driver file named after it into an aside folder
   beside it, records the rebuild in the machine-local kv, and asks for one
   more attempt. That attempt opens a fresh worker through the stored rows, so
   it bootstraps a fresh replica from the remote with the **same stored token
   and key**. No `disable`/`enable`, no passphrase, no `--url`.
2. **At most one automatic rebuild per 24 h.** The record holds the instant of
   the last rebuild. A second corruption inside the window does **not** move
   the replica again: the machine latches, and the latch sentence names
   `relevo db sync retry --rebuild`. A corruption after the window rebuilds
   again. The latch is cleared only by the retry verb, as every other latch
   is.
3. **One aside copy, always.** The aside folder has a fixed name, so a rebuild
   removes the previous copy before moving the new one in. A machine that
   rebuilds once a day for a month holds one copy, not thirty. The main
   replica file moves **after** its siblings, so a failed move leaves the
   machine readable rather than half-moved.
4. **Nothing is moved on any other failure.** A remote refusal, a network
   timeout, a quota refusal, a backing-off breaker, an auth refusal: none of
   them touch the replica. Only the classified corrupt errors do.
5. **`relevo db sync status` says it.** The line and the `--json` document
   carry when the last rebuild ran and where the aside copy is, and the
   `:sync` view draws the same two facts. A machine that has never rebuilt
   renders exactly the bytes it renders today.
6. **The step is ordered, not best-effort.** Stop the worker, then move, then
   record. A move that fails records nothing, so the cap is not spent and the
   next attempt tries again.
7. **The automatic rebuild does not wait for the operator.** After a
   successful move the tick marks the sync slot dirty, so the fresh bootstrap
   runs in the same slot rather than at the next five-minute window. It cannot
   loop: the record written by the rebuild makes the next corrupt detection
   latch instead of rebuilding, so at most two attempts are spent.

## Seams

New file `internal/sync/rebuild.go` (well under the 600-line limit; every
function under 70), as built:

```go
// WorkerRefusedText is the fixed sentence the sync pipe's refusal carries.
// Only a worker's answer can be about the replica's own file -- the daemon's
// own machine-local file is a Turso file read through the same driver, and a
// corrupt read of it must never move the replica aside. internal/sync cannot
// import internal/syncpipe (the pipe imports sync), so the sentence lives
// here and the pipe builds its sentinel from it.
const WorkerRefusedText = "the sync worker refused"

// IsReplicaCorrupt reports whether err is the replica's own file being
// unreadable rather than a remote or a network failure.
func IsReplicaCorrupt(err error) bool

// Rebuild is the last replica rebuild and where the copy it kept is.
type Rebuild struct {
    At    time.Time `json:"at"`
    Aside string    `json:"aside,omitempty"`
}

const (
    KeyRebuild    = "sync.rebuild" // the machine-local kv key, beside the breaker's
    RebuildWindow = 24 * time.Hour // how long an automatic rebuild waits
)

func ReadRebuild(kv db.KV) (Rebuild, error)            // zero value when none, and when the value will not parse
func RebuildDue(kv db.KV, now time.Time) (bool, error) // no record, or the window has passed (inclusive)
func WriteRebuild(kv db.KV, at time.Time, aside string) error
func LatchCorruptReplica(kv db.KV, now time.Time) error // SetAttention with the fixed sentence

// RebuildResult is what one rebuild moved.
type RebuildResult struct {
    At    time.Time
    Aside string
    Moved []string // in the order the files were moved, main file last
}

// RebuildReplica is the mechanism: stop the worker, move the files, record.
func (r *Runner) RebuildReplica(now time.Time) (RebuildResult, error)

// RebuildCorrupt is the policy: RebuildReplica while RebuildDue, else latch.
// rebuilt is false when the machine was latched instead.
func (r *Runner) RebuildCorrupt(now time.Time) (RebuildResult, bool, error)
```

Unexported in the same file: `asideDirName = "relevo-sync-aside"`,
`asideDir(replicaPath string) string`, `asideReplica(path string) (aside
string, moved []string, err error)`, `stopWorker(client
synclog.LogTransport) error`, and `errNoReplica = errors.New("sync: no replica
file to move aside")`.

`stopWorker` calls the transport's `Close() error` when it has one -- the
supervisor does, and its `Close` takes the call lock and drops only the current
client, which it kills and reaps; the next call starts a fresh worker -- and
falls back to `cancelWorker` otherwise.

`IsReplicaCorrupt` matches the fixed shapes, in one table with one reason per
entry, on the whole error chain's text -- the pattern
`internal/sync/remoterefusal.go` uses -- and requires **both** of:

- the chain's text to carry `WorkerRefusedText`, so only an answer the sync
  worker gave can classify. The pipe's `ErrRefused` is built from this const
  (`internal/syncpipe/client.go`: `ErrRefused = errors.New(
  relevosync.WorkerRefusedText)`), which is that file's only production change;
- one link of the chain to match a needle whole:

| needle | why |
| --- | --- |
| `database is corrupt` | the driver's own wording for a torn page |
| `database disk image is malformed` | SQLite's own wording for a damaged image |
| `converting NULL to int` **and** `name "batch"` | Go's `database/sql` wording for the NULL the log's `INTEGER NOT NULL` column forbids |

Edits, with the line ranges that matter:

- `internal/sync/breaker.go` -- add `latchReplicaCorrupt` to the fixed latch
  sentences:
  ``"sync: the replica went corrupt again within a day; run `relevo db sync
  retry --rebuild`"``.
- `internal/sync/sync.go` -- add `Rebuild Rebuild` to `State` and
  `{KeyRebuild, &s.Rebuild}` to `ReadState`'s reads table, so the state
  carries it with no second read.
- `internal/relevo/sync_tick.go` -- in `runSync`, before the ordinary
  `sync failed` log: when `out.Err != nil` and
  `relevosync.IsReplicaCorrupt(out.Err)`, call the new
  `d.rebuildCorruptReplica(ctx, runner, out.Err)` and return.
- New file `internal/relevo/sync_rebuild.go` -- `(*Daemon).rebuildCorruptReplica`:
  take `now := d.syncClock()()`, call `runner.RebuildCorrupt(now)`, log the
  corrupt cause with the outcome, and `d.queueSync(ctx)` when the replica was
  rebuilt (that sets the slot dirty and reruns the attempt in the same slot,
  `internal/relevo/sync_tick.go`, `internal/relevo/sync_liveness.go`). A
  latched outcome logs the window and queues nothing.
- `cmd/relevo/db_sync_status.go` -- add to `dbSyncStatusDoc`:
  `RebuildAt string \`json:"rebuild_at,omitempty"\`` and
  `RebuildAside string \`json:"rebuild_aside,omitempty"\``. In
  `dbSyncStatusDetails`, after the latch clause: when `RebuildAt != ""`,
  ` · rebuilt: <stamp>` and, when the aside is set, `, aside: <path>` with the
  path through `sanitize.Text`.
- `cmd/relevo/db_sync_r2.go` -- in `dbSyncStatusRead`, set
  `RebuildAt: statusStamp(state.Rebuild.At)` (`statusStamp` is
  `cmd/relevo/db_sync_attempt.go`) and `RebuildAside: state.Rebuild.Aside`.
- `internal/ui/view_sync.go` -- in `postOnLines`, after the `first upload`
  line and before `attemptLines`, draw a `last rebuild` row when
  `v.snap.State.Rebuild.At` is not zero, reusing `localStampPhrase` and naming
  the aside beside it. No `internal/ui/actions_sync.go` change: the snapshot
  already carries the whole `relevosync.State`.

## Ordered steps

Each step names its deliverable and how to know it worked.

1. **The classifier.** Add `internal/sync/rebuild.go` with `IsReplicaCorrupt`
   and its needle table; add `internal/sync/rebuild_test.go` with
   `TestIsReplicaCorruptClassifiesTheDriverAndTheNullBatch`: a table over the
   two exact strings from the issue, a `malformed` image, and the controls
   that must **not** classify -- a `SQLITE_CONSTRAINT` remote refusal, a
   blackholed call, `ErrQuotaReached`, `ErrAuthRefused`, and
   `"db: tx: turso: database is corrupt: ..."` with no worker marker. Pure
   function, no file, no network, no worker.
   *Works when:* `go test ./internal/sync/ -run TestIsReplicaCorrupt` passes,
   deleting one needle from the table makes it fail, and dropping the
   marker requirement fails the daemon's-own-file control.
2. **The record and the cap.** Add `Rebuild`, `KeyRebuild`, `RebuildWindow`,
   `ReadRebuild`, `RebuildDue`, `WriteRebuild` to the same file. Add
   `TestRebuildDueWaitsTheWindow`, over `openSplit`
   (`internal/sync/token_test.go`): no record is due; a record at `now` is
   not; one at `now-RebuildWindow+1s` is not; one at `now-RebuildWindow` is;
   a value in the key that will not parse as a record reads as no record and
   is due (the kv takes only valid JSON, so the corrupt case is valid JSON of
   the wrong shape). Add `TestRebuildRecordRoundTrips`, including that the
   aside is absent from the JSON when empty. No network.
   *Works when:* the focused `go test ./internal/sync/ -run 'Rebuild'` passes,
   and flipping the window comparison to `>` makes the boundary case fail.
3. **The latch sentence.** Add `latchReplicaCorrupt` to
   `internal/sync/breaker.go` and `LatchCorruptReplica` to `rebuild.go`. Add
   `TestLatchCorruptReplicaNamesTheManualVerb`: after the call,
   `NewBreaker(local).LatchCause()` returns the sentence, `Latched()` is
   true, and the sentence contains `relevo db sync retry --rebuild`.
   *Works when:* that test passes and shortening the sentence to drop the
   command name makes it fail.
4. **The move.** Add `asideDirName`, `asideDir`, `asideReplica` and
   `stopWorker`. Add `TestRebuildReplicaMovesTheFilesAndKeepsOneAside`: a
   temp dir holding `relevo-sync.db`, `relevo-sync.db-wal` and
   `relevo-sync.db-shm`; a transport that records its `Close` and, inside it,
   whether the replica file was still there; after `RebuildReplica`, assert
   the worker was closed while the file was still present, the three files
   are gone from the state dir, the aside folder holds all three, the kv
   record names it, and `Moved` names the main file last. Then re-seed the
   way the retry's fresh bootstrap would -- a fresh replica, with no
   write-ahead file yet -- run it again, and assert **exactly one** aside
   folder exists holding exactly the second copy. Add
   `TestRebuildReplicaRefusesWithNoReplicaPath`: an empty `ReplicaPath`
   returns `errNoReplica` and writes no record. Add
   `TestRebuildReplicaRecordsNothingWhenTheMoveFails`: a move that cannot
   land leaves the replica file where it was and writes no record, so the cap
   is not spent. Temp dirs only: no Turso file is opened, `newSyncDb` is
   never reached.
   *Works when:* those tests pass; removing the pre-move prune leaves the
   first copy's files beside the second's and fails the aside's contents;
   moving the main file before its siblings fails the order of `Moved`.
5. **The policy.** Add `RebuildReplica` and `RebuildCorrupt` on `*Runner` (the
   first as the mechanism, the second reading `RebuildDue` first). Add
   `TestRebuildCorruptLatchesInsideTheWindowAndRebuildsAfterIt`: with a fixed
   `now`, a first call rebuilds and records; a second call at `now+1h`
   returns `rebuilt == false` and latches, with the files **not** moved; a
   call at `now+RebuildWindow` rebuilds again.
   `Runner{Client: <double>, Local: local, ReplicaPath: <temp path>}`; no
   network, no worker.
   *Works when:* that test passes and dropping the `RebuildDue` check makes
   the second call move the files and fail it.
6. **The daemon.** Add `internal/relevo/sync_rebuild.go` and the
   `internal/relevo/sync_tick.go` branch. Add
   `internal/relevo/sync_rebuild_test.go`:
   - `TestCorruptReplicaIsMovedAsideAndRetried`: a transport embedding a
     `synclog.MemTransport` log (the `tickTransport` shape in
     `internal/relevo/sync_tick_test.go`) whose first `Append` returns the
     issue's `database is corrupt` text under the worker's refusal marker --
     wrapped the way the contabo log shows:
     `synclog: import: read the floors: the sync worker refused: turso:
     database is corrupt: range 25445.. out of bounds for buffer size 4096` --
     and every later call succeeds; `rt.Sync.ReplicaPath` set to a temp dir
     seeded with the replica files; drive `d.Tick` and `waitSyncIdle`. Assert
     the files are aside, the kv record names the folder, and the second
     attempt reached the transport. No network, no harness, no Turso file.
   - `TestSecondCorruptionInTheWindowLatchesInsteadOfRebuilding`: the same
     fixture with `d.syncNow` fixed and a fresh record already in the kv;
     assert the files did **not** move, `Breaker.Latched()` is true, and the
     cause names `retry --rebuild`.
   - `TestOrdinarySyncFailureMovesNothingAside`: an `ErrRemoteRefused`-shaped
     failure carrying the worker's marker, as refusals do; assert the replica
     files are untouched and no record was written.
   *Works when:* all three pass; deleting the `IsReplicaCorrupt` branch in
   `runSync` fails the first; deleting the cap fails the second; widening
   `IsReplicaCorrupt` fails the third.
7. **The surfaces.** The `cmd/relevo/db_sync_status.go` /
   `cmd/relevo/db_sync_r2.go` doc and line, and the
   `internal/ui/view_sync.go` row, from the Seams list. Extend
   `dbSyncStatusCase` (`cmd/relevo/db_sync_status_test.go`) with a
   `rebuildAt`/`rebuildAside` pair and one case that seeds
   `relevosync.KeyRebuild` and pins the exact line and `--json` document; the
   other cases keep their current golden bytes, which is what proves the line
   is unchanged when nothing rebuilt. Pin the view row in `internal/ui` with
   the same shape the other status rows use.
   *Works when:* `go test ./cmd/relevo/ -run TestDBSyncStatus` and
   `go test ./internal/ui/` pass, and dropping the render clause fails the new
   golden.
8. **The plan file.** Write this member's plan to
   `docs/plans/2026-10-23-sync-replica-rebuild-1.md` and commit it in this
   PR. It is the text of this member from the MasterMind's plan, adjusted
   only to what was actually built -- no new document, no history in the
   code.
   *Works when:* the file is in the PR and `make check-affected` is green.

Focused loop: `go test ./internal/sync/ ./internal/relevo/ ./cmd/relevo/
./internal/ui/`.
Full gate: `make check-affected` (CI's `make check` is the judge).

## What is deleted

Nothing. This member only adds: a new file, a new kv key, a new latch
sentence, one early branch in `runSync`, one status clause, one view row --
and the one-line const swap in `internal/syncpipe/client.go` that makes the
refusal marker shared text. `disable`, `deleteReplica`, `ReplicaFiles` and the
retry verb keep their behaviour exactly. The aside folder is deliberately
**not** added to `disable`'s delete set.

## Report

- The two exact corrupt strings, and the schema fact (`log.batch INTEGER NOT
  NULL`) that makes the NULL-batch read corrupt.
- Where the rebuild hooks in (`runSync`, `RebuildCorrupt`) and why the worker
  is stopped before the rename (the supervisor keeps a worker that *refused*,
  so a refusal is not a death).
- Why rebuilding loses no data, quoting `ExportBatch`'s ordering and the fact
  that the import marks live in `relevo.db`.
- Every named test and the mutation that fails it, per condition.
- The coverage verdict: no baseline regeneration (no code moves between
  packages), and the new statements' coverage from the named tests.
- `git diff --stat` against this scope, and confirmation that
  `internal/syncworker`, `internal/synclog` and `internal/syncpipe` production
  files are untouched apart from the one const swap the marker requires.
