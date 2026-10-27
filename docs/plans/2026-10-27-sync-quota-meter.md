# Plan: show the account's Turso traffic, its pace, and idle exports

Issue #1361, slice B: the user-visible half. Slice A, already on main, publishes every machine's monthly traffic and keeps a local snapshot (`internal/sync/traffic.go`: `ReadAccount`, `AccountTraffic`, `Account`). Slice C, also on main, records hourly exports and whether a round was open (`internal/sync/exports.go`: `IdleExports`, `ExportHour.OverIdle`, `IdleExportsWarn`). This slice reads those, with no network, and renders them in three places.

Save this plan as `docs/plans/2026-10-27-sync-quota-meter.md` and commit it with the work.

## Why

In October `relevo db sync status` said "turso sync 7.0 GB of 10.0 GB (70.4%)" on each machine while the account stood at 23.5 GB, because it compared one machine's traffic with the account's quota. Nothing warned. And zen exported about 3,700 entries an hour for weeks with no round running, and nothing said so either.

## Behaviour

1. **One reading, pure, in `internal/sync`** (new file `internal/sync/meter.go`):
   - `type QuotaReading struct { Account Account; Own int64; Quota int64; Projected int64; Projectable bool }`. `Own` is this machine's `TursoPush + TursoPull` for the month.
   - `ReadQuota(kv db.KV, origin string, now time.Time) (QuotaReading, bool, error)` reads `ReadSettings`, `ReadBytes`, `ReadAccount`, and folds them with `AccountTraffic`. It returns **false when there is nothing to report**: this machine moved no bytes this month *and* the snapshot has no row for the current month. That is a machine that has never synced, or one with sync off since before the month began. A machine with sync off but traffic earlier this month still reports, because those bytes were spent.
   - **Pace.** Measure elapsed time from the start of `now`'s UTC month. When less than 24 hours have passed, `Projectable` is false: a few early hours extrapolate wildly. Otherwise `Projected = Total * monthLength / elapsed`, where `monthLength` is the real length of that calendar month. Say in a comment where the 24 hours comes from.
   - **Thresholds**, as constants with a one-line why each:
     - `quotaWarnShare = 0.80`: doctor warns once the account has used this share.
     - `paceNoticeShare = 0.50`: status shows a notice once the month is *on pace* for this share.
     - Doctor also warns when the projection reaches the quota (`Projected >= Quota`).
   - Methods: `UsedShare() float64`, `OverWarn() bool`, `PaceOver() bool`, `PaceNotice() bool`. Guard every share against `Quota <= 0`: no quota means no warnings, never a division by zero.
2. **The quota clause names the scope** (`FormatBytes` in `internal/sync/bytes.go`, `FormatQuotas` in `logsize.go`):
   - The clause that read `turso sync X of Q (p%)` now reads `this machine: turso sync X`, with no quota and no percentage, because this machine's share is not what the quota measures.
   - `FormatQuotas` gains the account clause through a new `FormatAccount(q QuotaReading) string`:
     - `account: turso sync 22.2 GB of 10.0 GB (222.0%) across 3 machines, on pace for 30.1 GB, as of 14:05Z`;
     - drop the pace part when the month is not projectable;
     - when the snapshot was never read (`Account.ReadAt` zero): `account: not read yet`.
   - Change `FormatQuotas`'s signature as needed. Its callers are `cmd/relevo/db_sync_r2.go:323` and its tests.
   - Update the two tests that pin the old wording (`internal/sync/bytes_test.go:102`, `internal/sync/quota_test.go:25`) to the new wording. This is the intended change, not a weakened test. Say so in the report.
3. **`relevo db sync status`** (`cmd/relevo/db_sync_r2.go`, `db_sync_status.go`):
   - `dbSyncStatusRead` needs this machine's origin for `ReadQuota`. Its one caller (`db_sync_r2.go:189`) holds `shared`, so pass `shared.Origin()` in.
   - The document gains `Account *dbSyncAccountDoc` (`json:"account,omitempty"`) with `month`, `total`, `machines`, `own`, `quota`, `projected` (omitempty), `read_at` (omitempty), `over_warn` and `pace_over`. It is present when `ReadQuota` returns true.
   - It also gains `IdleExports *dbSyncIdleDoc` (`json:"idle_exports,omitempty"`) with `hour`, `exported`, `idle` and `over`. It is present when `IdleExports` returns a row.
   - Keep the existing `bytes` and `quota_turso_sync` fields. JSON consumers may read them, and removing them is not this slice's business.
   - Text output:
     - The quotas clause renders through the new `FormatQuotas`.
     - When the idle hour is `OverIdle()`, append ` · <idle> entries exported in <hour>Z with no round open (more than <IdleExportsWarn> an hour is unexpected)`.
     - When `PaceOver()` or `OverWarn()`, append ` · warning: <the reason>`.
   - Run `TestHelpJSONDocumentsTheSurface`. If it documents this document's fields, regenerate it with `go test ./cmd/relevo -run TestHelpJSONDocumentsTheSurface -update` and say so.
