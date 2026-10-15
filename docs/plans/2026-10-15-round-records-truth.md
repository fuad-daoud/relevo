# Plan `round-records-truth`: round records that disagree with what ran

## Scope as built

History per round (done):

- `internal/ingest/outcome.go`: a round with no pick or switch entry of its own
  carries the builder the earlier rounds left running; only a round with no
  record and no earlier round falls back to the binding's live candidate.
  `switchesForRound` skips a poll's observed-switch entry once the log holds the
  server's own switches for the round.
- `internal/relevo/remote_catchup.go`: `applyCatchUpReport` logs each switch the
  server shipped for the closed round as a switch entry of that round, after the
  report is queued so the payload does not name it twice.
- `internal/store/log.go`: `PollSwitchNotePrefix`, shared by the poll writer and
  the history reader.

Trace check result: `chainRenderShippedSeed` consulted the engine's recorded
check only when `chainSeedView` had found no member-gate record, so a builder
with `check on` kept its own (red) gate record in the reviewer seed while the
engine's check run was green. The engine's recorded check now overrides the
member-gate record whenever it carries a log; the record is the fallback for a
chain whose engine ran no check. The mapping of a non-pass to red is pinned by
`TestCheckResultWordsFollowTheRunnersResult`.

## Tests

- `TestCandidateForRoundCarriesTheEarlierRoundsBuilder`
- `TestSwitchesIgnorePollEntriesWhenTheServerShippedTheHistory`
- `TestCatchUpRecordsTheServersSwitchesOnTheClosedRound`
- `TestCheckResultWordsFollowTheRunnersResult`
- `TestReviewerSeedNamesTheEngineCheckOverAMemberGate`
