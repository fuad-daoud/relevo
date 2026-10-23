HARD RULES: do not push, do not run `gh`, do not touch any remote or PR; relevo owns the branch. Do only this plan, and end it in its own commit.

# Plan: a round row counts every session it ran (#1312)

Tree read at f00dedcf (`/home/fuad/projects/relevo`, throwaway copy for this round).
Do only #1312: the round row's tokens and cost. One commit.

## 1. What is wrong, verified in this tree

- A nudge (`nudgeResume`, `internal/relevo/nudge.go:107`) resumes the session and
  writes a `KindSwitch` entry whose `Usage` is the outgoing session's
  (`peekUsage`, `nudge.go:116`), read *before* `resumeRound` (`nudge.go:118`).
- `resumeRound` -> `startProcess` moves `Builder.StreamStart` to the stream's
  current size (`headless.go:342-352`, called at `headless.go:496`), so the
  close-time read (`reconcile.go:962`, `recordUsage(ctx, rt, roundSource(rt, b, roundStart, now))`)
  sees only the resumed session. That is why the report entry's `Usage` is short.
- The whole-round figure already exists for the status surface:
  `view.PriorTokensOf` (`internal/view/status.go:426-447`) sums the round's
  switch-entry usage plus a report's `PriorTokens`, and `internal/relevo/status.go:403`
  (status row) and `internal/relevo/served.go:249` (the wire) use it. The nudge
  log line in the issue printed the first session, so in that report the peek
  worked: the loss is the aggregation, not the read.
- The DB row is the one surface that never aggregates: `internal/ingest/facts.go`
  `roundFacts` -> `applyReport` -> `applyUsage(r, e.Usage)` (`facts.go:70-80`,
  `92-104`) takes the report entry alone. Every reader of `round` (histq/history,
  stats, the dash token cell) therefore shows the resumed session only. Same
  shape for a mid-round candidate switch (`switch.go:293`), a daemon-restart
  resume (`headless.go:1159`), and a client's remote round, whose earlier cost
  the wire cannot carry at all (`internal/remote/proto.go:251-253` ships
  `PriorTokens` only).

## 2. Behaviour after the change, and the cases

A round's row figures are the newest report entry's usage **plus every earlier
segment of that round**: each `KindSwitch` entry that carries a non-nil `Usage`
(nudge, mid-round switch, daemon-restart resume), and the `PriorTokens` a report
entry carries for a round another machine ran.

- **Nudged round**: tokens and cost cover both sessions; the basis follows the
  sum rule of §4.2.
- **Switched round**: the same, and a switch entry of another round never leaks
  in.
- **Remote round with `PriorTokens`**: tokens are complete; cost is `unknown`
  (§4.2) — a partial sum is never shown as the round's cost.
- **Round with no earlier segment**: exactly today's figures. A zero prior is a
  no-op in the fold, so this case is not a special branch.
- **Report entry with no usage**: the row keeps no token/cost figures, as today.
  A round is never given a partial total in place of an absent report segment.
- **Open round (no report entry)**: unchanged, nil figures.

## 3. Seams

