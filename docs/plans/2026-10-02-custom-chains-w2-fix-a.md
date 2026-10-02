# W2 review fixes, round A: the engine state is the only truth (bugs 3, 7, 8, 9, and the leftover legacy writers)

A whole-slice review of W2 (verdict `changes`) found bugs in the engine swap. Most trace back to legacy chain code that plan 9 should have removed: it still writes chain rows through the old `chain.State` and never updates `StateJSON`. **Base:** your branch HEAD (`9e0a6bbd`). Constraints as in every W2 round: the gate, no `--write`, no exclusions, files ≤ 600, functions ≤ 70, comments say why, new commits.

## The bugs to fix (reproduced by the reviewer unless marked)

- **Bug 3: a failed remote send halts an engine chain through the legacy path.** `chainRemoteSendFailed` (`internal/relevo/chain_send_remote.go:203`) calls `chainSweepHalt` (`chain_sweep.go:77`), which saves through `chainRowWithState` and writes a legacy trace row.
  - The row becomes `halted`, but `state.Status` stays `running`, so `ChainResume` fails with "cannot resume a running run".
  - **Fix:** halt through the engine. Feed a `needs_you` event, or set the state's status and reason, through the workflow driver that writes `StateJSON`, the projected columns and a workflow trace row.
- **Bug 9: `relevo done` leaves the engine state as it was.** `chainDoneRow` (`chain_verbs.go:228`) writes status `done` and phase `finished` without touching `StateJSON`. After `stop` then `done`, `state.Status` is still `stopped`.
  - **Fix:** done updates the state (status `done`), then projects.
- **Bug 8: the findings count is always 0 for engine chains.** `chainFindingsOf` and `chainFindings` (`chain.go:373`, `chain_served.go:390`) look for `chain.EventSecurityClosed`.
  - **Fix:** read the count from the state, i.e. the scan step's `findings` outcome in `Results` (for the default workflow, the step that declares a `findings` count). This feeds the served `ChainView.Findings`, the mirror's end payload (`chain_pull.go:467`) and the local end payload, which must carry the count again.
- **Bug 7 (not reproduced): a failed `ConvertLegacyChains` leaves rows with no engine.** `cmd/relevo/wire.go:193` only warns, and its comment claims the legacy machine still runs; plan 9 deleted it. One bad row aborts the conversion of every row.
  - **Fix:** convert per row, each in its own transaction.
  - A row that cannot convert is marked halted, with the reason "could not convert to a workflow: <err>", in a way that needs no engine (its status column), and is logged.
  - Every other row converts.
  - Fix the comment.
- **Finish plan 9's deletion.** After the fixes above, no code path writes a chain row through `chainRowWithState` or `chainStateOf`, or appends a legacy trace row. Delete whatever is dead as a result: `chainSweepHalt`, `chainSaveWithTrace`, `chainTerminal`, `chainRowWithState`, `chainStateOf`, `chainFindings`, `chainDoneRow`'s legacy body, and anything only they used.
  - If one of these is still needed to read an old trace row, keep only that read side, in `internal/chain/legacy.go`.

## Tests (named; each fails before its fix)

- `TestWorkflowRemoteSendFailureHaltsTheEngineState`: the row and the state both halted, and `ChainResume` works afterwards.
- `TestWorkflowChainDoneWritesTheEngineState`.
- `TestWorkflowFindingsCountReachesTheEndPayload`: local, served view, and mirror payload.
- `TestConvertLegacyChainsSkipsABadRowAndConvertsTheRest`.

## Mutation for the MasterMind (run it, report it, restore it)

Make `chainRemoteSendFailed` call the legacy halt again. `TestWorkflowRemoteSendFailureHaltsTheEngineState` must fail.

## Gate

- `make check` and `make e2e` green.
- Save this file as `docs/plans/2026-10-02-custom-chains-w2-fix-a.md` and commit it with the code.

## Halt if

- Deleting a legacy writer breaks an e2e pin.
- Reading an old trace row needs more than the read side.
