# Issue #1365: synced writes during a running round — three chained plans

Measured (2026-10-24, env sandbox from main e901681, `gh issue view 1365 --comments`):
a local round rewrites `binding_record` almost every tick (`runner.stream_offset`
+ `updated_at`), a served round additionally rewrites the client's copy
(`runner.remote_live`) every poll, the ingest mirror stamps `mastermind.last_seen`
on every binding save (~13 writes in a 40 s round), and `progress.sampled_at`
moves every 30 s. The daemon's `store.SameBinding` gate compares whole JSON, so
any one moving field forces a save, and every save is a Turso log entry every
machine pulls (~30 KB/entry, ~8 GB/month for one served round 12 h/day).

Chain order (each plan is self-contained, starts "Do only this plan.", ends in
its own commit; do not stack them as one diff):

- PLAN 1: ingest stops stamping `mastermind.last_seen` on mirror runs.
- PLAN 2: machine-local overlay for the per-tick fields; synced row moves only
  on transitions.
- PLAN 3: per-round write-ceiling regression test, red on today's main.

Global constraints (all three plans): no `BindingFormat` bump unless unavoidable
(struct keeps every field with `omitempty`; absence decodes to zero, so no bump
is expected — justify it if one appears); test and code names never carry issue
numbers; functions stay ≤ 70 lines, non-test files ≤ 600; comments say why only.
Downgrade effect, accepted: a downgraded binary on the same machine re-reading
the stream from 0 re-renders from the start and duplicates lines in that round's
builder log; other machines never drive the process, so nothing else changes.
Verification gates: `make check-affected` for the touched packages after the
last green test run, full `make check` only if cheap; compare coverage with
`RELEVO_REQUIRE_COVERAGE=1` and regenerate the baseline with
`sh scripts/check-coverage.sh --write` only if code moved between packages, never
lowering a baseline to get green. CLI tests stay in `internal/*` (pure
functions); nothing in `cmd/relevo` may spawn a harness (no network on CI).
Each plan's report states the files changed, the tests run with observed
results, the mutation check with the failing test's observed output, and the
red-on-main evidence for its new tests.

## PLAN 3 — per-round write-ceiling regression test

Do only this plan. Depends on PLAN 2 (and PLAN 1 for the `mastermind` half);
do not re-implement either — if a ceiling fails because of a PLAN 1/2 gap,
halt and report rather than fixing it here.

Goal: one test pins a fixed ceiling of `sync_outbox` rows for
`binding_record` and `mastermind` per round, independent of how many lines the
builder streams. It must fail on today's main; the plan states how that is
shown.

### Seams

- Outbox counting: `SELECT ... FROM sync_outbox` filtered to
  `tbl IN ('binding_record','mastermind')`, pattern after `outboxRows`
  (`internal/db/outbox_drift_test.go:33`) and `TruncateOutbox`
  (`internal/db/outbox.go`); `UPDATE`-always-queues per the `022` triggers.
- Round driver: `tickUntilRoundCloses` + `relevo.NewDaemon(rt, ...)` tick loop
  (`internal/e2e/headless_test.go:999-1008`), fake `claude` on PATH, daemon
  tick bounded at 200 ms (`:155`). Stream volume knob: the fake harness writes
  the report — extend it to stream a configurable K lines before the marker so
  the test varies K while the ceiling stays fixed.
- Store-level equivalent (deterministic core): a `Store` under `t.TempDir()`
  with a real stream file, driving `drainStream`-worth of ticks
  (`internal/relevo/headless.go:487`) plus `progressStep` samples
  (`internal/relevo/progress.go:78`) through the PLAN 2 save path.

### Cases

1. A round streaming K lines (K large, e.g. hundreds across many ticks) queues
   no more `binding_record`/`mastermind` rows than the same round streaming
   ~0 lines. Streaming volume is not a variable in the count.
2. The ceiling is fixed and small: transitions only
   (add/send/start/close/done/deliver — builder enumerates the exact expected
   rows after the fix and pins the number with minimal headroom, one integer
   per table).
3. `mastermind` contributes zero rows during the round (PLAN 1 holds).
4. Control: the identical scenario with PLAN 1/2 reverted exceeds the ceiling
   (this is the "fails on main" proof: restore the base files as "No git stash" describes).

### Steps

1. Deliverable: store-level ceiling test (mandatory, deterministic):
   `TestRunningRoundQueuesNoSyncedWritePerTick`-shaped (behavior name, no
   issue number) in `internal/store` or `internal/relevo` (whichever owns the
   tick-driving helper after PLAN 2; pure function, no harness, no network):
   truncate outbox, open round, loop T ticks each advancing stream cursor +
   sampling clocks (+ a `RemoteLive` refresh on a remote-flavored binding if
   cheap), assert zero new rows for both tables; then close the round, assert
   the fixed small count. Works when: green and stable across 3 runs
   (`-count=3`).
2. Deliverable: e2e ceiling assertion (mandatory): extend the fake harness
   with a K-lines knob, run the headless round (`pushBinding` path,
   `headless_test.go:85-260` pattern), truncate outbox at round start, assert
   the per-table ceiling from case 2 at round end; run once with small K and
   once with large K, same ceiling. Works when: green within the existing
   bounded waits (`roundDeadline` 60 s); if the e2e leg proves flaky beyond
   the round's own stability budget, keep the store-level pin, delete the e2e
   leg, and say exactly which wait flaked with its observed output.
3. Deliverable: red-on-main proof: temporarily restore the base files (see "No git stash") for the PLAN 1+2 src changes (keep
   the tests), run both legs, observe ceiling breaches (per-tick
   `binding_record` rows; `mastermind` rows on every ingest), pop, re-run
   green. Works when: the failure output shows counts growing with K.
4. Deliverable: mutation check: re-break the strip and separately the
   ingest stamp (one at a time); the store leg must fail on the first, the
   `mastermind` count on the second. Works when: each observed failure names
   the leg's test.
5. Deliverable: gates + commit. `make check-affected`, then commit alone.
   Works when: clean gate, `git diff --stat` inside scope.

Scope (may touch): `internal/e2e/headless_test.go` + e2e fake-harness helper
(K-lines knob only), the new store/relevo-level test file, e2e-only test
helpers. Must NOT touch: non-test src (any src change here means PLAN 1/2
regressed — halt instead), `cmd/relevo`, migrations, coverage baseline
(regenerate only under the stated rule, never lower).

Report must include: the pinned ceiling integers with the enumerated
transition writes that justify them, small-K vs large-K observed counts,
red-on-main counts showing growth with K, mutation outputs, and any deleted
or skipped leg with its reason.

### No git stash

Never run `git stash` in this worktree: every worktree shares one stash stack, and relevo's git refuses it. Where a step below says to stash, do this instead: commit your work, run `git checkout origin/main -- <the non-test files you changed>` to get the base versions, run the test and record its failure, then `git checkout HEAD -- <the same files>` to restore yours. Confirm with `git status` that the tree is clean again.
