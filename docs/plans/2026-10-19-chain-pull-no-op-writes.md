# Plan: chain pull writes nothing when nothing changed, and a server-stopped chain settles (#1293)

Scope: `internal/relevo` only. No `internal/store` / `internal/db` production change, no CLI change, no `ChainStop` change.

## Behaviour and cases

1. **A pull that changes nothing writes nothing.** A repeated pull of an unchanged (terminal or running) server view performs zero `binding_record` writes, zero `chains` writes, and therefore zero downstream `mastermind` writes: the sync outbox gains no entries and no `updated_at` column moves. A pull that does change something (a newly closed round, a changed view field, a resumed view) still writes exactly what it does today.
2. **Keep pulling terminal mirrors.** `chainPullServers` keeps its current filter (skip only `done` and gone mirrors): one GET per tick per terminal chain is cheap, installable rounds still land after a settle, and a server resume re-arms the mirror through the existing running-view arm.
3. **A server-stopped chain settles even when a member can never catch up.** A `stopped` view moves a `running` mirror to `stopped` and queues exactly one end delivery, without waiting for `chainAllCaughtUp`. Rounds that can still be installed keep installing on later passes (the pull still runs). `halted` and `done` views keep the caught-up guard (deliberate non-goal; halted can still spam nothing after fix 1, at one cheap GET per tick).

## Root causes, with file:line evidence

**Write spam (5 rows/tick).** `chainPullMember` ends every pass with an unconditional `tx.Save(cur)` (`internal/relevo/chain_pull.go:359`), and `chainPullFinish` with an unconditional `tx.ChainPut(row)` (`chain_pull.go:408`). `prepareSave` re-stamps `UpdatedAt` from the wall clock on every call (`internal/store/lifecycle.go:221-225`), so the marshalled record always differs. `RecordPut` then always runs its UPDATE (`internal/db/record.go:292-299`), firing `sync_outbox_binding_record_upd` (`internal/db/migrations/022_sync_outbox.sql:47-48`); `ChainPut`/`updateChain` likewise always UPDATEs (`internal/db/chain.go:318-330`), firing `sync_outbox_chains_upd` (`022_sync_outbox.sql:93-94`). The changed record JSON changes the ingest `Revision` digest (`internal/store/revision.go:46-48`), so the daemon's `ingestLiveBindings` no longer skips the binding (`internal/relevo/daemon.go:738-745` runs `ingest.Ingest` at :752), and `upsertMasterMind` bumps `last_seen` unconditionally (`internal/ingest/ingest_tx.go:40-74` via `internal/db/write.go:77-81`), firing `sync_outbox_mastermind_upd` (`022_sync_outbox.sql:52-53`). Total: 3 member rows + chain row + MasterMind row per tick. Precedent that UPDATEs fire triggers even when nothing changes, and the guard for it, is `updateIfChanged` (`internal/db/write.go:177-195`) used by `UpsertBinding`/`UpsertRound` — the fix follows the same compare-before-write shape, but at the pull call sites, so no `store`/`db` change is needed: skipping `Save`/`ChainPut` keeps `Revision` stable, which keeps ingest skipped, which keeps the MasterMind row untouched.

**Stuck `running`.** `chainPullFinish` moves running to terminal only when `chainAllCaughtUp` holds (`chain_pull.go:400-407`); the guard requires every view-named member's mirror round past the server's `ClosedRound` (`chain_pull.go:436-450`, strict `<=` at :446). `chainPullMember`'s fetch loop breaks at the first round it cannot fetch (`chain_pull.go:271-287`: fetch failure at :276-280, prompt failure at :281-285, the latter silent on 404 per `chain_pull_rounds.go:58-73`), leaving the member pinned at or below `ClosedRound` forever when a round is deterministically unfetchable. The server's `ClosedRound` comes from `Serve.ClosedRound` (`internal/relevo/served.go:143-148`), advanced only by `closeServedRound` (`served.go:62-109`); a chain stop that finds no open member round ends the chain directly without closing any round (`internal/relevo/chain_verbs.go:150-155` into `chainStopWorkflowDirect` at :183-213). That is the `resync-v0182` shape: stopped on the server while the review member was at round 0 (stopped, never closed, so its `ClosedRound` never advanced), with another member's closed round permanently out of the mirror's reach — the guard then never holds and the mirror stays `running` while rewriting its row every tick.

