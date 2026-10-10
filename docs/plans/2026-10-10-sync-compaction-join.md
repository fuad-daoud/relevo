# Sync everything, step 2: compaction and joining

The four round plans chain `se2` ran, in order. The spec is
`docs/specs/2026-10-10-sync-everything-design.md` (§7).

## Sync step 2, round 1: log format 2, marks and the floor

Spec: `docs/specs/2026-10-10-sync-everything-design.md` §7 "Compaction"
(marks, floor), §9 "Log format 2". Step 1's plan
`docs/plans/2026-10-10-sync-r2-bodies.md` shows the shape of the code this
extends. This round adds the marks every machine publishes and the floor
compaction will use. It drops nothing from any log.

Out of scope: compaction itself, the importer's below-floor path, the join
guard, the quota latch, byte counting, status output, `reconcile --dry-run`,
the `turso_cdc` drop, settings. Rounds 2-4 do those.

### Decisions (do not reopen)

1. **Log format 2.** The remote gains a fourth table:
   `CREATE TABLE marks (reader TEXT NOT NULL, origin TEXT NOT NULL, seq INTEGER NOT NULL, at TEXT NOT NULL, PRIMARY KEY (reader, origin))`.
   - `logFormatVersion` goes from 1 to 2.
   - `isLogTables` accepts exactly `["head","log","marks","meta"]` at format 2.
   - **A format-1 remote is upgraded in place:** it has exactly
     `head`, `log` and `meta`, and meta says format 1. The first build of
     this round that opens it creates `marks` and writes format 2 to meta, in
     one write through the sync connection, then pushes. Do this the same way
     `createLog` does.
   - After that, a format-1 binary refuses the remote rather than misreading
     it. That's intended: compaction (round 2) leaves holes that a format-1
     importer would hold on forever.
   - Anything else is still refused, with the existing `errNotALog`.
   - In the plan's report, say what a v0.16.0 binary does on meeting format 2,
     and cite the line that does it.
2. **Worker protocol stays v2** and gains two verbs:
   - `publish_marks`. Request:
     `{"marks":[{"origin":"…","seq":N}],"at":"RFC3339"}`. The reader is
     always the worker's own origin from hello, never a request field, so a
     caller can't write another reader's rows. Reply: `{}`.
   - `read_marks`. Request: `{}`. Reply:
     `{"marks":[{"reader":"…","origin":"…","seq":N,"at":"…"}],"floors":{"<origin>":N},"heads":{"<origin>":N},"entries":{"<origin>":N}}`.
   - `floors` reads meta keys `floor.<origin>`. Nothing writes them yet, so
     they come back empty until round 2.
   - `heads` is each origin's highest `log.seq`, and `entries` is its
     `COUNT(*)` of log rows.
   - Round 2's compaction needs `heads` for the floor and `entries` for its
     10% trigger, so this round adds them now.
   - Why there's no v3: no tagged release carries v2, and the daemon and the
     worker are the same binary.
3. **Floor rule** (pure function, `internal/synclog/marks.go`):
   ```go
   type Mark struct { Reader, Origin string; Seq int; At time.Time }
   // FloorFor is the highest sequence of origin's log every live reader has applied.
   func FloorFor(marks []Mark, origin string, head int, now time.Time) int
   ```
   - A reader is live when `now.Sub(At) <= 30*24h`.
   - Count only marks for `origin` whose `Reader != origin`.
   - With one or more live readers, the floor is the minimum `Seq` over them.
   - With none, the floor is `head`, the origin's own highest sequence.
     - Why: a machine syncing alone still needs compaction, and a reader that
       turns up later or comes back has a mark below the floor, so it takes
       round 2's snapshot path.
   - Name the 30 days `markLifetime`.
