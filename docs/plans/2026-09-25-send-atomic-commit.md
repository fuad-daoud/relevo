# Send records a round in one database transaction (#471)

## 1. System Overview

`Send` (`internal/relevo/send.go`) opens a round under `rt.Store.WithLock`.
That is a file lock; every `tx.AppendLog` and `tx.Save` inside it commits
its own SQLite transaction. After spawning the builder, `Send` makes up to
four writes: the pick entry (line 467), the plan entry (line 479), the drift
entry (line 493) and `tx.Save(b)` (line 554). The spawn-failure branch makes
two: the pick entry (line 441) and `tx.Save(b)` (line 445). If a later write
fails, the earlier ones stay. The result is a log with a plan entry for a
round whose binding was never saved.

This round adds `(*store.Tx).SaveWithLog(b, entries...)`. It saves the
binding and appends every entry in one database transaction: all or
nothing. `Send` collects its entries and calls it exactly once per path.
`save` and `appendLog` are refactored to share their validation and encoding
with it. Their behaviour is unchanged.

**This round deletes no behaviour.** Every existing test must pass
unchanged. The only comment rewritten is the one after the lock in `Send`
(line ~560, "The binding's writes rolled back ..."), which becomes true.

## 2. File Structure

```
internal/db/record.go            MODIFIED  (t *Tx) EventMaxSeq; (d *DB) EventMaxSeq delegates to it
internal/store/store.go          MODIFIED  prepareSave extracted from save; (t *Tx) SaveWithLog + (s *Store) saveWithLog
internal/store/log.go            MODIFIED  encodeEvent extracted from appendLog
internal/store/log_test.go       MODIFIED  two tests for SaveWithLog
internal/relevo/send.go          MODIFIED  both paths write through one tx.SaveWithLog
internal/relevo/send_test.go     MODIFIED  one test: pick + plan entries order and seqs via SaveWithLog
docs/plans/2026-09-25-send-atomic-commit.md   NEW  this plan (last step)
```

Nothing else changes. If you need to touch another file, halt and report.

## 3. Data Structures & Type Definitions

No new types. The new functions:

- `internal/store/store.go`:
  `func (s *Store) prepareSave(b Binding) (Binding, db.Record, error)`. This
  is the part of `save` (currently lines 429-473) from the `b.Format` check
  through the `json.Marshal`:
  - format check and stamping;
  - `ValidName`;
  - the empty-CWD check;
  - `assertCWDFree`;
  - defaults (`CreatedAt`, `UpdatedAt`, `RoundCap`, `RoundTimeoutMS`);
  - `MkdirAll` of the binding dir.

  It returns the stamped binding and the `db.Record` that `save` passes to
  `RecordPut` today, with Owner, Name, State, Round, CWD, JSON, CreatedAt
  and UpdatedAt exactly as now. `save` becomes `prepareSave` →
  `dbForWrite` → `d.RecordPut(rec)` → `importPresent`, with the same error
  wrapping (`save binding %q: %w`).
- `internal/store/log.go`:
  `func encodeEvent(e LogEntry, seq int) (db.RecordEvent, error)`. It
  defaults a zero `TS` to `time.Now().UTC()`, sets `e.Seq = seq`, marshals,
  and returns `recordEventOf(e, string(encoded))`. `appendLog` uses it in
  place of its own TS default, `e.Seq = n + 1` and `json.Marshal`. The
  error text `encode log entry: %w` is unchanged.
- `internal/db/record.go`: `func (t *Tx) EventMaxSeq(recordID string)
  (int, error)` runs the same SQL as `(d *DB) EventMaxSeq` (line 429) via
  `t.queryRow`. `(d *DB) EventMaxSeq` keeps its signature and behaviour. It
  may keep its own query; do not route it through a write transaction.

## 4. Interface Definitions & Component Contracts

**`func (t *Tx) SaveWithLog(b Binding, entries ...LogEntry) error`**
(`internal/store/store.go`, next to `(t *Tx) Save` at line 402). It
delegates to `t.s.saveWithLog(b, entries)`.

Contract:
- Preconditions: the caller holds the lock (it is a `*Tx` method).
  `entries` may be empty, in which case this is exactly `Save`.
- Postconditions on success:
  - the binding row is saved as `Save` would save it;
  - each entry is appended in argument order with consecutive seqs, starting
    at the record's current max seq + 1, each with `TS` defaulted as
    `AppendLog` does.
