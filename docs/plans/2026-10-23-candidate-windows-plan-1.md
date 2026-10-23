HARD RULES: do not push, do not run `gh`, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

Design: `docs/specs/2026-10-23-candidate-windows-design.md` in this tree; every § reference points there. Read the sections this plan cites before editing.

# Plan 1 — read `windows` and `max_concurrency`

Deliverable: the two new candidate keys decode and validate.

1. Add `Windows`, `Window`, `MaxConcurrency` to
   `internal/candidate/candidate.go` beside the other row fields.
2. Add the clock parse (`ParseWindowClock`) and the window validation to
   `checkRest` (`candidate.go:353-369`), naming the candidate index and field on
   every refusal.
3. Add `Windows.Covers(now)` and `Windows.NextEdge(now)` as pure functions in
   the same package (one place decides what a window means; §1.2-§1.3 both read
   it).
4. Write `TestParseWindowsAndConcurrencyDecode`, `TestParseRefusesBadWindow`,
   `TestWindowClockFormsAgree`, `TestWindowCoversAcrossMidnight` in
   `internal/candidate/candidate_test.go`.
5. Run `go test ./internal/candidate/...` — green, and each refusal message
   names its field.

Commit: `candidates: read windows and max_concurrency`.