4. **Publishing.** The steady loop publishes this machine's import marks
   (`db.ImportMarks()`) after an import that advanced any mark. It does so at
   most once every 15 minutes (`marksEvery`), and the last publish time is
   kept in local kv `sync.marks.published`. A join publishes once at the end,
   which is wired in round 4.
   - Why the throttle: every publish is a Turso write and push. A mark only
     grows, so a stale published mark is a lower one, which only makes the
     floor more cautious.
5. **Seam.** `LogTransport` gains these methods. Each implementation of the
   seam implements them: `MemTransport`, the syncpipe client and the stop
   transport.
   - `PublishMarks(marks map[string]int, at time.Time) error`
   - `ReadMarks() (MarkState, error)`, where
     `type MarkState struct { Marks []Mark; Floors, Heads, Entries map[string]int }`
   - `MemTransport` stores marks per reader, so a `memLog` built for reader A
     can't write B's rows.

### Steps

1. Add the remote DDL to `createStatements`, then the format-1 to format-2
   upgrade, then the `isLogTables` and format checks. Extend the existing
   `TestEnsureLog*` tests: a fresh remote, a format-1 remote that gets
   upgraded, a format-2 remote, and a remote with extra or missing tables,
   which is refused.
2. Write `marks.go`: `Mark`, `FloorFor`, `markLifetime`.
3. Add the transport methods and the `MemTransport` implementation.
4. Add the worker verbs, the syncpipe client methods and the conversion, and
   update the golden wire test.
5. Add the steady-loop publish with its throttle and kv key.

### Tests and mutations

Each mutation must make its named test fail. Report before and after for
every one.

- `TestFloorIsMinimumOverLiveReaders`. Mutation: drop the 30-day filter.
- `TestFloorIgnoresTheOriginsOwnMark`. Mutation: drop `Reader != origin`.
- `TestFloorWithNoLiveReaderIsHead`. Mutation: return 0 when nobody is live.
- `TestPublishMarksWritesOnlyOwnRows`, through the worker verb against the
  scratch Turso test the step-1 worker tests use. Mutation: take the reader
  from the request.
- `TestFormatOneRemoteIsUpgradedInPlace`. Mutation: skip creating `marks`.
- `TestSteadyPublishesMarksAtMostEveryFifteenMinutes`. Mutation: drop the
  throttle check.

### Constraints

- CI has no network and no harness binary. No `cmd/relevo` test may run a
  subcommand that spawns the worker or reaches the network.
- `make check` must pass (`GOTOOLCHAIN=go1.27.2 make check`).
  - It covers gofmt, vet, golangci-lint, `go mod tidy`,
    `scripts/check-comments.sh`, `scripts/check-filesize.sh`, and coverage
    within one point of `testdata/coverage-baseline.txt`.
  - Never lower a baseline.
  - Functions are at most 70 lines and files at most 600. Add no new
    exclusions.
- Comments say why. No issue numbers or history in code.

## Sync step 2, round 2: compaction and the snapshot import

Spec: `docs/specs/2026-10-10-sync-everything-design.md` §7, "Compaction".
Round 1 added the `marks` table, `FloorFor`, `read_marks` and
`publish_marks`. This round has an origin drop its own superseded log
entries. It also teaches the importer to read a compacted log from any mark.

Out of scope: the join guard, the quota latch, byte counting, status output,
`reconcile --dry-run`, the `turso_cdc` drop and settings (rounds 3 and 4).
Also out: deleting R2 objects that compaction leaves unreferenced. Name that
as a follow-up in the report; don't build it.

### The rule (decided; this refines spec §7, so do not reopen it)

Let F be the floor of origin O. F is always a batch tail, because reader
marks only ever land on batch tails, and so does O's head.

- **Compaction drops** an entry of O when its `seq <= F` and a later entry
  for the same `(tbl, pk)`, also with `seq <= F`, supersedes it.
