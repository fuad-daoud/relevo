# Server-chain checks halt on a lookup miss

Base: `origin/main 19e25095`. Issues: #1101, #1087, #1055.

## The halt

Both #1101 and #1087 halt with exactly:

    check <step>: relevo: no such check run on this binding

That string is built in one place, `internal/relevo/chain_check.go` in
`chainTickPlacedCheck`, from a `GetCheck` 404. The server half is
`ServedCheckGet` (`internal/relevo/served_check.go`): a binding holds one check
run at a time, so any id that does not name it is `ErrNoCheck`, mapped to 404 at
`internal/serve/checks.go`.

Two paths reached that halt with an id no server run carried.

### The fallback records a run id that was never minted

`chainFlowCheck` has two arms. The check-route arm mints `chk-N` via
`chainNextPlacedCheckRun` and posts `CreateCheck`; the awaited run is then one
the server holds. The fallback arm -- a server without the check feature --
answers from the pulled gate record and set `Awaiting.Run = round`, the
builder's newest closed round. That is a round number, not a run id, and no
`CreateCheck` was ever posted for it.

The number does not stay on the step it was recorded against. `chainFlowPullCheck`
closes that check and the engine routes to the next one, which inherits
`Run = 1`. Its tick then reads any `Run != 0` as a minted id and asks the server
for `chk-1` -- a run it never created. Round 1 collides exactly.

### A bare miss was read as a loss

`chainTickPlacedCheck` halted on any 404. A bare 404 proves nothing: the run is
stored when the create answer comes back, so a tick that reads between the
create being asked for and its answer arriving sees nothing under the id. The
same is true of a server still starting the run.

Halting there ended chains on checks about to answer, and because
`chainPullFinish` flips a halted mirror back to running whenever the server view
runs while the tick halts it again, one false halt became a chain notice per
sweep -- the `to_planner` event every ~12 s in #1087 and every ~4 s of
`show --log` in #1101.

## Topology: which machine halts

`chainOnServer` is `c.Server != ""` (`internal/relevo/chain_server.go`). The
mirror's row carries it; the server's own row does not, because the server's row
is built by `chainRow`, which never sets `Server`. So:

- **Placed writer, chain engine local** (the #1101 repro path): the mirror guard
  at `chain_check.go` returns early, so the tick that halts is the local engine's.
- **Whole chain on a server** (`relevo chain --server zen`, #1087): the client's
  row names the server, so the guard skips it; the halt is produced on the
  server's own daemon, where the row is local and the placed writer is remote.

Both are the same `GetCheck` 404 in `chainTickPlacedCheck`, reached through the
same fallback-id leak. Both are pinned by tests below.

## The fix

1. `chainTickPlacedCheck`: a 404 while the step is still running reads as
   running. Nothing is written and nothing is delivered; the run is polled
   again on the next tick. A loss is proved, not assumed -- the binding holds a
   *settled run under a different id for this same step*. Only then does the
   chain halt, naming the check. Everything else keeps awaiting, and the chain's
   own round and chain timeouts end a check that truly never answers.
2. `chainFlowPullCheck`: records no run id. The fallback runs no check on the
   server, so there is nothing to poll. It sets `Awaiting.Run = 0` and closes
   with `Run: 0`, which is the run the state it saved actually awaits.
3. `--from` for server chains: `ChainResumeRequest` gains `From` and `Params`,
   the client sets them, and `resumeOptionsFromWire` maps them onto
   `ResumeOptions`, so the server's own engine enters the named step.

`From` plus a close is refused by `resumeClosed` naming the target -- the
precedence is "refuse", never "silently enter check".

## #1055

Traced stored -> transported -> rendered for a served-chain builder round's gate:

- **stored**: a served chain runs its check as a chain step, so the log is sealed
  into a round_file row by `chainCheckSealLog` and removed from disk.
- **served**: `gatePath` substitutes that sealed row via `chainGateLogPath`,
  which resolves the newest check run keyed to that member and round.
- **transported**: `fetchCatchUpGate` -> `downloadTemp` -> `applyCatchUpFiles`
  renames it onto `GateLogPath`.
- **rendered**: `ShowGate` reads `GateLogPath` through `Store.ReadFile`.

No leg caps. `servedCheckLogCap` is an alibi: 9 lines is far below 64 KiB, and it
caps only a served check's `CheckView.LogTail`, never a builder round's gate log.
Tests now pin the whole multi-stage log at the serve leg and at the render leg.

## Tests

- `TestPlacedCheckMissKeepsAwaiting`, `TestPlacedCheckMissAsksAgain` -- a miss
  neither halts nor delivers, and is polled again.
- `TestPlacedCheckHaltsWhenTheServerHoldsAnotherRun` -- replaces the old
  `TestPlacedCheckHaltsWhenTheServerHasNoSuchRun`, which pinned halt-on-any-404.
- `TestFallbackCheckRecordsNoRunID` -- the fallback mints no run id and never
  polls one.
- `TestResumeFromEntersReviewOnTheDefaultWorkflow`,
  `TestResumeFromAndCloseIsRefused`, `TestChainResumeOnAServerChainCarriesFrom`,
  `TestChainResumeOnAServerChainOmitsEmptyFrom`,
  `TestServedResumeCarriesTheFromTarget`.
- `TestRoundFileGateServesTheWholeMultiStageLog`,
  `TestShowGateRendersTheWholePulledLog`.

## Scope

`internal/remote/chain.go` gained only `From` and `Params` on
`ChainResumeRequest` (plus its contract test). Without them `--from` cannot
reach the server that drives the chain. No other struct in that file was
touched.