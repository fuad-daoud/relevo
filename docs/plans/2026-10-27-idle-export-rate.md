# Plan: measure exports made while no round runs

Issue #1361, slice C. This slice **measures and exposes a pure reader only**. A later slice renders the warning in `relevo status` and `db sync status`, so add no output here.

Save this plan as `docs/plans/2026-10-27-idle-export-rate.md` and commit it with the work.

## Why

For weeks zen exported about 3,700 log entries an hour while no round ran, and nothing said so. The only persisted record of exports today is `sync.last_attempt` (`internal/sync/attempt.go:12-43`), which each attempt overwrites, so there is no rate. The sync code also cannot tell whether a round is running. The owner's rule: warn when a machine keeps exporting with no round running, more than about 50 entries an hour.

## Behaviour

1. **The daemon tells the sync runner how many rounds are open.**
   - Add `(r *Runner) NoteRoundsOpen(n int)` to `internal/sync`. It is nil-safe and stored atomically, because attempts run on the sync goroutine (`queueSync`, `internal/relevo/sync_tick.go:45`) while the daemon ticks.
   - Until the first note, the count is **unknown**. Unknown is not idle, so a daemon that just started never books its first attempt as idle while a round may be open.
   - `Daemon.Tick` (`internal/relevo/daemon.go:246`) notes the number of bindings whose current round is open: `store.RoundOpen(entries, b.Round)`, `internal/store/log.go:133`. A remote binding counts too, since its prompt and report sit in the local log.
   - It notes after the bindings loop, and notes 0 on the `len(bindings) == 0` early return.
   - The attempt that `idleSync` queues earlier in the same tick therefore classifies against the previous tick's count. That one-tick lag is fine, and the comment should say why.
   - Read entries the tick already reads, if `tickOne`/`reconcileWith` has them in hand. Otherwise do one log read per binding in a helper, and report which you did.
   - **`daemon.go` is at 897 lines and on the file-size allow list. Add at most ~6 lines there**: the calls only. Put the counting helper in `internal/relevo/sync_tick.go` (233 lines) or a new small file.
2. **Each attempt books its exports into an hourly record.**
   - `RecordAttempt` (`attempt.go:31`) also adds `out.Exported` to the hour holding `a.End` (UTC, keyed `2006-01-02T15`).
   - It books into `exported`, and also into `idle` when the noted count is known and 0.
   - The record is one local kv row, `sync.exports.hours`: a JSON list of `{"hour":"2026-10-27T17","exported":N,"idle":N}`, oldest first, capped at the newest 48 hours. It is one row, so nothing needs pruning by key.
   - An attempt that exported 0 books nothing new, but must not drop the existing hours.
   - Write through `r.put`/`marker`, like the other markers. The row is machine-local and never synced: the `sync.` prefix lives in the local file (`internal/sync/sync.go:15-23`).
3. **A pure reader.** `IdleExports(kv db.KV, now time.Time) (ExportHour, bool, error)` returns the **last complete hour**, the hour before `now`'s UTC hour. It returns false when that hour has no row. A machine that exported nothing in the hour has no row, so false means "nothing to warn about", not an error.
   - Add `const IdleExportsWarn = 50` with a comment giving the owner's threshold and why the number is not zero: a person's own session hooks and config edits export a few entries with no round open.
   - Add `func (h ExportHour) OverIdle() bool { return h.Idle > IdleExportsWarn }`.
   - Use the last complete hour, not a rolling window, because a partial hour cannot be compared with a per-hour threshold.

## Seams

- `internal/sync/runner.go`: one field on `Runner` (an `atomic.Int64` holding `n+1`, where 0 means unknown, or an equivalent you document).
- `internal/sync/exports.go` (new): `ExportHour`, the key, the cap, `recordExports`, `IdleExports`, `IdleExportsWarn` and `NoteRoundsOpen`.
- `internal/sync/attempt.go`: one call in `RecordAttempt`.
- `internal/relevo/sync_tick.go`, or a new file: the open-round counter.
- `internal/relevo/daemon.go`: the note calls only.

Declared file set: these, their `_test.go` siblings, and the plan doc.

## Cases (tests to add)

- In `internal/sync/exports_test.go`:
  - Exports in one hour accumulate.
  - A new hour starts a new row.
  - The list caps at 48 and drops the oldest.
  - Idle books only when the count is known and 0. Unknown, or n > 0, books `exported` only.
  - `IdleExports` returns the last complete hour, not the current one.
  - A missing hour returns false.
  - `OverIdle` is false at exactly 50 and true at 51.
  - A malformed row is read as empty and rewritten, not fatal. The precedent is `marksDue` (`steady.go:255-270`).
- In `internal/relevo`, driven by the daemon `Tick` with no network, as `sync_outbox_test.go` drives it:
  - With no bindings, the runner's count is 0.
  - One binding with a round open (a prompt entry and no report) gives 1.
  - A binding whose round has its report gives 0.
  - Expose the noted value to tests through an accessor in `internal/sync` (for example `RoundsOpen() (int, bool)`), which is also what the reader will use.

Mutation checks, each re-run and reported with the failing test's name:
1. Book idle when n > 0.
2. Have `IdleExports` read the current hour.
3. Drop the note on the no-bindings return.
4. Treat unknown as 0.

## Gate

- `go test ./internal/sync/ -count=1`
- `go test ./internal/relevo/ -run 'Sync|Outbox|Idle|RoundsOpen' -count=1`
- `gofmt -l` on touched files, `make lint`, `sh scripts/check-filesize.sh` and `sh scripts/check-comments.sh`.
- Do NOT run `make check` or `make check-affected`.

## Style

Follow CLAUDE.md "Code style":
- A comment says *why*.
- No issue numbers or history in code or tests.
- Functions are at most 70 lines and files at most 600.
- A test's name says what it pins.

## Report

Include:
- the files changed against the declared set;
- where the open-round count comes from (reused entries or one read per binding);
- lines added to `daemon.go`;
- each gate command with its result;
- the four mutation results.

Commit everything, and leave the tree clean.