- On any error, **nothing** is written: not the binding, not any entry.
- Errors:
  - everything `Save` returns;
  - `log exceeds %d entries` (the same text as `appendLog`) when appending
    would pass `maxLogEntries`;
  - `encode log entry: %w`;
  - a wrapped db error, including `db.ErrBusy` after `db.Tx`'s own retry.

`saveWithLog` pseudocode:
```
if err := ValidName(b.Name) ...                        // (inside prepareSave)
if err := s.importPresent(b.Name); err != nil { return err }   // as appendLog does first
b, rec, err := s.prepareSave(b)
d, err := s.dbForWrite()
err = d.Tx(func(dtx *db.Tx) error {
    id, err := dtx.RecordPut(rec)            ; wrap "save binding %q: %w"
    n, err := dtx.EventMaxSeq(id)
    for _, e := range entries {
        if n >= maxLogEntries: return fmt.Errorf("log exceeds %d entries", maxLogEntries)
        n++
        ev, err := encodeEvent(e, n)
        if err := dtx.EventAppend(id, ev); err != nil { return err }
    }
    return nil
})
if err != nil { return err }
return s.importPresent(b.Name)                          // as save does last
```
`importPresent` before the transaction adopts a waiting `log.jsonl` before
seqs are computed. The call after it matches `save` and is a no-op
otherwise. If `importPresent` cannot be called before `prepareSave` because
the record does not exist yet, keep only the trailing call, as `save` does,
and say so in the report.

## 5. High-Level Pseudocode: `Send` (`internal/relevo/send.go`)

Inside the `WithLock` closure (starts line 333):
```
var pending []store.LogEntry            // declared at the top of the closure

spawn-failure branch (lines ~436-449):
    b.State/Halt/HaltAt as today
    var failEntries []store.LogEntry
    if pf.pick != nil: failEntries = append(failEntries, pickEntry(now, b.Round, "builder", *pf.pick))
    if saveErr := tx.SaveWithLog(b, failEntries...); saveErr != nil {
        return fmt.Errorf("%v; and saving NEEDS YOU failed: %w", err, saveErr)
    }
    return err
    // the old "and appending the builder pick failed" message goes away: one write now

success path:
    line 467: pick   -> pending = append(pending, pickEntry(...)); pickLine = PickText(...) as today
    line 479: plan   -> pending = append(pending, entry)
    line 493: drift  -> pending = append(pending, driftEntry); driftLine = DriftLine(...) as today
    line 554: return tx.Save(b)  ->  return tx.SaveWithLog(b, pending...)
```
Entry order is unchanged (pick, plan, drift), so `builderForRound` and every
reader see the same sequence as before. All timestamps are still taken where
they are today.

After the lock (line ~556-570), the kill-on-failure block from #467 stays
exactly as is. Only its comment changes: the binding and its log entries are
now one transaction, so nothing of this round was recorded.

## 6. Error Handling Strategy

- `SaveWithLog` is all-or-nothing. A failure returns the error unchanged, so
  `errors.Is(err, db.ErrBusy)` and `store.ErrNewerFormat` still hold for
  callers.
- In `Send`, a `SaveWithLog` failure on the success path reaches #467's
  kill block: the builder is stopped, and now no entry of the round
  survives.
- The drift patch round file (`CaptureDrift` → `PutRoundFile`) stays a
  separate write, deliberately. It is addressed by round, and a stray file
  is harmless.

## 7. Working Efficiently

Each model step costs a round trip, so:
- Read these in one step, as parallel reads: `internal/store/store.go`
  395-500; `internal/store/log.go` 1-30 and 270-360;
  `internal/store/log_test.go` 1-100; `internal/db/record.go` 385-475;
  `internal/relevo/send.go` 320-575; `internal/relevo/send_test.go` 820-900
  (`TestSendBuilderMovesTheCandidateAndPersists`). Do not search for what
  this plan locates.
- Make every change to one file in a single edit call.
- Focused checks:
  - `go test ./internal/store/ -count=1`
  - `go test ./internal/db/ -count=1`
  - `go test ./internal/relevo/ -run 'TestSend' -count=1`