4. **`relevo status`** (`cmd/relevo/status.go:218-242`): one more notice line above the rows, text mode only, beside `statusNotice`/`restartNotice`/`daemonNotice`.
   - It is a pure `syncMeterNotice(q QuotaReading, qok bool, idle ExportHour, iok bool) string`. It returns `""` unless one of these holds, checked in this order (first wins):
     - `PaceOver()`: `sync: the account's Turso traffic is on pace for P this month, over its Q quota -- relevo db sync status`;
     - `OverWarn()`: `sync: the account has used U% of this month's Q Turso quota -- relevo db sync status`;
     - `PaceNotice()`: `sync: the account's Turso traffic is on pace for P this month, S% of its Q quota -- relevo db sync status`;
     - `idle.OverIdle()`: `sync: N entries were exported in <hour>Z with no round open -- relevo db sync status`.
   - The read goes through `rt.Store.DB()`, then `relevosync.LocalHandle`, then `d.Origin()`. **Any read error prints nothing**: status must never fail because of the meter. This is the same rule as the daemon notice at `status.go:233-241`.
   - `status.go` is at 407 lines. Put the reader and the pure function in a new `cmd/relevo/status_sync.go`.
5. **`relevo doctor`**: a `sync quota` row.
   - It lives in a new `cmd/relevo/doctor_sync.go`, as a pure `syncQuotaCheck(q QuotaReading, qok bool, idle ExportHour, iok bool) (doctor.Check, bool)`. The bool reports whether there is a row at all: no row when `!qok && !iok`.
   - `SevWarn` when `OverWarn()`, `PaceOver()` or `idle.OverIdle()`. The detail names every reason that holds.
   - Otherwise `SevOK`, with the detail `account <total> of <quota> (<share>%) across N machines`, plus `, on pace for P` when projectable.
   - `Fix` is `relevo db sync status`.
   - Insert it with `insertGlobalCheck` right after the `database` row (`doctor.go:324`). **`doctor.go` is at 587 lines: add at most 4 lines there**, the read and the insert. Put the reading helper in `doctor_sync.go`. The read is the same no-network local read as in status, and a read error means no row.

Out of scope: the join guard (`internal/sync/joinguard.go:165`). A machine re-enabling sync has no current-month snapshot yet, so the account figure there needs its own design. The MasterMind will file it.

## Seams and files

- New: `internal/sync/meter.go` (+ `meter_test.go`), `cmd/relevo/status_sync.go`, `cmd/relevo/doctor_sync.go`, and their `_test.go` files.
- Edited: `internal/sync/bytes.go`, `internal/sync/logsize.go`, their two tests, `cmd/relevo/db_sync_r2.go`, `cmd/relevo/db_sync_status.go`, `cmd/relevo/status.go` (the call only), `cmd/relevo/doctor.go` (≤4 lines), the help golden if needed, and the plan doc.
- CI runners have no harness and no network, and `cmd/relevo`'s `TestMain` isolates HOME/XDG (`cmd/relevo/CLAUDE.md`). **The `cmd/relevo` tests exercise the pure functions** (`syncMeterNotice`, `syncQuotaCheck`, the status-doc fill from a local kv the test builds) and never execute a subcommand that spawns a harness or reaches the network.

## Cases

- `internal/sync/meter_test.go`:
  - no bytes and no current-month snapshot gives false;
  - own bytes only gives true, with `Machines` 1;
  - the pace is not projectable at 23 hours and projectable at 25;
  - a mid-month projection, worked by hand (for example, 3 GB on the 10th of a 31-day month is about 9.3 GB);
  - the edges at exactly 80% of the quota, at 50% pace, and at a projection equal to the quota;
  - `Quota` 0 gives no warnings;
  - `FormatAccount` in its three shapes (full, unprojectable, not read yet).
- `cmd/relevo`:
  - `syncMeterNotice`: each of the four lines, the first-wins order (pace over beats over-warn), and `""` when calm;
  - `syncQuotaCheck`: no row; OK; warn with two reasons named;
  - the `db sync status` text and JSON with an account and an idle-over hour, filled from a temp local kv.

Mutation checks, each re-run and reported with the failing test's name:
1. Read the 80% threshold as 0.08.
2. Drop the 24-hour projectable guard.
3. Have `ReadQuota` use this machine's bytes as the account total.
4. Return the first warning instead of first-wins.
5. Let a status read error return an error instead of printing nothing.

## Gate

- `go test ./internal/sync/ -count=1`
- `go test ./cmd/relevo/ -run 'Sync|Doctor|Status|HelpJSON' -count=1`
- `gofmt -l` on touched files, `make lint`, `sh scripts/check-filesize.sh`, `sh scripts/check-comments.sh`.
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
- the exact new text of each rendered line, with an example;
- whether the help golden changed;
- the lines added to `doctor.go` and `status.go`;
- each gate command with its result;
- the five mutation results.

Commit everything, and leave the tree clean.
