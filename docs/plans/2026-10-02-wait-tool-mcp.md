# Plan: blocking `wait` MCP tool for tools-mode Claude Code MasterMind

Base: `origin/main`. One builder round. `relevo wait` CLI unchanged; no `cmd/relevo`
behaviour changes, so no `cmd/relevo` tests (per CLAUDE.md CI rules, CLI rules are tested
as pure functions in `internal/relevo`).

Seed-vs-code check: no contradictions. `server.go` does already carry the
`Mode == ModeTools && Kind != "opencode"` predicate for the bash wait block
(`internal/mcp/server.go:281`); `delivery.PullPendingThrough(ctx, st, name, route, round)`
(`internal/delivery/pull.go:109`) matches the seed's call shape; round 0 = newest planned
matches `DefaultWaitRound` (`internal/relevo/wait.go:77-88`).

## Behaviour and cases

1. **Availability.** The `wait` tool is listed and dispatched only when
   `Mode == ModeTools && Kind != "opencode"`. Channel mode and opencode list five tools,
   exactly as today; a `wait` call arriving in the wrong mode is rejected as unknown-tool
   (`CodeInvalidParams`), the same path as any unknown tool.
2. **Input.** `{name?, round?, timeout?}` with `additionalProperties: false`, decoded by the
   existing strict `decodeArgs`. Omit `name` = every non-done binding this MasterMind owns
   (resolved from the startup `MasterMind` id, the same identity `Status` filters on);
   `round: 0`/omitted = newest planned per binding (`DefaultWaitRound`); `timeout` = Go
   duration string, default the round budget (`RoundTimeoutMS`, store default 24h), clamped
   to at most 27h (under the ~28h `MCP_TOOL_TIMEOUT` wall), and additionally capped at 25m
   when the call carries no progress token (under the 30m stdio idle timeout). Unparseable/
   negative `timeout` is `-32602` invalid params before any verb runs; naming a binding that
   does not exist, a chain name, or a MasterMind with no active bindings is a tool result
   with `isError: true` (chains stay CLI-only: refuse with a hint at `relevo wait`).
3. **Blocking + result text.** The call blocks on `relevo.Wait` (single binding) or the new
   `WaitOwned` helper (no-name form), which resolves owned names then delegates to `Wait` --
   no new polling path, identical close semantics including immediate return on an
   already-closed round. Result text is one first line naming binding, round and outcome word
   (`closed`, `unmarked`, `needs-you`, `halted`, `gone`, `not-started`, `still-open`), then the
   delivered payload. Normal results (`isError: false`, never JSON-RPC errors): closed,
   unmarked, needs-you, halted, gone, not-started, the tool's own timeout (`still-open`, text:
   round still open, call `wait` again), and cancellation-evacuated states. Verb errors become
   `isError: true` text via the existing `toolResultFrom` path.
4. **Delivery route.** Delivery reuses `delivery.PullPendingThrough(ctx, st, name, "wait",
   round)` exactly as `Wait` already does -- the entry is marked `route=wait`; no new route is
   invented. A `DeliverErr` is carried in the text and never changes the outcome.
5. **Progress.** While blocked, the server emits `notifications/progress` with `params
   {progressToken, progress, total?, message?}` taken from the request's
   `_meta.progressToken`, first beat immediately then every ~30s. With no token the call is
   bounded under the idle window by the 25m cap above and emits nothing.
6. **Concurrency (decided).** Each `wait` call runs in its own goroutine so `Serve` keeps
   answering `ping` and other tools (`handleLine` is synchronous today; only the `wait` branch
   goes async; writes stay under the existing `writeMu`). Concurrent waits are allowed and
   independent; the store lock makes the payload claim first-wins -- a second waiter on the
   same binding+round gets the same outcome first line with an empty payload (already
   delivered), still `isError: false`. No second-waiter rejection, no serialization mutex.
7. **Cancellation.** The server keeps an in-flight map of `wait` request IDs to cancel funcs.
   A `notifications/cancelled` naming a live `wait` ID cancels that call's context (so
   `Wait`'s `select` on `ctx.Done()` stops polling); a cancelled call that already answered is
   a no-op. Builder verifies with evidence whether Claude Code 2.1.286 actually sends
   `notifications/cancelled` on `/tasks` stop; regardless, process EOF/SIGTERM still ends
   every wait via the `Serve` context.
8. **`send` + instructions.** In tools mode `send` stops appending the
   `run_in_background`/`relevo wait` bash block and instead points at the `wait` tool
   (keeping the served wait budget from `budgetOf`). `InstructionsTools` is rewritten: no
   background command, end the turn after `send` only when the model chooses to wait via the
   tool; document the outcome words, the `still-open` re-call, the progress keepalive, and
   that headless `claude -p` returns inline. Channel and opencode preludes untouched.
