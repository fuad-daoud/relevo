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

## PLAN 2 — machine-local overlay for the per-tick fields

Do only this plan.

Goal: `runner.stream_offset`, the progress sampling clocks, and the whole
`runner.remote_live` leave the synced row. They live in machine-local storage
keyed by binding, are stripped from the synced `record_json` on save, and are
merged back on load, so callers keep using `store.Binding` as today. A change
to only these fields rewrites no `binding_record` and queues no outbox row.

### Home decision (settled here, builder does not re-decide)

Machine-local kv JSON blob via `LocalOrSelf`, one key per binding (precedent:
`internal/store/daemoninfo.go:40-100` with `d.LocalOrSelf().KVPut/KVGet`).
Not a new table, because: (a) no migration and no schema work on either file;
(b) both files run the whole migration series, so a new shared-schema table
would inherit outbox triggers in BOTH files and need trigger surgery, while kv
provably has no outbox triggers; (c) access is single-key read/write per
binding — nothing needs querying. The store handle already carries the local
file (`internal/store/db.go:13-60` — `OpenSplit`, bindings stay on shared,
machine-local surface binds through the same handle). Builder verifies the one
open question: the sync export reads only the shared handle's outbox
(`internal/db/sync_exchange.go:376` and its callers), so local-kv writes are
never exported.

### Field audit (confirmed; builder extends, never narrows, this list)

Strip (move per tick / per sample while a round runs): `Builder.StreamOffset`
(`internal/relevo/headless.go:487-515` `drainStream`, every tick while
streaming); `Progress.SampledAt` (`internal/relevo/progress.go:93`, every
`ProgressInterval`, default 30 s per `internal/policy/policy.go:235`);
`Progress.CPUTicks`/`CPUTicksAt` (`stepCPU`, `:140-149`, every readable
sample); `Progress.OutputAt` (`:100-102`, every sample while the stream
grows — a per-sample clock, not a human-read label); the whole
`Builder.RemoteLive` (`internal/relevo/remote_sync.go:275`, every running
poll). Audit before coding: confirm from the code whether `Builder.RemoteQueue`
(`Queue.Since`), `Builder.RemoteStatus`, `StalledSince`, and `TreeAt` ever take
a NEW value on a no-op tick (assignment of an identical value is harmless —
the stripped compare stays equal); strip every field that does, keep `Tree`
(fingerprint changes only on real edits), the stall/explore labels
(transitions only), and `StreamRound`/`StreamStart`/`StreamSegments`/
`StreamSessionID` (move on start/switch only — but if the audit shows any of
them moving per tick, it joins the overlay). Rule: a no-op tick must leave the
stripped JSON byte-identical.

### Cross-machine readers (audit before coding)

Other machines receive this binding's row through sync. After this plan their copy has no stream cursor, progress clocks or `remote_live`.
List every reader of those fields that can run on a binding row written by another origin (status, ui/dash, chain views, serve views, `relevo show`, statusline).
A display that goes stale on a peer is acceptable: name it in the report. If any reader *decides* something from a peer's copy (halts, stalls, switches, delivery), stop and report it instead of coding around it.

### Seams

- Strip choke point: `prepareSave` (`internal/store/lifecycle.go:192-258`) —
  every `binding_record` write flows through it (`save` `:155`,
  `saveWithLog` `:264`, `chain.go:134`); builder confirms with
  `grep -rn RecordPut internal/store` that no writer bypasses it.
- Merge choke points: `load` (`:385`), `list`/`decodeRecords` (`:435ff`,
  also serving `ListForMasterMind`).
- Same-value gates to convert from `SameBinding` to the stripped comparison
  (name each in the diff): `internal/relevo/daemon.go:485`,
  `internal/relevo/reconcile.go:89`, `internal/relevo/remote_sync.go:424`,
  `internal/relevo/remote_sync.go:488`, `internal/relevo/stop.go:148`.
- Overlay lifecycle: delete the key on binding archive/delete
  (`internal/store/lifecycle.go` remove/archive paths); seed-from-synced when
  no overlay exists (old rows, fresh machines); overlay wins when present.

