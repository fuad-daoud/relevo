# Plan: every machine publishes its month's Turso traffic, so each one can read the account total

Issue #1361, slice A of three. Slice B renders the account total in `db sync status`, `relevo doctor` and the join guard. Slice C warns on idle exports. This slice adds no user-visible output.

Save this plan as `docs/plans/2026-10-27-sync-account-traffic.md` and commit it with the work, since plans ship in the same PR as their code.

## Why

`relevo db sync status` compares this machine's own push+pull bytes with the account-wide 10 GB quota. In October each machine read 60–91% while the account was at 222%. The per-machine counters (`sync.bytes.YYYY-MM`, `internal/sync/bytes.go`) live only in each machine's local file, and no other machine can see them. This slice publishes them into the shared Turso database and keeps a local snapshot of every machine's row.

## Behaviour

1. Each machine publishes one row into the remote `meta` table. The key is `traffic.<origin>` and the value is the JSON `{"month":"2026-10","push":N,"pull":N,"at":"<RFC3339Nano>"}`, giving this machine's Turso push and pull bytes for the current UTC month (read with `ReadBytes(r.Local, now)`). There is one row per origin, overwritten each publish. A new month simply overwrites the old month's row.
2. **The origin in the key is the worker's own origin from hello, never a request field.** This is the same rule as marks (`internal/syncworker/marks.go:33-37`): a caller must not be able to write another machine's row.
3. **No log-format bump.** Old builds read `meta` only by exact key or by their own prefixes (`floor.`, `signed_from.`, `epoch_cut.`, `key_wrapped.`, `claim:`, the format key; see `internal/syncworker/keymeta.go`, `keycut.go`, `claim.go`, `compact.go`, `format.go`). A `traffic.` row is therefore invisible to them, and a mixed-version fleet keeps syncing. Say this in a short comment by the prefix constant.
4. **The write is an UPDATE then an INSERT** in one transaction, followed by a push through `b.transfer(ctx, b.driver.Push)`. Do not use `INSERT OR REPLACE` or `ON CONFLICT`: `writeMark` (`marks.go:85-112`) documents that a sync connection can refuse an upsert onto an existing row, and this row is rewritten on every publish after the first.
5. **The publish answers with every origin's row** as this replica holds it, read after the write: `SELECT key, value FROM meta WHERE key LIKE 'traffic.%'`. Skip a row that does not parse rather than failing the read, as `readMarkRows` does (`marks.go:150-153`). A row parses when its origin is non-empty, `month` parses as `2006-01`, `push` and `pull` are ≥ 0, and `at` is RFC 3339.
6. **Cadence.** `trafficEvery = time.Hour`. A publish is due when the local marker `sync.traffic.published` is absent, cannot be parsed, or is at least an hour old. These are the same rules and the same reasons as `marksDue` (`internal/sync/steady.go:255-270`). It runs on every steady attempt **whether or not the import moved a mark**. The failure this issue is about is a machine that exports heavily with no import progress, and gating on `moved` (as `publishMarks` does) would never publish its traffic. Write the reason as a comment.
7. **The snapshot.** After a successful publish, the runner writes local kv `sync.account` = `{"read_at":"<now>","rows":{"<origin>":<row>,...}}` from the answer, then moves the marker. A publish that fails is logged with `slog.Warn` and never fails the attempt (the precedent is `publishMarks`, `steady.go:228-234`). On failure the marker and snapshot do not move, so the next attempt retries.
8. **A pure reader**, in `internal/sync`: `AccountTraffic(snap AccountSnapshot, own ByteCounters, origin string, now time.Time) Account`, where `Account{Month string; Total int64; Machines int; ReadAt time.Time}`.
   - `Month` is `now`'s UTC month.
   - This machine's share is `own.TursoPush + own.TursoPull`, taken from the live local counters the caller passes. Its row in the snapshot is ignored because the local counters are fresher.
   - Every other origin's row counts only when its `month == Month`. A row from an earlier month was billed in that month, so it must not count.
   - `Machines` counts this machine plus each other origin counted.
   - `ReadAt` is the snapshot's `read_at`.
   - An empty or absent snapshot gives this machine alone, with `Machines` 1.
   - Also add `ReadAccount(kv db.KV) (AccountSnapshot, error)`, where an absent key reads as the zero snapshot, like `ReadBytes`.

## Seams

Keep `synclog.Transport` unchanged. Twelve test fakes implement it (`grep -rln 'func (.*) PublishMarks(' --include='*_test.go' internal`), and adding a method would churn all of them. Add an **optional interface** instead, and say why in its comment.