**Decision retained (seed §2): keep pulling terminal mirrors, no skip.** One GET per tick is cheap; the resume path re-arms the pull because the running-view arm (`chain_pull.go:401-402`) flips the mirror back to `running`; keeping the pull running is also what lets installable rounds land after a stopped settle.

## Seams

- `internal/relevo/chain_pull.go:24-46` walk filter (unchanged); `:52-73` one-chain pass order (members install before finish — relied on by fix 3); `:299-360` member install tx with unconditional save at `:359`; `:374-379` post-lock ack (unchanged); `:388-431` finish with guard at `:400-407`, write at `:408`, delivery edge at `:414-429`; `:436-460` caught-up/terminal helpers.
- `internal/relevo/chain_pull_rounds.go:58-100` prompt fetch/entry; `chain_pull_queue.go:21-42` queue entry (already idempotent); `chain_pull_trace.go:21-47` trace copy (already skips held seqs).
- `internal/store/lifecycle.go:192-259` stamping writer; `internal/db/record.go:279-309`, `internal/db/chain.go:292-330` unconditional writers; `internal/db/write.go:177-195` no-op guard precedent; `internal/db/migrations/022_sync_outbox.sql:39-98` triggers.
- `internal/store/revision.go:29-54`, `internal/relevo/daemon.go:732-767`, `internal/ingest/ingest_tx.go:40-74`, `internal/db/write.go:59-97` MasterMind cascade (read-only for this plan).
- `internal/relevo/served.go:28-55,62-109,143-187`, `internal/relevo/chain_served.go:384-463`, `internal/relevo/stop.go:192-200,264-297`, `internal/relevo/chain_verbs.go:61-83,129-213`, `internal/relevo/remote_catchup.go:239-306`, `internal/relevo/remotefetch.go:370-417` server-side facts and fetch-failure arms.
- Tests to follow: `chain_pull_test.go:26-129` fake harness (`chainPullRuntime`, `chainPullView*`, `pullRounds`), `:478-587` delivery-guard arms, `:678-811` gone/done arms; `chain_pull_queue_test.go:156-181` re-pull idempotence idiom; `sync_outbox_test.go:23-62` raw-outbox counting pattern (`db.OpenRaw` on the test store).

## Steps

