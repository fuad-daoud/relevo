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

## PLAN 1 — ingest stops stamping `mastermind.last_seen`

Do only this plan.

Goal: a mirror ingest run leaves `mastermind.last_seen` alone. The only
`last_seen` writer left is the throttled registry touch.

### Seams

- `internal/ingest/ingest_tx.go:40-74` `upsertMasterMind`: builds
  `db.MasterMind` with zero `LastSeen` on every mirror run.
- `internal/db/write.go:53-76` `UpsertMasterMind` / `upsertMasterMindByID`:
  zero `LastSeen` becomes `now`, then an unconditional `UPDATE`.
- `internal/db/migrations/022_sync_outbox.sql:49-57`: `AFTER UPDATE` triggers
  fire on ANY `UPDATE`, even one that changes nothing — so preserving the value
  is not enough; the statement must be skipped.
- Precedent for the skip: `updateIfChanged` (`internal/db/write.go:177-195`),
  whose comment already names this exact hazard, used by `UpsertBinding`
  (`:232`) and pinned by `TestUpsertOfAnUnchangedRowQueuesNothing`
  (`internal/db/upsert_unchanged_test.go`).
- The only other `last_seen` writer: `DBRegistry.touchIn`
  (`internal/mastermind/registry.go:401-414`), throttled by
  `seenRefreshInterval` (1 min), over kv namespace `mastermind/` which
  `splitLocalNamespaces` (`internal/db/split.go`) keeps in the machine-local
  file; kv has no outbox triggers (no `kv` in `022_sync_outbox.sql` /
  `026_shared_outbox_triggers.sql`). Callers are hook init
  (`internal/mastermind/hook.go:205`) and resolution refresh
  (`internal/mastermind/resolve.go:177`) — neither is on the daemon tick.
  Builder verifies with `grep -rn "\.Touch("` that no third caller exists.

### Cases

1. Mirror run, mastermind row already exists, kind/session unchanged: no
   `UPDATE` on `mastermind`, no `sync_outbox` row for it.
2. Mirror run for a never-seen session: one `INSERT` (with `now`, as today) —
   exactly one outbox row, as today.
3. Mirror run hitting the stale-id heal path (`ErrInvalid`, `:56-73`): follows
   the held id as today, and preserves THAT row's `last_seen` too.
4. Real registrations (`relevo mastermind init` and friends) still stamp
   `last_seen`; the registry `Touch` path is untouched.

### Steps

1. Deliverable: `upsertMasterMind` preserves the stored stamp. Read the
   existing row first (`MasterMindBySession`, as the heal path already does),
   pass its `LastSeen` through on the update arms, and route the update arms
   through `updateIfChanged`-style compare-then-skip so an unchanged row emits
   no `UPDATE`. Works when: case 1 emits no statement (observe with the test
   in step 2).
2. Deliverable: ingest-level test `TestIngestMirrorLeavesLastSeenAlone`
   (name pins behavior, no issue number): seed a mastermind row with a fixed
   old `last_seen`, truncate the outbox, run a mirror ingest twice, assert
   `last_seen` is byte-identical and zero new `sync_outbox` rows name
   `mastermind`; then a changed-kind/session mirror run asserts exactly one
   row. Model the double-run on `TestIngestTwiceIsNoop`
   (`internal/ingest/ingest_test.go:304`) and the outbox assertion on
   `outboxRows` (`internal/db/outbox_drift_test.go:33`). Works when: green on
   the fixed code.
3. Deliverable: red-on-main proof. temporarily restore the base files (see "No git stash") for the step-1 change only, run the
   step-2 test, observe it fail (mirror stamps `now`, outbox row queued),
   restore your files (see "No git stash"), re-run green. Works when: the failure output names the
   `last_seen` mismatch and the extra outbox row.
4. Deliverable: mutation check. Temporarily restore the zero-`LastSeen` call
   in `upsertMasterMind`; the step-2 test must fail; revert. Works when: the
   observed failure is the test from step 2, not a compile error.
5. Deliverable: gates + commit. `make check-affected`, then commit alone.
   Works when: gate output is clean and `git diff --stat` names only the
   ingest/db files in scope.

Scope (may touch): `internal/ingest/ingest_tx.go`,
`internal/ingest/*_test.go` (new/affected tests only), `internal/db/write.go`
(update arms of the two mastermind functions only). Must NOT touch: registry,
sync exchange, store, relevo tick, migrations (no schema change).

Report must include: the preserved-vs-stamped decision, the `grep .Touch(`
caller list, red-on-main output excerpt, mutation output excerpt.

### No git stash

Never run `git stash` in this worktree: every worktree shares one stash stack, and relevo's git refuses it. Where a step below says to stash, do this instead: commit your work, run `git checkout origin/main -- <the non-test files you changed>` to get the base versions, run the test and record its failure, then `git checkout HEAD -- <the same files>` to restore yours. Confirm with `git status` that the tree is clean again.