### Cases

1. Cursor-only tick (stream advanced, nothing else): shared row untouched, no
   outbox row; overlay holds the new cursor; next load resumes from it (no
   re-rendered lines).
2. Real transition (send/start/switch/halt/close) with a moved cursor: one
   shared rewrite as today, overlay current afterwards.
3. Downgraded binary on the same machine: reads no cursor (renders from 0,
   duplicating lines in that round's builder log — accepted), and its save
   writes cursor-zero JSON (one outbox row; the overlay re-merges on the next
   new-binary load).
4. Crash between overlay write and shared save: same window as today's
   cursor-in-shared-row crash — a tick re-renders at most the unflushed tail.
   Order writes overlay-first and state the order in a why-comment.
5. Binding deleted/archived and the name re-added: no stale cursor leaks
   across incarnations (key choice or delete path must guarantee this).

### Steps

1. Deliverable: `internal/store/live_overlay.go` with the overlay type (only
   the audited fields), strip helper (stripped `Binding` + overlay), merge
   helper, and kv key scheme. Works when: a unit test round-trips a binding
   with every overlay field set — stripped JSON contains none of the stripped
   keys, merge restores the binding exactly.
2. Deliverable: `prepareSave` strips via step 1; `load`/`list` merge via
   step 1. Works when: `Save` then `Load` returns the full binding, while the
   stored `record_json` holds no stripped key (assert on the raw JSON).
3. Deliverable: write-through + gate conversion. New
   `Tx.SaveWithOverlay`-shaped path (builder's name): persists the overlay to local kv under the store lock only when the overlay differs from the stored one (a no-op tick writes nothing, local or shared), `RecordPut`s only when the
   stripped JSON differs, reports whether the shared row changed; `Save` keeps
   its signature as a wrapper; the five gate sites use the changed flag for
   their save/emit/count decisions so a cursor-only tick emits nothing.
   Works when: existing daemon/remote/stop tests pass unmodified in behavior
   (only the comparison narrowed).
4. Deliverable: store-level test pinning case 1: truncate outbox, `Save` a
   binding, then N saves advancing ONLY overlay fields — zero new
   `sync_outbox` rows for `binding_record`; one real-field save queues
   exactly one. Works when: green; and with the strip call reverted it fails
   with N rows (this is the red-on-main stand-in — plus step 5).
5. Deliverable: red-on-main proof for the regression value: temporarily restore the base files (see "No git stash") for steps
   1-3, run the step-4 test, observe N queued rows, pop, re-run green. Works
   when: failure output shows the per-save `binding_record` rows.
6. Deliverable: mutation check. Break the strip (return the binding
   unstripped) — the step-4 test must fail; break the merge (skip overlay on
   load) — the step-1 round-trip test must fail. Revert both. Works when:
   both observed failures name the respective tests.
7. Deliverable: gates + commit. `make check-affected`, then commit alone.
   Works when: clean gate, `git diff --stat` inside scope.

Scope (may touch): `internal/store/live_overlay.go` (new),
`internal/store/lifecycle.go` (prepareSave/load/list/remove/archive +
write-through only), the five gate call sites (comparison only),
`internal/store/*_test.go`, `internal/relevo/*_test.go` (only if a narrowed
comparison changes an expectation — justify each). Must NOT touch: migrations,
`BindingFormat`, sync exchange, ingest, the reconcile/progress/remote field
writers themselves.

Report must include: the final stripped field list with the audit evidence for
each suspect, the kv key scheme and incarnation-safety argument, the export
drain verification (`sync_exchange.go`), red-on-main and mutation outputs.

### No git stash

Never run `git stash` in this worktree: every worktree shares one stash stack, and relevo's git refuses it. Where a step below says to stash, do this instead: commit your work, run `git checkout origin/main -- <the non-test files you changed>` to get the base versions, run the test and record its failure, then `git checkout HEAD -- <the same files>` to restore yours. Confirm with `git status` that the tree is clean again.