1. Add a failing repro test: server-`stopped` view with one member whose closed-round fetch fails deterministically, pulled twice — deliverable: mirror stays `running` AND the outbox grows on the second pass; check with `go test ./internal/relevo/ -run '<repro name>' -count=1` failing as described.
2. Compare-before-`Save` in `chainPullMember` (`chain_pull.go:299-360`): skip `tx.Save(cur)` when the post-pass binding differs from the loaded one in no field other than `UpdatedAt` — deliverable: unchanged members write no `binding_record` rows on a re-pull; check via outbox count per table (`binding_record` flat) in the new test.
3. Compare-before-`ChainPut` in `chainPullFinish` (`chain_pull.go:388-431`): skip `tx.ChainPut(row)` when the view-built row differs from the stored one in no field other than `UpdatedAt` — deliverable: unchanged chain row keeps its `updated_at` and emits no `chains` outbox entry; check in the new test.
4. Assert the MasterMind cascade needs no production change: the no-op test counts `mastermind` outbox rows too and expects zero without touching `store`/`db` — deliverable: green assertion plus a comment-free causal note in the report (Revision stable → ingest skips at `daemon.go:743-745` → no `last_seen` bump).
5. Settle-on-stopped in `chainPullFinish` (`chain_pull.go:400-407`): a `stopped` view moves a `running` mirror to `stopped` regardless of `chainAllCaughtUp`, keeping the running→terminal delivery edge (`:414-429`) so exactly one end delivery queues — deliverable: the step-1 repro now settles with one delivery and a silent second pass; check with the focused test command.
6. Lock resume re-arm: halted/stopped mirror plus `running` view returns to `running` (existing arm at `chain_pull_test.go:526-555` kept, extended if thin) — deliverable: passing resume test; check with the focused test command.
7. Update the two stopped-view arms that encode the old rule (`chain_pull_test.go:481-499` behind-queues-none; `:557-587` level-is-not-caught-up) to settle-with-one-delivery, asserting installable rounds still land; add a halted-behind-stays-running test locking the kept guard — deliverable: full `chain_pull*` suite green under the new rule.
8. Mutation-check every new/changed test (invert the comparison to always-equal and to always-different; restore the caught-up guard for `stopped`; drop the running-view arm): each named test must fail under its mutation — deliverable: a report line per test naming the broken condition and the failure.
9. Place new helpers so `chain_pull.go` stays at most 600 lines (538 today; new file if they do not fit), new functions at most 70 lines, no issue numbers or history in code or test names — deliverable: `make check-affected` green; check once more with the full gate at the end.
10. Run the focused suite while iterating (`go test ./internal/relevo/ -run 'TestChainPull' -count=1`), finish with `make check-affected` — deliverable: both commands reported green (no `cmd/relevo` test added, so the no-harness/no-network rule needs no new proof).
11. Save this plan verbatim at `docs/plans/2026-10-19-chain-pull-no-op-writes.md` and include it in the round's commit; commit message: `chain: a pull that changes nothing writes nothing (#1293)` — deliverable: the doc file plus the commit on a new commit (no amend/rebase of a remote-branch commit).

## Deleted behaviour

1. Unconditional per-tick member rewrites: `tx.Save(cur)` on an unchanged binding (three `binding_record` UPDATEs plus outbox entries per tick) — removed; a no-op member pass now writes nothing.
2. Unconditional per-tick chain rewrites: `tx.ChainPut(row)` on an unchanged row (one `chains` UPDATE plus outbox entry per tick) — removed; a no-op finish now writes nothing.
3. The downstream per-tick MasterMind `last_seen` bump and its outbox entry — removed as an effect of (1)+(2) via a stable `Revision`; no `store`/`db` code is deleted or changed for it.
4. "A `stopped` view with a member behind stays `running` with no delivery" — replaced by "settles to `stopped` with exactly one end delivery"; installable rounds still land on later passes because terminal mirrors keep being pulled.

## Report must include

- Outbox counts per table (`binding_record`, `chains`, `mastermind`) before/after a repeated pull, and `updated_at` stability where asserted.
- Each new/changed test named with what it pins, its mutation (exact condition broken) and the observed failure.
- The confirmed blocking member/round shape from the step-1 repro (which member could never catch up and which fetch arm broke).
- `make check-affected` output, the focused test command output, and confirmation that no file outside `internal/relevo` (plus the plan doc) changed.
- Coverage-baseline status (regenerated via `sh scripts/check-coverage.sh --write` only if code moved packages, else untouched).
- The plan-doc path and the commit hash; no amend/rebase performed.

## MasterMind addendum (applies to every step above)

- The no-op comparison ignores `UpdatedAt` only. Compare whole values, for example by zeroing `UpdatedAt` on both copies and then calling `reflect.DeepEqual`, rather than a hand-picked list of fields, so a field added later is never silently skipped. Pin the nil-versus-empty-slice case: `chainPullMember` sets `RemoteQueue`/`RemoteLive` to nil before the save, so a loaded record holding an empty slice must still count as unchanged, or the comparison must normalise it.
- `chainRowFromView` re-marshals `WorkflowJSON`/`StateJSON` on every pass when the server sends no workflow. Confirm that output is byte-stable across passes, so the chain-row comparison really finds "unchanged".
- The halted-view caught-up guard stays as it is. Say in the report what a halted mirror with an unfetchable round does after this change: it should stay `running` with zero writes per tick.
- The plan file goes at `docs/plans/2026-10-19-chain-pull-no-op-writes.md`, including this addendum.