- **What survives at or below F** is exactly O's state as of `seq F`: the
  latest entry per key up to F, delete entries included.
  - Delete entries are kept. They're tiny, and keeping them means a reader
    that held a row and missed its removal still removes it.
  - The spec's "drop deletes below the floor" is deliberately not built.
    Dropping them would need a row sweep on every rejoin.
- **Entries above F are never touched.** A key superseded only by an entry
  above F keeps its entry at or below F. Otherwise a reader applying up to F
  could meet a child row whose parent's only surviving entry is above F.
- **The floor only rises.** O writes `floor.<O> = max(old floor, F)`.
- **The drop and the floor write are one transaction and one push**, so no
  reader ever sees holes under an old floor.
- **When O compacts:** when `CompactDue` says so, run from the steady loop.
  - `CompactDue(lastAt time.Time, lastEntries, entries int, now time.Time) bool`
    is true after 7 days, or when `entries*10 >= lastEntries*11`.
  - It is also true when it has never run and `entries > 0`.
  - Keep the state in local kv `sync.compact` as
    `{"at":"RFC3339","entries":N,"floor":F,"dropped":N}`.
  - Compute F with `FloorFor` over `read_marks`, using O's head from
    `heads`. Skip when F is not above the recorded floor.

### The importer (decided)

For each origin O with a floor F = `floor.<O>` > 0 and a local mark m < F,
the importer takes the **snapshot path** before its normal batch path:

1. Pull O's entries in `(m, F]`. Every one is the latest per key up to F,
   apart from the instant between a compaction's push and this pull, which
   the next step makes harmless.
2. Group the entries by key and keep the highest seq per key.
3. Apply the upserts tables parents-first, in the same table order the
   exporter and reconcile use, and by ascending seq inside a table.
   - **When a foreign key refuses an upsert,** set it aside and retry it
     after the rest.
   - Repeat passes while each one applies something.
   - A pass that applies nothing is a `Stall` naming the rows.
   - A table that references itself (forks) is why this retry is needed:
     a parent's surviving entry can have a higher seq than its child's.
   - Before relying on this, check what a refused statement does to the open
     transaction on the engine the tests use. If it aborts the transaction,
     use a savepoint per statement instead.
4. Apply the deletes children-first (the reverse table order).
5. Commit in chunks that the step-1 body budget bounds, applying bodies
   through the existing `resolveRefs`.
   - **The mark stays at m until the whole region is applied,** then goes
     to F.
   - A crash mid-region re-applies the region. Upserts are idempotent, and
     deleting an absent row is a no-op.
6. Continue with the normal batch path from F+1. Gap detection stays exactly
   as it is above the floor.

A reader joining from zero has m = 0. A reader that fell silent for more than
30 days has m < F. Both go down the same path, so there is no separate
"re-import from zero". Say so in the code comment, briefly.

### Wire

- **Worker verb `compact`.** Request: `{"floor":F}`. Reply:
  `{"dropped":N,"floor":F_written}`.
  - The origin is the worker's own, from hello.
  - It deletes with one statement keyed on
    `(origin, tbl, pk, seq <= F, EXISTS later seq <= F)`. Check the plan with
    `EXPLAIN` on the scratch Turso test so it uses the primary key rather
    than a full scan per row.
- **`Pull`** gains a ceiling for the snapshot path:
  `PullRange(origin string, after, upto int) ([]Entry, error)` on
  `LogTransport`, with a worker verb `pull_range` to match. `MemTransport`
  gets `Compact(origin string, floor int) (int, error)` with the same rule,
  for tests.

### Tests and mutations

Each mutation must make its named test fail. Report before and after for
every one.

- `TestCompactionKeepsStateAsOfFloor`.
  - Mutation: compare the superseding seq with no `<= F` bound.
  - The P@5 / C@6 / P@20 with F=10 case must fail under that mutation.