| Where | What |
|---|---|
| `internal/view/status.go:426-447` | `PriorTokensOf` becomes the tokens view of a new `PriorUsageOf(entries []store.LogEntry, round int) usage.Usage`, the one copy of the rule. Its two callers (`internal/relevo/status.go:403`, `internal/relevo/served.go:249`) are not touched. |
| `internal/usage/usage.go` (after `Fold`, ~147) | `Add(u, o Usage) Usage` — tokens sum, and the one place "how two segments' costs combine" is written (`Fold` owns the same rule for samples). |
| `internal/ingest/facts.go` | `13-51` `roundFacts` (computes the round's prior once), `70-80` `applyReport` (gains it), new `totalUsage`, `92-104` `applyUsage` unchanged. New import `internal/view` (acyclic: view imports neither ingest nor relevo; `go list -deps ./internal/view` names no ingest). |
| `internal/relevo/nudge.go:116` | the outgoing segment is read with `recordUsage` (`usage.go:123`, the close's `usageDeadline` 5 s) instead of `peekUsage` (`usage.go:26`, the live `liveDeadline` 500 ms). |
| Tests | `internal/usage/usage_test.go`, `internal/view/status_render_test.go:711`, `internal/ingest/facts_test.go`, `internal/relevo/nudge_test.go`. |

## 4. Decisions

### 4.1 One shared pure function, in view

`view.PriorUsageOf` is PriorTokensOf's rule with cost; `PriorTokensOf` returns
`PriorUsageOf(entries, round).Tokens`, so the rule exists once and both existing
callers keep working unchanged. `internal/ingest` calls `view.PriorUsageOf`
directly.

Rejected: moving the rule down to `internal/store` (which owns `HasKind`,
`HasPrompt`, `RoundOpen`). It would move code between packages, and CLAUDE.md
makes that a coverage-baseline regeneration (`sh scripts/check-coverage.sh --write`)
inside a bug fix; view is where a cleanup round (#592) deliberately put the
status read model this rule belongs to.

### 4.2 Cost: sum the bills, or say unknown

- A side that moved tokens must carry a bill: `Cost.Basis` `measured` or
  `estimated`, and not a plan lane.
- Both sides billed: `CostUSD` is their sum, `CostBasis` is `estimated` if
  either side is `estimated`, else `measured`. An estimate must not hide inside
  a measurement — the rule `Fold` applies and `Spend` splits by.
- Any side that moved tokens without a bill — a remote round's `PriorTokens`
  (tokens only), a plan lane, an unreadable read — makes the row's cost
  `unknown` with `CostUSD` 0, never the priced remainder. That is the rule
  `Fold` already states for an unpriced sample ("a measured sample after an
  unpriced one must not leak a partial figure"), and `unknown` is the word
  `histq`/`stats` already refuse a cost for (`costKnown`,
  `internal/histq/group.go:186`). Rendered, it reads `unknown`, not `$0.00`.
- A side that moved no tokens adds nothing and poisons nothing: a switch entry
  whose read found nothing is recorded, not guessed at.
- A plan-lane segment poisons because the row has no plan column: a
  subscription's notional dollars are not a bill and must not be summed as cash.
- Shipping prior cost from the server is **out of scope**: it would touch
  `internal/remote/proto.go`, `served.go`'s `ClosedRoundView`, the client's
  catch-up and the chain pull for one field, and the design already says remote
  dollars stay last-builder-only. The row says the cost is partial instead.

### 4.3 Re-ingest: existing rows are corrected, not only new rounds

Ingest recomputes, it does not append: `appendEvents` folds the stored events
in when the cursor resumed (`ingest_tx.go:183-205`), `upsertRounds` runs
`roundFacts` over *every* round it sees (`ingest_tx.go:229-274`), and
`UpsertRound` updates a changed row (`internal/db/write.go:280-296`). So a
round's row is corrected **on the next ingest of its binding**, whenever that
happens.

The catch, and the honest answer: the daemon ingests a live binding only when
its store revision changed (`internal/relevo/daemon.go:743`), an archived
binding once ever (`daemon.go:560-567`), and there is no `relevo ingest` verb to
force a pass. A binding nothing writes to again keeps its stale rows until
something touches it. Do not add a force verb in this round; say this in the
report.

### 4.4 The nudge reads the outgoing segment with the close's budget

Yes — `recordUsage`, not `peekUsage`. At nudge time the outgoing session has
exited and its stream is sealed, so the "don't wait for the record to close"
peek with its 500 ms live deadline (the status UI's per-tick read, #234) is the
wrong budget: a large stream's parse can overrun it and the whole first session
is silently lost, which is the loss #1312 is about. `recordUsage` (5 s, the
close's own deadline) reads the same complete record.

Accepted costs, to be named in the report: up to 5 s once per plan when a stream
has no exit trailer (the close already pays this inside the same write
transaction, `reconcile.go:962`); the nudge entry's usage is no longer nil when
the reader finds nothing (tokens 0, so it adds nothing and poisons nothing); and
the entry now carries the segment's step figures, which the Spend loop already
counts for every segment. `switch.go:293` and `headless.go:1159` keep
`peekUsage`: their outgoing process may still be writing, and a tick must not
wait on it.

### 4.5 Deliberately not done

- The issue's optional session count on the row: it needs a `round` column and a
  migration, and #1312 is the totals.
- The row's `Switches` counting a nudge as a switch
  (`internal/ingest/outcome.go:60-77`) while the status row counts it as 0: left
  exactly as it is; it is a different fact, not a total.
- Prior cost over the wire (§4.2).

## 5. Steps

Each step names its deliverable and how to know it worked. Order: tests first,
so every step's test fails before the code it pins exists.

1. **Tests first.** Add to `internal/ingest/facts_test.go`:
   `TestRoundFactsSumsANudgedRoundsSegments`,
   `TestRoundFactsSumsASwitchedRoundsSegments` (with a switch entry in another
   round that must not leak), `TestRoundFactsRemoteRoundCountsPriorTokens`,
   `TestRoundFactsSingleSegmentUnchanged`,
   `TestRoundFactsReportWithoutUsageKeepsNoFigures`; build the entries by hand
   and assert the `db.Round` fields (the file's `checkPtr` helper). Deliverable:
   the row contract of §2. Know: `go test ./internal/ingest -run RoundFacts -count=1`
   fails on the first three with exactly the report-only figures.
2. **`internal/usage/usage.go`**: add `Add(u, o Usage) Usage` and `TestAddCosts`
   (measured+measured -> measured; measured+estimated -> estimated; a side that
   moved tokens with no bill -> unknown/0; a zero-token unbilled side is a
   no-op; tokens always sum). Know: `go test ./internal/usage -run Add -count=1`
   green.
3. **`internal/view/status.go:426-447`**: add `PriorUsageOf`, make
   `PriorTokensOf` delegate, and add `TestPriorUsageOf` next to
   `status_render_test.go:711` (the existing tokens cases plus the cost cases of
   §4.2). Know: `go test ./internal/view -run Prior -count=1` green, and
   `TestPriorTokensOf` passes **unchanged** — that is the proof the tokens rule
   did not move.
4. **`internal/ingest/facts.go`**: import view; compute
   `prior := view.PriorUsageOf(events, n)` in `roundFacts`; pass it to
   `applyReport`, which folds it with `totalUsage` before `applyUsage`. Know:
   `go test ./internal/ingest -count=1` green — step 1's tests now pass and no
   other ingest test moved.
5. **`internal/relevo/nudge.go:116`**: read the outgoing segment with
   `recordUsage(ctx, rt, roundSource(rt, b, b.RoundStartedAt, now))`, still
   before `resumeRound`; add `TestNudgeRecordsTheOutgoingSegment` to
   `nudge_test.go` (`fakeUsage` with `samples` set, `peekSamples` unset):
   assert the nudge entry's usage carries those tokens and cost, and that the
   recorded `Read` source's `StreamFrom` is the pre-nudge `StreamStart`. Know:
   `go test ./internal/relevo -run Nudge -count=1` green and the existing nudge
   suite still green.
6. **Mutations.** For each row of §7: make the edit, run the named test, confirm
   it fails, revert. Know: the observed failure, quoted in the report.
7. **`make check-affected`**, then one commit.

## 6. Deletions (closed list)

1. `internal/relevo/nudge.go:116`'s `peekUsage` call for the nudge entry —
   replaced by the close-budget read. The peek stays for `switch.go:293` and
   `headless.go:1159`.
2. `applyReport`'s report-only fold (`facts.go:70-80`: the old signature and its
   `applyUsage(r, e.Usage)` call) — replaced by the fold that includes the
   round's earlier segments.

Nothing else is deleted: no exported name, column, config key, wire field, or
test. `PriorTokensOf` stays (delegating), `applyUsage` stays,
`TestPriorTokensOf` stays. Behaviour that changes without being deleted: the
row's figures for a round with an earlier segment; the nudge entry's usage
source and budget; existing rows, recomputed by the next ingest.

## 7. Tests, and the mutation each must fail

| Test | Mutation that must fail it |
|---|---|
| `TestRoundFactsSumsANudgedRoundsSegments` | `applyUsage(r, e.Usage)` in `applyReport`: drop the prior |
| `TestRoundFactsSumsASwitchedRoundsSegments` | (a) fold only entries whose note carries `nudgeNotePrefix`; (b) drop the `e.Round != round` guard from the shared rule |
| `TestRoundFactsRemoteRoundCountsPriorTokens` | (a) drop the report `PriorTokens` add; (b) keep the priced remainder with its own basis instead of `unknown`/0 |
| `TestRoundFactsSingleSegmentUnchanged` | force the sum's basis to `unknown` (or to `measured`) even with no earlier segment |
| `TestRoundFactsReportWithoutUsageKeepsNoFigures` | fold the prior into a report entry that carries no usage |
| `TestPriorUsageOf`, `TestAddCosts` | return the priced remainder when a side moved tokens without a bill; make a mixed measured+estimated sum `measured` |
| `TestNudgeRecordsTheOutgoingSegment` | revert `nudge.go:116` to `peekUsage`; and move the read after `resumeRound` |

## 8. What the report must include

- The four decisions of §4 (shared function and its home; the cost rule,
  including that a remote round's missing prior cost shows `unknown`, not `$0`;
  re-ingest, including that an untouched binding is not recomputed and no force
  verb was added; the nudge read and its accepted costs), plus §4.5's omissions.
- Files and functions touched, and `git diff --stat` against §3's list.
- The tests added, and for every mutation in §7 the command run, the failure
  observed, and that it was reverted.
- That no code moved between packages, so the coverage baseline is untouched —
  and `RELEVO_REQUIRE_COVERAGE=1` on the changed packages if it was run.
- The focused command's result and `make check-affected`'s output.
- Every existing test changed or ported, by name and with why (an unnamed
  changed test is a failed round).

## 9. Working efficiently

- Read §3's files once, at the lines given, in one batch.
- Focused loop: `go test ./internal/usage ./internal/view ./internal/ingest ./internal/relevo -count=1 -run 'Add|Prior|RoundFacts|Nudge'`
- Full gate: `make check-affected` (gofmt, vet, tidy, and every package the
  change reaches). Add no test in `cmd/relevo`: CI has no harness and no network.