9. **Output cap.** Payload text longer than the MCP cap returns the outcome first line plus
   the path and the exact hint `relevo show <name> --round <n> --report` instead of the
   payload. Cap is a named byte constant derived from 25k tokens at ~4 chars/token (pin
   ~96 KiB; `MaxPushBytes` 64 KiB single-round text plus multi-round headers from
   `PullPendingThrough` can exceed it, which is exactly the trigger).
10. **`internal/relevo` helper.** `WaitOwned(ctx, rt, masterMindID, round, timeout, interval)`
    lists `Store.List()` filtered to `MasterMindID` (mirroring the `Status` filter), excluding
    `StateDone`, and calls `Wait` with those names; empty set is an error naming the
    MasterMind. Reuses `Wait`'s loop verbatim.

## Seams

- `internal/mcp/server.go` -- `Serve` loop 77-123; `handleLine` 140-179 (`tools/list` 167-170,
  `tools/call` 171-172); `toolCallParams`/`callMeta` 200-225 (extend with `progressToken`);
  `handleToolsCall` 227-240; `callTool` 255-322 (`send` branch + bash predicate 265-284);
  `toolResultFrom` 326-335; `writeResponse`/`writeLine` 337-355 (progress + responses share
  `writeMu`); `Push` 359-385 (untouched, channel only).
- `internal/mcp/tools.go` -- `WaitCommand` 91-96, `appendWaitCommand` 98-105
  (deleted/replaced); `Tools()` 107-161 (superseded by mode/kind-aware listing);
  `decodeArgs` 163-177 (reused); validators 179-208 (add `validateWaitArgs`: timeout parses,
  round >= 0).
- `internal/mcp/verbs.go` -- `Verbs` iface 18-24 (add `Wait`); `RelevoVerbs` 31-35 +
  `MasterMind` (the no-name identity; opencode never lists `wait` so no per-call session
  resolution is needed); `masterMindFor` 41-58; `Status` filter 61-100 (ownership pattern to
  mirror); `sendResult`/`waitBudget`/`budgetOf` 102-122 (budget reused for the tool pointer).
- `internal/mcp/instructions.go` -- `InstructionsTools` 48-97 (rewrite);
  `InstructionsFor` 135-143 (unchanged shape).
- `internal/mcp/gate.go` 1-107 -- pattern reference for a second verb file only; new wait
  verb lives in a new `internal/mcp/wait.go` (keeps files <=600 lines, functions <=70).
- `internal/relevo/wait.go` -- `WaitResult`/codes 18-58; `DefaultWaitRound` 77-88;
  `WaitOutcome` 94-131; `WaitOptions` 133-140; `waitDeliverable` 145-147; `Wait` 161-241
  (reused as-is; new `WaitOwned` beside it).
- `internal/delivery/pull.go` -- `PullPendingThrough` 98-153 (reused, unchanged).
- `internal/delivery/push.go` -- `MaxPushBytes` 14; `showCommand` 21-23 (hint format
  reference); `PushText` 94-111.
- `internal/store/lifecycle.go` -- `List` 29; `RoundTimeoutMS` default 229-230.
  `internal/store/binding.go` -- `MasterMindID` 109-112, `RoundTimeoutMS` 154.
- `cmd/relevo/mcp.go` -- `mcpVerbs` 236-246 (startup `MasterMind` wiring); `mcpResolveMode`
  222-230. `cmd/relevo/wait.go` -- reference only (`waitDocOf` 33-39), unchanged.
- Tests -- `internal/mcp/server_test.go` `fakeVerbs` 17-59, `runServer` 61-89;
  `internal/mcp/contract_test.go` `-update` flag 19, `TestContractToolsList` 50-64,
  `TestContractInstructions` 66-86; `internal/mcp/waitcmd_test.go` `callSend` 39-63,
  `TestMCPSendResultDependsOnMode` 65-93; `internal/relevo/wait_test.go` fake-clock pattern
  (`rt.Now` tick, e.g. 379-405), `routeRuntime`/`seedPending` (`route_helpers_test.go`),
  `sentBinding` (`fixture_test.go:51`).
- Goldens -- `internal/mcp/testdata/contract/`: `tools-list.golden`, `instructions.golden`,
  new `tool-wait.golden`. Regenerate with `go test ./internal/mcp -run Contract -update`,
  never hand-edit.
- Spec -- `docs/specs/2026-09-21-planner-channel-design.md` §2 line 46 (`A wait tool. The
  channel is the wait.`): one-line note that tools mode now has a blocking `wait` tool.
- Plan file -- `docs/plans/2026-10-02-wait-tool-mcp.md`.

## Ordered steps

1. Deliverable: `WaitOwned` in `internal/relevo` + unit tests reusing `Wait`'s loop
   (owned-set resolution, empty-set error, done-exclusion, round-0 passthrough). Worked:
   `go test ./internal/relevo/ -run 'TestWaitOwned|TestWait'` green.
