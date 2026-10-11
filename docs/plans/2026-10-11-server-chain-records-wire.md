# Plan A: carry the server's real round start and step trace

Base: `origin/main 19e25095`. Issues: #1079 (round start), #1081 (step trace).

## What was wrong

A round that ran on a server was mirrored with the pull's own clock as its
start. `chainPullPromptEntry` stamped the prompt entry at `rt.Now()`, and the
mirror derives `round.started_at` from that entry (`ingest.roundFacts` takes the
earliest prompt TS), so `history --json` showed a server-chain round lasting
about the 30ms the pull itself took.

The server already knew the round's real start and had it twice: the binding
carries `RoundStartedAt` for its current round only, and the round's own log
entries carry the accept and admit notes for every round the member ever ran.
Neither travelled per round, so `ClosedRoundView` -- which is what an older
round of a member is shipped as -- had nowhere to put a start.

The server's step trace was the same shape of loss: `ServedChainView` builds a
full `ChainView.Trace` from `chain_event`, and `chainPullFinish` copied only
the chain row, so a mirror's `show --trace` had nothing the server had sent.

## Steps

1. Wire: `RoundStartedAt` and `QueuedAt` on `ClosedRoundView`, additive and
   `omitempty`, so a server older than the field sends neither.
2. Server: `servedRoundStamps` reads each round's start and queue wait from its
   own log entries -- the admit note for the start, the accept note for the
   wait -- so an older round of a member answers too, which the binding's
   current-round stamp cannot do. `servedRoundFacts` carries them, so both
   `ServedView` and the `ServedChainView` rounds loop ship them.
3. Pull: `chainRoundFacts` completes a newest closed round's start from the
   binding view, `chainRoundView` copies the per-round entry's start onto the
   view it installs from, and `chainPullPromptEntry` stamps the prompt entry
   with the server's start. The prompt entry is the durable half: it is what
   `roundFacts` derives `started_at` from, and it survives the round's close.
4. Pull: `chainPullTrace` copies the view's trace onto the mirror's
   `chain_event` under the server's own seqs, skipping seqs already stored. A
   row with no seq, or whose event/action is not the JSON document those columns
   hold, is refused rather than stored as a guess.
5. DB: `ChainEventPut` stores a row under a named seq and reports whether it was
   new, which is what makes a re-copy a no-op.

## What was deleted

1. Mirror prompt/`started_at` stamped at ingest time for server-chain rounds.
2. The mirror dropping the server chain's step trace.

## Findings on the plan as written

- The plan's step 2 named `ServedView`/`servedRoundFacts` reading the start from
  `RoundStartedAt`/`QueuedAt`. The binding's `QueuedAt` cannot answer for a
  closed round: `Admit` zeroes it as it admits (`internal/relevo/queue.go:123`),
  and the close zeroes `RoundStartedAt` (`internal/relevo/reconcile.go:1038`).
  The stamps therefore come from the round's own log entries, which do keep
  every round's history. The binding-level stamp is still used, as the fallback
  `servedRoundFacts` already had for a newest closed round.
- The plan's step 3 asked the pull to stamp the mirror binding's
  `RoundStartedAt`. That stamp is written, but it is cleared by the close the
  same install performs, so it is not observable afterwards; the tests pin the
  prompt entry and the mirrored `round.started_at` instead, which are the
  durable records.
- No migration was needed: both changes are additive wire fields plus a new
  insert statement. No schema column, event kind or shared table was added, so
  `testdata/schema.golden`, the sync outbox triggers and the coverage baseline
  are untouched. `chain_event` was already in `SharedTables` ahead of
  `chain_member`, and `TestSharedTablesAreParentsBeforeChildren` pins that
  order against the real foreign keys.
