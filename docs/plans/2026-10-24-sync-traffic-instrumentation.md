# Scope of THIS round (MasterMind override; it wins over anything below)

Do only steps 1, 3 and 4 of section 6: the proven-vs-hypothesis note, the per-attempt and per-table counters with capped local markers, and the `db sync status --json` and line surface for them. Use the plan below for seams and design.

NOT in this round:
- **Step 2 / section 5 (#1361):** do not implement it. Instead, read the code and state in the report whether the stored sync credential is a database token or could call the Turso platform API. The MasterMind decides the route afterwards.
- **Step 5 (sandbox measurement):** the MasterMind runs this after the round, with a throwaway Turso database. Do not create sandboxes or touch any Turso database, and do not enable sync anywhere.
- **Step 6 / section 4 (fixes):** no change to cadence constants, pull behaviour or marks publishing. List any fix you would propose in the report.

Gate: `make check-affected`.

---

# Plan: explain the ~5x pull amplification (#1194) and fix the quota meter (#1361)

One builder round. Context: October on the live DB — per machine
push 0.93 / 2.7 / 0.11 GB vs pull 6.1 / 6.4 / 5.9 GB; ~18.4 GB pull vs
~3.7 GB push; Turso billed 23.47 GB of a 10 GB quota. Traffic is flat at
~110 MB/h (~37 MB/h/machine) day and night, round-driven or not, while
the remote `log` bodies are 2–11 MB/day. Sync is OFF on all three
machines, so no live measurement is possible; all measurement runs in a
sandbox against a throwaway Turso database. The meter that hid this is
#1361: each machine compares only its own traffic with the account quota.

## 1. Transfer paths proven from the code (all Turso bytes flow through these)

- Every steady attempt runs exactly one engine `Pull` when idle, more when
  backlogged: `Runner.SyncOnce` → `attempt` → `exchange` → `drainImport`
  (`internal/sync/steady.go:85-110,155-171,294-315`) calls
  `Importer.Import`, each run calling `TursoBackend.Pull`
  (`internal/syncworker/turso.go:278-291`), which calls
  `transfer(driver.Pull)` (`internal/syncworker/bytes.go:26-49`) around the
  engine call (`internal/syncworker/driver_turso.go:89-92`). There is no
  skip-if-unchanged: no remote change-id/revision is read before pulling.
  Each `drainImport` loop iteration is one more engine pull; an idle
  attempt is exactly one pull and zero pushes.
- Pushes happen per export batch: `Exporter.Export` loops `ExportBatch`,
  each batch is one `Append` → one engine push
  (`internal/syncworker/turso.go:132-159`). A busy outbox is many pushes
  per attempt, not one.
- `PublishMarks` always pushes when called
  (`internal/syncworker/marks.go:43-58`); the steady layer throttles it to
  at most one publish per 15 min and only after an import that moved a mark
  (`internal/sync/steady.go:226-253`, `marksEvery`).
- `Compact` pushes when it drops (`internal/syncworker/compact.go:28-67`);
  due weekly or on 10% log growth (`internal/synclog/compact.go:18-29`),
  and skipped unless the floor moved past the recorded one
  (`internal/sync/compact.go:50-56`).
- `ClaimIdentity` pulls on every call and pushes+pulls when it writes
  (`internal/syncworker/claim.go:28-51`), but the steady path calls it only
  on adopted machines (`internal/sync/adopt.go:209-227` early-returns when
  no adopted fact). Not a steady cost on normal machines.
- `ReadMarks`, `Stats`, `PullRange`, `Head` perform no engine transfer:
  `ReadMarks` is documented local-only
  (`internal/syncworker/marks.go:118-121`); `PullRange` reads the replica
  as it stands (`internal/syncworker/compact.go:124-133`). `countBytes`
  after every attempt calls only `Stats`
  (`internal/sync/bytes.go:113-129`).
- Schema create/upgrade pushes once (`internal/syncworker/format.go:156-213`).
- The engine `Pull`'s changed flag is discarded
  (`internal/syncworker/driver_turso.go:89-92` ignores the bool).
- Cadence: idle window 5 min cold / 30 s hot for 5 min after applied rows or
  fresh enable (`internal/relevo/sync_liveness.go:13-37,121-133`);
  seal/outbox triggers debounced to 15 s
  (`internal/relevo/sync_liveness.go:13-19`,
  `internal/relevo/sync_tick.go:80-109`). So an idle machine pulls ~12/h;
  a machine with a running round pulls up to ~120/h plus seal triggers.
- Byte totals come from engine counters differenced around each transfer
  (`internal/syncworker/bytes.go:26-49`,
  `internal/syncworker/driver_turso.go:107-130`), accumulated per worker
  life, folded monthly into the local marker
  (`internal/sync/bytes.go:65-129`).

## 2. Ranked likely causes of ~5x pull (proven mechanism vs hypothesis)

1. Unconditional engine pull per attempt + per-tick bookkeeping pages.
   PROVEN: every attempt pulls; pulls fetch all pages dirtied since that
   machine's last pull. HYPOTHESIS for magnitude: with 3 machines the
   steady writers (exports, 15-min mark publishes rewriting `marks.at`,
   `meta` floor/head rows, compactions) dirty full 4 KB billed pages on
   every tick, so each machine's next pull re-fetches the others' pages
   forever. At ~37 MB/h/machine on a 5-min window this is ~3 MB per idle
   pull — far above the old ~5 kB/empty-pull estimate from #1089 — so
   either an empty engine pull is itself expensive (protocol/handshake
   overhead, frame negotiation) or something dirties megabytes per 5 min.
   The October data (flat nights, payload 100–1000x smaller than traffic)
   fits constant page churn, not entry volume. First thing to measure.
2. Stale `marks.at` rewrite behavior contradicting the throttle. The issue
   notes all six `marks` rows had fresh `at`, which sits uneasily with the
   15-min throttle in `steady.go:226-253`. PROVEN throttle in current code;
   HYPOTHESIS: the observation predates it, or another path republishes
   (check `syncpipe`/`syncworker` callers of `publish_marks`, and whether
   `marksDue`'s marker read can fail open every attempt). Verify from code
   before touching.
3. Push-per-batch export shape. PROVEN: N batches = N pushes per attempt.
   Demoted for magnitude: traffic is flat when no rounds run, and idle
   attempts export nothing. Only in scope if the sandbox shows busy-machine
   pushes dominating.
4. Re-bootstrap / rebuild / re-pull storms. `RebuildWindow` is 24 h and
   corrupt-triggered rebuilds move files aside
   (`internal/sync/rebuild.go:117-155`); `Open` bootstraps only the replica
   file it owns (`internal/syncworker/driver_turso.go:58-81`). Nothing in
   code re-downloads per attempt. Lowest rank; verify by code reading plus
   a daemon-restart experiment in the sandbox, not by changing the path.
5. `Stats`/`ReadMarks`/`countBytes` costing network. Code says local;
   verify against the vendored `tursogo` source in the module cache that
   `Stats` performs no I/O. If it does, that plus `countBytes` per attempt
   is a second per-tick cost.

Explicitly NOT causes (no code path): WAL size (local files only, already
bounded at 64 MiB in `internal/syncworker/wal.go:10-31`); R2 bodies
(separate billing); `PullRange`/snapshot (no transfer).

## 3. Instrumentation (the round's core deliverable)

Per-attempt record: extend the worker's counters so one attempt reports
pull count, push count, bytes pulled/pushed. The per-transfer differencing
already exists (`bytes.go:26-49` in `syncworker`); add a per-attempt
boundary: snapshot the worker totals at `SyncOnce` start
(`internal/sync/steady.go:85-94`) and at end via the existing `countBytes`
hook (`internal/sync/bytes.go:113-129`), and store the delta with the
attempt. Keep it in the machine-local kv beside `KeyLastAttempt`
(`internal/sync/attempt.go:1-43`): a capped ring (e.g. last 20 attempts)
of {start, pulls, pushes, turso sent/recv, exported, applied}, plus
extend `Attempt` only if the status doc needs it. Cap the ring and never
let markers grow without bound (follow `maxDroppedReports` precedent,
`steady.go:411-455`).

Per-table month breakdown: tally exported/imported rows per `tbl` in the
exporter/importer (`internal/synclog/export.go:77-97`,
`internal/synclog/import.go`) as entries + body bytes, fold into the month
row beside `ByteCounters` (`internal/sync/bytes.go:19-63`) via
`recordStats`, and surface in `db sync status --json`.

Status surface: add attempt ring + per-table breakdown to
`dbSyncStatusDoc` (`cmd/relevo/db_sync_status.go:21-92`), populate in
`dbSyncStatusStorage` (`cmd/relevo/db_sync_r2.go:275-296`), render a short
form on the line (`dbSyncQuotasLine`, `db_sync_r2.go:311-328`, fed by
`FormatQuotas` in `internal/sync/logsize.go:142-156`). The numbers must let
one sandbox run against a throwaway Turso database show: bytes per empty
pull, pulls per hour idle vs busy, and per-table push share.

Sandbox measurement (no live data exists; sync is off everywhere): use
`scripts/relevo-sandbox.sh create <name>` + `shell` (never `make install`
a test build), 2–3 sandboxes sharing one throwaway Turso DB, one idle
run (hours) and one loaded run (round running), reading
`relevo db sync status --json` from each. Compare per-empty-pull bytes
against the #1089 5 kB estimate; that single number decides §4.

## 4. Fix in the same round only if proven and contained

Ship a fix in this round only if the sandbox proves one contained cause
(e.g. empty pulls cost megabytes each, or a marks/meta write fires every
attempt despite the throttle). Contained fixes, in preference order:
(a) skip the engine pull when the remote has not moved — only if `tursogo`
exposes a cheap revision/changed probe; never by caching staleness
locally; (b) lengthen `coldWindow` / `hotWindow` / `exportDebounce`
(`sync_liveness.go:13-37`) with the new per-attempt numbers justifying
the values; (c) stop rewriting `marks.at` (or the meta/head row) on
no-op publishes. Structural changes — push once per attempt instead of
per batch, new published counters, protocol changes — are explicitly OUT;
they go to the follow-up plan the report lists. If nothing is proven,
this round delivers instrumentation + the sandbox numbers + a ranked
follow-up plan, no behavior change.

## 5. #1361: account total on the quota line

Decide by one code fact first: whether the stored sync credential can call
the Turso platform API (`GET /v1/organizations/.../usage`). The daemon's
token is a database token; if it cannot authorize platform calls, the
platform-API route needs a new credential and does not fit this round.
- If the stored credential works: add an account-total fetch to the status
  verb path only (CLI/doctor reads it on demand; never the hot tick),
  implemented as a pure function over a faked HTTP client in
  `internal/sync` or `internal/relevo` (CI has no network; per repo rule a
  `cmd/relevo` test must not reach the network — test the pure function,
  not the subcommand). Status shows `this machine: X; account: Y of
  10.0 GB (Z%)`; `relevo doctor` warns past 80% and when the month's
  projection crosses quota.
- If it does not work: list as follow-up (publish per-origin month traffic
  to the shared log and sum it; note this adds writes, so it must be
  weighed against §2 cause 1).
Either way this round stops hiding the account total behind the local
total; full wording lives in the builder's report.

## 6. Ordered steps (one line each: deliverable + check)

1. Confirm the transfer inventory (§1) against current code and the
   vendored `tursogo` `Stats`/`Pull` behavior; deliverable: a
   proven-vs-hypothesis note per §2 item. Check: every claim cites a
   function and line range; no claim rests on the issue text alone.
2. Resolve the #1361 credential question (stored token vs platform API);
   deliverable: platform route or published-sums follow-up. Check: a
   faked-client test proving which credential the code holds, no network
   in tests.
3. Implement per-attempt + per-table counters with capped local markers;
   deliverable: counters flowing from worker to month row. Check: focused
   `go test` on `internal/sync`, `internal/syncworker`, `internal/synclog`
   packages touched, all green.
4. Wire the attempt ring + per-table breakdown into `db sync status --json`
   and the line renderer; deliverable: new fields visible in the doc and
   line goldens updated. Check: `cmd/relevo` status tests green, goldens
   match.
5. Sandbox run vs throwaway Turso DB (2–3 sandboxes, idle + loaded);
   deliverable: per-empty-pull bytes, pulls/hour, per-table push share.
   Check: numbers from `status --json` on each sandbox, quoted in the
   report.
6. Apply a §4 fix only if step 5 proves a contained cause; otherwise write
   the follow-up plan. Check: `make check-affected`; a mutation probe
   (break the fixed condition, watch the named test fail) for any fix;
   `RELEVO_REQUIRE_COVERAGE=1` comparison for touched packages, regenerating
   the baseline with `sh scripts/check-coverage.sh --write` only if code
   moved packages, and never lowering a baseline to green.

## 7. Deleted behavior (closed list)

1. None. This round adds measurement and at most a contained traffic fix;
   no flag, verb, marker, or schedule is removed. If step 6 lengthens a
   window constant, the old value is a tuned default, not a deletion, and
   the report names the old and new values.

## 8. Tests and gates

- Pure functions / fakes only; no network in CI. Any CLI-touching test goes
  in `internal/sync` or `internal/relevo` as a pure function, never as a
  subcommand run from `cmd/relevo`.
- Gate: `make check-affected` (gofmt, vet, tidy, affected tests). `make
  check`/`make e2e` belong to the relevo/CI gate, not this round.
- Style: package comment 1–3 lines, comments say why, no history/issue
  numbers in code, functions ≤ 70 lines, no new lint exclusions, no
  lowered coverage baselines.

## 9. Report must include

Per-empty-pull bytes and pulls/hour idle vs loaded from the sandbox;
which §2 cause won, with the code + numbers behind it; what was fixed or
why nothing was (with the follow-up plan named); the #1361 route taken
and the exact new quota-line wording; `git diff --stat` vs this plan's
scope; `make check-affected` result and any coverage-baseline action.