- `TestCompactionKeepsDeleteEntries`. Mutation: drop deletes that are latest.
- `TestFloorNeverMovesDown`. Mutation: write F without the max.
- `TestCompactDueOnTenPercentGrowth`. Mutation: change 11 to 12.
- `TestCompactDueWeekly`. Mutation: change 7 days to 8.
- `TestSnapshotImportFromZeroAfterCompaction`.
  - A fork row whose parent's entry has the higher seq must import.
  - Mutation: remove the FK retry.
- `TestStaleReaderBelowFloorConverges`.
  - The reader held a row the origin then deleted below the new floor.
  - Mutation: skip the snapshot path, so it reports a gap and holds.
- `TestSnapshotMarkMovesOnlyAtTheEnd`.
  - Fail mid-region, then rerun; the result must converge.
  - Mutation: move the mark per chunk.
- **Convergence, in `converge_test.go`:** three machines write
  concurrently; one compacts; a fourth joins from zero; a stale one rejoins.
  All converge (`assertConverged`, `assertReferencesClean`). Run it in every
  pull order the existing harness drives.

### Constraints

- CI has no network and no harness binary. No `cmd/relevo` test may run a
  subcommand that spawns the worker or reaches the network.
- `GOTOOLCHAIN=go1.27.2 make check` must pass.
  - Coverage stays within one point of the baseline, and no baseline is
    lowered.
  - Functions are at most 70 lines and files at most 600. Add no new
    exclusions.
- Comments say why. No issue numbers or history in code.

## Sync step 2, round 3: join guard, quota latch, join bytes, storage in status

Spec: `docs/specs/2026-10-10-sync-everything-design.md` §7: "Counting",
"Join guard", "Join, new". Issue #1110 (`gh issue view 1110`): a join's bytes
are lost when its worker dies. Rounds 1 and 2 added marks, compaction and the
snapshot import.