2. Deliverable: new `internal/mcp/wait.go` (`WaitArgs`, schema, `RelevoVerbs.Wait`, result-text
   formatter, byte-cap + show-hint branch, timeout default/clamp) + unit tests over fake
   store/writer and fake clock. Worked: `go test ./internal/mcp/ -run TestRelevoVerbsWait`
   green.
3. Deliverable: server wiring -- mode/kind-aware tools listing, `wait` dispatch guard, per-call
   goroutine + progress emitter + `notifications/cancelled` registry -- plus tests for listing
   gating, async ping-during-wait, progress with/without token, and cancellation stopping the
   poll. Worked: `go test ./internal/mcp/` green.
4. Deliverable: `send` tool-pointer swap, `InstructionsTools` rewrite, regenerated goldens
   (`tools-list`, `instructions`, new `tool-wait`) via `-update`, one-line spec §2 note.
   Worked: golden diff shows only the intended hunks; `git diff --stat` matches declared scope.
5. Deliverable: plan saved to `docs/plans/2026-10-02-wait-tool-mcp.md` (fold the MasterMind
   amendment below into the saved file). Worked: file exists at that path.
6. Deliverable: full `make check` green (gofmt, vet, tidy, golangci, comments, filesize,
   coverage baseline -- regenerate baseline with `sh scripts/check-coverage.sh --write` only if
   code moved packages, and say so). Worked: `make check` passes; focused command was
   `go test ./internal/mcp/ ./internal/relevo/`.

Test-to-mutation mapping (each names its kill): list-gating kills removing the mode/kind gate;
closed-round call kills dropping `route=wait` confirmation; needs-you/isError-false kills
turning outcomes into RPC errors; progress with/without token kills always/never emitting;
no-name/named kills dropping the ownership filter; ping-during-wait kills running `wait` inline
in `handleLine`; send-pointer golden kills keeping the bash block; cap test kills raising the
cap past the fixture; bound-elapse normal result kills erroring on timeout; cancellation test
kills ignoring `notifications/cancelled`; default-budget test kills defaulting timeout to zero.

## Deleted behaviour (closed list)

1. The tools-mode `send` background-wait bash block (`appendWaitCommand` call,
   `server.go:278-283`) -- replaced by the `wait`-tool pointer carrying the same budget.
2. `WaitCommand`/`appendWaitCommand` helpers (`tools.go:91-105`) -- deleted if no other caller
   remains (only `server.go` uses them).
3. Background-wait prose in `InstructionsTools` (`instructions.go:51-73`) -- replaced by
   blocking-tool prose.
4. The unfiltered five-tool `Tools()` document as the served listing (`tools.go:107-161`) --
   superseded by the mode/kind-aware listing (kept only as the channel/opencode subset source if
   convenient).
5. Spec §2 non-goal line 46 is amended with the one-line note, not removed.

## MasterMind amendment (same round)

The plan above omits three spots that assert or teach the old background-shell route, plus one
explicit non-change:

1. `internal/e2e/headless_test.go` -- the tools-mode send->wait flow (the 8.1/8.2 assertion that
   the send result ends with the background wait, the `waitCommandRE`/`parseWaitCommand`
   helpers, and the 8.3 wait-through-CLI leg). When `send` stops appending the bash block this
   test fails. Update it to the new route: assert the send result points at the `wait` tool
   with the same budget, and drive the round's close through the wait-tool path (or the
   in-process `relevo.Wait` equivalent), keeping the delivery assertion (report delivered once,
   `route=wait`).
2. `internal/doctor/mastermind.go` plus `internal/doctor/mastermind_test.go` -- "reports arrive
   by background wait. For push, launch with `--channels`..." prose. Tools-mode reports now
   arrive via the blocking `wait` tool, not a background shell. Reword to the new route; keep
   the push/channel half intact.
3. `claude-plugin/skills/planner-loop/SKILL.md` step 2 -- teaches
   `relevo wait --name <n> --timeout <budget>` via run_in_background with exit codes. Rewrite
   the step for the `wait` MCP tool (outcome words, still-open re-call); keep the `relevo wait`
   CLI mention only as the no-MCP-tools fallback. Update any tests that render or assert this
   skill.
4. Explicit non-change: `internal/doctor/usage.go` (an opencode mastermind's reports wait for the
   background wait) stays -- opencode never lists the tool and its fallback is still the CLI
   wait.

## Report must include

Evidence answers (cancelled sent on `/tasks` stop? progress cadence observed vs idle; timeout
default + both clamps; bound-elapse text), the concurrency decision as built, the cap constant
with the 25k-token math, goldens regenerated via `-update` (no hand-edits), `make check` result
and any coverage-baseline regeneration, `git diff --stat` vs declared scope, and confirmation that
`cmd/relevo` and `relevo wait` CLI are untouched. Plus the amendment extras: e2e result for the
updated flow, doctor test updates, skill-render confirmation.