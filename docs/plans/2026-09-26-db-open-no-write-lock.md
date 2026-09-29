# db.Open: no write lock when the schema is current (#530) (2026-09-26)

## 1. Overview

`db.Open` (`internal/db/db.go`, `func Open`, ~lines 66-121) calls `applyMigrations` on every open. For each embedded
migration file, `applyOneMigration` (`internal/db/migrate.go`, ~lines 120-175) runs `CREATE TABLE IF NOT EXISTS
schema_version` and then takes `BEGIN IMMEDIATE` (the write lock), only to read back that the migration is already
applied.

That makes every relevo command take the write lock about 10 times before it does anything. Each attempt waits
`busyTimeoutMS` (5 s) with no retry. Under write load from the daemon and remote log mirroring, commands fail with
`db: open …: migrate: open failed: begin migration 001_initial.sql: busy`.

**The fix is one condition.** In `Open`, after the existing `if have > know { … }` branch, return early when
`have == know`, with the same `&DB{sqlDB: sqlDB, have: have, know: know}` the end of `Open` returns. The schema is
current, so there is nothing to migrate and no write is needed.
- `maxVersion` and `maxEmbedded` are plain reads, and WAL readers do not block on a writer.
- The `have < know` path, a real migration, is unchanged and keeps its `BEGIN IMMEDIATE` serialisation.

## 2. Files

```
internal/db/db.go        Open: the have == know early return
internal/db/db_test.go   + TestOpenCurrentSchemaWhileAnotherProcessWrites
docs/plans/2026-09-26-db-open-no-write-lock.md
```

## 3. Contract

`Open(path)` on a database whose `MAX(schema_version) == maxEmbedded(migrationFiles)`:
- runs no `CREATE`, no `BEGIN IMMEDIATE` and no other write;
- succeeds while another connection holds the write lock.

Everything else `Open` does is unchanged: the `ping`, the `chmod`s, and the `Newer()` handling.

## 4. Steps

### 0. Working efficiently

**How to work:**
- Read `internal/db/db.go:30-140`, `internal/db/migrate.go:1-180` and `internal/db/db_test.go:1-240` in one batch.
- Make each file's change in one edit.

**Commands:**
- First run `mkdir -p $HOME/.cache/relevo-verify/tmp`.
- Focused: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/db/ -run 'Open|Migrat' -count=1 -race`
- Final:
  - `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/db/ ./internal/config/ -count=1 -race`
  - `go vet ./internal/db/`
  - `test -z "$(gofmt -l $(git ls-files '*.go'))"`
  - `sh scripts/check-comments.sh`
  - `sh scripts/check-filesize.sh`
- **On a relevo worktree on this server, `git ls-files` may fail**, because `.git` is a pointer file. When it does, run
  `gofmt -l internal/db/` and `sh scripts/check-comments.sh` against the files you touched by hand, and say so.
- Skip `make check`.
- Comments say *why*, with no issue numbers, `§` or plan references; `check-comments.sh` enforces this. The test's
  name says what it pins.

### 1. The early return in `Open` (§1)

Give it a one-line comment saying why: a current schema needs no write, and taking the write lock here made every
command fail under load.

### 2. `TestOpenCurrentSchemaWhileAnotherProcessWrites` in `db_test.go`

1. `path := filepath.Join(t.TempDir(), "relevo.db")`. Then `db.Open(path)` once and close it, so the schema is current.
2. Open a second, independent `*sql.DB` on the same path with the `sqlite` driver, as `db.Open` does (same DSN
   pragmas). On one `Conn`, execute `BEGIN IMMEDIATE`, and keep it open until the test ends (`t.Cleanup` rolls it back).
3. Shrink the busy timeout for this test: set `busyTimeoutMS` to `200` and restore it in `t.Cleanup`. It is a package
   var for exactly this purpose, per its comment.
4. `db.Open(path)` again must return no error, and within 2 s: assert elapsed < 2 s.
5. Its `Newer()` is false.

**Required mutation:** delete the early return. The test must fail with `busy`. Report the failing line, then revert.

Also confirm that `TestOpenCreatesAndMigrates`, `TestOpenIsIdempotent`, `TestOpenLeavesANewerSchemaAlone` and
`TestConcurrentOpenAppliesEachMigrationOnce` pass unchanged.

### 3. Checks, the plan, the commit

1. Run the final commands listed in step 0.
2. Copy this plan to `docs/plans/2026-09-26-db-open-no-write-lock.md`.
3. `git add -A && git commit -m "fix(db): open a current database without the write lock (#530)"`. A new commit.

## 5. Deletions

None.

## 6. Stop rather than improvise

Halt and report if any of these happens:
- `Open`'s structure differs from §1;
- an existing db test fails with the early return in place;
- the new test passes without the early return. That means the premise is wrong, so report what `Open` does.
