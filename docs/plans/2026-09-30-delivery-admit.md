# Plan: one report, one delivery (#739) and NEEDS YOU only for a human (#738)

Seed: `#739` + sibling `#738`, both read in full. Code read at `8127ecff` in the throwaway tree.

## Seed vs code

Every location the seed names is real. One correction: the state lock is **not taken inside `DeliverPending`** — the caller holds it (`deliverAndSettle` → `Reconcile` under `Store.WithLock`, `internal/relevo/reconcile.go:132,641-658`; also `headless.go`, `remote_sync.go`) and hands `tx` in. So the opencode POST already runs *inside* the store lock, and `show`'s read cannot interleave with it. The defect is narrower than the seed's "same critical section": the fact *a POST was admitted* lives only in `OpencodeDeliverer.posted` (`internal/delivery/deliver_opencode.go:44-45,50-72`), so after the 3 s confirm window closes the entry looks plain-pending again and the next `Pull` claims it. The fix is to make *admitted* a durable, lock-guarded fact on the entry. Nothing in the seed contradicts the code; no halt.

## Behaviour and the cases

A `to_mastermind` unconfirmed entry has three delivery states:

- **pending** — nobody pushed it. `show`/`wait`/cockpit may claim it (`ConfirmIndex`, route `show`/`wait`/`tui`); the deliverer may POST it.
- **admitted** — a push route accepted it into its own queue (opencode 2xx; agy "sent"). It is *not* confirmed: the session has not been read back. It is no longer claimable or printable by any reader, and the deliverer may only read back, never send again.
- **confirmed** — the read-back proved the session took it.

Cases to hold:

1. Claimed-then-push: entry confirmed (route `show`) before the deliverer tick → deliverer's `PendingForMasterMind` no longer sees it → no POST. (Holds today; the durable admit makes claim and admit share the lock.)
2. Admitted-then-read (the bug): POST admitted, confirm window closed → a later `show`/`wait`/cockpit does **not** claim and does **not** print it (`Pull`/`PullPendingThrough` skip admitted). The daemon confirms it when the session records it.
3. Admitted before a crash: `admitted_at` is in the DB, so the restarted daemon sees it, does not POST again, and only read-backs; no reader re-presents it.
4. Not admitted, past `FallbackAfter`: unchanged — route `pull`, `show`/`wait` may claim.
5. Admitted and never read back (silent 2xx to the wrong server): stays admitted-unconfirmed, never re-presented, never re-sent; visible as `admitted_at` in `relevo show <name> --log --json`. Accepted cost of exactly-once; the report states it.
6. #738: a pending payload on a **live** route is never `NEEDS YOU`, however old; a pull route or a non-live route is `NEEDS YOU` at once. `--peek` unchanged.

Decisions the prompt asked for:

1. **Marker** = `LogEntry.AdmittedAt *time.Time` (`json:"admitted_at,omitempty"`), written into the entry's authoritative JSON by a new `Tx.AdmitIndex` under the same `Store.WithLock` as `ConfirmIndex`. `admitted` is what makes claim and admit mutually exclusive: `Pull`/`PullPendingThrough`/`Drain` use a new *claimable* scan (`!Confirmed && AdmittedAt == nil`), and `DeliverPending` treats `AdmittedAt != nil` as "confirm only". No column and **no migration**: the JSON is already authoritative and the pending scans already decode it (`store/db.go:124-141`); a column would buy nothing a reader needs.
2. **`show` on a pending-but-admitted entry**: it prints the requested section/`--output` as today but claims nothing (`Pull` returns `found=false`); `--peek` unchanged. Same for `wait` and the cockpit's `tui` pull.
3. **#738 rule and fields**: `stalled := pending && (b.MasterMindRoute == "pull" || !b.MasterMindRouteLive)` — the elapsed-time clause goes. No new fields are needed; `Pending`, `MasterMindRoute`, `MasterMindRouteLive` already carry it. The statusline must **not** consume `admitted_at`: the route already says whether a human must act, and consuming the admit would re-couple the view to the delivery state machine.

## Seams

Store (`internal/store/log.go`, `types.go`):
- `LogEntry`: add `AdmittedAt *time.Time` next to `DeliveredAt`/`Confirmed`/`Route` (`log.go:70-128`).
- `Tx.AdmitIndex(name string, idx int) error` + `Store.AdmitIndex` wrapper — mirrors `confirmIndex` (`log.go:414-480`): read events, guard bounds and `ev.Confirmed`, patch the JSON map with `admitted_at` (marshal `time.Now().UTC()`), write `seq` through, call the new db write. Idempotent if already admitted.
- `Tx.ClaimableForMasterMind(name) (LogEntry, int, bool, error)` and `Tx.ClaimableForMasterMindThrough(name string, round int) ([]PendingEntry, error)` — the existing scanners (`log.go:382-412`) with `&& e.AdmittedAt == nil`. Keep `PendingForMasterMind`/`Through` unchanged (status, tests).

