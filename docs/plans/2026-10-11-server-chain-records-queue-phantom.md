# Plan B: record queue waits and stop the phantom open round

Base: `origin/main 19e25095`, on top of Plan A. Issues: #1082 (queue waits),
#1080 (phantom open round).

## Queue waits

The server already writes a `KindQueue` log entry when it accepts a round
("queued (x/y builders busy)", with the census) and another when `Admit` starts
it ("started after <wait> queued"). Ingest turns every log line into an `event`
row linked to its round, so on the server both are queryable. A retry of an
accepted start returns before `recordRoundAccepted`, and a failed admit returns
before it writes its entry, so neither writes a record.

What was missing is the mirror: a pull installed the round's prompt and closed
facts and wrote no queue entry, so the mirror's own `event` table had no wait.
`chainPullQueueEntry` records the wait the server stamped on the closed round
(`QueuedAt` to `RoundStartedAt`), once per round, after the prompt entry. A
round the server never queued ships no stamp and writes nothing.

## Phantom open round

A chain that is done or stopped must leave no member with a prompt, no report,
and nothing that will ever close it. `chainCloseStrays` closes such a round as
stopped/abandoned with its real state, keeps its log entries, clears
`RoundStartedAt`/`QueuedAt`, and leaves the member at the round it closed. It
matches only a round that has a prompt and no report, on a chain already
terminal. A halted chain is not swept: a halt is the state a resume sends from,
so its open round is the awaited one. A running chain is never touched.

`chainPullFinish` runs the sweep after it writes the pulled row, so an existing
stray on a mirror is closed on the next pull and a re-pull never resurrects it.
Rows are never deleted.

## Premise notes

A local chain's finish and stop already leave no open round on current main
(`TestChainFinishLeavesNoOpenRound`, `TestChainStopLeavesNoOpenRound` pass with
the sweep removed), so no change was made to `chainFlowSend` or `chainApply`.

No migration: `event` and `chain_event` already sync, and a stray is closed by
appending log entries, not by a new column or table.

## Round 2

The sweep did not fix #1080. A sandbox chain left `rc` round 2 `open` with one
log entry: the chain's terminal notice, filed under `carrier.Round`, which had
already advanced past the closed round. Ingest derived `open` from that entry
alone; `store.RoundOpen` (prompt and no report) never saw it.

- Source: `chainTerminalRound` files the end notice (`chainTerminalWF`, and both
  notices in `chain_pull.go`) under the carrier's last closed round. Delivery
  does not depend on the number: a through-pull claims entries at or below the
  waited round (`ClaimableForMasterMindThrough`, `delivery/pull.go`), so an
  earlier round only makes the notice claimable sooner.
- Existing phantoms: `deriveOutcome` returns `halted` for a round holding no
  prompt, with no closed time. Evidence: 5 `open` rounds in the developer's
  database, every one without a prompt. No new outcome value, so no schema,
  histq or UI change. Nothing is deleted.
- Tests: `TestLocalChainTerminalNoticeLeavesNoOpenRound`,
  `TestMirroredChainTerminalNoticeLeavesNoOpenRound` and
  `TestIngestDerivesNoOpenForAPromptlessRound` assert on the ingested `round`
  row; each fails when its fix is reverted.
- Sweep removed: `chainCloseStrays` is deleted. Finish and stop already leave
  no prompted, reportless round (`TestChainFinishLeavesNoOpenRound`,
  `TestChainStopLeavesNoOpenRound` pass without it), and no sequence produced
  one on a done or stopped chain, so it guarded a case nobody can produce.
