# Plan: a repeat tick reads an admitted push back once (#750)

Seed: #750 read in full. Option 1 is the decision: the admitting tick keeps the
deliverer's full confirm window; a tick that finds the entry already admitted does one
read-back and returns. Code read at `16314604` (main, after #752's merge).

## Seed vs code

Every location the seed names is on main:

- `deliverViaDeliverer`'s `pending.AdmittedAt != nil` branch calls `del.Confirm`
  (`internal/delivery/deliver.go:115-121`), and `Confirm` runs the deliverer's poll —
  opencode `OpencodeConfirmWindow` 3s at `OpencodeConfirmPoll` 250ms
  (`internal/delivery/deliver_opencode.go:174,205-232`), agy `confirmWindow`/`confirmPoll`
  (`internal/delivery/deliver_agy.go:206,244-263`) — inside the caller's `Store.WithLock`.
- The daemon ticks at 2s (`cmd/relevo/daemon.go`), so while an entry is
  admitted-unconfirmed every tick holds the state lock for the whole window.
- Each opencode poll spawns `sqlite3` twice (`tableSet` + `seen`'s query,
  `deliver_opencode.go:292,324`). No correction to the seed; no halt.

## Decision

One new interface method and one call-site swap. Options 2 (move the poll outside the
lock) and 3 (per-entry backoff) are out: the tick already is the retry granularity.

- `MasterMindDeliverer.ConfirmOnce`: exactly one read-back. It never polls and never
  sends. After `OutcomeAdmitted` the admitting tick calls `Confirm` (unchanged); every
  later tick calls `ConfirmOnce`.
- The admitted branch in `deliverViaDeliverer` calls `ConfirmOnce`.

## Behaviour

- Tick that admits: `Deliver` -> `AdmitIndex` -> `Confirm` (full window), as today.
- Every later tick: one read-back under the lock; `OutcomeDelivered` confirms the entry,
  anything else leaves it admitted. No poll, so the lock is held for one read-back, not
  for the window.
- Restart: an admitted entry on a fresh daemon behaves like any later tick.
- Exactly-once unchanged: `ConfirmOnce` cannot POST/send, and `Pull`/
  `PullPendingThrough`/`drain` still skip admitted entries (`pull.go`/`drain.go`
  untouched).

## Seams

`internal/delivery/deliverer.go`:
- Add to the interface, documented with the call rule above:
  `ConfirmOnce(ctx context.Context, mastermind store.Endpoint, payload string, queuedAt time.Time) (Outcome, string, error)`.

`internal/delivery/deliver.go` (`deliverViaDeliverer`, :115-121):
- `del.Confirm` -> `del.ConfirmOnce` in the admitted branch only. Nothing else changes.

`internal/delivery/deliver_opencode.go`:
- `ConfirmOnce`: the guards `Confirm` uses (kind, Exec, session id), then ONE
  `d.seen(ctx, session, firstPayloadLine(payload))`:
  - seen -> `OutcomeDelivered, ""`;
  - unseen -> `OutcomeAdmitted, "posted; awaiting the session"`;
  - read error -> `OutcomeUnavailable, "sqlite3: " + firstErrorLine(err)`.
- Keep `Confirm` and the private `confirm` poll as they are. Extract the shared guard
  block into one helper only if needed to hold the 70-line rule.

`internal/delivery/deliver_agy.go`:
- `ConfirmOnce`: the guards `Confirm` uses, then ONE `d.inbox(conv, origin, queuedAt)`:
  `state.read` -> `OutcomeDelivered, "already present"`; otherwise
  `OutcomeAdmitted, "sent to agy but not yet read"`. Never sends.

## Ordered steps

1. **Interface + doubles.** Add `ConfirmOnce` to `MasterMindDeliverer`; fix every
   implementation the compiler names (`notMineDeliverer`, `deliveredDeliverer`,
   `stubDeliverer` in `internal/delivery/deliver_test.go:88,196,305`). Verify:
   `go build ./...`.
2. **Deliverers.** Opencode and agy `ConfirmOnce`. Verify:
   `go test ./internal/delivery -run 'Opencode|Agy'`.
3. **Call site.** `deliver.go` admitted branch -> `ConfirmOnce`. Verify:
   `go build ./... && go test ./internal/delivery`.
4. **Tests** (each names its pin):
   - Update `TestDeliverPendingAdmitsThenConfirmsWithoutResending`
     (`deliver_test.go:210`): tick 2 asserts `stub.calls == 1`, `confirmCalls == 1`,
     `onceCalls == 1`; the confirming tick scripts `onceOutcome: OutcomeDelivered` and
     must confirm the entry. Mutation (b) below must fail this test.
   - New `TestOpencodeConfirmOnceNeverPolls`: `fakeSqliteExec{seenFrom: 5}` ->
     `OutcomeAdmitted` with fewer than 5 exec calls (one read-back; a poll would reach
     5 and deliver).
   - New `TestOpencodeConfirmOnceSeen`: `fakeSqliteExec{seenFrom: 1}` ->
     `OutcomeDelivered`.
   - New `TestAgyConfirmOnceAdmitsThenDelivers`: an unread message in the inbox ->
     `OutcomeAdmitted` and zero sends; after `markAgyRead` -> `OutcomeDelivered,
     "already present"`.
   - The #739 pins stay green unchanged: `TestOpencodeDeliverPostsOnce`,
     `TestDeliverPendingAdmitsThenConfirmsWithoutResending`,
     `TestPullPendingSkipsAdmitted`, `TestShowDoesNotClaimAdmittedPayload`.
5. **Gate.** `make check`. The delivery seam is touched, so also `make e2e` and paste
   its tail.
6. **Mutation checks, each restored; the report carries the list.**
   (a) `ConfirmOnce` reusing the poll (`d.confirm(...)`) ->
   `TestOpencodeConfirmOnceNeverPolls` fails;
   (b) the admitted branch calling `Confirm` ->
   `TestDeliverPendingAdmitsThenConfirmsWithoutResending` fails;
   (c) `ConfirmOnce` sending/post-SQL -> `TestOpencodeDeliverPostsOnce` fails.
7. **Commit the plan** as `docs/plans/2026-09-30-delivery-lock.md` on the branch, as
   #739 committed its plan.

## Coverage

No package moves. `internal/delivery` must not drop below its
`testdata/coverage-baseline.txt` entry; add tests for the new branches, never lower
the baseline.

## Deleted (closed list)

Nothing is deleted: `Confirm`, `confirm`, and the poll constants keep their behaviour
for the admitting tick.

## Report must include

- what changed, and the tick counts: tick 1 one `Deliver` + one `Confirm`; tick 2 one
  `ConfirmOnce`, zero `Deliver`, zero polls;
- the three mutation checks: what was broken, which named test failed, that it was
  restored;
- the `make check` tail and the `make e2e` tail;
- `git diff --stat main...HEAD`;
- #750's acceptance verbatim, with the test that pins each clause: "a tick must not
  hold the state lock for the deliverer's confirm window" -> the repeat-tick call
  counts; "a repeat tick must not spawn more than one read-back per admitted entry" ->
  `ConfirmOnce`;
- the four #739 pins still passing, with output;
- coverage per package vs the baseline.