- Full check, once, at the end: `make check`. If this machine blocks heavy
  commands, use `dev run make check`. If `dev run` fails because `dist/` or
  `.git` is missing on the mirror, run `dev run go test -race -count=1
  ./internal/db/ ./internal/store/ ./internal/relevo/` instead, say so in
  the report, and rely on the PR's CI.
- CI runners have no harness binary, systemd user session or network. These
  tests use only the store, the db and `fakeRunner`.

If any step is impossible as written or contradicts the code, stop and
report. Do not bend the plan or a test to fit.

## 8. Ordered Implementation Steps

**Step 0: sync.** `git fetch origin && git merge --ff-only origin/main`.
Confirm, and halt if either anchor is missing:
- `send.go` has `tx.AppendLog(name, entry)` for the plan entry and a
  closing `return tx.Save(b)` inside `rt.Store.WithLock`, and a comment
  containing `writes rolled back`.
- `store.go` has `func (s *Store) save(b Binding) error` calling
  `d.RecordPut(db.Record{`.

**Step 1: refactor with no behaviour change.** Extract `prepareSave` and
`encodeEvent`, and add `(t *db.Tx) EventMaxSeq`, as in section 3.
Verification: `go test ./internal/store/ ./internal/db/ -count=1` is green
with no test edited.

**Step 2: `SaveWithLog`, test first.** In `internal/store/log_test.go`:
- `TestSaveWithLogWritesBindingAndEntriesTogether`: after `seedBinding`,
  change the binding's State and call `s.WithLock(func(tx *Tx) error {
  return tx.SaveWithLog(b, e1, e2) })`. Assert:
  - the reloaded binding has the new State;
  - `ReadLog` returns e1 then e2 with consecutive seqs after the seed's
    existing entries (0 today);
  - both have non-zero TS.
- `TestSaveWithLogWritesNothingWhenAnEntryFails`: seed the binding's log
  with `maxLogEntries-1` entries in one go. First write a `log.jsonl` of
  that many lines at `s.logPath(name)`, as
  `TestReadLogRefusesOversizedLogInsteadOfTruncating` does, then call
  `s.ReadLog(name)` so `importPresent` adopts it. If that route does not
  adopt it, seed with one `EventReplaceAll` through `s.dbForWrite()`
  instead and note which route you used. Then change the binding's State
  and call `SaveWithLog(b, e1, e2)`. e1 fits and e2 passes the cap. Assert:
  - the error mentions `exceeds`;
  - the reloaded binding still has the **old** State;
  - `ReadLog` still has exactly `maxLogEntries-1` entries.

Run them; they must fail to compile or fail. Then implement section 4.
Verification: the store package is green.

**Step 3: `Send`, test first.** In `internal/relevo/send_test.go`, add
`TestSendRecordsPickAndPlanInOrderInOneWrite`. Model it on
`TestSendBuilderMovesTheCandidateAndPersists` (line 831). After a `--builder`
send, the new round's log entries are pick then plan, with consecutive seqs,
and the binding carries the new pid. This test likely passes before the
change as well: it pins the order across the refactor. Say so in the report
if it does. Then implement section 5. Verification: `go test
./internal/relevo/ -run 'TestSend' -count=1` is green, including #467's
`TestSendKillsTheBuilderWhenTheSendFailsAfterSpawn`, with no existing test
edited.

**Step 4: mutation check.** Replace `saveWithLog`'s single `d.Tx` with the
non-atomic sequence: `s.save(b)`, then `s.appendLog` for each entry. Run
`go test ./internal/store/ -run TestSaveWithLog -count=1`. The confirmed
result is that `TestSaveWithLogWritesNothingWhenAnEntryFails` fails (the new
State or e1 persisted). Restore the change, then record the failure line.

**Step 5: full check.** Run `make check` (see section 7). It must pass.

**Step 6: ship.** Copy the plan file you were given (its path is in your
prompt) to `docs/plans/2026-09-25-send-atomic-commit.md`. Commit everything
as one commit:
`fix(send): record a round's log entries and binding in one database transaction (#471)`
with `Fixes #471` in the body. Push, and open a PR against `main` whose body
includes `Fixes #471`. Don't wait for CI and don't merge.

The report states:
- the pre-fix failure of each step 2 test;
- whether the step 3 test passed before the change;
- the step 4 mutation result;
- the full-check result;
- the PR number;
- `git diff --stat`.