DB (`internal/db/record.go`):
- `Tx.EventAdmit(recordID string, seq int, newJSON string) error` + `*DB` wrapper — a single `UPDATE binding_event SET entry_json = ? WHERE record_id = ? AND seq = ?` (the admit lives only in the JSON; `confirmed`/`delivered_at`/`route` columns are untouched). `Successor to `EventConfirm` (`record.go:342-348,410-412`).

Delivery (`internal/delivery/`):
- `deliverer.go`: add `OutcomeAdmitted` (after `OutcomeUnavailable`) and a second method to `MasterMindDeliverer`: `Confirm(ctx, mastermind store.Endpoint, payload string, queuedAt time.Time) (Outcome, string, error)` — read-back only, never sends. Update the interface doc: after `OutcomeAdmitted` the caller must call `Confirm`, never `Deliver` again.
- `deliver.go` (`DeliverPending`, `:68-112`): read `pending` as today; in the deliverer branch, if `pending.AdmittedAt != nil` call `Confirm` (confirm on `OutcomeDelivered`, otherwise leave admitted); else call `Deliver`, and on `OutcomeAdmitted` call `tx.AdmitIndex` then `Confirm` (poll inside the lock, as today). Extract a helper `deliverViaDeliverer(...) (Delivery, error)` so both functions stay under the 70-line rule. Channel route and `OutcomeNotMine`/`OutcomeUnavailable` paths unchanged.
- `pull.go` (`:67-84`, `:109-128`): `PendingForMasterMind` → `ClaimableForMasterMind`, `PendingForMasterMindThrough` → `ClaimableForMasterMindThrough`.
- `drain.go:103-115` (`pendingFor`): same swap, so a channel never pushes an entry another route admitted.
- `deliver_opencode.go`: delete `postedMu`/`posted`/`opencodeKey`/`hasPosted`/`recordPosted` (`:44-72`) and the `hasPosted` guard (`:172-174`) and `recordPosted` (`:191`); after the 2xx (`:189`) return `OutcomeAdmitted, "posted; awaiting the session"` instead of calling `confirm`; add `Confirm` calling the existing `confirm` (guards: kind/Exec/session id, then `confirm(ctx, SessionID, firstPayloadLine(payload))`); `confirm`'s timeout and ctx-done returns become `OutcomeAdmitted` (the read error stays `OutcomeUnavailable`). Update the step-order doc comment (`:117-124`).
- `deliver_agy.go`: add `Confirm` (guards, then `inbox`; `read`→`OutcomeDelivered`, else the existing `confirm`); in `Deliver`, `state.sent` → `OutcomeAdmitted` (`:159-161`) and `confirm`'s timeout/ctx-done → `OutcomeAdmitted` (`:226-232`); after a successful `send-message` (`:196`) return `OutcomeAdmitted` instead of calling `confirm`.

View (`internal/view/statusline.go`):
- Delete `PendingNeedsYouAfter` (`:20-23`); `stalled` (`:423-430`) becomes `pending && (b.MasterMindRoute == "pull" || !b.MasterMindRouteLive)`, with the comment rewritten to say a live push stays non-alarming however long the session takes.

Wire/JSON: `admitted_at` appears in `relevo show --log --json` and `relevo bugreport`; `status --json`/`--line --json` and every wire/mirror shape are unchanged. `internal/harness/opencodeplugin/tui.tsx` needs **no change** — it renders `row.needs_you` (`:415,:856,:998`) from the fixed row.

## Ordered steps

1. **Store + db admit** — `LogEntry.AdmittedAt`, `Tx.AdmitIndex`/`Store.AdmitIndex`, `db.EventAdmit`, `ClaimableForMasterMind`(+Through). Verify: `go test ./internal/store ./internal/db`. Know it worked: `internal/store/log_test.go` gains `TestAdmitIndexMarksButDoesNotConfirm` (admitted_at present, `Confirmed` false, a later `ConfirmIndex` keeps the key) and `TestClaimableSkipsAdmitted`.
2. **Port** — `OutcomeAdmitted` + `Confirm` on the interface; update the three doubles in `internal/delivery/deliver_test.go` (`notMineDeliverer`, `deliveredDeliverer`, `stubDeliverer`). Verify: `go build ./...`, then `go test ./internal/delivery`.
3. **Opencode + agy deliverers** — drop the `posted` map, split `Deliver`/`Confirm`, `OutcomeAdmitted` on admit. Verify: `go test ./internal/delivery -run 'Opencode|Agy'`. Know it worked: `TestOpencodeDeliverSilentTwoHundred` now wants `OutcomeAdmitted`; the repeated-`Deliver` test is rewritten so one `Deliver` returns `OutcomeAdmitted` and subsequent `Confirm` calls POST zero more times; `TestOpencodeDeliverIdempotentSkipsPost` unchanged (zero POSTs).
4. **`DeliverPending` + readers** — the admit branch, the helper, `AdmitIndex` on `OutcomeAdmitted`; `pullPending`/`PullPendingThrough`/`drain.pendingFor` on the claimable scan. Verify: `go test ./internal/delivery`. Know it worked: new `internal/delivery/deliver_test.go` case `TestDeliverPendingAdmitsThenConfirmsWithoutResending` (one `Deliver` + `Admit`, `Pull` found=false, second tick one `Confirm` and no `Deliver`, then `Confirm`→`Delivered` confirms) and new `pull_test.go` case `TestPullPendingSkipsAdmitted`.
5. **`show` acceptance** — verify `go test ./internal/relevo -run Show`. Know it worked: `internal/relevo/show_test.go` gains `TestShowDoesNotClaimAdmittedPayload` (non-peek `show` reads the section, leaves the entry unconfirmed; replaying `delivery.Pull` with `"probe"` returns found=false).
6. **#738 row rule** — statusline.go edit. Verify: `go test ./internal/view`. Know it worked: `TestStatusLineRowsStalled` is replaced by `TestStatusLineRowsLiveRouteNeverStalls` (live deliverer, 61 s and 30 min, `NeedsYou` false, status `report in`); the two table cases "stalled reader artifact"/"stalled report" (`statusline_test.go:621-680`) now expect `artifact in`/`report in`; `TestRenderStatusLineSharesTheRowRule`'s fixture row `"stalled"` (`:1597-1606`) moves to `MasterMindRoute:"pull"`, `MasterMindRouteLive:false` so it still pins a genuinely stalled pending.
7. **Full gate** — `make check` (gofmt over tracked files, `go vet`, golangci-lint, comment/filesize scripts, `go mod tidy`, plugin-version/name checks, `shellcheck`, then `go test -race -count=1 -cover ./...` against `testdata/coverage-baseline.txt`). Because the deliver path is touched, also run `make e2e` (not part of `check`) and paste its tail.
8. **Mutation checks** (the report must carry each): make `claimableForMasterMind` ignore `AdmittedAt` → step 5 fails; reinstate the elapsed-time clause → step 6 fails; make `Confirm` POST → step 3 fails; drop the `AdmitIndex` call → step 4 fails. Restore each.

Focused loop while building: `go test ./internal/store ./internal/delivery ./internal/view ./internal/relevo`. Final: `make check`.

## Coverage

No code moves between packages, so the baseline is **not** regenerated. `internal/delivery` 86.2, `internal/store` 80.4, `internal/view` 86.7: if any drops more than a point, add tests for the new branches (`AdmitIndex`, `Confirm`, the admit branch), never lower the baseline.

## Deleted (closed list)

1. `OpencodeDeliverer.posted`, `postedMu`, `opencodeKey`, `hasPosted`, `recordPosted` (`internal/delivery/deliver_opencode.go:44-45,50-72`) — the in-memory admit map, replaced by the store marker.
2. The `hasPosted` guard (`:172-174`) and `recordPosted` (`:191`).
3. The reason string `"posted but not seen in the session"` (`:173,227,231`) — replaced by `OutcomeAdmitted`/`"posted; awaiting the session"`.
4. `PendingNeedsYouAfter` (`internal/view/statusline.go:20-23`) and the `now.Sub(...TS) > PendingNeedsYouAfter` clause (`:428`) — the 60 s timer.
5. `TestOpencodeDeliverPostsOncePerPayload`'s premise (`deliver_opencode_test.go:719-749`) is rewritten (not deleted); `TestStatusLineRowsStalled` (`statusline_test.go:1450-1482`) is rewritten as the inverted pin.

## Report must include

- `make check` output (tail) and `make e2e` tail; `git diff --stat` against this scope, with any file outside the list named and justified.
- The four mutation checks: what was broken, which named test failed, that it was restored.
- **#739 acceptance verbatim, with the two tests that pin each clause**: "a claimed entry must never be pushed later" → the claim-then-push test; "an admitted push must never be re-presented by a following read" → `TestShowDoesNotClaimAdmittedPayload` + `TestPullPendingSkipsAdmitted`.
- The crash window: a crash between the 2xx and the `AdmitIndex` write can still re-POST on restart (the pre-POST `seen` scan usually catches it); the write happens before the read-back poll to keep the window to microseconds. Say plainly that this residual is narrower than today's, not gone.
- The silent-200/stuck-admitted limitation and that it is visible via `admitted_at` in the log.
- That `status --json`/`--line --json` shapes are unchanged and the plugin needed no edit; that no migration was added and why.
- Coverage per package vs baseline, and confirmation no baseline was lowered.
