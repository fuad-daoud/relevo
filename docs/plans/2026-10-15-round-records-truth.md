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

Trace check result (not reproduced): `chainCheckResult`, `chainGateResult`,
the placed poll and the pulled-record fallback all map a non-pass to red, and
the served view ships the runner's word. No source change was made; the mapping
is pinned by `TestCheckResultWordsFollowTheRunnersResult`.

## Tests

- `TestCandidateForRoundCarriesTheEarlierRoundsBuilder`
- `TestSwitchesIgnorePollEntriesWhenTheServerShippedTheHistory`
- `TestCatchUpRecordsTheServersSwitchesOnTheClosedRound`
- `TestCheckResultWordsFollowTheRunnersResult`
