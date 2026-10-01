# The first live chain's UX fixes (turso-466)

Base: `main` at `0ac52a80`, in a throwaway worktree, after two parallel rounds
landed — the `RoundOpenError` / conflict mapping round and the chain
security-diff round. One builder round: code, tests, and this document in one
new commit. New commits only; no amend and no rebase. `make check` and
`make e2e` are green on the round's own commit.

Each item below was verified against the tree while planning; the fix and its
test are named, and every test's mutation was run and watched to fail.

## What broke, and the fix per item

1. **A resume could overwrite an open member round.** `relevo chain --resume`
   computed the member it would send to only after it had written the builder's
   gate and, when security was just turned on, created the member; if that
   target's round was still open the send failed and the resume left a junk
   halt row and a changed chain row. Fix: the decision/target block moves above
   the first write in `chainResumeLocked`, and a new `resumeOpenRoundRefusal`
   returns the send path's `RoundOpenError` before any write — no trace row, no
   staged round, the chain row byte-for-byte unchanged, and the CLI maps it to
   `conflict` naming `relevo stop <member>`. (`internal/relevo/chain_resume.go`.)
2. **A needs-you trace row printed its reason twice.** `TraceLine.Line`
   appended the action reason unconditionally, duplicating the event detail
   when the row's detail already was that reason. Fix: append the trailing
   reason only when the rendered detail does not already carry it; a distinct
   detail and reason still both print. (`internal/chain/trace.go`.)
3. **A terminal chain still showed its step.** `ChainSegment` printed the step
   for every chain. Fix: a done or stopped chain's segment drops the step (the
   work is over); a halted chain keeps it, and corrections still print.
   (`internal/view/chain.go`.)
4. **A halted/stopped chain with a running manual round read `NEEDS YOU`.** The
   chain row claimed a human must act while a manual builder round was in
   flight. Fix: `ChainFacts` gains `manual_round`; `chainFactsOf` sets it from
   the existing `chainBuilderRoundOpen` predicate and the builder's current
   round; the segment becomes `manual round N running` and the display word is
   the chain's own `HALTED`/`STOPPED`, not `NEEDS YOU`. The new display words
   are ranked with `ACTIVE` so the row does not sink below `DONE`. A chain row
   with nothing running keeps `NEEDS YOU`. (`internal/view/chain.go`,
   `internal/view/sort.go`, `internal/relevo/chain_status.go`.)
5. **Input copies lingered after a chain ended.** `.chains/<chain>/inputs/`
   stayed after the chain was done or stopped. Fix: a new
   `chainInputsSweep(rt, name, status)` removes the directory on done or
   stopped, called from `chainTerminal` (state-machine finish, stop event and
   direct stop) and from `ChainDone`; a halted chain keeps it and the
   `plan-i.md` copies are never touched. A removal failure is logged, never
   raised. (`internal/relevo/chain_verbs.go`, `internal/relevo/chain.go`.)
6. **A resume re-sent the plan copy instead of the stopped round's own text.**
   A stopped correction or repair round came back as the plan copy. Fix: the
   text resolution became `chainResumeText`, which returns the awaited builder
   round's own staged prompt — the exact bytes the round was handed — and falls
   back to `chainSeedText` when there is no awaited round or the staged prompt
   is gone. A plain plan round is byte-identical to the plan copy.
   (`internal/relevo/chain_resume.go`.)
7. **`bind --server --gate` never reached the served binding.** The client
   resolved the gate for the local path but sent nothing on the wire and stored
   nothing on the mirror. Fix: `addRemote` resolves the gate exactly as the
   local add does (`resolveGateFor` with the client's policy and the actor's
   `roleChecks`), puts it on `remote.CreateBindingRequest.Gate` and records the
   same value on the mirror binding. An unnamed flag takes policy
   `gate.default`; `--no-gate` sends `""`; a reader role is still refused
   before any create. `--regate` is out of scope: the wire has no field for it.
   (`internal/relevo/remote_add.go`.)

## Tests and the mutation each catches

| # | Test | Mutation run | Observed failure |
|---|---|---|---|
| 1 | `TestChainResumeRefusesAnOpenMemberRound`; `TestChainResumeOpenRoundIsAConflict` | refusal block deleted from `chainResumeLocked` | both refusal subtests: chain row changed, trace rows 2 not 1; CLI: `internal`/`relevo bugreport` not `conflict`/`relevo stop` |
| 2 | `TestTraceLinePrintsAHaltReasonOnce` | unconditional trailing reason in `Line` | the reason appears twice |
| 3 | `TestChainSegmentOmitsTheStepOnATerminalChain` | done/stopped condition dropped | done and stopped cases print the step |
| 4 | `TestStatusShowsARunningManualRoundOnAHaltedChain` | `ManualRound` left zero in `chainFactsOf` | display `NEEDS YOU`, no `manual_round` |
| 5 | `TestChainInputsAreRemovedWhenTheChainEnds` | sweep calls dropped from `chainTerminal` and `ChainDone` | done, stopped and done-verb subtests find `inputs/` still there |
| 6 | `TestChainResumeReSendsTheStoppedRoundsOwnPrompt` | staged-prompt override disabled | the re-sent round holds the plan copy, not the planner/repair text |
| 7 | `TestAddRemoteCarriesTheGate` | `Gate:` dropped from `createReq` | the create request carries no gate |

## E2E assertions changed

The two finished-chain e2e tests asserted that the chain's input copies still
existed on disk and held the round-file key's bytes. With the inputs now swept
when the chain ends done, those assertions became false. In
`internal/e2e/chain_test.go` and `internal/e2e/chain_remote_test.go` every
"the copy is a regular file" and "the copy's bytes equal the key's bytes"
check was flipped to "the copy is gone" (`os.Stat` → `fs.ErrNotExist`); the
"the seed names the copy, never the key" assertions and the plan-copy reads
stay, and the whole-chain span check now reads the `NNN-chain-diff.patch` key
itself through the store, so the e2e still pins that the diff covers every
round. Byte-equality coverage of the copies lives on in the live-chain tests
(`TestChainReviewerSeedNamesACopyOfTheSealedDiff`,
`TestChainReviewerSeedNamesACopyOfThePlanDiff`,
`TestChainReviewerSeedNamesThePlanCumulativeDiff`).

## Checks run

- Focused: `go test -count=1 -run 'TestChainResume|TestChainInputs|TestStatusShowsARunningManualRound|TestAddRemoteCarriesTheGate' ./internal/relevo/`, then `go test -count=1 -run 'TestTraceLine|TestShowTraceHalt' ./internal/chain/ ./internal/relevo/`, then `go test -count=1 -run 'TestChainSegment|TestChainDisplay' ./internal/view/` — all green.
- `make check` — green (lint 0 issues; `internal/e2e` green within the run).
- `make e2e` — green.
- No golden changed: `cmd/relevo/testdata/contract/statusline-chain{,-json}.golden` and `status-chain.golden` stay byte-identical (the fixture is halted with no open builder round, so `manual_round` is 0 and omitted, and the step stays). `scripts/check-coverage.sh` reports ok and the baseline is not lowered.

## Deliberately left

- The adjacent `ErrReportPending` refusal path a resume can still halt on: a
  member with a pending round file is left to the send's own failure, exactly
  as before.
- `--regate` on a plain remote bind has no wire field and is out of scope; only
  `--gate`/`--no-gate` travel.
- The reviewer-open CLI pin drives a store-only fixture; the refusal precedes
  the send, so it starts no daemon, spawns no harness and reaches no network.
