# A4-2b: the round table names its candidate and its actor

Spec: `docs/specs/2026-09-26-a4-state-rename-design.md`, §2 and §3.4. It is a clean
break (D1): nothing writes or emits the old names.

**Vocabulary:**
- An **actor** is config.
- A **runner** plays one actor.
- A **candidate** is the model a round runs on.
- `builder` stays valid only as the seeded writer actor's name.

This round owns the **round table and its readers**:
- `internal/db`;
- `internal/ingest`;
- `internal/histq`;
- `internal/stats`;
- `cmd/relevo/history.go`.

Other A4 rounds own `store.Binding`, `status --json`, the wire, and the CLI flags and
texts. **Do not edit** `internal/store`, `internal/relevo`, `internal/view`,
`internal/remote`, `internal/serve` or `cmd/relevo/bind.go`. If a change there becomes
unavoidable, stop and report.

**If a step is impossible as written or contradicts the code, stop and report. Do not
improvise, and do not bend a test to fit.**

## 1. Migration `internal/db/migrations/007_round_actor.sql`

Follow the dialect rules in the headers of 001, 003 and 005: CREATE … IF NOT EXISTS, no
AUTOINCREMENT, no triggers. 005 shows that DROP INDEX is allowed. This is the first
ALTER TABLE, so add a header comment that says SQLite ≥ 3.25 supports `RENAME COLUMN`,
and that modernc's sqlite does.

```
ALTER TABLE round RENAME COLUMN builder_candidate TO candidate;
ALTER TABLE round RENAME COLUMN builder_harness   TO harness;
ALTER TABLE round RENAME COLUMN builder_provider  TO provider;
ALTER TABLE round RENAME COLUMN builder_model     TO model;
ALTER TABLE round RENAME COLUMN builder_mode      TO mode;
ALTER TABLE round ADD COLUMN actor TEXT NOT NULL DEFAULT 'builder';
DROP INDEX IF EXISTS round_builder_idx;
CREATE INDEX IF NOT EXISTS round_candidate_idx ON round(harness, provider, model);
```

This is the executable content of the migration file, not an implementation sketch.

- Existing rows keep their values. `actor` defaults to `builder`, which every existing
  round was, apart from consults, which are not round rows.
- Read `internal/db/migrate.go` (`applyOneMigration`, ~line 112) to confirm that a
  multi-statement file runs in one transaction. If it does not, stop and report.
- Regenerate `internal/db/testdata/schema.golden`.

## 2. Go names (internal/db)

- In `types.go` (`Round` and `RoundRow`), `BuilderCandidate`, `BuilderHarness`,
  `BuilderProvider`, `BuilderModel` and `BuilderMode` become `Candidate`, `Harness`,
  `Provider`, `Model` and `Mode`. Add `Actor string`.
- Every query in `read.go`, `write.go` and `lookup.go` uses the new column names and
  reads and writes `actor`.
- Do it as one mechanical pass: `gopls rename` if it is installed, else `gofmt -r`
  per field, then fix what no longer compiles.

## 3. Ingest writes the actor, and stops guessing it

- `internal/ingest/outcome.go`: `builderForRound` becomes `candidateForRound`.
- `isRolePick(note)` becomes `isOtherActorPick(note, actor string)`. A pick
  "picked <tok> for <x>: …" is skipped only when `x != actor`, where `actor` is the
  binding's actor: `b.Role`, or `"builder"` when that is empty.
  - Read `b.Role` as is. A4-2a renames the field, and whichever round merges second
    fixes the one line.
  - So a custom writer actor's own picks are counted, which fixes a real bug, and a
    consult's pick is still skipped.
- `ingest_tx.go` and `dedupe.go`: the round row gets `Actor` from the same value, plus
  the renamed fields.

## 4. History and queries

- `internal/histq`: the axis `AxisBuilder Axis = "builder"` (histq.go:~54) becomes
  `AxisCandidate Axis = "candidate"`. `by:builder` is now an **unknown axis**, a clean
  break, with the parse error listing the valid axes.
- Add `by:actor` if histq's axis table makes it a one-line addition, grouping by
  `round.actor`. Otherwise leave it out and say so in the report.
- `internal/histq/group.go`, `internal/stats/*.go` and `cmd/relevo/history.go`: the
  renamed fields.
- The history JSON keys (`BuilderCandidate` … `BuilderMode` in `history-*.golden`)
  follow the Go field names, so they become `Candidate` … `Mode`, plus `Actor`.
  Regenerate:
  - `cmd/relevo/testdata/contract/history-*.golden`;
  - every stats or histq golden that moves.

  List each one.

## 5. Tests

1. `TestMigration007RenamesRoundColumnsAndKeepsRows`:
   - open a DB migrated only to 006 (use the test helpers `migrate.go` offers, or apply
     001-006 by hand from the embedded FS);
   - insert a round row with every `builder_*` column set;
   - apply 007;
   - assert the values are in the new columns and `actor` is `builder`;
   - assert a second migrate run is a no-op.
2. `TestIngestCountsACustomWriterActorsOwnPicks`:
   - a binding whose actor is `designer`, with a pick entry
     "picked <tok> for designer: …";
   - the round's candidate is `<tok>` and its actor is `designer`.
3. `TestIngestSkipsAConsultPick`: on a `builder` binding, "picked <tok> for reviewer: …"
   is skipped.
4. `TestHistqByBuilderIsUnknown`, plus `by:candidate` grouping.
- **Required mutations.** Run each, confirm the named test fails, then restore. Report
  all three.
  - (a) `isOtherActorPick` compares with the literal "builder": test 2 fails.
  - (b) Ingest writes no `Actor`: test 2 fails.
  - (c) Drop the `ADD COLUMN actor` statement: test 1 fails.

## 6. Working efficiently

- Batch-read these, and nothing else unless a compile error points there:
  - internal/db/migrations/001_initial.sql 55-90, 005_*.sql and 006_*.sql;
  - internal/db/migrate.go;
  - internal/db/types.go, read.go, write.go and lookup.go;
  - internal/ingest/outcome.go, ingest_tx.go and dedupe.go;
  - internal/histq/histq.go and group.go;
  - internal/stats/{groups,scorecard,spend,stats}.go;
  - cmd/relevo/history.go;
  - the tests next to each.
- Focused loop: `go build ./... && go test -count=1 ./internal/db/ ./internal/ingest/ ./internal/histq/ ./internal/stats/ && go test -count=1 ./cmd/relevo/ -run 'History|Contract'`
- Full check, once at the end, on this server:
  `export TMPDIR=$HOME/.cache/go-tmp GOTMPDIR=$HOME/.cache/go-tmp; mkdir -p $TMPDIR; make check`

## 7. Commit

Copy this plan verbatim to `docs/plans/2026-09-26-a4-2b-round-columns.md`. Commit as
**one new commit**: `feat(a4): the round table names its candidate and actor; by:candidate`.
Never amend, and never rebase.

## Report

The report covers:
- `git diff --stat`;
- the goldens that moved, and why;
- the three mutations;
- the migrate.go transaction finding;
- whether `by:actor` was added;
- `make check`'s last lines;
- anything that did not match.