- `internal/synclog/transport.go`: add a `TrafficRow{Month string; Push, Pull int64; At time.Time}` and `type TrafficPublisher interface { PublishTraffic(row TrafficRow) (map[string]TrafficRow, error) }`.
- `internal/synclog/memfake.go`: `MemTransport` implements it. It stores rows on the shared fake log keyed by `m.origin` (the same "own origin only" rule `PublishMarks` documents at `memfake.go:300-303`), and answers with every origin's row. Two `MemTransport`s on one log see each other's rows.
- `internal/syncworker/`, in a **new file `traffic.go`**. `protocol.go` is at 586 lines, and its cap is 600.
  - The file holds `const VerbPublishTraffic Verb = "publish_traffic"`, the wire type `TrafficRow` with JSON tags `month`, `push`, `pull` and `at`, the `trafficPrefix`, and `(b *TursoBackend) PublishTraffic(row TrafficRow) (map[string]TrafficRow, error)`.
  - `PublishTraffic` runs `ready()`, validates the row (a bad month or a negative count is `ErrProtocol`), writes, pushes and reads. It reuses `markDriverRefusal` and `markTransferRefusal`.
  - It also holds the handler `(s *server) publishTraffic(req Request) Response`. A missing `req.Traffic` is `ErrProtocol`. The backend is reached through an optional `trafficBackend` interface asserted on `s.backend`, so the worker's test fakes of `Backend` (`worker.go:77`) stay unchanged. A backend without it refuses with a clear error.
  - In `protocol.go`, add only `Traffic *TrafficRow` to `Request` and `Traffic map[string]TrafficRow` to `Response`, one comment line each.
  - In `worker.go:182`, add the dispatch entry.
- `internal/syncpipe/`:
  - `client.go` `callBound`: add `VerbPublishTraffic` to the data bound (it pushes).
  - `convert.go`: add `(c *Client) PublishTraffic`, converting between the synclog and wire types the way the marks conversions do.
  - `supervisor.go`: add `(s *Supervisor) PublishTraffic`, following `Supervisor.PublishMarks`'s exact shape.
- `internal/sync/stop.go`: `(s *StopTransport) PublishTraffic`. A stopped transport returns `ErrStopped`. Otherwise it asserts `s.inner.(synclog.TrafficPublisher)`, and when that fails it returns a sentinel `errTrafficUnsupported`. The runner treats the sentinel as a silent skip (debug log), not a warning.
- `internal/sync/`, in a **new file `traffic.go`**: the keys `KeyTrafficPublished = "sync.traffic.published"` and `KeyAccount = "sync.account"`, `trafficEvery`, `AccountSnapshot`, `Account`, `ReadAccount`, `AccountTraffic`, `(r *Runner) trafficDue(now)` and `(r *Runner) publishTraffic(t *StopTransport, now time.Time)`.
- `internal/sync/steady.go` `exchange` (`:166-170`): one call, `r.publishTraffic(t, now)`, placed after `now := time.Now().UTC()` and after `r.publishMarks(...)`, so the row is published after this attempt's import has pulled the others. `steady.go` is at 511 lines, so add nothing else there.

Declared file set: the files above, their `_test.go` siblings, and the plan doc. Anything else is out of scope; if you find you need it, say so in the report.

## Cases (tests to add)

- `internal/syncworker/traffic_test.go`, built on the real local backend the marks tests use (copy the setup from `TestPublishMarksWritesOnlyOwnRows`, `marks_test.go:69`):
  - The publish writes `traffic.<own origin>` and no other key.
  - A second publish overwrites the row (the UPDATE path).
  - The answer includes a row for another origin that the test inserted into `meta` directly.
  - A malformed row (bad JSON, bad month, negative push) is skipped, and the other rows still come back.
  - An invalid month in the request is refused.
  - The verb is refused without hello, as `TestPublishMarksRefusedWithoutHello` does for marks.
- `internal/syncpipe`: `callBound` knows the new verb. If an existing test walks every verb, make sure it covers this one.
- `internal/synclog`: two `MemTransport`s on one log see each other's traffic rows, and each writes only its own.
- `internal/sync/traffic_test.go`:
  - The due rule: absent, less than an hour, at least an hour, and an unparseable marker.
  - A successful publish writes the snapshot and the marker.
  - A failing publish moves neither, and the attempt's result is unchanged.
  - A transport without the optional interface is skipped without a warning.
  - `AccountTraffic`: this machine's figure comes from the live counters, not its snapshot row. A previous-month row is excluded. An empty snapshot gives this machine alone. Three origins sum correctly.
- An attempt through `MemTransport` whose import moves no mark still publishes traffic (the zen case). Put it in a `steady` test or in `traffic_test.go`.

Mutation checks, each re-run and reported with the failing test's name:
1. Drop the month filter in `AccountTraffic`.
2. Make `trafficDue` ignore the hour.
3. Gate `publishTraffic` on the import having moved a mark.
4. Write the key from a constant instead of the worker's origin.

## Gate

- `go test ./internal/syncworker/ ./internal/syncpipe/ ./internal/synclog/ ./internal/sync/ -count=1`
- `gofmt -l` on touched files, `make lint`, `sh scripts/check-filesize.sh`, `sh scripts/check-comments.sh`.
- Do NOT run `make check` or `make check-affected`.

## Style

Follow CLAUDE.md "Code style":
- A comment says *why*, and only where the code cannot.
- No issue numbers, "slice A" or other history in code or tests.
- Functions are at most 70 lines and files at most 600.
- A test's name says what it pins.

## Report

Include:
- the files changed against the declared set (`git diff --stat`);
- each gate command with its result;
- the four mutation results;
- the line counts of `protocol.go`, `steady.go`, `memfake.go` and `convert.go` after the change;
- any deviation, with its reason.

Commit everything, and leave the tree clean.
