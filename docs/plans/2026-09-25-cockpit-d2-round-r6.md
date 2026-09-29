# Cockpit D2 round detail, round 6: honest round count, short cost word, graceful narrowing

Date: 2026-09-25. Worktree: ck-d2-round, which holds rounds 1-5 **uncommitted**. Build on it; do not reset or commit.
Line numbers are exact in the worktree now.

**Stop rather than improvise.** If anything differs from what is quoted, halt and report.

## 1. What the real-screen capture showed (idle binding, round 5 reported)

1. The tokens line printed the cost part verbatim:
   `unknown: no price for google/gemini-3.8-flash-high; stream still open; timed out`. It overflows and pushes the pid off.
2. The context row and tabs row say `round 5 of 6` / `r5 of 6`, although round 6 was never sent. `]` steps to an empty r6.
   `detail.rounds` is set from `r.Round` (round_pane.go:106 and :200), which for an idle binding is the **next** round.
3. At 80 columns the context row is cut mid-word (`relevo/ck`), because it is built as one string and `fit` truncates it.

## 2. Changes

**2.1 The cost word** (round_pane.go `tokensLine`, line 313). When the kept last part (the cost word) starts with `unknown`, render
it as `no price`. Every other cost word is unchanged.

**2.2 The round count.**
- Add `func roundsOf(r relevo.BindingStatus) int`: `r.PlanRound` when it is > 0, else `r.Round`.
- Use it at round_pane.go:106 (`rounds: roundsOf(*r)`) and :200 (`p.detail.rounds = roundsOf(*r)`). Leave the hist path (:143,
  `h.Rounds`) alone.
- `paneRound` is unchanged. A working binding already has Round == PlanRound, so nothing changes for it.
- The "live" suffix, in both `detailHeader` (line 306) and `roundView.Context`'s right side: show ` · live` only when the round on
  screen is the binding's newest sent round **and** that round is open (`b.RoundEnd.IsZero()`, where `b := row(p.report,
  p.detail.name)`). A nil row means no suffix. An idle binding whose last round reported shows `round 5 of 5` with no "live".

**2.3 Narrowing the context row** (view_round.go `Context`, from line 121). Build the left side as ordered parts instead of one string:
1. pill;
2. age;
3. candidate;
4. planner;
5. branch;
6. dirty.

Then fit them to `env.Width` in this order:
- first drop the right side (`round N of M…`) when `left + 1 + right` does not fit;
- then drop dirty, then branch, then planner, then candidate, one at a time, until the left fits;
- never drop the pill or the age.

Separators go with the part they introduce, so no dangling `  ·  ` is left. Return `(left, right)` with `right` possibly "".

## 3. Tests

- `TestRoundTokensLineCostWord`: a LiveUsage whose cost part is
  `unknown: no price for x/y; stream still open; timed out` renders `no price` and not `stream`. A measured cost word stays as it is.
- `TestRoundsOfIdleAfterReport`: row{Round 6, PlanRound 5, BuilderStatus idle, RoundEnd set}. Opening it gives `detail.rounds == 5`, the
  Context right side is `round 5 of 5` without `live`, and `]` stays on 5.
- `TestRoundsOfWorking`: row{Round 3, PlanRound 3, RoundEnd zero, working} gives `round 3 of 3 · live`.
- `TestRoundContextNarrowDropsWholeParts`: at widths 132, 100, 80 and 60, the stripped left never ends inside a word of the branch or
  planner. Each part is either whole or absent. The pill and age are always present.
- Existing round and step tests must stay green. If one encoded `N of Round` for an idle binding, port it (cite §2.2).
- Goldens: regenerate them. Round goldens may change on the `of M` count and the cost word. No fleet or stats golden may change.

## 4. Commands

- Loop: `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ -count=1`, plus `-run Golden -update`.
- Final: `gofmt -l internal/ui/*.go`, `go vet ./internal/ui/`, and
  `env TMPDIR=$HOME/.cache/relevo-verify/tmp go test ./internal/ui/ ./cmd/relevo/ -count=1`. Not `make check`.

## 5. Report

List the files changed, any ported tests, the golden diffs, and the check results.