Out of scope: `reconcile --dry-run`, the `turso_cdc` drop and settings
cleanup (round 4). Also out: parallel transfers (#1108) and R2 object GC.

### Decisions (do not reopen)

#### 1. Log size in R2

The worker's Turso replica bootstraps in `Open`, at hello. So the guard has
to estimate a join **before any worker exists**, from R2 alone.

- **What each machine writes:** its own `<origin>/log-size.json`:
  `{"entries":N,"bytes":B,"at":"RFC3339"}`.
  - `B = SUM(LENGTH(body)) + 96*COUNT(*)` over its own log rows. The 96 is
    per-row overhead for the other columns; derive the real number in the
    sandbox later.
  - It writes after each compaction, and otherwise at most once a day after
    an export that appended something.
- **Where the numbers come from:** a `sizes` field in the `read_marks` reply,
  computed with the same SQL on the worker.
- **Key function:** the key is `blobstore.SizeKey(origin)`.
  - It can't collide with a body key, because a body key's last segment is
    64 hex characters.
  - Every caller that lists the bucket must treat a non-hex key as "not a
    body". Check each one.

#### 2. Join guard

The guard runs in `Enabler` before the worker's hello. The daemon gets an
in-process `blobstore.BlobStore` from the same R2 settings and secret the
worker gets. Tests inject `MemBlobStore`.

- `EstimateJoin(store) (JoinEstimate, error)`:
  - lists the bucket;
  - totals every body as `R2Bytes`;
  - reads every `log-size.json` and sums them into `TursoBytes`;
  - sets `Known = false` when no size object exists.
- `GuardJoin(est JoinEstimate, quota, usedThisMonth int64) error`:
  - refuses with `ErrJoinOverQuota` when
    `est.Known && est.TursoBytes > quota-usedThisMonth`;
  - the refusal names the estimate, what's left and the quota;
  - the next move is `--yes`.
  - When the estimate isn't known it proceeds, and the enable result says no
    size was published.
- **R2 bytes are reported, never refused on.** R2 has no egress charge, and a
  join adds no R2 storage.
- **`--yes`:**
  - `EnableRequest.Yes bool`;
  - the `--yes` flag on `db sync enable`, carried in `wire.SyncVerb`;
  - a new code, `SyncCodeOverQuota = "over_quota"`, which the CLI shows as a
    usage-style refusal.
  - There's no prompt anywhere.

#### 3. Join order

`Enabler.join` runs these steps in order:

1. guard;
2. hello and bootstrap;
3. import every origin (the snapshot path included);
4. reconcile own rows (upload missing bodies, then append);
5. `PublishMarks`;
6. `ClearJoin`.

The marks are published only after reconcile succeeds.

#### 4. Quota latch

Turso's documented refusal at a plan quota is the error code `BLOCKED`.

- **Matching:** the worker's `markDriverRefusal` marks an error whose text
  contains `BLOCKED` as a whole word (case-sensitive) with a new class,
  `CodeQuota`.
- **Do not map tursogo's `TURSO_DATABASE_FULL`.** It is SQLite's local
  disk-full code, and latching it would call a full local disk "Turso quota
  reached".
- **Daemon side:**
  - `ErrQuotaReached`, which `IsPermanentRefusal` accepts;
  - a breaker sentence, `latchQuota = "sync: the Turso plan quota is reached; it resets with the billing month"`;
  - no retry;
  - `db sync retry` clears it like the other latches.
- Test it with a synthetic error carrying `BLOCKED`. Live confirmation stays
  open; note that in the report.

#### 5. Join bytes (#1110)

- One `recordStats(cur synclog.Stats)` with the existing reset rule
  (`counterDelta`).
- Called by the steady loop's `countBytes` and by the join:
  - after the bootstrap;
  - after every import batch, including the snapshot path's chunks;
  - after every `ReconcileBatch`.
- A worker that dies mid-join loses at most one batch of counts.

#### 6. Storage in status

- **Collection:** the steady loop, at most once a day, stores in kv
  `sync.storage`: `{"turso":N,"r2":N,"at":"RFC3339"}`.
  - `turso` is the replica file's size.
  - `r2` is the bucket total from the in-process store.
- **Rendering:** `FormatQuotas(c ByteCounters, s Settings, st Storage) string`
  renders this month's Turso traffic against `TursoSyncQuota`, Turso storage
  against `TursoStorageQuota`, and R2 storage against `R2StorageQuota`.
  - `db sync status` shows it.
  - A missing `sync.storage` reads "not measured yet", never zero.

### Tests and mutations

Each mutation must make its named test fail. Report before and after for
every one.

- `TestJoinGuardRefusesOverRemainder`. Mutation: change `>` to `>=` (pin the
  equal case), or use last month's key.
- `TestJoinGuardYesProceeds`. Mutation: ignore `Yes`.
- `TestJoinGuardUnknownEstimateProceeds`. Mutation: refuse when unknown.
- `TestSizeKeyIsNotABody`. Mutation: a size key that is hex-shaped.
- `TestQuotaLatchDoesNotRetry`. Mutation: classify `CodeQuota` as transient.
- `TestDatabaseFullIsNotQuota`. Mutation: map `TURSO_DATABASE_FULL` to quota.
- `TestJoinBytesCountedAcrossWorkerRestart`. Mutation: count only at the end
  of the join.
- `TestJoinPublishesMarksAfterReconcile`. Mutation: publish before reconcile.
- `TestStatusShowsStorageAgainstQuotas`. Mutation: show R2 against the Turso
  quota.

A `cmd/relevo` test for `--yes` or `over_quota` is a pure parse or render
test. It must not run a subcommand that spawns the worker or reaches the
network; test the rule in `internal/sync` or `internal/relevo`.

### Constraints

- `GOTOOLCHAIN=go1.27.2 make check` must pass.
  - Coverage stays within one point of the baseline, and no baseline is
    lowered.
  - Functions are at most 70 lines and files at most 600. Add no new
    exclusions.
- Comments say why. No issue numbers or history in code.

## Sync step 2, round 4: reconcile --dry-run, the turso_cdc drop, settings, plan

Spec: `docs/specs/2026-10-10-sync-everything-design.md` §7, "Also in step 2".
Rounds 1-3 built marks, compaction, the snapshot import, the join guard, the
quota latch, join byte counting and storage in status.

Out of scope: everything in steps 3-6, R2 object GC, and parallel transfers.

### Decisions (do not reopen)

#### 1. `relevo db sync reconcile --dry-run`

- **Where it runs:** through the daemon verb, like the other sync verbs.
- **What it walks:** `func (r *Reconciler) Preview(limit int) (Preview, error)`
  walks the same differences as `Reconcile`, in the same order.
  - It never calls `Append`.
  - It never uploads a body (no `put_blob`).
  - It never writes local state: no reconcile cursor, no marks.
- **Output:**
  ```
  <table>: N to append, M to delete
    + <pk>
    - <pk>
  dry run: nothing was appended
  ```
  - Tables are listed in walk order, then a footer.
  - Show at most 10 keys per table and say how many more there are.
  - A converged file prints `in step with the log; nothing to append` instead.
- **Where it's built:** the output is rendered as a pure function in
  `internal/sync`.

#### 2. One-time `turso_cdc` drop

- **API:**
  `func DropTursoCdcOnce(d *DB, backupDir string, now time.Time) (backupPath string, ran bool, err error)`
  in `internal/db/cdc_drop.go`. It follows `CompressHistoryOnce` exactly:
  - the kv done-key is `turso-cdc-drop.v1`;
  - `BackupTo` comes first;
  - then `DROP TABLE IF EXISTS turso_cdc`;
  - then a checkpoint, then `Vacuum`.
- **Which files:**
  - It runs on every database file the daemon opens that could hold the table.
  - Find where `CompressHistoryOnce` is called and run it beside that.
- **Without the table:** a no-op that takes no backup and writes the kv key.
- **Before the backup:**
  - Check free space in `backupDir` against the database file's size.
  - Refuse with a clear message rather than fill the disk.
- **Report:** say where the backup goes and how the space is reclaimed
  (VACUUM on which engine).

#### 3. `idle_seconds` and `backlog_threshold`

Read what the steady loop's triggers actually use (`internal/sync/sync.go`,
`steady.go`, the daemon's sync tick), then for each field either:

- wire it in, with one owner and the existing default when it's zero; or
- delete it: the field, its validation, its settings-form entry and its
  tests.

Report which one you did for each field, and why, citing the lines.

#### 4. Save the plan

The last step saves these four round plans, one after another, as
`docs/plans/2026-10-10-sync-compaction-join.md`, in a new commit.

### Tests and mutations

Each mutation must make its named test fail. Report before and after for
every one.

- `TestReconcileDryRunWritesNothing`, against `MemTransport` and
  `MemBlobStore`: assert there are no appends, no puts and no cursor.
  Mutation: call `Append` from `Preview`.
- `TestReconcileDryRunOnAConvergedFile`. Mutation: print the table list
  regardless.
- `TestDropTursoCdcOnceBacksUpThenDrops`. Mutation: drop before the backup
  (the test must observe the order).
- `TestDropTursoCdcOnceIsNoopWithoutTable`. Mutation: take a backup
  unconditionally.
- `TestDropTursoCdcOnceRefusesWithoutSpace`. Inject the free-space reader.
  Mutation: skip the check.
- Whatever the settings verdict is, write a test pinning it.

A `cmd/relevo` test for `--dry-run` is a pure parse or render test. It must
not run a subcommand that spawns the worker or reaches the network.

### Constraints

- `GOTOOLCHAIN=go1.27.2 make check` must pass.
  - Coverage stays within one point of the baseline, and no baseline is
    lowered.
  - Functions are at most 70 lines and files at most 600. Add no new
    exclusions.
- Comments say why. No issue numbers or history in code.
