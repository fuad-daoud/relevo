# Chain member round caps, plan 4 of 4 (#1317, #1331)

HARD RULES: do not push, do not run gh, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

Goal: a round ended by `relevo done` (or any `stopped/*` entry with no report) is stored `done_no_report` with `closed_at` set, instead of `open` forever. See finding 2/3: the existing outcome word `done_no_report` is used; no new outcome value is added.

Behaviour:
- Ingest: a `KindStop` entry on round n, with no report entry for n, makes the round closed: `deriveOutcome` returns `db.OutcomeDoneNoReport` for it (after the report, done-marker, exit and halted arms; before switched/open), and `roundFacts` sets `ClosedAt` from the first `KindStop` entry's timestamp (same rule as exit/switch).
- Done: `relevo done` on a binding whose current round is open per the log (`store.RoundOpen`) writes the `stopped/done` `KindStop` entry even when no process pid existed (`done.go:174-181` currently requires `pid != 0`). A binding with no open round writes nothing (no phantom entry).
- Unchanged: `relevo stop` (it writes a report entry through `closeStopped`, so it is already `reported`).

Steps:
1. `internal/ingest/outcome.go:20-53`: add the `hasStop` flag and the arm; update the doc comment table above `deriveOutcome`.
2. `internal/ingest/facts.go:41-46`: add `store.KindStop` to the exit/switch case so it stamps `ClosedAt`.
3. `internal/relevo/done.go:~170-181`: read the log (`tx.ReadLog`) before the state change and append the stop entry when `store.RoundOpen(entries, b.Round)` OR a process was stopped. Keep the function under 70 lines (extract a helper if needed).
4. Tests: `internal/ingest/outcome_test.go`: `TestDeriveOutcomeStopWithoutReportIsDoneNoReport`, `TestDeriveOutcomeReportBeatsStop`;
   `internal/ingest/facts_test.go`: `TestRoundFactsStopEntryStampsClosedAt`;
   `internal/relevo/done_test.go`: `TestDoneWritesStopEntryForARoundThatNeverSpawned`, `TestDoneWritesNoStopEntryWhenNoRoundIsOpen`.
5. Check that an already-ingested open row is rewritten when ingest next runs over that binding (`upsertRounds` upserts every round in `all`). State in the report whether it is (this is the repair path for rows after the upgrade), but do not build a one-time repair.
6. Run `go test ./internal/ingest/ ./internal/relevo/ -run 'DeriveOutcome|RoundFactsStop|Done' -count=1`, `make check-affected`, commit.

Report must include: which existing golden or stats test moved, if any; the answer to step 5.



Also: no issue numbers or history in code comments or test names; this plan is a new commit, never an amend or rebase. Report the commit subject, `git diff --stat`, the focused test result and the `make check-affected` result.
