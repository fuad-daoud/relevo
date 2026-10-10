# Sync everything, step 2: compaction, the join and the cleanup (round plans)

Spec: `docs/specs/2026-10-10-sync-everything-design.md` §7. Only the plan of the fourth round was on disk when this file was saved; the plans of rounds 1-3 (marks, compaction and the snapshot import, the join guard, the quota latch, join byte counting, storage in status) are in the git history of the work, not here.

---

## Round 4 of 4: Sync step 2, round 4: reconcile --dry-run, the turso_cdc drop, settings, plan

Spec: `docs/specs/2026-10-10-sync-everything-design.md` §7, "Also in step 2".
Rounds 1-3 built marks, compaction, the snapshot import, the join guard, the
quota latch, join byte counting and storage in status.

Out of scope: everything in steps 3-6, R2 object GC, and parallel transfers.

## Decisions (do not reopen)

### 1. `relevo db sync reconcile --dry-run`

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

### 2. One-time `turso_cdc` drop

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

### 3. `idle_seconds` and `backlog_threshold`

Read what the steady loop's triggers actually use (`internal/sync/sync.go`,
`steady.go`, the daemon's sync tick), then for each field either:

- wire it in, with one owner and the existing default when it's zero; or
- delete it: the field, its validation, its settings-form entry and its
  tests.

Report which one you did for each field, and why, citing the lines.

### 4. Save the plan

The last step saves these four round plans, one after another, as
`docs/plans/2026-10-10-sync-compaction-join.md`, in a new commit.

## Tests and mutations

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

## Constraints

- `GOTOOLCHAIN=go1.27.2 make check` must pass.
  - Coverage stays within one point of the baseline, and no baseline is
    lowered.
  - Functions are at most 70 lines and files at most 600. Add no new
    exclusions.
- Comments say why. No issue numbers or history in code.
